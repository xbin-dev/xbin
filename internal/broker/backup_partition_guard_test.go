package broker

// The guards around people's partition archives (plans/partitions/
// 11-backup-encryption.md §3 §4; PD-46, PD-56): a person names only their
// own partition's id; a partition restore judges everything again under
// the tile's backup lock; the pre-switch confirmation outlives the mode
// history's trimming (and asks while the record can't be read); POST
// /deployments/restore names the only confirmed path; retention deletes
// the dead archives of a partition that is gone.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

// switchOff switches apps/pk from people's partitions to unpartitioned, as
// a manager does (deleting every partition and erasing their keys).
func (f *partBk) switchOff() {
	f.t.Helper()
	f.write(map[string]string{"apps/pk/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/pk/docs","role":"writer"}]}`})
	f.rescan()
	rec := call(f.t, f.b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode",
		`{"tile":"apps/pk","act":"switch","from":{"user":true,"global":true},"to":null,"confirm":"apps/pk"}`, nil)
	if rec.Code != 200 {
		f.t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}
}

// covers PD-46 11§4 — a person names only their own partition's id: one
// naming another person's (or an earlier holder's) is refused before the
// archiver is asked — listing and dry run alike — and the refusal never
// says whose it is (carol, who can't read the tile, is told it doesn't
// exist: TestPartitionArchiveRules); their own id, named, still lists.
func TestPartitionBackupsOthersID(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	f.backup()
	alicePK, bobPK := f.pkeyOf("alice"), f.pkeyOf("bob")
	alice := personP(t, f.partWS, "alice")
	asked := 0
	arch := f.arch
	b.ProxyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked++; arch.ServeHTTP(w, r) })
	for _, c := range []struct {
		p    auth.Principal
		pid  string
		name string
	}{{alice, bobPK, "bob"}, {alice, "u-" + strings.Repeat("0", 32), "u-0000"}} { // bob's; an id nobody holds now
		rec := call(t, b.apiPartitionBackups, c.p, "GET", "/partitions/backups?tile=apps/pk&partitionId="+c.pid, "", nil)
		if rec.Code != 403 || strings.Contains(rec.Body.String(), c.name) {
			t.Errorf("%s lists %s's: %d %s", c.p.UserID, c.name, rec.Code, rec.Body.String())
		}
		code, out := f.restore(c.p, `{"tile":"apps/pk","partitionId":"`+c.pid+`","dryRun":true}`)
		if code != 403 || strings.Contains(fmtErr(out), c.name) {
			t.Errorf("%s dry-runs %s's: %d %v", c.p.UserID, c.name, code, out)
		}
		code, out = f.restore(c.p, `{"tile":"apps/pk","partitionId":"`+c.pid+`","confirm":"apps/pk user:`+c.p.UserID+`"}`)
		if code != 403 || strings.Contains(fmtErr(out), c.name) {
			t.Errorf("%s restores %s's: %d %v", c.p.UserID, c.name, code, out)
		}
	}
	if asked != 0 {
		t.Errorf("the archiver was asked %d times for someone else's partition", asked)
	}
	rec := call(t, b.apiPartitionBackups, alice, "GET", "/partitions/backups?tile=apps/pk&partitionId="+alicePK, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"versions":[{`) || asked == 0 {
		t.Errorf("alice lists her own, named: %d %s", rec.Code, rec.Body.String())
	}
}

