package delegation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

// covers D239 — the web and MCP policy is off until an admin sets it, and
// a file of the release before it (no web, no mcp) reads as off
func TestNetPolicyOffByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"enabled":true,"by":"user:ana","at":"2026-11-15T10:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := OpenStore(dir)
	got := s.NetPolicy()
	if got.Web.Mode != WebOff || got.MCP.Mode != MCPOff || got.Web.Search {
		t.Errorf("an old file's policy: %+v", got)
	}
	if !s.Workspace().Enabled {
		t.Error("the switch of the old file was lost")
	}
	// the person's state of before reads as off too
	if err := os.MkdirAll(userDir(dir, "ana"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(userDir(dir, "ana"), "state.json"), []byte(`{"paused":false,"adminChanges":true}`), 0o600)
	if st := s.State("ana"); st.Web || st.MCP || st.MCPReadOnly || !st.AdminChanges {
		t.Errorf("an old state: %+v", st)
	}
}

func TestNetPolicySetAndKept(t *testing.T) {
	s := OpenStore(t.TempDir())
	if _, err := s.SetWorkspace(true, "user:ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNet(NetPatch{WebMode: strp(WebRead), WebAllow: &[]string{"Docs.Example.com", "*.wiki.test", "docs.example.com"}, MCPMode: strp(MCPTiles)}, "user:ana"); err != nil {
		t.Fatal(err)
	}
	// turning the switch keeps the policy
	if _, err := s.SetWorkspace(false, "user:bob"); err != nil {
		t.Fatal(err)
	}
	got := s.NetPolicy()
	if got.Web.Mode != WebRead || got.MCP.Mode != MCPTiles || strings.Join(got.Web.Allow, ",") != "*.wiki.test,docs.example.com" {
		t.Errorf("policy after the switch turned: %+v", got)
	}
	for _, p := range []NetPatch{{WebMode: strp(WebFull)}, {MCPMode: strp(MCPListed)}, {WebSearch: boolp(true)}} {
		if _, err := s.SetNet(p, "user:ana"); !errors.Is(err, ErrNotYet) {
			t.Errorf("a reserved mode: %v", err)
		}
	}
	for _, p := range []NetPatch{{WebMode: strp("write")}, {MCPMode: strp("all")}, {WebDeny: &[]string{"10.0.0.1"}}, {WebAllow: &[]string{"http://x.com"}}} {
		if _, err := s.SetNet(p, "user:ana"); err == nil || errors.Is(err, ErrNotYet) {
			t.Errorf("a bad patch %+v: %v", p, err)
		}
	}
	if ok, _ := got.Web.HostAllows("docs.example.com"); !ok {
		t.Error("the allow list refuses its own host")
	}
	if ok, why := got.Web.HostAllows("evil.test"); ok || !strings.Contains(why, "allow list") {
		t.Errorf("a host off the allow list: %v %q", ok, why)
	}
}

