//go:build !linux

package fsutil

import (
	"context"
	"errors"
)

// CloneSparse is Linux's (clone_linux.go): tile sandboxes run only there.
func CloneSparse(context.Context, string, string) (bool, error) {
	return false, errors.New("fsutil: CloneSparse needs Linux")
}

// Exchange is Linux's (clone_linux.go).
func Exchange(string, string) error { return errors.New("fsutil: Exchange needs Linux") }
