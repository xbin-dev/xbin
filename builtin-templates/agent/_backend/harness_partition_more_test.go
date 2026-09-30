package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// More of coding agents in a partitioned agent (harness_partition.go): the
// hold, the wake and the brake around sign-ins and idle adapters.

// TestHarnessSignInRests: a coding agent parked on its sign-in (a prompt
// refused signed out) waits on a person — it doesn't keep a person's
// partition up and leaves no wake-up (C4); a device-code sign-in AgTT
// started holds it while AgTT waits for the adapter's answer, and the held
// prompt's turn after it holds it too, until it rests.
func TestHarnessSignInRests(t *testing.T) {
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login", "--device-ms=1500")
	open := holdCounter(ag.eng)
	run := askIn(t, h, box, "echo hi")
	parkOf(t, ag, run.ID, "login")
	if ag.eng.harnessOf(run.ID) == nil {
		t.Fatal("the signed-out adapter isn't this process's")
	}
	hwait(t, "the hold let go while it waits for a sign-in", func() bool { return open.Load() == 0 })
	if at := ag.db.userWake(time.Now()); at.runnable || at.wake != 0 {
		t.Fatalf("a sign-in park's wake-up: %+v", at)
	}

	// a device code: held while AgTT waits for the adapter's answer
	rec := serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/harness/authenticate", run.ID), `{"method":"fake-device"}`, alicesFrame("read")), 202, nil)
	if !strings.Contains(rec.Body.String(), "FAKE-1234") {
		t.Fatalf("the device code: %s", rec.Body)
	}
	ag.eng.mu.Lock()
	holds := ag.eng.harnessHoldsLocked() // the hold's own goroutine opens it: asked of what it reads
	ag.eng.mu.Unlock()
	if !holds {
		t.Fatal("a sign-in under way doesn't hold")
	}
	hwait(t, "the held prompt's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	hwait(t, "the hold let go once it rests", func() bool { return open.Load() == 0 })
}

// TestHarnessRestAfterWork: a rest that follows a turn's end never lands
// after the next turn went to work (the end's poke may send a queued prompt
// first) — the mark says so — and in a person's partition a prompt that
// waited for a turn keeps the hold through its own turn.
func TestHarnessRestAfterWork(t *testing.T) {
	setMode(t, modeUser, "alice")
	e := &Engine{db: newTestDB(t), harness: map[int64]*hsess{}}
	s := newHsess(e, &Run{ID: 1}, 1, 0, harnessProvider("fake", nil))
	mark := s.workMark()
	s.toWork(true) // the next prompt, sent by the pass the end's poke woke
	s.armIdleFrom(time.Time{}, mark)
	s.mu.Lock()
	armed := s.idleT != nil
	s.mu.Unlock()
	if s.rest.Load() || armed {
		t.Fatalf("a stale rest landed after the next turn's work: rest %v, reclaim armed %v", s.rest.Load(), armed)
	}
	s.armIdleFrom(time.Time{}, s.workMark()) // control: nothing since the mark
	s.mu.Lock()
	armed = s.idleT != nil
	s.disarmIdleLocked()
	s.mu.Unlock()
	if !s.rest.Load() || !armed {
		t.Fatal("control: a rest with no work since its mark didn't rest")
	}

	ag, h, box, _, _ := harnessPartition(t)
	open := holdCounter(ag.eng)
	run := askIn(t, h, box, "slow")
	hwait(t, "the first turn under way", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick") })
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"slow again"}`, alicesFrame("read")), 200, nil)
	hwait(t, "the queued prompt's turn under way", func() bool {
		return strings.Count(transcript(ag.db, run.ID), "U:") >= 2 && strings.Contains(draftText(ag.eng, run.ID), "tick")
	})
	if hs, _ := ag.db.harnessSession(run.ID); hs.PromptState == "" || open.Load() != 1 {
		t.Fatalf("the second turn doesn't hold: %+v, %d holds", hs, open.Load())
	}
	hwait(t, "both turns", func() bool { return turnOver(ag, run.ID)() && strings.Count(fullText(ag.db, run.ID), "tick 9") == 2 })
	hwait(t, "the hold let go", func() bool { return open.Load() == 0 })
}

// TestHarnessReclaimUnderBrake: stopping an idle adapter moves no work, so
// the brake doesn't hold it off — a partition woken for an idle one's
// reclaim while conf isn't read (it reads as on) still stops it, and then
// leaves nothing (no wake-up loop); under a known halt a stopping partition
// still leaves the reclaim's wake-up, and nothing else.
func TestHarnessReclaimUnderBrake(t *testing.T) {
	harnessIdleTest = 30 * time.Minute
	t.Cleanup(func() { harnessIdleTest = 0 }) // after the engine settles (cleanups run last first)
	ag, h, box, _, kv := harnessPartition(t)
	run := askIn(t, h, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	before, _ := ag.db.harnessSession(run.ID)
	s := ag.eng.harnessOf(run.ID)
	ag.eng.Shutdown(2 * time.Second)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the predecessor's consumer runs on")
	}
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET last_active_ms=? WHERE run_id=?`,
		time.Now().Add(-31*time.Minute).UnixMilli(), run.ID); err != nil {
		t.Fatal(err)
	}
	kv.mu.Lock()
	kv.m = map[string][]byte{} // conf gone (a wipe): unread again — the brake reads as on
	kv.mu.Unlock()
	confIn.mu.Lock()
	confIn.state, confIn.at = confPending, time.Time{}
	confIn.mu.Unlock()
	confIn.refresh()
	if !ag.eng.halted() {
		t.Fatal("the fixture: the brake doesn't read as on while conf is unread")
	}
	b := successor(t, ag)
	hwait(t, "the reclaim under the brake", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs.State == hsStopped && b.harnessOf(run.ID) == nil
	})
	if after, _ := ag.db.harnessSession(run.ID); after.Gen != before.Gen {
		t.Fatalf("after the reclaim: %+v", after)
	}
	if at := ag.db.userWake(time.Now()); at.runnable || at.wake != 0 {
		t.Fatalf("after the reclaim, a wake-up again: %+v", at)
	}
}

