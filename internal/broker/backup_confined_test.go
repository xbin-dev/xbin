package broker

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
)

// WP-9b (plans/tile-sandbox-runtime.md): a terminal layer is a tree a
// sandbox wrote — in range mode its upper holds files other sub-uids own,
// which xbind can't unlink — so every removal of one goes through removeTree
// (a confined run), and offload-full takes the layer out of use first.

// recordRemovals has b's removeTree log each dir to log (and remove it, as
// the direct run would).
func recordRemovals(b *Broker, log *[]string) {
	b.rmTree = func(dir string) error {
		*log = append(*log, "remove "+dir)
		return os.RemoveAll(dir)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A restore's swap: the day-old leftovers of .xbin/restore and the old layer
// it moved aside are removed confined — the old layer once the new one is in
// place and released, so no session waits on the removal.
func TestRestoreRemovesOldLayersConfined(t *testing.T) {
	const comp = "apps/calendar"
	b := testBroker(t)
	layer := b.termDir(comp)
	writeTree(t, layer, map[string]string{"upper/stale": "old", "vm/disk.img": "DISK"})
	dir := filepath.Join(b.Reg.Root, ".xbin", "restore")
	stale, fresh := filepath.Join(dir, "stale-1"), filepath.Join(dir, "fresh-2")
	writeTree(t, stale, map[string]string{"upper/f": "x"})
	writeTree(t, fresh, map[string]string{"upper/f": "x"})
	dayAgo := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, dayAgo, dayAgo); err != nil {
		t.Fatal(err)
	}
	var log []string
	recordRemovals(b, &log)
	b.HoldTermEnv = func(string) (func(), error) {
		log = append(log, "hold")
		return func() { log = append(log, "release") }, nil
	}
	man := backup.Manifest{Component: comp, Scope: comp, ScopeRoot: true, Includes: []string{"term-env"}}
	if _, err := b.restore(safetyArchive(t, man, archiveEntry{"term/upper/new", "n"}), comp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(log) != 4 || log[0] != "remove "+stale || log[1] != "hold" || log[2] != "release" ||
		!strings.HasPrefix(log[3], "remove "+filepath.Join(dir, "apps~calendar-")) || !strings.HasSuffix(log[3], ".old") {
		t.Fatalf("log = %q; want the stale leftover removed, the hold, its release, then the old layer removed", log)
	}
	if got := readRegular(t, filepath.Join(layer, "upper", "new")); got != "n" {
		t.Fatalf("upper/new = %q", got)
	}
	if got := readRegular(t, filepath.Join(layer, "vm", "disk.img")); got != "DISK" {
		t.Fatalf("vm/disk.img = %q", got)
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Fatalf("the fresh leftover went: %v", err)
	}
}

// offloadRig is a tile with a terminal layer and an archiver bound.
func offloadRig(t *testing.T) (*Broker, string, *[]string) {
	t.Helper()
	b := testBroker(t)
	b.ProxyHandler = &fakeArchiver{}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	const comp = "apps/calendar"
	writeTree(t, filepath.Join(b.Reg.Root, comp), map[string]string{"main.go": "package main"})
	writeTree(t, b.termDir(comp), map[string]string{"upper/etc/motd": "hi"})
	var log []string
	recordRemovals(b, &log)
	return b, comp, &log
}

// Offload-full: the layer's sessions are killed and the layer held (the
// hold), then it is removed confined, then released; a plain offload leaves
// the layer and its sessions alone.
func TestOffloadFullHoldsThenRemovesConfined(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		b, comp, log := offloadRig(t)
		b.HoldTermEnv = func(c string) (func(), error) {
			if c != comp {
				t.Errorf("held %q", c)
			}
			*log = append(*log, "hold")
			return func() { *log = append(*log, "release") }, nil
		}
		if err := b.offload(comp, true); err != nil {
			t.Fatalf("offload: %v", err)
		}
		if want := []string{"hold", "remove " + b.termDir(comp), "release"}; strings.Join(*log, "|") != strings.Join(want, "|") {
			t.Fatalf("log = %q, want %q", *log, want)
		}
		if _, err := os.Lstat(b.termDir(comp)); !os.IsNotExist(err) {
			t.Fatalf("the layer survived: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(b.Reg.Root, comp, "main.go")); !os.IsNotExist(err) {
			t.Fatalf("the source survived: %v", err)
		}
	})

	t.Run("a session that won't let go: archived, nothing removed", func(t *testing.T) {
		b, comp, log := offloadRig(t)
		b.HoldTermEnv = func(string) (func(), error) { return nil, os.ErrDeadlineExceeded }
		err := b.offload(comp, true)
		if err == nil || !strings.Contains(err.Error(), "nothing removed") {
			t.Fatalf("offload = %v, want a failure that removed nothing", err)
		}
		if b.ProxyHandler.(*fakeArchiver).latest == nil {
			t.Fatal("the hold was taken before the archive")
		}
		if len(*log) != 0 {
			t.Fatalf("removed %q", *log)
		}
		if got := readRegular(t, filepath.Join(b.termDir(comp), "upper", "etc", "motd")); got != "hi" {
			t.Fatalf("motd = %q", got)
		}
		if got := readRegular(t, filepath.Join(b.Reg.Root, comp, "main.go")); got != "package main" {
			t.Fatalf("main.go = %q", got)
		}
	})

	t.Run("not full", func(t *testing.T) {
		b, comp, log := offloadRig(t)
		b.HoldTermEnv = func(string) (func(), error) { t.Error("a plain offload held the layer"); return func() {}, nil }
		if err := b.offload(comp, false); err != nil {
			t.Fatalf("offload: %v", err)
		}
		if len(*log) != 0 {
			t.Fatalf("removed %q", *log)
		}
		if got := readRegular(t, filepath.Join(b.termDir(comp), "upper", "etc", "motd")); got != "hi" {
			t.Fatalf("motd = %q", got)
		}
	})
}

// A restored file gets the permission bits it was archived with — an
// executable in the source or a terminal upper stays one — and never a
// setuid, setgid or sticky bit, whatever the archive says.
func TestRestoreKeepsPermissionBits(t *testing.T) {
	const comp = "apps/calendar"
	b := testBroker(t)
	var buf bytes.Buffer
	w := backup.NewWriter(&buf)
	if err := w.Manifest(backup.Manifest{Component: comp, Scope: comp, ScopeRoot: true, Includes: []string{"source", "term-env"}}); err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		name string
		mode int64
	}{
		{"source/bin/run.sh", 0o755},
		{"source/private", 0o600},
		{"source/.git/objects/ab/cdef", 0o444},
		{"source/suid", 0o4755},
		{"source/sticky", 0o3775},
		{"term/upper/usr/local/bin/tool", 0o755},
	}
	for _, e := range entries {
		if err := w.File(e.name, e.mode, []byte("#!/bin/sh\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.restore(&buf, comp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	src, layer := filepath.Join(b.Reg.Root, comp), b.termDir(comp)
	for p, want := range map[string]fs.FileMode{
		filepath.Join(src, "bin", "run.sh"):                          0o755,
		filepath.Join(src, "private"):                                0o600,
		filepath.Join(src, ".git", "objects", "ab", "cdef"):          0o444,
		filepath.Join(src, "suid"):                                   0o755,
		filepath.Join(src, "sticky"):                                 0o775,
		filepath.Join(layer, "upper", "usr", "local", "bin", "tool"): 0o755,
	} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != want {
			t.Errorf("%s: %v, want %v", p, fi.Mode(), want)
		}
	}

	// And through a real backup: a 0755 file comes back 0755.
	if err := os.Chmod(filepath.Join(src, "bin", "run.sh"), 0o750); err != nil {
		t.Fatal(err)
	}
	c, _ := b.Reg.Component(comp)
	buf.Reset()
	bw := backup.NewWriter(&buf)
	if err := b.writeBackup(bw, c); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if _, err := b.restore(&buf, comp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(src, "bin", "run.sh")); err != nil || fi.Mode() != 0o750 {
		t.Fatalf("run.sh came back %v (%v), want 0750", fi.Mode(), err)
	}
}
