//go:build integration

package test

import (
	"bytes"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// covers P15 T11 PO-7 — the legacy fixture's variant with deployment state
// (15-test-plan §6): on a fresh workspace, the feature itself pauses live
// reload on a static tile and the work tree moves on; then two boots of the
// real binary change nothing outside derived trees (materializations under
// .xbin/deploy/ may be rebuilt): the record, the checkpoint store, the view
// repository's refs and every tile work tree stay byte-identical, and each
// tile's repository (refs, HEAD, config) is compared explicitly. The paused
// tile still serves its checkpoint afterwards. The fixture's M2 parts (a
// static dev deployment, an edge policy, a dormant job of dev's) wait for
// M2: this build answers them 501, logged.
func TestDeploymentStateBootsTwice(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", ws).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	const tile = "apps/ds-paused"
	mustRT(t, ws, tile, "static", "v1", "")
	must(t, saveIfChanged(filepath.Join(ws, tile, "style.css"), "/* v1 */\n"))
	record, _, store, view, _ := dlPaths(ws, tile)

	serveOnce(t, ws, func(a dlAPI) {
		a.waitServed(t, tile, "static", "v1", 30*time.Second)
		if _, e := a.op(t, "live-reload/pause", tile); e.Result != "ok" {
			t.Fatalf("the pause: %+v", e)
		}
		mustRT(t, ws, tile, "static", "v2", "") // the work tree moves on past the checkpoint
		for route, body := range map[string]string{
			"add":  dlBody(tile, "deployment", "dev"),
			"edge": dlBody(tile, "edge", "slot:net", "policy", "block"),
		} {
			c, _, b := a.post(t, route, body)
			t.Logf("M2's part of the fixture, %s: %d %.120s", route, c, b)
		}
	})
	for _, p := range []string{record, store, view} {
		if !isoExists(p) {
			t.Fatalf("the fixture has no %s", p)
		}
	}
	before := snapshot(t, ws)
	repos := tileRepos(t, ws)
	if len(repos) == 0 {
		t.Fatal("the fixture has no tile repository to compare")
	}
	for _, p := range []string{record, store, view} {
		rel, _ := filepath.Rel(ws, p)
		if !snapshotHas(before, filepath.ToSlash(rel)) {
			t.Fatalf("the snapshot doesn't cover %s", rel)
		}
	}

	derived := func(rel string) bool { return strings.HasPrefix(rel, ".xbin/deploy/") }
	prev := before
	for boot := 1; boot <= 2; boot++ {
		log := bootOnce(t, ws)
		now := snapshot(t, ws)
		var diff []string
		for _, rel := range changed(prev, now) {
			if !derived(rel) {
				diff = append(diff, rel)
			}
		}
		if len(diff) > 0 {
			t.Errorf("boot %d changed files outside derived trees:\n  %s\n(boot log:\n%s)", boot, strings.Join(diff, "\n  "), log)
		}
		prev = now
	}
	after := tileRepos(t, ws)
	for rel, b := range repos {
		if after[rel] != b {
			t.Errorf("the boots rewrote the tile repository file %s", rel)
		}
	}
	for rel := range after {
		if _, ok := repos[rel]; !ok {
			t.Errorf("the boots added the tile repository file %s", rel)
		}
	}

	serveOnce(t, ws, func(a dlAPI) {
		a.waitServed(t, tile, "static", "v1", 30*time.Second)
		if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") == "" {
			t.Errorf("after the boots: live reload %q, main %q", st.LiveReload, st.pinned("main"))
		}
	})
}

// serveOnce runs the real binary on ws (as bootOnce does), hands its API to
// fn, then stops it with SIGTERM and waits for it.
func serveOnce(t *testing.T, ws string, fn func(a dlAPI)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(xbindBin, "--workspace", ws, "--listen", addr, "--no-auth", "--insecure-vault")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	must(t, cmd.Start())
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("xbind:\n%s", out.String())
		}
	}()
	if !waitFor(func() bool {
		r, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 15*time.Second) {
		t.Fatalf("xbind never became healthy:\n%s", out.String())
	}
	fn(dlAPI{url: "http://" + addr})
}

// tileRepos reads every tile repository's HEAD, config and refs (loose and
// packed), keyed by workspace-relative path: the files a rewrite of a tile's
// repository would touch.
func tileRepos(t *testing.T, ws string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if !e.IsDir() {
			return nil
		}
		switch rel {
		case ".xbin", "data", "homes", ".git":
			return fs.SkipDir
		}
		if e.Name() != ".git" {
			return nil
		}
		_ = filepath.WalkDir(p, func(q string, f fs.DirEntry, err error) error {
			if err != nil || f.IsDir() {
				return nil
			}
			r, _ := filepath.Rel(p, q)
			r = filepath.ToSlash(r)
			if r == "HEAD" || r == "config" || r == "packed-refs" || strings.HasPrefix(r, "refs/") {
				b, _ := os.ReadFile(q)
				out[rel+"/"+r] = string(b)
			}
			return nil
		})
		return fs.SkipDir
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// snapshotHas says whether a snapshot holds rel or anything under it.
func snapshotHas(snap map[string]string, rel string) bool {
	for k := range snap {
		if k == rel || strings.HasPrefix(k, rel+"/") {
			return true
		}
	}
	return false
}
