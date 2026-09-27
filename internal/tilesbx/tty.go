package tilesbx

// tty.go — the TTY WebSocket (plans/tile-sandbox-runtime.md §3.7): a tty
// exec's terminal on exactly /ws/term's wire (internal/termwire), reached
// only by the manager's instance token, which relays it byte for byte to
// its consumer. The session frame comes first — its id and sandbox the
// manager's own ids when it passes sessionId and sandboxId — then the
// ring's tail, then the live stream with echo acks and pongs, and
// {"op":"exit"} with the code (or the signal) when the command ends.
//
// D88: a tty exec claimed for a user with noTerminal is refused, at its
// start and at every attach (the attach's own forUser too), and turning
// noTerminal on kills the tty execs claimed for that user (OnNoTerminal).

import (
	"net/http"
	"regexp"
	"strconv"
	"syscall"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/termwire"
)

// ttyIDRE is sessionId's and sandboxId's grammar.
var ttyIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ttyQuery is what both TTY routes take besides their command: the ids the
// session frame shows, and the person the attach is for.
type ttyQuery struct {
	sessionID, sandboxID, forUser string
}

// ttyGate checks a TTY request before anything happens: a WebSocket
// upgrade, the ids' grammar, the claim's size and D88.
func (m *Manager) ttyGate(r *http.Request) (ttyQuery, error) {
	if !websocket.IsWebSocketUpgrade(r) {
		return ttyQuery{}, refuse(RefInvalid, "a TTY route is a WebSocket upgrade")
	}
	q := r.URL.Query()
	tq := ttyQuery{sessionID: q.Get("sessionId"), sandboxID: q.Get("sandboxId"), forUser: q.Get("forUser")}
	for name, v := range map[string]string{"sessionId": tq.sessionID, "sandboxId": tq.sandboxID} {
		if v != "" && !ttyIDRE.MatchString(v) {
			return ttyQuery{}, refuse(RefInvalid, "%s must match %s", name, ttyIDRE)
		}
	}
	if len(tq.forUser) > maxClaim || hasControl(tq.forUser) {
		return ttyQuery{}, refuse(RefInvalid, "forUser must be at most %d printable characters", maxClaim)
	}
	if m.noTerminal(tq.forUser) {
		return ttyQuery{}, refuse(RefNotAllowed, "%s may not use terminals (noTerminal)", tq.forUser)
	}
	return tq, nil
}

// ServeExecTTY answers GET /sandboxes/{name}/execs/{id}/tty: a WebSocket
// on the /ws/term wire, attached to a tty exec. Only the manager's
// instance token reaches it; the manager relays it to its consumers.
func (m *Manager) ServeExecTTY(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	tq, err := m.ttyGate(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	b, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	switch {
	case !e.tty:
		writeErr(w, refuse(RefInvalid, "exec %s has no terminal (tty: false)", e.id))
		return
	case m.noTerminal(e.forUser):
		writeErr(w, refuse(RefNotAllowed, "%s may not use terminals (noTerminal)", e.forUser))
		return
	}
	m.attach(w, r, b, d, e, tq)
}

// ServeTTY answers GET /sandboxes/{name}/tty?cwd=&cmd=&rows=&cols=…: start a
// tty exec — the login shell unless cmd, labelled "terminal" and listed
// under execs — and attach, as ServeExecTTY.
func (m *Manager) ServeTTY(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	tq, err := m.ttyGate(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	req := &ExecRequest{Cwd: q.Get("cwd"), TTY: true, ForUser: tq.forUser}
	if cmd := q.Get("cmd"); cmd != "" {
		req.Cmd = cmd
	} else {
		req.Argv = []string{shellOf(d), "-l"}
	}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"rows", &req.Rows}, {"cols", &req.Cols}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 0xffff {
				writeErr(w, refuse(RefInvalid, "%s is 1…65535", p.name))
				return
			}
			*p.dst = n
		}
	}
	for _, p := range []struct {
		name string
		dst  **uint32
	}{{"uid", &req.UID}, {"gid", &req.GID}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				writeErr(w, refuse(RefInvalid, "%s is a number", p.name))
				return
			}
			id := uint32(n)
			*p.dst = &id
		}
	}
	e, b, _, err := m.startExec(k, d, req, "terminal")
	if err != nil {
		writeErr(w, err)
		return
	}
	if !m.attach(w, r, b, d, e, tq) {
		_ = m.killExec(b.execs, e, true) // nobody will ever see it
	}
}

// attach upgrades the request and attaches it to e's terminal: its live
// hub, or — once it ended — one that replays its ring's tail and says
// exit. It reports whether the socket was made.
func (m *Manager) attach(w http.ResponseWriter, r *http.Request, b *box, d *Def, e *execRec, tq ttyQuery) bool {
	conn, err := termwire.Upgrade(w, r, nil, termwire.ReadLimit)
	if err != nil {
		return false // answered
	}
	e.mu.Lock()
	if tq.forUser != "" {
		e.users[tq.forUser] = true
	}
	hub := e.hub
	e.mu.Unlock()
	if hub == nil {
		hub = termwire.NewHub(ttyReplay)
		hub.Output(e.ring.Tail(ttyReplay))
		hub.End(e.ttyExit())
	}
	hello := map[string]any{"id": e.id, "sandbox": d.Name}
	if tq.sessionID != "" {
		hello["id"] = tq.sessionID
	}
	if tq.sandboxID != "" {
		hello["sandbox"] = tq.sandboxID
	}
	hub.Attach(conn, hello, termwire.Terminal{
		Write: func(p []byte) error {
			m.touch(e.r)
			return e.writeIn(p)
		},
		Resize: func(cols, rows uint16) {
			m.touch(e.r)
			_ = e.resize(rows, cols)
		},
	})
	return true
}

// OnNoTerminal is the users API's hook (D88): user can no longer use
// terminals, so every running tty exec claimed for them — at its start or
// by an attach — is killed. It returns at once.
func (m *Manager) OnNoTerminal(user string) {
	if user == "" {
		return
	}
	m.mu.Lock()
	var tables []*execTable
	for _, boxes := range m.live {
		for _, b := range boxes {
			tables = append(tables, b.execs)
		}
	}
	m.mu.Unlock()
	for _, t := range tables {
		t.mu.Lock()
		execs := append([]*execRec(nil), t.order...)
		t.mu.Unlock()
		for _, e := range execs {
			e.mu.Lock()
			claimed := e.tty && e.state == ExecRunning && (e.forUser == user || e.users[user])
			e.mu.Unlock()
			if claimed {
				go func(e *execRec) { _ = e.signalGroup(syscall.SIGKILL, true) }(e)
			}
		}
	}
}
