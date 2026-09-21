package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// The agent routes' gates over the term server (no bx binary: creation
// stops at 503 after every gate has passed). The session itself is
// exercised in internal/term with the scripted agent.
func TestAgentRoutesGates(t *testing.T) {
	h, s := termServer(t)
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if c, b := do(bob, "GET", "/agent/providers", ""); c != 200 || !strings.Contains(b, `"id":"claude"`) || !strings.Contains(b, `"explicit":true`) {
		t.Fatalf("providers: %d %s", c, b)
	}
	// kind is required; a user without terminal level is refused; an admin
	// passes the gates and stops at the missing bx (503)
	if c, _ := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","provider":"claude"}`); c != 400 {
		t.Fatalf("no kind: %d", c)
	}
	if c, _ := do(bob, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude"}`); c != 403 {
		t.Fatalf("user: %d", c)
	}
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"nope"}`); c != 400 || !strings.Contains(b, "unknown provider") {
		t.Fatalf("provider: %d %s", c, b)
	}
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions"}`); c != 503 || !strings.Contains(b, "bx binary") {
		t.Fatalf("admin, no bx: %d %s", c, b)
	}
	// per-session routes: unknown ids are 404 for everyone
	for _, r := range [][3]string{{"GET", "/term/sessions/nope", ""}, {"GET", "/term/sessions/nope/events", ""}, {"GET", "/term/sessions/nope/log", ""},
		{"POST", "/term/sessions/nope/prompt", `{"text":"hi"}`}, {"POST", "/term/sessions/nope/cancel", ""},
		{"POST", "/term/sessions/nope/permissions/p1", `{"decision":"allow_once"}`}, {"DELETE", "/term/sessions/nope", ""}} {
		if c, b := do(alice, r[0], r[1], r[2]); c != 404 {
			t.Fatalf("%s %s: %d %s", r[0], r[1], c, b)
		}
	}
	// the driving routes are the data plane for the audit log; create/end are not
	if auditable("POST", "/term/sessions/a1/prompt") || auditable("POST", "/term/sessions/a1/cancel") || auditable("POST", "/term/sessions/a1/permissions/p1") {
		t.Fatal("driving an agent must not be audited per call")
	}
	if !auditable("POST", "/term/sessions") || !auditable("DELETE", "/term/sessions/a1") {
		t.Fatal("creating/ending a session is audited")
	}
}

func TestSessionEventFilter(t *testing.T) {
	ev := events.Event{Type: "session", Topic: "session.a1", Component: "apps/x",
		Data: term.SessionEvent{Event: agent.Event{Seq: 1, Type: "status"}, User: "alice", ID: "a1"}}
	alice := auth.Principal{UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: "user"}}
	bob := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: "user"}}
	if !termEventFor(alice, ev) || termEventFor(bob, ev) || !termEventFor(auth.Principal{Owner: true}, ev) {
		t.Fatal("session events go to the owner and admins")
	}
	// a terminal token of the same user (bx agent attach inside a shell) too
	tok := auth.Principal{Component: "apps/x", UserID: "alice", Via: "terminal", User: alice.User}
	if !termEventFor(tok, ev) {
		t.Fatal("the user's terminal token is the user")
	}
	// the SessionEvent flattens on the wire
	s := &Server{Hub: events.NewHub()}
	ch, cancel := s.Hub.Subscribe(func(events.Event) bool { return true })
	defer cancel()
	s.SessionEvent("apps/x", term.SessionEvent{Event: agent.Event{Seq: 7, Type: "turn.end"}, User: "alice", ID: "a1"})
	got := <-ch
	if got.Type != "session" || got.Topic != "session.a1" || got.Data.(term.SessionEvent).Seq != 7 {
		t.Fatalf("published: %+v", got)
	}
}
