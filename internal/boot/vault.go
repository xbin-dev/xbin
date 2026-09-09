package boot

// VaultMode is how the encryption barrier (docs/auth.md §vault,
// plans/vault-data.md) comes up at boot. It protects both secrets and
// resource data (kv/filesystem/sqlite/blob) at rest: secure by default in
// production, ON by default under a bare `make dev` too.
type VaultMode int

const (
	// VaultFromEnv: XBIN_VAULT_PASSPHRASE is set → auto-init/unseal at boot.
	VaultFromEnv VaultMode = iota
	// VaultPlaintext: --insecure-vault or --no-auth → plaintext at rest (the
	// opt-out; the frictionless/harness path — the barrier stays uninitialized).
	VaultPlaintext
	// VaultDevKey: --dev without the above → auto-init with a built-in DEV
	// key so `make dev` encrypts by default (INSECURE; dev only).
	VaultDevKey
	// VaultSealed: production, no env, barrier set up → start SEALED; an
	// admin unseals.
	VaultSealed
	// VaultLocked: production, no env, no barrier → start LOCKED until an
	// admin unseals (creates the barrier on first use; the passphrase never
	// touches env — the strongest mode).
	VaultLocked
)

func (m VaultMode) String() string {
	return [...]string{"from-env", "plaintext", "dev-key", "sealed", "locked"}[m]
}

// ResolveVaultMode picks the mode for these settings, first match wins;
// `initialized` is whether the workspace's barrier already exists.
func ResolveVaultMode(c *Config, initialized bool) VaultMode {
	switch {
	case c.VaultPassphrase != "":
		return VaultFromEnv
	case c.InsecureVault || c.NoAuth:
		return VaultPlaintext
	case c.Dev:
		return VaultDevKey
	case initialized:
		return VaultSealed
	default:
		return VaultLocked
	}
}

// devVaultPassphrase brings the vault up automatically under --dev/--no-auth so
// encryption-at-rest is ON by default while dogfooding (a bare `make dev`
// encrypts filesystem/sqlite/blob resources + kv). It is a FIXED key baked into
// the source — INSECURE by construction — and is never used outside dev/no-auth;
// real deployments supply XBIN_VAULT_PASSPHRASE or unseal manually.
const devVaultPassphrase = "xbin-dev-insecure-vault"
