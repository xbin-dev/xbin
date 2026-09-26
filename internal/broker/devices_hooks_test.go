package broker

import (
	"net/http"
	"slices"
	"testing"
)

// Per-device state (push registrations, wired in internal/boot/push.go)
// follows the device: OnDeviceRemoved fires for every way a device goes —
// revoked on its own, sign-out-everywhere with ?devices=1, a password change
// with removeDevices — and OnDeviceSignedOut when a device-key session signs
// itself out. A browser logout, an app session without a device, and
// sign-out-everywhere without ?devices=1 (the devices stay enrolled) fire
// neither.
func TestDeviceHooks(t *testing.T) {
	h, srv, b := deviceStack(t)
	var removed, signedOut []string
	b.OnDeviceRemoved = func(user, dev string) { removed = append(removed, user+"/"+dev) }
	srv.OnDeviceSignedOut = func(user, dev string) { signedOut = append(signedOut, user+"/"+dev) }
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}
	login := func(d testDevice) caller {
		t.Helper()
		n := d.challenge(t, h)
		code, out := d.login(h, n, d.sign(t, d.origin, n))
		if code != http.StatusOK {
			t.Fatalf("device login: %d %v", code, out)
		}
		return caller{bearer: out["token"].(string)}
	}
	want := func(what string, got []string, w ...string) {
		t.Helper()
		if !slices.Equal(got, w) {
			t.Fatalf("%s: got %v, want %v", what, got, w)
		}
	}
	phone, tablet, laptop := enroll(t, h, ann), enroll(t, h, ann), enroll(t, h, ann)

	// A device-key session signing out.
	app := login(phone)
	if code, _ := app.do(h, "POST", "/logout", ""); code != http.StatusNoContent {
		t.Fatalf("app logout: %d", code)
	}
	want("device sign-out", signedOut, "ann/"+phone.id)
	want("device sign-out removes nothing", removed)
	if code, _ := ann.do(h, "POST", "/logout", ""); code != http.StatusFound {
		t.Fatalf("browser logout: %d", code)
	}
	want("browser logout", signedOut, "ann/"+phone.id)

	// Revoking one device.
	ann = caller{cookie: srv.Auth.NewSession("ann", "")}
	if code, _ := ann.do(h, "DELETE", "/api/xbin/devices/"+phone.id, ""); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	want("revoke", removed, "ann/"+phone.id)

	// A password change keeping the calling device removes the others.
	app = login(tablet)
	if code, out := app.do(h, "POST", "/api/xbin/account/password",
		`{"current":"password123","new":"password456","removeDevices":true}`); code != http.StatusOK || out["devicesRemoved"] != float64(1) {
		t.Fatalf("password change: %d %v", code, out)
	}
	want("password change", removed, "ann/"+phone.id, "ann/"+laptop.id)

	// Sign-out-everywhere keeps devices unless asked.
	if code, _ := boss.do(h, "DELETE", "/api/xbin/users/ann/sessions", ""); code != http.StatusOK {
		t.Fatal("sign out everywhere")
	}
	want("sign-out-everywhere", removed, "ann/"+phone.id, "ann/"+laptop.id)
	if code, _ := boss.do(h, "DELETE", "/api/xbin/users/ann/sessions?devices=1", ""); code != http.StatusOK {
		t.Fatal("sign out everywhere with devices")
	}
	want("sign-out-everywhere ?devices=1", removed, "ann/"+phone.id, "ann/"+laptop.id, "ann/"+tablet.id)
	want("no further sign-outs", signedOut, "ann/"+phone.id)
}
