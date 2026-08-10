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
	"os/exec"
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
	if b, err := os.ReadFile(ledgerPath); err == nil {
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
	b, err := os.ReadFile(u.markerPath())
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
		Source: def.ID, InstallPath: installPath, Version: def.Version,
		Hash: rollup(hashes), Files: hashes,
	}
	return u.save(o)
}

// RecordTile records provenance for a freshly imported tile at installPath.
func (u *Updater) RecordTile(name, installPath string) error {
	def, ok := u.defByID("tile:" + name)
	if !ok {
		return fmt.Errorf("no builtin tile %q", name)
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
	return RenderTree(def.SrcFS, def.SrcRoot, installPath, def.DefaultPath, false)
}

func (u *Updater) installed(installPath string) bool {
	base := filepath.Join(u.root, filepath.FromSlash(installPath))
	for _, f := range []string{"xbin.json", "index.html"} {
		if _, err := os.Stat(filepath.Join(base, f)); err == nil {
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

	for _, rel := range rels {
		th, inTheirs := theirs[rel]
		bh, inBase := baseFiles[rel]
		ou, inOurs := u.oursHash(installPath, rel)

		st := stUpToDate
		switch {
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
			uu.Files = append(uu.Files, FileStatus{Path: rel, Status: st})
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

func (u *Updater) oursHash(installPath, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(u.root, filepath.FromSlash(installPath), filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return sha(b), true
}

// --- apply --------------------------------------------------------------

// ApplyReplace overwrites the workspace copy with the embedded version
// (discarding local edits), fast-forwarding cleanly and deleting files removed
// upstream. Records the new base/version.
func (u *Updater) ApplyReplace(id string) ([]string, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, fmt.Errorf("no such builtin %q", id)
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
	// Delete files removed upstream (present in base, gone in theirs).
	if state != nil {
		for rel := range state.Files {
			if _, ok := theirs[rel]; !ok {
				_ = os.Remove(filepath.Join(u.root, filepath.FromSlash(installPath), filepath.FromSlash(rel)))
			}
		}
	}
	written, err := WriteTree(filepath.Join(u.root, filepath.FromSlash(installPath)), installPath, theirs)
	if err != nil {
		return written, err
	}
	return written, u.record(def, installPath, theirs)
}

// ApplyMerge 3-way merges each changed file (git merge-file: ours / base /
// theirs), fast-forwarding clean files. Conflicting files keep standard
// conflict markers for the user to resolve. Not available for adopted units
// (no trustworthy base — use Replace). Records the new base/version.
func (u *Updater) ApplyMerge(id string) ([]string, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, fmt.Errorf("no such builtin %q", id)
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
	var written []string
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
		th, inTheirs := theirs[rel]
		bh, inBase := state.Files[rel]
		ourPath := filepath.Join(u.root, filepath.FromSlash(installPath), filepath.FromSlash(rel))
		ours, ourErr := os.ReadFile(ourPath)
		inOurs := ourErr == nil

		switch {
		case !inTheirs && inBase: // removed upstream
			if inOurs && sha(ours) == bh {
				_ = os.Remove(ourPath)
			}
		case !inOurs: // clean add / restore
			if _, err := writeFile(ourPath, th); err != nil {
				return written, err
			}
			written = append(written, installPath+"/"+rel)
		case sha(ours) == sha(th): // already up to date
		case sha(ours) == bh: // local unmodified → fast-forward
			if _, err := writeFile(ourPath, th); err != nil {
				return written, err
			}
			written = append(written, installPath+"/"+rel)
		default: // both changed → 3-way merge
			base, _ := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel)))
			merged, err := mergeFile(ours, base, th)
			if err != nil {
				return written, err
			}
			if _, err := writeFile(ourPath, merged); err != nil {
				return written, err
			}
			written = append(written, installPath+"/"+rel)
		}
	}
	return written, u.record(def, installPath, theirs)
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
	Series      string // format-patch mbox
	ToVersion   int
	ToHash      string // rollup of theirs; RecordApplied verifies the embed hasn't moved
}

// Propose renders a unit's pending update as a Proposal. Errors when the
// unit is unknown or already up to date.
func (u *Updater) Propose(id string) (*Proposal, error) {
	def, ok := u.defByID(id)
	if !ok {
		return nil, fmt.Errorf("no such builtin %q", id)
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
			if data, err := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel))); err == nil {
				base[rel] = data
			}
		}
	} else {
		root := filepath.Join(u.root, filepath.FromSlash(installPath))
		for _, fs := range uu.Files {
			if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fs.Path))); err == nil {
				base[fs.Path] = data
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
	for _, fs := range uu.Files {
		if fs.Status == stUpToDate {
			continue
		}
		fmt.Fprintf(&msg, "  %-9s %s\n", fs.Status, fs.Path)
	}
	msg.WriteString("\nApply in this tile's terminal (clone-first keeps markers out of live files):\n" +
		"  git clone \"$XBIN_WORKSPACE/" + installPath + "\" /tmp/upd && cd /tmp/upd\n" +
		"  bx code pr fetch <n> | git am --3way    # resolve, build, test\n" +
		"  git -C \"$XBIN_WORKSPACE/" + installPath + "\" pull /tmp/upd && bx code pr close <n> --merged\n" +
		"Closing merged refreshes update tracking; rejecting keeps the update offered.")

	series, err := patchSeries(base, theirs, title, msg.String())
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(theirs))
	for rel, data := range theirs {
		hashes[rel] = sha(data)
	}
	return &Proposal{
		ID: id, InstallPath: installPath, Title: title, Message: msg.String(),
		Series: series, ToVersion: uu.ToVersion, ToHash: rollup(hashes),
	}, nil
}

