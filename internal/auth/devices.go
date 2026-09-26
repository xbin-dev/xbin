package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Device login (plans/native.md §5, docs/auth.md §Device login,
// native/spec/device-login.md). The native app holds a P-256 key in the
// Secure Enclave per workspace; the server holds its public key on the user
// row (internal/users/devices.go). Three short-lived, single-use secrets
// live here, in memory (an xbind restart drops them — every one is minutes
// long, and sessions die on restart anyway):
//
//   - enrollment codes: minted by a signed-in session, redeemed once by the
//     app to register its key for that user (5 min);
//   - challenges: a nonce per login attempt, bound to one device (60 s);
//   - app SSO tickets: the one-shot result of an SSO sign-in the app started,
//     redeemed with the PKCE verifier only the app knows (2 min).

const (
	EnrollCodeTTL = 5 * time.Minute
	ChallengeTTL  = 60 * time.Second
	AppTicketTTL  = 2 * time.Minute

	// EnrollFreshLogin: minting an enrollment code — a credential that
	// outlives the session minting it — needs a login this recent, or the
	// password again (step-up; a stolen session alone can't mint a device).
	EnrollFreshLogin = 10 * time.Minute

	// Bounds on the in-memory maps: each is swept of expired entries first;
	// still full → the mint is refused (a flood can't grow memory).
	maxPendingDeviceSecrets = 4096
	maxChallengesPerDevice  = 8
)

// DeviceLoginContext is the first line of the signed message — the domain
// separator (the key signs nothing else, but a versioned label keeps it so).
const DeviceLoginContext = "xbin-device-login-v1"

var (
	errDeviceBusy = errors.New("too many pending device logins — try again shortly")
)

type enrollCode struct {
	userID  string
	origin  string
	expires time.Time
}

type deviceChallenge struct {
	deviceID string
	created  time.Time
	expires  time.Time
}

type appTicket struct {
	userID    string
	challenge string // base64url(sha256(verifier)), as the app sent it
	expires   time.Time
}

type deviceState struct {
	codes      map[string]*enrollCode      // sha256(code) → pending enrollment
	challenges map[string]*deviceChallenge // nonce → outstanding challenge
	tickets    map[string]*appTicket       // sha256(ticket) → pending app sign-in
	webTickets map[string]*webTicket       // sha256(ticket) → pending browser sign-in (webticket.go)
	webMints   map[string][]time.Time      // device id → recent web-ticket mints (the rate)
}

func newDeviceState() deviceState {
	return deviceState{codes: map[string]*enrollCode{}, challenges: map[string]*deviceChallenge{},
		tickets: map[string]*appTicket{}, webTickets: map[string]*webTicket{}, webMints: map[string][]time.Time{}}
}

// sweepLocked drops expired entries (caller holds Auth.mu).
func (d *deviceState) sweepLocked(now time.Time) {
	for k, c := range d.codes {
		if now.After(c.expires) {
			delete(d.codes, k)
		}
	}
	for k, c := range d.challenges {
		if now.After(c.expires) {
			delete(d.challenges, k)
		}
	}
	for k, t := range d.tickets {
		if now.After(t.expires) {
			delete(d.tickets, k)
		}
	}
	for k, t := range d.webTickets {
		if now.After(t.expires) {
			delete(d.webTickets, k)
		}
	}
	for dev, ts := range d.webMints {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= webTicketWindow {
			delete(d.webMints, dev)
		}
	}
}

// dropUserLocked voids a user's pending enrollment codes, app sign-in
// tickets and browser sign-in tickets (sign out everywhere, disable, delete
// — DropUserSessions).
func (d *deviceState) dropUserLocked(userID string) {
	for k, t := range d.webTickets {
		if t.userID == userID {
			delete(d.webTickets, k)
		}
	}
	for k, c := range d.codes {
		if c.userID == userID {
			delete(d.codes, k)
		}
	}
	for k, t := range d.tickets {
		if t.userID == userID {
			delete(d.tickets, k)
		}
	}
}

func secretKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// randomCode is 16 random bytes as unpadded base32 (26 chars of A–Z2–7):
// URL-safe, case-insensitive, unambiguous to read out.
func randomCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// normalizeCode accepts a code as typed: any case, dashes/spaces dropped.
func normalizeCode(c string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(c)))
}

