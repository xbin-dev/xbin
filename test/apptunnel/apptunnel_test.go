//go:build linux && integration

// Package apptunnel drives the xbin app's iroh tunnel end to end (D204,
// native/spec/tunnel.md, docs/app-tunnel.md): an `xbind --isolate` built
// from this tree (test/xbindtest) running the real xbin-iroh sidecar in its
// sandbox, and phones made of devclient — the Go reference of the app's
// side: the link, Noise, pairing, device login — over `xbin-iroh client`,
// a real iroh endpoint with a local listener per ALPN, the way the app's
// loopback forwarder works.
//
//   - TestTunnel: relays off (XBIN_IROH_RELAY=disabled, direct over
//     loopback). Tunnels on through the API, a tunnel code, pairing with the
//     code read on both screens, IK and device login over tunnel/1, HTTP
//     and /ws/events through it, the bound session refused off it, "They
//     don't" burning a code, replacing a device at the limit (its tunnel
//     closes), iroh-only refusing a classic device's bearer, the app's
//     password sign-in and plain enrollment, a revoked device's tunnel
//     closing.
//   - TestTunnelCustomRelay: a real iroh-relay (--dev, plain HTTP on
//     loopback) with an access token, set as the workspace's custom relay
//     through the admin routes: the sidecar's home relay, the selftest
//     through it, pairing and the tunnel through it with the token from the
//     QR code; the same phone with a wrong token is turned away by the
//     relay, and gets through again with the right one.
//   - TestTunnelEmbedded: an xbind built with the sidecar embedded, as a
//     release is (-tags xbin_iroh): it runs from a sealed memfd, confined,
//     and a phone pairs and signs in through it.
//
// Needs unprivileged user namespaces and the rootfs (xbindtest.Require),
// and bin/xbin-iroh (hack/build-xbin-iroh.sh bin, or make helpers
// HELPERS=iroh; XBIN_IROH_TEST names another): every test skips saying
// so without it. TestTunnelCustomRelay also needs iroh-relay 1.4.x
// (XBIN_IROH_RELAY_TEST, else bin/iroh-relay, else PATH: iroh's GitHub
// releases, iroh-relay-v1.4.1-x86_64-unknown-linux-musl).
//
//	go test -tags=integration -count=1 ./test/apptunnel/
//
// On a dev box run it with the Bash sandbox disabled.
package apptunnel

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

const (
	pwBob   = "bob-password-123"
	pwCarol = "carol-password-123"
	wait    = 30 * time.Second
)

// ---- the binaries ----

func repoBin(name string) string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..", "bin", name)
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// irohBin is the real sidecar: XBIN_IROH_TEST, else bin/xbin-iroh.
func irohBin(t *testing.T) string {
	t.Helper()
	p := os.Getenv("XBIN_IROH_TEST")
	if p == "" {
		p = repoBin("xbin-iroh")
	}
	p, _ = filepath.Abs(p)
	if !executable(p) {
		t.Skipf("no xbin-iroh at %s: hack/build-xbin-iroh.sh bin (or make helpers HELPERS=iroh), or set XBIN_IROH_TEST", p)
	}
	return p
}

// relayBin is iroh-relay: XBIN_IROH_RELAY_TEST, else bin/iroh-relay, else
// PATH's.
func relayBin(t *testing.T) string {
	t.Helper()
	p := os.Getenv("XBIN_IROH_RELAY_TEST")
	if p == "" {
		if p = repoBin("iroh-relay"); !executable(p) {
			p, _ = exec.LookPath("iroh-relay")
		}
	}
	if p == "" || !executable(p) {
		t.Skip("no iroh-relay (XBIN_IROH_RELAY_TEST, bin/iroh-relay or PATH): iroh's GitHub releases, iroh-relay-v1.4.1-x86_64-unknown-linux-musl")
	}
	return p
}

// logs collects a test's helper processes' output, printed when it fails.
type logs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }

func newLogs(t *testing.T, what string) *logs {
	l := &logs{}
	t.Cleanup(func() {
		if t.Failed() {
			l.mu.Lock()
			t.Logf("%s:\n%s", what, l.b.String())
			l.mu.Unlock()
		}
	})
	return l
}

// ---- the rig: an xbind with tunnels, people, phones ----

type rig struct {
	t    *testing.T
	d    *xbindtest.Daemon
	iroh string // the client binary phones run
	log  *logs
}

