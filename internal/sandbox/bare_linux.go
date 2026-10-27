//go:build linux

package sandbox

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// A bare sandbox (Spec.Bare): an empty tmpfs root, the binds, the host's
// DNS files bound read-only when it shares the host network, sealed
// read-only before pivot_root. The iroh sidecar runs in one
// (internal/apptunnel/sidecar); a VM's root is bare the same way.

// bareRoot mounts an empty tmpfs as the new root: a VM sandbox's, and a
// Spec.Bare one's (sealed read-only before pivot_root).
func bareRoot(newroot string) error {
	return must(unix.Mount("tmpfs", newroot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"), "bare root tmpfs")
}

// hostNetFiles gives a host-network sandbox the host's DNS and hosts, so
// name resolution works (a rootfs's own resolv.conf is typically empty):
// copied into an overlay root, bound read-only into a bare one (it is
// sealed). Runs pre-pivot, on the host's root.
func hostNetFiles(newroot string, s *Spec) error {
	if !s.HostNet {
		return nil
	}
	for _, p := range []string{"/etc/resolv.conf", "/etc/hosts"} {
		if s.Bare && s.VM == nil {
			if err := bindHostFile(newroot, p); err != nil {
				return err
			}
		} else {
			copyHostFile(newroot, p)
		}
	}
	return nil
}

// sealBareRoot remounts a bare root read-only once everything is in it
// (the .oldroot mount point too): nothing in the sandbox can add a file to
// it. Its /tmp stays writable.
func sealBareRoot(newroot string, s *Spec) error {
	if !s.Bare || s.VM != nil {
		return nil
	}
	err := unix.Mount("", newroot, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "")
	dbg(s.Debug, "bare root sealed read-only (%v)", err)
	return must(err, "seal the bare root")
}

// execPath is what the init execs: the entry, or the program passed open
// (Spec.Exe) through the new /proc.
func execPath(s *Spec) string {
	if s.ExeFD > 0 {
		return fdPath(s.ExeFD)
	}
	return s.Entry
}

// bindHostFile binds a host config file read-only at the same path in
// newroot, its symlinks (a systemd-resolved stub) resolved on the host
// first; a file the host lacks is skipped.
func bindHostFile(newroot, p string) error {
	src, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil
	}
	if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	return mountBind(newroot, Bind{Src: src, Dst: p, RO: true})
}

// copyHostFile copies a host config file (following symlinks, e.g. a
// systemd-resolved stub) into newroot at the same path — used to seed DNS for
// host-network sandboxes. The write never follows a symlink in newroot
// (writeInRoot): the root may be a persistent upper the sandbox wrote.
func copyHostFile(newroot, p string) {
	data, err := os.ReadFile(p) // reads through symlinks; we're pre-pivot on host root
	if err != nil {
		return
	}
	_ = writeInRoot(newroot, p, data)
}
