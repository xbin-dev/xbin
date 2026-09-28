package confine

// tree.go — xbind's own measuring, removing and copying of trees sandboxes
// wrote: a tile sandbox's upper, a terminal layer (D78;
// plans/tile-sandbox-runtime.md §8.3). xbind never walks one itself — a
// planted symlink, a file of another sub-uid, a mode that locks xbind out —
// so each helper runs one fixed coreutils tool on it in a throwaway sandbox
// with the file capabilities (Cmd.FSCaps), which sees only the dirs named.
//
// The paths are the caller's and must be absolute and clean, and every
// component one xbind created and no sandbox can write: a state dir, an
// upper itself (a sandbox writes inside its upper, never the upper's own
// entry). The sandbox's bind would follow a link there.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// treeTimeout bounds one tool run over a tree; callers bound it further
// with their context.
const treeTimeout = 30 * time.Minute

// DiskUsage is the space the tree at dir takes on disk, in bytes: allocated
// blocks, a hard-linked file once, one filesystem (`du -skx`). A missing dir
// is 0.
func DiskUsage(ctx context.Context, dir string) (int64, error) {
	if err := treePath(dir); err != nil {
		return 0, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	res, err := Run(ctx, Cmd{Argv: []string{"du", "-skx", "--", dir}, Dir: dir, ReadOnlyDir: true,
		Env: []string{"LC_ALL=C"}, FSCaps: true, Timeout: treeTimeout, MaxOutput: 1 << 20})
	if err != nil {
		return 0, fmt.Errorf("du %s: %w", dir, err)
	}
	// "<KiB>\t<dir>\n": the number is du's; the path is ours
	f := strings.Fields(string(res.Stdout))
	if len(f) == 0 {
		return 0, fmt.Errorf("du %s: no output", dir)
	}
	kib, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || kib < 0 {
		return 0, fmt.Errorf("du %s: unexpected output %q", dir, f[0])
	}
	return kib << 10, nil
}

// RemoveAll removes dir and everything in it, as os.RemoveAll would: the
// contents by a confined `find -delete` (it never follows a link), then the
// empty dir by xbind. A missing dir is nil; a file or link at dir is just
// unlinked.
func RemoveAll(ctx context.Context, dir string) error {
	if err := treePath(dir); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return os.Remove(dir)
	}
	if _, err := Run(ctx, Cmd{Argv: []string{"find", dir, "-xdev", "-mindepth", "1", "-delete"}, Dir: dir,
		Env: []string{"LC_ALL=C"}, FSCaps: true, Timeout: treeTimeout, MaxOutput: 1 << 20}); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// CopyTree copies everything in the dir src into the dir dst — one xbind
// created, empty — keeping owners, modes, times, links, special files
// (whiteouts, FIFOs) and xattrs, sharing extents where the filesystem can:
// `cp -a --preserve=xattr --reflink=auto` (GNU cp: the rootfs's when
// confined, the host's when direct). Naming xattr makes one that can't be
// copied an error, where plain -a drops it silently: both overlay flavours
// keep opaque-directory markers and ownership overrides in user.* xattrs
// (fuse-overlayfs's user.fuseoverlayfs.*, a userxattr kernel overlay's
// user.overlay.*), and a copied upper without them shows the base's old
// entries in a directory that was deleted and made again, or files with the
// wrong owner (plans/tile-sandbox-runtime.md §3.9). src is bound read-only;
// the two may not nest.
func CopyTree(ctx context.Context, src, dst string) error {
	for _, p := range []string{src, dst} {
		if err := treePath(p); err != nil {
			return err
		}
		if fi, err := os.Lstat(p); err != nil {
			return err
		} else if !fi.IsDir() {
			return fmt.Errorf("copy: %s is not a directory", p)
		}
	}
	if within(src, dst) || within(dst, src) {
		return fmt.Errorf("copy: %s and %s nest", src, dst)
	}
	argv := []string{"cp", "-a", "--preserve=xattr", "--reflink=auto", "--", src + "/.", dst + "/"}
	if _, err := Run(ctx, Cmd{Argv: argv, Dir: dst, Binds: []sandbox.Bind{RO(src)},
		Env: []string{"LC_ALL=C"}, FSCaps: true, Timeout: treeTimeout, MaxOutput: 1 << 20}); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

// treePath: absolute, clean, and never the root itself.
func treePath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return fmt.Errorf("confine: %q is not an absolute, clean path below /", p)
	}
	return nil
}

// within reports whether p is base or below it.
func within(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+"/")
}