// covers PD-26 PD-44 11§3 11§4 — a partition restore judges everything
// again under the tile's backup lock: an erase of the partition's key, or
// a switch to unpartitioned, that lands between the handler's checks and
// the restore wins — the restore refuses and writes nothing (no record,
// no vault, no data comes back).
func TestPartitionRestoreRechecks(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	f.backup()
	alice := personP(t, f.partWS, "alice")
	alicePK := f.pkeyOf("alice")
	key := partitionArchiveKey(pbTile, "main", alicePK)
	judged := func() (partitionBackupTarget, []byte, string) {
		t.Helper()
		raw := f.arch.keys[key][0].body
		pt, ok := b.partitionBackupTargetOf(httptest.NewRecorder(), alice, pbTile, "", "", true)
		if !ok || !pt.partitioned {
			t.Fatalf("not judged: %+v", pt)
		}
		if _, _, err := b.openPartitionArchive(raw, pt); err != nil {
			t.Fatalf("the handler's checks: %v", err)
		}
		_, subkey, _ := b.openPartitionArchive(raw, pt)
		return pt, raw, subkey
	}
	refused := func(err error, want string) {
		t.Helper()
		var se statusErr
		if !errors.As(err, &se) || se.code != 409 || !strings.Contains(se.msg, want) || !strings.Contains(se.msg, "while this restore waited") {
			t.Errorf("the restore: %v, want 409 %q", err, want)
		}
	}

	// her key erased (a reset, bx backup erase) meanwhile
	f.kv(partInst(pbTile, "alice"), "docs", "k", "changed")
	pt, raw, subkey := judged()
	if _, _, err := b.ErasePartitionBackups(pbTile, "main", alicePK, "partition reset", "alice"); err != nil {
		t.Fatal(err)
	}
	_, err := b.restorePartition(pt, raw, subkey)
	refused(err, "was erased on")
	if got := f.get(partInst(pbTile, "alice"), "k"); got != "changed" {
		t.Errorf("alice's data after the refused restore: %q", got)
	}

	// the tile switched to unpartitioned meanwhile
	f.backup() // her partition sealed again, under a new key
	pt, raw, subkey = judged()
	f.switchOff()
	_, err = b.restorePartition(pt, raw, subkey)
	refused(err, "isn't partitioned now")
	vf, _ := b.partVaultPath(pbTile, "main", alicePK)
	if dir := f.root + "/data/partitions/" + util.TileKey(pbTile) + "/main/" + alicePK; exists(dir) || exists(vf) {
		t.Errorf("the switched tile has alice's partition again: record %v, vault %v", exists(dir), exists(vf))
	}
}

