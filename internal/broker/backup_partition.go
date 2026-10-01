package broker

// backup_partition.go — people's partitions in backups (plans/partitions/
// 11-backup-encryption.md §2-§4; PD-25, PD-26, PD-56; S15). Each person's
// partition of a tile gets archives of its own,
//
//	.partitions.<TileKey>.<dep>.<pkey>
//
// sealed under a backup key of its own, part:<TileKey>/<dep>/<pkey>
// (partitionBackupSubject), so erasing that one key crypto-erases the
// partition in every archive — a person's reset, a purge or a sweep after
// they were deleted, a mode switch — and nobody else's. A partition archive
// holds the partition's namespace data (when the tile roots its scope) and
// its own records: partition.json, ns.json, its vault file (its values still
// sealed by the vault) and its registration files. It is written after the
// tile's main archive, which never names it.
//
// A tile's main, data and deployment archives hold none of a person's
// partition (S15): the main manifest lists the global instance's cron jobs
// and bus subscriptions only — people's rows live in maps of their own
// (partitionregs.go: cr.part, bs.part), never the ones cronJobsFor and
// forComponent read — its data archive is main's namespace at today's keys
// (global's), and the person layers (.xbin/term-part), partition agent
// history and mail are in no archive at all.
//
// Partition archives are only ever sealed: a plaintext-vault workspace
// (--insecure-vault) writes none, and a backup says so — an archive no key
// erases would hand a person's data to the archiver for good.
//
// Restoring one is backup_partition_restore.go's; erasing one partition's
// key, and recording erasures in the tile's history, are here.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionArchiveKey is one person's partition's archive key:
// .partitions.<TileKey>.<dep>.<pkey> — no CompKey equals it (none starts
// with "."), no data or deployment archive's either, it splits by its dots
// (none of the parts holds one) and is one path segment.
func partitionArchiveKey(tile, dep, pkey string) string {
	return partitionArchivePrefix + util.TileKey(tile) + "." + cmp.Or(dep, util.MainDeployment) + "." + pkey
}

func partitionSeal(tile, dep, pkey string) archiveSeal {
	return archiveSeal{partitionBackupSubject(tile, cmp.Or(dep, util.MainDeployment), pkey), tile, backup.KindPartition}
}

// archivedPartition is one person's partition of a tile as a backup finds
// it: its deployment and id, whose it is, and what it has on disk.
type archivedPartition struct {
	dep, pkey, user, uid string
	ns                   bool   // its namespace of the scope the tile roots exists
	dir                  string // its record directory; "" without one
}

// part is the partition's wire key.
func (a archivedPartition) part() util.Partition { return util.UserPartition(a.user) }

// whose names the partition for an admin's answer (PD-46: per-person
// metadata, never content).
func (a archivedPartition) whose() string {
	if a.user == "" {
		return a.pkey
	}
	return "user:" + a.user + " (" + a.pkey + ")"
}

// partitionsOf lists tile c's people's partitions that hold anything: the
// namespaces of the scope c roots (every deployment's), and c's own record
// directories. Whose each is comes from its record, else its namespace's
// ns.json — only when the person's id and uid hash to its partition id.
func (b *Broker) partitionsOf(c *registry.Component) ([]archivedPartition, error) {
	found := map[[2]string]*archivedPartition{}
	get := func(dep, pkey string) *archivedPartition {
		k := [2]string{dep, pkey}
		if found[k] == nil {
			found[k] = &archivedPartition{dep: dep, pkey: pkey}
		}
		return found[k]
	}
	claim := func(a *archivedPartition, user, uid string) {
		if a.user == "" && user != "" && uid != "" && util.PartitionKey(user, uid) == a.pkey {
			a.user, a.uid = user, uid
		}
	}
	var errs []error
	if c.Scope == c.Path && len(escS(c.Scope)) <= maxEscS {
		errs = append(errs, b.eachPartitionDir(c.Path, func(d partitionDirOf) {
			a := get(d.dep, d.pkey)
			a.dir = d.dir
			if rec, ok, err := readPartitionRecordAt(d.dir); err == nil && ok && rec.Tile == c.Path {
				claim(a, rec.User, rec.UID)
			}
		}), b.eachPartitionNamespace(c.Path, func(id nsID) {
			a := get(id.dep, id.pkey)
			a.ns = true
			if m, ok, _ := b.readNS(id); ok && m.Partition != nil {
				claim(a, m.Partition.User, m.Partition.UID)
			}
		}))
	} else {
		errs = append(errs, b.eachPartitionDir(c.Path, func(d partitionDirOf) {
			a := get(d.dep, d.pkey)
			a.dir = d.dir
			if rec, ok, err := readPartitionRecordAt(d.dir); err == nil && ok && rec.Tile == c.Path {
				claim(a, rec.User, rec.UID)
			}
		}))
	}
	out := make([]archivedPartition, 0, len(found))
	for _, k := range slices.SortedFunc(maps.Keys(found), func(x, y [2]string) int { return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1])) }) {
		out = append(out, *found[k])
	}
	return out, errors.Join(errs...)
}

