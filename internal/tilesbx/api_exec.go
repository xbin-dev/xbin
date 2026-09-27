package tilesbx

// api_exec.go — run, background execs and their output, stdin, signals and
// resizes, and the TTY WebSocket (§3.5–3.7). Not built yet: each route
// answers unsupported after its gates.

import "net/http"

// ServeRun answers POST /sandboxes/{name}/run: the contract's run, blocking.
func (m *Manager) ServeRun(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("running commands (exec)"))
	}
}

// ServeExecs answers GET /sandboxes/{name}/execs.
func (m *Manager) ServeExecs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("background execs (exec)"))
	}
}

// ServeExecStart answers POST /sandboxes/{name}/execs: 201 and the exec.
func (m *Manager) ServeExecStart(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("background execs (exec)"))
	}
}

// ServeExec answers GET /sandboxes/{name}/execs/{id}.
func (m *Manager) ServeExec(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("background execs (exec)"))
	}
}

// ServeExecKill answers DELETE /sandboxes/{name}/execs/{id}: kill the
// group, forget the exec.
func (m *Manager) ServeExecKill(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("background execs (exec)"))
	}
}

// ServeOutput answers GET /sandboxes/{name}/execs/{id}/output?since=&max=&waitMs=&encoding=.
func (m *Manager) ServeOutput(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("exec output (exec)"))
	}
}

// ServeStdin answers POST /sandboxes/{name}/execs/{id}/stdin[?eof=1].
func (m *Manager) ServeStdin(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("exec stdin (exec)"))
	}
}

// ServeSignal answers POST /sandboxes/{name}/execs/{id}/signal.
func (m *Manager) ServeSignal(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("exec signals (exec)"))
	}
}

// ServeResize answers POST /sandboxes/{name}/execs/{id}/resize (a tty exec).
func (m *Manager) ServeResize(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("terminals (tty)"))
	}
}

// ServeExecTTY answers GET /sandboxes/{name}/execs/{id}/tty: a WebSocket
// on the /ws/term wire, attached to a tty exec. Only the manager's
// instance token reaches it; the manager relays it to its consumers.
func (m *Manager) ServeExecTTY(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("terminals (tty)"))
	}
}

// ServeTTY answers GET /sandboxes/{name}/tty?cwd=&cmd=&rows=&cols=…: start a
// tty exec (the login shell unless cmd) and attach, as ServeExecTTY.
func (m *Manager) ServeTTY(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("terminals (tty)"))
	}
}
