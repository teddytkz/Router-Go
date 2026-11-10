// Package opencodefree is the built-in opencode provider: OpenCode Zen's free tier, no key.
package opencodefree

import "router-go/internal/provider"

// freeModels is the live free catalogue of /inference/v1/models (cost 0). A model's lane is not Model.Format:
// the gateway must keep treating this provider as a chat upstream, so laneOf picks the URL and converts here.
var freeModels = []provider.Model{
	{ID: "big-pickle", Name: "Big Pickle"},
	{ID: "mimo-v2.6-flash-free", Name: "MiMo-V2.6-Flash Free"},
	{ID: "deepseek-v4.1-flash-free", Name: "DeepSeek V4.1 Flash Free"},
	{ID: "glm-5.3-flash-free", Name: "GLM-5.3-Flash Free"},
	{ID: "crouton-alpha-free", Name: "Crouton Alpha Free"},
	{ID: "longcat-2.5-free", Name: "LongCat 2.5 Free"},
	{ID: "iceman-free", Name: "Iceman Free"},
	{ID: "inkling-2-small-free", Name: "Inkling 2 Small Free"},
	{ID: "beam-free", Name: "Beam Free"},
	{ID: "flint-40b-free", Name: "Flint 40B Free"},
	{ID: "nemotron-3.5-super-vl-free", Name: "Nemotron 3.5 Super VL Free"},
	{ID: "nemotron-3.5-lightning-free", Name: "Nemotron 3.5 Lightning Free"},
}

func init() {
	provider.Register(provider.Provider{
		Priority: 2,
		ID:       "opencode", Alias: "oc", Name: "OpenCode Free", Category: provider.CategoryFree, Status: provider.StatusExperimental, NoAuth: true,
		Website: "https://opencode.ai", AuthHint: "No key needed", FreeRefresh: "daily",
		Note:    "Free models from OpenCode Zen, no key. The service only serves OpenCode's own client (its TLS fingerprint, its session header and its tool/env fingerprint), which this provider reproduces; the free tier is also rate-limited per session, so a request may answer 429 at peak times.",
		BaseURL: defaultBase, ForceStream: true, Passthrough: true,
		Native: native, Transport: transport, DirectOnly: true,
		ProxyDialer: true, ProxyRun: ProxyForConn,
		Models: freeModels,
	})
}
