package auth

import (
	"net"
	"net/http"
	"net/netip"
	"time"
)

// --- recently-authenticated IPs (the /c/ subresource gate) ---

// authedIPTTL is how long a client IP counts as "recently authenticated"
// after its last successful auth — the second half of the /c/ credential-
// less subresource rule (plans/auth.md §6): the Fetch-Metadata fingerprint
// alone is spoofable by any non-browser client, so the exception also
// requires a source IP with a real login behind it. That kills drive-by
// internet scanners while staying invisible to browsers, whose tile
// subresource loads always follow an authenticated document load from the
// same IP (and open tiles renew frame tokens every few minutes).
const authedIPTTL = time.Hour

// SetClientIP installs the server's trusted-proxy-aware client-IP resolver
// (used for warm-IP attribution and per-session IP records). Nil-safe:
// RemoteAddr is the fallback.
func (a *Auth) SetClientIP(f func(*http.Request) string) { a.clientIP = f }

func (a *Auth) ipOf(r *http.Request) string {
	if a.clientIP != nil {
		return a.clientIP(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// noteAuthed records a successful authentication from r's client IP.
func (a *Auth) noteAuthed(r *http.Request) {
	a.mu.Lock()
	a.warmLocked(a.ipOf(r), time.Now())
	a.mu.Unlock()
}

// warmLocked marks ip freshly authenticated (caller holds a.mu). Only real
// IPs enter the set: gateway (unix-socket) peers authenticate constantly
// with a RemoteAddr like "@", and warming that would be meaningless noise —
// the /c/ gate only ever looks up TCP clients' addresses.
func (a *Auth) warmLocked(ip string, now time.Time) {
	if _, err := netip.ParseAddr(ip); err != nil {
		return
	}
	if len(a.warm) >= 4096 { // paranoia bound — prune before growing past it
		a.sweepWarmLocked(now)
	}
	a.warm[ip] = now
}

// RecentlyAuthed reports whether ip authenticated successfully within
// authedIPTTL. Deliberately does NOT slide the window: renewal comes only
// from real auths (noteAuthed) — if a credential-less gate check renewed
// it, a single login would keep an IP warm forever for anyone polling /c/
// from the same egress, even after every session from it was revoked.
// Browsers never notice: open tiles renew frame tokens every few minutes
// and the shell polls with credentials, all real auths that re-warm.
func (a *Auth) RecentlyAuthed(ip string) bool {
	if ip == "" {
		return false
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.warm[ip]
	if !ok {
		return false
	}
	if now.Sub(t) > authedIPTTL {
		delete(a.warm, ip)
		return false
	}
	return true
}

// sweepWarmLocked drops stale warm-IP entries (caller holds a.mu).
func (a *Auth) sweepWarmLocked(now time.Time) {
	for ip, t := range a.warm {
		if now.Sub(t) > authedIPTTL {
			delete(a.warm, ip)
		}
	}
}
