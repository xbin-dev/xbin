package checkpoint

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/util"
)

// fileInfo is one entry of a materialized tree, read with Lstat.
type fileInfo struct {
	mode fs.FileMode // type and permission bits
	body string      // a file's content, a symlink's target
}

// materialized lists a tree on the host without following anything: path →
// type, permissions and content (test code; C5 binds daemon code).
func materialized(t *testing.T, root string) map[string]fileInfo {
	t.Helper()
	out := map[string]fileInfo{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		e := fileInfo{mode: fi.Mode()}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			e.body, err = os.Readlink(p)
		case fi.Mode().IsRegular():
			var b []byte
			b, err = os.ReadFile(p)
			e.body = string(b)
		}
		out[filepath.ToSlash(rel)] = e
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkModes asserts 07-runtime §2.6's modes: directories 0755, files 0444
// or 0555, symlinks as symlinks, nothing else.
func checkModes(t *testing.T, got map[string]fileInfo, exec map[string]bool) {
	t.Helper()
	for rel, e := range got {
		switch m := e.mode; {
		case m.IsDir():
			if m.Perm() != 0o755 {
				t.Errorf("%s: directory mode %o, want 0755", rel, m.Perm())
			}
		case m.IsRegular():
			want := fs.FileMode(0o444)
			if exec[rel] {
				want = 0o555
			}
			if m.Perm() != want {
				t.Errorf("%s: file mode %o, want %o", rel, m.Perm(), want)
			}
		case m&fs.ModeSymlink != 0:
		default:
			t.Errorf("%s: a %v in a materialized tree", rel, m.Type())
		}
	}
}

// tmpLeft lists the .tmp-* entries under a tile's trees directory.
func tmpLeft(t *testing.T, trees string) []string {
	t.Helper()
	var out []string
	ents, _ := os.ReadDir(trees)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// materializeRuns counts the recorded materialization runs.
func materializeRuns(rec *recorder) int {
	n := 0
	for _, body := range rec.scripts() {
		if strings.Contains(body, "checkout-index") {
			n++
		}
	}
	return n
}

// covers P9 P16 — a checkpoint materializes at
// .xbin/deploy/<TileKey>/<full tree hash>/, byte-exact (in-tree
// .gitattributes don't apply), with directories 0755, files 0444 or 0555
// (the exec bit kept) and symlinks as symlinks; a present tree is returned
// with no run; host os.RemoveAll removes it with no chmod, and it is
// rebuilt from the store; an interrupted extraction (a failed run, a run
// killed after the checkout, a leftover .tmp-* of a killed xbind) is never
// visible and its leftovers are swept; concurrent callers share one run;
// only the tile's retained checkpoints materialize, and a tile without a
// store gets ErrNoStore with nothing created. (That sandboxes get the tree
// read-only by the bind flag, not the host modes, is the confined
// TestMaterializeReadOnlyToSandboxes.)
func TestMaterializeAtomicReadOnly(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	files := map[string]string{
		"index.html":              "<p>hi</p>\n",
		"lib/a.js":                "export const a = 1\n",
		"lib/deep/b.txt":          "$Id$\r\nline\n",
		"bin.dat":                 "\x00\xff\r\n",
		".gitattributes":          "* text eol=crlf ident filter=x working-tree-encoding=UTF-16\n",
		".gitignore":              "node_modules/\n",
		"node_modules/m/index.js": "module.exports = 1\n",
	}
	src := tile(t, s, "apps/mat", files)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	files["run.sh"] = "#!/bin/sh\necho hi\n"
	links := map[string]string{"link.js": "lib/a.js", "lib/up.html": "../index.html", "etc": "/etc"}
	for rel, target := range links {
		if err := os.Symlink(target, filepath.Join(src.WorkTree, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	res := capture(t, s, src, true)
	before := rec.count()

	root, err := s.Materialize(src.Tile, res.Hash)
	if err != nil {
		t.Fatal(err)
	}
	trees := filepath.Join(s.Root, ".xbin", "deploy", util.TileKey(src.Tile))
	if want := filepath.Join(trees, res.Hash); root != want {
		t.Fatalf("root %s, want %s", root, want)
	}
	if n := rec.count() - before; n != 1 {
		t.Errorf("materializing took %d confined runs, want 1", n)
	}
	got := materialized(t, root)
	checkModes(t, got, map[string]bool{"run.sh": true})
	for rel, body := range files {
		if e := got[rel]; !e.mode.IsRegular() || e.body != body {
			t.Errorf("%s: %v %q, want the file's bytes %q", rel, e.mode, e.body, body)
		}
	}
	for rel, target := range links {
		if e := got[rel]; e.mode&fs.ModeSymlink == 0 || e.body != target {
			t.Errorf("%s: %v %q, want a symlink to %s", rel, e.mode, e.body, target)
		}
	}
	if fi, err := os.Lstat(root); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the tree's root: %v %v", fi, err)
	}
	if left := tmpLeft(t, trees); len(left) > 0 {
		t.Errorf("extractions left behind: %v", left)
	}

	// present: no run
	before = rec.count()
	if again, err := s.Materialize(src.Tile, res.Hash); err != nil || again != root || rec.count() != before {
		t.Errorf("a present tree: %s %v, %d runs", again, err, rec.count()-before)
	}

	// host os.RemoveAll works with no chmod walk; the tree is rebuilt
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("os.RemoveAll of a materialized tree: %v", err)
	}
	if _, err := s.Materialize(src.Tile, res.Hash); err != nil {
		t.Fatal(err)
	}
	if again := materialized(t, root); len(again) != len(got) || again["lib/deep/b.txt"] != got["lib/deep/b.txt"] {
		t.Errorf("the rebuilt tree differs: %d entries, want %d", len(again), len(got))
	}
	if err := os.RemoveAll(filepath.Join(s.Root, ".xbin")); err != nil { // rm -rf .xbin
		t.Fatalf("removing .xbin: %v", err)
	}

	t.Run("interrupted", func(t *testing.T) {
		realRun := s.run
		defer func() { s.run = realRun }()
		// the run checks the tree out and then fails: nothing is visible
		s.run = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
			res, err := realRun(ctx, c)
			if err == nil && strings.Contains(c.Argv[2], "checkout-index") {
				err = errors.New("killed")
			}
			return res, err
		}
		if _, err := s.Materialize(src.Tile, res.Hash); err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("a failed run: %v", err)
		}
		if exists(root) || len(tmpLeft(t, trees)) > 0 {
			t.Fatalf("a failed extraction left %v (tree present %v)", tmpLeft(t, trees), exists(root))
		}
		// the run is killed after the checkout, before the rename
		s.run = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
			if strings.Contains(c.Argv[2], "checkout-index") {
				c.Argv = append([]string(nil), c.Argv...)
				c.Argv[2] += "exec sleep 30\n"
				c.Timeout = time.Second
			}
			return realRun(ctx, c)
		}
		if _, err := s.Materialize(src.Tile, res.Hash); err == nil {
			t.Fatal("a killed run succeeded")
		}
		if exists(root) || len(tmpLeft(t, trees)) > 0 {
			t.Fatalf("a killed extraction left %v (tree present %v)", tmpLeft(t, trees), exists(root))
		}
		s.run = realRun
		// a killed xbind's leftover: swept before the next extraction
		stale := filepath.Join(trees, tmpPrefix+"crashed")
		if err := os.MkdirAll(filepath.Join(stale, "tree", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stale, "tree", "sub", "f"), []byte("half"), 0o444); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Materialize(src.Tile, res.Hash); err != nil {
			t.Fatal(err)
		}
		if exists(stale) {
			t.Error("a leftover extraction survived the next one")
		}
		// Sweep (boot) removes leftovers, never an extraction in flight
		if err := os.MkdirAll(filepath.Join(stale, "tree"), 0o755); err != nil {
			t.Fatal(err)
		}
		busy := filepath.Join(trees, tmpPrefix+"busy")
		if err := os.Mkdir(busy, 0o700); err != nil {
			t.Fatal(err)
		}
		materializing.Store(busy, struct{}{})
		defer materializing.Delete(busy)
		if err := s.Sweep(); err != nil {
			t.Fatal(err)
		}
		if exists(stale) || !exists(busy) || !exists(root) {
			t.Errorf("Sweep: stale %v, in flight %v, tree %v; want false, true, true", exists(stale), exists(busy), exists(root))
		}
	})

	t.Run("single-flight", func(t *testing.T) {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		before := materializeRuns(rec)
		var wg sync.WaitGroup
		roots := make([]string, 8)
		errs := make([]error, 8)
		for i := range roots {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				roots[i], errs[i] = s.Materialize(src.Tile, res.Hash)
			}(i)
		}
		wg.Wait()
		for i := range roots {
			if errs[i] != nil || roots[i] != root {
				t.Errorf("caller %d: %s %v", i, roots[i], errs[i])
			}
		}
		if n := materializeRuns(rec) - before; n != 1 {
			t.Errorf("%d concurrent callers made %d extractions, want 1", len(roots), n)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		if _, err := s.Materialize(src.Tile, res.Hash[:7]); !errors.Is(err, ErrBadID) {
			t.Errorf("a short id: %v, want ErrBadID", err)
		}
		if _, err := s.Materialize("../x", res.Hash); err == nil {
			t.Error("a bad tile path materialized")
		}
		// the git view's tree is an object of the store, not a checkpoint
		view := strings.TrimSpace(storeGit(t, s, src.Tile, "rev-parse", "refs/xbin/views/"+res.Hash+"^{tree}"))
		if view == res.Hash {
			t.Fatal("the fixture's git view equals its checkpoint")
		}
		if _, err := s.Materialize(src.Tile, view); !errors.Is(err, ErrUnknownCheckpoint) {
			t.Errorf("a tree that is no checkpoint: %v, want ErrUnknownCheckpoint", err)
		}
		if exists(filepath.Join(trees, view)) || len(tmpLeft(t, trees)) > 0 {
			t.Error("a refused extraction left something behind")
		}
		// a tile without a store: nothing created
		none := tile(t, s, "apps/none", map[string]string{"a": "a\n"})
		if _, err := s.Materialize(none.Tile, res.Hash); !errors.Is(err, ErrNoStore) {
			t.Errorf("a tile without a store: %v, want ErrNoStore", err)
		}
		if exists(s.TreesDir(none.Tile)) || s.Exists(none.Tile) {
			t.Error("materializing on a tile without a store created something")
		}
		// out of time: an error that says so, nothing visible
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		release, err := s.acquire(context.Background(), src.Tile)
		if err != nil {
			t.Fatal(err)
		}
		s.Caps.Time = 200 * time.Millisecond
		_, err = s.Materialize(src.Tile, res.Hash)
		release()
		s.Caps.Time = DefaultCaps().Time
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting past the cap: %v, want DeadlineExceeded", err)
		}
		if exists(root) {
			t.Error("a timed-out call left a tree")
		}
	})
}

