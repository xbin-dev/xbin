//go:build linux && integration

// Run with: go test -tags=integration ./internal/checkpoint/
// Needs user namespaces and an unpacked rootfs with git (XBIN_TEST_ROOTFS, or
// the repo's .rootfs from `make rootfs`); skips otherwise.
package checkpoint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// TestMain doubles as the sandbox's re-exec init.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

// confined turns confinement on over the test rootfs for the rest of t,
// or skips t without one.
func confined(t testing.TB) {
	t.Helper()
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "usr", "bin", "git")); err != nil || !sandbox.Available() {
		t.Skip("no rootfs with git, or no user namespaces")
	}
	confine.Configure(fs)
	t.Cleanup(func() { confine.Configure("") })
}

// hostileTile is a tile whose own repository, attributes and embedded
// repositories try every way git has to run a command or rewrite content;
// each command would write marker (a host path the sandbox never sees).
func hostileTile(t *testing.T, s *Store, path, marker string) (Source, string) {
	t.Helper()
	src := tile(t, s, path, map[string]string{
		"a.txt":     "$Id$\nline one\nline two\n",
		"b.bin":     "\x00\xff\r\n",
		"emb/e.txt": "embedded\n",
		"unb/u.txt": "unborn\n",
		"gf/x":      "behind a gitfile\n",
	})
	w := src.WorkTree
	evil := filepath.Join(w, "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+" "+shellQuote(filepath.Join(s.Dir(path), "PWNED"))+"\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := repo(t, w, "a.txt", "b.bin", "evil.sh")
	// after the tile's own commit: its git would honour these too
	if err := os.WriteFile(filepath.Join(w, ".gitattributes"), []byte("* filter=x diff=x\n*.txt text eol=crlf ident working-tree-encoding=UTF-16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(w, ".git", "evil-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"post-index-change", "pre-commit", "post-checkout", "reference-transaction"} {
		if err := os.Symlink(evil, filepath.Join(hooks, h)); err != nil {
			t.Fatal(err)
		}
	}
	inc := filepath.Join(w, ".git", "evil.inc")
	if err := os.WriteFile(inc, []byte("[core]\n\tfsmonitor = "+evil+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"core.fsmonitor", evil}, {"core.hooksPath", hooks}, {"diff.external", evil},
		{"filter.x.clean", evil}, {"filter.x.smudge", evil}, {"filter.x.process", evil},
		{"core.sshCommand", evil}, {"credential.helper", "!" + evil}, {"include.path", inc},
		{"remote.evil.url", "ext::sh -c touch% " + marker},
	} {
		hostGit(t, w, "config", kv[0], kv[1])
	}
	alt := filepath.Join(t.TempDir(), "alt", "objects")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, ".git", "objects", "info", "alternates"), []byte(alt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo(t, filepath.Join(w, "emb"))
	hostGit(t, filepath.Join(w, "unb"), "init", "-q")
	if err := os.WriteFile(filepath.Join(w, "gf", ".git"), []byte("gitdir: ../.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, head
}

// covers T1 D119g — a tile whose .git/config names fsmonitor, diff.external,
// a filter driver, a hooks path with hooks, an ext:: remote, an ssh command,
// a credential helper and an include; whose alternates point at a host path;
// whose .gitattributes name the filter, eol conversion, ident and a
// working-tree encoding; with embedded repositories (a commit, none, a
// gitfile); and a tile whose .git is a gitfile into .xbin: both are
// checkpointed, git view included, with no command run, every blob
// byte-identical to its file, and the embedded repositories captured as
// files (the re-run with their .git masked), from then on without a re-run.
func TestCaptureIgnoresTileGitConfig(t *testing.T) {
	needGit(t) // host git builds the fixtures
	s, rec := testStore(t)
	marker := filepath.Join(t.TempDir(), "PWNED")
	src, head := hostileTile(t, s, "apps/hostile", marker)

	secret := filepath.Join(s.Root, ".xbin", "secret.git")
	hostGit(t, s.Root, "init", "-q", "--bare", secret)
	hostGit(t, secret, "config", "core.fsmonitor", filepath.Join(src.WorkTree, "evil.sh"))
	gitfile := tile(t, s, "apps/gitfile", map[string]string{".git": "gitdir: " + secret + "\n", "a.txt": "a\n"})

	confined(t)
	res := capture(t, s, src, true)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tile's git configuration ran a command on the host")
	}
	if exists(filepath.Join(s.Dir("apps/hostile"), "PWNED")) {
		t.Fatal("the tile's git configuration ran a command inside the capture's sandbox")
	}
	if res.WorkTreeHead != head {
		t.Errorf("work-tree head %q, want %s", res.WorkTreeHead, head)
	}
	got := treeOf(t, s, "apps/hostile", res.Hash)
	for rel, want := range map[string]entry{
		"a.txt":     {"100644", "$Id$\nline one\nline two\n"},
		"b.bin":     {"100644", "\x00\xff\r\n"},
		"evil.sh":   {"100755", ""},
		"emb/e.txt": {"100644", "embedded\n"},
		"unb/u.txt": {"100644", "unborn\n"},
		"gf/x":      {"100644", "behind a gitfile\n"},
	} {
		if e := got[rel]; e.mode != want.mode || (want.body != "" && e.body != want.body) {
			t.Errorf("%s: %+v, want %+v", rel, e, want)
		}
	}
	for rel, e := range got {
		if e.mode == "160000" || strings.HasPrefix(rel, ".git/") || strings.Contains(rel, "/.git") {
			t.Errorf("the checkpoint holds %s (%s)", rel, e.mode)
		}
	}
	if v := strings.TrimSpace(storeGit(t, s, "apps/hostile", "rev-parse", "refs/xbin/views/"+res.Hash+"^{tree}")); v != res.Hash {
		t.Errorf("the git view %s isn't the checkpoint %s (the tile ignores nothing)", v, res.Hash)
	}

	// the embedded repositories' files are in the index now: git recurses
	// into them without a re-run
	if err := os.WriteFile(filepath.Join(src.WorkTree, "emb", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := rec.count()
	next := capture(t, s, src, false)
	if runs := rec.count() - before; runs != 2 || !next.New {
		t.Errorf("the next capture: %d runs (want run 1 and run 2), new %v", runs, next.New)
	}
	if e := treeOf(t, s, "apps/hostile", next.Hash)["emb/new.txt"]; e.body != "new\n" {
		t.Errorf("emb/new.txt: %+v", e)
	}

	gres := capture(t, s, gitfile, true)
	if gres.WorkTreeHead != "" || treeOf(t, s, "apps/gitfile", gres.Hash)["a.txt"].body != "a\n" {
		t.Errorf("a tile whose .git is a gitfile into .xbin: %+v", gres)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a command ran")
	}
}

// covers T1 T11 — the store stays private through captures of a hostile
// tile: no hooks, the constant config and attributes, no exclude file or
// alternates, nothing but git's own files; the tile's symlinks into the
// store are kept as symlinks, never written through; every run binds the
// work tree read-only; and the store passes git fsck --strict. (The view
// repository's half lands with it, WP-12; confined gc and repack with GC,
// WP-11.)
func TestCheckpointStorePrivate(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	marker := filepath.Join(t.TempDir(), "PWNED")
	src, _ := hostileTile(t, s, "apps/private", marker)
	store := s.Dir("apps/private")
	for link, target := range map[string]string{"into-hooks": filepath.Join(store, "hooks"), "into-config": filepath.Join(store, "config"), "into-store": store} {
		if err := os.Symlink(target, filepath.Join(src.WorkTree, link)); err != nil {
			t.Fatal(err)
		}
	}

	confined(t)
	res := capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "more.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture(t, s, src, false)

	got := treeOf(t, s, "apps/private", res.Hash)
	for link, target := range map[string]string{"into-hooks": filepath.Join(store, "hooks"), "into-config": filepath.Join(store, "config"), "into-store": store} {
		if got[link] != (entry{"120000", target}) {
			t.Errorf("%s: %+v, want a symlink to %s", link, got[link], target)
		}
	}
	for rel, want := range map[string]string{"config": storeConfig, "info/attributes": storeAttributes} {
		if b, err := os.ReadFile(filepath.Join(store, rel)); err != nil || string(b) != want {
			t.Errorf("the store's %s: %q (%v)", rel, b, err)
		}
	}
	top, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range top {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	allowed := map[string]bool{"HEAD": true, "config": true, "info": true, "objects": true, "refs": true, "index": true, "packed-refs": true}
	for _, n := range names {
		if !allowed[n] {
			t.Errorf("the store holds %s (it has %v)", n, names)
		}
	}
	if info, _ := os.ReadDir(filepath.Join(store, "info")); len(info) != 1 {
		t.Errorf("the store's info/ holds %d entries, want only attributes", len(info))
	}
	if exists(filepath.Join(store, "objects", "info", "alternates")) {
		t.Error("the store has alternates")
	}
	if _, err := os.Stat(marker); err == nil || exists(filepath.Join(store, "PWNED")) {
		t.Fatal("a command ran")
	}
	for i, c := range rec.cmds {
		for _, b := range c.Binds {
			if b.Src == src.WorkTree && !b.RO {
				t.Errorf("run %d binds the work tree read-write", i)
			}
		}
		if c.Dir == src.WorkTree && !c.ReadOnlyDir {
			t.Errorf("run %d has the work tree as a writable Dir", i)
		}
	}
	for _, line := range strings.Split(storeGit(t, s, "apps/private", "fsck", "--strict", "--no-dangling"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "notice:") {
			t.Errorf("git fsck --strict: %s", line)
		}
	}
}

// covers D119d — BenchmarkCapture through the sandbox (07-runtime §2.10: about
// 45 ms per confined run on fuse-overlayfs, 17 ms on kernel overlay).
func BenchmarkCaptureConfined(b *testing.B) {
	confined(b)
	benchCapture(b)
}
