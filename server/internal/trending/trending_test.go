package trending

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchAndKeepLastGood(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		typ := r.URL.Query().Get("type")
		fmt.Fprintf(w, `{"subjects":[{"title":"%s-one"},{"title":"%s-two"}]}`, typ, typ)
	}))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL, Interval: time.Hour})
	svc.refresh(context.Background())

	snap := svc.Get()
	if got := snap.Movies; len(got) != 2 || got[0] != "movie-one" {
		t.Fatalf("movies = %v", got)
	}
	if got := snap.TV; len(got) != 2 || got[1] != "tv-two" {
		t.Fatalf("tv = %v", got)
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}

	// Upstream breaks: the previous snapshot must survive untouched.
	fail = true
	before := svc.Get()
	svc.refresh(context.Background())
	after := svc.Get()
	if len(after.Movies) != 2 || len(after.TV) != 2 {
		t.Fatalf("snapshot lost after failed refresh: %+v", after)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("UpdatedAt advanced on a fully failed refresh")
	}
}

func TestEmptySubjectsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"subjects":[]}`)
	}))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL})
	if _, err := svc.fetchList(context.Background(), "movie"); err == nil {
		t.Fatal("expected error for empty subjects")
	}
}
