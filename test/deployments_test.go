//go:build integration

package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Moving code onto main on the isolated daemon (M1): rolling back within
// its bound, and failed deploys that no viewer can see.

// covers SC-ROLLBACK SC-WORKTREE P9 — rolling main back to each of its
// previous three deploy-log entries, on R-go (examples/counter-go) and
// R-static (the agent template's files as a static tile), with the daemon
// restarted without go on its PATH (no build can run) and each work tree
// replaced by a broken one (nothing reads it): each rollback serves the
// rolled-back code, its frames reloaded, within 2 s p95 and 10 s worst for
// the backend and 1 s p95 for the static tile (the median of the samples in
// the fast tier, their p95 with XBIN_TEST_FULL=1); live reload stays
// paused; the data and the tile directories (.git included) are
// byte-identical afterwards; a rollback without a checkpoint takes the
// newest other one in the log. (That a rollback pauses live reload when the
// deployment was its target needs a deployment that follows the work tree
// with a record: M2's.)
func TestRollBackBound(t *testing.T) {
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	type ref struct {
		tile, name  string
		p95, bound  time.Duration
		edit        func(k int)      // the save that makes checkpoint k
		serves      func(k int) bool // what is served is checkpoint k's
		breakTheDir func()           // replaces the work tree by a broken one
		ids         map[int]string   // checkpoint k's id
		samples     []time.Duration
	}
	rStatic := &ref{tile: "apps/rb-static", name: "R-static", p95: time.Second, bound: 10 * time.Second, ids: map[int]string{}}
	rGo := &ref{tile: "apps/rb-go", name: "R-go", p95: 2 * time.Second, bound: 10 * time.Second, ids: map[int]string{}}

	// R-static
	sdir := filepath.Join(d.WS, rStatic.tile)
	copyLatencyTile(t, filepath.Join(repo, "builtin-templates", "agent"), sdir)
	must(t, os.Remove(filepath.Join(sdir, "scope.json")))
	must(t, os.WriteFile(filepath.Join(sdir, "xbin.json"), []byte(`{"runtime":"static"}`+"\n"), 0o644))
	index, err := os.ReadFile(filepath.Join(sdir, "index.html"))
	must(t, err)
	rStatic.edit = func(k int) {
		must(t, saveIfChanged(filepath.Join(sdir, "index.html"), string(index)+fmt.Sprintf("<!-- rb-%d -->\n", k)))
	}
	rStatic.serves = func(k int) bool {
		c, b := a.do("GET", "/c/"+rStatic.tile+"/", "")
		return c == 200 && strings.Contains(b, fmt.Sprintf("<!-- rb-%d -->", k))
	}
	rStatic.breakTheDir = func() {
		must(t, os.WriteFile(filepath.Join(sdir, "index.html"), []byte("BROKEN: not what any checkpoint holds\n"), 0o644))
	}

	// R-go, with a module path of its own
	gdir := filepath.Join(d.WS, rGo.tile)
	copyLatencyTile(t, filepath.Join(repo, "examples", "counter-go"), gdir)
	mod, err := os.ReadFile(filepath.Join(gdir, "go.mod"))
	must(t, err)
	renamed := strings.Replace(string(mod), "module counter\n", "module rbgo\n", 1)
	if renamed == string(mod) {
		t.Fatal("examples/counter-go's go.mod no longer says `module counter`: update this test's rename")
	}
	must(t, os.WriteFile(filepath.Join(gdir, "go.mod"), []byte(renamed), 0o644))
	src, err := os.ReadFile(filepath.Join(gdir, "backend", "main.go"))
	must(t, err)
	const shape = `"count":%d`
	if !bytes.Contains(src, []byte(shape)) {
		t.Fatalf("examples/counter-go's GET /count no longer prints %s: update this test's edit", shape)
	}
	rGo.edit = func(k int) {
		must(t, saveIfChanged(filepath.Join(gdir, "backend", "main.go"),
			strings.Replace(string(src), shape, fmt.Sprintf(`%s,"rb":%d`, shape, k), 1)))
	}
	rGo.serves = func(k int) bool {
		c, b := a.do("GET", "/api/"+rGo.tile+"/count", "")
		return c == 200 && strings.Contains(b, fmt.Sprintf(`"rb":%d,`, k))
	}
	rGo.breakTheDir = func() {
		must(t, os.WriteFile(filepath.Join(gdir, "backend", "main.go"), []byte("package main\n\nthis is not Go\n"), 0o644))
	}

	// Each deploy log: pause (checkpoint 1), then reload now three times
	// (2, 3, 4). Live reload stays paused: in M1 a main-only tile with a
	// record is always paused (resuming onto main opts it out, and a tile
	// without a record has nothing to roll back), so "a rollback pauses live
	// reload when it was the target" waits for M2's deployments.
	for _, r := range []*ref{rStatic, rGo} {
		r.edit(1)
		if !waitFor(func() bool { return r.serves(1) }, 3*time.Minute) {
			t.Fatalf("%s never served its first code", r.name)
		}
		a.waitTile(t, r.tile)
		for k := 1; k <= 4; k++ {
			route := "live-reload/now"
			if k == 1 {
				route = "live-reload/pause"
			} else {
				r.edit(k)
			}
			_, e := a.op(t, route, r.tile)
			if e.Result != "ok" {
				t.Fatalf("%s: %s for checkpoint %d: %+v", r.name, route, k, e)
			}
			r.ids[k] = e.Checkpoint
			if !waitFor(func() bool { return r.serves(k) }, time.Minute) {
				t.Fatalf("%s never served checkpoint %d", r.name, k)
			}
		}
	}

	// Restart without go, the work trees broken.
	d.stop(t)
	rStatic.breakTheDir()
	rGo.breakTheDir()
	var path []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "go")); err != nil {
			path = append(path, dir)
		}
	}
	d.opts.Env = append(d.opts.Env, "PATH="+strings.Join(path, string(filepath.ListSeparator)))
	d.start(t)
	if out, err := exec.Command("sh", "-c", "PATH='"+strings.Join(path, ":")+"' command -v go").CombinedOutput(); err == nil {
		t.Fatalf("go is still on the restarted daemon's PATH: %s", out)
	}
	data := dataHash(t, d.WS)
	trees := map[string]string{rStatic.tile: dirHash(t, sdir), rGo.tile: dirHash(t, gdir)}
	tape := a.tape(t)

	rounds := 1
	if os.Getenv("XBIN_TEST_FULL") == "1" {
		rounds = 10 // 30 samples per reference tile
	}
	for _, r := range []*ref{rStatic, rGo} {
		for round := 0; round < rounds; round++ {
			for _, k := range []int{3, 2, 1} {
				m := tape.mark()
				t0 := time.Now()
				ans := a.mustPost(t, "rollback", dlBody(r.tile, "deployment", "main", "checkpoint", r.ids[k]))
				if ans.State.LiveReload != "" {
					t.Errorf("%s: a rollback of main moved live reload to %q", r.name, ans.State.LiveReload)
				}
				var servedAt time.Time
				for deadline := t0.Add(r.bound + 20*time.Second); ; time.Sleep(latencyPoll) {
					if r.serves(k) {
						servedAt = time.Now()
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("%s: rolled back to checkpoint %d (%s), still not serving it: %s", r.name, k, r.ids[k], tape.describe(m, r.tile))
					}
				}
				ev, ok := tape.wait(m, isEvent("reload", r.tile), r.bound)
				if !ok {
					t.Errorf("%s: the rollback to checkpoint %d reloaded no frame: %s", r.name, k, tape.describe(m, r.tile))
				}
				took := servedAt.Sub(t0)
				if ok && ev.at.After(servedAt) {
					took = ev.at.Sub(t0)
				}
				if e := a.settle(t, r.tile, ans); e.How != "rollback" || e.Result != "ok" {
					t.Errorf("%s: the rollback to checkpoint %d: %+v", r.name, k, e)
				}
				r.samples = append(r.samples, took)
				t.Logf("%s: rollback to checkpoint %d (%s): %v", r.name, k, r.ids[k], took.Round(time.Millisecond))
				if took > r.bound {
					t.Errorf("%s: a rollback took %v, past the %v bound", r.name, took.Round(time.Millisecond), r.bound)
				}
			}
		}
		med, p95, worst := latencyStats(r.samples)
		t.Logf("%s: %d rollbacks — median %v, p95 %v, max %v; target p95 %v", r.name, len(r.samples),
			med.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond), r.p95)
		if got := map[bool]time.Duration{false: med, true: p95}[rounds > 1]; got > r.p95 {
			t.Errorf("%s: rollbacks took %v (%s), past the %v target", r.name, got.Round(time.Millisecond),
				map[bool]string{false: "median", true: "p95"}[rounds > 1], r.p95)
		}
		// Without a checkpoint: the newest "ok" entry with other code than
		// main runs now (checkpoint 1) is the rollback to checkpoint 2.
		if _, e := a.op(t, "rollback", r.tile, "deployment", "main"); e.Checkpoint != r.ids[2] || e.Result != "ok" {
			t.Errorf("%s: a rollback naming no checkpoint: %+v, want checkpoint 2 (%s)", r.name, e, r.ids[2])
		}
	}
	if got := dataHash(t, d.WS); got != data {
		t.Errorf("the rollbacks changed the workspace's data")
	}
	for tile, h := range trees {
		if got := dirHash(t, filepath.Join(d.WS, tile)); got != h {
			t.Errorf("the rollbacks changed %s's directory", tile)
		}
	}
}

