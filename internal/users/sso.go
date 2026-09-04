// sso.go — SSO sign-in configuration and IdP-identity → User-row binding.
//
// This implements what invite.go's package doc promised: an SSO identity is
// just another way a credential arrives at the same credential-agnostic User
// row (D22, docs/auth.md). No self-signup: an account exists because an admin
// created it (email binding) or because the admin configured a DOMAIN
// ALLOW-RULE and the IdP asserted a verified email under it (JIT
// provisioning). The protocol dance lives in internal/server/sso.go; this
// file owns config persistence and the user-resolution rules.
package users

import (
	"fmt"
	"net"
	"strings"
)

// SSOConfig is the workspace's single SSO provider configuration, persisted
// in users.json next to tokenLoginDisabled (readable at boot while the vault
// is sealed — a login secret in the vault would be circular).
type SSOConfig struct {
	Kind   string `json:"kind"`             // "oidc" | "github"
	Preset string `json:"preset,omitempty"` // google|keycloak|okta|entra|authentik|github|custom (UI/doc sugar)
	// Issuer is the OIDC issuer URL (ignored for kind "github"). Presets like
	// google imply it; on-prem presets (keycloak, authentik, …) supply their
	// realm/application issuer here.
	Issuer       string `json:"issuer,omitempty"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// AllowedDomains is the admin's domain allow-rule: a verified IdP email
	// under one of these domains may JIT-provision an account. Empty =
	// binding-only (every user pre-created with a matching Email).
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	ButtonLabel    string   `json:"buttonLabel,omitempty"` // login-page button text (preset default when empty)
}

// Enabled reports whether SSO sign-in is configured.
func (c *SSOConfig) Enabled() bool { return c != nil && c.ClientID != "" }

// DomainAllowed reports whether email's domain is under the JIT allow-rule.
func (c *SSOConfig) DomainAllowed(email string) bool {
	if c == nil {
		return false
	}
	_, domain, ok := strings.Cut(strings.ToLower(email), "@")
	if !ok {
		return false
	}
	for _, d := range c.AllowedDomains {
		if domain == d {
			return true
		}
	}
	return false
}

// SSO returns a copy of the current SSO configuration (nil = unconfigured).
func (s *Store) SSO() *SSOConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sso == nil {
		return nil
	}
	c := *s.sso
	c.AllowedDomains = append([]string(nil), s.sso.AllowedDomains...)
	return &c
}

// SetSSO validates and stores the configuration. An empty incoming
// ClientSecret keeps the stored one (the API never echoes secrets back, so
// an edit round-trip must not blank it). nil clears SSO entirely.
func (s *Store) SetSSO(c *SSOConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil {
		s.sso = nil
		return s.persistLocked()
	}
	switch c.Kind {
	case "oidc":
		// https required — except loopback issuers (a Keycloak dev container,
		// tests), where TLS adds nothing.
		if !strings.HasPrefix(c.Issuer, "https://") && !loopbackURL(c.Issuer) {
			return fmt.Errorf("oidc needs an https:// issuer URL")
		}
	case "github":
		c.Issuer = ""
	default:
		return fmt.Errorf("sso kind must be oidc or github")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("clientId required")
	}
	cc := *c
	cc.Issuer = strings.TrimRight(cc.Issuer, "/")
	cc.AllowedDomains = nil
	for _, d := range c.AllowedDomains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d != "" {
			cc.AllowedDomains = append(cc.AllowedDomains, d)
		}
	}
	if cc.ClientSecret == "" && s.sso != nil {
		cc.ClientSecret = s.sso.ClientSecret
	}
	s.sso = &cc
	return s.persistLocked()
}

// FindByEmail resolves a (case-folded) email to its bound user.
func (s *Store) FindByEmail(email string) (User, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return User{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		if u.Email == email {
			return u.Public(), true
		}
	}
	return User{}, false
}

// emailTakenLocked reports whether email is bound to a user other than exceptID.
func (s *Store) emailTakenLocked(email, exceptID string) bool {
	for _, u := range s.byID {
		if u.Email == email && u.ID != exceptID {
			return true
		}
	}
	return false
}

// ProvisionSSO creates an account for a verified IdP email under the domain
// allow-rule (JIT provisioning — the "admin-configured domain rule decides"
// half of no-self-signup). The id derives from the email local-part folded
// into the permanent id charset, suffixed on collision; the account is
// always role `user` (never admin — D52), credential-less (PassHash "" —
// SSO is the credential), and starts with the new-account defaults
// (defaults.go: tiles, create patterns, terminal flags, org memberships)
// on top of defaultTiles. Race-safe: an existing binding for the email wins.
func (s *Store) ProvisionSSO(email, name string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sso.DomainAllowed(email) {
		return User{}, fmt.Errorf("no account for %s and its domain is not in the SSO allow-rule — ask a workspace admin", email)
	}
	for _, u := range s.byID {
		if u.Email == email {
			return u.Public(), nil
		}
	}
	id := deriveSSOID(email)
	if _, taken := s.byID[id]; taken || reservedUserIDs[id] {
		base := id
		for n := 2; ; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
			if _, t := s.byID[id]; !t {
				break
			}
		}
	}
	if err := validID(id); err != nil {
		return User{}, fmt.Errorf("could not derive a user id from %q: %w", email, err)
	}
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	u := &User{ID: id, Name: name, Email: email, Role: RoleUser, Tiles: map[string]string{}, Created: timeNow()}
	s.seedNewUserLocked(u)
	s.byID[id] = u
	s.joinDefaultOrgsLocked(id)
	if err := s.persistLocked(); err != nil {
		delete(s.byID, id)
		return User{}, err
	}
	return u.Public(), nil
}

// loopbackURL reports whether u is http:// on a loopback host.
func loopbackURL(u string) bool {
	rest, ok := strings.CutPrefix(u, "http://")
	if !ok {
		return false
	}
	host := rest
	if i := strings.IndexAny(rest, "/"); i >= 0 {
		host = rest[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// deriveSSOID folds an email local-part into the id charset (validID):
// lowercase, invalid runs → "-", leading/trailing separators trimmed, capped
// at 24 chars (leaving room for collision suffixes), "user" fallback.
func deriveSSOID(email string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	var b strings.Builder
	lastSep := true // also swallows a leading separator
	for _, r := range local {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		switch {
		case ok && (r == '.' || r == '_' || r == '-'):
			if !lastSep {
				b.WriteRune(r)
			}
			lastSep = true
		case ok:
			b.WriteRune(r)
			lastSep = false
		default:
			if !lastSep {
				b.WriteByte('-')
			}
			lastSep = true
		}
	}
	id := strings.Trim(b.String(), "._-")
	if len(id) > 24 {
		id = strings.Trim(id[:24], "._-")
	}
	if id == "" || !userIDRe.MatchString(id) {
		return "user"
	}
	return id
}
