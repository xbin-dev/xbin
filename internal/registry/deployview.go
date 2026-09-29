package registry

// deployview.go — tile deployments in the registry (05-model §6). A tile's
// manifest splits three ways:
//
//   - tile-level fields (existence, path, scope membership, uses, interfaces,
//     deps) request authority and come from the work tree;
//   - the inbound surface (template, exposes, expose, provides, chrome) comes
//     from the primary's code, since only the primary receives inbound edges;
//   - deployment-level fields (runtime, entry, setup, alwaysOn, vm, inject,
//     native, partition, partitionMail, partitionNote, scope.json's
//     resources and importMap) come from the deployment's own code.
//
// The registry's component describes the primary. While the PinnedPrimary
// hook answers for a tile, Rescan composes its component from the pinned
// checkpoint (composePinned) and its scope from the checkpoint's scope.json
// (composePinnedScopes), so every reader of the registry follows the
// primary's code without an edit of its own (D119e). View gives a generation the
// component of the code it runs. A checkpoint is read from its materialized
// tree through fsutil.OpenBeneath only (ReadCheckpoint, D119g). A tile the hook
// doesn't answer for is scanned exactly as before tile deployments (D119c).

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// checkpointFileMax caps a checkpoint's xbin.json and scope.json.
const checkpointFileMax = 1 << 20

// keptNotice is the manifest error of a tile registered for its pinned
// primary though its work tree has no valid manifest.
const keptNotice = "the work tree has no valid xbin.json; serving the pinned primary"

// errAbsent is a checkpoint file that isn't there (or is a directory, which
// the work-tree scan treats the same way).
var errAbsent = errors.New("absent")

// ReadCheckpoint reads what the checkpoint materialized at root declares:
// its xbin.json, parsed as Rescan parses a work tree's, index.html's
// presence, its native UI entry, and its scope.json with every resource name
// checked. Every file is opened beneath root, so a symlink that leaves the
// checkpoint is never followed (D119g). An xbin.json that doesn't parse is the
// zero manifest with ManifestErr set; a scope.json that doesn't parse or
// names a resource outside the rule, or a file that can't be read, is an
// error, and nothing of the checkpoint may be provisioned or started. No
// error or ManifestErr carries file contents.
func ReadCheckpoint(root string) (*PinnedCode, error) {
	// walk-ok: root is the materialized tree itself, which xbind names and
	// owns; everything inside it is opened beneath it.
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("the checkpoint's materialized tree is missing")
	}
	pc := &PinnedCode{}
	b, err := readBeneath(root, "xbin.json")
	switch {
	case err == nil:
		if perr := jsonc.Unmarshal(b, &pc.Manifest); perr != nil {
			pc.Manifest, pc.ManifestErr = Manifest{}, parseProblem("xbin.json", perr)
		}
	case !errors.Is(err, errAbsent):
		return nil, err
	}
	if f, err := fsutil.OpenBeneath(root, "index.html"); err == nil {
		pc.HasIndex = true // present, as the work-tree scan's stat has it
		f.Close()
	}
	probe := &Component{Manifest: pc.Manifest, CodeRoot: root}
	pc.Native, pc.NativeErr = probe.resolveNative()

	b, err = readBeneath(root, "scope.json")
	switch {
	case err == nil:
		sm := &ScopeManifest{}
		if perr := jsonc.Unmarshal(b, sm); perr != nil {
			return nil, errors.New(parseProblem("scope.json", perr))
		}
		if bad := invalidResourceNames(sm.Resources); len(bad) > 0 {
			return nil, fmt.Errorf("the checkpoint's scope.json declares %d resource name(s) outside the rule (%s); nothing is provisioned", len(bad), ResourceNameRule)
		}
		pc.Scope = sm
	case !errors.Is(err, errAbsent):
		return nil, err
	}
	return pc, nil
}

