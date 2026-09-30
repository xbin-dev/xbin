package main

// templatemerge.go — `bx template merge-manifest`, the merge driver xbind
// names for xbin.json in every builtin template instance's repository
// (internal/manifestmerge; docs/overview/03-components.md §Templates). git
// runs it for a merge — `git merge template/main`, a rebase, a cherry-pick —
// in which both sides changed the manifest:
//
//	bx template merge-manifest [--marker-size N] [--rename FROM=TO] BASE OURS THEIRS
//
// First the merge git would do itself (git merge-file): when that is clean
// it stands, byte for byte. Only where it conflicts is the manifest merged
// by keys. Either way the result is written into OURS and bx exits 0; when
// neither can merge, OURS is left as it is and bx exits 1 — the driver's
// `|| git merge-file …` then writes git's own conflict markers.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/xbin-dev/xbin/internal/manifestmerge"
)

func cmdTemplateMergeManifest(args []string) error {
	marker, rename := "7", ""
	var files []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--marker-size" && i+1 < len(args):
			marker, i = args[i+1], i+1
		case a == "--rename" && i+1 < len(args):
			rename, i = args[i+1], i+1
		default:
			files = append(files, a)
		}
	}
	if len(files) != 3 {
		return errors.New("usage: bx template merge-manifest [--marker-size N] [--rename FROM=TO] BASE OURS THEIRS (git's merge driver for xbin.json)")
	}
	var docs [3][]byte
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		docs[i] = b
	}
	base, ours, theirs := docs[0], docs[1], docs[2]
	write := func(out []byte) error {
		if bytes.Equal(out, ours) {
			return nil
		}
		return os.WriteFile(files[1], out, 0o644)
	}
	// git's own line merge: stdout is the result, the exit status the
	// number of conflicts (an error — git missing, a bad file — goes on too)
	var stdout bytes.Buffer
	git := exec.Command("git", "merge-file", "-p", "-q", "--marker-size="+marker, files[1], files[0], files[2])
	git.Stdout = &stdout
	if git.Run() == nil {
		return write(stdout.Bytes())
	}
	var o manifestmerge.Options
	o.From, o.To, _ = strings.Cut(rename, "=")
	r, err := manifestmerge.Merge(base, ours, theirs, o)
	if err != nil {
		return fmt.Errorf("xbin.json can't be merged by keys (%v): git's line merge follows — resolve its conflict markers", err)
	}
	if err := write(r.Out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "bx: xbin.json merged by keys — upstream's change to %s taken, yours kept (/docs/overview/03-components.md §Templates)\n",
		strings.Join(r.Took, ", "))
	return nil
}
