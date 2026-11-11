package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"router-go/internal/consolelog"
	"router-go/internal/db"
	"router-go/internal/translate"
)

var upstreamPath = map[string]string{"chat": "/chat/completions", "responses": "/responses", "messages": "/messages"}

// passthrough are the OpenAI-style endpoints that are forwarded as they are (only "model" is swapped for the upstream's
// name): no format translation, no token rewrites. The key is the client format; the value the path on the upstream.
var passthrough = map[string]string{
	"embeddings": "/embeddings", "images": "/images/generations", "speech": "/audio/speech", "transcriptions": "/audio/transcriptions",
	"videos": "/videos/generations", "video-edits": "/videos/edits", "video-extensions": "/videos/extensions",
}

func isPass(cf string) bool { _, ok := passthrough[cf]; return ok }

// passLabel names a passthrough kind in messages ("does not support image generation").
var passLabel = map[string]string{"embeddings": "embeddings", "images": "image generation", "speech": "speech synthesis", "transcriptions": "transcription",
	"videos": "video generation", "video-edits": "video editing", "video-extensions": "video extension"}

// serve handles one client call in format cf ("chat", "responses", "messages" or "embeddings").
// Flow: auth → key access → plan (single model or combo members) → try each target in order → relay the first success.
func (g *Gateway) serve(cf string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		key, ok := g.authorize(w, r, cf)
		if !ok {
			return
		}
		fail := func(status int, msg, code string) { writeErrFmt(w, cf, status, msg, code) }
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		if err != nil {
			fail(413, "request body too large or unreadable", "bad_request")
			return
		}
		var req map[string]any
		ctype := r.Header.Get("Content-Type")
		if isPass(cf) && strings.HasPrefix(ctype, "multipart/") { // file uploads (transcriptions, video edits): only the model field is read
			m, err := multipartModel(raw, ctype)
			if err != nil {
				fail(400, "Invalid multipart body: "+err.Error(), "bad_request")
				return
			}
			req = map[string]any{"model": m}
		} else {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber() // keep large integers / float formatting intact
			if err := dec.Decode(&req); err != nil {
				fail(400, "Invalid JSON body", "bad_request")
				return
			}
		}
		if cf == "gemini" { // model and stream come from the URL; the rest of the pipeline sees a chat request
			if req, err = geminiRequest(r, req); err != nil {
				fail(400, "cannot convert request: "+err.Error(), "bad_request")
				return
			}
		}
		requested, _ := req["model"].(string)
		if requested == "" {
			fail(400, "Missing required field: model", "bad_request")
			return
		}
		acc := g.accessFor(key)
		combo, targets, err := g.plan(requested)
		switch {
		case err == errNoModels:
			fail(400, "Combo '"+requested+"' has no models.", "bad_request")
			return
		case err != nil:
			if acc != nil { // don't reveal which names exist to a restricted key
				fail(403, deniedMessage(requested), "model_not_allowed")
				return
			}
			var un *unsupportedError
			if errors.As(err, &un) {
				fail(400, un.Error(), "provider_unsupported")
				return
			}
			var na *notAddedError
			if errors.As(err, &na) {
				fail(404, na.Error(), "model_not_added")
				return
			}
			fail(404, "Model '"+requested+"' not found. Use '<prefix>/<model>' of a configured provider, or a combo name.", "model_not_found")
			return
		}
		// checked on the requested identity, before combo expansion and before any credential is touched
		if combo != nil && !acc.allowsCombo(combo.Name) || combo == nil && !acc.allowsModel(targets[0].node, targets[0].model) {
			fail(403, deniedMessage(requested), "model_not_allowed")
			return
		}

		if !isPass(cf) {
			hard, soft := requiredCaps(req, cf)
			targets = g.route(targets, hard, soft, acc, requested, estimateTokens(req))
		}
		stream, _ := req["stream"].(bool)
		pipe := g.pipelineFor()
		pipe.raw, pipe.ctype = raw, ctype
		rec := db.Usage{Endpoint: r.URL.Path, APIKey: db.MaskKey(key), Provider: "", Model: requested}
		record := func(o *outcome, status string) {
			rec.Status, rec.LatencyMs = status, int(time.Since(start).Milliseconds())
			if o.label != "" {
				rec.Model = o.label // the concrete model, not the combo/alias the client asked for
			}
			rec.PromptTokens, rec.CompletionTokens, rec.CacheTokens = o.u.prompt, o.u.completion, o.u.cache
			if o.node != nil {
				rec.Provider = o.node.ID
			}
			if o.conn != nil {
				rec.ConnectionID = o.conn.ID
			}
			_ = g.Store.AddUsage(rec)
			g.logRequest(strings.ToUpper(cf), requested, rec, o, status)
		}

		var last, scoped *failure
		var lastNode *db.Node // provider recorded in usage for a failed request
		var lastLabel string
		var soonest time.Time
		for i, t := range targets {
			if t.err != "" {
				last = &failure{status: 404, msg: t.err, scoped: true}
				continue
			}
			rw, peek := w, (*peekWriter)(nil)
			if i < len(targets)-1 && !isPass(cf) { // a member that answers with nothing hands over to the next one
				peek = newPeek(w, cf)
				rw = peek
			}
			o := g.attempt(rw, r, t, cf, req, stream, pipe)
			o.node, o.label = t.node, t.label
			if peek != nil && o.res == served && !peek.release() {
				o = outcome{res: failed, f: &failure{status: 502, msg: "returned an empty response (no text and no tool call)", conn: o.conn}, conn: o.conn, node: t.node, label: t.label}
			}
			switch o.res {
			case served:
				record(&o, o.status)
				return
			case aborted:
				record(&o, "canceled")
				return
			}
			last, lastNode, lastLabel = o.f, t.node, t.label
			if combo != nil {
				why := ""
				if o.f.msg != "" { // e.g. "returned an empty response": say why the member was skipped
					why = " — " + firstLine(o.f.msg, nil)
				}
				consolelog.Logf("INFO", "COMBO", "%s: %s failed (%d), trying next%s", combo.Name, t.label, o.f.status, why)
			}
			if o.f.scoped && scoped == nil {
				scoped = o.f
			}
			if !o.f.retry.IsZero() && (soonest.IsZero() || o.f.retry.Before(soonest)) {
				soonest = o.f.retry
			}
		}
		// every candidate failed before any byte reached the client
		switch {
		case scoped != nil: // the request itself is wrong: show the member's own answer
			last = scoped
		case combo != nil && len(targets) > 1:
			msg := "All " + strconv.Itoa(len(targets)) + " models in combo '" + combo.Name + "' failed"
			if last != nil && last.msg != "" {
				msg += ": " + last.msg
			}
			last = &failure{status: 503, msg: msg, retry: soonest}
		case last == nil:
			last = &failure{status: 502, msg: "no upstream available"}
		}
		last.relay(w, cf)
		record(&outcome{node: lastNode, label: lastLabel, conn: last.conn, note: fmt.Sprintf("%d %s", last.status, firstLine(last.msg, last.body))}, "error")
	}
}

