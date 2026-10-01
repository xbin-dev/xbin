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
// modules its own go.mod, manifest and code choose. The root go.work stays
// for terminals, editors and gopls; no build reads it.

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
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// SDKModule is the xbin SDK's module path, which every build's go.work
// resolves to the SDK's directory.
const SDKModule = "github.com/xbin-dev/xbin/sdk"

// buildGo is the lowest go line of a generated go.work, a build's and the
// root's (goModules). A module the go.work uses whose go line is higher
// raises it — the go command refuses a go.work older than a module it uses.
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
	// and the one holding its entry package when that is another (or, for a
	// tile holding none, the component module it sits in) — always used,
	// first.
	Own []Module
	// Others are every other Go module of the workspace: each component's,
	// and each a hand-managed root go.work uses. Only those the build's own
	// references choose are used (BuildWork).
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

	// for Hint, which names module paths and versions only — never another
	// tile, or its directory (the build's output is the tile's readers', and
	// D40 keeps unreadable tiles' names from them)
	tile      string
	paths     []string            // every other workspace module's path
	published map[string]string   // a dotted path → the highest published version a workspace go.mod requires it at
	ambiguous map[string][]string // an import several workspace modules could provide (none used) → their paths
}

// reach is how a build came to use a workspace module.
type reach uint8

const (
	// reachChosen: a replace names its directory, the referring tile's
	// manifest names its tile in deps, or only admins write it (Trusted) —
	// the choice of that very code.
	reachChosen reach = 1 << iota
	// reachPath: a require of its module path that only a workspace can
	// serve — a dotless path, or a placeholder version.
	reachPath
	// reachImport: an import no go.mod line of the build covers, of a
	// dotless path (or from a module nested in the tile's own), that it
	// alone provides.
	reachImport
)

// node is a module and its go.mod.
type node struct {
	m   Module
	mod goMod
}

// BuildWork renders b's build workspace: the tile's own modules, the SDK,
// and each other workspace module a module the build uses chooses —
// starting from the tile's own go.mod and code, and on through what the
// chosen modules choose. Whether a reference chooses a workspace module is
// read from the referring module's own go.mod and manifest, never from the
// go.mod of a module the build doesn't use:
//
//   - A require line whose go.mod also replaces that path is the replace's
//     alone: a directory is served only by the workspace module at that
//     directory, a module path only as a require of it would be.
//   - A require of path p is served by a workspace module declaring p when
//     p is dotless (`calendar`: no proxy serves it), the version is a
//     placeholder (v0.0.0, or the zero pseudo-version `go mod tidy` writes),
//     the referring tile's manifest names the module's tile in deps, or only
//     admins write it (Trusted). A dotted path at a published version
//     (`golang.org/x/crypto v0.48.0`) resolves as a normal module.
//   - An import is looked up among the workspace's modules only when no
//     path a used module requires or replaces, the SDK's or the go.work's
//     replaces covers it, and no module the build uses holds its package:
//     then the one workspace module that holds its package (as the go
//     command finds one: the directory, no nested go.mod on the way, a .go
//     file) serves it — a dotless path, a module nested in the tile's own
//     module's directory, or one of a deps-named tile or an admin's (a
//     dotted import no go.mod line covers is another tile's only by such a
//     choice: a namesake must not fill in a require the go.mod lacks). None
//     serves it when several could (the build fails, and Hint says how to
//     choose one), and a module whose path lies under the tile's own module
//     path serves it only from inside that module's directory (a nested
//     module, never a namesake elsewhere).
//
// A module is never used when its path is the tile's own, the SDK's or one
// beneath it, or one the go.work replaces at every version. A last check
// drops a module served only by path or import whose path lies under a
// dotted path a used module requires at a published version (and one
// served only by an import whose path a used module's go.mod covers), and
// resolves again without it. A module the build uses brings its go.mod's
// replace lines (in workspace mode they apply to the whole build): the
// tile chose to build with that module's code, which is in its binary
// either way.
func BuildWork(b Build) Work {
	w := Work{tile: b.Tile, published: map[string]string{}, ambiguous: map[string][]string{}}
	rs := &resolver{b: b, byPath: map[string][]int{}, byDir: map[string]int{}, imports: map[string][]string{}, holds: map[string]bool{}}
	ownDirs := map[string]bool{}
	ownPaths := map[string]bool{}
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
		rs.own = append(rs.own, &node{m, mod})
	}
	for _, m := range b.Others {
		m.Dir = filepath.Clean(m.Dir)
		if ownDirs[m.Dir] {
			continue
		}
		mod, ok := readGoModIn(m)
		if !ok || mod.Path == "" {
			continue // unreadable, or no module: it serves nothing (and breaks no one)
		}
		w.notePublished(mod)
		if ownPaths[mod.Path] || under(mod.Path, SDKModule) || (b.Root != nil && b.Root.replacesAll(mod.Path)) {
			continue // the tile's own path, the SDK's, the go.work's: never another module's (nor a hint's)
		}
		w.paths = append(w.paths, mod.Path)
		i := len(rs.cands)
		rs.cands = append(rs.cands, &node{m, mod})
		rs.byPath[mod.Path] = append(rs.byPath[mod.Path], i)
		rs.byDir[m.Dir] = i
	}
	banned := map[int]bool{}
	var used map[int]reach
	for {
		used = rs.run(banned)
		drop := rs.check(used)
		if len(drop) == 0 {
			break
		}
		for _, i := range drop {
			banned[i] = true // each round bans another used module: it ends
		}
	}
	w.ambiguous = rs.amb
	for _, n := range rs.own {
		w.Uses = append(w.Uses, n.m)
	}
	var more []Module
	goLine := buildGo
	if b.Root != nil && goVersionLess(goLine, b.Root.Go) {
		goLine = b.Root.Go
	}
	for _, n := range rs.active {
		if goVersionLess(goLine, n.mod.Go) {
			goLine = n.mod.Go
		}
	}
	for i := range used {
		more = append(more, rs.cands[i].m)
	}
	sort.Slice(more, func(i, j int) bool { return more[i].Dir < more[j].Dir })
	w.Uses = append(w.Uses, more...)
	w.GoWork = renderBuildWork(w.Uses, goLine, b.SDK, b.Root)
	return w
}

