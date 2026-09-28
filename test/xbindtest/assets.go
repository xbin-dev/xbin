//go:build linux

// Package xbindtest boots real, isolated xbinds for end-to-end tests
// (plans/tile-sandbox-runtime.md WP-21): an `xbind --isolate` built from this
// tree over the base rootfs, on a workspace of its own, driven over HTTP and
// WebSockets exactly as a browser, a tile or bx would drive it.
//
//	func TestMain(m *testing.M) { xbindtest.Main(m) }
//
//	func TestSomething(t *testing.T) {
//		a := xbindtest.Require(t) // skips without userns or a rootfs
//		d := xbindtest.Start(t, a, xbindtest.Options{})
//		d.CopyTile(t, a.Example("sandbox-go"), "apps/sbx")
//		d.Grant(t, "apps/sbx", "cap:sandboxes", "writer")
//		d.Must(t, "GET", "/api/apps/sbx/runtime", nil, 200)
//	}
//
// Each daemon is stopped, its tile sandboxes deleted first and its
// workspace removed (confined, for what a sandbox's sub-uids own) when the
// test ends; every process it started is checked gone.
//
// What it needs (Require skips without the first two): unprivileged user
// namespaces; a base rootfs ($XBIN_ROOTFS, $XBIN_TEST_ROOTFS or the repo's
// .rootfs; make rootfs); fuse-overlayfs and gocryptfs ($XBIN_FUSE_OVERLAYFS,
// $XBIN_GOCRYPTFS, the repo's bin/ or PATH; without them xbind uses the
// kernel overlay and holds encrypted resources); for VM mode the VM assets
// (the repo's bin/ after make vm-assets, or the XBIN_* variables) —
// Daemon.RequireVM asks the daemon itself. xbind, bx and xbin-vmagent are
// built from this tree, once per test binary. XBIN_VM_ACCEL=emulate in the
// test's environment reaches the daemons (QEMU's emulation).
//
// Options.Auth boots it with owner auth on (people are accounts: AddUser,
// Login). StartOrConnect drives an xbind elsewhere instead when
// XBIN_E2E_URL names one — the QA box's test instance through an ssh
// tunnel (remote.go has the variables).
//
// Run the tests with the Bash sandbox disabled on a dev box: the sandboxes'
// /proc/self/exe re-exec gets EPERM inside it.
package xbindtest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Main is TestMain for a package using these helpers: the test binary also
// serves as the confined runs' sandbox init (sandbox.InitArg), and the
// binaries it built go with it.
func Main(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2]) // never returns
	}
	code := m.Run()
	if built.dir != "" {
		_ = os.RemoveAll(built.dir)
	}
	os.Exit(code)
}

// Assets is what an isolated xbind runs with.
type Assets struct {
	Repo          string // the repo's root
	Bin           string // xbind, bx and xbin-vmagent, built from this tree (the daemon's XBIN_BIN)
	Rootfs        string // the base rootfs
	FuseOverlayfs string // "" = none found: the kernel overlay
	Gocryptfs     string // "" = none found
	// UIDRange is whether this host delegates a sub-uid range to the test's
	// user: namespace sandboxes then run in range mode (runtime users: any).
	UIDRange bool
}

var (
	built struct {
		once sync.Once
		dir  string
		err  error
	}
)

// Require returns the assets, skipping the test without user namespaces or
// a base rootfs; the binaries are built on first use.
func Require(t testing.TB) *Assets {
	t.Helper()
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	repo, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	a := &Assets{Repo: repo}
	for _, p := range []string{os.Getenv("XBIN_ROOTFS"), os.Getenv("XBIN_TEST_ROOTFS"), filepath.Join(repo, ".rootfs")} {
		if p == "" {
			continue
		}
		if abs, _ := filepath.Abs(p); isFile(filepath.Join(abs, "bin", "sh")) {
			a.Rootfs = abs
			break
		}
	}
	if a.Rootfs == "" {
		t.Skip("no base rootfs ($XBIN_ROOTFS, $XBIN_TEST_ROOTFS or the repo's .rootfs; make rootfs)")
	}
	a.FuseOverlayfs = findTool("XBIN_FUSE_OVERLAYFS", filepath.Join(repo, "bin", "fuse-overlayfs"), "fuse-overlayfs")
	a.Gocryptfs = findTool("XBIN_GOCRYPTFS", filepath.Join(repo, "bin", "gocryptfs"), "gocryptfs")
	a.UIDRange, _ = sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	built.once.Do(func() { built.dir, built.err = build(repo) })
	if built.err != nil {
		t.Fatal(built.err)
	}
	a.Bin = built.dir
	// the helpers' own removals of what sandboxes left (sub-uid-owned
	// trees) run confined, over the same rootfs
	confine.Configure(a.Rootfs)
	return a
}

