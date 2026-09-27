package runner

// covers SC-ZERO PO-12 — rows 1–15 of 15-test-plan §2.4: the runner's state
// machine as it is today, driven through the engine seam with no feature
// code. Each row runs its steps (settling after each), then compares the
// exact fake log, the exact tape of hub events, what every ensure step
// answered, and the final Status() entry.

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

type seamRow struct {
	id     string
	man    registry.Manifest // apps/x's manifest; a zero one means runtime "go"
	bare   bool              // use man as it is, even with no runtime
	steps  []string
	log    []string // the fake engine's log
	sorted bool     // compare the log as a multiset (concurrent drains; row 11)
	tape   []string
	ensure []string // each ensure step's answer, in order
	status string   // statusOf after the last step
}

const (
	bld = "build apps/x main@worktree"
	bs  = "build-start apps/x"
	bo  = "build-ok apps/x"
)

func st(g int) string { return fmt.Sprintf("start apps/x main g%d", g) }
func sp(g int) string { return fmt.Sprintf("stop apps/x main g%d", g) }

// crashLoop is the crash-loop breaker's text for apps/x; the log name in it
// is main's log key, hand-maintained (util.CompKey("apps/x")).
const crashLoop = "backend crash-looping (3 exits); fix the code and save to retry — see .xbin/log/apps~x-ebdae547.log"

