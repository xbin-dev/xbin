package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// partitionConf wires ag's conf reader the way startMode does in a person's
// partition (the test set the mode), over kv, and reads it once.
func partitionConf(t *testing.T, ag *Agent, kv kvStore) {
	t.Helper()
	confIn = newConfReader(kv, nil)
	confIn.parked = ag.db.brakeParked
	confIn.onHaltOff = func() { ag.eng.recover() }
	confIn.refresh()
}

// TestBrakeInPartition: a halt the global instance mirrors into conf cancels
// a person's running conversation at its next step (as PUT /halt does
// unpartitioned), and a stopped partition then leaves no wake-up; lifted,
// the next message works as ever.
func TestBrakeInPartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	kv := newMemKV()
	putConf(kv, "", `{}`)
	ag := newTestAgent(t, newTestDB(t))
	partitionConf(t, ag, kv)
	f := fakeOf(ag)
	f.on(lastUser("work"), callTools(tc("c1", "no_such_tool", "{}"))).block("g").once()
	id := newRun(t, ag, Config{}, "work")
	waitFor(t, "the model call", func() bool { return f.inFlight() == 1 })
	putConf(kv, "1", `{}`) // a manager halts the agent at the global instance
	f.release("g")
	waitStatus(t, ag.db, id, statusCanceled)
	if r, _ := ag.db.getRun(id); !strings.Contains(r.Result, haltReason) {
		t.Fatalf("the cancel doesn't say why: %q", r.Result)
	}
	if !brakeIdle() {
		t.Fatal("a partition under a known halt would leave a wake-up")
	}
	if ag.db.userWake(time.Now()).runnable {
		t.Fatal("a cancelled run is still runnable work")
	}
	putConf(kv, "", `{}`)
	f.on(lastUser("again"), say("back"))
	send(t, ag, id, "again")
	waitFor(t, "the answer after the halt", func() bool { return strings.Contains(transcript(ag.db, id), "back") })
}

// TestBrakeFailsClosed: until conf has been read, a person's partition
// parks its runs (nothing cancelled) and looks again; once conf answers,
// they move without a new message. A request for work is queued meanwhile.
func TestBrakeFailsClosed(t *testing.T) {
	setMode(t, modeUser, "alice")
	shorten(t, &brakeWatchMin, 20*time.Millisecond)
	kv := newMemKV() // global hasn't written conf yet
	ag := newTestAgent(t, newTestDB(t))
	partitionConf(t, ag, kv)
	if v := confIn.view(true); v.State != confPending || v.get("halt") != "1" {
		t.Fatalf("an unread conf: %+v", v)
	}
	f := fakeOf(ag)
	f.on(lastUser("early"), say("answered once conf came"))
	id := newRun(t, ag, Config{}, "early")
	time.Sleep(100 * time.Millisecond)
	if st := statusOf(ag.db, id); st != statusRunning || len(f.callsFor(id)) != 0 {
		t.Fatalf("a run moved (or was cancelled) before conf was read: %s, %d calls", st, len(f.callsFor(id)))
	}
	putConf(kv, "", `{"config":"{}"}`) // the global instance starts and mirrors
	waitFor(t, "the parked run to answer", func() bool { return strings.Contains(transcript(ag.db, id), "answered once conf came") })
}

// TestBrakeRequests: haltBlocks in a person's partition — a request for work
// is queued while conf is unread, refused (503, saying why) without conf in
// uses, and refused (503) when a manager's message can't take the halt off
// at the global instance, rather than queued behind a brake that stays on.
func TestBrakeRequests(t *testing.T) {
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	ag, h := partAgent(t)
	do := func(level string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, as("POST", "/ask", `{"text":"hi"}`, alicesFrame(level)))
		return rec
	}
	confIn = newConfReader(newMemKV(), nil) // unread: queued
	if rec := do("read"); rec.Code/100 != 2 {
		t.Fatalf("a reader's ask before conf was read: %d %s", rec.Code, rec.Body)
	}
	// the queued run's pass reads confIn: let it park before the test swaps it
	waitFor(t, "the queued run's pass to end", func() bool {
		ag.eng.mu.Lock()
		defer ag.eng.mu.Unlock()
		return len(ag.eng.actors) == 0
	})
	confIn = newConfReader(emptyKV{}, nil) // not in uses
	if rec := do("write"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "conf resource") {
		t.Fatalf("an ask without conf: %d %s", rec.Code, rec.Body)
	}
	kv := newMemKV()
	putConf(kv, "1", `{}`)
	confIn = newConfReader(kv, nil)
	stubGlobalCalls(t, func(method, path string, body []byte) (int, string) { return 502, `{"error":"down"}` })
	if rec := do("write"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "taking the pause off") {
		t.Fatalf("a manager's ask when the halt can't come off: %d %s", rec.Code, rec.Body)
	}
}

