package builtins

// applied.go — what a builtin-update proposal's merged close records: what
// the tile then holds, file by file (review of 2026-11-20, finding 5).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		merged, _ = u.mergeTheirs(installPath, snap, theirs, &discard)
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
		out, err := mergeFile(ours, b, merged[rel]) // a conflict leaves markers: never ours
		return err == nil && sha(out) == sha(ours)
	}
	files := map[string][]byte{}
	for _, rel := range sortedRels(theirs, base) {
		tf := readTile(u.root, installPath, rel)
		th, inTheirs := theirs[rel]
		switch {
		case inTheirs && tf.present && holds(rel, tf.data):
			files[rel] = th // updated
			continue
		case !inTheirs && !tf.present && tf.refused == "":
			continue // removed upstream, and gone here
		}
		pending = append(pending, installPath+"/"+rel)
		if b, ok := baseOf(rel); ok { // keeps its recorded base
			files[rel] = b
		}
	}
	if len(pending) == 0 {
		return nil, u.record(def, installPath, theirs)
	}
	version := 0
	if state != nil {
		version = state.Version
	}
	return pending, u.recordAs(def, installPath, files, version)
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
