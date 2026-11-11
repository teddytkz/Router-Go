package gateway

import (
	"strings"
	"testing"
)

func TestProbeReply(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"chat stream":      {"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n", "Hello"},
		"chat json":        {`{"choices":[{"message":{"content":"Hello  there\n"}}]}`, "Hello there"},
		"messages stream":  {"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n", "Hi"},
		"messages json":    {`{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"Hi"}]}`, "Hi"},
		"responses stream": {"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Yo\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n", "Yo"},
		"reasoning only":   {"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"let me \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n", "(no answer yet, still thinking) let me think"},
		"answer wins":      {"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hmm\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n", "Hi"},
		"messages thought": {`{"content":[{"type":"thinking","thinking":"hmm"}]}`, "(no answer yet, still thinking) hmm"},
		"not an answer":    {"upstream says hello", ""},
	} {
		if got := probeReply([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: probeReply = %q, want %q", name, got, tc.want)
		}
	}
	if got := []rune(probeReply([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("é", 400) + `"}}]}`))); len(got) != 301 || got[300] != '…' {
		t.Errorf("a long reply is cut to 300 characters plus an ellipsis, got %d runes", len(got))
	}
}
