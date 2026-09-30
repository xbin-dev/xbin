package deps

// buildwork.go — a Go tile's build workspace (D166). The workspace's root
// go.work `use`s every Go component, and in workspace mode the go command
// builds with ONE module graph over everything it uses: a tile requiring a
// newer version of a dependency changed the version every other tile built
// with, one tile's broken go.mod broke every build, and a `replace` in any
// used go.mod applied to every build — so a person who could change only
// tile A could point tile B's dependency at code of their choosing. A tile's
// build now gets a go.work of its own, rendered at build time from the
// tile's own go.mod: its own module, the xbin SDK, and only the workspace
// modules it reaches. The root go.work stays for terminals, editors and
// gopls; no build reads it.

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// SDKModule is the xbin SDK's module path, which every build's go.work
// resolves to the SDK's directory.
const SDKModule = "github.com/xbin-dev/xbin/sdk"

// buildGo is the go line of a generated build go.work: the root go.work's
// (renderGoWork), so a module the root's covers builds the same.
const buildGo = "1.24"

// Module is a Go module a tile's build may use.
type Module struct {
	// Dir is the module's directory as the build sees it: what its go.work
	// names.
	Dir string
	// Root and Rel say where xbind reads its files: at Rel (slash-separated,
	// "" = Root itself) beneath Root, never through a symlink on the way —
	// the workspace for a work tree, a materialized checkpoint shown at Dir
	// for a pinned one.
	Root, Rel string
	// Tile is the component the module belongs to; "" for a module only a
	// hand-managed root go.work lists, outside every component.
	Tile string
	// Trusted says only the workspace's admins write the module: a
	// hand-managed go.work's use outside every component. Any reference to
	// its path is served by it.
	Trusted bool
}

// Build is what one Go build's workspace is made from.
type Build struct {
	Tile string // the tile being built
	// Own are the tile's own modules — the one at its root or in backend/,
	// and the one holding its entry package when that is another — always
	// used, first.
	Own []Module
	// Others are every other Go module of the workspace: each component's,
	// and each a hand-managed root go.work uses. Only those the build
	// reaches are used (reach).
	Others []Module
	// SDK is the xbin SDK's directory ("" = none): the go.work replaces
	// SDKModule with it, as the root go.work does.
	SDK string
	// Root is the hand-managed root go.work's lines a build keeps; nil for
	// xbind's own go.work, or none.
	Root *RootWork
	// Std reports whether an import path is a standard-library package,
	// which no workspace module stands in for; nil = none is.
	Std func(importPath string) bool
	// Deps reports whether tile from names tile to in its manifest's deps;
	// nil = never.
	Deps func(from, to string) bool
}

// Work is one Go build's own workspace.
type Work struct {
	GoWork []byte   // the go.work, absolute paths
	Uses   []Module // the modules it uses: the tile's own first, then the others by Dir

	tile   string
	others []offer              // every other module and its path, for Hint
	reqs   map[string][]tileReq // what the other modules' go.mods require, for Hint
}

type offer struct {
	path string
	mod  Module
}

type tileReq struct{ who, version string }

