package server

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
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
	f := newFollowUps(t)
	w, retry := f.w, f.retry
	chosen := func(id string) *delegation.StartChoice {
		got, _ := w.store.Request("alice", id)
		return got.Chosen
	}

	// Try again as its sheet shows it — a new session — with the body a client before this version sent: the pick,
	// knows continue, what it showed, nothing of continuing. It continued the offered session; now a new one
	r2 := f.failedNew("Goal: add a computer player.")
	if code, b := retry(r2.ID, `{"knows":["continue"],"harness":"fake","mode":"ask",`+f.seen(r2.ID, "")+`}`); code != 200 {
		t.Fatalf("Try again: %d %s", code, b)
	}
	if c := chosen(r2.ID); c == nil || c.Continue != "" || c.Mode != "ask" {
		t.Fatalf("Try again as a new session continued the offered session: %+v", c)
	}
	// …and with the body a client of this version sends: continue false
	r3 := f.failedNew("Goal: add a computer player.")
	if code, b := retry(r3.ID, `{"knows":["continue"],"continue":false,"harness":"fake","mode":"ask",`+f.seen(r3.ID, "")+`}`); code != 200 {
		t.Fatalf("Try again, continue false: %d %s", code, b)
	}
	if c := chosen(r3.ID); c == nil || c.Continue != "" {
		t.Fatalf("Try again, continue false, continued: %+v", c)
	}
	// continuing on a Try again: only said, and only with the session's facts as its sheet showed them
	r4 := f.failedNew("Goal: add a computer player.")
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+f.seen(r4.ID, "")+`}`); code != 400 {
		t.Fatalf("a Try again that continues without the session's facts: %d %s", code, b)
	}
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+f.seen(r4.ID, `,"harness":"fake","mode":"ask"`)+`}`); code != 409 || !strings.Contains(b, `"changed":"session"`) || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("a Try again that continues a session its sheet showed otherwise: %d %s", code, b)
	}
	if code, b := retry(r4.ID, `{"knows":["continue"],"continue":true,`+f.seen(r4.ID, f.offered(r4))+`}`); code != 200 {
		t.Fatalf("a Try again that continues, as shown: %d %s", code, b)
	}
	if c := chosen(r4.ID); c == nil || c.Continue != f.sid {
		t.Fatalf("the Try again that said continue: %+v", c)
	}
}

// A v0.3.84 page left open across the upgrade preselects the continue on
// a failed follow-up, and its Try again says nothing of it — knows
// continue, what it showed, no continue, no pick (with the sign-in tick or
// without): this xbind can't tell it from a new session's, so it is
// refused — 409, reload, nothing decided, nothing spent — never a new
// session the sheet didn't show (D253, the re-review's 1). And continue
// true where the start offers no session, or from a client that doesn't
// know continue, is refused the same — on an approve and on a Try again —
// never a new session quietly opened.
func TestAnOlderPagesTryAgainOfAContinueIsRefused(t *testing.T) {
	f := newFollowUps(t)
	w := f.w
	r := f.failedNew("Goal: add a computer player.")
	before, _ := w.store.Request("alice", r.ID)
	for _, extra := range []string{"", `"signin":true,`} {
		code, b := f.retry(r.ID, `{`+extra+`"knows":["continue"],`+f.seen(r.ID, "")+`}`)
		if code != 409 || !strings.Contains(b, `"reload":true`) || !strings.Contains(b, "reload this page") {
			t.Fatalf("v0.3.84's Try again of a continue (%s): %d %s", extra, code, b)
		}
		if got, _ := w.store.Request("alice", r.ID); got.State != delegation.Failed || !reflect.DeepEqual(got.Chosen, before.Chosen) || got.UseSignin {
			t.Fatalf("decided by v0.3.84's Try again (%s): %s %+v", extra, got.State, got.Chosen)
		}
	}
	// continue true without knows continue, on a Try again: refused, nothing decided
	if code, b := f.retry(r.ID, `{"continue":true,`+f.seen(r.ID, f.offered(r))+`}`); code != 409 || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("a Try again that continues without knowing continue: %d %s", code, b)
	}
	// …and on an approve: a follow-up pending, continue without the word
	p := f.offer("Goal: a score board.")
	if code, b := f.approve(p.ID, `{"continue":true,`+f.seen(p.ID, f.offered(p))+`}`); code != 409 || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("an approve that continues without knowing continue: %d %s", code, b)
	}
	if got, _ := w.store.Request("alice", p.ID); got.State != delegation.Pending {
		t.Fatalf("decided: %s", got.State)
	}
	// a start that offers no session: continue true is refused on its approve and on its Try again
	plain := w.ask(f.alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: a menu.", "provider", "fake"))
	if sessOf(plain)["continue"] != nil {
		t.Fatal("a plain start offers a session")
	}
	cont := `{"knows":["continue"],"continue":true,` + f.seen(plain.ID, `,"harness":"fake","mode":"ask"`) + `}`
	if code, b := f.approve(plain.ID, cont); code != 409 || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("an approve that continues where nothing is offered: %d %s", code, b)
	}
	f.used(plain, `{"grant":"once"}`)
	if code, b := f.retry(plain.ID, cont); code != 409 || !strings.Contains(b, `"reload":true`) {
		t.Fatalf("a Try again that continues where nothing is offered: %d %s", code, b)
	}
	if code, b := f.retry(plain.ID, `{}`); code != 200 {
		t.Fatalf("its Try again as shown: %d %s", code, b)
	}
}

