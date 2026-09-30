package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// D166 — writeBuildWork puts a build's go.work in a directory of its own
// named by its content, beside a go.work.sum only builds of that same
// workspace write, seeded from the workspace's go.work.sum and the tile's
// newest one (from before D166 too). What a build left in the work
// directory is never written through — xbind writes nothing through a
// sandbox-written path (D78) — and old workspaces are swept.
func TestWriteBuildWork(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, ".xbin", "cache", "tile", "k1", "work")
	if err := os.WriteFile(filepath.Join(root, "go.work.sum"), []byte("ws v1.0.0 h1:w=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a go.work.sum from before D166, in work/ itself
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "go.work.sum"), []byte("legacy v1.0.0 h1:l=\nws v1.0.0 h1:w=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gw, err := writeBuildWork(work, []byte("go 1.24\n\nuse /a\n"), root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(gw)) != work || filepath.Base(gw) != "go.work" {
		t.Fatalf("go.work at %s", gw)
	}
	if b, _ := os.ReadFile(gw); string(b) != "go 1.24\n\nuse /a\n" {
		t.Errorf("go.work %q", b)
	}
	if b, _ := os.ReadFile(gw + ".sum"); string(b) != "legacy v1.0.0 h1:l=\nws v1.0.0 h1:w=\n" {
		t.Errorf("seeded go.work.sum %q", b)
	}
	// the same content: the same place, its sum kept as a build left it
	if err := os.WriteFile(gw+".sum", []byte("added v0.1.0 h1:a=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := writeBuildWork(work, []byte("go 1.24\n\nuse /a\n"), root); err != nil || again != gw {
		t.Fatalf("again: %s %v", again, err)
	}
	if b, _ := os.ReadFile(gw + ".sum"); string(b) != "added v0.1.0 h1:a=\n" {
		t.Errorf("the build's sums were replaced: %q", b)
	}
	// another workspace (a deployment's checkpoint): its own place, seeded
	// with the newest sums the tile's builds wrote
	now := time.Now().Add(time.Minute)
	_ = os.Chtimes(gw+".sum", now, now)
	gw2, err := writeBuildWork(work, []byte("go 1.24\n\nuse /b\n"), root)
	if err != nil || gw2 == gw {
		t.Fatalf("another workspace: %s %v", gw2, err)
	}
	if b, _ := os.ReadFile(gw2 + ".sum"); string(b) != "added v0.1.0 h1:a=\nws v1.0.0 h1:w=\n" {
		t.Errorf("seeded from the newest: %q", b)
	}
	if b, _ := os.ReadFile(gw); string(b) != "go 1.24\n\nuse /a\n" {
		t.Errorf("the other workspace's go.work changed: %q", b)
	}

	// what a build left: a link as go.work is replaced, never written through;
	// a link as go.work.sum is left alone, nothing written through it
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(gw)
	if err := os.Symlink(victim, gw); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBuildWork(work, []byte("go 1.24\n\nuse /a\n"), root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious\n" {
		t.Fatalf("written through the build's link: %q", b)
	}
	if fi, err := os.Lstat(gw); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("go.work isn't a regular file: %v %v", fi, err)
	}
	made := filepath.Join(t.TempDir(), "made")
	content3 := []byte("go 1.24\n\nuse /c\n")
	dir3 := filepath.Dir(gwPath(t, work, content3, root))
	_ = os.RemoveAll(dir3)
	if err := os.MkdirAll(dir3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(made, filepath.Join(dir3, "go.work.sum")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBuildWork(work, content3, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(made); !os.IsNotExist(err) {
		t.Fatalf("the seed was written through a link: %v", err)
	}
	// a workspace's directory swapped for a link to elsewhere: replaced
	elsewhere := t.TempDir()
	_ = os.RemoveAll(dir3)
	if err := os.Symlink(elsewhere, dir3); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBuildWork(work, content3, root); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("written through a linked workspace dir: %v", ents)
	}
	if !realDir(dir3) {
		t.Fatal("the workspace dir isn't a directory of its own")
	}

	// no build has written a workspace for a week: swept, with the legacy files
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, p := range []string{filepath.Dir(gw2), filepath.Join(work, "go.work.sum")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeBuildWork(work, []byte("go 1.24\n\nuse /a\n"), root); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Dir(gw2), filepath.Join(work, "go.work.sum")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s wasn't swept: %v", p, err)
		}
	}
	if _, err := os.Stat(gw); err != nil {
		t.Errorf("the current workspace was swept: %v", err)
	}
}

// gwPath is where writeBuildWork puts content's go.work.
func gwPath(t *testing.T, work string, content []byte, root string) string {
	t.Helper()
	gw, err := writeBuildWork(work, content, root)
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

// covers D166 — the race F6 found: a new Go tile's first build could run
// before the root go.work listed its module ("go: no modules were found in
// the current workspace"), and the failure stuck until the code changed. A
// build's go.work is rendered from the registry and the tile's own files at
// build time: a tile the root go.work doesn't list yet builds with its own
// module used, and a tile holding no module gets GOWORK=off, never the root
// go.work. The fake go records the GOWORK it ran with.
func TestBuildRightAfterRegistration(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for the fake go")
	}
	// the build asks the host toolchain for its GOROOT (once per process):
	// the real one, before the fake go is on PATH
	_, _ = hostToolchain()
	root := t.TempDir()
	fake := t.TempDir()
	seen := filepath.Join(fake, "seen")
	writeExec(t, filepath.Join(fake, "go"), "#!/bin/sh\n{ echo \"GOWORK=$GOWORK\"; [ -f \"$GOWORK\" ] && cat \"$GOWORK\"; } > "+seen+"\n: > \"$3\"\n")
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XBIN_SDK_PATH", "/opt/xbin/sdk")
	write := func(p, s string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// the root go.work as it stood before the tile: xbind's, listing an older tile
	write(filepath.Join(root, "go.work"), "// Code generated by xbind; DO NOT EDIT (remove this line to take ownership).\n\ngo 1.24\n\nuse (\n\t./apps/old\n)\n\nreplace github.com/xbin-dev/xbin/sdk => /opt/xbin/sdk\n")
	write(filepath.Join(root, "apps/old/xbin.json"), `{"runtime":"go"}`)
	write(filepath.Join(root, "apps/old/go.mod"), "module old\n\nreplace golang.org/x/sys => ./evil\n")
	write(filepath.Join(root, "apps/new/xbin.json"), `{"runtime":"go"}`)
	write(filepath.Join(root, "apps/new/go.mod"), "module new\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")
	write(filepath.Join(root, "apps/new/backend/main.go"), "package main\n\nfunc main() {}\n")
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Reg: reg}
	c, ok := reg.Component("apps/new")
	if !ok {
		t.Fatal("apps/new isn't registered")
	}
	if _, err := r.build(c); err != nil {
		t.Fatalf("the first build: %v", err)
	}
	b, _ := os.ReadFile(seen)
	got := string(b)
	gw, _, _ := strings.Cut(strings.TrimPrefix(got, "GOWORK="), "\n")
	if want := filepath.Join(root, ".xbin", "cache", "tile", util.CompKey("apps/new"), "work"); filepath.Dir(filepath.Dir(gw)) != want {
		t.Fatalf("GOWORK %q, want the tile's own under %s", gw, want)
	}
	for _, s := range []string{"\t" + filepath.Join(root, "apps/new") + "\n", "replace github.com/xbin-dev/xbin/sdk => /opt/xbin/sdk\n"} {
		if !strings.Contains(got, s) {
			t.Errorf("no %q in the build's go.work:\n%s", s, got)
		}
	}
	if strings.Contains(got, "apps/old") || strings.Contains(got, "evil") {
		t.Errorf("another tile reached the build:\n%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "go.work")); strings.Contains(string(b), "apps/new") {
		t.Fatal("the test's premise: the root go.work must not list the new tile")
	}

	// a Go tile holding no module: GOWORK=off
	write(filepath.Join(root, "apps/bare/xbin.json"), `{"runtime":"go"}`)
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	bare, ok := reg.Component("apps/bare")
	if !ok {
		t.Fatal("apps/bare isn't registered")
	}
	if _, err := r.build(bare); err != nil {
		t.Fatalf("build: %v", err)
	}
	if b, _ := os.ReadFile(seen); string(b) != "GOWORK=off\n" {
		t.Errorf("a tile without go.mod ran with %q", b)
	}
}
