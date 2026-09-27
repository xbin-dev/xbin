package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrEscapes is returned when a path resolves outside what the opener
// allows: out of its directory, through a symlink where none may be, or
// (OpenResolved) to a place the caller's filter refuses.
var ErrEscapes = errors.New("path escapes its directory")

// ErrNotRegular is returned for anything that is neither a regular file nor
// a directory — a FIFO, a socket, a device. Such a file is never handed out
// for reading: a FIFO would block the reader forever (a tile writer's
// `mkfifo x.js` must not hang xbind), a device may act on open.
var ErrNotRegular = errors.New("not a regular file or directory")

// OpenBeneath opens dir/rel for reading such that no step of rel — and no
// symlink met on the way, whatever its target — resolves outside dir: an
// in-tree symlink (a tile's own assets/ → dist/) still works, one that
// leaves the tree (→ /etc, → ../../.xbin/secret, → another tile) fails
// with ErrEscapes (or the platform's equivalent error). dir itself may be
// reached through symlinks; it is the trust boundary, not a path under
// suspicion. On Linux it is one openat2(RESOLVE_BENEATH) — no check-then-
// open race; elsewhere a resolve-and-compare fallback.
//
// Only regular files and directories are returned (ErrNotRegular
// otherwise), and the open itself never blocks (O_NONBLOCK, cleared again on
// what is handed back).
//
// Files written by sandboxes (tiles, homes) are untrusted: daemon code that
// reads them on a caller's behalf opens them through here, never os.Open.
func OpenBeneath(dir, rel string) (*os.File, error) { return openIn(dir, "", rel) }

// OpenIn is OpenBeneath(root/sub, rel) for a sub-directory that is itself
// untrusted: root is the trust boundary (it may be reached through
// symlinks), sub must be reached from root WITHOUT any symlink (else
// ErrEscapes), and rel is opened beneath root/sub. The /c/ plane opens a
// tile's files with root = the workspace and sub = the owning component:
// the registry names components from a walk that never follows symlinks,
// but it can be stale — a registered nested component's directory lives in
// its parent tile's writable tree, and swapped for a symlink (../../.xbin)
// it would otherwise make the "tile" whatever the link points at.
func OpenIn(root, sub, rel string) (*os.File, error) { return openIn(root, sub, rel) }

// OpenResolved opens root/rel following symlinks anywhere (the legacy /c/
// plane's contract: a symlink between tiles works), but only when the fully
// resolved path stays inside root and allow(its root-relative, slash-
// separated form) holds — and it opens exactly that resolved path: on Linux
// with RESOLVE_NO_SYMLINKS beneath root, so a symlink swapped in between
// the resolution and the open fails the open (ErrEscapes) instead of
// winning the race. Returns the file and the resolved relative path.
// Anything but a regular file or a directory: ErrNotRegular.
func OpenResolved(root, rel string, allow func(resolved string) bool) (*os.File, string, error) {
	return openResolved(root, rel, allow)
}

// MkdirAllIn creates the missing directories of root/sub, never through a
// symlink: every step of sub is made (or found) as a real directory beneath
// root, and a symlink or a file on the way fails the call (ErrEscapes, or
// ENOTDIR) instead of being followed. root is the trust boundary, as for
// OpenIn; sub must be a local path (filepath.IsLocal), and "" or "." is root
// itself. A restore makes a tile's directory with it: the tile may sit in
// another tile's writable tree, which could plant a link on the way.
func MkdirAllIn(root, sub string, perm os.FileMode) error {
	if sub == "" || sub == "." {
		return nil
	}
	if !filepath.IsLocal(sub) {
		return &os.PathError{Op: "mkdir", Path: filepath.Join(root, sub), Err: ErrEscapes}
	}
	return mkdirAllIn(root, filepath.Clean(sub), perm)
}

// OpenRootIn is an os.Root at root/sub, where sub is reached from root
// without any symlink (as for OpenIn): what the Root then opens stays inside
// that directory whatever is swapped in later, and the directory itself is
// the one the no-symlink walk found — not wherever a link planted on the way
// points. Writes into a tree that a sandbox can write go through here.
func OpenRootIn(root, sub string) (*os.Root, error) {
	d, err := OpenIn(root, sub, "")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	name := filepath.Join(root, filepath.FromSlash(sub))
	di, err := d.Stat()
	if err != nil {
		return nil, err
	}
	if !di.IsDir() {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ENOTDIR}
	}
	r, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	// The Root re-resolved the path; it must have landed on the very
	// directory the no-symlink open found (a link swapped in between would not).
	if ri, err := r.Stat("."); err != nil || !os.SameFile(di, ri) {
		r.Close()
		return nil, &os.PathError{Op: "open", Path: name, Err: ErrEscapes}
	}
	return r, nil
}
