package db

import (
	"database/sql"
	"maps"
	"sort"
	"time"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

type Connection struct {
	ID, Provider, AuthType, Name, Email, CreatedAt, UpdatedAt string
	Priority                                                  int
	IsActive                                                  bool
	Data                                                      map[string]any // apiKey, providerSpecificData, testStatus, lastError, ...
}

// APIKey is the secret sent upstream: the API key, or the access token of an OAuth connection.
func (c *Connection) APIKey() string {
	if k := str(c.Data, "apiKey"); k != "" {
		return k
	}
	return str(c.Data, "accessToken")
}

func (c *Connection) IsOAuth() bool { return c.AuthType == "oauth" }

func (c *Connection) RefreshToken() string { return str(c.Data, "refreshToken") }

// TokenExpiry is when an OAuth access token expires (zero when unknown).
func (c *Connection) TokenExpiry() time.Time {
	t, _ := time.Parse(time.RFC3339, str(c.Data, "expiresAt"))
	return t
}

// RateLimitedUntil returns the zero time when the connection is not cooling down.
func (c *Connection) RateLimitedUntil() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, str(c.Data, "rateLimitedUntil"))
	return t
}

func (c *Connection) BackoffLevel() int { f, _ := c.Data["backoffLevel"].(float64); return int(f) }

// Flat is the XRouter-Next JSON shape. The API key is masked; it never leaves the server in full.
func (c *Connection) Flat() map[string]any {
	m := map[string]any{}
	for k, v := range c.Data {
		m[k] = v
	}
	delete(m, "apiKey")
	delete(m, "accessToken") // tokens never leave the server
	delete(m, "refreshToken")
	if psd, ok := m["providerSpecificData"].(map[string]any); ok && psd["clientSecret"] != nil { // an OIDC client secret is a credential too
		psd = maps.Clone(psd)
		delete(psd, "clientSecret")
		m["providerSpecificData"] = psd
	}
	m["apiKeyMasked"] = MaskKey(c.APIKey())
	if c.IsOAuth() {
		m["apiKeyMasked"] = ""
	}
	m["id"], m["provider"], m["authType"], m["name"] = c.ID, c.Provider, c.AuthType, c.Name
	m["priority"], m["isActive"], m["createdAt"], m["updatedAt"] = c.Priority, c.IsActive, c.CreatedAt, c.UpdatedAt
	return m
}

func MaskKey(k string) string {
	if len(k) <= 8 {
		return "••••"
	}
	return k[:4] + "••••" + k[len(k)-4:]
}

const connCols = `id,provider,authType,name,email,priority,isActive,data,createdAt,updatedAt`

func scanConn(sc interface{ Scan(...any) error }) (*Connection, error) {
	var c Connection
	var name, email sql.NullString
	var prio sql.NullInt64
	var active sql.NullInt64
	var data string
	if err := sc.Scan(&c.ID, &c.Provider, &c.AuthType, &name, &email, &prio, &active, &data, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Name, c.Email, c.Priority, c.IsActive, c.Data = name.String, email.String, int(prio.Int64), !active.Valid || active.Int64 != 0, splitData(data)
	return &c, nil
}

// Connections lists by provider (empty = all), ordered by priority then creation.
func (s *Store) Connections(provider string, onlyActive bool) ([]*Connection, error) {
	q, args := `SELECT `+connCols+` FROM providerConnections WHERE 1=1`, []any{}
	if provider != "" {
		q += ` AND provider=?`
		args = append(args, provider)
	}
	if onlyActive {
		q += ` AND isActive=1`
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Connection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Priority, out[j].Priority
		if pi == 0 {
			pi = 999
		}
		if pj == 0 {
			pj = 999
		}
		return pi < pj
	})
	return out, rows.Err()
}

