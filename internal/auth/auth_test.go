package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	tok := a.MintFrameToken("apps/x", time.Minute)
	comp, ok := a.VerifyFrameToken(tok)
	if !ok || comp != "apps/x" {
		t.Fatalf("verify: %q %v", comp, ok)
	}
	if _, ok := a.VerifyFrameToken(tok + "x"); ok {
		t.Fatal("tampered token verified")
	}
	expired := a.MintFrameToken("apps/x", -time.Minute)
	if _, ok := a.VerifyFrameToken(expired); ok {
		t.Fatal("expired token verified")
	}
	// Token from a different secret must not verify.
	b := testAuth(t)
	if _, ok := b.VerifyFrameToken(tok); ok {
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
	r.Header.Set(FrameTokenHeader, a.MintFrameToken("apps/y", time.Minute))
	if p, ok := a.FromRequest(r); !ok || p.Owner || p.Component != "apps/y" {
		t.Fatalf("frame principal: %+v %v", p, ok)
	}
	// Invalid frame token must reject, not silently downgrade to owner.
	r.Header.Set(FrameTokenHeader, "garbage")
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("invalid frame token downgraded to owner")
	}
}
