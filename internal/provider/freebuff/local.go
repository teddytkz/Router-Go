package freebuff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

// The freebuff CLI is a terminal UI: it takes no prompt argument and has no stdin mode (cli/src/cli-args.ts), and
// the backend refuses its own protocol from anything but the official binary. So this port does not speak the
// protocol at all — it drives the vendor's program through tmux and reads the answer from the CLI's own on-disk
// chat log (structured JSON), never by parsing the screen. Nothing is impersonated: the real binary talks to the
// vendor with its own key, and tool calls stay inside the CLI.
const (
	// idleMarker is the input placeholder the TUI shows only while it is not working; its presence means the box is
	// free, which is how this port knows when to type the next prompt.
	idleMarker      = "Enter a coding task or / for commands"
	defaultBinary   = "~/.config/manicode/freebuff"
	defaultTimeout  = 240 * time.Second
	sessionPrefix   = "router-go-fb-"
	tuiWidth        = 210
	tuiHeight       = 50
	readyTimeout    = 90 * time.Second
	answerPollEvery = 400 * time.Millisecond
	// pasteSettle is how long the text sits in the prompt box before Return is sent; the TUI folds a fast burst of
	// keys into one paste, and a Return inside a paste does not submit.
	pasteSettle = 300 * time.Millisecond
	// admissionFooter marks the CLI's admission box: the turn did not start, and the box says why
	// (freebuff-chat-controls.tsx). Nothing reaches the chat log while it is up.
	admissionFooter = "Esc: back to draft"
	// takeoverGrace is how long after taking a session over the box may still be on screen before it counts as a
	// second refusal (the other client took the session back).
	takeoverGrace = 6 * time.Second
)

// opts are the per-connection knobs, read from providerSpecificData so an operator can point the driver at another
// install or profile without a rebuild.
type opts struct {
	binary    string
	configDir string
	cwd       string
	timeout   time.Duration
	token     string
}

func readOpts(data map[string]any, token string) opts {
	o := opts{binary: defaultBinary, timeout: defaultTimeout, token: token}
	if v, _ := data["binary"].(string); strings.TrimSpace(v) != "" {
		o.binary = strings.TrimSpace(v)
	}
	if v, _ := data["configDir"].(string); strings.TrimSpace(v) != "" {
		o.configDir = strings.TrimSpace(v)
	}
	if v, _ := data["cwd"].(string); strings.TrimSpace(v) != "" {
		o.cwd = strings.TrimSpace(v)
	}
	if n := provider.Num(data["timeoutSeconds"]); n > 0 {
		o.timeout = time.Duration(n) * time.Second
	}
	o.binary = expandHome(o.binary)
	o.configDir = expandHome(o.configDir)
	if o.cwd == "" {
		o.cwd = os.TempDir()
	}
	return o
}

func expandHome(p string) string {
	if p == "~" {
		p = "~/"
	}
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/"))
}

// client is one long-lived CLI process behind a tmux session. The CLI holds a paid session for a fixed window, so
// the process is reused across requests; a process per request would spend one window per call.
type client struct {
	mu       sync.Mutex
	opts     opts
	tmux     string
	dir      string // config dir holding projects/<name>/chats
	launched string // model the running process was started on
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*client{}
)

func clientFor(o opts) *client {
	key := o.binary + "\x00" + o.configDir + "\x00" + o.cwd
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[key]; ok {
		return c
	}
	c := &client{opts: o}
	clients[key] = c
	return c
}

