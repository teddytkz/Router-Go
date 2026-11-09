package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"router-go/internal/db"
)

type quotaInput struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	Target  string `json:"target"`
	Metric  string `json:"metric"`
	Window  string `json:"window"`
	Mode    string `json:"mode"`
	Limit   int64  `json:"limit"`
	Enforce bool   `json:"enforce"`
}

// validate checks the input and returns the limit to store, or a message for the client.
func (a *API) validateQuota(in quotaInput) (db.QuotaLimit, string) {
	l := db.QuotaLimit{Name: strings.TrimSpace(in.Name), Scope: in.Scope, Target: in.Target, Metric: in.Metric, Window: in.Window, Mode: in.Mode, Limit: in.Limit, Enforce: in.Enforce}
	if l.Mode == "" {
		l.Mode = "calendar"
	}
	switch {
	case l.Scope != "provider" && l.Scope != "connection":
		return l, "scope must be 'provider' or 'connection'"
	case l.Metric != "requests" && l.Metric != "tokens":
		return l, "metric must be 'requests' or 'tokens'"
	case l.Window != "hour" && l.Window != "day" && l.Window != "week" && l.Window != "month":
		return l, "window must be hour, day, week or month"
	case l.Mode != "calendar" && l.Mode != "rolling":
		return l, "mode must be 'calendar' or 'rolling'"
	case l.Limit < 1 || l.Limit > 1_000_000_000_000:
		return l, "limit must be between 1 and 1,000,000,000,000"
	case len(l.Name) > 80:
		return l, "name must be at most 80 characters"
	}
	if l.Scope == "provider" {
		if _, err := a.Store.Node(l.Target); err != nil {
			return l, "unknown provider"
		}
	} else if _, err := a.Store.Connection(l.Target); err != nil {
		return l, "unknown connection"
	}
	if l.Name == "" {
		l.Name = a.quotaTargetName(l) + " · " + l.Metric + "/" + l.Window
	}
	return l, ""
}

func (a *API) quotaTargetName(l db.QuotaLimit) string {
	if l.Scope == "provider" {
		if n, err := a.Store.Node(l.Target); err == nil {
			return n.Name
		}
		return l.Target
	}
	if c, err := a.Store.Connection(l.Target); err == nil {
		if n, err := a.Store.Node(c.Provider); err == nil {
			return n.Name + "/" + c.Name
		}
		return c.Name
	}
	return l.Target
}

func (a *API) quotaView(l db.QuotaLimit, now time.Time) map[string]any {
	st, _ := a.Store.QuotaState(l, now)
	pct := 0.0
	if l.Limit > 0 {
		pct = float64(st.Used) / float64(l.Limit) * 100
	}
	return map[string]any{"id": l.ID, "name": l.Name, "scope": l.Scope, "target": l.Target, "targetName": a.quotaTargetName(l), "metric": l.Metric,
		"window": l.Window, "mode": l.Mode, "limit": l.Limit, "enforce": l.Enforce, "createdAt": l.CreatedAt,
		"used": st.Used, "remaining": max(l.Limit-st.Used, 0), "percent": pct, "windowStart": st.WindowStart, "resetAt": st.ResetAt, "exhausted": st.Exhausted}
}

// listQuota: every limit with its current usage, plus per-provider usage for today and this month (for context).
func (a *API) listQuota(w http.ResponseWriter, _ *http.Request) {
	limits, err := a.Store.QuotaLimits()
	if err != nil {
		dbErr(w, err)
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(limits))
	for _, l := range limits {
		out = append(out, a.quotaView(l, now))
	}
	usage, err := a.Store.ProviderUsage(now)
	if err != nil {
		dbErr(w, err)
		return
	}
	for _, u := range usage {
		if n, err := a.Store.Node(u["provider"].(string)); err == nil {
			u["name"] = n.Name
		} else {
			u["name"] = u["provider"]
		}
	}
	writeJSON(w, 200, map[string]any{"limits": out, "usage": usage, "connections": a.trackedConnections(now)})
}

// trackedConnections: every connection (built-in and custom) with what it has used, so the tracker can show accounts
// that have no limit as well. Sorted by provider, then priority.
func (a *API) trackedConnections(now time.Time) []map[string]any {
	conns, err := a.Store.Connections("", false)
	if err != nil {
		return []map[string]any{}
	}
	usage, _ := a.Store.ConnectionUsage(now)
	byConn := map[string]db.ConnectionUsage{}
	for _, u := range usage {
		byConn[u.ConnectionID] = u
	}
	names := map[string]string{}
	out := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		if _, ok := names[c.Provider]; !ok {
			if n, err := a.Store.Node(c.Provider); err == nil {
				names[c.Provider] = n.Name
			} else {
				names[c.Provider] = c.Provider
			}
		}
		u := byConn[c.ID]
		out = append(out, map[string]any{"id": c.ID, "provider": c.Provider, "providerName": names[c.Provider], "name": c.Name, "authType": c.AuthType,
			"isActive": c.IsActive, "testStatus": c.Data["testStatus"], "priority": c.Priority, "lastUsed": u.LastUsed,
			"requests": map[string]int64{"today": u.Requests[0], "week": u.Requests[1], "month": u.Requests[2]},
			"tokens":   map[string]int64{"today": u.Tokens[0], "week": u.Tokens[1], "month": u.Tokens[2]}})
	}
	return out
}

func (a *API) createQuota(w http.ResponseWriter, r *http.Request) {
	var in quotaInput
	if !readJSON(w, r, &in) {
		return
	}
	l, msg := a.validateQuota(in)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	saved, err := a.Store.SaveQuotaLimit(l)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"limit": a.quotaView(*saved, time.Now())})
}

func (a *API) updateQuota(w http.ResponseWriter, r *http.Request) {
	var in quotaInput
	if !readJSON(w, r, &in) {
		return
	}
	l, msg := a.validateQuota(in)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	l.ID = chi.URLParam(r, "id")
	saved, err := a.Store.SaveQuotaLimit(l)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"limit": a.quotaView(*saved, time.Now())})
}

func (a *API) deleteQuota(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteQuotaLimit(chi.URLParam(r, "id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
