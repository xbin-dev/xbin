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
	"slices"
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

// covers SC-LATENCY-DEFAULT — static save → frame reload < 500 ms: the file
// write to the tile's `reload` on /ws/events, the watcher's debounce inside
// the budget. The reference tile is R-static: the agent template's files
// (the largest shipped tile) as a static tile.
func TestLatencyStaticSaveToReload(t *testing.T) {
	runLatency(t, latencyCase{
		name:   "static save → frame reload",
		budget: 500 * time.Millisecond,
		setup:  setupStaticLatency,
	})
}

// covers SC-LATENCY-DEFAULT — Go save → new backend serving < 2 s with a warm
// cache: the write to the first 200 carrying the new body, polled every
// 20 ms. The reference tile is R-go: examples/counter-go, copied in.
func TestLatencyGoSaveToServing(t *testing.T) {
	runLatency(t, latencyCase{
		name:   "Go save → new backend serving",
		budget: 2 * time.Second,
		setup:  setupGoLatency,
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

func runLatency(t *testing.T, c latencyCase) {
	n, full := latencyFastSamples, os.Getenv("XBIN_TEST_FULL") == "1"
	if full {
		n = latencyFullSamples
	}
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
	cand := measureLatency(t, c, latencyTarget{"candidate", baseURL, ws}, n)

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
	dir := filepath.Join(tgt.ws, comp)
	copyLatencyTile(t, filepath.Join(repo, "examples", "counter-go"), dir)
	// Its own module path: the workspace go.work refuses a module twice, and
	// TestGoBackendLifecycle's apps/counter is module counter too.
	gomod := filepath.Join(dir, "go.mod")
	mod, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(mod), "module counter\n", "module latencygo\n", 1)
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
	const shape = `"count":%d`
	if !bytes.Contains(orig, []byte(shape)) {
		t.Fatalf("examples/counter-go's GET /count no longer prints %s: update this test's save", shape)
	}
	url := tgt.url + "/api/" + comp + "/count"
	// The first request pays the cold build (a fresh workspace's GOCACHE); a
	// build failure won't mend itself, so it ends the wait.
	var code int
	var body string
	waitFor(func() bool {
		code, body = latencyGet(url)
		return code == 200 && strings.Contains(body, `"count":`) || strings.Contains(body, "build failed")
	}, 180*time.Second)
	if code != 200 || !strings.Contains(body, `"count":`) {
		t.Fatalf("%s: %s never came up: %d %s", tgt.label, comp, code, body)
	}
	bin := filepath.Join(tgt.ws, ".xbin", "build", util.CompKey(comp), "bin")
	return func(t *testing.T, i int) latencySample {
		tap.settle(comp)
		marker := fmt.Sprintf("s%d-%d", i, time.Now().UnixNano())
		save := bytes.Replace(orig, []byte(shape), []byte(shape+`,"lat":"`+marker+`"`), 1)
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
	Type      string `json:"type"`
	Component string `json:"component"`
	Text      string `json:"text"`
	at        time.Time
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
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