// resolver finds the workspace modules one build uses (BuildWork).
type resolver struct {
	b      Build
	own    []*node
	cands  []*node          // every other workspace module that may serve a reference
	byPath map[string][]int // module path → cands
	byDir  map[string]int   // directory → cand
	// caches across rounds
	imports map[string][]string // module dir → its code's imports
	holds   map[string]bool     // module dir NUL import → holds its package
	// one round's
	active []*node         // the modules used so far: own, then chosen, in order
	claims map[string]bool // paths a used go.mod requires or replaces, the SDK's, the go.work's replaces
	amb    map[string][]string
}

// run resolves the modules the build uses, banned aside: every used
// module's require and replace lines first, then its imports — so an import
// is looked up only once every go.mod line known by then has claimed its
// paths.
func (rs *resolver) run(banned map[int]bool) map[int]reach {
	used := map[int]reach{}
	rs.claims = map[string]bool{SDKModule: true}
	if rs.b.Root != nil {
		for _, r := range rs.b.Root.Replaces {
			rs.claims[r.Old] = true
		}
	}
	rs.amb = map[string][]string{}
	rs.active = append([]*node(nil), rs.own...)
	lines := append([]*node(nil), rs.own...)
	var code []*node
	add := func(i int, how reach) {
		if banned[i] {
			return
		}
		prev, seen := used[i]
		used[i] = prev | how
		if !seen {
			rs.active = append(rs.active, rs.cands[i])
			lines = append(lines, rs.cands[i])
		}
	}
	for len(lines) > 0 || len(code) > 0 {
		if len(lines) > 0 {
			n := lines[0]
			lines = lines[1:]
			rs.modLines(n, add)
			code = append(code, n)
			continue
		}
		n := code[0]
		code = code[1:]
		for _, imp := range rs.importsOf(n.m) {
			rs.importRef(n, imp, used, banned, add)
		}
	}
	return used
}

