package runner

// buildprotected.go — a protected primary's build products (07-runtime §3.4;
// D127m; 06-security T16, T17). While a tile's primary is protected, what it
// runs changes only by a tile manager's act, and its Go artifacts, Go caches
// and env layers are part of what it runs, so they live apart, under
// .xbin/deploy/<TileKey>/protected/: artifacts at build/<tree>/, caches at
// cache/{go-build,mod}, env layers at env/<envHash>/. No other build binds
// or reads anything there: every build masks .xbin and binds back only its
// own products' directories, a work-tree build's are .xbin/build/<CompKey>/
// and the tile's shared caches, and gcEnvLayers sweeps .xbin/env/<CompKey>/
// alone. The checkpoint store's GC and sweep touch only full tree ids and
// .tmp-* entries of .xbin/deploy/<TileKey>/, never protected/.
//
// Who writes: only a manager-initiated operation onto the protected primary
// (deploy, promote, roll back, reload now, reassignment, a restart-as-deploy,
// protecting itself) through prepareProtected, which reuses a product only
// from this namespace and otherwise builds it here. A restart path never
// builds here, except a Go artifact lost with .xbin/ whose recorded inputs
// all still match (rebuildLostProtected); otherwise the start is held with
// ErrProtectedLost, and so is a lost env layer, since setup reaches the
// network.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// products is where a build's products live: a tile's shared namespace, or
// its protected primary's.
type products struct {
	// base and arts: the artifacts are base/arts/<tree>/, as
	// .xbin/build/<CompKey>/c/<tree>/ or .xbin/deploy/<TileKey>/protected/build/<tree>/.
	base, arts        string
	gocache, modcache string // the Go caches the build writes
	env               string // the env layers, one directory per hash
}

// sharedProducts is tile's shared namespace: its deployments' checkpoint
// artifacts, the Go caches every build of the tile writes, and the env
// layers shared by hash (07-runtime §3.1, §3.3).
func (r *Runner) sharedProducts(tile string) products {
	gocache, modcache := goCaches(r.Root, tile)
	return products{
		base: filepath.Join(r.Root, ".xbin", "build", util.CompKey(tile)), arts: "c",
		gocache: gocache, modcache: modcache, env: r.envLayers(tile),
	}
}

// protectedProducts is tile's protected primary's own namespace. It is keyed
// by TileKey (05-model §3): a CompKey can be ground, and nothing another
// tile's builds write may land here.
func (r *Runner) protectedProducts(tile string) products {
	p := filepath.Join(r.Root, ".xbin", "deploy", util.TileKey(tile), "protected")
	return products{
		base: p, arts: "build",
		gocache: filepath.Join(p, "cache", "go-build"), modcache: filepath.Join(p, "cache", "mod"),
		env: filepath.Join(p, "env"),
	}
}

// ErrProtectedLost holds a protected primary's start whose build products
// are gone (.xbin/ lost) and can't be rebuilt on a restart path: its status
// names the checkpoint a tile manager must redeploy (07-runtime §3.4,
// NP-07-14), and the managers get an alert.
var ErrProtectedLost = errors.New("the protected primary's build products were lost")

func protectedLost(tree string) error {
	return fmt.Errorf("%w; a tile manager must redeploy c:%.7s", ErrProtectedLost, tree)
}

// prepareProtected makes what a manager-initiated operation onto tile v's
// protected primary puts on it: for checkpoint tree, shown by its view v
// (whose CodeRoot is the materialized tree), the env layer and the Go
// artifact, each reused only from the protected namespace and otherwise
// built there, however many other deployments hold one for the same tree or
// setup. The namespace then keeps the new products and those the primary
// runs now, its previous ones (07-runtime §2.8), and nothing else. rec is
// the artifact's build.json, which the deployments plane keeps a copy of
// (data/deployments/<TileKey>/protected-build.json) so a restart after
// .xbin/ loss can check the inputs (rebuildLostProtected); nil without a Go
// artifact.
func (r *Runner) prepareProtected(v *registry.Component, tree string) (bin string, rec []byte, err error) {
	switch {
	case !fullTree(tree):
		return "", nil, fmt.Errorf("%s: %q is not a checkpoint tree", v.Path, tree)
	case !r.Isolate:
		return "", nil, errPinnedNeedsIsolation
	}
	root := v.CodeRoot
	if root == "" {
		if root, err = r.materialize(v.Path, tree); err != nil {
			return "", nil, err
		}
	}
	pr := r.protectedProducts(v.Path)
	prevTree, prevEnv := r.runningCode(v.Path)
	if _, err := r.buildEnvLayer(v, pr.env, func(built string) {
		gcLayers(pr.env, map[string]bool{built: true, prevEnv: prevEnv != ""})
	}); err != nil {
		return "", nil, err
	}
	if v.Manifest.Runtime != "go" {
		return "", nil, nil
	}
	if bin, err = r.buildCheckpointGoIn(v, tree, root, pr); err != nil {
		return "", nil, err
	}
	if rec, err = readArtifactRecord(pr, tree); err != nil {
		return "", nil, err
	}
	r.pruneProtected(pr, tree, prevTree)
	return bin, rec, nil
}

