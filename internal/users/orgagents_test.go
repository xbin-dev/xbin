package users

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
)

func agentPatch(t *testing.T, js string) agentpolicy.Patch {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &body); err != nil {
		t.Fatal(err)
	}
	p, err := agentpolicy.ParsePatch(body)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// covers D229 — an org's agent constraints (Org.Agents) and the workspace's
// bounds round-trip through users.json, survive an org update, change rule
// by rule, can't reach past the bounds, and a users.json written before
// them loads with none (and writes none back).
func TestOrgAgentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old := `{"users":[{"id":"carol","role":"user"}],
		"orgs":[{"id":"acme","name":"Acme","members":[{"id":"carol","level":"read","admin":true}],"created":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if own, pin := s.OrgAgents("acme"); !own.IsZero() || !pin.IsZero() {
		t.Fatalf("an old users.json: %+v %+v", own, pin)
	}
	if o, _ := s.Org("acme"); o.Agents != nil || o.AgentsPin != nil {
		t.Fatalf("an old users.json: %+v", o)
	}
	if ws := s.WorkspaceAgents(); !ws.Rules.IsZero() || ws.Credentials != "own-first" {
		t.Fatalf("the defaults: %+v", ws)
	}
	// a write that changes nothing leaves no trace of the feature
	if _, err := s.SetOrgAgents("acme", agentPatch(t, `{"models":"any","workspaceAgent":"on"}`), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); strings.Contains(string(b), "agents") {
		t.Errorf("users.json gained agents with nothing set: %s", b)
	}

	known := func(id string) bool { return id == "claude" || id == "codex" || id == "gemini" }
	got, err := s.SetOrgAgents("acme", agentPatch(t, `{"harnesses":["codex","claude"],"models":"private"}`), known)
	if err != nil || got.Harnesses.String() != "claude, codex" || !got.Models.IsPrivate() {
		t.Fatalf("SetOrgAgents: %+v %v", got, err)
	}
	if _, err := s.UpsertOrg(Org{ID: "acme", Name: "Acme Inc", Members: []Member{{ID: "carol", Level: LevelRead, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.SetOrgAgents("acme", agentPatch(t, `{"workspaceAgent":"off"}`), known); err != nil || !got.WorkspaceAgent.IsOff() || !got.Models.IsPrivate() {
		t.Fatalf("a rule not named keeps its value: %+v %v", got, err)
	}
	for js, want := range map[string]string{
		`{"harnesses":["nope"]}`:      "no harness",
		`{"harnesses":"private"}`:     "rule for models",
		`{"models":["bad id"]}`:       "isn't an upstream",
		`{"models":["a","b","a"]}`:    "",
		`{"harnesses":["gemini"]}`:    "",
		`{"harnesses":["claude"]}`:    "",
		`{"models":"private"}`:        "",
		`{"workspaceAgent":"off"}`:    "",
		`{"harnesses":["claude",""]}`: "",
	} {
		_, err := s.SetOrgAgents("acme", agentPatch(t, js), known)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", js, err, want)
		}
	}
	if _, err := s.SetOrgAgents("nope", agentPatch(t, `{"models":"private"}`), nil); err == nil || !strings.Contains(err.Error(), "no such org") {
		t.Errorf("an unknown org: %v", err)
	}

	// the workspace's bounds: an org can't reach past them
	ws, err := s.SetWorkspaceAgents(agentPatch(t, `{"harnesses":["claude","codex"],"models":["local"]}`), "connector", known)
	if err != nil || ws.Credentials != "connector-only" || ws.Harnesses.String() != "claude, codex" {
		t.Fatalf("SetWorkspaceAgents: %+v %v", ws, err)
	}
	if _, err := s.SetOrgAgents("acme", agentPatch(t, `{"harnesses":["gemini"]}`), known); err == nil || !strings.Contains(err.Error(), "can't widen") {
		t.Errorf("past the bounds: %v", err)
	}
	if _, err := s.SetOrgAgents("acme", agentPatch(t, `{"harnesses":["codex"],"models":["local/llama"]}`), known); err != nil {
		t.Errorf("within the bounds: %v", err)
	}
	if _, err := s.SetOrgAgents("acme", agentPatch(t, `{"models":["hosted"]}`), known); err == nil {
		t.Error("a model outside the bounds: accepted")
	}
	if _, err := s.SetWorkspaceAgents(agentpolicy.Patch{}, "sometimes", known); err == nil {
		t.Error("a credential default that doesn't exist: accepted")
	}
	if ws, err = s.SetWorkspaceAgents(agentPatch(t, `{"workspaceAgent":"off"}`), "", known); err != nil || ws.Credentials != "connector-only" || !ws.WorkspaceAgent.IsOff() {
		t.Errorf("credentials \"\" keeps the default set: %+v %v", ws, err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := s2.Org("acme")
	if o.Name != "Acme Inc" || o.Agents == nil || o.Agents.Harnesses.String() != "codex" || o.Agents.Models.String() != "local/llama" || !o.Agents.WorkspaceAgent.IsOff() {
		t.Errorf("after a reload: %+v", o.Agents)
	}
	if ws := s2.WorkspaceAgents(); ws.Credentials != "connector-only" || ws.Models.String() != "local" || !ws.WorkspaceAgent.IsOff() {
		t.Errorf("the bounds after a reload: %+v", ws)
	}
	// back to nothing: the keys go
	if _, err := s2.SetOrgAgents("acme", agentPatch(t, `{"workspaceAgent":null,"harnesses":null,"models":null}`), known); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.SetWorkspaceAgents(agentPatch(t, `{"workspaceAgent":"on","harnesses":"any","models":"any"}`), "own-first", known); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); strings.Contains(string(b), "agents") {
		t.Errorf("users.json keeps agents after a reset: %s", b)
	}
}

// covers D229 — the orgs a tile falls under: its owner and every org it is
// shared into (exact or by pattern); the layers ask the workspace first.
func TestTileOrgsAndLayers(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(User{ID: "carol"}, "password1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"eng", "finance", "ops"} {
		if _, err := s.UpsertOrg(Org{ID: id, Name: strings.ToUpper(id[:1]) + id[1:]}); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetOwner("apps/wiki", "org:eng"))
	must(s.SetOwner("apps/mine", "user:carol"))
	must(s.SetOrgTile("finance", "apps/wiki", LevelRead))
	must(s.SetOrgTile("ops", "apps/*", LevelWrite))
	must(s.SetOrgTile("eng", "apps/wiki", LevelRead)) // the owner sharing to itself counts once
	for path, want := range map[string]string{
		"apps/wiki":         "eng|eng,finance,ops",
		"apps/wiki/src/sub": "eng|eng,finance,ops", // inside the tile: its orgs still
		"/apps/wiki/":       "eng|eng,finance,ops",
		"apps/mine":         "|ops",
		"tiles/docs":        "|",
		"apps":              "|ops", // the pattern apps/* covers its own prefix
	} {
		owner, orgs := s.TileOrgs(path)
		if got := owner + "|" + strings.Join(orgs, ","); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	if _, err := s.SetOrgAgents("finance", agentPatch(t, `{"models":"private"}`), nil); err != nil {
		t.Fatal(err)
	}
	layers := s.AgentLayers("apps/wiki")
	if len(layers) != 4 || layers[0].ID != "" || layers[2].ID != "finance" || layers[2].Name != "Finance" || !layers[2].Rules.Models.IsPrivate() {
		t.Fatalf("layers: %+v", layers)
	}
	d := agentpolicy.Check(layers, agentpolicy.Ask{WorkspaceAgent: true, Model: &agentpolicy.ModelRef{Upstream: "hosted"}})
	if d.Allowed || d.Reason != "Finance allows private models only (this tile is under Finance by its share apps/wiki)" {
		t.Errorf("the strictest org decides: %+v", d)
	}
	if d := agentpolicy.Check(s.AgentLayers("tiles/docs"), agentpolicy.Ask{WorkspaceAgent: true}); !d.Allowed {
		t.Errorf("a tile in no org with no bounds: %+v", d)
	}
}

// covers D229 round 2 — a workspace admin pins rules on an org: the org's
// own rules narrow below the pin and never past it, the pin survives an
// org update and a reload, every call is asked against both, and the
// three credential modes store.
func TestOrgAgentsPin(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertOrg(Org{ID: "finance", Name: "Finance"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/ledger", "org:finance"); err != nil {
		t.Fatal(err)
	}
	known := func(id string) bool { return id == "claude" || id == "codex" || id == "gemini" }
	if _, err := s.SetOrgAgents("finance", agentPatch(t, `{"harnesses":["claude","gemini"]}`), known); err != nil {
		t.Fatal(err)
	}
	pin, err := s.SetOrgAgentsPin("finance", agentPatch(t, `{"models":"private","harnesses":["claude","codex"]}`), known)
	if err != nil || !pin.Models.IsPrivate() {
		t.Fatalf("pin: %+v %v", pin, err)
	}
	// the org's own list was set before the pin and now reaches past it: it
	// is kept, and the pin holds at every call
	layers := s.AgentLayers("apps/ledger")
	if len(layers) != 3 || !layers[1].Pinned || layers[2].Pinned {
		t.Fatalf("layers: %+v", layers)
	}
	if d := agentpolicy.Check(layers, agentpolicy.Ask{Harness: "gemini"}); d.Allowed || !d.Pinned {
		t.Errorf("gemini, on the org's own list and past the pin: %+v", d)
	}
	if d := agentpolicy.Check(layers, agentpolicy.Ask{Harness: "claude"}); !d.Allowed {
		t.Errorf("claude, on both: %+v", d)
	}
	if d := agentpolicy.Check(layers, agentpolicy.Ask{WorkspaceAgent: true, Model: &agentpolicy.ModelRef{Upstream: "hosted"}}); d.Allowed ||
		d.Reason != "Finance allows private models only (pinned by a workspace admin)" {
		t.Errorf("the pinned model rule: %+v", d)
	}
	// an org admin's write: below the pin yes, past it no, "any" changes nothing the pin holds
	// (in this order: each write is judged with the org's rules as the one
	// before left them — as a map these ran in any order, and "off" written
	// while the org's own list still named gemini was refused for that)
	for _, c := range [][2]string{
		{`{"harnesses":["claude"]}`, ""},
		{`{"harnesses":["gemini"]}`, "gemini is outside what the pin allows (claude, codex)"},
		{`{"harnesses":"any","models":"any"}`, ""},
		{`{"workspaceAgent":"off"}`, ""},
	} {
		js, want := c[0], c[1]
		_, err := s.SetOrgAgents("finance", agentPatch(t, js), known)
		if (want == "") != (err == nil) || (err != nil && (!strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "pinned"))) {
			t.Errorf("%s: %v, want %q", js, err, want)
		}
	}
	if d := agentpolicy.Check(s.AgentLayers("apps/ledger"), agentpolicy.Ask{Harness: "gemini"}); d.Allowed {
		t.Error("the org's own any loosened the pin")
	}
	// the pin sits within the workspace's bounds too
	if _, err := s.SetWorkspaceAgents(agentPatch(t, `{"harnesses":["claude","codex"]}`), "connector-first", known); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetOrgAgentsPin("finance", agentPatch(t, `{"harnesses":["gemini"]}`), known); err == nil {
		t.Error("a pin past the workspace's bounds")
	}
	if _, err := s.UpsertOrg(Org{ID: "finance", Name: "Finance Ltd"}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	own, pin2 := s2.OrgAgents("finance")
	if !pin2.Models.IsPrivate() || pin2.Harnesses.String() != "claude, codex" || !own.WorkspaceAgent.IsOff() {
		t.Errorf("after an update and a reload: own %+v pin %+v", own, pin2)
	}
	if ws := s2.WorkspaceAgents(); ws.Credentials != "connector-first" {
		t.Errorf("the mode: %+v", ws)
	}
	for in, want := range map[string]string{"connector-only": "connector-only", "connector": "connector-only", "own-first": "own-first"} {
		if ws, err := s2.SetWorkspaceAgents(agentpolicy.Patch{}, in, known); err != nil || ws.Credentials != want {
			t.Errorf("mode %s: %+v %v", in, ws, err)
		}
	}
	if _, err := s2.SetOrgAgentsPin("finance", agentPatch(t, `{"models":null,"harnesses":null}`), known); err != nil {
		t.Fatal(err)
	}
	if o, _ := s2.Org("finance"); o.AgentsPin != nil {
		t.Errorf("a pin put back to nothing is gone: %+v", o.AgentsPin)
	}
}

// covers D229 round 2 — the share that puts a tile under an org is named.
func TestTileOrgVia(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"eng", "finance", "ops"} {
		if _, err := s.UpsertOrg(Org{ID: id, Name: strings.ToUpper(id[:1]) + id[1:]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range []error{
		s.SetOwner("apps/wiki", "org:eng"),
		s.SetOrgTile("finance", "apps/wiki", LevelRead),
		s.SetOrgTile("finance", "apps/*", LevelRead),
		s.SetOrgTile("ops", "*", LevelRead),
		s.SetOrgTile("eng", "apps/*", LevelRead),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, orgs, via := s.TileOrgVia("apps/wiki/src")
	if owner != "eng" || strings.Join(orgs, ",") != "eng,finance,ops" || len(via) != 2 || via["finance"] != "apps/*" || via["ops"] != "*" {
		t.Errorf("apps/wiki/src: %s %v %v", owner, orgs, via)
	}
	if _, _, via = s.TileOrgVia("apps/wiki"); via["finance"] != "apps/*" { // of two shares that cover it, the same one every time
		t.Errorf("apps/wiki: %v", via)
	}
	if _, err := s.SetOrgAgents("ops", agentPatch(t, `{"workspaceAgent":"off"}`), nil); err != nil {
		t.Fatal(err)
	}
	d := agentpolicy.Check(s.AgentLayers("tiles/docs"), agentpolicy.Ask{WorkspaceAgent: true})
	if d.Allowed || d.Reason != "Ops has the workspace agent turned off (this tile is under Ops by its share *)" || d.Via != "*" {
		t.Errorf("a tile under an org by a * share: %+v", d)
	}
}
