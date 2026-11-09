package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

const (
	defaultBase = "https://q.us-east-1.amazonaws.com"
	genPath     = "/generateAssistantResponse"
	// The codewhisperer.* host wants the operation named in a header.
	codewhispererTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	maxTokens           = 32000
	thinkingDefault     = 16000
	thinkingMax         = 32000
	agenticSuffix       = "-agentic"
	thinkingSuffix      = "-thinking"
)

// Public shared CodeWhisperer profiles, used when an account has not resolved its own profile ARN. Accounts bound to
// their own profile (IDC, external IdP, API key) never fall back to them: a foreign ARN is answered with 403.
const (
	builderIDProfileARN = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	socialProfileARN    = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
)

// agenticPrompt is injected for "-agentic" models: the same upstream model with a chunked-write protocol, because
// Kiro cuts requests that run longer than a couple of minutes (text from XRouter-Next).
const agenticPrompt = `# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- **MAXIMUM 350 LINES** per single write/edit operation - NO EXCEPTIONS
- **RECOMMENDED 300 LINES** or less for optimal performance
- **NEVER** write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

### For NEW FILES (>300 lines total):
1. FIRST: Write initial chunk (first 250-300 lines) using write_to_file/fsWrite
2. THEN: Append remaining content in 250-300 line chunks using file append operations
3. REPEAT: Continue appending until complete

### For EDITING EXISTING FILES:
1. Use surgical edits (apply_diff/targeted edits) - change ONLY what's needed
2. NEVER rewrite entire files - use incremental modifications
3. Split large refactors into multiple small, focused edits

### For LARGE CODE GENERATION:
1. Generate in logical sections (imports, types, functions separately)
2. Write each section as a separate operation
3. Use append operations for subsequent sections

## EXAMPLES OF CORRECT BEHAVIOR

CORRECT: Writing a 600-line file
- Operation 1: Write lines 1-300 (initial file creation)
- Operation 2: Append lines 301-600

CORRECT: Editing multiple functions
- Operation 1: Edit function A
- Operation 2: Edit function B
- Operation 3: Edit function C

WRONG: Writing 500 lines in single operation -> TIMEOUT
WRONG: Rewriting entire file to change 5 lines -> TIMEOUT
WRONG: Generating massive code blocks without chunking -> TIMEOUT

## WHY THIS MATTERS
- Server has 2-3 minute timeout for operations
- Large writes exceed timeout and FAIL completely
- Chunked writes are FASTER and more RELIABLE
- Failed writes waste time and require retry

REMEMBER: When in doubt, write LESS per operation. Multiple small operations > one large operation.`

var native = &provider.Native{Endpoints: endpoints, Request: request, Response: response}

// endpoints: the Amazon Q surface first, then CodeWhisperer, then the older kiro.dev gateway (which answers modern
// payloads with 400 on some accounts, so it is the last resort). Only regions known to host the service reach a URL.
// A BaseURL other than the default (tests, a mirror) is used alone.
func endpoints(base string, data map[string]any) []string {
	if base != defaultBase {
		return []string{base + genPath}
	}
	region := "us-east-1"
	if r, _ := data["region"].(string); strings.TrimSpace(r) == "eu-central-1" {
		region = "eu-central-1"
	}
	return []string{
		"https://q." + region + ".amazonaws.com" + genPath,
		"https://codewhisperer.us-east-1.amazonaws.com" + genPath,
		"https://runtime.us-east-1.kiro.dev" + genPath,
	}
}

type namesKey struct{}

// request builds one GenerateAssistantResponse call. The map from Kiro tool names back to the client's own rides on
// the request context, where response() finds it through resp.Request.
func request(ctx context.Context, c provider.NativeCall) (*http.Request, error) {
	payload, names := buildPayload(c.Model, c.Body, c.Data)
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, namesKey{}, names), "POST", c.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/vnd.amazon.eventstream")
	h.Set("Authorization", "Bearer "+c.Token)
	if m, _ := c.Data["authMethod"].(string); m == "external_idp" {
		h.Set("TokenType", "EXTERNAL_IDP") // binds an enterprise IdP token to its profile
	}
	if arn, _ := c.Data["profileArn"].(string); arn != "" {
		h.Set("x-amzn-codewhisperer-profile-arn", arn)
	}
	if strings.HasPrefix(c.URL, "https://codewhisperer.") {
		h.Set("X-Amz-Target", codewhispererTarget)
	}
	return req, nil
}

