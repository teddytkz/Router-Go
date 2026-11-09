// Package modelcaps works out what a model can do (read images, read audio, reason) from its id, and merges that
// with what the user has declared. Detection is a name heuristic: it only ever turns a capability ON, and an explicit
// setting always wins over it.
package modelcaps

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Caps are the capabilities the router can route on.
type Caps struct {
	Vision    bool `json:"vision"`
	Reasoning bool `json:"reasoning"`
	Audio     bool `json:"audioInput"`
	Context   int  `json:"contextWindow"` // max input tokens; 0 = unknown (never used to rule a model out)
}

// Keys are the names used in settings, stored overrides and the API.
const (
	Vision    = "vision"
	Reasoning = "reasoning"
	Audio     = "audioInput"
	Context   = "contextWindow"

	MinContext = 1000
	MaxContext = 100_000_000
)

func (c Caps) Has(key string) bool {
	switch key {
	case Vision:
		return c.Vision
	case Reasoning:
		return c.Reasoning
	case Audio:
		return c.Audio
	}
	return false
}

// Override is the user's explicit setting per model: bool keys for capabilities, a number for the context window.
// A missing key means "auto".
type Override = map[string]any

// Merge applies explicit overrides on top of the detected caps.
func Merge(detected Caps, override Override) Caps {
	if v, ok := override[Vision].(bool); ok {
		detected.Vision = v
	}
	if v, ok := override[Reasoning].(bool); ok {
		detected.Reasoning = v
	}
	if v, ok := override[Audio].(bool); ok {
		detected.Audio = v
	}
	if n, ok := toInt(override[Context]); ok {
		detected.Context = n
	}
	return detected
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n)) && int(n) >= MinContext && int(n) <= MaxContext
	case int:
		return n, n >= MinContext && n <= MaxContext
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil && i >= MinContext && i <= MaxContext
	}
	return 0, false
}

// ValidOverride keeps only known keys with valid values.
func ValidOverride(in Override) Override {
	out := Override{}
	for _, k := range []string{Vision, Reasoning, Audio} {
		if v, ok := in[k].(bool); ok {
			out[k] = v
		}
	}
	if n, ok := toInt(in[Context]); ok {
		out[Context] = n
	}
	return out
}

const sep = `[-_/:.]`

func re(parts ...string) *regexp.Regexp { return regexp.MustCompile("(?i)" + strings.Join(parts, "|")) }

// Not a chat model that reads images, even though the id carries a vision-looking word (image generators, embeddings, speech).
var notVision = re(`(^|`+sep+`)(image|img)(`+sep+`|$)`, "stable-image", "nanobanana", "imagine", "t2v", "i2v", "flux", "dall", "sdxl", "diffusion",
	"embed", "rerank", "guard", "moderation", "tts", "stt", "whisper", "voice", "speech", "audio")

var visionName = re(
	`(^|`+sep+`)(vision|vl|vlm|multimodal|omni|visual)(`+sep+`|$)`,
	`[0-9]\.[0-9]+v(`+sep+`|$)`,         // glm-4.6v
	`(^|`+sep+`)glm-[0-9]+v(`+sep+`|$)`, // glm-5v-turbo
	`(^|`+sep+`)(llava|pixtral|internvl|cogvlm|minicpm-v|moondream|idefics|fuyu|qvq)`,
	`qwen[0-9.]*-?vl`,
)

// Families that are natively multimodal.
var visionFamily = re(
	`(^|`+sep+`)claude-(3|sonnet-4|opus-4|haiku-4|[0-9])`, `(^|`+sep+`)claude-(sonnet|opus|haiku)`,
	`(^|`+sep+`)gpt-4o`, `(^|`+sep+`)gpt-4\.1`, `(^|`+sep+`)gpt-4-turbo`, `(^|`+sep+`)gpt-5`, `(^|`+sep+`)chatgpt-4o`,
	`(^|`+sep+`)o[134](`+sep+`|$)`,
	`(^|`+sep+`)gemini`, `(^|`+sep+`)gemma-?3`,
	`(^|`+sep+`)grok-(2-vision|4|vision)`,
	`llama-?3\.2-[0-9]+b-vision`, `llama-?4`,
	`mistral-(medium|small)-3`, `(^|`+sep+`)(mistral-large-3|ministral)`,
	`(^|`+sep+`)(kimi-k2\.[5-9]|kimi-latest|kimi-vl)`,
	`(^|`+sep+`)(doubao.*vision|step-1[vo])`,
)

var reasoningName = re(
	`(^|`+sep+`)o[134](`+sep+`|$)`, `(^|`+sep+`)gpt-5`, `(^|`+sep+`)gpt-oss`,
	`deepseek-(r1|reasoner|v3\.[1-9]|v[4-9])`,
	`(^|`+sep+`)claude-(3-7|3\.7|sonnet-4|opus-4|haiku-4|sonnet-5|opus-5|haiku-5|fable)`,
	`(^|`+sep+`)gemini-(2\.5|3)`,
	`(^|`+sep+`)(qwq|qvq)`, `qwen-?3.*(thinking|235b|30b)`, `thinking`,
	`(^|`+sep+`)grok-(3-mini|4)`,
	`(^|`+sep+`)glm-(4\.[5-9]|[5-9])`,
	`(^|`+sep+`)(kimi-k2|k2-thinking|kimi-k1)`,
	`(^|`+sep+`)minimax-m[2-9]`,
	`magistral`, `(^|`+sep+`)(r1|reasoner|reasoning)(`+sep+`|$)`, `(^|`+sep+`)hunyuan-t1`, `(^|`+sep+`)step-(r|3)`,
)

