package broker

// diskpart.go — disk accounting of user partitions' namespaces
// (plans/partitions/03 §B.7; PD-46): each is measured like a deployment's
// namespace (its quota key is its NS) and is a bucket of its own, with a
// hard ceiling — by default the tile's per-namespace one, lowerable per tile
// (partitionDiskCeilingSeam) — so one heavy person is write-blocked (507),
// not everyone. On low disk each partition is its own candidate among the
// biggest users. Its alerts name the tile and the partition key, never
// contents, and reach admins only. The admin runtime view lists each
// partition's resources as rows of their own.

import (
	"cmp"
	"log/slog"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionDiskCeilingSeam is the disk ceiling a tile manager or admin set
// for each of tile's people's partitions (POST /partitions/limits, 03 §A.5,
// §B.7), in bytes; 0 for none. The partitions API fills it; until then each
// partition's ceiling is the tile's per-namespace one.
var partitionDiskCeilingSeam = func(b *Broker, tile string) int64 { return 0 }

// partitionFacts adds every user partition's namespace to a scan's facts:
// its usage (its root, kv file included) and its bucket. limits are the
// lowest diskGiB each deployment namespace's claimants set. The scan also
// unmounts and closes what partitions left idle (reapIdlePartitions).
func (b *Broker) partitionFacts(fx deployFacts, limits map[nsID]int64) {
	err := b.eachPartitionNamespace("", func(id nsID) {
		k, err := id.keys()
		if err != nil {
			return
		}
		fx.usage[k.Quota], _ = treeUsage(filepath.Join(b.Reg.Root, filepath.FromSlash(k.Enc)))
		limit := limits[nsOf(id.scope, id.dep)] // the tile's namespace's own limit, if one is set
		if v := partitionDiskCeilingSeam(b, id.scope); v > 0 && (limit == 0 || v < limit) {
			limit = v
		}
		fx.buckets[k.Quota] = nsBucket{limit: limit, label: b.partitionNSLabel(id), tile: id.scope, alertDep: id.dep, partition: true}
	})
	if err != nil {
		slog.Warn("disk: people's partitions", "err", err)
	}
	b.reapIdlePartitions(time.Now())
}

// partitionUsageRows are the admin runtime view's rows of people's
// partitions (PD-46: admins see per-person metadata): for each partition
// namespace, each storage resource of its scope that isn't shared, measured
// in that namespace. Shared resources are today's rows.
func (b *Broker) partitionUsageRows() []ResourceInfo {
	var out []ResourceInfo
	_ = b.eachPartitionNamespace("", func(id nsID) {
		who := id.pkey
		if m, ok, _ := b.readNS(id); ok && m.Partition != nil && m.Partition.User != "" {
			who = "user:" + m.Partition.User
		}
		set, _ := b.declaredIn(id.scope, id.dep)
		for name, res := range set {
			if !storageType(res.Type) || res.Shared == registry.SharedAll || res.Shared == registry.SharedRead {
				continue
			}
			ri := b.partitionResourceUsage(id, name, res.Type)
			ri.Deployment, ri.Partition = id.dep, who
			out = append(out, ri)
		}
	})
	return out
}

// partitionResourceUsage measures one resource in partition namespace id.
func (b *Broker) partitionResourceUsage(id nsID, name, typ string) ResourceInfo {
	rt := resTarget{Scope: id.scope, Name: name}
	ri := ResourceInfo{ID: rt.String(), Scope: id.scope, Name: name, Type: typ}
	k, err := b.resKeysIn(rt, id.dep, id.pkey)
	if err != nil {
		return ri
	}
	cacheKey := ri.ID + "\x00" + id.dep + "\x00" + id.pkey
	switch typ {
	case "kv":
		ri.Size, ri.Detail = b.cachedUsage(cacheKey, func() (int64, string) {
			done := b.usePartitionKV(k.NS)
			defer done()
			keys, size := 0, int64(0)
			if db, err := b.kvDB(k, false); err == nil && db != nil {
				_ = db.View(func(tx *bolt.Tx) error {
					if bk := tx.Bucket([]byte(k.Bucket)); bk != nil {
						_ = bk.ForEach(func(key, v []byte) error { keys, size = keys+1, size+int64(len(key)+len(v)); return nil })
					}
					return nil
				})
			}
			return size, plural(keys, "key")
		})
	default:
		ri.Size, ri.Detail = b.cachedUsage(cacheKey, func() (int64, string) {
			if b.resenc == nil {
				return 0, plural(0, "file")
			}
			size, files := treeUsage(b.resenc.CipherDir(k.DirKey, k.Name))
			return size, plural(files, "file")
		})
	}
	return ri
}

// usageRank orders a resource's rows: main's first, then each of its
// partitions', then each deployment's, and so on, by name.
func usageRank(ri ResourceInfo) string {
	rank := cmp.Or(ri.Deployment, util.MainDeployment)
	if rank == util.MainDeployment {
		rank = ""
	}
	if ri.Partition == "" {
		return rank
	}
	return rank + "\x00" + ri.Partition
}
