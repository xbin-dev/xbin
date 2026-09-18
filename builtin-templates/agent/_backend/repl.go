// repl.go — the live half of the REPL: a goja VM per run, its watchdog, and
// the rebuild-from-log that makes a session look like it survived a backend
// swap. Durable state is in repl_store.go; the tool surface is repl_tools.go.
//
// Sandbox stance: goja.New() has NO host surface at all — no require, process,
// fetch, setTimeout, console, Buffer or WebAssembly (asserted by a test). We
// add exactly three things: console.*, files.* (this run's sqlite rows), and
// load(). We never add a network or xbin bridge: that would hand a private-lane
// run an egress channel and silently defeat the toolset firewall in tools.go.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/rand"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

//go:embed repl_bootstrap.js
var replBootstrapJS string

const (
	maxReplSessions   = 8                // live VMs; eviction is free (replay rebuilds)
	replIdleTTL       = 15 * time.Minute // janitor sweep threshold
	replGraceSlack    = 3 * time.Second  // beyond the JS budget before quarantine
	replMaxStack      = 2000
	replMaxOutBytes   = 8 << 10
	replMaxOutLine    = 2 << 10
	replMaxOutLines   = 200
	replReplayBudget  = 5 * time.Second
	maxReplLogEntries = 200
	maxReplLogBytes   = 256 << 10
	maxReplKilled     = 2 // killed statements before we refuse to run more
	maxReplQuarantine = 4 // leaked VMs before we refuse to run more
)

var (
	errReplTimeout   = errors.New("sandbox: execution exceeded the time budget")
	errReplMemory    = errors.New("sandbox: execution exceeded the memory budget")
	errReplCancelled = errors.New("sandbox: cancelled")
	errReplOutput    = errors.New("sandbox: produced too much output")
)

// --- output capture -----------------------------------------------------

type replOutput struct {
	mu      sync.Mutex
	b       strings.Builder
	bytes   int
	lines   int
	dropped int
	over    bool // past the hard multiple — the watchdog should interrupt
}

func (o *replOutput) write(level, s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(s) > replMaxOutLine {
		s = s[:replMaxOutLine] + "…(line truncated)"
	}
	if level == "warn" || level == "error" {
		s = level + ": " + s
	}
	o.lines++
	// Count first, append second: a loop that logs forever must be detectable
	// even though we stopped storing it.
	o.bytes += len(s) + 1
	if o.bytes > replMaxOutBytes*10 {
		o.over = true
	}
	if o.bytes > replMaxOutBytes || o.lines > replMaxOutLines {
		o.dropped++
		return
	}
	o.b.WriteString(s)
	o.b.WriteByte('\n')
}

func (o *replOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.b.String()
	if o.dropped > 0 {
		s += fmt.Sprintf("…[%d more output line(s) suppressed]\n", o.dropped)
	}
	return s
}

func (o *replOutput) overflowed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.over
}

// --- session ------------------------------------------------------------

type replSession struct {
	runID int64
	// gate is a 1-slot channel used as a mutex. A channel rather than
	// sync.Mutex so a waiter can abandon on the per-tool context instead of
	// blocking past the tool timeout — a goja Runtime is not goroutine-safe
	// and runToolBatch runs up to maxParallelTools calls concurrently.
	gate    chan struct{}
	vm      *goja.Runtime
	inspect goja.Callable
	out     *replOutput
	rng     *rand.Rand
	nowFn   func() time.Time
	loaded  map[string]bool
	stmt    int // statements executed in this session's lifetime
	lastUse time.Time

	db         *DB
	timeout    time.Duration
	memCap     uint64
	rejections []string
}

type replRegistry struct {
	mu         sync.Mutex
	byID       map[int64]*replSession
	quarantine int
}

func newReplRegistry() *replRegistry { return &replRegistry{byID: map[int64]*replSession{}} }

