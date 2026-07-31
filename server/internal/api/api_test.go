package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	return testServerOpts(t, Options{})
}

func testServerOpts(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, name := range []string{"Big Buck Bunny", "Sintel", "Ubuntu 24.04"} {
		if err := st.Upsert(store.Torrent{
			InfoHash:  strings.Repeat(string(rune('a'+i)), 40),
			Name:      name,
			TotalSize: int64(1+i) << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: name + ".mkv", Size: int64(1+i) << 30}},
			CreatedAt: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, func() CrawlerStatus {
		return CrawlerStatus{Enabled: false, Seen: 42}
	}, nil, opts).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d", url, resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%s: missing CORS header", url)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHealthz(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/healthz")
	if m["ok"] != true {
		t.Fatalf("healthz: %v", m)
	}
}

func TestSearch(t *testing.T) {
	base := testServer(t).URL

	m := getJSON(t, base+"/api/search?q=")
	if m["total"].(float64) != 3 {
		t.Fatalf("empty query total: %v", m)
	}
	results := m["results"].([]any)
	first := results[0].(map[string]any)
	if first["name"] != "Ubuntu 24.04" {
		t.Fatalf("expected newest first: %v", first["name"])
	}
	magnet := first["magnet"].(string)
	if !strings.HasPrefix(magnet, "magnet:?xt=urn:btih:") || !strings.Contains(magnet, "&dn=") {
		t.Fatalf("bad magnet: %s", magnet)
	}

	m = getJSON(t, base+"/api/search?q=big+bunny")
	if m["total"].(float64) != 1 {
		t.Fatalf("keyword search: %v", m["total"])
	}

	m = getJSON(t, base+"/api/search?q=&page_size=1&page=2")
	if len(m["results"].([]any)) != 1 || m["page"].(float64) != 2 {
		t.Fatalf("pagination: %v", m)
	}
}

// A huge page number must not translate into a huge OFFSET scan.
func TestSearchClampsDeepPagination(t *testing.T) {
	base := testServer(t).URL
	m := getJSON(t, base+"/api/search?q=&page=99999999&page_size=100")
	if got, want := m["page"].(float64), float64(maxOffset/100+1); got != want {
		t.Fatalf("page = %v, want clamped to %v", got, want)
	}
}

func TestSearchContentFilters(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, torrent := range []store.Torrent{
		{InfoHash: "safe", Name: "safe movie", TotalSize: 1, CreatedAt: 100},
		{InfoHash: "adult", Name: "adult movie", Category: store.CategoryAdult, TotalSize: 1, CreatedAt: 200},
	} {
		if err := st.Upsert(torrent); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Classify([]store.Classification{{
		InfoHash: "safe", Category: store.CategorySpam, Confidence: 0.9, Reason: "test",
	}}, 300); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(store.Torrent{InfoHash: "general", Name: "general movie", TotalSize: 1, CreatedAt: 300}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{}).Handler())
	t.Cleanup(srv.Close)

	if got := getJSON(t, srv.URL+"/api/search?q=")["total"].(float64); got != 1 {
		t.Fatalf("safe total=%v, want 1", got)
	}
	if got := getJSON(t, srv.URL+"/api/search?q=&include_adult=true")["total"].(float64); got != 2 {
		t.Fatalf("include-adult total=%v, want 2", got)
	}
	adult := getJSON(t, srv.URL+"/api/search?q=&category=adult")
	if got := adult["total"].(float64); got != 1 {
		t.Fatalf("adult total=%v, want 1", got)
	}
	result := adult["results"].([]any)[0].(map[string]any)
	if result["category"] != "adult" || result["is_adult"] != true {
		t.Fatalf("adult result=%v", result)
	}
	if got := getJSON(t, srv.URL+"/api/search?q=&category=spam")["total"].(float64); got != 1 {
		t.Fatalf("spam total=%v, want 1", got)
	}

	resp, err := http.Get(srv.URL + "/api/search?include_adult=perhaps")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid filter status=%d, want 400", resp.StatusCode)
	}
}

func TestStats(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/stats")
	if m["torrents"].(float64) != 3 {
		t.Fatalf("stats torrents: %v", m)
	}
	crawler := m["crawler"].(map[string]any)
	if crawler["seen_infohashes"].(float64) != 42 {
		t.Fatalf("crawler stats: %v", crawler)
	}
}

func TestOptionsPreflight(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/search", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS: status %d", resp.StatusCode)
	}
}
