package broker

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- tile deployments: namespace buckets, limits, the per-tile quota ----

const (
	// deployStoreQuota bounds one tile's deployment state: its checkpoint
	// store, view repository, materialized trees (the protected primary's
	// build products included) and per-checkpoint build artifacts
	// (07-runtime §2.5, NP-07-7).
	deployStoreQuota = 10 << 30
	deployStoreWarn  = 0.9 // the alert's threshold
	maxWalkDepth     = 1024
)

// nsBucket is the rule of one data namespace whose rule isn't today's: one
// beyond main, or main's when a claimant lowered its limit or it isn't the
// scope's primary.
type nsBucket struct {
	limit      int64  // bytes: the lowest limit its claimants set, 0 for none (the scope's quota)
	ceiling    int64  // bytes: its scope's quota, raised by the scope's decisions (D203); 0: the key's own (quotaOf)
	nonPrimary bool   // its deployment isn't P(S): write-blocked first on low disk (D127q)
	label      string // how messages name it
	tile       string // the alert's tile: beyond main its quota key, which no component path equals; "" for a non-primary main
	alertDep   string // the alert's deployment: set for every namespace but the primary's main
	partition  bool   // a user partition's namespace (diskpart.go)
}

// deployFacts is what a scan measures beyond today's: usage of every
// namespace beyond main (quota key → bytes), the rules that aren't today's,
// and each tile's deployment state (tile → bytes; tiles without any are
// absent).
type deployFacts struct {
	usage   map[string]int64
	buckets map[string]nsBucket
	stores  map[string]int64
}

// SetDeploymentQuota installs the deployments plane's records (its Lookup)
// as the source of per-deployment disk limits (D127n), and with them turns on
// the monitor's tile-deployment rules, from the next scan: the namespaces
// beyond main, non-primary namespaces blocked first on low disk, and the
// per-tile quota (08-data §12; 07-runtime §2.5). Boot calls it once the
// plane's answers are installed; until then a scan is today's.
func (b *Broker) SetDeploymentQuota(records func(tile string) deployments.Found) {
	if b.disk == nil || records == nil {
		return
	}
	b.disk.mu.Lock()
	b.disk.deploy = func(d *diskMon, seen map[string]bool) deployFacts { return b.deployFacts(d, records, seen) }
	b.disk.mu.Unlock()
}

// deployFacts measures the tile-deployment half of a scan.
func (b *Broker) deployFacts(d *diskMon, records func(string) deployments.Found, seen map[string]bool) deployFacts {
	fx := deployFacts{usage: map[string]int64{}, buckets: map[string]nsBucket{}, stores: map[string]int64{}}
	limits := map[nsID]int64{} // the lowest limit each namespace's claimants set
	for _, c := range b.Reg.Components() {
		if u := b.storeUsage(d, c.Path, seen); u > 0 {
			fx.stores[c.Path] = u
		}
		rec := records(c.Path).Record
		if rec == nil || c.Scope == "" { // the workspace's namespace is no tile's to limit
			continue
		}
		for dep, dr := range rec.Deployments {
			if v := diskLimitBytes(dr); v > 0 {
				if id := nsOf(c.Scope, dep); limits[id] == 0 || v < limits[id] {
					limits[id] = v
				}
			}
		}
	}
	b.eachNamespace(func(id nsID) { // a namespace is measured as its root, its kv file included
		if k, err := id.keys(); err == nil {
			fx.usage[k.Quota], _ = treeUsage(filepath.Join(b.Reg.Root, filepath.FromSlash(k.Enc)))
			fx.buckets[k.Quota] = b.bucketOf(id, limits[id])
		}
	})
	b.partitionFacts(fx, limits) // each user partition's namespace, a bucket of its own (diskpart.go)
	for scope := range b.scopesAndWorkspace() {
		id := nsOf(scope, util.MainDeployment)
		if bk := b.bucketOf(id, limits[id]); bk.limit > 0 || bk.nonPrimary {
			k, _ := scopeKeys(scope, util.MainDeployment)
			fx.buckets[k.Quota] = bk
		}
	}
	return fx
}