// TestHarnessIdleWakeUnderHalt: under a manager's halt a stopping partition
// leaves nothing for its runs — but an idle coding agent's reclaim is still
// its `wake` job (A-M4).
func TestHarnessIdleWakeUnderHalt(t *testing.T) {
	var mu sync.Mutex
	var jobs []map[string]any
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs" {
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			jobs = append(jobs, j)
		}
		w.WriteHeader(200)
	}))
	take := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := jobs
		jobs = nil
		return out
	}
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "1", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	confIn = newConfReader(kv, nil)
	confIn.refresh()
	d := newTestDB(t)
	ag := &Agent{db: d}
	sleeper, _ := d.createRun("sleeps", "", 0)
	_, _ = d.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, time.Now().Add(time.Hour).Unix(), sleeper)
	ag.leaveWakeUp(d)
	if j := take(); len(j) != 0 {
		t.Fatalf("control: under a halt a sleeping run leaves nothing: %v", j)
	}
	coder, _ := d.createRun("coder", "", 0)
	_, _ = d.q.Exec(`UPDATE runs SET engine='harness', status='idle' WHERE id=?`, coder)
	last := time.Now().Add(-time.Minute)
	if err := d.putHarnessSession(&harnessSession{RunID: coder, RootID: coder, State: hsLive, ExecID: "e1", LastActiveMs: last.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	ag.leaveWakeUp(d)
	want := wakeSchedule(last.Add(15 * time.Minute).Unix())
	if j := take(); len(j) != 1 || j[0]["name"] != "wake" || j[0]["schedule"] != want {
		t.Fatalf("an idle coding agent under a halt: %v, want one wake at %s", j, want)
	}
}

// TestHarnessSendingGone: a prompt marked on its way to an adapter that
// never opened its session (so no successor attaches it) isn't work that
// brings a partition back every minute, and the takeover that finds it
// ends its turn — "send it again" — rather than leaving the run running.
func TestHarnessSendingGone(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	s := ag.eng.harnessOf(run.ID)
	ag.eng.Shutdown(2 * time.Second)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the predecessor's consumer runs on")
	}
	// as if it was still opening its session when a prompt was marked
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET state='starting', prompt_state='sending', snapshot='', acp_session='' WHERE run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID)
	b := successor(t, ag)
	waitStatus(t, ag.db, run.ID, statusError)
	hs, _ := ag.db.harnessSession(run.ID)
	if r, _ := ag.db.getRun(run.ID); !strings.Contains(r.Result, "send it again") || hs.PromptState != "" || hs.State != hsStopped || b.harnessOf(run.ID) != nil {
		t.Fatalf("after the takeover: %q %+v", r.Result, hs)
	}
	// (in a person's partition such a prompt isn't work: TestHarnessUserWake)
}

// TestHarnessBrakeReachesChild: a halt reaches a coding agent a person's
// built-in conversation spawned, mid-turn — the child cancelled with why,
// its adapter stopped — as it reaches one the person started (A-M5).
func TestHarnessBrakeReachesChild(t *testing.T) {
	shorten(t, &confTTL, 0)
	ag, h, box, _, kv := harnessPartition(t)
	f := fakeOf(ag)
	f.on(turnOnly(lastIs("user", "harness spawn")), callTools(tc("c1", "subagent_spawn",
		`{"task":"slow","label":"fake coder","harness":"fake","summary":"Ask the coding agent"}`))).once()
	f.on(turnOnly(lastIs("tool", "")), say("the coding agent is done"))
	var parent Run
	serveJSON(t, h, as("POST", "/ask", mustJSON(map[string]any{"text": "harness spawn", "class": "coding",
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}}), alicesFrame("read")), 200, &parent)
	child := kidsOf(t, ag, parent.ID, 1)[0]
	if child.Engine != engineHarness {
		t.Fatalf("the child: %+v", child)
	}
	hwait(t, "the child's turn under way", func() bool { return strings.Contains(draftText(ag.eng, child.ID), "tick") })
	putConf(kv, "1", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	waitStatus(t, ag.db, child.ID, statusCanceled)
	if r, _ := ag.db.getRun(child.ID); !strings.Contains(r.Result, haltReason) {
		t.Fatalf("the child's cancel doesn't say why: %q", r.Result)
	}
	hwait(t, "the child's adapter stopped", func() bool {
		hs, _ := ag.db.harnessSession(child.ID)
		return hs != nil && hs.State == hsStopped && ag.eng.harnessOf(child.ID) == nil
	})
	if strings.Contains(fullText(ag.db, child.ID), "tick 9") {
		t.Fatal("the child's turn ran to its end under the halt")
	}
}
