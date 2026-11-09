// Package opencodefree is the built-in opencode provider.
package opencodefree

import "router-go/internal/provider"

func init() {
	provider.Register(provider.Provider{
		Priority: 2,
		ID:       "opencode", Alias: "oc", Name: "OpenCode Free", Category: provider.CategoryFree, Status: provider.StatusExperimental, NoAuth: true,
		Website: "https://opencode.ai", AuthHint: "No key needed",
		Note:    "Free models from OpenCode Zen, no key. The service only recognises OpenCode's own clients (client identity headers and TLS fingerprint), which are not reproduced here, so it may answer 403.",
		BaseURL: "https://opencode.ai/inference/v1", ForceStream: true, Passthrough: true,
		Models: []provider.Model{
			{ID: "muse-spark-1.2-contributor-free", Name: "Muse Spark 1.2 Contributor Free", Format: "responses"},
			{ID: "muse-spark-1.3-contributor-free", Name: "Muse Spark 1.3 Contributor Free", Format: "responses"},
			{ID: "union-alpha", Name: "Union Alpha Free", Format: "messages"},
		},
	})
}
