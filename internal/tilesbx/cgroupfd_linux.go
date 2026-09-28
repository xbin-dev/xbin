//go:build linux

package tilesbx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// useCgroupFD starts cmd straight into the cgroup dir (clone3
// CLONE_INTO_CGROUP): nothing the child runs or forks is ever outside it.
func useCgroupFD(cmd *exec.Cmd, dir *os.File) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(dir.Fd())
}

// cgroupFDUnsupported reports a start that failed because the kernel can't
// start into a cgroup (before 5.7: no clone3, or no CLONE_INTO_CGROUP).
func cgroupFDUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL)
}
