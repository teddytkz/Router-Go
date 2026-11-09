package cline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"router-go/internal/provider"
)

func base() string { p, _ := provider.ByID("cline"); return p.OAuthBase }

// Cline: the browser signs in at api.cline.bot and is redirected back with ?code=…; the code is the token bundle
// itself, base64-encoded JSON {accessToken, refreshToken, email, expiresAt, …} (with a token-exchange call as fallback).
var oauth = &provider.OAuth{
	Kind: provider.OAuthCode,
	Start: func(_ context.Context, _ provider.Doer, p provider.StartParams) (provider.StartResult, error) {
		q := url.Values{"client_type": {"extension"}, "callback_url": {p.RedirectURI}, "redirect_uri": {p.RedirectURI}}
		return provider.StartResult{AuthURL: base() + "/api/v1/auth/authorize?" + q.Encode(), ExpiresIn: 600}, nil
	},
	Exchange: func(ctx context.Context, d provider.Doer, code, redirectURI string) (provider.Tokens, error) {
		if t, ok := decodeCode(code); ok {
			return t, nil
		}
		raw, status, err := provider.DoJSON(ctx, d, "POST", base()+"/api/v1/auth/token", nil,
			map[string]any{"grant_type": "authorization_code", "code": code, "client_type": "extension", "redirect_uri": redirectURI})
		if err != nil {
			return provider.Tokens{}, err
		}
		if status >= 300 {
			return provider.Tokens{}, errors.New("Cline token exchange failed: " + snippet(raw))
		}
		return parseTokens(raw)
	},
	Refresh: func(ctx context.Context, d provider.Doer, refreshToken string) (provider.Tokens, error) {
		raw, status, err := provider.DoJSON(ctx, d, "POST", base()+"/api/v1/auth/refresh", nil,
			map[string]any{"refreshToken": refreshToken, "grantType": "refresh_token", "clientType": "extension"})
		if err != nil {
			return provider.Tokens{}, err
		}
		if status >= 300 {
			return provider.Tokens{}, errors.New("Cline refresh failed: " + snippet(raw))
		}
		t, err := parseTokens(raw)
		if t.RefreshToken == "" {
			t.RefreshToken = refreshToken
		}
		return t, err
	},
}

// workosSecret: Cline OAuth access tokens are WorkOS JWTs and must be sent as "workos:<jwt>"; API keys go as they are.
func workosSecret(token string) string {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(strings.ToLower(token), "workos:") || !strings.HasPrefix(token, "eyJ") {
		return token
	}
	return "workos:" + token
}

type tokenJSON struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	Email        string `json:"email"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	ExpiresAt    string `json:"expiresAt"`
	UserInfo     struct {
		Email string `json:"email"`
	} `json:"userInfo"`
}

func (j tokenJSON) tokens() provider.Tokens {
	t := provider.Tokens{AccessToken: j.AccessToken, RefreshToken: j.RefreshToken, Email: j.Email, Name: strings.TrimSpace(j.FirstName + " " + j.LastName)}
	if t.Email == "" {
		t.Email = j.UserInfo.Email
	}
	if ts, err := time.Parse(time.RFC3339, j.ExpiresAt); err == nil {
		t.ExpiresAt = ts
	}
	return t
}

// decodeCode reads the base64 JSON bundle Cline puts in the code parameter.
func decodeCode(code string) (provider.Tokens, bool) {
	code = strings.TrimSpace(code)
	for len(code)%4 != 0 {
		code += "="
	}
	raw, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(code)
	}
	if err != nil {
		return provider.Tokens{}, false
	}
	end := strings.LastIndexByte(string(raw), '}')
	if end < 0 {
		return provider.Tokens{}, false
	}
	var j tokenJSON
	if json.Unmarshal(raw[:end+1], &j) != nil || j.AccessToken == "" {
		return provider.Tokens{}, false
	}
	return j.tokens(), true
}

// parseTokens reads {data:{…}} or a bare token object.
func parseTokens(raw []byte) (provider.Tokens, error) {
	var env struct {
		Data tokenJSON `json:"data"`
		tokenJSON
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return provider.Tokens{}, err
	}
	j := env.Data
	if j.AccessToken == "" {
		j = env.tokenJSON
	}
	if j.AccessToken == "" {
		return provider.Tokens{}, errors.New("Cline returned no access token")
	}
	return j.tokens(), nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
