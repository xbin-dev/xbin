package apptunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
)

const loopback = "http://127.0.0.1:52100"

// End to end in one process (D204, plans/app-tunnel.md §16): the admin turns
// the tunnel on; a person mints a tunnel code; the phone pairs over pair/1
// and both sides show the same code; the person confirms; the phone opens
// tunnel/1 and signs in with its device key; its session works over the
// tunnel and is refused off it; iroh-only refuses the app's other ways in;
// removing the device closes its tunnel.
func TestEndToEnd(t *testing.T) {
	r := newRig(t)
	v := r.enable()
	if v.EndpointID == "" || r.fake.cfg.Key == nil || len(r.fake.cfg.Allow) != 0 {
		t.Fatalf("enabled: %+v", v)
	}
	if fi, err := os.Stat(filepath.Join(r.dir, "keys.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("keys.json: %v %v", fi, err)
	}
	if code, out := r.do("GET", "/api/xbin/login/methods", "", nil); code != 200 || out["tunnel"].(map[string]any)["enabled"] != true {
		t.Fatalf("login methods: %v", out)
	}

	d, _ := devclient.New("Bob's iPhone")
	link, id := r.mint("bob")
	if link.Origin != "http://example.com" || len(link.PSK) != 32 || link.Ticket == "" || link.Required {
		t.Fatalf("link %+v", link)
	}
	// the classic route never redeems a tunnel code
	if code, _ := r.do("POST", "/api/xbin/devices/enroll", `{"code":"`+link.Code+`","publicKey":"`+d.PublicKeyB64()+`"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("tunnel code over /devices/enroll: %d", code)
	}
	if !r.fake.pairing {
		t.Fatal("pair/1 not opened for a pending pairing")
	}
	res := r.startPair(d, link)
	pv := r.waitState("bob", id, "confirm")
	if pv["device"].(map[string]any)["name"] != "Bob's iPhone" || len(pv["sas"].(string)) != 6 {
		t.Fatalf("pairing view %v", pv)
	}
	// only the minting session reads or confirms it
	other := r.a.NewSession("bob", "")
	if code, _ := r.do("GET", "/api/xbin/devices/pairing/"+id, "", map[string]string{"Cookie": "xbin_session=" + other}); code != http.StatusNotFound {
		t.Fatalf("another login of bob read the pairing: %d", code)
	}
	if code, _ := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("alice")); code != http.StatusNotFound {
		t.Fatalf("alice confirmed bob's pairing: %d", code)
	}
	code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob"))
	if code != 200 || out["state"] != "enrolled" || out["deviceId"] == nil {
		t.Fatalf("confirm: %d %v", code, out)
	}
	got := <-res
	if got.err != nil || got.e.DeviceID != out["deviceId"] || got.e.User.ID != "bob" || got.e.Origin != "http://example.com" {
		t.Fatalf("phone: %+v %v", got.e, got.err)
	}
	if strings.ReplaceAll(got.sas, " ", "") != pv["sas"] {
		t.Fatalf("the codes differ: phone %q, computer %q", got.sas, pv["sas"])
	}
	devID := got.e.DeviceID
	rec, _ := r.findDevice(devID)
	if rec.Endpoint != d.EndpointHex() || rec.TunnelKey != d.NoiseKeyB64() || !r.fake.allow[d.EndpointHex()] {
		t.Fatalf("device record / allowlist: %+v %v", rec, r.fake.allow)
	}
	if r.fake.pairing {
		t.Fatal("pair/1 still open with nothing pending")
	}

	// tunnel/1: device login, a bound session
	var serverKey [32]byte
	copy(serverKey[:], link.ServerKey[:])
	tun := d.Tunnel(r.fake.dialer(d), r.serverEP(), serverKey)
	client := tun.Client()
	ctx := context.Background()
	sess, err := d.Login(ctx, client, loopback, got.e.Origin, devID)
	if err != nil {
		t.Fatalf("device login over the tunnel: %v", err)
	}
	var cc devclient.ClientConfig
	if err := devclient.Do(ctx, client, "GET", loopback+"/api/xbin/app-tunnel/client", sess.Token, nil, &cc); err != nil ||
		cc.EndpointID != v.EndpointID || cc.ServerKey != v.serverKeyFor(r) || cc.Ticket == "" {
		t.Fatalf("client config: %+v %v", cc, err)
	}
	var probe map[string]any
	if err := devclient.Do(ctx, client, "GET", loopback+"/api/xbin/probe", sess.Token, nil, &probe); err != nil || probe["device"] != devID {
		t.Fatalf("probe over the tunnel: %v %v", probe, err)
	}
	if code, out := r.do("GET", "/api/xbin/probe", "", bearer(sess.Token)); code != http.StatusUnauthorized || out["tunnel"] != "bound" {
		t.Fatalf("bound bearer off the tunnel: %d %v", code, out)
	}
	// nothing anonymous but its own sign-in; nobody else's credentials
	if err := devclient.Do(ctx, client, "GET", loopback+"/api/xbin/probe", "", nil, nil); !tunnelErr(err, "device") {
		t.Fatalf("anonymous over the tunnel: %v", err)
	}
	alice := r.a.NewBearerSession("alice", "", "")
	if err := devclient.Do(ctx, client, "GET", loopback+"/api/xbin/probe", alice.Token, nil, nil); !tunnelErr(err, "device") {
		t.Fatalf("alice's bearer over bob's tunnel: %v", err)
	}
	if err := devclient.Do(ctx, client, "POST", loopback+"/api/xbin/login", "", map[string]string{"username": "bob", "password": "password123"}, nil); !tunnelErr(err, "device") {
		t.Fatalf("password sign-in over the tunnel: %v", err)
	}

	// iroh-only
	code, out = r.do("PUT", "/api/xbin/app-tunnel", `{"tunnelOnly":true}`, r.as("alice"))
	if code != 200 || out["tunnelOnly"] != true || out["lockedOut"] != 0.0 {
		t.Fatalf("tunnelOnly: %d %v", code, out)
	}
	if code, out := r.do("POST", "/api/xbin/login", `{"username":"bob","password":"password123"}`, nil); code != http.StatusForbidden || out["tunnel"] != "required" || out["docs"] != docs {
		t.Fatalf("app password sign-in, required: %d %v", code, out)
	}
	if code, out := r.do("POST", "/api/xbin/devices/enroll-code", `{}`, r.as("bob")); code != http.StatusConflict || !strings.Contains(out["error"].(string), "/xbin/devices") {
		t.Fatalf("classic code, required: %d %v", code, out)
	}
	if code, out := r.do("GET", "/api/xbin/probe", "", r.as("bob")); code != 200 {
		t.Fatalf("a browser, required: %d %v", code, out)
	}
	if err := devclient.Do(ctx, client, "GET", loopback+"/api/xbin/probe", sess.Token, nil, nil); err != nil {
		t.Fatalf("the tunnel, required: %v", err)
	}
	if code, out := r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":false}`, r.as("alice")); code != 200 || out["tunnelOnly"] != false {
		t.Fatalf("disabling clears tunnelOnly: %v", out)
	}
	r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":true}`, r.as("alice"))
	client = tun.Client() // the restart closed the tunnel's connections (a POST isn't retried on a dead one)
	sess, err = d.Login(ctx, client, loopback, got.e.Origin, devID)
	if err != nil {
		t.Fatalf("login after a restart: %v", err)
	}

	// revoke: the device's tunnel closes and the sidecar forgets it
	conn, err := tun.DialConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.users.RemoveDevice(devID); err != nil {
		t.Fatal(err)
	}
	r.a.DropDeviceSessions(devID)
	r.svc.DeviceRemoved("bob", devID)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("a revoked device's connection stayed open: %v", err)
	}
	if r.fake.allow[d.EndpointHex()] || !contains(r.fake.dropped, d.EndpointHex()) {
		t.Fatalf("allowlist after revoke: %v dropped %v", r.fake.allow, r.fake.dropped)
	}
	if _, err := tun.DialConn(ctx); err == nil {
		t.Fatal("a revoked device dialed the tunnel")
	}
}

