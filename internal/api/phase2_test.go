package api_test

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"router-go/internal/api"
	"router-go/internal/config"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"router-go/internal/db"
	"router-go/internal/provider"
)

func (e *env) post(path, body, bearer string, hdr ...string) *http.Response {
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *env) models(bearer string) []string {
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Data []struct{ ID string } }
	json.NewDecoder(resp.Body).Decode(&out)
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// A chat client talking to a Responses upstream and an Anthropic client talking to a chat upstream.
func TestTranslation(t *testing.T) {
	var path string
	var got map[string]any
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(path, "/responses") {
			io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}, "responses")
	e.addKey("one", "k1")

	// chat client → responses upstream
	resp := e.chat(`{"model":"mock/m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"ping"}]}`, e.apiKey)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasSuffix(path, "/responses") || got["input"] == nil || got["messages"] != nil {
		t.Fatalf("status=%d path=%s upstream-body=%v", resp.StatusCode, path, got)
	}
	if !strings.Contains(string(body), `"chat.completion"`) || !strings.Contains(string(body), "pong") {
		t.Fatalf("client body = %s", body)
	}
	if rows, _, _ := e.store.History(1, 0, "", "", ""); len(rows) != 1 || rows[0].PromptTokens != 3 || rows[0].CompletionTokens != 1 {
		t.Fatalf("usage = %+v", rows)
	}

	// anthropic client → responses upstream
	resp = e.post("/v1/messages", `{"model":"mock/m","max_tokens":50,"system":"be brief","messages":[{"role":"user","content":"ping"}]}`, e.apiKey)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"type":"message"`) || !strings.Contains(string(body), "pong") {
		t.Fatalf("messages client: status=%d body=%s", resp.StatusCode, body)
	}

	// Anthropic-shaped error envelope on /v1/messages
	resp = e.post("/v1/messages", `{"model":"zzz/m","max_tokens":5,"messages":[]}`, e.apiKey)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(body), `"type":"error"`) {
		t.Fatalf("error envelope: %d %s", resp.StatusCode, body)
	}
}

func TestAnthropicNodeAndStreamTranslation(t *testing.T) {
	var hdr http.Header
	var path string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		hdr, path = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			"message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}",
			"content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
			"content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}",
			"content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}",
			"content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
			"message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}",
			"message_stop\ndata: {\"type\":\"message_stop\"}",
		} {
			io.WriteString(w, "event: "+ev+"\n\n")
			w.(http.Flusher).Flush()
		}
	}, "chat")
	// turn the mock node into an anthropic-compatible one
	up := e.node.BaseURL()
	an, err := e.store.CreateNode(db.NodeTypeAnthropic, "Claude", "anth", "", up)
	if err != nil {
		t.Fatal(err)
	}
	seedModels(e.store, "anth")
	if _, err := e.store.CreateConnection(an, "k", "sk-ant"); err != nil {
		t.Fatal(err)
	}

	resp := e.chat(`{"model":"anth/claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	raw, _ := io.ReadAll(resp.Body)
	text := string(raw)
	if resp.StatusCode != 200 || !strings.HasSuffix(path, "/messages") || hdr.Get("x-api-key") != "sk-ant" || hdr.Get("anthropic-version") == "" || hdr.Get("Authorization") != "" {
		t.Fatalf("status=%d path=%s headers=%v", resp.StatusCode, path, hdr)
	}
	if !strings.Contains(text, `"hel"`) || !strings.Contains(text, `"lo"`) || !strings.HasSuffix(strings.TrimSpace(text), "[DONE]") {
		t.Fatalf("chat SSE = %s", text)
	}
	rows, _, _ := e.store.History(1, 0, "", "", "")
	if len(rows) != 1 || rows[0].PromptTokens != 7 || rows[0].CompletionTokens != 5 {
		t.Fatalf("usage = %+v", rows)
	}
}

func TestCombosFallbackAndRoundRobin(t *testing.T) {
	var hits []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		hits = append(hits, req["model"].(string))
		if req["model"] == "bad" {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")

	if _, err := e.store.CreateCombo("duo", []string{"mock/bad", "mock/good"}, "", nil); err != nil {
		t.Fatal(err)
	}
	resp := e.chat(`{"model":"duo","messages":[]}`, e.apiKey)
	// mock/bad 503 benches k1 → member 2 finds the only key cooling → combo exhausted
	if resp.StatusCode != 503 || !strings.Contains(strings.Join(hits, ","), "bad") {
		t.Fatalf("status=%d hits=%v", resp.StatusCode, hits)
	}

	// second provider: member order falls through to it
	n2, _ := e.store.CreateNode(db.NodeTypeOpenAI, "Two", "two", "chat", e.node.BaseURL())
	seedModels(e.store, "two")
	e.store.CreateConnection(n2, "k2", "k2")
	e.store.CreateCombo("mix", []string{"mock/bad", "two/good"}, "", nil)
	hits = nil
	resp = e.chat(`{"model":"mix","messages":[]}`, e.apiKey)
	if resp.StatusCode != 200 {
		t.Fatalf("fallback status=%d hits=%v", resp.StatusCode, hits)
	}

	// round-robin: successive requests start at different members
	e.store.CreateCombo("rr", []string{"two/a", "two/b"}, "round-robin", nil)
	hits = nil
	for range 4 {
		e.chat(`{"model":"rr","messages":[]}`, e.apiKey)
	}
	if strings.Join(hits, ",") != "a,b,a,b" {
		t.Fatalf("round-robin order = %v", hits)
	}
}

func TestKeyAccessAndCustomModels(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"up1"},{"id":"up2"}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.CreateCombo("team", []string{"mock/up1"}, "", nil)
	e.store.AddCustomModel("mock", "extra", "")
	e.store.SetAlias("fast", "mock/up1")

	restricted, _ := e.store.CreateAPIKey("r", "m", "sk-restricted", db.Access{Restricted: true, Allow: []string{"mock/up2", "team"}})
	body := func(m string) string { return `{"model":"` + m + `","messages":[]}` }
	for model, want := range map[string]int{
		"mock/up2":   200, // listed
		"MOCK/UP2":   200, // exact identity, case-insensitive... prefix is case-sensitive so this is a 403 below
		"team":       200, // listed combo
		"mock/up1":   403, // member reached directly is not granted by the combo
		"fast":       403, // alias → mock/up1 is not listed
		"mock/other": 403,
		"zzz/x":      403, // unknown names are not revealed
	} {
		got := e.chat(body(model), restricted.Key).StatusCode
		if model == "MOCK/UP2" {
			want = 403
		}
		if got != want {
			t.Errorf("%s: got %d want %d", model, got, want)
		}
	}
	// an alias to a listed target is that target
	e.store.SetAlias("alias2", "mock/up2")
	if got := e.chat(body("alias2"), restricted.Key).StatusCode; got != 200 {
		t.Errorf("alias to allowed model: %d", got)
	}

	if got := e.models(restricted.Key); len(got) != 2 || !has(got, "team") || !has(got, "mock/up2") {
		t.Errorf("restricted /v1/models = %v", got)
	}
	all := e.models(e.apiKey)
	for _, want := range []string{"team", "mock/up1", "mock/up2", "mock/extra"} { // only added models are listed (the upstream also reports "other")
		if !has(all, want) {
			t.Errorf("unrestricted /v1/models missing %s: %v", want, all)
		}
	}
	if has(all, "mock/other") {
		t.Errorf("a model the upstream lists but nobody added must not be listed: %v", all)
	}

	// a corrupt allow list on a restricted key must deny, never allow
	e.store.DB.Exec(`UPDATE apiKeys SET accessAllow='not json' WHERE id=?`, restricted.ID)
	if got := e.chat(body("mock/up2"), restricted.Key).StatusCode; got != 403 {
		t.Errorf("corrupt allow list: got %d, want 403", got)
	}
}

