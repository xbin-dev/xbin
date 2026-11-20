package agentprefs

import (
	"fmt"
	"os"
	"path/filepath"
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
		"agentSessions":{"start":"allow","modes":{"claude":"plan"},"net":"none","api":false}}`)); err != nil {
		t.Fatal(err)
	}
	if a := s.Get("ana").AgentStart(claude); a.Ask || a.Mode != "plan" || a.Net != "none" || a.API || a.Model != "opus" {
		t.Errorf("as set: %+v", a)
	}
	if a := s.Get("ana").AgentStart(opencode); a.Mode != "" {
		t.Errorf("OpenCode has no mode that asks (D250 review M2): %+v", a)
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
	if got, ok := s.Get("ana").AgentSessions.Modes["claude"]; ok {
		t.Fatalf("a stored bypass mode read back (ignored at read, D250 re-review): %q", got)
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

// "Remember for this tile" (D250): the person's last approved choice per
// tile — never a bypass mode, at most MaxTiles, the oldest going first —
// and a preferred coding agent, checked like every other key.
func TestRememberTile(t *testing.T) {
	s := New(t.TempDir())
	s.Person = func(string) (int64, bool) { return 7, true }
	if err := s.RememberTile("ada", "apps/eui", TileStart{Harness: "codex", Model: "m1", Mode: "read-only"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTile("ada", "apps/x", TileStart{Harness: "claude", Mode: "bypassPermissions"}, 2); err != nil {
		t.Fatal(err)
	}
	p := s.Get("ada")
	if got := p.AgentSessions.Tiles["apps/eui"]; got.Harness != "codex" || got.Model != "m1" || got.Mode != "read-only" {
		t.Fatalf("apps/eui: %+v", got)
	}
	if got := p.AgentSessions.Tiles["apps/x"]; got.Mode != "" {
		t.Fatalf("a bypass mode was remembered: %+v", got)
	}
	if r, _ := s.TileChoice("bob", "apps/eui"); r != nil {
		t.Fatalf("bob reads ada's choice: %+v", r)
	}
	// the next pick replaces the whole entry: a mode the last pick had no word on doesn't linger
	_ = s.RememberTile("ada", "apps/eui", TileStart{Harness: "claude"}, 3)
	if got := s.Get("ada").AgentSessions.Tiles["apps/eui"]; got.Harness != "claude" || got.Mode != "" || got.Model != "" {
		t.Fatalf("replaced: %+v", got)
	}
	for i := 0; i < MaxTiles+5; i++ {
		_ = s.RememberTile("ada", fmt.Sprintf("apps/t%03d", i), TileStart{Harness: "claude"}, int64(10+i))
	}
	tiles := s.Get("ada").AgentSessions.Tiles
	if len(tiles) != MaxTiles || tiles["apps/t000"].Harness != "" || tiles[fmt.Sprintf("apps/t%03d", MaxTiles+4)].Harness != "claude" {
		t.Fatalf("bounded: %d tiles", len(tiles))
	}
	for _, bad := range []string{`{"agentSessions":{"harness":"no-such"}}`, `{"agentSessions":{"tiles":{"../x":{"harness":"claude"}}}}`,
		`{"agentSessions":{"tiles":{"apps/y":{"harness":"claude","mode":"bypassPermissions"}}}}`} {
		if _, err := s.Update("ada", []byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if p, err := s.Update("ada", []byte(`{"agentSessions":{"harness":"codex"}}`)); err != nil || p.AgentSessions.Harness != "codex" {
		t.Fatalf("the preferred coding agent: %+v %v", p.AgentSessions, err)
	}
}

// OpenCode has no mode xbin can vouch asks before it acts (D250 review
// M2): a person's "start without asking" never starts it — no asking mode
// to start it in — and the mode a session it would start in is the same
// asking mode start-defaults says (acp.Provider.AskingMode: one source).
func TestOpenCodeNeverStartsUnasked(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"start":"allow"}}`)); err != nil {
		t.Fatal(err)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "opencode"); ok || mode != "" {
		t.Fatalf("OpenCode without asking: mode %q, ok %v", mode, ok)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "claude"); !ok || mode != "default" {
		t.Fatalf("Claude Code without asking: mode %q, ok %v", mode, ok)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"modes":{"opencode":"plan"}}}`)); err == nil {
		t.Fatal("OpenCode's plan was saved as a mode that keeps asking")
	}
}

// "Remember for this tile" never keeps a mode that acts without asking,
// for any coding agent — decided here, by the predicate the approval's
// warning uses (acp.Provider.Asks) — and nothing of a coding agent with no
// mode that asks (OpenCode); a value that slipped past (an older file) is
// ignored when read (D250 review M1).
func TestRememberNeverKeepsAModeThatActsWithoutAsking(t *testing.T) {
	s, _ := testStore(t)
	if err := s.RememberTile("ana", "apps/x", TileStart{Harness: "claude", Model: "opus", Mode: "acceptEdits"}, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/x"]; got.Harness != "claude" || got.Model != "opus" || got.Mode != "" {
		t.Fatalf("a remembered pick in acceptEdits: %+v", got)
	}
	// OpenCode — no mode that asks — is the person's to choose: its coding agent and model kept, never its mode
	// (D250 re-review item 3)
	if err := s.RememberTile("ana", "apps/y", TileStart{Harness: "opencode", Model: "big", Mode: "build"}, 2); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/y"]; got.Harness != "opencode" || got.Model != "big" || got.Mode != "" {
		t.Fatalf("OpenCode remembered for a tile: %+v", got)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"tiles":{"apps/z":{"harness":"codex","mode":"agent"}}}}`)); err == nil {
		t.Fatal("a mode that acts without asking was saved for a tile")
	}
	if p, err := s.Update("ana", []byte(`{"agentSessions":{"harness":"opencode"}}`)); err != nil || p.AgentSessions.Harness != "opencode" {
		t.Fatalf("OpenCode as the person's coding agent for agent-started sessions: %v %+v", err, p.AgentSessions)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"tiles":{"apps/z":{"harness":"opencode","mode":"build"}}}}`)); err == nil {
		t.Fatal("OpenCode's build saved for a tile")
	}
	// an older file that holds modes that don't ask: ignored at read
	raw := `{"person":100,"agentSessions":{"harness":"opencode","tiles":{"apps/x":{"harness":"claude","mode":"auto"},"apps/y":{"harness":"opencode","mode":"build"}}}}`
	if err := os.WriteFile(s.path("ana"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := s.Get("ana")
	if got := p.AgentSessions.Tiles["apps/x"]; got.Harness != "claude" || got.Mode != "" {
		t.Fatalf("an older remembered auto: %+v", got)
	}
	if got := p.AgentSessions.Tiles["apps/y"]; got.Harness != "opencode" || got.Mode != "" || p.AgentSessions.Harness != "opencode" {
		t.Fatalf("an older remembered OpenCode: %+v", p.AgentSessions)
	}
}

// A file a released xbind wrote loads, and never blocks a later write
// (D250 re-review, item 1): v0.3.83 let a person save OpenCode's "plan" as
// the mode their agent's sessions start in (it was an asking mode then).
// Read now it is ignored — never an error — so the Agent tab launcher's PUT
// on every by-hand start, and Remember for this tile, still write.
func TestAReleasedFileNeverBlocksAWrite(t *testing.T) {
	s, _ := testStore(t)
	raw := `{"harness":"opencode","lastKind":"agent","harnesses":{"opencode":{"mode":"build"},"claude":{"mode":"acceptEdits","model":"opus"}},` +
		`"agentSessions":{"start":"allow","modes":{"opencode":"plan","claude":"plan"},"net":"none","api":true},"person":100}`
	if err := os.MkdirAll(filepath.Dir(s.path("ana")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("ana"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := s.Get("ana")
	if _, ok := p.AgentSessions.Modes["opencode"]; ok || p.AgentSessions.Modes["claude"] != "plan" || p.Harnesses["claude"].Model != "opus" {
		t.Errorf("the released file as read now: %+v", p.AgentSessions)
	}
	if _, err := s.Update("ana", []byte(`{"lastKind":"agent","harness":"claude"}`)); err != nil {
		t.Fatalf("the launcher's write over a released file: %v", err)
	}
	if err := s.RememberTile("ana", "apps/x", TileStart{Harness: "claude", Model: "sonnet"}, 1); err != nil {
		t.Fatalf("Remember over a released file: %v", err)
	}
	if got := s.Get("ana"); got.Harness != "claude" || got.AgentSessions.Tiles["apps/x"].Model != "sonnet" || got.AgentSessions.Modes["claude"] != "plan" {
		t.Fatalf("after the writes: %+v", got)
	}
	// a new write of a mode that acts without asking is still refused, saying why
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"modes":{"opencode":"plan"}}}`)); err == nil {
		t.Fatal("OpenCode's plan saved as an asking mode")
	}
}