// A text longer than the 4000 characters (code points) a client up to
// v0.3.84 showed before Show all is decided only by a client that says
// all of it was on screen (knows "seen-text"): from any other — a page,
// the app — an approve or a Try again of it is refused, 409 reload,
// nothing decided; 4000 still works. A start's brief, a continue's, a
// message's (D253, the re-review's 2). The words say what to do by the
// session's type: a browser's (cookie) reloads, the app's (bearer) updates.
func TestALongTextIsDecidedOnlyBySheetsThatShowedAll(t *testing.T) {
	f := newFollowUps(t)
	w := f.w
	app := bearer(w.s.Auth.NewBearerSession("alice", "dev-1", "127.0.0.1").Token)
	long := func(n int) string { return strings.Repeat("😀", n) } // one code point, two UTF-16 units, four bytes
	refused := func(code int, b, words string) bool {
		return code == 409 && strings.Contains(b, `"changed":"text"`) && strings.Contains(b, `"reload":true`) && strings.Contains(b, words)
	}
	// a start's brief, from the web and from the app
	for _, c := range []struct {
		via   cred
		words string
	}{{f.person, "reload this page"}, {app, "update the app"}} {
		over := w.ask(f.alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", long(4001), "provider", "fake"))
		if code, b := f.approveAs(c.via, over.ID, `{"grant":"once"}`); !refused(code, b, c.words) {
			t.Fatalf("a 4001-character brief, approved without the word: %d %s", code, b)
		}
		if got, _ := w.store.Request("alice", over.ID); got.State != delegation.Pending {
			t.Fatalf("decided: %s", got.State)
		}
		if code, b := f.approveAs(c.via, over.ID, `{"grant":"once","knows":["seen-text"],"seen":{"textLen":`+strconv.Itoa(4*4001)+`}}`); code != 200 {
			t.Fatalf("all of a 4001-character brief: %d %s", code, b)
		}
		at := w.ask(f.alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", long(4000), "provider", "fake"))
		if code, b := f.approveAs(c.via, at.ID, `{"grant":"once"}`); code != 200 {
			t.Fatalf("a 4000-character brief, approved without the word: %d %s", code, b)
		}
	}
	// a continue's brief, with v0.3.84's continuing body
	over := f.offer(long(4001))
	if code, b := f.approve(over.ID, `{"knows":["continue"],`+f.seen(over.ID, "")+`}`); !refused(code, b, "reload this page") {
		t.Fatalf("a 4001-character continue, approved without the word: %d %s", code, b)
	}
	w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+over.ID, "", f.alice)
	at := f.offer(long(4000))
	if code, b := f.approve(at.ID, `{"knows":["continue"],`+f.seen(at.ID, "")+`}`); code != 200 {
		t.Fatalf("a 4000-character continue, approved without the word: %d %s", code, b)
	}
	if got, _ := w.store.Request("alice", at.ID); got.Chosen == nil || got.Chosen.Continue != f.sid {
		t.Fatalf("the continue: %+v", got.Chosen)
	}
	w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+at.ID, "", f.alice)
	// a message's text
	briefTurnOver(t, w, f.sid)
	path := "/api/xbin/term/sessions/" + f.sid + "/prompt"
	m := w.ask(f.alice, "POST", path, `{"text":`+strconv.Quote(long(4001))+`}`)
	if code, b := f.approve(m.ID, `{"grant":"once"}`); !refused(code, b, "reload this page") {
		t.Fatalf("a 4001-character message, approved without the word: %d %s", code, b)
	}
	w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+m.ID, "", f.alice)
	m = w.ask(f.alice, "POST", path, `{"text":`+strconv.Quote(long(4000))+`}`)
	if code, b := f.approve(m.ID, `{"grant":"once"}`); code != 200 {
		t.Fatalf("a 4000-character message, approved without the word: %d %s", code, b)
	}
	// a Try again of a start whose brief is longer: only with the word
	failed := w.ask(f.alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", long(4001), "provider", "fake"))
	f.used(failed, `{"grant":"once","knows":["seen-text"],"seen":{"textLen":`+strconv.Itoa(4*4001)+`}}`)
	if code, b := f.retry(failed.ID, `{}`); !refused(code, b, "reload this page") {
		t.Fatalf("a Try again of a 4001-character brief without the word: %d %s", code, b)
	}
	if code, b := f.retryAs(app, failed.ID, `{}`); !refused(code, b, "update the app") {
		t.Fatalf("the app's Try again of a 4001-character brief without the word: %d %s", code, b)
	}
	if got, _ := w.store.Request("alice", failed.ID); got.State != delegation.Failed {
		t.Fatalf("decided: %s", got.State)
	}
	if code, b := f.retry(failed.ID, `{"knows":["seen-text"],"seen":{"textLen":`+strconv.Itoa(4*4000)+`}}`); code != 409 || !strings.Contains(b, `"changed":"text"`) {
		t.Fatalf("a Try again of part of a 4001-character brief: %d %s", code, b)
	}
	if code, b := f.retry(failed.ID, `{"knows":["seen-text"],"seen":{"textLen":`+strconv.Itoa(4*4001)+`}}`); code != 200 {
		t.Fatalf("a Try again of all of a 4001-character brief: %d %s", code, b)
	}
	short := w.ask(f.alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", long(4000), "provider", "fake"))
	f.used(short, `{"grant":"once"}`)
	if code, b := f.retry(short.ID, `{}`); code != 200 {
		t.Fatalf("a Try again of a 4000-character brief without the word: %d %s", code, b)
	}
}

// A start that offers to continue a session says that session's own
// reach — its network scope and whether it can call tile APIs — from
// xbind's record of the session: the sheet shows them in place of a new
// session's Network and Other tiles rows (D253, the re-review's 3).
func TestAContinueSaysTheSessionsOwnReach(t *testing.T) {
	f := newFollowUps(t)
	info, ok := f.w.s.Term.Info(f.sid)
	if !ok || info.Net == "" || info.Label == "" {
		t.Fatalf("the session's record: %+v", info)
	}
	c, _ := sessOf(f.offer("Goal: a score board."))["continue"].(map[string]any)
	if c["net"] != info.Net || c["netLabel"] != info.Label || c["api"] != info.API {
		t.Fatalf("the offer %v, the session's record net %q (%q), api %v", c, info.Net, info.Label, info.API)
	}
}

// followUps is a world for follow-ups: the agent's own session on apps/x
// (sid), started and idle, and the person's and the agent's credentials.
type followUps struct {
	t             *testing.T
	w             *dWorld
	alice, person cred
	sid           string
}

func newFollowUps(t *testing.T) *followUps {
	t.Helper()
	w := realDelegateWorld(t, "--auto-mode")
	f := &followUps{t: t, w: w, alice: delegateCred("d-alice"), person: w.cookie("alice")}
	first := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(f.alice, "POST", "/api/xbin/term/sessions", first)
	w.approve(f.person, req.ID, "")
	var a struct{ ID string }
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", first, f.alice, header(HeaderTicket, req.ID)); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("the start: %d %s", rec.Code, rec.Body.String())
	}
	f.sid = a.ID
	return f
}

