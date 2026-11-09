package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"router-go/internal/provider"
)

const (
	defaultBase = "https://api3.qoder.sh"
	// sigPath is the inference path; BaseURL names the host only.
	sigPath = "/api/v2/service/pro/sse/agent_chat_generation"
	query   = "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
)

var native = &provider.Native{Endpoints: endpoints, Request: request, Response: response}

// endpoints: api2 first, then api3. This provider is fed a Personal Access Token, which is exchanged for a job token,
// and api3 answers job tokens with a 403 "Login expired" while api2 serves them; a device token (dt-…) is the reverse,
// and the gateway moves on after a 401/403/404 anyway. A BaseURL that is not a Qoder host (tests, a mirror) is used
// alone.
func endpoints(base string, _ map[string]any) []string {
	if !qoderHost(base) {
		return []string{strings.TrimRight(base, "/") + sigPath + query}
	}
	return []string{chatAltBase + sigPath + query, ideHost + sigPath + query}
}

// qoderHost: base still points at Qoder itself, so the provider's own hosts may be used.
func qoderHost(base string) bool {
	switch base {
	case "", defaultBase, chatAltBase, openAPIBase: // defaultBase is ideHost
		return true
	}
	return false
}

// signed is what response() needs from the request it followed.
type signed struct {
	model string
}

type signedKey struct{}

// request exchanges the PAT, looks up the model's model_config, then builds the re-ordered body and signs its exact
// bytes. Both are done here because the signature covers the body and the path.
func request(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
	do := c.Do
	if do == nil {
		do = http.DefaultClient.Do
	}
	token, userID, err := resolveCreds(ctx, provider.DoerFunc(do), base(c.URL), c.Token, str(c.Data["userId"]))
	if err != nil {
		return nil, err
	}
	key := strings.TrimPrefix(c.Model, "qoder/")
	cfg, err := modelConfig(ctx, provider.DoerFunc(do), base(c.URL), token, userID, key)
	if err != nil {
		return nil, err
	}
	// A PAT connection stores no user id: the one the exchange resolved is what keys the session and the machine id.
	psd := make(map[string]any, len(c.Data)+1)
	for k, v := range c.Data {
		psd[k] = v
	}
	psd["userId"] = userID
	plain, err := json.Marshal(qoderPayload(c.Model, c.Body, psd, cfg))
	if err != nil {
		return nil, err
	}
	encoded := encodeBody(plain)
	hdrs, err := cosyHeaders(encoded, c.URL, cosyCreds{UserID: userID, Token: token, MachineID: str(c.Data["machineId"])})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, signedKey{}, signed{model: key}), "POST", c.URL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Model-Key", key)
	h.Set("X-Model-Source", str(cfg["source"], "system"))
	h.Set("Accept-Encoding", "identity") // gzip breaks the signature the CDN checks
	for k, v := range hdrs {
		h.Set(k, v)
	}
	return req, nil
}

// base is the host part of an inference URL, so side calls follow an overridden BaseURL.
func base(inferenceURL string) string {
	if i := strings.Index(inferenceURL, sigPath); i >= 0 {
		return inferenceURL[:i]
	}
	return inferenceURL
}

func str(v any, def ...string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}
