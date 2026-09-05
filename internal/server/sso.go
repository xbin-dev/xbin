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
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/xbin-dev/xbin/internal/events"
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

// googleGroupsScope lets the signing-in user list their own Google Groups
// through the Cloud Identity API (the ID token never carries groups).
const googleGroupsScope = "https://www.googleapis.com/auth/cloud-identity.groups.readonly"

// oauthConfig builds the oauth2 client config for the current SSO config.
// For OIDC kinds the issuer is discovered (and the provider cached per
// issuer) — the first call performs the daemon's first outbound request.
// Group-reading scopes are requested ONLY while group rules exist
// (wantGroups): adding a scope re-prompts consent, and Google rejects
// scopes its project doesn't serve.
func (s *Server) oauthConfig(ctx context.Context, c *users.SSOConfig, wantGroups bool) (*oauth2.Config, *oidc.Provider, error) {
	oc := &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  s.ssoRedirectURI(),
	}
	if c.Kind == "github" {
		oc.Endpoint = githubEndpoint
		oc.Scopes = []string{"read:user", "user:email"}
		if wantGroups {
			oc.Scopes = append(oc.Scopes, "read:org")
		}
		return oc, nil, nil
	}
	prov, err := s.oidcProvider(ctx, ssoIssuer(c))
	if err != nil {
		return nil, nil, err
	}
	oc.Endpoint = prov.Endpoint()
	oc.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	if wantGroups {
		switch {
		case c.Preset == "google":
			oc.Scopes = append(oc.Scopes, googleGroupsScope)
		case c.GroupsScope != "":
			oc.Scopes = append(oc.Scopes, c.GroupsScope)
		}
	}
	return oc, prov, nil
}

// wantGroups reports whether sign-ins should ask the provider for groups.
func (s *Server) wantGroups() bool {
	return s.Auth != nil && s.Auth.Users != nil && s.Auth.Users.SSOGroupRulesExist()
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
	oc, _, err := s.oauthConfig(r.Context(), c, s.wantGroups())
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
	wantGroups := s.wantGroups()
	oc, prov, err := s.oauthConfig(r.Context(), c, wantGroups)
	if err != nil {
		fail("failed", "issuer discovery", err)
		return
	}
	tok, err := oc.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		fail("failed", "code exchange", err)
		return
	}

	// Group fetches get their own deadline: a slow directory API must not
	// hang the sign-in, and a timeout is just a fetch failure (nothing is
	// removed, the user still gets in).
	gctx, cancel := context.WithTimeout(r.Context(), ssoGroupsTimeout)
	defer cancel()
	var ident ssoIdentity
	if c.Kind == "github" {
		ident, err = githubIdentity(gctx, oc, tok, wantGroups)
		if err != nil {
			fail("failed", "github identity", err)
			return
		}
	} else {
		ident, err = oidcIdentity(gctx, c, prov, tok, st.Nonce, wantGroups && c.Preset != "google")
		if err != nil {
			fail("failed", "id token", err)
			return
		}
		if c.Preset == "google" && wantGroups {
			ident.groups, ident.groupsErr = googleGroups(gctx, oc, tok, ident.email)
			ident.groupsFetched = ident.groupsErr == nil
		}
	}
	if ident.email == "" {
		fail("failed", "no verified email asserted", nil)
		return
	}

	u, found := s.Auth.Users.FindByEmail(ident.email)
	if !found {
		u, err = s.Auth.Users.ProvisionSSO(ident.email, ident.name)
		if err != nil {
			fail("noaccount", "no binding, provisioning refused", err)
			return
		}
		slog.Info("audit", "who", "user:"+u.ID, "method", "SSO", "path", "/login/sso/callback",
			"status", "provisioned", "email", ident.email)
	}
	if u.Disabled {
		fail("disabled", "account disabled", nil)
		return
	}
	s.syncGroups(u.ID, ident, wantGroups)
	if err := s.Auth.Users.TouchLogin(u.ID, "sso"); err != nil {
		slog.Warn("sso: last-login stamp failed", "user", u.ID, "err", err)
	}
	s.loginThrottle.ok(ip)
	setSessionCookie(w, r, s.Auth.NewSession(u.ID, ip))
	slog.Info("audit", "who", "user:"+u.ID, "method", "SSO", "path", "/login/sso/callback", "status", 200)
	http.Redirect(w, r, "/", http.StatusFound)
}

