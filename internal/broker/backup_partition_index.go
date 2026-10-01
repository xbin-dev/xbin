package broker

// backup_partition_index.go — retention of people's partitions' archives
// (plans/partitions/11-backup-encryption.md §3). A scheduled backup's
// retention keeps each partition's newest versions; the archives of a
// partition that is gone since — swept, reset, purged, switched away —
// were crypto-erased with its part: key, and an archiver with POST
// /archive/erase deleted them then. One without it keeps them "until
// retention prunes them": the tile's index of the partition archives its
// backups wrote finds them, and retention deletes every version of one
// whose partition is gone and whose key is erased (unreadable either way).

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionArchiveIndex is data/backup-refs/partitions/<CompKey>.json: of
// one archiver, the partition archives backups of the tile wrote, as
// "<dep>/<pkey>". Written only once a partition archive is: a tile without
// people's partitions has none.
type partitionArchiveIndex struct {
	Provider string   `json:"provider"`
	Keys     []string `json:"keys"`
}

func (b *Broker) partitionIndexPath(comp string) string {
	return filepath.Join(b.Reg.Root, "data", "backup-refs", "partitions", util.CompKey(comp)+".json")
}

// loadPartitionIndex reads comp's index for provider (empty for another
// archiver's, or none).
func (b *Broker) loadPartitionIndex(provider, comp string) partitionArchiveIndex {
	var x partitionArchiveIndex
	if data, err := os.ReadFile(b.partitionIndexPath(comp)); err == nil { // walk-ok: data/ is xbind's own
		_ = json.Unmarshal(data, &x)
	}
	if x.Provider != provider {
		x = partitionArchiveIndex{Provider: provider}
	}
	return x
}

func (b *Broker) savePartitionIndex(comp string, x partitionArchiveIndex) {
	slices.Sort(x.Keys)
	x.Keys = slices.Compact(x.Keys)
	data, err := json.MarshalIndent(x, "", "  ")
	if err == nil {
		err = fsutil.WriteFileAtomicIn(b.partitionIndexPath(comp), data, 0o600)
	}
	if err != nil {
		slog.Warn("backup: the index of people's partition archives", "component", comp, "err", err)
	}
}

// notePartitionArchives adds the partitions a backup of comp archived at
// provider to its index (the caller holds comp's backup lock).
func (b *Broker) notePartitionArchives(provider, comp string, written []string) {
	if len(written) == 0 {
		return
	}
	x := b.loadPartitionIndex(provider, comp)
	n := len(x.Keys)
	for _, e := range written {
		if !slices.Contains(x.Keys, e) {
			x.Keys = append(x.Keys, e)
		}
	}
	if len(x.Keys) != n {
		b.savePartitionIndex(comp, x)
	}
}

// prunePartitionArchives keeps the newest keep versions of each person's
// partition's archives of comp (a scheduled backup's retention, under
// comp's backup lock). A partition the index names that is gone: while its
// key lives it is pruned as any other; once its key is erased every version
// is deleted, and it leaves the index when the archiver lists none.
func (b *Broker) prunePartitionArchives(comp string, keep int) {
	c, ok := b.Reg.Component(comp)
	provider := b.archiveProvider(comp)
	if !ok || keep <= 0 || provider == "" {
		return
	}
	parts, _ := b.partitionsOf(c)
	here := map[string]bool{}
	for _, a := range parts {
		here[a.dep+"/"+a.pkey] = true
		b.pruneKey(comp, partitionArchiveKey(comp, a.dep, a.pkey), keep)
	}
	x := b.loadPartitionIndex(provider, comp)
	if len(x.Keys) == 0 {
		return
	}
	keys, err := b.backupKeys().list()
	if err != nil {
		return // which keys live can't be told: nothing is deleted
	}
	live := map[string]bool{}
	for _, k := range keys {
		live[k.Subject] = true
	}
	kept := []string{}
	for _, e := range x.Keys {
		dep, pkey, _ := strings.Cut(e, "/")
		if !util.DeploymentNameOK(dep) || !pkeyOK(pkey) {
			continue
		}
		key := partitionArchiveKey(comp, dep, pkey)
		switch {
		case here[e]:
		case live[partitionBackupSubject(comp, dep, pkey)]: // gone from disk, its key not erased (yet)
			b.pruneKey(comp, key, keep)
		default: // gone, its key erased: every version is unreadable
			b.pruneKey(comp, key, 0)
			if vs, err := b.archiveVersions(provider, key); err == nil && len(vs) == 0 {
				continue
			}
		}
		kept = append(kept, e)
	}
	if len(kept) != len(x.Keys) {
		x.Keys = kept
		b.savePartitionIndex(comp, x)
	}
}
