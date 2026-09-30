// harness_pipe.go — a coding agent's stdio in a sandbox (D-harness §3.6):
// the acp.Process the harness engine drives an ACP adapter through, over
// the sandbox-manager contract (docs/sandbox-manager.md) — the manager
// called for the person the conversation acts for (Sbx-User, asserted), as
// every sandbox tool calls it (sandbox_use.go).
//
// Two transports over one stream of offsets, the manager's:
//
//   - the baseline: a non-tty exec with stdin, its stderr sent to a log
//     file by a sh -c wrapper (the exec's one stream is then the adapter's
//     stdout alone); stdout long-polled as base64 by byte offset, stdin
//     POSTed in chunks of limits.stdinMax (a 503 retried);
//   - `stdio`, where the manager and the sandbox offer it (§5.3): the exec
//     is split (stderr a stream of its own, no wrapper) and both directions
//     ride one WebSocket (harness_pipe_stdio.go); the socket attached last
//     holds stdin. Each stdin frame is followed by a ping: its pong says the
//     command has it, and what no pong acknowledged goes again on the next
//     socket after a drop.
//
// Either reattaches: a pipe from an exec id and the offsets read so far
// resumes reading without spawning — how a successor process takes a
// session over after a handoff (§3.3). The ring losing bytes before they
// were read is an *acp.Gap from Read (the decoder resyncs at the next
// newline); an exec that is gone (lost after an xbind restart, forgotten)
// is the end of the stream with Lost set.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// harnessWrapper runs an adapter with its stderr in its log file (the
// baseline, §4.2.7): $1 names the log, the rest is the adapter's argv.
const harnessWrapper = `L="${HOME:-/tmp}/.cache/xbin-harness/$1.log"; shift; mkdir -p "${L%/*}"; exec "$@" 2>"$L"`

const (
	hpOutputMax  = 1 << 20 // one long-poll's bytes
	hpPollWaitMs = 25000
	hpStdinMax   = 1 << 20 // a manager that doesn't say its limits.stdinMax
)

const (
	hpPollGap  = 20 * time.Millisecond // before a poll when the last one didn't fill a chunk
	hpRetryFor = 2 * time.Minute       // a 503'd stdin write, a manager that doesn't answer
	hpKillStep = 3 * time.Second       // Kill: stdin eof, TERM this much later, DELETE as much again
)

var (
	errPipeLost     = errors.New("the coding agent's command is gone — its sandbox stopped or restarted")
	errPipeReplaced = errors.New("another process attached to the coding agent's input and output")
	errPipeDetached = errors.New("this process let the coding agent's command go (a handoff)")
	errPipeEnded    = errors.New("the coding agent's command has ended")
	errStdinClosed  = errors.New("the coding agent's input is closed")
)

// hpTarget is where a harness runs: a sandbox at its manager, called for the
// person the conversation acts for, and what the manager offers.
type hpTarget struct {
	Conn     *sbxConn
	ID       string // the sandbox's id at the manager
	Stdio    bool   // the manager and the sandbox offer stdio (§5.3)
	StdinMax int    // limits.stdinMax (0: 1 MiB)
	// Guard runs before every stdin write: an error (this process no
	// longer owns the session — a handoff's epoch) refuses the write
	// before a byte leaves; before the stdio socket is attached again after
	// a drop, it ends reading with that error and lets the command go
	// (Detach). nil: always.
	Guard func() error
	// Dropped hears of a JSON-RPC request the pipe gave up on rather than
	// risk sending it twice (stdio: it went on a socket that dropped before
	// a pong acknowledged it — the command may or may not have it). Only
	// requests: a response or a notification goes again (at least once).
	// Called on its own goroutine; nil: nobody hears.
	Dropped func(id json.RawMessage, method string)
}

// harnessTarget is the target a sandbox use resolved (sandboxUse).
func harnessTarget(u *sbxUse) hpTarget {
	return hpTarget{Conn: u.Conn, ID: u.ID, Stdio: u.Hello.has("stdio") && u.Box.hasCap("stdio"), StdinMax: u.Hello.Limits.StdinMax}
}

// hpSpawn is one adapter process: generation Gen of run Run's harness.
type hpSpawn struct {
	Run, Root int64
	Gen       int
	Provider  string            // the catalog id (the exec's label)
	Argv      []string          // the adapter's command
	Cwd       string            // "" = the sandbox's workdir
	Env       map[string]string // the provider's; IS_SANDBOX=1 and NO_COLOR=1 are added
}

