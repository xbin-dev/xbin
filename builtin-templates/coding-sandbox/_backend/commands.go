// commands.go — the contract's commands: a blocking run, background execs
// (their output by byte offset, stdin, signals, resizes), terminals and —
// where the backend has them — stdio sockets, relayed to the backend's box with the sandbox's user and the person as
// the claim (forUser). An exec's clientId is per consumer and sandbox: the
// manager dedupes it (in memory — execs die with their substrate) and hands
// the backend a clientId prefixed with the consumer, so two consumers of a
// shared sandbox never collide there either.
package main

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

func (m *Manager) commandRoutes(x *http.ServeMux) {
	x.HandleFunc("POST /sbx/sandboxes/{id}/run", m.run)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs", m.execList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs", m.execStart)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}", m.execGet)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}/execs/{eid}", m.execDelete)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/output", m.execOutput)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/stdin", m.execStdin)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/signal", m.execSignal)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/resize", m.execResize)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/tty", m.ttyAttach)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/stdio", m.stdioAttach)
	x.HandleFunc("GET /sbx/sandboxes/{id}/tty", m.ttyStart)
}

// box is rec's sandbox at the backend.
func (m *Manager) box(rec record) Box { return m.backend().Sandbox(rec.Runtime) }

// usable is find + ready: {id} for a command or a file operation, started
// and prepared. A caller who may only look (caller.lookOnly) reads a running
// sandbox and never starts a stopped one.
func (m *Manager) usable(w http.ResponseWriter, r *http.Request) (record, caller, bool) {
	rec, c, ok := m.find(w, r)
	if !ok {
		return rec, c, false
	}
	if err := m.ready(r.Context(), &rec, !c.lookOnly); err != nil {
		writeErr(w, err, &rec)
		return rec, c, false
	}
	return rec, c, true
}

// offers reports that the backend offers capability c now (false when it
// can't say).
func (m *Manager) offers(r *http.Request, c string) bool {
	o, err := m.offer(r.Context())
	return err == nil && contains(o.caps, c)
}

// hasCap: the backend offers capability c now (a refusal answered when not).
func (m *Manager) hasCap(w http.ResponseWriter, r *http.Request, c, what string) bool {
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return false
	}
	if !contains(o.caps, c) {
		fail(w, http.StatusNotImplemented, "unsupported", "this manager has no "+what+" ("+c+")")
		return false
	}
	return true
}

// --- run -------------------------------------------------------------------------------

// runReq is the contract's run body (no uid, gid or forUser: those are the
// manager's to set).
type runReq struct {
	Cmd       string            `json:"cmd"`
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Stdin     string            `json:"stdin"`
	TimeoutMs int64             `json:"timeoutMs"`
	MaxOutput int64             `json:"maxOutput"`
	Merge     bool              `json:"merge"`
}

