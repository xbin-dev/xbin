//go:build linux

package guest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The device modes a sandbox's root asks for (D182). /dev is a fresh
// devtmpfs at every boot and the guest runs no udev, so its nodes are root's
// (fuse and net/tun 0600) whatever the sandbox set the boot before. A root
// that wants some of them usable by its other users lists them in
// devModesFile, "<mode> <path>" a line ("0666 /dev/fuse"), and the guest sets
// them at each boot, before anything runs — what a stock distribution's udev
// rules do for fuse and tun, which rootless containers need. The coding
// sandbox writes it for an image whose user may sudo. Only character devices
// in this boot's devtmpfs (no symbolic link, no other mount on the way), only
// permission bits. The file is the sandbox's own (its root writes /etc), so
// it grants nothing that root couldn't; no file changes nothing.

const (
	devModesFile     = "etc/xbin-vm-devices" // in the sandbox's root
	devModesMaxBytes = 4 << 10
	devModesMax      = 64
)

// devMode is one line of the file.
type devMode struct {
	mode uint32 // permission bits
	rel  string // beneath /dev, clean
}

// parseDevModes reads the file: blank lines and '#' comments are skipped,
// and so is (counted in bad) a line that isn't "<octal mode ≤ 0777>
// /dev/<path>". At most devModesMax entries count.
func parseDevModes(b []byte) (ds []devMode, bad int) {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(ds) >= devModesMax {
			bad++
			continue
		}
		mode, err := strconv.ParseUint(f[0], 8, 32)
		rel, ok := strings.CutPrefix(f[1], "/dev/")
		if err != nil || mode > 0o777 || !ok || rel == "" || path.Clean(f[1]) != f[1] {
			bad++
			continue
		}
		ds = append(ds, devMode{mode: uint32(mode), rel: rel})
	}
	return ds, bad
}

// readDevModes reads root's file (nil: none, or one that can't be read).
func readDevModes(root string) []devMode {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(rfd)
	fd, err := openBeneath(rfd, devModesFile, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		logf("device modes: /%s: %v", devModesFile, err)
		return nil
	}
	_ = unix.SetNonblock(fd, false)
	f := os.NewFile(uintptr(fd), devModesFile)
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		logf("device modes: /%s is not a regular file", devModesFile)
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, devModesMaxBytes+1))
	if err != nil || len(b) > devModesMaxBytes {
		logf("device modes: /%s: unreadable or over %d bytes — none applied", devModesFile, devModesMaxBytes)
		return nil
	}
	ds, bad := parseDevModes(b)
	if bad > 0 {
		logf("device modes: /%s: %d line(s) skipped", devModesFile, bad)
	}
	return ds
}

// applyDevModes sets each mode on its node beneath dev (the boot's
// devtmpfs), which must be a character device reached without a symbolic
// link or another mount. Answers how many it set.
func applyDevModes(dev string, ds []devMode) int {
	dfd, err := unix.Open(dev, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("device modes: %v", err)
		return 0
	}
	defer unix.Close(dfd)
	set := 0
	for _, d := range ds {
		if err := chmodDev(dfd, d); err != nil {
			logf("device modes: /dev/%s: %v", d.rel, err)
			continue
		}
		set++
	}
	return set
}

// chmodDev: an O_PATH open (no open of the device itself: tun's or fuse's
// open allocates), checked, then the mode through its /proc/self/fd link.
func chmodDev(dfd int, d devMode) error {
	fd, err := openBeneath(dfd, d.rel, unix.O_PATH)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return errors.New("not a character device")
	}
	return unix.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), d.mode)
}

// deviceModes applies root's file to root's /dev (assembleRoot, once the
// kernel filesystems are mounted in it).
func deviceModes(root string) {
	ds := readDevModes(root)
	if len(ds) == 0 {
		return
	}
	n := applyDevModes(path.Join(root, "dev"), ds)
	logf("device modes: %d of %d set", n, len(ds))
}
