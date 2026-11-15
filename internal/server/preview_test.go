package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/preview"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// A preview session is the person, read-only, by its credential alone: on
// the workspace's ordinary handler — no preview transport in front — its
// writes are refused like a view-as session's, and nobody is "viewing".
func TestPreviewSessionIsReadOnlyByCredential(t *testing.T) {
	h, s := impServer(t)
	sid, err := s.Auth.NewPreviewSession("bob", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/probe", "", sid))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"bob"`) || !strings.Contains(w.Body.String(), `"by":"~preview"`) {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/xbin/probe"},
		{"POST", "/api/xbin/impersonate/stop"}, // the way out of a view is a view's: a preview has none
		{"GET", "/ws/term"},
	} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(c.method, c.path, "{}", sid))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	p, ok := s.Auth.FromRequest(withCookie("GET", "/api/xbin/probe", "", sid))
	if !ok || !p.ReadOnly() || !p.Preview() || p.ViewAs() || p.IsAdmin() {
		t.Fatalf("principal: %+v", p)
	}
	for _, si := range s.Auth.Sessions() {
		if si.UserID == "bob" {
			t.Fatalf("a preview session is listed as a sign-in: %+v", si)
		}
	}
	// it cannot start a view-as, is not one to stop, and ends when told
	if _, err := s.Auth.NewImpersonationTicket(p, "alice"); err == nil {
		t.Fatal("a preview session minted a view-as ticket")
	}
	if _, _, ok := s.Auth.StopImpersonation(sid); ok {
		t.Fatal("a preview session was ended as a view-as session")
	}
	if by, ok := s.Auth.Impersonation(sid); ok {
		t.Fatalf("a preview session reports an impersonator: %q", by)
	}
	s.Auth.EndPreviewSession(sid)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/probe", "", sid))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("after its end: %d %s", w.Code, w.Body)
	}
	if _, err := s.Auth.NewPreviewSession("nobody", time.Minute); err == nil {
		t.Fatal("a preview session of an unknown user")
	}
	// a preview's session expires by itself too
	short, _ := s.Auth.NewPreviewSession("bob", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := s.Auth.FromRequest(withCookie("GET", "/", "", short)); ok {
		t.Fatal("a preview session outlived its ttl")
	}
}

func screenScope() *previewScope {
	return &previewScope{what: "the screen Home", tiles: []string{"apps/a"}, person: true, shell: true}
}

