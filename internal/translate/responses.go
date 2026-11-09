package translate

import (
	"strings"
	"time"
)

// ---- Responses request -> chat ----

func responsesToChat(b M) M {
	out := M{}
	copyKeys(out, b, "model", "stream", "temperature", "top_p", "user", "parallel_tool_calls")
	if v := b["max_output_tokens"]; v != nil {
		out["max_tokens"] = v
	}
	if e := str(asMap(b["reasoning"])["effort"]); e != "" {
		out["reasoning_effort"] = e
	}
	if isStream(b) {
		out["stream_options"] = M{"include_usage": true}
	}
	var msgs []any
	if s := str(b["instructions"]); s != "" {
		msgs = append(msgs, M{"role": "system", "content": s})
	}
	if s, ok := b["input"].(string); ok {
		msgs = append(msgs, M{"role": "user", "content": s})
	}
	for _, it := range asList(b["input"]) {
		im := asMap(it)
		switch typ := str(im["type"]); {
		case typ == "function_call":
			id := str(im["call_id"])
			if id == "" {
				id = str(im["id"])
			}
			c := M{"id": id, "type": "function", "function": M{"name": str(im["name"]), "arguments": str(im["arguments"])}}
			if l := lastMsg(msgs); l != nil && l["role"] == "assistant" {
				l["tool_calls"] = append(asList(l["tool_calls"]), c)
			} else {
				msgs = append(msgs, M{"role": "assistant", "content": nil, "tool_calls": []any{c}})
			}
		case typ == "function_call_output":
			o := im["output"]
			if _, ok := o.(string); !ok {
				o = textOf(o, "\n")
			}
			msgs = append(msgs, M{"role": "tool", "tool_call_id": str(im["call_id"]), "content": o})
		case typ == "message" || (typ == "" && im["role"] != nil):
			role := str(im["role"])
			if role == "developer" {
				role = "system"
			}
			var parts []any
			if s, ok := im["content"].(string); ok {
				parts = []any{M{"type": "text", "text": s}}
			}
			for _, p := range asList(im["content"]) {
				pm := asMap(p)
				switch str(pm["type"]) {
				case "input_text", "output_text", "text":
					parts = append(parts, M{"type": "text", "text": str(pm["text"])})
				case "input_image":
					parts = append(parts, M{"type": "image_url", "image_url": M{"url": imageURL(pm)}})
				}
			}
			if len(parts) == 0 {
				continue
			}
			var content any = simplify(parts)
			if role != "user" {
				content = textOf(parts, "")
			}
			msgs = append(msgs, M{"role": role, "content": content})
		}
	}
	out["messages"] = msgs
	var tools []any
	for _, t := range asList(b["tools"]) {
		tm := asMap(t)
		if str(tm["type"]) != "function" {
			continue
		}
		fn := M{"name": str(tm["name"])}
		copyKeys(fn, tm, "description", "parameters", "strict")
		tools = append(tools, M{"type": "function", "function": fn})
	}
	if tools != nil {
		out["tools"] = tools
	}
	switch tc := b["tool_choice"].(type) {
	case string:
		out["tool_choice"] = tc
	case map[string]any:
		if n := str(tc["name"]); n != "" {
			out["tool_choice"] = M{"type": "function", "function": M{"name": n}}
		}
	}
	return out
}

// ---- chat request -> Responses ----

func chatToResponses(b M) M {
	out := M{}
	copyKeys(out, b, "model", "stream", "temperature", "top_p", "user", "parallel_tool_calls")
	mt := b["max_tokens"]
	if mt == nil {
		mt = b["max_completion_tokens"]
	}
	if mt != nil {
		out["max_output_tokens"] = mt
	}
	if e := str(b["reasoning_effort"]); e != "" {
		out["reasoning"] = M{"effort": e}
	}
	var system []string
	var input []any
	for _, m := range asList(b["messages"]) {
		mm := asMap(m)
		switch role := str(mm["role"]); role {
		case "system", "developer":
			if t := textOf(mm["content"], "\n"); t != "" {
				system = append(system, t)
			}
		case "user":
			var parts []any
			switch c := mm["content"].(type) {
			case string:
				parts = []any{M{"type": "input_text", "text": c}}
			case []any:
				for _, p := range c {
					pm := asMap(p)
					switch str(pm["type"]) {
					case "text":
						parts = append(parts, M{"type": "input_text", "text": str(pm["text"])})
					case "image_url":
						parts = append(parts, M{"type": "input_image", "image_url": imageURL(pm)})
					}
				}
			}
			if parts != nil {
				input = append(input, M{"role": "user", "content": parts})
			}
		case "assistant":
			if t := textOf(mm["content"], ""); t != "" {
				input = append(input, M{"role": "assistant", "content": []any{M{"type": "output_text", "text": t}}})
			}
			for _, tc := range asList(mm["tool_calls"]) {
				tm := asMap(tc)
				fn := asMap(tm["function"])
				input = append(input, M{"type": "function_call", "call_id": str(tm["id"]), "name": str(fn["name"]), "arguments": str(fn["arguments"])})
			}
		case "tool":
			input = append(input, M{"type": "function_call_output", "call_id": str(mm["tool_call_id"]), "output": textOf(mm["content"], "\n")})
		}
	}
	out["input"] = input
	if len(system) > 0 {
		out["instructions"] = strings.Join(system, "\n\n")
	}
	var tools []any
	for _, t := range asList(b["tools"]) {
		fn := asMap(asMap(t)["function"])
		if fn == nil {
			continue
		}
		rt := M{"type": "function", "name": str(fn["name"])}
		copyKeys(rt, fn, "description", "parameters", "strict")
		tools = append(tools, rt)
	}
	if tools != nil {
		out["tools"] = tools
	}
	switch tc := b["tool_choice"].(type) {
	case string:
		out["tool_choice"] = tc
	case map[string]any:
		if n := str(asMap(tc["function"])["name"]); n != "" {
			out["tool_choice"] = M{"type": "function", "name": n}
		}
	}
	return out
}

