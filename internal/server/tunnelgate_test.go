package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// The app tunnel's edge (tunnelgate.go, D204): what a tunnel serves, the
// app's sign-ins over it and (while required) off it, device login binding
// its session, and the plain-http rules.

type tunnelTest struct {
	h        http.Handler
	s        *Server
	dev      *devclient.Device
	devID    string
	tr       auth.Transport
	required bool
}

func newTunnelTest(t *testing.T) *tunnelTest {
	t.Helper()
	h, s := impServer(t)
	tt := &tunnelTest{h: h, s: s}
	s.Auth.SetTunnelPolicy(func() bool { return tt.required })
	s.Auth.Users.SetDeviceLimit(5)
	d, err := devclient.New("phone")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Auth.Users.AddDevice("bob", users.Device{Name: "phone", PublicKey: d.PublicKeyB64(), Origin: "https://ws.example",
		TunnelKey: d.NoiseKeyB64(), Endpoint: d.EndpointHex()})
	if err != nil {
		t.Fatal(err)
	}
	tt.dev, tt.devID = d, rec.ID
	tt.tr = auth.Transport{Tunnel: true, DeviceID: rec.ID, UserID: "bob", Endpoint: d.EndpointHex()}
	return tt
}

// do sends a request on the tunnel (tunnel true) or directly.
func (tt *tunnelTest) do(tunnel bool, method, path, body string, hdr map[string]string) (int, map[string]any, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:52100"
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if tunnel {
		r = r.WithContext(auth.WithTransport(r.Context(), tt.tr))
	}
	w := httptest.NewRecorder()
	tt.h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w
}

// login runs device login on a transport and returns the bearer.
func (tt *tunnelTest) login(t *testing.T, tunnel bool, deviceID string) (int, map[string]any) {
	t.Helper()
	code, out, _ := tt.do(tunnel, "POST", "/login/device/challenge", `{"deviceId":"`+deviceID+`"}`, nil)
	if code != http.StatusOK {
		return code, out
	}
	sig, _ := tt.dev.Sign("https://ws.example", deviceID, out["nonce"].(string))
	code, out, _ = tt.do(tunnel, "POST", "/login/device", `{"deviceId":"`+deviceID+`","nonce":"`+out["nonce"].(string)+`","signature":"`+sig+`"}`, nil)
	return code, out
}

func TestTunnelEdge(t *testing.T) {
	tt := newTunnelTest(t)
	// the app's other sign-ins are refused over the tunnel
	for _, p := range []string{"/api/xbin/login", "/api/xbin/devices/enroll", "/login/ticket", "/api/xbin/invite/redeem", "/api/xbin/invite/check"} {
		if code, out, _ := tt.do(true, "POST", p, `{}`, nil); code != http.StatusForbidden || out["tunnel"] != "device" {
			t.Errorf("%s over the tunnel: %d %v", p, code, out)
		}
	}
	// nothing anonymous but the open routes
	if code, out, _ := tt.do(true, "GET", "/api/xbin/probe", "", nil); code != http.StatusForbidden || out["tunnel"] != "device" || out["docs"] != "/docs/app-tunnel.md" {
		t.Fatalf("anonymous probe: %d %v", code, out)
	}
	if code, _, _ := tt.do(true, "GET", "/api/xbin/login/methods", "", nil); code != http.StatusOK {
		t.Fatalf("login methods over the tunnel: %d", code)
	}
	// another device's sign-in, over this tunnel
	other, _ := tt.s.Auth.Users.AddDevice("bob", users.Device{Name: "tablet", PublicKey: testDeviceKey, Origin: "https://ws.example"})
	if code, out := tt.login(t, true, other.ID); code != http.StatusForbidden || out["tunnel"] != "device" {
		t.Fatalf("another device's challenge: %d %v", code, out)
	}
	// its own: a bound session
	code, out := tt.login(t, true, tt.devID)
	if code != http.StatusOK {
		t.Fatalf("device login over the tunnel: %d %v", code, out)
	}
	bearer := map[string]string{"Authorization": "Bearer " + out["token"].(string)}
	if code, out, _ := tt.do(true, "GET", "/api/xbin/probe", "", bearer); code != http.StatusOK || out["user"] != "bob" {
		t.Fatalf("bound bearer on its tunnel: %d %v", code, out)
	}
	if code, out, _ := tt.do(false, "GET", "/api/xbin/probe", "", bearer); code != http.StatusUnauthorized || out["tunnel"] != "bound" {
		t.Fatalf("bound bearer off the tunnel: %d %v", code, out)
	}
	// a browser cookie never works over a tunnel
	cookie := tt.s.Auth.NewSession("bob", "")
	if code, out, _ := tt.do(true, "GET", "/api/xbin/probe", "", map[string]string{"Cookie": auth.CookieName + "=" + cookie}); code != http.StatusForbidden || out["tunnel"] != "device" {
		t.Fatalf("browser cookie over the tunnel: %d %v", code, out)
	}
	// D's expired credential: 401, so the app signs in again
	if code, _, _ := tt.do(true, "GET", "/api/xbin/probe", "", map[string]string{"Authorization": "Bearer nope"}); code != http.StatusUnauthorized {
		t.Fatalf("dead bearer over the tunnel: %d", code)
	}
}

