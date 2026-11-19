package builtins

// safe.go — an update that never loses a local edit (D248;
// plans/wsagent/updates.md §2): POST /builtins/update's mode "safe", which
// a workspace agent's builtin.update line applies as the approver.
//
// replace overwrites the installed copy (local edits are lost) and merge
// leaves conflict markers in live files where both sides changed the same
// lines. safe is merge's own plan (planMerge), written only when every
// file of it is clean; any conflict — markers, or upstream removing a file
// edited here — writes nothing, and the caller files the update as a
// proposal instead (mode "pr"). An adopted unit (no recorded base) is
// updated only where every file it has is upstream's byte for byte (it
// gains the files it lacks); a missing essential scaffold unit is installed
// only where nothing is. A pinned unit, one with no update and an unknown
// one are refused.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// What a safe update does, decided by SafePlan.
const (
	SafeMerge   = "merge"   // a tracked unit: the merge, every file of it clean
	SafeReplace = "replace" // an adopted unit whose every file is upstream's: it only gains files
	SafeInstall = "install" // a missing essential scaffold unit, where nothing is
	SafePropose = "propose" // it would conflict: filed as a proposal (mode "pr"), nothing written
)

// SafePlan's refusals.
var (
	ErrPinned       = errors.New("pinned: an admin chose to skip its updates — unpin it first (bx builtin unpin, or the Tile Manager's updates tab)")
	ErrUpToDate     = errors.New("it has no update: it is what this xbin ships")
	ErrNotInstalled = errors.New("it is not installed in this workspace")
	ErrOccupied     = errors.New("something is already at its path, so it is not installed over it: install it by hand")
)

// SafePlanned is what a safe update of one unit would do now.
type SafePlanned struct {
	ID, Kind, Name string
	InstallPath    string
	From, To       int
	Mode           string   // SafeMerge | SafeReplace | SafeInstall | SafePropose
	Changes        []string // the files it writes or removes (for SafePropose: the files the proposal changes)
	Conflicts      []string // SafePropose: the files that make it one
	Kept           []string // files edited here only: every mode keeps them as they are
	Merged         []string // SafeMerge: files both sides changed, merged line by line without a conflict
	// Pin is a hash of what it was decided from: the unit, the decision,
	// the embedded copy and the installed files. A line pinned to it is
	// stale once a file there changes.
	Pin   string
	Notes []string
	merge *mergePlan // SafeMerge: what ApplySafe writes
}

// safeMu holds one safe update at a time: its decision and its write are
// one step.
var safeMu sync.Mutex

// SafePlan decides a safe update of id (an exact id: ResolveID first),
// writing nothing.
func (u *Updater) SafePlan(id string) (*SafePlanned, error) {
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
	sp := &SafePlanned{ID: id, Kind: def.Kind, Name: def.Name, InstallPath: installPath, To: def.Version}
	if state != nil {
		sp.From = state.Version
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}
	if !u.installed(installPath) {
		if def.Kind != "scaffold" || !essentialScaffold(def.Name) {
			return nil, ErrNotInstalled
		}
		if ents, err := os.ReadDir(filepath.Join(u.root, filepath.FromSlash(installPath))); err == nil && len(ents) > 0 {
			return nil, ErrOccupied
		}
		sp.Mode, sp.Changes = SafeInstall, sortedKeys(theirs)
		sp.Pin = u.safePin(sp, theirs, nil)
		return sp, nil
	}
	uu, err := u.compare(def, installPath, state)
	if err != nil {
		return nil, err
	}
	switch {
	case uu.Pinned:
		return nil, ErrPinned
	case !uu.HasUpdate:
		return nil, ErrUpToDate
	}
	for _, f := range uu.Files {
		if f.Status == stUser {
			sp.Kept = append(sp.Kept, f.Path)
		}
	}
	var base map[string]string
	if state == nil { // adopted: only where it gains files and loses nothing
		for _, f := range uu.Files {
			switch f.Status {
			case stConflict:
				sp.Conflicts = append(sp.Conflicts, f.Path)
			case stClean:
				sp.Changes = append(sp.Changes, f.Path)
			}
		}
		sp.Mode = SafeReplace
		if len(sp.Conflicts) > 0 {
			sp.Mode, sp.Changes = SafePropose, proposalFiles(uu)
		}
	} else {
		base = state.Files
		p, err := u.planMerge(id, &sp.Notes)
		if err != nil {
			return nil, err
		}
		sp.Conflicts = p.conflicts
		if !p.whole { // a manifest it can't read: not something to merge unattended
			sp.Conflicts = append(sp.Conflicts, manifestsOf(theirs)...)
		}
		switch {
		case len(sp.Conflicts) > 0:
			sp.Mode, sp.Changes = SafePropose, proposalFiles(uu)
		default:
			sp.Mode, sp.merge = SafeMerge, p
			for _, st := range p.steps {
				if !st.same {
					sp.Changes = append(sp.Changes, st.rel)
				}
				if st.merged {
					sp.Merged = append(sp.Merged, st.rel)
				}
			}
		}
	}
	sort.Strings(sp.Conflicts)
	sp.Pin = u.safePin(sp, theirs, base)
	return sp, nil
}

