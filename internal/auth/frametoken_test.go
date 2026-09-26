package auth

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

func bindingAuth(t *testing.T) *Auth {
	t.Helper()
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "bob", Tiles: map[string]string{"apps/x": users.LevelWrite}},
	} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	return a
}

func frameOK(a *Auth, tok string) bool {
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, tok)
	_, ok := a.FromRequest(r)
	return ok
}

func principalOf(t *testing.T, a *Auth, cookie, bearer string) Principal {
	t.Helper()
	r := httptest.NewRequest("GET", "/x", nil)
	if cookie != "" {
		r.AddCookie(cookieFor(cookie))
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	p, ok := a.FromRequest(r)
	if !ok {
		t.Fatalf("principal (cookie %v, bearer %v) refused", cookie != "", bearer != "")
	}
	return p
}

// legacyFrameToken builds the pre-binding 4-field form an older xbind minted.
func legacyFrameToken(a *Auth, comp, uid string, exp time.Time) string {
	payload := fmt.Sprintf("%s|%s|%d", base64.RawURLEncoding.EncodeToString([]byte(comp)),
		base64.RawURLEncoding.EncodeToString([]byte(uid)), exp.Unix())
	return payload + "|" + a.sign(payload)
}

// Logout kills the frame tokens a session minted — and their renewals,
// which keep the binding (plans/native.md §20 row 2).
func TestFrameTokenDiesWithSession(t *testing.T) {
	a := bindingAuth(t)
	sid := a.NewSession("bob", "")
	other := a.NewSession("bob", "")
	p := principalOf(t, a, sid, "")
	tok := a.MintFrameTokenFor(p, "apps/x", time.Minute)
	if n := len(strings.Split(tok, "|")); n != 5 || !strings.HasPrefix(strings.Split(tok, "|")[3], "s.") {
		t.Fatalf("minted token is not session-bound: %q", tok)
	}
	sibling := a.MintFrameTokenFor(principalOf(t, a, other, ""), "apps/x", time.Minute)
	// The tile renews with its token alone: the renewal copies the binding.
	fp := func(tok string) Principal {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set(FrameTokenHeader, tok)
		fp, ok := a.FromRequest(r)
		if !ok {
			t.Fatal("frame token refused")
		}
		return fp
	}(tok)
	if fp.Gen != p.Gen || fp.Component != "apps/x" || fp.UserID != "bob" {
		t.Fatalf("frame principal: %+v", fp)
	}
	renewed := a.MintFrameTokenFor(fp, "apps/x", time.Minute)
	if strings.Split(renewed, "|")[3] != strings.Split(tok, "|")[3] {
		t.Fatal("renewal changed the binding")
	}
	a.DropSession(sid) // logout
	if frameOK(a, tok) || frameOK(a, renewed) {
		t.Fatal("frame tokens outlived their session")
	}
	if !frameOK(a, sibling) {
		t.Fatal("another session's frames died with this one")
	}
	// Expiry of the session kills them the same way.
	a.mu.Lock()
	a.sessions[other].lastActive = time.Now().Add(-a.sessionIdleTTL - time.Minute)
	a.mu.Unlock()
	if frameOK(a, sibling) {
		t.Fatal("frame token outlived its expired session")
	}
}

// Sign-out-everywhere (and disable/delete, which call it) kills every frame
// token of the user — session-bound and user-bound alike — and nobody else's.
func TestFrameTokenSignOutEverywhere(t *testing.T) {
	a := bindingAuth(t)
	sid := a.NewSession("bob", "")
	bound := a.MintFrameTokenFor(principalOf(t, a, sid, ""), "apps/x", time.Minute)
	userBound := a.MintFrameToken("apps/x", "bob", time.Minute) // no session behind it
	alice := a.MintFrameTokenFor(principalOf(t, a, a.NewSession("alice", ""), ""), "apps/x", time.Minute)
	if !frameOK(a, bound) || !frameOK(a, userBound) {
		t.Fatal("fresh tokens refused")
	}
	a.DropUserSessions("bob")
	if frameOK(a, bound) || frameOK(a, userBound) {
		t.Fatal("frame tokens survived sign-out-everywhere")
	}
	if !frameOK(a, alice) {
		t.Fatal("another user's frames died")
	}
	// New tokens bind to the new generation and work.
	if !frameOK(a, a.MintFrameToken("apps/x", "bob", time.Minute)) {
		t.Fatal("post-signout token refused")
	}
	// Disabling refuses at once; the server's disable path also signs the
	// user out, so re-enabling does not resurrect the old tokens.
	tok := a.MintFrameToken("apps/x", "bob", time.Minute)
	u, _ := a.Users.Get("bob")
	u.Disabled = true
	if _, err := a.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if frameOK(a, tok) {
		t.Fatal("frame token of a disabled user")
	}
	a.DropUserSessions("bob")
	u.Disabled = false
	if _, err := a.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if frameOK(a, tok) {
		t.Fatal("re-enabling resurrected a frame token")
	}
}

// Pre-upgrade 4-field tokens verify until expiry and renew into bound ones
// tied to the user's current generation.
func TestLegacyFrameTokenUpgrade(t *testing.T) {
	a := bindingAuth(t)
	legacy := legacyFrameToken(a, "apps/x", "bob", time.Now().Add(10*time.Minute))
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, legacy)
	fp, ok := a.FromRequest(r)
	if !ok || fp.Component != "apps/x" || fp.UserID != "bob" || fp.Gen != "" {
		t.Fatalf("legacy token: %+v %v", fp, ok)
	}
	renewed := a.MintFrameTokenFor(fp, "apps/x", time.Minute)
	if gen := strings.Split(renewed, "|")[3]; gen != a.UserGen("bob") {
		t.Fatalf("legacy renewal bound to %q, want the user generation %q", gen, a.UserGen("bob"))
	}
	if !frameOK(a, renewed) {
		t.Fatal("renewed token refused")
	}
	a.DropUserSessions("bob")
	if frameOK(a, renewed) {
		t.Fatal("upgraded token survived sign-out-everywhere")
	}
	// Owner-attributed legacy tokens renew onto the owner token's generation.
	ol := legacyFrameToken(a, "apps/x", "", time.Now().Add(10*time.Minute))
	r.Header.Set(FrameTokenHeader, ol)
	op, ok := a.FromRequest(r)
	if !ok {
		t.Fatal("legacy owner token refused")
	}
	if !frameOK(a, a.MintFrameTokenFor(op, "apps/x", time.Minute)) {
		t.Fatal("owner legacy renewal refused")
	}
	// Expired legacy tokens refuse; so does one expiring past the window —
	// no xbind that ran before this boot could have minted it.
	if frameOK(a, legacyFrameToken(a, "apps/x", "bob", time.Now().Add(-time.Second))) {
		t.Fatal("expired legacy token")
	}
	if frameOK(a, legacyFrameToken(a, "apps/x", "bob", time.Now().Add(legacyFrameWindow+time.Minute))) {
		t.Fatal("legacy token beyond the upgrade window")
	}
	// Stripping the generation off a bound token forges nothing.
	parts := strings.Split(a.MintFrameToken("apps/x", "bob", time.Minute), "|")
	if frameOK(a, strings.Join([]string{parts[0], parts[1], parts[2], parts[4]}, "|")) {
		t.Fatal("bound token downgraded to legacy")
	}
	if frameOK(a, strings.Join([]string{parts[0], parts[1], parts[2], "u.x.0", parts[4]}, "|")) {
		t.Fatal("generation swapped")
	}
}

