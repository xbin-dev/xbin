package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-01 PD-43 — the partition level of the key function
// (plans/partitions/03 §B.1): "" keeps every key of today and of a
// deployment byte for byte; a partition's keys are injective across scope,
// deployment and partition key, sit under .partitions (never .deployments,
// never main's), spell main, give back their (scope, deployment, pkey), and
// are a directory key resenc takes. The pkey is 02 §1's encoding exactly,
// and anything else is refused.
func TestNsKeysPartition(t *testing.T) {
	b := deployBroker(t)
	uid := "0123456789abcdef0123456789abcdef"
	sum := sha256.Sum256([]byte("xbin-partition-v1\x00alice\x00" + uid))
	if got, want := util.PartitionKey("alice", uid), "u-"+hex.EncodeToString(sum[:16]); got != want || !pkeyOK(got) {
		t.Fatalf("pkey %q, want %q", got, want)
	}
	pkeys := []string{util.PartitionKey("alice", uid), util.PartitionKey("alice", "other"), util.PartitionKey("alice."+uid, "")}
	deps := []string{"", util.MainDeployment, "dev", "global"}
	seen := map[string]string{}
	for _, scope := range keyScopes {
		for _, dep := range deps {
			today, err := scopeKeys(scope, dep)
			if got, gerr := nsKeysFor(scope, dep, ""); got != today || (err == nil) != (gerr == nil) {
				t.Errorf("%s/%q: nsKeysFor(\"\") %+v %v, want scopeKeys's %+v %v", scope, dep, got, gerr, today, err)
			}
			for _, name := range []string{"db", "a/b"} {
				rt := resTarget{Scope: scope, Name: name}
				k0, err0 := b.resKeys(rt, dep)
				if k1, err1 := b.resKeysIn(rt, dep, ""); k1 != k0 || (err0 == nil) != (err1 == nil) {
					t.Errorf("%s in %q: resKeysIn(\"\") %+v, want resKeys's %+v", rt, dep, k1, k0)
				}
			}
			for _, pk := range pkeys {
				k, err := nsKeysFor(scope, dep, pk)
				if err != nil {
					t.Fatalf("%s/%q/%s: %v", scope, dep, pk, err)
				}
				who := scope + "|" + dep + "|" + pk
				if dep == "" {
					who = scope + "|main|" + pk // "" and main are one namespace
				}
				if prev, dup := seen[k.NS]; dup && prev != who {
					t.Errorf("%s and %s share %s", prev, who, k.NS)
				}
				seen[k.NS] = who
				if !strings.HasPrefix(k.NS, partitionsLevel+"/") || strings.Contains(k.NS, deploymentsLevel) ||
					k.DirKey != k.NS+"/fs" || k.Quota != k.NS || k.Enc != "data/resources-enc/"+k.NS || k.Plain != "" {
					t.Errorf("%s: keys %+v", who, k)
				}
				if !resenc.PartitionVolume(k.DirKey) {
					t.Errorf("%s: resenc refuses the directory key %s", who, k.DirKey)
				}
				s, d, p, ok := partitionNS(k.NS)
				if !ok || s != scope || d != cmpDep(dep) || p != pk {
					t.Errorf("%s: partitionNS gives %q %q %q %v", who, s, d, p, ok)
				}
				main, _ := scopeKeys(scope, util.MainDeployment)
				if dk, err := scopeKeys(scope, "dev"); err == nil && (strings.HasPrefix(k.NS, dk.NS+"/") || k.NS == dk.NS) || k.DirKey == main.DirKey {
					t.Errorf("%s: nests in another namespace's key", who)
				}
				rk, err := b.resKeysIn(resTarget{Scope: scope, Name: "db"}, dep, pk)
				if err != nil || rk.NS != k.NS || rk.KVFile != nsKVFile(k.NS) || rk.KVLabel != "kv:"+k.NS+"/res:"+scope+"/db" ||
					rk.Bucket != "res:"+scope+"/db" || rk.FSLabel != k.DirKey+"/db" {
					t.Errorf("%s: resource keys %+v %v", who, rk, err)
				}
			}
		}
	}
	for _, c := range []struct{ scope, dep, pkey string }{
		{"apps/x", "", "u-0123"},                             // short
		{"apps/x", "", "u-0123456789ABCDEF0123456789ABCDEF"}, // upper case
		{"apps/x", "", "d-0123456789abcdef0123456789abcdef"}, // another prefix
		{"apps/x", "", "u-0123456789abcdef0123456789abcde/"}, // a separator
		{"", "", pkeys[0]},                                   // the workspace scope is never partitioned
		{"apps/x", "Dev", pkeys[0]},                          // not a deployment name
		{"../x", "", pkeys[0]},                               // not a workspace path
		{strings.Repeat("a/", 120) + "x", "", pkeys[0]},      // too long
	} {
		if k, err := nsKeysFor(c.scope, c.dep, c.pkey); err == nil {
			t.Errorf("%q/%q/%q: keys %+v, want a refusal", c.scope, c.dep, c.pkey, k)
		}
	}
	if _, err := b.resKeysIn(resTarget{Scope: "apps/x", Name: "./db"}, "", pkeys[0]); err == nil {
		t.Error("a name that aliases got a partition key")
	}
	for _, ns := range []string{"", ".deployments/apps~x/dev", ".partitions/apps~x/dev", ".partitions/apps~x/Dev/" + pkeys[0],
		".partitions/apps~x/dev/" + pkeys[0] + "/fs", ".partitions/%zz/dev/" + pkeys[0]} {
		if _, _, _, ok := partitionNS(ns); ok {
			t.Errorf("partitionNS(%q) parsed", ns)
		}
	}
}

