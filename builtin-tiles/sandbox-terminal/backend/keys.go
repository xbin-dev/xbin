// keys.go — SSH public keys, registered per person.
//
// A person registers and removes their own keys from the tile's page (the
// frame token makes them the verified X-XBin-User); the tile's managers
// (write or terminal on the tile, or the owner) list and revoke anyone's. A
// key belongs to one person: it is how an SSH login says who is asking —
// and only while that person may still use this tile (access.go: a key
// whose person lost access is kept, marked inactive).
// Keys live in the tile's kv (`state`, key "keys"), the list as one JSON
// document — a few dozen per workspace, read at start and on every change.
package main

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// keyRec is one registered key.
type keyRec struct {
	ID          string `json:"id"`          // the key's SHA-256, base64url (route-safe)
	User        string `json:"user"`        // the person it logs in as
	Name        string `json:"name"`        // a label (the key's comment unless given)
	Type        string `json:"type"`        // ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa, sk-…
	Fingerprint string `json:"fingerprint"` // SHA256:… as ssh-keygen -l prints it
	PublicKey   string `json:"publicKey"`   // the authorized_keys line, without a comment
	Added       int64  `json:"added"`       // unix ms
	LastUsed    int64  `json:"lastUsed,omitempty"`
	// Inactive is when xbind last said the key's person may no longer use
	// this tile (unix ms; 0 = active): the key logs nobody in while that
	// holds, and is active again once a check finds access back (access.go).
	Inactive int64 `json:"inactive,omitempty"`
}

const (
	maxKeysPerUser = 20
	maxKeys        = 2000
	maxKeyName     = 64
)

// keyID is a key's id: its SHA-256 over the wire form, base64url.
func keyID(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// parseKey reads one authorized_keys line: a plain public key (no options,
// no certificate) of a type worth trusting.
func parseKey(line string) (ssh.PublicKey, string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, "", errors.New("paste a public key (the contents of ~/.ssh/id_ed25519.pub, say)")
	}
	if strings.Contains(line, "PRIVATE KEY") {
		return nil, "", errors.New("that is a PRIVATE key — never paste it anywhere; paste the .pub file's line instead")
	}
	if strings.ContainsAny(strings.TrimRight(line, "\r\n"), "\n") {
		return nil, "", errors.New("one key at a time: paste a single line")
	}
	pk, comment, opts, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", fmt.Errorf("not an SSH public key: %v", err)
	}
	if len(opts) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, "", errors.New("paste the key alone: authorized_keys options (command=, from=, …) aren't taken here")
	}
	if _, ok := pk.(*ssh.Certificate); ok {
		return nil, "", errors.New("certificates aren't taken: paste the plain public key")
	}
	switch pk.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		if ck, ok := pk.(ssh.CryptoPublicKey); ok {
			if bits := rsaBits(ck); bits < 2048 {
				return nil, "", fmt.Errorf("an RSA key of %d bits is too weak: use ed25519 (ssh-keygen -t ed25519), or RSA of 2048 bits or more", bits)
			}
		}
	default:
		return nil, "", fmt.Errorf("keys of type %s aren't taken: use ed25519 (ssh-keygen -t ed25519)", pk.Type())
	}
	return pk, strings.TrimSpace(comment), nil
}

// rsaBits is an RSA key's modulus size (0: not an RSA key).
func rsaBits(ck ssh.CryptoPublicKey) int {
	if k, ok := ck.CryptoPublicKey().(*rsa.PublicKey); ok {
		return k.N.BitLen()
	}
	return 0
}

