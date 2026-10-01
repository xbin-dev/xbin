package deps

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseGoMod(t *testing.T) {
	m := parseGoMod([]byte(`// a comment
module "example.com/x" // quoted

go 1.24

require example.com/one v1.0.0
require (
	example.com/two v0.2.0 // indirect
	` + "`example.com/three`" + ` v0.3.0
)

replace example.com/one => ../one
replace (
	example.com/two v0.2.0 => example.com/fork v0.2.1
	example.com/four=>./four
)
replace broken =>
`))
	if m.Path != "example.com/x" {
		t.Errorf("module %q", m.Path)
	}
	if want := map[string]string{"example.com/one": "v1.0.0", "example.com/two": "v0.2.0", "example.com/three": "v0.3.0"}; !reflect.DeepEqual(m.Requires, want) {
		t.Errorf("requires %v", m.Requires)
	}
	want := []replaceDirective{
		{Old: "example.com/one", New: "../one"},
		{Old: "example.com/two", OldVersion: "v0.2.0", New: "example.com/fork", NewVersion: "v0.2.1"},
		{Old: "example.com/four", New: "./four"},
	}
	if !reflect.DeepEqual(m.Replaces, want) {
		t.Errorf("replaces\n got %+v\nwant %+v", m.Replaces, want)
	}
	if !want[0].dir() || want[1].dir() {
		t.Error("dir()")
	}
	if got := want[1].render("example.com/fork"); got != "replace example.com/two v0.2.0 => example.com/fork v0.2.1" {
		t.Errorf("render %q", got)
	}
	if got := (replaceDirective{Old: "a", New: "/with space"}).render("/with space"); got != `replace a => "/with space"` {
		t.Errorf("render quoted %q", got)
	}
	// the go.mod lexer's punctuation, a comment's start: quoted (a test's
	// t.TempDir holds a comma when its name does)
	for s, want := range map[string]string{"/tmp/a,b": `"/tmp/a,b"`, "/x[1]": `"/x[1]"`, "/a/*b": `"/a/*b"`, "/a{b}": `"/a{b}"`, "/plain/dir-1.2_x": "/plain/dir-1.2_x"} {
		if got := modToken(s); got != want {
			t.Errorf("modToken(%q) = %s, want %s", s, got, want)
		}
	}
	if got := parseModLines([]byte("use ( ./a ./b )\nuse ./c\n")); len(got) != 3 || got[1].args[0] != "./b" {
		t.Errorf("one-line use block: %+v", got)
	}
}

// wsFiles lays out files under root.
func wsFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, s := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// wsModule is the Module of the component at rel (its root module).
func wsModule(root, rel string) Module {
	return Module{Dir: filepath.Join(root, rel), Root: root, Rel: rel, Tile: rel}
}

func dirsOf(ms []Module) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Dir)
	}
	return out
}

// std stands in for the toolchain's standard library.
func std(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return slices.Contains([]string{"fmt", "net", "os"}, first)
}

// under root, each rel made absolute
func abs(root string, rels ...string) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = filepath.Join(root, r)
	}
	return out
}

