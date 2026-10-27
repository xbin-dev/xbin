//go:build linux && integration

package sidecar

// Run with: go test -tags=integration ./internal/apptunnel/sidecar/
// (the apptunnel-sidecar suite of hack/integration.jsonc). Needs
// unprivileged user namespaces; the real-sidecar test needs bin/xbin-iroh
// (make helpers HELPERS=iroh, or hack/build-xbin-iroh.sh) or XBIN_IROH_TEST.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar/fakeiroh"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

func needSandbox(t *testing.T) {
	t.Helper()
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
}

// The supervisor's own sandbox (Bare, host network, unprivileged, the
// binary passed open) around the fake: it runs confined, as PID 1 of its
// namespaces, and its streams reach xbind through Factory.Accept.
func TestConfinedFake(t *testing.T) {
	needSandbox(t)
	// static, like the real one: the bare root has no libc
	fake := filepath.Join(t.TempDir(), "fakeiroh")
	build := exec.Command("go", "build", "-o", fake, "../../../hack/fakeiroh")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeiroh: %v\n%s", err, out)
	}
	s := New(Options{RunDir: t.TempDir(), Workspace: t.TempDir(), Binary: fake, Isolated: true, minBackoff: 50 * time.Millisecond})
	defer s.Stop()
	dev := endpointOf(testKey(2))
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}, Allow: []string{dev}}); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateRunning)
	if !st.Confined || st.Restarts != 0 {
		t.Fatalf("status %+v: want confined, first try", st)
	}
	c, _, err := fakeiroh.Dial(st.Ticket, wire.ALPNTunnelName, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := nextStream(t, s)
	defer got.Conn.Close()
	if got.Header.EndpointHex() != dev {
		t.Fatalf("header %+v", got.Header)
	}
}

// The path a release takes: the embedded gzip extracted to a sealed memfd,
// exec'd in the sandbox through /proc/self/fd.
func TestConfinedMemfd(t *testing.T) {
	needSandbox(t)
	fake := filepath.Join(t.TempDir(), "fakeiroh")
	build := exec.Command("go", "build", "-o", fake, "../../../hack/fakeiroh")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeiroh: %v\n%s", err, out)
	}
	bin, _ := os.ReadFile(fake)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(bin)
	_ = zw.Close()
	old := embedded
	embedded = gz.Bytes()
	defer func() { embedded = old }()
	t.Setenv("XBIN_IROH", "")
	s := New(Options{RunDir: t.TempDir(), Workspace: t.TempDir(), Isolated: true, minBackoff: 50 * time.Millisecond})
	defer s.Stop()
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateRunning)
	if !st.Confined || st.Exec != "memfd" {
		t.Fatalf("status %+v: want confined, from a memfd", st)
	}
}

