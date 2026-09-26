//go:build linux

package fusefs

import (
	"io"
	"math"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// The operations below refuse a read-only node up front; below that the host
// mount is the authority. New files take the guest caller's ownership when
// the sandbox's uid map has it (range mode) — in single-uid mode only root is
// mapped and the file stays the sandbox's, as a namespace-mode process would
// leave it.

func (fs *FS) parentFor(id uint64) (*node, fuse.Status) {
	p := fs.node(id)
	if p == nil {
		return nil, fuse.ENOENT
	}
	if p.ro {
		return nil, fuse.EROFS
	}
	return p, fuse.OK
}

// made finishes a create-style op: chown to the caller, look the new name up.
func (fs *FS) made(p *node, name string, c *fuse.Caller, out *fuse.EntryOut) fuse.Status {
	_ = unix.Fchownat(p.fd, name, int(c.Uid), int(c.Gid), unix.AT_SYMLINK_NOFOLLOW)
	if fs.w != nil {
		fs.w.ours(p.id, name)
	}
	return fuse.Status(fs.lookup(p, name, out))
}

// Mknod implements fuse.RawFileSystem: FIFOs, sockets, regular files —
// device nodes stay out of tile directories.
func (fs *FS) Mknod(_ <-chan struct{}, in *fuse.MknodIn, name string, out *fuse.EntryOut) fuse.Status {
	p, st := fs.parentFor(in.NodeId)
	if !st.Ok() {
		return st
	}
	switch in.Mode & unix.S_IFMT {
	case unix.S_IFIFO, unix.S_IFSOCK, unix.S_IFREG:
	default:
		return fuse.EPERM
	}
	if !validName(name) {
		return fuse.EINVAL
	}
	if err := unix.Mknodat(p.fd, name, in.Mode, 0); err != nil {
		return fuse.ToStatus(err)
	}
	return fs.made(p, name, &in.Caller, out)
}

// Mkdir implements fuse.RawFileSystem.
func (fs *FS) Mkdir(_ <-chan struct{}, in *fuse.MkdirIn, name string, out *fuse.EntryOut) fuse.Status {
	p, st := fs.parentFor(in.NodeId)
	if !st.Ok() {
		return st
	}
	if !validName(name) {
		return fuse.EINVAL
	}
	if err := unix.Mkdirat(p.fd, name, in.Mode); err != nil {
		return fuse.ToStatus(err)
	}
	return fs.made(p, name, &in.Caller, out)
}

// Symlink implements fuse.RawFileSystem.
func (fs *FS) Symlink(_ <-chan struct{}, h *fuse.InHeader, target, name string, out *fuse.EntryOut) fuse.Status {
	p, st := fs.parentFor(h.NodeId)
	if !st.Ok() {
		return st
	}
	if !validName(name) {
		return fuse.EINVAL
	}
	if err := unix.Symlinkat(target, p.fd, name); err != nil {
		return fuse.ToStatus(err)
	}
	return fs.made(p, name, &h.Caller, out)
}

// Link implements fuse.RawFileSystem.
func (fs *FS) Link(_ <-chan struct{}, in *fuse.LinkIn, name string, out *fuse.EntryOut) fuse.Status {
	p, st := fs.parentFor(in.NodeId)
	if !st.Ok() {
		return st
	}
	t := fs.node(in.Oldnodeid)
	if t == nil {
		return fuse.ENOENT
	}
	if !validName(name) {
		return fuse.EINVAL
	}
	if err := unix.Linkat(unix.AT_FDCWD, procPath(t.fd), p.fd, name, unix.AT_SYMLINK_FOLLOW); err != nil {
		return fuse.ToStatus(err)
	}
	if fs.w != nil {
		fs.w.ours(p.id, name)
	}
	return fuse.Status(fs.lookup(p, name, out))
}

func (fs *FS) unlink(id uint64, name string, flags int) fuse.Status {
	p, st := fs.parentFor(id)
	if !st.Ok() {
		return st
	}
	if !validName(name) {
		return fuse.EINVAL
	}
	if fs.w != nil {
		fs.w.ours(p.id, name)
	}
	return fuse.ToStatus(unix.Unlinkat(p.fd, name, flags))
}

// Unlink implements fuse.RawFileSystem.
func (fs *FS) Unlink(_ <-chan struct{}, h *fuse.InHeader, name string) fuse.Status {
	return fs.unlink(h.NodeId, name, 0)
}

// Rmdir implements fuse.RawFileSystem.
func (fs *FS) Rmdir(_ <-chan struct{}, h *fuse.InHeader, name string) fuse.Status {
	return fs.unlink(h.NodeId, name, unix.AT_REMOVEDIR)
}

// Rename implements fuse.RawFileSystem (renameat2 flags included).
func (fs *FS) Rename(_ <-chan struct{}, in *fuse.RenameIn, oldName, newName string) fuse.Status {
	p, st := fs.parentFor(in.NodeId)
	if !st.Ok() {
		return st
	}
	np, st := fs.parentFor(in.Newdir)
	if !st.Ok() {
		return st
	}
	if !validName(oldName) || !validName(newName) {
		return fuse.EINVAL
	}
	if fs.w != nil {
		fs.w.ours(p.id, oldName)
		fs.w.ours(np.id, newName)
	}
	return fuse.ToStatus(unix.Renameat2(p.fd, oldName, np.fd, newName, uint(in.Flags)))
}

// Opens are zero-message: OPEN, OPENDIR, CREATE and FLUSH answer ENOSYS, so
// the guest kernel stops sending them (and RELEASE) and opens files by
// itself, keeping its page cache and directory listings across opens
// (FOPEN_KEEP_CACHE / FOPEN_CACHE_DIR are its defaults then). A new file
// arrives as MKNOD. Every open would otherwise cost two or three vsock round
// trips; I/O now names the node, which carries its own lazily opened fds.

// Create implements fuse.RawFileSystem: the kernel falls back to MKNOD.
func (fs *FS) Create(<-chan struct{}, *fuse.CreateIn, string, *fuse.CreateOut) fuse.Status {
	return fuse.ENOSYS
}

// Open implements fuse.RawFileSystem: zero-message opens.
func (fs *FS) Open(<-chan struct{}, *fuse.OpenIn, *fuse.OpenOut) fuse.Status { return fuse.ENOSYS }

// Flush implements fuse.RawFileSystem: nothing to do on close.
func (fs *FS) Flush(<-chan struct{}, *fuse.FlushIn) fuse.Status { return fuse.ENOSYS }

// Read implements fuse.RawFileSystem.
func (fs *FS) Read(_ <-chan struct{}, in *fuse.ReadIn, buf []byte) (fuse.ReadResult, fuse.Status) {
	n := fs.node(in.NodeId)
	if n == nil {
		return nil, fuse.ENOENT
	}
	fd, err := fs.ioFD(n, false)
	if err != nil {
		return nil, fuse.ToStatus(err)
	}
	c, err := unix.Pread(fd, buf[:min(int(in.Size), len(buf))], int64(in.Offset))
	if err != nil {
		return nil, fuse.ToStatus(err)
	}
	return fuse.ReadResultData(buf[:c]), fuse.OK
}

// Write implements fuse.RawFileSystem.
func (fs *FS) Write(_ <-chan struct{}, in *fuse.WriteIn, data []byte) (uint32, fuse.Status) {
	n := fs.node(in.NodeId)
	if n == nil {
		return 0, fuse.ENOENT
	}
	fd, err := fs.ioFD(n, true)
	if err != nil {
		return 0, fuse.ToStatus(err)
	}
	c, err := unix.Pwrite(fd, data, int64(in.Offset))
	fs.wrote(n)
	return uint32(max(c, 0)), fuse.ToStatus(err)
}

// Lseek implements fuse.RawFileSystem (SEEK_DATA/SEEK_HOLE).
func (fs *FS) Lseek(_ <-chan struct{}, in *fuse.LseekIn, out *fuse.LseekOut) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	fd, err := fs.ioFD(n, false)
	if err != nil {
		return fuse.ToStatus(err)
	}
	off, err := unix.Seek(fd, int64(in.Offset), int(in.Whence))
	out.Offset = uint64(off)
	return fuse.ToStatus(err)
}

