//go:build linux && integration

// Run with: go test -tags=integration ./internal/checkpoint/
// Needs user namespaces and an unpacked rootfs with git (XBIN_TEST_ROOTFS, or
// the repo's .rootfs from `make rootfs`); skips otherwise. TestMain and
// confined are hostile_linux_test.go's.
package checkpoint

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// gitIn runs host git in dir with stdin (test fixtures only).
func gitIn(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// plant writes a hand-made tree into tile's store as a retained checkpoint,
// bypassing capture and admission, and returns its id.
func plant(t *testing.T, s *Store, tilePath string, raw []byte) string {
	t.Helper()
	store := s.Dir(tilePath)
	tree := gitIn(t, store, raw, "--git-dir="+store, "hash-object", "-t", "tree", "--literally", "-w", "--stdin")
	c := gitIn(t, store, []byte("planted\n"), "--git-dir="+store, "commit-tree", tree)
	gitIn(t, store, nil, "--git-dir="+store, "update-ref", "refs/xbin/checkpoints/"+tree, c)
	s.forget(tilePath)
	return tree
}

// treeEntry is one raw tree entry: "<mode> <name>\0<20-byte id>".
func treeEntry(t *testing.T, mode, name, id string) []byte {
	t.Helper()
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 20 {
		t.Fatalf("bad id %q", id)
	}
	return append([]byte(mode+" "+name+"\x00"), b...)
}

// emptyDir fails t unless dir holds nothing.
func emptyDir(t *testing.T, dir, what string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("%s: something was written outside the tree: %v", what, names)
	}
}

