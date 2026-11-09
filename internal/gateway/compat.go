package gateway

import (
	"regexp"
	"strings"

	"router-go/internal/db"
)

// Request fix-ups for chat-format bodies that specific upstreams reject. They key on the model name or the upstream
// host, so they also apply to a custom node that points at one of these services.

var (
	deepseekRe = regexp.MustCompile(`(?i)deepseek`)
	kimiRe     = regexp.MustCompile(`(?i)^kimi-`)
)

// bodyFix returns the function that adapts a chat-format body for node/model, or nil when nothing applies.
func (g *Gateway) bodyFix(node *db.Node, model string) func(map[string]any) {
	var fixes []func(map[string]any)
	if p, ok := node.Builtin(); ok && p.Prepare != nil {
		fixes = append(fixes, p.Prepare)
	}
	host := strings.ToLower(node.BaseURL())
	if compatApplies(model, host) {
		fixes = append(fixes, func(b map[string]any) { compat(b, model, host) })
	}
	if len(fixes) == 0 {
		return nil // most requests: no body copy needed
	}
	return func(b map[string]any) {
		for _, f := range fixes {
			f(b)
		}
	}
}

func compatApplies(model, host string) bool {
	if deepseekRe.MatchString(model) || kimiRe.MatchString(model) {
		return true
	}
	for _, h := range []string{"groq.com", "mistral.ai", "cerebras.ai", "nvidia.com", "api.openai.com"} {
		if strings.Contains(host, h) {
			return true
		}
	}
	return false
}

func compat(body map[string]any, model, host string) {
	// DeepSeek / Kimi thinking modes demand reasoning_content on assistant history; clients in OpenAI format never send it.
	switch {
	case deepseekRe.MatchString(model):
		eachMessage(body, func(m map[string]any) { injectReasoning(m, false) })
	case kimiRe.MatchString(model):
		eachMessage(body, func(m map[string]any) { injectReasoning(m, true) })
	}
	switch {
	case strings.Contains(host, "groq.com") || strings.Contains(host, "mistral.ai") || strings.Contains(host, "cerebras.ai"):
		// strict validators answer 400/422 on the reasoning fields clients echo back
		eachMessage(body, func(m map[string]any) {
			delete(m, "reasoning_content")
			delete(m, "reasoning")
			delete(m, "reasoning_details")
		})
	case strings.Contains(host, "nvidia.com"): // reasoning and reasoning_content are one field there: sending both is a duplicate
		eachMessage(body, func(m map[string]any) {
			if v, ok := m["reasoning"]; ok {
				if _, has := m["reasoning_content"]; !has {
					m["reasoning_content"] = v
				}
				delete(m, "reasoning")
			}
		})
	case strings.Contains(host, "api.openai.com"): // current models reject max_tokens
		if v, ok := body["max_tokens"]; ok {
			if _, has := body["max_completion_tokens"]; !has {
				body["max_completion_tokens"] = v
			}
			delete(body, "max_tokens")
		}
	}
}

// injectReasoning adds a placeholder reasoning_content to an assistant message that lacks one (toolCallsOnly: only
// when the message carries tool calls).
func injectReasoning(m map[string]any, toolCallsOnly bool) {
	if m["role"] != "assistant" {
		return
	}
	if rc, _ := m["reasoning_content"].(string); rc != "" {
		return
	}
	if toolCallsOnly {
		if tc, _ := m["tool_calls"].([]any); len(tc) == 0 {
			return
		}
	}
	m["reasoning_content"] = " "
}

func eachMessage(body map[string]any, fn func(map[string]any)) {
	switch list := body["messages"].(type) {
	case []any:
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				fn(m)
			}
		}
	case []map[string]any:
		for _, m := range list {
			fn(m)
		}
	}
}
