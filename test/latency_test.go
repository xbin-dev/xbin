//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/util"
)

// The dev-flow latency budgets (plans/dev-flow.md, "Latency budgets"), each
// a p95 target, measured end to end against a running xbind in two tiers:
//
//   - every `make integration`: one cold sample is discarded, then five warm
//     ones are taken, and their median meets the budget. SC-LATENCY-DEFAULT
//     names no separate hard bound for these two budgets, so a sample is
//     bounded only by how long it is waited for (a sample that never lands
//     fails);
//   - XBIN_TEST_FULL=1 (milestone exits): 30 warm samples, whose p95 meets the
//     budget. With XBIN_BASELINE_BIN (the previous release's xbind) the same
//     samples are first taken on that binary, back to back on this host, and
//     the p95 may regress by at most the larger of 10 % and 50 ms.
//
// Every sample is logged with its breakdown. A budget that master already
// misses is reported, never loosened.
const (
	latencyFastSamples = 5
	latencyFullSamples = 30
	latencyPoll        = 20 * time.Millisecond
	latencyDebounce    = 300 * time.Millisecond // the watcher's debounce (boot.watchDebounce)
	latencyQuiet       = 2 * latencyDebounce    // no event this long: the previous batch is over
)

// covers SC-LATENCY-DEFAULT D119d — static save → frame reload < 500 ms: the
// file write to the tile's `reload` on /ws/events, the watcher's debounce
// inside the budget. The reference tile is R-static: the agent template's
// files (the largest shipped tile) as a static tile. Then (M2) the same
// budget with live reload attached to a non-primary deployment `dev`, on a
// daemon of the shared daemon's flavour with its own workspace (the shared
// one stays in the zero state): the write to dev's `deployments` op
// `reload`, and no bare `reload` of the pinned primary. The full tier
// compares both with the baseline's zero-state save.
func TestLatencyStaticSaveToReload(t *testing.T) {
	c := latencyCase{
		name:   "static save → frame reload",
		budget: 500 * time.Millisecond,
		setup:  setupStaticLatency,
	}
	base := runLatency(t, c)
	t.Run("live reload on dev", func(t *testing.T) {
		a, pws := startPlainDaemon(t)
		n, _ := latencySamples()
		dev := c
		dev.name, dev.setup = "static save → dev's frame reload (live reload on dev)", setupStaticDevLatency
		judgeLatency(t, dev, measureLatency(t, dev, latencyTarget{"candidate, live reload on dev", a.url, pws}, n), base)
	})
}

// covers SC-LATENCY-DEFAULT D119d — Go save → new backend serving < 2 s with a
// warm cache: the write to the first 200 carrying the new body, polled every
// 20 ms. The reference tile is R-go: examples/counter-go, copied in. Then
// (M2) the same budget with live reload attached to a non-primary
// deployment `dev`, on an isolated daemon (a non-primary backend needs
// isolation, D119h): the write to the first 200 from /api/<tile>+dev/ with the
// new body, broken down by dev's `deployments` ops `reload` and `build`,
// while the pinned primary keeps its code. The full tier's baseline for it is
// the baseline binary's zero-state save on an isolated daemon.
func TestLatencyGoSaveToServing(t *testing.T) {
	c := latencyCase{
		name:   "Go save → new backend serving",
		budget: 2 * time.Second,
		setup:  setupGoLatency,
	}
	runLatency(t, c)
	t.Run("live reload on dev", func(t *testing.T) {
		d := startIsolatedDaemon(t, isoOpts{})
		n, full := latencySamples()
		var base []time.Duration
		if bin := os.Getenv("XBIN_BASELINE_BIN"); full && bin != "" {
			t.Run("baseline", func(t *testing.T) {
				b := startIsolatedDaemon(t, isoOpts{})
				b.stop(t)
				b.Bin = bin
				b.start(t)
				base = measureLatency(t, c, latencyTarget{"baseline (isolated)", b.URL, b.WS}, n)
			})
			if base == nil {
				t.Fatal("the isolated baseline was not measured")
			}
		}
		dev := c
		dev.name, dev.setup = "Go save → dev's new backend serving (live reload on dev)", setupGoDevLatency
		judgeLatency(t, dev, measureLatency(t, dev, latencyTarget{"candidate, live reload on dev", d.URL, d.WS}, n), base)
	})
}

type latencyTarget struct {
	label string // candidate, or baseline
	url   string // http://host:port of its xbind
	ws    string // its workspace
}

type latencyCase struct {
	name   string
	budget time.Duration
	// setup installs the reference tile on a target and returns the sampler:
	// one save, measured.
	setup func(t *testing.T, tgt latencyTarget, tap *eventTap) func(t *testing.T, i int) latencySample
}

type latencyPart struct {
	name string
	d    time.Duration
	note string
}

type latencySample struct {
	total time.Duration
	parts []latencyPart
}

func (s latencySample) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v =", s.total.Round(time.Millisecond))
	for i, p := range s.parts {
		if i > 0 {
			b.WriteString(" ·")
		}
		switch {
		case p.note != "" && p.d == 0:
			fmt.Fprintf(&b, " %s — (%s)", p.name, p.note)
		case p.note != "":
			fmt.Fprintf(&b, " %s %v (%s)", p.name, p.d.Round(time.Millisecond), p.note)
		default:
			fmt.Fprintf(&b, " %s %v", p.name, p.d.Round(time.Millisecond))
		}
	}
	return b.String()
}

// latencySamples is how many warm samples the tier takes, and whether it is
// the full tier.
func latencySamples() (n int, full bool) {
	if os.Getenv("XBIN_TEST_FULL") == "1" {
		return latencyFullSamples, true
	}
	return latencyFastSamples, false
}

// runLatency measures c on the shared daemon (the zero state) and, in the
// full tier with XBIN_BASELINE_BIN, on the baseline binary first, and judges
// it. It returns the baseline's samples (nil when none were taken).
func runLatency(t *testing.T, c latencyCase) []time.Duration {
	n, full := latencySamples()
	bin := os.Getenv("XBIN_BASELINE_BIN")
	var base []time.Duration
	if full && bin != "" {
		t.Run("baseline", func(t *testing.T) { base = measureLatency(t, c, startLatencyDaemon(t, bin), n) })
		if base == nil {
			t.Fatal("the baseline was not measured")
		}
	} else if bin != "" {
		t.Logf("XBIN_BASELINE_BIN is compared only in the full tier (XBIN_TEST_FULL=1)")
	}
	judgeLatency(t, c, measureLatency(t, c, latencyTarget{"candidate", baseURL, ws}, n), base)
	return base
}

