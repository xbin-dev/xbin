//go:build !linux

package sandbox

import "os/exec"

// Launch is unsupported off Linux.
func Launch(*Spec) (*exec.Cmd, *Handle, error) { return nil, &Handle{}, ErrUnsupported }

// RecvTUN is unsupported off Linux.
func (h *Handle) RecvTUN() (int, error) { return -1, ErrUnsupported }

// RunInit is never reached off Linux (the __sandbox-init subcommand is only
// dispatched when Launch could have created the namespaces).
func RunInit(string) { panic("sandbox: RunInit called on non-linux") }

// Available reports whether OS sandboxing can be used here.
func Available() bool { return false }

// DetectProtections reports no terminal-hardening off Linux.
func DetectProtections() Protections { return Protections{} }
