// Package provider defines the built-in providers (Cline, CodeBuddy, Qoder, Freebuff, OpenCode Free, …) and the
// registry they join. Each provider lives in its own sub-package (internal/provider/<name>) and registers itself in
// init(); internal/provider/builtin imports them all. A provider is static data (endpoint, auth, models, quirks)
// ported from XRouter-Next; at runtime it behaves like a provider node: a prefix, a base URL and a key pool.
package provider

import (
	"context"
	"slices"
	"strings"
	"sync"
)

const (
	CategoryOAuth  = "oauth"
	CategoryFree   = "free"
	CategoryAPIKey = "apikey"

	// Status says how far the port goes.
	StatusReady        = "ready"        // implemented and expected to work with a valid key
	StatusExperimental = "experimental" // plain protocol only: the upstream may reject clients it does not recognise
	StatusUnsupported  = "unsupported"  // needs a protocol that is not ported yet; cannot be routed
)

type Model struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format,omitempty"` // upstream wire format when it differs from the provider's: "responses" | "messages"
}

type Provider struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"` // model prefix: "cl/<model>"
	Name     string `json:"name"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Priority int    `json:"-"`       // display order inside its category (lower first)
	Note     string `json:"note"`    // shown to the user: caveats, what to paste
	Website  string `json:"website"` // sign-up / key page
	AuthHint string `json:"authHint"`
	NoAuth   bool   `json:"noAuth"` // usable without a key
	// BaseURL + "/chat/completions" (or "/responses", "/messages") is the upstream endpoint.
	BaseURL string            `json:"baseUrl"`
	Headers map[string]string `json:"-"`
	// ForceStream: the upstream misbehaves on non-streaming calls, so it is always asked to stream and the answer is
	// assembled into a normal response for non-streaming clients.
	ForceStream bool `json:"-"`
	// Envelope: non-streaming answers come wrapped as {"success":true,"data":{...}}.
	Envelope bool `json:"-"`
	// Passthrough: any model id is accepted (the catalogue below is only a starting point).
	Passthrough bool    `json:"passthrough"`
	Models      []Model `json:"models"`

	// OAuthBase is the host the sign-in endpoints live on (overridable in tests); OAuth is nil when the provider has
	// no browser sign-in.
	OAuthBase string `json:"-"`
	OAuth     *OAuth `json:"-"`
	// OAuthSecret turns an OAuth access token into the key sent upstream (Cline wants a "workos:" prefix).
	OAuthSecret func(accessToken string) string `json:"-"`
	// DynamicHeaders are per-request protocol headers (e.g. request ids), generated for every upstream call.
	DynamicHeaders func() map[string]string `json:"-"`
	// Prepare reshapes a chat-format request body the upstream is strict about (CodeBuddy wants a leading system message).
	// It receives a private copy and edits it in place.
	Prepare func(body map[string]any) `json:"-"`
	// Native replaces the OpenAI-style call entirely (see native.go); BaseURL then only names the default endpoint.
	Native *Native `json:"-"`

	// Usage reads the connection's live quota from the provider's own usage API (see usage.go); nil = not supported.
	Usage func(ctx context.Context, d Doer, c UsageCall) (Usage, error) `json:"-"`
	// FreeRefresh: how a free allowance renews: daily | weekly | monthly | one-time | rolling ("" = unknown or not free).
	FreeRefresh string `json:"-"`
}

var (
	mu  sync.RWMutex
	all []Provider
)

// Register adds a provider; called from the init() of each provider package.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	all = append(all, p)
}

var categoryOrder = map[string]int{CategoryOAuth: 0, CategoryFree: 1, CategoryAPIKey: 2}

// All returns every registered provider, ordered by category then priority then name.
func All() []Provider {
	mu.RLock()
	out := slices.Clone(all)
	mu.RUnlock()
	slices.SortFunc(out, func(a, b Provider) int {
		if c := categoryOrder[a.Category] - categoryOrder[b.Category]; c != 0 {
			return c
		}
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func ByID(id string) (Provider, bool) {
	for _, p := range All() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ByAlias finds a provider by its model prefix (case-sensitive, like custom prefixes).
func ByAlias(alias string) (Provider, bool) {
	for _, p := range All() {
		if p.Alias == alias {
			return p, true
		}
	}
	return Provider{}, false
}

// Routable: providers that can actually be served.
func (p Provider) Routable() bool { return p.Status != StatusUnsupported }

// HasModel: is model id usable on this provider by default (listed, or passthrough)?
func (p Provider) HasModel(id string) bool {
	if p.Passthrough && strings.TrimSpace(id) != "" {
		return true
	}
	for _, m := range p.Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ModelFormat is the wire format to use for a model: its own override, else "chat".
func (p Provider) ModelFormat(id string) string {
	for _, m := range p.Models {
		if m.ID == id && m.Format != "" {
			return m.Format
		}
	}
	return "chat"
}

// OverrideOAuth points a provider's sign-in endpoints at another host (tests); the returned func restores it.
func OverrideOAuth(id, base string) (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	for i := range all {
		if all[i].ID == id {
			old := all[i].OAuthBase
			all[i].OAuthBase = base
			return func() { mu.Lock(); all[i].OAuthBase = old; mu.Unlock() }
		}
	}
	return func() {}
}

// Override points a provider at another base URL (for tests and self-hosted mirrors); the returned func restores it.
func Override(id, baseURL string) (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	for i := range all {
		if all[i].ID == id {
			old := all[i].BaseURL
			all[i].BaseURL = baseURL
			return func() { mu.Lock(); all[i].BaseURL = old; mu.Unlock() }
		}
	}
	return func() {}
}
