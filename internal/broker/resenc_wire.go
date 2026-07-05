package broker

import (
	"log/slog"
	"path/filepath"

	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/resenc"
	"github.com/magik6k/xbin/internal/util"
	"github.com/magik6k/xbin/internal/vault"
)

// Encryption-at-rest for resource data (plans/vault-data.md). There is no
// plaintext resource path: file-backed resources (filesystem/sqlite/blob) are
// ALWAYS stored as a per-resource gocryptfs mount keyed by a vault subkey, and
// kv values are ALWAYS envelope-encrypted per bucket. When encryption can't run
// (no gocryptfs binary, or the vault is sealed/absent) the resource is
// unavailable — the component that uses it is held — never silently plaintext.

// initResEnc builds the resource-encryption manager and clears any stale mounts
// left by a previous xbind. Called from New (after the barrier is opened).
func (b *Broker) initResEnc() {
	bin := resenc.Resolve()
	b.resenc = resenc.New(b.Reg.Root, bin, b.barrier.DeriveKey)
	b.resenc.RecoverStale()
	if bin == "" {
		slog.Warn("resource encryption: gocryptfs not found — a component that uses a filesystem/sqlite/blob resource will be HELD until it's available (build it via `make build`, or set XBIN_GOCRYPTFS)")
	}
}

// fileBackedType: resource types stored as a gocryptfs mount (a dir on disk).
// kv is the exception — it lives in the shared bbolt db and is envelope-encrypted
// per bucket instead.
func fileBackedType(t string) bool {
	switch t {
	case "filesystem", "sqlite", "blob":
		return true
	}
	return false
}

// resLabel is the stable key-derivation / mount identity for a resource.
func resLabel(scopeKey, name string) string { return scopeKey + "/" + name }

// fsReady reports whether a file-backed resource can be served right now:
// gocryptfs present, a vault configured + unsealed, and its mount up.
func (b *Broker) fsReady(scopeKey, name string) bool {
	return b.resenc != nil && b.resenc.Available() &&
		b.barrier != nil && b.barrier.Initialized() && !b.barrier.Sealed() &&
		b.resenc.Mounted(scopeKey, name)
}

// fsResPath returns the decrypted-mount path handed to a same-scope backend for
// a filesystem/sqlite resource (blob uses it via blobAccess). sqlite points at
// the .sqlite file inside the mount.
func (b *Broker) fsResPath(scope, name string, sqlite bool) string {
	dir := b.resenc.MountDir(util.ScopeKey(scope), name)
	if sqlite {
		return filepath.Join(dir, name+".sqlite")
	}
	return dir
}

// EncryptionHold reports whether a component must be kept from spawning because
// the tile state it depends on isn't currently accessible: a file resource whose
// encrypted mount isn't up (gocryptfs missing, vault sealed, or not yet mounted),
// or a kv bucket behind a sealed vault. Composed into runner.ShouldRun.
func (b *Broker) EncryptionHold(comp string) bool {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return false
	}
	sealedVault := b.barrier != nil && b.barrier.Initialized() && b.barrier.Sealed()
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.parseRes(u.Target)
		if !ok || res == nil {
			continue
		}
		switch {
		case fileBackedType(res.Type):
			if !b.fsReady(util.ScopeKey(rt.Scope), rt.Name) {
				return true
			}
		case res.Type == "kv":
			if sealedVault {
				return true // can't decode kv values while sealed
			}
		}
	}
	return false
}

// MountEncrypted ensures every declared file-backed resource has its decrypted
// view mounted. Called after the vault is unsealed and at the end of each
// (re)provision. No-op while encryption can't run.
func (b *Broker) MountEncrypted() {
	if b.resenc == nil || !b.resenc.Available() ||
		b.barrier == nil || !b.barrier.Initialized() || b.barrier.Sealed() {
		return
	}
	b.forEachFileRes(func(scope, name, _ string) {
		scopeKey := util.ScopeKey(scope)
		if _, err := b.resenc.Ensure(resLabel(scopeKey, name), scopeKey, name); err != nil {
			slog.Error("resource encryption: mount failed", "res", resLabel(scopeKey, name), "err", err)
		}
	})
}

// SealResources stops the components that depend on file resources and unmounts
// every decrypted view, so a sealed vault leaves only ciphertext on disk. Called
// from the seal API after the barrier is sealed.
func (b *Broker) SealResources() {
	if b.resenc == nil {
		return
	}
	if b.StopBackend != nil {
		for _, c := range b.Reg.Components() {
			if b.componentUsesFileRes(c) {
				b.StopBackend(c.Path)
			}
		}
	}
	b.resenc.UnmountAll()
}

func (b *Broker) componentUsesFileRes(c *registry.Component) bool {
	for _, u := range c.Manifest.Uses {
		if _, res, ok := b.parseRes(u.Target); ok && res != nil && fileBackedType(res.Type) {
			return true
		}
	}
	return false
}

// forEachFileRes calls fn(scope, name, type) for every declared file-backed
// resource (filesystem/sqlite/blob) across the workspace and its scopes.
func (b *Broker) forEachFileRes(fn func(scope, name, rtype string)) {
	do := func(scope string, resources map[string]registry.Resource) {
		for name, res := range resources {
			if fileBackedType(res.Type) {
				fn(scope, name, res.Type)
			}
		}
	}
	do("", b.Reg.Workspace().Resources)
	for scope, sm := range b.Reg.Scopes() {
		do(scope, sm.Resources)
	}
}

// kv values carry a 1-byte storage tag so the store is self-describing and can
// hold a mix of encrypted and (dev/insecure) plaintext values:
//
//	0x01 | nonce||ciphertext   — encrypted with DeriveKey("kv:<bucket>")
//	0x00 | plaintext           — stored while no barrier was configured
//	(anything else)            — legacy untagged plaintext (returned as-is)
const (
	kvTagPlain = 0x00
	kvTagEnc   = 0x01
)

// encodeKV wraps a kv value for storage, encrypting it whenever a barrier is
// configured. Returns vault.ErrSealed when the barrier is sealed.
func (b *Broker) encodeKV(bucket string, val []byte) ([]byte, error) {
	if b.barrier != nil && b.barrier.Initialized() {
		ct, err := b.barrier.EncryptFor("kv:"+bucket, val)
		if err != nil {
			return nil, err
		}
		return append([]byte{kvTagEnc}, ct...), nil
	}
	return append([]byte{kvTagPlain}, val...), nil
}

// decodeKV unwraps a stored kv value, decrypting when tagged encrypted.
func (b *Broker) decodeKV(bucket string, raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	switch raw[0] {
	case kvTagEnc:
		if b.barrier == nil {
			return nil, vault.ErrSealed
		}
		return b.barrier.DecryptFor("kv:"+bucket, raw[1:])
	case kvTagPlain:
		return raw[1:], nil
	default:
		return raw, nil // legacy untagged
	}
}
