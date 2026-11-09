package translate

import (
	"bytes"
	"strings"
)

// ---- Gemini generateContent request -> chat ----

func geminiToChat(b M) M {
	out := M{}
	copyKeys(out, b, "model", "stream")
	gc := asMap(b["generationConfig"])
	for from, to := range map[string]string{"maxOutputTokens": "max_tokens", "temperature": "temperature", "topP": "top_p", "stopSequences": "stop"} {
		if v := gc[from]; v != nil {
			out[to] = v
		}
	}
	if isStream(b) {
		out["stream_options"] = M{"include_usage": true}
	}
	var msgs []any
	if sys := textOf(asMap(b["systemInstruction"])["parts"], "\n"); sys != "" {
		msgs = append(msgs, M{"role": "system", "content": sys})
	}
	for _, c := range asList(b["contents"]) {
		msgs = append(msgs, geminiContent(asMap(c))...)
	}
	out["messages"] = msgs
	var tools []any
	for _, t := range asList(b["tools"]) {
		for _, d := range asList(asMap(t)["functionDeclarations"]) {
			dm := asMap(d)
			schema := dm["parametersJsonSchema"]
			if schema == nil {
				schema = dm["parameters"]
			}
			if schema == nil {
				schema = M{"type": "object", "properties": M{}}
			}
			fn := M{"name": str(dm["name"]), "parameters": lowerTypes(schema)}
			copyKeys(fn, dm, "description")
			tools = append(tools, M{"type": "function", "function": fn})
		}
	}
	if tools != nil {
		out["tools"] = tools
		fc := asMap(asMap(b["toolConfig"])["functionCallingConfig"])
		names := asList(fc["allowedFunctionNames"])
		switch strings.ToUpper(str(fc["mode"])) {
		case "AUTO":
			out["tool_choice"] = "auto"
		case "NONE":
			out["tool_choice"] = "none"
		case "ANY":
			out["tool_choice"] = "required"
			if len(names) == 1 {
				out["tool_choice"] = M{"type": "function", "function": M{"name": str(names[0])}}
			}
		}
	}
	return out
}

// geminiContent turns one Gemini content (role user|model, a list of parts) into chat messages.
func geminiContent(c M) []any {
	var parts, calls, results []any
	for _, p := range asList(c["parts"]) {
		pm := asMap(p)
		switch {
		case pm["thought"] == true: // the model's own reasoning is not input
		case pm["text"] != nil:
			parts = append(parts, M{"type": "text", "text": str(pm["text"])})
		case pm["inlineData"] != nil:
			d := asMap(pm["inlineData"])
			mime, data := str(d["mimeType"]), str(d["data"])
			if f, ok := strings.CutPrefix(mime, "audio/"); ok {
				if f == "mpeg" {
					f = "mp3"
				}
				parts = append(parts, M{"type": "input_audio", "input_audio": M{"data": data, "format": f}})
			} else {
				parts = append(parts, M{"type": "image_url", "image_url": M{"url": "data:" + mime + ";base64," + data}})
			}
		case pm["functionCall"] != nil:
			fc := asMap(pm["functionCall"])
			calls = append(calls, M{"id": geminiCallID(fc), "type": "function", "function": M{"name": str(fc["name"]), "arguments": jsonStr(fc["args"])}})
		case pm["functionResponse"] != nil:
			fr := asMap(pm["functionResponse"])
			results = append(results, M{"role": "tool", "tool_call_id": geminiCallID(fr), "content": jsonStr(fr["response"])})
		}
	}
	msgs := results // tool results must directly follow the assistant call, before any follow-up text
	switch {
	case len(calls) > 0:
		msg := M{"role": "assistant", "content": nil, "tool_calls": calls}
		if len(parts) > 0 {
			msg["content"] = simplify(parts)
		}
		msgs = append(msgs, msg)
	case len(parts) > 0:
		role := "user"
		if str(c["role"]) == "model" {
			role = "assistant"
		}
		msgs = append(msgs, M{"role": role, "content": simplify(parts)})
	}
	return msgs
}

