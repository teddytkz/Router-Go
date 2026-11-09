package translate

import (
	"bytes"
	"strconv"
	"strings"
	"time"
)

// tcDelta is an incremental tool call: id/name are set on the first delta only.
type tcDelta struct {
	idx            int
	id, name, args string
}

// ev is the hub (chat-chunk-like) stream event.
type ev struct {
	id, model, text, reasoning string
	tc                         *tcDelta
	finish                     string // chat finish_reason
	usage                      *usage
	done                       bool
}

type msgState struct {
	started bool
	blk     int
	open    string // "", "text", "thinking" or "tool<idx>"
}

type respState struct {
	started        bool
	seq, out       int
	kind, key      string // current open item: msg|reason|call
	itemID, callID string
	name           string
	text           strings.Builder // text, summary text or arguments of the open item
	items          []any
}

type Stream struct {
	from, to Format
	model    string
	id       string
	created  int64
	u        usage
	done     bool
	hasTool  bool
	finish   string
	// parser state
	nTools  int
	bm      map[int]int // upstream block/output index -> tool index
	sawArgs map[int]bool
	// renderer state
	chatStarted, chatFinished bool
	ms                        msgState
	rs                        respState
	gs                        gstate
}

func NewStream(from, to Format, model string) *Stream {
	return &Stream{from: from, to: to, model: model, created: time.Now().Unix(), bm: map[int]int{}, sawArgs: map[int]bool{}}
}

func (s *Stream) Usage() (prompt, completion int) { return s.u.prompt, s.u.completion }

func (s *Stream) cid() string {
	if s.id == "" {
		s.id = newID(map[Format]string{Chat: "chatcmpl-", Messages: "msg_", Responses: "resp_"}[s.to])
	}
	return s.id
}

func (s *Stream) Feed(event, data string) []byte {
	if s.done {
		return nil
	}
	var evs []ev
	switch s.from {
	case Chat:
		evs = s.parseChat(event, data)
	case Messages:
		evs = s.parseMsg(event, data)
	case Responses:
		evs = s.parseResp(event, data)
	}
	var b bytes.Buffer
	if s.from == s.to {
		if data != "" {
			if event != "" {
				b.WriteString("event: " + event + "\n")
			}
			b.WriteString("data: " + data + "\n\n")
		}
		for _, e := range evs {
			s.absorb(e)
		}
		return b.Bytes()
	}
	for _, e := range evs {
		s.absorb(e)
		s.render(&b, e)
	}
	return b.Bytes()
}

// Close emits any missing terminal events; it returns nil once the stream is terminated.
func (s *Stream) Close() []byte {
	if s.done {
		return nil
	}
	if s.from == s.to {
		s.done = true
		if s.to == Chat {
			return []byte("data: [DONE]\n\n")
		}
		return nil
	}
	var b bytes.Buffer
	e := ev{done: true}
	s.absorb(e)
	s.render(&b, e)
	return b.Bytes()
}

func (s *Stream) absorb(e ev) {
	if s.id == "" && e.id != "" && s.from != s.to {
		s.id = e.id
	}
	if s.model == "" {
		s.model = e.model
	}
	if e.usage != nil {
		s.u.merge(*e.usage)
	}
	if e.tc != nil && e.tc.id != "" {
		s.hasTool = true
	}
	if e.finish != "" {
		s.finish = e.finish
	}
	if e.done {
		s.done = true
	}
}

func (s *Stream) render(b *bytes.Buffer, e ev) {
	switch s.to {
	case Chat:
		s.renderChat(b, e)
	case Messages:
		s.renderMsg(b, e)
	case Responses:
		s.renderResp(b, e)
	case Gemini:
		s.renderGemini(b, e)
	}
}

// ---- chat renderer ----

func (s *Stream) renderChat(b *bytes.Buffer, e ev) {
	chunk := func(delta M, finish any) {
		if !s.chatStarted {
			s.chatStarted = true
			delta["role"] = "assistant"
		}
		frame(b, "", M{"id": s.cid(), "object": "chat.completion.chunk", "created": s.created, "model": s.model,
			"choices": []any{M{"index": 0, "delta": delta, "finish_reason": finish}}})
	}
	if !s.chatStarted {
		chunk(M{"content": ""}, nil)
	}
	if e.reasoning != "" {
		chunk(M{"reasoning_content": e.reasoning}, nil)
	}
	if e.text != "" {
		chunk(M{"content": e.text}, nil)
	}
	if t := e.tc; t != nil {
		fn := M{"arguments": t.args}
		d := M{"index": t.idx, "function": fn}
		if t.id != "" {
			d["id"], d["type"], fn["name"] = t.id, "function", t.name
		}
		chunk(M{"tool_calls": []any{d}}, nil)
	}
	if e.finish != "" && !s.chatFinished {
		s.chatFinished = true
		chunk(M{}, e.finish)
	}
	if e.done {
		if !s.chatFinished {
			s.chatFinished = true
			f := "stop"
			if s.hasTool {
				f = "tool_calls"
			}
			chunk(M{}, f)
		}
		if s.u.set {
			frame(b, "", M{"id": s.cid(), "object": "chat.completion.chunk", "created": s.created, "model": s.model,
				"choices": []any{}, "usage": chatUsage(s.u)})
		}
		b.WriteString("data: [DONE]\n\n")
	}
}

// ---- messages renderer ----

