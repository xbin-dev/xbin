//go:build linux

package vm

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// kvmUsable opens /dev/kvm and checks the API version, as Firecracker will.
func kvmUsable() error {
	fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		switch err {
		case unix.ENOENT:
			return fmt.Errorf("/dev/kvm is missing (no hardware virtualization, or nested virtualization is off on this VM)")
		case unix.EACCES, unix.EPERM:
			return fmt.Errorf("/dev/kvm is not accessible to this user (add it to the kvm group: usermod -aG kvm <user>, then restart xbind)")
		}
		return fmt.Errorf("/dev/kvm: %w", err)
	}
	defer unix.Close(fd)
	const kvmGetAPIVersion = 0xAE00
	v, err := unix.IoctlRetInt(fd, kvmGetAPIVersion)
	if err != nil {
		return fmt.Errorf("/dev/kvm: %w", err)
	}
	if v != 12 {
		return fmt.Errorf("/dev/kvm: unexpected KVM API version %d", v)
	}
	return nil
}
