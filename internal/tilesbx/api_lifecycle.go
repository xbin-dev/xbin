package tilesbx

// api_lifecycle.go — start, stop, reset and rebase (§3.4). The lifecycle
// isn't built yet: each route answers unsupported after its gates.

import "net/http"

// ServeStart answers POST /sandboxes/{name}/start?wait= (the manager).
func (m *Manager) ServeStart(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("starting tile sandboxes"))
	}
}

// ServeStop answers POST /sandboxes/{name}/stop?wait= (the manager, or an
// admin with ?tile=): sync, then kill; the state is kept.
func (m *Manager) ServeStop(w http.ResponseWriter, r *http.Request) {
	k, ok := m.managerOrAdmin(w, r)
	if !ok {
		return
	}
	if _, ok := m.lookup(w, k, r.PathValue("name")); ok {
		writeErr(w, notBuilt("stopping tile sandboxes"))
	}
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
