package opencodefree

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"router-go/internal/consolelog"
	"router-go/internal/provider"
)

func TestLaneOf(t *testing.T) {
	cases := map[string]string{
		"big-pickle":               "chat",
		"crouton-alpha-free":       "responses",
		"crouton-alpha-free(high)": "responses",
		"union-alpha":              "messages",
	}
	for model, want := range cases {
		if got := laneOf(model); got != want {
			t.Errorf("laneOf(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestEndpointsDefaultsToChatLane(t *testing.T) {
	got := endpoints("", nil)
	if len(got) != 1 || got[0] != defaultBase+"/openai/v1/chat/completions" {
		t.Fatalf("endpoints = %v", got)
	}
	got = endpoints("http://127.0.0.1:9000/inference", nil)
	if got[0] != "http://127.0.0.1:9000/inference/openai/v1/chat/completions" {
		t.Fatalf("overridden base = %v", got)
	}
}

func TestLaneURLKeepsOverridePrefix(t *testing.T) {
	base := "http://127.0.0.1:9000/inference"
	cases := map[string]string{
		"chat":      base + "/openai/v1/chat/completions",
		"responses": base + "/openai/v1/responses",
		"messages":  base + "/anthropic/v1/messages",
	}
	for lane, want := range cases {
		if got := laneURL(base+"/openai/v1/chat/completions", lane); got != want {
			t.Errorf("laneURL(%s) = %q, want %q", lane, got, want)
		}
	}
}

func TestSessionShape(t *testing.T) {
	ses := newSessionID(time.Now())
	if !strings.HasPrefix(ses, "ses_") || len(ses) != len("ses_")+12+14 {
		t.Fatalf("session = %q", ses)
	}
	for _, c := range ses[4:16] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("prefix is not 12 hex: %q", ses)
		}
	}
	if stableSession() != stableSession() {
		t.Fatal("stableSession is not stable")
	}
}

func TestRequestChatLaneAddsFingerprint(t *testing.T) {
	body := map[string]any{"model": "big-pickle", "messages": []any{map[string]any{"role": "user", "content": "what's in /etc?"}}}
	req := mustRequest(t, "big-pickle", body)

	if !strings.HasSuffix(req.URL.Path, "/openai/v1/chat/completions") {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Header.Get("User-Agent") != ua || req.Header.Get("Authorization") != "Bearer public" {
		t.Fatalf("headers = %v", req.Header)
	}
	if req.Header.Get("x-opencode-session") != req.Header.Get("x-opencode-session-id") {
		t.Fatal("session headers differ")
	}
	sent := sentBody(t, req)
	if sent["stream"] != true {
		t.Fatal("chat lane must stream")
	}
	tools, _ := sent["tools"].([]any)
	for _, want := range []string{"read", "shell"} {
		if !hasTool(tools, want) {
			t.Fatalf("fingerprint tool %q missing: %v", want, tools)
		}
	}
	msgs, _ := sent["messages"].([]any)
	if len(msgs) == 0 || !textCarriesEnv(asMap(msgs[0])["content"]) {
		t.Fatalf("no env marker in the leading system message: %v", msgs)
	}
}

func TestRequestMessagesLaneShape(t *testing.T) {
	body := map[string]any{"model": "union-alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}}}
	req := mustRequest(t, "union-alpha", body)

	if !strings.HasSuffix(req.URL.Path, "/anthropic/v1/messages") {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Fatal("messages lane needs anthropic-version")
	}
	sent := sentBody(t, req)
	if _, ok := sent["messages"]; !ok {
		t.Fatalf("no messages: %v", sent)
	}
	if _, ok := sent["max_tokens"]; !ok {
		t.Fatalf("messages lane needs max_tokens: %v", sent)
	}
	if !textCarriesEnv(sent["system"]) {
		t.Fatalf("marker not in system: %v", sent["system"])
	}
	for _, want := range []string{"read", "shell"} {
		if !hasToolFlat(sent, want) {
			t.Fatalf("fingerprint tool %q missing: %v", want, sent["tools"])
		}
	}
}

func TestRequestResponsesLaneFixes(t *testing.T) {
	body := map[string]any{"model": "crouton-alpha-free", "max_tokens": 128,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "user", "content": "hi"}}}
	req := mustRequest(t, "crouton-alpha-free", body)

	if !strings.HasSuffix(req.URL.Path, "/openai/v1/responses") {
		t.Fatalf("url = %s", req.URL)
	}
	sent := sentBody(t, req)
	if sent["store"] != false || sent["stream"] != true {
		t.Fatalf("store/stream = %v/%v", sent["store"], sent["stream"])
	}
	if _, ok := sent["stream_options"]; ok {
		t.Fatal("stream_options is not a Responses field")
	}
	if _, ok := sent["max_tokens"]; ok {
		t.Fatal("max_tokens must move to max_output_tokens")
	}
	if sent["max_output_tokens"] != json.Number("128") {
		t.Fatalf("max_output_tokens = %v", sent["max_output_tokens"])
	}
	if sent["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v", sent["tool_choice"])
	}
	if !strings.Contains(str(sent["instructions"]), envTag) {
		t.Fatalf("marker not in instructions: %v", sent["instructions"])
	}
	for _, want := range []string{"read", "shell"} {
		if !hasToolFlat(sent, want) {
			t.Fatalf("fingerprint tool %q missing: %v", want, sent["tools"])
		}
	}
}

