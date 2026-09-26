//go:build !linux

package vm

import "errors"

func kvmUsable() error { return errors.New("VM sandboxes need Linux with KVM") }
