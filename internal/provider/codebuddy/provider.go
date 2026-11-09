// Package codebuddy is the built-in codebuddy-intl provider.
package codebuddy

import (
	"encoding/json"
	"strings"

	"router-go/internal/provider"
)

func init() {
	provider.Register(provider.Provider{
		Priority: 2,
		ID:       "codebuddy-intl", Alias: "cbai", Name: "CodeBuddy", Category: provider.CategoryOAuth, Status: provider.StatusExperimental,
		Website: "https://www.codebuddy.ai", AuthHint: "CodeBuddy API key",
		Note:    "OpenAI-compatible over the official channel: the body carries the leading system message CodeBuddy requires, rival-CLI identity strings are neutralized, and the X-Conversation-* headers are derived from the conversation like the IDE's. Untested against a live account here: the upstream may still refuse callers it does not recognise.",
		BaseURL: "https://www.codebuddy.ai/v2", ForceStream: true, Prepare: prepare, Native: native,
		Headers: map[string]string{
			"User-Agent":          "IDE/2.108.1 CodeBuddy/2.108.1",
			"X-Product":           "SaaS",
			"X-IDE-Type":          "IDE",
			"X-IDE-Name":          "IDE",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
		OAuthBase: "https://www.codebuddy.ai", OAuth: oauth, Usage: usage,
		Models: []provider.Model{
			{ID: "glm-5.2", Name: "GLM-5.2"}, {ID: "glm-5.1", Name: "GLM-5.1"}, {ID: "glm-5.0", Name: "GLM-5.0"}, {ID: "glm-5.0-turbo", Name: "GLM-5.0-Turbo"},
			{ID: "glm-5v-turbo", Name: "GLM-5v-Turbo"}, {ID: "glm-4.7", Name: "GLM-4.7"}, {ID: "minimax-m3", Name: "MiniMax-M3"}, {ID: "minimax-m2.7", Name: "MiniMax-M2.7"},
			{ID: "kimi-k2.7", Name: "Kimi-K2.7-Code"}, {ID: "kimi-k2.6", Name: "Kimi-K2.6"}, {ID: "kimi-k2.5", Name: "Kimi-K2.5"}, {ID: "hy3-preview", Name: "Hy3 Preview"},
			{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro"}, {ID: "deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash"}, {ID: "deepseek-v3-2-volc", Name: "DeepSeek-V3.2"},
		},
	})
}

// prepare gives the body the shape CodeBuddy insists on (otherwise 11101 / 11128 "first message is not system prompt"),
// neutralizes rival-CLI identity strings the channel check treats as unauthorized tooling, and keeps the tools
// metadata under the WAF's size limit. The client's own wording is kept wherever the rules allow.
func prepare(body map[string]any) {
	fixReasoning(body)
	var system []string
	var rest []any
	for _, m := range messages(body["messages"]) {
		switch m["role"] {
		case "system", "developer":
			t := text(m["content"])
			if agentIdentityRe.MatchString(t) { // a rival CLI's whole prompt: swapping it out is safer than editing it
				t = neutralPrompt
			}
			if t = neutralize(t, "system"); t != "" {
				system = append(system, t)
			}
			continue
		case "assistant":
			if s, ok := m["content"].(string); ok {
				m["content"] = neutralize(s, "assistant")
			} else {
				neutralizeBlocks(m)
			}
		case "user":
			if s, ok := m["content"].(string); ok {
				m["content"] = []any{map[string]any{"type": "text", "text": s}}
			}
		}
		rest = append(rest, m)
	}
	prompt := strings.Join(system, "\n\n")
	if prompt == "" {
		prompt = "You are CodeBuddy Code."
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, rest...)
	compactTools(body)
}

// fixReasoning mirrors the IDE: "none"/"off" is dropped, any other level asks for a reasoning summary.
func fixReasoning(body map[string]any) {
	eff, _ := body["reasoning_effort"].(string)
	switch {
	case eff == "none" || eff == "off":
		delete(body, "reasoning_effort")
	case eff != "":
		body["reasoning_summary"] = "auto"
	}
}

func neutralizeBlocks(m map[string]any) {
	blocks, _ := m["content"].([]any)
	for _, b := range blocks {
		if part, ok := b.(map[string]any); ok {
			if s, ok := part["text"].(string); ok {
				part["text"] = neutralize(s, "assistant")
			}
		}
	}
}

// compactTools strips tool descriptions once the metadata passes the channel's 64KB limit.
func compactTools(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		return
	}
	if b, err := json.Marshal(tools); err != nil || len(b) < maxToolsBytes {
		return
	}
	for _, t := range tools {
		if tool, ok := t.(map[string]any); ok {
			if fn, ok := tool["function"].(map[string]any); ok {
				delete(fn, "description")
			}
		}
	}
}

func messages(v any) []map[string]any {
	var out []map[string]any
	switch list := v.(type) {
	case []map[string]any:
		return list
	case []any:
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// text is the plain text of a message content: a string, or the text parts of a typed array.
func text(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if m, ok := p.(map[string]any); ok {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
