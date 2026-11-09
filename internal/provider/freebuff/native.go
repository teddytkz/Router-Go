package freebuff

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

const (
	defaultBase = "https://www.codebuff.com/api/v1"
	chatPath    = "/chat/completions"
	// The CLI's own identities: completions and the session/run calls differ only in the User-Agent.
	clientUA  = "ai-sdk/openai-compatible/1.0/codebuff"
	cliUA     = "codebuff-cli/0.2.36"
	cliVer    = "0.2.36"
	sessionHr = "/freebuff/session"
	runHr     = "/agent-runs"
	tierFree  = "free"
)

// sessionTTL is the fallback when the backend does not say when the claimed session ends.
const sessionTTL = time.Hour

var native = &provider.Native{Endpoints: endpoints, Request: request, Response: response}

// endpoints: the free tier has one chat host. A BaseURL other than the default (tests, a mirror) is used alone.
func endpoints(base string, _ map[string]any) []string {
	if strings.TrimSpace(base) == "" {
		base = defaultBase
	}
	return []string{strings.TrimRight(base, "/") + chatPath}
}

// apiBase recovers the /api/v1 root from the chat URL, so a mirror moves the session and run calls too.
func apiBase(chatURL string) string { return strings.TrimSuffix(chatURL, chatPath) }

type runKey struct{}

// finisher closes an agent run once the response body has been read.
type finisher struct {
	do      func(*http.Request) (*http.Response, error)
	base    string
	token   string
	runID   string
	status  string
	finOnce sync.Once
}

func (f *finisher) finish() {
	f.finOnce.Do(func() { finishRun(context.Background(), f.do, f.base, f.token, f.runID, f.status) })
}

// request registers the run and the session the free tier requires, then builds the completions call.
func request(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
	do := c.Do
	if do == nil {
		do = http.DefaultClient.Do
	}
	base := apiBase(c.URL)
	session, err := ensureSession(ctx, do, base, c.Token, c.Model)
	if err != nil {
		return nil, err
	}
	runID, err := startRun(ctx, do, base, c.Token, c.Model)
	if err != nil {
		return nil, err
	}
	body := c.Body
	shape(body, runID, session.instanceID)

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	f := &finisher{do: do, base: base, token: c.Token, runID: runID, status: "completed"}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, runKey{}, f), http.MethodPost, c.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("x-freebuff-client-version", cliVer)
	return req, nil
}

// response keeps the upstream's OpenAI SSE as it is; it only closes the run when the body is done.
// A failed call never reaches here (the gateway returns error statuses untouched), so that run expires upstream.
func response(resp *http.Response, _ string, _ bool) (*http.Response, error) {
	if resp.StatusCode >= 400 {
		return resp, nil
	}
	if resp.Request != nil {
		if f, ok := resp.Request.Context().Value(runKey{}).(*finisher); ok {
			resp.Body = &finishBody{ReadCloser: resp.Body, f: f}
		}
	}
	return resp, nil
}

type finishBody struct {
	io.ReadCloser
	f *finisher
}

func (b *finishBody) Close() error {
	b.f.finish()
	return b.ReadCloser.Close()
}

// shape adds the fields the CLI sends at the top level of the body. It runs after Prepare.
func shape(body map[string]any, runID, instanceID string) {
	meta := map[string]any{
		"run_id":           runID,
		"client_id":        randomID(8),
		"cost_mode":        tierFree,
		"trace_session_id": uuid.NewString(),
	}
	if instanceID != "" {
		meta["freebuff_instance_id"] = instanceID
	}
	body["codebuff_metadata"] = meta
	body["provider"] = map[string]any{"allow_fallbacks": false}
}

