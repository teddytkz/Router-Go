package kiro

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"router-go/internal/provider"
)

func chat(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// payload builds the Kiro request for a chat body and returns it as plain JSON (what Kiro would receive).
func payload(t *testing.T, model, body string, data map[string]any) map[string]any {
	t.Helper()
	p, _ := buildPayload(model, chat(t, body), data)
	b, _ := json.Marshal(p)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("no %q in %v", k, v)
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				t.Fatalf("no [%d] in %v", k, v)
			}
			v = l[k]
		}
	}
	return v
}

func TestPayloadHistoryAndSystem(t *testing.T) {
	p := payload(t, "claude-sonnet-4.5", `{"temperature":0.5,"messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, nil)
	conv := dig(t, p, "conversationState").(map[string]any)
	if conv["chatTriggerType"] != "MANUAL" || conv["conversationId"] == "" {
		t.Fatalf("conversation = %v", conv)
	}
	// system + first user merge into one turn: the history is [user, assistant], then the current user turn
	if n := len(conv["history"].([]any)); n != 2 {
		t.Fatalf("history = %v", conv["history"])
	}
	if c := dig(t, conv, "history", 0, "userInputMessage", "content"); c != "<instructions>\nbe brief\n</instructions>\n\nhi" {
		t.Errorf("first turn = %q", c)
	}
	if c := dig(t, conv, "history", 1, "assistantResponseMessage", "content"); c != "hello" {
		t.Errorf("assistant = %q", c)
	}
	cur := dig(t, conv, "currentMessage", "userInputMessage").(map[string]any)
	if cur["content"] != "look" || cur["modelId"] != "claude-sonnet-4.5" || cur["origin"] != "AI_EDITOR" {
		t.Errorf("current = %v", cur)
	}
	if f, b := dig(t, cur, "images", 0, "format"), dig(t, cur, "images", 0, "source", "bytes"); f != "png" || b != "AAAA" {
		t.Errorf("image = %v %v", f, b)
	}
	if dig(t, p, "inferenceConfig", "temperature") != 0.5 || dig(t, p, "inferenceConfig", "maxTokens") != float64(32000) {
		t.Errorf("inferenceConfig = %v", p["inferenceConfig"])
	}
	// Builder-ID style account without its own ARN: the shared default is sent
	if p["profileArn"] != builderIDProfileARN {
		t.Errorf("profileArn = %v", p["profileArn"])
	}
}

func TestPayloadToolCallsAreRepaired(t *testing.T) {
	body := `{"tools":[{"type":"function","function":{"name":"get.weather","description":"d","parameters":{"type":"object","additionalProperties":false,"required":[],"properties":{"city":{"type":"string"}}}}}],
	"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"get.weather","arguments":"{\"city\":\"Oslo\"}"}},
			{"id":"call_2","type":"function","function":{"name":"gone","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"rain"},
		{"role":"tool","tool_call_id":"call_2","content":"orphan"},
		{"role":"tool","tool_call_id":"call_9","content":"nobody asked"}]}`
	p, back := buildPayload("claude-haiku-4.5", chat(t, body), map[string]any{"authMethod": "idc"})
	b, _ := json.Marshal(p)
	var out map[string]any
	json.Unmarshal(b, &out)
	conv := out["conversationState"]

	if back["get_weather"] != "get.weather" {
		t.Errorf("tool name map = %v", back)
	}
	// the answered call stays structured, under its Kiro-safe name; the call without definition is text
	use := dig(t, conv, "history", 1, "assistantResponseMessage", "toolUses", 0)
	if dig(t, use, "name") != "get_weather" || dig(t, use, "toolUseId") != "call_1" || dig(t, use, "input", "city") != "Oslo" {
		t.Errorf("toolUse = %v", use)
	}
	if n := len(dig(t, conv, "history", 1, "assistantResponseMessage", "toolUses").([]any)); n != 1 {
		t.Errorf("toolUses = %d", n)
	}
	if c := dig(t, conv, "history", 1, "assistantResponseMessage", "content").(string); !strings.Contains(c, "[Tool call: gone({})]") {
		t.Errorf("flattened call = %q", c)
	}
	cur := dig(t, conv, "currentMessage", "userInputMessage")
	res := dig(t, cur, "userInputMessageContext", "toolResults")
	if n := len(res.([]any)); n != 1 || dig(t, res, 0, "toolUseId") != "call_1" || dig(t, res, 0, "content", 0, "text") != "rain" {
		t.Errorf("toolResults = %v", res)
	}
	c := dig(t, cur, "content").(string)
	if !strings.Contains(c, "[Tool result: orphan]") || !strings.Contains(c, "[Tool result: nobody asked]") {
		t.Errorf("flattened results = %q", c)
	}
	// definitions ride on the current message, cleaned
	schema := dig(t, cur, "userInputMessageContext", "tools", 0, "toolSpecification", "inputSchema", "json").(map[string]any)
	if _, has := schema["additionalProperties"]; has || schema["required"] != nil || schema["type"] != "object" {
		t.Errorf("schema = %v", schema)
	}
	// IDC accounts never get a shared default ARN
	if _, has := out["profileArn"]; has {
		t.Errorf("profileArn on an idc account: %v", out["profileArn"])
	}
}

func TestPayloadEdgeShapes(t *testing.T) {
	// assistant first, nothing at the end for the user: placeholders keep the conversation valid
	p := payload(t, "glm-5", `{"messages":[{"role":"assistant","content":"hello"}]}`, map[string]any{"profileArn": "arn:x", "authMethod": "google"})
	if c := dig(t, p, "conversationState", "history", 0, "userInputMessage", "content"); c != "continue" {
		t.Errorf("leading placeholder = %v", c)
	}
	if c := dig(t, p, "conversationState", "currentMessage", "userInputMessage", "content"); c != "continue" {
		t.Errorf("trailing placeholder = %v", c)
	}
	if p["profileArn"] != "arn:x" {
		t.Errorf("own ARN lost: %v", p["profileArn"])
	}
	if p = payload(t, "x", `{"messages":[]}`, map[string]any{"authMethod": "github"}); p["profileArn"] != socialProfileARN {
		t.Errorf("social default ARN = %v", p["profileArn"])
	}
	// deepseek takes no images
	p = payload(t, "deepseek-3.2", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,QQ=="}}]}]}`, nil)
	if _, has := dig(t, p, "conversationState", "currentMessage", "userInputMessage").(map[string]any)["images"]; has {
		t.Error("images sent to deepseek")
	}
}