type result int

const (
	served  result = iota // a response was written to the client
	failed                // nothing written; f says why, the caller may try the next target
	aborted               // the client went away
)

type outcome struct {
	res    result
	f      *failure
	node   *db.Node
	label  string // prefix/model that served (or failed)
	conn   *db.Connection
	u      usage
	status string // usage status when served: ok | error
	note   string // why it failed, for the log
}

// attempt tries one target across its eligible connections (key pool), marking cooldowns on the way.
func (g *Gateway) attempt(w http.ResponseWriter, r *http.Request, t target, cf string, req map[string]any, stream bool, pipe pipeline) outcome {
	node := t.node
	uf := g.formatFor(node, t.model)
	if isPass(cf) {
		uf = "chat"
		if node.Type != db.NodeTypeOpenAI {
			return outcome{res: failed, f: &failure{status: 400, msg: "Provider '" + node.Name + "' does not support " + passLabel[cf] + ".", scoped: true}}
		}
	}
	if f := g.providerQuota(node); f != nil {
		return outcome{res: failed, f: f}
	}
	if p, ok := node.Builtin(); ok && p.NoAuth {
		_ = g.Store.EnsureDefaultConnection(node)
	}
	conns, err := g.Store.Connections(node.ID, true)
	if err != nil {
		return outcome{res: failed, f: &failure{status: 500, msg: "database error"}}
	}
	if len(conns) == 0 {
		return outcome{res: failed, f: &failure{status: 400, msg: "No active connection for provider '" + node.Name + "'. Add an API key in the dashboard.", scoped: true}}
	}
	var eligible []*db.Connection
	var soonest time.Time
	var quotaMsg string
	for _, c := range conns {
		if u := c.RateLimitedUntil(); u.After(time.Now()) {
			if soonest.IsZero() || u.Before(soonest) {
				soonest = u
			}
			continue
		}
		if reset, msg, hit := g.connectionQuota(node, c); hit {
			quotaMsg = msg
			if soonest.IsZero() || reset.Before(soonest) {
				soonest = reset
			}
			continue
		}
		eligible = append(eligible, c)
	}
	if len(eligible) == 0 {
		secs := int(time.Until(soonest).Seconds()) + 1
		msg := "All " + strconv.Itoa(len(conns)) + " connection(s) for '" + node.Name + "' are cooling down. Retry in " + strconv.Itoa(secs) + "s."
		if quotaMsg != "" {
			msg = quotaMsg + " Retry in " + strconv.Itoa(secs) + "s."
		}
		return outcome{res: failed, f: &failure{status: 429, retry: soonest, msg: msg}}
	}

	anthropic := node.Type == db.NodeTypeAnthropic
	np, native := nativeOf(node)
	respFmt := g.respFormat(node, uf)
	// a Native provider's Response always speaks chat, so its lanes stream too and are assembled below
	forceStream := g.forceStream(node) && (uf == "chat" || native) && !isPass(cf)
	var payload []byte
	path, ctype := passthrough[cf], ""
	if isPass(cf) {
		payload, ctype, err = passBody(req, pipe.raw, pipe.ctype, t.model)
	} else {
		payload, err = buildPayload(req, reqFormat(cf), uf, t.model, stream, forceStream, pipe.tf, g.bodyFix(node, t.model))
		path = upstreamPath[uf] + compactSuffix(r, uf)
	}
	if err != nil {
		return outcome{res: failed, f: &failure{status: 400, msg: "cannot convert request: " + err.Error(), scoped: true}}
	}
	br := g.breakerFor(node.ID)
	if pipe.breaker {
		if ok, wait := br.allow(); !ok {
			consolelog.Logf("WARN", "BREAKER", "%s is open: failing fast for %ds", node.Name, int(wait.Seconds())+1)
			return outcome{res: failed, f: &failure{status: 503, retry: time.Now().Add(wait),
				msg: "Provider '" + node.Name + "' is temporarily unavailable after repeated failures (circuit breaker open)."}}
		}
	}
	if cfg := g.Store.ProviderConfig(node.ID); cfg.RoundRobin && len(eligible) > 1 { // spread load over the keys
		ids := make([]string, len(eligible))
		for i, c := range eligible {
			ids[i] = c.ID
		}
		order := rotateSticky("keys:"+node.ID, ids, cfg.Sticky)
		slices.SortStableFunc(eligible, func(a, b *db.Connection) int { return slices.Index(order, a.ID) - slices.Index(order, b.ID) })
	}
	if pipe.sem { // accounts with a free slot first; the rest queue
		slices.SortStableFunc(eligible, func(a, b *db.Connection) int {
			return cmp.Compare(boolInt(g.busy(a)), boolInt(g.busy(b)))
		})
	}

	var last *failure
	perm := 0   // consecutive keys rejected for good (suspended, revoked): a dead pool is not walked to the end
	uFail := uf // format of an upstream error body (passthrough kinds are relayed untouched)
	if isPass(cf) {
		uFail = cf
	}
	// try runs one connection; stop=true ends the attempt with out.
	try := func(c *db.Connection) (out outcome, stop bool) {
		if max := maxConcurrency(c); pipe.sem && max > 0 {
			if g.busy(c) {
				consolelog.Logf("INFO", "LIMIT", "%s/%s is at its concurrency cap (%d): request queued", node.Name, c.Name, max)
			}
			release, err := g.limiter.acquire(r.Context(), c.ID, max)
			if err != nil {
				if r.Context().Err() != nil {
					return outcome{res: aborted, conn: c}, true
				}
				last = &failure{status: 429, msg: "Account '" + c.Name + "': " + err.Error(), conn: c}
				return outcome{}, false
			}
			defer release() // held until the response has been fully relayed
		}
		defer g.track(ActiveRequest{Provider: node.ID, Model: t.label, ConnectionID: c.ID, Account: c.Name, Endpoint: r.URL.Path, Started: time.Now()})()
		key, kerr := g.Secret(r.Context(), node, c)
		if kerr != nil { // an expired sign-in that cannot be renewed: bench this connection, the next one may work
			g.Store.MarkConnection(c.ID, 401, kerr.Error(), 2*time.Minute, c.BackoffLevel())
			consolelog.Logf("WARN", "AUTH", "%s/%s: %s", node.Name, c.Name, kerr)
			last = &failure{status: 401, msg: kerr.Error(), conn: c}
			return outcome{}, false
		}
		g.logPool(ConnCtx(r.Context(), c), node, c)
		// the finished-request line only appears once the answer is complete: say now where it went, so a long stream is visible
		consolelog.Logf("INFO", strings.ToUpper(cf), "%s via %s/%s · sent, waiting for the answer", t.label, node.Name, c.Name)
		var resp *http.Response
		var err error
		if native { // the provider's own wire protocol; the answer comes back as chat (uf is "chat")
			psd, _ := c.Data["providerSpecificData"].(map[string]any)
			resp, err = g.nativeDo(ConnCtx(r.Context(), c), node, np, psd, key, t.model, payload, stream)
		} else {
			hreq, _ := g.newReq(ConnCtx(r.Context(), c), "POST", node.BaseURL()+path, key, payload, stream || forceStream, anthropic)
			if ctype != "" {
				hreq.Header.Set("Content-Type", ctype)
			}
			g.nodeHeaders(hreq, node)
			resp, err = g.clientFor(node).Do(hreq)
		}
		if err != nil {
			if r.Context().Err() != nil {
				return outcome{res: aborted, conn: c}, true
			}
			g.breakerEvent(node, br.record(true))
			consolelog.Logf("WARN", "UPSTREAM", "%s/%s unreachable: %s", node.Name, c.Name, networkMessage(err))
			g.Store.MarkConnection(c.ID, 0, err.Error(), transientCooldown, c.BackoffLevel())
			last = &failure{status: 502, msg: "upstream unreachable: " + networkMessage(err), conn: c}
			return outcome{}, false
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if providerFailure(resp.StatusCode) {
				g.breakerEvent(node, br.record(true))
			}
			d := classify(resp.StatusCode, string(body), c.BackoffLevel())
			f := &failure{status: resp.StatusCode, body: body, header: resp.Header, conn: c, uf: uFail}
			if !d.fallback { // the request itself is wrong, another key will not help
				consolelog.Logf("WARN", "UPSTREAM", "%s/%s rejected the request (%d): %s", node.Name, c.Name, resp.StatusCode, firstLine("", body))
				f.scoped = true
				return outcome{res: failed, f: f, conn: c}, true
			}
			consolelog.Logf("WARN", "UPSTREAM", "%s/%s → %d, key cooling for %s", node.Name, c.Name, resp.StatusCode, d.cooldown.Round(time.Second))
			g.Store.MarkConnection(c.ID, resp.StatusCode, snippet(body, resp.StatusCode), d.cooldown, d.level)
			last = f
			if d.cooldown == permanentCooldown {
				if perm++; perm >= maxPermanentFailures {
					consolelog.Logf("WARN", "UPSTREAM", "%s: %d keys in a row are permanently rejected, giving up on this provider", node.Name, perm)
					return outcome{res: failed, f: f, conn: c}, true
				}
			} else {
				perm = 0
			}
			return outcome{}, false
		}
		g.breakerEvent(node, br.record(false))

		var u usage
		var relayErr error
		converted := true
		switch {
		case forceStream && !stream: // the upstream was asked to stream; assemble a normal response for this client
			u, converted, relayErr = relayAggregated(w, resp, cf, t.model)
		case cf == respFmt || isPass(cf):
			u, relayErr = relay(w, resp, g.envelope(node))
		default:
			u, converted, relayErr = relayConverted(w, resp, respFmt, cf, t.model, g.envelope(node))
		}
		resp.Body.Close()
		if !converted && relayErr == nil { // unreadable body, nothing was written: next key
			last = &failure{status: 502, msg: "upstream returned a response that could not be converted", conn: c}
			return outcome{}, false
		}
		if c.Data["testStatus"] != "active" || c.Data["lastError"] != nil || c.BackoffLevel() > 0 || c.Data["rateLimitedUntil"] != nil {
			g.Store.MarkConnection(c.ID, 0, "", 0, 0)
		}
		switch {
		case relayErr != nil && r.Context().Err() != nil:
			return outcome{res: aborted, conn: c, u: u}, true
		case relayErr != nil:
			return outcome{res: served, conn: c, u: u, status: "error"}, true
		}
		return outcome{res: served, conn: c, u: u, status: "ok"}, true
	}
	for _, c := range eligible {
		if out, stop := try(c); stop {
			return out
		}
	}
	if last == nil {
		last = &failure{status: 502, msg: "no upstream available"}
	}
	return outcome{res: failed, f: last, conn: last.conn}
}