// BuildWork renders b's build workspace: the tile's own modules, the SDK,
// and the workspace modules the tile reaches — through its go.mod's
// require and replace lines and its code's imports, and on through theirs.
//
// A reference is served by another workspace module only when it can mean
// nothing else, so no tile stands in for another's dependency: a module
// path without a dot in its first element (`calendar`: no proxy serves it),
// a `require` at v0.0.0 (or the zero pseudo-version: an unpublished
// module's placeholder), a `replace` naming the module's directory, a tile
// the referring tile's manifest names in deps, a module only admins write
// (Trusted), or an import its go.mod doesn't require at all (only a
// workspace resolves that) — unless some go.mod of the workspace requires
// that path at a published version, which says it is a real module a tile
// builds with. A dotted path at a published version — `golang.org/x/crypto
// v0.48.0` — resolves as a normal module even when a tile declares that
// path. A module the build uses brings its own go.mod's replace lines (in
// workspace mode they apply to the whole build): the tile chose to build
// with that module's code, which is in its binary either way.
func BuildWork(b Build) Work {
	w := Work{tile: b.Tile, reqs: map[string][]tileReq{}}
	type node struct {
		m   Module
		mod goMod
	}
	ownDirs := map[string]bool{}
	ownPaths := map[string]bool{}
	published := map[string]bool{} // module paths some go.mod requires at a published version
	notePublished := func(mod goMod) {
		for p, v := range mod.Requires {
			if !placeholder(v) {
				published[p] = true
			}
		}
	}
	var queue []node
	for _, m := range b.Own {
		m.Dir = filepath.Clean(m.Dir)
		if ownDirs[m.Dir] {
			continue
		}
		ownDirs[m.Dir] = true
		mod, _ := readGoModIn(m)
		if mod.Path != "" {
			ownPaths[mod.Path] = true
		}
		notePublished(mod)
		w.Uses = append(w.Uses, m)
		queue = append(queue, node{m, mod})
	}
	// module paths the go.work replaces at every version: a workspace
	// module declaring one would fail the build ("workspace module … is
	// replaced at all versions in the go.work file"), so none serves it
	replaced := map[string]bool{}
	if b.SDK != "" {
		replaced[SDKModule] = true
	}
	if b.Root != nil {
		for _, r := range b.Root.Replaces {
			if r.OldVersion == "" {
				replaced[r.Old] = true
			}
		}
	}
	var cands []node
	byPath := map[string][]int{}
	byDir := map[string]int{}
	for _, m := range b.Others {
		m.Dir = filepath.Clean(m.Dir)
		if ownDirs[m.Dir] {
			continue
		}
		mod, ok := readGoModIn(m)
		if !ok || mod.Path == "" {
			continue // unreadable, or no module: it serves nothing (and breaks no one)
		}
		who := m.Tile
		if who == "" {
			who = m.Dir
		}
		for p, v := range mod.Requires {
			w.reqs[p] = append(w.reqs[p], tileReq{who, v})
		}
		notePublished(mod)
		w.others = append(w.others, offer{mod.Path, m})
		if ownPaths[mod.Path] || replaced[mod.Path] {
			continue // the tile's own path, or the go.work's: never another module's
		}
		byPath[mod.Path] = append(byPath[mod.Path], len(cands))
		byDir[m.Dir] = len(cands)
		cands = append(cands, node{m, mod})
	}
	paths := make([]string, 0, len(byPath)+len(ownPaths))
	for p := range byPath {
		paths = append(paths, p)
	}
	for p := range ownPaths {
		paths = append(paths, p)
	}
	used := map[int]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		use := func(i int) {
			if !used[i] {
				used[i] = true
				queue = append(queue, cands[i])
			}
		}
		serve := func(p, version string) {
			for _, i := range byPath[p] {
				if b.serves(n.m, cands[i].m, p, version, published[p]) {
					use(i)
				}
			}
		}
		for p, v := range n.mod.Requires {
			serve(p, v)
		}
		for _, r := range n.mod.Replaces {
			if !r.dir() {
				serve(r.New, r.NewVersion)
				continue
			}
			d := filepath.FromSlash(r.New)
			if !filepath.IsAbs(d) {
				d = filepath.Join(n.m.Dir, d)
			}
			if i, ok := byDir[filepath.Clean(d)]; ok {
				use(i) // the go.mod names that very directory
			}
		}
		for _, imp := range scanImports(n.m) {
			if imp == "C" || (b.Std != nil && b.Std(imp)) {
				continue
			}
			if p := longestModule(paths, imp); p != "" && !ownPaths[p] {
				serve(p, n.mod.Requires[p])
			}
		}
	}
	var more []Module
	for i := range cands {
		if used[i] {
			more = append(more, cands[i].m)
		}
	}
	sort.Slice(more, func(i, j int) bool { return more[i].Dir < more[j].Dir })
	w.Uses = append(w.Uses, more...)
	w.GoWork = renderBuildWork(w.Uses, b.SDK, b.Root)
	return w
}

