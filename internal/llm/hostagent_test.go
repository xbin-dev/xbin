package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The host's workspaceAgent key: a bool or an object, read strictly.
func TestHostAgentKey(t *testing.T) {
	ok := map[string]HostAgent{
		`null`:                                 {},
		`false`:                                {Set: true},
		`true`:                                 {Set: true, Enabled: true},
		`{"enabled": false}`:                   {Set: true},
		`{"enabled": true}`:                    {Set: true, Enabled: true},
		`{"enabled": true, "who": "admins"}`:   {Set: true, Enabled: true, Who: "admins"},
		`{"enabled": true, "who": "everyone"}`: {Set: true, Enabled: true, Who: "everyone"},
		` {"who": "admins", "enabled": true} `: {Set: true, Enabled: true, Who: "admins"},
	}
	for in, want := range ok {
		var h HostAgent
		if err := json.Unmarshal([]byte(in), &h); err != nil || h != want {
			t.Errorf("%s: %+v %v, want %+v", in, h, err, want)
		}
	}
	for _, in := range []string{`"on"`, `1`, `{}`, `{"who": "admins"}`, `{"enabled": true, "who": "me"}`, `{"enabled": true, "who": "chosen"}`,
		`{"enabled": false, "who": "everyone"}`, `{"enabled": true, "whom": "admins"}`, `{"enabled": "yes"}`, `[]`} {
		var h HostAgent
		if err := json.Unmarshal([]byte(in), &h); err == nil {
			t.Errorf("%s: want an error, got %+v", in, h)
		}
	}
}

// In the host's file: read with the rest, an invalid one stops the load,
// and a change is seen as the connector sees one.
func TestHostAgentInHostFile(t *testing.T) {
	dir := t.TempDir()
	p := writeHost(t, dir, `{
	  // the connector's settings, and beside them
	  "workspaceAgent": {"enabled": true, "who": "admins"}
	}`, "")
	h, err := loadHost(p)
	if err != nil || h.WorkspaceAgent != (HostAgent{Set: true, Enabled: true, Who: "admins"}) {
		t.Fatalf("loadHost: %+v %v", h, err)
	}
	if _, err := loadHost(writeHost(t, dir, `{"workspaceAgent": {"enabled": true, "who": "people"}}`, "")); err == nil || !strings.Contains(err.Error(), "workspaceAgent") {
		t.Fatalf("a bad key stops the file: %v", err)
	}
	if h, err := loadHost(writeHost(t, dir, `{}`, "")); err != nil || h.WorkspaceAgent.Set {
		t.Fatalf("no key: %+v %v", h, err)
	}

	now := time.Now()
	writeHost(t, dir, `{"workspaceAgent": false}`, "")
	changed := make(chan struct{}, 4)
	s, err := New(Options{Dir: t.TempDir(), HostFile: p, Now: func() time.Time { return now }, HostChanged: func() { changed <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true}) {
		t.Fatalf("in force: %+v", got)
	}
	writeHost(t, dir, `{"workspaceAgent": {"enabled": true, "who": "everyone"}}`, "")
	future := now.Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	now = now.Add(hostCheckGap + time.Second)
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true, Enabled: true, Who: "everyone"}) {
		t.Fatalf("reloaded: %+v", got)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("HostChanged was not called after the reload")
	}
	// a changed file that is invalid is refused: the last good one stays
	writeHost(t, dir, `{"workspaceAgent": "yes"}`, "")
	later := future.Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	now = now.Add(hostCheckGap + time.Second)
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true, Enabled: true, Who: "everyone"}) {
		t.Fatalf("a refused reload keeps the last good one: %+v", got)
	}
}

