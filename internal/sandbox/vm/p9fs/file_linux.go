//go:build linux

package p9fs

import (
	"io"

	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// file is one fid: a virtual directory (v != nil) or a host file held by an
// O_PATH fd. The p9 server walks one name at a time and keeps each fid's
// parent referenced, so parent+name are always valid for a rename.
type file struct {
	p9.DefaultWalkGetAttr

	srv    *Server
	v      *vnode  // virtual node; nil for host files
	exp    *export // the export this file lives under
	fd     int     // O_PATH fd (-1 for virtual)
	open   int     // fd opened for I/O by Open/Create (-1 = not open)
	parent *file
	name   string
	path   string // absolute host path, for spotting nested exports
}

var _ p9.File = (*file)(nil)

func (f *file) ro() bool { return f.v != nil || f.exp.RO }

// Walk implements p9.File: zero names clones, one name steps (the server
// never passes more, and has already refused ".", ".." and "/").
func (f *file) Walk(names []string) ([]p9.QID, p9.File, error) {
	if len(names) == 0 {
		c := &file{srv: f.srv, v: f.v, exp: f.exp, fd: -1, open: -1, parent: f.parent, name: f.name, path: f.path}
		if f.v == nil {
			fd, err := unix.FcntlInt(uintptr(f.fd), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				return nil, nil, err
			}
			c.fd = fd
		}
		return nil, c, nil
	}
	if len(names) != 1 {
		return nil, nil, unix.EINVAL
	}
	name := names[0]
	if err := validName(name); err != nil {
		return nil, nil, err
	}
	if f.v != nil {
		c := f.v.children[name]
		if c == nil {
			return nil, nil, unix.ENOENT
		}
		if c.exp == nil {
			nf := &file{srv: f.srv, v: c, fd: -1, open: -1, parent: f, name: name}
			q, _, _, _ := nf.GetAttr(p9.AttrMaskAll)
			return []p9.QID{q}, nf, nil
		}
		fd, err := unix.FcntlInt(uintptr(c.exp.fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, nil, err
		}
		st, err := fstat(fd)
		if err != nil {
			unix.Close(fd)
			return nil, nil, err
		}
		return []p9.QID{f.srv.qidOf(&st)}, &file{srv: f.srv, exp: c.exp, fd: fd, open: -1, parent: f, name: name, path: c.exp.Path}, nil
	}
	fd, err := openBeneath(f.fd, name, unix.O_PATH, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := fstat(fd)
	if err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	child := &file{srv: f.srv, exp: f.exp, fd: fd, open: -1, parent: f, name: name, path: f.path + "/" + name}
	if x := f.srv.byPath[child.path]; x != nil {
		child.exp = x // a nested export: its own read-only flag
	}
	return []p9.QID{f.srv.qidOf(&st)}, child, nil
}

// GetAttr implements p9.File.
func (f *file) GetAttr(p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	if f.v != nil {
		q, m, a := virtualAttr()
		q.Path = f.srv.qidPath(^uint64(0), uint64(f.v.id))
		return q, m, a, nil
	}
	st, err := fstat(f.fd)
	if err != nil {
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, err
	}
	m, a := attrOf(&st)
	return f.srv.qidOf(&st), m, a, nil
}

// StatFS implements p9.File.
func (f *file) StatFS() (p9.FSStat, error) {
	if f.v != nil {
		return p9.FSStat{Type: 0x01021997, BlockSize: 4096, NameLength: 255}, nil
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(f.fd, &st); err != nil {
		return p9.FSStat{}, err
	}
	return p9.FSStat{
		Type:            uint32(st.Type),
		BlockSize:       uint32(st.Bsize),
		Blocks:          st.Blocks,
		BlocksFree:      st.Bfree,
		BlocksAvailable: st.Bavail,
		Files:           st.Files,
		FilesFree:       st.Ffree,
		FSID:            uint64(uint32(st.Fsid.Val[0])) | uint64(uint32(st.Fsid.Val[1]))<<32,
		NameLength:      uint32(st.Namelen),
	}, nil
}

// Open implements p9.File. Only directories and regular files are opened
// server-side; the guest kernel serves FIFOs, sockets and devices itself.
func (f *file) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	q, _, a, err := f.GetAttr(p9.AttrMaskAll)
	if err != nil {
		return q, 0, err
	}
	if f.v != nil {
		if mode.Mode() != p9.ReadOnly {
			return q, 0, unix.EROFS
		}
		return q, 0, nil
	}
	fl := dotlFlags(mode)
	if fl&unix.O_ACCMODE != unix.O_RDONLY && f.exp.RO {
		return q, 0, unix.EROFS
	}
	var fd int
	switch {
	case a.Mode.IsDir():
		fd, err = unix.Openat(f.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	case a.Mode.IsRegular():
		// Reopen through the magic link: the O_PATH fd already names a
		// regular file reached beneath the export, so nothing is resolved.
		fd, err = unix.Open(procPath(f.fd), fl|unix.O_CLOEXEC, 0)
	default:
		return q, 0, unix.EINVAL
	}
	if err != nil {
		return q, 0, err
	}
	f.open = fd
	return q, 0, nil
}

// ReadAt implements p9.File.
func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if f.open < 0 {
		return 0, unix.EBADF
	}
	n, err := unix.Pread(f.open, p, off)
	if err == nil && n == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	if n < 0 {
		n = 0
	}
	return n, err
}

// WriteAt implements p9.File.
func (f *file) WriteAt(p []byte, off int64) (int, error) {
	if f.open < 0 {
		return 0, unix.EBADF
	}
	n, err := unix.Pwrite(f.open, p, off)
	if n < 0 {
		n = 0
	}
	return n, err
}

// FSync implements p9.File.
func (f *file) FSync() error {
	if f.open >= 0 {
		return unix.Fsync(f.open)
	}
	return nil
}

// Close implements p9.File.
func (f *file) Close() error {
	if f.open >= 0 {
		unix.Close(f.open)
		f.open = -1
	}
	if f.fd >= 0 {
		unix.Close(f.fd)
		f.fd = -1
	}
	return nil
}

// Readdir implements p9.File. Offsets are the host filesystem's own
// getdents cookies, so a read resumes exactly where the last one ended.
func (f *file) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	if f.v != nil {
		return f.virtualReaddir(offset, count)
	}
	if f.open < 0 {
		return nil, unix.EBADF
	}
	if _, err := unix.Seek(f.open, int64(offset), io.SeekStart); err != nil {
		return nil, err
	}
	st, err := fstat(f.fd)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 32<<10)
	var out p9.Dirents
	budget := int(count)
	for budget > 0 {
		n, err := unix.Getdents(f.open, buf)
		if err != nil {
			return out, err
		}
		if n <= 0 {
			break
		}
		for b := buf[:n]; len(b) > 0; {
			ino := le64(b[0:8])
			off := le64(b[8:16])
			reclen := int(uint16(b[16]) | uint16(b[17])<<8)
			typ := b[18]
			name := cstring(b[19:reclen])
			b = b[reclen:]
			if name == "." || name == ".." {
				continue
			}
			qt := direntQIDType(typ)
			out = append(out, p9.Dirent{
				QID:    p9.QID{Type: qt, Path: f.srv.qidPath(st.Dev, ino)},
				Offset: off,
				Type:   qt,
				Name:   name,
			})
			budget -= 24 + len(name) // qid 13 + offset 8 + type 1 + name 2+len
			if budget <= 0 {
				break
			}
		}
	}
	return out, nil
}

func (f *file) virtualReaddir(offset uint64, count uint32) (p9.Dirents, error) {
	names := f.v.sortedNames()
	var out p9.Dirents
	for i := int(offset); i < len(names); i++ {
		c := f.v.children[names[i]]
		qt := p9.TypeDir
		if c.exp != nil {
			if st, err := fstat(c.exp.fd); err == nil {
				qt = p9.FileMode(st.Mode).QIDType()
			}
		}
		out = append(out, p9.Dirent{
			QID:    p9.QID{Type: qt, Path: f.srv.qidPath(^uint64(0), uint64(c.id))},
			Offset: uint64(i + 1),
			Type:   qt,
			Name:   names[i],
		})
	}
	return out, nil
}

// Readlink implements p9.File.
func (f *file) Readlink() (string, error) {
	if f.v != nil {
		return "", unix.EINVAL
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(f.fd, "", buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// Lock implements p9.File with open-file-description locks on the host, so
// guest processes (one fid each) exclude each other — and any host-side
// holder — the way POSIX locks would on a local disk. Never blocks: the guest
// kernel retries a blocked lock.
func (f *file) Lock(pid int, typ p9.LockType, flags p9.LockFlags, start, length uint64, client string) (p9.LockStatus, error) {
	if f.open < 0 {
		return p9.LockStatusError, unix.EBADF
	}
	lk := unix.Flock_t{Whence: io.SeekStart, Start: int64(start), Len: int64(length)}
	switch typ {
	case p9.ReadLock:
		lk.Type = unix.F_RDLCK
	case p9.WriteLock:
		lk.Type = unix.F_WRLCK
	case p9.Unlock:
		lk.Type = unix.F_UNLCK
	default:
		return p9.LockStatusError, unix.EINVAL
	}
	if err := unix.FcntlFlock(uintptr(f.open), unix.F_OFD_SETLK, &lk); err != nil {
		if err == unix.EAGAIN || err == unix.EACCES {
			return p9.LockStatusBlocked, nil
		}
		return p9.LockStatusError, err
	}
	return p9.LockStatusOK, nil
}

// Renamed implements p9.File: the server moved this fid.
func (f *file) Renamed(newDir p9.File, newName string) {
	if d, ok := newDir.(*file); ok {
		f.parent = d
		f.path = d.path + "/" + newName
	}
	f.name = newName
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

func direntQIDType(t uint8) p9.QIDType {
	switch t {
	case unix.DT_DIR:
		return p9.TypeDir
	case unix.DT_LNK:
		return p9.TypeSymlink
	}
	return p9.TypeRegular
}
