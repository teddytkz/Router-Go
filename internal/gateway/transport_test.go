package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// A provider that brings its own transport must be reached through it, side requests included.
func TestClientForUsesProviderTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "via-provider-transport")
	}))
	defer srv.Close()

	var dialed, done bool
	provider.Register(provider.Provider{ID: "transportfake", Alias: "tf", Name: "TransportFake",
		Category: provider.CategoryFree, Status: provider.StatusExperimental,
		Native: &provider.Native{
			Endpoints: func(string, map[string]any) []string { return []string{"http://upstream.invalid/x"} },
			Request: func(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
				req, _ := http.NewRequestWithContext(ctx, "POST", c.URL, strings.NewReader(`{}`))
				resp, err := c.Do(req) // a side request goes through the same client
				if err != nil {
					return nil, err
				}
				defer resp.Body.Close()
				if b, _ := io.ReadAll(resp.Body); string(b) == "via-provider-transport" {
					done = true
				}
				return req, nil
			},
			Response: func(resp *http.Response, _ string, _ bool) (*http.Response, error) { return resp, nil },
		},
		Transport: func() *http.Transport {
			return &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				dialed = true
				return net.Dial("tcp", srv.Listener.Addr().String())
			}}
		},
	})

	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	g := New(store)
	node, err := store.Node("transportfake")
	if err != nil {
		t.Fatal(err)
	}

	resp, err := g.clientFor(node).Get("http://upstream.invalid/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "via-provider-transport" || !dialed {
		t.Errorf("transport not used: %q dialed=%v", b, dialed)
	}

	// the side request is wrapped in the pool transport too, so a proxy pool still applies
	if _, ok := g.clientFor(node).Transport.(*poolTransport); !ok {
		t.Errorf("transport = %T, want *poolTransport", g.clientFor(node).Transport)
	}

	if _, err := g.nativeDo(context.Background(), node, mustProvider(t, node), nil, "", "m", []byte(`{}`), false); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("NativeCall.Do did not reach the upstream")
	}

	// providers without a transport keep the shared client
	plain := &db.Node{ID: "custom", Type: db.NodeTypeOpenAI}
	if g.clientFor(plain) != g.Client {
		t.Error("custom node did not get the shared client")
	}
}

// A proxied request must not use the provider's custom TLS dialer: net/http hands it the proxy address, so a
// fingerprinting provider would handshake with the proxy instead of tunnelling through it.
func TestProxyPoolDropsTheCustomTLSDialer(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateProxyPool(&db.ProxyPool{Name: "p", Type: "http", ProxyURL: "http://127.0.0.1:9999"}); err != nil {
		t.Fatal(err)
	}
	pools, _ := store.ProxyPools()

	tr := newPoolTransport(store, &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("custom dialer reached")
	}})
	req, _ := http.NewRequest("GET", "https://example.com/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), poolKey{}, pools[0].ID))
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "proxyconnect") && !strings.Contains(err.Error(), "refused") {
		t.Fatalf("proxied request = %v (the custom dialer must not be used)", err)
	}
	if got := tr.base.DialTLSContext; got == nil {
		t.Error("the direct transport lost its dialer")
	}
	for _, via := range tr.via {
		if via.DialTLSContext != nil {
			t.Error("a proxied transport kept the custom dialer")
		}
	}
}

// A provider that refuses relays must fail the request instead of letting the pool forward it: opencode.ai answers a
// relayed call with a 429 that reads like a spent quota and benches a key that is fine.
func TestDirectOnlyProviderRefusesARelay(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateProxyPool(&db.ProxyPool{Name: "r", Type: db.PoolCloudflare, ProxyURL: "https://relay.example.com", RelayKey: "k"}); err != nil {
		t.Fatal(err)
	}
	pools, _ := store.ProxyPools()

	tr := newPoolTransport(store, &http.Transport{})
	tr.directOnly = true
	req, _ := http.NewRequest("GET", "https://opencode.ai/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), poolKey{}, pools[0].ID))
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "refuses relays") {
		t.Fatalf("relayed direct-only request = %v, want a refusal", err)
	}

	// without a pool the same request goes to the direct transport instead of being refused
	direct := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct dial reached")
	}}
	tr.base = direct
	if _, err := tr.RoundTrip(req.WithContext(context.Background())); err == nil || !strings.Contains(err.Error(), "direct dial reached") {
		t.Fatalf("unpooled request = %v, want the direct transport", err)
	}
}

