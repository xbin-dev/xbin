//go:build integration

package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Moving code on the isolated daemon: rolling back within its bound (M1
// for main, M2 for every deployment) and failed deploys that no viewer can
// see (M1); and tile deployments' M2 rows (15-test-plan §5.3): data
// separation, promotion and rollback, dormant start-time registrations,
// self-calls, and a multi-tile scope.

// covers SC-ROLLBACK SC-WORKTREE P9 — rolling every deployment back to each
// of its previous three deploy-log entries, on R-go (examples/counter-go)
// and R-static (the agent template's files as a static tile), with the
// daemon restarted without go on its PATH (no build can run) and each work
// tree replaced by a broken one (nothing reads it): each rollback serves
// the rolled-back code, its frames reloaded, within 2 s p95 and 10 s worst
// for the backend and 1 s p95 for the static tile (the median of the
// samples in the fast tier, their p95 with XBIN_TEST_FULL=1); the data and
// the tile directories (.git included) are byte-identical afterwards; a
// rollback without a checkpoint takes the newest other one in the log.
// main (M1): a tile of each runtime pauses live reload and reloads now
// three times; rolling main back leaves live reload paused. dev (M2): on
// a tile of each runtime of its own, main is paused, dev is added pinned
// and deployed twice, then live reload is resumed onto dev, which follows
// the (unmoved) work tree; the first rollback of dev pauses live reload,
// since dev was its target (lastLiveReload dev), and dev's frames reload
// through the deployments event (op reload), never a bare reload.
func TestRollBackBound(t *testing.T) {
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	type ref struct {
		tile, dep, name string
		p95, bound      time.Duration
		dir             string
		edit            func(k int)                  // the save that makes checkpoint k
		servesAt        func(dep string, k int) bool // what deployment dep serves is checkpoint k's
		breakTheDir     func()                       // replaces the work tree by a broken one
		ids             map[int]string               // checkpoint k's id
		samples         []time.Duration
	}
	// staticRef and goRef set up a tile of each runtime; dep is the
	// deployment whose rollbacks are measured, whose URLs carry it
	// unless it is main.
	staticRef := func(tile, dep string) *ref {
		r := &ref{tile: tile, dep: dep, name: "R-static " + dep, p95: time.Second, bound: 10 * time.Second, ids: map[int]string{}}
		r.dir = filepath.Join(d.WS, tile)
		copyLatencyTile(t, filepath.Join(repo, "builtin-templates", "agent"), r.dir)
		must(t, os.Remove(filepath.Join(r.dir, "scope.json")))
		must(t, os.WriteFile(filepath.Join(r.dir, "xbin.json"), []byte(`{"runtime":"static"}`+"\n"), 0o644))
		index, err := os.ReadFile(filepath.Join(r.dir, "index.html"))
		must(t, err)
		r.edit = func(k int) {
			must(t, saveIfChanged(filepath.Join(r.dir, "index.html"), string(index)+fmt.Sprintf("<!-- rb-%d -->\n", k)))
		}
		r.servesAt = func(dep string, k int) bool {
			c, b := a.do("GET", "/c/"+depRef(r.tile, dep)+"/", "")
			return c == 200 && strings.Contains(b, fmt.Sprintf("<!-- rb-%d -->", k))
		}
		r.breakTheDir = func() {
			must(t, os.WriteFile(filepath.Join(r.dir, "index.html"), []byte("BROKEN: not what any checkpoint holds\n"), 0o644))
		}
		return r
	}
	goRef := func(tile, dep, module string) *ref {
		r := &ref{tile: tile, dep: dep, name: "R-go " + dep, p95: 2 * time.Second, bound: 10 * time.Second, ids: map[int]string{}}
		r.dir = filepath.Join(d.WS, tile)
		copyLatencyTile(t, filepath.Join(repo, "examples", "counter-go"), r.dir) // with a module path of its own
		mod, err := os.ReadFile(filepath.Join(r.dir, "go.mod"))
		must(t, err)
		renamed := strings.Replace(string(mod), "module counter\n", "module "+module+"\n", 1)
		if renamed == string(mod) {
			t.Fatal("examples/counter-go's go.mod no longer says `module counter`: update this test's rename")
		}
		must(t, os.WriteFile(filepath.Join(r.dir, "go.mod"), []byte(renamed), 0o644))
		src, err := os.ReadFile(filepath.Join(r.dir, "backend", "main.go"))
		must(t, err)
		const shape = `"count":%d`
		if !bytes.Contains(src, []byte(shape)) {
			t.Fatalf("examples/counter-go's GET /count no longer prints %s: update this test's edit", shape)
		}
		r.edit = func(k int) {
			must(t, saveIfChanged(filepath.Join(r.dir, "backend", "main.go"),
				strings.Replace(string(src), shape, fmt.Sprintf(`%s,"rb":%d`, shape, k), 1)))
		}
		r.servesAt = func(dep string, k int) bool {
			c, b := a.do("GET", "/api/"+depRef(r.tile, dep)+"/count", "")
			return c == 200 && strings.Contains(b, fmt.Sprintf(`"rb":%d,`, k))
		}
		r.breakTheDir = func() {
			must(t, os.WriteFile(filepath.Join(r.dir, "backend", "main.go"), []byte("package main\n\nthis is not Go\n"), 0o644))
		}
		return r
	}
	mains := []*ref{staticRef("apps/rb-static", "main"), goRef("apps/rb-go", "main", "rbgo")}
	devs := []*ref{staticRef("apps/rb-static-dev", "dev"), goRef("apps/rb-go-dev", "dev", "rbgodev")}
	ok := func(r *ref, what string, e dlEntry) string {
		t.Helper()
		if e.Result != "ok" {
			t.Fatalf("%s: %s: %+v", r.name, what, e)
		}
		return e.Checkpoint
	}
	serves := func(r *ref, k int) bool { return r.servesAt(r.dep, k) }
	first := func(r *ref) {
		t.Helper()
		r.edit(1)
		if !waitFor(func() bool { return r.servesAt("main", 1) }, 3*time.Minute) {
			t.Fatalf("%s never served its first code", r.name)
		}
		a.waitTile(t, r.tile)
	}

	// main's deploy log: pause (checkpoint 1), then reload now three times
	// (2, 3, 4). Live reload stays paused: a main-only tile with a record
	// is always paused (resuming onto main opts it out).
	for _, r := range mains {
		first(r)
		for k := 1; k <= 4; k++ {
			route := "live-reload/now"
			if k == 1 {
				route = "live-reload/pause"
			} else {
				r.edit(k)
			}
			_, e := a.op(t, route, r.tile)
			r.ids[k] = ok(r, fmt.Sprintf("%s for checkpoint %d", route, k), e)
			if !waitFor(func() bool { return serves(r, k) }, time.Minute) {
				t.Fatalf("%s never served checkpoint %d", r.name, k)
			}
		}
	}
	// dev's: main paused on checkpoint 1; dev added pinned to a fresh
	// checkpoint of the same work tree (1), deployed 2 and 3; then live
	// reload resumed onto dev, which follows the work tree still at 3.
	for _, r := range devs {
		first(r)
		ok(r, "the pause of main", func() dlEntry { _, e := a.op(t, "live-reload/pause", r.tile); return e }())
		_, e := a.op(t, "add", r.tile, "deployment", "dev")
		r.ids[1] = ok(r, "adding dev", e)
		for k := 2; k <= 3; k++ {
			r.edit(k)
			_, e := a.op(t, "deploy", r.tile, "deployment", "dev")
			r.ids[k] = ok(r, fmt.Sprintf("the deploy of checkpoint %d to dev", k), e)
			if !waitFor(func() bool { return serves(r, k) }, 3*time.Minute) {
				t.Fatalf("%s never served checkpoint %d", r.name, k)
			}
		}
		ans, e := a.op(t, "live-reload/resume", r.tile, "deployment", "dev")
		ok(r, "resuming live reload onto dev", e)
		if ans.State.LiveReload != "dev" || ans.State.pinned("dev") != "" || ans.State.pinned("main") == "" {
			t.Fatalf("%s: after resuming onto dev: %+v", r.name, ans.State)
		}
		if !waitFor(func() bool { return serves(r, 3) }, 3*time.Minute) {
			t.Fatalf("%s: dev following the work tree never served it", r.name)
		}
	}
	refs := append(slices.Clone(mains), devs...)

	// Restart without go, the work trees broken.
	d.stop(t)
	for _, r := range refs {
		r.breakTheDir()
	}
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
	trees := map[string]string{}
	for _, r := range refs {
		trees[r.tile] = dirHash(t, r.dir)
	}
	tape := a.tape(t)

	rounds := 1
	if os.Getenv("XBIN_TEST_FULL") == "1" {
		rounds = 10 // 30 samples per reference tile
	}
	for _, r := range refs {
		// A swap reloads main's frames with a bare reload, dev's with the
		// deployments event's op reload (C2).
		reloaded := isEvent("reload", r.tile)
		if r.dep != "main" {
			reloaded = func(ev dlEvent) bool {
				var dd struct{ Deployment string }
				return ev.Type == "deployments" && ev.Component == r.tile && ev.op() == "reload" &&
					json.Unmarshal(ev.Data, &dd) == nil && dd.Deployment == r.dep
			}
		}
		for round := 0; round < rounds; round++ {
			for _, k := range []int{3, 2, 1} {
				m := tape.mark()
				t0 := time.Now()
				ans := a.mustPost(t, "rollback", dlBody(r.tile, "deployment", r.dep, "checkpoint", r.ids[k]))
				switch {
				case r.dep == "main" && ans.State.LiveReload != "":
					t.Errorf("%s: a rollback of main moved live reload to %q", r.name, ans.State.LiveReload)
				case r.dep != "main" && (ans.State.LiveReload != "" || ans.State.LastLiveReload != r.dep):
					t.Errorf("%s: after a rollback of %s, live reload %q (last %q); want paused, last %s",
						r.name, r.dep, ans.State.LiveReload, ans.State.LastLiveReload, r.dep)
				}
				var servedAt time.Time
				for deadline := t0.Add(r.bound + 20*time.Second); ; time.Sleep(latencyPoll) {
					if serves(r, k) {
						servedAt = time.Now()
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("%s: rolled back to checkpoint %d (%s), still not serving it: %s", r.name, k, r.ids[k], tape.describe(m, r.tile))
					}
				}
				ev, ok := tape.wait(m, reloaded, r.bound)
				if !ok {
					t.Errorf("%s: the rollback to checkpoint %d reloaded no frame: %s", r.name, k, tape.describe(m, r.tile))
				}
				took := servedAt.Sub(t0)
				if ok && ev.at.After(servedAt) {
					took = ev.at.Sub(t0)
				}
				if e := a.settle(t, r.tile, ans); e.How != "rollback" || e.Result != "ok" || e.Deployment != r.dep {
					t.Errorf("%s: the rollback to checkpoint %d: %+v", r.name, k, e)
				}
				r.samples = append(r.samples, took)
				t.Logf("%s: rollback to checkpoint %d (%s): %v", r.name, k, r.ids[k], took.Round(time.Millisecond))
				if took > r.bound {
					t.Errorf("%s: a rollback took %v, past the %v bound", r.name, took.Round(time.Millisecond), r.bound)
				}
			}
		}
		if r.dep != "main" {
			for _, ev := range tape.since(0) {
				if ev.Component == r.tile && dlOldTypes[ev.Type] {
					t.Errorf("%s: a rollback of %s published %s", r.name, r.dep, ev.raw)
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
		// what runs now (checkpoint 1) is the rollback to checkpoint 2.
		if _, e := a.op(t, "rollback", r.tile, "deployment", r.dep); e.Checkpoint != r.ids[2] || e.Result != "ok" {
			t.Errorf("%s: a rollback naming no checkpoint: %+v, want checkpoint 2 (%s)", r.name, e, r.ids[2])
		}
	}
	if got := dataHash(t, d.WS); got != data {
		t.Errorf("the rollbacks changed the workspace's data")
	}
	for _, r := range refs {
		if got := dirHash(t, r.dir); got != trees[r.tile] {
			t.Errorf("the rollbacks changed %s's directory", r.tile)
		}
	}
}

// depRef is the tile ref of deployment dep of tile: the bare tile for main
// (the primary here), <tile>+<dep> otherwise.
func depRef(tile, dep string) string {
	if dep == "main" {
		return tile
	}
	return tile + "+" + dep
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
	m := tape.mark()
	if c, b := a.do("POST", "/api/xbin/tile-report", `{"component":"`+tile+`","level":"warn","message":"watching the deploys"}`); c != 200 {
		t.Fatalf("POST /tile-report: %d %s", c, b)
	}
	status := tileReport(t, a, tile)
	if !strings.Contains(status, "watching the deploys") {
		t.Fatalf("the reported status didn't take: %s", status)
	}
	// The report's own status event may reach the tape after the POST
	// answered: wait for it, so the watch below starts after it.
	if _, ok := tape.wait(m, func(ev dlEvent) bool {
		return ev.Component == tile && ev.Type == "status" && strings.Contains(string(ev.Data), "watching the deploys")
	}, 30*time.Second); !ok {
		t.Fatalf("the reported status published no status event")
	}
	m = tape.mark()
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

// ---- M2: tile deployments on the isolated daemon ----

// dataProbe describes the data probe (writeDataProbe): the probe of
// 15-test-plan §5.2 grown for the M2 rows, which exercise every resource
// kind through every addressing path, register at start, and call other
// tiles. examples/ is never edited for this.
type dataProbe struct {
	Marker    string
	Resources map[string]string // the scope.json it roots (name → type); nil: no scope.json of its own
	Uses      []string          // the targets it uses, each at writer ("res:<scope>/<name>" or a tile path)
	AtStart   bool              // at every start: a cron job and a bus subscription registered, a notification sent
	Instances bool              // provides inst (http, instances) and registers instance a at /i/<marker> at every start
}

// writeDataProbe writes (or turns into p) the data probe at ws/tile:
// backend/main.go with p.Marker compiled in, probeFile holding it, a go.mod
// requiring the sdk under a module path of the tile's own, the scope.json
// p.Resources declares (none when nil), and xbin.json using p.Uses, the
// manifest last. Only changed files are written, each saved as an editor
// saves.
//
// The probe serves, besides GET /v and GET /env (every XBIN_RES_*,
// XBIN_COMPONENT and XBIN_DEPLOYMENT):
//   - GET|PUT|DELETE /kv?res=&k=: xbind's kv API for resource res (k ""
//     lists); GET|PUT /blob?res=&k=: the blob API (k "" lists). res is an
//     id, "env:<name>" (XBIN_RES_<NAME>, what the manifest delivered) or
//     "self:<name>" (res:$XBIN_COMPONENT/<name>, hand-built from Self());
//   - GET|PUT /fs?p=: a file at p, read or written in the sandbox; p is a
//     path or "env:<name>[/<rel>]" beneath XBIN_RES_<NAME>; &ls=1 lists a
//     directory;
//   - POST /sql?p=&q=: q run on the sqlite database at p (the same forms)
//     by the rootfs's python3, its rows as JSON;
//   - POST /publish?res=&topic=: a bus publish of the body;
//   - PUT /cron?res=&name=&schedule=: a cron job of its own calling POST
//     /tick; PUT /sub?res=&name=&prefix=: a bus push subscription calling
//     POST /on;
//   - GET|PUT /secret?k=: its own vault's key (PUT's body is the value);
//   - POST /notify?user=&title=: POST /notify;
//   - POST /call {method, url, body}: any xbind or tile URL through the
//     gateway, answered as {status, body, deployment} (deployment: the
//     answer's X-XBin-Deployment);
//   - PUT /guarded?res=&k=: a kv write behind xbin.RoleFunc("writer");
//   - GET /caller, POST <any other path> (a delivery, recorded) and GET
//     /seen, as the probe does; GET /boot: what the start-time
//     registrations answered (cron, sub, notify; iface for Instances).
//
// Every relayed call answers xbind's status and body verbatim; a transport
// failure is 502 with the error.
func writeDataProbe(t *testing.T, ws, tile string, p dataProbe) {
	t.Helper()
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	mod := "dprobe/" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-._~", r) {
			return r
		}
		return '_'
	}, tile)
	var uses []string
	for _, u := range p.Uses {
		uses = append(uses, `{"target":"`+u+`","role":"writer"}`)
	}
	files := [][2]string{{"go.mod", "module " + mod + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"}}
	if p.Resources != nil {
		b, _ := json.Marshal(map[string]any{"resources": func() map[string]any {
			m := map[string]any{}
			for name, typ := range p.Resources {
				m[name] = map[string]string{"type": typ}
			}
			return m
		}()})
		files = append(files, [2]string{"scope.json", string(b) + "\n"})
	}
	provides := ""
	if p.Instances {
		provides = `,"expose":{"roles":{"reader":"read the probe","writer":"change the probe"}}` +
			`,"provides":{"inst":{"kind":"http","service":"inst","role":"reader","instances":true}}`
	}
	files = append(files,
		[2]string{"backend/main.go", strings.NewReplacer("__MARKER__", strconv.Quote(p.Marker),
			"__ATSTART__", strconv.FormatBool(p.AtStart), "__INSTANCES__", strconv.FormatBool(p.Instances)).Replace(dataProbeSource)},
		[2]string{probeFile, p.Marker},
		[2]string{"xbin.json", `{"runtime":"go","uses":[` + strings.Join(uses, ",") + `]` + provides + `}` + "\n"})
	for _, f := range files {
		must(t, saveIfChanged(filepath.Join(dir, filepath.FromSlash(f[0])), f[1]))
	}
}

// dataProbeSource is the data probe's backend/main.go (writeDataProbe).
const dataProbeSource = `package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const marker, atStart, instances = __MARKER__, __ATSTART__, __INSTANCES__

var (
	mu   sync.Mutex
	seen = []map[string]string{}
	boot = map[string]string{}
)

func res(q string) string {
	switch {
	case strings.HasPrefix(q, "env:"):
		return xbin.Resource(q[4:])
	case strings.HasPrefix(q, "self:"):
		return "res:" + xbin.Self() + "/" + q[5:]
	}
	return q
}

func fsPath(q string) string {
	if !strings.HasPrefix(q, "env:") {
		return q
	}
	name, rel, _ := strings.Cut(q[4:], "/")
	base := xbin.Resource(name)
	if base == "" || rel == "" {
		return base
	}
	return filepath.Join(base, rel)
}

// call sends one request through the gateway: its status, body and
// X-XBin-Deployment, or 502 and the transport error.
func call(method, url string, body []byte) (int, []byte, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://xbin"+url, rd)
	if err != nil {
		return 502, []byte(err.Error()), ""
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return 502, []byte("probe: " + err.Error()), ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header.Get("X-XBin-Deployment")
}

func relay(w http.ResponseWriter, method, url string, body []byte) {
	code, b, _ := call(method, url, body)
	w.WriteHeader(code)
	w.Write(b)
}

func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }

func register() map[string]string {
	out := map[string]string{}
	if instances {
		code, b, _ := call("PUT", "/api/xbin/iface-instances", jsonBody(map[string]any{"instances": map[string]string{"a": "/i/" + marker}}))
		out["iface"] = http.StatusText(code) + " " + string(b)
	}
	if !atStart {
		return out
	}
	code, b, _ := call("PUT", "/api/xbin/cron/jobs", jsonBody(map[string]string{"name": "boot-tick",
		"resource": xbin.Resource("cron"), "schedule": "@every 1s", "path": "/tick", "role": "writer"}))
	out["cron"] = http.StatusText(code) + " " + string(b)
	code, b, _ = call("PUT", "/api/xbin/bus/subscriptions", jsonBody(map[string]string{"name": "boot-sub",
		"resource": xbin.Resource("bus"), "prefix": "", "path": "/on"}))
	out["sub"] = http.StatusText(code) + " " + string(b)
	code, b, _ = call("POST", "/api/xbin/notify", jsonBody(map[string]string{"user": "owner", "title": "started " + marker}))
	out["notify"] = http.StatusText(code) + " " + string(b)
	return out
}

func main() {
	if atStart || instances { // the SDK's documented pattern: register at every start
		go func() {
			for i := 0; i < 50; i++ {
				got := register()
				mu.Lock()
				boot = got
				mu.Unlock()
				ok := func(k string) bool { return strings.HasPrefix(got[k], "OK") }
				if (!atStart || ok("cron") && ok("sub")) && (!instances || ok("iface")) {
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, marker) })
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		env := map[string]string{}
		for _, e := range os.Environ() {
			k, v, _ := strings.Cut(e, "=")
			if strings.HasPrefix(k, "XBIN_RES_") || k == "XBIN_DEPLOYMENT" || k == "XBIN_COMPONENT" {
				env[k] = v
			}
		}
		xbin.WriteJSON(w, 200, env)
	})
	kv := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u := "/api/xbin/kv/" + res(q.Get("res")) + "/" + q.Get("k")
		if q.Get("k") == "" {
			u += "?prefix="
		}
		b, _ := io.ReadAll(r.Body)
		relay(w, r.Method, u, b)
	}
	for _, m := range []string{"GET", "PUT", "DELETE"} { // methods named: POST / records deliveries
		mux.HandleFunc(m+" /kv", kv)
	}
	mux.Handle("PUT /guarded", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		relay(w, "PUT", "/api/xbin/kv/"+res(q.Get("res"))+"/"+q.Get("k"), b)
	}))
	blob := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		relay(w, r.Method, "/api/xbin/blob/"+res(q.Get("res"))+"/"+q.Get("k"), b)
	}
	mux.HandleFunc("GET /blob", blob)
	mux.HandleFunc("PUT /blob", blob)
	fsH := func(w http.ResponseWriter, r *http.Request) {
		p := fsPath(r.URL.Query().Get("p"))
		if p == "" {
			xbin.WriteError(w, 400, "no such path")
			return
		}
		switch {
		case r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				xbin.WriteError(w, 500, err.Error())
				return
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				xbin.WriteError(w, 500, err.Error())
				return
			}
			xbin.WriteJSON(w, 200, map[string]bool{"ok": true})
		case r.URL.Query().Get("ls") != "":
			es, err := os.ReadDir(p)
			if err != nil {
				xbin.WriteError(w, 404, err.Error())
				return
			}
			names := []string{}
			for _, e := range es {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			xbin.WriteJSON(w, 200, names)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				xbin.WriteError(w, 404, err.Error())
				return
			}
			w.Write(b)
		}
	}
	mux.HandleFunc("GET /fs", fsH)
	mux.HandleFunc("PUT /fs", fsH)
	mux.HandleFunc("POST /sql", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		py := "import json, sqlite3, sys\nc = sqlite3.connect(sys.argv[1])\nrows = c.execute(sys.argv[2]).fetchall()\nc.commit()\nprint(json.dumps(rows))\n"
		out, err := exec.Command("python3", "-c", py, fsPath(q.Get("p")), q.Get("q")).CombinedOutput()
		if err != nil {
			xbin.WriteError(w, 500, err.Error()+": "+string(out))
			return
		}
		w.Write(bytes.TrimSpace(out))
	})
	mux.HandleFunc("POST /publish", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		relay(w, "POST", "/api/xbin/bus/publish", jsonBody(map[string]any{"resource": res(q.Get("res")),
			"topic": q.Get("topic"), "data": json.RawMessage(b)}))
	})
	mux.HandleFunc("PUT /cron", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		relay(w, "PUT", "/api/xbin/cron/jobs", jsonBody(map[string]string{"name": q.Get("name"),
			"resource": res(q.Get("res")), "schedule": q.Get("schedule"), "path": "/tick", "role": "writer"}))
	})
	mux.HandleFunc("PUT /sub", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		relay(w, "PUT", "/api/xbin/bus/subscriptions", jsonBody(map[string]string{"name": q.Get("name"),
			"resource": res(q.Get("res")), "prefix": q.Get("prefix"), "path": "/on"}))
	})
	secret := func(w http.ResponseWriter, r *http.Request) {
		u := "/api/xbin/vault/" + xbin.Self() + "/" + r.URL.Query().Get("k")
		if r.Method == "PUT" {
			b, _ := io.ReadAll(r.Body)
			relay(w, "PUT", u, jsonBody(map[string]string{"value": string(b)}))
			return
		}
		code, b, _ := call("GET", u, nil)
		if code == 200 {
			var v struct{ Value string }
			if json.Unmarshal(b, &v) == nil {
				io.WriteString(w, v.Value)
				return
			}
		}
		w.WriteHeader(code)
		w.Write(b)
	}
	mux.HandleFunc("GET /secret", secret)
	mux.HandleFunc("PUT /secret", secret)
	mux.HandleFunc("POST /notify", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		relay(w, "POST", "/api/xbin/notify", jsonBody(map[string]string{"user": q.Get("user"), "title": q.Get("title")}))
	})
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, r *http.Request) {
		var c struct{ Method, URL, Body string }
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			xbin.WriteError(w, 400, err.Error())
			return
		}
		var body []byte
		if c.Body != "" {
			body = []byte(c.Body)
		}
		code, b, dep := call(c.Method, c.URL, body)
		xbin.WriteJSON(w, 200, map[string]any{"status": code, "body": string(b), "deployment": dep})
	})
	mux.HandleFunc("GET /caller", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		xbin.WriteJSON(w, 200, map[string]any{"from": c.From, "role": c.Role, "user": c.User,
			"deployment": r.Header.Get("X-XBin-Deployment"), "marker": marker})
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		d := map[string]string{"path": r.URL.Path, "from": c.From, "role": c.Role}
		var ev xbin.BusEvent
		if b, _ := io.ReadAll(r.Body); json.Unmarshal(b, &ev) == nil && ev.Subscription != "" {
			d["subscription"], d["topic"], d["data"] = ev.Subscription, ev.Topic, string(ev.Data)
		}
		mu.Lock()
		seen = append(seen, d)
		mu.Unlock()
		xbin.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		xbin.WriteJSON(w, 200, seen)
	})
	mux.HandleFunc("GET /boot", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		xbin.WriteJSON(w, 200, boot)
	})
	xbin.Serve(mux)
}
`

// dlDepM2 is a Deployment's M2 facts in the full view (11-contract §1.1).
type dlDepM2 struct {
	Name, URL, API string
	Primary        bool
	LiveReload     bool
	Checkpoint     *dlCheckpoint
	Data           *struct {
		State, From, Busy string
		Reset             bool
	}
	Vault *struct {
		Keys, Placeholders int
	}
	Deliveries    bool
	Registrations []struct {
		Kind, Name string
		Dormant    bool
	}
	WouldNotify []struct {
		To, Title string
	}
	Status struct {
		State, Serving string
		Gen            int
	}
}

// depM2 reads deployment name of tile from the full view; it fails the
// test when the state lists no such deployment.
func (a dlAPI) depM2(t *testing.T, tile, name string) dlDepM2 {
	t.Helper()
	code, body := a.do("GET", "/api/xbin/deployments?tile="+url.QueryEscape(tile), "")
	var st struct{ Deployments []dlDepM2 }
	if code != 200 || json.Unmarshal([]byte(body), &st) != nil {
		t.Fatalf("GET /deployments?tile=%s: %d %s", tile, code, body)
	}
	for _, d := range st.Deployments {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("%s has no deployment %q: %s", tile, name, body)
	return dlDepM2{}
}

// api sends one request to ref's backend (/api/<ref>/<path>), where ref is
// a tile or a tile ref naming a deployment (<tile>+<name>).
func (a dlAPI) api(method, ref, path, body string) (int, string) {
	return a.do(method, "/api/"+ref+"/"+strings.TrimPrefix(path, "/"), body)
}

// waitAPI waits (bounded by max) until ref's GET <path> answers 200 with
// want, and fails with the last answer otherwise.
func (a dlAPI) waitAPI(t *testing.T, ref, path, want string, max time.Duration) {
	t.Helper()
	var code int
	var body string
	if !waitFor(func() bool {
		code, body = a.api("GET", ref, path, "")
		return code == 200 && body == want
	}, max) {
		t.Fatalf("%s's %s never answered %q: %d %.300s", ref, path, want, code, body)
	}
}

// probeCall is a data probe's POST /call: what ref's backend got when it
// called u through the gateway.
type probeCall struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	Deployment string `json:"deployment"`
}

// call has ref's data probe call u (method, body) through the gateway.
func (a dlAPI) call(t *testing.T, ref, method, u, body string) probeCall {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"method": method, "url": u, "body": body})
	code, out := a.api("POST", ref, "/call", string(b))
	var c probeCall
	if code != 200 || json.Unmarshal([]byte(out), &c) != nil {
		t.Fatalf("%s's call of %s %s: %d %s", ref, method, u, code, out)
	}
	return c
}

// probeSeen is a data probe's GET /seen: the deliveries it recorded.
func (a dlAPI) probeSeen(t *testing.T, ref string) []map[string]string {
	t.Helper()
	code, body := a.api("GET", ref, "/seen", "")
	var seen []map[string]string
	if code != 200 || json.Unmarshal([]byte(body), &seen) != nil {
		t.Fatalf("%s's /seen: %d %s", ref, code, body)
	}
	return seen
}

// seenWhere counts the deliveries whose fields hold every pair of want.
func seenWhere(seen []map[string]string, want map[string]string) int {
	n := 0
outer:
	for _, d := range seen {
		for k, v := range want {
			if d[k] != v {
				continue outer
			}
		}
		n++
	}
	return n
}

// primaryDataHash hashes the workspace's data/ as the primary's deployment
// data: everything but the deployment stores (the record, the checkpoints,
// per-deployment registration files) and the namespaces and vaults beyond
// main, which non-primary deployments write by design.
func primaryDataHash(t *testing.T, ws string) string {
	t.Helper()
	return treeHash(t, filepath.Join(ws, "data"), func(rel string) bool {
		switch rel {
		case "deployments", "checkpoints", "resources-enc/.deployments", "vault/.deployments":
			return true
		}
		return false
	})
}

// fileBytes is the file at p ("" when it doesn't exist).
func fileBytes(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// gocryptfsAvailable reports whether the isolated daemon gets a gocryptfs
// (XBIN_GOCRYPTFS, the repo's bin/, or PATH): file-backed resources need it.
func gocryptfsAvailable() bool {
	if p := os.Getenv("XBIN_GOCRYPTFS"); p != "" {
		return isoExists(p)
	}
	if isoExists(filepath.Join(repo, "bin", "gocryptfs")) {
		return true
	}
	_, err := exec.LookPath("gocryptfs")
	return err == nil
}

// covers P12 T3 — self-calls stay inside the caller's deployment, through
// the gateway, on the probe: dev's call to its own bare /api/<self>/ is
// answered by dev (its code, and the answer names dev), as is its call to
// <self>+dev; its call to <self>+main is refused 403. main's bare call and
// its <self>+main alias answer main; its call to <self>+dev is refused. The
// callee of a dev self-call sees X-XBin-Deployment: dev and the bare tile
// path as the caller; main's self-calls carry no deployment. The standard
// probe's /self answers the same.
func TestSelfCallsStayInDeployment(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/self"
	writeDataProbe(t, d.WS, tile, dataProbe{Marker: "m1"})
	a.waitAPI(t, tile, "/v", "m1", 3*time.Minute)
	a.waitTile(t, tile)
	if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev: %+v", e)
	}
	writeDataProbe(t, d.WS, tile, dataProbe{Marker: "m2"})
	a.waitAPI(t, tile+"+dev", "/v", "m2", 3*time.Minute)
	if c, b := a.api("GET", tile, "/v", ""); c != 200 || b != "m1" {
		t.Fatalf("the bare URL: %d %q, want main's pinned m1", c, b)
	}

	for _, row := range []struct {
		from, target  string
		status        int
		body, answDep string
	}{
		{tile + "+dev", tile, 200, "m2", "dev"},
		{tile + "+dev", tile + "+dev", 200, "m2", "dev"},
		{tile + "+dev", tile + "+main", 403, "", ""},
		{tile, tile, 200, "m1", ""},
		{tile, tile + "+main", 200, "m1", ""},
		{tile, tile + "+dev", 403, "", ""},
	} {
		c := a.call(t, row.from, "GET", "/api/"+row.target+"/v", "")
		t.Logf("%s → /api/%s/v: %d %.80q (answered by %q)", row.from, row.target, c.Status, c.Body, c.Deployment)
		if c.Status != row.status || row.status == 200 && (c.Body != row.body || c.Deployment != row.answDep) {
			t.Errorf("%s → /api/%s/v: %d %q (answered by %q), want %d %q (answered by %q)",
				row.from, row.target, c.Status, c.Body, c.Deployment, row.status, row.body, row.answDep)
		}
		if row.status == 403 && strings.Contains(c.Body, "m1") || strings.Contains(c.Body, "m2") && row.status != 200 {
			t.Errorf("%s → /api/%s/v: the refusal carries code: %q", row.from, row.target, c.Body)
		}
	}
	var who struct{ From, Role, Deployment, Marker string }
	for _, row := range []struct{ from, dep, marker string }{{tile + "+dev", "dev", "m2"}, {tile, "", "m1"}} {
		c := a.call(t, row.from, "GET", "/api/"+tile+"/caller", "")
		if c.Status != 200 || json.Unmarshal([]byte(c.Body), &who) != nil {
			t.Fatalf("%s's self-call of /caller: %+v", row.from, c)
		}
		if who.From != tile || who.Deployment != row.dep || who.Marker != row.marker {
			t.Errorf("%s's self-call reached %+v, want from %s, deployment %q, served by %s", row.from, who, tile, row.dep, row.marker)
		}
	}

	// The standard probe's /self (15-test-plan §5.2), on a tile of its own.
	const std = "apps/self-std"
	writeProbe(t, d.WS, std, "m1")
	waitProbe(t, d, std, "m1")
	a.waitTile(t, std)
	if _, e := a.op(t, "add", std, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev to %s: %+v", std, e)
	}
	writeProbe(t, d.WS, std, "m2")
	a.waitAPI(t, std+"+dev", "/v", "m2", 3*time.Minute)
	var self struct {
		Self, Main         int
		SelfBody, MainBody string
	}
	if c, b := a.api("GET", std+"+dev", "/self", ""); c != 200 || json.Unmarshal([]byte(b), &self) != nil {
		t.Fatalf("dev's /self: %d %s", c, b)
	}
	if self.Self != 200 || self.SelfBody != "m2" || self.Main != 403 {
		t.Errorf("dev's /self: %+v, want its own /api/<self>/ 200 from dev (m2) and +main 403", self)
	}
	if c, b := a.api("GET", std, "/self", ""); c != 200 || json.Unmarshal([]byte(b), &self) != nil {
		t.Fatalf("main's /self: %d %s", c, b)
	}
	if self.Self != 200 || self.SelfBody != "m1" || self.Main != 200 || self.MainBody != "m1" {
		t.Errorf("main's /self: %+v, want both from main (m1)", self)
	}
}

// doH is do with the answer's headers.
func (a dlAPI) doH(method, path string) (int, string, http.Header) {
	rq, err := http.NewRequest(method, a.url+path, nil)
	if err != nil {
		return 0, err.Error(), nil
	}
	if a.token != "" {
		rq.Header.Set("Authorization", "Bearer "+a.token)
	}
	r, err := isoClient.Do(rq)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b), r.Header
}

// covers P10 SC-WORKTREE SC-AUDIT — flows B and D on the probe: main holds
// its own kv data and a vault key; dev is added with live reload attached
// (main pinned) and runs the next save, and writes kv of its own. The diff
// from main to dev shows the change and names the capture it reviewed;
// promoting dev → main with that capture as expect moves exactly that code
// onto main (/v), while main's kv, vault and every other store of main's
// data, and the tile directory (.git included), stay byte-identical, dev
// keeps its data and keeps following the work tree, and live reload stays
// on dev. A promote naming a capture the work tree has since moved past is
// refused 409. Rolling main back (no checkpoint named) takes main's pinned
// code of before the promotion, data untouched again. The deploy log holds
// main's pin at the add, the promotion (from dev, the reviewed capture) and
// the rollback, each ok and attributed.
func TestPromoteAndRollBack(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/promote"
	dir := filepath.Join(d.WS, tile)
	writeProbe(t, d.WS, tile, "m1")
	waitProbe(t, d, tile, "m1")
	a.waitTile(t, tile)
	if c, b := a.api("PUT", tile, "/kv/greeting", "main-data"); c != 200 {
		t.Fatalf("main's kv write: %d %s", c, b)
	}
	if c, b := a.do("PUT", "/api/xbin/vault/"+tile+"/API_KEY", `{"value":"main-secret"}`); c != 200 {
		t.Fatalf("main's vault key: %d %s", c, b)
	}

	// Flow B: add dev, live reload attached; main is pinned (c1).
	ans, add := a.op(t, "add", tile, "deployment", "dev", "attach", true)
	c1 := ans.State.pinned("main")
	if add.Result != "ok" || ans.State.LiveReload != "dev" || c1 == "" {
		t.Fatalf("adding dev: %+v, state %+v", add, ans.State)
	}
	writeProbe(t, d.WS, tile, "m2")
	a.waitAPI(t, tile+"+dev", "/v", "m2", 3*time.Minute)
	if c, b := a.api("PUT", tile+"+dev", "/kv/greeting", "dev-data"); c != 200 {
		t.Fatalf("dev's kv write: %d %s", c, b)
	}
	if c, b := a.api("GET", tile, "/kv/greeting", ""); c != 200 || b != "main-data" {
		t.Fatalf("main's kv after dev's write: %d %q", c, b)
	}
	data, tree := primaryDataHash(t, d.WS), dirHash(t, dir)

	// Review: the diff from main to dev (the work tree), and its capture.
	c, patch, hdr := a.doH("GET", "/api/xbin/deployments/diff?tile="+tile+"&from=deployment:main&to=deployment:dev")
	reviewed := hdr.Get("X-XBin-Checkpoint-To")
	if c != 200 || !strings.Contains(patch, `+const marker = "m2"`) || hdr.Get("X-XBin-Checkpoint-From") != c1 || !strings.HasPrefix(reviewed, "c:") {
		t.Fatalf("the diff main → dev: %d, from %q to %q:\n%.600s", c, hdr.Get("X-XBin-Checkpoint-From"), reviewed, patch)
	}

	// A review the work tree has moved past is refused, and changes nothing.
	must(t, saveIfChanged(filepath.Join(dir, "notes.txt"), "a later save\n"))
	if !waitFor(func() bool {
		code, _, _ := a.post(t, "promote", dlBody(tile, "from", "dev", "to", "main", "expect", reviewed, "dryRun", true))
		return code == 409
	}, 30*time.Second) {
		code, _, raw := a.post(t, "promote", dlBody(tile, "from", "dev", "to", "main", "expect", reviewed, "dryRun", true))
		t.Fatalf("a promote of a stale review: %d %s, want 409", code, raw)
	}
	must(t, os.Remove(filepath.Join(dir, "notes.txt")))
	tree = dirHash(t, dir)
	if !waitFor(func() bool {
		code, _, _ := a.post(t, "promote", dlBody(tile, "from", "dev", "to", "main", "expect", reviewed, "dryRun", true))
		return code == 200
	}, 30*time.Second) {
		t.Fatal("the work tree back at the reviewed capture, a promote's dry run still refuses it")
	}

	// Promote dev → main: code only.
	ans, prom := a.op(t, "promote", tile, "from", "dev", "to", "main", "expect", reviewed)
	if prom.Result != "ok" || prom.How != "promote" || prom.Deployment != "main" || prom.Checkpoint != reviewed || prom.Previous != c1 {
		t.Fatalf("the promotion: %+v, want main moved from %s to the reviewed %s", prom, c1, reviewed)
	}
	a.waitAPI(t, tile, "/v", "m2", time.Minute)
	if c, b := a.api("GET", tile, "/kv/greeting", ""); c != 200 || b != "main-data" {
		t.Errorf("after the promotion main's kv reads %d %q: data moved with the code", c, b)
	}
	if c, b := a.api("GET", tile+"+dev", "/kv/greeting", ""); c != 200 || b != "dev-data" {
		t.Errorf("after the promotion dev's kv reads %d %q", c, b)
	}
	if st := a.state(t, tile); st.LiveReload != "dev" || st.pinned("main") != reviewed || st.pinned("dev") != "" {
		t.Errorf("after the promotion: live reload %q, main %q, dev %q; want dev following the work tree, main at %s",
			st.LiveReload, st.pinned("main"), st.pinned("dev"), reviewed)
	}
	if v := a.depM2(t, tile, "dev").Vault; v == nil || v.Keys != 1 || v.Placeholders != 1 {
		t.Errorf("dev's vault after the promotion: %+v, want API_KEY a placeholder still", v)
	}
	if got := primaryDataHash(t, d.WS); got != data {
		t.Errorf("the promotion changed main's data or vault")
	}
	if got := dirHash(t, dir); got != tree {
		t.Errorf("the promotion changed the tile directory")
	}

	// Flow D: roll main back; no checkpoint named takes main's code of
	// before the promotion.
	_, rb := a.op(t, "rollback", tile, "deployment", "main")
	if rb.Result != "ok" || rb.How != "rollback" || rb.Checkpoint != c1 || rb.Previous != reviewed {
		t.Fatalf("the rollback: %+v, want main back from %s to %s", rb, reviewed, c1)
	}
	a.waitAPI(t, tile, "/v", "m1", time.Minute)
	if c, b := a.api("GET", tile, "/kv/greeting", ""); c != 200 || b != "main-data" {
		t.Errorf("after the rollback main's kv reads %d %q", c, b)
	}
	if c, b := a.api("GET", tile+"+dev", "/v", ""); c != 200 || b != "m2" {
		t.Errorf("after main's rollback dev serves %d %q, want m2", c, b)
	}
	if st := a.state(t, tile); st.LiveReload != "dev" || st.pinned("main") != c1 {
		t.Errorf("after the rollback: live reload %q, main %q", st.LiveReload, st.pinned("main"))
	}
	if got := primaryDataHash(t, d.WS); got != data {
		t.Errorf("the rollback changed main's data or vault")
	}
	if got := dirHash(t, dir); got != tree {
		t.Errorf("the rollback changed the tile directory")
	}

	// The deploy log: main's pin at the add, the promotion and the
	// rollback, oldest first in time, each ok and attributed.
	var pin, promoted, rolled *dlEntry
	log := a.log(t, tile)
	for i := range log {
		e := &log[i]
		switch {
		case e.Deployment != "main":
		case e.ID == prom.ID:
			promoted = e
		case e.ID == rb.ID:
			rolled = e
		case e.Checkpoint == c1 && e.ID < prom.ID:
			pin = e
		}
	}
	for what, e := range map[string]*dlEntry{"main's pin at the add": pin, "the promotion": promoted, "the rollback": rolled} {
		if e == nil || e.Result != "ok" || e.By != "owner" {
			t.Errorf("the deploy log's entry for %s: %+v (log: %+v)", what, e, log)
		}
	}
	if promoted != nil && (promoted.How != "promote" || promoted.Checkpoint != reviewed) {
		t.Errorf("the log's promotion: %+v", promoted)
	}
	code, body := a.do("GET", "/api/xbin/deployments/log?tile="+tile+"&deployment=main", "")
	if code != 200 || !strings.Contains(body, `"from":"dev"`) {
		t.Errorf("main's deploy log doesn't say the promotion came from dev: %d %.400s", code, body)
	}
}

// covers SC-DORMANT P13 T6 — a probe that registers a cron job (every
// second) and a bus push subscription at every start, and sends its user a
// notification, the SDK's documented pattern, runs on main and on dev
// (added with live reload attached, then saved to m2): main's job ticks
// main and a publish in main's namespace reaches main's subscription;
// dev's registrations are accepted active for dev (P13, revised
// 2026-09-28): answered and listed on dev's state without dormant, dev's
// deliveries on by default; dev's job ticks dev, and dev's own publish
// (dev's namespace) reaches dev's subscription, never main's, while
// main's publish never reaches dev's; main's registration stores
// (data/cron-jobs.json, data/bus-subscriptions.json) are byte-identical
// after dev starts; dev's notification is suppressed (never pushed) and
// listed as would-notify, reaching the event stream only as a deployments
// op notify. A manager switching dev's deliveries off silences it: its
// registrations are listed dormant, no tick and no bus delivery reach dev;
// switched back on, its job ticks dev again; main's stores stay untouched.
// Interface instances stay dormant: TestRouteRegistrationsAtStartStayDormant.
func TestRegistrationsAtStartActiveOnDeployment(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/active-regs"
	probe := dataProbe{Marker: "m1", AtStart: true,
		Resources: map[string]string{"bus": "bus", "cron": "cron"},
		Uses:      []string{"res:" + tile + "/bus", "res:" + tile + "/cron"}}
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile, "/v", "m1", 3*time.Minute)
	a.waitTile(t, tile)
	tape := a.tape(t)
	publish := func(topic string) {
		t.Helper()
		if c, b := a.do("POST", "/api/xbin/bus/publish", `{"resource":"res:`+tile+`/bus","topic":"`+topic+`","data":1}`); c != 200 {
			t.Fatalf("publishing %s: %d %s", topic, c, b)
		}
	}
	devPublish := func(topic string) {
		t.Helper()
		if c, b := a.api("POST", tile+"+dev", "/publish?res=env:bus&topic="+topic, "2"); c != 200 {
			t.Fatalf("dev's own publish %s: %d %s", topic, c, b)
		}
	}
	devTicks := func() int {
		return seenWhere(a.probeSeen(t, tile+"+dev"), map[string]string{"path": "/tick", "from": "xbin/cron"})
	}
	regsOf := func() (map[string]bool, dlDepM2) {
		dev := a.depM2(t, tile, "dev")
		regs := map[string]bool{}
		for _, r := range dev.Registrations {
			regs[r.Kind+"/"+r.Name] = r.Dormant
		}
		return regs, dev
	}
	// main's start-time registrations work: its job ticks, its
	// subscription delivers.
	if !waitFor(func() bool {
		return seenWhere(a.probeSeen(t, tile), map[string]string{"path": "/tick", "from": "xbin/cron"}) > 0
	}, time.Minute) {
		t.Fatalf("main's start-time cron job never ticked main: %+v", a.probeSeen(t, tile))
	}
	if !waitFor(func() bool {
		publish("before")
		return seenWhere(a.probeSeen(t, tile), map[string]string{"path": "/on", "subscription": "boot-sub", "topic": "before"}) > 0
	}, time.Minute) {
		t.Fatalf("main's start-time subscription never delivered: %+v", a.probeSeen(t, tile))
	}
	cronFile, subsFile := filepath.Join(d.WS, "data", "cron-jobs.json"), filepath.Join(d.WS, "data", "bus-subscriptions.json")
	cronBefore, subsBefore := fileBytes(t, cronFile), fileBytes(t, subsFile)
	if !strings.Contains(cronBefore, "boot-tick") || !strings.Contains(subsBefore, "boot-sub") {
		t.Fatalf("main's stores don't hold its registrations:\n%s\n%s", cronBefore, subsBefore)
	}
	var boot map[string]string
	if c, b := a.api("GET", tile, "/boot", ""); c != 200 || json.Unmarshal([]byte(b), &boot) != nil || strings.Contains(boot["notify"], "suppressed") {
		t.Fatalf("main's start-time answers: %d %s", c, b)
	}

	// dev starts (on its first request) and registers the same way.
	if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev: %+v", e)
	}
	m := tape.mark()
	probe.Marker = "m2"
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile+"+dev", "/v", "m2", 3*time.Minute)
	if !waitFor(func() bool {
		_, b := a.api("GET", tile+"+dev", "/boot", "")
		return json.Unmarshal([]byte(b), &boot) == nil && strings.HasPrefix(boot["cron"], "OK") && strings.HasPrefix(boot["sub"], "OK")
	}, time.Minute) {
		t.Fatalf("dev's start-time registrations: %+v", boot)
	}
	for _, k := range []string{"cron", "sub"} {
		if strings.Contains(boot[k], `"dormant"`) {
			t.Errorf("dev's %s registration was answered dormant: %s", k, boot[k])
		}
	}
	if !strings.Contains(boot["notify"], `"suppressed":true`) {
		t.Errorf("dev's notification wasn't suppressed: %s", boot["notify"])
	}
	regs, dev := regsOf()
	for _, k := range []string{"cron/boot-tick", "bus/boot-sub"} {
		if dorm, ok := regs[k]; !ok || dorm {
			t.Errorf("dev's state doesn't list its %s active: %+v", k, dev.Registrations)
		}
	}
	if !dev.Deliveries {
		t.Errorf("dev's deliveries are off by default")
	}
	held := false
	for _, n := range dev.WouldNotify {
		held = held || n.Title == "started m2"
	}
	if !held {
		t.Errorf("dev's would-notify list lacks its notification: %+v", dev.WouldNotify)
	}
	if _, ok := tape.wait(m, func(ev dlEvent) bool {
		return ev.Type == "deployments" && ev.Component == tile && ev.op() == "notify" && strings.Contains(string(ev.Data), `"deployment":"dev"`)
	}, 10*time.Second); !ok {
		t.Errorf("dev's held notification published no deployments op notify: %s", tape.describe(m, tile))
	}

	// Active for dev: its job ticks dev; its own publish reaches its own
	// subscription, never main's; main's publish never reaches dev's.
	if !waitFor(func() bool { return devTicks() > 0 }, time.Minute) {
		t.Errorf("dev's start-time job never ticked dev: %+v", a.probeSeen(t, tile+"+dev"))
	}
	if !waitFor(func() bool {
		devPublish("from-dev")
		return seenWhere(a.probeSeen(t, tile+"+dev"), map[string]string{"path": "/on", "subscription": "boot-sub", "topic": "from-dev"}) > 0
	}, time.Minute) {
		t.Errorf("dev's own publish never reached dev's subscription: %+v", a.probeSeen(t, tile+"+dev"))
	}
	publish("main-only")
	if !waitFor(func() bool {
		return seenWhere(a.probeSeen(t, tile), map[string]string{"path": "/on", "topic": "main-only"}) > 0
	}, 30*time.Second) {
		t.Errorf("main's subscription missed a publish in its namespace: %+v", a.probeSeen(t, tile))
	}
	time.Sleep(2 * time.Second) // negative: the namespaces are apart
	if n := seenWhere(a.probeSeen(t, tile), map[string]string{"topic": "from-dev"}); n != 0 {
		t.Errorf("dev's publish reached main's subscription %d times", n)
	}
	if n := seenWhere(a.probeSeen(t, tile+"+dev"), map[string]string{"topic": "main-only"}); n != 0 {
		t.Errorf("main's publish reached dev's subscription %d times", n)
	}
	for _, ev := range tape.since(m) {
		if ev.Type == "notify" || ev.Component == tile && dlOldTypes[ev.Type] && strings.Contains(ev.raw, "m2") {
			t.Errorf("a non-primary activity rode an old event type: %s", ev.raw)
		}
	}
	if got := fileBytes(t, cronFile); got != cronBefore {
		t.Errorf("dev's start rewrote main's cron store:\n%s\nwas\n%s", got, cronBefore)
	}
	if got := fileBytes(t, subsFile); got != subsBefore {
		t.Errorf("dev's start rewrote main's subscription store:\n%s\nwas\n%s", got, subsBefore)
	}

	// Deliveries off (a manager's act): dev is silenced, its registrations
	// listed dormant.
	if ans := a.mustPost(t, "deliveries", dlBody(tile, "deployment", "dev", "on", false)); ans.State.Tile != tile {
		t.Fatalf("the deliveries switch: %+v", ans)
	}
	regs, dev = regsOf()
	if dev.Deliveries || !regs["cron/boot-tick"] || !regs["bus/boot-sub"] {
		t.Errorf("with deliveries off dev's state: deliveries %v, registrations %+v", dev.Deliveries, dev.Registrations)
	}
	time.Sleep(1500 * time.Millisecond) // a tick in flight at the switch lands
	ticks, seen0 := devTicks(), len(a.probeSeen(t, tile+"+dev"))
	devPublish("while-off")
	time.Sleep(4 * time.Second) // negative: four of dev's every-second ticks would have fired
	if n := devTicks(); n != ticks {
		t.Errorf("with its deliveries off dev's job ticked %d times", n-ticks)
	}
	if seen := a.probeSeen(t, tile+"+dev"); len(seen) != seen0 {
		t.Errorf("dev got deliveries while its deliveries are off: %+v", seen[seen0:])
	}

	// Back on: dev's own job ticks dev again.
	a.mustPost(t, "deliveries", dlBody(tile, "deployment", "dev", "on", true))
	if !waitFor(func() bool { return devTicks() > ticks }, 30*time.Second) {
		t.Errorf("with deliveries back on, dev's job never ticked dev: %+v", a.probeSeen(t, tile+"+dev"))
	}
	if got := fileBytes(t, cronFile); got != cronBefore {
		t.Errorf("dev's deliveries rewrote main's cron store")
	}
	if got := fileBytes(t, subsFile); got != subsBefore {
		t.Errorf("dev's deliveries rewrote main's subscription store")
	}
}

