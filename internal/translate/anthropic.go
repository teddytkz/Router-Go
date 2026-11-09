package translate

import "strings"

// ---- Messages request -> chat ----

func messagesToChat(b M) M {
	out := M{}
	copyKeys(out, b, "model", "stream", "temperature", "top_p", "max_tokens")
	if v := b["stop_sequences"]; v != nil {
		out["stop"] = v
	}
	if u := str(asMap(b["metadata"])["user_id"]); u != "" {
		out["user"] = u
	}
	if isStream(b) {
		out["stream_options"] = M{"include_usage": true}
	}
	var msgs []any
	if sys := textOf(b["system"], "\n"); sys != "" {
		msgs = append(msgs, M{"role": "system", "content": sys})
	}
	for _, m := range asList(b["messages"]) {
		mm := asMap(m)
		role := str(mm["role"])
		blocks, ok := mm["content"].([]any)
		if !ok {
			msgs = append(msgs, M{"role": role, "content": str(mm["content"])})
			continue
		}
		if role == "assistant" {
			msg := M{"role": "assistant", "content": nil}
			var text []string
			var calls []any
			for _, bl := range blocks {
				bm := asMap(bl)
				switch str(bm["type"]) {
				case "text":
					text = append(text, str(bm["text"]))
				case "tool_use":
					calls = append(calls, M{"id": str(bm["id"]), "type": "function",
						"function": M{"name": str(bm["name"]), "arguments": jsonStr(bm["input"])}})
				}
			}
			if len(text) > 0 {
				msg["content"] = strings.Join(text, "")
			}
			if len(calls) > 0 {
				msg["tool_calls"] = calls
			}
			if msg["content"] != nil || calls != nil {
				msgs = append(msgs, msg)
			}
			continue
		}
		var parts, tools []any
		for _, bl := range blocks {
			bm := asMap(bl)
			switch str(bm["type"]) {
			case "text":
				parts = append(parts, M{"type": "text", "text": str(bm["text"])})
			case "image":
				parts = append(parts, M{"type": "image_url", "image_url": M{"url": blockToImageURL(bm)}})
			case "tool_result":
				tools = append(tools, M{"role": "tool", "tool_call_id": str(bm["tool_use_id"]), "content": textOf(bm["content"], "\n")})
			}
		}
		msgs = append(msgs, tools...)
		if len(parts) > 0 {
			msgs = append(msgs, M{"role": role, "content": simplify(parts)})
		}
	}
	out["messages"] = msgs
	var tools []any
	for _, t := range asList(b["tools"]) {
		tm := asMap(t)
		if tm["input_schema"] == nil { // server-side tools have no chat equivalent
			continue
		}
		fn := M{"name": str(tm["name"]), "parameters": tm["input_schema"]}
		copyKeys(fn, tm, "description")
		tools = append(tools, M{"type": "function", "function": fn})
	}
	if tools != nil {
		out["tools"] = tools
	}
	if tc := asMap(b["tool_choice"]); tc != nil {
		switch str(tc["type"]) {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			out["tool_choice"] = M{"type": "function", "function": M{"name": str(tc["name"])}}
		}
		if d, _ := tc["disable_parallel_tool_use"].(bool); d {
			out["parallel_tool_calls"] = false
		}
	}
	return out
}

// ---- chat request -> Messages ----

func chatToMessages(b M) M {
	out := M{}
	copyKeys(out, b, "model", "stream", "temperature", "top_p")
	mt := b["max_tokens"]
	if mt == nil {
		mt = b["max_completion_tokens"]
	}
	if mt == nil {
		mt = 4096
	}
	out["max_tokens"] = mt
	switch s := b["stop"].(type) {
	case string:
		out["stop_sequences"] = []any{s}
	case []any:
		out["stop_sequences"] = s
	}
	if u := str(b["user"]); u != "" {
		out["metadata"] = M{"user_id": u}
	}
	var system []string
	var msgs []any
	add := func(role string, blocks ...any) { // merges consecutive same-role messages
		if len(blocks) == 0 {
			return
		}
		if l := lastMsg(msgs); l != nil && l["role"] == role {
			l["content"] = append(l["content"].([]any), blocks...)
			return
		}
		msgs = append(msgs, M{"role": role, "content": blocks})
	}
	for _, m := range asList(b["messages"]) {
		mm := asMap(m)
		switch str(mm["role"]) {
		case "system", "developer":
			if t := textOf(mm["content"], "\n"); t != "" {
				system = append(system, t)
			}
		case "user":
			add("user", chatToBlocks(mm["content"])...)
		case "assistant":
			blocks := chatToBlocks(mm["content"])
			for _, tc := range asList(mm["tool_calls"]) {
				tm := asMap(tc)
				fn := asMap(tm["function"])
				id := str(tm["id"])
				if id == "" {
					id = newID("toolu_")
				}
				blocks = append(blocks, M{"type": "tool_use", "id": id, "name": str(fn["name"]), "input": parseArgs(str(fn["arguments"]))})
			}
			add("assistant", blocks...)
		case "tool":
			add("user", M{"type": "tool_result", "tool_use_id": str(mm["tool_call_id"]), "content": textOf(mm["content"], "\n")})
		}
	}
	out["messages"] = msgs
	if len(system) > 0 {
		out["system"] = strings.Join(system, "\n\n")
	}
	var tools []any
	for _, t := range asList(b["tools"]) {
		fn := asMap(asMap(t)["function"])
		if fn == nil {
			continue
		}
		schema := fn["parameters"]
		if schema == nil {
			schema = M{"type": "object", "properties": M{}}
		}
		at := M{"name": str(fn["name"]), "input_schema": schema}
		copyKeys(at, fn, "description")
		tools = append(tools, at)
	}
	if tools == nil {
		return out
	}
	out["tools"] = tools
	var tc M
	switch c := b["tool_choice"].(type) {
	case string:
		tc = M{"type": map[string]string{"auto": "auto", "required": "any", "none": "none"}[c]}
		if tc["type"] == "" {
			tc = nil
		}
	case map[string]any:
		if n := str(asMap(c["function"])["name"]); n != "" {
			tc = M{"type": "tool", "name": n}
		}
	}
	if p, ok := b["parallel_tool_calls"].(bool); ok && !p {
		if tc == nil {
			tc = M{"type": "auto"}
		}
		tc["disable_parallel_tool_use"] = true
	}
	if tc != nil {
		out["tool_choice"] = tc
	}
	return out
}

