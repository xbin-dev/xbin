package deps

// sharedwork.go — what the upgrade check of D166 (runner/goversions.go)
// needs of go.mod and go.work syntax: the workspace's shared go.work as
// every Go build used it before D166, a go.mod's direct requirements, and
// the module that pins extra requirements into a build workspace.

import (
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
// D166: the root go.work — xbind's (every component's module, the SDK
// replaced as GoWork renders it) or a hand-managed one's own lines — with
// absolute paths, so a build workspace elsewhere can stand in for it, as
// the confined builds' copy did (99fa52da). The go.work on disk is not
// read for xbind's own: it is rendered from the registry, as GoWork writes
// it.
func SharedWork(reg *registry.Registry, sdkPath string) ([]byte, error) {
	rw, err := ReadRootWork(reg.Root)
	if err != nil {
		return nil, err
	}
	if rw != nil { // hand-managed: its own lines, as the go command read them
		var sb strings.Builder
		sb.WriteString("// the workspace's hand-managed go.work, paths made absolute\n\n")
		if rw.Go != "" {
			fmt.Fprintf(&sb, "go %s\n", modToken(rw.Go))
		}
		if rw.Toolchain != "" {
			fmt.Fprintf(&sb, "\ntoolchain %s\n", modToken(rw.Toolchain))
		}
		for _, g := range rw.Godebug {
			fmt.Fprintf(&sb, "\ngodebug %s\n", modToken(g))
		}
		sb.WriteString("\nuse (\n")
		for _, u := range rw.Uses {
			fmt.Fprintf(&sb, "\t%s\n", modToken(u))
		}
		sb.WriteString(")\n")
		for _, r := range rw.Replaces {
			sb.WriteString("\n" + r.render(r.New) + "\n")
		}
		return []byte(sb.String()), nil
	}
	mods := goModules(reg, nil)
	if len(mods) == 0 {
		return nil, ErrNoSharedWork
	}
	var sb strings.Builder
	sb.WriteString(workMarker + "\n\ngo 1.24\n\nuse (\n")
	for _, m := range mods {
		fmt.Fprintf(&sb, "\t%s\n", modToken(filepath.Join(reg.Root, filepath.FromSlash(m))))
	}
	sb.WriteString(")\n")
	if sdkPath != "" {
		if !filepath.IsAbs(sdkPath) { // relative to the go.work's directory, as the go command reads it
			sdkPath = filepath.Join(reg.Root, sdkPath)
		}
		fmt.Fprintf(&sb, "\nreplace %s => %s\n", SDKModule, modToken(filepath.Clean(sdkPath)))
	}
	return []byte(sb.String()), nil
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
