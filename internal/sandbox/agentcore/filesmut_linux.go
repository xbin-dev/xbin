//go:build linux

package agentcore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// The file operations that change the tree: write, mkdir, remove, move.

// write replaces a file atomically: a temporary file beside it, the data
// (all of it, up to the terminator), fsync, then a rename. A connection that
// ends before the terminator commits nothing.
func (f *files) write(op *proto.FileOp, rel string, conn *proto.Conn) proto.FileResult {
	if op.Mode > 0o7777 {
		return refuse(proto.RefuseInvalid, "mode is permission bits")
	}
	if op.Mkdirs && rel != "." {
		if err := f.mkdirAll(path.Dir(rel), 0o755, op.Owner); err != nil {
			return fail(path.Dir(op.Path), err)
		}
	}
	dir, name, err := f.parentRW(rel)
	if err != nil {
		return fail(op.Path, err)
	}
	defer unix.Close(dir)
	var cur unix.Stat_t
	curErr := unix.Fstatat(dir, name, &cur, unix.AT_SYMLINK_NOFOLLOW)
	if curErr == nil && cur.Mode&unix.S_IFMT == unix.S_IFDIR {
		return refuse(proto.RefuseInvalid, op.Path+" is a directory")
	}
	if r, ok := precondition(op, curErr, &cur); !ok { // early: before the body
		return r
	}
	mode := op.Mode
	replacing := curErr == nil && cur.Mode&unix.S_IFMT == unix.S_IFREG
	if mode == 0 {
		mode = 0o644
		if replacing {
			mode = cur.Mode & 0o7777 // a replaced file keeps its mode
		}
	}
	tmp := tempName(name)
	tfd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fail(op.Path, err)
	}
	tf := os.NewFile(uintptr(tfd), tmp)
	committed := false
	defer func() {
		tf.Close()
		if !committed {
			_ = unix.Unlinkat(dir, tmp, 0)
		}
	}()
	src := io.Reader(proto.NewFrameReader(conn.Reader()))
	if op.Max > 0 {
		src = &capReader{r: src, left: op.Max}
	}
	if _, err := io.CopyBuffer(tf, src, make([]byte, 256<<10)); err != nil {
		if err == errTooLarge {
			return refuse(proto.RefuseTooLarge, fmt.Sprintf("%s: over %d bytes", op.Path, op.Max))
		}
		return proto.FileResult{Error: op.Path + ": the data: " + err.Error()} // (unexpected EOF: nothing committed)
	}
	_ = unix.Fchmod(tfd, mode) // (no umask)
	switch {
	case replacing:
		_ = unix.Fchown(tfd, int(cur.Uid), int(cur.Gid)) // and its owner
	case op.Owner != nil:
		_ = unix.Fchown(tfd, int(op.Owner[0]), int(op.Owner[1]))
	}
	if err := tf.Sync(); err != nil {
		return fail(op.Path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(tfd, &st); err != nil {
		return fail(op.Path, err)
	}
	f.c.wmu.Lock()
	curErr = unix.Fstatat(dir, name, &cur, unix.AT_SYMLINK_NOFOLLOW)
	r, ok := precondition(op, curErr, &cur)
	if ok {
		if op.Create {
			err = renameNoReplace(dir, tmp, dir, name)
		} else {
			err = unix.Renameat(dir, tmp, dir, name)
		}
	}
	f.c.wmu.Unlock()
	if !ok {
		return r
	}
	if err == unix.EEXIST {
		return f.preconditionFailed(op.Path, dir, name)
	}
	if err != nil {
		return fail(op.Path, err)
	}
	committed = true
	_ = unix.Fsync(dir)
	return proto.FileResult{OK: true, Stat: toStat(path.Clean(op.Path), &st, "")}
}

// precondition checks a write's IfMatch / Create against the current entry.
func precondition(op *proto.FileOp, curErr error, cur *unix.Stat_t) (proto.FileResult, bool) {
	want := strings.Trim(strings.TrimPrefix(op.IfMatch, "W/"), `"`) // as an ETag header quotes it
	switch {
	case op.Create && curErr == nil,
		want != "" && (curErr != nil || etag(cur) != want):
		r := refuse(proto.RefusePrecondition, op.Path+" changed")
		if op.Create {
			r.Error = op.Path + " exists"
		}
		if curErr == nil {
			r.Stat = toStat(path.Clean(op.Path), cur, "")
		}
		return r, false
	}
	return proto.FileResult{}, true
}

func (f *files) preconditionFailed(p string, dir int, name string) proto.FileResult {
	r := refuse(proto.RefusePrecondition, p+" exists")
	var cur unix.Stat_t
	if unix.Fstatat(dir, name, &cur, unix.AT_SYMLINK_NOFOLLOW) == nil {
		r.Stat = toStat(path.Clean(p), &cur, "")
	}
	return r
}

// parentRW is parent, opened for reading (fsync wants a real descriptor).
func (f *files) parentRW(rel string) (int, string, error) {
	if rel == "." {
		return -1, "", unix.EISDIR
	}
	fd, err := f.open(path.Dir(rel), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	return fd, path.Base(rel), err
}

// renameNoReplace renames unless the destination exists (EEXIST). Where
// the filesystem can't say so atomically, it checks first.
func renameNoReplace(fromDir int, from string, toDir int, to string) error {
	err := unix.Renameat2(fromDir, from, toDir, to, unix.RENAME_NOREPLACE)
	if err != unix.EINVAL && err != unix.ENOSYS {
		return err
	}
	var st unix.Stat_t
	if unix.Fstatat(toDir, to, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return unix.EEXIST
	}
	return unix.Renameat(fromDir, from, toDir, to)
}

func tempName(name string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	if len(name) > 200 {
		name = name[:200]
	}
	return "." + name + ".xbin-" + hex.EncodeToString(b[:])
}

// capReader fails with errTooLarge past left bytes.
type capReader struct {
	r    io.Reader
	left int64
}

var errTooLarge = errors.New("too large")

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errTooLarge
	}
	return n, err
}

