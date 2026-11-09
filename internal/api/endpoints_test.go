package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"router-go/internal/db"
)

// call sends a request and returns the status and the raw body.
func (e *env) call(method, path, body string, hdr ...string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func decodeMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not json: %s", s)
	}
	return m
}

const chatAnswer = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

func TestGeminiGenerateContent(t *testing.T) {
	var up map[string]any
	var path string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		path, up = r.URL.Path, nil
		json.NewDecoder(r.Body).Decode(&up)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatAnswer)
	}, "chat")
	e.addKey("one", "k1")
	body := `{"systemInstruction":{"parts":[{"text":"be brief"}]},"generationConfig":{"maxOutputTokens":50,"temperature":0.2,"topP":0.8,"stopSequences":["END"]},
	 "contents":[{"role":"user","parts":[{"text":"ping"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}]}`

	// the key may come as Bearer, x-goog-api-key or ?key=
	for name, c := range map[string]struct {
		path string
		hdr  []string
	}{
		"bearer": {"/v1beta/models/mock/m:generateContent", []string{"Authorization", "Bearer " + e.apiKey}},
		"header": {"/v1beta/models/mock/m:generateContent", []string{"x-goog-api-key", e.apiKey}},
		"query":  {"/v1beta/models/mock/m:generateContent?key=" + e.apiKey, nil},
	} {
		status, out := e.call("POST", c.path, body, c.hdr...)
		if status != 200 {
			t.Fatalf("%s: %d %s", name, status, out)
		}
		m := decodeMap(t, out)
		cand := m["candidates"].([]any)[0].(map[string]any)
		parts := cand["content"].(map[string]any)["parts"].([]any)
		if cand["finishReason"] != "STOP" || cand["content"].(map[string]any)["role"] != "model" || parts[0].(map[string]any)["text"] != "pong" {
			t.Errorf("%s: candidate %v", name, cand)
		}
		um := m["usageMetadata"].(map[string]any)
		if um["promptTokenCount"] != float64(3) || um["candidatesTokenCount"] != float64(1) || um["totalTokenCount"] != float64(4) {
			t.Errorf("%s: usage %v", name, um)
		}
	}
	// what the upstream chat node received
	msgs := up["messages"].([]any)
	if path != "/v1/chat/completions" || up["model"] != "m" || up["max_tokens"] != float64(50) || up["temperature"] != 0.2 || up["top_p"] != 0.8 || up["stream"] == true ||
		len(msgs) != 2 || msgs[0].(map[string]any)["content"] != "be brief" {
		t.Errorf("upstream path=%s body=%v", path, up)
	}
	img := msgs[1].(map[string]any)["content"].([]any)[1].(map[string]any)
	if img["image_url"].(map[string]any)["url"] != "data:image/png;base64,QUJD" {
		t.Errorf("image part = %v", img)
	}
	if rows, _, _ := e.store.History(1, 0, "", "", ""); len(rows) != 1 || rows[0].Endpoint != "/v1beta/models/mock/m:generateContent" || rows[0].PromptTokens != 3 {
		t.Errorf("usage row = %+v", rows)
	}
}

func TestGeminiStream(t *testing.T) {
	var up map[string]any
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		up = nil
		json.NewDecoder(r.Body).Decode(&up)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hel"}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
			`[DONE]`,
		} {
			io.WriteString(w, "data: "+d+"\n\n")
			w.(http.Flusher).Flush()
		}
	}, "chat")
	e.addKey("one", "k1")
	status, out := e.call("POST", "/v1beta/models/mock/m:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "x-goog-api-key", e.apiKey)
	if status != 200 || up["stream"] != true {
		t.Fatalf("status=%d upstream stream=%v body=%s", status, up["stream"], out)
	}
	var text string
	var calls []map[string]any
	frames := sseFrames(t, out)
	for _, f := range frames {
		for _, p := range f["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any) {
			pm := p.(map[string]any)
			if s, ok := pm["text"].(string); ok {
				text += s
			}
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				calls = append(calls, fc)
			}
		}
	}
	last := frames[len(frames)-1]
	if text != "hello" || len(calls) != 1 || calls[0]["name"] != "f" || calls[0]["args"].(map[string]any)["a"] != float64(1) {
		t.Errorf("text=%q calls=%v", text, calls)
	}
	if last["candidates"].([]any)[0].(map[string]any)["finishReason"] != "STOP" || last["usageMetadata"].(map[string]any)["promptTokenCount"] != float64(4) {
		t.Errorf("last frame = %v", last)
	}
	if len(frames) < 3 { // really streamed: the text came in several chunks
		t.Errorf("frames = %d", len(frames))
	}
}

