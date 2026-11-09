package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The upstream wraps every frame in {"statusCodeValue":200,"body":"<openai chunk>"}; the body is a JSON string or,
// on some frames, the object itself. Usage arrives on a later choices:[] frame than finish_reason (which itself often
// sits on delta), so the two are held back and emitted together — clients only read usage off the finish chunk.

type envelope struct {
	// statusCodeValue is documented numeric but numeric strings appear too.
	StatusCode any             `json:"statusCodeValue"`
	Body       json.RawMessage `json:"body"`
}

// response converts the envelope stream into a chat-completions response (see provider.Native). An error or queued
// first frame becomes a real error status, so the gateway classifies it like any other upstream failure.
func response(resp *http.Response, model string, stream bool) (*http.Response, error) {
	label := model
	if resp.Request != nil {
		if s, ok := resp.Request.Context().Value(signedKey{}).(signed); ok {
			label = s.model
		}
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	head, err := peekFrames(br)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	if head.status != 0 {
		resp.Body.Close()
		return errorResponse(resp, head.status, head.message), nil
	}
	if !head.sawData {
		resp.Body.Close()
		return nil, errors.New("qoder returned no answer (the stream ended before its first frame)")
	}

	if !stream {
		defer resp.Body.Close()
		return aggregate(resp, label, br, head.consumed)
	}

	pr, pw := io.Pipe()
	out := &http.Response{Status: resp.Status, StatusCode: resp.StatusCode, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor,
		ProtoMinor: resp.ProtoMinor, Header: http.Header{}, Request: resp.Request}
	out.Header.Set("Content-Type", "text/event-stream")
	out.Header.Set("Cache-Control", "no-cache")
	out.Body = pr
	go func() {
		defer resp.Body.Close()
		w := &frameWriter{w: pw, label: label}
		if err := w.write(head.consumed); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := w.consume(br); err != nil {
			pw.CloseWithError(err)
			return
		}
		w.end()
		pw.Close()
	}()
	return out, nil
}

// peekHead is the verdict on the first data frame.
type peekHead struct {
	status   int // 0 = fine
	message  string
	sawData  bool
	consumed []byte // every byte read while peeking, so the stream can be replayed from it
}

// peekFrames reads up to the first data frame without losing it; stream errors may come before any content.
func peekFrames(br *bufio.Reader) (peekHead, error) {
	var consumed bytes.Buffer
	check := func(inner string) peekHead {
		var env envelope
		if json.Unmarshal([]byte(inner), &env) == nil {
			if st, msg := frameError(env); st != 0 {
				return peekHead{status: st, message: msg}
			}
		}
		return peekHead{sawData: true, consumed: consumed.Bytes()}
	}
	for {
		line, err := br.ReadBytes('\n')
		consumed.Write(line)
		if err != nil && err != io.EOF {
			return peekHead{}, err
		}
		if t := strings.TrimSpace(string(line)); strings.HasPrefix(t, "data:") {
			inner := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
			if inner == "[DONE]" {
				return peekHead{sawData: true, consumed: consumed.Bytes()}, nil
			}
			return check(inner), nil
		}
		if err == io.EOF {
			// Not an SSE stream at all: a single JSON envelope is still one frame.
			if b := bytes.Clone(bytes.TrimSpace(consumed.Bytes())); len(b) > 0 && b[0] == '{' {
				consumed.Reset()
				consumed.WriteString("data: ")
				consumed.Write(b)
				consumed.WriteString("\n\n")
				return check(string(b)), nil
			}
			return peekHead{consumed: consumed.Bytes()}, nil
		}
	}
}

// frameError decides whether an envelope is a failure: a billing block, a queued model (a transient capacity signal,
// answered 429 so the gateway cools the key and the client retries), or any other non-200 status.
func frameError(env envelope) (int, string) {
	st := 200
	if n, ok := numOK(env.StatusCode); ok {
		st = int(n)
	}
	inner := bodyText(env.Body)
	// A queued model is a transient capacity signal whatever the envelope says (it usually comes as a 500 body), and a
	// 429 makes the gateway cool the key and fall back instead of holding this request for minutes.
	if isQueued(inner) {
		return 429, inner
	}
	if isBilling(inner) {
		return 403, inner
	}
	if st == 200 {
		return 0, ""
	}
	if st >= 400 && st <= 599 {
		return st, orElse(inner, "upstream status "+itoa(st))
	}
	return 502, orElse(inner, "upstream status "+itoa(st))
}

// isBilling: code 110 (daily count exceeded) / 112 (quota exhausted), or a pricingUrl pointer.
func isBilling(inner string) bool {
	if strings.Contains(strings.ToLower(inner), "pricingurl") {
		return true
	}
	var v struct {
		Code any `json:"code"`
	}
	if json.Unmarshal([]byte(inner), &v) == nil {
		switch c := v.Code.(type) {
		case string:
			if c == "110" || c == "112" {
				return true
			}
		case float64:
			if c == 110 || c == 112 {
				return true
			}
		}
	}
	return false
}

// isQueued: code 10605 / RESOURCE_LIMIT / isQueued, walking the way the value is nested (it is often JSON inside a
// JSON string inside another).
func isQueued(inner string) bool {
	if !strings.Contains(inner, "10605") && !strings.Contains(inner, "RESOURCE_LIMIT") && !strings.Contains(inner, "isQueued") {
		return false
	}
	var seen int
	return walkQueued(inner, &seen, 0)
}

func walkQueued(v any, seen *int, depth int) bool {
	if depth > 6 || *seen > 40 {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		*seen++
		if c, ok := x["code"]; ok {
			switch c := c.(type) {
			case string:
				if c == "10605" {
					return true
				}
			case float64:
				if c == 10605 {
					return true
				}
			}
		}
		if s, _ := x["subCode"].(string); s == "RESOURCE_LIMIT" {
			return true
		}
		if b, ok := x["isQueued"].(bool); ok && b {
			return true
		}
		for _, k := range []string{"error", "data", "body", "message", "cause", "result", "payload"} {
			if walkQueued(x[k], seen, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if walkQueued(item, seen, depth+1) {
				return true
			}
		}
	case string:
		if strings.HasPrefix(strings.TrimSpace(x), "{") || strings.HasPrefix(strings.TrimSpace(x), "[") {
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				return walkQueued(nested, seen, depth+1)
			}
		}
	}
	return false
}

func bodyText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// errorResponse turns a failed first frame into the error status the gateway classifies.
func errorResponse(resp *http.Response, status int, msg string) *http.Response {
	if msg == "" {
		msg = "qoder upstream error"
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error", "code": "qoder_upstream"}})
	return &http.Response{StatusCode: status, Status: itoa(status) + " " + http.StatusText(status), Proto: resp.Proto,
		ProtoMajor: resp.ProtoMajor, ProtoMinor: resp.ProtoMinor, Header: http.Header{"Content-Type": {"application/json"}},
		Request: resp.Request, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b))}
}