func (r *replRegistry) get(runID int64, db *DB, cfg Config) *replSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.byID[runID]; ok {
		s.lastUse = time.Now()
		s.timeout = cfg.replTimeout()
		s.memCap = uint64(cfg.replMemMB()) << 20
		return s
	}
	if len(r.byID) >= maxReplSessions {
		var oldestID int64
		var oldest time.Time
		for id, s := range r.byID {
			if oldest.IsZero() || s.lastUse.Before(oldest) {
				oldestID, oldest = id, s.lastUse
			}
		}
		delete(r.byID, oldestID)
	}
	s := &replSession{
		runID: runID, gate: make(chan struct{}, 1), db: db,
		loaded: map[string]bool{}, lastUse: time.Now(),
		timeout: cfg.replTimeout(), memCap: uint64(cfg.replMemMB()) << 20,
	}
	r.byID[runID] = s
	return s
}

func (r *replRegistry) drop(runID int64) {
	r.mu.Lock()
	delete(r.byID, runID)
	r.mu.Unlock()
}

// quarantined drops a session whose VM is stuck in an uninterruptible native
// call (regexp2 backtracking has no timeout and no interrupt check). The
// orphan goroutine keeps the dead VM alive forever; we count the leaks and
// stop serving once there are too many, because a leak is cheaper than a
// deadlocked run but not free.
func (r *replRegistry) quarantined(runID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, runID)
	r.quarantine++
	return r.quarantine
}

func (r *replRegistry) leaks() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quarantine
}

func (r *replRegistry) sweep() {
	cut := time.Now().Add(-replIdleTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.byID {
		if s.lastUse.Before(cut) {
			delete(r.byID, id)
		}
	}
}

func (s *replSession) acquire(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		s.lastUse = time.Now()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("another sandbox statement for this run is still executing")
	}
}

func (s *replSession) release() { <-s.gate }

// --- VM construction ----------------------------------------------------

func (s *replSession) newVM() (*goja.Runtime, error) {
	vm := goja.New()
	// goja's one host-filesystem surface is the parser's DEFAULT source-map
	// loader: a "//# sourceMappingURL=/some/path" comment in model source
	// causes a host file read at PARSE time, before execution and before any
	// interrupt can apply. Disabling it is mandatory. Note this covers
	// RunScript/eval/new Function because they all pass parserOptions —
	// package-level goja.Compile does NOT, so never use it here.
	vm.SetParserOptions(parser.WithDisableSourceMaps)
	// goja's default is effectively unlimited; without this, deep recursion
	// blows the Go stack (fatal, unrecoverable) instead of throwing.
	vm.SetMaxCallStackSize(replMaxStack)
	// Date and Math.random are pinned so a replayed session reproduces the
	// original exactly: the PRNG is re-seeded from run.ID on every rebuild,
	// and nowFn is set per replayed entry to that statement's own timestamp.
	vm.SetTimeSource(func() time.Time { return s.nowFn() })
	vm.SetRandSource(func() float64 { return s.rng.Float64() })
	vm.SetPromiseRejectionTracker(func(p *goja.Promise, op goja.PromiseRejectionOperation) {
		if op != goja.PromiseRejectionReject {
			return
		}
		s.rejections = append(s.rejections, s.inspectVal(p.Result()))
	})

	con := vm.NewObject()
	for _, lvl := range []string{"log", "info", "debug", "warn", "error"} {
		level := lvl
		_ = con.Set(level, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				if a != nil && a.ExportType() != nil && a.ExportType().Kind().String() == "string" {
					parts = append(parts, a.String())
				} else {
					parts = append(parts, s.inspectVal(a))
				}
			}
			s.out.write(level, strings.Join(parts, " "))
			return goja.Undefined()
		})
	}
	if err := vm.Set("console", con); err != nil {
		return nil, err
	}

	files := vm.NewObject()
	_ = files.Set("read", s.jsFileRead)
	_ = files.Set("write", s.jsFileWrite)
	_ = files.Set("list", s.jsFileList)
	_ = files.Set("exists", s.jsFileExists)
	_ = files.Set("remove", s.jsFileRemove)
	if err := vm.Set("files", files); err != nil {
		return nil, err
	}
	if err := vm.Set("load", s.jsLoad); err != nil {
		return nil, err
	}

	s.vm = vm // jsLoad and the console binding re-enter through s.vm
	v, err := vm.RunScript("bootstrap.js", replBootstrapJS)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return nil, fmt.Errorf("bootstrap did not return an inspect function")
	}
	s.inspect = fn
	return vm, nil
}

