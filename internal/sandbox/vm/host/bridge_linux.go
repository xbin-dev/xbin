//go:build linux

package host

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Backend sockets (plans/vm-sandbox.md): the guest reaches xbind's gateway
// through Firecracker's host-side vsock socket for GatewayPort, which is a
// link to gateway.sock — only the links set up here exist, so a guest dialing
// any other port finds nothing. The backend's own socket is served on the
// host path once the guest reports its process listening.

// linkGateway makes <uds>_<GatewayPort> the host gateway socket.
func (s *shim) linkGateway() error {
	if s.hs.Gateway == "" {
		return nil
	}
	return os.Symlink(s.hs.Gateway, filepath.Join(s.hs.RunDir, fmt.Sprintf("v.sock_%d", proto.GatewayPort)))
}

// serveListen exposes the guest process's socket at the host path: each
// connection becomes a "listen" connection to the agent.
func (s *shim) serveListen() error {
	_ = os.Remove(s.hs.Listen)
	ln, err := net.Listen("unix", s.hs.Listen)
	if err != nil {
		return err
	}
	uds := filepath.Join(s.hs.RunDir, "v.sock")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				g, err := proto.DialVsock(uds, proto.AgentPort, 5*time.Second)
				if err != nil {
					c.Close()
					return
				}
				b, _ := json.Marshal(proto.Hello{Kind: "listen", Session: 1})
				if _, err := g.Write(append(b, '\n')); err != nil {
					c.Close()
					g.Close()
					return
				}
				splice(c, g)
			}()
		}
	}()
	return nil
}

// splice copies both ways until either side ends, then closes both.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
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
