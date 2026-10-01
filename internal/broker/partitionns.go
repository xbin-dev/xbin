package broker

// partitionns.go — user partitions' data namespaces on disk
// (plans/partitions/03 §B.1, §B.5, §B.8; PD-26, PD-43, PD-48): who a
// namespace belongs to (its ns.json identity), the walk over them, "holds
// data" and the switch's wipe, the orphan rules and their sweep, the uid a
// person's records carry for adoption, and the idle unmount of their volumes
// and kv files. Their keys are deploydata.go's (nsKeysFor, resKeysIn); which
// namespace a request reaches is partitionreach.go's.
//
// A partition namespace is orphaned only on a recorded event — its person
// deleted (the users-store hook, or a record older than the id's current
// holder) or its tile removed — and swept after partitionRetention; never
// for a mode (pending, declined, invalid), a missing record or an unknown
// user record. A workspace no tile partitions has no ".partitions" level and
// this file reads nothing of it.

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- seams: the identity plane (F2) fills these ----

// errPartitionUnwired: a principal's partition, or a person's uid, can't be
// told — the identity plane isn't installed. Everything that needs one
// refuses (fail closed).
var errPartitionUnwired = errors.New("people's partitions aren't available in this xbind yet")

// addressedPartitionSeam is the partition p acts in on tile, as the identity
// plane's addressedPartition (plans/partitions/02 §3) answers it: "" when
// tile isn't partitioned, "global", or "user:<id>"; an error when p reaches
// none (403). The default answers "" for a tile that isn't partitioned, so
// every existing answer stays today's, and refuses everyone else.
var addressedPartitionSeam = func(b *Broker, p auth.Principal, tile string) (string, error) {
	if c, ok := b.Reg.Component(tile); !ok || !partitionedNow(c) {
		return "", nil
	}
	return "", errPartitionUnwired
}

// partitionUIDSeam is person userID's uid as the users store keeps it
// (users.User.UID, PD-43): "" while the record has none, or the store can't
// say. It never mints one.
var partitionUIDSeam = func(b *Broker, userID string) string { return "" }

// partitionMintUIDSeam is person userID's uid, minted — or adopted from the
// records their live partitions carry (adoptablePartitionUID, PD-43) — at
// their first partition.
var partitionMintUIDSeam = func(b *Broker, userID string) (string, error) { return "", errPartitionUnwired }

// partitionedNow reports whether c's recorded mode has user partitions and
// runs (not pending or invalid).
func partitionedNow(c *registry.Component) bool {
	_, ok := c.Partitioned()
	return ok
}

// partitionKeyOf is person userID's partition key (util.PartitionKey, the
// identity plane's one encoder, 02 §1) and uid, minting the uid on their
// first partition (partitionMintUIDSeam).
func (b *Broker) partitionKeyOf(userID string) (pkey, uid string, err error) {
	uid, err = partitionMintUIDSeam(b, userID)
	if err == nil && uid == "" {
		err = errPartitionUnwired
	}
	if err != nil {
		return "", "", err
	}
	return util.PartitionKey(userID, uid), uid, nil
}

// ---- a namespace's identity (ns.json) ----

// nsPartition is whose a user partition's namespace is, in its ns.json
// (plans/partitions/03 §B.1, §E): enough to rebuild the partition's record
// and to judge it after its person is deleted or their id recreated.
type nsPartition struct {
	User    string `json:"user"`
	UID     string `json:"uid"`
	Tile    string `json:"tile"`
	Created string `json:"created"`          // RFC 3339, nanoseconds
	Orphan  string `json:"orphan,omitempty"` // the recorded event: user-deleted | tile-removed (since nsMeta.Orphaned)
}

// The orphaning events (PD-26).
const (
	orphanUserDeleted = "user-deleted"
	orphanTileRemoved = "tile-removed"
)

// partitionRetention is how long an orphaned partition namespace is kept
// before the sweep deletes it (PD-26).
var partitionRetention = 30 * 24 * time.Hour

// partNS names user partition pkey's namespace of scope in deployment dep.
func partNS(scope, dep, pkey string) nsID {
	id := nsOf(scope, dep)
	id.pkey = pkey
	return id
}

