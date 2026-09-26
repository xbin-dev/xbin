package broker

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// whoami tells every caller — the xbin app asks it before opening tiles —
// that this xbind serves native runtime documents (native.runtime); an
// xbind without the field means every tile is a web tile.
func TestWhoamiNativeRuntime(t *testing.T) {
	b := testBroker(t)
	for name, p := range map[string]auth.Principal{
		"owner":   {Owner: true},
		"user":    {UserID: "ann", User: &users.User{ID: "ann", Role: "user"}},
		"element": {Component: "apps/x", Via: "frame"},
	} {
		r := httptest.NewRequest("GET", "/whoami", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiWhoami(w, r)
		var who struct {
			Native struct {
				Runtime int `json:"runtime"`
			} `json:"native"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &who); err != nil || who.Native.Runtime != server.NativeRuntimeVersion || server.NativeRuntimeVersion != 1 {
			t.Fatalf("%s: whoami native = %+v (%v): %s", name, who.Native, err, w.Body.String())
		}
	}
}

// An admin turned native tile UIs off for the workspace: every caller reads
// native {runtime: 0, disabled: true}, and the app opens tiles as web pages.
func TestWhoamiNativeRuntimeOff(t *testing.T) {
	b := testBroker(t)
	if b.Users == nil {
		t.Fatal("testBroker has no user store")
	}
	if err := b.Users.SetNativeRuntimeDisabled(true); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]auth.Principal{
		"owner":   {Owner: true},
		"user":    {UserID: "ann", User: &users.User{ID: "ann", Role: "user"}},
		"element": {Component: "apps/x", Via: "frame"},
	} {
		r := httptest.NewRequest("GET", "/whoami", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiWhoami(w, r)
		var who struct {
			Native map[string]any `json:"native"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &who); err != nil || who.Native["runtime"] != float64(0) || who.Native["disabled"] != true {
			t.Fatalf("%s: whoami native = %v (%v)", name, who.Native, err)
		}
	}
}
