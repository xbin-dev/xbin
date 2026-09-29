package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// Restore: the other half of backup.go — a component's archive unpacked back
// into place (plans/lifecycle.md).

// restore unpacks a backup tar of comp, reconstructing the component at the
// path its manifest records — which must be comp: the archive came from a
// tile (the archiver), so nothing it names is trusted as a host path.
//
// Deployment state the archive carries is comp's, settled through put
// before anything else is written (backup_deploy.go). A deployment archive
// is never a tile: POST /deployments/restore restores it.
//
// The restore runs as xbind and writes into trees sandboxes write (WP-9,
// plans/tile-sandbox-runtime.md): the tile's source, its resource mounts, its
// terminal layer. Every write goes through an os.Root at the tree's top, so
// no symlink — planted before the restore or swapped in during it — carries
// a write out of the tree. The tile's dir is reached from the workspace
// without symlinks; a symlink met on the way to a restored file is replaced,
// never followed. The terminal layer is rebuilt in a fresh staging dir and
// swapped in whole once the sessions holding it are gone (restoreDst.finish).
//
// The archive is read through backup.Open: a sealed one is decrypted with
// its backup key, a plaintext one — of any age — read as it always was.
func (b *Broker) restore(comp string, r io.Reader, put deploymentRestorer) (backup.Manifest, error) {
	br, err := backup.Open(r, b.backupKeyFunc())
	if err != nil {
		return backup.Manifest{}, err
	}
	return b.restoreFrom(comp, br, nil, put)
}

// restoreFrom is restore of an opened archive, and of the data archive a
// split main archive names (data: nil when it names none, or its key was
// erased), whose data members are written as an inline archive's are.
func (b *Broker) restoreFrom(comp string, br, data *backup.Reader, put deploymentRestorer) (backup.Manifest, error) {
	m := br.M
	switch {
	case m.DeploymentArchive():
		return m, fmt.Errorf("the archive holds deployment %q's data of %s, not a tile: restore it into a deployment (POST /deployments/restore)", m.Deployment, m.Component)
	case m.DataArchive():
		return m, fmt.Errorf("the archive holds the data of %s, not a tile: restore its main archive, or restore it into main (POST /deployments/restore)", m.Component)
	case data != nil && (!data.M.DataArchive() || data.M.Component != comp || data.M.Scope != comp || !data.M.ScopeRoot):
		return m, fmt.Errorf("the data archive isn't %s's data", comp)
	}
	// deployment state is refused first when it isn't comp's (nothing on
	// disk yet: it only checks)
	dep, err := newDeploymentRestore(b.Reg.Root, comp, m, put)
	if err != nil {
		return m, err
	}
	defer dep.close()
	if dep != nil {
		dep.putRegs = b.restoreRegistrations
	}
	if m.Component != comp {
		return m, fmt.Errorf("the archive is of %q, not %q", m.Component, comp)
	}
	// Resource data belongs to the scope the component roots (writeBackup
	// includes it only then); an archive naming another scope is refused.
	withData := m.ScopeRoot && m.Scope == m.Component
	if m.Has("data") && !withData {
		return m, fmt.Errorf("the archive carries resource data of scope %q, which %q does not root", m.Scope, comp)
	}
	// Restored resource data is re-encrypted under the current vault, so this
	// needs the vault unsealed (plans/vault-data.md).
	if b.vaultSealed() {
		return m, fmt.Errorf("vault sealed — unseal before restoring encrypted resources")
	}
	dst := &restoreDst{b: b, comp: comp}
	defer dst.close()

	for {
		name, rd, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		if dep.takes(name) {
			if err := dep.entry(name, rd); err != nil {
				return m, err
			}
			continue
		}
		if err := dep.settle(); err != nil {
			return m, err
		}
		if err := dst.member(name, m.Scope, withData, br, rd); err != nil {
			return m, err
		}
	}
	if err := dep.settle(); err != nil { // an archive of nothing else
		return m, err
	}
	// A split archive's data, from its data archive: its data members only.
	for data != nil {
		name, rd, err := data.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("its data archive: %w", err)
		}
		if !strings.HasPrefix(name, backup.DataPrefix) {
			continue
		}
		if err := dst.member(name, m.Scope, withData, data, rd); err != nil {
			return m, err
		}
	}
	if err := dst.finish(); err != nil {
		return m, err
	}
	// Restore this component's cron jobs.
	for _, raw := range m.CronJobs {
		var j cronJob
		if json.Unmarshal(raw, &j) == nil && b.cron != nil {
			if b.cron.add(j) == nil {
				b.cron.persist()
			}
		}
	}
	// And its bus subscriptions (a subscription of another component is
	// never restored from this backup). Each delivery re-checks the grant.
	restored := false
	for _, raw := range m.BusSubs {
		var s busSub
		if json.Unmarshal(raw, &s) == nil && s.Component == m.Component && busSubNameRe.MatchString(s.Name) &&
			strings.HasPrefix(s.Path, "/") && b.bus.put(s) == nil {
			restored = true
		}
	}
	if restored {
		b.bus.persist()
	}
	return m, nil
}