// reachWorkspace is a victim, apps/b, beside modules that could serve its
// references and namesakes that must not; the attacker, apps/a, whose go.mod
// steered every build under the shared go.work.
var reachWorkspace = map[string]string{
	"apps/a/go.mod":            "module a\n\ngo 1.24\n\nrequire (\n\tgolang.org/x/sys v0.46.0\n\texample.com/dep v1.1.0\n\tgithub.com/foo/bar v1.0.0\n\tcalendar/store v1.0.0\n\texample.com/lib v1.0.0\n\tlib2/greet v1.0.0\n)\n\nreplace golang.org/x/sys => ./evil\nreplace example.com/dep v1.1.0 => ./dep\nreplace lib2 => ./evil\n",
	"apps/a/a.go":              "package a\n",
	"apps/b/go.mod":            "module b\n\ngo 1.24\n\nrequire (\n\tgolang.org/x/sys v0.46.0\n\texample.com/dep v1.0.0\n\texample.com/lib v0.0.0\n\texample.com/c v1.0.0\n\texample.com/local v0.0.0\n\texample.com/dotted v1.2.3\n\tgithub.com/xbin-dev/xbin/sdk v0.0.0\n)\n\nreplace example.com/c => ../c\nreplace example.com/local => ./local\n",
	"apps/b/backend/main.go":   "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\n\t\"b/elsewhere\"\n\t\"b/nested/api\"\n\t\"b/store\"\n\t\"calendar/store\"\n\t\"example.com/a2/x\"\n\t\"example.com/dn/p\"\n\t\"github.com/foo/bar/baz\"\n\t\"github.com/xbin-dev/xbin/sdk/ws\"\n\t\"golang.org/x/sys/unix\"\n\t\"lib2/greet\"\n)\n",
	"apps/b/backend/tagged.go": "//go:build never\n\npackage main\n\nimport _ \"tagged\"\n",
	"apps/b/backend/x_test.go": "package main\n\nimport _ \"testonly\"\n",
	"apps/b/_backend/t.go":     "package main\n\nimport _ \"underscored\"\n", // an entry can live there
	"apps/b/.cache/h.go":       "package h\n\nimport _ \"hidden\"\n",
	"apps/b/store/s.go":        "package store\n",
	"apps/b/local/go.mod":      "module example.com/local\n", // the tile's own, by its replace
	"apps/b/local/l.go":        "package local\n",
	"apps/b/nested/go.mod":     "module b/nested\n", // a component nested in b
	"apps/b/nested/api/a.go":   "package api\n",
	"apps/b/dn/go.mod":         "module example.com/dn\n", // a dotted component nested in b's tree
	"apps/b/dn/p/p.go":         "package p\n",
	"apps/calendar/go.mod":     "module calendar\n\ngo 1.24\n\nrequire util v0.0.0\n",
	"apps/calendar/store/s.go": "package store\n",
	"apps/util/go.mod":         "module util\n\ngo 1.24\n",
	"apps/lib2/go.mod":         "module lib2\n",
	"apps/lib2/greet/g.go":     "package greet\n",
	"apps/tagged/go.mod":       "module tagged\n",
	"apps/tagged/t.go":         "package tagged\n",
	"apps/testonly/go.mod":     "module testonly\n",
	"apps/testonly/t.go":       "package testonly\n",
	"apps/underscored/go.mod":  "module underscored\n",
	"apps/underscored/u.go":    "package underscored\n",
	"apps/hidden/go.mod":       "module hidden\n",
	"apps/hidden/h.go":         "package hidden\n",
	"apps/lib/go.mod":          "module example.com/lib\n",
	"apps/c/go.mod":            "module example.com/c\n",
	"apps/dotted/go.mod":       "module example.com/dotted\n",
	"apps/a2/go.mod":           "module example.com/a2\n",
	"apps/a2/x/x.go":           "package x\n",
	// namesakes
	"apps/squat/go.mod":           "module golang.org/x/sys\n\nreplace example.com/dep => ../a/dep\n",
	"apps/squat/unix/u.go":        "package unix\n",
	"apps/unixsquat/go.mod":       "module golang.org/x/sys/unix\n\nreplace golang.org/x/sys => ../a/evil\n",
	"apps/unixsquat/u.go":         "package unix\n",
	"apps/xsquat/go.mod":          "module golang.org/x\n",
	"apps/xsquat/sys/unix/u.go":   "package unix\n",
	"apps/squat2/go.mod":          "module github.com/foo/bar\n",
	"apps/squat2/baz/b.go":        "package baz\n",
	"apps/csquat/go.mod":          "module example.com/c\n",
	"apps/localsquat/go.mod":      "module example.com/local\n",
	"apps/localsquat/l.go":        "package local\n",
	"apps/netsquat/go.mod":        "module net\n",
	"apps/netsquat/http/h.go":     "package http\n",
	"apps/sdkfake/go.mod":         "module github.com/xbin-dev/xbin/sdk\n",
	"apps/sdkws/go.mod":           "module github.com/xbin-dev/xbin/sdk/ws\n",
	"apps/sdkws/ws.go":            "package ws\n",
	"apps/sdkprefix/go.mod":       "module github.com/xbin-dev/xbin\n\nreplace golang.org/x/sys => ./evil\n",
	"apps/sdkprefix/sdk/ws/ws.go": "package ws\n",
	"apps/ownsquat/go.mod":        "module b/store\n",
	"apps/ownsquat/s.go":          "package store\n",
	"apps/namesake/go.mod":        "module b/elsewhere\n",
	"apps/namesake/e.go":          "package elsewhere\n",
	"apps/twin/go.mod":            "module b\n", // b's own path: never another module
	"apps/broken/go.mod":          "this is not a go.mod\n",
}

