package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// A plain API-key built-in is checked with GET /models (no credit spent): 200 = valid, 401/403 = the key is rejected,
// anything else is an upstream problem and must not count as a bad key.
func TestValidateBuiltinAPIKeyUsesModels(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("unexpected %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	provider.Register(provider.Provider{ID: "keyfake", Alias: "kf", Name: "KeyFake", Category: provider.CategoryAPIKey,
		Status: provider.StatusReady, BaseURL: srv.URL, Models: []provider.Model{{ID: "m1"}}})

	store, err := db.Open(filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.Node("keyfake")
	if err != nil {
		t.Fatal(err)
	}
	g := New(store)
	for _, c := range []struct {
		status          int
		valid, rejected bool
	}{{200, true, false}, {401, false, true}, {403, false, true}} {
		status = c.status
		res := g.ValidateBuiltin(context.Background(), node, "k")
		if res.Valid != c.valid || res.Rejected != c.rejected || res.Method != "models" {
			t.Errorf("status %d: %+v", c.status, res)
		}
	}
}
