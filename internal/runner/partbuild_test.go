package runner

// covers PD-18 — one build per change for people's partitions
// (plans/partitions/03 §A.3, §A.7): a save, a work-tree deploy and a
// restart of the primary each build once, and every live instance
// restarts onto that build; a start past the change builds anew, as the
// primary's always did. Also the admission details the build rides on: a
// person's failed start retried, the 503's exact text, and the memory the
// caps derive from.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// buildRecorder numbers every build's bin and records what each instance
// started from ("apps/x", "apps/x user:p0").
type buildRecorder struct {
	mu     sync.Mutex
	builds int
	bins   map[string]string
}

func recordBuilds(w *partWorld) *buildRecorder {
	b := &buildRecorder{bins: map[string]string{}}
	e := w.r.engine
	build, start := e.build, e.start
	e.build = func(c *registry.Component) (string, error) {
		bin, err := build(c)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.builds++
		return fmt.Sprintf("%s#%d", bin, b.builds), err
	}
	e.start = func(c *registry.Component, bin string, gen int) (*instance, error) {
		b.mu.Lock()
		b.bins[fakeKey(c)] = bin
		b.mu.Unlock()
		return start(c, bin, gen)
	}
	return b
}

func (b *buildRecorder) bin(k string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bins[k]
}

// covers PD-18 — TestPartitionBuildOnce (03 §A.3): N people's and the
// global instance's concurrent first starts build the work tree once and
// reuse its bin; a save and a restart of the primary (a deploy that swaps
// it: deploy advances the build before its own) build once each and
// restart every live instance onto it; a start after the change's
// restarts builds anew.
func TestPartitionBuildOnce(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	b := recordBuilds(w)
	w.f.holdNextBuild()
	const n = 8
	parts := make([]string, n+1)
	for i := range n {
		parts[i] = fmt.Sprintf("user:p%d", i)
	}
	parts[n] = "global"
	got := make([]string, n+1)
	var wg sync.WaitGroup
	for i, p := range parts {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = w.ensure(p) }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.f.mu.Lock()
		parked := w.f.parked != ""
		w.f.mu.Unlock()
		if parked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no build parked")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // the others reach the shared build meanwhile
	w.f.releaseBuild()
	wg.Wait()
	for i, g := range got {
		if g != "g1" {
			t.Errorf("%s: %s", parts[i], g)
		}
	}
	log := w.f.takeLog()
	if b, s := count(log, "build "), count(log, "start "); b != 1 || s != n+1 {
		t.Errorf("%d builds, %d starts, want 1 and %d: %q", b, s, n+1, log)
	}

	moved := func(what string, op func() error, want int) {
		t.Helper()
		if err := op(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		w.settleParts()
		log := w.f.takeLog()
		if b, s, st := count(log, "build "), count(log, "start "), count(log, "stop "); b != 1 || s != n+1 || st != n+1 {
			t.Errorf("%s: %d builds, %d starts, %d stops; want 1, %d, %d: %q", what, b, s, st, n+1, n+1, log)
		}
		for _, k := range []string{"apps/x", "apps/x user:p0", "apps/x user:p7"} {
			if got := b.bin(k); !strings.HasSuffix(got, fmt.Sprintf("#%d", want)) {
				t.Errorf("%s: %s runs %q, want build #%d", what, k, got, want)
			}
		}
	}
	ctx := context.Background()
	moved("a save", func() error { w.r.Changed(w.c); return nil }, 2)
	for i := range n {
		if g := w.ensure(parts[i]); g != "g2" {
			t.Errorf("%s after the save: %s, want g2", parts[i], g)
		}
	}
	if log := w.f.takeLog(); count(log, "build ") != 0 {
		t.Errorf("a request after the restarts built again: %q", log)
	}
	moved("a restart", func() error { return w.r.Restart(ctx, w.c, util.MainDeployment, nil) }, 3)

	w.r.StopPartition("apps/x", "main", "user:p0")
	w.f.takeLog()
	if g := w.ensure("user:p0"); g != "g1" {
		t.Fatalf("p0 after its stop: %s", g)
	}
	if log := w.f.takeLog(); count(log, "build ") != 1 {
		t.Errorf("a start past the change's restarts reused the build: %q", log)
	}
}

// covers PD-18 — a work-tree deploy of what every partition already runs,
// with the global instance serving it too, restarts nothing.
func TestPartitionDeployNoop(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.ensure("global")
	w.ensure("user:alice")
	w.f.takeLog()
	if err := w.r.Deploy(context.Background(), w.c, util.MainDeployment, Code{WorkTree: true}, nil, nil); err != nil {
		t.Fatal(err)
	}
	w.settleParts()
	if log := w.f.takeLog(); len(log) != 0 {
		t.Errorf("a deploy of what runs moved something: %q", log)
	}
}

// covers PD-18 — a person's start that failed for them alone (its spawn,
// its health) is tried again by their next request, through admission; a
// build error stays until the tile's code changes.
func TestPartitionStartRetried(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.f.failNext("health", "apps/x", "main", 1)
	if got := w.ensure("user:alice"); !strings.Contains(got, "did not become healthy") {
		t.Fatalf("alice's first start: %s", got)
	}
	if got := w.ensure("user:alice"); got != "g1" {
		t.Errorf("alice's next request: %s, want a fresh start", got)
	}
	w.f.failNext("build", "apps/x", "main", 1)
	if got := w.ensure("user:bob"); !strings.Contains(got, "fake compile error") {
		t.Fatalf("bob's start: %s", got)
	}
	w.f.takeLog()
	if got := w.ensure("user:bob"); !strings.Contains(got, "fake compile error") {
		t.Errorf("bob's next request: %s, want the build error, sticky", got)
	}
	if log := w.f.takeLog(); len(log) != 0 {
		t.Errorf("a sticky build error built again: %q", log)
	}
}

// covers PD-18 — the refusal past the caps says exactly the documented
// 503 text, and is still ErrPartitionBusy and an sbx refusal.
func TestPartitionBusyText(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.r.PartitionCapsFor = func(string) (int, int) { return 1, 0 }
	w.ensure("user:alice")
	_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:bob", StartInteractive)
	if want := "too many people's instances of apps/x are running; try again shortly"; err == nil || err.Error() != want {
		t.Errorf("the refusal says %v, want exactly %q", err, want)
	}
	if !errors.Is(err, ErrPartitionBusy) || !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("the refusal %v isn't ErrPartitionBusy and an sbx refusal", err)
	}
	// bx doctor's "caps hit recently" (plans/partitions 06 §7)
	if h, ok := w.r.PartitionCapHits()["apps/x"]; !ok || h.Kind != "refused" || h.Count != 1 || h.At.IsZero() {
		t.Errorf("the cap hit isn't recorded: %+v", w.r.PartitionCapHits())
	}
}