// syncGroups reconciles the user's memberships/admin role with the groups
// the provider returned (D53) and audits every change. A fetch failure is
// recorded and changes nothing — unknown is not empty.
func (s *Server) syncGroups(userID string, ident ssoIdentity, wantGroups bool) {
	st := s.Auth.Users
	audit := func(kv ...any) {
		slog.Info("audit", append([]any{"who", "user:" + userID, "method", "SSO", "path", "/login/sso/callback", "status", "group-sync"}, kv...)...)
	}
	if ident.groupsErr != nil {
		_ = st.RecordSSOSyncError(userID, ident.groupsErr.Error())
		slog.Warn("sso: group sync skipped — provider returned no groups", "user", userID, "err", ident.groupsErr)
		audit("change", "fetch-failed", "err", ident.groupsErr.Error())
		return
	}
	if !wantGroups {
		return // no rules anywhere: nothing to reconcile, nothing to record
	}
	rep, err := st.SyncSSOGroups(userID, ident.groups, ident.groupsFetched)
	if err != nil {
		slog.Warn("sso: group sync failed", "user", userID, "err", err)
		return
	}
	for _, ch := range rep.Added {
		audit("change", "added", "org", ch.Org, "level", ch.Level, "create", ch.Create, "admin", ch.Admin, "groups", strings.Join(ch.Groups, ","))
	}
	for _, ch := range rep.Updated {
		audit("change", "updated", "org", ch.Org, "level", ch.Level, "create", ch.Create, "admin", ch.Admin, "groups", strings.Join(ch.Groups, ","))
	}
	for _, org := range rep.Removed {
		audit("change", "removed", "org", org)
	}
	switch {
	case rep.AdminGranted:
		audit("change", "admin-granted")
	case rep.AdminRevoked:
		audit("change", "admin-revoked")
	case rep.AdminBlocked != "":
		audit("change", "admin-revoke-blocked", "why", rep.AdminBlocked)
	}
	if rep.Changed && s.Hub != nil {
		s.Hub.Publish(events.Event{Type: "users"}) // open admin consoles refresh
	}
}

// oidcIdentity verifies the ID token (signature via JWKS, issuer, audience,
// expiry, our nonce) and extracts a usable email. An explicit
// email_verified=false always refuses; absent means the IdP doesn't assert
// it (common on on-prem IdPs) and is accepted. Google preset + domain rule
// additionally requires the Workspace hd claim to match the email's domain
// (a consumer Google account can't satisfy it).
// ssoIdentity is what a provider asserted about the signing-in person. The
// groups half is tri-state: not asked (groupsFetched=false, no error),
// fetched (groupsFetched=true), or asked-and-failed (groupsErr set) — the
// reconcile treats the last as "unknown", never as "none".
type ssoIdentity struct {
	email, name   string
	groups        []string
	groupsFetched bool
	groupsErr     error
}

// ssoGroupsTimeout bounds the extra directory calls a sign-in may make.
const ssoGroupsTimeout = 8 * time.Second

func oidcIdentity(ctx context.Context, c *users.SSOConfig, prov *oidc.Provider, tok *oauth2.Token, nonce string, wantGroups bool) (ssoIdentity, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return ssoIdentity{}, fmt.Errorf("no id_token in the token response")
	}
	idt, err := prov.Verifier(&oidc.Config{ClientID: c.ClientID}).Verify(ctx, raw)
	if err != nil {
		return ssoIdentity{}, err
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
		Hd            string `json:"hd"`
		Nonce         string `json:"nonce"`
	}
	if err := idt.Claims(&claims); err != nil {
		return ssoIdentity{}, err
	}
	if claims.Nonce != nonce {
		return ssoIdentity{}, fmt.Errorf("nonce mismatch")
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return ssoIdentity{}, fmt.Errorf("email %s is not verified at the IdP", claims.Email)
	}
	if c.Preset == "google" && len(c.AllowedDomains) > 0 {
		_, domain, _ := strings.Cut(strings.ToLower(claims.Email), "@")
		if !strings.EqualFold(claims.Hd, domain) {
			return ssoIdentity{}, fmt.Errorf("google hd claim %q does not match %q (consumer account?)", claims.Hd, domain)
		}
	}
	id := ssoIdentity{email: strings.ToLower(claims.Email), name: claims.Name}
	if !wantGroups {
		return id, nil
	}
	// Groups: the configured claim in the ID token, else the same claim from
	// UserInfo (some IdPs only emit it there). Absent from both = the IdP
	// isn't sending it — a recorded failure, never "no groups".
	claim := c.GroupsClaimName()
	var all map[string]json.RawMessage
	if err := idt.Claims(&all); err != nil {
		return ssoIdentity{}, err
	}
	if groups, present, err := groupsClaim(all, claim); present {
		id.groups, id.groupsFetched, id.groupsErr = groups, err == nil, err
		return id, nil
	}
	ui, err := prov.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		id.groupsErr = fmt.Errorf("groups claim %q absent from the ID token and UserInfo failed: %w", claim, err)
		return id, nil
	}
	all = nil
	if err := ui.Claims(&all); err != nil {
		id.groupsErr = fmt.Errorf("userinfo: %w", err)
		return id, nil
	}
	groups, present, err := groupsClaim(all, claim)
	switch {
	case !present:
		id.groupsErr = fmt.Errorf("groups claim %q absent from the ID token and UserInfo — configure the IdP to emit it", claim)
	case err != nil:
		id.groupsErr = err
	default:
		id.groups, id.groupsFetched = groups, true
	}
	return id, nil
}

