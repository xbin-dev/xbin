package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// The go.work the upgrade check lists the shared graph with is the root
// go.work as every build used it before D166: xbind's (every component's
// module and the SDK), or a hand-managed one's lines — absolute either way.
func TestSharedWork(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"xbin.json":             `{"schema":1}`,
		"apps/a/xbin.json":      `{"runtime":"go"}`,
		"apps/a/go.mod":         "module a\n\ngo 1.22\n",
		"apps/b/xbin.json":      `{"runtime":"go"}`,
		"apps/b/backend/go.mod": "module b\n\ngo 1.22\n",
	})
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SharedWork(reg, "sdk-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := workMarker + "\n\ngo 1.24\n\nuse (\n\t" + filepath.Join(root, "apps/a") + "\n\t" + filepath.Join(root, "apps/b/backend") +
		"\n)\n\nreplace " + SDKModule + " => " + filepath.Join(root, "sdk-dev") + "\n"
	if string(b) != want {
		t.Errorf("xbind's:\n%s\nwant\n%s", b, want)
	}
	// what the root go.work holds when xbind wrote it: the same modules
	if err := GoWork(reg, "sdk-dev"); err != nil {
		t.Fatal(err)
	}
	if b2, _ := SharedWork(reg, "sdk-dev", nil); string(b2) != want {
		t.Errorf("with xbind's root go.work on disk:\n%s", b2)
	}

	wsFiles(t, root, map[string]string{"go.work": "go 1.25\n\ntoolchain go1.25.4\n\ngodebug panicnil=1\n\nuse ./apps/a\n\nreplace example.com/x => ./forks/x\n"})
	b, err = SharedWork(reg, "sdk-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"\ngo 1.25\n", "\ntoolchain go1.25.4\n", "\ngodebug panicnil=1\n", "\t" + filepath.Join(root, "apps/a") + "\n",
		"replace example.com/x => " + filepath.Join(root, "forks/x") + "\n"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("hand-managed: no %q in\n%s", s, b)
		}
	}
	if strings.Contains(string(b), "apps/b") || strings.Contains(string(b), SDKModule) {
		t.Errorf("hand-managed: its own lines only\n%s", b)
	}
	if got := parseModLines(b); len(got) == 0 {
		t.Error("unparsable")
	}

	empty := t.TempDir()
	wsFiles(t, empty, map[string]string{"xbin.json": `{"schema":1}`})
	ereg, err := registry.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SharedWork(ereg, "", nil); err != ErrNoSharedWork {
		t.Errorf("no Go module: %v", err)
	}
}

// covers G1 review finding 1 — shapes D166 made buildable that a go.work
// using every module can't hold: a module at `go 1.24.0` (what `go mod
// init` writes; "requires go >= 1.24.0, but go.work lists go 1.24"), a
// copied tile declaring another's module path ("appears multiple times in
// workspace"), a tile declaring the SDK's path, which the go.work replaces
// ("replaced at all versions"). The shared go.work raises its go line,
// uses only the namesake the tile's build uses (the first when it uses
// neither) and drops the SDK's namesake — and the go command loads it.
func TestSharedWorkLoads(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"xbin.json":           `{"schema":1}`,
		"apps/a/xbin.json":    `{"runtime":"go"}`,
		"apps/a/go.mod":       "module a\n\ngo 1.22\n",
		"apps/copy/xbin.json": `{"runtime":"go"}`,
		"apps/copy/go.mod":    "module a\n\ngo 1.22\n",
		"apps/c/xbin.json":    `{"runtime":"go"}`,
		"apps/c/go.mod":       "module c\n\ngo 1.24.0\n",
		"apps/fake/xbin.json": `{"runtime":"go"}`,
		"apps/fake/go.mod":    "module " + SDKModule + "\n\ngo 1.22\n",
		"sdk-dev/go.mod":      "module " + SDKModule + "\n\ngo 1.22\n",
	})
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, cp, c := filepath.Join(root, "apps/a"), filepath.Join(root, "apps/copy"), filepath.Join(root, "apps/c")
	gobin, _ := exec.LookPath("go")
	for _, tc := range []struct {
		name   string
		prefer func(string) bool
		uses   []string
	}{
		{"a's", func(d string) bool { return d == a }, []string{a, c}},
		{"the copy's", func(d string) bool { return d == cp }, []string{c, cp}},
		{"neither's", nil, []string{a, c}},
		{"another's", func(d string) bool { return d == c }, []string{a, c}},
	} {
		b, err := SharedWork(reg, "sdk-dev", tc.prefer)
		if err != nil {
			t.Fatal(err)
		}
		var uses []string
		goLine := ""
		for _, l := range parseModLines(b) {
			switch l.verb {
			case "use":
				uses = append(uses, l.args[0])
			case "go":
				goLine = l.args[0]
			}
		}
		if !reflect.DeepEqual(uses, tc.uses) || goLine != "1.24.0" {
			t.Errorf("%s build: go %s, uses %q\n%s", tc.name, goLine, uses, b)
		}
		if gobin == "" {
			continue
		}
		gw := filepath.Join(t.TempDir(), "go.work")
		if err := os.WriteFile(gw, b, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(gobin, "list", "-m")
		cmd.Dir = c
		cmd.Env = append(os.Environ(), "GOWORK="+gw, "GOPROXY=off", "GOTOOLCHAIN=local", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s build: the go command refuses the shared go.work: %s\n%s", tc.name, out, b)
		}
	}

	// a hand-managed go.work's go line, raised the same way
	wsFiles(t, root, map[string]string{"go.work": "go 1.23\n\nuse (\n\t./apps/a\n\t./apps/c\n)\n"})
	b, err := SharedWork(reg, "sdk-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\ngo 1.24.0\n") {
		t.Errorf("hand-managed, a module at go 1.24.0:\n%s", b)
	}
}

