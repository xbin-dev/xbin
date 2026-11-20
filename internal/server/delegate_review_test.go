package server

// delegate_review_test.go — the rules the independent review of D250 found
// unheld, each with a test that fails without its rule.

import (
	"encoding/json"
	"net/http"
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
	waitIdle(t, w, out.Session) // (its model is the agent's own report, which its status carries once it is up)
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

// A follow-up into a session that carries the person's saved sign-in is a
// use of that sign-in: it needs their own tick on the approval, each time
// (D230), or the sheet's other choice — a new session, which has none — and
// at the redeem the session must still carry the one they ticked (D250
// review L1).
func TestContinueIntoASessionWithASignInNeedsTheTick(t *testing.T) {
	live := map[string]any{"session": "s-1", "provider": "claude", "signIn": "saved", "signInId": "si_1", "signInName": "work"}
	plain := map[string]any{"session": "s-2", "provider": "claude", "signIn": ""}
	if why := continueTickRefusal(live, false); !strings.Contains(why, "tick") || !strings.Contains(why, "start a new session instead") {
		t.Fatalf("a follow-up into a session with a sign-in, unticked: %q", why)
	}
	if continueTickRefusal(live, true) != "" || continueTickRefusal(plain, false) != "" {
		t.Fatal("a ticked follow-up, or one into a session with no sign-in, refused")
	}
	if continueSigninStill(live, map[string]any{"signInId": "si_1"}, false) == "" {
		t.Fatal("redeemed without the tick")
	}
	if continueSigninStill(live, map[string]any{"signInId": "si_2"}, true) == "" {
		t.Fatal("redeemed into a session carrying another sign-in than the one ticked")
	}
	if continueSigninStill(live, map[string]any{"signInId": "si_1"}, true) != "" || continueSigninStill(plain, nil, false) != "" {
		t.Fatal("a ticked follow-up, or one into a session with no sign-in, refused at the redeem")
	}
}

// The approval of a start carries what the sheet showed of what xbind
// re-resolves when it is decided — the saved sign-in it offered and the
// network — and a mismatch is refused, specifically, with nothing decided:
// the sheet reloads and shows it as it is (D250 review L2).
func TestApprovalHoldsToWhatTheSheetShowed(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	s := sessOf(req)
	net, _ := s["net"].(string)
	stale := `{"knows":["continue"],"seen":{"signInId":"","net":"` + net + `-not"}}`
	rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", stale, person)
	var refused struct {
		Error, Changed string
		Reload         bool
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &refused)
	if rec.Code != 409 || refused.Changed != "net" || !refused.Reload || !strings.Contains(refused.Error, "network") {
		t.Fatalf("an approval of a network the sheet no longer shows: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Pending {
		t.Fatalf("the refusal decided it: %s", got.State)
	}
	stale = `{"knows":["continue"],"seen":{"signInId":"si_gone","net":"` + net + `"}}`
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", stale, person); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"changed":"signin"`) {
		t.Fatalf("an approval of a sign-in the sheet no longer offers: %d %s", rec.Code, rec.Body.String())
	}
	sid, _ := s["signInId"].(string)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"knows":["continue"],"seen":{"signInId":"`+sid+`","net":"`+net+`"}}`, person); rec.Code != 200 {
		t.Fatalf("an approval of what the sheet showed: %d %s", rec.Code, rec.Body.String())
	}
}

// askKnows files a request as a backend that says what it knows does.
func (w *dWorld) askKnows(agent cred, method, path, body string, knows ...string) delegation.Request {
	w.t.Helper()
	in, _ := json.Marshal(map[string]any{"method": method, "path": path, "body": body, "summary": "the agent's words", "knows": knows})
	rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), agent)
	if rec.Code != 200 {
		w.t.Fatalf("ask %s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	return decode[delegation.Request](w.t, rec)
}

// A start that fails is classified by its cause (D250 review L3): one the
// agent's own call causes is refused before its ticket is spent, as a
// ticket refusal with its reason; one that isn't the agent's is "failed",
// with Try again — only for a backend that says it knows ("retry"): an older
// one gets the open's answer as before, the request used; and the person's
// Try again is a decision like an approval — its picks validated, its
// sign-in tick its own, never the earlier one carried over.
func TestStartFailuresByCause(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	// the agent's own call: a start body that isn't a tile agent's start — refused before the ticket is spent
	bad := startBody("apps/x", "go", "kind", "shell")
	req := w.askKnows(alice, "POST", "/api/xbin/term/sessions", bad, "retry")
	w.approve(person, req.ID, "")
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", bad, alice, header(HeaderTicket, req.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"bound"`) {
		t.Fatalf("an agent's own bad start: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Approved {
		t.Fatalf("the agent's bad start spent the ticket: %s", got.State)
	}
	// not the agent's doing (no bx here: 503), from an older backend: the open's answer, the request used
	body := startBody("apps/x", "go")
	req = w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	w.approve(person, req.ID, "")
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "start-failed") {
		t.Fatalf("a failed start for an older backend: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Used {
		t.Fatalf("a failed start for an older backend: %s", got.State)
	}
	// …from a backend that knows: failed, and the person's Try again — validated as an approval
	req = w.askKnows(alice, "POST", "/api/xbin/term/sessions", body, "retry")
	w.approve(person, req.ID, "")
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); !strings.Contains(rec.Body.String(), `"refusal":"start-failed"`) {
		t.Fatalf("a failed start: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", `{"signin":true,"knows":["continue"]}`, person); rec.Code != 400 {
		t.Fatalf("Try again ticking a sign-in nobody offered: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", `{"harness":"codex","mode":"read-only","knows":["continue"]}`, person); rec.Code != 200 {
		t.Fatalf("Try again on another coding agent: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Approved || got.Chosen == nil || got.Chosen.Provider != "codex" || got.UseSignin {
		t.Fatalf("tried again: %s %+v (use sign-in %v)", got.State, got.Chosen, got.UseSignin)
	}
}

// Through the route: a message the person's agent queues behind a running
// turn is marked as the agent's, so the person's switch to a mode that acts
// without asking keeps it from going in (D250 review L4).
func TestAgentQueuedMessageIsMarkedAsTheAgents(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	w.approve(person, req.ID, "")
	var a struct{ ID string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the start: %d %s", rec.Code, rec.Body.String())
	}
	// the brief's own turn over first (xbind hands it the brief once it is up: an idle read can come before that
	// turn began), then the person's turn, held until they cancel it ("stall": the scripted agent waits for it)
	turnEnds := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			page, _ := w.s.Term.AgentPage(a.ID, 0, 200)
			ends := 0
			for _, e := range page.Events {
				if e.Type == agent.EvTurnEnd {
					ends++
				}
			}
			if ends >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn %d never ended (%d ended)", n, ends)
			}
		}
	}
	turnEnds(1)
	waitIdle(t, w, a.ID)
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+a.ID+"/prompt", `{"text":"stall"}`, person); rec.Code != 200 {
		t.Fatalf("the person's turn: %d %s", rec.Code, rec.Body.String())
	}
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+a.ID+"/queue", `{"text":"Fix the stray O."}`, alice)
	var held term.QueueResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &held) != nil || held.Outcome != term.QueueHeld {
		t.Fatalf("the agent's message behind the turn: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+a.ID+"/options", `{"id":"mode","value":"auto"}`, person); rec.Code != 200 {
		t.Fatalf("the person's switch: %d %s", rec.Code, rec.Body.String())
	}
	// the mode is the session's once the agent says so: then the person ends their turn, and what is held goes in
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if info, _ := w.s.Term.Info(a.ID); info.Mode == "auto" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the switch to auto never reached the session")
		}
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+a.ID+"/cancel", "", person); rec.Code != 200 {
		t.Fatalf("the person's cancel: %d %s", rec.Code, rec.Body.String())
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		page, _ := w.s.Term.AgentPage(a.ID, 0, 200)
		for _, e := range page.Events {
			if e.Type == term.EvQueue && strings.Contains(string(e.Data), `"returned":["`+held.ID+`"]`) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent's held message went in after the switch (events: %d)", len(page.Events))
		}
	}
}
