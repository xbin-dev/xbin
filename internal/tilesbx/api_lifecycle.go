package tilesbx

// api_lifecycle.go — start, stop, reset and rebase (§3.4). Start and stop
// run the lifecycle (lifecycle.go) and answer the sandbox as it stands
// afterwards: a start that failed leaves it stopped with why in
// stateDetail, a refusal answers the refusal. Reset, rebase and ?wait are
// WP-15b's.

import (
	"errors"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

// ServeStart answers POST /sandboxes/{name}/start?wait= (the manager).
func (m *Manager) ServeStart(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	m.answerTransition(w, k, d.Name, m.Start(k, d.Name))
}

// ServeStop answers POST /sandboxes/{name}/stop?wait= (the manager, or an
// admin with ?tile=): sync, then kill; the state is kept.
func (m *Manager) ServeStop(w http.ResponseWriter, r *http.Request) {
	k, ok := m.managerOrAdmin(w, r)
	if !ok {
		return
	}
	d, ok := m.lookup(w, k, r.PathValue("name"))
	if !ok {
		return
	}
	why := ""
	if !m.Manages(auth.PrincipalOf(r)) {
		why = "stopped by a workspace admin"
	}
	m.answerTransition(w, k, d.Name, m.Stop(k, d.Name, why))
}

// answerTransition answers a lifecycle call: a refusal as itself, anything
// else as the sandbox now (its stateDetail says what went wrong).
func (m *Manager) answerTransition(w http.ResponseWriter, k Key, name string, err error) {
	var e *Error
	if errors.As(err, &e) {
		writeErr(w, e)
		return
	}
	in, ok := m.infoOf(k, name)
	if !ok {
		writeErr(w, refuse(RefNotFound, "no sandbox %q", name))
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// ServeReset answers POST /sandboxes/{name}/reset (the manager): stop, wipe
// the state (confined), pin the current base.
func (m *Manager) ServeReset(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("resetting tile sandboxes"))
	}
}

// ServeRebase answers POST /sandboxes/{name}/rebase (the manager): stop,
// keep the state, pin the current base.
func (m *Manager) ServeRebase(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("rebasing tile sandboxes"))
	}
}
