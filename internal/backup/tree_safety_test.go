package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WP-9 (plans/tile-sandbox-runtime.md): Tree reads trees a sandbox writes, as
// xbind. A sandbox that swaps a file or a directory for a symlink between the
// walk seeing it and the open must not get the link's target into the
// archive. skip runs in exactly that window, so the tests race there
// deterministically.

func entries(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	r, err := NewReader(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for {
		name, rd, err := r.Next()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rd)
		got[name] = string(b)
	}
}

func treeArchive(t *testing.T, dir string, skip func(string) bool) (*bytes.Buffer, error) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Manifest(Manifest{Component: "apps/x"}); err != nil {
		t.Fatal(err)
	}
	err := w.Tree(SourcePrefix, dir, skip)
	if cerr := w.Close(); err == nil && cerr != nil {
		t.Fatal(cerr)
	}
	return &buf, err
}

func TestTreeNeverFollowsASwappedSymlink(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "key"), []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a file swapped for a symlink", func(t *testing.T) {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "key"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		buf, err := treeArchive(t, src, func(rel string) bool {
			if rel == "key" {
				_ = os.Remove(filepath.Join(src, "key"))
				_ = os.Symlink(filepath.Join(secret, "key"), filepath.Join(src, "key"))
			}
			return false
		})
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		for name, body := range entries(t, buf) {
			if strings.Contains(body, "HOST-SECRET") {
				t.Fatalf("%s carries the symlink target's bytes", name)
			}
		}
	})

	t.Run("a directory swapped for a symlink", func(t *testing.T) {
		src := t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, "d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "d", "key"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		buf, err := treeArchive(t, src, func(rel string) bool {
			if rel == "d/key" { // the walk is inside d: swap d itself
				_ = os.Rename(filepath.Join(src, "d"), filepath.Join(src, "d.real"))
				_ = os.Symlink(secret, filepath.Join(src, "d"))
			}
			return false
		})
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		got := entries(t, buf)
		for name, body := range got {
			if strings.Contains(body, "HOST-SECRET") {
				t.Fatalf("%s carries the symlink target's bytes", name)
			}
		}
		if got["source/d/key"] != "mine" {
			t.Fatalf("the file the walk listed should come from the directory it listed: %q", got["source/d/key"])
		}
	})
}

// legacyTree is Tree as it was before WP-9 (filepath.WalkDir, then os.Open):
// the walk changed, the archive it writes must not — a tile with nothing
// hostile in it backs up byte for byte as before.
func legacyTree(w *Writer, prefix, osDir string, skip func(rel string) bool) error {
	info, err := os.Stat(osDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(osDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(osDir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		return w.Stream(prefix+rel, int64(fi.Mode().Perm()), fi.Size(), f)
	})
}

func TestTreeBytesUnchanged(t *testing.T) {
	src := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"runtime":"go"}`, 0o644)
	write("a/b/c.go", "package c", 0o644)
	write("a.b", "sorts after the a/ subtree", 0o600)
	write("B", "upper case sorts first", 0o755)
	write("_under/x", "x", 0o644)
	write("node_modules/junk", "dropped", 0o644)
	write("a/skip.me", "dropped", 0o644)
	write("big", strings.Repeat("0123456789", 10000), 0o644)
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.b", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(src, "dirlink")); err != nil {
		t.Fatal(err)
	}
	skip := func(rel string) bool { return rel == "node_modules" || rel == "a/skip.me" }
	m := Manifest{Component: "apps/x", Created: "2026-09-28T00:00:00Z", Includes: []string{"source"}}

	build := func(tree func(w *Writer) error) []byte {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if err := w.Manifest(m); err != nil {
			t.Fatal(err)
		}
		if err := tree(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	old := build(func(w *Writer) error { return legacyTree(w, SourcePrefix, src, skip) })
	cur := build(func(w *Writer) error { return w.Tree(SourcePrefix, src, skip) })
	if !bytes.Equal(old, cur) {
		t.Fatalf("the archive changed: %d bytes before, %d now\nbefore: %v\nnow:    %v",
			len(old), len(cur), keys(entries(t, bytes.NewBuffer(old))), keys(entries(t, bytes.NewBuffer(cur))))
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TreeIn: a tile's directory reached through a symlink (a nested tile its
// parent swapped for a link to .xbin) is refused, not archived.
func TestTreeInRefusesASymlinkedSub(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".xbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".xbin", "secret"), []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.xbin", filepath.Join(root, "apps", "a", "sub")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Manifest(Manifest{Component: "apps/a/sub"}); err != nil {
		t.Fatal(err)
	}
	if err := w.TreeIn(SourcePrefix, root, "apps/a/sub", nil); err == nil {
		t.Fatal("a symlinked tile dir was walked")
	}
	if err := w.TreeIn(SourcePrefix, root, "apps/missing", nil); err != nil {
		t.Fatalf("a missing dir is nothing to add: %v", err)
	}
	_ = w.Close()
	for name, body := range entries(t, &buf) {
		if strings.Contains(body, "HOST-SECRET") {
			t.Fatalf("%s carries .xbin/secret", name)
		}
	}
}