// Example is the path of examples/<name> in the repo.
func (a *Assets) Example(name string) string { return filepath.Join(a.Repo, "examples", name) }

// vmEnv points a daemon at the VM assets: the repo's bin/ unless the test's
// environment names them, and the guest agent built here.
func (a *Assets) vmEnv() []string {
	var env []string
	for k, name := range map[string]string{
		"XBIN_FIRECRACKER": "firecracker", "XBIN_VM_KERNEL": "vmlinux", "XBIN_MKFS_EROFS": "mkfs.erofs",
		"XBIN_QEMU": "qemu-system-x86_64", "XBIN_VHOST_VSOCK": "vhost-device-vsock",
	} {
		if os.Getenv(k) == "" {
			if p := filepath.Join(a.Repo, "bin", name); isFile(p) {
				env = append(env, k+"="+p)
			}
		}
	}
	if os.Getenv("XBIN_VM_AGENT") == "" {
		env = append(env, "XBIN_VM_AGENT="+filepath.Join(a.Bin, "xbin-vmagent"))
	}
	return env
}

// build compiles xbind, bx and the guest agent from this tree, static (bx
// is bound into sandboxes as their agent; the guest agent is a VM's init).
func build(repo string) (string, error) {
	dir, err := os.MkdirTemp("", "xbindtest-bin-*")
	if err != nil {
		return "", err
	}
	for out, pkg := range map[string]string{"xbind": "./cmd/xbind", "bx": "./cmd/bx", "xbin-vmagent": "./cmd/xbin-vmagent"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, out), pkg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("build %s: %v\n%s", pkg, err, b)
		}
	}
	return dir, nil
}

// repoRoot walks up from the working directory to the xbin module's root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if f, err := os.Open(filepath.Join(dir, "go.mod")); err == nil {
			sc := bufio.NewScanner(f)
			sc.Scan()
			f.Close()
			if strings.TrimSpace(sc.Text()) == "module github.com/xbin-dev/xbin" {
				return dir, nil
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", fmt.Errorf("xbindtest: no xbin checkout above the working directory")
		}
		dir = up
	}
}

// findTool is $env (when set, only it), else def when executable, else
// PATH's name; "" when none.
func findTool(env, def, name string) string {
	if p := os.Getenv(env); p != "" {
		if executable(p) {
			return p
		}
		return ""
	}
	if executable(def) {
		return def
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// CopyRootfs copies the assets' rootfs to dst (cp -a, reflinked where the
// filesystem can: seconds on btrfs or xfs, a full copy on tmpfs) and stamps
// the copy's base version — a base of the test's own, which a boot's GC
// may release (never run a GC test over a shared rootfs: it releases the
// unpinned `<rootfs>-<version>` siblings next to it). dst's parent must
// exist; the copy is removed when the test ends. XBIN_ITEST_DIR picks where
// TempDir-like dirs for copies go (TempRoot).
func (a *Assets) CopyRootfs(t testing.TB, dst, version string) {
	t.Helper()
	CopyTree(t, a.Rootfs, dst)
	if err := os.WriteFile(filepath.Join(dst, "etc", "xbin-base-version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// CopyTree copies src to dst with cp -a (reflinked where it can) and
// removes dst when the test ends.
func CopyTree(t testing.TB, src, dst string) {
	t.Helper()
	t.Cleanup(func() { RemoveTree(dst) })
	if b, err := exec.Command("cp", "-a", "--reflink=auto", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", src, err, b)
	}
}

// TempRoot makes a directory for a test's large trees (rootfs copies) on
// $XBIN_ITEST_DIR — pick one on the rootfs's filesystem for reflinked
// copies — or the system's temp dir, removed when the test ends.
func TempRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("XBIN_ITEST_DIR"), "xbindtest-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { RemoveTree(dir) })
	return dir
}

// RemoveTree removes dir even where read-only directories (a module
// cache's, a rootfs's) are in it; what a sandbox's sub-uids own there is
// removed in a confined run (confine.RemoveAll).
func RemoveTree(dir string) {
	if _, err := os.Lstat(dir); err != nil {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	if os.RemoveAll(dir) == nil {
		return
	}
	_ = confine.RemoveAll(bgCtx(), dir)
}
