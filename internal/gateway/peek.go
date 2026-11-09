package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// An upstream can answer 200 and still deliver nothing (Gemini aborting a malformed tool call, a stream that dies
// before the first token). Inside a combo that must fall through to the next member rather than reach the client.
// peekWriter holds the response back until it carries real content (answer text or a tool call; reasoning alone does
// not count), then releases it and passes everything else straight through. It is used for every member except the
// last, which has nobody to fall back to.

const (
	peekMaxBytes = 128 << 10
	peekDeadline = 20 * time.Second // a long reasoning-only start is released rather than held for ever
)

type peekWriter struct {
	dst       http.ResponseWriter
	cf        string
	hdr       http.Header
	status    int
	buf       bytes.Buffer
	line      []byte // unfinished SSE line
	first     time.Time
	committed bool
	content   bool
	noRetry   bool // the upstream ended with a finish reason where empty is legitimate (content filter, length)
}

func newPeek(dst http.ResponseWriter, cf string) *peekWriter {
	return &peekWriter{dst: dst, cf: cf, hdr: http.Header{}, status: 200}
}

func (p *peekWriter) Header() http.Header {
	if p.committed {
		return p.dst.Header()
	}
	return p.hdr
}

func (p *peekWriter) WriteHeader(code int) {
	if p.committed {
		p.dst.WriteHeader(code)
		return
	}
	p.status = code
}

func (p *peekWriter) Write(b []byte) (int, error) {
	if p.committed {
		return p.dst.Write(b)
	}
	if p.first.IsZero() {
		p.first = time.Now()
	}
	p.buf.Write(b)
	if strings.HasPrefix(p.hdr.Get("Content-Type"), "text/event-stream") {
		p.line = append(p.line, b...)
		for {
			i := bytes.IndexByte(p.line, '\n')
			if i < 0 {
				break
			}
			p.inspect(bytes.TrimSpace(p.line[:i]))
			p.line = p.line[i+1:]
		}
		if p.content || p.buf.Len() > peekMaxBytes || time.Since(p.first) > peekDeadline {
			return len(b), p.commit()
		}
	}
	return len(b), nil
}

func (p *peekWriter) Flush() {
	if f, ok := p.dst.(http.Flusher); ok && p.committed {
		f.Flush()
	}
}

func (p *peekWriter) commit() error {
	if p.committed {
		return nil
	}
	p.committed = true
	for k, v := range p.hdr {
		p.dst.Header()[k] = v
	}
	p.dst.WriteHeader(p.status)
	_, err := p.dst.Write(p.buf.Bytes())
	if f, ok := p.dst.(http.Flusher); ok {
		f.Flush()
	}
	return err
}

// release ends the peek once the attempt is over. It reports false when the response was empty: nothing has been
// sent to the client and the caller should try the next member. Otherwise the held response is sent.
func (p *peekWriter) release() bool {
	if p.committed {
		return true
	}
	if !strings.HasPrefix(p.hdr.Get("Content-Type"), "text/event-stream") && p.status < 400 {
		p.inspectJSON(p.buf.Bytes())
	} else if len(bytes.TrimSpace(p.line)) > 0 {
		p.inspect(bytes.TrimSpace(p.line))
	}
	if p.content || p.noRetry || p.status >= 400 || p.buf.Len() == 0 {
		_ = p.commit()
		return true
	}
	return false
}

func (p *peekWriter) inspect(line []byte) {
	rest, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	rest = bytes.TrimSpace(rest)
	if len(rest) == 0 || rest[0] != '{' {
		return
	}
	var ev map[string]any
	if json.Unmarshal(rest, &ev) != nil {
		return
	}
	p.scan(ev, true)
}

func (p *peekWriter) inspectJSON(body []byte) {
	var ev map[string]any
	if json.Unmarshal(body, &ev) == nil {
		p.scan(ev, false)
	}
}

func nonEmpty(v any) bool {
	switch x := v.(type) {
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	}
	return false
}

func noRetryFinish(s string) bool {
	return s == "content_filter" || s == "refusal" || s == "length" || s == "max_tokens" || s == "max_output_tokens"
}

// scan reads one event (streaming) or one whole body (non-stream) of the client format.
func (p *peekWriter) scan(ev map[string]any, stream bool) {
	switch p.cf {
	case "messages":
		if stream {
			switch ev["type"] {
			case "content_block_delta":
				d, _ := ev["delta"].(map[string]any)
				p.content = p.content || nonEmpty(d["text"]) || nonEmpty(d["partial_json"])
			case "content_block_start":
				b, _ := ev["content_block"].(map[string]any)
				p.content = p.content || b["type"] == "tool_use" || nonEmpty(b["text"])
			case "message_delta":
				d, _ := ev["delta"].(map[string]any)
				s, _ := d["stop_reason"].(string)
				p.noRetry = p.noRetry || noRetryFinish(s)
			}
			return
		}
		blocks, _ := ev["content"].([]any)
		for _, e := range blocks {
			b, _ := e.(map[string]any)
			p.content = p.content || b["type"] == "tool_use" || nonEmpty(b["text"])
		}
		s, _ := ev["stop_reason"].(string)
		p.noRetry = p.noRetry || noRetryFinish(s)
	case "responses":
		t, _ := ev["type"].(string)
		switch {
		case !stream:
			items, _ := ev["output"].([]any)
			for _, e := range items {
				it, _ := e.(map[string]any)
				if it["type"] == "function_call" || it["type"] == "custom_tool_call" {
					p.content = true
				}
				parts, _ := it["content"].([]any)
				for _, c := range parts {
					cm, _ := c.(map[string]any)
					p.content = p.content || nonEmpty(cm["text"])
				}
			}
			d, _ := ev["incomplete_details"].(map[string]any)
			r, _ := d["reason"].(string)
			p.noRetry = p.noRetry || noRetryFinish(r)
		case t == "response.output_text.delta" || t == "response.function_call_arguments.delta" || t == "response.custom_tool_call_input.delta":
			p.content = p.content || nonEmpty(ev["delta"])
		case t == "response.output_item.added" || t == "response.output_item.done":
			it, _ := ev["item"].(map[string]any)
			p.content = p.content || it["type"] == "function_call" || it["type"] == "custom_tool_call"
		case t == "response.incomplete":
			r, _ := ev["response"].(map[string]any)
			d, _ := r["incomplete_details"].(map[string]any)
			s, _ := d["reason"].(string)
			p.noRetry = p.noRetry || noRetryFinish(s)
		}
	default: // chat
		choices, _ := ev["choices"].([]any)
		for _, e := range choices {
			c, _ := e.(map[string]any)
			m, _ := c["delta"].(map[string]any)
			if !stream {
				m, _ = c["message"].(map[string]any)
			}
			p.content = p.content || nonEmpty(m["content"]) || nonEmpty(m["tool_calls"])
			if f, _ := c["finish_reason"].(string); noRetryFinish(f) {
				p.noRetry = true
			}
		}
	}
}
