package builtins

// safe.go — an update that never loses a local edit (D248;
// plans/wsagent/updates.md §2): POST /builtins/update's mode "safe", which
// a workspace agent's builtin.update line applies as the approver.
//
// replace overwrites the installed copy (local edits are lost) and merge
// leaves conflict markers in live files where both sides changed the same
// lines. safe is merge's own plan (planMerge), written only when every
// file of it is clean; any conflict — markers, upstream removing a file
// edited here, upstream changing a file deleted here, a file no update
// reads (a link, a FIFO, a nested tile: tilefs.go) — writes nothing, and
// the caller files the update as a proposal instead (mode "pr"). A file
// deleted here that upstream didn't change stays deleted: a change made
// here, like any other. An adopted unit (no recorded base) is always a
// proposal: without a base nothing tells a change made here from one made
// upstream. A missing essential scaffold unit is installed only where
// nothing is. A pinned unit, one with no update and an unknown one are
// refused.
//
// ApplySafe applies only what was approved: the line's pin (SafePlanned.Pin)
// travels with the call and is checked under the lock, and each file is
// written only while it is still what the plan read.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// What a safe update does, decided by SafePlan.
const (
	SafeMerge   = "merge"   // a tracked unit: the merge, every file of it clean
	SafeInstall = "install" // a missing essential scaffold unit, where nothing is
	SafePropose = "propose" // it would conflict: filed as a proposal (mode "pr"), nothing written
)

// SafePlan's refusals.
var (
	ErrPinned       = errors.New("pinned: an admin chose to skip its updates — unpin it first (bx builtin unpin, or the Tile Manager's updates tab)")
	ErrUpToDate     = errors.New("it has no update: it is what this xbin ships")
	ErrNotInstalled = errors.New("it is not installed in this workspace")
	ErrOccupied     = errors.New("something is already at its path, so it is not installed over it: install it by hand")
	// ErrStale: the update is no longer what was approved (a file or the
	// decision changed since): nothing was written.
	ErrStale = errors.New("the update is no longer what was approved — a file there or the decision changed since: nothing was written; look at it again")
	// ErrBusy: another update was being written for longer than ApplySafe
	// waits: nothing was written.
	ErrBusy = errors.New("another builtin update is being written: nothing was written; try again in a moment")
)

// SafePlanned is what a safe update of one unit would do now.
type SafePlanned struct {
	ID, Kind, Name string
	InstallPath    string
	From, To       int
	Mode           string   // SafeMerge | SafeReplace | SafeInstall | SafePropose
	Changes        []string // the files it writes or removes (for SafePropose: the files the proposal changes)
	Conflicts      []string // SafePropose: the files that make it one
	Kept           []string // files edited here only: every mode keeps them as they are (deleted ones included)
	Deleted        []string // of Kept: files deleted here that upstream didn't change — they stay deleted
	Merged         []string // SafeMerge: files both sides changed, merged line by line without a conflict
	Adopted        bool     // no recorded base: always a proposal
	// Pin is a hash of what it was decided from: the unit, the decision,
	// the embedded copy and the installed files. A line pinned to it is
	// stale once a file there changes.
	Pin   string
	Notes []string
	merge *mergePlan // SafeMerge: what ApplySafe writes
}

// safeSem holds one safe update at a time — its decision and its write are
// one step — and a caller waits for it at most safeWait.
var safeSem = make(chan struct{}, 1)

// safeWait bounds how long ApplySafe waits for another safe update.
var safeWait = 30 * time.Second

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
		if u.occupied(installPath) {
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
	if state == nil { // adopted: no base tells a change made here from upstream's — a proposal, always
		sp.Adopted = true
		for _, f := range uu.Files {
			if f.Status == stConflict || f.Status == stClean || f.Status == stNew {
				sp.Conflicts = append(sp.Conflicts, f.Path)
			}
		}
		sp.Mode, sp.Changes = SafePropose, proposalFiles(uu)
	} else {
		base = state.Files
		p, err := u.planMerge(id, &sp.Notes)
		if err != nil {
			return nil, err
		}
		sp.Conflicts = append(append([]string{}, p.conflicts...), p.left...)
		if !p.whole && len(p.left) == 0 { // a manifest it can't read: not something to merge unattended
			sp.Conflicts = append(sp.Conflicts, manifestsOf(theirs)...)
		}
		// a file deleted here is a change made here: kept deleted while
		// upstream didn't change it, a conflict where it did
		steps := p.steps[:0]
		for _, st := range p.steps {
			if !contains(p.deleted, st.rel) {
				steps = append(steps, st)
				continue
			}
			if sha(st.data) == base[st.rel] {
				sp.Kept, sp.Deleted = append(sp.Kept, st.rel), append(sp.Deleted, st.rel)
			} else {
				sp.Conflicts = append(sp.Conflicts, st.rel)
			}
		}
		p.steps = steps
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
			if len(p.steps) == 0 { // only files deleted here differ: nothing a safe update writes or records
				return nil, ErrUpToDate
			}
		}
	}
	sort.Strings(sp.Kept)
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
// written in one step, while no other safe update is. pin, when not "", is
// the SafePlanned.Pin that was approved: an update that no longer has it
// is ErrStale, nothing written. A *ConflictError says it must be a
// proposal instead; nothing was written then. Each file is written only
// while it is what the decision read: one changed meanwhile stops the
// update there (*ChangedError), naming what was written before — the next
// listing shows what is left.
func (u *Updater) ApplySafe(id, pin string) (Applied, string, error) {
	select {
	case safeSem <- struct{}{}:
	case <-time.After(safeWait):
		return Applied{}, "", ErrBusy
	}
	defer func() { <-safeSem }()
	sp, err := u.SafePlan(id)
	if err != nil {
		return Applied{}, "", err
	}
	if pin != "" && sp.Pin != pin {
		return Applied{}, "", ErrStale
	}
	a := Applied{Notes: sp.Notes}
	switch sp.Mode {
	case SafePropose:
		return a, "", &ConflictError{Files: sp.Conflicts}
	case SafeInstall: // where nothing is: each file only while nothing is there
		a.Files, err = u.installWhereNothingIs(id, sp.InstallPath)
		return a, sp.Mode, err
	}
	a.Files, err = u.writeMerge(sp.merge)
	return a, sp.Mode, err
}

// installWhereNothingIs installs id at installPath, each file only while
// nothing is at its path, and records it.
func (u *Updater) installWhereNothingIs(id, installPath string) ([]string, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, noSuchUnit(id)
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}
	var written []string
	for _, rel := range sortedKeys(theirs) {
		wrote, err := writeTile(u.root, installPath, rel, theirs[rel], "-")
		if wrote {
			written = append(written, installPath+"/"+rel)
		}
		if err != nil {
			return written, writtenSoFar(err, written)
		}
	}
	return written, u.record(def, installPath, theirs)
}

// occupied reports whether anything is at installPath: a directory with
// an entry, a file, a link (never followed).
func (u *Updater) occupied(installPath string) bool {
	f, err := fsutil.OpenNoSymlinks(u.root, installPath)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.IsDir() {
		return true
	}
	names, err := f.Readdirnames(1)
	return len(names) > 0 || err != nil && !errors.Is(err, io.EOF)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
		ours := readTile(u.root, sp.InstallPath, rel).stamp()
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
