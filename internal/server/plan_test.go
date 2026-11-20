package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/users"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
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
	// (in Claude Code's work mode, which acts without asking: a fact on the line, no warning — an ordinary line,
	// ticked like the others, D251)
	if it.Line != `Start a tile agent in apps/x — Claude Code, in its mode "auto", which acts without asking` || it.Admin || it.Blocked != "" || len(it.Warnings) != 0 ||
		start["asks"] != false || start["prompt"] != "Goal: fix the bug." || start["provider"] != "claude" || it.UndoByHand == "" || cs.Session == "" ||
		!strings.Contains(strings.Join(it.Effects, " "), "It edits files and runs commands in this tile's sandbox without asking you. You approve what your agent tells it.") {
		t.Fatalf("the start line: %+v (start %v)", it, start)
	}
	// only its person applies it — and it reaches the open with every gate passed (503: no bx in this fixture)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, alice); rec.Code != 403 {
		t.Fatalf("the agent applied its own plan: %d", rec.Code)
	}
	if done := apply(cs.ID); done.Items[0].Result != delegation.ItemFailed || !strings.Contains(done.Items[0].Error, "bx") {
		t.Fatalf("applying the start: %+v", done.Items[0])
	}

	// a bypass mode, suggested by the agent: never taken — only its person names one — the line says what it left
	// out; any other mode it suggests is the line's (D251)
	_, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude","mode":"bypassPermissions"}}]`)
	if it := cs.Items[0]; len(it.Warnings) != 0 || !strings.Contains(it.Line, `"auto"`) || !strings.Contains(strings.Join(it.Effects, " "), `suggested the mode "bypassPermissions", which skips every check: not taken`) {
		t.Fatalf("a start the agent suggested a bypass mode for: %+v", it)
	}
	_, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude","mode":"default"}}]`)
	if it := cs.Items[0]; len(it.Warnings) != 0 || !strings.Contains(it.Line, `"default", which asks before it edits or runs anything`) {
		t.Fatalf("a start the agent suggested its asking mode for: %+v", it)
	}
	// …and a mode remembered for the tile is the line's (D251), an ordinary line
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "claude", Mode: "acceptEdits"}, 1); err != nil {
		t.Fatal(err)
	}
	_, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go"}}]`)
	if it := cs.Items[0]; len(it.Warnings) != 0 || !strings.Contains(it.Line, `"acceptEdits", which acts without asking`) {
		t.Fatalf("a start after acceptEdits was remembered for the tile: %+v", it)
	}
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "claude"}, 2); err != nil {
		t.Fatal(err)
	}
	// a harness command is nobody's to approve
	if _, cs, _ = plan(`[{"kind":"agent.start","args":{"tile":"apps/x","brief":" /model opus"}}]`); !strings.Contains(cs.Items[0].Blocked, "its own commands") {
		t.Fatalf("a brief that is a harness command: %+v", cs.Items[0])
	}
	// a dry run describes the plan and proposes nothing: the same lines, numbered, and of a line xbind can't apply
	// the agent reads that it can't — in xbind's one sentence, as on a proposal — so it can mend the plan before
	// anything is in front of its person. No set is kept and nobody is told.
	sets := func() int { all, _ := w.store.ChangeSets("alice"); return len(all) }
	before := sets()
	rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","dryRun":true,"items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"Goal: fix the bug.","harness":"claude"},"why":"it is broken"},`+
		`{"kind":"agent.start","args":{"tile":"apps/x","brief":" /model opus"},"why":"x"}]}`, alice)
	var dry struct {
		delegation.ChangeSet
		DryRun bool `json:"dryRun"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dry)
	if rec.Code != 200 || !dry.DryRun || dry.ID != "" || len(dry.Items) != 2 || dry.Items[0].N != 1 || dry.Items[1].N != 2 ||
		dry.Items[0].Line != `Start a tile agent in apps/x — Claude Code, in its mode "auto", which acts without asking` || dry.Items[0].Blocked != "" || dry.Items[1].Blocked != lineBlockedForAgent {
		t.Fatalf("a plan's dry run: %d %s", rec.Code, rec.Body.String())
	}
	if sets() != before {
		t.Fatalf("a dry run kept a change set: %d → %d", before, sets())
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
		{"described from something else (a start's network, a template)", delegation.ChangePreview{Line: shown.Line, Warnings: []string{"SPEND: x"}, Pin: "other"}, true},
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

// A plan is no way around the scope a person gave a conversation, nor to a
// tile hidden from it: a start on a tile outside the scope is refused when
// it is described and — the scope narrowed since — when it is applied; and
// a single start is held to the same (it reached the open before).
func TestPlanLinesStayInsideTheConversation(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	alice := delegateCred("d-alice")
	sid := w.s.Auth.NewSession("alice", "")
	own := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	id, _ := testHandles.Load("d-alice")
	scope := func(body string) {
		t.Helper()
		if rec := w.do(w.full, "PATCH", "/api/xbin/delegate/sessions/"+id.(string), body, alice); rec.Code != 200 {
			t.Fatalf("scope %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	plan := func(tile string) delegation.ChangeSet {
		t.Helper()
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"`+tile+`","brief":"go","harness":"claude"}}]}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		full, ok := w.store.ChangeSet("alice", cs.ID)
		if rec.Code != 200 || !ok {
			t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
		}
		return full
	}
	open := plan("apps/x") // proposed while the conversation sees everything
	if open.Items[0].Blocked != "" {
		t.Fatalf("a start inside the scope: %+v", open.Items[0])
	}
	scope(`{"scope":{"orgs":["fin"],"tiles":[]}}`) // now limited to Finance: apps/mail is in, apps/x is out
	if cs := plan("apps/x"); !strings.Contains(cs.Items[0].Blocked, "hidden by you") {
		t.Fatalf("a start outside the conversation's scope is proposed: %+v", cs.Items[0])
	}
	if cs := plan("apps/mail"); cs.Items[0].Blocked != "" {
		t.Fatalf("a start inside the scope: %+v", cs.Items[0])
	}
	rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+open.ID+"/approve", `{"items":[1]}`, own)
	done := decode[delegation.ChangeSet](t, rec)
	if rec.Code != 200 || done.Items[0].Result != delegation.ItemFailed || !strings.Contains(done.Items[0].Error, "hidden by you") {
		t.Fatalf("a start applied after the scope was narrowed past it: %d %+v", rec.Code, done.Items[0])
	}
	// the single start, by the same rule: its dry run, its request and the call
	const onX = `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"go"}`
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", onX, alice, header(HeaderDryRun, "1")); !strings.Contains(rec.Body.String(), "hidden by you") {
		t.Errorf("the dry run of a single start outside the scope: %s", rec.Body.String())
	}
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": onX})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), "hidden by you") {
		t.Errorf("a request for a single start outside the scope: %d %s", rec.Code, rec.Body.String())
	}
}