// start boots an xbind (owner auth on) running the sidecar env names, with
// bob and carol.
func start(t *testing.T, a *xbindtest.Assets, iroh string, env ...string) *rig {
	t.Helper()
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Env: env})
	r := &rig{t: t, d: d, iroh: iroh, log: newLogs(t, "the phones' iroh clients")}
	d.AddUser(t, "bob", pwBob, "user", nil)
	d.AddUser(t, "carol", pwCarol, "user", nil)
	return r
}

func bearer(tok string) xbindtest.Header { return xbindtest.H("Authorization", "Bearer "+tok) }

// view is GET /app-tunnel.
type view struct {
	Enabled, TunnelOnly, Confined, RelayConnected bool
	State, Exec, Error, EndpointID, HomeRelay     string
	Relay                                         struct{ Mode, URL, TokenHint string }
	Devices                                       struct{ Total, TunnelPaired, Classic, Connected, Synced int }
	Conns                                         []struct {
		Endpoint, Path string
		Conn           uint64
		RttMs          int
	}
	LockedOut *int
}

func (r *rig) view() view {
	r.t.Helper()
	var v view
	r.d.Must(r.t, "GET", "/api/xbin/app-tunnel", nil, 200).Decode(r.t, &v)
	return v
}

// put is PUT /app-tunnel as the owner.
func (r *rig) put(body any) view {
	r.t.Helper()
	var v view
	r.d.Must(r.t, "PUT", "/api/xbin/app-tunnel", body, 200).Decode(r.t, &v)
	return v
}

// waitRunning waits for the sidecar to run (and cond, if any) and returns
// the view.
func (r *rig) waitRunning(cond func(view) bool) view {
	r.t.Helper()
	var v view
	xbindtest.Eventually(r.t, wait, "the tunnel running", func() (bool, string) {
		v = r.view()
		return v.State == "running" && v.EndpointID != "" && (cond == nil || cond(v)), fmt.Sprintf("%+v", v)
	})
	return v
}

// pairing is one phone's pair/1 under way.
type pairing struct {
	r    *rig
	who  string // the minting login's bearer
	id   string
	dev  *devclient.Device
	link devclient.Link
	tk   devclient.Ticket
	cl   *devclient.IrohClient
	sas  chan string
	res  chan pairResult
}

type pairResult struct {
	e   *devclient.Enrolled
	err error
}

// mint is POST /devices/enroll-code {tunnel:true} as the login who.
func (r *rig) mint(who string) (devclient.Link, string) {
	r.t.Helper()
	var m struct {
		URL    string
		Tunnel struct{ Pairing, Ticket, ServerKey string }
	}
	r.d.Must(r.t, "POST", "/api/xbin/devices/enroll-code", map[string]bool{"tunnel": true}, 200, bearer(who)).Decode(r.t, &m)
	link, err := devclient.ParseLink(m.URL)
	if err != nil || !link.Tunnel() || m.Tunnel.Pairing == "" || m.Tunnel.Ticket != link.Ticket {
		r.t.Fatalf("a tunnel code: %v %+v", err, m)
	}
	return link, m.Tunnel.Pairing
}

// startPairing mints a tunnel code as who, and a new phone named name
// starts pairing with it through its own iroh endpoint (relay: the link's,
// or override's).
func (r *rig) startPairing(who, name string, override *devclient.IrohRelay) *pairing {
	r.t.Helper()
	link, id := r.mint(who)
	dev, err := devclient.New(name)
	if err != nil {
		r.t.Fatal(err)
	}
	p := &pairing{r: r, who: who, id: id, dev: dev, link: link, sas: make(chan string, 1), res: make(chan pairResult, 1)}
	p.pairWith(link, override)
	return p
}

// pairWith runs pair/1 with link from p's phone, in the background.
func (p *pairing) pairWith(link devclient.Link, override *devclient.IrohRelay) {
	r := p.r
	r.t.Helper()
	tk, err := devclient.ParseTicket(link.Ticket)
	if err != nil {
		r.t.Fatalf("the link's ticket: %v", err)
	}
	p.tk = tk
	r.t.Logf("%s's ticket %s: %+v", p.dev.Name, link.Ticket, tk)
	relay := override
	if relay == nil {
		if relay, err = devclient.RelayFor(link); err != nil {
			r.t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if p.cl, err = devclient.StartIrohClient(ctx, r.iroh, link.Ticket, wire.ALPNPairName, p.dev, relay, r.log); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(p.cl.Close)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*wait)
		defer cancel()
		e, err := p.dev.Pair(ctx, devclient.ClientDialer(p.cl, nil), link, devclient.PairOptions{ServerEndpoint: tk.Endpoint,
			OnSAS: func(code, _ string) { p.sas <- code }})
		p.res <- pairResult{e, err}
	}()
}

