//go:build !linux

package sandbox

import (
	"net"
	"os"
	"os/exec"
)

// Launch is unsupported off Linux.
func Launch(*Spec) (*exec.Cmd, *Handle, error) { return nil, &Handle{}, ErrUnsupported }

// RecvTUN is unsupported off Linux.
func (h *Handle) RecvTUN() (int, error) { return -1, ErrUnsupported }

// RunInit is never reached off Linux (the __sandbox-init subcommand is only
// dispatched when Launch could have created the namespaces).
func RunInit(string) { panic("sandbox: RunInit called on non-linux") }

// Available reports whether OS sandboxing can be used here.
func Available() bool { return false }

// IDMapStatus is unsupported off Linux (no user namespaces).
func IDMapStatus(int, int) (bool, string) { return false, "user namespaces are Linux-only" }

// DetectProtections reports no terminal-hardening off Linux.
func DetectProtections() Protections { return Protections{} }

// Factory is a sandbox agent's connection factory; Linux-only.
type Factory struct{}

// NewFactory is unsupported off Linux.
func NewFactory() (*Factory, *os.File, error) { return nil, nil, ErrUnsupported }

// Dial is unsupported off Linux.
func (*Factory) Dial() (net.Conn, error) { return nil, ErrUnsupported }

// Close is a no-op off Linux.
func (*Factory) Close() error { return nil }

// AcceptFrom is unsupported off Linux.
func AcceptFrom(*os.File) (net.Conn, error) { return nil, ErrUnsupported }
