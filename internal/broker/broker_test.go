package broker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
)

func testWorkspace(t *testing.T) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1,"grants":[
		{"from":"apps/email","target":"apps/calendar","role":"reader"}
	]}`)
	write("apps/calendar/scope.json", `{"resources":{
		"bus":{"type":"bus"},"events":{"type":"kv"},"db":{"type":"sqlite"}}}`)
	write("apps/calendar/xbin.json", `{
		"runtime":"go",
		"expose":{"roles":{"reader":"r","writer":"w"}},
		"uses":[{"target":"res:apps/calendar/events","role":"writer"},
		        {"target":"res:apps/calendar/bus","role":"writer"}]}`)
	write("apps/calendar/index.html", `<html></html>`)
	write("apps/email/xbin.json", `{
		"runtime":"go",
		"uses":[{"target":"apps/calendar","role":"reader"},
		        {"target":"res:apps/calendar/bus","role":"reader"}]}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func testBroker(t *testing.T) *Broker {
	t.Helper()
	b, err := New(testWorkspace(t), events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoleSatisfies(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{"admin", "reader", true},
		{"writer", "reader", true},
		{"reader", "writer", false},
		{"reader", "reader", true},
		{"publisher", "writer", true}, // bus alias
		{"subscriber", "reader", true},
		{"custom", "custom", true},
		{"custom", "reader", false},
	}
	for _, c := range cases {
		if got := roleSatisfies(c.have, c.want, nil); got != c.ok {
			t.Errorf("roleSatisfies(%q,%q)=%v want %v", c.have, c.want, got, c.ok)
		}
	}
	// Manifest implications for custom roles.
	exp := &registry.Expose{Implies: map[string][]string{"auditor": {"reader"}}}
	if !roleSatisfies("auditor", "reader", exp) {
		t.Error("implies chain should satisfy")
	}
}

func TestPolicy(t *testing.T) {
	b := testBroker(t)
	cal, _ := b.Reg.Component("apps/calendar")

	if role, ok := b.Policy(auth.Principal{Owner: true}, cal); !ok || role != "admin" {
		t.Fatalf("owner: %v %v", role, ok)
	}
	if role, ok := b.Policy(auth.Principal{Component: "apps/calendar"}, cal); !ok || role != "admin" {
		t.Fatalf("self: %v %v", role, ok)
	}
	if role, ok := b.Policy(auth.Principal{Component: "apps/email"}, cal); !ok || role != "reader" {
		t.Fatalf("granted: %v %v", role, ok)
	}
	if _, ok := b.Policy(auth.Principal{Component: "apps/other"}, cal); ok {
		t.Fatal("ungranted caller must be denied")
	}
}

func TestSameScopeAutoGrant(t *testing.T) {
	b := testBroker(t)
	// calendar uses its own scope's kv at writer — no explicit grant row.
	if role, ok := b.grantedRole("apps/calendar", "res:apps/calendar/events"); !ok || role != "writer" {
		t.Fatalf("same-scope auto-grant: %v %v", role, ok)
	}
	// email's bus use is cross-scope and NOT granted → pending.
	if _, ok := b.grantedRole("apps/email", "res:apps/calendar/bus"); ok {
		t.Fatal("cross-scope use must not auto-grant")
	}
	pending := b.Pending()
	found := false
	for _, g := range pending {
		if g.From == "apps/email" && g.Target == "res:apps/calendar/bus" && g.Role == "reader" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected pending bus grant, got %+v", pending)
	}
}

func TestAllowRes(t *testing.T) {
	b := testBroker(t)
	// same-scope writer ok
	if err := b.allowRes(auth.Principal{Component: "apps/calendar"}, "res:apps/calendar/events", "writer"); err != nil {
		t.Fatal(err)
	}
	// cross-scope denied until granted
	p := auth.Principal{Component: "apps/email"}
	if err := b.allowRes(p, "res:apps/calendar/bus", "reader"); err == nil {
		t.Fatal("want denial before grant")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "res:apps/calendar/bus", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.allowRes(p, "res:apps/calendar/bus", "reader"); err != nil {
		t.Fatalf("granted but denied: %v", err)
	}
	// reader grant must not satisfy writer
	if err := b.allowRes(p, "res:apps/calendar/bus", "writer"); err == nil {
		t.Fatal("reader grant satisfied writer")
	}
	// owner always passes
	if err := b.allowRes(auth.Principal{Owner: true}, "res:apps/calendar/bus", "writer"); err != nil {
		t.Fatal(err)
	}
	// unknown resource
	if err := b.allowRes(p, "res:apps/nope/x", "reader"); err == nil {
		t.Fatal("unknown resource must fail")
	}
}

func TestBusFilter(t *testing.T) {
	b := testBroker(t)
	ev := events.Event{Type: "bus", Topic: "res:apps/calendar/bus/events/created"}
	if b.busFilter(auth.Principal{Component: "apps/email"}, ev) {
		t.Fatal("ungranted subscriber received bus event")
	}
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "res:apps/calendar/bus", Role: "reader"})
	})
	if !b.busFilter(auth.Principal{Component: "apps/email"}, ev) {
		t.Fatal("granted subscriber filtered out")
	}
	if b.busFilter(auth.Principal{Component: "apps/other"}, ev) {
		t.Fatal("other component received bus event")
	}
}

func TestVaultRefusesPlaintextByDefault(t *testing.T) {
	b := testBroker(t) // no barrier configured, AllowInsecureVault defaults false
	// Secure default: a write with no encryption barrier is refused, not
	// silently written in the clear.
	if err := b.vaultWrite("apps/calendar", map[string]string{"k": "v"}); err == nil {
		t.Fatal("vaultWrite persisted plaintext with no barrier and AllowInsecureVault=false")
	}
	// Opt-in (dev / --insecure-vault) allows plaintext.
	b.AllowInsecureVault = true
	if err := b.vaultWrite("apps/calendar", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("plaintext write should succeed when allowed: %v", err)
	}
	m, err := b.vaultRead("apps/calendar")
	if err != nil || m["k"] != "v" {
		t.Fatalf("read back: %v %v", err, m)
	}
}