// Local answers a chat request by driving the CLI. It never touches the network itself.
func local(ctx context.Context, call provider.NativeCall) (*http.Response, error) {
	prompt, err := promptText(call.Body)
	if err != nil {
		return nil, err
	}
	o := readOpts(call.Data, call.Token)
	if o.configDir == "" {
		o.configDir = defaultConfigDir(o.binary)
	}
	c := clientFor(o)
	if !call.Stream {
		answer, err := c.ask(ctx, call.Model, prompt)
		if err != nil {
			return nil, err
		}
		return chatResponse(call.Model, answer), nil
	}
	// A streaming caller gets the answer as the CLI writes it, but the first event still gates the response: an error
	// before any text (no hours, a broken install) must leave this function as an error so the gateway can fall back,
	// not as a 200 whose body is a complaint.
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan chunk, 64)
	go func() {
		defer close(ch)
		_, err := c.askStream(ctx, call.Model, prompt, func(delta string) { ch <- chunk{delta: delta} })
		if err != nil {
			if ctx.Err() == nil { // a client that went away is not the provider's failure
				ch <- chunk{err: err}
			}
		}
	}()
	first, ok := <-ch
	if !ok {
		return streamResponse(call.Model, "", nil, cancel), nil
	}
	if first.err != nil {
		cancel()
		return nil, first.err
	}
	return streamResponse(call.Model, first.delta, ch, cancel), nil
}

// defaultConfigDir is the profile the CLI keeps its login and chats in: the one beside the binary when it was
// installed with its own config dir, otherwise the CLI's own default.
func defaultConfigDir(binary string) string {
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "credentials.json")); err == nil {
		return filepath.Dir(binary)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Dir(binary)
	}
	return filepath.Join(home, ".config", "manicode")
}

// ask runs one prompt through the CLI. A failure tears the process down instead of retrying, because a retry would
// spend a second session window on a broken install.
func (c *client) ask(ctx context.Context, model, prompt string) (string, error) {
	return c.drive(ctx, model, prompt, nil)
}

// askStream is ask with the answer reported while it is written: emit receives each new piece of text as the CLI
// appends it to its log, so a streaming caller sees the turn's first words long before the turn is complete.
func (c *client) askStream(ctx context.Context, model, prompt string, emit func(string)) (string, error) {
	return c.drive(ctx, model, prompt, emit)
}

func (c *client) drive(ctx context.Context, model, prompt string, emit func(string)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	deadline := time.Now().Add(c.opts.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	// The model is a launch setting, not a live one: /model opens a picker with no argument (command-registry.ts),
	// so a session is only ever started on c.opts.model. A different model means a different process.
	if model != c.launched {
		c.kill()
	}
	if err := c.ensureUp(ctx, model, deadline); err != nil {
		c.reset()
		return "", err
	}
	base := c.newestChat()
	baseCount := 0
	if base != "" {
		baseCount = messageCount(base)
	}
	if err := c.typePrompt(ctx, prompt); err != nil {
		c.reset()
		return "", err
	}
	answer, err := c.awaitAnswer(ctx, base, baseCount, deadline, emit)
	if err != nil {
		c.reset()
		return "", err
	}
	return answer, nil
}

// awaitAnswer waits for the CLI to finish the turn this prompt started. The CLI appends to the one chat directory it
// holds open (currentChatId only rotates on /new, project-files.ts), so progress is told apart from the previous
// answer by the log growing — not by a new directory appearing. A prompt that never reached the CLI ends at the
// deadline with the pane tail in the error, which is the diagnostic for that case.
func (c *client) awaitAnswer(ctx context.Context, base string, baseCount int, deadline time.Time, emit func(string)) (string, error) {
	var tookOver time.Time
	sent := 0 // characters of this turn's answer already handed to emit
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", errors.New("freebuff did not answer within " + c.opts.timeout.String() + ": " + c.tail())
		}
		// The CLI holds a sent message in an admission box when it will not start the model — no hours for it, or a
		// session to take over (freebuff-chat-controls.tsx). Nothing reaches the chat log while that box is up, so
		// waiting the deadline out would blame a slow provider for what is really a refusal, and hide the reason.
		pane := c.pane()
		// A free account holds one session across CLI and Desktop; the box offers to take it over. That costs no hours
		// and the operator asked for this router to own the session, so it is confirmed — once per turn, so a client
		// that keeps stealing it back ends in an error instead of a tug of war.
		if tookOver.IsZero() && takeoverOffered(pane) {
			if err := c.sendKeys(ctx, "\r"); err != nil {
				return "", err
			}
			tookOver = time.Now()
		} else if tookOver.IsZero() || time.Since(tookOver) > takeoverGrace {
			if notice := admissionNotice(pane); notice != "" {
				return "", errors.New(notice)
			}
		}
		if dir := c.newestChat(); dir != "" && (dir != base || messageCount(dir) > baseCount) {
			if text, done, ok := readText(dir); ok {
				// The message is written long before it is complete, so hand out what has landed and keep the rest for
				// the next poll: the client sees the answer grow instead of waiting the whole turn out.
				if emit != nil && len(text) > sent {
					emit(text[sent:])
					sent = len(text)
				}
				if done {
					return text, nil
				}
			}
		}
		time.Sleep(answerPollEvery)
	}
}

