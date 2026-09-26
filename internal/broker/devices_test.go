package broker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// deviceStack mounts the whole API (broker + server) over a real auth and
// user store: ann (a user who can write apps/calendar) and boss (admin).
func deviceStack(t *testing.T) (http.Handler, *server.Server, *Broker) {
	t.Helper()
	b := testBroker(t)
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(b.Users)
	for _, u := range []users.User{
		{ID: "ann", Tiles: map[string]string{"apps/calendar": users.LevelWrite}},
		{ID: "boss", Role: users.RoleAdmin},
	} {
		if _, err := b.Users.Upsert(u, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	srv := &server.Server{Reg: b.Reg, Hub: b.Hub, Auth: a}
	b.Register(srv)
	return srv.Handler(), srv, b
}

type caller struct{ cookie, bearer, frame string }

func (c caller) do(h http.Handler, method, path, body string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.frame != "" {
		r.Header.Set(auth.FrameTokenHeader, c.frame)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

type testDevice struct {
	key    *ecdsa.PrivateKey
	id     string
	origin string
}

func newTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return k, base64.RawURLEncoding.EncodeToString(der)
}

// enroll mints a code as `as` and enrolls a fresh key with it.
func enroll(t *testing.T, h http.Handler, as caller) testDevice {
	t.Helper()
	code, out := as.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	if code != http.StatusOK {
		t.Fatalf("enroll-code: %d %v", code, out)
	}
	link, _ := url.Parse(out["url"].(string))
	if link.Scheme != "xbin" || link.Host != "enroll" || link.Query().Get("c") != out["code"] || link.Query().Get("u") != out["origin"] {
		t.Fatalf("enroll url: %v", out)
	}
	k, pub := newTestKey(t)
	code, out = caller{}.do(h, "POST", "/api/xbin/devices/enroll",
		`{"code":"`+link.Query().Get("c")+`","name":"Ann's phone","platform":"ios","publicKey":"`+pub+`"}`)
	if code != http.StatusOK || out["deviceId"] == nil {
		t.Fatalf("enroll: %d %v", code, out)
	}
	return testDevice{key: k, id: out["deviceId"].(string), origin: out["origin"].(string)}
}

func (d testDevice) sign(t *testing.T, origin, nonce string) string {
	t.Helper()
	sum := sha256.Sum256(auth.DeviceLoginMessage(origin, d.id, nonce))
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(sig)
}

func (d testDevice) challenge(t *testing.T, h http.Handler) string {
	t.Helper()
	code, out := caller{}.do(h, "POST", "/login/device/challenge", `{"deviceId":"`+d.id+`"}`)
	if code != http.StatusOK {
		t.Fatalf("challenge: %d %v", code, out)
	}
	return out["nonce"].(string)
}

func (d testDevice) login(h http.Handler, nonce, sig string) (int, map[string]any) {
	return caller{}.do(h, "POST", "/login/device", `{"deviceId":"`+d.id+`","nonce":"`+nonce+`","signature":"`+sig+`"}`)
}

// The whole device flow over HTTP (plans/native.md §5): enrollment from a
// signed-in session, challenge login, replay/origin refusals, the bearer
// reaching what a cookie reaches, revocation killing sessions and frames.
func TestDeviceLoginFlow(t *testing.T) {
	h, srv, b := deviceStack(t)
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}

	// Only a human user mints codes: not the bootstrap token, not a tile.
	if code, _ := (caller{bearer: srv.Auth.OwnerTokenValue()}).do(h, "POST", "/api/xbin/devices/enroll-code", ""); code != http.StatusForbidden {
		t.Fatalf("owner token minted an enrollment code: %d", code)
	}
	tile := caller{frame: srv.Auth.MintFrameToken("apps/calendar", "ann", 60e9)}
	if code, _ := tile.do(h, "POST", "/api/xbin/devices/enroll-code", ""); code != http.StatusForbidden {
		t.Fatalf("a tile minted an enrollment code: %d", code)
	}

	dev := enroll(t, h, ann)
	if dev.origin != "http://example.com" { // httptest's Host, no --external-url
		t.Fatalf("origin %q", dev.origin)
	}
	// Codes are single-use; a bad key doesn't spend one.
	code, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	c := out["code"].(string)
	if code, _ := (caller{}).do(h, "POST", "/api/xbin/devices/enroll", `{"code":"`+c+`","publicKey":"AAAA"}`); code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", code)
	}
	_, pub := newTestKey(t)
	if code, _ := (caller{}).do(h, "POST", "/api/xbin/devices/enroll", `{"code":"`+c+`","publicKey":"`+pub+`"}`); code != http.StatusOK {
		t.Fatalf("code after a bad-key attempt: %d", code)
	}
	if code, _ := (caller{}).do(h, "POST", "/api/xbin/devices/enroll", `{"code":"`+c+`","publicKey":"`+pub+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("code reused: %d", code)
	}
	_ = code

	// Login: a good signature → a bearer session.
	nonce := dev.challenge(t, h)
	code, out = dev.login(h, nonce, dev.sign(t, dev.origin, nonce))
	if code != http.StatusOK || out["deviceId"] != dev.id || out["user"].(map[string]any)["id"] != "ann" {
		t.Fatalf("device login: %d %v", code, out)
	}
	app := caller{bearer: out["token"].(string)}
	// …the nonce is single-use,
	if code, _ := dev.login(h, nonce, dev.sign(t, dev.origin, nonce)); code != http.StatusUnauthorized {
		t.Fatalf("replayed nonce: %d", code)
	}
	// …bound to the origin the device enrolled with,
	n2 := dev.challenge(t, h)
	if code, _ := dev.login(h, n2, dev.sign(t, "https://evil.example", n2)); code != http.StatusUnauthorized {
		t.Fatalf("wrong origin: %d", code)
	}
	// …and to the device: another device's key can't use this one's id.
	other, _ := newTestKey(t)
	n3 := dev.challenge(t, h)
	if code, _ := dev.login(h, n3, testDevice{key: other, id: dev.id}.sign(t, dev.origin, n3)); code != http.StatusUnauthorized {
		t.Fatalf("foreign key: %d", code)
	}
	if u, _ := b.Users.Get("ann"); u.LastLoginVia != "device" || u.Devices[0].LastUsed == 0 {
		t.Fatalf("login stamps: %+v", u)
	}

	// The bearer reaches exactly what ann's cookie reaches.
	for _, probe := range []struct {
		method, path string
	}{
		{"GET", "/api/xbin/whoami"}, {"GET", "/api/xbin/components"}, {"GET", "/api/xbin/status"},
		{"GET", "/api/xbin/users"}, {"GET", "/api/xbin/frame-token?component=apps/calendar"},
		{"GET", "/api/xbin/frame-token?component=apps/email"}, {"GET", "/api/xbin/sessions"},
	} {
		cc, co := ann.do(h, probe.method, probe.path, "")
		bc, bo := app.do(h, probe.method, probe.path, "")
		if cc != bc {
			t.Errorf("%s %s: cookie %d, device bearer %d", probe.method, probe.path, cc, bc)
		}
		if probe.path == "/api/xbin/whoami" && (bo["kind"] != "user" || bo["id"] != "ann" || co["id"] != "ann") {
			t.Errorf("whoami: %v", bo)
		}
	}
	// GET /devices marks the calling device; the key never leaves.
	code, out = app.do(h, "GET", "/api/xbin/devices", "")
	list, _ := out["devices"].([]any)
	if code != http.StatusOK || len(list) != 2 {
		t.Fatalf("devices: %d %v", code, out)
	}
	first := list[0].(map[string]any)
	if first["id"] != dev.id || first["current"] != true || first["name"] != "Ann's phone" || first["publicKey"] != nil {
		t.Fatalf("device row: %v", first)
	}
	// Admin listing: boss yes, ann no.
	if code, out := boss.do(h, "GET", "/api/xbin/users/ann/devices", ""); code != http.StatusOK || len(out["devices"].([]any)) != 2 {
		t.Fatalf("admin listing: %d %v", code, out)
	}
	if code, _ := ann.do(h, "GET", "/api/xbin/users/ann/devices", ""); code != http.StatusForbidden {
		t.Fatalf("non-admin listing: %d", code)
	}
	if code, out := boss.do(h, "GET", "/api/xbin/users", ""); code != http.StatusOK || !strings.Contains(jsonOf(out), `"deviceCount":2`) {
		t.Fatalf("users list deviceCount: %v", out)
	}
	if code, out := boss.do(h, "GET", "/api/xbin/sessions", ""); code != http.StatusOK || !strings.Contains(jsonOf(out), `"via":"device"`) {
		t.Fatalf("sessions via: %v", out)
	}

	// A frame token the app mints is bound to its device session.
	_, out = app.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	appFrame := caller{frame: out["token"].(string)}
	if code, _ := appFrame.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusOK {
		t.Fatalf("app frame token: %d", code)
	}
	// Someone else can't revoke ann's device (404, not 403: no oracle).
	eve := caller{bearer: func() string {
		if _, err := b.Users.Upsert(users.User{ID: "eve"}, "password123"); err != nil {
			t.Fatal(err)
		}
		return srv.Auth.NewBearerSession("eve", "", "").Token
	}()}
	if code, _ := eve.do(h, "DELETE", "/api/xbin/devices/"+dev.id, ""); code != http.StatusNotFound {
		t.Fatalf("eve revoked ann's device: %d", code)
	}
	// Revoking drops the device's sessions — and their frames.
	code, out = ann.do(h, "DELETE", "/api/xbin/devices/"+dev.id, "")
	if code != http.StatusOK || out["dropped"] != float64(1) {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if code, _ := app.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked device's session: %d", code)
	}
	if code, _ := appFrame.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked device's frame token: %d", code)
	}
	if code, _ := (caller{}).do(h, "POST", "/login/device/challenge", `{"deviceId":"`+dev.id+`"}`); code != http.StatusNotFound {
		t.Fatalf("challenge for a revoked device: %d", code)
	}
	if code, _ := ann.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusOK {
		t.Fatal("revoking a device signed the browser out")
	}
}