// dashboard CRUD for combos, aliases, custom models, key access and anthropic nodes (session guard off for brevity)
func TestDashboardPhase2API(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	expect := func(what string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %d want %d", what, got, want)
		}
	}

	st, out := call("POST", "/api/combos", `{"name":"best","models":["mock/a","mock/b"],"strategy":"round-robin"}`)
	expect("create combo", st, 201)
	id := out["combo"].(map[string]any)["id"].(string)
	st, _ = call("POST", "/api/combos", `{"name":"best","models":["mock/a"]}`)
	expect("duplicate combo", st, 409)
	st, _ = call("POST", "/api/combos", `{"name":"bad name!","models":["mock/a"]}`)
	expect("invalid combo name", st, 400)
	st, _ = call("POST", "/api/combos", `{"name":"empty","models":[]}`)
	expect("combo without models", st, 400)
	st, out = call("PUT", "/api/combos/"+id, `{"name":"better","strategy":"fallback"}`)
	expect("rename combo", st, 200)
	if c := out["combo"].(map[string]any); c["name"] != "better" || c["strategy"] != "fallback" || len(c["models"].([]any)) != 2 {
		t.Errorf("renamed combo = %v", c)
	}
	st, out = call("GET", "/api/combos", "")
	if cs := out["combos"].([]any); st != 200 || len(cs) != 1 {
		t.Errorf("list combos: %d %v", st, out)
	}
	st, _ = call("DELETE", "/api/combos/"+id, "")
	expect("delete combo", st, 200)

	st, _ = call("PUT", "/api/models/alias", `{"alias":"fast","model":"mock/a"}`)
	expect("set alias", st, 200)
	st, _ = call("PUT", "/api/models/alias", `{"alias":"x","model":"nope"}`)
	expect("alias without prefix", st, 400)
	if _, out = call("GET", "/api/models/alias", ""); out["aliases"].(map[string]any)["fast"] != "mock/a" {
		t.Errorf("aliases = %v", out)
	}
	st, _ = call("DELETE", "/api/models/alias?alias=fast", "")
	expect("delete alias", st, 200)

	st, _ = call("POST", "/api/models/custom", `{"providerAlias":"mock","id":"ft-1","name":"Fine tune"}`)
	expect("add custom model", st, 201)
	st, _ = call("POST", "/api/models/custom", `{"providerAlias":"nope","id":"x"}`)
	expect("custom model for unknown prefix", st, 400)
	hasFT := func(out map[string]any) bool {
		for _, m := range out["models"].([]any) {
			if m.(map[string]any)["id"] == "ft-1" {
				return true
			}
		}
		return false
	}
	if _, out = call("GET", "/api/models/custom", ""); !hasFT(out) {
		t.Errorf("custom models = %v", out)
	}
	st, _ = call("DELETE", "/api/models/custom?providerAlias=mock&id=ft-1", "")
	expect("delete custom model", st, 200)
	if _, out = call("GET", "/api/models/custom", ""); hasFT(out) {
		t.Errorf("deleted model still listed: %v", out)
	}

	st, out = call("POST", "/api/keys", `{"name":"scoped","access":{"restricted":true,"allow":["team"," TEAM ","mock/a"]}}`)
	expect("create restricted key", st, 201)
	k := out["key"].(map[string]any)
	if a := k["access"].(map[string]any); a["restricted"] != true || len(a["allow"].([]any)) != 2 {
		t.Errorf("access = %v", a)
	}
	st, _ = call("PATCH", "/api/keys/"+k["id"].(string), `{"access":{"restricted":true,"allow":[`+strings.Repeat(`"x",`, 201)+`"y"]}}`)
	expect("oversized allow list", st, 400)
	st, _ = call("PATCH", "/api/keys/"+k["id"].(string), `{"access":{"restricted":false}}`)
	expect("unrestrict", st, 200)
	_, out = call("GET", "/api/keys", "")
	for _, raw := range out["keys"].([]any) {
		if m := raw.(map[string]any); m["id"] == k["id"] && m["access"].(map[string]any)["restricted"] != false {
			t.Errorf("key still restricted: %v", m)
		}
	}

	st, out = call("POST", "/api/provider-nodes", `{"type":"anthropic-compatible","name":"Claude","prefix":"anth","baseUrl":"https://api.anthropic.com/v1"}`)
	expect("create anthropic node", st, 201)
	if n := out["node"].(map[string]any); n["type"] != "anthropic-compatible" || n["apiType"] != nil || !strings.HasPrefix(n["id"].(string), "anthropic-compatible-") {
		t.Errorf("anthropic node = %v", n)
	}
	st, _ = call("POST", "/api/provider-nodes", `{"type":"anthropic-compatible","name":"Dup","prefix":"mock"}`)
	expect("prefix shared across node types", st, 409)
}

