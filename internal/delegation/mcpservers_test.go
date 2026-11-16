package delegation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func srv(id, url string) MCPServerPatch {
	return MCPServerPatch{MCPServer: MCPServer{ID: id, Name: "Server " + id, URL: url}}
}

func withSecret(p MCPServerPatch, auth, secret string) MCPServerPatch {
	p.Auth = auth
	if auth == AuthHeader {
		p.Header = "X-Api-Key"
	}
	p.Secret = &secret
	return p
}

func plainStore(t *testing.T) *Store {
	t.Helper()
	s := OpenStore(t.TempDir())
	s.SetSecrets(filepath.Join(s.dir, "mcp-secrets.json"), nil, true) // the plaintext vault mode
	return s
}

func setServers(s *Store, list ...MCPServerPatch) error {
	_, err := s.SetNet(NetPatch{MCPServers: &list}, "user:ana")
	return err
}

// covers D240 — what an admin lists is checked before anything is kept:
// https unless plainHttp says so, a host (no user info), an id, a name, a
// header xbin doesn't set itself, a secret for an auth that needs one
func TestMCPServerListChecked(t *testing.T) {
	s := plainStore(t)
	bad := map[string]MCPServerPatch{
		"http without plainHttp":  srv("a", "http://mcp.example.com/mcp"),
		"user info":               srv("a", "https://u:p@mcp.example.com/mcp"),
		"a numeric host":          srv("a", "https://2130706433/mcp"),
		"localhost unmarked":      srv("a", "https://localhost:9000/mcp"),
		"an id with capitals":     srv("A", "https://mcp.example.com/mcp"),
		"no name":                 {MCPServer: MCPServer{ID: "a", URL: "https://mcp.example.com/mcp"}},
		"a name with a newline":   {MCPServer: MCPServer{ID: "a", Name: "x\ny", URL: "https://mcp.example.com/mcp"}},
		"bearer, no secret":       {MCPServer: MCPServer{ID: "a", Name: "a", URL: "https://mcp.example.com/mcp", Auth: AuthBearer}},
		"a reserved header":       withSecret(MCPServerPatch{MCPServer: MCPServer{ID: "a", Name: "a", URL: "https://mcp.example.com/mcp"}}, AuthHeader, "k"),
		"an unknown auth":         {MCPServer: MCPServer{ID: "a", Name: "a", URL: "https://mcp.example.com/mcp", Auth: "oauth"}},
		"a secret on two lines":   withSecret(srv("a", "https://mcp.example.com/mcp"), AuthBearer, "a\nb"),
		"none with a secret":      withSecret(srv("a", "https://mcp.example.com/mcp"), AuthNone, "x"),
		"the admin's secretFile":  {MCPServer: MCPServer{ID: "a", Name: "a", URL: "https://mcp.example.com/mcp", Auth: AuthBearer, SecretFile: "/etc/passwd"}},
		"the workspace as an org": {MCPServer: MCPServer{ID: "a", Name: "a", URL: "https://mcp.example.com/mcp", Orgs: []string{"workspace"}}},
	}
	reserved := bad["a reserved header"]
	reserved.Header = "Mcp-Session-Id"
	bad["a reserved header"] = reserved
	for what, p := range bad {
		if err := setServers(s, p); err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	if err := setServers(s, srv("a", "https://mcp.example.com/mcp"), srv("a", "https://mcp2.example.com/mcp")); err == nil {
		t.Error("an id twice")
	}
	// what is taken, canonical
	ok := srv("plain", "http://mcp.example.com:8080/mcp#frag")
	ok.PlainHTTP = true
	loc := srv("local", "https://LocalHost:9000/mcp")
	loc.PrivateNetwork = true
	if err := setServers(s, ok, loc); err != nil {
		t.Fatal(err)
	}
	got := s.MCPServers()
	if len(got) != 2 || got[0].URL != "http://mcp.example.com:8080/mcp" || got[1].URL != "https://localhost:9000/mcp" || got[0].Auth != AuthNone {
		t.Errorf("kept: %+v", got)
	}
	if v := s.NetPolicy().MCP; len(v.Servers) != 2 || v.Servers[0].Server != "ext:plain" || v.Servers[0].Host != "mcp.example.com" {
		t.Errorf("in force: %+v", v)
	}
}

// covers D240 — a secret is write-only: kept sealed apart from the list,
// never in the list's file or a view; kept when not sent, removed with "",
// and never sent to another host than the one it was given for
func TestMCPServerSecrets(t *testing.T) {
	s := plainStore(t)
	gh := withSecret(srv("gh", "https://mcp.example.com/mcp"), AuthBearer, "ghp-TOPSECRET")
	if err := setServers(s, gh); err != nil {
		t.Fatal(err)
	}
	ws, _ := os.ReadFile(filepath.Join(s.dir, "workspace.json"))
	view, _ := json.Marshal(s.NetPolicy())
	if strings.Contains(string(ws), "TOPSECRET") || strings.Contains(string(view), "TOPSECRET") {
		t.Fatalf("a secret in the list or the view: %s %s", ws, view)
	}
	m, _ := s.MCPServerNamed("ext:gh")
	if sec, err := s.MCPSecret(m); err != nil || sec.Reveal() != "ghp-TOPSECRET" || !m.HasSecret {
		t.Fatalf("the secret: %v %v", err, m.HasSecret)
	}
	// sent again without its secret: kept at the same address…
	keep := srv("gh", "https://mcp.example.com/mcp")
	keep.Auth = AuthBearer
	keep.Note = "a new note"
	if err := setServers(s, keep); err != nil {
		t.Fatal(err)
	}
	// …and not to another path on the same host (shared hosts route by path)
	path := keep
	path.URL = "https://mcp.example.com/v2/mcp"
	if err := setServers(s, path); err == nil || !strings.Contains(err.Error(), "give its secret again") {
		t.Errorf("a secret followed its server to another path: %v", err)
	}
	if m, _ := s.MCPServerNamed("ext:gh"); !m.HasSecret {
		t.Error("the secret was dropped")
	}
	// …but not to another host
	moved := keep
	moved.URL = "https://evil.example.net/mcp"
	if err := setServers(s, moved); err == nil || !strings.Contains(err.Error(), "give its secret again") {
		t.Errorf("a secret followed its server to another host: %v", err)
	}
	clear := keep // …nor to plain http on the same host
	clear.URL, clear.PlainHTTP = "http://mcp.example.com/mcp", true
	if err := setServers(s, clear); err == nil || !strings.Contains(err.Error(), "give its secret again") {
		t.Errorf("a secret followed its server to plain http: %v", err)
	}
	// "" removes it — and then bearer has none
	empty := keep
	empty.Secret = new(string)
	if err := setServers(s, empty); err == nil {
		t.Error("bearer without its secret taken")
	}
	// none drops it; a server that leaves takes its secret with it
	none := srv("gh", "https://mcp.example.com/mcp")
	if err := setServers(s, none, withSecret(srv("x", "https://x.example.com/mcp"), AuthHeader, "xk")); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Secrets.Names(); strings.Join(names, ",") != "x" {
		t.Errorf("secrets after none: %v", names)
	}
	if err := setServers(s); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Secrets.Names(); len(names) != 0 {
		t.Errorf("secrets after the list emptied: %v", names)
	}
	// no vault: a secret is refused, nothing written
	bare := OpenStore(t.TempDir())
	var sv *ErrSecretVault
	if err := setServers(bare, gh); !errors.As(err, &sv) {
		t.Errorf("no vault: %v", err)
	}
	if len(bare.MCPServers()) != 0 {
		t.Error("the list was written without its secret")
	}
}

// covers D240 — what a person allows of a server is kept under its id AND
// its address: a changed URL, or the id given to another server, is a
// server nobody allowed
func TestMCPServerKeyFollowsTheAddress(t *testing.T) {
	a := MCPServer{ID: "gh", URL: "https://mcp.example.com/mcp"}
	b := MCPServer{ID: "gh", URL: "https://mcp.example.com/other"}
	if a.Key() == b.Key() || !strings.HasPrefix(a.Key(), "ext:gh@") || !IsExt(a.Key()) || a.Server() != "ext:gh" {
		t.Errorf("keys %s %s", a.Key(), b.Key())
	}
}

// covers D240 — a secret never prints: not by fmt in any verb, not in JSON,
// not in a log line
func TestSecretNeverPrints(t *testing.T) {
	m := MCPServer{ID: "gh", HostSecret: NewSecret("hunter2-VALUE")}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "server", m, "secret", m.HostSecret)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "server", m, "secret", m.HostSecret)
	j, _ := json.Marshal(m)
	js, _ := json.Marshal(m.HostSecret)
	for _, out := range []string{fmt.Sprint(m), fmt.Sprintf("%+v", m), fmt.Sprintf("%#v", m), fmt.Sprintf("%v %s %q", m.HostSecret, m.HostSecret, m.HostSecret), string(j), string(js), buf.String()} {
		if strings.Contains(out, "hunter2") {
			t.Errorf("printed: %s", out)
		}
	}
	if m.HostSecret.Reveal() != "hunter2-VALUE" {
		t.Error("Reveal")
	}
}

