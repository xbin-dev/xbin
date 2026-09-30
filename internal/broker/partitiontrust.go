package broker

// partitiontrust.go — the trust base of a partitioned tile (plans/partitions
// 06 §4; PD-23, AR-2). The code runs in every person's partition, so whoever
// can change it — the tile's writers, the writers of every non-partitioned
// provider bound to it, admins — can read every person's data there. xbind
// says so where it is seen:
//
//   - the trust panel (GET /partitions' trust, for the tile's people): who
//     can change the code, whether saves reach it live, the bound
//     providers;
//   - a warning on /alerts (kind partition-trust) and in bx doctor, for
//     admins and the tile's readers, while live reload is on and people who
//     aren't admins hold write level on the tile or a bound provider.
//
// The mode's own events are here too: the partitions event, op "mode", to
// the tile's readers whenever its state changes, and the removal of a
// removed tile's mode record once nothing of its partitions is left.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// SetPartitionLiveReload installs the deployments plane's answer "do saves
// reach tile's running code" (live reload on, the primary not protected);
// nil: assumed on (the warning errs toward showing).
func (b *Broker) SetPartitionLiveReload(f func(tile string) bool) {
	o := b.partOps()
	o.mu.Lock()
	o.liveReload = f
	o.mu.Unlock()
}

func (b *Broker) liveReloadOn(tile string) bool {
	o := b.partOps()
	o.mu.RLock()
	f := o.liveReload
	o.mu.RUnlock()
	return f == nil || f(tile)
}

// codeWritersTTL is how long a tile's code writers are reused: /alerts and
// the listing ask on every poll, and the answer resolves every person's
// access. A users-plane change (PartitionPeopleChanged) drops them at once.
const codeWritersTTL = 15 * time.Second

type writersAt struct {
	w   []string
	at  time.Time
	gen uint64
}

var (
	codeWritersSeen sync.Map // root \x00 tile → writersAt
	codeWritersGen  atomic.Uint64
)

// forgetCodeWriters drops every tile's reused code writers (people changed).
func forgetCodeWriters() { codeWritersGen.Add(1) }

// codeWriters are the people who aren't admins and hold write level on
// tile (they can change its code) — reused for codeWritersTTL.
func (b *Broker) codeWriters(tile string) []string {
	k, gen := b.Reg.Root+"\x00"+tile, codeWritersGen.Load()
	if v, ok := codeWritersSeen.Load(k); ok && v.(writersAt).gen == gen && time.Since(v.(writersAt).at) < codeWritersTTL {
		return slices.Clone(v.(writersAt).w)
	}
	w := b.codeWritersNow(tile)
	codeWritersSeen.Store(k, writersAt{w: slices.Clone(w), at: time.Now(), gen: gen})
	return w
}

func (b *Broker) codeWritersNow(tile string) []string {
	var out []string
	if b.Users == nil {
		return out
	}
	for _, u := range b.Users.List() {
		if u.Disabled || u.IsAdmin() {
			continue
		}
		if acc, ok := b.Users.Access(u.ID); ok && acc.CanWriteTile(tile) {
			out = append(out, u.ID)
		}
	}
	return out
}

// boundProviders are the tiles bound to tile's slots by global binds that
// aren't partitioned (their code runs on the tile's people's data too).
func (b *Broker) boundProviders(tile string) []string {
	var out []string
	for _, bind := range b.Reg.Workspace().Bindings[tile] {
		for _, ref := range bind.Refs() {
			prov, _ := splitRef(ref)
			if _, ok := b.Reg.Component(prov); !ok || slices.Contains(out, prov) {
				continue
			}
			if _, partitioned, _ := b.tilePartitioning(prov); !partitioned {
				out = append(out, prov)
			}
		}
	}
	slices.Sort(out)
	return out
}

// globalBindsOn are tile's global binds as bx doctor lists them for review:
// slot → providers.
func (b *Broker) globalBindsOn(tile string) map[string][]string {
	out := map[string][]string{}
	for slot, bind := range b.Reg.Workspace().Bindings[tile] {
		if refs := bind.Refs(); len(refs) > 0 {
			out[slot] = refs
		}
	}
	return out
}