func TestTerminalToolIsAliasedAndRestored(t *testing.T) {
	body := map[string]any{"model": "big-pickle", "messages": []any{map[string]any{"role": "user", "content": "ls"}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "run_terminal_cmd", "parameters": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}}}}}
	req := mustRequest(t, "big-pickle", body)
	sent := sentBody(t, req)

	if strings.Contains(mustJSON(t, sent["tools"]), "terminal") {
		t.Fatalf("gated tool name reached upstream: %v", sent["tools"])
	}
	if !hasTool(sent["tools"].([]any), "run_command") {
		t.Fatalf("alias missing: %v", sent["tools"])
	}

	renames, _ := req.Context().Value(renamesKey{}).(map[string]string)
	if renames["run_command"] != "run_terminal_cmd" {
		t.Fatalf("rename map = %v", renames)
	}
	event := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": "call_1", "function": map[string]any{"name": "run_command", "arguments": "{}"}}}}}}}
	restoreToolNames(event, renames)
	if !strings.Contains(mustJSON(t, event), `"run_terminal_cmd"`) {
		t.Fatalf("caller tool name not restored: %v", event)
	}
}

func TestCallerToolsKeptAndSchemasInPlace(t *testing.T) {
	callerRead := map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}},
		"required": []any{"path"}}
	body := map[string]any{"model": "big-pickle", "messages": []any{map[string]any{"role": "user", "content": "x"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "mine", "parameters": callerRead}},
			map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}},
		}}
	sent := sentBody(t, mustRequest(t, "big-pickle", body))
	tools, _ := sent["tools"].([]any)

	var readDef, lookup bool
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		switch fn["name"] {
		case "read":
			readDef = fn["description"] == fingerprintReadDesc
		case "lookup":
			lookup = true
		}
	}
	if !readDef {
		t.Fatalf("a caller 'read' with matching args must adopt the gated definition in place: %v", tools)
	}
	if !lookup {
		t.Fatalf("an unrelated caller tool was dropped: %v", tools)
	}
}

func TestResponseConvertsMessagesToChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"union-alpha","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	out, err := response(resp, "union-alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(out.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["object"] != "chat.completion" || !strings.Contains(mustJSON(t, got["choices"]), "hello") {
		t.Fatalf("converted = %v", got)
	}
}

// ---- helpers ----

