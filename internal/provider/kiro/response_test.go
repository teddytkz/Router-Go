package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// upstream wraps frames in a 200 response whose request carries the tool name map, as request() would leave it.
func upstream(names map[string]string, frames ...[]byte) *http.Response {
	req, _ := http.NewRequestWithContext(context.WithValue(context.Background(), namesKey{}, names), "POST", "http://kiro.test", nil)
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Request: req, Body: io.NopCloser(bytes.NewReader(bytes.Join(frames, nil)))}
}

func ev(typ string, payload any) []byte { return event(typ, payload) }

// sse parses a converted stream into its JSON chunks (and whether it ended with [DONE]).
func sse(t *testing.T, body string) (chunks []map[string]any, done bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		switch {
		case !ok:
		case data == "[DONE]":
			done = true
		default:
			var c map[string]any
			if err := json.Unmarshal([]byte(data), &c); err != nil {
				t.Fatalf("bad chunk %q: %v", data, err)
			}
			chunks = append(chunks, c)
		}
	}
	return
}

func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func delta(c map[string]any) map[string]any {
	return c["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
}

func TestResponseStreamTextToolsUsage(t *testing.T) {
	resp, err := response(upstream(map[string]string{"get_weather": "get.weather"},
		ev("assistantResponseEvent", map[string]any{"content": "Hel"}),
		ev("assistantResponseEvent", map[string]any{"content": "lo"}),
		ev("toolUseEvent", map[string]any{"name": "get_weather", "toolUseId": "tu1", "input": `{"city":`}),
		ev("toolUseEvent", map[string]any{"name": "get_weather", "toolUseId": "tu1", "input": `"Oslo"}`}),
		ev("toolUseEvent", map[string]any{"name": "get_weather", "toolUseId": "tu1", "stop": true}),
		ev("metricsEvent", map[string]any{"metricsEvent": map[string]any{"inputTokens": 11, "outputTokens": 4}}),
		ev("messageStopEvent", map[string]any{"stopReason": "tool_use"}),
	), "claude-x", true)
	if err != nil || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	chunks, done := sse(t, read(t, resp))
	if !done || len(chunks) != 4 { // two text chunks, one tool chunk, the closing chunk
		t.Fatalf("chunks = %d done=%v", len(chunks), done)
	}
	if d := delta(chunks[0]); d["role"] != "assistant" || d["content"] != "Hel" {
		t.Errorf("first delta = %v", d)
	}
	if d := delta(chunks[1]); d["role"] != nil || d["content"] != "lo" {
		t.Errorf("second delta = %v", d)
	}
	call := delta(chunks[2])["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "tu1" || call["index"] != float64(0) || fn["name"] != "get.weather" || fn["arguments"] != `{"city":"Oslo"}` {
		t.Errorf("tool call = %v", call)
	}
	last := chunks[3]
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] != "tool_calls" || last["usage"].(map[string]any)["prompt_tokens"] != float64(11) ||
		last["usage"].(map[string]any)["total_tokens"] != float64(15) || last["model"] != "claude-x" || last["object"] != "chat.completion.chunk" {
		t.Errorf("closing chunk = %v", last)
	}
}

func TestResponseNonStream(t *testing.T) {
	resp, err := response(upstream(nil,
		ev("reasoningContentEvent", map[string]any{"text": "hmm"}),
		ev("assistantResponseEvent", map[string]any{"content": "<thinking>plan</thinking>\nAnswer"}),
		ev("assistantResponseEvent", map[string]any{"content": " done"}),
		ev("contextUsageEvent", map[string]any{"contextUsagePercentage": 1.5}),
		ev("messageStopEvent", map[string]any{"stopReason": "max_tokens"}),
	), "m", false)
	if err != nil || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	var out struct {
		Object  string
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string
				Reasoning string `json:"reasoning_content"`
				ToolCalls []any  `json:"tool_calls"`
			}
		}
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		}
	}
	if err := json.Unmarshal([]byte(read(t, resp)), &out); err != nil || len(out.Choices) != 1 {
		t.Fatalf("body: %v %+v", err, out)
	}
	c := out.Choices[0]
	if out.Object != "chat.completion" || c.Message.Content != "Answer done" || c.Message.Reasoning != "hmmplan" || c.FinishReason != "length" || c.Message.ToolCalls != nil {
		t.Errorf("message = %+v", out)
	}
	// no token counts in the stream: prompt from the context %, completion from the text size (≈4 chars/token)
	if out.Usage.Prompt != 3000 || out.Usage.Completion < 1 {
		t.Errorf("estimated usage = %+v", out.Usage)
	}
}

