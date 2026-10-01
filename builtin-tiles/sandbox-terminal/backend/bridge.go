// bridge.go — one SSH session channel, bridged to a sandbox's manager.
//
//   - With a terminal (pty-req, then shell or exec): the manager's `tty`
//     route (`GET …/sandboxes/{id}/tty?cmd=&rows=&cols=`, a WebSocket on the
//     terminal wire, docs/protocol.md), dialled with sdk/ws as the person
//     (Sbx-User). Its binary frames are the channel's bytes both ways, a
//     window-change is a `resize`, its `exit` is the exit status.
//   - Without one (`ssh host cmd`, `ssh -T host`): a background exec with
//     stdin — the contract's `exec`, which every manager has — its output
//     read by byte offset (stdout and stderr arrive as one stream, on the
//     channel's stdout), the channel's input posted to its stdin. A manager
//     without `tty` runs a terminal request this way too, saying so.
//
// A client that leaves while the command runs ends it the way sshd does:
// HUP to its process group, and a DELETE (the group killed) when it still
// runs a moment later. One that leaves while the command starts too: the
// start is answered first (starting), since only its id can end it. Work
// meant to outlive the connection belongs in its own session (setsid,
// tmux).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
	"golang.org/x/crypto/ssh"
)

// session is one SSH session channel.
type session struct {
	t       *Tile
	lc      *liveConn
	ch      ssh.Channel
	started int64

	mu     sync.Mutex
	pty    *ptyReq
	begun  bool
	info   *entry                // the sandbox, once resolved (lc.mu)
	kind   string                // terminal | command (lc.mu)
	resize func(cols, rows int)  // the running bridge's, once there is one
	signal func(sig string) bool // likewise (exec bridge only)
}

type ptyReq struct {
	Term  string
	Cols  uint32
	Rows  uint32
	W, H  uint32
	Modes string
}

// exitInfo is how a command ended; nil fields: unknown.
type exitInfo struct {
	code   *int
	signal string
}

func (t *Tile) serveSession(ctx context.Context, lc *liveConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	s := &session{t: t, lc: lc, ch: ch, started: time.Now().UnixMilli()}
	lc.mu.Lock()
	lc.sess[s] = struct{}{}
	lc.mu.Unlock()
	defer func() {
		lc.mu.Lock()
		delete(lc.sess, s)
		lc.mu.Unlock()
	}()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var run sync.WaitGroup
	for req := range reqs {
		s.handle(sctx, req, &run)
	}
	cancel() // the client closed the channel (or the connection is gone)
	run.Wait()
	_ = ch.Close()
}

func (s *session) handle(ctx context.Context, req *ssh.Request, run *sync.WaitGroup) {
	switch req.Type {
	case "pty-req":
		var p ptyReq
		if ssh.Unmarshal(req.Payload, &p) != nil {
			_ = req.Reply(false, nil)
			return
		}
		s.mu.Lock()
		ok := !s.begun
		if ok {
			s.pty = &p
		}
		s.mu.Unlock()
		_ = req.Reply(ok, nil)
	case "window-change":
		var w struct{ Cols, Rows, W, H uint32 }
		if ssh.Unmarshal(req.Payload, &w) != nil {
			_ = req.Reply(false, nil)
			return
		}
		s.mu.Lock()
		if s.pty != nil {
			s.pty.Cols, s.pty.Rows = w.Cols, w.Rows
		}
		f := s.resize
		s.mu.Unlock()
		if f != nil && w.Cols > 0 && w.Rows > 0 {
			f(int(w.Cols), int(w.Rows))
		}
		_ = req.Reply(true, nil)
	case "shell", "exec":
		cmd := ""
		if req.Type == "exec" {
			var c struct{ Command string }
			if ssh.Unmarshal(req.Payload, &c) != nil {
				_ = req.Reply(false, nil)
				return
			}
			cmd = c.Command
		}
		s.mu.Lock()
		ok := !s.begun
		s.begun = true
		s.mu.Unlock()
		_ = req.Reply(ok, nil)
		if ok {
			run.Add(1)
			go func() {
				defer run.Done()
				s.run(ctx, cmd)
			}()
		}
	case "subsystem":
		var sub struct{ Name string }
		_ = ssh.Unmarshal(req.Payload, &sub)
		fmt.Fprintf(s.ch.Stderr(), "sandbox-terminal: the %q subsystem isn't offered (no sftp in v1): use a terminal, or pipe through `ssh <sandbox>@host 'cat > file'`\n", sub.Name)
		_ = req.Reply(false, nil)
	case "signal":
		var sg struct{ Name string }
		_ = ssh.Unmarshal(req.Payload, &sg)
		s.mu.Lock()
		f := s.signal
		s.mu.Unlock()
		ok := f != nil && f(sg.Name)
		_ = req.Reply(ok, nil)
	default: // env, x11-req, auth-agent-req@openssh.com, …: not offered
		_ = req.Reply(false, nil)
	}
}

