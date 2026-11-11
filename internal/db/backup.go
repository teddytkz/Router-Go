package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	BackupFormat  = "router-go-backup"
	BackupVersion = 1
)

// backupTables is the configuration worth moving between machines. Usage history, request details and proxy fitness are
// left out on purpose: large, and rebuilt by use.
var backupTables = []string{"settings", "providerConnections", "providerNodes", "proxyPools", "apiKeys", "combos", "kv"}

// machineSettings never travel with a backup: they describe how this machine listens, not what it is configured to do.
var machineSettings = []string{"port", "localOnly"}

type Backup struct {
	Format     string                      `json:"format"`
	Version    int                         `json:"version"`
	ExportedAt string                      `json:"exportedAt"`
	Tables     map[string][]map[string]any `json:"tables"`
}

// Export reads every backup table in one read transaction. The file holds secrets (upstream keys, OAuth tokens, the
// password hash, gateway keys): the caller must treat it as such.
func (s *Store) Export() (*Backup, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	b := &Backup{Format: BackupFormat, Version: BackupVersion, ExportedAt: now(), Tables: map[string][]map[string]any{}}
	for _, t := range backupTables {
		rows, err := tx.Query(`SELECT * FROM ` + t)
		if err != nil {
			return nil, err
		}
		cols, _ := rows.Columns()
		out := []map[string]any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			m := map[string]any{}
			for i, c := range cols {
				if bs, ok := vals[i].([]byte); ok {
					vals[i] = string(bs)
				}
				m[c] = vals[i]
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		b.Tables[t] = out
	}
	for _, row := range b.Tables["settings"] {
		data := splitData(fmt.Sprint(row["data"]))
		for _, k := range machineSettings {
			delete(data, k)
		}
		row["data"] = joinData(data)
	}
	return b, nil
}

// ErrBackup marks a backup file that is rejected before anything is changed.
var ErrBackup = errors.New("invalid backup")

// Restore replaces the content of every table present in b, all or nothing. Tables missing from b are left alone.
// The port and bind settings of this machine are kept. It returns the number of rows written per table.
func (s *Store) Restore(b *Backup) (map[string]int, error) {
	if b == nil || b.Format != BackupFormat {
		return nil, fmt.Errorf("%w: not a router-go backup file", ErrBackup)
	}
	if b.Version != BackupVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrBackup, b.Version)
	}
	allowed := map[string]bool{}
	for _, t := range backupTables {
		allowed[t] = true
	}
	for t := range b.Tables {
		if !allowed[t] {
			return nil, fmt.Errorf("%w: unknown table %q", ErrBackup, t)
		}
	}
	if len(b.Tables) == 0 {
		return nil, fmt.Errorf("%w: no tables", ErrBackup)
	}
	counts := map[string]int{}
	err := s.withTx(func(tx *sql.Tx) error {
		var curRaw string
		if err := tx.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&curRaw); err != nil {
			return err
		}
		cur := splitData(curRaw)
		for _, t := range backupTables {
			rows, ok := b.Tables[t]
			if !ok {
				continue
			}
			cols, err := tableColumns(tx, t)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
				return err
			}
			for i, row := range rows {
				if t == "settings" {
					data := splitData(fmt.Sprint(row["data"]))
					for _, k := range machineSettings {
						delete(data, k)
						if v, ok := cur[k]; ok {
							data[k] = v
						}
					}
					row = map[string]any{"id": 1, "data": joinData(data)}
				}
				names, args := make([]string, 0, len(row)), make([]any, 0, len(row))
				for c, v := range row {
					if !cols[c] { // column names are spliced into SQL below: only ever the table's own
						return fmt.Errorf("%w: %s[%d]: unknown column %q", ErrBackup, t, i, c)
					}
					names = append(names, c)
					args = append(args, v)
				}
				if len(names) == 0 {
					return fmt.Errorf("%w: %s[%d]: empty row", ErrBackup, t, i)
				}
				q := `INSERT INTO ` + t + `(` + strings.Join(names, ",") + `) VALUES(?` + strings.Repeat(",?", len(names)-1) + `)`
				if _, err := tx.Exec(q, args...); err != nil {
					return fmt.Errorf("%w: %s[%d]: %v", ErrBackup, t, i, err)
				}
				counts[t]++
			}
		}
		// keep the single settings row the rest of the code relies on
		_, err := tx.Exec(`INSERT OR IGNORE INTO settings(id,data) VALUES(1,?)`, joinData(cur))
		return err
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	return cols, rows.Err()
}

// ParseBackup decodes a backup file keeping integers exact (no float64 round trip).
func ParseBackup(r io.Reader) (*Backup, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var raw Backup
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: not valid JSON", ErrBackup)
	}
	for _, rows := range raw.Tables {
		for _, row := range rows {
			for k, v := range row {
				if n, ok := v.(json.Number); ok {
					if i, err := n.Int64(); err == nil {
						row[k] = i
					} else if f, err := n.Float64(); err == nil {
						row[k] = f
					}
				}
			}
		}
	}
	return &raw, nil
}
