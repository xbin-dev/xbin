// gh_jwt.go — the GitHub App's JSON Web Token (RS256, standard library
// only): what the App signs to call /app/* and mint installation tokens.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"
)

// parseAppKey reads the App's private key: PKCS#1 ("BEGIN RSA PRIVATE
// KEY", what GitHub downloads), falling back to PKCS#8.
func parseAppKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("the private key isn't PEM (paste the whole .pem file GitHub downloaded)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the private key is neither PKCS#1 nor PKCS#8 RSA")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key isn't RSA (GitHub Apps sign with RS256)")
	}
	return rk, nil
}

// keyFingerprint is the SHA-256 of the public key — what the page shows
// instead of the key (GitHub's App settings list the same fingerprint).
func keyFingerprint(k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

var b64 = base64.RawURLEncoding

// signJWT makes the App's token: iat a minute back (clock drift), exp nine
// minutes on (GitHub allows ten), iss the client id.
func signJWT(k *rsa.PrivateKey, iss string, now time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": iss,
	})
	signing := head + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign the App's JWT: %w", err)
	}
	return signing + "." + b64.EncodeToString(sig), nil
}

// jwtCache keeps the App's JWT for 8 of its 9 minutes.
type jwtCache struct {
	mu  sync.Mutex
	key string // the fingerprint|iss it was made for
	tok secretString
	at  time.Time
}

func (c *jwtCache) get(k *rsa.PrivateKey, iss string, now time.Time) (secretString, error) {
	id := keyFingerprint(k) + "|" + iss
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == id && !c.tok.Empty() && now.Sub(c.at) < 8*time.Minute && !now.Before(c.at) {
		return c.tok, nil
	}
	t, err := signJWT(k, iss, now)
	if err != nil {
		return secretString{}, err
	}
	c.key, c.tok, c.at = id, newSecret(t), now
	return c.tok, nil
}

func (c *jwtCache) reset() {
	c.mu.Lock()
	c.key, c.tok = "", secretString{}
	c.mu.Unlock()
}
