package auth

import (
	"errors"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Impersonation — "view as user" (docs/auth.md §Viewing the workspace as a
// user, D64). A workspace admin asks for a one-shot ticket naming a user;
// redeeming it TOP-LEVEL in their own browser swaps their cookie for a
// session that authenticates as that user with Impersonator set. The
// principal then reads exactly what the user reads — tiles, screens,
// grants, menus — while the server refuses every write (Principal.ReadOnly:
// the authed middleware, terminals included). Stopping hands the browser
// back to the admin's own session.
//
// Why a ticket rather than a direct cookie swap: the admin console is a
// sandboxed tile (no cookie access), so it can only mint a URL and open it
// in a new tab. The ticket is bound to the admin who minted it — redeeming
// it from a browser signed in as anyone else fails — so the URL delegates
// nothing to whoever else sees it.

// impTicketTTL bounds the mint → open gap; a ticket is single-use.
const impTicketTTL = 2 * time.Minute

type impTicket struct {
	issuer  string // "owner" or the admin's user id
	target  string // the user to view as
	expires time.Time
}

var (
	errImpNotAdmin    = errors.New("only a signed-in workspace admin may view as a user")
	errImpSelf        = errors.New("that is you — nothing to view as")
	errImpNoUser      = errors.New("no such user, or the account is disabled")
	errImpNested      = errors.New("already viewing as a user — exit that view first")
	errImpBadTicket   = errors.New("invalid or expired view-as link")
	errImpWrongIssuer = errors.New("this view-as link belongs to the admin who minted it — open it in their browser")
)

// issuerOf names the human behind an admin principal for ticket binding:
// the root token as "owner", a user by id. Elements without a human ("" —
// an instance token, a cron tick) get nothing: impersonation is a human act.
func issuerOf(p Principal) string {
	switch {
	case p.UserID != "":
		return p.UserID
	case p.Owner:
		return "owner"
	}
	return ""
}

// humanIsAdmin reports whether the human behind p is a workspace admin:
// the root token, or a user with the admin role — looked up by id, since a
// frame-token principal (the admin console calls through its tile frame)
// carries the driving user's id and Access but not the User record.
func (a *Auth) humanIsAdmin(p Principal) bool {
	if p.Owner {
		return true
	}
	if p.User != nil {
		return p.User.IsAdmin()
	}
	if p.UserID == "" {
		return false
	}
	u, ok := a.userSnapshot(p.UserID)
	return ok && u.IsAdmin()
}

// NewImpersonationTicket mints a one-shot ticket for `by` (a workspace
// admin — root, or an admin user, possibly through an admin tile's frame
// token) to view the workspace as target. The ticket is redeemed by the
// same human at GET /login?impersonate=<ticket>.
func (a *Auth) NewImpersonationTicket(by Principal, target string) (string, error) {
	issuer := issuerOf(by)
	if issuer == "" || !a.humanIsAdmin(by) {
		return "", errImpNotAdmin
	}
	if by.ReadOnly() {
		return "", errImpNested
	}
	if target == issuer {
		return "", errImpSelf
	}
	if _, ok := a.userSnapshot(target); !ok {
		return "", errImpNoUser
	}
	tok := util.RandomToken(32)
	now := time.Now()
	a.mu.Lock()
	for id, t := range a.tickets {
		if now.After(t.expires) {
			delete(a.tickets, id)
		}
	}
	a.tickets[tok] = &impTicket{issuer: issuer, target: target, expires: now.Add(impTicketTTL)}
	a.mu.Unlock()
	return tok, nil
}

// RedeemImpersonation consumes a ticket and returns the id of a new
// session that authenticates as the ticket's target with Impersonator set.
// by is the redeeming browser's current principal (its cookie): it must be
// the admin who minted the ticket, not already impersonating. prevCookie is
// that cookie's value — a live session id is remembered so Stop can hand
// the browser back; the bootstrap owner cookie is remembered as such.
func (a *Auth) RedeemImpersonation(ticket string, by Principal, prevCookie, ip string) (string, error) {
	now := time.Now()
	a.mu.Lock()
	t, ok := a.tickets[ticket]
	delete(a.tickets, ticket) // single use, even when the checks below fail
	a.mu.Unlock()
	if !ok || now.After(t.expires) {
		return "", errImpBadTicket
	}
	if by.ReadOnly() {
		return "", errImpNested
	}
	if issuerOf(by) != t.issuer || !a.humanIsAdmin(by) {
		return "", errImpWrongIssuer
	}
	if _, ok := a.userSnapshot(t.target); !ok {
		return "", errImpNoUser
	}
	id := util.RandomToken(32)
	a.mu.Lock()
	a.sweepSessionsLocked(now)
	s := &session{userID: t.target, created: now, lastActive: now, ip: ip, lastIP: ip, impersonator: t.issuer}
	if by.Owner {
		s.restoreOwner = true
	} else if _, live := a.sessions[prevCookie]; live {
		s.restoreSession = prevCookie
	}
	a.sessions[id] = s
	a.warmLocked(ip, now)
	a.mu.Unlock()
	return id, nil
}

// Impersonation reports whether session id is an admin's view of a user,
// and who is looking.
func (a *Auth) Impersonation(id string) (impersonator string, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, found := a.sessions[id]
	if !found || s.impersonator == "" {
		return "", false
	}
	return s.impersonator, true
}

// StopImpersonation ends an impersonation session and says how to hand the
// browser back: the admin's own session id when it is still alive, or
// restoreOwner for a bootstrap-token admin (the server sets the owner
// cookie again). Both empty → the admin has to sign in again. ok is false
// when id is not an impersonation session (a plain logout handles those).
func (a *Auth) StopImpersonation(id string) (restoreSession string, restoreOwner, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, found := a.sessions[id]
	if !found || s.impersonator == "" {
		return "", false, false
	}
	delete(a.sessions, id)
	if _, live := a.sessions[s.restoreSession]; live {
		restoreSession = s.restoreSession
	}
	return restoreSession, s.restoreOwner, true
}
