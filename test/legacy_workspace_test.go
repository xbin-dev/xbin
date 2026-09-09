//go:build integration

package test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A workspace created by an older xbind must boot on today's binary with
// exactly the migrations the contract allows, and a second boot must change
// nothing (docs/compat.md rules 1 and 9). The fixture is the current
// scaffold, aged: a legacy shared home/ holding real data, no homes/, no
// backfill ledger, no builtins provenance (an "adopted" workspace), no
// tiles/organisations (it predates the essential tile), a .gitignore without
// homes/. Every migration boot performs must be visible in the allowed list
// below; anything else that changes is a regression — the root xbin.json
// (the import map) in particular is never rewritten.
func TestLegacyWorkspaceBootsTwice(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", ws).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	age := func() {
		// legacy shared home with a real file (a pristine-only home/ is just deleted)
		_ = os.RemoveAll(filepath.Join(ws, "homes"))
		must(t, os.MkdirAll(filepath.Join(ws, "home", ".claude"), 0o755))
		must(t, os.WriteFile(filepath.Join(ws, "home", ".claude", "settings.json"), []byte(`{"kept":true}`), 0o644))
		_ = os.Remove(filepath.Join(ws, "data", "backfills.json"))
		_ = os.Remove(filepath.Join(ws, ".xbin", "builtins.json"))
		_ = os.RemoveAll(filepath.Join(ws, ".xbin", "builtins"))
		_ = os.RemoveAll(filepath.Join(ws, "tiles", "organisations"))
		gi := filepath.Join(ws, ".gitignore")
		if b, err := os.ReadFile(gi); err == nil {
			must(t, os.WriteFile(gi, []byte(strings.ReplaceAll(string(b), "homes/\n", "")), 0o644))
		}
	}
	age()
	before := snapshot(t, ws)
	if _, ok := before["home/.claude/settings.json"]; !ok {
		t.Fatal("fixture: legacy home/ missing")
	}

	// ---- boot #1: the migrations run ----
	log1 := bootOnce(t, ws)
	after1 := snapshot(t, ws)
	for _, want := range []string{"homes/owner/.claude/settings.json", "tiles/organisations/xbin.json", "data/backfills.json", "AGENTS.md"} {
		if _, ok := after1[want]; !ok {
			t.Errorf("after the first boot %s is missing (boot log:\n%s)", want, log1)
		}
	}
	if _, ok := after1["home/.claude/settings.json"]; ok {
		t.Error("legacy home/ still present after migration")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, ".gitignore")); !strings.Contains(string(b), "homes/") {
		t.Error(".gitignore did not gain homes/")
	}
	if before["xbin.json"] != after1["xbin.json"] {
		t.Error("the root xbin.json (the import map, grants) was rewritten at boot — an upgrade must never touch it")
	}
	// Everything else that changed must be a migration the contract allows.
	allowed := func(rel string) bool {
		for _, p := range []string{".xbin/", "data/", "homes/", "home/", "tiles/organisations/", "go.work", ".gitignore", "AGENTS.md", "CLAUDE.md", ".git/"} {
			if rel == strings.TrimSuffix(p, "/") || strings.HasPrefix(rel, p) {
				return true
			}
		}
		// materialised sdk/dep symlinks; per-component git repos (boot creates
		// the ones an older init never made — code PRs need them)
		return strings.Contains(rel, "/deps/") || strings.Contains(rel, "/.git/")
	}
	for _, rel := range changed(before, after1) {
		if !allowed(rel) {
			t.Errorf("first boot changed %s, which no migration is allowed to touch (docs/compat.md rule 1)", rel)
		}
	}

	// ---- boot #2: nothing changes ----
	bootOnce(t, ws)
	after2 := snapshot(t, ws)
	if diff := changed(after1, after2); len(diff) > 0 {
		t.Errorf("a second boot of the same binary changed files — migrations must be idempotent:\n  %s", strings.Join(diff, "\n  "))
	}
}

// Both home/ (with real data) and homes/ present: the daemon must refuse to
// start rather than merge by guesswork (docs/compat.md rule 1 keeps this stop).
func TestLegacyWorkspaceHomeConflictStops(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", ws).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	must(t, os.MkdirAll(filepath.Join(ws, "home", ".claude"), 0o755))
	must(t, os.WriteFile(filepath.Join(ws, "home", ".claude", "settings.json"), []byte(`{"old":true}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(ws, "homes", "owner", ".claude"), 0o755))
	must(t, os.WriteFile(filepath.Join(ws, "homes", "owner", ".claude", "settings.json"), []byte(`{"new":true}`), 0o644))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(xbindBin, "--workspace", ws, "--listen", addr, "--no-auth", "--insecure-vault")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("xbind exited 0 with a home/ + homes/ conflict:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "home/ holds real data") {
			t.Errorf("no merge-by-hand message in the boot log:\n%s", out.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("xbind kept running with a home/ + homes/ conflict:\n%s", out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "home", ".claude", "settings.json")); string(b) != `{"old":true}` {
		t.Error("the refused boot touched the legacy home/")
	}
}

// bootOnce starts the daemon on ws, waits until it is healthy, stops it, and
// returns its log.
func bootOnce(t *testing.T, ws string) string {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(xbindBin, "--workspace", ws, "--listen", addr, "--no-auth", "--insecure-vault")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	healthy := waitFor(func() bool {
		r, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 15*time.Second)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	_ = cmd.Wait()
	if !healthy {
		t.Fatalf("xbind never became healthy on the legacy workspace:\n%s", out.String())
	}
	return out.String()
}

// snapshot maps every file under ws (rel path → content hash), skipping the
// runtime state a boot legitimately churns: logs, sockets, build and cache
// dirs, terminal layers.
func snapshot(t *testing.T, ws string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".xbin/log", ".xbin/run", ".xbin/term", ".xbin/build", ".xbin/cache", ".xbin/env", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".log") || strings.HasSuffix(rel, ".sock") || strings.HasSuffix(rel, ".pid") {
			return nil
		}
		// git's stat cache and reflogs churn without any object or ref changing
		if strings.HasSuffix(rel, "/.git/index") || strings.Contains(rel, "/.git/logs/") {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// changed lists paths whose content differs between two snapshots (added,
// removed or rewritten), sorted.
func changed(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, k+" (removed)")
		} else if w != v {
			out = append(out, k+" (rewritten)")
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k+" (added)")
		}
	}
	sort.Strings(out)
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