// covers PD-56 11§4 — the pre-switch confirmation outlives the mode
// history's trimming: a flood of a person's restores keeps at most
// modeHistoryBackup backup entries and never pushes the managers' switch
// out, and the guard reads lastWipe, which the history doesn't hold; a
// record that can't be read asks for every archive (its own date); POST
// /deployments/restore refuses a pre-switch archive and names POST
// /restore, the whole tile's.
func TestPreSwitchGuardOutlivesHistory(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	f.backup()
	f.switchOff()
	for i := 0; i < 2*modeHistoryMax; i++ {
		b.noteTileHistory(pbTile, modeHistory{Op: modeOpPartitionRestore, By: "alice", Reason: "a flood"})
	}
	backups, switches := 0, 0
	for _, h := range f.historyOps(pbTile) {
		if h.backupOp() {
			backups++
		}
		if h.Op == modeOpSwitch {
			switches++
		}
	}
	if backups != modeHistoryBackup || switches != 1 {
		t.Errorf("after the flood: %d backup entries, %d switches", backups, switches)
	}
	// a history without the switch (trimmed): lastWipe still guards
	pm := b.parts
	pm.mu.Lock()
	rec := pm.recs[pbTile].clone(pbTile)
	rec.History = nil
	pm.recs[pbTile] = rec
	pm.mu.Unlock()
	sw, ok, _ := b.lastDeletingSwitch(pbTile)
	if !ok || rec.LastWipe == nil || !sw.At.Equal(rec.LastWipe.At) {
		t.Fatalf("lastWipe: %+v %v", rec.LastWipe, ok)
	}
	if _, _, err := b.restoreTile(pbTile, "latest"); !errors.As(err, new(preSwitchError)) {
		t.Errorf("a pre-switch archive with the switch trimmed from the history: %v", err)
	}

	// POST /deployments/restore of a pre-switch archive: refused, naming
	// POST /restore
	var plain bytes.Buffer
	bw := backup.NewWriter(&plain)
	kv, _ := json.Marshal(map[string]map[string]string{"docs": {"old": b64("from-before-the-switch")}})
	_ = bw.Manifest(backup.Manifest{Component: pbTile, Scope: pbTile, ScopeRoot: true, Resources: map[string]string{"docs": "kv"},
		Created: sw.At.Add(-time.Hour).UTC().Format(time.RFC3339), Includes: []string{"source", "data"}})
	_ = bw.File(backup.KVName, 0o644, kv)
	_ = bw.Close()
	f.arch.set(backupKey(pbTile), plain.Bytes())
	_, _, err := b.RestoreDeploymentData(pbTile, "root", deployments.RestoreRequest{Tile: pbTile, Deployment: "main", Confirm: deployments.ConfirmEraseData},
		func(string) error { return nil }, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "POST /restore") || !strings.Contains(err.Error(), "whole tile") || strings.Contains(err.Error(), "bx restore apps/pk --confirm") {
		t.Errorf("a pre-switch archive through POST /deployments/restore: %v", err)
	}
	if got := f.get(instanceOf(pbTile, ""), "old"); got != "" {
		t.Errorf("the refused restore wrote: %q", got)
	}

	// a record this xbind can't read: every archive asks, with its own date
	f.backup()
	m, _ := f.arch.latest(t, backupKey(pbTile))
	pm.mu.Lock()
	pm.unread[util.TileKey(pbTile)] = "a test's"
	pm.mu.Unlock()
	var pre preSwitchError
	if _, _, err := b.restoreTile(pbTile, "latest"); !errors.As(err, &pre) || pre.date() != m.Created[:10] || !strings.Contains(err.Error(), "can't be read") {
		t.Errorf("a restore with the mode record unread: %v", err)
	}
	if err := b.preSwitchRestore(pbTile, m, "main", m.Created[:10]); err != nil {
		t.Errorf("confirmed with the backup's date: %v", err)
	}
	pm.mu.Lock()
	delete(pm.unread, util.TileKey(pbTile))
	pm.mu.Unlock()
}

// covers 11§3 — retention deletes the archives of a partition that is
// gone: at an archiver without erase collection a swept person's archive
// stays after the sweep erased its key; the next scheduled retention
// deletes every version of it (the tile's index names it) and keeps the
// others' newest, and the index forgets it.
func TestPartitionRetentionDropsGone(t *testing.T) {
	f := newPartBk(t)
	b := f.b
	f.fill()
	f.backup()
	alicePK, bobPK := f.pkeyOf("alice"), f.pkeyOf("bob")
	aliceKey, bobKey := partitionArchiveKey(pbTile, "main", alicePK), partitionArchiveKey(pbTile, "main", bobPK)
	provider := b.archiveProvider(pbTile)
	if x := b.loadPartitionIndex(provider, pbTile); len(x.Keys) != 2 {
		t.Fatalf("the index after a backup: %+v", x)
	}
	f.arch.noEraseGC = true
	b.PartitionUserDeleted("alice", f.uid("alice"))
	b.sweepPartitionNamespaces(time.Now().Add(partitionRetention + time.Hour))
	if len(f.arch.keys[aliceKey]) != 1 {
		t.Fatalf("an archiver without erase collection: alice's %d versions", len(f.arch.keys[aliceKey]))
	}
	f.backup()
	f.backup()
	b.prunePartitionArchives(pbTile, 1)
	if len(f.arch.keys[aliceKey]) != 0 || len(f.arch.keys[bobKey]) != 1 {
		t.Errorf("after retention: alice %d, bob %d versions", len(f.arch.keys[aliceKey]), len(f.arch.keys[bobKey]))
	}
	if x := b.loadPartitionIndex(provider, pbTile); !reflect.DeepEqual(x.Keys, []string{"main/" + bobPK}) {
		t.Errorf("the index after retention: %+v", x)
	}
}
