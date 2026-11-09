package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"router-go/internal/modelcaps"
)

// ---- single-model lookup: GET /v1/models/info?id=... and GET /v1/models/{prefix/model} ----

// capsOfID are the capabilities of a "prefix/model" id (zero for combos and unknown ids).
func (g *Gateway) capsOfID(id string) modelcaps.Caps {
	if n, m, err := g.resolve(id); err == nil {
		return g.capsOf(target{node: n, model: m})
	}
	return modelcaps.Caps{}
}

// modelInfo is the /v1/models entry plus what the dashboard knows about the model.
func (g *Gateway) modelInfo(m modelItem) map[string]any {
	out := map[string]any{"id": m.ID, "object": m.Object, "created": m.Created, "owned_by": m.OwnedBy,
		"kind": "llm", "endpoint": "/v1/chat/completions"}
	if m.OwnedBy == "combo" {
		out["kind"] = "combo" // the capabilities depend on the member that answers
		return out
	}
	c := g.capsOfID(m.ID)
	out["capabilities"] = map[string]bool{modelcaps.Vision: c.Vision, modelcaps.Reasoning: c.Reasoning, modelcaps.Audio: c.Audio}
	if c.Context > 0 {
		out["contextWindow"] = c.Context
	}
	return out
}

// findModel writes the entry of the model id when the key may use it.
func (g *Gateway) findModel(w http.ResponseWriter, key, id string) {
	items, err := g.visibleModels(key)
	if err != nil {
		writeErr(w, 500, "database error", "internal_server_error")
		return
	}
	i := slices.IndexFunc(items, func(m modelItem) bool { return m.ID == id })
	if i < 0 {
		writeErr(w, 404, "The model '"+id+"' does not exist or you do not have access to it.", "model_not_found")
		return
	}
	writeJSON(w, g.modelInfo(items[i]))
}

func (g *Gateway) modelByQuery(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authorize(w, r, "chat")
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, 400, "Missing required query param: id (e.g. ?id=prefix/model)", "bad_request")
		return
	}
	g.findModel(w, key, id)
}

// kindSlugs are XRouter's GET /v1/models/{kind} lists; this router only serves chat models, so they are empty.
var kindSlugs = []string{"image", "tts", "stt", "embedding", "image-to-text", "web"}

func (g *Gateway) modelByPath(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authorize(w, r, "chat")
	if !ok {
		return
	}
	id := chi.URLParam(r, "*") // provider-prefixed ids contain a slash
	if slices.Contains(kindSlugs, id) {
		writeJSON(w, map[string]any{"object": "list", "data": []any{}})
		return
	}
	g.findModel(w, key, id)
}

// ---- POST /v1/messages/count_tokens ----

// countTokens answers Anthropic's token counter with a local estimate (see estimateTokens: ~4 characters per token),
// since the real tokenizer of the upstream model is not available. Clients use it for budgeting, not billing.
func (g *Gateway) countTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.authorize(w, r, "messages"); !ok {
		return
	}
	var req map[string]any
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req) != nil {
		writeErrFmt(w, "messages", 400, "Invalid JSON body", "bad_request")
		return
	}
	n := estimateTokens(map[string]any{"system": req["system"], "tools": req["tools"], "messages": req["messages"]})
	writeJSON(w, map[string]any{"input_tokens": n})
}

// ---- POST /v1/responses/compact ----

type compactKey struct{}

// compact is /v1/responses with the compact flag: a Responses upstream is called on its /responses/compact path
// (OpenAI-style context compaction); chat and Anthropic upstreams just answer like /v1/responses (as in XRouter).
func (g *Gateway) compact(w http.ResponseWriter, r *http.Request) {
	g.serve("responses")(w, r.WithContext(context.WithValue(r.Context(), compactKey{}, true)))
}

func compactSuffix(r *http.Request, uf string) string {
	if uf == "responses" && r.Context().Value(compactKey{}) != nil {
		return "/compact"
	}
	return ""
}

// ---- POST /v1/api/chat (Ollama chat API) ----