// Fallocate implements fuse.RawFileSystem.
func (fs *FS) Fallocate(_ <-chan struct{}, in *fuse.FallocateIn) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	fd, err := fs.ioFD(n, true)
	if err != nil {
		return fuse.ToStatus(err)
	}
	fs.wrote(n)
	return fuse.ToStatus(unix.Fallocate(fd, in.Mode, int64(in.Offset), int64(in.Length)))
}

// Fsync implements fuse.RawFileSystem.
func (fs *FS) Fsync(_ <-chan struct{}, in *fuse.FsyncIn) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	fd, err := fs.ioFD(n, false)
	if err != nil {
		return fuse.ToStatus(err)
	}
	if in.FsyncFlags&1 != 0 {
		return fuse.ToStatus(unix.Fdatasync(fd))
	}
	return fuse.ToStatus(unix.Fsync(fd))
}

// Locks are open-file-description locks on the host, one description per
// guest lock owner (a process's file table), so guest processes exclude
// each other — and any host-side holder — as POSIX locks would locally.

func flock(lk *fuse.FileLock) unix.Flock_t {
	f := unix.Flock_t{Type: int16(lk.Typ), Whence: io.SeekStart, Start: int64(lk.Start)}
	if lk.End != math.MaxInt64 && lk.End != math.MaxUint64 {
		f.Len = int64(lk.End - lk.Start + 1)
	}
	return f
}

