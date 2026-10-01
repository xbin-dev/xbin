package broker

// partitionw3a_test.go — wave 3a's seams (plans/partitions/records/
// W3a-wire.md): partition mail (F6) in the operations' listing and drops
// (F7b), and F7b's drops erasing through F17b's history-recording path;
// the offload refusal through offload itself.

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// mailCountOf is the mail counts of row user in a GET /partitions answer
// (nil: the row carries none; missing: no such row).
func mailCountOf(out map[string]any, user string) (mail map[string]any, found bool) {
	rows, _ := out["partitions"].([]any)
	for _, r := range rows {
		row := r.(map[string]any)
		if row["user"] == user {
			m, _ := row["mail"].(map[string]any)
			return m, true
		}
	}
	return nil, false
}

// covers 04§3 06§6 PD-46 — GET /partitions carries each inbox's counts
// (PartitionMailCounts): the person's own row, and every row for admins;
// never a content, never to tile code, never another person's to a person.
func TestPartitionsListMailCounts(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	global := instanceOf("apps/pg", "")
	mailSendOK(t, b, global, "user:alice", "secret-topic", "alice's secret")
	mailSendOK(t, b, global, "user:alice", "b", 1)
	mailSendOK(t, b, global, "user:zed", "z", 1)
	alice, bob, zed := personP(t, f.partWS, "alice"), personP(t, f.partWS, "bob"), personP(t, f.partWS, "zed")

	code, out := f.get(alice, "?tile=apps/pg")
	if code != 200 {
		t.Fatalf("alice: %d %v", code, out)
	}
	if m, ok := mailCountOf(out, "alice"); !ok || m["pending"] != float64(2) {
		t.Errorf("alice's own row's mail: %v (row %v)", m, ok)
	}
	if _, ok := mailCountOf(out, "zed"); ok {
		t.Error("alice sees zed's row")
	}
	code, out = f.get(zed, "?tile=apps/pg")
	if m, _ := mailCountOf(out, "zed"); code != 200 || m["pending"] != float64(1) {
		t.Errorf("zed's own row's mail: %d %v", code, m)
	}

	code, out = f.get(bob, "?tile=apps/pg") // the admin: every row's counts
	if code != 200 {
		t.Fatalf("bob: %d %v", code, out)
	}
	if m, _ := mailCountOf(out, "alice"); m["pending"] != float64(2) {
		t.Errorf("the admin's row of alice: mail %v", m)
	}
	if m, _ := mailCountOf(out, "zed"); m["pending"] != float64(1) {
		t.Errorf("the admin's row of zed: mail %v", m)
	}
	if m, ok := mailCountOf(out, "bob"); !ok || m != nil {
		t.Errorf("bob's empty inbox: row %v, mail %v", ok, m)
	}
	rec := call(t, b.apiPartitionsList, bob, "GET", "/partitions?tile=apps/pg", "", nil)
	if s := rec.Body.String(); strings.Contains(s, "secret-topic") || strings.Contains(s, "alice's secret") {
		t.Errorf("BUG: the listing carries mail content: %s", s)
	}

	code, out = f.get(frameOf("apps/pg", "alice"), "?tile=apps/pg") // tile code: tile-level fields only
	if _, has := out["partitions"]; code != 200 || has {
		t.Errorf("tile code: %d %v", code, out)
	}
	if !slices.Contains(featuresOf(out), PartitionMailFeature) {
		t.Errorf("features %q without %q", featuresOf(out), PartitionMailFeature)
	}
}

// featuresOf is a GET /partitions answer's features.
func featuresOf(out map[string]any) []string {
	var fs []string
	raw, _ := out["features"].([]any)
	for _, f := range raw {
		fs = append(fs, f.(string))
	}
	return fs
}

// historyEraseOf is tile's newest backup-erase entry for partition pkey.
func historyEraseOf(b *Broker, tile, pkey string) (modeHistory, bool) {
	rec := b.modeRecordOf(tile)
	if rec == nil {
		return modeHistory{}, false
	}
	for i := len(rec.History) - 1; i >= 0; i-- {
		if h := rec.History[i]; h.Op == modeOpBackupErase && h.Partition == pkey {
			return h, true
		}
	}
	return modeHistory{}, false
}

