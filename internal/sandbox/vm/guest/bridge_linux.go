//go:build linux

package guest

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Backends (plans/vm-sandbox.md): their sockets live on guest-local dirs at
// the host paths (Config.Local), and the agent bridges them to the host —
// xbind's gateway into the guest, the backend's listen socket out of it.

// mountLocal makes each local dir a tmpfs inside the new root.
func mountLocal(dirs []string) error {
	for _, d := range dirs {
		if err := mountAt("tmpfs", filepath.Join(newRoot, filepath.Clean("/"+d)), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return err
		}
	}
	return nil
}

// serveGateway listens on path in the guest and bridges each connection to
// the host's gateway (vsock GatewayPort → the shim's link to gateway.sock).
func serveGateway(path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				fd, err := dialHost(proto.GatewayPort)
				if err != nil {
					c.Close()
					return
				}
				splice(c, os.NewFile(uintptr(fd), "gateway"))
			}()
		}
	}()
	return nil
}

// awaitListen polls until path accepts connections (the backend is up),
// then reports "listening" — the host exposes its side only then, so xbind's
// health check means what it says. Gives up when stop closes.
func (a *agent) awaitListen(session int, path string, stop <-chan struct{}) {
	for {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			a.send(proto.Msg{Op: "listening", Session: session})
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// bridgeListen connects a host "listen" connection to the session's socket.
func (a *agent) bridgeListen(h proto.Hello, c io.ReadWriteCloser) {
	s := a.session(h.Session)
	if s == nil || s.ex.Listen == "" {
		c.Close()
		return
	}
	g, err := net.Dial("unix", s.ex.Listen)
	if err != nil {
		c.Close()
		return
	}
	splice(g, c)
}

// splice copies both ways until either side ends, then closes both.
func splice(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst, src io.ReadWriteCloser) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}
