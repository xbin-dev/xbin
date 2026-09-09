// Package fsutil holds the durable-write primitive every on-disk store in
// xbind uses. Before it, nine stores did their own tmp-then-rename (none of
// them fsync'd) and three wrote in place — including the uid map, whose
// torn write would have orphaned chowned sqlite files.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path so that a reader sees either the old
// file or the complete new one, never a torn write, and the bytes are on
// disk before the name is: a temp file in the same directory
// (".<base>.<random>.tmp" — a name the workspace watcher ignores), chmod to
// perm before writing (CreateTemp's 0600 would otherwise tighten a 0644
// file), write, fsync, close, rename, fsync the directory. On any error the
// temp file is removed and the old file is untouched.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// WriteFileAtomicIn is WriteFileAtomic after creating the parent directory
// (0755) — for stores that appear on first write.
func WriteFileAtomicIn(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(path, data, perm)
}