// judgeLatency checks the candidate's samples against c's budget (the
// median in the fast tier, the p95 in the full one) and, given the
// baseline's, the p95 regression.
func judgeLatency(t *testing.T, c latencyCase, cand, base []time.Duration) {
	t.Helper()
	_, full := latencySamples()
	bin := os.Getenv("XBIN_BASELINE_BIN")
	med, p95, worst := latencyStats(cand)
	t.Logf("%s: %d warm samples — median %v, p95 %v, max %v; budget %v", c.name, len(cand),
		med.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond), c.budget)
	if !full {
		if med > c.budget {
			t.Errorf("%s: the median of %d warm samples, %v, misses the %v budget", c.name, len(cand), med.Round(time.Millisecond), c.budget)
		}
		return
	}
	if p95 > c.budget {
		t.Errorf("%s: p95 over %d samples, %v, misses the %v budget", c.name, len(cand), p95.Round(time.Millisecond), c.budget)
	}
	if base != nil {
		bmed, bp95, bworst := latencyStats(base)
		allowed := bp95 + max(bp95/10, 50*time.Millisecond)
		t.Logf("%s, baseline %s: median %v, p95 %v, max %v; the candidate's p95 may reach %v", c.name, bin,
			bmed.Round(time.Millisecond), bp95.Round(time.Millisecond), bworst.Round(time.Millisecond), allowed.Round(time.Millisecond))
		if p95 > allowed {
			t.Errorf("%s: p95 regressed to %v from the baseline's %v (allowed: the larger of 10 %% and 50 ms, %v)",
				c.name, p95.Round(time.Millisecond), bp95.Round(time.Millisecond), allowed.Round(time.Millisecond))
		}
	}
}

// measureLatency installs the case's reference tile on tgt, discards one
// cold sample and returns n warm ones.
func measureLatency(t *testing.T, c latencyCase, tgt latencyTarget, n int) []time.Duration {
	t.Helper()
	tap := tapEvents(t, tgt)
	sample := c.setup(t, tgt, tap)
	t.Logf("%s, cold (discarded): %s", tgt.label, sample(t, 0))
	out := make([]time.Duration, 0, n)
	for i := 1; i <= n; i++ {
		s := sample(t, i)
		t.Logf("%s, sample %d: %s", tgt.label, i, s)
		out = append(out, s.total)
	}
	return out
}

// latencyStats returns the median, the p95 (nearest rank) and the maximum.
func latencyStats(ds []time.Duration) (med, p95, worst time.Duration) {
	s := slices.Clone(ds)
	slices.Sort(s)
	n := len(s)
	if n == 0 {
		return 0, 0, 0
	}
	med = s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	rank := (95*n + 99) / 100 // ⌈0.95·n⌉
	return med, s[rank-1], s[n-1]
}

func setupStaticLatency(t *testing.T, tgt latencyTarget, tap *eventTap) func(*testing.T, int) latencySample {
	const comp = "apps/latency-static"
	index, orig := installStaticLatency(t, tgt, comp)
	return func(t *testing.T, i int) latencySample {
		tap.settle(comp)
		save := append(slices.Clone(orig), fmt.Sprintf("<!-- latency sample %d -->\n", i)...)
		m := tap.mark()
		t0 := time.Now()
		if err := os.WriteFile(index, save, 0o644); err != nil {
			t.Fatal(err)
		}
		ev, ok := tap.next(m, func(e tapEvent) bool { return e.Type == "reload" && e.Component == comp }, 10*time.Second)
		if !ok {
			t.Fatalf("%s: no reload for %s within 10 s of a save; events: %s", tgt.label, comp, tap.describe(m))
		}
		watch := ev.at.Sub(t0)
		return latencySample{total: watch, parts: []latencyPart{
			{"debounce+rescan", watch, fmt.Sprintf("debounce %v, rescan and publish ≈%v", latencyDebounce, (watch - latencyDebounce).Round(time.Millisecond))},
			{"checkpoint", 0, "none: no checkpoint on this path"},
		}}
	}
}

func setupGoLatency(t *testing.T, tgt latencyTarget, tap *eventTap) func(*testing.T, int) latencySample {
	const comp = "apps/latency-go"
	src, orig := installGoLatency(t, tgt, comp, "latencygo")
	url := tgt.url + "/api/" + comp + "/count"
	bin := filepath.Join(tgt.ws, ".xbin", "build", util.CompKey(comp), "bin")
	return func(t *testing.T, i int) latencySample {
		tap.settle(comp)
		marker := fmt.Sprintf("s%d-%d", i, time.Now().UnixNano())
		save := bytes.Replace(orig, []byte(latencyShape), []byte(latencyShape+`,"lat":"`+marker+`"`), 1)
		m := tap.mark()
		t0 := time.Now()
		if err := os.WriteFile(src, save, 0o644); err != nil {
			t.Fatal(err)
		}
		var served time.Time
		for deadline := t0.Add(60 * time.Second); ; time.Sleep(latencyPoll) {
			if c, b := latencyGet(url); c == 200 && strings.Contains(b, marker) {
				served = time.Now()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s never served the save within 60 s; events: %s", tgt.label, comp, tap.describe(m))
			}
		}
		s := latencySample{total: served.Sub(t0)}
		of := func(typ string) (time.Time, bool) {
			ev, ok := tap.first(m, func(e tapEvent) bool { return e.Type == typ && e.Component == comp })
			return ev.at, ok
		}
		reload, ok1 := of("reload")
		start, ok2 := of("build-start")
		done, ok3 := of("build-ok")
		if !ok1 || !ok2 || !ok3 || start.Before(reload) || done.Before(start) || served.Before(done) {
			s.parts = []latencyPart{{"end to end", s.total, "no clean reload → build-start → build-ok sequence: " + tap.describe(m)}}
			return s
		}
		s.parts = []latencyPart{
			{"debounce+rescan", reload.Sub(t0), fmt.Sprintf("debounce %v", latencyDebounce)},
			{"to build-start", start.Sub(reload), ""},
			{"checkpoint", 0, "none: no checkpoint on this path"},
		}
		// The artifact's mtime splits the build from start and health (which
		// the event stream can't tell apart).
		if fi, err := os.Stat(bin); err == nil && !fi.ModTime().Before(start) && !fi.ModTime().After(done) {
			s.parts = append(s.parts,
				latencyPart{"build", fi.ModTime().Sub(start), ""},
				latencyPart{"start+health", done.Sub(fi.ModTime()), ""})
		} else {
			s.parts = append(s.parts, latencyPart{"build+start+health", done.Sub(start), ""})
		}
		s.parts = append(s.parts, latencyPart{"swap → served", served.Sub(done), fmt.Sprintf("polled every %v", latencyPoll)})
		return s
	}
}

