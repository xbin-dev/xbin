//go:build integration

package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Pinned backends (P9, SC-PINNED) on the isolated daemon: pinning a backend
// needs isolation (P18), so every test here starts its own isolated xbind
// and skips, saying why, where the host can't run one.

// covers P9 flow A — flow A on the probe: pausing live reload keeps m1
// (the pause's code-identical swap announces no reload), an edit to m2
// reaches neither the code nor the bound tree while the work-tree count
// sees it, reload now ships m2 with one reload and no build-* and stays
// paused, and resume follows every save again (the tile is back in the zero
// state).
func TestLiveReloadPauseGo(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/pin-flow"
	writeProbe(t, d.WS, tile, "m1")
	waitProbe(t, d, tile, "m1")
	tape := a.tape(t)

	m := tape.mark()
	_, e := a.op(t, "live-reload/pause", tile)
	if e.How != "pause" || e.Result != "ok" {
		t.Fatalf("the pause's deploy: %+v", e)
	}
	if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") != e.Checkpoint {
		t.Fatalf("after the pause: live reload %q, main %q", st.LiveReload, st.pinned("main"))
	}
	pinnedCode(t, d, tile, "m1", a.state(t, tile).dep("main").Checkpoint.Hash)

	writeProbe(t, d.WS, tile, "m2")
	if !waitFor(func() bool { s := a.state(t, tile); return s.WorkTree != nil && s.WorkTree.Changed >= 2 }, 15*time.Second) {
		t.Errorf("the work-tree count never saw the edit: %+v", a.state(t, tile).WorkTree)
	}
	time.Sleep(3 * latencyDebounce) // negative: the watcher's batch reaches nothing
	pinnedCode(t, d, tile, "m1", "")
	for _, ev := range tape.since(m) {
		if ev.Component == tile && dlOldTypes[ev.Type] {
			t.Errorf("pausing and editing published %s", ev.raw)
		}
	}

	m = tape.mark()
	_, e = a.op(t, "live-reload/now", tile)
	if e.How != "reload-now" || e.Result != "ok" {
		t.Fatalf("reload now's deploy: %+v", e)
	}
	a.waitServed(t, tile, "go", "m2", 10*time.Second)
	if _, ok := tape.wait(m, isEvent("reload", tile), 10*time.Second); !ok {
		t.Errorf("reload now published no reload: %s", tape.describe(m, tile))
	}
	time.Sleep(3 * latencyDebounce) // negative: nothing more follows
	for _, ev := range tape.since(m) {
		if ev.Component == tile && ev.Type != "reload" && dlOldTypes[ev.Type] {
			t.Errorf("reload now published %s (a checkpoint deploy rides the deployments event only)", ev.raw)
		}
	}
	if n := tape.count(m, isEvent("reload", tile)); n != 1 {
		t.Errorf("reload now published %d reloads, want 1: %s", n, tape.describe(m, tile))
	}
	if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") != e.Checkpoint {
		t.Errorf("after reload now: live reload %q, main %q (shipped %s)", st.LiveReload, st.pinned("main"), e.Checkpoint)
	}

	if ans := a.mustPost(t, "live-reload/resume", dlBody(tile)); ans.State.Record {
		t.Errorf("resuming onto main kept the record: %+v", ans.State)
	}
	writeProbe(t, d.WS, tile, "m3")
	waitProbe(t, d, tile, "m3")
	a.waitServed(t, tile, "go", "m3", 10*time.Second)
}

