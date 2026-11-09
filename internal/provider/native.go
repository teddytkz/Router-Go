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
}

type NativeCall struct {
	URL    string
	Token  string         // access token (after OAuthSecret)
	Data   map[string]any // the connection's providerSpecificData
	Model  string         // upstream model id
	Body   map[string]any // chat-format body: a private copy
	Stream bool
}
