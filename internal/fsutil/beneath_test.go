package fsutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A FIFO (or a symlink to one) is refused without blocking — a tile
// writer's `mkfifo x.js` must not hang the /c/ plane or a scan — by every
// opener, the fallbacks included.
func TestOpenersRefuseFIFOs(t *testing.T) {
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "x")
	if err := os.MkdirAll(tile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(filepath.Join(tile, "pipe.js")); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink("pipe.js", filepath.Join(tile, "x.js")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, rel := range []string{"pipe.js", "x.js"} {
			if f, err := OpenBeneath(tile, rel); !errors.Is(err, ErrNotRegular) {
				t.Errorf("OpenBeneath %s: %v", rel, err)
				if f != nil {
					f.Close()
				}
			}
			if _, err := OpenIn(root, "apps/x", rel); !errors.Is(err, ErrNotRegular) {
				t.Errorf("OpenIn %s: %v", rel, err)
			}
			if _, _, err := OpenResolved(root, "apps/x/"+rel, nil); !errors.Is(err, ErrNotRegular) {
				t.Errorf("OpenResolved %s: %v", rel, err)
			}
			if _, err := openInFallback(root, "apps/x", rel); !errors.Is(err, ErrNotRegular) {
				t.Errorf("fallback %s: %v", rel, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an opener blocked on a FIFO")
	}
}

// OpenIn: the sub-directory (a tile, named by a possibly stale registry)
// must contain no symlink at all; in-tree symlinks below it still work.
func TestOpenInRefusesSymlinkedSub(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "apps", "a", "dist"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".xbin"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".xbin", "secret"), []byte("s3cret"), 0o600))
	must(os.WriteFile(filepath.Join(root, "apps", "a", "dist", "app.js"), []byte("ok"), 0o644))
	must(os.Symlink("dist", filepath.Join(root, "apps", "a", "assets")))
	must(os.Symlink("../../.xbin", filepath.Join(root, "apps", "a", "sub2"))) // a "nested component" swapped for a link
	must(os.Symlink("apps", filepath.Join(root, "alias")))

	for _, open := range map[string]func(root, sub, rel string) (*os.File, error){"openat2": OpenIn, "fallback": openInFallback} {
		f, err := open(root, "apps/a", "assets/app.js")
		if err != nil {
			t.Fatalf("in-tree symlink refused: %v", err)
		}
		f.Close()
		for _, c := range [][2]string{{"apps/a/sub2", "secret"}, {"alias/a", "dist/app.js"}, {"apps/a", "sub2/secret"}} {
			if f, err := open(root, c[0], c[1]); err == nil {
				b, _ := io.ReadAll(f)
				f.Close()
				t.Errorf("%s + %s: opened %q", c[0], c[1], b)
			}
		}
	}
	// The trust root itself may be a symlink.
	link := filepath.Join(t.TempDir(), "ws")
	must(os.Symlink(root, link))
	if f, err := OpenIn(link, "apps/a", "dist/app.js"); err != nil {
		t.Fatalf("symlinked root: %v", err)
	} else {
		f.Close()
	}
}

// OpenResolved (the legacy plane): symlinks anywhere inside root resolve,
// the allow filter sees the resolved path, and nothing outside root opens.
func TestOpenResolved(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "apps", "a"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "apps", "b"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".xbin"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".xbin", "secret"), []byte("s3cret"), 0o600))
	must(os.WriteFile(filepath.Join(root, "apps", "b", "lib.js"), []byte("lib"), 0o644))
	must(os.Symlink("../b/lib.js", filepath.Join(root, "apps", "a", "shared.js")))
	must(os.Symlink(filepath.Join(root, "apps", "b", "lib.js"), filepath.Join(root, "apps", "a", "abs.js"))) // absolute, in-tree
	must(os.Symlink("../../.xbin/secret", filepath.Join(root, "apps", "a", "leak.txt")))
	must(os.Symlink("/etc/hostname", filepath.Join(root, "apps", "a", "host.txt")))
	noXbin := func(p string) bool { return !strings.HasPrefix(p, ".xbin") }

	for _, rel := range []string{"apps/a/shared.js", "apps/a/abs.js"} {
		f, r, err := OpenResolved(root, rel, noXbin)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != "lib" || r != "apps/b/lib.js" {
			t.Errorf("%s: %q via %q", rel, b, r)
		}
	}
	for _, rel := range []string{"apps/a/leak.txt", "apps/a/host.txt", "apps/a/../../x"} {
		if f, _, err := OpenResolved(root, rel, noXbin); err == nil {
			f.Close()
			t.Errorf("%s: opened", rel)
		}
	}
}