// inspectVal formats a value with the bootstrap's inspector, falling back to
// goja's own String() if the inspector itself is unavailable or fails (e.g.
// during an interrupt, when any further JS call also aborts).
func (s *replSession) inspectVal(v goja.Value) string {
	if v == nil {
		return "undefined"
	}
	if s.inspect == nil {
		return v.String()
	}
	out, err := s.inspect(goja.Undefined(), v)
	if err != nil || out == nil {
		return v.String()
	}
	return out.String()
}

// --- in-VM file API -----------------------------------------------------

func (s *replSession) throw(err error) {
	panic(s.vm.NewGoError(err))
}

func (s *replSession) argPath(call goja.FunctionCall, i int) string {
	p, err := normReplPath(call.Argument(i).String())
	if err != nil {
		s.throw(err)
	}
	return p
}

func (s *replSession) jsFileRead(call goja.FunctionCall) goja.Value {
	f, err := s.db.replFile(s.runID, s.argPath(call, 0))
	if err != nil {
		s.throw(fmt.Errorf("files.read: %w", err))
	}
	return s.vm.ToValue(f.Content)
}

func (s *replSession) jsFileWrite(call goja.FunctionCall) goja.Value {
	path := s.argPath(call, 0)
	body := call.Argument(1)
	if goja.IsUndefined(body) || goja.IsNull(body) {
		s.throw(fmt.Errorf("files.write: content is required"))
	}
	f, err := s.db.replPutFile(s.runID, path, body.String(), 0)
	if err != nil {
		s.throw(fmt.Errorf("files.write: %w", err))
	}
	o := s.vm.NewObject()
	_ = o.Set("path", f.Path)
	_ = o.Set("bytes", f.Bytes)
	_ = o.Set("version", f.Version)
	return o
}

func (s *replSession) jsFileList(goja.FunctionCall) goja.Value {
	files, err := s.db.replFiles(s.runID)
	if err != nil {
		s.throw(fmt.Errorf("files.list: %w", err))
	}
	out := make([]any, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]any{"path": f.Path, "bytes": f.Bytes, "version": f.Version})
	}
	return s.vm.ToValue(out)
}

func (s *replSession) jsFileExists(call goja.FunctionCall) goja.Value {
	_, err := s.db.replFile(s.runID, s.argPath(call, 0))
	return s.vm.ToValue(err == nil)
}

func (s *replSession) jsFileRemove(call goja.FunctionCall) goja.Value {
	path := s.argPath(call, 0)
	if err := s.db.replDeleteFile(s.runID, path); err != nil {
		s.throw(fmt.Errorf("files.remove: %w", err))
	}
	delete(s.loaded, path)
	return goja.Undefined()
}

// jsLoad evaluates a session file in GLOBAL scope by re-entering the VM from a
// native call. Idempotent by default because `let`/`const` at a file's top
// level land in the shared global lexical environment, so a second load would
// throw "Identifier already declared" — {force:true} opts into that.
func (s *replSession) jsLoad(call goja.FunctionCall) goja.Value {
	path := s.argPath(call, 0)
	force := false
	if o, ok := call.Argument(1).(*goja.Object); ok && o != nil {
		if fv := o.Get("force"); fv != nil {
			force = fv.ToBoolean()
		}
	}
	if s.loaded[path] && !force {
		return goja.Undefined()
	}
	f, err := s.db.replFile(s.runID, path)
	if err != nil {
		s.throw(fmt.Errorf("load: %w", err))
	}
	v, err := s.vm.RunScript("file:"+path, f.Content)
	if err != nil {
		var ex *goja.Exception
		if errors.As(err, &ex) {
			panic(ex.Value()) // rethrow the JS error unchanged
		}
		s.throw(err)
	}
	s.loaded[path] = true
	return v
}

