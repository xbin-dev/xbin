//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseDevModes(t *testing.T) {
	ds, bad := parseDevModes([]byte(strings.Join([]string{
		"# made by the coding sandbox",
		"0666 /dev/fuse",
		"  666   /dev/net/tun  ",
		"",
		"0666 /dev",                // the directory itself
		"0666 /dev/",               // ditto
		"0666 /etc/passwd",         // not a device path
		"0666 /dev/../etc/shadow",  // not clean
		"0666 /dev//fuse",          // ditto
		"4755 /dev/fuse",           // a special bit
		"rw /dev/fuse",             // not octal
		"0666 /dev/fuse /dev/kvm",  // two paths
		"0666 dev/fuse",            // relative
		"0660 /dev/kvm # a remark", // a trailing remark is two more fields
	}, "\n")))
	want := []devMode{{mode: 0o666, rel: "fuse"}, {mode: 0o666, rel: "net/tun"}}
	if fmt.Sprint(ds) != fmt.Sprint(want) || bad != 10 {
		t.Errorf("parsed %v (bad %d), want %v (bad 10)", ds, bad, want)
	}
	var b strings.Builder
	for i := 0; i < devModesMax+3; i++ {
		fmt.Fprintf(&b, "0666 /dev/d%d\n", i)
	}
	if ds, bad := parseDevModes([]byte(b.String())); len(ds) != devModesMax || bad != 3 {
		t.Errorf("past the bound: %d entries, bad %d", len(ds), bad)
	}
}

func TestReadDevModes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ds := readDevModes(root); ds != nil {
		t.Errorf("no file: %v", ds)
	}
	file := filepath.Join(root, devModesFile)
	if err := os.WriteFile(file, []byte("0666 /dev/fuse\n0666 /dev/net/tun\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ds := readDevModes(root); len(ds) != 2 {
		t.Errorf("read %v", ds)
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("#\n", devModesMaxBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if ds := readDevModes(root); ds != nil {
		t.Errorf("a file over the bound: %v", ds)
	}
	t.Run("behind a symbolic link: none", func(t *testing.T) {
		other := t.TempDir()
		if err := os.WriteFile(filepath.Join(other, "xbin-vm-devices"), []byte("0666 /dev/fuse\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := t.TempDir()
		if err := os.Symlink(other, filepath.Join(r, "etc")); err != nil {
			t.Fatal(err)
		}
		if ds := readDevModes(r); ds != nil {
			t.Errorf("read through a symbolic link: %v", ds)
		}
	})
}

// TestApplyDevModes: only a character device, reached without a symbolic
// link, gets its mode. Making one takes root; unprivileged, the device is a
// pseudo-terminal of the test's own (TestApplyDevModesPty).
func TestApplyDevModes(t *testing.T) {
	dev := t.TempDir()
	if err := os.WriteFile(filepath.Join(dev, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dev, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../file", filepath.Join(dev, "net", "link")); err != nil {
		t.Fatal(err)
	}
	ds := []devMode{{0o666, "file"}, {0o666, "net/link"}, {0o666, "missing"}}
	root := os.Getuid() == 0
	if root {
		if err := unix.Mknod(filepath.Join(dev, "null"), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("null", filepath.Join(dev, "nullink")); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, devMode{0o666, "null"}, devMode{0o666, "nullink"})
	}
	n := applyDevModes(dev, ds)
	if want := map[bool]int{false: 0, true: 1}[root]; n != want {
		t.Errorf("set %d, want %d", n, want)
	}
	if fi, _ := os.Lstat(filepath.Join(dev, "file")); fi.Mode().Perm() != 0o600 {
		t.Errorf("a regular file got %v", fi.Mode().Perm())
	}
	if root {
		if fi, _ := os.Stat(filepath.Join(dev, "null")); fi.Mode().Perm() != 0o666 {
			t.Errorf("the device is %v", fi.Mode().Perm())
		}
	}
}

// TestApplyDevModesPty sets a mode on a real character device without root:
// the slave of a pseudo-terminal the test opens is the test's own.
func TestApplyDevModesPty(t *testing.T) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Skip(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Skip(err)
	}
	slave := fmt.Sprint(n)
	for _, mode := range []uint32{0o600, 0o620} {
		if got := applyDevModes("/dev/pts", []devMode{{mode, slave}}); got != 1 {
			t.Fatalf("set %d", got)
		}
		var st unix.Stat_t
		if err := unix.Stat("/dev/pts/"+slave, &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&0o7777 != mode {
			t.Errorf("the slave is %o, want %o", st.Mode&0o7777, mode)
		}
	}
}
