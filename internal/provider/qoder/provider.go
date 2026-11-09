// Package qoder is the built-in qoder provider. It speaks Qoder's own agent protocol instead of OpenAI chat (see
// provider.Native): a COSY-signed request (AES + RSA + MD5 headers), a re-ordered base64 body, and answers wrapped in
// {statusCodeValue, body} SSE envelopes.
package qoder

import "router-go/internal/provider"

const note = "Qoder speaks its own COSY-signed agent protocol (AES+RSA+MD5 headers, an obfuscated body, " +
	"{statusCodeValue,body} SSE envelopes); chat completions are ported end to end. Paste a Personal Access Token (pt-…): " +
	"it is exchanged for a short-lived job token, and Qoder's account model list supplies each model's model_config. " +
	"Not ported: the model-queue wait/replay (a queued model answers 429 so the router falls back instead of holding the " +
	"request), image upload (small inline data URIs work, files are stubbed) and the 200K/400K/1M context-tier upgrade."

func init() {
	provider.Register(provider.Provider{
		Priority: 3,
		ID:       "qoder", Alias: "qd", Name: "Qoder", Category: provider.CategoryOAuth, Status: provider.StatusExperimental,
		Website: "https://qoder.com", AuthHint: "Personal Access Token (pt-…) from qoder.com/account/integrations",
		Note:    note,
		BaseURL: defaultBase,
		Native:  native,
		Models: []provider.Model{
			{ID: "ultimate", Name: "Ultimate"}, {ID: "auto", Name: "Auto"}, {ID: "performance", Name: "Performance"}, {ID: "efficient", Name: "Efficient"}, {ID: "lite", Name: "Lite"},
			{ID: "qmodel_38max", Name: "Qwen3.8-Max"}, {ID: "qmodel_latest", Name: "Qwen3.7-Max"}, {ID: "qmodel", Name: "Qwen3.7-Plus"}, {ID: "qfmodel", Name: "Qwen3.8-Flash"},
			{ID: "kmodel_latest", Name: "Kimi-K3"}, {ID: "kmodel", Name: "Kimi-K2.7-Code"}, {ID: "gmodel", Name: "GLM-5.3"}, {ID: "gfmodel", Name: "GLM-5.3-Flash"},
			{ID: "dmodel", Name: "DeepSeek-V4-Pro"}, {ID: "dfmodel", Name: "DeepSeek-V4-Flash"}, {ID: "mmodel", Name: "MiniMax-M3"}, {ID: "smodel", Name: "Sonus"}, {ID: "cmodel", Name: "Cantus"},
		},
	})
}
