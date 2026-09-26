// Package vm is xbind's side of VM sandboxes (plans/vm-sandbox.md): it
// finds the pieces (Firecracker, or QEMU to emulate where KVM isn't usable;
// the guest kernel, the guest agent, the static bx that becomes the shim),
// probes whether KVM is usable, builds the guest's read-only rootfs image
// and initramfs, and turns an ordinary namespace-sandbox Spec into a VM one
// (Apply). The sandbox package runs it;
// the shim and the guest agent live under internal/sandbox/vm.
package vm

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Assets are the host paths of a VM sandbox's pieces.
type Assets struct {
	Firecracker string // the VMM under KVM (static)
	// Emulation, for hosts without a usable KVM: QEMU (static, software
	// emulation only), the vhost-user backend serving its vsock, and QEMU's
	// two boot blobs (qboot and the PVH option ROM).
	QEMU, VsockDev, BIOS, PVH string
	Kernel                    string // guest vmlinux
	Agent                     string // xbin-vmagent, packed as the initramfs
	Mkfs                      string // mkfs.erofs (static), builds the rootfs image
	Bx                        string // static bx: `bx __vm-host` is the shim
}

// resolve finds one asset: $env, then next to the xbind binary, then PATH
// (binaries only) — the fuse-overlayfs / gocryptfs convention.
func resolve(env, name string, onPath bool) string {
	if p := os.Getenv(env); p != "" {
		if isFile(p) {
			return p
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(exe), name); isFile(cand) {
			return cand
		}
	}
	if onPath {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// FindAssets locates every piece; bx is the daemon's (term.Manager.BxPath).
// The error names what is missing of the pieces both VMMs need, and how to
// get it; kvm and emulation say what each VMM lacks.
func FindAssets(bx string) (Assets, error) {
	a := Assets{
		Firecracker: resolve("XBIN_FIRECRACKER", "firecracker", true),
		QEMU:        resolve("XBIN_QEMU", "qemu-system-x86_64", false),
		VsockDev:    resolve("XBIN_VHOST_VSOCK", "vhost-device-vsock", false),
		Kernel:      resolve("XBIN_VM_KERNEL", "vmlinux", false),
		Agent:       resolve("XBIN_VM_AGENT", "xbin-vmagent", false),
		Mkfs:        resolve("XBIN_MKFS_EROFS", "mkfs.erofs", true),
		Bx:          bx,
	}
	if a.QEMU != "" {
		// the blobs travel with QEMU (hack/build-qemu.sh)
		dir := filepath.Dir(a.QEMU)
		if p := filepath.Join(dir, "qemu-bios-microvm.bin"); isFile(p) {
			a.BIOS = p
		}
		if p := filepath.Join(dir, "qemu-pvh.bin"); isFile(p) {
			a.PVH = p
		}
	}
	var missing []string
	for _, m := range []struct{ path, what string }{
		{a.Kernel, "the guest kernel vmlinux (hack/build-vmkernel.sh or XBIN_VM_KERNEL)"},
		{a.Agent, "xbin-vmagent (make build or XBIN_VM_AGENT)"},
		{a.Mkfs, "mkfs.erofs (hack/build-mkfs-erofs.sh or XBIN_MKFS_EROFS)"},
		{a.Bx, "the bx CLI (XBIN_BIN)"},
	} {
		if m.path == "" {
			missing = append(missing, m.what)
		}
	}
	if len(missing) > 0 {
		return a, fmt.Errorf("missing %v", missing)
	}
	for _, p := range []string{a.Bx, a.Agent} {
		if err := static(p); err != nil {
			return a, err
		}
	}
	return a, nil
}

// kvm says what Firecracker's side lacks ("" = nothing).
func (a Assets) kvm() error {
	if a.Firecracker == "" {
		return errors.New("firecracker is missing (hack/fetch-firecracker.sh or XBIN_FIRECRACKER)")
	}
	return static(a.Firecracker)
}

// emulation says what QEMU's side lacks ("" = nothing).
func (a Assets) emulation() error {
	var missing []string
	for _, m := range []struct{ path, what string }{
		{a.QEMU, "qemu-system-x86_64 (hack/build-qemu.sh or XBIN_QEMU)"},
		{a.BIOS, "qemu-bios-microvm.bin next to it"},
		{a.PVH, "qemu-pvh.bin next to it"},
		{a.VsockDev, "vhost-device-vsock (hack/build-vhost-vsock.sh or XBIN_VHOST_VSOCK)"},
	} {
		if m.path == "" {
			missing = append(missing, m.what)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("emulation needs %s", strings.Join(missing, ", "))
	}
	for _, p := range []string{a.QEMU, a.VsockDev} {
		if err := static(p); err != nil {
			return err
		}
	}
	return nil
}

// static refuses a dynamically linked binary: the shim, the VMM and the
// agent run where there is no libc (a bare tmpfs root, the initramfs).
func static(p string) error {
	f, err := elf.Open(p)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	defer f.Close()
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return fmt.Errorf("%s is dynamically linked; VM sandboxes need a static build (CGO_ENABLED=0)", p)
		}
	}
	return nil
}

// ErrUnavailable wraps every reason a VM sandbox can't start here.
var ErrUnavailable = errors.New("VM sandboxes unavailable")
