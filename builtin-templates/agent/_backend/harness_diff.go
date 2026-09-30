// harness_diff.go — the diff of an ACP `diff` content (a coding agent's
// edit: path, oldText, newText) for its tool row's acp.diffs (D147
// §4.3.5): the files tools' unified diff (files_meta.go), exact line
// counts, at most 64 KiB of patch per file, cut at a hunk boundary.
package main

import "strings"

const hPatchMax = 64 << 10

// harnessDiff is one file's diff. old nil: the file is new; deleted: the
// call removed it.
func harnessDiff(path string, old *string, newText string, deleted bool) hDiff {
	d := hDiff{Path: path, Status: "modified"}
	from, to, o := "a"+slashed(path), "b"+slashed(path), ""
	switch {
	case old == nil:
		d.Status, from = "added", "/dev/null"
	case deleted:
		d.Status, to, o = "deleted", "/dev/null", *old
	default:
		o = *old
	}
	patch, del, add := unifiedDiff(from, to, o, newText)
	d.Add, d.Del = add, del
	if add+del == 0 {
		return d
	}
	for len(patch) > hPatchMax {
		cut := strings.LastIndex(patch[:hPatchMax], "\n@@ ")
		if cut < 0 {
			cut = strings.Index(patch, "\n@@ ") // the headers alone
			if cut < 0 || cut >= len(patch) {
				break
			}
		}
		patch, d.Truncated = patch[:cut+1], true
	}
	d.Patch = patch
	return d
}

// slashed is path with one leading slash ("a" + it reads a/work/x.go).
func slashed(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}