// frameWriter unwraps the envelope stream and re-emits plain chat SSE. Qoder puts finish_reason on the delta and the
// token counts on a later choices:[] frame, so the finish is held until usage arrives and the two go out as one
// terminal chunk — clients only read usage off the finish chunk.
type frameWriter struct {
	w     io.Writer
	label string
	id    string
	made  int64
	mdl   string

	usage      map[string]any
	finish     string
	sentFinish bool // a content chunk already carried finish_reason
	done       bool
}

func (fw *frameWriter) line(l string) error {
	t := strings.TrimSpace(strings.TrimSuffix(l, "\r"))
	if !strings.HasPrefix(t, "data:") {
		return nil
	}
	return fw.inner(strings.TrimSpace(strings.TrimPrefix(t, "data:")))
}

func (fw *frameWriter) inner(inner string) error {
	if fw.done {
		return nil
	}
	if inner == "[DONE]" {
		return fw.flush()
	}
	var env envelope
	if json.Unmarshal([]byte(inner), &env) != nil {
		return nil
	}
	if st, msg := frameError(env); st != 0 {
		// The 200 is already on the wire: report in band so the client (and the usage log) sees the failure.
		e, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error", "code": "qoder_stream_error"}})
		if err := fw.raw(string(e)); err != nil {
			return err
		}
		return fw.flush()
	}
	inner = bodyText(env.Body)
	if inner == "" {
		return nil
	}
	if inner == "[DONE]" {
		return fw.flush()
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(inner), &chunk) != nil {
		return fw.raw(inner)
	}
	if s, ok := chunk["id"].(string); ok && s != "" {
		fw.id = s
	}
	if n, ok := chunk["created"].(float64); ok {
		fw.made = int64(n)
	}
	if s, ok := chunk["model"].(string); ok && s != "" {
		fw.mdl = s
	}
	if u := canonicalUsage(chunk["usage"]); u != nil {
		fw.usage = u
	}
	finish := finishOf(chunk)
	if finish != "" {
		fw.finish = finish
	}
	if valuable(chunk) {
		if err := fw.raw(inner); err != nil {
			return err
		}
		if finish == "" {
			return nil
		}
		fw.sentFinish = true
	}
	if fw.usage == nil || fw.finish == "" {
		return nil
	}
	if err := fw.terminal(); err != nil {
		return err
	}
	return fw.flush()
}

// write takes the bytes the peek already consumed and replays them through the same path.
func (fw *frameWriter) write(consumed []byte) error {
	for _, l := range strings.Split(string(consumed), "\n") {
		if err := fw.line(l); err != nil {
			return err
		}
		if fw.done {
			return nil
		}
	}
	return nil
}

