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
	if o, _ := s.Org("acme"); o.Agents != nil || !s.OrgAgents("acme").IsZero() {
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
	if err != nil || ws.Credentials != "connector" || ws.Harnesses.String() != "claude, codex" {
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
	if ws, err = s.SetWorkspaceAgents(agentPatch(t, `{"workspaceAgent":"off"}`), "", known); err != nil || ws.Credentials != "connector" || !ws.WorkspaceAgent.IsOff() {
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
	if ws := s2.WorkspaceAgents(); ws.Credentials != "connector" || ws.Models.String() != "local" || !ws.WorkspaceAgent.IsOff() {
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
		"apps/wiki":  "eng|eng,finance,ops",
		"apps/mine":  "|ops",
		"tiles/docs": "|",
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
	if d.Allowed || d.Reason != "Finance allows private models only" {
		t.Errorf("the strictest org decides: %+v", d)
	}
	if d := agentpolicy.Check(s.AgentLayers("tiles/docs"), agentpolicy.Ask{WorkspaceAgent: true}); !d.Allowed {
		t.Errorf("a tile in no org with no bounds: %+v", d)
	}
}
