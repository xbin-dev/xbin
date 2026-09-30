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
