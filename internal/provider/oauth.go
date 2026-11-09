package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Browser sign-in for providers that offer it. Two shapes exist among the built-ins:
//
//	"code": the browser is sent to the provider, which redirects back to the dashboard with a code (Cline).
//	"poll": the dashboard shows a login link and polls until the user has signed in (CodeBuddy, Freebuff).
//
// The functions live in each provider's package (oauth.go) and talk to that provider only; the router stores the
// resulting tokens on a connection, refreshes them before they expire and sends the access token as the key.

const (
	OAuthCode = "code"
	OAuthPoll = "poll"
)

// Tokens is what a sign-in or a refresh yields.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time // zero = unknown
	Email        string
	Name         string
	Extra        map[string]any // provider-specific data kept on the connection
}

// Doer sends HTTP requests; the router wraps its client so user-defined headers are applied to sign-in calls too.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type StartParams struct {
	RedirectURI string // where a "code" flow sends the browser back to
	Fingerprint string // a stable per-install id, for providers that key sign-in on it
}

type StartResult struct {
	AuthURL   string // open this in a browser
	State     string // opaque; handed back to Poll
	Interval  int    // seconds between polls
	ExpiresIn int    // seconds the sign-in stays valid
}

type OAuth struct {
	Kind     string
	Start    func(ctx context.Context, d Doer, p StartParams) (StartResult, error)
	Exchange func(ctx context.Context, d Doer, code, redirectURI string) (Tokens, error)         // "code" flows
	Poll     func(ctx context.Context, d Doer, state string) (t Tokens, pending bool, err error) // "poll" flows
	Refresh  func(ctx context.Context, d Doer, refreshToken string) (Tokens, error)
	// RefreshConn is Refresh for providers whose refresh depends on how the account signed in: it also receives the
	// connection's providerSpecificData and may return Extra data to merge back into it.
	RefreshConn func(ctx context.Context, d Doer, refreshToken string, data map[string]any) (Tokens, error)
	// Import turns what the user pasted in the add-key dialog (a refresh token, or a JSON blob) into signed-in tokens.
	Import func(ctx context.Context, d Doer, raw string) (Tokens, error)
}

// CanRefresh: the provider can renew an expired sign-in.
func (o *OAuth) CanRefresh() bool { return o.Refresh != nil || o.RefreshConn != nil }

// DoRefresh renews a sign-in with whichever refresh variant the provider has.
func (o *OAuth) DoRefresh(ctx context.Context, d Doer, refreshToken string, data map[string]any) (Tokens, error) {
	if o.RefreshConn != nil {
		return o.RefreshConn(ctx, d, refreshToken, data)
	}
	return o.Refresh(ctx, d, refreshToken)
}

// DoJSON sends a request and returns the body (capped) and status.
func DoJSON(ctx context.Context, d Doer, method, url string, headers map[string]string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, err
}