// modLines claims the paths n's go.mod requires and replaces, and serves
// its references to workspace modules.
func (rs *resolver) modLines(n *node, add func(int, reach)) {
	reqs := make([]string, 0, len(n.mod.Requires))
	for p := range n.mod.Requires {
		reqs = append(reqs, p)
		rs.claims[p] = true
	}
	sort.Strings(reqs)
	for _, r := range n.mod.Replaces {
		rs.claims[r.Old] = true
	}
	for _, p := range reqs {
		if v := n.mod.Requires[p]; !n.mod.replaced(p, v) {
			rs.pathRef(n, p, v, add)
		} // else its replace decides, below
	}
	for _, r := range n.mod.Replaces {
		if !r.dir() {
			rs.pathRef(n, r.New, r.NewVersion, add)
			continue
		}
		d := filepath.FromSlash(r.New)
		if !filepath.IsAbs(d) {
			d = filepath.Join(n.m.Dir, d)
		}
		if i, ok := rs.byDir[filepath.Clean(d)]; ok {
			add(i, reachChosen) // the go.mod names that very directory
		}
	}
}

// pathRef serves module n's reference to module path p at version v (a
// require without a replace, or a replace's new module path).
func (rs *resolver) pathRef(n *node, p, v string, add func(int, reach)) {
	var chosen, open []int
	for _, i := range rs.byPath[p] {
		to := rs.cands[i]
		switch {
		case to.m.Trusted || rs.named(n, to):
			chosen = append(chosen, i)
		case (!dottedPath(p) || placeholder(v)) && rs.nestedIfOwn(p, to):
			open = append(open, i)
		}
	}
	if len(chosen) > 0 {
		open = nil // the manifest's (or the admins') choice among namesakes
	}
	for _, i := range chosen {
		add(i, reachChosen)
	}
	for _, i := range open {
		add(i, reachPath) // namesakes both: "module … appears multiple times in workspace"
	}
}

// importRef serves module n's import imp when nothing the build uses
// covers it: by the one workspace module that holds its package.
func (rs *resolver) importRef(n *node, imp string, used map[int]reach, banned map[int]bool, add func(int, reach)) {
	if imp == "C" || !importPathOK(imp) || (rs.b.Std != nil && rs.b.Std(imp)) {
		return
	}
	for p := range rs.claims {
		if under(imp, p) {
			return // a go.mod line of the build decides where it comes from
		}
	}
	for _, u := range rs.active {
		if under(imp, u.mod.Path) && rs.holdsPackage(u, imp) {
			return
		}
	}
	var named, open []int
	for p := imp; p != "." && p != "/"; p = path.Dir(p) {
		for _, i := range rs.byPath[p] {
			if _, ok := used[i]; ok || banned[i] {
				continue
			}
			to := rs.cands[i]
			if !rs.holdsPackage(to, imp) {
				continue
			}
			switch {
			case to.m.Trusted || rs.named(n, to):
				named = append(named, i)
			case (!dottedPath(p) || rs.insideOwn(to)) && rs.nestedIfOwn(p, to):
				open = append(open, i)
			}
		}
	}
	switch {
	case len(named) == 1:
		add(named[0], reachChosen)
	case len(named) == 0 && len(open) == 1:
		add(open[0], reachImport)
	case len(named)+len(open) > 1:
		var paths []string
		for _, i := range append(named, open...) {
			paths = append(paths, rs.cands[i].mod.Path)
		}
		sort.Strings(paths)
		rs.amb[imp] = slices.Compact(paths)
	}
}

// check lists the used modules the last check drops: one served only by
// path or import whose path lies strictly under a dotted path a used
// module requires, unreplaced, at a published version (that module's code
// comes from elsewhere, and a namesake of its package must not stand in
// for it), and one served only by an import whose path a used go.mod
// requires or replaces, or lies under one (the lines claimed it after the
// import was looked up).
func (rs *resolver) check(used map[int]reach) []int {
	published := map[string]bool{}
	for _, n := range rs.active {
		for p, v := range n.mod.Requires {
			if dottedPath(p) && !placeholder(v) && !n.mod.replaced(p, v) {
				published[p] = true
			}
		}
	}
	var drop []int
	for i, how := range used {
		if how&reachChosen != 0 {
			continue
		}
		p := rs.cands[i].mod.Path
		bad := false
		for q := range published {
			bad = bad || strings.HasPrefix(p, q+"/")
		}
		if how == reachImport {
			for q := range rs.claims {
				bad = bad || under(p, q)
			}
		}
		if bad {
			drop = append(drop, i)
		}
	}
	sort.Ints(drop)
	return drop
}

// named reports whether n's tile names to's in its manifest's deps.
func (rs *resolver) named(n, to *node) bool {
	return rs.b.Deps != nil && n.m.Tile != "" && to.m.Tile != "" && n.m.Tile != to.m.Tile && rs.b.Deps(n.m.Tile, to.m.Tile)
}

