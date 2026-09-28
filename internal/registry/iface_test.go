package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox-net slots are request-side, single and plain, and named so a
// sandbox can select them as class:<slot>; other kinds are not judged here.
func TestValidateInterfaces(t *testing.T) {
	ok := Manifest{
		Interfaces: map[string]Iface{"internet": {Kind: KindSandboxNet}, "lab-2": {Kind: KindSandboxNet}, "Net": {Kind: "net"}, "llm": {Kind: "http", Multi: true}},
		Provides:   map[string]Iface{"egress": {Kind: "net"}},
	}
	if err := ValidateInterfaces(ok); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	for want, m := range map[string]Manifest{
		"request-side only": {Provides: map[string]Iface{"sbx": {Kind: KindSandboxNet}}},
		"is named":          {Interfaces: map[string]Iface{"Internet": {Kind: KindSandboxNet}}},
		"takes no multi":    {Interfaces: map[string]Iface{"internet": {Kind: KindSandboxNet, Multi: true}}},
	} {
		if err := ValidateInterfaces(m); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error with %q, got %v", want, err)
		}
	}
	for _, s := range []string{"", "-x", "a b", "class:x", strings.Repeat("a", 33)} {
		if ValidSandboxNetSlot(s) {
			t.Errorf("%q must not be a sandbox-net slot name", s)
		}
	}
}

// A bad sandbox-net slot is a manifest error at load (bx ls / doctor), and
// the component still loads.
func TestSandboxNetManifestErrAtLoad(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":         `{"schema":1}`,
		"apps/m/xbin.json":  `{"interfaces":{"lab":{"kind":"sandbox-net","multi":true}}}`,
		"apps/ok/xbin.json": `{"interfaces":{"lab":{"kind":"sandbox-net"}}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := r.Component("apps/m"); !ok || !strings.Contains(c.ManifestErr, "interfaces.lab") {
		t.Fatalf("apps/m: %+v", c)
	}
	if c, ok := r.Component("apps/ok"); !ok || c.ManifestErr != "" {
		t.Fatalf("apps/ok: %+v", c)
	}
}
