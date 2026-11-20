package server

// delegate_d251_test.go — D251 (plans/wsagent/auto-default.md): the
// sessions a person's workspace agent starts act without asking by
// default; a mode is a choice; continuing is one plain approval — and what
// must not loosen with that.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// The default is the coding agent's work mode, and the start one plain
// approval; the session then takes none of the agent's texts unapproved:
// each one is a message request, sent once its person allows exactly it.
func TestAStartActsWithoutAskingAndEveryTextToItIsApproved(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	if s := sessOf(req); s["mode"] != "auto" || s["asks"] != false {
		t.Fatalf("the start's card: %v", s)
	}
	w.approve(person, req.ID, "") // (an approval with no picks: as shown)
	var a struct{ ID, Mode string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the start: %d %s", rec.Code, rec.Body.String())
	}
	waitIdle(t, w, a.ID)
	if info, _ := w.s.Term.Info(a.ID); info.Mode != "auto" {
		t.Fatalf("it opened in %q, not its work mode", info.Mode)
	}
	path := "/api/xbin/term/sessions/" + a.ID + "/prompt"
	msg := `{"text":"Now add a computer player."}`
	if rec := w.do(w.full, "POST", path, msg, alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"mode"`) || !strings.Contains(rec.Body.String(), "file it as a message request") {
		t.Fatalf("an unapproved text to a session that acts without asking: %d %s", rec.Code, rec.Body.String())
	}
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": path, "body": msg})
	m := decode[delegation.Request](t, w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice))
	if m.Kind != delegation.KindMessage {
		t.Fatalf("the message request: %+v", m)
	}
	// a notification's Allow once never sends it, nor starts anything: only a tile API call is for the lock screen
	for _, id := range []string{m.ID, w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "more")).ID} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", `{"via":"notification"}`, person); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"review":true`) {
			t.Fatalf("approved from a notification: %d %s", rec.Code, rec.Body.String())
		}
	}
	w.approve(person, m.ID, "")
	if rec := w.do(w.full, "POST", path, msg, alice, header(HeaderTicket, m.ID)); rec.Code != 200 {
		t.Fatalf("the approved text: %d %s", rec.Code, rec.Body.String())
	}
}

// No path without the sheet starts a session in a mode that doesn't ask:
// the person's "start without asking" starts unasked only in a mode that
// asks — one they named — and otherwise the start goes to the sheet, the
// agent told so in plain words.
func TestNoPathWithoutTheSheetStartsAModeThatDoesntAsk(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"agentSessions":{"start":"allow","modes":{"claude":"auto"}}}`)); err != nil {
		t.Fatal(err)
	}
	for mode, unasked := range map[string]bool{"auto": false, "acceptEdits": false, "bypassPermissions": false, "default": true, "plan": true} {
		w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{Mode: mode, API: true} })
		var sd startDefaultsView
		_ = json.Unmarshal(w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice).Body.Bytes(), &sd)
		if h, _ := sd.of("claude"); h.NeedsTicket == unasked {
			t.Errorf("a preference in %s: start-defaults %+v", mode, h)
		}
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"go"}`, alice)
		switch {
		case unasked && rec.Code != 503: // (every gate passed: no bx in this fixture)
			t.Errorf("a preference in %s: %d %s", mode, rec.Code, rec.Body.String())
		case !unasked && (rec.Code != 403 || !strings.Contains(rec.Body.String(), "every start is approved on the sheet")):
			t.Errorf("a preference in %s started with no sheet: %d %s", mode, rec.Code, rec.Body.String())
		}
	}
}

// Continuing is one plain approval — no tick for a sign-in the session
// carries (unit: TestContinueIntoASessionWithASignInIsThatSession) — held
// to the session the sheet showed: another mode than the one shown is a
// 409 and the sheet looks again, nothing decided.
func TestAContinueIsHeldToTheSessionShown(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	first := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", first)
	w.approve(person, req.ID, "")
	var a struct{ ID string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", first, alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the start: %d %s", rec.Code, rec.Body.String())
	}
	next := startBody("apps/x", "Goal: add a computer player.", "continue", a.ID)
	var r2 delegation.Request
	for i := 0; ; i++ {
		waitIdle(t, w, a.ID)
		if r2 = w.ask(alice, "POST", "/api/xbin/term/sessions", next); sessOf(r2)["continue"] != nil {
			break
		}
		w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+r2.ID, "", alice)
		if i > 200 {
			t.Fatal("never offered to continue")
		}
	}
	stale := `{"knows":["continue"],"seen":{"signInId":"","net":"` + sessOf(r2)["net"].(string) + `","harness":"fake","mode":"ask"}}`
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r2.ID+"/approve", stale, person); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"changed":"session"`) {
		t.Fatalf("a continue whose sheet showed another mode: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", r2.ID); got.State != delegation.Pending {
		t.Fatalf("decided: %s", got.State)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r2.ID+"/approve", w.seen("alice", r2.ID, `{"knows":["continue"]}`), person); rec.Code != 200 {
		t.Fatalf("the continue, as shown: %d %s", rec.Code, rec.Body.String())
	}
}
