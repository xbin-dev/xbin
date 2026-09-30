package broker

// partitionrecords.go — people's partitions' own records, beside their
// registrations (plans/partitions/03 §D-§E, 01 §2.2, §2.6; PD-20, PD-26,
// PD-43). Each user partition of a tile that registered something, keeps a
// vault or started has a directory of its own,
//
//	data/partitions/<TileKey>/<dep>/<pkey>/
//	    partition.json        whose it is (this file)
//	    cron.json, bus-subscriptions.json,
//	    iface-instances.json, ingress-hosts.json   (partitionregs.go)
//
// beside the tile's mode record (data/partitions/<TileKey>/mode.json), and
// its vault at data/vault/.partitions/<TileKey>/<dep>/<pkey>.json
// (partitionvault.go). All of it is xbind's own, under data/, which no
// sandbox ever sees (D118). An older xbind never reads any of it, so it
// never fires a person's job as the tile's.
//
// partition.json names the person (user id and uid, PD-43) and when the
// partition was made, so that:
//   - the registrations of the directory are loaded at boot for the right
//     tile (a TileKey is a hash);
//   - a person whose uid an older xbind's rewrite of the users store dropped
//     adopts it back (adoptablePartitionUID, beside the namespaces' ns.json);
//   - a person's deletion, or their id held by someone new, orphans the
//     partition (the same rules as its namespaces', partitionOrphanEvent),
//     and the sweep deletes an orphan after partitionRetention — never while
//     the tile is paused; a tile that comes back reclaims it.
//
// A missing partition.json is rebuilt from the namespaces' ns.json at the
// sweep (boot's included); a missing record never orphans anything.
//
// Three stores of 01 §2.2 are these ("partition-vault",
// "partition-registrations", "partition-records"), each with the wipe hook
// of its name (01 §2.6): a switch that deletes everything removes every
// person's; removing or adding "global" keeps them (owner ruling H1).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	partRecordFile    = "partition.json"
	partRecordSchema  = 1
	partStateActive   = "active"
	partStateOrphaned = "orphaned"
)

// partitionRecord is one user partition's partition.json (03 §E). dormant
// (disabled, or lost read) isn't stored: it is the liveness gate's answer
// at every use (PD-20). The crash metadata admins see (PD-24) — the last
// exit the runner's crash watch saw, how many there were, and whether its
// breaker holds the instance — survives a restart of xbind here
// (NotePartitionExit); a start clears crashLoop.
type partitionRecord struct {
	Schema      int    `json:"schema"`
	Tile        string `json:"tile"`
	Dep         string `json:"dep"`
	User        string `json:"user"`
	UID         string `json:"uid"`
	Created     string `json:"created"` // RFC 3339, nanoseconds
	LastStarted string `json:"lastStarted,omitempty"`
	State       string `json:"state"`            // active | orphaned
	Reason      string `json:"reason,omitempty"` // an orphan's event: user-deleted | tile-removed
	Orphaned    string `json:"orphaned,omitempty"`
	Rebuilt     bool   `json:"rebuilt,omitempty"`  // made from the namespaces' ns.json
	LastExit    string `json:"lastExit,omitempty"` // RFC 3339: an exit nothing asked for
	Restarts    int    `json:"restarts,omitempty"` // such exits, since the record was made
	CrashLoop   bool   `json:"crashLoop,omitempty"`
}

// partTarget is one user partition of a tile's deployment: whose its
// registrations, vault and record are.
type partTarget struct {
	tile, dep, pkey string
	part            util.Partition
	user, uid       string
}