var reachModules = []string{"a", "b/nested", "b/dn", "calendar", "util", "lib2", "tagged", "testonly", "underscored", "hidden", "lib", "c", "dotted", "a2",
	"squat", "unixsquat", "xsquat", "squat2", "csquat", "localsquat", "netsquat", "sdkfake", "sdkws", "sdkprefix", "ownsquat", "namesake", "twin", "broken"}

// covers D166 — a tile's build uses its own module, the SDK, and the
// workspace modules its own go.mod, manifest and code choose, never a
// namesake: a require of a dotted path at a published version is the
// proxy's (x/sys, example.com/dep, example.com/dotted); a require its
// go.mod also replaces is the replace's alone (example.com/c from ../c,
// example.com/local from its own ./local — the namesakes elsewhere never
// serve them); a placeholder requirement (example.com/lib v0.0.0) and a
// dotless import (calendar/store, lib2/greet, and on through calendar's
// util) are the workspace's; an import under a path its go.mod requires
// (golang.org/x/sys/unix — whatever declares golang.org/x/sys/unix or
// golang.org/x), the SDK's (sdk/ws) or a std package (net/http) is never
// looked up among the tiles; an import of its own path is its own
// package (b/store) or a module nested in its own directory (b/nested),
// never a namesake elsewhere (b/elsewhere); a dotted import no go.mod line
// covers is another tile's only when the manifest names it in deps — or
// when the module is nested in the tile's own tree (example.com/dn).
func TestBuildWorkReach(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, reachWorkspace)
	var others []Module
	for _, c := range reachModules {
		others = append(others, wsModule(root, "apps/"+c))
	}
	b := Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: others, SDK: "/opt/xbin/sdk", Std: std}
	w := BuildWork(b)
	want := abs(root, "apps/b", "apps/b/dn", "apps/b/nested", "apps/c", "apps/calendar", "apps/lib", "apps/lib2", "apps/tagged", "apps/underscored", "apps/util")
	if got := dirsOf(w.Uses); !reflect.DeepEqual(got, want) {
		t.Errorf("uses\n got %q\nwant %q", got, want)
	}
	gw := string(w.GoWork)
	for _, s := range []string{"\ngo 1.24\n", "\t" + filepath.Join(root, "apps/b") + "\n", "replace github.com/xbin-dev/xbin/sdk => /opt/xbin/sdk\n"} {
		if !strings.Contains(gw, s) {
			t.Errorf("no %q in the go.work:\n%s", s, gw)
		}
	}
	for _, s := range []string{"apps/a\n", "squat", "apps/sdk", "evil", "dotted", "namesake", "a2"} {
		if strings.Contains(gw, s) {
			t.Errorf("%q in the go.work:\n%s", s, gw)
		}
	}
	// without an SDK configured, its path is still never a tile's
	b.SDK = ""
	if got := dirsOf(BuildWork(b).Uses); !reflect.DeepEqual(got, want) {
		t.Errorf("no SDK: uses %q", got)
	}

	// the manifest names apps/dotted and apps/a2 in deps: the builder's own
	// choice of their code, even for a dotted path at a published version
	b.Deps = func(from, to string) bool { return from == "apps/b" && (to == "apps/dotted" || to == "apps/a2") }
	got := dirsOf(BuildWork(b).Uses)
	for _, m := range []string{"apps/dotted", "apps/a2"} {
		if !slices.Contains(got, filepath.Join(root, m)) {
			t.Errorf("with deps, %s isn't used: %q", m, got)
		}
	}
	if slices.Contains(got, filepath.Join(root, "apps/squat2")) {
		t.Errorf("with deps: %q", got)
	}

	// and a's build reaches none of b's: its own module alone
	a := Build{Tile: "apps/a", Own: []Module{wsModule(root, "apps/a")}, Others: append(others[1:], wsModule(root, "apps/b")), Std: std}
	if got := dirsOf(BuildWork(a).Uses); !reflect.DeepEqual(got, abs(root, "apps/a")) {
		t.Errorf("a's uses %q", got)
	}
}

