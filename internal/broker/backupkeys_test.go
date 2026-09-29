package broker

// The backup keystore's edges (plans/partitions/11-backup-encryption.md §1,
// §3, §5): a workspace whose vault isn't set up backs nothing up, an erase
// commits at its tombstone, a passphrase change makes the exported bundles
// stale, the key routes are an admin's — the bundle's export and import
// and the erase a person's — and an erase waits for the tile's backup lock
// and takes the subjects it names. Built on backup_seal_test.go's sealFx.

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers PD-56 11§1 — without a vault barrier only the plaintext-vault mode
// (--insecure-vault, --no-auth) writes plain archives: production before
// its vault is set up backs nothing up — no archive reaches the archiver —
// and says so in GET /backup-keys (vault-locked).
func TestVaultLockedBacksNothingUp(t *testing.T) {
	b := testBroker(t) // no barrier, plaintext not allowed
	arch := &memArchiver{keys: map[string][]memVersion{}}
	b.ProxyHandler = arch
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.doBackup("apps/calendar"); err == nil || !strings.Contains(err.Error(), "the vault isn't set up yet") {
		t.Fatalf("a backup with no vault: %v", err)
	}
	if err := b.offload("apps/calendar", false); err == nil {
		t.Error("an offload with no vault archived")
	}
	if len(arch.puts) != 0 {
		t.Errorf("PUT with no vault: %v", arch.puts)
	}
	if st, _ := b.backupKeysStatus(); st.Mode != "vault-locked" {
		t.Errorf("status: %+v", st)
	}
	plaintextVault(b)
	if st, _ := b.backupKeysStatus(); st.Mode != "plaintext" {
		t.Errorf("status in the plaintext-vault mode: %+v", st)
	}
	if _, err := b.doBackup("apps/calendar"); err != nil || len(arch.puts) != 1 {
		t.Errorf("a plaintext-vault backup: %v %v", err, arch.puts)
	}
}

