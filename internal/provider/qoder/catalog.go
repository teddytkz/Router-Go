package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"router-go/internal/provider"
)

const (
	// openapi is where the PAT exchange and userinfo live; the catalog and chat live on the inference host.
	openAPIBase = "https://openapi.qoder.sh"
	// chatAlt serves job-token traffic: api3 answers it with 403 "Login expired".
	chatAltBase = "https://api2.qoder.sh"
	ideHost     = "https://api3.qoder.sh"

	exchangePath = "/api/v1/jobToken/exchange"
	userInfoPath = "/api/v1/userinfo"
	modelListHr  = "/algo/api/v2/model/list"

	catalogTTL = time.Hour
	credTTL    = 24 * time.Hour
	// A job token this close to expiry is exchanged again.
	credBuffer = 5 * time.Minute
)

// staticConfigs cover models the per-account catalog leaves out even though the endpoint serves them.
var staticConfigs = map[string]map[string]any{
	"smodel": {"model": "", "format": "openai", "source": "system", "url": "", "key": "smodel",
		"display_name": "Sonus", "is_vl": true, "is_reasoning": true, "max_input_tokens": 180000},
	"cmodel": {"model": "", "format": "openai", "source": "system", "url": "", "key": "cmodel",
		"display_name": "Cantus", "is_vl": true, "is_reasoning": true, "max_input_tokens": 180000},
}

type cred struct {
	token, userID string
	expires       time.Time
}

type catalog struct {
	configs map[string]map[string]any
	expires time.Time
}

var (
	cacheMu  sync.Mutex
	credMemo = map[string]cred{}    // PAT → job token
	catMemo  = map[string]catalog{} // userId → model configs
)

// resolveCreds turns a PAT into the short-lived job token COSY signs with, plus the user id it needs. A device token
// (dt-) is used as it is, and the stored user id stands in for the exchange's.
func resolveCreds(ctx context.Context, do provider.Doer, base, token, userID string) (string, string, error) {
	if !strings.HasPrefix(token, "pt-") {
		return token, userID, nil
	}
	cacheMu.Lock()
	c, ok := credMemo[token]
	cacheMu.Unlock()
	if ok && time.Until(c.expires) > credBuffer {
		return c.token, c.userID, nil
	}
	raw, status, err := provider.DoJSON(ctx, do, "POST", apiURL(base, openAPIBase)+exchangePath,
		map[string]string{"User-Agent": "qodercli/1.0.0", "Cosy-Version": ideVersion, "Cosy-ClientType": clientType},
		map[string]any{"personal_token": token})
	if err != nil {
		return "", "", fmt.Errorf("qoder PAT exchange: %w", err)
	}
	if status != 200 {
		return "", "", fmt.Errorf("qoder PAT exchange: %d %s", status, provider.Snippet(raw))
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Token == "" {
		return "", "", errors.New("qoder PAT exchange returned no job token")
	}
	c = cred{token: out.Token, userID: userID, expires: time.Now().Add(credTTL)}
	if out.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
			c.expires = t
		}
	} else if out.ExpiresIn > 0 {
		c.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if c.userID == "" {
		c.userID = fetchUserID(ctx, do, apiURL(base, openAPIBase)+userInfoPath, out.Token)
	}
	if c.userID == "" {
		return "", "", errors.New("qoder PAT exchange returned no user id; use a token from an account that has one")
	}
	cacheMu.Lock()
	credMemo[token] = c
	cacheMu.Unlock()
	return c.token, c.userID, nil
}

func fetchUserID(ctx context.Context, do provider.Doer, url, token string) string {
	raw, status, err := provider.DoJSON(ctx, do, "GET", url, map[string]string{
		"Authorization": "Bearer " + token, "User-Agent": "qodercli/1.0.0"}, nil)
	if err != nil || status != 200 {
		return ""
	}
	var out struct {
		ID      string `json:"id"`
		UserID  string `json:"userId"`
		UserID2 string `json:"user_id"`
	}
	_ = json.Unmarshal(raw, &out)
	return cmpOr(out.ID, cmpOr(out.UserID, out.UserID2))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// modelConfig is the catalog block for key. A live entry always wins; a model the account catalog omits falls back to
// the static block, and an unknown key is an error (sending the wrong block silently swaps the upstream model).
func modelConfig(ctx context.Context, do provider.Doer, base, token, userID, key string) (map[string]any, error) {
	configs, err := catalogFor(ctx, do, base, token, userID)
	if err == nil {
		if cfg, ok := configs[key]; ok {
			c := make(map[string]any, len(cfg))
			for k, v := range cfg {
				c[k] = v
			}
			c["key"] = key
			return c, nil
		}
	}
	if cfg, ok := staticConfigs[key]; ok {
		c := make(map[string]any, len(cfg))
		for k, v := range cfg {
			c[k] = v
		}
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("qoder model_config for %q: %w", key, err)
	}
	return nil, fmt.Errorf("qoder model_config for %q is not in the account's model list", key)
}

// catalogFor returns the account's model_config blocks, cached for an hour. Every fetched entry is kept, including the
// ones flagged enable:false: the chat endpoint still serves them.
func catalogFor(ctx context.Context, do provider.Doer, base, token, userID string) (map[string]map[string]any, error) {
	cacheMu.Lock()
	c, ok := catMemo[userID]
	cacheMu.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.configs, nil
	}
	url := apiURL(base, ideHost) + modelListHr
	headers, err := cosyHeaders(nil, url, cosyCreds{UserID: userID, Token: token})
	if err != nil {
		return nil, err
	}
	headers["Accept"] = "application/json"
	headers["Accept-Encoding"] = "identity"
	raw, status, err := provider.DoJSON(ctx, do, "GET", url, headers, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("model list: %d %s", status, provider.Snippet(raw))
	}
	var body struct {
		Chat []map[string]any `json:"chat"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	configs := make(map[string]map[string]any, len(body.Chat))
	for _, e := range body.Chat {
		if k, _ := e["key"].(string); k != "" {
			configs[k] = e
		}
	}
	if len(configs) == 0 {
		return nil, errors.New("model list is empty")
	}
	cacheMu.Lock()
	catMemo[userID] = catalog{configs: configs, expires: time.Now().Add(catalogTTL)}
	cacheMu.Unlock()
	return configs, nil
}

// apiURL keeps side calls on the provider's own host: a BaseURL that is not a Qoder host (tests, a mirror) answers all
// of them, while the real hosts keep their own roles (exchange on openapi, catalog on the inference host).
func apiURL(inferenceBase, host string) string {
	if qoderHost(inferenceBase) {
		return host
	}
	return strings.TrimRight(inferenceBase, "/")
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func itoa(n int) string { return strconv.Itoa(n) }