// latencyShape is what examples/counter-go's GET /count prints; a save
// appends a marker field after it.
const latencyShape = `"count":%d`

// installStaticLatency installs R-static at comp on tgt, waits until it is
// served, and returns its index.html's path and bytes.
func installStaticLatency(t *testing.T, tgt latencyTarget, comp string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(tgt.ws, comp)
	copyLatencyTile(t, filepath.Join(repo, "builtin-templates", "agent"), dir)
	// Its files and size, not its backend or its resources: a static tile.
	if err := os.Remove(filepath.Join(dir, "scope.json")); err != nil {
		t.Fatal(err)
	}
	manifest := "{\n  // R-static: the agent template's files, served as a static tile\n  \"runtime\": \"static\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { c, _ := latencyGet(tgt.url + "/c/" + comp + "/"); return c == 200 }, 30*time.Second) {
		t.Fatalf("%s: %s never served", tgt.label, comp)
	}
	index := filepath.Join(dir, "index.html")
	orig, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	return index, orig
}

// installGoLatency installs R-go at comp on tgt under module path module,
// waits (through the cold build) until its GET /count answers, and returns
// backend/main.go's path and bytes.
func installGoLatency(t *testing.T, tgt latencyTarget, comp, module string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(tgt.ws, comp)
	copyLatencyTile(t, filepath.Join(repo, "examples", "counter-go"), dir)
	// Its own module path: the workspace go.work refuses a module twice, and
	// TestGoBackendLifecycle's apps/counter is module counter too.
	gomod := filepath.Join(dir, "go.mod")
	mod, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(mod), "module counter\n", "module "+module+"\n", 1)
	if renamed == string(mod) {
		t.Fatal("examples/counter-go's go.mod no longer says `module counter`: update this test's rename")
	}
	if err := os.WriteFile(gomod, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "backend", "main.go")
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(orig, []byte(latencyShape)) {
		t.Fatalf("examples/counter-go's GET /count no longer prints %s: update this test's save", latencyShape)
	}
	// The first request pays the cold build (a fresh workspace's GOCACHE); a
	// build failure won't mend itself, so it ends the wait.
	latencyWaitCount(t, tgt, tgt.url+"/api/"+comp+"/count", comp)
	return src, orig
}

// latencyWaitCount waits (bounded by a cold build) until url, an R-go tile's
// GET /count, answers.
func latencyWaitCount(t *testing.T, tgt latencyTarget, url, comp string) {
	t.Helper()
	var code int
	var body string
	waitFor(func() bool {
		code, body = latencyGet(url)
		return code == 200 && strings.Contains(body, `"count":`) || strings.Contains(body, "build failed")
	}, 180*time.Second)
	if code != 200 || !strings.Contains(body, `"count":`) {
		t.Fatalf("%s: %s never came up: %d %s", tgt.label, comp, code, body)
	}
}

// ---- the save budgets with live reload on dev (SC-LATENCY-DEFAULT, M2) ----

// latencyAttachDev adds deployment dev to comp on tgt with live reload
// attached to it (11-contract §1.5 add, attach:true), waits for the add to
// finish, and checks that saves now reach dev: the primary is pinned.
func latencyAttachDev(t *testing.T, tgt latencyTarget, comp string) {
	t.Helper()
	a := dlAPI{url: tgt.url}
	a.waitTile(t, comp) // the static plane serves a new directory before a rescan makes it a tile
	ans, e := a.op(t, "add", comp, "deployment", "dev", "attach", true)
	if e.Result != "ok" {
		t.Fatalf("%s: adding dev to %s with live reload attached: %+v", tgt.label, comp, e)
	}
	st := a.state(t, comp)
	if st.LiveReload != "dev" || st.pinned("main") == "" {
		t.Fatalf("%s: after the add live reload is on %q (the answer said %q) and main is pinned to %q", tgt.label, st.LiveReload, ans.State.LiveReload, st.pinned("main"))
	}
}

// devReload matches comp's `deployments` op `reload` for dev: dev's frames
// reload once (11-contract §3.3).
func devReload(comp string) func(tapEvent) bool {
	return func(e tapEvent) bool {
		return e.Type == "deployments" && e.Component == comp && e.dep("reload") == "dev"
	}
}

// devBuild matches comp's `deployments` op `build` of phase phase for dev.
func devBuild(comp, phase string) func(tapEvent) bool {
	return func(e tapEvent) bool {
		var d struct{ Op, Deployment, Phase string }
		return e.Type == "deployments" && e.Component == comp && json.Unmarshal(e.Data, &d) == nil &&
			d.Op == "build" && d.Deployment == "dev" && d.Phase == phase
	}
}

// setupStaticDevLatency is setupStaticLatency with live reload on dev: a
// sample is the write to dev's frame reload, and the pinned primary's frames
// never reload.
func setupStaticDevLatency(t *testing.T, tgt latencyTarget, tap *eventTap) func(*testing.T, int) latencySample {
	const comp = "apps/latency-static-dev"
	index, orig := installStaticLatency(t, tgt, comp)
	latencyAttachDev(t, tgt, comp)
	if !waitFor(func() bool { c, _ := latencyGet(tgt.url + "/c/" + comp + "+dev/"); return c == 200 }, 30*time.Second) {
		t.Fatalf("%s: %s+dev never served", tgt.label, comp)
	}
	return func(t *testing.T, i int) latencySample {
		tap.settle(comp)
		save := append(slices.Clone(orig), fmt.Sprintf("<!-- latency sample %d -->\n", i)...)
		m := tap.mark()
		t0 := time.Now()
		if err := os.WriteFile(index, save, 0o644); err != nil {
			t.Fatal(err)
		}
		ev, ok := tap.next(m, devReload(comp), 10*time.Second)
		if !ok {
			t.Fatalf("%s: no deployments reload of dev for %s within 10 s of a save; events: %s", tgt.label, comp, tap.describe(m))
		}
		if _, bare := tap.first(m, func(e tapEvent) bool { return e.Type == "reload" && e.Component == comp }); bare {
			t.Errorf("%s: a save with live reload on dev reloaded the pinned primary's frames: %s", tgt.label, tap.describe(m))
		}
		watch := ev.at.Sub(t0)
		return latencySample{total: watch, parts: []latencyPart{
			{"debounce+rescan", watch, fmt.Sprintf("debounce %v, rescan and publish ≈%v", latencyDebounce, (watch - latencyDebounce).Round(time.Millisecond))},
			{"checkpoint", 0, "none: dev follows the work tree"},
		}}
	}
}

