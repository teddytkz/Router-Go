package api_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// frame builds one AWS event stream frame (string headers, JSON payload, both CRCs).
func frame(typ string, payload any) []byte {
	var h bytes.Buffer
	for _, kv := range [][2]string{{":message-type", "event"}, {":event-type", typ}} {
		h.WriteByte(byte(len(kv[0])))
		h.WriteString(kv[0])
		h.WriteByte(7)
		binary.Write(&h, binary.BigEndian, uint16(len(kv[1])))
		h.WriteString(kv[1])
	}
	p, _ := json.Marshal(payload)
	b := make([]byte, 12, 16+h.Len()+len(p))
	binary.BigEndian.PutUint32(b, uint32(16+h.Len()+len(p)))
	binary.BigEndian.PutUint32(b[4:], uint32(h.Len()))
	binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
	b = append(append(b, h.Bytes()...), p...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

// fakeKiro plays the Kiro runtime (generateAssistantResponse) and its token service (refreshToken).
type fakeKiro struct {
	mu        sync.Mutex
	valid     map[string]bool // access tokens the runtime accepts
	gens      []gen           // every generate call
	refreshes int
}

type gen struct {
	token, arn string
	body       map[string]any
}

func (f *fakeKiro) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	switch r.URL.Path {
	case "/refreshToken":
		f.refreshes++
		if body["refreshToken"] != "rt-1" {
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"Invalid refresh token"}`)
			return
		}
		f.valid["fresh-token"] = true
		io.WriteString(w, `{"accessToken":"fresh-token","refreshToken":"rt-2","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/NEW"}`)
	case "/generateAssistantResponse":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !f.valid[token] {
			w.WriteHeader(403)
			io.WriteString(w, `{"message":"The bearer token included in the request is invalid."}`)
			return
		}
		f.gens = append(f.gens, gen{token, r.Header.Get("x-amzn-codewhisperer-profile-arn"), body})
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		cur := body["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
		if ctx, _ := cur["userInputMessageContext"].(map[string]any); ctx["tools"] != nil {
			w.Write(frame("toolUseEvent", map[string]any{"name": "get_weather", "toolUseId": "tu1", "input": `{"city":"Oslo"}`}))
			w.Write(frame("messageStopEvent", map[string]any{"stopReason": "tool_use"}))
			return
		}
		w.Write(frame("assistantResponseEvent", map[string]any{"content": "Hel"}))
		w.Write(frame("assistantResponseEvent", map[string]any{"content": "lo"}))
		w.Write(frame("metricsEvent", map[string]any{"inputTokens": 7, "outputTokens": 3}))
		w.Write(frame("messageStopEvent", map[string]any{"stopReason": "end_turn"}))
	default:
		w.WriteHeader(404)
	}
}

func kiroEnv(t *testing.T) (*env, *fakeKiro) {
	t.Helper()
	f := &fakeKiro{valid: map[string]bool{"tok-1": true}}
	mock := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(mock.Close)
	t.Cleanup(provider.Override("kiro", mock.URL))
	t.Cleanup(provider.OverrideOAuth("kiro", mock.URL))
	e := newEnv(t, func(http.ResponseWriter, *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	for _, m := range []string{"claude-sonnet-4.5", "claude-sonnet-4.5-thinking", "claude-haiku-4.5"} {
		e.store.AddCustomModel("kr", m, "")
	}
	return e, f
}

func (e *env) kiroConn(t *testing.T, tk provider.Tokens) *db.Connection {
	t.Helper()
	node, err := e.store.Node("kiro")
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.store.SaveOAuthConnection(node, tk)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var social = map[string]any{"authMethod": "google", "profileArn": "arn:own"}

func fresh(token string) provider.Tokens {
	return provider.Tokens{AccessToken: token, RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Hour), Email: "me@kiro.test", Extra: social}
}

func TestKiroChatThroughTheGateway(t *testing.T) {
	e, f := kiroEnv(t)
	e.kiroConn(t, fresh("tok-1"))

	// streaming OpenAI client
	resp := e.chat(`{"model":"kr/claude-sonnet-4.5","stream":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`, e.apiKey)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	var text string
	var usage map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var c struct {
				Choices []struct{ Delta struct{ Content string } }
				Usage   map[string]any
			}
			json.Unmarshal([]byte(data), &c)
			text += c.Choices[0].Delta.Content
			if c.Usage != nil {
				usage = c.Usage
			}
		}
	}
	if text != "Hello" || usage["prompt_tokens"] != float64(7) || !strings.HasSuffix(strings.TrimSpace(string(body)), "data: [DONE]") {
		t.Fatalf("stream body = %s", body)
	}
	g := f.gens[0]
	cur := g.body["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if g.token != "tok-1" || g.arn != "arn:own" || g.body["profileArn"] != "arn:own" || cur["modelId"] != "claude-sonnet-4.5" ||
		cur["content"] != "<instructions>\nbe brief\n</instructions>\n\nhi" {
		t.Errorf("upstream saw %+v", g)
	}

	// non-streaming OpenAI client, thinking variant: same upstream model, thinking prompt
	resp = e.chat(`{"model":"kr/claude-sonnet-4.5-thinking","messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	var ans struct {
		Choices []struct {
			Message      struct{ Content string }
			FinishReason string `json:"finish_reason"`
		}
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		}
	}
	json.NewDecoder(resp.Body).Decode(&ans)
	if resp.StatusCode != 200 || len(ans.Choices) != 1 || ans.Choices[0].Message.Content != "Hello" || ans.Choices[0].FinishReason != "stop" || ans.Usage.Prompt != 7 || ans.Usage.Completion != 3 {
		t.Fatalf("non-stream: %d %+v", resp.StatusCode, ans)
	}
	cur = f.gens[1].body["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if cur["modelId"] != "claude-sonnet-4.5" || !strings.HasPrefix(cur["content"].(string), "<thinking_mode>enabled</thinking_mode>") {
		t.Errorf("thinking request = %v", cur)
	}

	// tools: the definition goes upstream, the call comes back as an OpenAI tool call
	resp = e.chat(`{"model":"kr/claude-sonnet-4.5","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`, e.apiKey)
	var tc struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
	}
	json.NewDecoder(resp.Body).Decode(&tc)
	if len(tc.Choices) != 1 || tc.Choices[0].FinishReason != "tool_calls" || len(tc.Choices[0].Message.ToolCalls) != 1 ||
		tc.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" || tc.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Oslo"}` {
		t.Fatalf("tool call = %+v", tc)
	}

	// an Anthropic client goes through the translation hub
	resp = e.post("/v1/messages", `{"model":"kr/claude-sonnet-4.5","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"type":"message"`) || !strings.Contains(string(b), "Hello") {
		t.Errorf("messages client: %d %s", resp.StatusCode, b)
	}

	rows, _, _ := e.store.History(10, 0, "", "", "")
	if len(rows) != 4 || rows[3].Provider != "kiro" || rows[3].Status != "ok" || rows[3].PromptTokens != 7 || rows[3].CompletionTokens != 3 {
		t.Errorf("usage rows = %+v", rows)
	}
}