func TestTunnelRequired(t *testing.T) {
	tt := newTunnelTest(t)
	code, out := tt.login(t, false, tt.devID) // before iroh-only: a direct device session
	if code != http.StatusOK {
		t.Fatalf("direct login: %d %v", code, out)
	}
	direct := map[string]string{"Authorization": "Bearer " + out["token"].(string)}
	cookie := tt.s.Auth.NewSession("bob", "")
	tt.required = true
	for _, p := range []string{"/api/xbin/login", "/api/xbin/devices/enroll", "/login/ticket", "/api/xbin/invite/redeem", "/login/device/challenge", "/login/device", "/login/web-ticket"} {
		if code, out, _ := tt.do(false, "POST", p, `{}`, nil); code != http.StatusForbidden || out["tunnel"] != "required" || !strings.Contains(out["error"].(string), "QR") {
			t.Errorf("%s off the tunnel, required: %d %v", p, code, out)
		}
	}
	if code, out, _ := tt.do(false, "GET", "/login?ticket=abc", "", nil); code != http.StatusForbidden || out["tunnel"] != "required" {
		t.Errorf("web ticket off the tunnel, required: %d %v", code, out)
	}
	if code, out, _ := tt.do(false, "GET", "/api/xbin/probe", "", direct); code != http.StatusForbidden || out["tunnel"] != "required" {
		t.Fatalf("direct device bearer, required: %d %v", code, out)
	}
	// browsers aren't affected; the SSO browser leg isn't refused
	if code, out, _ := tt.do(false, "GET", "/api/xbin/probe", "", map[string]string{"Cookie": auth.CookieName + "=" + cookie}); code != http.StatusOK {
		t.Fatalf("browser, required: %d %v", code, out)
	}
	if code, out, _ := tt.do(false, "GET", "/login/sso?app=1&challenge=x", "", nil); out["tunnel"] != nil {
		t.Fatalf("SSO leg refused by the tunnel rule: %d %v", code, out)
	}
	// over the tunnel the direct session still works (same device), and login does
	if code, _, _ := tt.do(true, "GET", "/api/xbin/probe", "", direct); code != http.StatusOK {
		t.Fatalf("direct session on its device's tunnel: %d", code)
	}
	if code, out := tt.login(t, true, tt.devID); code != http.StatusOK {
		t.Fatalf("tunnel login, required: %d %v", code, out)
	}
}

func TestTunnelPlainHTTP(t *testing.T) {
	tt := newTunnelTest(t)
	tt.s.TrustedProxies = nil
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r = r.WithContext(auth.WithTransport(r.Context(), tt.tr))
	if got := tt.s.ClientIP(r); got != "tunnel/"+tt.devID {
		t.Fatalf("client key %q", got)
	}
	// EnrollOrigin over a tunnel: the device's recorded origin, never the loopback Host
	r.Host = "127.0.0.1:52100"
	if got := tt.s.EnrollOrigin(r); got != "https://ws.example" {
		t.Fatalf("enroll origin %q", got)
	}
	// the stripped headers: an app document's ws origin is ws://<Host>
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set(AppClientHeader, "app/1")
	var seen *http.Request
	gate := tt.s.tunnelGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r }))
	gate.ServeHTTP(httptest.NewRecorder(), r.Clone(r.Context()))
	if seen != nil {
		t.Fatal("an anonymous / passed the gate")
	}
	r.URL.Path = "/vendor/x.js"
	gate.ServeHTTP(httptest.NewRecorder(), r)
	if seen == nil || seen.Header.Get("X-Forwarded-Proto") != "" || seen.Header.Get("X-Forwarded-For") != "" {
		t.Fatal("X-Forwarded-* kept on a tunnel request")
	}
	if m := appWSOriginMeta(seen); !strings.Contains(m, `content="ws://127.0.0.1:52100"`) {
		t.Fatalf("ws origin meta %q", m)
	}
}