// covers D239 — the host's web and MCP policy (the workspaceAgent key of
// the host's file, internal/llm) presets and caps: a mode is a ceiling the
// admin may narrow and never pass, the lists always hold and lock nothing,
// and the policy follows the file as it reads now
func TestNetPolicyHost(t *testing.T) {
	host := HostNet{Web: &WebPolicy{Mode: WebRead, Deny: []string{"intranet.example.com"}, Search: boolp(false)}, MCP: &MCPPolicy{Mode: MCPOff}}
	s := OpenStore(t.TempDir())
	s.SetHost(func() HostNet { return host })
	got := s.NetPolicy()
	if got.Web.Mode != WebRead || !got.Web.ByHost || got.MCP.Mode != MCPOff {
		t.Errorf("the host's preset: %+v", got)
	}
	if ok, why := got.Web.HostAllows("a.intranet.example.com"); ok || !strings.Contains(why, "host's deny list") {
		t.Errorf("the host's deny list: %v %q", ok, why)
	}
	// what the host holds, by name: its modes are ceilings, its lists lock nothing
	if l := s.Host().Locks(); l["web.mode"].Ceiling != WebRead || l["mcp.mode"].Ceiling != MCPOff || l["web.search"].Ceiling != "false" || len(l) != 3 {
		t.Errorf("the locks: %+v", l)
	}
	// MCP past the host's off is refused by name (409), a reserved value as not yet (400), a list is the admin's own
	var held *ErrHostLocked
	if _, err := s.SetNet(NetPatch{MCPMode: strp(MCPTiles), WebMode: strp(WebOff)}, "user:ana"); !errors.As(err, &held) || strings.Join(held.Settings, ",") != "mcp.mode" ||
		!strings.Contains(err.Error(), "set by your host") {
		t.Errorf("a mode past the host's: %v", err)
	}
	if _, err := s.SetNet(NetPatch{WebSearch: boolp(true)}, "user:ana"); !errors.Is(err, ErrNotYet) {
		t.Errorf("search under the host's off: %v", err)
	}
	if _, err := s.SetNet(NetPatch{WebDeny: &[]string{"bank.test"}}, "user:ana"); err != nil {
		t.Errorf("the admin's own list beside the host's: %v", err)
	}
	// the admin narrows the web
	if _, err := s.SetNet(NetPatch{WebMode: strp(WebOff), WebSearch: boolp(false)}, "user:ana"); err != nil {
		t.Fatal(err)
	}
	got = s.NetPolicy()
	if got.Web.Mode != WebOff || got.MCP.Mode != MCPOff || !got.MCP.ByHost || got.Web.Search {
		t.Errorf("under the host's ceiling: %+v", got)
	}
	// the file changes: the policy follows it (the admin's off still in force — it narrows)
	host = HostNet{Web: &WebPolicy{Mode: WebRead}, MCP: &MCPPolicy{Mode: MCPTiles}}
	got = s.NetPolicy()
	if got.Web.Mode != WebOff || got.MCP.Mode != MCPTiles {
		t.Errorf("after the file changed: %+v", got)
	}
	if _, err := s.SetNet(NetPatch{WebMode: strp(WebRead), MCPMode: strp(MCPTiles)}, "user:ana"); err != nil {
		t.Errorf("up to the new ceiling: %v", err)
	}
	// the key goes: the admin's own settings, nothing held
	host = HostNet{}
	if l := s.Host().Locks(); len(l) != 0 {
		t.Errorf("no key, no locks: %+v", l)
	}
}

func TestMarkWeb(t *testing.T) {
	s := OpenStore(t.TempDir())
	h, err := s.NewAgentSession("ana", "xbin/wsagent", nil, "up/m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkWeb("ana", "xbin/wsagent", h.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgentSession("ana", "xbin/wsagent", h.ID); !got.Web {
		t.Error("not marked")
	}
	// an update can't clear it
	got, err := s.UpdateAgentSession("ana", "xbin/wsagent", h.ID, func(a *AgentSession) error { a.Web = false; return nil })
	if err != nil || !got.Web {
		t.Errorf("an update cleared web: %+v %v", got, err)
	}
	if err := s.MarkWeb("ana", "xbin/other", h.ID); !errors.Is(err, ErrNoAgentSession) {
		t.Errorf("another holder's handle: %v", err)
	}
}

// covers D239 — a call's approval never covers a tool, a tool's never a call
func TestApprovalScopesKeepToTheirKind(t *testing.T) {
	call := Approval{Tile: "apps/dice", Scope: ScopeCall, Method: MCPMethod, Path: "wipe"}
	tool := Approval{Tile: "apps/dice", Scope: ScopeTool, Method: MCPMethod, Path: "wipe"}
	if call.covers("apps/dice", MCPMethod, "wipe") {
		t.Error("a call's approval covers a tool")
	}
	if !tool.covers("apps/dice", MCPMethod, "wipe") || tool.covers("apps/dice", "POST", "wipe") {
		t.Error("a tool's approval: its tool only")
	}
	http := Approval{Tile: "apps/dice", Scope: ScopeCall, Method: "POST", Path: "/api/apps/dice/x"}
	if !http.covers("apps/dice", "POST", "/api/apps/dice/x") {
		t.Error("a call's approval no longer covers its call")
	}
}