// offer is a follow-up into the session with brief, pending — filed again until xbind offers to continue it.
func (f *followUps) offer(brief string) delegation.Request {
	f.t.Helper()
	next := startBody("apps/x", brief, "continue", f.sid)
	for i := 0; ; i++ {
		briefTurnOver(f.t, f.w, f.sid)
		r := f.w.askKnows(f.alice, "POST", "/api/xbin/term/sessions", next, "retry")
		if sessOf(r)["continue"] != nil {
			return r
		}
		f.w.do(f.w.full, "DELETE", "/api/xbin/delegate/requests/"+r.ID, "", f.alice)
		if i > 200 {
			f.t.Fatal("never offered to continue")
		}
	}
}

// failedNew is a follow-up whose person started a new session instead, in a mode that asks — and the new session
// failed, Try again allowed.
func (f *followUps) failedNew(brief string) delegation.Request {
	f.t.Helper()
	r := f.offer(brief)
	f.used(r, `{"knows":["continue"],"continue":false,"harness":"fake","mode":"ask"}`)
	got, _ := f.w.store.Request("alice", r.ID)
	if got.Chosen == nil || got.Chosen.Continue != "" || got.Chosen.Mode != "ask" || sessOf(got)["continue"] == nil {
		f.t.Fatalf("approved as: %+v, the card %v", got.Chosen, sessOf(got))
	}
	return r
}

