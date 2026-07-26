package store

import (
	"testing"

	"dhtsearch/server/internal/filter"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndSearch(t *testing.T) {
	s := openTest(t)
	recs := []Torrent{
		{InfoHash: "aa", Name: "Big Buck Bunny 2008", TotalSize: 700 << 20, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "bbb.mkv", Size: 700 << 20}}},
		{InfoHash: "bb", Name: "Sintel 2010 Open Movie", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 200},
		{InfoHash: "cc", Name: "Ubuntu 24.04 ISO", TotalSize: 5 << 30, FileCount: 1, CreatedAt: 300},
	}
	for _, r := range recs {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	// Duplicate insert is ignored.
	if err := s.Upsert(recs[0]); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.Search("", 1, 10)
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("empty search: items=%d total=%d err=%v", len(items), total, err)
	}
	if items[0].InfoHash != "cc" {
		t.Fatalf("expected newest first, got %q", items[0].InfoHash)
	}

	items, total, err = s.Search("big bunny", 1, 10)
	if err != nil || total != 1 || items[0].InfoHash != "aa" {
		t.Fatalf("keyword search: items=%v total=%d err=%v", items, total, err)
	}
	if items[0].Files[0].Path != "bbb.mkv" {
		t.Fatalf("files json round-trip failed: %+v", items[0].Files)
	}

	// LIKE wildcards in the query must be literal.
	_, total, err = s.Search("100%", 1, 10)
	if err != nil || total != 0 {
		t.Fatalf("wildcard query: total=%d err=%v", total, err)
	}

	// Pagination.
	items, total, err = s.Search("", 2, 2)
	if err != nil || total != 3 || len(items) != 1 || items[0].InfoHash != "aa" {
		t.Fatalf("pagination: items=%v total=%d err=%v", items, total, err)
	}
}

func TestStats(t *testing.T) {
	s := openTest(t)
	if err := s.IncrStat("seen", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrStat("seen", 2); err != nil {
		t.Fatal(err)
	}
	m, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if m["seen"] != 5 {
		t.Fatalf("seen=%d, want 5", m["seen"])
	}
}
