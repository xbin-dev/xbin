//go:build linux

package guest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRootModesOverlay assembles a root the way assembleRoot does — the
// image's modes staged in their layer, the overlay over it and the upper,
// the directories fixed — in a user and mount namespace of its own (the test
// binary again, as root there), and boots it three times: the first boot
// shows the image's modes and writes nothing of the files to the upper; a
// newer base (a rebase) shows the newer program with its modes; a program
// and a directory the sandbox changed stay as it left them.
func TestRootModesOverlay(t *testing.T) {
	if os.Getenv("XBIN_GUEST_ROOT_TEST") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRootModesOverlay$", "-test.v")
		cmd.Env = append(os.Environ(), "XBIN_GUEST_ROOT_TEST=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		switch {
		case err != nil && !errors.As(err, &ee):
			t.Skipf("no user namespace here: %v", err)
		case err != nil:
			t.Fatalf("in its namespace: %v\n%s", err, out)
		case strings.Contains(string(out), "--- SKIP"):
			t.Skipf("in its namespace:\n%s", out)
		}
		t.Logf("in its namespace:\n%s", out)
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skipf("no mounts here: %v", err)
	}
	probe := t.TempDir()
	if err := unix.Mount("overlay", probe, "overlay", 0, "lowerdir="+probe+":"+t.TempDir()); err != nil {
		t.Skipf("no overlay mounts in a user namespace here: %v", err)
	}
	_ = unix.Unmount(probe, 0)
	dir := t.TempDir()
	lower, upperfs, root := filepath.Join(dir, "lower"), filepath.Join(dir, "upperfs"), filepath.Join(dir, "root")
	for _, d := range []string{"lower/usr/bin", "lower/var/tmp", "lower/var/spool", "lower/etc", "upperfs/upper", "upperfs/work"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string, mode uint32) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := unix.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(lower, "usr/bin/su"), "su 1", 0o755)
	write(filepath.Join(lower, "usr/bin/passwd"), "passwd 1", 0o755)
	write(filepath.Join(lower, modesManifest), "4755 0 0 f /usr/bin/su\n4755 0 0 f /usr/bin/passwd\n1777 0 0 d /var/tmp\n1777 0 0 d /var/spool\n", 0o644)
	check := func(p, body string, mode uint32) {
		t.Helper()
		b, err := os.ReadFile(p)
		var st unix.Stat_t
		if err == nil {
			err = unix.Stat(p, &st)
		}
		if err != nil || string(b) != body || st.Mode&0o7777 != mode {
			t.Errorf("%s: %q %o (%v), want %q %o", p, b, st.Mode&0o7777, err, body, mode)
		}
	}
	absent := func(p string) {
		t.Helper()
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is there (%v)", p, err)
		}
	}
	fix := ""
	boot := func(n string) {
		t.Helper()
		fix = filepath.Join(dir, "fix"+n)
		lowerdir, dirs := imageModes(lower, fix)
		if lowerdir != fix+":"+lower {
			t.Fatalf("boot %s: lowerdir %s", n, lowerdir)
		}
		if err := mountRoot(root, lowerdir, upperfs); err != nil {
			t.Fatalf("boot %s: %v", n, err)
		}
		fixDirs(root, dirs)
	}
	halt := func() {
		t.Helper()
		for _, m := range []string{root, fix} {
			if err := unix.Unmount(m, 0); err != nil {
				t.Fatal(err)
			}
		}
	}

	boot("1")
	check(filepath.Join(root, "usr/bin/su"), "su 1", 0o4755)
	check(filepath.Join(root, "usr/bin/passwd"), "passwd 1", 0o4755)
	if st := lstat(t, filepath.Join(root, "var/tmp")); st.Mode&0o7777 != 0o1777 {
		t.Errorf("var/tmp is %o", st.Mode&0o7777)
	}
	absent(filepath.Join(upperfs, "upper/usr/bin/su")) // no file reaches the upper
	check(filepath.Join(lower, "usr/bin/su"), "su 1", 0o755)
	// the sandbox replaces passwd (apt) and makes its spool its own
	if err := os.Remove(filepath.Join(root, "usr/bin/passwd")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "usr/bin/passwd"), "passwd mine", 0o755)
	if err := unix.Chmod(filepath.Join(root, "var/spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	halt()

	// a newer base under the same upper (a rebase)
	write(filepath.Join(lower, "usr/bin/su"), "su 2", 0o755)
	boot("2")
	check(filepath.Join(root, "usr/bin/su"), "su 2", 0o4755)
	check(filepath.Join(root, "usr/bin/passwd"), "passwd mine", 0o755)
	if st := lstat(t, filepath.Join(root, "var/spool")); st.Mode&0o7777 != 0o700 {
		t.Errorf("the sandbox's var/spool is %o now", st.Mode&0o7777)
	}
	if st := lstat(t, filepath.Join(root, "var/tmp")); st.Mode&0o7777 != 0o1777 {
		t.Errorf("var/tmp is %o", st.Mode&0o7777)
	}
	halt()

	// an image without the list: as before
	if err := os.Remove(filepath.Join(lower, modesManifest)); err != nil {
		t.Fatal(err)
	}
	if lowerdir, dirs := imageModes(lower, filepath.Join(dir, "fix3")); lowerdir != lower || dirs != nil {
		t.Errorf("without a list: %s, %v", lowerdir, dirs)
	}
	absent(filepath.Join(dir, "fix3"))
}
