package builtins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// hostile is a workspace with a unit installed at tiles/x (v1) and a
// secret outside every tile, and v2 of the unit, which changes every file.
func hostile(t *testing.T) (root, tile, secret string, v2 fstest.MapFS) {
	t.Helper()
	v1 := fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\"title\": \"x\"}\n")},
		"tiles/x/index.html": {Data: []byte("<p>v1</p>\n")},
		"tiles/x/app.js":     {Data: []byte("a\nb\nc\nd\ne\n")},
		"tiles/x/sub/a.js":   {Data: []byte("one\n")},
	}
	root, _ = installed(t, v1)
	tile = filepath.Join(root, "tiles", "x")
	secret = filepath.Join(root, "homes", "admin", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	v2 = fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\"title\": \"x2\"}\n")},
		"tiles/x/index.html": {Data: []byte("<p>v2</p>\n")},
		"tiles/x/app.js":     {Data: []byte("A\nb\nc\nd\ne\n")},
		"tiles/x/sub/a.js":   {Data: []byte("two\n")},
	}
	return root, tile, secret, v2
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// within runs f, failing the test when it doesn't return in time (a FIFO
// read blocks forever).
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

func noteOf(uu *UnitUpdate, rel string) string {
	for _, f := range uu.Files {
		if f.Path == rel {
			return f.Status + ": " + f.Note
		}
	}
	return ""
}

func unitOf(t *testing.T, u *Updater, id string) *UnitUpdate {
	t.Helper()
	ups, err := u.Updates()
	if err != nil {
		t.Fatal(err)
	}
	for _, uu := range ups {
		if uu.ID == id {
			return uu
		}
	}
	return nil
}

// covers the review of 2026-11-20, finding 1 — a proposal's series (which
// every reader of the tile can fetch) never holds what a link in the tile
// names: an adopted unit whose page is a link to an admin's credentials
// proposes upstream's page, says the link was not read, and the secret is
// nowhere in it.
func TestProposeNeverReadsThroughALink(t *testing.T) {
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "chat")
	must(t, os.MkdirAll(tile, 0o755))
	secret := filepath.Join(root, "homes", "admin", ".credentials.json")
	must(t, os.MkdirAll(filepath.Dir(secret), 0o755))
	must(t, os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600))
	must(t, os.Symlink(secret, filepath.Join(tile, "index.html")))
	must(t, os.WriteFile(filepath.Join(tile, "app.js"), []byte("mine\n"), 0o644))
	u := NewUpdater(root, nil, fstest.MapFS{
		"apps/chat/index.html": {Data: []byte("<p>upstream</p>\n")},
		"apps/chat/app.js":     {Data: []byte("upstream\n")},
	})
	uu := unitOf(t, u, "scaffold:apps/chat")
	if uu == nil {
		t.Fatal("the adopted unit isn't listed")
	}
	if n := noteOf(uu, "index.html"); !strings.HasPrefix(n, "conflict: ") || !strings.Contains(n, "apps/chat/index.html is a symbolic link: not read") {
		t.Fatalf("the link's status: %q", n)
	}
	prop, err := u.Propose("scaffold:apps/chat")
	if err != nil {
		t.Fatal(err)
	}
	for what, s := range map[string]string{"series": prop.Series, "message": prop.Message, "notes": strings.Join(prop.Notes, "\n")} {
		if strings.Contains(s, "TOKEN-s3cret") {
			t.Fatalf("the %s holds the link's target:\n%s", what, s)
		}
	}
	if !strings.Contains(prop.Message, "apps/chat/index.html is a symbolic link: not read") {
		t.Fatalf("the proposal doesn't say the link was not read:\n%s", prop.Message)
	}
	if !strings.Contains(prop.Series, "-mine") || !strings.Contains(prop.Series, "+<p>upstream</p>") {
		t.Fatalf("the series isn't ours→theirs for what was read:\n%s", prop.Series)
	}
	if b, _ := os.ReadFile(secret); string(b) != "TOKEN-s3cret" {
		t.Fatal("the secret changed")
	}
}

