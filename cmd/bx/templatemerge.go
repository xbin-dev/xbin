package main

// templatemerge.go — `bx template merge-manifest`, the merge driver xbind
// names for xbin.json in every builtin template instance's repository
// (internal/manifestmerge; docs/overview/03-components.md §Templates). git
// runs it for every merge in which both sides changed the root manifest —
// `git merge template/main`, and also a builder's own branch, a rebase, a
// cherry-pick, a stash pop:
//
//	bx template merge-manifest [--marker-size N] [--rename FROM=TO] BASE OURS THEIRS
//
// First the merge git would do itself (git merge-file): when that is clean
// it stands, byte for byte. Only where it conflicts, and only when the
// other side is the template — BASE and THEIRS both versions of xbin.json
// in the history of the `template` remote (fromTemplate) — is the manifest
// merged by keys. Either way the result is written into OURS and bx exits
// 0; when neither merges, OURS is left as it is and bx exits 1 — the
// driver's `|| git merge-file …` then writes git's own conflict markers,
// exactly the merge git makes without the driver.

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
	if !fromTemplate(files[0], files[2]) {
		return errors.New("xbin.json: this merge isn't one of the template's changes (a branch of yours, a rebase, a stash?), so it isn't merged by keys — git's line merge follows: resolve its conflict markers")
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
	if len(r.Took) == 0 {
		fmt.Fprintln(os.Stderr, "bx: xbin.json merged by keys — upstream changed no value, only comments or layout where your manifest has none (it has no comments, or dropped that member): yours kept as it is (/docs/overview/03-components.md §Templates)")
		return nil
	}
	fmt.Fprintf(os.Stderr, "bx: xbin.json merged by keys — upstream's change to %s taken, yours kept (/docs/overview/03-components.md §Templates)\n",
		strings.Join(r.Took, ", "))
	return nil
}

// fromTemplate reports whether a merge takes the template's change: the
// base and theirs (files git hands the driver) are both versions of the
// root xbin.json in the history of the `template` remote's branches — `git
// merge template/main`, a cherry-pick of a template snapshot. Not a
// builder's own branch, nor a rebase onto the template (theirs is the
// builder's commit being replayed), nor a stash: their side is the
// builder's. git runs the driver at the top of the work tree.
func fromTemplate(files ...string) bool {
	out, err := exec.Command("git", "log", "--no-abbrev", "--root", "--no-renames", "--format=", "--raw",
		"--glob=refs/remotes/template/*", "--", ":(top,literal)xbin.json").Output()
	if err != nil {
		return false
	}
	versions := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		// :100644 100644 <old blob> <new blob> M	xbin.json
		if f := strings.Fields(ln); len(f) >= 5 && strings.HasPrefix(f[0], ":") {
			versions[f[3]] = true
		}
	}
	ids, err := exec.Command("git", append([]string{"hash-object", "--no-filters", "--"}, files...)...).Output()
	if err != nil {
		return false
	}
	got := strings.Fields(string(ids))
	for _, id := range got {
		if !versions[id] {
			return false
		}
	}
	return len(got) == len(files)
}
