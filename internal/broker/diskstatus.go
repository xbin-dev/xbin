package broker

// diskstatus.go — the disk monitor's answers for the rest of xbind: the
// alerts it holds, the extra sources it folds in, and one tile's footprint
// and alerts (the tile status API). Moved out of broker.go (its size
// budget).

import "github.com/xbin-dev/xbin/internal/util"

// DiskAlerts returns the current workspace disk/limit alerts.
func (b *Broker) DiskAlerts() []Alert { return b.disk.Alerts() }

// SetLimitAlerts injects extra alert sources (e.g. cgroup at-limit events)
// that the monitor folds into DiskAlerts. Called from main after wiring.
func (b *Broker) SetLimitAlerts(fn func() []Alert) {
	if b.disk != nil {
		b.disk.extra = fn
	}
}

// TileDiskStatus resolves a component to its scope's disk footprint/quota/block
// state (for the tile status API).
func (b *Broker) TileDiskStatus(component string) (usage, quota int64, blocked bool) {
	scope := ""
	if c, ok := b.Reg.Component(component); ok {
		scope = c.Scope
	}
	k, _ := scopeKeys(scope, util.MainDeployment)
	return b.disk.Status(k.Quota)
}

// TileAlerts returns the alerts relevant to one component (its own tile-scoped
// alerts plus any workspace-wide/system alerts).
func (b *Broker) TileAlerts(component string) []Alert {
	var out []Alert
	for _, a := range b.DiskAlerts() {
		if a.System || a.Tile == component {
			out = append(out, a)
		}
	}
	return out
}
