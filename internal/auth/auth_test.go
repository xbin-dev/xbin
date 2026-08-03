package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
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
	r.Header.Set("Authorization", "Bearer "+a.OwnerTokenValue())
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
	r.AddCookie(&http.Cookie{Name: CookieName, Value: a.OwnerTokenValue()})
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

// Browser-plane isolation (plans/auth.md §6): a frame token ALONE — no
// cookie, the situation inside a sandboxed tile frame — authenticates the
// tile. Present-but-invalid still rejects, never fails open.
func TestFrameTokenOnlyAuth(t *testing.T) {
	a := testAuth(t)

	r := httptest.NewRequest("GET", "/api/xbin/frame-token?component=apps/y", nil)
	r.Header.Set(FrameTokenHeader, a.MintFrameToken("apps/y", "", time.Minute))
	p, ok := a.FromRequest(r)
	if !ok || p.Component != "apps/y" || p.Via != "frame" || p.Owner {
		t.Fatalf("token-only frame principal: %+v %v", p, ok)
	}

	// WS path: the token rides a query param (browsers can't set WS headers).
	r = httptest.NewRequest("GET", "/ws/events?frame="+a.MintFrameToken("apps/z", "", time.Minute), nil)
	if p, ok := a.FromRequest(r); !ok || p.Component != "apps/z" {
		t.Fatalf("query-token frame principal: %+v %v", p, ok)
	}

	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, "garbage")
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("garbage token-only request accepted")
	}
}

// The authed-middleware cookie gate: requests showing the opaque-origin tile
// fingerprint (Fetch Metadata) must have the cookie dropped so a tile can't
// ride the ambient human session; chrome and legacy clients are unaffected.
func TestTileContext(t *testing.T) {
	req := func(method, path string, h map[string]string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"sandboxed tile fetch", req("GET", "/api/xbin/components", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}), true},
		{"opaque-origin engine variant (same-site)", req("GET", "/api/xbin/components", map[string]string{
			"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}), true},
		{"sandboxed tile WS", req("GET", "/ws/events", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "websocket", "Sec-Fetch-Dest": "empty"}), true},
		{"sandboxed tile form POST to API", req("POST", "/api/apps/x/do", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}), true},
		{"sandboxed tile subresource", req("GET", "/c/apps/x/app.js", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "script"}), true},
		{"shell fetch", req("POST", "/api/xbin/grants", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}), false},
		{"chrome tile fetch", req("GET", "/api/xbin/orgs", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors"}), false},
		{"external link navigation", req("GET", "/", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}), false},
		{"iframe document navigation", req("GET", "/c/apps/x/", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}), false},
		{"legacy browser (no metadata)", req("GET", "/api/xbin/components", nil), false},
		{"bearer tooling", req("GET", "/api/xbin/status", map[string]string{
			"Authorization": "Bearer x", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors"}), false},
		{"direct GET nav to API (browser URL bar)", req("GET", "/api/xbin/status", map[string]string{
			"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}), false},
	}
	for _, c := range cases {
		if got := TileContext(c.r); got != c.want {
			t.Errorf("%s: TileContext=%v, want %v", c.name, got, c.want)
		}
	}
}

// WithoutCookie must strip only the cookie credential, leaving frame tokens
// (header and query) and other headers intact for principal resolution.
func TestWithoutCookie(t *testing.T) {
	a := testAuth(t)
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: a.OwnerTokenValue()})
	r.Header.Set(FrameTokenHeader, a.MintFrameToken("apps/y", "", time.Minute))
	r2 := WithoutCookie(r)
	if _, err := r2.Cookie(CookieName); err == nil {
		t.Fatal("cookie survived WithoutCookie")
	}
	p, ok := a.FromRequest(r2)
	if !ok || p.Component != "apps/y" {
		t.Fatalf("token-only after WithoutCookie: %+v %v", p, ok)
	}
	if _, err := r.Cookie(CookieName); err != nil {
		t.Fatal("original request mutated")
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
		r.AddCookie(&http.Cookie{Name: CookieName, Value: a.OwnerTokenValue()})
		return r
	}
	bearerReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+a.OwnerTokenValue())
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

// A session principal carries the ownership-model Access snapshot (D24/D25):
// org membership levels flow through the Can*Tile gates, ownership confers
// terminal, and set-conferred term flags surface via CanTermNet.
func TestSessionPrincipalOrgAccess(t *testing.T) {
	a := testAuth(t)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	if _, err := st.Upsert(users.User{ID: "bob", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "bob", Level: users.LevelTerminal, Create: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/crm", "org:sales"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPermissionSet("dev", users.PermissionSet{TermNet: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgSets("sales", []string{"dev"}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: a.NewSession("bob")})
	p, ok := a.FromRequest(r)
	if !ok || p.UserID != "bob" || p.Access == nil {
		t.Fatalf("session principal: %+v %v", p, ok)
	}
	if !p.CanReadTile("apps/crm") || !p.CanWriteTile("apps/crm") ||
		!p.CanTerminalTile("apps/crm") || !p.CanTerminal() {
		t.Fatal("org member level must flow through the principal gates")
	}
	if p.CanReadTile("apps/chat") || p.CanCreateTile("apps/new") {
		t.Fatal("org level must stay on org-owned tiles; no personal create patterns")
	}
	if !p.CanTermNet() || p.CanTermAPI() {
		t.Fatal("term flags must union from attached permission sets")
	}
	if p.IsAdmin() {
		t.Fatal("org member is not a workspace admin")
	}
}
