// Package store persists indexed torrents and pipeline counters in SQLite
// using the pure-Go modernc.org/sqlite driver (no cgo).
package store

import (
	"bytes"
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
	InfoHash  string        `json:"info_hash"`
	Name      string        `json:"name"`
	TotalSize int64         `json:"total_size"`
	FileCount int           `json:"file_count"`
	Files     []filter.File `json:"files"`
	CreatedAt int64         `json:"created_at"`
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
-- Infohashes removed by moderation. Kept so the crawler cannot re-add (and
-- re-pay to re-classify) a torrent that was already rejected.
CREATE TABLE IF NOT EXISTS blocked (
	info_hash  TEXT PRIMARY KEY,
	reason     TEXT NOT NULL,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL
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
	// Partial index: only unreviewed rows are indexed, so it stays tiny and the
	// moderation sweep never scans the full table. Created after the migration
	// above so the column it filters on is guaranteed to exist.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_unreviewed
		ON torrents(created_at) WHERE reviewed_at = 0`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create unreviewed index: %w", err)
	}
	return &Store{db: db}, nil
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

// Upsert inserts a torrent, ignoring duplicates (same info hash) and
// infohashes previously rejected by moderation.
func (s *Store) Upsert(t Torrent) error {
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files, created_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)`,
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, compressFiles(fj), t.CreatedAt, t.InfoHash)
	return err
}

// Search finds torrents whose name contains every space-separated keyword
// of query (AND semantics), newest first. An empty query returns the latest
// additions. page is 1-based; pageSize must be > 0.
func (s *Store) Search(query string, page, pageSize int) (items []Torrent, total int, err error) {
	if page < 1 {
		page = 1
	}
	where, args := "", []interface{}{}
	keywords := strings.Fields(query)
	if len(keywords) > 0 {
		var conds []string
		for _, kw := range keywords {
			// Match either title: the raw one so trimming can never hide a
			// result, and the cleaned one so a phrase that only reads
			// contiguously once the ad text is gone still matches.
			conds = append(conds, "(name LIKE ? ESCAPE '\\' OR clean_name LIKE ? ESCAPE '\\')")
			args = append(args, "%"+escapeLike(kw)+"%", "%"+escapeLike(kw)+"%")
		}
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM torrents`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT info_hash, IIF(clean_name <> '', clean_name, name), total_size, file_count, files, created_at
	      FROM torrents` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Torrent
		var fb []byte
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.TotalSize, &t.FileCount, &fb, &t.CreatedAt); err != nil {
			return nil, 0, err
		}
		fj, err := decompressFiles(fb)
		if err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(fj, &t.Files); err != nil {
			return nil, 0, err
		}
		items = append(items, t)
	}
	return items, total, rows.Err()
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

// Block deletes the named torrents and records them as rejected so the
// crawler cannot re-add them. names must be parallel to hashes; it is stored
// only for the audit trail. Returns the number of rows actually deleted.
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

// BlockedCount returns the number of moderation-rejected infohashes.
func (s *Store) BlockedCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM blocked`).Scan(&n)
	return n, err
}

// PendingReviewCount returns how many stored torrents await moderation.
func (s *Store) PendingReviewCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM torrents WHERE reviewed_at = 0`).Scan(&n)
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
func (s *Store) Stats() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT key, value FROM stats`)
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
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM torrents`).Scan(&n)
	return n, err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// escapeLike escapes LIKE wildcards and the escape char itself.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
