package modelcaps

import "testing"

func TestDetect(t *testing.T) {
	for id, want := range map[string]Caps{
		"gpt-4o":                 {Vision: true},
		"openai/gpt-4o-mini":     {Vision: true},
		"gpt-5":                  {Vision: true, Reasoning: true},
		"o3-mini":                {Vision: true, Reasoning: true},
		"claude-sonnet-4-5":      {Vision: true, Reasoning: true},
		"claude-3-5-haiku":       {Vision: true},
		"deepseek-v4.1-flash":    {Reasoning: true},
		"deepseek-r1":            {Reasoning: true},
		"deepseek-chat":          {},
		"qwen3-vl-plus":          {Vision: true},
		"qwen2.5-vl-72b":         {Vision: true},
		"glm-4.6v":               {Vision: true, Reasoning: true},
		"glm-5v-turbo":           {Vision: true, Reasoning: true},
		"llava-1.6":              {Vision: true},
		"llama-3.1-70b":          {},
		"gemini-2.5-pro":         {Vision: true, Reasoning: true, Audio: true},
		"gpt-image-1":            {},
		"flux-schnell":           {},
		"text-embedding-3-small": {},
		"whisper-1":              {},
		"kimi-k2.5":              {Vision: true, Reasoning: true},
		"grok-4.6":               {Vision: true, Reasoning: true},
		"mistral-7b-instruct":    {},
	} {
		got := Detect(id)
		if got.Vision != want.Vision || got.Reasoning != want.Reasoning || (want.Audio && !got.Audio) {
			t.Errorf("%s: got %+v want %+v", id, got, want)
		}
	}
}

func TestMergeOverridesWinBothWays(t *testing.T) {
	d := Detect("deepseek-chat") // detected: nothing
	if c := Merge(d, Override{Vision: true}); !c.Vision || c.Reasoning {
		t.Errorf("force on: %+v", c)
	}
	d = Detect("gpt-4o") // detected: vision
	if c := Merge(d, Override{Vision: false}); c.Vision {
		t.Errorf("force off: %+v", c)
	}
	if c := Merge(d, nil); !c.Vision {
		t.Errorf("auto keeps the detection: %+v", c)
	}
	if got := ValidOverride(Override{Vision: true, "bogus": true, Context: float64(5)}); len(got) != 1 || got[Vision] != true {
		t.Errorf("ValidOverride = %v", got)
	}
}

func TestDetectContext(t *testing.T) {
	for id, want := range map[string]int{
		"gpt-4o":                    128_000,
		"openai/gpt-4.1-mini":       1_047_576,
		"gpt-5":                     400_000,
		"o3-mini":                   200_000,
		"o1":                        200_000,
		"gpt-4":                     8_192,
		"gpt-4-32k":                 32 * 1024,
		"claude-sonnet-4-5":         200_000,
		"claude-sonnet-4-1m":        1_000_000,
		"gemini-2.5-pro":            1_048_576,
		"gemini-1.5-pro":            2_097_152,
		"deepseek-v4.1-flash":       1_000_000,
		"deepseek-chat":             131_072,
		"qwen-plus-1m":              1_000_000,
		"llama-3.1-70b-instruct":    131_072,
		"llama-3-8b":                8_192,
		"grok-4.6":                  256_000,
		"glm-5.0":                   202_752,
		"kimi-k2.6":                 262_144,
		"minimax-m2.7":              204_800,
		"mistral-7b-instruct-v0.3":  32_768,
		"my-private-finetune":       0,
		"text-embedding-3-small":    0,
		"llama-3.1-8b-instruct-64k": 64 * 1024,
	} {
		if got := DetectContext(id); got != want {
			t.Errorf("%s: got %d want %d", id, got, want)
		}
	}
	d := Detect("gpt-4o")
	if d.Context != 128_000 {
		t.Fatalf("Detect carries the context window: %+v", d)
	}
	if c := Merge(d, Override{Context: float64(32000)}); c.Context != 32000 {
		t.Errorf("override: %d", c.Context)
	}
	if c := Merge(d, Override{Context: float64(5)}); c.Context != 128_000 {
		t.Errorf("an out-of-range override is ignored: %d", c.Context)
	}
}
