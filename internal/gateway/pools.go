package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"router-go/internal/consolelog"
	"router-go/internal/db"
)

// Proxy pools: a connection with providerSpecificData.proxyPoolId sends its upstream traffic through that pool.
// The pool id rides on the request context, so every upstream call (chat, test, validate) honours it in one place.

type poolKey struct{}

// psdKey carries the connection's providerSpecificData for providers whose requests depend on it (Native ones).
type psdKey struct{}

// ConnCtx tags ctx with the connection's proxy pool (when it has one) and its providerSpecificData.
func ConnCtx(ctx context.Context, c *db.Connection) context.Context {
	psd, _ := c.Data["providerSpecificData"].(map[string]any)
	ctx = context.WithValue(ctx, psdKey{}, psd)
	if id, _ := psd["proxyPoolId"].(string); id != "" && id != db.PoolNone {
		return context.WithValue(ctx, poolKey{}, id)
	}
	return ctx
}

// poolTransport routes requests per proxy pool and falls back to base (direct) when none applies.
type poolTransport struct {
	store *db.Store
	base  *http.Transport
	mu    sync.Mutex
	via   map[string]*http.Transport // proxy URL -> transport
}

func newPoolTransport(store *db.Store, base *http.Transport) *poolTransport {
	return &poolTransport{store: store, base: base, via: map[string]*http.Transport{}}
}

func (t *poolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	id, _ := req.Context().Value(poolKey{}).(string)
	if id == "" {
		return t.base.RoundTrip(req)
	}
	p, err := t.store.ProxyPool(id)
	if err != nil { // a deleted pool leaves the connection direct
		return t.base.RoundTrip(req)
	}
	if !p.IsActive || p.ProxyURL == "" {
		if p.StrictProxy { // never leak the direct IP when the user asked for a proxy
			return nil, errors.New("proxy pool '" + p.Name + "' is disabled and strict mode is on")
		}
		return t.base.RoundTrip(req)
	}
	if bypass(p.NoProxy, req.URL.Hostname()) {
		return t.base.RoundTrip(req)
	}
	if p.IsRelay() {
		return t.base.RoundTrip(relayRequest(req, p))
	}
	tr, err := t.proxyTransport(p.ProxyURL)
	if err != nil {
		return nil, err
	}
	return tr.RoundTrip(req)
}

func (t *poolTransport) proxyTransport(raw string) (*http.Transport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.via[raw]; tr != nil {
		return tr, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	tr := t.base.Clone()
	tr.Proxy = http.ProxyURL(u)
	// Through a proxy the custom dialer is handed the proxy address, not the target, so a provider that fingerprints
	// its TLS (opencode) would handshake to the proxy instead of tunnelling; the pool keeps the plain TLS path.
	if tr.DialTLSContext != nil {
		tr.DialTLSContext = nil
	}
	t.via[raw] = tr
	return tr, nil
}

// relayRequest points req at the relay worker and tells it where to forward to.
func relayRequest(req *http.Request, p *db.ProxyPool) *http.Request {
	out := req.Clone(req.Context())
	target, path := req.URL.Scheme+"://"+req.URL.Host, req.URL.RequestURI()
	u, _ := url.Parse(p.ProxyURL)
	out.URL, out.Host = u, u.Host
	out.Header.Set("x-relay-target", target)
	out.Header.Set("x-relay-path", path)
	out.Header.Set("x-relay-key", p.RelayKey)
	return out
}

// bypass reports whether host matches the pool's no-proxy list (comma separated hosts or .suffixes).
func bypass(list, host string) bool {
	for _, e := range strings.Split(list, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && (host == e || strings.HasSuffix(host, "."+strings.TrimPrefix(e, "."))) {
			return true
		}
	}
	return false
}

// dialer is the transport every pool shares with the direct client.
func baseTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, // reasoning models can be slow to first byte
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
	}
}

// TestPool checks that a pool works: a relay must answer /health with its key, a plain proxy must reach the internet
// through itself. It returns the HTTP status that came back.
func (g *Gateway) TestPool(ctx context.Context, p *db.ProxyPool) (int, error) {
	var req *http.Request
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if p.IsRelay() {
		req, _ = http.NewRequestWithContext(ctx, "GET", p.ProxyURL+"/health", nil)
		req.Header.Set("x-relay-key", p.RelayKey)
		client.Transport = g.directTransport()
	} else {
		u, err := url.Parse(p.ProxyURL)
		if err != nil {
			return 0, err
		}
		tr := baseTransport()
		tr.Proxy = http.ProxyURL(u)
		defer tr.CloseIdleConnections()
		client.Transport = tr
		req, _ = http.NewRequestWithContext(ctx, "GET", "https://example.com/", nil)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New(networkMessage(err))
	}
	resp.Body.Close()
	if p.IsRelay() && resp.StatusCode != 200 {
		if resp.StatusCode == 403 {
			return resp.StatusCode, errors.New("relay refused the key (403) — redeploy the relay")
		}
		return resp.StatusCode, errors.New("relay answered HTTP " + strconv.Itoa(resp.StatusCode))
	}
	return resp.StatusCode, nil
}

func (g *Gateway) directTransport() http.RoundTripper {
	return g.Client.Transport.(*poolTransport).base
}

// logPool notes that a request leaves through a proxy pool (the pool is invisible otherwise).
func (g *Gateway) logPool(ctx context.Context, node *db.Node, c *db.Connection) {
	id, _ := ctx.Value(poolKey{}).(string)
	if id == "" {
		return
	}
	name := id
	if p, err := g.Store.ProxyPool(id); err == nil {
		name = p.Name
	}
	consolelog.Logf("INFO", "PROXY", "%s/%s goes through proxy pool %s", node.Name, c.Name, name)
}