func TestModelSuffixesAndThinking(t *testing.T) {
	for in, want := range map[string]struct {
		up string
		ag bool
		th bool
	}{
		"claude-sonnet-4.5":                {"claude-sonnet-4.5", false, false},
		"claude-opus-4.7-thinking-agentic": {"claude-opus-4.7", true, true},
		"claude-sonnet-5-agentic":          {"claude-sonnet-5", true, false},
		"gpt-5.6-terra-thinking":           {"gpt-5.6-terra", false, true},
		"MiniMax-M2.5":                     {"minimax-m2.5", false, false},
		"claude-sonnet-4-5-20250929":       {"claude-sonnet-4.5", false, false},
		"claude-opus-5.5-thinking":         {"claude-opus-5.5", false, true},
		"something-new":                    {"something-new", false, false},
	} {
		up, ag, th := resolveModel(in)
		if up != want.up || ag != want.ag || th != want.th {
			t.Errorf("%s → %s %v %v, want %+v", in, up, ag, th, want)
		}
	}

	first := func(model, body string) string {
		p := payload(t, model, body, nil)
		return dig(t, p, "conversationState", "currentMessage", "userInputMessage", "content").(string)
	}
	msg := `"messages":[{"role":"user","content":"hi"}]`
	if c := first("claude-sonnet-4.5-thinking", "{"+msg+"}"); c != "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>16000</max_thinking_length>\n\nhi" {
		t.Errorf("thinking prefix = %q", c)
	}
	if c := first("claude-sonnet-4.5", `{"reasoning_effort":"medium",`+msg+"}"); !strings.Contains(c, "<max_thinking_length>8192<") {
		t.Errorf("effort budget = %q", c)
	}
	if c := first("claude-sonnet-4.5", `{"thinking":{"type":"enabled","budget_tokens":99999},`+msg+"}"); !strings.Contains(c, "<max_thinking_length>32000<") {
		t.Errorf("budget clamp = %q", c)
	}
	if c := first("claude-sonnet-4.5-thinking", `{"reasoning_effort":"none",`+msg+"}"); c != "hi" {
		t.Errorf("explicit off must win over the suffix: %q", c)
	}
	if c := first("claude-sonnet-4.5", "{"+msg+"}"); c != "hi" {
		t.Errorf("plain model got a prefix: %q", c)
	}
	if c := first("claude-sonnet-4.5-agentic", "{"+msg+"}"); !strings.HasPrefix(c, "# CRITICAL: CHUNKED WRITE PROTOCOL") || !strings.HasSuffix(c, "\n\nhi") {
		t.Errorf("agentic prompt = %.60q", c)
	}
	// the prefix goes into the first turn, not the current one, once there is history
	p := payload(t, "claude-sonnet-4.5-thinking", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`, nil)
	if c := dig(t, p, "conversationState", "history", 0, "userInputMessage", "content").(string); !strings.HasPrefix(c, "<thinking_mode>") {
		t.Errorf("first turn = %q", c)
	}
}

func TestEndpointsAndRequest(t *testing.T) {
	data := map[string]any{"region": "eu-central-1"}
	eps := endpoints(defaultBase, data)
	if len(eps) != 3 || eps[0] != "https://q.eu-central-1.amazonaws.com/generateAssistantResponse" || !strings.HasPrefix(eps[1], "https://codewhisperer.us-east-1.") || !strings.Contains(eps[2], "kiro.dev") {
		t.Errorf("endpoints = %v", eps)
	}
	if eps = endpoints(defaultBase, map[string]any{"region": "evil.com/x"}); eps[0] != "https://q.us-east-1.amazonaws.com/generateAssistantResponse" {
		t.Errorf("an unknown region must not reach a host name: %v", eps)
	}
	if eps = endpoints("http://mirror.test", nil); len(eps) != 1 || eps[0] != "http://mirror.test/generateAssistantResponse" {
		t.Errorf("custom base = %v", eps)
	}

	req, err := request(context.Background(), provider.NativeCall{URL: eps[0], Token: "tok", Model: "m",
		Data: map[string]any{"authMethod": "external_idp", "profileArn": "arn:y"}, Body: chat(t, `{"messages":[{"role":"user","content":"hi"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	h := req.Header
	if h.Get("Authorization") != "Bearer tok" || h.Get("TokenType") != "EXTERNAL_IDP" || h.Get("x-amzn-codewhisperer-profile-arn") != "arn:y" ||
		h.Get("Accept") != "application/vnd.amazon.eventstream" || h.Get("X-Amz-Target") != "" {
		t.Errorf("headers = %v", h)
	}
	req, _ = request(context.Background(), provider.NativeCall{URL: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse", Token: "t", Body: chat(t, `{}`)})
	if req.Header.Get("X-Amz-Target") != codewhispererTarget {
		t.Errorf("codewhisperer host without target header: %v", req.Header)
	}
}