// covers SC-DORMANT P13 P7 — a probe that registers instance a of its
// instances-capable provide at every start (the SDK's documented pattern)
// runs on main and on dev (added with live reload attached, then saved to
// r2): main's registration is answered active and listed active; dev's is
// accepted (the SDK keeps working), answered dormant and listed dormant on
// dev's state, because routes reach the primary only (P7); dev's
// deliveries switch, off or on, never activates it; the root xbin.json
// keeps main's instance table alone. Ingress hosts follow the same rule
// (TestDormantRegistrationsRouting); cron jobs and bus subscriptions don't
// (TestRegistrationsAtStartActiveOnDeployment).
func TestRouteRegistrationsAtStartStayDormant(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/dormant-routes"
	probe := dataProbe{Marker: "r1", Instances: true}
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile, "/v", "r1", 3*time.Minute)
	a.waitTile(t, tile)
	bootOf := func(ref string) string {
		t.Helper()
		var got map[string]string
		if !waitFor(func() bool {
			got = nil
			_, b := a.api("GET", ref, "/boot", "")
			return json.Unmarshal([]byte(b), &got) == nil && strings.HasPrefix(got["iface"], "OK")
		}, time.Minute) {
			t.Fatalf("%s's start-time instance registration: %+v", ref, got)
		}
		return got["iface"]
	}
	ifaceOf := func(dep string) (dormant, listed bool) {
		for _, r := range a.depM2(t, tile, dep).Registrations {
			if r.Kind == "iface-instance" && r.Name == "a" {
				return r.Dormant, true
			}
		}
		return false, false
	}
	if b := bootOf(tile); strings.Contains(b, `"dormant"`) {
		t.Errorf("main's instance was answered dormant: %s", b)
	}

	if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev: %+v", e)
	}
	probe.Marker = "r2"
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile+"+dev", "/v", "r2", 3*time.Minute)
	if b := bootOf(tile + "+dev"); !strings.Contains(b, `"dormant":true`) {
		t.Errorf("dev's instance wasn't answered dormant: %s", b)
	}
	if dormant, listed := ifaceOf("main"); !listed || dormant {
		t.Errorf("main's instance: listed %v, dormant %v, want listed active", listed, dormant)
	}
	for _, on := range []bool{true, false, true} {
		a.mustPost(t, "deliveries", dlBody(tile, "deployment", "dev", "on", on))
		if dormant, listed := ifaceOf("dev"); !listed || !dormant {
			t.Errorf("with deliveries %v dev's instance: listed %v, dormant %v, want listed dormant", on, listed, dormant)
		}
	}
	if ws := fileBytes(t, filepath.Join(d.WS, "xbin.json")); !strings.Contains(ws, `"/i/r1"`) || strings.Contains(ws, `"/i/r2"`) {
		t.Errorf("the root xbin.json's instance table isn't main's alone:\n%s", ws)
	}
}