// seamRows pin today's runner (15-test-plan §2.4). Step tokens:
//
//	ensure                  Ensure(ctx, apps/x), recording its answer
//	save, grant             Changed(apps/x): the watcher, and OnGrantChange
//	                        (internal/boot/boot.go:508-512), reach the runner alike
//	crash                   the current generation exits unasked
//	reap+<d>, wait+<d>      advance the fake clock by d; reap+ then runs reapOnce
//	track                   hold a Track (an open stream) to the row's end
//	seal, unseal            ShouldRun false (and Stop, as brk.StopBackend does) / true
//	restart                 StopAll (xbind's shutdown), then a new Runner over the same root
//	fail-next-build/health  the next build or health check fails
//	hold-build, release-build  park the next build / let it finish
var seamRows = []seamRow{
	{id: "1 first request builds and starts",
		steps: []string{"ensure"},
		log:   []string{bld, st(1)}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1"},
	{id: "2 a save swaps blue/green, the old generation stops after the new is healthy",
		steps: []string{"ensure", "save"},
		log:   []string{bld, st(1), bld, st(2), sp(1)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1"}, status: "healthy g2"},
	{id: "3 a save with no process yet builds nothing",
		steps:  []string{"save"},
		status: "idle g0"},
	{id: "4 a failed build leaves the old generation serving",
		steps: []string{"ensure", "fail-next-build", "save", "ensure"},
		log:   []string{bld, st(1), bld + ": fail"},
		tape:  []string{bs, bo, bs, "build-error apps/x: build failed:\nfake compile error"},
		// the error stays on the state, but Ensure's fast path serves g1
		ensure: []string{"g1", "g1"}, status: "healthy g1 · build failed:\nfake compile error"},
	{id: "5 a generation that never answers is stopped and the old one serves",
		steps: []string{"ensure", "fail-next-health", "save", "ensure"},
		log:   []string{bld, st(1), bld, st(2), sp(2)},
		tape:  []string{bs, bo, bs, "build-error apps/x: backend did not become healthy: fake: never answered"},
		// Status reports the spent generation number (2) while g1 serves
		ensure: []string{"g1", "g1"}, status: "healthy g2 · backend did not become healthy: fake: never answered"},
	{id: "6 a crash leaves the state dirty, the next request rebuilds",
		steps: []string{"ensure", "crash", "ensure"},
		log:   []string{bld, st(1), bld, st(2)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1", "g2"}, status: "healthy g2"},
	{id: "7a crashLimit crashes inside the window break the loop",
		steps: []string{"ensure", "crash", "ensure", "crash", "ensure", "crash", "ensure"},
		log:   []string{bld, st(1), bld, st(2), bld, st(3)},
		tape:  []string{bs, bo, bs, bo, bs, bo, "build-error apps/x: " + crashLoop},
		// the last ensure builds nothing and answers the breaker's error
		ensure: []string{"g1", "g2", "g3", "error: " + crashLoop}, status: "failed g3 · " + crashLoop},
	{id: "7b a save clears the crash loop",
		steps:  []string{"ensure", "crash", "ensure", "crash", "ensure", "crash", "ensure", "save"},
		log:    []string{bld, st(1), bld, st(2), bld, st(3), bld, st(4)},
		tape:   []string{bs, bo, bs, bo, bs, bo, "build-error apps/x: " + crashLoop, bs, bo},
		ensure: []string{"g1", "g2", "g3", "error: " + crashLoop}, status: "healthy g4"},
	{id: "7w crashes spread wider than the window are not a loop",
		steps:  []string{"ensure", "crash", "wait+31s", "ensure", "crash", "wait+31s", "ensure", "crash", "wait+31s", "ensure"},
		log:    []string{bld, st(1), bld, st(2), bld, st(3), bld, st(4)},
		tape:   []string{bs, bo, bs, bo, bs, bo, bs, bo},
		ensure: []string{"g1", "g2", "g3", "g4"}, status: "healthy g4"},
	{id: "8 an idle backend is reaped after 30 minutes and restarts lazily",
		steps: []string{"ensure", "reap+31m", "ensure"},
		log:   []string{bld, st(1), sp(1), bld, st(2)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1", "g2"}, status: "healthy g2"},
	{id: "8n not before 30 minutes",
		steps: []string{"ensure", "reap+29m"},
		log:   []string{bld, st(1)}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1"},
	{id: "8r every request restarts the idle clock",
		steps: []string{"ensure", "wait+20m", "ensure", "reap+20m"},
		log:   []string{bld, st(1)}, tape: []string{bs, bo},
		ensure: []string{"g1", "g1"}, status: "healthy g1"},
	{id: "9 an open stream is never reaped",
		steps: []string{"ensure", "track", "reap+31m"},
		log:   []string{bld, st(1)}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1"},
	{id: "10 an alwaysOn backend is never reaped",
		man:   registry.Manifest{Runtime: "go", AlwaysOn: true},
		steps: []string{"ensure", "reap+31m"},
		log:   []string{bld, st(1)}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1"},
	{id: "11 saves during one slow build coalesce into one more build",
		// the first save starts the slow build; the other two land while it
		// is parked. g1's drain stop runs concurrently with the next build.
		steps:  []string{"ensure", "hold-build", "save", "save", "save", "release-build"},
		log:    []string{bld, st(1), bld, st(2), sp(1), bld, st(3), sp(2)},
		sorted: true,
		tape:   []string{bs, bo, bs, bo, bs, bo},
		ensure: []string{"g1"}, status: "healthy g3"},
	{id: "12 a tile that may not run is refused",
		steps:  []string{"seal", "ensure", "save"},
		ensure: []string{"error: component apps/x is not enabled"}, status: "idle g0"},
	{id: "12s a sealed tile stops, a save doesn't respawn it, the first request after unseal does",
		steps: []string{"ensure", "seal", "save", "unseal", "ensure"},
		log:   []string{bld, st(1), sp(1), bld, st(2)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1", "g2"}, status: "healthy g2"},
	{id: "13 a grant change restarts the backend",
		steps: []string{"ensure", "grant"},
		log:   []string{bld, st(1), bld, st(2), sp(1)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1"}, status: "healthy g2"},
	{id: "14 after a restart the first request builds afresh",
		steps: []string{"ensure", "restart", "ensure"},
		log:   []string{bld, st(1), sp(1), bld, st(1)}, tape: []string{bs, bo, bs, bo},
		ensure: []string{"g1", "g1"}, status: "healthy g1"},
	{id: "15 a static tile has no backend",
		man:    registry.Manifest{Runtime: "static"},
		steps:  []string{"ensure", "save"},
		ensure: []string{"error: component apps/x has no long-running backend"}, status: "-"},
	{id: "15n a tile with no runtime has no backend",
		bare:   true,
		steps:  []string{"ensure", "save"},
		ensure: []string{"error: component apps/x has no long-running backend"}, status: "-"},
}

// covers SC-ZERO PO-12 — 15-test-plan §2.4 rows 1–15 against today's runner.
func TestStateMachineRows(t *testing.T) {
	for _, row := range seamRows {
		t.Run(row.id, func(t *testing.T) { runSeamRow(t, row) })
	}
}

func runSeamRow(t *testing.T, row seamRow) {
	man := row.man
	if man.Runtime == "" && !row.bare {
		man.Runtime = "go"
	}
	c := &registry.Component{Path: "apps/x", Manifest: man}
	r, f, tp := newSeamRunner(t, c)
	var answers []string
	var holds []func()
	defer func() {
		for _, release := range holds {
			release()
		}
	}()
	for _, step := range row.steps {
		switch {
		case step == "ensure":
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			sock, err := r.Ensure(ctx, c)
			cancel()
			answers = append(answers, answer(sock, err))
		case step == "save", step == "grant":
			r.Changed(c)
		case step == "crash":
			f.crash(c.Path, "main")
		case strings.HasPrefix(step, "reap+"), strings.HasPrefix(step, "wait+"):
			d, err := time.ParseDuration(step[len("reap+"):])
			if err != nil {
				t.Fatalf("step %q: %v", step, err)
			}
			f.advance(d)
			if strings.HasPrefix(step, "reap+") {
				r.reapOnce()
			}
		case step == "track":
			holds = append(holds, r.Track(c.Path))
		case step == "seal":
			f.seal(c.Path, true)
			r.Stop(c.Path)
		case step == "unseal":
			f.seal(c.Path, false)
		case step == "restart":
			r.StopAll()
			r = f.runner()
		case step == "fail-next-build":
			f.failNext("build", c.Path, "main", 1)
		case step == "fail-next-health":
			f.failNext("health", c.Path, "main", 1)
		case step == "hold-build":
			f.holdNextBuild()
		case step == "release-build":
			f.releaseBuild()
		default:
			t.Fatalf("unknown step %q", step)
		}
		settle(t, r, f)
	}

	log, want := f.takeLog(), row.log
	if row.sorted {
		log, want = sortedCopy(log), sortedCopy(want)
	}
	if !equalStrings(log, want) {
		t.Errorf("fake log:\n got %q\nwant %q", log, want)
	}
	if got := tp.take(); !equalStrings(got, row.tape) {
		t.Errorf("tape:\n got %q\nwant %q", got, row.tape)
	}
	if !equalStrings(answers, row.ensure) {
		t.Errorf("ensure answers:\n got %q\nwant %q", answers, row.ensure)
	}
	if got := statusOf(r, c.Path); got != row.status {
		t.Errorf("status:\n got %q\nwant %q", got, row.status)
	}
}

// equalStrings treats nil and empty alike.
func equalStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// covers SC-ZERO — the seam changes no shipped constant: idle reap stays 30
// minutes, the crash breaker 3 exits in 10 s windows, drains 30 s (D8).
func TestSeamKeepsConstants(t *testing.T) {
	if idleReap != 30*time.Minute || crashLimit != 3 || crashWindow != 10*time.Second ||
		drainDeadline != 30*time.Second || healthTimeout != 5*time.Second {
		t.Fatalf("idleReap %v crashLimit %d crashWindow %v drainDeadline %v healthTimeout %v",
			idleReap, crashLimit, crashWindow, drainDeadline, healthTimeout)
	}
	if (&Runner{}).engine != nil {
		t.Fatal("a zero Runner must run today's engine")
	}
}
