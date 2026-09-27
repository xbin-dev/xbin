package confine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Direct mode (isolation off): the tree helpers copy, measure and remove
// with the host's tools, never through a link planted in the tree, and
// refuse paths that aren't absolute and clean.
func TestTreeHelpersDirect(t *testing.T) {
	if Isolated() {
		t.Fatal("confinement is on in a unit test")
	}
	ctx := context.Background()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	for _, d := range []string{outside, filepath.Join(src, "sub"), dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "sub", "f"), make([]byte, 64<<10), 0o640)
	_ = os.WriteFile(filepath.Join(src, ".hidden"), []byte("h"), 0o600)
	_ = os.Symlink(outside, filepath.Join(src, "out")) // a link planted in the tree

	if err := CopyTree(ctx, src, dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "sub", "f")); err != nil || fi.Size() != 64<<10 || fi.Mode().Perm() != 0o640 {
		t.Fatalf("copied file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, ".hidden")); string(b) != "h" {
		t.Error("a dotfile was not copied")
	}
	if l, err := os.Readlink(filepath.Join(dst, "out")); err != nil || l != outside {
		t.Errorf("the link was not copied as a link: %q %v", l, err)
	}
	if n, err := DiskUsage(ctx, dst); err != nil || n < 64<<10 {
		t.Errorf("DiskUsage = %d, %v", n, err)
	}
	if n, err := DiskUsage(ctx, filepath.Join(root, "missing")); n != 0 || err != nil {
		t.Errorf("DiskUsage(missing) = %d, %v", n, err)
	}

	for _, d := range []string{src, dst} {
		if err := RemoveAll(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived", d)
		}
	}
	if b, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(b) != "keep" {
		t.Fatal("removing the tree followed its link")
	}
	if err := RemoveAll(ctx, src); err != nil {
		t.Errorf("RemoveAll(missing): %v", err)
	}
	// a link at the path itself is unlinked, its target kept
	link := filepath.Join(root, "link")
	_ = os.Symlink(outside, link)
	if err := RemoveAll(ctx, link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("RemoveAll of a link removed its target's contents")
	}

	for _, bad := range []string{"", "/", "rel/dir", root + "/../x", root + "/"} {
		if err := RemoveAll(ctx, bad); err == nil {
			t.Errorf("RemoveAll(%q) accepted", bad)
		}
		if _, err := DiskUsage(ctx, bad); err == nil {
			t.Errorf("DiskUsage(%q) accepted", bad)
		}
	}
	_ = os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	if err := CopyTree(ctx, filepath.Join(root, "a"), filepath.Join(root, "a", "b")); err == nil {
		t.Error("CopyTree into its own source accepted")
	}
}