// covers PD-18 — a background start's partition (a cron delivery's) isn't
// evicted before the proxy tracks its connection: every start's answer
// holds it off eviction for partitionHandoff.
func TestPartitionHandoff(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.r.PartitionCapsFor = func(string) (int, int) { return 1, 0 }
	if got := w.ensureDep("main", "user:alice", StartBackground); got != "g1" {
		t.Fatalf("alice's delivery: %s", got)
	}
	if got := w.ensure("user:bob"); !strings.Contains(got, "too many people's instances") || !w.running("user:alice") {
		t.Errorf("bob right after alice's delivery started: %s (alice running %v)", got, w.running("user:alice"))
	}
	w.f.advance(partitionHandoff)
	if got := w.ensure("user:bob"); got != "g1" || w.running("user:alice") {
		t.Errorf("bob once the handoff passed, alice untracked: %s (alice running %v)", got, w.running("user:alice"))
	}
}

// covers PD-18 — the caps' memory: MemTotal, or the lowest memory.max on
// xbind's own cgroup path when that is lower (a container), "max" and a
// missing file ignored.
func TestHostMemory(t *testing.T) {
	dir := t.TempDir()
	meminfo := filepath.Join(dir, "meminfo")
	self := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sys")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(meminfo, "MemTotal:       65536000 kB\nMemFree: 1 kB\n")
	if got := hostMemory(meminfo, self, root); got != 65536000<<10 {
		t.Errorf("no cgroup file: %d", got)
	}
	write(self, "0::/system.slice/xbind.service\n")
	write(filepath.Join(root, "system.slice/xbind.service/memory.max"), "max\n")
	if got := hostMemory(meminfo, self, root); got != 65536000<<10 {
		t.Errorf("memory.max max: %d", got)
	}
	write(filepath.Join(root, "system.slice/memory.max"), "2147483648\n")
	if got := hostMemory(meminfo, self, root); got != 2<<30 {
		t.Errorf("a 2 GiB slice above xbind: %d", got)
	}
	if tl, ws := partitionCapsFrom(hostMemory(meminfo, self, root)); tl != 5 || ws != 10 {
		t.Errorf("caps in a 2 GiB container: %d/%d, want 5/10", tl, ws)
	}
	if got := hostMemory(filepath.Join(dir, "none"), self, root); got != 2<<30 {
		t.Errorf("no meminfo, a cgroup cap: %d", got)
	}
}
