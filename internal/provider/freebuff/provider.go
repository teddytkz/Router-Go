// Package freebuff is the built-in freebuff provider.
package freebuff

import "router-go/internal/provider"

func init() {
	provider.Register(provider.Provider{
		Priority: 1,
		ID:       "freebuff", Alias: "fb", Name: "Freebuff", Category: provider.CategoryFree, Status: provider.StatusExperimental, FreeRefresh: "daily", Usage: usage,
		Website: "https://freebuff.com", AuthHint: "Freebuff auth token (from a freebuff.com login)",
		Note:    "Plain OpenAI-compatible call to the Codebuff backend. The free tier also needs a per-model session and run registration plus the official CLI's identity headers, which are not reproduced here, so requests are likely to be refused.",
		BaseURL: "https://www.codebuff.com/api/v1", OAuthBase: "https://freebuff.com", OAuth: oauth,
		Models: []provider.Model{
			{ID: "z-ai/glm-5.3-flash", Name: "GLM 5.3 Flash"}, {ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash"}, {ID: "openai/gpt-5.6-luna", Name: "GPT-5.6 Luna"},
			{ID: "mimo/mimo-v2.5", Name: "MiMo 2.5"}, {ID: "upstage/solar-pro4", Name: "Solar Pro 4"}, {ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3"},
			{ID: "meta/muse-spark-1.2-contributor", Name: "Muse Spark 1.2"}, {ID: "anthropic/claude-fable-5", Name: "Claude Fable 5"},
		},
	})
}
