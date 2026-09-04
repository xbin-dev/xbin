package server

import (
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
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
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
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	if err := st.SetSSO(&users.SSOConfig{
		Kind: "oidc", Preset: "custom", Issuer: f.srv.URL,
		ClientID: "test-client", ClientSecret: "test-secret", AllowedDomains: domains,
	}); err != nil {
		t.Fatal(err)
	}
	// (httptest IdPs are http://127.0.0.1 — SetSSO allows loopback issuers.)
	s := &Server{Auth: a, ExternalURL: "http://xbin.test"}
	return s.Handler(), s, st
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
