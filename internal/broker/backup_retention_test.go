package broker

// A sealed workspace's data archives against its main archives
// (plans/partitions/11-backup-encryption.md §4): retention keeps a data
// archive exactly as long as a kept main archive names it, a main archive
// whose data archive went missing still restores its source, a data
// archive is only ever restored with the main archive of its own backup.
// Built on backup_seal_test.go's sealFx.

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/util"
)

// dataOf is the data version main archive version v of the fixture's tile
// names (read through the restore path's own opener).
func dataOf(t *testing.T, f *bkFx, v string) string {
	t.Helper()
	d, err := f.b.mainDataRef("apps/archiver", bkTile, v)
	if err != nil {
		t.Fatalf("main archive %s: %v", v, err)
	}
	return d
}

// covers PD-56 11§4 — retention prunes data archives by reference, never by
// a count of their own: a data archive a failed main PUT left (an archiver
// may have stored the main archive all the same) stays until a run finds
// no kept main archive naming it, and the oldest kept main archive's data
// is never the one that goes. A main archive the cache doesn't know (made
// before it, or elsewhere) is read before any data archive goes; a
// plaintext workspace makes no data-archive call at all.
func TestSealedDataRetentionByReference(t *testing.T) {
	f := sealFx(t)
	b := f.b
	sched := backupSchedule{Component: bkTile, Schedule: "@daily", Retention: 2}
	mainKey, dataKey := backupKey(bkTile), dataArchiveKey(bkTile)

	b.runScheduledBackup(sched) // D1 M1
	m1 := f.arch.versions(mainKey)[0]
	d1 := dataOf(t, f, m1)
	f.arch.fail = mainKey
	b.runScheduledBackup(sched) // D2, no main archive
	f.arch.fail = ""
	orphan := f.arch.versions(dataKey)[0]
	if orphan == d1 || len(f.arch.versions(dataKey)) != 2 {
		t.Fatalf("a failed main PUT dropped its data archive: %v", f.arch.versions(dataKey))
	}
	b.runScheduledBackup(sched) // D3 M3: mains [M3 M1], datas [D3 D2 D1] → D2 goes
	m3 := f.arch.versions(mainKey)[0]
	if got, want := f.arch.versions(dataKey), []string{dataOf(t, f, m3), d1}; !slices.Equal(got, want) {
		t.Fatalf("data archives after the prune: %v, want %v", got, want)
	}
	// the oldest kept main archive still restores with its data
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "changed")
	if r, _, err := b.restoreTile(bkTile, m1); err != nil || r.DataMissing != "" || !r.Has("data") || f.kv(util.MainDeployment, "m") != "m="+sealMarker+"-main" {
		t.Fatalf("the oldest kept backup: %+v %v (%s)", r, err, f.kv(util.MainDeployment, "m"))
	}

	// the cache gone (another machine, say): the kept main archive it
	// doesn't know is read before its data archive could go
	if err := os.Remove(b.backupRefsPath(bkTile)); err != nil {
		t.Fatal(err)
	}
	b.runScheduledBackup(sched) // D4 M4: mains [M4 M3] (M1 pruned) → D1 goes, D3 stays
	m4 := f.arch.versions(mainKey)[0]
	if got, want := f.arch.versions(dataKey), []string{dataOf(t, f, m4), dataOf(t, f, m3)}; !slices.Equal(got, want) {
		t.Fatalf("data archives with the cache gone: %v, want %v", got, want)
	}
	var refs backupRefs
	raw, _ := os.ReadFile(b.backupRefsPath(bkTile))
	if json.Unmarshal(raw, &refs) != nil || len(refs.Mains) != 2 || refs.Mains[m3] == "" || refs.Mains[m4] == "" {
		t.Errorf("the cache after the run: %s", raw)
	}

	// a plaintext workspace lists no data archive
	p := plaintextVault(testBroker(t))
	arch := &memArchiver{keys: map[string][]memVersion{}}
	p.ProxyHandler = arch
	p.pruneData("apps/calendar")
	if len(arch.keys) != 0 {
		t.Errorf("a plaintext workspace's prune: %v", arch.keys)
	}
}

// covers PD-56 11§4 — an archiver that stores a main archive and still
// answers an error doesn't cost the backup its data: the data archive
// stays, and the stored main archive restores whole.
func TestSealedMainStoredDespiteAnError(t *testing.T) {
	f := sealFx(t)
	f.arch.storeThenFail = backupKey(bkTile)
	if _, err := f.b.doBackup(bkTile); err == nil {
		t.Fatal("the main PUT's error wasn't answered")
	}
	f.arch.storeThenFail = ""
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "changed")
	if r, _, err := f.b.restoreTile(bkTile, ""); err != nil || r.DataMissing != "" || f.kv(util.MainDeployment, "m") != "m="+sealMarker+"-main" {
		t.Fatalf("restore of the stored main archive: %+v %v", r, err)
	}
}

