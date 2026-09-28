package runner

// covers PO-2 Z6 SC-ZERO — TestZeroStateKeys, the runner half (15-test-plan
// §2.7): the storage names the runner derives for a tile's one deployment
// (main) are today's. The expected values are hand-maintained literals
// (hashes computed once with sha256sum, never by the code under test); a
// change to any of them is a compat change (12-compat PO-2).

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PO-2 Z6 — main's runner keys: socket dir, log, build output, Go
// cache, env layer, run dir and registry IDs, and interpreted entries.
func TestZeroStateKeys(t *testing.T) {
	t.Run("the per-tile key", func(t *testing.T) {
		// every per-tile runner store is named by it: socket dir, log, build
		// output, env layers, cgroup leaf, registry IDs
		for _, tc := range []struct{ path, key string }{
			{"apps/x", "apps~x-ebdae547"},
			{"apps/a/b", "apps~a~b-370a3174"},
			{"tiles/a-very-long-tile-name-past-24", "tiles~a-very-long-tile-n-06230721"}, // 24-char truncation
		} {
			if got := util.CompKey(tc.path); got != tc.key {
				t.Errorf("CompKey(%q) = %q, want %q", tc.path, got, tc.key)
			}
		}
	})

	t.Run("the env layer", func(t *testing.T) {
		r := &Runner{Root: "/ws", Isolate: true, Rootfs: "/nonexistent-rootfs"}
		c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go", Setup: "apt-get install -y jq"}}
		if got, want := r.envLayerDir(c), "/ws/.xbin/env/apps~x-ebdae547/754deeed638f32b1"; got != want {
			t.Errorf("env layer dir %q, want %q", got, want)
		}
	})

	t.Run("the run dir", func(t *testing.T) {
		const root = "/xbin-golden/ws" // not creatable: only the tmpfs dir is made
		dir := runDirFor(root)
		if dir == root+"/.xbin/run" {
			return // no tmpfs base on this host: the workspace fallback
		}
		defer os.RemoveAll(filepath.Dir(dir))
		if filepath.Base(dir) != "run" || filepath.Base(filepath.Dir(dir)) != "xbin-a5c30a99" {
			t.Errorf("run dir %q, want <tmpfs>/xbin-a5c30a99/run", dir)
		}
	})

	t.Run("interpreted entries", func(t *testing.T) {
		root := t.TempDir()
		r := &Runner{Root: root}
		for _, tc := range []struct {
			man   registry.Manifest
			entry string
		}{
			{registry.Manifest{Runtime: "node"}, "backend/server.js"},
			{registry.Manifest{Runtime: "python"}, "backend/server.py"},
			{registry.Manifest{Runtime: "node", Entry: "srv/main.js"}, "srv/main.js"},
		} {
			dir := filepath.Join(root, "apps", tc.man.Runtime+strings.ReplaceAll(tc.man.Entry, "/", "-"))
			p := filepath.Join(dir, filepath.FromSlash(tc.entry))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := r.build(&registry.Component{Path: "apps/n", Dir: dir, Manifest: tc.man})
			if err != nil || got != p {
				t.Errorf("%s entry: %q %v, want %q", tc.man.Runtime, got, err, p)
			}
		}
	})

	t.Run("a Go generation through the shipped engine", zeroStateGoGeneration)
}

