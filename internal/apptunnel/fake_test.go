package apptunnel

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// fakeRunner is the sidecar in memory: Start makes it "running" with a
// ticket; a device dials through dial, which admits tunnel/1 only from the
// allowlist and pair/1 only while pairing is open (the real sidecar's
// after-handshake hook), and hands xbind the stream over net.Pipe.
type fakeRunner struct {
	mu      sync.Mutex
	cfg     sidecar.Config
	running bool
	starts  int
	allow   map[string]bool
	pairing bool
	dropped []string
	ackErr  error
	streams chan sidecar.Stream
	serial  uint64
	pipes   map[string][]net.Conn // endpoint → xbind's ends (Drop closes them)
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{streams: make(chan sidecar.Stream), allow: map[string]bool{}, pipes: map[string][]net.Conn{}}
}

// Start begins a run that lasts until ctx ends (the supervisor's contract:
// a second Start restarts with the new config; Stop is for good).
func (f *fakeRunner) Start(ctx context.Context, cfg sidecar.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		go func() {
			<-ctx.Done()
			f.mu.Lock()
			f.running = false
			f.mu.Unlock()
		}()
	}
	f.cfg, f.running, f.pairing = cfg, true, cfg.Pairing
	f.starts++
	f.allow = map[string]bool{}
	for _, a := range cfg.Allow {
		f.allow[a] = true
	}
	return nil
}

func (f *fakeRunner) SetAllow(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		return f.ackErr
	}
	f.allow = map[string]bool{}
	for _, a := range ids {
		f.allow[a] = true
	}
	return nil
}

func (f *fakeRunner) SetPairing(open bool) { f.mu.Lock(); f.pairing = open; f.mu.Unlock() }

func (f *fakeRunner) Drop(ep string) {
	f.mu.Lock()
	f.dropped = append(f.dropped, ep)
	ps := f.pipes[ep]
	delete(f.pipes, ep)
	f.mu.Unlock()
	for _, p := range ps {
		p.Close()
	}
}

func (f *fakeRunner) SelfTest(context.Context) (wire.Event, error) {
	return wire.Event{T: "selftest", Reachable: true, Path: "relay", RTTMs: 42}, nil
}

func (f *fakeRunner) Streams() <-chan sidecar.Stream { return f.streams }

func (f *fakeRunner) Status() sidecar.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return sidecar.Status{State: sidecar.StateDisabled}
	}
	pub := f.cfg.Key.Public().(ed25519.PublicKey)
	return sidecar.Status{State: sidecar.StateRunning, Ticket: "endpointfake" + hex.EncodeToString(pub[:4]),
		EndpointID: hex.EncodeToString(pub), HomeRelay: "https://relay.example", RelayConnected: true, Confined: true, Exec: "memfd"}
}

func (f *fakeRunner) Stop() { f.mu.Lock(); f.running = false; f.mu.Unlock() }

var errRefused = errors.New("fake sidecar: refused by the after-handshake hook")

