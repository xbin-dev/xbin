package tilesbx

// api_lifecycle.go — start, stop, reset and rebase (§3.4). Each runs its
// transition (lifecycle.go, stop.go, reset.go) and answers the sandbox as
// it stands once the transition is done or ?wait=<seconds> runs out
// (waitMaxSec when absent; 0: without waiting) — the transition goes on
// after the answer, and GET says where it got. A start that failed leaves
// the sandbox stopped with why in stateDetail; a refusal answers the
// refusal.

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
	m.transition(w, r, k, d.Name, func() error { return m.Start(k, d.Name) })
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
	m.transition(w, r, k, d.Name, func() error { return m.Stop(k, d.Name, why) })
}

// ServeReset answers POST /sandboxes/{name}/reset?wait= (the manager):
// stop it if it runs, put its state aside for the confined remover, clear
// its base pin, start it again if it ran (reset.go).
func (m *Manager) ServeReset(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	m.transition(w, r, k, d.Name, func() error { return m.Reset(k, d.Name) })
}

// ServeRebase answers POST /sandboxes/{name}/rebase?wait= (the manager):
// stop it if it runs, pin its kept state to the current base, start it
// again if it ran (reset.go).
func (m *Manager) ServeRebase(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	m.transition(w, r, k, d.Name, func() error { return m.Rebase(k, d.Name) })
}

// transition runs f within the call's ?wait and answers.
func (m *Manager) transition(w http.ResponseWriter, r *http.Request, k Key, name string, f func() error) {
	wait, err := waitOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	done, err := within(wait, f)
	if !done {
		err = nil // still going: the sandbox as it stands
	}
	m.answerTransition(w, k, name, err)
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
