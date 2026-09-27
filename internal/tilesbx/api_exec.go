package tilesbx

// api_exec.go — run, background execs and their output, stdin, signals and
// resizes (plans/tile-sandbox-runtime.md §3.5–3.6); the TTY WebSocket is
// tty.go's. Every route is the manager's, on its own sandbox; the shapes
// are the sandbox-manager contract's, so a manager forwards them unchanged.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Output reads (the contract's bounds: clamped, never refused), and a
// run's body: its stdin arrives as a JSON string, escaped.
const (
	outputMaxDefault = 64 << 10
	outputMaxLimit   = 1 << 20
	outputWaitLimit  = 30000 // ms
	runBodyMax       = 2*(stdinMax+argvEnvMax) + 64<<10
)

// OutputChunk is a read of an exec's output (the contract's chunk).
type OutputChunk struct {
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	Total     int64  `json:"total"`
	RingStart int64  `json:"ringStart"`
	Data      string `json:"data"`
	Encoding  string `json:"encoding"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exitCode"`
	Signal    string `json:"signal"`
}

// execFor finds {id} among k's sandbox's execs: 410 lost when it is from
// another xbind start (the only lost), 404 when this sandbox has none such.
func (m *Manager) execFor(w http.ResponseWriter, k Key, d *Def, id string) (*box, *execRec, bool) {
	if !strings.HasPrefix(id, m.boot+"-") {
		writeErr(w, refuse(RefLost, "exec %s is gone: it ran before xbind restarted", id))
		return nil, nil, false
	}
	b, err := m.boxFor(k, d.Name)
	if err != nil {
		writeErr(w, err)
		return nil, nil, false
	}
	e := b.execs.get(id)
	if e == nil {
		writeErr(w, refuse(RefNotFound, "sandbox %q has no exec %s", d.Name, id))
		return nil, nil, false
	}
	m.touch(b)
	return b, e, true
}

