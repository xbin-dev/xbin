package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The catalog's JSON is what xbind's GET /agent/providers serves (its own
// golden: internal/server/agentproviders_golden_test.go); LoginCmd, Bins
// and AutoMode are the runner's and never appear in it.
func TestProvidersJSON(t *testing.T) {
	b, _ := json.Marshal(Providers())
	want := `[{"id":"claude","name":"Claude Code","login":"claude /login","modes":[{"id":"default","name":"Ask before acting"},{"id":"acceptEdits","name":"Accept edits"},{"id":"plan","name":"Plan"},{"id":"auto","name":"Auto"},{"id":"bypassPermissions","name":"Bypass permissions","explicit":true}],"defaultMode":"default"},{"id":"codex","name":"Codex","login":"codex login","modes":[{"id":"read-only","name":"Ask for approval"},{"id":"agent","name":"Approve for me"},{"id":"agent-full-access","name":"Full access","explicit":true}],"defaultMode":"read-only"},{"id":"gemini","name":"Gemini CLI","login":"gemini (then choose Login with Google)","modes":[{"id":"default","name":"Ask before acting"},{"id":"autoEdit","name":"Auto edit"},{"id":"plan","name":"Plan"},{"id":"yolo","name":"Auto-approve everything","explicit":true}],"defaultMode":""},{"id":"opencode","name":"OpenCode","login":"opencode auth login","modes":null,"defaultMode":""}]`
	if string(b) != want {
		t.Fatalf("providers JSON\n got %s\nwant %s", b, want)
	}
}

func TestProvidersCatalog(t *testing.T) {
	ps := Providers()
	ps[0].ID = "changed"
	if p, ok := Lookup("claude"); !ok || p.Name != "Claude Code" {
		t.Fatal("Providers hands out the catalog itself")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("an unknown id was found")
	}
	for _, p := range Providers() {
		if p.Driver != "acp" || len(p.Argv) == 0 || len(p.Bins) == 0 || p.Bins[0] != p.Argv[0] || p.LoginCmd == "" {
			t.Errorf("%s: argv %v bins %v login %q", p.ID, p.Argv, p.Bins, p.LoginCmd)
		}
		if p.AutoMode == "" {
			continue
		}
		ok := false
		for _, m := range p.Modes {
			if m.ID == p.AutoMode && !m.Explicit {
				ok = true
			}
			if m.Explicit && m.ID == p.DefaultMode {
				t.Errorf("%s: an explicit mode must never be the default", p.ID)
			}
		}
		if !ok {
			t.Errorf("%s: AutoMode %q is not one of its non-explicit modes", p.ID, p.AutoMode)
		}
	}
	c, _ := Lookup("claude")
	if m, err := c.ResolveMode(""); err != nil || m != "default" {
		t.Fatalf("default mode: %q %v", m, err)
	}
	if m, err := c.ResolveMode("bypassPermissions"); err != nil || m != "bypassPermissions" {
		t.Fatalf("explicit mode by name: %q %v", m, err)
	}
	if _, err := c.ResolveMode("yolo"); err == nil || !strings.Contains(err.Error(), "one of default, acceptEdits") {
		t.Fatalf("unknown mode: %v", err)
	}
	o, _ := Lookup("opencode")
	if m, err := o.ResolveMode("whatever"); err != nil || m != "whatever" {
		t.Fatal("a provider without a mode table lets the agent judge")
	}
}

// AutoMode, ApproveMode and PlanMode are each one of the provider's own
// non-explicit modes (or "": it has none), and Fake is the test agent with
// all three.
func TestProviderSettingModes(t *testing.T) {
	want := map[string][3]string{"claude": {"acceptEdits", "default", "plan"}, "codex": {"agent", "read-only", "read-only"},
		"gemini": {"autoEdit", "default", "plan"}, "opencode": {"", "", ""}}
	for _, p := range append(Providers(), Fake([]string{"/bin/fakeacp", "--steer"})) {
		got := [3]string{p.AutoMode, p.ApproveMode, p.PlanMode}
		if w, ok := want[p.ID]; ok && got != w {
			t.Errorf("%s: auto/approve/plan %v, want %v", p.ID, got, w)
		}
		for _, m := range got {
			if m == "" {
				continue
			}
			ok := false
			for _, pm := range p.Modes {
				ok = ok || (pm.ID == m && !pm.Explicit)
			}
			if !ok {
				t.Errorf("%s: %q is not one of its non-explicit modes", p.ID, m)
			}
		}
	}
	f := Fake([]string{"/bin/fakeacp", "--steer"})
	if f.ID != "fake" || f.DefaultMode != "ask" || f.AutoMode != "auto" || f.LoginCmd != "/bin/fakeacp login" ||
		len(f.Bins) != 1 || f.Bins[0] != "/bin/fakeacp" || len(f.Argv) != 2 {
		t.Fatalf("fake: %+v", f)
	}
	if _, ok := Lookup("fake"); ok {
		t.Fatal("the fake is never in the catalog")
	}
	if b, _ := json.Marshal(f); strings.Contains(string(b), "approve") || strings.Contains(string(b), "plan\"") {
		t.Fatalf("the setting modes are the runner's, never in the JSON: %s", b)
	}
}