// say writes a line of the tile's own to the client's stderr (\r\n on a
// terminal).
func (s *session) say(format string, a ...any) {
	msg := "sandbox-terminal: " + fmt.Sprintf(format, a...)
	s.mu.Lock()
	tty := s.pty != nil
	s.mu.Unlock()
	msg = strings.TrimRight(msg, "\n") + "\n"
	if tty {
		msg = strings.ReplaceAll(msg, "\n", "\r\n")
	}
	_, _ = io.WriteString(s.ch.Stderr(), msg)
}

// run resolves the sandbox, bridges the command and reports how it ended.
func (s *session) run(ctx context.Context, cmd string) {
	ex := s.bridge(ctx, cmd)
	if ctx.Err() != nil {
		return // the client left: nobody to tell
	}
	switch {
	case ex.code != nil:
		_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(*ex.code)}))
	case ex.signal != "":
		_, _ = s.ch.SendRequest("exit-signal", false, ssh.Marshal(struct {
			Signal     string
			CoreDumped bool
			Error      string
			Lang       string
		}{strings.TrimPrefix(ex.signal, "SIG"), false, "", ""}))
	}
	_ = s.ch.CloseWrite()
	_ = s.ch.Close()
}

func one(n int) exitInfo { return exitInfo{code: &n} }

// bridge runs the session's command in its sandbox — once its person may
// still use this tile (access.go: asked at the login, and again for every
// session a connection opens).
func (s *session) bridge(ctx context.Context, cmd string) exitInfo {
	t, person := s.t, s.lc.user
	why := s.lc.denied
	if why == "" {
		why = refusedWhy(t.access(ctx, person))
	}
	if why != "" {
		s.say("%s", why)
		return one(1)
	}
	es, views := t.usable(ctx, person)
	e, err := pick(es, views, s.lc.login)
	if err != nil {
		s.say("%s", err.Error())
		return one(1)
	}
	switch e.SB.State {
	case "archived", "archiving", "thawing":
		s.say("%s is %s — thaw it in its manager (%s) first", e.Login, e.SB.State, e.M.Provider)
		return one(1)
	case "deleting", "error":
		detail := e.SB.StateDetail
		if detail != "" {
			detail = ": " + detail
		}
		s.say("%s is in state %s%s", e.Login, e.SB.State, detail)
		return one(1)
	}
	s.mu.Lock()
	pty := s.pty
	s.mu.Unlock()
	kind := "command"
	if pty != nil && e.tty() {
		kind = "terminal"
	}
	s.lc.mu.Lock()
	s.info, s.kind = e, kind
	s.lc.mu.Unlock()
	if kind == "terminal" {
		return s.ttyBridge(ctx, e, cmd, pty)
	}
	if pty != nil {
		s.say("%s's manager (%s) offers no terminals: running without one", e.Login, e.M.Provider)
	}
	return s.execBridge(ctx, e, cmd)
}

// refused says what a manager's refusal means to someone at a shell.
func (s *session) refused(e *entry, err error) exitInfo {
	if r, ok := isRefusal(err); ok {
		switch r.Refusal {
		case "state":
			s.say("%s can't run commands now (it is %s): %s", e.Login, r.State, r.Msg)
		case "not-found":
			s.say("%s is gone (%s)", e.Login, r.Msg)
		case "limit":
			s.say("%s's manager is at a limit: %s", e.Login, r.Msg)
		default:
			s.say("%s", r.Error())
		}
		return one(1)
	}
	s.say("%s: %v", e.M.Provider, err)
	return one(1)
}

