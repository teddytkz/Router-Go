package gateway

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"router-go/internal/consolelog"
	"router-go/internal/modelcaps"
)

// Capability-aware routing.
//
//   - requiredCaps reads what a request needs: images or audio in the current user turn are HARD requirements (a model
//     without them would drop the data); a reasoning/thinking request is SOFT (a model without it still answers).
//   - route reorders the candidates so models that satisfy the request come first (stable, nothing is dropped, so
//     fallback still works), and, when NO candidate can satisfy a hard requirement, prepends the Capacity Adapter pool
//     configured in settings.capacityAdapter (e.g. a vision model for a text-only combo).

var hardCaps = []string{modelcaps.Vision, modelcaps.Audio}

// requiredCaps inspects a client request of format cf.
func requiredCaps(req map[string]any, cf string) (hard, soft []string) {
	add := func(l *[]string, k string) {
		if !slices.Contains(*l, k) {
			*l = append(*l, k)
		}
	}
	scanPart := func(p any) {
		m, _ := p.(map[string]any)
		switch m["type"] {
		case "image_url", "image", "input_image":
			add(&hard, modelcaps.Vision)
		case "input_audio", "audio_url", "audio":
			add(&hard, modelcaps.Audio)
		case "tool_result": // Anthropic: results may carry screenshots
			if inner, ok := m["content"].([]any); ok {
				for _, ip := range inner {
					if im, _ := ip.(map[string]any); im["type"] == "image" {
						add(&hard, modelcaps.Vision)
					}
				}
			}
		case "text", "input_text":
			if s, _ := m["text"].(string); strings.Contains(s, "data:image/") {
				add(&hard, modelcaps.Vision)
			}
		}
	}
	scanContent := func(c any) {
		switch v := c.(type) {
		case []any:
			for _, p := range v {
				scanPart(p)
			}
		case string:
			if strings.Contains(v, "data:image/") {
				add(&hard, modelcaps.Vision)
			}
		}
	}
	// only the current user turn counts: history media must not pin the request to a vision model
	trailing := func(items []any) []any {
		i := len(items) - 1
		for i >= 0 {
			if m, _ := items[i].(map[string]any); m["role"] == "assistant" || m["role"] == "model" {
				break
			}
			i--
		}
		return items[i+1:]
	}
	if cf == "responses" {
		items, _ := req["input"].([]any)
		for _, it := range trailing(items) {
			m, _ := it.(map[string]any)
			scanContent(m["content"])
		}
	} else {
		msgs, _ := req["messages"].([]any)
		for _, it := range trailing(msgs) {
			m, _ := it.(map[string]any)
			scanContent(m["content"])
		}
	}

	notNone := func(v any) bool { s, ok := v.(string); return ok && s != "" && s != "none" }
	switch {
	case notNone(req["reasoning_effort"]):
	case req["enable_thinking"] == true:
	default:
		if r, ok := req["reasoning"].(map[string]any); ok && (notNone(r["effort"]) || r["summary"] != nil) {
			break
		}
		if t, ok := req["thinking"].(map[string]any); ok && (t["type"] == "enabled" || t["type"] == "adaptive") {
			break
		}
		return hard, soft
	}
	return hard, append(soft, modelcaps.Reasoning)
}

// ctxCheckFrom: below this many tokens every model fits, so combos skip the (database) lookups for the context check.
const ctxCheckFrom = 4096

// estimateTokens is a rough size of the request: ~4 characters per token for text, a flat 1000 per embedded image or
// file (their base64 would otherwise dwarf everything), plus the output the client asked for.
func estimateTokens(req map[string]any) int {
	var walk func(v any) int
	walk = func(v any) int {
		switch x := v.(type) {
		case string:
			if strings.HasPrefix(x, "data:") {
				return 1000
			}
			return (len(x) + 3) / 4
		case []any:
			n := 0
			for _, e := range x {
				n += walk(e)
			}
			return n
		case map[string]any:
			if x["type"] == "base64" { // Anthropic image/document source: its payload is not text
				return 1000
			}
			n := 0
			for _, e := range x {
				n += walk(e)
			}
			return n
		}
		return 0
	}
	n := walk(req)
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		switch v := req[k].(type) {
		case json.Number:
			if i, err := v.Int64(); err == nil && i > 0 {
				return n + int(i)
			}
		case float64:
			if v > 0 {
				return n + int(v)
			}
		}
	}
	return n
}

