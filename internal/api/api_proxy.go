package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"router-go/internal/consolelog"
	"router-go/internal/db"
	"router-go/internal/relay"
)

// newDeployer builds the Vercel/Cloudflare deployer; tests swap it to point at fakes.
var newDeployer = relay.New

func (a *API) listProxyPools(w http.ResponseWriter, r *http.Request) {
	pools, err := a.Store.ProxyPools()
	if err != nil {
		dbErr(w, err)
		return
	}
	use := a.Store.PoolUsage()
	out := make([]map[string]any, len(pools))
	for i, p := range pools {
		out[i] = poolView(p, use[p.ID])
	}
	writeJSON(w, 200, map[string]any{"proxyPools": out})
}

// poolView is the API shape: the relay key stays on the server, only its presence is shown.
func poolView(p *db.ProxyPool, bound int) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "type": p.Type, "proxyUrl": maskURL(p.ProxyURL), "noProxy": p.NoProxy, "isActive": p.IsActive,
		"strictProxy": p.StrictProxy, "keyless": p.Keyless, "testStatus": p.TestStatus, "lastTestedAt": p.LastTestedAt, "lastError": p.LastError,
		"createdAt": p.CreatedAt, "updatedAt": p.UpdatedAt, "boundConnectionCount": bound}
}

// maskURL hides a proxy password (http://user:***@host:port).
func maskURL(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return raw
	}
	user := rest[:at]
	if c := strings.Index(user, ":"); c >= 0 {
		user = user[:c+1] + "***"
	}
	return raw[:i+3] + user + rest[at:]
}

type poolInput struct {
	Name        *string `json:"name"`
	Type        *string `json:"type"`
	ProxyURL    *string `json:"proxyUrl"`
	NoProxy     *string `json:"noProxy"`
	IsActive    *bool   `json:"isActive"`
	StrictProxy *bool   `json:"strictProxy"`
	// RelayKey registers a relay the user deployed themselves; it is write-only and never returned.
	RelayKey *string `json:"relayKey"`
	Keyless  *bool   `json:"keyless"`
}

func (in poolInput) apply(p *db.ProxyPool) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Type != nil && *in.Type != "" {
		p.Type = *in.Type
	}
	if in.Keyless != nil {
		p.Keyless = *in.Keyless
	}
	if in.RelayKey != nil && *in.RelayKey != "" {
		p.RelayKey = strings.TrimSpace(*in.RelayKey)
	}
	// an unchanged masked URL means "keep the stored one" (the edit form only ever sees the masked value)
	if in.ProxyURL != nil && *in.ProxyURL != maskURL(p.ProxyURL) {
		p.ProxyURL = *in.ProxyURL
	}
	if in.NoProxy != nil {
		p.NoProxy = *in.NoProxy
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if in.StrictProxy != nil {
		p.StrictProxy = *in.StrictProxy
	}
}

func (a *API) createProxyPool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !readJSON(w, r, &in) {
		return
	}
	p := &db.ProxyPool{Type: db.PoolHTTP} // relays made by the deploy endpoints carry their own type
	in.apply(p)
	if err := a.Store.CreateProxyPool(p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"proxyPool": poolView(p, 0)})
}

func (a *API) updateProxyPool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !readJSON(w, r, &in) {
		return
	}
	p, err := a.Store.UpdateProxyPool(chi.URLParam(r, "id"), func(p *db.ProxyPool) {
		old := p.ProxyURL
		in.apply(p)
		if p.ProxyURL != old {
			p.TestStatus, p.LastError = "unknown", ""
		}
	})
	if err != nil {
		var bad db.InvalidError
		if errors.As(err, &bad) {
			writeErr(w, 400, bad.Error())
			return
		}
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"proxyPool": poolView(p, a.Store.PoolUsage()[p.ID])})
}

