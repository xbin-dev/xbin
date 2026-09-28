package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// logEntry appends one deploy-log entry to refs/xbin/log/<name> of tile's
// store in 11-contract §10.3's shape, as the deploy log's writer does (host
// git: a test fixture, not xbind). It returns the new entry.
func logEntry(t *testing.T, s *Store, tilePath, name, tree, result string, at time.Time) string {
	t.Helper()
	store := s.Dir(tilePath)
	ref := "refs/xbin/log/" + name
	args := []string{"--git-dir=" + store, "commit-tree", tree}
	if parent, err := exec.Command("git", "--git-dir="+store, "rev-parse", "-q", "--verify", ref).Output(); err == nil {
		args = append(args, "-p", strings.TrimSpace(string(parent)))
	}
	msg := fmt.Sprintf("deploy c:%s → %s: %s\n\nXbin-Deployment: %s\nXbin-How: deploy\nXbin-Checkpoint: %s\nXbin-By: user:ana\nXbin-Result: %s\n",
		tree[:7], name, result, name, tree, result)
	cmd := exec.Command("git", append(args, "-F", "-")...)
	cmd.Stdin = strings.NewReader(msg)
	date := "@" + strconv.FormatInt(at.Unix(), 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=xbin", "GIT_AUTHOR_EMAIL=xbin@localhost", "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=xbin", "GIT_COMMITTER_EMAIL=xbin@localhost", "GIT_COMMITTER_DATE="+date)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("writing a log entry: %v", err)
	}
	c := strings.TrimSpace(string(out))
	storeGit(t, s, tilePath, "update-ref", ref, c)
	return c
}

// logChain is a deploy log, newest first: each entry's tree, subject,
// trailers and dates, and whether it has a parent.
func logChain(t *testing.T, s *Store, tilePath, name string) []string {
	t.Helper()
	out := storeGit(t, s, tilePath, "log", "--first-parent", "--format=%T %s|%(trailers:only,unfold,separator=;)|%ad %cd|%P%x00", "--date=raw", "refs/xbin/log/"+name)
	var chain []string
	for _, e := range strings.Split(out, "\x00") {
		if e = strings.TrimSpace(e); e != "" {
			chain = append(chain, e)
		}
	}
	return chain
}

// ageObjects makes every loose object of a store look two hours old, past
// prune's hour of grace.
func ageObjects(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	err := filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			err = os.Chtimes(p, old, old)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasObject(t *testing.T, s *Store, tilePath, id string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+s.Dir(tilePath), "cat-file", "-e", id)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return cmd.Run() == nil
}

func hasRef(t *testing.T, s *Store, tilePath, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+s.Dir(tilePath), "show-ref", "-q", "--verify", ref)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return cmd.Run() == nil
}

func looseCount(t *testing.T, s *Store, tilePath string) int {
	t.Helper()
	for _, line := range strings.Split(storeGit(t, s, tilePath, "count-objects", "-v"), "\n") {
		if n, ok := strings.CutPrefix(line, "count: "); ok {
			c, _ := strconv.Atoi(n)
			return c
		}
	}
	t.Fatal("count-objects printed no count")
	return 0
}

