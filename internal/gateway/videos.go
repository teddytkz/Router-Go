package gateway

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"router-go/internal/consolelog"
	"router-go/internal/db"
)

// videoStatus polls a video job: GET /v1/videos/{id}[/content]?model=<prefix>/<model>. Creating a job is a passthrough
// POST; the id only means something to the provider that issued it, so the client names that provider with `model`.
// Each active key of the provider is tried until one is allowed to see the job (jobs belong to one account).
func (g *Gateway) videoStatus(sub string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := g.authorize(w, r, "videos")
		if !ok {
			return
		}
		q := r.URL.Query()
		requested := q.Get("model")
		if requested == "" {
			writeErrFmt(w, "videos", 400, "Missing query parameter: model (the same model the job was created with)", "bad_request")
			return
		}
		node, mdl, err := g.resolve(requested)
		if err != nil {
			writeErrFmt(w, "videos", 404, "Model '"+requested+"' not found.", "model_not_found")
			return
		}
		if acc := g.accessFor(key); acc != nil && !acc.allowsModel(node, mdl) {
			writeErrFmt(w, "videos", 403, deniedMessage(requested), "model_not_allowed")
			return
		}
		if node.Type != db.NodeTypeOpenAI {
			writeErrFmt(w, "videos", 400, "Provider '"+node.Name+"' does not support video generation.", "provider_unsupported")
			return
		}
		conns, _ := g.Store.Connections(node.ID, true)
		if len(conns) == 0 {
			writeErrFmt(w, "videos", 400, "No active connection for provider '"+node.Name+"'.", "bad_request")
			return
		}
		q.Del("model")
		target := node.BaseURL() + "/videos/" + url.PathEscape(chi.URLParam(r, "id")) + sub
		if len(q) > 0 {
			target += "?" + q.Encode()
		}
		var last *http.Response
		for _, c := range conns {
			secret, kerr := g.Secret(r.Context(), node, c)
			if kerr != nil {
				continue
			}
			req, _ := g.newReq(ConnCtx(r.Context(), c), "GET", target, secret, nil, false, false)
			g.nodeHeaders(req, node)
			resp, err := g.clientFor(node).Do(req)
			if err != nil {
				consolelog.Logf("WARN", "UPSTREAM", "%s/%s video poll unreachable: %s", node.Name, c.Name, networkMessage(err))
				continue
			}
			if resp.StatusCode < 400 || len(conns) == 1 || (resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 404 && resp.StatusCode != 429) {
				if last != nil {
					last.Body.Close()
				}
				defer resp.Body.Close()
				_, _ = relay(w, resp, false)
				return
			}
			if last != nil {
				last.Body.Close()
			}
			last = resp
		}
		if last != nil {
			defer last.Body.Close()
			_, _ = relay(w, last, false)
			return
		}
		writeErrFmt(w, "videos", 502, "upstream unreachable", "upstream_error")
	}
}
