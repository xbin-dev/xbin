//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// RunInit is the re-exec entrypoint (buxond … __sandbox-init <specfile>). It
// runs inside the fresh namespaces as container-root, assembles the mount
// namespace, and execs the backend. It never returns on success.
func RunInit(specPath string) {
	if err := runInit(specPath); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-init:", err)
		os.Exit(127)
	}
}

func runInit(specPath string) error {
	runtime.LockOSThread()

	data, err := os.ReadFile(specPath)
	if err != nil {
		return must(err, "read spec")
	}
	os.Remove(specPath) // best-effort; not secret, but no reason to linger
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return must(err, "parse spec")
	}

	// Detach mount propagation so nothing we do leaks to the host.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return must(err, "make-rprivate /")
	}

	// A private tmpfs to hold the new root and (if needed) the overlay dirs.
	base := filepath.Join(os.TempDir(), fmt.Sprintf("bxroot.%d", os.Getpid()))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return must(err, "mkdir base")
	}
	if err := unix.Mount("tmpfs", base, "tmpfs", 0, "mode=0755"); err != nil {
		return must(err, "mount base tmpfs")
	}
	newroot := filepath.Join(base, "root")
	if err := os.Mkdir(newroot, 0o755); err != nil {
		return must(err, "mkdir newroot")
	}

	// Overlay: base rootfs + granted deps (lower, ro) with a per-component
	// writable upper. If the caller gave no Upper, use dirs on our private tmpfs.
	upper, work := s.Upper, s.Work
	if upper == "" {
		upper = filepath.Join(base, "up")
		work = filepath.Join(base, "work")
		if err := os.Mkdir(upper, 0o755); err != nil {
			return must(err, "mkdir upper")
		}
		if err := os.Mkdir(work, 0o755); err != nil {
			return must(err, "mkdir work")
		}
	}
	if len(s.Lower) == 0 {
		return fmt.Errorf("spec has no rootfs lowerdir")
	}
	opt := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", strings.Join(s.Lower, ":"), upper, work)
	if err := unix.Mount("overlay", newroot, "overlay", 0, opt); err != nil {
		return must(err, "mount overlay ("+opt+")")
	}

	// Fresh /proc (needs the new pid ns) and a private /tmp, /dev/shm — mounted
	// BEFORE the binds so that binds whose paths fall under /tmp (the run dir /
	// gateway socket use a /tmp fallback for the 108-byte unix-socket limit, and
	// the workspace itself may live under /tmp) land on top rather than being
	// shadowed.
	if err := mountAt(newroot, "proc", "proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	if err := mountAt(newroot, "tmp", "tmpfs", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777"); err != nil {
		return err
	}
	_ = mountAt(newroot, "dev/shm", "tmpfs", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")

	// Default minimal /dev nodes (bound from the host before we pivot away).
	for _, d := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		_ = bindNode(newroot, "/dev/"+d)
	}
	// Extra binds: component dir (ro), resource files (rw), gateway socket, …
	for _, b := range s.Binds {
		if err := mountBind(newroot, b); err != nil {
			return err
		}
	}

	// Egress relay: create the TUN in this netns and hand its fd to buxond,
	// which runs the userspace stack + policy. Without this the netns stays
	// empty = default-deny (plans/isolation.md §3).
	if s.Net == "relay" {
		if err := setupEgress(newroot); err != nil {
			return must(err, "egress")
		}
	}

	// pivot_root into the assembled tree.
	oldroot := filepath.Join(newroot, ".oldroot")
	if err := os.MkdirAll(oldroot, 0o700); err != nil {
		return must(err, "mkdir .oldroot")
	}
	if err := unix.PivotRoot(newroot, oldroot); err != nil {
		return must(err, "pivot_root")
	}
	if err := unix.Chdir("/"); err != nil {
		return must(err, "chdir /")
	}
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return must(err, "detach oldroot")
	}
	_ = os.Remove("/.oldroot")

	// Bring loopback up (best-effort; the netns is otherwise empty = deny).
	upLoopback()

	// Lock down and become the backend.
	_ = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	cwd := s.Cwd
	if cwd == "" {
		cwd = "/"
	}
	if err := unix.Chdir(cwd); err != nil {
		// Non-fatal: fall back to / so a bad Cwd doesn't wedge the backend.
		_ = unix.Chdir("/")
	}
	argv := s.Argv
	if len(argv) == 0 {
		argv = []string{s.Entry}
	}
	if err := unix.Exec(s.Entry, argv, s.Env); err != nil {
		return must(err, "exec "+s.Entry)
	}
	return nil // unreachable
}

// mountBind binds Src to <newroot>/Dst (recursively), creating the target, and
// remounts read-only if requested.
func mountBind(newroot string, b Bind) error {
	dst := filepath.Join(newroot, b.Dst)
	fi, err := os.Lstat(b.Src)
	if err != nil {
		return must(err, "bind src "+b.Src)
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return must(err, "mkdir "+dst)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return must(err, "mkdir "+filepath.Dir(dst))
		}
		if f, err := os.OpenFile(dst, os.O_CREATE, 0o644); err == nil {
			f.Close()
		}
	}
	if err := unix.Mount(b.Src, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return must(err, "bind "+b.Src+" -> "+b.Dst)
	}
	if b.RO {
		if err := remountRO(dst); err != nil {
			return must(err, "remount ro "+b.Dst)
		}
	}
	return nil
}

// remountRO makes an existing bind mount read-only. In a user namespace the
// kernel refuses a remount that would clear the source mount's *locked* flags
// (nodev/nosuid/noexec/atime), so we read them back via statfs and re-apply.
func remountRO(dst string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dst, &st); err != nil {
		return err
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY)
	for _, m := range []struct{ st, ms uintptr }{
		{unix.ST_NOSUID, unix.MS_NOSUID},
		{unix.ST_NODEV, unix.MS_NODEV},
		{unix.ST_NOEXEC, unix.MS_NOEXEC},
		{unix.ST_NOATIME, unix.MS_NOATIME},
		{unix.ST_NODIRATIME, unix.MS_NODIRATIME},
		{unix.ST_RELATIME, unix.MS_RELATIME},
	} {
		if uintptr(st.Flags)&m.st != 0 {
			flags |= m.ms
		}
	}
	return unix.Mount("", dst, "", flags, "")
}

// bindNode binds a single /dev node (as-is, so the device works) into newroot.
func bindNode(newroot, p string) error {
	if _, err := os.Stat(p); err != nil {
		return err
	}
	return mountBind(newroot, Bind{Src: p, Dst: p})
}

// mountAt mounts a fresh fs at <newroot>/rel (creating the dir).
func mountAt(newroot, rel, source, fstype string, flags uintptr, data string) error {
	dir := filepath.Join(newroot, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return must(err, "mkdir "+dir)
	}
	if err := unix.Mount(source, dir, fstype, flags, data); err != nil {
		return must(err, "mount "+fstype+" at "+rel)
	}
	return nil
}

// upLoopback sets lo UP via an ioctl on an AF_INET socket (no external tools).
func upLoopback() {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	var ifr [40]byte
	copy(ifr[:], "lo")
	// SIOCGIFFLAGS then set IFF_UP|IFF_RUNNING and SIOCSIFFLAGS.
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.SIOCGIFFLAGS), uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return
	}
	flags := uint16(ifr[16]) | uint16(ifr[17])<<8
	flags |= unix.IFF_UP | unix.IFF_RUNNING
	ifr[16] = byte(flags)
	ifr[17] = byte(flags >> 8)
	unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.SIOCSIFFLAGS), uintptr(unsafe.Pointer(&ifr)))
}