func cmpDep(dep string) string {
	if dep == "" {
		return util.MainDeployment
	}
	return dep
}

// covers PD-51 — "holds data" sees people's partitions: a partition
// namespace of the scope the tile roots (its record included) holds data,
// so a manifest change then waits for a manager; the wipe of a switch
// removes them all and then none is left.
func TestTileHoldsDataPartitions(t *testing.T) {
	w := partFx(t)
	b := w.b
	ask := registry.PartitionAsk{Tile: "apps/docs", Scope: "apps/docs", RootsScope: true}
	if held, store, _ := b.tileHoldsData(ask); held {
		t.Fatalf("an unused partitioned tile holds data in %s", store)
	}
	if code, body := nsKV(t, b, "PUT", aliceDocs, "res:apps/docs/docs/k", "v"); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if held, store, _ := b.tileHoldsData(ask); !held || store != "partition-namespaces" {
		t.Errorf("after alice's write: held %v in %q", held, store)
	}
	if code, body := nsKV(t, b, "PUT", carolDocs, "res:apps/docs/docs/k", "v"); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	// Not the agent's: it roots another scope.
	if held, _, _ := b.tileHoldsData(registry.PartitionAsk{Tile: "apps/agent", Scope: "apps/agent", RootsScope: true}); held {
		t.Error("apps/agent holds apps/docs's partitions")
	}
	// An unreadable level holds data.
	dir := filepath.Join(b.partitionsDir(), escS("apps/docs"))
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	if held, _, _ := b.tileHoldsData(ask); !held && os.Geteuid() != 0 {
		t.Error("an unreadable partitions level holds no data")
	}
	_ = os.Chmod(dir, 0o700)

	// A dry run (the switch's confirmation) counts, and deletes nothing.
	dry, err := b.wipePartitionNamespaces("apps/docs", true)
	if err != nil || dry.Namespaces != 2 || dry.Partitions != 2 || dry.Bytes <= 0 || strings.Join(dry.People, ",") != "alice,carol" {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if held, _, _ := b.tileHoldsData(ask); !held {
		t.Fatal("a dry run deleted people's partitions")
	}
	sum, err := b.wipePartitionNamespaces("apps/docs", false)
	if err != nil || sum.Namespaces != dry.Namespaces || sum.Bytes != dry.Bytes || strings.Join(sum.People, ",") != "alice,carol" {
		t.Fatalf("wipe: %+v %v, want the dry run's %+v", sum, err, dry)
	}
	if held, store, _ := b.tileHoldsData(ask); held {
		t.Errorf("after the wipe the tile holds data in %s", store)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the scope's partitions level survived the wipe: %v", err)
	}
	if code, _ := nsKV(t, b, "GET", aliceDocs, "res:apps/docs/docs/k", ""); code != 404 {
		t.Errorf("alice's key after the wipe: %d", code)
	}
	if n, err := b.wipePartitionNamespaces("apps/plain", false); n.Namespaces != 0 || err != nil {
		t.Errorf("a tile without partitions: %+v %v", n, err)
	}
}

// covers PD-26 PD-43 11§3 — TestPartitionOrphanRules (03 §B.8): only a
// recorded event orphans a partition's namespace — the delete hook, a
// record older than its id's current holder (or naming another uid), or
// its tile gone — and never a pause (pending), a missing record or an
// unknown person; a tile that comes back reclaims them; the sweep deletes
// an orphan only past the retention, never while its tile is paused, and
// erases the partition's part: backup subkey. A lost users-store uid is
// re-adopted from the records made since the user's Created, never those
// of a previous holder.
func TestPartitionOrphanRules(t *testing.T) {
	w := partFx(t)
	b := w.b
	if err := b.barrier.Init("partition-pass"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ns := func(user, uid string, created time.Time) nsID {
		t.Helper()
		id := partNS("apps/docs", util.MainDeployment, util.PartitionKey(user, uid))
		if err := b.notePartitionNS(id, nsPartition{User: user, UID: uid, Tile: "apps/docs"}); err != nil {
			t.Fatal(err)
		}
		if err := b.updateNS(id, false, func(m *nsMeta) { m.Partition.Created = created.UTC().Format(time.RFC3339Nano) }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	event := func(id nsID) string {
		t.Helper()
		m, ok, err := b.readNS(id)
		if err != nil || !ok {
			return "gone"
		}
		return m.Partition.Orphan
	}
	alice := ns("alice", "uid-alice", now.Add(time.Hour))   // alice, as the store knows her
	stale := ns("carol", "uid-old", now.Add(-24*time.Hour)) // carol's id's previous holder
	dave := ns("dave", "uid-dave", now)                     // no such person: kept
	bare := partNS("apps/docs", util.MainDeployment, util.PartitionKey("erin", "uid-erin"))
	if err := os.MkdirAll(filepath.Join(b.partitionsDir(), escS("apps/docs"), util.MainDeployment, bare.pkey), 0o700); err != nil {
		t.Fatal(err)
	}
	subject := partitionBackupSubject("apps/docs", util.MainDeployment, alice.pkey)
	if _, _, err := b.backupKeys().subkeyFor(subject, "apps/docs"); err != nil {
		t.Fatal(err)
	}

	b.SweepNamespaces()
	if e := event(alice) + "," + event(stale) + "," + event(dave); e != ",user-deleted," {
		t.Errorf("after a sweep: %s", e)
	}
	if _, ok, _ := b.readNS(bare); ok {
		t.Error("a namespace without a record got one")
	}

	// uid re-adoption: records made since alice's Created carry her uid;
	// carol's only record predates her and is orphaned: nothing to adopt.
	if uid := b.adoptablePartitionUID("alice", now.Unix()); uid != "uid-alice" {
		t.Errorf("alice adopts %q", uid)
	}
	if uid := b.adoptablePartitionUID("carol", now.Add(-time.Hour).Unix()); uid != "" {
		t.Errorf("carol adopts %q from a previous holder's record", uid)
	}
	var _ func(*Broker, string, int64) string = (*Broker).adoptablePartitionUID // F2's partitionAdoptUID seam

	// The delete hook orphans alice's; the tile paused (pending) keeps it
	// past the retention; unpaused, the sweep deletes it and erases its key.
	b.PartitionUserDeleted("alice", "uid-other") // another incarnation's delete: not hers
	if e := event(alice); e != "" {
		t.Fatalf("a delete naming another uid orphaned alice's: %s", e)
	}
	b.PartitionUserDeleted("alice", "uid-alice")
	if e := event(alice); e != orphanUserDeleted {
		t.Fatalf("the delete hook: %q", e)
	}
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs is %s, want pending", st)
	}
	later := now.Add(partitionRetention + time.Hour)
	b.sweepPartitionNamespaces(later)
	if e := event(alice); e != orphanUserDeleted {
		t.Errorf("a paused tile's orphan was swept: %q", e)
	}
	w.write(map[string]string{"apps/docs/xbin.json": partFxFiles["apps/docs/xbin.json"]})
	w.rescan()
	b.sweepPartitionNamespaces(now.Add(time.Hour)) // within the retention
	if e := event(alice); e != orphanUserDeleted {
		t.Errorf("swept within the retention: %q", e)
	}
	b.sweepPartitionNamespaces(later)
	if e := event(alice) + "," + event(stale) + "," + event(dave); e != "gone,gone," {
		t.Errorf("past the retention: %s", e)
	}
	keys, err := b.backupKeys().list()
	if err != nil || slices.ContainsFunc(keys, func(k backupSubkey) bool { return k.Subject == subject }) {
		t.Errorf("alice's part: key survived the sweep: %+v %v", keys, err)
	}

	// The tile gone: its partitions are orphaned (tile-removed), and
	// reclaimed when it comes back within the retention.
	fresh := ns("carol", "uid-carol", now.Add(time.Hour))
	if err := os.RemoveAll(filepath.Join(w.root, "apps", "docs")); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	b.sweepPartitionNamespaces(now)
	if e := event(fresh); e != orphanTileRemoved {
		t.Fatalf("a removed tile's partition: %q", e)
	}
	w.write(map[string]string{"apps/docs/xbin.json": partFxFiles["apps/docs/xbin.json"], "apps/docs/scope.json": partFxFiles["apps/docs/scope.json"]})
	w.rescan()
	b.sweepPartitionNamespaces(now)
	if e := event(fresh); e != "" {
		t.Errorf("the tile is back, the partition still orphaned: %q", e)
	}
	if _, err := os.Lstat(filepath.Join(b.partitionsDir(), escS("apps/docs"), util.MainDeployment, bare.pkey)); err != nil {
		t.Errorf("a namespace without a record was removed: %v", err)
	}
}

// covers PD-48 — a user partition's kv file closes after an hour without a
// request, unless its instance runs; a request in flight keeps it open.
func TestPartitionIdleKVClose(t *testing.T) {
	w := partFx(t)
	b := w.b
	if code, body := nsKV(t, b, "PUT", aliceDocs, "res:apps/docs/docs/k", "v"); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	k, _ := b.resKeysIn(resTarget{Scope: "apps/docs", Name: "docs"}, util.MainDeployment, util.PartitionKey("alice", "uid-alice"))
	open := func() bool {
		b.kv.mu.Lock()
		defer b.kv.mu.Unlock()
		return b.kv.ns[k.KVFile] != nil
	}
	if !open() {
		t.Fatal("alice's kv file isn't open after a write")
	}
	prev := partitionRunningSeam
	t.Cleanup(func() { partitionRunningSeam = prev })
	b.reapIdlePartitions(time.Now().Add(2 * partitionIdle)) // the default: the runner hasn't said → running
	if !open() {
		t.Fatal("closed while its instance may run")
	}
	partitionRunningSeam = func(*Broker, string, string, string) bool { return false }
	release := b.usePartitionKV(k.NS)
	b.reapIdlePartitions(time.Now().Add(2 * partitionIdle))
	if !open() {
		t.Fatal("closed under a request")
	}
	release()
	b.reapIdlePartitions(time.Now().Add(partitionIdle / 2))
	if !open() {
		t.Fatal("closed before an hour's idleness")
	}
	b.reapIdlePartitions(time.Now().Add(2 * partitionIdle))
	if open() {
		t.Fatal("still open after an hour's idleness")
	}
	if code, body := nsKV(t, b, "GET", aliceDocs, "res:apps/docs/docs/k", ""); code != 200 || body != "v" {
		t.Errorf("GET after the close: %d %s", code, body)
	}
}

// covers PD-48 I2 — a start the runner's admission turned away
// (PartitionTurnedAway) leaves nothing mounted for the idle hour: the
// encryption check mounted the person's own volumes before admission, and
// IdlePartitionVolumes makes just those due at the disk monitor's next pass
// — not another person's, not the tile's shared ones, not one a request
// holds, and none while an instance of the person runs.
func TestIdlePartitionVolumes(t *testing.T) {
	w := partFx(t)
	b := w.b
	nsFakeVolumes(t, b)
	for _, p := range []string{"user:alice", "user:carol"} {
		if why := b.PartitionEncryptionHoldReason("apps/docs", util.MainDeployment, p); why != "" {
			t.Fatalf("%s may not start: %q", p, why)
		}
	}
	mounted := func() []string {
		var out []string
		for _, mt := range b.resenc.Mounts() {
			out = append(out, mt.ScopeKey+"/"+mt.Name)
		}
		slices.Sort(out)
		return out
	}
	alicePK := util.PartitionKey("alice", "uid-alice")
	alice := func(name string) resKeys {
		t.Helper()
		k, err := b.resKeysIn(resTarget{Scope: "apps/docs", Name: name}, util.MainDeployment, alicePK)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	before := mounted()
	for _, n := range []string{"files", "notes", "box"} {
		if k := alice(n); !slices.Contains(before, k.DirKey+"/"+k.Name) {
			t.Fatalf("alice's %s isn't mounted after the encryption check: %q", n, before)
		}
	}
	prev := partitionRunningSeam
	t.Cleanup(func() { partitionRunningSeam = prev })
	running := true
	partitionRunningSeam = func(_ *Broker, _, _, pkey string) bool { return running && pkey == alicePK }

	b.IdlePartitionVolumes("apps/docs", "", "user:alice")
	b.reapIdlePartitions(time.Now())
	if got := mounted(); !slices.Equal(got, before) {
		t.Fatalf("unmounted while her instance runs: %q, was %q", got, before)
	}
	running = false
	b.reapIdlePartitions(time.Now())
	if got := mounted(); !slices.Equal(got, before) {
		t.Fatalf("unmounted without being turned away, before the idle hour: %q", got)
	}
	files := alice("files")
	release := b.resenc.Hold(files.DirKey, files.Name) // a request of hers
	b.IdlePartitionVolumes("apps/docs", "", "user:alice")
	b.reapIdlePartitions(time.Now())
	release()
	var want []string
	for _, m := range before {
		if k1, k2 := alice("notes"), alice("box"); m != k1.DirKey+"/"+k1.Name && m != k2.DirKey+"/"+k2.Name {
			want = append(want, m)
		}
	}
	if got := mounted(); !slices.Equal(got, want) || len(want) != len(before)-2 {
		t.Errorf("after a start of alice's was turned away: %q mounted, want %q (all but her unheld volumes)", got, want)
	}
	b.IdlePartitionVolumes("apps/nope", "", "user:alice") // no such tile, and no person: nothing
	b.IdlePartitionVolumes("apps/docs", "", "global")
}

// covers PD-26 PD-43 — the uid decides whose a namespace is when both the
// store and the record carry one, whatever the clocks say (a step back of
// the host clock never orphans a live person's data); only without one does
// the time decide, and a record made in the very second its person's id was
// (re)created can't be told: it is kept, and never adopted from.
func TestPartitionOrphanUID(t *testing.T) {
	w := partFx(t)
	b := w.b
	for _, id := range []string{"frank", "greg"} {
		if _, err := b.Users.Upsert(users.User{ID: id, Tiles: map[string]string{"apps/docs": "read"}}, "pw-"+id); err != nil {
			t.Fatal(err)
		}
	}
	prev := partitionUIDSeam
	t.Cleanup(func() { partitionUIDSeam = prev })
	partitionUIDSeam = func(b *Broker, userID string) string { // frank's and greg's records lost their uid
		if userID == "frank" || userID == "greg" {
			return ""
		}
		return "uid-" + userID
	}
	created := func(id string) time.Time {
		u, _ := b.Users.Get(id)
		return time.Unix(u.Created, 0)
	}
	ns := func(user, uid string, at time.Time) nsID {
		t.Helper()
		id := partNS("apps/docs", util.MainDeployment, util.PartitionKey(user, uid))
		if err := b.notePartitionNS(id, nsPartition{User: user, UID: uid}); err != nil {
			t.Fatal(err)
		}
		if err := b.updateNS(id, false, func(m *nsMeta) { m.Partition.Created = at.UTC().Format(time.RFC3339Nano) }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	event := func(id nsID) string {
		m, ok, _ := b.readNS(id)
		if !ok || m.Partition == nil {
			return "gone"
		}
		return m.Partition.Orphan
	}
	alice := ns("alice", "uid-alice", created("alice").Add(-time.Hour)) // the clock stepped back: the same uid keeps it
	frankOld := ns("frank", "uid-f0", created("frank").Add(-2*time.Second))
	gregSame := ns("greg", "uid-g0", created("greg").Add(500*time.Millisecond))
	b.sweepPartitionNamespaces(time.Now())
	if e := event(alice) + "," + event(frankOld) + "," + event(gregSame); e != ",user-deleted," {
		t.Errorf("after a sweep: %q, want alice's and greg's kept, frank's previous holder's orphaned", e)
	}
	if m, _, _ := b.readNS(alice); m.Partition.Tile != "apps/docs" {
		t.Errorf("the record's tile is %q, want the scope's root", m.Partition.Tile)
	}
	// greg's record made in his id's own second is never adopted; one made
	// after it is.
	if uid := b.adoptablePartitionUID("greg", created("greg").Unix()); uid != "" {
		t.Errorf("greg adopts %q from a record of his id's own second", uid)
	}
	ns("greg", "uid-g1", created("greg").Add(2*time.Second))
	if uid := b.adoptablePartitionUID("greg", created("greg").Unix()); uid != "uid-g1" {
		t.Errorf("greg adopts %q, want uid-g1", uid)
	}
	// Two records that disagree: nothing is adopted.
	ns("greg", "uid-g2", created("greg").Add(3*time.Second))
	if uid := b.adoptablePartitionUID("greg", created("greg").Unix()); uid != "" {
		t.Errorf("greg adopts %q from records that disagree", uid)
	}
}

// covers PD-26 03§B.8 — a mode never orphans people's data: a declined
// request (the manager kept R) and an invalid manifest leave every
// namespace unorphaned, and an invalid tile's are never swept, even past the
// retention.
func TestPartitionOrphanNeverOnMode(t *testing.T) {
	w := partFx(t)
	b := w.b
	for _, p := range []auth.Principal{aliceDocs, carolDocs} {
		if code, body := nsKV(t, b, "PUT", p, "res:apps/docs/docs/k", "v"); code != 200 {
			t.Fatalf("PUT: %d %s", code, body)
		}
	}
	var ids []nsID
	_ = b.eachPartitionNamespace("apps/docs", func(id nsID) { ids = append(ids, id) })
	if len(ids) != 2 {
		t.Fatalf("namespaces: %+v", ids)
	}
	kept := func(when string) {
		t.Helper()
		for _, id := range ids {
			m, ok, err := b.readNS(id)
			if err != nil || !ok || m.Partition == nil || m.Partition.Orphan != "" {
				t.Errorf("%s: %s's namespace: %+v %v %v", when, id.pkey, m.Partition, ok, err)
			}
		}
	}
	later := time.Now().Add(partitionRetention + time.Hour)

	// Declined: the code dropped the key, a manager kept R.
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if err := b.recordDecision("apps/docs", modeOpKeep, registry.PartitionSpec{User: true, Global: true}, registry.PartitionSpec{}, "root", nil); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	if st, _, req := w.state("apps/docs"); st != registry.PartitionPartitioned || req == nil || !req.Declined {
		t.Fatalf("apps/docs: %s %+v, want partitioned with a declined request", st, req)
	}
	b.sweepPartitionNamespaces(later)
	kept("declined")

	// Invalid: nothing runs, nothing is decided or deleted.
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"]`, `"partition":["user","nonsense"]`, 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionInvalid {
		t.Fatalf("apps/docs: %s, want invalid", st)
	}
	b.sweepPartitionNamespaces(later)
	kept("invalid")
}

// covers PD-46 03§B.7 — a person's partition is a disk bucket of its own:
// over its ceiling, that person's writes are 507 and no one else's; the
// alert names the tile and whose partition for admins only — never in
// /tile-status (TileAlerts), a non-admin's /alerts or the count every tile
// shows.
func TestPartitionDiskCeiling(t *testing.T) {
	w := partFx(t)
	b := w.b
	if _, err := b.Users.Upsert(users.User{ID: "ana", Role: users.RoleAdmin}, "pw-ana"); err != nil {
		t.Fatal(err)
	}
	free := 50 * gib
	d := quietDisk(t, b, 8*gib, &free)
	b.SetDeploymentQuota(quotaRecords(map[string]map[string]int64{}))
	for _, p := range []auth.Principal{aliceDocs, carolDocs} {
		if code, body := nsKV(t, b, "PUT", p, "res:apps/docs/docs/k", "v"); code != 200 {
			t.Fatalf("PUT: %d %s", code, body)
		}
	}
	alice, err := nsKeysFor("apps/docs", util.MainDeployment, util.PartitionKey("alice", "uid-alice"))
	if err != nil {
		t.Fatal(err)
	}
	sparse(t, filepath.Join(w.root, filepath.FromSlash(alice.Enc), "fs", "files", "c0"), 9*gib)
	d.scan()
	if code, body := nsKV(t, b, "PUT", aliceDocs, "res:apps/docs/docs/k2", "v"); code != http.StatusInsufficientStorage || !strings.Contains(body, "over its 8.0GB quota") {
		t.Errorf("alice's write over her partition's ceiling: %d %s", code, body)
	}
	for _, p := range []auth.Principal{carolDocs, docsGlobal} {
		if code, body := nsKV(t, b, "PUT", p, "res:apps/docs/docs/k2", "v"); code != 200 {
			t.Errorf("%s/%s's write beside a full partition: %d %s", p.Component, p.UserID, code, body)
		}
	}
	mine := func(a Alert) bool { return strings.Contains(a.Message, "user:alice partition") }
	a := findAlert(d.Alerts(), mine)
	switch {
	case a == nil:
		t.Fatalf("no alert names alice's partition: %+v", d.Alerts())
	case a.System || a.Tile != alice.Quota || a.Deployment != util.MainDeployment:
		t.Errorf("alice's alert: %+v", *a)
	}
	if a := findAlert(alertsFor(t, b, deployPerson(t, b, "ana")), mine); a == nil {
		t.Error("admins don't see alice's partition's alert")
	}
	if a := findAlert(alertsFor(t, b, deployPerson(t, b, "carol")), mine); a != nil {
		t.Errorf("a reader of apps/docs sees %+v", *a)
	}
	if a := findAlert(b.TileAlerts("apps/docs"), mine); a != nil {
		t.Errorf("/tile-status shows %+v", *a)
	}
	if a := findAlert(d.Alerts(), func(a Alert) bool { return a.Kind == "blocking" }); a != nil {
		t.Errorf("the count every tile shows counts a person's partition: %+v", *a)
	}
	// The ceiling a manager lowers applies to each partition.
	prev := partitionDiskCeilingSeam
	t.Cleanup(func() { partitionDiskCeilingSeam = prev })
	partitionDiskCeilingSeam = func(b *Broker, tile string) int64 { return 1 << 20 }
	carol, _ := nsKeysFor("apps/docs", util.MainDeployment, util.PartitionKey("carol", "uid-carol"))
	sparse(t, filepath.Join(w.root, filepath.FromSlash(carol.Enc), "fs", "files", "c0"), 2<<20)
	d.scan()
	if code, _ := nsKV(t, b, "PUT", carolDocs, "res:apps/docs/docs/k3", "v"); code != http.StatusInsufficientStorage {
		t.Errorf("carol over the lowered ceiling: %d", code)
	}
	if code, _ := nsKV(t, b, "PUT", docsGlobal, "res:apps/docs/docs/k3", "v"); code != 200 {
		t.Errorf("global under a partition's lowered ceiling: %d", code)
	}
}
