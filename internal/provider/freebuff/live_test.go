package freebuff

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveAgainstTheRealCLI drives the vendor's own freebuff binary, so it spends a real session window and needs a
// signed-in profile. It is therefore off unless explicitly asked for:
//
//	FREEBUFF_LIVE=1 FREEBUFF_LIVE_CONFIG_DIR=<profile with credentials.json> \
//	  go test ./internal/provider/freebuff/ -run TestLive -v
//
// Point FREEBUFF_LIVE_CWD at a scratch directory as well: the CLI records the conversation it has in the directory
// it was started in. FREEBUFF_LIVE_MODEL overrides the model (a catalogue key such as m-61c26317cc works: that is the
// only way to reach a row that has no wire id). Nothing here talks to the freebuff backend: the CLI does that
// itself, with its own login.
func TestLiveAgainstTheRealCLI(t *testing.T) {
	if os.Getenv("FREEBUFF_LIVE") == "" {
		t.Skip("set FREEBUFF_LIVE=1 and FREEBUFF_LIVE_CONFIG_DIR to spend a real freebuff session")
	}
	cfg := os.Getenv("FREEBUFF_LIVE_CONFIG_DIR")
	if cfg == "" {
		t.Fatal("FREEBUFF_LIVE_CONFIG_DIR is not set")
	}
	cwd := os.Getenv("FREEBUFF_LIVE_CWD")
	if cwd == "" {
		cwd = t.TempDir()
	}
	data := map[string]any{"configDir": cfg, "cwd": cwd, "timeoutSeconds": float64(300)}
	t.Cleanup(Close)
	c := clientFor(readOpts(data, ""))

	// the model is the thing most worth varying here: an id the profile cannot reach is coerced to a fallback rather
	// than refused, so a call only proves the pick stuck when the model is named explicitly
	model := os.Getenv("FREEBUFF_LIVE_MODEL")
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}

	// the second prompt goes to the same process and the same chat, which only ever grows: that is the path where a
	// stale answer from the first turn could be handed back as the second
	for _, tc := range []struct{ prompt, want string }{
		{"Reply with exactly: PONG", "PONG"},
		{"Reply with exactly: DONE", "DONE"},
	} {
		start := time.Now()
		answer, err := c.ask(context.Background(), model, tc.prompt)
		if err != nil {
			t.Fatalf("after %s: %v", time.Since(start).Round(time.Second), err)
		}
		t.Logf("answer after %s: %q", time.Since(start).Round(time.Second), answer)
		if !strings.Contains(strings.ToUpper(answer), tc.want) {
			t.Errorf("the CLI answered %q, which does not contain %s", answer, tc.want)
		}
	}
}
