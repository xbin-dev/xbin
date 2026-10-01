//go:build linux

package agentcore

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// call runs one file operation: body (write, tar-put) goes out in frames
// with the terminator; the first answer line, the data frames of a read or
// tar-get and its last line come back.
func (h *harness) call(op proto.FileOp, body []byte) (first proto.FileResult, data []byte, last proto.FileResult) {
	h.t.Helper()
	c := h.dial(proto.Hello{Kind: "file", File: &op})
	_ = c.SetDeadline(time.Now().Add(hangGuard))
	pc := proto.NewConn(c, nil)
	if op.Op == "write" || op.Op == "tar-put" {
		go func() {
			fw := proto.NewFrameWriter(c)
			_, _ = fw.Write(body)
			_ = fw.Close()
		}()
	}
	if err := pc.RecvMax(&first, proto.MaxResult); err != nil {
		h.t.Fatalf("%s %s: %v", op.Op, op.Path, err)
	}
	if (op.Op == "read" || op.Op == "tar-get") && first.OK {
		var err error
		if data, err = io.ReadAll(proto.NewFrameReader(pc.Reader())); err != nil {
			h.t.Fatalf("%s %s: the data: %v", op.Op, op.Path, err)
		}
		if err := pc.RecvMax(&last, proto.MaxResult); err != nil {
			h.t.Fatalf("%s %s: the last line: %v", op.Op, op.Path, err)
		}
	}
	return first, data, last
}

func (h *harness) ok(op proto.FileOp, body []byte) proto.FileResult {
	h.t.Helper()
	r, _, _ := h.call(op, body)
	if !r.OK {
		h.t.Fatalf("%s %s: %+v", op.Op, op.Path, r)
	}
	return r
}

func (h *harness) refused(op proto.FileOp, body []byte, refusal string) proto.FileResult {
	h.t.Helper()
	r, _, _ := h.call(op, body)
	if r.OK || r.Refusal != refusal {
		h.t.Fatalf("%s %s: %+v, want the refusal %q", op.Op, op.Path, r, refusal)
	}
	return r
}

func (h *harness) read(op proto.FileOp) string {
	h.t.Helper()
	op.Op = "read"
	first, data, last := h.call(op, nil)
	if !first.OK || !last.OK {
		h.t.Fatalf("read %s: %+v / %+v", op.Path, first, last)
	}
	return string(data)
}

func write(p, body string) (proto.FileOp, []byte) {
	return proto.FileOp{Op: "write", Path: p}, []byte(body)
}

func TestFileReadWrite(t *testing.T) {
	h := newHarness(t, nil)
	st1 := h.ok(write("/a.txt", "one")).Stat
	if st1.Type != "file" || st1.Size != 3 || st1.ETag == "" || st1.Path != "/a.txt" || st1.Mode != 0o644 {
		t.Fatalf("write: %+v", st1)
	}
	first, data, last := h.call(proto.FileOp{Op: "read", Path: "/a.txt"}, nil)
	if string(data) != "one" || first.Stat.ETag != st1.ETag || !last.OK {
		t.Fatalf("read: %q %+v %+v", data, first, last)
	}
	if st := h.ok(proto.FileOp{Op: "stat", Path: "/a.txt"}, nil).Stat; st.ETag != st1.ETag {
		t.Fatalf("stat etag %s, the write's %s", st.ETag, st1.ETag)
	}
	st2 := h.ok(write("/a.txt", "two")).Stat
	if st2.ETag == st1.ETag {
		t.Fatal("the etag didn't change with the content")
	}

	// ranged reads, and Max
	h.ok(write("/digits", "0123456789"))
	for _, c := range []struct {
		off, n int64
		want   string
	}{{2, 3, "234"}, {8, 0, "89"}, {0, 2, "01"}, {20, 0, ""}} {
		if got := h.read(proto.FileOp{Path: "/digits", Offset: c.off, Length: c.n}); got != c.want {
			t.Errorf("read %d+%d: %q, want %q", c.off, c.n, got, c.want)
		}
	}
	if r := h.refused(proto.FileOp{Op: "read", Path: "/digits", Max: 5}, nil, proto.RefuseTooLarge); r.Stat == nil || r.Stat.Size != 10 {
		t.Fatalf("too-large carries the stat: %+v", r)
	}
	if got := h.read(proto.FileOp{Path: "/digits", Offset: 8, Max: 5}); got != "89" {
		t.Fatalf("a range under Max: %q", got)
	}

	// preconditions
	op, body := write("/a.txt", "three")
	op.IfMatch = st1.ETag
	if r := h.refused(op, body, proto.RefusePrecondition); r.Stat == nil || r.Stat.ETag != st2.ETag {
		t.Fatalf("a stale ifMatch carries the current etag: %+v", r)
	}
	op.IfMatch = `"` + st2.ETag + `"` // as an ETag header quotes it
	h.ok(op, body)
	op, body = write("/a.txt", "four")
	op.Create = true
	h.refused(op, body, proto.RefusePrecondition)
	op.Path = "/new.txt"
	h.ok(op, body)
	if got := h.read(proto.FileOp{Path: "/a.txt"}); got != "three" {
		t.Fatalf("content %q", got)
	}

	// modes, mkdirs
	op, body = write("/x.sh", "#!/bin/sh")
	op.Mode = 0o755
	if st := h.ok(op, body).Stat; st.Mode != 0o755 {
		t.Fatalf("mode %o", st.Mode)
	}
	if st := h.ok(write("/x.sh", "#!/bin/sh\n")).Stat; st.Mode != 0o755 {
		t.Fatalf("a replaced file keeps its mode: %o", st.Mode)
	}
	op, body = write("/d1/d2/f", "deep")
	h.refused(op, body, proto.RefuseNotFound)
	op.Mkdirs = true
	h.ok(op, body)
	if b, err := os.ReadFile(filepath.Join(h.root, "d1/d2/f")); err != nil || string(b) != "deep" {
		t.Fatalf("%q %v", b, err)
	}

	// what isn't a file
	h.refused(proto.FileOp{Op: "read", Path: "/d1"}, nil, proto.RefuseInvalid)
	h.refused(proto.FileOp{Op: "read", Path: "/none"}, nil, proto.RefuseNotFound)
	h.refused(proto.FileOp{Op: "stat", Path: "/none"}, nil, proto.RefuseNotFound)
	op, body = write("/d1", "x")
	h.refused(op, body, proto.RefuseInvalid)
	for _, p := range []string{"", "a.txt", "./a"} {
		h.refused(proto.FileOp{Op: "stat", Path: p}, nil, proto.RefuseInvalid)
	}
}