// nestedIfOwn reports whether workspace module to may serve path p as far
// as the tile's own module paths go: a path strictly under one is served
// only by a module inside that own module's directory — a nested module,
// the only one the go command would give that path from the own module's
// tree — never by a namesake elsewhere.
func (rs *resolver) nestedIfOwn(p string, to *node) bool {
	for _, o := range rs.own {
		if o.mod.Path != "" && strings.HasPrefix(p, o.mod.Path+"/") && !inside(to.m.Dir, o.m.Dir) {
			return false
		}
	}
	return true
}

// insideOwn reports whether workspace module to lies inside one of the
// tile's own modules' directories — a component nested in the tile's own
// tree, which may serve a dotted import as a dotless one would.
func (rs *resolver) insideOwn(to *node) bool {
	for _, o := range rs.own {
		if to.m.Dir != o.m.Dir && inside(to.m.Dir, o.m.Dir) {
			return true
		}
	}
	return false
}

func (rs *resolver) importsOf(m Module) []string {
	if imps, ok := rs.imports[m.Dir]; ok {
		return imps
	}
	imps := scanImports(m)
	rs.imports[m.Dir] = imps
	return imps
}

// holdsPackage reports whether n's module holds imp's package, as the go
// command finds one (dirInModule): imp is its path or beneath it, the
// directory holds no go.mod on the way below the module's own (that is a
// nested module's), and it holds a .go file.
func (rs *resolver) holdsPackage(n *node, imp string) bool {
	mp := n.mod.Path
	rel := ""
	switch {
	case mp == "":
		return false
	case imp == mp:
	case strings.HasPrefix(imp, mp+"/"):
		rel = imp[len(mp)+1:]
	default:
		return false
	}
	key := n.m.Dir + "\x00" + imp
	if v, ok := rs.holds[key]; ok {
		return v
	}
	v := holdsPackage(n.m, rel)
	rs.holds[key] = v
	return v
}

