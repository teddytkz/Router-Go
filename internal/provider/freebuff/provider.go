// Package freebuff is the built-in freebuff provider.
package freebuff

import "router-go/internal/provider"

// models is the CLI's own selectable catalogue. Which ids a profile will actually open on was read off the installed
// CLI: an id it cannot reach is not refused but silently coerced to a fallback, so a row listed here that the account
// cannot run looks exactly like one that works until you read the status bar. Kept, because live-proven on this
// account: the two Slow catalogue rows (no wire id of their own — the CLI names them by opaque key), Solar, GPT-6
// Luna, Muse Spark 1.3 and GPT-6.1 Terra. A Slow row spends the plan's hours (m-61c26317cc costs 1 of 5), so on an
// empty balance it answers "Not enough hours" rather than anything wrong with the port. The rest of the compiled
// catalogue (GLM, MiMo, Gemini, Flash Fast, GPT-6.1
// Sol) is reachable only on plans this account does not hold and lands on DeepSeek Slow instead; add those ids back
// through the dashboard's "Add model" when a plan makes them answer.
var models = []provider.Model{
	{ID: "m-61c26317cc", Name: "DeepSeek V4.1 Flash Slow"},
	{ID: "m-8db1ccbba6", Name: "DeepSeek V4.1 Flash (premium)"},
	{ID: "upstage/solar-mini4", Name: "Solar Mini 4"},
	{ID: "upstage/solar-pro4", Name: "Solar Pro 4"},
	{ID: "openai/gpt-6-luna", Name: "GPT-6 Luna"},
	{ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3"},
	{ID: "openai/gpt-6.1-terra", Name: "GPT-6.1 Terra"},
}

func init() {
	provider.Register(provider.Provider{
		Priority: 1,
		ID:       "freebuff", Alias: "fb", Name: "Freebuff", Category: provider.CategoryFree, Status: provider.StatusExperimental, FreeRefresh: "daily", NoAuth: true,
		Website: "https://freebuff.com", AuthHint: "Sign in once with the freebuff CLI (it stores the login itself)",
		Note: "Driven through the vendor's own freebuff CLI, which must be installed and signed in on this machine (and tmux must be present). " +
			"The backend refuses freebuff's protocol from anything but the official binary and bills account strikes for trying, so this port never talks to the backend itself: it runs the real CLI in a private tmux session and reads answers from the CLI's own chat log. " +
			"One CLI session holds a paid hour that is pinned to a single model, so a model switch restarts that hour — and while the manual CLI holds one, requests here fail with a model mismatch. The account's own plan decides which of the models below will actually answer.",
		ForceStream: true, Native: &provider.Native{Local: local},
		Models: models,
	})
}
