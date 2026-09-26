//go:build linux

// Package fusefs serves a VM sandbox's host binds to the guest as FUSE
// filesystems over vsock (plans/vm-sandbox.md): the guest kernel's FUSE
// requests travel over a vsock stream to this server, which runs in the host
// shim — inside the namespace sandbox that also holds Firecracker, so its view
// is exactly the bind set a namespace-mode workload would see (read-only
// binds fail with EROFS from the host kernel, masks read empty).
//
// Two properties matter beyond plain passthrough:
//
//   - Nothing is resolved by path. Every step is one openat2 from the parent's
//     O_PATH fd with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS
//     (crossing mounts, as a namespace-mode process would), so a guest can't
//     walk out of an export into the shim's own plumbing. Nodes are keyed by
//     (mount id, inode): the same inode reached through a read-only and a
//     writable bind stays two nodes, each holding its own mount's fd.
//
//   - The guest caches aggressively — entries, attributes, directory
//     listings, page cache, negative lookups — for as long as the host says
//     nothing changed: inotify on every directory the guest looked into turns
//     host-side changes (the browser editor, a namespace terminal, another
//     VM) into FUSE invalidations (watch_linux.go). That is what makes a warm
//     `git status` or `find` run at guest-memory speed instead of one vsock
//     round trip per path component.
package fusefs

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// ttlWatched is how long the guest may trust an entry or attributes while
// inotify is there to invalidate them; ttlBlind when it isn't.
const (
	ttlWatched = time.Hour
	ttlBlind   = time.Second
)

// FS is one export, served as one FUSE connection.
type FS struct {
	fuse.RawFileSystem // ENOSYS for what isn't implemented below

	root   string
	nested map[string]bool // other exports below root: path → read-only

	mu     sync.Mutex
	nodes  map[uint64]*node
	byKey  map[nodeKey]uint64
	nextID uint64

	srv *fuse.Server
	w   *watcher // nil = no inotify: short TTLs
}

type nodeKey struct{ mnt, ino uint64 }

type node struct {
	id      uint64
	fd      int // O_PATH
	key     nodeKey
	parent  uint64
	name    string
	path    string // absolute, for spotting nested exports
	ro      bool
	dir     bool
	nlookup uint64

	// Opens are zero-message (ops_linux.go): I/O arrives by node, so the
	// node carries its own open fds, made on first use.
	rfd, wfd  int            // O_RDONLY / O_RDWR (-1 = not yet)
	locks     map[uint64]int // lock owner → its own open file description
	lastWrite time.Time      // the guest wrote it (don't invalidate its cache)
}

func (n *node) closeFDs() {
	for _, fd := range []int{n.fd, n.rfd, n.wfd} {
		if fd >= 0 {
			unix.Close(fd)
		}
	}
	for _, fd := range n.locks {
		unix.Close(fd)
	}
	n.fd, n.rfd, n.wfd, n.locks = -1, -1, -1, nil
}

// New opens the export root. nested maps the other exports under it (the
// guest mounts them separately; a walk that reaches one takes its flag).
func New(root string, ro bool, nested map[string]bool) (*FS, error) {
	root = path.Clean("/" + root)
	fd, err := unix.Open(root, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("fusefs: export %s: %w", root, err)
	}
	st, err := statx(fd)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	fs := &FS{
		RawFileSystem: fuse.NewDefaultRawFileSystem(),
		root:          root, nested: nested,
		nodes: map[uint64]*node{}, byKey: map[nodeKey]uint64{}, nextID: fuse.FUSE_ROOT_ID + 1,
	}
	r := &node{id: fuse.FUSE_ROOT_ID, fd: fd, rfd: -1, wfd: -1, key: keyOf(&st), path: root, ro: ro, dir: st.Mode&unix.S_IFMT == unix.S_IFDIR, nlookup: 1}
	fs.nodes[r.id] = r
	fs.byKey[r.key] = r.id
	if w, err := newWatcher(fs); err == nil {
		fs.w = w
		fs.w.add(r)
	}
	return fs, nil
}

