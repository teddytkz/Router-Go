package freebuff

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A live test of the driver needs the vendor binary, a signed-in profile and tmux, so nothing here starts a process.
// These cover the parts that decide what leaves this package: the prompt that goes in, the answer that comes out, and
// the settings file the launch reads.

func TestPromptTextFlattensRolesAndDropsSystem(t *testing.T) {
	// one user message is the ordinary chat request and goes in verbatim, as if typed into the CLI's prompt box
	got, err := promptText(map[string]any{"messages": []any{map[string]any{"role": "user", "content": "just this"}}})
	if err != nil || got != "just this" {
		t.Errorf("single message: %q %v", got, err)
	}
	got, err = promptText(map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "be terse"},
		map[string]any{"role": "user", "content": "first"},
	}})
	if err != nil || got != "first" {
		t.Errorf("system plus one user message: %q %v", got, err)
	}

	// a conversation gets role labels, because the CLI has nowhere else to put them
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "be terse"},
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "reply"},
		map[string]any{"role": "tool", "content": []any{map[string]any{"type": "text", "text": "tool out"}}},
	}}
	got, err = promptText(body)
	if err != nil {
		t.Fatal(err)
	}
	want := "User: first\n\nAssistant: reply\n\nTool result: tool out"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if strings.Contains(got, "be terse") {
		t.Error("the system message reached the CLI")
	}
}

func TestPromptTextNeedsText(t *testing.T) {
	for _, body := range []map[string]any{
		{},
		{"messages": []any{map[string]any{"role": "system", "content": "only a system message"}}},
		{"messages": []any{map[string]any{"role": "user", "content": "   "}}},
	} {
		if _, err := promptText(body); err == nil {
			t.Errorf("%v: expected an error", body)
		}
	}
}

func TestReadAnswerTakesTheLastCompletedTurn(t *testing.T) {
	dir := t.TempDir()
	write := func(msgs []any) { writeChat(t, dir, msgs) }

	write([]any{
		ai(true, text("reasoning", "thinking hard"), text("text", "the answer")),
	})
	if got, done := readAnswer(dir); !done || got != "the answer" {
		t.Errorf("got %q done=%v", got, done)
	}

	// the previous turn's finished answer is not this turn's answer
	write([]any{ai(true, text("text", "old")), map[string]any{"variant": "user", "isComplete": true}})
	if got, done := readAnswer(dir); done {
		t.Errorf("an unfinished turn reported %q", got)
	}

	write([]any{ai(false, text("text", "half"))})
	if got, done := readAnswer(dir); done {
		t.Errorf("a streaming turn reported %q", got)
	}

	// tool-only turns settle with an empty answer rather than hanging
	write([]any{ai(true, map[string]any{"type": "tool_use", "content": "ls"})})
	if got, done := readAnswer(dir); !done || got != "" {
		t.Errorf("tool-only turn: %q done=%v", got, done)
	}

	os.Remove(filepath.Join(dir, "chat-messages.json"))
	if _, done := readAnswer(dir); done {
		t.Error("a missing log reported an answer")
	}
}

func TestAdmissionNoticeReadsTheRefusal(t *testing.T) {
	// the box the CLI raises instead of starting a model, as it draws in a 210-column pane
	pane := strings.Join([]string{
		"   ▍Enter a coding task or / for commands          ",
		"╭───────────────────────────────────────────────────╮",
		"│ Not enough hours for DeepSeek V4.1 Flash Slow: an hour on it takes 1 hour.│",
		"│ Choose another model or visit https://freebuff.com/plans.                 │",
		"│ Enter: choose another model  Esc: back to draft                          │",
		"╰───────────────────────────────────────────────────╯",
		" DeepSeek V4.1 Flash Slow · /work · /model to change · Chat: New chat",
	}, "\n")
	got := admissionNotice(pane)
	for _, want := range []string{"Not enough hours", "DeepSeek V4.1 Flash Slow", "1 hour"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lost %q", got, want)
		}
	}
	// the same box doubles as a progress line while a session starts, and there it must not be read as a refusal:
	// turning a normal turn into an error would be worse than the timeout it replaces
	progress := "│ Starting your model session… Your message is saved. │\n│ Esc: back to draft │"
	if got := admissionNotice(progress); got != "" {
		t.Errorf("a starting session reported %q", got)
	}
	// the footer's model name is not the refuser, and a working pane has no box at all
	if got := admissionNotice("DeepSeek V4.1 Flash Slow · /work · /model to change"); got != "" {
		t.Errorf("a quiet pane reported %q", got)
	}
}

