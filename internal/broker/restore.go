package broker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Restore: the other half of backup.go — a component's archive unpacked back
// into place (plans/lifecycle.md).

// restore unpacks a backup tar of comp, reconstructing the component at the
// path its manifest records — which must be comp: the archive came from a
// tile (the archiver), so nothing it names is trusted as a host path.
//
// The restore runs as xbind and writes into trees sandboxes write (WP-9,
// plans/tile-sandbox-runtime.md): the tile's source, its resource mounts, its
// terminal layer. Every write goes through an os.Root at the tree's top, so
// no symlink — planted before the restore or swapped in during it — carries
// a write out of the tree. The tile's dir is reached from the workspace
// without symlinks; a symlink met on the way to a restored file is replaced,
// never followed. The terminal layer is rebuilt in a fresh staging dir and
// swapped in whole once the sessions holding it are gone (restoreDst.finish).
func (b *Broker) restore(r io.Reader, comp string) (backup.Manifest, error) {
	br, err := backup.NewReader(r)
	if err != nil {
		return backup.Manifest{}, err
	}
	m := br.M
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
		var (
			tree *destTree
			rel  string
		)
		switch {
		case name == backup.KVName:
			if !withData {
				continue
			}
			body, _ := io.ReadAll(rd)
			if err := b.loadKV(m.Scope, body); err != nil {
				return m, err
			}
			continue
		case strings.HasPrefix(name, backup.SQLitePrefix), strings.HasPrefix(name, backup.FSPrefix),
			strings.HasPrefix(name, backup.BlobPrefix):
			if !withData {
				continue
			}
			rest := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name,
				backup.SQLitePrefix), backup.FSPrefix), backup.BlobPrefix)
			res, sub, _ := strings.Cut(rest, "/")
			if rel = cleanRel(sub); rel == "" {
				continue // the mount itself: nothing to write
			}
			if tree, err = dst.resource(m.Scope, res); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.SourcePrefix):
			if rel = cleanRel(strings.TrimPrefix(name, backup.SourcePrefix)); rel == "" {
				continue
			}
			if tree, err = dst.source(); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.TermPrefix):
			rel = cleanRel(strings.TrimPrefix(name, backup.TermPrefix))
			if top, _, _ := strings.Cut(rel, "/"); top == "" || top == "vm" {
				continue // a VM terminal's disk is never in a backup; the layer keeps its own
			}
			if tree, err = dst.term(); err != nil {
				return m, err
			}
		default:
			continue
		}
		if err := tree.write(rel, rd); err != nil {
			return m, fmt.Errorf("restore %s: %w", name, err)
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
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, fmt.Errorf("restore: bad resource name %q", name)
	}
	scopeKey := util.ScopeKey(scope)
	mdir, err := d.b.resenc.Ensure(resLabel(scopeKey, name), scopeKey, name,
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
		sweepRestoreLeftovers(dir)
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
// killed) left in .xbin/restore/: anything a day old — no restore runs that long.
func sweepRestoreLeftovers(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// finish swaps a rebuilt terminal layer in: the sessions holding the old one
// are killed and the layer held (HoldTermEnv) so none mounts it meanwhile; the
// old layer is renamed aside — keeping a VM terminal's disk (vm/, never in a
// backup) — the new one renamed into place, and the old one removed.
func (d *restoreDst) finish() error {
	if d.stage == nil {
		return nil
	}
	d.stage.r.Close()
	d.stage = nil
	if b := d.b; b.HoldTermEnv != nil {
		release, err := b.HoldTermEnv(d.comp)
		if err != nil {
			return fmt.Errorf("restore the terminal layer: %w", err)
		}
		defer release()
	}
	if err := os.Chmod(d.staging, 0o755); err != nil { // a layer dir's mode (term.ensureLayerBase)
		return err
	}
	layer := d.b.termDir(d.comp)
	if err := os.MkdirAll(filepath.Dir(layer), 0o755); err != nil {
		return err
	}
	old := ""
	if _, err := os.Lstat(layer); err == nil {
		old = d.staging + ".old"
		// Stamp it fresh first: a long-lived layer's mtime is old, and
		// another tile's restore sweeping .xbin/restore meanwhile must not
		// take it while its vm/ is still on the way to the new layer.
		now := time.Now()
		_ = os.Chtimes(layer, now, now)
		if err := os.Rename(layer, old); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(old, "vm"), filepath.Join(d.staging, "vm")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(old, layer)
			return err
		}
	}
	if err := os.Rename(d.staging, layer); err != nil {
		if old != "" {
			_ = os.Rename(filepath.Join(d.staging, "vm"), filepath.Join(old, "vm"))
			_ = os.Rename(old, layer)
		}
		return err
	}
	d.staging = ""
	if old != "" {
		// os.RemoveAll never follows a symlink in the tree it removes.
		_ = os.RemoveAll(old)
	}
	return nil
}

// close releases the roots and drops an unfinished staging dir.
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
// tree's top. A restore overwrites wholesale, and nothing on the way is
// followed: a symlink where a directory belongs is replaced by the
// directory, one at the file's own path by the file (its target untouched),
// and os.Root keeps even a link swapped in mid-restore from leading out.
func (t *destTree) write(rel string, rd io.Reader) error {
	r := t.r
	if dir := path.Dir(rel); dir != "." {
		if err := t.mkdirAll(dir); err != nil {
			return err
		}
	}
	// Remove first even when the existing file is read-only — git objects
	// (.git/objects) are 0444 and can't be reopened for writing.
	_ = r.Remove(rel)
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, rd)
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
	var dump map[string]map[string]string
	if err := json.Unmarshal(body, &dump); err != nil {
		return err
	}
	return b.kv.db.Update(func(tx *bolt.Tx) error {
		for name, kvs := range dump {
			bucket := "res:" + scope + "/" + name
			bk, err := tx.CreateBucketIfNotExists([]byte(bucket))
			if err != nil {
				return err
			}
			for k, v64 := range kvs {
				v, err := base64.StdEncoding.DecodeString(v64)
				if err != nil {
					return err
				}
				// Re-encode under the current vault (the tar held plaintext).
				stored, err := b.encodeKV(bucket, v)
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
