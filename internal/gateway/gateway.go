// Package gateway serves the OpenAI-compatible /v1 API and proxies to custom OpenAI-compatible nodes.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"router-go/internal/db"
	"router-go/internal/provider"
)

type Gateway struct {
	Store    *db.Store
	Client   *http.Client
	cache    modelCache
	mu       sync.Mutex
	breakers map[string]*breaker
	limiter  limiter
	active   map[uint64]ActiveRequest
	nextID   uint64
}

// ActiveRequest is a request currently being served by an upstream (including while its response streams).
type ActiveRequest struct {
	Provider     string    `json:"provider"` // node id
	Model        string    `json:"model"`    // prefix/model
	ConnectionID string    `json:"connectionId"`
	Account      string    `json:"account"`
	Endpoint     string    `json:"endpoint"`
	Started      time.Time `json:"startedAt"`
}

// track registers an in-flight request; call the returned func when it finishes.
func (g *Gateway) track(a ActiveRequest) func() {
	g.mu.Lock()
	g.nextID++
	id := g.nextID
	g.active[id] = a
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		delete(g.active, id)
		g.mu.Unlock()
	}
}

// Active lists in-flight upstream requests, oldest first.
func (g *Gateway) Active() []ActiveRequest {
	g.mu.Lock()
	out := make([]ActiveRequest, 0, len(g.active))
	for _, a := range g.active {
		out = append(out, a)
	}
	g.mu.Unlock()
	slices.SortFunc(out, func(a, b ActiveRequest) int { return a.Started.Compare(b.Started) })
	return out
}

func New(store *db.Store) *Gateway {
	return &Gateway{Store: store, Client: newClient(store), cache: modelCache{m: map[string]cacheEntry{}},
		breakers: map[string]*breaker{}, active: map[uint64]ActiveRequest{}, limiter: limiter{gates: map[string]*gate{}}}
}

func (g *Gateway) Mount(r chi.Router) {
	r.Post("/v1/chat/completions", g.serve("chat"))
	r.Post("/v1/responses", g.serve("responses"))
	r.Post("/v1/messages", g.serve("messages"))
	r.Post("/v1/embeddings", g.serve("embeddings"))
	r.Post("/v1/images/generations", g.serve("images"))
	r.Post("/v1/audio/speech", g.serve("speech"))
	r.Post("/v1/audio/transcriptions", g.serve("transcriptions"))
	r.Post("/v1/videos/generations", g.serve("videos"))
	r.Post("/v1/videos/edits", g.serve("video-edits"))
	r.Post("/v1/videos/extensions", g.serve("video-extensions"))
	r.Get("/v1/videos/{id}", g.videoStatus(""))
	r.Get("/v1/videos/{id}/content", g.videoStatus("/content"))
	r.Get("/v1/models", g.models)
	r.Get("/v1/models/info", g.modelByQuery)
	r.Get("/v1/models/*", g.modelByPath)
	r.Post("/v1/messages/count_tokens", g.countTokens)
	r.Post("/v1/responses/compact", g.compact)
	r.Post("/v1/api/chat", g.ollamaChat)
	r.Get("/v1beta/models", g.geminiModels)
	r.Get("/v1beta/models/*", g.geminiModels)
	r.Post("/v1beta/models/*", g.geminiPost)
}

// writeJSON answers 200 with v as JSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, code string) {
	writeErrFmt(w, "chat", status, msg, code)
}

