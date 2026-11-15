//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// TestPrepareScriptSudoDevices: with "sudo" the script gives the sandbox's
// /dev/fuse and /dev/net/tun the modes it lists in /etc/xbin-vm-devices in
// the boot it runs in — the guest applies the list only as it boots, and a
// sandbox's first boot is before the script — and only to device nodes: a
// regular file or a symbolic link standing there keeps its mode.
//
// The device is a pty of the test's own, bound over <root>/dev/fuse in a
// mount namespace (an unprivileged process can make no device node, and a
// pty's slave is a character device it owns); the script then runs as the
// test's uid again, in a user namespace inside that one. Skips where no
// such namespace can be made.
func TestPrepareScriptSudoDevices(t *testing.T) {
	for _, tool := range []string{"sh", "awk", "stat", "mv", "chmod", "unshare", "mount"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s here", tool)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("as root the layout's user would be root, which the script leaves alone")
	}
	if b, err := exec.Command("unshare", "-Urm", "true").CombinedOutput(); err != nil {
		t.Skipf("no unprivileged user and mount namespaces here: %v %s", err, b)
	}
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer ptmx.Close()
	var n, unlock uint32
	for _, c := range []struct {
		req uintptr
		arg *uint32
	}{{syscall.TIOCSPTLCK, &unlock}, {syscall.TIOCGPTN, &n}} {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), c.req, uintptr(unsafe.Pointer(c.arg))); e != 0 {
			t.Skipf("no pty here: %v", e)
		}
	}
	pts := fmt.Sprintf("/dev/pts/%d", n)
	if err := os.Chmod(pts, 0o600); err != nil {
		t.Skipf("the pty isn't the test's to change: %v", err)
	}
	mode := func(p string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}

	dir := t.TempDir()
	for _, d := range []string{"etc", "dev/net"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	passwd := fmt.Sprintf("root:x:0:0:root:/root:/bin/bash\nubuntu:x:%d:%d::/home/ubuntu:/bin/sh\n", uid, gid)
	plain, other := filepath.Join(dir, "dev/net/tun"), filepath.Join(dir, "other")
	for p, body := range map[string]string{filepath.Join(dir, "etc/passwd"): passwd, filepath.Join(dir, "dev/fuse"): "", plain: "", other: ""} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// $1 the pty, $2 the root, $3 the script, the rest its arguments
	const wrap = `set -e
pts=$1 root=$2 script=$3; shift 3
mount --bind "$pts" "$root/dev/fuse"
cd "$root"
exec unshare -U --map-user="$UID_IN" --map-group="$GID_IN" sh -c "$script" prepare "$@"`
	run := func(sudo string) {
		t.Helper()
		cmd := exec.Command("unshare", "-Urm", "sh", "-c", wrap, "wrap", pts, dir, prepareScript,
			filepath.Join(dir, "work"), filepath.Join(dir, "home/dev"), fmt.Sprintf("%d:%d", uid, gid), "dev", "/bin/bash", sudo)
		cmd.Env = append(os.Environ(), fmt.Sprint("UID_IN=", uid), fmt.Sprint("GID_IN=", gid))
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the script (sudo %q): %v\n%s", sudo, err, b)
		}
	}

	run("")
	if got := mode(pts); got != 0o600 {
		t.Errorf("without sudo the device is %v, want it left 0600", got)
	}
	run("sudo")
	if got := mode(pts); got != 0o666 {
		t.Errorf("with sudo the device is %v in the boot the script ran in, want 0666 (/etc/xbin-vm-devices is read only at the next boot)", got)
	}
	if got := mode(plain); got != 0o600 {
		t.Errorf("a regular file at dev/net/tun is %v now, want it left 0600: only device nodes", got)
	}
	// a symbolic link there is never followed
	if err := os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, plain); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pts, 0o600); err != nil {
		t.Fatal(err)
	}
	run("sudo")
	if got := mode(other); got != 0o600 {
		t.Errorf("the file a symbolic link at dev/net/tun names is %v now, want it left 0600", got)
	}
	if got := mode(pts); got != 0o666 {
		t.Errorf("a second run: the device is %v, want 0666", got)
	}
}
