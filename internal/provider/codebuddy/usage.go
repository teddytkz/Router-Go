package codebuddy

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"router-go/internal/provider"
)

// account is one credit package of the billing answer. Two kinds are never merged:
//
//	refill: a recurring allowance whose cycle ends long before the package expires; the live numbers are the Cycle*
//	        fields and ResetAt is the next refresh.
//	bonus:  one-shot credits that run a single cycle (CycleEndTime == DeductionEndTime); numbers are the plain
//	        Capacity* fields and ResetAt is the expiry.
type account struct {
	PackageName, SubProductName                    string
	CycleStartTime, CycleEndTime, DeductionEndTime any
	CycleCapacityUsedPrecise, CycleCapacityUsed    any
	CycleCapacitySizePrecise, CycleCapacitySize    any
	CapacityUsedPrecise, CapacityUsed              any
	CapacitySizePrecise, CapacitySize              any
}

// usage ports XRouter-Next's getCodeBuddyIntlUsage (billing endpoint, payload wrapped twice under data.Response.Data).
// It sends the provider's static headers only (the Doer adds them), no per-request ids.
func usage(ctx context.Context, d provider.Doer, c provider.UsageCall) (provider.Usage, error) {
	if c.Token == "" {
		return provider.Usage{Message: "CodeBuddy (codebuddy-intl) credential not available."}, nil
	}
	raw, status, err := provider.DoJSON(ctx, d, "POST", c.BaseURL+"/billing/meter/get-user-resource", map[string]string{"Authorization": "Bearer " + c.Token}, map[string]any{})
	if err != nil {
		return provider.Usage{Message: "CodeBuddy (codebuddy-intl) error: " + err.Error()}, nil
	}
	switch {
	case status == 401 || status == 403:
		return provider.Usage{Message: "CodeBuddy credential invalid or expired."}, nil
	case status >= 300:
		return provider.Usage{Message: fmt.Sprintf("CodeBuddy quota API error (%d).", status)}, nil
	}
	var body struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Response struct {
				Data struct{ Accounts []account }
			}
		}
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return provider.Usage{Message: "CodeBuddy (codebuddy-intl) error: " + err.Error()}, nil
	}
	if body.Code == nil || *body.Code != 0 {
		return provider.Usage{Message: "CodeBuddy quota error: " + cmp.Or(body.Msg, "unknown")}, nil
	}
	accounts := body.Data.Response.Data.Accounts
	if len(accounts) == 0 {
		return provider.Usage{Message: "CodeBuddy connected. No credit package found."}, nil
	}

	cycleEnd := func(a account) float64 { // ms, +Inf when unknown
		if t := provider.ParseReset(a.CycleEndTime); t != nil {
			p, _ := time.Parse(time.RFC3339Nano, *t)
			return float64(p.UnixMilli())
		}
		return math.Inf(1)
	}
	const refillGap = 2 * 24 * float64(time.Hour/time.Millisecond) // a gap over 2 days between cycle end and expiry = refill pack
	isRefill := func(a account) bool {
		de, ok := number(a.DeductionEndTime)
		return ok && de-cycleEnd(a) > refillGap
	}
	var refills, bonuses []account
	for _, a := range accounts {
		if isRefill(a) {
			refills = append(refills, a)
		} else {
			bonuses = append(bonuses, a)
		}
	}
	byExpiry := func(a, b account) int { return cmp.Compare(cycleEnd(a), cycleEnd(b)) }
	slices.SortStableFunc(refills, byExpiry)
	slices.SortStableFunc(bonuses, byExpiry)

	quotas := map[string]provider.Quota{}
	seen := map[string]int{}
	for _, a := range refills {
		base := cadence(a)
		seen[base]++
		name := base
		if seen[base] > 1 {
			name = fmt.Sprintf("%s %d", base, seen[base])
		}
		quotas[name] = provider.Quota{Used: pick(a.CycleCapacityUsedPrecise, a.CycleCapacityUsed), Total: pick(a.CycleCapacitySizePrecise, a.CycleCapacitySize),
			ResetAt: provider.ParseReset(a.CycleEndTime), Recurring: new(true)}
	}
	for i, a := range bonuses {
		quotas[fmt.Sprintf("Bonus Pack %d", i+1)] = provider.Quota{Used: pick(a.CapacityUsedPrecise, a.CapacityUsed), Total: pick(a.CapacitySizePrecise, a.CapacitySize),
			ResetAt: provider.ParseReset(a.CycleEndTime), Recurring: new(false)}
	}
	first := accounts[0]
	if len(refills) > 0 {
		first = refills[0]
	}
	return provider.Usage{Plan: cmp.Or(first.PackageName, first.SubProductName, "CodeBuddy"), Quotas: quotas}, nil
}

// number reads a JSON number or numeric string; ok is false for anything else (JS Number() would give NaN).
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// pick prefers the exact *Precise field and falls back to the plain number; unreadable = 0.
func pick(precise, plain any) float64 {
	if precise == nil {
		precise = plain
	}
	n, _ := number(precise)
	return n
}

// cadence labels a refill pack by its cycle length (Monthly is the common case).
func cadence(a account) string {
	s, e := provider.ParseReset(a.CycleStartTime), provider.ParseReset(a.CycleEndTime)
	if s != nil && e != nil {
		ts, _ := time.Parse(time.RFC3339Nano, *s)
		te, _ := time.Parse(time.RFC3339Nano, *e)
		switch days := te.Sub(ts).Hours() / 24; {
		case days <= 1.5:
			return "Daily"
		case days <= 10:
			return "Weekly"
		}
	}
	return "Monthly"
}
