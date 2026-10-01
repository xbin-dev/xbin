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

// TestKeepWakeUp: a person's partition keeps its way back registered while
// it idles (resume_keep.go) — xbind revokes a stopping partition's token
// before it exits, so leaveWakeUp's calls at the exit are refused. Once its
// takeover cleared the jobs it registers the `wake` a sleeping run needs,
// never twice, deletes it when nothing waits, registers nothing while an
// engine holds the partition, and an idle coding agent's reclaim is a
// `wake` too; unpartitioned and at the global instance it does nothing.
func TestKeepWakeUp(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs":
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			calls = append(calls, fmt.Sprintf("put %v %v", j["name"], j["schedule"]))
		case r.Method == "DELETE":
			calls = append(calls, "delete "+strings.TrimPrefix(r.URL.Path, "/api/xbin/cron/jobs/"))
		}
		w.WriteHeader(200)
	}))
	take := func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(calls, "; ")
		calls = nil
		return out
	}
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	old := wakeKeepDelay
	wakeKeepDelay = time.Millisecond
	t.Cleanup(func() { wakeKeepDelay = old })

	// newTestAgent's, in alice's partition, with the gateway's calls on: its
	// takeover clears the jobs through the fake gateway, then keeps them
	setMode(t, modeUser, "alice")
	d := newTestDB(t)
	ag := &Agent{db: d, repl: newReplRegistry(), toolSem: make(chan struct{}, maxToolsGlobal),
		blobs: newMemBlobs(), blobCache: newBlobCache(8 << 20)}
	e := newEngine(d, ag, &fakeLLM{t: t, gates: map[string]chan struct{}{}}, "")
	ag.keepWakeUp(time.Now()) // before its takeover cleared the jobs: nothing
	e.takeOver()
	t.Cleanup(func() { e.Shutdown(2 * time.Second) })
	waitFor(t, "the takeover's clear, then the keeper ready", func() bool {
		ag.wakeKeep.mu.Lock()
		defer ag.wakeKeep.mu.Unlock()
		return ag.wakeKeep.ready && !ag.wakeKeep.pending
	})
	if got := take(); got != "delete resume; delete heartbeat; delete wake" {
		t.Fatalf("the takeover's calls: %s (nothing registered: nothing waits)", got)
	}
	now := time.Now()
	id, _ := d.createRun("x", "", 0)
	wake := now.Add(2 * time.Hour).Unix()
	_, _ = d.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, wake, id)
	ag.keepWakeUpSoon()
	want := "put wake " + wakeSchedule(wake)
	waitFor(t, "the sleeping run's wake, registered while idle", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(calls, "; ") == want
	})
	take()
	ag.keepWakeUp(now)
	if got := take(); got != "" {
		t.Fatalf("the same wake again: %s", got)
	}

	// while an engine holds the partition (a timer armed): nothing
	ag.eng.mu.Lock()
	ag.eng.timers[id] = time.NewTimer(time.Hour)
	ag.eng.mu.Unlock()
	_, _ = d.q.Exec(`UPDATE runs SET status='waiting_input', wake_at=0 WHERE id=?`, id)
	ag.keepWakeUp(now)
	if got := take(); got != "" {
		t.Fatalf("while held: %s", got)
	}
	ag.eng.mu.Lock()
	ag.eng.timers[id].Stop()
	delete(ag.eng.timers, id)
	ag.eng.mu.Unlock()
	// nothing waits any more (a person's answer does): the wake goes
	ag.keepWakeUp(now)
	if got := take(); got != "delete wake" {
		t.Fatalf("nothing to wait for: %s", got)
	}

	// an idle coding agent: its reclaim's minute
	old2 := harnessIdleTest
	harnessIdleTest = 15 * time.Minute
	t.Cleanup(func() { harnessIdleTest = old2 })
	_, _ = d.q.Exec(`UPDATE runs SET engine='harness', status='idle' WHERE id=?`, id)
	last := now.Add(-time.Minute).UnixMilli()
	if _, err := d.q.Exec(`INSERT INTO harness_sessions (run_id, provider, ref, exec_id, state, last_active_ms) VALUES (?, 'fake', 'apps/cs|b', 'e1', 'live', ?)`,
		id, last); err != nil {
		t.Fatal(err)
	}
	ag.keepWakeUp(now)
	if got, w := take(), "put wake "+wakeSchedule(time.UnixMilli(last).Add(15*time.Minute).Unix()); got != w {
		t.Fatalf("an idle coding agent: %s (want %s)", got, w)
	}

	// the global instance: nothing
	setMode(t, modeGlobal, "")
	ag.keepWakeUp(now)
	ag.keepWakeUpSoon()
	time.Sleep(20 * time.Millisecond)
	if got := take(); got != "" {
		t.Fatalf("the global instance: %s", got)
	}
}
