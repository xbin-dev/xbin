package broker

import (
	"bytes"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/vault"
)

// Single-tenant mounts are reserved for filesystem resources of scopes that
// host a cap:containers component — never other types, never workspace-level
// resources, and the decision follows the grant (including a policy-ceiling
// strip, which must also strip the relaxed mount mode).
func TestResSingleTenant(t *testing.T) {
	b := testBroker(t)

	if b.resSingleTenant("apps/calendar", "filesystem") {
		t.Fatal("no cap:containers in scope yet — must not single-tenant")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/calendar", Target: ContainersCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.resSingleTenant("apps/calendar", "filesystem") {
		t.Fatal("filesystem resource of a cap:containers scope must be single-tenant")
	}
	// Conservative: only the filesystem type, only scoped resources.
	if b.resSingleTenant("apps/calendar", "sqlite") || b.resSingleTenant("apps/calendar", "blob") {
		t.Fatal("sqlite/blob must never be single-tenant")
	}
	if b.resSingleTenant("", "filesystem") {
		t.Fatal("workspace-level resources must never be single-tenant")
	}
	if b.resSingleTenant("apps/email", "filesystem") {
		t.Fatal("a scope without the cap must not single-tenant")
	}
	// The policy ceiling strips cap:containers — and with it the mount mode.
	if err := b.Users.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if b.resSingleTenant("apps/calendar", "filesystem") {
		t.Fatal("an xbin-caps deny must strip single-tenant mounting too")
	}
}

// resType resolves declared resource types for the restore path.
func TestResType(t *testing.T) {
	b := testBroker(t)
	if got := b.resType("apps/calendar", "db"); got != "sqlite" {
		t.Fatalf("resType scoped: %q", got)
	}
	if got := b.resType("apps/calendar", "nope"); got != "" {
		t.Fatalf("resType unknown: %q", got)
	}
	if got := b.resType("", "nope"); got != "" {
		t.Fatalf("resType workspace unknown: %q", got)
	}
}

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
