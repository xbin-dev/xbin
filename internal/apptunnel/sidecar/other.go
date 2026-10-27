//go:build !linux

package sidecar

import (
	"errors"
	"io"
	"net"
	"os/exec"
)

// The iroh sidecar is Linux-only (its sandbox and the factory are).

var errNotLinux = errors.New("app tunnel: the iroh sidecar runs on Linux only")

type launched struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
	fac            interface {
		Accept() (net.Conn, error)
		Close() error
	}
	sandboxed, execFailed bool
}

func (o Options) openExe(string) (*exe, error) { return nil, errNotLinux }

func (s *Supervisor) launch(*exe, bool) (launched, error) { return launched{}, errNotLinux }

func (launched) cleanup() {}

func signalTerm(c *exec.Cmd) error { return c.Process.Kill() }
