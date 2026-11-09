package db

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type Combo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          *string  `json:"kind"`
	Models        []string `json:"models"`
	Strategy      string   `json:"strategy"` // filled from settings.comboStrategies
	ContextLength *int     `json:"context_length"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

var comboNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func ValidComboName(n string) bool { return comboNameRe.MatchString(n) }

const comboCols = `id,name,kind,models,context_length,createdAt,updatedAt`

func scanCombo(sc interface{ Scan(...any) error }) (*Combo, error) {
	var c Combo
	var kind sql.NullString
	var models string
	var ctx sql.NullInt64
	if err := sc.Scan(&c.ID, &c.Name, &kind, &models, &ctx, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if kind.Valid {
		c.Kind = &kind.String
	}
	if ctx.Valid {
		v := int(ctx.Int64)
		c.ContextLength = &v
	}
	c.Models = []string{}
	_ = json.Unmarshal([]byte(models), &c.Models)
	return &c, nil
}

// withStrategy fills Combo.Strategy from settings (per-combo override, else global, else "fallback").
func (s *Store) withStrategy(cs ...*Combo) {
	set, _ := s.Settings()
	per, _ := set["comboStrategies"].(map[string]any)
	global, _ := set["comboStrategy"].(string)
	for _, c := range cs {
		c.Strategy = "fallback"
		if global == "round-robin" {
			c.Strategy = global
		}
		if m, ok := per[c.Name].(map[string]any); ok {
			if v, _ := m["fallbackStrategy"].(string); v == "fallback" || v == "round-robin" {
				c.Strategy = v
			}
		}
	}
}

func (s *Store) Combos() ([]*Combo, error) {
	rows, err := s.DB.Query(`SELECT ` + comboCols + ` FROM combos ORDER BY createdAt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Combo{}
	for rows.Next() {
		c, err := scanCombo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.withStrategy(out...)
	return out, nil
}

func (s *Store) comboWhere(col, val string) (*Combo, error) {
	c, err := scanCombo(s.DB.QueryRow(`SELECT `+comboCols+` FROM combos WHERE `+col+`=?`, val))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	s.withStrategy(c)
	return c, nil
}

func (s *Store) ComboByID(id string) (*Combo, error)     { return s.comboWhere("id", id) }
func (s *Store) ComboByName(name string) (*Combo, error) { return s.comboWhere("name", name) }

// setStrategy records the per-combo strategy under settings.comboStrategies[name] (XRouter-Next layout);
// an empty strategy keeps the existing entry (moved to the new name) when keep is set, else removes it.
func (s *Store) setStrategy(old, name, strategy string, keep bool) error {
	set, err := s.Settings()
	if err != nil {
		return err
	}
	per, _ := set["comboStrategies"].(map[string]any)
	next := map[string]any{}
	for k, v := range per {
		if k != old && k != name {
			next[k] = v
		}
	}
	prev, _ := per[old].(map[string]any)
	if strategy != "" {
		e := map[string]any{}
		for k, v := range prev {
			e[k] = v
		}
		e["fallbackStrategy"] = strategy
		next[name] = e
	} else if keep && len(prev) > 0 {
		next[name] = prev
	}
	return s.UpdateSettings(map[string]any{"comboStrategies": next})
}

func cleanModels(in []string) []string {
	out := []string{}
	for _, m := range in {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) CreateCombo(name string, models []string, strategy string, ctxLen *int) (*Combo, error) {
	if _, err := s.ComboByName(name); err == nil {
		return nil, ErrConflict
	}
	t := now()
	c := &Combo{ID: uuid.NewString(), Name: name, Models: cleanModels(models), ContextLength: ctxLen, CreatedAt: t, UpdatedAt: t}
	mj, _ := json.Marshal(c.Models)
	if _, err := s.DB.Exec(`INSERT INTO combos(`+comboCols+`) VALUES(?,?,NULL,?,?,?,?)`, c.ID, c.Name, string(mj), ctxLen, t, t); err != nil {
		return nil, err
	}
	if err := s.setStrategy(name, name, strategy, false); err != nil {
		return nil, err
	}
	s.withStrategy(c)
	return c, nil
}

// UpdateCombo applies a partial update; nil fields are left alone.
func (s *Store) UpdateCombo(id string, name *string, models []string, strategy *string, ctxLen **int) (*Combo, error) {
	c, err := s.ComboByID(id)
	if err != nil {
		return nil, err
	}
	old := c.Name
	if name != nil && *name != c.Name {
		if _, err := s.ComboByName(*name); err == nil {
			return nil, ErrConflict
		}
		c.Name = *name
	}
	if models != nil {
		c.Models = cleanModels(models)
	}
	if ctxLen != nil {
		c.ContextLength = *ctxLen
	}
	c.UpdatedAt = now()
	mj, _ := json.Marshal(c.Models)
	if _, err := s.DB.Exec(`UPDATE combos SET name=?, models=?, context_length=?, updatedAt=? WHERE id=?`, c.Name, string(mj), c.ContextLength, c.UpdatedAt, id); err != nil {
		return nil, err
	}
	st := ""
	if strategy != nil {
		st = *strategy
	}
	if err := s.setStrategy(old, c.Name, st, true); err != nil {
		return nil, err
	}
	s.withStrategy(c)
	return c, nil
}

func (s *Store) DeleteCombo(id string) error {
	c, err := s.ComboByID(id)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`DELETE FROM combos WHERE id=?`, id); err != nil {
		return err
	}
	return s.setStrategy(c.Name, c.Name, "", false)
}

// NormalizeList trims entries and drops empties and exact duplicates, keeping order.
func NormalizeList(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range cleanModels(in) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