func TestTypableKeepsTextAndDropsKeystrokes(t *testing.T) {
	got := typable("a\r\nb\rc\x1b[31mred\x1b[0m\td\x00\x7f é日本")
	if want := "a\nb\nc[31mred[0m    d é日本"; got != want {
		t.Errorf("typable = %q, want %q", got, want)
	}
}

func TestTakeoverOffered(t *testing.T) {
	box := "│ Limited access without a subscription allows one session. Take over to stop the other session. │\n│ Enter: take over  Esc: back to draft │"
	if !takeoverOffered(box) {
		t.Error("the single-session box was not recognised")
	}
	// the other boxes spend hours or ask for a model, so they must never be auto-answered
	for _, other := range []string{"│ Enter: choose another model  Esc: back to draft │", "│ Enter: confirm and send  Esc: back to draft │", "Take over: Enter: take over"} {
		if takeoverOffered(other) {
			t.Errorf("%q was treated as a takeover", other)
		}
	}
}

func TestMessageCountCountsTurns(t *testing.T) {
	dir := t.TempDir()
	if messageCount(dir) != 0 {
		t.Error("a missing log should count 0")
	}
	writeChat(t, dir, []any{ai(true, text("text", "a")), ai(true, text("text", "b"))})
	if got := messageCount(dir); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	os.WriteFile(filepath.Join(dir, "chat-messages.json"), []byte("[{"), 0o600)
	if got := messageCount(dir); got != 0 { // a mid-write file counts as what it parsed
		t.Errorf("truncated log counted %d", got)
	}
}

func TestNewestChatIgnoresOtherProjects(t *testing.T) {
	c := &client{dir: t.TempDir(), opts: opts{cwd: "/tmp/fb-project"}}
	own := makeChat(t, c.dir, "fb-project", "own", time.Now().Add(-time.Hour))
	makeChat(t, c.dir, "other-project", "theirs", time.Now())
	if got := c.newestChat(); got != own {
		t.Errorf("newest = %q, want %q", got, own)
	}
	newer := makeChat(t, c.dir, "fb-project", "newer", time.Now())
	if got := c.newestChat(); got != newer {
		t.Errorf("newest = %q, want the newer chat %q", got, newer)
	}
}

func TestSaveModelPreservesTheRestOfSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"freebuffModelKey":"m-61c26317cc","theme":"dark","freebuffModel":"old/model"}`), 0o600)

	c := &client{dir: dir}
	if err := c.saveModel("deepseek/deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["freebuffModel"] != "deepseek/deepseek-v4-flash" || got["theme"] != "dark" {
		t.Errorf("settings = %v", got)
	}
	// the catalogue key outranks a written model id (applyFreebuffCatalog), so it has to go
	if _, ok := got["freebuffModelKey"]; ok {
		t.Errorf("the catalogue key survived: %v", got)
	}
	if err := c.saveModel(""); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path)
	if string(raw2) != string(raw) {
		t.Error("an empty model rewrote the settings")
	}
}

func TestSaveModelWritesACatalogKeyForACatalogRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"freebuffModel":"deepseek/deepseek-v4-flash"}`), 0o600)

	c := &client{dir: dir}
	if err := c.saveModel("m-61c26317cc"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &got) != nil {
		t.Fatal(string(raw))
	}
	// a catalogue row has no wire id, so the key is the only name for it, and the CLI reads it back from this field
	if got["freebuffModelKey"] != "m-61c26317cc" {
		t.Errorf("settings = %v", got)
	}
	// a stale named pick left beside the key makes the turn run (and bill) on that model instead
	if _, ok := got["freebuffModel"]; ok {
		t.Errorf("the stale named pick survived: %v", got)
	}
}

func TestSaveModelRefusesToDiscardUnreadableSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	c := &client{dir: dir}
	if err := c.saveModel("m/x"); err == nil {
		t.Fatal("expected an error")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "{not json" {
		t.Errorf("the file was overwritten: %q", raw)
	}
}

func TestChatResponseShapes(t *testing.T) {
	resp := chatResponse("m/x", "hello")
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	var body struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(raw, &body) != nil || body.Model != "m/x" || body.Choices[0].Message.Content != "hello" {
		t.Errorf("body = %s", raw)
	}
}