// partitionBackups is what one backup did with a tile's people's
// partitions: how many it archived, which it couldn't (and why), and — in
// a plaintext-vault workspace — why none was.
type partitionBackups struct {
	Archived int      `json:"archived"`
	Failed   []string `json:"failed,omitempty"`
	Skipped  string   `json:"skipped,omitempty"`
}

// any reports whether the backup met a person's partition at all.
func (p partitionBackups) any() bool { return p.Archived > 0 || len(p.Failed) > 0 || p.Skipped != "" }

// backupPartitions archives each person's partition of c, after its main
// archive, each under its own key and sealed under its own backup key. One
// that fails is reported and the others are still written: one person's
// volume that won't mount doesn't cost everyone else's backup. A
// plaintext-vault workspace archives none (sealed false).
func (b *Broker) backupPartitions(c *registry.Component, provider string, sealed bool) partitionBackups {
	var out partitionBackups
	parts, err := b.partitionsOf(c)
	if err != nil {
		out.Failed = append(out.Failed, "listing people's partitions: "+err.Error())
		slog.Warn("backup: people's partitions can't all be listed", "tile", c.Path, "err", err)
	}
	if len(parts) == 0 {
		return out
	}
	if !sealed {
		out.Skipped = fmt.Sprintf("%d people's partitions aren't archived: a person's partition is archived only sealed, and this workspace has no vault barrier (plaintext-vault mode)", len(parts))
		slog.Warn("backup: people's partitions aren't archived in a plaintext-vault workspace", "tile", c.Path, "partitions", len(parts))
		return out
	}
	var written []string
	for _, a := range parts {
		if a.user == "" {
			out.Failed = append(out.Failed, a.whose()+": whose partition it is can't be told (no record names it)")
			continue
		}
		key := partitionArchiveKey(c.Path, a.dep, a.pkey)
		written = append(written, a.dep+"/"+a.pkey) // a PUT that failed may have stored it all the same
		if _, err := b.putArchive(provider, key, partitionSeal(c.Path, a.dep, a.pkey), func(bw *backup.Writer) error {
			return b.writePartitionArchive(bw, c, a)
		}); err != nil {
			out.Failed = append(out.Failed, a.whose()+": "+err.Error())
			slog.Warn("backup: a person's partition isn't archived", "tile", c.Path, "deployment", a.dep, "partition", a.pkey, "err", err)
			continue
		}
		out.Archived++
	}
	b.notePartitionArchives(provider, c.Path, written) // retention finds them once the partition is gone (backup_partition_index.go)
	return out
}

// partitionResources are the resources a person's partition keeps in its
// own namespace of scope in dep: every declared one but the shared ones
// (today's keys, the global instance's archives).
func (b *Broker) partitionResources(scope, dep string) map[string]registry.Resource {
	declared, err := b.declaredIn(scope, dep)
	if err != nil {
		slog.Warn("backup: resources left out of a partition archive", "scope", scope, "deployment", dep, "err", err)
	}
	out := map[string]registry.Resource{}
	for name, res := range declared {
		if !sharedRes(res) {
			out[name] = res
		}
	}
	return out
}