// covers PD-56 11§4 — a main archive whose data archive is gone, its key
// not erased, restores its source and terminal layer and says so
// (dataMissing), as does re-enabling an offloaded tile; a data file of it
// is 404. So does one naming a version no archiver URL can, and an
// archiver answering a data PUT with no usable version fails the backup
// before the main archive is written.
func TestSealedDataMissing(t *testing.T) {
	f := sealFx(t)
	b := f.b
	v, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	d := dataOf(t, f, v)
	if w := zeroDataCall(t, b.apiLifecycleSet, "POST", "", `{"component":"apps/cal","state":"offloaded"}`, auth.Principal{Owner: true}); w.Code != 200 {
		t.Fatalf("offload: %d %s", w.Code, w.Body)
	}
	f.arch.mu.Lock()
	for key := range f.arch.keys { // the archiver lost every data version
		if strings.HasPrefix(key, ".data.") {
			f.arch.keys[key] = nil
		}
	}
	f.arch.mu.Unlock()
	if err := os.WriteFile(f.root+"/apps/cal/index.html", []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := zeroDataCall(t, b.apiLifecycleSet, "POST", "", `{"component":"apps/cal","state":"enabled"}`, auth.Principal{Owner: true})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dataMissing":"this backup's data archive is missing (archiver apps/archiver has no version `) ||
		strings.Contains(w.Body.String(), d+" of") { // the offload's own backup is the one restored
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(f.source(), sealMarker) {
		t.Errorf("the source wasn't restored: %q", f.source())
	}
	if _, code, err := b.extractMember(bkTile, v, backup.KVName); code != http.StatusNotFound || err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("a data file of a missing data archive: %d %v", code, err)
	}

	// a main archive naming a version no URL can (a plaintext schema-3 one here)
	m := backup.Manifest{Schema: backup.SchemaSplit, Component: bkTile, Scope: bkTile, ScopeRoot: true, Includes: []string{"source", "data"},
		Data: &backup.DataRef{Key: dataArchiveKey(bkTile), Version: "../x"}}
	bad := f.arch.set(backupKey(bkTile), writeArchive(t, []archiveMember{manifestJSON(t, m), {"source/index.html", 0o644, []byte("crafted")}}))
	if r, _, err := b.restoreTile(bkTile, bad); err != nil || !strings.Contains(r.DataMissing, `version "../x"`) || f.source() != "crafted" {
		t.Errorf("a main archive naming an unusable version: %+v %v", r, err)
	}

	// an archiver answering no version: the backup fails, no main archive names it
	f.arch.noVersion = dataArchiveKey(bkTile)
	before := len(f.arch.versions(backupKey(bkTile)))
	if _, err := b.doBackup(bkTile); err == nil || !strings.Contains(err.Error(), "can't fetch again") {
		t.Errorf("a data PUT without a version: %v", err)
	}
	if len(f.arch.versions(backupKey(bkTile))) != before {
		t.Error("a main archive was written naming a data archive without a version")
	}
}

// covers PD-56 11§4 — a data archive is restored only with the main archive
// of its own backup: another backup's data archive in its place (same key,
// its backup id another) and an archive sealed under another key are
// refused before anything is written.
func TestSealedDataPairing(t *testing.T) {
	f := sealFx(t)
	b := f.b
	f.plane.setPrimary(bkTile, "dev") // a deployment archive, sealed under another key
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	v2, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	d2 := dataOf(t, f, v2)
	swap := func(body []byte) {
		f.arch.mu.Lock()
		defer f.arch.mu.Unlock()
		for i, x := range f.arch.keys[dataArchiveKey(bkTile)] {
			if x.v == d2 {
				f.arch.keys[dataArchiveKey(bkTile)][i].body = body
			}
		}
	}
	older := f.arch.keys[dataArchiveKey(bkTile)][1].body
	swap(older)
	if err := os.WriteFile(f.root+"/apps/cal/index.html", []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.restoreTile(bkTile, v2); err == nil || !strings.Contains(err.Error(), "another backup's") {
		t.Errorf("another backup's data archive: %v", err)
	}
	swap(f.arch.keys[archiveKey(bkTile, "dev")][0].body)
	if _, _, err := b.restoreTile(bkTile, v2); err == nil || !strings.Contains(err.Error(), "isn't sealed under the backup key the backup names") {
		t.Errorf("an archive under another key: %v", err)
	}
	if f.source() != "current" {
		t.Errorf("a refused restore wrote the source: %q", f.source())
	}
}
