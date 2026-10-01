// vulncheck is the release's vulnerability gate (`make vulncheck`;
// hack/release.sh runs it before tagging): govulncheck, in source mode, over
// the Go modules a release builds from this checkout, failing on any known
// vulnerability their code reaches that hack/vulncheck-allow.txt doesn't
// list.
//
// The modules it checks ("targets", named as the allow file names them):
//
//   - xbind, xbind/arm64: the programs a release bundle carries (./cmd/...:
//     xbind, bx, xbin-vmagent), with the repo's go.work, as `make build`
//     builds them (CGO_ENABLED=0) for each bundle's platform (linux/amd64,
//     linux/arm64);
//   - relay: the push relay (relay/), deployed on its own;
//   - sdk: the Go SDK every Go tile compiles in;
//   - every Go module in the embedded trees — each builtin tile's and
//     template's backend (builtin-tiles/<name>, builtin-templates/<name>),
//     and any under workspace-template/ — against its own go.mod.tile (or
//     go.mod) and go.sum, read-only, through a go.work like the one xbind
//     builds it with (D166) with the sdk replaced by this checkout: the
//     versions a workspace builds (hack/tile-check.sh does the same).
//
// Not checked: Go programs a bundle carries prebuilt rather than built here
// (bin/gocryptfs, a pinned helper: hack/helpers.sha256) and the tools the
// base rootfs installs (gopls, dlv: docker/rootfs.Dockerfile).
//
// Reachable means govulncheck found a call path from the module's code to a
// vulnerable symbol; a vulnerable module only required, or a package only
// imported, is counted and reported but never gates. Standard-library
// findings are the toolchain's: they gate xbind and the relay (the go running
// this builds them), never the sdk or a tile (a workspace builds those with
// its host's Go), so a run with an outdated go fails once, on xbind. The
// standard library's version is the go's release (releaseVersion): one
// naming no release (a devel build) fails the gate rather than match no
// advisory.
//
//	go run ./hack/vulncheck [-allow FILE] [-govulncheck PKG@VERSION] [target...]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/version"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const sdkModule = "github.com/xbin-dev/xbin/sdk"

// embeddedTrees are the trees every xbind embeds and copies into workspaces
// (assets.go); a Go module in one of them ships with every release.
var embeddedTrees = []string{"builtin-tiles", "builtin-templates", "workspace-template"}

type target struct {
	name     string   // as printed and as the allow file names it
	dir      string   // where govulncheck runs
	patterns []string // its package patterns
	env      []string // added to the environment
	stdlib   bool     // standard-library findings gate it
	src      string   // a shipped tile's directory: dir is a copy made from it
}

// releaseEnv is xbind's programs' build environment for a bundle's arch:
// what `make build` and deploy/publish-release.sh set.
func releaseEnv(arch string) []string {
	return []string{"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0"}
}

func main() {
	allowPath := flag.String("allow", "hack/vulncheck-allow.txt", "the allow file: reachable vulnerabilities a release may ship with, and why")
	tool := flag.String("govulncheck", "golang.org/x/vuln/cmd/govulncheck@v1.8.0", "the govulncheck to build and run (a go install argument)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: go run ./hack/vulncheck [-allow FILE] [-govulncheck PKG@VERSION] [target...]")
		flag.PrintDefaults()
	}
	flag.Parse()
	os.Exit(run(*allowPath, *tool, flag.Args()))
}

