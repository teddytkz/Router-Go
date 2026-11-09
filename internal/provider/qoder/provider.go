// Package qoder is the built-in qoder provider.
package qoder

import "router-go/internal/provider"

func init() {
	provider.Register(provider.Provider{
		Priority: 3,
		ID:       "qoder", Alias: "qd", Name: "Qoder", Category: provider.CategoryOAuth, Status: provider.StatusUnsupported,
		Website: "https://qoder.com", AuthHint: "Personal Access Token (pt-…) from qoder.com/account/integrations",
		Note:    "Qoder speaks its own agent protocol (signed SSE agent_chat_generation), not OpenAI. The protocol translator is not ported yet, so Qoder cannot be routed.",
		BaseURL: "https://api3.qoder.sh",
		Models: []provider.Model{
			{ID: "ultimate", Name: "Ultimate"}, {ID: "auto", Name: "Auto"}, {ID: "performance", Name: "Performance"}, {ID: "efficient", Name: "Efficient"}, {ID: "lite", Name: "Lite"},
			{ID: "qmodel_38max", Name: "Qwen3.8-Max"}, {ID: "qmodel_latest", Name: "Qwen3.7-Max"}, {ID: "qmodel", Name: "Qwen3.7-Plus"}, {ID: "qfmodel", Name: "Qwen3.8-Flash"},
			{ID: "kmodel_latest", Name: "Kimi-K3"}, {ID: "kmodel", Name: "Kimi-K2.7-Code"}, {ID: "gmodel", Name: "GLM-5.3"}, {ID: "gfmodel", Name: "GLM-5.3-Flash"},
			{ID: "dmodel", Name: "DeepSeek-V4-Pro"}, {ID: "dfmodel", Name: "DeepSeek-V4-Flash"}, {ID: "mmodel", Name: "MiniMax-M3"}, {ID: "smodel", Name: "Sonus"}, {ID: "cmodel", Name: "Cantus"},
		},
	})
}
