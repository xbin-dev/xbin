package broker

// backupkeys.go — the backup key hierarchy (plans/partitions/11-backup-
// encryption.md §2, §3, §5; PD-25, PD-56):
//
//	vault passphrase ─Argon2id→ KEK ─wraps→ DEK                       (internal/vault)
//	DEK ─EncryptFor("backup-subkey:"+id)→ wrapped subkey               data/vault/.backup-keys/<id>.json
//	subkey ─HKDF(salt of the archive)→ archive key ─AES-GCM STREAM→    the archive (internal/backup/seal.go)
//
// Subkeys are random 256-bit keys, one current per subject — tile:<TileKey>
// (a main archive: source, terminal layer, deployment records), ns:<main
// namespace> (the data archive), ns:.deployments/<escS>/<dep> (a deployment
// archive), part:… (partition archives, F17b) — so deleting one
// crypto-erases that data in every archive sealed under it: an HKDF label
// of the DEK could never be deleted. The id is random and opaque, the only
// thing about a key an archiver sees. A key is unwrapped for one backup or
// restore and never goes into an archive, to an archiver, into an API
// response or into a sandbox: the directory is xbind's, under data/vault/,
// which no sandbox mounts, and a "."-directory the vault's own listing
// skips.
//
// An erase appends a tombstone to erased.json — metadata only, so a
// restore can say why an archive is unreadable — and then deletes the key
// file (fsyncing the directory): the tombstone is the erase's commit
// point, so a key it names is refused, and its file removed, wherever it
// is met from then on, even when a failure or a crash came between the
// two. The subject gets a new key (gen+1) at its next backup. exports.json
// records the key bundles exported, for the admin nudges (an /alerts row,
// bx doctor, the admin console's Backup tab; backupkeys_status.go).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vault"
)

const (
	backupKeysDir     = ".backup-keys"
	backupKeySchema   = 1
	backupErasedFile  = "erased.json"
	backupExportsFile = "exports.json"
	backupWrapLabel   = "backup-subkey:"
)

// backupSubkey is one key's file. Tile is the tile path the subject
// belongs to (an erase names tiles); Imported marks a key another
// workspace's bundle brought: it opens that workspace's archives, and never
// seals new ones.
type backupSubkey struct {
	Schema   int    `json:"schema"`
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Tile     string `json:"tile,omitempty"`
	Gen      int    `json:"gen"`
	Created  string `json:"created"`
	Imported string `json:"imported,omitempty"`
	Wrapped  []byte `json:"wrapped"` // nonce||AES-GCM under the DEK's backup-subkey:<id> key
}

// backupTombstone records an erased key.
type backupTombstone struct {
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Tile     string `json:"tile,omitempty"`
	Gen      int    `json:"gen"`
	ErasedAt string `json:"erasedAt"`
	Reason   string `json:"reason"`
	By       string `json:"by,omitempty"`
}

// backupExports is exports.json: the last export, and every key id an
// export since the last passphrase change held; Rekeyed is when the
// passphrase changed after the last export (backupkeys_status.go).
type backupExports struct {
	Last    string   `json:"last,omitempty"`
	Count   int      `json:"count"`
	IDs     []string `json:"ids,omitempty"`
	Rekeyed string   `json:"rekeyed,omitempty"`
}

// erasedError is a restore's refusal of an archive whose key was erased.
type erasedError struct{ t backupTombstone }

func (e erasedError) Error() string {
	return fmt.Sprintf("this backup's data was erased on %s (%s)", dateOf(e.t.ErasedAt), e.t.Reason)
}

var (
	errUnknownSubkey = errors.New("this backup was sealed by another workspace: import its keys (bx backup keys import)")
	errNoBarrier     = errors.New("this backup is sealed, and this workspace has no vault barrier to hold its keys: set one up (bx vault unseal), then import the keys (bx backup keys import)")
	errVaultSealed   = errors.New("vault sealed — unseal before backing up or restoring (every archive is sealed under a backup key)")
	errVaultLocked   = errors.New("the vault isn't set up yet, and every archive is sealed under a key it holds: set it up first (bx vault unseal), then back up")
)

