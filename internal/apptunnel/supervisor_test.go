package apptunnel

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar/fakeiroh"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// This test binary doubles as the sidecar (fakeiroh: xbin-iroh's control
// protocol and stream handoff over loopback TCP instead of iroh), as
// internal/apptunnel/sidecar's tests do, and as the sandbox's init.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "xbin-iroh" && len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(fakeiroh.Serve(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

// The service on the real supervisor (its process, factory handoff and
// control protocol), the sidecar being fakeiroh: pairing, IK, device login
// over the tunnel, revocation.
func TestWithSupervisor(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sup := sidecar.New(sidecar.Options{RunDir: t.TempDir(), Workspace: t.TempDir(), Binary: self})
	r := newRigRunner(t, sup, "disabled")
	code, out := r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":true}`, r.as("alice"))
	if code != http.StatusOK || out["enabled"] != true {
		t.Fatalf("enable: %d %v", code, out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for sup.Status().State != sidecar.StateRunning || sup.Status().Ticket == "" {
		if time.Now().After(deadline) {
			t.Fatalf("the sidecar never ran: %+v", sup.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := sup.Status(); st.EndpointID != r.svc.view().EndpointID {
		t.Fatalf("the sidecar's EndpointId %s isn't xbind's %s", st.EndpointID, r.svc.view().EndpointID)
	}
	d, _ := devclient.New("phone")
	link, id := r.mint("bob")
	dial := func(_ context.Context, alpn string) (net.Conn, error) {
		c, _, err := fakeiroh.Dial(link.Ticket, alpn, d.EndpointHex())
		return c, err
	}
	res := make(chan pairResult, 1)
	go func() {
		e, err := d.Pair(context.Background(), dial, link, devclient.PairOptions{ServerEndpoint: r.serverEP()})
		res <- pairResult{e: e, err: err}
	}()
	r.waitState("bob", id, "confirm")
	if code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob")); code != 200 || out["state"] != "enrolled" {
		t.Fatalf("confirm: %d %v", code, out)
	}
	got := <-res
	if got.err != nil {
		t.Fatalf("pair: %v", got.err)
	}
	var sk [32]byte
	copy(sk[:], link.ServerKey[:])
	tun := d.Tunnel(dial, r.serverEP(), sk)
	sess, err := d.Login(context.Background(), tun.Client(), loopback, got.e.Origin, got.e.DeviceID)
	if err != nil {
		t.Fatalf("device login through the sidecar: %v", err)
	}
	var probe map[string]any
	if err := devclient.Do(context.Background(), tun.Client(), "GET", loopback+"/api/xbin/probe", sess.Token, nil, &probe); err != nil || probe["device"] != got.e.DeviceID {
		t.Fatalf("probe: %v %v", probe, err)
	}
	// revoked: the sidecar refuses its next connection
	r.users.RemoveDevice(got.e.DeviceID)
	r.svc.DeviceRemoved("bob", got.e.DeviceID)
	if _, err := tun.DialConn(context.Background()); err == nil {
		t.Fatal("a revoked device connected through the sidecar")
	}
	// off and on again: a new run
	r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":false}`, r.as("alice"))
	if st := sup.Status().State; st == sidecar.StateRunning {
		t.Fatalf("still running after off: %s", st)
	}
	r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":true}`, r.as("alice"))
	for sup.Status().State != sidecar.StateRunning {
		if time.Now().After(deadline.Add(20 * time.Second)) {
			t.Fatalf("the sidecar never ran again: %+v", sup.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
