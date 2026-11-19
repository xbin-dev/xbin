// updates.go — keeping copied builtins current (plans/builtin-updates.md).
//
// Builtins are copied into a workspace once (scaffold at init, tiles on import)
// and then owned/edited by the user. This tracks their provenance so a newer
// xbind can offer updates without trampling local customizations: an origin
// marker (.xbin/builtins.json) plus a base snapshot (.xbin/builtins/<id>/)
// record what was installed, and a per-file 3-way comparison (base / workspace
// "ours" / embedded "theirs") drives replace-or-merge.
//
// Scope: builtin tiles and scaffold components. Template *instances* are forks
// and are deliberately not tracked (they get no marker).
package builtins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	originFile  = "builtins.json" // under .xbin/
	snapDirName = "builtins"      // under .xbin/ (base snapshots)
)

// UnitState is one managed builtin's recorded provenance.
type UnitState struct {
	Source      string            `json:"source"`      // "tile:llm-gw" | "scaffold:shell"
	InstallPath string            `json:"installPath"` // where it lives in the workspace
	Version     int               `json:"version,omitempty"`
	Hash        string            `json:"hash"`  // rollup over the base file set
	Files       map[string]string `json:"files"` // rel path -> sha256 (base)
	Adopted     bool              `json:"adopted,omitempty"`
	Pinned      bool              `json:"pinned,omitempty"`
}

type origin struct {
	Units map[string]*UnitState `json:"units"`
}

// EssentialScaffold lists scaffold components whose ABSENCE is never
// deliberate on upgrade: newer xbind versions grew chrome that targets them
// (the shell's ⚑ button opens tiles/organisations), so older workspaces get
// them backfilled at boot (BackfillEssentials) and `bx builtin updates`
// lists them as MISSING rather than skipping them.
var EssentialScaffold = []string{"tiles/organisations"}

func essentialScaffold(name string) bool {
	for _, e := range EssentialScaffold {
		if e == name {
			return true
		}
	}
	return false
}

// ResolveID resolves a unit id that may omit its kind prefix: "tiles/x"
// tries "scaffold:tiles/x" then "tile:tiles/x". Exact ids pass through.
func (u *Updater) ResolveID(id string) string {
	if strings.Contains(id, ":") {
		return id
	}
	for _, pre := range []string{"scaffold:", "tile:"} {
		if _, ok := u.defByID(pre + id); ok {
			return pre + id
		}
	}
	return id
}

// BackfillEssentials installs missing essential scaffold units, ledgered at
// ledgerPath so a later DELIBERATE delete sticks across restarts: a unit in
// the ledger is never reinstalled. Present units are ledgered without being
// touched (so their later deletion also reads as deliberate). Returns the
// units it installed.
func (u *Updater) BackfillEssentials(ledgerPath string) ([]string, error) {
	var l struct {
		Done []string `json:"done"`
	}
	if b, err := os.ReadFile(ledgerPath); err == nil { // walk-ok: the ledger is xbind's own (data dir)
		_ = json.Unmarshal(b, &l)
	}
	done := map[string]bool{}
	for _, d := range l.Done {
		done[d] = true
	}
	var installed []string
	changed := false
	for _, name := range EssentialScaffold {
		if done[name] {
			continue
		}
		if _, ok := u.defByID("scaffold:" + name); !ok {
			continue // embed lacks it (stripped build) — nothing to offer
		}
		if !u.installed(name) {
			if _, err := u.ApplyReplace("scaffold:" + name); err != nil {
				return installed, fmt.Errorf("backfill %s: %w", name, err)
			}
			installed = append(installed, name)
		}
		l.Done = append(l.Done, name)
		done[name] = true
		changed = true
	}
	if changed {
		if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
			return installed, err
		}
		b, _ := json.MarshalIndent(l, "", "  ")
		if err := os.WriteFile(ledgerPath, b, 0o644); err != nil {
			return installed, err
		}
	}
	return installed, nil
}

// Updater manages builtin update tracking for one workspace.
type Updater struct {
	root       string
	tiles      *Set
	scaffoldFS fs.FS
	recorded   func(tile string) (raw []byte, has, ok bool) // SetRecordedPartition
}