// setupGoDevLatency is setupGoLatency with live reload on dev: a sample is
// the write to the first 200 from /api/<comp>+dev/count carrying the new
// body, broken down by dev's deployments ops (reload, then build start and
// ok); the pinned primary keeps its code.
func setupGoDevLatency(t *testing.T, tgt latencyTarget, tap *eventTap) func(*testing.T, int) latencySample {
	const comp = "apps/latency-go-dev"
	src, orig := installGoLatency(t, tgt, comp, "latencygodev")
	latencyAttachDev(t, tgt, comp)
	url, bare := tgt.url+"/api/"+comp+"+dev/count", tgt.url+"/api/"+comp+"/count"
	latencyWaitCount(t, tgt, url, comp+"+dev")
	return func(t *testing.T, i int) latencySample {
		tap.settle(comp)
		marker := fmt.Sprintf("d%d-%d", i, time.Now().UnixNano())
		save := bytes.Replace(orig, []byte(latencyShape), []byte(latencyShape+`,"lat":"`+marker+`"`), 1)
		m := tap.mark()
		t0 := time.Now()
		if err := os.WriteFile(src, save, 0o644); err != nil {
			t.Fatal(err)
		}
		var served time.Time
		for deadline := t0.Add(60 * time.Second); ; time.Sleep(latencyPoll) {
			if c, b := latencyGet(url); c == 200 && strings.Contains(b, marker) {
				served = time.Now()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s+dev never served the save within 60 s; events: %s", tgt.label, comp, tap.describe(m))
			}
		}
		if c, b := latencyGet(bare); c != 200 || strings.Contains(b, marker) {
			t.Errorf("%s: with live reload on dev the primary answered %d %q to a save", tgt.label, c, b)
		}
		s := latencySample{total: served.Sub(t0)}
		reload, ok1 := tap.first(m, devReload(comp))
		start, ok2 := tap.first(m, devBuild(comp, "start"))
		done, ok3 := tap.first(m, devBuild(comp, "ok"))
		if !ok1 || !ok2 || !ok3 || start.at.Before(reload.at) || done.at.Before(start.at) || served.Before(done.at) {
			s.parts = []latencyPart{{"end to end", s.total, "no clean reload → build start → build ok sequence of dev: " + tap.describe(m)}}
			return s
		}
		s.parts = []latencyPart{
			{"debounce+rescan", reload.at.Sub(t0), fmt.Sprintf("debounce %v", latencyDebounce)},
			{"to build start", start.at.Sub(reload.at), ""},
			{"checkpoint", 0, "none: dev follows the work tree"},
			{"build+start+health", done.at.Sub(start.at), ""},
			{"swap → served", served.Sub(done.at), fmt.Sprintf("polled every %v", latencyPoll)},
		}
		return s
	}
}

// copyLatencyTile installs a reference tile from the repo, replacing any
// copy a previous run left.
func copyLatencyTile(t *testing.T, from, to string) {
	t.Helper()
	if err := os.RemoveAll(to); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(to) })
}

var latencyClient = &http.Client{Timeout: 10 * time.Second}

func latencyGet(url string) (int, string) {
	r, err := latencyClient.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// startLatencyDaemon starts a second xbind (the baseline binary) on its own
// workspace, the way the shared daemon runs.
func startLatencyDaemon(t *testing.T, bin string) latencyTarget {
	t.Helper()
	lws := filepath.Join(t.TempDir(), "ws")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(bin, "--workspace", lws, "--listen", addr, "--no-auth")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	startDaemon(t, cmd, lws)
	if !waitFor(func() bool { c, _ := latencyGet("http://" + addr + "/healthz"); return c == 200 }, 10*time.Second) {
		t.Fatalf("the baseline xbind (%s) never became healthy: %s", bin, out.String())
	}
	return latencyTarget{"baseline", "http://" + addr, lws}
}

// tapEvent is one /ws/events frame and when it arrived.
type tapEvent struct {
	Type      string          `json:"type"`
	Component string          `json:"component"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data"`
	at        time.Time
}

// dep is a deployments event's data.deployment when its data.op is op, else
// "".
func (e tapEvent) dep(op string) string {
	var d struct{ Op, Deployment string }
	if json.Unmarshal(e.Data, &d) != nil || d.Op != op {
		return ""
	}
	return d.Deployment
}

// eventTap records a target's event stream, timestamping each frame on
// arrival, so a sampler measures to the event, not to when it looked.
type eventTap struct {
	mu     sync.Mutex
	evs    []tapEvent
	notify chan struct{}
}

func tapEvents(t *testing.T, tgt latencyTarget) *eventTap {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(tgt.url, "http")+"/ws/events", nil)
	if err != nil {
		t.Fatalf("%s: /ws/events: %v", tgt.label, err)
	}
	e := &eventTap{notify: make(chan struct{}, 1)}
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			at := time.Now()
			if err != nil {
				return
			}
			var ev tapEvent
			if json.Unmarshal(msg, &ev) != nil {
				continue
			}
			ev.at = at
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

// mark is a cursor: the events after it arrive from now on.
func (e *eventTap) mark() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.evs)
}

// first returns the first event after the cursor that match accepts.
func (e *eventTap) first(from int, match func(tapEvent) bool) (tapEvent, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.evs[from:] {
		if match(ev) {
			return ev, true
		}
	}
	return tapEvent{}, false
}

// next waits up to timeout for first to find one.
func (e *eventTap) next(from int, match func(tapEvent) bool, timeout time.Duration) (tapEvent, bool) {
	deadline := time.After(timeout)
	for {
		if ev, ok := e.first(from, match); ok {
			return ev, true
		}
		select {
		case <-e.notify:
		case <-deadline:
			return tapEvent{}, false
		}
	}
}