// covers T2 P16 — a checkpoint of a tree with escaping symlinks (→ /etc,
// → ../../.xbin/secret, → a directory outside), a chain ending at a FIFO
// outside, a FIFO, a socket and (where the box allows) a device node
// materializes through the sandbox with directories 0755 and files 0444 or
// 0555, the links kept verbatim, no special file, and nothing followed or
// written on the host; the run's only writable place is its .tmp-*
// directory, the store bound read-only. Hand-made trees no capture admits,
// a symlink d → outside next to a directory d holding x in either order,
// write nothing through the link. A run killed mid-way leaves nothing
// visible.
func TestMaterializeSymlinkEscape(t *testing.T) {
	needGit(t) // host git plants the hand-made trees
	w := newTripwire(t)
	s, rec := testStore(t)
	outside := t.TempDir()
	src := tile(t, s, "apps/esc", map[string]string{"a.txt": "a\n", "x/y.txt": "y\n"})
	wt := src.WorkTree
	if err := os.WriteFile(filepath.Join(wt, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"etc": "/etc", "passwd": "/etc/passwd", "secret": "../../.xbin/secret", "dl": outside,
		"c1": "c2", "c2": "c3", "c3": w.path, "x/up": "../../../..",
	}
	for rel, target := range links {
		if err := os.Symlink(target, filepath.Join(wt, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(wt, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(wt, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if err := syscall.Mknod(filepath.Join(wt, "dev0"), syscall.S_IFCHR|0o600, 0); err != nil {
		t.Logf("no device node on this box (%v): the FIFO and the socket stand in", err)
	}

	confined(t)
	res := capture(t, s, src, true)
	before := len(rec.cmds)
	root, err := s.Materialize(src.Tile, res.Hash)
	if err != nil {
		t.Fatal(err)
	}
	got := materialized(t, root)
	checkModes(t, got, map[string]bool{"run.sh": true})
	for rel, target := range links {
		if e := got[rel]; e.mode.Type() != os.ModeSymlink || e.body != target {
			t.Errorf("%s: %v %q, want a symlink to %s", rel, e.mode, e.body, target)
		}
	}
	for _, rel := range []string{"pipe", "sock", "dev0"} {
		if _, ok := got[rel]; ok {
			t.Errorf("the tree holds the special file %s", rel)
		}
	}
	if got["a.txt"].body != "a\n" || got["x/y.txt"].body != "y\n" {
		t.Errorf("files: %+v %+v", got["a.txt"], got["x/y.txt"])
	}
	emptyDir(t, outside, "the directory link")
	if exists(filepath.Join(s.Root, ".xbin", "secret")) {
		t.Error("the materialization wrote through ../../.xbin/secret")
	}
	if w.tripped.Load() {
		t.Fatal("materializing opened the FIFO a chain of links ends at")
	}
	trees := s.TreesDir(src.Tile)
	for _, c := range rec.cmds[before:] {
		if !strings.Contains(c.Argv[2], "checkout-index") {
			continue
		}
		if filepath.Dir(c.Dir) != trees || !strings.HasPrefix(filepath.Base(c.Dir), tmpPrefix) || c.ReadOnlyDir {
			t.Errorf("the extraction's writable Dir is %s (read-only %v), not a fresh .tmp-* of %s", c.Dir, c.ReadOnlyDir, trees)
		}
		if len(c.Binds) != 1 || c.Binds[0] != confine.RO(s.Dir(src.Tile)) || c.Net != confine.NetNone {
			t.Errorf("the extraction binds %+v (net %v): only the store, read-only", c.Binds, c.Net)
		}
	}

	// hand-made trees: a symlink d → outside and a directory d holding x
	store := s.Dir(src.Tile)
	x := gitIn(t, store, []byte("through the link\n"), "--git-dir="+store, "hash-object", "-w", "--stdin")
	link := gitIn(t, store, []byte(outside), "--git-dir="+store, "hash-object", "-w", "--stdin")
	sub := gitIn(t, store, []byte("100644 blob "+x+"\tx\n"), "--git-dir="+store, "mktree")
	lnk, dir := treeEntry(t, "120000", "d", link), treeEntry(t, "40000", "d", sub)
	for i, raw := range [][]byte{append(append([]byte{}, lnk...), dir...), append(append([]byte{}, dir...), lnk...)} {
		tree := plant(t, s, src.Tile, raw)
		r, err := s.Materialize(src.Tile, tree)
		emptyDir(t, outside, "a hand-made tree")
		if err != nil {
			t.Logf("hand-made tree %d refused: %v", i, err)
			continue
		}
		if fi, err := os.Lstat(filepath.Join(r, "d")); err == nil && fi.Mode().Type() == os.ModeSymlink {
			if _, err := os.Lstat(filepath.Join(outside, "x")); err == nil {
				t.Errorf("hand-made tree %d: x was written through the link", i)
			}
		}
	}

	// a run killed mid-way, after the checkout and before the rename
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := capture(t, s, src, false)
	realRun := s.run
	s.run = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
		if strings.Contains(c.Argv[2], "checkout-index") {
			c.Argv = append([]string(nil), c.Argv...)
			c.Argv[2] += "exec sleep 30\n"
			c.Timeout = 3 * time.Second
		}
		return realRun(ctx, c)
	}
	if _, err := s.Materialize(src.Tile, next.Hash); err == nil {
		t.Fatal("a killed extraction succeeded")
	}
	s.run = realRun
	if exists(filepath.Join(trees, next.Hash)) || len(tmpLeft(t, trees)) > 0 {
		t.Fatalf("a killed extraction is visible: %v", tmpLeft(t, trees))
	}
	if _, err := s.Materialize(src.Tile, next.Hash); err != nil {
		t.Fatalf("after a killed run: %v", err)
	}
}

// covers P9 P16 T18 — sandboxes get a materialized tree read-only by the
// bind flag, not by host modes (07-runtime §2.6): bound read-only, a
// confined run can't create, remove, append to or chmod anything in it,
// though its directories are 0755 on the host; bound read-write (the
// control), the same uid creates a file there.
func TestMaterializeReadOnlyToSandboxes(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/ro", map[string]string{"a.txt": "a\n", "sub/b.txt": "b\n"})
	confined(t)
	res := capture(t, s, src, true)
	root, err := s.Materialize(src.Tile, res.Hash)
	if err != nil {
		t.Fatal(err)
	}
	want := materialized(t, root)
	run := func(rw bool, script string) error {
		bind := confine.RO(root)
		if rw {
			bind = confine.RW(root)
		}
		_, err := confine.Run(context.Background(), confine.Cmd{
			Argv: []string{"sh", "-c", script, "sh", root}, Dir: t.TempDir(), Binds: []sandbox.Bind{bind}, Timeout: time.Minute,
		})
		return err
	}
	for _, script := range []string{
		`: >"$1/new"`, `mkdir "$1/sub/new"`, `echo x >>"$1/a.txt"`, `chmod u+w "$1/a.txt"`, `rm -f "$1/sub/b.txt"`, `mv "$1/a.txt" "$1/c.txt"`,
	} {
		if err := run(false, script); err == nil {
			t.Errorf("a read-only bind allowed %s", script)
		}
	}
	if got := materialized(t, root); len(got) != len(want) || got["a.txt"] != want["a.txt"] || got["sub/b.txt"] != want["sub/b.txt"] {
		t.Errorf("the tree changed under a read-only bind: %v", got)
	}
	if err := run(true, `: >"$1/new"`); err != nil {
		t.Fatalf("the control: a read-write bind refused a new file (%v), so the read-only case proves nothing", err)
	}
	if err := os.Remove(filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
}

// covers T10 T1 — GC through the sandbox (ledger L7): a deploy log past 50
// entries is trimmed, an old unreferenced checkpoint and its view are
// deleted and its objects pruned, the store repacked, a kept one untouched;
// the store stays private (fsck clean, no hooks).
func TestCheckpointGCConfined(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	s.Caps.Every = 0
	base := time.Now().UTC().Truncate(time.Second)
	s.now = func() time.Time { return base.Add(-72 * time.Hour) }
	src := tile(t, s, "apps/gcc", map[string]string{"v.txt": "old\n"})
	confined(t)
	old := capture(t, s, src, true).Hash
	oldBlob := strings.TrimSpace(storeGit(t, s, src.Tile, "rev-parse", old+":v.txt"))
	if err := os.WriteFile(filepath.Join(src.WorkTree, "v.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept := capture(t, s, src, false).Hash
	for i := 0; i < 55; i++ {
		logEntry(t, s, src.Tile, "main", kept, "ok", base.Add(-50*time.Hour+time.Duration(i)*time.Minute))
	}
	ageObjects(t, s.Dir(src.Tile))
	s.now = func() time.Time { return base }
	before := len(rec.cmds)
	if err := s.GC(context.Background(), src.Tile, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.cmds) - before; n != 1 {
		t.Errorf("GC made %d confined runs, want 1", n)
	}
	if n := len(logChain(t, s, src.Tile, "main")); n != logEntriesKept {
		t.Errorf("the log holds %d entries, want %d", n, logEntriesKept)
	}
	if hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+old) || hasRef(t, s, src.Tile, "refs/xbin/views/"+old) || hasObject(t, s, src.Tile, oldBlob) {
		t.Error("the old checkpoint survived")
	}
	if !hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+kept) || !hasRef(t, s, src.Tile, "refs/xbin/views/"+kept) {
		t.Error("the logged checkpoint went")
	}
	if n := looseCount(t, s, src.Tile); n != 0 {
		t.Errorf("%d loose objects after the first GC's repack", n)
	}
	store := s.Dir(src.Tile)
	if exists(filepath.Join(store, "hooks")) || exists(filepath.Join(store, "objects", "info", "alternates")) {
		t.Error("GC left the store less private")
	}
	for _, line := range strings.Split(storeGit(t, s, src.Tile, "fsck", "--strict", "--no-dangling"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "notice:") {
			t.Errorf("git fsck --strict: %s", line)
		}
	}
}
