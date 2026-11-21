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

// covers D229 — what a person's workspace agent starts a session with
// when it starts one without the sheet: asks by default; with the
// person's "start without asking", only in a mode that asks they named —
// the default of their agent's sessions acts without asking (D251), so
// without one the start goes to the sheet — re-checked on every read.
func TestAgentStart(t *testing.T) {
	s, _ := testStore(t)
	claude, _ := agent.Lookup("claude")
	codex, _ := agent.Lookup("codex")
	opencode, _ := agent.Lookup("opencode")
	if a := s.Get("ana").AgentStart(claude); !a.Ask || a.Mode != "" || a.Net != "" || !a.API || a.Model != "" {
		t.Fatalf("the defaults: %+v", a)
	}
	if mode, net, api, ok := s.StartPrefs("ana", "claude"); ok || mode != "" || net != "" || !api {
		t.Fatalf("StartPrefs by default (never without the sheet): %s %s %v %v", mode, net, api, ok)
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
	if a := s.Get("ana").AgentStart(codex); a.Mode != "" || a.Ask {
		t.Errorf("a harness the person named no mode for: no start without the sheet (D251): %+v", a)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "codex"); ok || mode != "" {
		t.Errorf("StartPrefs for it: %q %v", mode, ok)
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
	if a := s.Get("ana").AgentStart(claude); a.Mode != "" {
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

// The mode a person prefers for the sessions their agent starts is any of
// a harness's modes but a bypass one (D251: a mode is a choice like the
// coding agent) — and a start without the sheet takes it only where it
// asks.
func TestAgentSessionModesAreAnyButBypass(t *testing.T) {
	s, _ := testStore(t)
	for mode, ok := range map[string]bool{"default": true, "plan": true, "acceptEdits": true, "auto": true, "bypassPermissions": false} {
		_, err := s.Update("ana", []byte(`{"agentSessions":{"start":"allow","modes":{"claude":"`+mode+`"}}}`))
		if (err == nil) != ok {
			t.Errorf("saving claude %s for agent-started sessions: %v", mode, err)
		}
		if mode, _, _, unasked := s.StartPrefs("ana", "claude"); err == nil && unasked != (mode == "default" || mode == "plan") {
			t.Errorf("claude %s: a start without the sheet %v", mode, unasked)
		}
	}
	for harness, mode := range map[string]string{"codex": "agent", "gemini": "autoEdit", "opencode": "build"} {
		if _, err := s.Update("ana", []byte(`{"agentSessions":{"modes":{"`+harness+`":"`+mode+`"}}}`)); err != nil {
			t.Errorf("%s %s: %v", harness, mode, err)
		}
		if _, _, _, unasked := s.StartPrefs("ana", harness); unasked {
			t.Errorf("%s %s: started without the sheet in a mode that doesn't ask", harness, mode)
		}
	}
	for _, bad := range []string{`{"agentSessions":{"modes":{"codex":"agent-full-access"}}}`, `{"agentSessions":{"modes":{"gemini":"yolo"}}}`} {
		if _, err := s.Update("ana", []byte(bad)); err == nil {
			t.Errorf("a bypass mode saved: %s", bad)
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

// A start without the sheet never opens a session in a mode that doesn't
// ask (D251): OpenCode has none that asks (D250 review M2), so the
// person's "start without asking" never starts it, whatever mode they
// prefer for it; Claude Code only in a mode that asks they named.
func TestNoStartWithoutTheSheetInAModeThatDoesntAsk(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"start":"allow","modes":{"opencode":"plan"}}}`)); err != nil {
		t.Fatal(err)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "opencode"); ok || mode != "" {
		t.Fatalf("OpenCode without the sheet: mode %q, ok %v", mode, ok)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "claude"); ok || mode != "" {
		t.Fatalf("Claude Code with no mode named: mode %q, ok %v", mode, ok)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"modes":{"claude":"default"}}}`)); err != nil {
		t.Fatal(err)
	}
	if mode, _, _, ok := s.StartPrefs("ana", "claude"); !ok || mode != "default" {
		t.Fatalf("Claude Code in the asking mode named: mode %q, ok %v", mode, ok)
	}
}

// "Remember for this tile" keeps any mode but a bypass one, as it keeps
// the coding agent and the model (D251) — OpenCode's too; a bypass mode a
// file holds is ignored when read.
func TestRememberKeepsAnyModeButBypass(t *testing.T) {
	s, _ := testStore(t)
	if err := s.RememberTile("ana", "apps/x", TileStart{Harness: "claude", Model: "opus", Mode: "acceptEdits"}, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/x"]; got.Harness != "claude" || got.Model != "opus" || got.Mode != "acceptEdits" {
		t.Fatalf("a remembered pick in acceptEdits: %+v", got)
	}
	if err := s.RememberTile("ana", "apps/y", TileStart{Harness: "opencode", Model: "big", Mode: "build"}, 2); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/y"]; got.Harness != "opencode" || got.Model != "big" || got.Mode != "build" {
		t.Fatalf("OpenCode remembered for a tile: %+v", got)
	}
	if err := s.RememberTile("ana", "apps/w", TileStart{Harness: "codex", Mode: "agent-full-access"}, 3); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/w"]; got.Harness != "codex" || got.Mode != "" {
		t.Fatalf("a bypass mode remembered: %+v", got)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"tiles":{"apps/z":{"harness":"codex","mode":"agent"}}}}`)); err != nil {
		t.Fatalf("a mode that acts without asking, for a tile: %v", err)
	}
	if _, err := s.Update("ana", []byte(`{"agentSessions":{"tiles":{"apps/z":{"harness":"claude","mode":"bypassPermissions"}}}}`)); err == nil {
		t.Fatal("a bypass mode saved for a tile")
	}
	// a file that holds a bypass mode (written by hand): ignored at read
	raw := `{"person":100,"agentSessions":{"tiles":{"apps/x":{"harness":"claude","mode":"bypassPermissions"},"apps/y":{"harness":"opencode","mode":"build"}}}}`
	if err := os.WriteFile(s.path("ana"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := s.Get("ana")
	if got := p.AgentSessions.Tiles["apps/x"]; got.Harness != "claude" || got.Mode != "" {
		t.Fatalf("a bypass mode in a file: %+v", got)
	}
	if got := p.AgentSessions.Tiles["apps/y"]; got.Mode != "build" {
		t.Fatalf("OpenCode's build in a file: %+v", got)
	}
}

// A file a released xbind wrote loads, and never blocks a later write
// (D250 re-review, item 1; D251): v0.3.83's (OpenCode's "plan" as the mode
// its person's agent's sessions start in) and v0.3.84's (D250: a remembered
// OpenCode for a tile, asking modes only) — so the Agent tab launcher's PUT
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
	if p.AgentSessions.Modes["opencode"] != "plan" || p.AgentSessions.Modes["claude"] != "plan" || p.Harnesses["claude"].Model != "opus" {
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
	// v0.3.84's shape: a remembered OpenCode for a tile, a preferred coding agent, asking modes
	raw84 := `{"harness":"claude","lastKind":"agent","harnesses":{"claude":{"mode":"default"}},"agentSessions":{"start":"ask","harness":"opencode",` +
		`"tiles":{"apps/eui":{"harness":"opencode","at":1795000000},"apps/x":{"harness":"codex","model":"m1","mode":"read-only","at":1795000001}},"modes":{"claude":"plan"}},"person":100}`
	if err := os.WriteFile(s.path("ana"), []byte(raw84), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := s.Get("ana"); p.AgentSessions.Harness != "opencode" || p.AgentSessions.Tiles["apps/eui"].Harness != "opencode" || p.AgentSessions.Tiles["apps/x"].Mode != "read-only" {
		t.Fatalf("a v0.3.84 file as read now: %+v", p.AgentSessions)
	}
	if _, err := s.Update("ana", []byte(`{"lastKind":"agent","harness":"codex"}`)); err != nil {
		t.Fatalf("the launcher's write over a v0.3.84 file: %v", err)
	}
	if err := s.RememberTile("ana", "apps/eui", TileStart{Harness: "opencode", Mode: "build"}, 2); err != nil {
		t.Fatalf("Remember over a v0.3.84 file: %v", err)
	}
	if got := s.Get("ana").AgentSessions.Tiles["apps/eui"]; got.Mode != "build" {
		t.Fatalf("after Remember: %+v", got)
	}
}