func isTimeout(err error) bool {
	var ne interface{ Timeout() bool }
	return errors.As(err, &ne) && ne.Timeout()
}

func tunnelErr(err error, want string) bool {
	var he *devclient.HTTPError
	return errors.As(err, &he) && he.Tunnel() == want
}

func (r *rig) findDevice(id string) (dev devStub, ok bool) {
	_, d, ok := r.users.FindDevice(id)
	return devStub{Endpoint: d.Endpoint, TunnelKey: d.TunnelKey}, ok
}

type devStub struct{ Endpoint, TunnelKey string }

func (v View) serverKeyFor(r *rig) string { return r.svc.keys.serverKey() }

// "They don't" burns the pairing; the phone hears mismatch.
func TestPairingMismatch(t *testing.T) {
	r := newRig(t)
	r.enable()
	d, _ := devclient.New("phone")
	link, id := r.mint("bob")
	res := r.startPair(d, link)
	r.waitState("bob", id, "confirm")
	if code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":false}`, r.as("bob")); code != 200 || out["state"] != "rejected" || out["reason"] != "mismatch" {
		t.Fatalf("they don't: %d %v", code, out)
	}
	got := <-res
	var rj *devclient.Rejected
	if !errors.As(got.err, &rj) || rj.Reason != "mismatch" {
		t.Fatalf("phone: %v", got.err)
	}
	if len(r.users.Devices("bob")) != 0 {
		t.Fatal("a device after they don't")
	}
	if code, _ := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob")); code != http.StatusConflict {
		t.Fatalf("confirming a rejected pairing: %d", code)
	}
	// the code is spent: a second phone can't use the link
	d2, _ := devclient.New("phone 2")
	if _, err := d2.Pair(context.Background(), r.fake.dialer(d2), link, devclient.PairOptions{ServerEndpoint: r.serverEP()}); err == nil {
		t.Fatal("a burnt link paired")
	}
}

