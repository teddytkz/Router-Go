// Package api exposes the dashboard JSON API under /api and assembles the full HTTP handler.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"router-go/internal/auth"
	"router-go/internal/config"
	"router-go/internal/consolelog"
	"router-go/internal/db"
	"router-go/internal/gateway"
	"router-go/internal/modelcaps"
	"router-go/internal/provider"
	_ "router-go/internal/provider/builtin" // registers the built-in providers
	"router-go/internal/sysstat"
)

type API struct {
	Cfg     *config.Config
	Store   *db.Store
	GW      *gateway.Gateway
	Session *auth.Session
	Limiter *auth.Limiter
	usages  sync.Map // live quota answers by connection id (see usageTTL)
	tests   sync.Map // recent test results by what was tested: a double click must not become two upstream calls
}

// testThrottle: the same key/model is not sent to the upstream again within this window; the last answer is returned.
// Providers punish repeated rejected calls (CodeBuddy banned an account for it), so tests are rationed.
const testThrottle = 10 * time.Second

type testEntry struct {
	at   time.Time
	res  gateway.ValidateResult
	took time.Duration
	out  map[string]any
}

func (a *API) recentTest(key string) (testEntry, bool) {
	if v, ok := a.tests.Load(key); ok {
		if e := v.(testEntry); time.Since(e.at) < testThrottle {
			return e, true
		}
	}
	return testEntry{}, false
}

// Handler builds the full router: /api (dashboard), /v1 (gateway) and the SPA for everything else.
func Handler(cfg *config.Config, store *db.Store, spa http.Handler) http.Handler {
	a := &API{Cfg: cfg, Store: store, GW: gateway.New(store), Session: auth.NewSession(cfg.JWTSecret), Limiter: auth.NewLimiter()}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, securityHeaders)

	r.Group(func(r chi.Router) {
		r.Use(cors)
		a.GW.Mount(r)
		r.Options("/v1/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		r.Options("/v1beta/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"status": "ok"}) })
		r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"version": config.Version})
		})
		r.Get("/auth/status", a.authStatus)
		r.Post("/auth/login", a.login)
		r.Post("/auth/logout", a.logout)

		r.Group(func(r chi.Router) {
			r.Use(a.requireSession)
			r.Get("/settings", a.getSettings)
			r.Patch("/settings", a.patchSettings)

			r.Get("/keys", a.listKeys)
			r.Post("/keys", a.createKey)
			r.Patch("/keys/{id}", a.patchKey)
			r.Delete("/keys/{id}", a.deleteKey)

			r.Post("/oauth/{provider}/start", a.oauthStart)
			r.Post("/oauth/{provider}/exchange", a.oauthExchange)
			r.Post("/oauth/{provider}/poll", a.oauthPoll)
			r.Get("/builtin-providers", a.listBuiltin)
			r.Put("/builtin-providers/{id}/headers", a.setProviderHeaders)
			r.Delete("/builtin-providers/{id}/headers", a.resetProviderHeaders)
			r.Post("/builtin-providers/{id}/models", a.bulkBuiltinModels)
			r.Get("/provider-config", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"configs": a.Store.ProviderConfigs()})
			})
			r.Put("/provider-config/{id}", a.setProviderConfig)
			r.Get("/provider-nodes", a.listNodes)
			r.Post("/provider-nodes", a.createNode)
			r.Post("/provider-nodes/validate", a.validateNode)
			r.Patch("/provider-nodes/{id}", a.patchNode)
			r.Put("/provider-nodes/{id}", a.patchNode)
			r.Delete("/provider-nodes/{id}", a.deleteNode)
			r.Get("/provider-nodes/{id}/models", a.nodeModels)

			r.Get("/providers", a.listConnections)
			r.Post("/providers", a.createConnection)
			r.Patch("/providers/{id}", a.patchConnection)
			r.Put("/providers/{id}", a.patchConnection)
			r.Delete("/providers/{id}", a.deleteConnection)
			r.Post("/providers/test-batch", a.testBatch)
			r.Post("/providers/bulk-proxy", a.bulkProxy)
			r.Post("/providers/{id}/test", a.testConnection)
			r.Get("/providers/{id}/models", a.connectionModels)

			r.Get("/combos", a.listCombos)
			r.Post("/combos", a.createCombo)
			r.Put("/combos/{id}", a.updateCombo)
			r.Patch("/combos/{id}", a.updateCombo)
			r.Delete("/combos/{id}", a.deleteCombo)

			r.Get("/models/alias", a.listAliases)
			r.Put("/models/alias", a.setAlias)
			r.Delete("/models/alias", a.deleteAlias)
			r.Get("/models/custom", a.listCustomModels)
			r.Post("/models/custom", a.addCustomModel)
			r.Delete("/models/custom", a.deleteCustomModel)
			r.Patch("/models/custom", a.setModelCaps)

			r.Get("/usage/stats", a.usageStats)
			r.Get("/usage/history", a.usageHistory)
			r.Get("/usage/summary", a.usageSummary)
			r.Get("/usage/{connectionId}", a.connectionUsage)
			r.Get("/free-tiers", a.freeTiers)
			r.Get("/system", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, sysstat.Default.Snapshot()) })
			r.Get("/health/routing", a.routingHealth)
			r.Get("/console-logs", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"entries": consolelog.Default.Snapshot()})
			})
			r.Get("/console-logs/stream", a.consoleStream)
			r.Delete("/console-logs", func(w http.ResponseWriter, _ *http.Request) {
				consolelog.Default.Clear()
				writeJSON(w, 200, map[string]any{"success": true})
			})
			r.Get("/proxy-pools", a.listProxyPools)
			r.Post("/proxy-pools", a.createProxyPool)
			r.Post("/proxy-pools/vercel-deploy", a.vercelDeploy)
			r.Post("/proxy-pools/cloudflare-deploy", a.cloudflareDeploy)
			r.Put("/proxy-pools/{id}", a.updateProxyPool)
			r.Patch("/proxy-pools/{id}", a.updateProxyPool)
			r.Delete("/proxy-pools/{id}", a.deleteProxyPool)
			r.Post("/proxy-pools/{id}/test", a.testProxyPool)
			r.Get("/quota", a.listQuota)
			r.Post("/quota", a.createQuota)
			r.Put("/quota/{id}", a.updateQuota)
			r.Delete("/quota/{id}", a.deleteQuota)
			r.Get("/usage/active", a.usageActive)
			r.Post("/models/test", a.testModel)
			r.Post("/usage/reset", a.usageReset)
		})
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeErr(w, 404, "not found") })
	})

	r.NotFound(spa.ServeHTTP)
	return r
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// cors lets browser SDKs call /v1 with a bearer key. Cookies are never used on /v1, so "*" is safe.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, x-goog-api-key")
		next.ServeHTTP(w, r)
	})
}

