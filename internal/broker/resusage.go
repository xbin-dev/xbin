package broker

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	bolt "go.etcd.io/bbolt"

	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/util"
)

// ResourceInfo is a provisioned resource plus its on-disk usage.
type ResourceInfo struct {
	ID     string `json:"id"`     // res:<scope>/<name>
	Scope  string `json:"scope"`  // "" = workspace
	Name   string `json:"name"`   //
	Type   string `json:"type"`   // kv|sqlite|blob|bus|cron
	Size   int64  `json:"size"`   // bytes on disk (0 for ephemeral)
	Detail string `json:"detail"` // "N keys" | "N files" | "N jobs" | "ephemeral"
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
	dir := filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(scope))
	switch typ {
	case "sqlite":
		if fi, err := os.Stat(filepath.Join(dir, name+".sqlite")); err == nil {
			ri.Size = fi.Size()
		}
	case "blob":
		size, files := dirUsage(filepath.Join(dir, name))
		ri.Size = size
		ri.Detail = plural(files, "file")
	case "kv":
		keys, size := b.kvUsage(id)
		ri.Size = size
		ri.Detail = plural(keys, "key")
	case "cron":
		ri.Detail = plural(b.cronCount(id), "job")
	case "bus":
		ri.Detail = "ephemeral"
	}
	return ri
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

func plural(n int, unit string) string {
	s := ""
	if n != 1 {
		s = "s"
	}
	return strconv.Itoa(n) + " " + unit + s
}