// settle waits until neither a reload anywhere (a watcher batch) nor an event
// naming comp has arrived for latencyQuiet, so the tail of the previous
// batch (a setup copy, a go.work rewrite, the old generation's stop) can't
// land in the next sample. Waiting out a quiet period is a bounded negative
// wait; after 30 s of steady events the sample goes ahead anyway.
func (e *eventTap) settle(comp string) {
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		m := e.mark()
		time.Sleep(latencyQuiet) // negative wait: nothing more of the last batch arrives
		if _, busy := e.first(m, func(ev tapEvent) bool { return ev.Type == "reload" || ev.Component == comp }); !busy {
			return
		}
	}
}

// describe lists the events after the cursor, for a failure message.
func (e *eventTap) describe(from int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.evs) == from {
		return "none"
	}
	var parts []string
	for _, ev := range e.evs[from:] {
		s := ev.Type
		if ev.Component != "" {
			s += " " + ev.Component
		}
		if ev.Text != "" {
			s += fmt.Sprintf(" %q", ev.Text)
		}
		if len(ev.Data) > 0 && ev.Type == "deployments" {
			s += " " + string(ev.Data)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// ---- the lifecycle operations (SC-LATENCY-OPS: M1 rows, WP-28; M2 rows, WP-63) ----

// opsBudget is one row of SC-LATENCY-OPS for one reference tile: the p95
// target and the hard bound (0: none but the wait).
type opsBudget struct{ p95, bound time.Duration }

// opsRef is a reference tile for TestLatencyLifecycleOps.
type opsRef struct {
	name, tile string
	builds     bool                          // new code needs a build (R-go): the M2 row "deploy that needs a build"
	install    func(t *testing.T, ws string) // puts the tile at ws/tile
	edit       func(t *testing.T, k int)     // a save making code k
	// serving is the k whose code deployment dep answers now ("" the bare
	// URL, the primary's; else <tile>+dep), -1 when unknown.
	serving func(a dlAPI, dep string) int
	budgets map[string]opsBudget // by operation
}

// opsOrder is every operation TestLatencyLifecycleOps samples, in the order
// it reports them: the M1 rows, then the M2 rows.
var opsOrder = []string{
	"checkpoint", "pause", "reload now", "roll back", "resume",
	"deploy (build)", "deploy (new checkpoint)", "roll back (dev)", "deploy", "reassign → dev", "reassign → main", "promote",
}

// opsKept is the M2 row "deploy, promote or roll back to an existing
// checkpoint whose artifact is kept", and the row "reassign the primary (the
// new primary is healthy, its artifact kept)": static 1 s, backends 2 s, both
// bounded by 10 s. Each is its own entry, so each reports its own numbers.
func opsKept(budgets map[string]opsBudget, p95 time.Duration) map[string]opsBudget {
	for _, op := range []string{"deploy", "promote", "roll back (dev)", "reassign → dev", "reassign → main"} {
		budgets[op] = opsBudget{p95, 10 * time.Second}
	}
	return budgets
}

// covers SC-LATENCY-OPS — from the request to the first request served by
// the new code with every open frame of the deployment reloaded (the bare
// reload for the primary, the deployments op reload for dev; a deploy's
// bookkeeping after its swap is not counted, and a pause, which moves no
// code, counts until its deploy is done), on R-static
// (a daemon of the shared daemon's flavour on its own workspace, which may
// hold deployment state), R-go and R-node (an isolated daemon). The M1 rows:
// a checkpoint (a dry run's capture of an edited work tree; the
// materialization is inside the other rows), pausing live reload, reload
// now, rolling back main to a kept checkpoint, and resume. The M2 rows, with
// a dev deployment beside a pinned main: a deploy of a kept checkpoint to
// dev, rolling dev back to its previous one, promoting dev to main (static
// 1 s, backends 2 s, bound 10 s); reassigning the primary to dev and back,
// each new primary healthy with its artifact kept (the same targets); and on
// R-go a deploy of an edited work tree to dev, which needs a build (the time
// past the build's own within 1.5 s). One cold cycle of each is discarded,
// then five warm ones: every sample meets the hard bound and the median meets
// the p95 target; XBIN_TEST_FULL=1 takes 30 and checks the p95. No baseline
// applies: an older xbind has none of these operations.
func TestLatencyLifecycleOps(t *testing.T) {
	n, _ := latencySamples()
	t.Run("R-static", func(t *testing.T) {
		a, pws := startPlainDaemon(t)
		runOpsLatency(t, a, pws, opsStatic(), n)
	})
	t.Run("backends", func(t *testing.T) {
		d := startIsolatedDaemon(t, isoOpts{})
		t.Run("R-go", func(t *testing.T) { runOpsLatency(t, d.dl(), d.WS, opsGo(), n) })
		t.Run("R-node", func(t *testing.T) { runOpsLatency(t, d.dl(), d.WS, opsNode(), n) })
	})
}

// opsMarker is code k's marker, as each reference tile embeds it; no tile
// path can hold one, so an injected <meta> never reads as a marker.
func opsMarker(k int) string { return fmt.Sprintf("@lat%06d@", k) }

var opsMarkerRe = regexp.MustCompile(`@lat(\d{6})@`)

// opsSeen finds the marker in a body: its k, or -1.
func opsSeen(body string) int {
	m := opsMarkerRe.FindStringSubmatch(body)
	if m == nil {
		return -1
	}
	k, _ := strconv.Atoi(m[1])
	return k
}

// opsAt is a tile's path as a URL names deployment dep: the bare path for
// "", else the qualified <tile>+dep.
func opsAt(tile, dep string) string {
	if dep == "" {
		return tile
	}
	return tile + "+" + dep
}

// opsAgentCopy installs the agent template's files (R-static's size) at dir
// with manifest, as a tile of its own.
func opsAgentCopy(t *testing.T, dir, manifest string) {
	copyLatencyTile(t, filepath.Join(repo, "builtin-templates", "agent"), dir)
	if err := os.Remove(filepath.Join(dir, "scope.json")); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(manifest), 0o644))
}

