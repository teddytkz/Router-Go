package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"router-go/internal/config"
	"router-go/internal/db"
	"router-go/internal/provider"
)

func newClient(store *db.Store) *http.Client {
	return &http.Client{Transport: newPoolTransport(store, baseTransport())} // no Client.Timeout: it would cut long SSE streams
}

// ValidateBaseURL accepts only http(s) URLs with a host. Private hosts are allowed on purpose (Ollama, LAN gateways);
// only an authenticated admin can create nodes.
func ValidateBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("base URL must be a valid http(s) URL")
	}
	return raw, nil
}

const anthropicVersion = "2023-06-01"

func (g *Gateway) newReq(ctx context.Context, method, rawURL, key string, body []byte, stream, anthropic bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case anthropic:
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		req.Header.Set("anthropic-version", anthropicVersion)
	case key != "":
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	req.Header.Set("User-Agent", "router-go/"+config.Version)
	return req, nil
}

// ---- validation / test ----

type ValidateResult struct {
	Valid  bool     `json:"valid"`
	Error  string   `json:"error,omitempty"`
	Method string   `json:"method,omitempty"`
	Models []string `json:"models,omitempty"`
	// Rejected: the upstream answered 401/403, i.e. the key itself is bad (as opposed to an outage or a quota problem).
	Rejected bool `json:"-"`
	// Prompt/Completion: tokens a chat probe reported, for the usage history.
	Prompt, Completion int `json:"-"`
}

