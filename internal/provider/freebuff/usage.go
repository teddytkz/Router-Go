package freebuff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"router-go/internal/provider"
)

// usage ports XRouter-Next's getFreebuffUsage. The daily session quota lives on the session endpoint itself and is read
// with GET only: a POST would claim a session and burn a unit of the quota.
func usage(ctx context.Context, d provider.Doer, c provider.UsageCall) (provider.Usage, error) {
	if c.Token == "" {
		return provider.Usage{Message: "Freebuff credential not available — connect a Freebuff login first."}, nil
	}
	raw, status, err := provider.DoJSON(ctx, d, http.MethodGet, c.BaseURL+sessionHr, map[string]string{
		"Authorization": "Bearer " + c.Token, "User-Agent": cliUA, "x-freebuff-client-version": cliVer,
	}, nil)
	if err != nil {
		return provider.Usage{Message: "Freebuff usage error: " + err.Error()}, nil
	}
	var body struct {
		Status       string                `json:"status"`
		Model        string                `json:"model"`
		AccessTier   string                `json:"accessTier"`
		Message      string                `json:"message"`
		RateLimit    *rateLimit            `json:"rateLimit"`
		RateLimitsBy map[string]*rateLimit `json:"rateLimitsByModel"`
	}
	_ = json.Unmarshal(raw, &body)
	switch {
	case status == 401:
		return provider.Usage{Message: "Freebuff credential invalid or expired — re-login in the dashboard."}, nil
	case status == 403 && body.Status == "country_blocked":
		return provider.Usage{Message: "Freebuff is not available in your region."}, nil
	case status == 403 && body.Status == "banned":
		return provider.Usage{Message: "Your Freebuff account has been banned."}, nil
	case status == 403:
		msg := ""
		if body.Message != "" {
			msg = ": " + body.Message
		}
		return provider.Usage{Message: fmt.Sprintf("Freebuff quota access denied (403)%s.", msg)}, nil
	case status == 404:
		return provider.Usage{Plan: "Freebuff", Message: "Freebuff connected. No session quota to report right now."}, nil
	case status >= 300:
		return provider.Usage{Message: fmt.Sprintf("Freebuff quota API error (%d).", status)}, nil
	}
	limits := body.RateLimitsBy
	if limits == nil {
		limits = map[string]*rateLimit{}
	}
	if body.Status == "active" && body.RateLimit != nil && limits[body.Model] == nil {
		limits[body.Model] = body.RateLimit
	}
	names := map[string]string{}
	if p, ok := provider.ByID("freebuff"); ok {
		for _, m := range p.Models {
			names[m.ID] = m.Name
		}
	}
	quotas := map[string]provider.Quota{}
	for model, rl := range limits {
		if rl == nil {
			continue
		}
		quotas[model] = provider.Quota{Used: rl.RecentCount, Total: rl.Limit, ResetAt: provider.ParseReset(rl.ResetAt), Recurring: new(true), DisplayName: names[model]}
	}
	plan := "Freebuff"
	if body.AccessTier == "limited" {
		plan = "Freebuff (Limited)"
	}
	if len(quotas) == 0 {
		return provider.Usage{Plan: plan, Message: "Freebuff connected. No session quota to report right now."}, nil
	}
	return provider.Usage{Plan: plan, Quotas: quotas}, nil
}

// rateLimit is one model's share of the session quota; recentCount is fractional (a long agent run costs more than 1).
type rateLimit struct {
	Limit       float64 `json:"limit"`
	RecentCount float64 `json:"recentCount"`
	ResetAt     any     `json:"resetAt"`
}