// A start is pinned to everything it was shown to resolve to — the network
// and the API access too, not only the coding agent and mode its sentence
// names (review finding 8): if the person's preference (or, for a tile the
// plan makes, its org's rules) gives it another network by the time it is
// applied, the line is stale and nothing starts.
func TestPlanStartIsPinnedToWhatWasShown(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	alice := delegateCred("d-alice")
	sid := w.s.Auth.NewSession("alice", "")
	own := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{Net: "none"} })
	rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude"}}]}`, alice)
	var cs delegation.ChangeSet
	_ = json.Unmarshal(rec.Body.Bytes(), &cs)
	full, _ := w.store.ChangeSet("alice", cs.ID)
	var start map[string]any
	_ = json.Unmarshal(full.Items[0].Start, &start)
	if rec.Code != 200 || start["net"] != "none" || full.Items[0].Pin == "" {
		t.Fatalf("the start as shown: %d %v pin %q", rec.Code, start, full.Items[0].Pin)
	}
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{Net: "internet"} }) // meanwhile: the whole internet
	done := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, own))
	if done.Items[0].Result != delegation.ItemStale {
		t.Fatalf("a start whose network changed since it was shown: %+v", done.Items[0])
	}
}

// A plan's start line gets a saved sign-in only when the line its person
// approved NAMES it (D230) — the one named then, bound as a ticket binds a
// single start's: not whatever the person's default is by the time the
// plan is applied, and none at all where the line named none (a tile an
// earlier line creates is described before it exists: no sign-in can be
// shown for it, so none is used). The open checks the named one again.
func TestPlanStartUsesOnlyTheSignInItsLineNamed(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	alice := delegateCred("d-alice")
	sid := w.s.Auth.NewSession("alice", "")
	own := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	now := "" // the saved sign-in a start of alice's is offered at this moment ("": none)
	was := startSignIn
	var line *term.SigninTrust // the trust line a start of alice's carries at this moment (nil: none)
	startSignIn = func(*Server, auth.Principal, string, agent.Provider, *term.ProviderLLM, bool) (string, string, *term.SigninTrust) {
		return "needed", now, line // (what it runs on as things are; the sign-in the sheet offers; its trust line)
	}
	t.Cleanup(func() { startSignIn = was })
	var opened []term.DelegateStart
	w.s.PlanStarter = func(ctx context.Context, _ auth.Principal, _ []byte) (string, int, string) {
		d, _ := ctx.Value(delegateStartKey{}).(*term.DelegateStart)
		if d == nil {
			t.Error("a plan's start reached the open with no authorisation")
			return "", 500, "no authorisation"
		}
		if !d.Picked {
			// unpicked, the open runs what the body names — the agent's suggestion — not the reviewed line (D250 review
			// H1; this stub reads no further than the authorisation: TestPlanStartRunsTheReviewedLine opens for real)
			t.Errorf("a plan's start reached the open unpicked: %+v", *d)
		}
		opened = append(opened, *d)
		return "sess", 200, ""
	}
	// OpenCode — no mode that asks (D250 review M2) — is an ordinary choice (D251): the agent's suggestion of it is
	// the line's, an ordinary line in its own mode, no warning; so is the person's own choice of it for the tile
	{
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"opencode"}}]}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		full, ok := w.store.ChangeSet("alice", cs.ID)
		if rec.Code != 200 || !ok {
			t.Fatalf("a plan that starts OpenCode: %d %s", rec.Code, rec.Body.String())
		}
		it := full.Items[0]
		if it.Line != `Start a tile agent in apps/x — OpenCode, in its own default mode, which acts without asking` || it.Blocked != "" || len(it.Warnings) != 0 {
			t.Fatalf("the line OpenCode was suggested for: %q blocked %q warnings %q effects %q", it.Line, it.Blocked, it.Warnings, it.Effects)
		}
		opened = nil
		done := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, own))
		if done.Items[0].Result != delegation.ItemApplied || len(opened) != 1 || opened[0].Provider != "opencode" || opened[0].Mode != "" || !opened[0].Approved {
			t.Fatalf("applying it: %+v (error %q; opens %+v)", done.Items[0].Result, done.Items[0].Error, opened)
		}
		if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "opencode", Mode: "build"}, 3); err != nil {
			t.Fatal(err)
		}
		rec = w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go"}}]}`, alice)
		var cs2 delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs2)
		full2, _ := w.store.ChangeSet("alice", cs2.ID)
		if it := full2.Items; len(it) != 1 || len(it[0].Warnings) != 0 || it[0].Line != `Start a tile agent in apps/x — OpenCode, in its mode "build", which acts without asking` {
			t.Fatalf("a line on the person's OpenCode: %+v", full2.Items)
		}
		opened = nil
		done2 := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs2.ID+"/approve", `{"items":[1]}`, own))
		if done2.Items[0].Result != delegation.ItemApplied || len(opened) != 1 || opened[0].Provider != "opencode" || opened[0].Mode != "build" || !opened[0].Picked {
			t.Fatalf("the line, ticked: %+v (error %q; opens %+v)", done2.Items[0].Result, done2.Items[0].Error, opened)
		}
		if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"agentSessions":{"tiles":null}}`)); err != nil {
			t.Fatal(err)
		}
		// …and a start of a coding agent that HAS a default is never opened with no mode
		if noDefaultMode("claude") || noDefaultMode("codex") || noDefaultMode("no-such-agent") || !noDefaultMode("opencode") {
			t.Error("which coding agents open with no mode named")
		}
	}
	// the sign-in a line OFFERS is its person's to tick when they apply it —
	// unticked, the session has none; ticked, it is the one the line showed,
	// not whatever their default is by then
	for _, c := range []struct{ name, shown, atApply, body, want string }{
		{"offered, not ticked: none", "sg-work", "sg-work", `{"items":[1]}`, ""},
		{"ticked: the one the line offered, though the default moved", "sg-work", "sg-other", `{"items":[1],"signin":[1]}`, "sg-work"},
		{"ticked, unchanged", "sg-work", "sg-work", `{"items":[1],"signin":[1]}`, "sg-work"},
		{"none offered: none used, though one exists by Apply", "", "sg-work", `{"items":[1]}`, ""},
	} {
		now = c.shown
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude"}}]}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		full, ok := w.store.ChangeSet("alice", cs.ID)
		if rec.Code != 200 || !ok {
			t.Fatalf("%s: plan: %d %s", c.name, rec.Code, rec.Body.String())
		}
		var start struct{ SignIn, SignInID string }
		_ = json.Unmarshal(full.Items[0].Start, &start)
		if start.SignInID != c.shown || start.SignIn == "saved" || len(full.Items[0].Warnings) != 0 || full.Items[0].Wait != nil {
			t.Fatalf("%s: the line as its person is shown it: %+v (warnings %v, wait %v)", c.name, start, full.Items[0].Warnings, full.Items[0].Wait)
		}
		now, opened = c.atApply, nil
		done := decode[delegation.ChangeSet](t, w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", c.body, own))
		if done.Items[0].Result != delegation.ItemApplied || len(opened) != 1 {
			t.Fatalf("%s: applying: %+v (%d opens)", c.name, done.Items[0], len(opened))
		}
		if d := opened[0]; d.Signin != c.want || !d.Approved || d.Provider != "claude" || d.TrustAsked {
			t.Errorf("%s: the open was authorised for the sign-in %q — the line offered %q", c.name, d.Signin, c.shown)
		}
	}
	// a tick where none was offered, or for a line that wasn't chosen, applies nothing
	{
		now = ""
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude"}}]}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		opened = nil
		for _, body := range []string{`{"items":[1],"signin":[1]}`, `{"items":[1],"signin":[2]}`, `{"items":[1],"trust":[1],"shown":"signin+home"}`} {
			if rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", body, own); rec.Code != 400 || len(opened) != 0 {
				t.Fatalf("%s: %d %s (%d opens)", body, rec.Code, rec.Body.String(), len(opened))
			}
		}
	}
	// a start line on a tile whose source isn't only hers carries the trust
	// line under the sign-in's: the sign-in ticked without it, the open is
	// one that asked and nothing is recorded; both ticked (signin: [n],
	// trust: [n]), her trust is on record before the open — for the tile as
	// the line showed it: one that changed since applies nothing (409)
	w.s.Signins = signins.New(signins.Options{Dir: filepath.Join(t.TempDir(), "signins"), AllowPlaintext: true})
	owner := "org:sales"
	var editors []string
	w.s.Term.Signins, w.s.Term.SigninDev = w.s.Signins, true
	w.s.Term.SigninSource = testSource(&owner, &editors)
	question := func() *term.SigninTrust {
		src, _ := w.s.Term.SigninSource("alice", "apps/x")
		return w.s.Term.SigninQuestion(src, w.s.Signins.Policy().Trust)
	}
	for _, c := range []struct {
		name, body string
		change     func()
		status     int
		signin     string
		trusted    int
	}{
		{"neither ticked", `{"items":[1]}`, nil, 200, "", 0},
		{"the sign-in ticked, the trust line not", `{"items":[1],"signin":[1]}`, nil, 200, "sg-work", 0},
		{"the trust line alone", `{"items":[1],"trust":[1],"shown":"signin+home"}`, nil, 400, "", 0},
		{"the tile changed since the line was drawn", `{"items":[1],"signin":[1],"trust":[1],"shown":"signin+home"}`, func() { editors = []string{"mallory"} }, 409, "", 0},
		{"a page that doesn't say what its trust line showed", `{"items":[1],"signin":[1],"trust":[1]}`, nil, 409, "", 0},
		{"both ticked", `{"items":[1],"signin":[1],"trust":[1],"shown":"signin+home"}`, nil, 200, "sg-work", 1},
	} {
		editors = nil
		now, line = "sg-work", question()
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", `{"title":"t","items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"claude"}}]}`, alice)
		var cs delegation.ChangeSet
		_ = json.Unmarshal(rec.Body.Bytes(), &cs)
		full, ok := w.store.ChangeSet("alice", cs.ID)
		if rec.Code != 200 || !ok {
			t.Fatalf("%s: plan: %d %s", c.name, rec.Code, rec.Body.String())
		}
		var start struct {
			SignInID    string
			SignInTrust *term.SigninTrust
		}
		_ = json.Unmarshal(full.Items[0].Start, &start)
		if start.SignInID != "sg-work" || start.SignInTrust == nil || start.SignInTrust.Owner != "sales" || start.SignInTrust.Basis == "" {
			t.Fatalf("%s: the line as shown: %s", c.name, full.Items[0].Start)
		}
		if c.change != nil {
			c.change()
		}
		opened = nil
		rec = w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", c.body, own)
		if rec.Code != c.status {
			t.Fatalf("%s: apply: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if c.status != 200 {
			if len(opened) != 0 || len(w.s.Signins.Trusted("alice")) != 0 {
				t.Fatalf("%s: refused, yet %d open(s), trusted %+v", c.name, len(opened), w.s.Signins.Trusted("alice"))
			}
			continue
		}
		done := decode[delegation.ChangeSet](t, rec)
		if done.Items[0].Result != delegation.ItemApplied || len(opened) != 1 || !opened[0].TrustAsked || opened[0].Signin != c.signin {
			t.Fatalf("%s: applying: %+v (opens %+v)", c.name, done.Items[0], opened)
		}
		if tr := w.s.Signins.Trusted("alice"); len(tr) != c.trusted || c.trusted == 1 && (tr[0].Via != "plan" || tr[0].Owner != "org:sales") {
			t.Errorf("%s: her trusted tiles: %+v", c.name, tr)
		}
	}
}

