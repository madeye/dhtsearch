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
	info_hash  TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	total_size INTEGER NOT NULL,
	file_count INTEGER NOT NULL,
	files_json TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS stats (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Upsert inserts a torrent, ignoring duplicates (same info hash).
func (s *Store) Upsert(t Torrent) error {
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, string(fj), t.CreatedAt)
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
			conds = append(conds, "name LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(kw)+"%")
		}
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM torrents`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT info_hash, name, total_size, file_count, files_json, created_at
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