// admissionNotice reads a refusal out of the CLI's admission box, or "" when there is nothing to report. The box also
// stands in as a progress line while a session is simply starting ("Starting your model session…", with no Enter
// hint), so only a box offering an Enter action counts: that is the CLI stopped on a human decision — not enough
// hours, a session to take over, or a charge to confirm — and a driver that cannot answer it would otherwise sit
// until the deadline and blame a slow provider. The verdict is deliberately not auto-answered (except a session takeover, see
// takeoverOffered), because confirming the others spends the operator's hours. The box draws its message above the key hints, so the lines back from the hint carry
// the whole reason even after it wraps.
func admissionNotice(pane string) string {
	lines := strings.Split(pane, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, admissionFooter) || !strings.Contains(ln, "Enter:") {
			continue
		}
		var parts []string
		for _, l := range lines[max(i-2, 0) : i+1] {
			if l = strings.Trim(strings.TrimSpace(l), "│|"); l != "" {
				parts = append(parts, l)
			}
		}
		return "freebuff held the request: " + strings.Join(parts, " ")
	}
	return ""
}

// takeoverOffered reports whether the admission box is asking to take over the account's single session
// ("Enter: take over"). Unlike the hours or confirm-and-send boxes, answering it spends nothing.
func takeoverOffered(pane string) bool {
	for _, ln := range strings.Split(pane, "\n") {
		if strings.Contains(ln, admissionFooter) && strings.Contains(strings.ToLower(ln), "enter: take over") {
			return true
		}
	}
	return false
}

// newestChat is the chat directory the CLI most recently wrote for this project. The CLI keys projects by the base
// name of the directory it was started in (getProjectDataDir, project-files.ts), and this driver starts it in one known
// directory, so only that project is ever scanned — a chat another CLI wrote in another project is not a candidate.
func (c *client) newestChat() string {
	dirs, _ := filepath.Glob(filepath.Join(c.dir, "projects", filepath.Base(c.opts.cwd), "chats", "*"))
	newest, newestMod := "", time.Time{}
	for _, d := range dirs {
		st, err := os.Stat(d)
		if err != nil {
			continue
		}
		if st.ModTime().After(newestMod) {
			newest, newestMod = d, st.ModTime()
		}
	}
	return newest
}

// messageCount is how many turns the log holds; a short read (a mid-write file) counts as what it parsed.
func messageCount(dir string) int {
	var msgs []json.RawMessage
	raw, err := os.ReadFile(filepath.Join(dir, "chat-messages.json"))
	if err != nil || json.Unmarshal(raw, &msgs) != nil {
		return 0
	}
	return len(msgs)
}

// readAnswer reads the newest turn's answer, which is the log's last entry once it is finished. Only the last entry is
// considered, so the previous turn's completed answer cannot be mistaken for this one. Reasoning blocks are skipped:
// the CLI marks them as text blocks with textType "reasoning", and a caller wants the answer, not the chain of thought.
func readAnswer(dir string) (string, bool) {
	text, done, ok := readText(dir)
	return text, ok && done
}

