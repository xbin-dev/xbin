package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/magik6k/xbin/internal/users"
)

func testAuth(t *testing.T) *Auth {
	t.Helper()
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFrameTokens(t *testing.T) {
	a := testAuth(t)
	tok := a.MintFrameToken("apps/x", "", time.Minute)
	comp, _, ok := a.VerifyFrameToken(tok)
	if !ok || comp != "apps/x" {
		t.Fatalf("verify: %q %v", comp, ok)
	}
	if _, _, ok := a.VerifyFrameToken(tok + "x"); ok {
		t.Fatal("tampered token verified")
	}
	expired := a.MintFrameToken("apps/x", "", -time.Minute)
	if _, _, ok := a.VerifyFrameToken(expired); ok {
		t.Fatal("expired token verified")
	}
	// Token from a different secret must not verify.
	b := testAuth(t)
	if _, _, ok := b.VerifyFrameToken(tok); ok {
		t.Fatal("cross-instance token verified")
	}
}

func TestFromRequest(t *testing.T) {
	a := testAuth(t)

	r := httptest.NewRequest("GET", "/x", nil)
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("unauthenticated request accepted")
	}

	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+a.OwnerToken)
	if p, ok := a.FromRequest(r); !ok || !p.Owner {
		t.Fatal("owner bearer rejected")
	}

	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("bad bearer accepted")
	}

	a.RegisterInstance("itok", "apps/x")
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer itok")
	if p, ok := a.FromRequest(r); !ok || p.Owner || p.Component != "apps/x" {
		t.Fatalf("instance token: %+v %v", p, ok)
	}
	a.RevokeInstance("itok")
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("revoked instance token accepted (stale generation lives on!)")
	}

	// Cookie alone = owner; cookie + frame token = element frontend.
	r = httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: a.OwnerToken})
	if p, ok := a.FromRequest(r); !ok || !p.Owner {
		t.Fatal("cookie owner rejected")
	}
	r.Header.Set(FrameTokenHeader, a.MintFrameToken("apps/y", "", time.Minute))
	if p, ok := a.FromRequest(r); !ok || p.Owner || p.Component != "apps/y" {
		t.Fatalf("frame principal: %+v %v", p, ok)
	}
	// Invalid frame token must reject, not silently downgrade to owner.
	r.Header.Set(FrameTokenHeader, "garbage")
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("invalid frame token downgraded to owner")
	}
}

// Disabling token login must reject the owner-token *cookie* (so a leaked token
// can't be pasted into a cookie) while leaving the *Bearer* path intact — bx
// and component backends authenticate with the owner token over the gateway.
func TestTokenLoginDisabledGating(t *testing.T) {
	a := testAuth(t)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "admin", Role: users.RoleAdmin}, "pw"); err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)

	cookieReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: a.OwnerToken})
		return r
	}
	bearerReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+a.OwnerToken)
		return r
	}

	if p, ok := a.FromRequest(cookieReq()); !ok || !p.Owner {
		t.Fatal("owner cookie rejected while token login enabled")
	}

	if err := st.SetTokenLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.FromRequest(cookieReq()); ok {
		t.Fatal("owner cookie still accepted after token login disabled")
	}
	if p, ok := a.FromRequest(bearerReq()); !ok || !p.Owner {
		t.Fatal("owner Bearer rejected after token login disabled — this breaks bx")
	}

	if err := st.SetTokenLoginDisabled(false); err != nil {
		t.Fatal(err)
	}
	if p, ok := a.FromRequest(cookieReq()); !ok || !p.Owner {
		t.Fatal("owner cookie rejected after re-enabling token login")
	}
}
