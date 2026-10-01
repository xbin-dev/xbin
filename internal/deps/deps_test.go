package deps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

func TestGoWorkSelfHeal(t *testing.T) {
	root := t.TempDir()
	mk := func(p, content string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("xbin.json", `{"schema":1}`)
	mk("apps/a/xbin.json", `{"runtime":"go"}`) // module at the component root
	mk("apps/a/go.mod", "module a\ngo 1.24\n")
	mk("apps/b/xbin.json", `{"runtime":"go"}`) // module in backend/ (non-standard)
	mk("apps/b/backend/go.mod", "module b\ngo 1.24\n")

	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	wp := filepath.Join(root, "go.work")

	// 1. No go.work → generate with BOTH modules (root + backend/) and the marker.
	if err := GoWork(reg, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(wp)
	for _, want := range []string{workMarker, "./apps/a", "./apps/b/backend"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("fresh go.work missing %q:\n%s", want, got)
		}
	}

	// 2. Simulate `go work use` stripping the marker AND a module (stale/broken).
	mk("go.work", "go 1.24\n\nuse (\n\tapps/a\n)\n") // missing apps/b/backend, no marker
	if err := GoWork(reg, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(wp)
	if !strings.Contains(string(got), "./apps/b/backend") || !strings.Contains(string(got), workMarker) {
		t.Fatalf("stale go.work was not reclaimed:\n%s", got)
	}

	// 3. Hand-managed AND complete (no marker, lists every module) → left alone.
	hand := "go 1.24\n\nuse (\n\t./apps/a\n\t./apps/b/backend\n)\n"
	mk("go.work", hand)
	if err := GoWork(reg, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(wp)
	if string(got) != hand {
		t.Fatalf("a complete hand-managed go.work must be left untouched, got:\n%s", got)
	}
}

func TestMissingModules(t *testing.T) {
	// Recognises entries with and without the leading "./" (the go tool omits it).
	content := "use (\n\tapps/a\n\t./apps/b/backend\n)\n"
	m := missingModules(content, []string{"./apps/a", "./apps/b/backend", "./apps/c"})
	if len(m) != 1 || m[0] != "./apps/c" {
		t.Fatalf("missingModules = %v, want [./apps/c]", m)
	}
}

// D40: GoWorkFor filters the rendered go.work to the components a restricted
// terminal may read — no leaked names, no `use` of dirs absent from the
// allow-list mount.
func TestGoWorkFor(t *testing.T) {
	root := t.TempDir()
	mk := func(p, content string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("xbin.json", `{"schema":1}`)
	mk("apps/mine/xbin.json", `{"runtime":"go"}`)
	mk("apps/mine/go.mod", "module mine\ngo 1.24\n")
	mk("apps/secret/xbin.json", `{"runtime":"go"}`)
	mk("apps/secret/backend/go.mod", "module secret\ngo 1.24\n")
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got := GoWorkFor(reg, "/opt/xbin/sdk", func(p string) bool { return p == "apps/mine" })
	if !strings.Contains(got, "./apps/mine") || strings.Contains(got, "secret") {
		t.Fatalf("filtered go.work wrong:\n%s", got)
	}
	if !strings.Contains(got, "/opt/xbin/sdk") {
		t.Error("sdk replace must survive")
	}
	if GoWorkFor(reg, "", func(string) bool { return false }) != "" {
		t.Error("nothing readable and no sdk → empty")
	}
}

// The generated go.work's go line is the highest of 1.24 and its modules':
// the go command refuses, for every command run with it, a go.work whose go
// line is below a module it uses ("module apps/c listed in go.work file
// requires go >= 1.26.0, but go.work lists go 1.24"). A go line that isn't a
// Go version, and a go.mod reached through a symlink, never raise it; a
// restricted terminal's go.work counts only the modules it lists.
func TestGoWorkGoLine(t *testing.T) {
	root := t.TempDir()
	mk := func(p, content string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("xbin.json", `{"schema":1}`)
	mk("apps/a/xbin.json", `{"runtime":"go"}`)
	mk("apps/a/go.mod", "module a\n\ngo 1.22\n")
	mk("apps/b/xbin.json", `{"runtime":"go"}`)
	mk("apps/b/backend/go.mod", "module b\n\ngo 1.24\n")
	mk("apps/odd/xbin.json", `{"runtime":"go"}`)
	mk("apps/odd/go.mod", "module odd\n\ngo 9.x\n")
	outside := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(outside, []byte("module link\n\ngo 1.99.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk("apps/link/xbin.json", `{"runtime":"go"}`)
	if err := os.Symlink(outside, filepath.Join(root, "apps", "link", "go.mod")); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	goLine := func() string {
		t.Helper()
		if err := GoWork(reg, ""); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(root, "go.work"))
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "go "); ok {
				return v
			}
		}
		t.Fatalf("no go line:\n%s", b)
		return ""
	}
	if v := goLine(); v != "1.24" {
		t.Errorf("modules at go 1.22/1.24 (and an odd one, a symlinked one): go %s, want 1.24 as before", v)
	}

	mk("apps/c/xbin.json", `{"runtime":"go"}`)
	mk("apps/c/go.mod", "module c\n\ngo 1.26.0\n\nrequire golang.org/x/crypto v0.57.0\n")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if v := goLine(); v != "1.26.0" {
		t.Errorf("a module at go 1.26.0: go %s, want 1.26.0", v)
	}

	got := GoWorkFor(reg, "", func(p string) bool { return p == "apps/a" || p == "apps/b" })
	if !strings.Contains(got, "\ngo 1.24\n") {
		t.Errorf("a restricted go.work counts only its own modules:\n%s", got)
	}
}
