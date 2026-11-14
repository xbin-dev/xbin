package sealed

// build.go — the release's own builds of its sealed components' backends
// (`xbind sealed-build`, run by `make build`). A production xbind runs
// bin/sealed/<name>-<digest> and never compiles one: no Go toolchain, no
// module download and no build sandbox with network on the host. The file
// name carries the digest of the rendered tree, computed by the code that
// will look for it, so a build of other source is never mistaken for it.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// BuildOptions says where and for what the backends are built.
type BuildOptions struct {
	Out string // the directory the binaries land in (bin/sealed)
	SDK string // the xbin Go SDK's directory, which every build resolves the sdk module to
	// GOOS and GOARCH are the target ("" = the go command's default).
	GOOS, GOARCH string
	Go           string    // the go command ("" = go on PATH)
	Log          io.Writer // progress lines (nil = none)
}

// BuildName is the file name of c's prebuilt backend.
func BuildName(c *Component) string { return c.Name + "-" + c.ID() }

// BuildNames are the files a release's bin/sealed must hold for the set:
// one per component with a Go backend.
func (s *Set) BuildNames() []string {
	var out []string
	for _, c := range s.comps {
		if c.Runtime == "go" {
			out = append(out, BuildName(c))
		}
	}
	sort.Strings(out)
	return out
}

// Build compiles the backend of every Go component of the set into o.Out,
// as static binaries named BuildName, and removes what else is there (an
// earlier source's builds): afterwards the directory holds exactly this
// table's. It answers the files it holds. Each is rebuilt every time (the
// go command's cache makes a repeat cheap): the name says which source a
// file was built from, not which OS and architecture it was built for.
func (s *Set) Build(o BuildOptions) ([]string, error) {
	logf := func(f string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, f+"\n", a...)
		}
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	sdk, err := filepath.Abs(o.SDK)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(sdk, "go.mod")); err != nil {
		return nil, fmt.Errorf("the xbin SDK isn't at %s: %w", sdk, err)
	}
	want := map[string]bool{}
	for _, c := range s.comps {
		if c.Runtime != "go" {
			continue
		}
		name := BuildName(c)
		want[name] = true
		out, err := filepath.Abs(filepath.Join(o.Out, name))
		if err != nil {
			return nil, err
		}
		logf("sealed-build: building %s → %s", c.Path, name) // always: the name says the source, not the target it was built for
		if err := buildOne(c, out, sdk, o); err != nil {
			return nil, fmt.Errorf("sealed component %s: %w", c.Path, err)
		}
	}
	ents, err := os.ReadDir(o.Out)
	if err != nil {
		return nil, err
	}
	var have []string
	for _, e := range ents {
		if want[e.Name()] {
			have = append(have, e.Name())
			continue
		}
		logf("sealed-build: removing %s (not this source's)", e.Name())
		if err := os.RemoveAll(filepath.Join(o.Out, e.Name())); err != nil {
			return nil, err
		}
	}
	sort.Strings(have)
	return have, nil
}

var goLineRe = regexp.MustCompile(`(?m)^go\s+([0-9][0-9.]*)`)

// buildOne compiles c's rendered tree in a scratch copy, in a go.work
// shaped as xbind's own build renders one (the module, the SDK replaced).
func buildOne(c *Component, out, sdk string, o BuildOptions) error {
	work, err := os.MkdirTemp("", "xbin-sealed-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	src := filepath.Join(work, "src")
	for rel, b := range c.Files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	var man struct{ Entry string }
	_ = jsonc.Unmarshal(c.Files["xbin.json"], &man)
	entry := man.Entry
	if entry == "" {
		entry = "./backend"
	}
	goLine := "1.24"
	if m := goLineRe.FindSubmatch(c.Files["go.mod"]); m != nil && versionLess(goLine, string(m[1])) {
		goLine = string(m[1])
	}
	gowork := filepath.Join(work, "go.work")
	if err := os.WriteFile(gowork, []byte(fmt.Sprintf("go %s\n\nuse ./src\n\nreplace github.com/xbin-dev/xbin/sdk => %q\n", goLine, sdk)), 0o644); err != nil {
		return err
	}
	gobin := o.Go
	if gobin == "" {
		gobin = "go"
	}
	tmp := out + ".tmp"
	cmd := exec.Command(gobin, "build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w", "-o", tmp, entry) // exec-ok: the release's build tool on xbind's own embedded source, never a daemon path
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK="+gowork, "GOFLAGS=-mod=readonly", "GOTELEMETRY=off")
	if o.GOOS != "" {
		cmd.Env = append(cmd.Env, "GOOS="+o.GOOS)
	}
	if o.GOARCH != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+o.GOARCH)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("go build: %w\n%s", err, strings.TrimSpace(string(b)))
	}
	return os.Rename(tmp, out)
}

// versionLess compares dotted numeric versions ("1.24" < "1.26.0").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			fmt.Sscanf(as[i], "%d", &x)
		}
		if i < len(bs) {
			fmt.Sscanf(bs[i], "%d", &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}
