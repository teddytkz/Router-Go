package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"router-go/internal/api"
	"router-go/internal/config"
	"router-go/internal/db"
)

// The update URL is saved in settings, refused when it is not https, and the check endpoint reports through
// Cfg.CheckUpdate (which talks to the update server for real in main).
func TestUpdateURLSettingAndCheck(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.UpdateSettings(map[string]any{"requireLogin": false})
	cfg := &config.Config{DataDir: dir, JWTSecret: []byte("s"), MachineID: "0123456789abcdef", InitialPassword: "123456"}
	h := httptest.NewServer(api.Handler(cfg, store, http.NotFoundHandler()))
	t.Cleanup(h.Close)
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if st, _ := call("PATCH", "/api/settings", `{"updateUrl":"ftp://example.com/router-go"}`); st != 400 {
		t.Errorf("a non-http URL must be refused: %d", st)
	}
	if st, _ := call("PATCH", "/api/settings", `{"updateUrl":"http://example.com/router-go"}`); st != 200 {
		t.Errorf("plain http must be accepted (the UI warns instead): %d", st)
	}
	if st, _ := call("PATCH", "/api/settings", `{"updateUrl":"https://example.com/router-go"}`); st != 200 {
		t.Errorf("https must be accepted: %d", st)
	}
	if st, out := call("GET", "/api/settings", ""); st != 200 || out["updateUrl"] != "https://example.com/router-go" {
		t.Errorf("saved URL not returned: %d %v", st, out["updateUrl"])
	}
	// no CheckUpdate wired (a plain handler, not main): a clear 400 instead of a panic
	if st, _ := call("POST", "/api/update/check", ""); st != 400 {
		t.Errorf("check without a wired update source: %d", st)
	}

	cfg.CheckUpdate = func() (bool, string, error) { return true, "A different binary is published.", nil }
	if st, out := call("POST", "/api/update/check", ""); st != 200 || out["message"] != "A different binary is published." || out["restarting"] != true {
		t.Errorf("check: %d %v", st, out)
	}
}