// covers SC-SAFE-DEPLOY — fault injection per runtime (go, node, python): a
// broken build (for node and python, a syntax error, which fails at start),
// a crash at start and a health timeout, each shipped by reload now, a
// deploy and a rollback naming the failed checkpoint (promote is M2), with
// the event stream and the served answers watched throughout. The pinned
// code serves every request; no reload, build-* or status event names the
// tile; its reported status stays as it was; the failure reaches the actor:
// the attempt's result and error in the answer and the deploy log, a
// deployments event, and bx's exit 1 with the reason. XBIN_DEPLOY_TAPE names
// a file that receives the recorded event stream, one JSON frame per line,
// for the shipped app's parser (WP-60).
func TestFailedDeployInvisible(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	tape := a.tape(t)
	m0 := tape.mark()
	t.Run("runtimes", func(t *testing.T) {
		for _, rt := range []string{"go", "node", "python"} {
			t.Run(rt, func(t *testing.T) {
				t.Parallel()
				failedDeploys(t, d, tape, "apps/fd-"+rt, rt)
			})
		}
	})
	if out := os.Getenv("XBIN_DEPLOY_TAPE"); out != "" {
		var b strings.Builder
		for _, ev := range tape.since(m0) {
			b.WriteString(ev.raw + "\n")
		}
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Errorf("writing the tape: %v", err)
		}
		t.Logf("the event tape (%d frames) is in %s", len(tape.since(m0)), out)
	}
}

