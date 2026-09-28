//go:build !linux

package tilesbx

import (
	"os"
	"os/exec"
)

// useCgroupFD does nothing off Linux: there are no cgroups.
func useCgroupFD(*exec.Cmd, *os.File) {}

func cgroupFDUnsupported(error) bool { return false }
