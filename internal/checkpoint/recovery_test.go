package checkpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// covers T2 T11 — a killed run's leftovers never reach a checkpoint: a stale
// quarantine is removed with the index that may name its objects, stale
// lock files go inside the next run, and an index git finds corrupt is
// dropped and the capture retried once (a full re-hash).
func TestCaptureRecoversFromKilledRun(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/crash", map[string]string{"a.txt": "a\n", "b/c.txt": "c\n"})
	first := capture(t, s, src, true)
	dir := s.Dir("apps/crash")

	// a run killed after it hashed a new file: its blob is only in the
	// quarantine, and the index names it
	if err := os.WriteFile(filepath.Join(src.WorkTree, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(dir, "quarantine", "killed")
	if err := os.MkdirAll(filepath.Join(q, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "--git-dir="+dir, "--work-tree="+src.WorkTree, "add", "-A")
	add.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_OBJECT_DIRECTORY="+filepath.Join(q, "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(dir, "objects"))
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	for _, lock := range []string{"index.lock", "HEAD.lock", "packed-refs.lock"} {
		if err := os.WriteFile(filepath.Join(dir, lock), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := capture(t, s, src, false)
	if !res.New || res.Hash == first.Hash || treeOf(t, s, "apps/crash", res.Hash)["new.txt"].body != "new\n" {
		t.Fatalf("after a killed run: %+v", res)
	}
	storeGit(t, s, "apps/crash", "fsck", "--strict", "--no-dangling") // every object the refs reach is in the store
	for _, left := range []string{"quarantine", "index.lock", "HEAD.lock", "packed-refs.lock"} {
		if exists(filepath.Join(dir, left)) {
			t.Errorf("%s survived", left)
		}
	}

	// a corrupt index
	if err := os.WriteFile(filepath.Join(dir, "index"), []byte("DIRC garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again := capture(t, s, src, false); again.Hash != res.Hash || again.New {
		t.Fatalf("after a corrupt index: %+v, want %s again", again, res.Hash)
	}
}

// covers T10 — one store operation per tile at a time: concurrent captures
// of one tile queue on its lock and all succeed; another tile's capture
// doesn't wait for them, and a caller that gives up waiting gets its
// context's error.
func TestCaptureSerializedPerTile(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	s.Caps.Burst = 100 // the rate is TestCheckpointRateLimit's
	src := tile(t, s, "apps/busy", map[string]string{"a.txt": "a\n"})
	capture(t, s, src, true)
	var wg sync.WaitGroup
	hashes := make([]string, 8)
	errs := make([]error, 8)
	for i := range hashes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.Capture(context.Background(), CaptureRequest{Source: src, By: "user:ana"})
			hashes[i], errs[i] = res.Hash, err
		}(i)
	}
	wg.Wait()
	for i := range hashes {
		if errs[i] != nil || hashes[i] != hashes[0] {
			t.Errorf("capture %d: %s, %v", i, hashes[i], errs[i])
		}
	}

	release, err := s.acquire(context.Background(), "apps/busy")
	if err != nil {
		t.Fatal(err)
	}
	other := tile(t, s, "apps/free", map[string]string{"b.txt": "b\n"})
	capture(t, s, other, true) // not behind apps/busy's lock
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"}); err == nil || ctx.Err() == nil {
		t.Errorf("a capture behind a held lock: %v", err)
	}
	release()
	capture(t, s, src, false)
}

// covers P9 — a file or directory the capture can't read fails the
// checkpoint, naming it: a pinned deployment silently missing files would be
// a lie. The store is unchanged and the index dropped.
func TestCaptureRefusesUnreadable(t *testing.T) {
	needGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	s, _ := testStore(t)
	src := tile(t, s, "apps/locked", map[string]string{"a.txt": "a\n", "secret.txt": "s\n", "dir/x.txt": "x\n"})
	capture(t, s, src, true)
	for _, p := range []string{"secret.txt", "dir"} {
		if err := os.Chmod(filepath.Join(src.WorkTree, p), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(src.WorkTree, p), 0o755) })
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refsBefore := storeGit(t, s, "apps/locked", "for-each-ref")
	_, err := s.Capture(context.Background(), CaptureRequest{Source: src, By: "user:ana"})
	var r *Refusal
	if !errors.As(err, &r) || r.Rule != RuleUnreadable || !strings.Contains(err.Error(), "secret.txt") || !strings.Contains(err.Error(), "dir") {
		t.Fatalf("unreadable files: %v", err)
	}
	if refs := storeGit(t, s, "apps/locked", "for-each-ref"); refs != refsBefore || exists(filepath.Join(s.Dir("apps/locked"), "index")) {
		t.Errorf("a refused capture changed the store or kept the index")
	}
	// and the estimate a dry run makes says so too, before any capture
	if _, err := s.Estimate(context.Background(), src); !errors.As(err, &r) || r.Rule != RuleUnreadable {
		t.Errorf("estimate of an unreadable tree: %v", err)
	}
}