func (s *Stream) renderMsg(b *bytes.Buffer, e ev) {
	r := &s.ms
	if !r.started {
		r.started = true
		frame(b, "message_start", M{"type": "message_start", "message": M{"id": s.cid(), "type": "message", "role": "assistant",
			"model": s.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": msgUsage(usage{prompt: s.u.prompt, cacheRead: s.u.cacheRead, cacheCreate: s.u.cacheCreate})}})
	}
	closeBlk := func() {
		if r.open != "" {
			frame(b, "content_block_stop", M{"type": "content_block_stop", "index": r.blk})
			r.blk++
			r.open = ""
		}
	}
	open := func(key string, block M) {
		if r.open == key {
			return
		}
		closeBlk()
		r.open = key
		frame(b, "content_block_start", M{"type": "content_block_start", "index": r.blk, "content_block": block})
	}
	delta := func(d M) {
		frame(b, "content_block_delta", M{"type": "content_block_delta", "index": r.blk, "delta": d})
	}
	if e.reasoning != "" {
		open("thinking", M{"type": "thinking", "thinking": ""})
		delta(M{"type": "thinking_delta", "thinking": e.reasoning})
	}
	if e.text != "" {
		open("text", M{"type": "text", "text": ""})
		delta(M{"type": "text_delta", "text": e.text})
	}
	if t := e.tc; t != nil {
		key := "tool" + strconv.Itoa(t.idx)
		id := t.id
		if id == "" && r.open != key { // delta for an unannounced call
			id = newID("toolu_")
		}
		if id != "" {
			open(key, M{"type": "tool_use", "id": id, "name": t.name, "input": M{}})
		}
		if t.args != "" {
			delta(M{"type": "input_json_delta", "partial_json": t.args})
		}
	}
	if e.done {
		closeBlk()
		u := msgUsage(s.u)
		frame(b, "message_delta", M{"type": "message_delta", "delta": M{"stop_reason": chatToStop(s.finish, s.hasTool), "stop_sequence": nil}, "usage": u})
		frame(b, "message_stop", M{"type": "message_stop"})
	}
}

// ---- responses renderer ----

func (s *Stream) renderResp(b *bytes.Buffer, e ev) {
	r := &s.rs
	emit := func(name string, m M) {
		m["type"], m["sequence_number"] = name, r.seq
		r.seq++
		frame(b, name, m)
	}
	if !r.started {
		r.started = true
		o := respObj(s.cid(), s.model, s.created, "in_progress", nil, nil)
		emit("response.created", M{"response": o})
		emit("response.in_progress", M{"response": o})
	}
	closeItem := func() {
		if r.kind == "" {
			return
		}
		t := r.text.String()
		ref := M{"item_id": r.itemID, "output_index": r.out}
		var item M
		switch r.kind {
		case "msg":
			emit("response.output_text.done", M{"item_id": r.itemID, "output_index": r.out, "content_index": 0, "text": t})
			emit("response.content_part.done", M{"item_id": r.itemID, "output_index": r.out, "content_index": 0,
				"part": M{"type": "output_text", "text": t, "annotations": []any{}}})
			item = msgItem(r.itemID, t, "completed")
		case "reason":
			emit("response.reasoning_summary_text.done", M{"item_id": r.itemID, "output_index": r.out, "summary_index": 0, "text": t})
			emit("response.reasoning_summary_part.done", M{"item_id": r.itemID, "output_index": r.out, "summary_index": 0,
				"part": M{"type": "summary_text", "text": t}})
			item = reasoningItem(r.itemID, t)
		default:
			ref["arguments"] = t
			emit("response.function_call_arguments.done", ref)
			item = callItem(r.itemID, r.callID, r.name, t, "completed")
		}
		emit("response.output_item.done", M{"output_index": r.out, "item": item})
		r.items = append(r.items, item)
		r.kind, r.key = "", ""
		r.out++
		r.text.Reset()
	}
	open := func(kind, key, prefix string, item func(id string) M) {
		if r.key == key {
			return
		}
		closeItem()
		r.kind, r.key, r.itemID = kind, key, newID(prefix)
		emit("response.output_item.added", M{"output_index": r.out, "item": item(r.itemID)})
	}
	if e.reasoning != "" {
		if r.key != "reason" {
			open("reason", "reason", "rs_", func(id string) M { return reasoningItem(id, "") })
			emit("response.reasoning_summary_part.added", M{"item_id": r.itemID, "output_index": r.out, "summary_index": 0,
				"part": M{"type": "summary_text", "text": ""}})
		}
		r.text.WriteString(e.reasoning)
		emit("response.reasoning_summary_text.delta", M{"item_id": r.itemID, "output_index": r.out, "summary_index": 0, "delta": e.reasoning})
	}
	if e.text != "" {
		if r.key != "msg" {
			open("msg", "msg", "msg_", func(id string) M { it := msgItem(id, "", "in_progress"); it["content"] = []any{}; return it })
			emit("response.content_part.added", M{"item_id": r.itemID, "output_index": r.out, "content_index": 0,
				"part": M{"type": "output_text", "text": "", "annotations": []any{}}})
		}
		r.text.WriteString(e.text)
		emit("response.output_text.delta", M{"item_id": r.itemID, "output_index": r.out, "content_index": 0, "delta": e.text})
	}
	if t := e.tc; t != nil {
		key := "call" + strconv.Itoa(t.idx)
		if t.id != "" || r.key != key {
			r.callID, r.name = t.id, t.name
			if r.callID == "" {
				r.callID = newID("call_")
			}
			open("call", key, "fc_", func(id string) M { return callItem(id, r.callID, r.name, "", "in_progress") })
		}
		if t.args != "" {
			r.text.WriteString(t.args)
			emit("response.function_call_arguments.delta", M{"item_id": r.itemID, "output_index": r.out, "delta": t.args})
		}
	}
	if e.done {
		closeItem()
		name, status := "response.completed", "completed"
		if s.finish == "length" {
			name, status = "response.incomplete", "incomplete"
		}
		emit(name, M{"response": respObj(s.cid(), s.model, s.created, status, r.items, &s.u)})
	}
}