func TestUsageStatsCacheAndBuckets(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6}}}`)
	}, "chat")
	e.addKey("one", "k1")
	e.chat(`{"model":"mock/m","messages":[]}`, e.apiKey)
	st, err := e.store.Stats(24)
	if err != nil || st.Requests != 1 || st.PromptTokens != 10 || st.CompletionTokens != 2 || st.CacheTokens != 6 || st.Bucket != "hour" {
		t.Fatalf("stats = %+v err=%v", st, err)
	}
	if last := st.Series[len(st.Series)-1]; last.CacheTokens != 6 || last.Requests != 1 {
		t.Fatalf("last bucket = %+v", last)
	}
	if st, _ = e.store.Stats(24 * 7); st.Bucket != "day" || len(st.Series) < 7 || st.CacheTokens != 6 {
		t.Fatalf("7d stats = bucket %s, %d points, cache %d", st.Bucket, len(st.Series), st.CacheTokens)
	}
}

func TestUsageSummaryAndReset(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}, "chat")
	e.addKey("acct", "k1")
	e.chat(`{"model":"mock/m","messages":[]}`, e.apiKey)
	e.chat(`{"model":"mock/m2","messages":[]}`, e.apiKey)
	for _, p := range []string{"today", "24h", "7d", "month", "all"} {
		s, err := e.store.Summary(p)
		if err != nil || s.Requests != 2 || s.PromptTokens != 20 || len(s.Series) == 0 {
			t.Fatalf("%s: %+v err=%v", p, s, err)
		}
	}
	s, _ := e.store.Summary("today")
	if len(s.Breakdown) != 2 || s.Breakdown[0].ProviderName != "Mock" || s.Breakdown[0].Account != "acct" || s.Breakdown[0].KeyName != "test" {
		t.Fatalf("breakdown = %+v", s.Breakdown)
	}
	if s.Live.Requests != 2 || s.Live.ModelsActive != 2 || len(s.Recent) != 2 || s.Bucket != "hour" {
		t.Fatalf("live=%+v recent=%d bucket=%s", s.Live, len(s.Recent), s.Bucket)
	}
	if n, err := e.store.ResetUsage(); err != nil || n != 2 {
		t.Fatalf("reset: %d %v", n, err)
	}
	if s, _ = e.store.Summary("all"); s.Requests != 0 || len(s.Series) != 0 {
		t.Fatalf("after reset: %+v", s)
	}
}

// Token Saver (ponytail, watermarks remover, custom prompt) and Guards & Shields end to end via /api/settings.
func TestTokenSaverAndGuardsSettings(t *testing.T) {
	var seen map[string]any
	var seenMu sync.Mutex
	var inflight, peak int32
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		seenMu.Lock()
		seen = got
		seenMu.Unlock()
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	patch := func(body string) int {
		req, _ := http.NewRequest("PATCH", e.srv.URL+"/api/settings", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	system := func() string {
		seenMu.Lock()
		defer seenMu.Unlock()
		msgs, _ := seen["messages"].([]any)
		if len(msgs) == 0 {
			return ""
		}
		m, _ := msgs[0].(map[string]any)
		s, _ := m["content"].(string)
		if m["role"] != "system" {
			return ""
		}
		return s
	}
	body := `{"model":"mock/m","messages":[{"role":"user","content":"hi"}]}`

	e.chat(body, e.apiKey)
	if system() != "" {
		t.Fatalf("nothing is injected by default, got %q", system())
	}
	if patch(`{"ponytailEnabled":true,"ponytailLevel":"ultra","watermarksRemoverEnabled":true,"customPromptEnabled":true,"customPrompt":"ALWAYS REPLY IN HAIKU"}`) != 200 {
		t.Fatal("settings patch")
	}
	e.chat(body, e.apiKey)
	s := system()
	for _, want := range []string{"YAGNI extremist", "Text Hygiene", "ALWAYS REPLY IN HAIKU"} {
		if !strings.Contains(s, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if patch(`{"ponytailLevel":"bogus"}`) != 400 || patch(`{"customPrompt":"`+strings.Repeat("x", 8001)+`"}`) != 400 {
		t.Error("invalid ponytail level / oversized custom prompt must be rejected")
	}
	patch(`{"ponytailEnabled":false,"watermarksRemoverEnabled":false,"customPromptEnabled":false}`)
	e.chat(body, e.apiKey)
	if system() != "" {
		t.Errorf("toggled off but still injected: %q", system())
	}

	// semaphore: maxConcurrency=1 serialises requests to one account; 0 would be unlimited
	e.store.DB.Exec(`UPDATE providerConnections SET data=json_set(data,'$.providerSpecificData.maxConcurrency',1)`)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := e.chat(body, e.apiKey).StatusCode; got != 200 {
				t.Errorf("queued request: %d", got)
			}
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("peak concurrency = %d, want 1", peak)
	}
	// ...and switching the semaphore off lifts the cap
	patch(`{"semaphoreEnabled":false}`)
	atomic.StoreInt32(&peak, 0)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); e.chat(body, e.apiKey) }()
	}
	wg.Wait()
	if peak < 2 {
		t.Errorf("semaphore off: peak = %d, want >1", peak)
	}
}

func TestPortSetting(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	// the env helper's cfg is private to newEnv, so build our own handler around a fresh config
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, JWTSecret: []byte("s"), APIKeySecret: "s", MachineID: "0123456789abcdef", InitialPassword: "123456"}
	cfg.ResolvePort(0, 0)
	var moved []int
	var rebindErr error
	cfg.Rebind = func(p int) error {
		if rebindErr == nil {
			moved = append(moved, p)
		}
		return rebindErr
	}
	srv := httptest.NewServer(api.Handler(cfg, e.store, http.NotFoundHandler()))
	t.Cleanup(srv.Close)
	patch := func(body string) (int, map[string]any) {
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/settings", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	get := func() map[string]any {
		resp, err := http.Get(srv.URL + "/api/settings")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	if s := get(); s["activePort"] != float64(20128) || s["portSource"] != "default" || s["defaultPort"] != float64(20128) {
		t.Fatalf("initial = %v", s)
	}
	if st, _ := patch(`{"port":0}`); st != 400 {
		t.Errorf("port 0: %d", st)
	}
	if st, _ := patch(`{"port":70000}`); st != 400 {
		t.Errorf("port 70000: %d", st)
	}
	if st, _ := patch(`{"port":8123}`); st != 200 || len(moved) != 1 || moved[0] != 8123 {
		t.Fatalf("change port: status=%d moved=%v", st, moved)
	}
	if s := get(); s["activePort"] != float64(8123) || s["portSource"] != "settings" || s["port"] != float64(8123) {
		t.Fatalf("after change = %v", s)
	}
	rebindErr = errors.New("address already in use")
	if st, out := patch(`{"port":9000}`); st != 409 || !strings.Contains(out["error"].(string), "9000") {
		t.Errorf("busy port: %d %v", st, out)
	}
	if s := get(); s["activePort"] != float64(8123) || s["port"] != float64(8123) {
		t.Errorf("a failed move must not change anything: %v", s)
	}
	// --port wins: the value is saved for later but the running server stays put
	rebindErr, moved = nil, nil
	cfg.SetPort(7777, config.PortFromFlag)
	if st, _ := patch(`{"port":8200}`); st != 200 || len(moved) != 0 {
		t.Errorf("flag override: status=%d moved=%v", st, moved)
	}
	if s := get(); s["activePort"] != float64(7777) || s["portSource"] != "flag" || s["port"] != float64(8200) {
		t.Errorf("flag override state = %v", s)
	}
}

// Provider/model activity: in-flight requests are listed while they run, and usage records the concrete model.
func TestActiveRequestsAndProviderActivity(t *testing.T) {
	hold, started := make(chan struct{}), make(chan struct{}, 1)
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] == "slow" {
			started <- struct{}{}
			<-hold
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("acct", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	e.store.CreateCombo("pick", []string{"mock/fast"}, "", nil)
	active := func() []any {
		resp, err := http.Get(e.srv.URL + "/api/usage/active")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Active []any }
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Active
	}
	if len(active()) != 0 {
		t.Fatal("nothing should be active yet")
	}

	done := make(chan struct{})
	go func() { e.chat(`{"model":"mock/slow","messages":[]}`, e.apiKey); close(done) }()
	<-started
	act := active()
	if len(act) != 1 {
		t.Fatalf("active = %v", act)
	}
	if a := act[0].(map[string]any); a["model"] != "mock/slow" || a["account"] != "acct" || a["provider"] != e.node.ID {
		t.Fatalf("active entry = %v", a)
	}
	close(hold)
	<-done
	if len(active()) != 0 {
		t.Fatal("finished request must leave the active list")
	}

	e.chat(`{"model":"pick","messages":[]}`, e.apiKey) // a combo: usage must name the member, not the combo
	s, err := e.store.Summary("today")
	if err != nil || len(s.ProviderActivity) != 1 || s.ProviderActivity[0].Provider != e.node.ID {
		t.Fatalf("provider activity = %+v err=%v", s.ProviderActivity, err)
	}
	models := map[string]bool{}
	for _, m := range s.ProviderActivity[0].Models {
		models[m.Model] = true
	}
	if !models["mock/slow"] || !models["mock/fast"] || models["pick"] {
		t.Fatalf("models = %v", models)
	}
}

func TestModelTestAndRoutingHealth(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] == "broken" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"model not found"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) map[string]any {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	if r := call("POST", "/api/models/test", `{"model":"mock/fine"}`); r["ok"] != true || r["latencyMs"] == nil {
		t.Errorf("good model: %v", r)
	}
	if r := call("POST", "/api/models/test", `{"model":"mock/broken"}`); r["ok"] != false || r["error"] != "HTTP 400: model not found" {
		t.Errorf("broken model: %v", r)
	}
	if r := call("POST", "/api/models/test", `{"model":"zzz/x"}`); r["ok"] != false {
		t.Errorf("unknown prefix: %v", r)
	}

	e.chat(`{"model":"mock/fine","messages":[]}`, e.apiKey)
	e.chat(`{"model":"mock/broken","messages":[]}`, e.apiKey) // a 400 is a failed request but not a provider failure
	h := call("GET", "/api/health/routing", "")["providers"].([]any)
	if len(h) != 1 {
		t.Fatalf("providers = %v", h)
	}
	if p := h[0].(map[string]any); p["circuit"] != "CLOSED" || p["requests"] != float64(2) || p["successRate"] != float64(50) || p["name"] != "Mock" {
		t.Errorf("health = %v", p)
	}
}

// Renaming a node's prefix must not orphan custom models, aliases, combos or key allow lists.
func TestPrefixRenameMigratesReferences(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	e.store.AddCustomModel("mock", "ft-1", "Tune")
	e.store.SetAlias("fast", "mock/up1")
	e.store.CreateCombo("duo", []string{"mock/up1", "other/x"}, "", nil)
	k, _ := e.store.CreateAPIKey("r", "m", "sk-r", db.Access{Restricted: true, Allow: []string{"Mock/up1", "duo"}})

	req, _ := http.NewRequest("PATCH", e.srv.URL+"/api/provider-nodes/"+e.node.ID, strings.NewReader(`{"prefix":"renamed"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("rename: %v %v", resp, err)
	}
	resp.Body.Close()

	cms, _ := e.store.CustomModels()
	found := false
	for _, m := range cms {
		if m.ProviderAlias != "renamed" {
			t.Errorf("model left under the old prefix: %+v", m)
		}
		found = found || m.ID == "ft-1"
	}
	if !found {
		t.Errorf("custom model lost in the rename: %+v", cms)
	}
	if al, _ := e.store.Aliases(); al["fast"] != "renamed/up1" {
		t.Errorf("aliases = %v", al)
	}
	if c, _ := e.store.ComboByName("duo"); len(c.Models) != 2 || c.Models[0] != "renamed/up1" || c.Models[1] != "other/x" {
		t.Errorf("combo = %+v", c)
	}
	if got := e.store.APIKeyByValue(k.Key).Access.Allow; len(got) != 2 || got[0] != "renamed/up1" || got[1] != "duo" {
		t.Errorf("allow list = %v", got)
	}
	if got := e.chat(`{"model":"fast","messages":[]}`, e.apiKey).StatusCode; got != 200 {
		t.Errorf("alias still routes? %d", got)
	}
}

