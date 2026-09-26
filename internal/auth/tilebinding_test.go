package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

func sessionReq(a *Auth, host, sid string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	r.Host = host
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: a.SessionCookieName(r), Value: sid})
	}
	return r
}

// Tile-origin credentials are bound to the browser session that obtained
// them: sign-out, sign-out everywhere and expiry kill them on the next
// request; a user credential without a session binding never verifies; the
// ticket is one-time; a view-as session's credential stays read-only.
func TestTileCredentialsBoundToSession(t *testing.T) {
	a, st := assetTestAuth(t)
	sid := a.NewSession("ana", "192.0.2.1")
	r := sessionReq(a, "xbin.example", sid)

	if _, ok := a.TileBinding(sessionReq(a, "xbin.example", ""), "ana"); ok {
		t.Fatal("bound without a session")
	}
	if _, ok := a.TileBinding(r, "bob"); ok {
		t.Fatal("ana's session bound a credential for bob")
	}
	if b, ok := a.TileBinding(r, ""); !ok || b != a.credGeneration("") {
		t.Fatal("owner principal: the owner generation")
	}
	bind, ok := a.TileBinding(r, "ana")
	if !ok {
		t.Fatal("no binding for a live session")
	}
	state := NewTileState()
	tk := a.MintTileTicket("apps/a", "ana", bind, state)
	// Another browser's state (login CSRF: a ticket minted from someone
	// else's session, planted here) or none: refused, and not spent.
	for _, other := range []string{NewTileState(), "", "x"} {
		if _, ok := a.RedeemTileTicket(tk, other); ok {
			t.Fatalf("redeemed for state %q", other)
		}
	}
	g, ok := a.RedeemTileTicket(tk, state)
	if !ok || g.Tile != "apps/a" || g.UserID != "ana" || g.SessionEnd.Before(time.Now().Add(29*24*time.Hour)) || g.Gen != bind {
		t.Fatalf("redeem: %+v %v", g, ok)
	}
	if _, ok := a.RedeemTileTicket(tk, state); ok {
		t.Fatal("a ticket redeemed twice")
	}
	// Two tickets for the same (tile, user, session) in the same second are
	// distinct: both redeem (the shell's frame and a direct open side by side).
	t1, t2 := a.MintTileTicket("apps/a", "ana", bind, state), a.MintTileTicket("apps/a", "ana", bind, state)
	if t1 == t2 {
		t.Fatal("two tickets minted alike")
	}
	if _, ok := a.RedeemTileTicket(t1, state); !ok {
		t.Fatal("first of two tickets refused")
	}
	if _, ok := a.RedeemTileTicket(t2, state); !ok {
		t.Fatal("second of two tickets refused")
	}
	c := a.MintTileCookie(g.Tile, g.UserID, g.Gen, time.Hour)
	if _, ok := a.VerifyTileCookie(c); !ok {
		t.Fatal("bound cookie refused")
	}
	if _, ok := a.VerifyTileCookie(a.MintTileCookie("apps/a", "ana", "", time.Hour)); ok {
		t.Fatal("an unbound user cookie verifies")
	}
	a.DropSession(sid) // sign-out
	if _, ok := a.VerifyTileCookie(c); ok {
		t.Fatal("cookie survived sign-out")
	}
	if _, ok := a.RedeemTileTicket(a.MintTileTicket("apps/a", "ana", bind, state), state); ok {
		t.Fatal("ticket survived sign-out")
	}

	// Sign out everywhere.
	sid2 := a.NewSession("ana", "192.0.2.1")
	bind2, _ := a.TileBinding(sessionReq(a, "xbin.example", sid2), "ana")
	c2 := a.MintTileCookie("apps/a", "ana", bind2, time.Hour)
	if _, ok := a.VerifyTileCookie(c2); !ok {
		t.Fatal("fresh cookie refused")
	}
	a.DropUserSessions("ana")
	if _, ok := a.VerifyTileCookie(c2); ok {
		t.Fatal("cookie survived sign-out everywhere")
	}

	// Idle expiry.
	sid3 := a.NewSession("ana", "192.0.2.1")
	bind3, _ := a.TileBinding(sessionReq(a, "xbin.example", sid3), "ana")
	c3 := a.MintTileCookie("apps/a", "ana", bind3, time.Hour)
	a.mu.Lock()
	a.sessions[sid3].lastActive = time.Now().Add(-a.sessionIdleTTL - time.Minute)
	a.mu.Unlock()
	if _, ok := a.VerifyTileCookie(c3); ok {
		t.Fatal("cookie outlived its idle session")
	}

	// A view-as session: the credential carries the impersonator.
	if _, err := st.Upsert(users.User{ID: "boss", Role: users.RoleAdmin}, "password1"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.sessions["imp"] = &session{userID: "ana", created: time.Now(), lastActive: time.Now(), impersonator: "boss"}
	a.indexSessionLocked("imp")
	a.mu.Unlock()
	bi, ok := a.TileBinding(sessionReq(a, "xbin.example", "imp"), "ana")
	if !ok {
		t.Fatal("no binding for a view-as session")
	}
	if g, ok := a.VerifyTileCookie(a.MintTileCookie("apps/a", "ana", bi, time.Hour)); !ok || g.Impersonator != "boss" {
		t.Fatalf("view-as cookie: %+v %v", g, ok)
	}

	// The owner's credentials follow the owner token.
	oc := a.MintTileCookie("apps/a", "", a.credGeneration(""), time.Hour)
	if _, ok := a.VerifyTileCookie(oc); !ok {
		t.Fatal("owner cookie refused")
	}
	if _, err := a.RotateOwnerToken(); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyTileCookie(oc); ok {
		t.Fatal("owner cookie survived a rotation")
	}
}

// Origins mode names the session cookie __Host-xbin_session on secure
// requests (a tile origin can't toss one of that name) and reads nothing
// else there; duplicates of a credential cookie count as none.
func TestHostSessionCookie(t *testing.T) {
	a, _ := assetTestAuth(t)
	plain := httptest.NewRequest("GET", "http://xbin.example/", nil)
	plain.Host = "xbin.example"
	if a.SessionCookieName(plain) != CookieName {
		t.Fatal("legacy name changed")
	}
	a.SetHostCookies(true)
	for _, h := range []string{"xbin.localhost:9260", "localhost"} {
		r := httptest.NewRequest("GET", "http://"+h+"/", nil)
		r.Host = h
		if a.SessionCookieName(r) != "__Host-xbin_session" || !a.SessionCookieSecure(r) {
			t.Errorf("%s: %s", h, a.SessionCookieName(r))
		}
	}
	tlsReq := httptest.NewRequest("GET", "https://xbin.example/", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	if a.SessionCookieName(tlsReq) != "__Host-xbin_session" {
		t.Fatal("TLS request: plain name")
	}
	if a.SessionCookieName(plain) != CookieName || a.SessionCookieSecure(plain) {
		t.Fatal("plain http can't carry the prefix")
	}
	sid := a.NewSession("ana", "192.0.2.1")
	tlsReq.AddCookie(&http.Cookie{Name: CookieName, Value: sid}) // a tossed or pre-origins cookie
	if _, ok := a.FromRequest(tlsReq); ok {
		t.Fatal("the plain name authenticated a secure request in origins mode")
	}
	tlsReq.Header.Del("Cookie")
	tlsReq.AddCookie(&http.Cookie{Name: "__Host-xbin_session", Value: sid})
	if p, ok := a.FromRequest(tlsReq); !ok || p.UserID != "ana" {
		t.Fatal("__Host- session refused")
	}
	plain.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	plain.AddCookie(&http.Cookie{Name: CookieName, Value: a.NewSession("bob", "")})
	if _, ok := a.FromRequest(plain); ok {
		t.Fatal("duplicate session cookies authenticated")
	}
}
