package agentprefs

import (
	"os"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
)

func testStore(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	s := New(t.TempDir())
	people := map[string]int64{"ana": 100, "ben": 200}
	s.Person = func(id string) (int64, bool) { c, ok := people[id]; return c, ok }
	return s, people
}

// covers D229 — what a person's workspace agent starts a session with:
// asks by default, the provider's asking mode by default, the person's
// choice when it is a mode known to keep asking — re-checked on every read,
// whatever the file holds.
func TestAgentStart(t *testing.T) {
	s, _ := testStore(t)
	claude, _ := agent.Lookup("claude")
	codex, _ := agent.Lookup("codex")
	opencode, _ := agent.Lookup("opencode")
	if a := s.Get("ana").AgentStart(claude); !a.Ask || a.Mode != "default" || a.Net != "" || !a.API || a.Model != "" {
		t.Fatalf("the defaults: %+v", a)
	}
	if mode, net, api, ok := s.StartPrefs("ana", "claude"); ok || mode != "default" || net != "" || !api {
		t.Fatalf("StartPrefs by default (never without asking): %s %s %v %v", mode, net, api, ok)
	}
	if a := s.Get("ana").AgentStart(codex); a.Mode != "read-only" {
		t.Errorf("codex's asking mode: %+v", a)
	}
	if _, err := s.Update("ana", []byte(`{"harnesses":{"claude":{"model":"opus","mode":"acceptEdits"}},
		"agentSessions":{"start":"allow","modes":{"claude":"plan","opencode":"plan"},"net":"none","api":false}}`)); err != nil {
		t.Fatal(err)
	}
	if a := s.Get("ana").AgentStart(claude); a.Ask || a.Mode != "plan" || a.Net != "none" || a.API || a.Model != "opus" {
		t.Errorf("as set: %+v", a)
	}
	if a := s.Get("ana").AgentStart(opencode); a.Mode != "plan" {
		t.Errorf("a mode outside the table the catalog knows is safe: %+v", a)
	}
	if a := s.Get("ana").AgentStart(codex); a.Mode != "read-only" || a.Ask {
		t.Errorf("a harness with no mode set: %+v", a)
	}
	if a, ok := s.AgentStartFor("ana", "claude"); !ok || a.Mode != "plan" {
		t.Errorf("AgentStartFor: %+v %v", a, ok)
	}
	if a, ok := s.AgentStartFor("ana", "nope"); ok || !a.Ask {
		t.Errorf("a harness this xbind doesn't run: %+v %v", a, ok)
	}
	if mode, net, api, ok := s.StartPrefs("ana", "claude"); !ok || mode != "plan" || net != "none" || api {
		t.Errorf("StartPrefs once allowed: %s %s %v %v", mode, net, api, ok)
	}
	if _, _, _, ok := s.StartPrefs("ana", "nope"); ok {
		t.Error("StartPrefs for a harness this xbind doesn't run")
	}
	if _, _, _, ok := s.StartPrefs("ben", "claude"); ok {
		t.Error("another person's preference let ben's agent start")
	}
	// a file edited by hand (or by a newer xbind) to a bypass mode: never used
	b, _ := os.ReadFile(s.path("ana"))
	if err := os.WriteFile(s.path("ana"), []byte(strings.Replace(string(b), `"claude": "plan"`, `"claude": "bypassPermissions"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Modes["claude"]; got != "bypassPermissions" {
		t.Fatalf("the edit didn't take: %q", got)
	}
	if a := s.Get("ana").AgentStart(claude); a.Mode != "default" {
		t.Errorf("a stored bypass mode was used: %+v", a)
	}
}

// covers D229 — a user id given to a new person: the launcher's
// conveniences carry over like the home directory does; "my agent may
// start sessions" does not.
func TestPrefsAreThePersons(t *testing.T) {
	s, people := testStore(t)
	if _, err := s.Update("ana", []byte(`{"harness":"codex","agentSessions":{"start":"allow","net":"internet"}}`)); err != nil {
		t.Fatal(err)
	}
	if p := s.Get("ana"); p.AgentSessions.Start != StartAllow || p.Harness != "codex" {
		t.Fatalf("ana's: %+v", p)
	}
	people["ana"] = 999 // deleted, and another ana made
	p := s.Get("ana")
	if p.AgentSessions.Start != StartAsk || p.AgentSessions.Net != "" || p.Harness != "codex" {
		t.Errorf("the new ana inherits the old one's agent rule: %+v", p)
	}
	// her own write makes the file hers; the old rule doesn't come back
	if p, err := s.Update("ana", []byte(`{"lastKind":"shell"}`)); err != nil || p.AgentSessions.Start != StartAsk {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := s.Update("ana", []byte(`{"agentSessions":{"start":"allow"}}`)); err != nil || p.AgentSessions.Start != StartAllow || s.Get("ana").AgentSessions.Start != StartAllow {
		t.Errorf("the new ana's own choice: %+v %v", p, err)
	}
	// someone the workspace doesn't know (deleted since): the defaults, and nothing is theirs to allow
	delete(people, "ben")
	if p, err := s.Update("ben", []byte(`{"agentSessions":{"start":"allow"}}`)); err != nil || p.AgentSessions.Start != StartAsk {
		t.Errorf("nobody's agent rule: %+v %v", p, err)
	}
	if _, err := s.Update("", []byte(`{}`)); err == nil {
		t.Error("preferences with no person")
	}
	// a file that can't be read is the defaults, and is never overwritten
	if err := os.WriteFile(s.path("ana"), []byte(`{"harness": 7`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := s.Get("ana"); p.Harness != "" || p.AgentSessions.Start != StartAsk {
		t.Errorf("an unreadable file: %+v", p)
	}
	if _, err := s.Update("ana", []byte(`{"harness":"claude"}`)); err == nil {
		t.Error("an unreadable file was overwritten")
	}
	if b, _ := os.ReadFile(s.path("ana")); string(b) != `{"harness": 7` {
		t.Errorf("the file changed: %s", b)
	}
}

// A session the person's agent starts without asking opens only in a mode
// that asks before every edit and command: the others can't be saved.
func TestAgentSessionModesMustAsk(t *testing.T) {
	s := New(t.TempDir())
	for mode, ok := range map[string]bool{"default": true, "plan": true, "acceptEdits": false, "auto": false, "bypassPermissions": false} {
		_, err := s.Update("ana", []byte(`{"agentSessions":{"start":"allow","modes":{"claude":"`+mode+`"}}}`))
		if (err == nil) != ok {
			t.Errorf("saving claude %s for agent-started sessions: %v", mode, err)
		}
	}
	for harness, mode := range map[string]string{"codex": "agent", "gemini": "autoEdit", "opencode": "build"} {
		if _, err := s.Update("ana", []byte(`{"agentSessions":{"modes":{"`+harness+`":"`+mode+`"}}}`)); err == nil {
			t.Errorf("%s %s was saved for agent-started sessions", harness, mode)
		}
	}
}
