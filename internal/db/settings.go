package db

import (
	"database/sql"
	"strings"
)

// DefaultSettings is the subset of XRouter-Next settings that phase 1 understands.
func DefaultSettings() map[string]any {
	return map[string]any{
		"requireLogin":  true,
		"requireApiKey": true,
	}
}

// Settings returns stored settings merged over defaults (includes the password hash; strip before sending to UI).
func (s *Store) Settings() (map[string]any, error) {
	var raw string
	if err := s.DB.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&raw); err != nil {
		return nil, err
	}
	out := DefaultSettings()
	for k, v := range splitData(raw) {
		out[k] = v
	}
	return out, nil
}

// UpdateSettings merges patch into the stored row; a nil value deletes the key.
func (s *Store) UpdateSettings(patch map[string]any) error {
	return s.withTx(func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&raw); err != nil {
			return err
		}
		cur := splitData(raw)
		for k, v := range patch {
			if v == nil {
				delete(cur, k)
			} else {
				cur[k] = v
			}
		}
		_, err := tx.Exec(`UPDATE settings SET data=? WHERE id=1`, joinData(cur))
		return err
	})
}

// Alias resolves kv.modelAliases[alias] ("prefix/model") — read-only in phase 1.
func (s *Store) Alias(alias string) (string, bool) {
	var raw string
	if err := s.DB.QueryRow(`SELECT value FROM kv WHERE scope='modelAliases' AND key=?`, alias).Scan(&raw); err != nil {
		return "", false
	}
	v := strings.Trim(strings.TrimSpace(raw), `"`)
	return v, v != ""
}
