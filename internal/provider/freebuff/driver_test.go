package freebuff

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"router-go/internal/provider"
)

// TestDriverTalksToATerminal is the one test that exercises the whole handshake: tmux session, the trust question,
// typing into the prompt box, and reading the answer back out of a growing chat log. It drives testdata/fakecli, not
// the vendor binary — the real one needs a signed-in account and spends a session window per call.
func TestDriverTalksToATerminal(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	bin := filepath.Join(t.TempDir(), "fakecli")
	build := exec.Command("go", "build", "-o", bin, "./testdata/fakecli")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakecli: %v\n%s", err, out)
	}
	cfg := t.TempDir()
	// the driver only takes ~ once it is a real profile dir
	data := map[string]any{"binary": bin, "configDir": cfg, "cwd": t.TempDir(), "timeoutSeconds": float64(30)}

	t.Cleanup(Close)
	answer := func(model, prompt string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := local(ctx, provider.NativeCall{Data: data, Model: model, Body: map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}, Stream: false})
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
			return "", nil
		}
		return out.Choices[0].Message.Content, nil
	}

	// every answer is the prompt as typed, with the reasoning block left behind
	got, err := answer("m/one", "first")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got != "echo: first" {
		t.Errorf("first answer = %q (reasoning blocks must be dropped, the echo must survive)", got)
	}
	// the same process answers again: the model did not change and the CLI's log only ever grows
	got, err = answer("m/one", "second")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got != "echo: second" {
		t.Errorf("second answer = %q", got)
	}

	// a prompt line that starts with "-" must reach the CLI as text, not be parsed by tmux as a flag
	if got, err = answer("m/one", "- item"); err != nil || got != "echo: - item" {
		t.Fatalf("dash prompt: %q %v", got, err)
	}

	// the single-session box is answered with Enter and the turn then completes
	os.WriteFile(filepath.Join(cfg, "takeover"), nil, 0o600)
	if got, err = answer("m/one", "after takeover"); err != nil || got != "echo: after takeover" {
		t.Fatalf("takeover call: %q %v", got, err)
	}

	// a different model is a different process, and its launch writes the model into the CLI's own settings and takes
	// the catalogue key that would outrank it (applyFreebuffCatalog). The new process re-reads the profile at launch,
	// so seeding it here is what it sees.
	os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"theme":"dark","freebuffModelKey":"m-key"}`), 0o600)
	if got, err = answer("m/two", "third"); err != nil || got != "echo: third" {
		t.Fatalf("third call: %q %v", got, err)
	}
	var settings map[string]any
	raw, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	if json.Unmarshal(raw, &settings) != nil || settings["freebuffModel"] != "m/two" || settings["theme"] != "dark" {
		t.Errorf("settings after launch = %s", raw)
	}
	if _, ok := settings["freebuffModelKey"]; ok {
		t.Errorf("the catalogue key still shadows the model: %s", raw)
	}
}

// TestDriverStreamsWhileTheTurnIsOpen: the answer frames leave the driver while the CLI still has the turn open, so a
// streaming client sees the first words instead of the whole answer at the end. fakecli writes half the answer, waits,
// then completes; the two content frames can only both be there if the driver relayed the half it saw first.
func TestDriverStreamsWhileTheTurnIsOpen(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	bin := filepath.Join(t.TempDir(), "fakecli")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakecli").CombinedOutput(); err != nil {
		t.Fatalf("build fakecli: %v\n%s", err, out)
	}
	cfg := t.TempDir()
	data := map[string]any{"binary": bin, "configDir": cfg, "cwd": t.TempDir(), "timeoutSeconds": float64(30)}
	t.Cleanup(Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := local(ctx, provider.NativeCall{Data: data, Model: "m/one", Stream: true, Body: map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "streamed"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var content strings.Builder
	frames := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue
		}
		if c := ev.Choices[0].Delta.Content; c != "" {
			frames++
			content.WriteString(c)
		}
	}
	if got := content.String(); got != "echo: streamed" {
		t.Errorf("streamed answer = %q", got)
	}
	if frames < 2 {
		t.Errorf("the answer arrived in %d frame(s), so nothing was relayed before the turn was complete", frames)
	}
}

// TestDriverNeedsTmux: the failure worth a message rather than a hang is a machine without tmux.
func TestDriverNeedsTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err == nil {
		t.Skip("tmux is installed, so its absence cannot be simulated here")
	}
	_, err := clientFor(opts{binary: "true", configDir: t.TempDir(), cwd: t.TempDir(), timeout: time.Second}).
		ask(context.Background(), "m/x", "hi")
	if err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Errorf("err = %v", err)
	}
}
