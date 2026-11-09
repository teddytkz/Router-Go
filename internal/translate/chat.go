package translate

import "time"

func firstChoice(b M) M {
	if l := asList(b["choices"]); len(l) > 0 {
		return asMap(l[0])
	}
	return nil
}

func parseChatResp(b M) result {
	r := result{id: str(b["id"]), model: str(b["model"]), u: usageChat(asMap(b["usage"]))}
	ch := firstChoice(b)
	msg := asMap(ch["message"])
	r.text = textOf(msg["content"], "")
	r.reasoning = str(msg["reasoning_content"])
	r.finish = str(ch["finish_reason"])
	for _, tc := range asList(msg["tool_calls"]) {
		tm := asMap(tc)
		fn := asMap(tm["function"])
		r.calls = append(r.calls, call{id: str(tm["id"]), name: str(fn["name"]), args: str(fn["arguments"])})
	}
	return r
}

func renderChatResp(r result) M {
	msg := M{"role": "assistant", "content": r.text}
	if r.reasoning != "" {
		msg["reasoning_content"] = r.reasoning
	}
	if len(r.calls) > 0 {
		var tcs []any
		for _, c := range r.calls {
			id := c.id
			if id == "" {
				id = newID("call_")
			}
			tcs = append(tcs, M{"id": id, "type": "function", "function": M{"name": c.name, "arguments": c.args}})
		}
		msg["tool_calls"] = tcs
		if r.text == "" {
			msg["content"] = nil
		}
	}
	fin := r.finish
	if fin == "" {
		fin = "stop"
		if len(r.calls) > 0 {
			fin = "tool_calls"
		}
	}
	id := r.id
	if id == "" {
		id = newID("chatcmpl-")
	}
	out := M{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": r.model,
		"choices": []any{M{"index": 0, "message": msg, "finish_reason": fin}}}
	if r.u.set {
		out["usage"] = chatUsage(r.u)
	}
	return out
}

// parseChat parses one upstream chat chunk (or [DONE]) into hub events.
func (s *Stream) parseChat(_, data string) []ev {
	if data == "[DONE]" {
		return []ev{{done: true}}
	}
	m := decode(data)
	if m == nil {
		return nil
	}
	base := ev{id: str(m["id"]), model: str(m["model"])}
	if u := asMap(m["usage"]); u != nil {
		x := usageChat(u)
		base.usage = &x
	}
	ch := firstChoice(m)
	d := asMap(ch["delta"])
	base.text = str(d["content"])
	base.reasoning = str(d["reasoning_content"])
	if base.reasoning == "" {
		base.reasoning = str(d["reasoning"])
	}
	out := []ev{base}
	for _, tc := range asList(d["tool_calls"]) {
		tm := asMap(tc)
		fn := asMap(tm["function"])
		out = append(out, ev{tc: &tcDelta{idx: toInt(tm["index"]), id: str(tm["id"]), name: str(fn["name"]), args: str(fn["arguments"])}})
	}
	if f := str(ch["finish_reason"]); f != "" {
		out = append(out, ev{finish: f})
	}
	return out
}
