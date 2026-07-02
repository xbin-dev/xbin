// Package vault provides the encryption barrier that protects secrets at
// rest. It mirrors the property that makes HashiCorp Vault's at-rest story
// meaningful: the master key never lives in the at-rest data — it is derived
// from a passphrase supplied at *unseal* time and held only in memory.
//
// Key hierarchy:
//
//	passphrase --Argon2id(salt)--> KEK  (key-encryption key, in memory only)
//	KEK --AES-256-GCM--> wraps -->  DEK  (data-encryption key, random)
//	DEK --AES-256-GCM--> encrypts each vault entry blob
//
// Only the *wrapped* DEK, the KDF salt, and parameters live on disk
// (`.barrier.json`). Without the passphrase the DEK cannot be recovered, so
// a stolen workspace dir / backup / snapshot yields only ciphertext.
//
// Seal/unseal lifecycle:
//   - Sealed: DEK is not in memory; Encrypt/Decrypt return ErrSealed.
//   - Unseal(passphrase): derive KEK, unwrap DEK into memory.
//   - Seal(): zero the DEK.
//   - Auto-unseal: buxond unseals at boot from BUXON_VAULT_PASSPHRASE.
//
// Honest limits (documented for operators): Go's GC gives no guaranteed
// zeroing, so the DEK/plaintext may be copied within the heap or appear in a
// core dump while unsealed — same fundamental constraint HashiCorp Vault (also
// Go) has. We mlock the DEK page and zero buffers best-effort. This barrier
// defends against theft of data at rest; it does not defend against a
// root-compromised container while the vault is unsealed.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/sys/unix"
)

var (
	ErrSealed        = errors.New("vault is sealed")
	ErrNotInited     = errors.New("vault barrier not initialized")
	ErrBadPassphrase = errors.New("wrong passphrase, or corrupt keyfile")
	ErrInited        = errors.New("vault barrier already initialized")
)

// Argon2id parameters (OWASP-ish defaults; interactive-tolerable).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB → 64 MiB
	argonThreads = 4
	keyLen       = 32 // AES-256
	saltLen      = 16
)

// keyfile is the on-disk barrier descriptor. It holds no plaintext secret:
// wrappedDEK is only recoverable with the passphrase-derived KEK.
type keyfile struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"` // "argon2id"
	Salt       []byte `json:"salt"`
	Time       uint32 `json:"time"`
	Memory     uint32 `json:"memory"`
	Threads    uint8  `json:"threads"`
	WrappedDEK []byte `json:"wrapped_dek"` // nonce || AES-GCM(KEK, DEK)
}

type Barrier struct {
	path string // .barrier.json

	mu  sync.RWMutex
	kf  *keyfile // loaded descriptor (nil until loaded/initialized)
	dek []byte   // nil when sealed
}

// Open loads (or notes the absence of) the barrier keyfile under dir.
func Open(dir string) (*Barrier, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := &Barrier{path: filepath.Join(dir, ".barrier.json")}
	if data, err := os.ReadFile(b.path); err == nil {
		var kf keyfile
		if err := json.Unmarshal(data, &kf); err != nil {
			return nil, fmt.Errorf("vault keyfile corrupt: %w", err)
		}
		b.kf = &kf
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return b, nil
}

func (b *Barrier) Initialized() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.kf != nil
}

func (b *Barrier) Sealed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dek == nil
}

type Status struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

func (b *Barrier) Status() Status {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return Status{Initialized: b.kf != nil, Sealed: b.dek == nil}
}

func deriveKEK(passphrase string, kf *keyfile) []byte {
	return argon2.IDKey([]byte(passphrase), kf.Salt, kf.Time, kf.Memory, kf.Threads, keyLen)
}

// Init sets up the barrier for the first time: generate a random DEK, wrap it
// with a KEK derived from passphrase, persist the keyfile, and leave the
// barrier unsealed. Errors if already initialized.
func (b *Barrier) Init(passphrase string) error {
	if passphrase == "" {
		return errors.New("passphrase required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.kf != nil {
		return ErrInited
	}
	kf := &keyfile{
		Version: 1, KDF: "argon2id",
		Salt: make([]byte, saltLen), Time: argonTime, Memory: argonMemory, Threads: argonThreads,
	}
	if _, err := rand.Read(kf.Salt); err != nil {
		return err
	}
	dek := make([]byte, keyLen)
	if _, err := rand.Read(dek); err != nil {
		return err
	}
	kek := deriveKEK(passphrase, kf)
	defer zero(kek)
	wrapped, err := gcmSeal(kek, dek)
	if err != nil {
		return err
	}
	kf.WrappedDEK = wrapped
	if err := b.persist(kf); err != nil {
		zero(dek)
		return err
	}
	b.kf = kf
	b.setDEK(dek)
	return nil
}

// Unseal derives the KEK from passphrase and unwraps the DEK into memory.
func (b *Barrier) Unseal(passphrase string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.kf == nil {
		return ErrNotInited
	}
	if b.dek != nil {
		return nil // already unsealed
	}
	kek := deriveKEK(passphrase, b.kf)
	defer zero(kek)
	dek, err := gcmOpen(kek, b.kf.WrappedDEK)
	if err != nil {
		return ErrBadPassphrase
	}
	b.setDEK(dek)
	return nil
}

// Seal drops the DEK from memory (best-effort zeroed and unlocked).
func (b *Barrier) Seal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dek != nil {
		_ = unix.Munlock(b.dek)
		zero(b.dek)
		b.dek = nil
	}
}

// Rekey changes the passphrase without re-encrypting entries: it re-wraps the
// existing DEK with a KEK derived from the new passphrase. Must be unsealed.
func (b *Barrier) Rekey(newPassphrase string) error {
	if newPassphrase == "" {
		return errors.New("passphrase required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dek == nil {
		return ErrSealed
	}
	kf := *b.kf
	kf.Salt = make([]byte, saltLen)
	if _, err := rand.Read(kf.Salt); err != nil {
		return err
	}
	kek := deriveKEK(newPassphrase, &kf)
	defer zero(kek)
	wrapped, err := gcmSeal(kek, b.dek)
	if err != nil {
		return err
	}
	kf.WrappedDEK = wrapped
	if err := b.persist(&kf); err != nil {
		return err
	}
	b.kf = &kf
	return nil
}

// Encrypt seals plaintext with the DEK. Output is nonce||ciphertext.
func (b *Barrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.dek == nil {
		return nil, ErrSealed
	}
	return gcmSeal(b.dek, plaintext)
}

// Decrypt opens a nonce||ciphertext blob with the DEK.
func (b *Barrier) Decrypt(blob []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.dek == nil {
		return nil, ErrSealed
	}
	return gcmOpen(b.dek, blob)
}

// --- internals ---

func (b *Barrier) setDEK(dek []byte) {
	// Best-effort: pin the page so the DEK isn't swapped to disk.
	if err := unix.Mlock(dek); err != nil {
		// Non-fatal (e.g. RLIMIT_MEMLOCK); the DEK is still only in RAM.
		_ = err
	}
	b.dek = dek
}

func (b *Barrier) persist(kf *keyfile) error {
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

func gcmSeal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func gcmOpen(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b) // discourage dead-store elimination of the wipe
}
