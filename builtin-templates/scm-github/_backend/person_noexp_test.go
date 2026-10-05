package main

import (
	"strings"
	"testing"
	"time"
)

// With "Expire user authorization tokens" off GitHub answers a token that
// never expires and no refresh token: nothing could rotate or end it, so the
// sign-in is refused and the grant revoked at once.
func TestSigninRefusedWithoutExpiry(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.gh.noExpiry = true
	s := e.user("alice")
	h := s.routes()
	var st signinState
	decode(t, e.call(h, pageC("alice"), "POST", "/scm/signin", nil), &st)
	if st.State != "pending" {
		t.Fatalf("signin: %+v", st)
	}
	e.gh.approve("octocat", "approved")
	e.clock.advance(6e9)
	decode(t, e.call(h, pageC("alice"), "GET", "/scm/signin/"+st.Signin.PollID, nil), &st)
	if st.State != "error" || !strings.Contains(st.Error, "Expire user authorization tokens") {
		t.Fatalf("a sign-in without a refresh token: %+v", st)
	}
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("the sign-in was kept")
	}
	if acc, _, _ := s.userTokens(); acc.Token != "" {
		t.Fatal("the pair was kept in the vault")
	}
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	for _, u := range e.gh.userTokens {
		if u.login == "octocat" && !u.revoked {
			t.Fatal("the never-expiring token still works at GitHub")
		}
	}
}

// legacyPair turns alice's sign-in into one kept before such pairs were
// refused: a never-expiring access token, no refresh token.
func legacyPair(t *testing.T, e *env, s *srv) vaultTok {
	t.Helper()
	acc, _, _ := s.userTokens()
	e.gh.mu.Lock()
	e.gh.userTokens[acc.Token].exp = e.clock.now().Add(100 * 365 * 24 * time.Hour)
	for rt, l := range e.gh.refresh {
		if l == "octocat" {
			delete(e.gh.refresh, rt)
		}
	}
	e.gh.mu.Unlock()
	if err := s.storeUser(oauthTokenResp{AccessToken: newSecret(acc.Token)}, "octocat", e.gh.users["octocat"], e.clock.now()); err != nil {
		t.Fatal(err)
	}
	return acc
}

// Such a pair's epoch ends: the grant is revoked with the still-live access
// token before the sign-in is cleared — nothing handed out stays valid.
func TestLegacyPairWithoutRefreshRevoked(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	acc := legacyPair(t, e, s)
	m := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read"})
	e.gh.mu.Lock()
	e.gh.userTokens[m["token"].(string)].exp = e.clock.now().Add(100 * 365 * 24 * time.Hour)
	e.gh.mu.Unlock()
	e.clock.advance(7*time.Hour + 30*time.Minute)
	// GitHub refuses the revocation: answered, nothing cleared.
	e.gh.fail("DELETE /applications/", 1, 500, nil, `{"message":"boom"}`)
	if r := e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}); r.Code < 500 {
		t.Fatalf("a refused revocation: %d %s", r.Code, r.Body)
	}
	if s.personRecord() == nil || e.userTok(acc.Token).revoked {
		t.Fatal("a refused revocation cleared the sign-in")
	}
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 409, "signin")
	if s.personRecord() != nil {
		t.Fatal("the sign-in was kept")
	}
	if !e.userTok(acc.Token).revoked || !e.userTok(m["token"].(string)).revoked {
		t.Fatal("the never-expiring tokens still work at GitHub")
	}
}

// Forget with no refresh token in the vault revokes the grant with the
// access token as it is, live or expired, and clears; with the refresh
// token itself expired it clears without trying a refresh.
func TestForgetWithoutRefresh(t *testing.T) {
	e := newEnv(t)
	e.setup()
	grants := func() int { return e.gh.count("DELETE /applications/" + e.gh.clientID + "/grant") }
	refreshes := func() int { return e.gh.count("POST /login/oauth/access_token") }

	// Live access token, no refresh token: revoked with it.
	s := e.signIn("alice", "octocat")
	u := s.routes()
	acc := legacyPair(t, e, s)
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if !e.userTok(acc.Token).revoked || s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("Forget without a refresh token didn't revoke the grant with the access token")
	}

	// Expired access token, no refresh token: revoked as it is (GitHub's
	// 404 counts as revoked), no refresh tried, cleared.
	s = e.signIn("alice", "octocat")
	u = s.routes()
	acc, _, _ = s.userTokens()
	if err := s.vault.Delete(vaultUserRefresh); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(9 * time.Hour)
	g, rf := grants(), refreshes()
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if grants() != g+1 || refreshes() != rf {
		t.Fatalf("grant revocations %d→%d, refreshes %d→%d", g, grants(), rf, refreshes())
	}
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("the identity is still kept")
	}

	// The refresh token itself expired: nothing to revoke with, no refresh
	// tried, cleared.
	s = e.signIn("alice", "octocat")
	u = s.routes()
	e.clock.advance(185 * 24 * time.Hour)
	g, rf = grants(), refreshes()
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if grants() != g || refreshes() != rf {
		t.Fatalf("an expired refresh token: grant revocations %d→%d, refreshes %d→%d", g, grants(), rf, refreshes())
	}
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("the identity is still kept")
	}
}
