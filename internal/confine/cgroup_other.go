//go:build !linux

package confine

import (
	"os"
	"os/exec"
)

// startInCgroup: no cgroups here (and no sandbox either).
func startInCgroup(*exec.Cmd, *os.File) {}
