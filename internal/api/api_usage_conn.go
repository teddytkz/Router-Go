package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"router-go/internal/gateway"
	"router-go/internal/provider"
)

// Live quotas as the provider itself reports them (XRouter-Next's Quota Tracker); the user-defined limits are /api/quota.

const (
	usageTTL     = 30 * time.Second // a short cache: the dashboard polls, and providers dislike being hammered
	usageTimeout = 15 * time.Second
)

type usageEntry struct {
	at    time.Time
	token string // the key the answer was fetched with: a changed key never reuses it
	u     provider.Usage
}

func usageMsg(msg string) provider.Usage {
	return provider.Usage{Message: msg, Quotas: map[string]provider.Quota{}}
}

// connectionUsage: GET /api/usage/{connectionId}[?force=1]. Problems with the upstream or the sign-in come back as a
// message (HTTP 200), never as a 5xx: the dashboard shows it on the connection's row.
func (a *API) connectionUsage(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.Connection(chi.URLParam(r, "connectionId"))
	if err != nil {
		dbErr(w, err)
		return
	}
	node, err := a.Store.Node(c.Provider)
	if err != nil {
		dbErr(w, err)
		return
	}
	p, builtin := node.Builtin()
	if !builtin || p.Usage == nil {
		writeJSON(w, 200, usageMsg("Usage API not implemented for "+c.Provider))
		return
	}
	ctx, cancel := context.WithTimeout(gateway.ConnCtx(r.Context(), c), usageTimeout)
	defer cancel()
	token, err := a.GW.Secret(ctx, node, c) // refreshes an OAuth token that is about to expire
	if err != nil {
		writeJSON(w, 200, usageMsg("Credential refresh failed: "+err.Error()))
		return
	}
	if e, ok := a.usages.Load(c.ID); ok && r.URL.Query().Get("force") != "1" {
		if e := e.(usageEntry); e.token == token && time.Since(e.at) < usageTTL {
			writeJSON(w, 200, e.u)
			return
		}
	}
	psd, _ := c.Data["providerSpecificData"].(map[string]any)
	u, err := p.Usage(ctx, a.GW.OAuthClient(node), provider.UsageCall{Token: token, Data: psd, BaseURL: p.BaseURL})
	if err != nil {
		u = usageMsg("Unable to fetch " + p.Name + " usage right now: " + err.Error())
	}
	if u.Quotas == nil {
		u.Quotas = map[string]provider.Quota{}
	}
	if len(u.Quotas) > 0 { // only real quota numbers are worth keeping; a message may be gone on the next try
		a.usages.Store(c.ID, usageEntry{time.Now(), token, u})
	}
	writeJSON(w, 200, u)
}