func (s *Store) Connection(id string) (*Connection, error) {
	c, err := scanConn(s.DB.QueryRow(`SELECT `+connCols+` FROM providerConnections WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

// CreateConnection adds an API-key connection to a node. A duplicate name returns ErrConflict
// (XRouter-Next refuses to silently overwrite a pool entry either).
func (s *Store) CreateConnection(node *Node, name, apiKey string) (*Connection, error) {
	t := now()
	c := &Connection{
		ID: uuid.NewString(), Provider: node.ID, AuthType: "apikey", Name: name, IsActive: true, CreatedAt: t, UpdatedAt: t,
		Data: map[string]any{
			"apiKey":               apiKey,
			"testStatus":           "unknown",
			"providerSpecificData": map[string]any{"prefix": node.Prefix(), "apiType": node.APIType(), "baseUrl": node.BaseURL(), "nodeName": node.Name},
		},
	}
	err := s.withTx(func(tx *sql.Tx) error {
		var dup int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM providerConnections WHERE provider=? AND authType='apikey' AND name=?`, node.ID, name).Scan(&dup)
		if dup > 0 {
			return ErrConflict
		}
		_ = tx.QueryRow(`SELECT COALESCE(MAX(priority),0)+1 FROM providerConnections WHERE provider=?`, node.ID).Scan(&c.Priority)
		_, err := tx.Exec(`INSERT INTO providerConnections(`+connCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.Provider, c.AuthType, c.Name, nil, c.Priority, 1, joinData(c.Data), t, t)
		return err
	})
	return c, err
}

// PatchConnection updates columns (name, priority, isActive) and merges data fields (apiKey, testStatus, ...).
// A nil data value deletes that key.
func (s *Store) PatchConnection(id string, cols map[string]any, data map[string]any) (*Connection, error) {
	var out *Connection
	err := s.withTx(func(tx *sql.Tx) error {
		c, err := scanConn(tx.QueryRow(`SELECT `+connCols+` FROM providerConnections WHERE id=?`, id))
		if err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if v, ok := cols["name"].(string); ok {
			c.Name = v
		}
		if v, ok := cols["priority"].(float64); ok {
			c.Priority = int(v)
		}
		if v, ok := cols["isActive"].(bool); ok {
			c.IsActive = v
		}
		for k, v := range data {
			if v == nil {
				delete(c.Data, k)
			} else {
				c.Data[k] = v
			}
		}
		c.UpdatedAt = now()
		_, err = tx.Exec(`UPDATE providerConnections SET name=?, priority=?, isActive=?, data=?, updatedAt=? WHERE id=?`,
			c.Name, c.Priority, boolInt(c.IsActive), joinData(c.Data), c.UpdatedAt, id)
		out = c
		return err
	})
	return out, err
}

// MarkConnection records a request outcome. Pass err=="" for success (clears cooldown/backoff).
func (s *Store) MarkConnection(id string, status int, errMsg string, cooldown time.Duration, backoffLevel int) {
	d := map[string]any{}
	if errMsg == "" {
		d = map[string]any{"testStatus": "active", "lastError": nil, "lastErrorAt": nil, "errorCode": nil, "rateLimitedUntil": nil, "backoffLevel": 0}
	} else {
		if len(errMsg) > 500 {
			errMsg = errMsg[:500]
		}
		d = map[string]any{"testStatus": "error", "lastError": errMsg, "lastErrorAt": now(), "errorCode": status,
			"rateLimitedUntil": time.Now().Add(cooldown).UTC().Format(time.RFC3339Nano), "backoffLevel": backoffLevel}
	}
	_, _ = s.PatchConnection(id, nil, d)
}

