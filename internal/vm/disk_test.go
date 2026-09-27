package vm

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EnsureDiskAt makes dir/vm/disk.img sparse at the size asked, grows it and
// never shrinks it; a symlink or a FIFO in its place is refused and its
// target untouched.
func TestEnsureDiskAt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".xbin", "sbx", "apps~m-1", "web")
	p, err := EnsureDiskAt(dir, 1<<30)
	if err != nil || p != filepath.Join(dir, "vm", "disk.img") {
		t.Fatalf("create: %q %v", p, err)
	}
	size := func() int64 {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("vm dir mode %v", fi.Mode())
	}
	if _, err := EnsureDiskAt(dir, 3<<30); err != nil || size() != 3<<30 {
		t.Fatalf("grow: %d %v", size(), err)
	}
	if _, err := EnsureDiskAt(dir, 1<<30); err != nil || size() != 3<<30 {
		t.Fatalf("a smaller size shrank it: %d %v", size(), err)
	}

	target := filepath.Join(root, "target")
	os.WriteFile(target, []byte("x"), 0o600)
	link := filepath.Join(root, "linked")
	os.MkdirAll(filepath.Join(link, "vm"), 0o700)
	os.Symlink(target, filepath.Join(link, "vm", "disk.img"))
	if _, err := EnsureDiskAt(link, 1<<30); err == nil {
		t.Fatal("a symlinked disk was sized")
	}
	if fi, _ := os.Stat(target); fi.Size() != 1 {
		t.Fatalf("the symlink's target grew to %d", fi.Size())
	}
}

// GC keeps the current base's image and those of pinned, installed bases;
// unknown pins (nil) keep every image. Build leftovers always go.
func TestImageGCKeepsPinned(t *testing.T) {
	root := t.TempDir()
	rootfs := filepath.Join(root, "rootfs")
	for dir, ver := range map[string]string{rootfs: "b2", rootfs + "-b1": "b1", rootfs + "-b0": "b0"} {
		os.MkdirAll(filepath.Join(dir, "etc"), 0o755)
		os.WriteFile(filepath.Join(dir, "etc", "xbin-base-version"), []byte(ver+"\n"), 0o644)
	}
	mkfs := filepath.Join(root, "mkfs.erofs")
	os.WriteFile(mkfs, []byte("#!"), 0o755)
	m := &Manager{Root: root, Rootfs: rootfs}
	m.assets.Mkfs = mkfs
	images := filepath.Join(root, ".xbin", "vm", "images")
	os.MkdirAll(images, 0o755)
	names := []string{m.imageKey(rootfs) + ".erofs", m.imageKey(rootfs+"-b1") + ".erofs",
		m.imageKey(rootfs+"-b0") + ".erofs", "b2-stale000000.erofs", ".b2-x.tmp"}
	for _, n := range names {
		os.WriteFile(filepath.Join(images, n), nil, 0o644)
	}
	left := func() string {
		ents, _ := os.ReadDir(images)
		var out []string
		for _, e := range ents {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	sorted := func(n ...string) string {
		n = append([]string(nil), n...)
		sort.Strings(n)
		return strings.Join(n, " ")
	}

	m.GC(nil)
	if got := left(); got != sorted(names[:4]...) {
		t.Fatalf("unknown pins: %s", got)
	}
	m.GC(map[string]bool{"b1": true, "gone": true})
	if got := left(); got != sorted(names[0], names[1]) {
		t.Fatalf("pinned b1: %s", got)
	}
	m.GC(map[string]bool{})
	if got := left(); got != names[0] {
		t.Fatalf("nothing pinned: %s", got)
	}
}