// covers P6 P14 P22 T8 SC-DATA — a dev data probe exercises every resource
// kind (kv, blob, filesystem, sqlite, bus, cron) through every addressing
// path it has (XBIN_RES_* as delivered, the canonical res: id, the id
// hand-built from Self(); for the file-backed kinds the delivered path) and
// reads every vault key, with main's data and vault hashed before and
// after:
//   - the env: every XBIN_RES_* value both declare is equal in main and
//     dev; dev alone has XBIN_DEPLOYMENT=dev;
//   - before a seed dev sees empty namespaces and placeholder vault keys;
//     its writes land in its own namespace and vault; its cron jobs and bus
//     subscriptions are accepted active for dev (P13, revised): its
//     publishes reach its own subscriptions, never main's, and main's
//     never reach dev's; main still reads its own data, and main's data
//     and vault are byte-identical;
//   - a resource declared only in dev's checkpoint exists only in dev's
//     namespace: main can neither write nor read it (P22);
//   - after a seed (a manager's act) dev sees the copy of main's data, the
//     dev-only resource starts empty, and main is unchanged by the seed and
//     by dev's writes that follow; a vault copy gives dev a value, main's
//     vault untouched;
//   - a reset (vault too) empties dev's namespace and vault.
//
// File-backed kinds need gocryptfs (and an encrypted vault: the daemon
// gets XBIN_VAULT_PASSPHRASE); without it they are left out, as the log
// says.
func TestDataSeparation(t *testing.T) {
	t.Parallel()
	files := gocryptfsAvailable()
	d := startIsolatedDaemon(t, isoOpts{Env: []string{"XBIN_VAULT_PASSPHRASE=itest-data-separation"}})
	releaseMountsAtEnd(t, d)
	a := d.dl()
	const tile = "apps/ds"
	dev := tile + "+dev"
	resources := map[string]string{"kv": "kv", "bus": "bus", "cron": "cron"}
	if files {
		resources["files"], resources["store"], resources["db"] = "blob", "filesystem", "sqlite"
	} else {
		t.Log("no gocryptfs: the file-backed kinds (blob, filesystem, sqlite) are left out")
	}
	var uses []string
	for name := range resources {
		uses = append(uses, "res:"+tile+"/"+name)
	}
	slices.Sort(uses)
	probe := dataProbe{Marker: "m1", Resources: resources, Uses: uses}
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile, "/v", "m1", 3*time.Minute)
	a.waitTile(t, tile)

	q := url.QueryEscape
	ids := func(name string) []string { return []string{"env:" + name, "res:" + tile + "/" + name, "self:" + name} }
	kv := func(ref, method, res, k, v string) (int, string) {
		return a.api(method, ref, "/kv?res="+q(res)+"&k="+q(k), v)
	}
	blob := func(ref, method, res, k, v string) (int, string) {
		return a.api(method, ref, "/blob?res="+q(res)+"&k="+q(k), v)
	}
	mustOK := func(what string, code int, body string) {
		t.Helper()
		if code != 200 {
			t.Fatalf("%s: %d %s", what, code, body)
		}
	}
	// reads answers ref's reads of every kind (retrying while a backend
	// restarts after a data act): kv key k through every path, the blob
	// a.txt, the file f.txt, the sqlite rows, and the vault keys.
	type view struct{ kv, blob, fs, sql, apiKey, dbPass string }
	reads := func(ref, k string) view {
		t.Helper()
		var v view
		var parts []string
		ok := waitFor(func() bool {
			parts = parts[:0]
			for _, id := range ids("kv") {
				c, b := kv(ref, "GET", id, k, "")
				if c != 200 && c != 404 {
					return false
				}
				parts = append(parts, fmt.Sprint(c, ":", b))
			}
			return true
		}, 2*time.Minute)
		if !ok {
			t.Fatalf("%s's kv reads never answered: %v", ref, parts)
		}
		if parts[0] != parts[1] || parts[1] != parts[2] {
			t.Errorf("%s reads kv %s differently by path (env, canonical, hand-built): %v", ref, k, parts)
		}
		v.kv = parts[0]
		if files {
			var bl []string
			for _, id := range ids("files") {
				c, b := blob(ref, "GET", id, "a.txt", "")
				bl = append(bl, fmt.Sprint(c, ":", b))
			}
			if bl[0] != bl[1] || bl[1] != bl[2] {
				t.Errorf("%s reads blob a.txt differently by path: %v", ref, bl)
			}
			v.blob = bl[0]
			c, b := a.api("GET", ref, "/fs?p="+q("env:store/f.txt"), "")
			v.fs = fmt.Sprint(c, ":", b)
			if c != 200 {
				v.fs = fmt.Sprint(c)
			}
			c, b = a.api("POST", ref, "/sql?p="+q("env:db")+"&q="+q("select v from t order by rowid"), "")
			v.sql = b
			if c != 200 {
				v.sql = "no table"
				if !strings.Contains(b, "no such table") {
					v.sql = fmt.Sprint(c, ":", b)
				}
			}
		}
		for _, s := range []struct {
			k   string
			out *string
		}{{"API_KEY", &v.apiKey}, {"DB_PASS", &v.dbPass}} {
			c, b := a.api("GET", ref, "/secret?k="+s.k, "")
			*s.out = fmt.Sprint(c, ":", b)
			if c == 404 && strings.Contains(b, "has no value in deployment") {
				*s.out = "placeholder"
			}
		}
		t.Logf("%s reads %+v", ref, v)
		return v
	}
	env := func(ref string) map[string]string {
		t.Helper()
		c, b := a.api("GET", ref, "/env", "")
		var m map[string]string
		if c != 200 || json.Unmarshal([]byte(b), &m) != nil {
			t.Fatalf("%s's /env: %d %s", ref, c, b)
		}
		return m
	}

	// main's data: every kind, and two vault keys.
	for i, id := range ids("kv") {
		code, body := kv(tile, "PUT", id, fmt.Sprintf("main-%d", i), fmt.Sprintf("main-kv-%d", i))
		mustOK("main's kv write through "+id, code, body)
	}
	code, body := kv(tile, "PUT", "env:kv", "shared", "main-shared")
	mustOK("main's kv write", code, body)
	if files {
		code, body = blob(tile, "PUT", "env:files", "a.txt", "main-blob")
		mustOK("main's blob write", code, body)
		code, body = a.api("PUT", tile, "/fs?p="+q("env:store/f.txt"), "main-fs")
		mustOK("main's file write", code, body)
		for _, stmt := range []string{"create table t (v text)", "insert into t values ('main-sql')"} {
			code, body = a.api("POST", tile, "/sql?p="+q("env:db")+"&q="+q(stmt), "")
			mustOK("main's sqlite "+stmt, code, body)
		}
	}
	code, body = a.api("PUT", tile, "/cron?res=env:cron&name=main-job&schedule="+q("@every 1h"), "")
	mustOK("main's cron job", code, body)
	code, body = a.api("PUT", tile, "/sub?res=env:bus&name=main-sub&prefix=", "")
	mustOK("main's subscription", code, body)
	for k, v := range map[string]string{"API_KEY": "main-secret", "DB_PASS": "main-pass"} {
		code, body = a.do("PUT", "/api/xbin/vault/"+tile+"/"+k, `{"value":"`+v+`"}`)
		mustOK("main's vault key "+k, code, body)
	}
	mainView := reads(tile, "shared")
	want := view{kv: "200:main-shared", apiKey: "200:main-secret", dbPass: "200:main-pass"}
	if files {
		want.blob, want.fs, want.sql = "200:main-blob", "200:main-fs", `[["main-sql"]]`
	}
	if mainView != want {
		t.Fatalf("main's own reads: %+v, want %+v", mainView, want)
	}
	mainEnv := env(tile)
	primary := primaryDataHash(t, d.WS)

	// dev: live reload attached, then pinned to a checkpoint whose code
	// (m2) declares one more resource, "extra", which main's doesn't.
	ans, e := a.op(t, "add", tile, "deployment", "dev", "attach", true)
	if e.Result != "ok" || ans.State.pinned("main") == "" {
		t.Fatalf("adding dev: %+v", e)
	}
	probe.Marker = "m2"
	probe.Resources = maps.Clone(resources)
	probe.Resources["extra"] = "kv"
	probe.Uses = append(slices.Clone(uses), "res:"+tile+"/extra")
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, dev, "/v", "m2", 3*time.Minute)
	if _, e := a.op(t, "deploy", tile, "deployment", "dev"); e.Result != "ok" {
		t.Fatalf("pinning dev: %+v", e)
	}
	if st := a.state(t, tile); st.pinned("dev") == "" || st.LiveReload != "" {
		t.Fatalf("dev isn't pinned: %+v", st)
	}
	a.waitAPI(t, dev, "/v", "m2", 3*time.Minute)
	dd := a.depM2(t, tile, "dev")
	if dd.Data == nil || dd.Data.State != "empty" || dd.Vault == nil || dd.Vault.Keys != 2 || dd.Vault.Placeholders != 2 {
		t.Errorf("dev's data and vault after the add: %+v %+v, want empty and two placeholders", dd.Data, dd.Vault)
	}

	// The env: equal values for everything both declare.
	devEnv := env(dev)
	for k, v := range mainEnv {
		if devEnv[k] != v {
			t.Errorf("/env %s: dev %q, main %q", k, devEnv[k], v)
		}
	}
	if devEnv["XBIN_DEPLOYMENT"] != "dev" || mainEnv["XBIN_DEPLOYMENT"] != "" {
		t.Errorf("XBIN_DEPLOYMENT: dev %q, main %q", devEnv["XBIN_DEPLOYMENT"], mainEnv["XBIN_DEPLOYMENT"])
	}
	if devEnv["XBIN_RES_EXTRA"] == "" || mainEnv["XBIN_RES_EXTRA"] != "" {
		t.Errorf("XBIN_RES_EXTRA: dev %q, main %q; want dev's alone", devEnv["XBIN_RES_EXTRA"], mainEnv["XBIN_RES_EXTRA"])
	}

	// Before a seed: dev's namespace is empty and its vault placeholders.
	empty := view{kv: "404", apiKey: "placeholder", dbPass: "placeholder"}
	if files {
		empty.blob, empty.fs, empty.sql = "404", "404", "no table"
	}
	got := reads(dev, "shared")
	if strings.HasPrefix(got.kv, "404") {
		got.kv = "404"
	}
	if strings.HasPrefix(got.blob, "404") {
		got.blob = "404"
	}
	if got != empty {
		t.Errorf("dev before a seed: %+v, want %+v", got, empty)
	}
	for _, id := range ids("kv") {
		if c, b := kv(dev, "GET", id, "", ""); c != 200 || strings.Contains(b, "main-") {
			t.Errorf("dev's kv listing through %s before a seed: %d %s", id, c, b)
		}
	}

	// dev writes every kind, through every path.
	for i, id := range ids("kv") {
		code, body := kv(dev, "PUT", id, fmt.Sprintf("dev-%d", i), fmt.Sprintf("dev-kv-%d", i))
		mustOK("dev's kv write through "+id, code, body)
	}
	for i := range ids("kv") { // each key through every other path
		for _, id := range ids("kv") {
			if c, b := kv(dev, "GET", id, fmt.Sprintf("dev-%d", i), ""); c != 200 || b != fmt.Sprintf("dev-kv-%d", i) {
				t.Errorf("dev reads dev-%d through %s: %d %q", i, id, c, b)
			}
		}
	}
	code, body = kv(dev, "PUT", "env:kv", "shared", "dev-shared")
	mustOK("dev's kv write", code, body)
	if files {
		for i, id := range ids("files") {
			code, body = blob(dev, "PUT", id, fmt.Sprintf("dev-%d.txt", i), "dev-blob")
			mustOK("dev's blob write through "+id, code, body)
		}
		code, body = blob(dev, "PUT", "env:files", "a.txt", "dev-blob")
		mustOK("dev's blob write", code, body)
		code, body = a.api("PUT", dev, "/fs?p="+q("env:store/f.txt"), "dev-fs")
		mustOK("dev's file write", code, body)
		for _, stmt := range []string{"create table t (v text)", "insert into t values ('dev-sql')"} {
			code, body = a.api("POST", dev, "/sql?p="+q("env:db")+"&q="+q(stmt), "")
			mustOK("dev's sqlite "+stmt, code, body)
		}
	}
	for i, id := range ids("cron") {
		code, body = a.api("PUT", dev, fmt.Sprintf("/cron?res=%s&name=dev-job-%d&schedule=%s", q(id), i, q("@every 1h")), "")
		if code != 200 || strings.Contains(body, `"dormant"`) {
			t.Errorf("dev's cron job through %s: %d %s, want accepted active", id, code, body)
		}
	}
	for i, id := range ids("bus") {
		code, body = a.api("PUT", dev, fmt.Sprintf("/sub?res=%s&name=dev-sub-%d&prefix=", q(id), i), "")
		if code != 200 || strings.Contains(body, `"dormant"`) {
			t.Errorf("dev's subscription through %s: %d %s, want accepted active", id, code, body)
		}
		code, body = a.api("POST", dev, fmt.Sprintf("/publish?res=%s&topic=dev-%d", q(id), i), `"x"`)
		mustOK("dev's publish through "+id, code, body)
	}
	code, body = a.api("POST", tile, "/publish?res=env:bus&topic=main-control", `"x"`)
	mustOK("main's publish", code, body)
	if !waitFor(func() bool { return seenWhere(a.probeSeen(t, tile), map[string]string{"topic": "main-control"}) > 0 }, 30*time.Second) {
		t.Errorf("main's subscription missed main's own publish: %+v", a.probeSeen(t, tile))
	}
	time.Sleep(time.Second) // negative: dev's publishes, made before main's, may not follow it
	for _, s := range a.probeSeen(t, tile) {
		if strings.HasPrefix(s["topic"], "dev-") {
			t.Errorf("dev's publish reached main's subscription: %+v", s)
		}
	}
	for i := range ids("bus") {
		topic := fmt.Sprintf("dev-%d", i)
		if !waitFor(func() bool {
			return seenWhere(a.probeSeen(t, dev), map[string]string{"path": "/on", "topic": topic}) > 0
		}, 30*time.Second) {
			t.Errorf("dev's publish %s never reached dev's own subscriptions: %+v", topic, a.probeSeen(t, dev))
		}
	}
	if n := seenWhere(a.probeSeen(t, dev), map[string]string{"topic": "main-control"}); n != 0 {
		t.Errorf("main's publish reached dev's subscriptions %d times", n)
	}
	code, body = a.api("PUT", dev, "/secret?k=API_KEY", "dev-secret")
	mustOK("dev's vault write", code, body)

	// P22: extra exists in dev's namespace only.
	code, body = kv(dev, "PUT", "res:"+tile+"/extra", "x", "dev-extra")
	mustOK("dev's write of its own resource", code, body)
	if c, b := kv(dev, "GET", "env:extra", "x", ""); c != 200 || b != "dev-extra" {
		t.Errorf("dev reads its own resource: %d %q", c, b)
	}
	if c, b := kv(tile, "PUT", "res:"+tile+"/extra", "x", "main-extra"); c == 200 {
		t.Errorf("main wrote a resource only dev's code declares: %d %s", c, b)
	}
	if c, b := kv(tile, "GET", "res:"+tile+"/extra", "x", ""); c == 200 {
		t.Errorf("main read a resource only dev's code declares: %d %s", c, b)
	}

	want = view{kv: "200:dev-shared", apiKey: "200:dev-secret", dbPass: "placeholder"}
	if files {
		want.blob, want.fs, want.sql = "200:dev-blob", "200:dev-fs", `[["dev-sql"]]`
	}
	if got := reads(dev, "shared"); got != want {
		t.Errorf("dev after its writes: %+v, want %+v", got, want)
	}
	if got := reads(tile, "shared"); got != mainView {
		t.Errorf("main after dev's writes: %+v, want its own %+v", got, mainView)
	}
	if primaryDataHash(t, d.WS) != primary {
		t.Errorf("dev's writes changed main's data or vault")
	}

	// A seed, a manager's act: dev gets a copy of main's data.
	a.mustPost(t, "seed", dlBody(tile, "deployment", "dev", "confirm", "copy-data"))
	waitData := func(what string, ok func(dlDepM2) bool) {
		t.Helper()
		var dd dlDepM2
		if !waitFor(func() bool { dd = a.depM2(t, tile, "dev"); return dd.Data != nil && dd.Data.Busy == "" && ok(dd) }, 3*time.Minute) {
			t.Fatalf("dev's data never became %s: %+v", what, dd.Data)
		}
	}
	waitData("seeded", func(d dlDepM2) bool { return d.Data.State == "seeded" || d.Data.State == "partial" })
	if dd := a.depM2(t, tile, "dev"); dd.Data.State != "seeded" || dd.Data.From != "main" {
		t.Fatalf("the seed: %+v", dd.Data)
	}
	got = reads(dev, "shared")
	want = mainView
	want.apiKey, want.dbPass = got.apiKey, "placeholder" // the vault is not data: a seed leaves it
	if got != want {
		t.Errorf("dev after the seed: %+v, want main's copy %+v", got, want)
	}
	for i := range ids("kv") {
		if c, b := kv(dev, "GET", "env:kv", fmt.Sprintf("main-%d", i), ""); c != 200 || b != fmt.Sprintf("main-kv-%d", i) {
			t.Errorf("dev reads the copy of main-%d: %d %q", i, c, b)
		}
		if c, _ := kv(dev, "GET", "env:kv", fmt.Sprintf("dev-%d", i), ""); c != 404 {
			t.Errorf("dev's own dev-%d outlived the seed: %d", i, c)
		}
	}
	if c, b := kv(dev, "GET", "env:extra", "x", ""); c != 404 {
		t.Errorf("the dev-only resource after the seed: %d %q, want empty", c, b)
	}
	if primaryDataHash(t, d.WS) != primary {
		t.Errorf("the seed changed main's data or vault")
	}
	code, body = kv(dev, "PUT", "env:kv", "shared", "dev-after-seed")
	mustOK("dev's write after the seed", code, body)
	if files {
		code, body = blob(dev, "PUT", "env:files", "a.txt", "dev-after-seed")
		mustOK("dev's blob write after the seed", code, body)
		code, body = a.api("PUT", dev, "/fs?p="+q("env:store/f.txt"), "dev-after-seed")
		mustOK("dev's file write after the seed", code, body)
		code, body = a.api("POST", dev, "/sql?p="+q("env:db")+"&q="+q("insert into t values ('dev-after-seed')"), "")
		mustOK("dev's sqlite write after the seed", code, body)
	}
	code, body = a.do("POST", "/api/xbin/deployments/vault-copy", dlBody(tile, "deployment", "dev", "keys", []string{"DB_PASS"}))
	if code != 200 || !strings.Contains(body, `"copied":["DB_PASS"]`) {
		t.Errorf("the vault copy: %d %.300s", code, body)
	}
	if c, b := a.api("GET", dev, "/secret?k=DB_PASS", ""); c != 200 || b != "main-pass" {
		t.Errorf("dev's copied vault key: %d %q", c, b)
	}
	if got := reads(tile, "shared"); got != mainView {
		t.Errorf("main after the seed and dev's writes: %+v, want %+v", got, mainView)
	}
	if primaryDataHash(t, d.WS) != primary {
		t.Errorf("dev's writes after the seed, or the vault copy, changed main's data or vault")
	}

	// A reset (vault too) empties dev's namespace and vault.
	a.mustPost(t, "reset", dlBody(tile, "deployment", "dev", "confirm", "erase-data", "vault", true))
	waitData("empty after the reset", func(d dlDepM2) bool { return d.Data.State == "empty" && d.Data.Reset })
	got = reads(dev, "shared")
	if strings.HasPrefix(got.kv, "404") {
		got.kv = "404"
	}
	if strings.HasPrefix(got.blob, "404") {
		got.blob = "404"
	}
	if got != empty {
		t.Errorf("dev after the reset: %+v, want %+v", got, empty)
	}
	if c, _ := kv(dev, "GET", "env:kv", "main-0", ""); c != 404 {
		t.Errorf("the seeded copy outlived the reset: %d", c)
	}
	if got := reads(tile, "shared"); got != mainView {
		t.Errorf("main after the reset: %+v, want %+v", got, mainView)
	}
	if primaryDataHash(t, d.WS) != primary {
		t.Errorf("the reset changed main's data or vault")
	}
}