// failedDeploys is TestFailedDeployInvisible for one runtime's tile.
func failedDeploys(t *testing.T, d *isoDaemon, tape *dlTape, tile, rt string) {
	a := d.dl()
	mustRT(t, d.WS, tile, rt, "m1", "")
	a.waitServed(t, tile, rt, "m1", 3*time.Minute)
	if _, e := a.op(t, "live-reload/pause", tile); e.Result != "ok" {
		t.Fatalf("the pause: %+v", e)
	}
	pinned := a.state(t, tile).pinned("main")
	if c, b := a.do("POST", "/api/xbin/tile-report", `{"component":"`+tile+`","level":"warn","message":"watching the deploys"}`); c != 200 {
		t.Fatalf("POST /tile-report: %d %s", c, b)
	}
	status := tileReport(t, a, tile)
	if !strings.Contains(status, "watching the deploys") {
		t.Fatalf("the reported status didn't take: %s", status)
	}
	m := tape.mark()
	stop := observe(a, tile, rt)

	var ids []int64
	failed := func(what string, e dlEntry) {
		t.Helper()
		if e.Result != "failed" || e.Error == "" {
			t.Errorf("%s: %+v, want a failed attempt with its error", what, e)
			return
		}
		ids = append(ids, e.ID)
		t.Logf("%s: %s %d failed: %.200s", what, e.How, e.ID, e.Error)
	}
	for _, fault := range []string{"build", "crash", "hang"} {
		mustRT(t, d.WS, tile, rt, "bad-"+fault, fault)
		_, e := a.op(t, "live-reload/now", tile)
		failed("reload now of a "+fault+" fault", e)
		bad := e.Checkpoint
		_, e = a.op(t, "deploy", tile, "deployment", "main")
		failed("a deploy of a "+fault+" fault", e)
		_, e = a.op(t, "rollback", tile, "deployment", "main", "checkpoint", bad)
		failed("a rollback to a "+fault+" fault", e)
		if rt == "go" && fault == "build" {
			code, out := runBx(t, d, buildBx(t), "", "live-reload", "now", tile, "--yes")
			if code != 1 || !strings.Contains(out, "failed") || !strings.Contains(out, "keeps running") {
				t.Errorf("bx live-reload now of a broken build: exit %d, want 1 naming the failure:\n%s", code, out)
			}
		}
	}
	time.Sleep(3 * latencyDebounce) // negative: nothing trails the last failure
	for _, o := range stop() {
		if !o.ok || o.v != "m1" || o.file != "m1" {
			t.Errorf("during the failed deploys a request answered %q/%q (ok %v), not the pinned m1", o.v, o.file, o.ok)
			break
		}
	}
	for _, ev := range tape.since(m) {
		if ev.Component == tile && dlOldTypes[ev.Type] {
			t.Errorf("a failed deploy published %s", ev.raw)
		}
	}
	for _, id := range ids {
		if _, ok := tape.wait(m, func(ev dlEvent) bool {
			var dd struct {
				Op     string `json:"op"`
				ID     int64  `json:"id"`
				Result string `json:"result"`
			}
			return ev.Component == tile && ev.Type == "deployments" && json.Unmarshal(ev.Data, &dd) == nil &&
				dd.Op == "deploy" && dd.ID == id && dd.Result == "failed"
		}, 10*time.Second); !ok {
			t.Errorf("deploy %d's failure reached no deployments event: %s", id, tape.describe(m, tile))
		}
	}
	for _, e := range a.log(t, tile) {
		if slices.Contains(ids, e.ID) && (e.Result != "failed" || e.Error == "") {
			t.Errorf("the deploy log's entry %d: %+v", e.ID, e)
		}
	}
	if got := tileReport(t, a, tile); got != status {
		t.Errorf("the failed deploys changed the reported status: %s, was %s", got, status)
	}
	if st := a.state(t, tile); st.pinned("main") != pinned || st.LiveReload != "" {
		t.Errorf("after the failed deploys main is pinned to %q (was %s), live reload %q", st.pinned("main"), pinned, st.LiveReload)
	}
}