func mustRequest(t *testing.T, model string, body map[string]any) *http.Request {
	t.Helper()
	req, err := request(context.Background(), provider.NativeCall{URL: endpoints("", nil)[0], Model: model, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func sentBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func hasTool(tools []any, name string) bool {
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		if fn["name"] == name {
			return true
		}
	}
	return false
}

// hasToolFlat finds a tool by name on the shapes that carry it at the top level (responses and messages).
func hasToolFlat(sent map[string]any, name string) bool {
	for _, tool := range asList(sent["tools"]) {
		if tool.(map[string]any)["name"] == name {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTransportRepeatsHandshake guards the fingerprint spec being derived per dial: a cached *ClientHelloSpec fails
// every handshake after the first with "tls: internal error", which live calls hit on the second request.
func TestTransportRepeatsHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	c := &http.Client{Transport: transportFor(true)}
	for i := 0; i < 3; i++ {
		resp, err := c.Post(srv.URL, "text/plain", nil)
		if err != nil {
			t.Fatalf("handshake %d: %v", i, err)
		}
		resp.Body.Close()
	}
}

// TestTransportThroughConnectProxy: the fingerprint must run inside the proxy's tunnel, end to end with the target, and
// the DNS name the proxy is asked for must be the target's (not the proxy's).
func TestTransportThroughConnectProxy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "via-tunnel") }))
	defer srv.Close()

	var asked string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy got %s, want CONNECT", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		asked = r.Host
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		defer conn.Close()
		defer up.Close()
		io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { io.Copy(up, buf); up.Close() }()
		io.Copy(conn, up)
	}))
	defer px.Close()

	c := &http.Client{Transport: transportFor(true)}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req = req.WithContext(ProxyForConn(req.Context(), px.URL))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "via-tunnel" {
		t.Errorf("body = %q, want via-tunnel", b)
	}
	if want := strings.TrimPrefix(srv.URL, "https://"); asked != want {
		t.Errorf("proxy asked for %q, want the target %q", asked, want)
	}

	// the spec is derived per dial: a cached one fails the second handshake with "tls: internal error"
	for i := 0; i < 2; i++ {
		c.CloseIdleConnections()
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req = req.WithContext(ProxyForConn(req.Context(), px.URL))
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("dial %d through the tunnel: %v", i+2, err)
		}
		resp.Body.Close()
	}

	// a socks5 pool cannot carry the fingerprint: it must be refused by name, not dialled
	c.CloseIdleConnections()
	req, _ = http.NewRequest("GET", srv.URL, nil)
	req = req.WithContext(ProxyForConn(req.Context(), "socks5://127.0.0.1:1080"))
	if _, err := c.Do(req); err == nil || !strings.Contains(err.Error(), "http(s) proxy") {
		t.Fatalf("socks5 request = %v, want a refusal", err)
	}
}

// Next to other Claude Code tools, ReportFindings makes the free tier answer 429 for hours (live, 2026-11-11). It is
// sent under another name on every lane and gets its own name back in the answer.
func TestReportFindingsToolIsAliasedAndRestored(t *testing.T) {
	tool := func(name string) any {
		return map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	body := map[string]any{"model": "big-pickle", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{tool("ReportFindings"), tool("DeferredToolPlaceholder"), tool("ScheduleWakeup"), tool("Write")}}
	req := mustRequest(t, "big-pickle", body)
	tools, _ := sentBody(t, req)["tools"].([]any)
	if hasTool(tools, "ReportFindings") || !hasTool(tools, "report_findings") || !hasTool(tools, "DeferredToolPlaceholder") || !hasTool(tools, "ScheduleWakeup") || !hasTool(tools, "Write") {
		t.Fatalf("only ReportFindings should be renamed: %s", mustJSON(t, tools))
	}
	renames, _ := req.Context().Value(renamesKey{}).(map[string]string)
	if renames["report_findings"] != "ReportFindings" {
		t.Fatalf("rename map = %v", renames)
	}
	logged := false
	for _, e := range consolelog.Default.Snapshot() {
		logged = logged || strings.Contains(e.Msg, "[OPENCODE]") && strings.Contains(e.Msg, "ReportFindings → report_findings")
	}
	if !logged {
		t.Error("the rename is not in the console log")
	}
	event := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "function": map[string]any{"name": "report_findings", "arguments": "{}"}}}}}}}
	restoreToolNames(event, renames)
	if !strings.Contains(mustJSON(t, event), `"ReportFindings"`) {
		t.Fatalf("caller tool name not restored: %v", event)
	}
}
