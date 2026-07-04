package broker

import (
	"net/http"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/server"
)

// Component lifecycle (plans/lifecycle.md). The owner enables/disables (and,
// once the archiver lands, offloads) a component. State lives in the workspace
// manifest; the proxy refuses to spawn a non-enabled backend. Disabling also
// stops any running backend now, to free compute.

// apiLifecycleSet handles POST /api/buxon/lifecycle {component, state}.
func (b *Broker) apiLifecycleSet(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only — lifecycle is the owner's to set"})
		return
	}
	var body struct{ Component, State string }
	if err := decodeJSON(r, &body); err != nil || body.Component == "" || body.State == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component, state}"})
		return
	}
	if _, ok := b.Reg.Component(body.Component); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return
	}
	switch body.State {
	case registry.StateEnabled, registry.StateDisabled:
		// implemented
	case registry.StateOffloaded, registry.StateOffloadedFull:
		server.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "offload needs an archiver binding — not wired yet (plans/lifecycle.md phase 4)"})
		return
	default:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "state must be one of: enabled, disabled"})
		return
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if body.State == registry.StateEnabled {
			delete(ws.Lifecycle, body.Component)
			return
		}
		if ws.Lifecycle == nil {
			ws.Lifecycle = map[string]string{}
		}
		ws.Lifecycle[body.Component] = body.State
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Disabling stops the backend now (free compute); enabling lets the next
	// request re-spawn it.
	if body.State != registry.StateEnabled && b.StopBackend != nil {
		b.StopBackend(body.Component)
	}
	b.Hub.Publish(events.Event{Type: "reload", Component: body.Component})
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true", "state": body.State})
}
