package gateway

import (
	"errors"
	"strings"
	"sync"

	"router-go/internal/db"
)

// target is one concrete upstream choice: a node plus the model name it expects.
type target struct {
	node  *db.Node
	model string
	label string // what the client/combo called it, used for usage records
	err   string // member could not be resolved
}

var errNoModels = &resolveError{msg: "combo has no models"}

type resolveError struct{ msg string }

func (e *resolveError) Error() string { return e.msg }

// plan turns the requested model string into an ordered target list. A bare name that matches a combo expands to its
// members (rotated for round-robin); everything else is a single target.
func (g *Gateway) plan(requested string) (combo *db.Combo, targets []target, err error) {
	if !strings.Contains(requested, "/") {
		name := g.maskedCombo(requested)
		if c, cerr := g.Store.ComboByName(name); cerr == nil {
			if len(c.Models) == 0 {
				return c, nil, errNoModels
			}
			members := c.Models
			if c.Strategy == "round-robin" {
				members = g.rotate(c.Name, members)
			}
			for _, m := range members {
				if n, model, rerr := g.resolve(m); rerr == nil {
					targets = append(targets, target{node: n, model: model, label: n.Prefix() + "/" + model})
				} else {
					msg := "model '" + m + "' of combo '" + c.Name + "' not found"
					var na *notAddedError
					if errors.As(rerr, &na) {
						msg = na.Error()
					}
					targets = append(targets, target{label: m, err: msg})
				}
			}
			return c, targets, nil
		}
	}
	n, model, rerr := g.resolve(requested)
	if rerr != nil {
		return nil, nil, rerr
	}
	return nil, []target{{node: n, model: model, label: n.Prefix() + "/" + model}}, nil
}

// maskedCombo follows an alias that masks a combo ("fast" -> combo "smart-coder"); any other name is returned as is.
// A real combo of that name always wins over an alias.
func (g *Gateway) maskedCombo(name string) string {
	if _, err := g.Store.ComboByName(name); err == nil {
		return name
	}
	if a, ok := g.Store.Alias(name); ok && !strings.Contains(a, "/") {
		return a
	}
	return name
}

type rotation struct{ idx, used int }

var rotations = struct {
	sync.Mutex
	m map[string]*rotation
}{m: map[string]*rotation{}}

// rotate starts the member list at the current round-robin position and advances it after `sticky` consecutive uses.
func (g *Gateway) rotate(name string, models []string) []string {
	sticky := 1
	if set, err := g.Store.Settings(); err == nil {
		if v, ok := set["comboStickyRoundRobinLimit"].(float64); ok && v >= 1 {
			sticky = int(v)
		}
	}
	return rotateSticky(name, models, sticky)
}

// rotateSticky starts the list at the current position of the named rotation, advancing after `sticky` uses.
func rotateSticky(name string, models []string, sticky int) []string {
	rotations.Lock()
	defer rotations.Unlock()
	r := rotations.m[name]
	if r == nil {
		r = &rotation{}
		rotations.m[name] = r
	}
	start := r.idx % len(models)
	if r.used++; r.used >= sticky {
		r.idx, r.used = (start+1)%len(models), 0
	} else {
		r.idx = start
	}
	return append(append([]string{}, models[start:]...), models[:start]...)
}
