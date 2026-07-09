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
// It restricts exactly one right, LANDLOCK_ACCESS_FS_READ_FILE, and grants it
// on everything the shell legitimately reads (the whole rootfs, all tile
// source, the own $HOME) — but NOT on the workspace's .xbin/data/homes. So
// reading those files' *contents* (the owner token, vault, password hashes,
// other users' agent credentials) is denied at the kernel, independent of the
// mount. Directory listing, execution, and writes are untouched (the read-only
// bind already scopes writes), so collateral is near zero. Best-effort: a
// kernel without Landlock is a silent no-op (the masks + mount guard remain).

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
	if g == nil || g.Root == "" || g.Root == "/" || landlockABI() < 1 {
		return nil
	}
	attr := unix.LandlockRulesetAttr{Access_fs: unix.LANDLOCK_ACCESS_FS_READ_FILE}
	fd, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", e)
	}
	rulesetFD := int(fd)
	defer unix.Close(rulesetFD)

	// grant read-file on a path hierarchy (missing paths are skipped — a bind
	// that isn't present just isn't granted, never an error).
	grant := func(path string) {
		pf, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return
		}
		defer unix.Close(pf)
		pb := unix.LandlockPathBeneathAttr{
			Allowed_access: unix.LANDLOCK_ACCESS_FS_READ_FILE,
			Parent_fd:      int32(pf),
		}
		_, _, _ = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
			uintptr(rulesetFD), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
			uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
	}

	// Everything in the rootfs stays readable — grant every top-level of / except
	// the workspace's own top component (so nothing there is granted broadly and
	// the secrets under it aren't swept in). Generous on purpose: the only reads
	// we deny are the workspace secrets, so the shell can never be starved.
	wsTop := topComponent(g.Root)
	if entries, err := os.ReadDir("/"); err == nil {
		for _, e := range entries {
			if e.Name() == wsTop {
				continue
			}
			grant("/" + e.Name())
		}
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

// topComponent returns the first path element of an absolute path
// ("/workspace" → "workspace", "/home/u/ws" → "home", "/" → "").
func topComponent(p string) string {
	t := strings.SplitN(strings.TrimPrefix(filepath.Clean(p), "/"), "/", 2)
	return t[0]
}
