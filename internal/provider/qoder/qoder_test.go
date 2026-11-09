package qoder

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"router-go/internal/provider"
)

func TestEncodeBodyMatchesReference(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"a":1,"b":"héllo"}`, ")uH%bBEw$oK*BMn*PVlmYKiBMn%G"},
		{"ab", "$SB#"},
	} {
		if got := string(encodeBody([]byte(tc.in))); got != tc.want {
			t.Errorf("encode(%q) = %q, want %q", tc.in, got, tc.want)
		}
		back, err := decodeBody([]byte(tc.want))
		if err != nil || string(back) != tc.in {
			t.Errorf("decode(%q) = %q %v", tc.want, back, err)
		}
	}
	long := strings.Repeat(`{"message":"x"}`, 400)
	if back, err := decodeBody(encodeBody([]byte(long))); err != nil || string(back) != long {
		t.Fatalf("round trip of %d bytes failed: %v", len(long), err)
	}
}

func TestCosyHeadersSignTheExactBody(t *testing.T) {
	body := []byte("encoded-body")
	const u = "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?Encode=1"
	h, err := cosyHeaders(body, u, cosyCreds{UserID: "u-1", Token: "jt-1"})
	if err != nil {
		t.Fatal(err)
	}
	auth, ok := strings.CutPrefix(h["Authorization"], "Bearer COSY.")
	if !ok {
		t.Fatalf("authorization = %q", h["Authorization"])
	}
	parts := strings.Split(auth, ".")
	if len(parts) != 2 || len(parts[1]) != 32 {
		t.Fatalf("authorization = %q", h["Authorization"])
	}
	var payload struct{ Version, RequestId, Info, CosyVersion string }
	raw, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("payload %q: %v", raw, err)
	}
	if payload.Version != "v1" || payload.RequestId == "" || payload.CosyVersion != ideVersion || payload.Info == "" {
		t.Errorf("payload = %+v", payload)
	}
	if h["Cosy-Sigpath"] != "/api/v2/service/pro/sse/agent_chat_generation" {
		t.Errorf("sigpath = %q", h["Cosy-Sigpath"])
	}
	if h["Cosy-Bodylength"] != "12" || h["Cosy-Machineid"] != h["Cosy-Machinetoken"] {
		t.Errorf("body length %q machine %q", h["Cosy-Bodylength"], h["Cosy-Machineid"])
	}
	for _, k := range []string{"Cosy-Key", "Cosy-User", "Cosy-Date", "Cosy-Version", "Cosy-Machinetype", "Cosy-Machineos",
		"Cosy-Clienttype", "Cosy-Clientip", "Cosy-Bodyhash", "Cosy-Data-Policy", "Cosy-Organization-Id", "Login-Version", "X-Request-Id"} {
		if h[k] == "" && k != "Cosy-Organization-Id" {
			t.Errorf("%s is missing", k)
		}
	}
	// same user, same machine id: the upstream tracks its model queue per client
	other, _ := cosyHeaders(body, u, cosyCreds{UserID: "u-1", Token: "jt-1"})
	if other["Cosy-Machineid"] != h["Cosy-Machineid"] {
		t.Error("machine id is not stable per user")
	}
	if other["Cosy-Key"] == h["Cosy-Key"] {
		t.Error("the aes key was reused across requests")
	}
	if _, err := cosyHeaders(body, u, cosyCreds{UserID: "", Token: "jt-1"}); err == nil {
		t.Error("a missing user id must be an error")
	}
}

func TestSealInfoDecryptsToTheIdentity(t *testing.T) {
	const key = "0123456789abcdef"
	plain, _ := json.Marshal(map[string]any{"uid": "u-1", "security_oauth_token": "jt-1", "name": "", "aid": "", "email": ""})
	sealed, err := sealInfo(plain, key)
	if err != nil || len(sealed)%aes.BlockSize != 0 {
		t.Fatalf("seal: %d %v", len(sealed), err)
	}
	block, _ := aes.NewCipher([]byte(key))
	round := make([]byte, len(sealed))
	cipher.NewCBCDecrypter(block, []byte(key)).CryptBlocks(round, sealed)
	pad := int(round[len(round)-1])
	if string(round[:len(round)-pad]) != string(plain) {
		t.Errorf("round trip = %q", round)
	}
}

func TestPayloadHoistsSystemAndKeepsImages(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "be terse"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "what is this?"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAA"}},
				map[string]any{"type": "file", "file": map[string]any{"filename": "big.pdf"}},
			}},
		},
		"tools":      []any{map[string]any{"type": "function", "function": map[string]any{"name": "read"}}},
		"max_tokens": float64(1000),
	}
	cfg := map[string]any{"key": "auto", "is_reasoning": true, "max_output_tokens": 65536}
	p := qoderPayload("qoder/auto", body, map[string]any{"userId": "u-1"}, cfg)

	if p["system"] != "be terse" || p["stream"] != true || p["agent_id"] != "agent_common" || p["session_type"] != "qodercli" {
		t.Errorf("payload scalars: system=%v stream=%v agent=%v", p["system"], p["stream"], p["agent_id"])
	}
	msgs := p["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	blocks := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("content blocks = %v", blocks)
	}
	if text := blocks[0].(map[string]any)["text"].(string); !strings.Contains(text, "big.pdf") || !strings.HasPrefix(text, "what is this?") {
		t.Errorf("text block = %q", text)
	}
	if got := p["parameters"].(map[string]any)["max_tokens"]; got != 1000 {
		t.Errorf("max_tokens = %v (a smaller client budget must win)", got)
	}
	if p["model_config"].(map[string]any)["key"] != "auto" {
		t.Errorf("model_config = %v", p["model_config"])
	}
	ctx := p["chat_context"].(map[string]any)
	if text, _ := ctx["text"].(string); !strings.HasPrefix(text, "what is this?") || ctx["extra"].(map[string]any)["modelConfig"].(map[string]any)["is_reasoning"] != true {
		t.Errorf("chat_context = %v", ctx)
	}
	// the same conversation yields the same ids, so the upstream keeps its record
	again := qoderPayload("qoder/auto", body, map[string]any{"userId": "u-1"}, cfg)
	if again["chat_record_id"] != p["chat_record_id"] || again["session_id"] != p["session_id"] {
		t.Error("ids are not stable for the same conversation")
	}
	if again["request_id"] == p["request_id"] {
		t.Error("request_id must be fresh per call")
	}
}

func envelopeSSE(inner string) string {
	b, _ := json.Marshal(inner)
	return `data: {"statusCodeValue":200,"body":` + string(b) + "}\n\n"
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResponseStreamsChatAndCoalescesUsage(t *testing.T) {
	up := envelopeSSE(`{"id":"c1","created":7,"model":"auto","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`) +
		envelopeSSE(`{"id":"c1","choices":[{"index":0,"delta":{"content":" there"}}]}`) +
		envelopeSSE(`{"id":"c1","choices":[{"index":0,"delta":{"finish_reason":"stop"}}]}`) +
		envelopeSSE(`{"id":"c1","choices":[],"usage":{"input_tokens":11,"output_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}`) +
		"data: [DONE]\n\n" + ": keepalive\n\n"
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(up))}

	out, err := response(resp, "auto", true)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, out.Body)
	lines := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(lines) != 4 || lines[3] != "data: [DONE]" {
		t.Fatalf("frames = %q", lines)
	}
	if !strings.Contains(lines[0], `"content":"hi"`) || !strings.Contains(lines[1], `"content":" there"`) {
		t.Errorf("content frames = %q", lines[:2])
	}
	// the finish frame carries what the client reads usage from
	if !strings.Contains(lines[2], `"finish_reason":"stop"`) || !strings.Contains(lines[2], `"prompt_tokens":11`) ||
		!strings.Contains(lines[2], `"completion_tokens":4`) || !strings.Contains(lines[2], `"cached_tokens":3`) {
		t.Errorf("terminal frame = %q", lines[2])
	}
}