// ConflictError is ApplySafe's answer for an update that would conflict:
// nothing was written; it is the proposal's to carry.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string {
	return "the update conflicts with this workspace's changes to " + strings.Join(e.Files, ", ") + ": nothing was written"
}

// ApplySafe applies a safe update of id (an exact id): decided again and
// written in one step. A *ConflictError says it must be a proposal
// instead; nothing was written then.
func (u *Updater) ApplySafe(id string) (Applied, string, error) {
	safeMu.Lock()
	defer safeMu.Unlock()
	sp, err := u.SafePlan(id)
	if err != nil {
		return Applied{}, "", err
	}
	a := Applied{Notes: sp.Notes}
	switch sp.Mode {
	case SafePropose:
		return a, "", &ConflictError{Files: sp.Conflicts}
	case SafeInstall, SafeReplace: // nothing there, or every file there upstream's: replace overwrites nothing
		r, err := u.ApplyReplace(id)
		r.Notes = append(a.Notes, r.Notes...)
		return r, sp.Mode, err
	}
	a.Files, err = u.writeMerge(sp.merge)
	return a, sp.Mode, err
}

// proposalFiles are the files a proposal of uu changes.
func proposalFiles(uu *UnitUpdate) []string {
	var out []string
	for _, f := range uu.Files {
		if f.Status != stUser && f.Status != stUpToDate {
			out = append(out, f.Path)
		}
	}
	return out
}

func manifestsOf(files map[string][]byte) []string {
	var out []string
	for rel := range files {
		if isManifest(rel) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// safePin hashes what sp was decided from: the unit and its decision, the
// embedded copy, and each file the update concerns as it is installed now.
func (u *Updater) safePin(sp *SafePlanned, theirs map[string][]byte, base map[string]string) string {
	rels := map[string]bool{}
	for rel := range theirs {
		rels[rel] = true
	}
	for rel := range base {
		rels[rel] = true
	}
	keys := make([]string, 0, len(rels))
	for rel := range rels {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00", sp.ID, sp.Mode, sp.From, sp.To)
	for _, rel := range keys {
		ours, ok := u.oursHash(sp.InstallPath, rel)
		if !ok {
			ours = "-"
		}
		th := "-"
		if d, ok := theirs[rel]; ok {
			th = sha(d)
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", rel, ours, th)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Unit is an embedded builtin's catalog facts, for a line that names it
// (an exact id: ResolveID first): its kind, name, where it is installed,
// the version it is at and the one this xbin ships.
type Unit struct {
	ID, Kind, Name string
	InstallPath    string
	From, To       int
	Installed      bool
	Pinned         bool
}

// Unit is id's catalog facts (false: no such builtin).
func (u *Updater) Unit(id string) (Unit, bool) {
	def, ok := u.defByID(id)
	if !ok {
		return Unit{}, false
	}
	out := Unit{ID: id, Kind: def.Kind, Name: def.Name, InstallPath: def.DefaultPath, To: def.Version}
	if st := u.load().Units[id]; st != nil {
		out.InstallPath, out.From, out.Pinned = st.InstallPath, st.Version, st.Pinned
	}
	out.Installed = u.installed(out.InstallPath)
	return out, true
}
