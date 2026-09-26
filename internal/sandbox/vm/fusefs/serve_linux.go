//go:build linux

package fusefs

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// maxWrite bounds a READ/WRITE payload. A request travels host-side as one
// SOCK_SEQPACKET record, which must fit an unprivileged socket buffer
// (~208 KiB by default), so this stays at the kernel's classic 128 KiB.
const maxWrite = 128 << 10

// maxMsg is the largest FUSE message either way: payload plus headers.
const maxMsg = maxWrite + 4096

// Serve runs fs as one FUSE connection whose kernel end is the guest's
// /dev/fuse on the far side of c. go-fuse's server reads whole requests from
// one end of a SOCK_SEQPACKET socketpair exactly as it would from /dev/fuse
// (the "/dev/fd/N" mountpoint form); this pumps the other end to c, where
// messages are framed by their own length field. Returns when either side
// ends.
func Serve(c net.Conn, fs *FS) error {
	defer c.Close()
	defer fs.Close()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	// The pumps run first: NewServer reads the kernel's INIT synchronously.
	pumped := make(chan struct{})
	go func() { // guest → server: one framed message per record
		defer unix.Shutdown(fds[1], unix.SHUT_WR)
		buf := make([]byte, maxMsg)
		for {
			if _, err := io.ReadFull(c, buf[:4]); err != nil {
				return
			}
			n := int(binary.LittleEndian.Uint32(buf[:4]))
			if n < 4 || n > len(buf) {
				return
			}
			if _, err := io.ReadFull(c, buf[4:n]); err != nil {
				return
			}
			if _, err := unix.Write(fds[1], buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() { // server → guest: replies and notifications, each a whole record
		defer close(pumped)
		buf := make([]byte, maxMsg)
		for {
			n, err := unix.Read(fds[1], buf)
			if err == unix.EINTR {
				continue
			}
			if err != nil || n <= 0 {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	debug := os.Getenv("XBIN_VM_FUSE_DEBUG") != ""
	logger := log.New(io.Discard, "", 0) // its notes land in the terminal otherwise
	if debug {
		logger = log.New(os.Stderr, "fuse ", log.Lmicroseconds)
	}
	srv, err := fuse.NewServer(fs, fmt.Sprintf("/dev/fd/%d", fds[0]), &fuse.MountOptions{
		MaxWrite:             maxWrite,
		MaxReadAhead:         maxWrite,
		MaxBackground:        64,
		EnableLocks:          true,
		IgnoreSecurityLabels: true,
		EnableSymlinkCaching: true,
		// No writeback cache: with it the kernel drops a file's block count
		// on every close, so each later stat is a vsock round trip.
		Name:   "xbinfs",
		FsName: fs.root,
		Debug:  debug,
		Logger: logger,
	})
	if err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		<-pumped
		return err
	}
	srv.Serve() // until the guest's side goes away
	unix.Close(fds[1])
	<-pumped
	return nil
}
