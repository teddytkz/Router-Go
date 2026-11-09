package db

import (
	"database/sql"
	"encoding/json"
	"strings"

	"router-go/internal/provider"
)

// ---- kv(scope='modelAliases'): alias -> "prefix/model" ----

func (s *Store) Aliases() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key,value FROM kv WHERE scope='modelAliases' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out, rows.Err()
}

// SetAlias stores the target as a JSON string, like XRouter-Next's kv helper.
func (s *Store) SetAlias(alias, model string) error {
	v, _ := json.Marshal(model)
	_, err := s.DB.Exec(`INSERT INTO kv(scope,key,value) VALUES('modelAliases',?,?) ON CONFLICT(scope,key) DO UPDATE SET value=excluded.value`, alias, string(v))
	return err
}

func (s *Store) DeleteAlias(alias string) error {
	r, err := s.DB.Exec(`DELETE FROM kv WHERE scope='modelAliases' AND key=?`, alias)
	return affected(r, err)
}

// ---- kv(scope='customModels'): "<prefix>|<id>|llm" -> {providerAlias,id,type,name} ----

type CustomModel struct {
	ProviderAlias string         `json:"providerAlias"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Name          string         `json:"name"`
	Caps          map[string]any `json:"caps,omitempty"` // explicit capability overrides (vision, reasoning, audioInput); absent = auto-detect
}

func (s *Store) CustomModels() ([]CustomModel, error) {
	rows, err := s.DB.Query(`SELECT value FROM kv WHERE scope='customModels' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomModel{}
	for rows.Next() {
		var raw string
		var m CustomModel
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &m) != nil || m.ID == "" || m.ProviderAlias == "" {
			continue
		}
		if m.Type == "" {
			m.Type = "llm"
		}
		if m.Name == "" {
			m.Name = m.ID
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddCustomModel upserts; re-adding keeps the stored fields and only updates a non-empty name.
func (s *Store) AddCustomModel(prefix, id, name string) error {
	key := prefix + "|" + id + "|llm"
	return s.withTx(func(tx *sql.Tx) error {
		cur := map[string]any{"providerAlias": prefix, "id": id, "type": "llm", "name": id}
		var raw string
		if err := tx.QueryRow(`SELECT value FROM kv WHERE scope='customModels' AND key=?`, key).Scan(&raw); err == nil {
			_ = json.Unmarshal([]byte(raw), &cur)
		}
		if name != "" {
			cur["name"] = name
		}
		v, _ := json.Marshal(cur)
		_, err := tx.Exec(`INSERT INTO kv(scope,key,value) VALUES('customModels',?,?) ON CONFLICT(scope,key) DO UPDATE SET value=excluded.value`, key, string(v))
		return err
	})
}

func (s *Store) DeleteCustomModel(prefix, id string) error {
	r, err := s.DB.Exec(`DELETE FROM kv WHERE scope='customModels' AND key=?`, prefix+"|"+id+"|llm")
	return affected(r, err)
}

// HasModel reports whether a model has been added to the provider with this prefix: only added models can be used.
func (s *Store) HasModel(prefix, id string) bool {
	if p, ok := provider.ByAlias(prefix); ok && p.Passthrough && strings.TrimSpace(id) != "" {
		return true // e.g. OpenCode Free: any id is accepted
	}
	var one int
	return s.DB.QueryRow(`SELECT 1 FROM kv WHERE scope='customModels' AND key=?`, prefix+"|"+id+"|llm").Scan(&one) == nil
}

// ModelCaps returns the explicit capability overrides of an added model (nil when none).
func (s *Store) ModelCaps(prefix, id string) map[string]any {
	var raw string
	if s.DB.QueryRow(`SELECT value FROM kv WHERE scope='customModels' AND key=?`, prefix+"|"+id+"|llm").Scan(&raw) != nil {
		return nil
	}
	var m CustomModel
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m.Caps
}

// SetModelCaps stores explicit capability overrides for an added model; an empty map goes back to auto-detection.
func (s *Store) SetModelCaps(prefix, id string, caps map[string]any) error {
	key := prefix + "|" + id + "|llm"
	return s.withTx(func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRow(`SELECT value FROM kv WHERE scope='customModels' AND key=?`, key).Scan(&raw); err != nil {
			return ErrNotFound
		}
		cur := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &cur)
		if len(caps) == 0 {
			delete(cur, "caps")
		} else {
			cur["caps"] = caps
		}
		v, _ := json.Marshal(cur)
		_, err := tx.Exec(`UPDATE kv SET value=? WHERE scope='customModels' AND key=?`, string(v), key)
		return err
	})
}
