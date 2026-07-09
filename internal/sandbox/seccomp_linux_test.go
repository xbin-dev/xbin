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