// ---- Responses item constructors (shared by stream and non-stream) ----

func msgItem(id, text, status string) M {
	return M{"id": id, "type": "message", "status": status, "role": "assistant",
		"content": []any{M{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func reasoningItem(id, text string) M {
	sum := []any{}
	if text != "" {
		sum = append(sum, M{"type": "summary_text", "text": text})
	}
	return M{"id": id, "type": "reasoning", "summary": sum}
}

func callItem(id, callID, name, args, status string) M {
	return M{"id": id, "type": "function_call", "status": status, "call_id": callID, "name": name, "arguments": args}
}

func respObj(id, model string, created int64, status string, output []any, u *usage) M {
	if output == nil {
		output = []any{}
	}
	o := M{"id": id, "object": "response", "created_at": created, "status": status, "model": model, "output": output}
	if u != nil && u.set {
		o["usage"] = respUsage(*u)
	}
	if status == "incomplete" {
		o["incomplete_details"] = M{"reason": "max_output_tokens"}
	}
	return o
}

// ---- Responses non-stream response <-> hub ----

func parseRespResp(b M) result {
	r := result{id: str(b["id"]), model: str(b["model"]), u: usageResp(asMap(b["usage"]))}
	for _, it := range asList(b["output"]) {
		im := asMap(it)
		switch str(im["type"]) {
		case "message":
			for _, p := range asList(im["content"]) {
				pm := asMap(p)
				r.text += str(pm["text"]) + str(pm["refusal"])
			}
		case "reasoning":
			r.reasoning += textOf(im["summary"], "\n")
		case "function_call":
			id := str(im["call_id"])
			if id == "" {
				id = str(im["id"])
			}
			r.calls = append(r.calls, call{id: id, name: str(im["name"]), args: str(im["arguments"])})
		}
	}
	switch {
	case str(b["status"]) == "incomplete":
		r.finish = "length"
	case len(r.calls) > 0:
		r.finish = "tool_calls"
	default:
		r.finish = "stop"
	}
	return r
}

func renderRespResp(r result) M {
	var out []any
	if r.reasoning != "" {
		out = append(out, reasoningItem(newID("rs_"), r.reasoning))
	}
	if r.text != "" {
		out = append(out, msgItem(newID("msg_"), r.text, "completed"))
	}
	for _, c := range r.calls {
		id := c.id
		if id == "" {
			id = newID("call_")
		}
		out = append(out, callItem(newID("fc_"), id, c.name, c.args, "completed"))
	}
	status := "completed"
	if r.finish == "length" {
		status = "incomplete"
	}
	id := r.id
	if id == "" {
		id = newID("resp_")
	}
	return respObj(id, r.model, time.Now().Unix(), status, out, &r.u)
}

// ---- Responses stream events -> hub events ----

func (s *Stream) parseResp(event, data string) []ev {
	m := decode(data)
	if m == nil {
		return nil
	}
	t := str(m["type"])
	if t == "" {
		t = event
	}
	switch t {
	case "response.created":
		r := asMap(m["response"])
		return []ev{{id: str(r["id"]), model: str(r["model"])}}
	case "response.output_item.added":
		it := asMap(m["item"])
		if str(it["type"]) != "function_call" {
			return nil
		}
		idx := s.nTools
		s.nTools++
		s.bm[toInt(m["output_index"])] = idx
		id := str(it["call_id"])
		if id == "" {
			id = str(it["id"])
		}
		return []ev{{tc: &tcDelta{idx: idx, id: id, name: str(it["name"])}}}
	case "response.output_text.delta", "response.refusal.delta":
		return []ev{{text: str(m["delta"])}}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return []ev{{reasoning: str(m["delta"])}}
	case "response.function_call_arguments.delta":
		if idx, ok := s.bm[toInt(m["output_index"])]; ok {
			s.sawArgs[idx] = true
			return []ev{{tc: &tcDelta{idx: idx, args: str(m["delta"])}}}
		}
	case "response.output_item.done":
		it := asMap(m["item"])
		if idx, ok := s.bm[toInt(m["output_index"])]; ok && str(it["type"]) == "function_call" && !s.sawArgs[idx] && str(it["arguments"]) != "" {
			s.sawArgs[idx] = true
			return []ev{{tc: &tcDelta{idx: idx, args: str(it["arguments"])}}}
		}
	case "response.completed", "response.incomplete", "response.failed":
		r := asMap(m["response"])
		e := ev{done: true, finish: "stop"}
		if s.hasTool {
			e.finish = "tool_calls"
		}
		if t == "response.incomplete" || str(r["status"]) == "incomplete" {
			e.finish = "length"
		}
		if u := asMap(r["usage"]); u != nil {
			x := usageResp(u)
			e.usage = &x
		}
		return []ev{e}
	}
	return nil
}
