package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// The warm-IP set backing the /c/ subresource gate: an IP counts as
// recently authenticated only after a real auth, slides on activity, and
// lapses after the TTL.
func TestWarmIP(t *testing.T) {
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(store)
	if _, err := store.Upsert(users.User{ID: "bob"}, "password123"); err != nil {
		t.Fatal(err)
	}
	if a.RecentlyAuthed("10.1.2.3") {
		t.Fatal("an IP that never authenticated must be cold")
	}
	if a.RecentlyAuthed("") {
		t.Fatal("empty IP is never warm")
	}

	// Login warms the IP.
	a.NewSession("alice", "10.1.2.3")
	if !a.RecentlyAuthed("10.1.2.3") {
		t.Fatal("login must warm the client IP")
	}

	// A successful FromRequest warms the request's IP too (frame tokens,
	// bearer, owner cookie — any credential). httptest's default peer is
	// 192.0.2.1:1234.
	id := a.NewSession("bob", "")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: id})
	if _, ok := a.FromRequest(r); !ok {
		t.Fatal("session cookie must resolve")
	}
	if !a.RecentlyAuthed("192.0.2.1") {
		t.Fatal("a successful FromRequest must warm the request's IP")
	}

	// Failed auth does NOT warm: a bad bearer from a fresh IP stays cold.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "203.0.113.99:8000"
	r2.Header.Set("Authorization", "Bearer wrong")
	if _, ok := a.FromRequest(r2); ok {
		t.Fatal("bad bearer must not resolve")
	}
	if a.RecentlyAuthed("203.0.113.99") {
		t.Fatal("failed auth must not warm the IP")
	}

	// The gate check itself must NOT renew the window — only real auths do
	// (else one login + sub-hourly polling keeps an IP warm forever, even
	// after every session from it is revoked).
	a.TestAgeWarmIP("10.1.2.3", 59*time.Minute)
	if !a.RecentlyAuthed("10.1.2.3") {
		t.Fatal("59m-old warmth is still inside the 1h window")
	}
	a.mu.RLock()
	aged := a.warm["10.1.2.3"]
	a.mu.RUnlock()
	if time.Since(aged) < 58*time.Minute {
		t.Fatal("a credential-less gate check must not renew the warm window")
	}

	// Expiry: past the TTL the IP lapses (and the entry is reaped).
	a.TestAgeWarmIP("10.1.2.3", 2*time.Hour)
	if a.RecentlyAuthed("10.1.2.3") {
		t.Fatal("stale warm entry must lapse")
	}
	a.mu.RLock()
	_, stillThere := a.warm["10.1.2.3"]
	a.mu.RUnlock()
	if stillThere {
		t.Fatal("lapsed entry must be evicted")
	}
}

// Sessions carry their client IPs for the admin sessions view: the login IP
// plus the last-seen IP (updated as the session is used).
func TestSessionIPs(t *testing.T) {
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	id := a.NewSession("alice", "10.0.0.1")
	if _, ok := a.sessionUser(id, "10.0.0.2"); !ok {
		t.Fatal("session must resolve")
	}
	ss := a.Sessions()
	if len(ss) != 1 {
		t.Fatalf("want 1 session, got %d", len(ss))
	}
	s := ss[0]
	if s.ID != id || s.UserID != "alice" || s.IP != "10.0.0.1" || s.LastIP != "10.0.0.2" {
		t.Fatalf("bad session row: %+v", s)
	}
	a.DropSession(id)
	if len(a.Sessions()) != 0 {
		t.Fatal("dropped session must not be listed")
	}
}
