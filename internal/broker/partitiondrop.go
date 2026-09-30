package broker

// partitiondrop.go — deleting one person's partition of a tile whole
// (plans/partitions/06 §6, §9; 11 §3): a reset, an orphan's purge and the
// orphan sweep share dropOnePartition. What goes is the tile's own — its
// person's terminal layers and agent history, its records, registrations,
// vault and log of every deployment, the namespaces when the tile roots its
// scope (a partitioned tile that doesn't root its scope uses no scope
// resources: 01 §3 rule 1), the other planes' stores — and last the backup
// keys that sealed exactly that: part:<tile>/<dep>/<pkey>, in the tile's
// own key store, under the tile's own backup lock. The scope root's keys
// and locks are never the member's to take.

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xbin-dev/xbin/internal/util"
)

// ---- drop hooks: the planes that keep a partition's data beside these ----

// partitionDropHook is one plane's part of deleting one user partition
// whole (a reset, a purge, the orphan sweep): a later plane deletes its own
// store of (tile, dep, pkey) here — called once for each deployment the
// partition has data in. (F6's mail needs none: DropPartition drops the
// person's inbox.) The partition's
// instance and terminals are stopped, its new sessions refused, and the
// tile's backups held, when it runs. The backup keys are dropOnePartition's
// to erase, after every hook succeeded.
type partitionDropHook struct {
	name string
	drop func(b *Broker, tile, dep, pkey string) error
}

var partitionDropHooks []partitionDropHook

func registerPartitionDropHook(h partitionDropHook) {
	partitionDropHooks = append(partitionDropHooks, h)
}

// partitionLogDir is a user partition's log directory,
// .xbin/partition/<TileKey>/<dep>/<pkey> (the runner writes backend.log in it).
func (b *Broker) partitionLogDir(tile, dep, pkey string) (string, error) {
	dep = cmp.Or(dep, util.MainDeployment)
	if tile == "" || !util.DeploymentNameOK(dep) || !util.PartitionKeyOK(pkey) {
		return "", fmt.Errorf("%s: no partition log for %q, %q", tile, dep, pkey)
	}
	return filepath.Join(b.Reg.Root, ".xbin", "partition", util.TileKey(tile), dep, pkey), nil
}

// partitionDrops counts the holds on partitions being deleted (workspace
// root, tile, pkey): no instance of one starts meanwhile
// (ShouldRunPartition asks).
var (
	partitionDropsMu sync.Mutex
	partitionDrops   = map[string]int{}
)

func (b *Broker) dropKey(tile, pkey string) string { return b.Reg.Root + "\x00" + tile + "\x00" + pkey }

// holdPartitionDrop keeps partition pkey of tile from starting until
// release: a reset holds it from before its stop until its data is gone.
func (b *Broker) holdPartitionDrop(tile, pkey string) (release func()) {
	k := b.dropKey(tile, pkey)
	partitionDropsMu.Lock()
	partitionDrops[k]++
	partitionDropsMu.Unlock()
	return func() {
		partitionDropsMu.Lock()
		defer partitionDropsMu.Unlock()
		if partitionDrops[k]--; partitionDrops[k] <= 0 {
			delete(partitionDrops, k)
		}
	}
}

// partitionDropping reports a partition held by holdPartitionDrop.
func (b *Broker) partitionDropping(tile, pkey string) bool {
	partitionDropsMu.Lock()
	defer partitionDropsMu.Unlock()
	return partitionDrops[b.dropKey(tile, pkey)] > 0
}

// dropSummary is what deleting one partition removed.
type dropSummary struct {
	Namespaces int   `json:"namespaces"`
	Layers     int   `json:"layers"`
	Histories  int   `json:"histories"`
	Subkeys    int   `json:"subkeys"`
	Bytes      int64 `json:"bytes"`
}

// rootsOwnScope reports whether tile's scope resources are its own (it
// roots its scope, or has none named): only then are the namespaces of its
// scope its people's partitions' data. A partitioned tile inside another
// tile's scope uses no scope resources (01 §3 rule 1): the root's
// namespaces, and their part: keys, are the root's.
func (b *Broker) rootsOwnScope(tile string) bool {
	c, ok := b.Reg.Component(tile)
	return !ok || c.Scope == "" || c.Scope == tile
}