// Inputs changes with what decides a build's versions — its go.work and a
// used module's go.mod — and with nothing else.
func TestWorkInputs(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/a/go.mod":  "module a\n\ngo 1.22\n\nrequire example.com/x v1.0.0\n",
		"apps/a/main.go": "package main\n",
		"lib/go.mod":     "module lib\n\ngo 1.22\n",
	})
	w := Work{GoWork: []byte("go 1.24\n"), Uses: []Module{
		{Dir: filepath.Join(root, "apps/a"), Root: root, Rel: "apps/a"},
		{Dir: filepath.Join(root, "lib"), Root: root, Rel: "lib"},
	}}
	in := w.Inputs()
	if w.Inputs() != in || len(in) != 64 {
		t.Fatalf("not stable: %s", in)
	}
	wsFiles(t, root, map[string]string{"apps/a/main.go": "package main\n\nfunc main() {}\n"})
	if w.Inputs() != in {
		t.Error("code changed the inputs")
	}
	wsFiles(t, root, map[string]string{"lib/go.mod": "module lib\n\ngo 1.22\n\nrequire example.com/x v1.1.0\n"})
	if w.Inputs() == in {
		t.Error("a used module's go.mod didn't change the inputs")
	}
	in = w.Inputs()
	w.GoWork = []byte("go 1.24.0\n")
	if w.Inputs() == in {
		t.Error("the go.work didn't change the inputs")
	}
}

func TestDirectRequires(t *testing.T) {
	root := t.TempDir()
	wsFiles(t, root, map[string]string{"go.mod": `module x

go 1.24

require modernc.org/sqlite v1.34.5
require golang.org/x/sys v0.22.0 // indirect

require (
	github.com/a/direct v1.0.0
	github.com/b/indirect v1.0.0 // indirect
	github.com/c/commented v1.0.0 // pinned for a bug
)
`})
	got := DirectRequires(Module{Dir: root, Root: root})
	want := map[string]bool{"modernc.org/sqlite": true, "github.com/a/direct": true, "github.com/c/commented": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DirectRequires = %v", got)
	}
	if DirectRequires(Module{Dir: root, Root: root, Rel: "none"}) != nil {
		t.Error("no go.mod: nil")
	}
}

func TestPinModule(t *testing.T) {
	gw := []byte("go 1.24.0\n\nuse (\n\t/w/apps/a\n)\n")
	if got := WorkGoLine(gw); got != "1.24.0" {
		t.Errorf("WorkGoLine %q", got)
	}
	if got := WorkGoLine([]byte("use ./a\n")); got != "" {
		t.Errorf("no go line: %q", got)
	}
	pin := string(PinGoMod("1.24.0", []Pin{{"modernc.org/sqlite", "v1.39.1"}, {"golang.org/x/net", "v0.46.0"}}))
	m := parseGoMod([]byte(pin))
	if m.Path != PinModulePath || m.Go != "1.24.0" || !reflect.DeepEqual(m.Requires, map[string]string{"modernc.org/sqlite": "v1.39.1", "golang.org/x/net": "v0.46.0"}) {
		t.Errorf("pin module %+v\n%s", m, pin)
	}
	with := WithUse(gw, "/w/.xbin/cache/tile/k/versions/pin")
	var uses []string
	for _, l := range parseModLines(with) {
		if l.verb == "use" {
			uses = append(uses, l.args[0])
		}
	}
	if !reflect.DeepEqual(uses, []string{"/w/apps/a", "/w/.xbin/cache/tile/k/versions/pin"}) {
		t.Errorf("uses %q in\n%s", uses, with)
	}
}
