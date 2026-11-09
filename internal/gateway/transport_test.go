package gateway

import (
	"context"
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

func mustProvider(t *testing.T, node *db.Node) provider.Provider {
	t.Helper()
	p, ok := nativeOf(node)
	if !ok {
		t.Fatal("not native")
	}
	return p
}
