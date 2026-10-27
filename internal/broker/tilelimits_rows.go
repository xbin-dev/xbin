package broker

import (
	"sort"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
)

// The shapes GET /limits answers (plans/tile-limits.md §W, docs/protocol.md
// §Tile limits): a row per tile, a kind per limit, the pending items the
// shell's prompts read.

// limitRow is one tile's limits as a viewer sees them.
type limitRow struct {
	Tile       string                     `json:"tile"`
	Scope      string                     `json:"scope"`
	ScopeKey   string                     `json:"scopeKey"`
	RootsScope bool                       `json:"rootsScope"`
	Org        string                     `json:"org,omitempty"`
	Limits     map[limits.Kind]*limitKind `json:"limits"`
}

// limitKind is one kind of a row.
type limitKind struct {
	Effective int64            `json:"effective"`
	Default   int64            `json:"default"`
	Max       int64            `json:"max"`
	Enforced  bool             `json:"enforced"`
	Ask       int64            `json:"ask,omitempty"`     // the manifest's standing ask above the default
	Covered   bool             `json:"covered,omitempty"` // the manifest asks no more than the default
	Pending   bool             `json:"pending,omitempty"`
	Set       *limits.Decision `json:"set,omitempty"`
	Approved  *limits.Decision `json:"approved,omitempty"`
	Dormant   bool             `json:"dormant,omitempty"`
	Usage     int64            `json:"usage,omitempty"`
	Applied   int64            `json:"applied,omitempty"`
	AppliesAt string           `json:"appliesAt,omitempty"`
	Hit       *limits.Hit      `json:"hit,omitempty"`
	Note      string           `json:"note,omitempty"`
	Approvers []string         `json:"approvers,omitempty"`
	CanDecide bool             `json:"canDecide,omitempty"`
	CanRevert bool             `json:"canRevert,omitempty"`
	Choices   []limits.Choice  `json:"choices,omitempty"`
}

// pendingItem is one thing waiting for an approver: a manifest's ask, or a
// tile at its cap.
type pendingItem struct {
	Tile       string      `json:"tile"`
	Scope      string      `json:"scope,omitempty"` // diskGiB: the scope (tile roots it)
	Kind       limits.Kind `json:"kind"`
	Unit       string      `json:"unit"`
	Why        string      `json:"why"` // "ask" | "cap"
	Ask        int64       `json:"ask,omitempty"`
	Hit        *limits.Hit `json:"hit,omitempty"`
	Effective  int64       `json:"effective"`
	Approvable bool        `json:"approvable"`
	Mine       bool        `json:"mine"`
	Approvers  []string    `json:"approvers"`
}

// shown reports whether kind k belongs in tile's row: it applies, the
// manifest asks it (a note says why it's ignored), or the store holds
// something of it.
func shown(kf kindFacts, e limits.Entry, hit bool) bool {
	return kf.applies() || kf.ask > 0 && kf.note != "" || !e.Empty() || hit
}

// rowOf is tf's row for v from f. caps (the capacities, by kind) fills a
// decider's choices; nil leaves them out.
func (b *Broker) rowOf(v limitsViewer, tf tileFacts, f *limits.File, caps map[limits.Kind]capInput) *limitRow {
	row := &limitRow{Tile: tf.tile, Scope: tf.scope, ScopeKey: tf.scopeKey, RootsScope: tf.rootsScope, Org: tf.org,
		Limits: map[limits.Kind]*limitKind{}}
	for _, k := range limits.Kinds {
		kf := tf.kinds[k]
		key := tf.keyOf(k)
		e := entryIn(f, key, k)
		h, hit := hitIn(f, key, k)
		if k == limits.DiskGiB && !tf.rootsScope {
			e, hit = limits.Entry{}, false // the scope's, shown on the tile that roots it
		}
		if !shown(kf, e, hit) {
			continue
		}
		if hit && h.Deployment != "" && !v.admin {
			hit = false // a non-primary deployment's hit: admins only (D127h)
		}
		lk := &limitKind{Default: kf.def, Max: kf.max, Enforced: kf.applies(), Usage: kf.usage, Note: kf.note,
			Set: e.Set, Approved: e.Ask}
		lk.Effective = kf.def
		if kf.applies() {
			lk.Effective = kf.effective(e)
		}
		switch {
		case kf.ask > kf.def:
			lk.Ask = kf.ask
		case kf.ask > 0:
			lk.Covered = true
		}
		lk.Pending = kf.pending(tf, e)
		lk.Dormant = e.Ask != nil && kf.ask <= kf.def
		if kf.applied > 0 && kf.applied != lk.Effective {
			lk.Applied, lk.AppliesAt = kf.applied, "next-start"
		}
		if k == limits.VMMemMiB || k == limits.VMVCPUs {
			lk.AppliesAt = "next-start"
		}
		if hit {
			lk.Hit = &h
		}
		if kf.applies() {
			want := limits.Step(k, lk.Effective, limits.Pcts[0])
			if lk.Pending {
				want = kf.wanted()
			}
			lk.Approvers = b.approversFor(tf.tile, k, want)
			lk.CanDecide = b.mayDecideKind(v, tf.tile, k, kf.def)
			lk.CanRevert = b.mayRevert(v, tf.tile)
			if ci, ok := caps[k]; ok && lk.CanDecide {
				lk.Choices = b.choicesFor(v, tf.tile, k, lk.Effective, ci.at(f, key))
			}
		}
		row.Limits[k] = lk
	}
	return row
}

