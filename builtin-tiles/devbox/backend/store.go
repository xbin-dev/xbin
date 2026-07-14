package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// store holds the SSH host key and the owner's authorized public keys, both
// persisted in the tile's `storage` filesystem resource (encrypted at rest).
// Default-deny: with no authorized keys, nobody can SSH in.
type store struct {
	dir string

	mu   sync.RWMutex
	keys map[string]authKey // fingerprint → key
}

type authKey struct {
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
	Type        string `json:"type"`
	line        string // the raw authorized_keys line
}

func newStore(dir string) *store {
	s := &store{dir: dir, keys: map[string]authKey{}}
	s.load()
	return s
}

func (s *store) authorizedPath() string { return filepath.Join(s.dir, "authorized_keys") }
func (s *store) hostKeyPath() string    { return filepath.Join(s.dir, "ssh_host_ed25519_key") }

// hostKey loads the persistent SSH host key, generating one on first use so a
// client's known_hosts entry stays stable across restarts.
func (s *store) hostKey() (ssh.Signer, error) {
	if b, err := os.ReadFile(s.hostKeyPath()); err == nil {
		return ssh.ParsePrivateKey(b)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pemBytes, err := ssh.MarshalPrivateKey(priv, "devbox host key")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.hostKeyPath(), pem.EncodeToMemory(pemBytes), 0o600); err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(pem.EncodeToMemory(pemBytes))
}

// load reads authorized_keys into the fingerprint index.
func (s *store) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = map[string]authKey{}
	b, err := os.ReadFile(s.authorizedPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		fp := ssh.FingerprintSHA256(pk)
		s.keys[fp] = authKey{Fingerprint: fp, Comment: comment, Type: pk.Type(), line: line}
	}
}

// authorized reports whether a presented public key is trusted.
func (s *store) authorized(key ssh.PublicKey) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.keys[ssh.FingerprintSHA256(key)]
	return ok
}

// list returns the authorized keys (no secret material).
func (s *store) list() []authKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]authKey, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, authKey{Fingerprint: k.Fingerprint, Comment: k.Comment, Type: k.Type})
	}
	return out
}

// add parses and stores an authorized_keys line.
func (s *store) add(line string) (authKey, error) {
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return authKey{}, fmt.Errorf("not a valid SSH public key: %w", err)
	}
	fp := ssh.FingerprintSHA256(pk)
	k := authKey{Fingerprint: fp, Comment: comment, Type: pk.Type(),
		line: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))) + " " + comment}
	s.mu.Lock()
	s.keys[fp] = k
	s.mu.Unlock()
	return authKey{Fingerprint: fp, Comment: comment, Type: pk.Type()}, s.persist()
}

// remove drops an authorized key by fingerprint.
func (s *store) remove(fp string) error {
	s.mu.Lock()
	delete(s.keys, fp)
	s.mu.Unlock()
	return s.persist()
}

func (s *store) persist() error {
	s.mu.RLock()
	var b strings.Builder
	for _, k := range s.keys {
		b.WriteString(k.line)
		b.WriteByte('\n')
	}
	s.mu.RUnlock()
	tmp := s.authorizedPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.authorizedPath())
}