// --- execution ----------------------------------------------------------

type replResult struct {
	value      goja.Value
	output     string
	err        error // JS exception, interrupt, or overflow
	interrupt  error // non-nil when the watchdog stopped it
	stuck      bool  // did not return even after the grace period
	ms         int
	rejections []string
}

// exec runs one statement. The caller must already hold the session gate.
func (s *replSession) exec(ctx context.Context, name, code string, budget time.Duration) replResult {
	s.out = &replOutput{}
	s.rejections = nil
	start := time.Now()

	type outcome struct {
		v   goja.Value
		err error
	}
	done := make(chan outcome, 1)
	stop := make(chan struct{})
	wdDone := make(chan struct{})

	go s.watchdog(ctx, budget, stop, wdDone)
	go func() {
		defer func() {
			// A panic from a native binding (s.throw) surfaces as a JS
			// exception via goja; anything else would take the process down,
			// so convert it into an error for this statement only.
			if r := recover(); r != nil {
				if ex, ok := r.(*goja.Exception); ok {
					done <- outcome{nil, ex}
					return
				}
				done <- outcome{nil, fmt.Errorf("sandbox panic: %v", r)}
			}
		}()
		v, err := s.vm.RunScript(name, code)
		done <- outcome{v, err}
	}()

	var res replResult
	select {
	case o := <-done:
		close(stop)
		<-wdDone // rendezvous: the watchdog is provably gone before we clear
		s.vm.ClearInterrupt()
		res.value, res.err = o.v, o.err
	case <-time.After(budget + replGraceSlack):
		// Interrupt was ignored — we are inside a native call that does not
		// check it. Abandon the VM; do NOT ClearInterrupt (wrong goroutine).
		res.stuck = true
		res.err = errReplTimeout
	}
	res.ms = int(time.Since(start).Milliseconds())
	res.output = s.out.String()
	res.rejections = s.rejections

	var ie *goja.InterruptedError
	if res.err != nil && errors.As(res.err, &ie) {
		if cause, ok := ie.Value().(error); ok {
			res.interrupt = cause
		} else {
			res.interrupt = errReplTimeout
		}
	}
	if res.stuck {
		res.interrupt = errReplTimeout
	}
	return res
}

// watchdog enforces wall clock and memory. It must always close wdDone, and
// exec must always wait for that before calling ClearInterrupt: otherwise a
// watchdog firing between "check stop" and "return" leaves the interrupt flag
// set and kills the NEXT statement instead.
func (s *replSession) watchdog(ctx context.Context, budget time.Duration, stop <-chan struct{}, wdDone chan<- struct{}) {
	defer close(wdDone)
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()

	var sample [1]metrics.Sample
	// Not runtime.ReadMemStats: that stops the world, and at 40Hz on a process
	// that is also streaming LLM tokens the pause cost is real. runtime/metrics
	// takes a per-P lock-free snapshot. "heap/objects" counts live plus
	// not-yet-swept garbage, so a runaway allocation loop shows up immediately
	// rather than waiting for the next GC.
	sample[0].Name = "/memory/classes/heap/objects:bytes"
	metrics.Read(sample[:])
	base := sample[0].Value.Uint64()
	breaches := 0

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			s.vm.Interrupt(errReplCancelled)
			return
		case <-deadline.C:
			s.vm.Interrupt(errReplTimeout)
			return
		case <-tick.C:
			if s.out.overflowed() {
				s.vm.Interrupt(errReplOutput)
				return
			}
			metrics.Read(sample[:])
			cur := sample[0].Value.Uint64()
			// Three guards against tripping on someone else's allocation (an
			// LLM response, a sqlite read): an absolute floor, a ratio, and
			// two consecutive breaching samples.
			if cur > base+s.memCap && cur > 64<<20 && cur > base+base/2 {
				breaches++
				if breaches >= 2 {
					s.vm.Interrupt(errReplMemory)
					return
				}
			} else {
				breaches = 0
			}
		}
	}
}

