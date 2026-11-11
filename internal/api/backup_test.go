package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBackupRoundTrip(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false, "port": 9999, "customPrompt": "keep me", "password": "hash-1"})
	e.addKey("one", "upstream-secret-1")
	e.store.CreateCombo("smart", []string{"mock/m"}, "fallback", nil)

	do := func(method, path, ctype, body string) (int, string) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	st, dump := do("GET", "/api/backup", "", "")
	if st != 200 || !strings.Contains(dump, "upstream-secret-1") || !strings.Contains(dump, "smart") || !strings.Contains(dump, "sk-test-key") {
		t.Fatalf("export: %d %.200s", st, dump)
	}
	if strings.Contains(dump, `9999`) {
		t.Errorf("port is machine-specific and must not be exported")
	}

	// change everything, then restore
	conns, _ := e.store.Connections(e.node.ID, false)
	for _, c := range conns {
		e.store.DeleteConnection(c.ID)
	}
	e.store.UpdateSettings(map[string]any{"customPrompt": "changed", "port": 7000})
	cs, _ := e.store.Combos()
	for _, c := range cs {
		e.store.DeleteCombo(c.ID)
	}

	if st, _ := do("POST", "/api/restore", "text/plain", dump); st != 415 {
		t.Errorf("a non-JSON content type must be refused (CSRF): %d", st)
	}
	st, out := do("POST", "/api/restore", "application/json", dump)
	if st != 200 {
		t.Fatalf("restore: %d %s", st, out)
	}
	if conns, _ = e.store.Connections(e.node.ID, false); len(conns) != 1 || conns[0].APIKey() != "upstream-secret-1" {
		t.Errorf("connections after restore = %+v", conns)
	}
	if cs, _ = e.store.Combos(); len(cs) != 1 || cs[0].Name != "smart" {
		t.Errorf("combos after restore = %+v", cs)
	}
	s, _ := e.store.Settings()
	if s["customPrompt"] != "keep me" || s["password"] != "hash-1" {
		t.Errorf("settings not restored: %v", s)
	}
	if s["port"] != float64(7000) {
		t.Errorf("this machine's port must survive a restore: %v", s["port"])
	}
	if !e.store.ValidAPIKey("sk-test-key") {
		t.Error("gateway key lost")
	}
}

func TestRestoreRejectsBadFiles(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	post := func(body string) int {
		resp, err := http.Post(e.srv.URL+"/api/restore", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, body := range map[string]string{
		"not json":      `nope`,
		"wrong format":  `{"format":"other","version":1,"tables":{"combos":[]}}`,
		"wrong version": `{"format":"router-go-backup","version":9,"tables":{"combos":[]}}`,
		"no tables":     `{"format":"router-go-backup","version":1,"tables":{}}`,
		"unknown table": `{"format":"router-go-backup","version":1,"tables":{"usageHistory":[]}}`,
		"sql in column": `{"format":"router-go-backup","version":1,"tables":{"combos":[{"id) VALUES(1);--":1}]}}`,
		"missing field": `{"format":"router-go-backup","version":1,"tables":{"combos":[{"id":"x"}]}}`,
	} {
		if st := post(body); st != 400 {
			t.Errorf("%s: got %d, want 400", name, st)
		}
	}
	// a rejected file changes nothing: the seeded node and key are still there
	if !e.store.ValidAPIKey("sk-test-key") {
		t.Error("a rejected restore must roll back")
	}
	// half-valid file: first table is fine, second fails; the first must not stick
	if st := post(`{"format":"router-go-backup","version":1,"tables":{"apiKeys":[],"combos":[{"id":"x"}]}}`); st != 400 || !e.store.ValidAPIKey("sk-test-key") {
		t.Errorf("partial restore leaked: status=%d", st)
	}
}
