package broker

import (
	"time"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
)

// Fit (D203, plans/tile-limits.md §6): whether raising a tile's limit fits
// the workspace. The room is the runtime's (memory, vCPUs) or the disk
// monitor's (the data partition), less the 10 % reserve and what the other
// tiles were granted above their default but don't use — the owner's
// "only the granted extra counts" rule: a default is a fair share, a raise
// is a promise. The promises are re-counted under the store's lock with
// the decision, so two approvals can't both take the same room.

const capacityTTL = 5 * time.Second

// ---- the disk monitor's side (diskGiB) ----

// diskDefault is the per-scope quota's default, in GiB (XBIN_LIMIT_DISK,
// rounded up).
func (b *Broker) diskDefault() int64 {
	return limits.FromBytes(limits.DiskGiB, b.DiskQuota())
}

// diskSpace is the disk monitor's last scan: the partition's free and total
// bytes, and each scope's bytes (main's). Zero before the first scan.
func (d *diskMon) space() (free, total int64, usage map[string]int64) {
	if d == nil {
		return 0, 0, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.freeB, d.totalB, d.usage
}

// diskMax is the most a scope's quota may be: the partition less its
// reserve (0 before the first scan: no maximum known).
func (b *Broker) diskMax() int64 {
	_, total, _ := b.disk.space()
	t := total >> 30
	return t - int64(float64(t)*limits.ReserveFraction)
}

// diskUsageOf is a scope's last measured bytes, in GiB (rounded up).
func (b *Broker) diskUsageOf(scopeKey string) int64 {
	_, _, usage := b.disk.space()
	return limits.FromBytes(limits.DiskGiB, usage[scopeKey])
}

// ---- capacity ----

// capacityBase is k's room as the runtime (or the disk monitor) reports it
// — Max, Total, Free — with the reserve filled; the runtime's answer is
// kept capacityTTL. Never under the store's lock.
func (b *Broker) capacityBase(k limits.Kind) limits.Capacity {
	if k == limits.DiskGiB {
		free, total, _ := b.disk.space()
		return limits.Capacity{Kind: k, Max: b.diskMax(), Total: total >> 30, Free: free >> 30}.WithReserve()
	}
	s := &b.tl
	s.capMu.Lock()
	if time.Since(s.capAt) > capacityTTL {
		s.capCache, s.capAt = map[limits.Kind]limits.Capacity{}, time.Now()
	}
	c, ok := s.capCache[k]
	s.capMu.Unlock()
	if !ok {
		if f := s.src.Capacity; f != nil {
			c = f(k)
		}
		c.Kind = k
		if c.Max == 0 {
			c.Max = b.kindMax(k)
		}
		c = c.WithReserve()
		s.capMu.Lock()
		if s.capCache != nil {
			s.capCache[k] = c
		}
		s.capMu.Unlock()
	}
	return c
}

// promiseFacts are what Promised needs of every key that might hold a
// raise of one kind — its default, usage and manifest ask — gathered before
// the store's lock (they call out).
type promiseFacts map[string]kindFacts

// promiseFactsFor gathers k's facts for every tile (and for disk, every
// scope the disk monitor measures).
func (b *Broker) promiseFactsFor(k limits.Kind) promiseFacts {
	pf := promiseFacts{}
	if k == limits.DiskGiB {
		def := b.diskDefault()
		_, _, usage := b.disk.space()
		for key, n := range usage {
			pf[key] = kindFacts{def: def, usage: limits.FromBytes(k, n), ask: b.manifestAskOf(key, k), enforced: true, placed: true}
		}
		return pf
	}
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() {
			continue
		}
		kf := kindFacts{ask: c.Manifest.LimitAsk(k), max: b.kindMax(k)}
		kf.def, kf.enforced = b.kindDefault(c.Path, k)
		kf.usage = b.kindUsage(c.Path, k)
		pf[c.Path] = kf
	}
	return pf
}

// promised is what the keys of kind k other than except were granted above
// their default and don't use, in f. A key pf doesn't know (new since) is
// counted whole — its decisions, as if the manifest still asked them.
func promised(f *limits.File, k limits.Kind, except string, pf promiseFacts) int64 {
	var sum int64
	for key, bk := range booksOf(f, k) {
		e := bk.Limits[k]
		if key == except || e.Empty() {
			continue
		}
		kf, ok := pf[key]
		if !ok && e.Ask != nil {
			kf = kindFacts{ask: e.Ask.Value}
		}
		sum += limits.Promise(kf.effective(e), kf.usage, kf.def)
	}
	return sum
}

// capacityFor is k's capacity for key, its Promised counted from f.
func capacityFor(base limits.Capacity, f *limits.File, key string, pf promiseFacts) limits.Capacity {
	base.Promised = promised(f, base.Kind, key, pf)
	return base
}

// capInput is what one kind's capacity is computed from: the runtime's room
// and the promise facts, both gathered with no lock held.
type capInput struct {
	base limits.Capacity
	pf   promiseFacts
}

// at is the capacity for key, counted from f.
func (ci capInput) at(f *limits.File, key string) limits.Capacity {
	return capacityFor(ci.base, f, key, ci.pf)
}

// capInputs gathers kinds' capacity inputs (they call out).
func (b *Broker) capInputs(kinds ...limits.Kind) map[limits.Kind]capInput {
	out := map[limits.Kind]capInput{}
	for _, k := range kinds {
		out[k] = capInput{base: b.capacityBase(k), pf: b.promiseFactsFor(k)}
	}
	return out
}

// choicesFor are the +25/50/100 % raises from current for a viewer: each
// with whether it fits, and whether this viewer may approve it.
func (b *Broker) choicesFor(v limitsViewer, tile string, k limits.Kind, current int64, c limits.Capacity) []limits.Choice {
	out := limits.Choices(k, current, c)
	for i := range out {
		ok, why := b.mayApprove(v, tile, k, out[i].Value)
		out[i].Approvable = ok // fits says whether the workspace has the room; both grey a choice
		if !ok && out[i].Fits {
			out[i].Why = why
		}
	}
	return out
}

// rootComponent is the registry's component of tile, if a live one.
func (b *Broker) rootComponent(tile string) (*registry.Component, bool) {
	if tile == "" {
		return nil, false
	}
	c, ok := b.Reg.Component(tile)
	if !ok || c.IsTemplate() {
		return nil, false
	}
	return c, true
}
