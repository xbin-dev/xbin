package main

// Taking a coding agent over after a handoff or restart, where it goes
// wrong: the successor's attach refused (the adapter is stopped, the turn
// ends), not answered (tried again), a sign-in park with no session (kept
// attached), codex's detached turn (followed again), another client on
// the stdio socket (not a successor: taken back).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// execRuns: the exec still runs at the manager (gone counts as not).
func execRuns(tg hpTarget, id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ex, err := tg.Conn.ExecGet(ctx, tg.ID, id)
	return err == nil && ex.State == "running"
}

// handOff lets ag's engine go (a handoff) and waits for its consumers.
func handOff(t *testing.T, ag *Agent, runs ...int64) {
	t.Helper()
	var all []*hsess
	for _, id := range runs {
		if s := ag.eng.harnessOf(id); s != nil {
			all = append(all, s)
		}
	}
	ag.eng.BeginShutdown()
	for _, s := range all {
		select {
		case <-s.done:
		case <-time.After(15 * time.Second):
			t.Fatal("a consumer outlived the handoff")
		}
	}
}

// The successor's attach is refused (the sandbox's egress became none):
// the adapter is stopped — not left to work on unread — the session
// stored failed with why, the turn in flight ended with it.
func TestHarnessAttachRefusedStops(t *testing.T) {
	ag, mux, box, m := harnessFixtureWith(t, nil, false)
	run := askHarness(t, mux, box, "stall")
	hwait(t, "stalling", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.PromptState == "sent" && draftText(ag.eng, run.ID) == "stalling"
	})
	tg := ag.eng.harnessOf(run.ID).pipe.t
	hs, _ := ag.db.harnessSession(run.ID)
	handOff(t, ag, run.ID)
	m.mu.Lock()
	m.boxes[box.ID].Egress = "none"
	m.mu.Unlock()
	b := successor(t, ag)
	hwait(t, "the refusal", func() bool { h, _ := ag.db.harnessSession(run.ID); return h.State == hsFailed })
	hwait(t, "the turn's end", turnOver(ag, run.ID))
	r, _ := ag.db.getRun(run.ID)
	h, _ := ag.db.harnessSession(run.ID)
	if r.Status != statusError || !strings.Contains(r.Result, "egress is none") || !strings.Contains(h.Error, "egress is none") || b.harnessOf(run.ID) != nil {
		t.Fatalf("after the refused attach: run %s %q, session %+v", r.Status, r.Result, h)
	}
	hwait(t, "the adapter stopped", func() bool { return !execRuns(tg, hs.ExecID) })
}

// A session stored failed whose adapter still runs (a stop whose kill
// didn't get through): /cancel and deleting the conversation end it.
func TestHarnessStopsFailedExec(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	a := askHarness(t, mux, box, "stall")
	d := askHarness(t, mux, box, "stall")
	for _, id := range []int64{a.ID, d.ID} {
		hwait(t, "stalling", func() bool {
			hs, _ := ag.db.harnessSession(id)
			return hs != nil && hs.PromptState == "sent" && draftText(ag.eng, id) == "stalling"
		})
	}
	tg := ag.eng.harnessOf(a.ID).pipe.t
	ha, _ := ag.db.harnessSession(a.ID)
	hd, _ := ag.db.harnessSession(d.ID)
	handOff(t, ag, a.ID, d.ID)
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET state='failed', error='refused' WHERE run_id IN (?,?)`, a.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	successor(t, ag)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/cancel", a.ID), nil); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	hwait(t, "the cancel", func() bool { return statusOf(ag.db, a.ID) == statusCanceled })
	hwait(t, "the cancelled one's adapter stopped", func() bool { return !execRuns(tg, ha.ExecID) })
	if w := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d", d.ID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	hwait(t, "the deleted one's adapter stopped", func() bool { return !execRuns(tg, hd.ExecID) })
}

// The manager doesn't answer while the successor takes over (it restarts
// too): the attach is tried again, backing off, until it takes — the turn
// the adapter ran meanwhile is read and ends once.
func TestHarnessAttachRetried(t *testing.T) {
	var down atomic.Bool
	wrap := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"restarting","refusal":"unavailable"}`))
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	ag, mux, box, _ := harnessFixtureWith(t, wrap, false)
	run := askHarness(t, mux, box, "slow")
	hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
	handOff(t, ag, run.ID)
	down.Store(true)
	b := successor(t, ag)
	time.Sleep(1500 * time.Millisecond)
	if b.harnessOf(run.ID) != nil {
		t.Fatal("attached while the manager was down")
	}
	down.Store(false)
	hwait(t, "the attach once the manager is back", func() bool { return b.harnessOf(run.ID) != nil })
	hwait(t, "the turn", turnOver(ag, run.ID))
	if text := fullText(ag.db, run.ID); strings.Count(text, "tick 0 ") != 1 || strings.Count(text, "tick 9 ") != 1 || statusOf(ag.db, run.ID) != statusIdle {
		t.Fatalf("after the attach: %s %q", statusOf(ag.db, run.ID), text)
	}
}

