package deps

// sharedwork.go — what the upgrade check of D166 (runner/goversions.go)
// needs of go.mod and go.work syntax: the workspace's shared go.work as
// every Go build used it before D166, a go.mod's direct requirements, the
// module that pins extra requirements into a build workspace, and a digest
// of what decides a build workspace's versions.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
)

// ErrNoSharedWork: the workspace has no Go module for a shared go.work to
// use.
var ErrNoSharedWork = errors.New("the workspace has no Go module")

// SharedWork is the go.work every Go build of the workspace ran with before
// D166, for the build of one tile: the root go.work — xbind's (every
// component's module, the SDK replaced as GoWork renders it) or a
// hand-managed one's own lines — with absolute paths, so a build workspace
// elsewhere can stand in for it, as the confined builds' copy did
// (99fa52da). The go.work on disk is not read for xbind's own: it is
// rendered from the registry, as GoWork writes it.
//
// Two shapes D166 made buildable, which a go.work using every module
// can't hold, are rendered as the go command takes them:
//
//   - The go line is the highest of 1.24 (the root file's), a hand-managed
//     go.work's and every used module's, as BuildWork's: the go command
//     refuses a go.work older than a module it uses, and `go mod init`
//     writes `go 1.24.0`. A go.work's go line doesn't change the versions
//     MVS selects (each module's own go line decides its graph's pruning).
//   - Of several modules declaring one path (a copied tile: "appears
//     multiple times in workspace"), one is used: the one prefer reports
//     (the tile's own, or the one its build workspace chose), else the
//     first (a copy's go.mod is the original's: either brings the same
//     requirements into the graph). Nor is a module whose path the go.work
//     replaces at every version.
//
// prefer is nil, or reports the directories the tile's build uses.
func SharedWork(reg *registry.Registry, sdkPath string, prefer func(dir string) bool) ([]byte, error) {
	rw, err := ReadRootWork(reg.Root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	if rw != nil { // hand-managed: its own lines, as the go command read them
		dirs = rw.Uses
	} else {
		mods, _ := goModules(reg, nil) // the go line is SharedWork's own (below)
		for _, m := range mods {
			dirs = append(dirs, filepath.Join(reg.Root, filepath.FromSlash(m)))
		}
		if len(dirs) == 0 {
			return nil, ErrNoSharedWork
		}
		if sdkPath != "" && !filepath.IsAbs(sdkPath) { // relative to the go.work's directory, as the go command reads it
			sdkPath = filepath.Join(reg.Root, sdkPath)
		}
	}
	replacedAll := func(p string) bool {
		if rw != nil {
			return rw.replacesAll(p)
		}
		return sdkPath != "" && p == SDKModule
	}
	type used struct {
		dir string
		mod goMod
	}
	var mods []used
	byPath := map[string][]int{}
	for _, d := range dirs {
		mod, _ := readGoModIn(moduleAt(reg.Root, d)) // unreadable: used all the same, as the go command found it
		if mod.Path != "" {
			if replacedAll(mod.Path) {
				continue
			}
			byPath[mod.Path] = append(byPath[mod.Path], len(mods))
		}
		mods = append(mods, used{d, mod})
	}
	drop := map[int]bool{}
	for _, is := range byPath {
		if len(is) < 2 {
			continue
		}
		keep := is[0]
		for _, i := range is {
			if prefer != nil && prefer(filepath.Clean(mods[i].dir)) {
				keep = i
				break
			}
		}
		for _, i := range is {
			drop[i] = i != keep
		}
	}
	goLine := buildGo
	if rw != nil && goVersionLess(goLine, rw.Go) {
		goLine = rw.Go
	}
	var uses []string
	for i, u := range mods {
		if drop[i] {
			continue
		}
		if goVersionLess(goLine, u.mod.Go) {
			goLine = u.mod.Go
		}
		uses = append(uses, u.dir)
	}
	var sb strings.Builder
	if rw != nil {
		sb.WriteString("// the workspace's hand-managed go.work, paths made absolute\n\n")
	} else {
		sb.WriteString(workMarker + "\n\n")
	}
	fmt.Fprintf(&sb, "go %s\n", modToken(goLine))
	if rw != nil && rw.Toolchain != "" {
		fmt.Fprintf(&sb, "\ntoolchain %s\n", modToken(rw.Toolchain))
	}
	if rw != nil {
		for _, g := range rw.Godebug {
			fmt.Fprintf(&sb, "\ngodebug %s\n", modToken(g))
		}
	}
	sb.WriteString("\nuse (\n")
	for _, u := range uses {
		fmt.Fprintf(&sb, "\t%s\n", modToken(u))
	}
	sb.WriteString(")\n")
	if rw != nil {
		for _, r := range rw.Replaces {
			sb.WriteString("\n" + r.render(r.New) + "\n")
		}
	} else if sdkPath != "" {
		fmt.Fprintf(&sb, "\nreplace %s => %s\n", SDKModule, modToken(filepath.Clean(sdkPath)))
	}
	return []byte(sb.String()), nil
}

// moduleAt is the module at dir, read beneath root when it is there (never
// through a symlink on the way), else beneath dir itself.
func moduleAt(root, dir string) Module {
	if inside(dir, root) {
		rel, _ := filepath.Rel(root, dir)
		if rel == "." {
			rel = ""
		}
		return Module{Dir: dir, Root: root, Rel: filepath.ToSlash(rel)}
	}
	return Module{Dir: dir, Root: dir}
}

// Inputs is a digest of what decides the versions w's build selects: its
// go.work and the go.mod of every module it uses (MVS reads nothing else of
// the workspace's, and a published module's go.mod never changes). Builds
// with the same inputs select the same versions.
func (w Work) Inputs() string {
	h := sha256.New()
	frame := func(b []byte) {
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	frame(w.GoWork)
	for _, m := range w.Uses {
		frame([]byte(m.Dir))
		var b []byte
		if f, err := fsutil.OpenBeneath(m.Root, filepath.FromSlash(path.Join(m.Rel, "go.mod"))); err == nil {
			b, _ = readCapped(f, modFileMax)
			f.Close()
		}
		frame(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DirectRequires is the module paths m's go.mod requires on a line not
// marked `// indirect`, the tile's own choices. nil when it can't be read.
func DirectRequires(m Module) map[string]bool {
	f, err := fsutil.OpenBeneath(m.Root, filepath.FromSlash(path.Join(m.Rel, "go.mod")))
	if err != nil {
		return nil
	}
	b, ok := readCapped(f, modFileMax)
	f.Close()
	if !ok {
		return nil
	}
	out := map[string]bool{}
	block := false
	for _, raw := range strings.Split(string(b), "\n") {
		code, comment, _ := strings.Cut(raw, "//")
		toks := modTokens(code)
		indirect := strings.HasPrefix(strings.TrimSpace(comment), "indirect")
		switch {
		case block && len(toks) > 0 && toks[0] == ")":
			block = false
		case block && len(toks) >= 2:
			if !indirect {
				out[toks[0]] = true
			}
		case len(toks) >= 2 && toks[0] == "require" && toks[1] == "(":
			block = true
		case len(toks) >= 3 && toks[0] == "require":
			if !indirect {
				out[toks[1]] = true
			}
		}
	}
	return out
}

// Pin is one requirement a build workspace's pin module adds.
type Pin struct{ Path, Version string }

// PinModulePath is the module path of the module that pins requirements
// into a build workspace: a reserved domain no tile can be served by.
const PinModulePath = "xbind-go-versions.invalid/pin"

// PinGoMod is the go.mod of a module requiring pins, which a build
// workspace that uses it builds as if the tile's go.mod required them: in
// workspace mode every used module's requirements are roots of the one
// module graph. goLine is the go.work's (a used module's go line may not
// be higher).
func PinGoMod(goLine string, pins []Pin) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "module %s\n\ngo %s\n\nrequire (\n", PinModulePath, modToken(goLine))
	for _, p := range pins {
		fmt.Fprintf(&sb, "\t%s %s\n", modToken(p.Path), modToken(p.Version))
	}
	sb.WriteString(")\n")
	return []byte(sb.String())
}

// WithUse is gowork using one more module, at dir.
func WithUse(gowork []byte, dir string) []byte {
	out := append([]byte(nil), gowork...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, "\nuse "+modToken(dir)+"\n"...)
}

// WorkGoLine is a go.work's go line, "" when it has none.
func WorkGoLine(gowork []byte) string {
	for _, l := range parseModLines(gowork) {
		if l.verb == "go" && len(l.args) == 1 {
			return l.args[0]
		}
	}
	return ""
}
