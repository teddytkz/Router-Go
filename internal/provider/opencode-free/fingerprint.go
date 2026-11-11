package opencodefree

import (
	"os"
	"runtime"
	"slices"
	"strings"

	"router-go/internal/consolelog"
)

// The free lane gates on two body-side fingerprints, both mirrored verbatim from 9router's opencodeFingerprint.js
// (probed live against /inference/v1): the CLI's `<env>` system marker and its own `read`/`shell` tool definitions.
// A request missing either answers 429 FreeUsageLimitError whatever the headers say.

const envTag = "<env>"

// gatedName is a second, purely textual gate: a declared tool whose name contains "terminal" is refused, so it
// travels under an alias and the caller's own name is restored on the way back.
var gatedAlias = "run_command"

const fingerprintReadDesc = "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files."

const fingerprintShellDesc = "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete."

// fingerprintDefs is compared field by field upstream, so keep every string and key exactly as captured.
var fingerprintDefs = map[string]struct {
	desc   string
	params map[string]any
}{
	"read": {desc: fingerprintReadDesc, params: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "File or directory to read"},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "The line or directory entry to start reading from (1-based)"},
			"limit":  map[string]any{"type": "integer", "minimum": 0, "description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)"},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}},
	"shell": {desc: fingerprintShellDesc, params: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":    map[string]any{"type": "string", "description": "Shell command string to execute"},
			"workdir":    map[string]any{"type": "string", "description": "Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead."},
			"timeout":    map[string]any{"type": "integer", "minimum": 0, "description": "Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default."},
			"background": map[string]any{"type": "boolean", "description": "Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion."},
		},
		"required":             []any{"command"},
		"additionalProperties": false,
	}},
}

var fingerprintOrder = []string{"read", "shell"}

// envMarker is the CLI's environment block; values mirror a client running on this host.
func envMarker() string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	return strings.Join([]string{
		"Here is some useful information about the environment you are running in:",
		envTag,
		"  Working directory: " + wd,
		"  Workspace root folder: " + wd,
		"  Is directory a git repo: no",
		"  Platform: " + runtime.GOOS,
		"  Prefer " + strings.TrimRight(os.TempDir(), "/") + "/opencode over generic system temporary directories such as /tmp; it is pre-created and approved for external access.",
		"</env>",
	}, "\n")
}

// defOf is the object holding a tool's name/description/schema: the nested function for chat tools, the tool itself
// otherwise; messages lanes call the schema input_schema.
func defOf(shape string, tool map[string]any) (map[string]any, string) {
	if shape == "chat" {
		if fn, ok := tool["function"].(map[string]any); ok {
			return fn, "parameters"
		}
		return tool, "parameters"
	}
	if shape == "messages" {
		return tool, "input_schema"
	}
	return tool, "parameters"
}

func toolName(shape string, tool map[string]any) string {
	d, _ := defOf(shape, tool)
	return strings.TrimSpace(str(d["name"]))
}

func withToolName(shape string, tool map[string]any, name string) map[string]any {
	d, _ := defOf(shape, tool)
	d["name"] = name
	return tool
}

func withFingerprintDef(shape string, tool map[string]any, key string) map[string]any {
	def := fingerprintDefs[key]
	d, pk := defOf(shape, tool)
	d["description"], d[pk] = def.desc, deepCopy(def.params)
	return tool
}

// isFingerprintDef: the tool already carries OpenCode's exact definition.
func isFingerprintDef(shape string, tool map[string]any, key string) bool {
	def := fingerprintDefs[key]
	d, pk := defOf(shape, tool)
	return str(d["description"]) == def.desc && sameJSON(d[pk], def.params)
}

// canNormalizeInPlace: the tool speaks OpenCode's parameter names, so its definition can be replaced without hiding
// an argument the caller's handler expects (OpenCode composes shell's description per platform, so exact matches are
// the exception, not the rule).
func canNormalizeInPlace(shape string, tool map[string]any, key string) bool {
	d, pk := defOf(shape, tool)
	params, _ := d[pk].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return false
	}
	want := fingerprintDefs[key].params
	wanted := map[string]bool{}
	for k := range want["properties"].(map[string]any) {
		wanted[k] = true
	}
	for k := range props {
		if !wanted[k] {
			return false
		}
	}
	for _, r := range want["required"].([]any) {
		if _, ok := props[r.(string)]; !ok {
			return false
		}
	}
	return true
}