// releaseMountsAtEnd makes d's end unmount every FUSE mount under its
// workspace. xbind leaves its gocryptfs volumes mounted when it stops (the
// next start reuses them, and clears stale ones), and the group kill that
// ends an isolated daemon leaves them stale, which the workspace's removal
// can't get past. Registered after startIsolatedDaemon, it runs before
// that cleanup: it stops d itself, then lazily unmounts what is left.
func releaseMountsAtEnd(t *testing.T, d *isoDaemon) {
	t.Helper()
	t.Cleanup(func() {
		if err := d.halt(); err != nil {
			t.Errorf("stopping the isolated xbind: %v", err)
		}
		fm, err := exec.LookPath("fusermount3")
		if err != nil {
			fm, err = exec.LookPath("fusermount")
		}
		b, rerr := os.ReadFile("/proc/self/mounts")
		if rerr != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 3 || !strings.HasPrefix(f[2], "fuse") || !strings.HasPrefix(f[1], d.WS+"/") {
				continue
			}
			if err != nil {
				t.Errorf("no fusermount to release %s: %v", f[1], err)
				continue
			}
			if out, err := exec.Command(fm, "-u", "-z", f[1]).CombinedOutput(); err != nil {
				t.Errorf("releasing %s: %v %s", f[1], err, out)
			}
		}
	})
}

