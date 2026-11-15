package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
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
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/probe", "{}", sid))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("write: %d %s", w.Code, w.Body)
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
	// it cannot start a view-as, and ends when told
	if _, err := s.Auth.NewImpersonationTicket(p, "alice"); err == nil {
		t.Fatal("a preview session minted a view-as ticket")
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
}

// The transport half: whatever the browser sends on a render's socket, it is
// the render's person, and nothing but a read passes.
func TestPreviewTransport(t *testing.T) {
	h, s := impServer(t)
	alice := s.Auth.NewSession("alice", "10.0.0.1") // an admin's real session the page somehow holds
	sid, _ := s.Auth.NewPreviewSession("bob", time.Minute)
	pt := s.previewTransport(h, sid)

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
	// the real session it tried to ride is untouched
	if _, ok := s.Auth.FromRequest(withCookie("GET", "/", "", alice)); !ok {
		t.Fatal("the admin's own session ended")
	}
	_ = auth.PreviewViewer
}
