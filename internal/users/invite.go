// Invite tokens (plans/user-org-ux.md, D22): the credential-delivery half of
// manual user creation. There is NO self-signup — an admin creates the
// account and may mint a single-use, TTL'd invite link instead of choosing a
// password; the invitee sets their own password by redeeming it. Tokens are
// random, hashed at rest (a leaked users.json doesn't leak live invites), and
// invalidated by re-minting or redemption.
//
// This is deliberately provider-agnostic groundwork: an account is a User row
// whose credential arrives *somehow* — today a password (admin-set or
// invite-redeemed); an SSO/OIDC identity later would be another way to bind a
// credential to the same row, not a different account model.
package users

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"time"
)

// InviteTTL is how long a minted invite link stays redeemable.
const InviteTTL = 72 * time.Hour

// MinPasswordLen is the UX floor for passwords set through the API/login
// planes (the store itself stays policy-free for tests/seeding).
const MinPasswordLen = 8

// CreateInvite mints a fresh invite token for an existing user, replacing any
// previous one (re-minting invalidates old links). The user's current
// password, if any, keeps working until the invite is redeemed. Returns the
// plaintext token — shown once; only its hash is stored.
func (s *Store) CreateInvite(id string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = InviteTTL
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(tok))
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return "", fmt.Errorf("no such user %q", id)
	}
	nu := *u
	nu.InviteHash = base64.RawStdEncoding.EncodeToString(h[:])
	nu.InviteExpires = time.Now().Add(ttl).Unix()
	s.byID[nu.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return "", err
	}
	return tok, nil
}

// InviteUser looks up the (unexpired) invite a token belongs to WITHOUT
// consuming it — the set-password page uses this to greet the invitee.
func (s *Store) InviteUser(token string) (*User, bool) {
	h := sha256.Sum256([]byte(token))
	want := base64.RawStdEncoding.EncodeToString(h[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		if u.InviteHash == "" || u.InviteExpires < timeNow() {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(u.InviteHash), []byte(want)) == 1 {
			c := u.Public()
			return &c, true
		}
	}
	return nil, false
}

// RedeemInvite consumes an invite: sets the user's password and clears the
// invite (single-use). Expired/unknown tokens fail generically.
func (s *Store) RedeemInvite(token, password string) (*User, error) {
	if len([]rune(password)) < MinPasswordLen {
		return nil, fmt.Errorf("password too short (min %d characters)", MinPasswordLen)
	}
	h := sha256.Sum256([]byte(token))
	want := base64.RawStdEncoding.EncodeToString(h[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.byID {
		if u.InviteHash == "" || u.InviteExpires < timeNow() {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(u.InviteHash), []byte(want)) != 1 {
			continue
		}
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		nu := *u
		nu.PassHash = hashPassword(password, salt)
		nu.InviteHash = ""
		nu.InviteExpires = 0
		s.byID[nu.ID] = &nu
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		c := nu.Public()
		return &c, nil
	}
	return nil, fmt.Errorf("invalid or expired invite")
}

// UpsertInvited creates a user with NO credential yet (Verify always fails
// until an invite is redeemed or an admin sets a password). The invite itself
// is minted separately (CreateInvite) so the two steps share one code path
// with re-invites.
func (s *Store) UpsertInvited(u User) (*User, error) {
	u.ID = normalizeID(u.ID)
	if u.ID == "" {
		return nil, fmt.Errorf("user id required")
	}
	if u.Role != RoleAdmin && u.Role != RoleUser {
		u.Role = RoleUser
	}
	if u.Tiles == nil {
		u.Tiles = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[u.ID] != nil {
		return nil, fmt.Errorf("user %q already exists — mint a new invite instead", u.ID)
	}
	if err := validID(u.ID); err != nil {
		return nil, err
	}
	u.PassHash = ""
	u.Created = time.Now().Unix()
	nu := u
	s.byID[u.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := nu
	return &c, nil
}
