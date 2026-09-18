package main

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// silentGateway accepts connections on a unix socket and never answers — a
// gateway that is up but wedged, which is what a restart looks like from
// inside the sandbox for a while. The SDK client has no Timeout, so every call
// the agent makes on it must bring its own.
func silentGateway(t *testing.T) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // hold it open, say nothing
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	t.Setenv("XBIN_GATEWAY", sock)
}

func shorten(t *testing.T, d *time.Duration, to time.Duration) {
	old := *d
	*d = to
	t.Cleanup(func() { *d = old })
}

func within(t *testing.T, what string, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s still blocked after %v on a silent gateway", what, limit)
	}
}

// TestGatewayCallsGiveUp — the model lookup runs after a run is marked running
// and before any chat request, and the heartbeat calls hold reconcileBeat's
// single-flight slot. Either one hanging reads as "thinking forever".
func TestGatewayCallsGiveUp(t *testing.T) {
	silentGateway(t)
	shorten(t, &beatCallTimeout, 150*time.Millisecond)
	shorten(t, &modelLookupTimeout, 150*time.Millisecond)
	ag := newTestAgent(t, newTestDB(t))

	within(t, "stopBeat", 2*time.Second, func() {
		if ag.stopBeat() {
			t.Error("stopBeat reported success from a gateway that never answered")
		}
	})
	within(t, "putBeat", 2*time.Second, func() {
		if ag.putBeat([]byte(`{}`)) {
			t.Error("putBeat reported success from a gateway that never answered")
		}
	})
	within(t, "preferredModel", 2*time.Second, func() {
		if m := preferredModel(context.Background(), "agent-test-silent"); m != "" {
			t.Errorf("preferredModel = %q from a silent gateway", m)
		}
	})
}

// TestReconcileBeatIsSingleFlight — reconcileBeat is reached from every
// completing drive; while one registration is retrying, the others must not
// queue up behind it making their own round-trips.
func TestReconcileBeatIsSingleFlight(t *testing.T) {
	silentGateway(t)
	shorten(t, &beatCallTimeout, 150*time.Millisecond)
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("sleeper", "", 0)
	_ = db.setStatus(id, statusSleep, now()+60, "", "") // something needs the beat

	first := make(chan struct{})
	go func() { ag.reconcileBeat(); close(first) }()
	time.Sleep(50 * time.Millisecond) // let it take the slot
	within(t, "a second reconcileBeat", 100*time.Millisecond, ag.reconcileBeat)
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("the first reconcile never finished")
	}
	ag.mu.Lock()
	on := ag.beatOn
	ag.mu.Unlock()
	if on {
		t.Fatal("beatOn set although no registration succeeded — the keeper would never retry")
	}
}
