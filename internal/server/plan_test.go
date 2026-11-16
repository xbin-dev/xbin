package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// planStub stands for the broker in a server-only world: it describes no
// line of its own (agent.start is the server's).
type planStub struct{}

func (planStub) PreviewChange(auth.Principal, delegation.ChangeOp) (delegation.ChangePreview, error) {
	return delegation.ChangePreview{}, errors.New("no such kind of change")
}
func (planStub) PreviewPlanned(auth.Principal, delegation.ChangeOp, []delegation.PlannedTile) (delegation.ChangePreview, error) {
	return delegation.ChangePreview{}, errors.New("no such kind of change")
}
func (planStub) AgentLineAllows(string, string, string, string) error { return nil }

// An agent.start line of a plan is a single start, on the review: the
// coding agent, the mode and whether it asks are xbind's, the brief is the
// line's own argument; a mode that doesn't ask is a warned line; applying
// it is the person's own act and needs no admin; it starts under the
// conversation that proposed it — and that conversation's taint refuses it
// exactly as it refuses a single start, when it is described and when it
// is applied.
func TestPlanStartsATileAgent(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	alice := delegateCred("d-alice")
	sid := w.s.Auth.NewSession("alice", "")
	w.s.Auth.TestAgeSession(sid, 11*time.Minute, 0) // no fresh sign-in: a personal line asks for none
	own := func() cred {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	}
	plan := func(items string) (int, delegation.ChangeSet, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":`+items+`}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		if full, ok := w.store.ChangeSet("alice", cs.ID); ok {
			cs = full // as xbin's chrome reads it for the person (the agent's own answer says less: setFor)
		}
		return rec.Code, cs, rec.Body.String()
	}
	apply := func(id string) delegation.ChangeSet {
		t.Helper()
		rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+id+"/approve", `{"items":[1]}`, own())
		if rec.Code != 200 {
			t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
		}
		return decode[delegation.ChangeSet](t, rec)
	}
	const onX = `[{"kind":"agent.start","args":{"tile":"apps/x","brief":"Goal: fix the bug.","harness":"claude"},"why":"it is broken"}]`

	code, cs, body := plan(onX)
	if code != 200 || len(cs.Items) != 1 {
		t.Fatalf("plan: %d %s", code, body)
	}
	it := cs.Items[0]
	var start map[string]any
	_ = json.Unmarshal(it.Start, &start)
	if it.Line != `Start a tile agent in apps/x — Claude Code, in its mode "default"` || it.Admin || it.Blocked != "" || len(it.Warnings) != 0 ||
		start["asks"] != true || start["prompt"] != "Goal: fix the bug." || start["provider"] != "claude" || it.UndoByHand == "" || cs.Session == "" {
		t.Fatalf("the start line: %+v (start %v)", it, start)
	}
	// only its person applies it — and it reaches the open with every gate passed (503: no bx in this fixture)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, alice); rec.Code != 403 {
		t.Fatalf("the agent applied its own plan: %d", rec.Code)
	}
	if done := apply(cs.ID); done.Items[0].Result != delegation.ItemFailed || !strings.Contains(done.Items[0].Error, "bx") {
		t.Fatalf("applying the start: %+v", done.Items[0])
	}

	// a mode that doesn't ask: the danger wording, as a warning (the review starts it unticked)
	_, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude","mode":"bypassPermissions"}}]`)
	if it := cs.Items[0]; len(it.Warnings) != 1 || !strings.HasPrefix(it.Warnings[0], "ACTS WITHOUT ASKING: ") || !strings.Contains(it.Line, "bypassPermissions") {
		t.Fatalf("a start in a mode that doesn't ask: %+v", it)
	}
	// a harness command is nobody's to approve
	if _, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":" /model opus"}}]`); !strings.Contains(cs.Items[0].Blocked, "its own commands") {
		t.Fatalf("a brief that is a harness command: %+v", cs.Items[0])
	}
	// the org's rule for the tile
	w.policy.set(func(d *dPolicy) { d.deny = map[string]error{"apps/x": errors.New("Sales lets no agent in")} })
	if _, cs, _ = plan(onX); !strings.Contains(cs.Items[0].Blocked, "Sales lets no agent in") {
		t.Fatalf("a start an org's rule refuses: %+v", cs.Items[0])
	}
	w.policy.set(func(d *dPolicy) { d.deny = nil })

	// the taint: proposed clean, then the conversation reads a Finance tile — applying it is refused
	_, clean, _ := plan(onX)
	if rec := w.do(w.full, "GET", "/api/xbin/components/apps/mail", "", alice); rec.Code != 200 {
		t.Fatalf("read apps/mail: %d", rec.Code)
	}
	if done := apply(clean.ID); done.Items[0].Result != delegation.ItemFailed || !strings.Contains(done.Items[0].Error, "Finance allows private models only") {
		t.Fatalf("a tainted conversation's plan started a tile agent outside Finance: %+v", done.Items[0])
	}
	// …and a new proposal says so before anyone is asked
	if _, cs, _ = plan(onX); !strings.Contains(cs.Items[0].Blocked, "Finance") {
		t.Fatalf("the start line of a tainted conversation: %+v", cs.Items[0])
	}
	// a plan whose conversation is over starts nothing
	_, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/mail","brief":"go","harness":"claude"}}]`)
	if cs.Items[0].Blocked != "" {
		t.Fatalf("a start on Finance's own tile: %+v", cs.Items[0])
	}
	if ok, err := w.store.DeleteAgentSession("alice", dHolder, cs.Session); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if done := apply(cs.ID); done.Items[0].Result != delegation.ItemFailed || !strings.Contains(done.Items[0].Error, "conversation that proposed this is over") {
		t.Fatalf("a start for a conversation that is over: %+v", done.Items[0])
	}
}

// A line described before its tile existed is held to its sentence and to
// the warnings its person saw: a warning that appears when the tile is
// there is not one they approved.
func TestDeferredLineStaleness(t *testing.T) {
	shown := delegation.ChangeItem{Line: "Bind apps/a's mcp to apps/b", Warnings: []string{"SPEND: x"}, Deferred: true}
	for _, c := range []struct {
		name  string
		pv    delegation.ChangePreview
		stale bool
	}{
		{"the same", delegation.ChangePreview{Line: shown.Line, Warnings: []string{"SPEND: x"}, Effects: []string{"anything"}}, false},
		{"a warning less", delegation.ChangePreview{Line: shown.Line}, false},
		{"a warning it never showed", delegation.ChangePreview{Line: shown.Line, Warnings: []string{"SPEND: x", "PUBLIC: y"}}, true},
		{"another sentence", delegation.ChangePreview{Line: "Bind apps/a's mcp to apps/c", Warnings: []string{"SPEND: x"}}, true},
	} {
		if got := lineStale(shown, c.pv); got != c.stale {
			t.Errorf("%s: stale %v, want %v", c.name, got, c.stale)
		}
	}
	// a line described against what was there is held to all of it
	pv := delegation.ChangePreview{Line: "l", Before: json.RawMessage(`{"a":1}`), Effects: []string{"e"}}
	now := delegation.ChangeItem{Line: "l", Basis: changeBasis(pv)}
	moved := pv
	moved.Before = json.RawMessage(`{"a":2}`)
	if lineStale(now, pv) || !lineStale(now, moved) {
		t.Error("a line described against the tile itself isn't held to its before")
	}
}
