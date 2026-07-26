package store

import (
	"database/sql"
	"path/filepath"
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

func TestUnreviewedMarkReviewedAndBlock(t *testing.T) {
	s := openTest(t)
	for _, r := range []Torrent{
		{InfoHash: "aa", Name: "keep me", TotalSize: 1 << 30, CreatedAt: 100},
		{InfoHash: "bb", Name: "drop me", TotalSize: 1 << 30, CreatedAt: 200},
	} {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}

	cands, err := s.Unreviewed(10)
	if err != nil || len(cands) != 2 {
		t.Fatalf("unreviewed: %d cands err=%v", len(cands), err)
	}
	if cands[0].InfoHash != "aa" {
		t.Fatalf("expected oldest first, got %q", cands[0].InfoHash)
	}

	n, err := s.Block([]string{"bb"}, []string{"drop me"}, "adult", 500)
	if err != nil || n != 1 {
		t.Fatalf("block: n=%d err=%v", n, err)
	}
	if err := s.MarkReviewed([]string{"aa", "bb"}, 500); err != nil {
		t.Fatal(err)
	}

	if cands, err = s.Unreviewed(10); err != nil || len(cands) != 0 {
		t.Fatalf("after review: %d cands err=%v", len(cands), err)
	}
	if pending, _ := s.PendingReviewCount(); pending != 0 {
		t.Fatalf("pending=%d, want 0", pending)
	}
	if blocked, _ := s.BlockedCount(); blocked != 1 {
		t.Fatalf("blocked=%d, want 1", blocked)
	}

	// A blocked infohash must not come back via the crawler.
	if err := s.Upsert(Torrent{InfoHash: "bb", Name: "drop me", TotalSize: 1 << 30, CreatedAt: 900}); err != nil {
		t.Fatal(err)
	}
	if total, _ := s.Count(); total != 1 {
		t.Fatalf("count=%d, want 1 (blocked hash was re-added)", total)
	}
}

func TestBlockRejectsMismatchedNames(t *testing.T) {
	s := openTest(t)
	if _, err := s.Block([]string{"aa", "bb"}, []string{"only one"}, "spam", 1); err == nil {
		t.Fatal("expected error for mismatched hashes/names")
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

// A database created before the moderation feature has no reviewed_at column.
// Open must migrate it in place rather than failing on the statements that
// reference the column.
func TestOpenMigratesPreModerationDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE torrents (
		info_hash  TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		total_size INTEGER NOT NULL,
		file_count INTEGER NOT NULL,
		files_json TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	INSERT INTO torrents VALUES ('aa', 'Sintel 2010', 1024, 1, '[]', 100);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on pre-migration db: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// The pre-existing row survives and counts as unreviewed.
	if total, _ := s.Count(); total != 1 {
		t.Fatalf("count=%d, want 1", total)
	}
	n, err := s.PendingReviewCount()
	if err != nil || n != 1 {
		t.Fatalf("pending=%d err=%v, want 1", n, err)
	}

	// Reopening an already-migrated database is a no-op, not an error.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen migrated db: %v", err)
	}
	s2.Close()
}
