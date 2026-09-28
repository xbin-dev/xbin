package boot

// liveroute.go — which deployment a save drives (07-runtime §6). The watcher
// loop maps each batch to its changed components (changedComponents), then
// asks the deployments plane where each one's live reload points. A tile
// without a deployment record drives main, the primary, with exactly
// today's reload event and rebuild (D119c, D119d). A tile whose live reload is
// attached to another deployment reloads and rebuilds only that one, and no
// event of today's types names it (rule C2). A tile whose live reload is
// paused drives nothing: its pinned deployments never reload on a save, and
// the plane only recounts how far its work tree moved (NP-13-12). A tile
// whose live reload target has an assigned branch (D131) deploys a batch
// only once the plane found its work tree on that branch.

import (
	"log/slog"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// liveTarget is what one changed component's live reload drives.
type liveTarget struct {
	c       *registry.Component
	dep     string // the live reload target
	primary bool   // dep is the primary: today's bare reload; otherwise a deployments op reload
	restart bool   // not a native-entry-only change, judged by the work tree's own manifest
}

// liveTargets maps one batch's changed components to what live reload
// drives. lr answers from the deployment records in memory: ("main", true)
// for a tile without a record; ("", false) while its live reload is paused,
// and while its record holds it. Such a tile yields no target; it is listed
// in paused instead, for the plane's work-tree notice. Pure: one in-memory
// lookup per changed component (D119d).
func liveTargets(reload map[string]*registry.Component, restart map[string]bool,
	lr func(tile string) (dep string, attached bool), primary func(tile string) string) (targets []liveTarget, paused []string) {
	targets = make([]liveTarget, 0, len(reload))
	for tile, c := range reload {
		dep, attached := lr(tile)
		if !attached {
			paused = append(paused, tile)
			continue
		}
		targets = append(targets, liveTarget{c: c, dep: dep, primary: dep == primary(tile), restart: restart[tile]})
	}
	return targets, paused
}

// deploymentReload is the data of a deployments event op reload: a
// non-primary deployment's frames reload once (11-contract §3.3).
type deploymentReload struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
}

// event is the reload a target announces: today's bare reload for the
// primary; a deployments op reload naming any other deployment, whose
// component stays the bare tile path.
func (t liveTarget) event() events.Event {
	if t.primary {
		return events.Event{Type: "reload", Component: t.c.Path}
	}
	return events.Event{Type: "deployments", Component: t.c.Path, Data: deploymentReload{Op: "reload", Deployment: t.dep}}
}

// liveGate is what the watcher loop asks the deployments plane
// (*deployments.Plane): where a tile's live reload points, which deployment
// is its primary, and the notice for a paused tile that a batch touched.
// Branches, GuardBatch and NoteBranch are the assigned branches' (D131):
// whether the tile's record assigns any (aware) and its target one
// (guarded, an in-memory lookup, false for every tile without a record);
// a guarded batch's deploy runs once the plane checked the work tree's
// branch; an aware tile's batch notes the branch for the follow offers.
type liveGate interface {
	LiveReload(tile string) (dep string, attached bool)
	Primary(tile string) string
	WorkTreeMoved(tile string)
	Branches(tile, dep string) (aware, guarded bool)
	GuardBatch(c *registry.Component, dep string, restart bool, deploy func(restart bool))
	NoteBranch(tile string)
}

// routeBatch drives what one batch's changed components reach: for each
// live reload target, its reload event, then a rebuild of that deployment
// unless only the native entry changed (changed is run.ChangedDeployment);
// for each paused tile, the plane's work-tree notice, which counts the drift
// off the loop, debounced.
func routeBatch(reload map[string]*registry.Component, restart map[string]bool, gate liveGate, hub *events.Hub, changed func(*registry.Component, string)) {
	targets, paused := liveTargets(reload, restart, gate.LiveReload, gate.Primary)
	for _, t := range targets {
		aware, guarded := gate.Branches(t.c.Path, t.dep)
		if guarded { // its deploy waits for the branch check, off the loop (D131)
			gate.GuardBatch(t.c, t.dep, t.restart, func(restart bool) { t.drive(hub, changed, restart) })
			continue
		}
		t.drive(hub, changed, t.restart)
		if aware {
			gate.NoteBranch(t.c.Path)
		}
	}
	for _, tile := range paused {
		gate.WorkTreeMoved(tile)
	}
}

// drive is what a save reaching t does: its reload event, then a rebuild of
// its deployment unless only the native entry changed.
func (t liveTarget) drive(hub *events.Hub, changed func(*registry.Component, string), restart bool) {
	if t.primary {
		slog.Debug("changed", "component", t.c.Path) // today's line: the save path allocates as it did (D119d)
	} else {
		slog.Debug("changed", "component", t.c.Path, "deployment", t.dep)
	}
	hub.Publish(t.event())
	if restart {
		changed(t.c, t.dep)
	}
}
