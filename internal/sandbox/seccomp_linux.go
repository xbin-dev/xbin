//go:build linux

package sandbox

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The mount-guard seccomp filter (plans/terminal-tokens.md, docs/isolation.md).
//
// A terminal shell is uid 0 in its own user namespace and keeps CAP_SYS_ADMIN
// (so `apt`, nested namespaces, profiling still work). That cap would let it
// `umount` the empty-tmpfs masks that hide the workspace's secrets (.xbin,
// data, other homes) and read what's underneath. seccomp fixes this without
// dropping the cap: the filter follows the *process* across execve and every
// namespace, so blocking the mount-teardown/move syscalls closes the reveal
// even after unshare(CLONE_NEWNS|CLONE_NEWUSER).
//
// Blocked (→ EPERM): umount2, move_mount, open_tree, and mount(MS_MOVE) — the
// four ways to remove or relocate a mask mount. Plain mount(2) (bind, new fs),
// unshare, clone, and pivot_root stay allowed, so most nested-container work
// still functions; only unmounting/moving mounts is denied (see the doc for
// the collateral). This is a hardening layer, not a complete tenant boundary —
// per-tenant uids remain the real fix.

// seccomp_data field offsets (linux/seccomp.h): nr@0, arch@4, args[0..5]@16+8n.
// The mount(2) flags argument is args[3], whose low 32 bits sit at offset 40
// (little-endian) — enough for MS_MOVE.
const (
	scOffNR         = 0
	scOffArch       = 4
	scOffMountFlags = 16 + 3*8
)

// retErrnoEPERM is SECCOMP_RET_ERRNO with EPERM in the data bits — deny the
// call but let the shell live (a KILL would nuke the terminal on a stray
// `fusermount`/`umount`).
const retErrnoEPERM = uint32(unix.SECCOMP_RET_ERRNO | (uint32(unix.EPERM) & unix.SECCOMP_RET_DATA))

// nativeAuditArch is the AUDIT_ARCH_* token for the running architecture, or
// (0,false) if we don't have a syscall table for it — in which case the guard
// is skipped rather than risk a wrong filter (the masks still give hygiene).
func nativeAuditArch() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return uint32(unix.AUDIT_ARCH_X86_64), true
	case "arm64":
		return uint32(unix.AUDIT_ARCH_AARCH64), true
	default:
		return 0, false
	}
}

// mountGuardProgram builds the classic-BPF seccomp program for the given audit
// arch. Kept separate from installation so it can be exercised in a userspace
// BPF VM (seccomp_test.go) without a real sandbox. A syscall on any *other*
// arch (e.g. the i386 compat ABI on amd64, whose numbers differ) is killed, so
// the block can't be bypassed through a different ABI.
func mountGuardProgram(arch uint32) []bpf.Instruction {
	// Layout (indices matter for the jump offsets):
	//  0 load arch
	//  1 arch==native ? skip the kill : fall through
	//  2 RET kill (foreign arch)
	//  3 load nr
	//  4 nr==umount2    ? -> BLOCK
	//  5 nr==move_mount ? -> BLOCK
	//  6 nr==open_tree  ? -> BLOCK
	//  7 nr==mount      ? fall through : -> ALLOW
	//  8 load mount flags
	//  9 A &= MS_MOVE
	// 10 (flags&MS_MOVE)==0 ? -> ALLOW : fall through
	// 11 BLOCK: RET errno(EPERM)
	// 12 ALLOW: RET allow
	const idxBlock, idxAllow = 11, 12
	jmpTo := func(from, to int) uint8 { return uint8(to - from - 1) }
	return []bpf.Instruction{
		/* 0 */ bpf.LoadAbsolute{Off: scOffArch, Size: 4},
		/* 1 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch, SkipTrue: 1},
		/* 2 */ bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_KILL_PROCESS)},
		/* 3 */ bpf.LoadAbsolute{Off: scOffNR, Size: 4},
		/* 4 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(unix.SYS_UMOUNT2), SkipTrue: jmpTo(4, idxBlock)},
		/* 5 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(unix.SYS_MOVE_MOUNT), SkipTrue: jmpTo(5, idxBlock)},
		/* 6 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(unix.SYS_OPEN_TREE), SkipTrue: jmpTo(6, idxBlock)},
		/* 7 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(unix.SYS_MOUNT), SkipFalse: jmpTo(7, idxAllow)},
		/* 8 */ bpf.LoadAbsolute{Off: scOffMountFlags, Size: 4},
		/* 9 */ bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: uint32(unix.MS_MOVE)},
		/* 10 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: jmpTo(10, idxAllow)},
		/* 11 */ bpf.RetConstant{Val: retErrnoEPERM},
		/* 12 */ bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_ALLOW)},
	}
}

// installMountGuard assembles and installs the filter on the calling thread
// (and, by inheritance, everything it execs). Requires no_new_privs, which the
// init sets before calling this. A no-op on architectures we don't have a
// syscall table for.
func installMountGuard() error {
	arch, ok := nativeAuditArch()
	if !ok {
		return nil
	}
	raw, err := bpf.Assemble(mountGuardProgram(arch))
	if err != nil {
		return fmt.Errorf("assemble seccomp: %w", err)
	}
	filter := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		filter[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}
	prog := &unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(prog)), 0, 0); err != nil {
		return fmt.Errorf("PR_SET_SECCOMP: %w", err)
	}
	return nil
}