// A start on a tile the same plan creates as the person's own names their
// default saved sign-in (the default is on) — "names none, so gets none"
// is gone; one the plan would make an org's, or that someone else could
// change, names none: it has no identity to trust yet.
func TestPlannedTileStartNamesTheDefaultSignIn(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	w.s.Signins = signins.New(signins.Options{Dir: filepath.Join(t.TempDir(), "signins"), AllowPlaintext: true})
	w.s.Term.Signins, w.s.Term.SigninDev = w.s.Signins, true
	w.s.Term.SigninSource = testSource(nil, nil)
	var editors []string
	w.s.SigninSourcePlanned = func(user, path, owner string) signins.Source {
		return signins.Source{Tile: path, Owner: owner, OwnerKind: "you", NotOwn: owner != "user:"+user, Editors: editors}
	}
	sg, _, err := w.s.Signins.Save("alice", "claude", "Personal", fakeSigninToken, false)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := agent.Lookup("claude")
	p := auth.Principal{UserID: "alice", Via: "delegate", Agent: "xbin/wsagent", User: &users.User{ID: "alice", Role: users.RoleUser}}
	if got := w.s.plannedSignIn(p, "apps/new", "user:alice", prov); got != sg.ID {
		t.Fatalf("a tile the plan makes hers: %q, want her default %q", got, sg.ID)
	}
	if got := w.s.plannedSignIn(p, "apps/new", "org:sales", prov); got != "" {
		t.Fatalf("a tile the plan makes an org's: %q", got)
	}
	editors = []string{"bob"}
	if got := w.s.plannedSignIn(p, "apps/new", "user:alice", prov); got != "" {
		t.Fatalf("a new tile bob's pattern lets him change: %q", got)
	}
	editors = nil
	if _, err := w.s.Signins.SetPolicy(false); err != nil {
		t.Fatal(err)
	}
	w.s.Term.SigninsOff = func() bool { return !w.s.Signins.Policy().Enabled }
	if got := w.s.plannedSignIn(p, "apps/new", "user:alice", prov); got != "" {
		t.Fatalf("saved sign-ins are off: %q", got)
	}
	_ = alice
}