// MintEnrollCode mints a one-time enrollment code for userID. origin is the
// server origin the device will sign into its login challenges (recorded on
// the device when the code is redeemed).
func (a *Auth) MintEnrollCode(userID, origin string) (code string, expires time.Time, err error) {
	now := time.Now()
	code = randomCode()
	expires = now.Add(EnrollCodeTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dev.sweepLocked(now)
	if len(a.dev.codes) >= maxPendingDeviceSecrets {
		return "", time.Time{}, errDeviceBusy
	}
	a.dev.codes[secretKey(code)] = &enrollCode{userID: userID, origin: origin, expires: expires}
	return code, expires, nil
}

// RedeemEnrollCode consumes a code (single use, even when the caller then
// fails) and returns the user and origin it was minted for.
func (a *Auth) RedeemEnrollCode(code string) (userID, origin string, ok bool) {
	code = normalizeCode(code)
	if code == "" {
		return "", "", false
	}
	k := secretKey(code)
	now := time.Now()
	a.mu.Lock()
	c := a.dev.codes[k]
	delete(a.dev.codes, k)
	a.mu.Unlock()
	if c == nil || now.After(c.expires) {
		return "", "", false
	}
	return c.userID, c.origin, true
}

// NewDeviceChallenge issues a single-use login nonce bound to deviceID. A
// device holds at most maxChallengesPerDevice outstanding (the oldest is
// dropped), so one device can't crowd out the rest.
func (a *Auth) NewDeviceChallenge(deviceID string) (nonce string, expires time.Time, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	nonce = base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	expires = now.Add(ChallengeTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dev.sweepLocked(now)
	var oldest string
	n := 0
	for k, c := range a.dev.challenges {
		if c.deviceID != deviceID {
			continue
		}
		n++
		if oldest == "" || c.created.Before(a.dev.challenges[oldest].created) {
			oldest = k
		}
	}
	if n >= maxChallengesPerDevice {
		delete(a.dev.challenges, oldest)
	}
	if len(a.dev.challenges) >= maxPendingDeviceSecrets {
		return "", time.Time{}, errDeviceBusy
	}
	a.dev.challenges[nonce] = &deviceChallenge{deviceID: deviceID, created: now, expires: expires}
	return nonce, expires, nil
}

// ConsumeDeviceChallenge spends a nonce: true only once, only for the device
// it was issued to, and only before it expires. The nonce is gone either way.
func (a *Auth) ConsumeDeviceChallenge(deviceID, nonce string) bool {
	if nonce == "" {
		return false
	}
	now := time.Now()
	a.mu.Lock()
	c := a.dev.challenges[nonce]
	delete(a.dev.challenges, nonce)
	a.mu.Unlock()
	return c != nil && c.deviceID == deviceID && !now.After(c.expires)
}

// ValidPKCEChallenge reports a well-formed S256 code challenge: base64url
// (no padding) of a SHA-256 digest — 43 characters.
func ValidPKCEChallenge(c string) bool {
	b, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

// MintAppTicket issues the one-shot ticket an app-started SSO sign-in ends
// with, bound to the user and to the PKCE challenge the app chose.
func (a *Auth) MintAppTicket(userID, challenge string) (string, error) {
	if !ValidPKCEChallenge(challenge) {
		return "", errors.New("bad PKCE challenge")
	}
	now := time.Now()
	t := randomCode()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dev.sweepLocked(now)
	if len(a.dev.tickets) >= maxPendingDeviceSecrets {
		return "", errDeviceBusy
	}
	a.dev.tickets[secretKey(t)] = &appTicket{userID: userID, challenge: challenge, expires: now.Add(AppTicketTTL)}
	return t, nil
}

// RedeemAppTicket consumes a ticket (single use, even on a wrong verifier)
// and returns its user when base64url(sha256(verifier)) matches the
// challenge the sign-in started with — so another app that registered the
// xbin:// scheme and caught the redirect cannot redeem it.
func (a *Auth) RedeemAppTicket(ticket, verifier string) (userID string, ok bool) {
	ticket = normalizeCode(ticket)
	if ticket == "" || verifier == "" {
		return "", false
	}
	k := secretKey(ticket)
	now := time.Now()
	a.mu.Lock()
	t := a.dev.tickets[k]
	delete(a.dev.tickets, k)
	a.mu.Unlock()
	if t == nil || now.After(t.expires) {
		return "", false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(t.challenge)) != 1 {
		return "", false
	}
	return t.userID, true
}

// --- the signed message ---------------------------------------------------

// DeviceLoginMessage is the exact byte string a device signs (ECDSA P-256,
// SHA-256) to log in:
//
//	"xbin-device-login-v1\n" + origin + "\n" + deviceID + "\n" + nonce
//
// origin is the device's enrollment origin (NormalizeOrigin form), nonce the
// challenge exactly as the server sent it. native/spec/device-login.md is the
// app-side contract, with a test vector (TestDeviceLoginVector).
func DeviceLoginMessage(origin, deviceID, nonce string) []byte {
	return []byte(DeviceLoginContext + "\n" + origin + "\n" + deviceID + "\n" + nonce)
}

// DecodeB64URL decodes base64url, tolerating (only) trailing padding.
func DecodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// ParseDeviceKey decodes an enrollment public key: SPKI DER, base64url, and
// it must be an ECDSA P-256 key. Returns the key and its canonical encoding
// (unpadded base64url of the DER) for storage.
func ParseDeviceKey(b64 string) (*ecdsa.PublicKey, string, error) {
	der, err := DecodeB64URL(b64)
	if err != nil || len(der) == 0 || len(der) > 512 {
		return nil, "", errors.New("publicKey: want SPKI DER, base64url without padding")
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", fmt.Errorf("publicKey: %v", err)
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, "", errors.New("publicKey: must be an EC P-256 key")
	}
	return ek, base64.RawURLEncoding.EncodeToString(der), nil
}

// VerifyDeviceSignature checks sigB64 (ASN.1 DER ECDSA signature,
// base64url) over DeviceLoginMessage(origin, deviceID, nonce) with the
// device's stored public key.
func VerifyDeviceSignature(publicKeyB64, origin, deviceID, nonce, sigB64 string) bool {
	pub, _, err := ParseDeviceKey(publicKeyB64)
	if err != nil {
		return false
	}
	sig, err := DecodeB64URL(sigB64)
	if err != nil || len(sig) == 0 || len(sig) > 128 {
		return false
	}
	digest := sha256.Sum256(DeviceLoginMessage(origin, deviceID, nonce))
	return ecdsa.VerifyASN1(pub, digest[:], sig)
}

// NormalizeOrigin reduces a URL to the origin form devices sign:
// lowercase scheme://host, the port only when it is not the scheme's
// default, no path, no trailing slash. http and https only.
func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("not an http(s) origin: %q", raw)
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	if port == "" || scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		return scheme + "://" + host, nil
	}
	return scheme + "://" + host + ":" + port, nil
}