func TestTestBatchLogsEveryKey(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "bad" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}, "chat")
	good, bad := e.addKey("good-key", "good"), e.addKey("bad-key", "bad")
	other, _ := e.store.CreateNode(db.NodeTypeOpenAI, "Other", "oth", "chat", e.node.BaseURL())
	seedModels(e.store, "oth")
	e.store.CreateConnection(other, "other-key", "good")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	post := func(body string) map[string]any {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/providers/test-batch", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	out := post(`{}`)
	sum := out["summary"].(map[string]any)
	if sum["total"] != float64(3) || sum["passed"] != float64(2) || sum["failed"] != float64(1) {
		t.Fatalf("summary = %v", sum)
	}
	for _, raw := range out["results"].([]any) {
		r := raw.(map[string]any)
		if r["name"] == "bad-key" && (r["ok"] != false || r["error"] != "API key unauthorized" || r["providerName"] != "Mock") {
			t.Errorf("bad key entry = %v", r)
		}
		if r["name"] == "good-key" && (r["ok"] != true || r["latencyMs"] == nil) {
			t.Errorf("good key entry = %v", r)
		}
	}
	// a list of providers (one section of the page)
	if sum := post(`{"providers":["` + e.node.ID + `","` + other.ID + `"]}`)["summary"].(map[string]any); sum["total"] != float64(3) {
		t.Errorf("provider list summary = %v", sum)
	}
	if sum := post(`{"providers":["` + other.ID + `"]}`)["summary"].(map[string]any); sum["total"] != float64(1) {
		t.Errorf("one provider via the list = %v", sum)
	}
	// one provider only, and the outcome is recorded on the connection
	if sum := post(`{"providerId":"` + e.node.ID + `"}`)["summary"].(map[string]any); sum["total"] != float64(2) {
		t.Errorf("single provider summary = %v", sum)
	}
	if c, _ := e.store.Connection(bad.ID); c.Data["testStatus"] != "error" {
		t.Errorf("bad key status = %v", c.Data["testStatus"])
	}
	if c, _ := e.store.Connection(good.ID); c.Data["testStatus"] != "active" {
		t.Errorf("good key status = %v", c.Data["testStatus"])
	}
}

// The console log shows what the gateway did: requests, upstream failures, fallbacks; it streams live and can be cleared.
func TestConsoleLogStream(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "k1" { // the first provider's key is rate limited
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	}, "chat")
	e.addKey("k-one", "k1")
	two, _ := e.store.CreateNode(db.NodeTypeOpenAI, "Two", "two", "chat", e.node.BaseURL())
	seedModels(e.store, "two")
	e.store.CreateConnection(two, "k-two", "k2")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	e.store.CreateCombo("fb", []string{"mock/x", "two/fine"}, "", nil)

	resp, err := http.Get(e.srv.URL + "/api/console-logs/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
	events := make(chan map[string]any, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				var m map[string]any
				if json.Unmarshal([]byte(line[6:]), &m) == nil {
					events <- m
				}
			}
		}
		close(events)
	}()
	next := func() map[string]any {
		select {
		case m, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no event in 3s")
		}
		return nil
	}
	if m := next(); m["type"] != "init" {
		t.Fatalf("first event = %v", m)
	}

	e.chat(`{"model":"fb","messages":[]}`, e.apiKey) // first member is rate limited: cooldown, fallback, then success
	var lines []string
	for len(lines) < 3 {
		m := next()
		if m["type"] == "lines" {
			for _, raw := range m["entries"].([]any) {
				ent := raw.(map[string]any)
				lines = append(lines, ent["level"].(string)+" "+ent["msg"].(string))
			}
		}
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"WARN [UPSTREAM] Mock/k-one → 429", "INFO [COMBO] fb: mock/x failed (429)", "INFO [CHAT] fb → two/fine via Two/k-two"} {
		if !strings.Contains(all, want) {
			t.Errorf("log is missing %q in:\n%s", want, all)
		}
	}

	req, _ := http.NewRequest("DELETE", e.srv.URL+"/api/console-logs", nil)
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 200 {
		t.Fatalf("clear: %v %v", r, err)
	}
	for {
		if m := next(); m["type"] == "clear" {
			break
		}
	}
}

