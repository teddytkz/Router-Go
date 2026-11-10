package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// nativeOf: the built-in provider behind node when it brings its own wire protocol (see provider.Native).
func nativeOf(node *db.Node) (provider.Provider, bool) {
	p, ok := node.Builtin()
	return p, ok && p.Native != nil
}

// nativeDo sends a chat-format body (payload) to a Native provider and returns a chat-completions response. Error
// statuses come back untouched, so classification, cooldowns and key rotation work as for any other upstream.
// A 401/403/404 moves on to the provider's next endpoint.
func (g *Gateway) nativeDo(ctx context.Context, node *db.Node, p provider.Provider, psd map[string]any, key, model string, payload []byte, stream bool) (*http.Response, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if p.Native.Local != nil { // no HTTP at all: the provider reaches its upstream through a local program
		return p.Native.Local(ctx, provider.NativeCall{Token: key, Data: psd, Model: model, Body: body, Stream: stream})
	}
	urls := p.Native.Endpoints(node.BaseURL(), psd)
	client := g.clientFor(node)
	for i, u := range urls {
		req, err := p.Native.Request(ctx, provider.NativeCall{URL: u, Token: key, Data: psd, Model: model, Body: body, Stream: stream, Do: client.Do})
		if err != nil {
			return nil, err
		}
		g.nodeHeaders(req, node)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode >= 400 && i+1 < len(urls) && (resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404):
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		case resp.StatusCode >= 400:
			return resp, nil
		default:
			out, err := p.Native.Response(resp, model, stream)
			if err != nil {
				resp.Body.Close()
				return nil, err
			}
			return out, nil
		}
	}
	return nil, errors.New("provider has no endpoint")
}

// probeNative is ProbeModel for Native providers: a tiny real chat call through the provider's own request/response
// code, read to the end so an error inside the stream counts too.
func (g *Gateway) probeNative(ctx context.Context, node *db.Node, p provider.Provider, key, model string) (ProbeResult, error) {
	budget := probeTimeout
	if p.Native.Local != nil {
		budget = localProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_, body := probeRequest(false, "chat", model)
	delete(body, "stream_options") // a Native provider speaks its own protocol; usage is read from the converted stream if it is there
	b, _ := json.Marshal(body)
	psd, _ := ctx.Value(psdKey{}).(map[string]any)
	start := time.Now()
	resp, err := g.nativeDo(ctx, node, p, psd, key, model, b, true)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ProbeResult{}, fmt.Errorf("no answer within %ds — the provider is not responding to chat requests", int(budget.Seconds()))
		}
		return ProbeResult{}, errors.New(networkMessage(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 400 {
		return ProbeResult{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errorMessage(raw, resp.StatusCode))
	}
	if _, inband, ok := bytes.Cut(raw, []byte(`data: {"error"`)); ok { // the converted stream reports failures as an SSE error event
		line, _, _ := bytes.Cut(inband, []byte("\n"))
		return ProbeResult{}, errors.New(errorMessage([]byte(`{"error"`+string(line)), 0))
	}
	in, out := probeUsage(raw)
	return ProbeResult{Latency: time.Since(start), Prompt: in, Completion: out}, nil
}
