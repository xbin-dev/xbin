package agentpolicy

import (
	"slices"
	"strings"
)

// Layer is one set of rules a tile falls under: the workspace's bounds
// (ID "") or one of the tile's orgs — an org's own rules, or the rules a
// workspace admin pinned on it (Pinned), which its admins can narrow below
// and never loosen. Both are asked, so the pin holds whatever the org says.
type Layer struct {
	ID    string // the org's id; "" for the workspace
	Name  string // the org's name, for reasons
	Rules Rules
	// Pinned: the rules a workspace admin pinned on the org.
	Pinned bool
	// Via is the share that brought the tile under the org — the pattern
	// or path of the org's share that covers it; "" when the org owns it.
	Via string
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
	// Pinned: the rule is one a workspace admin pinned on the org (the
	// org's admins can't loosen it); Via: the share that put the tile
	// under the org, when it doesn't own it. The reason says both.
	Pinned bool   `json:"pinned,omitempty"`
	Via    string `json:"via,omitempty"`
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
	return Decision{Reason: reason + l.note(), By: by, Rule: rule, Pinned: l.Pinned, Via: l.Via}
}

// note is what a reason adds about the layer: that a workspace admin
// pinned the rule, and the share that put the tile under the org — so a
// rule that surprises (a `*` share, a rule the org's admins never set) can
// be traced.
func (l Layer) note() string {
	var notes []string
	if l.Pinned {
		notes = append(notes, "pinned by a workspace admin")
	}
	if l.Via != "" {
		notes = append(notes, "this tile is under "+l.who()+" by its share "+l.Via)
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, "; ") + ")"
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

// Who serves a tile agent session's model calls, for Brief.
const (
	ServedUnknown   = ""          // not started yet: a start
	ServedUnsettled = "unsettled" // running, and nothing says who serves it (no connector here, or not settled yet)
	ServedConnector = "connector" // the LLM connector, and nothing else can
	ServedOffered   = "offered"   // the connector by default; the harness may leave it
	ServedOwn       = "own"       // the person's own sign-in: a model xbind can't see
)

// Brief answers whether a conversation holding data under the rules
// `from` (one org's layers) may send it to a tile agent session bound by
// the rules `to` (its tile's layers), served as `served` on `model` (nil:
// not known). Every layer of `from` with a model rule other than "any"
// must hold for the session, one of two ways: the session's tile is under
// that very layer — its sessions run on the connector alone, which asks
// the rule for every call — or the session is on the connector, nothing
// lets it leave, and its model satisfies the rule. A session on the
// person's own sign-in can't be vouched for: xbind doesn't see its model.
func Brief(from, to []Layer, served string, model *ModelRef) Decision {
	for _, l := range from {
		rule := modelRule(l.Rules.Models)
		if rule == "" {
			continue
		}
		bound := slices.ContainsFunc(to, func(t Layer) bool { return t.ID == l.ID && t.Pinned == l.Pinned && modelRule(t.Rules.Models) == rule })
		switch {
		case bound && (served == ServedUnknown || served == ServedConnector):
			continue // the session's own tile is under the rule: the connector asks it for every call
		case served == ServedConnector && model != nil && modelRefusal(l.Rules.Models, model) == "":
			continue
		}
		why := "the session's model isn't known to satisfy that"
		switch {
		case served == ServedOwn:
			why = "the session runs on its person's own sign-in, a model xbind can't see"
		case served == ServedOffered:
			why = "nothing holds the session to the LLM connector"
		case served == ServedConnector && model != nil:
			why = "the session is on " + model.ID()
		case served == ServedUnknown:
			why = "the session's tile isn't under that rule, so nothing holds its model to it"
		}
		return l.deny("models", l.who()+" "+rule+": "+why)
	}
	return Decision{Allowed: true}
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

// SessionLLM is how the model calls of an agent session on one tile are
// served: the credential mode in force there, who serves the session once
// the person's saved choice is applied, and the models the tile's rules
// let it use.
//
// An org's model rule binds the sessions on its tiles, not only the
// workspace agent: a harness on a person's own sign-in sends the tile's
// code to a model xbind can't see. So wherever a layer's model rule is
// anything but "any", the mode is connector-only — the session's calls go
// through the LLM connector, which asks ModelAllowed for each — and a
// harness that can't run on the connector can't start there.
type SessionLLM struct {
	// Mode: own-first | connector-first | connector-only.
	Mode string `json:"mode"`
	// Use: "own", "connector" or "own-first" — the mode with the person's
	// saved choice for the harness applied (Choose applies a session's).
	Use string `json:"use"`
	// Forced: connector-only; no choice changes it.
	Forced bool `json:"forced"`
	// Reason says why the mode is connector-only when a model rule made it
	// so ("Finance allows private models only"); By names that layer's org
	// (WorkspaceID for the bounds). Both empty when the workspace's own
	// mode is the reason, or nothing forces anything.
	Reason string `json:"reason,omitempty"`
	By     string `json:"by,omitempty"`

	layers []Layer
	saved  string
}

// Session resolves SessionLLM for a tile's layers, the workspace's
// credential mode and the person's saved choice for the harness ("" for
// none).
func Session(layers []Layer, workspaceMode, saved string) SessionLLM {
	mode, _ := CredentialMode(workspaceMode)
	s := SessionLLM{Mode: mode, layers: layers, saved: saved}
	for _, l := range layers {
		if why := modelRule(l.Rules.Models); why != "" {
			d := l.deny("models", l.who()+" "+why)
			s.Mode, s.Reason, s.By = CredentialsConnectorOnly, d.Reason, d.By
			break
		}
	}
	s.Use, s.Forced = ResolveCredentials("", saved, s.Mode)
	return s
}

// modelRule is a model rule as a sentence after the layer's name; "" for
// any.
func modelRule(rule Allow) string {
	switch ids, listed := rule.List(); {
	case rule.IsPrivate():
		return "allows private models only"
	case listed && len(ids) == 0:
		return "allows no models"
	case listed:
		return "allows these models only: " + strings.Join(ids, ", ")
	}
	return ""
}

// Choose is who serves the session when it states its own choice ("own",
// "connector" or ""): "own", "connector" or "own-first".
func (s SessionLLM) Choose(session string) string {
	use, _ := ResolveCredentials(session, s.saved, s.Mode)
	return use
}

// Restricted reports that some layer's model rule narrows the models (the
// mode is then connector-only).
func (s SessionLLM) Restricted() bool { return s.Reason != "" }

// ModelAllowed is nil when every layer's model rule lets the session use
// m, else the refusal a person reads.
func (s SessionLLM) ModelAllowed(m ModelRef) error {
	return Check(s.layers, Ask{Model: &m}).Err()
}

// HarnessRefusal is why an agent session of harness can't start under s
// ("" when it can): under connector-only the harness has to run on the LLM
// connector, and connector says why this one can't (nil: it can; a nil
// func: there is no connector on this xbind).
func (s SessionLLM) HarnessRefusal(harness string, connector func(harness string) error) string {
	if s.Mode != CredentialsConnectorOnly {
		return ""
	}
	why := "this xbind has no LLM connector set up"
	if connector != nil {
		err := connector(harness)
		if err == nil {
			return ""
		}
		why = err.Error()
	}
	if s.Reason != "" {
		return s.Reason + ", so a coding agent here runs on the workspace's LLM connector — and " + harness + " can't: " + why
	}
	return "This workspace runs coding agents on its LLM connector only — and " + harness + " can't: " + why
}
