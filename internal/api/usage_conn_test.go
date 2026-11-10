package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"router-go/internal/db"
	"router-go/internal/provider"
)

// usageEnv serves a fake provider usage API at the provider's overridden base URL; hits counts its calls.
func usageEnv(t *testing.T, id, basePath string, h http.HandlerFunc) (e *env, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/refreshToken" {
			hits.Add(1)
		}
		h(w, r)
	}))
	t.Cleanup(mock.Close)
	t.Cleanup(provider.Override(id, mock.URL+basePath))
	t.Cleanup(provider.OverrideOAuth(id, mock.URL))
	e = newEnv(t, func(http.ResponseWriter, *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	return e, hits
}

func (e *env) providerConn(id, key string) *db.Connection {
	e.t.Helper()
	node, err := e.store.Node(id)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := e.store.CreateConnection(node, id, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) oauthConn(id string, tk provider.Tokens) *db.Connection {
	e.t.Helper()
	node, _ := e.store.Node(id)
	c, err := e.store.SaveOAuthConnection(node, tk)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// usage calls GET /api/usage/{id}; raw is the body as sent, out the decoded object.
func (e *env) usage(id, query string) (status int, raw string, out map[string]any) {
	e.t.Helper()
	resp, err := http.Get(e.srv.URL + "/api/usage/" + id + query)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &out)
	return resp.StatusCode, string(b), out
}

func quota(t *testing.T, out map[string]any, name string) map[string]any {
	t.Helper()
	q, _ := out["quotas"].(map[string]any)[name].(map[string]any)
	if q == nil {
		t.Fatalf("no quota %q in %v", name, out)
	}
	return q
}

const kiroLimits = `{"subscriptionInfo":{"subscriptionTitle":"KIRO POWER"},"nextDateReset":1893456000,
 "usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST","currentUsageWithPrecision":12.5,"usageLimitWithPrecision":1000,
  "freeTrialInfo":{"currentUsageWithPrecision":3,"usageLimitWithPrecision":50,"freeTrialExpiry":"2030-01-01T00:00:00Z"}}]}`

func TestKiroUsageParsesAndCaches(t *testing.T) {
	var ua, tokenType string
	e, hits := usageEnv(t, "kiro", "", func(w http.ResponseWriter, r *http.Request) {
		ua, tokenType = r.Header.Get("User-Agent"), r.Header.Get("tokentype")
		if r.Header.Get("Authorization") != "Bearer secret-1" || r.URL.Path != "/getUsageLimits" {
			w.WriteHeader(403)
			return
		}
		io.WriteString(w, kiroLimits)
	})
	c := e.oauthConn("kiro", provider.Tokens{AccessToken: "secret-1", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour), Extra: map[string]any{"authMethod": "google"}})

	status, raw, out := e.usage(c.ID, "")
	if status != 200 || out["plan"] != "KIRO POWER" {
		t.Fatalf("status %d %s", status, raw)
	}
	q := quota(t, out, "agentic_request")
	if q["used"] != 12.5 || q["total"] != 1000.0 || q["remaining"] != 987.5 || q["resetAt"] != "2030-01-01T00:00:00.000Z" || q["unlimited"] != nil {
		t.Errorf("quota = %v", q)
	}
	if f := quota(t, out, "agentic_request_freetrial"); f["used"] != 3.0 || f["remaining"] != 47.0 || f["resetAt"] != "2030-01-01T00:00:00.000Z" {
		t.Errorf("free trial = %v", f)
	}
	if strings.Contains(raw, "secret-1") {
		t.Error("the token leaked into the response")
	}
	if !strings.Contains(ua, "kiro-ide") || strings.Contains(ua, "KiroIDE") || tokenType != "" {
		t.Errorf("headers: user-agent %q tokentype %q", ua, tokenType)
	}

	e.usage(c.ID, "")
	if hits.Load() != 1 {
		t.Errorf("second call was not served from the cache (%d upstream calls)", hits.Load())
	}
	e.usage(c.ID, "?force=1")
	if hits.Load() != 2 {
		t.Errorf("force=1 did not skip the cache (%d upstream calls)", hits.Load())
	}
	// a changed key never sees the old answer
	e.store.UpdateTokens(c.ID, provider.Tokens{AccessToken: "other", ExpiresAt: time.Now().Add(time.Hour)})
	status, _, out = e.usage(c.ID, "")
	if hits.Load() <= 2 || status != 200 || out["message"] == nil { // refetched (the fake refuses the new key on all 3 endpoints)
		t.Errorf("new key: hits=%d out=%v", hits.Load(), out)
	}
}

func TestKiroUsageFallbackEndpointsAndAuthMessages(t *testing.T) {
	var methods []string
	var target, tokenType, arn string
	e, hits := usageEnv(t, "kiro", "", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method != "POST" {
			w.WriteHeader(403)
			return
		}
		target, tokenType, arn = r.Header.Get("X-Amz-Target"), r.Header.Get("tokentype"), ""
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		arn, _ = body["profileArn"].(string)
		io.WriteString(w, kiroLimits)
	})
	c := e.providerConn("kiro", "ksk-key")
	e.store.PatchConnection(c.ID, nil, map[string]any{"providerSpecificData": map[string]any{"authMethod": "api_key"}})
	_, _, out := e.usage(c.ID, "")
	if strings.Join(methods, ",") != "GET,POST" || quota(t, out, "agentic_request")["used"] != 12.5 {
		t.Fatalf("methods %v out %v", methods, out)
	}
	if target != "AmazonCodeWhispererService.GetUsageLimits" || tokenType != "API_KEY" || arn != "" {
		t.Errorf("post call: target=%q tokentype=%q profileArn=%q", target, tokenType, arn)
	}

	// every endpoint refuses the token: the message depends on how the account signed in
	for method, want := range map[string]string{"idc": "IAM Identity Center", "github": "authentication expired", "builder-id": "rejected the current token"} {
		e, hits = usageEnv(t, "kiro", "", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
		c := e.oauthConn("kiro", provider.Tokens{AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour), Extra: map[string]any{"authMethod": method}})
		status, _, out := e.usage(c.ID, "")
		if msg, _ := out["message"].(string); status != 200 || !strings.Contains(msg, want) || len(out["quotas"].(map[string]any)) != 0 || hits.Load() != 3 {
			t.Errorf("%s: status %d out %v hits %d", method, status, out, hits.Load())
		}
		e.usage(c.ID, "") // an answer without quotas is not cached
		if hits.Load() != 6 {
			t.Errorf("%s: message was cached (%d)", method, hits.Load())
		}
	}
}

