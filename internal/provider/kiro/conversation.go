package kiro

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The Kiro wire conversation: alternating user/assistant turns, the current (last) user turn carrying the tool
// definitions, and every assistant tool call answered by exactly one tool result in the turn right after it.
// Chat histories do not always satisfy that, so build() repairs them the way XRouter-Next does: a call without a
// matching result (or without a definition), and a result without a call, are flattened into plain text.

type image struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type textPart struct {
	Text string `json:"text"`
}

type toolResult struct {
	ToolUseID string     `json:"toolUseId"`
	Status    string     `json:"status"`
	Content   []textPart `json:"content"`
}

type toolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"` // nil = the arguments were not a JSON object
}

type toolSpec struct {
	ToolSpecification struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON map[string]any `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpecification"`
}

type userCtx struct {
	ToolResults []toolResult `json:"toolResults,omitempty"`
	Tools       []toolSpec   `json:"tools,omitempty"`
}

type userMsg struct {
	Content string   `json:"content"`
	ModelID string   `json:"modelId"`
	Origin  string   `json:"origin,omitempty"`
	Images  []image  `json:"images,omitempty"`
	Context *userCtx `json:"userInputMessageContext,omitempty"`
}

type assistantMsg struct {
	Content  string    `json:"content"`
	ToolUses []toolUse `json:"toolUses,omitempty"`
}

type turn struct {
	User      *userMsg      `json:"userInputMessage,omitempty"`
	Assistant *assistantMsg `json:"assistantResponseMessage,omitempty"`
}

const (
	maxToolName = 64
	maxToolDesc = 10237
	maxToolID   = 64
	// A turn with nothing but tool results must not read like an instruction ("continue" makes models answer the word).
	resultsPlaceholder = "Tool results provided."
	emptyPlaceholder   = "continue"
)

var (
	badNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	dataURI      = regexp.MustCompile(`^data:([^;,]+);base64,(.+)$`)
)

func joinText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

func runes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// textOf reads a chat message content (string or parts): its text and its base64 images.
// shortcut: http(s) image URLs become a text note, Kiro takes bytes only; fetching them is not worth a network call here.
func textOf(content any) (text string, images []image) {
	switch v := content.(type) {
	case string:
		return v, nil
	case []any:
		var parts []string
		for _, p := range v {
			m, _ := p.(map[string]any)
			switch m["type"] {
			case "text":
				if s, _ := m["text"].(string); s != "" {
					parts = append(parts, s)
				}
			case "image_url":
				u, _ := m["image_url"].(map[string]any)
				url, _ := u["url"].(string)
				if g := dataURI.FindStringSubmatch(url); g != nil {
					img := image{Format: g[1][strings.LastIndex(g[1], "/")+1:]}
					img.Source.Bytes = g[2]
					images = append(images, img)
				} else if strings.HasPrefix(url, "http") {
					parts = append(parts, "[Image: "+url+"]")
				}
			}
		}
		return strings.Join(parts, "\n"), images
	}
	return "", nil
}

// toolSpecs converts chat tool definitions; names are made Kiro-safe and unique (returned map: original → Kiro name).
func toolSpecs(tools any) ([]toolSpec, map[string]string) {
	var specs []toolSpec
	names, used := map[string]string{}, map[string]bool{}
	list, _ := tools.([]any)
	for i, t := range list {
		m, _ := t.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		if fn == nil {
			fn = m // already flat ({name, description, parameters})
		}
		raw, _ := fn["name"].(string)
		if _, dup := names[raw]; strings.TrimSpace(raw) == "" || dup {
			continue
		}
		base := runes(strings.Trim(badNameChars.ReplaceAllString(strings.TrimSpace(raw), "_"), "_"), maxToolName)
		if base == "" {
			base = fmt.Sprintf("tool_%d", i+1)
		}
		name := base
		for n := 2; used[name]; n++ {
			tail := fmt.Sprintf("_%d", n)
			name = runes(base, maxToolName-len(tail)) + tail
		}
		used[name], names[raw] = true, name
		desc, _ := fn["description"].(string)
		if desc == "" {
			desc = "Tool: " + raw
		}
		schema, _ := fn["parameters"].(map[string]any)
		var s toolSpec
		s.ToolSpecification.Name, s.ToolSpecification.Description = name, runes(desc, maxToolDesc)
		s.ToolSpecification.InputSchema.JSON = rootSchema(schema)
		specs = append(specs, s)
	}
	return specs, names
}