// codex refuses a session signed out: its sign-in park has an adapter and
// no session. The successor attaches it (not "still opening": killed) so
// the sign-in card still works — signing in opens the session and sends
// the held message.
func TestHarnessLoginParkHandoff(t *testing.T) {
	ag, _, _, run := signedOutFixture(t)
	handOff(t, ag, run.ID)
	hs, _ := ag.db.harnessSession(run.ID)
	var st map[string]any // what spawnHarness's AwaitLogin branch stores for codex
	_ = json.Unmarshal([]byte(hs.Snapshot), &st)
	st["sessionId"] = ""
	raw, _ := json.Marshal(st)
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET snapshot=?, acp_session='' WHERE run_id=?`, string(raw), run.ID); err != nil {
		t.Fatal(err)
	}
	b := successor(t, ag)
	hwait(t, "the attach", func() bool { return b.harnessOf(run.ID) != nil })
	h, _ := ag.db.harnessSession(run.ID)
	r, _ := ag.db.getRun(run.ID)
	if h.State != hsLogin || h.ExecID != hs.ExecID || r.Status != statusWaiting || parsePending(r.Pending).Kind != "login" {
		t.Fatalf("after the takeover: session %+v, run %s %s", h, r.Status, r.Pending)
	}
	res, err := b.harnessAuthenticate(context.Background(), r, "fake-api-key", "k")
	if err != nil || res.State != "ready" {
		t.Fatalf("authenticate: %+v %v", res, err)
	}
	hwait(t, "the held message's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	if h, _ := ag.db.harnessSession(run.ID); h.State != hsLive || h.ACPSession == "" || h.Gen != hs.Gen {
		t.Fatalf("the session: %+v", h)
	}
}

// codex's detached turn (a steer answered startedNewTurn) is stored as a
// running run with no prompt in flight: the successor follows it again —
// it ends once the adapter is quiet, and an interrupt ends it (one queued
// before the attach too).
func TestHarnessDetachedTurnHandoff(t *testing.T) {
	detached := func(t *testing.T) (*Agent, *http.ServeMux, *Run) {
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "echo one")
		hwait(t, "the turn", turnOver(ag, run.ID))
		// what harnessSteer's startedNewTurn branch stores, and follows
		if _, err := ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		ag.eng.harnessOf(run.ID).followDetached()
		return ag, mux, run
	}
	quiet := func(t *testing.T, d time.Duration) {
		old := hDetachedQuiet
		hDetachedQuiet = d
		t.Cleanup(func() { hDetachedQuiet = old })
	}
	t.Run("quiet", func(t *testing.T) {
		quiet(t, 300*time.Millisecond)
		ag, _, run := detached(t)
		handOff(t, ag, run.ID)
		b := successor(t, ag)
		hwait(t, "the attach", func() bool { return b.harnessOf(run.ID) != nil })
		hwait(t, "the detached turn's end", turnOver(ag, run.ID))
		if st := statusOf(ag.db, run.ID); st != statusIdle {
			t.Fatalf("after the handoff: %s", st)
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		quiet(t, time.Hour)
		ag, mux, run := detached(t)
		handOff(t, ag, run.ID)
		b := successor(t, ag)
		hwait(t, "the attach", func() bool { return b.harnessOf(run.ID) != nil })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/interrupt", run.ID), nil); w.Code != 200 {
			t.Fatalf("interrupt: %d %s", w.Code, w.Body)
		}
		hwait(t, "the interrupted turn", turnOver(ag, run.ID)) // (not the quiet: an hour)
		if st := statusOf(ag.db, run.ID); st != statusIdle {
			t.Fatalf("after the interrupt: %s", st)
		}
	})
	t.Run("interrupt-before-attach", func(t *testing.T) {
		quiet(t, time.Hour)
		ag, _, run := detached(t)
		handOff(t, ag, run.ID)
		if _, _, err := ag.queue(run.ID, inboxInterrupt, inboxBody{Reason: "interrupted by the owner"}, ""); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		hwait(t, "the interrupted turn", turnOver(ag, run.ID)) // (not the quiet: an hour)
		if st := statusOf(ag.db, run.ID); st != statusIdle {
			t.Fatalf("after the interrupt: %s", st)
		}
	})
	t.Run("message-waits", func(t *testing.T) {
		quiet(t, 300*time.Millisecond)
		ag, _, run := detached(t)
		handOff(t, ag, run.ID)
		if _, _, err := ag.queue(run.ID, inboxHPrompt, inboxBody{Text: "echo two", Source: "human", Sender: "alice"}, ""); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		hwait(t, "the next prompt's turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
		})
	})
	t.Run("first-prompt-isnt-one", func(t *testing.T) {
		// a new run is running before its first prompt goes (turn_seq 0):
		// a handoff between its adapter's start and the prompt isn't a
		// detached turn — the prompt goes as it would have
		quiet(t, time.Hour)
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "echo one")
		hwait(t, "the turn", turnOver(ag, run.ID))
		if _, err := ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET turn_seq=0 WHERE run_id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		handOff(t, ag, run.ID)
		if _, _, err := ag.queue(run.ID, inboxHPrompt, inboxBody{Text: "echo two", Source: "human", Sender: "alice"}, ""); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		hwait(t, "the prompt's turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
		})
	})
}

// Another client attaching the adapter's stdio socket while this process
// still owns the session (the epoch unchanged: not a successor) doesn't
// make it let the session go: it attaches again, and the turn ends.
func TestHarnessForeignStdioAttach(t *testing.T) {
	ag, mux, box := harnessFixture(t, true)
	run := askHarness(t, mux, box, "slow")
	hwait(t, "a few ticks", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 2") })
	s := ag.eng.harnessOf(run.ID)
	if s == nil || !s.pipe.Stdio() {
		t.Fatalf("no stdio session: %v", s)
	}
	tg := s.pipe.t
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := xbin.DialManagerStdio(ctx, tg.Conn.M.URL, tg.ID, s.pipe.ExecID(), xbin.ManagerStdioOptions{User: "mallory", Client: sbxClient()})
	if err != nil {
		t.Fatalf("the other client's attach: %v", err)
	}
	defer c.Close()
	hwait(t, "the turn", turnOver(ag, run.ID))
	if ag.eng.harnessOf(run.ID) != s || s.pipe.Err() != nil {
		t.Fatalf("the session was let go: %v", s.pipe.Err())
	}
	if text := fullText(ag.db, run.ID); strings.Count(text, "tick 0 ") != 1 || strings.Count(text, "tick 9 ") != 1 || statusOf(ag.db, run.ID) != statusIdle {
		t.Fatalf("the turn: %s %q", statusOf(ag.db, run.ID), text)
	}
}
