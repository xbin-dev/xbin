package broker

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// realPartitionIdentity puts the wired identity plane back in place of
// partFx's stand-in (stubPartitionIdentity) for the rest of the test: uids
// minted in the users store, pkeys util.PartitionKey's.
func realPartitionIdentity() {
	addressedPartitionSeam = (*Broker).addressedPartitionKey
	partitionMintUIDSeam = (*Broker).mintPartitionUID
	partitionUIDSeam = (*Broker).storedPartitionUID
}

// covers PD-43 PD-48 G2 — wave 1's seams are filled with the planes' own
// answers (partitionwire.go), not their fail-closed defaults: the data
// plane asks the identity plane's addressedPartition and uid functions,
// the bus stamp is events.Event.Partition, the uid adoption is the data
// plane's, the disk ceiling the partitions API's, and the idle unmount
// asks the runner once boot installed it (every partition counts as
// running before).
func TestPartitionWireSeams(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	if got, err := addressedPartitionSeam(b, frameOf("apps/pg", "alice"), "apps/pg"); got != "user:alice" || err != nil {
		t.Errorf("addressedPartitionSeam: %q, %v", got, err)
	}
	if _, err := addressedPartitionSeam(b, frameOf("apps/pg", "carol"), "apps/pg"); err == nil {
		t.Error("addressedPartitionSeam let carol, who can't read apps/pg, into it")
	}
	uid, err := partitionMintUIDSeam(b, "alice")
	if err != nil || uid == "" || partitionUIDSeam(b, "alice") != uid {
		t.Errorf("the uid seams: minted %q (%v), stored %q", uid, err, partitionUIDSeam(b, "alice"))
	}
	if pkey, _, err := b.partitionKeyOf("alice"); err != nil || pkey != util.PartitionKey("alice", uid) {
		t.Errorf("the data plane's pkey %q (%v), want util.PartitionKey's", pkey, err)
	}
	var ev events.Event
	stampBusPartition(&ev, "user:alice")
	if ev.Partition != "user:alice" || busEventPartition(ev) != "user:alice" {
		t.Errorf("the bus stamp: %+v", ev)
	}
	if partitionAdoptUID == nil {
		t.Error("partitionAdoptUID is unset")
	}
	if got := partitionDiskCeilingSeam(b, "apps/pg"); got != b.PartitionBytes("apps/pg") {
		t.Errorf("the disk ceiling seam %d, PartitionBytes %d", got, b.PartitionBytes("apps/pg"))
	}
	pk := util.PartitionKey("alice", uid)
	if !partitionRunningSeam(b, "apps/pg", "main", pk) {
		t.Error("without the runner's side, a partition must count as running (nothing unmounted)")
	}
	b.SetPartitionRunner(func(scope, pkey string) bool { return scope == "apps/pg" && pkey == pk }, nil, nil)
	if !partitionRunningSeam(b, "apps/pg", "main", pk) || partitionRunningSeam(b, "apps/pu", "main", pk) {
		t.Error("partitionRunningSeam doesn't ask the runner")
	}
}

// covers PD-44 PD-48 S13 01§2.5 — a switch of a partitioned tile that
// deletes everything, through the wired planes: every person's instance of
// the tile's scope is stopped and its tokens revoked (the runner's side)
// before anything is wiped, while the old mode is still the recorded one;
// people's partition namespaces go through the "partition-namespaces" wipe
// hook, counted in the dry run and the history, and their people are
// told. Removing or adding "global" keeps them (H1).
func TestPartitionWireSwitch(t *testing.T) {
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	withSeam(t, &partitionIsolated, func() bool { return true })
	for _, p := range []auth.Principal{aliceDocs, carolDocs} {
		if code, body := nsKV(t, b, "PUT", p, "res:apps/docs/docs/k", p.UserID+"'s"); code != 200 {
			t.Fatalf("%s's write: %d %s", p.UserID, code, body)
		}
	}
	namespaces := func() int {
		n := 0
		_ = b.eachPartitionNamespace("apps/docs", func(nsID) { n++ })
		return n
	}
	if namespaces() != 2 {
		t.Fatalf("people's namespaces of apps/docs: %d, want 2", namespaces())
	}
	var mu sync.Mutex
	var calls []string
	b.SetPartitionRunner(nil, func(tile string) {
		_, r, _ := w.state(tile)
		mu.Lock()
		calls = append(calls, "stop "+tile+" "+r.String())
		mu.Unlock()
	}, func(tile string) int {
		mu.Lock()
		calls = append(calls, "revoke "+tile)
		mu.Unlock()
		return 0
	})
	act := func(body string) (int, string) {
		t.Helper()
		rec := call(t, b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode", body, nil)
		return rec.Code, rec.Body.String()
	}

	// removing global keeps people's partitions (H1): nothing of theirs goes
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `["user","global"]`, `["user"]`, 1)})
	w.rescan()
	code, body := act(`{"tile":"apps/docs","act":"switch","from":{"user":true,"global":true},"to":{"user":true},"confirm":"apps/docs"}`)
	if code != 200 || namespaces() != 2 {
		t.Fatalf("removing global: %d %s; people's namespaces %d, want 2", code, body, namespaces())
	}

	// dropping the partition: the tile holds people's data, so it waits
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs is %s, want pending", st)
	}
	code, body = act(`{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"dryRun":true}`)
	if code != 200 || !strings.Contains(body, `"partitions":2`) || namespaces() != 2 {
		t.Fatalf("the dry run: %d %s; namespaces %d", code, body, namespaces())
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	code, body = act(`{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"confirm":"apps/docs"}`)
	if code != 200 {
		t.Fatalf("the switch: %d %s", code, body)
	}
	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	if len(got) < 2 || got[0] != "stop apps/docs user" || got[1] != "revoke apps/docs" {
		t.Errorf("the runner's side at the switch: %q, want apps/docs's people stopped and revoked, under the old mode", got)
	}
	if n := namespaces(); n != 0 {
		t.Errorf("%d people's namespaces of apps/docs survived the switch", n)
	}
	rec := w.record("apps/docs")
	if h := rec.History[len(rec.History)-1]; h.Op != modeOpSwitch || h.Wiped["partitions"] != 2 {
		t.Errorf("the switch's history: %+v", h)
	}
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionUnpartitioned {
		t.Errorf("after the switch apps/docs is %s", st)
	}
}

