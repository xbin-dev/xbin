//go:build integration

// End-to-end tests of the container-store filesystem stack: the patched
// gocryptfs in -xbin-single-tenant mode (hack/gocryptfs-patches/), alone and
// under fuse-overlayfs, exactly as cap:containers resources mount it
// (docs/resources.md, plans/DECISIONS.md D43/D44).
//
// Three suites, each a shell script under test/containerfs/ run inside
// `unshare --user --map-root-user --mount`:
//
//   - fidelity.sh: the single-tenant semantics podman's layer store needs
//     (0555 dirs, chmod/chown round-trip, whiteouts, security.capability,
//     hardlinks, RENAME_WHITEOUT, tar layer round-trip);
//   - poison.sh: the D44 inode-reuse regression — a deleted file's cached
//     identity/capability must not dress the inode's next occupant (reuse is
//     simulated deterministically with test/reusefs, since CI/dev
//     filesystems often never recycle inode numbers);
//   - overlay.sh: dpkg's unpack syscall sequence through fuse-overlayfs on
//     the encrypted store, execing everything after the kernel attr caches
//     expire (the failure window of the original bug).
//
// Needs the patched gocryptfs at bin/gocryptfs (`make gocryptfs`, or point
// XBIN_GOCRYPTFS at one) and unprivileged user namespaces; the overlay suite
// also needs fuse-overlayfs (`make fuse-overlayfs`, XBIN_FUSE_OVERLAYFS, or
// PATH). Suites skip with instructions when prerequisites are missing.
package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// containerfsEnv resolves shared prerequisites once per suite run.
type containerfsEnv struct {
	gocryptfs string
	tool      string // static EXEC-OK probe binary
}

func containerfsSetup(t *testing.T) *containerfsEnv {
	t.Helper()
	if out, err := exec.Command("unshare", "--user", "--map-root-user", "true").CombinedOutput(); err != nil {
		t.Skipf("unprivileged user namespaces unavailable (%v: %s) — on Ubuntu 24.04: sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0", err, strings.TrimSpace(string(out)))
	}

	g := os.Getenv("XBIN_GOCRYPTFS")
	if g == "" {
		g = "../bin/gocryptfs"
	}
	abs, err := filepath.Abs(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("patched gocryptfs not found at %s — run `make gocryptfs` (needs docker) or set XBIN_GOCRYPTFS", abs)
	}
	// A binary without the patch is a broken build, not a missing prereq.
	hh, _ := exec.Command(abs, "-hh").CombinedOutput()
	if !strings.Contains(string(hh), "xbin-single-tenant") {
		t.Fatalf("%s lacks -xbin-single-tenant — stale binary? rebuild with `make gocryptfs`", abs)
	}

	tool := filepath.Join(t.TempDir(), "exectool")
	buildGo(t, tool, "./containerfs/exectool")
	return &containerfsEnv{gocryptfs: abs, tool: tool}
}

// buildGo compiles a static helper from this module (or dir with its own
// module) into dest.
func buildGo(t *testing.T, dest, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", dest, ".")
	cmd.Dir = strings.TrimPrefix(dir, "./")
	// GOWORK=off: test/reusefs is its own module (keeps go-fuse out of the
	// main go.mod) and must not resolve through the repo's go.work.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", dir, err, out)
	}
}

// runSuite executes one containerfs script inside a fresh user+mount ns and
// requires exit 0 plus the script's final PASSED marker.
func runSuite(t *testing.T, marker, script string, args ...string) {
	t.Helper()
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", append([]string{"--user", "--map-root-user", "--mount", abs}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), marker) {
		t.Fatalf("%s failed (err=%v):\n%s", script, err, out)
	}
}

func needTools(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not installed (apt: attr libcap2-bin) — required by this suite", n)
		}
	}
}

// TestContainerFSFidelity: single-tenant mode serves everything podman's
// layer store throws at an encrypted resource.
func TestContainerFSFidelity(t *testing.T) {
	env := containerfsSetup(t)
	needTools(t, "setcap", "getcap", "setfattr", "getfattr", "python3")
	runSuite(t, "ALL-SINGLE-TENANT-TESTS-PASSED", "containerfs/fidelity.sh", t.TempDir(), env.gocryptfs)
}

// TestContainerFSReusePoisoning: the D44 regression — per-inode caches must
// be invalidated when the backing filesystem recycles an inode number.
func TestContainerFSReusePoisoning(t *testing.T) {
	env := containerfsSetup(t)
	needTools(t, "setfattr", "getfattr")
	reusefs := filepath.Join(t.TempDir(), "reusefs")
	buildGo(t, reusefs, "./reusefs")
	runSuite(t, "POISON-TESTS-PASSED", "containerfs/poison.sh", t.TempDir(), env.gocryptfs, reusefs, env.tool)
}

// TestContainerFSOverlayExec: dpkg's unpack sequence through fuse-overlayfs
// on the encrypted store; everything must still exec once the kernel attr
// caches have expired.
func TestContainerFSOverlayExec(t *testing.T) {
	env := containerfsSetup(t)
	fo := os.Getenv("XBIN_FUSE_OVERLAYFS")
	if fo == "" {
		fo = "../bin/fuse-overlayfs"
	}
	if _, err := os.Stat(fo); err != nil {
		if p, err := exec.LookPath("fuse-overlayfs"); err == nil {
			fo = p
		} else {
			t.Skip("fuse-overlayfs not found — run `make fuse-overlayfs` (needs docker) or set XBIN_FUSE_OVERLAYFS")
		}
	}
	if abs, err := filepath.Abs(fo); err == nil {
		fo = abs
	}
	dpkgsim := filepath.Join(t.TempDir(), "dpkgsim")
	buildGo(t, dpkgsim, "./containerfs/dpkgsim")
	runSuite(t, "OVERLAY-TESTS-PASSED", "containerfs/overlay.sh", t.TempDir(), env.gocryptfs, fo, env.tool, dpkgsim)
}
