package api_test

import (
	"net/http"
	"strings"
	"testing"

	"router-go/internal/provider"
	_ "router-go/internal/provider/builtin"
)

// Adding a key checks it: a rejected key is refused, a good one is stored already "active" (not "untested"), with the
// priority and proxy pool from the dialog. Same for a custom node, whose key is checked right after it is stored.
func TestAddKeyIsCheckedAndConnected(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") || r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	t.Cleanup(provider.Override("tokenharbor", strings.TrimSuffix(e.node.BaseURL(), "/v1")+"/v1"))

	// the Check button
	if st, out := e.json("POST", "/api/builtin-providers/tokenharbor/validate", `{"apiKey":"good"}`); st != 200 || out["valid"] != true {
		t.Errorf("check good: %d %v", st, out)
	}
	if _, out := e.json("POST", "/api/builtin-providers/tokenharbor/validate", `{"apiKey":"nope"}`); out["valid"] == true {
		t.Errorf("check bad: %v", out)
	}
	if st, _ := e.json("POST", "/api/builtin-providers/cline/validate", `{"apiKey":"x"}`); st != 400 {
		t.Errorf("a token provider has no key check: %d", st)
	}

	// built-in: bad key refused and not stored; good key stored as active, at the requested priority
	if st, _ := e.json("POST", "/api/providers", `{"provider":"tokenharbor","name":"bad","apiKey":"nope"}`); st != 400 {
		t.Errorf("bad key: %d", st)
	}
	st, out := e.json("POST", "/api/providers", `{"provider":"tokenharbor","name":"ok","apiKey":"good","priority":3}`)
	c, _ := out["connection"].(map[string]any)
	if st != 201 || c["testStatus"] != "active" || c["priority"] != float64(3) {
		t.Errorf("good key: %d %v", st, out)
	}
	if st, _ := e.json("POST", "/api/providers", `{"provider":"tokenharbor","name":"p","apiKey":"good","proxyPoolId":"missing"}`); st != 400 {
		t.Errorf("unknown proxy pool: %d", st)
	}

	// custom node: stored either way, but the outcome is recorded at once
	for key, want := range map[string]string{"good": "active", "nope": "error"} {
		_, out := e.json("POST", "/api/providers", `{"provider":"`+e.node.ID+`","name":"k-`+key+`","apiKey":"`+key+`"}`)
		if c, _ := out["connection"].(map[string]any); c["testStatus"] != want {
			t.Errorf("custom node key %q: testStatus = %v, want %s", key, c["testStatus"], want)
		}
	}
}
