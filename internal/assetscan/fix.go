package assetscan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Change is one file the codemod rewrites.
type Change struct {
	File   string    // tile-relative
	Before []byte    // the file as read
	After  []byte    // the file as it would be written
	Edits  []Finding // the references replaced, in file order
}

// Plan scans tile (at dir) and returns, per file, the rewrite of every
// fixable reference (Finding.Fix != ""), plus the report the plan came from
// (its non-fixable findings are what a human must look at).
func Plan(dir, tile string, opt Options) ([]Change, Report, error) {
	rep, err := Scan(dir, tile, opt)
	if err != nil {
		return nil, rep, err
	}
	byFile := map[string][]Finding{}
	for _, f := range rep.Findings {
		if f.Fix != "" {
			byFile[f.File] = append(byFile[f.File], f)
		}
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	var out []Change
	for _, rel := range files {
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || !fi.Mode().IsRegular() {
			continue // a symlinked file is fixed at its target, which the scan also visits
		}
		edits := byFile[rel]
		before, err := readBeneath(dir, rel, 1<<30)
		if err != nil {
			return nil, rep, err
		}
		after := make([]byte, 0, len(before))
		prev := 0
		for _, e := range edits { // sorted by start (scanFile)
			if e.start < prev || e.end > len(before) || string(before[e.start:e.end]) != e.Ref {
				return nil, rep, fmt.Errorf("%s changed while planning; run again", rel)
			}
			after = append(after, before[prev:e.start]...)
			after = append(after, e.Fix...)
			prev = e.end
		}
		after = append(after, before[prev:]...)
		out = append(out, Change{File: rel, Before: before, After: after, Edits: edits})
	}
	return out, rep, nil
}

// Apply writes a plan's changes atomically, keeping each file's mode. A file
// that changed since it was planned is refused (nothing of it is written).
func Apply(dir string, changes []Change) error {
	for _, c := range changes {
		now, err := readBeneath(dir, c.File, 1<<30)
		if err != nil {
			return err
		}
		if string(now) != string(c.Before) {
			return fmt.Errorf("%s changed since it was scanned; run again", c.File)
		}
		full := filepath.Join(dir, filepath.FromSlash(c.File))
		mode := fs.FileMode(0o644)
		if fi, err := os.Lstat(full); err == nil {
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file; fix it by hand", c.File)
			}
			mode = fi.Mode().Perm()
		}
		if err := fsutil.WriteFileAtomic(full, c.After, mode); err != nil {
			return err
		}
	}
	return nil
}
