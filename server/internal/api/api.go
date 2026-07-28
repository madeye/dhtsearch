// Package api exposes the search HTTP API (JSON only, GET + CORS).
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dhtsearch/server/internal/store"
)

const (
	maxQueryLen    = 200
	maxPageSize    = 100
	defaultPerPage = 20
	// maxOffset caps how deep pagination can reach: OFFSET n makes SQLite
	// walk and discard n rows, so an unbounded page number turns one GET
	// into a full-table scan. Nothing legitimate paginates 10k results deep.
	maxOffset = 10_000
	// admissionWait is how long a search waits for an in-flight slot before
	// being turned away. Long enough to absorb a burst, short enough that a
	// flood fails fast instead of stacking goroutines.
	admissionWait = time.Second

	defaultMaxInflight   = 16
	defaultSearchTimeout = 10 * time.Second
	defaultStatsTTL      = 10 * time.Second
)

// Options tunes the API's DoS defenses. Zero values pick safe defaults,
// except RateRPS where <= 0 disables per-client rate limiting entirely
// (tests, fully trusted networks).
type Options struct {
	// RateRPS is the sustained per-client request rate; RateBurst is the
	// bucket size (<= 0: 10 seconds' worth of RateRPS).
	RateRPS   float64
	RateBurst int
	// MaxInflight caps concurrent search queries. The store has one SQLite
	// connection, so extra concurrency only queues; this bounds the queue.
	MaxInflight int
	// SearchTimeout bounds one request's total database time.
	SearchTimeout time.Duration
	// StatsTTL is how long stats and total-count responses are served from
	// cache. Stats hit four aggregate queries; caching makes them O(1).
	StatsTTL time.Duration
	// ScraperStatus, when non-nil, adds a "scraper" section to /api/stats.
	ScraperStatus func() ScraperStatus
}

// CrawlerStatus describes the crawler for the stats endpoint. The sampler
// fields are the ones to watch when discovery looks slow: Nodes is the pool
// the samplers draw from, and Harvested/Sampled is the average yield per
// answered query.
type CrawlerStatus struct {
	Enabled   bool  `json:"enabled"`
	Seen      int64 `json:"seen_infohashes"`
	Nodes     int   `json:"nodes"`
	Queued    int   `json:"queued_nodes"`
	Sampled   int64 `json:"sampled"`
	SampleErr int64 `json:"sample_errors"`
	Harvested int64 `json:"harvested"`
}

// ScraperStatus describes the tracker-scrape prioritizer. Seeded/Scraped is
// the share of discovered hashes with at least one tracker-known seeder;
// Evicted counts hashes that lost their fetch slot to better-seeded ones.
type ScraperStatus struct {
	Queue      int   `json:"queue"`
	Scraped    int64 `json:"scraped"`
	Seeded     int64 `json:"seeded"`
	ScrapeErrs int64 `json:"scrape_errors"`
	Dropped    int64 `json:"dropped"`
	Evicted    int64 `json:"evicted"`
}

// Server serves the search API.
type Server struct {
	st      *store.Store
	crawler func() CrawlerStatus
	logger  *log.Logger
	mux     *http.ServeMux
	opts    Options

	limiter   *ipLimiter    // nil when rate limiting is disabled
	searchSem chan struct{} // admission gate for search queries

	// Cached /api/stats body and empty-query total, both refreshed at most
	// once per StatsTTL. The mutexes double as single-flight: one request
	// pays for the refresh while the rest wait for its result.
	statsMu   sync.Mutex
	statsBody []byte
	statsAt   time.Time
	countMu   sync.Mutex
	countVal  int
	countAt   time.Time
}

