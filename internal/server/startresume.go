package server

// startresume.go — the mode of a resumed tile agent (D251 review M2). A
// resume reopens a session the workspace agent started, on its history's
// coding agent and model; its mode is the card's: the one it ran in last,
// unless that is a bypass mode — which a session the workspace agent
// starts or resumes never opens in unless its person picks it, on that
// approval: it resumes in its coding agent's work mode then, and the card
// says what it was in (preview.session.resumeModeNotTaken).

import (
	"maps"
	"net/http"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// resumeChoice is what a resume's approval may pick: the one coding agent
// it resumes on, its model as it was, and any of its modes.
func resumeChoice(prov agent.Provider, model string) harnessChoice {
	return harnessChoice{ID: prov.ID, Name: prov.Name, DefaultMode: workMode(prov), Modes: modesOf(prov), Models: []modelChoice{{ID: model}}}
}

// resumePick is the person's pick on the approval of a resume: its mode,
// nothing else; nil, nil: as the card shows it.
func resumePick(req delegation.Request, shown map[string]any, b pickBody) (*delegation.StartChoice, map[string]any, int, string) {
	if b.Harness != nil || b.Model != nil || b.Remember || b.Continue != nil {
		return nil, nil, http.StatusBadRequest, "a resumed session keeps its coding agent and model: only its mode is picked again"
	}
	if why := seenRefusal(b.Seen, shown); why != "" {
		return nil, nil, http.StatusConflict, why
	}
	if b.Mode == nil {
		return nil, nil, 0, ""
	}
	id, _ := shown["provider"].(string)
	prov, ok := agent.Lookup(id)
	known := *b.Mode == "" && noDefaultMode(id)
	for _, m := range prov.Modes {
		known = known || m.ID == *b.Mode
	}
	if !ok || !known {
		return nil, nil, http.StatusBadRequest, "mode " + *b.Mode + " isn't one of " + id + "'s"
	}
	model, _ := shown["model"].(string)
	c := delegation.StartChoice{Provider: id, Mode: *b.Mode, Model: model}
	redrawn := maps.Clone(shown)
	redrawn["mode"], redrawn["asks"] = c.Mode, prov.Asks(c.Mode)
	out := maps.Clone(req.Preview)
	out["session"] = redrawn
	return &c, out, 0, ""
}
