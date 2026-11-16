package server

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
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
