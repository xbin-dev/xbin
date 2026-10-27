package deployments

// limits.go — a tile's own ceilings (D203, plans/tile-limits.md §5): the
// workspace's per-tile caps (TileLimits, XBIN_LIMIT_MEM's) raised by the
// tile's decisions, which every deployment's limits and every person's
// partition inherit as their ceiling — per leaf, not shared (D127n) — and
// the scope's disk quota, raised by the scope's.

import "github.com/xbin-dev/xbin/internal/cgroup"

// tileLimits is tile's cgroup caps: TileCeiling's when boot installed it
// (stepTileLimits: max(the default, the tile's grant)), else TileLimits.
func (p *Plane) tileLimits(tile string) cgroup.Limits {
	if f := p.TileCeiling; f != nil {
		return f(tile)
	}
	return p.TileLimits
}

// limitCeiling is limit k's ceiling for tile's deployments: memMiB and pids
// the tile's caps' (0: unbounded, as without cgroup delegation); diskGiB
// its scope's quota in GiB — DiskCeilingFor's, else the default's — and -1
// when the disk quota isn't wired at all.
func (p *Plane) limitCeiling(tile, k string) int64 {
	switch k {
	case LimitMemMiB:
		return p.tileLimits(tile).MemMax >> 20
	case LimitPids:
		return p.tileLimits(tile).PidsMax
	case LimitDiskGiB:
		switch g := p.gov(); {
		case g.DiskCeilingFor != nil:
			return g.DiskCeilingFor(tile) >> 30
		case g.DiskCeiling != nil:
			return g.DiskCeiling() >> 30
		}
		return -1
	}
	return 0
}
