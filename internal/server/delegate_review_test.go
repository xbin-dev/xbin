package server

// delegate_review_test.go — the rules the independent review of D250 found
// unheld, each with a test that fails without its rule.

import (
	"encoding/json"
	"os"
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

// OpenCode has no mode xbin can vouch asks before it acts (D250 review M2;
// read from opencode 1.18.32: its plan agent denies edits but allows every
// command and fetch, and a tile's own .opencode config and plugins can
// loosen any agent) — so every mode of it is "acts without asking": none is
// a default that asks, a suggestion of it is never preselected, start-
// defaults never lets it start unasked, and the agent's messages to a
// session of it are each the person's to approve, whatever mode it opened in.
func TestOpenCodeHasNoAskingMode(t *testing.T) {
	oc, _ := agent.Lookup("opencode")
	if oc.Asks("plan") || oc.Asks("build") {
		t.Fatal("an OpenCode mode is said to ask")
	}
	ids := func(ms []modeChoice) (out []string) {
		for _, m := range ms {
			if m.Asks {
				t.Errorf("mode %q is said to ask", m.ID)
			}
			out = append(out, m.ID)
		}
		return out
	}
	// the catalog lists no modes of its own for it: its own default, and the ones the catalog knows
	if got := strings.Join(ids(modesOf(oc)), ","); got != ",build,plan" {
		t.Fatalf("OpenCode's modes: %q", got)
	}
	if got := strings.Join(ids(modesOf(agent.Provider{ID: "bare"})), ","); got != "" || len(modesOf(agent.Provider{ID: "bare"})) != 1 {
		t.Fatalf("a coding agent the catalog knows nothing of: %v", modesOf(agent.Provider{ID: "bare"}))
	}
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	// start-defaults, under a "start without asking" that names plan: it would need the person — plan doesn't ask
	w.policy.set(func(p *dPolicy) { p.prefs["alice"] = delegation.StartPrefs{Mode: "plan", API: true} })
	var sd startDefaultsView
	_ = json.Unmarshal(w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice).Body.Bytes(), &sd)
	w.policy.set(func(p *dPolicy) { delete(p.prefs, "alice") })
	if h, ok := sd.of("opencode"); !ok || h.Asks || !h.NeedsTicket {
		t.Fatalf("OpenCode in start-defaults: %+v", h)
	}
	// the agent suggests it: not preselected — the default is, and the sheet says which was not taken
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "opencode"))
	if s := sessOf(req); s["provider"] == "opencode" || s["harnessNotTaken"] != "opencode" {
		t.Fatalf("a suggested OpenCode: %v", s)
	}
	// a session of it its person approved in build: the agent's messages are each theirs to approve
	if why := modeRefusal(term.SessionInfo{Provider: "opencode", Mode: "build"}, "build", "alice"); why == "" {
		t.Fatal("an OpenCode session takes the agent's messages unapproved")
	}
	if why := modeRefusal(term.SessionInfo{Provider: "claude", Mode: "acceptEdits"}, "acceptEdits", "alice"); why != "" {
		t.Fatalf("a Claude Code session approved in acceptEdits: %s", why)
	}
}

