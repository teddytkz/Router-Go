package freebuff

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"router-go/internal/provider"
)

func base() string { p, _ := provider.ByID("freebuff"); return p.OAuthBase }

// Freebuff: not OAuth2 but a device fingerprint flow. Ask freebuff.com for a login code for this install, open the
// returned login URL, then poll the status endpoint until it returns the user (and their auth token).
// The fingerprint id/hash and expiry travel inside State.
var oauth = &provider.OAuth{
	Kind: provider.OAuthPoll,
	Start: func(ctx context.Context, d provider.Doer, p provider.StartParams) (provider.StartResult, error) {
		raw, status, err := provider.DoJSON(ctx, d, "POST", base()+"/api/auth/cli/code", nil, map[string]any{"fingerprintId": p.Fingerprint})
		if err != nil {
			return provider.StartResult{}, err
		}
		var r struct {
			FingerprintID   string  `json:"fingerprintId"`
			FingerprintHash string  `json:"fingerprintHash"`
			LoginURL        string  `json:"loginUrl"`
			ExpiresAt       float64 `json:"expiresAt"`
		}
		if status >= 300 || json.Unmarshal(raw, &r) != nil || r.LoginURL == "" {
			return provider.StartResult{}, errors.New("Freebuff login could not start: " + snippet(raw))
		}
		if r.FingerprintID == "" {
			r.FingerprintID = p.Fingerprint
		}
		state, _ := json.Marshal(map[string]any{"fingerprintId": r.FingerprintID, "fingerprintHash": r.FingerprintHash, "expiresAt": int64(r.ExpiresAt)})
		expires := 300 // the sign-in is offered for five minutes at most
		if r.ExpiresAt > 0 {
			if s := int(time.Until(time.UnixMilli(int64(r.ExpiresAt))).Seconds()); s > 60 && s < expires {
				expires = s
			}
		}
		return provider.StartResult{AuthURL: r.LoginURL, State: string(state), Interval: 5, ExpiresIn: expires}, nil
	},
	Poll: func(ctx context.Context, d provider.Doer, state string) (provider.Tokens, bool, error) {
		var s struct {
			FingerprintID   string `json:"fingerprintId"`
			FingerprintHash string `json:"fingerprintHash"`
			ExpiresAt       int64  `json:"expiresAt"`
		}
		if json.Unmarshal([]byte(state), &s) != nil || s.FingerprintID == "" || s.FingerprintHash == "" {
			return provider.Tokens{}, false, errors.New("invalid sign-in state")
		}
		q := url.Values{"fingerprintId": {s.FingerprintID}, "fingerprintHash": {s.FingerprintHash}, "expiresAt": {strconv.FormatInt(s.ExpiresAt, 10)}}
		// the endpoint answers 401 while the browser sign-in is still pending, and 200 {user} once it is done
		raw, _, err := provider.DoJSON(ctx, d, "GET", base()+"/api/auth/cli/status?"+q.Encode(), nil, nil)
		if err != nil {
			return provider.Tokens{}, true, nil
		}
		var r struct {
			User struct {
				ID        string `json:"id"`
				Email     string `json:"email"`
				Name      string `json:"name"`
				AuthToken string `json:"authToken"`
			} `json:"user"`
		}
		if json.Unmarshal(raw, &r) != nil || r.User.AuthToken == "" {
			return provider.Tokens{}, true, nil
		}
		return provider.Tokens{AccessToken: r.User.AuthToken, Email: r.User.Email, Name: r.User.Name,
			Extra: map[string]any{"authMethod": "device_code", "fingerprintId": s.FingerprintID, "userId": r.User.ID}}, false, nil
	},
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
