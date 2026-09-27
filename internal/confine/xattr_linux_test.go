package confine

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// upperXattrs is what an upper's entries carry in user.* xattrs, which
// CopyTree must keep exactly: a plain one and a binary value, both overlay
// flavours' opaque markers (fuse-overlayfs's, a userxattr kernel
// overlay's), and ownership overrides.
var upperXattrs = map[string]map[string]string{
	"f":                  {"user.test": "kept", "user.bin": "\x00\xff\x01\n"},
	"opaque-fuse":        {"user.fuseoverlayfs.opaque": "y"},
	"opaque-kernel":      {"user.overlay.opaque": "y"},
	"opaque-fuse/nested": {"user.fuseoverlayfs.override_stat": "0:0:040755", "user.containers.override_stat": "1000:1000:0100600"},
}

// xattrTree makes upperXattrs' entries under dir; false when dir's
// filesystem takes no user xattrs.
func xattrTree(t *testing.T, dir string) bool {
	t.Helper()
	for _, d := range []string{"opaque-fuse/nested", "opaque-kernel"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, xs := range upperXattrs {
		for k, v := range xs {
			if err := unix.Setxattr(filepath.Join(dir, name), k, []byte(v), 0); errors.Is(err, unix.ENOTSUP) {
				return false
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	return true
}

// userXattrs is p's user.* xattrs and their values.
func userXattrs(t *testing.T, p string) map[string]string {
	t.Helper()
	buf := make([]byte, 64<<10)
	n, err := unix.Listxattr(p, buf)
	if err != nil {
		t.Fatalf("listxattr %s: %v", p, err)
	}
	got := map[string]string{}
	for _, k := range strings.Split(strings.TrimRight(string(buf[:n]), "\x00"), "\x00") {
		if !strings.HasPrefix(k, "user.") {
			continue
		}
		v := make([]byte, 64<<10)
		m, err := unix.Getxattr(p, k, v)
		if err != nil {
			t.Fatalf("getxattr %s %s: %v", p, k, err)
		}
		got[k] = string(v[:m])
	}
	return got
}

// checkXattrs: every entry under dst carries exactly its user xattrs.
func checkXattrs(t *testing.T, dst string) {
	t.Helper()
	for name, want := range upperXattrs {
		if got := userXattrs(t, filepath.Join(dst, name)); !maps.Equal(got, want) {
			t.Errorf("%s: xattrs %q, want %q", name, got, want)
		}
	}
}

// Direct mode (isolation off) runs the same cp: xattrs come through exactly.
func TestCopyTreeXattrsDirect(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if !xattrTree(t, src) {
		t.Skip("the temp dir's filesystem takes no user xattrs")
	}
	if err := CopyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	checkXattrs(t, dst)
}