// Owner-token frames die when the owner token is rotated.
func TestFrameTokenOwnerRotation(t *testing.T) {
	a := bindingAuth(t)
	owner := principalOf(t, a, "", a.OwnerTokenValue())
	tok := a.MintFrameTokenFor(owner, "apps/x", time.Minute)
	if !frameOK(a, tok) {
		t.Fatal("owner frame refused")
	}
	if _, err := a.RotateOwnerToken(); err != nil {
		t.Fatal(err)
	}
	if frameOK(a, tok) {
		t.Fatal("owner frame survived rotation")
	}
}

// An admin's view-as session keeps its tiles read-only even without the
// cookie (sandboxed frames never carry it).
func TestFrameTokenViewAsReadOnly(t *testing.T) {
	a := bindingAuth(t)
	adminSid := a.NewSession("alice", "")
	admin := principalOf(t, a, adminSid, "")
	tk, err := a.NewImpersonationTicket(admin, "bob")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := a.RedeemImpersonation(tk, admin, adminSid, "")
	if err != nil {
		t.Fatal(err)
	}
	view := principalOf(t, a, sid, "")
	tok := a.MintFrameTokenFor(view, "apps/x", time.Minute)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, tok)
	fp, ok := a.FromRequest(r)
	if !ok || fp.Impersonator != "alice" || !fp.ReadOnly() {
		t.Fatalf("cookie-less view-as frame: %+v %v", fp, ok)
	}
	if _, _, ok := a.StopImpersonation(sid); !ok {
		t.Fatal("stop")
	}
	if frameOK(a, tok) {
		t.Fatal("view-as frames outlived the view")
	}
}

