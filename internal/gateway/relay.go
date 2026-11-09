package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"router-go/internal/db"
	"router-go/internal/translate"
)

// usage: prompt includes cached tokens (OpenAI convention); cache = cached/cache-read/cache-creation tokens.
type usage struct{ prompt, completion, cache int }

type failure struct {
	status int
	body   []byte
	header http.Header
	msg    string
	conn   *db.Connection
	scoped bool      // the request itself is wrong (not the account): a combo may still try another member
	retry  time.Time // earliest time a cooling provider is usable again
	uf     string    // wire format of body
}

// relay sends an upstream failure to the client. The raw upstream body is passed through when the client speaks the
// upstream's format (cf == f.uf); otherwise only the message is kept and re-wrapped in the client's error format.
func (f *failure) relay(w http.ResponseWriter, cf string) {
	if !f.retry.IsZero() {
		w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(f.retry).Seconds())+1))
	}
	if f.body == nil || cf != f.uf {
		msg := f.msg
		if f.body != nil {
			msg = errorMessage(f.body, f.status)
		}
		writeErrFmt(w, cf, f.status, msg, "upstream_error")
		return
	}
	copyHeaders(w, f.header)
	w.Header().Del("Content-Length")
	w.WriteHeader(f.status)
	_, _ = w.Write(f.body)
}

// errorMessage digs the human message out of the common error envelopes ({error:{message}}, {error:"x"}, {message}).
func errorMessage(body []byte, status int) string {
	var e struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, _ := v["message"].(string); m != "" {
				return m
			}
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return snippet(body, status)
}

var hopByHop = map[string]bool{"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Upgrade": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true, "Trailer": true, "Content-Length": true, "Content-Encoding": true}

