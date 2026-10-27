package tilesbx

// grants.go — a tile's sandbox grants (D203, plans/tile-limits.md §5, §6):
// the tile-limits decisions raise a sandbox manager's totals (perTile
// memMiB, vcpus, diskGiB) above the policy's, as a layer of their own — the
// policy file and the admin's overrides are never written — and a single
// sandbox's maximum with them, so one sandbox may use the whole grant (a
// grant no sandbox could use would be a trap). They apply at the next
// admission; a running sandbox's own size stays its manager's (PATCH). A
// tile meeting a total — a start refused at it, a sandbox stopped over its
// disk, one the OOM killer took — is a hit at the cap (Deps.AtCap).

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/limits"
)

// raised is l with a grant laid over it: each of the tile's totals at
// least the grant's, and one sandbox's maximum too. A zero grant changes
// nothing.
func (l Limits) raised(memMiB, vcpus, diskGiB int) Limits {
	l.PerTile.MemMiB, l.PerSandbox.MaxMemMiB = max(l.PerTile.MemMiB, memMiB), max(l.PerSandbox.MaxMemMiB, memMiB)
	l.PerTile.VCPUs, l.PerSandbox.MaxVCPUs = max(l.PerTile.VCPUs, vcpus), max(l.PerSandbox.MaxVCPUs, vcpus)
	l.PerTile.DiskGiB, l.PerSandbox.MaxDiskGiB = max(l.PerTile.DiskGiB, diskGiB), max(l.PerSandbox.MaxDiskGiB, diskGiB)
	return l
}

// granted is tile's limits l raised by its grants (Deps.Granted).
func (m *Manager) granted(tile string, l Limits) Limits {
	if f := m.deps.Granted; f != nil {
		return l.raised(f(tile))
	}
	return l
}

// atCap tells Deps.AtCap that tile met its total of kind k, value. Callers
// don't hold m.mu.
func (m *Manager) atCap(tile string, k limits.Kind, value int, format string, args ...any) {
	if f := m.deps.AtCap; f != nil {
		f(tile, k, int64(value), fmt.Sprintf(format, args...))
	}
}

// leafOOMKills is how many processes the OOM killer took in a run's leaf.
func (m *Manager) leafOOMKills(leaf string) int64 {
	if f := m.oomKills; f != nil {
		return f(leaf)
	}
	return m.cg.OOMKills(leaf)
}
