package trending

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stub mimics the two Douban endpoints: charts with Chinese titles, and
// per-subject abstracts carrying "中文 English‎ (year)" titles.
func stub(fail *atomic.Bool, abstractCalls *atomic.Int64) http.Handler {
	abstracts := map[string]string{
		"1": "痴迷 Obsession‎ (2025)",
		"2": "瑞克和莫蒂 第九季 Rick and Morty Season 9‎ (2026)",
		"3": "无名之辈‎ (2018)", // Chinese-only: no English name
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/j/search_subjects":
			switch tag := r.URL.Query().Get("tag"); {
			case r.URL.Query().Get("type") == "movie":
				fmt.Fprint(w, `{"subjects":[{"id":"1","title":"痴迷"},{"id":"3","title":"无名之辈"}]}`)
			case tag == "日剧":
				fmt.Fprint(w, `{"subjects":[{"id":"4","title":"直到T恤干透"}]}`)
			case tag == "韩剧":
				fmt.Fprint(w, `{"subjects":[{"id":"5","title":"铁拳教育"}]}`)
			default:
				fmt.Fprint(w, `{"subjects":[{"id":"2","title":"瑞克和莫蒂 第九季"}]}`)
			}
		case "/j/subject_abstract":
			abstractCalls.Add(1)
			fmt.Fprintf(w, `{"subject":{"title":"%s"}}`, abstracts[r.URL.Query().Get("subject_id")])
		default:
			http.NotFound(w, r)
		}
	})
}

func TestFetchResolvesEnglishTitles(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL, Interval: time.Hour})
	svc.refresh(context.Background())

	snap := svc.Get()
	if got := snap.Movies; len(got) != 2 || got[0] != "Obsession" || got[1] != "无名之辈" {
		t.Fatalf("movies = %v", got)
	}
	if got := snap.TV; len(got) != 1 || got[0] != "Rick and Morty Season 9" {
		t.Fatalf("tv = %v", got)
	}
	// JP/KR charts keep Chinese titles and must not hit subject_abstract.
	if got := snap.TVJP; len(got) != 1 || got[0] != "直到T恤干透" {
		t.Fatalf("tv_jp = %v", got)
	}
	if got := snap.TVKR; len(got) != 1 || got[0] != "铁拳教育" {
		t.Fatalf("tv_kr = %v", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("abstract calls = %d, want 3 (english charts only)", calls.Load())
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}

	// Second refresh must serve names from the cache: no new abstract calls.
	before := calls.Load()
	svc.refresh(context.Background())
	if calls.Load() != before {
		t.Fatalf("abstract calls after cached refresh: %d -> %d", before, calls.Load())
	}

	// Upstream breaks: the previous snapshot must survive untouched.
	fail.Store(true)
	prev := svc.Get()
	svc.refresh(context.Background())
	after := svc.Get()
	if len(after.Movies) != 2 || len(after.TV) != 1 || len(after.TVJP) != 1 || len(after.TVKR) != 1 {
		t.Fatalf("snapshot lost after failed refresh: %+v", after)
	}
	if !after.UpdatedAt.Equal(prev.UpdatedAt) {
		t.Fatal("UpdatedAt advanced on a fully failed refresh")
	}
}

func TestEmptySubjectsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"subjects":[]}`)
	}))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL})
	if _, err := svc.fetchList(context.Background(), "movie", "欧美", true); err == nil {
		t.Fatal("expected error for empty subjects")
	}
}

func TestExtractEnglish(t *testing.T) {
	cases := []struct{ in, chinese, want string }{
		{"痴迷 Obsession‎ (2025)", "痴迷", "Obsession"},
		{"瑞克和莫蒂 第九季 Rick and Morty Season 9‎ (2026)", "瑞克和莫蒂 第九季", "Rick and Morty Season 9"},
		{"柯蒂斯总统 第一季 President Curtis Season 1‎ (2026)", "柯蒂斯总统 第一季", "President Curtis Season 1"},
		{"无名之辈‎ (2018)", "无名之辈", ""},     // no English name
		{"寒战1994‎ (1994)", "寒战1994", ""}, // digit-tailed Chinese title, nothing after
		{"疯狂动物城2 Zootopia 2‎ (2025)", "疯狂动物城2", "Zootopia 2"},
		{"V字仇杀队 V for Vendetta‎ (2005)", "V字仇杀队", "V for Vendetta"},
		// Chart/abstract mismatch falls back to the last-CJK heuristic.
		{"痴迷 Obsession‎ (2025)", "different", "Obsession"},
	}
	for _, c := range cases {
		if got := extractEnglish(c.in, c.chinese); got != c.want {
			t.Errorf("extractEnglish(%q, %q) = %q, want %q", c.in, c.chinese, got, c.want)
		}
	}
}
