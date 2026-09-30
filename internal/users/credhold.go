package users

// credhold.go — credentials an admin made for a person that wait for the
// person (plans/partitions/06 §9; PD-07's a+): with the workspace policy
// credentialResetConfirm on, a sign-in link, password or SSO email an admin
// or org admin sets for someone who holds partitions takes effect only once
// the person allows it, or 24 h after they were told. The broker keeps what
// is held (its own file, never users.json); the store only
//
//   - mints a held link (CreateHeldInvite): its stored hash carries
//     HeldInvitePrefix, which no token's hash matches, so an xbind without
//     the gate (an older one, after a downgrade) never redeems it — the
//     link just doesn't work there;
//   - asks the installed gate before a held link is redeemed
//     (SetInviteGate): a held link answers ErrInviteHeld's text, and with
//     no gate installed every held link does;
//   - activates (ReleaseInvite) or revokes (RevokeInvite) a held link, and
//     stores a held password's hash (SetPassHash), when the person decides
//     or the 24 hours pass.
//
// Without a held link every invite redeems as before.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// HeldInvitePrefix marks a stored invite hash as held for its person: the
// hash of no token starts with it.
const HeldInvitePrefix = "held:"

// ErrInviteHeld is RedeemInvite's answer for an invite that waits for its
// person to allow it (an InviteHeldError wraps it).
var ErrInviteHeld = errors.New("this sign-in link waits for its person to confirm it")

// InviteHeldError is a held invite's refusal: whom it waits for.
type InviteHeldError struct{ Person string }

func (e *InviteHeldError) Error() string {
	return fmt.Sprintf("waiting for %s to confirm this sign-in link (an admin made it; %s allows it from a signed-in device, or it works 24 hours after they were told)", e.Person, e.Person)
}

func (e *InviteHeldError) Unwrap() error { return ErrInviteHeld }

var inviteGates sync.Map // *Store → func(User) error

// SetInviteGate installs the check RedeemInvite asks before it spends a
// held invite: nil lets it through, an error refuses it and keeps it
// unspent. The gate runs under the store's lock and must not call the
// store. Without a gate a held invite is refused.
func (s *Store) SetInviteGate(gate func(u User) error) {
	if gate == nil {
		inviteGates.Delete(s)
		return
	}
	inviteGates.Store(s, gate)
}

// inviteGateLocked asks the installed gate about u's held invite (s.mu
// held).
func (s *Store) inviteGateLocked(u *User) error {
	g, ok := inviteGates.Load(s)
	if !ok {
		return &InviteHeldError{Person: u.ID}
	}
	return g.(func(User) error)(*u)
}

// inviteMatch reports whether stored (User.InviteHash) is want, the hash of
// the token presented, plainly or held; constant-time in the hash.
func inviteMatch(stored, want string) (ok, held bool) {
	h, held := strings.CutPrefix(stored, HeldInvitePrefix)
	return subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1, held
}

// InviteHashOf is the stored form of invite token tok (User.InviteHash,
// without HeldInvitePrefix).
func InviteHashOf(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return base64.RawStdEncoding.EncodeToString(h[:])
}

// CreateHeldInvite mints an invite as CreateInvite does, held: stored with
// HeldInvitePrefix, it redeems only when the gate lets it through, until
// ReleaseInvite activates it or RevokeInvite drops it.
func (s *Store) CreateHeldInvite(id string, ttl time.Duration) (string, error) {
	return s.createInvite(id, ttl, true)
}

// HashPassword is password's stored form, for a password held until its
// person allows it (SetPassHash stores it then).
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return hashPassword(password, salt), nil
}

// SetPassHash stores hash (HashPassword's) as id's password: a held
// password its person allowed.
func (s *Store) SetPassHash(id, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return fmt.Errorf("no such user %q", id)
	}
	nu := *u
	nu.PassHash = hash
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}

// The states of a held invite in the store (HeldInviteState).
const (
	InviteHeldState   = "held"   // stored held: waits for its person
	InviteActiveState = "active" // stored plain: redeemable (released, or its wait passed)
	InviteGoneState   = "gone"   // spent, replaced by a newer link, revoked or expired
)

// HeldInviteState is where id's invite whose hash is hash stands.
func (s *Store) HeldInviteState(id, hash string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.byID[normalizeID(id)]
	switch {
	case u == nil || u.InviteHash == "" || u.InviteExpires < timeNow():
		return InviteGoneState
	case u.InviteHash == HeldInvitePrefix+hash:
		return InviteHeldState
	case u.InviteHash == hash:
		return InviteActiveState
	}
	return InviteGoneState
}

// ReleaseInvite activates id's held invite whose hash is hash (its person
// allowed it, or 24 hours passed); nothing when it isn't held any more.
func (s *Store) ReleaseInvite(id, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil || u.InviteHash != HeldInvitePrefix+hash {
		return nil
	}
	nu := *u
	nu.InviteHash = hash
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}

// RevokeInvite clears id's pending invite when it is still the one whose
// hash is hash, held or not (a held link its person refused, or one whose
// hold couldn't be kept); a newer one stays. revoked false: it was no longer
// pending (redeemed, replaced or expired) — under the store's lock, so a
// redemption and a revocation never both win.
func (s *Store) RevokeInvite(id, hash string) (revoked bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil || hash == "" || u.InviteExpires < timeNow() || u.InviteHash != hash && u.InviteHash != HeldInvitePrefix+hash {
		return false, nil
	}
	nu := *u
	nu.InviteHash, nu.InviteExpires = "", 0
	s.byID[nu.ID] = &nu
	return true, s.persistLocked()
}

// ErrSSOHeld is SSOSignInHeld's answer for a person whose sign-ins through
// a newly configured single sign-on provider wait for them to confirm it
// (an SSOHeldError wraps it).
var ErrSSOHeld = errors.New("sign-in through this single sign-on provider waits for its person to confirm it")

// SSOHeldError is a held SSO sign-in's refusal: whose.
type SSOHeldError struct{ Person string }

func (e *SSOHeldError) Error() string {
	return fmt.Sprintf("the workspace's single sign-on provider changed: signing in through it as %s waits for %s to confirm it from a signed-in device (or 24 hours)", e.Person, e.Person)
}

func (e *SSOHeldError) Unwrap() error { return ErrSSOHeld }

var ssoGates sync.Map // *Store → func(User) error

// SetSSOGate installs the check an SSO sign-in asks once it resolved its
// person (SSOSignInHeld); nil removes it.
func (s *Store) SetSSOGate(gate func(u User) error) {
	if gate == nil {
		ssoGates.Delete(s)
		return
	}
	ssoGates.Store(s, gate)
}

// SSOSignInHeld asks the installed gate whether u's sign-in through the
// single sign-on provider waits (an error: refuse it); nil without a gate.
// Called outside the store's lock.
func (s *Store) SSOSignInHeld(u User) error {
	g, ok := ssoGates.Load(s)
	if !ok {
		return nil
	}
	return g.(func(User) error)(u)
}

// InviteGateInstalled reports whether a gate is installed (SetInviteGate).
func (s *Store) InviteGateInstalled() bool {
	_, ok := inviteGates.Load(s)
	return ok
}