// requestersWithoutGlobal are the tiles that aren't partitioned and hold a
// global bind to tile, a partitioned tile without a global instance: their
// calls reach nothing (PD-12), which bx doctor reports.
func (b *Broker) requestersWithoutGlobal(tile string) []string {
	var out []string
	for req, slots := range b.Reg.Workspace().Bindings {
		if _, partitioned, _ := b.tilePartitioning(req); partitioned || slices.Contains(out, req) {
			continue
		}
		for _, bind := range slots {
			if slices.ContainsFunc(bind.Refs(), func(ref string) bool { p, _ := splitRef(ref); return p == tile }) {
				out = append(out, req)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// partitionTrust is the trust panel of a partitioned tile.
func (b *Broker) partitionTrust(tile string) map[string]any {
	provs := []map[string]any{}
	for _, prov := range b.boundProviders(tile) {
		provs = append(provs, map[string]any{"tile": prov, "writers": b.codeWriters(prov), "liveReload": b.liveReloadOn(prov)})
	}
	return map[string]any{"writers": b.codeWriters(tile), "admins": "every workspace admin", "liveReload": b.liveReloadOn(tile),
		"protected": b.primaryProtected(tile), "providers": provs, "warnings": b.trustWarnings(tile)}
}

// trustWarnings are tile's trust warnings (06 §4): live reload on while
// people who aren't admins can change its code or a bound provider's.
func (b *Broker) trustWarnings(tile string) []string {
	var out []string
	if w := b.codeWriters(tile); len(w) > 0 && b.liveReloadOn(tile) {
		out = append(out, fmt.Sprintf("%s runs its work tree live and %d people who aren't admins can change its code (%s): their saves run on every person's data in it",
			tile, len(w), strings.Join(w, ", ")))
	}
	for _, prov := range b.boundProviders(tile) {
		if w := b.codeWriters(prov); len(w) > 0 && b.liveReloadOn(prov) {
			out = append(out, fmt.Sprintf("%s is bound to %s, which runs its work tree live and %d people who aren't admins can change (%s): its code sees every person's calls",
				tile, prov, len(w), strings.Join(w, ", ")))
		}
	}
	return out
}

// partitionTrustAlerts are the /alerts rows of the trust warnings: admins
// and each partitioned tile's readers.
func (b *Broker) partitionTrustAlerts(p auth.Principal, admin bool) []Alert {
	var out []Alert
	for _, c := range b.Reg.Components() {
		if _, on := c.Partitioned(); !on || !admin && !p.CanReadTile(c.Path) {
			continue
		}
		for _, w := range b.trustWarnings(c.Path) {
			out = append(out, Alert{Level: "warn", Kind: "partition-trust", Tile: c.Path, Message: w + " (docs/partitions.md)"})
		}
	}
	return out
}

// publishPartitionMode is the partitions event, op mode: tile's settled
// state, recorded mode and request, to its readers (the component row's
// partition fields; what a manager decides is GET /partitions').
func (b *Broker) publishPartitionMode(tile string) {
	c, ok := b.Reg.Component(tile)
	if !ok || b.Hub == nil {
		return
	}
	st, rec, req := c.PartitionState()
	data := map[string]any{"op": "mode", "tile": tile, "state": st.String(), "spec": rec, "request": modeRequestView(req)}
	b.Hub.Publish(events.Event{Type: "partitions", Component: tile, Data: audienceEvent{data: data, tile: tile, readers: true}})
}

// removedTileModeRecord removes the mode record of tile once it is no
// longer registered and nothing it kept is left — no partition of its
// people, no data of its own (06 §10: after the sweep). A tile that comes
// back finds none, and records its mode again from its code.
func (b *Broker) removedTileModeRecord(tile string) {
	pm := b.parts
	if pm == nil || tile == "" {
		return
	}
	if _, registered := b.Reg.Component(tile); registered {
		return
	}
	if registry.IsOffloaded(b.Reg.LifecycleState(tile)) {
		return // offloaded, not removed: a restore brings it back to its recorded mode (and its history)
	}
	pm.settleMu.Lock()
	defer pm.settleMu.Unlock()
	pm.mu.Lock()
	_, has := pm.recs[tile]
	pm.mu.Unlock()
	if !has {
		return
	}
	left := false
	_ = b.eachPartitionDir(tile, func(partitionDirOf) { left = true })
	_ = b.eachPartitionNamespace(tile, func(nsID) { left = true })
	if held, _, _ := b.tileHoldsData(registry.PartitionAsk{Tile: tile, Scope: tile, RootsScope: true}); left || held {
		return
	}
	dir := pm.dir(tile)
	if err := os.Remove(filepath.Join(dir, modeFile)); err != nil && !errNotExist(err) {
		slog.Warn("partitions: a removed tile's mode record", "tile", tile, "err", err)
		return
	}
	removeEmptyDirs(dir)
	pm.mu.Lock()
	delete(pm.recs, tile)
	pm.mu.Unlock()
	slog.Info("partitions: a removed tile's mode record removed: nothing of it is left", "tile", tile)
}

// sweepRemovedModeRecords runs removedTileModeRecord for every recorded
// tile that isn't registered: after the partitions' sweep.
func (b *Broker) sweepRemovedModeRecords() {
	pm := b.parts
	if pm == nil {
		return
	}
	pm.mu.Lock()
	var gone []string
	for tile := range pm.recs {
		if _, ok := b.Reg.Component(tile); !ok {
			gone = append(gone, tile)
		}
	}
	pm.mu.Unlock()
	for _, tile := range gone {
		b.removedTileModeRecord(tile)
	}
}
