// Package store persists indexed torrents and pipeline counters in SQLite
// using the pure-Go modernc.org/sqlite driver (no cgo).
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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
	files_json  TEXT NOT NULL,
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

// Upsert inserts a torrent, ignoring duplicates (same info hash) and
// infohashes previously rejected by moderation.
func (s *Store) Upsert(t Torrent) error {
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files_json, created_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)`,
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, string(fj), t.CreatedAt, t.InfoHash)
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
	q := `SELECT info_hash, IIF(clean_name <> '', clean_name, name), total_size, file_count, files_json, created_at
	      FROM torrents` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Torrent
		var fj string
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.TotalSize, &t.FileCount, &fj, &t.CreatedAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(fj), &t.Files); err != nil {
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
