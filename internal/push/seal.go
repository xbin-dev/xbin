// Package push delivers notifications from xbind to the xbin app through
// the push relay (plans/native.md §14): device registrations (a relay
// handle + the device's X25519 public key, per user × device), end-to-end
// sealing, an asynchronous sender with retries, rate limits, per-user tile
// mutes, and the sources — POST /api/xbin/notify from tile backends and the
// ACP agent session events. Wire formats: native/spec/push.md; the relay:
// relay/README.md.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Payload is what a notification says. It is sealed to the device's key;
// only the app's Notification Service Extension reads it.
type Payload struct {
	V          int    `json:"v"`          // 1
	WS         string `json:"ws"`         // this workspace's push id (GET /devices/push → workspace)
	Kind       string `json:"kind"`       // agent.permission | agent.question | agent.turn | tile[.<kind>] | test
	Title      string `json:"title"`      // plain text
	Body       string `json:"body"`       // plain text
	Link       string `json:"link"`       // workspace-relative: c/<tile>/…, agent/<session>, "" = none
	CollapseID string `json:"collapseId"` // notifications sharing one replace each other ("" = none)
}

// Envelope is a sealed Payload: base64url (no padding) of the ephemeral
// public key, the nonce and ciphertext||tag.
type Envelope struct {
	V   int    `json:"v"`
	EPK string `json:"epk"`
	N   string `json:"n"`
	CT  string `json:"ct"`
}

// hkdfInfo binds derived keys to this format; a v2 changes it.
const hkdfInfo = "xbin-push-v1"

var b64 = base64.RawURLEncoding

// Seal encrypts plaintext (the payload's UTF-8 JSON) to a device's X25519
// public key: an ephemeral X25519 key pair, shared = X25519(ephemeral,
// recipient), key = HKDF-SHA256(shared, salt = ephemeralPublic ||
// recipientPublic, info = "xbin-push-v1", 32 bytes), AES-256-GCM with a
// random 12-byte nonce and no associated data.
func Seal(recipientPublic, plaintext []byte) (Envelope, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, err
	}
	return seal(eph, nonce, recipientPublic, plaintext)
}

// seal is Seal with the randomness supplied (the test vectors).
func seal(eph *ecdh.PrivateKey, nonce, recipientPublic, plaintext []byte) (Envelope, error) {
	key, err := deriveKey(eph, recipientPublic)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return Envelope{}, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	return Envelope{V: 1, EPK: b64.EncodeToString(eph.PublicKey().Bytes()), N: b64.EncodeToString(nonce), CT: b64.EncodeToString(ct)}, nil
}

// deriveKey is the sender side of the key agreement.
func deriveKey(eph *ecdh.PrivateKey, recipientPublic []byte) ([]byte, error) {
	rp, err := ecdh.X25519().NewPublicKey(recipientPublic)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(rp) // refuses low-order points (an all-zero secret)
	if err != nil {
		return nil, err
	}
	return kdf(shared, eph.PublicKey().Bytes(), recipientPublic)
}

func kdf(shared, ephemeralPublic, recipientPublic []byte) ([]byte, error) {
	salt := make([]byte, 0, 64)
	salt = append(salt, ephemeralPublic...)
	salt = append(salt, recipientPublic...)
	return hkdf.Key(sha256.New, shared, salt, hkdfInfo, 32)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Open is the device side of Seal (the app implements the same; Go has it
// for tests and tooling).
func Open(recipient *ecdh.PrivateKey, env Envelope) ([]byte, error) {
	if env.V != 1 {
		return nil, errors.New("push: unknown envelope version")
	}
	epk, err1 := b64.DecodeString(env.EPK)
	nonce, err2 := b64.DecodeString(env.N)
	ct, err3 := b64.DecodeString(env.CT)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	if len(nonce) != 12 {
		return nil, errors.New("push: bad nonce")
	}
	ep, err := ecdh.X25519().NewPublicKey(epk)
	if err != nil {
		return nil, err
	}
	shared, err := recipient.ECDH(ep)
	if err != nil {
		return nil, err
	}
	key, err := kdf(shared, epk, recipient.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, nil)
}

// ParsePublicKey decodes a device's X25519 public key (base64url; padding
// and the standard alphabet are tolerated) and refuses keys no sealing
// could use.
func ParsePublicKey(s string) ([]byte, error) {
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if raw, err = enc.DecodeString(s); err == nil {
			break
		}
	}
	if err != nil || len(raw) != 32 {
		return nil, errors.New("publicKey: 32 bytes of X25519, base64url")
	}
	if _, err := deriveKey(mustEphemeral(), raw); err != nil {
		return nil, errors.New("publicKey: not a usable X25519 key")
	}
	return raw, nil
}

func mustEphemeral() *ecdh.PrivateKey {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}