// member writes one archive member where it belongs: the scope's resource
// data (withData: the tile roots the scope the archive names), the tile's
// source, or the staged terminal layer. Anything else is skipped.
func (d *restoreDst) member(name, scope string, withData bool, br *backup.Reader, rd io.Reader) error {
	var (
		tree *destTree
		rel  string
		err  error
	)
	switch {
	case name == backup.KVName:
		if !withData {
			return nil
		}
		body, _ := io.ReadAll(rd)
		return d.b.loadKV(scope, body)
	case strings.HasPrefix(name, backup.SQLitePrefix), strings.HasPrefix(name, backup.FSPrefix),
		strings.HasPrefix(name, backup.BlobPrefix):
		if !withData {
			return nil
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name,
			backup.SQLitePrefix), backup.FSPrefix), backup.BlobPrefix)
		res, sub, _ := strings.Cut(rest, "/")
		if rel = cleanRel(sub); rel == "" {
			return nil // the mount itself: nothing to write
		}
		if tree, err = d.resource(scope, res); err != nil {
			return err
		}
	case strings.HasPrefix(name, backup.SourcePrefix):
		if rel = cleanRel(strings.TrimPrefix(name, backup.SourcePrefix)); rel == "" {
			return nil
		}
		if tree, err = d.source(); err != nil {
			return err
		}
	case strings.HasPrefix(name, backup.TermPrefix):
		rel = cleanRel(strings.TrimPrefix(name, backup.TermPrefix))
		if top, _, _ := strings.Cut(rel, "/"); top == "" || top == "vm" {
			return nil // a VM terminal's disk is never in a backup; the layer keeps its own
		}
		if tree, err = d.term(); err != nil {
			return err
		}
	default:
		return nil
	}
	if err := tree.write(rel, br.Perm(), rd); err != nil {
		return fmt.Errorf("restore %s: %w", name, err)
	}
	return nil
}

// cleanRel is a tar entry's path below its prefix, cleaned and relative ("" for
// the tree's top itself): a hostile "../../x" clamps to "x".
func cleanRel(name string) string { return path.Clean("/" + name)[1:] }

// restoreDst holds the trees one restore writes into, each opened once.
type restoreDst struct {
	b    *Broker
	comp string
	src  *destTree
	res  map[string]*destTree
	// The terminal layer is rebuilt here, then swapped in (finish).
	staging string
	stage   *destTree
}

// source is the tile's dir, made if missing (a disaster-recovery restore) —
// reached from the workspace without symlinks either way.
func (d *restoreDst) source() (*destTree, error) {
	if d.src == nil {
		ws := d.b.Reg.Root
		if err := fsutil.MkdirAllIn(ws, d.comp, 0o755); err != nil {
			return nil, err
		}
		r, err := fsutil.OpenRootIn(ws, d.comp)
		if err != nil {
			return nil, err
		}
		d.src = newDestTree(r)
	}
	return d.src, nil
}

// resource is a file-backed resource's decrypted mount, mounted first so the
// restored data is re-encrypted under the current vault. The mount dir is
// xbind's (.xbin/resenc/…); what is inside it a sandbox wrote.
func (d *restoreDst) resource(scope, name string) (*destTree, error) {
	if r := d.res[name]; r != nil {
		return r, nil
	}
	if err := archivedScopeOK(scope); err != nil {
		return nil, err
	}
	if !registry.ValidResourceName(name) { // the archive names it (D118)
		return nil, fmt.Errorf("restore: backup entry names resource %q, which isn't a valid resource name", name)
	}
	if !d.b.Reg.HoldsScopeKey(scope) {
		// Another scope holds this data key (D118): its volume isn't ours to write.
		return nil, fmt.Errorf("scope %s doesn't hold its resource data key %q — its data isn't restored", scope, scopeDataKey(scope))
	}
	k, err := d.b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment)
	if err != nil {
		return nil, err
	}
	mdir, err := d.b.resenc.Ensure(k.FSLabel, k.DirKey, k.Name,
		d.b.resSingleTenant(scope, d.b.resType(scope, name)))
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(mdir)
	if err != nil {
		return nil, err
	}
	if d.res == nil {
		d.res = map[string]*destTree{}
	}
	d.res[name] = newDestTree(r)
	return d.res[name], nil
}