// consume reads the rest of the stream, stopping at the terminal frame: the upstream keeps the socket open with
// keepalives after [DONE], which would hang a client that drains the body.
func (fw *frameWriter) consume(r *bufio.Reader) error {
	for !fw.done {
		l, err := r.ReadString('\n')
		if l != "" {
			if werr := fw.line(l); werr != nil { // the last line may arrive without its newline
				return werr
			}
		}
		if err != nil {
			return nil
		}
	}
	return nil
}

// end is called when the upstream ended (or the client went away) without a terminal frame.
func (fw *frameWriter) end() {
	if !fw.done {
		_ = fw.flush()
	}
}

func (fw *frameWriter) raw(inner string) error {
	if fw.done {
		return nil
	}
	_, err := io.WriteString(fw.w, "data: "+strings.ReplaceAll(inner, "\n", "")+"\n\n")
	return err
}

func (fw *frameWriter) terminal() error {
	if fw.usage == nil && (fw.finish == "" || fw.sentFinish) {
		return nil
	}
	id := fw.id
	if id == "" {
		id = "chatcmpl-" + uuid.NewString()
	}
	created := fw.made
	if created == 0 {
		created = time.Now().Unix()
	}
	mdl := fw.mdl
	if mdl == "" {
		mdl = fw.label
	}
	finish := fw.finish
	if finish == "" {
		finish = "stop"
	}
	c := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": mdl,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}}
	if fw.sentFinish { // the finish already went out with its content: send usage alone, as OpenAI does
		c["choices"] = []any{}
	}
	if fw.usage != nil {
		c["usage"] = fw.usage
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	fw.usage, fw.finish = nil, finish
	fw.sentFinish = true
	_, err = io.WriteString(fw.w, "data: "+string(b)+"\n\n")
	return err
}

func (fw *frameWriter) flush() error {
	if fw.done {
		return nil
	}
	if err := fw.terminal(); err != nil {
		return err
	}
	fw.done = true
	_, err := io.WriteString(fw.w, "data: [DONE]\n\n")
	return err
}

// aggregate reads the whole stream and answers one chat.completion body.
func aggregate(resp *http.Response, label string, br *bufio.Reader, consumed []byte) (*http.Response, error) {
	var (
		id, mdl, finish string
		created         int64
		content, think  strings.Builder
		calls           = map[int]map[string]any{}
		firstSeen       bool
		streamErr       string
		usage           map[string]any
	)
	handle := func(inner string) {
		if inner == "" || inner == "[DONE]" {
			return
		}
		var env envelope
		if json.Unmarshal([]byte(inner), &env) != nil {
			return
		}
		if st, msg := frameError(env); st != 0 {
			streamErr = msg
			return
		}
		raw := bodyText(env.Body)
		var chunk map[string]any
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			return
		}
		if s, ok := chunk["id"].(string); ok && s != "" {
			id = s
		}
		if n, ok := chunk["created"].(float64); ok {
			created = int64(n)
		}
		if s, ok := chunk["model"].(string); ok && s != "" {
			mdl = s
		}
		if u := canonicalUsage(chunk["usage"]); u != nil {
			usage = u
		}
		if f := finishOf(chunk); f != "" {
			finish = f
		}
		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			if delta == nil {
				if m, ok := ch["message"].(map[string]any); ok {
					delta = m
				}
			}
			if s, ok := delta["content"].(string); ok {
				content.WriteString(s)
				firstSeen = firstSeen || s != ""
			}
			if s, ok := delta["reasoning_content"].(string); ok {
				think.WriteString(s)
				firstSeen = firstSeen || s != ""
			}
			list, _ := delta["tool_calls"].([]any)
			for _, tc := range list {
				t, _ := tc.(map[string]any)
				idx := int(num(t["index"]))
				cur := calls[idx]
				if cur == nil {
					cur = map[string]any{"index": idx, "type": "function", "function": map[string]any{"name": "", "arguments": ""}}
					calls[idx] = cur
				}
				if s, ok := t["id"].(string); ok && s != "" {
					cur["id"] = s
				}
				fn, _ := t["function"].(map[string]any)
				name, _ := fn["name"].(string)
				args, _ := fn["arguments"].(string)
				curFn := cur["function"].(map[string]any)
				if name != "" {
					curFn["name"] = name
				}
				curFn["arguments"] = curFn["arguments"].(string) + args
				firstSeen = true
			}
		}
	}

	for _, l := range strings.Split(string(consumed), "\n") {
		t := strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if strings.HasPrefix(t, "data:") {
			handle(strings.TrimSpace(strings.TrimPrefix(t, "data:")))
		}
	}
	for {
		l, err := br.ReadString('\n')
		t := strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if strings.HasPrefix(t, "data:") {
			inner := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
			if inner == "[DONE]" {
				break
			}
			if terminalMarker(inner) {
				handle(inner) // the last frame may carry the usage, so read it before stopping
				break
			}
			handle(inner)
		}
		if err != nil {
			break
		}
	}
	if !firstSeen && finish == "" {
		if streamErr != "" {
			return nil, errors.New("qoder upstream error: " + streamErr)
		}
		return nil, errors.New("qoder returned an empty completion")
	}
	if id == "" {
		id = "chatcmpl-" + uuid.NewString()
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if mdl == "" {
		mdl = label
	}
	if finish == "" {
		finish = "stop"
	}
	msg := map[string]any{"role": "assistant", "content": content.String()}
	if think.Len() > 0 {
		msg["reasoning_content"] = think.String()
	}
	if len(calls) > 0 {
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sortInts(idx)
		list := make([]any, 0, len(idx))
		for _, i := range idx {
			c := calls[i]
			delete(c, "index")
			list = append(list, c)
		}
		msg["tool_calls"] = list
		if content.Len() == 0 {
			msg["content"] = nil
		}
	}
	body := map[string]any{"id": id, "object": "chat.completion", "created": created, "model": mdl,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}}
	if usage != nil {
		body["usage"] = usage
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp.Header = http.Header{"Content-Type": {"application/json"}}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	return resp, nil
}