// A provider's upstream models are not usable until they are added to it.
func TestModelMustBeAddedBeforeUse(t *testing.T) {
	var hits int32
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"listed-upstream"}]}`)
			return
		}
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})

	resp := e.chat(`{"model":"mock/listed-upstream","messages":[]}`, e.apiKey)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(body), "not added to provider 'Mock'") || !strings.Contains(string(body), "model_not_added") || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("unadded model: status=%d hits=%d body=%s", resp.StatusCode, hits, body)
	}
	if has(e.models(e.apiKey), "mock/listed-upstream") {
		t.Error("an unadded model must not appear in /v1/models")
	}
	// the dashboard still sees the upstream's list to pick from, separately from what is usable
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/provider-nodes/"+e.node.ID+"/models", nil)
	r2, _ := http.DefaultClient.Do(req)
	var nm struct{ Models, Upstream []string }
	json.NewDecoder(r2.Body).Decode(&nm)
	r2.Body.Close()
	if !has(nm.Upstream, "listed-upstream") || has(nm.Models, "listed-upstream") {
		t.Errorf("node models: usable=%v upstream=%v", nm.Models, nm.Upstream)
	}

	post, _ := http.NewRequest("POST", e.srv.URL+"/api/models/custom", strings.NewReader(`{"providerAlias":"mock","id":"listed-upstream"}`))
	if r, err := http.DefaultClient.Do(post); err != nil || r.StatusCode != 201 {
		t.Fatalf("add model: %v %v", r, err)
	}
	if got := e.chat(`{"model":"mock/listed-upstream","messages":[]}`, e.apiKey).StatusCode; got != 200 || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("after adding: status=%d hits=%d", got, hits)
	}
	if !has(e.models(e.apiKey), "mock/listed-upstream") {
		t.Error("an added model must appear in /v1/models")
	}
	// aliases and combos go through the same rule
	e.store.SetAlias("al", "mock/not-added")
	e.store.CreateCombo("cb", []string{"mock/not-added", "mock/listed-upstream"}, "", nil)
	if got := e.chat(`{"model":"al","messages":[]}`, e.apiKey).StatusCode; got != 404 {
		t.Errorf("alias to an unadded model: %d", got)
	}
	if got := e.chat(`{"model":"cb","messages":[]}`, e.apiKey).StatusCode; got != 200 {
		t.Errorf("a combo skips an unadded member and uses the next: %d", got)
	}
	// a restricted key learns nothing about which models exist
	rk, _ := e.store.CreateAPIKey("r", "m", "sk-rr", db.Access{Restricted: true, Allow: []string{"mock/listed-upstream"}})
	if got := e.chat(`{"model":"mock/not-added","messages":[]}`, rk.Key).StatusCode; got != 403 {
		t.Errorf("restricted key + unadded model: %d, want 403", got)
	}
}

// Capability routing: models that can read images / reason are tried first when a request needs it, and the
// Capacity Adapter supplies a vision model when nothing the client asked for can read images.
func TestCapabilityRoutingAndAdapter(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		hits = append(hits, req["model"].(string))
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	for _, m := range []string{"plain-chat", "gpt-4o", "o3-mini", "other-text"} { // gpt-4o reads images; o3-mini reasons
		e.store.AddCustomModel("mock", m, "")
	}
	last := func() string { mu.Lock(); defer mu.Unlock(); return hits[len(hits)-1] }
	text := `{"messages":[{"role":"user","content":"hi"}]`
	image := `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]`
	reason := `,"reasoning_effort":"high"`
	e.store.CreateCombo("mix", []string{"mock/plain-chat", "mock/gpt-4o", "mock/o3-mini"}, "", nil)

	call := func(model, body string) int { return e.chat(`{"model":"`+model+`",`+body[1:]+`}`, e.apiKey).StatusCode }
	if call("mix", text) != 200 || last() != "plain-chat" {
		t.Fatalf("a plain request keeps the combo order, hit %s", last())
	}
	if call("mix", image) != 200 || last() != "gpt-4o" {
		t.Fatalf("an image request goes to the vision member first, hit %s", last())
	}
	if call("mix", text+reason) != 200 || last() != "o3-mini" {
		t.Fatalf("a reasoning request goes to the reasoning member first, hit %s", last())
	}
	// images in earlier turns do not pin the request to a vision model
	history := `{"messages":[` + image[len(`{"messages":[`):len(image)-1] + `,{"role":"assistant","content":"a cat"},{"role":"user","content":"thanks"}]}`
	if call("mix", history) != 200 || last() != "plain-chat" {
		t.Fatalf("history media must not reorder, hit %s", last())
	}

	// explicit overrides beat the name: tell the router plain-chat can read images, and gpt-4o cannot
	e.store.SetModelCaps("mock", "plain-chat", map[string]any{"vision": true})
	e.store.SetModelCaps("mock", "gpt-4o", map[string]any{"vision": false})
	if call("mix", image) != 200 || last() != "plain-chat" {
		t.Fatalf("overrides win over detection, hit %s", last())
	}
	e.store.SetModelCaps("mock", "plain-chat", nil)
	e.store.SetModelCaps("mock", "gpt-4o", nil)

	// adapter: a text-only target with an image request. On by default, but an empty pool changes nothing
	if call("mock/other-text", image) != 200 || last() != "other-text" {
		t.Fatalf("empty pool: hit %s", last())
	}
	e.store.UpdateSettings(map[string]any{"capacityAdapter": map[string]any{"vision": map[string]any{"enabled": false, "models": []any{"mock/gpt-4o"}}}})
	if call("mock/other-text", image) != 200 || last() != "other-text" {
		t.Fatalf("adapter switched off: hit %s", last())
	}
	e.store.UpdateSettings(map[string]any{"capacityAdapter": map[string]any{"vision": map[string]any{"enabled": true, "models": []any{"mock/gpt-4o"}}}})
	if call("mock/other-text", image) != 200 || last() != "gpt-4o" {
		t.Fatalf("adapter on: a vision model must serve it, hit %s", last())
	}
	if call("mock/other-text", text) != 200 || last() != "other-text" {
		t.Fatalf("no image, no adapter: hit %s", last())
	}
	// a target that can already read images is left alone
	if call("mock/o3-mini", image) != 200 || last() != "o3-mini" {
		t.Fatalf("capable target must not be replaced: hit %s", last())
	}
	// a restricted key may only be sent to adapter models it is allowed to use
	rk, _ := e.store.CreateAPIKey("r", "m", "sk-adapter", db.Access{Restricted: true, Allow: []string{"mock/other-text"}})
	if got := e.chat(`{"model":"mock/other-text",`+image[1:]+`}`, rk.Key).StatusCode; got != 200 || last() != "other-text" {
		t.Fatalf("restricted key must not reach a model it is not allowed: status=%d hit=%s", got, last())
	}
}

func TestModelCapsAPI(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-4o"},{"id":"plain"}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	find := func(id string) map[string]any {
		_, out := call("GET", "/api/models/custom", "")
		for _, m := range out["models"].([]any) {
			if mm := m.(map[string]any); mm["id"] == id {
				return mm
			}
		}
		return nil
	}
	if st, _ := call("POST", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o"}`); st != 201 {
		t.Fatal("add")
	}
	m := find("gpt-4o")
	if eff := m["effective"].(map[string]any); eff["vision"] != true || m["caps"] != nil {
		t.Fatalf("detected from the name: %v", m)
	}
	if st, _ := call("PATCH", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o","caps":{"vision":false,"reasoning":true,"bogus":true}}`); st != 200 {
		t.Fatal("patch caps")
	}
	m = find("gpt-4o")
	if eff := m["effective"].(map[string]any); eff["vision"] != false || eff["reasoning"] != true || len(m["caps"].(map[string]any)) != 2 {
		t.Fatalf("overrides: %v", m)
	}
	call("PATCH", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o","caps":null}`)
	if m = find("gpt-4o"); m["caps"] != nil || m["effective"].(map[string]any)["vision"] != true {
		t.Fatalf("reset to auto: %v", m)
	}
	if st, _ := call("PATCH", "/api/models/custom", `{"providerAlias":"mock","id":"never-added","caps":{"vision":true}}`); st != 404 {
		t.Errorf("caps for an unadded model: %d", st)
	}
	// the upstream catalogue carries detected caps so the UI can badge chips before adding
	_, nm := call("GET", "/api/provider-nodes/"+e.node.ID+"/models", "")
	if uc := nm["upstreamCaps"].(map[string]any); uc["gpt-4o"].(map[string]any)["vision"] != true || uc["plain"].(map[string]any)["vision"] != false {
		t.Errorf("upstreamCaps = %v", nm["upstreamCaps"])
	}
	// capacity adapter settings are validated and normalised
	if st, _ := call("PATCH", "/api/settings", `{"capacityAdapter":{"bogus":{"enabled":true}}}`); st != 400 {
		t.Errorf("unknown capability: %d", st)
	}
	if st, _ := call("PATCH", "/api/settings", `{"capacityAdapter":{"vision":{"enabled":true,"roundRobin":true,"models":[" mock/gpt-4o ","mock/gpt-4o",""]}}}`); st != 200 {
		t.Fatalf("save adapter: %d", st)
	}
	_, s := call("GET", "/api/settings", "")
	v := s["capacityAdapter"].(map[string]any)["vision"].(map[string]any)
	if v["enabled"] != true || v["roundRobin"] != true || len(v["models"].([]any)) != 1 {
		t.Errorf("adapter = %v", v)
	}
}

// Context-window routing: a long request skips models whose window is too small (they stay as a last resort).
func TestContextWindowRouting(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		hits = append(hits, req["model"].(string))
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	for _, m := range []string{"small-model", "big-model", "unknown-model"} {
		e.store.AddCustomModel("mock", m, "")
	}
	e.store.SetModelCaps("mock", "small-model", map[string]any{"contextWindow": 8000})
	e.store.SetModelCaps("mock", "big-model", map[string]any{"contextWindow": 1000000})
	e.store.CreateCombo("ctx", []string{"mock/small-model", "mock/big-model"}, "", nil)
	e.store.CreateCombo("ctx2", []string{"mock/small-model", "mock/unknown-model"}, "", nil)
	last := func() string { mu.Lock(); defer mu.Unlock(); return hits[len(hits)-1] }
	long := `{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 20000) + `"}]}` // ~25k tokens
	short := `{"messages":[{"role":"user","content":"hi"}]}`

	if e.chat(`{"model":"ctx",`+short[1:], e.apiKey).StatusCode != 200 || last() != "small-model" {
		t.Fatalf("a short request keeps the combo order, hit %s", last())
	}
	if e.chat(`{"model":"ctx",`+long[1:], e.apiKey).StatusCode != 200 || last() != "big-model" {
		t.Fatalf("a long request skips the small window, hit %s", last())
	}
	// an unknown window is never held against a model
	if e.chat(`{"model":"ctx2",`+long[1:], e.apiKey).StatusCode != 200 || last() != "unknown-model" {
		t.Fatalf("unknown window beats a known too-small one, hit %s", last())
	}
	// a request that fits nowhere still goes out (the upstream decides) in the original order
	e.store.SetModelCaps("mock", "big-model", map[string]any{"contextWindow": 9000})
	if e.chat(`{"model":"ctx",`+long[1:], e.apiKey).StatusCode != 200 || last() != "small-model" {
		t.Fatalf("nothing fits: keep the order, hit %s", last())
	}
}

func TestContextWindowInAPI(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[{"id":"gpt-4o"}]}`) }, "chat")
	e.addKey("one", "k1")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) map[string]any {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	find := func() map[string]any {
		for _, m := range call("GET", "/api/models/custom", "")["models"].([]any) {
			if mm := m.(map[string]any); mm["id"] == "gpt-4o" {
				return mm
			}
		}
		return nil
	}
	call("POST", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o"}`)
	if m := find(); m["detected"].(map[string]any)["contextWindow"] != float64(128000) || m["effective"].(map[string]any)["contextWindow"] != float64(128000) {
		t.Fatalf("detected context: %v", m)
	}
	call("PATCH", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o","caps":{"contextWindow":32000}}`)
	if m := find(); m["effective"].(map[string]any)["contextWindow"] != float64(32000) || m["detected"].(map[string]any)["contextWindow"] != float64(128000) {
		t.Fatalf("override: %v", m)
	}
	call("PATCH", "/api/models/custom", `{"providerAlias":"mock","id":"gpt-4o","caps":{"contextWindow":5}}`) // out of range: ignored
	if m := find(); m["caps"] != nil || m["effective"].(map[string]any)["contextWindow"] != float64(128000) {
		t.Fatalf("an invalid window must not be stored: %v", m)
	}
	nm := call("GET", "/api/provider-nodes/"+e.node.ID+"/models", "")
	if uc := nm["upstreamCaps"].(map[string]any)["gpt-4o"].(map[string]any); uc["contextWindow"] != float64(128000) {
		t.Errorf("upstream caps carry the window: %v", uc)
	}
}

