package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// snapshot records every entry beneath dir: its type, mode and content (a
// symlink's target). A test's own reading, not the daemon's.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			v += " → " + l
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h := sha256.Sum256(b)
			v += " " + hex.EncodeToString(h[:]) + " " + fi.ModTime().String()
		}
		out[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s: %s changed: %q → %q", what, p, v, after[p])
		}
	}
	for p, v := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("%s: %s appeared (%s)", what, p, v)
		}
	}
}

// covers T1 NP-13-12 — the drift count (L16) counts the files that differ
// from a checkpoint — changed, added, removed; not a nested component's —
// in one confined run that changes nothing durable: the store (its refs,
// objects, persistent index), the work tree and the capture rate are as they
// were, and no scratch directory stays. The run binds the store read-only,
// its scratch directory read-write and the work tree read-only, no network;
// the tile's own git config never runs, and a work tree of symlinks to a
// FIFO outside it (and a FIFO in it) is counted without anything opening
// them. A tile without a store gets ErrNoStore, and none is made.
func TestDriftCountChangesNothingDurable(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	s.Caps.Burst, s.Caps.Every = 3, time.Hour
	ctx := context.Background()
	src := tile(t, s, "apps/crm", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n", "sub/comp/x.js": "nested\n"})
	src.Nested = []string{"sub/comp"}
	repo(t, src.WorkTree)
	marker := filepath.Join(t.TempDir(), "PWNED")
	evil := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"core.fsmonitor", evil}, {"core.hooksPath", filepath.Dir(evil)}, {"filter.x.clean", evil}} {
		hostGit(t, src.WorkTree, "config", kv[0], kv[1])
	}
	c1 := capture(t, s, src, true) // one capture of three

	if n, err := s.Drift(ctx, src, c1.Hash); err != nil || n != 0 {
		t.Fatalf("drift of an unchanged work tree: %d (%v)", n, err)
	}
	_ = os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src.WorkTree, "new.txt"), []byte("new\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src.WorkTree, ".gitattributes"), []byte("* filter=x\n"), 0o644)
	_ = os.Remove(filepath.Join(src.WorkTree, "b.txt"))
	_ = os.WriteFile(filepath.Join(src.WorkTree, "sub", "comp", "x.js"), []byte("another tile's\n"), 0o644)

	store := s.Dir("apps/crm")
	storeBefore, wtBefore := snapshot(t, store), snapshot(t, src.WorkTree)
	before := rec.count()
	for i := 0; i < 5; i++ { // drift counts don't spend the capture rate
		if n, err := s.Drift(ctx, src, c1.Hash); err != nil || n != 4 {
			t.Fatalf("drift: %d (%v), want 4 (a.txt, new.txt, .gitattributes, b.txt)", n, err)
		}
	}
	sameSnapshot(t, "the store", storeBefore, snapshot(t, store))
	sameSnapshot(t, "the work tree", wtBefore, snapshot(t, src.WorkTree))
	if exists(driftDir(store)) {
		t.Error("the drift count left its scratch directory")
	}
	for i, c := range rec.cmds[before:] {
		wantBinds := []string{driftDir(store) + " rw", src.WorkTree + " ro"}
		var binds []string
		for _, b := range c.Binds {
			mode := "rw"
			if b.RO {
				mode = "ro"
			}
			if b.Src != b.Dst || b.Mask {
				t.Errorf("run %d: bind %+v", i, b)
			}
			binds = append(binds, b.Src+" "+mode)
		}
		if c.Dir != store || !c.ReadOnlyDir || strings.Join(binds, ",") != strings.Join(wantBinds, ",") || c.Net != confine.NetNone {
			t.Errorf("run %d: Dir %s ro %v binds %v net %v", i, c.Dir, c.ReadOnlyDir, binds, c.Net)
		}
		if body := strings.TrimPrefix(c.Argv[2], preamble); bareGit.MatchString(body) {
			t.Errorf("run %d runs git without the hardened prefix:\n%s", i, body)
		}
		for _, a := range c.Argv[3:] {
			if strings.Contains(a, "a.txt") || strings.Contains(a, "sub/comp") {
				t.Errorf("run %d carries a tile path on argv: %q", i, c.Argv[3:])
			}
		}
	}
	if rec.count()-before != 5 {
		t.Errorf("%d confined runs for five drift counts", rec.count()-before)
	}

	c2 := capture(t, s, src, false) // two of three: the drift counts spent none
	if n, err := s.Drift(ctx, src, c2.Hash); err != nil || n != 0 {
		t.Errorf("drift against the new checkpoint: %d (%v)", n, err)
	}
	if n, err := s.Drift(ctx, src, c1.Hash); err != nil || n != 4 {
		t.Errorf("drift against the older checkpoint: %d (%v)", n, err)
	}
	capture(t, s, src, false)
	if _, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana"}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("the fourth capture: %v, want the rate limit (the drift counts took none of it)", err)
	}
	if exists(marker) {
		t.Fatal("the tile's git config ran a command")
	}

	// a hostile work tree: symlinks to a FIFO outside, a FIFO inside
	w := newTripwire(t)
	for _, rel := range []string{"link.js", "xbin.json", ".git/HEAD"} {
		p := filepath.Join(src.WorkTree, filepath.FromSlash(rel))
		_ = os.Remove(p)
		if err := os.Symlink(w.path, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(src.WorkTree, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		n, err := s.Drift(ctx, src, c1.Hash)
		if err == nil && n != 6 { // + link.js, xbin.json (symlinks, counted as files); the FIFO isn't a file
			err = fmt.Errorf("drift of the hostile tree: %d, want 6", n)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the drift count blocked")
	}
	if w.tripped.Load() {
		t.Fatal("the drift count opened the FIFO outside the work tree")
	}

	// no store
	zero := tile(t, s, "apps/zero", map[string]string{"a.txt": "a\n"})
	if _, err := s.Drift(ctx, zero, c1.Hash); !errors.Is(err, ErrNoStore) {
		t.Errorf("drift of a tile without a store: %v", err)
	}
	if s.Exists("apps/zero") {
		t.Error("a drift count created a store")
	}
	if _, err := s.Drift(ctx, src, "c:"+c1.Hash[:7]); err == nil {
		t.Error("a drift count took a short id")
	}
}

// covers NP-13-12 — a file rewritten with the same size within the second
// its checkpoint's capture wrote the persistent index still counts: the
// drift count's copy of the index keeps its mtime, so git's racy-entry check
// re-reads such a file rather than trusting its stat data (a git that
// compares whole seconds sees nothing else change). The rewrite lands in the
// capture's second and the count runs in the next one.
func TestDriftSeesRacyRewrite(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/racy", map[string]string{"x.txt": "x\n"})
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 20*time.Millisecond)))
	if err := os.WriteFile(filepath.Join(src.WorkTree, "r.txt"), []byte("r1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "r.txt"), []byte("r2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 20*time.Millisecond)))
	if n, err := s.Drift(ctx, src, c.Hash); err != nil || n != 1 {
		t.Fatalf("drift after a same-size rewrite in the capture's second: %d (%v), want 1", n, err)
	}
}