// partTargetOf is the partition p — a principal of tile acting in a user
// partition (the partition gate stamped it) — keeps its registrations and
// vault in: the deployment it acts in, which is the primary (people's
// partitions run nowhere else, PD-17), and its person's pkey, minted at
// their first partition.
func (b *Broker) partTargetOf(p auth.Principal, tile string) (partTarget, error) {
	id, ok := p.Partition.User()
	if !ok || p.Component != tile {
		return partTarget{}, fmt.Errorf("%s: this credential acts in no person's partition of it", tile)
	}
	dep, err := b.addressed(p, tile)
	if err != nil {
		return partTarget{}, err
	}
	dep = cmp.Or(dep, util.MainDeployment)
	if !b.isPrimary(tile, dep) {
		return partTarget{}, fmt.Errorf("%s: a person's partition runs only in the primary", tile)
	}
	pkey, uid, err := b.partitionKeyOf(id)
	if err != nil {
		return partTarget{}, fmt.Errorf("%s: %s's partition: %w", tile, id, err)
	}
	return partTarget{tile: tile, dep: dep, pkey: pkey, part: p.Partition, user: id, uid: uid}, nil
}

// partitionRecordDir is data/partitions/<TileKey>/<dep>/<pkey>.
func (b *Broker) partitionRecordDir(tile, dep, pkey string) (string, error) {
	dep = cmp.Or(dep, util.MainDeployment)
	if tile == "" || !util.DeploymentNameOK(dep) || !util.PartitionKeyOK(pkey) {
		return "", fmt.Errorf("%s: no partition directory for %q, %q", tile, dep, pkey)
	}
	return filepath.Join(b.Reg.Root, "data", partitionsDir, util.TileKey(tile), dep, pkey), nil
}

func (t partTarget) dir(b *Broker) (string, error) {
	return b.partitionRecordDir(t.tile, t.dep, t.pkey)
}

// partRecLocks serializes a workspace's record writes (root → *sync.Mutex).
var partRecLocks sync.Map

func (b *Broker) partRecLock() *sync.Mutex {
	v, _ := partRecLocks.LoadOrStore(b.Reg.Root, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// readPartitionRecordAt reads dir's partition.json; ok false when there is
// none. A record of another schema (a newer xbind's) is an error: never read
// as this one's, never written over.
func readPartitionRecordAt(dir string) (partitionRecord, bool, error) {
	var rec partitionRecord
	data, err := os.ReadFile(filepath.Join(dir, partRecordFile)) // walk-ok: data/partitions is xbind's own
	if errNotExist(err) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, false, fmt.Errorf("%s: %w", filepath.Join(dir, partRecordFile), err)
	}
	if rec.Schema != partRecordSchema {
		return rec, false, fmt.Errorf("%s has schema %d, which this xbind doesn't read", filepath.Join(dir, partRecordFile), rec.Schema)
	}
	return rec, true, nil
}

func writePartitionRecordAt(dir string, rec partitionRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeFileIn(dir, partRecordFile, append(data, '\n'))
}

// writeFileIn writes file in dir (made 0700 if missing) atomically, 0600:
// a partition's own files are xbind's alone.
func writeFileIn(dir, file string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, file), data, 0o600)
}

