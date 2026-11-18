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
	// her workspace agent directs the sessions it starts, so a saved sign-in
	// is never what one runs on by itself (decision A): the default stays
	// the harness the workspace's model serves, and start-defaults says
	// nothing of the sign-in — not that it would be used, not its label, not
	// its id
	d = get()
	c, _ := d.of("claude")
	if d.Harness != "codex" || c.SignIn == "saved" || c.NoSignIn {
		t.Fatalf("with a saved sign-in: %q %+v", d.Harness, c)
	}
	if b := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice).Body.String(); strings.Contains(b, "Personal") || strings.Contains(b, "si_") || strings.Contains(b, `"saved"`) {
		t.Fatalf("start-defaults speaks of the sign-in: %s", b)
	}
	// …nor does the providers list, which her agent may read: that one exists, nothing of it
	if b := w.do(w.full, "GET", "/api/xbin/agent/providers?cwd=apps/x", "", alice).Body.String(); strings.Contains(b, "Personal") || strings.Contains(b, "si_") || strings.Contains(b, "setup-token") ||
		strings.Contains(b, `"trust"`) || !strings.Contains(b, `"saved":{"inject":false}`) {
		t.Fatalf("GET /agent/providers, read by her agent: %s", b)
	}
	// a start she is asked to approve OFFERS the sign-in, by id — the sheet
	// draws the label from her own list and its own line, unticked. Approved
	// without the tick, the ticket's start has none
	sid := w.s.Auth.NewSession("alice", "")
	own := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	ask := func() delegation.Request {
		return w.ask(alice, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"p"}`)
	}
	approve := func(id, body string, who cred) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", body, who)
		return rec.Code, rec.Body.String()
	}
	stored := func(id string) delegation.Request { r, _ := w.store.Request("alice", id); return r }
	mine, _, _ := w.s.Signins.List("alice")
	req := ask()
	sess, _ := req.Preview["session"].(map[string]any)
	if sess["signIn"] == "saved" || sess["signInId"] != mine[0].ID || sess["savedSignIn"] != nil || sess["signInTrust"] != nil {
		t.Fatalf("the approval's preview on her own tile: %v", sess)
	}
	if c, b := approve(req.ID, `{"grant":"once"}`, own); c != 200 {
		t.Fatalf("approve: %d %s", c, b)
	}
	if d, why := ticketStart(stored(req.ID).Preview, stored(req.ID).UseSignin); why != "" || d.Signin != "" || d.TrustAsked || !d.Approved {
		t.Fatalf("approved without the tick: the ticket's start %+v %q", d, why)
	}
	// ticked — by her, in her own session; never by the agent, never from a notification
	req = ask()
	if c, _ := approve(req.ID, `{"grant":"once","signin":true}`, alice); c == 200 || stored(req.ID).UseSignin {
		t.Fatalf("her agent approved its own start with the sign-in ticked: %d", c)
	}
	if c, b := approve(req.ID, `{"grant":"once","signin":true,"via":"notification"}`, own); c != 409 || stored(req.ID).State != delegation.Pending {
		t.Fatalf("the sign-in's line from a notification: %d %s", c, b)
	}
	if c, b := approve(req.ID, `{"grant":"once","trust":true}`, own); c != 400 || stored(req.ID).State != delegation.Pending {
		t.Fatalf("the trust line without the sign-in's: %d %s", c, b)
	}
	if c, b := approve(req.ID, `{"grant":"once","signin":true,"trust":true}`, own); c != 400 || stored(req.ID).State != delegation.Pending {
		t.Fatalf("a trust tick where the sheet carried no trust line: %d %s", c, b)
	}
	if c, b := approve(req.ID, `{"grant":"once","signin":true}`, own); c != 200 {
		t.Fatalf("approve with the sign-in ticked: %d %s", c, b)
	}
	if d, _ := ticketStart(stored(req.ID).Preview, stored(req.ID).UseSignin); d == nil || d.Signin != mine[0].ID || d.TrustAsked {
		t.Fatalf("approved with the tick: the ticket's start %+v", d)
	}
	// the tick is this start's: her preference to start unasked changes nothing of it
	w.policy.set(func(p *dPolicy) { p.prefs["alice"] = delegation.StartPrefs{Mode: c.Mode, API: true} })
	if c2, _ := get().of("claude"); c2.SignIn == "saved" || c2.NeedsTicket {
		t.Fatalf("an unasked start was promised her sign-in: %+v", c2)
	}
	w.policy.set(func(p *dPolicy) { delete(p.prefs, "alice") })

	// a tile whose source isn't only hers: the sheet carries the trust line
	// too — xbind's question, with the basis of what it shows
	others = []string{"bob"}
	req = ask()
	sess, _ = req.Preview["session"].(map[string]any)
	line, _ := sess["signInTrust"].(map[string]any)
	basis, _ := line["basis"].(string)
	if sess["signInId"] != mine[0].ID || line == nil || line["ownerKind"] != "you" || line["allowed"] != true || fmt.Sprint(line["editors"]) != "[bob]" || !strings.HasPrefix(basis, "tb2_") {
		t.Fatalf("the approval's preview on a tile bob can change: %v", sess)
	}
	// the sign-in ticked, the trust line not: approved, no trust recorded — the start is one that asked
	if c, b := approve(req.ID, `{"grant":"once","signin":true}`, own); c != 200 {
		t.Fatalf("approve: %d %s", c, b)
	}
	if d, _ := ticketStart(stored(req.ID).Preview, stored(req.ID).UseSignin); d == nil || !d.TrustAsked || d.Signin != mine[0].ID || len(w.s.Signins.Trusted("alice")) != 0 {
		t.Fatalf("the ticket's start: %+v; trusted %+v", d, w.s.Signins.Trusted("alice"))
	}
	// the tile changes between the question and her answer: the tick is for
	// what the sheet showed — refused, nothing decided, nothing recorded
	req = ask()
	others = []string{"bob", "mallory"}
	if c, b := approve(req.ID, `{"grant":"once","signin":true,"trust":true}`, own); c != 409 || !strings.Contains(b, "look again") || !strings.Contains(b, `"changed":true`) ||
		stored(req.ID).State != delegation.Pending || len(w.s.Signins.Trusted("alice")) != 0 {
		t.Fatalf("trusting a tile that changed since the sheet was drawn: %d %s (state %s, trusted %+v)", c, b, stored(req.ID).State, w.s.Signins.Trusted("alice"))
	}
	// both ticked, the tile as shown: her trust is on record — for that tile, as it stands — and the start carries the sign-in
	req = ask()
	c0, b0 := approve(req.ID, `{"grant":"once","signin":true,"trust":true}`, own)
	if tr := w.s.Signins.Trusted("alice"); c0 != 200 || len(tr) != 1 || tr[0].Tile != "apps/x" || tr[0].Via != "approval" || len(tr[0].Editors) != 2 || !stored(req.ID).UseSignin {
		t.Fatalf("approved with both lines ticked: %d %s — %+v", c0, b0, tr)
	}
	// her trust in the tile is no tick: the next start offers the line again, unticked, without the trust line
	if sess, _ = ask().Preview["session"].(map[string]any); sess["signInId"] != mine[0].ID || sess["signInTrust"] != nil || sess["signIn"] == "saved" {
		t.Fatalf("a start on the tile she trusts: %v", sess)
	}
	// a tile whose source she may not trust (the workspace's switch): the sheet offers no sign-in at all
	if _, err := w.s.Signins.SetTrustPolicy(false); err != nil {
		t.Fatal(err)
	}
	if sess, _ = ask().Preview["session"].(map[string]any); sess["signInId"] != "" || sess["signInTrust"] != nil {
		t.Fatalf("trusting forbidden: %v", sess)
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
