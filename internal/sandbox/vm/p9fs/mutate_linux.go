//go:build linux

package p9fs

import (
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// The mutating half of p9.File. Every call refuses a read-only export (and
// the virtual tree) up front; below that the host mount is the authority.

// chownBestEffort gives a new file the guest caller's ownership when the
// sandbox's uid map has it (range mode); in single-uid mode only root is
// mapped and the file stays the sandbox's — as a namespace-mode process
// creating it would leave it.
func chownBestEffort(dirfd int, name string, uid p9.UID, gid p9.GID) {
	_ = unix.Fchownat(dirfd, name, int(uid), int(gid), unix.AT_SYMLINK_NOFOLLOW)
}

// Create implements p9.File.
func (f *file) Create(name string, flags p9.OpenFlags, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.File, p9.QID, uint32, error) {
	if err := validName(name); err != nil {
		return nil, p9.QID{}, 0, err
	}
	if f.ro() {
		return nil, p9.QID{}, 0, unix.EROFS
	}
	open, err := openBeneath(f.fd, name, uint64(dotlFlags(flags)|unix.O_CREAT|unix.O_EXCL), uint64(perm.Permissions()))
	if err != nil {
		return nil, p9.QID{}, 0, err
	}
	chownBestEffort(f.fd, name, uid, gid)
	fd, err := openBeneath(f.fd, name, unix.O_PATH, 0)
	if err != nil {
		unix.Close(open)
		return nil, p9.QID{}, 0, err
	}
	st, err := fstat(fd)
	if err != nil {
		unix.Close(open)
		unix.Close(fd)
		return nil, p9.QID{}, 0, err
	}
	nf := &file{srv: f.srv, exp: f.exp, fd: fd, open: open, parent: f, name: name, path: f.path + "/" + name}
	return nf, f.srv.qidOf(&st), 0, nil
}

func (f *file) statChild(name string) (p9.QID, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(f.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return p9.QID{}, err
	}
	return f.srv.qidOf(&st), nil
}

// Mkdir implements p9.File.
func (f *file) Mkdir(name string, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.QID, error) {
	if err := validName(name); err != nil {
		return p9.QID{}, err
	}
	if f.ro() {
		return p9.QID{}, unix.EROFS
	}
	if err := unix.Mkdirat(f.fd, name, uint32(perm.Permissions())); err != nil {
		return p9.QID{}, err
	}
	chownBestEffort(f.fd, name, uid, gid)
	return f.statChild(name)
}

// Symlink implements p9.File.
func (f *file) Symlink(target, name string, uid p9.UID, gid p9.GID) (p9.QID, error) {
	if err := validName(name); err != nil {
		return p9.QID{}, err
	}
	if f.ro() {
		return p9.QID{}, unix.EROFS
	}
	if err := unix.Symlinkat(target, f.fd, name); err != nil {
		return p9.QID{}, err
	}
	chownBestEffort(f.fd, name, uid, gid)
	return f.statChild(name)
}

// Link implements p9.File (a hard link to target named name, here).
func (f *file) Link(target p9.File, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	t, ok := target.(*file)
	if !ok || t.v != nil || f.ro() {
		return unix.EROFS
	}
	return unix.Linkat(unix.AT_FDCWD, procPath(t.fd), f.fd, name, unix.AT_SYMLINK_FOLLOW)
}

// Mknod implements p9.File: FIFOs, sockets and empty regular files only —
// device nodes stay out of tile directories.
func (f *file) Mknod(name string, mode p9.FileMode, major, minor uint32, uid p9.UID, gid p9.GID) (p9.QID, error) {
	if err := validName(name); err != nil {
		return p9.QID{}, err
	}
	if f.ro() {
		return p9.QID{}, unix.EROFS
	}
	switch {
	case mode.IsNamedPipe(), mode.IsSocket(), mode.IsRegular():
	default:
		return p9.QID{}, unix.EPERM
	}
	if err := unix.Mknodat(f.fd, name, uint32(mode), 0); err != nil {
		return p9.QID{}, err
	}
	chownBestEffort(f.fd, name, uid, gid)
	return f.statChild(name)
}

// Rename implements p9.File (this fid, to newDir/newName).
func (f *file) Rename(newDir p9.File, newName string) error {
	d, ok := newDir.(*file)
	if !ok || f.parent == nil {
		return unix.EINVAL
	}
	return f.parent.RenameAt(f.name, d, newName)
}

// RenameAt implements p9.File.
func (f *file) RenameAt(oldName string, newDir p9.File, newName string) error {
	if validName(oldName) != nil || validName(newName) != nil {
		return unix.EINVAL
	}
	d, ok := newDir.(*file)
	if !ok || f.ro() || d.ro() {
		return unix.EROFS
	}
	return unix.Renameat(f.fd, oldName, d.fd, newName)
}

// UnlinkAt implements p9.File.
func (f *file) UnlinkAt(name string, flags uint32) error {
	if err := validName(name); err != nil {
		return err
	}
	if f.ro() {
		return unix.EROFS
	}
	return unix.Unlinkat(f.fd, name, int(flags)&unix.AT_REMOVEDIR)
}

// SetAttr implements p9.File.
func (f *file) SetAttr(valid p9.SetAttrMask, attr p9.SetAttr) error {
	if f.ro() {
		return unix.EROFS
	}
	st, err := fstat(f.fd)
	if err != nil {
		return err
	}
	link := st.Mode&unix.S_IFMT == unix.S_IFLNK
	if valid.Permissions && !link {
		if err := unix.Fchmodat(unix.AT_FDCWD, procPath(f.fd), uint32(attr.Permissions.Permissions()), 0); err != nil {
			return err
		}
	}
	if valid.UID || valid.GID {
		uid, gid := -1, -1
		if valid.UID {
			uid = int(attr.UID)
		}
		if valid.GID {
			gid = int(attr.GID)
		}
		if err := unix.Fchownat(f.fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
	}
	if valid.Size {
		if link || st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return unix.EINVAL
		}
		if f.open < 0 || unix.Ftruncate(f.open, int64(attr.Size)) != nil {
			if err := unix.Truncate(procPath(f.fd), int64(attr.Size)); err != nil {
				return err
			}
		}
	}
	if (valid.ATime || valid.MTime) && !link {
		ts := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
		if valid.ATime {
			ts[0] = unix.Timespec{Nsec: unix.UTIME_NOW}
			if valid.ATimeNotSystemTime {
				ts[0] = unix.Timespec{Sec: int64(attr.ATimeSeconds), Nsec: int64(attr.ATimeNanoSeconds)}
			}
		}
		if valid.MTime {
			ts[1] = unix.Timespec{Nsec: unix.UTIME_NOW}
			if valid.MTimeNotSystemTime {
				ts[1] = unix.Timespec{Sec: int64(attr.MTimeSeconds), Nsec: int64(attr.MTimeNanoSeconds)}
			}
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, procPath(f.fd), ts, 0); err != nil {
			return err
		}
	}
	return nil
}

// Extended attributes go through the fd's magic link, which names the file
// itself (never a path the guest chose).

// SetXattr implements p9.File.
func (f *file) SetXattr(attr string, data []byte, flags p9.XattrFlags) error {
	if f.ro() {
		return unix.EROFS
	}
	return unix.Setxattr(procPath(f.fd), attr, data, int(flags))
}

// GetXattr implements p9.File.
func (f *file) GetXattr(attr string) ([]byte, error) {
	if f.v != nil {
		return nil, unix.ENODATA
	}
	buf := make([]byte, 64<<10)
	n, err := unix.Getxattr(procPath(f.fd), attr, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// ListXattrs implements p9.File.
func (f *file) ListXattrs() ([]string, error) {
	if f.v != nil {
		return nil, nil
	}
	buf := make([]byte, 64<<10)
	n, err := unix.Listxattr(procPath(f.fd), buf)
	if err != nil {
		return nil, err
	}
	return splitNul(buf[:n]), nil
}

// RemoveXattr implements p9.File.
func (f *file) RemoveXattr(attr string) error {
	if f.ro() {
		return unix.EROFS
	}
	return unix.Removexattr(procPath(f.fd), attr)
}

func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	return out
}
