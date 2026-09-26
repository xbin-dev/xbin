//go:build linux

package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Emulated VMs (plans/vm-sandbox.md §Emulated VMs): on a host without a
// usable /dev/kvm — most small cloud VMs — QEMU's software emulation (TCG)
// runs the same kernel, initramfs and rootfs image as Firecracker would, on
// the same TAP, with the same disks. QEMU has no vsock device of its own, so
// vhost-device-vsock serves the guest's vsock over Firecracker's hybrid
// Unix-socket protocol at the same v.sock path: nothing past the VMM's start
// knows which VMM it is.

// qemuBootArgs is bootArgs with a triple-fault reboot: the microvm board has
// no keyboard controller to reset through, and -no-reboot turns the reset
// into QEMU's exit.
var qemuBootArgs = strings.Replace(bootArgs, "reboot=k", "reboot=t", 1)

// tbMiB caps QEMU's translated-code cache (it counts toward the VM's
// cgroup leaf; internal/vm.EmulatedOverheadMiB covers it).
const tbMiB = 256

// startQEMU cold-boots the VM under QEMU's emulation, with its vsock
// backend started first (QEMU connects to it as a vhost-user client).
func (s *shim) startQEMU() error {
	vhost := filepath.Join(s.hs.RunDir, "vhost-vsock.sock")
	// inside the VM sandbox, like the VMM itself (see startFirecracker)
	s.vsockd = exec.Command(s.hs.VsockDev, // exec-ok: runs inside the namespace sandbox
		"--guest-cid", "3",
		"--socket", vhost,
		"--uds-path", filepath.Join(s.hs.RunDir, "v.sock"),
	)
	s.vsockd.Stdout, s.vsockd.Stderr = s.serial, s.serial
	if err := s.vsockd.Start(); err != nil {
		return fmt.Errorf("vsock backend: %w", err)
	}
	s.vsockdDone = make(chan struct{})
	go func() { _ = s.vsockd.Wait(); close(s.vsockdDone) }()
	if err := waitSocket(vhost, s.vsockdDone, 10*time.Second); err != nil {
		return fmt.Errorf("vsock backend: %w", err)
	}

	cmdline := s.kernelArgs(qemuBootArgs)
	if khz := tscKHz(); khz > 0 {
		cmdline += " tsc_early_khz=" + strconv.Itoa(khz) // see tscKHz
	}
	mem := strconv.Itoa(max(128, s.hs.MemMiB))
	args := []string{
		"-L", s.hs.Firmware,
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		// pic=off: the guest never programs a legacy PIC, and its first
		// timer interrupt through one would arrive as vector 0 (#DE)
		"-machine", "microvm,pic=off,isa-serial=on,memory-backend=mem",
		"-accel", "tcg,thread=multi,tb-size=" + strconv.Itoa(tbMiB),
		"-cpu", "max",
		"-smp", strconv.Itoa(max(1, s.hs.VCPUs)),
		"-m", mem + "M",
		// shared: the vsock backend maps guest memory to reach the rings
		"-object", "memory-backend-memfd,id=mem,size=" + mem + "M,share=on",
		"-kernel", s.hs.Kernel,
		"-initrd", s.hs.Initrd,
		"-append", cmdline,
		"-serial", "stdio",
		"-drive", "id=rootfs,if=none,format=raw,readonly=on,file=" + qemuPath(s.hs.Image),
		"-device", "virtio-blk-device,drive=rootfs",
	}
	if s.hs.Disk != "" {
		args = append(args,
			"-drive", "id=disk,if=none,format=raw,cache=writeback,discard=unmap,file="+qemuPath(s.hs.Disk),
			"-device", "virtio-blk-device,drive=disk")
	}
	if s.hs.Tap != "" {
		args = append(args,
			"-netdev", "tap,id=net0,ifname="+s.hs.Tap+",script=no,downscript=no",
			"-device", "virtio-net-device,netdev=net0,mac="+s.hs.GuestMAC)
	}
	args = append(args,
		"-chardev", "socket,id=vsock,path="+qemuPath(vhost),
		"-device", "vhost-user-vsock-device,chardev=vsock",
		"-device", "virtio-balloon-device,deflate-on-oom=on,free-page-reporting=on",
		// QEMU's own syscall filter, inside the namespace sandbox's
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
	)
	return s.startVMM(exec.Command(s.hs.QEMU, args...)) // exec-ok: runs inside the namespace sandbox
}

// qemuPath escapes a path for a QEMU option value (commas double).
func qemuPath(p string) string { return strings.ReplaceAll(p, ",", ",,") }

// waitSocket waits for the Unix socket a process creates, or its exit.
func waitSocket(path string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-exited:
			return errors.New("exited before it listened")
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("didn't start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
