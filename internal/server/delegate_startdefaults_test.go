package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

// An agent-started session defaults to a harness that can run (the
// journey's G8): with no saved choice, a preferred harness the connector
// can't serve and nobody is signed in to gives way to the first one the
// connector serves; a sign-in a session saw keeps the preferred one; the
// person's saved choice always wins; and the answer says which run with no
// sign-in. Without a connector offered to sessions, nothing changes.
func TestDelegateStartDefaultsRunnable(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	yes := true
	// what term's AgentLLM answers on a workspace whose connector serves
	// the OpenAI wire: Claude Code and Gemini need their own sign-in, Codex
	// and OpenCode run on the connector
	rows := func(claudeSignedIn *bool) map[string]*term.ProviderLLM {
		return map[string]*term.ProviderLLM{
			"claude":   {Use: "own", SignedIn: claudeSignedIn},
			"gemini":   {Use: "own"},
			"codex":    {Use: "own-first", Connector: true},
			"opencode": {Use: "connector", Connector: true},
		}
	}
	var llm map[string]*term.ProviderLLM
	saved := startLLMOf
	startLLMOf = func(_ *Server, _ auth.Principal, _ string, _ []agent.Provider) map[string]*term.ProviderLLM {
		return llm
	}
	t.Cleanup(func() { startLLMOf = saved })
	get := func() startDefaultsView {
		t.Helper()
		rec := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice)
		if rec.Code != 200 {
			t.Fatalf("start-defaults: %d %s", rec.Code, rec.Body.String())
		}
		return decode[startDefaultsView](t, rec)
	}
	first := get().Harnesses[0].ID // the catalog's preferred, as the rules allow it

	// no connector offered to sessions: today's default, and nothing runs without a sign-in
	if d := get(); d.Harness != first || anyNoSignIn(d) {
		t.Fatalf("without a connector: %+v", d)
	}

	// the connector serves Codex and OpenCode; nobody signed in to Claude Code
	llm = rows(nil)
	d := get()
	if first == "claude" && d.Harness != "codex" {
		t.Fatalf("the default with Claude Code unusable: %q (%+v)", d.Harness, d.Harnesses)
	}
	for _, h := range d.Harnesses {
		if want := h.ID == "codex" || h.ID == "opencode"; h.NoSignIn != want {
			t.Errorf("%s: noSignIn %v, want %v", h.ID, h.NoSignIn, want)
		}
	}
	// a start that names no harness opens on it too (the request's preview)
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","prompt":"p"}`)
	if sess, _ := req.Preview["session"].(map[string]any); sess["provider"] != d.Harness {
		t.Fatalf("an unnamed start opens on %v, the default is %s", sess["provider"], d.Harness)
	}

	// a session saw alice signed in to Claude Code: the preferred one runs
	llm = rows(&yes)
	if d := get(); d.Harness != first {
		t.Fatalf("signed in to the preferred one: %q", d.Harness)
	}

	// her saved choice (starts unasked, on Claude Code's mode) wins, even unusable
	llm = rows(nil)
	claude, _ := get().of("claude")
	w.policy.set(func(p *dPolicy) { p.prefs["alice"] = delegation.StartPrefs{Mode: claude.Mode, API: true} })
	t.Cleanup(func() { w.policy.set(func(p *dPolicy) { delete(p.prefs, "alice") }) })
	if d := get(); d.Harness != first {
		t.Fatalf("her saved choice: %q", d.Harness)
	}

	// no harness the connector serves: the preferred one, as today
	w.policy.set(func(p *dPolicy) { delete(p.prefs, "alice") })
	llm = map[string]*term.ProviderLLM{"claude": {Use: "own"}, "gemini": {Use: "own"}, "codex": {Use: "own"}, "opencode": {Use: "own"}}
	if d := get(); d.Harness != first || anyNoSignIn(d) {
		t.Fatalf("nothing runs on the connector: %+v", d)
	}
}

func anyNoSignIn(d startDefaultsView) bool {
	for _, h := range d.Harnesses {
		if h.NoSignIn {
			return true
		}
	}
	return false
}