// The phone leaving before the confirmation abandons the pairing: 409, no
// device.
func TestPairingAbandoned(t *testing.T) {
	r := newRig(t)
	r.enable()
	d, _ := devclient.New("phone")
	link, id := r.mint("bob")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Pair(ctx, r.fake.dialer(d), link, devclient.PairOptions{ServerEndpoint: r.serverEP()})
		done <- err
	}()
	r.waitState("bob", id, "confirm")
	cancel()
	<-done
	r.waitState("bob", id, "abandoned")
	if code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob")); code != http.StatusConflict || out["state"] != "abandoned" {
		t.Fatalf("confirm after abandon: %d %v", code, out)
	}
	if len(r.users.Devices("bob")) != 0 {
		t.Fatal("an orphan device record")
	}
}

// Nobody confirming in the window: expired, on both sides.
func TestPairingExpired(t *testing.T) {
	r := newRig(t)
	r.svc.window = 150 * time.Millisecond
	r.enable()
	d, _ := devclient.New("phone")
	link, id := r.mint("bob")
	got := <-r.startPair(d, link)
	var rj *devclient.Rejected
	if !errors.As(got.err, &rj) || rj.Reason != "expired" {
		t.Fatalf("phone: %v", got.err)
	}
	r.waitState("bob", id, "expired")
}

// A wrong psk never claims the pairing: the genuine phone still pairs.
func TestPairingWrongPSK(t *testing.T) {
	r := newRig(t)
	r.enable()
	link, id := r.mint("bob")
	bad := link
	bad.PSK = append([]byte(nil), link.PSK...)
	bad.PSK[0] ^= 1
	evil, _ := devclient.New("evil")
	if _, err := evil.Pair(context.Background(), r.fake.dialer(evil), bad, devclient.PairOptions{ServerEndpoint: r.serverEP()}); err == nil {
		t.Fatal("a wrong psk paired")
	}
	r.waitState("bob", id, "minted")
	d, _ := devclient.New("phone")
	res := r.startPair(d, link)
	r.waitState("bob", id, "confirm")
	r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob"))
	if got := <-res; got.err != nil {
		t.Fatalf("the genuine phone: %v", got.err)
	}
}

