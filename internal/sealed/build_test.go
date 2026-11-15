package sealed

import (
	"debug/elf"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if len(ents) != 2 { // the build and the manifest that lists it
		t.Errorf("stale builds survived: %v", ents)
	}
	man, err := ReadManifest(out)
	sum, size, _ := FileSum(filepath.Join(out, want[0]))
	sdkSum, _ := TreeDigest("../../sdk")
	if e := man.Builds[want[0]]; err != nil || len(man.Builds) != 1 || e.SHA256 != sum || e.Size != size || e.Source != set.Components()[0].Digest ||
		e.Component != "xbin/fix" || e.SDK != sdkSum || e.Go != runtime.Version() || e.OS != "linux" || e.Arch != runtime.GOARCH {
		t.Errorf("the manifest: %+v (%v), want the build's hash %s, its source, the SDK %s, %s", man, err, sum, sdkSum, runtime.Version())
	}
	if n, err := set.CheckBuilds(out, "../../sdk"); err != nil || n != 1 {
		t.Errorf("CheckBuilds on a fresh build: %d %v", n, err)
	}
	if _, err := set.CheckBuilds(out, t.TempDir()); err == nil || !strings.Contains(err.Error(), "another xbin SDK") {
		t.Errorf("CheckBuilds against another SDK tree: %v", err)
	}
	// …for the bundle's OS and architecture, by the release's toolchain
	if n, err := set.CheckBuilds(out, "../../sdk", BuildOptions{GOOS: "linux", GOARCH: runtime.GOARCH}); err != nil || n != 1 {
		t.Errorf("CheckBuilds for its own target: %d %v", n, err)
	}
	if _, err := set.CheckBuilds(out, "../../sdk", BuildOptions{GOOS: "linux", GOARCH: other}); err == nil || !strings.Contains(err.Error(), "was built for linux/"+runtime.GOARCH) {
		t.Errorf("CheckBuilds for another architecture: %v", err)
	}
	stale, _ := ReadManifest(out)
	entry := stale.Builds[want[0]]
	entry.Go = "go1.0"
	stale.Builds[want[0]] = entry
	sb, _ := json.Marshal(stale)
	fresh, _ := os.ReadFile(filepath.Join(out, ManifestName))
	_ = os.WriteFile(filepath.Join(out, ManifestName), sb, 0o644)
	if _, err := set.CheckBuilds(out, "../../sdk", BuildOptions{GOOS: "linux", GOARCH: runtime.GOARCH}); err == nil || !strings.Contains(err.Error(), "was built by go1.0") {
		t.Errorf("CheckBuilds with builds of another toolchain: %v", err)
	}
	_ = os.WriteFile(filepath.Join(out, ManifestName), fresh, 0o644)

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

	// What runs is verified where a backend is spawned, not only at boot —
	// and not on a request an instance already serves (Admit stays cheap).
	s, err = Open([]Spec{buildable("one")}, Options{Workspace: ws, Isolate: true, BinDir: out, Release: "v"})
	if err != nil {
		t.Fatal(err)
	}
	c := s.Components()[0]
	if why, err := s.Admit("xbin/fix"), s.Verify("xbin/fix"); why != "" || err != nil {
		t.Fatalf("a sound install: %q %v", why, err)
	}
	// its tree, changed on disk between two starts: written again
	page := filepath.Join(c.Dir, "index.html")
	if err := os.Chmod(page, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "extra.js"), []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := s.Admit("xbin/fix"); why != "" {
		t.Fatalf("a request met a changed tree: %s", why)
	}
	if b, _ := os.ReadFile(page); string(b) != "planted" {
		t.Error("Admit (every request) rewrote the tree: the check belongs to a spawn")
	}
	if err := s.Verify("xbin/fix"); err != nil {
		t.Fatalf("a changed tree should be written again, not refused: %v", err)
	}
	if b, _ := os.ReadFile(page); string(b) != string(c.Files["index.html"]) {
		t.Errorf("the tree was not written again: index.html is %q", b)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "extra.js")); err == nil {
		t.Error("a planted file survived in its tree")
	}
	// its build, unreadable for a moment (an install replacing bin/sealed):
	// that start fails, the next one runs — nothing latches
	bin := filepath.Join(out, want[0])
	if err := os.Rename(bin, bin+".away"); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify("xbin/fix"); err == nil || !strings.Contains(err.Error(), "can't be read") {
		t.Errorf("a spawn with its build gone: %v", err)
	}
	if err := os.Rename(bin+".away", bin); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify("xbin/fix"); err != nil {
		t.Errorf("the next spawn, its build back: %v (a failed check latched)", err)
	}
	if h, held := s.Hold("xbin/fix"); held {
		t.Errorf("a failed check became a hold: %+v", h)
	}
	// its build, replaced after boot: not run, at any spawn, and no hold
	orig, _ := os.ReadFile(bin)
	if err := os.WriteFile(bin, append([]byte("#!/bin/sh\n# planted\n"), orig...), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Verify("xbin/fix"); err == nil || !strings.Contains(err.Error(), "not the file this xbin install shipped") {
			t.Errorf("a replaced build was passed: %v", err)
		}
	}
	if why := s.Admit("xbin/fix"); why != "" {
		t.Errorf("a request is refused for it: %q (the spawn is what fails)", why)
	}
	// a boot on that install holds it: the file is definitely not the one listed
	s, err = Open([]Spec{buildable("one")}, Options{Workspace: ws, Isolate: true, BinDir: out, Release: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if h, held := s.Hold("xbin/fix"); !held || !strings.Contains(h.Reason, "SHA-256 differs") {
		t.Errorf("a boot with a replaced build: %+v %v", h, held)
	}
	if _, err := set.CheckBuilds(out, "../../sdk"); err == nil || !strings.Contains(err.Error(), "SHA-256 differs") {
		t.Errorf("CheckBuilds with a replaced build: %v", err)
	}
	// a boot while the file can't be read holds nothing: the spawn decides
	if err := os.WriteFile(bin, orig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o111); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		s, err = Open([]Spec{buildable("one")}, Options{Workspace: ws, Isolate: true, BinDir: out, Release: "v"})
		if err != nil {
			t.Fatal(err)
		}
		if h, held := s.Hold("xbin/fix"); held {
			t.Errorf("a boot that couldn't read its build holds it: %+v", h)
		}
		if err := s.Verify("xbin/fix"); err == nil {
			t.Error("a spawn passed a build it couldn't read")
		}
	}
	_ = os.Chmod(bin, 0o755)
}
