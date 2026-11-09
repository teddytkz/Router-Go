package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"router-go/internal/consolelog"
)

// transforms are the optional request rewrites configured in Settings → Token Saver / Guards & Shields.
// They run on the final upstream-format body, after translation, in the order XRouter-Next uses:
// ponytail → watermarks remover → custom prompt → loop guard.
type transforms struct {
	ponytail   string // system prompt, "" = off
	level      string // ponytail level, for the log
	watermarks bool
	custom     string // "" = off
	loopGuard  bool
}

func (t transforms) active() bool {
	return t.ponytail != "" || t.watermarks || t.custom != "" || t.loopGuard
}

func (g *Gateway) transformsFor() transforms {
	set, err := g.Store.Settings()
	if err != nil {
		return transforms{}
	}
	on := func(k string) bool { v, _ := set[k].(bool); return v }
	t := transforms{watermarks: on("watermarksRemoverEnabled"), loopGuard: set["loopGuardEnabled"] != false}
	if on("ponytailEnabled") {
		lvl, _ := set["ponytailLevel"].(string)
		if lvl == "" {
			lvl = "full"
		}
		t.ponytail, t.level = ponytailPrompt(lvl), lvl
	}
	if p, _ := set["customPrompt"].(string); on("customPromptEnabled") {
		t.custom = strings.TrimSpace(p)
	}
	return t
}

// apply rewrites body in place for the given wire format ("chat", "responses", "messages") and reports what it did.
// The caller must pass a private copy of the body (nested slices/maps are mutated).
func (t transforms) apply(body map[string]any, format string) {
	var applied []string
	for i, p := range []string{t.ponytail, ternary(t.watermarks, watermarksPrompt, ""), t.custom} {
		if p != "" {
			injectSystem(body, format, p)
			applied = append(applied, []string{"ponytail (" + t.level + ")", "watermarks remover", "custom prompt"}[i])
		}
	}
	if len(applied) > 0 {
		consolelog.Logf("INFO", "SAVER", "%s request: system prompt added by %s", format, strings.Join(applied, ", "))
	}
	if t.loopGuard {
		if hint := detectLoop(body, format); hint != "" {
			injectSystem(body, format, terminationPrompt)
			appendHint(body, format, hint)
			consolelog.Logf("WARN", "GUARD", "loop detected in the conversation (%s format): stop instruction injected", format)
		}
	}
}

func ternary(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// deepCopy clones a decoded JSON body so transforms never leak into the client request shared by combo members.
func deepCopy(m map[string]any) map[string]any {
	raw, err := json.Marshal(m)
	if err != nil {
		return m
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if dec.Decode(&out) != nil {
		return m
	}
	return out
}

const sep = "\n\n"

// hasPrompt: the prompt is a whole sep-delimited segment (not a substring of unrelated text), which keeps injection idempotent.
func hasPrompt(h, p string) bool {
	return h == p || slicesContains(strings.Split(h, sep), p)
}

func slicesContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func appendText(cur, p string) string {
	switch {
	case cur == "":
		return p
	case hasPrompt(cur, p):
		return cur
	}
	return cur + sep + p
}

// injectSystem appends prompt to the system message of the body, idempotently. Never panics on odd shapes.
func injectSystem(body map[string]any, format, prompt string) {
	switch format {
	case "messages": // Anthropic: dedicated `system` field (string or text blocks)
		switch sys := body["system"].(type) {
		case string:
			body["system"] = appendText(sys, prompt)
		case []any:
			for _, b := range sys {
				if m, ok := b.(map[string]any); ok && m["text"] == prompt {
					return
				}
			}
			block := map[string]any{"type": "text", "text": prompt}
			at := len(sys)
			for i := len(sys) - 1; i >= 0; i-- { // keep the cached prefix intact: insert before the last cache breakpoint
				if m, ok := sys[i].(map[string]any); ok && m["cache_control"] != nil {
					at = i
					break
				}
			}
			body["system"] = append(sys[:at:at], append([]any{block}, sys[at:]...)...)
		default:
			body["system"] = prompt
		}
	case "responses":
		switch ins := body["instructions"].(type) {
		case string:
			body["instructions"] = appendText(ins, prompt)
		default:
			body["instructions"] = prompt
		}
	default: // chat
		msgs, ok := body["messages"].([]any)
		if !ok {
			return
		}
		for _, raw := range msgs {
			m, ok := raw.(map[string]any)
			if !ok || (m["role"] != "system" && m["role"] != "developer") {
				continue
			}
			switch c := m["content"].(type) {
			case string:
				m["content"] = appendText(c, prompt)
			case []any:
				for _, p := range c {
					if pm, ok := p.(map[string]any); ok && pm["text"] == prompt {
						return
					}
				}
				m["content"] = append(c, map[string]any{"type": "text", "text": prompt})
			default:
				m["content"] = prompt
			}
			return
		}
		body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, msgs...)
	}
}