// ollamaChat serves Ollama's chat endpoint through the chat pipeline: stream defaults to true (as in Ollama) and the
// answer is NDJSON {model, created_at, message, done}. Ollama-only fields (options, format, images) are not mapped.
func (g *Gateway) ollamaChat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeErr(w, 413, "request body too large or unreadable", "bad_request")
		return
	}
	var req map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&req) != nil || req == nil {
		writeErr(w, 400, "Invalid JSON body", "bad_request")
		return
	}
	stream := req["stream"] != false
	req["stream"] = stream
	body, _ := json.Marshal(req)
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	model, _ := req["model"].(string)
	ow := &ollamaWriter{ResponseWriter: w, model: model, stream: stream}
	g.serve("chat")(ow, r)
	ow.finish()
}

type ollamaCall struct{ name, args string }

// ollamaWriter converts the chat answer written by serve into Ollama's format. Errors (status >= 400) pass through.
type ollamaWriter struct {
	http.ResponseWriter
	model               string
	stream, ok, started bool // ok: the status is below 400
	done                bool
	buf                 []byte
	calls               []*ollamaCall
}

func (o *ollamaWriter) WriteHeader(code int) {
	o.started, o.ok = true, code < 400
	if o.ok {
		ct := "application/json"
		if o.stream {
			ct = "application/x-ndjson"
		}
		o.Header().Set("Content-Type", ct)
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *ollamaWriter) Write(p []byte) (int, error) {
	if !o.started {
		o.WriteHeader(200)
	}
	if !o.ok {
		return o.ResponseWriter.Write(p)
	}
	o.buf = append(o.buf, p...)
	if !o.stream {
		return len(p), nil
	}
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := strings.TrimSpace(string(o.buf[:i]))
		o.buf = o.buf[i+1:]
		if err := o.line(line); err != nil {
			return 0, err
		}
	}
}

func (o *ollamaWriter) Flush() {
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type chatToolCall struct {
	Index    int `json:"index"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (o *ollamaWriter) addCall(tc chatToolCall) {
	for len(o.calls) <= tc.Index {
		o.calls = append(o.calls, &ollamaCall{})
	}
	c := o.calls[tc.Index]
	c.name += tc.Function.Name
	c.args += tc.Function.Arguments
}

// line handles one line of the chat SSE stream.
func (o *ollamaWriter) line(l string) error {
	d, ok := strings.CutPrefix(l, "data:")
	if !ok {
		return nil
	}
	d = strings.TrimSpace(d)
	if d == "[DONE]" {
		return o.end()
	}
	var ch struct {
		Choices []struct {
			Delta struct {
				Content   string         `json:"content"`
				ToolCalls []chatToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(d), &ch) != nil || len(ch.Choices) == 0 {
		return nil
	}
	c := ch.Choices[0]
	for _, tc := range c.Delta.ToolCalls {
		o.addCall(tc)
	}
	if c.Delta.Content != "" {
		if err := o.emit(map[string]any{"role": "assistant", "content": c.Delta.Content}, false); err != nil {
			return err
		}
	}
	if c.FinishReason != "" {
		return o.end()
	}
	return nil
}

// emit writes one Ollama object; the final one (done) carries the tool calls collected so far.
func (o *ollamaWriter) emit(msg map[string]any, done bool) error {
	if done {
		var calls []any
		for _, c := range o.calls {
			var args any = map[string]any{}
			_ = json.Unmarshal([]byte(c.args), &args)
			calls = append(calls, map[string]any{"function": map[string]any{"name": c.name, "arguments": args}})
		}
		if calls != nil {
			msg["tool_calls"] = calls
		}
	}
	b, _ := json.Marshal(map[string]any{"model": o.model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "message": msg, "done": done})
	_, err := o.ResponseWriter.Write(append(b, '\n'))
	o.Flush()
	return err
}

func (o *ollamaWriter) end() error {
	if o.done {
		return nil
	}
	o.done = true
	return o.emit(map[string]any{"role": "assistant", "content": ""}, true)
}

// finish runs after serve: it closes a stream that ended without a finish reason, or converts the whole non-stream body.
func (o *ollamaWriter) finish() {
	if !o.ok {
		return
	}
	if o.stream {
		_ = o.end()
		return
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string         `json:"content"`
				ToolCalls []chatToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(o.buf, &resp) != nil || len(resp.Choices) == 0 {
		_, _ = o.ResponseWriter.Write(o.buf) // not a chat answer: hand it over as it is
		return
	}
	m := resp.Choices[0].Message
	for i, tc := range m.ToolCalls {
		tc.Index = i
		o.addCall(tc)
	}
	o.done = true
	_ = o.emit(map[string]any{"role": "assistant", "content": m.Content}, true)
}
