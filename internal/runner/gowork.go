package runner

// gowork.go — a Go build's own workspace (D166): the go.work a build runs
// with is rendered at build time from the tile's own go.mod
// (deps.BuildWork) — the tile's modules, the xbin SDK and the workspace
// modules the tile reaches — never the workspace's root go.work, whose one
// module graph let every Go tile's go.mod steer every other tile's build.
// Rendered per build, it names the tile the moment the registry does: a new
// tile's first build no longer races the root go.work's regeneration.

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
)

// buildWork is the build workspace of c (the tile, or a deployment's view
// of it) for entry: its code read from codeRoot when that is a checkpoint
// tree shown at c.Dir ("" = the work tree), and each other component's
// from shown's tree when a checkpoint build shows it from its primary's
// checkpoint (checkpointPlan; nil for a work-tree build). ok=false when c
// has no Go module of its own: the build runs with GOWORK=off, and `go`
// says "go.mod file not found".
func (r *Runner) buildWork(c *registry.Component, entry, codeRoot string, shown map[string]shownComp) (deps.Work, bool) {
	root, rel := r.readRoot(c.Dir)
	if codeRoot != "" {
		root, rel = codeRoot, ""
	}
	var own []deps.Module
	addOwn := func(sub string) {
		own = append(own, deps.Module{Dir: filepath.Join(c.Dir, filepath.FromSlash(sub)), Root: root, Rel: path.Join(rel, sub), Tile: c.Path})
	}
	if sub, ok := deps.ModuleSub(root, rel); ok {
		addOwn(sub)
	}
	if sub, ok := deps.EntryModule(root, rel, entry); ok {
		addOwn(sub) // BuildWork drops a repeat
	}
	if len(own) == 0 {
		return deps.Work{}, false
	}
	rw, err := deps.ReadRootWork(r.Root)
	if err != nil {
		slog.Warn("go build: the workspace's go.work is not a file beneath the workspace; its lines don't reach tile builds", "err", err)
	}
	comps := r.components()
	// where xbind reads component n's code: its primary's checkpoint when the
	// build shows that, else its work tree
	codeOf := func(n *registry.Component) (string, string) {
		if s, ok := shown[n.Path]; ok && s.tree != "" {
			return s.root, ""
		}
		return r.readRoot(n.Dir)
	}
	var others []deps.Module
	have := map[string]bool{}
	for _, n := range comps {
		if n.Path == c.Path {
			continue
		}
		nroot, nrel := codeOf(n)
		sub, ok := deps.ModuleSub(nroot, nrel)
		if !ok {
			continue
		}
		m := deps.Module{Dir: filepath.Join(n.Dir, filepath.FromSlash(sub)), Root: nroot, Rel: path.Join(nrel, sub), Tile: n.Path}
		have[m.Dir] = true
		others = append(others, m)
	}
	if rw != nil {
		for _, u := range rw.Uses {
			if have[u] {
				continue
			}
			have[u] = true
			m := deps.Module{Dir: u, Root: u}
			if n := owningComponent(comps, u); n != nil {
				nroot, nrel := codeOf(n)
				sub, _ := filepath.Rel(n.Dir, u)
				m.Root, m.Rel, m.Tile = nroot, path.Join(nrel, filepath.ToSlash(sub)), n.Path
			} else if within(u, r.Root) {
				sub, _ := filepath.Rel(r.Root, u)
				m.Root, m.Rel = r.Root, filepath.ToSlash(sub)
				// the workspace's own directories (not .xbin, data or homes,
				// which sandboxes and people write): admins' alone
				first, _, _ := strings.Cut(m.Rel, "/")
				m.Trusted = first != ".xbin" && first != "data" && first != "homes"
			} else {
				m.Trusted = true // outside the workspace: the admin's own choice
			}
			others = append(others, m)
		}
	}
	b := deps.Build{Tile: c.Path, Own: own, Others: others, Root: rw, Deps: r.namesInDeps(c)}
	if sdk := deps.SDKPath(); sdk != "" {
		if abs, err := filepath.Abs(sdk); err == nil {
			sdk = abs
		}
		b.SDK = sdk
	}
	if tc, err := hostToolchain(); err == nil {
		b.Std = stdPackage(tc.goroot)
	}
	return deps.BuildWork(b), true
}

// readRoot is where xbind reads a component's files at dir: beneath the
// workspace at its relative path, or beneath dir itself for one outside it.
func (r *Runner) readRoot(dir string) (root, rel string) {
	if within(dir, r.Root) {
		if p, err := filepath.Rel(r.Root, dir); err == nil {
			if p == "." {
				p = ""
			}
			return r.Root, filepath.ToSlash(p)
		}
	}
	return dir, ""
}

// owningComponent is the innermost component whose directory holds dir.
func owningComponent(comps []*registry.Component, dir string) *registry.Component {
	var best *registry.Component
	for _, n := range comps {
		if within(dir, n.Dir) && (best == nil || len(n.Dir) > len(best.Dir)) {
			best = n
		}
	}
	return best
}