// get is GET /devices/pairing/{id} as the minting login.
func (p *pairing) get() map[string]any {
	p.r.t.Helper()
	var v map[string]any
	p.r.d.Must(p.r.t, "GET", "/api/xbin/devices/pairing/"+p.id, nil, 200, bearer(p.who)).Decode(p.r.t, &v)
	return v
}

// awaitConfirm waits for both screens: the phone's code, and xbind's
// pairing in confirm with the same code and the phone's name.
func (p *pairing) awaitConfirm() map[string]any {
	t := p.r.t
	t.Helper()
	var phone string
	select {
	case phone = <-p.sas:
	case res := <-p.res:
		t.Fatalf("pairing ended before the code: %+v %v", res.e, res.err)
	case <-time.After(wait):
		t.Fatalf("the phone never showed a code (xbind: %v)", p.get())
	}
	var v map[string]any
	xbindtest.Eventually(t, wait, "the pairing in confirm", func() (bool, string) {
		v = p.get()
		return v["state"] == "confirm", fmt.Sprint(v)
	})
	if len(phone) != 7 || phone[3] != ' ' || strings.ReplaceAll(phone, " ", "") != v["sas"] {
		t.Fatalf("the codes differ: the phone shows %q, the computer %v", phone, v["sas"])
	}
	if dev, _ := v["device"].(map[string]any); dev["name"] != p.dev.Name || dev["platform"] != "ios" {
		t.Fatalf("the computer shows the phone as %v", v["device"])
	}
	return v
}

// confirm is POST /devices/pairing/{id}/confirm as the minting login.
func (p *pairing) confirm(body any, want int) map[string]any {
	p.r.t.Helper()
	var out map[string]any
	p.r.d.Must(p.r.t, "POST", "/api/xbin/devices/pairing/"+p.id+"/confirm", body, want, bearer(p.who)).Decode(p.r.t, &out)
	return out
}

// result waits for the phone's end of pairing; its pair/1 endpoint goes.
func (p *pairing) result() pairResult {
	p.r.t.Helper()
	select {
	case res := <-p.res:
		p.cl.Close()
		return res
	case <-time.After(2 * wait):
		p.r.t.Fatal("the phone never heard how pairing ended")
	}
	return pairResult{}
}

// enroll pairs a phone as who end to end: both screens, Codes match.
func (r *rig) enroll(who, name string) (*pairing, *devclient.Enrolled) {
	r.t.Helper()
	p := r.startPairing(who, name, nil)
	p.awaitConfirm()
	out := p.confirm(map[string]bool{"match": true}, 200)
	res := p.result()
	if res.err != nil || out["state"] != "enrolled" || res.e.DeviceID != out["deviceId"] {
		r.t.Fatalf("enrolled: xbind %v, the phone %+v %v", out, res.e, res.err)
	}
	return p, res.e
}

// phone is a paired phone on tunnel/1.
type phone struct {
	r    *rig
	dev  *devclient.Device
	e    *devclient.Enrolled
	cl   *devclient.IrohClient
	tun  *devclient.Tunnel
	http *http.Client
	base string // the forwarder's loopback origin, as the app's requests name it
	sess *devclient.Session
}

