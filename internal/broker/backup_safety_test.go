package broker

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/util"
)

// WP-9 (plans/tile-sandbox-runtime.md): a restore runs as xbind and writes
// into trees a sandbox wrote — a tile's source, its terminal layer's upper,
// its resource mounts. Whatever the sandbox planted there (a symlinked dir, a
// symlink at a file's path) must never carry a write outside the tree.

type archiveEntry struct{ name, body string }

func safetyArchive(t *testing.T, m backup.Manifest, entries ...archiveEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := backup.NewWriter(&buf)
	if err := w.Manifest(m); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := w.File(e.name, 0o644, []byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// fakeResEnc points the broker's resource mounts at plain directories: a
// gocryptfs stand-in that "inits" and "mounts" by exiting 0, so Ensure hands
// back the mount dir without FUSE.
func fakeResEnc(t *testing.T, b *Broker) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gocryptfs")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.resenc = resenc.New(b.Reg.Root, bin, func(string) ([]byte, error) { return make([]byte, 32), nil })
}

// outsideUntouched fails if anything but want lives in dir.
func outsideUntouched(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		body, ok := want[e.Name()]
		if !ok {
			t.Errorf("the restore wrote %s outside the tree", filepath.Join(dir, e.Name()))
			continue
		}
		if got, _ := os.ReadFile(filepath.Join(dir, e.Name())); string(got) != body {
			t.Errorf("the restore wrote through a symlink into %s: %q", e.Name(), got)
		}
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// readRegular reads p, failing unless it is a regular file (not a symlink).
func readRegular(t *testing.T, p string) string {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("%s is %v, want a regular file", p, fi.Mode())
	}
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestRestoreNeverFollowsPlantedSymlinks(t *testing.T) {
	const comp = "apps/calendar"
	man := backup.Manifest{Component: comp, Scope: comp, ScopeRoot: true,
		Resources: map[string]string{"db": "sqlite"}, Includes: []string{"source", "data", "term-env"}}

	t.Run("a symlinked dir in source", func(t *testing.T) {
		b := testBroker(t)
		outside := t.TempDir()
		src := filepath.Join(b.Reg.Root, comp)
		mustSymlink(t, outside, filepath.Join(src, "evil"))
		_, err := b.restore(safetyArchive(t, man, archiveEntry{"source/evil/pwned", "x"}), comp)
		outsideUntouched(t, outside, nil)
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if got := readRegular(t, filepath.Join(src, "evil", "pwned")); got != "x" {
			t.Fatalf("restored %q", got)
		}
	})

	t.Run("a symlink at a file's path in source", func(t *testing.T) {
		b := testBroker(t)
		outside := t.TempDir()
		target := filepath.Join(outside, "target.txt")
		if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(b.Reg.Root, comp)
		_ = os.Remove(filepath.Join(src, "index.html"))
		mustSymlink(t, target, filepath.Join(src, "index.html"))
		if _, err := b.restore(safetyArchive(t, man, archiveEntry{"source/index.html", "restored"}), comp); err != nil {
			t.Fatalf("restore: %v", err)
		}
		outsideUntouched(t, outside, map[string]string{"target.txt": "original"})
		if got := readRegular(t, filepath.Join(src, "index.html")); got != "restored" {
			t.Fatalf("restored %q", got)
		}
	})

	t.Run("a symlinked dir in term/upper", func(t *testing.T) {
		b := testBroker(t)
		outside := t.TempDir()
		layer := b.termDir(comp)
		mustSymlink(t, outside, filepath.Join(layer, "upper", "etc"))
		_, err := b.restore(safetyArchive(t, man, archiveEntry{"term/upper/etc/pwned", "x"}), comp)
		outsideUntouched(t, outside, nil)
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if got := readRegular(t, filepath.Join(layer, "upper", "etc", "pwned")); got != "x" {
			t.Fatalf("restored %q", got)
		}
	})

	t.Run("a symlinked dir in a resource mount", func(t *testing.T) {
		b := testBroker(t)
		fakeResEnc(t, b)
		outside := t.TempDir()
		mdir := b.resenc.MountDir(util.ScopeKey(comp), "db")
		mustSymlink(t, outside, filepath.Join(mdir, "sub"))
		_, err := b.restore(safetyArchive(t, man, archiveEntry{"data/sqlite/db/sub/pwned", "x"}), comp)
		outsideUntouched(t, outside, nil)
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if got := readRegular(t, filepath.Join(mdir, "sub", "pwned")); got != "x" {
			t.Fatalf("restored %q", got)
		}
	})
}

// A tile's dir reached through a symlink — a nested tile its parent swapped
// for a link to .xbin — is refused, and nothing lands where the link points.
func TestRestoreRefusesASymlinkedTileDir(t *testing.T) {
	b := testBroker(t)
	victim := filepath.Join(b.Reg.Root, ".xbin", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "../../.xbin/victim", filepath.Join(b.Reg.Root, "apps", "calendar", "nested"))
	const comp = "apps/calendar/nested"
	man := backup.Manifest{Component: comp, Scope: "apps/calendar", Includes: []string{"source"}}
	if _, err := b.restore(safetyArchive(t, man, archiveEntry{"source/xbin.json", "{}"}), comp); err == nil {
		t.Fatal("restored through a symlinked tile dir")
	}
	outsideUntouched(t, victim, nil)
}

// The archive comes from a tile (the archiver): what its manifest names is
// never trusted as a place to write.
func TestRestoreRefusesAnotherComponentsArchive(t *testing.T) {
	b := testBroker(t)
	other := backup.Manifest{Component: "apps/email", Scope: "apps/email", Includes: []string{"source"}}
	if _, err := b.restore(safetyArchive(t, other, archiveEntry{"source/x", "x"}), "apps/calendar"); err == nil {
		t.Fatal("restored apps/email's archive as apps/calendar")
	}
	escape := backup.Manifest{Component: "../../outside", Includes: []string{"source"}}
	if _, err := b.restore(safetyArchive(t, escape, archiveEntry{"source/x", "x"}), "apps/calendar"); err == nil {
		t.Fatal("restored an archive naming a path outside the workspace")
	}
	// Resource data of a scope the component doesn't root.
	foreign := backup.Manifest{Component: "apps/email", Scope: "apps/calendar", Includes: []string{"source", "data"}}
	if _, err := b.restore(safetyArchive(t, foreign, archiveEntry{backup.KVName, `{"events":{"k":"dg=="}}`}), "apps/email"); err == nil {
		t.Fatal("restored another scope's resource data")
	}
	if _, err := os.Stat(filepath.Join(b.Reg.Root, "apps", "email", "x")); err == nil {
		t.Fatal("a refused archive wrote a file")
	}
}

// The terminal layer is rebuilt apart and swapped in whole, only while the
// sessions holding it are gone; a VM terminal's disk stays with the layer.
func TestRestoreSwapsTheTermLayer(t *testing.T) {
	const comp = "apps/calendar"
	man := backup.Manifest{Component: comp, Scope: comp, ScopeRoot: true, Includes: []string{"source", "term-env"}}
	setup := func(t *testing.T) (*Broker, string) {
		b := testBroker(t)
		layer := b.termDir(comp)
		for rel, body := range map[string]string{"base": "v1\n", "upper/stale": "old", "vm/disk.img": "DISK"} {
			p := filepath.Join(layer, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return b, layer
	}
	archive := func(t *testing.T) *bytes.Buffer {
		return safetyArchive(t, man,
			archiveEntry{"term/base", "v2\n"},
			archiveEntry{"term/upper/new", "restored"},
			archiveEntry{"term/vm/disk.img", "HOSTILE"}, // never in a real backup: ignored
		)
	}

	t.Run("swapped while held", func(t *testing.T) {
		b, layer := setup(t)
		held, released := 0, 0
		b.HoldTermEnv = func(c string) (func(), error) {
			if c != comp {
				t.Errorf("held %q", c)
			}
			if _, err := os.Stat(filepath.Join(layer, "upper", "stale")); err != nil {
				t.Error("the layer was touched before its sessions were gone")
			}
			held++
			return func() {
				if _, err := os.Stat(filepath.Join(layer, "upper", "new")); err != nil {
					t.Error("released before the new layer was in place")
				}
				released++
			}, nil
		}
		if _, err := b.restore(archive(t), comp); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if held != 1 || released != 1 {
			t.Fatalf("held %d, released %d", held, released)
		}
		if got := readRegular(t, filepath.Join(layer, "upper", "new")); got != "restored" {
			t.Fatalf("upper/new = %q", got)
		}
		if got := readRegular(t, filepath.Join(layer, "base")); got != "v2\n" {
			t.Fatalf("base = %q", got)
		}
		if _, err := os.Stat(filepath.Join(layer, "upper", "stale")); err == nil {
			t.Fatal("the old layer's files survived the swap")
		}
		if got := readRegular(t, filepath.Join(layer, "vm", "disk.img")); got != "DISK" {
			t.Fatalf("vm/disk.img = %q, want the layer's own disk", got)
		}
		if fi, err := os.Stat(layer); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("layer dir: %v %v", fi, err)
		}
		if ents, _ := os.ReadDir(filepath.Join(b.Reg.Root, ".xbin", "restore")); len(ents) != 0 {
			t.Fatalf("leftovers in .xbin/restore: %v", ents)
		}
	})

	t.Run("a hold that fails leaves the layer alone", func(t *testing.T) {
		b, layer := setup(t)
		b.HoldTermEnv = func(string) (func(), error) { return nil, os.ErrDeadlineExceeded }
		if _, err := b.restore(archive(t), comp); err == nil {
			t.Fatal("restore went on without the layer")
		}
		if got := readRegular(t, filepath.Join(layer, "upper", "stale")); got != "old" {
			t.Fatalf("upper/stale = %q", got)
		}
		if ents, _ := os.ReadDir(filepath.Join(b.Reg.Root, ".xbin", "restore")); len(ents) != 0 {
			t.Fatalf("leftovers in .xbin/restore: %v", ents)
		}
	})
}

// Offloading a tile clears its source through the dir the registry named; a
// link swapped in for that dir must not turn the clearing onto its target.
func TestOffloadClearsOnlyTheTileDir(t *testing.T) {
	b := testBroker(t)
	victim := filepath.Join(b.Reg.Root, ".xbin", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(b.Reg.Root, "apps", "calendar")
	if err := os.Rename(dir, dir+".real"); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "../.xbin/victim", dir) // the registry still lists apps/calendar
	if err := b.removeSourceBulk("apps/calendar"); err == nil {
		t.Fatal("cleared through a symlinked tile dir")
	}
	if got := readRegular(t, filepath.Join(victim, "keep")); got != "k" {
		t.Fatalf("victim/keep = %q", got)
	}

	// The real thing still works: all but the stub goes.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".real", dir); err != nil {
		t.Fatal(err)
	}
	if err := b.removeSourceBulk("apps/calendar"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "scope.json" || names[1] != "xbin.json" {
		t.Fatalf("left %v, want the stub", names)
	}
}

// A real backup restores as it always did — over read-only files (git
// objects are 0444) and into a tile whose dir is gone.
func TestBackupRestoreRoundTrip(t *testing.T) {
	b := testBroker(t)
	const comp = "apps/calendar"
	dir := filepath.Join(b.Reg.Root, comp)
	obj := filepath.Join(dir, ".git", "objects", "ab", "cdef")
	if err := os.MkdirAll(filepath.Dir(obj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obj, []byte("blob"), 0o444); err != nil {
		t.Fatal(err)
	}
	c, _ := b.Reg.Component(comp)
	var buf bytes.Buffer
	bw := backup.NewWriter(&buf)
	if err := b.writeBackup(bw, c); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	arch := buf.Bytes()

	// Over the live tree (the 0444 object is replaced).
	if _, err := b.restore(bytes.NewReader(arch), comp); err != nil {
		t.Fatalf("restore over the tree: %v", err)
	}
	if got := readRegular(t, obj); got != "blob" {
		t.Fatalf("git object = %q", got)
	}
	// Into a missing dir.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := b.restore(bytes.NewReader(arch), comp); err != nil {
		t.Fatalf("restore into a missing dir: %v", err)
	}
	if got := readRegular(t, filepath.Join(dir, "index.html")); got != "<html></html>" {
		t.Fatalf("index.html = %q", got)
	}
	if got := readRegular(t, obj); got != "blob" {
		t.Fatalf("git object = %q", got)
	}
}

// fakeArchiver is an archiver tile's API in memory: PUT stores a version,
// GET versions/latest serves it back.
type fakeArchiver struct{ latest []byte }

func (a *fakeArchiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut:
		a.latest, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"version":"v1"}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/versions/latest") && a.latest != nil:
		_, _ = w.Write(a.latest)
	default:
		http.Error(w, "no", http.StatusNotFound)
	}
}

// Offload-full, then the restore that re-enabling runs: the tile comes back
// whole — source and terminal layer — with a link a sandbox planted in the
// meantime replaced, not followed, and the layer swapped under a hold.
func TestOffloadFullThenRestore(t *testing.T) {
	b := testBroker(t)
	const comp = "apps/calendar"
	b.ProxyHandler = &fakeArchiver{}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(b.Reg.Root, comp)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	layer := b.termDir(comp)
	if err := os.MkdirAll(filepath.Join(layer, "upper", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "upper", "etc", "motd"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	holds := 0
	b.HoldTermEnv = func(string) (func(), error) { holds++; return func() {}, nil }

	if err := b.offload(comp, true); err != nil {
		t.Fatalf("offload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err == nil {
		t.Fatal("offload-full kept the source")
	}
	// A sandbox of the parent plants a link where the source had a file.
	outside := t.TempDir()
	mustSymlink(t, filepath.Join(outside, "main.go"), filepath.Join(dir, "main.go"))

	if _, err := b.doRestore(comp, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	outsideUntouched(t, outside, nil)
	if got := readRegular(t, filepath.Join(dir, "main.go")); got != "package main" {
		t.Fatalf("main.go = %q", got)
	}
	if got := readRegular(t, filepath.Join(layer, "upper", "etc", "motd")); got != "hi" {
		t.Fatalf("the terminal layer came back as %q", got)
	}
	if holds != 1 {
		t.Fatalf("the layer was held %d times", holds)
	}
}