// covers D166 — what a go.mod the build doesn't use says never changes the
// build: the attacker's requirements of paths the victim imports from the
// workspace (calendar/store, lib2/greet, example.com/lib), a module
// declaring a path beneath the victim's own (b/store), or a replace of a
// module the victim uses — b's workspace is the same without them.
func TestBuildWorkUnrelatedGoMods(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, reachWorkspace)
	var others []Module
	for _, c := range reachModules {
		others = append(others, wsModule(root, "apps/"+c))
	}
	b := Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: others, Std: std}
	hostile := BuildWork(b)
	wsFiles(t, root, map[string]string{"apps/a/go.mod": "module a\n"})
	for _, rel := range []string{"apps/ownsquat", "apps/unixsquat", "apps/squat"} {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if benign := BuildWork(b); !reflect.DeepEqual(hostile.GoWork, benign.GoWork) {
		t.Errorf("another tile's go.mod changed b's build:\nhostile\n%s\nbenign\n%s", hostile.GoWork, benign.GoWork)
	}

	// a dotless import b makes without a require: another tile requiring
	// that path at a version, or a dotted module's, doesn't take it away
	root = t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/b/go.mod":            "module b\n",
		"apps/b/backend/main.go":   "package main\n\nimport _ \"calendar/store\"\n",
		"apps/calendar/go.mod":     "module calendar\n",
		"apps/calendar/store/s.go": "package store\n",
		"apps/a/go.mod":            "module a\n\nrequire (\n\tcalendar/store v1.0.0\n\tcalendar v1.0.0\n)\n\nreplace calendar => ./evil\n",
	})
	w := BuildWork(Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: []Module{wsModule(root, "apps/a"), wsModule(root, "apps/calendar")}, Std: std})
	if got := dirsOf(w.Uses); !reflect.DeepEqual(got, abs(root, "apps/b", "apps/calendar")) {
		t.Errorf("uses %q", got)
	}
}

// covers D166 — an import two workspace modules could provide (calendar's
// store/ and a module declaring calendar/store) is served by neither: the
// go command would say "ambiguous import", and silently taking either
// would let a namesake stand in. The manifest's deps pick one; a require
// line makes it the go.mod's.
func TestBuildWorkAmbiguousImport(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/b/go.mod":            "module b\n",
		"apps/b/backend/main.go":   "package main\n\nimport _ \"calendar/store\"\n",
		"apps/calendar/go.mod":     "module calendar\n",
		"apps/calendar/store/s.go": "package store\n",
		"apps/a/go.mod":            "module calendar/store\n",
		"apps/a/s.go":              "package store\n",
	})
	b := Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: []Module{wsModule(root, "apps/a"), wsModule(root, "apps/calendar")}, Std: std}
	w := BuildWork(b)
	if got := dirsOf(w.Uses); !reflect.DeepEqual(got, abs(root, "apps/b")) {
		t.Errorf("uses %q", got)
	}
	h := w.Hint("backend/main.go:3:10: package calendar/store is not in std (/usr/lib/go/src/calendar/store)\n")
	if !strings.Contains(h, "could provide calendar/store (calendar, calendar/store)") || !strings.Contains(h, "name its tile in apps/b's xbin.json deps, or add `require <it> v0.0.0` and `replace <it> => <its directory>`") || strings.Contains(h, "apps/a") || strings.Contains(h, root) {
		t.Errorf("hint:\n%s", h)
	}
	b.Deps = func(from, to string) bool { return to == "apps/calendar" }
	if got := dirsOf(BuildWork(b).Uses); !reflect.DeepEqual(got, abs(root, "apps/b", "apps/calendar")) {
		t.Errorf("deps: uses %q", got)
	}
	b.Deps = nil
	wsFiles(t, root, map[string]string{"apps/b/go.mod": "module b\n\nrequire calendar v0.0.0\n"})
	if got := dirsOf(BuildWork(b).Uses); !reflect.DeepEqual(got, abs(root, "apps/b", "apps/calendar")) {
		t.Errorf("required: uses %q", got)
	}
}

