package main

import (
	"errors"
	"os"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/host"
)

// cmdVMHost is `bx __vm-host <spec>`: PID 1 of a VM sandbox's namespace
// sandbox, which boots Firecracker and bridges the guest session to its own
// stdio (internal/sandbox/vm/host). Not for humans — xbind runs it.
func cmdVMHost(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bx __vm-host <spec>")
	}
	os.Exit(host.Main(args[0]))
	return nil
}