// slowKV is a kv whose reads wait until released.
type slowKV struct {
	*memKV
	gate chan struct{}
}

func (k slowKV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	<-k.gate
	return k.memKV.Get(ctx, key)
}

// TestConfNeverWaitsInTx: a settings read inside a transaction (the db has
// one connection) never waits on the network — it serves the cached copy
// while a refresh runs beside it.
func TestConfNeverWaitsInTx(t *testing.T) {
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	kv := slowKV{memKV: newMemKV(), gate: make(chan struct{})}
	putConf(kv.memKV, "", `{"config":"{\"model\":\"m\"}"}`)
	close(kv.gate)
	confIn = newConfReader(kv, nil)
	confIn.refresh()
	kv.gate = make(chan struct{}) // conf answers slowly from now on
	confIn.kv = kv
	d := newTestDB(t)
	done := make(chan string, 1)
	go func() {
		_ = d.Tx(func(tx *DB) error {
			done <- tx.getSetting("config")
			return nil
		})
	}()
	select {
	case v := <-done:
		if parseConfig(v).Model != "m" {
			t.Fatalf("the cached config: %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read inside a transaction waited on conf")
	}
	close(kv.gate)
}

// refusingKV refuses writes of keys with a prefix (a kv value too big).
type refusingKV struct {
	*memKV
	prefix string
}

func (k refusingKV) Put(ctx context.Context, key string, val []byte) error {
	if strings.HasPrefix(key, k.prefix) {
		return errors.New("kv put: HTTP 413: too big")
	}
	return k.memKV.Put(ctx, key, val)
}

// TestConfMirrorSkipsWhatItCantWrite: a shared skill the kv refuses doesn't
// hold back the halt switch, the config or the other skills; the error
// arms the retry.
func TestConfMirrorSkipsWhatItCantWrite(t *testing.T) {
	setMode(t, modeGlobal, "")
	g := newTestDB(t)
	kv := refusingKV{memKV: newMemKV(), prefix: "skill:huge"}
	_ = g.upsertSkill(&Skill{Name: "huge", Content: "…"})
	_ = g.upsertSkill(&Skill{Name: "small", Content: "ok"})
	_ = g.putSetting("halt", "1")
	m := newConfMirror(kv, g)
	if err := m.sync(context.Background()); err == nil {
		t.Fatal("a refused skill went unreported")
	}
	keys, _ := kv.List(context.Background(), "")
	if strings.Join(keys, ",") != "halt,settings,skill:small" {
		t.Fatalf("conf after a refused skill: %v", keys)
	}
	// a partitioned agent refuses saving what conf can't hold
	_, h := partAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("PUT", "/skills", `{"name":"big","content":"`+strings.Repeat("x", confMaxValue)+`"}`,
		map[string]string{"X-XBin-From": "owner", "X-XBin-Role": "admin"}))
	if rec.Code != 400 {
		t.Fatalf("saving a shared skill conf can't hold: %d %s", rec.Code, rec.Body)
	}
}

// TestBrakeWatchStops: the look at conf re-arms only while runs are parked.
func TestBrakeWatchStops(t *testing.T) {
	shorten(t, &brakeWatchMin, 5*time.Millisecond)
	var mu sync.Mutex
	parked := true
	c := newConfReader(newMemKV(), nil)
	c.parked = func() bool { mu.Lock(); defer mu.Unlock(); return parked }
	c.watchBrake()
	time.Sleep(40 * time.Millisecond)
	c.mu.Lock()
	armed := c.watch != nil
	c.mu.Unlock()
	if !armed {
		t.Fatal("the watch stopped while runs are parked")
	}
	mu.Lock()
	parked = false
	mu.Unlock()
	waitFor(t, "the watch to stop", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.watch == nil
	})
}
