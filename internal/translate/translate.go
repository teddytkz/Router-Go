// Package translate converts between OpenAI Chat Completions ("chat"),
// OpenAI Responses ("responses") and Anthropic Messages ("messages") using chat as the hub.
// Gemini generateContent ("gemini") is a client-only format: Gemini requests are converted to chat and any upstream
// response is rendered as Gemini (non-stream and SSE); there is no Gemini upstream.
package translate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type Format string

const (
	Chat      Format = "chat"
	Responses Format = "responses"
	Messages  Format = "messages"
	Gemini    Format = "gemini" // client side only
)

// M is the JSON object type used throughout (identical to map[string]any).
type M = map[string]any

var (
	toChat   = map[Format]func(M) M{Messages: messagesToChat, Responses: responsesToChat, Gemini: geminiToChat}
	fromChat = map[Format]func(M) M{Messages: chatToMessages, Responses: chatToResponses}
	parsers  = map[Format]func(M) result{Chat: parseChatResp, Messages: parseMsgResp, Responses: parseRespResp}
	renders  = map[Format]func(result) M{Chat: renderChatResp, Messages: renderMsgResp, Responses: renderRespResp, Gemini: renderGeminiResp}
)

func check(from, to Format, body M) error {
	_, parses := parsers[from]
	_, converts := toChat[from]
	if !parses && !converts {
		return errors.New("translate: unknown source format " + string(from))
	}
	_, renders_ := renders[to]
	_, builds := fromChat[to]
	if !renders_ && !builds {
		return errors.New("translate: unknown target format " + string(to))
	}
	if body == nil {
		return errors.New("translate: nil body")
	}
	return nil
}

// Request converts a client request in format from to the upstream format to.
func Request(from, to Format, body map[string]any) (map[string]any, error) {
	if err := check(from, to, body); err != nil {
		return nil, err
	}
	if from == to {
		return body, nil
	}
	c := body
	if from != Chat {
		if toChat[from] == nil {
			return nil, errors.New("translate: " + string(from) + " cannot be a request source")
		}
		c = toChat[from](body)
	}
	if to != Chat {
		if fromChat[to] == nil {
			return nil, errors.New("translate: " + string(to) + " cannot be an upstream request format")
		}
		c = fromChat[to](c)
	}
	return c, nil
}

// Response converts a non-streaming upstream response in format from to the client format to.
func Response(from, to Format, body map[string]any) (map[string]any, error) {
	if err := check(from, to, body); err != nil {
		return nil, err
	}
	if from == to {
		return body, nil
	}
	if parsers[from] == nil {
		return nil, errors.New("translate: " + string(from) + " cannot be an upstream response format")
	}
	if renders[to] == nil {
		return nil, errors.New("translate: " + string(to) + " cannot be a response target")
	}
	return renders[to](parsers[from](body)), nil
}

// ErrorBody returns an error envelope in the client format.
func ErrorBody(to Format, status int, msg string) map[string]any {
	typ := "invalid_request_error"
	switch {
	case status == 401:
		typ = "authentication_error"
	case status == 429:
		typ = "rate_limit_error"
	case status >= 500 && to == Messages:
		typ = "api_error"
	case status >= 500:
		typ = "server_error"
	}
	if to == Messages {
		return M{"type": "error", "error": M{"type": typ, "message": msg}}
	}
	return M{"error": M{"message": msg, "type": typ, "code": typ}}
}

// ---- hub representation of a complete (non-stream) response ----

type call struct{ id, name, args string }

type usage struct {
	prompt, completion, cacheRead, cacheCreate int // prompt includes cached tokens
	set                                        bool
}

func (u *usage) merge(n usage) {
	if !n.set {
		return
	}
	u.set = true
	if n.prompt > 0 {
		u.prompt = n.prompt
	}
	if n.completion > 0 {
		u.completion = n.completion
	}
	if n.cacheRead > 0 {
		u.cacheRead = n.cacheRead
	}
	if n.cacheCreate > 0 {
		u.cacheCreate = n.cacheCreate
	}
}

type result struct {
	id, model, text, reasoning string
	finish                     string // chat vocabulary: stop|length|tool_calls|content_filter
	calls                      []call
	u                          usage
}

func usageChat(m M) usage {
	if m == nil {
		return usage{}
	}
	return usage{set: true, prompt: toInt(m["prompt_tokens"]), completion: toInt(m["completion_tokens"]),
		cacheRead: toInt(asMap(m["prompt_tokens_details"])["cached_tokens"])}
}