// readBeneath reads root/name, a checkpoint's manifest file, without leaving
// root: errAbsent when it isn't there, an error naming why it can't be read
// otherwise.
func readBeneath(root, name string) ([]byte, error) {
	f, err := fsutil.OpenBeneath(root, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errAbsent
	case errors.Is(err, fsutil.ErrEscapes):
		return nil, fmt.Errorf("the checkpoint's %s leaves the checkpoint through a symlink", name)
	case errors.Is(err, fsutil.ErrNotRegular):
		return nil, fmt.Errorf("the checkpoint's %s is not a regular file", name)
	case err != nil:
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // the host path says nothing the file name doesn't
		}
		return nil, fmt.Errorf("the checkpoint's %s can't be read: %v", name, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		return nil, errAbsent
	}
	b, err := io.ReadAll(io.LimitReader(f, checkpointFileMax+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("the checkpoint's %s can't be read", name)
	case len(b) > checkpointFileMax:
		return nil, fmt.Errorf("the checkpoint's %s is larger than %d bytes", name, checkpointFileMax)
	}
	return b, nil
}

// parseProblem says that a checkpoint's file doesn't parse, and where,
// without quoting it: a parser's message may carry the file's bytes.
func parseProblem(name string, err error) string {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("the checkpoint's %s does not parse (byte %d)", name, se.Offset)
	case errors.As(err, &te):
		return fmt.Sprintf("the checkpoint's %s does not parse (a value of the wrong type, byte %d)", name, te.Offset)
	}
	return fmt.Sprintf("the checkpoint's %s does not parse", name)
}

// composeManifest assembles a manifest from its three field kinds (05-model
// §6): tile-level fields from tile, the inbound surface from inbound,
// deployment-level fields from code. Every Manifest field is in exactly one
// kind (TestManifestFieldSplit holds that).
func composeManifest(tile, inbound, code Manifest) Manifest {
	return Manifest{
		// tile-level: authority requests
		Uses:       tile.Uses,
		Interfaces: tile.Interfaces,
		Deps:       tile.Deps,
		// the inbound surface
		Template: inbound.Template,
		Exposes:  inbound.Exposes,
		Expose:   inbound.Expose,
		Provides: inbound.Provides,
		Chrome:   inbound.Chrome,
		// deployment-level
		Runtime:  code.Runtime,
		Entry:    code.Entry,
		Setup:    code.Setup,
		AlwaysOn: code.AlwaysOn,
		VM:       code.VM,
		Inject:   code.Inject,
		Native:   code.Native,
		// deployment-level too: the running code is what must know how to
		// be partitioned (plans/partitions/01 §1)
		Partition:     code.Partition,
		PartitionMail: code.PartitionMail,
		PartitionNote: code.PartitionNote,
	}
}

// nonPrimaryInbound is the inbound surface a non-primary deployment's view
// carries: the tile's, which is the primary's, since no inbound edge reaches
// any other deployment (D127d) — except chrome: a non-primary deployment's
// documents are never chrome.
func nonPrimaryInbound(primary Manifest) Manifest {
	primary.Chrome = false
	return primary
}

// unionUses is a pinned deployment's uses for its env: the work tree's, then
// those only its code declares (NP-07-3). Grants still filter them.
func unionUses(workTree, code []Use) []Use {
	if len(code) == 0 {
		return workTree
	}
	out := append([]Use(nil), workTree...)
	for _, u := range code {
		dup := false
		for _, w := range workTree {
			if w == u {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, u)
		}
	}
	return out
}

// pinnedPrimary asks the PinnedPrimary hook about rel; without a hook (every
// workspace without deployments' wiring) nothing is asked.
func (r *Registry) pinnedPrimary(rel string) (*PinnedCode, bool) {
	if r.PinnedPrimary == nil {
		return nil, false
	}
	pc, ok := r.PinnedPrimary(rel)
	return pc, ok && pc != nil
}

