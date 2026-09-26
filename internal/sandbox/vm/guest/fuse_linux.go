//go:build linux

package guest

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// The host's binds reach the guest as FUSE filesystems whose server runs in
// the host shim (internal/sandbox/vm/fusefs): the agent mounts /dev/fuse and
// pumps its messages over a vsock stream, each framed by its own length
// field. The kernel caches entries, attributes, listings and pages; the host
// invalidates them when something outside the guest changes a file.

// fuseBuf holds any request the kernel may hand us (max_write + headers).
const fuseBuf = 256 << 10

// mountFiles mounts one export at its host path inside the new root.
func mountFiles(m proto.Mount) error {
	p := path.Clean("/" + m.Path)
	dst := filepath.Join(newRoot, p)
	rootmode := "40000"
	if m.File {
		rootmode = "100000"
		if err := unixMkdirAll(filepath.Dir(dst)); err != nil {
			return err
		}
		if fd, err := unix.Open(dst, unix.O_CREAT|unix.O_RDONLY|unix.O_CLOEXEC, 0o644); err == nil {
			unix.Close(fd)
		}
	} else if err := unixMkdirAll(dst); err != nil {
		return err
	}
	vs, err := dialHost(proto.FilesPort)
	if err != nil {
		return fmt.Errorf("dial the file server: %w", err)
	}
	hello, _ := json.Marshal(proto.FilesHello{Path: p})
	if err := writeAll(vs, append(hello, '\n')); err != nil {
		unix.Close(vs)
		return err
	}
	dev, err := unix.Open("/dev/fuse", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(vs)
		return fmt.Errorf("/dev/fuse: %w", err)
	}
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if m.RO {
		flags |= unix.MS_RDONLY
	}
	opts := fmt.Sprintf("fd=%d,rootmode=%s,user_id=0,group_id=0,allow_other,default_permissions", dev, rootmode)
	if err := unix.Mount("xbinfs", dst, "fuse.xbinfs", flags, opts); err != nil {
		unix.Close(dev)
		unix.Close(vs)
		return err
	}
	go pumpRequests(dev, vs)
	go pumpReplies(dev, vs)
	return nil
}

// pumpRequests forwards each kernel request to the host as it is read; the
// host answers them concurrently.
func pumpRequests(dev, vs int) {
	buf := make([]byte, fuseBuf)
	for {
		n, err := unix.Read(dev, buf)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil || n <= 0 {
			return // ENODEV: unmounted
		}
		if writeAll(vs, buf[:n]) != nil {
			return
		}
	}
}

// pumpReplies writes the host's replies to the kernel. Notifications (unique
// 0) go through their own writer: an invalidation can wait on a directory
// lock that an in-flight request holds, and that request's reply must not
// be stuck behind it.
func pumpReplies(dev, vs int) {
	notes := newNoteQueue()
	go func() {
		for {
			msg, ok := notes.pop()
			if !ok {
				return
			}
			_, _ = unix.Write(dev, msg)
		}
	}()
	defer notes.close()
	hdr := make([]byte, 16)
	for {
		if readFull(vs, hdr[:4]) != nil {
			return
		}
		n := int(binary.LittleEndian.Uint32(hdr[:4]))
		if n < 16 || n > fuseBuf {
			return
		}
		msg := make([]byte, n)
		copy(msg, hdr[:4])
		if readFull(vs, msg[4:]) != nil {
			return
		}
		if binary.LittleEndian.Uint64(msg[8:16]) == 0 {
			notes.push(msg)
			continue
		}
		if _, err := unix.Write(dev, msg); err == unix.ENODEV {
			return
		}
	}
}

// noteQueue is an unbounded FIFO: the reply pump must never block on it.
type noteQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	q      [][]byte
	closed bool
}

func newNoteQueue() *noteQueue {
	n := &noteQueue{}
	n.cond = sync.NewCond(&n.mu)
	return n
}

func (n *noteQueue) push(b []byte) {
	n.mu.Lock()
	n.q = append(n.q, b)
	n.mu.Unlock()
	n.cond.Signal()
}

func (n *noteQueue) pop() ([]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for len(n.q) == 0 && !n.closed {
		n.cond.Wait()
	}
	if len(n.q) == 0 {
		return nil, false
	}
	b := n.q[0]
	n.q = n.q[1:]
	return b, true
}

func (n *noteQueue) close() {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.cond.Broadcast()
}

func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func readFull(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Read(fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("eof")
		}
		b = b[n:]
	}
	return nil
}

func unixMkdirAll(p string) error {
	if p == "/" || p == "." {
		return nil
	}
	var st unix.Stat_t
	if unix.Stat(p, &st) == nil {
		return nil
	}
	if err := unixMkdirAll(filepath.Dir(p)); err != nil {
		return err
	}
	if err := unix.Mkdir(p, 0o755); err != nil && err != unix.EEXIST {
		return err
	}
	return nil
}
