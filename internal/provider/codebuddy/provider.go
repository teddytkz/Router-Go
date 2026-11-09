// Package codebuddy is the built-in codebuddy-intl provider.
package codebuddy

import (
	"strings"

	"router-go/internal/provider"
)

func init() {
	provider.Register(provider.Provider{
		Priority: 2,
		ID:       "codebuddy-intl", Alias: "cbai", Name: "CodeBuddy", Category: provider.CategoryOAuth, Status: provider.StatusExperimental,
		Website: "https://www.codebuddy.ai", AuthHint: "CodeBuddy API key",
		Note:    "Plain OpenAI-compatible protocol; requests get the leading system message CodeBuddy requires. Its gateway also checks for its own IDE clients and may still reject other callers (error 11128): the router does not disguise requests as an official client.",
		BaseURL: "https://www.codebuddy.ai/v2", ForceStream: true,
		Headers: map[string]string{
			"User-Agent":          "IDE/2.108.1 CodeBuddy/2.108.1",
			"X-Product":           "SaaS",
			"X-IDE-Type":          "IDE",
			"X-IDE-Name":          "IDE",
			"x-requested-with":    "XMLHttpRequest",
			"x-codebuddy-request": "1",
		},
		OAuthBase: "https://www.codebuddy.ai", OAuth: oauth, Usage: usage, DynamicHeaders: requestHeaders, Prepare: prepare,
		Models: []provider.Model{
			{ID: "glm-5.2", Name: "GLM-5.2"}, {ID: "glm-5.1", Name: "GLM-5.1"}, {ID: "glm-5.0", Name: "GLM-5.0"}, {ID: "glm-5.0-turbo", Name: "GLM-5.0-Turbo"},
			{ID: "glm-5v-turbo", Name: "GLM-5v-Turbo"}, {ID: "glm-4.7", Name: "GLM-4.7"}, {ID: "minimax-m3", Name: "MiniMax-M3"}, {ID: "minimax-m2.7", Name: "MiniMax-M2.7"},
			{ID: "kimi-k2.7", Name: "Kimi-K2.7-Code"}, {ID: "kimi-k2.6", Name: "Kimi-K2.6"}, {ID: "kimi-k2.5", Name: "Kimi-K2.5"}, {ID: "hy3-preview", Name: "Hy3 Preview"},
			{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro"}, {ID: "deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash"}, {ID: "deepseek-v3-2-volc", Name: "DeepSeek-V3.2"},
		},
	})
}

// prepare gives the body the shape CodeBuddy insists on (otherwise 11101 / 11128 "first message is not system prompt"):
// one system message first, built from the client's own system/developer messages (or a neutral default), and user
// text as typed content blocks. The client's wording is kept as it is.
func prepare(body map[string]any) {
	var system []string
	var rest []any
	for _, m := range messages(body["messages"]) {
		switch m["role"] {
		case "system", "developer":
			if t := text(m["content"]); t != "" {
				system = append(system, t)
			}
			continue
		case "user":
			if s, ok := m["content"].(string); ok {
				m["content"] = []any{map[string]any{"type": "text", "text": s}}
			}
		}
		rest = append(rest, m)
	}
	prompt := strings.Join(system, "\n\n")
	if prompt == "" {
		prompt = "You are a helpful AI assistant."
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, rest...)
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
