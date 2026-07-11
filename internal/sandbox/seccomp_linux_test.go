package sandbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// seccompData builds a synthetic seccomp_data record (linux/seccomp.h layout)
// so the filter can be exercised in a userspace BPF VM without a real sandbox.
//
// Fields are written big-endian on purpose: the kernel reads seccomp_data in
// *native* byte order, while a classic-BPF VM does *big-endian* (ntohl) loads —
// so on a little-endian host, big-endian test data makes the VM compute the
// exact comparisons the kernel does on native data (confirmed against the live
// kernel by TestMountGuardKernelInstall).
func seccompData(nr int, arch uint32, mountFlags uint64) []byte {
	b := make([]byte, 64) // nr(4) arch(4) ip(8) args[6]*8
	binary.BigEndian.PutUint32(b[0:], uint32(nr))
	binary.BigEndian.PutUint32(b[4:], arch)
	binary.BigEndian.PutUint32(b[scOffMountFlags:], uint32(mountFlags)) // args[3] low word = mount flags
	return b
}

// seccompDataArg0 is like seccompData but puts flags in args[0] — the clone(2)/
// unshare(2) flags argument that restrictNSProgram inspects.
func seccompDataArg0(nr int, arch uint32, arg0 uint64) []byte {
	b := make([]byte, 64)
	binary.BigEndian.PutUint32(b[0:], uint32(nr))
	binary.BigEndian.PutUint32(b[4:], arch)
	binary.BigEndian.PutUint32(b[scOffArg0:], uint32(arg0)) // args[0] low word = clone/unshare flags
	return b
}

// TestRestrictNSProgram pins the namespace-restrict filter: clone/unshare that
// create a user or mount namespace are EPERM'd, clone3 is ENOSYS'd (so glibc
// falls back to the filterable clone), setns is denied, and ordinary spawns /
// syscalls are allowed. Foreign ABI is killed. The whole security argument for
// dropping CAP_SYS_ADMIN on a restricted terminal rests on this being right.
func TestRestrictNSProgram(t *testing.T) {
	arch, ok := nativeAuditArch()
	if !ok {
		t.Skipf("no audit arch for GOARCH=%s", runtime.GOARCH)
	}
	vm, err := bpf.NewVM(restrictNSProgram(arch))
	if err != nil {
		t.Fatal(err)
	}
	run := func(nr int, a uint32, arg0 uint64) uint32 {
		v, err := vm.Run(seccompDataArg0(nr, a, arg0))
		if err != nil {
			t.Fatal(err)
		}
		return uint32(v)
	}
	allow, eperm, enosys := uint32(unix.SECCOMP_RET_ALLOW), retErrnoEPERM, retErrnoENOSYS
	newuser, newns := uint64(unix.CLONE_NEWUSER), uint64(unix.CLONE_NEWNS)

	cases := []struct {
		name string
		nr   int
		arg0 uint64
		want uint32
	}{
		// clone3: always ENOSYS — flags are unreachable in memory, so the only safe
		// move is to make glibc retry via clone (even a NEWUSER "ptr" is ignored).
		{"clone3", unix.SYS_CLONE3, 0, enosys},
		{"clone3 w/ newuser-looking arg", unix.SYS_CLONE3, newuser, enosys},
		// clone/unshare creating a user or mount ns → denied.
		{"clone NEWUSER", unix.SYS_CLONE, newuser, eperm},
		{"clone NEWNS", unix.SYS_CLONE, newns, eperm},
		{"clone NEWUSER|SIGCHLD", unix.SYS_CLONE, newuser | 17, eperm},
		{"unshare NEWUSER", unix.SYS_UNSHARE, newuser, eperm},
		{"unshare NEWNS", unix.SYS_UNSHARE, newns, eperm},
		{"setns", unix.SYS_SETNS, 0, eperm},
		// Ordinary spawns / syscalls → allowed (apt, shells, threads keep working).
		{"clone thread", unix.SYS_CLONE, uint64(unix.CLONE_VM | unix.CLONE_FS | unix.CLONE_FILES), allow},
		{"clone fork (SIGCHLD)", unix.SYS_CLONE, 17, allow},
		{"unshare files only", unix.SYS_UNSHARE, uint64(unix.CLONE_FILES), allow},
		{"read", unix.SYS_READ, 0, allow},
		{"mount (not this filter's job)", unix.SYS_MOUNT, 0, allow},
	}
	for _, c := range cases {
		if got := run(c.nr, arch, c.arg0); got != c.want {
			t.Errorf("%s: got %#x, want %#x", c.name, got, c.want)
		}
	}
	if got := run(unix.SYS_READ, arch^0xFF, 0); got != uint32(unix.SECCOMP_RET_KILL_PROCESS) {
		t.Errorf("foreign arch: got %#x, want kill", got)
	}
}

