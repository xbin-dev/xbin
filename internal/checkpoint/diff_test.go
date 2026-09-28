package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// covers T20 T1 — the diff runs --no-ext-diff --no-textconv: with the tile's
// .gitattributes naming a driver, the tile's own config, a global config and
// even the store's config and attributes (tampered with) naming an external
// diff and a textconv, no command runs and the patch is git's own. A patch
// detects renames and names binary files without inlining them; the summary
// gives statuses and counts; a path narrows either, taken literally (a file
// named "--output=x" is a path, not an option); a work-tree side is captured
// and named; the 16 MiB and 5 000-file bounds cut and say so. A tile without
// a store has nothing to diff (the 409), and no tool runs, no store appears.
func TestCheckpointDiffNoExternalTools(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "PWNED")
	evil := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\necho EVIL\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte("[diff]\n\texternal = "+evil+"\n"), 0o644)
	src := tile(t, s, "apps/crm", map[string]string{
		"a.txt":          "one\ntwo\n",
		"gone.txt":       "bye\n",
		"big.txt":        strings.Repeat("line\n", 40),
		"bin.dat":        "x\x00y",
		"--output=x":     "flag-like\n",
		".gitattributes": "* diff=evil\n",
	})
	repo(t, src.WorkTree)
	for _, kv := range [][2]string{{"diff.external", evil}, {"diff.evil.textconv", evil}, {"diff.evil.command", evil}} {
		hostGit(t, src.WorkTree, "config", kv[0], kv[1])
	}
	c1 := capture(t, s, src, true)
	for rel, body := range map[string]string{"a.txt": "one\n2\n", "bin.dat": "z\x00q", "new.txt": "new\n", "--output=x": "still a path\n"} {
		if err := os.WriteFile(filepath.Join(src.WorkTree, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(src.WorkTree, "gone.txt"))
	if err := os.Rename(filepath.Join(src.WorkTree, "big.txt"), filepath.Join(src.WorkTree, "moved.txt")); err != nil {
		t.Fatal(err)
	}
	c2 := capture(t, s, src, false)
	// the store itself names the drivers now (nothing but xbind writes it;
	// the flags must hold even so)
	store := s.Dir("apps/crm")
	cfg, _ := os.ReadFile(filepath.Join(store, "config"))
	_ = os.WriteFile(filepath.Join(store, "config"), append(cfg, []byte("[diff]\n\texternal = "+evil+"\n[diff \"evil\"]\n\ttextconv = "+evil+"\n\tcommand = "+evil+"\n")...), 0o644)
	_ = os.WriteFile(filepath.Join(store, "info", "attributes"), []byte("* diff=evil\n"), 0o644)
	gitlog := gitWrapper(t)

	res, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, By: "user:ana"})
	if err != nil {
		t.Fatal(err)
	}
	patch := string(res.Patch)
	for _, want := range []string{"diff --git a/a.txt b/a.txt", "-two\n+2\n", "Binary files a/bin.dat and b/bin.dat differ",
		"rename from big.txt", "rename to moved.txt", "deleted file mode 100644", "+new\n", "+++ b/--output=x"} {
		if !strings.Contains(patch, want) {
			t.Errorf("the patch lacks %q:\n%s", want, patch)
		}
	}
	if strings.Contains(patch, "EVIL") || strings.Contains(patch, "x\x00y") || res.Truncated {
		t.Errorf("the patch ran a driver, inlined a binary, or was cut:\n%s", patch)
	}
	if res.From.Hash != c1.Hash || res.To.Hash != c2.Hash || res.From.ID != c1.ID {
		t.Errorf("the sides: %+v → %+v", res.From, res.To)
	}

	stat, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, Stat: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiffFile{}
	for _, f := range stat.Files {
		got[f.Path] = f
	}
	for path, want := range map[string]DiffFile{
		"a.txt":      {Path: "a.txt", Status: "M", Added: 1, Removed: 1},
		"bin.dat":    {Path: "bin.dat", Status: "M", Binary: true},
		"moved.txt":  {Path: "moved.txt", Status: "R"},
		"gone.txt":   {Path: "gone.txt", Status: "D", Removed: 1},
		"new.txt":    {Path: "new.txt", Status: "A", Added: 1},
		"--output=x": {Path: "--output=x", Status: "M", Added: 1, Removed: 1},
	} {
		if got[path] != want {
			t.Errorf("stat %s: %+v, want %+v", path, got[path], want)
		}
	}
	if len(stat.Files) != 6 || stat.Truncated || stat.Patch != nil {
		t.Errorf("stat: %d files, truncated %v: %+v", len(stat.Files), stat.Truncated, stat.Files)
	}

	// a path, literally
	for _, p := range []string{"--output=x", "a.txt"} {
		one, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, Path: p, Stat: true})
		if err != nil || len(one.Files) != 1 || one.Files[0].Path != p {
			t.Errorf("narrowed to %q: %+v (%v)", p, one.Files, err)
		}
	}
	if exists(filepath.Join(store, "x")) || exists("x") {
		t.Error("a path became an option")
	}
	for _, p := range []string{"../a.txt", "/etc/passwd", "a/../b", "a\nb", ".", "a//b"} {
		if _, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, Path: p}); !errors.Is(err, ErrBadDiffPath) {
			t.Errorf("path %q: %v, want ErrBadDiffPath", p, err)
		}
	}

	// a work-tree side is captured
	if err := os.WriteFile(filepath.Join(src.WorkTree, "wt.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c2.Hash}, To: DiffSide{WorkTree: true}, By: "user:ana", Stat: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(wt.Files) != 1 || wt.Files[0].Path != "wt.txt" || wt.To.Hash == c2.Hash || wt.To.ID == "" || wt.From.Hash != c2.Hash {
		t.Errorf("the work-tree diff: %+v %+v → %+v", wt.Files, wt.From, wt.To)
	}
	if _, err := s.Get(ctx, "apps/crm", wt.To.Hash); err != nil {
		t.Errorf("the work-tree side isn't a checkpoint of the store: %v", err)
	}

	// the bounds
	defer func(p, f int) { diffMaxPatch, diffMaxFiles = p, f }(diffMaxPatch, diffMaxFiles)
	diffMaxPatch, diffMaxFiles = 100, 2
	cut, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}})
	if err != nil || len(cut.Patch) != 100 || !cut.Truncated {
		t.Errorf("a patch past the bound: %d bytes, truncated %v (%v)", len(cut.Patch), cut.Truncated, err)
	}
	cutStat, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, Stat: true})
	if err != nil || len(cutStat.Files) != 2 || !cutStat.Truncated {
		t.Errorf("a summary past the bound: %d files, truncated %v (%v)", len(cutStat.Files), cutStat.Truncated, err)
	}

	// every diff-tree ran hardened, with the flags
	var diffs int
	for _, c := range gitCalls(t, gitlog) {
		all := strings.Join(c.args, " ")
		if !strings.Contains(all, " diff-tree ") {
			continue
		}
		diffs++
		if !strings.Contains(all, " --no-ext-diff ") || !strings.Contains(all, " --no-textconv ") || !strings.Contains(all, "--git-dir="+store) {
			t.Errorf("a diff ran without --no-ext-diff --no-textconv on the store: %q", c.args)
		}
	}
	if diffs < 6 {
		t.Errorf("only %d diff-tree runs logged", diffs)
	}
	if exists(marker) {
		t.Fatal("a diff driver ran")
	}

	// an unknown checkpoint; a bad side
	if _, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: strings.Repeat("ab", 20)}, To: DiffSide{Tree: c2.Hash}}); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("an unknown checkpoint: %v", err)
	}
	for _, side := range []DiffSide{{}, {Tree: "c:" + c1.ID}, {Tree: c1.Hash, WorkTree: true}} {
		if _, err := s.Diff(ctx, DiffRequest{Source: src, From: side, To: DiffSide{Tree: c2.Hash}}); err == nil {
			t.Errorf("side %+v passed", side)
		}
	}

	// no store: nothing to diff, nothing run, nothing made
	other := tile(t, s, "apps/zero", map[string]string{"a.txt": "a\n"})
	n := rec.count()
	for _, req := range []DiffRequest{
		{Source: other, From: DiffSide{Tree: c1.Hash}, To: DiffSide{WorkTree: true}, By: "user:ana"},
		{Source: other, From: DiffSide{WorkTree: true}, To: DiffSide{WorkTree: true}, By: "user:ana", Stat: true},
	} {
		_, err := s.Diff(ctx, req)
		if !errors.Is(err, ErrNothingToDiff) || !strings.Contains(err.Error(), "apps/zero has no deployments: its work tree is what runs, so there is nothing to diff") {
			t.Errorf("a diff of a tile without a store: %v", err)
		}
	}
	if rec.count() != n || s.Exists("apps/zero") {
		t.Errorf("a diff of a tile without a store ran %d tools, store %v", rec.count()-n, s.Exists("apps/zero"))
	}
}

