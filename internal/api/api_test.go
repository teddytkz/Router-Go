package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"router-go/internal/api"
	"router-go/internal/config"
	"router-go/internal/db"
)

type env struct {
	t       *testing.T
	store   *db.Store
	srv     *httptest.Server
	handler http.Handler
	node    *db.Node
	apiKey  string
}

// newEnv wires a real store + handler against a mock upstream whose behaviour is chosen per bearer key.
func newEnv(t *testing.T, upstream http.HandlerFunc, apiType string) *env {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{DataDir: dir, JWTSecret: []byte("test-secret"), APIKeySecret: "s", MachineID: "0123456789abcdef", InitialPassword: "123456"}
	h := api.Handler(cfg, store, http.NotFoundHandler())
	node, err := store.CreateNode(db.NodeTypeOpenAI, "Mock", "mock", apiType, up.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	seedModels(store, "mock")
	k, _ := store.CreateAPIKey("test", cfg.MachineID, "sk-test-key", db.Access{})
	return &env{t: t, store: store, srv: httptest.NewServer(h), handler: h, node: node, apiKey: k.Key}
}

// Only models added to a provider can be used, so tests register the ids they call.
var commonModels = []string{"m", "m2", "gpt-x", "fine", "broken", "slow", "a", "b", "good", "bad", "x", "fast", "up1", "up2", "claude"}

func seedModels(s *db.Store, prefix string) {
	for _, id := range commonModels {
		s.AddCustomModel(prefix, id, "")
	}
}

func (e *env) addKey(name, key string) *db.Connection {
	c, err := e.store.CreateConnection(e.node, name, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) chat(body string, bearer string) *http.Response {
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func upstreamKey(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func TestFallbackRotatesKeyOn429(t *testing.T) {
	var seen []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		k := upstreamKey(r)
		seen = append(seen, k)
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "gpt-x" { // prefix must be stripped
			t.Errorf("upstream model = %v", req["model"])
		}
		if k == "k1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"Rate limit reached"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}, "chat")
	c1 := e.addKey("one", "k1")
	e.addKey("two", "k2")

	resp := e.chat(`{"model":"mock/gpt-x","messages":[{"role":"user","content":"yo"}]}`, e.apiKey)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Join(seen, ",") != "k1,k2" {
		t.Fatalf("upstream key order = %v", seen)
	}
	got, _ := e.store.Connection(c1.ID)
	if got.RateLimitedUntil().IsZero() || got.BackoffLevel() != 1 {
		t.Fatalf("k1 not cooled down: until=%v level=%d", got.RateLimitedUntil(), got.BackoffLevel())
	}
	rows, _, _ := e.store.History(10, 0, "", "", "")
	if len(rows) != 1 || rows[0].Status != "ok" || rows[0].PromptTokens != 7 || rows[0].CompletionTokens != 3 {
		t.Fatalf("usage row = %+v", rows)
	}

	// second request skips the cooling key entirely
	seen = nil
	e.chat(`{"model":"mock/gpt-x","messages":[]}`, e.apiKey)
	if strings.Join(seen, ",") != "k2" {
		t.Fatalf("cooling key was retried: %v", seen)
	}
}

func TestRequestScopedErrorIsNotRetried(t *testing.T) {
	calls := 0
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"bad param"}}`)
	}, "chat")
	c := e.addKey("one", "k1")
	e.addKey("two", "k2")
	resp := e.chat(`{"model":"mock/m","messages":[]}`, e.apiKey)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "bad param") || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", resp.StatusCode, calls, b)
	}
	if got, _ := e.store.Connection(c.ID); !got.RateLimitedUntil().IsZero() {
		t.Fatal("a 400 must not bench the key")
	}
}

func TestStreamPassthroughAndUsage(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"he"}}]}`,
		`data: {"choices":[{"delta":{"content":"llo"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
		`data: [DONE]`,
	}
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["stream"] != true {
			t.Errorf("stream flag lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			io.WriteString(w, c+"\n\n")
			w.(http.Flusher).Flush()
		}
	}, "chat")
	e.addKey("one", "k1")
	resp := e.chat(`{"model":"mock/m","stream":true,"messages":[]}`, e.apiKey)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if strings.Join(lines, "\n") != strings.Join(chunks, "\n") {
		t.Fatalf("stream altered:\n%v", lines)
	}
	rows, _, _ := e.store.History(1, 0, "", "", "")
	if len(rows) != 1 || rows[0].PromptTokens != 4 || rows[0].CompletionTokens != 2 || rows[0].Status != "ok" {
		t.Fatalf("usage = %+v", rows)
	}
}

func TestGatewayAuthAndRouting(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }, "responses")
	e.addKey("one", "k1")
	for name, tc := range map[string]struct {
		bearer, body string
		want         int
	}{
		"no key":          {"", `{"model":"mock/m"}`, 401},
		"bad key":         {"nope", `{"model":"mock/m"}`, 401},
		"unknown prefix":  {e.apiKey, `{"model":"zzz/m"}`, 404},
		"no model":        {e.apiKey, `{}`, 400},
		"translated":      {e.apiKey, `{"model":"mock/m","messages":[{"role":"user","content":"hi"}]}`, 200}, // 'responses' node via chat endpoint
		"bad json":        {e.apiKey, `{`, 400},
		"unaliased plain": {e.apiKey, `{"model":"plain"}`, 404},
	} {
		if got := e.chat(tc.body, tc.bearer).StatusCode; got != tc.want {
			t.Errorf("%s: got %d want %d", name, got, tc.want)
		}
	}
	// requireApiKey=false lets keyless calls through to routing
	e.store.UpdateSettings(map[string]any{"requireApiKey": false})
	if got := e.chat(`{"model":"zzz/m"}`, "").StatusCode; got != 404 {
		t.Errorf("keyless with requireApiKey=false: got %d", got)
	}
}

func TestAllKeysCoolingReturns429(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, "chat")
	e.addKey("one", "k1")
	e.chat(`{"model":"mock/m","messages":[]}`, e.apiKey) // benches k1
	resp := e.chat(`{"model":"mock/m","messages":[]}`, e.apiKey)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	post := func(remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("127.0.0.1:1", `{"password":"wrong"}`); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	for _, remote := range []string{"8.8.8.8:1", "127.0.0.1:1"} { // the default password works from anywhere
		if rec := post(remote, `{"password":"123456"}`); rec.Code != 200 || len(rec.Result().Cookies()) == 0 {
			t.Fatalf("default password from %s: %d", remote, rec.Code)
		}
	}
	rec := post("127.0.0.1:1", `{"password":"123456"}`)
	cookie := rec.Result().Cookies()[0]

	req := httptest.NewRequest("GET", "/api/provider-nodes", nil)
	if r2 := httptest.NewRecorder(); func() int { e.handler.ServeHTTP(r2, req); return r2.Code }() != 401 {
		t.Fatal("dashboard API must require a session")
	}
	req = httptest.NewRequest("GET", "/api/provider-nodes", nil)
	req.AddCookie(cookie)
	r3 := httptest.NewRecorder()
	e.handler.ServeHTTP(r3, req)
	if r3.Code != 200 {
		t.Fatalf("with cookie: %d", r3.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	var last int
	for range 5 {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"x"}`))
		req.RemoteAddr = "127.0.0.1:9"
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != 429 {
		t.Fatalf("5th failure should lock out, got %d", last)
	}
}

func TestPasswordChangeAndConnectionMasking(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	c := e.addKey("one", "sk-supersecretvalue")
	flat := c.Flat()
	if _, leaked := flat["apiKey"]; leaked || flat["apiKeyMasked"] == "sk-supersecretvalue" {
		t.Fatalf("api key leaked in %v", flat)
	}
	// the DB `data` blob stays compatible: apiKey + providerSpecificData live inside it
	var raw string
	e.store.DB.QueryRow(`SELECT data FROM providerConnections WHERE id=?`, c.ID).Scan(&raw)
	if !strings.Contains(raw, `"apiKey":"sk-supersecretvalue"`) || !strings.Contains(raw, `"providerSpecificData"`) {
		t.Fatalf("stored data = %s", raw)
	}
}

// Clients that append /v1 to a base URL already ending in /v1 must still reach the same routes.
func TestDoubledV1PrefixIsCollapsed(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }, "chat")
	for _, p := range []string{"/v1/models", "/v1/v1/models", "/v1/v1/v1/models"} {
		req, _ := http.NewRequest("GET", e.srv.URL+p, nil)
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", p, resp.StatusCode)
		}
	}
	// a path that merely starts with "/v1/v1x" is left alone
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/v1x", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("/v1/v1x unexpectedly 200")
		}
	}
}
