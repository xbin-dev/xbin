package server

// termenv.go — a tile's persistent terminal layer before any terminal is
// open (/ws/term/env): its status and its reset, moved out of server.go
// (its size budget). On a partitioned tile the layer is the caller's own
// (term.EnvStatusFor, ResetEnvFor; PD-22).

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
)

// handleTermReset wipes a component's persistent terminal layer (?cwd=<path>)
// back to the base rootfs, killing any live session on it first
// (DELETE /ws/term/env). The UI's "reset sandbox" action calls this.
func (s *Server) handleTermReset(w http.ResponseWriter, r *http.Request) {
	cwd, ok := termEnvGate(w, r)
	if !ok {
		return
	}
	if err := s.Term.ResetEnvFor(auth.PrincipalOf(r), cwd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTermEnv reports a component's persistent terminal layer (?cwd=):
// {exists, baseOutdated, baseAutoUpdate} — so the terminal window can offer
// the base update, or say the next session moves to it (D175), before any
// terminal is open (GET /ws/term/env) — and whether a VM terminal
// can open ({vm: {available, reason}}, plans/vm-sandbox.md).
func (s *Server) handleTermEnv(w http.ResponseWriter, r *http.Request) {
	cwd, ok := termEnvGate(w, r)
	if !ok {
		return
	}
	exists, old := s.Term.EnvStatusFor(auth.PrincipalOf(r), cwd)
	WriteJSON(w, http.StatusOK, map[string]any{"exists": exists, "baseOutdated": old, "baseAutoUpdate": s.Term.BaseAutoUpdateOn(), "vm": s.Term.VMStatus()})
}

// termEnvGate: a tile's dev layer is the terminal plane (it IS the terminal's
// overlay), so reading or resetting it needs terminal level on that tile
// (admins: any); the legacy root layer ("" — root terminals are disabled) is
// admin-only.
func termEnvGate(w http.ResponseWriter, r *http.Request) (string, bool) {
	cwd := r.URL.Query().Get("cwd")
	p := auth.PrincipalOf(r)
	if cwd == "" {
		if !p.IsAdmin() {
			http.Error(w, "the root layer is admin-only", http.StatusForbidden)
			return "", false
		}
	} else if !p.CanTerminalTile(strings.Trim(cwd, "/")) {
		http.Error(w, "your account doesn't have terminal access to this tile", http.StatusForbidden)
		return "", false
	}
	return cwd, true
}
