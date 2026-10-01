//go:build !linux

package resenc

import (
	"syscall"
	"time"
)

// daemon is a gocryptfs process serving a cipher directory; only Linux
// finds them (daemon_linux.go).
type daemon struct{ pid int }

func daemonsOn(string) []daemon        { return nil }
func (daemon) wait(time.Duration) bool { return true }
func (daemon) signal(syscall.Signal)   {}
func closeDaemons([]daemon)            {}