// harnessClientID is the exec's clientId: a start repeated after an answer
// that got lost finds the same exec.
func harnessClientID(run int64, gen int) string { return fmt.Sprintf("harness:%d:%d", run, gen) }

// harnessLogName names a generation's log file (the baseline's stderr).
func harnessLogName(run int64, gen int) string { return fmt.Sprintf("%d-%d", run, gen) }

// harnessExecReq is the exec that runs an adapter.
func harnessExecReq(t hpTarget, s hpSpawn) sbxExecReq {
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = v
	}
	env["IS_SANDBOX"], env["NO_COLOR"] = "1", "1"
	argv := append([]string(nil), s.Argv...)
	if !t.Stdio {
		argv = append([]string{"sh", "-c", harnessWrapper, "h", harnessLogName(s.Run, s.Gen)}, argv...)
	}
	return sbxExecReq{Argv: argv, Cwd: s.Cwd, Env: env, Stdin: true, Split: t.Stdio,
		Label: fmt.Sprintf("harness %s · #%d", s.Provider, s.Root), ClientID: harnessClientID(s.Run, s.Gen)}
}

// startHarnessPipe starts an adapter in the target sandbox and returns its
// pipe, reading from the start. A start repeated for the same run and
// generation (an answer that never came) is the same exec.
func startHarnessPipe(ctx context.Context, t hpTarget, s hpSpawn) (*harnessPipe, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("no command to start the coding agent with")
	}
	ex, err := t.Conn.ExecStart(ctx, t.ID, harnessExecReq(t, s))
	if err != nil {
		return nil, err
	}
	p := newHarnessPipe(t, ex.ID, 0, 0)
	p.open(ctx)
	return p, nil
}

// attachHarnessPipe resumes an adapter another pipe started — this
// process's earlier one, or a predecessor's — reading stdout from readOff
// and (stdio) stderr from errOff, the offsets read so far. Nothing is
// spawned; an exec that is gone ends the stream at once, Lost.
func attachHarnessPipe(ctx context.Context, t hpTarget, execID string, readOff, errOff int64) *harnessPipe {
	p := newHarnessPipe(t, execID, readOff, errOff)
	p.open(ctx)
	return p
}

// harnessPipe is one adapter's stdio. Stdout is read by one goroutine at a
// time (the acp.Conn's read loop); stdin writes are serialized.
type harnessPipe struct {
	t      hpTarget
	execID string
	base   int64 // Process.Off: where reading started
	ctx    context.Context
	cancel context.CancelFunc // Detach
	// killStep is Kill's pace (hpKillStep; tests shorten it)
	killStep time.Duration

	// the reader's own
	since   int64  // how far the manager's stream is read
	buf     []byte // read, not yet handed on
	gap     int64  // lost before buf: handed on first
	last    time.Time
	full    bool      // the last poll filled a chunk: poll again at once
	failing time.Time // since when the manager hasn't answered
	tries   int
	drops   int // stdio sockets that dropped with nothing read since

	mu       sync.Mutex
	stdio    bool     // the transport now: the socket (else polls and POSTs)
	sock     *ws.Conn // the stdio socket, while attached
	changed  chan struct{}
	over     bool  // the stream has ended (the reader hands on what it holds, then the end)
	why      error // what ended it: nil its end, errPipeLost, or the error Read returns
	off      int64 // handed to the reader (gaps included)
	errOff   int64 // stderr read (stdio)
	state    string
	code     *int
	signal   string
	killing  bool  // Kill deleted it: its being gone is its end, not a loss
	acked    int64 // the last stdin unit a pong acknowledged (stdio)
	done     chan struct{}
	doneOnce sync.Once

	wmu      sync.Mutex // one writer
	eof      bool       // stdin closed (wmu)
	wseq     int64      // (wmu)
	outbox   []*hpUnit  // sent, not acknowledged (wmu)
	killOnce sync.Once
}

func newHarnessPipe(t hpTarget, execID string, readOff, errOff int64) *harnessPipe {
	ctx, cancel := context.WithCancel(context.Background())
	return &harnessPipe{t: t, execID: execID, base: readOff, since: readOff, off: readOff, errOff: errOff, stdio: t.Stdio,
		ctx: ctx, cancel: cancel, changed: make(chan struct{}), state: "running", done: make(chan struct{}), killStep: hpKillStep}
}

