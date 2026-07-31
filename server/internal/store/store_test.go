package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
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

	items, total, err := s.Search(t.Context(), "", 1, 10)
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("empty search: items=%d total=%d err=%v", len(items), total, err)
	}
	if items[0].InfoHash != "cc" {
		t.Fatalf("expected newest first, got %q", items[0].InfoHash)
	}

	items, total, err = s.Search(t.Context(), "big bunny", 1, 10)
	if err != nil || total != 1 || items[0].InfoHash != "aa" {
		t.Fatalf("keyword search: items=%v total=%d err=%v", items, total, err)
	}
	if items[0].Files[0].Path != "bbb.mkv" {
		t.Fatalf("files json round-trip failed: %+v", items[0].Files)
	}

	// LIKE wildcards in the query must be literal.
	_, total, err = s.Search(t.Context(), "100%", 1, 10)
	if err != nil || total != 0 {
		t.Fatalf("wildcard query: total=%d err=%v", total, err)
	}

	// Pagination.
	items, total, err = s.Search(t.Context(), "", 2, 2)
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
	if pending, _ := s.PendingReviewCount(t.Context()); pending != 0 {
		t.Fatalf("pending=%d, want 0", pending)
	}
	if blocked, _ := s.BlockedCount(t.Context()); blocked != 1 {
		t.Fatalf("blocked=%d, want 1", blocked)
	}

	// A blocked infohash must not come back via the crawler.
	if err := s.Upsert(Torrent{InfoHash: "bb", Name: "drop me", TotalSize: 1 << 30, CreatedAt: 900}); err != nil {
		t.Fatal(err)
	}
	if total, _ := s.Count(t.Context()); total != 1 {
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
	m, err := s.Stats(t.Context())
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
	if total, _ := s.Count(t.Context()); total != 1 {
		t.Fatalf("count=%d, want 1", total)
	}
	n, err := s.PendingReviewCount(t.Context())
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

// A database from before compression stores the file list as plain JSON in a
// files_json column. Open must move it into the compressed files blob, drop
// the old column, and keep every file list readable.
func TestOpenMigratesFilesJSONToCompressedBlob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A file list long enough that zstd beats the plain text.
	var files []filter.File
	for i := 0; i < 200; i++ {
		files = append(files, filter.File{
			Path: fmt.Sprintf("Some.Show.S01E%03d.1080p.WEB-DL.x265/episode.%03d.mkv", i, i),
			Size: int64(i) << 20,
		})
	}
	fj, err := json.Marshal(files)
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
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO torrents VALUES ('aa', 'Some Show S01', 42, 200, ?, 100)`,
		string(fj)); err != nil {
		t.Fatal(err)
	}
	// A second row whose tiny list is cheaper kept raw.
	if _, err := db.Exec(`INSERT INTO torrents VALUES ('bb', 'One File', 7, 1, '[]', 200)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on files_json db: %v", err)
	}
	defer s.Close()

	var hasOld int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('torrents') WHERE name = 'files_json'`).Scan(&hasOld); err != nil {
		t.Fatal(err)
	}
	if hasOld != 0 {
		t.Fatal("files_json column still present after migration")
	}

	var blobLen int
	if err := s.db.QueryRow(
		`SELECT length(files) FROM torrents WHERE info_hash = 'aa'`).Scan(&blobLen); err != nil {
		t.Fatal(err)
	}
	if blobLen == 0 || blobLen >= len(fj) {
		t.Fatalf("stored blob is %d bytes, want (0, %d)", blobLen, len(fj))
	}

	items, total, err := s.Search(t.Context(), "", 1, 10)
	if err != nil || total != 2 {
		t.Fatalf("search after migration: total=%d err=%v", total, err)
	}
	// Newest first, so items[1] is the big row.
	got := items[1].Files
	if len(got) != len(files) || got[0] != files[0] || got[199] != files[199] {
		t.Fatalf("file list did not survive migration: got %d files", len(got))
	}
	if len(items[0].Files) != 0 {
		t.Fatalf("empty file list did not survive migration: %+v", items[0].Files)
	}
}

func TestCompressFilesRoundTrip(t *testing.T) {
	// Too small to compress: stored verbatim.
	small := []byte(`[{"path":"a.mkv","size":1}]`)
	if got := compressFiles(small); !bytes.Equal(got, small) {
		t.Fatalf("small payload was not kept raw: %x", got)
	}
	// Large and repetitive: stored compressed, and round-trips.
	big := bytes.Repeat([]byte(`{"path":"show/episode.mkv","size":123456},`), 100)
	c := compressFiles(big)
	if !bytes.HasPrefix(c, zstdMagic) {
		t.Fatal("large payload was not compressed")
	}
	if len(c) >= len(big) {
		t.Fatalf("compressed %d bytes to %d, no gain", len(big), len(c))
	}
	for _, in := range [][]byte{small, big} {
		out, err := decompressFiles(compressFiles(in))
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("round-trip failed: err=%v", err)
		}
	}
}

func TestSetCleanNamesReplacesDisplayNameOnly(t *testing.T) {
	s := openTest(t)
	const raw = "【发布页 www.spam.net】Blue Bloods S14E03 1080p"
	if err := s.Upsert(Torrent{InfoHash: "aa", Name: raw, TotalSize: 1 << 30, FileCount: 1, CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}

	n, err := s.SetCleanNames(map[string]string{"aa": "Blue Bloods S14E03 1080p"})
	if err != nil || n != 1 {
		t.Fatalf("SetCleanNames = %d, %v; want 1, nil", n, err)
	}

	// Search returns the cleaned title...
	items, _, err := s.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != "Blue Bloods S14E03 1080p" {
		t.Errorf("Name = %q, want the cleaned title", items[0].Name)
	}

	// ...but the raw title is preserved in the row and still matches.
	var stored string
	if err := s.db.QueryRow(`SELECT name FROM torrents WHERE info_hash='aa'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != raw {
		t.Errorf("stored name = %q, want the raw title %q", stored, raw)
	}
	for _, q := range []string{"spam.net", "Blue Bloods", "Bloods 1080p"} {
		if _, total, err := s.Search(t.Context(), q, 1, 10); err != nil || total != 1 {
			t.Errorf("Search(%q) total = %d err = %v, want 1", q, total, err)
		}
	}
}

