package broker

import (
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

// Component lifecycle (plans/lifecycle.md). The owner enables/disables (and,
// once the archiver lands, offloads) a component. State lives in the workspace
// manifest; the proxy refuses to spawn a non-enabled backend. Disabling also
// stops any running backend now, to free compute.
//
// Lifecycle is the tile's, never a deployment's (05-model §11) (D119i): a
// state set on a tile reaches every deployment it has. Disabling, hiding and
// offloading stop each one (StopBackend is the runner's Stop, which stops
// them all, and the spawn gate reads the tile's state for every deployment);
// enabling starts the primary as today, and a deployment beyond it starts on
// demand, or by its own alwaysOn switch. A qualified name has no lifecycle of
// its own: it names no component, so it answers 404.

// apiLifecycleSet handles POST /api/xbin/lifecycle {component, state}.
// Lifecycle is the OWNER'S to set (D24/D31): workspace admins anywhere, and
// a tile's user-owner / the owning org's admins on their own tiles — an org
// admin must be able to stop their runaway tile without paging a ws-admin.
func (b *Broker) apiLifecycleSet(w http.ResponseWriter, r *http.Request) {
	var body struct{ Component, State string }
	if err := server.DecodeJSON(r, &body); err != nil || body.Component == "" || body.State == "" {
		server.WriteError(w, http.StatusBadRequest, "need {component, state}")
		return
	}
	if p := auth.PrincipalOf(r); !b.IsAdmin(p) && !b.mayManageTile(p, body.Component) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "lifecycle is the owner's to set — the tile's owner, its org's admins, or a workspace admin"})
		return
	}
	// A component whose source was removed (offloaded-full) may not be in the
	// registry; only reject truly-unknown components.
	cur := b.Reg.LifecycleState(body.Component)
	if _, ok := b.Reg.Component(body.Component); !ok && cur == registry.StateEnabled {
		server.WriteError(w, http.StatusNotFound, "no such component")
		return
	}
	// Heavy transitions run before the state flips, so a failure leaves the
	// component untouched (nothing removed until its archive is confirmed).
	// filesChanged tracks whether source/data moved on disk (offload/restore),
	// which needs a rescan+provision; a plain enable/disable does not (and doing
	// it would only churn the watcher).
	filesChanged := false
	out := map[string]any{"ok": "true", "state": body.State}
	switch body.State {
	case registry.StateEnabled:
		if registry.IsOffloaded(cur) {
			// what the restore left out is answered, as POST /restore does
			r, listed, err := b.restoreTile(body.Component, "")
			if err != nil {
				server.WriteError(w, http.StatusBadGateway, "restore failed: "+err.Error())
				return
			}
			r.answer(out)
			if listed != nil {
				out["deployments"] = listed
			}
			filesChanged = true
		}
	case registry.StateDisabled:
		// no data movement
	case registry.StateHidden:
		// Disabled + filtered out of sidebars/listings (D42). No data
		// movement — but never from an offloaded state: overwriting the
		// marker would orphan the archived data.
		if registry.IsOffloaded(cur) {
			server.WriteError(w, http.StatusBadRequest, "restore the component before hiding it (it is "+cur+")")
			return
		}
	case registry.StateOffloaded:
		if err := b.offload(body.Component, false); err != nil {
			server.WriteError(w, offloadStatus(err), err.Error())
			return
		}
		filesChanged = true
	case registry.StateOffloadedFull:
		if err := b.offload(body.Component, true); err != nil {
			server.WriteError(w, offloadStatus(err), err.Error())
			return
		}
		filesChanged = true
	default:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "state must be one of: enabled, disabled, hidden, offloaded, offloaded-full"})
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
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Disabling/offloading stops the backend now (free compute): every
	// deployment's, and the tile sandboxes it manages (state kept). Enabling
	// lets the next request re-spawn it (Ensure is gated on the new state); a
	// deployment beyond the primary, the next request that addresses it.
	if body.State != registry.StateEnabled {
		b.StopBackendSafe(body.Component)
		b.stopTileSandboxes(body.Component, "its tile was "+body.State+": stopped, state kept")
	} else {
		b.wakeBackends() // an always-on primary starts now, as does a deployment whose alwaysOn switch is on; others on first request
	}
	// Only offload/restore moved files — rescan/provision + reconcile then.
	if filesChanged {
		_ = b.Reg.Rescan()
		b.Provision()
		if b.OnStructureChange != nil {
			b.OnStructureChange()
		}
	}
	b.publishLifecycle(body.Component)
	server.WriteJSON(w, http.StatusOK, out)
}

// lifecycleReload is the data of the deployments event op reload (11-contract
// §3.3): the frames of the deployment it names reload once.
type lifecycleReload struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
}

// publishLifecycle tells tile's open frames its lifecycle changed: today's
// bare reload, which speaks of the primary, whatever its name; then op
// reload naming each other deployment, since no event of today's types ever
// names one (C2). A tile without a deployment record has main alone, so it
// publishes exactly today's one event (D119c).
func (b *Broker) publishLifecycle(tile string) {
	b.Hub.Publish(events.Event{Type: "reload", Component: tile})
	primary, names := b.deploymentsOf(tile)
	for _, dep := range names {
		if dep != primary {
			b.Hub.Publish(events.Event{Type: "deployments", Component: tile,
				Data: lifecycleReload{Op: "reload", Deployment: dep}})
		}
	}
}