// Process is the pipe as the acp.Client starts it (a Spawner returns it).
func (p *harnessPipe) Process() *acp.Process {
	return &acp.Process{Stdin: hpStdin{p}, Stdout: hpStdout{p}, Wait: p.Wait, Kill: p.Kill, Off: p.base}
}

type hpStdout struct{ p *harnessPipe }

func (r hpStdout) Read(b []byte) (int, error) { return r.p.read(b) }

type hpStdin struct{ p *harnessPipe }

func (w hpStdin) Write(b []byte) (int, error) { return w.p.write(b) }

// Close sends stdin's eof in the background: a write still retrying holds
// the writer's turn for up to 2 min, and the client's Close shouldn't wait
// on it (Kill, which follows it, sends the eof too).
func (w hpStdin) Close() error {
	go func() { _ = w.p.closeStdin(10 * time.Second) }()
	return nil
}

// hpExit is where the command stands: running, exited (Code), killed
// (Signal; Code nil) or lost.
type hpExit struct {
	State  string
	Code   *int
	Signal string
}

func (p *harnessPipe) ExecID() string { return p.execID }

func (p *harnessPipe) Exit() hpExit {
	p.mu.Lock()
	defer p.mu.Unlock()
	return hpExit{State: p.state, Code: p.code, Signal: p.signal}
}

// Lost: the exec is gone (410 lost, or forgotten) — the stream ended
// without its end.
func (p *harnessPipe) Lost() bool { return p.Exit().State == "lost" }

// Err is what ended reading when the command didn't end: errPipeReplaced,
// errPipeDetached, a manager's refusal; nil otherwise (and while reading).
func (p *harnessPipe) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.why == errPipeLost {
		return nil
	}
	return p.why
}

// Done is closed once Read has returned the stream's end.
func (p *harnessPipe) Done() <-chan struct{} { return p.done }

// Stdio: the pipe speaks the stdio socket now (else the exec routes).
func (p *harnessPipe) Stdio() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stdio
}

// Off is the stdout offset handed to the reader so far (gaps included);
// ErrOff the stderr offset read (stdio: a successor attaches from it).
func (p *harnessPipe) Off() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.off
}

func (p *harnessPipe) ErrOff() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errOff
}

// Wait waits for the reader to reach the end, and says how it ended: nil
// for an exit 0.
func (p *harnessPipe) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.why != nil:
		return p.why
	case p.signal != "":
		return fmt.Errorf("the coding agent was killed (%s)", p.signal)
	case p.code != nil && *p.code != 0:
		return fmt.Errorf("the coding agent exited %d", *p.code)
	}
	return nil
}

// Detach lets the command go without touching it (a handoff: another
// process attaches): reading ends with errPipeDetached, writes fail, Kill
// does nothing.
func (p *harnessPipe) Detach() {
	p.cancel()
	p.mu.Lock()
	c := p.sock
	p.sock = nil
	p.bump()
	p.mu.Unlock()
	if c != nil {
		go c.Close()
	}
}

// Kill ends the command (asynchronously): stdin eof, TERM 3 s later while
// it runs, DELETE 3 s after that. Reading goes on to the end.
func (p *harnessPipe) Kill() {
	if p.ctx.Err() != nil {
		return // detached: another process's now
	}
	p.killOnce.Do(func() { go p.kill() })
}

func (p *harnessPipe) kill() {
	go func() { _ = p.closeStdin(p.killStep) }()
	if p.ended(p.killStep) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
	defer cancel()
	if err := p.t.Conn.ExecSignal(ctx, p.t.ID, p.execID, "TERM", true); gone(err) {
		return
	}
	if p.ended(p.killStep) {
		return
	}
	p.mu.Lock()
	p.killing = true
	p.mu.Unlock()
	if err := p.t.Conn.ExecDelete(ctx, p.t.ID, p.execID); err != nil && !gone(err) {
		logf("harness exec %s: DELETE: %v", p.execID, err)
	}
}

// ended waits up to d for the command to end — the reader reaching the end,
// else the manager saying so.
func (p *harnessPipe) ended(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-p.ctx.Done():
		return true // detached meanwhile: not ours to end
	case <-t.C:
	}
	ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
	defer cancel()
	ex, err := p.t.Conn.ExecGet(ctx, p.t.ID, p.execID)
	if err != nil {
		return gone(err)
	}
	return ex.State != "running"
}

