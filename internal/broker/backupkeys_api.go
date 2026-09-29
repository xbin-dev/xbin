package broker

// backupkeys_api.go — the backup keys' disaster recovery and erase routes
// (plans/partitions/11-backup-encryption.md §3, §5): a key bundle exported
// and imported, the export status the admin nudges read, and an admin's
// crypto-erase of a tile's backups.
//
// A bundle is {schema, workspace, created, barrier, keys, erased}: the
// vault's .barrier.json (the DEK wrapped under the passphrase's KEK), every
// key file (wrapped under the DEK) and every tombstone. Without the vault
// passphrase it opens nothing. An import unwraps the other workspace's DEK
// in memory with its passphrase, re-wraps each key under this workspace's
// DEK, and appends the tombstones; it never adopts the other DEK or
// passphrase.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/vault"
)

const (
	backupBundleSchema = 1
	maxBundleKeys      = 1 << 16
)

type backupKeyBundle struct {
	Schema    int               `json:"schema"`
	Workspace string            `json:"workspace"`
	Created   string            `json:"created"`
	Barrier   json.RawMessage   `json:"barrier"`
	Keys      []backupSubkey    `json:"keys"`
	Erased    []backupTombstone `json:"erased"`
}

// registerBackupKeys mounts the routes (Register calls it).
func (b *Broker) registerBackupKeys(srv *server.Server) {
	srv.RegisterAPI("GET /backup-keys", b.apiBackupKeysStatus)
	srv.RegisterAPI("POST /backup-keys/export", b.apiBackupKeysExport)
	srv.RegisterAPI("POST /backup-keys/import", b.apiBackupKeysImport)
	srv.RegisterAPI("POST /backup/erase", b.apiBackupErase)
}

// export builds the bundle and records it: every key it holds is in an
// export from now on.
func (s *backupKeyStore) export(workspace string) (backupKeyBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.barrier == nil || !s.b.barrier.Initialized() {
		return backupKeyBundle{}, errors.New("this workspace has no vault barrier: its backups aren't sealed, and there are no keys to export")
	}
	kf, err := s.b.barrier.Keyfile()
	if err != nil {
		return backupKeyBundle{}, err
	}
	keys, err := s.list()
	if err != nil {
		return backupKeyBundle{}, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return backupKeyBundle{}, err
	}
	now := nowStamp(time.Now())
	bundle := backupKeyBundle{Schema: backupBundleSchema, Workspace: workspace, Created: now, Barrier: kf,
		Keys: append([]backupSubkey{}, keys...), Erased: append([]backupTombstone{}, tombs...)}
	ex := s.exports()
	ex.Last, ex.Count = now, ex.Count+1
	for _, k := range keys {
		if !slices.Contains(ex.IDs, k.ID) {
			ex.IDs = append(ex.IDs, k.ID)
		}
	}
	data, _ := json.MarshalIndent(ex, "", "  ")
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return bundle, err
	}
	return bundle, fsutil.WriteFileAtomic(filepath.Join(s.dir, backupExportsFile), data, 0o600)
}

// backupImport is what an import did.
type backupImport struct {
	Imported int `json:"imported"` // keys re-wrapped under this vault
	Present  int `json:"present"`  // keys this workspace already had
	Erased   int `json:"erased"`   // tombstones taken (a key of theirs present here is erased)
}