// serves reports whether workspace module to may serve a reference to path
// p (at version: "" for an import without a require) from module from;
// published says some go.mod of the workspace requires p at a published
// version (BuildWork).
func (b Build) serves(from, to Module, p, version string, published bool) bool {
	switch {
	case to.Trusted, !dottedPath(p), placeholder(version):
		return true
	case version == "" && !published:
		return true
	case b.Deps != nil && from.Tile != "" && to.Tile != "" && from.Tile != to.Tile:
		return b.Deps(from.Tile, to.Tile)
	}
	return false
}

// placeholder reports whether a required version is the stand-in for an
// unpublished module: v0.0.0, or the zero pseudo-version `go mod tidy`
// writes for a replaced one.
func placeholder(v string) bool {
	return v == "v0.0.0" || strings.HasPrefix(v, "v0.0.0-00010101000000-")
}

// dottedPath reports whether a module path's first element holds a dot —
// one a proxy could serve (a dotless one only a workspace or a replace can).
func dottedPath(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return strings.Contains(first, ".")
}

// longestModule is the longest of paths that is imp or a prefix of it at a
// path boundary; "" when none is.
func longestModule(paths []string, imp string) string {
	best := ""
	for _, p := range paths {
		if len(p) > len(best) && (imp == p || strings.HasPrefix(imp, p+"/")) {
			best = p
		}
	}
	return best
}

func renderBuildWork(uses []Module, sdk string, rw *RootWork) []byte {
	var sb strings.Builder
	sb.WriteString("// Code generated by xbind: this tile build's own workspace (D166), made from\n")
	sb.WriteString("// the tile's go.mod at each build. The workspace's go.work is not read.\n\n")
	goLine := buildGo
	if rw != nil && rw.Go != "" {
		goLine = rw.Go
	}
	fmt.Fprintf(&sb, "go %s\n", modToken(goLine))
	if rw != nil && rw.Toolchain != "" {
		fmt.Fprintf(&sb, "\ntoolchain %s\n", modToken(rw.Toolchain))
	}
	if rw != nil && len(rw.Godebug) > 0 {
		sb.WriteString("\ngodebug (\n")
		for _, g := range rw.Godebug {
			fmt.Fprintf(&sb, "\t%s\n", modToken(g))
		}
		sb.WriteString(")\n")
	}
	sb.WriteString("\nuse (\n")
	for _, m := range uses {
		fmt.Fprintf(&sb, "\t%s\n", modToken(m.Dir))
	}
	sb.WriteString(")\n")
	if sdk != "" && (rw == nil || !rw.replacesAll(SDKModule)) {
		// a replace, not a use, as in the root go.work (renderGoWork)
		fmt.Fprintf(&sb, "\nreplace %s => %s\n", SDKModule, modToken(sdk))
	}
	if rw != nil && len(rw.Replaces) > 0 {
		sb.WriteString("\n")
		for _, r := range rw.Replaces {
			sb.WriteString(r.render(r.New) + "\n")
		}
	}
	return []byte(sb.String())
}

// noProvider is the go command's error for an import no module the build
// uses provides.
var noProvider = regexp.MustCompile(`no required module provides package ([^\s;:]+)`)

