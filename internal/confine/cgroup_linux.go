package confine

import (
	"os"
	"os/exec"
	"syscall"
)

// startInCgroup makes cmd start inside the cgroup dir names (Cmd.Cgroup).
func startInCgroup(cmd *exec.Cmd, dir *os.File) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, int(dir.Fd())
}