func usageMsg(m M) usage {
	if m == nil {
		return usage{}
	}
	cr, cc := toInt(m["cache_read_input_tokens"]), toInt(m["cache_creation_input_tokens"])
	return usage{set: true, prompt: toInt(m["input_tokens"]) + cr + cc, completion: toInt(m["output_tokens"]), cacheRead: cr, cacheCreate: cc}
}

func usageResp(m M) usage {
	if m == nil {
		return usage{}
	}
	return usage{set: true, prompt: toInt(m["input_tokens"]), completion: toInt(m["output_tokens"]),
		cacheRead: toInt(asMap(m["input_tokens_details"])["cached_tokens"])}
}

func chatUsage(u usage) M {
	m := M{"prompt_tokens": u.prompt, "completion_tokens": u.completion, "total_tokens": u.prompt + u.completion}
	if u.cacheRead > 0 {
		m["prompt_tokens_details"] = M{"cached_tokens": u.cacheRead}
	}
	return m
}

func msgUsage(u usage) M {
	m := M{"input_tokens": max(u.prompt-u.cacheRead-u.cacheCreate, 0), "output_tokens": u.completion}
	if u.cacheRead > 0 {
		m["cache_read_input_tokens"] = u.cacheRead
	}
	if u.cacheCreate > 0 {
		m["cache_creation_input_tokens"] = u.cacheCreate
	}
	return m
}

func respUsage(u usage) M {
	m := M{"input_tokens": u.prompt, "output_tokens": u.completion, "total_tokens": u.prompt + u.completion}
	if u.cacheRead > 0 {
		m["input_tokens_details"] = M{"cached_tokens": u.cacheRead}
	}
	return m
}

// stopToChat maps an Anthropic stop_reason to a chat finish_reason ("" stays "").
func stopToChat(s string) string {
	switch s {
	case "":
		return ""
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

func chatToStop(f string, hasTool bool) string {
	switch f {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	case "":
		if hasTool {
			return "tool_use"
		}
	}
	return "end_turn"
}

// ---- helpers ----

func asMap(v any) M      { m, _ := v.(map[string]any); return m }
func asList(v any) []any { l, _ := v.([]any); return l }
func str(v any) string   { s, _ := v.(string); return s }

func toInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		f, _ := n.Float64()
		return int(f)
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func copyKeys(dst, src M, keys ...string) {
	for _, k := range keys {
		if v := src[k]; v != nil {
			dst[k] = v
		}
	}
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func jsonStr(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decode(s string) M {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m M
	if d.Decode(&m) != nil {
		return nil
	}
	return m
}

// parseArgs turns a tool-call argument string into a JSON value ({} when invalid).
func parseArgs(s string) any {
	if m := decode(s); m != nil {
		return m
	}
	return M{}
}

func frame(b *bytes.Buffer, event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if event != "" {
		b.WriteString("event: " + event + "\n")
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
}

// textOf extracts text from a string or a list of text-ish parts/blocks.
func textOf(v any, sep string) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			pm := asMap(p)
			if t := str(pm["text"]); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, sep)
	}
	return ""
}

func isStream(b M) bool { v, _ := b["stream"].(bool); return v }

// simplify collapses a single text part into a plain string.
func simplify(parts []any) any {
	if len(parts) == 1 {
		if p := asMap(parts[0]); str(p["type"]) == "text" {
			return p["text"]
		}
	}
	return parts
}

func imageURL(p M) string {
	if s := str(p["image_url"]); s != "" {
		return s
	}
	return str(asMap(p["image_url"])["url"])
}

func imageToBlock(url string) M {
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		if meta, data, ok := strings.Cut(rest, ","); ok {
			return M{"type": "image", "source": M{"type": "base64", "media_type": strings.TrimSuffix(meta, ";base64"), "data": data}}
		}
	}
	return M{"type": "image", "source": M{"type": "url", "url": url}}
}

func blockToImageURL(b M) string {
	src := asMap(b["source"])
	if str(src["type"]) == "base64" {
		return "data:" + str(src["media_type"]) + ";base64," + str(src["data"])
	}
	return str(src["url"])
}

func lastMsg(msgs []any) M {
	if len(msgs) == 0 {
		return nil
	}
	return asMap(msgs[len(msgs)-1])
}