// term is the staging dir the terminal layer is rebuilt in: fresh, empty and
// xbind's, under .xbin/restore/ (never .xbin/term/, whose every dir is read
// as a layer).
func (d *restoreDst) term() (*destTree, error) {
	if d.stage == nil {
		dir := filepath.Join(d.b.Reg.Root, ".xbin", "restore")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		d.b.sweepRestoreLeftovers(dir)
		staging, err := os.MkdirTemp(dir, util.CompKey(d.comp)+"-")
		if err != nil {
			return nil, err
		}
		d.staging = staging
		r, err := os.OpenRoot(staging)
		if err != nil {
			return nil, err
		}
		d.stage = newDestTree(r)
	}
	return d.stage, nil
}

// sweepRestoreLeftovers drops what a restore that died mid-way (xbind was
// killed) left in .xbin/restore/: anything a day old — no restore runs that
// long. An old layer a swap moved aside is among them, so each goes through
// removeTree.
func (b *Broker) sweepRestoreLeftovers(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			if err := b.removeTree(filepath.Join(dir, e.Name())); err != nil {
				slog.Warn("restore: sweeping a leftover", "dir", filepath.Join(dir, e.Name()), "err", err)
			}
		}
	}
}

// removeTree removes a tree a sandbox wrote — a terminal layer, whose upper
// holds files other sub-uids own in range mode, which xbind can't unlink —
// in a confined run with the file capabilities (confine.RemoveAll; WP-9b,
// plans/tile-sandbox-runtime.md §8.3). dir must be one xbind made, in a dir
// no sandbox writes.
func (b *Broker) removeTree(dir string) error {
	if b.rmTree != nil {
		return b.rmTree(dir)
	}
	return confine.RemoveAll(context.Background(), dir)
}

// finish swaps a rebuilt terminal layer in (swapIn), then removes the old
// one — a tree the sandbox wrote, so in a confined run (removeTree), once the
// new layer is in use. A removal that fails leaves it in .xbin/restore, for
// the sweep a day later.
func (d *restoreDst) finish() error {
	if d.stage == nil {
		return nil
	}
	d.stage.r.Close()
	d.stage = nil
	old, err := d.swapIn()
	if err != nil {
		return err
	}
	if old != "" {
		if err := d.b.removeTree(old); err != nil {
			slog.Warn("restore: removing the old terminal layer", "dir", old, "err", err)
		}
	}
	return nil
}

