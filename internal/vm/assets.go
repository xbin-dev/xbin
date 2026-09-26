// Package vm is xbind's side of VM sandboxes (plans/vm-sandbox.md): it
// finds the pieces (Firecracker, the guest kernel, the guest agent, the
// static bx that becomes the shim), probes whether KVM is usable, builds the
// guest's read-only rootfs image and initramfs, and turns an ordinary
// namespace-sandbox Spec into a VM one (Apply). The sandbox package runs it;
// the shim and the guest agent live under internal/sandbox/vm.
package vm

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Assets are the host paths of a VM sandbox's pieces.
type Assets struct {
	Firecracker string // the VMM (static)
	Kernel      string // guest vmlinux
	Agent       string // xbin-vmagent, packed as the initramfs
	Mkfs        string // mkfs.erofs (static), builds the rootfs image
	Bx          string // static bx: `bx __vm-host` is the shim
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
// The error names what is missing and how to get it.
func FindAssets(bx string) (Assets, error) {
	a := Assets{
		Firecracker: resolve("XBIN_FIRECRACKER", "firecracker", true),
		Kernel:      resolve("XBIN_VM_KERNEL", "vmlinux", false),
		Agent:       resolve("XBIN_VM_AGENT", "xbin-vmagent", false),
		Mkfs:        resolve("XBIN_MKFS_EROFS", "mkfs.erofs", true),
		Bx:          bx,
	}
	var missing []string
	for _, m := range []struct{ path, what string }{
		{a.Firecracker, "firecracker (hack/fetch-firecracker.sh or XBIN_FIRECRACKER)"},
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
	for _, p := range []string{a.Bx, a.Firecracker, a.Agent} {
		if err := static(p); err != nil {
			return a, err
		}
	}
	return a, nil
}

// static refuses a dynamically linked binary: the shim, Firecracker and the
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
