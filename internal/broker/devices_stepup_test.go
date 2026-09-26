package broker

import (
	"net/http"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// A stolen session can't mint a device: enrollment codes need a fresh login
// (auth.EnrollFreshLogin) or the password again.
func TestEnrollCodeStepUp(t *testing.T) {
	h, srv, b := deviceStack(t)
	sid := srv.Auth.NewSession("ann", "")
	ann := caller{cookie: sid}
	if code, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", ""); code != http.StatusOK {
		t.Fatalf("fresh login: %d %v", code, out)
	}
	// The same session, 11 minutes on (a stolen cookie, say).
	srv.Auth.TestAgeSession(sid, auth.EnrollFreshLogin+time.Minute, 0)
	code, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	if code != http.StatusForbidden || out["stepUp"] != "password" || out["code"] != nil {
		t.Fatalf("stale login minted a code: %d %v", code, out)
	}
	if code, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"nope"}`); code != http.StatusForbidden || out["stepUp"] != "password" {
		t.Fatalf("wrong password: %d %v", code, out)
	}
	if code, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"password123"}`); code != http.StatusOK || out["code"] == nil {
		t.Fatalf("password step-up: %d %v", code, out)
	}
	// An app session is held to the same rule.
	app := srv.Auth.NewBearerSession("ann", "", "")
	srv.Auth.TestAgeSession(app.Token, auth.EnrollFreshLogin+time.Minute, 0)
	if code, _ := (caller{bearer: app.Token}).do(h, "POST", "/api/xbin/devices/enroll-code", ""); code != http.StatusForbidden {
		t.Fatalf("stale app session minted a code: %d", code)
	}
	// Wrong passwords count against the login throttle.
	thr := caller{cookie: srv.Auth.NewSession("ann", "")}
	srv.Auth.TestAgeSession(thr.cookie, time.Hour, 0)
	for i := 0; i < 6; i++ {
		thr.do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"guess"}`)
	}
	if code, _ := thr.do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"password123"}`); code != http.StatusTooManyRequests {
		t.Fatalf("step-up guessing not throttled: %d", code)
	}

	// SSO-only mode: a non-admin's password is no sign-in credential there —
	// they sign in again (through the IdP); an admin keeps the password.
	h, srv, b = deviceStack(t)
	if err := b.Users.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	sid = srv.Auth.NewSession("ann", "")
	srv.Auth.TestAgeSession(sid, time.Hour, 0)
	if code, out := (caller{cookie: sid}).do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"password123"}`); code != http.StatusForbidden || out["stepUp"] != "signin" {
		t.Fatalf("SSO-only non-admin step-up: %d %v", code, out)
	}
	boss := srv.Auth.NewSession("boss", "")
	srv.Auth.TestAgeSession(boss, time.Hour, 0)
	if code, out := (caller{cookie: boss}).do(h, "POST", "/api/xbin/devices/enroll-code", `{"password":"password123"}`); code != http.StatusOK {
		t.Fatalf("SSO-only admin password step-up: %d %v", code, out)
	}
	// A fresh (SSO) sign-in needs nothing more.
	if code, _ := (caller{cookie: srv.Auth.NewSession("ann", "")}).do(h, "POST", "/api/xbin/devices/enroll-code", ""); code != http.StatusOK {
		t.Fatalf("fresh SSO-only login: %d", code)
	}
}

// Sign-out-everywhere voids a code minted just before it, says how many
// devices are left, and removes them with ?devices=1.
func TestSignOutEverywhereAndDevices(t *testing.T) {
	h, srv, _ := deviceStack(t)
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}
	dev := enroll(t, h, ann)
	_, out := ann.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	pending := out["code"].(string)
	code, out := boss.do(h, "DELETE", "/api/xbin/users/ann/sessions", "")
	if code != http.StatusOK || out["devicesLeft"] != float64(1) || out["devicesRemoved"] != nil {
		t.Fatalf("sign out everywhere: %d %v", code, out)
	}
	_, pub := newTestKey(t)
	if code, _ := (caller{}).do(h, "POST", "/api/xbin/devices/enroll", `{"code":"`+pending+`","publicKey":"`+pub+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("a code minted before sign-out-everywhere enrolled a device: %d", code)
	}
	// The device itself still signs in — until the admin removes it too.
	n := dev.challenge(t, h)
	_, out = dev.login(h, n, dev.sign(t, dev.origin, n))
	app := caller{bearer: out["token"].(string)}
	code, out = boss.do(h, "DELETE", "/api/xbin/users/ann/sessions?devices=1", "")
	if code != http.StatusOK || out["devicesRemoved"] != float64(1) || out["devicesLeft"] != float64(0) {
		t.Fatalf("sign out everywhere + devices: %d %v", code, out)
	}
	if code, _ := app.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
		t.Fatalf("app session survived: %d", code)
	}
	if code, _ := (caller{}).do(h, "POST", "/login/device/challenge", `{"deviceId":"`+dev.id+`"}`); code != http.StatusNotFound {
		t.Fatalf("removed device still challenged: %d", code)
	}
}