// swapIn puts the staged layer in place: the sessions holding the old one are
// killed and the layer held (HoldTermEnv) so none mounts it meanwhile; the old
// layer is renamed aside — keeping a VM terminal's disk (vm/, never in a
// backup) — and the new one renamed into place. old is where the old layer
// went ("" when there was none).
func (d *restoreDst) swapIn() (old string, err error) {
	if b := d.b; b.HoldTermEnv != nil {
		release, err := b.HoldTermEnv(d.comp)
		if err != nil {
			return "", fmt.Errorf("restore the terminal layer: %w", err)
		}
		defer release()
	}
	if err := os.Chmod(d.staging, 0o755); err != nil { // a layer dir's mode (term.ensureLayerBase)
		return "", err
	}
	layer := d.b.termDir(d.comp)
	if err := os.MkdirAll(filepath.Dir(layer), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Lstat(layer); err == nil {
		old = d.staging + ".old"
		// Stamp it fresh first: a long-lived layer's mtime is old, and
		// another tile's restore sweeping .xbin/restore meanwhile must not
		// take it while its vm/ is still on the way to the new layer.
		now := time.Now()
		_ = os.Chtimes(layer, now, now)
		if err := os.Rename(layer, old); err != nil {
			return "", err
		}
		if err := os.Rename(filepath.Join(old, "vm"), filepath.Join(d.staging, "vm")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(old, layer)
			return "", err
		}
	}
	if err := os.Rename(d.staging, layer); err != nil {
		if old != "" {
			_ = os.Rename(filepath.Join(d.staging, "vm"), filepath.Join(old, "vm"))
			_ = os.Rename(old, layer)
		}
		return "", err
	}
	d.staging = ""
	return old, nil
}

// close releases the roots and drops an unfinished staging dir. That one holds
// only what this restore wrote as xbind (and, if a failed swap couldn't hand
// it back, the disk image xbind made for a VM terminal), so xbind removes it
// itself.
func (d *restoreDst) close() {
	if d.src != nil {
		d.src.r.Close()
	}
	for _, t := range d.res {
		t.r.Close()
	}
	if d.stage != nil {
		d.stage.r.Close()
	}
	if d.staging != "" {
		_ = os.RemoveAll(d.staging)
	}
}

// destTree is one tree a restore writes into, through an os.Root at its top.
type destTree struct {
	r    *os.Root
	made map[string]bool // directories this restore already made or checked
}

func newDestTree(r *os.Root) *destTree { return &destTree{r: r, made: map[string]bool{}} }

// write writes one restored file at rel (clean, relative, not "") beneath the
// tree's top, with perm — the bits it was archived with (backup.Reader.Perm),
// so an executable stays one. A restore overwrites wholesale, and nothing on
// the way is followed: a symlink where a directory belongs is replaced by the
// directory, one at the file's own path by the file (its target untouched),
// and os.Root keeps even a link swapped in mid-restore from leading out.
func (t *destTree) write(rel string, perm fs.FileMode, rd io.Reader) error {
	r := t.r
	if dir := path.Dir(rel); dir != "." {
		if err := t.mkdirAll(dir); err != nil {
			return err
		}
	}
	// Remove first even when the existing file is read-only — git objects
	// (.git/objects) are 0444 and can't be reopened for writing.
	_ = r.Remove(rel)
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, rd)
	if err == nil {
		err = f.Chmod(perm & fs.ModePerm) // exactly, whatever the umask; never setuid, setgid or sticky
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// mkdirAll makes dir's missing steps beneath the tree's top, replacing a
// symlink met on the way with a real directory. Each step is checked once
// per restore.
func (t *destTree) mkdirAll(dir string) error {
	r, cur := t.r, ""
	for _, seg := range strings.Split(dir, "/") {
		cur = path.Join(cur, seg)
		if t.made[cur] {
			continue
		}
		fi, err := r.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			t.made[cur] = true
			continue
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			if err := r.Remove(cur); err != nil {
				return err
			}
		case err == nil:
			return &fs.PathError{Op: "mkdir", Path: cur, Err: syscall.ENOTDIR}
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if err := r.Mkdir(cur, 0o755); err != nil {
			return err
		}
		t.made[cur] = true
	}
	return nil
}

func (b *Broker) loadKV(scope string, body []byte) error {
	if b.kv == nil {
		return nil
	}
	if err := archivedScopeOK(scope); err != nil {
		return err
	}
	if !b.Reg.HoldsScopeKey(scope) {
		// A scope at "workspace" would write the workspace-level buckets (D118).
		return fmt.Errorf("scope %s doesn't hold its resource data key %q — its data isn't restored", scope, scopeDataKey(scope))
	}
	var dump map[string]map[string]string
	if err := json.Unmarshal(body, &dump); err != nil {
		return err
	}
	for name := range dump {
		if !registry.ValidResourceName(name) { // "a/b" would land in another scope's bucket (D118)
			return fmt.Errorf("backup names kv resource %q, which isn't a valid resource name", name)
		}
	}
	keys := make(map[string]resKeys, len(dump))
	for name := range dump {
		k, err := b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment)
		if err != nil {
			return err
		}
		keys[name] = k
	}
	db, err := b.scopeKV(scope, util.MainDeployment, true) // main's: data/kv.db
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		for name, kvs := range dump {
			rk := keys[name]
			bk, err := tx.CreateBucketIfNotExists([]byte(rk.Bucket))
			if err != nil {
				return err
			}
			for k, v64 := range kvs {
				v, err := base64.StdEncoding.DecodeString(v64)
				if err != nil {
					return err
				}
				// Re-encode under the current vault (the tar held plaintext).
				stored, err := b.encodeKV(rk.KVLabel, v)
				if err != nil {
					return err
				}
				if err := bk.Put([]byte(k), stored); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// archivedScopeOK refuses resource data an archive files under the
// workspace scope: an archive carries data only for the scope its tile roots
// (writeBackup), and the workspace's resources are no tile's, so such an
// entry would write the workspace-level volumes or buckets.
func archivedScopeOK(scope string) error {
	if scope == "" {
		return fmt.Errorf("the archive files resource data under the workspace scope, which no tile's archive holds — its data isn't restored")
	}
	return nil
}