// connect starts p's tunnel endpoint the way the app does after
// "enrolled" — its ticket and relay — then IK and device login.
func (r *rig) connect(p *pairing, e *devclient.Enrolled, relay *devclient.IrohRelay) *phone {
	r.t.Helper()
	if relay == nil && e.Tunnel.Relay.URL != "" {
		relay = &devclient.IrohRelay{Mode: "custom", URL: e.Tunnel.Relay.URL, Token: e.Tunnel.Relay.Token}
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cl, err := devclient.StartIrohClient(ctx, r.iroh, e.Tunnel.Ticket, wire.ALPNTunnelName, p.dev, relay, r.log)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(cl.Close)
	tk, err := devclient.ParseTicket(e.Tunnel.Ticket)
	if err != nil {
		r.t.Fatal(err)
	}
	ph := &phone{r: r, dev: p.dev, e: e, cl: cl, tun: p.dev.Tunnel(devclient.ClientDialer(nil, cl), tk.Endpoint, p.link.ServerKey),
		base: "http://" + cl.Addr}
	ph.http = ph.tun.Client()
	if ph.sess, err = p.dev.Login(ctx, ph.http, ph.base, e.Origin, e.DeviceID); err != nil {
		r.t.Fatalf("device login over the tunnel: %v\n%s", err, cl.Errors())
	}
	return ph
}

// do is one request over the tunnel with the phone's session.
func (ph *phone) do(method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return devclient.Do(ctx, ph.http, method, ph.base+path, ph.sess.Token, body, out)
}

// current is the device GET /devices marks as the caller's, over the tunnel.
func (ph *phone) current() string {
	ph.r.t.Helper()
	var l struct {
		Devices []struct {
			ID      string
			Current bool
			Tunnel  bool
		}
	}
	if err := ph.do("GET", "/api/xbin/devices", nil, &l); err != nil {
		ph.r.t.Fatalf("GET /devices over the tunnel: %v", err)
	}
	for _, d := range l.Devices {
		if d.Current {
			if !d.Tunnel {
				ph.r.t.Fatalf("the device isn't marked tunnel-paired: %+v", l)
			}
			return d.ID
		}
	}
	return ""
}

// closes reports a connection closed by the far side within the wait (not
// a timeout).
func closes(c net.Conn) error {
	_ = c.SetReadDeadline(time.Now().Add(wait))
	_, err := c.Read(make([]byte, 1))
	var ne net.Error
	if err == nil {
		return errors.New("it answered")
	}
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("still open after %s", wait)
	}
	return nil
}

// tunnelRefusal is a devclient error's status and tunnel refusal.
func tunnelRefusal(err error) string {
	var he *devclient.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("%d %s", he.Status, he.Tunnel())
	}
	return fmt.Sprint(err)
}

// respTunnel is an answer's status and tunnel refusal ("401 bound").
func respTunnel(r xbindtest.Resp) string {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	s, _ := m["tunnel"].(string)
	return fmt.Sprintf("%d %s", r.Status, s)
}

// ---- the tests ----