// covers T10 — the diff queue: one diff runs per tile and one waits; a third
// request for that tile answers 429 at once with the contract's text, even
// while another tile has a free slot; two diffs run across xbind, so a
// tile's running diff and its waiting one hold one slot between them and
// another tile's diff runs beside them, while a third tile's waits (no 429)
// until a slot frees; a waiter whose deadline passes gets the 504 and gives
// its place back; nothing is left in the queue afterwards.
func TestDiffQueueBounded(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	trees := map[string]string{}
	srcs := map[string]Source{}
	for _, name := range []string{"apps/a", "apps/b", "apps/c"} {
		src := tile(t, s, name, map[string]string{"x.txt": name + "\n"})
		srcs[name] = src
		trees[name] = capture(t, s, src, true).Hash
	}

	var mu sync.Mutex
	gates := map[string]chan struct{}{}
	for name := range srcs {
		gates[s.Dir(name)] = make(chan struct{})
	}
	entered := make(chan string, 16)
	s.run = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
		if len(c.Argv) > 2 && strings.Contains(c.Argv[2], "diff-tree") {
			entered <- c.Dir
			mu.Lock()
			g := gates[c.Dir]
			mu.Unlock()
			select {
			case <-g:
			case <-ctx.Done():
				return confine.Result{}, ctx.Err()
			}
		}
		return confine.Run(ctx, c)
	}
	diff := func(ctx context.Context, name string) error {
		_, err := s.Diff(ctx, DiffRequest{Source: srcs[name], From: DiffSide{Tree: trees[name]}, To: DiffSide{Tree: trees[name]}, Stat: true})
		return err
	}
	type result struct {
		name string
		err  error
	}
	results := make(chan result, 16)
	start := func(name string) {
		go func() { results <- result{name, diff(ctx, name)} }()
	}
	expectEnter := func(name string) {
		t.Helper()
		select {
		case dir := <-entered:
			if dir != s.Dir(name) {
				t.Fatalf("%s's diff ran, want %s's", dir, name)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s's diff never ran", name)
		}
	}
	quiet := func(what string) {
		t.Helper()
		select {
		case dir := <-entered:
			t.Fatalf("%s: a diff ran (%s)", what, dir)
		case r := <-results:
			t.Fatalf("%s: %s's diff returned %v", what, r.name, r.err)
		case <-time.After(300 * time.Millisecond):
		}
	}

	start("apps/a")
	expectEnter("apps/a")
	start("apps/a") // waits for a's turn
	quiet("a's second diff")
	began := time.Now()
	if err := diff(ctx, "apps/a"); !errors.Is(err, ErrDiffBusy) || err.Error() != "apps/a already has a diff running and one waiting; retry shortly" {
		t.Fatalf("a's third diff: %v", err)
	}
	if time.Since(began) > 2*time.Second {
		t.Error("the 429 wasn't at once")
	}
	start("apps/b") // a holds one slot across xbind, not two
	expectEnter("apps/b")
	start("apps/c") // both slots taken: it waits, it isn't refused
	quiet("c's diff")

	// a waiter whose deadline passes: the 504, and its place comes back
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := diff(short, "apps/b"); !errors.Is(err, ErrDiffTimeout) {
		t.Fatalf("b's waiting diff past its deadline: %v", err)
	}

	// a's and c's diffs pass once they run; b's still holds its slot, so
	// a's second and c's share the other one, in either order
	mu.Lock()
	close(gates[s.Dir("apps/a")])
	close(gates[s.Dir("apps/c")])
	mu.Unlock()
	next := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case dir := <-entered:
			next[dir] = true
		case <-time.After(20 * time.Second):
			t.Fatal("the waiting diffs never ran")
		}
	}
	if !next[s.Dir("apps/a")] || !next[s.Dir("apps/c")] {
		t.Fatalf("after a's first diff: %v", next)
	}
	mu.Lock()
	close(gates[s.Dir("apps/b")])
	mu.Unlock()
	for i := 0; i < 4; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("%s's diff: %v", r.name, r.err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a diff never finished")
		}
	}
	diffs.mu.Lock()
	left := len(diffs.tiles)
	diffs.mu.Unlock()
	if left != 0 || len(diffs.run) != 0 {
		t.Errorf("the queue kept %d tiles and %d slots", left, len(diffs.run))
	}
}
