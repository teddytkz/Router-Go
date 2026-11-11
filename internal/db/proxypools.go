package db

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// Proxy pools (XRouter-Next `proxyPools` table). A pool is either a plain proxy (http/https/socks5 URL) or a relay
// worker the user deployed to Vercel / Cloudflare; a connection opts in with providerSpecificData.proxyPoolId.

const (
	PoolHTTP       = "http"
	PoolVercel     = "vercel"
	PoolCloudflare = "cloudflare"
	// PoolPostcard is a relay that preserves the client's TLS identity, so an upstream that gates on it still
	// accepts the call. Unlike the plain relays it does not re-originate TLS with its own handshake.
	PoolPostcard = "postcard"
	// PoolNone in providerSpecificData.proxyPoolId means "no proxy" explicitly.
	PoolNone = "__none__"
)

type ProxyPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	ProxyURL     string `json:"proxyUrl"`
	NoProxy      string `json:"noProxy"`
	IsActive     bool   `json:"isActive"`
	StrictProxy  bool   `json:"strictProxy"` // fail the request instead of leaving over the direct IP
	TestStatus   string `json:"testStatus"`
	LastTestedAt string `json:"lastTestedAt,omitempty"`
	LastError    string `json:"lastError,omitempty"`
	RelayKey     string `json:"-"`       // shared secret of a deployed relay; never leaves the server
	Keyless      bool   `json:"keyless"` // a relay deployed elsewhere that does not check a key (e.g. XRouter-Next's); no key is sent and Test forwards a real request
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// InvalidError is a rejected input (HTTP 400), as opposed to a storage failure.
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

// IsRelay: the pool is a deployed forwarder (not a proxy). Fingerprint relays count too — they are deployed workers
// reached over HTTP, they just rebuild the client's TLS themselves.
func (p *ProxyPool) IsRelay() bool {
	return p.Type == PoolVercel || p.Type == PoolCloudflare || p.Type == PoolPostcard
}

// KeepsFingerprint: the relay resumes the client's own TLS, so a provider that refuses plain relays still accepts it.
func (p *ProxyPool) KeepsFingerprint() bool { return p.Type == PoolPostcard }

type poolData struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	ProxyURL     string `json:"proxyUrl"`
	NoProxy      string `json:"noProxy"`
	StrictProxy  bool   `json:"strictProxy"`
	LastTestedAt string `json:"lastTestedAt,omitempty"`
	LastError    string `json:"lastError,omitempty"`
	RelayKey     string `json:"relayKey,omitempty"`
	Keyless      bool   `json:"keyless,omitempty"`
}

func scanPool(sc interface{ Scan(...any) error }) (*ProxyPool, error) {
	var p ProxyPool
	var active int
	var status sql.NullString
	var raw string
	if err := sc.Scan(&p.ID, &active, &status, &raw, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	var d poolData
	_ = json.Unmarshal([]byte(raw), &d)
	p.IsActive, p.TestStatus = active == 1, status.String
	p.Name, p.Type, p.ProxyURL, p.NoProxy, p.StrictProxy = d.Name, d.Type, d.ProxyURL, d.NoProxy, d.StrictProxy
	p.LastTestedAt, p.LastError, p.RelayKey, p.Keyless = d.LastTestedAt, d.LastError, d.RelayKey, d.Keyless
	if p.Type == "" {
		p.Type = PoolHTTP
	}
	return &p, nil
}

const poolCols = `id,isActive,testStatus,data,createdAt,updatedAt`

func (s *Store) ProxyPools() ([]*ProxyPool, error) {
	rows, err := s.DB.Query(`SELECT ` + poolCols + ` FROM proxyPools ORDER BY updatedAt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyPool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ProxyPool(id string) (*ProxyPool, error) {
	p, err := scanPool(s.DB.QueryRow(`SELECT `+poolCols+` FROM proxyPools WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}

// ValidProxyURL accepts http(s) and socks5 proxies, or (for relays) the http(s) URL of the worker.
func ValidProxyURL(raw string, relay bool) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", InvalidError("proxy URL must look like http://host:port")
	}
	switch u.Scheme {
	case "http", "https":
	case "socks5", "socks5h":
		if relay {
			return "", InvalidError("a relay URL must be http(s)")
		}
	default:
		return "", InvalidError("proxy URL must start with http://, https:// or socks5://")
	}
	return raw, nil
}