// covers D166 — the build's go line is the highest of xbind's (1.24), a
// hand-managed root go.work's and those of the modules the build uses, in
// the go command's order (1.24 < 1.24rc1 < 1.24.0): `go mod init` writes
// `go 1.24.0`, which a `go 1.24` go.work refuses.
func TestBuildWorkGoLine(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/b/go.mod":   "module b\n\ngo 1.24.0\n\nrequire lib v0.0.0\n",
		"apps/lib/go.mod": "module lib\n\ngo 1.25rc1\n",
		"apps/x/go.mod":   "module x\n\ngo 1.99.0\n", // not used
		"apps/y/go.mod":   "module y\n\ngo banana\n",
	})
	b := Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: []Module{wsModule(root, "apps/lib"), wsModule(root, "apps/x")}}
	if gw := string(BuildWork(b).GoWork); !strings.Contains(gw, "\ngo 1.25rc1\n") {
		t.Errorf("go line:\n%s", gw)
	}
	b.Root = &RootWork{Go: "1.25"}
	if gw := string(BuildWork(b).GoWork); !strings.Contains(gw, "\ngo 1.25rc1\n") {
		t.Errorf("a root's lower go line won:\n%s", gw)
	}
	b.Root = &RootWork{Go: "1.26"}
	if gw := string(BuildWork(b).GoWork); !strings.Contains(gw, "\ngo 1.26\n") {
		t.Errorf("a root's higher go line lost:\n%s", gw)
	}
	b = Build{Tile: "apps/y", Own: []Module{wsModule(root, "apps/y")}}
	if gw := string(BuildWork(b).GoWork); !strings.Contains(gw, "\ngo 1.24\n") {
		t.Errorf("an invalid go line:\n%s", gw)
	}
	for _, tc := range [][2]string{{"1.24", "1.24rc1"}, {"1.24rc1", "1.24.0"}, {"1.24beta2", "1.24rc1"}, {"1.24.0", "1.24.1"}, {"1.24.9", "1.25"}, {"1.9.0", "1.10"}, {"banana", "1.0"}} {
		if !goVersionLess(tc[0], tc[1]) || goVersionLess(tc[1], tc[0]) {
			t.Errorf("goVersionLess(%s, %s)", tc[0], tc[1])
		}
	}
}

