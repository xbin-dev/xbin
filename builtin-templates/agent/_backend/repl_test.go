package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

// newTestSession builds a live session against an in-memory DB.
func newTestSession(t *testing.T, runID int64) (*replSession, *DB) {
	t.Helper()
	db := newTestDB(t)
	reg := newReplRegistry()
	s := reg.get(runID, db, Config{})
	if _, err := s.ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return s, db
}

// run executes one statement the way the tool layer does, without the tool
// layer — gate, exec, and nothing else.
func (s *replSession) testRun(t *testing.T, code string) replResult {
	t.Helper()
	if err := s.acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer s.release()
	return s.exec(context.Background(), "t.js", code, s.timeout)
}

// --- sandbox isolation ---------------------------------------------------

func TestReplNoHostGlobals(t *testing.T) {
	s, _ := newTestSession(t, 1)
	for _, g := range []string{"require", "process", "fetch", "XMLHttpRequest", "setTimeout",
		"setInterval", "Buffer", "WebAssembly", "importScripts", "Worker"} {
		r := s.testRun(t, "typeof "+g)
		if r.err != nil {
			t.Fatalf("typeof %s: %v", g, r.err)
		}
		if got := r.value.String(); got != "undefined" {
			t.Fatalf("host global %q leaked into the sandbox: typeof = %q", g, got)
		}
	}
}

// TestReplSourceMapLoaderDisabled locks goja's one host-filesystem surface.
// Without parser.WithDisableSourceMaps a "//# sourceMappingURL=" comment makes
// the PARSER read that path off the host disk, before execution and before any
// interrupt can apply. The negative control proves the test has teeth: the
// same source against a default parser does reach the loader.
func TestReplSourceMapLoaderDisabled(t *testing.T) {
	s, _ := newTestSession(t, 1)
	src := "var ok = 1; ok\n//# sourceMappingURL=/etc/shadow\n"
	r := s.testRun(t, src)
	if r.err != nil {
		t.Fatalf("hardened VM should ignore the sourceMappingURL comment, got: %v", r.err)
	}
	if r.value.ToInteger() != 1 {
		t.Fatalf("expected 1, got %v", r.value)
	}

	// Negative control: the same source against a VM whose source-map loader is
	// a probe. If the probe is reached, goja really does resolve that comment
	// at parse time — which is exactly what the default (filesystem) loader
	// would have done with the host path.
	var reached string
	vm := goja.New()
	vm.SetParserOptions(parser.WithSourceMapLoader(func(path string) ([]byte, error) {
		reached = path
		return nil, fmt.Errorf("probe")
	}))
	_, _ = vm.RunScript("t.js", src)
	if !strings.Contains(reached, "etc/shadow") {
		t.Fatalf("negative control failed: the probe loader was not reached (got %q), "+
			"so this test would not notice the hardening being removed", reached)
	}
}

func TestReplNoGCObservables(t *testing.T) {
	// WeakRef/FinalizationRegistry expose GC timing, which would make a
	// replayed session diverge from the original in an unfixable way.
	s, _ := newTestSession(t, 1)
	for _, g := range []string{"WeakRef", "FinalizationRegistry"} {
		if got := s.testRun(t, "typeof "+g).value.String(); got != "undefined" {
			t.Fatalf("%s should be removed by the bootstrap, typeof = %q", g, got)
		}
	}
}

func TestReplFilesAreNotHostFiles(t *testing.T) {
	s, db := newTestSession(t, 1)
	if _, err := db.replPutFile(1, "a.txt", "hello", 0); err != nil {
		t.Fatal(err)
	}
	if got := s.testRun(t, `files.read('a.txt')`).value.String(); got != "hello" {
		t.Fatalf("files.read = %q", got)
	}
	// An absolute host path is not addressable at all.
	r := s.testRun(t, `try { files.read('/etc/hostname') } catch (e) { 'rejected: ' + e.message }`)
	if !strings.Contains(r.value.String(), "rejected") {
		t.Fatalf("absolute path should be rejected, got %v", r.value)
	}
}

// --- limits --------------------------------------------------------------

