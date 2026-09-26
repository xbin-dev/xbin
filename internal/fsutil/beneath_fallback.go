package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// openBeneathFallback resolves every symlink and compares; a racing swap
// between the check and the open can still win here, which is why Linux
// uses openat2.
func openBeneathFallback(dir, rel string) (*os.File, error) {
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if full != base && !strings.HasPrefix(full, base+string(filepath.Separator)) {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, rel), Err: ErrEscapes}
	}
	return os.Open(full)
}
