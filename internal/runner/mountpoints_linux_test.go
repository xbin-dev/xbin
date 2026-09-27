//go:build linux && integration

package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// covers WP-2b — a Go backend's sandbox on the shipped rootfs: it starts
// with its mount points never followed (/run/backend, its source, its run
// dir, the gateway socket), and a symlink its env layer (what its setup
// script left) holds where one goes — /run → a host dir — fails the start
// with the path and the hint, making nothing on the host.
func TestBackendMountPoints(t *testing.T) {
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
	root, host := t.TempDir(), t.TempDir()
	tile, runDir, outside := filepath.Join(root, "apps", "x"), filepath.Join(host, "run"), filepath.Join(host, "outside")
	for _, d := range []string{tile, filepath.Join(runDir, "apps~x"), outside, filepath.Join(host, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(runDir, "gateway.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tile, "f"), []byte("tile-src"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the backend: reads what its binds show and says so
	src := filepath.Join(host, "src", "main.go")
	if err := os.WriteFile(src, []byte(`package main

import (
	"fmt"
	"os"
)

func main() {
	b, _ := os.ReadFile(os.Getenv("PROBE_SRC"))
	_, gw := os.Stat(os.Getenv("PROBE_GW"))
	fmt.Printf("BACKEND-UP %s gw=%v\n", b, gw == nil)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(host, "backend")
	build := exec.Command("go", "build", "-o", bin, src)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the backend: %v\n%s", err, out)
	}

	r := &Runner{Root: root, Isolate: true, Rootfs: fs, RunDir: runDir}
	c := &registry.Component{Path: "apps/x", Dir: tile, Manifest: registry.Manifest{Runtime: "go"}}
	run := func(envLower string) (string, error) {
		t.Helper()
		env := []string{"PATH=/usr/bin:/bin", "PROBE_SRC=" + filepath.Join(tile, "f"), "PROBE_GW=" + filepath.Join(runDir, "gateway.sock")}
		cmd, h, err := r.sandboxCmd(c, bin, filepath.Join(runDir, "apps~x"), filepath.Join(runDir, "apps~x", "g1.sock"),
			env, sandbox.EgressPolicy{}, envLower)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Cleanup()
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		h.Started()
		if err := h.SetupUserns(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("userns: %v\n%s", err, out.String())
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("the sandbox wedged:\n%s", out.String())
		}
		return out.String(), err
	}
	if out, err := run(""); err != nil || !strings.Contains(out, "BACKEND-UP tile-src gw=true") {
		t.Fatalf("a backend on the rootfs: %v\n%s", err, out)
	}

	env := filepath.Join(host, "env")
	if err := os.MkdirAll(env, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(env, "run")); err != nil {
		t.Fatal(err)
	}
	out, err := run(env)
	if want := "nested mount point /run: a symlink is in the way (" + envRootHint + ")"; err == nil || !strings.Contains(out, want) {
		t.Errorf("an env layer's /run -> a host dir: %v, want %q in\n%s", err, want, out)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("made %v on the host where the env layer's /run points", ents)
	}
}
