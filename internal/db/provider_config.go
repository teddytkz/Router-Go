package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Per-provider settings that live next to the connections: extra request headers, and how the key pool is used.
// Stored in kv so they follow the provider id (custom node or built-in) without a schema change.

const (
	scopeHeaders = "providerHeaders"
	scopeConfig  = "providerConfig"
)

// ProviderHeaders returns the user's header overrides for a provider (nil when none).
func (s *Store) ProviderHeaders(id string) map[string]string {
	var raw string
	if s.DB.QueryRow(`SELECT value FROM kv WHERE scope=? AND key=?`, scopeHeaders, id).Scan(&raw) != nil {
		return nil
	}
	m := map[string]string{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

var forbiddenHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "authorization": true, "x-api-key": true, "content-type": true}

// ValidateHeaders checks user-supplied header overrides; authentication headers belong to the keys, not here.
func ValidateHeaders(h map[string]string) (map[string]string, error) {
	if len(h) > 30 {
		return nil, errors.New("at most 30 headers")
	}
	out := map[string]string{}
	for k, v := range h {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case k == "":
			return nil, errors.New("a header name is empty")
		case strings.ContainsAny(k, " :\t\r\n") || len(k) > 100:
			return nil, errors.New("invalid header name: " + k)
		case forbiddenHeaders[strings.ToLower(k)]:
			return nil, errors.New(k + " is managed by the router (use API keys for credentials)")
		case strings.ContainsAny(v, "\r\n") || len(v) > 1000:
			return nil, errors.New("invalid value for " + k)
		}
		out[k] = v
	}
	return out, nil
}

// SetProviderHeaders replaces the overrides; an empty map removes them (back to the provider's defaults).
func (s *Store) SetProviderHeaders(id string, h map[string]string) error {
	if len(h) == 0 {
		_, err := s.DB.Exec(`DELETE FROM kv WHERE scope=? AND key=?`, scopeHeaders, id)
		return err
	}
	v, _ := json.Marshal(h)
	_, err := s.DB.Exec(`INSERT INTO kv(scope,key,value) VALUES(?,?,?) ON CONFLICT(scope,key) DO UPDATE SET value=excluded.value`, scopeHeaders, id, string(v))
	return err
}

// ProviderConfig: how the provider's key pool is used.
type ProviderConfig struct {
	RoundRobin bool `json:"roundRobin"` // rotate the starting key instead of always trying them in priority order
	Sticky     int  `json:"sticky"`     // requests served by one key before rotating (≥1)
}

func (s *Store) ProviderConfig(id string) ProviderConfig {
	c := ProviderConfig{Sticky: 1}
	var raw string
	if err := s.DB.QueryRow(`SELECT value FROM kv WHERE scope=? AND key=?`, scopeConfig, id).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	} else if err != sql.ErrNoRows {
		return c
	}
	if c.Sticky < 1 {
		c.Sticky = 1
	}
	return c
}

func (s *Store) SetProviderConfig(id string, c ProviderConfig) error {
	if c.Sticky < 1 || c.Sticky > 1000 {
		return errors.New("sticky must be between 1 and 1000")
	}
	v, _ := json.Marshal(c)
	_, err := s.DB.Exec(`INSERT INTO kv(scope,key,value) VALUES(?,?,?) ON CONFLICT(scope,key) DO UPDATE SET value=excluded.value`, scopeConfig, id, string(v))
	return err
}

// ProviderConfigs returns every stored config by provider id.
func (s *Store) ProviderConfigs() map[string]ProviderConfig {
	out := map[string]ProviderConfig{}
	rows, err := s.DB.Query(`SELECT key,value FROM kv WHERE scope=?`, scopeConfig)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		c := ProviderConfig{Sticky: 1}
		if rows.Scan(&k, &v) == nil && json.Unmarshal([]byte(v), &c) == nil {
			if c.Sticky < 1 {
				c.Sticky = 1
			}
			out[k] = c
		}
	}
	return out
}
