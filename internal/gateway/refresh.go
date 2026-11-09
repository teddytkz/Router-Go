package gateway

import (
	"context"
	"time"

	"router-go/internal/consolelog"
)

const (
	bgRefreshEvery = 5 * time.Minute
	bgRefreshLead  = 30 * time.Minute // renew sign-ins that expire within this window, before a request has to wait for it
)

// RunBackgroundRefresh renews OAuth tokens ahead of time until ctx ends, so the first request after a quiet period
// does not pay for (or fail on) a refresh. A failure is only logged: the request path retries and reports it.
func (g *Gateway) RunBackgroundRefresh(ctx context.Context) {
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		g.refreshExpiring(ctx)
		t.Reset(bgRefreshEvery)
	}
}

func (g *Gateway) refreshExpiring(ctx context.Context) {
	conns, err := g.Store.Connections("", true)
	if err != nil {
		return
	}
	for _, c := range conns {
		exp := c.TokenExpiry()
		if !c.IsOAuth() || c.RefreshToken() == "" || exp.IsZero() || time.Until(exp) > bgRefreshLead {
			continue
		}
		node, err := g.Store.Node(c.Provider)
		if err != nil {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := g.secret(rctx, node, c, bgRefreshLead); err != nil {
			consolelog.Logf("WARN", "AUTH", "%s/%s: background token refresh failed: %s", node.Name, c.Name, err)
		}
		cancel()
	}
}