// App sessions: bearer-only, the same human principal as a cookie session,
// device-bound ones dropped with their device.
func TestBearerSessions(t *testing.T) {
	a := bindingAuth(t)
	bs := a.NewBearerSession("bob", "dev-1", "10.0.0.9")
	if time.Until(bs.ExpiresIdle) > a.sessionIdleTTL || time.Until(bs.ExpiresMax) < a.sessionAbsTTL-time.Minute {
		t.Fatalf("deadlines: %+v", bs)
	}
	p := principalOf(t, a, "", bs.Token)
	cookie := principalOf(t, a, a.NewSession("bob", ""), "")
	if p.Via != "device" || p.DeviceID != "dev-1" || p.UserID != "bob" || p.User == nil || p.Owner || p.Component != "" {
		t.Fatalf("device principal: %+v", p)
	}
	for _, tile := range []string{"apps/x", "apps/other", "tiles/admin"} {
		if p.CanReadTile(tile) != cookie.CanReadTile(tile) || p.CanWriteTile(tile) != cookie.CanWriteTile(tile) ||
			p.CanTerminalTile(tile) != cookie.CanTerminalTile(tile) {
			t.Fatalf("device session reaches %s differently from a cookie session", tile)
		}
	}
	if p.IsAdmin() || p.CanTerminal() != cookie.CanTerminal() {
		t.Fatal("device session is not the same human principal")
	}
	// Transport-bound: the bearer isn't a cookie, the cookie isn't a bearer.
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(cookieFor(bs.Token))
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("app session accepted as a cookie")
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+a.NewSession("bob", ""))
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("browser session accepted as a bearer")
	}
	// Its frames are bound to it; revoking the device ends both.
	tok := a.MintFrameTokenFor(p, "apps/x", time.Minute)
	app := a.NewBearerSession("bob", "", "")
	if ap := principalOf(t, a, "", app.Token); ap.Via != "app" || ap.DeviceID != "" {
		t.Fatalf("app principal: %+v", ap)
	}
	if n := a.DropDeviceSessions("dev-1"); n != 1 {
		t.Fatalf("dropped %d", n)
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+bs.Token)
	if _, ok := a.FromRequest(r); ok || frameOK(a, tok) {
		t.Fatal("revoked device's session or frames survived")
	}
	principalOf(t, a, "", app.Token) // the password-login session is not the device's
	// Sessions list names the channel.
	var vias []string
	for _, si := range a.Sessions() {
		vias = append(vias, si.Via)
	}
	if got := strings.Join(vias, ","); !strings.Contains(got, "app") || !strings.Contains(got, "session") {
		t.Fatalf("sessions via: %s", got)
	}
	// Sign-out of the app: only bearer sessions drop by token.
	drop := func(tok string) bool { _, _, ok := a.DropBearerSession(tok); return ok }
	if drop(cookieSID(t, a)) || !drop(app.Token) || drop(app.Token) {
		t.Fatal("DropBearerSession")
	}
	// A disabled user's app session refuses.
	bs2 := a.NewBearerSession("bob", "", "")
	u, _ := a.Users.Get("bob")
	u.Disabled = true
	_, _ = a.Users.Upsert(*u, "")
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+bs2.Token)
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("disabled user's app session")
	}
}

func cookieSID(t *testing.T, a *Auth) string {
	t.Helper()
	return a.NewSession("alice", "")
}

func cookieFor(sid string) *http.Cookie { return &http.Cookie{Name: CookieName, Value: sid} }

// CredentialLive answers "does the login behind this generation still
// live" for state that must end with it (push registrations) — and, unlike
// a frame token's use, doesn't count as the login's activity.
func TestCredentialLive(t *testing.T) {
	a, _ := assetTestAuth(t)
	sid := a.NewSession("ana", "192.0.2.1")
	sv, _ := a.sessionUser(sid, "", false)
	if !a.CredentialLive(sv.gen, "ana") || a.CredentialLive(sv.gen, "bob") {
		t.Fatal("a live session's generation, for its user only")
	}
	a.mu.Lock()
	before := a.sessions[sid].lastActive.Add(-time.Hour)
	a.sessions[sid].lastActive = before
	a.mu.Unlock()
	a.CredentialLive(sv.gen, "ana")
	a.mu.RLock()
	slid := a.sessions[sid].lastActive != before
	a.mu.RUnlock()
	if slid {
		t.Fatal("asking slid the session's idle window")
	}
	if !a.CredentialLive(a.UserGen("ana"), "ana") || !a.CredentialLive(a.ownerGen(), "") || a.CredentialLive("", "ana") || a.CredentialLive("x|y", "ana") {
		t.Fatal("user and owner generations; garbage")
	}
	a.DropSession(sid)
	if a.CredentialLive(sv.gen, "ana") {
		t.Fatal("a dropped session's generation")
	}
	old := a.UserGen("ana")
	a.DropUserSessions("ana")
	if a.CredentialLive(old, "ana") {
		t.Fatal("the user's generation after sign-out-everywhere")
	}
}