// covers D166 — the go command's "no required module provides package" and
// "package … is not in std" get a line that says what to add under the
// build's own workspace — naming module paths and versions only, never
// another tile or its directory (the build's output is the tile's readers';
// D40 keeps unreadable tiles' names from them).
func TestBuildWorkHint(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/x/go.mod":  "module x\n",
		"apps/y/go.mod":  "module example.com/y\n\nrequire golang.org/x/sys v0.41.0\n",
		"apps/z/go.mod":  "module z\n\nrequire golang.org/x/sys v0.46.0\n",
		"apps/zz/go.mod": "module zz\n\nrequire golang.org/x/sys v0.9.0\n",
		"apps/l/go.mod":  "module lib\n",
		// namesakes the hint never steers the builder to
		"apps/squat/go.mod": "module golang.org/x/sys\n",
		"apps/sdkws/go.mod": "module github.com/xbin-dev/xbin/sdk/ws\n",
	})
	w := BuildWork(Build{Tile: "apps/x", Own: []Module{wsModule(root, "apps/x")},
		Others: []Module{wsModule(root, "apps/y"), wsModule(root, "apps/z"), wsModule(root, "apps/zz"), wsModule(root, "apps/l"), wsModule(root, "apps/squat"), wsModule(root, "apps/sdkws")}})
	out := "backend/main.go:5:2: no required module provides package golang.org/x/sys/unix; to add it:\n\tgo get golang.org/x/sys/unix\n" +
		"backend/main.go:6:2: no required module provides package example.com/y/api; to add it:\n" +
		"backend/main.go:7:2: package lib/missing is not in std (/usr/lib/go/src/lib/missing)\n"
	h := w.Hint(out)
	if l := strings.Split(h, "\n")[0]; !strings.HasSuffix(l, "add `require golang.org/x/sys v0.46.0` to apps/x's go.mod") {
		t.Errorf("the published module's line offers more: %q", l)
	}
	if h := w.Hint("no required module provides package github.com/xbin-dev/xbin/sdk/ws/x; to add it:\n"); h != "" {
		t.Errorf("a hint offers a namesake of the SDK: %s", h)
	}
	for _, s := range []string{
		"add `require golang.org/x/sys v0.46.0` to apps/x's go.mod",
		"no module this build uses provides example.com/y/api. If you mean the workspace's Go module example.com/y",
		"name its tile in apps/x's xbin.json deps, or add `require example.com/y v0.0.0` and `replace example.com/y => <its directory>` to apps/x's go.mod",
		"lib/missing is in the workspace's Go module lib, which this build doesn't use: name its tile",
		"2026-09-30-go-build-workspace.md",
	} {
		if !strings.Contains(h, s) {
			t.Errorf("no %q in the hint:\n%s", s, h)
		}
	}
	for _, s := range []string{"apps/y", "apps/z", "apps/l", root} {
		if strings.Contains(h, s) {
			t.Errorf("the hint names %q:\n%s", s, h)
		}
	}
	if w.Hint("undefined: foo") != "" {
		t.Error("a hint for an unrelated error")
	}
	if !semverLess("v0.9.0", "v0.41.0") || semverLess("v1.0.0", "v1.0.0-rc1") || !semverLess("v1.0.0-rc1", "v1.0.0") {
		t.Error("semverLess")
	}
}

// A module a tile reaches brings its own reach, and its go.mod's replace
// lines apply to the build (workspace mode): the tile chose its code.
func TestBuildWorkTransitive(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/x/go.mod":      "module x\n\nrequire mid v0.0.0\n",
		"apps/mid/go.mod":    "module mid\n\nreplace example.com/q => ./q\n",
		"apps/mid/m.go":      "package mid\n\nimport _ \"leaf/p\"\n",
		"apps/leaf/go.mod":   "module leaf\n",
		"apps/leaf/p/p.go":   "package p\n",
		"apps/other/go.mod":  "module other\n",
		"apps/x/sub/go.mod":  "module x/sub\n", // nested module: not scanned as x's
		"apps/x/sub/s.go":    "package sub\n\nimport _ \"other\"\n",
		"apps/x/vendor/v.go": "package v\n\nimport _ \"other\"\n",
	})
	w := BuildWork(Build{Tile: "apps/x", Own: []Module{wsModule(root, "apps/x")},
		Others: []Module{wsModule(root, "apps/mid"), wsModule(root, "apps/leaf"), wsModule(root, "apps/other")}})
	want := []string{filepath.Join(root, "apps/x"), filepath.Join(root, "apps/leaf"), filepath.Join(root, "apps/mid")}
	if got := dirsOf(w.Uses); !reflect.DeepEqual(got, want) {
		t.Errorf("uses %q, want %q", got, want)
	}
	if strings.Contains(string(w.GoWork), "replace") {
		t.Errorf("no SDK: no replace line\n%s", w.GoWork)
	}
}

