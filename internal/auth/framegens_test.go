package auth

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// bindingAuthAt is bindingAuth over a given workspace dir (a restart = a
// second Load of the same dir, same user store).
func bindingAuthAt(t *testing.T, dir string, st *users.Store) *Auth {
	t.Helper()
	a, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	return a
}

func framePrincipalOf(t *testing.T, a *Auth, tok string) Principal {
	t.Helper()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, tok)
	p, ok := a.FromRequest(r)
	if !ok {
		t.Fatal("frame token refused")
	}
	return p
}

// A tile renewing its token alone keeps the login behind it from idling
// out (tile requests carry no cookie) — the absolute TTL still caps it.
// The review's repro: idle TTL 12 s, renewals every 4 s died at 16 s.
func TestFrameUseKeepsSessionAlive(t *testing.T) {
	a := bindingAuth(t)
	sid := a.NewSession("bob", "")
	tok := a.MintFrameTokenFor(principalOf(t, a, sid, ""), "apps/x", 15*time.Minute)
	// Deterministic: three rounds of "11 h idle, then the tile renews" —
	// 33 h past a 12 h idle TTL, and the session (cookie too) lives on.
	for i := 0; i < 3; i++ {
		a.TestAgeSession(sid, 0, a.sessionIdleTTL-time.Hour)
		tok = a.MintFrameTokenFor(framePrincipalOf(t, a, tok), "apps/x", 15*time.Minute) // the renewal
		a.mu.RLock()
		idle := time.Since(a.sessions[sid].lastActive)
		a.mu.RUnlock()
		if idle > time.Minute {
			t.Fatalf("round %d: frame use did not slide the session (idle %v)", i, idle)
		}
	}
	principalOf(t, a, sid, "") // the cookie session is alive too
	// Without frame use, idling out still ends it.
	a.TestAgeSession(sid, 0, a.sessionIdleTTL+time.Minute)
	if frameOK(a, tok) {
		t.Fatal("an idle session's frames survived")
	}
	// The absolute TTL caps sliding.
	sid = a.NewSession("bob", "")
	tok = a.MintFrameTokenFor(principalOf(t, a, sid, ""), "apps/x", 15*time.Minute)
	a.TestAgeSession(sid, a.sessionAbsTTL+time.Minute, 0)
	if frameOK(a, tok) {
		t.Fatal("frame use outlived the absolute TTL")
	}

	// Real time, short TTLs (XBIN_SESSION_IDLE_TTL below the 1-minute grain
	// must still slide).
	a.sessionIdleTTL = 800 * time.Millisecond
	sid = a.NewSession("bob", "")
	tok = a.MintFrameTokenFor(principalOf(t, a, sid, ""), "apps/x", time.Minute)
	for i := 0; i < 6; i++ { // 1.5 s of renewals past a 0.8 s idle TTL
		time.Sleep(250 * time.Millisecond)
		tok = a.MintFrameTokenFor(framePrincipalOf(t, a, tok), "apps/x", time.Minute)
	}
	time.Sleep(time.Second)
	if frameOK(a, tok) {
		t.Fatal("an idle session's frames survived (real time)")
	}
}

// Frame generations persist: after a restart the sessions are gone (sign in
// again), but open tiles keep renewing until their login would have
// expired; sign-out-everywhere, device revocation and logout are final.
func TestFrameGensSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Role: users.RoleAdmin}, {ID: "bob"}} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a := bindingAuthAt(t, dir, st)
	sid := a.NewSession("bob", "")
	sessTok := a.MintFrameTokenFor(principalOf(t, a, sid, ""), "apps/x", 15*time.Minute)
	userTok := a.MintFrameToken("apps/x", "bob", 15*time.Minute)
	dev := a.NewBearerSession("bob", "dev-1", "")
	devTok := a.MintFrameTokenFor(principalOf(t, a, "", dev.Token), "apps/x", 15*time.Minute)
	gone := a.NewSession("bob", "")
	goneTok := a.MintFrameTokenFor(principalOf(t, a, gone, ""), "apps/x", 15*time.Minute)
	a.DropSession(gone) // logout before the restart
	stale := a.NewSession("bob", "")
	staleTok := a.MintFrameTokenFor(principalOf(t, a, stale, ""), "apps/x", 15*time.Minute)
	a.TestAgeSession(stale, 0, a.sessionIdleTTL-time.Minute) // idles out soon after the restart
	adminSid := a.NewSession("alice", "")
	admin := principalOf(t, a, adminSid, "")
	tk, _ := a.NewImpersonationTicket(admin, "bob")
	view, err := a.RedeemImpersonation(tk, admin, adminSid, "")
	if err != nil {
		t.Fatal(err)
	}
	viewTok := a.MintFrameTokenFor(principalOf(t, a, view, ""), "apps/x", 15*time.Minute)
	a.FlushGens()

	b := bindingAuthAt(t, dir, st) // the restart
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(cookieFor(sid))
	if _, ok := b.FromRequest(r); ok {
		t.Fatal("a session survived the restart (only its tiles should)")
	}
	for name, tok := range map[string]string{"session": sessTok, "user": userTok, "device": devTok, "view-as": viewTok} {
		if !frameOK(b, tok) {
			t.Fatalf("%s-bound frame token died with the restart", name)
		}
	}
	if frameOK(b, goneTok) {
		t.Fatal("a logged-out session's frames came back after the restart")
	}
	if fp := framePrincipalOf(t, b, viewTok); !fp.ReadOnly() || fp.Impersonator != "alice" {
		t.Fatalf("view-as frames lost read-only across the restart: %+v", fp)
	}
	// Renewal keeps the (orphaned) binding and slides it.
	renewed := b.MintFrameTokenFor(framePrincipalOf(t, b, sessTok), "apps/x", 15*time.Minute)
	if strings.Split(renewed, "|")[3] != strings.Split(sessTok, "|")[3] || !frameOK(b, renewed) {
		t.Fatal("renewal after the restart changed or lost the binding")
	}
	// The orphan still idles out: stale was 1 min from its idle limit.
	b.mu.Lock()
	for _, s := range b.gens.orphans {
		if s.lastActive.Before(time.Now().Add(-b.sessionIdleTTL + 2*time.Minute)) {
			s.lastActive = time.Now().Add(-b.sessionIdleTTL - time.Second)
		}
	}
	b.mu.Unlock()
	if frameOK(b, staleTok) {
		t.Fatal("an orphan outlived its idle TTL")
	}
	// Revoking the device ends its orphan; sign-out-everywhere ends the
	// rest — and stays in force across the next restart.
	b.DropDeviceSessions("dev-1")
	if frameOK(b, devTok) || !frameOK(b, sessTok) {
		t.Fatal("device revocation: wrong orphans ended")
	}
	b.DropUserSessions("bob")
	for _, tok := range []string{sessTok, renewed, userTok, viewTok} {
		if frameOK(b, tok) {
			t.Fatal("a frame token survived sign-out-everywhere after a restart")
		}
	}
	fresh := b.MintFrameToken("apps/x", "bob", 15*time.Minute)
	c := bindingAuthAt(t, dir, st) // no flush: the drop saved synchronously
	if frameOK(c, userTok) || frameOK(c, sessTok) || !frameOK(c, fresh) {
		t.Fatal("sign-out-everywhere was not persisted")
	}

	// An unreadable state file starts fresh: every bound token dies.
	if err := os.WriteFile(filepath.Join(dir, ".xbin", gensFileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := bindingAuthAt(t, dir, st)
	if frameOK(d, fresh) {
		t.Fatal("a corrupt state file kept old generations")
	}
	if fi, err := os.Stat(filepath.Join(dir, ".xbin", gensFileName)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".xbin", gensFileName))
	if strings.Contains(string(raw), sid) || strings.Contains(string(raw), dev.Token) {
		t.Fatal("a session credential was persisted")
	}
}

