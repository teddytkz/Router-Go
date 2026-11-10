package gateway

import "testing"

func TestProbeUsageShapes(t *testing.T) {
	for name, c := range map[string]struct {
		raw     string
		in, out int
	}{
		"chat stream": {"data: {\"choices\":[{\"delta\":{\"content\":\"p\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1}}\n\ndata: [DONE]\n", 9, 1},
		"messages":    {"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n", 12, 3},
		"responses":   {"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n", 7, 2},
		"json body":   {`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":5}}`, 4, 5},
		"no usage":    {"data: {\"choices\":[{\"delta\":{}}]}\n\ndata: [DONE]\n", 0, 0},
		"garbage":     {"not json at all", 0, 0},
	} {
		if in, out := probeUsage([]byte(c.raw)); in != c.in || out != c.out {
			t.Errorf("%s: got %d/%d, want %d/%d", name, in, out, c.in, c.out)
		}
	}
}
