//go:build linux

// Package p9fs serves a VM sandbox's host binds to the guest over 9P2000.L
// (plans/vm-sandbox.md). It runs in the host shim, inside the namespace
// sandbox that also holds Firecracker, so its view of the filesystem is
// exactly the bind set a namespace-mode workload would see: read-only binds
// fail with EROFS from the host kernel, masks read as empty directories, and
// nothing outside the binds exists.
//
// That mount namespace also holds the VM's own plumbing (the Firecracker API
// socket, the vsock sockets, the kernel and images), so the server never
// resolves a guest-supplied path: the guest sees a virtual root whose only
// children lead to the exports, and every step below an export is one
// openat2 from the parent's O_PATH fd with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|
// RESOLVE_NO_MAGICLINKS (no RESOLVE_NO_XDEV: the walk must cross into nested
// binds and masks, as a namespace-mode process would). Symlinks are returned
// as symlinks; the guest kernel resolves them against its own tree.
package p9fs

import (
	"fmt"
	"net"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// Export is one host path served to the guest.
type Export struct {
	Path string // absolute, as seen by the server (== the guest mount point)
	RO   bool   // refuse writes up front (the host mount enforces it anyway)
}

// Server serves a fixed set of exports.
type Server struct {
	root   *vnode
	byPath map[string]*export // every export, nested ones included
	mu     sync.Mutex
	qids   map[devIno]uint64
}

type devIno struct{ dev, ino uint64 }

// vnode is one node of the virtual tree leading to the exports.
type vnode struct {
	id       int // distinct qid path within the virtual tree
	name     string
	children map[string]*vnode
	exp      *export // non-nil: this node is an export root
}

type export struct {
	Export
	fd int // O_PATH fd of the export root, opened at New
}

// New opens every export root. An export nested inside another is reached by
// walking through its parent (the host mount table carries the walk into it)
// but keeps its own read-only flag: a writable tile dir under the read-only
// workspace stays writable.
func New(exports []Export) (*Server, error) {
	s := &Server{root: &vnode{children: map[string]*vnode{}}, byPath: map[string]*export{}, qids: map[devIno]uint64{}}
	ids := 0
	next := func() int { ids++; return ids }
	sorted := append([]Export(nil), exports...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, e := range sorted {
		p := path.Clean("/" + e.Path)
		if p == "/" {
			return nil, fmt.Errorf("p9fs: refusing to export /")
		}
		n := s.root
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		nested := false
		for _, part := range parts[:len(parts)-1] {
			c := n.children[part]
			if c == nil {
				c = &vnode{id: next(), name: part, children: map[string]*vnode{}}
				n.children[part] = c
			}
			if c.exp != nil {
				nested = true // reached through its parent export
				break
			}
			n = c
		}
		if _, dup := s.byPath[p]; dup {
			continue
		}
		if nested {
			s.byPath[p] = &export{Export: Export{Path: p, RO: e.RO}, fd: -1}
			continue
		}
		last := parts[len(parts)-1]
		fd, err := unix.Open(p, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("p9fs: export %s: %w", p, err)
		}
		x := &export{Export: Export{Path: p, RO: e.RO}, fd: fd}
		s.byPath[p] = x
		n.children[last] = &vnode{id: next(), name: last, exp: x}
	}
	return s, nil
}

func (n *vnode) sortedNames() []string {
	names := make([]string, 0, len(n.children))
	for k := range n.children {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Close releases the export root fds.
func (s *Server) Close() {
	var walk func(*vnode)
	walk = func(n *vnode) {
		if n.exp != nil && n.exp.fd >= 0 {
			unix.Close(n.exp.fd)
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(s.root)
}

// Serve handles one 9P connection until it closes.
func (s *Server) Serve(c net.Conn) error {
	defer c.Close()
	return p9.NewServer(s).Handle(c, c)
}

// Attach implements p9.Attacher: the virtual root. The server walks the
// attach name from here, so a guest can only ever reach an export.
func (s *Server) Attach() (p9.File, error) {
	return &file{srv: s, fd: -1, open: -1, v: s.root}, nil
}

func (s *Server) qidPath(dev, ino uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := devIno{dev, ino}
	if p, ok := s.qids[k]; ok {
		return p
	}
	p := uint64(len(s.qids)) + 2 // 1 is the virtual root
	s.qids[k] = p
	return p
}

func (s *Server) qidOf(st *unix.Stat_t) p9.QID {
	return p9.QID{Type: p9.FileMode(st.Mode).QIDType(), Path: s.qidPath(st.Dev, st.Ino)}
}

// procPath names an open fd's file through its magic link.
func procPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

func openBeneath(dirfd int, name string, flags uint64, mode uint64) (int, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return -1, unix.EINVAL
	}
	return unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    mode,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return unix.EINVAL
	}
	return nil
}

// dotlFlags maps 9P2000.L open flags to host open flags: the access mode plus
// the few behavioral bits worth honoring server-side (the rest — O_CREAT,
// O_TRUNC, O_DIRECTORY, … — the guest kernel handles or sends separately).
func dotlFlags(f p9.OpenFlags) int {
	const (
		dotlAppend = 0o2000
		dotlDsync  = 0o10000
		dotlSync   = 0o4000000
	)
	fl := int(f & p9.OpenFlagsModeMask)
	if f&dotlAppend != 0 {
		fl |= unix.O_APPEND
	}
	if f&dotlDsync != 0 {
		fl |= unix.O_DSYNC
	}
	if f&dotlSync == dotlSync {
		fl |= unix.O_SYNC
	}
	return fl
}

func attrOf(st *unix.Stat_t) (p9.AttrMask, p9.Attr) {
	return p9.AttrMaskAll, p9.Attr{
		Mode:             p9.FileMode(st.Mode),
		UID:              p9.UID(st.Uid),
		GID:              p9.GID(st.Gid),
		NLink:            p9.NLink(st.Nlink),
		RDev:             p9.Dev(st.Rdev),
		Size:             uint64(st.Size),
		BlockSize:        uint64(st.Blksize),
		Blocks:           uint64(st.Blocks),
		ATimeSeconds:     uint64(st.Atim.Sec),
		ATimeNanoSeconds: uint64(st.Atim.Nsec),
		MTimeSeconds:     uint64(st.Mtim.Sec),
		MTimeNanoSeconds: uint64(st.Mtim.Nsec),
		CTimeSeconds:     uint64(st.Ctim.Sec),
		CTimeNanoSeconds: uint64(st.Ctim.Nsec),
	}
}

func fstat(fd int) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(fd, "", &st, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

// virtualAttr is a read-only directory's attributes, for the virtual tree.
func virtualAttr() (p9.QID, p9.AttrMask, p9.Attr) {
	return p9.QID{Type: p9.TypeDir, Path: 1}, p9.AttrMaskAll, p9.Attr{Mode: p9.ModeDirectory | 0o555, NLink: 2, BlockSize: 4096}
}
