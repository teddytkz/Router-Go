package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"router-go/internal/provider"
)

const (
	limitsPath      = "/getUsageLimits"
	codewhispererUS = "https://codewhisperer.us-east-1.amazonaws.com"
	usageTarget     = "AmazonCodeWhispererService.GetUsageLimits"
)

// usage ports XRouter-Next's getKiroUsage: the same three endpoint attempts (CodeWhisperer GET, CodeWhisperer POST,
// Amazon Q GET), the first that answers 2xx wins. Only the provider's own static headers are sent: the extra
// "KiroIDE" identity headers XRouter adds to the first attempt are left out.
func usage(ctx context.Context, d provider.Doer, c provider.UsageCall) (provider.Usage, error) {
	method, _ := c.Data["authMethod"].(string)
	if method == "" {
		method = "builder-id"
	}
	auth := map[string]string{"Authorization": "Bearer " + c.Token}
	switch method {
	case "api_key": // a long-lived key, not an OIDC token
		auth["tokentype"] = "API_KEY"
	case "external_idp":
		auth["TokenType"] = "EXTERNAL_IDP"
	}
	arn := profileARN(c.Data)
	cw, q := codewhispererUS, c.BaseURL // a mirror (tests) answers every attempt
	if c.BaseURL != defaultBase {
		cw = c.BaseURL
	}
	qs := url.Values{"origin": {"AI_EDITOR"}, "resourceType": {"AGENTIC_REQUEST"}}
	if arn != "" {
		qs.Set("profileArn", arn)
	}
	post := map[string]any{"origin": "AI_EDITOR", "resourceType": "AGENTIC_REQUEST"}
	if arn != "" {
		post["profileArn"] = arn
	}
	postHeaders := map[string]string{"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": usageTarget}
	for k, v := range auth {
		postHeaders[k] = v
	}
	attempts := []struct {
		name, method, url string
		headers           map[string]string
		body              any
	}{
		{"codewhisperer-get", "GET", cw + limitsPath + "?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST", auth, nil},
		{"codewhisperer-post", "POST", cw, postHeaders, post},
		{"q-get", "GET", q + limitsPath + "?" + qs.Encode(), auth, nil},
	}
	var errs []string
	sawAuth := false
	for _, a := range attempts {
		raw, status, err := provider.DoJSON(ctx, d, a.method, a.url, a.headers, a.body)
		switch {
		case err != nil:
			errs = append(errs, a.name+":"+err.Error())
		case status >= 300:
			sawAuth = sawAuth || status == 401 || status == 403
			errs = append(errs, fmt.Sprintf("%s:%d:%s", a.name, status, provider.Snippet(raw)))
		default:
			return parseUsage(raw)
		}
	}
	msg := "Unable to fetch Kiro usage right now."
	if len(errs) > 0 {
		msg = fmt.Sprintf("Unable to fetch Kiro usage right now. (%s)", errs[len(errs)-1])
	}
	switch {
	case sawAuth && method == "idc":
		msg = "Kiro quota API is unavailable for the current AWS IAM Identity Center session. Chat may still work. If this persists after renewing your session, reconnect Kiro."
	case sawAuth && (method == "google" || method == "github"):
		msg = "Kiro quota API authentication expired. Chat may still work."
	case sawAuth:
		msg = "Kiro quota API rejected the current token. Chat may still work."
	}
	return provider.Usage{Message: msg}, nil
}

type kiroLimit struct {
	Used  float64 `json:"currentUsageWithPrecision"`
	Limit float64 `json:"usageLimitWithPrecision"`
}

func parseUsage(raw []byte) (provider.Usage, error) {
	var data struct {
		List []struct {
			ResourceType string `json:"resourceType"`
			kiroLimit
			FreeTrial *struct {
				kiroLimit
				Expiry any `json:"freeTrialExpiry"`
			} `json:"freeTrialInfo"`
		} `json:"usageBreakdownList"`
		NextReset any `json:"nextDateReset"`
		ResetDate any `json:"resetDate"`
		Sub       struct {
			Title string `json:"subscriptionTitle"`
		} `json:"subscriptionInfo"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return provider.Usage{}, err
	}
	reset := provider.ParseReset(data.NextReset)
	if reset == nil {
		reset = provider.ParseReset(data.ResetDate)
	}
	quotas := map[string]provider.Quota{}
	row := func(l kiroLimit, at *string) provider.Quota {
		rem := l.Limit - l.Used
		return provider.Quota{Used: l.Used, Total: l.Limit, Remaining: &rem, ResetAt: at}
	}
	for _, b := range data.List {
		name := strings.ToLower(b.ResourceType)
		if name == "" {
			name = "unknown"
		}
		quotas[name] = row(b.kiroLimit, reset)
		if ft := b.FreeTrial; ft != nil {
			at := provider.ParseReset(ft.Expiry)
			if at == nil {
				at = reset
			}
			quotas[name+"_freetrial"] = row(ft.kiroLimit, at)
		}
	}
	plan := data.Sub.Title
	if plan == "" {
		plan = "Kiro"
	}
	return provider.Usage{Plan: plan, Quotas: quotas}, nil
}
