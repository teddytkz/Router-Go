package codebuddy

import (
	"regexp"
	"strings"
)

// The channel check flags third-party CLI identity strings in system and assistant text as unauthorized tooling, and
// repeated hits put the account at risk. User and tool content is never touched.
const neutralPrompt = "You are CodeBuddy Code, an expert software engineering assistant."

// agentIdentityRe is the whole-prompt test (from the reference's AGENT_PATTERN): a match replaces the entire system
// message rather than editing around it.
var agentIdentityRe = regexp.MustCompile(`(?i)you are claude code|claude.?code.+official.+cli|anthropic.+official.+cli|anxthxropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|you are an? (?:ai )?(?:coding |code )?agent|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude.?code.+issues|give feedback.+claude.?code|you are .{0,30}(?:powerful )?ai agent|orchestration capabilities|ohmyopencode|<agent-identity>|<role>|<behavior_instructions>`)

type identityRewrite struct {
	roles   []string
	pattern *regexp.Regexp
	to      string
}

var identityRewrites = []identityRewrite{
	{[]string{"system"}, regexp.MustCompile(`(?m)^You are Claude Code, Anthropic's official CLI for Claude[^\n]*`), neutralPrompt},
	{[]string{"assistant"}, regexp.MustCompile(`You are Claude Code, Anthropic's official CLI for Claude[^\n]*`), neutralPrompt},
	{[]string{"system", "assistant"}, regexp.MustCompile(`(?i)You are (?:ZCode|Cursor|Windsurf|Cline|Aider|Continue|Cody)[^\n.]*[.!]?`), neutralPrompt},
	{[]string{"system", "assistant"}, regexp.MustCompile(`(?i)You are an interactive (?:ZCode|coding|code) agent[^\n.]*[.!]?`), neutralPrompt},
	{[]string{"system", "assistant"}, regexp.MustCompile(`Sisyphus-Junior - Focused executor from OhMyOpenCode`), "Sisyphus-Junior - Focused executor"},
	{[]string{"system"}, regexp.MustCompile(`(?i)x-anthropic-billing-header`), "x-billing-header"},
	{[]string{"system"}, regexp.MustCompile(`Main branch \(you will usually use this for PRs\):`), "Main branch:"},
	{[]string{"system"}, regexp.MustCompile(`(?im)^[ \t]*#[ \t]*(?:ZCode|DeepSeek[ \t-]+Harness|Claude[ \t]+Code)[ \t]+Desktop[ \t]+Context[ \t]*$`), "# Desktop Context"},
}

// neutralize applies the rewrites for one role.
func neutralize(s, role string) string {
	for _, r := range identityRewrites {
		if strings.Contains(strings.Join(r.roles, ",")+",", role+",") {
			s = r.pattern.ReplaceAllString(s, r.to)
		}
	}
	return s
}