func NewUpdater(root string, tiles *Set, scaffoldFS fs.FS) *Updater {
	return &Updater{root: root, tiles: tiles, scaffoldFS: scaffoldFS}
}

// unitDef is an embedded managed builtin's source definition.
type unitDef struct {
	ID          string
	Kind        string // "tile" | "scaffold"
	Name        string
	SrcFS       fs.FS
	SrcRoot     string
	DefaultPath string
	Version     int
	Changelog   string
}

func (u *Updater) scaffoldDefs() []unitDef {
	if u.scaffoldFS == nil {
		return nil
	}
	comps := map[string]bool{}
	_ = fs.WalkDir(u.scaffoldFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Base(p) {
		case "xbin.json", "index.html":
			if dir := path.Dir(p); dir != "." {
				comps[dir] = true
			}
		}
		return nil
	})
	defs := make([]unitDef, 0, len(comps))
	for dir := range comps {
		defs = append(defs, unitDef{
			ID: "scaffold:" + dir, Kind: "scaffold", Name: dir,
			SrcFS: u.scaffoldFS, SrcRoot: dir, DefaultPath: dir, Version: 1,
		})
	}
	return defs
}

func (u *Updater) tileDefs() []unitDef {
	if u.tiles == nil {
		return nil
	}
	var defs []unitDef
	for _, m := range u.tiles.List() {
		v := m.Version
		if v == 0 {
			v = 1
		}
		defs = append(defs, unitDef{
			ID: "tile:" + m.Name, Kind: "tile", Name: m.Name,
			SrcFS: u.tiles.fsys, SrcRoot: m.Name, DefaultPath: m.DefaultPath,
			Version: v, Changelog: m.Changelog,
		})
	}
	return defs
}

func (u *Updater) defs() []unitDef {
	return append(u.scaffoldDefs(), u.tileDefs()...)
}

func (u *Updater) defByID(id string) (unitDef, bool) {
	for _, d := range u.defs() {
		if d.ID == id {
			return d, true
		}
	}
	return unitDef{}, false
}

// --- origin marker + snapshots ------------------------------------------

func (u *Updater) markerPath() string { return filepath.Join(u.root, ".xbin", originFile) }

func encodeID(id string) string { return strings.NewReplacer(":", "~", "/", "~").Replace(id) }

func (u *Updater) snapDir(id string) string {
	return filepath.Join(u.root, ".xbin", snapDirName, encodeID(id))
}

func (u *Updater) load() *origin {
	o := &origin{Units: map[string]*UnitState{}}
	b, err := os.ReadFile(u.markerPath()) // walk-ok: the marker is xbind's own (.xbin/builtins.json)
	if err != nil {
		return o
	}
	_ = json.Unmarshal(b, o)
	if o.Units == nil {
		o.Units = map[string]*UnitState{}
	}
	return o
}