// At the limit a tunnel code still mints; the confirmation answers 409
// until it names a device to replace, which goes in the same step (its
// sessions end, its push hook runs).
func TestPairingReplaceAtLimit(t *testing.T) {
	r := newRig(t)
	r.enable()
	var removed []string
	r.svc.opt.OnDeviceRemoved = func(u, d string) { removed = append(removed, u+"/"+d) }
	_, first := r.pair("bob", "old phone")
	old := r.a.NewBearerSession("bob", first.DeviceID, "")
	if code, out := r.do("POST", "/api/xbin/devices/enroll-code", `{}`, r.as("bob")); code != http.StatusConflict || out["limit"] != 1.0 || out["count"] != 1.0 {
		t.Fatalf("classic code at the limit: %d %v", code, out)
	}
	d, _ := devclient.New("new phone")
	link, id := r.mint("bob") // a tunnel code mints at the limit
	res := r.startPair(d, link)
	r.waitState("bob", id, "confirm")
	if code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true}`, r.as("bob")); code != http.StatusConflict || out["limit"] != 1.0 {
		t.Fatalf("confirm at the limit: %d %v", code, out)
	}
	r.waitState("bob", id, "confirm") // still waiting
	code, out := r.do("POST", "/api/xbin/devices/pairing/"+id+"/confirm", `{"match":true,"replace":"`+first.DeviceID+`"}`, r.as("bob"))
	if code != 200 || out["state"] != "enrolled" || out["replaced"] != first.DeviceID {
		t.Fatalf("replace: %d %v", code, out)
	}
	if got := <-res; got.err != nil {
		t.Fatal(got.err)
	}
	if ds := r.users.Devices("bob"); len(ds) != 1 || ds[0].Name != "new phone" {
		t.Fatalf("devices %v", ds)
	}
	if code, _ := r.do("GET", "/api/xbin/probe", "", bearer(old.Token)); code != http.StatusUnauthorized {
		t.Fatalf("the replaced device's session: %d", code)
	}
	if len(removed) != 1 || removed[0] != "bob/"+first.DeviceID {
		t.Fatalf("hook %v", removed)
	}
	// a limit of 0: not even a tunnel code
	zero := 0
	r.users.SetMaxDevices("bob", &zero)
	if code, _ := r.do("POST", "/api/xbin/devices/enroll-code", `{"tunnel":true}`, r.as("bob")); code != http.StatusConflict {
		t.Fatalf("tunnel code at limit 0: %d", code)
	}
}

// An unknown key, or a known key from another EndpointId, never completes IK.
func TestTunnelRefusesUnknown(t *testing.T) {
	r := newRig(t)
	r.enable()
	d, e := r.pair("bob", "phone")
	var sk [32]byte
	copy(sk[:], r.svc.keys.noise.Public[:])
	stranger, _ := devclient.New("stranger")
	r.fake.allow[stranger.EndpointHex()] = true // even past the sidecar's allowlist
	if _, err := stranger.Tunnel(r.fake.dialer(stranger), r.serverEP(), sk).DialConn(context.Background()); err == nil {
		t.Fatal("an unknown key completed IK")
	}
	// bob's Noise key under another iroh identity
	clone := *stranger
	clone.Noise = d.Noise
	if _, err := clone.Tunnel(r.fake.dialer(&clone), r.serverEP(), sk).DialConn(context.Background()); err == nil {
		t.Fatal("a device key from another EndpointId completed IK")
	}
	if _, err := d.Tunnel(r.fake.dialer(d), r.serverEP(), sk).DialConn(context.Background()); err != nil {
		t.Fatalf("the device itself: %v (%s)", err, e.DeviceID)
	}
	// a disabled person's device: off the allowlist, refused
	u, _ := r.users.Get("bob")
	nu := *u
	nu.Disabled = true
	r.users.Upsert(nu, "")
	r.svc.UserChanged("bob", false)
	if r.fake.allow[d.EndpointHex()] {
		t.Fatal("a disabled person's device stayed allowed")
	}
	r.fake.allow[d.EndpointHex()] = true
	if _, err := d.Tunnel(r.fake.dialer(d), r.serverEP(), sk).DialConn(context.Background()); err == nil {
		t.Fatal("a disabled person's device completed IK")
	}
}

// Settings: validation, tokens shown as hints only (a generated one once),
// rotation through next, the env override.
func TestConfigAndTokens(t *testing.T) {
	r := newRig(t)
	r.enable()
	for _, bad := range []string{
		`{"relay":{"mode":"custom","url":"http://relay.example"}}`,
		`{"relay":{"mode":"custom"}}`,
		`{"relay":{"mode":"other"}}`,
	} {
		if code, _ := r.do("PUT", "/api/xbin/app-tunnel", bad, r.as("alice")); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _ := r.do("PUT", "/api/xbin/app-tunnel", `{"enabled":true}`, r.as("bob")); code != http.StatusForbidden {
		t.Fatalf("bob changed the tunnel: %d", code)
	}
	if code, _ := r.do("POST", "/api/xbin/app-tunnel/relay-tokens", `{"action":"generate-next"}`, r.as("alice")); code != http.StatusConflict {
		t.Fatalf("a token for n0: %d", code)
	}
	starts := r.fake.starts
	code, out := r.do("PUT", "/api/xbin/app-tunnel", `{"relay":{"mode":"custom","url":"https://relay.example"}}`, r.as("alice"))
	if code != 200 || out["relay"].(map[string]any)["url"] != "https://relay.example" || r.fake.starts != starts+1 || r.fake.cfg.Relay.URL != "https://relay.example" {
		t.Fatalf("custom relay: %d %v", code, out)
	}
	code, out = r.do("POST", "/api/xbin/app-tunnel/relay-tokens", `{"action":"generate-next"}`, r.as("alice"))
	tok, _ := out["token"].(string)
	next := out["relay"].(map[string]any)["next"].(map[string]any)
	if code != 200 || len(tok) < 16 || next["tokenHint"] != "…"+tok[len(tok)-4:] {
		t.Fatalf("generate-next: %d %v", code, out)
	}
	_, out = r.do("GET", "/api/xbin/app-tunnel", "", r.as("alice"))
	if out["token"] != nil || strings.Contains(stringify(out), tok) {
		t.Fatal("the token shown again")
	}
	if code, _ := r.do("POST", "/api/xbin/app-tunnel/relay-tokens", `{"action":"set-next","token":"short"}`, r.as("alice")); code != http.StatusBadRequest {
		t.Fatalf("a short token: %d", code)
	}
	code, out = r.do("POST", "/api/xbin/app-tunnel/relay-tokens", `{"action":"promote"}`, r.as("alice"))
	if code != 200 || out["relay"].(map[string]any)["next"] != nil || r.fake.cfg.Relay.Token != tok {
		t.Fatalf("promote: %d %v (%v)", code, out, r.fake.cfg.Relay)
	}
	// the link carries the token; the pairing's enrolled message too
	link, _ := r.mint("bob")
	if link.RelayToken != tok {
		t.Fatalf("link token %q", link.RelayToken)
	}
	if code, out := r.do("POST", "/api/xbin/app-tunnel/test", `{}`, r.as("alice")); code != 200 || out["reachable"] != true || out["rttMs"] != 42.0 {
		t.Fatalf("test: %d %v", code, out)
	}
	raw, _ := os.ReadFile(filepath.Join(r.dir, "config.json"))
	if fi, _ := os.Stat(filepath.Join(r.dir, "config.json")); fi.Mode().Perm() != 0o600 || !strings.Contains(string(raw), tok) {
		t.Fatal("config.json")
	}
	// a restart keeps it all, keys included
	ep := r.svc.view().EndpointID
	svc2, err := New(Options{Dir: r.dir, Users: r.users, Auth: r.a, Runner: newFakeRunner(), IsAdmin: r.svc.opt.IsAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	if v := svc2.view(); v.EndpointID != ep || v.Relay.TokenHint != hint(tok) || !v.Enabled {
		t.Fatalf("reload: %+v", v)
	}
	// XBIN_IROH_RELAY wins and says so
	svc3, err := New(Options{Dir: r.dir, Users: r.users, Auth: r.a, Runner: newFakeRunner(), EnvRelay: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc3.Close()
	if v := svc3.view(); v.Env != "disabled" {
		t.Fatalf("env: %+v", v)
	}
	if _, err := New(Options{Dir: t.TempDir(), EnvRelay: "http://relay.example"}); err == nil {
		t.Fatal("a plain-http env relay")
	}
}

func stringify(v any) string { b, _ := json.Marshal(v); return string(b) }

// The upgrade route: the device's own fresh session, direct, registers its
// keys; refused while the tunnel is required.
func TestUpgrade(t *testing.T) {
	r := newRig(t)
	r.enable()
	r.users.SetDeviceLimit(5)
	d, _ := devclient.New("old phone")
	rec, err := r.users.AddDevice("bob", usersDevice(d))
	if err != nil {
		t.Fatal(err)
	}
	bs := r.a.NewBearerSession("bob", rec.ID, "")
	body := `{"noiseKey":"` + d.NoiseKeyB64() + `","endpoint":"` + d.EndpointHex() + `"}`
	if code, _ := r.do("POST", "/api/xbin/devices/"+rec.ID+"/tunnel", body, r.as("bob")); code != http.StatusForbidden {
		t.Fatalf("a browser upgraded a device: %d", code)
	}
	if code, _ := r.do("POST", "/api/xbin/devices/"+rec.ID+"/tunnel", `{"noiseKey":"x","endpoint":"y"}`, bearer(bs.Token)); code != http.StatusBadRequest {
		t.Fatalf("bad keys: %d", code)
	}
	code, out := r.do("POST", "/api/xbin/devices/"+rec.ID+"/tunnel", body, bearer(bs.Token))
	if code != 200 || out["endpointId"] == nil || !r.fake.allow[d.EndpointHex()] {
		t.Fatalf("upgrade: %d %v", code, out)
	}
	var sk [32]byte
	copy(sk[:], r.svc.keys.noise.Public[:])
	if _, err := d.Tunnel(r.fake.dialer(d), r.serverEP(), sk).DialConn(context.Background()); err != nil {
		t.Fatalf("the upgraded device's tunnel: %v", err)
	}
	r.do("PUT", "/api/xbin/app-tunnel", `{"tunnelOnly":true}`, r.as("alice"))
	if code, out := r.do("POST", "/api/xbin/devices/"+rec.ID+"/tunnel", body, bearer(bs.Token)); code != http.StatusForbidden || out["tunnel"] != "required" {
		t.Fatalf("upgrade while required: %d %v", code, out)
	}
}

// Alerts: none while running; warn when it can't run; crit when required.
func TestAlerts(t *testing.T) {
	r := newRig(t)
	r.enable()
	if a := r.svc.Alerts(); len(a) != 0 {
		t.Fatalf("running: %v", a)
	}
	r.fake.Stop()
	now := time.Now()
	r.svc.opt.Now = func() time.Time { return now }
	r.svc.Alerts()
	now = now.Add(downAfter)
	if a := r.svc.Alerts(); len(a) != 1 || a[0].Level != "warn" {
		t.Fatalf("down: %v", a)
	}
	r.svc.mu.Lock()
	r.svc.cfg.TunnelOnly = true
	r.svc.mu.Unlock()
	if a := r.svc.Alerts(); len(a) != 1 || a[0].Level != "crit" {
		t.Fatalf("down, required: %v", a)
	}
}