// writePartitionArchive streams partition a of tile c: a schema-3 manifest
// naming whose it is, its namespace's data (when c roots its scope), then
// its records. A namespace an act holds (a reset, a restore, a removal)
// fails it: the next backup takes it.
func (b *Broker) writePartitionArchive(bw *backup.Writer, c *registry.Component, a archivedPartition) error {
	if b.vaultSealed() {
		return errVaultSealed
	}
	id := partNS(c.Scope, a.dep, a.pkey)
	var declared map[string]registry.Resource
	if a.ns {
		if act := b.busyAct(id); act != "" {
			return nsBusy(a.dep, act)
		}
		declared = b.partitionResources(c.Scope, a.dep)
	}
	m := backup.Manifest{Schema: backup.SchemaSplit, Kind: backup.KindPartition, Component: c.Path, Scope: c.Scope,
		ScopeRoot: c.Scope == c.Path, Resources: map[string]string{}, XBinVersion: b.Version,
		Created: time.Now().UTC().Format(time.RFC3339), Includes: []string{"partition"},
		Partition: &backup.PartitionRef{ID: a.pkey, User: a.user, UID: a.uid, Deployment: a.dep}}
	if a.ns {
		m.Includes = append(m.Includes, "data")
		for name, res := range declared {
			m.Resources[name] = res.Type
		}
	}
	if err := bw.Manifest(m); err != nil {
		return err
	}
	if a.ns {
		if err := b.writePartitionData(bw, c.Scope, a.dep, a.pkey, declared); err != nil {
			return err
		}
	}
	return b.writePartitionRecords(bw, c.Path, a, id)
}

// writePartitionData archives a person's namespace of scope in dep as a
// deployment archive lays data out: its kv file's buckets decoded, each
// volume that was ever written read through its decrypted view — held
// (resenc.Hold), so the idle unmount leaves it while it is read (PD-48),
// and walked beneath its top, never following a link out.
func (b *Broker) writePartitionData(bw *backup.Writer, scope, dep, pkey string, declared map[string]registry.Resource) error {
	kv := map[string]map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		k, err := b.resKeysIn(resTarget{Scope: scope, Name: name}, dep, pkey)
		if err != nil {
			return err
		}
		switch typ := declared[name].Type; {
		case typ == "kv":
			release := b.usePartitionKV(k.NS) // the idle close leaves it meanwhile
			kv[name], err = b.dumpNSKV(k)
			release()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		case fileBackedType(typ) && b.resenc != nil && b.resenc.Encrypted(k.DirKey, k.Name):
			if err := b.archiveVolume(bw, k, scope, typ); err != nil {
				return err
			}
		}
	}
	if len(kv) == 0 {
		return nil
	}
	j, _ := json.Marshal(kv)
	return bw.File(backup.KVName, 0o644, j)
}

// archiveVolume writes one of a partition's volumes into bw, held for the
// length of the walk. A volume the backup mounted itself leaves idle at
// once (resenc.Expire): the disk monitor's next idle pass unmounts it —
// unless something holds it or its instance runs by then — so a backup of a
// tile with many people doesn't keep each person's volume mounted for the
// whole idle time (PD-48).
func (b *Broker) archiveVolume(bw *backup.Writer, k resKeys, scope, typ string) error {
	mounted := volumeMounted(b.resenc, k)
	release := b.holdPartitionVolume(k)
	defer func() {
		release()
		if !mounted && resenc.PartitionVolume(k.DirKey) {
			b.resenc.Expire(k.DirKey, k.Name)
		}
	}()
	if !b.ensureVolume(k, scope, typ) {
		return fmt.Errorf("%s can't be mounted to be archived", k.Name)
	}
	prefix := map[string]string{"sqlite": backup.SQLitePrefix, "filesystem": backup.FSPrefix, "blob": backup.BlobPrefix}[typ]
	return bw.TreeBeneath(prefix+k.Name+"/", b.resMount(k, false), nil)
}

// maxPartitionRecord bounds each record file a partition archive carries.
const maxPartitionRecord = 16 << 20

// writePartitionRecords adds the partition's records, each as xbind keeps
// it: partition.json, its namespace's ns.json, its vault file (sealed by the
// vault, as on disk) and its registration files.
func (b *Broker) writePartitionRecords(bw *backup.Writer, tile string, a archivedPartition, id nsID) error {
	add := func(name, p string) error {
		f, err := os.Open(p) // walk-ok: data/ is xbind's own; no sandbox sees it
		if errNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := readCapped(f, maxPartitionRecord)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return bw.File(name, 0o600, data)
	}
	if a.dir != "" {
		if err := add(backup.PartRecordName, filepath.Join(a.dir, partRecordFile)); err != nil {
			return err
		}
	}
	if a.ns {
		if dir, err := b.nsDir(id); err == nil {
			if err := add(backup.PartNSName, filepath.Join(dir, nsMetaFile)); err != nil {
				return err
			}
		}
	}
	if vf, err := b.partVaultPath(tile, a.dep, a.pkey); err == nil {
		if err := add(backup.PartVaultName, vf); err != nil {
			return err
		}
	}
	if a.dir == "" {
		return nil
	}
	for _, file := range partRegistrationFiles {
		if err := add(backup.PartRegsPrefix+file, filepath.Join(a.dir, file)); err != nil {
			return err
		}
	}
	return nil
}

