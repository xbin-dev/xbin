package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
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
	if got, want := partitionKeyFor("alice", uid), "u-"+hex.EncodeToString(sum[:16]); got != want || !pkeyOK(got) {
		t.Fatalf("pkey %q, want %q", got, want)
	}
	pkeys := []string{partitionKeyFor("alice", uid), partitionKeyFor("alice", "other"), partitionKeyFor("alice."+uid, "")}
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

	n, err := b.wipePartitionNamespaces("apps/docs")
	if err != nil || n != 2 {
		t.Fatalf("wipe: %d %v", n, err)
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
	if n, err := b.wipePartitionNamespaces("apps/plain"); n != 0 || err != nil {
		t.Errorf("a tile without partitions: %d %v", n, err)
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
		id := partNS("apps/docs", util.MainDeployment, partitionKeyFor(user, uid))
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
	bare := partNS("apps/docs", util.MainDeployment, partitionKeyFor("erin", "uid-erin"))
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
	if uid, ok := b.adoptablePartitionUID("alice", now); !ok || uid != "uid-alice" {
		t.Errorf("alice adopts %q %v", uid, ok)
	}
	if uid, ok := b.adoptablePartitionUID("carol", now.Add(-time.Hour)); ok {
		t.Errorf("carol adopts %q from a previous holder's record", uid)
	}

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
	k, _ := b.resKeysIn(resTarget{Scope: "apps/docs", Name: "docs"}, util.MainDeployment, partitionKeyFor("alice", "uid-alice"))
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