// RecordApplied refreshes a unit's provenance after its proposal PR closed
// merged. toHash guards against the embed having moved since the PR was
// filed (a newer xbind): a mismatched proposal must not claim currency.
func (u *Updater) RecordApplied(id, toHash string) error {
	def, ok := u.defByID(id)
	if !ok {
		return fmt.Errorf("no such builtin %q", id)
	}
	o := u.load()
	installPath := def.DefaultPath
	if state := o.Units[id]; state != nil {
		installPath = state.InstallPath
	}
	theirs, err := u.render(def, installPath)
	if err != nil {
		return err
	}
	hashes := make(map[string]string, len(theirs))
	for rel, data := range theirs {
		hashes[rel] = sha(data)
	}
	if got := rollup(hashes); got != toHash {
		return fmt.Errorf("embedded %s changed since the proposal was filed (a newer xbind?) — refresh the Updates tab and propose again", id)
	}
	return u.record(def, installPath, theirs)
}

// patchSeries renders old→new as a one-commit git format-patch mbox: a temp
// repo, the old tree as a baseline commit, the new tree as the update commit
// (subject + body = the PR title + message). git computes renames/deletions;
// `git am --3way` on the receiving side gets index blob ids to merge from.
func patchSeries(from, to map[string][]byte, subject, body string) (string, error) {
	dir, err := os.MkdirTemp("", "xbin-propose-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	git := func(args ...string) (string, error) {
		full := append([]string{"-C", dir,
			"-c", "user.email=builtins@xbin", "-c", "user.name=xbin builtins",
			"-c", "commit.gpgsign=false"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %v: %s", args, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	writeTree := func(files map[string][]byte) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.Name() != ".git" {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					return err
				}
			}
		}
		for rel, data := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := git("init", "-q", "-b", "main"); err != nil {
		return "", err
	}
	if err := writeTree(from); err != nil {
		return "", err
	}
	if _, err := git("add", "-A"); err != nil {
		return "", err
	}
	if _, err := git("commit", "-q", "--allow-empty", "-m", "builtin base"); err != nil {
		return "", err
	}
	if err := writeTree(to); err != nil {
		return "", err
	}
	if _, err := git("add", "-A"); err != nil {
		return "", err
	}
	if _, err := git("commit", "-q", "-m", subject+"\n\n"+body); err != nil {
		return "", err
	}
	series, err := git("format-patch", "--stdout", "-1", "HEAD")
	if err != nil {
		return "", err
	}
	if !strings.Contains(series, "diff --git") {
		return "", fmt.Errorf("update produced an empty diff")
	}
	return series, nil
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

func writeFile(p string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, os.WriteFile(p, data, 0o644)
}

// mergeFile runs `git merge-file -p` on (ours, base, theirs) and returns the
// merged bytes (with conflict markers where both sides changed the same lines).
func mergeFile(ours, base, theirs []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bx-merge")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	write := func(name string, b []byte) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, b, 0o644)
	}
	op, _ := write("ours", ours)
	bp, _ := write("base", base)
	tp, _ := write("theirs", theirs)
	cmd := exec.Command("git", "merge-file", "-p", "--diff3", op, bp, tp)
	outb, err := cmd.Output()
	// git merge-file exits with the conflict count (>0) — not a real error.
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, fmt.Errorf("git merge-file: %w", err)
		}
	}
	return outb, nil
}