// The state file never holds a session id — only generation handles.
func TestFrameGensFileHoldsNoCredential(t *testing.T) {
	dir := t.TempDir()
	a, _ := Load(dir, false)
	sid := a.NewSession("bob", "")
	bs := a.NewBearerSession("bob", "dev-1", "")
	a.FlushGens()
	raw, err := os.ReadFile(filepath.Join(dir, ".xbin", gensFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sid) || strings.Contains(string(raw), bs.Token) || !strings.Contains(string(raw), `"dev-1"`) {
		t.Fatalf("state file: %s", raw)
	}
}

// Sign-out-everywhere voids pending enrollment codes and app tickets: a code
// minted just before can't enroll a device after.
func TestSignOutVoidsPendingDeviceSecrets(t *testing.T) {
	a := bindingAuth(t)
	code, _, _ := a.MintEnrollCode("bob", "https://x.example")
	other, _, _ := a.MintEnrollCode("alice", "https://x.example")
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	tk, _ := a.MintAppTicket("bob", challenge)
	a.DropUserSessions("bob")
	if _, _, ok := a.RedeemEnrollCode(code); ok {
		t.Fatal("an enrollment code outlived sign-out-everywhere")
	}
	if _, ok := a.RedeemAppTicket(tk, "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); ok {
		t.Fatal("an app ticket outlived sign-out-everywhere")
	}
	if uid, _, ok := a.RedeemEnrollCode(other); !ok || uid != "alice" {
		t.Fatal("another user's code was voided")
	}
}

// LoginTime: the sign-in time of a principal's own live login session only.
func TestLoginTime(t *testing.T) {
	a := bindingAuth(t)
	sid := a.NewSession("bob", "")
	p := principalOf(t, a, sid, "")
	if at, ok := a.LoginTime(p); !ok || time.Since(at) > time.Minute {
		t.Fatalf("fresh session: %v %v", at, ok)
	}
	a.TestAgeSession(sid, 20*time.Minute, 0)
	if at, ok := a.LoginTime(p); !ok || time.Since(at) < 19*time.Minute {
		t.Fatalf("aged session: %v %v", at, ok)
	}
	frame := framePrincipalOf(t, a, a.MintFrameTokenFor(p, "apps/x", time.Minute))
	owner := principalOf(t, a, "", a.OwnerTokenValue())
	if _, ok := a.LoginTime(frame); ok {
		t.Fatal("a tile has no login time")
	}
	if _, ok := a.LoginTime(owner); ok {
		t.Fatal("the owner token has no login time")
	}
	a.DropSession(sid)
	if _, ok := a.LoginTime(p); ok {
		t.Fatal("an ended session has no login time")
	}
}

// A capped app session (SSO-only device login) reports and enforces the cap.
func TestBearerSessionCap(t *testing.T) {
	a := bindingAuth(t)
	capAt := time.Now().Add(time.Hour)
	bs := a.NewBearerSessionUntil("bob", "dev-1", "", capAt)
	if bs.ExpiresMax.Unix() != capAt.Unix() || bs.ExpiresIdle.After(bs.ExpiresMax) {
		t.Fatalf("deadlines: %+v", bs)
	}
	principalOf(t, a, "", bs.Token)
	past := a.NewBearerSessionUntil("bob", "dev-1", "", time.Now().Add(-time.Second))
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+past.Token)
	if _, ok := a.FromRequest(r); ok {
		t.Fatal("a session past its cap authenticated")
	}
}