func (u *Updater) save(o *origin) error {
	if err := os.MkdirAll(filepath.Dir(u.markerPath()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(o, "", "  ")
	return os.WriteFile(u.markerPath(), append(b, '\n'), 0o644)
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func rollup(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, files[k])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// record writes the base snapshot + marker entry for a unit aligned to `files`
// (the installed form of the given version).
func (u *Updater) record(def unitDef, installPath string, files map[string][]byte) error {
	return u.recordAs(def, installPath, files, def.Version)
}

// recordAs is record at version.
func (u *Updater) recordAs(def unitDef, installPath string, files map[string][]byte, version int) error {
	snap := u.snapDir(def.ID)
	_ = os.RemoveAll(snap)
	hashes := make(map[string]string, len(files))
	for rel, data := range files {
		out := filepath.Join(snap, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		hashes[rel] = sha(data)
	}
	o := u.load()
	o.Units[def.ID] = &UnitState{
		Source: def.ID, InstallPath: installPath, Version: version,
		Hash: rollup(hashes), Files: hashes,
	}
	return u.save(o)
}

// RecordTile records provenance for a freshly imported tile at installPath.
func (u *Updater) RecordTile(name, installPath string) error {
	def, ok := u.defByID("tile:" + name)
	if !ok {
		return noSuchTile(name)
	}
	files, err := u.render(def, installPath)
	if err != nil {
		return err
	}
	return u.record(def, installPath, files)
}

// RecordScaffoldSeed records provenance for every scaffold component present in
// the workspace — called right after init seeds them (so base == embedded).
func (u *Updater) RecordScaffoldSeed() error {
	for _, def := range u.scaffoldDefs() {
		if !u.installed(def.DefaultPath) {
			continue
		}
		files, err := u.render(def, def.DefaultPath)
		if err != nil {
			return err
		}
		if err := u.record(def, def.DefaultPath, files); err != nil {
			return err
		}
	}
	return nil
}

func (u *Updater) render(def unitDef, installPath string) (map[string][]byte, error) {
	return RenderTree(def.SrcFS, def.SrcRoot, installPath, def.DefaultPath, nil)
}

// installed reports whether a component is at installPath: anything at its
// manifest's or its page's path (a link too — it is never followed, and
// reads as a conflict).
func (u *Updater) installed(installPath string) bool {
	for _, f := range []string{"xbin.json", "index.html"} {
		if hasTile(u.root, installPath, f) {
			return true
		}
	}
	return false
}

// --- detection ----------------------------------------------------------

// File statuses.
const (
	stUpToDate = "uptodate"
	stClean    = "clean"    // upstream changed, local unmodified → safe fast-forward
	stConflict = "conflict" // both changed (or adopted, unknown base) → choose/merge
	stNew      = "new"      // added upstream
	stRemoved  = "removed"  // removed upstream
	stUser     = "user"     // local-only edit, upstream unchanged → keep
)

type FileStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	// Note says why a file is a conflict when it was not read: a symbolic
	// link, not a regular file, too large, or inside a component of its own
	// (tilefs.go) — no update writes it.
	Note string `json:"note,omitempty"`
}

type UnitUpdate struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	InstallPath string       `json:"installPath"`
	FromVersion int          `json:"fromVersion"`
	ToVersion   int          `json:"toVersion"`
	Changelog   string       `json:"changelog,omitempty"`
	Adopted     bool         `json:"adopted"`
	Pinned      bool         `json:"pinned"`
	Files       []FileStatus `json:"files"`
	Clean       int          `json:"clean"`
	Conflicts   int          `json:"conflicts"`
	HasUpdate   bool         `json:"hasUpdate"`
	// Missing marks an ESSENTIAL scaffold unit that is not installed at all
	// (an upgraded workspace predating it) — install with mode "replace".
	Missing bool `json:"missing,omitempty"`
}

// Updates scans every installed managed builtin and returns those with an
// update available (or an adopted unit that diverges from the current embed).
func (u *Updater) Updates() ([]*UnitUpdate, error) {
	o := u.load()
	var out []*UnitUpdate
	for _, def := range u.defs() {
		state := o.Units[def.ID]
		installPath := def.DefaultPath
		if state != nil {
			installPath = state.InstallPath
		}
		if !u.installed(installPath) {
			if def.Kind == "scaffold" && essentialScaffold(def.Name) {
				out = append(out, &UnitUpdate{
					ID: def.ID, Kind: def.Kind, Name: def.Name, InstallPath: installPath,
					ToVersion: def.Version, Missing: true, HasUpdate: true,
				})
			}
			continue
		}
		uu, err := u.compare(def, installPath, state)
		if err != nil {
			return nil, err
		}
		if uu.HasUpdate {
			out = append(out, uu)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (u *Updater) compare(def unitDef, installPath string, state *UnitState) (*UnitUpdate, error) {
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}
	adopted := state == nil
	uu := &UnitUpdate{
		ID: def.ID, Kind: def.Kind, Name: def.Name, InstallPath: installPath,
		ToVersion: def.Version, Changelog: def.Changelog, Adopted: adopted,
	}
	var baseFiles map[string]string
	if state != nil {
		baseFiles = state.Files
		uu.FromVersion = state.Version
		uu.Pinned = state.Pinned
	}

	paths := map[string]bool{}
	for p := range theirs {
		paths[p] = true
	}
	for p := range baseFiles {
		paths[p] = true
	}
	rels := make([]string, 0, len(paths))
	for p := range paths {
		rels = append(rels, p)
	}
	sort.Strings(rels)

	ships := shipsOf(theirs, baseFiles)
	for _, rel := range rels {
		th, inTheirs := theirs[rel]
		bh, inBase := baseFiles[rel]
		tf := readTile(u.root, installPath, rel)
		ou, inOurs := "", tf.present
		if inOurs {
			ou = sha(tf.data)
		}

		st, note := stUpToDate, leftAlone(u.root, installPath, rel, tf, ships, false)
		if note != "" {
			st = stConflict
		}
		switch {
		case note != "":
		case adopted:
			// No trustworthy base: any divergence is a review/conflict.
			if !inOurs {
				st = stClean
			} else if inTheirs && sha(th) != ou {
				st = stConflict
			}
		case !inBase && inTheirs: // added upstream
			if !inOurs {
				st = stNew
			} else if sha(th) == ou {
				st = stUpToDate
			} else {
				st = stConflict
			}
		case inBase && !inTheirs: // removed upstream
			if !inOurs {
				st = stUpToDate
			} else if ou == bh {
				st = stRemoved
			} else {
				st = stConflict
			}
		default: // in both base and theirs
			switch {
			case !inOurs:
				st = stClean // user deleted a managed file; offer to restore
			case sha(th) == ou:
				st = stUpToDate
			case ou == bh: // local unmodified, upstream changed
				st = stClean
			case sha(th) == bh: // local edited, upstream unchanged
				st = stUser
			default:
				st = stConflict
			}
		}

		switch st {
		case stClean, stNew:
			uu.Clean++
		case stConflict:
			uu.Conflicts++
		}
		if st != stUpToDate {
			uu.Files = append(uu.Files, FileStatus{Path: rel, Status: st, Note: note})
		}
	}

	actionable := uu.Clean + uu.Conflicts
	for _, f := range uu.Files {
		if f.Status == stRemoved {
			actionable++
		}
	}
	uu.HasUpdate = actionable > 0 && !uu.Pinned
	return uu, nil
}

// --- apply --------------------------------------------------------------

// applyReplace overwrites the workspace copy with the embedded version
// (discarding local edits but the manifests' "partition", partition.go),
// fast-forwarding cleanly and deleting files removed upstream. Records the
// new base/version.
func (u *Updater) applyReplace(id string, notes *[]string) ([]string, error) {
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
	var base map[string]string
	if state != nil {
		base = state.Files
	}
	ships := shipsOf(theirs, base)
	whole := true
	// Delete files removed upstream (present in base, gone in theirs): the
	// file itself, never what a link there names.
	var left []string
	for rel := range base {
		if _, ok := theirs[rel]; ok {
			continue
		}
		if why := leftAlone(u.root, installPath, rel, readTile(u.root, installPath, rel), ships, true); why != "" {
			left, whole = append(left, why), false
			continue
		}
		if err := removeTile(u.root, installPath, rel, ""); err != nil {
			left, whole = append(left, err.Error()), false
		}
	}
	kept, keptWhole := u.keepPartition(installPath, theirs, notes) // keptWhole: no manifest left as it is
	whole = whole && keptWhole
	for rel := range kept {
		if why := leftAlone(u.root, installPath, rel, readTile(u.root, installPath, rel), ships, true); why != "" {
			delete(kept, rel)
			left, whole = append(left, why), false
		}
	}
	sort.Strings(left)
	*notes = append(*notes, left...)
	written, err := writeTreeIn(u.root, installPath, kept, "")
	if err != nil || !whole { // a file left as it is keeps the update offered
		return written, writtenSoFar(err, written)
	}
	return written, u.record(def, installPath, theirs)
}

// applyMerge 3-way merges each changed file (git merge-file: ours / base /
// theirs), fast-forwarding clean files. Conflicting files keep standard
// conflict markers for the user to resolve. Not available for adopted units
// (no trustworthy base — use Replace). Records the new base/version.
func (u *Updater) applyMerge(id string, notes *[]string) ([]string, error) {
	p, err := u.planMerge(id, notes)
	if err != nil {
		return nil, err
	}
	return u.writeMerge(p)
}

// Proposal is a builtin update rendered as a git patch series, for filing
// through the cross-tile PR flow (update mode "pr" — where builtin-updates
// meets plans/code-prs.md). The series is base→theirs for tracked units and
// ours→theirs for adopted ones (no trusted base), so `git am --3way` in the
// tile's own terminal does the merge with real git machinery instead of
// conflict markers dropped into live files. Provenance is refreshed only
// when the PR closes merged (RecordApplied) — after resolution, not before.
type Proposal struct {
	ID          string
	InstallPath string
	Title       string
	Message     string
	Series      string // format-patch mbox; "" when upstream changed nothing but a partition (Notes say so)
	ToVersion   int
	ToHash      string // rollup of theirs; RecordApplied verifies the embed hasn't moved
	Notes       []string
}

// Propose renders a unit's pending update as a Proposal. Errors when the
// unit is unknown or already up to date.
func (u *Updater) Propose(id string) (*Proposal, error) {
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
	uu, err := u.compare(def, installPath, state)
	if err != nil {
		return nil, err
	}
	if !uu.HasUpdate {
		return nil, fmt.Errorf("%s is up to date — nothing to propose", id)
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return nil, err
	}

	// The "from" side of the diff: the recorded base snapshot, or — adopted
	// units, no trusted base — the installed files as they are now.
	base := map[string][]byte{}
	if state != nil && !state.Adopted {
		snap := u.snapDir(id)
		for rel := range state.Files {
			if data, err := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))); err == nil { // walk-ok: the base snapshot is xbind's own (.xbin/builtins)
				base[rel] = data
			}
		}
	} else {
		// Each file as it is at its path: a link, a FIFO, a file too large
		// is not read (its status says so) — the series, which the tile's
		// readers fetch, holds only what is there.
		for _, fs := range uu.Files {
			if tf := readTile(u.root, installPath, fs.Path); tf.present {
				base[fs.Path] = tf.data
			}
		}
	}

	title := fmt.Sprintf("builtin update: %s v%d→v%d", def.Name, uu.FromVersion, uu.ToVersion)
	if uu.FromVersion == 0 && state == nil {
		title = fmt.Sprintf("builtin update: %s v%d (adopted — no recorded base)", def.Name, uu.ToVersion)
	}
	var msg strings.Builder
	if def.Changelog != "" {
		msg.WriteString(def.Changelog + "\n\n")
	}
	msg.WriteString("File status (base / yours / upstream):\n")
	var unread []string
	for _, fs := range uu.Files {
		if fs.Status == stUpToDate {
			continue
		}
		fmt.Fprintf(&msg, "  %-9s %s\n", fs.Status, fs.Path)
		if fs.Note != "" {
			unread = append(unread, fs.Note)
		}
	}
	if len(unread) > 0 {
		msg.WriteString("\nNot read (the series takes upstream's file for each):\n  " + strings.Join(unread, "\n  ") + "\n")
	}
	msg.WriteString("\nApply in this tile's terminal (clone-first keeps markers out of live files):\n" +
		"  git clone \"$XBIN_WORKSPACE/" + installPath + "\" /tmp/upd && cd /tmp/upd\n" +
		"  bx code pr fetch <n> | git am --3way    # resolve, build, test\n" +
		"  git -C \"$XBIN_WORKSPACE/" + installPath + "\" pull /tmp/upd && bx code pr close <n> --merged\n" +
		"Closing merged (a person's call, never the tile's own code) records the files that then hold\n" +
		"the update; it stays offered for the rest, as rejecting keeps it offered.")

	to, notes := u.proposalPartition(installPath, base, theirs) // the series never touches "partition"
	notes = append(notes, unread...)
	msg.WriteString(proposalNote(notes))
	series := "" // stays "" when upstream changed nothing else: nothing to propose
	if !sameFiles(base, to) {
		if series, err = patchSeries(base, to, title, msg.String()); err != nil {
			return nil, err
		}
	}
	hashes := make(map[string]string, len(theirs))
	for rel, data := range theirs {
		hashes[rel] = sha(data)
	}
	return &Proposal{
		ID: id, InstallPath: installPath, Title: title, Message: msg.String(),
		Series: series, ToVersion: uu.ToVersion, ToHash: rollup(hashes), Notes: notes,
	}, nil
}

// Pin/unpin stop/resume offering updates for a unit.
func (u *Updater) Pin(id string, pinned bool) error {
	o := u.load()
	if o.Units[id] == nil {
		return fmt.Errorf("%s is not tracked (nothing to pin)", id)
	}
	o.Units[id].Pinned = pinned
	return u.save(o)
}