// Validate checks a base URL + key: GET /models first, then (when modelId is given) a 1-token probe.
func (g *Gateway) Validate(ctx context.Context, nodeType, baseURL, apiType, key, modelID string) ValidateResult {
	anthropic := nodeType == db.NodeTypeAnthropic
	base, err := ValidateBaseURL(baseURL)
	if err != nil {
		return ValidateResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	models, status, err := g.listModels(ctx, g.Client, base, key, anthropic)
	switch {
	case err == nil:
		return ValidateResult{Valid: true, Method: "models", Models: models}
	case status == 401 || status == 403:
		return ValidateResult{Error: "API key unauthorized", Method: "models"}
	case modelID == "":
		if status == 0 {
			return ValidateResult{Error: networkMessage(err)}
		}
		if status == 404 || status == 405 {
			return ValidateResult{Error: "/models endpoint not found — enter a model ID to validate through chat"}
		}
		return ValidateResult{Error: fmt.Sprintf("unexpected response (%d)", status)}
	}

	path, body := probeRequest(anthropic, apiType, modelID)
	b, _ := json.Marshal(body)
	req, _ := g.newReq(ctx, "POST", base+path, key, b, true, anthropic)
	resp, err := g.Client.Do(req)
	if err != nil {
		return ValidateResult{Error: networkMessage(err), Method: "chat"}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	switch {
	case resp.StatusCode < 300:
		return ValidateResult{Valid: true, Method: "chat"}
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ValidateResult{Error: "API key unauthorized", Method: "chat"}
	}
	return ValidateResult{Error: fmt.Sprintf("chat request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(snippet))), Method: "chat"}
}

func networkMessage(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "request timeout — provider not responding"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused — provider offline or unreachable"
	case strings.Contains(err.Error(), "no such host"):
		return "DNS lookup failed — check the domain"
	}
	return "network error: " + err.Error()
}

// listModels does GET {base}/models. status==0 means a transport error.
func (g *Gateway) listModels(ctx context.Context, c *http.Client, base, key string, anthropic bool) ([]string, int, error) {
	req, _ := g.newReq(ctx, "GET", base+"/models", key, nil, false, anthropic)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// OpenAI: {data:[{id}]}; some gateways: {models:[{id|name}]} or a bare array.
	var env struct {
		Data   []modelEntry `json:"data"`
		Models []modelEntry `json:"models"`
	}
	var list []modelEntry
	if json.Unmarshal(raw, &env) == nil {
		list = append(env.Data, env.Models...)
	} else if json.Unmarshal(raw, &list) != nil {
		return nil, 200, errors.New("unrecognised /models response")
	}
	seen, out := map[string]bool{}, []string{}
	for _, m := range list {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, 200, nil
}

type modelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ---- per-node model cache ----

type cacheEntry struct {
	models []string
	at     time.Time
}

type modelCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

const modelTTL = 5 * time.Minute

// NodeModels lists a node's upstream models, trying each active key until one works. force bypasses the cache.
func (g *Gateway) NodeModels(ctx context.Context, node *db.Node, force bool) ([]string, error) {
	g.cache.mu.Lock()
	e, ok := g.cache.m[node.ID]
	g.cache.mu.Unlock()
	if ok && !force && time.Since(e.at) < modelTTL {
		return e.models, nil
	}
	conns, err := g.Store.Connections(node.ID, true)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, errors.New("no active connections")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lastErr error
	for _, c := range conns {
		models, _, err := g.listModels(ctx, g.clientFor(node), node.BaseURL(), c.APIKey(), node.Type == db.NodeTypeAnthropic)
		if err == nil {
			g.cache.mu.Lock()
			g.cache.m[node.ID] = cacheEntry{models, time.Now()}
			g.cache.mu.Unlock()
			return models, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (g *Gateway) InvalidateModels(nodeID string) {
	g.cache.mu.Lock()
	delete(g.cache.m, nodeID)
	g.cache.mu.Unlock()
}

// probePrompt is what a test call asks: a real greeting and question rather than a bare "ping".
const probePrompt = "hi, who are you? are you ai?"

// probeRequest is the smallest valid request for a node: probePrompt with a one-token answer budget (16 on
// responses). It always streams: some upstreams only accept streaming chat (CodeBuddy), and others answer a stream
// in about a second while a non-stream call hangs.
func probeRequest(anthropic bool, apiType, model string) (path string, body map[string]any) {
	switch {
	case anthropic:
		return "/messages", map[string]any{"model": model, "max_tokens": 1, "stream": true, "messages": []any{map[string]any{"role": "user", "content": probePrompt}}}
	case apiType == "responses":
		return "/responses", map[string]any{"model": model, "input": probePrompt, "max_output_tokens": 16, "stream": true}
	}
	return "/chat/completions", map[string]any{"model": model, "max_tokens": 1, "stream": true, "stream_options": map[string]any{"include_usage": true}, "messages": []any{map[string]any{"role": "user", "content": probePrompt}}}
}

const (
	probeTimeout = 20 * time.Second
	// localProbeTimeout covers a provider whose upstream is a program on this machine (provider.Native.Local): the
	// program starts, opens a session and answers in one turn, which on freebuff has taken two minutes for an answer
	// a 20s probe would have reported as an unresponsive provider.
	localProbeTimeout = 180 * time.Second
)

// ProbeResult is what a one-token test call reports: how long it took and the tokens the upstream said it used
// (0 when the answer carries no usage).
type ProbeResult struct {
	Latency            time.Duration
	Prompt, Completion int
}

// probeUsage reads the token counts out of a probe answer: an SSE stream or a JSON body, in chat (prompt/completion),
// messages (input/output, split over message_start and message_delta) or responses (response.usage) shape.
func probeUsage(raw []byte) (prompt, completion int) {
	take := func(u map[string]any) {
		for _, k := range []string{"prompt_tokens", "input_tokens"} {
			if v, ok := u[k].(float64); ok && int(v) > prompt {
				prompt = int(v)
			}
		}
		for _, k := range []string{"completion_tokens", "output_tokens"} {
			if v, ok := u[k].(float64); ok && int(v) > completion {
				completion = int(v)
			}
		}
	}
	scan := func(b []byte) {
		var ev map[string]any
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		if u, ok := ev["usage"].(map[string]any); ok {
			take(u)
		}
		for _, k := range []string{"message", "response"} {
			if sub, ok := ev[k].(map[string]any); ok {
				if u, ok := sub["usage"].(map[string]any); ok {
					take(u)
				}
			}
		}
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			scan(bytes.TrimSpace(data))
		}
	}
	scan(bytes.TrimSpace(raw)) // a non-streaming JSON answer
	return prompt, completion
}

// ProbeModel sends a one-token request for model through node with the given key and reports how long it took and
// the tokens it used.
func (g *Gateway) ProbeModel(ctx context.Context, node *db.Node, key, model string) (ProbeResult, error) {
	if p, ok := nativeOf(node); ok {
		return g.probeNative(ctx, node, p, key, model)
	}
	format := g.formatFor(node, model)
	anthropic := format == "messages"
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	path, body := probeRequest(anthropic, format, model)
	if fix := g.bodyFix(node, model); fix != nil && format == "chat" {
		fix(body) // the same fix-ups as real requests, or a strict upstream would reject the probe
	}
	start := time.Now()
	for attempt := 0; ; attempt++ {
		b, _ := json.Marshal(body)
		req, _ := g.newReq(ctx, "POST", node.BaseURL()+path, key, b, true, anthropic)
		g.nodeHeaders(req, node)
		resp, err := g.clientFor(node).Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return ProbeResult{}, fmt.Errorf("no answer within %ds — the provider is not responding to chat requests", int(probeTimeout.Seconds()))
			}
			return ProbeResult{}, errors.New(networkMessage(err))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			if _, asked := body["stream_options"]; asked && attempt == 0 && bytes.Contains(raw, []byte("stream_options")) {
				delete(body, "stream_options") // a strict upstream: ask again without the usage option, tokens stay 0
				continue
			}
			return ProbeResult{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errorMessage(raw, resp.StatusCode))
		}
		in, out := probeUsage(raw)
		return ProbeResult{Latency: time.Since(start), Prompt: in, Completion: out}, nil
	}
}

// ValidateBuiltin checks a key for a built-in provider. Most of them have no usable GET /models, so the key is
// proven by a one-token chat request for the first model of the provider's catalogue.
func (g *Gateway) ValidateBuiltin(ctx context.Context, node *db.Node, key string) ValidateResult {
	p, ok := node.Builtin()
	if !ok || len(p.Models) == 0 {
		return ValidateResult{Error: "unknown built-in provider"}
	}
	if !p.Routable() {
		return ValidateResult{Error: p.Name + " cannot be used yet: " + p.Note}
	}
	if p.Category == provider.CategoryAPIKey && p.Native == nil { // a plain key provider: GET /models proves the key without spending credit
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		models, status, err := g.listModels(ctx, g.clientFor(node), node.BaseURL(), key, false)
		switch {
		case err == nil:
			return ValidateResult{Valid: true, Method: "models", Models: models}
		case status == 401 || status == 403:
			return ValidateResult{Error: "API key unauthorized", Method: "models", Rejected: true}
		}
	}
	pr, err := g.ProbeModel(ctx, node, key, p.Models[0].ID)
	if err != nil {
		return ValidateResult{Error: err.Error(), Method: "chat", Rejected: strings.HasPrefix(err.Error(), "HTTP 401") || strings.HasPrefix(err.Error(), "HTTP 403")}
	}
	return ValidateResult{Valid: true, Method: "chat", Prompt: pr.Prompt, Completion: pr.Completion}
}