// Built-in providers (registry): addressed by alias prefix, keyed like nodes, with per-provider quirks.
func TestBuiltinProviders(t *testing.T) {
	var gotPath, gotAuth, gotReferer string
	var gotStream bool
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotReferer = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("HTTP-Referer")
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		gotStream, _ = req["stream"].(bool)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			// Cline-style: always streamed
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []string{
				`{"id":"c1","model":"m","created":1,"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}`,
				`{"id":"c1","choices":[{"delta":{"content":"lo","reasoning_content":"think"}}]}`,
				`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
				`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			} {
				io.WriteString(w, "data: "+ev+"\n\n")
			}
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"success":true,"data":{"id":"r","model":"m"}}`)
	}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// the registry lists the five providers and how far each port goes
	_, out := call("GET", "/api/builtin-providers", "")
	status := map[string]string{}
	for _, p := range out["providers"].([]any) {
		pm := p.(map[string]any)
		status[pm["id"].(string)] = pm["status"].(string)
	}
	if len(status) != 6 || status["kiro"] != "experimental" || status["cline"] != "ready" || status["qoder"] != "unsupported" || status["opencode"] != "experimental" {
		t.Fatalf("registry = %v", status)
	}
	// a custom node cannot take a reserved alias
	if st, _ := call("POST", "/api/provider-nodes", `{"name":"x","prefix":"cl","apiType":"chat","baseUrl":"http://x"}`); st != 409 {
		t.Errorf("custom node with a built-in alias: %d", st)
	}
	// unsupported providers cannot get keys, and a key is required where none is free
	if st, _ := call("POST", "/api/providers", `{"provider":"qoder","name":"k","apiKey":"pt-1"}`); st != 400 {
		t.Errorf("qoder key: %d", st)
	}
	if st, _ := call("POST", "/api/providers", `{"provider":"cline","name":"k","apiKey":""}`); st != 400 {
		t.Errorf("cline without a key: %d", st)
	}

	// point cline at the mock (its base URL is fixed in the registry) and use it for real
	defer provider.Override("cline", e.node.BaseURL())()
	st, _ := call("POST", "/api/providers", `{"provider":"cline","name":"main","apiKey":"cline-key"}`)
	if st != 201 {
		t.Fatalf("add cline key: %d", st)
	}
	if got := e.models(e.apiKey); !has(got, "cl/anthropic/claude-sonnet-4.6") {
		t.Errorf("a built-in provider with a key lists its catalogue: %v", got)
	}
	// non-streaming client: the upstream is still asked to stream, and the answer is assembled into one response
	resp := e.chat(`{"model":"cl/anthropic/claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	body, _ := io.ReadAll(resp.Body)
	var ans struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
		}
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		}
	}
	json.Unmarshal(body, &ans)
	if resp.StatusCode != 200 || len(ans.Choices) != 1 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	c := ans.Choices[0]
	if c.Message.Content != "Hello" || c.Message.Reasoning != "think" || c.FinishReason != "tool_calls" || len(c.Message.ToolCalls) != 1 ||
		c.Message.ToolCalls[0].ID != "call_1" || c.Message.ToolCalls[0].Function.Name != "f" || c.Message.ToolCalls[0].Function.Arguments != `{"a":1}` {
		t.Fatalf("assembled message = %+v", c)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer cline-key" || gotReferer != "https://cline.bot" || !gotStream {
		t.Errorf("upstream saw path=%s auth=%s referer=%s stream=%v", gotPath, gotAuth, gotReferer, gotStream)
	}
	if rows, _, _ := e.store.History(1, 0, "", "", ""); len(rows) != 1 || rows[0].PromptTokens != 5 || rows[0].CompletionTokens != 3 || rows[0].Provider != "cline" {
		t.Errorf("usage = %+v", rows)
	}
	// an Anthropic-format client works through the same provider (translated), still streamed upstream
	resp = e.post("/v1/messages", `{"model":"cl/anthropic/claude-sonnet-4.6","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"type":"message"`) || !strings.Contains(string(body), "Hello") {
		t.Errorf("messages client: %d %s", resp.StatusCode, body)
	}
	// the connection test proves the key with a one-token chat request
	_, conns := call("GET", "/api/providers", "")
	id := conns["connections"].([]any)[0].(map[string]any)["id"].(string)
	if _, r := call("POST", "/api/providers/"+id+"/test", ""); r["valid"] != true {
		t.Errorf("test connection: %v", r)
	}
	// unsupported providers are refused at request time with a clear message
	if st, out := call("POST", "/v1/chat/completions", `{"model":"qd/auto","messages":[]}`); st != 400 && st != 401 {
		t.Errorf("qoder request: %d %v", st, out)
	}
}

// Quota tracker: limits are measured from usage, validated, and (when enforced) make the gateway skip the target.
func TestQuotaTrackerAndEnforcement(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}, "chat")
	k1, k2 := e.addKey("first", "k1"), e.addKey("second", "k2")
	two, _ := e.store.CreateNode(db.NodeTypeOpenAI, "Two", "two", "chat", e.node.BaseURL())
	seedModels(e.store, "two")
	e.store.CreateConnection(two, "t1", "t-key")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	lastKey := func() string { mu.Lock(); defer mu.Unlock(); return keys[len(keys)-1] }

	for _, bad := range []string{
		`{"scope":"nope","target":"x","metric":"requests","window":"day","limit":5}`,
		`{"scope":"provider","target":"` + e.node.ID + `","metric":"cost","window":"day","limit":5}`,
		`{"scope":"provider","target":"` + e.node.ID + `","metric":"requests","window":"year","limit":5}`,
		`{"scope":"provider","target":"` + e.node.ID + `","metric":"requests","window":"day","limit":0}`,
		`{"scope":"provider","target":"missing","metric":"requests","window":"day","limit":5}`,
		`{"scope":"connection","target":"missing","metric":"requests","window":"day","limit":5}`,
	} {
		if st, _ := call("POST", "/api/quota", bad); st != 400 {
			t.Errorf("invalid limit accepted (%d): %s", st, bad)
		}
	}
	// a per-key limit: the first key may serve one request per day, enforced
	st, out := call("POST", "/api/quota", `{"scope":"connection","target":"`+k1.ID+`","metric":"requests","window":"day","limit":1,"enforce":true}`)
	if st != 201 {
		t.Fatalf("create: %d %v", st, out)
	}
	lim := out["limit"].(map[string]any)
	if lim["used"] != float64(0) || lim["targetName"] != "Mock/first-key" && lim["targetName"] != "Mock/first" || lim["mode"] != "calendar" || lim["name"] == "" {
		t.Fatalf("view = %v", lim)
	}
	body := `{"model":"mock/m","messages":[]}`
	if e.chat(body, e.apiKey).StatusCode != 200 || lastKey() != "k1" {
		t.Fatalf("first request uses the first key, got %s", lastKey())
	}
	if e.chat(body, e.apiKey).StatusCode != 200 || lastKey() != "k2" {
		t.Fatalf("the first key is over its quota: the second key must serve, got %s", lastKey())
	}
	_, list := call("GET", "/api/quota", "")
	l0 := list["limits"].([]any)[0].(map[string]any)
	if l0["used"] != float64(1) || l0["exhausted"] != true || l0["percent"] != float64(100) || l0["remaining"] != float64(0) {
		t.Errorf("tracked usage = %v", l0)
	}
	// every connection is tracked, with its own usage, whether or not it has a limit
	tracked := map[string]map[string]any{}
	for _, c := range list["connections"].([]any) {
		cm := c.(map[string]any)
		tracked[cm["name"].(string)] = cm
	}
	if f := tracked["first"]; f == nil || f["requests"].(map[string]any)["today"] != float64(1) || f["tokens"].(map[string]any)["month"] != float64(5) || f["providerName"] != "Mock" || f["lastUsed"] == "" {
		t.Errorf("tracked connection 'first' = %v", tracked["first"])
	}
	if sc := tracked["second"]; sc == nil || sc["requests"].(map[string]any)["today"] != float64(1) || tracked["t1"] == nil || tracked["t1"]["requests"].(map[string]any)["today"] != float64(0) {
		t.Errorf("tracked connections = %v", tracked)
	}
	if u := list["usage"].([]any); len(u) != 1 || u[0].(map[string]any)["name"] != "Mock" || u[0].(map[string]any)["tokens"].(map[string]any)["today"] != float64(10) {
		t.Errorf("provider usage = %v", list["usage"])
	}
	// without enforcement the key keeps being used (tracking only)
	id := lim["id"].(string)
	if st, _ := call("PUT", "/api/quota/"+id, `{"scope":"connection","target":"`+k1.ID+`","metric":"requests","window":"day","limit":1,"enforce":false}`); st != 200 {
		t.Fatal("update")
	}
	e.chat(body, e.apiKey)
	if lastKey() != "k1" {
		t.Errorf("a tracking-only limit must not block: %s", lastKey())
	}
	call("DELETE", "/api/quota/"+id, "")
	// a provider-wide enforced limit: every request skips it, a combo moves on to the next provider
	call("POST", "/api/quota", `{"scope":"provider","target":"`+e.node.ID+`","metric":"requests","window":"day","limit":2,"enforce":true,"name":"Mock daily"}`)
	if got := e.chat(body, e.apiKey).StatusCode; got != 429 {
		t.Errorf("provider over quota with no alternative: %d", got)
	}
	e.store.CreateCombo("qc", []string{"mock/m", "two/m"}, "", nil)
	if e.chat(`{"model":"qc","messages":[]}`, e.apiKey).StatusCode != 200 || lastKey() != "t-key" {
		t.Errorf("the combo must move to the provider with quota left, got %s", lastKey())
	}
	_ = k2
	if st, _ := call("DELETE", "/api/quota/nope", ""); st != 404 {
		t.Errorf("delete unknown: %d", st)
	}
}