// scopesAndWorkspace is every scope path, the workspace's ("") included.
func (b *Broker) scopesAndWorkspace() map[string]bool {
	out := map[string]bool{"": true}
	for s := range b.Reg.Scopes() {
		out[s] = true
	}
	return out
}

// diskLimitBytes is a deployment's diskGiB override in bytes; 0 for none or a
// value that isn't a positive number of GiB.
func diskLimitBytes(dr *deployments.DeploymentRecord) int64 {
	if dr == nil {
		return 0
	}
	if v := dr.Limits[deployments.LimitDiskGiB]; v > 0 && v <= 1<<33 {
		return v << 30
	}
	return 0
}

// bucketOf is namespace id's rule: limit, the lowest its claimants set, and
// its role, from P(S) (08-data §12 item 3: main counts as non-primary while
// P(S) isn't main).
func (b *Broker) bucketOf(id nsID, limit int64) nsBucket {
	bk := nsBucket{limit: limit, nonPrimary: id.dep != b.scopePrimary(id.scope), ceiling: b.disk.scopeQuota(id.scope)}
	named := fmt.Sprintf("%s's %q data", cmp.Or(id.scope, "the workspace"), id.dep)
	switch k, _ := scopeKeys(id.scope, id.dep); {
	case !id.main():
		bk.label, bk.tile, bk.alertDep = named, k.Quota, id.dep
	case bk.nonPrimary: // no tile field: /tile-status matches it to a component path
		bk.label, bk.alertDep = named, id.dep
	default: // the primary's main with a lowered limit: today's alert
		bk.label, bk.tile = k.Quota, k.Quota
	}
	return bk
}

// namespaceQuota is the quota of the namespace whose quota key is quotaKey:
// the lowest limit its claimants set (at the last scan), never above the
// scope's quota (raised by its decisions, D203). Seed preflight's target
// limit (08-data §12 item 4).
func (d *diskMon) namespaceQuota(quotaKey string) int64 {
	d.mu.RLock()
	bk, ok := d.buckets[quotaKey]
	d.mu.RUnlock()
	if !ok {
		return d.quotaOf(quotaKey) // a main namespace: its scope's own
	}
	return d.quotaFor(quotaKey, bk)
}

// DeploymentDiskStatus is TileDiskStatus for deployment dep of tile ("" is
// main): the footprint of the data namespace it reaches, (scope, dep), that
// namespace's quota, and whether its writes are blocked (08-data §12). A
// tile in the workspace scope reaches the workspace's, which is never split.
// For /tile-status?deployment= and the deployment's effective limits.
func (b *Broker) DeploymentDiskStatus(tile, dep string) (usage, quota int64, blocked bool) {
	d := b.disk
	if d == nil {
		return 0, 0, false
	}
	scope := ""
	if c, ok := b.Reg.Component(tile); ok {
		scope = c.Scope
	}
	if scope == "" {
		dep = util.MainDeployment
	}
	k, err := scopeKeys(scope, dep)
	if err != nil {
		return 0, 0, false
	}
	quota = d.namespaceQuota(k.Quota)
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, blocked = d.blocked[k.Quota]
	return d.usage[k.Quota], quota, blocked
}

// DiskQuota is the default per-scope disk quota in bytes (XBIN_LIMIT_DISK,
// or 50 GiB), which a scope's decisions raise (DiskQuotaFor, D203): the
// plane's limits op's ceiling where DiskCeilingFor isn't wired
// (GovHooks.DiskCeiling).
func (b *Broker) DiskQuota() int64 {
	if b.disk == nil {
		return defaultQuotaBytes
	}
	return b.disk.quota
}

