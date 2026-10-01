package broker

// backupkeys_api.go — the backup keys' disaster recovery and erase routes
// (plans/partitions/11-backup-encryption.md §3, §5): a key bundle exported
// and imported, the export status the admin nudges read, and an admin's
// crypto-erase of a tile's backups.
//
// A bundle is {schema, workspace, created, barrier, keys, erased}: the
// vault's .barrier.json (the DEK wrapped under the passphrase's KEK), every
// key file (wrapped under the DEK) and every tombstone. Without the vault
// passphrase it opens nothing; with the passphrase in force when it was
// exported it opens the workspace's DEK itself — every vault secret and
// all resource data at rest, not only the backups — and a passphrase
// change doesn't take that back (the DEK never rotates), so the nudges ask
// for a fresh export after one and the docs say to destroy older bundles.
// An import unwraps the other workspace's DEK in memory with its
// passphrase, re-wraps each key under this workspace's DEK, and appends the
// tombstones; it never adopts the other DEK or passphrase.
//
// Export, import and erase are a person's acts (requireAdminPerson): an
// admin in their own session or through the admin tile's frame under their
// login, never a tile's backend, terminal or agent on its tile's grants.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
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
	b.registerPartitionBackups(srv) // a person's partition's archives: list, restore (backup_partition_restore.go)
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
	ex.Last, ex.Count, ex.Rekeyed = now, ex.Count+1, ""
	for _, k := range keys {
		if !slices.Contains(ex.IDs, k.ID) {
			ex.IDs = append(ex.IDs, k.ID)
		}
	}
	return bundle, s.writeExports(ex)
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
	// earlier import) goes — its tombstone first, as every erase — and each
	// tombstone is recorded. A bundle's tombstones aren't sealed by its
	// barrier, so one never erases a key of this workspace's own: only an
	// imported one.
	var newTombs []backupTombstone
	var drop []string
	for _, t := range bundle.Erased {
		if !backup.SubkeyID.MatchString(t.ID) || tombstoned(tombs, t.ID) || tombstoned(newTombs, t.ID) {
			continue
		}
		if k, err := s.read(t.ID); err == nil {
			if k.Imported == "" {
				continue // ours: an admin erases it here (POST /backup/erase), never a bundle
			}
			drop = append(drop, t.ID)
			t.Reason += " (erased by the exporting workspace)"
		}
		newTombs = append(newTombs, t)
	}
	if len(newTombs) > 0 {
		if err := s.writeTombstones(append(tombs, newTombs...)); err != nil {
			return out, err
		}
		out.Erased = len(newTombs)
		for _, id := range drop {
			s.finishErase(id)
		}
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
	if _, ok := b.requireAdminPerson(w, r); !ok {
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
	p, ok := b.requireAdminPerson(w, r)
	if !ok {
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
	out, err := b.backupKeys().importBundle(body.Bundle, body.Passphrase, p.From())
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// apiBackupErase is POST /backup/erase {component, what: "data"|"all"}: an
// admin's crypto-erase of a tile's backups (§3). data erases its data
// keys — the source stays restorable; all its every key. note says what
// the erase couldn't reach: the tile's data when another tile roots its
// scope, nothing sealed yet, and — always — the plain archives made before
// sealing, which hold the data in the clear until deleted at the archiver.
func (b *Broker) apiBackupErase(w http.ResponseWriter, r *http.Request) {
	p, ok := b.requireAdminPerson(w, r)
	if !ok {
		return
	}
	var body struct{ Component, What string }
	if err := server.DecodeJSON(r, &body); err != nil || body.Component == "" || body.What != "data" && body.What != "all" {
		server.WriteError(w, http.StatusBadRequest, `need {component, what: "data"|"all"}`)
		return
	}
	erased, gc, err := b.EraseBackupKeys(body.Component, body.What == "all", "bx backup erase --"+body.What, p.From())
	if err != nil && len(erased) == 0 {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.noteBackupErase(body.Component, erased, "bx backup erase --"+body.What, p.From(), "") // the tile's history, when it has one
	rows := []map[string]any{}
	for _, t := range erased {
		rows = append(rows, map[string]any{"id": t.ID, "subject": t.Subject, "gen": t.Gen})
	}
	out := map[string]any{"ok": true, "component": body.Component, "erased": rows, "archiver": gc, "note": b.eraseNote(body.Component, len(erased) > 0)}
	if err != nil { // erased (tombstoned): a key file is removed at its next use
		out["error"] = err.Error()
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// eraseNote is what POST /backup/erase couldn't reach for tile.
func (b *Broker) eraseNote(tile string, erased bool) string {
	const plain = "archives made before backups were sealed (or without a vault barrier) are plain tars no key erases: delete them at the archiver when their data must go"
	c, ok := b.Reg.Component(tile)
	_, isRoot := b.Reg.Scopes()[tile]
	switch {
	case erased:
		return plain
	case ok && !isRoot && c.Scope != "":
		return tile + " doesn't root its scope: its data is backed up with " + c.Scope + "'s archives — erase that tile's (bx backup erase " + c.Scope + " --data). " + plain
	default:
		return tile + " has no sealed backups whose keys this erases (no backup sealed its data yet, or its keys were erased before). " + plain
	}
}

// requireAdminPerson is requireAdmin for the backup keys' irreversible
// and exfiltrating acts — a crypto-erase, a key bundle's export or import:
// a person who is a workspace admin, in their own session (bx with the
// root token or a login, the admin console) or through the admin tile's
// frame under their login (AdminFrameDriver), answered as that person. A
// tile's backend, terminal or agent session, cron or bus never passes on
// its tile's xbin admin grant alone.
func (b *Broker) requireAdminPerson(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p := auth.PrincipalOf(r)
	if p.Component != "" {
		d, ok := b.AdminFrameDriver(p)
		if !ok {
			server.WriteError(w, http.StatusForbidden, "a person's act: an admin in their own session (bx, the admin console) — no tile's backend, terminal or agent erases backups or moves backup keys, whatever its tile holds", "/docs/auth.md")
			return p, false
		}
		p = d
	}
	if !b.IsAdmin(p) {
		server.WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
		return p, false
	}
	return p, true
}
