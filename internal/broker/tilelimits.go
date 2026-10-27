package broker

import (
	"github.com/xbin-dev/xbin/internal/limits"
)

// Tile resource limits (D203, docs/isolation.md §Resource limits): a tile's
// manifest asks for more memory, disk or sandbox/VM room than the default
// ("limits", "vm"), an approver approves or raises it, and the runtime
// applies max(default, grant). The decisions and the at-cap hits live in
// data/tile-limits.json (limits.File) — xbind-owned, never the tile's.
//
// The seams below are the contract between the store (this file and its
// siblings: tilelimits_api.go, tilelimits_fit.go, tilelimits_alerts.go) and
// the runtime that feeds and obeys it (boot's stepTileLimits: the runner,
// the VM and tile-sandbox runtimes, the disk monitor).

// LimitSources are the runtime's answers the store needs, wired at boot
// (stepTileLimits). Every value is in the kind's unit. The disk kind
// (diskGiB) is the broker's own — the disk monitor's default quota, a
// scope's usage, the partition's room — and never asks these.
type LimitSources struct {
	// Default is kind k's default for tile, and whether the kind is
	// enforced there at all (memMiB without cgroup delegation is not; a VM
	// kind on a tile that runs no VM is not).
	Default func(tile string, k limits.Kind) (def int64, enforced bool)
	// Max is the most any tile may have of k (0 = no maximum).
	Max func(k limits.Kind) int64
	// Usage is what tile uses of k now (0 = unknown or none).
	Usage func(tile string, k limits.Kind) int64
	// Capacity is the workspace's room for k: Max, Total and Free. The
	// store fills Reserve and Promised (only the granted extra counts).
	Capacity func(k limits.Kind) limits.Capacity
	// Apply makes a decision take effect: raise a running backend's
	// memory.max, recompute a tile's sandbox book; restart restarts the
	// backend (a VM's size, a crash-looping tile). Called with no store
	// lock held.
	Apply func(tile string, k limits.Kind, restart bool)
}

// tileLimitState is the broker's limit state; the store's fields join it.
type tileLimitState struct {
	src LimitSources
}

// SetLimitSources wires the runtime's side (boot, before the server step).
func (b *Broker) SetLimitSources(s LimitSources) { b.tl.src = s }

// LimitGrant is what the decisions on key raise kind k to — the store's
// limits.Effective less the default's floor: 0 when nothing raises it. The
// runtime applies max(default, grant). key is a tile path, or for diskGiB
// the scope's data key (util.ScopeKey).
func (b *Broker) LimitGrant(key string, k limits.Kind) int64 {
	return 0 // the store (D203 W1)
}

// RecordLimitHit notes key meeting its kind-k cap: an OOM kill, a full
// quota, a sandbox refused or stopped at its tile's total. The hit is
// sticky — an alert with a raise action — until a decision on the kind or
// an approver's dismissal; repeated hits coalesce (Count, At). Safe to call
// from any goroutine, with any runtime lock held: it never calls out.
func (b *Broker) RecordLimitHit(key string, k limits.Kind, h limits.Hit) {
	// the store (D203 W1)
}