// buildPayload prepares the upstream body: translated when the formats differ, model replaced by the upstream's name.
func buildPayload(req map[string]any, cf, uf, model string, stream, force bool, tf transforms, fix func(map[string]any)) ([]byte, error) {
	body := make(map[string]any, len(req))
	for k, v := range req {
		body[k] = v
	}
	if cf != uf {
		var err error
		if body, err = translate.Request(translate.Format(cf), translate.Format(uf), body); err != nil {
			return nil, err
		}
	}
	body["model"] = model
	if force { // the upstream must be asked to stream whatever the client wants; usage arrives in the last chunk
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
		stream = true
	}
	if !stream {
		delete(body, "stream_options") // several upstreams 400 on this for non-stream calls
	}
	if tf.active() || (fix != nil && uf == "chat") {
		body = deepCopy(body) // nested messages are shared with other combo members: never edit them in place
		if tf.active() {
			tf.apply(body, uf)
		}
		if fix != nil && uf == "chat" {
			fix(body)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func snippet(body []byte, status int) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "HTTP " + strconv.Itoa(status)
	}
	return "[" + strconv.Itoa(status) + "] " + s
}

// pipeline is the per-request view of the optional rewrites and guards (Settings → Token Saver).
type pipeline struct {
	tf      transforms
	breaker bool
	sem     bool
	raw     []byte // the client's body and content type, for passthrough kinds that forward it as it came
	ctype   string
}

func (g *Gateway) pipelineFor() pipeline {
	p := pipeline{tf: g.transformsFor(), breaker: true, sem: true}
	if set, err := g.Store.Settings(); err == nil {
		p.breaker, p.sem = set["circuitBreakerEnabled"] != false, set["semaphoreEnabled"] != false
	}
	return p
}

// maxConcurrency is the per-account in-flight cap: providerSpecificData.maxConcurrency, default 3, 0 = unlimited.
func maxConcurrency(c *db.Connection) int {
	if psd, ok := c.Data["providerSpecificData"].(map[string]any); ok {
		if v, ok := psd["maxConcurrency"].(float64); ok {
			return max(int(v), 0)
		}
	}
	return defaultConcurrency
}

// busy: the account is at its concurrency cap, so a new request would have to queue.
func (g *Gateway) busy(c *db.Connection) bool {
	m := maxConcurrency(c)
	return m > 0 && !g.limiter.free(c.ID, m)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (g *Gateway) breakerEvent(node *db.Node, changed string) {
	switch changed {
	case "OPEN":
		consolelog.Logf("ERROR", "BREAKER", "%s tripped after repeated failures: requests fail fast, one probe every %s or more", node.Name, breakerReset)
	case "CLOSED":
		consolelog.Logf("INFO", "BREAKER", "%s recovered: circuit closed", node.Name)
	}
}

// firstLine is a short human message from an upstream error (msg, or the body's error message), capped for the log.
func firstLine(msg string, body []byte) string {
	if msg == "" && body != nil {
		msg = errorMessage(body, 0)
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// logRequest writes one line per finished client request: who served it, how long it took, tokens, and why it failed.
func (g *Gateway) logRequest(tag, requested string, rec db.Usage, o *outcome, status string) {
	via := rec.Model
	if requested != "" && requested != rec.Model {
		via = requested + " → " + rec.Model // a combo or alias
	}
	who := ""
	if o.node != nil {
		who = " via " + o.node.Name
		if o.conn != nil {
			who += "/" + o.conn.Name
		}
	}
	switch status {
	case "ok":
		consolelog.Logf("INFO", tag, "%s%s · %dms · %d↑ %d↓", via, who, rec.LatencyMs, rec.PromptTokens, rec.CompletionTokens)
	case "canceled":
		consolelog.Logf("WARN", tag, "%s%s · client disconnected after %dms", via, who, rec.LatencyMs)
	default:
		note := o.note
		if note == "" {
			note = "response interrupted"
		}
		consolelog.Logf("ERROR", tag, "%s%s · failed after %dms: %s", via, who, rec.LatencyMs, note)
	}
}

// formatFor is the wire format used to talk to node for a model (built-in providers can differ per model).
func (g *Gateway) formatFor(node *db.Node, model string) string {
	if p, ok := node.Builtin(); ok {
		return p.ModelFormat(model)
	}
	return node.Format()
}

func (g *Gateway) forceStream(node *db.Node) bool {
	p, ok := node.Builtin()
	return ok && p.ForceStream
}

// respFormat is the format the upstream answer comes back in: a Native provider converts its own lanes to chat, so the
// relay never needs to know them.
func (g *Gateway) respFormat(node *db.Node, uf string) string {
	if _, native := nativeOf(node); native {
		return "chat"
	}
	return uf
}

func (g *Gateway) envelope(node *db.Node) bool {
	p, ok := node.Builtin()
	return ok && p.Envelope
}

// nodeHeaders adds the static headers a built-in provider needs.
func (g *Gateway) nodeHeaders(req *http.Request, node *db.Node) {
	if p, ok := node.Builtin(); ok {
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}
	}
	if p, ok := node.Builtin(); ok && p.DynamicHeaders != nil {
		for k, v := range p.DynamicHeaders() {
			req.Header.Set(k, v)
		}
	}
	for k, v := range g.Store.ProviderHeaders(node.ID) { // the user's overrides win
		req.Header.Set(k, v)
	}
}
