// Package trending periodically fetches Douban's trending movie and TV
// lists and caches the titles, feeding the homepage's quick-search chips.
//
// Douban has no official API; this uses the JSON endpoint its own frontend
// calls (movie.douban.com/j/search_subjects). One request per list per
// interval is far below any plausible abuse threshold, but the endpoint can
// still change or disappear at any time — so every failure mode degrades to
// "keep serving the last good list", and an empty snapshot just means the
// homepage renders no trending section.
package trending

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	defaultBaseURL  = "https://movie.douban.com"
	defaultInterval = time.Hour
	defaultLimit    = 10
	fetchTimeout    = 20 * time.Second
	// maxBody bounds how much of a response is read; the real payload for 10
	// subjects is ~4 KB, so 1 MB means "something is very wrong upstream".
	maxBody = 1 << 20
	// browserUA is required: Douban serves the JSON endpoint to browsers and
	// rejects obvious non-browser clients.
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
// one bad response never blanks the homepage section.
func (s *Service) refresh(ctx context.Context) {
	movies, errM := s.fetchList(ctx, "movie")
	tv, errT := s.fetchList(ctx, "tv")
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

// fetchList pulls one 热门 list ("movie" or "tv") and returns its titles.
func (s *Service) fetchList(ctx context.Context, typ string) ([]string, error) {
	u := fmt.Sprintf("%s/j/search_subjects?type=%s&tag=%s&page_limit=%d&page_start=0",
		s.cfg.BaseURL, typ, url.QueryEscape("热门"), s.cfg.Limit)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Subjects []struct {
			Title string `json:"title"`
		} `json:"subjects"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	titles := make([]string, 0, len(payload.Subjects))
	for _, sub := range payload.Subjects {
		if sub.Title != "" {
			titles = append(titles, sub.Title)
		}
	}
	if len(titles) == 0 {
		// A 200 with no subjects usually means Douban changed the response
		// shape or is soft-blocking; treat as failure to keep the last list.
		return nil, fmt.Errorf("no titles in response")
	}
	return titles, nil
}