// --- state --------------------------------------------------------------------

// bump wakes whoever waits for the socket or the end (p.mu held).
func (p *harnessPipe) bump() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// end marks the stream over: why nil is its end (EOF once what was read is
// handed on), errPipeLost the end with Lost, anything else Read's error.
// An exec gone after Kill deleted it has ended: it was killed.
func (p *harnessPipe) end(why error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.over {
		return
	}
	switch {
	case why == errPipeLost && p.killing:
		why = nil
		if p.state == "running" {
			p.state, p.signal = "killed", "KILL"
		}
	case why == errPipeLost:
		p.state = "lost"
	}
	p.over, p.why = true, why
	p.bump()
}

func (p *harnessPipe) setState(state string, code *int, signal string) {
	if state == "" {
		return
	}
	p.mu.Lock()
	p.state, p.code, p.signal = state, code, signal
	p.mu.Unlock()
}

// --- reading ----------------------------------------------------------------

func (p *harnessPipe) read(b []byte) (int, error) {
	for {
		if p.ctx.Err() != nil { // let go: what was read and not handed on is the successor's
			p.buf, p.gap = nil, 0
			p.end(errPipeDetached)
		}
		if n, ok, err := p.take(b); ok {
			return n, err
		}
		if p.Stdio() {
			p.stepSocket()
		} else {
			p.stepPoll()
		}
	}
}

// take hands on what the reader holds: the gap before the bytes read last,
// those bytes, then the end. ok false: nothing — read on.
func (p *harnessPipe) take(b []byte) (n int, ok bool, err error) {
	if p.gap > 0 {
		g := p.gap
		p.gap = 0
		p.advance(g)
		return 0, true, &acp.Gap{Lost: g}
	}
	if len(p.buf) > 0 {
		n = copy(b, p.buf)
		p.buf = p.buf[n:]
		p.advance(int64(n))
		return n, true, nil
	}
	p.mu.Lock()
	over, why := p.over, p.why
	p.mu.Unlock()
	if !over {
		return 0, false, nil
	}
	p.doneOnce.Do(func() { close(p.done) })
	if why == nil || why == errPipeLost {
		return 0, true, io.EOF
	}
	return 0, true, why
}

func (p *harnessPipe) advance(n int64) {
	p.mu.Lock()
	p.off += n
	p.mu.Unlock()
}

// stepPoll is one long-poll of the exec's output (the baseline).
func (p *harnessPipe) stepPoll() {
	if !p.full && !p.last.IsZero() {
		if d := hpPollGap - time.Since(p.last); d > 0 && !sleepCtx(p.ctx, d) {
			return
		}
	}
	ch, err := p.t.Conn.ExecOutput(p.ctx, p.t.ID, p.execID, p.since, hpOutputMax, hpPollWaitMs, true)
	p.last = time.Now()
	if err != nil {
		p.failed(err)
		return
	}
	p.failing, p.tries = time.Time{}, 0
	data, derr := base64.StdEncoding.DecodeString(ch.Data)
	if derr != nil || ch.Encoding == "text" {
		data = []byte(ch.Data) // a manager that answered in text anyway
	}
	if ch.Start > p.since {
		p.gap = ch.Start - p.since // the ring dropped them before they were read
	}
	p.buf = data
	end := ch.End
	if end < ch.Start { // a manager that left it out
		end = ch.Start + int64(len(data))
	}
	p.since = max(p.since, end)
	p.full = p.since < ch.Total || len(data) >= hpOutputMax
	switch ch.State {
	case "lost":
		p.end(errPipeLost)
	case "", "running":
	default:
		p.setState(ch.State, ch.ExitCode, ch.Signal)
		if p.since >= ch.Total {
			p.end(nil)
		}
	}
}

// failed: the manager refused or didn't answer a read — the exec gone is
// the end (Lost), a manager that is down is asked again for up to
// hpRetryFor, anything else ends reading with it.
func (p *harnessPipe) failed(err error) {
	if p.ctx.Err() != nil {
		p.end(errPipeDetached)
		return
	}
	switch sbxRefusal(err) {
	case "lost", "not-found":
		p.end(errPipeLost)
		return
	case "unavailable", "unreachable", "limit":
		if p.failing.IsZero() {
			p.failing = time.Now()
		}
		if time.Since(p.failing) < hpRetryFor {
			sleepCtx(p.ctx, backoff(p.tries, err))
			p.tries++
			return
		}
	}
	p.end(err)
}

