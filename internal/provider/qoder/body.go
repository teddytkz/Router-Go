package qoder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// defaultMaxTokens is what qodercli asks for when the model block does not say.
const defaultMaxTokens = 32768

// qoderPayload is the agent_chat_generation request qodercli sends, built from a chat-format body: system text is
// hoisted out of the messages array (the endpoint rejects a system role), ids are stable per conversation so the
// upstream keeps queue and context state, and the model's own model_config block is echoed back verbatim (a wrong
// block silently downgrades the model).
func qoderPayload(model string, body, psd map[string]any, cfg map[string]any) map[string]any {
	key := strings.TrimPrefix(model, "qoder/")
	systemText, messages := normalizeMessages(body["messages"])
	tools, _ := body["tools"].([]any)
	if tools == nil {
		tools = []any{}
	}
	maxTokens := defaultMaxTokens
	if n := num(cfg["max_output_tokens"]); n > 0 {
		maxTokens = int(n)
	}
	for _, f := range []string{"max_tokens", "max_completion_tokens"} {
		if n := num(body[f]); n > 0 && int(n) < maxTokens {
			maxTokens = int(n)
		}
	}
	last := lastUserText(messages)
	record := recordID(key, messages, tools, maxTokens)
	session := stableID("qoder-session", psd["userId"], key)
	reasoning, _ := cfg["is_reasoning"].(bool)
	return map[string]any{
		"request_id":       uuid.NewString(),
		"request_set_id":   record,
		"chat_record_id":   record,
		"session_id":       session,
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"code_language":    "",
		"chat_prompt":      "",
		"image_urls":       nil,
		"aliyun_user_type": "",
		"system":           systemText,
		"messages":         messages,
		"tools":            tools,
		"parameters":       map[string]any{"max_tokens": maxTokens},
		"chat_context": map[string]any{
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     map[string]any{"key": key, "is_reasoning": reasoning},
				"originalContent": last,
			},
			"features": []any{},
			"text":     last,
		},
		"model_config": cfg,
		"business": map[string]any{
			"product": "cli", "version": "1.0.0", "type": "agent", "stage": "start",
			"id": uuid.NewString(), "name": truncate(last, 30), "begin_at": nowMillis(),
		},
	}
}

// normalizeMessages pulls system messages out (joined into systemText) and flattens every other message's content.
func normalizeMessages(v any) (string, []any) {
	list, _ := v.([]any)
	var system []string
	out := make([]any, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "system" {
			if t := flattenText(m["content"]); t != "" {
				system = append(system, t)
			}
			continue
		}
		c := make(map[string]any, len(m))
		for k, val := range m {
			c[k] = val
		}
		c["content"] = normalizeContent(m["content"])
		out = append(out, c)
	}
	return strings.Join(system, "\n\n"), out
}

// normalizeContent keeps text as a plain string unless images are present; file blocks become a one-line stub so a
// large document is never inlined into the agent request.
func normalizeContent(v any) any {
	switch c := v.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		var text []string
		blocks := make([]any, 0, len(c))
		images := false
		for _, item := range c {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := b["type"].(string)
			if url := imageURL(b); url != "" {
				blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
				images = true
				continue
			}
			switch typ {
			case "file":
				name := "file"
				if f, ok := b["file"].(map[string]any); ok {
					if n, ok := f["filename"].(string); ok && n != "" {
						name = n
					}
				}
				text = append(text, "[file omitted: "+name+" — Qoder reads documents through its file API, not inlined bytes]")
			case "document":
				name, _ := b["title"].(string)
				if name == "" {
					name = "document"
				}
				text = append(text, "[file omitted: "+name+" — Qoder reads documents through its file API, not inlined bytes]")
			default:
				if s, ok := b["text"].(string); ok && s != "" {
					text = append(text, s)
				}
			}
		}
		if !images {
			return strings.Join(text, "\n")
		}
		if len(text) > 0 {
			blocks = append([]any{map[string]any{"type": "text", "text": strings.Join(text, "\n")}}, blocks...)
		}
		return blocks
	default:
		b, _ := json.Marshal(c)
		return string(b)
	}
}

// imageURL reads an OpenAI image_url block, a Claude {type:image, source:{…}} block, or a bare image_url string.
func imageURL(b map[string]any) string {
	if u, ok := b["image_url"].(string); ok {
		return u
	}
	if m, ok := b["image_url"].(map[string]any); ok {
		if u, _ := m["url"].(string); ok {
			return u
		}
	}
	if src, ok := b["source"].(map[string]any); ok {
		if u, _ := src["url"].(string); ok && u != "" {
			return u
		}
		if data, _ := src["data"].(string); ok && data != "" {
			mt, _ := src["media_type"].(string)
			if mt == "" {
				mt = "image/png"
			}
			return "data:" + mt + ";base64," + data
		}
	}
	return ""
}

func flattenText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if b, ok := item.(map[string]any); ok {
				if s, ok := b["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		return flattenText(normalizeContent(c))
	}
}

func lastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m, _ := messages[i].(map[string]any)
		if role, _ := m["role"].(string); role != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			return flattenText(c)
		}
		return ""
	}
	return ""
}

// stableID is a short fingerprint, so the same conversation keeps the same upstream ids across requests.
func stableID(prefix string, parts ...any) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		h.Write([]byte{0})
		if s, ok := p.(string); ok {
			h.Write([]byte(s))
		} else if p != nil {
			b, _ := json.Marshal(p)
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// recordID: same prompt + tools + budget → same chat_record_id, so a retry continues the same upstream record.
func recordID(model string, messages, tools []any, maxTokens int) string {
	h := sha256.New()
	h.Write([]byte("qoder-record\x00" + model))
	for _, item := range messages {
		m, _ := item.(map[string]any)
		if role, _ := m["role"].(string); role != "" {
			h.Write([]byte{0})
			h.Write([]byte(role))
		}
		switch c := m["content"].(type) {
		case string:
			if c != "" {
				h.Write([]byte{0})
				h.Write([]byte(c))
			}
		default:
			if c != nil {
				b, _ := json.Marshal(c)
				h.Write([]byte{0})
				h.Write(b)
			}
		}
	}
	if b, err := json.Marshal(tools); err == nil {
		h.Write([]byte{0})
		h.Write(b)
	}
	h.Write([]byte("\x00mt=" + itoa(maxTokens)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var multiNewline = regexp.MustCompile(`\s+`)

func truncate(s string, n int) string {
	s = multiNewline.ReplaceAllString(strings.TrimSpace(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
