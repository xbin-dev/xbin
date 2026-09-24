//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run Confined
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

// A Go backend builds inside the sandbox with the host toolchain: the
// workspace's go.work resolves through the read-only root bind, the binary
// lands in the tile's build dir, caches are the tile's own — and a hostile
// .git/config (which VCS stamping would run) never runs on the host.
func TestConfinedGoBuild(t *testing.T) {
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "etc", "os-release")); err != nil || !sandbox.Available() {
		t.Skip("no rootfs or no user namespaces")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "x")
	write := func(p, s string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(tile, "go.mod"), "module example.com/x\n\ngo 1.22\n")
	write(filepath.Join(tile, "backend", "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"built in a box\") }\n")
	write(filepath.Join(root, "go.work"), "go 1.22\n\nuse ./apps/x\n")
	write(filepath.Join(root, "data", "vault"), "secret")
	marker := filepath.Join(t.TempDir(), "PWNED")
	if out, err := exec.Command("git", "-C", tile, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	cfg, _ := os.OpenFile(filepath.Join(tile, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = cfg.WriteString("[core]\n\tfsmonitor = \"touch " + marker + "; echo\"\n")
	cfg.Close()

	confine.Configure(fs)
	defer confine.Configure("")
	r := &Runner{Root: root, Isolate: true, Rootfs: fs}
	c := &registry.Component{Path: "apps/x", Dir: tile, Manifest: registry.Manifest{Runtime: "go"}}
	bin, err := r.build(c)
	if err != nil {
		if be, ok := err.(*BuildError); ok {
			t.Fatalf("build: %s", be.Output)
		}
		t.Fatalf("build: %v", err)
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "built in a box" {
		t.Fatalf("the backend: %q %v", out, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tile's .git/config ran on the host")
	}
	for _, d := range []string{"go-build", "mod"} {
		if _, err := os.Stat(filepath.Join(root, ".xbin", "cache", "tile", util.CompKey("apps/x"), d)); err != nil {
			t.Fatalf("the tile's own %s cache: %v", d, err)
		}
	}
	// a compile error comes back as the build's output, like a host build's
	write(filepath.Join(tile, "backend", "main.go"), "package main\n\nfunc main() { undefined() }\n")
	if _, err := r.build(c); err == nil || !strings.Contains(err.(*BuildError).Output, "undefined") {
		t.Fatalf("a broken build: %v", err)
	}
}