func (f *files) mkdir(op *proto.FileOp, rel string) proto.FileResult {
	mode := op.Mode
	if mode == 0 {
		mode = 0o755
	}
	if mode > 0o7777 {
		return refuse(proto.RefuseInvalid, "mode is permission bits")
	}
	if op.Parents {
		if err := f.mkdirAll(rel, mode, op.Owner); err != nil {
			return fail(op.Path, err)
		}
		return proto.FileResult{OK: true}
	}
	dir, name, err := f.parent(rel)
	if err != nil {
		if rel == "." {
			return refuse(proto.RefuseInvalid, "/ exists")
		}
		return fail(op.Path, err)
	}
	defer unix.Close(dir)
	if err := f.mkdirAt(dir, name, mode, op.Owner); err != nil {
		return fail(op.Path, err)
	}
	return proto.FileResult{OK: true}
}

func (f *files) mkdirAt(dir int, name string, mode uint32, owner *[2]uint32) error {
	if err := unix.Mkdirat(dir, name, mode); err != nil {
		return err
	}
	_ = unix.Fchmodat(dir, name, mode, 0) // past the umask
	if owner != nil {
		_ = unix.Fchownat(dir, name, int(owner[0]), int(owner[1]), unix.AT_SYMLINK_NOFOLLOW)
	}
	return nil
}

// mkdirAll creates rel and its missing parents inside the root (each
// existing component may be a symlink, resolved inside it).
func (f *files) mkdirAll(rel string, mode uint32, owner *[2]uint32) error {
	if rel == "." {
		return nil
	}
	cur := "."
	for _, part := range strings.Split(rel, "/") {
		next := path.Join(cur, part)
		fd, err := f.open(next, unix.O_PATH|unix.O_DIRECTORY, 0)
		if err == nil {
			unix.Close(fd)
			cur = next
			continue
		}
		if err != unix.ENOENT {
			return err
		}
		dir, err := f.open(cur, unix.O_PATH|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		err = f.mkdirAt(dir, part, mode, owner)
		unix.Close(dir)
		if err != nil && err != unix.EEXIST {
			return err
		}
		cur = next
	}
	return nil
}

func (f *files) remove(op *proto.FileOp, rel string) proto.FileResult {
	dir, name, err := f.parent(rel)
	if err != nil {
		if rel == "." {
			return refuse(proto.RefuseInvalid, "won't remove the sandbox's root")
		}
		return fail(op.Path, err)
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(op.Path, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		err = unix.Unlinkat(dir, name, 0)
	case op.Recursive:
		err = removeAll(dir, name, 0)
	default:
		if err = unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err == unix.ENOTEMPTY || err == unix.EEXIST {
			return refuse(proto.RefuseInvalid, op.Path+" is a directory that isn't empty (remove it recursively)")
		}
	}
	if err != nil {
		return fail(op.Path, err)
	}
	return proto.FileResult{OK: true}
}

// removeAll removes name under dir and everything beneath it, never
// following a symlink (a symlink is removed, not what it points at).
func removeAll(dir int, name string, depth int) error {
	err := unix.Unlinkat(dir, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if err != unix.EISDIR && err != unix.EPERM {
		return err
	}
	if depth > 1024 {
		return errors.New("too deep")
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	d := os.NewFile(uintptr(fd), name)
	defer d.Close()
	// passes until one finds nothing: removing entries moves readdir's offsets
	for removed := false; ; {
		names, err := d.Readdirnames(1024)
		if len(names) == 0 {
			if err != io.EOF {
				return err
			}
			if !removed {
				break
			}
			removed = false
			if _, err := d.Seek(0, io.SeekStart); err != nil {
				return err
			}
			continue
		}
		for _, n := range names {
			if err := removeAll(fd, n, depth+1); err != nil {
				return err
			}
		}
		removed = true
	}
	if err := unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
		return err
	}
	return nil
}

func (f *files) move(op *proto.FileOp, rel string) proto.FileResult {
	toRel, err := relPath(op.To)
	if err != nil {
		return refuse(proto.RefuseInvalid, "to: "+err.Error())
	}
	if rel == "." || toRel == "." {
		return refuse(proto.RefuseInvalid, "won't move the sandbox's root")
	}
	fromDir, from, err := f.parent(rel)
	if err != nil {
		return fail(op.Path, err)
	}
	defer unix.Close(fromDir)
	var st unix.Stat_t
	if err := unix.Fstatat(fromDir, from, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(op.Path, err)
	}
	toDir, to, err := f.parent(toRel)
	if err != nil {
		return fail(op.To, err)
	}
	defer unix.Close(toDir)
	f.c.wmu.Lock()
	if op.Overwrite {
		err = unix.Renameat(fromDir, from, toDir, to)
	} else {
		err = renameNoReplace(fromDir, from, toDir, to)
	}
	f.c.wmu.Unlock()
	switch {
	case err == unix.EEXIST && !op.Overwrite:
		return f.preconditionFailed(op.To, toDir, to)
	case err == unix.EXDEV:
		return refuse(proto.RefuseInvalid, "can't move across filesystems ("+op.Path+" → "+op.To+")")
	case err != nil:
		return fail(op.Path+" → "+op.To, err)
	}
	return proto.FileResult{OK: true}
}
