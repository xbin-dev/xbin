//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run GoVersions
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
package runner

import (
	"archive/zip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
// unaffected. The lists run confined and write nothing of the workspace.
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
	g.runAll(true)
	rep := g.Report()
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %+v", rep.Errors)
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
	g2.runAll(false)
	rep2 := g2.Report()
	if len(rep2.Errors) > 0 || len(rep2.Tiles) != 1 || !reflect.DeepEqual(rep2.Tiles[0].Require, []string{"example.com/top v1.1.0"}) {
		t.Errorf("isolation off: %+v", rep2)
	}
}