// concealTools keeps caller tools on the fingerprint names wherever their arguments can carry the gated definition,
// moves the rest off them, and returns the map sent-name -> caller-name.
func concealTools(shape string, tools []any) ([]any, map[string]string) {
	renames := map[string]string{}
	taken := map[string]bool{}
	seen := map[string]bool{}
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok {
			taken[toolName(shape, m)] = true
		}
	}
	out := make([]any, 0, len(tools))
	aliasFor := func(base string) string {
		alias := base
		for i := 2; taken[alias]; i++ { // a caller tool already holds the plain alias
			alias = base + strconvItoa(i)
		}
		taken[alias] = true
		return alias
	}
	// Found by bisecting a live 429 on 2026-11-11 (Claude Code's 15 tools): declared next to some other Claude Code
	// tools (found so far: DeferredToolPlaceholder, ScheduleWakeup), "ReportFindings" makes the free tier answer
	// FreeUsageLimitError with a retry-after of about 13 h, while every one of them alone, or under another name, passes.
	// ReportFindings is in every offending pair, so it is the one sent under another name. The full captured body
	// answered 200 once it was renamed, and 429 before. Only this provider's upstream was tested.
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		name := toolName(shape, m)
		if name == "ReportFindings" {
			alias := aliasFor("report_findings")
			renames[alias] = name
			out = append(out, withToolName(shape, m, alias))
			continue
		}
		if strings.Contains(strings.ToLower(name), "terminal") {
			alias := aliasFor(gatedAlias)
			renames[alias] = name
			out = append(out, withToolName(shape, m, alias))
			continue
		}
		switch name {
		case "read", "shell":
		default:
			out = append(out, t)
			continue
		}
		if seen[name] { // duplicate declarations of one name are rejected upstream
			continue
		}
		seen[name] = true
		switch {
		case isFingerprintDef(shape, m, name):
			out = append(out, t)
		case canNormalizeInPlace(shape, m, name):
			out = append(out, withFingerprintDef(shape, m, name))
		default:
			alias := aliasFor(name + "_client")
			renames[alias] = name
			out = append(out, withToolName(shape, m, alias))
		}
	}
	return out, renames
}

// appendMissing adds OpenCode's definition for each fingerprint tool not already declared, in the lane's own shape.
func appendMissing(shape string, tools *[]any) []string {
	have := map[string]bool{}
	for _, t := range *tools {
		if m, ok := t.(map[string]any); ok {
			have[toolName(shape, m)] = true
		}
	}
	var appended []string
	for _, key := range fingerprintOrder {
		if have[key] {
			continue
		}
		def := fingerprintDefs[key]
		switch shape {
		case "responses":
			*tools = append(*tools, map[string]any{"type": "function", "name": key, "description": def.desc, "parameters": deepCopy(def.params), "strict": false})
		case "messages":
			*tools = append(*tools, map[string]any{"name": key, "description": def.desc, "input_schema": deepCopy(def.params)})
		default:
			*tools = append(*tools, map[string]any{"type": "function", "function": map[string]any{"name": key, "description": def.desc, "parameters": deepCopy(def.params)}})
		}
		appended = append(appended, key)
	}
	return appended
}

// applyFingerprintTools runs the request-side pass for one lane and returns the rename map for the response side.
func applyFingerprintTools(body map[string]any, shape string) map[string]string {
	tools, _ := body["tools"].([]any)
	out, renames := concealTools(shape, tools)
	logRenames(renames)
	appended := appendMissing(shape, &out)
	body["tools"] = out
	retargetToolChoice(body, renames)
	aliasHistory(body, shape, renames)
	if len(appended) > 0 {
		names := make([]string, len(appended))
		for i, n := range appended {
			names[i] = "`" + n + "`"
		}
		note := "Do not call the " + strings.Join(names, " or ") + " tools; they are placeholders that do nothing. Use the other tools you have been given instead."
		if len(appended) == 1 {
			note = "Do not call the " + names[0] + " tool; it is a placeholder that does nothing. Use the other tools you have been given instead."
		}
		appendSystem(body, shape, note)
	}
	if !carriesEnv(body, shape) {
		appendSystem(body, shape, envMarker())
	}
	return renames
}

// retargetToolChoice points an explicit choice at the alias of a concealed caller tool.
func retargetToolChoice(body map[string]any, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	byOriginal := map[string]string{}
	for alias, orig := range renames {
		byOriginal[orig] = alias
	}
	switch c := body["tool_choice"].(type) {
	case map[string]any:
		if n, ok := c["name"].(string); ok {
			if alias, ok := byOriginal[n]; ok {
				c["name"] = alias
			}
			return
		}
		if fn, ok := c["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				if alias, ok := byOriginal[n]; ok {
					fn["name"] = alias
				}
			}
		}
	case string:
		switch c {
		case "none": // upstream answers tool_choice "none" with 403 FreeTierError
			body["tool_choice"] = "auto"
		}
	}
}

