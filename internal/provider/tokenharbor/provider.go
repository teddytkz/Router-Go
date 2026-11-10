// Package tokenharbor is the built-in Token Harbor provider: an OpenAI-compatible aggregator behind one prepaid key.
package tokenharbor

import "router-go/internal/provider"

func init() {
	provider.Register(provider.Provider{
		Priority: 1,
		ID:       "tokenharbor", Alias: "th", Name: "Token Harbor", Category: provider.CategoryAPIKey, Status: provider.StatusReady,
		Website: "https://tokenharbor.ai/dashboard", AuthHint: "API key from tokenharbor.ai → Dashboard",
		Note:    "OpenAI-compatible aggregator: one API key reaches every model, billed per token from a prepaid wallet. Model ids are bare (claude-opus-5.5, gpt-6-astra, deepseek-v4.1-flash:free) and the catalogue rotates, so any id is accepted; the list below is only a starting point.",
		BaseURL: "https://tokenharbor.ai/v1", Passthrough: true,
		// Seed only: the live catalogue is /v1/models. Their :free set rotates.
		Models: []provider.Model{
			{ID: "claude-opus-5.5", Name: "Claude Opus 5.5"}, {ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
			{ID: "gpt-6-astra", Name: "GPT-6 Astra"}, {ID: "gpt-6-sol", Name: "GPT-6 Sol"},
			{ID: "deepseek-v4.1-flash:free", Name: "DeepSeek V4.1 Flash (Free)"}, {ID: "grok-4.7", Name: "Grok 4.7"},
		},
	})
}