func chatToBlocks(content any) []any {
	var out []any
	switch c := content.(type) {
	case string:
		if c != "" {
			out = append(out, M{"type": "text", "text": c})
		}
	case []any:
		for _, p := range c {
			pm := asMap(p)
			switch str(pm["type"]) {
			case "text":
				out = append(out, M{"type": "text", "text": str(pm["text"])})
			case "image_url":
				out = append(out, imageToBlock(imageURL(pm)))
			}
		}
	}
	return out
}

// ---- Messages non-stream response <-> hub ----

func parseMsgResp(b M) result {
	r := result{id: str(b["id"]), model: str(b["model"]), finish: stopToChat(str(b["stop_reason"])), u: usageMsg(asMap(b["usage"]))}
	for _, bl := range asList(b["content"]) {
		bm := asMap(bl)
		switch str(bm["type"]) {
		case "text":
			r.text += str(bm["text"])
		case "thinking":
			r.reasoning += str(bm["thinking"])
		case "tool_use":
			r.calls = append(r.calls, call{id: str(bm["id"]), name: str(bm["name"]), args: jsonStr(bm["input"])})
		}
	}
	return r
}

func renderMsgResp(r result) M {
	var content []any
	if r.reasoning != "" {
		content = append(content, M{"type": "thinking", "thinking": r.reasoning, "signature": ""})
	}
	if r.text != "" {
		content = append(content, M{"type": "text", "text": r.text})
	}
	for _, c := range r.calls {
		id := c.id
		if id == "" {
			id = newID("toolu_")
		}
		content = append(content, M{"type": "tool_use", "id": id, "name": c.name, "input": parseArgs(c.args)})
	}
	if content == nil {
		content = []any{M{"type": "text", "text": ""}}
	}
	id := r.id
	if id == "" {
		id = newID("msg_")
	}
	return M{"id": id, "type": "message", "role": "assistant", "model": r.model, "content": content,
		"stop_reason": chatToStop(r.finish, len(r.calls) > 0), "stop_sequence": nil, "usage": msgUsage(r.u)}
}

// ---- Messages stream events -> hub events ----

func (s *Stream) parseMsg(event, data string) []ev {
	m := decode(data)
	if m == nil {
		return nil
	}
	t := str(m["type"])
	if t == "" {
		t = event
	}
	switch t {
	case "message_start":
		msg := asMap(m["message"])
		e := ev{id: str(msg["id"]), model: str(msg["model"])}
		if u := asMap(msg["usage"]); u != nil {
			x := usageMsg(u)
			e.usage = &x
		}
		return []ev{e}
	case "content_block_start":
		cb := asMap(m["content_block"])
		switch str(cb["type"]) {
		case "text":
			return []ev{{text: str(cb["text"])}}
		case "tool_use":
			idx := s.nTools
			s.nTools++
			s.bm[toInt(m["index"])] = idx
			id := str(cb["id"])
			if id == "" {
				id = newID("toolu_")
			}
			return []ev{{tc: &tcDelta{idx: idx, id: id, name: str(cb["name"])}}}
		}
	case "content_block_delta":
		d := asMap(m["delta"])
		switch str(d["type"]) {
		case "text_delta":
			return []ev{{text: str(d["text"])}}
		case "thinking_delta":
			return []ev{{reasoning: str(d["thinking"])}}
		case "input_json_delta":
			if idx, ok := s.bm[toInt(m["index"])]; ok {
				return []ev{{tc: &tcDelta{idx: idx, args: str(d["partial_json"])}}}
			}
		}
	case "message_delta":
		e := ev{finish: stopToChat(str(asMap(m["delta"])["stop_reason"]))}
		if u := asMap(m["usage"]); u != nil {
			x := usageMsg(u)
			e.usage = &x
		}
		return []ev{e}
	case "message_stop":
		return []ev{{done: true}}
	}
	return nil
}
