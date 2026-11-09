package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"router-go/internal/provider"
)

func base() string { p, _ := provider.ByID("codebuddy-intl"); return p.OAuthBase }

// protocol headers of the plugin sign-in endpoints (not credentials)
var signInHeaders = map[string]string{
	"X-Requested-With":   "XMLHttpRequest",
	"X-Domain":           "www.codebuddy.ai",
	"X-No-Authorization": "true",
	"X-No-User-Id":       "true",
	"X-Product":          "SaaS",
}

// CodeBuddy: ask for a sign-in state, open its authUrl in the browser, then poll the token endpoint with that state
// until the user has signed in (code 11217 = still waiting).
var oauth = &provider.OAuth{
	Kind: provider.OAuthPoll,
	Start: func(ctx context.Context, d provider.Doer, _ provider.StartParams) (provider.StartResult, error) {
		raw, status, err := provider.DoJSON(ctx, d, "POST", base()+"/v2/plugin/auth/state?platform=ide", signInHeaders, map[string]any{})
		if err != nil {
			return provider.StartResult{}, err
		}
		var r struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				State   string `json:"state"`
				AuthURL string `json:"authUrl"`
			} `json:"data"`
		}
		if status >= 300 || json.Unmarshal(raw, &r) != nil || r.Code != 0 || r.Data.State == "" || r.Data.AuthURL == "" {
			return provider.StartResult{}, errors.New("CodeBuddy sign-in could not start: " + firstNonEmpty(r.Msg, snippet(raw)))
		}
		return provider.StartResult{AuthURL: r.Data.AuthURL, State: r.Data.State, Interval: 5, ExpiresIn: 600}, nil
	},
	Poll: func(ctx context.Context, d provider.Doer, state string) (provider.Tokens, bool, error) {
		h := map[string]string{"X-No-Enterprise-Id": "true", "X-No-Department-Info": "true"}
		for k, v := range signInHeaders {
			h[k] = v
		}
		raw, status, err := provider.DoJSON(ctx, d, "GET", base()+"/v2/plugin/auth/token?state="+url.QueryEscape(state), h, nil)
		if err != nil {
			return provider.Tokens{}, false, err
		}
		if status >= 300 {
			return provider.Tokens{}, true, nil // transient: keep polling
		}
		var r struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int64  `json:"expiresIn"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return provider.Tokens{}, true, nil
		}
		switch {
		case r.Code == 0 && r.Data.AccessToken != "":
			return tokens(r.Data.AccessToken, r.Data.RefreshToken, r.Data.ExpiresIn), false, nil
		case r.Code == 11217:
			return provider.Tokens{}, true, nil
		}
		return provider.Tokens{}, false, errors.New("CodeBuddy sign-in failed: " + firstNonEmpty(r.Msg, "unknown error"))
	},
	Refresh: func(ctx context.Context, d provider.Doer, refreshToken string) (provider.Tokens, error) {
		h := map[string]string{"X-Refresh-Token": refreshToken, "X-Auth-Refresh-Source": "plugin"}
		for k, v := range signInHeaders {
			h[k] = v
		}
		delete(h, "X-No-Authorization")
		delete(h, "X-No-User-Id")
		raw, status, err := provider.DoJSON(ctx, d, "POST", base()+"/v2/plugin/auth/token/refresh", h, map[string]any{})
		if err != nil {
			return provider.Tokens{}, err
		}
		var r struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int64  `json:"expiresIn"`
			} `json:"data"`
		}
		if status >= 300 || json.Unmarshal(raw, &r) != nil || r.Code != 0 || r.Data.AccessToken == "" {
			return provider.Tokens{}, errors.New("CodeBuddy refresh failed: " + firstNonEmpty(r.Msg, snippet(raw)))
		}
		t := tokens(r.Data.AccessToken, r.Data.RefreshToken, r.Data.ExpiresIn)
		if t.RefreshToken == "" {
			t.RefreshToken = refreshToken
		}
		return t, nil
	},
}

func tokens(access, refresh string, expiresIn int64) provider.Tokens {
	if expiresIn <= 0 {
		expiresIn = 86400
	}
	return provider.Tokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second)}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
