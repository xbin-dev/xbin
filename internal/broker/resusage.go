package broker

import (
	"cmp"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// ResourceInfo is a provisioned resource plus its on-disk usage.
type ResourceInfo struct {
	ID    string `json:"id"`    // res:<scope>/<name>
	Scope string `json:"scope"` // "" = workspace
	Name  string `json:"name"`  //
	// Deployment is whose data namespace the row measures (08-data §12), on
	// every row of a scope whose root tile has a deployment record or whose
	// tiles have deployments beyond main: main's rows say "main". Beyond
	// main only the storage types have rows.
	Deployment string `json:"deployment,omitempty"`
	Type       string `json:"type"`             // kv|sqlite|blob|bus|cron
	Size       int64  `json:"size"`             // bytes on disk (0 for ephemeral)
	Detail     string `json:"detail"`           // "N keys" | "N files" | "N jobs" | "ephemeral"
	Events     int64  `json:"events,omitempty"` // bus: events published since start (cumulative — the admin UI derives events/min)
}

// resUsageTTL bounds how often a walk-heavy resource (filesystem/blob tree
// walk, kv bucket iteration) is re-measured. The admin tab polls /runtime
// every 2s; without this every poll re-walked every tree — for an encrypted
// container store, hundreds of thousands of cipher files per tick.
const resUsageTTL = 30 * time.Second

// resUsageEntry is one cached measurement. Immutable after publication —
// refreshes Store a fresh entry; running gates one refresher at a time.
type resUsageEntry struct {
	size    int64
	detail  string
	at      time.Time
	running atomic.Bool
}

// cachedUsage serves the last measurement and, when stale, kicks ONE
// background recompute — /runtime never blocks on a tree walk. A resource
// never measured yet reports "measuring…" until the first walk lands
// (visible for at most one 2s poll in the admin tab).
func (b *Broker) cachedUsage(id string, compute func() (int64, string)) (int64, string) {
	v, _ := b.resUsageC.LoadOrStore(id, &resUsageEntry{detail: "measuring…"})
	e := v.(*resUsageEntry)
	if time.Since(e.at) > resUsageTTL && e.running.CompareAndSwap(false, true) {
		go func() {
			size, detail := compute()
			b.resUsageC.Store(id, &resUsageEntry{size: size, detail: detail, at: time.Now()})
		}()
	}
	return e.size, e.detail
}

// ResourceUsage enumerates every declared resource with its storage footprint,
// for the admin runtime view: main's namespace of each scope with what main's
// own code declares (the registry's, the primary's code, while main is the
// primary), and every namespace beyond main that the scope's tiles claim,
// with what that deployment's code declares (P22).
func (b *Broker) ResourceUsage() []ResourceInfo {
	var out []ResourceInfo
	add := func(scope, dep, label string, m map[string]registry.Resource) {
		for name, rr := range m {
			if dep != util.MainDeployment && !storageType(rr.Type) {
				continue // bus and cron hold no data of a namespace's own
			}
			rt := resTarget{Scope: scope, Name: name}
			ri := b.resourceUsageIn(scope, name, rr.Type, rt.String(), dep)
			ri.Deployment = label
			out = append(out, ri)
		}
	}
	add("", util.MainDeployment, "", b.Reg.Workspace().Resources)
	beyond := map[string]map[string]bool{} // scope → the deployments beyond main its tiles have
	for _, c := range b.Reg.Components() {
		if _, names := b.deploymentsOf(c.Path); c.Scope != "" && len(names) > 1 {
			for _, n := range names {
				if n != util.MainDeployment {
					if beyond[c.Scope] == nil {
						beyond[c.Scope] = map[string]bool{}
					}
					beyond[c.Scope][n] = true
				}
			}
		}
	}
	for scope, sm := range b.Reg.Scopes() {
		label := ""
		if len(beyond[scope]) > 0 || b.hasDeploymentRecord(scope) {
			label = util.MainDeployment
		}
		set, _ := b.declaredFrom(scope, sm, util.MainDeployment) // declaredIn's
		add(scope, util.MainDeployment, label, set)
		for _, dep := range slices.Sorted(maps.Keys(beyond[scope])) {
			set, _ := b.declaredFrom(scope, sm, dep)
			add(scope, dep, dep, set)
		}
	}
	rank := func(ri ResourceInfo) string { // main's row before the others of its id
		if ri.Deployment == util.MainDeployment {
			return ""
		}
		return ri.Deployment
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return rank(out[i]) < rank(out[j])
	})
	return out
}

// storageType reports whether a resource type holds data on disk.
func storageType(typ string) bool {
	return typ == "kv" || typ == "blob" || typ == "sqlite" || typ == "filesystem"
}

// hasDeploymentRecord reports whether tile has a deployment record (the
// plane's primary summary answers only for one).
func (b *Broker) hasDeploymentRecord(tile string) bool {
	_, _, _, ok := brokerPolicy{b}.PrimarySummary(tile)
	return ok
}

