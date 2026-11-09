// Package db wraps SQLite (WAL) and the repositories used by the dashboard and gateway.
package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)"} {
		q.Add("_pragma", p)
	}
	// read-modify-write transactions must take the write lock up front, or concurrent ones fail with SQLITE_BUSY
	q.Set("_txlock", "immediate")
	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(4)
	s := &Store{DB: d}
	for _, stmt := range schemaStatements {
		if _, err := d.Exec(stmt); err != nil {
			d.Close()
			return nil, err
		}
	}
	// INSERT OR IGNORE: never clobber a DB created by XRouter-Next.
	if _, err := d.Exec(`INSERT OR IGNORE INTO _meta(key,value) VALUES('schemaVersion', ?)`, schemaVersion); err != nil {
		return nil, err
	}
	if _, err := d.Exec(`INSERT OR IGNORE INTO settings(id,data) VALUES(1,'{}')`); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// splitData decodes the JSON `data` column; unknown fields survive round trips.
func splitData(raw string) map[string]any {
	m := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

func joinData(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// withTx runs fn in a transaction, committing on nil error.
func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
