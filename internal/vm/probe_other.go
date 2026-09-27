//go:build !linux

package vm

import (
	"errors"
	"os"
)

func kvmUsable() error { return errors.New("VM sandboxes need Linux with KVM") }

func allocated(fi os.FileInfo) int64 { return fi.Size() }
