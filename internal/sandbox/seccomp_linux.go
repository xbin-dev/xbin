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

// denyProgram builds a classic-BPF seccomp program: EPERM every syscall in
// deny, and (when mountMove) additionally EPERM mount(2) only if its flags carry
// MS_MOVE — everything else allowed. A syscall on any *other* arch (e.g. the
// i386 compat ABI on amd64, whose numbers differ) is killed, so the block can't
// be bypassed through a different ABI. Kept separate from installation so it can
// be exercised in a userspace BPF VM (seccomp_test.go) without a real sandbox.
func denyProgram(arch uint32, deny []uint32, mountMove bool) []bpf.Instruction {
	// Fixed head: [0]load arch [1]arch==native?skip kill [2]RET kill [3]load nr.
	// Then one JumpIf per denied nr, then the optional mount-MS_MOVE block, then
	// BLOCK (RET EPERM) and ALLOW (RET allow). Compute the two tail indices so
	// every jump lands correctly regardless of len(deny).
	// Tail indices differ between the two shapes so the fall-through after the
	// deny checks lands on the right verdict: a plain deny-list must fall
	// through to ALLOW (deny only on an explicit match), while the mount guard
	// falls through the deny checks into its mount special, which ends at BLOCK.
	base := 4 + len(deny) // first index after the deny checks
	var idxBlock, idxAllow int
	if mountMove {
		idxBlock, idxAllow = base+4, base+5 // …special(4), BLOCK, ALLOW
	} else {
		idxAllow, idxBlock = base, base+1 // …ALLOW (fall-through), BLOCK
	}
	jmp := func(from, to int) uint8 { return uint8(to - from - 1) }

	prog := []bpf.Instruction{
		bpf.LoadAbsolute{Off: scOffArch, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch, SkipTrue: 1},
		bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_KILL_PROCESS)},
		bpf.LoadAbsolute{Off: scOffNR, Size: 4},
	}
	for i, nr := range deny {
		prog = append(prog, bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipTrue: jmp(4+i, idxBlock)})
	}
	if mountMove {
		m := base // index of the mount check
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(unix.SYS_MOUNT), SkipFalse: jmp(m, idxAllow)}, // not mount → allow
			bpf.LoadAbsolute{Off: scOffMountFlags, Size: 4},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: uint32(unix.MS_MOVE)},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: jmp(m+3, idxAllow)}, // no MS_MOVE → allow; else fall to BLOCK
			bpf.RetConstant{Val: retErrnoEPERM},                                   // idxBlock
			bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_ALLOW)},                  // idxAllow
		)
		return prog
	}
	return append(prog,
		bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_ALLOW)}, // idxAllow (deny-check fall-through)
		bpf.RetConstant{Val: retErrnoEPERM},                  // idxBlock
	)
}

// mountGuardProgram (terminals): deny the mount teardown/move syscalls and
// mount(MS_MOVE) so the shell can't peel a secret mask; plain mount stays.
func mountGuardProgram(arch uint32) []bpf.Instruction {
	return denyProgram(arch, []uint32{
		uint32(unix.SYS_UMOUNT2), uint32(unix.SYS_MOVE_MOUNT), uint32(unix.SYS_OPEN_TREE),
	}, true)
}

// backendDeny is the block-list for tile backends: privileged / system-damaging
// syscalls no element server needs (mount family, module/kexec/reboot, device
// nodes, ptrace, bpf, keyrings, time/quota/accounting). Defense in depth on top
// of the dropped capabilities — a buggy or wedged tile can't reach past its own
// process. All present on the arches nativeAuditArch supports.
func backendDeny() []uint32 {
	return []uint32{
		uint32(unix.SYS_MOUNT), uint32(unix.SYS_UMOUNT2), uint32(unix.SYS_MOVE_MOUNT),
		uint32(unix.SYS_OPEN_TREE), uint32(unix.SYS_FSOPEN), uint32(unix.SYS_FSMOUNT),
		uint32(unix.SYS_FSCONFIG), uint32(unix.SYS_FSPICK), uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_CHROOT), uint32(unix.SYS_SETNS),
		uint32(unix.SYS_INIT_MODULE), uint32(unix.SYS_FINIT_MODULE), uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_KEXEC_LOAD), uint32(unix.SYS_KEXEC_FILE_LOAD), uint32(unix.SYS_REBOOT),
		uint32(unix.SYS_SWAPON), uint32(unix.SYS_SWAPOFF), uint32(unix.SYS_MKNODAT),
		uint32(unix.SYS_PTRACE), uint32(unix.SYS_BPF), uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_KEYCTL), uint32(unix.SYS_ADD_KEY), uint32(unix.SYS_REQUEST_KEY),
		uint32(unix.SYS_ACCT), uint32(unix.SYS_QUOTACTL),
		uint32(unix.SYS_SETTIMEOFDAY), uint32(unix.SYS_ADJTIMEX), uint32(unix.SYS_CLOCK_SETTIME),
	}
}

// installMountGuard / installBackendSeccomp install the respective filter on the
// calling thread (inherited across execve). Require no_new_privs (init sets it).
// No-op on architectures without a known syscall table.
func installMountGuard() error { return installFilter(mountGuardProgram) }
func installBackendSeccomp() error {
	return installFilter(func(arch uint32) []bpf.Instruction { return denyProgram(arch, backendDeny(), false) })
}

func installFilter(build func(arch uint32) []bpf.Instruction) error {
	arch, ok := nativeAuditArch()
	if !ok {
		return nil
	}
	raw, err := bpf.Assemble(build(arch))
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

// dropAllCaps makes a backend truly unprivileged: it drops every capability
// from the bounding set (so none can be regained across execve) and clears the
// effective/permitted/inheritable sets. A tile backend needs no capabilities
// (it serves on a socket as its own uid), so this is pure hardening. The
// bounding drops run first, while CAP_SETPCAP is still held.
func dropAllCaps() error {
	for c := 0; c <= unix.CAP_LAST_CAP; c++ {
		_ = unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0)
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData // all zero: no eff/perm/inh
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capset(clear): %w", err)
	}
	return nil
}