// Disabled users can't log in with a device; their app sessions and frames
// die; sign-out-everywhere and logout kill frame tokens too.
func TestDeviceLoginDisabledAndSignout(t *testing.T) {
	h, srv, b := deviceStack(t)
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}
	dev := enroll(t, h, ann)
	n := dev.challenge(t, h)
	_, out := dev.login(h, n, dev.sign(t, dev.origin, n))
	app := caller{bearer: out["token"].(string)}

	// Logout kills the cookie session's frame tokens.
	_, out = ann.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	cookieFrame := caller{frame: out["token"].(string)}
	_, out = cookieFrame.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "") // a renewal
	renewed := caller{frame: out["token"].(string)}
	if code, _ := renewed.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusOK {
		t.Fatalf("renewed frame: %d", code)
	}
	if code, _ := ann.do(h, "POST", "/logout", ""); code != http.StatusFound {
		t.Fatalf("logout: %d", code)
	}
	for _, f := range []caller{cookieFrame, renewed} {
		if code, _ := f.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
			t.Fatalf("frame token outlived logout: %d", code)
		}
	}
	if code, _ := app.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusOK {
		t.Fatal("browser logout ended the app session")
	}

	// SSO-only mode (D53): a device login needs a recent SSO sign-in
	// (TestDeviceLoginSSOOnly has the rest).
	if err := b.Users.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.TouchLogin("ann", "sso"); err != nil {
		t.Fatal(err)
	}
	n = dev.challenge(t, h)
	if code, _ := dev.login(h, n, dev.sign(t, dev.origin, n)); code != http.StatusOK {
		t.Fatalf("device login under SSO-only mode after an SSO sign-in: %d", code)
	}

	// Disable ann: the app session and its frames die; login refuses.
	_, out = app.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	appFrame := caller{frame: out["token"].(string)}
	if code, _ := boss.do(h, "PATCH", "/api/xbin/users/ann", `{"disabled":true}`); code != http.StatusOK {
		t.Fatalf("disable: %d", code)
	}
	for _, c := range []caller{app, appFrame} {
		if code, _ := c.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
			t.Fatalf("disabled user's credential still works: %d", code)
		}
	}
	n = dev.challenge(t, h)
	if code, _ := dev.login(h, n, dev.sign(t, dev.origin, n)); code != http.StatusForbidden {
		t.Fatalf("disabled user's device login: %d", code)
	}
	// Re-enable: the key works again, but nothing old came back.
	if code, _ := boss.do(h, "PATCH", "/api/xbin/users/ann", `{"disabled":false}`); code != http.StatusOK {
		t.Fatal("re-enable")
	}
	for _, c := range []caller{app, appFrame} {
		if code, _ := c.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
			t.Fatalf("re-enable resurrected a credential: %d", code)
		}
	}
	n = dev.challenge(t, h)
	code, out := dev.login(h, n, dev.sign(t, dev.origin, n))
	if code != http.StatusOK {
		t.Fatalf("login after re-enable: %d", code)
	}
	app = caller{bearer: out["token"].(string)}
	_, out = app.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	appFrame = caller{frame: out["token"].(string)}
	// Sign out everywhere ends app sessions and their frames too.
	if code, out := boss.do(h, "DELETE", "/api/xbin/users/ann/sessions", ""); code != http.StatusOK || out["dropped"] != float64(1) {
		t.Fatalf("sign out everywhere: %d %v", code, out)
	}
	for _, c := range []caller{app, appFrame} {
		if code, _ := c.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
			t.Fatalf("credential survived sign-out-everywhere: %d", code)
		}
	}
	// An admin revokes anyone's device.
	if code, _ := boss.do(h, "DELETE", "/api/xbin/devices/"+dev.id, ""); code != http.StatusOK {
		t.Fatalf("admin revoke: %d", code)
	}
}

// With --external-url, devices enroll against (and sign) its origin.
func TestEnrollOriginExternalURL(t *testing.T) {
	h, srv, _ := deviceStack(t)
	srv.ExternalURL = "https://XBin.Corp.example:443/"
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	_, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	if out["origin"] != "https://xbin.corp.example" || !strings.HasPrefix(out["url"].(string), "xbin://enroll?u=https%3A%2F%2Fxbin.corp.example&c=") {
		t.Fatalf("enroll-code: %v", out)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