// groupsClaim reads a groups claim as a string array (a lone string is
// accepted too). present=false when the claim isn't there at all.
func groupsClaim(all map[string]json.RawMessage, claim string) (groups []string, present bool, err error) {
	raw, ok := all[claim]
	if !ok || string(raw) == "null" {
		return nil, false, nil
	}
	if err := json.Unmarshal(raw, &groups); err == nil {
		return groups, true, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, true, nil
	}
	return nil, true, fmt.Errorf("groups claim %q is not a string array", claim)
}

// githubIdentity fetches the verified primary email (GitHub OAuth2 has no ID
// token), the display name, and — with group rules active — the account's
// teams (org/team-slug) and orgs (org), which need the read:org scope.
func githubIdentity(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token, wantGroups bool) (ssoIdentity, error) {
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
		return ssoIdentity{}, err
	}
	email := ""
	for _, e := range emails {
		if e.Primary && e.Verified {
			email = strings.ToLower(e.Email)
			break
		}
	}
	if email == "" {
		return ssoIdentity{}, fmt.Errorf("no verified primary email on the GitHub account")
	}
	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := get(githubAPI+"/user", &user); err != nil {
		return ssoIdentity{}, err
	}
	if user.Name == "" {
		user.Name = user.Login
	}
	id := ssoIdentity{email: email, name: user.Name}
	if !wantGroups {
		return id, nil
	}
	var groups []string
	for page := 1; page <= 10; page++ {
		var teams []struct {
			Slug string `json:"slug"`
			Org  struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		if err := get(fmt.Sprintf("%s/user/teams?per_page=100&page=%d", githubAPI, page), &teams); err != nil {
			id.groupsErr = fmt.Errorf("teams: %w (the authorization may predate the read:org scope — sign out and in again)", err)
			return id, nil
		}
		for _, t := range teams {
			groups = append(groups, strings.ToLower(t.Org.Login+"/"+t.Slug))
		}
		if len(teams) < 100 {
			break
		}
	}
	for page := 1; page <= 10; page++ {
		var orgs []struct {
			Login string `json:"login"`
		}
		if err := get(fmt.Sprintf("%s/user/orgs?per_page=100&page=%d", githubAPI, page), &orgs); err != nil {
			id.groupsErr = fmt.Errorf("orgs: %w", err)
			return id, nil
		}
		for _, o := range orgs {
			groups = append(groups, strings.ToLower(o.Login))
		}
		if len(orgs) < 100 {
			break
		}
	}
	id.groups, id.groupsFetched = groups, true
	return id, nil
}

// githubAPI is a var so the callback tests can point it at a fake server.
var githubAPI = "https://api.github.com"

// googleCloudIdentityAPI likewise (Google Workspace groups, D53).
var googleCloudIdentityAPI = "https://cloudidentity.googleapis.com/v1"

// googleGroups lists the signing-in user's direct Google Groups (as group
// emails) through Cloud Identity — the only way to learn Workspace groups,
// since Google's ID token never carries them. Needs googleGroupsScope AND
// the Cloud Identity API enabled on the OAuth client's GCP project; groups
// the user may not view are silently filtered by Google.
func googleGroups(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token, email string) ([]string, error) {
	client := oc.Client(ctx, tok)
	var groups []string
	pageToken := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"query": {"member_key_id == '" + email + "'"}, "pageSize": {"200"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", googleCloudIdentityAPI+"/groups/-/memberships:searchDirectGroups?"+q.Encode(), nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Memberships []struct {
				GroupKey struct {
					ID string `json:"id"`
				} `json:"groupKey"`
			} `json:"memberships"`
			NextPageToken string `json:"nextPageToken"`
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("cloud identity: %s: %s (is the Cloud Identity API enabled on the OAuth client's project, and was the groups scope consented?)", resp.Status, strings.TrimSpace(string(b)))
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("cloud identity: %w", err)
		}
		for _, m := range body.Memberships {
			if m.GroupKey.ID != "" {
				groups = append(groups, strings.ToLower(m.GroupKey.ID))
			}
		}
		if body.NextPageToken == "" {
			break
		}
		pageToken = body.NextPageToken
	}
	return groups, nil
}