// lockFD is the open file description that holds owner's locks on n.
func (fs *FS) lockFD(n *node, owner uint64) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fd, ok := n.locks[owner]; ok {
		return fd, nil
	}
	if n.fd < 0 {
		return -1, unix.EBADF
	}
	mode := unix.O_RDWR
	if n.ro {
		mode = unix.O_RDONLY
	}
	fd, err := unix.Open(procPath(n.fd), mode|unix.O_CLOEXEC, 0)
	if err != nil && mode == unix.O_RDWR {
		fd, err = unix.Open(procPath(n.fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return -1, err
	}
	if n.locks == nil {
		n.locks = map[uint64]int{}
	}
	n.locks[owner] = fd
	return fd, nil
}

// GetLk implements fuse.RawFileSystem.
func (fs *FS) GetLk(_ <-chan struct{}, in *fuse.LkIn, out *fuse.LkOut) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	fd, err := fs.lockFD(n, in.Owner)
	if err != nil {
		return fuse.ToStatus(err)
	}
	f := flock(&in.Lk)
	if err := unix.FcntlFlock(uintptr(fd), unix.F_OFD_GETLK, &f); err != nil {
		return fuse.ToStatus(err)
	}
	out.Lk = fuse.FileLock{Typ: uint32(f.Type), Start: uint64(f.Start), End: math.MaxInt64}
	if f.Len > 0 {
		out.Lk.End = uint64(f.Start + f.Len - 1)
	}
	return fuse.OK
}

// SetLk implements fuse.RawFileSystem.
func (fs *FS) SetLk(_ <-chan struct{}, in *fuse.LkIn) fuse.Status {
	return fs.setLk(in, unix.F_OFD_SETLK)
}

// SetLkw implements fuse.RawFileSystem.
func (fs *FS) SetLkw(_ <-chan struct{}, in *fuse.LkIn) fuse.Status {
	return fs.setLk(in, unix.F_OFD_SETLKW)
}

func (fs *FS) setLk(in *fuse.LkIn, cmd int) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	fd, err := fs.lockFD(n, in.Owner)
	if err != nil {
		return fuse.ToStatus(err)
	}
	f := flock(&in.Lk)
	return fuse.ToStatus(unix.FcntlFlock(uintptr(fd), cmd, &f))
}

// ---- directories ----

// OpenDir implements fuse.RawFileSystem: zero-message, like files; the
// guest keeps listings (FOPEN_CACHE_DIR) until the watcher invalidates.
func (fs *FS) OpenDir(<-chan struct{}, *fuse.OpenIn, *fuse.OpenOut) fuse.Status {
	return fuse.ENOSYS
}

// FsyncDir implements fuse.RawFileSystem.
func (fs *FS) FsyncDir(_ <-chan struct{}, in *fuse.FsyncIn) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	return fuse.ToStatus(unix.Syncfs(n.fd))
}

// ReadDir implements fuse.RawFileSystem.
func (fs *FS) ReadDir(_ <-chan struct{}, in *fuse.ReadIn, out *fuse.DirEntryList) fuse.Status {
	return fs.readDir(in, out, false)
}

// ReadDirPlus implements fuse.RawFileSystem: entries with their attributes,
// so a traversal needs no lookup round trip per file.
func (fs *FS) ReadDirPlus(_ <-chan struct{}, in *fuse.ReadIn, out *fuse.DirEntryList) fuse.Status {
	return fs.readDir(in, out, true)
}

// readDir resumes at the host filesystem's own getdents cookie, so a
// listing continues exactly where the last reply ended.
func (fs *FS) readDir(in *fuse.ReadIn, out *fuse.DirEntryList, plus bool) fuse.Status {
	dir := fs.node(in.NodeId)
	if dir == nil {
		return fuse.ENOENT
	}
	dfd, err := unix.Openat(dir.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fuse.ToStatus(err)
	}
	defer unix.Close(dfd)
	if _, err := unix.Seek(dfd, int64(in.Offset), io.SeekStart); err != nil {
		return fuse.ToStatus(err)
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := unix.Getdents(dfd, buf)
		if err != nil {
			return fuse.ToStatus(err)
		}
		if n <= 0 {
			return fuse.OK
		}
		for b := buf[:n]; len(b) > 0; {
			reclen := int(uint16(b[16]) | uint16(b[17])<<8)
			e := fuse.DirEntry{
				Ino:  le64(b[0:8]),
				Off:  le64(b[8:16]),
				Mode: uint32(b[18]) << 12, // DT_* → S_IF* high bits
				Name: cstring(b[19:reclen]),
			}
			b = b[reclen:]
			if !plus {
				if !out.AddDirEntry(e) {
					return fuse.OK
				}
				continue
			}
			eo := out.AddDirLookupEntry(e)
			if eo == nil {
				return fuse.OK
			}
			if e.Name == "." || e.Name == ".." {
				continue // no lookup: the kernel ignores their entry
			}
			if fs.lookup(dir, e.Name, eo) != 0 {
				*eo = fuse.EntryOut{} // vanished since getdents: no node
			}
		}
	}
}

func le64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