// TestReplTimeoutThenReuse is the important one: it catches the classic goja
// bug where a watchdog firing just as the statement finishes leaves the
// interrupt flag set and kills the NEXT statement instead.
func TestReplTimeoutThenReuse(t *testing.T) {
	s, _ := newTestSession(t, 1)
	s.timeout = 200 * time.Millisecond
	start := time.Now()
	r := s.testRun(t, `while (true) {}`)
	if r.interrupt == nil {
		t.Fatalf("infinite loop should have been interrupted, err=%v", r.err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("interrupt took too long: %v", el)
	}
	// The session must still be usable.
	if got := s.testRun(t, `1 + 1`); got.err != nil || got.value.ToInteger() != 2 {
		t.Fatalf("session unusable after a timeout: err=%v val=%v", got.err, got.value)
	}
}

func TestReplInterruptUncatchable(t *testing.T) {
	s, _ := newTestSession(t, 1)
	s.timeout = 200 * time.Millisecond
	r := s.testRun(t, `try { while (true) {} } catch (e) { 'caught' }`)
	if r.interrupt == nil {
		t.Fatalf("JS must not be able to catch the watchdog interrupt; got value=%v err=%v", r.value, r.err)
	}
}

func TestReplStackOverflowIsNotATimeout(t *testing.T) {
	s, _ := newTestSession(t, 1)
	r := s.testRun(t, `(function f() { return f() })()`)
	if r.err == nil {
		t.Fatal("infinite recursion should error")
	}
	if r.interrupt != nil {
		t.Fatalf("stack overflow must not be reported as an interrupt: %v", r.interrupt)
	}
	if got := s.testRun(t, `40 + 2`); got.err != nil || got.value.ToInteger() != 42 {
		t.Fatalf("session unusable after a stack overflow: %v", got.err)
	}
}

func TestReplStringBombCapped(t *testing.T) {
	s, _ := newTestSession(t, 1)
	// A single 1GB allocation never reaches a watchdog sample point, and Go's
	// OOM is fatal — the bootstrap prelude has to stop it in JS.
	r := s.testRun(t, `try { 'x'.repeat(1e9); 'no-cap' } catch (e) { e.constructor.name }`)
	if got := r.value.String(); got != "RangeError" {
		t.Fatalf("repeat(1e9) should throw RangeError from the prelude, got %q (err=%v)", got, r.err)
	}
	// The cap must not break ordinary use.
	if got := s.testRun(t, `'ab'.repeat(3)`).value.String(); got != "ababab" {
		t.Fatalf("repeat regression: %q", got)
	}
	if got := s.testRun(t, `'7'.padStart(3, '0')`).value.String(); got != "007" {
		t.Fatalf("padStart regression: %q", got)
	}
}

func TestReplOutputCapped(t *testing.T) {
	s, _ := newTestSession(t, 1)
	s.timeout = 3 * time.Second
	r := s.testRun(t, `for (var i = 0; i < 200000; i++) console.log('spam line ' + i); 'done'`)
	if len(r.output) > replMaxOutBytes*2 {
		t.Fatalf("output not capped: %d bytes", len(r.output))
	}
	if !strings.Contains(r.output, "suppressed") {
		t.Fatalf("expected a suppression notice, got tail: %q", tail(r.output, 120))
	}
}

// --- concurrency ---------------------------------------------------------

func TestReplSerializedEvals(t *testing.T) {
	s, _ := newTestSession(t, 1)
	if r := s.testRun(t, `var n = 0`); r.err != nil {
		t.Fatal(r.err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.acquire(context.Background()); err != nil {
				return
			}
			defer s.release()
			s.exec(context.Background(), "t.js", `n = n + 1`, s.timeout)
		}()
	}
	wg.Wait()
	if got := s.testRun(t, `n`).value.ToInteger(); got != 6 {
		t.Fatalf("6 concurrent increments should serialize to 6, got %d", got)
	}
}

// --- replay --------------------------------------------------------------

// replay simulates a backend swap: drop the live VM and rebuild from the log.
func replay(t *testing.T, db *DB, runID int64) (*replSession, string) {
	t.Helper()
	reg := newReplRegistry()
	s := reg.get(runID, db, Config{})
	banner, err := s.ensure()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return s, banner
}

// logged runs a statement AND records it, the way the tool layer does.
func (s *replSession) logged(t *testing.T, code string) replResult {
	t.Helper()
	seq, created, err := s.db.replAppend(s.runID, "eval", code)
	if err != nil {
		t.Fatal(err)
	}
	prev := s.nowFn
	s.nowFn = func() time.Time { return time.UnixMilli(created) }
	r := s.testRun(t, code)
	s.nowFn = prev
	switch {
	case r.interrupt != nil || r.stuck:
		s.db.replDrop(s.runID, seq)
	case r.err != nil:
		s.db.replFinish(s.runID, seq, replThrew, r.ms)
	default:
		s.db.replFinish(s.runID, seq, replOK, r.ms)
	}
	return r
}

func TestReplRebuildFromLog(t *testing.T) {
	s, db := newTestSession(t, 7)
	s.logged(t, `var x = 1`)
	s.logged(t, `x = x + 41`)

	s2, banner := replay(t, db, 7)
	if got := s2.testRun(t, `x`).value.ToInteger(); got != 42 {
		t.Fatalf("state did not survive the rebuild: x = %d", got)
	}
	if !strings.Contains(banner, "replayed 2") {
		t.Fatalf("banner should report the replay, got %q", banner)
	}
}

func TestReplReplayIsDeterministic(t *testing.T) {
	s, db := newTestSession(t, 9)
	s.logged(t, `var r = Math.random(); var t0 = Date.now()`)
	before := s.testRun(t, `r + ':' + t0`).value.String()

	s2, _ := replay(t, db, 9)
	after := s2.testRun(t, `r + ':' + t0`).value.String()
	if before != after {
		t.Fatalf("replay must reproduce Math.random and Date exactly:\n before=%s\n after =%s", before, after)
	}
}

func TestReplReplayDrift(t *testing.T) {
	s, db := newTestSession(t, 11)
	if _, err := db.replPutFile(11, "h.js", `var greet = function () { return 'hi' }`, 0); err != nil {
		t.Fatal(err)
	}
	seq, _, _ := db.replAppend(11, "load", "h.js")
	s.testRun(t, `load('h.js')`)
	db.replFinish(11, seq, replOK, 1)
	s.logged(t, `var used = greet()`)

	// The human (or the model) edits the file so the later statement breaks.
	if _, err := db.replPutFile(11, "h.js", `var somethingElse = 1`, 0); err != nil {
		t.Fatal(err)
	}
	s2, banner := replay(t, db, 11)
	if !strings.Contains(banner, "STOPPED") {
		t.Fatalf("drift should stop the replay and say so, got %q", banner)
	}
	// Whatever ran before the drift is intact, and the session still works.
	if got := s2.testRun(t, `1 + 1`); got.err != nil {
		t.Fatalf("session unusable after drift: %v", got.err)
	}
}

// TestReplKilledStatementIsNotReplayed covers the crash-loop guard: a
// statement that took the process down leaves a 'running' row, and replaying
// it would kill the process again on every single rebuild.
func TestReplKilledStatementIsNotReplayed(t *testing.T) {
	db := newTestDB(t)
	// A statement that started but never finished — exactly what an OOM leaves.
	if _, _, err := db.replAppend(5, "eval", `var boom = 1`); err != nil {
		t.Fatal(err)
	}
	s, banner := replay(t, db, 5)
	if !strings.Contains(banner, "crashed") {
		t.Fatalf("banner should report the discarded statement, got %q", banner)
	}
	if got := s.testRun(t, `typeof boom`).value.String(); got != "undefined" {
		t.Fatal("the killer statement must not be replayed")
	}
	if db.replKilledCount(5) != 1 {
		t.Fatalf("expected 1 killed row, got %d", db.replKilledCount(5))
	}

	// A second crash disables the sandbox for this run rather than letting it
	// walk the backend into the 3-crash 'failed' state.
	if _, _, err := db.replAppend(5, "eval", `var boom2 = 1`); err != nil {
		t.Fatal(err)
	}
	reg := newReplRegistry()
	if _, err := reg.get(5, db, Config{}).ensure(); err == nil {
		t.Fatal("a second killed statement should disable the sandbox for this run")
	}
}

func TestReplPruneKeepsLoads(t *testing.T) {
	db := newTestDB(t)
	seq, _, _ := db.replAppend(3, "load", "setup.js")
	db.replFinish(3, seq, replOK, 1)
	for i := 0; i < 30; i++ {
		s, _, _ := db.replAppend(3, "eval", fmt.Sprintf("var v%d = %d", i, i))
		db.replFinish(3, s, replOK, 1)
	}
	if dropped := db.replPrune(3, 10, 1<<20); dropped != 20 {
		t.Fatalf("expected 20 dropped, got %d", dropped)
	}
	entries, err := db.replayable(3)
	if err != nil {
		t.Fatal(err)
	}
	var loads int
	for _, e := range entries {
		if e.Kind == "load" {
			loads++
		}
	}
	if loads != 1 {
		t.Fatalf("pruning must keep load rows (files are the stable substrate), got %d", loads)
	}
}

// --- files, from inside the VM ------------------------------------------

func TestReplLoadIdempotent(t *testing.T) {
	s, db := newTestSession(t, 1)
	if _, err := db.replPutFile(1, "c.js", `const K = 1; var seen = (typeof seen === 'undefined' ? 1 : seen + 1)`, 0); err != nil {
		t.Fatal(err)
	}
	if r := s.testRun(t, `load('c.js'); seen`); r.err != nil || r.value.ToInteger() != 1 {
		t.Fatalf("first load: %v %v", r.value, r.err)
	}
	// A second plain load is a no-op — a const redeclaration would throw.
	if r := s.testRun(t, `load('c.js'); seen`); r.err != nil || r.value.ToInteger() != 1 {
		t.Fatalf("second load should be a no-op: %v %v", r.value, r.err)
	}
	// force:true opts into re-running, which surfaces the const collision —
	// the sharp edge the tool description warns about.
	if r := s.testRun(t, `load('c.js', {force:true})`); r.err == nil {
		t.Fatal("forcing a reload of a const-declaring file should throw")
	}
}

func TestReplFilesWriteFromInsideVM(t *testing.T) {
	s, db := newTestSession(t, 1)
	// The "compute, then emit a document" path the render tool depends on.
	r := s.testRun(t, `files.write('out.html', '<h1>' + (6*7) + '</h1>').version`)
	if r.err != nil || r.value.ToInteger() != 1 {
		t.Fatalf("files.write: %v %v", r.value, r.err)
	}
	f, err := db.replFile(1, "out.html")
	if err != nil || f.Content != "<h1>42</h1>" {
		t.Fatalf("file content = %q (%v)", f.Content, err)
	}
}

// --- formatting ----------------------------------------------------------

func TestReplInspectCycles(t *testing.T) {
	s, _ := newTestSession(t, 1)
	r := s.testRun(t, `var a = {n: 1}; a.self = a; a`)
	if got := s.inspectVal(r.value); !strings.Contains(got, "Circular") {
		t.Fatalf("cycle should render as [Circular *1], got %q", got)
	}
}

func TestReplInspectSurvivesTampering(t *testing.T) {
	// The formatter captures its intrinsics before any model code runs, so
	// overwriting them later must not break (or hijack) formatting.
	s, _ := newTestSession(t, 1)
	s.testRun(t, `Object.keys = function () { throw new Error('gotcha') };
	              Array.isArray = function () { return true };
	              JSON.stringify = function () { throw new Error('gotcha') }`)
	r := s.testRun(t, `({a: 1, b: 'two'})`)
	got := s.inspectVal(r.value)
	if !strings.Contains(got, "a: 1") || !strings.Contains(got, "two") {
		t.Fatalf("formatting broke after intrinsic tampering: %q", got)
	}
}

func TestReplInspectDoesNotInvokeGetters(t *testing.T) {
	s, _ := newTestSession(t, 1)
	r := s.testRun(t, `({ get boom() { throw new Error('invoked') } })`)
	if got := s.inspectVal(r.value); !strings.Contains(got, "[Getter]") {
		t.Fatalf("a getter must be reported, never invoked; got %q", got)
	}
}

func TestReplInspectErrors(t *testing.T) {
	s, _ := newTestSession(t, 1)
	r := s.testRun(t, `null.x`)
	if r.err == nil {
		t.Fatal("expected a TypeError")
	}
	if got := replErrorText(s, r); !strings.Contains(got, "TypeError") {
		t.Fatalf("error text should name the type, got %q", got)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// --- wiring --------------------------------------------------------------

func TestReplToolGatingAndLanes(t *testing.T) {
	has := func(specs []toolSpec, name string) bool {
		for _, s := range specs {
			if s.Function.Name == name {
				return true
			}
		}
		return false
	}
	// Default-on, like every other feature key (cfg.feature returns true for
	// absent keys so existing runs pick new capabilities up).
	for _, lane := range []string{"", "web"} {
		specs := toolSpecs(Config{Toolset: lane}, 0, nil)
		for name := range replToolNames {
			if !has(specs, name) {
				t.Fatalf("toolset %q: %s missing — the sandbox has no egress and no internal "+
					"reach, so it belongs in both lanes", lane, name)
			}
		}
	}
	// And the feature key turns the whole family off.
	off := toolSpecs(Config{Features: map[string]bool{"repl": false}}, 0, nil)
	for name := range replToolNames {
		if has(off, name) {
			t.Fatalf("repl:false should hide %s", name)
		}
	}
}

func TestReplToolsAreNotSideEffecting(t *testing.T) {
	// The approval gate exists for tools that touch the world. These touch one
	// run's private sqlite rows; pausing a turn for them would be pure friction.
	for name := range replToolNames {
		if sideEffect(name) {
			t.Fatalf("%s should not require approval", name)
		}
	}
}

func TestReplDeleteRunCascade(t *testing.T) {
	// deleteRun enumerates its tables by hand, so a new per-run table is easy
	// to forget and would leak rows forever.
	db := newTestDB(t)
	id, err := db.createRun("t", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.replPutFile(id, "a.js", "x", 0); err != nil {
		t.Fatal(err)
	}
	seq, _, _ := db.replAppend(id, "eval", "1")
	db.replFinish(id, seq, replOK, 1)

	if err := db.deleteRun(id); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM repl_files WHERE run_id=?`,
		`SELECT count(*) FROM repl_log WHERE run_id=?`,
	} {
		var n int
		if err := db.sql.QueryRow(q, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s left %d row(s) behind", q, n)
		}
	}
}

// TestSandboxWorkflow walks the path the whole feature exists for: compute in
// the VM, emit a document from the computed values, then render it. The frame
// is static, so a chart has to be inline SVG the sandbox generated itself —
// this is the workflow the tool descriptions point the model at.
func TestSandboxWorkflow(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	call := func(name string, args map[string]any) string {
		out, err := ag.runTool(context.Background(), run, Config{}, name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	// A helper the agent writes once and reuses.
	call("file_write", map[string]any{
		"path": "chart.js",
		"content": "var bar = function (label, v, max) {\n" +
			"  var w = Math.round(300 * v / max);\n" +
			"  return '<g><rect width=\"' + w + '\" height=\"18\" fill=\"#4a7\"/>' +\n" +
			"         '<text x=\"' + (w + 6) + '\" y=\"14\">' + label + ' ' + v + '</text></g>';\n" +
			"};\n",
	})
	call("js_run", map[string]any{"path": "chart.js"})

	// Compute, then emit a document from the computed values.
	out := call("js_eval", map[string]any{
		"code": "var data = [['alpha', 12], ['beta', 30], ['gamma', 7]];\n" +
			"var max = data.reduce(function (m, d) { return Math.max(m, d[1]) }, 0);\n" +
			"var rows = data.map(function (d, i) {\n" +
			"  return '<g transform=\"translate(0,' + (i * 24) + ')\">' + bar(d[0], d[1], max) + '</g>';\n" +
			"}).join('');\n" +
			"files.write('report.html', '<!doctype html><html><body><h1>Totals</h1>' +\n" +
			"  '<svg width=\"420\" height=\"80\" font-size=\"12\">' + rows + '</svg></body></html>');\n" +
			"data.reduce(function (s, d) { return s + d[1] }, 0)",
	})
	if !strings.Contains(out, "=> 49") {
		t.Fatalf("expected the computed total in the result, got:\n%s", out)
	}

	f, err := db.replFile(id, "report.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Content, "<svg") || !strings.Contains(f.Content, "beta 30") {
		t.Fatalf("generated document looks wrong:\n%s", f.Content)
	}
	if out := call("render_html", map[string]any{"path": "report.html"}); !strings.Contains(out, "report.html") {
		t.Fatalf("render_html: %s", out)
	}

	// And it all survives a backend swap: drop the live VM, and the helper the
	// agent defined is still callable because js_run was logged.
	ag.repl.drop(id)
	out = call("js_eval", map[string]any{"code": "typeof bar"})
	if !strings.Contains(out, "function") {
		t.Fatalf("the loaded helper should survive a rebuild, got:\n%s", out)
	}
	if !strings.Contains(out, "rebuilt") {
		t.Fatalf("the rebuild should be announced to the model, got:\n%s", out)
	}
}