// A provider that signs its own TLS cannot be wrapped in url.Proxy (that would hand its dialer the proxy's address), so
// the pool passes the proxy URL on the context and the provider's dialer opens the tunnel itself.
func TestProxyDialerGetsThePoolURL(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const proxy = "http://127.0.0.1:3128"
	if err := store.CreateProxyPool(&db.ProxyPool{Name: "p", Type: db.PoolHTTP, ProxyURL: proxy}); err != nil {
		t.Fatal(err)
	}
	pools, _ := store.ProxyPools()

	var got string
	tr := newPoolTransport(store, &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		got, _ = ctx.Value(proxySeenKey{}).(string)
		return nil, errors.New("provider dialer reached")
	}})
	tr.directOnly, tr.proxyDial = true, true
	tr.run = func(ctx context.Context, raw string) context.Context {
		return context.WithValue(ctx, proxySeenKey{}, raw)
	}
	req, _ := http.NewRequest("GET", "https://opencode.ai/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), poolKey{}, pools[0].ID))
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "provider dialer reached") {
		t.Fatalf("proxied request = %v, want the base transport's dialer", err)
	}
	if got != proxy {
		t.Errorf("dialer saw proxy %q, want %q", got, proxy)
	}
	if tr.via[proxy] != nil {
		t.Error("the pool wrapped the transport in url.Proxy as well")
	}
}

type proxySeenKey struct{}

func mustProvider(t *testing.T, node *db.Node) provider.Provider {
	t.Helper()
	p, ok := nativeOf(node)
	if !ok {
		t.Fatal("not native")
	}
	return p
}

// A fingerprint relay is the one relay a direct-only provider accepts: it replays the client's own ClientHello, so the
// request's headers must reach it untouched and its head line becomes the response.
func TestFingerprintRelayCarriesHeadersAndAnswers(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var got struct {
		key, target, path, ua, auth, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Target  string            `json:"target"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		got.key, got.target, got.path = r.Header.Get("x-relay-key"), in.Target, in.Path
		got.ua, got.auth, got.body = in.Headers["User-Agent"], in.Headers["Authorization"], in.Body
		w.WriteHeader(200)
		io.WriteString(w, `{"status":200,"headers":{"content-type":"text/event-stream"}}`+"\n")
		io.WriteString(w, "data: ok\n\n")
	}))
	defer srv.Close()

	if err := store.CreateProxyPool(&db.ProxyPool{Name: "fp", Type: db.PoolPostcard, ProxyURL: srv.URL, RelayKey: "sekret"}); err != nil {
		t.Fatal(err)
	}
	pools, _ := store.ProxyPools()

	tr := newPoolTransport(store, &http.Transport{})
	tr.directOnly = true // opencode refuses plain relays; a fingerprint relay must still go through
	req, _ := http.NewRequest("POST", "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(`{"model":"big-pickle"}`))
	req.Header.Set("User-Agent", "opencode/latest/2.1.4/cli")
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Accept", "text/event-stream")
	req = req.WithContext(context.WithValue(req.Context(), poolKey{}, pools[0].ID))

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("relayed request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "data: ok\n\n" {
		t.Fatalf("answer = %d %q, want the relay's body", resp.StatusCode, body)
	}
	if got.key != "sekret" {
		t.Errorf("relay key = %q", got.key)
	}
	if got.target != "https://opencode.ai" || got.path != "/inference/openai/v1/chat/completions" {
		t.Errorf("relay target = %q%s", got.target, got.path)
	}
	if got.ua != "opencode/latest/2.1.4/cli" || got.auth != "Bearer public" {
		t.Errorf("provider headers lost: ua=%q auth=%q", got.ua, got.auth)
	}
	if got.body != `{"model":"big-pickle"}` {
		t.Errorf("body = %q", got.body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("upstream content-type lost: %q", ct)
	}
}