// tileReport is tile's reported status (GET /tile-report), verbatim.
func tileReport(t *testing.T, a dlAPI, tile string) string {
	t.Helper()
	code, body := a.do("GET", "/api/xbin/tile-report", "")
	var out struct {
		Statuses map[string]json.RawMessage `json:"statuses"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("GET /tile-report: %d %s", code, body)
	}
	return string(out.Statuses[tile])
}

// dataHash hashes the workspace's data/ except the deployment stores, which
// deploys write by design (the record, the deploy log, the checkpoints).
func dataHash(t *testing.T, ws string) string {
	t.Helper()
	root := filepath.Join(ws, "data")
	return treeHash(t, root, func(rel string) bool {
		return rel == "deployments" || rel == "checkpoints"
	})
}

// dirHash hashes a tile directory, .git included.
func dirHash(t *testing.T, dir string) string {
	t.Helper()
	return treeHash(t, dir, func(string) bool { return false })
}

// treeHash hashes every path, mode, link target and file under root, minus
// the directories skip names (root-relative).
func treeHash(t *testing.T, root string, skip func(rel string) bool) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if e.IsDir() && rel != "." && skip(rel) {
			return fs.SkipDir
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %v\n", rel, info.Mode())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			fmt.Fprintf(h, "-> %s\n", target)
		case e.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