// The whole path with relays off: the tunnel's ticket names the sidecar's
// loopback sockets.
func TestTunnel(t *testing.T) {
	bin := irohBin(t)
	a := xbindtest.Require(t)
	r := start(t, a, bin, "XBIN_IROH="+bin, "XBIN_IROH_RELAY=disabled")
	d := r.d

	// tunnels on: the sidecar runs confined (the bare sandbox: xbind runs
	// --isolate), XBIN_IROH's path; the sign-in methods say so
	if v := r.view(); v.Enabled || v.State != "disabled" {
		t.Fatalf("a fresh workspace's tunnel: %+v", v)
	}
	r.put(map[string]bool{"enabled": true})
	v := r.waitRunning(nil)
	if !v.Confined || v.Exec != "path" {
		t.Fatalf("the sidecar isn't confined from its path: %+v", v)
	}
	var methods struct {
		Tunnel struct{ Enabled, Required bool }
	}
	d.Must(t, "GET", "/api/xbin/login/methods", nil, 200).Decode(t, &methods)
	if !methods.Tunnel.Enabled || methods.Tunnel.Required {
		t.Fatalf("login methods: %+v", methods)
	}

	// carol's classic device (before iroh-only): enrolled and signed in
	// over the web address
	carol := d.Login(t, "carol", pwCarol)
	var code struct{ Code, URL, Origin string }
	d.Must(t, "POST", "/api/xbin/devices/enroll-code", map[string]any{}, 200, bearer(carol)).Decode(t, &code)
	if strings.Contains(code.URL, "tv=") {
		t.Fatalf("a classic code got a tunnel link: %s", code.URL)
	}
	old, _ := devclient.New("Carol's old phone")
	var en struct{ DeviceID string }
	d.Must(t, "POST", "/api/xbin/devices/enroll", map[string]string{"code": code.Code, "name": old.Name, "platform": "ios", "publicKey": old.PublicKeyB64()}, 200).Decode(t, &en)
	ctx := context.Background()
	carolDev, err := old.Login(ctx, http.DefaultClient, d.URL, code.Origin, en.DeviceID)
	if err != nil {
		t.Fatalf("carol's device login: %v", err)
	}
	d.Must(t, "GET", "/api/xbin/devices", nil, 200, bearer(carolDev.Token))

	// bob pairs a phone: a tunnel code (its link, never over
	// /devices/enroll), the code read on both screens, Codes match
	bob := d.Login(t, "bob", pwBob)
	p1 := r.startPairing(bob, "Bob's iPhone", nil)
	if p1.link.Origin != d.URL || p1.link.Required || p1.link.RelayToken != "" || len(p1.tk.Addrs) == 0 || len(p1.tk.Relays) != 0 {
		t.Fatalf("the link: %+v, its ticket %+v", p1.link, p1.tk)
	}
	if got := fmt.Sprintf("%x", p1.tk.Endpoint); got != v.EndpointID {
		t.Fatalf("the ticket's EndpointId %s isn't the tunnel's %s", got, v.EndpointID)
	}
	pv := p1.awaitConfirm()
	if pv["atLimit"] != false {
		t.Fatalf("bob has room for one: %v", pv)
	}
	if rr := d.Call(t, "POST", "/api/xbin/devices/enroll", map[string]string{"code": p1.link.Code, "name": "x", "platform": "ios", "publicKey": p1.dev.PublicKeyB64()}); rr.Status != 401 {
		t.Fatalf("a tunnel code over /devices/enroll: %d %s", rr.Status, rr)
	}
	out := p1.confirm(map[string]bool{"match": true}, 200)
	res := p1.result()
	if res.err != nil || out["state"] != "enrolled" || res.e.DeviceID != out["deviceId"] || res.e.User.ID != "bob" ||
		res.e.Origin != d.URL || res.e.Tunnel.Ticket == "" || res.e.Tunnel.Required {
		t.Fatalf("enrolled: xbind %v, the phone %+v %v", out, res.e, res.err)
	}
	e1 := res.e

	// IK on tunnel/1, device login over it
	ph1 := r.connect(p1, e1, nil)
	if id := ph1.current(); id != e1.DeviceID {
		t.Fatalf("over the tunnel, GET /devices's current is %q, not %s", id, e1.DeviceID)
	}
	var cc devclient.ClientConfig
	if err := ph1.do("GET", "/api/xbin/app-tunnel/client", nil, &cc); err != nil || cc.EndpointID != v.EndpointID ||
		cc.ServerKey != base64.RawURLEncoding.EncodeToString(p1.link.ServerKey[:]) || cc.Ticket != e1.Tunnel.Ticket || cc.Required {
		t.Fatalf("GET /app-tunnel/client over the tunnel: %+v %v", cc, err)
	}
	// the admin view: the phone counted connected and synced, its iroh
	// connection listed (with the pair/1 one, until it idles out: the
	// phone's pairing endpoint went without closing it), every one from
	// the phone, a path named
	xbindtest.Eventually(t, wait, "the admin view counting the connected phone", func() (bool, string) {
		v := r.view()
		ok := v.Devices.TunnelPaired == 1 && v.Devices.Connected == 1 && v.Devices.Classic == 1 && v.Devices.Synced == 1 && len(v.Conns) > 0
		for _, c := range v.Conns {
			ok = ok && c.Endpoint == p1.dev.EndpointHex() && c.Path == "direct"
		}
		return ok, fmt.Sprintf("%+v", v)
	})

	// a WebSocket through the tunnel: /ws/events hears an event a direct
	// request caused
	wd := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ph1.tun.DialConn(ctx) },
		HandshakeTimeout: wait}
	ws, resp, err := wd.Dial("ws://"+ph1.cl.Addr+"/ws/events", http.Header{"Authorization": {"Bearer " + ph1.sess.Token}})
	if err != nil {
		t.Fatalf("/ws/events over the tunnel: %v %+v", err, resp)
	}
	defer ws.Close()
	r.put(map[string]bool{"enabled": true}) // unchanged: no restart, an app-tunnel event
	_ = ws.SetReadDeadline(time.Now().Add(wait))
	for {
		var ev struct{ Type string }
		if err := ws.ReadJSON(&ev); err != nil {
			t.Fatalf("no app-tunnel event over the tunnel's socket: %v", err)
		}
		if ev.Type == "app-tunnel" {
			break
		}
	}

	// the session is bound to the tunnel; nobody else's credential works
	// on it
	if rr := d.Call(t, "GET", "/api/xbin/devices", nil, bearer(ph1.sess.Token)); respTunnel(rr) != "401 bound" {
		t.Fatalf("the tunnel's session over the web address: %d %s", rr.Status, rr)
	}
	ctx30, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := devclient.Do(ctx30, ph1.http, "GET", ph1.base+"/api/xbin/devices", bob, nil, nil); tunnelRefusal(err) != "403 device" {
		t.Fatalf("bob's password session over his phone's tunnel: %v", err)
	}
	if err := devclient.Do(ctx30, ph1.http, "GET", ph1.base+"/api/xbin/devices", d.Token(), nil, nil); tunnelRefusal(err) != "403 device" {
		t.Fatalf("the owner token over a phone's tunnel: %v", err)
	}
	if err := devclient.Do(ctx30, ph1.http, "POST", ph1.base+"/api/xbin/login", "", map[string]string{"username": "bob", "password": pwBob}, nil); tunnelRefusal(err) != "403 device" {
		t.Fatalf("a password sign-in over the tunnel: %v", err)
	}

	// "They don't" burns the code: the phone hears mismatch, and the
	// same link never pairs again (another pairing open meanwhile, so pair/1
	// is admitted and the refusal is the code's)
	p2 := r.startPairing(bob, "Not my phone", nil)
	if pv := p2.awaitConfirm(); pv["atLimit"] != true || pv["limit"] != 1.0 || pv["count"] != 1.0 {
		t.Fatalf("bob at his limit: %v", pv)
	}
	if out := p2.confirm(map[string]bool{"match": false}, 200); out["state"] != "rejected" || out["reason"] != "mismatch" {
		t.Fatalf("They don't: %v", out)
	}
	var rej *devclient.Rejected
	if res := p2.result(); !errors.As(res.err, &rej) || rej.Reason != "mismatch" {
		t.Fatalf("the phone after They don't: %+v %v", res.e, res.err)
	}
	if v := p2.get(); v["state"] != "rejected" || v["reason"] != "mismatch" {
		t.Fatalf("the pairing after They don't: %v", v)
	}
	p3 := r.startPairing(bob, "Bob's new iPhone", nil)
	again := &pairing{r: r, who: bob, id: p2.id, dev: p2.dev, sas: make(chan string, 1), res: make(chan pairResult, 1)}
	again.pairWith(p2.link, nil)
	if res := again.result(); res.err == nil {
		t.Fatalf("a burnt code paired again: %+v", res.e)
	}

	// at the limit, the confirmation replaces a device: without
	// replace 409 {limit, count} and the pairing waits; with it, enrolled
	// and the old phone's tunnel closes
	conn1, err := ph1.tun.DialConn(ctx30)
	if err != nil {
		t.Fatalf("a tunnel connection: %v", err)
	}
	defer conn1.Close()
	p3.awaitConfirm()
	if out := p3.confirm(map[string]bool{"match": true}, 409); out["limit"] != 1.0 || out["count"] != 1.0 {
		t.Fatalf("Codes match at the limit without replace: %v", out)
	}
	if v := p3.get(); v["state"] != "confirm" {
		t.Fatalf("the pairing after a 409: %v", v)
	}
	out = p3.confirm(map[string]any{"match": true, "replace": e1.DeviceID}, 200)
	res = p3.result()
	if res.err != nil || out["state"] != "enrolled" || out["replaced"] != e1.DeviceID || res.e.DeviceID != out["deviceId"] {
		t.Fatalf("replace at the limit: xbind %v, the phone %+v %v", out, res.e, res.err)
	}
	if err := closes(conn1); err != nil {
		t.Fatalf("the replaced phone's tunnel connection: %v", err)
	}
	if c, err := ph1.tun.DialConn(ctx30); err == nil {
		c.Close()
		t.Fatal("the replaced phone dialed the tunnel")
	}
	ph3 := r.connect(p3, res.e, nil)
	if id := ph3.current(); id != res.e.DeviceID {
		t.Fatalf("the new phone's current device: %q", id)
	}

	// iroh-only: the classic device's bearer, the app's password
	// sign-in and plain enrollment are refused off the tunnel (403
	// tunnel:"required", never 401); the tunnel's phone goes on
	v = r.put(map[string]bool{"tunnelOnly": true})
	if !v.TunnelOnly || v.LockedOut == nil || *v.LockedOut != 1 {
		t.Fatalf("iroh-only on: %+v", v)
	}
	if rr := d.Call(t, "GET", "/api/xbin/devices", nil, bearer(carolDev.Token)); respTunnel(rr) != "403 required" {
		t.Fatalf("a classic device's bearer, iroh-only: %d %s", rr.Status, rr)
	}
	if rr := d.Call(t, "POST", "/api/xbin/login", map[string]string{"username": "carol", "password": pwCarol}); respTunnel(rr) != "403 required" {
		t.Fatalf("the app's password sign-in, iroh-only: %d %s", rr.Status, rr)
	}
	if rr := d.Call(t, "POST", "/api/xbin/devices/enroll", map[string]string{"code": code.Code, "name": "x", "platform": "ios", "publicKey": old.PublicKeyB64()}); respTunnel(rr) != "403 required" {
		t.Fatalf("plain enrollment, iroh-only: %d %s", rr.Status, rr)
	}
	d.Must(t, "GET", "/api/xbin/login/methods", nil, 200).Decode(t, &methods)
	if !methods.Tunnel.Required {
		t.Fatalf("login methods, iroh-only: %+v", methods)
	}
	if err := ph3.do("GET", "/api/xbin/app-tunnel/client", nil, &cc); err != nil || !cc.Required {
		t.Fatalf("the tunnel's phone, iroh-only: %+v %v", cc, err)
	}

	// revoking the device closes its tunnel, and it can't dial again
	conn3, err := ph3.tun.DialConn(ctx30)
	if err != nil {
		t.Fatal(err)
	}
	defer conn3.Close()
	d.Must(t, "DELETE", "/api/xbin/devices/"+res.e.DeviceID, nil, 200)
	if err := closes(conn3); err != nil {
		t.Fatalf("the revoked phone's tunnel connection: %v", err)
	}
	if c, err := ph3.tun.DialConn(ctx30); err == nil {
		c.Close()
		t.Fatal("a revoked phone dialed the tunnel")
	}
	if v := r.view(); v.Devices.TunnelPaired != 0 {
		t.Fatalf("after the revoke: %+v", v.Devices)
	}
}