// A hand-managed root go.work keeps its settings for every build — go,
// toolchain and godebug lines, its replaces (the SDK's too) — and its uses
// are modules like the components': used when reached, an admin-only one
// (outside every tile) by any reference. xbind's own go.work says nothing a
// build keeps; a marker below the first line doesn't make it xbind's.
func TestReadRootWork(t *testing.T) {
	root := t.TempDir()
	if rw, err := ReadRootWork(root); rw != nil || err != nil {
		t.Fatalf("no go.work: %+v %v", rw, err)
	}
	wsFiles(t, root, map[string]string{"go.work": renderGoWork([]string{"./apps/a"}, buildGo, "/opt/xbin/sdk")})
	if rw, err := ReadRootWork(root); rw != nil || err != nil {
		t.Fatalf("xbind's go.work: %+v %v", rw, err)
	}
	wsFiles(t, root, map[string]string{
		"go.work":              "go 1.25.1\n" + workMarker + "\n\ntoolchain go1.25.3\n\ngodebug (\n\tpanicnil=1\n)\n\nuse (\n\t./apps/x\n\t\"./lib/shared\" // the org's\n)\nuse /elsewhere/mod\n\nreplace github.com/xbin-dev/xbin/sdk => ./sdk-dev\nreplace example.com/pinned v1.0.0 => example.com/pinned v1.0.1\n",
		"apps/x/go.mod":        "module x\n\nrequire (\n\tgithub.com/org/shared v1.2.3\n\tgithub.com/y/lib v1.0.0\n)\n",
		"lib/shared/go.mod":    "module github.com/org/shared\n",
		"apps/y/lib/go.mod":    "module github.com/y/lib\n",
		"apps/x/backend/m.go":  "package main\n\nimport _ \"github.com/y/lib\"\n",
		"sdk-dev/go.mod":       "module github.com/xbin-dev/xbin/sdk\n",
		"apps/fake-sdk/go.mod": "module github.com/xbin-dev/xbin/sdk\n",
	})
	rw, err := ReadRootWork(root)
	if err != nil || rw == nil {
		t.Fatalf("hand-managed: %+v %v", rw, err)
	}
	if rw.Go != "1.25.1" || rw.Toolchain != "go1.25.3" || !reflect.DeepEqual(rw.Godebug, []string{"panicnil=1"}) {
		t.Errorf("lines: %+v", rw)
	}
	if want := []string{filepath.Join(root, "apps/x"), filepath.Join(root, "lib/shared"), "/elsewhere/mod"}; !reflect.DeepEqual(rw.Uses, want) {
		t.Errorf("uses %q", rw.Uses)
	}
	shared := Module{Dir: filepath.Join(root, "lib/shared"), Root: root, Rel: "lib/shared", Trusted: true}
	ylib := Module{Dir: filepath.Join(root, "apps/y/lib"), Root: root, Rel: "apps/y/lib", Tile: "apps/y"}
	w := BuildWork(Build{Tile: "apps/x", Own: []Module{wsModule(root, "apps/x")}, Others: []Module{shared, ylib, wsModule(root, "apps/fake-sdk")}, SDK: "/opt/xbin/sdk", Root: rw})
	if got := dirsOf(w.Uses); !reflect.DeepEqual(got, []string{filepath.Join(root, "apps/x"), shared.Dir}) {
		t.Errorf("uses %q (the org's module, admin-written, serves a published version; y's, a tile's, doesn't)", got)
	}
	gw := string(w.GoWork)
	for _, s := range []string{"\ngo 1.25.1\n", "\ntoolchain go1.25.3\n", "godebug (\n\tpanicnil=1\n)", "replace github.com/xbin-dev/xbin/sdk => " + filepath.Join(root, "sdk-dev") + "\n", "replace example.com/pinned v1.0.0 => example.com/pinned v1.0.1\n"} {
		if !strings.Contains(gw, s) {
			t.Errorf("no %q in\n%s", s, gw)
		}
	}
	if strings.Contains(gw, "/opt/xbin/sdk") {
		t.Errorf("the go.work's own SDK replace is the one:\n%s", gw)
	}

	// one that leaves the workspace through a symlink: an error, nothing read
	outside := filepath.Join(t.TempDir(), "go.work")
	wsFiles(t, filepath.Dir(outside), map[string]string{"go.work": "go 1.24\n\nreplace a => ./b\n"})
	_ = os.Remove(filepath.Join(root, "go.work"))
	if err := os.Symlink(outside, filepath.Join(root, "go.work")); err != nil {
		t.Fatal(err)
	}
	if rw, err := ReadRootWork(root); err == nil || rw != nil {
		t.Errorf("a go.work outside the workspace was read: %+v %v", rw, err)
	}
}