// Detect guesses capabilities from a model id (with or without a "provider/" prefix).
func Detect(model string) Caps {
	id := strings.ToLower(model)
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	var c Caps
	if !notVision.MatchString(id) && (visionName.MatchString(id) || visionFamily.MatchString(id)) {
		c.Vision = true
	}
	c.Reasoning = reasoningName.MatchString(id)
	c.Context = DetectContext(id)
	c.Audio = regexp.MustCompile(`(^|`+sep+`)(omni|gpt-4o-audio|audio-preview|gemini)`).MatchString(id) && !strings.Contains(id, "tts")
	return c
}

// explicit size in the id: gpt-4-32k, llama-3.1-8b-instruct-128k, qwen-plus-1m, claude-sonnet-4-1m
var sizeSuffix = regexp.MustCompile(`(?i)(?:^|[-_:.@])([0-9]{1,4})(k|m)(?:$|[-_:.@])`)

type ctxRule struct {
	re     *regexp.Regexp
	tokens int
}

// Published context windows of common families, most specific first. These are best-effort defaults:
// providers often serve a smaller window, so the dashboard lets the user override the number per model.
var ctxRules = []ctxRule{
	{re("gpt-4\\.1"), 1_047_576}, {re("gpt-5"), 400_000}, {re("gpt-4o", "chatgpt-4o", "gpt-4-turbo", "gpt-4-1106", "gpt-4-0125"), 128_000},
	{re("gpt-4-32k"), 32_768}, {re("(^|" + sep + ")gpt-4(-0613)?$"), 8_192}, {re("gpt-3\\.5"), 16_385},
	{re("(^|" + sep + ")o[134](" + sep + "|$)"), 200_000}, {re("gpt-oss"), 131_072},
	{re("claude-(sonnet|opus)-4.*1m", "claude.*[-_]1m"), 1_000_000}, {re("claude"), 200_000},
	{re("gemini-1\\.5-pro"), 2_097_152}, {re("gemini"), 1_048_576}, {re("gemma-?3"), 131_072}, {re("gemma"), 8_192},
	{re("llama-?4-scout"), 10_000_000}, {re("llama-?4"), 1_000_000}, {re("llama-?3\\.[1-3]", "llama-?3-1"), 131_072}, {re("llama-?3"), 8_192},
	{re("grok-4-fast", "grok-4\\.[1-9]-fast"), 2_000_000}, {re("grok-4"), 256_000}, {re("grok-(2|3)"), 131_072}, {re("grok-code"), 256_000},
	{re("deepseek-(r1|reasoner|chat|v3)"), 131_072}, {re("deepseek-v4", "deepseek-v[5-9]"), 1_000_000}, {re("deepseek"), 131_072},
	{re("qwen-long"), 10_000_000}, {re("qwen3-(coder|max|next|235b-a22b-(instruct|thinking)-2507)", "qwen3-coder"), 262_144}, {re("qwen-?3"), 131_072}, {re("qwen2\\.5-(turbo|1m)"), 1_000_000},
	{re("qwen"), 131_072}, {re("qwq", "qvq"), 131_072},
	{re("mistral-(large|medium|small)", "ministral", "pixtral-large", "magistral"), 131_072}, {re("mistral|mixtral"), 32_768}, {re("codestral"), 262_144},
	{re("glm-5", "glm-4\\.6"), 202_752}, {re("glm-4\\.5"), 131_072}, {re("glm-4-long"), 1_000_000}, {re("glm-4"), 128_000},
	{re("kimi-k2\\.[5-9]", "kimi-k2-(thinking|0905)", "kimi-latest"), 262_144}, {re("kimi|moonshot"), 131_072},
	{re("minimax-m1", "minimax-text-01"), 1_000_000}, {re("minimax-m[2-9]"), 204_800}, {re("minimax"), 245_760},
	{re("hunyuan"), 262_144}, {re("step-"), 262_144}, {re("yi-"), 200_000}, {re("command-r"), 128_000},
	{re("doubao"), 262_144}, {re("ernie"), 128_000}, {re("phi-?[34]"), 131_072}, {re("nova-(pro|lite|premier)"), 300_000},
}

// DetectContext guesses the maximum context window (tokens) from a model id; 0 means "unknown".
func DetectContext(id string) int {
	id = strings.ToLower(id)
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	if g := sizeSuffix.FindStringSubmatch(id); g != nil {
		n := 0
		for _, c := range g[1] {
			n = n*10 + int(c-'0')
		}
		if g[2] == "m" || g[2] == "M" {
			return n * 1_000_000
		}
		if n >= 4 { // "-1k" style tokens in names are not context sizes
			return n * 1024
		}
	}
	for _, r := range ctxRules {
		if r.re.MatchString(id) {
			return r.tokens
		}
	}
	return 0
}