// writeErrFmt writes an error in the envelope of the client's API format (Anthropic differs from OpenAI).
func writeErrFmt(w http.ResponseWriter, format string, status int, msg, code string) {
	typ := "invalid_request_error"
	switch {
	case status == 401:
		typ = "authentication_error"
	case status == 403:
		typ = "permission_error"
	case status == 404:
		typ = "not_found_error"
	case status == 429:
		typ = "rate_limit_error"
	case status >= 500:
		typ = "api_error"
		if format != "messages" {
			typ = "server_error"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if format == "gemini" {
		_ = json.NewEncoder(w).Encode(geminiError(status, msg))
		return
	}
	if format == "messages" {
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

// authorize enforces settings.requireApiKey. It returns the presented key (possibly empty) and ok.
func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request, format string) (string, bool) {
	key := requestKey(r, format)
	s, _ := g.Store.Settings()
	if s["requireApiKey"] != false {
		if key == "" {
			writeErrFmt(w, format, 401, "Missing API key. Send 'Authorization: Bearer <key>'.", "invalid_api_key")
			return "", false
		}
		if !g.Store.ValidAPIKey(key) {
			writeErrFmt(w, format, 401, "Invalid API key provided", "invalid_api_key")
			return "", false
		}
	}
	return key, true
}

var errModel = errors.New("model not found")

// unsupportedError: a built-in provider whose protocol is not ported yet.
type unsupportedError struct{ provider, note string }

func (e *unsupportedError) Error() string { return e.provider + " cannot be used yet: " + e.note }

// notAddedError: the provider exists but the model has not been added to it, so it cannot be used yet.
type notAddedError struct{ provider, model string }

func (e *notAddedError) Error() string {
	return "Model '" + e.model + "' is not added to provider '" + e.provider + "'. Add it under Providers → " + e.provider + " → Models first."
}

// resolve maps "prefix/model" (or an alias) to its node and the upstream model name.
func (g *Gateway) resolve(model string) (*db.Node, string, error) {
	for range 2 { // one alias hop
		if i := strings.IndexByte(model, '/'); i > 0 {
			if n, err := g.Store.NodeByPrefix(model[:i]); err == nil {
				if p, ok := n.Builtin(); ok && !p.Routable() {
					return nil, "", &unsupportedError{p.Name, p.Note}
				}
				if !g.Store.HasModel(n.Prefix(), model[i+1:]) {
					return nil, "", &notAddedError{n.Name, model[i+1:]}
				}
				return n, model[i+1:], nil
			}
			break
		}
		a, ok := g.Store.Alias(model)
		if !ok {
			break
		}
		model = a
	}
	return nil, "", errModel
}

// ---- /v1/models ----

type modelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// AddedModels are the models of a provider that clients may use, in the order they were added.
func (g *Gateway) AddedModels(n *db.Node) []string {
	custom, _ := g.Store.CustomModels()
	out := []string{}
	for _, c := range custom {
		if c.ProviderAlias == n.Prefix() {
			out = append(out, c.ID)
		}
	}
	return out
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authorize(w, r, "chat")
	if !ok {
		return
	}
	data, err := g.visibleModels(key)
	if err != nil {
		writeErr(w, 500, "database error", "internal_server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// visibleModels are the combos and added models the key may use.
func (g *Gateway) visibleModels(key string) ([]modelItem, error) {
	acc := g.accessFor(key)
	nodes, err := g.Store.Nodes("")
	if err != nil {
		return nil, err
	}
	nodes = slices.DeleteFunc(nodes, func(n *db.Node) bool { return n.Type != db.NodeTypeOpenAI && n.Type != db.NodeTypeAnthropic })
	for _, p := range provider.All() { // built-in providers that have a key (or need none)
		if !p.Routable() {
			continue
		}
		if node, err := g.Store.Node(p.ID); err == nil {
			if conns, _ := g.Store.Connections(p.ID, true); len(conns) > 0 {
				nodes = append(nodes, node)
			}
		}
	}
	byNode := map[string][]modelItem{}
	for _, n := range nodes {
		for _, m := range g.AddedModels(n) {
			if acc.allowsModel(n, m) {
				byNode[n.ID] = append(byNode[n.ID], modelItem{n.Prefix() + "/" + m, "model", n.CreatedUnix(), n.Name})
			}
		}
	}
	data := []modelItem{}
	if combos, err := g.Store.Combos(); err == nil {
		for _, c := range combos {
			if acc.allowsCombo(c.Name) {
				data = append(data, modelItem{c.Name, "model", 0, "combo"})
			}
		}
	}
	for _, n := range nodes {
		data = append(data, byNode[n.ID]...)
	}
	return data, nil
}