// covers the review of 2026-11-20, finding 2 — no update writes through a
// link, follows one to decide, or blocks on a FIFO: a managed file swapped
// for a dangling link (to a path outside the tile), a directory swapped for
// a link to another tile, a FIFO and a directory where a file was — for
// replace, merge, propose and the listing. Each is said, nothing outside
// the tile is created or changed, and the update stays offered.
func TestUpdateNeverFollowsOrBlocks(t *testing.T) {
	for _, mode := range []string{"replace", "merge", "propose", "list"} {
		t.Run(mode, func(t *testing.T) {
			root, tile, secret, v2 := hostile(t)
			other := filepath.Join(root, "apps", "other")
			must(t, os.MkdirAll(other, 0o755))
			must(t, os.WriteFile(filepath.Join(other, "a.js"), []byte("other's\n"), 0o644))
			planted := filepath.Join(root, "apps", "planted.js")
			// index.html: a dangling link to a path outside the tile.
			must(t, os.Remove(filepath.Join(tile, "index.html")))
			must(t, os.Symlink(planted, filepath.Join(tile, "index.html")))
			// sub/: swapped for a link to another tile.
			must(t, os.RemoveAll(filepath.Join(tile, "sub")))
			must(t, os.Symlink(other, filepath.Join(tile, "sub")))
			// app.js: a FIFO.
			must(t, os.Remove(filepath.Join(tile, "app.js")))
			must(t, syscall.Mkfifo(filepath.Join(tile, "app.js"), 0o644))

			u := NewUpdater(root, nil, v2)
			var err error
			var a Applied
			within(t, mode, func() {
				switch mode {
				case "replace":
					a, err = u.ApplyReplace("scaffold:tiles/x")
				case "merge":
					a, err = u.ApplyMerge("scaffold:tiles/x")
				case "propose":
					var p *Proposal
					if p, err = u.Propose("scaffold:tiles/x"); err == nil {
						a.Notes = p.Notes
					}
				case "list":
					uu := unitOf(t, u, "scaffold:tiles/x")
					for _, rel := range []string{"index.html", "app.js", "sub/a.js"} {
						a.Notes = append(a.Notes, noteOf(uu, rel))
					}
				}
			})
			said := strings.Join(a.Notes, "\n")
			if err != nil {
				said += "\n" + err.Error()
			}
			if _, serr := os.Lstat(planted); serr == nil {
				t.Fatal("a file was created where the dangling link pointed")
			}
			if b, _ := os.ReadFile(filepath.Join(other, "a.js")); string(b) != "other's\n" {
				t.Fatalf("another tile's file was written through the linked directory: %q", b)
			}
			if b, _ := os.ReadFile(secret); string(b) != "TOKEN-s3cret" {
				t.Fatal("the secret changed")
			}
			fi, _ := os.Lstat(filepath.Join(tile, "app.js"))
			switch mode {
			case "replace": // replace takes upstream's files: a link or a FIFO at a file's path is replaced, never followed
				if err != nil || !strings.Contains(said, "tiles/x/sub/a.js is beneath a symbolic link") {
					t.Fatalf("replace through the linked directory: %v\n%s", err, said)
				}
				if fi == nil || !fi.Mode().IsRegular() {
					t.Errorf("replace: the FIFO wasn't replaced by upstream's file: %v", fi)
				}
				if fi, _ := os.Lstat(filepath.Join(tile, "index.html")); fi == nil || !fi.Mode().IsRegular() {
					t.Errorf("replace: the link wasn't replaced by upstream's file: %v", fi)
				}
			default:
				for _, want := range []string{"tiles/x/index.html is a symbolic link", "tiles/x/app.js is not a regular file", "tiles/x/sub/a.js is beneath a symbolic link"} {
					if !strings.Contains(said, want) {
						t.Errorf("%s: not said %q:\n%s", mode, want, said)
					}
				}
				if fi == nil || fi.Mode()&os.ModeNamedPipe == 0 {
					t.Errorf("%s: the FIFO was replaced: %v", mode, fi)
				}
				if l, _ := os.Readlink(filepath.Join(tile, "index.html")); l != planted {
					t.Errorf("%s: the link was replaced", mode)
				}
			}
			if !offered(t, &Updater{root: root, scaffoldFS: v2}) && mode != "replace" {
				t.Errorf("%s: the update is no longer offered", mode)
			}
		})
	}
}