// namesInDeps reports whether tile from names tile to in its manifest's
// deps: c's own manifest (a deployment's view says what that deployment
// runs), the registry's for any other tile.
func (r *Runner) namesInDeps(c *registry.Component) func(from, to string) bool {
	return func(from, to string) bool {
		m := c.Manifest
		if from != c.Path {
			if r.Reg == nil {
				return false
			}
			n, ok := r.Reg.Component(from)
			if !ok {
				return false
			}
			m = n.Manifest
		}
		return slices.Contains(m.Deps, to)
	}
}

// stdPackage reports whether an import path is a package of the standard
// library under goroot: one no workspace module stands in for, whatever
// module path a tile declares.
func stdPackage(goroot string) func(string) bool {
	return func(imp string) bool {
		if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") || !fsValid(imp) {
			return false
		}
		// walk-ok: the host toolchain's own GOROOT, not tile data
		fi, err := os.Stat(filepath.Join(goroot, "src", filepath.FromSlash(imp)))
		return err == nil && fi.IsDir()
	}
}

// fsValid reports whether p is a clean relative slash path (no "..").
func fsValid(p string) bool {
	return p != "" && p == path.Clean(p) && !strings.HasPrefix(p, "../") && p != ".." && !strings.HasPrefix(p, "/")
}

// buildWorkTTL is how long a build workspace no build has written stays in
// a tile's work directory.
const buildWorkTTL = 7 * 24 * time.Hour

// writeBuildWork writes a build's go.work into work, the tile's (or its
// protected primary's) work directory beside its caches: into a directory
// of its own named by its content, bound read-write into the build, so the
// go.work.sum beside it is written by the builds of that same workspace
// alone, and builds with different workspaces (a deployment's checkpoint
// and the work tree) never share one file. A new directory's go.work.sum is
// seeded from the workspace's go.work.sum and the tile's newest one, so a
// changed workspace keeps the checksums its builds already added. work
// itself is bound into no build: what sits in it was left by xbind or by a
// build before D166, and is never followed (openArtifacts, fsutil.OpenIn).
// Directories no build has written for buildWorkTTL are removed.
func writeBuildWork(work string, content []byte, wsRoot string) (string, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	h := sha256.Sum256(content)
	name := hex.EncodeToString(h[:8])
	fd, err := openArtifacts(work, name)
	if err != nil {
		return "", err
	}
	unix.Close(fd)
	dir := filepath.Join(work, name)
	gw := filepath.Join(dir, "go.work")
	if err := fsutil.WriteFileAtomic(gw, content, 0o644); err != nil {
		return "", err
	}
	if _, err := os.Lstat(gw + ".sum"); os.IsNotExist(err) {
		if seed := seedWorkSum(work, name, wsRoot); len(seed) > 0 {
			if err := fsutil.WriteFileAtomic(gw+".sum", seed, 0o644); err != nil {
				return "", err
			}
		}
	}
	// walk-ok: work is xbind's own (made here, bound into no build); only
	// its entries' names and link-free infos are read
	ents, _ := os.ReadDir(work)
	for _, e := range ents {
		if e.Name() == name {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > buildWorkTTL {
			_ = os.RemoveAll(filepath.Join(work, e.Name())) // a link is removed, never followed
		}
	}
	return gw, nil
}

// workSumMax caps a go.work.sum read to seed another.
const workSumMax = 16 << 20

// seedWorkSum is a new build workspace's first go.work.sum: the lines of
// the workspace's go.work.sum and of the newest one the tile's builds wrote
// under work (another workspace's directory, or work/go.work.sum from
// before D166), each a regular file read without following a link.
func seedWorkSum(work, name, wsRoot string) []byte {
	var parts [][]byte
	if b, ok := readRegular(filepath.Join(wsRoot, "go.work.sum"), workSumMax); ok {
		parts = append(parts, b)
	}
	var newest []byte
	var at time.Time
	consider := func(f *os.File, err error) {
		if err != nil {
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > workSumMax || !fi.ModTime().After(at) {
			return
		}
		b := make([]byte, fi.Size())
		if n, err := f.ReadAt(b, 0); err == nil || n == len(b) {
			newest, at = b[:n], fi.ModTime()
		}
	}
	// walk-ok: work is xbind's own; each file in it is opened beneath it
	ents, _ := os.ReadDir(work)
	for _, e := range ents {
		switch {
		case e.Name() == name:
		case e.Name() == "go.work.sum" && e.Type().IsRegular():
			consider(fsutil.OpenBeneath(work, "go.work.sum"))
		case e.IsDir():
			consider(fsutil.OpenIn(work, e.Name(), "go.work.sum"))
		}
	}
	if newest != nil {
		parts = append(parts, newest)
	}
	seen := map[string]bool{}
	var lines []string
	for _, p := range parts {
		for _, l := range strings.Split(string(p), "\n") {
			if l = strings.TrimSpace(l); l != "" && !seen[l] {
				seen[l] = true
				lines = append(lines, l)
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	slices.Sort(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

// withHint adds what the build's own workspace explains about a failed
// build's output (deps.Work.Hint).
func withHint(out string, w *deps.Work) string {
	if w == nil {
		return out
	}
	if h := w.Hint(out); h != "" {
		return strings.TrimRight(out, "\n") + "\n\n" + h
	}
	return out
}