func (a *API) deleteProxyPool(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteProxyPool(chi.URLParam(r, "id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) testProxyPool(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.ProxyPool(chi.URLParam(r, "id"))
	if err != nil {
		dbErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	start := time.Now()
	status, terr := a.GW.TestPool(ctx, p)
	tookMs := time.Since(start).Milliseconds()
	ok := terr == nil
	_, _ = a.Store.UpdateProxyPool(p.ID, func(q *db.ProxyPool) {
		q.TestStatus, q.LastTestedAt, q.LastError = "active", time.Now().UTC().Format(time.RFC3339), ""
		if !ok {
			q.TestStatus, q.LastError = "error", terr.Error()
		}
	})
	res := map[string]any{"ok": ok, "status": status, "elapsedMs": tookMs}
	if !ok {
		res["error"] = terr.Error()
		consolelog.Logf("WARN", "PROXY", "%s test failed: %s", p.Name, terr)
	}
	writeJSON(w, 200, res)
}

// deploy runs one hosting deploy and stores the resulting relay as a proxy pool.
func (a *API) deployRelay(w http.ResponseWriter, r *http.Request, kind, name string, run func(context.Context, *relay.Deployer) (relay.Result, error)) {
	if name == "" {
		name = "relay-" + strings.ToLower(relay.NewKey()[:8])
	}
	if err := relay.ValidName(name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := run(ctx, newDeployer())
	if err != nil {
		consolelog.Logf("WARN", "PROXY", "%s deploy failed: %s", kind, err)
		writeErr(w, 502, err.Error())
		return
	}
	p := &db.ProxyPool{Name: name, Type: kind, ProxyURL: res.URL, RelayKey: res.Key}
	if err := a.Store.CreateProxyPool(p); err != nil {
		writeErr(w, 500, "deployed, but saving the pool failed: "+err.Error())
		return
	}
	consolelog.Logf("INFO", "PROXY", "deployed %s relay %s at %s", kind, name, res.URL)
	writeJSON(w, 201, map[string]any{"proxyPool": poolView(p, 0), "deployUrl": res.URL})
}

func (a *API) vercelDeploy(w http.ResponseWriter, r *http.Request) {
	var in struct{ VercelToken, ProjectName string }
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.VercelToken) == "" {
		writeErr(w, 400, "Vercel API token is required")
		return
	}
	a.deployRelay(w, r, db.PoolVercel, strings.TrimSpace(in.ProjectName), func(ctx context.Context, d *relay.Deployer) (relay.Result, error) {
		return d.Vercel(ctx, strings.TrimSpace(in.VercelToken), strings.TrimSpace(in.ProjectName))
	})
}

func (a *API) cloudflareDeploy(w http.ResponseWriter, r *http.Request) {
	var in struct{ AccountID, APIToken, ProjectName string }
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.APIToken) == "" {
		writeErr(w, 400, "Cloudflare Account ID and API token are required")
		return
	}
	a.deployRelay(w, r, db.PoolCloudflare, strings.TrimSpace(in.ProjectName), func(ctx context.Context, d *relay.Deployer) (relay.Result, error) {
		return d.Cloudflare(ctx, strings.TrimSpace(in.AccountID), strings.TrimSpace(in.APIToken), strings.TrimSpace(in.ProjectName))
	})
}

// bulkProxy binds many connections to pools in one call (the "Apply Proxy" dialog of a provider).
func (a *API) bulkProxy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Assignments []db.ProxyAssignment `json:"assignments"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Assignments) == 0 || len(in.Assignments) > 1000 {
		writeErr(w, 400, "send 1 to 1000 assignments")
		return
	}
	pools := map[string]bool{}
	for _, p := range func() []*db.ProxyPool { l, _ := a.Store.ProxyPools(); return l }() {
		pools[p.ID] = true
	}
	for _, as := range in.Assignments {
		if as.PoolID != "" && !pools[as.PoolID] {
			writeErr(w, 400, "proxy pool not found: "+as.PoolID)
			return
		}
	}
	if err := a.Store.SetConnectionProxies(in.Assignments); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "updated": len(in.Assignments)})
}