// Init implements fuse.RawFileSystem: keep the server for notifications.
func (fs *FS) Init(s *fuse.Server) {
	fs.srv = s
	if fs.w != nil {
		go fs.w.run()
	}
}

// Close releases every fd (the connection is gone).
func (fs *FS) Close() {
	if fs.w != nil {
		fs.w.close()
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, n := range fs.nodes {
		n.closeFDs()
	}
	fs.nodes = map[uint64]*node{}
}

func (fs *FS) String() string { return "xbinfs:" + fs.root }

func (fs *FS) ttl() time.Duration {
	if fs.w != nil {
		return ttlWatched
	}
	return ttlBlind
}

func (fs *FS) node(id uint64) *node {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.nodes[id]
}

// lookup opens name beneath parent and registers (or re-counts) its node,
// filling out. ENOENT is returned as is (the caller decides on caching it).
func (fs *FS) lookup(parent *node, name string, out *fuse.EntryOut) syscall.Errno {
	fd, err := openBeneath(parent.fd, name, unix.O_PATH, 0)
	if err != nil {
		return errno(err)
	}
	st, err := statx(fd)
	if err != nil {
		unix.Close(fd)
		return errno(err)
	}
	k := keyOf(&st)
	fs.mu.Lock()
	n := fs.nodes[fs.byKey[k]]
	if n != nil {
		unix.Close(fd)
		n.nlookup++
		n.parent, n.name = parent.id, name
	} else {
		p := parent.path + "/" + name
		ro, ok := fs.nested[p]
		if !ok {
			ro = parent.ro
		}
		n = &node{id: fs.nextID, fd: fd, rfd: -1, wfd: -1, key: k, parent: parent.id, name: name, path: p, ro: ro,
			dir: st.Mode&unix.S_IFMT == unix.S_IFDIR, nlookup: 1}
		fs.nextID++
		fs.nodes[n.id] = n
		fs.byKey[k] = n.id
	}
	fs.mu.Unlock()
	if n.dir && fs.w != nil {
		fs.w.add(n)
	}
	out.NodeId = n.id
	out.Generation = 1
	fillAttr(&out.Attr, &st)
	out.SetEntryTimeout(fs.ttl())
	out.SetAttrTimeout(fs.ttl())
	return 0
}

// Lookup implements fuse.RawFileSystem. A missing name is cached as a
// negative entry; inotify invalidates it when the name appears.
func (fs *FS) Lookup(_ <-chan struct{}, h *fuse.InHeader, name string, out *fuse.EntryOut) fuse.Status {
	p := fs.node(h.NodeId)
	if p == nil {
		return fuse.ENOENT
	}
	if e := fs.lookup(p, name, out); e != 0 {
		if e == unix.ENOENT {
			*out = fuse.EntryOut{}
			out.SetEntryTimeout(fs.ttl())
			return fuse.OK
		}
		return fuse.Status(e)
	}
	return fuse.OK
}

// Forget implements fuse.RawFileSystem.
func (fs *FS) Forget(id, n uint64) {
	fs.mu.Lock()
	nd := fs.nodes[id]
	if nd == nil || id == fuse.FUSE_ROOT_ID {
		fs.mu.Unlock()
		return
	}
	if nd.nlookup > n {
		nd.nlookup -= n
		fs.mu.Unlock()
		return
	}
	delete(fs.nodes, id)
	if fs.byKey[nd.key] == id {
		delete(fs.byKey, nd.key)
	}
	fs.mu.Unlock()
	if fs.w != nil && nd.dir {
		fs.w.remove(nd)
	}
	fs.mu.Lock()
	nd.closeFDs()
	fs.mu.Unlock()
}

// GetAttr implements fuse.RawFileSystem.
func (fs *FS) GetAttr(_ <-chan struct{}, in *fuse.GetAttrIn, out *fuse.AttrOut) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	st, err := statx(n.fd)
	if err != nil {
		return fuse.ToStatus(err)
	}
	fillAttr(&out.Attr, &st)
	out.SetTimeout(fs.ttl())
	return fuse.OK
}

