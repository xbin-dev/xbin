package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers D173 D174 PD-18 (LAND) — a person's partition swaps like a
// deployment, and its generations answer the same two questions:
//
//   - D174: its code is the primary's, so a generation it built from the
//     work tree while a pause took its checkpoint asks SettledCodeFor before
//     it serves. When the pause commits, that generation never serves — and,
//     a partition's generation being one whose token must not outlive its
//     stop, its token is revoked and it is no longer the partition's starting
//     generation (discardGen); the generation before it serves on. When the
//     pause fails, it swaps in as any save's would.
//   - D173: a Gen the proxy took before the swap reports Retired once the
//     swap replaced it, and EnsurePartitionGen answers the new one — what the
//     proxy's rerouting follows for a request the swap cut off. A Gen the
//     discarded swap never replaced stays live.
func TestPartitionWorkTreeGenerationAfterPause(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := map[bool]string{true: "the pause commits", false: "the pause fails"}[commit]
		t.Run(name, func(t *testing.T) {
			w := newTokenWorld(t, userOnly, registry.Manifest{})
			lw := &liveWorld{settled: make(chan struct{}, 1)}
			w.r.DeploymentHooks = DeploymentHooks{CodeFor: lw.codeFor, SettledCodeFor: lw.settledCodeFor}
			gen := func() Gen {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				g, err := w.r.EnsurePartitionGen(ctx, w.c, "main", "user:alice", StartInteractive)
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			g1 := gen()
			tok1 := w.lastToken()
			w.settleParts()
			w.f.takeLog()

			w.f.holdNextBuild()
			w.r.Changed(w.c) // a save: the partition restarts onto its build, which parks
			waitParked(t, w.f, "apps/x")
			end := lw.pause() // the pause begins while the save builds
			w.f.releaseBuild()
			select { // the partition's new generation waits for the pause to end
			case <-lw.settled:
			case <-time.After(time.Minute): // a hang guard
				t.Fatal("the partition's work-tree generation never asked whether live reload still drives it")
			}
			tok2 := w.lastToken()
			if tok2 == tok1 || !w.authn(tok2) {
				t.Fatalf("the new generation's token %q isn't registered (the first's: %q)", tok2, tok1)
			}
			end(commit)
			w.settleParts()

			s := w.state("user:alice")
			s.mu.Lock()
			starting := s.pt.starting
			s.mu.Unlock()
			if starting != nil {
				t.Error("a generation that never served is still the partition's starting one")
			}
			got := gen()
			log := w.f.takeLog()
			if commit {
				if got.Sock() != g1.Sock() || g1.Retired() || got.Retired() {
					t.Errorf("after the pause committed, the partition answers %s (retired %v), want the generation before it, %s, live", got.Sock(), g1.Retired(), g1.Sock())
				}
				if w.authn(tok2) {
					t.Error("the discarded generation's token still authenticates")
				}
				if !w.authn(tok1) {
					t.Error("the serving generation's token no longer authenticates")
				}
				if count(log, "start apps/x user:alice main g2") != 1 || count(log, "stop apps/x user:alice main g2") != 1 || count(log, "stop apps/x user:alice main g1") != 0 {
					t.Errorf("fake log %q: want g2 started and stopped unserved, g1 kept", log)
				}
			} else {
				if got.Sock() == g1.Sock() || !strings.HasSuffix(got.Sock(), "/g2.sock") || got.Retired() {
					t.Errorf("after the pause failed, the partition answers %s, want its save's generation g2", got.Sock())
				}
				if !g1.Retired() {
					t.Error("the generation the swap replaced isn't retired: a request it cut off wouldn't follow the swap")
				}
				if !w.authn(tok2) {
					t.Error("the serving generation's token doesn't authenticate")
				}
			}
		})
	}
}