// covers P9 T11 SC-PINNED — every restart path runs pinned code
// (15-test-plan §5.4): the probe pinned at m1 while its work tree says m3
// answers m1 from its code and its bound tree after a crash restart,
// crash-loop recovery (a save doesn't restart it; a deploy onto its
// checkpoint does), a grant change, an xbind restart, the loss of .xbin
// (build and deploy) and a lifecycle disable/enable; each start but the one
// after .xbin went is the retained artifact, never a build. An alwaysOn
// probe pinned the same way comes back by itself on its checkpoint. Not run
// end to end, with the reason logged: idle reap (a 30-minute constant, seam
// row 18), vault seal/unseal (the test vault is plaintext), a provider nudge
// (needs a net-provider tile; seam row 19 shares its path with the grant
// change).
func TestPinnedThroughRestartPaths(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/pin-paths"
	writeProbe(t, d.WS, tile, "m1")
	waitProbe(t, d, tile, "m1")
	_, e := a.op(t, "live-reload/pause", tile)
	if e.Result != "ok" {
		t.Fatalf("the pause's deploy: %+v", e)
	}
	tree := a.state(t, tile).dep("main").Checkpoint.Hash
	writeProbe(t, d.WS, tile, "m3") // the work tree moves on
	art := filepath.Join(d.WS, ".xbin", "build", util.CompKey(tile), "c", tree, "bin")
	fi, err := os.Stat(art)
	if err != nil {
		t.Fatalf("the checkpoint's retained artifact: %v", err)
	}
	built := fi.ModTime()
	pinnedCode(t, d, tile, "m1", tree)
	retained := func(path string) {
		t.Helper()
		if fi, err := os.Stat(art); err != nil || !fi.ModTime().Equal(built) {
			t.Errorf("%s: the artifact was rebuilt or lost (%v), not started as retained", path, err)
		}
	}

	t.Run("crash restart", func(t *testing.T) {
		pid := backendPID(t, a, tile)
		must(t, syscall.Kill(pid, syscall.SIGKILL))
		if !waitFor(func() bool { v, _, ok := a.served(tile, "go"); return ok && v == "m1" && backendPID(t, a, tile) != pid }, time.Minute) {
			t.Fatalf("the crashed generation never came back")
		}
		pinnedCode(t, d, tile, "m1", tree)
		retained("crash restart")
	})

	t.Run("crash-loop recovery", func(t *testing.T) {
		// Kill each generation a request starts until one doesn't start:
		// crashLimit exits inside the window trip the breaker (the crash
		// restart above counts too).
		for i := 1; ; i++ {
			_, _, ok := a.served(tile, "go")
			pid := backendPID(t, a, tile)
			if !ok && pid == 0 {
				break
			}
			if i > 4 || pid == 0 {
				t.Fatalf("crash %d: the breaker never tripped (answer ok %v, pid %d)", i, ok, pid)
			}
			must(t, syscall.Kill(pid, syscall.SIGKILL))
			if !waitFor(func() bool { return backendPID(t, a, tile) != pid }, 30*time.Second) {
				t.Fatalf("crash %d: pid %d never left the sandbox list", i, pid)
			}
		}
		if _, b := a.do("GET", "/api/xbin/backends", ""); !strings.Contains(b, "crash-looping") {
			t.Errorf("the breaker tripped without saying so: %s", b)
		}
		writeProbe(t, d.WS, tile, "m4")
		time.Sleep(3 * latencyDebounce) // negative: a save doesn't reach a pinned deployment
		if v, _, ok := a.served(tile, "go"); ok || v == "m4" {
			t.Errorf("after the breaker tripped a save restarted it: %q (ok %v)", v, ok)
		}
		ans, de := a.op(t, "deploy", tile, "deployment", "main", "checkpoint", e.Checkpoint)
		if ans.Unchanged || de.How != "restart" || de.Result != "ok" {
			t.Errorf("a deploy of its own checkpoint while crash-looping: unchanged %v, %+v; want a restart from the kept artifact (11-contract §1.6)", ans.Unchanged, de)
			a.op(t, "deploy", tile, "deployment", "main", "restart", true) // clear the breaker for the paths that follow
		}
		pinnedCode(t, d, tile, "m1", tree)
		retained("crash-loop recovery")
	})

	t.Run("grant change", func(t *testing.T) {
		gen := backendGen(t, a, tile)
		// A new resource use in the work tree, then its approval, which
		// restarts the tile's generations (OnGrantChange).
		b, err := os.ReadFile(filepath.Join(d.WS, tile, "xbin.json"))
		must(t, err)
		use := `{"target":"res:` + tile + `-grant/kv","role":"reader"}`
		must(t, saveIfChanged(filepath.Join(d.WS, tile, "xbin.json"), strings.Replace(string(b), `"uses":[`, `"uses":[`+use+`,`, 1)))
		grantChange(t, a, tile)
		if !waitFor(func() bool { a.served(tile, "go"); return backendGen(t, a, tile) > gen }, time.Minute) {
			t.Fatalf("the grant change never restarted the backend (gen %d)", gen)
		}
		pinnedCode(t, d, tile, "m1", tree)
		retained("grant change")
	})

	t.Run("xbind restart", func(t *testing.T) {
		d.restart(t)
		pinnedCode(t, d, tile, "m1", tree)
		retained("xbind restart")
	})

	t.Run("loss of .xbin", func(t *testing.T) {
		d.stop(t)
		for _, rel := range []string{"build", "deploy"} {
			removeTree(filepath.Join(d.WS, ".xbin", rel))
		}
		d.start(t)
		pinnedCode(t, d, tile, "m1", tree) // rebuilt from the checkpoint, never from the work tree
		if !isoExists(art) {
			t.Errorf("the checkpoint's artifact wasn't rebuilt at %s", art)
		}
	})

	t.Run("lifecycle", func(t *testing.T) {
		for _, st := range []string{"disabled", "enabled"} {
			if c, b := a.do("POST", "/api/xbin/lifecycle", `{"component":"`+tile+`","state":"`+st+`"}`); c != 200 {
				t.Fatalf("lifecycle %s: %d %s", st, c, b)
			}
			if st == "disabled" {
				if v, _, ok := a.served(tile, "go"); ok {
					t.Errorf("disabled, it still answers %q", v)
				}
			}
		}
		pinnedCode(t, d, tile, "m1", tree)
	})

	t.Run("alwaysOn backoff", func(t *testing.T) {
		const always = "apps/pin-always"
		must(t, writeRT(d.WS, always, "go", "a1", "", true))
		if !waitFor(func() bool { return backendPID(t, a, always) > 0 }, 3*time.Minute) {
			t.Fatal("the alwaysOn backend never started by itself")
		}
		if _, e := a.op(t, "live-reload/pause", always); e.Result != "ok" {
			t.Fatalf("pausing %s: %+v", always, e)
		}
		a.waitServed(t, always, "go", "a1", time.Minute)
		must(t, writeRT(d.WS, always, "go", "a3", "", true))
		pid := backendPID(t, a, always)
		must(t, syscall.Kill(pid, syscall.SIGKILL))
		// No request: alwaysOn brings it back by itself (afterExit's backoff).
		if !waitFor(func() bool { p := backendPID(t, a, always); return p > 0 && p != pid }, time.Minute) {
			t.Fatal("the alwaysOn backend never came back by itself")
		}
		a.waitServed(t, always, "go", "a1", 10*time.Second)
	})

	t.Log("not run end to end: idle reap (a 30-minute constant with no test knob; seam row 18), vault seal/unseal " +
		"(the test vault is plaintext; seam row 22), a provider nudge (needs a net-provider tile; seam row 19)")
}

