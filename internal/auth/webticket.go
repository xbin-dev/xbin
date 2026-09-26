package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Signed-in Safari (docs/auth.md §Device login, "Opening the workspace in
// a browser"): the xbin app's device session hands the human over to a
// browser — SFSafariViewController, which has its own cookie jar the app
// can't write — through a one-shot ticket. The app mints it
// (POST /api/xbin/web-ticket), opens <origin>/login?ticket=…&next=…, and the
// redeem swaps it for an ordinary cookie session of the same user.
//
// The ticket is bound to the device session that minted it — its
// generation handle — so it redeems only while that login lives: signing
// the app out, revoking the device, sign-out-everywhere and disabling the
// user all void it. The cookie session it opens stays bound too:
//
//   - it carries the device's id, so removing the device ends it with the
//     device's own sessions (DropDeviceSessions);
//   - signing the app out (POST /logout with the device bearer) ends it
//     with the session that opened it (DropBearerSession);
//   - its login time is the DEVICE LOGIN's, not the redeem's: the enrollment
//     step-up (EnrollFreshLogin) and the absolute TTL both count from the
//     Face ID sign-in, so a handoff can't launder an old device session
//     into a fresh one;
//   - it inherits the device session's cap (an SSO-bound account's window,
//     D93), further capped by the caller's notAfter.
//
// Redeeming is two steps when the browser has no session of its own (login
// CSRF: anyone can mint a ticket for THEIR account and hand the link to
// someone else — a chat message, a QR code — and a link opened from another
// app is as "nobody started it" as the xbin app's own open). The GET spends
// the ticket and trades it for a confirmation (HoldWebTicket): a page that
// names the account, whose Continue button posts a one-shot nonce back from
// the same browser (ConsumeWebConfirm) — only then is the session opened.

const (
	// WebTicketTTL bounds the mint → open gap (the app opens it at once).
	WebTicketTTL = 60 * time.Second
	// WebConfirmTTL bounds the confirmation page → Continue gap (a person
	// reads the page first).
	WebConfirmTTL = 2 * time.Minute
	// maxWebTicketsPerSession: outstanding tickets per device session (the
	// oldest is dropped past it).
	maxWebTicketsPerSession = 4
	// webTicketRate / webTicketWindow: mints per device in a sliding window.
	webTicketRate   = 10
	webTicketWindow = time.Minute
)

var (
	// ErrWebTicketNotDevice: the minting principal is not a live device-key
	// session.
	ErrWebTicketNotDevice = errors.New("only the xbin app's device sign-in can open the workspace in a browser")
	errWebTicketGone      = errors.New("the app session that opened this link has ended — open it again from the app")
)

// WebTicketRateError is a mint refused by the per-device rate: RetryAfter
// says when the next one fits.
type WebTicketRateError struct{ RetryAfter time.Duration }

func (e *WebTicketRateError) Error() string {
	return "too many browser sign-in links from this device — try again shortly"
}

type webTicket struct {
	userID   string
	deviceID string
	handle   string // the minting device session's generation handle
	next     string // the same-origin path to land on (validated by the server)
	created  time.Time
	expires  time.Time
}

// WebTicket is what a redeemed ticket names.
type WebTicket struct {
	UserID   string
	DeviceID string
	Next     string
	handle   string
}

// MintWebTicket issues a one-shot browser sign-in ticket for p, which must
// be a device-key session (Via "device") — not the app's password or SSO
// session, a browser cookie, a tile, a terminal or the owner token. next is
// stored with the ticket (the caller validated it); the redeem lands there.
func (a *Auth) MintWebTicket(p Principal, next string) (string, time.Time, error) {
	handle, ok := strings.CutPrefix(p.Gen, "s.")
	if !ok || p.Via != "device" || p.DeviceID == "" || p.UserID == "" || p.Component != "" || p.ReadOnly() {
		return "", time.Time{}, ErrWebTicketNotDevice
	}
	now := time.Now()
	tok := util.RandomToken(32)
	a.mu.Lock()
	defer a.mu.Unlock()
	sid, ok := a.gens.sessions[handle]
	s := a.sessions[sid]
	if !ok || s == nil || !s.bearer || s.deviceID != p.DeviceID || s.userID != p.UserID || a.expiredLocked(s, now) {
		return "", time.Time{}, ErrWebTicketNotDevice
	}
	a.dev.sweepLocked(now)
	// the per-device rate: a sliding window of recent mints
	recent := a.dev.webMints[p.DeviceID][:0]
	for _, t := range a.dev.webMints[p.DeviceID] {
		if now.Sub(t) < webTicketWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= webTicketRate {
		a.dev.webMints[p.DeviceID] = recent
		return "", time.Time{}, &WebTicketRateError{RetryAfter: webTicketWindow - now.Sub(recent[0])}
	}
	// outstanding per session: the oldest goes
	var oldest string
	n := 0
	for k, t := range a.dev.webTickets {
		if t.handle != handle {
			continue
		}
		n++
		if oldest == "" || t.created.Before(a.dev.webTickets[oldest].created) {
			oldest = k
		}
	}
	if n >= maxWebTicketsPerSession {
		delete(a.dev.webTickets, oldest)
	}
	if len(a.dev.webTickets) >= maxPendingDeviceSecrets {
		return "", time.Time{}, errDeviceBusy
	}
	a.dev.webMints[p.DeviceID] = append(recent, now)
	exp := now.Add(WebTicketTTL)
	a.dev.webTickets[secretKey(tok)] = &webTicket{userID: p.UserID, deviceID: p.DeviceID, handle: handle,
		next: next, created: now, expires: exp}
	return tok, exp, nil
}

// ConsumeWebTicket spends a ticket — single use, whatever happens next —
// and reports what it names; false when unknown, spent or expired.
func (a *Auth) ConsumeWebTicket(ticket string) (WebTicket, bool) {
	if ticket == "" || len(ticket) > 128 {
		return WebTicket{}, false
	}
	k := secretKey(ticket)
	now := time.Now()
	a.mu.Lock()
	t := a.dev.webTickets[k]
	delete(a.dev.webTickets, k)
	a.mu.Unlock()
	if t == nil || now.After(t.expires) {
		return WebTicket{}, false
	}
	return WebTicket{UserID: t.userID, DeviceID: t.deviceID, Next: t.next, handle: t.handle}, true
}

// HoldWebTicket trades a consumed ticket for the confirmation step's nonce:
// one-shot, WebConfirmTTL, naming the same user, device, device session and
// landing path (ConsumeWebConfirm gives it back). Only while the device
// session that minted the ticket lives.
func (a *Auth) HoldWebTicket(t WebTicket) (string, time.Time, error) {
	now := time.Now()
	tok := util.RandomToken(32)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.webParentLocked(t, now); !ok {
		return "", time.Time{}, errWebTicketGone
	}
	a.dev.sweepLocked(now)
	if len(a.dev.webConfirms) >= maxPendingDeviceSecrets {
		return "", time.Time{}, errDeviceBusy
	}
	exp := now.Add(WebConfirmTTL)
	a.dev.webConfirms[secretKey(tok)] = &webTicket{userID: t.UserID, deviceID: t.DeviceID, handle: t.handle,
		next: t.Next, created: now, expires: exp}
	return tok, exp, nil
}

// ConsumeWebConfirm spends a confirmation nonce (HoldWebTicket) — single
// use, whatever happens next — and reports the ticket it stands for; false
// when unknown, spent or expired.
func (a *Auth) ConsumeWebConfirm(nonce string) (WebTicket, bool) {
	if nonce == "" || len(nonce) > 128 {
		return WebTicket{}, false
	}
	k := secretKey(nonce)
	now := time.Now()
	a.mu.Lock()
	t := a.dev.webConfirms[k]
	delete(a.dev.webConfirms, k)
	a.mu.Unlock()
	if t == nil || now.After(t.expires) {
		return WebTicket{}, false
	}
	return WebTicket{UserID: t.userID, DeviceID: t.deviceID, Next: t.next, handle: t.handle}, true
}

// webParentLocked finds the live device session a ticket was minted by
// (caller holds a.mu).
func (a *Auth) webParentLocked(t WebTicket, now time.Time) (string, bool) {
	pid, ok := a.gens.sessions[t.handle]
	parent := a.sessions[pid]
	if t.handle == "" || !ok || parent == nil || !parent.bearer || parent.deviceID != t.DeviceID ||
		parent.userID != t.UserID || a.expiredLocked(parent, now) {
		return "", false
	}
	return pid, true
}

// OpenWebSession opens the browser session a consumed ticket stands for —
// only while the device session that minted it still lives (checked under
// the lock the session is created under, so a sign-out racing the redeem
// wins). notAfter (zero: none) caps it below the device session's own cap.
// Returns the cookie value.
func (a *Auth) OpenWebSession(t WebTicket, ip string, notAfter time.Time) (string, error) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	pid, ok := a.webParentLocked(t, now)
	if !ok {
		return "", errWebTicketGone
	}
	parent := a.sessions[pid]
	capAt := parent.notAfter
	if !notAfter.IsZero() && (capAt.IsZero() || notAfter.Before(capAt)) {
		capAt = notAfter
	}
	a.sweepSessionsLocked(now)
	id := a.addSessionLocked(&session{userID: t.UserID, created: parent.created, lastActive: now, ip: ip, lastIP: ip,
		deviceID: t.DeviceID, notAfter: capAt, opener: pid})
	a.warmLocked(ip, now)
	return id, nil
}
