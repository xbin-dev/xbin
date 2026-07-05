package vault

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBarrierRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.Initialized() {
		t.Fatal("fresh barrier should not be initialized")
	}
	if err := b.Init("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if b.Sealed() {
		t.Fatal("barrier should be unsealed after Init")
	}

	secret := []byte(`{"imap-pass":"hunter2"}`)
	ct, err := b.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hunter2")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	pt, err := b.Decrypt(ct)
	if err != nil || !bytes.Equal(pt, secret) {
		t.Fatalf("round trip failed: %v %q", err, pt)
	}

	// The keyfile on disk must not contain the passphrase or the DEK plainly.
	kf, _ := os.ReadFile(filepath.Join(dir, ".barrier.json"))
	if bytes.Contains(kf, []byte("staple")) || bytes.Contains(kf, []byte("hunter2")) {
		t.Fatal("keyfile leaks passphrase/secret")
	}
	var parsed keyfile
	if json.Unmarshal(kf, &parsed) != nil || parsed.KDF != "argon2id" || len(parsed.WrappedDEK) == 0 {
		t.Fatalf("keyfile malformed: %s", kf)
	}
}

func TestSealBlocks(t *testing.T) {
	b, _ := Open(t.TempDir())
	_ = b.Init("pw")
	ct, _ := b.Encrypt([]byte("x"))
	b.Seal()
	if !b.Sealed() {
		t.Fatal("should be sealed")
	}
	if _, err := b.Encrypt([]byte("x")); err != ErrSealed {
		t.Fatalf("encrypt while sealed: %v", err)
	}
	if _, err := b.Decrypt(ct); err != ErrSealed {
		t.Fatalf("decrypt while sealed: %v", err)
	}
}

func TestUnsealPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	b1, _ := Open(dir)
	_ = b1.Init("s3cret")
	ct, _ := b1.Encrypt([]byte("payload"))

	// Reopen (simulates a restart): must load the keyfile, start sealed,
	// and require the passphrase.
	b2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !b2.Initialized() || !b2.Sealed() {
		t.Fatal("reopened barrier should be initialized and sealed")
	}
	if err := b2.Unseal("wrong"); err != ErrBadPassphrase {
		t.Fatalf("wrong passphrase should fail with ErrBadPassphrase, got %v", err)
	}
	if err := b2.Unseal("s3cret"); err != nil {
		t.Fatalf("correct passphrase should unseal: %v", err)
	}
	pt, err := b2.Decrypt(ct)
	if err != nil || string(pt) != "payload" {
		t.Fatalf("decrypt after reopen: %v %q", err, pt)
	}
}

func TestDeriveKey(t *testing.T) {
	dir := t.TempDir()
	b, _ := Open(dir)
	_ = b.Init("master")

	a1, err := b.DeriveKey("fs:apps/thing/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(a1) != keyLen {
		t.Fatalf("derived key len = %d, want %d", len(a1), keyLen)
	}
	a2, _ := b.DeriveKey("fs:apps/thing/store")
	if !bytes.Equal(a1, a2) {
		t.Fatal("same label must derive the same key")
	}
	other, _ := b.DeriveKey("kv:apps/thing/store")
	if bytes.Equal(a1, other) {
		t.Fatal("different labels must derive different keys")
	}

	// Sealed → refused.
	b.Seal()
	if _, err := b.DeriveKey("fs:apps/thing/store"); err != ErrSealed {
		t.Fatalf("DeriveKey while sealed: %v", err)
	}

	// Survives a restart: reopen, unseal with the same passphrase, same key.
	b2, _ := Open(dir)
	if err := b2.Unseal("master"); err != nil {
		t.Fatal(err)
	}
	a3, _ := b2.DeriveKey("fs:apps/thing/store")
	if !bytes.Equal(a1, a3) {
		t.Fatal("derived key must be stable across unseal/restart")
	}
}

func TestRekey(t *testing.T) {
	dir := t.TempDir()
	b, _ := Open(dir)
	_ = b.Init("old")
	ct, _ := b.Encrypt([]byte("v"))
	if err := b.Rekey("new"); err != nil {
		t.Fatal(err)
	}
	// Old data still decrypts (DEK unchanged); new passphrase unseals.
	if pt, _ := b.Decrypt(ct); string(pt) != "v" {
		t.Fatal("rekey must preserve DEK / existing ciphertext")
	}
	b.Seal()
	if err := b.Unseal("old"); err != ErrBadPassphrase {
		t.Fatal("old passphrase must no longer work")
	}
	if err := b.Unseal("new"); err != nil {
		t.Fatalf("new passphrase must unseal: %v", err)
	}
}

func TestCheckPassphrase(t *testing.T) {
	b, _ := Open(t.TempDir())
	if err := b.CheckPassphrase("x"); err != ErrNotInited {
		t.Fatalf("uninitialized: want ErrNotInited, got %v", err)
	}
	_ = b.Init("right")
	// Works while unsealed (where Unseal would no-op without verifying) …
	if err := b.CheckPassphrase("right"); err != nil {
		t.Fatalf("correct passphrase rejected: %v", err)
	}
	if err := b.CheckPassphrase("wrong"); err != ErrBadPassphrase {
		t.Fatalf("wrong passphrase: want ErrBadPassphrase, got %v", err)
	}
	// … and must not disturb the unsealed state.
	if b.Sealed() {
		t.Fatal("CheckPassphrase must not seal the barrier")
	}
}