// terminalMarker: a done frame that carries no more content ends the read (the upstream keepalives follow).
func terminalMarker(inner string) bool {
	var env envelope
	if json.Unmarshal([]byte(inner), &env) != nil {
		return false
	}
	raw := bodyText(env.Body)
	if raw == "[DONE]" {
		return true
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(raw), &chunk) != nil {
		return false
	}
	return finishOf(chunk) != "" && !valuable(chunk) && canonicalUsage(chunk["usage"]) != nil
}

func finishOf(chunk map[string]any) string {
	choices, _ := chunk["choices"].([]any)
	if len(choices) > 0 {
		c, _ := choices[0].(map[string]any)
		if s, _ := c["finish_reason"].(string); s != "" {
			return s
		}
		if d, ok := c["delta"].(map[string]any); ok {
			if s, _ := d["finish_reason"].(string); s != "" {
				return s
			}
		}
	}
	if s, _ := chunk["finish_reason"].(string); s != "" {
		return s
	}
	return ""
}

func valuable(chunk map[string]any) bool {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	c, _ := choices[0].(map[string]any)
	d, _ := c["delta"].(map[string]any)
	if d == nil {
		return false
	}
	if s, _ := d["content"].(string); s != "" {
		return true
	}
	if s, _ := d["reasoning_content"].(string); s != "" {
		return true
	}
	if list, _ := d["tool_calls"].([]any); len(list) > 0 {
		return true
	}
	if s, _ := d["role"].(string); s != "" {
		return true
	}
	return false
}

// canonicalUsage folds Qoder's usage variants onto the OpenAI shape (prompt_tokens + cached tokens).
func canonicalUsage(v any) map[string]any {
	u, ok := v.(map[string]any)
	if !ok || u == nil {
		return nil
	}
	prompt, hasP := numOK(u["prompt_tokens"], u["input_tokens"])
	completion, hasC := numOK(u["completion_tokens"], u["output_tokens"])
	if !hasP && !hasC {
		return nil
	}
	details := map[string]any{}
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		for k, val := range d {
			details[k] = val
		}
	}
	cached, hasCached := numOK(details["cached_tokens"], u["cached_tokens"], u["prompt_cache_hit_tokens"], u["cache_read_input_tokens"])
	created, hasCreated := numOK(details["cache_creation_tokens"], u["cache_creation_input_tokens"])
	out := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}
	if total, ok := numOK(u["total_tokens"]); ok {
		out["total_tokens"] = total
	} else {
		out["total_tokens"] = prompt + completion
	}
	if hasCached {
		out["cached_tokens"] = cached
		details["cached_tokens"] = cached
	}
	if hasCreated {
		details["cache_creation_tokens"] = created
	}
	if len(details) > 0 {
		out["prompt_tokens_details"] = details
	}
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		out["completion_tokens_details"] = d
	}
	if r, ok := numOK(u["reasoning_tokens"]); ok {
		out["reasoning_tokens"] = r
	}
	return out
}

// numOK returns the first value that is a finite number.
func numOK(vals ...any) (float64, bool) {
	for _, v := range vals {
		switch x := v.(type) {
		case float64:
			return x, true
		case json.Number:
			if f, err := x.Float64(); err == nil {
				return f, true
			}
		case int:
			return float64(x), true
		}
	}
	return 0, false
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func orElse(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