// notePartitionNS writes id's identity into its ns.json unless it has one:
// at the first write into the namespace or its instance's first start, so
// the namespace names its person before it holds anything. Its tile is
// always the scope's root, which owns the namespace (whoever wrote first: a
// nested tile's start included). An act holding id leaves it for later (the
// request is refused meanwhile).
func (b *Broker) notePartitionNS(id nsID, who nsPartition) error {
	who.Tile = id.scope
	dir, err := b.nsDir(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dir, nsMetaFile)); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err // walk-ok: data/ is xbind's own
	}
	release, err := b.holdNS(id, nsSweeping)
	if err != nil {
		return nil
	}
	defer release()
	return b.updateNS(id, true, func(m *nsMeta) {
		if m.Partition == nil {
			w := who
			w.Created = time.Now().UTC().Format(time.RFC3339Nano)
			m.Partition = &w
		}
	})
}

// ---- the walk ----

// partitionsDir is data/resources-enc/.partitions.
func (b *Broker) partitionsDir() string {
	return filepath.Join(b.Reg.Root, "data", "resources-enc", partitionsLevel)
}

// eachPartitionNamespace calls fn for each user partition's namespace on
// disk, of every scope ("" for scope) or of one.
func (b *Broker) eachPartitionNamespace(scope string, fn func(id nsID)) error {
	base := b.partitionsDir()
	var scopes []string
	if scope != "" {
		scopes = []string{escS(scope)}
	} else {
		ents, err := os.ReadDir(base) // walk-ok: data/ is xbind's own
		if absent(err) {
			return nil
		} else if err != nil {
			return err
		}
		for _, e := range ents {
			scopes = append(scopes, e.Name())
		}
	}
	var errs []error
	for _, es := range scopes {
		sc, ok := unescS(es)
		deps, err := os.ReadDir(filepath.Join(base, es)) // walk-ok: data/ is xbind's own
		if !ok || absent(err) {
			continue
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, d := range deps {
			parts, err := os.ReadDir(filepath.Join(base, es, d.Name())) // walk-ok: data/ is xbind's own
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, p := range parts {
				if s, dep, pkey, ok := partitionNS(path.Join(partitionsLevel, es, d.Name(), p.Name())); ok && s == sc && p.IsDir() {
					fn(partNS(s, dep, pkey))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// holdsPartitionNamespaces: a user partition's namespace of the scope the
// tile roots holds data (plans/partitions/01 §2.2) — any entry under
// .partitions/<escS>, a namespace's identity included: a person's
// partition exists once it started or was written, and the rule leans
// toward asking. A directory that can't be read holds data.
func holdsPartitionNamespaces(b *Broker, ask registry.PartitionAsk) (bool, error) {
	if !ask.RootsScope || len(escS(ask.Scope)) > maxEscS {
		return false, nil
	}
	ents, err := os.ReadDir(filepath.Join(b.partitionsDir(), escS(ask.Scope))) // walk-ok: data/ is xbind's own
	switch {
	case absent(err):
		return false, nil
	case err != nil:
		return true, err
	}
	return len(ents) > 0, nil
}

func init() {
	registerPartitionStore(partitionStore{"partition-namespaces", holdsPartitionNamespaces})
}

// ---- the switch's wipe ----

// partitionWipe is what wipePartitionNamespaces deleted — or, on a dry run,
// would delete — for the switch's summary (F13a's wipeSummary).
type partitionWipe struct {
	Namespaces int64    // people's partitions' namespaces
	Partitions int64    // people's partitions (partition keys) among them
	Bytes      int64    // stored bytes: ciphertext, kv files, records
	People     []string // whose they are (their ns.json), each once, sorted
}

// wipePartitionNamespaces deletes every user partition's namespace of the
// scope tile roots, whole — volumes unmounted and verified first, kv files
// closed — for a confirmed switch that deletes everything (plans/partitions/
// 01 §2.6; F13a's wipeEverything, never wipeGlobal or wipeNone, which keep
// people's partitions): the wipe executor calls it once the tile's
// instances are stopped and revoked, under the tile's backup lock. dryRun
// only counts, for the confirmation. It answers what it deleted; one an act
// holds, or that is still mounted, is an error, and the executor keeps the
// switch from completing. A tile that doesn't root its scope has none.
func (b *Broker) wipePartitionNamespaces(tile string, dryRun bool) (partitionWipe, error) {
	var sum partitionWipe
	c, ok := b.Reg.Component(tile)
	scope := tile
	if ok && c.Scope != tile || tile == "" || len(escS(scope)) > maxEscS {
		return sum, nil
	}
	var ids []nsID
	if err := b.eachPartitionNamespace(scope, func(id nsID) { ids = append(ids, id) }); err != nil {
		return sum, err
	}
	people, pkeys := map[string]bool{}, map[string]bool{}
	var errs []error
	for _, id := range ids {
		size := int64(0)
		if k, err := id.keys(); err == nil {
			size, _ = treeUsage(filepath.Join(b.Reg.Root, filepath.FromSlash(k.Enc)))
		}
		who := ""
		if m, ok, _ := b.readNS(id); ok && m.Partition != nil {
			who = m.Partition.User
		}
		if !dryRun {
			if err := b.dropPartitionNS(id); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		sum.Namespaces++
		sum.Bytes += size
		pkeys[id.pkey] = true
		if who != "" {
			people[who] = true
		}
	}
	sum.Partitions = int64(len(pkeys))
	sum.People = slices.Sorted(maps.Keys(people))
	if len(errs) == 0 && !dryRun { // what no namespace's walk reached: never through a mount
		mounts := filepath.Join(b.Reg.Root, ".xbin", "resenc", partitionsLevel, escS(scope))
		if pts, err := nsMountPoints(mounts); err != nil || len(pts) > 0 {
			errs = append(errs, cmpErr(err, fmt.Errorf("%s is still mounted: stop what uses it and try again", strings.Join(pts, ", "))))
		} else {
			errs = append(errs, os.RemoveAll(filepath.Join(b.partitionsDir(), escS(scope))), os.RemoveAll(mounts))
		}
	}
	return sum, errors.Join(errs...)
}

// dropPartitionNS deletes namespace id whole under its hold.
func (b *Broker) dropPartitionNS(id nsID) error {
	release, err := b.holdNS(id, nsRemoving)
	if err != nil {
		return err
	}
	defer release()
	b.forgetPartitionKV(id)
	return b.wipeNS(id, true)
}

// ---- orphans (PD-26) ----

// PartitionUserDeleted records that person userID, whose uid was uid, is
// deleted (the users store's delete hook): each of their partitions'
// namespaces is orphaned (user-deleted) and swept after the retention. A
// namespace whose record names another uid is someone else's and kept.
func (b *Broker) PartitionUserDeleted(userID, uid string) {
	stamp := nowStamp(time.Now())
	_ = b.eachPartitionNamespace("", func(id nsID) {
		m, ok, err := b.readNS(id)
		if err != nil || !ok || m.Partition == nil || m.Partition.User != userID || uid != "" && m.Partition.UID != uid {
			return
		}
		b.orphanPartitionNS(id, orphanUserDeleted, stamp)
	})
	b.orphanPartitionRecords(userID, uid) // their partitions' records, registrations and vaults (partitionrecords.go)
	b.mailUserDeleted(userID, uid)        // their inboxes, at once: mail is transient (partitionmail.go)
}

// orphanPartitionNS records event on id's ns.json, once.
func (b *Broker) orphanPartitionNS(id nsID, event, stamp string) {
	release, err := b.holdNS(id, nsSweeping)
	if err != nil {
		return // an act holds it: the next sweep judges it again
	}
	defer release()
	err = b.updateNS(id, false, func(m *nsMeta) {
		if m.Partition == nil || m.Partition.Orphan != "" {
			return
		}
		m.Partition.Orphan, m.Orphaned = event, stamp
		m.History = append(m.History, nsEvent{Op: "orphaned", At: stamp, Error: event})
	})
	if err == nil {
		slog.Warn("partition data orphaned", "scope", id.scope, "deployment", id.dep, "partition", id.pkey, "event", event,
			"deletes", nowStamp(time.Now().Add(partitionRetention)))
	}
}

// partitionOrphanEvent is the event that orphans namespace id, whose
// ns.json is m, now: its tile no longer registered (tile-removed), or its
// person's id held by someone else (user-deleted). The uid decides when
// both the store and the record carry one: the same uid is the same
// incarnation whatever the clocks say, another is someone else's. Only
// without one does the time decide: a record created in a second before
// the id's current holder was. A record made in the holder's own second
// can't be told either way: kept, and logged for bx doctor. "" keeps it: a
// missing record, an unknown person (bx doctor reports it), or anything
// that can't be told.
func (b *Broker) partitionOrphanEvent(id nsID, m nsMeta) string {
	pi := m.Partition
	if pi == nil {
		return ""
	}
	if _, ok := b.Reg.Component(id.scope); !ok {
		return orphanTileRemoved
	}
	if b.Users == nil {
		return ""
	}
	u, ok := b.Users.Get(pi.User)
	if !ok {
		return ""
	}
	if uid := partitionUIDSeam(b, pi.User); uid != "" && pi.UID != "" {
		if uid == pi.UID {
			return ""
		}
		return orphanUserDeleted
	}
	created, err := time.Parse(time.RFC3339Nano, pi.Created)
	if err != nil || u.Created <= 0 {
		return ""
	}
	switch holder := time.Unix(u.Created, 0); {
	case created.Before(holder):
		return orphanUserDeleted
	case created.Before(holder.Add(time.Second)):
		slog.Warn("partition data: made in the second its person's id was (re)created, so whose it is can't be told; kept",
			"scope", id.scope, "partition", id.pkey, "user", pi.User)
	}
	return ""
}

// sweepPartitionNamespaces reconciles user partitions' namespaces (PD-26),
// with SweepNamespaces: a crashed act becomes partial, an orphaning event is
// recorded, a tile that reappears reclaims its namespaces, and one orphaned
// past partitionRetention is deleted and its backup subkey (part:) erased —
// never while its tile is paused (pending or invalid: a manager decides).
func (b *Broker) sweepPartitionNamespaces(now time.Time) {
	err := b.eachPartitionNamespace("", func(id nsID) {
		if err := b.sweepPartitionOne(id, now); err != nil {
			slog.Warn("partition namespace sweep", "scope", id.scope, "deployment", id.dep, "partition", id.pkey, "err", err)
		}
	})
	if err != nil {
		slog.Warn("partition namespace sweep", "err", err)
	}
	b.sweepPartitionRecords(now) // their records, registrations and vaults (partitionrecords.go)
	b.sweepRemovedModeRecords()  // a removed tile with nothing left loses its mode record (partitiontrust.go)
}

func (b *Broker) sweepPartitionOne(id nsID, now time.Time) error {
	m, ok, err := b.readNS(id)
	if err != nil || !ok {
		return err // a missing record never orphans (§B.8)
	}
	stamp := nowStamp(now)
	if m.Busy != "" && b.busyAct(id) == "" {
		act := m.Busy
		release, err := b.holdNS(id, nsSweeping)
		if err != nil {
			return nil
		}
		defer release()
		slog.Warn("partition data left partial", "scope", id.scope, "partition", id.pkey, "act", act)
		return b.updateNS(id, false, func(m *nsMeta) {
			m.State, m.Busy, m.Failed, m.Step = nsPartial, "", act, act
			m.Error = "xbind stopped during the " + act
			m.History = append(m.History, nsEvent{Op: "partial", At: stamp, Error: m.Error})
		})
	}
	pi := m.Partition
	switch ev := b.partitionOrphanEvent(id, m); {
	case pi == nil:
		return nil
	case pi.Orphan == "" && ev != "":
		b.orphanPartitionNS(id, ev, stamp)
		return nil
	case pi.Orphan == orphanTileRemoved && ev != orphanTileRemoved:
		return b.reclaimPartitionNS(id, stamp) // the tile is back
	case pi.Orphan == "":
		return nil
	}
	if root, ok := b.Reg.Component(id.scope); ok {
		if st, _, _ := root.PartitionState(); st.Held() {
			return nil // pending or invalid: kept until a manager decides
		}
	}
	since, err := time.Parse(time.RFC3339, m.Orphaned)
	if err != nil || now.Sub(since) < partitionRetention {
		return nil
	}
	tile := id.scope // the scope's root owns the namespace and its archives, whatever the record says
	defer b.holdBackups(tile)()
	if err := b.dropPartitionNS(id); err != nil {
		return err
	}
	// its part: key, recorded in the tile's history (backup_partition.go)
	if _, _, err := b.erasePartitionBackupsHeld([]string{tile}, id.dep, id.pkey, "partition swept: "+pi.Orphan, ""); err != nil {
		slog.Warn("partition sweep: backup key erase", "tile", tile, "subject", partitionBackupSubject(tile, id.dep, id.pkey), "err", err)
	}
	slog.Info("partition data deleted: orphaned past the retention", "scope", id.scope, "partition", id.pkey, "event", pi.Orphan, "since", m.Orphaned)
	return nil
}

// reclaimPartitionNS clears a tile-removed orphan whose tile is back.
func (b *Broker) reclaimPartitionNS(id nsID, stamp string) error {
	release, err := b.holdNS(id, nsSweeping)
	if err != nil {
		return nil
	}
	defer release()
	return b.updateNS(id, false, func(m *nsMeta) {
		if m.Partition != nil {
			m.Partition.Orphan, m.Orphaned = "", ""
			m.History = append(m.History, nsEvent{Op: "claimed", At: stamp})
		}
	})
}

// cmpErr is err, or alt when err is nil.
func cmpErr(err, alt error) error {
	if err != nil {
		return err
	}
	return alt
}

// partitionBackupSubject is the backup subject of a user partition's
// archives (plans/partitions/11 §2): part:<TileKey>/<dep>/<pkey>, whose key
// the sweep erases.
func partitionBackupSubject(tile, dep, pkey string) string {
	return "part:" + util.TileKey(tile) + "/" + dep + "/" + pkey
}

// adoptablePartitionUID is the uid person userID's live partition records
// carry — the namespaces' ns.json and the partitions' partition.json
// (partitionrecords.go) alike — for a users-store record without one whose
// Created (Unix seconds) is created (PD-43); "" for none. Only records made
// after the second the record was created count — older ones were a
// previous holder's, and one made in that very second can't be told from
// theirs — and only when they agree. Its signature is F2's
// partitionAdoptUID seam's (partitionwire.go): the identity plane adopts it
// rather than mint a new uid.
func (b *Broker) adoptablePartitionUID(userID string, created int64) string {
	after := time.Unix(created, 0).Add(time.Second)
	uid, conflict := "", false
	note := func(user, recUID, at string, orphan bool) {
		t, err := time.Parse(time.RFC3339Nano, at)
		if user != userID || orphan || recUID == "" || err != nil || t.Before(after) {
			return
		}
		switch {
		case uid == "":
			uid = recUID
		case uid != recUID:
			conflict = true
		}
	}
	_ = b.eachPartitionNamespace("", func(id nsID) {
		if m, ok, err := b.readNS(id); err == nil && ok && m.Partition != nil {
			note(m.Partition.User, m.Partition.UID, m.Partition.Created, m.Partition.Orphan != "")
		}
	})
	b.eachRecordIdentity(note)
	if conflict {
		slog.Warn("partition data: records of one person carry different uids; none is adopted", "user", userID)
		return ""
	}
	return uid
}

// ---- idle volumes and kv files (PD-48) ----

// partitionIdle is how long a user partition's volume or kv file stays
// open with no user before it is unmounted or closed.
var partitionIdle = 60 * time.Minute

// partitionRunningSeam reports whether user partition pkey of scope's
// deployment dep has an instance (the runner fills it, F3): its volumes stay
// mounted, and the idle clock restarts at each scan that sees it. The
// runner must answer true from the moment a start asks PartitionEnv for its
// binds until that instance's sandbox has exited — starting, running and
// stopping alike: xbind's unmount of a view a sandbox still binds (in its
// own mount namespace) succeeds, and the next mount would then be a second
// gocryptfs on the same ciphertext. The default answers true — until the
// runner says, nothing is unmounted for idleness, as before.
var partitionRunningSeam = func(b *Broker, scope, dep, pkey string) bool { return true }

// partKV tracks a workspace's partition kv files in use: requests in
// flight and the last use, per namespace key.
type partKV struct {
	mu   sync.Mutex
	refs map[string]int
	last map[string]time.Time
}

var partKVs sync.Map // workspace root → *partKV

func (b *Broker) partKV() *partKV {
	v, _ := partKVs.LoadOrStore(b.Reg.Root, &partKV{refs: map[string]int{}, last: map[string]time.Time{}})
	return v.(*partKV)
}

// usePartitionKV marks namespace ns's kv file in use until release, so the
// idle close never closes it under a request.
func (b *Broker) usePartitionKV(ns string) (release func()) {
	t := b.partKV()
	t.mu.Lock()
	t.refs[ns]++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if t.refs[ns]--; t.refs[ns] <= 0 {
				delete(t.refs, ns)
			}
			t.last[ns] = time.Now()
			t.mu.Unlock()
		})
	}
}

// forgetPartitionKV forgets the use of id's kv file (a wipe, which closes it).
func (b *Broker) forgetPartitionKV(id nsID) {
	if k, err := id.keys(); err == nil {
		t := b.partKV()
		t.mu.Lock()
		delete(t.last, k.NS)
		t.mu.Unlock()
	}
}

// holdPartitionVolume holds k's volume, a user partition's, for one
// request (resenc.Hold); a no-op for any other volume.
func (b *Broker) holdPartitionVolume(k resKeys) (release func()) {
	if b.resenc == nil || !resenc.PartitionVolume(k.DirKey) {
		return func() {}
	}
	return b.resenc.Hold(k.DirKey, k.Name)
}

// reapIdlePartitions unmounts the user partitions' volumes nobody held for
// partitionIdle and whose instance isn't running, and closes their kv files
// idle as long (PD-48). The disk monitor's scan calls it.
func (b *Broker) reapIdlePartitions(now time.Time) {
	running := func(ns string) bool {
		scope, dep, pkey, ok := partitionNS(ns)
		return !ok || partitionRunningSeam(b, scope, dep, pkey)
	}
	if b.resenc != nil {
		b.resenc.UnmountIdle(now, partitionIdle, func(dirKey, _ string) bool {
			return running(strings.TrimSuffix(dirKey, "/fs"))
		})
	}
	if b.kv == nil {
		return
	}
	t := b.partKV()
	t.mu.Lock()
	defer t.mu.Unlock()
	for ns, last := range t.last {
		if running(ns) {
			t.last[ns] = now // its idle clock starts when its instance stops
			continue
		}
		if t.refs[ns] == 0 && now.Sub(last) >= partitionIdle {
			if err := b.kv.closeNamespace(ns); err != nil {
				slog.Warn("partition kv: idle close", "namespace", ns, "err", err)
				continue
			}
			delete(t.last, ns)
		}
	}
}

// IdlePartitionVolumes lets the volumes of person part's partition of
// deployment dep of tile go idle at once (resenc.Expire), as a backup's
// own mount does: the runner's admission turned a start of it away
// (PartitionTurnedAway) after the encryption check (holdReasonIn) mounted
// them, and they would otherwise stay mounted, a gocryptfs process and its
// logger each, for partitionIdle with nothing of the person's running —
// for every person a busy tile refuses (I2). The disk monitor's next pass
// unmounts them unless a request holds one or an instance of the person in
// the scope runs by then (partitionRunningSeam). Only the person's own
// volumes: the tile's shared ones and other scopes' stay as they are.
func (b *Broker) IdlePartitionVolumes(tile, dep, part string) {
	user, ok := strings.CutPrefix(part, "user:")
	if !ok || user == "" || b.resenc == nil {
		return
	}
	c, ok := b.Reg.Component(tile)
	if !ok || c.Scope == "" {
		return
	}
	pkey, _, err := b.partitionKeyOf(user)
	if err != nil {
		return
	}
	if dep == "" {
		dep = util.MainDeployment
	}
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.envTarget(c, dep, u.Target)
		if !ok || !fileBackedType(res.Type) || rt.Scope != c.Scope || res.Shared == registry.SharedAll || res.Shared == registry.SharedRead {
			continue
		}
		if k, err := b.resKeysIn(rt, dep, pkey); err == nil && resenc.PartitionVolume(k.DirKey) {
			b.resenc.Expire(k.DirKey, k.Name)
		}
	}
}

// partitionNSLabel names id for admins' alerts and rows: its tile and whose
// partition, never its content (PD-46).
func (b *Broker) partitionNSLabel(id nsID) string {
	who := id.pkey
	if m, ok, _ := b.readNS(id); ok && m.Partition != nil && m.Partition.User != "" {
		who = "user:" + m.Partition.User
	}
	return fmt.Sprintf("%s's %s partition", id.scope, who)
}