// importBundle re-wraps bundle's keys under this workspace's DEK. Every key
// is unwrapped (checked) before anything is written; a key this workspace
// has, or erased, is left as it is; a tombstone of the bundle erases the key
// here too.
func (s *backupKeyStore) importBundle(bundle backupKeyBundle, passphrase, by string) (backupImport, error) {
	var out backupImport
	switch {
	case bundle.Schema != backupBundleSchema:
		return out, fmt.Errorf("a key bundle of schema %d, which this xbind doesn't read", bundle.Schema)
	case len(bundle.Barrier) == 0:
		return out, errors.New("the bundle holds no vault barrier")
	case len(bundle.Keys) > maxBundleKeys:
		return out, fmt.Errorf("the bundle holds %d keys, over %d", len(bundle.Keys), maxBundleKeys)
	case s.b.barrier == nil || !s.b.barrier.Initialized():
		return out, errors.New("this workspace has no vault barrier to hold the keys: set one up first (bx vault unseal)")
	case s.b.barrier.Sealed():
		return out, errVaultSealed
	}
	theirs, err := vault.FromKeyfile(bundle.Barrier)
	if err != nil {
		return out, fmt.Errorf("the bundle's vault barrier: %w", err)
	}
	if err := theirs.Unseal(passphrase); err != nil {
		return out, errors.New("the passphrase doesn't open the bundle's vault barrier (it is the vault passphrase of the workspace that exported it)")
	}
	defer theirs.Seal() // their DEK lives only for this import
	s.mu.Lock()
	defer s.mu.Unlock()
	have, err := s.list()
	if err != nil {
		return out, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return out, err
	}
	known := func(id string) bool {
		return slices.ContainsFunc(have, func(k backupSubkey) bool { return k.ID == id }) ||
			slices.ContainsFunc(tombs, func(t backupTombstone) bool { return t.ID == id })
	}
	theirTomb := func(id string) bool {
		return slices.ContainsFunc(bundle.Erased, func(t backupTombstone) bool { return t.ID == id })
	}
	now := nowStamp(time.Now())
	var add []backupSubkey
	for _, k := range bundle.Keys {
		switch {
		case !backup.SubkeyID.MatchString(k.ID) || k.Subject == "" || len(k.Wrapped) == 0:
			return out, fmt.Errorf("the bundle's key %q isn't a key file", k.ID)
		case known(k.ID) || theirTomb(k.ID) || slices.ContainsFunc(add, func(a backupSubkey) bool { return a.ID == k.ID }):
			out.Present++
			continue
		}
		key, err := theirs.DecryptFor(backupWrapLabel+k.ID, k.Wrapped)
		if err != nil {
			return out, fmt.Errorf("the bundle's key %s doesn't unwrap under its vault: nothing was imported", k.ID)
		}
		wrapped, err := s.b.barrier.EncryptFor(backupWrapLabel+k.ID, key)
		clear(key)
		if err != nil {
			return out, err
		}
		add = append(add, backupSubkey{Schema: backupKeySchema, ID: k.ID, Subject: k.Subject, Tile: k.Tile, Gen: k.Gen,
			Created: k.Created, Imported: now, Wrapped: wrapped})
	}
	for _, k := range add {
		if err := s.write(k); err != nil {
			return out, err
		}
		out.Imported++
	}
	// Their erasures hold here: a key of theirs this workspace has (an
	// earlier import) goes, and each tombstone is recorded. A bundle's
	// tombstones aren't sealed by its barrier, so one never erases a key of
	// this workspace's own: only an imported one.
	var newTombs []backupTombstone
	for _, t := range bundle.Erased {
		if !backup.SubkeyID.MatchString(t.ID) || slices.ContainsFunc(tombs, func(x backupTombstone) bool { return x.ID == t.ID }) {
			continue
		}
		if k, err := s.read(t.ID); err == nil {
			if k.Imported == "" {
				continue // ours: an admin erases it here (POST /backup/erase), never a bundle
			}
			if err := os.Remove(s.path(t.ID)); err != nil {
				return out, err
			}
			t.Reason += " (erased by the exporting workspace)"
		}
		newTombs = append(newTombs, t)
	}
	if len(newTombs) > 0 {
		if err := s.writeTombstones(append(tombs, newTombs...)); err != nil {
			return out, err
		}
		out.Erased = len(newTombs)
	}
	slog.Info("backup keys imported", "workspace", bundle.Workspace, "imported", out.Imported, "present", out.Present, "erased", out.Erased, "by", by)
	return out, nil
}

// ---- routes ----

func (b *Broker) apiBackupKeysStatus(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	st, err := b.backupKeysStatus()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, st)
}

func (b *Broker) apiBackupKeysExport(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	bundle, err := b.backupKeys().export(filepath.Base(b.Reg.Root))
	if err != nil {
		server.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, bundle)
}

func (b *Broker) apiBackupKeysImport(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct {
		Bundle     backupKeyBundle `json:"bundle"`
		Passphrase string          `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&body); err != nil || body.Passphrase == "" {
		server.WriteError(w, http.StatusBadRequest, "need {bundle, passphrase}: the bundle bx backup keys export wrote, and the exporting workspace's vault passphrase")
		return
	}
	out, err := b.backupKeys().importBundle(body.Bundle, body.Passphrase, principalName(r))
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// apiBackupErase is POST /backup/erase {component, what: "data"|"all"}: an
// admin's crypto-erase of a tile's backups (§3). data erases its data
// keys — the source stays restorable; all its every key.
func (b *Broker) apiBackupErase(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct{ Component, What string }
	if err := server.DecodeJSON(r, &body); err != nil || body.Component == "" || body.What != "data" && body.What != "all" {
		server.WriteError(w, http.StatusBadRequest, `need {component, what: "data"|"all"}`)
		return
	}
	erased, gc, err := b.EraseBackupKeys(body.Component, body.What == "all", "bx backup erase --"+body.What, principalName(r))
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := []map[string]any{}
	for _, t := range erased {
		rows = append(rows, map[string]any{"id": t.ID, "subject": t.Subject, "gen": t.Gen})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "component": body.Component, "erased": rows, "archiver": gc})
}

// principalName names who acted, for a tombstone and the log.
func principalName(r *http.Request) string { return auth.PrincipalOf(r).From() }
