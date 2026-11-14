package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/auth"
)

func prefsServer(t *testing.T) (http.Handler, *Server) {
	t.Helper()
	h, s := termServer(t)
	s.AgentPrefs = agentprefs.New(t.TempDir())
	s.AgentPrefs.Person = func(id string) (int64, bool) {
		u, ok := s.Auth.Users.Get(id)
		if !ok {
			return 0, false
		}
		return u.Created, true
	}
	return h, s
}

// covers D229 — a person's agent preferences over the API: their own
// session reads and merges them; a terminal's or an agent session's token
// (a tile's, or the workspace's), the root token and a principal viewing
// as them never do — what a workspace agent may start is in there.
func TestAgentPrefsRoutes(t *testing.T) {
	h, s := prefsServer(t)
	alice, bob := s.Auth.NewSession("alice", ""), s.Auth.NewSession("bob", "")
	do := func(sid, method, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin/agent/prefs", body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	const defaults = `{"harness":"","lastKind":"","harnesses":{},"agentSessions":{"start":"ask"}}`
	if c, b := do(alice, "GET", ""); c != 200 || b != defaults {
		t.Fatalf("defaults: %d %s", c, b)
	}
	c, b := do(alice, "PUT", `{"harness":"claude","lastKind":"agent","harnesses":{"claude":{"mode":"acceptEdits","model":"opus","options":{"effort":"high"}}},"agentSessions":{"start":"allow","modes":{"claude":"plan"},"net":"none","api":false}}`)
	if c != 200 || !strings.Contains(b, `"harness":"claude"`) || !strings.Contains(b, `"claude":{"mode":"acceptEdits","model":"opus","options":{"effort":"high"}}`) ||
		!strings.Contains(b, `"agentSessions":{"start":"allow","modes":{"claude":"plan"},"net":"none","api":false}`) {
		t.Fatalf("set: %d %s", c, b)
	}
	// a merge patch: one option changes, one harness is added, the rest stays
	c, b = do(alice, "PUT", `{"harnesses":{"claude":{"options":{"effort":null,"thinking":"on"}},"codex":{"mode":"agent"}}}`)
	if c != 200 || !strings.Contains(b, `"claude":{"mode":"acceptEdits","model":"opus","options":{"thinking":"on"}}`) || !strings.Contains(b, `"codex":{"mode":"agent"}`) ||
		!strings.Contains(b, `"start":"allow"`) {
		t.Fatalf("merge: %d %s", c, b)
	}
	if c, got := do(alice, "GET", ""); c != 200 || got != b {
		t.Errorf("reads back: %d %s", c, got)
	}
	if c, b := do(bob, "GET", ""); c != 200 || b != defaults {
		t.Errorf("another person's: %d %s", c, b)
	}
	for body, want := range map[string]string{
		`{"harnesses":{"claude":{"mode":"bypassPermissions"}}}`:      "picked per session",
		`{"harnesses":{"claude":{"mode":"nope"}}}`:                   "no mode",
		`{"agentSessions":{"modes":{"claude":"bypassPermissions"}}}`: "picked per session",
		`{"agentSessions":{"modes":{"opencode":"anything"}}}`:        "keeps asking",
		`{"agentSessions":{"modes":{"nope":"x"}}}`:                   "no harness",
		`{"agentSessions":{"start":"always"}}`:                       "ask",
		`{"harness":"nope"}`:                                         "no harness",
		`{"lastKind":"vm"}`:                                          "shell",
		`{"harnesses":{"claude":{"credentials":"mine"}}}`:            "own",
		`{"harnesses":{"claude":{"options":{"mode":"plan"}}}}`:       "the mode is",
		`{"harnesses":{"claude":{"secret":"x"}}}`:                    "secret",
		`{"token":"x"}`:                         "unknown key",
		`[]`:                                    "JSON object",
		`{"agentSessions":{"net":"NO SPACES"}}`: "network choice",
	} {
		if c, b := do(alice, "PUT", body); c != 400 || !strings.Contains(b, want) {
			t.Errorf("%s: %d %s, want 400 %q", body, c, b, want)
		}
	}
	if c, b := do(alice, "PUT", `{"harnesses":{"codex":null},"agentSessions":{"start":"ask","modes":null,"net":null,"api":null}}`); c != 200 ||
		strings.Contains(b, "codex") || !strings.Contains(b, `"agentSessions":{"start":"ask"}`) {
		t.Errorf("null removes: %d %s", c, b)
	}

	// credentials that name alice and are not her own session
	bearer := func(tok, method, body string) (int, string) {
		r := httptest.NewRequest(method, "/api/xbin/agent/prefs", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for name, tok := range map[string]string{
		"a tile terminal's token":      s.Auth.MintTerminal("apps/x", "alice"),
		"a workspace terminal's token": s.Auth.MintTerminal("", "alice"),
		"the root token":               s.Auth.OwnerTokenValue(),
	} {
		for _, m := range []string{"GET", "PUT"} {
			if c, b := bearer(tok, m, `{"agentSessions":{"start":"allow"}}`); c != 403 {
				t.Errorf("%s, %s: %d %s", name, m, c, b)
			}
		}
	}
	if got := s.AgentPrefs.Get("alice").AgentSessions.Start; got != agentprefs.StartAsk {
		t.Errorf("a refused write changed the preference: %s", got)
	}
	// the handler holds on its own too (were it reached past the route table)
	for name, p := range map[string]auth.Principal{
		"a frame":  {Component: "apps/x", Via: "frame", UserID: "alice"},
		"view-as":  {UserID: "alice", Impersonator: "dave"},
		"nobody":   {},
		"a ws pty": {Via: "terminal", UserID: "alice"},
	} {
		r := httptest.NewRequest("PUT", "/agent/prefs", strings.NewReader(`{"agentSessions":{"start":"allow"}}`))
		w := httptest.NewRecorder()
		s.apiAgentPrefsPut(w, r.WithContext(auth.WithPrincipal(context.Background(), p)))
		if w.Code != 403 {
			t.Errorf("%s reached the handler: %d %s", name, w.Code, w.Body.String())
		}
	}
}
