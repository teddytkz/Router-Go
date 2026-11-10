package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	store      *db.Store
	base       *http.Transport
	directOnly bool // the provider refuses plain relays: one must fail loudly rather than be silently ignored
	proxyDial  bool // the provider's run dials through the proxy itself (see provider.Provider.ProxyDialer)
	run        func(context.Context, string) context.Context
	mu         sync.Mutex
	via        map[string]*http.Transport // proxy URL -> transport
	plain      *http.Transport            // the leg to a fingerprint relay; created on first use
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
	if err != nil || !p.IsActive || p.ProxyURL == "" || bypass(p.NoProxy, req.URL.Hostname()) {
		if err == nil && (!p.IsActive || p.ProxyURL == "") && p.StrictProxy { // never leak the direct IP when asked for a proxy
			return nil, errors.New("proxy pool '" + p.Name + "' is disabled and strict mode is on")
		}
		if err == nil && p.IsRelay() && t.directOnly {
			return nil, errors.New("this provider refuses relays — set its proxy to none (a plain http proxy works)")
		}
		return t.base.RoundTrip(req) // a deleted, disabled or bypassed pool leaves the connection direct
	}
	if p.IsRelay() {
		if t.directOnly && !p.KeepsFingerprint() {
			// The upstream refuses a relayed request for this provider whatever the relay adds (opencode.ai answers
			// "Client IP address is required for free usage"). Failing here names the cause; forwarding would return a
			// 429 that reads like a spent quota and bench a connection whose credentials are fine.
			return nil, errors.New("this provider refuses relays — set its proxy to none (a plain http proxy works)")
		}
		if p.KeepsFingerprint() {
			return t.relayFingerprint(req, p)
		}
		return t.base.RoundTrip(relayRequest(req, p))
	}
	if t.proxyDial && t.run != nil {
		// The provider's dialer opens the CONNECT tunnel and fingerprints the TLS inside it; wrapping the transport in
		// url.Proxy would hand its DialTLS the proxy's address instead of the target's.
		return t.base.RoundTrip(req.WithContext(t.run(req.Context(), p.ProxyURL)))
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

// relayFingerprint sends the call through a postcard relay: the function replays the client's ClientHello, so it needs
// the request's own headers (they are the provider's fingerprint) and answers with a JSON head line followed by the
// body. The relay is reached with a normal TLS client — the provider's fingerprint dialer belongs to the upstream.
func (t *poolTransport) relayFingerprint(req *http.Request, p *db.ProxyPool) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	hdrs := map[string]string{}
	for k, v := range req.Header {
		if len(v) > 0 && !strings.EqualFold(k, "Content-Length") {
			hdrs[k] = v[0]
		}
	}
	payload, err := json.Marshal(map[string]any{"target": "https://" + req.URL.Host, "path": req.URL.RequestURI(), "headers": hdrs, "body": string(body)})
	if err != nil {
		return nil, err
	}
	out, err := http.NewRequestWithContext(req.Context(), "POST", p.ProxyURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("x-relay-key", p.RelayKey)
	resp, err := t.plainTransport().RoundTrip(out)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("fingerprint relay answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadBytes('\n')
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("fingerprint relay sent no head: %w", err)
	}
	var head struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Error   string            `json:"error"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("fingerprint relay sent a bad head: %w", err)
	}
	if head.Error != "" {
		resp.Body.Close()
		return nil, errors.New("fingerprint relay: " + head.Error)
	}
	res := &http.Response{
		Status: strconv.Itoa(head.Status) + " " + http.StatusText(head.Status), StatusCode: head.Status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Request: req,
		Body: readCloser{br, resp.Body},
	}
	for k, v := range head.Headers {
		res.Header.Set(k, v)
	}
	return res, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// plainTransport is the leg to a fingerprint relay: a function at an https:// address answers a normal TLS client, so
// the provider's fingerprint dialer must not be used for it.
func (t *poolTransport) plainTransport() *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plain == nil {
		t.plain = baseTransport()
	}
	return t.plain
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

// testFingerprintRelay asks a postcard relay to fetch a page it can reach and checks it answered for us. The relay has
// no /health of its own (it is a function replaying a ClientHello), so a plain GET through it is the smallest proof
// that the deployment is up and the key matches. The captured hello is the CLI's, so it only verifies against an
// endpoint that serves the CLI's certificate chain (example.com fails with "invalid signature").
func (g *Gateway) testFingerprintRelay(ctx context.Context, p *db.ProxyPool) (int, error) {
	body, _ := json.Marshal(map[string]any{"target": "https://opencode.ai", "path": "/inference/v1/models", "method": "GET",
		"headers": map[string]string{"Accept": "application/json", "Authorization": "Bearer public", "User-Agent": "opencode/latest/2.1.4/cli", "x-opencode-client": "cli"}, "body": ""})
	req, _ := http.NewRequestWithContext(ctx, "POST", p.ProxyURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-relay-key", p.RelayKey)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, errors.New(networkMessage(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return resp.StatusCode, errors.New("relay refused the key (403) — redeploy the relay")
	}
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return resp.StatusCode, errors.New("relay answered HTTP " + strconv.Itoa(resp.StatusCode) + ": " + strings.TrimSpace(string(msg)))
	}
	line, err := bufio.NewReader(resp.Body).ReadBytes('\n')
	if err != nil {
		return resp.StatusCode, errors.New("relay sent no head line")
	}
	var head struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return resp.StatusCode, errors.New("relay sent an unreadable head: " + err.Error())
	}
	if head.Error != "" {
		return head.Status, errors.New(head.Error)
	}
	return head.Status, nil
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
		if p.KeepsFingerprint() {
			return g.testFingerprintRelay(ctx, p)
		}
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
