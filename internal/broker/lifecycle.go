package broker

import (
	"net/http"
	"time"

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
	// A component whose source was removed (offloaded-full) may not be in the
	// registry; only reject truly-unknown components.
	cur := b.Reg.LifecycleState(body.Component)
	if _, ok := b.Reg.Component(body.Component); !ok && cur == registry.StateEnabled {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return
	}
	// Heavy transitions run before the state flips, so a failure leaves the
	// component untouched (nothing removed until its archive is confirmed).
	// filesChanged tracks whether source/data moved on disk (offload/restore),
	// which needs a rescan+provision; a plain enable/disable does not (and doing
	// it would only churn the watcher).
	filesChanged := false
	switch body.State {
	case registry.StateEnabled:
		if registry.IsOffloaded(cur) {
			if _, err := b.doRestore(body.Component, ""); err != nil {
				server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "restore failed: " + err.Error()})
				return
			}
			filesChanged = true
		}
	case registry.StateDisabled:
		// no data movement
	case registry.StateOffloaded:
		if err := b.offload(body.Component, false); err != nil {
			server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		filesChanged = true
	case registry.StateOffloadedFull:
		if err := b.offload(body.Component, true); err != nil {
			server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		filesChanged = true
	default:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "state must be one of: enabled, disabled, offloaded, offloaded-full"})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if body.State == registry.StateEnabled {
			delete(ws.Lifecycle, body.Component)
			delete(ws.LifecycleAt, body.Component)
			return
		}
		if ws.Lifecycle == nil {
			ws.Lifecycle = map[string]string{}
		}
		if ws.LifecycleAt == nil {
			ws.LifecycleAt = map[string]string{}
		}
		ws.Lifecycle[body.Component] = body.State
		ws.LifecycleAt[body.Component] = now
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Disabling/offloading stops the backend now (free compute); enabling lets
	// the next request re-spawn it (Ensure is gated on the new state).
	if body.State != registry.StateEnabled {
		b.StopBackendSafe(body.Component)
	}
	// Only offload/restore moved files — rescan/provision + reconcile then.
	if filesChanged {
		_ = b.Reg.Rescan()
		b.Provision()
		if b.OnStructureChange != nil {
			b.OnStructureChange()
		}
	}
	b.Hub.Publish(events.Event{Type: "reload", Component: body.Component})
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true", "state": body.State})
}
