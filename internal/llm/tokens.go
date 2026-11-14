package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

// tokens.go — LLM keys: narrow credentials for the connector's
// OpenAI-compatible routes and nothing else (docs/auth.md "LLM keys",
// D226). A terminal or an agent session gets one so the coding CLI inside
// it can reach the workspace's models without holding the tile's API token;
// a component bound to the connector will ask for one for a harness it
// runs in a sandbox. The worst a leaked key does is spend its person's
// quota until it expires or is revoked.
//
// This package holds the store and honours the keys (Authorization: Bearer
// xbl_…, the form OpenCode and Codex send). Nothing mints one yet: the
// session hook-up and the SDK call that do are later packages, which call
// Mint and Revoke.

// KeyPrefix starts every LLM key, so redaction and secret scanners
// recognise one.
const KeyPrefix = "xbl_"

// KeyGrant is what an LLM key stands for.
type KeyGrant struct {
	ID        string `json:"id"`                  // names the key in listings and for Revoke; not secret
	User      string `json:"user"`                // the person its calls are metered to ("owner": the root token's)
	Component string `json:"component,omitempty"` // the tile it was minted for or by
	Label     string `json:"label,omitempty"`     // what it is for: a session id, a harness and run
	Sandbox   string `json:"sandbox,omitempty"`   // the sandbox it was handed to, when one
	// Bound: the key was minted under Component's binding to the connector
	// (a component asking for a key for work it runs): it is refused once
	// the tile is no longer bound, and revoked when the binding goes.
	Bound   bool      `json:"bound,omitempty"`
	Expires time.Time `json:"expires"`
}

// KeySource resolves a presented LLM key. *Keys is the implementation; a
// test, or a store kept elsewhere, is another.
type KeySource interface {
	Resolve(token string) (KeyGrant, bool)
}

// MaxKeyTTL caps a key's life; a session's key is revoked when the session
// ends, long before.
const MaxKeyTTL = 30 * 24 * time.Hour

// Keys is the in-memory store of minted LLM keys. Keys do not outlive
// xbind: the sessions they are minted for don't either.
type Keys struct {
	now func() time.Time

	mu   sync.Mutex
	byID map[string]*keyEntry
	bySH map[[32]byte]*keyEntry // sha256 of the token: the token itself is never kept
}

type keyEntry struct {
	grant KeyGrant
	sum   [32]byte
}

// NewKeys makes an empty store (now nil: time.Now).
func NewKeys(now func() time.Time) *Keys {
	if now == nil {
		now = time.Now
	}
	return &Keys{now: now, byID: map[string]*keyEntry{}, bySH: map[[32]byte]*keyEntry{}}
}

// Mint makes a key for g (ID and Expires are set here; ttl is capped at
// MaxKeyTTL) and answers the token, shown once.
func (k *Keys) Mint(g KeyGrant, ttl time.Duration) (token string, grant KeyGrant, err error) {
	if g.User == "" {
		return "", KeyGrant{}, errors.New("an LLM key names the person it is metered to")
	}
	if ttl <= 0 || ttl > MaxKeyTTL {
		ttl = MaxKeyTTL
	}
	var raw [32]byte
	var id [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", KeyGrant{}, err
	}
	if _, err := rand.Read(id[:]); err != nil {
		return "", KeyGrant{}, err
	}
	token = KeyPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	g.ID, g.Expires = "lk_"+base64.RawURLEncoding.EncodeToString(id[:]), k.now().Add(ttl)
	e := &keyEntry{grant: g, sum: sha256.Sum256([]byte(token))}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sweep()
	k.byID[g.ID], k.bySH[e.sum] = e, e
	return token, g, nil
}

// Resolve is the grant of a presented token, while it lives.
func (k *Keys) Resolve(token string) (KeyGrant, bool) {
	if !strings.HasPrefix(token, KeyPrefix) {
		return KeyGrant{}, false
	}
	sum := sha256.Sum256([]byte(token))
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.bySH[sum]
	if e == nil {
		return KeyGrant{}, false
	}
	if !k.now().Before(e.grant.Expires) {
		delete(k.byID, e.grant.ID)
		delete(k.bySH, e.sum)
		return KeyGrant{}, false
	}
	return e.grant, true
}

// Revoke ends the key with this id; false when there is none.
func (k *Keys) Revoke(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.byID[id]
	if e == nil {
		return false
	}
	delete(k.byID, id)
	delete(k.bySH, e.sum)
	return true
}

// RevokeWhere ends every key match picks (a person signed out everywhere, a
// tile unbound) and says how many.
func (k *Keys) RevokeWhere(match func(KeyGrant) bool) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for id, e := range k.byID {
		if match(e.grant) {
			delete(k.byID, id)
			delete(k.bySH, e.sum)
			n++
		}
	}
	return n
}

// sweep drops expired keys. k.mu held.
func (k *Keys) sweep() {
	now := k.now()
	for id, e := range k.byID {
		if !now.Before(e.grant.Expires) {
			delete(k.byID, id)
			delete(k.bySH, e.sum)
		}
	}
}
