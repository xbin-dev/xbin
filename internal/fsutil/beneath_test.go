package fsutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBeneath(t *testing.T) {
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "x")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(tile, "dist"), 0o755))
	must(os.WriteFile(filepath.Join(tile, "dist", "app.js"), []byte("ok"), 0o644))
	must(os.WriteFile(filepath.Join(root, "secret"), []byte("s3cret"), 0o600))
	must(os.Symlink("dist", filepath.Join(tile, "assets")))                     // in-tree dir link
	must(os.Symlink(filepath.Join(root, "secret"), filepath.Join(tile, "abs"))) // absolute escape
	must(os.Symlink("../../secret", filepath.Join(tile, "rel")))                // relative escape

	f, err := OpenBeneath(tile, "assets/app.js")
	if err != nil {
		t.Fatalf("in-tree symlink refused: %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "ok" {
		t.Fatalf("read %q", b)
	}
	for _, rel := range []string{"abs", "rel", "../../secret", "dist/../../../secret"} {
		if f, err := OpenBeneath(tile, rel); err == nil {
			f.Close()
			t.Errorf("%s: escaped the tile", rel)
		}
	}
	if _, err := OpenBeneath(tile, "abs"); !errors.Is(err, ErrEscapes) && !errors.Is(err, os.ErrNotExist) {
		t.Logf("abs: %v", err) // the kernel may report ELOOP/EXDEV; any error is a refusal
	}
	if _, err := OpenBeneath(tile, "nope.js"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: want ErrNotExist, got %v", err)
	}
}