// pinnedCode checks that tile's probe runs pinned code: GET /v and GET /file
// answer marker (waiting, bounded, for a restart), and, when tree is given,
// the running generation names that checkpoint (/runtime).
func pinnedCode(t *testing.T, d *isoDaemon, tile, marker, tree string) {
	t.Helper()
	d.dl().waitServed(t, tile, "go", marker, 3*time.Minute)
	if tree == "" {
		return
	}
	if got := runtimeCheckpoint(t, d.dl(), tile); got != tree {
		t.Errorf("/runtime says %s runs %q, want the checkpoint %s", tile, got, tree)
	}
}

// runtimeCheckpoint is the checkpoint tile's running generation runs, as
// /runtime's backend row names it ("" while it runs the work tree).
func runtimeCheckpoint(t *testing.T, a dlAPI, tile string) string {
	t.Helper()
	code, body := a.do("GET", "/api/xbin/runtime", "")
	var rt struct {
		Backends []struct {
			Path       string `json:"path"`
			Checkpoint string `json:"checkpoint"`
		} `json:"backends"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &rt) != nil {
		t.Fatalf("/runtime: %d %.300s", code, body)
	}
	for _, b := range rt.Backends {
		if b.Path == tile {
			return b.Checkpoint
		}
	}
	return ""
}

// backendPID is the pid of tile's live backend generation, from the sandbox
// registry (0 when none is listed).
func backendPID(t *testing.T, a dlAPI, tile string) int {
	t.Helper()
	_, body := a.do("GET", "/api/xbin/sandboxes?tile="+tile, "")
	var out struct {
		Sandboxes []struct {
			Kind string `json:"kind"`
			PID  int    `json:"pid"`
		} `json:"sandboxes"`
	}
	if json.Unmarshal([]byte(body), &out) != nil {
		return 0
	}
	for _, s := range out.Sandboxes {
		if s.Kind == "backend" && s.PID > 0 {
			return s.PID
		}
	}
	return 0
}

// backendGen is tile's current generation number, from /backends.
func backendGen(t *testing.T, a dlAPI, tile string) int {
	t.Helper()
	_, body := a.do("GET", "/api/xbin/backends", "")
	var all map[string]struct {
		Gen int `json:"gen"`
	}
	_ = json.Unmarshal([]byte(body), &all)
	return all[tile].Gen
}

// grantChange approves a resource grant for tile, which restarts its
// generations (the broker's OnGrantChange).
func grantChange(t *testing.T, a dlAPI, tile string) {
	t.Helper()
	body := `{"from":"` + tile + `","target":"res:` + tile + `-grant/kv","role":"reader"}`
	if c, b := a.do("POST", "/api/xbin/grants", body); c != 200 {
		t.Fatalf("POST /grants %s: %d %s", body, c, b)
	}
}