// used: r approved with body, its start made, and failed — Try again allowed.
func (f *followUps) used(r delegation.Request, body string) {
	f.t.Helper()
	if code, b := f.approve(r.ID, f.w.seen("alice", r.ID, body)); code != 200 {
		f.t.Fatalf("approve %s: %d %s", body, code, b)
	}
	got, _ := f.w.store.Request("alice", r.ID) // (the body it was filed with: the ticket binds it)
	if rec := f.w.do(f.w.full, "POST", "/api/xbin/term/sessions", got.Body, f.alice, header(HeaderTicket, r.ID)); rec.Code >= 300 {
		f.t.Fatalf("the approved start: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.w.store.MarkFailed("alice", r.ID, "the coding agent didn't start", true); err != nil {
		f.t.Fatal(err)
	}
}

// seen is a body's "seen" as the sheet of request id showed it: the sign-in it offers, its network, and more.
func (f *followUps) seen(id, more string) string {
	got, _ := f.w.store.Request("alice", id)
	net, _ := sessOf(got)["net"].(string)
	sid, _ := sessOf(got)["signInId"].(string)
	return `"seen":{"signInId":"` + sid + `","net":"` + net + `"` + more + `}`
}

// offered is the facts of the session r offers to continue, as seen says them.
func (f *followUps) offered(r delegation.Request) string {
	c := sessOf(r)["continue"].(map[string]any)
	return `,"harness":"` + c["provider"].(string) + `","mode":"` + c["mode"].(string) + `"`
}

func (f *followUps) approve(id, body string) (int, string) { return f.approveAs(f.person, id, body) }
func (f *followUps) approveAs(c cred, id, body string) (int, string) {
	rec := f.w.do(f.w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", body, c)
	return rec.Code, rec.Body.String()
}
func (f *followUps) retry(id, body string) (int, string) { return f.retryAs(f.person, id, body) }
func (f *followUps) retryAs(c cred, id, body string) (int, string) {
	rec := f.w.do(f.w.full, "POST", "/api/xbin/delegate/requests/"+id+"/retry", body, c)
	return rec.Code, rec.Body.String()
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
