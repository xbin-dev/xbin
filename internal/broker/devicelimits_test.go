package broker

import (
	"net/http"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// The device limit over HTTP (D204): the default of 1 refuses a second
// enrollment with the limit's words, an override raises it, the views carry
// limit/count/override, and only xbin:users sets either.
func TestDeviceLimitAPI(t *testing.T) {
	h, srv, b := deviceStack(t)
	if err := b.Users.SetDeviceLimit(users.DefaultDeviceLimit); err != nil {
		t.Fatal(err)
	}
	ann := caller{cookie: srv.Auth.NewSession("ann", "")}
	boss := caller{cookie: srv.Auth.NewSession("boss", "")}
	first := enroll(t, h, ann)
	code, out := ann.do(h, "GET", "/api/xbin/devices", "")
	if code != http.StatusOK || out["limit"] != 1.0 || out["count"] != 1.0 {
		t.Fatalf("GET /devices: %d %v", code, out)
	}
	// a second enrollment: the code mints, the enroll answers 409 with the words
	_, out = ann.do(h, "POST", "/api/xbin/devices/enroll-code", "")
	_, pub := newTestKey(t)
	code, out = caller{}.do(h, "POST", "/api/xbin/devices/enroll", `{"code":"`+out["code"].(string)+`","name":"tablet","publicKey":"`+pub+`"}`)
	if code != http.StatusConflict || out["error"] != "you have 1 device; this workspace allows 1 per person — remove one first (an admin can raise your limit)" {
		t.Fatalf("at the limit: %d %v", code, out)
	}
	// only xbin:users reads and sets limits
	if code, _ := ann.do(h, "GET", "/api/xbin/devices/limits", ""); code != http.StatusForbidden {
		t.Fatalf("ann read limits: %d", code)
	}
	if code, _ := ann.do(h, "PATCH", "/api/xbin/users/ann", `{"maxDevices":3}`); code != http.StatusForbidden {
		t.Fatalf("ann raised her own: %d", code)
	}
	if code, out := boss.do(h, "PATCH", "/api/xbin/users/ann", `{"maxDevices":33}`); code != http.StatusBadRequest {
		t.Fatalf("33: %d %v", code, out)
	}
	if code, out := boss.do(h, "PATCH", "/api/xbin/users/ann", `{"maxDevices":2}`); code != http.StatusOK || out["maxDevices"] != 2.0 {
		t.Fatalf("override: %d %v", code, out)
	}
	enroll(t, h, ann) // now fits
	code, out = boss.do(h, "GET", "/api/xbin/devices/limits", "")
	if code != http.StatusOK || out["default"] != 1.0 || out["max"] != 32.0 {
		t.Fatalf("limits: %d %v", code, out)
	}
	if at := out["atLimit"].([]any); len(at) != 1 || at[0].(map[string]any)["user"] != "ann" || at[0].(map[string]any)["count"] != 2.0 {
		t.Fatalf("atLimit %v", out["atLimit"])
	}
	if ov := out["overrides"].([]any); len(ov) != 1 || ov[0].(map[string]any)["override"] != 2.0 {
		t.Fatalf("overrides %v", out["overrides"])
	}
	code, out = boss.do(h, "GET", "/api/xbin/users/ann/devices", "")
	if code != http.StatusOK || out["limit"] != 2.0 || out["override"] != 2.0 {
		t.Fatalf("user devices: %d %v", code, out)
	}
	// a PATCH without maxDevices keeps it; null clears it
	boss.do(h, "PATCH", "/api/xbin/users/ann", `{"name":"Ann"}`)
	if b.Users.UserDeviceLimit("ann") != 2 {
		t.Fatal("PATCH without maxDevices dropped the override")
	}
	boss.do(h, "PATCH", "/api/xbin/users/ann", `{"maxDevices":null}`)
	if b.Users.UserDeviceLimit("ann") != 1 {
		t.Fatal("null kept the override")
	}
	// the workspace default
	if code, _ := boss.do(h, "PUT", "/api/xbin/devices/limits", `{"default":0}`); code != http.StatusBadRequest {
		t.Fatalf("default 0: %d", code)
	}
	if code, out := boss.do(h, "PUT", "/api/xbin/devices/limits", `{"default":3}`); code != http.StatusOK || out["default"] != 3.0 {
		t.Fatalf("default 3: %d %v", code, out)
	}
	code, out = boss.do(h, "GET", "/api/xbin/users", "")
	for _, r := range out["users"].([]any) {
		if row := r.(map[string]any); row["id"] == "ann" && row["deviceLimit"] != 3.0 {
			t.Fatalf("users row: %v", row)
		}
	}
	// a new account with maxDevices
	if code, out := boss.do(h, "POST", "/api/xbin/users", `{"id":"cat","password":"password123","maxDevices":0}`); code != http.StatusOK && code != http.StatusCreated || b.Users.UserDeviceLimit("cat") != 0 {
		t.Fatalf("create with maxDevices: %d %v", code, out)
	}
	_ = first
}