func opsStatic() *opsRef {
	r := &opsRef{name: "R-static", tile: "apps/lat-ops-static", budgets: opsKept(map[string]opsBudget{
		"checkpoint": {500 * time.Millisecond, 5 * time.Second},
		"pause":      {time.Second, 5 * time.Second},
		"reload now": {1500 * time.Millisecond, 5 * time.Second},
		"roll back":  {time.Second, 10 * time.Second},
		"resume":     {1500 * time.Millisecond, 5 * time.Second},
	}, time.Second)}
	var dir string
	var index []byte
	r.install = func(t *testing.T, ws string) {
		dir = filepath.Join(ws, r.tile)
		opsAgentCopy(t, dir, `{"runtime":"static"}`+"\n")
		var err error
		index, err = os.ReadFile(filepath.Join(dir, "index.html"))
		must(t, err)
	}
	r.edit = func(t *testing.T, k int) {
		must(t, saveIfChanged(filepath.Join(dir, "index.html"), string(index)+"<!-- "+opsMarker(k)+" -->\n"))
	}
	r.serving = func(a dlAPI, dep string) int {
		c, b := a.do("GET", "/c/"+opsAt(r.tile, dep)+"/", "")
		if c != 200 {
			return -1
		}
		return opsSeen(b)
	}
	return r
}

func opsGo() *opsRef {
	r := &opsRef{name: "R-go", tile: "apps/lat-ops-go", builds: true, budgets: opsKept(map[string]opsBudget{
		"checkpoint":     {500 * time.Millisecond, 5 * time.Second},
		"pause":          {3500 * time.Millisecond, 0}, // bound: the confined build's timeout
		"reload now":     {3 * time.Second, 0},
		"roll back":      {2 * time.Second, 10 * time.Second},
		"resume":         {3 * time.Second, 0},
		"deploy (build)": {1500 * time.Millisecond, 0}, // build + 1.5 s: the sample is the time past the build; bound: the build's timeout
	}, 2*time.Second)}
	var dir, src string
	r.install = func(t *testing.T, ws string) {
		dir = filepath.Join(ws, r.tile)
		copyLatencyTile(t, filepath.Join(repo, "examples", "counter-go"), dir)
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		must(t, err)
		renamed := strings.Replace(string(mod), "module counter\n", "module latopsgo\n", 1)
		if renamed == string(mod) {
			t.Fatal("examples/counter-go's go.mod no longer says `module counter`: update this test's rename")
		}
		must(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(renamed), 0o644))
		b, err := os.ReadFile(filepath.Join(dir, "backend", "main.go"))
		must(t, err)
		if src = string(b); !strings.Contains(src, latencyShape) {
			t.Fatalf("examples/counter-go's GET /count no longer prints %s: update this test's edit", latencyShape)
		}
	}
	r.edit = func(t *testing.T, k int) {
		must(t, saveIfChanged(filepath.Join(dir, "backend", "main.go"),
			strings.Replace(src, latencyShape, latencyShape+`,"lat":"`+opsMarker(k)+`"`, 1)))
	}
	r.serving = func(a dlAPI, dep string) int {
		c, b := a.do("GET", "/api/"+opsAt(r.tile, dep)+"/count", "")
		if c != 200 {
			return -1
		}
		return opsSeen(b)
	}
	return r
}

func opsNode() *opsRef {
	r := &opsRef{name: "R-node", tile: "apps/lat-ops-node", budgets: opsKept(map[string]opsBudget{
		"checkpoint": {500 * time.Millisecond, 5 * time.Second},
		"pause":      {1500 * time.Millisecond, 10 * time.Second},
		"reload now": {2 * time.Second, 10 * time.Second},
		"roll back":  {2 * time.Second, 10 * time.Second},
		"resume":     {2 * time.Second, 10 * time.Second},
	}, 2*time.Second)}
	var dir string
	r.install = func(t *testing.T, ws string) {
		dir = filepath.Join(ws, r.tile)
		opsAgentCopy(t, dir, `{"runtime":"node"}`+"\n")
	}
	r.edit = func(t *testing.T, k int) {
		must(t, saveIfChanged(filepath.Join(dir, "backend", "server.js"),
			strings.NewReplacer("__MARKER__", strconv.Quote(opsMarker(k)), "__FAULT__", `""`, "__FILE__", strconv.Quote(probeFile)).Replace(rtNodeSource)))
	}
	r.serving = func(a dlAPI, dep string) int {
		c, b := a.do("GET", "/api/"+opsAt(r.tile, dep)+"/v", "")
		if c != 200 {
			return -1
		}
		return opsSeen(b)
	}
	return r
}

// runOpsLatency takes n warm samples of every operation on r (after one
// cold cycle, discarded): the M1 cycles on the zero state, then the M2
// cycles with dev beside a pinned main, then back to the zero state. It
// checks each sample against its row's bound and each row's median (p95 in
// the full tier) against its target; an operation without a row is
// reported, not judged.
func runOpsLatency(t *testing.T, a dlAPI, ws string, r *opsRef, n int) {
	r.install(t, ws)
	k := 0
	r.edit(t, k)
	if !waitFor(func() bool { return r.serving(a, "") == k }, 3*time.Minute) {
		t.Fatalf("%s never served its first code", r.name)
	}
	a.waitTile(t, r.tile)
	tape := a.tape(t)
	samples := map[string][]time.Duration{}
	take := func(cycle int, got map[string]time.Duration) {
		for op, d := range got {
			if cycle == 0 {
				t.Logf("%s %s, cold (discarded): %v", r.name, op, d.Round(time.Millisecond))
				continue
			}
			samples[op] = append(samples[op], d)
			if b := r.budgets[op]; b.bound > 0 && d > b.bound {
				t.Errorf("%s %s: a sample took %v, past the %v bound", r.name, op, d.Round(time.Millisecond), b.bound)
			}
		}
	}
	for cycle := 0; cycle <= n; cycle++ {
		take(cycle, opsCycle(t, a, tape, r, &k))
	}
	st := opsM2Setup(t, a, r, &k)
	for cycle := 0; cycle <= n; cycle++ {
		take(cycle, opsCycleM2(t, a, tape, r, &k, &st))
	}
	opsM2Teardown(t, a, r)
	full := n >= latencyFullSamples
	for _, op := range opsOrder {
		if len(samples[op]) == 0 {
			continue
		}
		med, p95, worst := latencyStats(samples[op])
		b, judged := r.budgets[op]
		if !judged {
			t.Logf("%s %s: %d warm samples — median %v, p95 %v, max %v; no SC-LATENCY-OPS row for this tile: reported only", r.name, op,
				len(samples[op]), med.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond))
			continue
		}
		t.Logf("%s %s: %d warm samples — median %v, p95 %v, max %v; target p95 %v, bound %v", r.name, op, len(samples[op]),
			med.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond), b.p95, b.bound)
		switch {
		case !full && med > b.p95:
			t.Errorf("%s %s: the median of %d warm samples, %v, misses the %v target", r.name, op, len(samples[op]), med.Round(time.Millisecond), b.p95)
		case full && p95 > b.p95:
			t.Errorf("%s %s: p95 over %d samples, %v, misses the %v target", r.name, op, len(samples[op]), p95.Round(time.Millisecond), b.p95)
		}
	}
}

