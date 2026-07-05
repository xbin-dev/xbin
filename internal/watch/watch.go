// Package watch provides a recursive, debounced workspace watcher on top of
// fsnotify (which is non-recursive). It coalesces editor atomic-save dances
// (write-tmp → rename) and emits per-component change batches.
//
// Consumers: the events hub (live reload), the runner (rebuilds), the
// registry (rescan), and the deps materializer.
package watch

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/magik6k/xbin/internal/util"
)

// Event is a debounced change notification for one workspace-relative path
// (a file's parent chain decides which components it belongs to; that mapping
// is the consumer's job via registry.Resolve).
type Event struct {
	// Paths are the workspace-relative file paths that changed in this batch.
	Paths []string
}

type Watcher struct {
	root     string
	fs       *fsnotify.Watcher
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]struct{}
	timer   *time.Timer

	C    chan Event
	done chan struct{}
}

// ignoreFile reports editor droppings and other files that must never
// trigger reloads or rebuilds.
func ignoreFile(name string) bool {
	base := filepath.Base(name)
	switch {
	case strings.HasSuffix(base, ".swp"), strings.HasSuffix(base, ".swx"),
		strings.HasSuffix(base, "~"), strings.HasPrefix(base, ".#"),
		base == "4913", // vim's permission probe
		strings.HasPrefix(base, ".goutputstream"):
		return true
	}
	return false
}

func ignoreDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 && util.ReservedTop[parts[0]] && parts[0] != "xbin" {
		// data/, home/, .xbin/, vendor/ never drive reloads. (Top-level
		// "xbin" is reserved as an id but isn't a real directory.)
		return true
	}
	for _, p := range parts {
		if util.IgnoredDirs[p] || (strings.HasPrefix(p, ".") && p != ".") {
			return true
		}
	}
	return false
}

func New(root string, debounce time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root: root, fs: fw, debounce: debounce,
		pending: map[string]struct{}{},
		C:       make(chan Event, 16),
		done:    make(chan struct{}),
	}
	if err := w.addRecursive(root); err != nil {
		fw.Close()
		return nil, err
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(w.root, p)
		if rel != "." && ignoreDir(rel) {
			return filepath.SkipDir
		}
		if err := w.fs.Add(p); err != nil {
			slog.Warn("watch add failed", "dir", p, "err", err)
		}
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			slog.Warn("watch error", "err", err)
		case <-w.done:
			return
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if ignoreDir(filepath.Dir(rel)) || ignoreFile(rel) {
		return
	}

	// New directories must be watched immediately (mkdir -p a/b/c arrives as
	// one event for "a" on some kernels).
	if ev.Op.Has(fsnotify.Create) {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
			_ = w.addRecursive(ev.Name)
		}
	}

	w.mu.Lock()
	w.pending[rel] = struct{}{}
	if w.timer == nil {
		w.timer = time.AfterFunc(w.debounce, w.flush)
	} else {
		w.timer.Reset(w.debounce)
	}
	w.mu.Unlock()
}

func (w *Watcher) flush() {
	w.mu.Lock()
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	w.pending = map[string]struct{}{}
	w.timer = nil
	w.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	select {
	case w.C <- Event{Paths: paths}:
	default:
		slog.Warn("watch: consumer slow, dropping batch", "n", len(paths))
	}
}

func (w *Watcher) Close() error {
	close(w.done)
	return w.fs.Close()
}