// The transport half: whatever the browser sends on a render's socket, it is
// the render's person, and nothing but a read passes.
func TestPreviewTransport(t *testing.T) {
	h, s := impServer(t)
	s.previewBase = "http://example.com"            // httptest.NewRequest's Host
	alice := s.Auth.NewSession("alice", "10.0.0.1") // an admin's real session the page somehow holds
	sid, _ := s.Auth.NewPreviewSession("bob", time.Minute)
	pt := s.previewTransport(h, sid, screenScope())

	r := withCookie("GET", "/api/xbin/probe", "", alice)
	r.Header.Set("Authorization", "Bearer "+alice)
	r.Header.Set("X-Forwarded-For", "10.9.9.9")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	pt.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"bob"`) || !strings.Contains(w.Body.String(), `"admin":false`) {
		t.Fatalf("a foreign credential on the socket was honoured: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/xbin/probe"}, {"PUT", "/api/xbin/prefs/x"}, {"DELETE", "/ws/term"}, {"PATCH", "/api/apps/x/y"},
		{"GET", "/ws/term"}, {"GET", "/ws/term/env"}, {"GET", "/login?token=x"}, {"POST", "/logout"}, {"POST", "/login"},
	} {
		r := withCookie(c.method, c.path, "{}", alice)
		r.Header.Set("Origin", "null")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		pt.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "preview render") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "null" {
			t.Errorf("%s %s: a sandboxed frame can't read the refusal", c.method, c.path)
		}
	}
	// a request a sandboxed tile frame starts (cross-site: its origin is
	// opaque), or one with no Fetch Metadata, gets no session — not the
	// render's, not the one it sent
	for _, site := range []string{"cross-site", "same-site", ""} {
		for _, mode := range []string{"navigate", "cors", "no-cors"} {
			r := withCookie("GET", "/api/xbin/probe", "", alice)
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
			r.Header.Set("Sec-Fetch-Mode", mode)
			w := httptest.NewRecorder()
			pt.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusFound {
				t.Errorf("Sec-Fetch-Site %q, mode %s: %d %s", site, mode, w.Code, w.Body)
			}
		}
	}
	// a WebSocket handshake carries no Fetch Metadata: its Origin decides
	for origin, want := range map[string]int{"http://example.com": 200, "null": 401, "http://evil.example": 401, "": 401} {
		r := withCookie("GET", "/api/xbin/probe", "", "")
		r.Header.Set("Upgrade", "websocket")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		pt.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("a socket from Origin %q: %d, want %d", origin, w.Code, want)
		}
	}
	// another host is not the workspace, whatever resolves to the socket
	r = withCookie("GET", "/api/xbin/probe", "", "")
	r.Host = "169.254.169.254"
	r.Header.Set("Sec-Fetch-Site", "none")
	w = httptest.NewRecorder()
	pt.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("another host on the socket: %d %s", w.Code, w.Body)
	}
	// the real session it tried to ride is untouched
	if _, ok := s.Auth.FromRequest(withCookie("GET", "/", "", alice)); !ok {
		t.Fatal("the admin's own session ended")
	}
}

// The scope: a render's socket admits what the render shows.
func TestPreviewScope(t *testing.T) {
	_, s := impServer(t)
	s.previewBase = "http://example.com"
	root := t.TempDir()
	for _, rel := range []string{"root/index.html", "apps/a/xbin.json", "apps/a/index.html", "apps/a/editor/x.js", "apps/b/xbin.json", "apps/b/app.js"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Reg = reg
	sid, _ := s.Auth.NewPreviewSession("bob", time.Minute)
	person := func(path string) *http.Request {
		r := withCookie("GET", path, "", "")
		s.Auth.SetRequestSession(r, sid)
		return r
	}
	frame := func(path, tile string) *http.Request {
		p, _ := s.Auth.FromRequest(person("/"))
		r := withCookie("GET", path, "", "")
		r.Header.Set(auth.FrameTokenHeader, s.Auth.MintFrameTokenFor(p, tile, time.Minute))
		return r
	}
	// a frame token of bob's REAL session, which a tile kept and handed its page
	real := s.Auth.NewSession("bob", "10.0.0.3")
	realFrame := func(path, tile string) *http.Request {
		p, _ := s.Auth.FromRequest(withCookie("GET", "/", "", real))
		r := withCookie("GET", path, "", "")
		r.Header.Set(auth.FrameTokenHeader, s.Auth.MintFrameTokenFor(p, tile, time.Minute))
		return r
	}
	tile := &previewScope{what: "the tile apps/a", tiles: []string{"apps/a"}}
	screen := screenScope()
	for _, c := range []struct {
		name string
		sc   *previewScope
		r    *http.Request
		ok   bool
	}{
		{"vendor", tile, person("/vendor/xbin-client.js"), true},
		{"tile: its own document", tile, person("/c/apps/a/"), true},
		{"tile: its nested component", tile, person("/c/apps/a/editor/x.js"), true},
		{"tile: another tile's file", tile, person("/c/apps/b/app.js"), false},
		{"tile: the shell", tile, person("/c/root/"), false},
		{"tile: a path that climbs out", tile, person("/c/apps/a/../b/x.js"), false},
		{"tile: the person's own API", tile, person("/api/xbin/components"), false},
		{"tile: the person's events", tile, person("/ws/events"), false},
		{"tile: its frame calls xbind", tile, frame("/api/xbin/components", "apps/a"), true},
		{"tile: its frame calls a tile it is granted", tile, frame("/api/apps/b/x", "apps/a"), true},
		{"tile: its window's frame", tile, frame("/api/xbin/whoami", "apps/a/editor"), true},
		{"tile: another tile's frame", tile, frame("/api/xbin/components", "apps/b"), false},
		{"tile: its frame, by a token from outside the render", tile, realFrame("/api/apps/a/ws", "apps/a"), false},
		{"tile: a path ticket", tile, person("/api/~abc/apps/a/x"), false},
		{"tile: nobody (the workspace answers)", tile, withCookie("GET", "/api/xbin/components", "", ""), true},
		{"screen: the shell", screen, person("/c/root/"), true},
		{"screen: the shell's reads", screen, person("/api/xbin/components"), true},
		{"screen: events", screen, person("/ws/events"), true},
		{"screen: a tile on it", screen, person("/c/apps/a/"), true},
		{"screen: a tile not on it", screen, person("/c/apps/b/"), false},
		{"screen: a token for a tile on it", screen, person("/api/xbin/frame-token?component=apps/a"), true},
		{"screen: a token for a tile not on it", screen, person("/api/xbin/frame-token?component=apps/b"), false},
		{"screen: a tile's source", screen, person("/api/xbin/code/file?component=apps/a&path=x"), false},
		{"screen: the PR badge", screen, person("/api/xbin/code/prs/summary"), true},
		{"screen: git", screen, person("/api/xbin/git/remote-info?url=https://example.com/x.git"), false},
		{"screen: the vault", screen, person("/api/xbin/vault/x"), false},
		{"screen: a tile's API in person, on it", screen, person("/api/apps/a/x"), true},
		{"screen: a tile's API in person, not on it", screen, person("/api/apps/b/x"), false},
		{"screen: another tile's frame", screen, frame("/api/xbin/components", "apps/b"), false},
	} {
		if why := s.previewAdmits(c.r, c.sc, "", false); (why == "") != c.ok {
			t.Errorf("%s: admitted=%v (%s), want %v", c.name, why == "", why, c.ok)
		}
	}
}

// Who a preview renders as: a person for themselves, a terminal for its own
// tile — nobody else.
func TestPreviewPerson(t *testing.T) {
	bob := &users.User{ID: "bob", Role: users.RoleUser}
	tileT, screenT := PreviewTarget{Tile: "apps/a"}, PreviewTarget{Screen: "Home", shell: true}
	for _, c := range []struct {
		name string
		p    auth.Principal
		t    PreviewTarget
		want string
	}{
		{"a browser session", auth.Principal{UserID: "bob", User: bob, Via: "session"}, screenT, "bob"},
		{"the app's device bearer", auth.Principal{UserID: "bob", User: bob, Via: "device"}, tileT, "bob"},
		{"the app's password bearer", auth.Principal{UserID: "bob", User: bob, Via: "app"}, tileT, "bob"},
		{"a terminal, its own tile", auth.Principal{UserID: "bob", Component: "apps/a", Via: "terminal"}, tileT, "bob"},
		{"a terminal, its tile's window", auth.Principal{UserID: "bob", Component: "apps/a", Via: "terminal"}, PreviewTarget{Tile: "apps/a/editor"}, "bob"},
		{"a terminal, another tile", auth.Principal{UserID: "bob", Component: "apps/b", Via: "terminal"}, tileT, ""},
		{"a terminal, a look-alike tile", auth.Principal{UserID: "bob", Component: "apps/a", Via: "terminal"}, PreviewTarget{Tile: "apps/ab"}, ""},
		{"a terminal, a screen", auth.Principal{UserID: "bob", Component: "apps/a", Via: "terminal"}, screenT, ""},
		{"a terminal, its tile in the shell", auth.Principal{UserID: "bob", Component: "apps/a", Via: "terminal"}, PreviewTarget{Tile: "apps/a", shell: true}, ""},
		{"the root token", auth.Principal{Owner: true, Via: "bearer"}, tileT, ""},
		{"the owner's terminal", auth.Principal{Component: "apps/a", Via: "terminal"}, tileT, ""},
		{"a tile's frame", auth.Principal{UserID: "bob", Component: "apps/a", Via: "frame"}, tileT, ""},
		{"a tile's backend", auth.Principal{Component: "apps/a", Via: "instance"}, tileT, ""},
		{"an admin viewing as bob", auth.Principal{UserID: "bob", User: bob, Via: "session", Impersonator: "alice"}, tileT, ""},
		{"a preview", auth.Principal{UserID: "bob", User: bob, Via: "session", Impersonator: auth.PreviewViewer}, tileT, ""},
		{"a cron tick", auth.Principal{UserID: "bob", Component: "apps/a", Via: "cron"}, tileT, ""},
	} {
		got, err := previewPerson(c.p, c.t)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s: %q, %v — want %q", c.name, got, err, c.want)
		}
	}
}

// Not set up, switched off, or without a sandbox: a clear 503, and a status
// that says why.
func TestPreviewUnavailable(t *testing.T) {
	h, s := impServer(t)
	bob := s.Auth.NewSession("bob", "10.0.0.2")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/preview", `{"tile":"apps/a"}`, bob))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "previews are unavailable") {
		t.Fatalf("no service: %d %s", w.Code, w.Body)
	}
	s.EnablePreview(&preview.Service{Off: "switched off for this workspace"}, "http://0.0.0.0:8642")
	s.SetPreviewHandler(h)
	if s.previewBase != "http://127.0.0.1:8642" {
		t.Errorf("a wildcard listener's address: %s", s.previewBase)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/preview", `{"tile":"apps/a"}`, bob))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "switched off") {
		t.Fatalf("switched off: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/preview", "", bob))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), "switched off") {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	// who may ask is decided before anything is rendered
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/xbin/preview", strings.NewReader(`{"tile":"apps/a"}`))
	r.Header.Set("Authorization", "Bearer "+s.Auth.OwnerTokenValue())
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "nobody's view") {
		t.Fatalf("the root token: %d %s", w.Code, w.Body)
	}
	for _, u := range []string{"https://ws.example", "http://[::]:8642", "http://:9000"} {
		s.EnablePreview(&preview.Service{}, u)
		if want := map[string]string{"https://ws.example": "https://ws.example", "http://[::]:8642": "http://127.0.0.1:8642", "http://:9000": "http://127.0.0.1:9000"}[u]; s.previewBase != want {
			t.Errorf("base of %s: %s, want %s", u, s.previewBase, want)
		}
	}
}
