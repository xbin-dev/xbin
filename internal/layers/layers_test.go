package layers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func stampBase(t *testing.T, dir, ver string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, VersionFile), []byte(ver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBaseVersionAndResolve(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "abc123")
	if v := BaseVersion(cur); v != "abc123" {
		t.Fatalf("BaseVersion: %q", v)
	}
	if v := BaseVersion(mkdir(t, filepath.Join(root, "unstamped"))); v != Legacy {
		t.Fatalf("unstamped: %q", v)
	}
	if p, ok := ResolveBase(cur, "abc123"); !ok || p != cur {
		t.Fatalf("resolve current: %q %v", p, ok)
	}
	stampBase(t, cur+"-v0", "v0")
	if p, ok := ResolveBase(cur, "v0"); !ok || p != cur+"-v0" {
		t.Fatalf("resolve preserved: %q %v", p, ok)
	}
	// A version names one sibling, never a path: "v0/../../x" or "" don't
	// resolve, whatever exists.
	mkdir(t, filepath.Join(cur+"-v0", "x"))
	for _, v := range []string{"gone", "", "v0/x", "v0/../rootfs-v0"} {
		if p, ok := ResolveBase(cur, v); ok {
			t.Fatalf("%q resolved to %q", v, p)
		}
	}
}

// Stamps round-trip; a symlink in a stamp's place is refused on read and
// replaced (never followed) on write.
func TestStampRead(t *testing.T) {
	dir := mkdir(t, filepath.Join(t.TempDir(), "layer"))
	if s, err := Read(dir); err != nil || s != (Stamps{}) {
		t.Fatalf("unstamped: %+v %v", s, err)
	}
	if err := Stamp(dir, Stamps{Base: "b1", Overlay: OverlayFuse}); err != nil {
		t.Fatal(err)
	}
	if err := Stamp(dir, Stamps{Base: "b2"}); err != nil { // overlay left alone
		t.Fatal(err)
	}
	if s, err := Read(dir); err != nil || s != (Stamps{Base: "b2", Overlay: OverlayFuse}) {
		t.Fatalf("read: %+v %v", s, err)
	}
	if err := Stamp(dir, Stamps{Base: "a\nb"}); err == nil {
		t.Fatal("a multi-line stamp was written")
	}

	target := filepath.Join(filepath.Dir(dir), "target")
	os.WriteFile(target, []byte("precious\n"), 0o644)
	os.Remove(filepath.Join(dir, BaseFile))
	os.Symlink(target, filepath.Join(dir, BaseFile))
	if _, err := Read(dir); err == nil {
		t.Fatal("a symlinked stamp was read")
	}
	if err := Stamp(dir, Stamps{Base: "b3"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "precious\n" {
		t.Fatalf("the stamp write followed a symlink: %q", b)
	}
	if s, err := Read(dir); err != nil || s.Base != "b3" {
		t.Fatalf("after replacing the link: %+v %v", s, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("temp files left: %v", ents)
	}
}

// Pin: a fresh layer takes the current base and overlay; a restart after a
// base upgrade keeps its pin (the preserved sibling); another overlay
// flavour or a missing base refuses without writing.
func TestPin(t *testing.T) {
	root := t.TempDir()
	rootfs := filepath.Join(root, "rootfs")
	stampBase(t, rootfs, "b1")
	dir := mkdir(t, filepath.Join(root, ".xbin", "sbx", "apps~m-1", "web"))

	s, base, err := Pin(dir, rootfs, OverlayFuse)
	if err != nil || s != (Stamps{Base: "b1", Overlay: OverlayFuse}) || base != rootfs {
		t.Fatalf("fresh pin: %+v %q %v", s, base, err)
	}
	// The base is upgraded; b1 is preserved.
	os.Rename(rootfs, rootfs+"-b1")
	stampBase(t, rootfs, "b2")
	if s, base, err = Pin(dir, rootfs, OverlayFuse); err != nil || s.Base != "b1" || base != rootfs+"-b1" {
		t.Fatalf("restart keeps the pin: %+v %q %v", s, base, err)
	}
	if !Outdated(dir, rootfs) {
		t.Fatal("a b1 layer on a b2 host is outdated")
	}
	if _, _, err = Pin(dir, rootfs, OverlayKernel); !errors.Is(err, ErrOverlay) {
		t.Fatalf("flavour change: %v", err)
	}
	os.RemoveAll(rootfs + "-b1")
	if _, _, err = Pin(dir, rootfs, OverlayFuse); !errors.Is(err, ErrBaseMissing) {
		t.Fatalf("missing base: %v", err)
	}
	if s, _ := Read(dir); s != (Stamps{Base: "b1", Overlay: OverlayFuse}) {
		t.Fatalf("a refused pin wrote: %+v", s)
	}
	// A reset re-stamps; the next pin takes it.
	if err := Stamp(dir, Stamps{Base: BaseVersion(rootfs), Overlay: OverlayKernel}); err != nil {
		t.Fatal(err)
	}
	if s, base, err = Pin(dir, rootfs, OverlayKernel); err != nil || s.Base != "b2" || base != rootfs {
		t.Fatalf("after a reset: %+v %q %v", s, base, err)
	}
	if Outdated(dir, rootfs) {
		t.Fatal("a reset layer is current")
	}
	// VM mode pins no overlay.
	vmDir := mkdir(t, filepath.Join(root, ".xbin", "sbx", "apps~m-1", "vmbox"))
	if s, _, err := Pin(vmDir, rootfs, ""); err != nil || s != (Stamps{Base: "b2"}) {
		t.Fatalf("vm pin: %+v %v", s, err)
	}
	if _, _, err := Pin(vmDir, "", ""); err == nil {
		t.Fatal("a pin without a rootfs")
	}
}
