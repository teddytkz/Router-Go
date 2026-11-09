package codebuddy

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

const chatPath = "/chat/completions"

// maxToolsBytes is the channel WAF's limit on the tools metadata; past it the descriptions are dropped.
const maxToolsBytes = 65536

// native exists because the channel check reads the request body: the conversation id must be derived from the
// session the body names, which a static header map cannot see. The upstream is otherwise OpenAI-compatible, so the
// response passes through untouched.
var native = &provider.Native{
	Endpoints: endpoints,
	Request:   request,
	Response:  func(resp *http.Response, _ string, _ bool) (*http.Response, error) { return resp, nil },
}

func endpoints(base string, _ map[string]any) []string { return []string{base + chatPath} }

func request(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
	body := c.Body
	if body == nil {
		body = map[string]any{}
	}
	prepare(body) // also the probe path: the upstream rejects a body without its leading system message
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	if c.Token != "" {
		h.Set("Authorization", "Bearer "+c.Token)
	}
	if stream, _ := body["stream"].(bool); stream || c.Stream { // ForceStream asks the upstream to stream even for a non-streaming client
		h.Set("Accept", "text/event-stream")
	}
	// The channel check wants one id shared by the request and its conversation-request header, and a separate
	// message id; the conversation id stays the same across the turns of one session.
	reqID := uuid.NewString()
	h.Set("X-Conversation-ID", conversationID(body))
	h.Set("X-Request-ID", reqID)
	h.Set("X-Conversation-Request-ID", reqID)
	h.Set("X-Conversation-Message-ID", uuid.NewString())
	return req, nil
}

// conversationID is stable for one conversation and fresh otherwise: a session key in the body wins, then the
// opening user text, so a retry of the same turn lands on the same upstream conversation.
func conversationID(body map[string]any) string {
	key := ""
	for _, f := range []string{"session_id", "prompt_cache_key", "conversation_id"} {
		if s, ok := body[f].(string); ok && strings.TrimSpace(s) != "" {
			key = s
			break
		}
	}
	if key == "" {
		if s := firstUserText(body["messages"]); s != "" {
			key = "cbai:" + s
		}
	}
	if key == "" {
		return uuid.NewString()
	}
	return uuidV5From(key)
}

// uuidV5From shapes a sha1 digest as a v5 UUID, which is what the official client's per-conversation id looks like.
func uuidV5From(key string) string {
	sum := sha1.Sum([]byte(key))
	h := hex.EncodeToString(sum[:])
	timeHigh := fmt.Sprintf("%04x", (hexN(h[12:16])&0x0fff)|0x5000)
	clockSeq := fmt.Sprintf("%04x", (hexN(h[16:20])&0x3fff)|0x8000)
	return h[0:8] + "-" + h[8:12] + "-" + timeHigh + "-" + clockSeq + "-" + h[20:32]
}

func hexN(s string) uint64 {
	var n uint64
	for _, c := range s {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			n |= uint64(c-'a') + 10
		}
	}
	return n
}

func firstUserText(v any) string {
	for _, m := range messages(v) {
		if m["role"] == "user" {
			return text(m["content"])
		}
	}
	return ""
}
