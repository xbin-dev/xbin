package server

// delegate_review_test.go — the rules the independent review of D250 found
// unheld, each with a test that fails without its rule.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// A plan's agent.start line opens what its review showed (review H1): the
// coding agent, model and mode the chain resolved — the person's choice for
// the tile over the agent's suggestion in the line's arguments — never the
// suggestion the line's call body still carries; and a suggested mode that
// doesn't ask never runs from a plan, whose review has no pickers. The open
// is the real one (no PlanStarter stub reads past the body).
func TestPlanStartRunsTheReviewedLine(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	alice, own := delegateCred("d-alice"), w.cookie("alice")
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "fake", Model: "fake-fast"}, 1); err != nil {
		t.Fatal(err)
	}
	rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"Goal: a game.","harness":"fake","model":"fake-default","mode":"auto"}}]}`, alice)
	cs := decode[delegation.ChangeSet](t, rec)
	if full, ok := w.store.ChangeSet("alice", cs.ID); ok {
		cs = full
	}
	if rec.Code != 200 || len(cs.Items) != 1 {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	it := cs.Items[0]
	var start map[string]any
	_ = json.Unmarshal(it.Start, &start)
	if start["provider"] != "fake" || start["mode"] != "ask" || start["model"] != "fake-fast" || start["asks"] != true {
		t.Fatalf("the reviewed line: %v", start)
	}
	done := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, own))
	var out struct{ Session string }
	_ = json.Unmarshal(done.Items[0].Out, &out)
	if done.Items[0].Result != delegation.ItemApplied || out.Session == "" {
		t.Fatalf("applying the reviewed start: %+v", done.Items[0])
	}
	info, _ := w.s.Term.Info(out.Session)
	if info.Provider != "fake" || info.Mode != "ask" || info.Model != "fake-fast" {
		t.Fatalf("what opened: %s in mode %q on model %q — the review showed fake in ask on fake-fast", info.Provider, info.Mode, info.Model)
	}
	// an outranked coding agent: the line's suggestion is claude, the person's choice for the tile fake — fake opens
	rec = w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t2","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"Goal: more.","harness":"claude"}}]}`, alice)
	cs2 := decode[delegation.ChangeSet](t, rec)
	done2 := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs2.ID+"/approve", `{"items":[1]}`, own))
	var out2 struct{ Session string }
	_ = json.Unmarshal(done2.Items[0].Out, &out2)
	if info2, _ := w.s.Term.Info(out2.Session); done2.Items[0].Result != delegation.ItemApplied || info2.Provider != "fake" {
		t.Fatalf("a line whose suggestion the person's choice outranks: %+v (opened %s)", done2.Items[0], info2.Provider)
	}
	effects := strings.Join(it.Effects, " ")
	if !strings.Contains(effects, `suggested the mode "auto", which acts without asking: not taken`) || !strings.Contains(effects, "a single start's approval") {
		t.Fatalf("the line says what runs and where it is changed: %q", effects)
	}
}
