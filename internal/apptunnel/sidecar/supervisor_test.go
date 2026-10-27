//go:build linux

package sidecar

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar/fakeiroh"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// TestMain doubles as the sidecar: the supervisor runs this test binary
// as `xbin-iroh serve --fd N` (Options.Binary), which becomes fakeiroh —
// the real control protocol and stream handoff over loopback TCP. And, for
// the confined (integration) runs, as the sandbox's init.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "xbin-iroh" && len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(fakeiroh.Serve(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

func testKey(b byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	return ed25519.NewKeyFromSeed(seed)
}

func endpointOf(k ed25519.PrivateKey) string {
	return hex.EncodeToString(k.Public().(ed25519.PublicKey))
}

// fakeSupervisor runs this test binary as the sidecar, unconfined.
func fakeSupervisor(t *testing.T, env ...string) *Supervisor {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{RunDir: t.TempDir(), Workspace: t.TempDir(), Binary: self,
		noSandbox: true, minBackoff: 20 * time.Millisecond, extraEnv: env})
	t.Cleanup(s.Stop)
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitState(t *testing.T, s *Supervisor, want State) Status {
	t.Helper()
	var st Status
	waitFor(t, "state "+string(want), func() bool { st = s.Status(); return st.State == want })
	return st
}

func nextStream(t *testing.T, s *Supervisor) Stream {
	t.Helper()
	select {
	case st, ok := <-s.Streams():
		if !ok {
			t.Fatal("Streams closed")
		}
		return st
	case <-time.After(10 * time.Second):
		t.Fatal("no stream")
	}
	return Stream{}
}

func TestSupervisorServesStreams(t *testing.T) {
	s := fakeSupervisor(t)
	server, dev, stranger := testKey(1), testKey(2), testKey(3)
	ctx := context.Background()
	if err := s.Start(ctx, Config{Key: server, Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateRunning)
	if st.EndpointID != endpointOf(server) || !strings.HasPrefix(st.Ticket, fakeiroh.TicketPrefix) {
		t.Fatalf("status %+v: want the key's EndpointId and a ticket", st)
	}
	if st.Exec != "path" || st.Confined {
		t.Errorf("exec %q confined %v: want path, unconfined (the test seam)", st.Exec, st.Confined)
	}

	// not yet allowed
	if c, _, err := fakeiroh.Dial(st.Ticket, wire.ALPNTunnelName, endpointOf(dev)); err == nil {
		c.Close()
		t.Fatal("a device not on the allowlist got through")
	}
	if err := s.SetAllow(ctx, []string{endpointOf(dev)}); err != nil {
		t.Fatal(err)
	}
	c, serial, err := fakeiroh.Dial(st.Ticket, wire.ALPNTunnelName, endpointOf(dev))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := nextStream(t, s)
	defer got.Conn.Close()
	if got.Header.ALPN != wire.ALPNTunnel || got.Header.EndpointHex() != endpointOf(dev) || got.Header.Conn != serial {
		t.Fatalf("header %+v (serial %d)", got.Header, serial)
	}
	_ = got.Conn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(got.Conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("xbind read %q %v", buf, err)
	}
	if _, err := io.WriteString(got.Conn, "pong"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("device read %q %v", buf, err)
	}
	waitFor(t, "the conn in Status", func() bool { return len(s.Status().Conns) == 1 })

	// pairing: closed, then open
	if c2, _, err := fakeiroh.Dial(st.Ticket, wire.ALPNPairName, endpointOf(stranger)); err == nil {
		c2.Close()
		t.Fatal("pair/1 got through while pairing is closed")
	}
	s.SetPairing(true)
	waitFor(t, "pairing to open", func() bool {
		c2, _, err := fakeiroh.Dial(st.Ticket, wire.ALPNPairName, endpointOf(stranger))
		if err != nil {
			return false
		}
		defer c2.Close()
		p := nextStream(t, s)
		p.Conn.Close()
		return p.Header.ALPN == wire.ALPNPair
	})

	// Drop closes the device's connection
	s.Drop(endpointOf(dev))
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("the dropped device's connection is still open")
	}

	ev, err := s.SelfTest(ctx)
	if err != nil || ev.T != "selftest" || !ev.Reachable {
		t.Fatalf("selftest %+v %v", ev, err)
	}

	s.Stop()
	if _, ok := <-s.Streams(); ok {
		t.Fatal("Streams not closed after Stop")
	}
	if st := s.Status(); st.State != StateDisabled {
		t.Fatalf("after Stop: %s", st.State)
	}
}