func run(allowPath, tool string, only []string) int {
	repo, err := repoRoot()
	if err != nil {
		return fail(err)
	}
	file := allowPath // as given, for the messages
	if !filepath.IsAbs(file) {
		file = filepath.Join(repo, file)
	}
	allow, err := readAllow(file)
	if err != nil {
		return fail(err)
	}
	targets, err := discover(repo)
	if err != nil {
		return fail(err)
	}
	if targets, err = selectTargets(targets, only); err != nil {
		return fail(err)
	}
	scratch, err := os.MkdirTemp("", "xbin-vulncheck-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(scratch)

	bin := filepath.Join(scratch, "bin")
	if out, err := goCmd(repo, []string{"GOBIN=" + bin, "GOWORK=off"}, "install", tool).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("go install %s: %v\n%s", tool, err, out))
	}
	vc := filepath.Join(bin, "govulncheck")

	var failed, reachable int
	header := false
	for _, t := range targets {
		if t.src != "" {
			if err := copyTile(t, repo, filepath.Join(scratch, "m", t.name)); err != nil {
				return fail(fmt.Errorf("%s: %w", t.name, err))
			}
			t.dir = filepath.Join(scratch, "m", t.name)
			t.env = append(t.env[:len(t.env):len(t.env)], "GOWORK="+filepath.Join(t.dir, "go.work"))
			if t.patterns, err = packageDirs(t.dir); err != nil {
				return fail(fmt.Errorf("%s: %w", t.name, err))
			}
			if len(t.patterns) == 0 {
				continue // a module with no Go code: nothing to reach
			}
		}
		// the standard library govulncheck judges is the go's release:
		// passed explicitly, since it reads only a bare one and finds no
		// standard-library advisory at all for "go1.27.0-X:nodwarf5" (a
		// GOEXPERIMENT baked in) or a devel build
		gover, err := goVersion(t)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", t.name, err))
		}
		env := t.env
		if rel, ok := releaseVersion(gover); ok {
			env = append(env[:len(env):len(env)], "GOVERSION="+rel)
		} else if t.stdlib {
			return fail(fmt.Errorf("%s: the go running this is %q, which names no Go release: govulncheck can't match its standard library against any advisory and would report none — run the gate with a released go, the one that builds the release", t.name, gover))
		}
		cmd := exec.Command(vc, append([]string{"-format", "json"}, t.patterns...)...)
		cmd.Dir, cmd.Env = t.dir, append(os.Environ(), env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			hint := ""
			if t.src != "" {
				hint = fmt.Sprintf("\n(checked read-only against its go.mod.tile and go.sum, as xbind builds it: a go.mod or go.sum error means they don't match its code — tidy them: hack/tile-check.sh %s says how)", path.Base(t.name))
			}
			return fail(fmt.Errorf("govulncheck %s (%s): %v\n%s%s", t.name, strings.Join(t.patterns, " "), err, stderr.String(), hint))
		}
		r, err := parse(bytes.NewReader(out), t.stdlib)
		if err != nil {
			return fail(fmt.Errorf("govulncheck %s: %w", t.name, err))
		}
		if !header {
			header = true
			goNote := r.goVersion
			if gover != r.goVersion {
				goNote += " (the go running this: " + gover + ")"
			}
			fmt.Printf("vulncheck: govulncheck %s, the standard library of %s, vulnerability database of %s\n", r.scanner, goNote, r.dbDate())
		}
		gating := report(os.Stdout, t, r, allow, allowPath)
		if gating > 0 {
			failed++
			reachable += gating
		}
	}
	for _, a := range allow {
		if !a.used && (len(only) == 0 || contains(only, a.target)) {
			fmt.Printf("vulncheck: warning: %s:%d (%s %s) matches nothing any more — remove it\n", allowPath, a.line, a.id, a.target)
		}
	}
	if failed > 0 {
		fmt.Printf("vulncheck: FAILED — %d reachable vulnerabilit%s in %d module%s, not in %s: update the dependency (or the go running this), or list it there with why\n",
			reachable, plural(reachable, "y", "ies"), failed, plural(failed, "", "s"), allowPath)
		return 1
	}
	fmt.Printf("vulncheck: %d module%s, no reachable vulnerability outside %s\n", len(targets), plural(len(targets), "", "s"), allowPath)
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "vulncheck:", err)
	return 2
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// repoRoot is the xbin checkout around the working directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && bytes.HasPrefix(b, []byte("module github.com/xbin-dev/xbin\n")) {
			return dir, nil
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", errors.New("not inside the xbin repo (run it from the checkout: make vulncheck)")
		}
		dir = up
	}
}

func goCmd(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	return cmd
}

