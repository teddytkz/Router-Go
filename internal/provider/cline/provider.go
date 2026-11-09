// Package cline is the built-in cline provider.
package cline

import (
	"runtime"

	"router-go/internal/config"
	"router-go/internal/provider"
)

func init() {
	provider.Register(provider.Provider{
		Priority: 1,
		ID:       "cline", Alias: "cl", Name: "Cline", Category: provider.CategoryOAuth, Status: provider.StatusReady,
		Website: "https://app.cline.bot", AuthHint: "API key from app.cline.bot → Settings → API Keys",
		Note:        "Paste a Cline API key. Models named cline-free/* are only served to Cline's own apps and may answer 403.",
		BaseURL:     "https://api.cline.bot/api/v1",
		Headers:     map[string]string{"HTTP-Referer": "https://cline.bot", "X-Title": "Cline"},
		ForceStream: true, Envelope: true,
		OAuthBase: "https://api.cline.bot", OAuth: oauth, OAuthSecret: workosSecret,
		Models: []provider.Model{
			{ID: "anthropic/claude-opus-4.7", Name: "Claude Opus 4.7"}, {ID: "anthropic/claude-sonnet-4.6", Name: "Claude Sonnet 4.6"},
			{ID: "anthropic/claude-opus-4.6", Name: "Claude Opus 4.6"}, {ID: "openai/gpt-5.3-codex", Name: "GPT-5.3 Codex"}, {ID: "openai/gpt-5.4", Name: "GPT-5.4"},
			{ID: "google/gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview"}, {ID: "google/gemini-3.1-flash-lite-preview", Name: "Gemini 3.1 Flash Lite Preview"},
			{ID: "kwaipilot/kat-coder-pro", Name: "KAT Coder Pro"},
			{ID: "cline-free/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 (Free)"}, {ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash (Free)"},
			{ID: "z-ai/glm-5.3-flash", Name: "GLM 5.3 Flash (Free)"}, {ID: "cline-free/solar-pro4", Name: "Solar Pro 4 (Free)"},
			{ID: "cline-free/longcat-2.0", Name: "LongCat 2.0 (Free)"}, {ID: "poolside/laguna-s-2.1:free", Name: "Laguna S 2.1 (Free)"},
			{ID: "dots-studio/dots-3-note-preview:free", Name: "Dots 3 Note Preview (Free)"},
		},
	})
}

// headers are the defaults XRouter-Next sends to Cline (shown, editable and resettable in the dashboard).
func headers() map[string]string {
	return map[string]string{
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"X-PLATFORM":         runtime.GOOS,
		"X-PLATFORM-VERSION": runtime.Version(),
		"X-CLIENT-TYPE":      "cline",
		"X-CLIENT-VERSION":   config.Version,
		"X-CORE-VERSION":     config.Version,
		"X-IS-MULTIROOT":     "false",
	}
}
