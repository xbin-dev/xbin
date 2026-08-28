// sso.go — SSO sign-in (docs/auth.md §SSO, D51): generic OIDC
// (authorization code + PKCE, ID token verified against the issuer's JWKS)
// with provider presets, plus a GitHub OAuth2 path (GitHub has no OIDC — the
// verified primary email comes from its API). The IdP only ASSERTS identity;
// who gets an account stays the admin's call: a pre-bound User.Email, or the
// admin-configured domain allow-rule (users.ProvisionSSO). Config lives in
// the users store (readable while the vault is sealed at boot).
//
// These handlers are the daemon's only outbound HTTP (discovery, JWKS, token
// exchange — to the configured issuer); the default transport honors
// HTTPS_PROXY.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// ssoPresets maps a preset name to its fixed issuer (when the provider has a
// global one) and default button label. On-prem presets (keycloak, authentik,
// …) take the operator's issuer URL; they exist for the label + docs recipe.
// Apple is deliberately absent: no static client secret (an ES256 JWT minted
// from a .p8 key) and private-relay emails defeat the domain rule — deferred.
var ssoPresets = map[string]struct{ issuer, label string }{
	"google":    {issuer: "https://accounts.google.com", label: "Sign in with Google"},
	"github":    {label: "Sign in with GitHub"},
	"keycloak":  {label: "Sign in with Keycloak"},
	"okta":      {label: "Sign in with Okta"},
	"entra":     {label: "Sign in with Microsoft"},
	"authentik": {label: "Sign in with authentik"},
	"custom":    {label: "Single sign-on"},
}

// githubEndpoint avoids importing x/oauth2/github for two URLs.
var githubEndpoint = oauth2.Endpoint{
	AuthURL:  "https://github.com/login/oauth/authorize",
	TokenURL: "https://github.com/login/oauth/access_token",
}

// ssoIssuer resolves the effective issuer (preset default or configured).
func ssoIssuer(c *users.SSOConfig) string {
	if c.Issuer != "" {
		return c.Issuer
	}
	return ssoPresets[c.Preset].issuer
}

// SSOButtonLabel resolves the login-page button text.
func SSOButtonLabel(c *users.SSOConfig) string {
	if c.ButtonLabel != "" {
		return c.ButtonLabel
	}
	if p, ok := ssoPresets[c.Preset]; ok && p.label != "" {
		return p.label
	}
	return "Single sign-on"
}

// ssoConfig returns the store's SSO config when usable (nil otherwise).
func (s *Server) ssoConfig() *users.SSOConfig {
	if s.Auth == nil || s.Auth.Users == nil {
		return nil
	}
	c := s.Auth.Users.SSO()
	if !c.Enabled() {
		return nil
	}
	return c
}

// SSOReady reports whether SSO login can actually run (configured AND the
// redirect URI is derivable — --external-url is set).
func (s *Server) SSOReady() bool { return s.ssoConfig() != nil && s.ExternalURL != "" }

func (s *Server) ssoRedirectURI() string { return s.ExternalURL + "/login/sso/callback" }

// oauthConfig builds the oauth2 client config for the current SSO config.
// For OIDC kinds the issuer is discovered (and the provider cached per
// issuer) — the first call performs the daemon's first outbound request.
func (s *Server) oauthConfig(ctx context.Context, c *users.SSOConfig) (*oauth2.Config, *oidc.Provider, error) {
	oc := &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  s.ssoRedirectURI(),
	}
	if c.Kind == "github" {
		oc.Endpoint = githubEndpoint
		oc.Scopes = []string{"read:user", "user:email"}
		return oc, nil, nil
	}
	prov, err := s.oidcProvider(ctx, ssoIssuer(c))
	if err != nil {
		return nil, nil, err
	}
	oc.Endpoint = prov.Endpoint()
	oc.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	return oc, prov, nil
}

func (s *Server) oidcProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	s.ssoMu.Lock()
	defer s.ssoMu.Unlock()
	if s.ssoProv != nil && s.ssoProvIssuer == issuer {
		return s.ssoProv, nil
	}
	prov, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	s.ssoProv, s.ssoProvIssuer = prov, issuer
	return prov, nil
}