func TestSetCleanNamesEmptyMapIsNoOp(t *testing.T) {
	s := openTest(t)
	n, err := s.SetCleanNames(nil)
	if err != nil || n != 0 {
		t.Fatalf("SetCleanNames(nil) = %d, %v; want 0, nil", n, err)
	}
}

func TestAdultCategoryIsStoredAndFiltered(t *testing.T) {
	s := openTest(t)
	if err := s.Upsert(Torrent{
		InfoHash: "adult", Name: "classified title", Category: CategoryAdult,
		CategoryConfidence: 1, CategoryReason: "static keyword", TotalSize: 1 << 30, CreatedAt: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Torrent{InfoHash: "safe", Name: "safe title", TotalSize: 1 << 30, CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.Search(t.Context(), "", 1, 10)
	if err != nil || total != 1 || items[0].InfoHash != "safe" {
		t.Fatalf("safe search: items=%v total=%d err=%v", items, total, err)
	}
	items, total, err = s.Search(t.Context(), "classified", 1, 10, SearchFilter{IncludeAdult: true})
	if err != nil || total != 1 || items[0].Category != CategoryAdult {
		t.Fatalf("include adult: items=%v total=%d err=%v", items, total, err)
	}
	items, err = s.Latest(t.Context(), 1, 10, SearchFilter{Category: CategoryAdult})
	if err != nil || len(items) != 1 || items[0].InfoHash != "adult" {
		t.Fatalf("adult only: items=%v err=%v", items, err)
	}
	if count, err := s.CountSearchable(t.Context(), SearchFilter{IncludeAdult: true}); err != nil || count != 2 {
		t.Fatalf("all searchable count=%d err=%v", count, err)
	}

	var confidence float64
	var reason string
	if err := s.db.QueryRow(`SELECT confidence, reason FROM categories WHERE info_hash='adult'`).Scan(&confidence, &reason); err != nil {
		t.Fatal(err)
	}
	if confidence != 1 || reason != "static keyword" {
		t.Fatalf("classification metadata = %v, %q", confidence, reason)
	}
}

func TestClassifyUpdatesCategoryWithoutDeleting(t *testing.T) {
	s := openTest(t)
	if err := s.Upsert(Torrent{InfoHash: "aa", Name: "review me", TotalSize: 1 << 30, CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.Classify([]Classification{{
		InfoHash: "aa", Category: CategoryAdult, Confidence: 0.91, Reason: "model reason",
	}}, 500); err != nil {
		t.Fatal(err)
	}
	if count, _ := s.Count(t.Context()); count != 1 {
		t.Fatalf("stored count=%d, want 1", count)
	}
	if pending, _ := s.PendingReviewCount(t.Context()); pending != 0 {
		t.Fatalf("pending=%d, want 0", pending)
	}
	var confidence float64
	var reason string
	if err := s.db.QueryRow(`SELECT confidence, reason FROM categories WHERE info_hash='aa'`).Scan(&confidence, &reason); err != nil {
		t.Fatal(err)
	}
	if confidence != 0.91 || reason != "model reason" {
		t.Fatalf("classification metadata=%v, %q", confidence, reason)
	}
	items, total, err := s.Search(t.Context(), "review", 1, 10, SearchFilter{Category: CategoryAdult})
	if err != nil || total != 1 || items[0].InfoHash != "aa" {
		t.Fatalf("classified search: items=%v total=%d err=%v", items, total, err)
	}
}

func TestOpenMigratesLegacyAdultAndSpamBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-blocks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM stats WHERE key='legacy_block_categories_v1';
		INSERT INTO blocked VALUES ('adult-old', 'adult', 'old adult title', 100);
		INSERT INTO blocked VALUES ('spam-old', 'spam', 'old spam title', 101);
		INSERT INTO blocked VALUES ('copyright-old', 'copyright', 'removed work', 102);`); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if blocked, err := s.BlockedCount(t.Context()); err != nil || blocked != 1 {
		t.Fatalf("permanent blocked=%d err=%v, want 1", blocked, err)
	}
	for _, torrent := range []Torrent{
		{InfoHash: "adult-old", Name: "restored adult", TotalSize: 1, CreatedAt: 200},
		{InfoHash: "spam-old", Name: "restored spam", TotalSize: 1, CreatedAt: 201},
		{InfoHash: "copyright-old", Name: "must stay blocked", TotalSize: 1, CreatedAt: 202},
	} {
		if err := s.Upsert(torrent); err != nil {
			t.Fatal(err)
		}
	}
	if count, _ := s.Count(t.Context()); count != 2 {
		t.Fatalf("restored count=%d, want 2", count)
	}
	if _, total, _ := s.Search(t.Context(), "", 1, 10); total != 0 {
		t.Fatalf("legacy classifications leaked into safe results: %d", total)
	}
	if _, total, _ := s.Search(t.Context(), "", 1, 10, SearchFilter{Category: CategoryAdult}); total != 1 {
		t.Fatalf("adult category total=%d, want 1", total)
	}
	if _, total, _ := s.Search(t.Context(), "", 1, 10, SearchFilter{Category: CategorySpam}); total != 1 {
		t.Fatalf("spam category total=%d, want 1", total)
	}
}