func TestFileSpecialsRefused(t *testing.T) {
	h := newHarness(t, nil)
	if err := unix.Mkfifo(filepath.Join(h.root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(h.root, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, p := range []string{"/fifo", "/sock"} { // a FIFO would block an open
		h.refused(proto.FileOp{Op: "read", Path: p}, nil, proto.RefuseInvalid)
		if st := h.ok(proto.FileOp{Op: "stat", Path: p}, nil).Stat; st.Type != "other" {
			t.Fatalf("stat %s: %+v", p, st)
		}
	}
}

// Symlinks resolve inside the root, wherever they point.
func TestFileSymlinksStayInside(t *testing.T) {
	h := newHarness(t, nil)
	etc := filepath.Join(h.root, "etc")
	_ = os.Mkdir(etc, 0o755)
	_ = os.WriteFile(filepath.Join(etc, "hostname"), []byte("inside"), 0o644)
	for name, target := range map[string]string{"abs": "/etc/hostname", "up": "../../../../../../etc/hostname", "etcdir": "/etc"} {
		if err := os.Symlink(target, filepath.Join(h.root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/abs", "/up", "/etcdir/hostname", "/../../etc/hostname"} {
		if got := h.read(proto.FileOp{Path: p}); got != "inside" {
			t.Fatalf("read %s: %q (want the root's own file)", p, got)
		}
	}
	if st := h.ok(proto.FileOp{Op: "stat", Path: "/abs"}, nil).Stat; st.Type != "symlink" || st.Target != "/etc/hostname" {
		t.Fatalf("stat of a symlink: %+v", st)
	}
	// a write through a symlinked parent lands in the root's /etc
	h.ok(write("/etcdir/x", "planted"))
	if b, err := os.ReadFile(filepath.Join(etc, "x")); err != nil || string(b) != "planted" {
		t.Fatalf("%q %v", b, err)
	}
	// a write onto a symlink replaces the link, not what it points at
	h.ok(write("/abs", "replaced"))
	if b, _ := os.ReadFile(filepath.Join(etc, "hostname")); string(b) != "inside" {
		t.Fatalf("the write went through the link: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(h.root, "abs")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("abs: %v %v", fi, err)
	}
}

func TestWriteNeedsTheTerminator(t *testing.T) {
	h := newHarness(t, nil)
	op := proto.FileOp{Op: "write", Path: "/nt"}
	c := h.dial(proto.Hello{Kind: "file", File: &op})
	_ = c.SetDeadline(time.Now().Add(hangGuard))
	if err := proto.WriteFrame(c, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	_ = c.CloseWrite() // the connection ends, no terminator
	var r proto.FileResult
	if err := proto.NewConn(c, nil).RecvMax(&r, proto.MaxResult); err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Error == "" {
		t.Fatalf("an unterminated write: %+v", r)
	}
	if ents, _ := os.ReadDir(h.root); len(ents) != 0 {
		t.Fatalf("an unterminated write left %v", ents)
	}

	// over Max: nothing either
	op = proto.FileOp{Op: "write", Path: "/big", Max: 4}
	h.refused(op, []byte("0123456789"), proto.RefuseTooLarge)
	if ents, _ := os.ReadDir(h.root); len(ents) != 0 {
		t.Fatalf("a refused write left %v", ents)
	}
}

func TestFileTree(t *testing.T) {
	h := newHarness(t, nil)
	h.ok(write("/a", "A"))
	h.ok(write("/b", "B"))
	h.ok(proto.FileOp{Op: "mkdir", Path: "/sub"}, nil)
	h.refused(proto.FileOp{Op: "mkdir", Path: "/sub"}, nil, proto.RefuseInvalid)
	h.refused(proto.FileOp{Op: "mkdir", Path: "/p/q/r"}, nil, proto.RefuseNotFound)
	h.ok(proto.FileOp{Op: "mkdir", Path: "/p/q/r", Parents: true, Mode: 0o700}, nil)
	h.ok(proto.FileOp{Op: "mkdir", Path: "/p/q/r", Parents: true}, nil)
	if st := h.ok(proto.FileOp{Op: "stat", Path: "/p/q/r"}, nil).Stat; st.Type != "dir" || st.Mode != 0o700 {
		t.Fatalf("mkdir: %+v", st)
	}

	l := h.ok(proto.FileOp{Op: "list", Path: "/"}, nil)
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name+":"+e.Type)
	}
	if got := strings.Join(names, " "); got != "a:file b:file p:dir sub:dir" || l.Truncated {
		t.Fatalf("list: %s (truncated %v)", got, l.Truncated)
	}
	if l := h.ok(proto.FileOp{Op: "list", Path: "/", Limit: 1}, nil); len(l.Entries) != 1 || !l.Truncated {
		t.Fatalf("list limit 1: %+v", l)
	}
	h.refused(proto.FileOp{Op: "list", Path: "/a"}, nil, proto.RefuseInvalid)

	// remove
	h.ok(proto.FileOp{Op: "remove", Path: "/a"}, nil)
	h.refused(proto.FileOp{Op: "stat", Path: "/a"}, nil, proto.RefuseNotFound)
	h.refused(proto.FileOp{Op: "remove", Path: "/p"}, nil, proto.RefuseInvalid)
	h.ok(proto.FileOp{Op: "remove", Path: "/p", Recursive: true}, nil)
	h.refused(proto.FileOp{Op: "remove", Path: "/p"}, nil, proto.RefuseNotFound)
	h.refused(proto.FileOp{Op: "remove", Path: "/"}, nil, proto.RefuseInvalid)
	// a recursive remove never follows a symlink
	_ = os.MkdirAll(filepath.Join(h.root, "keep"), 0o755)
	_ = os.WriteFile(filepath.Join(h.root, "keep", "k"), []byte("k"), 0o644)
	_ = os.MkdirAll(filepath.Join(h.root, "rm", "deep"), 0o755)
	for i := range 3000 { // more than one readdir batch
		_ = os.WriteFile(filepath.Join(h.root, "rm", "deep", fmt.Sprintf("f%04d", i)), nil, 0o644)
	}
	_ = os.Symlink("../keep", filepath.Join(h.root, "rm", "link"))
	_ = os.Symlink("/keep", filepath.Join(h.root, "rm", "abs"))
	h.ok(proto.FileOp{Op: "remove", Path: "/rm", Recursive: true}, nil)
	if _, err := os.Stat(filepath.Join(h.root, "keep", "k")); err != nil {
		t.Fatalf("a recursive remove followed a symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.root, "rm")); !os.IsNotExist(err) {
		t.Fatalf("rm is still there: %v", err)
	}

	// move
	h.ok(proto.FileOp{Op: "move", Path: "/b", To: "/sub/c"}, nil)
	if got := h.read(proto.FileOp{Path: "/sub/c"}); got != "B" {
		t.Fatalf("moved: %q", got)
	}
	h.ok(write("/d", "D"))
	if r := h.refused(proto.FileOp{Op: "move", Path: "/sub/c", To: "/d"}, nil, proto.RefusePrecondition); r.Stat == nil {
		t.Fatalf("a refused move names what is there: %+v", r)
	}
	if got := h.read(proto.FileOp{Path: "/d"}); got != "D" {
		t.Fatalf("a refused move changed the target: %q", got)
	}
	h.ok(proto.FileOp{Op: "move", Path: "/sub/c", To: "/d", Overwrite: true}, nil)
	if got := h.read(proto.FileOp{Path: "/d"}); got != "B" {
		t.Fatalf("overwritten: %q", got)
	}
	h.refused(proto.FileOp{Op: "move", Path: "/none", To: "/e"}, nil, proto.RefuseNotFound)
	h.refused(proto.FileOp{Op: "move", Path: "/d", To: "e"}, nil, proto.RefuseInvalid)
	h.refused(proto.FileOp{Op: "move", Path: "/", To: "/e"}, nil, proto.RefuseInvalid)
}

// A listing's line stays under proto.MaxResult however its names and link
// targets escape in JSON (a control byte is six bytes there).
func TestListBudgetCountsEscapes(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(h.root, "many")
	_ = os.Mkdir(dir, 0o755)
	target := strings.Repeat("\x01", 4000)
	for i := range 400 {
		if err := os.Symlink(target, filepath.Join(dir, fmt.Sprintf("l%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	l := h.ok(proto.FileOp{Op: "list", Path: "/many"}, nil) // fails past MaxResult
	if !l.Truncated || len(l.Entries) == 0 || l.Entries[0].Target != target {
		t.Fatalf("%d entries, truncated %v", len(l.Entries), l.Truncated)
	}
}
