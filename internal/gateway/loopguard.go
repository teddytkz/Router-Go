package gateway

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Loop guard: stateless detection of an agent stuck in a loop, from the conversation history in the request itself.
// Thresholds and hints are those of XRouter-Next (open-sse/utils/loopGuard.js).
const (
	singleRepeat    = 3  // same tool+args at least this many times
	sequenceRepeat  = 2  // same run of tool calls at least this many times
	minSequenceLen  = 2  // shortest run considered
	textMsgRepeat   = 3  // same assistant message
	textSentRepeat  = 3  // same sentence across assistant messages
	minTextLen      = 12 // ignore fragments shorter than this
	hintSingle      = "You have called the same tool with identical arguments multiple times with no new progress. STOP repeating. Summarize findings from existing results or change your strategy."
	hintSequence    = "You have repeated the same sequence of tool calls multiple times. This is a loop. STOP this pattern immediately. Summarize what you have already found or take a completely different approach."
	hintTextMessage = "You have repeated the same response multiple times without making progress. This is a text loop — you are NOT moving forward. STOP repeating yourself. Either call a tool to act, or give your final answer now with the information you already have."
	hintTextSent    = "You keep repeating the same planning statement without acting on it. STOP planning in circles. Either execute a tool call NOW, or provide your final answer with current knowledge. Do not restate your plan again."
	routerNote      = "[ROUTER NOTE:"
)

func normArgs(v any) string {
	if s, ok := v.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) != nil {
			return s
		}
		v = parsed
	}
	b, _ := json.Marshal(v) // map keys are marshalled sorted, so {b,a} == {a,b}
	return string(b)
}

// history pulls the ordered tool-call hashes and assistant texts out of the body, whatever its wire format.
func history(body map[string]any, format string) (calls, texts []string) {
	addText := func(t string) {
		if len(t) >= minTextLen {
			texts = append(texts, t)
		}
	}
	switch format {
	case "responses":
		items, _ := body["input"].([]any)
		for _, raw := range items {
			it, _ := raw.(map[string]any)
			switch it["type"] {
			case "function_call":
				calls = append(calls, str(it["name"])+"::"+normArgs(it["arguments"]))
			case "message":
				if it["role"] == "assistant" {
					addText(contentText(it["content"]))
				}
			}
		}
	default: // chat and messages share the messages[] envelope
		msgs, _ := body["messages"].([]any)
		for _, raw := range msgs {
			m, _ := raw.(map[string]any)
			if m["role"] != "assistant" {
				continue
			}
			addText(contentText(m["content"]))
			tcs, _ := m["tool_calls"].([]any)
			for _, tc := range tcs {
				t, _ := tc.(map[string]any)
				fn, _ := t["function"].(map[string]any)
				calls = append(calls, str(fn["name"])+"::"+normArgs(fn["arguments"]))
			}
			blocks, _ := m["content"].([]any)
			for _, b := range blocks {
				if bm, _ := b.(map[string]any); bm["type"] == "tool_use" {
					calls = append(calls, str(bm["name"])+"::"+normArgs(bm["input"]))
				}
			}
		}
	}
	return calls, texts
}

func str(v any) string { s, _ := v.(string); return s }

func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, p := range v {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

var (
	spaceRe    = regexp.MustCompile(`\s+`)
	trailPunct = regexp.MustCompile(`[.!?…]+$`)
	sentSplit  = regexp.MustCompile(`[\n.!?…]+`)
)

func normText(s string) string {
	return strings.TrimSpace(trailPunct.ReplaceAllString(spaceRe.ReplaceAllString(strings.ToLower(s), " "), ""))
}

// detectLoop returns the correction hint, or "" when no loop is found.
func detectLoop(body map[string]any, format string) string {
	calls, texts := history(body, format)
	if len(calls) >= singleRepeat {
		counts := map[string]int{}
		for _, c := range calls {
			if counts[c]++; counts[c] >= singleRepeat {
				return hintSingle
			}
		}
		if repeatedRun(calls) {
			return hintSequence
		}
	}
	if len(texts) < textMsgRepeat {
		return ""
	}
	msgCounts := map[string]int{}
	for _, t := range texts {
		n := normText(t)
		if len(n) < minTextLen {
			continue
		}
		if msgCounts[n]++; msgCounts[n] >= textMsgRepeat {
			return hintTextMessage
		}
	}
	sentCounts := map[string]int{}
	for _, t := range texts {
		for _, s := range sentSplit.Split(t, -1) {
			if s = normText(s); len(s) >= minTextLen {
				if sentCounts[s]++; sentCounts[s] >= textSentRepeat {
					return hintTextSent
				}
			}
		}
	}
	return ""
}

// repeatedRun: some window of ≥2 consecutive calls occurs ≥2 times without overlapping (longest windows first).
func repeatedRun(seq []string) bool {
	n := len(seq)
	for l := n / 2; l >= minSequenceLen; l-- {
		for start := 0; start <= n-l*2; start++ {
			pattern := strings.Join(seq[start:start+l], "|")
			count := 0
			for pos := 0; pos <= n-l; {
				if strings.Join(seq[pos:pos+l], "|") == pattern {
					count++
					pos += l
				} else {
					pos++
				}
			}
			if count >= sequenceRepeat {
				return true
			}
		}
	}
	return false
}

// appendHint adds "[ROUTER NOTE: …]" to the message the model answers next (last user/tool turn, or a trailing assistant turn).
func appendHint(body map[string]any, format, hint string) {
	if format == "responses" {
		return // the termination prompt in `instructions` is enough; items have no uniform text slot
	}
	msgs, _ := body["messages"].([]any)
	note := "\n\n" + routerNote + " " + hint + "]"
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m == nil {
			continue
		}
		role := m["role"]
		if role != "user" && role != "tool" && !(role == "assistant" && i == len(msgs)-1) {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if !strings.Contains(c, routerNote) {
				m["content"] = c + note
			}
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok && strings.Contains(str(pm["text"]), routerNote) {
					return
				}
			}
			m["content"] = append(c, map[string]any{"type": "text", "text": strings.TrimSpace(note)})
		default:
			m["content"] = strings.TrimSpace(note)
		}
		return
	}
}