// SSOTestResult is the admin console's "test connection" report.
type SSOTestResult struct {
	OK          bool              `json:"ok"`
	Kind        string            `json:"kind"`
	Preset      string            `json:"preset,omitempty"`
	Issuer      string            `json:"issuer,omitempty"`
	RedirectURI string            `json:"redirectUri"`
	ExternalURL string            `json:"externalUrl"`
	Ready       bool              `json:"ready"`
	Endpoints   map[string]string `json:"endpoints,omitempty"`
	JWKSKeys    int               `json:"jwksKeys,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// SSOTest probes the provider without a user: OIDC discovery + a JWKS fetch
// (the two unauthenticated halves of a sign-in), or GitHub API reachability.
// draft, when given, is the admin's unsaved form (an empty secret means the
// stored one) — so a typo'd issuer is caught before saving. Never mutates
// config; on OIDC success the provider cache is refreshed.
func (s *Server) SSOTest(ctx context.Context, draft *users.SSOConfig) SSOTestResult {
	c := s.ssoConfig()
	if draft != nil {
		d := *draft
		if d.ClientSecret == "" && c != nil {
			d.ClientSecret = c.ClientSecret
		}
		c = &d
	}
	res := SSOTestResult{RedirectURI: s.ssoRedirectURI(), ExternalURL: s.ExternalURL}
	if c == nil || c.ClientID == "" {
		res.Error = "SSO is not configured (no client id)"
		return res
	}
	res.Kind, res.Preset = c.Kind, c.Preset
	if s.ExternalURL == "" {
		res.Warnings = append(res.Warnings, "--external-url (XBIN_EXTERNAL_URL) is not set — the login button stays off until it is")
	}
	rules := s.wantGroups()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch c.Kind {
	case "github":
		req, _ := http.NewRequestWithContext(ctx, "GET", githubAPI+"/", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			res.Error = "GitHub API unreachable: " + err.Error()
			return res
		}
		resp.Body.Close()
		res.Endpoints = map[string]string{"authorization": githubEndpoint.AuthURL, "token": githubEndpoint.TokenURL, "api": githubAPI}
		if rules {
			res.Warnings = append(res.Warnings, "group rules are active: sign-ins request the read:org scope (existing authorizations re-consent once)")
		}
	case "oidc":
		issuer := ssoIssuer(c)
		if strings.HasPrefix(issuer, "http://") {
			res.Warnings = append(res.Warnings, "issuer is plain http — fine for a local IdP, never for production")
		}
		prov, err := oidc.NewProvider(ctx, issuer)
		if err != nil {
			res.Issuer = issuer
			res.Error = "discovery failed: " + err.Error()
			return res
		}
		var ep struct {
			Auth     string `json:"authorization_endpoint"`
			Token    string `json:"token_endpoint"`
			JWKS     string `json:"jwks_uri"`
			UserInfo string `json:"userinfo_endpoint"`
		}
		_ = prov.Claims(&ep)
		res.Issuer = issuer
		res.Endpoints = map[string]string{"authorization": ep.Auth, "token": ep.Token, "jwks": ep.JWKS, "userinfo": ep.UserInfo}
		if ep.JWKS == "" {
			res.Error = "discovery document has no jwks_uri — ID tokens could not be verified"
			return res
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", ep.JWKS, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			res.Error = "JWKS fetch failed: " + err.Error()
			return res
		}
		var jwks struct {
			Keys []json.RawMessage `json:"keys"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwks)
		resp.Body.Close()
		if err != nil || len(jwks.Keys) == 0 {
			res.Error = "JWKS at " + ep.JWKS + " has no usable signing keys"
			return res
		}
		res.JWKSKeys = len(jwks.Keys)
		s.ssoMu.Lock()
		s.ssoProv, s.ssoProvIssuer = prov, issuer
		s.ssoMu.Unlock()
		switch {
		case rules && c.Preset == "google":
			res.Warnings = append(res.Warnings, "group rules are active: sign-ins request "+googleGroupsScope+" and read Google Groups via the Cloud Identity API — enable that API on the OAuth client's project")
		case rules && c.Preset != "google" && ep.UserInfo == "":
			res.Warnings = append(res.Warnings, "no userinfo endpoint: the groups claim must be in the ID token itself")
		}
	default:
		res.Error = "unknown SSO kind " + c.Kind
		return res
	}
	res.Ready = c.Enabled() && s.ExternalURL != ""
	res.OK = true
	return res
}

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