// rootSchema: Kiro wants an object schema without additionalProperties or empty required lists.
func rootSchema(schema map[string]any) map[string]any {
	s, _ := cleanSchema(schema).(map[string]any)
	if s == nil {
		s = map[string]any{}
	}
	s["type"] = "object"
	props, ok := s["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		s["properties"] = props
	}
	if req, ok := s["required"].([]any); ok {
		var keep []any
		for _, r := range req {
			if n, _ := r.(string); props[n] != nil {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			delete(s, "required")
		} else {
			s["required"] = keep
		}
	}
	return s
}

func cleanSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, c := range x {
			if k == "additionalProperties" {
				continue
			}
			if r, ok := c.([]any); k == "required" && ok && len(r) == 0 {
				continue
			}
			out[k] = cleanSchema(c)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = cleanSchema(c)
		}
		return out
	}
	return v
}

// turns reads the chat messages into raw turns (consecutive turns of one role are merged). System messages ride
// as <instructions> inside user turns, tool messages as tool results.
func turns(messages any, names map[string]string) []turn {
	var out []turn
	list, _ := messages.([]any)
	add := func(t turn) {
		if n := len(out); n > 0 {
			switch last := &out[n-1]; {
			case t.User != nil && last.User != nil:
				last.User.Content = joinText(last.User.Content, t.User.Content)
				last.User.Images = append(last.User.Images, t.User.Images...)
				if t.User.Context != nil {
					if last.User.Context == nil {
						last.User.Context = &userCtx{}
					}
					last.User.Context.ToolResults = append(last.User.Context.ToolResults, t.User.Context.ToolResults...)
				}
				return
			case t.Assistant != nil && last.Assistant != nil:
				last.Assistant.Content = joinText(last.Assistant.Content, t.Assistant.Content)
				last.Assistant.ToolUses = append(last.Assistant.ToolUses, t.Assistant.ToolUses...)
				return
			}
		}
		out = append(out, t)
	}
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		text, images := textOf(m["content"])
		switch m["role"] {
		case "system", "developer":
			if text != "" {
				add(turn{User: &userMsg{Content: "<instructions>\n" + text + "\n</instructions>"}})
			}
		case "user":
			add(turn{User: &userMsg{Content: text, Images: images}})
		case "tool":
			id, _ := m["tool_call_id"].(string)
			add(turn{User: &userMsg{Context: &userCtx{ToolResults: []toolResult{{ToolUseID: id, Status: "success", Content: []textPart{{Text: text}}}}}}})
		case "assistant":
			a := &assistantMsg{Content: strings.TrimSpace(text)}
			calls, _ := m["tool_calls"].([]any)
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				fn, _ := cm["function"].(map[string]any)
				id, _ := cm["id"].(string)
				name, _ := fn["name"].(string)
				if k, ok := names[name]; ok {
					name = k
				}
				args, _ := fn["arguments"].(string)
				input := map[string]any{}
				if strings.TrimSpace(args) != "" && json.Unmarshal([]byte(args), &input) != nil {
					input = nil
				}
				a.ToolUses = append(a.ToolUses, toolUse{ToolUseID: id, Name: name, Input: input})
			}
			add(turn{Assistant: a})
		}
	}
	return out
}

func callText(u toolUse) string {
	in, _ := json.Marshal(u.Input)
	return "[Tool call: " + u.Name + "(" + string(in) + ")]"
}

