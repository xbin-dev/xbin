package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers P17 T3 PO-6 — the credentials of a deployment's own origin
// (11-contract §2.6, §7.5). main's origin label is TileHostID, and its x1
// tickets and c1 cookies keep today's six parts under today's purposes, byte
// for byte. Any other deployment's label is "t-" + base32 of
// HMAC(secret, "xbin-tile-host-v1" ‖ 0 ‖ tile ‖ 0 ‖ name), the same 18
// characters, distinct per (tile, name), a tile named "<tile>+<name>"
// included. Its x2 and c2 carry the deployment as a seventh, MAC-covered
// field under purposes of their own: a swapped field, a v1 prefix, a v1
// purpose, a dropped field or a claim naming main is refused; a six-part
// verifier (the pre-deployment one) refuses them; a cookie is no ticket and a
// ticket no cookie; the grant and TilePrincipal carry the deployment, and
// main's carry none.
func TestTileOriginCredentialsPerDeployment(t *testing.T) {
	a, st := assetTestAuth(t)
	if _, err := st.Upsert(users.User{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	label := func(parts ...string) string {
		m := hmac.New(sha256.New, a.secret)
		m.Write([]byte("xbin-tile-host-v1"))
		for _, p := range parts {
			m.Write([]byte{0})
			m.Write([]byte(p))
		}
		return "t-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(m.Sum(nil)[:10]))
	}

	// Labels.
	main := a.TileHostID("apps/a")
	if main != label("apps/a") || a.TileHostIDDeployment("apps/a", "") != main || a.TileHostIDDeployment("apps/a", "main") != main {
		t.Fatalf("main's label moved: %q", main)
	}
	dev := a.TileHostIDDeployment("apps/a", "dev")
	if dev != label("apps/a", "dev") || len(dev) != 18 || strings.Trim(dev[2:], "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("dev's label %q", dev)
	}
	for _, other := range []string{main, a.TileHostIDDeployment("apps/b", "dev"), a.TileHostID("apps/a+dev"),
		a.TileHostIDDeployment("apps/a", "dev2"), a.TileHostIDDeployment("apps", "a")} {
		if other == dev {
			t.Errorf("label collision with dev's: %q", other)
		}
	}
	for _, bad := range []string{"Dev", "..", "a/b", "x y"} {
		if l := a.TileHostIDDeployment("apps/a", bad); l != "" {
			t.Errorf("a label for %q: %q", bad, l)
		}
	}

	r := httptest.NewRequest("GET", "http://xbin.example/", nil)
	r.AddCookie(&http.Cookie{Name: a.SessionCookieName(r), Value: a.NewSession("wes", "192.0.2.1")})
	bind, ok := a.TileBinding(r, "wes")
	if !ok {
		t.Fatal("no binding")
	}
	dec := func(s string) string {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Cookies: main's is today's c1; dev's a c2 naming it inside the MAC.
	c1 := a.MintTileCookieDeployment("apps/a", "main", "wes", bind, time.Hour)
	p := strings.Split(c1, ".")
	if len(p) != 6 || p[0] != "c1" || dec(p[1]) != "apps/a" || dec(p[2]) != "wes" || dec(p[4]) != bind ||
		p[5] != a.mac("xbin-tile-origin-v1", strings.Join(p[:5], ".")) {
		t.Fatalf("main's cookie is not today's c1: %q", c1)
	}
	if g, ok := a.VerifyTileCookie(c1); !ok || g.Deployment != "" {
		t.Fatalf("c1: %+v %v", g, ok)
	}
	c2 := a.MintTileCookieDeployment("apps/a", "dev", "wes", bind, time.Hour)
	p = strings.Split(c2, ".")
	if len(p) != 7 || p[0] != "c2" || dec(p[1]) != "apps/a" || dec(p[5]) != "dev" ||
		p[6] != a.mac("xbin-tile-origin-v2", strings.Join(p[:6], ".")) {
		t.Fatalf("dev's cookie: %q", c2)
	}
	g, ok := a.VerifyTileCookie(c2)
	if !ok || g.Tile != "apps/a" || g.UserID != "wes" || g.Deployment != "dev" || g.FrameGen == "" {
		t.Fatalf("c2: %+v %v", g, ok)
	}
	enc := base64.RawURLEncoding.EncodeToString
	swapped := append([]string(nil), p...)
	swapped[5] = enc([]byte("prod"))
	for name, forged := range map[string]string{
		"another deployment":  strings.Join(swapped, "."),
		"a v1 prefix":         "c1." + strings.Join(p[1:], "."),
		"the field dropped":   strings.Join(append(append([]string(nil), p[:5]...), p[6]), "."),
		"main named":          a.mintGrantDeployment("c2", "xbin-tile-origin-v2", "apps/a", "wes", bind, "main", time.Hour),
		"under the v1 tag":    a.mintGrantDeployment("c2", "xbin-tile-origin-v1", "apps/a", "wes", bind, "dev", time.Hour),
		"an x2 as a cookie":   a.MintTileTicketDeployment("apps/a", "dev", "wes", bind, NewTileState()),
		"not a name":          a.mintGrantDeployment("c2", "xbin-tile-origin-v2", "apps/a", "wes", bind, "Dev!", time.Hour),
		"expired":             a.MintTileCookieDeployment("apps/a", "dev", "wes", bind, -time.Minute),
		"another secret's c2": testAuth(t).MintTileCookieDeployment("apps/a", "dev", "wes", bind, time.Hour),
	} {
		if g, ok := a.VerifyTileCookie(forged); ok {
			t.Errorf("%s verified: %+v", name, g)
		}
	}
	if _, ok := a.verifyGrant("c2", "xbin-tile-origin-v2", c2); ok {
		t.Error("a six-part verifier accepted a c2")
	}
	if a.MintTileCookieDeployment("apps/a", "Dev!", "wes", bind, time.Hour) != "" {
		t.Error("a cookie for a string that isn't a deployment name")
	}
	tp, ok := a.TilePrincipal(g)
	if !ok || tp.Component != "apps/a" || tp.Via != "frame" || tp.Deployment != "dev" || tp.UserID != "wes" {
		t.Fatalf("TilePrincipal of a c2: %+v", tp)
	}
	g1, _ := a.VerifyTileCookie(c1)
	if tp, _ := a.TilePrincipal(g1); tp.Deployment != "" {
		t.Fatalf("TilePrincipal of a c1 names %q", tp.Deployment)
	}

	// Tickets: main's is today's x1; dev's an x2, one-time and bound to the
	// browser's exchange state like x1.
	state := NewTileState()
	x1 := a.MintTileTicketDeployment("apps/a", "", "wes", bind, state)
	if p := strings.Split(x1, "."); len(p) != 6 || p[0] != "x1" || p[5] != a.mac("xbin-tile-ticket-v1", strings.Join(p[:5], ".")) {
		t.Fatalf("main's ticket is not today's x1: %q", x1)
	}
	if g, ok := a.RedeemTileTicket(x1, state); !ok || g.Deployment != "" || g.Gen != bind {
		t.Fatalf("x1: %+v %v", g, ok)
	}
	x2 := a.MintTileTicketDeployment("apps/a", "dev", "wes", bind, state)
	p = strings.Split(x2, ".")
	if len(p) != 7 || p[0] != "x2" || dec(p[5]) != "dev" || p[6] != a.mac("xbin-tile-ticket-v2", strings.Join(p[:6], ".")) {
		t.Fatalf("dev's ticket: %q", x2)
	}
	if _, ok := a.RedeemTileTicket(x2, NewTileState()); ok {
		t.Fatal("an x2 redeemed for another browser's state")
	}
	if _, ok := a.RedeemTileTicket(c2, state); ok {
		t.Fatal("a c2 redeemed as a ticket")
	}
	swapped = append([]string(nil), p...)
	swapped[5] = enc([]byte("prod"))
	if _, ok := a.RedeemTileTicket(strings.Join(swapped, "."), state); ok {
		t.Fatal("an x2 with its deployment swapped redeemed")
	}
	g, ok = a.RedeemTileTicket(x2, state)
	if !ok || g.Tile != "apps/a" || g.Deployment != "dev" || g.Gen != bind {
		t.Fatalf("x2: %+v %v", g, ok)
	}
	if _, ok := a.RedeemTileTicket(x2, state); ok {
		t.Fatal("an x2 redeemed twice")
	}
	if a.MintTileTicketDeployment("apps/a", "..", "wes", bind, state) != "" {
		t.Error("a ticket for a string that isn't a deployment name")
	}
}