// backupKeyStore is one workspace's keystore; mu serializes its writes and
// guards counts, the export status' cached counts (backupkeys_status.go),
// which every write drops.
type backupKeyStore struct {
	mu     sync.Mutex
	dir    string
	b      *Broker
	counts atomic.Pointer[backupKeyCounts]
}

var backupKeyStores sync.Map // *Broker → *backupKeyStore (the Broker struct stays as it is)

func (b *Broker) backupKeys() *backupKeyStore {
	v, _ := backupKeyStores.LoadOrStore(b, &backupKeyStore{dir: filepath.Join(b.Reg.Root, "data", "vault", backupKeysDir), b: b})
	return v.(*backupKeyStore)
}

// sealing reports whether archives are sealed now: a vault barrier is set
// up. The one workspace whose archives are today's plain tars is the
// plaintext-vault mode (--insecure-vault, --no-auth: AllowInsecureVault);
// any other without a barrier is locked — production before the vault is
// set up — and backs nothing up, as a sealed vault doesn't: no archive can
// be sealed without the DEK, and none may go out in the clear where the
// vault is meant to exist.
func (b *Broker) sealing() (bool, error) {
	switch {
	case b.barrier == nil || !b.barrier.Initialized():
		if b.AllowInsecureVault {
			return false, nil // the plaintext-vault mode: today's tars
		}
		return false, errVaultLocked
	case b.barrier.Sealed():
		return false, errVaultSealed
	}
	return true, nil
}

// backupKeyFunc resolves an archive's subkey id for backup.Open.
func (b *Broker) backupKeyFunc() backup.KeyFunc { return b.backupKeys().lookup }

// tileSubject, nsSubject: the subjects of a tile's main archive and of a
// data namespace's archive (main's, or a deployment's beyond it).
func tileSubject(tile string) string { return "tile:" + util.TileKey(tile) }

func nsSubject(scope, dep string) string {
	main, _ := scopeKeys(scope, util.MainDeployment) // main's keys: never an error
	if dep == "" || dep == util.MainDeployment {
		return "ns:" + main.DirKey
	}
	k, err := scopeKeys(scope, dep)
	if err != nil {
		return "ns:" + main.DirKey + "+" + dep // never a namespace's; still one subject per name
	}
	return "ns:" + k.NS
}

func newSubkeyID() (string, error) {
	var r [16]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	return "bk-" + hex.EncodeToString(r[:]), nil
}

func (s *backupKeyStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

// list reads every key file (never unwrapping one). A key a tombstone
// names is erased: its file, which a failed or interrupted erase left, is
// removed now and never listed.
func (s *backupKeyStore) list() ([]backupSubkey, error) {
	ents, err := os.ReadDir(s.dir) // walk-ok: data/vault is xbind's own; no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return nil, err
	}
	var out []backupSubkey
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || !backup.SubkeyID.MatchString(id) {
			continue
		}
		if tombstoned(tombs, id) {
			s.finishErase(id)
			continue
		}
		k, err := s.read(id)
		if err != nil {
			slog.Warn("backup keys: a key file can't be read", "id", id, "err", err)
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

func (s *backupKeyStore) read(id string) (backupSubkey, error) {
	var k backupSubkey
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return k, err
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return k, err
	}
	if k.ID != id || k.Schema != backupKeySchema || len(k.Wrapped) == 0 {
		return k, fmt.Errorf("key file %s isn't a schema-%d key of that id", id, backupKeySchema)
	}
	return k, nil
}

// write writes a key file durably: an archive sealed under the key leaves
// the host once this returns, so the file and its name are on disk first —
// the directory is fsynced (and, when this made it, data/vault too), and
// a sync that fails fails the write.
func (s *backupKeyStore) write(k backupSubkey) error {
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(k.ID+".json", data)
}

// writeFile writes one of the store's files durably (write).
func (s *backupKeyStore) writeFile(name string, data []byte) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	s.counts.Store(nil)
	if err := fsutil.WriteFileAtomic(filepath.Join(s.dir, name), data, 0o600); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// ensureDir makes the store's directory, fsyncing data/vault when it does.
func (s *backupKeyStore) ensureDir() error {
	if _, err := os.Stat(s.dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return syncDir(filepath.Dir(s.dir))
}

// syncDir fsyncs a directory: the names in it are on disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *backupKeyStore) unwrap(k backupSubkey) ([]byte, error) {
	key, err := s.b.barrier.DecryptFor(backupWrapLabel+k.ID, k.Wrapped)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			return nil, errVaultSealed
		}
		return nil, fmt.Errorf("backup key %s doesn't unwrap under this vault: %w", k.ID, err)
	}
	return key, nil
}

