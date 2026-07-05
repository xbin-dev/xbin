package broker

import (
	"bytes"
	"testing"

	"github.com/magik6k/xbin/internal/vault"
)

func TestKVCodecEncrypted(t *testing.T) {
	bar, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := bar.Init("master-pw"); err != nil {
		t.Fatal(err)
	}
	b := &Broker{barrier: bar}

	bucket := "res:apps/thing/kv"
	enc, err := b.encodeKV(bucket, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if enc[0] != kvTagEnc {
		t.Fatalf("value should be tagged encrypted, got tag %#x", enc[0])
	}
	if bytes.Contains(enc, []byte("hello")) {
		t.Fatal("stored kv value leaks plaintext")
	}
	got, err := b.decodeKV(bucket, enc)
	if err != nil || string(got) != "hello" {
		t.Fatalf("round trip: %v %q", err, got)
	}
	// A value from one bucket must not decrypt under another (per-bucket key).
	if _, err := b.decodeKV("res:apps/thing/other", enc); err == nil {
		t.Fatal("cross-bucket decrypt should fail (independent keys)")
	}

	// Sealed → both directions refused.
	bar.Seal()
	if _, err := b.encodeKV(bucket, []byte("x")); err != vault.ErrSealed {
		t.Fatalf("encode while sealed: %v", err)
	}
	if _, err := b.decodeKV(bucket, enc); err != vault.ErrSealed {
		t.Fatalf("decode while sealed: %v", err)
	}
}

func TestKVCodecPlaintext(t *testing.T) {
	// No barrier configured (dev / --insecure-vault): values are tagged plaintext.
	bar, _ := vault.Open(t.TempDir())
	b := &Broker{barrier: bar}

	enc, err := b.encodeKV("res:apps/thing/kv", []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if enc[0] != kvTagPlain {
		t.Fatalf("value should be tagged plaintext, got tag %#x", enc[0])
	}
	got, err := b.decodeKV("res:apps/thing/kv", enc)
	if err != nil || string(got) != "hi" {
		t.Fatalf("round trip: %v %q", err, got)
	}
	// A legacy untagged value is returned as-is.
	legacy := []byte("legacy-raw-value")
	if out, err := b.decodeKV("res:apps/thing/kv", legacy); err != nil || string(out) != "legacy-raw-value" {
		t.Fatalf("legacy passthrough: %v %q", err, out)
	}
}