// covers T10 — GC (07-runtime §2.8) keeps what a deployment, the deploy-log
// retention or a materialization references, and drops the rest, in one
// confined run: keep's checkpoints (the record's pointers), the checkpoints
// of each deploy log's last 20 successful entries and any checkpoint younger
// than 24 hours keep their retention roots and git views; the rest lose
// both, and their objects are pruned. A deploy log past 50 entries is
// rewritten to its newest 50 (trees, messages and dates kept, the oldest
// parentless); a shorter one is untouched. Materialized trees stay for
// keep's checkpoints, each deployment's current and previous checkpoint and
// a tree handed out within the last half hour; the rest are removed, with
// leftover extractions; d/ is left alone. The store is repacked at most once
// a day. A tile without a store gets nothing created.
func TestCheckpointGC(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	s.Caps.Every = 0 // captures at made-up times
	base := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) func() time.Time { return func() time.Time { return base.Add(d) } }
	src := tile(t, s, "apps/gc", map[string]string{"v.txt": "v\n", "app.js": "shared\n"})
	v := make([]string, 30) // v[i]: the checkpoint of v.txt = "v<i>"
	capt := func(i int) {
		if err := os.WriteFile(filepath.Join(src.WorkTree, "v.txt"), []byte(fmt.Sprintf("v%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		v[i] = capture(t, s, src, i == 0).Hash
	}
	s.now = at(-72 * time.Hour)
	for i := 0; i < 30; i++ {
		if i != 28 {
			capt(i)
		}
	}
	s.now = at(-time.Hour)
	capt(28) // young, and last: the persistent index names its blob, not v29's
	blob := func(i int) string {
		return strings.TrimSpace(storeGit(t, s, src.Tile, "rev-parse", v[i]+":v.txt"))
	}
	blob29 := blob(29)

	// main: ok entries for v0..v24, then a failed one for v25
	for i := 0; i <= 24; i++ {
		logEntry(t, s, src.Tile, "main", v[i], "ok", base.Add(-48*time.Hour+time.Duration(i)*time.Minute))
	}
	logEntry(t, s, src.Tile, "main", v[25], "failed", base.Add(-47*time.Hour))
	mainHead := strings.TrimSpace(storeGit(t, s, src.Tile, "rev-parse", "refs/xbin/log/main"))
	// dev: 55 ok entries alternating v23 and v24, newest v23
	for i := 0; i < 55; i++ {
		tr := v[24]
		if i%2 == 0 {
			tr = v[23]
		}
		logEntry(t, s, src.Tile, "dev", tr, "ok", base.Add(-40*time.Hour+time.Duration(i)*time.Minute))
	}
	devBefore := logChain(t, s, src.Tile, "dev")

	// materialize some, long enough ago that no grace applies
	s.now = at(-2 * time.Hour)
	roots := map[int]string{}
	for _, i := range []int{0, 5, 23, 24, 25, 26, 27, 28, 29} {
		r, err := s.Materialize(src.Tile, v[i])
		if err != nil {
			t.Fatalf("materializing v%d: %v", i, err)
		}
		roots[i] = r
	}
	trees := s.TreesDir(src.Tile)
	stale := filepath.Join(trees, tmpPrefix+"leftover")
	perDep := filepath.Join(trees, "d", "dev", "backend.log")
	for _, d := range []string{stale, filepath.Dir(perDep)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(perDep, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ageObjects(t, s.Dir(src.Tile))

	running := roots[27] // TestGCKeepsTreesOfRunningGenerations below
	keep := func() []string {
		return []string{v[26], running, filepath.Join(t.TempDir(), v[29]), "not an id"}
	}
	s.now = at(0)
	before := rec.count()
	if err := s.GC(context.Background(), src.Tile, keep); err != nil {
		t.Fatal(err)
	}
	if n := rec.count() - before; n != 1 {
		t.Errorf("GC made %d confined runs, want 1", n)
	}

	// the store's retention
	for i := 0; i < 30; i++ {
		want := i >= 5 && i <= 24 || i == 26 || i == 27 || i == 28
		cp := hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+v[i])
		view := hasRef(t, s, src.Tile, "refs/xbin/views/"+v[i])
		if cp != want || view != want {
			t.Errorf("v%d: checkpoint %v, view %v; want %v", i, cp, view, want)
		}
	}
	if hasObject(t, s, src.Tile, blob29) {
		t.Error("v29's content wasn't pruned")
	}
	if !hasObject(t, s, src.Tile, blob(24)) || !hasObject(t, s, src.Tile, blob(28)) {
		t.Error("a kept checkpoint lost its content")
	}
	if _, err := s.Resolve(context.Background(), src.Tile, "c:"+v[0][:12]); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("a collected checkpoint still resolves: %v", err)
	}
	if _, err := s.Resolve(context.Background(), src.Tile, "c:"+v[5][:12]); err != nil {
		t.Errorf("a roll-back target no longer resolves: %v", err)
	}

	// the deploy logs
	if h := strings.TrimSpace(storeGit(t, s, src.Tile, "rev-parse", "refs/xbin/log/main")); h != mainHead {
		t.Errorf("main's log of 26 entries was rewritten: %s, was %s", h, mainHead)
	}
	devAfter := logChain(t, s, src.Tile, "dev")
	if len(devAfter) != logEntriesKept {
		t.Fatalf("dev's log holds %d entries after GC, want %d", len(devAfter), logEntriesKept)
	}
	strip := func(e string) string { i := strings.LastIndex(e, "|"); return e[:i] } // parents change
	for i := range devAfter {
		if strip(devAfter[i]) != strip(devBefore[i]) {
			t.Errorf("dev entry %d: %q, was %q", i, strip(devAfter[i]), strip(devBefore[i]))
		}
	}
	if last := devAfter[len(devAfter)-1]; !strings.HasSuffix(last, "|") {
		t.Errorf("the oldest kept entry has a parent: %q", last)
	}

	// materialized trees: keep's (v26, v27), the current and previous of
	// main (v24, v23) and dev (v23, v24)
	for i, r := range roots {
		want := i == 23 || i == 24 || i == 26 || i == 27
		if exists(r) != want {
			t.Errorf("v%d's materialized tree present %v, want %v", i, exists(r), want)
		}
	}
	if exists(stale) {
		t.Error("a leftover extraction survived GC")
	}
	if !exists(perDep) {
		t.Error("GC removed a deployment's d/ state")
	}

	t.Run("TestGCKeepsTreesOfRunningGenerations", func(t *testing.T) {
		// covers T10 T18 — GC never evicts a tree a running generation
		// binds (keep, the runner's RootsInUse): v27 is in no log, older than
		// a day and not handed out recently, yet its tree and checkpoint stay
		// while keep names its root; a root of another tile, or a bare path,
		// protects nothing; once no generation binds it, it goes
		for _, d := range []time.Duration{time.Hour, 25 * time.Hour} {
			s.now = at(d)
			if err := s.GC(context.Background(), src.Tile, keep); err != nil {
				t.Fatal(err)
			}
			if !exists(running) || !hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+v[27]) {
				t.Fatalf("GC at +%s removed the tree of a running generation", d)
			}
		}
		if hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+v[29]) {
			t.Error("a root of another tile kept v29")
		}
		if err := s.GC(context.Background(), src.Tile, func() []string { return nil }); err != nil {
			t.Fatal(err)
		}
		if exists(running) || hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+v[27]) {
			t.Error("the tree outlived its generation")
		}
	})

	t.Run("handed-out", func(t *testing.T) {
		// a tree Materialize returned within the half hour stays, and its
		// checkpoint with it
		s.now = at(30 * time.Hour)
		r, err := s.Materialize(src.Tile, v[5])
		if err != nil {
			t.Fatal(err)
		}
		s.now = at(30*time.Hour + 10*time.Minute)
		if err := s.GC(context.Background(), src.Tile, nil); err != nil {
			t.Fatal(err)
		}
		if !exists(r) {
			t.Fatal("GC removed a tree handed out ten minutes ago")
		}
		s.now = at(30*time.Hour + 31*time.Minute)
		if err := s.GC(context.Background(), src.Tile, nil); err != nil {
			t.Fatal(err)
		}
		if exists(r) {
			t.Error("the grace outlived its half hour")
		}
	})

	t.Run("repack", func(t *testing.T) {
		// the first GC repacked; within a day the next doesn't
		dir := s.Dir(src.Tile)
		packs, _ := filepath.Glob(filepath.Join(dir, "objects", "pack", "*.pack"))
		if len(packs) == 0 {
			t.Fatal("the first GC didn't repack")
		}
		last, _ := repacked.Load(dir)
		s.now = func() time.Time { return last.(time.Time).Add(time.Hour) }
		if err := os.WriteFile(filepath.Join(src.WorkTree, "new.txt"), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fresh := capture(t, s, src, false).Hash
		loose := looseCount(t, s, src.Tile)
		if loose == 0 {
			t.Fatal("the capture left no loose objects")
		}
		if err := s.GC(context.Background(), src.Tile, nil); err != nil {
			t.Fatal(err)
		}
		if n := looseCount(t, s, src.Tile); n != loose {
			t.Errorf("a GC within the day repacked: %d loose objects, was %d", n, loose)
		}
		s.now = func() time.Time { return last.(time.Time).Add(25 * time.Hour) }
		if err := s.GC(context.Background(), src.Tile, func() []string { return []string{fresh} }); err != nil {
			t.Fatal(err)
		}
		if n := looseCount(t, s, src.Tile); n != 0 {
			t.Errorf("a GC a day later left %d loose objects", n)
		}
		if !hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+fresh) {
			t.Error("keep's checkpoint went")
		}
	})

	t.Run("no store", func(t *testing.T) {
		none := tile(t, s, "apps/none", map[string]string{"a": "a\n"})
		called := false
		if err := s.GC(context.Background(), none.Tile, func() []string { called = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Error("keep wasn't asked")
		}
		if s.Exists(none.Tile) || exists(s.TreesDir(none.Tile)) {
			t.Error("GC created a store or a trees directory")
		}
	})
}
