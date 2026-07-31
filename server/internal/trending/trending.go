// Package trending periodically fetches Douban's trending Western movie and
// US-TV lists and caches the titles, feeding the homepage's quick-search
// chips.
//
// Douban has no official API; this uses the JSON endpoints its own frontend
// calls (movie.douban.com/j/search_subjects and /j/subject_abstract). The
// list endpoint only carries Chinese titles, so each subject's English name
// is resolved through one subject_abstract call, cached by subject ID —
// steady-state cost is a couple of requests per interval for whatever newly
// entered the charts. Every failure mode degrades to "keep serving the last
// good list", and an empty snapshot just means the homepage renders no
// trending section.
package trending

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultBaseURL  = "https://movie.douban.com"
	defaultInterval = time.Hour
	defaultLimit    = 10
	fetchTimeout    = 20 * time.Second
	// abstractDelay spaces out the per-subject metadata requests so a cold
	// start doesn't burst-hammer Douban.
	abstractDelay = 500 * time.Millisecond
	// maxBody bounds how much of a response is read; the real payloads are a
	// few KB, so 1 MB means "something is very wrong upstream".
	maxBody = 1 << 20
	// browserUA is required: Douban serves these JSON endpoints to browsers
	// and rejects obvious non-browser clients.
	browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Config tunes the fetcher. Zero values pick the defaults above.
type Config struct {
	// Interval between refreshes.
	Interval time.Duration
	// Limit is how many titles to keep per list.
	Limit int
	// BaseURL overrides the Douban origin (tests point it at a stub).
	BaseURL string
	Logger  *log.Logger
}

// Snapshot is the current trending state. Slices are never nil.
type Snapshot struct {
	Movies    []string
	TV        []string
	UpdatedAt time.Time
}

// Service fetches and caches the trending lists.
type Service struct {
	cfg    Config
	client *http.Client

	mu   sync.RWMutex
	snap Snapshot

	// names caches subject ID -> resolved English title so refreshes only
	// pay the subject_abstract call for subjects new to the charts. Only
	// successful resolutions are cached; failures retry next interval.
	names map[string]string
}

// New builds a Service; call Run to start refreshing.
func New(cfg Config) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Service{
		cfg:    cfg,
		client: &http.Client{Timeout: fetchTimeout},
		names:  make(map[string]string),
	}
}

// Get returns the latest snapshot. Empty lists mean no successful fetch yet.
func (s *Service) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Run refreshes immediately and then every Interval until ctx is done.
func (s *Service) Run(ctx context.Context) {
	s.refresh(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refresh(ctx)
		}
	}
}

// refresh fetches both lists. A list that fails keeps its previous value, so
// one bad response never blanks the homepage section. Movies use Douban's
// 欧美 tag, TV the 美剧 tag (the tv type has no 欧美 tag).
func (s *Service) refresh(ctx context.Context) {
	movies, errM := s.fetchList(ctx, "movie", "欧美")
	tv, errT := s.fetchList(ctx, "tv", "美剧")
	if errM != nil {
		s.cfg.Logger.Printf("trending: movie fetch: %v", errM)
	}
	if errT != nil {
		s.cfg.Logger.Printf("trending: tv fetch: %v", errT)
	}
	if errM != nil && errT != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errM == nil {
		s.snap.Movies = movies
	}
	if errT == nil {
		s.snap.TV = tv
	}
	s.snap.UpdatedAt = time.Now()
}

type subject struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// fetchList pulls one chart and returns its titles, resolved to English
// names where possible.
func (s *Service) fetchList(ctx context.Context, typ, tag string) ([]string, error) {
	u := fmt.Sprintf("%s/j/search_subjects?type=%s&tag=%s&page_limit=%d&page_start=0",
		s.cfg.BaseURL, typ, url.QueryEscape(tag), s.cfg.Limit)
	body, err := s.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Subjects []subject `json:"subjects"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(payload.Subjects) == 0 {
		// A 200 with no subjects usually means Douban changed the response
		// shape or is soft-blocking; treat as failure to keep the last list.
		return nil, fmt.Errorf("no subjects in response")
	}
	titles := make([]string, 0, len(payload.Subjects))
	for _, sub := range payload.Subjects {
		if sub.Title == "" {
			continue
		}
		titles = append(titles, s.englishTitle(ctx, sub))
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("no titles in response")
	}
	return titles, nil
}

// englishTitle resolves a subject's English name via subject_abstract,
// falling back to the Chinese chart title when resolution fails.
func (s *Service) englishTitle(ctx context.Context, sub subject) string {
	s.mu.RLock()
	cached, ok := s.names[sub.ID]
	s.mu.RUnlock()
	if ok {
		return cached
	}
	time.Sleep(abstractDelay)
	u := fmt.Sprintf("%s/j/subject_abstract?subject_id=%s", s.cfg.BaseURL, url.QueryEscape(sub.ID))
	body, err := s.get(ctx, u)
	if err != nil {
		s.cfg.Logger.Printf("trending: abstract %s: %v", sub.ID, err)
		return sub.Title
	}
	var payload struct {
		Subject struct {
			Title string `json:"title"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.cfg.Logger.Printf("trending: abstract %s: decode: %v", sub.ID, err)
		return sub.Title
	}
	name := extractEnglish(payload.Subject.Title, sub.Title)
	if name == "" {
		// Chinese-only production (or an unexpected format): the chart title
		// is the best keyword there is. Cache it — retrying won't improve it.
		name = sub.Title
	}
	s.mu.Lock()
	s.names[sub.ID] = name
	s.mu.Unlock()
	return name
}

// yearSuffix matches the trailing " (2026)" on abstract titles.
var yearSuffix = regexp.MustCompile(`\s*\(\d{4}\)\s*$`)

// hasLetter requires at least one ASCII letter for a string to count as an
// English name.
var hasLetter = regexp.MustCompile(`[A-Za-z]`)

// extractEnglish pulls the original English name out of an abstract title of
// the form "中文名 English Name‎ (2026)", given the chart's Chinese title.
// Returns "" when there is no such name.
func extractEnglish(full, chinese string) string {
	full = yearSuffix.ReplaceAllString(full, "")
	var name string
	if chinese != "" && strings.HasPrefix(full, chinese) {
		// Exact prefix strip. This is the reliable path: a Chinese title
		// that ends in digits ("疯狂动物城2 Zootopia 2") defeats any
		// per-rune classification.
		name = full[len(chinese):]
	} else {
		// Chart and abstract titles disagree; fall back to everything after
		// the last CJK rune.
		cut := 0
		for i, r := range full {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				cut = i + len(string(r))
			}
		}
		name = full[cut:]
	}
	// Trim whitespace and invisible format runes — Douban embeds a U+200E
	// LEFT-TO-RIGHT MARK after the original name (Cf, not graphic).
	name = strings.TrimFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsGraphic(r)
	})
	if !hasLetter.MatchString(name) {
		return ""
	}
	return name
}

// get performs one browser-shaped GET and returns the body.
func (s *Service) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://movie.douban.com/")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