func TestKiroUsageRefreshesExpiredToken(t *testing.T) {
	var used string
	e, _ := usageEnv(t, "kiro", "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refreshToken" {
			io.WriteString(w, `{"accessToken":"renewed","refreshToken":"rt-2","expiresIn":3600}`)
			return
		}
		used = r.Header.Get("Authorization")
		io.WriteString(w, kiroLimits)
	})
	c := e.oauthConn("kiro", provider.Tokens{AccessToken: "old", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Minute), Extra: map[string]any{"authMethod": "google"}})
	if _, raw, _ := e.usage(c.ID, ""); !strings.Contains(raw, "KIRO POWER") || used != "Bearer renewed" {
		t.Errorf("used %q, body %s", used, raw)
	}
	// a sign-in that cannot be renewed is a message, not a 5xx
	e.store.UpdateTokens(c.ID, provider.Tokens{AccessToken: "old", ExpiresAt: time.Now().Add(-time.Minute)})
	e.store.PatchConnection(c.ID, nil, map[string]any{"refreshToken": ""})
	if status, _, out := e.usage(c.ID, ""); status != 200 || !strings.Contains(out["message"].(string), "sign in again") {
		t.Errorf("status %d out %v", status, out)
	}
}

// Freebuff is driven through the vendor's own CLI, not an HTTP API, so it has no quota endpoint this port may call:
// the session endpoint the CLI itself uses refuses a third-party client and bills strikes. The dashboard must say so
// rather than invent a quota.
func TestFreebuffReportsNoQuotaApi(t *testing.T) {
	e := newEnv(t, func(http.ResponseWriter, *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c := e.providerConn("freebuff", "fb-token")
	status, _, out := e.usage(c.ID, "")
	if status != 200 || out["message"] != "Usage API not implemented for freebuff" || len(out["quotas"].(map[string]any)) != 0 {
		t.Errorf("status %d out %v", status, out)
	}
}

func TestCodeBuddyUsagePacks(t *testing.T) {
	const day = 24 * 3600 * 1000
	var method string
	var static http.Header
	e, _ := usageEnv(t, "codebuddy-intl", "/v2", func(w http.ResponseWriter, r *http.Request) {
		method, static = r.Method, r.Header.Clone()
		if r.URL.Path != "/v2/billing/meter/get-user-resource" || r.Header.Get("Authorization") != "Bearer cb-key" {
			w.WriteHeader(401)
			return
		}
		now := time.Now().UnixMilli()
		resp := map[string]any{"code": 0, "data": map[string]any{"Response": map[string]any{"Data": map[string]any{"Accounts": []any{
			// refill: cycle ends in 10 days, package in 100; ~30-day cycle = "Monthly"
			map[string]any{"PackageName": "Free Plan", "CycleStartTime": now - 20*day, "CycleEndTime": now + 10*day, "DeductionEndTime": now + 100*day,
				"CycleCapacityUsedPrecise": "6.5", "CycleCapacitySizePrecise": "500"},
			// bonus: one cycle only, ends with the package
			map[string]any{"PackageName": "Promo", "CycleEndTime": now + 5*day, "DeductionEndTime": now + 5*day, "CapacityUsed": 1, "CapacitySize": 20},
		}}}}}
		json.NewEncoder(w).Encode(resp)
	})
	c := e.providerConn("codebuddy-intl", "cb-key")
	_, raw, out := e.usage(c.ID, "")
	if method != "POST" || out["plan"] != "Free Plan" {
		t.Fatalf("method %s out %s", method, raw)
	}
	m := quota(t, out, "Monthly")
	if m["used"] != 6.5 || m["total"] != 500.0 || m["recurring"] != true || m["resetAt"] == nil {
		t.Errorf("refill = %v", m)
	}
	b := quota(t, out, "Bonus Pack 1")
	if b["used"] != 1.0 || b["total"] != 20.0 || b["recurring"] != false {
		t.Errorf("bonus = %v", b)
	}
	if static.Get("X-Product") != "SaaS" || static.Get("x-codebuddy-request") != "1" || static.Get("X-Request-Id") != "" {
		t.Errorf("headers = %v", static)
	}

	// credential refused / api error code / empty
	for body, want := range map[string]string{`{"code":7,"msg":"boom"}`: "quota error: boom", `{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`: "No credit package"} {
		e, _ = usageEnv(t, "codebuddy-intl", "/v2", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, _, out := e.usage(e.providerConn("codebuddy-intl", "k").ID, ""); !strings.Contains(out["message"].(string), want) {
			t.Errorf("%s: %v", body, out)
		}
	}
	e, _ = usageEnv(t, "codebuddy-intl", "/v2", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	if _, _, out := e.usage(e.providerConn("codebuddy-intl", "k").ID, ""); !strings.Contains(out["message"].(string), "invalid or expired") {
		t.Errorf("403: %v", out)
	}
}

func TestUsageNotImplementedAndErrors(t *testing.T) {
	e, _ := usageEnv(t, "kiro", "", func(w http.ResponseWriter, r *http.Request) {})
	for _, id := range []string{"cline", "opencode", "qoder"} {
		_, _, out := e.usage(e.providerConn(id, "k").ID, "")
		if out["message"] != "Usage API not implemented for "+id || len(out["quotas"].(map[string]any)) != 0 {
			t.Errorf("%s: %v", id, out)
		}
	}
	custom := e.addKey("k", "sk") // a custom node
	if _, _, out := e.usage(custom.ID, ""); !strings.HasPrefix(out["message"].(string), "Usage API not implemented for ") {
		t.Errorf("custom node: %v", out)
	}
	if status, _, _ := e.usage("nope", ""); status != 404 {
		t.Errorf("unknown connection: %d", status)
	}
	// the route is behind the dashboard login
	e.store.UpdateSettings(map[string]any{"requireLogin": true})
	if status, _, _ := e.usage(custom.ID, ""); status != 401 {
		t.Errorf("without login: %d", status)
	}
}

func TestBuiltinViewFlagsLiveQuotaProviders(t *testing.T) {
	e := newEnv(t, func(http.ResponseWriter, *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	_, out := e.json("GET", "/api/builtin-providers", "")
	got := map[string]bool{}
	for _, p := range out["providers"].([]any) {
		if p := p.(map[string]any); p["usage"] == true {
			got[p["id"].(string)] = true
		}
	}
	if len(got) != 2 || !got["kiro"] || !got["codebuddy-intl"] {
		t.Errorf("providers with live quotas = %v", got)
	}
}

func TestFreeTiers(t *testing.T) {
	e := newEnv(t, func(http.ResponseWriter, *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	_, out := e.json("GET", "/api/free-tiers", "")
	rows, _ := out["tiers"].([]any)
	kiro, _ := provider.ByID("kiro")
	oc, _ := provider.ByID("opencode")
	fb, _ := provider.ByID("freebuff")
	if want := 2 + len(kiro.Models) + len(oc.Models) + 1 + len(fb.Models); len(rows) != want {
		t.Fatalf("%d rows, want %d", len(rows), want)
	}
	row := func(i int) map[string]any { return rows[i].(map[string]any) }
	// the two daily providers lead, by provider name ("Freebuff" before "OpenCode Free"); a provider row carries the
	// cadence and no model, then its own models follow; unknown cadences sort after every known one
	if r := row(0); r["providerId"] != "freebuff" || r["freeRefresh"] != "daily" || r["modelId"] != nil || r["modelName"] != nil {
		t.Errorf("first row = %v", r)
	}
	if r := row(1); r["providerId"] != "freebuff" || r["freeRefresh"] != "daily" || r["modelId"] == nil || r["modelName"] == nil {
		t.Errorf("second row = %v", r)
	}
	if r := row(1 + len(fb.Models)); r["providerId"] != "opencode" || r["freeRefresh"] != "daily" || r["modelId"] != nil {
		t.Errorf("opencode provider row = %v", r)
	}
	// an unknown cadence sorts after every known one; the provider row comes before its models
	found := false
	for i := range rows {
		if r := row(i); r["providerId"] == "kiro" && r["modelId"] == nil {
			found = true
			if r["freeRefresh"] != nil || r["modelName"] != nil {
				t.Errorf("kiro provider row = %v", r)
			}
		}
	}
	if !found {
		t.Error("no kiro provider row")
	}
	seen := map[any]bool{}
	for i := range rows {
		seen[row(i)["providerId"]] = true
	}
	if len(seen) != 3 || !seen["opencode"] || !seen["freebuff"] { // paid providers (cline, codebuddy-intl, search) are no free tiers
		t.Errorf("providers = %v", seen)
	}
}