// SetAttr implements fuse.RawFileSystem.
func (fs *FS) SetAttr(_ <-chan struct{}, in *fuse.SetAttrIn, out *fuse.AttrOut) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	if n.ro {
		return fuse.EROFS
	}
	st, err := statx(n.fd)
	if err != nil {
		return fuse.ToStatus(err)
	}
	link := st.Mode&unix.S_IFMT == unix.S_IFLNK
	if mode, ok := in.GetMode(); ok && !link {
		if err := unix.Fchmodat(unix.AT_FDCWD, procPath(n.fd), mode&07777, 0); err != nil {
			return fuse.ToStatus(err)
		}
	}
	uid, uok := in.GetUID()
	gid, gok := in.GetGID()
	if uok || gok {
		u, g := -1, -1
		if uok {
			u = int(uid)
		}
		if gok {
			g = int(gid)
		}
		if err := unix.Fchownat(n.fd, "", u, g, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fuse.ToStatus(err)
		}
	}
	if size, ok := in.GetSize(); ok {
		if err := unix.Truncate(procPath(n.fd), int64(size)); err != nil {
			return fuse.ToStatus(err)
		}
		fs.wrote(n)
	}
	const (
		fattrAtime    = 1 << 4
		fattrMtime    = 1 << 5
		fattrAtimeNow = 1 << 7
		fattrMtimeNow = 1 << 8
	)
	if in.Valid&(fattrAtime|fattrMtime) != 0 && !link {
		ts := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
		if in.Valid&fattrAtime != 0 {
			ts[0] = unix.Timespec{Sec: int64(in.Atime), Nsec: int64(in.Atimensec)}
			if in.Valid&fattrAtimeNow != 0 {
				ts[0] = unix.Timespec{Nsec: unix.UTIME_NOW}
			}
		}
		if in.Valid&fattrMtime != 0 {
			ts[1] = unix.Timespec{Sec: int64(in.Mtime), Nsec: int64(in.Mtimensec)}
			if in.Valid&fattrMtimeNow != 0 {
				ts[1] = unix.Timespec{Nsec: unix.UTIME_NOW}
			}
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, procPath(n.fd), ts, 0); err != nil {
			return fuse.ToStatus(err)
		}
	}
	if st, err = statx(n.fd); err != nil {
		return fuse.ToStatus(err)
	}
	fillAttr(&out.Attr, &st)
	out.SetTimeout(fs.ttl())
	return fuse.OK
}

// Readlink implements fuse.RawFileSystem.
func (fs *FS) Readlink(_ <-chan struct{}, h *fuse.InHeader) ([]byte, fuse.Status) {
	n := fs.node(h.NodeId)
	if n == nil {
		return nil, fuse.ENOENT
	}
	buf := make([]byte, unix.PathMax)
	sz, err := unix.Readlinkat(n.fd, "", buf)
	if err != nil {
		return nil, fuse.ToStatus(err)
	}
	return buf[:sz], fuse.OK
}

// Access implements fuse.RawFileSystem; the guest mounts with
// default_permissions, so its kernel checks modes itself.
func (fs *FS) Access(<-chan struct{}, *fuse.AccessIn) fuse.Status { return fuse.OK }