// Import scanning reads regular Go files only, never blocks on a FIFO,
// and never follows a link out of the module.
func TestScanImportsSafe(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	wsFiles(t, root, map[string]string{
		"m/go.mod":            "module m\n",
		"m/ok.go":             "package m\n\nimport _ \"seen\"\n",
		"m/.hidden/h.go":      "package h\n\nimport _ \"hidden\"\n",
		"m/_under/u.go":       "package u\n\nimport _ \"under\"\n",
		"m/node_modules/n.go": "package n\n\nimport _ \"nodemods\"\n",
	})
	wsFiles(t, outside, map[string]string{"o.go": "package o\n\nimport _ \"outside\"\n"})
	if err := os.Symlink(filepath.Join(outside, "o.go"), filepath.Join(root, "m", "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "m", "fifo.go"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("fifo.go", filepath.Join(root, "m", "tofifo.go")); err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() { done <- scanImports(Module{Dir: filepath.Join(root, "m"), Root: root, Rel: "m"}) }()
	select {
	case got := <-done:
		if !reflect.DeepEqual(got, []string{"seen", "under"}) {
			t.Errorf("imports %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scanning blocked on a FIFO")
	}
	// a module directory reached through a symlink isn't read
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if got := scanImports(Module{Root: root, Rel: "linked"}); len(got) != 0 {
		t.Errorf("read through a linked module dir: %q", got)
	}
}

func TestModuleSubAndEntryModule(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/a/go.mod":             "module a\n",
		"apps/b/backend/go.mod":     "module b\n",
		"apps/c/go.mod":             "module c\n",
		"apps/c/server/go.mod":      "module c/server\n",
		"apps/c/server/cmd/main.go": "package main\n",
	})
	for rel, want := range map[string]string{"apps/a": "", "apps/b": "backend", "apps/c": ""} {
		if sub, ok := ModuleSub(root, rel); !ok || sub != want {
			t.Errorf("ModuleSub(%s) = %q %v", rel, sub, ok)
		}
	}
	if _, ok := ModuleSub(root, "apps/none"); ok {
		t.Error("ModuleSub of a tile without go.mod")
	}
	for _, tc := range []struct{ rel, entry, want string }{
		{"apps/a", "./backend", ""},
		{"apps/b", "./backend", "backend"},
		{"apps/b", "./backend/main.go", "backend"},
		{"apps/c", "./server/cmd", "server"},
		{"apps/c", ".", ""},
	} {
		if sub, ok := EntryModule(root, tc.rel, tc.entry); !ok || sub != tc.want {
			t.Errorf("EntryModule(%s, %s) = %q %v, want %q", tc.rel, tc.entry, sub, ok, tc.want)
		}
	}
	for _, entry := range []string{"example.com/x/cmd", "../a", "./../a"} {
		if sub, ok := EntryModule(root, "apps/b", entry); ok {
			t.Errorf("EntryModule(%s) = %q", entry, sub)
		}
	}
}
