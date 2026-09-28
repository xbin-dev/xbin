//go:build linux

package sandbox

import (
	"fmt"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The file-caps profile (Spec.FileCaps): xbind's own confined du, rm -rf and
// cp -a of a tree sandboxes wrote — a tile sandbox's upper, a terminal layer
// (internal/confine's Cmd.FSCaps; plans/tile-sandbox-runtime.md §8.3). Such a
// tree holds files of other (sub-)uids, modes their owner locked, whiteouts
// (0:0 char devices), FIFOs and file capabilities, so the tool needs the file
// capabilities to read all of it, copy it with its ownership and remove it.
// It never mounts, traces, loads or builds namespaces: those stay out, as
// for a backend.

// fileCaps is the keep-set: read and write past any mode (DAC_OVERRIDE,
// DAC_READ_SEARCH), own and stamp what it copies (CHOWN, FOWNER, FSETID) and
// keep file capabilities (SETFCAP). Only files whose owners are mapped into
// the sandbox's user namespace are reachable, and only under its binds.
func fileCaps() []int {
	return []int{
		unix.CAP_CHOWN, unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH,
		unix.CAP_FOWNER, unix.CAP_FSETID, unix.CAP_SETFCAP,
	}
}

// fileCapsDeny is the backend block-list minus mknodat, the VM jail's list:
// cp -a recreates whiteouts, FIFOs and sockets (a real device node still
// needs CAP_MKNOD, which is gone).
func fileCapsDeny() []uint32 { return vmDeny() }

// fileCapsLockdown applies the profile. Order as for a VM jail: pin nested
// user namespaces to zero while CAP_SYS_RESOURCE is still held, drop the
// caps, then the filter.
func fileCapsLockdown() error {
	setUserNSLimits()
	if err := dropCapsExcept(fileCaps()); err != nil {
		return fmt.Errorf("file caps: %w", err)
	}
	if err := installFilter(func(arch uint32) []bpf.Instruction { return denyProgram(arch, fileCapsDeny(), false) }); err != nil {
		return fmt.Errorf("file-caps seccomp: %w", err)
	}
	return nil
}
