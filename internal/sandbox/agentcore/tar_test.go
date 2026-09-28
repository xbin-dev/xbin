//go:build linux

package agentcore

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

type tarEnt struct {
	typ        byte
	body, link string
}

func untar(t *testing.T, b []byte) map[string]tarEnt {
	t.Helper()
	out := map[string]tarEnt{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = tarEnt{h.Typeflag, string(body), h.Linkname}
	}
}

func TestTarRoundTrip(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(h.root, "src")
	for p, body := range map[string]string{"a.txt": "A", "sub/b.txt": "B", "node_modules/x": "X", "sub/c.o": "O"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755)
		_ = os.WriteFile(filepath.Join(src, p), []byte(body), 0o640)
	}
	_ = os.Symlink("a.txt", filepath.Join(src, "link"))
	_ = os.Symlink("/etc/passwd", filepath.Join(src, "sub", "abs")) // stored, never followed
	_ = os.Link(filepath.Join(src, "a.txt"), filepath.Join(src, "hard"))
	_ = unix.Mkfifo(filepath.Join(src, "fifo"), 0o644) // not archived

	first, data, last := h.call(proto.FileOp{Op: "tar-get", Path: "/src", Exclude: []string{"node_modules", "*.o"}}, nil)
	if !first.OK || first.Stat.Type != "dir" || !last.OK {
		t.Fatalf("tar-get: %+v / %+v", first, last)
	}
	ents := untar(t, data)
	want := map[string]tarEnt{
		"a.txt":     {tar.TypeReg, "A", ""},
		"hard":      {tar.TypeLink, "", "a.txt"},
		"link":      {tar.TypeSymlink, "", "a.txt"},
		"sub/":      {tar.TypeDir, "", ""},
		"sub/abs":   {tar.TypeSymlink, "", "/etc/passwd"},
		"sub/b.txt": {tar.TypeReg, "B", ""},
	}
	if len(ents) != len(want) {
		t.Fatalf("entries %v, want %v", ents, want)
	}
	for name, w := range want {
		if ents[name] != w {
			t.Fatalf("%s: %+v, want %+v", name, ents[name], w)
		}
	}

	// back in, under a directory mkdirs creates
	h.ok(proto.FileOp{Op: "tar-put", Path: "/dst/here", Mkdirs: true}, data)
	dst := filepath.Join(h.root, "dst", "here")
	for p, body := range map[string]string{"a.txt": "A", "sub/b.txt": "B", "hard": "A"} {
		if b, err := os.ReadFile(filepath.Join(dst, p)); err != nil || string(b) != body {
			t.Fatalf("%s: %q %v", p, b, err)
		}
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "a.txt" {
		t.Fatalf("link: %q %v", l, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "sub", "abs")); err != nil || l != "/etc/passwd" {
		t.Fatalf("abs: %q %v", l, err)
	}
	var a, hard unix.Stat_t
	_ = unix.Stat(filepath.Join(dst, "a.txt"), &a)
	_ = unix.Stat(filepath.Join(dst, "hard"), &hard)
	if a.Ino != hard.Ino {
		t.Fatal("the hard link came back as a copy")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "a.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}

	// bounds, and what isn't a directory
	if _, _, last := h.call(proto.FileOp{Op: "tar-get", Path: "/src", Max: 1024}, nil); last.Refusal != proto.RefuseTooLarge {
		t.Fatalf("tar-get past Max: %+v", last)
	}
	h.refused(proto.FileOp{Op: "tar-put", Path: "/dst2", Mkdirs: true, Max: 1024}, data, proto.RefuseTooLarge)
	h.refused(proto.FileOp{Op: "tar-get", Path: "/src/a.txt"}, nil, proto.RefuseInvalid)
	h.refused(proto.FileOp{Op: "tar-put", Path: "/src/a.txt"}, data, proto.RefuseInvalid)
	h.refused(proto.FileOp{Op: "tar-put", Path: "/none"}, data, proto.RefuseNotFound)
}

func TestTarPutStaysInside(t *testing.T) {
	h := newHarness(t, nil)
	outside := filepath.Dir(h.root) // the test's own temp dir: what an escape would reach
	dst := filepath.Join(h.root, "dst")
	_ = os.Mkdir(dst, 0o755)
	_ = os.Symlink(outside, filepath.Join(dst, "pre")) // planted before the put

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	file := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	link := func(typ byte, name, target string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Mode: 0o777, Typeflag: typ})
	}
	file("../evil", "!")
	file("a/../../evil2", "!")
	file("/abs", "abs")
	link(tar.TypeSymlink, "l", "/")
	file("l/escaped1", "!")
	link(tar.TypeSymlink, "up", "../..")
	file("up/escaped2", "!")
	file("pre/escaped3", "!")
	link(tar.TypeLink, "hl", "../../outside-hard")
	file("ok/fine", "fine")
	_ = tw.Close()

	h.ok(proto.FileOp{Op: "tar-put", Path: "/dst"}, buf.Bytes())
	for _, p := range []string{
		filepath.Join(h.root, "evil"), filepath.Join(h.root, "evil2"), filepath.Join(outside, "evil"),
		filepath.Join(outside, "escaped1"), filepath.Join(outside, "escaped2"), filepath.Join(outside, "escaped3"),
		filepath.Join(h.root, "escaped2"), filepath.Join(dst, "hl"), "/escaped1",
	} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s: written outside the target", p)
		}
	}
	for p, body := range map[string]string{"abs": "abs", "ok/fine": "fine"} {
		if b, err := os.ReadFile(filepath.Join(dst, p)); err != nil || string(b) != body {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if l, err := os.Readlink(filepath.Join(dst, "l")); err != nil || l != "/" {
		t.Errorf("l: %q %v", l, err)
	}
}

// A tar of the sandbox's root leaves out /proc, /sys and /dev — also when a
// symlink leads there — and nothing else of those names deeper down.
func TestTarOfRootLeavesOutPseudoFS(t *testing.T) {
	h := newHarness(t, nil)
	for _, p := range []string{"proc/1/status", "sys/kernel/x", "dev/null-ish", "usr/include/sys/types.h", "work/proc", "etc/hostname"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(h.root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(h.root, p), []byte("x"), 0o644)
	}
	_ = os.Symlink("/", filepath.Join(h.root, "work", "root"))
	for _, p := range []string{"/", "/work/root"} {
		first, data, last := h.call(proto.FileOp{Op: "tar-get", Path: p}, nil)
		if !first.OK || !last.OK {
			t.Fatalf("tar-get %s: %+v / %+v", p, first, last)
		}
		ents := untar(t, data)
		for _, gone := range []string{"proc/", "proc/1/status", "sys/", "sys/kernel/x", "dev/", "dev/null-ish"} {
			if _, ok := ents[gone]; ok {
				t.Errorf("tar-get %s: %s archived", p, gone)
			}
		}
		for _, kept := range []string{"usr/include/sys/types.h", "work/proc", "etc/hostname"} {
			if _, ok := ents[kept]; !ok {
				t.Errorf("tar-get %s: %s left out", p, kept)
			}
		}
	}
	// below the root, a directory of that name is an ordinary one
	first, data, last := h.call(proto.FileOp{Op: "tar-get", Path: "/usr/include"}, nil)
	if !first.OK || !last.OK {
		t.Fatalf("tar-get /usr/include: %+v / %+v", first, last)
	}
	if _, ok := untar(t, data)["sys/types.h"]; !ok {
		t.Fatal("tar-get /usr/include: sys/ left out")
	}
}
