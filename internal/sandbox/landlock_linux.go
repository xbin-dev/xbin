//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The terminal read guard (docs/isolation.md) is a second layer under the
// secret masks: even if a mask mount were peeled (a kernel bug, or a reveal
// path the seccomp mount guard doesn't cover), the secret *files* still can't
// be opened for reading. seccomp can't do this — it can't dereference the path
// argument of openat — so we use Landlock, a VFS-level, unprivileged,
// inherited-across-execve filesystem sandbox.
//
// It restricts LANDLOCK_ACCESS_FS_READ_FILE, and grants it on everything the
// shell legitimately reads (the whole rootfs, all tile source, the own $HOME)
// — but NOT on the workspace's .xbin/data/homes. So reading those files'
// *contents* (the owner token, vault, password hashes, other users' agent
// credentials) is denied at the kernel, independent of the mount. Directory
// listing, execution, and writes are untouched (the read-only bind already
// scopes writes), so collateral is near zero. Best-effort: a kernel without
// Landlock is a silent no-op (the masks + mount guard remain).
//
// LANDLOCK_ACCESS_FS_REFER (ABI ≥ 2) MUST be handled too, or the guard silently
// breaks every cross-directory rename/link in the sandbox. Once ANY Landlock
// ruleset is enforced on an ABI-2+ kernel, the kernel denies reparenting a file
// to a different directory with EXDEV UNLESS the ruleset handles REFER and
// grants it on both the source and destination — even if REFER is otherwise
// unrelated to what the ruleset restricts. Without this, `apt`'s
// `partial/ → parent` rename (and any tool that moves a file between dirs)
// fails with "Invalid cross-device link". We therefore handle REFER and grant
// it on the very same hierarchies as READ_FILE: since every granted path
// carries the identical right, moving a file among them is access-neutral (no
// escalation, which REFER forbids), while the ungranted secret dirs still can't
// be a rename source or target. (See go-test TestReadGuardRefer; docs/isolation.md.)

// DetectProtections probes the kernel for the terminal-hardening mechanisms
// (for the admin console). Cheap, side-effect-free syscalls.
func DetectProtections() Protections {
	p := Protections{}
	if _, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err == nil {
		p.Seccomp = true // seccomp compiled in (filter mode is universal on such kernels)
	}
	if abi := landlockABI(); abi > 0 {
		p.Landlock, p.LandlockABI = true, abi
	}
	return p
}

// landlockABI returns the kernel's supported Landlock ABI version, or 0 if
// Landlock is unavailable/disabled. landlock_create_ruleset(NULL, 0, VERSION).
func landlockABI() int {
	r, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if e != 0 {
		return 0
	}
	return int(r)
}

// installReadGuard applies the read guard for g. Requires no_new_privs (the
// init sets it). Returns nil (no-op) when Landlock is unavailable — the caller
// treats the guard as best-effort defense in depth.
func installReadGuard(g *ReadGuardSpec) error {
	abi := landlockABI()
	if g == nil || g.Root == "" || g.Root == "/" || abi < 1 {
		return nil
	}
	// Handle READ_FILE always; add REFER on ABI ≥ 2 so cross-directory renames
	// stay possible where we grant it (else the kernel EXDEVs every reparent —
	// breaks apt; see the package doc). Both the ruleset's handled set and each
	// path grant use this same mask, so granted paths are access-equivalent for
	// REFER (no escalation) and the ungranted secrets remain off-limits.
	access := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
	if abi >= 2 {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	attr := unix.LandlockRulesetAttr{Access_fs: access}
	fd, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", e)
	}
	rulesetFD := int(fd)
	defer unix.Close(rulesetFD)

	// grant read-file (+ refer) on a path hierarchy (missing paths are skipped —
	// a bind that isn't present just isn't granted, never an error).
	grant := func(path string) {
		pf, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return
		}
		defer unix.Close(pf)
		pb := unix.LandlockPathBeneathAttr{
			Allowed_access: access,
			Parent_fd:      int32(pf),
		}
		_, _, _ = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
			uintptr(rulesetFD), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
			uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
	}

	// Everything OUTSIDE the workspace stays readable: walk from / down to the
	// workspace root, granting every SIBLING at each level, so only the
	// workspace subtree itself is left to the selective grants below.
	// (Granting an ancestor — or excluding a whole top-level component, as an
	// earlier version did — is wrong in both directions: "/" would sweep the
	// secrets in, and skipping all of e.g. /opt read-blocked its other
	// children, notably the SDK bind at /opt/xbin/sdk when the workspace lives
	// at /opt/xbin/workspace: `go build` and `cat` failed on world-readable
	// files while `ls` worked, since only READ_FILE is restricted.)
	// Generous on purpose: the only reads we deny are the workspace secrets,
	// so the shell can never be starved.
	dir := "/"
	for _, seg := range strings.Split(strings.TrimPrefix(filepath.Clean(g.Root), "/"), "/") {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.Name() != seg {
					grant(filepath.Join(dir, e.Name()))
				}
			}
		}
		dir = filepath.Join(dir, seg)
	}
	// The workspace: grant each child (sibling tile source, root files like
	// AGENTS.md/go.work) except the secret dirs; keep the own $HOME readable.
	secret := map[string]bool{}
	for _, s := range g.SecretDirs {
		secret[s] = true
	}
	if entries, err := os.ReadDir(g.Root); err == nil {
		for _, e := range entries {
			if secret[e.Name()] {
				continue
			}
			grant(filepath.Join(g.Root, e.Name()))
		}
	}
	for _, p := range g.AllowUnder {
		grant(p)
	}

	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); e != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", e)
	}
	return nil
}
