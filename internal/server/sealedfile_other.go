//go:build !linux

package server

import "os"

// memFile is a file holding data, positioned at its start: a temporary
// file removed before it is used (no memfd outside Linux), so only this
// descriptor reaches it.
func memFile(data []byte) (*os.File, error) {
	f, err := os.CreateTemp("", "xbin-sealed-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
