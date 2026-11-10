package provider

import (
	"context"
	"net/http"
)

// Native is for upstreams that do not speak OpenAI chat (Kiro): the provider builds its own request from a chat-format
// body and turns the answer back into a chat-completions response, so the rest of the gateway (translation hub,
// combos, key rotation, quota, usage) treats the provider as an ordinary chat upstream.
type Native struct {
	// Endpoints lists the URLs to try in order (a 401/403/404 moves to the next); base is the provider's BaseURL
	// and data the connection's providerSpecificData.
	Endpoints func(base string, data map[string]any) []string
	// Request builds the upstream call for call.URL. The gateway adds the provider's static and custom headers after it.
	Request func(ctx context.Context, call NativeCall) (*http.Request, error)
	// Response converts a 2xx upstream response into a chat-completions one: SSE chunks (with usage) when stream,
	// one JSON object otherwise. It must not touch error statuses: those are handled by the gateway as they are.
	Response func(resp *http.Response, model string, stream bool) (*http.Response, error)
	// Local replaces the HTTP round trip for an upstream reachable only through its own program on this machine
	// (freebuff drives the vendor's CLI). It returns a chat-completions response itself: SSE when call.Stream, one
	// JSON object otherwise. Endpoints and Request are unused when it is set.
	Local func(ctx context.Context, call NativeCall) (*http.Response, error)
}

type NativeCall struct {
	URL    string
	Token  string         // access token (after OAuthSecret)
	Data   map[string]any // the connection's providerSpecificData
	Model  string         // upstream model id
	Body   map[string]any // chat-format body: a private copy
	Stream bool
	// Do sends a side request (session claim, run registration, token exchange) through the gateway's client, so
	// proxy pools and its transport apply as they do to the chat call itself.
	Do func(*http.Request) (*http.Response, error)
}

// DoerFunc adapts a bare Do to Doer, for side calls that go through DoJSON or a provider's own request code.
type DoerFunc func(*http.Request) (*http.Response, error)

func (f DoerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