// New builds the HTTP handler. crawlerStatus may be nil.
func New(st *store.Store, crawlerStatus func() CrawlerStatus, logger *log.Logger, opts Options) *Server {
	if logger == nil {
		logger = log.Default()
	}
	if opts.MaxInflight <= 0 {
		opts.MaxInflight = defaultMaxInflight
	}
	if opts.SearchTimeout <= 0 {
		opts.SearchTimeout = defaultSearchTimeout
	}
	if opts.StatsTTL <= 0 {
		opts.StatsTTL = defaultStatsTTL
	}
	s := &Server{
		st:        st,
		crawler:   crawlerStatus,
		logger:    logger,
		opts:      opts,
		searchSem: make(chan struct{}, opts.MaxInflight),
	}
	if opts.RateRPS > 0 {
		burst := opts.RateBurst
		if burst <= 0 {
			burst = int(opts.RateRPS*10) + 1
		}
		s.limiter = newIPLimiter(opts.RateRPS, burst)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/healthz", s.handleHealth)
	s.mux = mux
	return s
}

// Handler returns the root handler with CORS/OPTIONS support and per-client
// rate limiting. The health check is exempt so monitoring and the deploy
// script never get throttled behind a flood.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if s.limiter != nil && r.URL.Path != "/api/healthz" && !s.limiter.allow(clientKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

type resultItem struct {
	InfoHash  string `json:"info_hash"`
	Name      string `json:"name"`
	TotalSize int64  `json:"total_size"`
	FileCount int    `json:"file_count"`
	Files     any    `json:"files"`
	Magnet    string `json:"magnet"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := truncateUTF8(r.URL.Query().Get("q"), maxQueryLen)
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoiDefault(r.URL.Query().Get("page_size"), defaultPerPage)
	if pageSize < 1 {
		pageSize = defaultPerPage
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if (page-1)*pageSize > maxOffset {
		page = maxOffset/pageSize + 1
	}

	// Admission gate: the store serializes queries on one connection, so
	// when all slots are busy piling on more requests only grows the queue.
	// Wait briefly for a slot, then shed load instead of stacking up.
	select {
	case s.searchSem <- struct{}{}:
		defer func() { <-s.searchSem }()
	case <-r.Context().Done():
		return
	case <-time.After(admissionWait):
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "server busy")
		return
	}

	// The context bounds database time and dies with the client connection,
	// so queries for hung-up clients stop consuming the SQLite handle.
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.SearchTimeout)
	defer cancel()

	var (
		items []store.Torrent
		total int
		err   error
	)
	if strings.TrimSpace(q) == "" {
		// The landing page. Latest walks the created_at index instead of
		// counting matches, and the total comes from the shared cache.
		items, err = s.st.Latest(ctx, page, pageSize)
		if err == nil {
			total, err = s.cachedCount(ctx)
		}
	} else {
		items, total, err = s.st.Search(ctx, q, page, pageSize)
	}
	if err != nil {
		if ctx.Err() != nil {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "search timed out")
		} else {
			writeError(w, http.StatusInternalServerError, "search failed")
		}
		s.logger.Printf("api: search: %v", err)
		return
	}
	results := make([]resultItem, 0, len(items))
	for _, t := range items {
		results = append(results, resultItem{
			InfoHash:  t.InfoHash,
			Name:      t.Name,
			TotalSize: t.TotalSize,
			FileCount: t.FileCount,
			Files:     t.Files,
			Magnet:    magnetURI(t.InfoHash, t.Name),
			CreatedAt: t.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"results":   results,
	})
}

// cachedCount returns the total torrent count, at most StatsTTL stale.
// COUNT(*) walks a full index, so the landing page must not pay for it per
// request. The lock is held across the query on purpose: it makes concurrent
// misses single-flight.
func (s *Server) cachedCount(ctx context.Context) (int, error) {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	if !s.countAt.IsZero() && time.Since(s.countAt) < s.opts.StatsTTL {
		return s.countVal, nil
	}
	n, err := s.st.Count(ctx)
	if err != nil {
		return 0, err
	}
	s.countVal, s.countAt = n, time.Now()
	return n, nil
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Single-flight with a TTL: stats aggregate four queries (two of them
	// full scans), so one refresh per TTL serves everyone. The lock being
	// held across the refresh is what keeps a stats flood down to one
	// query stream.
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.statsBody != nil && time.Since(s.statsAt) < s.opts.StatsTTL {
		writeRawJSON(w, http.StatusOK, s.statsBody)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.SearchTimeout)
	defer cancel()

	total, err := s.st.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	counters, err := s.st.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	blocked, err := s.st.BlockedCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	pending, err := s.st.PendingReviewCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	resp := map[string]any{
		"torrents":       total,
		"seen":           counters["seen"],
		"fetched":        counters["fetched"],
		"adult_filtered": counters["adult_filtered"],
		"spam_filtered":  counters["spam_filtered"],
		"size_filtered":  counters["size_filtered"],
		// Metadata fetch outcomes. A timeout share near 100% means the
		// fetch pool, not discovery, is what caps indexing throughput.
		"fetch": map[string]any{
			"fetched":   counters["fetched"],
			"timed_out": counters["meta_timed_out"],
			"failed":    counters["meta_failed"],
		},
		"moderation": map[string]any{
			"reviewed":      counters["llm_reviewed"],
			"adult_removed": counters["llm_adult_removed"],
			"spam_removed":  counters["llm_spam_removed"],
			"errors":        counters["llm_errors"],
			"pending":       pending,
			"blocked":       blocked,
		},
	}
	if s.crawler != nil {
		resp["crawler"] = s.crawler()
	}
	if s.opts.ScraperStatus != nil {
		resp["scraper"] = s.opts.ScraperStatus()
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	s.statsBody, s.statsAt = body, time.Now()
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func magnetURI(hash, name string) string {
	return "magnet:?xt=urn:btih:" + hash + "&dn=" + url.QueryEscape(name)
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// truncateUTF8 cuts s to at most n bytes without splitting a multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeRawJSON(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
