package gateway

import (
	"testing"
	"time"
)

// No cooldown outlasts 30 s, however many times a key failed, except an account the upstream calls suspended.
func TestCooldownNeverExceedsThirtySeconds(t *testing.T) {
	const limit = 30 * time.Second
	for level := 0; level <= backoffMaxLevel; level++ {
		for _, tc := range []struct {
			status int
			body   string
		}{
			{429, `{"error":"Free usage limit reached"}`}, {429, ""}, {429, "rate limit"}, {401, ""}, {402, ""}, {403, ""}, {404, ""},
			{500, ""}, {503, "overloaded"}, {400, "no credentials"}, {400, "improperly formed request"}, {403, "request not allowed"},
		} {
			if d := classify(tc.status, tc.body, level); d.fallback && d.cooldown > limit {
				t.Errorf("status %d %q at level %d: cooldown %s", tc.status, tc.body, level, d.cooldown)
			}
		}
	}
	if d := classify(429, "", 0); d.cooldown != 2*time.Second {
		t.Errorf("first 429 should still wait 2s, got %s", d.cooldown)
	}
	if d := classify(429, "", 4); d.cooldown != 30*time.Second {
		t.Errorf("a repeated 429 should reach the 30s cap, got %s", d.cooldown)
	}
	if d := classify(403, "Your account has been suspended", 0); d.cooldown != permanentCooldown {
		t.Errorf("a suspended account must stay benched, got %s", d.cooldown)
	}
}
