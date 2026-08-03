package broker

import (
	"io/fs"
	"os"
	"path/filepath"
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
	ID     string `json:"id"`               // res:<scope>/<name>
	Scope  string `json:"scope"`            // "" = workspace
	Name   string `json:"name"`             //
	Type   string `json:"type"`             // kv|sqlite|blob|bus|cron
	Size   int64  `json:"size"`             // bytes on disk (0 for ephemeral)
	Detail string `json:"detail"`           // "N keys" | "N files" | "N jobs" | "ephemeral"
	Events int64  `json:"events,omitempty"` // bus: events published since start (cumulative — the admin UI derives events/min)
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
// for the admin runtime view.
func (b *Broker) ResourceUsage() []ResourceInfo {
	var out []ResourceInfo
	add := func(scope string, m map[string]registry.Resource) {
		for name, rr := range m {
			rt := resTarget{Scope: scope, Name: name}
			out = append(out, b.resourceUsage(scope, name, rr.Type, rt.String()))
		}
	}
	add("", b.Reg.Workspace().Resources)
	for scope, sm := range b.Reg.Scopes() {
		add(scope, sm.Resources)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *Broker) resourceUsage(scope, name, typ, id string) ResourceInfo {
	ri := ResourceInfo{ID: id, Scope: scope, Name: name, Type: typ}
	switch typ {
	case "sqlite":
		// single stat, cheap — always live
		ri.Size, _ = b.fileResSize(scope, name, "sqlite")
	case "filesystem":
		ri.Size, ri.Detail = b.cachedUsage(id, func() (int64, string) {
			size, files := b.fileResSize(scope, name, "filesystem")
			return size, plural(files, "file")
		})
	case "blob":
		ri.Size, ri.Detail = b.cachedUsage(id, func() (int64, string) {
			size, files := b.fileResSize(scope, name, "blob")
			return size, plural(files, "file")
		})
	case "kv":
		ri.Size, ri.Detail = b.cachedUsage(id, func() (int64, string) {
			keys, size := b.kvUsage(id)
			return size, plural(keys, "key")
		})
	case "cron":
		ri.Detail = plural(b.cronCount(id), "job")
	case "bus":
		ri.Detail = "ephemeral"
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
	key := util.ScopeKey(scope)
	if b.resenc != nil && b.resenc.Encrypted(key, name) {
		return dirUsage(b.resenc.CipherDir(key, name))
	}
	dir := filepath.Join(b.Reg.Root, "data", "resources", key)
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

func (b *Broker) kvUsage(bucket string) (keys int, size int64) {
	if b.kv == nil || b.kv.db == nil {
		return 0, 0
	}
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
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
