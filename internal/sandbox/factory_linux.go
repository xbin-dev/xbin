//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Factory is xbind's end of a sandbox agent's connection factory
// (plans/tile-sandbox-runtime.md §2.1): a SOCK_SEQPACKET socketpair whose
// other end the agent holds (Spec.Agent). Every connection xbind makes to
// the agent is a fresh SOCK_STREAM socketpair, one end of which travels over
// the factory with SCM_RIGHTS. There is no path, so no stale socket and no
// 108-byte limit; and the factory's EOF is how the agent learns that xbind
// is gone. Safe for concurrent use.
type Factory struct {
	f *os.File
}

// NewFactory makes a connection factory: xbind keeps the Factory; child
// goes to the sandbox as Spec.Agent (and xbind closes its copy of it once
// the sandbox has started, Handle.Started). Both ends are close-on-exec in
// xbind, so no other process xbind starts ever holds one: the agent's EOF
// means xbind itself is gone.
func NewFactory() (xbind *Factory, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("factory socketpair: %w", err)
	}
	return &Factory{f: os.NewFile(uintptr(fds[0]), "sbx-factory")}, os.NewFile(uintptr(fds[1]), "sbx-factory-child"), nil
}

// Dial makes one connection to the agent: a fresh stream socketpair, one end
// sent over the factory, the other returned. The send never blocks
// (MSG_DONTWAIT): an agent that has stopped accepting fills the factory's
// queue, and that is an error, not a hang. An agent that is gone (the child
// end closed everywhere) is an error too.
func (f *Factory) Dial() (net.Conn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("factory dial: %w", err)
	}
	keep, send := fds[0], fds[1]
	err = f.send(send)
	unix.Close(send) // in flight, or refused: either way not ours any more
	if err != nil {
		unix.Close(keep)
		return nil, err
	}
	return fileConn(keep, "sbx-conn")
}

func (f *Factory) send(fd int) error {
	rc, err := f.f.SyscallConn()
	if err != nil {
		return fmt.Errorf("factory dial: %w", err) // closed
	}
	var serr error
	// Control holds the fd open for the call: a concurrent Close waits.
	if err := rc.Control(func(sfd uintptr) {
		for {
			serr = unix.Sendmsg(int(sfd), []byte{'c'}, unix.UnixRights(fd), nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
			if serr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		return fmt.Errorf("factory dial: %w", err)
	}
	switch {
	case serr == nil:
		return nil
	case errors.Is(serr, unix.EAGAIN):
		return fmt.Errorf("factory dial: the agent is not accepting (queue full): %w", serr)
	default:
		return fmt.Errorf("factory dial: %w", serr)
	}
}

// Close closes xbind's end: the agent's AcceptFrom then drains what is queued
// and returns io.EOF. Dial fails afterwards.
func (f *Factory) Close() error { return f.f.Close() }

// AcceptFrom is the agent's (or the shim's) side: it waits for the next
// connection xbind dials over the factory end f and returns it. io.EOF means
// xbind's end is closed — xbind is gone. The received socket is close-on-exec
// from the moment it arrives (MSG_CMSG_CLOEXEC), so a process the agent is
// starting at that moment never inherits it. f may be blocking (as a child
// inherits it) or non-blocking.
func AcceptFrom(f *os.File) (net.Conn, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	for {
		var (
			got   []int
			n     int
			opErr error
		)
		err := rc.Read(func(sfd uintptr) bool {
			buf := make([]byte, 1)
			oob := make([]byte, unix.CmsgSpace(4*4)) // room for strays, closed below
			var oobn int
			var e error
			for {
				n, oobn, _, _, e = unix.Recvmsg(int(sfd), buf, oob, unix.MSG_CMSG_CLOEXEC)
				if e != unix.EINTR {
					break
				}
			}
			if e == unix.EAGAIN {
				return false // non-blocking: wait for readability
			}
			if e != nil {
				opErr = e
				return true
			}
			if scms, perr := unix.ParseSocketControlMessage(oob[:oobn]); perr == nil {
				for i := range scms {
					if fds, rerr := unix.ParseUnixRights(&scms[i]); rerr == nil {
						got = append(got, fds...)
					}
				}
			}
			return true
		})
		if err == nil {
			err = opErr
		}
		for _, fd := range got[min(len(got), 1):] {
			unix.Close(fd) // one connection per message; anything more is dropped
		}
		switch {
		case err != nil:
			if len(got) > 0 {
				unix.Close(got[0])
			}
			return nil, err
		case len(got) > 0:
			return fileConn(got[0], "sbx-conn")
		case n == 0:
			return nil, io.EOF // xbind's end is closed
		}
		// a message without a connection: nothing to accept, wait for the next
	}
}

// fileConn wraps a stream socket fd as a net.Conn, taking ownership of fd.
func fileConn(fd int, name string) (net.Conn, error) {
	f := os.NewFile(uintptr(fd), name)
	c, err := net.FileConn(f) // dups (close-on-exec) and registers with the poller
	f.Close()
	return c, err
}
