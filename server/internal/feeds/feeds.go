// Package feeds polls publisher RSS feeds (dmhy, Nyaa) for infohashes and
// their published titles.
//
// The DHT crawler only finds a torrent if BEP 51 sampling happens to land on
// it, which a single fansub pack with a few dozen seeders rarely does; and
// even once indexed, a release whose torrent name is romaji ("Jigoku ni
// Ochiru Wa Yo") cannot be found by its Chinese title (地狱占星师), which
// only exists on the publisher's page. Feeds closes both gaps: every item's
// infohash goes straight to the metadata fetcher ahead of DHT discoveries,
// and its title is stored as the torrent's alias, which search matches.
//
// Besides fixed feeds, an optional keyword source (the homepage's trending
// 日剧/韩剧 chips) is searched through a keyword feed URL, so a chip's
// Chinese title finds older releases that have scrolled off the fixed feeds.
package feeds

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultInterval = 30 * time.Minute
	defaultBuffer   = 1024
	fetchTimeout    = 30 * time.Second
	// retryAfter is how long a queued hash waits before it is queued again:
	// a metadata fetch that timed out may well land later, once more peers
	// are online. maxAttempts bounds that, so a dead swarm or a torrent the
	// content filter rejects is not retried forever.
	retryAfter  = 6 * time.Hour
	maxAttempts = 3
	// forgetAfter drops bookkeeping for hashes no feed has listed for this
	// long, keeping memory bounded on a long-running process.
	forgetAfter = 7 * 24 * time.Hour
	maxBody     = 8 << 20
	browserUA   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// keywordDelay spaces out keyword searches so a poll is not a burst of
// requests against one site. A var so tests can shorten it.
var keywordDelay = 2 * time.Second

// Store is the slice of the store the feeds need.
type Store interface {
	// SetAlias records alias on an indexed torrent and reports whether the
	// torrent exists.
	SetAlias(infoHash, alias string) (bool, error)
	IsBlocked(infoHash string) (bool, error)
}

// Config tunes the poller. Zero values pick the defaults above.
type Config struct {
	// Feeds are RSS URLs polled every Interval.
	Feeds []string
	// KeywordURL is an RSS search URL with one %s for the query-escaped
	// keyword. Empty disables keyword searches.
	KeywordURL string
	// Keywords returns the current search keywords (nil: none).
	Keywords func() []string
	Interval time.Duration
	// Buffer is the capacity of the Out channel.
	Buffer int
	Store  Store
	Logger *log.Logger
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// Stats counts poller activity.
type Stats struct {
	Polls   int64 `json:"polls"`
	Items   int64 `json:"items"`   // feed items with a valid infohash
	Queued  int64 `json:"queued"`  // hashes sent to the fetcher
	Aliased int64 `json:"aliased"` // indexed torrents given an alias
	Errors  int64 `json:"errors"`  // failed feed requests
}

type entry struct {
	alias    string
	attempts int
	lastTry  time.Time
	lastSeen time.Time
	done     bool // aliased or blocked: nothing left to do
}

// Service polls the feeds.
type Service struct {
	cfg Config
	out chan string

	mu      sync.Mutex
	entries map[string]*entry
	stats   Stats
	now     func() time.Time
}

// New builds a Service; call Run to start polling.
func New(cfg Config) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Buffer <= 0 {
		cfg.Buffer = defaultBuffer
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: fetchTimeout}
	}
	return &Service{
		cfg:     cfg,
		out:     make(chan string, cfg.Buffer),
		entries: make(map[string]*entry),
		now:     time.Now,
	}
}

// Out carries hex infohashes to fetch.
func (s *Service) Out() <-chan string { return s.out }

// Alias returns the feed title for infoHash, or "" if no feed listed it.
func (s *Service) Alias(infoHash string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[infoHash]; ok {
		return e.alias
	}
	return ""
}

// Stats returns a snapshot of the counters.
func (s *Service) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Run polls immediately and then every Interval until ctx is done.
func (s *Service) Run(ctx context.Context) {
	s.poll(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.poll(ctx)
		}
	}
}

func (s *Service) poll(ctx context.Context) {
	urls := append([]string(nil), s.cfg.Feeds...)
	if s.cfg.KeywordURL != "" && s.cfg.Keywords != nil {
		for _, kw := range s.cfg.Keywords() {
			if kw = strings.TrimSpace(kw); kw != "" {
				urls = append(urls, fmt.Sprintf(s.cfg.KeywordURL, url.QueryEscape(kw)))
			}
		}
	}
	for i, u := range urls {
		if i >= len(s.cfg.Feeds) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(keywordDelay):
			}
		}
		items, err := s.fetch(ctx, u)
		if err != nil {
			s.cfg.Logger.Printf("feeds: %s: %v", u, err)
			s.mu.Lock()
			s.stats.Errors++
			s.mu.Unlock()
			continue
		}
		for _, it := range items {
			s.handle(it)
		}
	}
	s.mu.Lock()
	s.stats.Polls++
	cutoff := s.now().Add(-forgetAfter)
	for h, e := range s.entries {
		if e.lastSeen.Before(cutoff) {
			delete(s.entries, h)
		}
	}
	s.mu.Unlock()
}

