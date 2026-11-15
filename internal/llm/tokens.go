package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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
// xbl_…, the form OpenCode and Codex send). They are minted for terminal
// and agent sessions (internal/term, through MintKey) and for a bound
// component that asks for one (POST /llm/keys, sessions.go).

// KeyPrefix starts every LLM key, so redaction and secret scanners
// recognise one.
const KeyPrefix = "xbl_"

// KeyGrant is what an LLM key stands for.
type KeyGrant struct {
	ID        string `json:"id"`                  // names the key in listings and for Revoke; not secret
	User      string `json:"user"`                // the person its calls are metered to ("owner": the root token's; "": nobody — a component's key for a backend that serves everyone)
	Component string `json:"component,omitempty"` // the tile it was minted for or by
	Label     string `json:"label,omitempty"`     // what it is for: a session id, a harness and run
	Sandbox   string `json:"sandbox,omitempty"`   // the sandbox it was handed to, when one
	// Deployment is the tile deployment the key was minted in or for ("" =
	// the primary): the deployment of the backend that asked for it, or the
	// one a person's session targets. A key a deployment's backend asked
	// for is held to the tile's edge policy for that deployment on every
	// call; its keys are ended by its own label and with it.
	Deployment string `json:"deployment,omitempty"`
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

// The store's bounds. A key is a few hundred bytes and every call resolves
// one, so what a tile — or a person through it — can keep alive is capped:
// MaxKeysPerHolder live keys for one (component, person) pair and
// MaxKeysPerComponent for one component, whoever they are for. A session
// holds one key and a harness run one, so the caps are far above honest
// use; reaching one is ErrTooManyKeys, never a silent eviction of a key
// something is running on. MaxKeyLabel bounds a label and a sandbox name.
const (
	MaxKeysPerHolder    = 64
	MaxKeysPerComponent = 512
	MaxKeyLabel         = 200
)

// ErrTooManyKeys is Mint's refusal at a cap.
var ErrTooManyKeys = errors.New("too many live LLM keys")

// keySweepEvery is how often expired keys are dropped (Sweeper); a cap that
// is hit sweeps at once before it refuses.
const keySweepEvery = time.Minute

// Keys is the in-memory store of minted LLM keys. Keys do not outlive
// xbind: the sessions they are minted for don't either.
type Keys struct {
	now func() time.Time

	mu     sync.Mutex
	byID   map[string]*keyEntry
	bySH   map[[32]byte]*keyEntry // sha256 of the token: the token itself is never kept
	byComp map[string]int         // live keys per component
	byPair map[[2]string]int      // …and per (component, user)
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
	return &Keys{now: now, byID: map[string]*keyEntry{}, bySH: map[[32]byte]*keyEntry{},
		byComp: map[string]int{}, byPair: map[[2]string]int{}}
}

// add and drop keep the counts. k.mu held.
func (k *Keys) add(e *keyEntry) {
	k.byID[e.grant.ID], k.bySH[e.sum] = e, e
	k.byComp[e.grant.Component]++
	k.byPair[[2]string{e.grant.Component, e.grant.User}]++
}

func (k *Keys) drop(e *keyEntry) {
	delete(k.byID, e.grant.ID)
	delete(k.bySH, e.sum)
	pair := [2]string{e.grant.Component, e.grant.User}
	if k.byComp[e.grant.Component]--; k.byComp[e.grant.Component] <= 0 {
		delete(k.byComp, e.grant.Component)
	}
	if k.byPair[pair]--; k.byPair[pair] <= 0 {
		delete(k.byPair, pair)
	}
}

// full reports whether g's holder is at a cap. k.mu held.
func (k *Keys) full(g KeyGrant) bool {
	return k.byPair[[2]string{g.Component, g.User}] >= MaxKeysPerHolder || (g.Component != "" && k.byComp[g.Component] >= MaxKeysPerComponent)
}

// Sweeper drops expired keys every keySweepEvery until stop is closed: the
// store's own housekeeping, off the path of Mint and Resolve.
func (k *Keys) Sweeper(stop <-chan struct{}) {
	t := time.NewTicker(keySweepEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			k.Sweep()
		}
	}
}

// Sweep drops expired keys and says how many.
func (k *Keys) Sweep() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sweep()
}

// Extend moves the expiry of the live key id that match picks to ttl from
// now (capped at MaxKeyTTL; never earlier than it was) and answers the
// grant; false when there is no such key.
func (k *Keys) Extend(id string, ttl time.Duration, match func(KeyGrant) bool) (KeyGrant, bool) {
	if ttl <= 0 || ttl > MaxKeyTTL {
		ttl = MaxKeyTTL
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.byID[id]
	now := k.now()
	if e == nil || !now.Before(e.grant.Expires) || (match != nil && !match(e.grant)) {
		return KeyGrant{}, false
	}
	if until := now.Add(ttl); until.After(e.grant.Expires) {
		e.grant.Expires = until
	}
	return e.grant, true
}

// Mint makes a key for g (ID and Expires are set here; ttl is capped at
// MaxKeyTTL) and answers the token, shown once. ErrTooManyKeys when g's
// holder is at a cap (expired keys are dropped first).
func (k *Keys) Mint(g KeyGrant, ttl time.Duration) (token string, grant KeyGrant, err error) {
	if g.User == "" && g.Component == "" {
		return "", KeyGrant{}, errors.New("an LLM key names the person or the component it is metered to")
	}
	if len(g.Label) > MaxKeyLabel || len(g.Sandbox) > MaxKeyLabel {
		return "", KeyGrant{}, fmt.Errorf("an LLM key's label and sandbox are at most %d characters", MaxKeyLabel)
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
	if k.full(g) {
		if k.sweep(); k.full(g) {
			return "", KeyGrant{}, fmt.Errorf("%w for %s (at most %d for one person on a tile, %d for a tile): end the ones no longer in use",
				ErrTooManyKeys, holderName(g), MaxKeysPerHolder, MaxKeysPerComponent)
		}
	}
	k.add(e)
	return token, g, nil
}

func holderName(g KeyGrant) string {
	switch {
	case g.Component == "":
		return g.User
	case g.User == "":
		return g.Component
	}
	return g.User + " on " + g.Component
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
		k.drop(e)
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
	k.drop(e)
	return true
}

// RevokeWhere ends every key match picks (a person signed out everywhere, a
// tile unbound) and says how many.
func (k *Keys) RevokeWhere(match func(KeyGrant) bool) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, e := range k.byID {
		if match(e.grant) {
			k.drop(e)
			n++
		}
	}
	return n
}

// Live is how many keys the store holds (expired ones until the next sweep
// included).
func (k *Keys) Live() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.byID)
}

// sweep drops expired keys and says how many. k.mu held.
func (k *Keys) sweep() int {
	now, n := k.now(), 0
	for _, e := range k.byID {
		if !now.Before(e.grant.Expires) {
			k.drop(e)
			n++
		}
	}
	return n
}
