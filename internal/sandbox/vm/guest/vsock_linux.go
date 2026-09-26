//go:build linux

package guest

import (
	"os"

	"golang.org/x/sys/unix"
)

// vsockListener accepts host-initiated vsock connections.
type vsockListener struct{ fd int }

func listenVsock(port uint32) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Listen(fd, 64); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &vsockListener{fd: fd}, nil
}

// accept blocks (a dedicated goroutine calls it) and returns the connection as
// a pollable *os.File — the net package has no vsock support.
func (l *vsockListener) accept() (*os.File, error) {
	for {
		nfd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(nfd), "vsock"), nil
	}
}

// dialHost connects to a host vsock port (Firecracker forwards it to the
// shim's "<uds>_<port>" listener) and returns the raw blocking fd.
func dialHost(port uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for {
		err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