func TestKiroRefreshesExpiredTokenFirst(t *testing.T) {
	e, f := kiroEnv(t)
	tk := fresh("tok-old")
	tk.ExpiresAt = time.Now().Add(-time.Minute)
	tk.Extra = map[string]any{"authMethod": "google"} // no profile ARN yet: the refresh learns it
	c := e.kiroConn(t, tk)

	resp := e.chat(`{"model":"kr/claude-haiku-4.5","messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	if f.refreshes != 1 || len(f.gens) != 1 || f.gens[0].token != "fresh-token" {
		t.Fatalf("refreshes=%d gens=%+v", f.refreshes, f.gens)
	}
	// the profile ARN the refresh returned is already on this very request, and stored for the next ones
	const arn = "arn:aws:codewhisperer:us-east-1:1:profile/NEW"
	if f.gens[0].arn != arn {
		t.Errorf("profile ARN on the first request = %q", f.gens[0].arn)
	}
	got, _ := e.store.Connection(c.ID)
	psd := got.Data["providerSpecificData"].(map[string]any)
	if got.APIKey() != "fresh-token" || got.RefreshToken() != "rt-2" || psd["profileArn"] != arn || psd["authMethod"] != "google" || got.TokenExpiry().Before(time.Now().Add(30*time.Minute)) {
		t.Errorf("stored connection = %+v", got.Data)
	}
	e.chat(`{"model":"kr/claude-haiku-4.5","messages":[{"role":"user","content":"again"}]}`, e.apiKey)
	if f.refreshes != 1 {
		t.Errorf("a fresh token was refreshed again (%d)", f.refreshes)
	}
}

func TestKiroKeyTest(t *testing.T) {
	e, f := kiroEnv(t)
	good := e.kiroConn(t, fresh("tok-1"))
	st, out := e.json("POST", "/api/providers/"+good.ID+"/test", `{}`)
	if st != 200 || out["valid"] != true || out["method"] != "chat" {
		t.Fatalf("good key: %d %v", st, out)
	}
	if len(f.gens) != 1 {
		t.Errorf("the test must make a real request: %d", len(f.gens))
	}
	// the stored sign-in's psd is used: the profile ARN rides on the test call too
	if f.gens[0].arn != "arn:own" {
		t.Errorf("test call profile ARN = %q", f.gens[0].arn)
	}

	bad := fresh("tok-nope")
	bad.Email = "other@kiro.test"
	c := e.kiroConn(t, bad)
	st, out = e.json("POST", "/api/providers/"+c.ID+"/test", `{}`)
	if st != 200 || out["valid"] != false || !strings.Contains(out["error"].(string), "403") {
		t.Errorf("rejected token: %d %v", st, out)
	}
}

func TestKiroImportAndRegistry(t *testing.T) {
	e, f := kiroEnv(t)

	_, reg := e.json("GET", "/api/builtin-providers", "")
	var view map[string]any
	for _, p := range reg["providers"].([]any) {
		if p.(map[string]any)["id"] == "kiro" {
			view = p.(map[string]any)
		}
	}
	if view == nil || view["oauthKind"] != "poll" || view["status"] != "experimental" || view["alias"] != "kr" || !strings.Contains(view["note"].(string), "RISK") ||
		!strings.Contains(view["authHint"].(string), "refresh token") {
		t.Fatalf("registry entry = %v", view)
	}

	// a pasted refresh token becomes an OAuth connection, validated by refreshing it once
	st, out := e.json("POST", "/api/providers", `{"provider":"kiro","name":"personal","apiKey":"rt-1"}`)
	conn, _ := out["connection"].(map[string]any)
	if st != 200 || conn["authType"] != "oauth" || conn["accessToken"] != nil || conn["refreshToken"] != nil || f.refreshes != 1 {
		t.Fatalf("import: %d %v refreshes=%d", st, out, f.refreshes)
	}
	cs, _ := e.store.Connections("kiro", false)
	if len(cs) != 1 || cs[0].APIKey() != "fresh-token" || cs[0].RefreshToken() != "rt-2" || cs[0].Data["providerSpecificData"].(map[string]any)["authMethod"] != "imported" {
		t.Fatalf("stored = %+v", cs)
	}
	resp := e.chat(`{"model":"kr/claude-haiku-4.5","messages":[{"role":"user","content":"hi"}]}`, e.apiKey)
	if resp.StatusCode != 200 {
		t.Errorf("chat on the imported account: %d", resp.StatusCode)
	}

	// a bad token is refused and nothing is stored
	if st, _ = e.json("POST", "/api/providers", `{"provider":"kiro","name":"bad","apiKey":"nope"}`); st != 400 {
		t.Errorf("bad token: %d", st)
	}
	if cs, _ = e.store.Connections("kiro", false); len(cs) != 1 {
		t.Errorf("connections = %d", len(cs))
	}

	// the OIDC client secret of a Builder ID / IDC account never reaches the dashboard
	tk := fresh("x")
	tk.Email = "idc@kiro.test"
	tk.Extra = map[string]any{"authMethod": "idc", "clientId": "cid", "clientSecret": "very-secret"}
	e.kiroConn(t, tk)
	_, raw := e.call("GET", "/api/providers", "")
	if strings.Contains(raw, "very-secret") || !strings.Contains(raw, `"clientId":"cid"`) {
		t.Errorf("connections listing leaks or lost client data: %s", raw)
	}
}