// zeroStateGoGeneration runs apps/x through New's runner (a nil engine:
// today's build, start, health check and stop). A fake `go` on PATH records
// the build it is asked for and emits a wrapper that runs this test binary as
// the backend (TestZeroStateHelperProcess), so no compiler runs.
func zeroStateGoGeneration(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for the fake go")
	}
	testBin, err := os.Executable()
	if err != nil {
		t.Skip("no test binary path:", err)
	}
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "x")
	if err := os.MkdirAll(tile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tile, "xbin.json"), []byte(`{"runtime":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	goLog, wrapper := filepath.Join(fake, "go.log"), filepath.Join(fake, "backend.sh")
	writeExec(t, filepath.Join(fake, "go"), "#!/bin/sh\n"+
		`{ pwd -P; echo "GOCACHE=$GOCACHE"; echo "$@"; } >> "$FAKE_GO_LOG"`+"\n"+
		`cp "$FAKE_GO_BACKEND" "$3"`+"\n")
	writeExec(t, wrapper, "#!/bin/sh\n"+
		`export GORACE="${GORACE:+$GORACE }atexit_sleep_ms=0"`+"\n"+ // a -race helper exits at once
		fmt.Sprintf("exec '%s' '-test.run=^TestZeroStateHelperProcess$'\n", testBin))
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GO_LOG", goLog)
	t.Setenv("FAKE_GO_BACKEND", wrapper)
	t.Setenv(helperEnv, "1") // a host backend inherits xbind's env, minus XBIN_*

	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	r := New(root, a, events.NewHub(), reg)
	if r.engine != nil {
		t.Fatal("New must leave the engine nil (today's methods)")
	}
	r.Sandboxes = sbx.New()
	t.Cleanup(r.StopAll)
	if rd := r.RunDir; !strings.HasPrefix(rd, root) {
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(rd)) })
	}
	c, ok := reg.Component("apps/x")
	if !ok {
		t.Fatal("apps/x not scanned")
	}
	const key = "apps~x-ebdae547"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sock, err := r.Ensure(ctx, c)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if want := filepath.Join(r.RunDir, key, "g1.sock"); sock != want {
		t.Errorf("socket %q, want %q", sock, want)
	}
	physTile, _ := filepath.EvalSymlinks(tile)
	wantGo := []string{
		physTile,
		"GOCACHE=" + filepath.Join(root, ".xbin", "cache", "go-build"),
		"build -o " + filepath.Join(root, ".xbin", "build", key, "bin") + " ./backend",
	}
	b, _ := os.ReadFile(goLog)
	if got := strings.Split(strings.TrimSpace(string(b)), "\n"); !equalStrings(got, wantGo) {
		t.Errorf("go invocation:\n got %q\nwant %q", got, wantGo)
	}
	logPath := filepath.Join(root, ".xbin", "log", key+".log")
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "--- gen 1 start ") {
		t.Errorf("backend log %s: %v %q", logPath, err, b)
	}
	wantEntry(t, r, "backend:"+key+":g1", 1)

	// a save: g2 builds to the same output, listens on g2.sock, and g1
	// drains (its registry row goes at exit, its socket once stop sees it)
	r.Changed(c)
	waitUntil(t, "g2 serving alone", func() bool {
		l := r.Sandboxes.List(sbx.Filter{})
		_, err := os.Stat(sock)
		return statusOf(r, "apps/x") == "healthy g2" && len(l) == 1 && l[0].Gen == 2 && os.IsNotExist(err)
	})
	if sock2, err := r.Ensure(ctx, c); err != nil || sock2 != filepath.Join(r.RunDir, key, "g2.sock") {
		t.Errorf("after a save: %q %v", sock2, err)
	}
	wantEntry(t, r, "backend:"+key+":g2", 2)
	b, _ = os.ReadFile(goLog)
	if n := strings.Count(string(b), "build -o "+filepath.Join(root, ".xbin", "build", key, "bin")+" ./backend"); n != 2 {
		t.Errorf("builds to main's output: %d, want 2", n)
	}
	b, _ = os.ReadFile(logPath)
	if !strings.Contains(string(b), "--- gen 2 start ") {
		t.Errorf("g2 logs elsewhere than %s", logPath)
	}

	r.StopAll()
	if n := len(r.Sandboxes.List(sbx.Filter{})); n != 0 {
		t.Errorf("%d registry entries after StopAll", n)
	}
}

// wantEntry checks the one D112 registry row a host backend generation has.
func wantEntry(t *testing.T, r *Runner, id string, gen int) {
	t.Helper()
	l := r.Sandboxes.List(sbx.Filter{})
	if len(l) != 1 {
		t.Fatalf("registry entries: %+v", l)
	}
	e := l[0]
	if e.ID != id || e.Kind != sbx.Backend || e.Tile != "apps/x" || e.Gen != gen || e.Mode != sbx.Host || e.Leaf != "" {
		t.Errorf("registry entry %+v, want ID %s gen %d on the host with no leaf (cgroups off)", e, id, gen)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// helperEnv marks the test binary's run as a tile backend.
const helperEnv = "RUNNER_ZEROSTATE_HELPER"

// covers PO-2 — no test of its own: the backend's main when
// zeroStateGoGeneration runs this test binary as apps/x's Go backend.
// It listens on XBIN_SOCKET until SIGTERM (a drain) or a minute passes.
func TestZeroStateHelperProcess(t *testing.T) {
	sock := os.Getenv("XBIN_SOCKET")
	if os.Getenv(helperEnv) != "1" || sock == "" {
		return
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper backend:", err)
		os.Exit(3)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	select {
	case <-sig:
	case <-time.After(time.Minute):
	}
	os.Exit(0)
}