// ServeRun answers POST /sandboxes/{name}/run: the contract's run, blocking.
func (m *Manager) ServeRun(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	var req RunRequest
	if err := decode(w, r, runBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.runCommand(r.Context(), k, d, &req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case r.Context().Err() == nil:
		writeErr(w, err)
	} // hung up: nobody reads the answer
}

// ServeExecs answers GET /sandboxes/{name}/execs: the running execs and
// those kept since they ended, oldest first.
func (m *Manager) ServeExecs(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	b, err := m.boxFor(k, d.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.touch(b)
	out := []Exec{}
	for _, e := range b.execs.list(m.now().UnixMilli()) {
		out = append(out, e.info())
	}
	writeJSON(w, http.StatusOK, map[string]any{"execs": out})
}

// ServeExecStart answers POST /sandboxes/{name}/execs: 201 and the exec
// (200 and the same one when clientId repeats the same request).
func (m *Manager) ServeExecStart(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	var req ExecRequest
	if err := decode(w, r, execBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	e, _, repeat, err := m.startExec(k, d, &req, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if repeat {
		status = http.StatusOK
	}
	writeJSON(w, status, e.info())
}

// ServeExec answers GET /sandboxes/{name}/execs/{id}.
func (m *Manager) ServeExec(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	if _, e, ok := m.execFor(w, k, d, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, e.info())
	}
}

// ServeExecKill answers DELETE /sandboxes/{name}/execs/{id}: kill the
// group, forget the exec.
func (m *Manager) ServeExecKill(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	b, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	if err := m.killExec(b.execs, e, true); err != nil {
		var re *Error
		if errors.As(err, &re) && re.Refusal == RefUnavailable {
			writeErr(w, err) // forgotten, but the kill didn't reach the sandbox
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeOutput answers GET /sandboxes/{name}/execs/{id}/output?since=&max=&waitMs=&encoding=:
// the bytes from since (or the oldest kept: start > since is a gap), up to
// max; with nothing past since while the exec runs, it waits up to waitMs
// for more and answers as soon as the exec ends.
func (m *Manager) ServeOutput(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	since, err1 := intQuery(q.Get("since"), 0)
	limit, err2 := intQuery(q.Get("max"), outputMaxDefault)
	waitMs, err3 := intQuery(q.Get("waitMs"), 0)
	enc := q.Get("encoding")
	switch {
	case errors.Join(err1, err2, err3) != nil:
		writeErr(w, refuse(RefInvalid, "since, max and waitMs are non-negative numbers"))
		return
	case enc == "":
		enc = "text"
	case enc != "text" && enc != "base64":
		writeErr(w, refuse(RefInvalid, "encoding is text or base64"))
		return
	}
	limit = min(max(limit, 1), outputMaxLimit)
	waitMs = min(waitMs, outputWaitLimit)
	_, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	c := e.ring.Read(since, limit)
	if since > c.total {
		writeErr(w, refuse(RefInvalid, "since %d is past the output's end (%d)", since, c.total))
		return
	}
	if c.end == c.start && !c.ended && waitMs > 0 {
		e.ring.Wait(r.Context(), since, time.Duration(waitMs)*time.Millisecond)
		c = e.ring.Read(since, limit)
	}
	x := e.info()
	out := OutputChunk{Start: c.start, End: c.end, Total: c.total, RingStart: c.ringStart, Encoding: enc,
		State: x.State, ExitCode: x.ExitCode, Signal: x.Signal}
	if enc == "base64" {
		out.Data = base64.StdEncoding.EncodeToString(c.data)
	} else {
		final := c.end == c.total && (c.ended || x.State != ExecRunning)
		cut := textCut(c.data, final)
		out.End = c.start + int64(cut)
		out.Data = utf8Text(c.data[:cut])
	}
	writeJSON(w, http.StatusOK, out)
}

// intQuery is a non-negative number query value (def when absent).
func intQuery(v string, def int64) (int64, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad number %q", v)
	}
	return n, nil
}

// ServeStdin answers POST /sandboxes/{name}/execs/{id}/stdin[?eof=1]: the
// raw body to the exec's stdin (a stdin: true exec's, or a tty's terminal);
// eof=1 closes stdin after it (not a tty's: send ^D).
func (m *Manager) ServeStdin(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	b, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	eof := r.URL.Query().Get("eof")
	switch {
	case !e.stdin && !e.tty:
		writeErr(w, refuse(RefInvalid, "exec %s was started without stdin: true", e.id))
		return
	case eof != "" && eof != "0" && eof != "1":
		writeErr(w, refuse(RefInvalid, "eof is 1 or absent"))
		return
	case e.tty && eof == "1":
		writeErr(w, refuse(RefInvalid, "a tty exec's stdin is its terminal: send ^D (\\x04) instead of eof"))
		return
	}
	if !m.execLive(w, e) {
		return
	}
	body := http.MaxBytesReader(w, r.Body, stdinMax)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			m.touch(b)
			if werr := e.writeIn(buf[:n]); werr != nil {
				writeErr(w, werr)
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeErr(w, refuse(RefTooLarge, "the body is over %d bytes (runtime limits.stdinMax)", stdinMax))
			} else {
				writeErr(w, refuse(RefInvalid, "reading the body: %v", err))
			}
			return
		}
	}
	if eof == "1" {
		if err := e.closeIn(); err != nil && e.running() {
			writeErr(w, refuse(RefState, "closing exec %s's stdin: %v", e.id, err))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeSignal answers POST /sandboxes/{name}/execs/{id}/signal: {signal:
// INT | TERM | KILL | HUP, group?}. group defaults to true (the contract's):
// a forwarded {"signal":"INT"} reaches the whole process group.
func (m *Manager) ServeSignal(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	var req struct {
		Signal string `json:"signal"`
		Group  *bool  `json:"group"` // nil: true
	}
	if err := decode(w, r, defBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	sig, known := signals[strings.TrimPrefix(req.Signal, "SIG")]
	if !known {
		writeErr(w, refuse(RefInvalid, "signal is INT, TERM, KILL or HUP"))
		return
	}
	group := req.Group == nil || *req.Group
	_, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok || !m.execLive(w, e) {
		return
	}
	if err := e.signalGroup(sig, group); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeResize answers POST /sandboxes/{name}/execs/{id}/resize (a tty exec).
func (m *Manager) ServeResize(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	var req struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if err := decode(w, r, defBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Rows < 1 || req.Cols < 1 || req.Rows > 0xffff || req.Cols > 0xffff {
		writeErr(w, refuse(RefInvalid, "rows and cols are 1…65535"))
		return
	}
	_, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	if !e.tty {
		writeErr(w, refuse(RefInvalid, "exec %s has no terminal (tty: false)", e.id))
		return
	}
	if !m.execLive(w, e) {
		return
	}
	if err := e.resize(uint16(req.Rows), uint16(req.Cols)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// execLive answers 409 state for an exec that has ended.
func (m *Manager) execLive(w http.ResponseWriter, e *execRec) bool {
	if x := e.info(); x.State != ExecRunning {
		writeErr(w, &Error{Refusal: RefState, State: x.State, Msg: fmt.Sprintf("exec %s has ended (%s)", e.id, x.State)})
		return false
	}
	return true
}
