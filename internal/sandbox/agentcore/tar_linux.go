//go:build linux

package agentcore

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Trees move as tar streams (Go's archive/tar: the agent is a static binary
// with no tool to lean on). tar-get walks by descriptor and never follows a
// symlink — it stores the link. tar-put extracts through an os.Root opened on
// the target directory, so no entry — "../x", an absolute name, a path
// through a symlink it planted itself — writes outside it.

// tarGet: the directory's stat line, the archive in frames, the
// terminator, the last line.
func (f *files) tarGet(op *proto.FileOp, rel string, conn *proto.Conn) {
	fd, err := f.open(rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err == unix.ENOTDIR {
		_ = conn.Send(refuse(proto.RefuseInvalid, op.Path+" is not a directory (read a file with read)"))
		return
	}
	if err != nil {
		_ = conn.Send(fail(op.Path, err))
		return
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		_ = conn.Send(fail(op.Path, err))
		return
	}
	if err := conn.Send(proto.FileResult{OK: true, Stat: toStat(path.Clean(op.Path), &st, "")}); err != nil {
		unix.Close(fd)
		return
	}
	fw := proto.NewFrameWriter(conn.Writer())
	out := &capWriter{w: fw, left: op.Max}
	bw := bufio.NewWriterSize(out, 256<<10)
	w := &tarWalk{tw: tar.NewWriter(bw), exclude: op.Exclude, links: map[[2]uint64]string{}}
	err = w.dir(fd, "", 0) // closes fd
	if err == nil {
		err = w.tw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if out.broken { // the connection is gone
		return
	}
	if fw.Close() != nil {
		return
	}
	switch {
	case errors.Is(err, errTooLarge):
		_ = conn.Send(refuse(proto.RefuseTooLarge, fmt.Sprintf("%s: the archive is over %d bytes", op.Path, op.Max)))
	case err != nil:
		_ = conn.Send(proto.FileResult{Error: op.Path + ": " + err.Error()})
	default:
		_ = conn.Send(proto.FileResult{OK: true})
	}
}

// capWriter passes writes on until left runs out (0: no bound); it notes a
// write that failed, which means the connection did.
type capWriter struct {
	w      io.Writer
	left   int64
	used   int64
	broken bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.left > 0 && c.used+int64(len(p)) > c.left {
		return 0, errTooLarge
	}
	n, err := c.w.Write(p)
	c.used += int64(n)
	if err != nil {
		c.broken = true
	}
	return n, err
}

type tarWalk struct {
	tw      *tar.Writer
	exclude []string
	links   map[[2]uint64]string // dev/ino → the first name of a hard-linked file
}

func (w *tarWalk) excluded(rel, name string) bool {
	for _, x := range w.exclude {
		if ok, _ := path.Match(x, rel); ok {
			return true
		}
		if ok, _ := path.Match(x, name); ok {
			return true
		}
	}
	return false
}

// dir archives the entries of fd (names under prefix), then closes it.
func (w *tarWalk) dir(fd int, prefix string, depth int) error {
	d := os.NewFile(uintptr(fd), prefix)
	defer d.Close()
	if depth > 256 {
		return fmt.Errorf("%s: too deep", prefix)
	}
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		rel := prefix + name
		if w.excluded(rel, name) {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if err == unix.ENOENT {
				continue // gone since
			}
			return fmt.Errorf("%s: %w", rel, err)
		}
		h := &tar.Header{
			Name: rel, Mode: int64(st.Mode & 0o7777), Uid: int(st.Uid), Gid: int(st.Gid),
			ModTime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			h.Typeflag, h.Name = tar.TypeDir, rel+"/"
			if err := w.tw.WriteHeader(h); err != nil {
				return err
			}
			sub, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				if err == unix.ENOENT {
					continue
				}
				return fmt.Errorf("%s: %w", rel, err)
			}
			if err := w.dir(sub, rel+"/", depth+1); err != nil {
				return err
			}
		case unix.S_IFLNK:
			target, err := readlinkAt(fd, name)
			if err != nil {
				continue
			}
			h.Typeflag, h.Linkname = tar.TypeSymlink, target
			if err := w.tw.WriteHeader(h); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := w.file(fd, name, rel, h, &st); err != nil {
				return err
			}
		default:
			// devices, FIFOs, sockets: not archived
		}
	}
	return nil
}

func (w *tarWalk) file(dir int, name, rel string, h *tar.Header, st *unix.Stat_t) error {
	key := [2]uint64{uint64(st.Dev), st.Ino}
	if st.Nlink > 1 {
		if first, ok := w.links[key]; ok {
			h.Typeflag, h.Linkname = tar.TypeLink, first
			return w.tw.WriteHeader(h)
		}
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return fmt.Errorf("%s: %w", rel, err)
	}
	file := os.NewFile(uintptr(fd), rel)
	defer file.Close()
	var now unix.Stat_t
	if unix.Fstat(fd, &now) != nil || now.Ino != st.Ino || now.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil // replaced since
	}
	if st.Nlink > 1 {
		w.links[key] = rel
	}
	h.Typeflag, h.Size = tar.TypeReg, now.Size
	if err := w.tw.WriteHeader(h); err != nil {
		return err
	}
	// exactly Size bytes: a file that shrank meanwhile is padded, one that
	// grew is cut
	n, err := io.Copy(w.tw, io.LimitReader(file, h.Size))
	if err != nil {
		var pe *os.PathError
		if !errors.As(err, &pe) { // not the file's read: the archive's write
			return err
		}
	}
	if n < h.Size {
		_, err = io.CopyN(w.tw, zeros{}, h.Size-n)
	}
	return err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// tarPut extracts a tar stream (frames, then the terminator) under the
// directory, creating it first with Mkdirs.
func (f *files) tarPut(op *proto.FileOp, rel string, conn *proto.Conn) proto.FileResult {
	if op.Mkdirs {
		if err := f.mkdirAll(rel, 0o755, op.Owner); err != nil {
			return fail(op.Path, err)
		}
	}
	fd, err := f.open(rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err == unix.ENOTDIR {
		return refuse(proto.RefuseInvalid, op.Path+" is not a directory")
	}
	if err != nil {
		return fail(op.Path, err)
	}
	root, err := openRootAt(fd, f.c.o.Root, rel)
	unix.Close(fd)
	if err != nil {
		return proto.FileResult{Error: op.Path + ": " + err.Error()}
	}
	defer root.Close()
	src := io.Reader(proto.NewFrameReader(conn.Reader()))
	if op.Max > 0 {
		src = &capReader{r: src, left: op.Max}
	}
	x := &extract{root: root, owner: op.Owner}
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err == nil {
			err = x.entry(h, tr)
		}
		if err != nil {
			return x.failed(op, err)
		}
	}
	// what trails the archive, up to the terminator
	if _, err := io.Copy(io.Discard, src); err != nil {
		return x.failed(op, err)
	}
	return proto.FileResult{OK: true}
}

// openRootAt opens an os.Root on an already-resolved directory: through
// /proc/self/fd, or — without /proc, at the sandbox's own "/" — by path.
func openRootAt(fd int, root, rel string) (*os.Root, error) {
	r, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil && root == "/" {
		r, err = os.OpenRoot("/" + rel)
	}
	return r, err
}

type extract struct {
	root  *os.Root
	owner *[2]uint32
}

func (x *extract) failed(op *proto.FileOp, err error) proto.FileResult {
	switch {
	case errors.Is(err, errTooLarge):
		return refuse(proto.RefuseTooLarge, fmt.Sprintf("%s: the archive is over %d bytes", op.Path, op.Max))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return proto.FileResult{Error: op.Path + ": the archive ended early"}
	case errors.Is(err, tar.ErrHeader) || errors.Is(err, tar.ErrFieldTooLong):
		return refuse(proto.RefuseInvalid, op.Path+": bad tar: "+err.Error())
	}
	return fail(op.Path, err)
}

// entryName is where an entry lands under the target: an absolute name
// lands under it too; "" skips one that climbs out.
func entryName(name string) string {
	n := path.Clean(strings.TrimLeft(name, "/"))
	if n == "." || n == ".." || strings.HasPrefix(n, "../") {
		return ""
	}
	return n
}

// escaped is an os.Root refusal: the path leads out of the target (through
// a symlink or ".."). Everything else it returns is an errno.
func escaped(err error) bool {
	var errno syscall.Errno
	return err != nil && !errors.As(err, &errno)
}

func (x *extract) entry(h *tar.Header, body io.Reader) error {
	name := entryName(h.Name)
	if name == "" {
		return nil
	}
	perm := os.FileMode(h.Mode & 0o1777) // no setuid/setgid from a stream
	var err error
	switch h.Typeflag {
	case tar.TypeDir:
		err = x.mkdirAll(name)
		if err == nil {
			_ = x.root.Chmod(name, perm) // (no umask)
			x.chown(name)
		}
	case tar.TypeReg: // (the reader turns the old TypeRegA into TypeReg)
		if err = x.clear(name); err == nil {
			err = x.file(name, perm, body, h.ModTime)
		}
	case tar.TypeSymlink:
		if err = x.clear(name); err == nil {
			if err = x.root.Symlink(h.Linkname, name); err == nil {
				x.chown(name)
			}
		}
	case tar.TypeLink:
		target := entryName(h.Linkname)
		if target == "" {
			return nil
		}
		if err = x.clear(name); err == nil {
			err = x.root.Link(target, name)
		}
	default:
		return nil // devices, FIFOs, PAX globals: not extracted
	}
	if escaped(err) {
		return nil // skipped: it would land outside
	}
	return err
}

// clear makes way for a non-directory entry: its parents exist, and what
// was at its name — a symlink above all, which it replaces rather than
// writes through — is gone. A directory there is an error.
func (x *extract) clear(name string) error {
	if dir := path.Dir(name); dir != "." {
		if err := x.mkdirAll(dir); err != nil {
			return err
		}
	}
	fi, err := x.root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s: %w", name, syscall.EISDIR)
	}
	return x.root.Remove(name)
}

// mkdirAll is Root.MkdirAll, but a symlink leading out of the target is an
// escape on every toolchain: Go 1.26's answers EEXIST for one (1.27's, the
// escape), which would fail the whole put instead of skipping the entry.
func (x *extract) mkdirAll(name string) error {
	err := x.root.MkdirAll(name, 0o755)
	if errors.Is(err, syscall.EEXIST) {
		if _, serr := x.root.Stat(name); escaped(serr) {
			return serr
		}
	}
	return err
}

func (x *extract) file(name string, perm os.FileMode, body io.Reader, mt time.Time) error {
	w, err := x.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, body)
	if err == nil {
		err = w.Chmod(perm) // (no umask)
	}
	if err2 := w.Close(); err == nil {
		err = err2
	}
	if err != nil {
		return err
	}
	x.chown(name)
	_ = x.root.Chtimes(name, mt, mt)
	return nil
}

func (x *extract) chown(name string) {
	if x.owner != nil {
		_ = x.root.Lchown(name, int(x.owner[0]), int(x.owner[1]))
	}
}
