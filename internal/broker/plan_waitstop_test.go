package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/server"
)

// A waiting line is watched from the moment it is on record — not before.
// A look at the waiting lines that lands while its plan is still being
// applied (a later line of the same plan takes its time) used to find the
// person registered and nothing of theirs waiting yet, and drop them: the
// line then never fired, whatever the tile declared.
func TestWaitingLineIsWatchedOnceItIsWritten(t *testing.T) {
	for name, during := range map[string]func(w *csWorld){
		"a look while the plan is applied":               func(w *csWorld) { w.srv.CheckPlanWaits() },
		"the tile provides it while the plan is applied": func(w *csWorld) { w.dice(diceProvides) },
	} {
		t.Run(name, func(t *testing.T) {
			w := startingWorld(t)
			_, bob := w.session("bob")
			starts := 0
			w.srv.PlanStarter = func(context.Context, auth.Principal, []byte) (string, int, string) {
				if starts++; starts == 2 { // the start AFTER the line that waits: the plan's results are not written yet
					during(w)
					if w.binding("apps/assistant", "mcp") != "" {
						t.Errorf("a line of a plan still being applied was bound")
					}
				}
				return fmt.Sprintf("sess-%d", starts), 200, ""
			}
			cs := w.plan(t, "bob", strings.TrimSuffix(waitPlan, "]")+`,
			{"kind":"agent.start","args":{"tile":"apps/assistant","brief":"Goal: greet.","harness":"claude"},"why":"and its own agent"}]`)
			done := w.approve(t, cs.ID, `{"items":[1,2,3,4,5]}`, bob)
			if r := results(done); starts != 2 || r != "applied,applied,applied,waiting,applied" && r != "applied,applied,applied,applied,applied" {
				t.Fatalf("the plan applied: %s (%d starts; %s)", r, starts, done.Items[4].Error)
			}
			w.dice(diceProvides)
			w.srv.CheckPlanWaits()
			got := w.set(t, "bob", cs.ID).Items[3]
			ok, refused := w.waitRows(t, "bob", cs.ID)
			if got.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" || len(ok) != 1 || len(refused) != 0 {
				t.Fatalf("the tile provides what was approved, and the line: %s (%s), bound %q, log: %d applied, %+v refused — want it applied once", got.Result, got.Error, w.binding("apps/assistant", "mcp"), len(ok), refused)
			}
		})
	}
}

// A waiting line never outlives its person's agent, whichever way the
// agent was stopped and whether or not that path remembered the waiting
// lines: every reason xbind has for refusing an Allow is judged at the
// line itself. A pause or a hold keeps it waiting — it applies when that
// clears; every other reason ends it, and nothing brings it back.
func TestWaitingLineEndsWhateverStoppedTheAgent(t *testing.T) {
	keeps := map[string]bool{"paused": true, "held": true}
	codes := []string{"an error the gate doesn't name"}
	// every code the gate answers, read from the source: a new one is judged here before it ships
	src, _ := filepath.Glob("../server/*.go")
	for _, f := range src {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`OffError\{"([a-z-]+)"`).FindAllSubmatch(b, -1) {
			if !slices.Contains(codes, string(m[1])) {
				codes = append(codes, string(m[1]))
			}
		}
	}
	for _, want := range []string{"paused", "held", "workspace-off", "no-agent", "no-sandbox"} {
		if !slices.Contains(codes, want) {
			t.Fatalf("the gate's codes read from the source: %v — %q is missing", codes, want)
		}
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			w, _, cs := waitWorld(t, "bob")
			// the stop as the gate sees it — set there directly: no handler ran, none could cancel anything
			w.srv.SetDelegationGate(func(_, user string) error {
				if strings.Contains(code, " ") {
					return errors.New(code)
				}
				return &server.OffError{Code: code, Msg: "stopped (" + code + ")"}
			})
			w.dice(diceProvides)
			got := w.set(t, "bob", cs.ID).Items[3]
			if w.binding("apps/assistant", "mcp") != "" {
				t.Fatalf("bound while the agent was stopped (%s)", got.Result)
			}
			w.srv.SetDelegationGate(nil) // …and the agent is back
			w.srv.CheckPlanWaits()
			after := w.set(t, "bob", cs.ID).Items[3]
			if keeps[code] {
				if got.Result != delegation.ItemWaiting || after.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" {
					t.Fatalf("a %s agent's line: %s while stopped, %s (%s) after — want it to wait, then apply", code, got.Result, after.Result, after.Error)
				}
				return
			}
			if got.Result != delegation.ItemCancelled || !strings.Contains(got.Error, "nothing was bound") || after.Result != delegation.ItemCancelled || w.binding("apps/assistant", "mcp") != "" {
				t.Fatalf("stopped (%s): %s (%q) at the stop, %s after it cleared, bound %q — want it cancelled for good", code, got.Result, got.Error, after.Result, w.binding("apps/assistant", "mcp"))
			}
			if ok, refused := w.waitRows(t, "bob", cs.ID); len(ok) != 0 || len(refused) != 1 {
				t.Fatalf("the log: applied %+v, refused %+v", ok, refused)
			}
		})
	}
}

// At most once, whoever looks at the same moment. The second look is
// started while the first is between its judgement and its bind (the hook
// holds it there): it waits for the first to end and then finds the line
// applied — one bind, one row. Without that rule the second runs through
// in the hook's window and the line is bound and logged twice, every time.
func TestWaitingLineAppliesOnceUnderTwoLooks(t *testing.T) {
	w, _, cs := waitWorld(t, "bob")
	var binds atomic.Int32
	second := make(chan struct{})
	bindWaitHook = func() {
		if binds.Add(1) > 1 {
			return
		}
		go func() { defer close(second); w.srv.CheckPlanWaits() }()
		select {
		case <-second: // it ran through while the first look was mid-bind
		case <-time.After(300 * time.Millisecond): // it waits for the first
		}
	}
	t.Cleanup(func() { bindWaitHook = nil })
	w.write(map[string]string{"apps/dice/xbin.json": diceProvides, "apps/dice/backend/main.go": diceMain})
	w.rescan()
	w.srv.CheckPlanWaits()
	select {
	case <-second:
	case <-time.After(10 * time.Second):
		t.Fatal("the second look never ended")
	}
	got := w.set(t, "bob", cs.ID).Items[3]
	ok, refused := w.waitRows(t, "bob", cs.ID)
	if binds.Load() != 1 || len(ok) != 1 || len(refused) != 0 || got.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" {
		t.Fatalf("two looks at once: %d binds, %d applied rows, %d refused, the line %s (%s) — want one of each, applied", binds.Load(), len(ok), len(refused), got.Result, got.Error)
	}
}
