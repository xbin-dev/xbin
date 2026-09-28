//go:build linux

package agentcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// File operations (plans/tile-sandbox-runtime.md §2.2–2.3) run inside the
// sandbox, as its root. Every path resolves with openat2(RESOLVE_IN_ROOT)
// beneath Options.Root — "/" in production — so a symlink leads only to the
// sandbox's own files, and the kernel enforces its read-only mounts. Device
// nodes, FIFOs and sockets are never opened.

// fileOp serves one "file" connection: one operation, then it closes.
func (c *Core) fileOp(op *proto.FileOp, conn *proto.Conn) {
	defer conn.Close()
	reply := func(r proto.FileResult) { _ = conn.Send(r) }
	if op == nil {
		reply(refuse(proto.RefuseInvalid, "no file operation"))
		return
	}
	if !c.isConfigured() {
		reply(proto.FileResult{Error: "the sandbox isn't configured yet"})
		return
	}
	root, err := unix.Open(c.o.Root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		reply(proto.FileResult{Error: "the sandbox root: " + err.Error()})
		return
	}
	defer unix.Close(root)
	fs := &files{c: c, root: root}
	rel, err := relPath(op.Path)
	if err != nil {
		reply(refuse(proto.RefuseInvalid, err.Error()))
		return
	}
	switch op.Op {
	case "stat":
		reply(fs.stat(op.Path, rel))
	case "read":
		fs.read(op, rel, conn)
	case "write":
		reply(fs.write(op, rel, conn))
	case "list":
		reply(fs.list(op, rel))
	case "mkdir":
		reply(fs.mkdir(op, rel))
	case "remove":
		reply(fs.remove(op, rel))
	case "move":
		reply(fs.move(op, rel))
	case "tar-get":
		fs.tarGet(op, rel, conn) // tar_linux.go
	case "tar-put":
		reply(fs.tarPut(op, rel, conn))
	default:
		reply(refuse(proto.RefuseInvalid, "unknown file operation "+op.Op))
	}
}

// files is one operation's view of the sandbox's tree.
type files struct {
	c    *Core
	root int // O_PATH of Options.Root
}

func refuse(refusal, msg string) proto.FileResult {
	return proto.FileResult{Refusal: refusal, Error: msg}
}

// fail turns a syscall error on p into a result: a refusal when it is the
// caller's (no such path, not a directory, read-only, …), else an error.
func fail(p string, err error) proto.FileResult {
	var errno unix.Errno
	errors.As(err, &errno)
	msg := p + ": " + err.Error()
	switch errno {
	case unix.ENOENT, unix.ENOTDIR:
		return refuse(proto.RefuseNotFound, msg)
	case unix.EEXIST, unix.EISDIR, unix.ENOTEMPTY, unix.EINVAL, unix.ELOOP, unix.ENAMETOOLONG,
		unix.EROFS, unix.EACCES, unix.EPERM, unix.EXDEV, unix.EBUSY, unix.ETXTBSY, unix.EMLINK:
		return refuse(proto.RefuseInvalid, msg)
	}
	return proto.FileResult{Error: msg}
}

// relPath turns an absolute sandbox path into one relative to the root
// ("." for the root itself).
func relPath(p string) (string, error) {
	switch {
	case !strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("%q is not an absolute path", p)
	case strings.IndexByte(p, 0) >= 0:
		return "", errors.New("a path with a NUL byte")
	case len(p) > unix.PathMax:
		return "", errors.New("a path over PATH_MAX")
	}
	if c := path.Clean(p); c != "/" {
		return c[1:], nil
	}
	return ".", nil
}

// open resolves rel inside the root (a final symlink is followed, inside it).
func (f *files) open(rel string, flags int, mode uint32) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS}
	if flags&unix.O_CREAT != 0 {
		how.Mode = uint64(mode)
	}
	for tries := 0; ; tries++ {
		fd, err := unix.Openat2(f.root, rel, &how)
		// EAGAIN: a rename or mount raced the in-root walk
		if (err == unix.EINTR || err == unix.EAGAIN) && tries < 16 {
			continue
		}
		if err == unix.ENOSYS && f.c.o.Root == "/" {
			// before Linux 5.6: at "/" of the sandbox a plain lookup is the
			// same walk (the agent's root is the sandbox's)
			return unix.Openat(unix.AT_FDCWD, "/"+rel, flags|unix.O_CLOEXEC, mode)
		}
		return fd, err
	}
}

// parent opens the directory holding rel's final component (O_PATH, which
// every *at call takes) and names that component. The root has none.
func (f *files) parent(rel string) (int, string, error) {
	if rel == "." {
		return -1, "", errors.New("the sandbox root itself")
	}
	fd, err := f.open(path.Dir(rel), unix.O_PATH|unix.O_DIRECTORY, 0)
	return fd, path.Base(rel), err
}

// lstat stats rel without following a final symlink.
func (f *files) lstat(rel string) (unix.Stat_t, string, error) {
	var st unix.Stat_t
	if rel == "." {
		return st, "", unix.Fstat(f.root, &st)
	}
	dir, name, err := f.parent(rel)
	if err != nil {
		return st, "", err
	}
	defer unix.Close(dir)
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return st, "", err
	}
	target := ""
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		target, _ = readlinkAt(dir, name)
	}
	return st, target, nil
}

