// Package store persists indexed torrents and pipeline counters in SQLite
// using the pure-Go modernc.org/sqlite driver (no cgo).
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"dhtsearch/server/internal/filter"
)

// Torrent is one indexed torrent record.
type Torrent struct {
	InfoHash string `json:"info_hash"`
	Name     string `json:"name"`
	Category string `json:"category"`
	// Classification metadata is written on ingest but not exposed as part of
	// the torrent payload; public results only need Category.
	CategoryConfidence float64       `json:"-"`
	CategoryReason     string        `json:"-"`
	TotalSize          int64         `json:"total_size"`
	FileCount          int           `json:"file_count"`
	Files              []filter.File `json:"files"`
	CreatedAt          int64         `json:"created_at"`
}

const (
	CategoryGeneral = "general"
	CategoryAdult   = "adult"
	CategorySpam    = "spam"
)

// SearchFilter controls which classified rows are visible. The zero value is
// safe for public search: adult and spam rows are excluded.
type SearchFilter struct {
	IncludeAdult bool
	Category     string
}

// Classification is a persisted moderation decision. Categories live in a
// separate table so the taxonomy can expand without widening torrent rows.
type Classification struct {
	InfoHash   string
	Category   string
	Confidence float64
	Reason     string
}

// Candidate is the slim projection of a torrent handed to the moderation
// pass. The file list is deliberately excluded: it is the bulk of the row and
// the classifier only needs the name, size and file count.
type Candidate struct {
	InfoHash  string
	Name      string
	TotalSize int64
	FileCount int
}

// Store wraps the SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (and creates if needed) the database at path and ensures the
// schema exists.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer; keep a single connection to avoid lock churn.
	db.SetMaxOpenConns(1)
	const schema = `
CREATE TABLE IF NOT EXISTS torrents (
	info_hash   TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	total_size  INTEGER NOT NULL,
	file_count  INTEGER NOT NULL,
	-- JSON file list, zstd-compressed when that is smaller than the plain
	-- text. See compressFiles/decompressFiles.
	files       BLOB NOT NULL,
	created_at  INTEGER NOT NULL,
	reviewed_at INTEGER NOT NULL DEFAULT 0,
	-- Title with promotional junk stripped by the moderation pass. Empty means
	-- "not trimmed"; name always keeps the raw title so trimming is reversible
	-- and search still matches text that only appears in the original.
	clean_name  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS stats (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
-- Permanent removals (for example copyright). Adult/spam decisions live in
-- categories so those records remain available to explicit filtered views.
CREATE TABLE IF NOT EXISTS blocked (
	info_hash  TEXT PRIMARY KEY,
	reason     TEXT NOT NULL,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS categories (
	info_hash   TEXT PRIMARY KEY,
	category    TEXT NOT NULL,
	confidence  REAL NOT NULL DEFAULT 1,
	reason      TEXT NOT NULL DEFAULT '',
	reviewed_at INTEGER NOT NULL DEFAULT 0
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Migrate databases created before reviewed_at existed. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so a duplicate-column error is the success
	// case on an already-migrated database. This must run before anything
	// that references reviewed_at — on a pre-migration database the column
	// does not exist yet and those statements would fail.
	for _, m := range []struct{ name, stmt string }{
		{"reviewed_at", `ALTER TABLE torrents ADD COLUMN reviewed_at INTEGER NOT NULL DEFAULT 0`},
		{"clean_name", `ALTER TABLE torrents ADD COLUMN clean_name TEXT NOT NULL DEFAULT ''`},
	} {
		if _, err := db.Exec(m.stmt); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// Migrate the uncompressed files_json TEXT column (databases created
	// before compression) into the files BLOB column.
	if err := migrateFilesBlob(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate files_json to files: %w", err)
	}
	if err := migrateLegacyModerationBlocks(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate legacy moderation blocks: %w", err)
	}
	// Partial index: only unreviewed rows are indexed, so it stays tiny and the
	// moderation sweep never scans the full table. Created after the migration
	// above so the column it filters on is guaranteed to exist.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_unreviewed
		ON torrents(created_at) WHERE reviewed_at = 0`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create unreviewed index: %w", err)
	}
	// Newest-first is the sort order of every search. Without this index an
	// empty query — the landing page — sorts the whole table per request,
	// and a keyword query cannot stop early after filling its page.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_created
		ON torrents(created_at DESC)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create created_at index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_categories_category
		ON categories(category, info_hash)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create category index: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateLegacyModerationBlocks converts adult/spam hashes created by the old
