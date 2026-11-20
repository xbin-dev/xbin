package builtins

// applied.go — what a builtin-update proposal's merged close records: what
// the tile then holds, file by file (review of 2026-11-20, finding 5).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// RecordApplied refreshes a unit's provenance after its proposal PR closed
// merged. toHash guards against the embed having moved since the PR was
// filed (a newer xbind): a mismatched proposal must not claim currency.
//
// Closing a proposal merged says nothing of what the tile holds, so only
// what it holds is recorded: a file that holds upstream's change — equal
// to upstream's (a manifest with its installed "partition" kept counts),
// or, against the recorded base, already carrying the change beside edits
// of its own (merging upstream's change into it changes nothing) — is
// recorded as updated, and a file removed upstream that is gone here;
// every other file keeps its recorded base (none, for a file the record
// never had), so the unit is still offered for it — "update pending" —
// until it holds the change. pending names those files; the version moves
// only when there are none.
func (u *Updater) RecordApplied(id, toHash string) (pending []string, err error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, noSuchUnit(id)
	}
	o := u.load()
	state := o.Units[id]
	installPath := def.DefaultPath
	if state != nil {
		installPath = state.InstallPath
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(theirs))
	for rel, data := range theirs {
		hashes[rel] = sha(data)
	}
	if got := rollup(hashes); got != toHash {
		return nil, fmt.Errorf("embedded %s changed since the proposal was filed (a newer xbind?) — refresh the Updates tab and propose again", id)
	}
	var discard []string
	kept, _ := u.keepPartition(installPath, theirs, &discard) // a manifest as an update writes it here
	var base map[string]string
	merged := theirs // upstream's change as a merge takes it (its "partition" change undone)
	snap := u.snapDir(id)
	if state != nil {
		base = state.Files
		merged, _, _ = u.mergeTheirs(installPath, snap, theirs, &discard)
	}
	baseOf := func(rel string) ([]byte, bool) {
		if _, had := base[rel]; !had {
			return nil, false
		}
		b, err := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))) // walk-ok: the base snapshot is xbind's own (.xbin/builtins)
		return b, err == nil
	}
	holds := func(rel string, ours []byte) bool {
		if sha(ours) == sha(theirs[rel]) || kept[rel] != nil && sha(ours) == sha(kept[rel]) {
			return true
		}
		b, ok := baseOf(rel)
		if !ok {
			return false
		}
		return holdsChange(ours, b, merged[rel])
	}
	left := map[string]bool{}
	ships := shipsOf(theirs, base)
	for _, rel := range sortedRels(theirs, base) {
		why, tf := leftAlone(u.root, installPath, rel, ships, false, false) // a nested tile's file is never read
		if why != "" {
			pending, left[rel] = append(pending, installPath+"/"+rel), true
			continue
		}
		_, inTheirs := theirs[rel]
		switch {
		case inTheirs && tf.present && holds(rel, tf.data):
			continue // updated
		case !inTheirs && !tf.present && tf.refused == "":
			continue // removed upstream, and gone here
		}
		pending, left[rel] = append(pending, installPath+"/"+rel), true
	}
	return pending, u.recordPartly(def, installPath, theirs, state, left)
}

// recordPartly records the unit at theirs but for the files of left,
// which keep their recorded base (none for a file the record never had),
// so the update stays offered for them alone; the version moves only when
// left is empty (then it is record).
func (u *Updater) recordPartly(def unitDef, installPath string, theirs map[string][]byte, state *UnitState, left map[string]bool) error {
	if len(left) == 0 {
		return u.record(def, installPath, theirs)
	}
	var base map[string]string
	version := 0
	if state != nil {
		base, version = state.Files, state.Version
	}
	snap := u.snapDir(def.ID)
	files := map[string][]byte{}
	for _, rel := range sortedRels(theirs, base) {
		if !left[rel] {
			if th, ok := theirs[rel]; ok {
				files[rel] = th
			}
			continue
		}
		if _, had := base[rel]; had { // read before record empties the snapshot
			b, err := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))) // walk-ok: the base snapshot is xbind's own (.xbin/builtins)
			if err != nil {
				return err
			}
			files[rel] = b
		}
	}
	return u.recordAs(def, installPath, files, version)
}

// sortedRels is the paths of theirs and base, sorted.
func sortedRels(theirs map[string][]byte, base map[string]string) []string {
	seen := map[string]bool{}
	for rel := range theirs {
		seen[rel] = true
	}
	for rel := range base {
		seen[rel] = true
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// holdsChange reports whether ours already carries upstream's change from
// base to theirs, beside edits of its own: merging the change into ours
// adds nothing where it merges cleanly (resolving every conflict to ours
// gives ours back), and where it conflicts — an edit of ours next to or
// over upstream's — ours holds upstream's lines of each block and none of
// the base's lines upstream removed there. A merge it can't read is no.
func holdsChange(ours, base, theirs []byte) bool {
	merged, err := mergeFile(ours, base, theirs)
	if err != nil {
		return false
	}
	if bytes.Equal(merged, ours) {
		return true
	}
	if side, ok := conflictSide(merged, sideOurs); !ok || !bytes.Equal(side, ours) {
		return false // a change that merged cleanly is not ours yet, or the markers don't pair up
	}
	blocks, ok := conflictBlocks(merged)
	if !ok {
		return false
	}
	for _, b := range blocks {
		if !subsequence(b[sideTheirs], b[sideOurs]) {
			return false
		}
		for _, l := range b[sideBase] {
			if !slices.Contains(b[sideTheirs], l) && slices.Contains(b[sideOurs], l) {
				return false // a line upstream removed is still there
			}
		}
	}
	return true
}

// conflictBlocks are merged's conflict blocks (--diff3), each its three
// sides' lines; ok is false when the markers don't pair up.
func conflictBlocks(merged []byte) ([][3][]string, bool) {
	var out [][3][]string
	var cur [3][]string
	in := -1
	for _, ln := range strings.SplitAfter(string(merged), "\n") {
		switch {
		case isMarker(ln, "<<<<<<<") && in == -1:
			in, cur = sideOurs, [3][]string{}
			continue
		case isMarker(ln, "|||||||") && in == sideOurs:
			in = sideBase
			continue
		case isMarker(ln, "=======") && (in == sideOurs || in == sideBase):
			in = sideTheirs
			continue
		case isMarker(ln, ">>>>>>>") && in == sideTheirs:
			out, in = append(out, cur), -1
			continue
		case isMarker(ln, "<<<<<<<"), isMarker(ln, ">>>>>>>"):
			return nil, false
		}
		if in != -1 {
			cur[in] = append(cur[in], strings.TrimRight(ln, "\n"))
		}
	}
	return out, in == -1
}

// subsequence reports whether every line of want appears in have, in order.
func subsequence(want, have []string) bool {
	i := 0
	for _, l := range have {
		if i < len(want) && l == want[i] {
			i++
		}
	}
	return i == len(want)
}