// --- writing ----------------------------------------------------------------

// write sends one frame (the acp.Conn writes whole frames): in chunks of at
// most limits.stdinMax, each retried while the manager is busy or down.
func (p *harnessPipe) write(b []byte) (int, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if p.eof {
		return 0, errStdinClosed
	}
	if p.t.Guard != nil {
		if err := p.t.Guard(); err != nil {
			return 0, err
		}
	}
	limit := p.t.StdinMax
	if limit <= 0 {
		limit = hpStdinMax
	}
	f := frameOf(b)
	n := 0
	for n < len(b) {
		k := min(len(b)-n, limit)
		if err := p.send(b[n:n+k], false, hpRetryFor, f, n == 0); err != nil {
			return n, err
		}
		n += k
	}
	return n, nil
}

// hpFrame is the frame a stdin unit is part of, when it is a JSON-RPC
// request: its id and method (the acp.Conn writes one frame per Write).
type hpFrame struct {
	id      json.RawMessage
	method  string
	dropped bool // given up on (flush): its units still to go are not sent
}

// frameOf is b's hpFrame when b is a request (an id and a method), else nil.
func frameOf(b []byte) *hpFrame {
	if !bytes.Contains(b, []byte(`"method"`)) {
		return nil
	}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(b, &m) != nil || m.Method == "" || len(m.ID) == 0 || string(m.ID) == "null" {
		return nil
	}
	return &hpFrame{id: m.ID, method: m.Method}
}

// closeStdin sends stdin's eof (once), trying for up to budget.
func (p *harnessPipe) closeStdin(budget time.Duration) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if p.eof {
		return nil
	}
	p.eof = true
	err := p.send(nil, true, budget, nil, true)
	if r := sbxRefusal(err); r == "state" || r == "invalid" || gone(err) || err == errPipeEnded {
		return nil // it ended, or its stdin was closed already
	}
	return err
}

// hpUnit is a stdin chunk (or the eof) on its way. Over stdio it stays in
// the outbox until a pong acknowledges it — a manager answers a ping only
// once it has handed the command everything before it — and goes again on
// each socket attached after the one it went on: a socket that dropped may
// have swallowed it. Except a request's: one whose first chunk went on a
// socket that dropped unacknowledged is given up on (flush) — the command
// may have it, and a session/prompt must never reach the adapter twice.
type hpUnit struct {
	seq   int64
	data  []byte
	eof   bool
	on    *ws.Conn // the socket it went (or began to go) on last
	frame *hpFrame // a request's (nil: a response, a notification, the eof)
	first bool     // the frame's first chunk
}

// send delivers one chunk (or the eof) after every one before it: over the
// exec routes a POST of the exec's stdin, a 503 (the command isn't
// reading) or a manager that is down tried again with backoff until budget
// runs out; over stdio a frame of the socket and a ping, the socket
// attached again by the reader when it drops. A chunk send gave up on is
// not sent later.
func (p *harnessPipe) send(data []byte, eof bool, budget time.Duration, f *hpFrame, first bool) error {
	if f != nil && f.dropped {
		return nil // the rest of a request given up on
	}
	p.wseq++
	u := &hpUnit{seq: p.wseq, data: append([]byte(nil), data...), eof: eof, frame: f, first: first}
	p.outbox = append(p.outbox, u)
	deadline := time.Now().Add(budget)
	for try := 0; ; try++ {
		c, err := p.writeWay(deadline)
		if err == nil {
			if err = p.flush(c); err == nil {
				return nil
			}
		}
		switch {
		case p.ctx.Err() != nil:
			err = errPipeDetached
		case c != nil:
			p.dropSock(c)
		}
		d := time.Until(deadline)
		if r := sbxRefusal(err); d <= 0 || err == errPipeDetached || err == errPipeEnded || (c == nil && r != "unavailable" && r != "unreachable") {
			p.unqueue(u)
			return err
		}
		if !sleepCtx(p.ctx, min(d, backoff(try, err))) {
			p.unqueue(u)
			return errPipeDetached
		}
	}
}