// dialer is a device's Dialer through this sidecar.
func (f *fakeRunner) dialer(d *devclient.Device) devclient.Dialer {
	return func(ctx context.Context, alpn string) (net.Conn, error) {
		ep := d.EndpointID()
		hexEP := hex.EncodeToString(ep[:])
		f.mu.Lock()
		ok := f.running && (alpn == wire.ALPNTunnelName && f.allow[hexEP] || alpn == wire.ALPNPairName && f.pairing)
		f.serial++
		h := wire.Header{Endpoint: ep, Conn: f.serial, ALPN: wire.ALPNTunnel}
		if alpn == wire.ALPNPairName {
			h.ALPN = wire.ALPNPair
		}
		dev, x := net.Pipe()
		if ok {
			f.pipes[hexEP] = append(f.pipes[hexEP], x)
		}
		f.mu.Unlock()
		if !ok {
			return nil, errRefused
		}
		select {
		case f.streams <- sidecar.Stream{Header: h, Conn: x}:
			return dev, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// rig is a workspace with the app tunnel: alice (admin), bob (user), the
// server's handler, the tunnel's HTTP server on the service's listener.
type rig struct {
	t      *testing.T
	svc    *Service
	fake   *fakeRunner
	srv    *server.Server
	h      http.Handler
	users  *users.Store
	a      *auth.Auth
	hub    *events.Hub
	cookie map[string]string // user → browser session
	dir    string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigRunner(t, nil, "")
}

// newRigRunner is newRig on a given runner (nil: the in-memory fake) and
// XBIN_IROH_RELAY.
func newRigRunner(t *testing.T, runner sidecar.Runner, envRelay string) *rig {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Name: "Alice", Role: users.RoleAdmin}, {ID: "bob", Name: "Bob"}} {
		if _, err := st.Upsert(u, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	hub := events.NewHub()
	srv := &server.Server{Auth: a, Hub: hub}
	a.SetClientIP(srv.ClientIP)
	srv.RegisterAPI("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		server.WriteJSON(w, http.StatusOK, map[string]any{"user": p.UserID, "via": p.Via, "device": p.DeviceID})
	})
	fake := newFakeRunner()
	if runner == nil {
		runner = fake
	}
	dir := t.TempDir()
	svc, err := New(Options{Dir: dir, Users: st, Auth: a, Hub: hub, Runner: runner, EnvRelay: envRelay,
		IsAdmin: func(p auth.Principal) bool { return p.IsAdmin() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	svc.Register(srv)
	r := &rig{t: t, svc: svc, fake: fake, srv: srv, h: srv.Handler(), users: st, a: a, hub: hub, dir: dir,
		cookie: map[string]string{"alice": a.NewSession("alice", "10.0.0.1"), "bob": a.NewSession("bob", "10.0.0.2")}}
	hs := &http.Server{Handler: r.h, ConnContext: ConnContext, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(svc.Listener()) }()
	t.Cleanup(func() { _ = hs.Close() })
	return r
}

// do sends a request straight to the handler (off the tunnel) as a cookie
// or bearer.
func (r *rig) do(method, path, body string, hdr map[string]string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (r *rig) as(user string) map[string]string {
	return map[string]string{"Cookie": auth.CookieName + "=" + r.cookie[user]}
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func (r *rig) enable() View {
	r.t.Helper()
	code, out := r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":true}`, r.as("alice"))
	if code != http.StatusOK || out["enabled"] != true || out["state"] != "running" {
		r.t.Fatalf("enable: %d %v", code, out)
	}
	return r.svc.view()
}

// mint is a tunnel enroll code for user.
func (r *rig) mint(user string) (devclient.Link, string) {
	r.t.Helper()
	code, out := r.do("POST", "/api/xbin/devices/enroll-code", `{"tunnel":true}`, r.as(user))
	if code != http.StatusOK {
		r.t.Fatalf("enroll-code: %d %v", code, out)
	}
	link, err := devclient.ParseLink(out["url"].(string))
	if err != nil || !link.Tunnel() {
		r.t.Fatalf("link %v: %v", out["url"], err)
	}
	return link, out["tunnel"].(map[string]any)["pairing"].(string)
}

// serverEP is xbind's EndpointId (from the view, as the ticket carries it).
func (r *rig) serverEP() [32]byte {
	var ep [32]byte
	b, _ := hex.DecodeString(r.svc.view().EndpointID)
	copy(ep[:], b)
	return ep
}

// waitState polls the pairing until it reaches state (as user's session).
func (r *rig) waitState(user, id, state string) map[string]any {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, out := r.do("GET", "/api/xbin/devices/pairing/"+id, "", r.as(user))
		if code == http.StatusOK && out["state"] == state {
			return out
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("pairing %s never reached %s: %d %v", id, state, code, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type pairResult struct {
	e   *devclient.Enrolled
	err error
	sas string
}

// startPair runs the phone's side in the background.
func (r *rig) startPair(d *devclient.Device, link devclient.Link) chan pairResult {
	ch := make(chan pairResult, 1)
	go func() {
		var sas string
		e, err := d.Pair(context.Background(), r.fake.dialer(d), link, devclient.PairOptions{ServerEndpoint: r.serverEP(),
			OnSAS: func(code, _ string) { sas = code }})
		ch <- pairResult{e, err, sas}
	}()
	return ch
}

// pair pairs a new device for user end to end and returns it with its id.
func (r *rig) pair(user, name string) (*devclient.Device, *devclient.Enrolled) {
	r.t.Helper()
	d, _ := devclient.New(name)
	link, id := r.mint(user)
	res := r.startPair(d, link)
	r.waitState(user, id, "confirm")
	if code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as(user)); code != http.StatusOK || out["state"] != "enrolled" {
		r.t.Fatalf("confirm: %d %v", code, out)
	}
	got := <-res
	if got.err != nil {
		r.t.Fatalf("pair: %v", got.err)
	}
	return d, got.e
}

func usersDevice(d *devclient.Device) users.Device {
	return users.Device{Name: d.Name, Platform: "ios", PublicKey: d.PublicKeyB64(), Origin: "http://example.com"}
}