// readText is the answer so far. The CLI writes the turn's blocks while the model is still producing them, so the text
// grows between polls: done says the log marked the turn complete, not that this text is final. ok is false while the
// last entry is not an assistant turn (nothing to report yet).
func readText(dir string) (text string, done bool, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "chat-messages.json"))
	if err != nil {
		return "", false, false
	}
	var msgs []struct {
		Variant    string `json:"variant"`
		IsComplete bool   `json:"isComplete"`
		Blocks     []struct {
			Type     string `json:"type"`
			TextType string `json:"textType"`
			Content  string `json:"content"`
		} `json:"blocks"`
	}
	if json.Unmarshal(raw, &msgs) != nil || len(msgs) == 0 {
		return "", false, false
	}
	m := msgs[len(msgs)-1]
	if m.Variant != "ai" {
		return "", false, false
	}
	var b strings.Builder
	for _, blk := range m.Blocks {
		if blk.Type == "text" && blk.TextType != "reasoning" {
			b.WriteString(blk.Content)
		}
	}
	return b.String(), m.IsComplete, true
}

// ---- process ----

func (c *client) ensureUp(ctx context.Context, model string, deadline time.Time) error {
	if c.tmux != "" && c.paneIdle() {
		return nil
	}
	c.kill()
	if _, err := exec.LookPath("tmux"); err != nil {
		return errors.New("freebuff needs tmux on this machine: install tmux and retry")
	}
	c.dir = c.opts.configDir
	if err := c.saveModel(model); err != nil {
		return err
	}
	c.tmux = sessionPrefix + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
	cmd := exec.Command("tmux", "new-session", "-d", "-s", c.tmux, "-x", strconv.Itoa(tuiWidth), "-y", strconv.Itoa(tuiHeight))
	cmd.Dir = c.opts.cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		c.tmux = ""
		return fmt.Errorf("freebuff: cannot start tmux: %v: %s", err, strings.TrimSpace(string(out)))
	}
	line := shellQuote(c.opts.binary) + " --cwd " + shellQuote(c.opts.cwd)
	if c.opts.configDir != "" {
		line = "FREEBUFF_CONFIG_DIR=" + shellQuote(c.opts.configDir) + " " + line
	}
	if c.opts.token != "" {
		line = "CODEBUFF_API_KEY=" + shellQuote(c.opts.token) + " " + line
	}
	if err := c.sendKeys(ctx, line+"\r"); err != nil {
		c.kill()
		return err
	}
	if err := c.awaitReady(ctx, deadline); err != nil {
		c.kill()
		return err
	}
	c.launched = model
	return nil
}

// awaitReady waits for the input box, answering the one prompt that can stand in front of it (the project trust
// question) and failing loudly on anything else, because a question this driver cannot answer would look exactly
// like an empty answer.
func (c *client) awaitReady(ctx context.Context, deadline time.Time) error {
	trust := time.Now().Add(readyTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.paneIdle() {
			return nil
		}
		if time.Now().After(trust) || time.Now().After(deadline) {
			return errors.New("freebuff's terminal never reached the prompt box — check the CLI by hand: " + c.tail())
		}
		if strings.Contains(c.pane(), "trust") {
			_ = c.sendKeys(ctx, "\t")
			_ = c.sendKeys(ctx, "\r")
			trust = time.Now().Add(readyTimeout)
		}
		time.Sleep(answerPollEvery * 2)
	}
}

// catalogKeyRe is the shape the CLI accepts for a catalogue key (FREEBUFF_CATALOG_KEY_PATTERN in utils/settings.ts),
// which is validated by shape alone: the key is an opaque handle and the row behind it is the server's to publish.
var catalogKeyRe = regexp.MustCompile(`^m-[0-9a-f]{10}$`)