// "Remember for this tile" keeps only what the person themselves changed on
// the sheet — a field left at the agent's suggestion isn't theirs — and
// never a mode that acts without asking; after such a remember, every path
// with no picker (the app's sheet, a start without asking) opens in the
// asking mode (D250 review M1).
func TestRememberKeepsOnlyThePersonsOwnPick(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	remembered := func() *agentprefs.TileStart { r, _ := w.s.AgentPrefs.TileChoice("alice", "apps/x"); return r }
	forget := func() {
		if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"agentSessions":{"tiles":{"apps/x":null}}}`)); err != nil {
			t.Fatal(err)
		}
	}
	approve := func(body string) {
		t.Helper()
		req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude", "model", "sonnet"))
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", body, person); rec.Code != 200 {
			t.Fatalf("approve %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// the agent suggested Claude Code on sonnet; the person changes only the coding agent: only that is theirs
	approve(`{"harness":"codex","model":"","mode":"read-only","remember":true,"knows":["continue"]}`)
	if r := remembered(); r == nil || r.Harness != "codex" || r.Model != "" || r.Mode != "" {
		t.Fatalf("the person changed the coding agent only: %+v", r)
	}
	forget()
	// …only the mode, to one that acts without asking: nothing of theirs is left to remember
	approve(`{"harness":"claude","model":"sonnet","mode":"acceptEdits","remember":true,"knows":["continue"]}`)
	if r := remembered(); r != nil {
		t.Fatalf("a mode that acts without asking, on the agent's suggestion otherwise: %+v", r)
	}
	// …the model (their preference's opus preselected, they pick sonnet) and a mode that acts without asking:
	// the model is kept, with the coding agent it is of, and the mode is dropped
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"harnesses":{"claude":{"model":"opus"}}}`)); err != nil {
		t.Fatal(err)
	}
	approve(`{"harness":"claude","model":"sonnet","mode":"auto","remember":true,"knows":["continue"]}`)
	if r := remembered(); r == nil || r.Harness != "claude" || r.Model != "sonnet" || r.Mode != "" {
		t.Fatalf("a changed model and a mode that acts without asking: %+v", r)
	}
	// the next start — on a sheet with no picker (the app approves what is preselected), or none — asks
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	if s := sessOf(req); s["provider"] != "claude" || s["model"] != "sonnet" || s["mode"] != "default" || s["asks"] != true {
		t.Fatalf("the start after it: %v", s)
	}
	ap := auth.Principal{UserID: "alice", Via: "delegate", Agent: dHolder}
	if pr, _, differs := w.s.unaskedStart(ap, sessionAsk{Cwd: "apps/x"}); pr != "claude" || differs != "" {
		t.Fatalf("a start without asking after it: %s %s", pr, differs)
	}
	if p, _ := w.s.startPick(ap, "apps/x", startSuggestion{}, nil); p.Mode != "default" {
		t.Fatalf("a start without asking after it opens in: %+v", p)
	}
}

// Continuing a session is judged as a message to that session is: on its
// live source — who serves it now — never on what a new start there would
// run on (D250 review M3). Here a new start on Finance's own tile would be
// held to Finance's rule and pass, while the session there runs on a source
// nothing vouches for: after the conversation read Finance's data, the
// follow-up into it is refused, and the ticket isn't spent.
func TestContinueIsJudgedOnTheLiveSession(t *testing.T) {
	w := realDelegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	if err := os.MkdirAll(filepath.Join(w.s.Term.Root, "apps", "mail"), 0o755); err != nil {
		t.Fatal(err)
	}
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	first := startBody("apps/mail", "Goal: sort the mail.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", first)
	w.approve(person, req.ID, "")
	var a struct{ ID string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", first, alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the first start: %d %s", rec.Code, rec.Body.String())
	}
	var next delegation.Request
	body := startBody("apps/mail", "Goal: and file it.", "continue", a.ID)
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		waitIdle(t, w, a.ID)
		if next = w.ask(alice, "POST", "/api/xbin/term/sessions", body); sessOf(next)["continue"] != nil {
			if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+next.ID+"/approve", `{"knows":["continue"]}`, person); rec.Code == 200 {
				break
			}
		}
		w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+next.ID, "", alice)
		if time.Now().After(deadline) {
			t.Fatal("the idle session was never offered to continue")
		}
	}
	if rec := w.do(w.full, "GET", "/api/xbin/components/apps/mail", "", alice); rec.Code != 200 { // the conversation reads Finance's data
		t.Fatalf("read apps/mail: %d", rec.Code)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, next.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), "Finance allows private models only") {
		t.Fatalf("a follow-up into a session nothing vouches for, from a conversation holding Finance's data: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", next.ID); got.State != delegation.Approved {
		t.Fatalf("the refusal spent the ticket: %s", got.State)
	}
}