// Built-in provider settings: models must be enabled, headers can be customised, and keys can be rotated.
func TestBuiltinModelsHeadersAndRoundRobin(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	var ua, ide string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		ua, ide = r.Header.Get("User-Agent"), r.Header.Get("X-IDE-Type")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}, "chat")
	defer provider.Override("codebuddy-intl", e.node.BaseURL())()
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	view := func() map[string]any {
		_, out := call("GET", "/api/builtin-providers", "")
		for _, p := range out["providers"].([]any) {
			if pm := p.(map[string]any); pm["id"] == "codebuddy-intl" {
				return pm
			}
		}
		return nil
	}
	enabledCount := func() int {
		n := 0
		for _, m := range view()["models"].([]any) {
			if m.(map[string]any)["enabled"] == true {
				n++
			}
		}
		return n
	}
	body := `{"model":"cbai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`
	if enabledCount() != 0 {
		t.Fatal("nothing is enabled before a key exists")
	}
	if got := e.chat(body, e.apiKey).StatusCode; got != 404 {
		t.Fatalf("a model that is not enabled must be refused: %d", got)
	}
	// the first key switches the catalogue on
	call("POST", "/api/providers", `{"provider":"codebuddy-intl","name":"one","apiKey":"key-one"}`)
	call("POST", "/api/providers", `{"provider":"codebuddy-intl","name":"two","apiKey":"key-two"}`)
	if n := enabledCount(); n != 15 {
		t.Fatalf("catalogue enabled by the first key: %d", n)
	}
	if got := e.chat(body, e.apiKey).StatusCode; got != 200 {
		t.Fatalf("enabled model: %d", got)
	}
	// a model added by hand (an id outside the catalogue) is listed, usable, and goes away when removed
	if st, _ := call("POST", "/api/models/custom", `{"providerAlias":"cbai","id":"my-private-model"}`); st != 201 {
		t.Fatalf("add a custom model to a built-in provider: %d", st)
	}
	var found map[string]any
	for _, m := range view()["models"].([]any) {
		if mm := m.(map[string]any); mm["id"] == "my-private-model" {
			found = mm
		}
	}
	if found == nil || found["custom"] != true || found["enabled"] != true {
		t.Fatalf("custom model missing from the provider view: %v", found)
	}
	if got := e.chat(`{"model":"cbai/my-private-model","messages":[{"role":"user","content":"hi"}]}`, e.apiKey).StatusCode; got != 200 {
		t.Errorf("custom model on a built-in provider: %d", got)
	}
	call("DELETE", "/api/models/custom?providerAlias=cbai&id=my-private-model", "")
	for _, m := range view()["models"].([]any) {
		if m.(map[string]any)["id"] == "my-private-model" {
			t.Error("a removed custom model must disappear")
		}
	}
	// disable one model and then everything, enable all again
	call("DELETE", "/api/models/custom?providerAlias=cbai&id=glm-5.2", "")
	if got := e.chat(body, e.apiKey).StatusCode; got != 404 || enabledCount() != 14 {
		t.Errorf("disabled model: %d (enabled %d)", got, enabledCount())
	}
	call("POST", "/api/builtin-providers/codebuddy-intl/models", `{"action":"disable-all"}`)
	if enabledCount() != 0 {
		t.Error("disable-all")
	}
	call("POST", "/api/builtin-providers/codebuddy-intl/models", `{"action":"enable-all"}`)
	if enabledCount() != 15 {
		t.Error("enable-all")
	}
	if st, _ := call("POST", "/api/builtin-providers/codebuddy-intl/models", `{"action":"x"}`); st != 400 {
		t.Errorf("bad action: %d", st)
	}

	// headers: user overrides are sent, forbidden ones are refused, reset restores the defaults
	if st, _ := call("PUT", "/api/builtin-providers/codebuddy-intl/headers", `{"headers":{"Authorization":"Bearer x"}}`); st != 400 {
		t.Errorf("authorization override: %d", st)
	}
	st, v := call("PUT", "/api/builtin-providers/codebuddy-intl/headers", `{"headers":{"User-Agent":"my-client/1.0","X-IDE-Type":"IDE"}}`)
	if st != 200 || v["headers"].(map[string]any)["customized"] != true {
		t.Fatalf("set headers: %d %v", st, v)
	}
	e.chat(body, e.apiKey)
	mu.Lock()
	gotUA, gotIDE := ua, ide
	mu.Unlock()
	if gotUA != "my-client/1.0" || gotIDE != "IDE" {
		t.Errorf("upstream saw UA=%q X-IDE-Type=%q", gotUA, gotIDE)
	}
	if _, v = call("DELETE", "/api/builtin-providers/codebuddy-intl/headers", ""); v["headers"].(map[string]any)["customized"] != false {
		t.Errorf("reset: %v", v["headers"])
	}
	// the XRouter defaults are part of the provider: visible in the dashboard and sent when nothing is overridden
	hv := view()["headers"].(map[string]any)
	if def := hv["defaults"].(map[string]any); def["User-Agent"] != "IDE/2.108.1 CodeBuddy/2.108.1" || def["X-Product"] != "SaaS" || def["x-codebuddy-request"] != "1" || len(hv["effective"].(map[string]any)) != 6 {
		t.Errorf("default headers = %v", hv)
	}
	e.chat(body, e.apiKey)
	mu.Lock()
	gotUA = ua
	mu.Unlock()
	if gotUA != "IDE/2.108.1 CodeBuddy/2.108.1" {
		t.Errorf("default User-Agent not sent: %q", gotUA)
	}

	// round-robin over the keys, one request per key (sticky 1)
	if st, _ := call("PUT", "/api/provider-config/codebuddy-intl", `{"roundRobin":true,"sticky":1}`); st != 200 {
		t.Fatal("config")
	}
	mu.Lock()
	keys = nil
	mu.Unlock()
	for range 4 {
		e.chat(body, e.apiKey)
	}
	mu.Lock()
	got := strings.Join(keys, ",")
	mu.Unlock()
	if got != "key-one,key-two,key-one,key-two" && got != "key-two,key-one,key-two,key-one" {
		t.Errorf("round-robin keys = %s", got)
	}
	if st, _ := call("PUT", "/api/provider-config/codebuddy-intl", `{"roundRobin":true,"sticky":0}`); st != 400 {
		t.Errorf("sticky 0: %d", st)
	}
	if st, _ := call("PUT", "/api/provider-config/nope", `{"roundRobin":true,"sticky":1}`); st != 404 {
		t.Errorf("unknown provider: %d", st)
	}
}

