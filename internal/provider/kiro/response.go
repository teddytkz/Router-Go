package kiro

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// contextWindow turns Kiro's "context usage %" into prompt tokens when the stream has no token counts.
const contextWindow = 200000

// response converts the event stream into a chat-completions response (see provider.Native).
func response(resp *http.Response, model string, stream bool) (*http.Response, error) {
	var names map[string]string
	if resp.Request != nil {
		names, _ = resp.Request.Context().Value(namesKey{}).(map[string]string)
	}
	out := &http.Response{Status: resp.Status, StatusCode: resp.StatusCode, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor, ProtoMinor: resp.ProtoMinor,
		Header: http.Header{}, Request: resp.Request}
	id, created := "chatcmpl-"+uuid.NewString(), time.Now().Unix()
	chunk := func(delta map[string]any, finish, usage any) []byte {
		c := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			c["usage"] = usage
		}
		b, _ := json.Marshal(c)
		return append(append([]byte("data: "), b...), '\n', '\n')
	}

	if !stream {
		defer resp.Body.Close()
		var content, reasoning strings.Builder
		var calls []any
		finish, usage, err := decode(resp.Body, names, func(d map[string]any) error {
			if s, ok := d["content"].(string); ok {
				content.WriteString(s)
			}
			if s, ok := d["reasoning_content"].(string); ok {
				reasoning.WriteString(s)
			}
			if list, ok := d["tool_calls"].([]any); ok {
				for _, c := range list {
					delete(c.(map[string]any), "index")
					calls = append(calls, c)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		msg := map[string]any{"role": "assistant", "content": content.String()}
		if reasoning.Len() > 0 {
			msg["reasoning_content"] = reasoning.String()
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
			if content.Len() == 0 {
				msg["content"] = nil
			}
		}
		b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion", "created": created, "model": model, "usage": usage,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}})
		out.Header.Set("Content-Type", "application/json")
		out.Body, out.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
		return out, nil
	}

	pr, pw := io.Pipe()
	out.Header.Set("Content-Type", "text/event-stream")
	out.Body = pr
	go func() { // closing pr (the client left) fails the next write, which stops decode and closes the upstream body
		defer resp.Body.Close()
		first := true
		finish, usage, err := decode(resp.Body, names, func(d map[string]any) error {
			if first {
				d["role"], first = "assistant", false
			}
			_, err := pw.Write(chunk(d, nil, nil))
			return err
		})
		var tail []byte
		if err != nil { // the 200 is already out: report in-band
			e, _ := json.Marshal(map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error", "code": "kiro_stream_error"}})
			tail = []byte("data: " + string(e) + "\n\n")
		} else {
			tail = chunk(map[string]any{}, finish, usage)
		}
		_, _ = pw.Write(append(tail, "data: [DONE]\n\n"...))
		pw.Close()
	}()
	return out, nil
}

type pendingTool struct {
	id, name string
	chunks   []string
	obj      map[string]any
}

type state struct {
	emit      func(map[string]any) error
	thinking  bool // inside <thinking>…</thinking> of a text event
	tools     []*pendingTool
	stop      string
	sawStop   bool
	out       bool // something reached the client
	chars     int  // runes of output, to estimate completion tokens
	ctxPct    float64
	in, outTk int
	cache     int
}

// decode reads the whole event stream. Text and reasoning go out as they arrive; tool calls are held until the end
// (their input arrives in fragments). It returns the OpenAI finish reason and usage (nil when unknown).
// shortcut: a <thinking> tag split across two events is not recognised; upgrade by buffering a tag-length tail.
func decode(r io.Reader, names map[string]string, emit func(map[string]any) error) (finish string, usage map[string]any, err error) {
	s := &state{emit: emit}
	for {
		f, err := readFrame(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("Kiro event stream: %w", err)
		}
		if t := f.headers[":message-type"]; t == "error" || t == "exception" {
			var p struct{ Message string }
			_ = json.Unmarshal(f.payload, &p)
			if p.Message == "" {
				p.Message = f.headers[":exception-type"] + f.headers[":error-code"]
			}
			return "", nil, errors.New("Kiro upstream " + t + ": " + p.Message)
		}
		var p map[string]any
		if len(bytes.TrimSpace(f.payload)) > 0 && json.Unmarshal(f.payload, &p) != nil {
			return "", nil, errors.New("Kiro event stream: payload is not valid JSON")
		}
		if err := s.event(f.headers[":event-type"], p); err != nil {
			return "", nil, err
		}
	}
	calls := s.toolCalls(names)
	if len(calls) > 0 {
		list := make([]any, len(calls))
		for i, c := range calls {
			list[i] = c
		}
		if err := emit(map[string]any{"tool_calls": list}); err != nil {
			return "", nil, err
		}
		s.out = true
	}
	if !s.out && !s.sawStop {
		return "", nil, errors.New("Kiro event stream ended without model output")
	}
	return s.finish(len(calls) > 0), s.usage(), nil
}

func (s *state) event(typ string, p map[string]any) error {
	switch typ {
	case "assistantResponseEvent", "codeEvent":
		return s.text(field(p, "content"), typ == "codeEvent")
	case "reasoningContentEvent":
		v := p
		if inner, ok := p["reasoningContentEvent"].(map[string]any); ok {
			v = inner
		}
		return s.send("reasoning_content", cmp.Or(field(v, "text"), field(v, "content")))
	case "toolUseEvent":
		s.toolUse(p)
	case "messageStopEvent", "metadataEvent", "MetadataEvent":
		for _, v := range []map[string]any{p, sub(p, "metadataEvent"), sub(p, "metadata")} {
			if r := stopReason(cmp.Or(str(v, "stopReason"), str(v, "stop_reason"))); r != "" {
				s.stop, s.sawStop = r, true
			}
		}
		if typ == "messageStopEvent" {
			s.sawStop = true
		}
	case "contextUsageEvent":
		s.ctxPct, _ = p["contextUsagePercentage"].(float64)
	case "metricsEvent":
		v := p
		if inner := sub(p, "metricsEvent"); inner != nil {
			v = inner
		}
		in, _ := v["inputTokens"].(float64)
		out, _ := v["outputTokens"].(float64)
		cr, _ := v["cacheReadInputTokens"].(float64)
		s.in, s.outTk, s.cache = int(in), int(out), int(cr)
	}
	return nil
}

// field reads a string untouched: streamed text keeps its leading and trailing spaces.
func field(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func sub(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }

// text sends assistant text; <thinking> sections (the "-thinking" prompt makes the model write them) become reasoning.
func (s *state) text(c string, code bool) error {
	for c != "" {
		switch {
		case s.thinking:
			end := strings.Index(c, "</thinking>")
			if end < 0 {
				return s.send("reasoning_content", c)
			}
			s.thinking = false
			if err := s.send("reasoning_content", c[:end]); err != nil {
				return err
			}
			c = strings.TrimPrefix(c[end+len("</thinking>"):], "\n")
		case !code && strings.Contains(c, "<thinking>"):
			start := strings.Index(c, "<thinking>")
			s.thinking = true
			if err := s.send("content", c[:start]); err != nil {
				return err
			}
			c = c[start+len("<thinking>"):]
		default:
			return s.send("content", c)
		}
	}
	return nil
}

func (s *state) send(key, text string) error {
	if text == "" {
		return nil
	}
	s.out = true
	s.chars += utf8.RuneCountInString(text)
	return s.emit(map[string]any{key: text})
}

// toolUse collects one fragment of a tool call: the name and id repeat, the input arrives piece by piece.
func (s *state) toolUse(p map[string]any) {
	name, id := strings.TrimSpace(str(p, "name")), str(p, "toolUseId")
	if name == "" {
		return
	}
	if id == "" {
		id = fmt.Sprintf("call_%d_%d", time.Now().Unix(), len(s.tools)+1)
	}
	var t *pendingTool
	for _, c := range s.tools {
		if c.id == id {
			t = c
		}
	}
	if t == nil {
		t = &pendingTool{id: id, name: name}
		s.tools = append(s.tools, t)
	}
	switch in := p["input"].(type) {
	case string:
		t.chunks = append(t.chunks, in)
	case map[string]any:
		t.obj = in
	}
}

// toolCalls: the finished calls in OpenAI form. A call whose input is not a JSON object is dropped (clients would
// choke on it); the rest of the answer still goes through.
func (s *state) toolCalls(names map[string]string) []map[string]any {
	var calls []map[string]any
	for _, t := range s.tools {
		var args []byte
		switch {
		case t.obj != nil:
			args, _ = json.Marshal(t.obj)
		case len(t.chunks) == 0:
			args = []byte("{}")
		default:
			args = []byte(strings.Join(t.chunks, ""))
		}
		var obj map[string]any
		if json.Unmarshal(args, &obj) != nil || obj == nil {
			continue
		}
		name := t.name
		if orig, ok := names[name]; ok {
			name = orig
		}
		s.chars += utf8.RuneCountInString(name) + len(args) // tool arguments are billed output too
		calls = append(calls, map[string]any{"index": len(calls), "id": t.id, "type": "function",
			"function": map[string]any{"name": name, "arguments": string(args)}})
	}
	return calls
}

var camel = regexp.MustCompile(`([a-z])([A-Z])`)

// stopReason normalises Kiro's spellings ("endTurn", "END_TURN", "max-tokens") to snake case.
func stopReason(v string) string {
	r := strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(camel.ReplaceAllString(strings.TrimSpace(v), "${1}_${2}")))
	switch r {
	case "maxtokens", "max_output_tokens", "length":
		return "max_tokens"
	}
	return r
}

func (s *state) finish(tools bool) string {
	switch {
	case tools:
		return "tool_calls"
	case s.stop == "max_tokens" || s.stop == "model_context_window_exceeded":
		return "length"
	case s.stop == "refusal" || strings.Contains(s.stop, "filter") || strings.Contains(s.stop, "guardrail"):
		return "content_filter"
	}
	return "stop"
}

func (s *state) usage() map[string]any {
	in, out := s.in, s.outTk
	if in == 0 && out == 0 && s.ctxPct > 0 { // no counts in the stream: estimate like XRouter-Next does
		in = int(s.ctxPct * contextWindow / 100)
		if s.chars > 0 {
			out = max(1, s.chars/4)
		}
	}
	if in == 0 && out == 0 {
		return nil
	}
	u := map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out}
	if s.cache > 0 {
		u["prompt_tokens_details"] = map[string]any{"cached_tokens": s.cache}
	}
	return u
}
