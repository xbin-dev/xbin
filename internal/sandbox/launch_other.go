//go:build !linux

package sandbox

import "os/exec"

// Launch is unsupported off Linux.
func Launch(*Spec) (*exec.Cmd, func(), error) { return nil, func() {}, ErrUnsupported }

// RunInit is never reached off Linux (the __sandbox-init subcommand is only
// dispatched when Launch could have created the namespaces).
func RunInit(string) { panic("sandbox: RunInit called on non-linux") }

// Available reports whether OS sandboxing can be used here.
func Available() bool { return false }
