package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// planFile and timingsFile are repo-relative.
const (
	planFile    = "hack/integration.jsonc"
	timingsFile = "hack/integration-timings.json"
)

// suite is one `go test -tags=integration` over one package (planFile).
type suite struct {
	Name      string   `json:"name"`
	Pkg       string   `json:"pkg"`
	Env       []string `json:"env"`
	Run       string   `json:"run"`
	Skip      string   `json:"skip"`
	Timeout   string   `json:"timeout"`
	Delegated bool     `json:"delegated"`
	Job       string   `json:"job"`
	Overhead  float64  `json:"overhead"`
}

type plan struct {
	Shards     int     `json:"shards"`
	UnitShards int     `json:"unit_shards"`
	Suites     []suite `json:"suites"`
	// Alone are patterns over unit keys (<suite>/<Test>): tests a local
	// run of every shard at once runs after the shards, by themselves.
	Alone []string `json:"alone"`
}

// alone reports whether key matches one of the plan's alone patterns.
func (p *plan) alone(key string) bool {
	for _, a := range p.Alone {
		if regexp.MustCompile(a).MatchString(key) {
			return true
		}
	}
	return false
}

// timings are measured seconds per profile ("ci", "local"): an integration
// test's key is <suite>/<Test>, a unit package's unit/<import path>.
type timings map[string]map[string]float64

func loadPlan(root string) (*plan, error) {
	b, err := os.ReadFile(filepath.Join(root, planFile))
	if err != nil {
		return nil, err
	}
	var p plan
	if err := jsonc.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", planFile, err)
	}
	if p.Shards < 1 || p.UnitShards < 1 {
		return nil, fmt.Errorf("%s: shards and unit_shards must be at least 1", planFile)
	}
	for _, a := range p.Alone {
		if _, err := regexp.Compile(a); err != nil {
			return nil, fmt.Errorf("%s: alone %q: %w", planFile, a, err)
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Suites {
		switch {
		case s.Name == "" || strings.ContainsAny(s.Name, "/ \t") || seen[s.Name]:
			return nil, fmt.Errorf("%s: suite name %q: empty, repeated, or with a slash or space", planFile, s.Name)
		case !strings.HasPrefix(s.Pkg, "./"):
			return nil, fmt.Errorf("%s: suite %s: pkg %q is not ./-relative", planFile, s.Name, s.Pkg)
		case strings.Contains(s.Run, "/"):
			return nil, fmt.Errorf("%s: suite %s: run %q names subtests: only a top-level filter decides membership", planFile, s.Name, s.Run)
		case s.Job != "" && (s.Job == "all" || strings.ContainsAny(s.Job, "/ ")):
			return nil, fmt.Errorf("%s: suite %s: job %q", planFile, s.Name, s.Job)
		}
		for _, re := range []string{s.Run, s.Skip} {
			if _, err := regexp.Compile(re); err != nil {
				return nil, fmt.Errorf("%s: suite %s: %w", planFile, s.Name, err)
			}
		}
		for _, kv := range s.Env {
			if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
				return nil, fmt.Errorf("%s: suite %s: env %q is not KEY=value", planFile, s.Name, kv)
			}
		}
		seen[s.Name] = true
	}
	return &p, nil
}

// jobs are the plan's job names, sorted.
func (p *plan) jobs() []string {
	set := map[string]bool{}
	for _, s := range p.Suites {
		if s.Job != "" {
			set[s.Job] = true
		}
	}
	var out []string
	for j := range set {
		out = append(out, j)
	}
	sort.Strings(out)
	return out
}

func loadTimings(root string) (timings, error) {
	b, err := os.ReadFile(filepath.Join(root, timingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return timings{}, nil
	}
	if err != nil {
		return nil, err
	}
	var t timings
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", timingsFile, err)
	}
	return t, nil
}

// goList is `go list -json` over pkgs with the given tags, in root.
type listedPkg struct {
	Dir          string
	ImportPath   string
	TestGoFiles  []string
	XTestGoFiles []string
}