// buildPayload converts a chat body into the Kiro request and returns the Kiro→client tool name map.
func buildPayload(model string, body, data map[string]any) (map[string]any, map[string]string) {
	upstream, agentic, thinking := resolveModel(model)
	specs, names := toolSpecs(body["tools"])
	history, current := build(body["messages"], names, specs, upstream)

	var prefix []string
	if budget := thinkingBudget(body, thinking); budget > 0 {
		prefix = append(prefix, "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>"+strconv.Itoa(budget)+"</max_thinking_length>")
	}
	if agentic {
		prefix = append(prefix, agenticPrompt)
	}
	first := current
	if len(history) > 0 {
		first = history[0].User
	}
	first.Content = joinText(strings.Join(prefix, "\n\n"), first.Content)
	if strings.HasPrefix(upstream, "deepseek") || strings.HasPrefix(upstream, "qwen") { // no image input
		for i := range history {
			if history[i].User != nil {
				history[i].User.Images = nil
			}
		}
		current.Images = nil
	}

	conv := map[string]any{
		"chatTriggerType": "MANUAL", "conversationId": uuid.NewString(),
		"currentMessage": map[string]any{"userInputMessage": current}, "history": history,
	}
	payload := map[string]any{"conversationState": conv}
	if arn := profileARN(data); arn != "" {
		payload["profileArn"] = arn
	}
	inf := map[string]any{"maxTokens": maxTokens}
	if v, ok := body["temperature"]; ok {
		inf["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		inf["topP"] = v
	}
	payload["inferenceConfig"] = inf

	back := map[string]string{} // Kiro name → client name, only where they differ
	for orig, k := range names {
		if orig != k {
			back[k] = orig
		}
	}
	return payload, back
}

func profileARN(data map[string]any) string {
	if arn, _ := data["profileArn"].(string); arn != "" {
		return arn
	}
	switch m, _ := data["authMethod"].(string); m {
	case "api_key", "idc", "external_idp":
		return "" // the token's own default profile applies
	case "google", "github", "social":
		return socialProfileARN
	}
	return builderIDProfileARN
}

// effort levels → thinking budget; 0 turns thinking off (same table as XRouter-Next).
var effortBudget = map[string]int{"none": 0, "off": 0, "disabled": 0, "minimal": 512, "low": 1024, "medium": 8192, "high": 24576, "xhigh": 32768, "max": 128000}

// thinkingBudget is the reasoning budget to ask for (0 = none). An explicit request (reasoning_effort, reasoning.effort,
// thinking) wins over the "-thinking" model suffix, so a client can turn a thinking model's reasoning off.
func thinkingBudget(body map[string]any, suffix bool) int {
	clamp := func(n int) int { return max(1, min(n, thinkingMax)) }
	effort, _ := body["reasoning_effort"].(string)
	if r, ok := body["reasoning"].(map[string]any); ok && effort == "" {
		effort, _ = r["effort"].(string)
	}
	if effort != "" {
		if b, ok := effortBudget[strings.ToLower(effort)]; ok {
			if b == 0 {
				return 0
			}
			return clamp(b)
		}
		return thinkingDefault
	}
	if t, ok := body["thinking"].(map[string]any); ok {
		switch t["type"] {
		case "disabled":
			return 0
		case "enabled":
			if n, ok := t["budget_tokens"].(float64); ok && n > 0 {
				return clamp(int(n))
			}
			return thinkingDefault
		}
	}
	if suffix {
		return thinkingDefault
	}
	return 0
}

// resolveModel splits "-thinking" / "-agentic" (variants invented by the router: same upstream model, different
// prompt) from the real model id and folds the id onto the spelling Kiro accepts.
func resolveModel(model string) (upstream string, agentic, thinking bool) {
	if m, ok := strings.CutSuffix(model, agenticSuffix); ok {
		agentic, model = true, m
	}
	if m, ok := strings.CutSuffix(model, thinkingSuffix); ok {
		thinking, model = true, m
	}
	return normalizeModel(model), agentic, thinking
}

var snapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// normalizeModel folds Anthropic-style ids ("claude-sonnet-4-5-20250929") onto Kiro's dotted alias ("claude-sonnet-4.5"),
// which is all it accepts (anything else is a 400 INVALID_MODEL_ID). Unknown ids pass through unchanged.
func normalizeModel(model string) string {
	id := snapshotSuffix.ReplaceAllString(strings.ToLower(model), "")
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(id, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("sonnet"):
		switch {
		case has("5.5", "5-5"):
			return "claude-sonnet-5.5"
		case has("4.6", "4-6"):
			return "claude-sonnet-4.6"
		case has("sonnet-5", "sonnet.5"):
			return "claude-sonnet-5"
		case has("sonnet-4", "sonnet.4") && !has("4.5", "4-5"):
			return "claude-sonnet-4"
		}
		return "claude-sonnet-4.5"
	case has("fable"):
		return "claude-fable-5"
	case has("opus"):
		switch {
		case has("5.5", "5-5"):
			return "claude-opus-5.5"
		case has("opus-5", "opus.5", "opus 5"):
			return "claude-opus-5"
		case has("4.8", "4-8"):
			return "claude-opus-4.8"
		case has("4.7", "4-7"):
			return "claude-opus-4.7"
		case has("4.5", "4-5"):
			return "claude-opus-4.5"
		}
		return "claude-opus-4.6"
	case has("haiku"):
		if has("5.5", "5-5") {
			return "claude-haiku-5.5"
		}
		return "claude-haiku-4.5"
	case id == "auto":
		return "auto"
	case has("deepseek"):
		return "deepseek-3.2"
	case has("glm"):
		return "glm-5"
	case has("minimax"):
		if has("2.5", "2-5") {
			return "minimax-m2.5"
		}
		return "minimax-m2.1"
	case has("qwen"):
		return "qwen3-coder-next"
	case has("kimi"):
		return "kimi-k3"
	case has("gpt"):
		switch {
		case has("terra"):
			return "gpt-5.6-terra"
		case has("luna"):
			return "gpt-5.6-luna"
		case has("sol", "5.6", "5-6"):
			return "gpt-5.6-sol"
		}
	}
	return model
}