func TestResponseNonStreamToolCallOnly(t *testing.T) {
	resp, _ := response(upstream(nil,
		ev("toolUseEvent", map[string]any{"name": "f", "toolUseId": "a", "input": map[string]any{"x": 1}}),
		ev("toolUseEvent", map[string]any{"name": "bad", "toolUseId": "b", "input": `{"broken`}), // dropped, not fatal
	), "m", false)
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   *string
				ToolCalls []struct {
					Index    *int
					ID       string
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
		}
	}
	if err := json.Unmarshal([]byte(read(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	c := out.Choices[0]
	if c.FinishReason != "tool_calls" || c.Message.Content != nil || len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].Index != nil ||
		c.Message.ToolCalls[0].Function.Name != "f" || c.Message.ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Errorf("message = %+v", c)
	}
}

func TestResponseErrors(t *testing.T) {
	boom := encodeFrame(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"}, []byte(`{"message":"slow down"}`))

	// non-stream: an error is an error
	if _, err := response(upstream(nil, ev("assistantResponseEvent", map[string]any{"content": "a"}), boom), "m", false); err == nil || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("non-stream exception: %v", err)
	}
	// stream: the 200 is out, so the error is an in-band SSE event followed by [DONE]
	resp, _ := response(upstream(nil, ev("assistantResponseEvent", map[string]any{"content": "a"}), boom), "m", true)
	body := read(t, resp)
	chunks, done := sse(t, body)
	if !done || len(chunks) != 2 || chunks[1]["error"].(map[string]any)["message"] == "" || !strings.Contains(body, "slow down") {
		t.Errorf("stream exception: %s", body)
	}
	// a truncated stream is an error, never a silently short answer
	cut := ev("assistantResponseEvent", map[string]any{"content": "partial"})
	resp, _ = response(upstream(nil, cut[:len(cut)-2]), "m", true)
	if body = read(t, resp); !strings.Contains(body, `"error"`) {
		t.Errorf("truncated stream: %s", body)
	}
	// nothing at all is an error as well, an explicit empty stop is not
	if _, err := response(upstream(nil), "m", false); err == nil {
		t.Error("empty stream accepted")
	}
	if _, err := response(upstream(nil, ev("messageStopEvent", map[string]any{"stopReason": "end_turn"})), "m", false); err != nil {
		t.Errorf("empty stop: %v", err)
	}
}

func TestResponseStopsWhenClientLeaves(t *testing.T) {
	var big [][]byte
	for range 50 {
		big = append(big, ev("assistantResponseEvent", map[string]any{"content": strings.Repeat("x", 100)}))
	}
	up := upstream(nil, big...)
	closed := make(chan struct{})
	up.Body = struct {
		io.Reader
		io.Closer
	}{up.Body, closerFunc(func() error { close(closed); return nil })}
	resp, _ := response(up, "m", true)
	buf := make([]byte, 10)
	resp.Body.Read(buf)
	resp.Body.Close() // the client is gone: the decoder must notice and release the upstream body
	<-closed
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestStopReason(t *testing.T) {
	for in, want := range map[string]string{"endTurn": "end_turn", "END_TURN": "end_turn", "tool-use": "tool_use", "MaxTokens": "max_tokens", "length": "max_tokens", "": ""} {
		if got := stopReason(in); got != want {
			t.Errorf("stopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
