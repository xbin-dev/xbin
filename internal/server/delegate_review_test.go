package server

// delegate_review_test.go — the rules the independent review of D250 found
// unheld, each with a test that fails without its rule.

import (
	"cmp"
	"encoding/json"
	"fmt"
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
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
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
	// the person's own choice for the tile — fake, fast, in its asking mode — outranks the line's suggestion
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "fake", Model: "fake-fast", Mode: "ask"}, 1); err != nil {
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
	if !strings.Contains(effects, "What runs is this line's coding agent, model and mode") || !strings.Contains(effects, "a single start's approval") {
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
	// the agent suggests it: a preselection the person sees, like any coding agent (D251) — in its own mode
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "opencode"))
	if s := sessOf(req); s["provider"] != "opencode" || s["mode"] != "" || s["asks"] != false || s["harnessNotTaken"] != nil {
		t.Fatalf("a suggested OpenCode: %v", s)
	}
	// every message of the agent's to a session that acts without asking is its person's to approve — whatever
	// mode they approved the session in (D251: no exemption for "the mode approved")
	for _, in := range []term.SessionInfo{{Provider: "opencode", Mode: "build"}, {Provider: "claude", Mode: "acceptEdits"}, {Provider: "claude", Mode: "auto"}, {Provider: "codex", Mode: "agent"}} {
		if why := modeRefusal(in, "alice"); why == "" {
			t.Errorf("%s in %s takes the agent's messages unapproved", in.Provider, in.Mode)
		}
	}
	if why := modeRefusal(term.SessionInfo{Provider: "claude", Mode: "default"}, "alice"); why != "" {
		t.Fatalf("a Claude Code session that asks: %s", why)
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
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", w.seen("alice", req.ID, body), person); rec.Code != 200 {
			t.Fatalf("approve %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// the agent suggested Claude Code on sonnet; the person changes only the coding agent (Codex comes in its own
	// default, its work mode): only that is theirs
	approve(`{"harness":"codex","model":"","mode":"agent","remember":true,"knows":["continue"]}`)
	if r := remembered(); r == nil || r.Harness != "codex" || r.Model != "" || r.Mode != "" {
		t.Fatalf("the person changed the coding agent only: %+v", r)
	}
	forget()
	// …only the mode — to accept edits, or to one that asks: a mode is a choice like any other (D251), kept
	for _, mode := range []string{"acceptEdits", "default"} {
		approve(`{"harness":"claude","model":"sonnet","mode":"` + mode + `","remember":true,"knows":["continue"]}`)
		if r := remembered(); r == nil || r.Harness != "claude" || r.Model != "" || r.Mode != mode {
			t.Fatalf("the person changed the mode only, to %s: %+v", mode, r)
		}
		forget()
	}
	// …the model (their preference's opus preselected, they pick sonnet), the mode left at its default: the model
	// is kept, with the coding agent it is of
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"harnesses":{"claude":{"model":"opus"}}}`)); err != nil {
		t.Fatal(err)
	}
	approve(`{"harness":"claude","model":"sonnet","mode":"auto","remember":true,"knows":["continue"]}`)
	if r := remembered(); r == nil || r.Harness != "claude" || r.Model != "sonnet" || r.Mode != "" {
		t.Fatalf("a changed model, the default mode: %+v", r)
	}
	// the next start preselects what was kept, in the work mode
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	if s := sessOf(req); s["provider"] != "claude" || s["model"] != "sonnet" || s["mode"] != "auto" || s["asks"] != false {
		t.Fatalf("the start after it: %v", s)
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
			if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+next.ID+"/approve", w.seen("alice", next.ID, `{"knows":["continue"]}`), person); rec.Code == 200 {
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

// A follow-up into a session that carries the person's saved sign-in
// needs no tick (D251: they ticked it when the session started; a
// follow-up is the same session doing more of what they approved) — but
// at the redeem the session must still carry the sign-in it carried when
// the sheet showed it.
func TestContinueIntoASessionWithASignInIsThatSession(t *testing.T) {
	live := map[string]any{"session": "s-1", "provider": "claude", "signIn": "saved", "signInId": "si_1", "signInName": "work"}
	plain := map[string]any{"session": "s-2", "provider": "claude", "signIn": ""}
	if continueSigninStill(live, map[string]any{"signInId": "si_1"}) != "" || continueSigninStill(plain, nil) != "" || continueSigninStill(plain, map[string]any{}) != "" {
		t.Fatal("the session as shown, refused at the redeem")
	}
	if continueSigninStill(live, map[string]any{"signInId": "si_2"}) == "" || continueSigninStill(live, map[string]any{}) == "" || continueSigninStill(plain, map[string]any{"signInId": "si_1"}) == "" {
		t.Fatal("redeemed into a session carrying another sign-in than the one shown")
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
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", w.seen("alice", req.ID, `{"signin":true,"knows":["continue"]}`), person); rec.Code != 400 {
		t.Fatalf("Try again ticking a sign-in nobody offered: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", w.seen("alice", req.ID, `{"harness":"codex","mode":"read-only","knows":["continue"]}`), person); rec.Code != 200 {
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
	// (in its asking mode, picked: the agent's messages go to it unapproved until the person switches it)
	w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", w.seen("alice", req.ID, `{"harness":"fake","mode":"ask"}`), person)
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
				goto returned
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent's held message went in after the switch (events: %d)", len(page.Events))
		}
	}
returned:
	// the agent's own read of its sessions says so, with xbind's reason (D250 re-review 4a); the person's doesn't carry it
	{
		var rows []struct {
			ID       string
			Returned []term.Returned
		}
		rec := w.do(w.full, "GET", "/api/xbin/term/sessions?origin=agent&compact=1", "", alice)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &rows) != nil || len(rows) != 1 || len(rows[0].Returned) != 1 ||
			rows[0].Returned[0].ID != held.ID || rows[0].Returned[0].Text != "Fix the stray O." || !strings.Contains(rows[0].Returned[0].Reason, `"auto"`) {
			t.Fatalf("the agent's read of the session: %d %s", rec.Code, rec.Body.String())
		}
		if rec := w.do(w.full, "GET", "/api/xbin/term/sessions?compact=1", "", person); strings.Contains(rec.Body.String(), `"returned"`) {
			t.Fatalf("the person's read carries it: %s", rec.Body.String())
		}
	}
}

// The owner's case (D250 re-review item 3; D251): he wants OpenCode for a
// tile. Its coding agent, model and mode are remembered like any other's,
// and preselected on every later approval — an ordinary start, which acts
// without asking (as every coding agent's default does now); it never
// starts without the sheet, whatever the person's preference; the default
// is any coding agent that runs as things are.
func TestTheOwnersOpenCodeForATile(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	// the default of a start nobody chose anything for: the coding agent's work mode (D251)
	if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude"))); s["mode"] != "auto" || s["asks"] != false {
		t.Fatalf("Claude Code's default for a start the agent asks for: %v", s)
	}
	// picked on the sheet, remembered: OpenCode, its model and its mode are kept
	r := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r.ID+"/approve", w.seen("alice", r.ID, `{"harness":"opencode","mode":"build","remember":true}`), person); rec.Code != 200 {
		t.Fatalf("approve OpenCode, remembered: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.s.AgentPrefs.TileChoice("alice", "apps/x"); got == nil || got.Harness != "opencode" || got.Mode != "build" {
		t.Fatalf("OpenCode remembered for the tile: %+v", got)
	}
	// the next start there preselects it, in build
	s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "more")))
	if s["provider"] != "opencode" || s["mode"] != "build" || s["asks"] != false || fmt.Sprint(s["from"]) != "map[harness:tile mode:tile model:default]" {
		t.Fatalf("the next start: %v", s)
	}
	// never without the sheet: the person's "start without asking" doesn't reach a mode that doesn't ask — and
	// the agent is told so in plain words
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{} })
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"agentSessions":{"start":"allow"}}`)); err != nil {
		t.Fatal(err)
	}
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","prompt":"go"}`, alice)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "act without asking, so every start is approved on the sheet") {
		t.Fatalf("an unasked start on the person's OpenCode: %d %s", rec.Code, rec.Body.String())
	}
	// the default coding agent: the first allowed that runs as things are — whatever its modes (D251)
	w.policy.set(func(d *dPolicy) { delete(d.prefs, "alice") })
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"agentSessions":{"tiles":null}}`)); err != nil {
		t.Fatal(err)
	}
	saved := startLLMOf
	startLLMOf = func(_ *Server, _ auth.Principal, _ string, provs []agent.Provider) map[string]*term.ProviderLLM {
		out := map[string]*term.ProviderLLM{}
		for _, pv := range provs {
			out[pv.ID] = &term.ProviderLLM{Use: "own"}
		}
		out["opencode"] = &term.ProviderLLM{Use: "own-first", Connector: true}
		return out
	}
	t.Cleanup(func() { startLLMOf = saved })
	if def := w.s.startDefaults(auth.Principal{UserID: "alice", Agent: dHolder}, "apps/x"); def.Harness != "opencode" {
		t.Fatalf("the default where only OpenCode runs as things are: %q", def.Harness)
	}
}

// seen is body — an approve's or a retry's — with the seen basis the
// request's sheet shows, as the web's sheet sends it with a pick or a
// continue (D250 re-review item 5): the sign-in it offers and the network,
// of the coding agent picked, or of the session continued. A body that
// says one itself, or picks nothing, is left as it is.
func (w *dWorld) seen(user, id, body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(cmp.Or(body, "{}")), &m) != nil {
		return body
	}
	_, has := m["seen"]
	picks := m["harness"] != nil || m["model"] != nil || m["mode"] != nil || m["remember"] != nil || m["continue"] != nil || m["knows"] != nil
	if has || !picks {
		return body
	}
	r, _ := w.store.Request(user, id)
	s, _ := r.Preview["session"].(map[string]any)
	sid, _ := s["signInId"].(string)
	net, _ := s["net"].(string)
	if h, _ := m["harness"].(string); h != "" && h != s["provider"] {
		ch, _ := s["choices"].(map[string]any)
		hs, _ := ch["harnesses"].([]any)
		for _, x := range hs {
			if hm, _ := x.(map[string]any); hm["id"] == h {
				sid, _ = hm["signInId"].(string)
				net, _ = hm["net"].(string)
			}
		}
	}
	seen := map[string]any{"signInId": sid, "net": net}
	if c, _ := s["continue"].(map[string]any); c != nil && m["continue"] != false && (m["harness"] == nil || m["harness"] == c["provider"]) {
		seen["signInId"], _ = c["signInId"].(string)
		seen["harness"], seen["mode"] = c["provider"], c["mode"] // the session as the sheet shows it (D251)
		if seen["signInId"] == nil {
			seen["signInId"] = ""
		}
	}
	m["seen"] = seen
	b, _ := json.Marshal(m)
	return string(b)
}

// An approval that picks — or continues — says what its sheet showed;
// without it, 400 and nothing decided; an approval that picks nothing (an
// older client, the app) is as it was (D250 re-review item 5).
func TestPicksNeedWhatTheSheetShowed(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	for _, body := range []string{`{"harness":"codex"}`, `{"mode":"plan"}`, `{"remember":true,"harness":"claude"}`, `{"continue":false,"harness":"claude"}`, `{"harness":"claude","seen":{"net":""}}`} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", body, person); rec.Code != 400 || !strings.Contains(rec.Body.String(), "says what its sheet showed") {
			t.Errorf("%s without seen: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Pending {
		t.Fatalf("refused, and decided: %s", got.State)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", w.seen("alice", req.ID, `{"harness":"codex"}`), person); rec.Code != 200 {
		t.Fatalf("with what the sheet showed: %d %s", rec.Code, rec.Body.String())
	}
	older := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "more"))
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+older.ID+"/approve", `{"grant":"once"}`, person); rec.Code != 200 {
		t.Fatalf("an older client's approval (no picks): %d %s", rec.Code, rec.Body.String())
	}
}

// A start filed as a follow-up into the agent's own session is judged at
// filing as the decide judges it (D250 re-review item 6): on that
// session's live source — here the connector, on a model Finance allows —
// not as a new start on the model the body suggests, which Finance's rule
// keeps out; a new session the person picks instead is judged in full.
func TestAFollowUpIsFiledAsItWouldBeDecided(t *testing.T) {
	w := realDelegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.Term.LLM = offerLLM{term.LLMOffer{Agents: true, Terminals: true, Wires: []string{sdkacp.WireChat}, Model: "local/m1"}, true}
	w.s.Term.Credentials = func(user, tile, harness, choice string) term.CredentialRule {
		return term.CredentialRule{Use: "connector", Forced: true}
	}
	env := w.s.Term.Env
	w.s.Term.Env = func() []string { // where its sessions reach the connector (nothing calls it here)
		var out []string
		if env != nil {
			out = env()
		}
		return append(out, "XBIN_URL=http://127.0.0.1:9")
	}
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	first := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", first)
	w.approve(person, req.ID, "")
	var a struct{ ID string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", first, alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the first start: %d %s", rec.Code, rec.Body.String())
	}
	waitIdle(t, w, a.ID)
	if info, _ := w.s.Term.Info(a.ID); info.LLM != "connector" || w.s.Term.AgentConnectorModel(a.ID) != "local/m1" {
		t.Fatalf("the session isn't on the connector's local model: %+v %q", info, w.s.Term.AgentConnectorModel(a.ID))
	}
	if rec := w.do(w.full, "GET", "/api/xbin/components/apps/mail", "", alice); rec.Code != 200 { // the conversation holds Finance's data
		t.Fatalf("read apps/mail: %d", rec.Code)
	}
	newStart := startBody("apps/x", "Goal: and more.", "provider", "fake", "model", "hosted/big")
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": newStart})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), "Finance") {
		t.Fatalf("a new start on a hosted model, filed: %d %s", rec.Code, rec.Body.String())
	}
	var next delegation.Request
	follow := startBody("apps/x", "Goal: and more.", "provider", "fake", "model", "hosted/big", "continue", a.ID)
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		waitIdle(t, w, a.ID)
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": follow})
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice)
		if rec.Code != 200 {
			t.Fatalf("the follow-up into the session there, filed: %d %s", rec.Code, rec.Body.String())
		}
		if next = decode[delegation.Request](t, rec); sessOf(next)["continue"] != nil {
			break
		}
		w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+next.ID, "", alice)
		if time.Now().After(deadline) {
			t.Fatal("never offered to continue")
		}
	}
	// the person picks a new session instead: judged in full — refused, nothing decided
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+next.ID+"/approve", w.seen("alice", next.ID, `{"knows":["continue"],"continue":false,"harness":"fake","model":"hosted/big"}`), person); rec.Code == 200 {
		t.Fatalf("a new session on a hosted model, picked: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+next.ID+"/approve", w.seen("alice", next.ID, `{"knows":["continue"]}`), person); rec.Code != 200 {
		t.Fatalf("the follow-up, approved: %d %s", rec.Code, rec.Body.String())
	}
}
