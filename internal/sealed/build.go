package sealed

// build.go — the release's own builds of its sealed components' backends
// (`xbind sealed-build`, run by `make build`). A production xbind runs
// bin/sealed/<name>-<digest> and never compiles one: no Go toolchain, no
// module download and no build sandbox with network on the host. The file
// name carries the digest of the rendered tree, computed by the code that
// will look for it, so a build of other source is never mistaken for it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// ManifestName is the file in bin/sealed that lists the release's builds:
// what each file was built from and its hash. Build writes it; xbind runs
// only a binary it lists, with the hash it gives, checked at boot and again
// before every start (Set.verify). The manifest sits beside the binaries:
// it catches a build of other source or another SDK, a damaged or replaced
// file and a half-finished install — not someone who can write the install
// prefix, who can replace xbind itself.
const ManifestName = "sealed.json"

// BuildManifest is ManifestName's content.
type BuildManifest struct {
	Schema int                   `json:"schema"`
	Builds map[string]BuildEntry `json:"builds"` // file name (BuildName) → what it is
}

// BuildEntry is one build of a sealed component's backend.
type BuildEntry struct {
	Component string `json:"component"` // its path
	Source    string `json:"source"`    // the digest of the source tree it was built from (Component.Digest)
	SDK       string `json:"sdk"`       // the digest of the xbin SDK tree it was built against (TreeDigest)
	Go        string `json:"go"`        // the go version that built it
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"` // of the file
	Size      int64  `json:"size"`
}

// ReadManifest reads dir's build manifest; a zero one when there is none.
func ReadManifest(dir string) (BuildManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return BuildManifest{}, nil
	}
	if err != nil {
		return BuildManifest{}, err
	}
	var m BuildManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return BuildManifest{}, fmt.Errorf("%s: %w", filepath.Join(dir, ManifestName), err)
	}
	return m, nil
}

// FileSum is the hex SHA-256 of the file at p and its size.
func FileSum(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// TreeDigest hashes the regular files under dir — each one's slash path
// and bytes, in path order, version-control directories left out: what a
// build against the SDK at dir was built against.
func TreeDigest(dir string) (string, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return "", err
	}
	return digest(files), nil
}

// CheckBuilds verifies that dir holds exactly this set's builds, each as
// its manifest says — the file's hash, the source it was built from — and
// built against the SDK at sdk (when given): what a release bundle must
// hold. It answers how many backends that is.
func (s *Set) CheckBuilds(dir, sdk string) (int, error) {
	man, err := ReadManifest(dir)
	if err != nil {
		return 0, err
	}
	want := s.BuildNames()
	ents, err := os.ReadDir(dir)
	if err != nil && (len(want) > 0 || !errors.Is(err, fs.ErrNotExist)) {
		return 0, err
	}
	have := map[string]bool{}
	for _, e := range ents {
		have[e.Name()] = true
	}
	sdkSum := ""
	if sdk != "" && len(want) > 0 {
		if sdkSum, err = TreeDigest(sdk); err != nil {
			return 0, err
		}
	}
	for _, c := range s.comps {
		if c.Runtime != "go" {
			continue
		}
		n := BuildName(c)
		if !have[n] {
			return 0, fmt.Errorf("%s has no %s: the release would hold that component (no-build)", dir, n)
		}
		if why := man.mismatch(c, filepath.Join(dir, n)); why != "" {
			return 0, fmt.Errorf("%s: %s %s", dir, n, why)
		}
		if e := man.Builds[n]; sdkSum != "" && e.SDK != sdkSum {
			return 0, fmt.Errorf("%s: %s was built against another xbin SDK than the one at %s: rebuild it (make sealed)", dir, n, sdk)
		}
		delete(have, n)
	}
	if len(want) > 0 {
		delete(have, ManifestName)
	}
	for n := range have {
		return 0, fmt.Errorf("%s holds %s, which isn't a build of this source", dir, n)
	}
	return len(want), nil
}

// mismatch says why the file at p isn't c's build as m lists it ("" when it
// is): not listed, built from other source, or not the bytes the manifest
// names.
func (m BuildManifest) mismatch(c *Component, p string) string {
	e, ok := m.Builds[filepath.Base(p)]
	switch {
	case !ok:
		return "isn't listed in " + ManifestName
	case e.Source != c.Digest || e.Component != c.Path:
		return "is listed in " + ManifestName + " as a build of other source"
	}
	sum, size, err := FileSum(p)
	switch {
	case err != nil:
		return "can't be read: " + err.Error()
	case sum != e.SHA256 || size != e.Size:
		return "isn't the file " + ManifestName + " lists (its SHA-256 differs)"
	}
	return ""
}

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
	man := BuildManifest{Schema: 1, Builds: map[string]BuildEntry{}}
	sdkSum, goVersion, target := "", "", [2]string{o.GOOS, o.GOARCH}
	want := map[string]bool{}
	for _, c := range s.comps {
		if c.Runtime != "go" {
			continue
		}
		if sdkSum == "" { // once, and only when something is built
			if sdkSum, err = TreeDigest(sdk); err != nil {
				return nil, err
			}
			if goVersion, target, err = goTarget(o); err != nil {
				return nil, err
			}
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
		sum, size, err := FileSum(out)
		if err != nil {
			return nil, err
		}
		man.Builds[name] = BuildEntry{Component: c.Path, Source: c.Digest, SDK: sdkSum, Go: goVersion, OS: target[0], Arch: target[1], SHA256: sum, Size: size}
	}
	if len(man.Builds) > 0 { // a table without Go backends ships an empty directory
		want[ManifestName] = true
		mb, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(o.Out, ManifestName+".tmp"), append(mb, '\n'), 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(filepath.Join(o.Out, ManifestName+".tmp"), filepath.Join(o.Out, ManifestName)); err != nil {
			return nil, err
		}
	}
	ents, err := os.ReadDir(o.Out)
	if err != nil {
		return nil, err
	}
	var have []string
	for _, e := range ents {
		if want[e.Name()] {
			if e.Name() != ManifestName {
				have = append(have, e.Name())
			}
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

// goTarget asks the go command its version and the OS and architecture a
// build with o's settings is for.
func goTarget(o BuildOptions) (version string, target [2]string, err error) {
	gobin := o.Go
	if gobin == "" {
		gobin = "go"
	}
	cmd := exec.Command(gobin, "env", "GOVERSION", "GOOS", "GOARCH") // exec-ok: the release's build tool asking its own toolchain, never a daemon path
	cmd.Env = os.Environ()
	if o.GOOS != "" {
		cmd.Env = append(cmd.Env, "GOOS="+o.GOOS)
	}
	if o.GOARCH != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+o.GOARCH)
	}
	out, err := cmd.Output()
	lines := strings.Fields(string(out))
	if err != nil || len(lines) != 3 {
		return "", target, fmt.Errorf("go env: %v (%q)", err, out)
	}
	return lines[0], [2]string{lines[1], lines[2]}, nil
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