// SetAllow returns only once the sidecar acknowledged the list (a device
// told "enrolled" connects at once), and gives up with ctx.
func TestSupervisorSetAllowWaitsForAck(t *testing.T) {
	s := fakeSupervisor(t, "FAKEIROH_ACK_DELAY=300ms")
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateRunning)
	t0 := time.Now()
	if err := s.SetAllow(context.Background(), []string{endpointOf(testKey(2))}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 250*time.Millisecond {
		t.Fatalf("SetAllow returned after %v, before the ack", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.SetAllow(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SetAllow with a short ctx: %v", err)
	}
}

// A sidecar that keeps dying is restarted with a growing delay, and its
// status says so meanwhile.
func TestSupervisorRestarts(t *testing.T) {
	s := fakeSupervisor(t, "FAKEIROH_EXIT=after-ready")
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "three restarts", func() bool { return s.Status().Restarts >= 3 })
	st := s.Status()
	if st.State != StateFailed && st.State != StateStarting && st.State != StateRunning {
		t.Fatalf("state %s", st.State)
	}
	waitFor(t, "a failed state with why", func() bool {
		st := s.Status()
		return st.State == StateFailed && strings.Contains(st.Error, "exited")
	})
}

func TestNextBackoff(t *testing.T) {
	d := time.Second
	var seq []time.Duration
	for range 8 {
		seq = append(seq, d)
		d = nextBackoff(d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i := range want {
		if seq[i] != want[i]*time.Second {
			t.Fatalf("backoff %v, want 1 s doubling to 60 s", seq)
		}
	}
}

// A second Start restarts the process with the new config.
func TestSupervisorSecondStartRestarts(t *testing.T) {
	s := fakeSupervisor(t)
	ctx := context.Background()
	if err := s.Start(ctx, Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	first := waitState(t, s, StateRunning).Ticket
	if err := s.Start(ctx, Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}, Allow: []string{endpointOf(testKey(2))}}); err != nil {
		t.Fatal(err)
	}
	var st Status
	waitFor(t, "a new process", func() bool {
		st = s.Status()
		return st.State == StateRunning && st.Ticket != first && st.Restarts == 1
	})
	c, _, err := fakeiroh.Dial(st.Ticket, wire.ALPNTunnelName, endpointOf(testKey(2)))
	if err != nil {
		t.Fatalf("the new config's device: %v", err)
	}
	c.Close()
}

// ctx ending stops the process; Start can run it again.
func TestSupervisorStartCtx(t *testing.T) {
	s := fakeSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateRunning)
	cancel()
	waitState(t, s, StateDisabled)
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateRunning)
}

// With isolation on and no sandbox, the sidecar never runs (D78).
func TestSupervisorIsolatedWithoutSandbox(t *testing.T) {
	self, _ := os.Executable()
	s := New(Options{Binary: self, Isolated: true, noSandbox: true, minBackoff: time.Hour})
	defer s.Stop()
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateFailed)
	if !strings.Contains(st.Error, "isolation is on") || st.EndpointID != "" {
		t.Fatalf("status %+v", st)
	}
}

func TestSupervisorUnavailable(t *testing.T) {
	t.Setenv("XBIN_IROH", "")
	if len(embedded) > 0 {
		t.Skip("built with the sidecar embedded")
	}
	s := New(Options{})
	defer s.Stop()
	if st := s.Status(); st.State != StateUnavailable {
		t.Fatalf("state %s", st.State)
	}
	if err := s.Start(context.Background(), Config{Key: testKey(1)}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start: %v", err)
	}
}

// A stream whose header is wrong is closed, never handed on.
func TestHandoffChecksTheHeader(t *testing.T) {
	s := New(Options{})
	defer s.Stop()
	a, b := net.Pipe()
	s.wg.Add(1)
	go s.handoff(a)
	go func() { _, _ = b.Write(make([]byte, wire.HeaderLen)) }() // no magic
	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := b.Read(make([]byte, 1)); err == nil {
		t.Fatal("a bad header was not closed")
	}
	a, b = net.Pipe()
	s.wg.Add(1)
	go s.handoff(a)
	h := wire.Header{ALPN: wire.ALPNPair, Conn: 9}
	go func() { _, _ = b.Write(h.Encode()) }()
	got := nextStream(t, s)
	if got.Header != h {
		t.Fatalf("header %+v", got.Header)
	}
	got.Conn.Close()
	b.Close()
}