// goVersion is the GOVERSION of the go that govulncheck's package loading
// runs for t (in its directory, with its environment: a go.mod's toolchain
// line may pick another).
func goVersion(t target) (string, error) {
	out, err := goCmd(t.dir, t.env, "env", "GOVERSION").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// goRelease is a Go release version as govulncheck v1.8.0 reads one
// (internal/semver.GoTagToSemver): anything else, it treats every
// standard-library advisory as not affecting the code.
var (
	goRelease       = regexp.MustCompile(`^go\d+\.\d+(\.\d+)?((beta|rc)\d+)?$`)
	goReleasePrefix = regexp.MustCompile(`^go\d+\.\d+(\.\d+)?((beta|rc)\d+)?`)
)

// releaseVersion is the Go release a GOVERSION names, whose standard library
// the toolchain's is: "go1.27.0-X:nodwarf5" (a GOEXPERIMENT baked in) and
// "go1.27.0 X:nodwarf5" are go1.27.0. ok is false for one naming no release
// ("devel go1.28-abc…", "go1.27.0abc").
func releaseVersion(gover string) (string, bool) {
	gover = strings.TrimSpace(gover)
	rel := goReleasePrefix.FindString(gover)
	if rel == "" {
		return "", false
	}
	if rest := gover[len(rel):]; rest != "" && rest[0] != '-' && rest[0] != ' ' {
		return "", false
	}
	return rel, true
}

// discover lists the targets, in the order they print.
func discover(repo string) ([]target, error) {
	ts := []target{
		{name: "xbind", dir: repo, patterns: []string{"./cmd/..."}, env: releaseEnv("amd64"), stdlib: true},
		{name: "xbind/arm64", dir: repo, patterns: []string{"./cmd/..."}, env: releaseEnv("arm64"), stdlib: true},
		{name: "relay", dir: filepath.Join(repo, "relay"), patterns: []string{"./..."}, stdlib: true},
		{name: "sdk", dir: filepath.Join(repo, "sdk"), patterns: []string{"./..."}},
	}
	var tiles []target
	for _, tree := range embeddedTrees {
		err := filepath.WalkDir(filepath.Join(repo, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || (d.Name() != "go.mod.tile" && d.Name() != "go.mod") {
				return nil
			}
			rel, _ := filepath.Rel(repo, filepath.Dir(p))
			tiles = append(tiles, target{name: filepath.ToSlash(rel), src: filepath.Dir(p),
				// read-only (and through its own go.work, run adds): the
				// versions it ships, or an error — never ones the go command
				// would fetch or add to make it build
				env: []string{"GOFLAGS=" + strings.TrimSpace(os.Getenv("GOFLAGS")+" -mod=readonly")}})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].name < tiles[j].name })
	return append(ts, tiles...), nil
}

func selectTargets(all []target, only []string) ([]target, error) {
	if len(only) == 0 {
		return all, nil
	}
	var out []target
	for _, name := range only {
		found := false
		for _, t := range all {
			if t.name == name {
				out, found = append(out, t), true
			}
		}
		if !found {
			var names []string
			for _, t := range all {
				names = append(names, t.name)
			}
			return nil, fmt.Errorf("no target %q (targets: %s)", name, strings.Join(names, " "))
		}
	}
	return out, nil
}

// copyTile makes t's module buildable in dst as a workspace builds it: its
// tree (regular files only), go.mod.tile restored to go.mod and go.sum.tile
// to go.sum, beside a go.work shaped as xbind's build renders one (D166,
// internal/deps renderBuildWork): the module, the sdk replaced by this
// checkout's, the go line the highest of 1.24 and the module's. Its go.mod
// and go.sum are used as they are: workspace mode never edits them, so a
// mismatch with its code is an error, as in the build.
func copyTile(t target, repo, dst string) error {
	err := filepath.WalkDir(t.src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(t.src, p)
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case !d.Type().IsRegular():
			return nil
		}
		switch rel {
		case "go.mod.tile":
			out = filepath.Join(dst, "go.mod")
		case "go.sum.tile":
			out = filepath.Join(dst, "go.sum")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return err
	}
	mod, err := os.ReadFile(filepath.Join(dst, "go.mod"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, "go.work"), []byte(tileGoWork(mod, filepath.Join(repo, "sdk"))), 0o644)
}

// buildGo is the lowest go line of a go.work xbind renders (internal/deps).
const buildGo = "1.24"

var goLineRE = regexp.MustCompile(`(?m)^go[ \t]+([^ \t\r\n/]+)`)

// tileGoWork is the go.work a build of the module whose go.mod is mod gets
// from xbind (D166): itself, the sdk at sdkDir. The go command refuses a
// go.work whose go line is below a module it uses; one that isn't a Go
// version never raises it.
func tileGoWork(mod []byte, sdkDir string) string {
	goLine := buildGo
	if m := goLineRE.FindSubmatch(mod); m != nil && version.Compare("go"+string(m[1]), "go"+goLine) > 0 {
		goLine = string(m[1])
	}
	if strings.ContainsAny(sdkDir, " \t\"'`\\") {
		sdkDir = strconv.Quote(sdkDir)
	}
	return fmt.Sprintf("go %s\n\nuse .\n\nreplace %s => %s\n", goLine, sdkModule, sdkDir)
}

// packageDirs lists dir's package directories as patterns: every directory
// holding a non-test .go file, but testdata, vendor, node_modules and ".*"
// ones. "_*" ones count: a template's backend is _backend (its entry), which
// ./... would skip.
func packageDirs(dir string) ([]string, error) {
	seen := map[string]bool{}
	var pats []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch n := d.Name(); {
			case p == dir:
			case n == "testdata" || n == "vendor" || n == "node_modules" || strings.HasPrefix(n, "."):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(dir, filepath.Dir(p))
		pat := "."
		if rel != "." {
			pat = "./" + filepath.ToSlash(rel)
		}
		if !seen[pat] {
			seen[pat] = true
			pats = append(pats, pat)
		}
		return nil
	})
	sort.Strings(pats)
	return pats, err
}

