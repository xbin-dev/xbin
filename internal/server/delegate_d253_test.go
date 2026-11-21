package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// A Try again continues a session only when its body says so, held to the
// session its sheet showed (D253, the review's H1). The sequence: a start
// offers the agent's own idle session; its person picks "Start a new
// session instead" in a mode that asks and approves; the new session
// fails, Try again allowed; the sheet shows "new session", and its body —
// the pick, knows continue, what it showed — says nothing of continuing.
// Before, that continued the offered session (the retry took no continue,
// and absent meant continue); now it is a new session, as shown. Continue
// true without the session's facts is 400; with them it continues.
func TestTryAgainContinuesOnlyWhenItSaysSo(t *testing.T) {
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
	// a follow-up that offers the session; its person starts a new session instead, in a mode that asks; it fails
	failedNew := func() delegation.Request {
		t.Helper()
		var r delegation.Request
		for i := 0; ; i++ {
			briefTurnOver(t, w, a.ID)
			if r = w.askKnows(alice, "POST", "/api/xbin/term/sessions", next, "retry"); sessOf(r)["continue"] != nil {
				break
			}
			w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+r.ID, "", alice)
			if i > 200 {
				t.Fatal("never offered to continue")
			}
		}
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r.ID+"/approve",
			w.seen("alice", r.ID, `{"knows":["continue"],"continue":false,"harness":"fake","mode":"ask"}`), person); rec.Code != 200 {
			t.Fatalf("a new session instead: %d %s", rec.Code, rec.Body.String())
		}
		got, _ := w.store.Request("alice", r.ID)
		if got.Chosen == nil || got.Chosen.Continue != "" || got.Chosen.Mode != "ask" || sessOf(got)["continue"] == nil {
			t.Fatalf("approved as: %+v, the card %v", got.Chosen, sessOf(got))
		}
		if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", next, alice, header(HeaderTicket, r.ID)); rec.Code >= 300 {
			t.Fatalf("the new session: %d %s", rec.Code, rec.Body.String())
		}
		if _, err := w.store.MarkFailed("alice", r.ID, "the coding agent didn't start", true); err != nil {
			t.Fatal(err)
		}
		return r
	}
	seen := func(id, more string) string {
		f, _ := w.store.Request("alice", id)
		net, _ := sessOf(f)["net"].(string)
		sid, _ := sessOf(f)["signInId"].(string)
		return `"seen":{"signInId":"` + sid + `","net":"` + net + `"` + more + `}`
	}
	retry := func(id, body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/retry", body, person)
		return rec.Code, rec.Body.String()
	}
	chosen := func(id string) *delegation.StartChoice {
		got, _ := w.store.Request("alice", id)
		return got.Chosen
	}

	// Try again as its sheet shows it — a new session — with the body a client before this version sent: the pick,
	// knows continue, what it showed, nothing of continuing. It continued the offered session; now a new one
	r2 := failedNew()
	if code, b := retry(r2.ID, `{"knows":["continue"],"harness":"fake","mode":"ask",`+seen(r2.ID, "")+`}`); code != 200 {
		t.Fatalf("Try again: %d %s", code, b)
	}
	if c := chosen(r2.ID); c == nil || c.Continue != "" || c.Mode != "ask" {
		t.Fatalf("Try again as a new session continued the offered session: %+v", c)
	}
	// …and with the body a client of this version sends: continue false
	r3 := failedNew()
	if code, b := retry(r3.ID, `{"knows":["continue"],"continue":false,"harness":"fake","mode":"ask",`+seen(r3.ID, "")+`}`); code != 200 {
		t.Fatalf("Try again, continue false: %d %s", code, b)
	}
	if c := chosen(r3.ID); c == nil || c.Continue != "" {
		t.Fatalf("Try again, continue false, continued: %+v", c)
	}
	// continuing on a Try again: only said, and only with the session's facts as its sheet showed them
	r4 := failedNew()
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+seen(r4.ID, "")+`}`); code != 400 {
		t.Fatalf("a Try again that continues without the session's facts: %d %s", code, b)
	}
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+seen(r4.ID, `,"harness":"fake","mode":"ask"`)+`}`); code != 409 || !strings.Contains(b, `"changed":"session"`) || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("a Try again that continues a session its sheet showed otherwise: %d %s", code, b)
	}
	offer := sessOf(r4)["continue"].(map[string]any)
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+seen(r4.ID, `,"harness":"`+offer["provider"].(string)+`","mode":"`+offer["mode"].(string)+`"`)+`}`); code != 200 {
		t.Fatalf("a Try again that continues, as shown: %d %s", code, b)
	}
	if c := chosen(r4.ID); c == nil || c.Continue != a.ID {
		t.Fatalf("the Try again that said continue: %+v", c)
	}
}

// GET /delegate/state says what an approve and a Try again may carry
// beyond v0.3.84's (D253): a newer client sends only what its xbind takes.
func TestTheStateSaysWhatAnApproveTakes(t *testing.T) {
	w := delegateWorld(t)
	rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", w.cookie("alice"))
	var st struct{ Takes []string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("the state: %d %s", rec.Code, rec.Body.String())
	}
	for _, word := range []string{"seen-text", "seen-session", "retry-continue", "device-message"} {
		if !slices.Contains(st.Takes, word) {
			t.Errorf("takes %v: no %q", st.Takes, word)
		}
	}
	if strings.Contains(rec.Body.String(), `"takes":null`) {
		t.Error("takes is a list")
	}
}