func resultText(r toolResult) string {
	var parts []string
	for _, p := range r.Content {
		parts = append(parts, p.Text)
	}
	label := "Tool result"
	if r.Status == "error" {
		label += " (error)"
	}
	return "[" + label + ": " + strings.Join(parts, "\n") + "]"
}

// toolID is a call id Kiro accepts ([a-zA-Z0-9_-], ≤64, unique in the conversation).
func toolID(raw string, turnIdx, callIdx int, name string, used map[string]bool) string {
	id := runes(badNameChars.ReplaceAllString(raw, ""), maxToolID)
	if id == "" {
		id = runes(fmt.Sprintf("call_msg%d_tc%d_%s", turnIdx, callIdx, name), maxToolID)
	}
	base := id
	for n := 2; used[id]; n++ {
		tail := fmt.Sprintf("_%d", n)
		id = runes(base, maxToolID-len(tail)) + tail
	}
	used[id] = true
	return id
}

// build returns the history and the current user turn for the given model, with the tool definitions on the latter.
func build(messages any, names map[string]string, specs []toolSpec, model string) (history []turn, current *userMsg) {
	ts := turns(messages, names)
	if len(ts) == 0 || ts[0].Assistant != nil {
		ts = append([]turn{{User: &userMsg{}}}, ts...)
	}
	if ts[len(ts)-1].Assistant != nil {
		ts = append(ts, turn{User: &userMsg{}})
	}
	specNames := map[string]bool{}
	for _, s := range specs {
		specNames[s.ToolSpecification.Name] = true
	}
	flatten := func(u *userMsg, r toolResult) { u.Content = joinText(u.Content, resultText(r)) }
	if c := ts[0].User.Context; c != nil { // nothing precedes the first turn: its results have no call
		for _, r := range c.ToolResults {
			flatten(ts[0].User, r)
		}
		ts[0].User.Context = nil
	}
	used := map[string]bool{}
	for i := range ts {
		a := ts[i].Assistant
		if a == nil {
			continue
		}
		next := ts[i+1].User // turns alternate and the last one is a user turn, so a user turn follows
		var results []toolResult
		if next.Context != nil {
			results = next.Context.ToolResults
		}
		byID := map[string]toolResult{}
		for _, r := range results {
			byID[r.ToolUseID] = r
		}
		var calls []toolUse
		var answers []toolResult
		for k, u := range a.ToolUses {
			r, ok := byID[u.ToolUseID]
			if !ok || !specNames[u.Name] || u.Input == nil {
				a.Content = joinText(a.Content, callText(u))
				continue
			}
			delete(byID, u.ToolUseID)
			id := toolID(u.ToolUseID, i, k, u.Name, used)
			u.ToolUseID, r.ToolUseID = id, id
			calls, answers = append(calls, u), append(answers, r)
		}
		a.ToolUses = calls
		for _, r := range results { // a result nobody asked for: keep what the tool said, as text
			if _, left := byID[r.ToolUseID]; left {
				flatten(next, r)
			}
		}
		next.Context = nil
		if len(answers) > 0 {
			next.Context = &userCtx{ToolResults: answers}
		}
	}
	for i := range ts {
		if u := ts[i].User; u != nil {
			if strings.TrimSpace(u.Content) == "" {
				u.Content = emptyPlaceholder
				if u.Context != nil && len(u.Context.ToolResults) > 0 {
					u.Content = resultsPlaceholder
				}
			}
			u.Content, u.ModelID = strings.TrimSpace(u.Content), model
		} else if strings.TrimSpace(ts[i].Assistant.Content) == "" {
			ts[i].Assistant.Content = "..."
		}
	}
	current = ts[len(ts)-1].User
	current.Origin = "AI_EDITOR"
	if len(specs) > 0 {
		if current.Context == nil {
			current.Context = &userCtx{}
		}
		current.Context.Tools = specs
	}
	return ts[:len(ts)-1], current
}