func goList(root string, tags string, pkgs ...string) ([]listedPkg, error) {
	args := []string{"list", "-json=Dir,ImportPath,TestGoFiles,XTestGoFiles"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	cmd := exec.Command("go", append(args, pkgs...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %v\n%s", strings.Join(pkgs, " "), err, stderr.String())
	}
	var res []listedPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, nil
}

// testNames are the names `go test -list .` prints for a package's test
// files (Benchmarks aside: a plain run never runs them): Test and Fuzz
// functions with the testing signature, and the examples with an output
// comment (the only ones go test runs). Sorted.
func testNames(dir string, files []string) ([]string, error) {
	fset := token.NewFileSet()
	var names []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, filepath.Join(dir, f), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		testing := testingName(file)
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			n := fn.Name.Name
			switch {
			case isTest(n, "Test") && takes(fn, testing, "T"),
				isTest(n, "Fuzz") && takes(fn, testing, "F"):
				names = append(names, n)
			}
		}
		for _, ex := range doc.Examples(file) {
			if ex.Output != "" || ex.EmptyOutput {
				names = append(names, "Example"+ex.Name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// isTest is cmd/go's: name is prefix, or prefix and then not a lower-case
// letter (Testing is no test).
func isTest(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// testingName is what file calls package testing ("" when it doesn't import
// it; "." for a dot import).
func testingName(f *ast.File) string {
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == "testing" {
			if im.Name != nil {
				return im.Name.Name
			}
			return "testing"
		}
	}
	return ""
}

// takes reports whether fn's one parameter is *<pkg>.<typ> and it returns
// nothing.
func takes(fn *ast.FuncDecl, pkg, typ string) bool {
	if pkg == "" || fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	ps := fn.Type.Params.List
	if len(ps) != 1 || len(ps[0].Names) > 1 {
		return false
	}
	star, ok := ps[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := star.X.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == pkg && x.Sel.Name == typ
	case *ast.Ident:
		return pkg == "." && x.Name == typ
	}
	return false
}

// unit is one top-level test of one suite.
type unit struct {
	suite string
	test  string
}

func (u unit) key() string { return u.suite + "/" + u.test }

// listSuites returns each suite's tests: the package's (with the
// integration tag), kept when the suite's run filter matches and its skip
// filter doesn't skip the whole test.
func listSuites(root string, p *plan) (map[string][]string, error) {
	listed, err := goList(root, "integration", suitePkgs(p)...)
	if err != nil {
		return nil, err
	}
	byDir := map[string][]string{}
	for _, lp := range listed {
		names, err := testNames(lp.Dir, append(append([]string(nil), lp.TestGoFiles...), lp.XTestGoFiles...))
		if err != nil {
			return nil, err
		}
		byDir[filepath.Clean(lp.Dir)] = names
	}
	out := map[string][]string{}
	for _, s := range p.Suites {
		names, ok := byDir[filepath.Join(root, filepath.FromSlash(s.Pkg))]
		if !ok {
			return nil, fmt.Errorf("suite %s: go list returned no package for %s", s.Name, s.Pkg)
		}
		run := regexp.MustCompile(s.Run)
		var skip *regexp.Regexp
		if top, _, sub := splitTop(s.Skip); s.Skip != "" && !sub {
			skip = regexp.MustCompile(top)
		}
		var keep []string
		for _, n := range names {
			if run.MatchString(n) && (skip == nil || !skip.MatchString(n)) {
				keep = append(keep, n)
			}
		}
		out[s.Name] = keep
	}
	return out, nil
}

// splitTop splits a -run/-skip pattern at its first slash outside brackets
// and parentheses, as go test does: the top-level part, the rest, and
// whether there was a rest.
func splitTop(pat string) (top, rest string, ok bool) {
	depth := 0
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '\\':
			i++
		case '[', '(':
			depth++
		case ']', ')':
			depth--
		case '/':
			if depth == 0 {
				return pat[:i], pat[i+1:], true
			}
		}
	}
	return pat, "", false
}

// assign splits units into n shards: measured ones longest first, each onto
// the shard that ends least loaded with it (a suite's overhead counted once
// per shard that runs any of it; ties to the lower index), then every unit
// without a measurement onto the shard its key hashes to — so a new test
// moves nothing else. Deterministic: same units, weights and n, same
// shards. Returns shard index (0-based) → units in suite order then name.
func assign(units []unit, n int, weight map[string]float64, overhead map[string]float64) [][]unit {
	var known, unknown []unit
	for _, u := range units {
		if _, ok := weight[u.key()]; ok {
			known = append(known, u)
		} else {
			unknown = append(unknown, u)
		}
	}
	sort.Slice(known, func(i, j int) bool {
		wi, wj := weight[known[i].key()], weight[known[j].key()]
		if wi != wj {
			return wi > wj
		}
		return known[i].key() < known[j].key()
	})
	shards := make([][]unit, n)
	load := make([]float64, n)
	has := make([]map[string]bool, n)
	for i := range has {
		has[i] = map[string]bool{}
	}
	for _, u := range known {
		best, bestEnd := 0, 0.0
		for i := 0; i < n; i++ {
			end := load[i] + weight[u.key()]
			if !has[i][u.suite] {
				end += overhead[u.suite]
			}
			if i == 0 || end < bestEnd {
				best, bestEnd = i, end
			}
		}
		shards[best] = append(shards[best], u)
		load[best], has[best][u.suite] = bestEnd, true
	}
	for _, u := range unknown {
		h := fnv.New32a()
		_, _ = h.Write([]byte(u.key()))
		i := int(h.Sum32() % uint32(n))
		shards[i] = append(shards[i], u)
	}
	return shards
}

// integrationShards lists the plan's sharded suites (those without a job)
// and splits them into n shards by profile's timings. Without withAlone the
// plan's alone tests are left out of the split and returned on their own
// (every shard at once on one host: they run after the shards).
func integrationShards(root string, p *plan, t timings, profile string, n int, withAlone bool) ([][]unit, []unit, map[string][]string, error) {
	lists, err := listSuites(root, p)
	if err != nil {
		return nil, nil, nil, err
	}
	var units, alone []unit
	overhead := map[string]float64{}
	for _, s := range p.Suites {
		overhead[s.Name] = s.Overhead
		if s.Job != "" {
			continue
		}
		for _, n := range lists[s.Name] {
			u := unit{s.Name, n}
			if !withAlone && p.alone(u.key()) {
				alone = append(alone, u)
			} else {
				units = append(units, u)
			}
		}
	}
	shards := assign(units, n, floor(t[profile]), overhead)
	order := map[string]int{}
	for i, s := range p.Suites {
		order[s.Name] = i
	}
	for _, sh := range shards {
		sort.Slice(sh, func(i, j int) bool {
			if sh[i].suite != sh[j].suite {
				return order[sh[i].suite] < order[sh[j].suite]
			}
			return sh[i].test < sh[j].test
		})
	}
	return shards, alone, lists, nil
}

// floor gives every measured weight a small minimum, so tests measured at
// 0.00s (skipped on that host) still spread instead of piling up.
func floor(w map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(w))
	for k, v := range w {
		out[k] = max(v, 0.05)
	}
	return out
}

// unitPackages are `make test`'s packages: the root module's and the sdk's
// and relay's (their own modules).
func unitPackages(root string) ([]string, error) {
	listed, err := goList(root, "", "./...", "./sdk/...", "./relay/...")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, lp := range listed {
		out = append(out, lp.ImportPath)
	}
	sort.Strings(out)
	return out, nil
}

// unitShards splits the unit packages by package (key unit/<import path>).
func unitShards(root string, t timings, profile string, n int) ([][]string, []string, error) {
	pkgs, err := unitPackages(root)
	if err != nil {
		return nil, nil, err
	}
	units := make([]unit, len(pkgs))
	for i, p := range pkgs {
		units[i] = unit{"unit", p}
	}
	shards := assign(units, n, floor(t[profile]), nil)
	out := make([][]string, n)
	for i, sh := range shards {
		for _, u := range sh {
			out[i] = append(out[i], u.test)
		}
		sort.Strings(out[i])
	}
	return out, pkgs, nil
}

// parseShard reads "i/N" (1-based i).
func parseShard(s string) (i, n int, err error) {
	a, b, ok := strings.Cut(s, "/")
	if ok {
		i, err = strconv.Atoi(a)
		if err == nil {
			n, err = strconv.Atoi(b)
		}
	}
	if !ok || err != nil || n < 1 || i < 1 || i > n {
		return 0, 0, fmt.Errorf("shard %q: want i/N with 1 ≤ i ≤ N", s)
	}
	return i, n, nil
}
