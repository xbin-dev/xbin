package watch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Close drops a pending debounce and closes C: a consumer ranging over C
// ends, and a change Close cut short is never delivered — not by the
// debounce timer, not by a flush that raced Close (xbind's shutdown: its
// watch loop writes nothing into the workspace after it). Idempotent.
func TestCloseEndsTheStream(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, time.Hour) // the debounce never fires by itself
	if err != nil {
		t.Fatal(err)
	}
	w.handle(fsnotify.Event{Name: filepath.Join(root, "apps", "x", "index.html"), Op: fsnotify.Write})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w.flush() // a timer that fired as Close ran
	w.handle(fsnotify.Event{Name: filepath.Join(root, "apps", "x", "app.js"), Op: fsnotify.Write})
	n := 0
	for range w.C { // ends: C is closed
		n++
	}
	if n != 0 {
		t.Fatalf("%d batches after Close", n)
	}
	w.mu.Lock()
	timer := w.timer
	w.mu.Unlock()
	if timer != nil {
		t.Fatal("a debounce timer outlived Close")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// dropped is the watcher's filter for one changed file, as handle applies it
// (watch.go, handle): the file's directory by ignoreDir, its name by
// ignoreFile.
func dropped(rel string) bool {
	return ignoreDir(filepath.Dir(rel)) || ignoreFile(rel)
}

// covers D119d D119f PO-12 — the watcher never reports xbind's own stores
// (.xbin/deploy, data/checkpoints, data/deployments, all under reserved
// top-level directories), any .git, node_modules or deps tree, or editor
// droppings; ordinary tile files, dot files included, are reported. So
// materializing a checkpoint, refreshing the view repository or writing the
// record can never trigger a reload.
func TestIgnoreRules(t *testing.T) {
	files := []struct {
		rel     string
		dropped bool
	}{
		// The dev-lifecycle stores (reserved top-level directories).
		{".xbin/deploy/3f2a1c9e0b7d4a55/9c1e0f2b7a3d/index.html", true}, // a materialized tree
		{".xbin/deploy/3f2a1c9e0b7d4a55/9c1e0f2b7a3d/backend/main.go", true},
		{".xbin/deploy/3f2a1c9e0b7d4a55/d/dev/backend.log", true},
		{".xbin/deploy/3f2a1c9e0b7d4a55/d/dev/sbx/state.json", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.git/HEAD", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.git/objects/ab/cdef0123", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.git/refs/xbin/log/main", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.view.git/info/refs", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.view.git/refs/heads/deploy/main", true},
		{"data/deployments/3f2a1c9e0b7d4a55.json", true},
		{"data/deployments/.3f2a1c9e0b7d4a55.json.4821.tmp", true},
		// Today's reserved top-level directories.
		{".xbin/log/apps~counter-0badc0de.log", true},
		{".xbin/build/apps~counter-0badc0de/bin", true},
		{"data/grants.json", true},
		{"data/resources/apps~counter/db.sqlite", true},
		{"home/alice/notes.md", true},
		{"homes/alice/notes.md", true},
		{"vendor/lit.js", true},
		// Ignored directories at any depth.
		{".git/HEAD", true},
		{"apps/counter/.git/index", true},
		{"apps/counter/.git/objects/ab/cdef0123", true},
		{"node_modules/lodash/index.js", true},
		{"apps/counter/node_modules/lodash/index.js", true},
		{"apps/counter/deps/lib/index.js", true},
		{"apps/pyapp/__pycache__/main.cpython-312.pyc", true},
		{"apps/counter/.cache/go-build/x", true},
		// Editor droppings and xbind's own atomic-write temp files.
		{"apps/counter/.main.go.swp", true},
		{"apps/counter/main.go.swx", true},
		{"apps/counter/main.go~", true},
		{"apps/counter/.#main.go", true},
		{"apps/counter/4913", true},
		{"apps/counter/.goutputstream-ABC123", true},
		{"apps/counter/.index.html.8f3a.tmp", true},
		// Ordinary tile files.
		{"apps/counter/index.html", false},
		{"apps/counter/xbin.json", false},
		{"apps/counter/backend/main.go", false},
		{"apps/counter/native.js", false},
		{"apps/a/b/x.js", false},
		{"apps/counter/.gitignore", false}, // dot files are ordinary files
		{"apps/counter/.env", false},
		{"apps/counter/build.tmp", false}, // .tmp only when dot-prefixed
		// Names reserved at the top level are ordinary below it.
		{"apps/counter/data/seed.json", false},
		{"apps/counter/home/index.html", false},
		{"apps/counter/deploy/run.sh", false},
		{"apps/counter/checkpoints/x.json", false},
		// Top-level files and the unreal "xbin" directory.
		{"xbin.json", false},
		{"README.md", false},
		{"xbin/cron", false},
	}
	for _, f := range files {
		if got := dropped(f.rel); got != f.dropped {
			t.Errorf("file %s: dropped=%v, want %v", f.rel, got, f.dropped)
		}
	}

	// Directories: addRecursive skips these, so nothing below them is watched.
	dirs := []struct {
		rel     string
		skipped bool
	}{
		{".xbin", true},
		{".xbin/deploy", true},
		{".xbin/deploy/3f2a1c9e0b7d4a55/d/dev", true},
		{"data", true},
		{"data/checkpoints", true},
		{"data/checkpoints/3f2a1c9e0b7d4a55.git", true},
		{"data/deployments", true},
		{"home", true},
		{"homes", true},
		{"vendor", true},
		{"apps/counter/.git", true},
		{"apps/counter/node_modules", true},
		{"apps/counter/deps", true},
		{"apps/counter/.cache", true},
		{"apps", false},
		{"apps/counter", false},
		{"apps/counter/backend", false},
		{"apps/counter/data", false},
		{"apps/a/b", false},
		{"xbin", false},
	}
	for _, d := range dirs {
		if got := ignoreDir(d.rel); got != d.skipped {
			t.Errorf("dir %s: skipped=%v, want %v", d.rel, got, d.skipped)
		}
	}
}

// covers D119d D119f PO-12 — end to end through fsnotify: writes into the stores
// of a running workspace (whose .xbin and data exist, as boot leaves them)
// never reach a batch; a tile edit made after them does, alone.
func TestWatcherDropsStoreWrites(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".xbin/log", "data", "apps/counter/.git", "apps/counter/node_modules"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/counter/index.html")

	w, err := New(root, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// What materializing, the view repository and the record write, plus the
	// tile's own ignored trees and an editor's swap file.
	for _, rel := range []string{
		".xbin/deploy/3f2a1c9e0b7d4a55/9c1e0f2b7a3d/index.html",
		".xbin/deploy/3f2a1c9e0b7d4a55/d/main/backend.log",
		"data/checkpoints/3f2a1c9e0b7d4a55.git/HEAD",
		"data/checkpoints/3f2a1c9e0b7d4a55.view.git/info/refs",
		"data/deployments/3f2a1c9e0b7d4a55.json",
		"apps/counter/.git/index",
		"apps/counter/node_modules/x/index.js",
		"apps/counter/.index.html.swp",
	} {
		write(rel)
	}
	tmp := filepath.Join(root, "data/deployments/.3f2a1c9e0b7d4a55.json.1.tmp")
	if err := os.WriteFile(tmp, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(root, "data/deployments/3f2a1c9e0b7d4a55.json")); err != nil {
		t.Fatal(err)
	}
	// The sentinel: inotify delivers in order, so every earlier event has been
	// handled once a batch holds it.
	write("apps/counter/index.html")

	var seen []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-w.C:
			seen = append(seen, ev.Paths...)
			for _, p := range ev.Paths {
				if p == "apps/counter/index.html" {
					sort.Strings(seen)
					if got := strings.Join(seen, ","); got != "apps/counter/index.html" {
						t.Fatalf("batches held %s, want only the tile edit", got)
					}
					return
				}
			}
		case <-deadline:
			t.Fatalf("no batch with the tile edit within 5s; saw %v", seen)
		}
	}
}
