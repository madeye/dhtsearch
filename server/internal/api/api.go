// Package api exposes the search HTTP API (JSON only, GET + CORS).
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"dhtsearch/server/internal/store"
)

const (
	maxQueryLen    = 200
	maxPageSize    = 100
	defaultPerPage = 20
)

// CrawlerStatus describes the crawler for the stats endpoint.
type CrawlerStatus struct {
	Enabled bool  `json:"enabled"`
	Seen    int64 `json:"seen_infohashes"`
}

// Server serves the search API.
type Server struct {
	st      *store.Store
	crawler func() CrawlerStatus
	logger  *log.Logger
	mux     *http.ServeMux
}

// New builds the HTTP handler. crawlerStatus may be nil.
func New(st *store.Store, crawlerStatus func() CrawlerStatus, logger *log.Logger) *Server {
	s := &Server{st: st, crawler: crawlerStatus, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/healthz", s.handleHealth)
	s.mux = mux
	return s
}

// Handler returns the root handler with CORS/OPTIONS support.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
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
	q := r.URL.Query().Get("q")
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
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

	items, total, err := s.st.Search(q, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
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

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	total, err := s.st.Count()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	counters, err := s.st.Stats()
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
	}
	if s.crawler != nil {
		resp["crawler"] = s.crawler()
	}
	writeJSON(w, http.StatusOK, resp)
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
