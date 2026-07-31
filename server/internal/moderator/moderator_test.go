package moderator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dhtsearch/server/internal/store"
)

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *store.Store, names ...string) {
	t.Helper()
	for i, n := range names {
		if err := s.Upsert(store.Torrent{
			InfoHash: string(rune('a'+i)) + "hash", Name: n,
			TotalSize: 1 << 30, FileCount: 1, CreatedAt: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeAPI serves an OpenAI-compatible endpoint labelling by title substring.
func fakeAPI(t *testing.T, label func(title string) string, calls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		type v struct {
			I          int     `json:"i"`
			Category   string  `json:"category"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
		}
		var items []promptItem
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &items); err != nil {
			t.Fatalf("listing is not JSON: %v (%q)", err, req.Messages[1].Content)
		}
		var out []v
		for _, it := range items {
			out = append(out, v{I: it.I, Category: label(it.Title), Confidence: 0.9, Reason: "test reason"})
		}
		content, _ := json.Marshal(map[string]any{"verdicts": out})
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: string(content)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newMod(t *testing.T, st *store.Store, url string, tweak func(*Config)) *Moderator {
	t.Helper()
	cfg := Config{
		BaseURL: url, APIKey: "test-key", Model: "deepseek-v4-flash",
		BatchSize: 10, MaxBatches: 10, Logger: discardLogger(),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	m, err := New(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSweepClassifiesAdultAndSpamKeepsAllRows(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Big Buck Bunny 1080p", "Hot XXX Collection", "FREE CRACK DOWNLOAD click here", "Ubuntu 24.04 ISO")

	srv := fakeAPI(t, func(line string) string {
		switch {
		case strings.Contains(line, "XXX"):
			return "adult"
		case strings.Contains(line, "CRACK"):
			return "spam"
		}
		return "ok"
	}, nil)

	s, err := newMod(t, st, srv.URL, nil).SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Reviewed != 4 || s.Adult != 1 || s.Spam != 1 || s.Classified != 4 {
		t.Fatalf("summary = %+v", s)
	}

	items, total, err := st.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("safe results = %d, want 2", total)
	}
	for _, it := range items {
		if strings.Contains(it.Name, "XXX") || strings.Contains(it.Name, "CRACK") {
			t.Fatalf("flagged torrent survived: %q", it.Name)
		}
	}
	if _, total, err := st.Search(t.Context(), "", 1, 10, store.SearchFilter{Category: store.CategoryAdult}); err != nil || total != 1 {
		t.Fatalf("adult results = %d, err=%v; want 1", total, err)
	}
	if _, total, err := st.Search(t.Context(), "", 1, 10, store.SearchFilter{Category: store.CategorySpam}); err != nil || total != 1 {
		t.Fatalf("spam results = %d, err=%v; want 1", total, err)
	}
	if total, _ := st.Count(t.Context()); total != 4 {
		t.Fatalf("stored rows = %d, want 4", total)
	}
}

func TestReviewedRowsAreNotReclassified(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Sintel 2010", "Tears of Steel")
	var calls int32
	srv := fakeAPI(t, func(string) string { return "ok" }, &calls)
	m := newMod(t, st, srv.URL, nil)

	if _, err := m.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := atomic.LoadInt32(&calls)
	if _, err := m.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != first {
		t.Fatalf("second sweep made %d extra API calls, want 0", got-first)
	}
	if n, _ := st.PendingReviewCount(t.Context()); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestClassifiedTorrentIsRetainedAcrossRediscovery(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Hot XXX Collection")
	srv := fakeAPI(t, func(string) string { return "adult" }, nil)
	if _, err := newMod(t, st, srv.URL, nil).SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The crawler re-discovers the same infohash.
	if err := st.Upsert(store.Torrent{
		InfoHash: "ahash", Name: "Hot XXX Collection", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 999,
	}); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := st.Search(t.Context(), "", 1, 10); total != 0 {
		t.Fatalf("adult torrent leaked into safe results (total=%d)", total)
	}
	if _, total, _ := st.Search(t.Context(), "", 1, 10, store.SearchFilter{Category: store.CategoryAdult}); total != 1 {
		t.Fatalf("adult results = %d, want 1", total)
	}
	if n, _ := st.BlockedCount(t.Context()); n != 0 {
		t.Fatalf("blocked count = %d, want 0", n)
	}
}

func TestDryRunDeletesNothing(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Hot XXX Collection")
	srv := fakeAPI(t, func(string) string { return "adult" }, nil)
	m := newMod(t, st, srv.URL, func(c *Config) { c.DryRun = true })

	s, err := m.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Adult != 1 || s.Classified != 0 {
		t.Fatalf("summary = %+v, want Adult=1 Classified=0", s)
	}
	if _, total, _ := st.Search(t.Context(), "", 1, 10); total != 1 {
		t.Fatalf("dry run deleted rows (total=%d)", total)
	}
	if n, _ := st.PendingReviewCount(t.Context()); n != 1 {
		t.Fatalf("dry run marked row reviewed (pending=%d)", n)
	}
}

// An API failure must leave rows unreviewed so the next sweep retries them.
func TestAPIErrorLeavesRowsUnreviewed(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Sintel 2010")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := newMod(t, st, srv.URL, nil).SweepOnce(context.Background()); err == nil {
		t.Fatal("expected error from 401")
	}
	if n, _ := st.PendingReviewCount(t.Context()); n != 1 {
		t.Fatalf("pending = %d, want 1 (row must be retried)", n)
	}
	if _, total, _ := st.Search(t.Context(), "", 1, 10); total != 1 {
		t.Fatalf("row deleted on API error (total=%d)", total)
	}
}

func TestRetriesOn429ThenSucceeds(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Sintel 2010")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		content, _ := json.Marshal(map[string]any{"verdicts": []map[string]any{{"i": 1, "label": "ok"}}})
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: string(content)}}}})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := newMod(t, st, srv.URL, nil).SweepOnce(ctx); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

// Unknown labels and missing entries must default to "ok" — never delete on doubt.
func TestUnknownAndMissingLabelsDefaultToOK(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Movie A", "Movie B", "Movie C")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verdict for #1 is nonsense, #2 is out of range, #3 is omitted.
		content := `{"verdicts":[{"i":1,"label":"probably-bad"},{"i":99,"label":"adult"}]}`
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: content}}}})
	}))
	defer srv.Close()

	s, err := newMod(t, st, srv.URL, nil).SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Classified != 3 {
		t.Fatalf("classified %d rows, want 3", s.Classified)
	}
	if _, total, _ := st.Search(t.Context(), "", 1, 10); total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
}

func TestStripFences(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"{\"a\":1}", `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
	} {
		if got := stripFences(tc.in); got != tc.want {
			t.Errorf("stripFences(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewRejectsMissingConfig(t *testing.T) {
	st := newStore(t)
	for _, c := range []Config{
		{Model: "m", BaseURL: "u"},
		{APIKey: "k", BaseURL: "u"},
		{APIKey: "k", Model: "m"},
	} {
		if _, err := New(st, c); err == nil {
			t.Errorf("New(%+v) succeeded, want error", c)
		}
	}
}
