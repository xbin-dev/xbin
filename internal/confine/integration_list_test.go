package confine

import (
	"bufio"
	"go/build/constraint"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// covers NP-15-6 — every package with integration-tagged tests is in
// `make integration`.
//
// The Makefile's integration target runs `go test -tags=integration` over an
// explicit package list. A package whose sandboxed tests carry the tag but
// which the list misses is compiled by no build and run by no CI job:
// internal/sandbox's tests sat like that. This finds every such package by
// Go's own reach — the root module and the go.work modules; no `.`/`_`
// directories, no testdata, no other nested module — and fails when no
// `go test -tags=integration` line of the target names it, directly or
// through a `./dir/...` pattern.
func TestIntegrationPackagesListed(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pats := integrationTargetPatterns(t, filepath.Join(root, "Makefile"))
	if len(pats) == 0 {
		t.Fatal("found no `go test -tags=integration ./…` line in the Makefile's integration target — " +
			"the parser below no longer understands it; fix the parser, not the list")
	}
	tagged := integrationTaggedPackages(t, root)
	if len(tagged) == 0 && runtime.GOOS == "linux" {
		t.Fatal("found no package with integration-tagged tests (internal/confine has one) — the walk is broken")
	}
	var missing []string
	for pkg, files := range tagged {
		if !patternsCover(pats, pkg) {
			missing = append(missing, "./"+pkg+"/ ("+strings.Join(files, ", ")+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("packages with integration-tagged tests that `make integration` never runs — add each to "+
			"a `go test -tags=integration` line of the Makefile's integration target (it lists: %s):\n  %s",
			strings.Join(pats, " "), strings.Join(missing, "\n  "))
	}
}

// integrationTargetPatterns returns the package patterns (./…) of every
// `go test` command in the Makefile's integration recipe that sets the
// integration tag.
func integrationTargetPatterns(t *testing.T, makefile string) []string {
	t.Helper()
	b, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatal(err)
	}
	var recipe []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case !in:
			in = strings.HasPrefix(l, "integration:")
		case strings.HasPrefix(l, "\t"):
			recipe = append(recipe, l)
		case strings.TrimSpace(l) == "":
		default: // the next rule or assignment ends the recipe
			in = false
		}
		if !in && len(recipe) > 0 {
			break
		}
	}
	var pats []string
	seen := map[string]bool{}
	for _, cmd := range strings.Split(strings.ReplaceAll(strings.Join(recipe, "\n"), "\\\n", " "), "\n") {
		f := strings.Fields(cmd)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") || !goTestWithIntegrationTag(f) {
			continue
		}
		for _, a := range f {
			if strings.HasPrefix(a, "./") && !seen[a] {
				seen[a] = true
				pats = append(pats, a)
			}
		}
	}
	return pats
}

// goTestWithIntegrationTag reports whether a command line runs `go test`
// with the integration build tag (-tags=a,integration or -tags integration).
func goTestWithIntegrationTag(f []string) bool {
	goTest, tag := false, false
	for i, a := range f {
		if a == "go" && i+1 < len(f) && f[i+1] == "test" {
			goTest = true
		}
		a = strings.TrimPrefix(a, "-")
		var tags string
		switch {
		case strings.HasPrefix(a, "-tags="):
			tags = strings.TrimPrefix(a, "-tags=")
		case strings.HasPrefix(a, "tags="):
			tags = strings.TrimPrefix(a, "tags=")
		case (a == "tags" || a == "-tags") && i+1 < len(f):
			tags = f[i+1]
		}
		for _, tg := range strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' || r == '"' || r == '\'' }) {
			tag = tag || tg == "integration"
		}
	}
	return goTest && tag
}

// integrationTaggedPackages maps each package directory (repo-relative,
// slash-separated) that holds _test.go files built only with the
// integration tag, on this host, to those files' names.
func integrationTaggedPackages(t *testing.T, root string) map[string][]string {
	t.Helper()
	mods := goWorkModules(t, root)
	out := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			n := d.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "testdata" || n == "node_modules" {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil && !mods[rel] {
				return filepath.SkipDir // another module: no pattern of the Makefile reaches it
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		ok, err := integrationOnly(p)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		if ok {
			dir := filepath.ToSlash(filepath.Dir(rel))
			out[dir] = append(out[dir], d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// goWorkModules returns the module directories of go.work's use directives
// (repo-relative, slash-separated), always including the root module.
func goWorkModules(t *testing.T, root string) map[string]bool {
	t.Helper()
	mods := map[string]bool{".": true}
	b, err := os.ReadFile(filepath.Join(root, "go.work"))
	if os.IsNotExist(err) {
		return mods
	}
	if err != nil {
		t.Fatal(err)
	}
	block := false
	for _, l := range strings.Split(string(b), "\n") {
		l, _, _ = strings.Cut(l, "//")
		f := strings.Fields(l)
		switch {
		case len(f) == 0:
		case block && f[0] == ")":
			block = false
		case block:
			mods[filepath.ToSlash(filepath.Clean(f[0]))] = true
		case f[0] == "use" && len(f) > 1 && f[1] == "(":
			block = true
		case f[0] == "use" && len(f) > 1:
			mods[filepath.ToSlash(filepath.Clean(f[1]))] = true
		}
	}
	return mods
}

// integrationOnly reports whether a Go file's //go:build line makes it build
// with the integration tag and not without it, on this host.
func integrationOnly(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	host := func(tag string) bool {
		switch tag {
		case runtime.GOOS, runtime.GOARCH:
			return true
		case "unix":
			return runtime.GOOS != "windows" && runtime.GOOS != "plan9" && runtime.GOOS != "js" && runtime.GOOS != "wasip1"
		}
		return false
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			break
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, err
		}
		with := expr.Eval(func(tag string) bool { return tag == "integration" || host(tag) })
		return with && !expr.Eval(host), nil
	}
	return false, sc.Err()
}

// patternsCover reports whether a package pattern list (./dir, ./dir/,
// ./dir/..., ./...) names the package directory pkg.
func patternsCover(pats []string, pkg string) bool {
	for _, p := range pats {
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
		if p == "..." {
			return true
		}
		if tree, ok := strings.CutSuffix(p, "/..."); ok {
			if pkg == tree || strings.HasPrefix(pkg, tree+"/") {
				return true
			}
			continue
		}
		if pkg == p {
			return true
		}
	}
	return false
}
