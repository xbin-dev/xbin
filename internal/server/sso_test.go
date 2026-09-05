package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// fakeIdP is an in-process OIDC provider: discovery, JWKS, and a token
// endpoint minting RS256 ID tokens for whatever identity the test sets.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	email string
	name  string
	nonce string // set by the test from the auth-URL redirect
	// verifiedNull leaves email_verified out entirely (on-prem IdPs often
	// don't map it); verifiedFalse asserts it false.
	verifiedFalse bool
	hd            string // google's hosted-domain claim
	// Group claims (D53): groups go into the ID token under groupsClaim
	// (default "groups") unless omitGroupsClaim; userinfoGroups are served
	// by /userinfo instead (IdPs that only emit groups there).
	groups          []string
	groupsClaim     string
	omitGroupsClaim bool
	userinfoGroups  []string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"userinfo_endpoint":                     f.srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims := map[string]any{"sub": "sub-1", "email": f.email}
		if f.userinfoGroups != nil {
			claims[f.claimName()] = f.userinfoGroups
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(claims)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := &f.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{
			"iss": f.srv.URL, "aud": "test-client", "sub": "sub-1",
			"exp": timeNowUnix() + 300, "iat": timeNowUnix(),
			"email": f.email, "name": f.name, "nonce": f.nonce,
		}
		if f.verifiedFalse {
			claims["email_verified"] = false
		}
		if f.hd != "" {
			claims["hd"] = f.hd
		}
		if f.groups != nil && !f.omitGroupsClaim {
			claims[f.claimName()] = f.groups
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-1", "token_type": "bearer",
			"id_token": signRS256(f.key, claims),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func timeNowUnix() int64 { return time.Now().Unix() }

func (f *fakeIdP) claimName() string {
	if f.groupsClaim == "" {
		return "groups"
	}
	return f.groupsClaim
}

// signRS256 builds a minimal JWS the go-oidc verifier accepts.
func signRS256(key *rsa.PrivateKey, claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ssoTestServer builds a Server with a real auth+users store and SSO
// pointed at the fake IdP, returning the http handler and the store.
func ssoTestServer(t *testing.T, f *fakeIdP, domains []string) (http.Handler, *Server, *users.Store) {
	t.Helper()
	return ssoTestServerCfg(t, f, func(c *users.SSOConfig) { c.AllowedDomains = domains })
}

// ssoTestServerCfg is ssoTestServer with a hook to shape the SSO config
// (preset, group claim, admin groups…) before it is stored.
func ssoTestServerCfg(t *testing.T, f *fakeIdP, mutate func(c *users.SSOConfig)) (http.Handler, *Server, *users.Store) {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	c := &users.SSOConfig{
		Kind: "oidc", Preset: "custom", Issuer: f.srv.URL,
		ClientID: "test-client", ClientSecret: "test-secret",
	}
	if mutate != nil {
		mutate(c)
	}
	if err := st.SetSSO(c); err != nil {
		t.Fatal(err)
	}
	// (httptest IdPs are http://127.0.0.1 — SetSSO allows loopback issuers.)
	s := &Server{Auth: a, ExternalURL: "http://xbin.test"}
	return s.Handler(), s, st
}

// ssoLogin runs a round-trip that must succeed (302 → /).
func ssoLogin(t *testing.T, h http.Handler, f *fakeIdP) {
	t.Helper()
	w := ssoRoundTrip(t, h, f, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("sso login: %d → %q (%s)", w.Code, w.Header().Get("Location"), w.Body.String())
	}
}

// ssoStartScopes returns the scope list /login/sso would request.
func ssoStartScopes(t *testing.T, h http.Handler) []string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login/sso", nil))
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound {
		t.Fatalf("sso start: %d %v", w.Code, err)
	}
	return strings.Fields(loc.Query().Get("scope"))
}

// ssoRoundTrip drives start → (fake IdP) → callback and returns the final
// callback response.
func ssoRoundTrip(t *testing.T, h http.Handler, f *fakeIdP, mutate func(q url.Values, cookies []*http.Cookie) ([]*http.Cookie, string)) *httptest.ResponseRecorder {
	t.Helper()
	// Step 1: /login/sso → capture the state cookie + auth-URL params.
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest("GET", "/login/sso", nil))
	if w1.Code != http.StatusFound {
		t.Fatalf("sso start: %d %s", w1.Code, w1.Body.String())
	}
	loc, err := url.Parse(w1.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	f.nonce = q.Get("nonce") // the IdP echoes it into the ID token
	cookies := (&http.Response{Header: w1.Header()}).Cookies()

	// Step 2: the IdP "redirects back" with code+state.
	cb := url.Values{"code": {"authcode-1"}, "state": {q.Get("state")}}
	cbCookies := cookies
	path := "/login/sso/callback"
	if mutate != nil {
		cbCookies, path = mutate(cb, cookies)
		if path == "" {
			path = "/login/sso/callback"
		}
	}
	r2 := httptest.NewRequest("GET", path+"?"+cb.Encode(), nil)
	for _, c := range cbCookies {
		r2.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	return w2
}

// The full OIDC dance against a fake IdP: JIT provisioning under the domain
// rule, a session cookie at the end, and audit-visible state.
func TestSSOLoginJIT(t *testing.T) {
	f := newFakeIdP(t)
	f.email, f.name = "Jane.Doe@corp.com", "Jane Doe"
	h, _, st := ssoTestServer(t, f, []string{"corp.com"})
	// New-account defaults (D52): the JIT account lands in org corp with
	// the seeded tiles/flags — never as admin.
	if _, err := st.UpsertOrg(users.Org{ID: "corp"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNewUserDefaults(users.NewUserDefaults{
		Tiles: map[string]string{"apps/shared/*": users.LevelWrite}, TermAPI: true,
		Orgs: []users.OrgDefault{{Org: "corp", Level: users.LevelWrite, Create: true}},
	}); err != nil {
		t.Fatal(err)
	}

	w := ssoRoundTrip(t, h, f, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d → %q (%s)", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	var session bool
	for _, c := range (&http.Response{Header: w.Header()}).Cookies() {
		if c.Name == auth.CookieName && c.Value != "" && c.MaxAge >= 0 {
			session = true
		}
	}
	if !session {
		t.Fatal("no session cookie set")
	}
	u, ok := st.FindByEmail("jane.doe@corp.com")
	if !ok || u.ID != "jane.doe" || u.Name != "Jane Doe" {
		t.Fatalf("JIT user: %+v %v", u, ok)
	}
	if u.Role != users.RoleUser || u.Tiles["apps/shared/*"] != users.LevelWrite || !u.TermAPI || u.TermNet {
		t.Fatalf("JIT seed: %+v", u)
	}
	if orgs := st.UserOrgs(u.ID); len(orgs) != 1 || orgs[0].ID != "corp" || !orgs[0].Create || orgs[0].Level != users.LevelWrite || orgs[0].Admin {
		t.Fatalf("JIT default org: %+v", orgs)
	}
	// Second login binds to the same row.
	w2 := ssoRoundTrip(t, h, f, nil)
	if w2.Code != http.StatusFound || w2.Header().Get("Location") != "/" {
		t.Fatalf("re-login: %d", w2.Code)
	}
	if got := len(st.List()); got != 1 {
		t.Fatalf("re-login must not create users: %d", got)
	}
}

// Pre-bound emails resolve to their row (binding beats JIT); off-domain
// unknown emails are refused with the fixed error redirect; disabled
// accounts are refused; unverified emails are refused.
func TestSSOLoginRefusals(t *testing.T) {
	f := newFakeIdP(t)
	h, _, st := ssoTestServer(t, f, nil) // no domain rule

	// Pre-bound email → that account.
	if _, err := st.Upsert(users.User{ID: "boss", Email: "boss@corp.com"}, "password123"); err != nil {
		t.Fatal(err)
	}
	f.email = "boss@corp.com"
	if w := ssoRoundTrip(t, h, f, nil); w.Header().Get("Location") != "/" {
		t.Fatalf("bound email login: %d → %q", w.Code, w.Header().Get("Location"))
	}

	// Unknown email, no domain rule → noaccount error.
	f.email = "stranger@corp.com"
	if w := ssoRoundTrip(t, h, f, nil); !strings.Contains(w.Header().Get("Location"), "sso_err=noaccount") {
		t.Fatalf("stranger: %d → %q", w.Code, w.Header().Get("Location"))
	}

	// Disabled account → disabled error.
	if _, err := st.Upsert(users.User{ID: "boss", Email: "boss@corp.com", Disabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	f.email = "boss@corp.com"
	if w := ssoRoundTrip(t, h, f, nil); !strings.Contains(w.Header().Get("Location"), "sso_err=disabled") {
		t.Fatalf("disabled: %d → %q", w.Code, w.Header().Get("Location"))
	}

	// email_verified=false → refused outright.
	f.verifiedFalse = true
	f.email = "boss@corp.com"
	if w := ssoRoundTrip(t, h, f, nil); !strings.Contains(w.Header().Get("Location"), "sso_err=failed") {
		t.Fatalf("unverified: %d → %q", w.Code, w.Header().Get("Location"))
	}
	f.verifiedFalse = false

	// Tampered state → failed.
	if w := ssoRoundTrip(t, h, f, func(q url.Values, cookies []*http.Cookie) ([]*http.Cookie, string) {
		q.Set("state", "forged")
		return cookies, ""
	}); !strings.Contains(w.Header().Get("Location"), "sso_err=failed") {
		t.Fatalf("forged state must fail")
	}

	// Missing state cookie → failed.
	if w := ssoRoundTrip(t, h, f, func(q url.Values, cookies []*http.Cookie) ([]*http.Cookie, string) {
		return nil, ""
	}); !strings.Contains(w.Header().Get("Location"), "sso_err=failed") {
		t.Fatalf("missing cookie must fail")
	}
}

// The GitHub OAuth2 path: no ID token — identity comes from the API's
// verified primary email.
func TestSSOGitHub(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/emails":
			fmt.Fprint(w, `[{"email":"Octo@Corp.com","primary":true,"verified":true},{"email":"x@y.z","primary":false,"verified":true}]`)
		case "/user":
			fmt.Fprint(w, `{"login":"octo","name":"Octo Cat"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"gh-at","token_type":"bearer"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	oldAPI, oldEP := githubAPI, githubEndpoint
	githubAPI = api.URL
	githubEndpoint.AuthURL, githubEndpoint.TokenURL = api.URL+"/authorize", api.URL+"/token"
	defer func() { githubAPI, githubEndpoint = oldAPI, oldEP }()

	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	if err := st.SetSSO(&users.SSOConfig{Kind: "github", Preset: "github",
		ClientID: "test-client", ClientSecret: "s", AllowedDomains: []string{"corp.com"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Auth: a, ExternalURL: "http://xbin.test"}
	h := s.Handler()

	f := &fakeIdP{} // unused fields; ssoRoundTrip only reads/writes nonce
	w := ssoRoundTrip(t, h, f, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("github login: %d → %q (%s)", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if u, ok := st.FindByEmail("octo@corp.com"); !ok || u.ID != "octo" || u.Name != "Octo Cat" {
		t.Fatalf("github JIT: %+v %v", u, ok)
	}
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func membership(t *testing.T, st *users.Store, org, id string) (users.Member, bool) {
	t.Helper()
	o, ok := st.Org(org)
	if !ok {
		t.Fatalf("no org %s", org)
	}
	return o.Member(id)
}

// Group sync over generic OIDC (D53): rules on orgs turn the groups claim
// into memberships with provenance, a later sign-in without the group
// removes them, the claim is read from UserInfo when the ID token lacks it,
// an absent claim is a recorded failure that removes nothing, and the extra
// scope is requested only while rules exist.
func TestSSOGroupSyncOIDC(t *testing.T) {
	f := newFakeIdP(t)
	f.email, f.name = "jane@corp.com", "Jane"
	f.groups = []string{"Sales", "everyone"}
	h, _, st := ssoTestServerCfg(t, f, func(c *users.SSOConfig) {
		c.AllowedDomains = []string{"corp.com"}
		c.GroupsScope = "groups"
	})
	// No rules: no scope, no groups recorded.
	if hasScope(ssoStartScopes(t, h), "groups") {
		t.Fatal("groups scope requested without rules")
	}
	ssoLogin(t, h, f)
	if u, _ := st.FindByEmail("jane@corp.com"); len(u.SSOGroups) != 0 {
		t.Fatalf("groups recorded without rules: %v", u.SSOGroups)
	}

	if _, err := st.UpsertOrg(users.Org{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgSSOGroups("sales", []users.GroupRule{{Group: "sales", Level: users.LevelTerminal, Create: true}}); err != nil {
		t.Fatal(err)
	}
	if !hasScope(ssoStartScopes(t, h), "groups") {
		t.Fatal("groups scope missing with rules active")
	}
	ssoLogin(t, h, f)
	m, ok := membership(t, st, "sales", "jane")
	if !ok || m.Via != users.MemberViaSSO || m.Level != users.LevelTerminal || !m.Create {
		t.Fatalf("synced membership: %+v %v", m, ok)
	}
	u, _ := st.FindByEmail("jane@corp.com")
	if len(u.SSOGroups) != 2 || u.SSOSyncError != "" || u.LastLoginVia != "sso" || u.LastLogin == 0 {
		t.Fatalf("sign-in facts: %+v", u)
	}
	if got := st.KnownSSOGroups(); len(got) != 2 {
		t.Fatalf("known groups: %v", got)
	}

	// Claim absent everywhere → failure recorded, membership intact.
	f.omitGroupsClaim = true
	ssoLogin(t, h, f)
	u, _ = st.FindByEmail("jane@corp.com")
	if u.SSOSyncError == "" || !strings.Contains(u.SSOSyncError, "absent") {
		t.Fatalf("absent claim must be recorded: %+v", u)
	}
	if _, ok := membership(t, st, "sales", "jane"); !ok {
		t.Fatal("fetch failure must not remove memberships")
	}
	// UserInfo fallback with a custom claim name.
	f.omitGroupsClaim = false
	f.groups = nil
	f.userinfoGroups = []string{"sales"}
	f.groupsClaim = "memberOf"
	h2, _, st2 := ssoTestServerCfg(t, f, func(c *users.SSOConfig) {
		c.AllowedDomains = []string{"corp.com"}
		c.GroupsClaim = "memberOf"
	})
	st2.UpsertOrg(users.Org{ID: "sales"})
	st2.SetOrgSSOGroups("sales", []users.GroupRule{{Group: "sales"}})
	ssoLogin(t, h2, f)
	if m, ok := membership(t, st2, "sales", "jane"); !ok || m.Via != users.MemberViaSSO || m.Level != users.LevelRead {
		t.Fatalf("userinfo fallback: %+v %v", m, ok)
	}
	// Group gone → membership gone; error cleared.
	f.userinfoGroups = []string{"other"}
	ssoLogin(t, h2, f)
	if _, ok := membership(t, st2, "sales", "jane"); ok {
		t.Fatal("membership must go with the group")
	}
	if u, _ := st2.FindByEmail("jane@corp.com"); u.SSOSyncError != "" || len(u.SSOGroups) != 1 {
		t.Fatalf("after success: %+v", u)
	}
}

// Workspace admin by group rule, through the real callback: granted with
// provenance, revoked when the group goes, blocked for the last admin.
func TestSSOGroupSyncAdminRole(t *testing.T) {
	f := newFakeIdP(t)
	f.email, f.name = "ops@corp.com", "Ops"
	f.groups = []string{"xbin-admins"}
	h, _, st := ssoTestServerCfg(t, f, func(c *users.SSOConfig) {
		c.AllowedDomains = []string{"corp.com"}
		c.AdminGroups = []string{"xbin-admins"}
	})
	ssoLogin(t, h, f)
	u, _ := st.FindByEmail("ops@corp.com")
	if u.Role != users.RoleAdmin || u.RoleVia != users.MemberViaSSO {
		t.Fatalf("admin by rule: %+v", u)
	}
	// Only admin → revoke blocked. (An EMPTY claim — nil would omit it, which
	// is "unknown" and changes nothing by design.)
	f.groups = []string{}
	ssoLogin(t, h, f)
	if u, _ = st.FindByEmail("ops@corp.com"); u.Role != users.RoleAdmin {
		t.Fatalf("last admin must keep the role: %+v", u)
	}
	// Another admin exists → revoked next time.
	if _, err := st.Upsert(users.User{ID: "boss", Role: users.RoleAdmin}, "password123"); err != nil {
		t.Fatal(err)
	}
	ssoLogin(t, h, f)
	if u, _ = st.FindByEmail("ops@corp.com"); u.Role != users.RoleUser || u.RoleVia != "" {
		t.Fatalf("revoke: %+v", u)
	}
}

// GitHub: teams (org/slug) and orgs become groups when rules exist;
// read:org is requested only then; an API refusal is a recorded failure.
func TestSSOGitHubGroups(t *testing.T) {
	teamsStatus := 200
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/emails":
			fmt.Fprint(w, `[{"email":"octo@corp.com","primary":true,"verified":true}]`)
		case "/user":
			fmt.Fprint(w, `{"login":"octo","name":"Octo Cat"}`)
		case "/user/teams":
			if teamsStatus != 200 {
				http.Error(w, `{"message":"Resource not accessible"}`, teamsStatus)
				return
			}
			fmt.Fprint(w, `[{"slug":"Platform","organization":{"login":"Acme"}}]`)
		case "/user/orgs":
			fmt.Fprint(w, `[{"login":"Acme"}]`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"gh-at","token_type":"bearer"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	oldAPI, oldEP := githubAPI, githubEndpoint
	githubAPI = api.URL
	githubEndpoint.AuthURL, githubEndpoint.TokenURL = api.URL+"/authorize", api.URL+"/token"
	defer func() { githubAPI, githubEndpoint = oldAPI, oldEP }()

	a, _ := auth.Load(t.TempDir(), false)
	st, _ := users.Open(t.TempDir())
	a.SetUsers(st)
	if err := st.SetSSO(&users.SSOConfig{Kind: "github", Preset: "github",
		ClientID: "test-client", ClientSecret: "s", AllowedDomains: []string{"corp.com"}}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Auth: a, ExternalURL: "http://xbin.test"}).Handler()
	f := &fakeIdP{}
	if hasScope(ssoStartScopes(t, h), "read:org") {
		t.Fatal("read:org requested without rules")
	}
	st.UpsertOrg(users.Org{ID: "infra"})
	st.SetOrgSSOGroups("infra", []users.GroupRule{{Group: "acme/platform", Level: users.LevelTerminal, Create: true}})
	if !hasScope(ssoStartScopes(t, h), "read:org") {
		t.Fatal("read:org missing with rules")
	}
	ssoLogin(t, h, f)
	if m, ok := membership(t, st, "infra", "octo"); !ok || m.Via != users.MemberViaSSO {
		t.Fatalf("team → org: %+v %v", m, ok)
	}
	if u, _ := st.FindByEmail("octo@corp.com"); len(u.SSOGroups) != 2 { // acme/platform + acme
		t.Fatalf("github groups: %v", u.SSOGroups)
	}
	teamsStatus = 403
	ssoLogin(t, h, f)
	u, _ := st.FindByEmail("octo@corp.com")
	if u.SSOSyncError == "" {
		t.Fatal("403 must be recorded")
	}
	if _, ok := membership(t, st, "infra", "octo"); !ok {
		t.Fatal("failure must not remove the membership")
	}
}

// Google preset: groups come from Cloud Identity (never a claim); the scope
// is requested only with rules; keys are group emails; paging works.
func TestSSOGoogleGroups(t *testing.T) {
	var gotQuery string
	ci := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/groups/-/memberships:searchDirectGroups" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			fmt.Fprint(w, `{"memberships":[{"groupKey":{"id":"Sales@corp.com"}}],"nextPageToken":"p2"}`)
			return
		}
		fmt.Fprint(w, `{"memberships":[{"groupKey":{"id":"all@corp.com"}}]}`)
	}))
	defer ci.Close()
	old := googleCloudIdentityAPI
	googleCloudIdentityAPI = ci.URL
	defer func() { googleCloudIdentityAPI = old }()

	f := newFakeIdP(t)
	f.email, f.name, f.hd = "jane@corp.com", "Jane", "corp.com"
	f.groups = []string{"ignored-claim"} // google has no groups claim; must not be read
	h, _, st := ssoTestServerCfg(t, f, func(c *users.SSOConfig) {
		c.Preset = "google"
		c.Issuer = f.srv.URL // ssoIssuer prefers an explicit issuer
		c.AllowedDomains = []string{"corp.com"}
	})
	if hasScope(ssoStartScopes(t, h), googleGroupsScope) {
		t.Fatal("cloud-identity scope requested without rules")
	}
	st.UpsertOrg(users.Org{ID: "sales"})
	st.SetOrgSSOGroups("sales", []users.GroupRule{{Group: "sales@corp.com", Level: users.LevelWrite}})
	if !hasScope(ssoStartScopes(t, h), googleGroupsScope) {
		t.Fatal("cloud-identity scope missing with rules")
	}
	ssoLogin(t, h, f)
	if !strings.Contains(gotQuery, "jane@corp.com") {
		t.Fatalf("query: %q", gotQuery)
	}
	if m, ok := membership(t, st, "sales", "jane"); !ok || m.Level != users.LevelWrite {
		t.Fatalf("google group → org: %+v %v", m, ok)
	}
	u, _ := st.FindByEmail("jane@corp.com")
	if len(u.SSOGroups) != 2 || u.SSOGroups[0] != "all@corp.com" {
		t.Fatalf("google groups (paged, lowercased, sorted): %v", u.SSOGroups)
	}
}

// SSOTest: discovery + JWKS against the fake, a bogus issuer, and a draft.
func TestSSOTestProbe(t *testing.T) {
	f := newFakeIdP(t)
	_, s, st := ssoTestServer(t, f, nil)
	res := s.SSOTest(context.Background(), nil)
	if !res.OK || res.JWKSKeys != 1 || res.Endpoints["userinfo"] == "" || !res.Ready {
		t.Fatalf("probe: %+v", res)
	}
	bad := s.SSOTest(context.Background(), &users.SSOConfig{Kind: "oidc", Issuer: "http://127.0.0.1:1", ClientID: "x"})
	if bad.OK || bad.Error == "" {
		t.Fatalf("bogus issuer: %+v", bad)
	}
	st.SetSSO(nil)
	if r := s.SSOTest(context.Background(), nil); r.OK || !strings.Contains(r.Error, "not configured") {
		t.Fatalf("unconfigured: %+v", r)
	}
	// A draft is testable before anything is saved.
	if r := s.SSOTest(context.Background(), &users.SSOConfig{Kind: "oidc", Issuer: f.srv.URL, ClientID: "x"}); !r.OK {
		t.Fatalf("draft: %+v", r)
	}
}

// SSO-only mode: a correct password is refused for non-admins (with the
// fixed message), admins still get in, the login page carries the note,
// and last-login is stamped for password logins.
func TestPasswordLoginDisabledForNonAdmins(t *testing.T) {
	f := newFakeIdP(t)
	h, _, st := ssoTestServer(t, f, nil)
	for _, u := range []users.User{{ID: "ann"}, {ID: "boss", Role: users.RoleAdmin}} {
		if _, err := st.Upsert(u, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	login := func(id string) *httptest.ResponseRecorder {
		form := url.Values{"username": {id}, "password": {"password123"}}
		r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := login("ann"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "single sign-on") {
		t.Fatalf("non-admin password login: %d %s", w.Code, w.Body.String())
	}
	if w := login("boss"); w.Code != http.StatusFound {
		t.Fatalf("admin break-glass: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Get("boss"); u.LastLoginVia != "password" || u.LastLogin == 0 {
		t.Fatalf("last login: %+v", u)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(w.Body.String(), "reserved for workspace admins") {
		t.Fatal("login page must explain SSO-only mode")
	}
	// Wrong password still fails generically (no role leak).
	form := url.Values{"username": {"ann"}, "password": {"nope"}}
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
}
