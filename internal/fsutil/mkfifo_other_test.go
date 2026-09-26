//go:build !unix

package fsutil

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs here") }
