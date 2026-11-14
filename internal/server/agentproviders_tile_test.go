package server

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D229 — GET /agent/providers?cwd= offers only what the tile's orgs
// allow, and POST /term/sessions refuses the rest with the reason; without
// cwd (older clients, the app) the catalog is whole.
func TestAgentProvidersFollowTheTile(t *testing.T) {
	h, s := termServer(t)
	alice, bob := s.Auth.NewSession("alice", ""), s.Auth.NewSession("bob", "")
	why := errors.New("Finance allows these coding agents only: claude, codex (not gemini)")
	s.Term.AgentGate = func(p auth.Principal, tile, harness string) error {
		if tile == "apps/x" && (harness == "gemini" || harness == "opencode") {
			return why
		}
		return nil
	}
	get := func(sid, q string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/api/xbin/agent/providers"+q, "", sid))
		if w.Code != 200 {
			t.Fatalf("providers%s: %d %s", q, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	ids := func(body string) string {
		var out []string
		for _, id := range []string{"claude", "codex", "gemini", "opencode"} {
			if strings.Contains(body, `"id":"`+id+`"`) {
				out = append(out, id)
			}
		}
		return strings.Join(out, ",")
	}
	if got := ids(get(alice, "")); got != "claude,codex,gemini,opencode" {
		t.Errorf("no tile: %s", got)
	}
	if got := ids(get(alice, "?cwd=apps/x")); got != "claude,codex" {
		t.Errorf("the tile's harnesses: %s", got)
	}
	if got := ids(get(alice, "?cwd=/apps/y/")); got != "claude,codex,gemini,opencode" {
		t.Errorf("another tile: %s", got)
	}
	// bob can't read apps/x: he learns nothing about its rules
	if got := ids(get(bob, "?cwd=apps/x")); got != "claude,codex,gemini,opencode" {
		t.Errorf("a tile the caller can't read: %s", got)
	}
	s.Term.AgentGate = func(auth.Principal, string, string) error { return why }
	if got := strings.TrimSpace(get(alice, "?cwd=apps/x")); got != "[]" {
		t.Errorf("nothing allowed is an empty list: %s", got)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"gemini"}`, alice))
	if w.Code != 403 || !strings.Contains(w.Body.String(), why.Error()) {
		t.Errorf("create of a disallowed harness: %d %s", w.Code, w.Body.String())
	}
}
