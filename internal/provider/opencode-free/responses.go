package opencodefree

import (
	"encoding/json"
	"strings"
)

// responsesFixes gives the Responses lane the shape it validates strictly (mirrored from 9router's executor):
// stateless, streamed, one output-token field, no prior reasoning items, and a tool_choice that is never "none".
func responsesFixes(body map[string]any) {
	body["stream"] = true
	body["store"] = false
	delete(body, "stream_options") // not a Responses-API field; the lane rejects unknown keys
	if body["max_output_tokens"] == nil {
		if v := body["max_completion_tokens"]; v != nil {
			body["max_output_tokens"] = v
		} else if v := body["max_tokens"]; v != nil {
			body["max_output_tokens"] = v
		}
	}
	delete(body, "max_tokens")
	delete(body, "max_completion_tokens")
	normalizeInput(body)
	sanitizeItems(body)
	normalizeTools(body)
	if body["tool_choice"] == nil {
		body["tool_choice"] = "auto" // upstream answers "none" with 403 FreeTierError
	}
}

// normalizeInput replaces a plain-string or empty input with the item shape the Responses API requires.
func normalizeInput(body map[string]any) {
	placeholder := func() []any {
		return []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "..."}}}}
	}
	switch in := body["input"].(type) {
	case string:
		text := in
		if strings.TrimSpace(text) == "" {
			text = "..."
		}
		body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
	case []any:
		if len(in) == 0 {
			body["input"] = placeholder()
		}
	}
	if asList(body["input"]) == nil {
		body["input"] = placeholder()
	}
}

const maxCallIDLen = 64

// sanitizeItems drops prior reasoning items and clamps what upstream validates. The lane uses pooled public
// credentials, so a reasoning item's encrypted_content was issued to another account and cannot be replayed.
func sanitizeItems(body map[string]any) {
	items := asList(body["input"])
	out := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			out = append(out, it)
			continue
		}
		if m["type"] == "reasoning" {
			continue
		}
		delete(m, "encrypted_content")
		delete(m, "reasoning_encrypted_content")
		switch m["type"] {
		case "function_call":
			name := strings.TrimSpace(str(m["name"]))
			if name == "" {
				continue
			}
			m["name"] = name
			m["call_id"] = clampCallID(m["call_id"])
			m["arguments"] = coerceArguments(m["arguments"])
		case "function_call_output":
			m["call_id"] = clampCallID(m["call_id"])
			m["output"] = coerceOutput(m["output"])
		}
		out = append(out, m)
	}
	body["input"] = out
}

func clampCallID(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "call_" + randBase62(20)
	}
	if len(s) > maxCallIDLen {
		return s[:maxCallIDLen]
	}
	return s
}

// coerceArguments single-stringifies: a JSON object is encoded once, a valid JSON string passes through, anything
// else becomes "{}" rather than being double-encoded.
func coerceArguments(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case string:
		if x == "" || !json.Valid([]byte(x)) {
			return "{}"
		}
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// coerceOutput makes function_call_output.output a string; text parts are joined.
func coerceOutput(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, c := range x {
			if m, ok := c.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
					continue
				}
			}
			if raw, err := json.Marshal(c); err == nil {
				b.Write(raw)
			}
		}
		return b.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// normalizeTools moves tools onto the flat Responses shape, drops unnamed ones, and forgets a choice that points at
// one it dropped.
func normalizeTools(body map[string]any) {
	tools := asList(body["tools"])
	if tools == nil {
		return
	}
	out := make([]any, 0, len(tools))
	names := map[string]bool{}
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := m["function"].(map[string]any); ok { // the chat shape translate hands us
			m = map[string]any{"type": "function", "name": fn["name"], "description": fn["description"],
				"parameters": fn["parameters"], "strict": fn["strict"]}
		}
		name, _ := m["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if p, ok := m["parameters"].(map[string]any); ok && p["type"] == "object" && p["properties"] == nil {
			p["properties"] = map[string]any{}
		}
		delete(m, "function")
		names[name] = true
		out = append(out, m)
	}
	body["tools"] = out
	if tc, ok := body["tool_choice"].(map[string]any); ok && tc["type"] == "function" {
		if n, _ := tc["name"].(string); !names[n] {
			delete(body, "tool_choice")
		}
	}
}
