package api_test

import (
	"encoding/json"
	"fmt"
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

// A model test and a key test are upstream calls like any other: they appear in the usage history, with the endpoint
// that tells them apart from gateway traffic, and the tokens the probe answer reported. A cached repeat is not a call.
func TestDashboardTestsAreRecordedInUsage(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1}}\n\ndata: [DONE]\n"))
	}, "chat")
	c := e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})

	e.json("POST", "/api/models/test", `{"model":"mock/m"}`)
	e.json("POST", "/api/models/test", `{"model":"mock/m"}`) // answered from the 10 s cache: no upstream call, no row
	e.json("POST", "/api/providers/"+c.ID+"/test", `{}`)

	rows, _, err := e.store.History(10, 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		if r.Provider != e.node.ID || r.ConnectionID != c.ID {
			t.Errorf("row not attributed to the tested connection: %+v", r)
		}
		got[r.Endpoint] = fmt.Sprintf("%s %s %d/%d", r.Model, r.Status, r.PromptTokens, r.CompletionTokens)
	}
	// the key test only lists models, so it has no tokens; the model test carries the probe's usage
	if len(rows) != 2 || got["/dashboard/test-model"] != "mock/m ok 9/1" || got["/dashboard/test-key"] != "mock/(list models) ok 0/0" {
		t.Errorf("usage rows = %d %v", len(rows), got)
	}
}

// A strict upstream that rejects stream_options still passes the model test: it is asked again without the option.
func TestModelTestRetriesWithoutStreamOptions(t *testing.T) {
	var asked []bool
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		_, has := body["stream_options"]
		asked = append(asked, has)
		if has {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Unknown parameter: stream_options"}}`))
			return
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\ndata: [DONE]\n"))
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	if _, out := e.json("POST", "/api/models/test", `{"model":"mock/m"}`); out["ok"] != true || len(asked) != 2 || !asked[0] || asked[1] {
		t.Errorf("out=%v asked=%v", out, asked)
	}
}

// The model test asks a real question, not a bare "ping".
func TestModelTestSendsGreetingPrompt(t *testing.T) {
	var prompt string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			prompt = body.Messages[0].Content
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\ndata: [DONE]\n"))
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})

	if _, out := e.json("POST", "/api/models/test", `{"model":"mock/m"}`); out["ok"] != true {
		t.Fatalf("model test failed: %v", out)
	}
	if prompt != "hi, who are you? are you ai?" {
		t.Errorf("probe prompt = %q", prompt)
	}
}
