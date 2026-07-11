package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// TestReadGuardKernelInstall applies the real Landlock read guard in a child
// process and checks the kernel denies reading a "secret" file while a sibling
// stays readable — no sandbox or privileges needed (Landlock is unprivileged
// with no_new_privs), so it runs in normal CI.
func TestReadGuardKernelInstall(t *testing.T) {
	if landlockABI() < 1 {
		t.Skip("kernel has no Landlock")
	}
	root := filepath.Join(t.TempDir(), "ws")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "allowed", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".xbin"), 0o755))
	must(os.WriteFile(filepath.Join(root, "allowed", "f"), []byte("OK"), 0o644))
	must(os.WriteFile(filepath.Join(root, "allowed", "sub", "moveme"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, ".xbin", "token"), []byte("SECRET"), 0o600))

	cmd := exec.Command(os.Args[0], "-test.run=TestReadGuardChild", "-test.v")
	cmd.Env = append(os.Environ(), "XBIN_READGUARD_CHILD=1", "XBIN_READGUARD_ROOT="+root)
	out, _ := cmd.CombinedOutput()
	switch {
	case bytes.Contains(out, []byte("READGUARD-SKIP")):
		t.Skipf("child couldn't apply Landlock:\n%s", out)
	case bytes.Contains(out, []byte("REFER-DENIED")):
		// The guard broke cross-directory rename — the apt regression. Distinct
		// message so a future breakage points straight at the REFER handling.
		t.Fatalf("read guard denied a cross-directory rename in a granted dir "+
			"(missing LANDLOCK_ACCESS_FS_REFER → apt's partial/->parent rename EXDEVs):\n%s", out)
	case bytes.Contains(out, []byte("READGUARD-OK")):
		// secret denied, sibling allowed, cross-dir rename allowed — the assertion.
	default:
		t.Fatalf("read guard did not deny reading the secret file:\n%s", out)
	}
}

// TestReadGuardChild is the child half: apply the guard for $XBIN_READGUARD_ROOT
// (secret dir .xbin), then read a granted file and the secret. Uses raw opens
// and exits immediately so nothing else touches the filesystem post-restrict.
func TestReadGuardChild(t *testing.T) {
	if os.Getenv("XBIN_READGUARD_CHILD") == "" {
		t.Skip("child-only helper (spawned by TestReadGuardKernelInstall)")
	}
	root := os.Getenv("XBIN_READGUARD_ROOT")
	runtime.LockOSThread() // Landlock binds the calling thread (and its execve heirs)
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fmt.Println("READGUARD-SKIP no_new_privs:", err)
		os.Exit(0)
	}
	if err := installReadGuard(&ReadGuardSpec{Root: root, SecretDirs: []string{".xbin"}}); err != nil {
		fmt.Println("READGUARD-SKIP install:", err)
		os.Exit(0)
	}
	readable := func(p string) bool {
		fd, err := unix.Open(p, unix.O_RDONLY, 0)
		if err == nil {
			unix.Close(fd)
			return true
		}
		return false
	}
	okAllowed := readable(filepath.Join(root, "allowed", "f"))
	okSecret := readable(filepath.Join(root, ".xbin", "token"))
	// Cross-directory rename WITHIN a granted hierarchy (allowed/sub → allowed):
	// must succeed. Landlock (ABI≥2) denies reparenting with EXDEV unless the
	// guard handles+grants REFER — the exact failure that broke `apt`. os.Rename
	// surfaces that EXDEV (it doesn't fall back to copy the way `mv` does).
	referErr := os.Rename(filepath.Join(root, "allowed", "sub", "moveme"), filepath.Join(root, "allowed", "moved"))
	switch {
	case referErr != nil:
		fmt.Printf("REFER-DENIED %v\n", referErr)
	case okAllowed && !okSecret:
		fmt.Println("READGUARD-OK")
	default:
		fmt.Printf("READGUARD-FAIL allowed=%v secret=%v\n", okAllowed, okSecret)
	}
	os.Exit(0)
}