// covers 11§3 PD-26 06§6 04§3 — F7b's reset and purge (dropOnePartition)
// erase a partition's part: key through F17b's erasePartitionBackupsHeld,
// so each erase is in the tile's history (reason, partition id, by); the
// reset drops the person's inbox, and deleting a person through the users
// API drops theirs at once (F6's mailUserDeleted via F7b's hook).
func TestPartitionDropsEraseThroughHistory(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	if err := b.barrier.Init("w3a-pass"); err != nil {
		t.Fatal(err)
	}
	bob := personP(t, f.partWS, "bob")
	global := instanceOf("apps/pg", "")
	apk, zpk := f.pkeyOf("alice"), f.pkeyOf("zed")
	for _, pk := range []string{apk, zpk} {
		if _, _, err := b.backupKeys().subkeyFor(partitionBackupSubject("apps/pg", util.MainDeployment, pk), "apps/pg"); err != nil {
			t.Fatal(err)
		}
	}
	mailSendOK(t, b, global, "user:alice", "a", 1)
	mailSendOK(t, b, global, "user:zed", "z", 1)
	mailSendOK(t, b, global, "user:bob", "b", 1)
	counts := func() map[string]MailCount {
		t.Helper()
		c, err := b.PartitionMailCounts("apps/pg", "")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	// the admin's reset of alice's partition
	mustCode(t, call(t, b.apiPartitionReset, bob, "POST", "/", `{"tile":"apps/pg","partition":"user:alice","confirm":"apps/pg user:alice"}`, nil), 200, "the admin resets alice's")
	h, ok := historyEraseOf(b, "apps/pg", apk)
	if !ok || h.Wiped["subkeys"] != 1 || !strings.HasPrefix(h.Reason, "partition reset: ") || h.By == "" {
		t.Errorf("the reset's erase in the tile's history: %v %+v", ok, h)
	}
	if subkeyLive(t, b, "apps/pg", partitionBackupSubject("apps/pg", util.MainDeployment, apk)) {
		t.Error("alice's part: key survived her reset")
	}
	if c := counts(); c[apk].Pending != 0 || c[zpk].Pending != 1 {
		t.Errorf("mail after alice's reset: %+v", c)
	}

	// zed deleted through the users API: his inbox goes at once, his
	// partition is orphaned; the admin's purge erases his key, in the history
	del := func(w http.ResponseWriter, r *http.Request) { b.apiUsersDelete(nil, w, r) }
	mustCode(t, call(t, del, bob, "DELETE", "/", "", map[string]string{"id": "zed"}), 200, "delete zed")
	if c := counts(); c[zpk].Pending != 0 || c[f.pkeyOf("bob")].Pending != 1 {
		t.Errorf("mail after zed's deletion: %+v", c)
	}
	if _, has := counts()[zpk]; has {
		t.Error("zed's inbox survived his deletion")
	}
	out := mustCode(t, call(t, b.apiPartitionPurge, bob, "POST", "/", `{"tile":"apps/pg"}`, nil), 200, "the admin purges")
	if p, _ := out["purged"].([]any); len(p) != 1 {
		t.Errorf("purged %v", out["purged"])
	}
	h, ok = historyEraseOf(b, "apps/pg", zpk)
	if !ok || h.Wiped["subkeys"] != 1 || !strings.HasPrefix(h.Reason, "partition purged: ") {
		t.Errorf("the purge's erase in the tile's history: %v %+v", ok, h)
	}
	if subkeyLive(t, b, "apps/pg", partitionBackupSubject("apps/pg", util.MainDeployment, zpk)) {
		t.Error("zed's part: key survived the purge")
	}
}

// covers C16 06§10 — offload itself refuses a partitioned tile (the
// one-line check in backup.go's offload, F7b's refusal in F17b's file):
// 409, nothing archived.
func TestPartitionOffloadRefusedByOffload(t *testing.T) {
	f := newOpsFx(t)
	err := f.b.offload("apps/pg", false)
	var refused *errPartitionedOffload
	if !errors.As(err, &refused) || offloadStatus(err) != 409 {
		t.Fatalf("offloading apps/pg: %v", err)
	}
	if err := f.b.offload("apps/pg", true); !errors.As(err, &refused) {
		t.Errorf("a full offload of apps/pg: %v", err)
	}
}