// fixture for the unit at tiles/x of TestUpdateNeverFollowsOrBlocks is
// "scaffold:tiles/x"; offered() asks for exactly that id.

// covers the review of 2026-11-20, finding 2 — a directory where the unit
// has a file: merge says it and leaves it; replace refuses to write it.
func TestUpdateADirectoryWhereAFileWas(t *testing.T) {
	for _, mode := range []string{"replace", "merge"} {
		t.Run(mode, func(t *testing.T) {
			root, tile, _, v2 := hostile(t)
			must(t, os.Remove(filepath.Join(tile, "app.js")))
			must(t, os.MkdirAll(filepath.Join(tile, "app.js", "inner"), 0o755))
			u := NewUpdater(root, nil, v2)
			var a Applied
			var err error
			if mode == "replace" {
				a, err = u.ApplyReplace("scaffold:tiles/x")
			} else {
				a, err = u.ApplyMerge("scaffold:tiles/x")
			}
			said := strings.Join(a.Notes, "\n")
			if err != nil {
				said += "\n" + err.Error()
			}
			if !strings.Contains(said, "tiles/x/app.js is a directory") {
				t.Fatalf("not said:\n%s", said)
			}
			if fi, err := os.Lstat(filepath.Join(tile, "app.js", "inner")); err != nil || !fi.IsDir() {
				t.Fatal("the directory was touched")
			}
		})
	}
}