func isCatalogKey(model string) bool { return catalogKeyRe.MatchString(model) }

// saveModel writes the model into the CLI's own settings before launch. The CLI has no command to switch models in a
// running session (/model takes no argument and opens a picker, command-registry.ts), so the pick is made the way the
// CLI makes it for itself: saveFreebuffModelPreference in its settings.json (utils/settings.ts). Every other key in
// that file is preserved — it holds the operator's login and preferences, not just this one field.
func (c *client) saveModel(model string) error {
	if model == "" {
		return nil
	}
	path := filepath.Join(c.dir, "settings.json")
	settings := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(raw, &settings) != nil {
			return errors.New("freebuff: " + path + " is not valid JSON, so its model cannot be set without discarding it")
		}
	}
	if isCatalogKey(model) {
		// A catalogue row has no wire id of its own (freebuff-model-presets.ts): its opaque key is the only way to
		// name it, and the CLI reads it back from this field. Writing freebuffModel alongside would leave the key in
		// charge anyway, so the two are kept disjoint the way the CLI keeps them (saveFreebuffModelKeyPreference
		// leaves freebuffModel alone; saveFreebuffModelPreference clears the key).
		if raw, err := json.Marshal(model); err == nil {
			settings["freebuffModelKey"] = raw
		}
	} else {
		if raw, err := json.Marshal(model); err == nil {
			settings["freebuffModel"] = raw
		}
		// A named pick is the newest pick, and the CLI drops the key when it adopts one (utils/settings.ts) — leaving
		// it would let the older catalogue row outrank the model just asked for (applyFreebuffCatalog).
		delete(settings, "freebuffModelKey")
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// typable makes prompt text safe to type into a terminal: send-keys -l passes every byte through as a key, so a lone
// CR would submit the message early, ESC would start a key sequence (tool output often carries ANSI colour codes),
// and a Tab would trigger completion. Line breaks are normalised to \n, tabs become spaces, and every other
// control character is dropped.
func typable(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || (r >= 0x7f && r < 0xa0):
			return -1
		}
		return r
	}, strings.ReplaceAll(s, "\t", "    "))
}

// typePrompt puts the prompt in the box and submits it. Newlines go in as Alt+Enter so a multi-line prompt stays one
// message; the text and the Return are sent separately so tmux does not swallow the last character.
func (c *client) typePrompt(ctx context.Context, prompt string) error {
	lines := strings.Split(typable(prompt), "\n")
	for i, ln := range lines {
		if err := c.sendKeys(ctx, ln); err != nil {
			return err
		}
		if i < len(lines)-1 {
			if err := c.sendKeys(ctx, "\x1b\r"); err != nil { // Alt+Enter
				return err
			}
		}
	}
	// The submit needs a moment: sent in the same burst as the text, the TUI consumes Return as the last key of a
	// paste and leaves the prompt sitting in the box, which looks exactly like a request that was never typed.
	time.Sleep(pasteSettle)
	return c.sendKeys(ctx, "\r")
}

func (c *client) paneIdle() bool { return strings.Contains(c.pane(), idleMarker) }