// TestMountGuardProgram runs the filter in a userspace VM over synthetic
// syscalls, pinning exactly which calls it denies — the security-critical
// logic, validated without needing the (privileged, locally-flaky) sandbox.
func TestMountGuardProgram(t *testing.T) {
	arch, ok := nativeAuditArch()
	if !ok {
		t.Skipf("no audit arch for GOARCH=%s", runtime.GOARCH)
	}
	vm, err := bpf.NewVM(mountGuardProgram(arch))
	if err != nil {
		t.Fatal(err)
	}
	run := func(nr int, a uint32, flags uint64) uint32 {
		v, err := vm.Run(seccompData(nr, a, flags))
		if err != nil {
			t.Fatal(err)
		}
		return uint32(v)
	}
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	deny := retErrnoEPERM
	kill := uint32(unix.SECCOMP_RET_KILL_PROCESS)

	cases := []struct {
		name  string
		nr    int
		flags uint64
		want  uint32
	}{
		// The four reveal vectors — deny.
		{"umount2", unix.SYS_UMOUNT2, 0, deny},
		{"move_mount", unix.SYS_MOVE_MOUNT, 0, deny},
		{"open_tree", unix.SYS_OPEN_TREE, 0, deny},
		{"mount MS_MOVE", unix.SYS_MOUNT, uint64(unix.MS_MOVE), deny},
		{"mount MS_MOVE|MS_RDONLY", unix.SYS_MOUNT, uint64(unix.MS_MOVE | unix.MS_RDONLY), deny},
		// Everything else — allow, so the dev box keeps its power.
		{"mount MS_BIND", unix.SYS_MOUNT, uint64(unix.MS_BIND), allow},
		{"mount no flags", unix.SYS_MOUNT, 0, allow},
		{"read", unix.SYS_READ, 0, allow},
		{"openat", unix.SYS_OPENAT, 0, allow},
		{"unshare", unix.SYS_UNSHARE, 0, allow},
		{"pivot_root", unix.SYS_PIVOT_ROOT, 0, allow},
	}
	for _, c := range cases {
		if got := run(c.nr, arch, c.flags); got != c.want {
			t.Errorf("%s: got %#x, want %#x", c.name, got, c.want)
		}
	}
	// A syscall on a *different* arch (e.g. the i386 compat ABI, whose numbers
	// differ) is killed — no ABI-confusion bypass of the block.
	if got := run(unix.SYS_READ, arch^0xFF, 0); got != kill {
		t.Errorf("foreign arch: got %#x, want kill %#x", got, kill)
	}
}

// TestBackendSeccompProgram pins the backend block-list: every listed syscall
// is EPERM'd, ordinary server syscalls are allowed, foreign arch is killed.
func TestBackendSeccompProgram(t *testing.T) {
	arch, ok := nativeAuditArch()
	if !ok {
		t.Skipf("no audit arch for GOARCH=%s", runtime.GOARCH)
	}
	vm, err := bpf.NewVM(denyProgram(arch, backendDeny(), false))
	if err != nil {
		t.Fatal(err)
	}
	run := func(nr int, a uint32) uint32 {
		v, err := vm.Run(seccompData(nr, a, 0))
		if err != nil {
			t.Fatal(err)
		}
		return uint32(v)
	}
	deny, allow := retErrnoEPERM, uint32(unix.SECCOMP_RET_ALLOW)
	for _, nr := range backendDeny() {
		if got := run(int(nr), arch); got != deny {
			t.Errorf("backend syscall %d: got %#x, want EPERM", nr, got)
		}
	}
	for _, nr := range []int{unix.SYS_READ, unix.SYS_WRITE, unix.SYS_OPENAT, unix.SYS_CONNECT, unix.SYS_CLONE} {
		if got := run(nr, arch); got != allow {
			t.Errorf("ordinary syscall %d must be allowed, got %#x", nr, got)
		}
	}
	if got := run(unix.SYS_READ, arch^0xFF); got != uint32(unix.SECCOMP_RET_KILL_PROCESS) {
		t.Errorf("foreign arch not killed: %#x", got)
	}
}