// covers P3 P6 P28 — flow G end to end on an auth-on isolated daemon.
// apps/shop roots a scope (orders: kv, events: bus) that its nested tiles
// apps/shop/admin (which may call apps/shop, granted writer) and
// apps/shop/stats share. apps/shop and apps/shop/admin each get a dev
// deployment, live reload attached; stats gets none:
//   - the scope's dev namespace is shared: what apps/shop+dev writes,
//     apps/shop/admin+dev reads, and no main nor stats sees it; what stats
//     writes lands in the primary namespace, which the devs don't see;
//   - apps/shop/admin+dev's calls to apps/shop reach apps/shop's primary
//     (its pinned code), clamped to reader and marked X-XBin-Deployment:
//     dev: it can read the scope's primary data through the API but not
//     write it (a writer-guarded route refuses it, and answers the admin's
//     main); block on that edge (a manager's act) stops even the reads,
//     for dev alone; other tiles never reach a non-primary deployment of
//     apps/shop, nor its +main alias;
//   - resetting the scope's dev namespace needs terminal level on every
//     claimant (a user with it on apps/shop only is refused, naming
//     apps/shop/admin, and nothing changes); its dry run lists both
//     tiles' dev as stopping (dev, apps/shop/admin+dev); the reset empties
//     it for both, and leaves the primary namespace;
//   - neither tile can reassign its primary (it would split the scope's
//     data);
//   - the namespace is deleted with its last claimant: removing
//     apps/shop's dev leaves it to apps/shop/admin+dev, removing that one
//     deletes it.
func TestMultiTileScope(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{Auth: true})
	a := d.dl()
	const shop, admin, stats = "apps/shop", "apps/shop/admin", "apps/shop/stats"
	orders, events := "res:"+shop+"/orders", "res:"+shop+"/events"
	shopProbe := dataProbe{Marker: "shop-m1", Resources: map[string]string{"orders": "kv", "events": "bus"}, Uses: []string{orders, events}}
	adminProbe := dataProbe{Marker: "admin-m1", Uses: []string{orders, events, shop}}
	writeDataProbe(t, d.WS, shop, shopProbe)
	writeDataProbe(t, d.WS, admin, adminProbe)
	writeDataProbe(t, d.WS, stats, dataProbe{Marker: "stats-m1", Uses: []string{orders}})
	for tile, m := range map[string]string{shop: "shop-m1", admin: "admin-m1", stats: "stats-m1"} {
		a.waitAPI(t, tile, "/v", m, 3*time.Minute)
		a.waitTile(t, tile)
	}
	if c, b := a.do("POST", "/api/xbin/grants", `{"from":"`+admin+`","target":"`+shop+`","role":"writer"}`); c != 200 {
		t.Fatalf("granting %s writer on %s: %d %s", admin, shop, c, b)
	}
	if !waitFor(func() bool { return a.call(t, admin, "GET", "/api/"+shop+"/v", "").Status == 200 }, time.Minute) {
		t.Fatalf("%s can't call %s after the grant: %+v", admin, shop, a.call(t, admin, "GET", "/api/"+shop+"/v", ""))
	}

	// dev on both tiles, live reload attached, each saved once more.
	for _, x := range []struct {
		tile  string
		probe *dataProbe
	}{{shop, &shopProbe}, {admin, &adminProbe}} {
		if _, e := a.op(t, "add", x.tile, "deployment", "dev", "attach", true); e.Result != "ok" {
			t.Fatalf("adding dev to %s: %+v", x.tile, e)
		}
		x.probe.Marker = strings.Replace(x.probe.Marker, "-m1", "-m2", 1)
		writeDataProbe(t, d.WS, x.tile, *x.probe)
		a.waitAPI(t, x.tile+"+dev", "/v", x.probe.Marker, 3*time.Minute)
	}
	if c, b := a.api("GET", shop, "/v", ""); c != 200 || b != "shop-m1" {
		t.Fatalf("%s's primary: %d %q, want its pinned shop-m1", shop, c, b)
	}

	// One dev namespace for the scope; stats and the mains share the
	// primary's.
	kvAt := func(ref, method, k, v string) (int, string) {
		return a.api(method, ref, "/kv?res="+url.QueryEscape(orders)+"&k="+k, v)
	}
	if c, b := kvAt(shop+"+dev", "PUT", "o1", "dev"); c != 200 {
		t.Fatalf("%s+dev's write: %d %s", shop, c, b)
	}
	if c, b := kvAt(stats, "PUT", "o2", "main"); c != 200 {
		t.Fatalf("%s's write: %d %s", stats, c, b)
	}
	for _, r := range []struct {
		ref, k, want string
	}{
		{admin + "+dev", "o1", "dev"}, {shop + "+dev", "o1", "dev"},
		{shop, "o1", ""}, {admin, "o1", ""}, {stats, "o1", ""},
		{shop, "o2", "main"}, {admin, "o2", "main"}, {stats, "o2", "main"},
		{shop + "+dev", "o2", ""}, {admin + "+dev", "o2", ""},
	} {
		c, b := kvAt(r.ref, "GET", r.k, "")
		if r.want == "" && c != 404 || r.want != "" && (c != 200 || b != r.want) {
			t.Errorf("%s reads %s: %d %q, want %q", r.ref, r.k, c, b, map[bool]string{true: "none", false: r.want}[r.want == ""])
		}
	}

	// Calls stay edges: apps/shop/admin+dev → apps/shop's primary, clamped.
	var who struct{ From, Role, Deployment, Marker string }
	for _, r := range []struct{ from, role, dep string }{{admin + "+dev", "reader", "dev"}, {admin, "writer", ""}} {
		c := a.call(t, r.from, "GET", "/api/"+shop+"/caller", "")
		if c.Status != 200 || json.Unmarshal([]byte(c.Body), &who) != nil {
			t.Fatalf("%s → %s/caller: %+v", r.from, shop, c)
		}
		if who.Marker != "shop-m1" || who.Role != r.role || who.Deployment != r.dep || who.From != admin || c.Deployment != "" {
			t.Errorf("%s → %s reached %+v (answered by %q), want the primary (shop-m1) as %s, deployment %q, from %s",
				r.from, shop, who, c.Deployment, r.role, r.dep, admin)
		}
	}
	if c := a.call(t, admin+"+dev", "GET", "/api/"+shop+"/kv?res="+url.QueryEscape(orders)+"&k=o2", ""); c.Status != 200 || c.Body != "main" {
		t.Errorf("%s+dev reads the scope's primary data through %s's API: %+v", admin, shop, c)
	}
	guarded := "/api/" + shop + "/guarded?res=" + url.QueryEscape(orders) + "&k=w"
	if c := a.call(t, admin+"+dev", "PUT", guarded, "from-dev"); c.Status != 403 {
		t.Errorf("%s+dev wrote through the edge: %+v", admin, c)
	}
	if c := a.call(t, admin, "PUT", guarded, "from-admin"); c.Status != 200 {
		t.Errorf("%s's main can't write through its writer grant: %+v", admin, c)
	}
	if c, b := kvAt(shop, "GET", "w", ""); c != 200 || b != "from-admin" {
		t.Errorf("%s's primary reads w: %d %q, want the admin's write alone", shop, c, b)
	}
	for _, target := range []string{shop + "+dev", shop + "+main"} {
		for _, from := range []string{admin, admin + "+dev", stats} {
			if c := a.call(t, from, "GET", "/api/"+target+"/v", ""); c.Status != 403 {
				t.Errorf("%s → /api/%s/v: %+v, want 403 (another tile)", from, target, c)
			}
		}
	}
	// block on the edge: dev's reads stop too; the admin's main still calls.
	edge := ""
	var st struct {
		Edges []struct{ ID, To, Policy string }
	}
	if c, b := a.do("GET", "/api/xbin/deployments?tile="+admin, ""); c != 200 || json.Unmarshal([]byte(b), &st) != nil {
		t.Fatalf("%s's state: %d %s", admin, c, b)
	}
	for _, e := range st.Edges {
		if e.To == shop && strings.HasPrefix(e.ID, "grant:") {
			edge = e.ID
		}
	}
	if edge == "" {
		t.Fatalf("%s's state lists no edge to %s: %+v", admin, shop, st.Edges)
	}
	a.mustPost(t, "edge", dlBody(admin, "edge", edge, "policy", "block"))
	if c := a.call(t, admin+"+dev", "GET", "/api/"+shop+"/v", ""); c.Status != 403 || !strings.Contains(c.Body, "block") {
		t.Errorf("%s+dev → %s under block: %+v, want 403 naming the policy", admin, shop, c)
	}
	if c := a.call(t, admin, "GET", "/api/"+shop+"/v", ""); c.Status != 200 || c.Body != "shop-m1" {
		t.Errorf("%s's main → %s under dev's block: %+v", admin, shop, c)
	}
	a.mustPost(t, "edge", dlBody(admin, "edge", edge, "policy", "default"))

	// A scope-wide act needs its level on every claimant.
	if c, b := a.do("POST", "/api/xbin/users", `{"id":"tess","role":"user","tiles":{"`+shop+`":"terminal","`+admin+`":"write"},"password":"tess-pw12"}`); c != 200 {
		t.Fatalf("creating tess: %d %s", c, b)
	}
	tess := loginAs(t, a, "tess", "tess-pw12")
	reset := dlBody(shop, "deployment", "dev", "confirm", "erase-data")
	if c, b := tess.do("POST", "/api/xbin/deployments/reset", reset); c != 403 || !strings.Contains(b, admin) {
		t.Errorf("tess's reset of the scope's dev: %d %s, want 403 naming %s", c, b, admin)
	}
	if c, b := kvAt(admin+"+dev", "GET", "o1", ""); c != 200 || b != "dev" {
		t.Errorf("the refused reset changed the namespace: %d %q", c, b)
	}
	c, raw := a.do("POST", "/api/xbin/deployments/reset", dlBody(shop, "deployment", "dev", "confirm", "erase-data", "dryRun", true))
	var dry struct {
		Impact struct{ Stops []string }
	}
	// the tile's own deployments by name, a sibling tile's as its ref
	if c != 200 || json.Unmarshal([]byte(raw), &dry) != nil || !slices.Contains(dry.Impact.Stops, "dev") || !slices.Contains(dry.Impact.Stops, admin+"+dev") {
		t.Errorf("the reset's dry run: %d %.300s, want both tiles' dev stopping", c, raw)
	}
	a.mustPost(t, "reset", reset)
	if !waitFor(func() bool {
		dd := a.depM2(t, shop, "dev")
		return dd.Data != nil && dd.Data.Busy == "" && dd.Data.State == "empty"
	}, 2*time.Minute) {
		t.Fatalf("the scope's dev namespace never became empty: %+v", a.depM2(t, shop, "dev").Data)
	}
	for _, ref := range []string{shop + "+dev", admin + "+dev"} {
		var c int
		var b string
		if !waitFor(func() bool { c, b = kvAt(ref, "GET", "o1", ""); return c == 404 || c == 200 }, 2*time.Minute) || c != 404 {
			t.Errorf("%s reads o1 after the reset: %d %q, want none", ref, c, b)
		}
	}
	if c, b := kvAt(shop, "GET", "o2", ""); c != 200 || b != "main" {
		t.Errorf("the reset reached the primary namespace: %d %q", c, b)
	}

	// No split primaries.
	for _, tile := range []string{shop, admin} {
		if c, _, raw := a.post(t, "primary", dlBody(tile, "deployment", "dev", "confirm", "data-stays")); c != 409 || !strings.Contains(raw, "split") {
			t.Errorf("reassigning %s's primary: %d %s, want 409 (it would split the scope's data)", tile, c, raw)
		}
	}

	// Deleted with the last claimant.
	if c, b := kvAt(shop+"+dev", "PUT", "o3", "dev3"); c != 200 {
		t.Fatalf("%s+dev's write: %d %s", shop, c, b)
	}
	nsDirs := func() []string {
		m, _ := filepath.Glob(filepath.Join(d.WS, "data", "resources-enc", ".deployments", "*", "dev"))
		return m
	}
	if n := nsDirs(); len(n) != 1 {
		t.Fatalf("the scope's dev namespace on disk: %v", n)
	}
	a.mustPost(t, "remove", dlBody(shop, "deployment", "dev", "confirm", "erase"))
	if c, b := kvAt(admin+"+dev", "GET", "o3", ""); c != 200 || b != "dev3" {
		t.Errorf("after %s removed its dev, %s+dev reads o3: %d %q, want the namespace kept", shop, admin, c, b)
	}
	if n := nsDirs(); len(n) != 1 {
		t.Errorf("removing one claimant deleted the namespace: %v", n)
	}
	a.mustPost(t, "remove", dlBody(admin, "deployment", "dev", "confirm", "erase"))
	if !waitFor(func() bool { return len(nsDirs()) == 0 }, 30*time.Second) {
		t.Errorf("the last claimant's removal left the namespace: %v", nsDirs())
	}
	if c, b := kvAt(shop, "GET", "o2", ""); c != 200 || b != "main" {
		t.Errorf("the removals reached the primary namespace: %d %q", c, b)
	}
}