func fits(c modelcaps.Caps, need int) bool { return c.Context == 0 || c.Context >= need }

func (g *Gateway) capsOf(t target) modelcaps.Caps {
	if t.node == nil {
		return modelcaps.Caps{}
	}
	return modelcaps.Merge(modelcaps.Detect(t.model), g.Store.ModelCaps(t.node.Prefix(), t.model))
}

func satisfies(c modelcaps.Caps, keys []string) bool {
	for _, k := range keys {
		if !c.Has(k) {
			return false
		}
	}
	return true
}

type adapterPool struct {
	Enabled    bool
	RoundRobin bool
	Models     []string
}

// adapterSetting reads settings.capacityAdapter[cap].
func (g *Gateway) adapterSetting(cap string) adapterPool {
	set, err := g.Store.Settings()
	if err != nil {
		return adapterPool{}
	}
	all, _ := set["capacityAdapter"].(map[string]any)
	e, _ := all[cap].(map[string]any)
	p := adapterPool{Enabled: e["enabled"] != false, RoundRobin: e["roundRobin"] == true} // on unless switched off
	if ms, ok := e["models"].([]any); ok {
		for _, m := range ms {
			if s, ok := m.(string); ok && strings.TrimSpace(s) != "" {
				p.Models = append(p.Models, strings.TrimSpace(s))
			}
		}
	}
	return p
}

// route applies the adapter and the capability ordering to the candidate list.
func (g *Gateway) route(targets []target, hard, soft []string, acc *access, label string, need int) []target {
	if len(hard) == 0 && len(soft) == 0 && (need < ctxCheckFrom || len(targets) < 2) {
		return targets
	}
	resolved := func(t target) bool { return t.err == "" && t.node != nil }

	// 1. nobody can satisfy the hard requirement: bring in the adapter pool
	if len(hard) > 0 && !slices.ContainsFunc(targets, func(t target) bool { return resolved(t) && satisfies(g.capsOf(t), hard) }) {
		var pool []target
		for _, capKey := range hard {
			cfg := g.adapterSetting(capKey)
			if !cfg.Enabled || len(cfg.Models) == 0 {
				continue
			}
			members := cfg.Models
			if cfg.RoundRobin {
				members = g.rotate("adapter:"+capKey, members)
			}
			for _, m := range members {
				n, model, err := g.resolve(m)
				if err != nil {
					continue
				}
				t := target{node: n, model: model, label: n.Prefix() + "/" + model}
				if satisfies(g.capsOf(t), hard) && acc.allowsModel(n, model) &&
					!slices.ContainsFunc(pool, func(p target) bool { return p.label == t.label }) &&
					!slices.ContainsFunc(targets, func(p target) bool { return p.label == t.label }) {
					pool = append(pool, t)
				}
			}
		}
		if len(pool) > 0 {
			consolelog.Logf("INFO", "ROUTE", "%s needs %s but none of its models has it: trying adapter model %s first", label, strings.Join(hard, "+"), pool[0].label)
			targets = append(pool, targets...)
		} else {
			consolelog.Logf("WARN", "ROUTE", "%s needs %s but no model that can serve it is available (set up the Capacity Adapter in Combos)", label, strings.Join(hard, "+"))
		}
	}

	// 2. stable tiers: 0 = satisfies hard and soft, 1 = hard only, 2 = the rest (unresolved members last)
	if len(targets) < 2 {
		return targets
	}
	tier := func(t target) int {
		if !resolved(t) {
			return 3
		}
		c := g.capsOf(t)
		switch {
		case !satisfies(c, hard), !fits(c, need):
			return 2
		case satisfies(c, soft):
			return 0
		}
		return 1
	}
	if need < ctxCheckFrom {
		need = 0 // small request: every model fits, only capabilities matter
	}
	before := targets[0].label
	sorted := slices.Clone(targets)
	slices.SortStableFunc(sorted, func(a, b target) int { return tier(a) - tier(b) })
	if sorted[0].label != before {
		why := strings.Join(append(slices.Clone(hard), soft...), "+")
		if need >= ctxCheckFrom {
			why = strings.TrimPrefix(why+" ~"+strconv.Itoa(need)+" tokens", " ")
		}
		consolelog.Logf("INFO", "ROUTE", "%s: request needs %s, %s goes first", label, why, sorted[0].label)
	}
	return sorted
}