// relay runs iroh-relay --dev on a loopback port with token as its one
// access token; it returns its URL and its log.
func relay(t *testing.T, token string) (string, *logs) {
	t.Helper()
	bin := relayBin(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "iroh-relay.toml")
	toml := fmt.Sprintf("http_bind_addr = %q\nenable_quic_addr_discovery = false\nenable_metrics = false\naccess.shared_token = [%q]\n", addr, token)
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--dev", "--config-path", cfg)
	l := newLogs(t, "iroh-relay")
	cmd.Stdout, cmd.Stderr = l, l
	cmd.Env = append(os.Environ(), "RUST_LOG=info")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := "http://" + addr
	xbindtest.Eventually(t, wait, "iroh-relay answering", func() (bool, string) {
		resp, err := http.Get(url + "/")
		if err != nil {
			return false, err.Error()
		}
		resp.Body.Close()
		return true, ""
	})
	return url, l
}

// count is how many times s is in the log so far.
func (l *logs) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), s)
}

// A self-hosted relay with an access token, as docs/app-tunnel.md sets one
// up: the workspace's custom relay through the admin routes.
func TestTunnelCustomRelay(t *testing.T) {
	bin := irohBin(t)
	const token = "relay-token-0123456789abcdef"
	url, rlog := relay(t, token)
	a := xbindtest.Require(t)
	r := start(t, a, bin, "XBIN_IROH="+bin, "XBIN_IROH_RELAY=")

	// the admin: tunnels on with the relay, then its token (a restart);
	// the sidecar's home relay is it, connected
	r.put(map[string]any{"enabled": true, "relay": map[string]string{"mode": "custom", "url": url}})
	var tv view
	r.d.Must(t, "POST", "/api/xbin/app-tunnel/relay-tokens", map[string]string{"action": "set-current", "token": token}, 200).Decode(t, &tv)
	if tv.Relay.Mode != "custom" || tv.Relay.URL != url || tv.Relay.TokenHint == "" || strings.Contains(tv.Relay.TokenHint, token) {
		t.Fatalf("the relay's view: %+v", tv.Relay)
	}
	r.waitRunning(func(v view) bool { return v.RelayConnected && strings.TrimSuffix(v.HomeRelay, "/") == url })
	var st struct {
		Reachable bool
		Path      string
		Error     string
	}
	r.d.Must(t, "POST", "/api/xbin/app-tunnel/test", map[string]any{}, 200).Decode(t, &st)
	if !st.Reachable {
		t.Fatalf("the selftest through the relay: %+v", st)
	}
	t.Logf("selftest through the custom relay: %+v", st)

	// pairing through the relay: the QR code carries the token, the ticket
	// the relay alone
	bob := r.d.Login(t, "bob", pwBob)
	p, e := r.enroll(bob, "Bob's iPhone")
	if p.link.RelayToken != token || len(p.tk.Relays) != 1 || strings.TrimSuffix(p.tk.Relays[0], "/") != url || len(p.tk.Addrs) != 0 {
		t.Fatalf("the link through a custom relay: %+v, its ticket %+v", p.link, p.tk)
	}
	if strings.TrimSuffix(e.Tunnel.Relay.URL, "/") != url || e.Tunnel.Relay.Token != token {
		t.Fatalf("enrolled's relay: %+v", e.Tunnel)
	}

	// the tunnel through it, with the token
	ph := r.connect(p, e, nil)
	if id := ph.current(); id != e.DeviceID {
		t.Fatalf("over the relay, the current device is %q", id)
	}
	var cc devclient.ClientConfig
	if err := ph.do("GET", "/api/xbin/app-tunnel/client", nil, &cc); err != nil || strings.TrimSuffix(cc.Relay.URL, "/") != url || cc.Relay.Token != token {
		t.Fatalf("GET /app-tunnel/client through the relay: %+v %v", cc, err)
	}

	// the same phone with a wrong token: the relay turns its endpoint away
	// (iroh-relay's access.shared_token), and the ticket names no address
	// but the relay's
	const denied = "denied our authentication (not authorized)"
	if n := rlog.count(denied); n != 0 {
		t.Fatalf("the relay denied %d connections with the right token", n)
	}
	ph.cl.Close()
	wrong := &devclient.IrohRelay{Mode: "custom", URL: e.Tunnel.Relay.URL, Token: "not-the-relay-token-0000"}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	bad, err := devclient.StartIrohClient(ctx, r.iroh, e.Tunnel.Ticket, wire.ALPNTunnelName, p.dev, wrong, r.log)
	if err != nil {
		t.Fatal(err)
	}
	c, err := bad.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte{0}) // QUIC opens a stream with its first bytes
	xbindtest.Eventually(t, wait, "the relay refusing the wrong token", func() (bool, string) {
		return rlog.count(denied) > 0, "no denial in its log"
	})
	c.Close()
	bad.Close()

	// …and with the right one again, through
	ph2 := r.connect(p, e, nil)
	if id := ph2.current(); id != e.DeviceID {
		t.Fatalf("back with the right token: %q", id)
	}
}

