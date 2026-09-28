//go:build integration

package test

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The downgrade simulation (15-test-plan §5.6; 12-compat §5): the new xbind
// leaves deployment state in a workspace, the previous release's xbind
// (XBIN_DOWNGRADE_BIN) runs on it, then the new one again. An older xbind
// loses no state and never fires a deployment's registrations as main's: it
// serves every tile's work tree, fires only main's registrations, and never
// opens the stores it doesn't know (a non-primary deployment's cron jobs,
// active for it under the new binary, P13, live in files of their own).
//
// XBIN_DOWNGRADE_BIN is the previous release's xbind: CI downloads it from the
// release assets; locally, build it from the tag, e.g.
//
//	git archive v0.3.61 | tar -x -C /tmp/v0.3.61 && (cd /tmp/v0.3.61 && go build -o /tmp/xbind-v0.3.61 ./cmd/xbind)
//
// Without it both tests skip.

const (
	dgStaticTile = "apps/dg-static" // tile A: static, live reload paused, the work tree past the checkpoint
	dgProbeTile  = "apps/dg-probe"  // tile B: the probe, with a dev deployment and a cron job of dev's own
)

// covers T11 12-compat — 15-test-plan §5.6 on a daemon of the shared daemon's
// flavour (--no-auth, not isolated): static tile A has live reload paused,
// main pinned to checkpoint c1 (m1), its work tree at m2. The previous
// release boots on that workspace and serves A's work tree (m2), and leaves
// the root xbin.json and data/cron-jobs.json byte-identical and
// data/deployments/ and data/checkpoints/ untouched. The new binary again:
// A serves c1, still pinned, live reload still paused.
func TestDowngradeStatic(t *testing.T) {
	old := downgradeBinOrSkip(t)
	h := startDowngradePlain(t)
	c1 := dgStaticSetup(t, h)
	before := dgHandOver(t, h, old)
	dgOlderXbind(t, h)
	dgStaticUnderOld(t, h)
	h.stop(t)
	dgSameStores(t, h, before)
	h.start(t, xbindBin)
	dgStaticAfter(t, h, c1)
}

// covers T11 SC-DORMANT PO-9 12-compat — 15-test-plan §5.6 on an isolated daemon
// (a non-primary backend needs isolation, P18): tile A as in
// TestDowngradeStatic, and probe tile B with a main cron job /tick and a dev
// deployment whose cron job /dev-tick is active for dev (P13, revised): it
// ticks dev, never main. Under the previous release A serves m2; B's main
// serves its work tree and ticks, and its /seen never shows /dev-tick
// across two main ticks; B+dev isn't served; the root xbin.json and
// data/cron-jobs.json are byte-identical and data/deployments/ and
// data/checkpoints/ untouched. The new binary again: A serves c1, and B's
// dev job is still dev's (listed active for dev, ticking dev, never main).
func TestDowngradeDormantRegistrations(t *testing.T) {
	old := downgradeBinOrSkip(t)
	d := startIsolatedDaemon(t, isoOpts{})
	h := &dgHost{ws: d.WS, a: d.dl(),
		start: func(t *testing.T, bin string) { d.Bin = bin; d.start(t) },
		stop:  d.stop,
	}
	c1 := dgStaticSetup(t, h)
	dgProbeSetup(t, h)
	before := dgHandOver(t, h, old)
	dgOlderXbind(t, h)
	dgStaticUnderOld(t, h)
	dgProbeUnderOld(t, h)
	h.stop(t)
	dgSameStores(t, h, before)
	h.start(t, xbindBin)
	dgStaticAfter(t, h, c1)
	dgProbeAfter(t, h)
}

// downgradeBinOrSkip is XBIN_DOWNGRADE_BIN, the previous release's xbind, or
// skips the test saying why.
func downgradeBinOrSkip(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("XBIN_DOWNGRADE_BIN")
	if bin == "" {
		t.Skip("downgrade: XBIN_DOWNGRADE_BIN names no previous release's xbind (CI downloads it; locally, build it from the tag)")
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		t.Fatalf("XBIN_DOWNGRADE_BIN=%s is not an executable file (%v)", bin, err)
	}
	return abs
}

// dgHost is one workspace and the xbind serving it, whichever binary that
// is: start runs bin on the workspace (the same address), stop stops it.
type dgHost struct {
	ws    string
	a     dlAPI
	start func(t *testing.T, bin string)
	stop  func(t *testing.T)
}

