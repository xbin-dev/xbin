package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// Preview sessions — "see what the user sees" (D234, docs/auth.md
// §Previews, docs/protocol/preview.md). xbind renders a tile or a screen for
// a person in a headless browser of its own; that browser acts through a
// one-shot session that is the PERSON'S OWN AUTHORITY REDUCED TO READ.
//
// It rides the view-as machinery (impersonate.go): the session's
// impersonator is the reserved PreviewViewer, so the principal is ReadOnly
// and every credential derived from it — frame tokens, tile-origin cookies,
// asset tokens, path tickets — carries the mark and stays read-only, with no
// new claim in any of their wire forms. What differs from an admin's view:
//
//   - nobody else is looking. ViewAs() is false, so the gates that keep a
//     person's private data from an admin's eyes (a partitioned tile, PD-08)
//     open: the session reaches the person's own partitions, read-only;
//   - it is never handed to anyone: the id leaves this package only towards
//     the render's socket (internal/server/preview.go), which sets it on
//     requests server-side. The browser, the caller and the page never hold
//     it;
//   - it is not a sign-in: not listed (Sessions), not persisted
//     (framegens.go), with no way out to "stop" (StopImpersonation), and it
//     ends with the render — or at its ttl.
//
// The reserved viewer is nobody: it cannot be a user id (users.validID:
// [a-z0-9][a-z0-9._-]*, and reserved by name), is not "owner", and neither
// mints nor redeems a view-as (impersonate.go). Code outside this package
// never reads Principal.Impersonator to learn WHO is looking without first
// asking ViewAs() — TestImpersonatorReadsAreClassified holds that line.
const PreviewViewer = "~preview"

// Preview reports a preview session's principal (or a tile credential
// derived from one): read-only, the person themselves.
func (p Principal) Preview() bool { return p.Impersonator == PreviewViewer }

// ViewAs reports that SOMEONE ELSE is looking through UserID's eyes: an
// admin's view-as session (D64). False for a preview, which is the person's
// own read. Gates about privacy ask this; gates about writes ask ReadOnly.
func (p Principal) ViewAs() bool { return p.Impersonator != "" && p.Impersonator != PreviewViewer }

var errPreviewNoUser = errors.New("no such user, or the account is disabled")

// NewPreviewSession opens a preview session of userID that lives at most
// ttl. The caller ends it with EndPreviewSession; expiry is the backstop.
func (a *Auth) NewPreviewSession(userID string, ttl time.Duration) (string, error) {
	if _, ok := a.userSnapshot(userID); !ok {
		return "", errPreviewNoUser
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addSessionLocked(&session{userID: userID, created: now, lastActive: now,
		impersonator: PreviewViewer, notAfter: now.Add(ttl)}), nil
}

// EndPreviewSession drops a preview session: every frame token, tile cookie
// and asset token minted under it stops verifying with it. A no-op for any
// other session.
func (a *Auth) EndPreviewSession(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[id]; ok && s.impersonator == PreviewViewer {
		a.dropSessionLocked(id)
	}
}

// SetRequestSession makes r carry session id as its one session cookie, the
// way this server reads it on r (SessionCookieName), dropping whatever
// session cookie the client sent: how the preview listener gives its
// browser's requests their identity without the browser holding it.
func (a *Auth) SetRequestSession(r *http.Request, id string) {
	StripRequestSession(r)
	r.AddCookie(&http.Cookie{Name: a.SessionCookieName(r), Value: id})
}

// StripRequestSession removes the session cookies (either name) from r.
func StripRequestSession(r *http.Request) {
	cs := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name == CookieName || c.Name == hostSessionCookie || strings.EqualFold(c.Name, CookieName) {
			continue
		}
		r.AddCookie(c)
	}
}