// covers the review of 2026-11-20, finding 3 — a file the tile's writer
// changes between the merge reading it and writing it is left as the
// writer made it, the merge stops there (writing nothing more) and says
// which file and what it wrote before; a file the merge leaves as it was is
// not written at all.
func TestMergeStopsWhereAFileChanged(t *testing.T) {
	root, tile, _, v2 := hostile(t)
	// index.html edited here, upstream changes it elsewhere too: merged.
	must(t, os.WriteFile(filepath.Join(tile, "index.html"), []byte("<p>v1</p>\n<p>mine</p>\n"), 0o644))
	// sub/a.js edited here, upstream unchanged for it: it stays as it is.
	v2["tiles/x/sub/a.js"] = &fstest.MapFile{Data: []byte("one\n")}
	must(t, os.WriteFile(filepath.Join(tile, "sub", "a.js"), []byte("one, mine\n"), 0o644))
	subBefore, _ := os.Stat(filepath.Join(tile, "sub", "a.js"))
	var order []string
	beforeWrite = func(full string) {
		order = append(order, full)
		if full == "tiles/x/index.html" { // the tile's agent saves meanwhile
			_ = os.WriteFile(filepath.Join(tile, "index.html"), []byte("<p>the agent's</p>\n"), 0o644)
		}
	}
	defer func() { beforeWrite = nil }()
	a, err := NewUpdater(root, nil, v2).ApplyMerge("scaffold:tiles/x")
	var ce *ChangedError
	if !errors.As(err, &ce) || ce.Path != "tiles/x/index.html" {
		t.Fatalf("want the change said, got %v (order %v)", err, order)
	}
	if !strings.Contains(err.Error(), "written before it stopped: tiles/x/app.js") {
		t.Fatalf("what was written before isn't said: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(tile, "index.html")); string(b) != "<p>the agent's</p>\n" {
		t.Fatalf("the agent's edit was lost: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(tile, "xbin.json")); string(b) != "{\"title\": \"x\"}\n" {
		t.Fatalf("a file after the stop was written: %q", b)
	}
	_ = a
	// The run without the agent: sub/a.js (merged to what it holds) is not rewritten.
	beforeWrite = nil
	root2, tile2, _, v2b := hostile(t)
	v2b["tiles/x/sub/a.js"] = &fstest.MapFile{Data: []byte("one\n")}
	must(t, os.WriteFile(filepath.Join(tile2, "sub", "a.js"), []byte("one, mine\n"), 0o644))
	past := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(filepath.Join(tile2, "sub", "a.js"), past, past))
	subBefore, _ = os.Stat(filepath.Join(tile2, "sub", "a.js"))
	a2, err := NewUpdater(root2, nil, v2b).ApplyMerge("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	subAfter, _ := os.Stat(filepath.Join(tile2, "sub", "a.js"))
	if !subAfter.ModTime().Equal(subBefore.ModTime()) || !os.SameFile(subBefore, subAfter) {
		t.Fatal("a file the merge leaves as it is was written again")
	}
	for _, f := range a2.Files {
		if f == "tiles/x/sub/a.js" {
			t.Fatalf("listed as written: %v", a2.Files)
		}
	}
}

// covers the review of 2026-11-20, finding (d) — a unit's update never
// writes inside a component nested in it that the unit doesn't ship (it
// may be someone else's tile): it is a conflict, said, for every mode.
func TestUpdateNeverWritesInANestedComponent(t *testing.T) {
	for _, mode := range []string{"replace", "merge", "list"} {
		t.Run(mode, func(t *testing.T) {
			root, tile, _, v2 := hostile(t)
			must(t, os.WriteFile(filepath.Join(tile, "sub", "xbin.json"), []byte("{\"title\": \"bob's\"}\n"), 0o644))
			must(t, os.WriteFile(filepath.Join(tile, "sub", "a.js"), []byte("bob's\n"), 0o644))
			u := NewUpdater(root, nil, v2)
			var said string
			switch mode {
			case "replace":
				a, err := u.ApplyReplace("scaffold:tiles/x")
				must(t, err)
				said = strings.Join(a.Notes, "\n")
			case "merge":
				a, err := u.ApplyMerge("scaffold:tiles/x")
				must(t, err)
				said = strings.Join(a.Notes, "\n")
			case "list":
				said = noteOf(unitOf(t, u, "scaffold:tiles/x"), "sub/a.js")
			}
			if !strings.Contains(said, "tiles/x/sub/a.js is inside tiles/x/sub, a component of its own") {
				t.Fatalf("not said:\n%s", said)
			}
			if b, _ := os.ReadFile(filepath.Join(tile, "sub", "a.js")); string(b) != "bob's\n" {
				t.Fatalf("written in the nested component: %q", b)
			}
		})
	}
}

// covers the review of 2026-11-20 (instantiation) — copying a workspace
// template reads its files as they are: a link in it (to a home's
// credentials) fails the copy, and the secret is copied nowhere.
func TestCopyTreeNeverReadsThroughALink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "apps", "tpl")
	must(t, os.MkdirAll(src, 0o755))
	must(t, os.WriteFile(filepath.Join(src, "xbin.json"), []byte("{\"title\": \"t\"}\n"), 0o644))
	secret := filepath.Join(root, "homes", "admin", "creds")
	must(t, os.MkdirAll(filepath.Dir(secret), 0o755))
	must(t, os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600))
	must(t, os.Symlink(secret, filepath.Join(src, "index.html")))
	done := make(chan error, 1)
	go func() {
		_, err := CopyTree(fsutil.NoSymlinksFS(root), "apps/tpl", root, "apps/copy", "apps/tpl", nil)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked")
	}
	if err == nil {
		t.Fatal("copied through a link")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "apps", "copy", "index.html")); strings.Contains(string(b), "s3cret") {
		t.Fatal("the secret was copied")
	}
}

// covers the review of 2026-11-20, finding 2 — a write never follows a link
// swapped in after the update looked: the tile's writer turns sub/ into a
// link to another tile just as a replace writes sub/a.js — the write is
// refused, said, and the other tile's file is as it was.
func TestUpdateWriteNeverFollowsALinkSwappedIn(t *testing.T) {
	root, tile, _, v2 := hostile(t)
	other := filepath.Join(root, "apps", "other")
	must(t, os.MkdirAll(other, 0o755))
	must(t, os.WriteFile(filepath.Join(other, "a.js"), []byte("other's\n"), 0o644))
	beforeWrite = func(full string) {
		if full == "tiles/x/sub/a.js" {
			_ = os.RemoveAll(filepath.Join(tile, "sub"))
			_ = os.Symlink(other, filepath.Join(tile, "sub"))
		}
	}
	defer func() { beforeWrite = nil }()
	_, err := NewUpdater(root, nil, v2).ApplyReplace("scaffold:tiles/x")
	if err == nil || !strings.Contains(err.Error(), "tiles/x/sub/a.js is beneath a symbolic link: not written") {
		t.Fatalf("the write through the swapped-in link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "a.js")); string(b) != "other's\n" {
		t.Fatalf("another tile's file was written: %q", b)
	}
}