// geminiCallID: Gemini calls often carry no id, so the function name pairs a call with its response.
func geminiCallID(m M) string {
	if id := str(m["id"]); id != "" {
		return id
	}
	return "call_" + str(m["name"])
}

// lowerTypes lowercases JSON-schema "type" values: Gemini clients send OBJECT/STRING, chat upstreams want object/string.
func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(M, len(x))
		for k, e := range x {
			if s, ok := e.(string); ok && k == "type" {
				e = strings.ToLower(s)
			}
			out[k] = lowerTypes(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = lowerTypes(e)
		}
		return out
	}
	return v
}

// ---- hub -> Gemini response ----

func geminiFinish(f string) string {
	switch f {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	}
	return "STOP"
}

func geminiUsage(u usage) M {
	m := M{"promptTokenCount": u.prompt, "candidatesTokenCount": u.completion, "totalTokenCount": u.prompt + u.completion}
	if u.cacheRead > 0 {
		m["cachedContentTokenCount"] = u.cacheRead
	}
	return m
}

func geminiCallPart(id, name, args string) M {
	fc := M{"name": name, "args": parseArgs(args)}
	if id != "" {
		fc["id"] = id
	}
	return M{"functionCall": fc}
}

func geminiCandidate(parts []any, finish string) M {
	if parts == nil {
		parts = []any{M{"text": ""}}
	}
	c := M{"content": M{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		c["finishReason"] = finish
	}
	return c
}

func renderGeminiResp(r result) M {
	var parts []any
	if r.reasoning != "" {
		parts = append(parts, M{"text": r.reasoning, "thought": true})
	}
	if r.text != "" {
		parts = append(parts, M{"text": r.text})
	}
	for _, c := range r.calls {
		parts = append(parts, geminiCallPart(c.id, c.name, c.args))
	}
	out := M{"candidates": []any{geminiCandidate(parts, geminiFinish(r.finish))}, "modelVersion": r.model}
	if r.u.set {
		out["usageMetadata"] = geminiUsage(r.u)
	}
	return out
}

// ---- hub events -> Gemini SSE ----

type gcall struct {
	id, name string
	args     strings.Builder
}

// gstate holds the tool calls of a stream: Gemini wants each call whole (args as an object), so they wait for the end.
type gstate struct {
	calls []*gcall
	byIdx map[int]*gcall
}

func (s *Stream) renderGemini(b *bytes.Buffer, e ev) {
	g := &s.gs
	chunk := func(parts []any, finish string) {
		out := M{"candidates": []any{geminiCandidate(parts, finish)}, "modelVersion": s.model}
		if finish != "" && s.u.set {
			out["usageMetadata"] = geminiUsage(s.u)
		}
		frame(b, "", out)
	}
	var parts []any
	if e.reasoning != "" {
		parts = append(parts, M{"text": e.reasoning, "thought": true})
	}
	if e.text != "" {
		parts = append(parts, M{"text": e.text})
	}
	if parts != nil {
		chunk(parts, "")
	}
	if t := e.tc; t != nil {
		c := g.byIdx[t.idx]
		if c == nil {
			if g.byIdx == nil {
				g.byIdx = map[int]*gcall{}
			}
			c = &gcall{}
			g.byIdx[t.idx] = c
			g.calls = append(g.calls, c)
		}
		if t.id != "" {
			c.id = t.id
		}
		if t.name != "" {
			c.name = t.name
		}
		c.args.WriteString(t.args)
	}
	if e.done { // the closing chunk carries the calls, the finish reason and the usage
		var calls []any
		for _, c := range g.calls {
			calls = append(calls, geminiCallPart(c.id, c.name, c.args.String()))
		}
		chunk(calls, geminiFinish(s.finish))
	}
}
