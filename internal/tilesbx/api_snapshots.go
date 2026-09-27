package tilesbx

// api_snapshots.go — snapshots and restores (§3.9); a clone is POST
// /sandboxes with from. Not built yet: each route answers unsupported after
// its gates.

import "net/http"

// ServeSnapshots answers GET /sandboxes/{name}/snapshots.
func (m *Manager) ServeSnapshots(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("snapshots"))
	}
}

// ServeSnapshot answers POST /sandboxes/{name}/snapshots {name, clientId}: 201.
func (m *Manager) ServeSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("snapshots"))
	}
}

// ServeRestore answers POST /sandboxes/{name}/snapshots/{sid}/restore.
func (m *Manager) ServeRestore(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("snapshots"))
	}
}

// ServeDeleteSnapshot answers DELETE /sandboxes/{name}/snapshots/{sid}: 204.
func (m *Manager) ServeDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("snapshots"))
	}
}
