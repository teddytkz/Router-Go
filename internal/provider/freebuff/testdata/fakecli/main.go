// Command fakecli stands in for the freebuff binary in the driver's own test. It is a plain terminal program: it
// asks the project-trust question, submits to the exact placeholder the driver watches for while idle, and answers
// every prompt it is given by appending to a chat log shaped like the real CLI's.
//
// It exists because the handshake (tmux, the trust prompt, the idle marker, the growing chat log) is the part of
// this provider that unit tests cannot reach, and driving the real binary there would need a signed-in account.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const idleMarker = "Enter a coding task or / for commands"

func main() {
	cwd := flag.String("cwd", ".", "directory to treat as the project")
	flag.Parse()
	root := os.Getenv("FREEBUFF_CONFIG_DIR")
	if root == "" {
		fmt.Println("fakecli: FREEBUFF_CONFIG_DIR is not set")
		os.Exit(1)
	}
	chat := filepath.Join(root, "projects", filepath.Base(*cwd), "chats", "chat-1")
	if err := os.MkdirAll(chat, 0o755); err != nil {
		fmt.Println("fakecli:", err)
		os.Exit(1)
	}
	log := filepath.Join(chat, "chat-messages.json")
	write(log, []any{})

	fmt.Println("Do you trust this project?")
	in := bufio.NewReader(os.Stdin)
	trusted := false
	for {
		c, err := in.ReadByte()
		if err != nil {
			fmt.Println("fakecli: the trust question was never answered")
			os.Exit(1)
		}
		if c == '\t' {
			trusted = true
		}
		if isEnter(c) {
			break
		}
	}
	if !trusted {
		fmt.Println("fakecli: the trust question was not answered with Tab")
		os.Exit(1)
	}
	fmt.Println(idleMarker)

	var turns []any
	for {
		prompt, err := readPrompt(in)
		if err != nil {
			return
		}
		if prompt == "" {
			continue
		}
		turns = append(turns, turn("user", prompt))
		write(log, turns)
		time.Sleep(200 * time.Millisecond)
		turns = append(turns, map[string]any{"variant": "ai", "isComplete": false, "blocks": []any{}})
		write(log, turns)
		time.Sleep(400 * time.Millisecond)
		turns[len(turns)-1] = map[string]any{"variant": "ai", "isComplete": true, "blocks": []any{
			block("reasoning", "the caller must not see this"),
			block("text", "echo: "+prompt),
		}}
		write(log, turns)
		fmt.Println(idleMarker)
	}
}

func turn(variant, content string) map[string]any {
	return map[string]any{"variant": variant, "isComplete": true, "blocks": []any{block("text", content)}}
}

// readPrompt reads one submitted line. The driver sends Alt+Enter ("\x1b\r") between the lines of a multi-line
// prompt and a bare "\r" to submit; a terminal's canonical mode turns both into "\n", which is why either counts
// as the end of a line here.
func readPrompt(in *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := in.ReadByte()
		if err != nil {
			return "", err
		}
		if c == 0x1b {
			if next, err := in.Peek(1); err == nil && isEnter(next[0]) {
				in.ReadByte()
				b.WriteByte('\n')
			}
			continue
		}
		if isEnter(c) {
			return strings.TrimSpace(b.String()), nil
		}
		b.WriteByte(c)
	}
}

func isEnter(c byte) bool { return c == '\r' || c == '\n' }

func block(textType, content string) map[string]any {
	return map[string]any{"type": "text", "textType": textType, "content": content}
}

func write(path string, turns []any) {
	raw, _ := json.Marshal(turns)
	os.WriteFile(path, raw, 0o600)
}