// Hint explains a failed build's "no required module provides package"
// errors that the build's own workspace causes, for the build's output:
// the package is in a workspace module the build doesn't use, or another
// tile's go.mod requires its module where this tile's doesn't (a build
// with the shared go.work picked that up). "" when there is nothing to say.
func (w Work) Hint(output string) string {
	var notes []string
	seen := map[string]bool{}
	for _, m := range noProvider.FindAllStringSubmatch(output, -1) {
		pkg := m[1]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		if o, ok := w.offerFor(pkg); ok {
			who := o.mod.Tile
			if who == "" {
				who = o.mod.Dir
			}
			note := fmt.Sprintf("%s is in %s's Go module %s, which this build doesn't use: add `require %s v0.0.0` to %s's go.mod", pkg, who, o.path, o.path, w.tile)
			if o.mod.Tile != "" {
				note += fmt.Sprintf(", or name %s in its xbin.json deps", o.mod.Tile)
			}
			notes = append(notes, note)
			continue
		}
		if p, r, ok := w.requiredFor(pkg); ok {
			notes = append(notes, fmt.Sprintf("%s's go.mod requires %s %s and %s's doesn't — each Go tile builds against its own go.mod now (D166): add `require %s %s` to %s's go.mod", r.who, p, r.version, w.tile, p, r.version, w.tile))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "xbind: " + strings.Join(notes, "\nxbind: ") + "\n(/docs/changes/2026-09-30-go-build-workspace.md)"
}

// offerFor is the other workspace module whose path is the longest prefix
// of pkg.
func (w Work) offerFor(pkg string) (offer, bool) {
	var best offer
	for _, o := range w.others {
		if len(o.path) > len(best.path) && (pkg == o.path || strings.HasPrefix(pkg, o.path+"/")) {
			best = o
		}
	}
	return best, best.path != ""
}

// requiredFor is the module path, a prefix of pkg, that other modules
// require, and the requirement at the highest version.
func (w Work) requiredFor(pkg string) (string, tileReq, bool) {
	best := ""
	for p := range w.reqs {
		if len(p) > len(best) && (pkg == p || strings.HasPrefix(pkg, p+"/")) {
			best = p
		}
	}
	if best == "" {
		return "", tileReq{}, false
	}
	rs := w.reqs[best]
	top := rs[0]
	for _, r := range rs[1:] {
		if semverLess(top.version, r.version) || (top.version == r.version && r.who < top.who) {
			top = r
		}
	}
	return best, top, true
}

// semverLess orders module versions well enough for a hint: by the numbers
// of vMAJOR.MINOR.PATCH, a pre-release below its release.
func semverLess(a, b string) bool {
	pa, ra := semverParts(a)
	pb, rb := semverParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	switch {
	case ra == rb:
		return false
	case ra == "":
		return false
	case rb == "":
		return true
	}
	return ra < rb
}

func semverParts(v string) ([3]int, string) {
	var n [3]int
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	for i, f := range strings.SplitN(core, ".", 3) {
		n[i], _ = strconv.Atoi(f)
	}
	return n, pre
}

// ModuleSub is where the Go module of a component at rel beneath root
// lives, as the root go.work lists it (goModules): "" for one at its root,
// "backend" for one in backend/; ok=false when neither holds a go.mod.
// Nothing is followed out of root.
func ModuleSub(root, rel string) (string, bool) {
	for _, sub := range []string{"", "backend"} {
		if regularBeneath(root, path.Join(rel, sub, "go.mod")) {
			return sub, true
		}
	}
	return "", false
}

// EntryModule is the directory, relative to the tile's (at rel beneath
// root), of the module that holds its entry package: the nearest go.mod
// from the entry's directory up to the tile's, as the go command finds it.
// ok=false for an entry that isn't a directory of the tile ("./…"), or no
// go.mod on the way.
func EntryModule(root, rel, entry string) (string, bool) {
	if entry != "." && !strings.HasPrefix(entry, "./") {
		return "", false
	}
	dir := path.Clean(entry)
	if strings.HasSuffix(dir, ".go") {
		dir = path.Dir(dir)
	}
	if !fs.ValidPath(dir) {
		return "", false
	}
	for {
		if regularBeneath(root, path.Join(rel, dir, "go.mod")) {
			if dir == "." {
				dir = ""
			}
			return dir, true
		}
		if dir == "." {
			return "", false
		}
		dir = path.Dir(dir)
	}
}

func regularBeneath(root, rel string) bool {
	f, err := fsutil.OpenBeneath(root, filepath.FromSlash(rel))
	if err != nil {
		return false
	}
	fi, err := f.Stat()
	f.Close()
	return err == nil && fi.Mode().IsRegular()
}

// readGoModIn reads m's go.mod; ok=false when it can't be read.
func readGoModIn(m Module) (goMod, bool) {
	f, err := fsutil.OpenBeneath(m.Root, filepath.FromSlash(path.Join(m.Rel, "go.mod")))
	if err != nil {
		return goMod{}, false
	}
	defer f.Close()
	b, ok := readCapped(f, modFileMax)
	if !ok {
		return goMod{}, false
	}
	return parseGoMod(b), true
}

// scanImports limits: files beyond them go unscanned — their imports
// reach nothing, and a build that needs one says so (Hint).
const (
	scanMaxFiles = 20000
	scanFileMax  = 1 << 20 // bytes read of one file: its imports come first
)

// scanImports lists the import paths of m's Go files: every .go file but
// tests, in every directory a build can compile a package from — "_*" and
// testdata too, which only `./...` skips (a template's entry is
// `./_backend`) — but ".*" (no import path holds one), vendor/,
// node_modules/ and nested modules. Build constraints are not evaluated —
// a superset is harmless. Nothing is followed out of the module's
// directory, and nothing but a regular file is read.
func scanImports(m Module) []string {
	r, err := fsutil.OpenRootIn(m.Root, filepath.FromSlash(m.Rel))
	if err != nil {
		return nil
	}
	defer r.Close()
	seen := map[string]bool{}
	fset := token.NewFileSet()
	files := 0
	_ = fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p == "." {
				return nil
			}
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return fs.SkipDir
			}
			if fi, err := r.Lstat(p + "/go.mod"); err == nil && !fi.IsDir() {
				return fs.SkipDir // a nested module is a module of its own
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || d.Type()&^fs.ModeSymlink != 0 {
			return nil
		}
		if files++; files > scanMaxFiles {
			slog.Warn("go build workspace: too many Go files to scan", "module", m.Dir, "max", scanMaxFiles)
			return fs.SkipAll
		}
		// never blocks on a FIFO a link swapped in: O_NONBLOCK, then regular only
		f, err := r.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil
		}
		defer f.Close()
		if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		src, err := io.ReadAll(io.LimitReader(f, scanFileMax))
		if err != nil {
			return nil
		}
		af, _ := parser.ParseFile(fset, p, src, parser.ImportsOnly)
		if af == nil {
			return nil
		}
		for _, im := range af.Imports {
			if v, err := strconv.Unquote(im.Path.Value); err == nil {
				seen[v] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// RootWork is what a hand-managed root go.work says that every tile's build
// keeps: its go and toolchain lines, its godebug settings and replace lines
// (the directory of a relative replacement made absolute), and the modules
// it uses (candidates, as the components' are: a build uses those it
// reaches). Only admins write the workspace's root, so its lines are the
// workspace's own settings, not a tile's.
type RootWork struct {
	Go, Toolchain string
	Godebug       []string
	Replaces      []replaceDirective
	Uses          []string // absolute, clean
}

// replacesAll reports whether the go.work replaces module p at every version.
func (rw *RootWork) replacesAll(p string) bool {
	for _, r := range rw.Replaces {
		if r.Old == p && r.OldVersion == "" {
			return true
		}
	}
	return false
}

// ReadRootWork reads the workspace's go.work for a build: nil when there is
// none or xbind generated it (its lines are the modules and the SDK, which
// every build workspace renders itself), else a hand-managed one's lines.
// It is read beneath the workspace: one that leaves it through a symlink is
// an error, and none of its lines reach a build.
func ReadRootWork(root string) (*RootWork, error) {
	f, err := fsutil.OpenBeneath(root, "go.work")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("the workspace's go.work can't be read: %w", err)
	}
	b, ok := readCapped(f, modFileMax)
	f.Close()
	if !ok {
		return nil, fmt.Errorf("the workspace's go.work can't be read")
	}
	if bytes.HasPrefix(b, []byte(workMarker+"\n")) {
		return nil, nil
	}
	rw := &RootWork{}
	abs := func(p string) string {
		p = filepath.FromSlash(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		return filepath.Clean(p)
	}
	for _, l := range parseModLines(b) {
		switch {
		case l.verb == "go" && len(l.args) == 1:
			rw.Go = l.args[0]
		case l.verb == "toolchain" && len(l.args) == 1:
			rw.Toolchain = l.args[0]
		case l.verb == "godebug" && len(l.args) == 1:
			rw.Godebug = append(rw.Godebug, l.args[0])
		case l.verb == "use" && len(l.args) == 1:
			rw.Uses = append(rw.Uses, abs(l.args[0]))
		case l.verb == "replace":
			if r, ok := parseReplace(l.args); ok {
				if r.dir() {
					r.New = abs(r.New)
				}
				rw.Replaces = append(rw.Replaces, r)
			}
		}
	}
	return rw, nil
}
