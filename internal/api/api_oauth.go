package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"router-go/internal/consolelog"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// Browser sign-in for built-in providers that support it. Each provider's flow differs (see internal/provider/<name>/oauth.go):
//
//	"code": POST …/start returns the provider's login URL (it redirects back to the dashboard with ?code=…),
//	        then POST …/exchange {code, redirectUri} stores the tokens.
//	"poll": POST …/start returns a login URL and an opaque state; the dashboard calls POST …/poll {state} until it
//	        answers {status:"ok"}.

func (a *API) oauthProvider(w http.ResponseWriter, r *http.Request) (provider.Provider, *db.Node, bool) {
	p, ok := provider.ByID(chi.URLParam(r, "provider"))
	if !ok || p.OAuth == nil {
		writeErr(w, 400, "this provider has no browser sign-in")
		return p, nil, false
	}
	if !p.Routable() {
		writeErr(w, 400, p.Name+" cannot be used yet: "+p.Note)
		return p, nil, false
	}
	node, err := a.Store.Node(p.ID)
	if err != nil {
		writeErr(w, 404, "provider not found")
		return p, nil, false
	}
	return p, node, true
}

// fingerprint is a stable random id for this install, for providers that key sign-in on a device.
func (a *API) fingerprint() string {
	var v string
	if a.Store.DB.QueryRow(`SELECT value FROM kv WHERE scope='oauth' AND key='fingerprint'`).Scan(&v) == nil && v != "" {
		return v
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	v = hex.EncodeToString(b)
	_, _ = a.Store.DB.Exec(`INSERT OR REPLACE INTO kv(scope,key,value) VALUES('oauth','fingerprint',?)`, v)
	return v
}

func (a *API) oauthStart(w http.ResponseWriter, r *http.Request) {
	p, node, ok := a.oauthProvider(w, r)
	if !ok {
		return
	}
	var in struct {
		RedirectURI string `json:"redirectUri"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if p.OAuth.Kind == provider.OAuthCode && !strings.HasPrefix(in.RedirectURI, "http") {
		writeErr(w, 400, "redirectUri is required")
		return
	}
	res, err := p.OAuth.Start(r.Context(), a.GW.OAuthClient(node), provider.StartParams{RedirectURI: in.RedirectURI, Fingerprint: a.fingerprint()})
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"kind": p.OAuth.Kind, "authUrl": res.AuthURL, "state": res.State, "interval": max(res.Interval, 2), "expiresIn": res.ExpiresIn})
}

// finishOAuth stores the tokens as a connection (the first connection of a provider switches its catalogue on).
func (a *API) finishOAuth(w http.ResponseWriter, p provider.Provider, node *db.Node, t provider.Tokens) {
	if t.AccessToken == "" {
		writeErr(w, 502, p.Name+" returned no access token")
		return
	}
	a.enableCatalogueIfFirst(node)
	conn, err := a.Store.SaveOAuthConnection(node, t)
	if err != nil {
		dbErr(w, err)
		return
	}
	consolelog.Logf("INFO", "AUTH", "%s: signed in as %s", p.Name, conn.Name)
	writeJSON(w, 200, map[string]any{"status": "ok", "connection": conn.Flat()})
}

func (a *API) oauthExchange(w http.ResponseWriter, r *http.Request) {
	p, node, ok := a.oauthProvider(w, r)
	if !ok {
		return
	}
	if p.OAuth.Kind != provider.OAuthCode {
		writeErr(w, 400, "this provider signs in by polling, not by code")
		return
	}
	var in struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Code) == "" {
		writeErr(w, 400, "code is required")
		return
	}
	t, err := p.OAuth.Exchange(r.Context(), a.GW.OAuthClient(node), in.Code, in.RedirectURI)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	a.finishOAuth(w, p, node, t)
}

func (a *API) oauthPoll(w http.ResponseWriter, r *http.Request) {
	p, node, ok := a.oauthProvider(w, r)
	if !ok {
		return
	}
	if p.OAuth.Kind != provider.OAuthPoll {
		writeErr(w, 400, "this provider signs in with a code, not by polling")
		return
	}
	var in struct {
		State string `json:"state"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	t, pending, err := p.OAuth.Poll(r.Context(), a.GW.OAuthClient(node), in.State)
	switch {
	case err != nil:
		writeErr(w, 502, err.Error())
	case pending:
		writeJSON(w, 200, map[string]any{"status": "pending"})
	default:
		a.finishOAuth(w, p, node, t)
	}
}

// enableCatalogueIfFirst: the first key or sign-in of a built-in provider switches its whole model catalogue on.
func (a *API) enableCatalogueIfFirst(node *db.Node) {
	p, ok := node.Builtin()
	if !ok {
		return
	}
	if existing, _ := a.Store.Connections(node.ID, false); len(existing) > 0 {
		return
	}
	if cms, _ := a.Store.CustomModels(); slices.ContainsFunc(cms, func(m db.CustomModel) bool { return m.ProviderAlias == p.Alias }) {
		return
	}
	for _, m := range p.Models {
		_ = a.Store.AddCustomModel(p.Alias, m.ID, m.Name)
	}
}
