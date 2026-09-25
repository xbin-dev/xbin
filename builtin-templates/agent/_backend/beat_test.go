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

// TestGatewayCallsGiveUp — the model lookup runs before a turn's first call,
// and the cron calls run from shutdown (which has a hard deadline). Either one
// hanging on a wedged gateway would read as "thinking forever" or lose the
// handoff.
func TestGatewayCallsGiveUp(t *testing.T) {
	silentGateway(t)
	shorten(t, &cronCallTimeout, 150*time.Millisecond)
	shorten(t, &modelLookupTimeout, 150*time.Millisecond)
	ag := newTestAgent(t, newTestDB(t))

	within(t, "cronPut", 2*time.Second, func() {
		if ag.cronPut(map[string]any{"name": "x"}) {
			t.Error("cronPut reported success from a gateway that never answered")
		}
	})
	within(t, "cronDelete", 2*time.Second, func() { ag.cronDelete("x") })
	within(t, "preferredModel", 2*time.Second, func() {
		if m := preferredModel(context.Background(), "agent-test-silent"); m != "" {
			t.Errorf("preferredModel = %q from a silent gateway", m)
		}
	})
}
