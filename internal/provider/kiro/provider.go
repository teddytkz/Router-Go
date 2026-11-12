// Package kiro is the built-in kiro provider: Amazon's CodeWhisperer / Kiro runtime. It speaks AWS's binary event
// stream instead of OpenAI chat (see provider.Native), and its sign-in has several flavours (see oauth.go).
package kiro

import (
	"router-go/internal/provider"
)

// variants: ids that exist only in the router. "-thinking" asks for reasoning, "-agentic" adds a chunked-write
// system prompt; both are the same upstream model (see resolveModel).
var families = [][2]string{
	{"claude-haiku-4.5", "Claude Haiku 4.5"}, {"claude-sonnet-4.5", "Claude Sonnet 4.5"}, {"claude-sonnet-5", "Claude Sonnet 5"},
	{"claude-opus-5.5", "Claude Opus 5.5"}, {"claude-opus-5", "Claude Opus 5"}, {"claude-opus-4.8", "Claude Opus 4.8"},
	{"claude-opus-4.7", "Claude Opus 4.7"}, {"claude-opus-4.5", "Claude Opus 4.5"},
	{"gpt-5.6-sol", "GPT 5.6 Sol"}, {"gpt-5.6-terra", "GPT 5.6 Terra"}, {"gpt-5.6-luna", "GPT 5.6 Luna"},
}

func models() []provider.Model {
	var out []provider.Model
	for _, f := range families {
		out = append(out, provider.Model{ID: f[0], Name: f[1]},
			provider.Model{ID: f[0] + thinkingSuffix, Name: f[1] + " (Thinking)"},
			provider.Model{ID: f[0] + agenticSuffix, Name: f[1] + " (Agentic)"},
			provider.Model{ID: f[0] + thinkingSuffix + agenticSuffix, Name: f[1] + " (Thinking + Agentic)"})
	}
	return append(out, provider.Model{ID: "deepseek-3.2", Name: "DeepSeek 3.2"}, provider.Model{ID: "qwen3-coder-next", Name: "Qwen3 Coder Next"},
		provider.Model{ID: "glm-5", Name: "GLM 5"}, provider.Model{ID: "MiniMax-M2.5", Name: "MiniMax M2.5"})
}

const note = "RISK: Kiro is meant for the Kiro IDE and CLI. Using an account through a router is not licensed by Amazon/Kiro and may get the account " +
	"restricted or suspended: use at your own risk (XRouter-Next marks it deprecated for the same reason). Experimental: the wire protocol is ported " +
	"from XRouter-Next and has only been tested against mocks. Requests identify as Kiro IDE " + kiroVersion + " (see identity.go); the " +
	"Custom Headers card still overrides any header."

func init() {
	provider.Register(provider.Provider{
		Priority: 3,
		ID:       "kiro", Alias: "kr", Name: "Kiro", Category: provider.CategoryFree, Status: provider.StatusExperimental,
		Website: "https://kiro.dev",
		AuthHint: "OAuth button: AWS Builder ID. Or paste (Add key) a refresh token (Google/GitHub accounts: refreshToken of Kiro's ~/.aws/sso/cache/kiro-auth-token.json), " +
			`or a JSON: IAM Identity Center {"refreshToken":"…","clientId":"…","clientSecret":"…","region":"us-east-1"}, ` +
			`enterprise IdP {"authMethod":"external_idp","refreshToken":"…","clientId":"…","tokenEndpoint":"https://login.microsoftonline.com/…","scope":"…","profileArn":"…"}`,
		Note:    note,
		BaseURL: defaultBase,
		// The identity headers are per account, so they are built per request (identity.go) instead of here.
		OAuthBase: "https://prod.us-east-1.auth.desktop.kiro.dev", OAuth: oauth,
		Native: native, Usage: usage,
		Models: models(),
	})
}