// resourceUsageIn measures a resource in deployment dep's namespace ("" or
// main: today's keys).
func (b *Broker) resourceUsageIn(scope, name, typ, id, dep string) ResourceInfo {
	ri := ResourceInfo{ID: id, Scope: scope, Name: name, Type: typ}
	rt, cacheKey := resTarget{Scope: scope, Name: name}, id
	if dep = cmp.Or(dep, util.MainDeployment); dep != util.MainDeployment {
		cacheKey = id + "\x00" + dep
	}
	switch typ {
	case "sqlite":
		// single stat, cheap — always live
		ri.Size, _ = b.fileResSizeIn(scope, name, "sqlite", dep)
	case "filesystem":
		ri.Size, ri.Detail = b.cachedUsage(cacheKey, func() (int64, string) {
			size, files := b.fileResSizeIn(scope, name, "filesystem", dep)
			return size, plural(files, "file")
		})
	case "blob":
		ri.Size, ri.Detail = b.cachedUsage(cacheKey, func() (int64, string) {
			size, files := b.fileResSizeIn(scope, name, "blob", dep)
			return size, plural(files, "file")
		})
	case "kv":
		ri.Size, ri.Detail = b.cachedUsage(cacheKey, func() (int64, string) {
			keys, size := b.kvUsageIn(rt, dep)
			return size, plural(keys, "key")
		})
	case "cron":
		ri.Detail = plural(b.cronCount(id), "job")
	case "bus":
		ri.Detail = "ephemeral"
		if n := b.bus.countFor(id); n > 0 {
			ri.Detail += " · " + plural(n, "push subscription")
		}
		ri.Events = b.busEventCount(id)
	}
	return ri
}

// fileResSize measures a file-backed resource's real on-disk footprint,
// honoring encryption: once a resource is encrypted its bytes are CIPHERTEXT
// under data/resources-enc/<key>/<name> (the plaintext dir is just an empty
// mountpoint), so the old plaintext-only scan reported ~0. The ciphertext is
// the honest "disk used" figure; unencrypted resources keep the legacy
// data/resources/<key> layout. (blob file counts are ciphertext counts when
// encrypted — a small gocryptfs-metadata skew.)
func (b *Broker) fileResSize(scope, name, typ string) (int64, int) {
	return b.fileResSizeIn(scope, name, typ, util.MainDeployment)
}

// fileResSizeIn is fileResSize in deployment dep's namespace. Beyond main a
// file resource is always a volume (its namespace has no plaintext tree), so
// its size is its ciphertext's, walked without following a link.
func (b *Broker) fileResSizeIn(scope, name, typ, dep string) (int64, int) {
	k, err := b.resKeys(resTarget{Scope: scope, Name: name}, dep)
	if err != nil {
		return 0, 0 // a refused name: never provisioned
	}
	if k.NS != "" {
		if b.resenc == nil {
			return 0, 0
		}
		return treeUsage(b.resenc.CipherDir(k.DirKey, k.Name))
	}
	if b.resenc != nil && b.resenc.Encrypted(k.DirKey, k.Name) {
		return dirUsage(b.resenc.CipherDir(k.DirKey, k.Name))
	}
	sk, _ := scopeKeys(scope, util.MainDeployment)
	dir := filepath.Join(b.Reg.Root, filepath.FromSlash(sk.Plain))
	if typ == "sqlite" {
		if fi, err := os.Stat(filepath.Join(dir, name+".sqlite")); err == nil {
			return fi.Size(), 1
		}
		return 0, 0
	}
	return dirUsage(filepath.Join(dir, name))
}

func dirUsage(dir string) (size int64, files int) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			size += fi.Size()
			files++
		}
		return nil
	})
	return size, files
}

func (b *Broker) kvUsage(rt resTarget) (keys int, size int64) {
	return b.kvUsageIn(rt, util.MainDeployment)
}

// kvUsageIn is kvUsage in deployment dep's namespace: its own kv file beyond
// main, which a read never creates.
func (b *Broker) kvUsageIn(rt resTarget, dep string) (keys int, size int64) {
	k, err := b.resKeys(rt, dep)
	if err != nil {
		return 0, 0
	}
	db, err := b.kvDB(k, false)
	if err != nil || db == nil {
		return 0, 0
	}
	_ = db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(k.Bucket))
		if bk == nil {
			return nil
		}
		return bk.ForEach(func(k, v []byte) error {
			keys++
			size += int64(len(k) + len(v))
			return nil
		})
	})
	return keys, size
}

func (b *Broker) cronCount(resource string) int {
	if b.cron == nil {
		return 0
	}
	b.cron.mu.Lock()
	defer b.cron.mu.Unlock()
	n := 0
	for _, j := range b.cron.jobs {
		if j.Resource == resource {
			n++
		}
	}
	return n
}

// countBusEvent bumps the published-event counter for one bus resource
// (in-memory, like the bus itself — resets with the daemon).
func (b *Broker) countBusEvent(id string) {
	v, _ := b.busEv.LoadOrStore(id, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

func (b *Broker) busEventCount(id string) int64 {
	if v, ok := b.busEv.Load(id); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

func plural(n int, unit string) string {
	s := ""
	if n != 1 {
		s = "s"
	}
	return strconv.Itoa(n) + " " + unit + s
}
