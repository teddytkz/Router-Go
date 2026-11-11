package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type APIKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	MachineID string `json:"machineId"`
	IsActive  bool   `json:"isActive"`
	CreatedAt string `json:"createdAt"`
	Access    Access `json:"access"`
	// UsedTokens counts prompt+completion tokens served to this key; it is what Access.MaxTokens is checked against.
	UsedTokens int64 `json:"usedTokens"`
}

// Access is the per-key allow list. Restricted keys may only call listed combos/models.
type Access struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
	// MaxTokens is the total prompt+completion tokens the key may use; 0 means unlimited.
	MaxTokens int64 `json:"maxTokens"`
}

const (
	maxAllowEntries = 200
	maxAllowLen     = 256
)

// NormalizeAllow trims, drops empties and de-duplicates case-insensitively (first spelling wins).
func NormalizeAllow(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if k := strings.ToLower(e); e != "" && !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// ValidateAccess rejects (never repairs) oversized input and returns the normalised value.
func ValidateAccess(a Access) (Access, error) {
	if a.MaxTokens < 0 {
		return a, errors.New("access.maxTokens must be 0 (unlimited) or more")
	}
	if len(a.Allow) > maxAllowEntries {
		return a, errors.New("access.allow has more than 200 entries")
	}
	for _, e := range a.Allow {
		if len(strings.TrimSpace(e)) > maxAllowLen {
			return a, errors.New("access.allow entries must be at most 256 characters")
		}
	}
	a.Allow = NormalizeAllow(a.Allow)
	if !a.Restricted {
		a.Allow = []string{}
	}
	return a, nil
}

const keyCols = `id,key,name,machineId,isActive,createdAt,accessRestricted,accessAllow,maxTokens,usedTokens`

func scanKey(sc interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var name, mid sql.NullString
	var active, restricted, maxTok, usedTok sql.NullInt64
	var allow sql.NullString
	if err := sc.Scan(&k.ID, &k.Key, &name, &mid, &active, &k.CreatedAt, &restricted, &allow, &maxTok, &usedTok); err != nil {
		return nil, err
	}
	k.Name, k.MachineID, k.IsActive = name.String, mid.String, !active.Valid || active.Int64 != 0
	// fails closed: an unparsable allow list on a restricted key means "nothing allowed", never "everything"
	k.Access = Access{Restricted: restricted.Int64 == 1, Allow: []string{}, MaxTokens: maxTok.Int64}
	k.UsedTokens = usedTok.Int64
	if k.Access.Restricted && allow.String != "" {
		var l []string
		if json.Unmarshal([]byte(allow.String), &l) == nil {
			k.Access.Allow = NormalizeAllow(l)
		}
	}
	return &k, nil
}

func (s *Store) APIKeys() ([]*APIKey, error) {
	rows, err := s.DB.Query(`SELECT ` + keyCols + ` FROM apiKeys ORDER BY createdAt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) CreateAPIKey(name, machineID, key string, access Access) (*APIKey, error) {
	access, err := ValidateAccess(access)
	if err != nil {
		return nil, err
	}
	k := &APIKey{ID: uuid.NewString(), Key: key, Name: name, MachineID: machineID, IsActive: true, CreatedAt: now(), Access: access}
	aj, _ := json.Marshal(access.Allow)
	_, err = s.DB.Exec(`INSERT INTO apiKeys(id,key,name,machineId,isActive,createdAt,accessRestricted,accessAllow,maxTokens) VALUES(?,?,?,?,1,?,?,?,?)`,
		k.ID, k.Key, k.Name, k.MachineID, k.CreatedAt, boolInt(access.Restricted), string(aj), access.MaxTokens)
	return k, err
}

// SetAPIKeyAccess validates and stores the access list; invalid input returns an error that is safe to show.
func (s *Store) SetAPIKeyAccess(id string, access Access) error {
	access, err := ValidateAccess(access)
	if err != nil {
		return err
	}
	aj, _ := json.Marshal(access.Allow)
	r, err := s.DB.Exec(`UPDATE apiKeys SET accessRestricted=?, accessAllow=?, maxTokens=? WHERE id=?`, boolInt(access.Restricted), string(aj), access.MaxTokens, id)
	return affected(r, err)
}

// APIKeyByValue returns the active key record for a presented key, or nil.
func (s *Store) APIKeyByValue(key string) *APIKey {
	k, err := scanKey(s.DB.QueryRow(`SELECT `+keyCols+` FROM apiKeys WHERE key=?`, key))
	if err != nil || !k.IsActive {
		return nil
	}
	return k
}

// AddKeyTokens adds served tokens to the key's counter. Concurrent requests may overshoot MaxTokens by what
// was in flight (shortcut: the check runs before the call, not mid-stream; upgrade by reserving an estimate up front).
func (s *Store) AddKeyTokens(key string, n int) {
	if n > 0 {
		_, _ = s.DB.Exec(`UPDATE apiKeys SET usedTokens=COALESCE(usedTokens,0)+? WHERE key=?`, n, key)
	}
}

func (s *Store) SetAPIKeyActive(id string, active bool) error {
	r, err := s.DB.Exec(`UPDATE apiKeys SET isActive=? WHERE id=?`, boolInt(active), id)
	return affected(r, err)
}

// UpdateAPIKey renames a key and/or replaces its secret (nil leaves a field alone). A taken secret
// returns ErrConflict. Usage rows follow the new secret so history stays attributed to the key.
func (s *Store) UpdateAPIKey(id string, name, key *string) error {
	return s.withTx(func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRow(`SELECT key FROM apiKeys WHERE id=?`, id).Scan(&old); err != nil {
			return ErrNotFound
		}
		if name != nil {
			if _, err := tx.Exec(`UPDATE apiKeys SET name=? WHERE id=?`, *name, id); err != nil {
				return err
			}
		}
		if key != nil && *key != old {
			if _, err := tx.Exec(`UPDATE apiKeys SET key=? WHERE id=?`, *key, id); err != nil {
				if strings.Contains(err.Error(), "UNIQUE") {
					return ErrConflict
				}
				return err
			}
			if _, err := tx.Exec(`UPDATE usageHistory SET apiKey=? WHERE apiKey=?`, *key, old); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) DeleteAPIKey(id string) error {
	r, err := s.DB.Exec(`DELETE FROM apiKeys WHERE id=?`, id)
	return affected(r, err)
}

// ValidAPIKey reports whether key exists and is active.
func (s *Store) ValidAPIKey(key string) bool {
	var active sql.NullInt64
	if err := s.DB.QueryRow(`SELECT isActive FROM apiKeys WHERE key=?`, key).Scan(&active); err != nil {
		return false
	}
	return !active.Valid || active.Int64 != 0
}

func affected(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
