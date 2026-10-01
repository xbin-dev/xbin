package broker

// partitioncreds_sso.go — a change of the workspace's single sign-on
// provider, as a credential made for every partition holder bound to it by
// email (plans/partitions/06 §9; PD-07, 10 "rebind SSO"). Whoever controls
// the provider signs in as anyone whose email it asserts, so repointing it
// (PATCH /auth-settings {sso: {kind, issuer, clientId, …}}) is taking over
// every SSO-bound account at once, and is never silent for people who hold
// partitions:
//
//   - policy off: audited (the line names the people), and each is pushed
//     and noticed, as for a sign-in link;
//   - policy credentialResetConfirm on: each one's sign-ins through the
//     changed provider are held — the SSO callback refuses them ("held")
//     until they allow it from a signed-in session, app or device, or 24
//     hours pass; Refuse unbinds their email from their account, so the
//     provider can't sign in as them (their password and devices keep
//     working).
//
// Only the provider's identity counts (kind, issuer, client id): a new
// secret, allowed domains, button label or group rules change nothing of
// whom it vouches for. Clearing SSO holds nothing (no one signs in through
// it). The gate reads the person's file without locks; one it can't read
// lets the sign-in through (logged): it is asked for every SSO sign-in, and
// failing closed there would lock partition holders out of SSO after a
// downgrade — the audit line and the push still went.

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// ssoIdentity is what decides whose sign-ins an SSO configuration vouches
// for; "" for none.
func ssoIdentity(c *users.SSOConfig) string {
	if c == nil || !c.Enabled() {
		return ""
	}
	return c.Kind + "\x00" + c.Issuer + "\x00" + c.ClientID
}

// ssoProviderName names c's provider in a notice.
func ssoProviderName(c *users.SSOConfig) string {
	if c.Issuer != "" {
		return c.Issuer
	}
	return c.Kind
}

// CredentialSSOChanged is PATCH /auth-settings' hook once the SSO
// configuration was stored: when the provider's identity changed, every
// partition holder with an email bound is told, and — policy on — their
// sign-ins through it held. It answers the people it concerned.
func (b *Broker) CredentialSSOChanged(r *http.Request, old, now *users.SSOConfig) []string {
	if b.Users == nil || ssoIdentity(now) == "" || ssoIdentity(now) == ssoIdentity(old) {
		return nil
	}
	p := auth.PrincipalOf(r)
	held := b.Policies().CredentialResetConfirm
	at := peopleNow().UTC()
	var concerned []string
	for _, u := range b.Users.List() {
		by := credentialBy(p, u.ID)
		if u.Email == "" || by == "" {
			continue
		}
		if _, holder := b.credentialPerson(u.ID); !holder {
			continue
		}
		h := heldCredential{ID: newNoticeID(), Kind: credSSO, By: by, At: at, Email: u.Email, Issuer: ssoProviderName(now)}
		ok := held
		if held {
			if err := b.holdCredential(u.ID, h); err != nil {
				// can't hold it: the provider mustn't sign in as them unconfirmed — unbind their email
				slog.Error("partitions: a provider change can't be held for a person; their SSO email is unbound", "user", u.ID, "err", err)
				if _, err := b.applyHeldSSO(u.ID, h, false); err != nil {
					slog.Error("partitions: unbinding a person's SSO email", "user", u.ID, "err", err)
				}
				ok = false
			}
		}
		concerned = append(concerned, u.ID)
		b.credentialNotice(u.ID, h, ok)
	}
	slog.Info("audit", "who", p.From(), "method", "PATCH", "path", "/auth-settings", "sso", "provider changed",
		"issuer", ssoProviderName(now), "partitionHolders", strings.Join(concerned, ","), "held", held)
	return concerned
}

// ssoCredentialText is the notice of a provider change (credentialText's).
func ssoCredentialText(h heldCredential, held bool, when string) (title, text string) {
	title = "A new sign-in provider for your account"
	text = fmt.Sprintf("The workspace's single sign-on provider was changed to %s by %s at %s: whoever controls it can sign in as %s.",
		h.Issuer, h.By, when, h.Email)
	if held {
		text += fmt.Sprintf(" Signing in through it as you works only once you allow it, or from %s if you don't answer. Not you? Refuse it: %s is unbound from your account (your password and devices keep working).",
			h.At.Add(credentialHoldFor).UTC().Format("2006-01-02 15:04 UTC"), h.Email)
	}
	return title, text
}

// applyHeldSSO makes a provider change take effect for user (allow:
// nothing to store — the hold just goes), or refuses it: their email, when
// it is still the one the change was noticed for, is unbound.
func (b *Broker) applyHeldSSO(user string, h heldCredential, allow bool) (bool, error) {
	if allow {
		return true, nil
	}
	u, ok := b.Users.Get(user)
	if !ok || !strings.EqualFold(u.Email, h.Email) {
		return true, nil
	}
	nu := *u
	nu.Email = ""
	_, err := b.Users.Upsert(nu, "")
	return true, err
}

// ssoGate is the users store's SSO gate (SetSSOGate): a person's sign-in
// through the provider waits while a provider change noticed for their
// bound email is held and its 24 hours haven't passed.
func (b *Broker) ssoGate(u users.User) error {
	path, ok := b.personDocPath(u.UID)
	if !ok || u.Email == "" {
		return nil
	}
	d, err := readPersonDocAt(path, u.UID)
	if err != nil {
		slog.Warn("partitions: an SSO sign-in's notices file can't be read; let through", "user", u.ID, "err", err)
		return nil
	}
	if d == nil || d.User != u.ID {
		return nil
	}
	for _, h := range d.Held {
		if h.Kind == credSSO && strings.EqualFold(h.Email, u.Email) && peopleNow().Before(h.At.Add(credentialHoldFor)) {
			return &users.SSOHeldError{Person: u.ID}
		}
	}
	return nil
}