func (a *API) loginRequired() bool {
	s, err := a.Store.Settings()
	return err != nil || s["requireLogin"] != false
}

func (a *API) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.loginRequired() && !a.Session.FromRequest(r) {
			writeErr(w, 401, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "invalid JSON body")
		return false
	}
	return true
}

func dbErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, db.ErrConflict):
		writeErr(w, 409, "already exists")
	default:
		writeErr(w, 500, "internal error")
	}
}

func queryInt(r *http.Request, key string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

var prefixRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// ---- auth ----

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	s, _ := a.Store.Settings()
	_, hasPW := s["password"].(string)
	req := a.loginRequired()
	writeJSON(w, 200, map[string]any{
		"requireLogin": req, "authMode": "password", "hasPassword": hasPW,
		"authenticated": !req || a.Session.FromRequest(r),
	})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, a.Cfg.TrustProxy)
	if secs := a.Limiter.Locked(ip); secs > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, 429, map[string]any{"error": "Too many failed attempts. Try again in " + strconv.Itoa(secs) + "s.", "retryAfter": secs})
		return
	}
	var body struct{ Password string }
	if !readJSON(w, r, &body) {
		return
	}
	s, err := a.Store.Settings()
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	hash, hasHash := s["password"].(string)
	var ok bool
	if hasHash && hash != "" {
		ok = auth.CheckPassword(hash, body.Password)
	} else {
		ok = subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.Cfg.InitialPassword)) == 1
	}
	if !ok {
		left := a.Limiter.Fail(ip)
		if secs := a.Limiter.Locked(ip); secs > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeJSON(w, 429, map[string]any{"error": "Too many failed attempts. Try again in " + strconv.Itoa(secs) + "s.", "retryAfter": secs})
			return
		}
		writeJSON(w, 401, map[string]any{"error": "Invalid password. " + strconv.Itoa(left) + " attempt(s) left before lockout.", "remainingBeforeLock": left})
		return
	}
	// A public default password must never mint a remote session (it would let anyone disable auth).
	if (!hasHash || hash == "") && auth.IsPlaceholder(a.Cfg.InitialPassword) && !auth.IsLocal(r) {
		writeJSON(w, 403, map[string]any{"success": false, "mustChangePassword": true,
			"error": "Default password must be changed before remote access. Log in from the local machine or set INITIAL_PASSWORD."})
		return
	}
	a.Limiter.Success(ip)
	secure := a.Cfg.SecureCookie || r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	if err := a.Session.SetCookie(w, secure); err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) logout(w http.ResponseWriter, _ *http.Request) {
	auth.ClearCookie(w)
	writeJSON(w, 200, map[string]any{"success": true})
}

// ---- settings ----

