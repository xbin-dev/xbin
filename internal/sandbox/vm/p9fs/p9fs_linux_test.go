//go:build linux

package p9fs

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// serve starts the server on one end of a socketpair and returns a client.
func serve(t *testing.T, exports []Export) *p9.Client {
	t.Helper()
	s, err := New(exports)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	a, b := net.Pipe()
	go func() { _ = s.Serve(a) }()
	c, err := p9.NewClient(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func walk(t *testing.T, f p9.File, names ...string) (p9.File, error) {
	t.Helper()
	cur := f
	for _, n := range names {
		_, nf, err := cur.Walk([]string{n})
		if err != nil {
			return nil, err
		}
		cur = nf
	}
	return cur, nil
}

func isErrno(err error, want unix.Errno) bool {
	var e linux.Errno
	if errors.As(err, &e) {
		return uintptr(e) == uintptr(want)
	}
	var u unix.Errno
	return errors.As(err, &u) && u == want
}

// dirFid clones f: a create turns the fid it runs on into the new file.
func dirFid(t *testing.T, f p9.File) p9.File {
	t.Helper()
	_, d, err := f.Walk(nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExportsOnly(t *testing.T) {
	ws := t.TempDir()
	secret := t.TempDir()
	os.WriteFile(filepath.Join(secret, "key"), []byte("s3cret"), 0o600)
	os.MkdirAll(filepath.Join(ws, "tile"), 0o755)
	os.WriteFile(filepath.Join(ws, "tile", "a.txt"), []byte("hello"), 0o644)
	// a symlink planted in the tile pointing out of the export
	os.Symlink(secret, filepath.Join(ws, "tile", "escape"))

	c := serve(t, []Export{{Path: ws}})

	// the attach name must lead to an export
	if _, err := c.Attach(secret); err == nil {
		t.Fatalf("attached outside the exports")
	}
	root, err := c.Attach("/")
	if err != nil {
		t.Fatal(err)
	}
	// the virtual root only lists the path to the export
	if _, err := walk(t, root, "etc"); !isErrno(err, unix.ENOENT) {
		t.Fatalf("walk /etc from the virtual root: %v, want ENOENT", err)
	}

	f, err := c.Attach(ws)
	if err != nil {
		t.Fatal(err)
	}
	a, err := walk(t, f, "tile", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Open(p9.ReadOnly); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, _ := a.ReadAt(buf, 0)
	if string(buf[:n]) != "hello" {
		t.Fatalf("read %q", buf[:n])
	}

	// the symlink is returned as a symlink, never followed server-side
	l, err := walk(t, f, "tile", "escape")
	if err != nil {
		t.Fatal(err)
	}
	if target, err := l.Readlink(); err != nil || target != secret {
		t.Fatalf("readlink = %q, %v", target, err)
	}
	if _, err := walk(t, l, "key"); err == nil {
		t.Fatalf("walked through a symlink out of the export")
	}
}

func TestReadOnlyExport(t *testing.T) {
	ro := t.TempDir()
	os.WriteFile(filepath.Join(ro, "f"), []byte("x"), 0o644)
	c := serve(t, []Export{{Path: ro, RO: true}})
	f, err := c.Attach(ro)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := dirFid(t, f).Create("new", p9.WriteOnly, 0o644, 0, 0); !isErrno(err, unix.EROFS) {
		t.Fatalf("create on a read-only export: %v, want EROFS", err)
	}
	g, err := walk(t, f, "f")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Open(p9.WriteOnly); !isErrno(err, unix.EROFS) {
		t.Fatalf("open for write on a read-only export: %v, want EROFS", err)
	}
}

func TestCreateWriteReaddir(t *testing.T) {
	dir := t.TempDir()
	c := serve(t, []Export{{Path: dir}})
	f, err := c.Attach(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := f
	nf, _, _, err := dirFid(t, d).Create("out.txt", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nf.WriteAt([]byte("written"), 0); err != nil {
		t.Fatal(err)
	}
	nf.Close()
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(b) != "written" {
		t.Fatalf("host sees %q", b)
	}
	if _, err := d.Mkdir("sub", 0o755, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Symlink("out.txt", "link", 0, 0); err != nil {
		t.Fatal(err)
	}
	// many entries, read in pages
	for i := 0; i < 300; i++ {
		os.WriteFile(filepath.Join(dir, "sub", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), nil, 0o644)
	}
	s, err := walk(t, f, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(p9.ReadOnly); err != nil {
		t.Fatal(err)
	}
	seen := 0
	var off uint64
	for {
		ents, err := s.Readdir(off, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) == 0 {
			break
		}
		seen += len(ents)
		off = ents[len(ents)-1].Offset
	}
	if seen != 300 {
		t.Fatalf("readdir saw %d entries, want 300", seen)
	}
	if err := d.RenameAt("out.txt", d, "moved.txt"); err != nil {
		t.Fatal(err)
	}
	if err := d.UnlinkAt("moved.txt", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("unlink did not reach the host: %v", err)
	}
}

func TestNestedExportKeepsItsFlag(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "tiles", "t1"), 0o755)
	os.MkdirAll(filepath.Join(ws, "tiles", "t2"), 0o755)
	// the terminal shape: the workspace read-only, its own tile writable
	c := serve(t, []Export{{Path: ws, RO: true}, {Path: filepath.Join(ws, "tiles", "t1")}})
	f, err := c.Attach(filepath.Join(ws, "tiles", "t1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := dirFid(t, f).Create("x", p9.WriteOnly, 0o644, 0, 0); err != nil {
		t.Fatalf("create in the writable nested export: %v", err)
	}
	g, err := c.Attach(filepath.Join(ws, "tiles", "t2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := dirFid(t, g).Create("x", p9.WriteOnly, 0o644, 0, 0); !isErrno(err, unix.EROFS) {
		t.Fatalf("create elsewhere under the read-only export: %v, want EROFS", err)
	}
}