// opsTiming is one measured operation: its answer, how long it took, and the
// tape's cursor and the clock at its request.
type opsTiming struct {
	ans  dlAnswer
	took time.Duration
	mark int
	t0   time.Time
}

// opsMeasure sends route and times it from the request (the one the capture
// rate let through) to deployment dep of r ("" the bare URL, the primary's)
// serving code want, with its frames' reload when reload is set (the bare
// reload for the bare URL, dep's deployments op reload otherwise), and logs
// the breakdown. When the operation moves the code dep serves, that is where
// it ends: the deploy's bookkeeping after the swap (its log entry, the
// retention it runs) is waited for afterwards, uncounted, and must end ok.
// When it doesn't (a pause pins the code already served), it ends with its
// deploy.
func opsMeasure(t *testing.T, a dlAPI, tape *dlTape, r *opsRef, op, dep string, want int, reload bool, route, body string) opsTiming {
	t.Helper()
	moves := r.serving(a, dep) != want
	m := tape.mark()
	code, ans, raw, t0 := a.postPaced(t, route, body)
	if code != 200 {
		t.Fatalf("%s %s: %d %s", r.name, op, code, raw)
	}
	answered := time.Since(t0)
	pending := ans.Deploy != nil && !ans.Deploy.final()
	var entry time.Duration
	settle := func() {
		t.Helper()
		if e := a.settle(t, r.tile, ans); e.Result != "ok" {
			t.Fatalf("%s %s: the deploy ended %+v", r.name, op, e)
		}
		entry = time.Since(t0)
	}
	if pending && !moves {
		settle()
	}
	for deadline := t0.Add(3 * time.Minute); r.serving(a, dep) != want; time.Sleep(latencyPoll) {
		if time.Now().After(deadline) {
			t.Fatalf("%s %s: code %d never served at %s: %s", r.name, op, want, opsAt(r.tile, dep), tape.describe(m, r.tile))
		}
	}
	served := time.Since(t0) // after the deploy, when it doesn't move the code
	took := served
	note := "no reload expected"
	if reload {
		match := isEvent("reload", r.tile)
		if dep != "" {
			match = isDepReload(r.tile, dep)
		}
		ev, ok := tape.wait(m, match, time.Minute)
		if !ok {
			t.Fatalf("%s %s: no frame reload of %s: %s", r.name, op, opsAt(r.tile, dep), tape.describe(m, r.tile))
		}
		note = fmt.Sprintf("reload %v", ev.at.Sub(t0).Round(time.Millisecond))
		took = max(took, ev.at.Sub(t0))
	}
	done := "deploy done"
	if pending && moves {
		settle()
		done = "deploy done (uncounted)"
	}
	t.Logf("%s %s: %v = answer %v · served %v · %s · %s %v", r.name, op, took.Round(time.Millisecond),
		answered.Round(time.Millisecond), served.Round(time.Millisecond), note, done, entry.Round(time.Millisecond))
	return opsTiming{ans: ans, took: took, mark: m, t0: t0}
}

// isDepReload matches tile's deployments op reload of deployment dep: a
// non-primary deployment's frames reload once (11-contract §3.3).
func isDepReload(tile, dep string) func(dlEvent) bool {
	return func(ev dlEvent) bool {
		var d struct{ Op, Deployment string }
		return ev.Type == "deployments" && ev.Component == tile && json.Unmarshal(ev.Data, &d) == nil &&
			d.Op == "reload" && d.Deployment == dep
	}
}

// opsCycle is one sample of each M1 operation, in the order a person meets
// them: pause (the tile follows its work tree), an edit's checkpoint (a dry
// run), reload now, a rollback to the pause's checkpoint, resume after one
// more edit. *k is the newest code's number, advanced by the edits.
func opsCycle(t *testing.T, a dlAPI, tape *dlTape, r *opsRef, k *int) map[string]time.Duration {
	t.Helper()
	out := map[string]time.Duration{}
	measure := func(op string, want int, reload bool, route, body string) dlAnswer {
		t.Helper()
		tm := opsMeasure(t, a, tape, r, op, "", want, reload, route, body)
		out[op] = tm.took
		return tm.ans
	}
	if a.hasRecord(r.tile) {
		t.Fatalf("%s: a cycle starts in the zero state", r.name)
	}
	cp := *k
	ans := measure("pause", cp, false, "live-reload/pause", dlBody(r.tile))
	if ans.Deploy == nil {
		t.Fatalf("%s: the pause shipped nothing: %+v", r.name, ans)
	}
	paused := ans.Deploy.Checkpoint

	*k++
	r.edit(t, *k)
	code, dry, raw, t0 := a.postPaced(t, "live-reload/now", dlBody(r.tile, "dryRun", true))
	out["checkpoint"] = time.Since(t0)
	if code != 200 || dry.Deploy != nil {
		t.Fatalf("%s: a dry run of reload now: %d %s", r.name, code, raw)
	}
	t.Logf("%s checkpoint (a dry run's capture): %v", r.name, out["checkpoint"].Round(time.Millisecond))

	measure("reload now", *k, true, "live-reload/now", dlBody(r.tile))
	measure("roll back", cp, true, "rollback", dlBody(r.tile, "deployment", "main", "checkpoint", paused))
	*k++
	r.edit(t, *k)
	time.Sleep(2 * latencyDebounce) // negative: the edit reaches nothing while paused
	measure("resume", *k, true, "live-reload/resume", dlBody(r.tile))
	return out
}

// opsM2State is where the M2 cycles stand: the code main and dev run (by
// number), and dev's checkpoint.
type opsM2State struct {
	mainK, devK int
	devCP       string
}