// --- govulncheck's JSON -----------------------------------------------------

type frame struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Package  string `json:"package"`
	Function string `json:"function"`
	Receiver string `json:"receiver"`
	Position *struct {
		Filename string `json:"filename"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
	} `json:"position"`
}

type message struct {
	Config *struct {
		ScannerVersion string `json:"scanner_version"`
		GoVersion      string `json:"go_version"`
		DBLastModified string `json:"db_last_modified"`
	} `json:"config"`
	OSV *struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"osv"`
	Finding *struct {
		OSV          string  `json:"osv"`
		FixedVersion string  `json:"fixed_version"`
		Trace        []frame `json:"trace"`
	} `json:"finding"`
}

// finding is one vulnerability a module's code reaches.
type finding struct {
	ID, Module, Version, Fixed, Summary string
	Trace                               string // one call path's last step, as govulncheck prints it
}

type result struct {
	scanner, goVersion, dbModified string
	reachable                      []finding // by ID, one each
	notCalled                      []string  // IDs required or imported, never called
	stdlibSkipped                  []string  // reachable standard-library IDs that don't gate this target
}

func (r result) dbDate() string {
	if d, _, ok := strings.Cut(r.dbModified, "T"); ok {
		return d
	}
	return r.dbModified
}

// parse reads govulncheck's -format json stream. stdlib: standard-library
// findings gate this target.
func parse(rd io.Reader, stdlib bool) (result, error) {
	var r result
	summaries := map[string]string{}
	reach := map[string]*finding{}
	other := map[string]bool{}
	skipped := map[string]bool{}
	dec := json.NewDecoder(rd)
	for {
		var m message
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return r, err
		}
		switch {
		case m.Config != nil:
			r.scanner, r.goVersion, r.dbModified = m.Config.ScannerVersion, m.Config.GoVersion, m.Config.DBLastModified
		case m.OSV != nil:
			summaries[m.OSV.ID] = m.OSV.Summary
		case m.Finding != nil && len(m.Finding.Trace) > 0:
			f, top := m.Finding, m.Finding.Trace[0]
			switch {
			case top.Function == "":
				other[f.OSV] = true
			case top.Module == "stdlib" && !stdlib:
				skipped[f.OSV] = true
			case reach[f.OSV] == nil:
				reach[f.OSV] = &finding{ID: f.OSV, Module: top.Module, Version: top.Version, Fixed: f.FixedVersion, Trace: traceLine(f.Trace)}
			}
		}
	}
	if stdlib && !goRelease.MatchString(r.goVersion) {
		// govulncheck found no standard-library advisory because it could
		// read no version, not because there is none: never a pass
		return r, fmt.Errorf("govulncheck judged the standard library of go version %q, which it can't match against any advisory (it reads only a release: go1.N.P)", r.goVersion)
	}
	for id, f := range reach {
		f.Summary = summaries[id]
		r.reachable = append(r.reachable, *f)
		delete(other, id)
		delete(skipped, id)
	}
	sort.Slice(r.reachable, func(i, j int) bool { return r.reachable[i].ID < r.reachable[j].ID })
	for id := range other {
		if !skipped[id] {
			r.notCalled = append(r.notCalled, id)
		}
	}
	for id := range skipped {
		r.stdlibSkipped = append(r.stdlibSkipped, id)
	}
	sort.Strings(r.notCalled)
	sort.Strings(r.stdlibSkipped)
	return r, nil
}

