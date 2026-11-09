package opencodefree

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"router-go/internal/provider"
	"router-go/internal/translate"
)

const (
	defaultBase = "https://opencode.ai/inference"
	ua          = "opencode/latest/2.1.4/cli"
	// anthropicVersion is what the /messages lane expects.
	anthropicVersion = "2023-06-01"
)

// lanePaths are the lanes under defaultBase. Model.Format stays empty so the gateway keeps translating into one chat
// body and one chat answer; the lane is chosen here, from the model id.
var lanePaths = map[string]string{
	"chat":      "/openai/v1/chat/completions",
	"responses": "/openai/v1/responses",
	"messages":  "/anthropic/v1/messages",
}

// laneOverride holds models that are not served on the chat lane: crouton-alpha-free is a Responses model, and
// union-alpha (not a free-catalogue id, but routable) is a Claude one.
var laneOverride = map[string]string{"crouton-alpha-free": "responses", "union-alpha": "messages"}

var native = &provider.Native{Endpoints: endpoints, Request: request, Response: response}

// laneOf is the upstream lane of a model; an effort suffix such as "model(high)" keeps its lane.
func laneOf(model string) string {
	if i := strings.LastIndex(model, "("); i > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:i])
	}
	if l, ok := laneOverride[model]; ok {
		return l
	}
	return "chat"
}

// endpoints is the default lane URL; request() swaps in the model's own lane. A base other than defaultBase is an
// override (a test mock or a mirror) and is used as it stands.
func endpoints(base string, _ map[string]any) []string {
	if base == "" {
		base = defaultBase
	}
	return []string{strings.TrimRight(base, "/") + lanePaths["chat"]}
}

// laneURL rewrites a lane path onto raw, so an overridden base keeps its own prefix.
func laneURL(raw, lane string) string {
	for _, p := range lanePaths {
		if strings.HasSuffix(raw, p) {
			return strings.TrimSuffix(raw, p) + lanePaths[lane]
		}
	}
	return raw + lanePaths[lane]
}

// request builds one upstream call: the chat body becomes the lane's own shape, and the free-tier client fingerprint
// is applied to it (environment marker + its own read/shell tools). The rename map for the caller's tool names rides
// on the context, where response() finds it through resp.Request.
func request(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
	lane := laneOf(c.Model)
	body := c.Body
	if lane != "chat" {
		var err error
		if body, err = translate.Request(translate.Chat, translate.Format(lane), body); err != nil {
			return nil, err
		}
	}
	body["model"] = c.Model
	body["stream"] = true
	switch lane {
	case "responses":
		responsesFixes(body)
	case "chat":
		dropAdaptiveEffort(body)
		injectReasoning(body)
	}
	renames := applyFingerprintTools(body, lane)

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, renamesKey{}, renames), "POST", laneURL(c.URL, lane), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ses := stableSession()
	for _, h := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		req.Header.Set(h, ses)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", "global")
	if lane == "messages" {
		req.Header.Set("anthropic-version", anthropicVersion)
	}
	return req, nil
}

type renamesKey struct{}

// dropAdaptiveEffort: the chat lane validates reasoning_effort against the plain OpenAI enum and 400s on "auto".
func dropAdaptiveEffort(body map[string]any) {
	if body["reasoning_effort"] == "auto" {
		delete(body, "reasoning_effort")
	}
}

// response turns the lane's answer into a chat-completions one (see provider.Native), restoring the caller's tool
// names on the way. The answer always streams: the lane is always asked to, and the gateway assembles it for a
// non-streaming client.
func response(resp *http.Response, model string, _ bool) (*http.Response, error) {
	var renames map[string]string
	if resp.Request != nil {
		renames, _ = resp.Request.Context().Value(renamesKey{}).(map[string]string)
	}
	lane := laneOf(model)

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		dec.UseNumber()
		var in map[string]any
		if err := dec.Decode(&in); err != nil {
			return nil, err
		}
		restoreToolNames(in, renames)
		if lane != "chat" {
			var err error
			if in, err = translate.Response(translate.Format(lane), translate.Chat, in); err != nil {
				return nil, err
			}
		}
		b, _ := json.Marshal(in)
		out := rebuild(resp, "application/json")
		out.Body, out.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
		return out, nil
	}

	pr, pw := io.Pipe()
	out := rebuild(resp, "text/event-stream")
	out.Body = pr
	go func() {
		defer resp.Body.Close()
		defer pw.Close()
		conv := translate.NewStream(translate.Format(lane), translate.Chat, model)
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
			if restored := restoreEvent(payload, renames); restored != "" {
				payload = restored
			}
			if b := conv.Feed(event, payload); len(b) > 0 {
				if _, err := pw.Write(b); err != nil {
					return err
				}
			}
			return nil
		}
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if err := dispatch(); err != nil {
					return // the client went away: closing pr fails the write, and the upstream body closes with us
				}
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
		}
		if sc.Err() == nil {
			if dispatch() != nil {
				return
			}
		}
		if b := conv.Close(); len(b) > 0 {
			_, _ = pw.Write(b)
		}
	}()
	return out, nil
}

// restoreEvent puts the caller's tool names back into one upstream event and returns it re-encoded; an unparsable
// payload is passed through as it came.
func restoreEvent(data string, renames map[string]string) string {
	if len(renames) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(data), &m) != nil {
		return ""
	}
	restoreToolNames(m, renames)
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// rebuild is resp with a fresh body and content type.
func rebuild(resp *http.Response, contentType string) *http.Response {
	out := &http.Response{Status: resp.Status, StatusCode: resp.StatusCode, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor,
		ProtoMinor: resp.ProtoMinor, Header: resp.Header.Clone(), Request: resp.Request}
	out.Header.Del("Content-Length")
	out.Header.Set("Content-Type", contentType)
	return out
}

// restoreToolNames puts the caller's own tool names back into a lane-format payload (chat, messages or responses
// shape, streamed or whole).
func restoreToolNames(payload any, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	switch p := payload.(type) {
	case map[string]any:
		if name, ok := p["name"].(string); ok { // messages tool_use block, responses function_call item
			if orig, ok := renames[name]; ok {
				p["name"] = orig
			}
		}
		for _, key := range []string{"item", "content_block", "message", "delta"} {
			if m, ok := p[key].(map[string]any); ok {
				restoreToolNames(m, renames)
				for _, c := range asList(m["tool_calls"]) {
					if cm, ok := c.(map[string]any); ok {
						restoreToolNames(cm["function"], renames)
					}
				}
			}
		}
		for _, key := range []string{"choices", "content", "output"} {
			restoreToolNames(p[key], renames)
		}
	case []any:
		for _, e := range p {
			restoreToolNames(e, renames)
		}
	}
}

// deepCopy keeps the shared fingerprint definitions from being edited by a caller's body.
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	}
	return v
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