// StatFs implements fuse.RawFileSystem.
func (fs *FS) StatFs(_ <-chan struct{}, h *fuse.InHeader, out *fuse.StatfsOut) fuse.Status {
	n := fs.node(h.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(n.fd, &st); err != nil {
		return fuse.ToStatus(err)
	}
	out.Blocks, out.Bfree, out.Bavail = st.Blocks, st.Bfree, st.Bavail
	out.Files, out.Ffree = st.Files, st.Ffree
	out.Bsize, out.NameLen, out.Frsize = uint32(st.Bsize), uint32(st.Namelen), uint32(st.Frsize)
	return fuse.OK
}

// Extended attributes go through the node's magic link, which names the
// file itself (never a path the guest chose).

// GetXAttr implements fuse.RawFileSystem.
func (fs *FS) GetXAttr(_ <-chan struct{}, h *fuse.InHeader, attr string, dest []byte) (uint32, fuse.Status) {
	n := fs.node(h.NodeId)
	if n == nil {
		return 0, fuse.ENOENT
	}
	sz, err := unix.Getxattr(procPath(n.fd), attr, dest)
	return uint32(max(sz, 0)), fuse.ToStatus(err)
}

// ListXAttr implements fuse.RawFileSystem.
func (fs *FS) ListXAttr(_ <-chan struct{}, h *fuse.InHeader, dest []byte) (uint32, fuse.Status) {
	n := fs.node(h.NodeId)
	if n == nil {
		return 0, fuse.ENOENT
	}
	sz, err := unix.Listxattr(procPath(n.fd), dest)
	return uint32(max(sz, 0)), fuse.ToStatus(err)
}

// SetXAttr implements fuse.RawFileSystem.
func (fs *FS) SetXAttr(_ <-chan struct{}, in *fuse.SetXAttrIn, attr string, data []byte) fuse.Status {
	n := fs.node(in.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	if n.ro {
		return fuse.EROFS
	}
	return fuse.ToStatus(unix.Setxattr(procPath(n.fd), attr, data, int(in.Flags)))
}

// RemoveXAttr implements fuse.RawFileSystem.
func (fs *FS) RemoveXAttr(_ <-chan struct{}, h *fuse.InHeader, attr string) fuse.Status {
	n := fs.node(h.NodeId)
	if n == nil {
		return fuse.ENOENT
	}
	if n.ro {
		return fuse.EROFS
	}
	return fuse.ToStatus(unix.Removexattr(procPath(n.fd), attr))
}

// ---- helpers ----

func procPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "/")
}

func openBeneath(dirfd int, name string, flags uint64, mode uint64) (int, error) {
	if !validName(name) {
		return -1, unix.EINVAL
	}
	return unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    mode,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func statx(fd int) (unix.Statx_t, error) {
	var st unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_SYNC_AS_STAT,
		unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st)
	return st, err
}

func keyOf(st *unix.Statx_t) nodeKey { return nodeKey{mnt: st.Mnt_id, ino: st.Ino} }

func fillAttr(a *fuse.Attr, st *unix.Statx_t) {
	a.Ino = st.Ino
	a.Size = st.Size
	a.Blocks = st.Blocks
	a.Atime, a.Atimensec = uint64(st.Atime.Sec), st.Atime.Nsec
	a.Mtime, a.Mtimensec = uint64(st.Mtime.Sec), st.Mtime.Nsec
	a.Ctime, a.Ctimensec = uint64(st.Ctime.Sec), st.Ctime.Nsec
	a.Mode = uint32(st.Mode)
	a.Nlink = st.Nlink
	a.Uid, a.Gid = st.Uid, st.Gid
	a.Rdev = uint32(unix.Mkdev(st.Rdev_major, st.Rdev_minor))
	a.Blksize = st.Blksize
}

func errno(err error) syscall.Errno {
	if e, ok := err.(syscall.Errno); ok {
		return e
	}
	return syscall.EIO
}

// ioFD is node n's fd for reading, or for writing (O_RDWR), opened on first
// use through the magic link: nothing is resolved by name.
func (fs *FS) ioFD(n *node, write bool) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if n.fd < 0 {
		return -1, unix.EBADF
	}
	if write {
		if n.ro {
			return -1, unix.EROFS
		}
		if n.wfd < 0 {
			fd, err := unix.Open(procPath(n.fd), unix.O_RDWR|unix.O_CLOEXEC, 0)
			if err != nil {
				return -1, err
			}
			n.wfd = fd
		}
		return n.wfd, nil
	}
	if n.rfd < 0 {
		if n.wfd >= 0 {
			return n.wfd, nil
		}
		fd, err := unix.Open(procPath(n.fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, err
		}
		n.rfd = fd
	}
	return n.rfd, nil
}

// wrote notes that the guest changed n: the watcher's host event for it is
// the guest's own, its cache already right.
func (fs *FS) wrote(n *node) {
	fs.mu.Lock()
	n.lastWrite = time.Now()
	fs.mu.Unlock()
}