// covers PD-26 PD-52 — a new tile at a removed partitioned tile's path
// (F13b's open end): the old tile's mode record and its people's partition
// namespaces are leftovers, so creation there is refused (as for a
// deployment's) instead of a new owner inheriting the old mode and data; a
// registered tile's are never listed, nor anything for a path no
// partitioned tile had.
func TestPartitionLeftovers(t *testing.T) {
	w := partFx(t)
	b := w.b
	if code, body := nsKV(t, b, "PUT", aliceDocs, "res:apps/docs/docs/k", "alice's"); code != 200 {
		t.Fatalf("alice's write: %d %s", code, body)
	}
	if left := b.partitionLeftovers("apps/docs", func(p string) bool { return p == "apps/docs" }); len(left) != 0 {
		t.Errorf("a registered tile's leftovers: %q", left)
	}
	if err := os.RemoveAll(filepath.Join(w.root, "apps", "docs")); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	if _, ok := b.Reg.Component("apps/docs"); ok {
		t.Fatal("apps/docs is still registered")
	}
	left := b.pathLeftovers("apps/docs", "user:bob")
	for _, want := range []string{"partition mode record of apps/docs", "apps/docs's people's partition data (1 namespace(s))"} {
		if !slices.Contains(left, want) {
			t.Errorf("leftovers %q lack %q", left, want)
		}
	}
	if ok, msg := b.newTilePathOK("apps/docs", "user:bob"); ok || !strings.Contains(msg, "partition mode record of apps/docs") {
		t.Errorf("creating at the removed tile's path: %v %q", ok, msg)
	}
	if left := b.pathLeftovers("apps/fresh", "user:bob"); len(left) != 0 {
		t.Errorf("a fresh path's leftovers: %q", left)
	}
}

// covers PD-48 T18 C7 — the runner's binds of a person's partition through
// the real hooks, in the runner's order (plans/partitions/03 §B.4): the
// identity plane's PartitionIdent, the data plane's encryption hold (which
// mounts), its PartitionEnv, then the runner's dataBinds
// (partitionBindsFor, sharedResources): the partitioned filesystem and
// sqlite bind from the person's own volumes at the canonical paths, the
// shared sqlite from today's volume at its own path read-write, the "read"
// filesystem read-only. Real gocryptfs mounts: skipped without FUSE.
func TestPartitionBindsThroughHooks(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "gocryptfs"))
	if err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("XBIN_GOCRYPTFS"); v != "" {
		bin = v
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no gocryptfs (make build, or XBIN_GOCRYPTFS): skipping the FUSE test")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse absent: skipping the FUSE test")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("fusermount(3) absent: skipping the FUSE test")
		}
	}
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	b.resenc = resenc.New(b.Reg.Root, bin, func(label string) ([]byte, error) { return b.barrier.DeriveKey(label) })
	t.Cleanup(b.resenc.UnmountAll)
	if err := b.UnsealOrInit("binds-pass"); err != nil {
		t.Fatal(err)
	}
	if !b.resenc.Available() {
		t.Skip("gocryptfs isn't usable here")
	}
	c, _ := b.Reg.Component("apps/docs")
	run := &runner.Runner{Root: w.root, Reg: b.Reg}
	run.PartitionEnv = b.PartitionEnv

	pkey, uid, err := b.PartitionIdent("apps/docs", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if why := b.PartitionEncryptionHoldReason("apps/docs", util.MainDeployment, "user:alice"); why != "" || !b.ShouldRunPartition("apps/docs", util.MainDeployment, "user:alice", uid) {
		t.Fatalf("alice's partition may not start: %q", why)
	}
	binds, err := run.PartitionDataBinds(c, "user:alice", pkey)
	if err != nil {
		t.Fatalf("alice's binds: %v", err)
	}
	own := func(name string) string {
		k, err := b.resKeysIn(resTarget{Scope: "apps/docs", Name: name}, util.MainDeployment, pkey)
		if err != nil {
			t.Fatal(err)
		}
		return b.resMount(k, false)
	}
	canon := func(name string) string { return b.fsResPath("apps/docs", name, false) }
	want := map[string]struct {
		src string
		ro  bool
	}{
		canon("files"): {own("files"), false},
		canon("notes"): {own("notes"), false},
		canon("team"):  {canon("team"), false},
		canon("conf"):  {canon("conf"), true},
	}
	if len(binds) != len(want) {
		t.Errorf("alice's binds: %+v, want %d", binds, len(want))
	}
	for _, bd := range binds {
		wb, ok := want[bd.Dst]
		if !ok || bd.Src != wb.src || bd.RO != wb.ro {
			t.Errorf("bind %+v, want %+v at %s", bd, wb, bd.Dst)
		}
	}
	if !resenc.IsMountPoint(own("files")) || !resenc.IsMountPoint(canon("team")) {
		t.Error("alice's own volume and the shared one aren't mounted views")
	}
}
