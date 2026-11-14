package sealed

import (
	"debug/elf"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

// buildable is a sealed component whose backend compiles on the SDK alone.
func buildable(body string) Spec {
	return Spec{Name: "fix", Authored: "apps/fix", Source: fstest.MapFS{
		"xbin.json":   {Data: []byte(`{"runtime":"go"}`)},
		"index.html":  {Data: []byte("<html>" + body + "</html>")},
		"go.mod.tile": {Data: []byte("module fix\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")},
		"backend/main.go": {Data: []byte(`package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() { xbin.Serve(http.NewServeMux()) }
`)},
	}}
}

// The release's build: one static binary per Go component, named by the
// source's digest, for the target asked; what else is in the directory
// goes, so a bundle never carries another source's build; and the install
// that holds it runs it (Open finds it by that name).
func TestBuildNamesAndTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a backend twice")
	}
	out := t.TempDir()
	static := Spec{Name: "page", Source: fstest.MapFS{"xbin.json": {Data: []byte(`{}`)}, "index.html": {Data: []byte("x")}}}
	set, err := Load([]Spec{buildable("one"), static})
	if err != nil {
		t.Fatal(err)
	}
	want := set.BuildNames()
	if len(want) != 1 || want[0] != "fix-"+set.Components()[0].ID() {
		t.Fatalf("BuildNames = %v: one per Go backend, none for a static component", want)
	}
	_ = os.WriteFile(filepath.Join(out, "fix-000000000000000000000000"), []byte("stale"), 0o755)
	_ = os.WriteFile(filepath.Join(out, "gone-0123"), []byte("stale"), 0o755)

	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	for _, arch := range []string{other, runtime.GOARCH} { // the same name, whichever target: rebuilt each time
		have, err := set.Build(BuildOptions{Out: out, SDK: "../../sdk", GOOS: "linux", GOARCH: arch})
		if err != nil {
			t.Fatal(err)
		}
		if len(have) != 1 || have[0] != want[0] {
			t.Fatalf("after a build the directory holds %v, want %v alone", have, want)
		}
		f, err := elf.Open(filepath.Join(out, want[0]))
		if err != nil {
			t.Fatal(err)
		}
		machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[arch]
		if f.Machine != machine {
			t.Errorf("built for %s: the binary is %s", arch, f.Machine)
		}
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				t.Error("the backend is dynamically linked: it must run on any sandbox rootfs")
			}
		}
		f.Close()
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 {
		t.Errorf("stale builds survived: %v", ents)
	}

	// the install that holds it runs it; other code's build is not it
	ws := t.TempDir()
	s, err := Open([]Spec{buildable("one")}, Options{Workspace: ws, Isolate: true, BinDir: out, Release: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if h, held := s.Hold("xbin/fix"); held || s.Components()[0].Prebuilt != filepath.Join(out, want[0]) {
		t.Errorf("the install's build isn't picked up: %+v, prebuilt %q", h, s.Components()[0].Prebuilt)
	}
	s, err = Open([]Spec{buildable("two")}, Options{Workspace: t.TempDir(), Isolate: true, BinDir: out, Release: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if h, held := s.Hold("xbin/fix"); !held || h.Code != HoldNoBuild {
		t.Errorf("a build of other code was accepted: %+v", h)
	}
}

func TestBuildFailureSaysWhy(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	sp := buildable("x")
	sp.Source.(fstest.MapFS)["backend/main.go"] = &fstest.MapFile{Data: []byte("package main\n\nfunc main() { undefined() }\n")}
	set, err := Load([]Spec{sp})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := set.Build(BuildOptions{Out: out, SDK: "../../sdk"}); err == nil {
		t.Fatal("a backend that doesn't compile built")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Errorf("a failed build left %v", ents)
	}
	if _, err := set.Build(BuildOptions{Out: out, SDK: filepath.Join(out, "nosdk")}); err == nil {
		t.Error("a build without the SDK started")
	}
}
