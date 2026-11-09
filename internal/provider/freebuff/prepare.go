package freebuff

import "strings"

// marker opens the CLI's root system prompt; the backend expects it to be there.
const marker = "You are Buffy, the strategic coding assistant."

// openings are the root prompts a request may already carry; one of them means the marker is not needed.
var openings = []string{
	marker,
	"You are Buffy, the Freebuff Cloud project planner.",
	"You are Buffy, a strategic assistant that orchestrates complex coding tasks through specialized sub-agents.",
}

var endTurn = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "end_turn",
		"description": "Signal the end of the current task.",
		"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

// prepare shapes a chat body the way the CLI sends it: the Buffy marker on the system prompt, the end_turn tool
// alongside caller tools, and no reasoning controls (the backend rejects them).
func prepare(body map[string]any) {
	delete(body, "reasoning")
	delete(body, "reasoning_effort")
	markerOn(body)
	endTurnOn(body)
}

func markerOn(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		body["messages"] = []any{map[string]any{"role": "system", "content": marker}}
		return
	}
	first, _ := msgs[0].(map[string]any)
	if text, ok := first["content"].(string); ok && first["role"] == "system" {
		trimmed := strings.TrimLeft(text, " \t\r\n")
		for _, o := range openings {
			if strings.HasPrefix(trimmed, o) {
				return
			}
		}
		first["content"] = marker + "\n\n" + text
		return
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": marker}}, msgs...)
}

func endTurnOn(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		return
	}
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok && toolName(m) == "end_turn" {
			return
		}
	}
	body["tools"] = append(tools, endTurn)
}

func toolName(t map[string]any) string {
	if fn, ok := t["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return n
		}
	}
	n, _ := t["name"].(string)
	return n
}
