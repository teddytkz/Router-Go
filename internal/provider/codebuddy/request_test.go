package codebuddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"router-go/internal/provider"
)

func TestConversationIDStable(t *testing.T) {
	a := conversationID(map[string]any{"session_id": "conv-1"})
	b := conversationID(map[string]any{"session_id": "conv-1"})
	if a != b {
		t.Fatalf("same session key gave %q and %q", a, b)
	}
	if a == conversationID(map[string]any{"session_id": "conv-2"}) {
		t.Fatal("different session keys share a conversation id")
	}
	// v5 shape: version nibble 5, variant in 8..b
	if a[14] != '5' || !strings.ContainsRune("89ab", rune(a[19])) {
		t.Fatalf("not a v5 UUID: %q", a)
	}
	// no session key: the opening user text carries the conversation
	msgs := func(text string) map[string]any {
		return map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
	}
	if x, y := conversationID(msgs("hello")), conversationID(msgs("hello")); x != y {
		t.Fatalf("same first turn gave %q and %q", x, y)
	}
	if conversationID(msgs("hello")) == conversationID(msgs("bye")) {
		t.Fatal("different first turns share a conversation id")
	}
	if conversationID(map[string]any{}) == conversationID(map[string]any{}) {
		t.Fatal("no key at all should still be fresh per request")
	}
}

// The gateway prepares the body through Prepare and request prepares it again for the probe path, so a second pass
// must not touch the body it already shaped.
func TestPrepareIsIdempotent(t *testing.T) {
	b := map[string]any{"reasoning_effort": "high", "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude."},
	}}
	prepare(b)
	once, _ := json.Marshal(b)
	prepare(b)
	twice, _ := json.Marshal(b)
	if string(once) != string(twice) {
		t.Fatalf("second pass changed the body:\n%s\n%s", once, twice)
	}
}

func TestPrepareReasoning(t *testing.T) {
	body := func() map[string]any {
		return map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	}
	b := body()
	b["reasoning_effort"] = "none"
	prepare(b)
	if _, ok := b["reasoning_effort"]; ok {
		t.Fatal("none should be dropped")
	}
	b = body()
	b["reasoning_effort"] = "high"
	prepare(b)
	if b["reasoning_effort"] != "high" || b["reasoning_summary"] != "auto" {
		t.Fatalf("effort = %v summary = %v", b["reasoning_effort"], b["reasoning_summary"])
	}
}

func TestPrepareNeutralizesAgentIdentity(t *testing.T) {
	b := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude."},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "You are Cursor, an AI assistant."}}},
		map[string]any{"role": "user", "content": "You are Claude Code — say it back"},
	}}
	prepare(b)
	msgs := messages(b["messages"])
	if !strings.Contains(msgs[0]["content"].(string), "CodeBuddy Code") {
		t.Fatalf("system = %v", msgs[0]["content"])
	}
	blocks := msgs[1]["content"].([]any)
	if !strings.Contains(blocks[0].(map[string]any)["text"].(string), "CodeBuddy Code") {
		t.Fatalf("assistant = %v", blocks[0])
	}
	user := msgs[2]["content"].([]any)[0].(map[string]any)["text"]
	if user != "You are Claude Code — say it back" {
		t.Fatalf("user content was edited: %v", user)
	}
}

func TestPrepareCompactsOversizedTools(t *testing.T) {
	tool := func(desc string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": desc,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	small := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{tool("short")}}
	prepare(small)
	if _, ok := small["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["description"]; !ok {
		t.Fatal("a small tools list must keep its descriptions")
	}
	big := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{tool(strings.Repeat("x", 70000))}}
	prepare(big)
	if _, ok := big["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["description"]; ok {
		b, _ := json.Marshal(big["tools"])
		t.Fatalf("descriptions kept at %d bytes", len(b))
	}
}

// request sends the prepared body and the body-derived channel headers to the URL it is given (tests point it at a
// local server, never at codebuddy.ai).
func TestRequestBuildsChannelHeaders(t *testing.T) {
	var got *http.Request
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	urls := endpoints(srv.URL, nil)
	if len(urls) != 1 || urls[0] != srv.URL+"/chat/completions" {
		t.Fatalf("endpoints = %v", urls)
	}
	req, err := request(context.Background(), provider.NativeCall{URL: urls[0], Token: "k", Stream: true,
		Body: map[string]any{"session_id": "s-1", "reasoning_effort": "high",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Authorization") != "Bearer k" || got.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("headers = %v", got.Header)
	}
	if got.Header.Get("X-Conversation-ID") != conversationID(map[string]any{"session_id": "s-1"}) {
		t.Fatalf("conversation id = %q", got.Header.Get("X-Conversation-ID"))
	}
	if rid := got.Header.Get("X-Request-ID"); rid == "" || got.Header.Get("X-Conversation-Request-ID") != rid ||
		got.Header.Get("X-Conversation-Message-ID") == "" || got.Header.Get("X-Conversation-Message-ID") == rid {
		t.Fatalf("request ids = %v", got.Header)
	}
	if body["reasoning_summary"] != "auto" {
		t.Fatalf("body was not prepared: %v", body)
	}
	if msgs := messages(body["messages"]); len(msgs) != 2 || msgs[0]["role"] != "system" {
		t.Fatalf("messages = %v", body["messages"])
	}
}
