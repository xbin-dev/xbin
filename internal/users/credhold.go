package users

// credhold.go — credentials an admin made for a person that wait for the
// person (plans/partitions/06 §9; PD-07's a+): with the workspace policy
// credentialResetConfirm on, a sign-in link, password or SSO email an admin
// or org admin sets for someone who holds partitions takes effect only once
// the person allows it, or 24 h after they were told. The broker keeps what
// is held (its own file, never users.json); the store only
//
//   - asks the installed gate before an invite is redeemed
//     (SetInviteGate): a held link answers ErrInviteHeld's text;
//   - stores a held password's hash, or revokes a held invite, when the
//     person decides (SetPassHash, RevokeInvite).
//
// Without a gate every invite redeems as before.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
)

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

// SetInviteGate installs the check RedeemInvite asks before it spends an
// invite: a non-nil error refuses it and keeps it unspent. The gate runs
// under the store's lock and must not call the store.
func (s *Store) SetInviteGate(gate func(u User) error) {
	if gate == nil {
		inviteGates.Delete(s)
		return
	}
	inviteGates.Store(s, gate)
}

// inviteGateLocked asks the installed gate about u (s.mu held).
func (s *Store) inviteGateLocked(u *User) error {
	g, ok := inviteGates.Load(s)
	if !ok {
		return nil
	}
	return g.(func(User) error)(*u)
}

// InviteHashOf is the stored form of invite token tok (User.InviteHash).
func InviteHashOf(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return base64.RawStdEncoding.EncodeToString(h[:])
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

// RevokeInvite clears id's pending invite when it is still the one whose
// stored hash is hash (a held link its person refused); a newer one stays.
func (s *Store) RevokeInvite(id, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil || u.InviteHash == "" || u.InviteHash != hash {
		return nil
	}
	nu := *u
	nu.InviteHash, nu.InviteExpires = "", 0
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}
