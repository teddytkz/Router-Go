package freebuff

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"router-go/internal/provider"
)

func TestAgentID(t *testing.T) {
	cases := map[string]string{
		"upstage/solar-pro4":              "base3-free-solar-pro4",
		"meta/muse-spark-1.3-contributor": "base3-free-muse-spark-1-3",
		"something/new":                   "base2-free",
		"":                                "base2-free",
	}
	for model, want := range cases {
		if got := agentID(model); got != want {
			t.Errorf("agentID(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestPrepareMarker(t *testing.T) {
	// a system prompt without the marker gets it prepended, keeping the caller's text
	body := map[string]any{"messages": []any{map[string]any{"role": "system", "content": "You are a helpful bot."}}}
	prepare(body)
	if got := body["messages"].([]any)[0].(map[string]any)["content"]; got != marker+"\n\nYou are a helpful bot." {
		t.Errorf("system = %q", got)
	}

	// a prompt that already opens with a Buffy line is left alone
	for _, opening := range openings {
		body = map[string]any{"messages": []any{map[string]any{"role": "system", "content": opening + "\nmore"}}}
		prepare(body)
		if got := body["messages"].([]any)[0].(map[string]any)["content"]; got != opening+"\nmore" {
			t.Errorf("opening %q was rewritten to %q", opening, got)
		}
	}

	// no messages at all: a system message carrying the marker
	body = map[string]any{}
	prepare(body)
	if got := body["messages"].([]any)[0].(map[string]any)["content"]; got != marker {
		t.Errorf("empty body system = %q", got)
	}
}

func TestPrepareEndTurn(t *testing.T) {
	tool := map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{}}}
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}, "tools": []any{tool}}
	prepare(body)
	tools := body["tools"].([]any)
	if len(tools) != 2 || toolName(tools[1].(map[string]any)) != "end_turn" {
		t.Fatalf("tools = %v", tools)
	}
	prepare(body) // running twice must not add a second end_turn
	if n := len(body["tools"].([]any)); n != 2 {
		t.Errorf("after second pass tools has %d entries", n)
	}

	// no caller tools: nothing is added
	body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	prepare(body)
	if _, ok := body["tools"]; ok {
		t.Errorf("tools added without a caller tool array")
	}
}

func TestPrepareDropsReasoning(t *testing.T) {
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}, "reasoning": map[string]any{"effort": "high"}, "reasoning_effort": "high"}
	prepare(body)
	if _, ok := body["reasoning"]; ok {
		t.Error("reasoning kept")
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("reasoning_effort kept")
	}
}

func TestShapeMetadata(t *testing.T) {
	body := map[string]any{}
	shape(body, "run-1", "inst-1")
	meta := body["codebuff_metadata"].(map[string]any)
	if meta["run_id"] != "run-1" || meta["freebuff_instance_id"] != "inst-1" || meta["cost_mode"] != "free" {
		t.Errorf("metadata = %v", meta)
	}
	if meta["client_id"] == "" || meta["trace_session_id"] == "" {
		t.Errorf("missing generated ids: %v", meta)
	}
	if pv := body["provider"].(map[string]any)["allow_fallbacks"]; pv != false {
		t.Errorf("allow_fallbacks = %v", pv)
	}

	// without a session instance the field is simply absent
	body = map[string]any{}
	shape(body, "run-2", "")
	if _, ok := body["codebuff_metadata"].(map[string]any)["freebuff_instance_id"]; ok {
		t.Error("empty instance id set")
	}
}

// fakeDo answers the session/run/chat calls from one table and records what was asked.
type fakeDo struct {
	calls   []string
	session map[string]any
	status  int
}

func (f *fakeDo) Do(req *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, req.URL.Path)
	body := "{}"
	status := 200
	switch {
	case strings.HasSuffix(req.URL.Path, sessionHr):
		b, _ := json.Marshal(f.session)
		body = string(b)
		status = f.status
	case strings.HasSuffix(req.URL.Path, runHr):
		body = `{"runId":"run-abc"}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func call(t *testing.T, f *fakeDo, token, model string) provider.NativeCall {
	t.Helper()
	return provider.NativeCall{
		URL: defaultBase + chatPath, Token: token, Model: model, Stream: true,
		Body: map[string]any{"model": model, "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		Do:   f.Do,
	}
}

func TestRequestClaimsSessionAndRun(t *testing.T) {
	resetSessions(t)
	f := &fakeDo{session: map[string]any{"status": "active", "instanceId": "inst-1", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	req, err := request(context.Background(), call(t, f, "tok", "mimo/mimo-v2.5"))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("User-Agent"); got != clientUA {
		t.Errorf("chat User-Agent = %q", got)
	}
	if got := req.Header.Get("x-freebuff-client-version"); got != cliVer {
		t.Errorf("version header = %q", got)
	}
	if req.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("authorization = %q", req.Header.Get("Authorization"))
	}
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	meta := body["codebuff_metadata"].(map[string]any)
	if meta["run_id"] != "run-abc" || meta["freebuff_instance_id"] != "inst-1" {
		t.Errorf("metadata = %v", meta)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls = %v", f.calls)
	}

	// the session is cached: a second request claims nothing, only registers a new run
	f.calls = nil
	if _, err := request(context.Background(), call(t, f, "tok", "mimo/mimo-v2.5")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.HasSuffix(f.calls[0], runHr) {
		t.Errorf("cached session calls = %v", f.calls)
	}
}

func TestRequestSessionGates(t *testing.T) {
	resetSessions(t)
	for status, want := range map[string]string{
		"country_blocked": "not available in your region",
		"model_locked":    "locked to another model",
		"rate_limited":    "session limit",
	} {
		resetSessions(t)
		f := &fakeDo{session: map[string]any{"status": status}}
		f.status = 200
		_, err := request(context.Background(), call(t, f, "tok", "upstage/solar-pro4"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", status, err)
		}
		// a refused claim is not cached: the next call retries instead of failing forever
		if len(sessions) != 0 {
			t.Errorf("%s: refused session cached", status)
		}
	}

	// status "none" is a refusal too
	f := &fakeDo{session: map[string]any{"status": "none"}}
	if _, err := request(context.Background(), call(t, f, "tok", "upstage/solar-pro4")); err == nil {
		t.Error("status none accepted")
	}
}

func TestFinishBodyClosesRun(t *testing.T) {
	resetSessions(t)
	f := &fakeDo{session: map[string]any{"status": "active", "instanceId": "i"}}
	req, err := request(context.Background(), call(t, f, "tok", "mimo/mimo-v2.5"))
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {}\n\n")), Header: http.Header{}, Request: req}
	out, err := response(resp, "mimo/mimo-v2.5", true)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	out.Body.Close()
	out.Body.Close() // the run is finished once, not once per close
	var finished int
	for _, p := range f.calls {
		if strings.HasSuffix(p, runHr) {
			finished++
		}
	}
	if finished != 1 {
		t.Errorf("FINISH calls = %d (%v)", finished, f.calls)
	}
}

func resetSessions(t *testing.T) {
	t.Helper()
	sessMu.Lock()
	sessions = map[string]session{}
	inflight = map[string]*sessionCall{}
	sessMu.Unlock()
}