// An xbind as a release builds it, the sidecar embedded: extracted to a
// sealed memfd and run confined, and a phone pairs and signs in through it.
func TestTunnelEmbedded(t *testing.T) {
	bin := irohBin(t)
	a := xbindtest.Require(t)
	emb := buildEmbedded(t, a, bin)
	r := start(t, emb, bin, "XBIN_IROH=", "XBIN_IROH_RELAY=disabled")
	r.put(map[string]bool{"enabled": true})
	v := r.waitRunning(nil)
	if !v.Confined || v.Exec != "memfd" {
		t.Fatalf("the embedded sidecar: %+v (want confined, from a memfd)", v)
	}
	bob := r.d.Login(t, "bob", pwBob)
	p, e := r.enroll(bob, "Bob's iPhone")
	if id := r.connect(p, e, nil).current(); id != e.DeviceID {
		t.Fatalf("through the embedded sidecar, the current device is %q", id)
	}
}

// buildEmbedded is a's binaries with xbind rebuilt as `make build` builds
// it (-tags xbin_iroh), the gzip of bin overlaid where the build embeds it
// (go build -overlay: nothing written into the tree).
func buildEmbedded(t *testing.T, a *xbindtest.Assets, bin string) *xbindtest.Assets {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	if err := gzipTo(&gz, raw); err != nil {
		t.Fatal(err)
	}
	gzPath := filepath.Join(dir, "xbin-iroh.gz")
	if err := os.WriteFile(gzPath, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	arch := strings.TrimSpace(goEnv(t, a.Repo, "GOARCH"))
	target := filepath.Join(a.Repo, "internal", "apptunnel", "sidecar", "embedded", "xbin-iroh.linux-"+arch+".gz")
	ov, _ := json.Marshal(map[string]map[string]string{"Replace": {target: gzPath}})
	ovPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(ovPath, ov, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bin")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-tags", "xbin_iroh", "-overlay", ovPath, "-o", filepath.Join(out, "xbind"), "./cmd/xbind")
	cmd.Dir = a.Repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building xbind with the sidecar embedded: %v\n%s", err, b)
	}
	for _, name := range []string{"bx", "xbin-vmagent"} {
		if err := os.Symlink(filepath.Join(a.Bin, name), filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
	emb := *a
	emb.Bin = out
	return &emb
}

func goEnv(t *testing.T, dir, key string) string {
	cmd := exec.Command("go", "env", key)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gzipTo(w io.Writer, b []byte) error {
	zw := gzip.NewWriter(w)
	if _, err := zw.Write(b); err != nil {
		return err
	}
	return zw.Close()
}