func randomID(n int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// agentID is the upstream root agent that serves model; unknown ids fall back like the CLI does.
func agentID(model string) string {
	if id, ok := agents[model]; ok {
		return id
	}
	return "base2-free"
}

var agents = map[string]string{
	"z-ai/glm-5.3-flash":              "base3-free-glm-5-3-flash",
	"deepseek/deepseek-v4-flash":      "base3-free-deepseek-flash",
	"openai/gpt-5.6-luna":             "base3-free-luna",
	"mimo/mimo-v2.5":                  "base3-free-mimo",
	"upstage/solar-pro4":              "base3-free-solar-pro4",
	"meta/muse-spark-1.3-contributor": "base3-free-muse-spark-1-3",
	"meta/muse-spark-1.2-contributor": "base3-free-muse-spark",
	"anthropic/claude-fable-5":        "base3-free-fable",
}

// ---- session ----

type session struct {
	instanceID string
	expiresAt  time.Time
}

type sessionCall struct {
	done chan struct{}
	s    session
	err  error
}

var (
	sessMu   sync.Mutex
	sessions = map[string]session{}
	inflight = map[string]*sessionCall{}
)

func sessionKey(token, model string) string { return token + "\x00" + model }

// ensureSession claims (or reuses) the per-model free-tier session. One claim is in flight per (token, model).
func ensureSession(ctx context.Context, do func(*http.Request) (*http.Response, error), base, token, model string) (session, error) {
	key := sessionKey(token, model)
	sessMu.Lock()
	if s, ok := sessions[key]; ok && s.expiresAt.After(time.Now()) {
		sessMu.Unlock()
		return s, nil
	}
	if c, ok := inflight[key]; ok {
		sessMu.Unlock()
		select {
		case <-c.done:
			return c.s, c.err
		case <-ctx.Done():
			return session{}, ctx.Err()
		}
	}
	c := &sessionCall{done: make(chan struct{})}
	inflight[key] = c
	sessMu.Unlock()

	c.s, c.err = claimSession(ctx, do, base, token, model)
	sessMu.Lock()
	delete(inflight, key)
	if c.err == nil {
		sessions[key] = c.s
	}
	sessMu.Unlock()
	close(c.done)
	return c.s, c.err
}

// claimSession posts the session claim. A status of "none" means the tier is not usable for this model right now.
func claimSession(ctx context.Context, do func(*http.Request) (*http.Response, error), base, token, model string) (session, error) {
	req, err := newJSONRequest(ctx, base+sessionHr, token, cliUA, nil)
	if err != nil {
		return session{}, err
	}
	req.Header.Set("x-freebuff-model", model)
	raw, status, err := send(do, req)
	if err != nil {
		return session{}, err
	}
	var r struct {
		Status     string `json:"status"`
		Model      string `json:"model"`
		Message    string `json:"message"`
		InstanceID string `json:"instanceId"`
		ExpiresAt  string `json:"expiresAt"`
	}
	_ = json.Unmarshal(raw, &r)
	if status == 401 {
		return session{}, errors.New("Freebuff session refused (401): the credential expired, sign in again")
	}
	if status >= 300 {
		return session{}, errors.New("Freebuff session refused (" + fmt.Sprint(status) + "): " + gateText(r.Status, r.Message, raw))
	}
	switch r.Status {
	case "active":
		exp := time.Now().Add(sessionTTL)
		if t, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil {
			exp = t
		}
		return session{instanceID: r.InstanceID, expiresAt: exp}, nil
	case "none":
		return session{}, errors.New("Freebuff has no free session for " + model + " right now — try again later")
	}
	return session{}, errors.New("Freebuff session refused: " + gateText(r.Status, r.Message, raw))
}

// gateText turns the backend's gate status into something the user can act on.
func gateText(status, message string, raw []byte) string {
	why := map[string]string{
		"country_blocked":    "Freebuff is not available in your region",
		"banned":             "the account is banned",
		"ip_capped":          "the IP cap for today is reached",
		"rate_limited":       "the session limit for this model is reached",
		"spend_limited":      "the spend limit is reached",
		"model_locked":       "the session is locked to another model until it expires",
		"model_unavailable":  "the model is not available right now",
		"premium_slot_taken": "the premium slot is taken",
	}[status]
	if why == "" {
		why = strings.TrimSpace(string(raw))
		if len(why) > 160 {
			why = why[:160] + "…"
		}
	}
	if message != "" {
		why += " — " + message
	}
	return why
}

// ---- agent run ----

func startRun(ctx context.Context, do func(*http.Request) (*http.Response, error), base, token, model string) (string, error) {
	payload := map[string]any{"action": "START", "agentId": agentID(model), "ancestorRunIds": []any{}}
	req, err := newJSONRequest(ctx, base+runHr, token, cliUA, payload)
	if err != nil {
		return "", err
	}
	raw, status, err := send(do, req)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", errors.New("Freebuff run registration failed (" + fmt.Sprint(status) + "): " + trim(raw))
	}
	var r struct {
		RunID string `json:"runId"`
	}
	if json.Unmarshal(raw, &r) != nil || r.RunID == "" {
		return "", errors.New("Freebuff run registration returned no run id: " + trim(raw))
	}
	return r.RunID, nil
}

// finishRun closes the run. It is best effort and capped, because the answer has already been served.
func finishRun(ctx context.Context, do func(*http.Request) (*http.Response, error), base, token, runID, status string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := newJSONRequest(ctx, base+runHr, token, cliUA, map[string]any{"action": "FINISH", "runId": runID, "status": status})
	if err != nil {
		return
	}
	if resp, err := do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}
}

func newJSONRequest(ctx context.Context, url, token, ua string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("x-freebuff-client-version", cliVer)
	return req, nil
}

func send(do func(*http.Request) (*http.Response, error), req *http.Request) ([]byte, int, error) {
	resp, err := do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, err
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
