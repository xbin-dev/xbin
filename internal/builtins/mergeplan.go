package builtins

// mergeplan.go — a unit's merge decided file by file before anything is
// written (planMerge), and written (writeMerge): what applyMerge does, and
// what safe mode (safe.go, D248) writes only when no file of it conflicts.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// mergeStep is one file of a merge, decided before anything is written:
// data to write, or the file to remove. same: data is what the file holds
// already (a file edited only here, merged with an unchanged upstream).
type mergeStep struct {
	rel    string
	data   []byte
	remove bool
	same   bool
	merged bool // a 3-way merge: both sides changed it
}

// mergePlan is a unit's merge, decided file by file and not yet written
// (planMerge): what applyMerge writes — and what safe mode writes only when
// conflicts is empty (safe.go, D248).
type mergePlan struct {
	def         unitDef
	installPath string
	theirs      map[string][]byte
	steps       []mergeStep
	// whole: no manifest was left as it is (mergeTheirs) — the update is
	// recorded as applied only then.
	whole bool
	// conflicts are the files the merge can't take cleanly: git merge-file
	// left markers, or upstream removed a file edited here (applyMerge keeps
	// such a file as it is).
	conflicts []string
}

// planMerge decides applyMerge's every file without writing one.
func (u *Updater) planMerge(id string, notes *[]string) (*mergePlan, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, noSuchUnit(id)
	}
	o := u.load()
	state := o.Units[id]
	if state == nil {
		return nil, fmt.Errorf("%s has no recorded base (adopted) — use replace, or diff and merge by hand", id)
	}
	installPath := state.InstallPath
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}
	snap := u.snapDir(id)
	kept, whole := u.mergeTheirs(installPath, snap, theirs, notes) // theirs, upstream's "partition" change undone
	p := &mergePlan{def: def, installPath: installPath, theirs: theirs, whole: whole}
	seen := map[string]bool{}
	for rel := range theirs {
		seen[rel] = true
	}
	for rel := range state.Files {
		seen[rel] = true
	}
	rels := make([]string, 0, len(seen))
	for rel := range seen {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		th, inTheirs := kept[rel]
		bh, inBase := state.Files[rel]
		ourPath := filepath.Join(u.root, filepath.FromSlash(installPath), filepath.FromSlash(rel))
		ours, ourErr := os.ReadFile(ourPath)
		inOurs := ourErr == nil

		switch {
		case !inTheirs && inBase: // removed upstream
			switch {
			case inOurs && sha(ours) == bh:
				p.steps = append(p.steps, mergeStep{rel: rel, remove: true})
			case inOurs: // edited here: kept as it is — a conflict to a merge that must be clean
				p.conflicts = append(p.conflicts, rel)
			}
		case !inOurs: // clean add / restore
			p.steps = append(p.steps, mergeStep{rel: rel, data: th})
		case sha(ours) == sha(th): // already up to date
		case sha(ours) == bh: // local unmodified → fast-forward
			p.steps = append(p.steps, mergeStep{rel: rel, data: th})
		default: // both changed → 3-way merge
			base, _ := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel)))
			merged, conflicted, err := mergeKeepingC(rel, ours, base, th)
			if err != nil {
				return nil, err
			}
			p.steps = append(p.steps, mergeStep{rel: rel, data: merged, same: sha(merged) == sha(ours), merged: sha(th) != bh})
			if conflicted {
				p.conflicts = append(p.conflicts, rel)
			}
		}
	}
	return p, nil
}

// writeMerge writes p and, when it is whole, records the new base.
func (u *Updater) writeMerge(p *mergePlan) ([]string, error) {
	var written []string
	for _, st := range p.steps {
		ourPath := filepath.Join(u.root, filepath.FromSlash(p.installPath), filepath.FromSlash(st.rel))
		if st.remove {
			_ = os.Remove(ourPath)
			continue
		}
		if _, err := writeFile(ourPath, st.data); err != nil {
			return written, err
		}
		written = append(written, p.installPath+"/"+st.rel)
	}
	if !p.whole { // a manifest left as it is keeps the update offered
		return written, nil
	}
	return written, u.record(p.def, p.installPath, p.theirs)
}