// Browser sign-in differs per provider: Cline returns a code, CodeBuddy and Freebuff are polled.
func TestOAuthFlowsPerProvider(t *testing.T) {
	var mu sync.Mutex
	var chatAuth, chatReqID, freebuffFP string
	var codebuddyPolls, freebuffPolls int
	refreshed := false
	jwt := func(s string) string { return "eyJhbGciOi." + s + ".sig" }

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/auth/refresh": // cline
			refreshed = true
			io.WriteString(w, `{"success":true,"data":{"accessToken":"`+jwt("renewed")+`","refreshToken":"rt-2","expiresAt":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}}`)
		case r.URL.Path == "/v2/plugin/auth/state": // codebuddy
			io.WriteString(w, `{"code":0,"data":{"state":"st-123","authUrl":"https://login.example/cb?state=st-123"}}`)
		case r.URL.Path == "/v2/plugin/auth/token":
			codebuddyPolls++
			if codebuddyPolls < 2 {
				io.WriteString(w, `{"code":11217,"msg":"pending"}`)
				return
			}
			io.WriteString(w, `{"code":0,"data":{"accessToken":"cb-access","refreshToken":"cb-refresh","expiresIn":3600}}`)
		case r.URL.Path == "/api/auth/cli/code": // freebuff
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			freebuffFP, _ = in["fingerprintId"].(string)
			io.WriteString(w, `{"fingerprintId":"`+freebuffFP+`","fingerprintHash":"h1","loginUrl":"https://freebuff.example/login?auth_code=abc","expiresAt":`+strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)+`}`)
		case r.URL.Path == "/api/auth/cli/status":
			freebuffPolls++
			if freebuffPolls < 2 {
				w.WriteHeader(401)
				io.WriteString(w, `{"error":"Authentication failed"}`)
				return
			}
			io.WriteString(w, `{"user":{"id":"u1","email":"me@fb.test","name":"Me","authToken":"fb-token"}}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"): // chat for both providers
			chatAuth, chatReqID = r.Header.Get("Authorization"), r.Header.Get("X-Request-ID")
			io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer mock.Close()
	for _, id := range []string{"cline", "codebuddy-intl", "freebuff"} {
		defer provider.OverrideOAuth(id, mock.URL)()
		defer provider.Override(id, mock.URL+"/v1")()
	}
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	connections := func(provider string) []map[string]any {
		_, out := call("GET", "/api/providers", "")
		var res []map[string]any
		for _, c := range out["connections"].([]any) {
			if cm := c.(map[string]any); cm["provider"] == provider {
				res = append(res, cm)
			}
		}
		return res
	}

	// the registry tells the UI which providers sign in, and how
	_, reg := call("GET", "/api/builtin-providers", "")
	kinds := map[string]string{}
	for _, p := range reg["providers"].([]any) {
		pm := p.(map[string]any)
		kinds[pm["id"].(string)], _ = pm["oauthKind"].(string)
	}
	if kinds["cline"] != "code" || kinds["codebuddy-intl"] != "poll" || kinds["freebuff"] != "poll" || kinds["opencode"] != "" || kinds["qoder"] != "" {
		t.Fatalf("oauth kinds = %v", kinds)
	}
	if st, _ := call("POST", "/api/oauth/opencode/start", `{}`); st != 400 {
		t.Errorf("a provider without sign-in: %d", st)
	}
	if st, _ := call("POST", "/api/oauth/qoder/start", `{}`); st != 400 {
		t.Errorf("qoder sign-in: %d", st)
	}

	// ---- Cline: login URL with the redirect, then the code carries the tokens ----
	st, start := call("POST", "/api/oauth/cline/start", `{"redirectUri":"http://localhost:20128/dashboard/oauth/callback"}`)
	if st != 200 || start["kind"] != "code" || !strings.Contains(start["authUrl"].(string), "/api/v1/auth/authorize?") || !strings.Contains(start["authUrl"].(string), "client_type=extension") {
		t.Fatalf("cline start: %d %v", st, start)
	}
	if st, _ := call("POST", "/api/oauth/cline/start", `{}`); st != 400 {
		t.Errorf("a code flow needs a redirect URI: %d", st)
	}
	expired := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	bundle, _ := json.Marshal(map[string]any{"accessToken": jwt("first"), "refreshToken": "rt-1", "email": "dev@cline.test", "firstName": "Dev", "lastName": "One", "expiresAt": expired})
	code := base64.StdEncoding.EncodeToString(bundle)
	st, ex := call("POST", "/api/oauth/cline/exchange", `{"code":"`+code+`","redirectUri":"http://localhost:20128/dashboard/oauth/callback"}`)
	if st != 200 || ex["status"] != "ok" {
		t.Fatalf("cline exchange: %d %v", st, ex)
	}
	conn := ex["connection"].(map[string]any)
	if conn["authType"] != "oauth" || conn["name"] != "dev@cline.test" || conn["accessToken"] != nil || conn["refreshToken"] != nil {
		t.Fatalf("connection must be OAuth and never expose tokens: %v", conn)
	}
	if st, _ := call("POST", "/api/oauth/cline/exchange", `{"code":""}`); st != 400 {
		t.Errorf("empty code: %d", st)
	}
	// the token was already expired: using it refreshes it first, and the key goes out with the workos: prefix
	if got := e.chat(`{"model":"cl/anthropic/claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`, e.apiKey).StatusCode; got != 200 {
		t.Fatalf("chat through a signed-in cline: %d", got)
	}
	mu.Lock()
	gotAuth, gotRefresh := chatAuth, refreshed
	mu.Unlock()
	if !gotRefresh || gotAuth != "Bearer workos:"+jwt("renewed") {
		t.Errorf("refresh=%v auth=%q", gotRefresh, gotAuth)
	}
	// signing in again with the same account updates the connection instead of adding another
	call("POST", "/api/oauth/cline/exchange", `{"code":"`+code+`","redirectUri":"x"}`)
	if n := len(connections("cline")); n != 1 {
		t.Errorf("same account twice: %d connections", n)
	}
	// the whole catalogue came on with the first sign-in
	_, reg = call("GET", "/api/builtin-providers", "")
	for _, p := range reg["providers"].([]any) {
		if pm := p.(map[string]any); pm["id"] == "cline" {
			n := 0
			for _, m := range pm["models"].([]any) {
				if m.(map[string]any)["enabled"] == true {
					n++
				}
			}
			if n != len(pm["models"].([]any)) {
				t.Errorf("catalogue enabled by sign-in: %d of %d", n, len(pm["models"].([]any)))
			}
		}
	}

	// ---- CodeBuddy: state + login URL, polled until the token arrives ----
	st, start = call("POST", "/api/oauth/codebuddy-intl/start", `{}`)
	if st != 200 || start["kind"] != "poll" || start["state"] != "st-123" || !strings.HasPrefix(start["authUrl"].(string), "https://login.example/") {
		t.Fatalf("codebuddy start: %d %v", st, start)
	}
	if _, p1 := call("POST", "/api/oauth/codebuddy-intl/poll", `{"state":"st-123"}`); p1["status"] != "pending" {
		t.Fatalf("first poll must be pending: %v", p1)
	}
	st, p2 := call("POST", "/api/oauth/codebuddy-intl/poll", `{"state":"st-123"}`)
	if st != 200 || p2["status"] != "ok" || p2["connection"].(map[string]any)["authType"] != "oauth" {
		t.Fatalf("second poll: %d %v", st, p2)
	}
	if got := e.chat(`{"model":"cbai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`, e.apiKey).StatusCode; got != 200 {
		t.Fatalf("chat through codebuddy: %d", got)
	}
	mu.Lock()
	gotAuth, gotID := chatAuth, chatReqID
	mu.Unlock()
	if gotAuth != "Bearer cb-access" || gotID == "" {
		t.Errorf("codebuddy upstream auth=%q request-id=%q", gotAuth, gotID)
	}
	if st, _ := call("POST", "/api/oauth/codebuddy-intl/exchange", `{"code":"x"}`); st != 400 {
		t.Errorf("exchange on a poll provider: %d", st)
	}

	// ---- Freebuff: the install's fingerprint goes to the login endpoint; 401 means "not yet" ----
	st, start = call("POST", "/api/oauth/freebuff/start", `{}`)
	if st != 200 || start["kind"] != "poll" || !strings.Contains(start["authUrl"].(string), "auth_code=abc") {
		t.Fatalf("freebuff start: %d %v", st, start)
	}
	mu.Lock()
	fp := freebuffFP
	mu.Unlock()
	if len(fp) != 32 {
		t.Errorf("fingerprint = %q", fp)
	}
	state, _ := json.Marshal(start["state"])
	if _, p := call("POST", "/api/oauth/freebuff/poll", `{"state":`+string(state)+`}`); p["status"] != "pending" {
		t.Fatalf("freebuff first poll: %v", p)
	}
	if st, p := call("POST", "/api/oauth/freebuff/poll", `{"state":`+string(state)+`}`); st != 200 || p["status"] != "ok" || p["connection"].(map[string]any)["name"] != "me@fb.test" {
		t.Fatalf("freebuff second poll: %d %v", st, p)
	}
	// the fingerprint is stable across sign-ins
	call("POST", "/api/oauth/freebuff/start", `{}`)
	mu.Lock()
	again := freebuffFP
	mu.Unlock()
	if again != fp {
		t.Errorf("fingerprint changed: %s vs %s", fp, again)
	}
}