// --- the state cookie -------------------------------------------------------
// One login round-trip's CSRF state, nonce, and PKCE verifier travel in an
// HMAC-signed, short-TTL, HttpOnly cookie (SameSite=Lax survives the IdP's
// top-level GET redirect back). The key is boot-random: a restart mid-login
// just means clicking the button again.

const ssoStateCookie = "xbin_sso"
const ssoStateTTL = 10 * time.Minute

type ssoState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Exp      int64  `json:"e"`
}

func (s *Server) ssoKey() []byte {
	s.ssoMu.Lock()
	defer s.ssoMu.Unlock()
	if s.ssoStateKey == nil {
		s.ssoStateKey = make([]byte, 32)
		if _, err := rand.Read(s.ssoStateKey); err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
	}
	return s.ssoStateKey
}

func (s *Server) ssoSign(payload []byte) string {
	m := hmac.New(sha256.New, s.ssoKey())
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) ssoVerify(v string) (ssoState, bool) {
	payloadB64, sig, ok := strings.Cut(v, ".")
	if !ok {
		return ssoState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return ssoState{}, false
	}
	m := hmac.New(sha256.New, s.ssoKey())
	m.Write(payload)
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(m.Sum(nil), want) {
		return ssoState{}, false
	}
	var st ssoState
	if json.Unmarshal(payload, &st) != nil || time.Now().Unix() > st.Exp {
		return ssoState{}, false
	}
	return st, true
}

// --- handlers ---------------------------------------------------------------

// handleSSOStart: GET /login/sso — redirect to the IdP's authorization
// endpoint with PKCE + state + nonce.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	c := s.ssoConfig()
	if c == nil || s.ExternalURL == "" {
		http.Error(w, "SSO is not configured (needs auth-settings sso + --external-url)", http.StatusNotFound)
		return
	}
	if !s.loginThrottle.allow(s.ClientIP(r)) {
		http.Error(w, "too many attempts, slow down", http.StatusTooManyRequests)
		return
	}
	oc, _, err := s.oauthConfig(r.Context(), c)
	if err != nil {
		slog.Warn("sso: issuer discovery failed", "err", err)
		http.Redirect(w, r, "/login?sso_err=failed", http.StatusFound)
		return
	}
	st := ssoState{
		State:    util.RandomToken(16),
		Nonce:    util.RandomToken(16),
		Verifier: oauth2.GenerateVerifier(),
		Exp:      time.Now().Add(ssoStateTTL).Unix(),
	}
	payload, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: ssoStateCookie, Value: s.ssoSign(payload), Path: "/login",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil,
		MaxAge: int(ssoStateTTL.Seconds()),
	})
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(st.Verifier)}
	if c.Kind == "oidc" {
		opts = append(opts, oauth2.SetAuthURLParam("nonce", st.Nonce))
	}
	http.Redirect(w, r, oc.AuthCodeURL(st.State, opts...), http.StatusFound)
}