func TestStreamResponseRelaysEachDelta(t *testing.T) {
	ch := make(chan chunk, 2)
	ch <- chunk{delta: "lo"}
	ch <- chunk{delta: " there"}
	close(ch)
	stream := streamResponse("m/x", "hel", ch, func() {})
	if ct := stream.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("stream content type %q", ct)
	}
	raw, _ := io.ReadAll(stream.Body)
	if !strings.Contains(string(raw), `"content":"hel"`) || !strings.Contains(string(raw), `"content":" there"`) {
		t.Errorf("stream = %s", raw)
	}
	if !strings.Contains(string(raw), `"finish_reason":"stop"`) || !strings.HasSuffix(string(raw), "data: [DONE]\n\n") {
		t.Errorf("stream = %s", raw)
	}
	// three separate content frames, not one assembled answer
	if got := strings.Count(string(raw), "chat.completion.chunk"); got != 5 {
		t.Errorf("frames = %d, want role + 3 content + finish before DONE: %s", got, raw)
	}

	// a turn that dies mid-answer reports the failure in-band instead of pretending it stopped cleanly
	fail := make(chan chunk, 1)
	fail <- chunk{err: io.ErrUnexpectedEOF}
	close(fail)
	raw, _ = io.ReadAll(streamResponse("m/x", "", fail, func() {}).Body)
	if !strings.Contains(string(raw), `"error"`) || strings.Contains(string(raw), `"finish_reason":"stop"`) {
		t.Errorf("failure stream = %s", raw)
	}
}

// A turn's text is visible in the log before the CLI marks it complete, which is what lets a streaming caller see it grow.
func TestReadTextReportsTheGrowingTurn(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, []any{ai(false, text("reasoning", "thinking"), text("text", "half an ans"))})
	got, done, ok := readText(dir)
	if !ok || done || got != "half an ans" {
		t.Errorf("partial turn: %q done=%v ok=%v", got, done, ok)
	}
	writeChat(t, dir, []any{ai(true, text("reasoning", "thinking"), text("text", "half an answer"))})
	if got, done, ok = readText(dir); !ok || !done || got != "half an answer" {
		t.Errorf("finished turn: %q done=%v ok=%v", got, done, ok)
	}
}

func TestExpandHomeAndShellQuote(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/.config/manicode/freebuff"); got != filepath.Join(home, ".config/manicode/freebuff") {
		t.Errorf("expandHome = %q", got)
	}
	if got := expandHome("/opt/freebuff"); got != "/opt/freebuff" {
		t.Errorf("expandHome = %q", got)
	}
	if got := shellQuote("it's here"); got != `'it'\''s here'` {
		t.Errorf("shellQuote = %q", got)
	}
}

func TestReadOptsDefaultsAndOverrides(t *testing.T) {
	o := readOpts(nil, "tok")
	if !strings.HasPrefix(o.binary, "/") || o.timeout != defaultTimeout || o.cwd != os.TempDir() || o.token != "tok" {
		t.Errorf("defaults = %+v", o)
	}
	o = readOpts(map[string]any{"binary": "~/bin/freebuff", "cwd": "/work", "timeoutSeconds": float64(30), "configDir": "~/.fb"}, "t")
	home, _ := os.UserHomeDir()
	if o.binary != filepath.Join(home, "bin/freebuff") || o.cwd != "/work" || o.timeout != 30*time.Second || o.configDir != filepath.Join(home, ".fb") {
		t.Errorf("overrides = %+v", o)
	}
	if got := clientFor(o); got != clientFor(o) { // one process per (binary, config, cwd)
		t.Error("clientFor did not reuse the client")
	}
}

func writeChat(t *testing.T, dir string, msgs []any) {
	t.Helper()
	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat-messages.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func ai(done bool, blocks ...map[string]any) map[string]any {
	out := make([]any, len(blocks))
	for i, b := range blocks {
		out[i] = b
	}
	return map[string]any{"variant": "ai", "isComplete": done, "blocks": out}
}

func text(textType, content string) map[string]any {
	return map[string]any{"type": "text", "textType": textType, "content": content}
}

func makeChat(t *testing.T, root, project, id string, mod time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "projects", project, "chats", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, mod, mod); err != nil {
		t.Fatal(err)
	}
	return dir
}