// subkeyFor answers subject's current key, unwrapped, creating it (gen one
// past every key and tombstone the subject had) when it has none. The
// caller zeroes the key once its archive is written.
func (s *backupKeyStore) subkeyFor(subject, tile string) (string, []byte, error) {
	if s.b.barrier == nil || !s.b.barrier.Initialized() {
		return "", nil, errors.New("no vault barrier: archives aren't sealed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.list()
	if err != nil {
		return "", nil, err
	}
	gen, cur := 0, -1
	for i, k := range keys {
		if k.Subject != subject {
			continue
		}
		gen = max(gen, k.Gen)
		if k.Imported == "" && (cur < 0 || k.Gen > keys[cur].Gen) {
			cur = i
		}
	}
	if cur >= 0 {
		key, err := s.unwrap(keys[cur])
		return keys[cur].ID, key, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return "", nil, err
	}
	for _, t := range tombs {
		if t.Subject == subject {
			gen = max(gen, t.Gen)
		}
	}
	id, err := newSubkeyID()
	if err != nil {
		return "", nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, err
	}
	wrapped, err := s.b.barrier.EncryptFor(backupWrapLabel+id, key)
	if err != nil {
		clear(key)
		if errors.Is(err, vault.ErrSealed) {
			err = errVaultSealed
		}
		return "", nil, err
	}
	k := backupSubkey{Schema: backupKeySchema, ID: id, Subject: subject, Tile: tile, Gen: gen + 1,
		Created: nowStamp(time.Now()), Wrapped: wrapped}
	if err := s.write(k); err != nil {
		clear(key)
		return "", nil, err
	}
	slog.Info("backup key created", "id", id, "subject", subject, "tile", tile, "gen", k.Gen)
	return id, key, nil
}

// lookup unwraps key id for a restore: an erased key's error names the
// erasure; an id this workspace never had names the import.
func (s *backupKeyStore) lookup(id string) ([]byte, error) {
	if !backup.SubkeyID.MatchString(id) {
		return nil, errUnknownSubkey
	}
	tombs, err := s.tombstones()
	if err != nil {
		return nil, err
	}
	for _, t := range tombs {
		if t.ID == id {
			s.finishErase(id) // a file an interrupted erase left
			return nil, erasedError{t}
		}
	}
	k, err := s.read(id)
	if err == nil {
		if s.b.barrier == nil || !s.b.barrier.Initialized() {
			return nil, errNoBarrier
		}
		return s.unwrap(k)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if s.b.barrier == nil || !s.b.barrier.Initialized() {
		return nil, errNoBarrier
	}
	return nil, errUnknownSubkey
}

func (s *backupKeyStore) tombstones() ([]backupTombstone, error) {
	var out []backupTombstone
	data, err := os.ReadFile(filepath.Join(s.dir, backupErasedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(data, &out)
}

func (s *backupKeyStore) writeTombstones(ts []backupTombstone) error {
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(backupErasedFile, data)
}

func tombstoned(tombs []backupTombstone, id string) bool {
	return slices.ContainsFunc(tombs, func(t backupTombstone) bool { return t.ID == id })
}

// removeKey deletes a key file, fsyncing the directory.
func (s *backupKeyStore) removeKey(id string) error {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.counts.Store(nil)
	return syncDir(s.dir)
}

// finishErase removes the file of a key a tombstone names, which an erase
// that failed or was cut short left behind.
func (s *backupKeyStore) finishErase(id string) {
	if _, err := os.Lstat(s.path(id)); err != nil {
		return
	}
	if err := s.removeKey(id); err != nil {
		slog.Warn("backup keys: an erased key's file can't be removed", "id", id, "err", err)
		return
	}
	slog.Info("backup key erase finished", "id", id)
}

// erase erases every key match picks (§3 steps 1-2): the tombstones are
// written first — the commit point: from then on the keys are refused
// everywhere and their files removed wherever met — then the files are
// deleted. The data sealed under them is unreadable in every archive from
// now on. It answers the tombstones written; err names a key file that
// couldn't be removed yet (erased all the same).
func (s *backupKeyStore) erase(match func(backupSubkey) bool, reason, by string) ([]backupTombstone, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.list()
	if err != nil {
		return nil, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return nil, err
	}
	var done []backupTombstone
	now := nowStamp(time.Now())
	for _, k := range keys {
		if match(k) {
			done = append(done, backupTombstone{ID: k.ID, Subject: k.Subject, Tile: k.Tile, Gen: k.Gen, ErasedAt: now, Reason: reason, By: by})
		}
	}
	if len(done) == 0 {
		return nil, nil
	}
	if err := s.writeTombstones(append(tombs, done...)); err != nil {
		return nil, err // nothing erased: no tombstone, every key as it was
	}
	var errs []error
	for _, t := range done {
		if err := s.removeKey(t.ID); err != nil {
			errs = append(errs, fmt.Errorf("backup key %s is erased, but its file isn't removed yet: %w", t.ID, err))
		}
		slog.Info("backup key erased", "id", t.ID, "subject", t.Subject, "tile", t.Tile, "gen", t.Gen, "reason", reason, "by", by)
	}
	return done, errors.Join(errs...)
}

// EraseBackupKeys crypto-erases tile's backups: its data keys (every ns:
// and part: subject of the tile), and with all its tile: key too, so no
// archive of it can be read again (EraseBackupSubjects).
func (b *Broker) EraseBackupKeys(tile string, all bool, reason, by string) ([]backupTombstone, string, error) {
	return b.EraseBackupSubjects(tile, func(subject string) bool {
		return all || !strings.HasPrefix(subject, "tile:")
	}, reason, by)
}

// EraseBackupSubjects crypto-erases the backup keys of tile whose subject
// match picks — every data key, or one partition's part: key — under the
// tile's backup lock, so no backup of the tile is sealing under one of them
// meanwhile. The archiver is then asked to drop the dead versions (§3 step
// 3); gc says how that went. It answers the tombstones written even with
// an error (a key file not removed yet: erased all the same).
func (b *Broker) EraseBackupSubjects(tile string, match func(subject string) bool, reason, by string) ([]backupTombstone, string, error) {
	defer b.holdBackups(tile)()
	return b.eraseBackupSubjectsHeld(tile, match, reason, by)
}

// eraseBackupSubjectsHeld is EraseBackupSubjects for a caller that holds
// tile's backup lock (holdBackups). The partition fabric's hooks that wipe
// data (a mode switch's wipe, a partition's purge, sweep or reset) keep
// this order, so no backup seals the doomed data under a fresh key between
// the wipe and the erase: stop what writes the data, holdBackups(tile),
// remove the data, eraseBackupSubjectsHeld, release.
func (b *Broker) eraseBackupSubjectsHeld(tile string, match func(subject string) bool, reason, by string) ([]backupTombstone, string, error) {
	erased, err := b.backupKeys().erase(func(k backupSubkey) bool { return k.Tile == tile && match(k.Subject) }, reason, by)
	if len(erased) == 0 {
		return nil, "", err
	}
	return erased, b.archiverErase(tile, erased), err
}

// archiverErase asks tile's archiver to delete every version sealed under
// the erased keys: POST /archive/erase {subkeys} → {deleted}. One without
// the route (404/405) keeps them until retention prunes them; they are
// unreadable either way.
func (b *Broker) archiverErase(tile string, erased []backupTombstone) string {
	provider := b.archiveProvider(tile)
	if provider == "" {
		return "no archiver bound: nothing to collect"
	}
	ids := make([]string, 0, len(erased))
	for _, t := range erased {
		ids = append(ids, t.ID)
	}
	body, _ := json.Marshal(map[string]any{"subkeys": ids})
	code, resp, err := b.archiveDo("POST", provider, "/archive/erase", strings.NewReader(string(body)))
	switch {
	case err != nil:
		return "archiver: " + err.Error()
	case code == 404 || code == 405:
		return "archiver " + provider + " can't delete by key (update it): the dead versions stay until retention prunes them"
	case code >= 400:
		return "archiver " + provider + ": " + firstLine(string(resp))
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(resp, &out)
	return fmt.Sprintf("archiver %s deleted %d version(s)", provider, out.Deleted)
}