// protectedArtifact returns the Go binary the protected namespace keeps for
// tile v's checkpoint tree, and whether it does: a regular bin beside a
// build.json recording v's path and the tree. An artifact of the same tree
// in the shared namespace is never it.
func (r *Runner) protectedArtifact(v *registry.Component, tree string) (string, bool) {
	pr := r.protectedProducts(v.Path)
	if !fullTree(tree) {
		return "", false
	}
	if rec, ok := artifactRecordIn(pr.base, pr.arts, tree); !ok || rec.Tile != v.Path {
		return "", false
	}
	return filepath.Join(pr.base, pr.arts, tree, "bin"), true
}

// rebuildLostProtected answers a restart path that finds the protected
// primary's artifact of tree missing: a rebuild into the protected
// namespace, only when every input the saved build.json recorded (the
// plane's copy) still matches — the tile and the checkpoint, the toolchain
// and the Go settings, each go.work module's code, and go.sum's module set.
// Otherwise the start is held (ErrProtectedLost), with what moved.
func (r *Runner) rebuildLostProtected(v *registry.Component, tree string, saved []byte) (string, error) {
	if !fullTree(tree) {
		return "", fmt.Errorf("%s: %q is not a checkpoint tree", v.Path, tree)
	}
	old, err := parseBuildRecord(saved)
	if err != nil || old.Tile != v.Path || old.Tree != tree {
		return "", protectedLost(tree)
	}
	root := v.CodeRoot
	if root == "" {
		if root, err = r.materialize(v.Path, tree); err != nil {
			return "", err
		}
	}
	plan, err := r.checkpointPlan(v, tree, root)
	if err != nil {
		return "", err
	}
	if moved := r.newBuildRecord(v, tree, plan, time.Now()).changedFrom(old); len(moved) > 0 {
		return "", fmt.Errorf("%w (its inputs moved: %s)", protectedLost(tree), strings.Join(moved, "; "))
	}
	return r.buildCheckpointGoIn(v, tree, root, r.protectedProducts(v.Path))
}

// protectedLayer is a restart path's env layer for tile v's protected
// primary: the protected namespace's layer for v's setup, never built here
// (setup reaches the network), so a lost one holds the start. "" when v
// declares no setup.
func (r *Runner) protectedLayer(v *registry.Component, tree string) (string, error) {
	dir := r.envLayerDirIn(v, r.protectedProducts(v.Path).env)
	if dir == "" {
		return "", nil
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".ok")); err != nil || !fi.Mode().IsRegular() {
		return "", protectedLost(tree)
	}
	return filepath.Join(dir, "upper"), nil
}

// runningCode is the checkpoint tree and env-layer hash the running
// generation of tile's primary uses ("" for none).
func (r *Runner) runningCode(tile string) (tree, envHash string) {
	s := r.existingState(tile)
	if s == nil {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return "", ""
	}
	return s.cur.code.Tree, s.cur.envHash
}

// pruneProtected removes the protected namespace's artifacts but those of
// keep and those a generation still runs; a build's temporary directory is
// left until it is old enough to be a crashed build's (artifactTmpAge).
func (r *Runner) pruneProtected(pr products, keep ...string) {
	dir := filepath.Join(pr.base, pr.arts)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	kept := map[string]bool{}
	for _, t := range keep {
		kept[t] = t != ""
	}
	for _, e := range ents {
		name := e.Name()
		if kept[name] {
			continue
		}
		if strings.Contains(name, ".tmp-") {
			if fi, err := e.Info(); err != nil || time.Since(fi.ModTime()) < artifactTmpAge {
				continue
			}
		}
		p := filepath.Join(dir, name)
		r.inUse.mu.Lock() // held across the removal: a start can't take it meanwhile
		if r.inUse.arts[p] == 0 {
			_ = os.RemoveAll(p)
		}
		r.inUse.mu.Unlock()
	}
}

// readArtifactRecord reads the build.json of pr's artifact of tree, beneath
// the namespace, never following a symlink.
func readArtifactRecord(pr products, tree string) ([]byte, error) {
	f, err := fsutil.OpenIn(pr.base, pr.arts+"/"+tree, "build.json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, buildRecordMax+1))
	if err == nil && len(b) > buildRecordMax {
		err = errors.New("build.json is too large")
	}
	return b, err
}

// parseBuildRecord decodes a build.json read back: none that is empty or
// larger than buildRecordMax.
func parseBuildRecord(b []byte) (*buildRecord, error) {
	if len(b) == 0 || len(b) > buildRecordMax {
		return nil, errors.New("no usable build.json")
	}
	rec := &buildRecord{}
	if err := json.Unmarshal(b, rec); err != nil {
		return nil, err
	}
	return rec, nil
}