// holdsPackage reports whether module m holds a package at rel ("" = its
// root), read beneath the module's directory: no go.mod on the way, and a
// .go file there. Nothing but directories is opened, never blocking.
func holdsPackage(m Module, rel string) bool {
	if rel != "" && !fs.ValidPath(rel) {
		return false
	}
	r, err := fsutil.OpenRootIn(m.Root, filepath.FromSlash(m.Rel))
	if err != nil {
		return false
	}
	defer r.Close()
	dir := "."
	if rel != "" {
		dir = rel
		for d := rel; d != "."; d = path.Dir(d) {
			if fi, err := r.Lstat(d + "/go.mod"); err == nil && !fi.IsDir() {
				return false
			}
		}
	}
	f, err := r.OpenFile(dir, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	for {
		ents, err := f.ReadDir(256)
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// notePublished records the dotted paths mod requires at a published
// version, for Hint.
func (w *Work) notePublished(mod goMod) {
	for p, v := range mod.Requires {
		if dottedPath(p) && !placeholder(v) {
			if cur, ok := w.published[p]; !ok || semverLess(cur, v) {
				w.published[p] = v
			}
		}
	}
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

// under reports whether import or module path p is q or beneath it.
func under(p, q string) bool {
	return q != "" && (p == q || strings.HasPrefix(p, q+"/"))
}

// inside reports whether dir is parent or beneath it.
func inside(dir, parent string) bool {
	rel, err := filepath.Rel(parent, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// importPathOK reports whether an import path is one a workspace module
// could serve: a clean relative slash path.
func importPathOK(p string) bool {
	return p != "" && fs.ValidPath(p)
}

// goVersion matches a go line's version: 1.24, 1.24.0, 1.25rc1.
var goVersion = regexp.MustCompile(`^([0-9]+)\.([0-9]+)(?:\.([0-9]+)|(beta|rc)([0-9]+))?$`)

// goVersionLess orders go lines as the go command does (gover): a language
// version below its pre-releases below its releases — 1.24 < 1.24rc1 <
// 1.24.0 < 1.24.1. A version that isn't one is never more than another.
func goVersionLess(a, b string) bool {
	va, oka := parseGoVersion(a)
	vb, okb := parseGoVersion(b)
	if !okb {
		return false
	}
	if !oka {
		return true
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}

// parseGoVersion is a go line's version as major, minor, kind (0 language,
// 1 beta, 2 rc, 3 release), then the pre-release or patch number.
func parseGoVersion(s string) ([5]int, bool) {
	m := goVersion.FindStringSubmatch(s)
	if m == nil {
		return [5]int{}, false
	}
	var v [5]int
	v[0], _ = strconv.Atoi(m[1])
	v[1], _ = strconv.Atoi(m[2])
	switch {
	case m[3] != "":
		v[2] = 3
		v[3], _ = strconv.Atoi(m[3])
	case m[4] == "beta":
		v[2] = 1
		v[3], _ = strconv.Atoi(m[5])
	case m[4] == "rc":
		v[2] = 2
		v[3], _ = strconv.Atoi(m[5])
	}
	return v, true
}

func renderBuildWork(uses []Module, goLine, sdk string, rw *RootWork) []byte {
	var sb strings.Builder
	sb.WriteString("// Code generated by xbind: this tile build's own workspace (D166), made from\n")
	sb.WriteString("// the tile's go.mod at each build. The workspace's go.work is not read.\n\n")
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

// The go command's errors for an import no module the build uses provides:
// a dotted path's, and a dotless one's (taken for a standard-library path).
var (
	noProvider = regexp.MustCompile(`no required module provides package ([^\s;:]+)`)
	notInStd   = regexp.MustCompile(`package ([^\s;:]+) is not in std`)
)

// Hint explains a failed build's errors for a package no module the build
// uses provides, as far as the build's own workspace causes them, for the
// build's output: several workspace modules could provide it, it is in a
// workspace module the tile's go.mod doesn't require, or a workspace go.mod
// requires its module at a published version where this tile's doesn't (a
// build with the shared go.work picked that up). It names module paths and
// versions only, never another tile. "" when there is nothing to say.
func (w Work) Hint(output string) string {
	var notes []string
	seen := map[string]bool{}
	for _, re := range []*regexp.Regexp{noProvider, notInStd} {
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			if pkg := m[1]; !seen[pkg] {
				seen[pkg] = true
				if note := w.hintFor(pkg); note != "" {
					notes = append(notes, note)
				}
			}
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "xbind: " + strings.Join(notes, "\nxbind: ") + "\n(/docs/changes/2026-09-30-go-build-workspace.md)"
}

// A bare `require M v0.0.0` of a workspace module isn't advised: the go
// command looks M@v0.0.0 up once a build loads the whole module graph (any
// import from a module outside the workspace), and fails; a replace with
// the module's directory, or deps, serves it either way.
func (w Work) hintFor(pkg string) string {
	choose := func(m string) string {
		if m == "" {
			m = "<it>"
		}
		return fmt.Sprintf("name its tile in %s's xbin.json deps, or add `require %s v0.0.0` and `replace %s => <its directory>` to %s's go.mod", w.tile, m, m, w.tile)
	}
	if paths := w.ambiguous[pkg]; len(paths) > 0 {
		return fmt.Sprintf("more than one of the workspace's Go modules could provide %s (%s), so the build uses none — for the one you mean, %s", pkg, strings.Join(paths, ", "), choose(""))
	}
	ws := ""
	for _, p := range w.paths {
		if under(pkg, p) && len(p) > len(ws) {
			ws = p
		}
	}
	pub, ver := "", ""
	for p, v := range w.published {
		if under(pkg, p) && len(p) > len(pub) {
			pub, ver = p, v
		}
	}
	switch {
	case ws != "" && !dottedPath(ws):
		return fmt.Sprintf("%s is in the workspace's Go module %s, which this build doesn't use: %s", pkg, ws, choose(ws))
	case pub != "":
		// the published module, and only it: a tile declaring that dotted
		// path is no reason to build with its code
		return fmt.Sprintf("each Go tile builds against its own go.mod now (D166): add `require %s %s` to %s's go.mod", pub, ver, w.tile)
	case ws != "":
		return fmt.Sprintf("no module this build uses provides %s. If you mean the workspace's Go module %s (another tile's module with a dotted path is used only when your manifest or go.mod chooses it): %s", pkg, ws, choose(ws))
	}
	return ""
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

// HasGoMod reports whether the directory at rel beneath root holds a
// go.mod, a regular file reached without leaving root.
func HasGoMod(root, rel string) bool {
	return regularBeneath(root, path.Join(rel, "go.mod"))
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
