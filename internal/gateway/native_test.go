package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// nativeDo walks a Native provider's endpoints on 401/403/404 only, converts a 2xx and returns other errors untouched.
func TestNativeDoEndpointFallback(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		code := map[string]int{"/a": 401, "/b": 404, "/c": 200, "/deny1": 403, "/deny2": 403, "/bad": 400, "/never": 200}[r.URL.Path]
		w.WriteHeader(code)
		io.WriteString(w, "upstream "+r.URL.Path)
	}))
	defer srv.Close()

	var urls []string
	provider.Register(provider.Provider{ID: "natfake", Alias: "nf", Name: "NatFake", Category: provider.CategoryFree, Status: provider.StatusExperimental,
		Headers: map[string]string{"X-Static": "1"},
		Native: &provider.Native{
			Endpoints: func(string, map[string]any) []string { return urls },
			Request: func(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
				req, err := http.NewRequestWithContext(ctx, "POST", c.URL, strings.NewReader(`{}`))
				req.Header.Set("X-Token", c.Token+"/"+c.Model+"/"+c.Data["k"].(string))
				return req, err
			},
			Response: func(resp *http.Response, model string, stream bool) (*http.Response, error) {
				b, _ := io.ReadAll(resp.Body)
				resp.Body = io.NopCloser(strings.NewReader("converted " + string(b)))
				return resp, nil
			},
		}})
	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	g := New(store)
	node, err := store.Node("natfake")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := nativeOf(node)
	if !ok {
		t.Fatal("not native")
	}
	do := func(paths ...string) (*http.Response, string) {
		hits, urls = nil, nil
		for _, path := range paths {
			urls = append(urls, srv.URL+path)
		}
		resp, err := g.nativeDo(context.Background(), node, p, map[string]any{"k": "v"}, "tok", "m", []byte(`{"messages":[]}`), false)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	if resp, body := do("/a", "/b", "/c", "/never"); resp.StatusCode != 200 || body != "converted upstream /c" || strings.Join(hits, ",") != "/a,/b,/c" {
		t.Errorf("fallback: %d %q hits=%v", resp.StatusCode, body, hits)
	}
	// every endpoint refuses: the last answer is what the caller classifies, untouched
	if resp, body := do("/deny1", "/deny2"); resp.StatusCode != 403 || body != "upstream /deny2" || len(hits) != 2 {
		t.Errorf("all refused: %d %q hits=%v", resp.StatusCode, body, hits)
	}
	// anything else is not an endpoint problem: no fallback
	if resp, body := do("/bad", "/never"); resp.StatusCode != 400 || body != "upstream /bad" || len(hits) != 1 {
		t.Errorf("400: %d %q hits=%v", resp.StatusCode, body, hits)
	}
}