// cleanName bounds a key's label.
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for len(s) > maxKeyName {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// --- the store ---------------------------------------------------------------------

// loadKeys reads the key list; a missing one is empty.
func (t *Tile) loadKeys() error {
	b, err := t.kv.Get("keys")
	if errors.Is(err, errNotFound) {
		b, err = nil, nil
	}
	if err != nil {
		return err
	}
	var keys []keyRec
	if len(b) > 0 {
		if err := json.Unmarshal(b, &keys); err != nil {
			return fmt.Errorf("the stored keys don't parse: %w", err)
		}
	}
	t.mu.Lock()
	t.keys, t.keysLoaded = keys, true
	t.mu.Unlock()
	return nil
}

// saveKeysLocked writes keys (t.mu held); the list in memory changes only
// when the write went through.
func (t *Tile) saveKeysLocked(keys []keyRec) error {
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := t.kv.Put("keys", b); err != nil {
		return err
	}
	t.keys = keys
	return nil
}

var errKeysUnloaded = errors.New("the keys aren't loaded yet (the tile's storage didn't answer) — try again shortly")

// addKey registers line for user.
func (t *Tile) addKey(user, line, name string) (keyRec, int, error) {
	pk, comment, err := parseKey(line)
	if err != nil {
		return keyRec{}, 400, err
	}
	if name = cleanName(name); name == "" {
		name = cleanName(comment)
	}
	rec := keyRec{ID: keyID(pk), User: user, Name: name, Type: pk.Type(), Fingerprint: ssh.FingerprintSHA256(pk),
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), Added: time.Now().UnixMilli()}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.keysLoaded {
		return keyRec{}, 503, errKeysUnloaded
	}
	mine := 0
	for _, k := range t.keys {
		if k.ID == rec.ID {
			if k.User == user {
				return k, 200, nil // registered already: the same answer
			}
			return keyRec{}, 409, errors.New("this key is registered to someone else — each person logs in with keys of their own")
		}
		if k.User == user {
			mine++
		}
	}
	if mine >= maxKeysPerUser {
		return keyRec{}, 409, fmt.Errorf("you have %d keys registered, the most one person may — remove one first", mine)
	}
	if len(t.keys) >= maxKeys {
		return keyRec{}, 409, errors.New("this tile holds as many keys as it takes")
	}
	next := append(append([]keyRec(nil), t.keys...), rec)
	if err := t.saveKeysLocked(next); err != nil {
		return keyRec{}, 503, err
	}
	return rec, 201, nil
}

// removeKey deletes key id — a person's own, or anyone's for a manager.
func (t *Tile) removeKey(id, user string, manager bool) (keyRec, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.keysLoaded {
		return keyRec{}, 503, errKeysUnloaded
	}
	for i, k := range t.keys {
		if k.ID != id {
			continue
		}
		if k.User != user && !manager {
			break // someone else's: as if it weren't there
		}
		next := append(append([]keyRec(nil), t.keys[:i]...), t.keys[i+1:]...)
		if err := t.saveKeysLocked(next); err != nil {
			return keyRec{}, 503, err
		}
		return k, 204, nil
	}
	return keyRec{}, 404, errors.New("no such key")
}

// keysOf lists user's keys ("" = everyone's).
func (t *Tile) keysOf(user string) []keyRec {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []keyRec{}
	for _, k := range t.keys {
		if user == "" || k.User == user {
			out = append(out, k)
		}
	}
	return out
}

// keyFor finds the registered key that pk is: the person it logs in as.
func (t *Tile) keyFor(pk ssh.PublicKey) (keyRec, bool) {
	id := keyID(pk)
	wire := string(pk.Marshal())
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range t.keys {
		if k.ID != id {
			continue
		}
		stored, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
		if err != nil || string(stored.Marshal()) != wire {
			return keyRec{}, false
		}
		return k, true
	}
	return keyRec{}, false
}

// touchKey records a login with key id — at most every ten minutes a key,
// so a busy key doesn't write the kv on every connection.
func (t *Tile) touchKey(id string) {
	now := time.Now().UnixMilli()
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, k := range t.keys {
		if k.ID != id {
			continue
		}
		if now-k.LastUsed < int64(10*time.Minute/time.Millisecond) {
			return
		}
		next := append([]keyRec(nil), t.keys...)
		next[i].LastUsed = now
		_ = t.saveKeysLocked(next) // best effort: a stale "last used" harms nobody
		return
	}
}