// composePinned is the registry's component of a tile whose primary is pinned
// to pc, from wt, the work tree's scan of its directory. The inbound surface,
// the deployment-level fields, HasIndex and the native entry are the
// checkpoint's; the tile-level fields and the manifest error stay the work
// tree's, whose scan is kept as WorkTree. While the work tree has no valid
// manifest (neither xbin.json nor index.html, or an xbin.json that doesn't
// parse) the tile is Kept: registered for its pinned primary, with the
// checkpoint's tile-level fields and the work tree's error surfaced.
func composePinned(wt *Component, pc *PinnedCode, valid bool) *Component {
	c := &Component{Path: wt.Path, Dir: wt.Dir, ManifestErr: wt.ManifestErr,
		HasIndex: pc.HasIndex, Native: pc.Native, NativeErr: pc.NativeErr,
		WorkTree: &WorkTreeScan{Manifest: wt.Manifest, HasIndex: wt.HasIndex, Native: wt.Native, NativeErr: wt.NativeErr}}
	tile := wt.Manifest
	if !valid {
		c.Kept, tile = true, pc.Manifest
		c.WorkTree.Manifest = Manifest{} // what the work tree validly declares: nothing
		if c.ManifestErr != "" {
			c.ManifestErr += "; "
		}
		c.ManifestErr += keptNotice
	}
	c.Manifest = composeManifest(tile, pc.Manifest, pc.Manifest)
	return c
}

// composePinnedScopes gives each scope a pinned primary's tile roots the
// declarations of its checkpoint's scope.json, resources and importMap
// (D127n; the importMap as 16-open-questions Q4's default). Whether the
// directory roots a scope stays the work tree's; a Kept tile, whose work
// tree has no valid manifest, takes that from its checkpoint as well, as
// every other tile-level field. The work tree's refusals (Err) stay on the
// scope: the manifest error describes the work tree.
func composePinnedScopes(scopes map[string]*ScopeManifest, comps map[string]*Component, pinned map[string]*PinnedCode) {
	for rel, pc := range pinned {
		wt, roots := scopes[rel]
		if kept := comps[rel].Kept; kept && pc.Scope == nil {
			delete(scopes, rel)
			continue
		} else if !roots && !kept {
			continue
		}
		sm := &ScopeManifest{}
		if wt != nil {
			sm.Err = wt.Err
		}
		if pc.Scope != nil {
			sm.Resources = copyMap(pc.Scope.Resources)
			sm.ImportMap = copyMap(pc.Scope.ImportMap)
		}
		scopes[rel] = sm
	}
}

