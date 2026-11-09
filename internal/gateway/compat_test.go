package gateway

import (
	"encoding/json"
	"testing"

	"router-go/internal/provider"
	_ "router-go/internal/provider/codebuddy"
)

func msgs(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	raw, _ := json.Marshal(body["messages"])
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompatFixes(t *testing.T) {
	mk := func() map[string]any {
		return map[string]any{"max_tokens": 5, "messages": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": "x", "reasoning": "r", "tool_calls": []any{map[string]any{"id": "1"}}},
			map[string]any{"role": "assistant", "content": "y"},
		}}
	}
	b := mk()
	compat(b, "deepseek-v4-pro", "https://api.deepseek.com/v1")
	if m := msgs(t, b); m[1]["reasoning_content"] != " " || m[2]["reasoning_content"] != " " {
		t.Errorf("deepseek: %v", m)
	}
	b = mk()
	compat(b, "kimi-k2.5", "https://x/v1")
	if m := msgs(t, b); m[1]["reasoning_content"] != " " || m[2]["reasoning_content"] != nil {
		t.Errorf("kimi only injects on tool-call turns: %v", m)
	}
	b = mk()
	compat(b, "llama", "https://api.groq.com/openai/v1")
	if m := msgs(t, b); m[1]["reasoning"] != nil {
		t.Errorf("groq keeps reasoning fields: %v", m)
	}
	b = mk()
	compat(b, "gpt-5", "https://api.openai.com/v1")
	if b["max_completion_tokens"] != 5 || b["max_tokens"] != nil {
		t.Errorf("openai rename: %v", b)
	}
	if compatApplies("llama", "https://example.com/v1") {
		t.Error("nothing should apply to an unknown model/host")
	}
}

func TestCodeBuddyLeadingSystemMessage(t *testing.T) {
	p, _ := provider.ByID("codebuddy-intl")
	b := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "system", "content": "Be brief."},
		map[string]any{"role": "developer", "content": []any{map[string]any{"type": "text", "text": "No emojis."}}},
		map[string]any{"role": "assistant", "content": "ok"},
	}}
	p.Prepare(b)
	m := msgs(t, b)
	if len(m) != 3 || m[0]["role"] != "system" || m[0]["content"] != "Be brief.\n\nNo emojis." || m[2]["role"] != "assistant" {
		t.Fatalf("messages = %v", m)
	}
	if c, _ := m[1]["content"].([]any); len(c) != 1 || c[0].(map[string]any)["text"] != "hello" {
		t.Errorf("user text not typed: %v", m[1])
	}
	none := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "x"}}}
	p.Prepare(none)
	if m := msgs(t, none); m[0]["role"] != "system" || m[0]["content"] == "" {
		t.Errorf("default system prompt missing: %v", m)
	}
}
