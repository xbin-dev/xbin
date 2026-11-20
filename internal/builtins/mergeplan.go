package builtins

// mergeplan.go — a unit's merge decided file by file before anything is
// written (planMerge), and written (writeMerge): what applyMerge does, and
// what safe mode (safe.go, D248) writes only when no file of it conflicts.
// Each file is read as the file at its path (tilefs.go), and written only
// while it is still what was read.

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
	merged bool   // a 3-way merge: both sides changed it
	want   string // the file as it was read (tileFile.stamp): written or removed only while it is still that
}

// mergePlan is a unit's merge, decided file by file and not yet written
// (planMerge): what applyMerge writes — and what safe mode writes only when
// conflicts is empty (safe.go, D248).
type mergePlan struct {
	def         unitDef
	installPath string
	theirs      map[string][]byte
	state       *UnitState // the record the plan was decided from
	steps       []mergeStep
	// whole: no file was left as it is (a manifest mergeTheirs couldn't
	// read, a link, a FIFO, a nested tile) — the update is recorded as
	// applied only then.
	whole bool
	// conflicts are the files the merge can't take cleanly: git merge-file
	// left markers, or upstream removed a file edited here (applyMerge keeps
	// such a file as it is).
	conflicts []string
	// left are the files no update reads or writes (leftAlone): a merge
	// leaves them and says so; to safe mode each is a conflict.
	left []string
	// keptAsIs are the manifests mergeTheirs left as they are (neither the
	// installed copy nor its base can be read).
	keptAsIs []string
	// deleted are files of the recorded base that are gone here: a merge
	// restores them (a step adds each); to safe mode, a change made here.
	deleted []string
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
	kept, whole, keptAsIs := u.mergeTheirs(installPath, snap, theirs, notes) // theirs, upstream's "partition" change undone
	p := &mergePlan{def: def, installPath: installPath, theirs: theirs, state: state, whole: whole, keptAsIs: keptAsIs}
	ships := shipsOf(theirs, state.Files)
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
		why, tf := leftAlone(u.root, installPath, rel, ships, false, false)
		if why != "" { // a link, a FIFO, a nested tile…: left as it is, and said
			*notes = append(*notes, why)
			p.left, p.whole = append(p.left, rel), false
			continue
		}
		ours, inOurs, want := tf.data, tf.present, tf.stamp()

		switch {
		case !inTheirs && inBase: // removed upstream
			switch {
			case inOurs && sha(ours) == bh:
				p.steps = append(p.steps, mergeStep{rel: rel, remove: true, want: want})
			case inOurs: // edited here: kept as it is — a conflict to a merge that must be clean
				p.conflicts = append(p.conflicts, rel)
			}
		case !inOurs: // clean add / restore
			if inBase {
				p.deleted = append(p.deleted, rel)
			}
			p.steps = append(p.steps, mergeStep{rel: rel, data: th, want: want})
		case sha(ours) == sha(th): // already up to date
		case sha(ours) == bh: // local unmodified → fast-forward
			p.steps = append(p.steps, mergeStep{rel: rel, data: th, want: want})
		default: // both changed → 3-way merge
			base, _ := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))) // walk-ok: the base snapshot is xbind's own (.xbin/builtins)
			merged, conflicted, err := mergeKeepingC(rel, ours, base, th)
			if err != nil {
				return nil, err
			}
			p.steps = append(p.steps, mergeStep{rel: rel, data: merged, same: sha(merged) == sha(ours), merged: sha(th) != bh, want: want})
			if conflicted {
				p.conflicts = append(p.conflicts, rel)
			}
		}
	}
	return p, nil
}

// writeMerge writes p and records it (recordPartly: what it left keeps
// its old base). A step
// that leaves its file as it is writes nothing; every other file is
// written only while it is what planMerge read (tilefs.go): one changed
// meanwhile stops the merge there, said with what was written before.
func (u *Updater) writeMerge(p *mergePlan) ([]string, error) {
	var written []string
	for _, st := range p.steps {
		if st.same {
			continue
		}
		if st.remove {
			if err := removeTile(u.root, p.installPath, st.rel, st.want); err != nil {
				return written, writtenSoFar(err, written)
			}
			continue
		}
		wrote, err := writeTile(u.root, p.installPath, st.rel, st.data, st.want, false)
		if wrote {
			written = append(written, p.installPath+"/"+st.rel)
		}
		if err != nil {
			return written, writtenSoFar(err, written)
		}
	}
	// what was merged is recorded at the new base; a file left as it is
	// keeps its old one, so the update stays offered for it alone and a
	// later merge never merges a merged file again (re-review, M1)
	left := map[string]bool{}
	for _, rel := range append(append([]string{}, p.left...), p.keptAsIs...) {
		left[rel] = true
	}
	return written, u.recordPartly(p.def, p.installPath, p.theirs, p.state, left)
}