// covers D240 — a list the host's file names is the list in force, locked:
// the admin's is kept but not used, and a write of the list is refused
func TestMCPHostListLocks(t *testing.T) {
	s := plainStore(t)
	if err := setServers(s, srv("mine", "https://mine.example.com/mcp")); err != nil {
		t.Fatal(err)
	}
	host, err := CheckHostServers([]MCPServer{{ID: "corp", Name: "Corp", URL: "https://mcp.corp.example/mcp", Auth: AuthBearer, SecretEnv: "CORP_TOKEN", HostSecret: NewSecret("corp-tok")}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetHost(func() HostNet { return HostNet{MCP: &MCPPolicy{Mode: MCPListed, Servers: &host}} })
	got := s.MCPServers()
	if len(got) != 1 || got[0].ID != "corp" || !got[0].ByHost() {
		t.Fatalf("in force: %+v", got)
	}
	if sec, err := s.MCPSecret(got[0]); err != nil || sec.Reveal() != "corp-tok" {
		t.Errorf("the host's secret: %v", err)
	}
	pol := s.NetPolicy().MCP
	if !pol.ServersByHost || len(pol.Servers) != 1 || !pol.Servers[0].ByHost || pol.Mode != MCPListed {
		t.Errorf("policy: %+v", pol)
	}
	var lk *ErrHostLocked
	if err := setServers(s, srv("mine", "https://mine.example.com/mcp")); !errors.As(err, &lk) || strings.Join(lk.Settings, ",") != "mcp.servers" {
		t.Errorf("a write of the host's list: %v", err)
	}
	if l := s.Host().Locks()["mcp.servers"]; !l.Locked {
		t.Error("locks")
	}
	// an empty list the host names is a list too
	empty := []MCPServer{}
	s.SetHost(func() HostNet { return HostNet{MCP: &MCPPolicy{Servers: &empty}} })
	if len(s.MCPServers()) != 0 || setServers(s) == nil {
		t.Error("an empty host list")
	}
	// the host's list wants a secret by reference, one way
	for _, bad := range []MCPServer{{ID: "a", Name: "a", URL: "https://a.example/mcp", Auth: AuthBearer},
		{ID: "a", Name: "a", URL: "https://a.example/mcp", Auth: AuthBearer, SecretFile: "f", SecretEnv: "E"}} {
		if _, err := CheckHostServers([]MCPServer{bad}); err == nil {
			t.Errorf("%+v taken", bad)
		}
	}
	// a mode the host caps at tiles: listed is past it
	s.SetHost(func() HostNet { return HostNet{MCP: &MCPPolicy{Mode: MCPTiles}} })
	if _, err := s.SetNet(NetPatch{MCPMode: strp(MCPListed)}, "user:ana"); !errors.As(err, &lk) {
		t.Errorf("listed past a tiles ceiling: %v", err)
	}
}

// covers D240 — visibility: a server listed for orgs is seen by their
// members only; for no org, by everyone
func TestMCPServerVisible(t *testing.T) {
	m := MCPServer{Orgs: []string{"finance"}}
	if !m.Visible([]string{"sales", "finance"}) || m.Visible([]string{"sales"}) || m.Visible(nil) || !(MCPServer{}).Visible(nil) || !m.Inside("finance") || m.Inside("sales") {
		t.Error("visible")
	}
}

// covers D240 — a stored secret is the address's it was given for: a list
// saved apart from its secrets (a write whose list didn't land, a hand
// edit) that pairs it with another address gets nothing sent
func TestMCPServerSecretBoundToItsURL(t *testing.T) {
	s := plainStore(t)
	if err := setServers(s, withSecret(srv("gh", "https://mcp.example.com/mcp"), AuthBearer, "ghp-BOUND")); err != nil {
		t.Fatal(err)
	}
	// the list as if its write had failed after the secret's: the old id at another address, hasSecret still
	w := s.Workspace()
	list := *w.MCP.Servers
	list[0].URL = "https://mcp.example.com/other"
	w.MCP.Servers = &list
	if err := save(&s.ws, filepath.Join(s.dir, "workspace.json"), w); err != nil {
		t.Fatal(err)
	}
	m, _ := s.MCPServerNamed("ext:gh")
	if sec, err := s.MCPSecret(m); err == nil || sec.Set() || !strings.Contains(err.Error(), "another address") {
		t.Errorf("a secret paired with another address: %v", err)
	}
	m.URL = "https://mcp.example.com/mcp"
	if sec, err := s.MCPSecret(m); err != nil || sec.Reveal() != "ghp-BOUND" {
		t.Errorf("at its own address: %v", err)
	}
}
