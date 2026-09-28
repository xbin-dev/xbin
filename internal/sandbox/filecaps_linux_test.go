package sandbox

import (
	"runtime"
	"slices"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The file-caps profile keeps exactly the file capabilities — none that
// mount, build namespaces, trace, raise limits, switch users or make device
// nodes — and its filter is the backend's with mknodat let through (cp -a
// recreates whiteouts), the mount family and setns still denied.
func TestFileCapsProfile(t *testing.T) {
	got := fileCaps()
	want := []int{unix.CAP_CHOWN, unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH,
		unix.CAP_FOWNER, unix.CAP_FSETID, unix.CAP_SETFCAP}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("fileCaps = %v, want %v", got, want)
	}

	arch, ok := nativeAuditArch()
	if !ok {
		t.Skipf("no audit arch for GOARCH=%s", runtime.GOARCH)
	}
	vm, err := bpf.NewVM(denyProgram(arch, fileCapsDeny(), false))
	if err != nil {
		t.Fatal(err)
	}
	run := func(nr int) uint32 {
		v, err := vm.Run(seccompData(nr, arch, 0))
		if err != nil {
			t.Fatal(err)
		}
		return uint32(v)
	}
	deny, allow := retErrnoEPERM, uint32(unix.SECCOMP_RET_ALLOW)
	for _, nr := range backendDeny() {
		want := deny
		if nr == uint32(unix.SYS_MKNODAT) {
			want = allow
		}
		if got := run(int(nr)); got != want {
			t.Errorf("syscall %d: got %#x, want %#x", nr, got, want)
		}
	}
	for _, nr := range []int{unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_SETNS, unix.SYS_PTRACE, unix.SYS_OPEN_TREE} {
		if got := run(nr); got != deny {
			t.Errorf("syscall %d must stay denied, got %#x", nr, got)
		}
	}
	for _, nr := range []int{unix.SYS_OPENAT, unix.SYS_UNLINKAT, unix.SYS_FCHOWNAT, unix.SYS_LSETXATTR, unix.SYS_IOCTL, unix.SYS_COPY_FILE_RANGE} {
		if got := run(nr); got != allow {
			t.Errorf("file syscall %d must be allowed, got %#x", nr, got)
		}
	}
}