// covers D239 — the key's web and mcp: the web and MCP policy for people's
// workspace agents, validated as an admin's would be; alone they leave the
// switch to the admins
func TestHostAgentWebAndMCP(t *testing.T) {
	var h HostAgent
	if err := json.Unmarshal([]byte(`{"web": {"mode": "read", "deny": ["Intranet.Example.com"]}, "mcp": {"mode": "tiles"}}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.Set || h.Web == nil || h.Web.Mode != "read" || strings.Join(h.Web.Deny, ",") != "intranet.example.com" || h.MCP == nil || h.MCP.Mode != "tiles" {
		t.Errorf("web and mcp alone: %+v %+v %+v", h, h.Web, h.MCP)
	}
	h = HostAgent{}
	if err := json.Unmarshal([]byte(`{"enabled": true, "who": "admins", "web": {"mode": "off"}}`), &h); err != nil || !h.Set || !h.Enabled || h.Who != "admins" || h.Web == nil || h.Web.Mode != "off" {
		t.Errorf("with the switch: %+v %v", h, err)
	}
	for _, in := range []string{`{"web": {"mode": "full"}}`, `{"mcp": {"mode": "all"}}`, `{"web": {"search": true}}`, `{"web": {"mode": "read", "alow": []}}`,
		`{"web": {"deny": ["10.0.0.1"]}}`, `{"web": {"mode": "write"}}`, `{"who": "admins", "web": {"mode": "read"}}`, `{"web": "read"}`} {
		var h HostAgent
		if err := json.Unmarshal([]byte(in), &h); err == nil {
			t.Errorf("%s: want an error, got %+v", in, h)
		}
	}
}

// The host's agentSignIns key (D230): a bool that sets and locks whether
// people may save coding-agent sign-ins; absent, the admins' switch.
func TestHostAgentSignIns(t *testing.T) {
	dir := t.TempDir()
	p := writeHost(t, dir, `{"agentSignIns": false}`, "")
	now := time.Now()
	s, err := New(Options{Dir: t.TempDir(), HostFile: p, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if set, on := s.HostAgentSignIns(); !set || on {
		t.Fatalf("false: set %v on %v", set, on)
	}
	for body, want := range map[string][2]bool{`{"agentSignIns": true}`: {true, true}, `{}`: {false, false}, `{"agentSignIns": null}`: {false, false}} {
		writeHost(t, dir, body, "")
		now = now.Add(time.Hour)
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
		if set, on := s.HostAgentSignIns(); set != want[0] || on != want[1] {
			t.Errorf("%s: set %v on %v", body, set, on)
		}
	}
	if _, err := loadHost(writeHost(t, dir, `{"agentSignIns": "yes"}`, "")); err == nil {
		t.Fatal("a value that isn't a bool loaded")
	}
	if _, err := loadHost(writeHost(t, dir, `{"agentSignin": true}`, "")); err == nil {
		t.Fatal("a misspelled key loaded")
	}
}

// covers D240 — the host's file may list the outside MCP servers, each
// secret named by reference (a file beside it, or an environment variable)
// and read with the file: one that can't be read stops the load, a secret
// written into the file itself is refused, and a rotated secret file is a
// changed file
func TestHostAgentMCPServers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tracker.token"), []byte("tok-FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XBIN_TEST_MCP_SECRET", "tok-ENV")
	p := writeHost(t, dir, `{"workspaceAgent": {"mcp": {"mode": "listed", "servers": [
	  {"id": "tracker", "name": "Tracker", "url": "https://mcp.tracker.example/mcp", "auth": "bearer", "secretFile": "tracker.token"},
	  {"id": "wiki", "name": "Wiki", "url": "https://wiki.example/mcp", "auth": "header", "header": "X-Api-Key", "secretEnv": "XBIN_TEST_MCP_SECRET", "orgs": ["eng"]},
	  {"id": "open", "name": "Open data", "url": "https://open.example/mcp"}]}}}`, "")
	h, err := loadHost(p)
	if err != nil {
		t.Fatal(err)
	}
	m := h.WorkspaceAgent.MCP
	if m == nil || m.Mode != "listed" || m.Servers == nil || len(*m.Servers) != 3 {
		t.Fatalf("mcp: %+v", m)
	}
	list := *m.Servers
	if list[0].HostSecret.Reveal() != "tok-FILE" || list[1].HostSecret.Reveal() != "tok-ENV" || list[2].HostSecret.Set() || !list[0].HasSecret || list[2].HasSecret {
		t.Errorf("the secrets: %+v", list)
	}
	if h.changed() {
		t.Fatal("changed at once")
	}
	if err := os.WriteFile(filepath.Join(dir, "tracker.token"), []byte("tok-ROTATED-LONGER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !h.changed() {
		t.Error("a rotated secret file isn't a change of the file")
	}
	for what, cfg := range map[string]string{
		"a missing secret file": `{"workspaceAgent": {"mcp": {"servers": [{"id": "t", "name": "T", "url": "https://t.example/mcp", "auth": "bearer", "secretFile": "nope.token"}]}}}`,
		"an empty variable":     `{"workspaceAgent": {"mcp": {"servers": [{"id": "t", "name": "T", "url": "https://t.example/mcp", "auth": "bearer", "secretEnv": "XBIN_TEST_MCP_UNSET"}]}}}`,
		"a secret in the file":  `{"workspaceAgent": {"mcp": {"servers": [{"id": "t", "name": "T", "url": "https://t.example/mcp", "auth": "bearer", "secret": "inline"}]}}}`,
		"http, unmarked":        `{"workspaceAgent": {"mcp": {"servers": [{"id": "t", "name": "T", "url": "http://t.example/mcp"}]}}}`,
		"no reference":          `{"workspaceAgent": {"mcp": {"servers": [{"id": "t", "name": "T", "url": "https://t.example/mcp", "auth": "bearer"}]}}}`,
	} {
		if _, err := loadHost(writeHost(t, dir, cfg, "")); err == nil {
			t.Errorf("%s: loaded", what)
		} else if strings.Contains(err.Error(), "inline") {
			t.Errorf("%s: the error repeats the secret: %v", what, err)
		}
	}
}