// limitTiles are the tiles a list shows: every live tile with a row v may
// read, with its facts.
func (b *Broker) limitTiles(v limitsViewer, f *limits.File) []tileFacts {
	var out []tileFacts
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() || !b.mayReadLimits(v, c.Path, false) {
			continue
		}
		tf := b.tileFacts(c)
		for _, k := range limits.Kinds {
			key := tf.keyOf(k)
			_, hit := hitIn(f, key, k)
			e := entryIn(f, key, k)
			if k == limits.DiskGiB && !tf.rootsScope {
				e, hit = limits.Entry{}, false
			}
			if shown(tf.kinds[k], e, hit) {
				out = append(out, tf)
				break
			}
		}
	}
	return out
}

// pendingFor is what waits for v: every manifest ask v may approve or whose
// tile v manages, every hit likewise.
func (b *Broker) pendingFor(v limitsViewer, f *limits.File) []pendingItem {
	out := []pendingItem{}
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() {
			continue
		}
		var tf tileFacts
		gathered := false
		mine := v.person && b.mayManageTile(v.p, c.Path)
		for _, k := range limits.Kinds {
			key := limitKeyOf(c, k)
			if key == "" {
				continue
			}
			e := entryIn(f, key, k)
			h, hit := hitIn(f, key, k)
			if hit && h.Deployment != "" && !v.admin {
				hit = false
			}
			if !hit && c.Manifest.LimitAsk(k) == 0 {
				continue // nothing could wait: no runtime question asked
			}
			if !gathered {
				tf, gathered = b.tileFacts(c), true
			}
			kf := tf.kinds[k]
			if !kf.applies() {
				continue
			}
			eff := kf.effective(e)
			if kf.pending(tf, e) {
				want := kf.wanted()
				ok, _ := b.mayApprove(v, c.Path, k, want)
				if ok || mine {
					out = append(out, b.pendingItem(tf, k, "ask", eff, ok, mine, want))
					out[len(out)-1].Ask = kf.ask
				}
			}
			if hit {
				ok := false
				for _, ch := range limits.Choices(k, eff, limits.Capacity{Kind: k}) {
					if a, _ := b.mayApprove(v, c.Path, k, ch.Value); a {
						ok = true
						break
					}
				}
				if ok || mine {
					out = append(out, b.pendingItem(tf, k, "cap", eff, ok, mine, limits.Step(k, eff, limits.Pcts[0])))
					out[len(out)-1].Hit = &h
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Tile < out[j].Tile })
	return out
}

func (b *Broker) pendingItem(tf tileFacts, k limits.Kind, why string, eff int64, approvable, mine bool, want int64) pendingItem {
	it := pendingItem{Tile: tf.tile, Kind: k, Unit: k.Meta().Unit, Why: why, Effective: eff, Approvable: approvable, Mine: mine,
		Approvers: b.approversFor(tf.tile, k, want)}
	if k == limits.DiskGiB {
		it.Scope = tf.scope
	}
	return it
}

// limitKeyOf is the store key of c's kind k, "" when c can't hold it (disk
// on a tile that doesn't root its scope).
func limitKeyOf(c *registry.Component, k limits.Kind) string {
	if k != limits.DiskGiB {
		return c.Path
	}
	if c.Scope == "" || c.Scope != c.Path {
		return ""
	}
	return limitScopeKey(c.Scope)
}
