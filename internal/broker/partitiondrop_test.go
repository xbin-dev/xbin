package broker

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// subkeyLive reports whether subject has a live key in tile's store.
func subkeyLive(t *testing.T, b *Broker, tile, subject string) bool {
	t.Helper()
	keys, err := b.backupKeys().list()
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(keys, func(k backupSubkey) bool { return k.Tile == tile && k.Subject == subject })
}

// covers 11§3 01§3 — a reset of a partitioned tile inside another tile's
// scope deletes the member's own partition only: the scope root's
// namespace of the person and its part: key stay (the member uses no scope
// resources); only the member's key is erased.
func TestPartitionDropScopeMember(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	if err := b.barrier.Init("ops-pass"); err != nil {
		t.Fatal(err)
	}
	f.write(map[string]string{
		"apps/r/xbin.json":           `{"runtime":"go","partition":["user"]}`,
		"apps/r/scope.json":          `{"resources":{"db":{"type":"kv"}}}`,
		"apps/r/backend/main.go":     "package main\n",
		"apps/r/sub/xbin.json":       `{"runtime":"go","partition":["user"]}`,
		"apps/r/sub/backend/main.go": "package main\n",
	})
	f.rescan()
	for _, tile := range []string{"apps/r", "apps/r/sub"} {
		if st, _, _ := f.state(tile); st != registry.PartitionPartitioned {
			t.Fatalf("%s: %v, want partitioned", tile, st)
		}
	}
	if c, _ := b.Reg.Component("apps/r/sub"); c.Scope != "apps/r" {
		t.Fatalf("apps/r/sub's scope is %q", c.Scope)
	}
	pkey := f.hold("alice", "apps/r")
	f.hold("alice", "apps/r/sub")
	rootNS := partNS("apps/r", util.MainDeployment, pkey)
	if err := b.notePartitionNS(rootNS, nsPartition{User: "alice", UID: b.storedPartitionUID("alice")}); err != nil {
		t.Fatal(err)
	}
	rootSubject := partitionBackupSubject("apps/r", util.MainDeployment, pkey)
	memberSubject := partitionBackupSubject("apps/r/sub", util.MainDeployment, pkey)
	for tile, s := range map[string]string{"apps/r": rootSubject, "apps/r/sub": memberSubject} {
		if _, _, err := b.backupKeys().subkeyFor(s, tile); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := b.dropOnePartition("apps/r/sub", util.MainDeployment, pkey, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Namespaces != 0 || sum.Subkeys != 1 {
		t.Errorf("the member's reset deleted %+v, want no namespace and one key", sum)
	}
	if _, ok, _ := b.readNS(rootNS); !ok {
		t.Error("the member's reset deleted the scope root's namespace of alice")
	}
	if !subkeyLive(t, b, "apps/r", rootSubject) {
		t.Error("the member's reset erased the scope root's part: key")
	}
	if subkeyLive(t, b, "apps/r/sub", memberSubject) {
		t.Error("the member's own part: key survived its reset")
	}
	if rec, _ := b.partitionRecordDir("apps/r", util.MainDeployment, pkey); !opsExists(rec) {
		t.Error("the member's reset deleted alice's partition of the root")
	}
}

// covers 11§3 PD-17 — a partition kept by an older primary goes whole too:
// its record, vault, log and part: key of every deployment; the
// terminals' hold spans the drop, and the sessions' wipe happens inside
// it.
func TestPartitionDropEveryDeployment(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	if err := b.barrier.Init("ops-pass"); err != nil {
		t.Fatal(err)
	}
	terms := &fakeTerms{state: func() string { return "" }}
	b.SetPartitionTerminals(terms)
	pkey := f.pkeyOf("alice")
	uid := b.storedPartitionUID("alice")
	// a record, a vault, a log and a key under an older primary, "old"
	old := "old"
	oldRec, _ := b.partitionRecordDir("apps/pg", old, pkey)
	if err := os.MkdirAll(oldRec, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePartitionRecordAt(oldRec, partitionRecord{Tile: "apps/pg", Dep: old, User: "alice", UID: uid, State: "active"}); err != nil {
		t.Fatal(err)
	}
	vf, _ := b.partVaultPath("apps/pg", old, pkey)
	logDir, _ := b.partitionLogDir("apps/pg", old, pkey)
	for _, p := range []string{filepath.Dir(vf), logDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(vf, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	subjects := map[string]string{}
	for _, d := range []string{util.MainDeployment, old} {
		subjects[d] = partitionBackupSubject("apps/pg", d, pkey)
		if _, _, err := b.backupKeys().subkeyFor(subjects[d], "apps/pg"); err != nil {
			t.Fatal(err)
		}
	}
	var heldDuring, probed bool
	registerPartitionDropHook(partitionDropHook{name: "test-probe", drop: func(b *Broker, tile, dep, pk string) error {
		if tile != "apps/pg" || pk != pkey || probed {
			return nil
		}
		probed = true
		// the sessions' wipe released its own hold; the drop's is still held
		holds, releases, wiped := 0, 0, false
		for _, c := range terms.take() {
			switch c {
			case "hold apps/pg " + pkey:
				holds++
			case "release apps/pg " + pkey:
				releases++
			case "wipe apps/pg " + pkey:
				wiped = true
			}
		}
		heldDuring = wiped && holds == releases+1
		return nil
	}})
	t.Cleanup(func() {
		partitionDropHooks = slices.DeleteFunc(partitionDropHooks, func(h partitionDropHook) bool { return h.name == "test-probe" })
	})
	sum, err := b.dropOnePartition("apps/pg", util.MainDeployment, pkey, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if !heldDuring {
		t.Error("the partition's sessions weren't held across the drop")
	}
	if rest := terms.take(); !slices.Contains(rest, "release apps/pg "+pkey) {
		t.Errorf("the drop's hold wasn't released: %q", rest)
	}
	if sum.Subkeys != 2 {
		t.Errorf("erased %d keys, want main's and old's", sum.Subkeys)
	}
	for _, gone := range []string{oldRec, vf, logDir} {
		if opsExists(gone) {
			t.Errorf("%s survived the drop", gone)
		}
	}
	for d, s := range subjects {
		if subkeyLive(t, b, "apps/pg", s) {
			t.Errorf("the %s deployment's part: key survived", d)
		}
	}
}

// covers 06§6 — stop and reset name a partition the person holds of the
// tile: none is 404 (and no one is told); a non-reader learns nothing of
// the tile; someone else's partition is refused before its existence is
// said.
func TestPartitionOpsTargets(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	alice, bob, carol := personP(t, f.partWS, "alice"), personP(t, f.partWS, "bob"), personP(t, f.partWS, "carol")
	// wendy writes apps/pg but holds no partition of it
	mustCode(t, call(t, b.apiPartitionStop, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:wendy"}`, nil), 404, "stopping a partition no one holds")
	mustCode(t, call(t, b.apiPartitionReset, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:wendy","confirm":"apps/pg user:wendy"}`, nil), 404, "resetting a partition no one holds")
	if len(b.noticesOf("wendy", "")) != 0 || slices.Contains(f.pushes, "wendy tile.partition-reset") {
		t.Error("wendy was told of a reset of a partition she doesn't hold")
	}
	mustCode(t, call(t, b.apiPartitionStop, alice, "POST", "/", `{"tile":"apps/pg","partition":"global"}`, nil), 400, "the global instance")
	mustCode(t, call(t, b.apiPartitionStop, alice, "POST", "/", `{"tile":"apps/pg","partition":"user:wendy"}`, nil), 403, "someone else's, whether or not it exists")
	mustCode(t, call(t, b.apiPartitionStop, alice, "POST", "/", `{"tile":"apps/nope","partition":"user:alice"}`, nil), 404, "no such tile")
	mustCode(t, call(t, b.apiPartitionStop, alice, "POST", "/", `{"tile":"users/alice/mcp","partition":"user:alice"}`, nil), 409, "a readable unpartitioned tile")
	// carol can't read apps/pg: it answers as a missing tile, whoever's partition she names
	mustCode(t, call(t, b.apiPartitionStop, carol, "POST", "/", `{"tile":"apps/pg","partition":"user:alice"}`, nil), 404, "a non-reader names someone's")
	mustCode(t, call(t, b.apiPartitionStop, carol, "POST", "/", `{"tile":"apps/pg","partition":"user:carol"}`, nil), 404, "a non-reader names her own, which she doesn't hold")
}

// covers PD-20 06§9 — the people hooks through the users API itself (not
// the hooks called directly): PATCH /users/{id} disabling a person stops
// their running instance before it answers; DELETE /users/{id} stops and
// revokes theirs and orphans their partitions.
func TestPartitionPeopleHooksThroughAPI(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	bob := personP(t, f.partWS, "bob")
	f.running = []PartitionInstance{{Tile: "apps/pg", Dep: "main", Partition: "user:alice"}, {Tile: "apps/pg", Dep: "main", Partition: "user:zed"}}
	update := func(w http.ResponseWriter, r *http.Request) { b.apiUsersUpdate(nil, w, r) }
	del := func(w http.ResponseWriter, r *http.Request) { b.apiUsersDelete(nil, w, r) }

	mustCode(t, call(t, update, bob, "PATCH", "/", `{"disabled":true}`, map[string]string{"id": "alice"}), 200, "disable alice")
	if !slices.Contains(f.stops, "apps/pg main user:alice") || slices.Contains(f.stops, "apps/pg main user:zed") {
		t.Errorf("disabling alice stopped %q", f.stops)
	}

	zpkey := f.pkeyOf("zed")
	mustCode(t, call(t, del, bob, "DELETE", "/", "", map[string]string{"id": "zed"}), 200, "delete zed")
	if !slices.Contains(f.stopOf, "zed") || !slices.Contains(f.revoked, "zed") {
		t.Errorf("deleting zed: stops %q, revoked %q", f.stopOf, f.revoked)
	}
	if !slices.ContainsFunc(b.partitionOrphans("apps/pg"), func(o orphanRow) bool { return o.Partition == zpkey }) {
		t.Errorf("zed's partition isn't orphaned: %+v", b.partitionOrphans("apps/pg"))
	}
}