func TestResponseNonStreamAggregates(t *testing.T) {
	up := envelopeSSE(`{"id":"c1","model":"auto","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`) +
		envelopeSSE(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`) +
		envelopeSSE(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`) +
		envelopeSSE(`{"choices":[{"index":0,"delta":{"finish_reason":"tool_calls"}}]}`) +
		envelopeSSE(`{"choices":[],"usage":{"input_tokens":5,"output_tokens":2}}`)
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(up))}
	out, err := response(resp, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     float64 `json:"prompt_tokens"`
			Completion float64 `json:"completion_tokens"`
		}
	}
	if err := json.Unmarshal([]byte(readAll(t, out.Body)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "auto" || len(got.Choices) != 1 || got.Choices[0].Finish != "tool_calls" {
		t.Fatalf("response = %+v", got)
	}
	if len(got.Choices[0].Message.ToolCalls) != 1 || got.Choices[0].Message.ToolCalls[0].ID != "t1" ||
		got.Choices[0].Message.ToolCalls[0].Function.Name != "read" || got.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("tool calls = %+v", got.Choices[0].Message.ToolCalls)
	}
	if got.Usage.Prompt != 5 || got.Usage.Completion != 2 {
		t.Errorf("usage = %+v", got.Usage)
	}
}

func TestResponseTurnsFirstFrameErrorsIntoStatuses(t *testing.T) {
	queue := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\"}","subCode":"RESOURCE_LIMIT"}`
	billing := `{"code":"112","message":"quota exhausted","pricingUrl":"https://qoder.com/pricing"}`
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"queued", `data: {"statusCodeValue":500,"body":` + quote(queue) + "}\n\n", 429},
		{"billing", `data: {"statusCodeValue":500,"body":` + quote(billing) + "}\n\n", 403},
		{"upstream", `data: {"statusCodeValue":503,"body":"busy"}` + "\n\n", 503},
	} {
		resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}
		out, err := response(resp, "auto", true)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.StatusCode != tc.status || !strings.Contains(readAll(t, out.Body), `"error"`) {
			t.Errorf("%s: status %d", tc.name, out.StatusCode)
		}
	}
	// a queued model announced on a 200 envelope is a capacity signal too
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`data: {"statusCodeValue":200,"body":` + quote(queue) + "}\n\n"))}
	if out, _ := response(resp, "auto", true); out.StatusCode != 429 {
		t.Errorf("queue on a 200 envelope: %d", out.StatusCode)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A non-SSE body (a bare envelope, as some proxy setups hand it back) and a numeric-string status still work.
func TestResponseAcceptsBareEnvelopeAndStringStatus(t *testing.T) {
	bare := `{"statusCodeValue":"500","body":"{\"code\":\"10605\"}"}`
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bare))}
	out, err := response(resp, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != 429 {
		t.Errorf("status = %d", out.StatusCode)
	}

	ok := `{"statusCodeValue":"200","body":"{\"id\":\"c9\",\"model\":\"auto\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"yo\"},\"finish_reason\":\"stop\"}]}"}`
	resp = &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(ok))}
	out, err = response(resp, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	if b := readAll(t, out.Body); !strings.Contains(b, `"yo"`) || !strings.Contains(b, `"finish_reason":"stop"`) {
		t.Errorf("body = %s", b)
	}
}

func TestEndpointsHonourAnOverriddenBase(t *testing.T) {
	// what provider.Override("qoder", …) does to the registry entry the gateway reads
	restore := provider.Override("qoder", "http://127.0.0.1:41234")
	defer restore()
	p, ok := provider.ByID("qoder")
	if !ok || p.Status == provider.StatusUnsupported {
		t.Fatalf("qoder is not routable: %+v", p)
	}
	got := endpoints(p.BaseURL, nil)
	if len(got) != 1 || got[0] != "http://127.0.0.1:41234"+sigPath+query {
		t.Errorf("override = %v", got)
	}
	if got[0] != "http://127.0.0.1:41234/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1" {
		t.Errorf("override path = %s", got[0])
	}
	got = endpoints(defaultBase, nil)
	if len(got) != 2 || !strings.HasPrefix(got[0], chatAltBase) || !strings.HasPrefix(got[1], ideHost) {
		t.Errorf("default = %v (the job token a PAT is exchanged for needs api2 first)", got)
	}
}

// mockDoer answers every side call from the real hosts' paths.
type mockDoer struct {
	body    func(*http.Request) string
	headers []http.Header
	urls    []string
}

func (m *mockDoer) Do(r *http.Request) (*http.Response, error) {
	m.headers = append(m.headers, r.Header.Clone())
	m.urls = append(m.urls, r.URL.String())
	body := m.body(r)
	status := 200
	if body == "" {
		status = 404
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestRequestExchangesPatAndSignsEncodedBody(t *testing.T) {
	credMemo = map[string]cred{}
	catMemo = map[string]catalog{}
	do := &mockDoer{body: func(r *http.Request) string {
		switch {
		case strings.HasSuffix(r.URL.Path, exchangePath):
			return `{"token":"jt-abc","expires_in":3600}`
		case strings.HasSuffix(r.URL.Path, userInfoPath):
			return `{"id":"user-9"}`
		case strings.HasSuffix(r.URL.Path, modelListHr):
			return `{"chat":[{"key":"auto","display_name":"Auto","max_input_tokens":200000,"max_output_tokens":64000,"is_reasoning":true,"source":"system","enable":false}]}`
		}
		return ""
	}}
	call := provider.NativeCall{
		URL: "https://api3.qoder.sh" + sigPath + query, Token: "pt-xyz", Model: "auto", Stream: true,
		Data: map[string]any{}, Do: do.Do,
		Body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}
	req, err := request(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != call.URL || req.Header.Get("X-Model-Key") != "auto" || req.Header.Get("X-Model-Source") != "system" {
		t.Errorf("url %s headers %v", req.URL, req.Header)
	}
	if req.Header.Get("Accept-Encoding") != "identity" || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer COSY.") {
		t.Errorf("headers %v", req.Header)
	}
	// the exchange and the catalog are plain calls; the chat body is the encoded payload
	if len(do.urls) != 3 || !strings.HasSuffix(do.urls[0], exchangePath) || !strings.HasSuffix(do.urls[2], modelListHr) {
		t.Fatalf("side calls = %v", do.urls)
	}
	if got := do.headers[0].Get("Cosy-ClientType"); got != clientType {
		t.Errorf("exchange headers = %v", do.headers[0])
	}
	if strings.HasPrefix(do.headers[2].Get("Authorization"), "Bearer COSY.") == false { // the catalog is COSY-signed, not a bearer token
		t.Errorf("catalog headers = %v", do.headers[2])
	}
	plain, err := decodeBody([]byte(readAll(t, req.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["agent_id"] != "agent_common" || payload["model_config"].(map[string]any)["display_name"] != "Auto" {
		t.Errorf("payload = %v", payload)
	}
	if got := payload["parameters"].(map[string]any)["max_tokens"]; got != float64(64000) {
		t.Errorf("max_tokens = %v (the catalog's max_output_tokens wins)", got)
	}
	// a second request reuses both the exchanged job token and the cached catalog
	call.URL = chatAltBase + sigPath + query
	before := len(do.urls)
	if _, err := request(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(do.urls)-before != 0 {
		t.Errorf("the job token or catalog was fetched again: %d side calls", len(do.urls)-before)
	}
}

// Overridden via provider.Override (what the api tests do), every call — exchange, userinfo, catalog, chat — stays on
// the mock host, so no test ever reaches qoder.sh.
func TestOverrideKeepsEveryCallOnTheMockHost(t *testing.T) {
	credMemo = map[string]cred{}
	catMemo = map[string]catalog{}
	restore := provider.Override("qoder", "http://127.0.0.1:41234")
	defer restore()
	p, _ := provider.ByID("qoder")
	urls := endpoints(p.BaseURL, nil)
	if len(urls) != 1 {
		t.Fatalf("endpoints = %v", urls)
	}
	do := &mockDoer{body: func(r *http.Request) string {
		switch {
		case strings.HasSuffix(r.URL.Path, exchangePath):
			return `{"token":"jt-1"}`
		case strings.HasSuffix(r.URL.Path, userInfoPath):
			return `{"userId":"u-9"}`
		case strings.HasSuffix(r.URL.Path, modelListHr):
			return `{"chat":[{"key":"auto","display_name":"Auto"}]}`
		}
		return ""
	}}
	call := provider.NativeCall{URL: urls[0], Token: "pt-1", Model: "auto", Data: map[string]any{}, Do: do.Do,
		Body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}}
	if _, err := request(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(do.urls) != 3 {
		t.Fatalf("side calls = %v", do.urls)
	}
	for _, u := range do.urls {
		if !strings.HasPrefix(u, "http://127.0.0.1:41234/") {
			t.Errorf("side call left the mock host: %s", u)
		}
	}
}

func TestModelConfigFallsBackToTheStaticBlock(t *testing.T) {
	catMemo = map[string]catalog{}
	do := &mockDoer{body: func(r *http.Request) string {
		return `{"chat":[{"key":"auto","display_name":"Auto"}]}`
	}}
	cfg, err := modelConfig(context.Background(), do, "https://api3.qoder.sh", "dt-1", "u-new", "smodel")
	if err != nil || cfg["display_name"] != "Sonus" || cfg["is_reasoning"] != true {
		t.Fatalf("static block = %v %v", cfg, err)
	}
	if _, err := modelConfig(context.Background(), do, "https://api3.qoder.sh", "dt-1", "u-new", "nope"); err == nil {
		t.Error("an unknown model must fail loudly")
	}
	cfg, err = modelConfig(context.Background(), do, "https://api3.qoder.sh", "dt-1", "u-new", "auto")
	if err != nil || cfg["display_name"] != "Auto" || cfg["key"] != "auto" {
		t.Fatalf("catalog block = %v %v", cfg, err)
	}
}

func TestCanonicalUsageAndFinish(t *testing.T) {
	u := canonicalUsage(map[string]any{"input_tokens": 10.0, "output_tokens": 5.0, "prompt_cache_hit_tokens": 2.0})
	if u["prompt_tokens"] != 10.0 || u["completion_tokens"] != 5.0 || u["total_tokens"] != 15.0 || u["cached_tokens"] != 2.0 {
		t.Errorf("usage = %v", u)
	}
	if canonicalUsage(map[string]any{"foo": 1.0}) != nil || canonicalUsage(nil) != nil {
		t.Error("an empty usage must stay nil")
	}
	if got := finishOf(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"finish_reason": "length"}}}}); got != "length" {
		t.Errorf("finish = %q", got)
	}
	if !isBilling(`{"code":"110"}`) || isBilling(`{"code":"10605"}`) {
		t.Error("billing detection is off")
	}
	if !isQueued(`{"code":"10605"}`) || isQueued(`{"code":"110"}`) {
		t.Error("queue detection is off")
	}
}

func TestPayloadFlattensTextOnlyContent(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "one"}, map[string]any{"type": "text", "text": "two"}}},
	}}
	p := qoderPayload("auto", body, nil, map[string]any{})
	msgs := p["messages"].([]any)
	if got := msgs[0].(map[string]any)["content"]; got != "one\ntwo" {
		t.Errorf("content = %#v", got)
	}
	if p["system"] != "" || p["tools"].([]any) == nil {
		t.Errorf("system=%q tools=%v", p["system"], p["tools"])
	}
}

// A chunk that carries content, finish and usage at once must not send finish_reason twice; the usage follows alone.
func TestResponseDoesNotRepeatFinishReason(t *testing.T) {
	up := envelopeSSE(`{"id":"c1","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"input_tokens":3,"output_tokens":1}}`) +
		"data: [DONE]\n\n"
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(up))}
	out, err := response(resp, "auto", true)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, out.Body)
	if n := strings.Count(body, `"finish_reason":"stop"`); n != 1 {
		t.Errorf("finish_reason appears %d times: %q", n, body)
	}
	if !strings.Contains(body, `"choices":[]`) || !strings.Contains(body, `"prompt_tokens":3`) {
		t.Errorf("usage frame = %q", body)
	}
}

// The non-stream path reads the terminal frame too: usage and finish often share it.
func TestResponseNonStreamReadsTheTerminalFrame(t *testing.T) {
	up := envelopeSSE(`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"input_tokens":4,"output_tokens":2}}`)
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(up))}
	out, err := response(resp, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Choices []struct {
			Finish string                   `json:"finish_reason"`
			Msg    struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt float64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(readAll(t, out.Body)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Finish != "stop" || got.Choices[0].Msg.Content != "hi" || got.Usage.Prompt != 4 {
		t.Errorf("response = %+v", got)
	}
}