func (a *API) publicSettings() (map[string]any, error) {
	s, err := a.Store.Settings()
	if err != nil {
		return nil, err
	}
	_, hasPW := s["password"].(string)
	delete(s, "password")
	s["hasPassword"], s["dataDir"], s["version"], s["machineId"] = hasPW, a.Cfg.DataDir, config.Version, a.Cfg.MachineID
	s["activePort"], s["portSource"] = a.Cfg.PortInfo()
	s["defaultPort"] = config.DefaultPort
	return s, nil
}

func (a *API) getSettings(w http.ResponseWriter, _ *http.Request) {
	s, err := a.publicSettings()
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, s)
}

func (a *API) patchSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequireLogin    *bool `json:"requireLogin"`
		RequireAPIKey   *bool `json:"requireApiKey"`
		StickyLimit     *int  `json:"comboStickyRoundRobinLimit"`
		Port            *int  `json:"port"`
		CapacityAdapter map[string]struct {
			Enabled    bool     `json:"enabled"`
			RoundRobin bool     `json:"roundRobin"`
			Models     []string `json:"models"`
		} `json:"capacityAdapter"`
		PonytailLevel   *string `json:"ponytailLevel"`
		PonytailEnabled *bool   `json:"ponytailEnabled"`
		Watermarks      *bool   `json:"watermarksRemoverEnabled"`
		LoopGuard       *bool   `json:"loopGuardEnabled"`
		CircuitBreaker  *bool   `json:"circuitBreakerEnabled"`
		Semaphore       *bool   `json:"semaphoreEnabled"`
		CustomEnabled   *bool   `json:"customPromptEnabled"`
		CustomPrompt    *string `json:"customPrompt"`
		CurrentPassword string  `json:"currentPassword"`
		NewPassword     *string `json:"newPassword"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	patch := map[string]any{}
	if in.RequireLogin != nil {
		patch["requireLogin"] = *in.RequireLogin
	}
	if in.RequireAPIKey != nil {
		patch["requireApiKey"] = *in.RequireAPIKey
	}
	for k, v := range map[string]*bool{"ponytailEnabled": in.PonytailEnabled, "watermarksRemoverEnabled": in.Watermarks, "loopGuardEnabled": in.LoopGuard,
		"circuitBreakerEnabled": in.CircuitBreaker, "semaphoreEnabled": in.Semaphore, "customPromptEnabled": in.CustomEnabled} {
		if v != nil {
			patch[k] = *v
		}
	}
	if in.Port != nil {
		if !config.ValidPort(*in.Port) {
			writeErr(w, 400, "port must be between 1 and 65535")
			return
		}
		active, source := a.Cfg.PortInfo()
		// --port / PORT win over the saved value: it is stored, and used on the next start without them
		if *in.Port != active && (source == config.PortFromSettings || source == config.PortFromDefault) && a.Cfg.Rebind != nil {
			if err := a.Cfg.Rebind(*in.Port); err != nil {
				writeErr(w, 409, fmt.Sprintf("cannot listen on port %d: %v", *in.Port, err))
				return
			}
			a.Cfg.SetPort(*in.Port, config.PortFromSettings)
		}
		patch["port"] = *in.Port
	}
	if in.CapacityAdapter != nil {
		norm := map[string]any{}
		for k, e := range in.CapacityAdapter {
			if k != modelcaps.Vision && k != modelcaps.Audio {
				writeErr(w, 400, "capacityAdapter supports 'vision' and 'audioInput' only")
				return
			}
			if len(e.Models) > 50 {
				writeErr(w, 400, "an adapter pool can hold at most 50 models")
				return
			}
			ms := db.NormalizeList(e.Models)
			for _, m := range ms {
				if len(m) > 256 {
					writeErr(w, 400, "model names must be at most 256 characters")
					return
				}
			}
			norm[k] = map[string]any{"enabled": e.Enabled, "roundRobin": e.RoundRobin, "models": ms}
		}
		patch["capacityAdapter"] = norm
	}
	if in.PonytailLevel != nil {
		if _, ok := map[string]bool{"lite": true, "full": true, "ultra": true}[*in.PonytailLevel]; !ok {
			writeErr(w, 400, "ponytailLevel must be lite, full or ultra")
			return
		}
		patch["ponytailLevel"] = *in.PonytailLevel
	}
	if in.CustomPrompt != nil {
		if len(*in.CustomPrompt) > 8000 {
			writeErr(w, 400, "customPrompt must be at most 8000 characters")
			return
		}
		patch["customPrompt"] = *in.CustomPrompt
	}
	if in.StickyLimit != nil {
		if *in.StickyLimit < 1 || *in.StickyLimit > 1000 {
			writeErr(w, 400, "comboStickyRoundRobinLimit must be between 1 and 1000")
			return
		}
		patch["comboStickyRoundRobinLimit"] = *in.StickyLimit
	}
	if in.NewPassword != nil {
		if len(*in.NewPassword) < 8 {
			writeErr(w, 400, "new password must be at least 8 characters")
			return
		}
		if !a.passwordOK(in.CurrentPassword) {
			writeErr(w, 403, "current password is incorrect")
			return
		}
		h, err := auth.HashPassword(*in.NewPassword)
		if err != nil {
			writeErr(w, 500, "internal error")
			return
		}
		patch["password"] = h
	}
	if err := a.Store.UpdateSettings(patch); err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	a.getSettings(w, r)
}

func (a *API) passwordOK(p string) bool {
	s, _ := a.Store.Settings()
	if h, ok := s["password"].(string); ok && h != "" {
		return auth.CheckPassword(h, p)
	}
	return subtle.ConstantTimeCompare([]byte(p), []byte(a.Cfg.InitialPassword)) == 1
}

// ---- API keys ----

func (a *API) listKeys(w http.ResponseWriter, _ *http.Request) {
	ks, err := a.Store.APIKeys()
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"keys": ks})
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string
		Access *db.Access
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	var acc db.Access
	if in.Access != nil {
		acc = *in.Access
	}
	if _, err := db.ValidateAccess(acc); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	k, err := a.Store.CreateAPIKey(name, a.Cfg.MachineID, auth.NewAPIKey(a.Cfg.APIKeySecret, a.Cfg.MachineID), acc)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"key": k})
}

func (a *API) patchKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IsActive *bool      `json:"isActive"`
		Access   *db.Access `json:"access"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.IsActive == nil && in.Access == nil {
		writeErr(w, 400, "isActive or access is required")
		return
	}
	id := chi.URLParam(r, "id")
	if in.Access != nil {
		if _, err := db.ValidateAccess(*in.Access); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := a.Store.SetAPIKeyAccess(id, *in.Access); err != nil {
			dbErr(w, err)
			return
		}
	}
	if in.IsActive != nil {
		if err := a.Store.SetAPIKeyActive(id, *in.IsActive); err != nil {
			dbErr(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteAPIKey(chi.URLParam(r, "id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// ---- provider nodes ----

func (a *API) listNodes(w http.ResponseWriter, _ *http.Request) {
	ns, err := a.Store.Nodes("")
	if err != nil {
		dbErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Flat())
	}
	writeJSON(w, 200, map[string]any{"nodes": out})
}

type nodeInput struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	APIType string `json:"apiType"`
	BaseURL string `json:"baseUrl"`
}

func (a *API) createNode(w http.ResponseWriter, r *http.Request) {
	var in nodeInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Name, in.Prefix = strings.TrimSpace(in.Name), strings.TrimSpace(in.Prefix)
	if in.Name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	if !prefixRe.MatchString(in.Prefix) {
		writeErr(w, 400, "prefix must be 1-32 chars: letters, digits, '-' or '_'")
		return
	}
	switch in.Type {
	case "", db.NodeTypeOpenAI:
		in.Type = db.NodeTypeOpenAI
		if in.APIType != "chat" && in.APIType != "responses" {
			writeErr(w, 400, "apiType must be 'chat' or 'responses'")
			return
		}
	case db.NodeTypeAnthropic:
		in.APIType = ""
	default:
		writeErr(w, 400, "type must be 'openai-compatible' or 'anthropic-compatible'")
		return
	}
	if in.BaseURL == "" {
		in.BaseURL = "https://api.openai.com/v1"
		if in.Type == db.NodeTypeAnthropic {
			in.BaseURL = "https://api.anthropic.com/v1"
		}
	}
	base, err := gateway.ValidateBaseURL(in.BaseURL)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	n, err := a.Store.CreateNode(in.Type, in.Name, in.Prefix, in.APIType, base)
	if errors.Is(err, db.ErrConflict) {
		writeErr(w, 409, "prefix already in use")
		return
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"node": n.Flat()})
}

func (a *API) patchNode(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if !readJSON(w, r, &in) {
		return
	}
	patch := map[string]any{}
	if v, ok := in["name"].(string); ok && strings.TrimSpace(v) != "" {
		patch["name"] = strings.TrimSpace(v)
	}
	if v, ok := in["prefix"].(string); ok {
		if !prefixRe.MatchString(v) {
			writeErr(w, 400, "invalid prefix")
			return
		}
		patch["prefix"] = v
	}
	if v, ok := in["baseUrl"].(string); ok {
		base, err := gateway.ValidateBaseURL(v)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		patch["baseUrl"] = base
	}
	n, err := a.Store.UpdateNode(chi.URLParam(r, "id"), patch)
	if errors.Is(err, db.ErrConflict) {
		writeErr(w, 409, "prefix already in use")
		return
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	a.GW.InvalidateModels(n.ID)
	writeJSON(w, 200, map[string]any{"node": n.Flat()})
}

func (a *API) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.Store.DeleteNode(id); err != nil {
		dbErr(w, err)
		return
	}
	a.GW.InvalidateModels(id)
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) validateNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type    string `json:"type"`
		BaseURL string `json:"baseUrl"`
		APIType string `json:"apiType"`
		APIKey  string `json:"apiKey"`
		ModelID string `json:"modelId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.BaseURL == "" {
		writeErr(w, 400, "baseUrl is required")
		return
	}
	writeJSON(w, 200, a.GW.Validate(r.Context(), in.Type, in.BaseURL, in.APIType, in.APIKey, in.ModelID))
}

func (a *API) nodeModels(w http.ResponseWriter, r *http.Request) {
	n, err := a.Store.Node(chi.URLParam(r, "id"))
	if err != nil {
		dbErr(w, err)
		return
	}
	if _, builtin := n.Builtin(); builtin { // a built-in provider's catalogue is fixed: no upstream list to pick from
		writeJSON(w, 200, map[string]any{"models": a.GW.AddedModels(n), "upstream": []string{}, "upstreamCaps": map[string]any{}})
		return
	}
	// models: what clients may use (added to this provider); upstream: what the provider's own /models reports
	up, upErr := a.GW.NodeModels(r.Context(), n, r.URL.Query().Get("refresh") == "1")
	if up == nil {
		up = []string{}
	}
	upCaps := make(map[string]modelcaps.Caps, len(up))
	for _, id := range up {
		upCaps[id] = modelcaps.Detect(id)
	}
	out := map[string]any{"models": a.GW.AddedModels(n), "upstream": up, "upstreamCaps": upCaps}
	if upErr != nil {
		out["error"] = upErr.Error()
	}
	writeJSON(w, 200, out)
}

// ---- connections (upstream key pool) ----

func (a *API) listConnections(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Store.Connections(r.URL.Query().Get("provider"), false)
	if err != nil {
		dbErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Flat())
	}
	writeJSON(w, 200, map[string]any{"connections": out})
}

func (a *API) createConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		APIKey   string `json:"apiKey"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	node, err := a.Store.Node(in.Provider)
	if err != nil {
		writeErr(w, 404, "provider node not found")
		return
	}
	if p, ok := node.Builtin(); ok {
		if !p.Routable() {
			writeErr(w, 400, p.Name+" cannot be used yet: "+p.Note)
			return
		}
		if !p.NoAuth && strings.TrimSpace(in.APIKey) == "" {
			writeErr(w, 400, p.Name+" needs an API key or token")
			return
		}
		if p.OAuth != nil && p.OAuth.Import != nil { // what was pasted is a sign-in to import (a refresh token…), not a key
			t, err := p.OAuth.Import(r.Context(), a.GW.OAuthClient(node), in.APIKey)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			if t.Email == "" && strings.TrimSpace(in.Name) != "" {
				t.Name = strings.TrimSpace(in.Name)
			}
			a.finishOAuth(w, p, node, t)
			return
		}
	}
	a.enableCatalogueIfFirst(node)
	in.Name, in.APIKey = strings.TrimSpace(in.Name), strings.TrimSpace(in.APIKey)
	if in.Name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	c, err := a.Store.CreateConnection(node, in.Name, in.APIKey)
	if errors.Is(err, db.ErrConflict) {
		writeErr(w, 409, "a connection with this name already exists")
		return
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	a.GW.InvalidateModels(node.ID)
	writeJSON(w, 201, map[string]any{"connection": c.Flat()})
}

func (a *API) patchConnection(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if !readJSON(w, r, &in) {
		return
	}
	cols, data := map[string]any{}, map[string]any{}
	for _, k := range []string{"name", "priority", "isActive"} {
		if v, ok := in[k]; ok {
			cols[k] = v
		}
	}
	if v, ok := in["apiKey"].(string); ok && strings.TrimSpace(v) != "" {
		data["apiKey"] = strings.TrimSpace(v)
		data["testStatus"] = "unknown"
	}
	if v, ok := in["defaultModel"].(string); ok {
		data["defaultModel"] = v
	}
	if v, ok := in["proxyPoolId"]; ok { // "" clears it, "__none__" opts out explicitly
		id, _ := v.(string)
		if id != "" && id != db.PoolNone {
			if _, err := a.Store.ProxyPool(id); err != nil {
				writeJSON(w, 400, map[string]any{"error": "proxy pool not found"})
				return
			}
		}
		if err := a.Store.SetConnectionProxy(chi.URLParam(r, "id"), id); err != nil {
			dbErr(w, err)
			return
		}
	}
	c, err := a.Store.PatchConnection(chi.URLParam(r, "id"), cols, data)
	if err != nil {
		dbErr(w, err)
		return
	}
	a.GW.InvalidateModels(c.Provider)
	writeJSON(w, 200, map[string]any{"connection": c.Flat()})
}

func (a *API) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteConnection(chi.URLParam(r, "id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// testConnection validates the stored key and records the outcome (a success also lifts any cooldown).
// runConnectionTest checks one key (GET /models, or a one-token probe when modelID is set), records the outcome on the
// connection and reports how long it took.
func (a *API) runConnectionTest(ctx context.Context, c *db.Connection, node *db.Node, modelID string) (gateway.ValidateResult, time.Duration) {
	if modelID == "" {
		modelID, _ = c.Data["defaultModel"].(string)
	}
	psd, _ := c.Data["providerSpecificData"].(map[string]any)
	cacheKey := fmt.Sprint(c.ID, "|", modelID, "|", c.APIKey(), "|", psd["proxyPoolId"])
	if e, ok := a.recentTest(cacheKey); ok {
		return e.res, e.took
	}
	start := time.Now()
	ctx = gateway.ConnCtx(ctx, c) // test through the connection's proxy pool, like real traffic
	var res gateway.ValidateResult
	if node.Type == db.NodeTypeBuiltin {
		key, kerr := a.GW.Secret(ctx, node, c)
		if kerr != nil {
			res = gateway.ValidateResult{Error: kerr.Error()}
		} else {
			res = a.GW.ValidateBuiltin(ctx, node, key)
		}
	} else {
		res = a.GW.Validate(ctx, node.Type, node.BaseURL(), node.APIType(), c.APIKey(), modelID)
	}
	took := time.Since(start)
	if res.Valid {
		a.Store.MarkConnection(c.ID, 0, "", 0, 0)
	} else {
		_, _ = a.Store.PatchConnection(c.ID, nil, map[string]any{
			"testStatus": "error", "lastError": res.Error, "lastErrorAt": time.Now().UTC().Format(time.RFC3339Nano)})
	}
	a.GW.InvalidateModels(node.ID)
	a.tests.Store(cacheKey, testEntry{at: time.Now(), res: res, took: took})
	return res, took
}

func (a *API) testConnection(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.Connection(chi.URLParam(r, "id"))
	if err != nil {
		dbErr(w, err)
		return
	}
	node, err := a.Store.Node(c.Provider)
	if err != nil {
		dbErr(w, err)
		return
	}
	var in struct {
		ModelID string `json:"modelId"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	res, _ := a.runConnectionTest(r.Context(), c, node, in.ModelID)
	writeJSON(w, 200, res)
}

// testBatch tests every enabled key four at a time and returns a per-key log. providerId limits it to one provider,
// providers to several (e.g. one section of the list); with neither, every provider is tested.
func (a *API) testBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProviderID string   `json:"providerId"`
		Providers  []string `json:"providers"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	if in.ProviderID != "" {
		in.Providers = append(in.Providers, in.ProviderID)
	}
	nodes, err := a.Store.Nodes("")
	if err != nil {
		dbErr(w, err)
		return
	}
	for _, p := range provider.All() { // built-in providers are nodes too
		if node, err := a.Store.Node(p.ID); err == nil && p.Routable() {
			nodes = append(nodes, node)
		}
	}
	type job struct {
		node *db.Node
		conn *db.Connection
	}
	var jobs []job
	skipped := 0
	for _, n := range nodes {
		if (n.Type != db.NodeTypeOpenAI && n.Type != db.NodeTypeAnthropic && n.Type != db.NodeTypeBuiltin) || (len(in.Providers) > 0 && !slices.Contains(in.Providers, n.ID)) {
			continue
		}
		conns, err := a.Store.Connections(n.ID, true)
		if err != nil {
			dbErr(w, err)
			return
		}
		if p, ok := n.Builtin(); ok && p.Status == provider.StatusExperimental {
			skipped += len(conns) // risky upstreams (CodeBuddy, Freebuff, ...) are only tested one key at a time, on purpose
			continue
		}
		for _, c := range conns {
			jobs = append(jobs, job{n, c})
		}
	}
	results := make([]map[string]any, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, took := a.runConnectionTest(r.Context(), j.conn, j.node, "")
			item := map[string]any{"connectionId": j.conn.ID, "name": j.conn.Name, "provider": j.node.ID, "providerName": j.node.Name,
				"ok": res.Valid, "latencyMs": took.Milliseconds(), "method": res.Method}
			if !res.Valid {
				item["error"] = res.Error
			}
			results[i] = item
		}()
	}
	wg.Wait()
	passed := 0
	for _, r := range results {
		if r["ok"] == true {
			passed++
		}
	}
	writeJSON(w, 200, map[string]any{"results": results, "summary": map[string]any{"total": len(results), "passed": passed, "failed": len(results) - passed, "skipped": skipped}})
}

func (a *API) connectionModels(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.Connection(chi.URLParam(r, "id"))
	if err != nil {
		dbErr(w, err)
		return
	}
	node, err := a.Store.Node(c.Provider)
	if err != nil {
		dbErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(gateway.ConnCtx(r.Context(), c), 20*time.Second)
	defer cancel()
	res := a.GW.Validate(ctx, node.Type, node.BaseURL(), node.APIType(), c.APIKey(), "")
	if !res.Valid {
		writeJSON(w, 200, map[string]any{"models": []string{}, "error": res.Error})
		return
	}
	writeJSON(w, 200, map[string]any{"models": res.Models})
}

// ---- usage ----

func (a *API) usageStats(w http.ResponseWriter, r *http.Request) {
	st, err := a.Store.Stats(queryInt(r, "hours", 24, 1, 24*30))
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (a *API) usageSummary(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "today"
	}
	if !db.Periods[period] {
		writeErr(w, 400, "period must be one of month, today, 24h, 7d, 30d, 60d, all")
		return
	}
	sum, err := a.Store.Summary(period)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, sum)
}

func (a *API) usageActive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"active": a.GW.Active()})
}

func (a *API) usageReset(w http.ResponseWriter, _ *http.Request) {
	n, err := a.Store.ResetUsage()
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

func (a *API) usageHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := a.Store.History(queryInt(r, "limit", 50, 1, 500), queryInt(r, "offset", 0, 0, 1<<30), q.Get("provider"), q.Get("model"), q.Get("status"))
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rows": rows, "total": total})
}

// ---- routing health / model test ----

// testModel sends a one-token request for prefix/model using the provider's first usable key.
func (a *API) testModel(w http.ResponseWriter, r *http.Request) {
	var in struct{ Model string }
	if !readJSON(w, r, &in) {
		return
	}
	prefix, model, ok := strings.Cut(in.Model, "/")
	if !ok || prefix == "" || model == "" {
		writeErr(w, 400, "model must be '<prefix>/<model>'")
		return
	}
	node, err := a.Store.NodeByPrefix(prefix)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "no provider uses prefix '" + prefix + "'"})
		return
	}
	if p, ok := node.Builtin(); ok && p.NoAuth {
		_ = a.Store.EnsureDefaultConnection(node)
	}
	conns, err := a.Store.Connections(node.ID, true)
	if err != nil {
		dbErr(w, err)
		return
	}
	modelKey := "model|" + node.ID + "|" + model
	if e, ok := a.recentTest(modelKey); ok {
		writeJSON(w, 200, e.out)
		return
	}
	reply := func(out map[string]any) {
		a.tests.Store(modelKey, testEntry{at: time.Now(), out: out})
		writeJSON(w, 200, out)
	}
	for _, c := range conns {
		if c.RateLimitedUntil().After(time.Now()) {
			continue
		}
		key, kerr := a.GW.Secret(r.Context(), node, c)
		if kerr != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": kerr.Error()})
			return
		}
		d, err := a.GW.ProbeModel(gateway.ConnCtx(r.Context(), c), node, key, model)
		if err != nil {
			consolelog.Logf("WARN", "TEST", "%s/%s via %s: %s", prefix, model, c.Name, err)
			reply(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		consolelog.Logf("INFO", "TEST", "%s/%s via %s: OK in %dms", prefix, model, c.Name, d.Milliseconds())
		reply(map[string]any{"ok": true, "latencyMs": d.Milliseconds()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": false, "error": "no active connection to test with"})
}

// routingHealth: per provider circuit-breaker state plus latency and success rate over the last 15 minutes.
func (a *API) routingHealth(w http.ResponseWriter, _ *http.Request) {
	nodes, err := a.Store.Nodes("")
	if err != nil {
		dbErr(w, err)
		return
	}
	stats, err := a.Store.RouteStats(time.Now().Add(-15 * time.Minute))
	if err != nil {
		dbErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, n := range nodes {
		if n.Type != db.NodeTypeOpenAI && n.Type != db.NodeTypeAnthropic {
			continue
		}
		state, retry, failures := a.GW.BreakerStatus(n.ID)
		item := map[string]any{"provider": n.ID, "name": n.Name, "type": n.Type, "circuit": state,
			"retryAfterMs": retry.Milliseconds(), "failures": failures, "avgLatencyMs": nil, "successRate": nil, "requests": 0}
		if st, ok := stats[n.ID]; ok && st.Requests > 0 {
			item["requests"] = st.Requests
			item["successRate"] = float64(int(float64(st.OK)/float64(st.Requests)*1000)) / 10
			if st.OK > 0 {
				item["avgLatencyMs"] = int(st.AvgLatencyMs)
			}
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"providers": out})
}

// consoleStream pushes server log lines as Server-Sent Events: {type:"init",entries}, then {type:"lines",entries} / {type:"clear"}.
func (a *API) consoleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	send := func(v any) bool {
		b, _ := json.Marshal(v)
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		return err == nil
	}
	snap, events, cancel := consolelog.Default.Subscribe()
	defer cancel()
	if !send(map[string]any{"type": "init", "entries": snap}) {
		return
	}
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		case ev, ok := <-events:
			if !ok { // dropped as a slow consumer: the browser reconnects and gets a fresh snapshot
				return
			}
			msg := map[string]any{"type": "lines", "entries": ev.Entries}
			if ev.Clear {
				msg = map[string]any{"type": "clear"}
			}
			if !send(msg) {
				return
			}
		}
	}
}

// builtinView is a registry provider as the dashboard sees it: catalogue (with which models are enabled), headers, key-pool config.
func (a *API) builtinView(p provider.Provider) map[string]any {
	enabled := map[string]bool{}
	if cms, err := a.Store.CustomModels(); err == nil {
		for _, m := range cms {
			if m.ProviderAlias == p.Alias {
				enabled[m.ID] = true
			}
		}
	}
	models := make([]map[string]any, 0, len(p.Models))
	inCatalogue := map[string]bool{}
	add := func(id, name string, custom bool) {
		item := map[string]any{"id": id, "name": name, "enabled": enabled[id], "custom": custom}
		for k, v := range capsView(id, a.Store.ModelCaps(p.Alias, id)) {
			item[k] = v
		}
		models = append(models, item)
	}
	for _, m := range p.Models {
		inCatalogue[m.ID] = true
		add(m.ID, m.Name, false)
	}
	// models the user added by hand (ids outside the catalogue) are listed too, always enabled
	if cms, err := a.Store.CustomModels(); err == nil {
		for _, m := range cms {
			if m.ProviderAlias == p.Alias && !inCatalogue[m.ID] {
				add(m.ID, m.Name, true)
			}
		}
	}
	custom := a.Store.ProviderHeaders(p.ID)
	effective := map[string]string{}
	for k, v := range p.Headers {
		effective[k] = v
	}
	for k, v := range custom {
		effective[k] = v
	}
	cfg := a.Store.ProviderConfig(p.ID)
	return map[string]any{"id": p.ID, "alias": p.Alias, "name": p.Name, "category": p.Category, "status": p.Status, "note": p.Note,
		"website": p.Website, "authHint": p.AuthHint, "noAuth": p.NoAuth, "passthrough": p.Passthrough, "baseUrl": p.BaseURL, "models": models,
		"oauth": p.OAuth != nil, "oauthKind": oauthKind(p), "usage": p.Usage != nil,
		"headers": map[string]any{"defaults": p.Headers, "custom": custom, "effective": effective, "customized": len(custom) > 0},
		"config":  cfg}
}

// listBuiltin: the registry of built-in providers (Cline, CodeBuddy, …) with their models and how far the port goes.
func (a *API) listBuiltin(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0, len(provider.All()))
	for _, p := range provider.All() {
		out = append(out, a.builtinView(p))
	}
	writeJSON(w, 200, map[string]any{"providers": out})
}