// realSidecar is bin/xbin-iroh (or XBIN_IROH_TEST).
func realSidecar(t *testing.T) string {
	t.Helper()
	p := os.Getenv("XBIN_IROH_TEST")
	if p == "" {
		p, _ = filepath.Abs("../../../bin/xbin-iroh")
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode()&0o111 == 0 {
		t.Skipf("no xbin-iroh at %s: make helpers HELPERS=iroh (or hack/build-xbin-iroh.sh), or set XBIN_IROH_TEST", p)
	}
	return p
}

// client runs `xbin-iroh client` as the device keyed by seed byte k and
// returns its local TCP address.
func client(t *testing.T, bin, ticket string, k byte) string {
	t.Helper()
	cmd := exec.Command(bin, "client", "--ticket", ticket, "--alpn", wire.ALPNTunnelName, "--listen", "tcp:127.0.0.1:0")
	in, _ := json.Marshal(map[string]any{"secretKey": hex.EncodeToString(testKey(k).Seed()), "relay": map[string]string{"mode": "disabled"}})
	cmd.Stdin = strings.NewReader(string(in) + "\n")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(out)
	if !sc.Scan() {
		t.Fatal("the client said nothing")
	}
	var l struct {
		T, EndpointID, Addr string
	}
	if err := json.Unmarshal(sc.Bytes(), &l); err != nil || l.T != "listening" {
		t.Fatalf("client: %s %v", sc.Text(), err)
	}
	if l.EndpointID != endpointOf(testKey(k)) {
		t.Fatalf("client endpoint %s, want %s", l.EndpointID, endpointOf(testKey(k)))
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	return l.Addr
}

func noStream(t *testing.T, s *Supervisor, d time.Duration) {
	t.Helper()
	select {
	case st := <-s.Streams():
		st.Conn.Close()
		t.Fatalf("a stream arrived: %+v", st.Header)
	case <-time.After(d):
	}
}

// The real xbin-iroh, as xbind runs it: confined by the supervisor, relay
// disabled (direct over loopback), dialed by `xbin-iroh client` with the
// ticket from ready. An allowed device's stream reaches xbind with the
// right header and bytes go both ways; a stranger never gets a stream;
// Drop cuts the device off; the selftest answers.
func TestConfinedRealSidecar(t *testing.T) {
	needSandbox(t)
	bin := realSidecar(t)
	s := New(Options{RunDir: t.TempDir(), Workspace: t.TempDir(), Binary: bin, Isolated: true, minBackoff: 50 * time.Millisecond})
	defer s.Stop()
	ctx := context.Background()
	if err := s.Start(ctx, Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateRunning)
	if !st.Confined || st.Exec != "path" || st.EndpointID != endpointOf(testKey(1)) || !strings.HasPrefix(st.Ticket, "endpoint") {
		t.Fatalf("status %+v", st)
	}
	t.Logf("xbin-iroh %s ready: %s", st.Version, st.Ticket)
	dev := endpointOf(testKey(2))
	if err := s.SetAllow(ctx, []string{dev}); err != nil {
		t.Fatal(err)
	}

	devAddr := client(t, bin, st.Ticket, 2)
	c, err := net.Dial("tcp", devAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(c, "hello xbind"); err != nil { // QUIC announces a stream with its first bytes
		t.Fatal(err)
	}
	got := nextStream(t, s)
	defer got.Conn.Close()
	if got.Header.ALPN != wire.ALPNTunnel || got.Header.EndpointHex() != dev || got.Header.Conn == 0 {
		t.Fatalf("header %+v", got.Header)
	}
	_ = got.Conn.SetDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, len("hello xbind"))
	if _, err := io.ReadFull(got.Conn, buf); err != nil || string(buf) != "hello xbind" {
		t.Fatalf("xbind read %q %v", buf, err)
	}
	if _, err := io.WriteString(got.Conn, "hello phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello phone" {
		t.Fatalf("device read %q %v", buf, err)
	}
	waitFor(t, "the connection in Status", func() bool {
		for _, ci := range s.Status().Conns {
			if ci.Endpoint == dev {
				return true
			}
		}
		return false
	})

	// a stranger: refused by the hook, nothing reaches xbind
	sc, err := net.Dial("tcp", client(t, bin, st.Ticket, 3))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(sc, "let me in")
	_ = sc.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := sc.Read(make([]byte, 1)); err == nil {
		t.Fatal("the stranger's stream got an answer")
	}
	sc.Close()
	noStream(t, s, time.Second)

	// Drop: the device's connection is closed
	s.Drop(dev)
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("the dropped device's stream still answers")
	}
	waitFor(t, "the connection gone from Status", func() bool { return len(s.Status().Conns) == 0 })

	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ev, err := s.SelfTest(sctx)
	if err != nil || !ev.Reachable {
		t.Fatalf("selftest %+v %v", ev, err)
	}

	s.Stop()
	if st := s.Status(); st.State != StateDisabled {
		t.Fatalf("after Stop: %s", st.State)
	}
}
