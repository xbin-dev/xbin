//go:build linux && integration

// Run with: go test -tags=integration ./internal/confine/
// Needs user namespaces and an unpacked rootfs with git (XBIN_TEST_ROOTFS, or
// the repo's .rootfs from `make rootfs`); skips otherwise.
package confine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// TestMain doubles as the sandbox's re-exec init and as noXattrFS's FUSE
// server.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	if len(os.Args) == 6 && os.Args[1] == noXattrServeArg {
		serveNoXattrFS(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	}
	os.Exit(m.Run())
}

func testRootfs(t *testing.T) string {
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "usr", "bin", "git")); err != nil || !sandbox.Available() {
		t.Skip("no rootfs with git, or no user namespaces")
	}
	return fs
}

// The point of confinement: a repo whose config runs a command (here
// diff.external, which no -c flag of ours overrides) runs it INSIDE the
// sandbox — it can touch the repo it came from, never the host beyond it.
func TestHostileRepoStaysInside(t *testing.T) {
	fs := testRootfs(t)
	dir := testRepo(t) // made directly, before confinement is on
	marker := filepath.Join(t.TempDir(), "PWNED")
	script := "#!/bin/sh\ntouch " + marker + " " + filepath.Join(dir, "ran-inside") + "\ncat /proc/1/cmdline > " + filepath.Join(dir, "pid1") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "evil.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = cfg.WriteString("[diff]\n\texternal = " + filepath.Join(dir, "evil.sh") + "\n")
	cfg.Close()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)

	Configure(fs)
	defer Configure("")
	if !Isolated() {
		t.Fatal("not isolated")
	}
	if _, err := Git(context.Background(), dir, nil, "diff"); err != nil {
		t.Fatalf("git diff: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran-inside")); err != nil {
		t.Fatal("the repo's command did not run at all — the test proves nothing")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repo's command wrote a host path outside the repo")
	}
	// its own PID namespace: PID 1 is the confined git itself (the init
	// execs the tool), never the host's init
	if b, _ := os.ReadFile(filepath.Join(dir, "pid1")); !strings.HasPrefix(string(b), "git\x00") {
		t.Fatalf("the command saw another PID 1 than the confined git: %q", b)
	}

	// a read-only bind stays read-only; the timeout kills the sandbox
	if _, err := GitRead(context.Background(), dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qam", "nope"); err == nil {
		t.Fatal("a commit through a read-only bind succeeded")
	}
	out, err := GitRead(context.Background(), dir, "log", "--format=%s")
	if err != nil || strings.TrimSpace(out) != "first" {
		t.Fatalf("log: %q %v", out, err)
	}
}

// TmpfsBytes sizes what a run may write in memory: /tmp, /dev/shm and the
// root's own layer are that big, not the kernel's default (half of RAM each).
func TestTmpfsIsSized(t *testing.T) {
	fs := testRootfs(t)
	Configure(fs)
	defer Configure("")
	script := `for d in / /tmp /dev/shm /dev; do df -k "$d" | awk -v d="$d" 'NR==2{print d, $2}'; done; ` +
		`for f in /tmp/fill /dev/shm/fill /dev/fill /fill; do dd if=/dev/zero of=$f bs=1M count=64 conv=fsync 2>/dev/null; echo "$f wrote=$?"; done`
	sized, err := Run(context.Background(), Cmd{Argv: []string{"sh", "-c", script}, TmpfsBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	out := string(sized.Stdout)
	for _, want := range []string{"/ 16384", "/tmp 16384", "/dev/shm 16384", "/dev 1024", "/tmp/fill wrote=1", "/dev/shm/fill wrote=1", "/dev/fill wrote=1", "/fill wrote=1"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("a sized run: no %q — it wrote 64 MiB past a 16 MiB mount, or the mount is not sized:\n%s", want, out)
		}
	}
	plain, err := Run(context.Background(), Cmd{Argv: []string{"sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	if out := string(plain.Stdout); strings.Contains(out, "/tmp 16384") || !strings.Contains(out, "/tmp/fill wrote=0") {
		t.Errorf("an unsized run's mounts changed:\n%s", out)
	}
}