func (s *Store) DeleteConnection(id string) error {
	return s.withTx(func(tx *sql.Tx) error {
		var provider string
		if err := tx.QueryRow(`SELECT provider FROM providerConnections WHERE id=?`, id).Scan(&provider); err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM providerConnections WHERE id=?`, id); err != nil {
			return err
		}
		// keep priorities contiguous 1..N
		_, err := tx.Exec(`UPDATE providerConnections SET priority = (
			SELECT COUNT(*) FROM providerConnections b WHERE b.provider=providerConnections.provider
			AND (COALESCE(b.priority,999) < COALESCE(providerConnections.priority,999)
			  OR (COALESCE(b.priority,999) = COALESCE(providerConnections.priority,999) AND b.id <= providerConnections.id))
		) WHERE provider=?`, provider)
		return err
	})
}

// SaveOAuthConnection stores the tokens of a browser sign-in. A second sign-in with the same account (email) updates
// that connection's tokens instead of adding a duplicate.
func (s *Store) SaveOAuthConnection(node *Node, t provider.Tokens) (*Connection, error) {
	name := t.Email
	if name == "" {
		name = t.Name
	}
	if name == "" {
		name = "OAuth account"
	}
	data := map[string]any{"accessToken": t.AccessToken, "testStatus": "unknown"}
	if t.RefreshToken != "" {
		data["refreshToken"] = t.RefreshToken
	}
	if !t.ExpiresAt.IsZero() {
		data["expiresAt"] = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	psd := map[string]any{"prefix": node.Prefix(), "apiType": node.APIType(), "baseUrl": node.BaseURL(), "nodeName": node.Name}
	for k, v := range t.Extra {
		psd[k] = v
	}
	data["providerSpecificData"] = psd
	if t.Email != "" {
		data["email"] = t.Email
	}
	var existing *Connection
	if conns, err := s.Connections(node.ID, false); err == nil {
		for _, c := range conns {
			if c.IsOAuth() && c.Name == name {
				existing = c
			}
		}
	}
	if existing != nil {
		return s.PatchConnection(existing.ID, map[string]any{"isActive": true}, data)
	}
	conn := &Connection{ID: uuid.NewString(), Provider: node.ID, AuthType: "oauth", Name: name, Email: t.Email, IsActive: true, Data: data}
	at := now()
	conn.CreatedAt, conn.UpdatedAt = at, at
	err := s.withTx(func(tx *sql.Tx) error {
		_ = tx.QueryRow(`SELECT COALESCE(MAX(priority),0)+1 FROM providerConnections WHERE provider=?`, node.ID).Scan(&conn.Priority)
		_, err := tx.Exec(`INSERT INTO providerConnections(`+connCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			conn.ID, conn.Provider, conn.AuthType, conn.Name, nullIfEmpty(t.Email), conn.Priority, 1, joinData(conn.Data), at, at)
		return err
	})
	return conn, err
}

// UpdateTokens stores refreshed tokens on a connection.
func (s *Store) UpdateTokens(id string, t provider.Tokens) error {
	data := map[string]any{"accessToken": t.AccessToken, "testStatus": "unknown", "lastError": nil}
	if t.RefreshToken != "" {
		data["refreshToken"] = t.RefreshToken
	}
	if !t.ExpiresAt.IsZero() {
		data["expiresAt"] = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if len(t.Extra) > 0 { // provider-specific data learned on refresh (profile ARN, …) is merged, never replaced
		c, err := s.Connection(id)
		if err != nil {
			return err
		}
		psd := map[string]any{}
		old, _ := c.Data["providerSpecificData"].(map[string]any)
		maps.Copy(psd, old)
		maps.Copy(psd, t.Extra)
		data["providerSpecificData"] = psd
	}
	_, err := s.PatchConnection(id, nil, data)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EnsureDefaultConnection gives a provider that needs no key (OpenCode Free) its one keyless connection when it has
// none at all, so using it never starts with "add an API key". A connection the user disabled is left alone.
func (s *Store) EnsureDefaultConnection(node *Node) error {
	all, err := s.Connections(node.ID, false)
	if err != nil || len(all) > 0 {
		return err
	}
	_, err = s.CreateConnection(node, "Default", "")
	return err
}
