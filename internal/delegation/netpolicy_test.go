package delegation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

func writeHost(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "wsagent.jsonc")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

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

// covers D239 — the host's file presets and caps: a mode is a ceiling the
// admin may narrow and never pass, the lists always hold, lockAll freezes
// every setting
func TestNetPolicyHostFile(t *testing.T) {
	dir := t.TempDir()
	path := writeHost(t, dir, `// the host
	{"web": {"mode": "read", "deny": ["intranet.example.com"], "search": false}, "mcp": {"mode": "off"}}`)
	h, err := LoadHostFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := OpenStore(t.TempDir())
	s.SetHost(h)
	got := s.NetPolicy()
	if got.Web.Mode != WebRead || !got.Web.ByHost || got.MCP.Mode != MCPOff {
		t.Errorf("the host's preset: %+v", got)
	}
	if ok, why := got.Web.HostAllows("a.intranet.example.com"); ok || !strings.Contains(why, "host's deny list") {
		t.Errorf("the host's deny list: %v %q", ok, why)
	}
	// what the host holds, by name: its modes are ceilings, its lists lock nothing
	if l := h.cur.Locks(); l["web.mode"].Ceiling != WebRead || l["mcp.mode"].Ceiling != MCPOff || l["web.search"].Ceiling != "false" || len(l) != 3 {
		t.Errorf("the locks: %+v", l)
	}
	// MCP past the host's off is refused by name (409), a reserved value as not yet (400), a list is the admin's own
	var held *ErrHostLocked
	if _, err := s.SetNet(NetPatch{MCPMode: strp(MCPTiles), WebMode: strp(WebOff)}, "user:ana"); !errors.As(err, &held) || strings.Join(held.Settings, ",") != "mcp.mode" {
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
	// a change of the file is reloaded; an invalid one is refused and the last good stays
	clock := time.Now()
	h.SetClock(func() time.Time { return clock })
	writeHost(t, dir, `{"web": {"mode": "read"}, "mcp": {"mode": "tiles"}, "lockAll": true}`)
	clock = clock.Add(3 * time.Second)
	got = s.NetPolicy()
	if got.Web.Mode != WebRead || got.MCP.Mode != MCPTiles {
		t.Errorf("after the reload (lockAll, the admin's off no longer in force): %+v", got)
	}
	var lk *ErrHostLocked
	if _, err := s.SetNet(NetPatch{WebMode: strp(WebOff), WebDeny: &[]string{"x.test"}}, "user:ana"); !errors.As(err, &lk) || strings.Join(lk.Settings, ",") != "web.mode,web.deny" {
		t.Errorf("a write under lockAll: %v", err)
	}
	if l := h.cur.Locks(); len(l) != len(NetSettings) || l["web.allow"].Ceiling != "" || !l["web.allow"].Locked {
		t.Errorf("lockAll's locks name every setting: %+v", l)
	}
	writeHost(t, dir, `{"web": {"mode": "full"}}`)
	clock = clock.Add(3 * time.Second)
	if _, why := s.Host(); why == "" {
		t.Error("an invalid change wasn't said")
	}
	if got := s.NetPolicy(); got.Web.Mode != WebRead {
		t.Errorf("an invalid change took effect: %+v", got)
	}
	// at boot an invalid file is an error
	for _, bad := range []string{`{"web": {"mode": "full"}}`, `{"web": {"mode": "read", "alow": []}}`, `{"mcp": {"mode": "listed"}}`, `not json`} {
		if _, err := LoadHostFile(writeHost(t, t.TempDir(), bad)); err == nil {
			t.Errorf("LoadHostFile(%s) took it", bad)
		}
	}
	if _, err := LoadHostFile(filepath.Join(dir, "missing.jsonc")); err == nil {
		t.Error("a missing file loaded")
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
