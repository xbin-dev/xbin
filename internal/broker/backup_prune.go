package broker

// backup_prune.go — each tile's backup lock, and the retention of a sealed
// workspace's data archives (plans/partitions/11-backup-encryption.md §4).
//
// A tile's backups, prunes and backup-key erasures take its backup lock
// (holdBackups), so a backup never seals under a key an erase is removing,
// and a prune never meets a data archive whose main archive is still on its
// way. A data archive is kept exactly as long as a retained main archive
// names it — never by a count of its own: a data archive the main archive
// of its backup never named (its PUT failed, or the archiver answered an
// error after storing it) would shift a count, and the oldest kept main
// archive's data would go. Which data version a main archive names is
// cached in data/backup-refs/<CompKey>.json when the backup writes it; a
// main archive the cache doesn't know (made before, or elsewhere) is read,
// only when a data version is up for deletion.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

type backupLockKey struct {
	b    *Broker
	tile string
}

var backupLocks sync.Map // backupLockKey → *sync.Mutex (the Broker struct stays as it is)

// holdBackups takes tile's backup lock until release: no backup, prune or
// backup-key erase of the tile runs meanwhile. The hooks that wipe a
// tile's data hold it across the wipe and the erase
// (eraseBackupSubjectsHeld).
func (b *Broker) holdBackups(tile string) (release func()) {
	v, _ := backupLocks.LoadOrStore(backupLockKey{b, tile}, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// backupRefs is data/backup-refs/<CompKey>.json: of one archiver, which
// data archive version each main archive version of the tile names ("":
// none).
type backupRefs struct {
	Provider string            `json:"provider"`
	Mains    map[string]string `json:"mains"`
}

func (b *Broker) backupRefsPath(comp string) string {
	return filepath.Join(b.Reg.Root, "data", "backup-refs", util.CompKey(comp)+".json")
}

// loadBackupRefs reads comp's cache for provider (empty for another
// archiver's, or none).
func (b *Broker) loadBackupRefs(provider, comp string) backupRefs {
	var r backupRefs
	if data, err := os.ReadFile(b.backupRefsPath(comp)); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	if r.Provider != provider || r.Mains == nil {
		r = backupRefs{Provider: provider, Mains: map[string]string{}}
	}
	return r
}

func (b *Broker) saveBackupRefs(comp string, r backupRefs) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = fsutil.WriteFileAtomicIn(b.backupRefsPath(comp), data, 0o600)
	}
	if err != nil {
		slog.Warn("backup: caching which data archive a main archive names", "component", comp, "err", err)
	}
}

// noteDataRef records that main archive version main of comp names data
// archive version data (the caller holds comp's backup lock).
func (b *Broker) noteDataRef(provider, comp, main, data string) {
	r := b.loadBackupRefs(provider, comp)
	r.Mains[main] = data
	b.saveBackupRefs(comp, r)
}

// archiveVersions lists key's versions at provider, newest first; a
// version no archiver URL can name is left out.
func (b *Broker) archiveVersions(provider, key string) ([]string, error) {
	code, body, err := b.archiveDo("GET", provider, "/archive/"+key+"/versions", nil)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, errors.New("archiver " + provider + ": " + firstLine(string(body)))
	}
	var out struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	var vs []string
	for _, v := range out.Versions {
		if archiveVersion.MatchString(v.Version) {
			vs = append(vs, v.Version)
		}
	}
	return vs, nil
}

// mainDataRef reads which data archive version main archive version v of
// comp names: "" for none, or for a main archive whose key was erased (it
// can never be restored, so neither can the data it names).
func (b *Broker) mainDataRef(provider, comp, v string) (string, error) {
	body, err := b.fetchArchive(provider, backupKey(comp), v)
	if errors.Is(err, errArchiveGone) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	br, err := b.openArchive(body)
	var gone erasedError
	switch {
	case errors.As(err, &gone):
		return "", nil
	case err != nil:
		return "", err
	case br.M.Data == nil || br.M.Data.Key != dataArchiveKey(comp):
		return "", nil
	}
	return br.M.Data.Version, nil
}

// pruneData deletes the versions of comp's data archive no retained main
// archive names. It runs after the main archive's own prune, under comp's
// backup lock. Nothing without a vault barrier — a plaintext workspace's
// archives hold their data inline, and it makes no extra archiver call —
// and nothing when a main archive it must read can't be: every data
// version stays until a later run can tell.
func (b *Broker) pruneData(comp string) {
	if sealed, err := b.sealing(); err != nil || !sealed {
		return
	}
	provider := b.archiveProvider(comp)
	if provider == "" {
		return
	}
	datas, err := b.archiveVersions(provider, dataArchiveKey(comp))
	if err != nil || len(datas) == 0 {
		return
	}
	mains, err := b.archiveVersions(provider, backupKey(comp))
	if err != nil {
		return
	}
	refs := b.loadBackupRefs(provider, comp)
	kept := backupRefs{Provider: provider, Mains: map[string]string{}}
	named := map[string]bool{}
	var unread []string
	for _, v := range mains {
		d, ok := refs.Mains[v]
		if !ok {
			unread = append(unread, v)
			continue
		}
		kept.Mains[v] = d
		named[d] = true
	}
	var doomed []string
	for _, d := range datas {
		if !named[d] {
			doomed = append(doomed, d)
		}
	}
	if len(doomed) > 0 {
		for _, v := range unread { // made before the cache, or elsewhere: read it
			d, err := b.mainDataRef(provider, comp, v)
			if err != nil {
				slog.Warn("backup retention: a main archive can't be read, so no data archive is pruned this time", "component", comp, "version", v, "err", err)
				b.saveBackupRefs(comp, kept)
				return
			}
			kept.Mains[v] = d
			named[d] = true
		}
	}
	for _, d := range doomed {
		if !named[d] {
			if code, body, err := b.archiveDo("DELETE", provider, "/archive/"+dataArchiveKey(comp)+"/versions/"+d, nil); err != nil || code >= 400 && code != 404 {
				slog.Warn("backup retention: deleting a data archive no kept backup names", "component", comp, "version", d, "code", code, "err", err, "body", firstLine(string(body)))
			}
		}
	}
	b.saveBackupRefs(comp, kept)
}

// dataRefOf is the data version a main archive's manifest names, for the
// cache ("" for none).
func dataRefOf(ref *backup.DataRef) string {
	if ref == nil {
		return ""
	}
	return ref.Version
}