// DiskLimitCheck judges a tile manager's diskGiB for deployment dep of tile,
// for POST /deployments/limits (11-contract §1.7, §1.14) (D127n): a positive
// number of GiB, at most the tile's ceiling (its scope's quota: the default,
// XBIN_LIMIT_DISK, raised by the scope's decisions, D203), set on the tile
// that roots its scope, since the (scope, name) namespace is the scope's.
func (b *Broker) DiskLimitCheck(tile, dep string, gib int64) error {
	ceiling := b.DiskQuotaFor(tile)
	c, ok := b.Reg.Component(tile)
	switch {
	case !ok:
		return nsErr(http.StatusNotFound, "", "no such tile: "+tile)
	case gib <= 0:
		return nsErr(http.StatusBadRequest, "", "diskGiB takes a positive integer")
	case gib > ceiling>>30:
		return nsErr(http.StatusBadRequest, "", fmt.Sprintf("diskGiB can't exceed the tile's ceiling (%d)", ceiling>>30))
	case c.Scope != tile:
		root, detail := c.Scope, ""
		if _, isTile := b.Reg.Component(c.Scope); !isTile || c.Scope == "" {
			root, detail = cmp.Or(c.Scope, "the workspace"), " — no tile roots it, so it stays at the tile's ceiling"
		}
		return nsErr(http.StatusConflict, deployments.KindPolicy, fmt.Sprintf("the quota of %s's %q data is set on %s%s",
			cmp.Or(c.Scope, "the workspace"), cmp.Or(dep, util.MainDeployment), root, detail))
	}
	return nil
}

// DeployStoreRoom answers whether tile's deployment state has room under
// its per-tile quota, for a new capture or materialization (07-runtime
// §2.5): nil below the quota at the last scan; at it, the state is measured
// again (a GC may have freed some), and still full is a 507 with the
// reason. Running deployments keep serving either way. nil until
// SetDeploymentQuota.
func (b *Broker) DeployStoreRoom(tile string) error {
	d := b.disk
	if d == nil {
		return nil
	}
	d.mu.RLock()
	on, u := d.deploy != nil, d.stores[tile]
	d.mu.RUnlock()
	if !on || u < deployStoreQuota {
		return nil
	}
	u = b.storeUsage(d, tile, nil)
	d.mu.Lock()
	if d.stores != nil {
		d.stores[tile] = u
	}
	d.mu.Unlock()
	if u < deployStoreQuota {
		return nil
	}
	return nsErr(http.StatusInsufficientStorage, deployments.KindState, fmt.Sprintf(
		"%s's checkpoints, materialized trees and build artifacts use %s of their %s quota: remove a deployment or purge its history to free space",
		tile, humanBytes(u), humanBytes(deployStoreQuota)))
}

// storeAlerts are the per-tile quota's alerts: a warning from 90%, critical
// when full. Tile-scoped, like a scope's quota alert.
func storeAlerts(stores map[string]int64) []Alert {
	var out []Alert
	for _, tile := range slices.Sorted(maps.Keys(stores)) {
		switch u := stores[tile]; {
		case u >= deployStoreQuota:
			out = append(out, Alert{Level: "crit", Kind: "quota", Tile: tile, Message: fmt.Sprintf(
				"%s's checkpoints, materialized trees and build artifacts fill their %s quota (%s): new captures and materializations are refused until GC or a purge frees space; running deployments keep serving",
				tile, humanBytes(deployStoreQuota), humanBytes(u))})
		case float64(u) >= deployStoreWarn*deployStoreQuota:
			out = append(out, Alert{Level: "warn", Kind: "quota", Tile: tile, Message: fmt.Sprintf(
				"%s's checkpoints, materialized trees and build artifacts use %s of their %s quota", tile, humanBytes(u), humanBytes(deployStoreQuota))})
		}
	}
	return out
}

