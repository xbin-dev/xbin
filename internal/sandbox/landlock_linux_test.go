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
// process and checks the kernel denies reading a "secret" file while
// everything legitimate stays readable — no sandbox or privileges needed
// (Landlock is unprivileged with no_new_privs), so it runs in normal CI.
//
// The layout mirrors the production install (`/opt/xbin/workspace`): the
// workspace root is NESTED, with an sdk/ sibling next to it — the shape that
// regressed on 2026-07-12, when the guard skipped the workspace's whole
// top-level component and thereby read-blocked /opt/xbin/sdk (cat/go build
// failed on world-readable files while ls worked).
func TestReadGuardKernelInstall(t *testing.T) {
	if landlockABI() < 1 {
		t.Skip("kernel has no Landlock")
	}
	base := filepath.Join(t.TempDir(), "opt", "xbin") // ← ws root is 4+ levels deep
	root := filepath.Join(base, "workspace")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "allowed", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".xbin"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "homes", "alice"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "homes", "bob"), 0o755))
	must(os.MkdirAll(filepath.Join(base, "sdk"), 0o755))
	must(os.WriteFile(filepath.Join(root, "allowed", "f"), []byte("OK"), 0o644))
	must(os.WriteFile(filepath.Join(root, "allowed", "sub", "moveme"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, ".xbin", "token"), []byte("SECRET"), 0o600))
	must(os.WriteFile(filepath.Join(root, "homes", "alice", "cred"), []byte("mine"), 0o644))
	must(os.WriteFile(filepath.Join(root, "homes", "bob", "cred"), []byte("theirs"), 0o644))
	must(os.WriteFile(filepath.Join(base, "sdk", "xbin.go"), []byte("package xbin"), 0o644))
	// A FILE directly at the workspace root — granted individually, not via a
	// parent dir. REFER is directory-only, so a naive READ_FILE|REFER grant
	// EINVALs here and read-blocks it (AGENTS.md/go.work/CLAUDE.md).
	must(os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("guide"), 0o644))

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
	case bytes.Contains(out, []byte("SIBLING-DENIED")):
		// The 2026-07-12 regression: a workspace-ADJACENT path (the SDK bind)
		// must stay readable when the workspace root is nested.
		t.Fatalf("read guard denied a workspace-sibling read (the /opt/xbin/sdk "+
			"regression — grant siblings level by level, never skip a whole top component):\n%s", out)
	case bytes.Contains(out, []byte("ROOTFILE-DENIED")):
		// A file granted directly at the workspace root read-blocked because
		// READ_FILE|REFER EINVALs on a non-directory (AGENTS.md/go.work).
		t.Fatalf("read guard denied a workspace-root FILE (REFER is directory-only — "+
			"grant a file READ_FILE without REFER):\n%s", out)
	case bytes.Contains(out, []byte("READGUARD-OK")):
		// secrets denied, everything legitimate allowed — the assertion.
	default:
		t.Fatalf("read guard did not behave (secret readable, or a grant missing):\n%s", out)
	}
}

// TestReadGuardChild is the child half: apply the guard for $XBIN_READGUARD_ROOT
// (secret dirs .xbin/data/homes, own $HOME under AllowUnder — the production
// spec shape), then probe reads. Uses raw opens and exits immediately so
// nothing else touches the filesystem post-restrict.
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
	if err := installReadGuard(&ReadGuardSpec{
		Root:       root,
		SecretDirs: []string{".xbin", "data", "homes"},
		AllowUnder: []string{filepath.Join(root, "homes", "alice")},
	}); err != nil {
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
	okSibling := readable(filepath.Join(filepath.Dir(root), "sdk", "xbin.go")) // the SDK bind shape
	okOwnHome := readable(filepath.Join(root, "homes", "alice", "cred"))       // AllowUnder inside a secret dir
	okOtherHome := readable(filepath.Join(root, "homes", "bob", "cred"))       // still masked
	okRootFile := readable(filepath.Join(root, "AGENTS.md"))                   // file granted directly at root
	// Cross-directory rename WITHIN a granted hierarchy (allowed/sub → allowed):
	// must succeed. Landlock (ABI≥2) denies reparenting with EXDEV unless the
	// guard handles+grants REFER — the exact failure that broke `apt`. os.Rename
	// surfaces that EXDEV (it doesn't fall back to copy the way `mv` does).
	referErr := os.Rename(filepath.Join(root, "allowed", "sub", "moveme"), filepath.Join(root, "allowed", "moved"))
	switch {
	case referErr != nil:
		fmt.Printf("REFER-DENIED %v\n", referErr)
	case !okSibling:
		fmt.Println("SIBLING-DENIED")
	case !okRootFile:
		fmt.Println("ROOTFILE-DENIED")
	case okAllowed && okOwnHome && !okSecret && !okOtherHome:
		fmt.Println("READGUARD-OK")
	default:
		fmt.Printf("READGUARD-FAIL allowed=%v secret=%v ownHome=%v otherHome=%v rootFile=%v\n",
			okAllowed, okSecret, okOwnHome, okOtherHome, okRootFile)
	}
	os.Exit(0)
}
