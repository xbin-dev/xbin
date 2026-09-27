//go:build linux

package guest

import (
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Backends (plans/vm-sandbox.md): their sockets live on guest-local dirs at
// the host paths (Config.Local), and the agent bridges them to the host —
// xbind's gateway into the guest (here), the backend's listen socket out of
// it (agentcore: Exec.Listen and "listen" connections).

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
				agentcore.Splice(c, os.NewFile(uintptr(fd), "gateway"))
			}()
		}
	}()
	return nil
}