// ended ends an exec whose client left: HUP to its group, then — if it
// still runs after the grace — DELETE (the manager kills the group).
func (t *Tile) ended(e *entry, person, eid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second+t.hupGrace)
	defer cancel()
	route := sbxPath(e.SB.ID, "/execs/"+url.PathEscape(eid))
	_ = t.call(ctx, e.M, person, "POST", route+"/signal", map[string]any{"signal": "HUP"}, nil)
	deadline := time.Now().Add(t.hupGrace)
	for {
		var x struct {
			State string `json:"state"`
		}
		if err := t.call(ctx, e.M, person, "GET", route, nil, &x); err != nil || x.State != "running" {
			return // gone, or ended by the HUP: nothing to kill
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = t.call(ctx, e.M, person, "DELETE", route, nil, nil)
}

// leftStartGrace is how long a command's start may still take once its
// client has left: the tile waits that long for the command's id to end it.
const leftStartGrace = 2 * time.Minute

// starting is ctx for a request that starts a command: the client's
// leaving doesn't abandon it — a manager starts the command whether or not
// anyone still waits for its answer (a stopped sandbox boots first), and
// only that answer, the command's id, lets the tile end it. Once ctx ends
// the request has leftStartGrace more.
func starting(ctx context.Context) (context.Context, context.CancelFunc) {
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		grace := time.NewTimer(leftStartGrace)
		defer grace.Stop()
		select {
		case <-sctx.Done():
		case <-grace.C:
			cancel()
		}
	})
	return sctx, func() { stop(); cancel() }
}

// --- with a terminal: the tty route ----------------------------------------------------