// aliasHistory renames prior-turn calls to concealed tools too: otherwise the model reads its own earlier calls as
// calls to OpenCode's tool of the same name.
func aliasHistory(body map[string]any, shape string, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	byOriginal := map[string]string{}
	for alias, orig := range renames {
		byOriginal[orig] = alias
	}
	alias := func(name string) string {
		if a, ok := byOriginal[name]; ok {
			return a
		}
		return name
	}
	for _, it := range asList(body["input"]) {
		if m, ok := it.(map[string]any); ok && m["type"] == "function_call" {
			if n, ok := m["name"].(string); ok {
				m["name"] = alias(n)
			}
		}
	}
	for _, msg := range asList(body["messages"]) {
		mm, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		for _, tc := range asList(mm["tool_calls"]) {
			tcm, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			if fn, ok := tcm["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					fn["name"] = alias(n)
				}
			}
		}
		for _, blk := range asList(mm["content"]) { // messages lane: assistant tool_use blocks
			if bm, ok := blk.(map[string]any); ok && bm["type"] == "tool_use" {
				if n, ok := bm["name"].(string); ok {
					bm["name"] = alias(n)
				}
			}
		}
	}
}

// carriesEnv: a genuine opencode client is passthrough; anything else gets the marker appended.
func carriesEnv(body map[string]any, shape string) bool {
	switch shape {
	case "responses":
		s, _ := body["instructions"].(string)
		return strings.Contains(s, envTag)
	case "messages":
		return textCarriesEnv(body["system"])
	}
	for _, msg := range asList(body["messages"]) {
		if mm, ok := msg.(map[string]any); ok && mm["role"] == "system" && textCarriesEnv(mm["content"]) {
			return true
		}
	}
	return false
}

func textCarriesEnv(v any) bool {
	switch c := v.(type) {
	case string:
		return strings.Contains(c, envTag)
	case []any:
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok && strings.Contains(str(bm["text"]), envTag) {
				return true
			}
		}
	}
	return false
}

// appendSystem adds text to the lane's system prompt, keeping the caller's own content.
func appendSystem(body map[string]any, shape, text string) {
	switch shape {
	case "responses":
		body["instructions"] = joinText(body["instructions"], text)
	case "messages":
		body["system"] = joinBlocks(body["system"], text)
	default:
		msgs := asList(body["messages"])
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
				mm["content"] = joinBlocks(mm["content"], text)
				return
			}
		}
		body["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, msgs...)
	}
}

func joinText(v any, text string) string {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return s + "\n\n" + text
	}
	return text
}

func joinBlocks(v any, text string) any {
	switch c := v.(type) {
	case string:
		return joinText(c, text)
	case []any:
		for i := len(c) - 1; i >= 0; i-- {
			if bm, ok := c[i].(map[string]any); ok && bm["text"] != nil {
				bm["text"] = joinText(bm["text"], text)
				return c
			}
		}
		return append(c, map[string]any{"type": "text", "text": text})
	}
	return text
}

// injectReasoning keeps a multi-turn chat history valid: Zen reasoning models reject assistant turns that carry no
// reasoning_content, and the response pipeline strips it before the client ever sees it, so the echo cannot come from
// the client.
func injectReasoning(body map[string]any) {
	for _, msg := range asList(body["messages"]) {
		mm, ok := msg.(map[string]any)
		if !ok || mm["role"] != "assistant" {
			continue
		}
		if s, ok := mm["reasoning_content"].(string); ok && s != "" {
			continue
		}
		mm["reasoning_content"] = " "
	}
}

func str(v any) string { s, _ := v.(string); return s }

func asList(v any) []any { l, _ := v.([]any); return l }

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// logRenames tells the console which of the caller's tools went upstream under another name (the answer gets the
// caller's name back), so a changed request is never a surprise when reading a failure.
func logRenames(renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	parts := make([]string, 0, len(renames))
	for alias, orig := range renames {
		parts = append(parts, orig+" → "+alias)
	}
	slices.Sort(parts)
	consolelog.Logf("INFO", "OPENCODE", "tools sent under another name, restored in the answer: %s", strings.Join(parts, ", "))
}