func (p *ProxyPool) validate() error {
	p.Name = strings.TrimSpace(p.Name)
	switch {
	case p.Name == "" || len(p.Name) > 80:
		return InvalidError("name is required (max 80 characters)")
	case p.Type != PoolHTTP && !p.IsRelay():
		return InvalidError("type must be http, vercel, cloudflare or postcard")
	case p.Keyless && p.Type != PoolVercel && p.Type != PoolCloudflare:
		return InvalidError("only a Vercel or Cloudflare relay can be keyless")
	}
	u, err := ValidProxyURL(p.ProxyURL, p.IsRelay())
	p.ProxyURL = u
	return err
}

func (p *ProxyPool) data() string {
	b, _ := json.Marshal(poolData{p.Name, p.Type, p.ProxyURL, strings.TrimSpace(p.NoProxy), p.StrictProxy, p.LastTestedAt, p.LastError, p.RelayKey, p.Keyless})
	return string(b)
}

// CreateProxyPool stores a new pool (active, untested).
func (s *Store) CreateProxyPool(p *ProxyPool) error {
	if err := p.validate(); err != nil {
		return err
	}
	p.ID, p.IsActive, p.TestStatus, p.CreatedAt = uuid.NewString(), true, "unknown", now()
	p.UpdatedAt = p.CreatedAt
	_, err := s.DB.Exec(`INSERT INTO proxyPools(`+poolCols+`) VALUES(?,?,?,?,?,?)`, p.ID, boolInt(p.IsActive), nullIfEmpty(p.TestStatus), p.data(), p.CreatedAt, p.UpdatedAt)
	return err
}

// UpdateProxyPool applies fn to the stored pool and saves it (fn must not change the id).
func (s *Store) UpdateProxyPool(id string, fn func(*ProxyPool)) (*ProxyPool, error) {
	var out *ProxyPool
	err := s.withTx(func(tx *sql.Tx) error {
		p, err := scanPool(tx.QueryRow(`SELECT `+poolCols+` FROM proxyPools WHERE id=?`, id))
		if err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		fn(p)
		if err := p.validate(); err != nil {
			return err
		}
		p.UpdatedAt = now()
		_, err = tx.Exec(`UPDATE proxyPools SET isActive=?, testStatus=?, data=?, updatedAt=? WHERE id=?`, boolInt(p.IsActive), nullIfEmpty(p.TestStatus), p.data(), p.UpdatedAt, id)
		out = p
		return err
	})
	return out, err
}

// DeleteProxyPool removes a pool; connections that referenced it fall back to a direct connection.
func (s *Store) DeleteProxyPool(id string) error {
	res, err := s.DB.Exec(`DELETE FROM proxyPools WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.DB.Exec(`UPDATE providerConnections SET data=json_remove(data,'$.providerSpecificData.proxyPoolId') WHERE json_extract(data,'$.providerSpecificData.proxyPoolId')=?`, id)
	return err
}

// PoolUsage counts connections per proxy pool id.
func (s *Store) PoolUsage() map[string]int {
	out := map[string]int{}
	rows, err := s.DB.Query(`SELECT json_extract(data,'$.providerSpecificData.proxyPoolId'), COUNT(*) FROM providerConnections GROUP BY 1`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullString
		var n int
		if rows.Scan(&id, &n) == nil && id.String != "" {
			out[id.String] = n
		}
	}
	return out
}

// SetConnectionProxy points a connection at a proxy pool (PoolNone opts out); an empty id clears the setting.
func (s *Store) SetConnectionProxy(connID, poolID string) error {
	q, args := `UPDATE providerConnections SET data=json_remove(data,'$.providerSpecificData.proxyPoolId'), updatedAt=? WHERE id=?`, []any{now(), connID}
	if poolID != "" {
		q = `UPDATE providerConnections SET data=json_set(data,'$.providerSpecificData.proxyPoolId',?), updatedAt=? WHERE id=?`
		args = []any{poolID, now(), connID}
	}
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProxyAssignment binds one connection to a pool; an empty PoolID clears it.
type ProxyAssignment struct {
	ConnectionID string `json:"connectionId"`
	PoolID       string `json:"proxyPoolId"`
}

// SetConnectionProxies applies many assignments in one transaction (all or nothing), e.g. "Apply Proxy" on a provider.
func (s *Store) SetConnectionProxies(list []ProxyAssignment) error {
	return s.withTx(func(tx *sql.Tx) error {
		for _, a := range list {
			q, args := `UPDATE providerConnections SET data=json_remove(data,'$.providerSpecificData.proxyPoolId'), updatedAt=? WHERE id=?`, []any{now(), a.ConnectionID}
			if a.PoolID != "" {
				q, args = `UPDATE providerConnections SET data=json_set(data,'$.providerSpecificData.proxyPoolId',?), updatedAt=? WHERE id=?`, []any{a.PoolID, now(), a.ConnectionID}
			}
			res, err := tx.Exec(q, args...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
		}
		return nil
	})
}