// covers P15 T11 PO-7 — TestDeploymentStateBootsTwice's M2 parts (15-test-plan
// §6), which that test logs as not built: on a fresh workspace, a static
// tile gets a dev deployment with live reload attached (main pinned), an
// edge policy (block on its call grant), dev's deliveries switched off and
// so a dormant cron job of dev's,
// and the work tree moves on. Two boots of the real binary then change
// nothing outside derived trees (.xbin/deploy/): the record, the
// checkpoint store, the view repository, dev's registration file and every
// tile work tree stay byte-identical, and so does each tile's repository
// (refs, HEAD, config). Afterwards the bare URL still serves main's
// checkpoint and dev's URL the work tree, live reload is still on dev, the
// edge still blocked, dev's deliveries still off and its job still listed,
// dormant.
func TestDeploymentStateBootsTwiceWithDeployments(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", ws).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	const tile, other = "apps/ds-deployments", "apps/ds-other"
	dir := filepath.Join(ws, tile)
	mustRT(t, ws, other, "static", "o1", "")
	mustRT(t, ws, tile, "static", "v1", "")
	must(t, saveIfChanged(filepath.Join(dir, "scope.json"), `{"resources":{"cron":{"type":"cron"}}}`+"\n"))
	must(t, saveIfChanged(filepath.Join(dir, "xbin.json"), `{"title":"ds","uses":[{"target":"`+other+`","role":"writer"},`+
		`{"target":"res:`+tile+`/cron","role":"writer"}]}`+"\n"))
	record, recDir, store, view, _ := dlPaths(ws, tile)
	devCron := filepath.Join(recDir, "dev", "cron.json")

	edgeOf := func(a dlAPI) (id, policy string) {
		t.Helper()
		var st struct {
			Edges []struct{ ID, To, Policy string }
		}
		if c, b := a.do("GET", "/api/xbin/deployments?tile="+tile, ""); c != 200 || json.Unmarshal([]byte(b), &st) != nil {
			t.Fatalf("the state: %d %s", c, b)
		}
		for _, e := range st.Edges {
			if e.To == other {
				return e.ID, e.Policy
			}
		}
		t.Fatalf("%s's state lists no edge to %s: %+v", tile, other, st.Edges)
		return "", ""
	}
	serveOnce(t, ws, func(a dlAPI) {
		a.waitServed(t, tile, "static", "v1", 30*time.Second)
		if c, b := a.do("POST", "/api/xbin/grants", `{"from":"`+tile+`","target":"`+other+`","role":"writer"}`); c != 200 {
			t.Fatalf("granting %s writer on %s: %d %s", tile, other, c, b)
		}
		if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
			t.Fatalf("adding dev: %+v", e)
		}
		must(t, saveIfChanged(filepath.Join(dir, "index.html"), "<!doctype html><html><head></head><body>v2</body></html>\n"))
		must(t, saveIfChanged(filepath.Join(dir, probeFile), "v2"))
		if !waitFor(func() bool { c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, ""); return c == 200 && b == "v2" }, 30*time.Second) {
			t.Fatal("dev never served the work tree's v2")
		}
		edge, _ := edgeOf(a)
		a.mustPost(t, "edge", dlBody(tile, "edge", edge, "policy", "block"))
		a.mustPost(t, "deliveries", dlBody(tile, "deployment", "dev", "on", false))
		c, b := a.do("PUT", "/api/xbin/cron/jobs?deployment=dev", `{"name":"nightly","resource":"res:`+tile+`/cron",`+
			`"schedule":"@every 1h","path":"/tick","role":"writer","component":"`+tile+`"}`)
		if c != 200 || !strings.Contains(b, `"dormant":true`) {
			t.Fatalf("dev's cron job: %d %s", c, b)
		}
	})
	for _, p := range []string{record, store, view, devCron} {
		if !isoExists(p) {
			t.Fatalf("the fixture has no %s", p)
		}
	}
	before := snapshot(t, ws)
	repos := tileRepos(t, ws)
	if len(repos) == 0 {
		t.Fatal("the fixture has no tile repository to compare")
	}
	for _, p := range []string{record, store, view, devCron} {
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
		if !waitFor(func() bool { c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, ""); return c == 200 && b == "v2" }, 30*time.Second) {
			t.Error("after the boots dev doesn't serve the work tree's v2")
		}
		if st := a.state(t, tile); st.LiveReload != "dev" || st.pinned("main") == "" || st.pinned("dev") != "" {
			t.Errorf("after the boots: live reload %q, main %q, dev %q", st.LiveReload, st.pinned("main"), st.pinned("dev"))
		}
		if _, policy := edgeOf(a); policy != "block" {
			t.Errorf("after the boots the edge's policy is %q, want block", policy)
		}
		dormant := false
		for _, r := range a.depM2(t, tile, "dev").Registrations {
			dormant = dormant || r.Kind == "cron" && r.Name == "nightly" && r.Dormant
		}
		if !dormant || a.depM2(t, tile, "dev").Deliveries {
			t.Errorf("after the boots dev's deliveries aren't off, or its job isn't listed dormant: %+v", a.depM2(t, tile, "dev"))
		}
	})
}