func copyHeaders(w http.ResponseWriter, h http.Header) {
	for k, vs := range h {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

// relay streams a successful upstream response to the client and extracts token usage on the way.
// SSE is flushed chunk by chunk; the upstream is always cancelled through the request context.
func relay(w http.ResponseWriter, resp *http.Response, envelope bool) (usage, error) {
	copyHeaders(w, resp.Header)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return usage{}, err
		}
		if envelope {
			body = unwrapEnvelope(body)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return usageFromJSON(body), nil
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	sc := &sseScanner{}
	buf := make([]byte, 8<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			sc.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return sc.u, werr
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr == io.EOF {
			return sc.u, nil
		}
		if rerr != nil {
			return sc.u, rerr
		}
	}
}

type usageBody struct {
	Usage   *usageFields `json:"usage"`
	Message *struct {    // Anthropic message_start
		Usage *usageFields `json:"usage"`
	} `json:"message"`
	Response *struct {
		Usage *usageFields `json:"usage"`
	} `json:"response"`
}

type usageFields struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`  // Responses / Anthropic
	OutputTokens     int `json:"output_tokens"` // Responses / Anthropic
	PromptDetails    struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails struct {
		Cached int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	CacheRead   int `json:"cache_read_input_tokens"`     // Anthropic (not part of input_tokens)
	CacheCreate int `json:"cache_creation_input_tokens"` // Anthropic (not part of input_tokens)
}

func usageFromJSON(b []byte) usage {
	var ub usageBody
	if json.Unmarshal(b, &ub) != nil {
		return usage{}
	}
	f := ub.Usage
	if f == nil && ub.Response != nil {
		f = ub.Response.Usage
	}
	if f == nil && ub.Message != nil {
		f = ub.Message.Usage
	}
	if f == nil {
		return usage{}
	}
	anthropicCache := f.CacheRead + f.CacheCreate
	return usage{
		prompt:     max(f.PromptTokens, f.InputTokens+anthropicCache),
		completion: max(f.CompletionTokens, f.OutputTokens),
		cache:      max(f.PromptDetails.Cached, f.InputDetails.Cached, anthropicCache),
	}
}

// sseScanner watches the stream for the last `usage` object without altering it.
type sseScanner struct {
	line []byte
	u    usage
}

func (s *sseScanner) feed(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(s.line) < 1<<20 { // cap: a pathological line must not grow memory
				s.line = append(s.line, p...)
			}
			return
		}
		s.line = append(s.line, p[:i]...)
		s.handle(s.line)
		s.line = s.line[:0]
		p = p[i+1:]
	}
}

func (s *sseScanner) handle(line []byte) {
	if !bytes.HasPrefix(line, []byte("data:")) || !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	// Anthropic splits usage over message_start (input) and message_delta (output): keep the max of each.
	u := usageFromJSON(bytes.TrimSpace(line[5:]))
	s.u = usage{max(s.u.prompt, u.prompt), max(s.u.completion, u.completion), max(s.u.cache, u.cache)}
}

// relayConverted translates an upstream response of format uf into the client's format cf and sends it.
// SSE responses are converted event by event (flushed immediately); JSON responses are converted whole.
// ok=false with a nil error means the body could not be converted and nothing was written (the caller may fall back).
func relayConverted(w http.ResponseWriter, resp *http.Response, uf, cf, model string, envelope bool) (u usage, ok bool, err error) {
	from, to := translate.Format(uf), translate.Format(cf)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if rerr != nil {
			return usage{}, false, rerr
		}
		if envelope {
			raw = unwrapEnvelope(raw)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var in map[string]any
		if dec.Decode(&in) != nil {
			return usage{}, false, nil
		}
		out, cerr := translate.Response(from, to, in)
		if cerr != nil {
			return usage{}, false, nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(out)
		_, werr := w.Write(buf.Bytes())
		return usageFromJSON(raw), true, werr
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	conv := translate.NewStream(from, to, model)
	emit := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	up := &sseScanner{} // upstream usage incl. cache tokens; the converter's own count is the fallback
	finish := func(err error) (usage, bool, error) {
		u := up.u
		if p, c := conv.Usage(); u.prompt+u.completion == 0 {
			u.prompt, u.completion = p, c
		}
		return u, true, err
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var event string
	var data []string
	dispatch := func() error {
		defer func() { event, data = "", nil }()
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		up.handle([]byte("data: " + payload))
		return emit(conv.Feed(event, payload))
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return finish(err)
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return finish(err)
	}
	if err := dispatch(); err != nil {
		return finish(err)
	}
	return finish(emit(conv.Close()))
}

// unwrapEnvelope turns {"success":true,"data":{...}} into the data object; anything else is returned unchanged.
func unwrapEnvelope(raw []byte) []byte {
	var e struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Success != nil && len(e.Data) > 0 && e.Data[0] == '{' {
		return e.Data
	}
	return raw
}

// relayAggregated reads a streamed chat-completions answer and sends it to a non-streaming client as one response
// (converted to the client's format cf when that is not chat). Used for providers that must be asked to stream.
// ok=false with a nil error means nothing was written and the caller may fall back.
func relayAggregated(w http.ResponseWriter, resp *http.Response, cf, model string) (u usage, ok bool, err error) {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") { // the upstream answered plainly after all
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if rerr != nil {
			return usage{}, false, rerr
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, werr := w.Write(raw)
		return usageFromJSON(raw), true, werr
	}
	var (
		id, mdl, role, finish string
		created               json.Number
		content, reasoning    strings.Builder
		calls                 = map[int]map[string]any{}
		usageRaw              json.RawMessage
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data == "[DONE]" {
			continue
		}
		var ch struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Created json.Number     `json:"created"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.ID != "" {
			id = ch.ID
		}
		if ch.Model != "" {
			mdl = ch.Model
		}
		if ch.Created != "" {
			created = ch.Created
		}
		if len(ch.Usage) > 0 && string(ch.Usage) != "null" {
			usageRaw = ch.Usage
		}
		for _, c := range ch.Choices {
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			if c.Delta.Role != "" {
				role = c.Delta.Role
			}
			content.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.Reasoning)
			for _, tc := range c.Delta.ToolCalls {
				cur := calls[tc.Index]
				if cur == nil {
					cur = map[string]any{"index": tc.Index, "type": "function", "function": map[string]any{"name": "", "arguments": ""}}
					calls[tc.Index] = cur
				}
				if tc.ID != "" {
					cur["id"] = tc.ID
				}
				fn := cur["function"].(map[string]any)
				if tc.Function.Name != "" {
					fn["name"] = tc.Function.Name
				}
				fn["arguments"] = fn["arguments"].(string) + tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return usage{}, false, err
	}
	if role == "" {
		role = "assistant"
	}
	msg := map[string]any{"role": role, "content": content.String()}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		slices.Sort(idx)
		list := make([]any, 0, len(idx))
		for _, i := range idx {
			list = append(list, calls[i])
		}
		msg["tool_calls"] = list
		if content.Len() == 0 {
			msg["content"] = nil
		}
	}
	if finish == "" {
		finish = "stop"
	}
	if created == "" {
		created = json.Number(strconv.FormatInt(time.Now().Unix(), 10))
	}
	out := map[string]any{"id": id, "object": "chat.completion", "created": created, "model": mdl,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}}
	if len(usageRaw) > 0 {
		out["usage"] = usageRaw
	}
	if cf != "chat" {
		conv, cerr := translate.Response(translate.Chat, translate.Format(cf), out)
		if cerr != nil {
			return usage{}, false, nil
		}
		out = conv
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, werr := w.Write(buf.Bytes())
	return usageFromJSON(buf.Bytes()), true, werr
}
