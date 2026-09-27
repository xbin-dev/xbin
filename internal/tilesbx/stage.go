package tilesbx

// stage.go — what snapshots, restores and clones share
// (plans/tile-sandbox-runtime.md §3.9): a copy runs off the request as a
// job (?wait bounds how long a call waits for it) under a context xbind's
// shutdown cancels; it lands in a fresh staging dir, <state>/tmp/<rand>/
// (0700, xbind's), and is renamed into place whole — or, when it fails,
// put aside for the confined remover. An upper is copied by a confined cp,
// exactly (confine.CopyTree), a disk sparse (fsutil.CloneSparse); nothing a
// sandbox wrote is read as xbind.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/layers"
)

// copyJob is a copy running off the request: a snapshot, a restore, a
// clone. done is closed when it ended; err says how.
type copyJob struct {
	done chan struct{}
	err  error
}

func newJob() *copyJob { return &copyJob{done: make(chan struct{})} }

// waitJob waits at most wait (at least minWait) for j: whether it ended.
func waitJob(j *copyJob, wait time.Duration) bool {
	t := time.NewTimer(max(wait, minWait))
	defer t.Stop()
	select {
	case <-j.done:
		return true
	case <-t.C:
		return false
	}
}

// copyCtl is the context the copies run under: cancelAll ends those in
// flight (xbind is shutting down) and later ones run under a fresh one.
type copyCtl struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *copyCtl) get() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		c.ctx, c.cancel = context.WithCancel(context.Background())
	}
	return c.ctx
}

func (c *copyCtl) cancelAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ctx, c.cancel = nil, nil
}

// stage makes a fresh staging dir, <state>/tmp/<rand> (0700, xbind's).
func (m *Manager) stage(dir string) (string, error) {
	root := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(root, randSuffix())
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return "", err
	}
	return tmp, nil
}

// discard puts what a failed copy staged aside for the confined remover
// (left in tmp/ when that fails: the boot sweeps it).
func (m *Manager) discard(k Key, d *Def, tmp string) {
	trash, err := m.TrashDir(k)
	if err != nil {
		return
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return
	}
	to := filepath.Join(trash, d.UID+"."+randSuffix())
	if err := os.Rename(tmp, to); err != nil {
		slog.Warn("tile sandbox: putting a failed copy aside", "dir", tmp, "err", err)
		return
	}
	m.trash.put(to)
}

// copyLayer copies a state layer — an upper (namespace) or a disk (VM) —
// from src (a cur/ or a snapshot dir) into dst, a fresh staging dir, and
// stamps dst with st. An upper is copied confined, owners, modes, links,
// whiteouts and xattrs exactly (copyTree: an opaque directory's marker or
// an ownership override that can't be copied fails the copy); a disk is
// cloned sparse. Nothing in src is read as xbind; a src without its layer
// (a VM that never booted) copies nothing.
func (m *Manager) copyLayer(ctx context.Context, mode, src, dst string, st layers.Stamps) error {
	if mode == ModeVM {
		if err := os.Mkdir(filepath.Join(dst, "vm"), 0o700); err != nil {
			return err
		}
		disk := filepath.Join(src, "vm", "disk.img")
		if _, err := os.Lstat(disk); !errors.Is(err, fs.ErrNotExist) {
			if _, err := fsutil.CloneSparse(ctx, disk, filepath.Join(dst, "vm", "disk.img")); err != nil {
				return fmt.Errorf("copying its disk: %w", err)
			}
		}
	} else {
		upper := filepath.Join(dst, "upper")
		if err := os.Mkdir(upper, 0o755); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(dst, "work"), 0o700); err != nil {
			return err
		}
		if fi, err := os.Lstat(filepath.Join(src, "upper")); err == nil && fi.IsDir() {
			if err := m.copyTree(ctx, filepath.Join(src, "upper"), upper); err != nil {
				return fmt.Errorf("copying its upper: %w", err)
			}
		}
	}
	return layers.Stamp(dst, st)
}

// stagedBytes is what a staged copy takes: a confined du of an upper, a
// disk's allocated blocks.
func (m *Manager) stagedBytes(ctx context.Context, mode, dir string) (int64, error) {
	if mode == ModeVM {
		return allocBytes(filepath.Join(dir, "vm", "disk.img")), nil
	}
	return m.usage.du(ctx, dir)
}

// allocBytes is a file's allocated bytes, lstat'ed (0: none, or not a
// regular file).
func allocBytes(p string) int64 {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return 0
}

// diskGiBOf is a disk image's size in GiB, rounded up (0: none).
func diskGiBOf(p string) int {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return int((fi.Size() + 1<<30 - 1) >> 30)
}
