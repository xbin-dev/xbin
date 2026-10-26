package main

import (
	"fmt"
	"sync"
	"testing"
)

// rootRuns keeps the root summaries the hub publishes for one tree, those it
// still holds from before included.
type rootRuns struct {
	mu   sync.Mutex
	data []map[string]any
}

func watchRoot(t *testing.T, ag *Agent, root int64) *rootRuns {
	s, missed, _ := ag.eng.hub.subscribe(root, 0, who{kind: whoSystem})
	l := &rootRuns{}
	take := func(evs []*Event) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, ev := range evs {
			if ev.Type == evRun && ev.Run == root {
				if d, ok := ev.Data.(map[string]any); ok {
					l.data = append(l.data, d)
				}
			}
		}
	}
	take(missed)
	go func() {
		for range s.wake {
			take(s.drain())
		}
	}()
	t.Cleanup(func() { ag.eng.hub.unsubscribe(s) })
	return l
}

// says: the root's latest summary says waiting == want (and was published
// after at least n others).
func (l *rootRuns) says(want bool, n int) func() bool {
	return func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		if len(l.data) <= n {
			return false
		}
		w, ok := l.data[len(l.data)-1]["waiting"].(bool)
		return ok && w == want
	}
}

func (l *rootRuns) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.data)
}

// D198: a coding agent below a conversation that parks on a person is news
// at once — a push for the run that waits (linking to it), and the
// conversation's row says it waits (its summary on the stream carries
// `waiting`, the list, Needs you and the native view follow); answering it
// says the row no longer waits.
func TestHarnessChildParkPushesAndShowsOnTheRow(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	log := withPushes(ag, 0)
	f := fakeOf(ag)
	f.on(turnOnly(lastIs("user", "harness spawn")), callTools(tc("c1", "subagent_spawn",
		`{"task":"perm","label":"fake coder","harness":"fake","summary":"Ask the coding agent"}`))).once()
	f.on(turnOnly(lastIs("tool", "")), say("done"))
	parent := codingParent(t, mux, box, "harness spawn")
	rows := watchRoot(t, ag, parent.ID)
	child := kidsOf(t, ag, parent.ID, 1)[0]
	p := parkOf(t, ag, child.ID, "approval")
	hwait(t, "a push", func() bool { return len(log.all()) >= 1 })
	if n := log.all()[0]; n.User != "alice" || n.Kind != needApproval || n.Link != fmt.Sprintf("#c=%d", child.ID) {
		t.Fatalf("the push: %+v", n)
	}
	hwait(t, "the row to say it waits", rows.says(true, 0))
	seen := rows.count()
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", child.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the row to say it no longer waits", rows.says(false, seen))
}

// A coding agent's sign-in is a push too (its words: needs_push.go
// harnessNeed).
func TestHarnessLoginPushes(t *testing.T) {
	ag, mux, box := harnessFixture(t, false, "--require-login")
	log := withPushes(ag, 0)
	run := askHarness(t, mux, box, "hi")
	parkOf(t, ag, run.ID, "login")
	hwait(t, "a push", func() bool { return len(log.all()) >= 1 })
	if n := log.all()[0]; n.Kind != needLogin || n.Link != fmt.Sprintf("#c=%d", run.ID) {
		t.Fatalf("the push: %+v", n)
	}
}