func (c *client) pane() string {
	out, err := exec.Command("tmux", "capture-pane", "-t", c.tmux, "-p").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func (c *client) tail() string {
	p := c.pane()
	if len(p) > 400 {
		p = p[len(p)-400:]
	}
	return strings.TrimSpace(p)
}

func (c *client) sendKeys(ctx context.Context, keys string) error {
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", c.tmux, "-l", "--", keys) // "--": a prompt line starting with "-" is text, not a flag
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("freebuff: tmux send-keys failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// reset drops the process and the cached conversation, so the next request starts clean instead of collecting a
// broken session.
func (c *client) reset() {
	c.kill()
	c.launched = ""
}

func (c *client) kill() {
	if c.tmux == "" {
		return
	}
	_ = exec.Command("tmux", "kill-session", "-t", c.tmux).Run()
	c.tmux = ""
}

// Close tears down every CLI process this port started. Called when the server shuts down.
func Close() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for _, c := range clients {
		c.mu.Lock()
		c.kill()
		c.mu.Unlock()
	}
	clients = map[string]*client{}
}

// ---- request/answer plumbing ----

// promptText flattens a chat request into the single prompt the CLI takes. Tool definitions are dropped on purpose:
// the CLI executes tools itself on the real filesystem, so forwarding them would let it run commands the caller
// never asked for. One user message — the ordinary shape of a chat request — goes in verbatim, with no role label:
// the CLI's own prompt box has no notion of roles, so the label would only be noise in front of the model.
func promptText(body map[string]any) (string, error) {
	msgs, _ := body["messages"].([]any)
	var parts []string
	var roles []string
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "system" {
			continue
		}
		if text := messageText(m["content"]); text != "" {
			parts = append(parts, text)
			roles = append(roles, role)
		}
	}
	if len(parts) == 1 && roles[0] == "user" {
		return parts[0], nil
	}
	var b strings.Builder
	for i, text := range parts {
		switch roles[i] {
		case "assistant":
			b.WriteString("Assistant: ")
		case "tool":
			b.WriteString("Tool result: ")
		default:
			b.WriteString("User: ")
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", errors.New("freebuff: the request has no text the CLI can take as a prompt")
	}
	return out, nil
}

func messageText(content any) string {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, raw := range v {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := p["text"].(string); ok && strings.TrimSpace(t) != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// chunk is one event of a streamed turn: a piece of answer text, or the failure that ended the turn.
type chunk struct {
	delta string
	err   error
}

// sseFrame renders one chat.completion.chunk event. No usage block: the driver has no tokenizer, and a made-up count
// would be a lie.
func sseFrame(id string, created int64, model string, delta map[string]any, finish any) []byte {
	c := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	raw, _ := json.Marshal(c)
	return []byte("data: " + string(raw) + "\n\n")
}

// streamResponse relays a turn as it is written: the role frame, one content frame per delta the driver hands out, then
// the finish frame. first is the delta the caller already took off the channel (it gates the status); a nil ch streams
// the empty answer of a turn that produced no text (tool-only, for instance). cancel stops the driver when the last
// frame has left (or the client went away), so its poll loop does not keep reading a log nobody is waiting for.
func streamResponse(model, first string, ch <-chan chunk, cancel context.CancelFunc) *http.Response {
	pr, pw := io.Pipe()
	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	go func() {
		defer cancel()
		defer pw.Close()
		write := func(b []byte) bool {
			if _, err := pw.Write(b); err != nil { // the client went away: drain, the driver stops on the cancelled ctx
				return false
			}
			return true
		}
		write(sseFrame(id, created, model, map[string]any{"role": "assistant"}, nil))
		failed := false
		if first != "" {
			write(sseFrame(id, created, model, map[string]any{"content": first}, nil))
		}
		for c := range ch {
			if c.err != nil {
				failed = true
				raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": c.err.Error(), "type": "provider_error"}})
				write([]byte("data: " + string(raw) + "\n\n"))
				continue
			}
			if !write(sseFrame(id, created, model, map[string]any{"content": c.delta}, nil)) {
				for range ch { // drain so the driver's emit never blocks on a gone client
				}
				return
			}
		}
		if !failed {
			write(sseFrame(id, created, model, map[string]any{}, "stop"))
		}
		write([]byte("data: [DONE]\n\n"))
	}()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: pr}
}

// chatResponse builds the chat-completions reply the gateway expects for a non-streaming caller: one JSON object.
func chatResponse(model, text string) *http.Response {
	created := time.Now().Unix()
	body := map[string]any{
		"id": "chatcmpl-" + uuid.NewString(), "object": "chat.completion", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": text}}},
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