// covers PD-56 11§3 — an erase commits at its tombstone: a key file an
// interrupted erase left behind (the tombstone written, the file not yet
// removed) is refused with the erasure's reason and removed where it is
// met, never listed, exported or used to seal again.
func TestEraseCommitsAtTheTombstone(t *testing.T) {
	f := sealFx(t)
	b := f.b
	v, err := b.doBackup(bkTile)
	if err != nil {
		t.Fatal(err)
	}
	s := b.backupKeys()
	keys, _ := s.list()
	var data backupSubkey
	for _, k := range keys {
		if !strings.HasPrefix(k.Subject, "tile:") {
			data = k
		}
	}
	// the crash: the tombstone is on disk, the key file still there
	if err := s.writeTombstones([]backupTombstone{{ID: data.ID, Subject: data.Subject, Tile: data.Tile, Gen: data.Gen,
		ErasedAt: "2026-09-29T10:00:00Z", Reason: "interrupted", By: "owner"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path(data.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.lookup(data.ID); err == nil || !strings.Contains(err.Error(), "erased on 2026-09-29 (interrupted)") {
		t.Errorf("an erased key's lookup: %v", err)
	}
	if _, err := os.Stat(s.path(data.ID)); !os.IsNotExist(err) {
		t.Errorf("the erased key's file stayed: %v", err)
	}
	if r, _, err := b.restoreTile(bkTile, v); err != nil || !strings.Contains(r.DataErased, "interrupted") {
		t.Errorf("restore: %+v %v", r, err)
	}
	// the next backup seals under a new key, one generation on
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.list()
	for _, k := range keys {
		if k.ID == data.ID || k.Subject == data.Subject && k.Gen != data.Gen+1 {
			t.Errorf("after the erase: %+v", k)
		}
	}
}

// covers PD-56 11§5 — a passphrase change makes every exported bundle
// stale (they open the data key with the old passphrase): the admin alert
// and the status say so until a fresh export, which clears it; the status
// counts are cached, and a new key shows at once.
func TestRekeyMakesExportsStale(t *testing.T) {
	f := sealFx(t)
	b := f.b
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	// no export yet: a rekey has nothing to make stale
	if w := callAdmin(t, b.apiVaultRekey, "POST", `{"current":"backup-pass","new":"pass-two"}`); w.Code != 200 {
		t.Fatalf("rekey: %d %s", w.Code, w.Body)
	}
	if st, _ := b.backupKeysStatus(); st.PassphraseChanged != "" || st.Unexported != 2 {
		t.Errorf("status before any export: %+v", st)
	}
	if w := callAdmin(t, b.apiBackupKeysExport, "POST", "{}"); w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	if st, _ := b.backupKeysStatus(); st.Unexported != 0 || st.PassphraseChanged != "" {
		t.Errorf("status after the export: %+v", st)
	}
	if w := callAdmin(t, b.apiVaultRekey, "POST", `{"current":"pass-two","new":"pass-three"}`); w.Code != 200 {
		t.Fatalf("rekey: %d %s", w.Code, w.Body)
	}
	st, _ := b.backupKeysStatus()
	if st.PassphraseChanged == "" || st.Unexported != 2 || st.Exports != 1 {
		t.Errorf("status after the rekey: %+v", st)
	}
	if al := alertsAsAdmin(t, b); !strings.Contains(al, "the vault passphrase changed on") || !strings.Contains(al, "open with the old passphrase") {
		t.Errorf("alerts after the rekey: %s", al)
	}
	if w := callAdmin(t, b.apiBackupKeysExport, "POST", "{}"); w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	if st, _ := b.backupKeysStatus(); st.PassphraseChanged != "" || st.Unexported != 0 || st.Exports != 2 {
		t.Errorf("status after a fresh export: %+v", st)
	}
	if al := alertsAsAdmin(t, b); strings.Contains(al, "backup-keys") {
		t.Errorf("alerts after a fresh export: %s", al)
	}
	f.plane.setPrimary(bkTile, "dev") // a new key: the cached counts drop
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.backupKeysStatus(); st.Keys != 3 || st.Unexported != 1 {
		t.Errorf("status after a new key: %+v", st)
	}
}

// covers PD-56 11§3 11§5 — the key routes are an admin's: another tile and a
// signed-in non-admin get 403 on the status, the export, the import and
// the erase, and never see the backup-keys alert. The bundle's export and
// import and the erase are a person's too: a tile's backend holding xbin
// admin gets the status and 403 on those; the admin tile's frame under a
// person's login, and the person, pass.
func TestBackupKeyRoutesArePersonsAndAdmins(t *testing.T) {
	f := sealFx(t)
	b := f.b
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/plain", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	st := testUsers(t, b)
	if _, err := st.Upsert(users.User{ID: "bob", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	bundle, _ := json.Marshal(map[string]any{"bundle": map[string]any{"schema": 1}, "passphrase": "x"})
	routes := []struct {
		name   string
		h      func(http.ResponseWriter, *http.Request)
		method string
		body   string
		person bool // a person's act
	}{
		{"status", b.apiBackupKeysStatus, "GET", "", false},
		{"export", b.apiBackupKeysExport, "POST", "{}", true},
		{"import", b.apiBackupKeysImport, "POST", string(bundle), true},
		{"erase", b.apiBackupErase, "POST", `{"component":"apps/nope","what":"data"}`, true},
	}
	backend := auth.Principal{Component: "apps/plain", Via: "instance"}
	loginFrame := auth.Principal{Component: "apps/plain", Via: "frame", Gen: "o.x"}
	for _, r := range routes {
		for who, p := range map[string]auth.Principal{"another tile": {Component: "apps/mail"}, "a non-admin": bob} {
			if w := zeroDataCall(t, r.h, r.method, "", r.body, p); w.Code != http.StatusForbidden {
				t.Errorf("%s by %s: %d %s", r.name, who, w.Code, w.Body)
			}
		}
		w := zeroDataCall(t, r.h, r.method, "", r.body, backend)
		if r.person && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "a person's act")) || !r.person && w.Code != 200 {
			t.Errorf("%s by a backend holding xbin admin: %d %s", r.name, w.Code, w.Body)
		}
		for who, p := range map[string]auth.Principal{"the admin tile's login frame": loginFrame, "the owner": {Owner: true}} {
			if w := zeroDataCall(t, r.h, r.method, "", r.body, p); w.Code == http.StatusForbidden {
				t.Errorf("%s by %s: %d %s", r.name, who, w.Code, w.Body)
			}
		}
	}
	if al := zeroDataCall(t, b.apiAlerts, "GET", "", "", bob).Body.String(); strings.Contains(al, "backup-keys") {
		t.Errorf("a non-admin's alerts: %s", al)
	}
	// the erase answers what it couldn't reach: a tile that doesn't root
	// its scope names its root
	w := callAdmin(t, b.apiBackupErase, "POST", `{"component":"apps/cal/widget","what":"data"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"erased":[]`) || !strings.Contains(w.Body.String(), "doesn't root its scope: its data is backed up with apps/cal's archives") {
		t.Errorf("erase of a non-root tile: %d %s", w.Code, w.Body)
	}
}

// covers PD-56 11§3 — a tile's backup-key erase waits for its backup lock
// (a backup, a prune, a wiping hook holding it), and an erase by subject
// takes only the keys it names: one deployment's, here.
func TestBackupEraseWaitsAndPicks(t *testing.T) {
	f := sealFx(t)
	b := f.b
	f.plane.setPrimary(bkTile, "dev")
	if _, err := b.doBackup(bkTile); err != nil {
		t.Fatal(err)
	}
	release := b.holdBackups(bkTile)
	done := make(chan []backupTombstone)
	go func() {
		erased, _, _ := b.EraseBackupSubjects(bkTile, func(s string) bool { return s == nsSubject(bkTile, "dev") }, "dev reset", "owner")
		done <- erased
	}()
	select {
	case <-done:
		t.Fatal("the erase ran while the tile's backups were held")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	erased := <-done
	if len(erased) != 1 || erased[0].Subject != nsSubject(bkTile, "dev") {
		t.Fatalf("erased %+v", erased)
	}
	keys, _ := b.backupKeys().list()
	if len(keys) != 2 || slices.ContainsFunc(keys, func(k backupSubkey) bool { return k.Subject == nsSubject(bkTile, "dev") }) {
		t.Errorf("keys left: %+v", keys)
	}
}
