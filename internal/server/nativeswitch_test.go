package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
)

// The workspace's native-runtime switch: off, the app's runtime documents
// answer 410 with the reason (previews keep working), the tile's web page
// is untouched, and whoami's generation reads 0.
func TestNativeRuntimeSwitchDocument(t *testing.T) {
	s, a := nativeWorkspace(t)
	dir := t.TempDir()
	st, err := users.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	owner := auth.Principal{Owner: true}
	if w := serveAs(s, "GET", "/c/apps/conv/?native=1", owner, nil); w.Code != http.StatusOK || s.NativeRuntime() != NativeRuntimeVersion {
		t.Fatalf("on: %d, runtime %d", w.Code, s.NativeRuntime())
	}
	if err := st.SetNativeRuntimeDisabled(true); err != nil {
		t.Fatal(err)
	}
	w := serveAs(s, "GET", "/c/apps/conv/?native=1", owner, nil)
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "turned off for this workspace") ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("off: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := serveAs(s, "HEAD", "/c/apps/conv/?native=1", owner, nil); w.Code != http.StatusGone {
		t.Fatalf("off, HEAD: %d", w.Code)
	}
	if s.NativeRuntime() != 0 {
		t.Fatalf("runtime while off: %d", s.NativeRuntime())
	}
	if w := serveAs(s, "GET", "/c/apps/conv/?native=1&preview=1", owner, nil); w.Code != http.StatusOK {
		t.Fatalf("a preview while off: %d %s", w.Code, w.Body.String())
	}
	if w := serveAs(s, "GET", "/c/apps/web/", owner, nil); w.Code != http.StatusOK {
		t.Fatalf("the web page while off: %d", w.Code)
	}
	// Persisted with the workspace policy.
	st2, err := users.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.NativeRuntimeDisabled() {
		t.Fatal("the switch did not survive a reopen")
	}
	if err := st.SetNativeRuntimeDisabled(false); err != nil {
		t.Fatal(err)
	}
	if w := serveAs(s, "GET", "/c/apps/conv/?native=1", owner, nil); w.Code != http.StatusOK {
		t.Fatalf("back on: %d", w.Code)
	}
}

// GET /native-runtime for anyone signed in; PUT for admins only, publishing
// `native` so open apps re-read whoami.
func TestNativeRuntimeSwitchAPI(t *testing.T) {
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Role: users.RoleAdmin}, {ID: "bob"}} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	s := &Server{Auth: a, Hub: events.NewHub()}
	h := s.Handler()
	ch, cancel := s.Hub.Subscribe(func(events.Event) bool { return true })
	defer cancel()
	call := func(method, body, sid string) (int, map[string]any) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin/native-runtime", body, sid))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	alice, bob := a.NewSession("alice", ""), a.NewSession("bob", "")
	if code, out := call("GET", "", bob); code != http.StatusOK || out["enabled"] != true || out["runtime"] != float64(1) || out["version"] != float64(1) {
		t.Fatalf("GET: %d %v", code, out)
	}
	if code, _ := call("PUT", `{"enabled":false}`, bob); code != http.StatusForbidden {
		t.Fatalf("a non-admin flipped the switch: %d", code)
	}
	if code, _ := call("PUT", `{}`, alice); code != http.StatusBadRequest {
		t.Fatalf("PUT without enabled: %d", code)
	}
	if code, _ := call("PUT", `{"enabled":false,"typo":1}`, alice); code != http.StatusBadRequest {
		t.Fatalf("PUT with an unknown field: %d", code)
	}
	if st.NativeRuntimeDisabled() {
		t.Fatal("a refused PUT changed the switch")
	}
	if code, out := call("PUT", `{"enabled":false}`, alice); code != http.StatusOK || out["enabled"] != false || out["runtime"] != float64(0) {
		t.Fatalf("admin PUT: %d %v", code, out)
	}
	select {
	case e := <-ch:
		if e.Type != "native" {
			t.Fatalf("event: %+v", e)
		}
	default:
		t.Fatal("no native event")
	}
	if code, out := call("GET", "", bob); code != http.StatusOK || out["enabled"] != false || out["runtime"] != float64(0) {
		t.Fatalf("GET while off: %d %v", code, out)
	}
	// The owner token (bx, curl) is admin too.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/xbin/native-runtime", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("Authorization", "Bearer "+a.OwnerTokenValue())
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || st.NativeRuntimeDisabled() {
		t.Fatalf("owner PUT: %d %s", w.Code, w.Body.String())
	}
}