// --- build / replay -----------------------------------------------------

// ensure brings the session up if its VM is cold, replaying the log. Returns a
// banner to prefix to the next tool result (empty when nothing notable
// happened), or an error if the session must not be used at all.
func (s *replSession) ensure() (string, error) {
	if s.vm != nil {
		return "", nil
	}
	// Any row still 'running' is a statement that took the process down with
	// it. Mark it killed so it is never replayed — otherwise every rebuild
	// re-runs the bomb and walks the backend into the 3-crash failed state.
	killedNow := s.db.replMarkKilled(s.runID)
	killedTotal := s.db.replKilledCount(s.runID)
	if killedTotal >= maxReplKilled {
		return "", fmt.Errorf("this run's sandbox has crashed the backend %d times "+
			"(a statement allocated more memory than the process could survive). "+
			"Sandbox execution is disabled for this run — call js_reset to start clean, "+
			"and work in smaller steps", killedTotal)
	}

	s.rng = rand.New(rand.NewSource(s.runID))
	s.nowFn = time.Now
	s.out = &replOutput{}
	s.loaded = map[string]bool{}
	s.stmt = 0
	if _, err := s.newVM(); err != nil {
		s.vm = nil
		return "", err
	}

	entries, err := s.db.replayable(s.runID)
	if err != nil || len(entries) == 0 {
		if killedNow > 0 {
			return fmt.Sprintf("[sandbox rebuilt: the previous statement crashed the backend and was discarded "+
				"(%d/%d). Avoid allocating huge strings or arrays.]", killedTotal, maxReplKilled), nil
		}
		return "", nil
	}

	t0, applied, stopped := time.Now(), 0, ""
	for i, e := range entries {
		if time.Since(t0) > replReplayBudget {
			stopped = "replay budget exhausted"
			break
		}
		code, name := e.Code, fmt.Sprintf("eval%d.js", e.Seq)
		if e.Kind == "load" {
			f, ferr := s.db.replFile(s.runID, e.Code)
			if ferr != nil {
				continue // the file was deleted since; not drift, just gone
			}
			code, name = f.Content, "file:"+e.Code
			s.loaded[e.Code] = true
		}
		// Pin Date to what this statement originally saw, so a rebuilt scope
		// holds the same values rather than today's.
		created := e.Created
		s.nowFn = func() time.Time { return time.UnixMilli(created) }
		r := s.exec(context.Background(), name, code, replayBudgetFor(e.MS))
		if r.stuck || r.interrupt != nil {
			stopped = fmt.Sprintf("statement %d/%d did not finish in time", i+1, len(entries))
			break
		}
		if e.State == replOK && r.err != nil {
			stopped = fmt.Sprintf("statement %d/%d now throws: %s", i+1, len(entries), oneLine(r.err.Error()))
			break
		}
		s.stmt++
		applied++
	}
	s.nowFn = time.Now

	banner := fmt.Sprintf("[sandbox rebuilt: replayed %d statement(s) in %dms",
		applied, time.Since(t0).Milliseconds())
	if killedNow > 0 {
		banner += fmt.Sprintf("; the previous statement crashed the backend and was discarded (%d/%d)",
			killedTotal, maxReplKilled)
	}
	if stopped != "" {
		banner += fmt.Sprintf("; STOPPED — %s. Scope before that point is intact, later definitions are missing. "+
			"Re-run js_run on your files, or js_reset to start clean", stopped)
	}
	return banner + "]", nil
}

// replayBudgetFor gives a statement roughly 4x its original cost, floored at
// half a second: a statement that took 3ms originally cannot eat the whole
// rebuild budget just because the machine is busier today.
func replayBudgetFor(ms int) time.Duration {
	d := time.Duration(ms*4) * time.Millisecond
	if d < 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	if d > replReplayBudget {
		d = replReplayBudget
	}
	return d
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	return clip(s, 160)
}

// --- janitor ------------------------------------------------------------

func (ag *Agent) replJanitor() {
	for {
		time.Sleep(time.Minute)
		ag.repl.sweep()
	}
}