func (m *Manager) run(w http.ResponseWriter, r *http.Request) {
	var q runReq
	if err := decode(r, 8<<20, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	if q.Cmd == "" && len(q.Argv) == 0 {
		fail(w, http.StatusBadRequest, "invalid", "cmd or argv is required")
		return
	}
	rec, c, ok := m.usable(w, r)
	if !ok {
		return
	}
	uid, gid := rec.UID, rec.GID
	res, err := m.box(rec).Run(r.Context(), xbin.RunRequest{Cmd: q.Cmd, Argv: q.Argv, Cwd: q.Cwd, Env: q.Env, Stdin: q.Stdin,
		TimeoutMs: q.TimeoutMs, MaxOutput: q.MaxOutput, Merge: q.Merge, UID: &uid, GID: &gid, ForUser: c.user})
	if err != nil {
		if r.Context().Err() == nil {
			writeErr(w, err, &rec)
		}
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- execs ------------------------------------------------------------------------------

type execReq struct {
	Cmd       string            `json:"cmd"`
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	TTY       bool              `json:"tty"`
	Rows      int               `json:"rows"`
	Cols      int               `json:"cols"`
	Stdin     bool              `json:"stdin"`
	Split     bool              `json:"split"`
	TimeoutMs int64             `json:"timeoutMs"`
	Label     string            `json:"label"`
	ClientID  string            `json:"clientId"`
}

// execView is the contract's exec.
type execView struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Cmd      string   `json:"cmd"`
	Argv     []string `json:"argv"`
	Cwd      string   `json:"cwd"`
	TTY      bool     `json:"tty"`
	State    string   `json:"state"`
	ExitCode *int     `json:"exitCode"`
	Signal   string   `json:"signal"`
	Started  int64    `json:"started"`
	Ended    int64    `json:"ended"`
	Total    int64    `json:"total"`
	Split    bool     `json:"split,omitempty"`
	ErrTotal int64    `json:"errTotal,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
}

// clientPrefix is how a consumer's exec clientIds are told apart at the
// backend.
func clientPrefix(consumer string) string { return "c" + hashOf(consumer)[:8] + ":" }

// execOut is x as consumer sees it: its own clientId, nobody else's.
func execOut(x xbin.ExecInfo, consumer string) execView {
	v := execView{ID: x.ID, Label: x.Label, Cmd: x.Cmd, Argv: x.Argv, Cwd: x.Cwd, TTY: x.TTY, State: x.State,
		ExitCode: x.ExitCode, Signal: x.Signal, Started: x.Started, Ended: x.Ended, Total: x.Total,
		Split: x.Split, ErrTotal: x.ErrTotal}
	if v.Argv == nil {
		v.Argv = []string{}
	}
	if p := clientPrefix(consumer); strings.HasPrefix(x.ClientID, p) {
		v.ClientID = x.ClientID[len(p):]
	}
	return v
}

func (m *Manager) execStart(w http.ResponseWriter, r *http.Request) {
	var q execReq
	if err := decode(r, 1<<20, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	if q.TTY && !m.hasCap(w, r, "tty", "terminals") {
		return
	}
	if q.Cmd == "" && len(q.Argv) == 0 {
		fail(w, http.StatusBadRequest, "invalid", "cmd or argv is required")
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	key, h := c.from+"\x00"+rec.ID+"\x00"+q.ClientID, hashOf(q)
	if q.ClientID != "" {
		defer m.lock("exec\x00" + key)()
		m.mu.Lock()
		prev, seen := m.execIdem[key]
		m.mu.Unlock()
		if seen && prev.hash != h {
			fail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different command")
			return
		}
		if seen {
			if x, err := m.box(rec).GetExec(r.Context(), prev.id); err == nil {
				writeJSON(w, http.StatusOK, execOut(*x, c.from))
				return
			}
		}
	}
	if err := m.ready(r.Context(), &rec, true); err != nil {
		writeErr(w, err, &rec)
		return
	}
	uid, gid := rec.UID, rec.GID
	req := xbin.ExecRequest{Cmd: q.Cmd, Argv: q.Argv, Cwd: q.Cwd, Env: q.Env, TTY: q.TTY, Rows: q.Rows, Cols: q.Cols,
		Stdin: q.Stdin, Split: q.Split && m.offers(r, "stdio"), TimeoutMs: q.TimeoutMs, Label: q.Label, UID: &uid, GID: &gid, ForUser: c.user}
	if q.ClientID != "" {
		req.ClientID = clientPrefix(c.from) + q.ClientID
	}
	x, err := m.box(rec).Exec(r.Context(), req)
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	if q.ClientID != "" {
		m.mu.Lock()
		m.execIdem[key] = execIdem{id: x.ID, hash: h}
		m.mu.Unlock()
	}
	writeJSON(w, http.StatusCreated, execOut(*x, c.from))
}

func (m *Manager) execList(w http.ResponseWriter, r *http.Request) {
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	xs, err := m.box(rec).Execs(r.Context())
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	out := []execView{}
	for _, x := range xs {
		out = append(out, execOut(x, c.from))
	}
	writeJSON(w, http.StatusOK, map[string]any{"execs": out})
}

func (m *Manager) execGet(w http.ResponseWriter, r *http.Request) {
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	x, err := m.box(rec).GetExec(r.Context(), r.PathValue("eid"))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	writeJSON(w, http.StatusOK, execOut(*x, c.from))
}

func (m *Manager) execDelete(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).Kill(r.Context(), r.PathValue("eid")); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chunkView is the contract's output chunk.
type chunkView struct {
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

func (m *Manager) execOutput(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	enc := orStr(qs.Get("encoding"), "text")
	if enc != "text" && enc != "base64" {
		fail(w, http.StatusBadRequest, "invalid", "encoding is text or base64")
		return
	}
	stream := qs.Get("stream")
	if stream != "" && stream != "stdout" && stream != "stderr" {
		fail(w, http.StatusBadRequest, "invalid", "stream is stdout or stderr")
		return
	}
	num := func(k string) int64 { n, _ := strconv.ParseInt(qs.Get(k), 10, 64); return max(n, 0) }
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if stream != "" && !m.offers(r, "stdio") {
		stream = "" // without stdio every exec is one stream: stream names nothing
	}
	ch, err := m.box(rec).Output(r.Context(), r.PathValue("eid"),
		xbin.OutputQuery{Since: num("since"), Max: min(num("max"), 1<<20), WaitMs: min(num("waitMs"), 30000), Encoding: enc, Stream: stream})
	if err != nil {
		if r.Context().Err() == nil {
			writeErr(w, err, &rec)
		}
		return
	}
	writeJSON(w, http.StatusOK, chunkView{Start: ch.Start, End: ch.End, Total: ch.Total, RingStart: ch.RingStart, Data: ch.Data,
		Encoding: orStr(ch.Encoding, enc), State: ch.State, ExitCode: ch.ExitCode, Signal: ch.Signal})
}

func (m *Manager) execStdin(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).Stdin(r.Context(), r.PathValue("eid"), r.Body, r.URL.Query().Get("eof") == "1"); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var signals = map[string]bool{"INT": true, "TERM": true, "KILL": true, "HUP": true}

func (m *Manager) execSignal(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Signal string `json:"signal"`
		Group  *bool  `json:"group"`
	}
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	sig := strings.TrimPrefix(strings.ToUpper(q.Signal), "SIG")
	if !signals[sig] {
		fail(w, http.StatusBadRequest, "invalid", "signal is INT, TERM, KILL or HUP")
		return
	}
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).Signal(r.Context(), r.PathValue("eid"), sig, q.Group == nil || *q.Group); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) execResize(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "tty", "terminals") {
		return
	}
	var q struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if err := decode(r, 64<<10, &q); err != nil || q.Rows <= 0 || q.Cols <= 0 {
		fail(w, http.StatusBadRequest, "invalid", "rows and cols are positive")
		return
	}
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).Resize(r.Context(), r.PathValue("eid"), q.Rows, q.Cols); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- terminals -------------------------------------------------------------------------

// ttyAttach: GET …/execs/{eid}/tty — a tty exec's terminal, relayed.
func (m *Manager) ttyAttach(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "tty", "terminals") {
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if !ws.IsUpgrade(r) {
		fail(w, http.StatusBadRequest, "invalid", "a terminal is a WebSocket upgrade")
		return
	}
	eid := r.PathValue("eid")
	sw := &scrubWriter{ResponseWriter: w, from: rec.Runtime, to: rec.ID}
	m.box(rec).RelayTTY(sw, r, eid, xbin.TTYOptions{SessionID: eid, SandboxID: rec.ID, ForUser: c.user})
	sw.finish()
}

// ttyStart: GET …/tty?cwd=&cmd=&rows=&cols= — a tty exec (the login shell
// unless cmd), started and relayed. A stopped sandbox starts first.
func (m *Manager) ttyStart(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "tty", "terminals") {
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if !ws.IsUpgrade(r) { // before anything starts
		fail(w, http.StatusBadRequest, "invalid", "a terminal is a WebSocket upgrade")
		return
	}
	if err := m.ready(r.Context(), &rec, true); err != nil {
		writeErr(w, err, &rec)
		return
	}
	qs := r.URL.Query()
	rows, _ := strconv.Atoi(qs.Get("rows"))
	cols, _ := strconv.Atoi(qs.Get("cols"))
	uid, gid := rec.UID, rec.GID
	sw := &scrubWriter{ResponseWriter: w, from: rec.Runtime, to: rec.ID}
	m.box(rec).RelayNewTTY(sw, r, xbin.TTYStart{Cwd: qs.Get("cwd"), Cmd: qs.Get("cmd"), Rows: max(rows, 0), Cols: max(cols, 0),
		UID: &uid, GID: &gid, ForUser: c.user, SandboxID: rec.ID})
	sw.finish()
}

// --- stdio sockets -----------------------------------------------------------------------

// stdioAttach: GET …/execs/{eid}/stdio?since=&errSince= — a non-tty exec's
// streams on one WebSocket (the stdio capability), relayed. The exec ids
// are the backend's, so its frames pass as they are.
func (m *Manager) stdioAttach(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "stdio", "stdio sockets") {
		return
	}
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if !ws.IsUpgrade(r) {
		fail(w, http.StatusBadRequest, "invalid", "the stdio route is a WebSocket upgrade")
		return
	}
	qs := r.URL.Query()
	var off [2]int64
	for i, k := range []string{"since", "errSince"} {
		if v := qs.Get(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				fail(w, http.StatusBadRequest, "invalid", "since and errSince are non-negative numbers")
				return
			}
			off[i] = n
		}
	}
	sb, ok := m.box(rec).(StdioBox)
	if !ok { // offered only when it is one (stdioBackend)
		fail(w, http.StatusNotImplemented, "unsupported", "this manager has no stdio sockets (stdio)")
		return
	}
	sw := &scrubWriter{ResponseWriter: w, from: rec.Runtime, to: rec.ID}
	sb.RelayStdio(sw, r, r.PathValue("eid"), off[0], off[1])
	sw.finish()
}

// scrubWriter is a relayed terminal's (or stdio socket's) answer: a
// refusal (before the upgrade) is held and written with the runtime's name
// for the sandbox replaced by the contract id — the relay copies it as it
// came. Anything else passes as it is, and an upgrade hijacks the
// connection underneath (Unwrap).
type scrubWriter struct {
	http.ResponseWriter
	from, to string
	status   int
	held     bytes.Buffer
}

func (s *scrubWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *scrubWriter) WriteHeader(code int) {
	if s.status != 0 {
		return
	}
	s.status = code
	if code >= 400 {
		s.ResponseWriter.Header().Del("Content-Length") // the body changes length
		return
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *scrubWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.WriteHeader(http.StatusOK)
	}
	if s.status >= 400 {
		if s.held.Len() > 64<<10 {
			return len(b), nil // a refusal is short; the rest isn't kept
		}
		return s.held.Write(b)
	}
	return s.ResponseWriter.Write(b)
}

func (s *scrubWriter) Flush() {
	if s.status != 0 && s.status < 400 {
		_ = http.NewResponseController(s.ResponseWriter).Flush()
	}
}

// finish writes a held refusal.
func (s *scrubWriter) finish() {
	if s.status < 400 {
		return
	}
	s.ResponseWriter.WriteHeader(s.status)
	body := s.held.Bytes()
	if s.from != "" {
		body = bytes.ReplaceAll(body, []byte(s.from), []byte(s.to))
	}
	_, _ = s.ResponseWriter.Write(body)
}