// ---- erasing one person's partition's backups ----

// ErasePartitionBackups crypto-erases person pkey's partition of deployment
// dep of tile in every backup: its part: key (11 §3), once the partition's
// data is gone. It takes tile's backup lock; a caller deleting the data
// holds it across the delete and the erase — holdBackups(tile), delete,
// erasePartitionBackupsHeld, release — so no backup archives the partition
// in between, as the partitions API's reset and purge and the records'
// sweep do (dropOnePartition, partitiondrop.go). It answers how many keys
// it erased and what the archiver did.
func (b *Broker) ErasePartitionBackups(tile, dep, pkey, reason, by string) (int, string, error) {
	defer b.holdBackups(tile)()
	return b.erasePartitionBackupsHeld([]string{tile}, dep, pkey, reason, by)
}

// erasePartitionBackupsHeld erases person pkey's partition's part: key of
// deployment dep of each tile in owners — whose backup locks the caller
// holds: the tile's own (its records' archives) and, for a tile in another
// tile's scope, the scope root's (the namespace's) — and records each erase
// in that tile's history. Every erase of a person's partition's keys goes
// through it — a sweep, a reset, a purge (dropOnePartition, partitiondrop.go:
// owners the tile alone, per deployment) — so each is in the history. It
// answers how many keys it erased and what the archivers did.
func (b *Broker) erasePartitionBackupsHeld(owners []string, dep, pkey, reason, by string) (int, string, error) {
	n, gcs, errs := 0, []string{}, []error{}
	for _, owner := range owners {
		subject := partitionBackupSubject(owner, cmp.Or(dep, util.MainDeployment), pkey)
		erased, gc, err := b.eraseBackupSubjectsHeld(owner, func(s string) bool { return s == subject }, reason, by)
		b.noteBackupErase(owner, erased, reason, by, pkey)
		n += len(erased)
		if gc != "" {
			gcs = append(gcs, gc)
		}
		errs = append(errs, err)
	}
	return n, strings.Join(gcs, "; "), errors.Join(errs...)
}

// ---- the tile's history ----

// The history ops of backups (partitionmode.go's history).
const (
	modeOpBackupErase      = "backup-erase"      // backup keys of the tile erased: wiped.subkeys, reason, partition?
	modeOpPartitionRestore = "partition-restore" // a person's partition restored from its archive: partition, reason
)

// noteBackupErase records an erase of tile's backup keys in the tile's
// history (11 §3): how many, why, by whom and — for one person's partition
// — its id. Also slog'd, by the key store.
func (b *Broker) noteBackupErase(tile string, erased []backupTombstone, reason, by, partition string) {
	if len(erased) == 0 {
		return
	}
	b.noteTileHistory(tile, modeHistory{Op: modeOpBackupErase, By: by, Reason: reason, Partition: partition,
		Wiped: map[string]int64{"subkeys": int64(len(erased))}})
}

// noteTileHistory appends h (stamped now) to tile's mode history — only
// when the tile has a mode record: a tile that never used partitions gets
// none (its zero state), and its erasures stay in slog and the tombstones.
func (b *Broker) noteTileHistory(tile string, h modeHistory) {
	pm := b.parts
	if pm == nil {
		return
	}
	pm.settleMu.Lock()
	defer pm.settleMu.Unlock()
	pm.mu.Lock()
	rec := pm.recs[tile]
	pm.mu.Unlock()
	if rec == nil {
		return
	}
	next := rec.clone(tile)
	h.At = pm.now()
	next.log(h)
	if err := pm.write(next, true); err != nil {
		slog.Warn("partitions: the tile's history", "tile", tile, "op", h.Op, "err", err)
		return
	}
	pm.mu.Lock()
	pm.recs[tile] = next
	pm.mu.Unlock()
}
