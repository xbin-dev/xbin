package checkpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// purgeRuns counts the recorded runs of the purge script.
func purgeRuns(r *recorder) int {
	n := 0
	for _, b := range r.scripts() {
		if b == purgeScript {
			n++
		}
	}
	return n
}

// blobOf is the blob id of path in tree, read from tile's store.
func blobOf(t *testing.T, s *Store, tilePath, tree, path string) string {
	t.Helper()
	return strings.TrimSpace(storeGit(t, s, tilePath, "rev-parse", tree+":"+path))
}

// gitStdout runs git on the store at dir and returns its stdout alone (fsck
// says what it notices, such as the dangling HEAD, on stderr).
func gitStdout(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// allObjects lists every object the refs of tile's store reach.
func allObjects(t *testing.T, s *Store, tilePath string) string {
	t.Helper()
	return storeGit(t, s, tilePath, "rev-list", "--all", "--objects")
}

// covers T20 NP-06-16 P1 — a purge (06-security L7) removes a checkpoint the
// retention still keeps, in one confined run under the store lock: every
// deploy-log entry that deployed it is rewritten to name none (the empty
// tree, no checkpoint or feed trailer; the attempt, who, when, its result,
// its error and its subject stay, and a later entry's previous still names
// it), a log that never named it keeps its head, its retention root and git
// view go, and its objects — packed or loose — are pruned at once while the
// objects other checkpoints share stay; the store's index goes (git keeps
// what an index names), so no object survives unreachable; its materialized
// tree goes and the others stay; it no longer resolves, and the next capture
// works. Another tile's store holding the same content is untouched. A
// second purge, a tile without a store and a bad id are refused, creating
// nothing.
func TestPurgeRemovesObjects(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	s.Caps.Every = 0
	ctx := context.Background()
	const tp = "apps/purge"
	src := tile(t, s, tp, map[string]string{"app.js": "shared\n", "secret.env": "TOKEN=hunter2\n"})
	t1 := capture(t, s, src, true).Hash
	secret := blobOf(t, s, tp, t1, "secret.env")
	if err := os.Remove(filepath.Join(src.WorkTree, "secret.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, "v.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t2 := capture(t, s, src, false).Hash
	shared := blobOf(t, s, tp, t2, "app.js")
	// back to t1's content: the store's index names the secret's blob again
	if err := os.WriteFile(filepath.Join(src.WorkTree, "secret.env"), []byte("TOKEN=hunter2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src.WorkTree, "v.txt")); err != nil {
		t.Fatal(err)
	}
	if again := capture(t, s, src, false); again.Hash != t1 || again.New {
		t.Fatalf("recapturing t1's content gave %s (new %v)", again.Hash, again.New)
	}

	other := tile(t, s, "apps/other", map[string]string{"app.js": "shared\n", "secret.env": "TOKEN=hunter2\n"})
	if o1 := capture(t, s, other, true).Hash; o1 != t1 {
		t.Fatalf("the same content made %s in another tile, not %s", o1, t1)
	}

	entries := []LogEntry{
		{ID: 1, Deployment: "main", How: "pause", Checkpoint: t1, Feed: FeedWorkTree, By: "user:ana", Via: "session", Result: LogOK},
		{ID: 2, Deployment: "dev", How: "add", Checkpoint: t1, Feed: FeedWorkTree, By: "user:ben", Via: "terminal", Session: "s-1", Result: LogFailed, Error: "boom"},
		{ID: 3, Deployment: "main", How: "deploy", Checkpoint: t2, Previous: t1, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
		{ID: 4, Deployment: "dev", How: "deploy", Checkpoint: t2, Previous: t1, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
		{ID: 5, Deployment: "stage", How: "add", Checkpoint: t2, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
	}
	for i := range entries {
		entries[i].RequestedAt = logAt.Add(time.Duration(i) * time.Minute)
		entries[i].FinishedAt = logAt.Add(time.Duration(i)*time.Minute + 30*time.Second)
		if err := s.AppendLog(ctx, tp, entries[i]); err != nil {
			t.Fatalf("append %d: %v", entries[i].ID, err)
		}
	}
	stageHead := storeGit(t, s, tp, "rev-parse", "refs/xbin/log/stage")
	subjects := storeGit(t, s, tp, "log", "--format=%s", "refs/xbin/log/main")
	root1, err := s.Materialize(tp, t1)
	if err != nil {
		t.Fatal(err)
	}
	root2, err := s.Materialize(tp, t2)
	if err != nil {
		t.Fatal(err)
	}
	storeGit(t, s, tp, "repack", "-a", "-d", "-q") // t1's objects packed: repack -a -d must drop them
	if !hasObject(t, s, tp, secret) || !exists(filepath.Join(s.Dir(tp), "index")) {
		t.Fatal("the fixture's secret or index is missing before the purge")
	}
	if _, err := s.List(ctx, tp); err != nil { // warm: the purge's own run is the only one
		t.Fatal(err)
	}

	before := rec.count()
	res, err := s.Purge(ctx, tp, t1, func() []string { return []string{t2, root2} })
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n := rec.count() - before; n != 1 || purgeRuns(rec) != 1 {
		t.Errorf("the purge made %d confined runs (%d purge scripts), want 1", n, purgeRuns(rec))
	}
	if res.Checkpoint.Hash != t1 || res.Checkpoint.By != "user:ana" || res.Entries != 2 || !slices.Equal(res.Logs, []string{"dev", "main"}) {
		t.Errorf("Purge answered %+v", res)
	}

	// refs and objects
	for ref, want := range map[string]bool{
		"refs/xbin/checkpoints/" + t1: false, "refs/xbin/views/" + t1: false,
		"refs/xbin/checkpoints/" + t2: true, "refs/xbin/views/" + t2: true,
	} {
		if hasRef(t, s, tp, ref) != want {
			t.Errorf("%s present %v, want %v", ref, !want, want)
		}
	}
	if hasObject(t, s, tp, secret) || hasObject(t, s, tp, t1) {
		t.Error("the purged checkpoint's content is still in the store")
	}
	if !hasObject(t, s, tp, shared) || !hasObject(t, s, tp, t2) {
		t.Error("a kept checkpoint lost its content")
	}
	if strings.Contains(allObjects(t, s, tp), secret) {
		t.Error("a ref still reaches the purged content")
	}
	if n := looseCount(t, s, tp); n != 0 {
		t.Errorf("%d loose objects left after the purge", n)
	}
	if out := gitStdout(t, s.Dir(tp), "fsck", "--unreachable", "--no-reflogs", "--no-progress"); strings.TrimSpace(out) != "" {
		t.Errorf("objects nothing reaches survived the purge:\n%s", out)
	}
	if exists(filepath.Join(s.Dir(tp), "index")) {
		t.Error("the store's index, which may name the purged blobs, survived")
	}
	if _, err := s.Resolve(ctx, tp, "c:"+t1[:12]); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("the purged checkpoint still resolves: %v", err)
	}
	if _, err := s.Resolve(ctx, tp, "c:"+t2[:12]); err != nil {
		t.Errorf("a kept checkpoint no longer resolves: %v", err)
	}
	if _, err := s.Resolve(ctx, "apps/other", "c:"+t1[:12]); err != nil {
		t.Errorf("another tile's checkpoint of the same content went: %v", err)
	}

	// the deploy logs
	got, _, err := s.Log(ctx, tp, LogQuery{})
	if err != nil || len(got) != len(entries) {
		t.Fatalf("Log after the purge: %d entries, %v", len(got), err)
	}
	for i, e := range got {
		want := entries[len(entries)-1-i]
		if want.Checkpoint == t1 {
			want.Checkpoint, want.Feed = "", ""
		}
		if e != want {
			t.Errorf("entry %d reads back as\n%+v\nwant\n%+v", want.ID, e, want)
		}
	}
	if tr := strings.TrimSpace(storeGit(t, s, tp, "rev-parse", "refs/xbin/log/main~1^{tree}")); tr != emptyTree {
		t.Errorf("the rewritten entry's tree is %s, want the empty tree", tr)
	}
	if after := storeGit(t, s, tp, "log", "--format=%s", "refs/xbin/log/main"); after != subjects {
		t.Errorf("the entries' subjects changed:\n%s\nwas\n%s", after, subjects)
	}
	if storeGit(t, s, tp, "rev-parse", "refs/xbin/log/stage") != stageHead {
		t.Error("a deploy log that never named the checkpoint was rewritten")
	}
	if strings.Contains(storeGit(t, s, tp, "log", "--format=%B", "--glob=refs/xbin/log/*"), "Xbin-Checkpoint: "+t1) {
		t.Error("an entry still names the purged checkpoint as its own")
	}

	// materialized trees
	if exists(root1) {
		t.Error("the purged checkpoint's materialized tree survived")
	}
	if !present(root2) {
		t.Error("a kept checkpoint's materialized tree went")
	}

	// the store keeps working
	if err := os.WriteFile(filepath.Join(src.WorkTree, "v.txt"), []byte("v3 loose-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t3 := capture(t, s, src, false).Hash
	loose := blobOf(t, s, tp, t3, "v.txt")
	if looseCount(t, s, tp) == 0 {
		t.Fatal("the capture after the purge left no loose objects")
	}
	if _, err := s.Purge(ctx, tp, t3, nil); err != nil {
		t.Fatalf("purging a loose checkpoint: %v", err)
	}
	if hasObject(t, s, tp, loose) || looseCount(t, s, tp) != 0 {
		t.Error("a loose checkpoint's content survived its purge")
	}

	// refusals, which create nothing
	if _, err := s.Purge(ctx, tp, t1, nil); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("a second purge: %v, want ErrUnknownCheckpoint", err)
	}
	none := tile(t, s, "apps/none", map[string]string{"a": "a\n"})
	if _, err := s.Purge(ctx, none.Tile, t2, nil); !errors.Is(err, ErrNoStore) {
		t.Errorf("a tile without a store: %v, want ErrNoStore", err)
	}
	if s.Exists(none.Tile) || exists(s.TreesDir(none.Tile)) {
		t.Error("a purge created a store or a trees directory")
	}
	if _, err := s.Purge(ctx, tp, t2[:12], nil); !errors.Is(err, ErrBadID) {
		t.Errorf("a short id: %v, want ErrBadID", err)
	}
	if _, err := s.Purge(ctx, "../x", t2, nil); err == nil {
		t.Error("a bad tile path was accepted")
	}
}

// covers T20 T18 P9 — the store's own refusal: a purge of a checkpoint its
// keep names, by full tree id or by the root a running generation binds,
// changes nothing and runs nothing (ErrInUse); a root of another tile with
// the same tree protects nothing, as with GC.
func TestPurgeRefusesKept(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/kept", map[string]string{"a.txt": "a\n"})
	tr := capture(t, s, src, true).Hash
	root, err := s.Materialize(src.Tile, tr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(ctx, src.Tile, LogEntry{ID: 1, Deployment: "main", How: "pause", Checkpoint: tr,
		Feed: FeedWorkTree, By: "user:ana", Result: LogOK, FinishedAt: logAt}); err != nil {
		t.Fatal(err)
	}
	head := storeGit(t, s, src.Tile, "rev-parse", "refs/xbin/log/main")
	before := rec.count()
	for name, k := range map[string][]string{"its id": {tr}, "its root": {"/elsewhere", root + "/"}} {
		if _, err := s.Purge(ctx, src.Tile, tr, func() []string { return k }); !errors.Is(err, ErrInUse) {
			t.Errorf("keep naming %s: %v, want ErrInUse", name, err)
		}
	}
	if n := rec.count() - before; n != 0 {
		t.Errorf("a refused purge made %d confined runs", n)
	}
	if !hasRef(t, s, src.Tile, "refs/xbin/checkpoints/"+tr) || !present(root) ||
		storeGit(t, s, src.Tile, "rev-parse", "refs/xbin/log/main") != head {
		t.Error("a refused purge changed the store")
	}
	elsewhere := filepath.Join(s.TreesDir("apps/other"), tr)
	if _, err := s.Purge(ctx, src.Tile, tr, func() []string { return []string{elsewhere, "not an id"} }); err != nil {
		t.Errorf("a root of another tile refused the purge: %v", err)
	}
}
