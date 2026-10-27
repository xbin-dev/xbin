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

// PolicyPerTile is tile's sandbox totals by the policy and its overrides
// alone — no grant — read without the runtime's lock: the limits store's
// defaults, which it may ask under its own lock (D203).
func (m *Manager) PolicyPerTile(tile string) PerTile { return m.policy.snapshot().For(tile).PerTile }

// GrantMax is the most a grant may raise a tile's totals to: every tile
// sandbox's memory together (total.memMiB, or ¾ of the host's), and the
// policy's bounds on one sandbox's vCPUs and disk (a grant raises one
// sandbox's maximum too). Without the runtime's lock.
func (m *Manager) GrantMax() (memMiB, vcpus, diskGiB int) {
	memMiB = m.policy.snapshot().Effective().Total.MemMiB
	if memMiB <= 0 {
		memMiB = int(hostMemBytes() / 4 * 3 >> 20)
	}
	return memMiB, 256, 65536
}

// TileHeld is what tile's running sandboxes hold — memory and vCPUs, as
// the admission booked them — and its sandboxes' disk, snapshots included,
// as last measured (bytes).
func (m *Manager) TileHeld(tile string) (memMiB, vcpus int, diskBytes int64) {
	m.mu.Lock()
	diskBytes = m.tileDiskBytesLocked(tile)
	m.mu.Unlock()
	_, memMiB, vcpus = m.booked(tile)
	return memMiB, vcpus, diskBytes
}
