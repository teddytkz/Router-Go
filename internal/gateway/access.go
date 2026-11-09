package gateway

import (
	"strings"

	"router-go/internal/db"
)

// access is the resolved allow list of a restricted API key. A nil *access means unrestricted.
// Matching is on resolved identities: an alias, a prefix spelling or `prefix/model` that lead to the
// same node+model are one target, and a combo name only matches a combo.
type access struct {
	combos, models map[string]bool
}

func canon(nodeID, model string) string { return strings.ToLower(nodeID + "/" + model) }

// accessFor returns the key's restrictions; nil for unknown keys (auth already ran) and unrestricted keys.
func (g *Gateway) accessFor(key string) *access {
	k := g.Store.APIKeyByValue(key)
	if k == nil || !k.Access.Restricted {
		return nil
	}
	a := &access{combos: map[string]bool{}, models: map[string]bool{}}
	for _, e := range k.Access.Allow {
		if c, err := g.Store.ComboByName(e); err == nil {
			a.combos[strings.ToLower(c.Name)] = true
			continue
		}
		if n, m, err := g.resolve(e); err == nil {
			a.models[canon(n.ID, m)] = true
		}
	}
	return a
}

func (a *access) allowsCombo(name string) bool { return a == nil || a.combos[strings.ToLower(name)] }

func (a *access) allowsModel(n *db.Node, model string) bool {
	return a == nil || a.models[canon(n.ID, model)]
}

func deniedMessage(requested string) string {
	return `This API key is not allowed to use model "` + requested + `"`
}
