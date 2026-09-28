package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers P1 T2 — what a checkpoint holds follows the glossary: every file
// but .git directories (at any depth) and nested components, gitignored
// files included; symlinks as symlinks, whatever they point at; the exec bit
// kept; empty directories, other permission bits, xattrs and special files
// dropped, the special files with a warning. An embedded repository is
// captured as files only under isolation (hostile_linux_test.go): in direct
// mode, where confine can't mask its .git, the capture is refused, naming
// it, and never drops its files.
func TestCheckpointContent(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/a", map[string]string{
		"a.txt":               "hello\n",
		"run.sh":              "#!/bin/sh\necho hi\n",
		"private":             "secret\n",
		"tool":                "#!/bin/sh\n",
		".gitignore":          "ignored.txt\nbuild/\n",
		"ignored.txt":         "still here\n",
		"build/out.bin":       "\x00\x01binary",
		"emptydir/":           "",
		"deep/x/.git/junk":    "not a repository",
		"deep/x/file":         "kept\n",
		"sub/comp/xbin.json":  `{"runtime":"static"}`,
		"sub/comp/index.html": "nested component\n",
		"sub/own.txt":         "the tile's own\n",
	})
	w := src.WorkTree
	head := repo(t, w, "a.txt")
	for p, mode := range map[string]os.FileMode{"run.sh": 0o755, "private": 0o600, "tool": 0o700} {
		if err := os.Chmod(filepath.Join(w, p), mode); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"abs": "/etc/passwd", "up": "../../outside/x"} {
		if err := os.Symlink(target, filepath.Join(w, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(w, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(w, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_ = syscall.Setxattr(filepath.Join(w, "a.txt"), "user.xbin-test", []byte("x"), 0) // where the filesystem allows it
	src.Nested = []string{"sub/comp"}

	res := capture(t, s, src, true)
	if !res.New || res.Feed != FeedWorkTree || res.By != "user:ana" || res.WorkTreeHead != head {
		t.Fatalf("result: %+v", res)
	}
	got := treeOf(t, s, "apps/a", res.Hash)
	want := map[string]entry{
		"a.txt":         {"100644", "hello\n"},
		"run.sh":        {"100755", "#!/bin/sh\necho hi\n"},
		"private":       {"100644", "secret\n"},
		"tool":          {"100755", "#!/bin/sh\n"},
		".gitignore":    {"100644", "ignored.txt\nbuild/\n"},
		"ignored.txt":   {"100644", "still here\n"},
		"build/out.bin": {"100644", "\x00\x01binary"},
		"deep/x/file":   {"100644", "kept\n"},
		"sub/own.txt":   {"100644", "the tile's own\n"},
		"abs":           {"120000", "/etc/passwd"},
		"up":            {"120000", "../../outside/x"},
	}
	for p, e := range want {
		if got[p] != e {
			t.Errorf("%s: %+v, want %+v", p, got[p], e)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected entry %s", p) // .git/…, deep/x/.git/…, sub/comp/…, fifo, sock, emptydir
		}
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "fifo") || !strings.Contains(res.Warnings[0], "sock") {
		t.Errorf("warnings: %q, want the FIFO and the socket named", res.Warnings)
	}

	// an embedded repository, with a commit or none: direct mode refuses,
	// naming both, and a refused first capture leaves no store
	emb := tile(t, s, "apps/b", map[string]string{"top.txt": "x\n", "emb/e.txt": "e\n", "unb/u.txt": "u\n"})
	repo(t, filepath.Join(emb.WorkTree, "emb"))
	hostGit(t, filepath.Join(emb.WorkTree, "unb"), "init", "-q")
	_, err = s.Capture(context.Background(), CaptureRequest{Source: emb, By: "user:ana", Create: true})
	var r *Refusal
	if !errors.As(err, &r) || r.Rule != RuleEmbedded || !errors.Is(err, ErrRefused) ||
		!strings.Contains(err.Error(), "emb") || !strings.Contains(err.Error(), "unb") || !strings.Contains(err.Error(), "--isolate") {
		t.Fatalf("embedded repositories without isolation: %v", err)
	}
	if s.Exists("apps/b") {
		t.Error("a refused first capture left a store behind")
	}
}

// covers P1 — ids: "c:" and at least 7 hex digits of the tree, lengthened
// until unique; the full hash in refs; identical content is the same
// checkpoint, and capturing it again is free and idempotent.
func TestCheckpointIDs(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/ids", map[string]string{"a.txt": "one\n"})
	first := capture(t, s, src, true)
	if !regexp.MustCompile(`^c:[0-9a-f]{7}$`).MatchString(first.ID) || !fullID(first.Hash) || !strings.HasPrefix(first.Hash, first.ID[2:]) {
		t.Fatalf("id %q, hash %q", first.ID, first.Hash)
	}
	refs := storeGit(t, s, "apps/ids", "for-each-ref", "--format=%(refname)")
	for _, ref := range []string{"refs/xbin/checkpoints/" + first.Hash, "refs/xbin/views/" + first.Hash} {
		if !strings.Contains(refs, ref+"\n") {
			t.Errorf("no %s in %q", ref, refs)
		}
	}
	if tree := strings.TrimSpace(storeGit(t, s, "apps/ids", "rev-parse", "refs/xbin/checkpoints/"+first.Hash+"^{tree}")); tree != first.Hash {
		t.Errorf("the retention commit's tree is %s, not the checkpoint %s", tree, first.Hash)
	}

	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	again := capture(t, s, src, false)
	if again.New || again.Hash != first.Hash || again.ID != first.ID || !again.At.Equal(first.At) || again.By != first.By {
		t.Fatalf("re-capturing the same tree: %+v, first %+v", again, first)
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := capture(t, s, src, false)
	if !second.New || second.Hash == first.Hash {
		t.Fatalf("a changed tree: %+v", second)
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if back := capture(t, s, src, false); back.New || back.Hash != first.Hash {
		t.Fatalf("the tree changed back: %+v, want %s again", back, first.Hash)
	}

	ctx := context.Background()
	for _, id := range []string{first.ID, "c:" + first.Hash[:12], "c:" + first.Hash} {
		if c, err := s.Resolve(ctx, "apps/ids", id); err != nil || c.Hash != first.Hash {
			t.Errorf("Resolve(%s) = %+v, %v", id, c, err)
		}
	}
	for _, id := range []string{"", "3f2a1c9", "c:3f2a1c", "c:3F2A1C9", "c:3f2a1c9x", "c:" + strings.Repeat("a", 65)} {
		if _, err := s.Resolve(ctx, "apps/ids", id); !errors.Is(err, ErrBadID) {
			t.Errorf("Resolve(%q): %v, want ErrBadID", id, err)
		}
	}
	unknown := "c:0000000"
	if strings.HasPrefix(first.Hash, "0000000") || strings.HasPrefix(second.Hash, "0000000") {
		unknown = "c:fffffff"
	}
	if _, err := s.Resolve(ctx, "apps/ids", unknown); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("Resolve(unknown): %v", err)
	}
	if _, err := s.Resolve(ctx, "apps/other", first.ID); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("an id resolved in another tile's store: %v", err)
	}
	list, err := s.List(ctx, "apps/ids")
	if err != nil || len(list) != 2 {
		t.Fatalf("List: %+v, %v", list, err)
	}

	// lengthened until unique, and ambiguity refused: the pure rule, over
	// trees made to share prefixes
	a, b, c := "abcdef0"+strings.Repeat("1", 33), "abcdef0"+strings.Repeat("2", 33), "abcdef9"+strings.Repeat("3", 33)
	known := map[string]meta{a: {}, b: {}, c: {}}
	if got := shortID(a, known); got != "c:abcdef01" {
		t.Errorf("shortID = %s, want c:abcdef01", got)
	}
	if got := shortID(c, known); got != "c:abcdef9" {
		t.Errorf("shortID = %s, want c:abcdef9", got)
	}
	s.remember("apps/amb", known)
	_, err = s.Resolve(ctx, "apps/amb", "c:abcdef0")
	if !errors.Is(err, ErrAmbiguousID) || err.Error() != "checkpoint id c:abcdef0 is ambiguous in apps/amb; use more digits" {
		t.Errorf("an ambiguous prefix: %v", err)
	}
	if got, err := s.Resolve(ctx, "apps/amb", "c:abcdef02"); err != nil || got.Hash != b || got.ID != "c:abcdef02" {
		t.Errorf("Resolve(c:abcdef02) = %+v, %v", got, err)
	}
}

// covers P8 — the persistent private index: re-capturing an unchanged
// 5 000-file tree writes no object and takes one confined run.
func TestCheckpointIncremental(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	files := map[string]string{}
	for d := 0; d < 50; d++ {
		for f := 0; f < 100; f++ {
			files[fmt.Sprintf("d%02d/f%03d.txt", d, f)] = fmt.Sprintf("%d %d\n", d, f)
		}
	}
	src := tile(t, s, "apps/big", files)
	first := capture(t, s, src, true)
	objects := looseObjects(t, s.Dir("apps/big"))
	if objects < 5000 {
		t.Fatalf("%d loose objects after the first capture", objects)
	}
	before := rec.count()
	again := capture(t, s, src, false)
	if runs := rec.count() - before; runs != 1 {
		t.Errorf("re-capturing an unchanged tree took %d confined runs, want 1", runs)
	}
	if again.New || again.Hash != first.Hash {
		t.Errorf("unchanged tree: %+v", again)
	}
	if n := looseObjects(t, s.Dir("apps/big")); n != objects {
		t.Errorf("an unchanged re-capture wrote objects: %d → %d", objects, n)
	}
	if exists(filepath.Join(s.Dir("apps/big"), "quarantine")) {
		t.Error("the quarantine outlived the capture")
	}

	// ten changed files: run 1 and run 2, and only their objects are new
	for f := 0; f < 10; f++ {
		if err := os.WriteFile(filepath.Join(src.WorkTree, fmt.Sprintf("d07/f%03d.txt", f)), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before = rec.count()
	changed := capture(t, s, src, false)
	if runs := rec.count() - before; runs != 2 || !changed.New {
		t.Errorf("ten changed files: %d runs, new %v", runs, changed.New)
	}
	if n := looseObjects(t, s.Dir("apps/big")); n-objects > 5 {
		// one blob (the ten files are equal), the d07 tree, the root tree,
		// the view's root tree, two commits — the view's d07 is the same
		t.Errorf("ten changed files wrote %d objects", n-objects)
	}
}

// covers T2 T10 — the file-count and byte caps refuse, naming the cap: before
// a first capture from the stat-only estimate (no store is left), and at
// admission once the store has an index (the store is unchanged, and the
// index goes, so the next capture re-hashes).
func TestCaptureCapsRefuseHugeTree(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	files := map[string]string{}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("node_modules/p%02d/index.js", i)] = strings.Repeat("x", 100)
	}
	files["app.js"] = "main\n"
	src := tile(t, s, "apps/huge", files)
	ctx := context.Background()
	refused := func(rule string, create bool) *Refusal {
		t.Helper()
		_, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana", Create: create})
		var r *Refusal
		if !errors.As(err, &r) || r.Rule != rule || !errors.Is(err, ErrRefused) {
			t.Fatalf("want a %s refusal, got %v", rule, err)
		}
		if !strings.Contains(err.Error(), "cap") || !strings.Contains(err.Error(), "node_modules/") || !strings.Contains(err.Error(), "resource") {
			t.Errorf("the refusal doesn't name the cap, the largest directory and the way out: %v", err)
		}
		return r
	}

	s.Caps.Entries = 50
	refused(RuleEntries, true)
	s.Caps.Entries, s.Caps.Bytes = 1000, 1000
	refused(RuleBytes, true)
	s.Caps.Bytes, s.Caps.LargestFile = 1<<20, 50
	refused(RuleLargestFile, true)
	if s.Exists("apps/huge") || exists(filepath.Join(s.Root, "data", "checkpoints")) {
		t.Fatal("a refused first capture left a store behind")
	}

	s.Caps = DefaultCaps()
	first := capture(t, s, src, true)
	refsBefore := storeGit(t, s, "apps/huge", "for-each-ref")
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(src.WorkTree, "node_modules", fmt.Sprintf("more%02d.js", i)), []byte("y"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.Caps.Entries = 100
	refused(RuleEntries, false)
	if refs := storeGit(t, s, "apps/huge", "for-each-ref"); refs != refsBefore {
		t.Errorf("a refused capture changed the store's refs:\n%s\nwas\n%s", refs, refsBefore)
	}
	if exists(filepath.Join(s.Dir("apps/huge"), "index")) || exists(filepath.Join(s.Dir("apps/huge"), "quarantine")) {
		t.Error("a refused capture kept the index or the quarantine")
	}
	s.Caps = DefaultCaps()
	if res := capture(t, s, src, false); res.Hash == first.Hash || !res.New {
		t.Errorf("after the refusal: %+v", res)
	}
}

// covers T10 — captures per tile: a burst of 10, then one every 3 s; past
// that a 429's RetryAfter, and nothing runs. Estimates (dry runs) don't
// count, and another tile has its own budget.
func TestCheckpointRateLimit(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	src := tile(t, s, "apps/rate", map[string]string{"a.txt": "a\n"})
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := s.Estimate(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		capture(t, s, src, true)
	}
	runs := rec.count()
	_, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"})
	var rl *RateLimited
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) || rl.RetryAfter <= 0 || rl.RetryAfter > 3*time.Second {
		t.Fatalf("the 11th capture: %v", err)
	}
	if !strings.Contains(err.Error(), "retry in 3s") {
		t.Errorf("text: %v", err)
	}
	if rec.count() != runs {
		t.Error("a rate-limited capture ran something")
	}
	other := tile(t, s, "apps/other", map[string]string{"b.txt": "b\n"})
	capture(t, s, other, true)

	now = now.Add(3 * time.Second)
	capture(t, s, src, false)
	if _, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("one capture per 3 s: %v", err)
	}
	now = now.Add(time.Minute)
	for i := 0; i < 10; i++ {
		capture(t, s, src, false) // the burst refills, and no further
	}
	if _, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the burst refilled past 10: %v", err)
	}
}

// covers P16 T20, flow H — the git view is the checkpoint minus what the
// tile's own ignore rules exclude, evaluated in the capture's own run and
// kept in the store with it; its commit names the checkpoint and the
// work-tree HEAD, and carries no Xbin-By.
func TestGitViewExcludesIgnored(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/crm", map[string]string{
		".gitignore":          "node_modules/\n.env\n",
		"src/.gitignore":      "*.log\n",
		"src/app.js":          "app\n",
		"src/debug.log":       "noise\n",
		"node_modules/x/i.js": "dep\n",
		".env":                "SECRET=1\n",
		"README.md":           "hi\n",
	})
	head := repo(t, src.WorkTree)
	res := capture(t, s, src, true)

	full := treeOf(t, s, "apps/crm", res.Hash)
	for _, p := range []string{"node_modules/x/i.js", ".env", "src/debug.log", "src/app.js"} {
		if _, ok := full[p]; !ok {
			t.Errorf("the checkpoint lacks %s: gitignore doesn't apply to it", p)
		}
	}
	viewRef := "refs/xbin/views/" + res.Hash
	view := treeOf(t, s, "apps/crm", viewRef+"^{tree}")
	for _, p := range []string{"node_modules/x/i.js", ".env", "src/debug.log"} {
		if _, ok := view[p]; ok {
			t.Errorf("the git view holds the ignored %s", p)
		}
	}
	for _, p := range []string{".gitignore", "src/.gitignore", "src/app.js", "README.md"} {
		if full[p] != view[p] || view[p].mode == "" {
			t.Errorf("the git view's %s: %+v, the checkpoint's %+v", p, view[p], full[p])
		}
	}

	viewMsg := storeGit(t, s, "apps/crm", "log", "-1", "--format=%B", viewRef)
	for _, want := range []string{"checkpoint " + res.ID + " of apps/crm (git view: ignored files left out)",
		"Xbin-Tile: apps/crm", "Xbin-Checkpoint: " + res.Hash, "Xbin-Work-Tree-Head: " + head} {
		if !strings.Contains(viewMsg, want) {
			t.Errorf("the view commit lacks %q:\n%s", want, viewMsg)
		}
	}
	if strings.Contains(viewMsg, "Xbin-By") {
		t.Errorf("the view commit names who acted:\n%s", viewMsg)
	}
	if parents := strings.TrimSpace(storeGit(t, s, "apps/crm", "log", "-1", "--format=%P", viewRef)); parents != "" {
		t.Errorf("the view commit has parents %q", parents)
	}
	msg := storeGit(t, s, "apps/crm", "log", "-1", "--format=%B", "refs/xbin/checkpoints/"+res.Hash)
	for _, want := range []string{"checkpoint of apps/crm", "Xbin-Tile: apps/crm", "Xbin-Feed: work-tree", "Xbin-By: user:ana",
		"Xbin-At: " + res.At.Format(time.RFC3339), "Xbin-Work-Tree-Head: " + head} {
		if !strings.Contains(msg, want) {
			t.Errorf("the retention commit lacks %q:\n%s", want, msg)
		}
	}

	// the head is read beneath the tile: a loose ref, a packed one, a
	// detached HEAD; a gitfile (never followed out) and no repository omit it
	hostGit(t, src.WorkTree, "pack-refs", "--all")
	if got := workTreeHead(src.WorkTree); got != head {
		t.Errorf("packed ref: %q, want %s", got, head)
	}
	hostGit(t, src.WorkTree, "checkout", "-q", "--detach")
	if got := workTreeHead(src.WorkTree); got != head {
		t.Errorf("detached: %q, want %s", got, head)
	}
	gitfile := tile(t, s, "apps/gf", map[string]string{".git": "gitdir: " + filepath.Join(src.WorkTree, ".git") + "\n"})
	if got := workTreeHead(gitfile.WorkTree); got != "" {
		t.Errorf("a gitfile was followed: %q", got)
	}
	plain := tile(t, s, "apps/plain", map[string]string{"a": "a"})
	res = capture(t, s, plain, true)
	if res.WorkTreeHead != "" || strings.Contains(storeGit(t, s, "apps/plain", "log", "-1", "--format=%B", "refs/xbin/views/"+res.Hash), "Work-Tree-Head") {
		t.Errorf("a tile without a repository got a work-tree head: %+v", res)
	}
}

// covers P5 — the store is lazy: nothing but a committed opt-in (a Capture
// with Create) creates it; estimates, reads and a capture without Create
// leave no data/checkpoints behind.
func TestCheckpointStoreLazy(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/lazy", map[string]string{"a.txt": "a\n"})
	ctx := context.Background()
	if _, err := s.Estimate(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"}); !errors.Is(err, ErrNoStore) {
		t.Fatalf("a capture without Create on a tile with no store: %v", err)
	}
	if _, err := s.Resolve(ctx, "apps/lazy", "c:1234567"); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Fatalf("Resolve: %v", err)
	}
	if list, err := s.List(ctx, "apps/lazy"); err != nil || len(list) != 0 {
		t.Fatalf("List: %v %v", list, err)
	}
	if exists(filepath.Join(s.Root, "data")) || exists(filepath.Join(s.Root, ".xbin")) {
		t.Fatal("reads, an estimate or a refused capture created deployment state")
	}
	capture(t, s, src, true)
	if !s.Exists("apps/lazy") || s.Dir("apps/lazy") != filepath.Join(s.Root, "data", "checkpoints", util.TileKey("apps/lazy")+".git") {
		t.Fatal("the opt-in's capture made no store")
	}
}
