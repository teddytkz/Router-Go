package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"router-go/internal/db"
	"router-go/internal/modelcaps"
)

// ---- combos ----

type comboInput struct {
	Name          *string   `json:"name"`
	Models        *[]string `json:"models"`
	Strategy      *string   `json:"strategy"`
	ContextLength *int      `json:"context_length"`
}

// validate checks the fields that are present; creating requires name and ≥1 model.
func (in *comboInput) validate(create bool) string {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !db.ValidComboName(*in.Name) {
			return "name must be 1-64 chars: letters, digits, '.', '_' or '-'"
		}
	} else if create {
		return "name is required"
	}
	if in.Models != nil {
		if len(*in.Models) > 100 {
			return "a combo can have at most 100 models"
		}
		*in.Models = db.NormalizeList(*in.Models)
	}
	if create && (in.Models == nil || len(*in.Models) == 0) {
		return "at least one model is required"
	}
	if in.Models != nil && len(*in.Models) == 0 {
		return "at least one model is required"
	}
	if in.Strategy != nil && *in.Strategy != "fallback" && *in.Strategy != "round-robin" {
		return "strategy must be 'fallback' or 'round-robin'"
	}
	return ""
}

func (a *API) listCombos(w http.ResponseWriter, _ *http.Request) {
	cs, err := a.Store.Combos()
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"combos": cs})
}

func (a *API) createCombo(w http.ResponseWriter, r *http.Request) {
	var in comboInput
	if !readJSON(w, r, &in) {
		return
	}
	if msg := in.validate(true); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	strategy := ""
	if in.Strategy != nil {
		strategy = *in.Strategy
	}
	c, err := a.Store.CreateCombo(*in.Name, *in.Models, strategy, in.ContextLength)
	if errors.Is(err, db.ErrConflict) {
		writeErr(w, 409, "a combo with this name already exists")
		return
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"combo": c})
}

func (a *API) updateCombo(w http.ResponseWriter, r *http.Request) {
	var in comboInput
	if !readJSON(w, r, &in) {
		return
	}
	if msg := in.validate(false); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	var models []string
	if in.Models != nil {
		models = *in.Models
	}
	var ctxLen **int
	if in.ContextLength != nil {
		ctxLen = &in.ContextLength
	}
	c, err := a.Store.UpdateCombo(chi.URLParam(r, "id"), in.Name, models, in.Strategy, ctxLen)
	if errors.Is(err, db.ErrConflict) {
		writeErr(w, 409, "a combo with this name already exists")
		return
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"combo": c})
}

func (a *API) deleteCombo(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteCombo(chi.URLParam(r, "id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// ---- model aliases ----

func (a *API) listAliases(w http.ResponseWriter, _ *http.Request) {
	m, err := a.Store.Aliases()
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"aliases": m})
}

func (a *API) setAlias(w http.ResponseWriter, r *http.Request) {
	var in struct{ Alias, Model string }
	if !readJSON(w, r, &in) {
		return
	}
	in.Alias, in.Model = strings.TrimSpace(in.Alias), strings.TrimSpace(in.Model)
	switch {
	case in.Alias == "" || strings.Contains(in.Alias, "/"):
		writeErr(w, 400, "alias is required and must not contain '/'")
		return
	case !strings.Contains(in.Model, "/") && !a.isCombo(in.Model): // a bare name can only mask a combo
		writeErr(w, 400, "target must be '<prefix>/<model>' or the name of a combo")
		return
	}
	if _, err := a.Store.ComboByName(in.Alias); err == nil {
		writeErr(w, 409, "a combo already uses this name")
		return
	}
	if err := a.Store.SetAlias(in.Alias, in.Model); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) isCombo(name string) bool { _, err := a.Store.ComboByName(name); return err == nil }

func (a *API) deleteAlias(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteAlias(r.URL.Query().Get("alias")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// ---- custom models ----

// capsView is what the dashboard needs per model: the explicit overrides, what the name suggests, and the result.
func capsView(id string, override map[string]any) map[string]any {
	det := modelcaps.Detect(id)
	return map[string]any{"caps": override, "detected": det, "effective": modelcaps.Merge(det, override)}
}

func (a *API) listCustomModels(w http.ResponseWriter, _ *http.Request) {
	ms, err := a.Store.CustomModels()
	if err != nil {
		dbErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		item := map[string]any{"providerAlias": m.ProviderAlias, "id": m.ID, "type": m.Type, "name": m.Name}
		for k, v := range capsView(m.ID, m.Caps) {
			item[k] = v
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"models": out})
}

// setModelCaps sets (or, with an empty/null caps, resets to auto-detect) the capability overrides of an added model.
func (a *API) setModelCaps(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProviderAlias string         `json:"providerAlias"`
		ID            string         `json:"id"`
		Caps          map[string]any `json:"caps"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Store.SetModelCaps(in.ProviderAlias, in.ID, modelcaps.ValidOverride(in.Caps)); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (a *API) addCustomModel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProviderAlias string         `json:"providerAlias"`
		ID            string         `json:"id"`
		Name          string         `json:"name"`
		Caps          map[string]any `json:"caps"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.ProviderAlias, in.ID, in.Name = strings.TrimSpace(in.ProviderAlias), strings.TrimSpace(in.ID), strings.TrimSpace(in.Name)
	if in.ProviderAlias == "" || in.ID == "" || strings.Contains(in.ProviderAlias, "|") || strings.Contains(in.ID, "|") {
		writeErr(w, 400, "providerAlias and id are required (no '|')")
		return
	}
	if _, err := a.Store.NodeByPrefix(in.ProviderAlias); err != nil {
		writeErr(w, 400, "no provider node uses prefix '"+in.ProviderAlias+"'")
		return
	}
	if err := a.Store.AddCustomModel(in.ProviderAlias, in.ID, in.Name); err != nil {
		dbErr(w, err)
		return
	}
	if len(in.Caps) > 0 {
		if err := a.Store.SetModelCaps(in.ProviderAlias, in.ID, modelcaps.ValidOverride(in.Caps)); err != nil {
			dbErr(w, err)
			return
		}
	}
	writeJSON(w, 201, map[string]any{"success": true})
}

func (a *API) deleteCustomModel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := a.Store.DeleteCustomModel(q.Get("providerAlias"), q.Get("id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
