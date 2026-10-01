//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run GoVersions
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
package runner

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// proxyModule lays out one module version as a GOPROXY serves it: its
// .info, .mod and .zip beneath dir, and its version in @v/list.
func proxyModule(t *testing.T, dir, path, version, gomod string, files map[string]string) {
	t.Helper()
	at := filepath.Join(dir, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, s string) {
		if err := os.WriteFile(filepath.Join(at, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(version+".info", fmt.Sprintf(`{"Version":%q,"Time":"2026-01-02T03:04:05Z"}`, version))
	put(version+".mod", gomod)
	f, err := os.Create(filepath.Join(at, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, s := range map[string]string{"go.mod": gomod} {
		files[name] = s
	}
	for name, s := range files {
		w, err := zw.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	list, _ := os.ReadFile(filepath.Join(at, "list"))
	put("list", string(list)+version+"\n")
}

// covers D166's upgrade check (G1) — confined, with real tiles and the go
// command: tile a requires example.com/top v1.1.0, whose example.com/base
// v1.1.0 pulls in example.com/extra; tile b requires top v1.0.0. Under the
// shared go.work b linked a's versions; with its own go.mod it links its
// own, lower ones, and drops extra. The check finds the one line that
// keeps what b had — top's, b's own direct requirement, which lifts base
// and brings extra back — and names it in the admin alert; a is
// unaffected. Tile c, a copy of a at `go 1.24.0` (as `go mod init` writes
// it), is a shape the shared go.work can't hold as it was — two modules
// `a`, one newer than its go line — and breaks no tile's check (G1 review
// finding 1). The lists run confined and write nothing of the workspace.
// When b's go.mod catches up, b's next build re-checks it and the line
// goes. Isolation off, the check finds the same.
func TestConfinedGoVersionsAlert(t *testing.T) {
	fs := ckRootfs(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	root := t.TempDir()
	w := func(rel, s string) { ckWrite(t, filepath.Join(root, filepath.FromSlash(rel)), s) }
	// a module proxy of its own, inside the workspace so the sandbox sees it
	proxy := filepath.Join(root, "proxy")
	proxyModule(t, proxy, "example.com/extra", "v1.0.0", "module example.com/extra\n\ngo 1.22\n",
		map[string]string{"extra.go": "package extra\n\nconst V = \"extra\"\n"})
	proxyModule(t, proxy, "example.com/base", "v1.0.0", "module example.com/base\n\ngo 1.22\n",
		map[string]string{"base.go": "package base\n\nconst V = \"base v1.0.0\"\n"})
	proxyModule(t, proxy, "example.com/base", "v1.1.0", "module example.com/base\n\ngo 1.22\n\nrequire example.com/extra v1.0.0\n",
		map[string]string{"base.go": "package base\n\nimport \"example.com/extra\"\n\nconst V = \"base v1.1.0 \" + extra.V\n"})
	proxyModule(t, proxy, "example.com/top", "v1.0.0", "module example.com/top\n\ngo 1.22\n\nrequire example.com/base v1.0.0\n",
		map[string]string{"top.go": "package top\n\nimport \"example.com/base\"\n\nconst V = \"top v1.0.0, \" + base.V\n"})
	proxyModule(t, proxy, "example.com/top", "v1.1.0", "module example.com/top\n\ngo 1.22\n\nrequire example.com/base v1.1.0\n",
		map[string]string{"top.go": "package top\n\nimport \"example.com/base\"\n\nconst V = \"top v1.1.0, \" + base.V\n"})
	main := "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/top\"\n)\n\nfunc main() { fmt.Println(top.V) }\n"
	w("apps/a/xbin.json", `{"runtime":"go"}`)
	w("apps/a/go.mod", "module a\n\ngo 1.22\n\nrequire example.com/top v1.1.0\n\nrequire (\n\texample.com/base v1.1.0 // indirect\n\texample.com/extra v1.0.0 // indirect\n)\n")
	w("apps/a/backend/main.go", main)
	w("apps/b/xbin.json", `{"runtime":"go"}`)
	w("apps/b/go.mod", "module b\n\ngo 1.22\n\nrequire example.com/top v1.0.0\n\nrequire example.com/base v1.0.0 // indirect\n")
	w("apps/b/backend/main.go", main)
	w("apps/c/xbin.json", `{"runtime":"go"}`)
	w("apps/c/go.mod", "module a\n\ngo 1.24.0\n\nrequire example.com/top v1.1.0\n\nrequire (\n\texample.com/base v1.1.0 // indirect\n\texample.com/extra v1.0.0 // indirect\n)\n")
	w("apps/c/backend/main.go", main)
	// an earlier xbind built b
	w(".xbin/build/"+util.CompKey("apps/b")+"/bin", "an old binary")

	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.GoWork(reg, ""); err != nil { // the root go.work, as xbind writes it
		t.Fatal(err)
	}
	gw, _ := os.ReadFile(filepath.Join(root, "go.work"))

	confine.Configure(fs)
	defer confine.Configure("")
	if _, err := hostToolchain(); err != nil {
		t.Skip("no host toolchain:", err)
	}
	saved := tcVal
	tcVal.goproxy, tcVal.download = "file://"+proxy, "" // only this proxy: nothing from the network or the host's cache
	t.Cleanup(func() { tcVal = saved })
	t.Setenv("GONOSUMDB", "example.com")
	t.Setenv("GOFLAGS", "")

	r := &Runner{Root: root, Isolate: true, Rootfs: fs, Reg: reg}
	g := &GoVersions{Run: r, Path: filepath.Join(root, "data", "go-build-versions.json"), Version: "v0.3.65"}
	r.GoVersions = g
	if err := g.Boot(); err != nil || !g.due {
		t.Fatalf("an upgraded workspace: due=%v %v", g.due, err)
	}
	_ = os.Remove(filepath.Join(root, ".xbin", "build", util.CompKey("apps/b"), "bin")) // not an object file go build would replace
	runPass(g, true)
	rep := g.Report()
	if len(rep.Errors) > 0 || rep.WorkspaceError != "" {
		t.Fatalf("errors: %+v %s", rep.Errors, rep.WorkspaceError)
	}
	if st := readGoVersionsState(t, g); len(st.Baseline) != 3 || st.Baseline["apps/a"] == nil || st.Baseline["apps/c"] == nil {
		t.Errorf("baselines: %+v", st.Baseline)
	}
	if !rep.Done || len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/b" {
		t.Fatalf("report %+v", rep)
	}
	b := rep.Tiles[0]
	if !b.Minimal || !reflect.DeepEqual(b.Require, []string{"example.com/top v1.1.0"}) {
		t.Errorf("b's lines %q minimal=%v", b.Require, b.Minimal)
	}
	wantChanges := []VersionChange{
		{Module: "example.com/base", Had: "v1.1.0", Now: "v1.0.0"},
		{Module: "example.com/extra", Had: "v1.0.0"},
		{Module: "example.com/top", Had: "v1.1.0", Now: "v1.0.0"},
	}
	if !reflect.DeepEqual(b.Changes, wantChanges) {
		t.Errorf("b's changes %+v", b.Changes)
	}
	msg, ok := g.Alert()
	if want := "apps/b builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require example.com/top v1.1.0` to apps/b's go.mod to keep what it had"; !ok || msg != want {
		t.Errorf("alert %q", msg)
	}
	// the lists wrote nothing of the workspace's
	if cur, _ := os.ReadFile(filepath.Join(root, "go.work")); string(cur) != string(gw) {
		t.Errorf("the workspace's go.work changed:\n%s", cur)
	}
	for _, p := range []string{"go.work.sum", "apps/b/go.sum", "apps/a/go.sum"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Errorf("a list wrote %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".xbin", "cache", "tile", util.CompKey("apps/b"), "versions", "go.work.sum")); err != nil {
		t.Errorf("the lists' own go.work.sum: %v", err)
	}

	// b's go.mod catches up, the alert's line added as it says (the go
	// command takes the higher of two requirements): its next build
	// re-checks it, and the line goes
	w("apps/b/go.mod", "module b\n\ngo 1.22\n\nrequire example.com/top v1.0.0\n\nrequire example.com/base v1.0.0 // indirect\n\nrequire example.com/top v1.1.0\n")
	bc, _ := reg.Component("apps/b")
	bin, err := r.build(bc)
	if err != nil {
		if be, ok := err.(*BuildError); ok {
			t.Fatalf("build b: %s", be.Output)
		}
		t.Fatal(err)
	}
	if out, _ := exec.Command(bin).Output(); strings.TrimSpace(string(out)) != "top v1.1.0, base v1.1.0 extra" {
		t.Errorf("b's backend printed %q", out)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		g.mu.Lock()
		busy, left := g.busy["apps/b"], g.st.Tiles["apps/b"]
		g.mu.Unlock()
		if !busy && left == nil {
			break
		}
		if !busy || time.Now().After(deadline) {
			t.Fatalf("b's re-check left %+v (busy %v)", left, busy)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if msg, ok := g.Alert(); ok {
		t.Errorf("an alert after b caught up: %q", msg)
	}

	// isolation off: the same lists, run as the build runs then
	w("apps/b/go.mod", "module b\n\ngo 1.22\n\nrequire example.com/top v1.0.0\n\nrequire example.com/base v1.0.0 // indirect\n")
	confine.Configure("")
	t.Setenv("GOPROXY", "file://"+proxy)
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "mod"))
	t.Setenv("GOFLAGS", "-modcacherw")
	r2 := &Runner{Root: root, Reg: reg}
	g2 := &GoVersions{Run: r2, Path: filepath.Join(t.TempDir(), "state.json"), Version: "v0.3.65"}
	if err := g2.Boot(); err != nil || !g2.due { // b's binary: built
		t.Fatalf("isolation off, an upgraded workspace: due=%v %v", g2.due, err)
	}
	runPass(g2, true)
	rep2 := g2.Report()
	if len(rep2.Errors) > 0 || len(rep2.Tiles) != 1 || !reflect.DeepEqual(rep2.Tiles[0].Require, []string{"example.com/top v1.1.0"}) {
		t.Errorf("isolation off: %+v", rep2)
	}
}

// covers G1 review finding 8 — the accidental half of the shared versions,
// with the go command's own module graph pruning: tile t imports
// example.com/lib and example.com/net (requiring net v1.0.0); lib's go.mod
// requires example.com/tool, whose go.mod requires net v1.1.0 (and its
// crypto v1.1.0) — an edge t's pruned graph never loads, as t builds no
// package of tool. Tile u's `example.com/tool v1.0.0 // indirect` line makes
// tool a root of the shared graph, which un-prunes that edge: under the
// shared go.work t linked net v1.1.0 and crypto v1.1.0. The check finds the
// one line — net's, t's own direct requirement — and that line, through the
// pin module or added to t's go.mod, reproduces exactly what t linked under
// the shared go.work. Without u's line nothing changes: it was the line.
func TestConfinedGoVersionsUnprunedIndirect(t *testing.T) {
	fs := ckRootfs(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	root := t.TempDir()
	w := func(rel, s string) { ckWrite(t, filepath.Join(root, filepath.FromSlash(rel)), s) }
	proxy := filepath.Join(root, "proxy")
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		proxyModule(t, proxy, "example.com/crypto", v, "module example.com/crypto\n\ngo 1.22\n",
			map[string]string{"crypto.go": "package crypto\n\nconst V = \"crypto " + v + "\"\n"})
		proxyModule(t, proxy, "example.com/net", v, "module example.com/net\n\ngo 1.22\n\nrequire example.com/crypto "+v+"\n",
			map[string]string{"net.go": "package net\n\nimport \"example.com/crypto\"\n\nconst V = \"net " + v + " \" + crypto.V\n"})
	}
	proxyModule(t, proxy, "example.com/tool", "v1.0.0", "module example.com/tool\n\ngo 1.22\n\nrequire example.com/net v1.1.0\n\nrequire example.com/crypto v1.1.0 // indirect\n",
		map[string]string{"tool.go": "package tool\n\nconst V = \"tool\"\n", "nt/nt.go": "package nt\n\nimport \"example.com/net\"\n\nconst V = net.V\n"})
	proxyModule(t, proxy, "example.com/lib", "v1.0.0", "module example.com/lib\n\ngo 1.22\n\nrequire example.com/tool v1.0.0\n",
		map[string]string{"lib.go": "package lib\n\nconst V = \"lib\"\n", "gen/gen.go": "package gen\n\nimport \"example.com/tool\"\n\nconst V = \"gen \" + tool.V\n"})
	tGoMod := "module t\n\ngo 1.22\n\nrequire (\n\texample.com/lib v1.0.0\n\texample.com/net v1.0.0\n)\n\nrequire example.com/crypto v1.0.0 // indirect\n"
	w("apps/t/xbin.json", `{"runtime":"go"}`)
	w("apps/t/go.mod", tGoMod)
	w("apps/t/backend/main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/lib\"\n\t\"example.com/net\"\n)\n\nfunc main() { fmt.Println(lib.V, net.V) }\n")
	uGoMod := "module u\n\ngo 1.22\n\nrequire example.com/lib v1.0.0\n\nrequire example.com/tool v1.0.0 // indirect\n"
	w("apps/u/xbin.json", `{"runtime":"go"}`)
	w("apps/u/go.mod", uGoMod)
	w("apps/u/backend/main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/lib/gen\"\n)\n\nfunc main() { fmt.Println(gen.V) }\n")
	w(".xbin/build/"+util.CompKey("apps/t")+"/bin", "an old binary")
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	confine.Configure(fs)
	defer confine.Configure("")
	if _, err := hostToolchain(); err != nil {
		t.Skip("no host toolchain:", err)
	}
	saved := tcVal
	tcVal.goproxy, tcVal.download = "file://"+proxy, ""
	t.Cleanup(func() { tcVal = saved })
	t.Setenv("GONOSUMDB", "example.com")
	t.Setenv("GOFLAGS", "")

	r := &Runner{Root: root, Isolate: true, Rootfs: fs, Reg: reg}
	g := &GoVersions{Run: r, Path: filepath.Join(root, "data", "go-build-versions.json"), Version: "v0.3.65"}
	r.GoVersions = g
	if err := g.Boot(); err != nil || !g.due {
		t.Fatalf("an upgraded workspace: due=%v %v", g.due, err)
	}
	_ = os.Remove(filepath.Join(root, ".xbin", "build", util.CompKey("apps/t"), "bin"))
	runPass(g, true)
	rep := g.Report()
	if len(rep.Errors) > 0 || rep.WorkspaceError != "" || len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/t" {
		t.Fatalf("report %+v", rep)
	}
	if tt := rep.Tiles[0]; !tt.Minimal || !reflect.DeepEqual(tt.Require, []string{"example.com/net v1.1.0"}) || !reflect.DeepEqual(tt.Changes, []VersionChange{
		{Module: "example.com/crypto", Had: "v1.1.0", Now: "v1.0.0"},
		{Module: "example.com/net", Had: "v1.1.0", Now: "v1.0.0"},
	}) {
		t.Errorf("t's entry %+v", tt)
	}
	comparable := func(mods []modVer) []modVer {
		return slices.DeleteFunc(slices.Clone(mods), func(m modVer) bool { return !m.comparable() })
	}
	had := comparable(parseModVers(readGoVersionsState(t, g).Baseline["apps/t"].Had))
	if !reflect.DeepEqual(had, []modVer{{Path: "example.com/crypto", Version: "v1.1.0"}, {Path: "example.com/lib", Version: "v1.0.0"}, {Path: "example.com/net", Version: "v1.1.0"}}) {
		t.Fatalf("t under the shared go.work: %+v", had)
	}
	// the pin module's list is the shared one
	tc, _ := reg.Component("apps/t")
	work, _ := r.buildWork(tc, "./backend", "", nil)
	pinned, err := r.listModules(context.Background(), tc, "./backend", work.GoWork, []deps.Pin{{Path: "example.com/net", Version: "v1.1.0"}})
	if err != nil || !reflect.DeepEqual(comparable(pinned), had) {
		t.Errorf("with the pin module: %+v %v", pinned, err)
	}
	// and so is the list with the line in t's go.mod, which its build links
	w("apps/t/go.mod", strings.Replace(tGoMod, "example.com/net v1.0.0", "example.com/net v1.1.0", 1))
	work, _ = r.buildWork(tc, "./backend", "", nil)
	edited, err := r.listModules(context.Background(), tc, "./backend", work.GoWork, nil)
	if err != nil || !reflect.DeepEqual(comparable(edited), had) {
		t.Errorf("with the line in go.mod: %+v %v", edited, err)
	}
	bin, err := r.build(tc)
	if err != nil {
		if be, ok := err.(*BuildError); ok {
			t.Fatalf("build t: %s", be.Output)
		}
		t.Fatal(err)
	}
	if out, _ := exec.Command(bin).Output(); strings.TrimSpace(string(out)) != "lib net v1.1.0 crypto v1.1.0" {
		t.Errorf("t's backend printed %q", out)
	}
	waitIdle(t, g, "apps/t")
	if msg, ok := g.Alert(); ok {
		t.Errorf("an alert after t caught up: %q", msg)
	}

	// without u's indirect line the shared graph prunes the edge: t linked
	// its own versions, nothing to say
	w("apps/t/go.mod", tGoMod)
	w("apps/u/go.mod", strings.Replace(uGoMod, "\nrequire example.com/tool v1.0.0 // indirect\n", "", 1))
	w(".xbin/build/"+util.CompKey("apps/t")+"/bin", "an old binary")
	g2 := &GoVersions{Run: r, Path: filepath.Join(t.TempDir(), "state.json"), Version: "v0.3.65"}
	if err := g2.Boot(); err != nil || !g2.due {
		t.Fatalf("control: due=%v %v", g2.due, err)
	}
	runPass(g2, true)
	if rep := g2.Report(); rep.WorkspaceError != "" || slices.ContainsFunc(rep.Tiles, func(x GoVersionsTile) bool { return x.Tile == "apps/t" }) ||
		slices.ContainsFunc(rep.Errors, func(x GoVersionsFailed) bool { return x.Tile == "apps/t" }) {
		t.Errorf("control: %+v", rep)
	}
}