// traceLine is a call path's step into the vulnerable symbol, as
// govulncheck's text output prints it: "backend/sshd.go:344:43:
// backend.Tile.handleConn calls ssh.NewServerConn".
func traceLine(tr []frame) string {
	callee := symbol(tr[0])
	if len(tr) < 2 {
		return callee
	}
	caller := tr[1]
	pos := ""
	if caller.Position != nil {
		pos = fmt.Sprintf("%s:%d:%d: ", caller.Position.Filename, caller.Position.Line, caller.Position.Column)
	}
	return pos + symbol(caller) + " calls " + callee
}

func symbol(f frame) string {
	s := path.Base(f.Package) + "."
	if r := strings.TrimPrefix(f.Receiver, "*"); r != "" {
		s += r + "."
	}
	return s + f.Function
}

// --- the allow file ---------------------------------------------------------

type allowEntry struct {
	id, target, reason string
	line               int
	used               bool
}

var osvID = regexp.MustCompile(`^GO-\d{4}-\d{4,}$`)

// readAllow parses the allow file: one entry per line, `<OSV id> <target>
// # <why>`, target a name this prints or * for every one; "#" lines and
// blank ones are comments. A missing file allows nothing.
func readAllow(p string) ([]*allowEntry, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseAllow(f, p)
}

func parseAllow(rd io.Reader, name string) ([]*allowEntry, error) {
	var out []*allowEntry
	sc := bufio.NewScanner(rd)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, why, _ := strings.Cut(line, "#")
		fields := strings.Fields(entry)
		why = strings.TrimSpace(why)
		switch {
		case len(fields) != 2:
			return nil, fmt.Errorf("%s:%d: want `<OSV id> <target> # <why>`, got %q", name, n, line)
		case !osvID.MatchString(fields[0]):
			return nil, fmt.Errorf("%s:%d: %q is not a Go vulnerability id (GO-YYYY-NNNN)", name, n, fields[0])
		case why == "":
			return nil, fmt.Errorf("%s:%d: %s %s says nothing about why it may ship — add `# <why>`", name, n, fields[0], fields[1])
		}
		out = append(out, &allowEntry{id: fields[0], target: fields[1], reason: why, line: n})
	}
	return out, sc.Err()
}

// allowed is the entry letting target ship with vulnerability id, or nil.
func allowed(allow []*allowEntry, target, id string) *allowEntry {
	for _, a := range allow {
		if a.id == id && (a.target == target || a.target == "*") {
			return a
		}
	}
	return nil
}

// report prints t's result and returns how many of its reachable
// vulnerabilities gate the release.
func report(w io.Writer, t target, r result, allow []*allowEntry, allowPath string) int {
	var gating, ok []finding
	var by []*allowEntry
	for _, f := range r.reachable {
		if a := allowed(allow, t.name, f.ID); a != nil {
			a.used = true
			ok, by = append(ok, f), append(by, a)
		} else {
			gating = append(gating, f)
		}
	}
	var notes []string
	if n := len(r.notCalled); n > 0 {
		notes = append(notes, fmt.Sprintf("%d more required or imported, never called", n))
	}
	if n := len(r.stdlibSkipped); n > 0 {
		notes = append(notes, fmt.Sprintf("%d in the standard library, the host Go's", n))
	}
	note := ""
	if len(notes) > 0 {
		note = " (" + strings.Join(notes, "; ") + ")"
	}
	if len(gating) == 0 {
		fmt.Fprintf(w, "  ✓ %s%s\n", t.name, note)
	} else {
		fmt.Fprintf(w, "  ✗ %s: %d reachable%s\n", t.name, len(gating), note)
	}
	for _, f := range gating {
		fixed := "no fix yet"
		if f.Fixed != "" {
			fixed = "fixed in " + f.Fixed
		}
		mod := f.Module + "@" + f.Version
		if f.Module == "stdlib" {
			mod = "the standard library of go" + strings.TrimPrefix(f.Version, "v")
		}
		fmt.Fprintf(w, "      %s %s (%s): %s\n          %s\n", f.ID, mod, fixed, f.Summary, f.Trace)
	}
	for i, f := range ok {
		fmt.Fprintf(w, "      %s allowed by %s:%d: %s\n", f.ID, allowPath, by[i].line, by[i].reason)
	}
	return len(gating)
}
