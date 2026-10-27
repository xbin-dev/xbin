package broker

// diskraise.go — the disk monitor's half of tile limits (D203,
// plans/tile-limits.md §5, §6): a scope's quota is the default raised by
// the decisions on it (quotaOf); a main namespace held to its scope's
// quota offers raising it (an alert's action, decorated per viewer by
// apiAlerts), warns from 90 % (quota-near), and meeting it is a hit the
// tile-limits store keeps until a decision or an approver's dismissal. A
// decision kicks a rescan, so a raised quota lifts its 507 at once.

import (
	"fmt"
	"time"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/util"
)

// quotaNear is the share of its quota from which a scope is warned.
const quotaNear = 0.9

// diskTiles is what the disk monitor asks the broker about raised quotas:
// *Broker; nil in a bare monitor, which keeps the default for every scope.
type diskTiles interface {
	// diskGrant is what the decisions on the scope whose data key is key
	// raise its quota to, in bytes; 0 for nothing.
	diskGrant(key string) int64
	// diskScopes maps each scope's data key to its path and the tile that
	// roots it ("" for none). The workspace scope isn't in it.
	diskScopes() map[string]scopeRoot
	// diskHit records the scope at its quota (RecordLimitHit; never calls
	// out).
	diskHit(key string, h limits.Hit)
}

// scopeRoot is a scope's path and the tile that roots it, if one does.
type scopeRoot struct{ scope, tile string }

// quotaOf is the quota of the scope whose data key is key: the default,
// raised by its decisions.
func (d *diskMon) quotaOf(key string) int64 {
	q := d.quota
	if d.tiles != nil {
		q = max(q, d.tiles.diskGrant(key))
	}
	return q
}

// scopeQuota is quotaOf of the scope at path scope ("" the workspace's).
func (d *diskMon) scopeQuota(scope string) int64 { return d.quotaOf(mainQuotaKey(scope)) }

// mainQuotaKey is scope's main namespace's quota key: its data key.
func mainQuotaKey(scope string) string {
	k, _ := scopeKeys(scope, util.MainDeployment) // main's: never an error
	return k.Quota
}

// quotaFor is the quota a namespace's usage is held to: its bucket's
// ceiling (its scope's quota; a main namespace's is quotaOf its own key),
// lowered to the lowest limit its claimants set.
func (d *diskMon) quotaFor(key string, bk nsBucket) int64 {
	q := bk.ceiling
	if q <= 0 {
		q = d.quotaOf(key)
	}
	if bk.limit > 0 && bk.limit < q {
		q = bk.limit
	}
	return q
}

// scopeRoots is diskScopes' map for one scan; nil without the broker.
func (d *diskMon) scopeRoots() map[string]scopeRoot {
	if d.tiles == nil {
		return nil
	}
	return d.tiles.diskScopes()
}

// raiseAction is what an alert about the namespace key, held to quota,
// offers (Current in GiB rounded up, as the store's default is: an 8 MiB
// quota reads 1 GiB, and a raise goes past it): raising its scope's quota — only for a scope's main namespace
// while it is its primary's and the scope's quota is what holds it (a
// claimant's lower limit isn't the scope's to raise; a deployment's or a
// person's partition's namespace gets none). nil otherwise, and for the
// workspace scope, which no tile roots and the API can't name.
func raiseAction(key string, bk nsBucket, quota int64, roots map[string]scopeRoot) *limits.Action {
	sr, ok := roots[key]
	if !ok || bk.nonPrimary || bk.partition || bk.alertDep != "" || bk.limit > 0 && bk.limit <= quota {
		return nil
	}
	return &limits.Action{Action: limits.ActionRaise, Tile: sr.tile, Scope: sr.scope, Kind: limits.DiskGiB,
		Unit: limits.DiskGiB.Meta().Unit, Current: limits.FromBytes(limits.DiskGiB, quota)}
}

// raiseWords ends an alert's message with the ways to raise its quota, for
// a shell or console that shows only the text.
func raiseWords(a *limits.Action) string {
	switch {
	case a == nil:
		return ""
	case a.Tile == "":
		return " — an admin can raise it at /xbin/limits"
	}
	return fmt.Sprintf(" — raise it: the tile's ⚙ → Limits, /xbin/limits, or `bx limits raise %s disk`", a.Tile)
}

// recordFull records a hit for each scope at its quota now (full: key →
// its quota) that wasn't at the last scan.
func (d *diskMon) recordFull(full, was map[string]int64, usage map[string]int64) {
	if d.tiles == nil {
		return
	}
	now := time.Now()
	for key, quota := range full {
		if _, before := was[key]; before {
			continue
		}
		d.tiles.diskHit(key, limits.Hit{At: now, Since: now, Count: 1, Value: limits.FromBytes(limits.DiskGiB, quota),
			Detail: fmt.Sprintf("the data quota is full (%s of %s): resource writes are refused", humanBytes(usage[key]), humanBytes(quota))})
	}
}

// The broker's answers to the disk monitor (diskTiles).

func (b *Broker) diskGrant(key string) int64 { return b.LimitGrant(key, limits.DiskGiB) << 30 }

func (b *Broker) diskScopes() map[string]scopeRoot {
	out := map[string]scopeRoot{}
	for scope := range b.Reg.Scopes() {
		if scope == "" {
			continue
		}
		sr := scopeRoot{scope: scope}
		if c, ok := b.Reg.Component(scope); ok && c.Scope == scope {
			sr.tile = scope
		}
		out[mainQuotaKey(scope)] = sr
	}
	return out
}

func (b *Broker) diskHit(key string, h limits.Hit) { b.RecordLimitHit(key, limits.DiskGiB, h) }

// DiskQuotaFor is the quota of tile's scope in bytes — the default raised
// by its decisions (D203): a deployment's diskGiB ceiling (D127n), for the
// plane's limits op (GovHooks.DiskCeilingFor).
func (b *Broker) DiskQuotaFor(tile string) int64 {
	if b.disk == nil {
		return defaultQuotaBytes
	}
	scope := ""
	if c, ok := b.Reg.Component(tile); ok {
		scope = c.Scope
	}
	return b.disk.scopeQuota(scope)
}
