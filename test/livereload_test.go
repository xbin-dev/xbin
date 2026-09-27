//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// covers P5 P15 SC-ZERO Z1 PO-7 — a zero-state Go tile's whole life on the
// shared daemon (created by copying the counter example, saved, hot-swapped,
// crash-restarted) leaves no tile-deployment state behind: no
// data/deployments, data/checkpoints or .xbin/deploy, no dot-level namespace
// root, nothing per deployment. The root xbin.json and every data/*.json
// store stay byte-identical throughout.
func TestZeroStateLifecycleFiles(t *testing.T) {
	const tile = "apps/zs-counter"
	stores := func() map[string]string {
		out := map[string]string{}
		files, _ := filepath.Glob(filepath.Join(ws, "data", "*.json"))
		for _, p := range append(files, filepath.Join(ws, "xbin.json")) {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(ws, p)
				out[filepath.ToSlash(rel)] = string(b)
			}
		}
		return out
	}
	before := stores()
	names := make([]string, 0, len(before))
	for k := range before {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("stores held byte-identical: %s", strings.Join(names, " "))

	// create: a copy of the counter example (never edited in place), with a
	// module path of its own — the shared workspace's go.work already uses
	// one "module counter" (TestGoBackendLifecycle's copy), and a second
	// would fail every Go build there — moved into place whole
	stage := filepath.Join(t.TempDir(), "zs-counter")
	if out, err := exec.Command("cp", "-r", filepath.Join(repo, "examples", "counter-go"), stage).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	mod, err := os.ReadFile(filepath.Join(stage, "go.mod"))
	if err != nil || !bytes.Contains(mod, []byte("module counter\n")) {
		t.Fatalf("fixture: the counter example's go.mod changed: %v\n%s", err, mod)
	}
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), bytes.Replace(mod, []byte("module counter\n"), []byte("module zscounter\n"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, filepath.Join(ws, filepath.FromSlash(tile))); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		c, b := get(t, "/api/"+tile+"/count")
		return c == 200 && strings.Contains(b, `"count":`)
	}, 120*time.Second) {
		t.Fatal("the copied counter never came up")
	}

	// save: the swap serves the new code
	src := filepath.Join(ws, filepath.FromSlash(tile), "backend", "main.go")
	code, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, bytes.Replace(code, []byte(`"count":%d`), []byte(`"count":%d,"zs":2`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, b := get(t, "/api/"+tile+"/count")
		return strings.Contains(b, `"zs":2`)
	}, 60*time.Second) {
		t.Fatal("the save was never swapped in")
	}

	// crash: kill the running generation; the next call restarts it
	pid := zeroStatePID(t, tile)
	if pid <= 0 {
		t.Fatal("the sandbox registry lists no pid for the tile")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		c, b := get(t, "/api/"+tile+"/count")
		return c == 200 && strings.Contains(b, `"zs":2`) && zeroStatePID(t, tile) != pid
	}, 60*time.Second) {
		t.Fatal("the crashed backend never came back")
	}

	// nothing of tile deployments exists
	for _, rel := range []string{"data/deployments", "data/checkpoints", ".xbin/deploy"} {
		if _, err := os.Lstat(filepath.Join(ws, rel)); err == nil {
			t.Errorf("%s exists after a zero-state tile's life", rel)
		}
	}
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == ".deployments" {
			rel, _ := filepath.Rel(ws, p)
			t.Errorf("a dot-level namespace root exists: %s", filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	after := stores()
	var diff []string
	for k, v := range before {
		if after[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diff = append(diff, k+" (added)")
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("a zero-state tile's save, swap and restart changed stores:\n  %s", strings.Join(diff, "\n  "))
	}
}

// zeroStatePID is the pid of tile's live backend generation, from the
// sandbox registry (0 when none is listed).
func zeroStatePID(t *testing.T, tile string) int {
	t.Helper()
	_, body := get(t, "/api/xbin/sandboxes?tile="+tile)
	var out struct {
		Sandboxes []struct {
			Kind string
			PID  int
		}
	}
	if json.Unmarshal([]byte(body), &out) != nil {
		return 0
	}
	for _, s := range out.Sandboxes {
		if s.Kind == "backend" {
			return s.PID
		}
	}
	return 0
}