// handleSSOCallback: GET /login/sso/callback — verify everything, resolve
// the asserted email to a User row (binding first, then the domain
// allow-rule), and mint the same session every other login path mints.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if !s.loginThrottle.allow(ip) {
		http.Error(w, "too many attempts, slow down", http.StatusTooManyRequests)
		return
	}
	fail := func(code string, why string, err error) {
		s.loginThrottle.fail(ip)
		slog.Warn("sso: sign-in refused", "why", why, "err", err, "ip", ip)
		http.Redirect(w, r, "/login?sso_err="+code, http.StatusFound)
	}
	c := s.ssoConfig()
	if c == nil || s.ExternalURL == "" {
		http.Error(w, "SSO is not configured", http.StatusNotFound)
		return
	}
	// Clear the state cookie regardless of outcome — it's one-shot.
	cookie, cerr := r.Cookie(ssoStateCookie)
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookie, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true})
	if e := r.URL.Query().Get("error"); e != "" {
		fail("denied", "idp returned "+e, nil)
		return
	}
	if cerr != nil {
		fail("failed", "missing state cookie", cerr)
		return
	}
	st, ok := s.ssoVerify(cookie.Value)
	if !ok || st.State == "" || r.URL.Query().Get("state") != st.State {
		fail("failed", "state mismatch", nil)
		return
	}
	oc, prov, err := s.oauthConfig(r.Context(), c)
	if err != nil {
		fail("failed", "issuer discovery", err)
		return
	}
	tok, err := oc.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		fail("failed", "code exchange", err)
		return
	}

	var email, name string
	if c.Kind == "github" {
		email, name, err = githubIdentity(r.Context(), oc, tok)
		if err != nil {
			fail("failed", "github identity", err)
			return
		}
	} else {
		email, name, err = oidcIdentity(r.Context(), c, prov, tok, st.Nonce)
		if err != nil {
			fail("failed", "id token", err)
			return
		}
	}
	if email == "" {
		fail("failed", "no verified email asserted", nil)
		return
	}

	u, found := s.Auth.Users.FindByEmail(email)
	if !found {
		u, err = s.Auth.Users.ProvisionSSO(email, name)
		if err != nil {
			fail("noaccount", "no binding, provisioning refused", err)
			return
		}
		slog.Info("audit", "who", "user:"+u.ID, "method", "SSO", "path", "/login/sso/callback",
			"status", "provisioned", "email", email)
	}
	if u.Disabled {
		fail("disabled", "account disabled", nil)
		return
	}
	s.loginThrottle.ok(ip)
	setSessionCookie(w, r, s.Auth.NewSession(u.ID, ip))
	slog.Info("audit", "who", "user:"+u.ID, "method", "SSO", "path", "/login/sso/callback", "status", 200)
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcIdentity verifies the ID token (signature via JWKS, issuer, audience,
// expiry, our nonce) and extracts a usable email. An explicit
// email_verified=false always refuses; absent means the IdP doesn't assert
// it (common on on-prem IdPs) and is accepted. Google preset + domain rule
// additionally requires the Workspace hd claim to match the email's domain
// (a consumer Google account can't satisfy it).
func oidcIdentity(ctx context.Context, c *users.SSOConfig, prov *oidc.Provider, tok *oauth2.Token, nonce string) (string, string, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return "", "", fmt.Errorf("no id_token in the token response")
	}
	idt, err := prov.Verifier(&oidc.Config{ClientID: c.ClientID}).Verify(ctx, raw)
	if err != nil {
		return "", "", err
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
		Hd            string `json:"hd"`
		Nonce         string `json:"nonce"`
	}
	if err := idt.Claims(&claims); err != nil {
		return "", "", err
	}
	if claims.Nonce != nonce {
		return "", "", fmt.Errorf("nonce mismatch")
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return "", "", fmt.Errorf("email %s is not verified at the IdP", claims.Email)
	}
	if c.Preset == "google" && len(c.AllowedDomains) > 0 {
		_, domain, _ := strings.Cut(strings.ToLower(claims.Email), "@")
		if !strings.EqualFold(claims.Hd, domain) {
			return "", "", fmt.Errorf("google hd claim %q does not match %q (consumer account?)", claims.Hd, domain)
		}
	}
	return strings.ToLower(claims.Email), claims.Name, nil
}

// githubIdentity fetches the verified primary email (GitHub OAuth2 has no ID
// token) and the display name.
func githubIdentity(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (string, string, error) {
	client := oc.Client(ctx, tok)
	get := func(url string, v any) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(b)))
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := get(githubAPI+"/user/emails", &emails); err != nil {
		return "", "", err
	}
	email := ""
	for _, e := range emails {
		if e.Primary && e.Verified {
			email = strings.ToLower(e.Email)
			break
		}
	}
	if email == "" {
		return "", "", fmt.Errorf("no verified primary email on the GitHub account")
	}
	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := get(githubAPI+"/user", &user); err != nil {
		return "", "", err
	}
	if user.Name == "" {
		user.Name = user.Login
	}
	return email, user.Name, nil
}

// githubAPI is a var so the callback tests can point it at a fake server.
var githubAPI = "https://api.github.com"

// ssoErrText maps ?sso_err= codes to fixed login-page messages (never
// attacker-controlled free text).
func ssoErrText(code string) string {
	switch code {
	case "denied":
		return "Sign-in was cancelled at the identity provider."
	case "noaccount":
		return "No account is bound to that identity and its domain is not on the allow-list — ask a workspace admin."
	case "disabled":
		return "That account is disabled — ask a workspace admin."
	case "failed":
		return "Single sign-on failed — try again."
	}
	return ""
}