func copyMap[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ViewCode names the code a deployment view describes.
type ViewCode struct {
	Deployment string // the deployment the view is for; "" names the primary
	Tree       string // the checkpoint's full tree hash; "" is the work tree
	Root       string // the checkpoint's materialized tree; set with Tree
}

// View is the component a generation of deployment code.Deployment of tile
// c spawns from, running code (07-runtime §5.1). c is the registry's
// component; every view carries its Path, Dir and Scope, and the manifest
// error of its work tree.
//   - The primary on the work tree while nothing is pinned — the zero state —
//     is c itself, the registry's own pointer.
//   - The work tree otherwise: its scan. The primary takes all of it (the
//     component an unpinned scan would give); a non-primary deployment takes
//     its deployment-level fields, HasIndex and native entry. A Kept tile's
//     work tree has no valid manifest and no view.
//   - A checkpoint: read from code.Root through OpenBeneath, cached by tree
//     (checkpoints are immutable). The primary takes its inbound surface and
//     deployment-level fields; a non-primary deployment only the latter. Its
//     uses are the union of the tile's and its own, for env. A checkpoint
//     whose xbin.json doesn't parse can't start and has no view.
//
// A non-primary deployment's view carries the tile's inbound surface with
// chrome off, Deployment set, and CodeRoot for checkpoint code. Views are
// cached by (path, deployment, tree, a hash of the component's work-tree
// inputs), so a rescan that changed nothing a view reads returns the same
// pointer.
func (r *Registry) View(c *Component, code ViewCode) (*Component, error) {
	primary := code.Deployment == ""
	switch {
	case code.Tree == "" && primary && c.WorkTree == nil:
		return c, nil
	case code.Tree == "" && c.Kept:
		return nil, fmt.Errorf("%s: %s", c.Path, keptNotice)
	case code.Tree != "" && code.Root == "":
		return nil, fmt.Errorf("%s: checkpoint %s has no materialized tree", c.Path, code.Tree)
	}
	key := viewKey{path: c.Path, dep: code.Deployment, tree: code.Tree, root: code.Root, sum: viewInputs(c)}
	if v := r.views.view(key); v != nil {
		return v, nil
	}
	v := &Component{Path: c.Path, Dir: c.Dir, Scope: c.Scope, ManifestErr: c.ManifestErr, Deployment: code.Deployment}
	inbound := nonPrimaryInbound(c.Manifest)
	if code.Tree == "" {
		ws := c.WorkTree
		if ws == nil { // the primary follows the work tree: c is its scan
			ws = &WorkTreeScan{Manifest: c.Manifest, HasIndex: c.HasIndex, Native: c.Native, NativeErr: c.NativeErr}
		}
		if primary {
			inbound = ws.Manifest
		}
		v.Manifest = composeManifest(ws.Manifest, inbound, ws.Manifest)
		v.HasIndex, v.Native, v.NativeErr = ws.HasIndex, ws.Native, ws.NativeErr
	} else {
		pc, err := r.views.checkpoint(c.Path, code.Tree, code.Root)
		if err != nil {
			return nil, fmt.Errorf("%s: checkpoint %s: %w", c.Path, code.Tree, err)
		}
		if pc.ManifestErr != "" {
			return nil, fmt.Errorf("%s: checkpoint %s can't start: %s", c.Path, code.Tree, pc.ManifestErr)
		}
		if primary {
			inbound = pc.Manifest
		}
		v.Manifest = composeManifest(c.Manifest, inbound, pc.Manifest)
		v.Manifest.Uses = unionUses(c.Manifest.Uses, pc.Manifest.Uses)
		v.HasIndex, v.Native, v.NativeErr = pc.HasIndex, pc.Native, pc.NativeErr
		v.CodeRoot = code.Root
	}
	return r.views.putView(key, v), nil
}

// viewInputs hashes what a view reads from the registry's component: the
// work-tree manifest and everything composed beside it.
func viewInputs(c *Component) [sha256.Size]byte {
	b, _ := json.Marshal(struct {
		Dir, Scope, ManifestErr, Native, NativeErr string
		HasIndex, Kept                             bool
		Manifest                                   Manifest
		WorkTree                                   *WorkTreeScan
	}{c.Dir, c.Scope, c.ManifestErr, c.Native, c.NativeErr, c.HasIndex, c.Kept, c.Manifest, c.WorkTree})
	return sha256.Sum256(b)
}

// viewCacheMax bounds each of the view cache's maps; views are made per
// spawn, so a full map is simply started afresh.
const viewCacheMax = 256

type viewKey struct {
	path, dep, tree, root string
	sum                   [sha256.Size]byte
}

type checkpointKey struct{ path, tree, root string }

// viewCache holds the views View made and the checkpoints it read. The zero
// value is ready.
type viewCache struct {
	mu          sync.Mutex
	views       map[viewKey]*Component
	checkpoints map[checkpointKey]*PinnedCode
}

func (vc *viewCache) view(k viewKey) *Component {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.views[k]
}

// putView stores v under k, or returns the view another caller stored
// first, so equal inputs share one pointer.
func (vc *viewCache) putView(k viewKey, v *Component) *Component {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if old := vc.views[k]; old != nil {
		return old
	}
	if vc.views == nil || len(vc.views) >= viewCacheMax {
		vc.views = map[viewKey]*Component{}
	}
	vc.views[k] = v
	return v
}

// checkpoint is ReadCheckpoint(root), read once per (tile, tree). A read
// that fails isn't cached.
func (vc *viewCache) checkpoint(path, tree, root string) (*PinnedCode, error) {
	k := checkpointKey{path, tree, root}
	vc.mu.Lock()
	pc := vc.checkpoints[k]
	vc.mu.Unlock()
	if pc != nil {
		return pc, nil
	}
	pc, err := ReadCheckpoint(root)
	if err != nil {
		return nil, err
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if old := vc.checkpoints[k]; old != nil {
		return old, nil
	}
	if vc.checkpoints == nil || len(vc.checkpoints) >= viewCacheMax {
		vc.checkpoints = map[checkpointKey]*PinnedCode{}
	}
	vc.checkpoints[k] = pc
	return pc, nil
}
