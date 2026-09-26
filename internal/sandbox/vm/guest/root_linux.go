//go:build linux

package guest

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// earlyMounts gives the initramfs the kernel filesystems the agent itself
// needs (block devices, vsock, /proc for the process table).
func earlyMounts() error {
	for _, m := range []struct {
		src, dst, typ string
		flags         uintptr
		data          string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
	} {
		if err := os.MkdirAll(m.dst, 0o755); err != nil {
			return err
		}
		if err := unix.Mount(m.src, m.dst, m.typ, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}
	return nil
}

// configure applies Config once: the clock, the network, the root, the 9P
// mounts, then switches the agent itself into the new root.
func (a *agent) configure(c proto.Config) error {
	a.mu.Lock()
	done := a.configured
	a.mu.Unlock()
	if done {
		return errors.New("already configured")
	}
	if c.Time != 0 {
		ts := unix.NsecToTimespec(c.Time)
		_ = unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
	}
	if c.Hostname != "" {
		_ = unix.Sethostname([]byte(c.Hostname))
	}
	upLoopback()
	if c.Net != nil {
		if err := configNet(c.Net); err != nil {
			return fmt.Errorf("network: %w", err)
		}
	}
	if err := a.assembleRoot(c.Root); err != nil {
		return err
	}
	for _, m := range c.Mounts {
		if err := mount9p(m); err != nil {
			return fmt.Errorf("mount %s: %w", m.Path, err)
		}
	}
	if err := writeEtc(c); err != nil {
		return err
	}
	if err := switchRoot(); err != nil {
		return err
	}
	a.mu.Lock()
	a.configured = true
	a.mu.Unlock()
	return nil
}

const newRoot = "/newroot"

// assembleRoot builds the workload's root at /newroot: an overlay whose lower
// is the read-only rootfs image and whose upper is the persistent VM disk or
// a tmpfs, plus fresh kernel filesystems inside it.
func (a *agent) assembleRoot(r proto.Root) error {
	typ := r.ImageType
	if typ == "" {
		typ = "erofs"
	}
	if err := mountAt(r.Image, "/lower", typ, unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("rootfs image: %w", err)
	}
	if r.Upper != "" {
		if err := a.mountDisk(r.Upper); err != nil { // disk_linux.go
			return err
		}
	} else if err := mountAt("tmpfs", "/upperfs", "tmpfs", 0, "mode=0755"); err != nil {
		return err
	}
	for _, d := range []string{"/upperfs/upper", "/upperfs/work"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	opt := "lowerdir=/lower,upperdir=/upperfs/upper,workdir=/upperfs/work,redirect_dir=on,metacopy=off"
	if err := mountAt("overlay", newRoot, "overlay", 0, opt); err != nil {
		// older overlay builds without redirect_dir
		if err := mountAt("overlay", newRoot, "overlay", 0, "lowerdir=/lower,upperdir=/upperfs/upper,workdir=/upperfs/work"); err != nil {
			return fmt.Errorf("root overlay: %w", err)
		}
	}
	for _, m := range []struct {
		src, dst, typ string
		flags         uintptr
		data          string
	}{
		{"proc", "proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"cgroup2", "sys/fs/cgroup", "cgroup2", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"devpts", "dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620,gid=5"},
		{"tmpfs", "dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"tmpfs", "run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"tmpfs", "tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	} {
		if err := mountAt(m.src, filepath.Join(newRoot, m.dst), m.typ, m.flags, m.data); err != nil {
			return err
		}
	}
	ptmx := filepath.Join(newRoot, "dev", "ptmx")
	_ = os.Remove(ptmx)
	_ = os.Symlink("pts/ptmx", ptmx)
	return nil
}

// mount9p dials the host's file server and mounts one export at its host path
// inside the new root. A file export is mounted over a file.
func mount9p(m proto.Mount) error {
	dst := filepath.Join(newRoot, path.Clean("/"+m.Path))
	if m.File {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if f, err := os.OpenFile(dst, os.O_CREATE|os.O_RDONLY, 0o644); err == nil {
			f.Close()
		}
	} else if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	fd, err := dialHost(proto.P9Port)
	if err != nil {
		return fmt.Errorf("dial file server: %w", err)
	}
	defer unix.Close(fd) // the mount holds its own reference
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if m.RO {
		flags |= unix.MS_RDONLY
	}
	opts := fmt.Sprintf("trans=fd,rfdno=%d,wfdno=%d,version=9p2000.L,msize=%d,cache=mmap,access=client,aname=%s",
		fd, fd, 1<<20, path.Clean("/"+m.Path))
	return unix.Mount("xbin", dst, "9p", flags, opts)
}

// writeEtc points the new root's resolver at the relay and names the host.
func writeEtc(c proto.Config) error {
	etc := filepath.Join(newRoot, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return err
	}
	if c.Net != nil {
		rc := filepath.Join(etc, "resolv.conf")
		_ = os.Remove(rc) // often a symlink into systemd-resolved's runtime dir
		if err := os.WriteFile(rc, []byte("nameserver "+c.Net.DNS+"\noptions single-request\n"), 0o644); err != nil {
			return err
		}
	}
	if c.Hostname != "" {
		_ = os.WriteFile(filepath.Join(etc, "hostname"), []byte(c.Hostname+"\n"), 0o644)
		hosts := filepath.Join(etc, "hosts")
		b, _ := os.ReadFile(hosts)
		if !strings.Contains(string(b), " "+c.Hostname) {
			b = append(b, []byte("127.0.1.1 "+c.Hostname+"\n")...)
			_ = os.WriteFile(hosts, b, 0o644)
		}
	}
	return nil
}

// switchRoot moves /newroot over / and chroots the agent into it (the
// initramfs switch_root dance; pivot_root can't leave rootfs).
func switchRoot() error {
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	return unix.Chdir("/")
}

func mountAt(src, dst, typ string, flags uintptr, data string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var err error
	for i := 0; i < 50; i++ {
		// a block device can appear a moment after the agent does
		if err = unix.Mount(src, dst, typ, flags, data); !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENXIO) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("mount %s (%s) at %s: %w", src, typ, dst, err)
	}
	return nil
}

// flushBlockdev drops the buffer cache of dev (BLKFLSBUF): a restored
// template may hold blocks it read from the placeholder disk at boot.
func flushBlockdev(dev string) {
	fd, err := unix.Open(dev, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	_ = unix.IoctlSetInt(fd, unix.BLKFLSBUF, 0)
}