// partitionDeps are the deployments of tile partition pkey has anything in
// — records, registrations and vault, its log, and (roots) its namespaces
// — plus dep, the current primary: a partition kept by an older primary
// (before a promotion) goes whole too. Sorted.
func (b *Broker) partitionDeps(tile, dep, pkey string, roots bool) []string {
	deps := []string{cmp.Or(dep, util.MainDeployment)}
	add := func(d string) {
		if util.DeploymentNameOK(d) && !slices.Contains(deps, d) {
			deps = append(deps, d)
		}
	}
	_ = b.eachPartitionDir(tile, func(d partitionDirOf) {
		if d.pkey == pkey {
			add(d.dep)
		}
	})
	if roots {
		_ = b.eachPartitionNamespace(tile, func(id nsID) {
			if id.pkey == pkey {
				add(id.dep)
			}
		})
	}
	for _, base := range []string{b.partVaultDir(tile), filepath.Join(b.Reg.Root, ".xbin", "partition", util.TileKey(tile))} {
		ents, _ := os.ReadDir(base) // walk-ok: xbind's own (data/vault; the runner's log tree)
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Lstat(filepath.Join(base, e.Name(), pkey+".json")); err == nil {
				add(e.Name())
			} else if _, err := os.Lstat(filepath.Join(base, e.Name(), pkey)); err == nil {
				add(e.Name())
			}
		}
	}
	slices.Sort(deps)
	return deps
}

// dropOnePartition deletes user partition pkey of tile whole, in every
// deployment it has data in (dep is the current primary): its person's new
// sessions are refused throughout (F7a's hold, held until the keys are
// erased), their sessions end and their layers and history go, then — under
// the tile's backup lock — its namespaces (only when tile roots its scope,
// which owns them), registrations, record, vault, log and the other planes'
// stores (partitionDropHooks), and last its backup keys are erased
// (part:<tile>/<dep>/<pkey> for each such dep), so no backup seals the
// deleted data under a fresh key in between (11 §3). The caller stopped its
// instance first.
func (b *Broker) dropOnePartition(tile, dep, pkey, reason, by string) (dropSummary, error) {
	var sum dropSummary
	if tm := b.partitionTerminals(); tm != nil {
		defer tm.HoldPartition(tile, pkey)() // no session of it opens between the terminals' wipe and the drop
	}
	tw, err := b.wipePersonTerminalsOf(tile, pkey, false)
	sum.Layers, sum.Histories = tw.Layers, tw.Histories
	if err != nil {
		return sum, fmt.Errorf("ending the partition's terminals: %w", err)
	}
	roots := b.rootsOwnScope(tile)
	defer b.holdBackups(tile)() // the tile whose data goes; never its scope root's
	deps := b.partitionDeps(tile, dep, pkey, roots)
	var errs []error
	if roots {
		var ids []nsID
		_ = b.eachPartitionNamespace(tile, func(id nsID) {
			if id.pkey == pkey {
				ids = append(ids, id)
			}
		})
		for _, id := range ids {
			if dir, err := b.nsDir(id); err == nil {
				n, _ := treeUsage(dir)
				sum.Bytes += n
			}
			if err := b.dropPartitionNS(id); err != nil {
				errs = append(errs, err)
				continue
			}
			sum.Namespaces++
		}
	}
	for _, d := range deps {
		errs = append(errs, b.DropPartition(tile, d, pkey))
		if dir, err := b.partitionLogDir(tile, d, pkey); err == nil {
			errs = append(errs, os.RemoveAll(dir)) // xbind's own: the runner writes it
			removeEmptyDirs(filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
		}
		for _, h := range partitionDropHooks {
			if err := h.drop(b, tile, d, pkey); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", h.name, err))
			}
		}
	}
	b.forgetPartitionBytes(tile, pkey)
	if err := errors.Join(errs...); err != nil {
		return sum, err // the keys stay until what they seal is gone: a retry erases them
	}
	sum.Subkeys = b.erasePartitionKeysHeld(tile, deps, pkey, reason, by)
	return sum, nil
}

// erasePartitionKeysHeld erases partition pkey's part: key of each of deps
// of tile, in tile's own key store — the caller holds tile's backup lock
// and has deleted what the keys sealed — and answers how many went. Each
// erase goes through erasePartitionBackupsHeld (backup_partition.go), so it
// is in the tile's history; the owner is the tile alone, never its scope
// root (whose part: key seals the root's namespaces, not this tile's).
func (b *Broker) erasePartitionKeysHeld(tile string, deps []string, pkey, reason, by string) int {
	n := 0
	for _, d := range deps {
		erased, _, err := b.erasePartitionBackupsHeld([]string{tile}, d, pkey, reason, by)
		n += erased
		if err != nil {
			slog.Warn("partitions: a partition's backup key erase", "tile", tile, "subject", partitionBackupSubject(tile, d, pkey), "err", err)
		}
	}
	return n
}