// destructive moderation flow into category placeholders. The torrent rows
// no longer exist, but removing these hashes from blocked lets the crawler
// restore their metadata; the placeholder keeps them hidden in the meantime
// and immediately classifies them when they are rediscovered.
func migrateLegacyModerationBlocks(db *sql.DB) error {
	var migrated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stats WHERE key = 'legacy_block_categories_v1'`).Scan(&migrated); err != nil {
		return err
	}
	if migrated != 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO categories (info_hash, category, confidence, reason, reviewed_at)
		SELECT info_hash, reason, 1, 'migrated from legacy block: ' || name, created_at
		FROM blocked WHERE reason IN ('adult', 'spam')
		ON CONFLICT(info_hash) DO NOTHING`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM blocked WHERE reason IN ('adult', 'spam')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO stats(key, value) VALUES ('legacy_block_categories_v1', 1)`); err != nil {
		return err
	}
	return tx.Commit()
}

// Shared zstd coders. EncodeAll/DecodeAll on a nil-stream coder are safe for
// concurrent use. Options are static and valid, so construction cannot fail.
var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	zdec, _ = zstd.NewReader(nil)
)

// zstdMagic starts every zstd frame (RFC 8878). Raw JSON starts with '[', so
// the first bytes of a files blob say unambiguously whether it is compressed.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// compressFiles returns raw zstd-compressed, or raw itself when compression
// does not pay (small file lists, where the frame overhead wins).
func compressFiles(raw []byte) []byte {
	c := zenc.EncodeAll(raw, make([]byte, 0, len(raw)))
	if len(c) >= len(raw) {
		return raw
	}
	return c
}

// decompressFiles reverses compressFiles.
func decompressFiles(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, zstdMagic) {
		return b, nil
	}
	return zdec.DecodeAll(b, nil)
}

// migrateFilesBlob rewrites databases created when the file list was stored
// as plain JSON in files_json: it backfills the compressed files column,
// drops files_json, and vacuums to hand the freed pages back to the OS. It is
// crash-safe: every batch commits separately, and until the DROP COLUMN at
// the end both columns coexist, so an interrupted run resumes where it left
// off on the next Open. No-op unless files_json exists.
func migrateFilesBlob(db *sql.DB) error {
	var hasOld int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('torrents') WHERE name = 'files_json'`).Scan(&hasOld); err != nil {
		return err
	}
	if hasOld == 0 {
		return nil
	}
	// The files column cannot be in the CREATE TABLE path here (the table
	// already existed), so add it. x'' marks "not yet backfilled": a real
	// blob is never empty because even an empty file list serializes to "[]".
	if _, err := db.Exec(`ALTER TABLE torrents ADD COLUMN files BLOB NOT NULL DEFAULT x''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Backfill in bounded batches so memory stays flat on large databases.
	for {
		rows, err := db.Query(
			`SELECT info_hash, files_json FROM torrents WHERE length(files) = 0 LIMIT 1000`)
		if err != nil {
			return err
		}
		type pending struct {
			hash string
			blob []byte
		}
		var batch []pending
		for rows.Next() {
			var hash, fj string
			if err := rows.Scan(&hash, &fj); err != nil {
				rows.Close()
				return err
			}
			if fj == "" {
				// Defensive: an empty string would neither unmarshal nor
				// leave the "not yet backfilled" state. Normalize.
				fj = "[]"
			}
			batch = append(batch, pending{hash, compressFiles([]byte(fj))})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		up, err := tx.Prepare(`UPDATE torrents SET files = ? WHERE info_hash = ?`)
		if err != nil {
			tx.Rollback()
			return err
		}
		for _, p := range batch {
			if _, err := up.Exec(p.blob, p.hash); err != nil {
				up.Close()
				tx.Rollback()
				return err
			}
		}
		up.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`ALTER TABLE torrents DROP COLUMN files_json`); err != nil {
		return err
	}
	// Return the freed pages to the filesystem. Best-effort: VACUUM needs
	// scratch space on disk, and a database that keeps its old size is still
	// fully functional, so a failure here must not block startup.
	db.Exec(`VACUUM`)
	return nil
}

// Upsert inserts a torrent, ignoring duplicates (same info hash) and hashes
// explicitly blocked for reasons such as copyright removal. Adult/spam
// moderation uses categories and does not add hashes to blocked.
func (s *Store) Upsert(t Torrent) error {
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files, created_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)`,
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, compressFiles(fj), t.CreatedAt, t.InfoHash)
	if err != nil {
		return err
	}
	if t.Category != "" && t.Category != CategoryGeneral {
		confidence := t.CategoryConfidence
		if confidence <= 0 || confidence > 1 {
			confidence = 1
		}
		if _, err := tx.Exec(`INSERT INTO categories (info_hash, category, confidence, reason, reviewed_at)
			SELECT ?, ?, ?, ?, 0 WHERE EXISTS (SELECT 1 FROM torrents WHERE info_hash = ?)
			ON CONFLICT(info_hash) DO UPDATE SET category = excluded.category,
				confidence = excluded.confidence, reason = excluded.reason`,
			t.InfoHash, t.Category, confidence, t.CategoryReason, t.InfoHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// maxKeywords bounds how many query keywords turn into LIKE conditions.
// Each keyword adds two LIKE evaluations per scanned row, so an unbounded
// list lets one request multiply its own scan cost ~100x. Past a handful of
// keywords extra terms only narrow an already tiny result set; the tail is
// ignored.
const maxKeywords = 8

// selectCols is the projection shared by Search and Latest, decoded by
// scanTorrents.
const selectCols = `SELECT t.info_hash, IIF(t.clean_name <> '', t.clean_name, t.name),
	COALESCE(c.category, 'general'), t.total_size, t.file_count, t.files, t.created_at
	FROM torrents t LEFT JOIN categories c ON c.info_hash = t.info_hash`

func searchFilterSQL(filter SearchFilter) (string, []interface{}) {
	if filter.Category != "" {
		return `COALESCE(c.category, 'general') = ?`, []interface{}{filter.Category}
	}
	if filter.IncludeAdult {
		return `COALESCE(c.category, 'general') <> 'spam'`, nil
	}
	return `COALESCE(c.category, 'general') NOT IN ('adult', 'spam')`, nil
}

func firstSearchFilter(filters []SearchFilter) SearchFilter {
	if len(filters) == 0 {
		return SearchFilter{}
	}
	return filters[0]
}

// Search finds torrents whose name contains every space-separated keyword
// of query (AND semantics), newest first. An empty query returns the latest
// additions. page is 1-based; pageSize must be > 0. ctx bounds the queries:
// keyword matching is a full-table scan, so callers must be able to cut it
// off when the client hangs up or a deadline passes.
func (s *Store) Search(ctx context.Context, query string, page, pageSize int, filters ...SearchFilter) (items []Torrent, total int, err error) {
	if page < 1 {
		page = 1
	}
	filterWhere, args := searchFilterSQL(firstSearchFilter(filters))
	conds := []string{filterWhere}
	keywords := strings.Fields(query)
	if len(keywords) > maxKeywords {
		keywords = keywords[:maxKeywords]
	}
	if len(keywords) > 0 {
		for _, kw := range keywords {
			// Match either title: the raw one so trimming can never hide a
			// result, and the cleaned one so a phrase that only reads
			// contiguously once the ad text is gone still matches.
			conds = append(conds, "(name LIKE ? ESCAPE '\\' OR clean_name LIKE ? ESCAPE '\\')")
			args = append(args, "%"+escapeLike(kw)+"%", "%"+escapeLike(kw)+"%")
		}
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents t
		LEFT JOIN categories c ON c.info_hash = t.info_hash`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := selectCols + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	items, err = scanTorrents(rows)
	return items, total, err
}

// Latest returns a page of the newest torrents without counting the table.
// It walks the created_at index, so its cost is O(pageSize + offset) — the
// cheap path the landing page should take instead of Search("").
func (s *Store) Latest(ctx context.Context, page, pageSize int, filters ...SearchFilter) ([]Torrent, error) {
	if page < 1 {
		page = 1
	}
	where, args := searchFilterSQL(firstSearchFilter(filters))
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx,
		selectCols+` WHERE `+where+` ORDER BY t.created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanTorrents(rows)
}

// scanTorrents drains rows from a selectCols query, decompressing each file
// list. It always closes rows.
func scanTorrents(rows *sql.Rows) (items []Torrent, err error) {
	defer rows.Close()
	for rows.Next() {
		var t Torrent
		var fb []byte
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.Category, &t.TotalSize, &t.FileCount, &fb, &t.CreatedAt); err != nil {
			return nil, err
		}
		fj, err := decompressFiles(fb)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(fj, &t.Files); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

// Unreviewed returns up to limit torrents the moderation pass has not seen
// yet, oldest first.
func (s *Store) Unreviewed(limit int) ([]Candidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT info_hash, name, total_size, file_count FROM torrents
		 WHERE reviewed_at = 0 ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.InfoHash, &c.Name, &c.TotalSize, &c.FileCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkReviewed stamps hashes as classified so later sweeps skip them.
func (s *Store) MarkReviewed(hashes []string, ts int64) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]interface{}, 0, len(hashes)+1)
	args = append(args, ts)
	for _, h := range hashes {
		args = append(args, h)
	}
	_, err := s.db.Exec(
		`UPDATE torrents SET reviewed_at = ? WHERE info_hash IN (`+placeholders(len(hashes))+`)`, args...)
	return err
}

// Classify stores moderation decisions and marks the matching torrents as
// reviewed in one transaction. No torrent row is deleted.
func (s *Store) Classify(items []Classification, ts int64) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	up, err := tx.Prepare(`INSERT INTO categories
		(info_hash, category, confidence, reason, reviewed_at)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM torrents WHERE info_hash = ?)
		ON CONFLICT(info_hash) DO UPDATE SET category = excluded.category,
			confidence = excluded.confidence, reason = excluded.reason,
			reviewed_at = excluded.reviewed_at`)
	if err != nil {
		return err
	}
	defer up.Close()
	mark, err := tx.Prepare(`UPDATE torrents SET reviewed_at = ? WHERE info_hash = ?`)
	if err != nil {
		return err
	}
	defer mark.Close()
	for _, item := range items {
		confidence := item.Confidence
		if confidence < 0 {
			confidence = 0
		} else if confidence > 1 {
			confidence = 1
		}
		if _, err := up.Exec(item.InfoHash, item.Category, confidence, item.Reason, ts, item.InfoHash); err != nil {
			return err
		}
		if _, err := mark.Exec(ts, item.InfoHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetCleanNames records trimmed titles. clean is keyed by info hash and holds
// the title with promotional text removed; the raw name column is left alone.
// Returns the number of rows updated.
func (s *Store) SetCleanNames(clean map[string]string) (int64, error) {
	if len(clean) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	up, err := tx.Prepare(`UPDATE torrents SET clean_name = ? WHERE info_hash = ?`)
	if err != nil {
		return 0, err
	}
	defer up.Close()

	var n int64
	for hash, name := range clean {
		res, err := up.Exec(name, hash)
		if err != nil {
			return 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += affected
	}
	return n, tx.Commit()
}

// Block permanently removes named torrents and records them so the crawler
// cannot re-add them. This is reserved for hard removals such as copyright;
// adult/spam moderation uses Classify instead. names must be parallel to
// hashes and is stored only for the audit trail.
func (s *Store) Block(hashes, names []string, reason string, ts int64) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	if len(names) != len(hashes) {
		return 0, fmt.Errorf("block: %d hashes but %d names", len(hashes), len(names))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(
		`INSERT OR IGNORE INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	for i, h := range hashes {
		if _, err := ins.Exec(h, reason, names[i], ts); err != nil {
			return 0, err
		}
	}

	args := make([]interface{}, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	res, err := tx.Exec(`DELETE FROM torrents WHERE info_hash IN (`+placeholders(len(hashes))+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// BlockedCount returns the number of permanently rejected infohashes.
func (s *Store) BlockedCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocked`).Scan(&n)
	return n, err
}

// PendingReviewCount returns how many stored torrents await moderation.
func (s *Store) PendingReviewCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents WHERE reviewed_at = 0`).Scan(&n)
	return n, err
}

// placeholders returns "?, ?, ..." with n entries.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// IncrStat atomically adds delta to the named counter.
func (s *Store) IncrStat(key string, delta int64) error {
	_, err := s.db.Exec(
		`INSERT INTO stats (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = value + excluded.value`, key, delta)
	return err
}

// Stats returns all pipeline counters.
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// Count returns the number of stored torrents.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents`).Scan(&n)
	return n, err
}

// CountSearchable returns the total visible under filter.
func (s *Store) CountSearchable(ctx context.Context, filter SearchFilter) (int, error) {
	where, args := searchFilterSQL(filter)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents t
		LEFT JOIN categories c ON c.info_hash = t.info_hash WHERE `+where, args...).Scan(&n)
	return n, err
}

// CategoryCounts returns the number of persisted rows per effective category.
func (s *Store) CategoryCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(c.category, 'general'), COUNT(*)
		FROM torrents t LEFT JOIN categories c ON c.info_hash = t.info_hash GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var category string
		var count int64
		if err := rows.Scan(&category, &count); err != nil {
			return nil, err
		}
		out[category] = count
	}
	return out, rows.Err()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// escapeLike escapes LIKE wildcards and the escape char itself.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