// sseFrames decodes a Gemini SSE body: data-only frames, no [DONE].
func sseFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, fr := range strings.Split(strings.TrimSpace(body), "\n\n") {
		data, ok := strings.CutPrefix(fr, "data: ")
		if !ok || strings.Contains(data, "[DONE]") {
			t.Fatalf("bad frame %q", fr)
		}
		out = append(out, decodeMap(t, data))
	}
	return out
}

func TestGeminiThroughAnthropicNode(t *testing.T) {
	var up map[string]any
	var path string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		up = nil
		json.NewDecoder(r.Body).Decode(&up)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"pong"},{"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":5}}`)
	}, "chat")
	an, err := e.store.CreateNode(db.NodeTypeAnthropic, "Claude", "anth", "", e.node.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	seedModels(e.store, "anth")
	if _, err := e.store.CreateConnection(an, "k", "sk-ant"); err != nil {
		t.Fatal(err)
	}
	status, out := e.call("POST", "/v1beta/models/anth/claude:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},
	 "contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT","properties":{"a":{"type":"INTEGER"}}}}]}]}`,
		"x-goog-api-key", e.apiKey)
	if status != 200 || !strings.HasSuffix(path, "/messages") || up["system"] != "sys" || up["model"] != "claude" {
		t.Fatalf("status=%d path=%s upstream=%v body=%s", status, path, up, out)
	}
	if schema := up["tools"].([]any)[0].(map[string]any)["input_schema"].(map[string]any); schema["type"] != "object" {
		t.Errorf("schema = %v", schema)
	}
	m := decodeMap(t, out)
	cand := m["candidates"].([]any)[0].(map[string]any)
	parts := cand["content"].(map[string]any)["parts"].([]any)
	fc := parts[1].(map[string]any)["functionCall"].(map[string]any)
	if parts[0].(map[string]any)["text"] != "pong" || fc["name"] != "f" || fc["args"].(map[string]any)["a"] != float64(1) || cand["finishReason"] != "STOP" {
		t.Errorf("response = %s", out)
	}
	if um := m["usageMetadata"].(map[string]any); um["promptTokenCount"] != float64(7) || um["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage = %v", um)
	}
}