// covers P16 T2 — rule C5 for materialization and GC, behaviourally: a
// checkpoint whose manifest, a source file, a dependency link and a chain
// of links point at a FIFO outside it materializes, is found present, and
// is evicted by GC without xbind (or the tools) opening any of them. The
// case internal/confine's TestNoFollowingHostWalks drives once registered.
func TestMaterializeNoFollowingHostWalk(t *testing.T) {
	needGit(t)
	w := newTripwire(t)
	s, _ := testStore(t)
	s.Caps.Every = 0
	src := tile(t, s, "apps/links", map[string]string{"a.txt": "a\n", "lib/": "", "deps/": ""})
	for rel, target := range map[string]string{
		"xbin.json":    w.path,
		"lib/index.js": w.path,
		"deps/other":   filepath.Dir(w.path),
		"c1":           "c2",
		"c2":           w.path,
	} {
		if err := os.Symlink(target, filepath.Join(src.WorkTree, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	res := capture(t, s, src, true)
	root, err := s.Materialize(src.Tile, res.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Materialize(src.Tile, res.Hash); err != nil {
		t.Fatal(err)
	}
	if w.tripped.Load() {
		t.Fatal("materializing opened the FIFO outside the tree")
	}
	handedOut.Delete(root)
	s.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if err := s.GC(context.Background(), src.Tile, nil); err != nil {
		t.Fatal(err)
	}
	if exists(root) {
		t.Error("GC kept a tree nothing keeps")
	}
	if w.tripped.Load() {
		t.Fatal("GC opened the FIFO outside the tree")
	}
}