// TestMountGuardKernelInstall installs the real filter in a child process and
// checks the kernel actually denies umount2 — no namespaces or privileges
// needed (no_new_privs lets any process install a seccomp filter), so this
// runs in normal CI unlike the full sandbox e2e.
func TestMountGuardKernelInstall(t *testing.T) {
	if _, ok := nativeAuditArch(); !ok {
		t.Skip("unsupported arch")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMountGuardChild", "-test.v")
	cmd.Env = append(os.Environ(), "XBIN_GUARD_CHILD=1")
	out, _ := cmd.CombinedOutput()
	switch {
	case bytes.Contains(out, []byte("GUARD-OK")):
		// filter installed and denied umount2 — the real assertion.
	case bytes.Contains(out, []byte("install:")), bytes.Contains(out, []byte("no_new_privs:")):
		t.Skipf("environment won't let a process install a seccomp filter:\n%s", out)
	default:
		t.Fatalf("mount guard did not deny umount2 in a real process:\n%s", out)
	}
}

// TestMountGuardChild is the child half of TestMountGuardKernelInstall: it
// installs the guard, then umount2's a nonexistent path. Unfiltered that is
// ENOENT; the filter turns it into EPERM.
func TestMountGuardChild(t *testing.T) {
	if os.Getenv("XBIN_GUARD_CHILD") != "1" {
		t.Skip("child-only helper (spawned by TestMountGuardKernelInstall)")
	}
	runtime.LockOSThread() // seccomp (mode filter) binds the calling thread
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fmt.Println("GUARD-FAIL no_new_privs:", err)
		return
	}
	if err := installMountGuard(); err != nil {
		fmt.Println("GUARD-FAIL install:", err)
		return
	}
	if err := unix.Unmount("/xbin-guard-probe-nonexistent", 0); err == unix.EPERM {
		fmt.Println("GUARD-OK")
	} else {
		fmt.Println("GUARD-FAIL umount2 not denied:", err)
	}
}

// TestRestrictNSKernelInstall installs the ns-restrict filter in a child and
// checks the real kernel denies namespace creation — same no-privilege trick as
// the mount-guard test, so it runs in normal CI without the full sandbox.
func TestRestrictNSKernelInstall(t *testing.T) {
	if _, ok := nativeAuditArch(); !ok {
		t.Skip("unsupported arch")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestRestrictNSChild", "-test.v")
	cmd.Env = append(os.Environ(), "XBIN_NSGUARD_CHILD=1")
	out, _ := cmd.CombinedOutput()
	switch {
	case bytes.Contains(out, []byte("NSGUARD-OK")):
		// filter installed and the kernel denied setns + unshare(NEWUSER).
	case bytes.Contains(out, []byte("install:")), bytes.Contains(out, []byte("no_new_privs:")):
		t.Skipf("environment won't let a process install a seccomp filter:\n%s", out)
	default:
		t.Fatalf("ns-restrict filter did not deny in a real process:\n%s", out)
	}
}

// TestRestrictNSChild is the child half: install the filter, then confirm the
// kernel turns setns and unshare(CLONE_NEWUSER) into EPERM. setns(-1) is the
// tell — unfiltered it's EBADF, so EPERM proves the filter is live regardless of
// whether the host even allows unprivileged userns.
func TestRestrictNSChild(t *testing.T) {
	if os.Getenv("XBIN_NSGUARD_CHILD") != "1" {
		t.Skip("child-only helper (spawned by TestRestrictNSKernelInstall)")
	}
	runtime.LockOSThread() // seccomp (mode filter) binds the calling thread
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fmt.Println("NSGUARD-FAIL no_new_privs:", err)
		return
	}
	if err := installRestrictNamespaces(); err != nil {
		fmt.Println("NSGUARD-FAIL install:", err)
		return
	}
	if err := unix.Setns(-1, 0); err != unix.EPERM { // unfiltered → EBADF
		fmt.Println("NSGUARD-FAIL setns not denied:", err)
		return
	}
	if err := unix.Unshare(unix.CLONE_NEWUSER); err != unix.EPERM {
		fmt.Println("NSGUARD-FAIL unshare(NEWUSER) not denied:", err)
		return
	}
	fmt.Println("NSGUARD-OK")
}