func TestGeminiErrorShape(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"bad param"}}`)
	}, "chat")
	e.addKey("one", "k1")
	want := func(name string, status int, gstatus, contains string, path, body string, hdr ...string) {
		t.Helper()
		got, out := e.call("POST", path, body, hdr...)
		m := decodeMap(t, out)
		er, _ := m["error"].(map[string]any)
		if got != status || er["code"] != float64(status) || er["status"] != gstatus || !strings.Contains(er["message"].(string), contains) {
			t.Errorf("%s: %d %s", name, got, out)
		}
	}
	ok := []string{"x-goog-api-key", e.apiKey}
	good := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	want("no key", 401, "UNAUTHENTICATED", "Missing API key", "/v1beta/models/mock/m:generateContent", good)
	want("bad key", 401, "UNAUTHENTICATED", "Invalid API key", "/v1beta/models/mock/m:generateContent?key=nope", good)
	want("unknown model", 404, "NOT_FOUND", "not found", "/v1beta/models/zzz/m:generateContent", good, ok...)
	want("bad json", 400, "INVALID_ARGUMENT", "Invalid JSON", "/v1beta/models/mock/m:generateContent", `{`, ok...)
	want("bad action", 404, "NOT_FOUND", "Unsupported method", "/v1beta/models/mock/m:embedContent", good, ok...)
	want("upstream rejects", 400, "INVALID_ARGUMENT", "bad param", "/v1beta/models/mock/m:streamGenerateContent", good, ok...)
	// ?key= is Gemini-only: other endpoints still need a header
	if st, _ := e.call("POST", "/v1/chat/completions?key="+e.apiKey, `{"model":"mock/m","messages":[]}`); st != 401 {
		t.Errorf("?key= on chat = %d", st)
	}
}

func TestModelsInfoAndLookup(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.addKey("one", "k1")
	e.store.AddCustomModel("mock", "gpt-4o", "")
	if err := e.store.SetModelCaps("mock", "m", map[string]any{"reasoning": true, "contextWindow": float64(200000)}); err != nil {
		t.Fatal(err)
	}
	auth := []string{"Authorization", "Bearer " + e.apiKey}
	for _, path := range []string{"/v1/models/info?id=mock/m", "/v1/models/mock/m"} {
		st, out := e.call("GET", path, "", auth...)
		m := decodeMap(t, out)
		caps, _ := m["capabilities"].(map[string]any)
		if st != 200 || m["id"] != "mock/m" || m["object"] != "model" || m["owned_by"] != "Mock" || caps["reasoning"] != true || caps["vision"] != false || m["contextWindow"] != float64(200000) {
			t.Errorf("%s: %d %s", path, st, out)
		}
	}
	st, out := e.call("GET", "/v1/models/mock/gpt-4o", "", auth...) // detected from the name
	if m := decodeMap(t, out); st != 200 || m["capabilities"].(map[string]any)["vision"] != true {
		t.Errorf("vision detection: %d %s", st, out)
	}
	for path, want := range map[string]int{"/v1/models/info": 400, "/v1/models/info?id=mock/nope": 404, "/v1/models/mock/nope": 404, "/v1/models/zzz/m": 404} {
		if st, out := e.call("GET", path, "", auth...); st != want || !strings.Contains(out, `"error"`) {
			t.Errorf("%s: %d %s", path, st, out)
		}
	}
	if st, _ := e.call("GET", "/v1/models/mock/m", ""); st != 401 {
		t.Errorf("no key = %d", st)
	}
	if st, out := e.call("GET", "/v1/models/embedding", "", auth...); st != 200 || !strings.Contains(out, `"data":[]`) {
		t.Errorf("kind slug: %d %s", st, out)
	}
	// a restricted key does not see a model outside its allow list
	k, _ := e.store.CreateAPIKey("r", "0123456789abcdef", "sk-restricted", db.Access{Restricted: true, Allow: []string{"mock/m2"}})
	if st, _ := e.call("GET", "/v1/models/mock/m", "", "Authorization", "Bearer "+k.Key); st != 404 {
		t.Errorf("restricted key sees mock/m: %d", st)
	}
	if st, _ := e.call("GET", "/v1/models/mock/m2", "", "Authorization", "Bearer "+k.Key); st != 200 {
		t.Errorf("restricted key cannot see mock/m2: %d", st)
	}

	// Gemini list and single model
	st, out = e.call("GET", "/v1beta/models?key="+e.apiKey, "")
	list, _ := decodeMap(t, out)["models"].([]any)
	if st != 200 || len(list) == 0 || !strings.Contains(out, `"name":"models/mock/m"`) || !strings.Contains(out, `"inputTokenLimit":200000`) || !strings.Contains(out, "streamGenerateContent") {
		t.Errorf("gemini models: %d %s", st, out)
	}
	if st, out := e.call("GET", "/v1beta/models/mock/m", "", "x-goog-api-key", e.apiKey); st != 200 || !strings.Contains(out, `"name":"models/mock/m"`) {
		t.Errorf("gemini model: %d %s", st, out)
	}
	if st, out := e.call("GET", "/v1beta/models/mock/nope", "", "x-goog-api-key", e.apiKey); st != 404 || !strings.Contains(out, `"NOT_FOUND"`) {
		t.Errorf("gemini model 404: %d %s", st, out)
	}
}

func TestCountTokens(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { t.Error("count_tokens must not call the upstream") }, "chat")
	e.addKey("one", "k1")
	auth := []string{"Authorization", "Bearer " + e.apiKey}
	text := strings.Repeat("a", 4000)
	st, out := e.call("POST", "/v1/messages/count_tokens", `{"model":"mock/m","max_tokens":9999,"system":"`+text+`","messages":[{"role":"user","content":"`+text+`"}]}`, auth...)
	n := decodeMap(t, out)["input_tokens"]
	if st != 200 || n != float64(2001) { // 8000 characters / 4 (+ the role); max_tokens is not input
		t.Errorf("anthropic: %d %s", st, out)
	}
	// an image counts a flat amount, not its base64 length
	_, out = e.call("POST", "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+strings.Repeat("A", 40000)+`"}}]}]}`, auth...)
	if n := decodeMap(t, out)["input_tokens"].(float64); n < 1000 || n > 1100 {
		t.Errorf("image tokens = %v", n)
	}
	if st, out := e.call("POST", "/v1/messages/count_tokens", `{`, auth...); st != 400 || !strings.Contains(out, `"type":"error"`) {
		t.Errorf("bad json: %d %s", st, out)
	}
	if st, out := e.call("POST", "/v1/messages/count_tokens", `{}`); st != 401 || !strings.Contains(out, `"authentication_error"`) {
		t.Errorf("no key: %d %s", st, out)
	}
	// Gemini countTokens, plain or wrapped in generateContentRequest
	for _, body := range []string{`{"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}`,
		`{"generateContentRequest":{"model":"models/x","generationConfig":{"maxOutputTokens":50},"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}}`} {
		st, out := e.call("POST", "/v1beta/models/mock/m:countTokens", body, "x-goog-api-key", e.apiKey)
		if tt := decodeMap(t, out)["totalTokens"]; st != 200 || tt != float64(1001) { // 4000 characters / 4 (+ the role); maxOutputTokens is not input
			t.Errorf("gemini countTokens: %d %s", st, out)
		}
	}
}