// storeUsage is tile's deployment state on disk (07-runtime §2.5): its
// checkpoint store and view repository; under .xbin/deploy/<TileKey>/ its
// materialized trees, in-progress ones and the protected primary's build
// products, but not d/ (its deployments' logs and sandbox state); and its
// per-checkpoint build artifacts, .xbin/build/<CompKey>/c/ (the runner's
// buildcheckpoint.go). 0 for a tile in the zero state, which has none.
func (b *Broker) storeUsage(d *diskMon, tile string, seen map[string]bool) int64 {
	cs := checkpoint.Store{Root: b.Reg.Root}
	arts := filepath.Join(b.Reg.Root, ".xbin", "build", util.CompKey(tile), "c")
	store, _ := treeUsage(cs.Dir(tile))
	view, _ := treeUsage(cs.ViewDir(tile))
	return store + view + d.treesUsage(cs.TreesDir(tile), "d", seen) + d.treesUsage(arts, "", seen)
}

// treeSize is a content-addressed tree's measured size, valid while its
// directory keeps the inode it was measured at.
type treeSize struct {
	ino  uint64
	size int64
}

// treesUsage is treeUsage of a directory of content-addressed trees: a child
// named by a full tree id is complete and never changes once renamed into
// place, so its size is kept by path and inode and walked again only when it
// is replaced. skip names a child that isn't counted; seen collects the
// trees still present.
func (d *diskMon) treesUsage(dir, skip string, seen map[string]bool) (n int64) {
	fd, err := openDirNoFollow(unix.AT_FDCWD, dir)
	if err != nil {
		return 0
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	for _, name := range names {
		var st unix.Stat_t
		if name == skip || unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			n += st.Size
			continue
		}
		p, memo := filepath.Join(dir, name), isTreeID(name)
		if memo {
			if seen != nil {
				seen[p] = true
			}
			if v, ok := d.trees.Load(p); ok && v.(treeSize).ino == uint64(st.Ino) {
				n += v.(treeSize).size
				continue
			}
		}
		cfd, err := openDirNoFollow(fd, name)
		if err != nil {
			continue
		}
		size, _ := usageAt(cfd, p, 1)
		if memo {
			d.trees.Store(p, treeSize{ino: uint64(st.Ino), size: size})
		}
		n += size
	}
	return n
}

// isTreeID reports whether name is a full git object id (SHA-1 or SHA-256
// hex), the name of a materialized tree or an artifact directory.
func isTreeID(name string) bool {
	if len(name) != 40 && len(name) != 64 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// treeUsage is the bytes of every entry under dir, and how many there are
// besides directories, never following a symlink (06-security C5;
// 07-runtime §2.5): each directory is opened relative to its parent with
// O_NOFOLLOW|O_DIRECTORY, so one swapped for a link or a FIFO mid-walk fails
// to open instead of being followed or blocking, and entries are sized with
// fstatat(AT_SYMLINK_NOFOLLOW) (a link's own size, as dirUsage counts it). A
// missing dir is 0; entries deeper than maxWalkDepth aren't counted.
func treeUsage(dir string) (size int64, files int) {
	fd, err := openDirNoFollow(unix.AT_FDCWD, dir)
	if err != nil {
		return 0, 0
	}
	return usageAt(fd, dir, 0)
}

func openDirNoFollow(at int, name string) (int, error) {
	return unix.Openat(at, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

// usageAt sums the directory open at fd (named name, depth levels down),
// and closes it.
func usageAt(fd int, name string, depth int) (size int64, files int) {
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for {
		names, err := f.Readdirnames(256)
		for _, e := range names {
			var st unix.Stat_t
			if unix.Fstatat(fd, e, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				size, files = size+st.Size, files+1
				continue
			}
			if depth < maxWalkDepth {
				if cfd, err := openDirNoFollow(fd, e); err == nil {
					s, n := usageAt(cfd, name+"/"+e, depth+1)
					size, files = size+s, files+n
				}
			}
		}
		if err != nil || len(names) == 0 {
			return size, files
		}
	}
}