// handle routes one feed item: alias it if indexed, otherwise queue a fetch.
func (s *Service) handle(it Item) {
	now := s.now()
	s.mu.Lock()
	s.stats.Items++
	e, ok := s.entries[it.InfoHash]
	if !ok {
		e = &entry{}
		s.entries[it.InfoHash] = e
	}
	e.lastSeen = now
	if e.alias == "" {
		e.alias = it.Title
	}
	if e.done {
		s.mu.Unlock()
		return
	}
	alias := e.alias
	s.mu.Unlock()

	// Store calls run unlocked: Alias is read from the fetch callback, which
	// must not wait on SQLite behind this lock.
	if exists, err := s.cfg.Store.SetAlias(it.InfoHash, alias); err != nil {
		s.cfg.Logger.Printf("feeds: set alias %s: %v", it.InfoHash, err)
		return
	} else if exists {
		s.mu.Lock()
		e.done = true
		s.stats.Aliased++
		s.mu.Unlock()
		return
	}
	if blocked, err := s.cfg.Store.IsBlocked(it.InfoHash); err != nil {
		s.cfg.Logger.Printf("feeds: blocked check %s: %v", it.InfoHash, err)
		return
	} else if blocked {
		s.mu.Lock()
		e.done = true
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e.attempts >= maxAttempts || (e.attempts > 0 && now.Sub(e.lastTry) < retryAfter) {
		return
	}
	select {
	case s.out <- it.InfoHash:
		e.attempts++
		e.lastTry = now
		s.stats.Queued++
	default:
		// Fetcher backlog full: leave the attempt unspent, the next poll
		// offers it again.
	}
}

// Item is one feed entry with a usable infohash.
type Item struct {
	InfoHash string // lowercase hex
	Title    string
}

func (s *Service) fetch(ctx context.Context, u string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := s.cfg.Client.Do(req)
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
	return Parse(body)
}

type rssItem struct {
	Title     string `xml:"title"`
	Link      string `xml:"link"`
	Enclosure struct {
		URL string `xml:"url,attr"`
	} `xml:"enclosure"`
	// Nyaa's namespaced <nyaa:infoHash>; encoding/xml matches the local name.
	InfoHash string `xml:"infoHash"`
}

// Parse extracts items from an RSS document. The infohash comes from
// <nyaa:infoHash>, or else from a magnet link in the enclosure or link
// (dmhy uses base32 btih). Items without one are skipped.
func Parse(body []byte) ([]Item, error) {
	var doc struct {
		Items []rssItem `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode rss: %w", err)
	}
	var items []Item
	for _, ri := range doc.Items {
		h := normalizeHash(ri.InfoHash)
		if h == "" {
			h = magnetHash(ri.Enclosure.URL)
		}
		if h == "" {
			h = magnetHash(ri.Link)
		}
		title := strings.Join(strings.Fields(ri.Title), " ")
		if h == "" || title == "" {
			continue
		}
		items = append(items, Item{InfoHash: h, Title: title})
	}
	return items, nil
}

var btihRe = regexp.MustCompile(`(?i)urn:btih:([0-9a-z]+)`)

func magnetHash(s string) string {
	m := btihRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return normalizeHash(m[1])
}

// normalizeHash accepts a 40-char hex or 32-char base32 v1 infohash and
// returns it as lowercase hex, or "" if it is neither.
func normalizeHash(s string) string {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 40:
		if _, err := hex.DecodeString(s); err == nil {
			return strings.ToLower(s)
		}
	case 32:
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(s)); err == nil {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

// Prioritize merges hi and lo into one channel, always draining hi first,
// so feed hashes take the next free fetch worker ahead of the DHT backlog.
// Either input may be nil. The output closes when ctx is done.
func Prioritize(ctx context.Context, hi, lo <-chan string) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		for {
			var v string
			var ok bool
			select {
			case v, ok = <-hi:
				if !ok {
					hi = nil
					continue
				}
			default:
				select {
				case <-ctx.Done():
					return
				case v, ok = <-hi:
					if !ok {
						hi = nil
						continue
					}
				case v, ok = <-lo:
					if !ok {
						lo = nil
						continue
					}
				}
			}
			select {
			case out <- v:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
