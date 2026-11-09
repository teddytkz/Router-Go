package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"router-go/internal/auth"
	"router-go/internal/translate"
)

// Gemini native API (/v1beta/models/...). The client format is "gemini": serve converts the Gemini body to a chat
// request at the top (geminiRequest) and from then on only the response side and the error envelope speak Gemini.

// requestKey is the client's API key: Bearer / x-api-key, plus Google's x-goog-api-key header and (Gemini only) ?key=.
func requestKey(r *http.Request, format string) string {
	if k := auth.BearerToken(r); k != "" {
		return k
	}
	if k := strings.TrimSpace(r.Header.Get("x-goog-api-key")); k != "" || format != "gemini" {
		return k
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// reqFormat is the format of the request body that attempt translates for the upstream: serve has already turned a
// Gemini body into chat.
func reqFormat(cf string) string {
	if cf == "gemini" {
		return "chat"
	}
	return cf
}

// geminiTarget splits the URL tail "prefix/model:action" of /v1beta/models/*.
func geminiTarget(r *http.Request) (model, action string) {
	name := chi.URLParam(r, "*")
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// geminiRequest turns the Gemini body into a chat request: the model comes from the URL and streaming from the
// action (:streamGenerateContent), never from the body.
func geminiRequest(r *http.Request, body map[string]any) (map[string]any, error) {
	model, action := geminiTarget(r)
	body["model"], body["stream"] = model, action == "streamGenerateContent"
	return translate.Request(translate.Gemini, translate.Chat, body)
}

var geminiStatus = map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
	429: "RESOURCE_EXHAUSTED", 502: "UNAVAILABLE", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}

// geminiError is Gemini's error envelope: {"error":{"code","message","status"}}.
func geminiError(status int, msg string) map[string]any {
	s, ok := geminiStatus[status]
	if !ok {
		s = "INVALID_ARGUMENT"
		if status >= 500 {
			s = "INTERNAL"
		}
	}
	return map[string]any{"error": map[string]any{"code": status, "message": msg, "status": s}}
}

// geminiPost routes POST /v1beta/models/{model}:{action}.
func (g *Gateway) geminiPost(w http.ResponseWriter, r *http.Request) {
	switch _, action := geminiTarget(r); action {
	case "generateContent", "streamGenerateContent":
		g.serve("gemini")(w, r)
	case "countTokens":
		g.geminiCount(w, r)
	default:
		writeErrFmt(w, "gemini", 404, "Unsupported method '"+action+"': use generateContent, streamGenerateContent or countTokens.", "not_found")
	}
}

// geminiCount answers :countTokens with the local estimate (about 4 characters per token, 1000 per image): the
// router has no tokenizer of the upstream model.
func (g *Gateway) geminiCount(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.authorize(w, r, "gemini"); !ok {
		return
	}
	var body map[string]any
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&body) != nil {
		writeErrFmt(w, "gemini", 400, "Invalid JSON body", "bad_request")
		return
	}
	if inner, ok := body["generateContentRequest"].(map[string]any); ok {
		body = inner
	}
	chat, err := translate.Request(translate.Gemini, translate.Chat, body)
	if err != nil {
		writeErrFmt(w, "gemini", 400, err.Error(), "bad_request")
		return
	}
	delete(chat, "max_tokens") // the requested output and the model name are not input
	delete(chat, "model")
	writeJSON(w, map[string]any{"totalTokens": estimateTokens(chat)})
}

// geminiModel is a /v1beta/models entry for a model the key may use.
func (g *Gateway) geminiModel(m modelItem) map[string]any {
	in := g.capsOfID(m.ID).Context
	if in == 0 {
		in = 128000
	}
	return map[string]any{"name": "models/" + m.ID, "displayName": m.ID, "description": m.OwnedBy + " model: " + m.ID,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		"inputTokenLimit":            in, "outputTokenLimit": 8192}
}

// geminiModels lists models (GET /v1beta/models), or returns one (GET /v1beta/models/{model}).
func (g *Gateway) geminiModels(w http.ResponseWriter, r *http.Request) {
	key, ok := g.authorize(w, r, "gemini")
	if !ok {
		return
	}
	items, err := g.visibleModels(key)
	if err != nil {
		writeErrFmt(w, "gemini", 500, "database error", "internal_server_error")
		return
	}
	if id, _ := geminiTarget(r); id != "" {
		for _, m := range items {
			if m.ID == id {
				writeJSON(w, g.geminiModel(m))
				return
			}
		}
		writeErrFmt(w, "gemini", 404, "models/"+id+" is not found or you do not have access to it.", "model_not_found")
		return
	}
	list := make([]any, 0, len(items))
	for _, m := range items {
		list = append(list, g.geminiModel(m))
	}
	writeJSON(w, map[string]any{"models": list})
}
