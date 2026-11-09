package gateway

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"router-go/internal/consolelog"
	"sync"
	"time"

	"router-go/internal/config"
	"router-go/internal/db"
)

// refreshAhead: tokens expiring within this window are refreshed before they are used.
const refreshAhead = 2 * time.Minute

var refreshLocks sync.Map // connection id → *sync.Mutex, so one request refreshes and the others reuse the result

// headerDoer sends sign-in/refresh calls with the provider's user-defined headers (and a neutral User-Agent).
type headerDoer struct {
	c *http.Client
	h map[string]string
}

func (d headerDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "router-go/"+config.Version)
	}
	for k, v := range d.h {
		req.Header.Set(k, v)
	}
	return d.c.Do(req)
}

// OAuthClient is the HTTP client used for a provider's sign-in and refresh calls.
func (g *Gateway) OAuthClient(node *db.Node) headerDoer {
	h := map[string]string{}
	if p, ok := node.Builtin(); ok {
		for k, v := range p.Headers { // the provider's defaults, then the user's overrides
			h[k] = v
		}
	}
	for k, v := range g.Store.ProviderHeaders(node.ID) {
		h[k] = v
	}
	return headerDoer{c: g.Client, h: h}
}

// Secret returns the key to send upstream for a connection: its API key, or the access token of an OAuth connection
// (refreshed first when it is about to expire, and shaped the way the provider wants it).
func (g *Gateway) Secret(ctx context.Context, node *db.Node, c *db.Connection) (string, error) {
	return g.secret(ctx, node, c, refreshAhead)
}

// secret is Secret with a chosen renewal lead time (the background renewer looks further ahead than a request does).
func (g *Gateway) secret(ctx context.Context, node *db.Node, c *db.Connection, lead time.Duration) (string, error) {
	p, builtin := node.Builtin()
	if !c.IsOAuth() {
		return c.APIKey(), nil
	}
	if !builtin || p.OAuth == nil {
		return c.APIKey(), nil
	}
	token, orig := c.APIKey(), c
	if exp := c.TokenExpiry(); !exp.IsZero() && time.Until(exp) < lead {
		mu, _ := refreshLocks.LoadOrStore(c.ID, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()
		if fresh, err := g.Store.Connection(c.ID); err == nil { // another request may have refreshed it while we waited
			c = fresh
			token = c.APIKey()
			syncData(orig, c.Data)
		}
		if exp := c.TokenExpiry(); exp.IsZero() || time.Until(exp) < lead {
			if c.RefreshToken() == "" || !p.OAuth.CanRefresh() {
				return "", errors.New("the sign-in expired and cannot be refreshed: sign in again")
			}
			psd, _ := c.Data["providerSpecificData"].(map[string]any)
			t, err := p.OAuth.DoRefresh(ctx, g.OAuthClient(node), c.RefreshToken(), psd)
			if err != nil {
				return "", errors.New("token refresh failed: " + err.Error() + " — sign in again")
			}
			if err := g.Store.UpdateTokens(c.ID, t); err != nil {
				return "", err
			}
			token = t.AccessToken
			syncData(orig, map[string]any{"providerSpecificData": t.Extra})
			consolelog.Logf("INFO", "AUTH", "%s/%s: token refreshed, valid until %s", node.Name, c.Name, t.ExpiresAt.Local().Format("15:04:05"))
		}
	}
	if p.OAuthSecret != nil {
		token = p.OAuthSecret(token)
	}
	return token, nil
}

// syncData copies fresh providerSpecificData values into c in place: the request (or its context) already holds c's
// map, and a refresh may have just learned something the upstream call needs (Kiro's profile ARN).
func syncData(c *db.Connection, fresh map[string]any) {
	from, _ := fresh["providerSpecificData"].(map[string]any)
	if to, ok := c.Data["providerSpecificData"].(map[string]any); ok {
		maps.Copy(to, from)
	}
}
