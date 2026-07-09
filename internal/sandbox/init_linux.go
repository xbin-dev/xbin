//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// mappedEnv marks the second stage of range-uid init (after the parent wrote
// our uid/gid maps and we re-exec'd to regain capabilities).
const mappedEnv = "BX_SANDBOX_MAPPED"

// RunInit is the re-exec entrypoint (xbind … __sandbox-init <specfile>). It
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
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		os.Remove(specPath)
		return must(err, "parse spec")
	}

	// Range-uid mode is two-stage. This first stage was exec'd as the unmapped
	// (overflow) uid, so execve stripped its capabilities. Wait for the parent to
	// write our uid/gid maps (via newuidmap), then re-exec: that execve runs as
	// now-mapped root, which regains full capabilities — required for the mount
	// setup below. Single-uid mode maps before exec, so it skips straight through.
	if s.SyncFD > 0 && os.Getenv(mappedEnv) != "1" {
		if err := awaitMaps(s.SyncFD); err != nil {
			os.Remove(specPath)
			return must(err, "await uid maps")
		}
		env := append(os.Environ(), mappedEnv+"=1")
		return must(unix.Exec("/proc/self/exe", []string{"xbind", InitArg, specPath}, env), "re-exec mapped")
	}
	os.Remove(specPath) // consume the spec (final stage, or single-uid mode)

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
	if s.FuseOverlay != "" {
		// fuse-overlayfs honors redirect_dir/metacopy (which unprivileged kernel
		// overlayfs forbids), so directory renames work → `apt install` etc. It
		// backgrounds itself once mounted; Run returns when the mount is ready.
		// CombinedOutput so its harmless mount-flag warnings (e.g. "lazytime")
		// don't print into every terminal; surface output only on failure.
		fo := exec.Command(s.FuseOverlay, "-o", opt, newroot)
		if out, err := fo.CombinedOutput(); err != nil {
			return must(fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out))), "fuse-overlayfs mount")
		}
	} else if err := unix.Mount("overlay", newroot, "overlay", 0, opt); err != nil {
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
	// Private tmpfs /dev: device-node binds (incl. conditional GPU nodes) land on
	// an ephemeral layer, never the overlay upper — so a persistent terminal layer
	// doesn't accumulate stale device placeholders. (No MS_NODEV — this is /dev.)
	if err := mountAt(newroot, "dev", "tmpfs", "tmpfs", unix.MS_NOSUID, "mode=0755"); err != nil {
		return err
	}
	// Standard /dev symlinks a fresh tmpfs lacks (programs expect /dev/stdin, …).
	for _, ln := range [][2]string{
		{"/proc/self/fd", "dev/fd"}, {"/proc/self/fd/0", "dev/stdin"},
		{"/proc/self/fd/1", "dev/stdout"}, {"/proc/self/fd/2", "dev/stderr"},
	} {
		_ = os.Symlink(ln[0], filepath.Join(newroot, ln[1]))
	}
	_ = mountAt(newroot, "dev/shm", "tmpfs", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")

	// Default minimal /dev nodes (bound from the host before we pivot away).
	for _, d := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		_ = bindNode(newroot, "/dev/"+d)
	}
	// A private devpts so programs can allocate PTYs (dpkg/debconf, tmux, script,
	// sudo). No gid=5 — the tty group may be unmapped in single-uid mode; nodes
	// are owned by the mounter, which is fine here. /dev/ptmx → the new instance.
	if err := mountAt(newroot, "dev/pts", "devpts", "devpts",
		unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"); err == nil {
		ptmx := filepath.Join(newroot, "dev", "ptmx")
		_ = os.Remove(ptmx)
		_ = os.Symlink("pts/ptmx", ptmx)
	}
	// /dev/fuse, for the fuse-overlayfs backend (and anything else using FUSE).
	if s.FuseOverlay != "" {
		_ = bindNode(newroot, "/dev/fuse")
	}
	// Extra binds: component dir (ro), resource files (rw), gateway socket, …
	// Mounted ancestors-first (sortBinds) so overlapping binds nest instead of
	// a later broad mount shadowing an earlier deeper one.
	for _, b := range sortBinds(s.Binds) {
		if err := mountBind(newroot, b); err != nil {
			return err
		}
	}

	// Egress relay: create the TUN in this netns and hand its fd to xbind,
	// which runs the userspace stack + policy. Without this the netns stays
	// empty = default-deny (plans/isolation.md §3).
	if s.Net == "relay" || s.Net == "splice" {
		if err := setupEgress(newroot, &s); err != nil {
			return must(err, "egress")
		}
	}
	// Sharing the host network (terminals): give the sandbox the host's DNS +
	// hosts so name resolution / internet works (the rootfs's own resolv.conf is
	// typically empty). We're still on the host root here (pre-pivot).
	if s.HostNet {
		copyHostFile(newroot, "/etc/resolv.conf")
		copyHostFile(newroot, "/etc/hosts")
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

	// Bring loopback up in our own netns (skip when sharing the host's).
	if !s.HostNet {
		upLoopback()
	}

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

// awaitMaps blocks until the parent writes our uid/gid maps and signals via the
// sync fd (one byte). EOF means the parent failed to map us — abort rather than
// run privileged as the overflow uid.
func awaitMaps(fd int) error {
	f := os.NewFile(uintptr(fd), "bx-sync")
	defer f.Close()
	var b [1]byte
	if n, err := f.Read(b[:]); err != nil || n == 0 {
		return fmt.Errorf("parent did not complete uid mapping")
	}
	return nil
}

// mountBind binds Src to <newroot>/Dst (recursively), creating the target, and
// remounts read-only if requested.
func mountBind(newroot string, b Bind) error {
	dst := filepath.Join(newroot, b.Dst)
	if b.Mask {
		return mountMask(dst, b.RO)
	}
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

// mountMask shadows dst with an empty tmpfs, hiding whatever is beneath it
// (workspace secrets, other users' homes) from the sandbox — the caller relies
// on the emptiness, not on permissions (the process is uid 0 in its userns).
// If dst doesn't exist there's nothing to hide. ro seals the cover; otherwise
// it stays writable so a deeper bind (the own $HOME under a masked homes/) can
// nest on top. Mounting over dst never needs the underlying (read-only) tree to
// be writable — only the mountpoint to exist.
func mountMask(dst string, ro bool) error {
	if _, err := os.Lstat(dst); err != nil {
		return nil // nothing beneath to hide
	}
	// A sealed cover (ro) is mode 0000 — nothing to traverse. A writable cover
	// stays traversable (0755) so a deeper bind under it is reachable even when
	// the shell isn't uid 0 in its userns (scope-uids): the own $HOME nests
	// under the homes cover, and other homes are hidden by being shadowed, not
	// by permissions.
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	mode := "mode=0755"
	if ro {
		flags |= unix.MS_RDONLY
		mode = "mode=0000"
	}
	if err := unix.Mount("tmpfs", dst, "tmpfs", flags, mode); err != nil {
		return must(err, "mask "+dst)
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

// copyHostFile copies a host config file (following symlinks, e.g. a
// systemd-resolved stub) into newroot at the same path — used to seed DNS for
// host-network sandboxes.
func copyHostFile(newroot, p string) {
	data, err := os.ReadFile(p) // reads through symlinks; we're pre-pivot on host root
	if err != nil {
		return
	}
	dst := filepath.Join(newroot, p)
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, data, 0o644)
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