func (a *API) builtinParam(w http.ResponseWriter, r *http.Request) (provider.Provider, bool) {
	p, ok := provider.ByID(chi.URLParam(r, "id"))
	if !ok {
		writeErr(w, 404, "unknown built-in provider")
	}
	return p, ok
}

// setProviderHeaders stores the user's header overrides (the effective set is the provider's defaults plus these).
func (a *API) setProviderHeaders(w http.ResponseWriter, r *http.Request) {
	p, ok := a.builtinParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Headers map[string]string `json:"headers"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	h, err := db.ValidateHeaders(in.Headers)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// only what differs from the defaults is an override
	for k, v := range h {
		if p.Headers[k] == v {
			delete(h, k)
		}
	}
	if err := a.Store.SetProviderHeaders(p.ID, h); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, a.builtinView(p))
}

func (a *API) resetProviderHeaders(w http.ResponseWriter, r *http.Request) {
	p, ok := a.builtinParam(w, r)
	if !ok {
		return
	}
	if err := a.Store.SetProviderHeaders(p.ID, nil); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, a.builtinView(p))
}

// bulkBuiltinModels enables or disables the whole catalogue of a built-in provider.
func (a *API) bulkBuiltinModels(w http.ResponseWriter, r *http.Request) {
	p, ok := a.builtinParam(w, r)
	if !ok {
		return
	}
	var in struct{ Action string }
	if !readJSON(w, r, &in) {
		return
	}
	switch in.Action {
	case "enable-all":
		for _, m := range p.Models {
			if err := a.Store.AddCustomModel(p.Alias, m.ID, m.Name); err != nil {
				dbErr(w, err)
				return
			}
		}
	case "disable-all":
		for _, m := range p.Models {
			if err := a.Store.DeleteCustomModel(p.Alias, m.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
				dbErr(w, err)
				return
			}
		}
	default:
		writeErr(w, 400, "action must be enable-all or disable-all")
		return
	}
	writeJSON(w, 200, a.builtinView(p))
}

// setProviderConfig: round-robin / sticky for the key pool of any provider (custom or built-in).
func (a *API) setProviderConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.Store.Node(id); err != nil {
		writeErr(w, 404, "provider not found")
		return
	}
	var in db.ProviderConfig
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Store.SetProviderConfig(id, in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"config": a.Store.ProviderConfig(id)})
}

func oauthKind(p provider.Provider) string {
	if p.OAuth == nil {
		return ""
	}
	return p.OAuth.Kind
}