func (s *session) ttyBridge(ctx context.Context, e *entry, cmd string, pty *ptyReq) exitInfo {
	t, person := s.t, s.lc.user
	q := url.Values{}
	if cmd != "" {
		q.Set("cmd", cmd)
	}
	if pty.Rows > 0 && pty.Cols > 0 {
		q.Set("rows", strconv.Itoa(int(pty.Rows)))
		q.Set("cols", strconv.Itoa(int(pty.Cols)))
	}
	u := e.M.URL + "/sbx" + sbxPath(e.SB.ID, "/tty")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	hdr := http.Header{}
	hdr.Set("Sbx-User", person)
	if ctx.Err() != nil {
		return exitInfo{} // the client left already: nothing to start
	}
	sctx, started := starting(ctx) // the dial starts the command
	dctx, cancel := context.WithTimeout(sctx, 30*time.Second)
	conn, resp, err := ws.Dial(dctx, u, hdr, &ws.DialOptions{Client: t.hc, MaxMessageSize: 4 << 20})
	cancel()
	started()
	if err != nil {
		if ctx.Err() != nil { // the client left while it started
			if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("ssh: %s's terminal in %s may run on: it didn't open within 30s, its client gone", person, e.SB.ID)
			}
			return exitInfo{}
		}
		if resp != nil && resp.StatusCode >= 300 {
			return s.refused(e, decodeRefusal(e.M.Provider, resp))
		}
		s.say("%s: the terminal didn't open: %v", e.M.Provider, err)
		return one(255)
	}
	defer conn.Close()
	ctl := func(v any) {
		b, _ := json.Marshal(v)
		_ = conn.WriteMessage(ws.TextMessage, b)
	}
	resize := func(cols, rows int) { ctl(map[string]any{"op": "resize", "cols": cols, "rows": rows}) }
	s.mu.Lock()
	s.resize = resize
	cols, rows := int(s.pty.Cols), int(s.pty.Rows)
	s.mu.Unlock()
	if cols > 0 && rows > 0 {
		resize(cols, rows) // first on every connect (the wire)
	}
	go func() { // keystrokes
		buf := make([]byte, 32<<10)
		for {
			n, err := s.ch.Read(buf)
			if n > 0 && conn.WriteMessage(ws.BinaryMessage, buf[:n]) != nil {
				return
			}
			if err != nil {
				return // EOF: a terminal's input just stops
			}
		}
	}()
	var (
		mu     sync.Mutex
		execID string
		ex     *exitInfo
	)
	known := make(chan struct{}) // closed once execID is set
	var knownOnce sync.Once
	done := make(chan error, 1)
	go func() { // output, then how it ended
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			if typ == ws.BinaryMessage {
				if _, err := s.ch.Write(data); err != nil {
					done <- err
					return
				}
				continue
			}
			var m struct {
				Op     string `json:"op"`
				ID     string `json:"id"`
				Code   *int   `json:"code"`
				Signal string `json:"signal"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Op {
			case "session":
				mu.Lock()
				execID = m.ID
				mu.Unlock()
				if m.ID != "" {
					knownOnce.Do(func() { close(known) })
				}
			case "exit":
				mu.Lock()
				ex = &exitInfo{code: m.Code, signal: m.Signal}
				mu.Unlock()
				done <- nil
				return
			}
		}
	}()
	var err2 error
	select {
	case err2 = <-done:
	case <-ctx.Done():
		// the client left: the session frame, the first, says which
		// command to end — wait for it if it hasn't come
		grace := time.NewTimer(leftStartGrace)
		select {
		case <-known:
		case err2 = <-done:
		case <-grace.C:
		}
		grace.Stop()
		_ = conn.Close()
	}
	mu.Lock()
	eid, end := execID, ex
	mu.Unlock()
	switch {
	case end != nil:
		return *end
	case ctx.Err() != nil: // the client left while it ran
		if eid != "" {
			t.ended(e, person, eid)
		}
		return exitInfo{}
	case ws.IsClose(err2, ws.CloseNormalClosure, ws.CloseNoStatusReceived):
		return exitInfo{} // a clean close: it ended, how isn't known
	default:
		s.say("the connection to %s's terminal was lost (%v)", e.Login, err2)
		if eid != "" {
			t.ended(e, person, eid)
		}
		return one(255)
	}
}

// --- without a terminal: a background exec ---------------------------------------------

// chunk is an exec's output read (…/output).
type chunk struct {
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Total    int64  `json:"total"`
	Data     string `json:"data"`
	State    string `json:"state"`
	ExitCode *int   `json:"exitCode"`
	Signal   string `json:"signal"`
}

func (s *session) execBridge(ctx context.Context, e *entry, cmd string) exitInfo {
	t, person := s.t, s.lc.user
	body := map[string]any{"stdin": true, "label": "ssh " + person, "timeoutMs": 0}
	if cmd != "" {
		body["cmd"] = cmd
	} else {
		sh := e.SB.Shell
		if sh == "" {
			sh = "/bin/sh"
		}
		body["argv"] = []string{sh, "-l"} // `ssh -T host`: a login shell reading its script from stdin
	}
	var x struct {
		ID string `json:"id"`
	}
	if ctx.Err() != nil {
		return exitInfo{} // the client left already: nothing to start
	}
	sctx, started := starting(ctx)
	err := t.call(sctx, e.M, person, "POST", sbxPath(e.SB.ID, "/execs"), body, &x)
	started()
	if ctx.Err() != nil { // the client left while it started
		switch {
		case err == nil:
			t.ended(e, person, x.ID)
		case errors.Is(err, context.Canceled):
			log.Printf("ssh: %s's command in %s may run on: its start wasn't answered within %s of the client leaving", person, e.SB.ID, leftStartGrace)
		}
		return exitInfo{}
	}
	if err != nil {
		return s.refused(e, err)
	}
	route := sbxPath(e.SB.ID, "/execs/"+url.PathEscape(x.ID))
	s.mu.Lock()
	s.signal = func(sig string) bool {
		switch sig = strings.TrimPrefix(strings.ToUpper(sig), "SIG"); sig {
		case "INT", "TERM", "KILL", "HUP":
			return t.call(ctx, e.M, person, "POST", route+"/signal", map[string]any{"signal": sig}, nil) == nil
		}
		return false
	}
	s.mu.Unlock()
	go func() { // the channel's input → the exec's stdin, then EOF
		buf := make([]byte, 64<<10)
		for {
			n, err := s.ch.Read(buf)
			if n > 0 {
				if t.call(ctx, e.M, person, "POST", route+"/stdin", append([]byte(nil), buf[:n]...), nil) != nil {
					_, _ = io.Copy(io.Discard, s.ch) // it stopped reading: drop the rest
					return
				}
			}
			if err == io.EOF {
				_ = t.call(ctx, e.M, person, "POST", route+"/stdin?eof=1", []byte{}, nil)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var since int64
	fails := 0
	for {
		var c chunk
		q := fmt.Sprintf("/output?since=%d&max=%d&waitMs=25000&encoding=base64", since, 256<<10)
		err := t.call(ctx, e.M, person, "GET", route+q, nil, &c)
		if ctx.Err() != nil {
			t.ended(e, person, x.ID)
			return exitInfo{}
		}
		if err != nil {
			if r, ok := isRefusal(err, "lost", "not-found"); ok {
				s.say("the command is gone: %s", r.Msg)
				return one(255)
			}
			if fails++; fails > 5 {
				s.say("%s stopped answering: %v", e.M.Provider, err)
				t.ended(e, person, x.ID)
				return one(255)
			}
			time.Sleep(time.Duration(fails) * 500 * time.Millisecond)
			continue
		}
		fails = 0
		if c.Start > since {
			s.say("%d bytes of output were dropped (the manager's output ring overflowed)", c.Start-since)
		}
		if c.Data != "" {
			b, err := base64.StdEncoding.DecodeString(c.Data)
			if err == nil {
				if _, err := s.ch.Write(b); err != nil {
					t.ended(e, person, x.ID)
					return exitInfo{}
				}
			}
		}
		since = max(since, c.End)
		if c.State != "running" && c.End >= c.Total {
			if c.State == "lost" {
				s.say("the command was lost (its sandbox restarted)")
				return one(255)
			}
			return exitInfo{code: c.ExitCode, signal: c.Signal}
		}
	}
}
