package server

import (
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
	others := []string{}
	w.s.Term.Signins, w.s.Term.SigninDev = w.s.Signins, true
	w.s.Term.SigninOthers = func(user, tile string) []string { return others }
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
	if c, _ := d.of("claude"); d.Harness != "codex" || c.SignIn == "saved" || c.SavedSignIn != "" {
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
	if d.Harness != "claude" || c.SignIn != "saved" || c.SavedSignIn != "Personal" || c.NoSignIn {
		t.Fatalf("with a saved sign-in: %q %+v", d.Harness, c)
	}
	// a session saw no login of Claude Code's in her home, and the tile is
	// shared: the saved sign-in is left out, the card says a sign-in is needed
	w.s.Term.NoteHomeLogin("alice", "claude", false)
	others = []string{"bob"}
	d = get()
	c, _ = d.of("claude")
	if d.Harness != "codex" || c.SignIn != "needed" || c.SavedSignIn != "" {
		t.Fatalf("a shared tile: %q %+v", d.Harness, c)
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