// dialTape records the event stream of one /ws/events subscriber: wsURL
// (with ?frame=<token> for a frame principal) and hdr (a session cookie)
// say who subscribes. It ends with the test.
func dialTape(t *testing.T, wsURL string, hdr http.Header) *dlTape {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("/ws/events (%s): %v", wsURL, err)
	}
	e := &dlTape{notify: make(chan struct{}, 1)}
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			at := time.Now()
			if err != nil {
				return
			}
			var ev dlEvent
			if json.Unmarshal(msg, &ev) != nil {
				continue
			}
			ev.raw, ev.at = string(msg), at
			e.mu.Lock()
			e.evs = append(e.evs, ev)
			e.mu.Unlock()
			select {
			case e.notify <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return e
}

// covers SC-EVENTS P13 T7 — end to end on an auth-on isolated daemon, the
// server-side rows of TestNonPrimaryBuildErrorNotBroadcast and
// TestDeploymentEventsFiltered through real subscribers: while dev (live
// reload attached) builds, reports a status, notifies, fails a build and
// recovers, the write audience (the owner, a write user) receives each as
// a deployments op naming dev (build start/ok and error with the compiler
// output, reload, status, notify); a read user, the primary's frame token
// minted for them, and another tile's frame token receive nothing that
// names dev; nobody receives a reload, build-*, status or notify event
// for the tile, whose primary never changed; the read user's /components
// entry carries the primary summary alone.
func TestNonPrimaryActivityReachesWriteAudienceOnly(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{Auth: true})
	a := d.dl()
	const tile, other = "apps/ev", "apps/ev-other"
	probe := dataProbe{Marker: "m1", Resources: map[string]string{"kv": "kv"}, Uses: []string{"res:" + tile + "/kv"}}
	must(t, saveIfChanged(filepath.Join(d.WS, tile, "index.html"), "<!doctype html><html><head></head><body>ev</body></html>\n"))
	writeDataProbe(t, d.WS, tile, probe)
	mustRT(t, d.WS, other, "static", "o1", "")
	a.waitAPI(t, tile, "/v", "m1", 3*time.Minute)
	a.waitTile(t, tile)
	a.waitServed(t, other, "static", "o1", 30*time.Second)
	for _, u := range []string{
		`{"id":"wendy","role":"user","tiles":{"` + tile + `":"write"},"password":"wendy-pw1"}`,
		`{"id":"rita","role":"user","tiles":{"` + tile + `":"read","` + other + `":"read"},"password":"rita-pw22"}`,
	} {
		if c, b := a.do("POST", "/api/xbin/users", u); c != 200 {
			t.Fatalf("creating a user: %d %s", c, b)
		}
	}
	writer, reader := loginAs(t, a, "wendy", "wendy-pw1"), loginAs(t, a, "rita", "rita-pw22")
	owner := a.tape(t)
	if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev: %+v", e)
	}
	// The add pins main in its own attempt, which the answer doesn't
	// wait for: the subscribers watch what follows it.
	if !waitFor(func() bool {
		for _, e := range a.log(t, tile) {
			if e.Deployment == "main" && e.How == "attach" && e.final() {
				return true
			}
		}
		return false
	}, time.Minute) {
		t.Fatalf("main's pin at the add never finished: %+v", a.log(t, tile))
	}
	if n := owner.count(0, isEvent("reload", tile)); n > 0 {
		// 11-contract §3.5: a swap reloads the primary only when its code
		// changed, and main's pin at the add is a capture of the work tree
		// it served.
		t.Errorf("main's code-identical pin at the add published %d bare reload(s)", n)
	}

	ws := "ws" + strings.TrimPrefix(a.url, "http") + "/ws/events"
	cookie := func(u userClient) http.Header { return http.Header{"Cookie": {u.cookie.String()}} }
	frameOf := func(path string) string {
		t.Helper()
		_, doc := reader.get(path)
		tok := frameTokenIn(doc)
		if tok == "" {
			t.Fatalf("the read user's %s carries no frame token", path)
		}
		return ws + "?frame=" + url.QueryEscape(tok)
	}
	tapes := map[string]*dlTape{
		"the owner":                  a.tape(t),
		"the write user":             dialTape(t, ws, cookie(writer)),
		"the read user":              dialTape(t, ws, cookie(reader)),
		"the primary's frame token":  dialTape(t, frameOf("/c/"+tile+"/"), nil),
		"another tile's frame token": dialTape(t, frameOf("/c/"+other+"/"), nil),
	}
	marks := map[string]int{}
	for who, tp := range tapes {
		marks[who] = tp.mark()
	}

	// dev's activity: a build, a status, a notification, a failed build,
	// a recovery.
	probe.Marker = "m2"
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile+"+dev", "/v", "m2", 3*time.Minute)
	if c := a.call(t, tile+"+dev", "POST", "/api/xbin/tile-report", `{"level":"warn","message":"dev-status"}`); c.Status != 200 {
		t.Fatalf("dev's status report: %+v", c)
	}
	if c, b := a.api("POST", tile+"+dev", "/notify?user=wendy&title=dev-note", ""); c/100 != 2 || !strings.Contains(b, `"suppressed":true`) {
		t.Fatalf("dev's notification: %d %s", c, b)
	}
	must(t, saveIfChanged(filepath.Join(d.WS, tile, "backend", "main.go"), "package main\n\nthis is not Go\n"))
	errorOp := func(ev dlEvent) bool {
		return ev.Type == "deployments" && ev.Component == tile && ev.op() == "build" &&
			strings.Contains(string(ev.Data), `"phase":"error"`) && strings.Contains(string(ev.Data), `"deployment":"dev"`)
	}
	if _, ok := tapes["the owner"].wait(marks["the owner"], errorOp, 3*time.Minute); !ok {
		t.Fatalf("dev's broken build reached the owner as no op build error: %s", tapes["the owner"].describe(marks["the owner"], tile))
	}
	probe.Marker = "m3"
	must(t, os.Remove(filepath.Join(d.WS, tile, "backend", "main.go")))
	writeDataProbe(t, d.WS, tile, probe)
	a.waitAPI(t, tile+"+dev", "/v", "m3", 3*time.Minute)
	if c, b := a.api("GET", tile, "/v", ""); c != 200 || b != "m1" {
		t.Errorf("the primary: %d %q, want its pinned m1 throughout", c, b)
	}
	time.Sleep(2 * time.Second) // negative: the last event of the recovery reaches every subscriber

	names := func(ev dlEvent) bool {
		return strings.Contains(ev.raw, `"dev"`) || strings.Contains(ev.raw, "+dev") ||
			strings.Contains(ev.raw, "dev-status") || strings.Contains(ev.raw, "dev-note")
	}
	for who, tp := range tapes {
		evs := tp.since(marks[who])
		for _, ev := range evs {
			if ev.Component == tile && (dlOldTypes[ev.Type] || ev.Type == "notify") {
				t.Errorf("%s received %s: dev's activity rode an old type", who, ev.raw)
			}
		}
		switch who {
		case "the owner", "the write user":
			for op, match := range map[string]func(dlEvent) bool{
				"build start": func(ev dlEvent) bool {
					return ev.op() == "build" && strings.Contains(string(ev.Data), `"phase":"start"`)
				},
				"build error": errorOp,
				"build ok":    func(ev dlEvent) bool { return ev.op() == "build" && strings.Contains(string(ev.Data), `"phase":"ok"`) },
				"reload":      func(ev dlEvent) bool { return ev.op() == "reload" },
				"status":      func(ev dlEvent) bool { return ev.op() == "status" && strings.Contains(string(ev.Data), "dev-status") },
				"notify":      func(ev dlEvent) bool { return ev.op() == "notify" && strings.Contains(string(ev.Data), "dev-note") },
			} {
				n := 0
				for _, ev := range evs {
					if ev.Type == "deployments" && ev.Component == tile && strings.Contains(string(ev.Data), `"deployment":"dev"`) && match(ev) {
						n++
					}
				}
				if n == 0 {
					t.Errorf("%s received no deployments op %s naming dev: %s", who, op, tp.describe(marks[who], tile))
				}
			}
			for _, ev := range evs {
				if errorOp(ev) && !strings.Contains(string(ev.Data), "syntax error") {
					t.Errorf("%s's build error carries no compiler output: %s", who, ev.raw)
				}
			}
		default:
			for _, ev := range evs {
				if names(ev) {
					t.Errorf("%s received a fact naming dev: %s", who, ev.raw)
				}
			}
		}
	}

	c, body := reader.get("/api/xbin/components/" + tile)
	if c != 200 || !strings.Contains(body, `"deployments":{`) || strings.Contains(body, `"dev"`) || strings.Contains(body, "+dev") {
		t.Errorf("the read user's /components entry: %d %.400s, want the primary summary alone", c, body)
	}
}
