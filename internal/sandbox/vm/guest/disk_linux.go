//go:build linux

package guest

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The persistent VM disk (plans/vm-sandbox.md): a sparse image the host
// sizes and the guest owns. The agent formats it only when it is blank —
// never on a mount error, which could be a filesystem worth fsck-ing — and
// grows the filesystem (offline, before mounting) when the host grew the
// image.

// mountDisk prepares and mounts the persistent upper disk at /upperfs.
func (a *agent) mountDisk(dev string) error {
	flushBlockdev(dev) // a restored template may hold blocks of the placeholder
	fsBytes, ok := ext4Size(dev)
	if !ok {
		if err := a.runLower("mkfs.ext4", "-F", "-q", "-E", "lazy_itable_init=1,lazy_journal_init=1,discard", "-L", "xbin-vm", dev); err != nil {
			return fmt.Errorf("format the VM disk: %w", err)
		}
	} else if devBytes := blockdevSize(dev); devBytes > fsBytes+(64<<20) {
		// the policy grew the disk: resize wants a clean check first
		if err := a.runLower("e2fsck", "-f", "-p", dev); err != nil {
			logf("e2fsck before growing the VM disk: %v", err)
		}
		if err := a.runLower("resize2fs", dev); err != nil {
			logf("growing the VM disk: %v (it keeps its old size)", err)
		}
	}
	// A VM can be killed at any moment (its terminal closed, xbind stopped):
	// commit the journal every second and write dirty pages back fast, so at
	// most about a second of changes is ever at risk.
	for k, v := range map[string]string{"dirty_expire_centisecs": "100", "dirty_writeback_centisecs": "100"} {
		_ = os.WriteFile("/proc/sys/vm/"+k, []byte(v), 0)
	}
	if err := mountAt(dev, "/upperfs", "ext4", unix.MS_NOATIME, "discard,commit=1"); err != nil {
		return fmt.Errorf("the VM disk doesn't mount (%v) — reset the terminal to start a fresh one", err)
	}
	return nil
}

// ext4Size reads dev's ext4 superblock: the filesystem's size, or ok=false
// when there is no ext4 there (a blank disk).
func ext4Size(dev string) (int64, bool) {
	f, err := os.Open(dev)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sb := make([]byte, 1024)
	if _, err := f.ReadAt(sb, 1024); err != nil {
		return 0, false
	}
	if binary.LittleEndian.Uint16(sb[0x38:]) != 0xEF53 {
		return 0, false
	}
	blocks := uint64(binary.LittleEndian.Uint32(sb[0x4:]))
	if binary.LittleEndian.Uint32(sb[0x60:])&0x80 != 0 { // INCOMPAT_64BIT
		blocks |= uint64(binary.LittleEndian.Uint32(sb[0x150:])) << 32
	}
	return int64(blocks) << (10 + binary.LittleEndian.Uint32(sb[0x18:])), true
}

func blockdevSize(dev string) int64 {
	fd, err := unix.Open(dev, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0
	}
	defer unix.Close(fd)
	var size uint64
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.BLKGETSIZE64, uintptr(unsafePtr(&size))); e != 0 {
		return 0
	}
	return int64(size)
}

// runLower runs a tool from the rootfs image (the initramfs holds only the
// agent), chrooted into /lower with /dev bound in. PID 1's reaper owns every
// wait, so the child goes through spawn.
func (a *agent) runLower(argv ...string) error {
	if err := unix.Mount("/dev", "/lower/dev", "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind /dev: %w", err)
	}
	defer unix.Unmount("/lower/dev", unix.MNT_DETACH)
	path := ""
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		if _, err := os.Stat("/lower" + dir + "/" + argv[0]); err == nil {
			path = dir + "/" + argv[0]
			break
		}
	}
	if path == "" {
		return fmt.Errorf("%s: not in the rootfs image", argv[0])
	}
	_, done, err := a.spawn(path, argv, &os.ProcAttr{
		Env:   []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"},
		Files: []*os.File{nil, os.Stderr, os.Stderr},
		Sys:   &syscall.SysProcAttr{Chroot: "/lower"},
	})
	if err != nil {
		return err
	}
	if ws := <-done; ws.ExitStatus() != 0 {
		return fmt.Errorf("%s exited %d", argv[0], ws.ExitStatus())
	}
	return nil
}

func unsafePtr(p *uint64) unsafe.Pointer { return unsafe.Pointer(p) }
