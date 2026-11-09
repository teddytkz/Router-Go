package gateway

import (
	"strconv"
	"time"

	"router-go/internal/consolelog"
	"router-go/internal/db"
)

// Quota enforcement: limits saved with Enforce=true (Quota page) stop traffic to a provider, or to one key, once its
// budget for the current window is used up. The request is not failed outright: the next key or combo member serves it.

type enforced struct {
	limit db.QuotaLimit
	state db.QuotaState
}

// exhausted returns the enforced limits of the given scope and target that are used up right now.
func (g *Gateway) exhausted(scope, target string) []enforced {
	limits, err := g.Store.QuotaLimits()
	if err != nil {
		return nil
	}
	var out []enforced
	for _, l := range limits {
		if !l.Enforce || l.Scope != scope || l.Target != target {
			continue
		}
		if st, err := g.Store.QuotaState(l, time.Now()); err == nil && st.Exhausted {
			out = append(out, enforced{l, st})
		}
	}
	return out
}

func describe(e enforced) string {
	unit := map[string]string{"requests": "requests", "tokens": "tokens"}[e.limit.Metric]
	return e.limit.Name + ": " + strconv.FormatInt(e.state.Used, 10) + "/" + strconv.FormatInt(e.limit.Limit, 10) + " " + unit + " per " + e.limit.Window
}

// providerQuota: the provider as a whole is over an enforced limit.
func (g *Gateway) providerQuota(node *db.Node) *failure {
	hits := g.exhausted("provider", node.ID)
	if len(hits) == 0 {
		return nil
	}
	reset := hits[0].state.ResetAt
	for _, h := range hits[1:] {
		if h.state.ResetAt.After(reset) {
			reset = h.state.ResetAt
		}
	}
	consolelog.Logf("WARN", "QUOTA", "%s is over its quota (%s): skipped until %s", node.Name, describe(hits[0]), reset.Format("15:04"))
	return &failure{status: 429, retry: reset, msg: "Quota used up for '" + node.Name + "' (" + describe(hits[0]) + ")."}
}

// connectionQuota: this key is over an enforced limit; reset is when it frees up again.
func (g *Gateway) connectionQuota(node *db.Node, c *db.Connection) (reset time.Time, msg string, hit bool) {
	hits := g.exhausted("connection", c.ID)
	if len(hits) == 0 {
		return time.Time{}, "", false
	}
	reset = hits[0].state.ResetAt
	for _, h := range hits[1:] {
		if h.state.ResetAt.After(reset) {
			reset = h.state.ResetAt
		}
	}
	consolelog.Logf("WARN", "QUOTA", "%s/%s is over its quota (%s): skipped until %s", node.Name, c.Name, describe(hits[0]), reset.Format("15:04"))
	return reset, "Quota used up for '" + node.Name + "/" + c.Name + "' (" + describe(hits[0]) + ").", true
}