func TestResponsesCompact(t *testing.T) {
	var path string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(path, "/responses/compact") {
			io.WriteString(w, `{"id":"resp_1","object":"response.compaction","output":[]}`)
			return
		}
		io.WriteString(w, chatAnswer)
	}, "responses")
	e.addKey("one", "k1")
	body := `{"model":"mock/m","input":[{"role":"user","content":"hi"}]}`
	resp := e.post("/v1/responses/compact", body, e.apiKey)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || path != "/v1/responses/compact" || !strings.Contains(string(b), "response.compaction") {
		t.Errorf("compact: %d path=%s %s", resp.StatusCode, path, b)
	}
	e.post("/v1/responses", body, e.apiKey).Body.Close() // the plain endpoint is untouched
	if path != "/v1/responses" {
		t.Errorf("plain responses went to %s", path)
	}
}

func TestOllamaChat(t *testing.T) {
	var up map[string]any
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		up = nil
		json.NewDecoder(r.Body).Decode(&up)
		if up["messages"] == nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"no messages"}}`)
			return
		}
		if up["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"pong","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":"hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		} {
			io.WriteString(w, "data: "+d+"\n\n")
		}
	}, "chat")
	e.addKey("one", "k1")
	auth := []string{"Authorization", "Bearer " + e.apiKey}
	lines := func(out string) []map[string]any {
		var ls []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			ls = append(ls, decodeMap(t, l))
		}
		return ls
	}

	// Ollama streams by default
	st, out := e.call("POST", "/v1/api/chat", `{"model":"mock/m","messages":[{"role":"user","content":"hi"}]}`, auth...)
	ls := lines(out)
	if st != 200 || up["stream"] != true || len(ls) != 3 {
		t.Fatalf("stream: %d upstream stream=%v %s", st, up["stream"], out)
	}
	if ls[0]["message"].(map[string]any)["content"] != "hel" || ls[0]["done"] != false || ls[0]["model"] != "mock/m" || ls[1]["message"].(map[string]any)["content"] != "lo" {
		t.Errorf("chunks: %s", out)
	}
	fin := ls[2]
	call := fin["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fin["done"] != true || call["name"] != "f" || call["arguments"].(map[string]any)["a"] != float64(1) {
		t.Errorf("final: %s", out)
	}

	// stream:false gives one object
	st, out = e.call("POST", "/v1/api/chat", `{"model":"mock/m","stream":false,"messages":[{"role":"user","content":"hi"}]}`, auth...)
	one := decodeMap(t, out)
	if st != 200 || up["stream"] == true || one["done"] != true || one["message"].(map[string]any)["content"] != "pong" || len(lines(out)) != 1 {
		t.Errorf("non-stream: %d %s", st, out)
	}

	// errors keep their normal shape
	if st, out := e.call("POST", "/v1/api/chat", `{"model":"zzz/m","messages":[]}`, auth...); st != 404 || !strings.Contains(out, "model_not_found") {
		t.Errorf("unknown model: %d %s", st, out)
	}
	if st, _ := e.call("POST", "/v1/api/chat", `{"model":"mock/m"}`, auth...); st != 400 {
		t.Errorf("upstream 400 = %d", st)
	}
}