// notePartition makes sure t's partition.json exists before anything of the
// partition is stored — its first registration, vault key or start — and,
// started, stamps the start. A tile-removed orphan whose tile is back is
// active again. A record this xbind can't read is kept as it is: an error.
func (b *Broker) notePartition(t partTarget, started bool) error {
	dir, err := t.dir(b)
	if err != nil {
		return err
	}
	mu := b.partRecLock()
	mu.Lock()
	defer mu.Unlock()
	rec, ok, err := readPartitionRecordAt(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	switch {
	case !ok:
		rec = partitionRecord{Schema: partRecordSchema, Tile: t.tile, Dep: cmp.Or(t.dep, util.MainDeployment), User: t.user,
			UID: t.uid, Created: now.Format(time.RFC3339Nano), State: partStateActive}
	case rec.State == partStateOrphaned && rec.Reason == orphanTileRemoved:
		rec.State, rec.Reason, rec.Orphaned = partStateActive, "", ""
	case !started:
		return nil
	}
	if started {
		rec.LastStarted, rec.CrashLoop = now.Format(time.RFC3339), false
	}
	return writePartitionRecordAt(dir, rec)
}

// notePartitionStart records a start of user partition part of deployment
// dep of tile (PartitionEnv: once per spawn), best effort; none while the
// tile is paused (nothing of it starts then, and a switch's wipe may be
// deleting the record).
func (b *Broker) notePartitionStart(tile, dep, part, pkey, uid string) {
	id, ok := util.Partition(part).User()
	if !ok || b.partitionPaused(tile, dep) {
		return
	}
	t := partTarget{tile: tile, dep: dep, pkey: pkey, part: util.Partition(part), user: id, uid: uid}
	if err := b.notePartition(t, true); err != nil {
		slog.Warn("partitions: the partition's record", "tile", tile, "partition", part, "err", err)
	}
	b.mailPartitionStarted(tile, dep, pkey) // mail that waited for it rings (partitionmail_bell.go)
}

// NotePartitionExit records an exit of user partition part (id pkey) of
// deployment dep of tile that the runner's crash watch saw — nothing asked
// for it — in its record: the time, one more restart, and whether the
// breaker now holds the instance (crashLoop). The runner's PartitionExit
// hook, called outside its state's lock. A partition without a record (its
// data deleted meanwhile) gets none: an exit never makes one.
func (b *Broker) NotePartitionExit(tile, dep, part, pkey string, crashLoop bool) {
	if _, ok := util.Partition(part).User(); !ok {
		return
	}
	dir, err := b.partitionRecordDir(tile, dep, pkey)
	if err != nil {
		return
	}
	mu := b.partRecLock()
	mu.Lock()
	defer mu.Unlock()
	rec, ok, err := readPartitionRecordAt(dir)
	if err != nil || !ok || rec.Tile != tile {
		return
	}
	rec.LastExit, rec.CrashLoop = time.Now().UTC().Format(time.RFC3339), crashLoop
	rec.Restarts++
	if err := writePartitionRecordAt(dir, rec); err != nil {
		slog.Warn("partitions: the partition's record", "tile", tile, "partition", part, "err", err)
	}
}

// ---- the walk ----

// partitionDirOf is one user partition's directory as the walk finds it.
type partitionDirOf struct {
	dir, tileKey, dep, pkey string
}

// eachPartitionDir calls fn for every user partition's directory under
// data/partitions — of one tile (tile != "") or of all — whatever it holds.
// Directories that aren't a TileKey's (another plane's: binds, consents)
// and entries that aren't a deployment's or a pkey's are skipped.
func (b *Broker) eachPartitionDir(tile string, fn func(d partitionDirOf)) error {
	base := filepath.Join(b.Reg.Root, "data", partitionsDir)
	var keys []string
	if tile != "" {
		keys = []string{util.TileKey(tile)}
	} else {
		ents, err := os.ReadDir(base) // walk-ok: data/partitions is xbind's own
		if errNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		for _, e := range ents {
			if e.IsDir() && tileKeyOK(e.Name()) {
				keys = append(keys, e.Name())
			}
		}
	}
	var errs []error
	for _, key := range keys {
		deps, err := os.ReadDir(filepath.Join(base, key)) // walk-ok: as above
		if errNotExist(err) {
			continue
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, d := range deps {
			if !d.IsDir() || !util.DeploymentNameOK(d.Name()) {
				continue
			}
			parts, err := os.ReadDir(filepath.Join(base, key, d.Name())) // walk-ok: as above
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, p := range parts {
				if p.IsDir() && util.PartitionKeyOK(p.Name()) {
					fn(partitionDirOf{dir: filepath.Join(base, key, d.Name(), p.Name()), tileKey: key, dep: d.Name(), pkey: p.Name()})
				}
			}
		}
	}
	return errors.Join(errs...)
}

// tileKeyOK reports a name util.TileKey produces: 32 lower hex digits.
func tileKeyOK(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// eachPartitionRecord calls fn for every readable record (of tile, or all)
// whose tile hashes to its directory.
func (b *Broker) eachPartitionRecord(tile string, fn func(d partitionDirOf, rec partitionRecord)) error {
	return b.eachPartitionDir(tile, func(d partitionDirOf) {
		rec, ok, err := readPartitionRecordAt(d.dir)
		switch {
		case err != nil:
			slog.Warn("partitions: a partition record this xbind can't read; kept", "dir", d.dir, "err", err)
		case !ok:
		case util.TileKey(rec.Tile) != d.tileKey || rec.Dep != d.dep || rec.UID == "" || util.PartitionKey(rec.User, rec.UID) != d.pkey:
			slog.Warn("partitions: a partition record names another partition than its directory's; ignored", "dir", d.dir)
		default:
			fn(d, rec)
		}
	})
}

// ---- rebuild, adoption, orphans (PD-26, PD-43) ----

// rebuildPartitionRecords writes the partition.json a namespace's ns.json
// names when it is missing (03 §E): the tile is the scope's root, which
// owns the namespace. None while the tile is paused: a switch's wipe may be
// between the records and the namespaces.
func (b *Broker) rebuildPartitionRecords() {
	_ = b.eachPartitionNamespace("", func(id nsID) {
		m, ok, err := b.readNS(id)
		if err != nil || !ok || m.Partition == nil || m.Partition.User == "" || m.Partition.UID == "" ||
			util.PartitionKey(m.Partition.User, m.Partition.UID) != id.pkey {
			return
		}
		pi := m.Partition
		dir, err := b.partitionRecordDir(cmp.Or(pi.Tile, id.scope), id.dep, id.pkey)
		if err != nil {
			return
		}
		mu := b.partRecLock()
		mu.Lock()
		defer mu.Unlock()
		if _, found, err := readPartitionRecordAt(dir); found || err != nil {
			return
		}
		if b.partitionPaused(cmp.Or(pi.Tile, id.scope), id.dep) {
			return // a switch may be deleting it (the records' wipe holds this lock): the next sweep
		}
		rec := partitionRecord{Schema: partRecordSchema, Tile: cmp.Or(pi.Tile, id.scope), Dep: id.dep, User: pi.User, UID: pi.UID,
			Created: pi.Created, State: partStateActive, Rebuilt: true}
		if pi.Orphan != "" {
			rec.State, rec.Reason, rec.Orphaned = partStateOrphaned, pi.Orphan, m.Orphaned
		}
		if err := writePartitionRecordAt(dir, rec); err != nil {
			slog.Warn("partitions: rebuilding a partition record from its namespace", "dir", dir, "err", err)
		}
	})
}

// eachRecordIdentity calls note with whose every readable partition.json
// is (user, uid), when it was made and whether it is orphaned: the records'
// half of adoptablePartitionUID's rule (PD-43), beside the namespaces'
// ns.json.
func (b *Broker) eachRecordIdentity(note func(user, uid, created string, orphan bool)) {
	_ = b.eachPartitionRecord("", func(_ partitionDirOf, rec partitionRecord) {
		note(rec.User, rec.UID, rec.Created, rec.State == partStateOrphaned)
	})
}

// readoptPartitionUID gives person userID back the uid their partitions'
// records agree on (adoptablePartitionUID) when an older xbind's rewrite of
// the users store dropped it (PD-43) — at boot's load of the registrations,
// so their jobs and subscriptions fire without waiting for their next
// request on the tile. Nothing when the records name no uid, or two: a new
// uid is minted only at their next partition, as ever.
func (b *Broker) readoptPartitionUID(userID string) {
	if b.Users == nil || partitionAdoptUID == nil {
		return
	}
	u, ok := b.Users.Get(userID)
	if !ok || u.UID != "" {
		return
	}
	uid, ok := partitionAdoptUID(b, u.ID, time.Unix(u.Created, 0))
	if !ok {
		return
	}
	if _, err := b.Users.EnsureUID(u.ID, uid); err != nil {
		slog.Warn("partitions: re-adopting a person's uid from their partitions' records", "user", userID, "err", err)
		return
	}
	slog.Info("partitions: a person's uid re-adopted from their partitions' records", "user", userID)
}

// orphanPartitionRecords marks person userID's partitions (of uid, when
// given) orphaned: their person is deleted (the users store's delete hook,
// through PartitionUserDeleted).
func (b *Broker) orphanPartitionRecords(userID, uid string) {
	stamp := nowStamp(time.Now())
	_ = b.eachPartitionRecord("", func(d partitionDirOf, rec partitionRecord) {
		if rec.User == userID && (uid == "" || rec.UID == uid) && rec.State != partStateOrphaned {
			b.markPartitionRecord(d, orphanUserDeleted, stamp)
		}
	})
}

// markPartitionRecord records event on d's record ("" reclaims it).
func (b *Broker) markPartitionRecord(d partitionDirOf, event, stamp string) {
	mu := b.partRecLock()
	mu.Lock()
	defer mu.Unlock()
	rec, ok, err := readPartitionRecordAt(d.dir)
	if err != nil || !ok {
		return
	}
	rec.State, rec.Reason, rec.Orphaned = partStateOrphaned, event, stamp
	if event == "" {
		rec.State, rec.Orphaned = partStateActive, ""
	}
	if err := writePartitionRecordAt(d.dir, rec); err != nil {
		slog.Warn("partitions: a partition record", "dir", d.dir, "err", err)
		return
	}
	if event != "" {
		slog.Warn("partition orphaned", "tile", rec.Tile, "partition", d.pkey, "event", event,
			"deletes", nowStamp(time.Now().Add(partitionRetention)))
	}
}

// sweepPartitionRecords reconciles the partitions' records, as the
// namespaces' sweep does theirs (PD-26): a missing record is rebuilt from
// ns.json; an orphaning event is recorded (the tile gone, the person's id
// held by someone new); a tile that is back reclaims its partitions; an
// orphan past partitionRetention is deleted — its registrations, record and
// vault — never while its tile is paused.
func (b *Broker) sweepPartitionRecords(now time.Time) {
	b.rebuildPartitionRecords()
	stamp := nowStamp(now)
	err := b.eachPartitionRecord("", func(d partitionDirOf, rec partitionRecord) {
		ev := b.partitionOrphanEvent(partNS(rec.Tile, rec.Dep, d.pkey), nsMeta{Partition: &nsPartition{
			User: rec.User, UID: rec.UID, Tile: rec.Tile, Created: rec.Created}})
		switch {
		case rec.State != partStateOrphaned && ev != "":
			b.markPartitionRecord(d, ev, stamp)
			return
		case rec.State == partStateOrphaned && rec.Reason == orphanTileRemoved && ev != orphanTileRemoved:
			b.markPartitionRecord(d, "", stamp)
			return
		case rec.State != partStateOrphaned:
			return
		}
		if c, ok := b.Reg.Component(rec.Tile); ok {
			if st, _, _ := c.PartitionState(); st.Held() {
				return // pending or invalid: kept until a manager decides
			}
		}
		since, err := time.Parse(time.RFC3339, rec.Orphaned)
		if err != nil || now.Sub(since) < partitionRetention {
			return
		}
		// the whole partition: its terminals, log and other planes' stores too, its subkey erased (partitionops.go)
		if _, err := b.dropOnePartition(rec.Tile, rec.Dep, d.pkey, "partition swept: "+rec.Reason, ""); err != nil {
			slog.Warn("partition sweep", "tile", rec.Tile, "partition", d.pkey, "err", err)
			return
		}
		slog.Info("partition deleted: orphaned past the retention", "tile", rec.Tile, "partition", d.pkey, "event", rec.Reason)
	})
	if err != nil {
		slog.Warn("partition record sweep", "err", err)
	}
}

// dropPartition deletes one user partition's registrations (the rows the
// broker holds included), record and vault: an orphan's sweep, or a
// reset/purge of one partition (F7b). Its namespaces are
// dropPartitionNS's.
func (b *Broker) dropPartition(t partTarget) error {
	dir, err := t.dir(b)
	if err != nil {
		return err
	}
	vf, err := b.partVaultPath(t.tile, t.dep, t.pkey)
	if err != nil {
		return err
	}
	defer b.lockPartFiles()() // no rewrite that read the files lands after
	b.dropPartRows(t.tile, t.dep, t.pkey)
	vmu := b.partVaultLock()
	vmu.Lock()
	defer vmu.Unlock()
	var errs []error
	if err := os.Remove(vf); err != nil && !errNotExist(err) {
		errs = append(errs, err)
	}
	removeEmptyDirs(filepath.Dir(vf), filepath.Dir(filepath.Dir(vf)))
	mu := b.partRecLock()
	mu.Lock()
	defer mu.Unlock()
	errs = append(errs, os.RemoveAll(dir))
	removeEmptyDirs(filepath.Dir(dir))                                    // the deployment's level; the TileKey's keeps mode.json
	if _, _, err := b.dropMailBucket(t.tile, t.dep, t.pkey); err != nil { // its inbox (partitionmail.go)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// removeEmptyDirs removes each directory of dirs, in order, while it is
// empty (os.Remove refuses one that isn't): the levels a removal left
// behind, so "holds data" never counts an empty directory.
func removeEmptyDirs(dirs ...string) {
	for _, d := range dirs {
		if os.Remove(d) != nil {
			return
		}
	}
}

// DropPartition deletes user partition pkey of deployment dep of tile's
// registrations, record and vault — the partitions API's reset and purge of
// one partition (F7b), beside dropPartitionNS for its namespaces.
func (b *Broker) DropPartition(tile, dep, pkey string) error {
	return b.dropPartition(partTarget{tile: tile, dep: cmp.Or(dep, util.MainDeployment), pkey: pkey})
}

// ---- holds data and the switch's wipe (01 §2.2, §2.6) ----

func init() {
	registerPartitionStore(partitionStore{"partition-vault", holdsPartitionVault})
	registerPartitionStore(partitionStore{"partition-registrations", holdsPartitionRegistrations})
	registerPartitionStore(partitionStore{"partition-records", holdsPartitionRecords})
	registerWipeHook(wipeHook{name: "partition-vault", wipe: wipePartitionVaults})
	registerWipeHook(wipeHook{name: "partition-registrations", wipe: wipePartitionRegistrations})
	// "partition-records" is registered in partitionwire.go, as the last
	// metadata hook: the records name whose every other store's leftovers are
}

// holdsPartitionRecords: a person's partition of the tile has a directory —
// it started, registered or kept a vault key. One that can't be listed
// holds data.
func holdsPartitionRecords(b *Broker, ask registry.PartitionAsk) (bool, error) {
	held := false
	err := b.eachPartitionDir(ask.Tile, func(d partitionDirOf) { held = true })
	return held || err != nil, err
}

// holdsPartitionRegistrations: a person's partition of the tile keeps a
// registration file (each is removed when it empties).
func holdsPartitionRegistrations(b *Broker, ask registry.PartitionAsk) (bool, error) {
	held := false
	var errs []error
	err := b.eachPartitionDir(ask.Tile, func(d partitionDirOf) {
		for _, f := range partRegistrationFiles {
			if _, err := os.Lstat(filepath.Join(d.dir, f)); err == nil {
				held = true
			} else if !errNotExist(err) {
				errs = append(errs, err)
			}
		}
	})
	err = errors.Join(append(errs, err)...)
	return held || err != nil, err
}

// wipePartitionRecords removes every person's partition directory of the
// tile — records and whatever else a plane kept there — on a switch that
// deletes everything (H1: "global" coming or going keeps them). The
// directories of a deployment are removed when they empty; the tile's mode
// record stays (the switch writes it next).
func wipePartitionRecords(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind != wipeEverything {
		return nil
	}
	if !t.DryRun {
		defer b.lockPartFiles()() // no rewrite that read the files lands after
		mu := b.partRecLock()
		mu.Lock()
		defer mu.Unlock()
	}
	var dirs []partitionDirOf
	err := b.eachPartitionDir(t.Tile, func(d partitionDirOf) { dirs = append(dirs, d) })
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range dirs {
		if rec, ok, _ := readPartitionRecordAt(d.dir); ok { // under partRecLock: readPartitionRecordAt takes none
			sum.addPerson(rec.User)
		}
		if t.DryRun {
			continue
		}
		b.dropPartRows(t.Tile, d.dep, d.pkey)
		if err := os.RemoveAll(d.dir); err != nil {
			errs = append(errs, err)
			continue
		}
		_ = os.Remove(filepath.Dir(d.dir)) // the deployment's level, once empty
	}
	return errors.Join(errs...)
}