// opsM2Setup takes r from the zero state the M1 cycles end in (main follows
// its work tree at code *k) to the M2 cycles' start: dev added from a
// checkpoint of the work tree, then live reload paused, main pinned to the
// same code.
func opsM2Setup(t *testing.T, a dlAPI, r *opsRef, k *int) opsM2State {
	t.Helper()
	if a.hasRecord(r.tile) {
		t.Fatalf("%s: the M2 cycles start from the zero state", r.name)
	}
	_, e := a.op(t, "add", r.tile, "deployment", "dev")
	if e.Result != "ok" || e.Checkpoint == "" {
		t.Fatalf("%s: adding dev: %+v", r.name, e)
	}
	if !waitFor(func() bool { return r.serving(a, "dev") == *k }, 3*time.Minute) {
		t.Fatalf("%s+dev never served code %d", r.name, *k)
	}
	if _, p := a.op(t, "live-reload/pause", r.tile); p.Result != "ok" {
		t.Fatalf("%s: pausing live reload beside dev: %+v", r.name, p)
	}
	if st := a.state(t, r.tile); st.LiveReload != "" || st.pinned("main") == "" || st.pinned("dev") != e.Checkpoint {
		t.Fatalf("%s: the M2 cycles start with main and dev pinned: live reload %q, main %q, dev %q (added %s)",
			r.name, st.LiveReload, st.pinned("main"), st.pinned("dev"), e.Checkpoint)
	}
	return opsM2State{mainK: *k, devK: *k, devCP: e.Checkpoint}
}

// opsM2Teardown returns r to the zero state, as the M1 cycles leave it, so
// the next reference tile on the same daemon runs beside no deployment of
// this one: dev removed, then live reload resumed onto main, which removes
// the record (11-contract §1.4).
func opsM2Teardown(t *testing.T, a dlAPI, r *opsRef) {
	t.Helper()
	if code, _, raw := a.post(t, "remove", dlBody(r.tile, "deployment", "dev", "confirm", "erase")); code != 200 {
		t.Fatalf("%s: removing dev: %d %s", r.name, code, raw)
	}
	a.op(t, "live-reload/resume", r.tile)
	if a.hasRecord(r.tile) {
		t.Fatalf("%s: removing dev and resuming live reload left a record", r.name)
	}
}

// opsCycleM2 is one sample of each M2 operation, main and dev pinned: a
// deploy of an edited work tree to dev (on R-go one that needs a build; its
// sample is the time past the build's own), a rollback of dev to its
// previous checkpoint, a deploy of the new one again (its artifact kept),
// reassigning the primary to dev and back to main, and promoting dev to main.
// Afterwards main and dev both run the newest code, *k.
func opsCycleM2(t *testing.T, a dlAPI, tape *dlTape, r *opsRef, k *int, st *opsM2State) map[string]time.Duration {
	t.Helper()
	out := map[string]time.Duration{}
	measure := func(op, dep string, want int, route, body string) opsTiming {
		t.Helper()
		tm := opsMeasure(t, a, tape, r, op, dep, want, true, route, body)
		out[op] = tm.took
		return tm
	}

	*k++
	r.edit(t, *k)
	op := "deploy (new checkpoint)" // static and node: a checkpoint and a start, the reload now row's work
	if r.builds {
		op = "deploy (build)"
	}
	tm := measure(op, "dev", *k, "deploy", dlBody(r.tile, "deployment", "dev"))
	if tm.ans.Deploy == nil {
		t.Fatalf("%s: a deploy of new code to dev shipped nothing: %+v", r.name, tm.ans)
	}
	fresh := tm.ans.Deploy.Checkpoint
	if r.builds {
		build, ok := opsBuildTime(tape, tm, r.tile)
		if !ok {
			t.Fatalf("%s %s: the deploy of new code reported no build phase: %s", r.name, op, tape.describe(tm.mark, r.tile))
		}
		out[op] = tm.took - build
		t.Logf("%s %s: %v past the build's %v (total %v)", r.name, op, out[op].Round(time.Millisecond), build.Round(time.Millisecond), tm.took.Round(time.Millisecond))
	}

	measure("roll back (dev)", "dev", st.devK, "rollback", dlBody(r.tile, "deployment", "dev", "checkpoint", st.devCP))
	measure("deploy", "dev", *k, "deploy", dlBody(r.tile, "deployment", "dev", "checkpoint", fresh))

	measure("reassign → dev", "", *k, "primary", dlBody(r.tile, "deployment", "dev", "confirm", "data-stays"))
	opsWaitHealthy(t, a, r, "main", st.mainK)
	measure("reassign → main", "", st.mainK, "primary", dlBody(r.tile, "deployment", "main", "confirm", "data-stays"))
	opsWaitHealthy(t, a, r, "dev", *k)

	measure("promote", "", *k, "promote", dlBody(r.tile, "from", "dev", "to", "main"))
	st.mainK, st.devK, st.devCP = *k, *k, fresh
	return out
}

// opsBuildTime is how long tm's deploy spent building: from its build phase
// to its start phase on the deployments events (the materialization of the
// checkpoint rides the build phase).
func opsBuildTime(tape *dlTape, tm opsTiming, tile string) (time.Duration, bool) {
	var from, to time.Time
	for _, ev := range tape.since(tm.mark) {
		var d struct {
			Op    string `json:"op"`
			ID    int64  `json:"id"`
			Phase string `json:"phase"`
		}
		if ev.Type != "deployments" || ev.Component != tile || json.Unmarshal(ev.Data, &d) != nil || d.Op != "deploy" || d.ID != tm.ans.Deploy.ID {
			continue
		}
		switch {
		case d.Phase == "build" && from.IsZero():
			from = ev.at
		case d.Phase == "start" && !from.IsZero() && to.IsZero():
			to = ev.at
		}
	}
	return to.Sub(from), !from.IsZero() && !to.IsZero()
}

// opsWaitHealthy waits (bounded) until deployment dep of r serves code want
// at its qualified URL, which starts it if it isn't running, and its status
// lets it become the primary again: healthy, for a backend (09-fabric §8).
func opsWaitHealthy(t *testing.T, a dlAPI, r *opsRef, dep string, want int) {
	t.Helper()
	var state string
	if !waitFor(func() bool {
		if r.serving(a, dep) != want {
			return false
		}
		d := a.state(t, r.tile).dep(dep)
		if d == nil {
			return false
		}
		state = d.Status.State
		return state == "healthy" || state == "static"
	}, time.Minute) {
		t.Fatalf("%s+%s never served code %d healthy (state %q)", r.name, dep, want, state)
	}
}
