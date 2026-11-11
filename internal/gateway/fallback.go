package gateway

import (
	"strings"
	"time"
)

// Constants ported from XRouter-Next open-sse/config/errorConfig.js, except that no cooldown lasts longer than
// 30 s (XRouter-Next backs off to 5 min and benches a rejected key for 2 min) so a key comes back quickly. The one
// exception is permanentCooldown: an account the upstream says is suspended or revoked is not retried every 30 s.
const (
	backoffBase       = 2 * time.Second
	backoffMax        = 30 * time.Second
	backoffMaxLevel   = 15
	transientCooldown = 30 * time.Second
	longCooldown      = 30 * time.Second
	shortCooldown     = 5 * time.Second
	permanentCooldown = 24 * time.Hour

	maxPermanentFailures = 5 // consecutive permanently rejected keys before a request stops walking the pool
)

var permanentPatterns = []string{
	"temporarily suspended", "account suspended", "account has been suspended", "temporarily blocked",
	"account has been disabled", "account is disabled", "bearer token included in the request is invalid",
	"token has been revoked", "token was revoked", "refresh_token_reused", "invalid_grant",
}

type decision struct {
	fallback bool          // try the next connection
	cooldown time.Duration // how long to bench this connection
	level    int           // new backoff level
}

func quotaCooldown(level int) time.Duration {
	d := backoffBase << max(0, level-1)
	if d <= 0 || d > backoffMax {
		return backoffMax
	}
	return d
}

// classify decides whether an upstream failure is the credential's fault (rotate + cool down)
// or the request's fault (hand the upstream error back untouched). Mirrors checkFallbackError.
func classify(status int, body string, level int) decision {
	text := strings.ToLower(body)
	backoff := func() decision {
		l := min(level+1, backoffMaxLevel)
		return decision{true, quotaCooldown(l), l}
	}

	if status >= 400 && status <= 403 && status != 404 {
		for _, p := range permanentPatterns {
			if strings.Contains(text, p) {
				return decision{true, permanentCooldown, level}
			}
		}
	}
	rules := []struct {
		text    string
		cool    time.Duration
		backoff bool
		scoped  bool
	}{
		{"unapproved channel", 0, true, false},                                           // repeated rejected calls can get the account banned: back off, do not hammer
		{"request illegal", 0, true, false}, {"conversation id missing", 0, true, false}, // CodeBuddy intl gateway blocks: back the account off
		{"no credentials", longCooldown, false, false},
		{"request not allowed", shortCooldown, false, false},
		{"improperly formed request", longCooldown, false, false},
		{"rate limit", 0, true, false}, {"too many requests", 0, true, false}, {"quota exceeded", 0, true, false},
		{"insufficient credit", 0, true, false}, {"credit balance is too low", 0, true, false},
		{"capacity", 0, true, false}, {"overloaded", 0, true, false},
	}
	for _, r := range rules {
		if text != "" && strings.Contains(text, r.text) {
			switch {
			case r.scoped:
				return decision{}
			case r.backoff:
				return backoff()
			}
			return decision{true, r.cool, level}
		}
	}
	switch status {
	case 401, 402, 403, 404:
		return decision{true, longCooldown, level}
	case 429:
		return backoff()
	}
	// other 4xx: the request is malformed/oversized, the credential is fine
	if status >= 400 && status < 500 {
		return decision{}
	}
	return decision{true, transientCooldown, level}
}