// A password change can remove the user's other devices (the one making
// the change stays).
func TestPasswordChangeRemovesDevices(t *testing.T) {
	h, srv, b := deviceStack(t)
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	d1, d2 := enroll(t, h, ann), enroll(t, h, ann)
	login := func(d testDevice) caller {
		n := d.challenge(t, h)
		code, out := d.login(h, n, d.sign(t, d.origin, n))
		if code != http.StatusOK {
			t.Fatalf("device login: %d %v", code, out)
		}
		return caller{bearer: out["token"].(string)}
	}
	app1, app2 := login(d1), login(d2)
	// Without the flag nothing changes.
	if code, out := app1.do(h, "POST", "/api/xbin/account/password", `{"current":"password123","new":"password456"}`); code != http.StatusOK || out["devicesRemoved"] != nil {
		t.Fatalf("plain change: %d %v", code, out)
	}
	code, out := app1.do(h, "POST", "/api/xbin/account/password", `{"current":"password456","new":"password789","removeDevices":true}`)
	if code != http.StatusOK || out["devicesRemoved"] != float64(1) {
		t.Fatalf("change + remove devices: %d %v", code, out)
	}
	if ds := b.Users.Devices("ann"); len(ds) != 1 || ds[0].ID != d1.id {
		t.Fatalf("devices left: %+v", ds)
	}
	if code, _ := app2.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
		t.Fatalf("removed device's session survived: %d", code)
	}
	if code, _ := app1.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusOK {
		t.Fatalf("the calling device was signed out: %d", code)
	}
	// From a browser: every device goes.
	if code, out := ann.do(h, "POST", "/api/xbin/account/password", `{"current":"password789","new":"password000","removeDevices":true}`); code != http.StatusOK || out["devicesRemoved"] != float64(1) {
		t.Fatalf("browser change + remove devices: %d %v", code, out)
	}
	if len(b.Users.Devices("ann")) != 0 {
		t.Fatal("devices left after a browser password change asked to remove them")
	}
}

// SSO-only mode: a device login needs the user's last SSO sign-in within the
// session max TTL, and the session it opens ends with that window — so
// removing someone at the IdP still bounds their access. Admins are exempt
// (they keep password sign-in there too).
func TestDeviceLoginSSOOnly(t *testing.T) {
	t.Setenv("XBIN_SESSION_MAX_TTL", "3s")
	h, srv, b := deviceStack(t)
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	dev := enroll(t, h, ann)
	bossDev := enroll(t, h, caller{cookie: srv.Auth.NewSession("boss", "")})
	if err := b.Users.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	// Never signed in through the IdP: refused, with the re-auth hint.
	n := dev.challenge(t, h)
	code, out := dev.login(h, n, dev.sign(t, dev.origin, n))
	if code != http.StatusForbidden || out["reauth"] != "sso" {
		t.Fatalf("device login without an SSO sign-in: %d %v", code, out)
	}
	// An admin's device is not bound.
	n = bossDev.challenge(t, h)
	if code, out := bossDev.login(h, n, bossDev.sign(t, bossDev.origin, n)); code != http.StatusOK {
		t.Fatalf("admin device login: %d %v", code, out)
	}
	// After an SSO sign-in: allowed, capped at lastSSO + max TTL.
	if err := b.Users.TouchLogin("ann", "sso"); err != nil {
		t.Fatal(err)
	}
	u, _ := b.Users.Get("ann")
	n = dev.challenge(t, h)
	code, out = dev.login(h, n, dev.sign(t, dev.origin, n))
	if code != http.StatusOK || int64(out["expiresMax"].(float64)) > u.LastSSO+3 {
		t.Fatalf("device login after SSO: %d %v (lastSSO %d)", code, out, u.LastSSO)
	}
	app := caller{bearer: out["token"].(string)}
	// A device login doesn't renew the IdP's word: once it is older than the
	// max TTL, both the session and new device logins end.
	time.Sleep(3100 * time.Millisecond)
	if code, _ := app.do(h, "GET", "/api/xbin/whoami", ""); code != http.StatusUnauthorized {
		t.Fatalf("session outlived the SSO window: %d", code)
	}
	n = dev.challenge(t, h)
	if code, out := dev.login(h, n, dev.sign(t, dev.origin, n)); code != http.StatusForbidden || out["reauth"] != "sso" {
		t.Fatalf("device login past the SSO window: %d %v", code, out)
	}
	if u, _ := b.Users.Get("ann"); u.LastSSO == 0 || u.LastLoginVia != "device" {
		t.Fatalf("a device login must keep LastSSO: %+v", u)
	}
}

// A tile renewing its frame token over HTTP (no cookie) keeps its login's
// idle clock moving: the reviewer's repro, through /api/xbin/frame-token.
func TestFrameRenewalSlidesSession(t *testing.T) {
	h, srv, _ := deviceStack(t)
	sid := srv.Auth.NewSession("ann", "")
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}
	_, out := (caller{cookie: sid}).do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	tile := caller{frame: out["token"].(string)}
	srv.Auth.TestAgeSession(sid, 0, 11*time.Hour)
	code, out := tile.do(h, "GET", "/api/xbin/frame-token?component=apps/calendar", "")
	if code != http.StatusOK || out["token"] == nil {
		t.Fatalf("renewal: %d %v", code, out)
	}
	_, out = boss.do(h, "GET", "/api/xbin/sessions", "")
	for _, row := range out["sessions"].([]any) {
		m := row.(map[string]any)
		if m["user"] == "ann" && time.Since(time.Unix(int64(m["lastActive"].(float64)), 0)) > time.Minute {
			t.Fatalf("the renewal did not count as the session's activity: %v", m)
		}
	}
}