// flush sends the outbox the way c (nil: the exec routes) hasn't had it,
// in order, once what the manager acknowledged is dropped.
func (p *harnessPipe) flush(c *ws.Conn) error {
	p.mu.Lock()
	acked := p.acked
	p.mu.Unlock()
	for len(p.outbox) > 0 && p.outbox[0].on != nil && p.outbox[0].seq <= acked {
		p.outbox = p.outbox[1:]
	}
	if c != nil {
		p.dropUnsure(c)
	}
	for c == nil && len(p.outbox) > 0 { // a POST answered is delivered
		u := p.outbox[0]
		ctx, cancel := context.WithTimeout(p.ctx, sbxCallTimeout)
		err := p.t.Conn.ExecStdin(ctx, p.t.ID, p.execID, u.data, u.eof)
		cancel()
		if err != nil {
			return err
		}
		p.outbox = p.outbox[1:]
	}
	for _, u := range p.outbox {
		if u.on == c {
			continue
		}
		typ, frame := ws.BinaryMessage, u.data
		if u.eof {
			typ, frame = ws.TextMessage, []byte(`{"op":"eof"}`)
		}
		u.on = c // a write that fails may still have put it on the wire
		if err := c.WriteMessage(typ, frame); err != nil {
			return err
		}
		if err := c.WriteMessage(ws.TextMessage, []byte(`{"op":"ping","t":`+strconv.FormatInt(u.seq, 10)+`}`)); err != nil {
			return err
		}
	}
	return nil
}

// dropUnsure gives up on each request whose first chunk went on a socket
// other than c and no pong acknowledged (the ones before it are gone from
// the outbox): the command may or may not have it, so it is never sent
// again — at most once. A newline takes its place, ending whatever part
// of it the command took; Dropped hears of it.
func (p *harnessPipe) dropUnsure(c *ws.Conn) {
	var gone []*hpFrame
	out := make([]*hpUnit, 0, len(p.outbox))
	for _, u := range p.outbox {
		f := u.frame
		switch {
		case f == nil:
		case f.dropped:
			continue
		case u.first && u.on != nil && u.on != c:
			f.dropped = true
			gone = append(gone, f)
			out = append(out, &hpUnit{seq: u.seq, data: []byte("\n")})
			continue
		}
		out = append(out, u)
	}
	p.outbox = out
	for _, f := range gone {
		logf("harness exec %s: %s %s may not have reached the command (its socket dropped) — not sent again", p.execID, f.method, f.id)
		if p.t.Dropped != nil {
			go p.t.Dropped(f.id, f.method)
		}
	}
}

// unqueue takes a unit send gave up on out of the outbox.
func (p *harnessPipe) unqueue(u *hpUnit) {
	for i, x := range p.outbox {
		if x == u {
			p.outbox = append(p.outbox[:i:i], p.outbox[i+1:]...)
			return
		}
	}
}

// writeWay is how a chunk goes now: the socket (waiting until deadline for
// the reader to attach it), or nil for the exec's stdin route.
func (p *harnessPipe) writeWay(deadline time.Time) (*ws.Conn, error) {
	for {
		p.mu.Lock()
		c, stdio, over, ch := p.sock, p.stdio, p.over, p.changed
		p.mu.Unlock()
		switch {
		case p.ctx.Err() != nil:
			return nil, errPipeDetached
		case over:
			return nil, errPipeEnded
		case !stdio:
			return nil, nil
		case c != nil:
			return c, nil
		}
		t := time.NewTimer(time.Until(deadline))
		select {
		case <-ch:
		case <-p.ctx.Done():
		case <-t.C:
			return nil, &sbxError{Provider: p.t.Conn.M.Provider, Refusal: "unreachable", Msg: "the coding agent's stdio socket didn't attach"}
		}
		t.Stop()
	}
}

// backoff is how long to wait before try n+1: what the manager asked for,
// else 100 ms doubling to 5 s.
func backoff(n int, err error) time.Duration {
	var e *sbxError
	if errors.As(err, &e) && e.RetryAfterMs > 0 {
		return min(time.Duration(e.RetryAfterMs)*time.Millisecond, 5*time.Second)
	}
	return min(100*time.Millisecond<<min(n, 6), 5*time.Second)
}

// sleepCtx sleeps d, or less when ctx ends first (false).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
