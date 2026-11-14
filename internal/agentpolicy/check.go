package agentpolicy

import (
	"slices"
	"strings"
)

// Layer is one set of rules a tile falls under: the workspace's bounds
// (ID "") or one of the tile's orgs.
type Layer struct {
	ID    string // the org's id; "" for the workspace
	Name  string // the org's name, for reasons
	Rules Rules
}

// WorkspaceID names the workspace layer in a Decision's By, and the
// group of tiles in no org in a connection view and a scope. No org can
// take it (users reserves the id).
const WorkspaceID = "workspace"

// Ask is one question for Check.
type Ask struct {
	// WorkspaceAgent: the call is a person's workspace agent's — the on/off
	// rule applies, and the model rule even when Model is nil (no model
	// chosen satisfies only "any").
	WorkspaceAgent bool
	// Harness, when set: an agent session of this harness.
	Harness string
	// Model, when set: the model that would see the tile's data.
	Model *ModelRef
}

// Decision is Check's answer: Allowed, or the first rule that refused —
// its reason as a person reads it, the layer it belongs to (an org's id,
// or WorkspaceID) and which rule (workspaceAgent, harnesses, models).
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	By      string `json:"by,omitempty"`
	Rule    string `json:"rule,omitempty"`
}

// Err is the refusal as an error, nil when allowed.
func (d Decision) Err() error {
	if d.Allowed {
		return nil
	}
	return refusal(d)
}

type refusal Decision

func (r refusal) Error() string { return r.Reason }

// Check asks every layer: all must allow (the strictest wins). The
// workspace's layer is asked first, then the orgs in the order given, so
// the reason is stable.
func Check(layers []Layer, a Ask) Decision {
	for _, l := range layers {
		if d := l.check(a); !d.Allowed {
			return d
		}
	}
	return Decision{Allowed: true}
}

func (l Layer) who() string {
	switch {
	case l.ID == "":
		return "This workspace"
	case l.Name != "":
		return l.Name
	}
	return l.ID
}

func (l Layer) deny(rule, reason string) Decision {
	by := l.ID
	if by == "" {
		by = WorkspaceID
	}
	return Decision{Reason: reason, By: by, Rule: rule}
}

func (l Layer) check(a Ask) Decision {
	r := l.Rules
	if a.WorkspaceAgent && r.WorkspaceAgent.IsOff() {
		return l.deny("workspaceAgent", l.who()+" has the workspace agent turned off")
	}
	if a.Harness != "" {
		if ids, ok := r.Harnesses.List(); ok && !slices.Contains(ids, a.Harness) {
			if len(ids) == 0 {
				return l.deny("harnesses", l.who()+" allows no coding agents")
			}
			return l.deny("harnesses", l.who()+" allows these coding agents only: "+strings.Join(ids, ", ")+" (not "+a.Harness+")")
		}
	}
	if a.Model != nil || a.WorkspaceAgent {
		if why := modelRefusal(r.Models, a.Model); why != "" {
			return l.deny("models", l.who()+" "+why)
		}
	}
	return Decision{Allowed: true}
}

// modelRefusal is why rule refuses m ("" when it allows it): the sentence
// after the layer's name.
func modelRefusal(rule Allow, m *ModelRef) string {
	switch {
	case rule.IsAny():
		return ""
	case rule.IsPrivate():
		if m != nil && m.Private {
			return ""
		}
		if m == nil {
			return "allows private models only, and no model is chosen"
		}
		return "allows private models only"
	}
	ids, _ := rule.List()
	if m != nil && (slices.Contains(ids, m.Upstream) || (m.Model != "" && slices.Contains(ids, m.ID()))) {
		return ""
	}
	if len(ids) == 0 {
		return "allows no models"
	}
	if m == nil {
		return "allows these models only: " + strings.Join(ids, ", ") + ", and no model is chosen"
	}
	return "allows these models only: " + strings.Join(ids, ", ")
}

// Harnesses is the harness rule of all layers together: any, or the ids
// every layer allows.
func Harnesses(layers []Layer) Allow {
	var out []string
	listed := false
	for _, l := range layers {
		ids, ok := l.Rules.Harnesses.List()
		if !ok {
			continue
		}
		if !listed {
			out, listed = ids, true
			continue
		}
		out = slices.DeleteFunc(out, func(id string) bool { return !slices.Contains(ids, id) })
	}
	if !listed {
		return Any()
	}
	return Only(out...)
}
