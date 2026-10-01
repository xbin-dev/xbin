package main

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// failingEpoch makes e's epoch reads fail n times (an injected read error),
// then read the database; it counts the reads.
type failingEpoch struct {
	mu    sync.Mutex
	fail  int
	reads int
}

func (f *failingEpoch) install(e *Engine) {
	e.readEpoch = func(q queryer, key string) (int64, error) {
		f.mu.Lock()
		f.reads++
		failing := f.fail > 0
		if failing {
			f.fail--
		}
		f.mu.Unlock()
		if failing {
			return 0, &epochReadError{errors.New("injected: disk I/O error")}
		}
		return readEpoch(q, key)
	}
}

func (f *failingEpoch) set(n int) {
	f.mu.Lock()
	f.fail, f.reads = n, 0
	f.mu.Unlock()
}

func (f *failingEpoch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func owns(e *Engine) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owned
}

// covers LAND (the agent engine's false takeover) — a read of the engine
// epoch that fails is an error, never an epoch: fenced tries its
// transaction again (its fn not yet run) and writes once the read answers;
// a read that keeps failing returns the error with nothing written, and the
// engine keeps its ownership and goes on writing once the read answers
// again. Only an epoch that was read and differs fences an engine out (the
// stale engine of TestEpochFencesAStaleEngine). The harness pipe's guard
// and a takeover read the same way: a failed read refuses that write, or is
// read again, and is never taken for 0 — which once rewound a takeover's
// epoch to 1.
func TestEpochReadFailureIsNoTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	a := fileAgent(t, path)
	a.eng.lockPath = ""
	a.eng.takeOver()
	t.Cleanup(func() { a.eng.Shutdown(time.Second) })
	var f failingEpoch
	f.install(a.eng)
	ran := 0
	write := func() error {
		return a.eng.fenced(func(t *DB) error { ran++; return t.putSetting("probe", itoa(int64(ran))) })
	}

	// a transient failure: the transaction is tried again, and writes once
	f.set(2)
	if err := write(); err != nil {
		t.Fatalf("a transient read failure: %v", err)
	}
	if ran != 1 || f.count() != 3 || a.eng.db.getSetting("probe") != "1" {
		t.Fatalf("ran %d, reads %d, probe %q: want one write after three reads", ran, f.count(), a.eng.db.getSetting("probe"))
	}
	if !owns(a.eng) {
		t.Fatal("a transient read failure took the engine's ownership")
	}

	// a failure that stays: an error, nothing written, still the owner
	f.set(1 << 20)
	err := write()
	var re *epochReadError
	if !errors.As(err, &re) || errors.Is(err, errFenced) {
		t.Fatalf("a read that keeps failing: %v, want the read's error", err)
	}
	if ran != 1 || f.count() != epochReadTries {
		t.Fatalf("ran %d, reads %d: want no write after %d reads", ran, f.count(), epochReadTries)
	}
	if !owns(a.eng) || a.eng.base.Err() != nil {
		t.Fatal("a failed epoch read stopped the engine as if another had taken over")
	}
	if _, err := a.eng.epochNow(); !errors.As(err, &re) || errors.Is(err, errFenced) {
		t.Fatalf("the guard's read: %v, want the read's error", err)
	}
	f.set(0)
	if err := write(); err != nil || ran != 2 {
		t.Fatalf("once the read answers again: %v (ran %d)", err, ran)
	}
	if ep, err := a.eng.epochNow(); err != nil || ep != 1 {
		t.Fatalf("the guard's read: %d %v", ep, err)
	}

	// a takeover over a read that fails once reads again: epoch 2, not 1
	a.eng.Shutdown(time.Second)
	b := fileAgent(t, path)
	b.eng.lockPath = ""
	var fb failingEpoch
	fb.install(b.eng)
	fb.set(1)
	b.eng.takeOver()
	t.Cleanup(func() { b.eng.Shutdown(time.Second) })
	b.eng.mu.Lock()
	ep, owned := b.eng.epoch, b.eng.owned
	b.eng.mu.Unlock()
	if !owned || ep != 2 || b.db.getSetting("engine_epoch") != "2" {
		t.Fatalf("the takeover: owned %v, epoch %d, stored %q — want 2", owned, ep, b.db.getSetting("engine_epoch"))
	}
}