// covers D230 — a brief's card is truthful about sign-ins: with a saved
// sign-in the preferred harness is usable and named as running on it;
// where the shared-tile rule leaves it out, the connector-served harness is
// the default again; a harness nobody is signed in to says "needed". The
// agent is refused the sign-ins themselves.
func TestDelegateStartDefaultsSavedSignIn(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	w.s.Signins = signins.New(signins.Options{Dir: filepath.Join(t.TempDir(), "signins"), AllowPlaintext: true})
	var others []string
	owner := ""
	w.s.Term.Signins, w.s.Term.SigninDev = w.s.Signins, true
	w.s.Term.SigninSource = testSource(&owner, &others)
	llm := map[string]*term.ProviderLLM{
		"claude": {Use: "own"}, "gemini": {Use: "own"},
		"codex": {Use: "own-first", Connector: true}, "opencode": {Use: "connector", Connector: true},
	}
	saved := startLLMOf
	startLLMOf = func(_ *Server, _ auth.Principal, _ string, _ []agent.Provider) map[string]*term.ProviderLLM {
		out := map[string]*term.ProviderLLM{}
		for k, v := range llm {
			c := *v
			out[k] = &c
		}
		return out
	}
	t.Cleanup(func() { startLLMOf = saved })
	get := func() startDefaultsView {
		t.Helper()
		rec := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice)
		if rec.Code != 200 {
			t.Fatalf("start-defaults: %d %s", rec.Code, rec.Body.String())
		}
		return decode[startDefaultsView](t, rec)
	}
	first := get().Harnesses[0].ID
	if first != "claude" {
		t.Skipf("the catalog's preferred harness here is %s", first)
	}
	// none saved: Claude Code can't run, the connector's first harness is the default
	d := get()
	if c, _ := d.of("claude"); d.Harness != "codex" || c.SignIn == "saved" {
		t.Fatalf("nothing saved: %q %+v", d.Harness, c)
	}
	if x, _ := d.of("codex"); x.SignIn != "connector" || !x.NoSignIn {
		t.Fatalf("codex on the connector: %+v", x)
	}

	if _, _, err := w.s.Signins.Save("alice", "claude", "Personal", fakeSigninToken, false); err != nil {
		t.Fatal(err)
	}
	d = get()
	c, _ := d.of("claude")
	if d.Harness != "claude" || c.SignIn != "saved" || c.NoSignIn {
		t.Fatalf("with a saved sign-in: %q %+v", d.Harness, c)
	}
	// the agent's model learns that a saved sign-in exists — never its label
	// or its id (review finding 7): those are on the approval, for the person
	if b := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice).Body.String(); strings.Contains(b, "Personal") || strings.Contains(b, "si_") {
		t.Fatalf("start-defaults names the sign-in: %s", b)
	}
	// a start she is asked to approve says what it would use, by id: the
	// sheet shows the label from her own list, and the open is bound to it
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"p"}`)
	sess, _ := req.Preview["session"].(map[string]any)
	mine, _, _ := w.s.Signins.List("alice")
	if sess["signIn"] != "saved" || sess["signInId"] != mine[0].ID || sess["savedSignIn"] != nil {
		t.Fatalf("the approval's preview: %v", sess)
	}
	if sess["signInTrust"] != nil {
		t.Fatalf("her own tile carries a trust line: %v", sess["signInTrust"])
	}
	if d, why := ticketStart(req.Preview); why != "" || d.Signin != mine[0].ID || d.TrustAsked || !d.Approved {
		t.Fatalf("the ticket's start: %+v %q", d, why)
	}
	// the default is on: where her preference lets the agent start unasked
	// on her own tile, the session gets her sign-in and the answer says so
	w.policy.set(func(p *dPolicy) { p.prefs["alice"] = delegation.StartPrefs{Mode: c.Mode, API: true} })
	if c2, _ := get().of("claude"); c2.SignIn != "saved" || c2.NeedsTicket {
		t.Fatalf("an unasked start on her own tile: %+v", c2)
	}
	// …and never into a tile whose source isn't only hers, until she trusts it
	w.s.Term.NoteHomeLogin("alice", "claude", false)
	others = []string{"bob"}
	if c2, _ := get().of("claude"); c2.SignIn != "needed" || c2.NeedsTicket {
		t.Fatalf("an unasked start on a tile bob can change was promised her sign-in: %+v", c2)
	}
	w.policy.set(func(p *dPolicy) { delete(p.prefs, "alice") })
	// a start she is asked to approve there names the sign-in AND carries
	// the trust line; approving without ticking it records no trust, and
	// the ticket's start is one that asked
	d = get()
	c, _ = d.of("claude")
	if d.Harness != "claude" || c.SignIn != "saved" || !c.NeedsTicket {
		t.Fatalf("a tile bob can change, a start she approves: %q %+v", d.Harness, c)
	}
	sid := w.s.Auth.NewSession("alice", "")
	own := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	ask := func() delegation.Request {
		return w.ask(alice, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"p"}`)
	}
	req = ask()
	sess, _ = req.Preview["session"].(map[string]any)
	line, _ := sess["signInTrust"].(map[string]any)
	if sess["signIn"] != "saved" || sess["signInId"] != mine[0].ID || line == nil || line["ownerKind"] != "you" || line["allowed"] != true || fmt.Sprint(line["editors"]) != "[bob]" {
		t.Fatalf("the approval's preview on a tile bob can change: %v", sess)
	}
	if d, _ := ticketStart(req.Preview); d == nil || !d.TrustAsked || d.Signin != mine[0].ID {
		t.Fatalf("the ticket's start: %+v", d)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once"}`, own); rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if len(w.s.Signins.Trusted("alice")) != 0 {
		t.Fatal("approving a start without the trust line recorded her trust")
	}
	// the agent can't tick it for her, nor from a notification
	req = ask()
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once","trust":true}`, alice); rec.Code == 200 || len(w.s.Signins.Trusted("alice")) != 0 {
		t.Fatalf("her agent approved its own start with the trust line: %d", rec.Code)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once","trust":true,"via":"notification"}`, own); rec.Code != 409 || len(w.s.Signins.Trusted("alice")) != 0 {
		t.Fatalf("the trust line from a notification: %d %s", rec.Code, rec.Body.String())
	}
	// ticked: her trust is on record — for that tile, as it stands — and a
	// start there no longer asks
	rec0 := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once","trust":true}`, own)
	if tr := w.s.Signins.Trusted("alice"); rec0.Code != 200 || len(tr) != 1 || tr[0].Tile != "apps/x" || tr[0].Via != "approval" || len(tr[0].Editors) != 1 {
		t.Fatalf("approved with the trust line: %d %s — %+v", rec0.Code, rec0.Body.String(), tr)
	}
	if sess, _ = ask().Preview["session"].(map[string]any); sess["signIn"] != "saved" || sess["signInTrust"] != nil {
		t.Fatalf("a start on the tile she trusts: %v", sess)
	}
	// a tile whose source she may not trust (the workspace's switch): the
	// sheet names no sign-in at all
	if _, err := w.s.Signins.SetTrustPolicy(false); err != nil {
		t.Fatal(err)
	}
	if sess, _ = ask().Preview["session"].(map[string]any); sess["signIn"] == "saved" || sess["signInId"] != "" || sess["signInTrust"] != nil {
		t.Fatalf("trusting forbidden: %v", sess)
	}
	d = get()
	c, _ = d.of("claude")
	if d.Harness != "codex" || c.SignIn != "needed" {
		t.Fatalf("trusting forbidden: %q %+v", d.Harness, c)
	}
	// the answer never carries the sign-in's id or anything of its secret
	rec := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice)
	if b := rec.Body.String(); strings.Contains(b, "FAKEFAKE") || strings.Contains(b, "si_") {
		t.Fatalf("start-defaults says too much: %s", b)
	}
	// …and the agent is refused the sign-ins themselves
	for _, try := range [][3]string{{"GET", "/api/xbin/agent/signins", ""}, {"GET", "/api/xbin/agent/signins/uses", ""},
		{"POST", "/api/xbin/agent/signins", `{"harness":"claude","secret":"` + fakeSigninKey + `"}`}, {"POST", "/api/xbin/agent/signins/guided", `{"harness":"claude"}`}} {
		if rec := w.do(w.full, try[0], try[1], try[2], alice); rec.Code != 403 {
			t.Errorf("her agent, %s %s: %d %s", try[0], try[1], rec.Code, rec.Body.String())
		}
	}
}
