//go:build linux

package fusefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

func newFS(t *testing.T, root string, ro bool, nested map[string]bool) *FS {
	t.Helper()
	fs, err := New(root, ro, nested)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fs.Close)
	return fs
}

func look(t *testing.T, fs *FS, parent uint64, name string) (fuse.EntryOut, fuse.Status) {
	t.Helper()
	var out fuse.EntryOut
	st := fs.Lookup(nil, &fuse.InHeader{NodeId: parent}, name, &out)
	return out, st
}

func TestLookupStaysBeneath(t *testing.T) {
	ws, secret := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(secret, "key"), []byte("s3cret"), 0o600)
	os.Symlink(secret, filepath.Join(ws, "escape"))
	fs := newFS(t, ws, false, nil)

	l, st := look(t, fs, fuse.FUSE_ROOT_ID, "escape")
	if !st.Ok() || l.NodeId == 0 || l.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Fatalf("the symlink should be a symlink node: %v %o", st, l.Mode)
	}
	if out, st := look(t, fs, l.NodeId, "key"); st.Ok() && out.NodeId != 0 {
		t.Fatalf("walked through a symlink out of the export")
	}
	for _, bad := range []string{"..", ".", "a/b", ""} {
		if out, st := look(t, fs, fuse.FUSE_ROOT_ID, bad); st.Ok() && out.NodeId != 0 {
			t.Errorf("looked up %q", bad)
		}
	}
	// readlink returns the target for the guest kernel to resolve itself
	target, st := fs.Readlink(nil, &fuse.InHeader{NodeId: l.NodeId})
	if !st.Ok() || string(target) != secret {
		t.Fatalf("readlink = %q %v", target, st)
	}
}

func TestNegativeLookupIsCached(t *testing.T) {
	fs := newFS(t, t.TempDir(), false, nil)
	out, st := look(t, fs, fuse.FUSE_ROOT_ID, "missing")
	if !st.Ok() || out.NodeId != 0 || out.EntryTimeout() == 0 {
		t.Fatalf("a missing name should be a cached negative entry: %v %+v", st, out)
	}
}

func TestReadOnlyAndNestedExports(t *testing.T) {
	ws := t.TempDir()
	t1 := filepath.Join(ws, "tiles", "t1")
	os.MkdirAll(t1, 0o755)
	os.MkdirAll(filepath.Join(ws, "tiles", "t2"), 0o755)
	os.WriteFile(filepath.Join(ws, "f"), []byte("x"), 0o644)
	fs := newFS(t, ws, true, map[string]bool{t1: false})

	caller := fuse.Caller{}
	if st := fs.Mkdir(nil, &fuse.MkdirIn{InHeader: fuse.InHeader{NodeId: fuse.FUSE_ROOT_ID, Caller: caller}, Mode: 0o755}, "new", &fuse.EntryOut{}); st != fuse.EROFS {
		t.Errorf("mkdir in a read-only export: %v, want EROFS", st)
	}
	f, _ := look(t, fs, fuse.FUSE_ROOT_ID, "f")
	if _, st := fs.Write(nil, &fuse.WriteIn{InHeader: fuse.InHeader{NodeId: f.NodeId}}, []byte("y")); st != fuse.EROFS {
		t.Errorf("write in a read-only export: %v, want EROFS", st)
	}
	tiles, _ := look(t, fs, fuse.FUSE_ROOT_ID, "tiles")
	n1, _ := look(t, fs, tiles.NodeId, "t1")
	n2, _ := look(t, fs, tiles.NodeId, "t2")
	var out fuse.EntryOut
	if st := fs.Mkdir(nil, &fuse.MkdirIn{InHeader: fuse.InHeader{NodeId: n1.NodeId}, Mode: 0o755}, "ok", &out); !st.Ok() {
		t.Errorf("mkdir in the writable nested export: %v", st)
	}
	if st := fs.Mkdir(nil, &fuse.MkdirIn{InHeader: fuse.InHeader{NodeId: n2.NodeId}, Mode: 0o755}, "no", &out); st != fuse.EROFS {
		t.Errorf("mkdir elsewhere under the read-only export: %v, want EROFS", st)
	}
}

func TestWriteReadAndNoDevices(t *testing.T) {
	dir := t.TempDir()
	fs := newFS(t, dir, false, nil)
	var e fuse.EntryOut
	if st := fs.Mknod(nil, &fuse.MknodIn{InHeader: fuse.InHeader{NodeId: fuse.FUSE_ROOT_ID}, Mode: unix.S_IFCHR | 0o600, Rdev: 1}, "dev", &e); st != fuse.EPERM {
		t.Errorf("a device node: %v, want EPERM", st)
	}
	if st := fs.Mknod(nil, &fuse.MknodIn{InHeader: fuse.InHeader{NodeId: fuse.FUSE_ROOT_ID}, Mode: unix.S_IFREG | 0o644}, "f", &e); !st.Ok() {
		t.Fatal(st)
	}
	if n, st := fs.Write(nil, &fuse.WriteIn{InHeader: fuse.InHeader{NodeId: e.NodeId}}, []byte("hello")); !st.Ok() || n != 5 {
		t.Fatalf("write %d %v", n, st)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "f")); string(b) != "hello" {
		t.Fatalf("host sees %q", b)
	}
	buf := make([]byte, 16)
	res, st := fs.Read(nil, &fuse.ReadIn{InHeader: fuse.InHeader{NodeId: e.NodeId}, Size: 16}, buf)
	if !st.Ok() {
		t.Fatal(st)
	}
	got, _ := res.Bytes(buf)
	if string(got) != "hello" {
		t.Fatalf("read %q", got)
	}
	// directory listing, with attributes
	list := fuse.NewDirEntryList(make([]byte, 4096), 0)
	if st := fs.ReadDirPlus(nil, &fuse.ReadIn{InHeader: fuse.InHeader{NodeId: fuse.FUSE_ROOT_ID}, Size: 4096}, list); !st.Ok() {
		t.Fatal(st)
	}
}
