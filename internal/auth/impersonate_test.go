package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

func impAuth(t *testing.T) *Auth {
	t.Helper()
	a := testAuth(t)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelWrite}},
		{ID: "carol", Role: users.RoleUser, Disabled: true},
	} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	return a
}

func cookieReq(sid string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	return r
}

func TestImpersonationRoundTrip(t *testing.T) {
	a := impAuth(t)
	adminSid := a.NewSession("alice", "10.0.0.1")
	admin, ok := a.FromRequest(cookieReq(adminSid))
	if !ok || !admin.IsAdmin() {
		t.Fatal("admin session")
	}

	tk, err := a.NewImpersonationTicket(admin, "bob")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := a.RedeemImpersonation(tk, admin, adminSid, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RedeemImpersonation(tk, admin, adminSid, "10.0.0.1"); err == nil {
		t.Fatal("ticket redeemed twice")
	}

	// The view reads as bob — bob's tiles, no admin — and is marked.
	p, ok := a.FromRequest(cookieReq(sid))
	if !ok || p.UserID != "bob" || p.IsAdmin() || !p.CanWriteTile("apps/x") {
		t.Fatalf("impersonated principal: %+v", p)
	}
	if p.Impersonator != "alice" || !p.ReadOnly() {
		t.Fatalf("impersonator marker: %+v", p)
	}
	// A frame token minted for bob's tile under this session carries the mark.
	r := cookieReq(sid)
	r.Header.Set(FrameTokenHeader, a.MintFrameToken("apps/x", "bob", time.Minute))
	if fp, ok := a.FromRequest(r); !ok || fp.Component != "apps/x" || fp.Impersonator != "alice" {
		t.Fatalf("frame principal under impersonation: %+v %v", fp, ok)
	}
	// No nesting: the view can't mint or redeem.
	if _, err := a.NewImpersonationTicket(p, "alice"); err == nil {
		t.Fatal("nested ticket minted")
	}
	// Sessions API sees the impersonator.
	var seen bool
	for _, si := range a.Sessions() {
		if si.ID == sid && si.Impersonator == "alice" && si.UserID == "bob" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("sessions list lacks the impersonation row")
	}
	if imp, ok := a.Impersonation(sid); !ok || imp != "alice" {
		t.Fatal("Impersonation lookup")
	}
	if _, ok := a.Impersonation(adminSid); ok {
		t.Fatal("plain session reported as impersonation")
	}

	// Stop hands the browser back to alice's live session.
	restore, owner, ok := a.StopImpersonation(sid)
	if !ok || owner || restore != adminSid {
		t.Fatalf("stop: %q %v %v", restore, owner, ok)
	}
	if _, ok := a.FromRequest(cookieReq(sid)); ok {
		t.Fatal("impersonation session survived stop")
	}
	if q, ok := a.FromRequest(cookieReq(adminSid)); !ok || q.UserID != "alice" {
		t.Fatal("admin session lost")
	}
	if _, _, ok := a.StopImpersonation(adminSid); ok {
		t.Fatal("stop on a plain session")
	}
}

func TestImpersonationRefusals(t *testing.T) {
	a := impAuth(t)
	adminSid := a.NewSession("alice", "")
	admin, _ := a.FromRequest(cookieReq(adminSid))
	bobSid := a.NewSession("bob", "")
	bob, _ := a.FromRequest(cookieReq(bobSid))

	if _, err := a.NewImpersonationTicket(bob, "alice"); err == nil {
		t.Fatal("non-admin minted a ticket")
	}
	if _, err := a.NewImpersonationTicket(admin, "alice"); err == nil {
		t.Fatal("self ticket")
	}
	if _, err := a.NewImpersonationTicket(admin, "carol"); err == nil {
		t.Fatal("disabled target")
	}
	if _, err := a.NewImpersonationTicket(admin, "nobody"); err == nil {
		t.Fatal("unknown target")
	}
	if _, err := a.NewImpersonationTicket(Principal{Component: "apps/x", Via: "instance"}, "bob"); err == nil {
		t.Fatal("headless element minted a ticket")
	}
	// The admin console calls through its frame token: the principal is the
	// tile with the driving admin's id (no User record) — still an admin.
	fr := httptest.NewRequest("GET", "/x", nil)
	fr.Header.Set(FrameTokenHeader, a.MintFrameToken("tiles/admin", "alice", time.Minute))
	viaFrame, ok := a.FromRequest(fr)
	if !ok || viaFrame.Component != "tiles/admin" || viaFrame.User != nil {
		t.Fatalf("frame principal: %+v", viaFrame)
	}
	if _, err := a.NewImpersonationTicket(viaFrame, "bob"); err != nil {
		t.Fatalf("admin via frame token: %v", err)
	}
	fr.Header.Set(FrameTokenHeader, a.MintFrameToken("tiles/admin", "bob", time.Minute))
	bobFrame, _ := a.FromRequest(fr)
	if _, err := a.NewImpersonationTicket(bobFrame, "alice"); err == nil {
		t.Fatal("non-admin via frame token minted a ticket")
	}
	// A ticket is bound to its minter: bob's browser can't redeem alice's.
	tk, err := a.NewImpersonationTicket(admin, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RedeemImpersonation(tk, bob, bobSid, ""); err == nil {
		t.Fatal("ticket redeemed by another browser")
	}
	if _, err := a.RedeemImpersonation(tk, admin, adminSid, ""); err == nil {
		t.Fatal("ticket survived a failed redemption")
	}
	if _, err := a.RedeemImpersonation("nope", admin, adminSid, ""); err == nil {
		t.Fatal("bogus ticket")
	}
	// Bootstrap-token admins get handed back the owner cookie.
	owner := Principal{Owner: true, Via: "cookie"}
	tk, _ = a.NewImpersonationTicket(owner, "bob")
	sid, err := a.RedeemImpersonation(tk, owner, a.OwnerTokenValue(), "")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := a.FromRequest(cookieReq(sid)); !ok || p.Impersonator != "owner" {
		t.Fatalf("owner impersonation: %+v", p)
	}
	if restore, isOwner, ok := a.StopImpersonation(sid); !ok || !isOwner || restore != "" {
		t.Fatalf("owner stop: %q %v %v", restore, isOwner, ok)
	}
	// Sign-out-everywhere on the viewed user ends the views of them too.
	tk, _ = a.NewImpersonationTicket(admin, "bob")
	sid, _ = a.RedeemImpersonation(tk, admin, adminSid, "")
	a.DropUserSessions("bob")
	if _, ok := a.FromRequest(cookieReq(sid)); ok {
		t.Fatal("impersonation survived the user's sign-out-everywhere")
	}
}