func readlinkAt(dir int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dir, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func fileType(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return "file"
	case unix.S_IFDIR:
		return "dir"
	case unix.S_IFLNK:
		return "symlink"
	}
	return "other"
}

// etag is "<ino>-<size>-<mtime ns>" in hex (§2.2).
func etag(st *unix.Stat_t) string {
	return fmt.Sprintf("%x-%x-%x", st.Ino, st.Size, st.Mtim.Nano())
}

func toStat(p string, st *unix.Stat_t, target string) *proto.FileStat {
	return &proto.FileStat{
		Path: p, Name: path.Base(p), Type: fileType(st.Mode), Target: target,
		Size: st.Size, Mode: st.Mode & 0o7777, MTimeMs: st.Mtim.Nano() / 1e6, ETag: etag(st),
	}
}

func (f *files) stat(p, rel string) proto.FileResult {
	st, target, err := f.lstat(rel)
	if err != nil {
		return fail(p, err)
	}
	return proto.FileResult{OK: true, Stat: toStat(path.Clean(p), &st, target)}
}

// openRegular opens rel for reading, following a final symlink inside the
// root, and refuses anything but a regular file — before opening it: an
// O_PATH descriptor is checked, then reopened.
func (f *files) openRegular(rel string) (*os.File, *unix.Stat_t, error) {
	pfd, err := f.open(rel, unix.O_PATH, 0)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return nil, nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		return nil, nil, unix.EISDIR
	default:
		return nil, nil, errNotRegular
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", pfd), unix.O_RDONLY|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil { // no /proc: by the path again, and the same file or nothing
		if fd, err = f.open(rel, unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0); err != nil {
			return nil, nil, err
		}
		var again unix.Stat_t
		if err := unix.Fstat(fd, &again); err != nil || again.Ino != st.Ino || again.Dev != st.Dev {
			unix.Close(fd)
			return nil, nil, errNotRegular
		}
	}
	return os.NewFile(uintptr(fd), rel), &st, nil
}

var errNotRegular = errors.New("not a regular file (devices, FIFOs and sockets are refused)")

// read: the stat line, the range in frames, the terminator, the last line.
func (f *files) read(op *proto.FileOp, rel string, conn *proto.Conn) {
	file, st, err := f.openRegular(rel)
	if err != nil {
		r := fail(op.Path, err)
		if err == errNotRegular {
			r = refuse(proto.RefuseInvalid, op.Path+": "+err.Error())
		}
		_ = conn.Send(r)
		return
	}
	defer file.Close()
	if op.Offset < 0 || op.Length < 0 {
		_ = conn.Send(refuse(proto.RefuseInvalid, "offset and length are byte counts"))
		return
	}
	n := max(st.Size-op.Offset, 0)
	if op.Length > 0 {
		n = min(n, op.Length)
	}
	if op.Max > 0 && n > op.Max {
		_ = conn.Send(proto.FileResult{Refusal: proto.RefuseTooLarge, Error: fmt.Sprintf("%s: %d bytes, over %d (read a range)", op.Path, n, op.Max), Stat: toStat(path.Clean(op.Path), st, "")})
		return
	}
	if err := conn.Send(proto.FileResult{OK: true, Stat: toStat(path.Clean(op.Path), st, "")}); err != nil {
		return
	}
	fw := proto.NewFrameWriter(conn.Writer())
	src := io.NewSectionReader(file, op.Offset, n)
	buf := make([]byte, 256<<10)
	var rerr error
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, werr := fw.Write(buf[:k]); werr != nil {
				return // the connection is gone
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			rerr = err
			break
		}
	}
	if fw.Close() != nil {
		return
	}
	if rerr != nil {
		_ = conn.Send(proto.FileResult{Error: op.Path + ": " + rerr.Error()})
		return
	}
	_ = conn.Send(proto.FileResult{OK: true})
}

// listBudget bounds a listing's encoded entries (the line stays under
// proto.MaxResult): past it the listing is truncated.
const listBudget = 1 << 20

func (f *files) list(op *proto.FileOp, rel string) proto.FileResult {
	fd, err := f.open(rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err == unix.ENOTDIR {
		return refuse(proto.RefuseInvalid, op.Path+" is not a directory")
	}
	if err != nil {
		return fail(op.Path, err)
	}
	d := os.NewFile(uintptr(fd), rel)
	defer d.Close()
	limit := op.Limit
	if limit <= 0 {
		limit = 1000
	}
	limit = min(limit, 100000)
	names, err := d.Readdirnames(limit + 1)
	if err != nil && err != io.EOF {
		return fail(op.Path, err)
	}
	r := proto.FileResult{OK: true, Entries: []proto.FileStat{}}
	if len(names) > limit {
		names, r.Truncated = names[:limit], true
	}
	sort.Strings(names)
	used := 0
	for _, name := range names {
		var st unix.Stat_t
		if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue // gone since
		}
		e := proto.FileStat{Name: name, Type: fileType(st.Mode), Size: st.Size, Mode: st.Mode & 0o7777, MTimeMs: st.Mtim.Nano() / 1e6}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			e.Target, _ = readlinkAt(fd, name)
		}
		b, _ := json.Marshal(e) // as it is sent: a control byte escapes to six
		if used += len(b) + 1; used > listBudget {
			r.Truncated = true
			break
		}
		r.Entries = append(r.Entries, e)
	}
	return r
}
