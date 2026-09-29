package broker

// Sealed backups (plans/partitions/11-backup-encryption.md §Tests; PD-25,
// PD-56): every archive of a workspace with a vault barrier is sealed under
// its subject's backup key, a plaintext archive of any age still restores,
// erasing a key erases that data in every archive, and a key bundle carries
// the keys to another workspace. Built on backup_deployments_test.go's bkFx
// (apps/cal, a vault barrier, an archiver that keeps what it is given).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/util"
)

// unsealWith opens a sealed archive with b's keys, for a test to read.
func unsealWith(b *Broker) func(t *testing.T, body []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		r, _, err := backup.OpenSealed(bytes.NewReader(body), b.backupKeyFunc())
		if err != nil {
			t.Fatalf("unsealing an archive: %v", err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("unsealing an archive: %v", err)
		}
		return out
	}
}

const sealMarker = "PLAINTEXT-MARKER-7f3c"

// sealFx is bkFx holding main data and a source file with a known marker.
func sealFx(t *testing.T) *bkFx {
	t.Helper()
	f := newBkFx(t, false)
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", sealMarker+"-main")
	f.ns.putKV(bkTile, "dev", "events", "d", sealMarker+"-dev")
	if err := os.WriteFile(filepath.Join(f.root, "apps", "cal", "index.html"), []byte("<html>"+sealMarker+"</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *bkFx) source() string {
	b, _ := os.ReadFile(filepath.Join(f.root, "apps", "cal", "index.html"))
	return string(b)
}

// covers PD-56 11§1 11§4 — a sealed main archive and its data archive round
// trip: every object the archiver holds is sealed, names its subkey in the
// PUT's X-XBin-Backup-Subkey (which its header names too), and holds no
// plaintext; the key files hold no key in the clear; the restore brings the
// source and the data back, and a single file comes back from either
// archive.
func TestSealedBackupRoundTrip(t *testing.T) {
	f := sealFx(t)
	b := f.b
	f.plane.setPrimary(bkTile, "dev") // a deployment archive too
	v, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	mainKey, dataKey, devKey := backupKey(bkTile), dataArchiveKey(bkTile), archiveKey(bkTile, "dev")
	subjects := map[string]string{mainKey: tileSubject(bkTile), dataKey: nsSubject(bkTile, util.MainDeployment), devKey: nsSubject(bkTile, "dev")}
	kinds := map[string]string{mainKey: backup.KindMain, dataKey: backup.KindData, devKey: backup.KindDeployment}
	keys, err := b.backupKeys().list()
	if err != nil || len(keys) != 3 {
		t.Fatalf("keys %+v %v", keys, err)
	}
	for key, versions := range f.arch.keys {
		for _, x := range versions {
			if !backup.IsSealed(x.body) || bytes.Contains(x.body, []byte(sealMarker)) || bytes.Contains(x.body, []byte("backup.json")) {
				t.Errorf("%s %s: not sealed, or plaintext in it", key, x.v)
			}
			h, sealed, err := backup.ReadSealHeader(bytes.NewReader(x.body))
			if !sealed || err != nil || h.Subkey != f.arch.subkeys[x.v] || h.Kind != kinds[key] {
				t.Errorf("%s: header %+v (sealed %v, %v), PUT subkey %q", key, h, sealed, err, f.arch.subkeys[x.v])
			}
			k, err := b.backupKeys().read(h.Subkey)
			if err != nil || k.Subject != subjects[key] || k.Tile != bkTile || k.Gen != 1 {
				t.Errorf("%s: its key %+v %v", key, k, err)
			}
		}
	}
	// the key files hold wrapped keys only
	for _, k := range keys {
		raw, _ := os.ReadFile(b.backupKeys().path(k.ID))
		key, err := b.backupKeys().unwrap(k)
		if err != nil || len(key) != 32 || bytes.Contains(raw, key) {
			t.Errorf("key %s: %v", k.ID, err)
		}
		if fi, err := os.Stat(b.backupKeys().path(k.ID)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("key file mode: %v %v", fi, err)
		}
	}

	// single files: the source from the main archive, the data from the data archive
	if data, code, err := b.extractMember(bkTile, v, "source/index.html"); err != nil || code != 200 || !strings.Contains(string(data), sealMarker) {
		t.Errorf("a source file: %q %d %v", data, code, err)
	}
	if data, code, err := b.extractMember(bkTile, "latest", backup.KVName); err != nil || code != 200 ||
		!strings.Contains(string(data), base64.StdEncoding.EncodeToString([]byte(sealMarker+"-main"))) {
		t.Errorf("a data file: %q %d %v", data, code, err)
	}
	if _, code, err := b.extractMember(bkTile, v, "source/nope"); code != http.StatusNotFound || err == nil {
		t.Errorf("a missing file: %d %v", code, err)
	}

	// the restore brings both back
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "changed")
	f.ns.putKV(bkTile, "dev", "events", "d", "changed")
	if err := os.WriteFile(filepath.Join(f.root, "apps", "cal", "index.html"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, listed, err := b.restoreTile(bkTile, v)
	if err != nil || r.DataErased != "" || !r.Has("data") {
		t.Fatalf("restore: %+v %v", r, err)
	}
	if got := f.kv(util.MainDeployment, "m") + " " + f.kv("dev", "d"); got != "m="+sealMarker+"-main d="+sealMarker+"-dev" {
		t.Errorf("restored data: %s (listed %+v)", got, listed)
	}
	if !strings.Contains(f.source(), sealMarker) {
		t.Errorf("restored source: %q", f.source())
	}
	// ... and POST /deployments/restore of main follows the data pointer
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "changed")
	if _, _, err := b.RestoreDeploymentData(bkTile, "user:ana", restoreReq(util.MainDeployment, "", true), allow, f.stop); err != nil {
		t.Fatal(err)
	}
	if got := f.kv(util.MainDeployment, "m"); got != "m="+sealMarker+"-main" {
		t.Errorf("main's data restored into main: %s", got)
	}
	// a data archive is never a tile
	body := f.arch.keys[dataKey][0].body
	if _, err := b.restore(bkTile, bytes.NewReader(body), nil); err == nil || !strings.Contains(err.Error(), "not a tile") {
		t.Errorf("a data archive restored as a tile: %v", err)
	}
}

// covers PD-56 11§4 — tampering is refused before anything is written: a
// flipped byte in a sealed archive fails the restore, and the tile is left
// as it was.
func TestSealedRestoreTampered(t *testing.T) {
	f := sealFx(t)
	b := f.b
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), f.arch.keys[backupKey(bkTile)][0].body...)
	body[len(body)-100] ^= 1
	v := f.arch.set(backupKey(bkTile), body)
	if err := os.WriteFile(filepath.Join(f.root, "apps", "cal", "index.html"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.restoreTile(bkTile, v); err == nil || !strings.Contains(err.Error(), "tampered") {
		t.Fatalf("a tampered archive: %v", err)
	}
	if f.source() != "current" {
		t.Errorf("a refused restore wrote the source: %q", f.source())
	}
}

// covers PD-25 PD-56 11§3 11§4 — erasing a tile's data keys erases its data
// in every archive: the main archive still restores its source and says the
// data was erased (whether or not the archiver kept the dead versions), a
// data restore is refused with the reason, and the subject gets a new key at
// its next backup. --all erases the tile: key too. An unknown subkey names
// the import.
func TestBackupErase(t *testing.T) {
	f := sealFx(t)
	b := f.b
	f.arch.noEraseGC = true // first an archiver without the erase route
	v1, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	dataKey := dataArchiveKey(bkTile)
	erased, gc, err := b.EraseBackupKeys(bkTile, false, "test erase", "user:ana")
	if err != nil || len(erased) != 1 || erased[0].Subject != nsSubject(bkTile, util.MainDeployment) || !strings.Contains(gc, "can't delete by key") {
		t.Fatalf("erase: %+v %q %v", erased, gc, err)
	}
	if len(f.arch.versions(dataKey)) != 1 {
		t.Fatal("an archiver without the route lost versions")
	}
	if err := os.WriteFile(filepath.Join(f.root, "apps", "cal", "index.html"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.ns.putKV(bkTile, util.MainDeployment, "events", "m", "current")
	r, _, err := b.restoreTile(bkTile, v1)
	if err != nil || !strings.Contains(r.DataErased, "this backup's data was erased on") || !strings.Contains(r.DataErased, "(test erase)") || r.Has("data") {
		t.Fatalf("restore after the erase: %+v %v", r, err)
	}
	if !strings.Contains(f.source(), sealMarker) || f.kv(util.MainDeployment, "m") != "m=current" {
		t.Errorf("after the restore: source %q, data %s", f.source(), f.kv(util.MainDeployment, "m"))
	}
	_, _, err = b.RestoreDeploymentData(bkTile, "user:ana", restoreReq(util.MainDeployment, "", true), allow, f.stop)
	if err == nil || !strings.Contains(err.Error(), "this backup's data was erased") {
		t.Errorf("a data restore of erased data: %v", err)
	}
	if _, code, err := b.extractMember(bkTile, v1, backup.KVName); code != http.StatusConflict || err == nil || !strings.Contains(err.Error(), "erased") {
		t.Errorf("a data file of erased data: %d %v", code, err)
	}

	// the next backup seals the data under a new key, gen 2
	f.arch.noEraseGC = false
	v2, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := backup.ReadSealHeader(bytes.NewReader(f.arch.keys[dataKey][0].body))
	if k, err := b.backupKeys().read(h.Subkey); err != nil || k.Gen != 2 || h.Subkey == erased[0].ID {
		t.Errorf("the data's new key: %+v %v", k, err)
	}
	// erase again, with an archiver that collects: the dead versions go, and
	// a restore still says why
	erased2, gc, err := b.EraseBackupKeys(bkTile, false, "second erase", "owner")
	if err != nil || len(erased2) != 1 || !strings.Contains(gc, "deleted 1 version") {
		t.Fatalf("second erase: %+v %q %v", erased2, gc, err)
	}
	if got := f.arch.versions(dataKey); len(got) != 1 { // the first data version stays: its key's erase was before the route
		t.Errorf("data versions after the GC: %v", got)
	}
	if r, _, err := b.restoreTile(bkTile, v2); err != nil || !strings.Contains(r.DataErased, "second erase") {
		t.Errorf("restore after the GC: %+v %v", r, err)
	}

	// --all: the main archive can't be read either (the archiver keeps it here)
	f.arch.noEraseGC = true
	if erased, _, err := b.EraseBackupKeys(bkTile, true, "all", "owner"); err != nil || len(erased) != 1 || !strings.HasPrefix(erased[0].Subject, "tile:") {
		t.Fatalf("erase --all: %+v %v", erased, err)
	}
	if _, _, err := b.restoreTile(bkTile, v2); err == nil || !strings.Contains(err.Error(), "this backup's data was erased on") {
		t.Errorf("restore of an erased main archive: %v", err)
	}
	tombs, _ := b.backupKeys().tombstones()
	if len(tombs) != 3 || tombs[0].Reason != "test erase" || tombs[0].By != "user:ana" {
		t.Errorf("tombstones: %+v", tombs)
	}

	// another workspace's archive names the import
	other := sealFx(t)
	if _, err := other.b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	foreign := f.arch.set(backupKey(bkTile), other.arch.keys[backupKey(bkTile)][0].body)
	if _, _, err := b.restoreTile(bkTile, foreign); err == nil || !strings.Contains(err.Error(), "sealed by another workspace: import its keys (bx backup keys import)") {
		t.Errorf("a foreign archive: %v", err)
	}
}

// covers PD-56 11§4 11§7 — a plaintext archive made before sealing (schema
// 1, data inline) restores into a sealed workspace as it always did.
func TestPlaintextArchiveRestoresSealed(t *testing.T) {
	f := sealFx(t)
	b := f.b
	m := backup.Manifest{Component: bkTile, Scope: bkTile, ScopeRoot: true, Resources: map[string]string{"events": "kv"},
		Includes: []string{"source", "data"}}
	kv, _ := json.Marshal(map[string]map[string]string{"events": {"m": "b2xk"}}) // "old"
	v := f.arch.set(backupKey(bkTile), writeArchive(t, []archiveMember{manifestJSON(t, m),
		{"source/index.html", 0o644, []byte("<html>old</html>")}, {backup.KVName, 0o644, kv}}))
	r, _, err := b.restoreTile(bkTile, v)
	if err != nil || r.Schema != backup.Schema {
		t.Fatalf("a plaintext archive: %+v %v", r, err)
	}
	if f.source() != "<html>old</html>" || f.kv(util.MainDeployment, "m") != "m=old" {
		t.Errorf("restored: %q %s", f.source(), f.kv(util.MainDeployment, "m"))
	}
	if data, code, err := b.extractMember(bkTile, v, backup.KVName); err != nil || code != 200 || !bytes.Equal(data, kv) {
		t.Errorf("a plaintext archive's file: %q %d %v", data, code, err)
	}
}

// covers PD-56 11§7 — a sealed vault blocks every backup: the main archive
// too, before anything reaches the archiver; a scheduled run logs and skips.
func TestSealedVaultSkipsBackups(t *testing.T) {
	f := sealFx(t)
	b := f.b
	b.barrier.Seal()
	if _, err := b.doBackup(bkTile); err == nil || !strings.Contains(err.Error(), "vault sealed") {
		t.Fatalf("a backup with the vault sealed: %v", err)
	}
	b.runScheduledBackup(backupSchedule{Component: "apps/cal/widget", Schedule: "@daily"}) // a tile without data too
	if len(f.arch.puts) != 0 {
		t.Errorf("PUT while sealed: %v", f.arch.puts)
	}
}

// covers PD-56 11§5 — keys travel in a bundle: an export from one workspace
// imports into another under a different passphrase (the exporting
// workspace's passphrase opens it; a wrong one is refused), and the other
// workspace restores the first's archives. The old DEK is never persisted:
// the importing workspace's barrier is unchanged and its key files are
// wrapped under its own DEK. Tombstones travel: a key erased before the
// export is erased here too. The export status and the admin alert follow
// the exports.
func TestBackupKeysExportImport(t *testing.T) {
	a := sealFx(t)
	if _, err := a.b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	st, _ := a.b.backupKeysStatus()
	if st.Mode != "sealed" || st.Keys != 2 || st.Unexported != 2 || st.LastExport != "" {
		t.Errorf("status before an export: %+v", st)
	}
	if al := alertsAsAdmin(t, a.b); !strings.Contains(al, `"kind":"backup-keys"`) || !strings.Contains(al, "2 backup keys aren't in any export yet") {
		t.Errorf("alerts: %s", al)
	}
	// a key erased before the export: its tombstone goes with the bundle
	if _, _, err := a.b.EraseBackupKeys(bkTile, false, "before export", "owner"); err != nil {
		t.Fatal(err)
	}
	w := callAdmin(t, a.b.apiBackupKeysExport, "POST", "{}")
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	var bundle backupKeyBundle
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil || len(bundle.Keys) != 1 || len(bundle.Erased) != 1 || len(bundle.Barrier) == 0 {
		t.Fatalf("bundle: %+v %v", bundle, err)
	}
	st, _ = a.b.backupKeysStatus()
	if st.Unexported != 0 || st.LastExport == "" || st.Exports != 1 || st.ErasedSinceExport != 0 || st.Erased != 1 {
		t.Errorf("status after an export: %+v", st)
	}
	if al := alertsAsAdmin(t, a.b); strings.Contains(al, "backup-keys") {
		t.Errorf("alerts after the export: %s", al)
	}

	// the other workspace: its own vault passphrase
	b := newBkFx(t, true) // "namespace-pass"
	barrierBefore, _ := os.ReadFile(filepath.Join(b.root, "data", "vault", ".barrier.json"))
	body, _ := json.Marshal(map[string]any{"bundle": bundle, "passphrase": "wrong"})
	if w := callAdmin(t, b.b.apiBackupKeysImport, "POST", string(body)); w.Code != 400 || !strings.Contains(w.Body.String(), "passphrase") {
		t.Errorf("a wrong passphrase: %d %s", w.Code, w.Body)
	}
	body, _ = json.Marshal(map[string]any{"bundle": bundle, "passphrase": "backup-pass"})
	w = callAdmin(t, b.b.apiBackupKeysImport, "POST", string(body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"imported":1`) || !strings.Contains(w.Body.String(), `"erased":1`) {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	if again := callAdmin(t, b.b.apiBackupKeysImport, "POST", string(body)); !strings.Contains(again.Body.String(), `"imported":0`) {
		t.Errorf("a second import: %s", again.Body)
	}
	barrierAfter, _ := os.ReadFile(filepath.Join(b.root, "data", "vault", ".barrier.json"))
	if !bytes.Equal(barrierBefore, barrierAfter) {
		t.Error("the import changed this workspace's barrier")
	}
	var theirs struct {
		Salt       []byte `json:"salt"`
		WrappedDEK []byte `json:"wrapped_dek"`
	}
	_ = json.Unmarshal(bundle.Barrier, &theirs)
	_ = filepath.WalkDir(filepath.Join(b.root, "data"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			raw, _ := os.ReadFile(p)
			if bytes.Contains(raw, theirs.WrappedDEK) || bytes.Contains(raw, []byte(string(bundle.Barrier))) {
				t.Errorf("%s holds the other workspace's barrier", p)
			}
		}
		return nil
	})
	for _, k := range bundle.Keys {
		mine, err := b.b.backupKeys().read(k.ID)
		if err != nil || bytes.Equal(mine.Wrapped, k.Wrapped) || mine.Imported == "" {
			t.Errorf("imported key %s: %+v %v", k.ID, mine, err)
		}
	}
	if _, err := b.b.backupKeys().lookup(bundle.Erased[0].ID); err == nil || !strings.Contains(err.Error(), "before export") {
		t.Errorf("a key erased before the export: %v", err)
	}

	// the other workspace restores the first's main archive (its data was erased)
	v := b.arch.set(backupKey(bkTile), a.arch.keys[backupKey(bkTile)][0].body)
	for _, x := range a.arch.keys[dataArchiveKey(bkTile)] {
		b.arch.keys[dataArchiveKey(bkTile)] = append(b.arch.keys[dataArchiveKey(bkTile)], x)
	}
	if err := os.WriteFile(filepath.Join(b.root, "apps", "cal", "index.html"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, _, err := b.b.restoreTile(bkTile, v); err != nil || !strings.Contains(r.DataErased, "before export") {
		t.Fatalf("a restore on the other workspace: %+v %v", r, err)
	}
	if !strings.Contains(b.source(), sealMarker) {
		t.Errorf("restored source on the other workspace: %q", b.source())
	}
	// an imported key never seals: the other workspace's next backup has its own
	if _, err := b.b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	h, _, _ := backup.ReadSealHeader(bytes.NewReader(b.arch.keys[backupKey(bkTile)][0].body))
	if h.Subkey == bundle.Keys[0].ID {
		t.Error("an imported key sealed a new archive")
	}
	// a bundle's tombstones never erase a key of the importing workspace's own
	forged := bundle
	forged.Erased = []backupTombstone{{ID: h.Subkey, Subject: "tile:x", ErasedAt: "2026-01-01T00:00:00Z", Reason: "forged"}}
	body, _ = json.Marshal(map[string]any{"bundle": forged, "passphrase": "backup-pass"})
	if w := callAdmin(t, b.b.apiBackupKeysImport, "POST", string(body)); w.Code != 200 {
		t.Fatalf("a forged tombstone's import: %d %s", w.Code, w.Body)
	}
	if _, err := b.b.backupKeys().read(h.Subkey); err != nil {
		t.Errorf("a bundle's tombstone erased this workspace's own key: %v", err)
	}
}

// callAdmin runs a broker handler as the owner.
func callAdmin(t *testing.T, h func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
	t.Helper()
	return zeroDataCall(t, h, method, "", body, auth.Principal{Owner: true})
}

func alertsAsAdmin(t *testing.T, b *Broker) string {
	t.Helper()
	return callAdmin(t, b.apiAlerts, "GET", "").Body.String()
}

// covers PD-56 11§3 — POST /backup/erase is an admin's: others get 403, a
// bad body 400, and data keeps the tile: key.
func TestBackupEraseRoute(t *testing.T) {
	f := sealFx(t)
	if _, err := f.b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	if w := zeroDataCall(t, f.b.apiBackupErase, "POST", "", `{"component":"apps/cal","what":"data"}`, auth.Principal{Component: "apps/mail"}); w.Code != http.StatusForbidden {
		t.Errorf("a tile's erase: %d", w.Code)
	}
	if w := callAdmin(t, f.b.apiBackupErase, "POST", `{"component":"apps/cal","what":"everything"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a bad what: %d", w.Code)
	}
	w := callAdmin(t, f.b.apiBackupErase, "POST", `{"component":"apps/cal","what":"data"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"subject":"ns:`) || strings.Contains(w.Body.String(), `"subject":"tile:`) {
		t.Errorf("erase data: %d %s", w.Code, w.Body)
	}
	if keys, _ := f.b.backupKeys().list(); len(keys) != 1 || !strings.HasPrefix(keys[0].Subject, "tile:") {
		t.Errorf("keys left: %+v", keys)
	}
}