// startDowngradePlain starts the suite's xbind as the shared daemon runs
// (--no-auth, not isolated) on a workspace of its own, restartable as another
// binary on the same workspace and address. It stops, and its workspace goes,
// when the test ends.
func startDowngradePlain(t *testing.T) *dgHost {
	t.Helper()
	dir := t.TempDir()
	p := &dgPlain{ws: filepath.Join(dir, "ws"), addr: isoFreeAddr(t), logPath: filepath.Join(dir, "xbind.log")}
	t.Cleanup(func() {
		if err := p.halt(); err != nil {
			t.Errorf("stopping xbind: %v", err)
		}
		if t.Failed() {
			t.Logf("xbind (%s):\n%s", p.ws, isoTail(p.logPath, 80))
		}
		for i := 0; i < 40; i++ { // outlast a straggler's final flush
			if removeTree(p.ws); !isoExists(p.ws) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("the workspace %s could not be removed", p.ws)
	})
	p.start(t, xbindBin)
	return &dgHost{ws: p.ws, a: dlAPI{url: "http://" + p.addr}, start: p.start,
		stop: func(t *testing.T) {
			t.Helper()
			if err := p.halt(); err != nil {
				t.Fatalf("stopping xbind: %v", err)
			}
		}}
}

// dgPlain is a non-isolated xbind on a workspace and an address, each start
// running the binary it is given; the log file collects every run.
type dgPlain struct {
	ws, addr, logPath string
	cmd               *exec.Cmd
	done              chan struct{} // closed once cmd has been waited for
}

// start runs bin on the workspace in a process group of its own and waits (a
// bounded minute) until it is healthy.
func (p *dgPlain) start(t *testing.T, bin string) {
	t.Helper()
	if p.cmd != nil {
		t.Fatal("xbind: start while it runs (stop it first)")
	}
	args := []string{"--workspace", p.ws, "--listen", p.addr, "--no-auth"}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	logf, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close() // the child holds its own copy
	_, _ = io.WriteString(logf, "--- start "+time.Now().Format(time.RFC3339Nano)+": "+bin+" "+strings.Join(args, " ")+"\n")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	p.cmd, p.done = cmd, done
	a := dlAPI{url: "http://" + p.addr}
	healthy := waitFor(func() bool {
		select {
		case <-done:
			return true // exited: no use waiting
		default:
		}
		c, _ := a.do("GET", "/healthz", "")
		return c == 200
	}, 60*time.Second)
	select {
	case <-done:
		healthy = false
	default:
	}
	if !healthy {
		_ = p.halt()
		t.Fatalf("%s never became healthy on %s:\n%s", bin, p.ws, isoTail(p.logPath, 60))
	}
}

// halt stops xbind (SIGTERM, a bounded wait) and then kills its process
// group, stragglers included; the workspace stays.
func (p *dgPlain) halt() error {
	if p.cmd == nil {
		return nil
	}
	cmd, done := p.cmd, p.done
	p.cmd, p.done = nil, nil
	var err error
	select {
	case <-done:
	default:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			err = errors.New("no exit 20 s after SIGTERM; killed")
		}
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	return err
}

// ---- step 1: the state the new binary leaves ----

// dgStaticSetup makes tile A: a static tile whose live reload is paused, main
// pinned to c1 (m1), and whose work tree then moves to m2, which nothing
// serves. It returns c1's id.
func dgStaticSetup(t *testing.T, h *dgHost) string {
	t.Helper()
	mustRT(t, h.ws, dgStaticTile, "static", "m1", "")
	h.a.waitServed(t, dgStaticTile, "static", "m1", time.Minute)
	_, e := h.a.op(t, "live-reload/pause", dgStaticTile)
	if e.Result != "ok" || e.Checkpoint == "" {
		t.Fatalf("pausing live reload on %s: %+v", dgStaticTile, e)
	}
	mustRT(t, h.ws, dgStaticTile, "static", "m2", "")
	// the save is seen (the work tree moved past c1) and reaches no one
	var st dlState
	if !waitFor(func() bool {
		st = h.a.state(t, dgStaticTile)
		return st.WorkTree != nil && st.WorkTree.Changed > 0
	}, 30*time.Second) {
		t.Fatalf("%s's work tree never moved past c1: %+v", dgStaticTile, st.WorkTree)
	}
	if v, _, ok := h.a.served(dgStaticTile, "static"); !ok || v != "m1" {
		t.Fatalf("%s paused serves %q (ok %v), want c1's m1", dgStaticTile, v, ok)
	}
	if st.pinned("main") != e.Checkpoint || st.LiveReload != "" {
		t.Fatalf("%s: main pinned to %q, live reload %q; want c1 %s, paused", dgStaticTile, st.pinned("main"), st.LiveReload, e.Checkpoint)
	}
	return e.Checkpoint
}

// dgProbeSetup makes tile B: the probe at m1 with a main cron job /tick
// every second, a dev deployment (a checkpoint of the work tree), and dev's
// cron job /dev-tick every second, active for dev; main's job ticks main,
// dev's ticks dev.
func dgProbeSetup(t *testing.T, h *dgHost) {
	t.Helper()
	writeProbe(t, h.ws, dgProbeTile, "m1")
	dgWaitV(t, h, "", "m1", 3*time.Minute)
	h.a.waitTile(t, dgProbeTile)
	dgPutJob(t, h, "", "tick", "/tick")
	if _, e := h.a.op(t, "add", dgProbeTile, "deployment", "dev"); e.Result != "ok" {
		t.Fatalf("adding dev to %s: %+v", dgProbeTile, e)
	}
	dgWaitV(t, h, "dev", "m1", 3*time.Minute)
	dgPutJob(t, h, "dev", "dev-tick", "/dev-tick")
	dgDevTicksDev(t, h, "the new binary")
}

// dgPutJob registers an every-second cron job of B's deployment dep ("" for
// main) as the owner; dev's is stored active, for dev.
func dgPutJob(t *testing.T, h *dgHost, dep, name, path string) {
	t.Helper()
	route := "/api/xbin/cron/jobs"
	if dep != "" {
		route += "?deployment=" + dep
	}
	body := `{"name":"` + name + `","resource":"res:` + dgProbeTile + `/cron","schedule":"@every 1s","path":"` + path +
		`","role":"writer","component":"` + dgProbeTile + `"}`
	code, raw := h.a.do("PUT", route, body)
	var ans struct {
		Dormant    bool   `json:"dormant"`
		Deployment string `json:"deployment"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &ans) != nil {
		t.Fatalf("PUT %s %s: %d %s", route, name, code, raw)
	}
	if dep != "" && (ans.Dormant || ans.Deployment != dep) {
		t.Fatalf("%s's job %s: %s, want it stored active for %s", dep, name, raw, dep)
	}
}

// ---- step 2: the hand-over ----

// dgHandOver stops the new binary, records the stores an older binary must
// leave as they are, and starts bin on the same workspace.
func dgHandOver(t *testing.T, h *dgHost, bin string) dgStores {
	t.Helper()
	h.stop(t)
	s := dgSnapshot(t, h.ws)
	h.start(t, bin)
	return s
}

// dgStores is what 12-compat §5 promises an older xbind leaves as it found
// it: the root xbin.json and data/cron-jobs.json byte for byte (absent stays
// absent), and every file, mode and directory of data/deployments/ and
// data/checkpoints/.
type dgStores struct {
	files map[string]string            // rel path → content, or dgAbsent
	trees map[string]map[string]string // rel dir → snapshot (file → hash)
	hash  map[string]string            // rel dir → treeHash (modes and directories too)
}

const dgAbsent = "(absent)"

func dgSnapshot(t *testing.T, ws string) dgStores {
	t.Helper()
	s := dgStores{files: map[string]string{}, trees: map[string]map[string]string{}, hash: map[string]string{}}
	for _, rel := range []string{"xbin.json", "data/cron-jobs.json"} {
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s.files[rel] = dgAbsent
		case err != nil:
			t.Fatal(err)
		default:
			s.files[rel] = string(b)
		}
	}
	for _, rel := range []string{"data/deployments", "data/checkpoints"} {
		dir := filepath.Join(ws, filepath.FromSlash(rel))
		if !isoExists(dir) {
			t.Fatalf("%s doesn't exist: the new binary left no deployment state to downgrade over", rel)
		}
		s.trees[rel] = snapshot(t, dir)
		s.hash[rel] = treeHash(t, dir, func(string) bool { return false })
	}
	return s
}

// dgSameStores compares the stores with what the new binary left.
func dgSameStores(t *testing.T, h *dgHost, before dgStores) {
	t.Helper()
	after := dgSnapshot(t, h.ws)
	for rel, was := range before.files {
		if now := after.files[rel]; now != was {
			t.Errorf("the previous release changed %s:\n--- before\n%s\n--- after\n%s", rel, was, now)
		}
	}
	for rel, was := range before.trees {
		if diff := changed(was, after.trees[rel]); len(diff) > 0 {
			t.Errorf("the previous release touched %s:\n  %s", rel, strings.Join(diff, "\n  "))
		} else if after.hash[rel] != before.hash[rel] {
			t.Errorf("the previous release changed a mode or a directory under %s", rel)
		}
	}
}

// ---- step 3: under the previous release ----

// dgOlderXbind checks that the binary now serving predates tile deployments:
// GET /deployments is Go's plain 404 or 405, not JSON (11-contract §1.3).
// Once the previous release speaks them, §5.6's simulation (an xbind that
// knows one runtime per tile) no longer applies, and the test skips.
func dgOlderXbind(t *testing.T, h *dgHost) {
	t.Helper()
	code, body := h.a.do("GET", "/api/xbin/deployments?tile="+dgStaticTile, "")
	switch {
	case code == 200:
		t.Skipf("XBIN_DOWNGRADE_BIN speaks tile deployments (GET /deployments: %.120s): 15-test-plan §5.6 simulates a downgrade to a release before them", body)
	case (code != 404 && code != 405) || strings.HasPrefix(strings.TrimSpace(body), "{"):
		t.Fatalf("XBIN_DOWNGRADE_BIN's GET /deployments answered %d %q, not an older xbind's plain 404 or 405", code, body)
	}
}

// dgStaticUnderOld: the older binary serves A's work tree (m2): it knows no
// pinned deployment.
func dgStaticUnderOld(t *testing.T, h *dgHost) {
	t.Helper()
	var v string
	var ok bool
	if !waitFor(func() bool { v, _, ok = h.a.served(dgStaticTile, "static"); return ok && v == "m2" }, time.Minute) {
		t.Fatalf("under the previous release %s serves %q (ok %v), want its work tree's m2", dgStaticTile, v, ok)
	}
}

// dgProbeUnderOld: the older binary serves B's work tree, ticks main's job,
// never delivers dev's, and doesn't route B+dev.
func dgProbeUnderOld(t *testing.T, h *dgHost) {
	t.Helper()
	dgWaitV(t, h, "", "m1", 3*time.Minute)
	dgNoDevTick(t, h, "the previous release")
	if code, body := h.a.do("GET", "/api/"+dgProbeTile+"+dev/v", ""); code == 200 {
		t.Errorf("the previous release routed %s+dev: %d %q", dgProbeTile, code, body)
	}
}

// ---- step 4: the new binary again ----

// dgStaticAfter: A serves c1 again, pinned, live reload still paused.
func dgStaticAfter(t *testing.T, h *dgHost, c1 string) {
	t.Helper()
	var v string
	var ok bool
	if !waitFor(func() bool { v, _, ok = h.a.served(dgStaticTile, "static"); return ok && v == "m1" }, time.Minute) {
		t.Fatalf("after the upgrade %s serves %q (ok %v), want c1's m1", dgStaticTile, v, ok)
	}
	if st := h.a.state(t, dgStaticTile); st.pinned("main") != c1 || st.LiveReload != "" {
		t.Errorf("after the upgrade %s: main pinned to %q, live reload %q; want c1 %s, paused", dgStaticTile, st.pinned("main"), st.LiveReload, c1)
	}
}

// dgProbeAfter: B's dev job is listed active for dev, and it ticks dev,
// never main.
func dgProbeAfter(t *testing.T, h *dgHost) {
	t.Helper()
	code, raw := h.a.do("GET", "/api/xbin/cron/jobs?deployment=dev", "")
	var jobs struct {
		Jobs []struct {
			Name, Component, Deployment string
			Dormant                     bool
		} `json:"jobs"`
	}
	if code != 200 || json.Unmarshal([]byte(raw), &jobs) != nil {
		t.Fatalf("GET /cron/jobs?deployment=dev: %d %s", code, raw)
	}
	found := false
	for _, j := range jobs.Jobs {
		if j.Component == dgProbeTile && j.Name == "dev-tick" {
			found = true
			if j.Dormant || j.Deployment != "dev" {
				t.Errorf("after the upgrade dev's job: %+v, want it active for dev", j)
			}
		}
	}
	if !found {
		t.Errorf("after the upgrade dev's job dev-tick is gone: %s", raw)
	}
	dgWaitV(t, h, "", "m1", 3*time.Minute)
	dgWaitV(t, h, "dev", "m1", 3*time.Minute)
	dgDevTicksDev(t, h, "the new binary, after the upgrade")
}

// ---- the probe's deliveries ----

// dgWaitV waits (bounded by max) until B's deployment dep ("" for the bare
// URL) answers marker on GET /v.
func dgWaitV(t *testing.T, h *dgHost, dep, marker string, max time.Duration) {
	t.Helper()
	path := "/api/" + dgProbeTile
	if dep != "" {
		path = "/api/" + dgProbeTile + "+" + dep
	}
	var code int
	var body string
	waitFor(func() bool {
		code, body = h.a.do("GET", path+"/v", "")
		return code == 200 && body == marker || strings.Contains(body, "build failed")
	}, max)
	if code != 200 || body != marker {
		t.Fatalf("%s/v never answered %q: %d %s", path, marker, code, body)
	}
}

// dgSeen is the deliveries B's deployment dep ("" for the bare URL) recorded.
func dgSeen(t *testing.T, h *dgHost, dep string) []dgDelivery {
	t.Helper()
	path := "/api/" + dgProbeTile
	if dep != "" {
		path = "/api/" + dgProbeTile + "+" + dep
	}
	code, body := h.a.do("GET", path+"/seen", "")
	var seen []dgDelivery
	if code != 200 || json.Unmarshal([]byte(body), &seen) != nil {
		t.Fatalf("GET %s/seen: %d %s", path, code, body)
	}
	return seen
}

// dgDelivery is one POST the probe recorded (probe_test.go's type).
type dgDelivery struct {
	Path       string `json:"path"`
	From       string `json:"from"`
	Deployment string `json:"deployment"`
}

// dgTicks counts the deliveries of path in seen.
func dgTicks(seen []dgDelivery, path string) int {
	n := 0
	for _, s := range seen {
		if s.Path == path {
			n++
		}
	}
	return n
}

// dgNoDevTick waits until main's /tick has been delivered twice more (a
// bounded 30 s), then checks that /dev-tick reached neither main nor, while
// the binary routes it, dev. who names the binary serving, for the message.
func dgNoDevTick(t *testing.T, h *dgHost, who string) {
	t.Helper()
	devRouted := func() bool { c, _ := h.a.do("GET", "/api/"+dgProbeTile+"+dev/v", ""); return c == 200 }()
	start := dgTicks(dgSeen(t, h, ""), "/tick")
	var seen []dgDelivery
	if !waitFor(func() bool { seen = dgSeen(t, h, ""); return dgTicks(seen, "/tick") >= start+2 }, 30*time.Second) {
		t.Fatalf("under %s main's /tick was delivered %d times in 30 s, want 2 more than %d: %+v", who, dgTicks(seen, "/tick"), start, seen)
	}
	if n := dgTicks(seen, "/dev-tick"); n > 0 {
		t.Errorf("under %s main's probe got dev's /dev-tick %d times: %+v", who, n, seen)
	}
	for _, s := range seen {
		if s.Path == "/tick" && s.From != "xbin/cron" {
			t.Errorf("under %s main's /tick came from %q, not xbin/cron", who, s.From)
		}
	}
	if devRouted {
		if dev := dgSeen(t, h, "dev"); dgTicks(dev, "/dev-tick") > 0 {
			t.Errorf("under %s dev's job fired: %+v", who, dev)
		}
	}
}

// dgDevTicksDev is the new binary's check: main's /tick is delivered twice
// more (dgNoDevTick's wait, never /dev-tick to main), and dev's own job
// ticks dev (a bounded 30 s), never with main's /tick.
func dgDevTicksDev(t *testing.T, h *dgHost, who string) {
	t.Helper()
	var dev []dgDelivery
	if !waitFor(func() bool { dev = dgSeen(t, h, "dev"); return dgTicks(dev, "/dev-tick") > 0 }, 30*time.Second) {
		t.Errorf("under %s dev's job never ticked dev: %+v", who, dev)
	}
	start := dgTicks(dgSeen(t, h, ""), "/tick")
	var seen []dgDelivery
	if !waitFor(func() bool { seen = dgSeen(t, h, ""); return dgTicks(seen, "/tick") >= start+2 }, 30*time.Second) {
		t.Fatalf("under %s main's /tick was delivered %d times in 30 s, want 2 more than %d: %+v", who, dgTicks(seen, "/tick"), start, seen)
	}
	if n := dgTicks(seen, "/dev-tick"); n > 0 {
		t.Errorf("under %s main's probe got dev's /dev-tick %d times: %+v", who, n, seen)
	}
	if n := dgTicks(dgSeen(t, h, "dev"), "/tick"); n > 0 {
		t.Errorf("under %s dev's probe got main's /tick %d times", who, n)
	}
}
