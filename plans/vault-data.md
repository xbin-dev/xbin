# Vault-backed encryption at rest for tile data

Extends the vault barrier (`plans/auth.md` §4, `internal/vault`) from protecting
*secrets* to protecting all **tile state** — the data a component's resources
hold — so a stolen workspace dir / disk image / backup snapshot yields only
ciphertext.

## Scope (the three constraints)

1. **Code is not encrypted.** Component source lives in per-component git repos
   under the workspace tree, not under `data/`; it stays plaintext.
2. **Tile data is encrypted with vault keys.** The four resource types that hold
   state — `filesystem`, `sqlite`, `blob`, `kv` — are encrypted at rest under
   keys derived from the vault DEK.
3. **Backup tars are NOT encrypted.** `bx backup` / the archive interface stream
   *plaintext*; encrypting the archive is the archiver tile's job (it has its own
   vault for keys). So the backup path reads through the decrypted view.

**Always-on, no plaintext path.** File-backed resources (filesystem/sqlite/blob)
are *always* per-resource gocryptfs mounts and kv values are *always* envelope-
encrypted — there is no `res` vs `resenc` distinction and no plaintext resource
storage. If encryption can't run (no gocryptfs binary, or the vault is
sealed/absent) the resource is **unavailable** — the component that uses it is
held — **never silently plaintext**. A barrier is brought up automatically under
a bare `make dev` (a built-in *insecure* dev key), so dev encrypts too; only an
explicit `--insecure-vault` / `--no-auth` keeps resources plaintext (throwaway /
inspection). **DEK rotation is out of scope** (passphrase *rekey* stays O(1) — it
re-wraps the DEK, which the derived keys and all ciphertext depend on, so nothing
needs re-encrypting; rotating the DEK itself would require re-encrypting
everything and is deferred).

## The one hard problem, and the split it forces

Resources reach backends two ways:

- **Broker-mediated** (`kv`, `blob`): the backend calls buxond over HTTP, so
  buxond is in the data path and can transparently encrypt/decrypt.
- **Directly bind-mounted** (`filesystem`, `sqlite`): buxond binds the real
  directory into the sandbox and the backend touches files itself — **no
  interposition point**.

Because **the vault key must never enter a component sandbox** (backends are
least-privileged tenants), decryption must happen inside buxond's trust domain.
That rules out handing a key to the backend (e.g. SQLCipher) and dictates:

- `kv` / `blob` → encrypt in the broker.
- `filesystem` / `sqlite` → a **buxond-owned encrypting FUSE** whose decrypted
  view is bind-mounted in. We use **gocryptfs** (see "gocryptfs" below).

`blob` is grouped with the FUSE side rather than broker-encrypted: blobs are up
to 256 MiB, and a single AES-GCM seal of a whole blob would buffer it in memory,
so serving it from a gocryptfs mount (streaming `ServeFile` / `os.Create`, plus
filename encryption) is both simpler and safer. Only `kv` (values in the shared
bbolt db) is broker-encrypted.

## Key hierarchy

```
passphrase --Argon2id--> KEK --unwraps--> DEK           (as today; in memory only)
DEK --HKDF-SHA256("buxon/<label>")--> per-resource 32-byte subkey
     label "fs:<scopeKey>/<name>"  → gocryptfs password (base64) for that resource
     label "kv:<res-string>"       → AES-256-GCM key for that kv bucket's values
```

`Barrier.DeriveKey(label)` (new) yields the subkey; `Barrier.EncryptFor/DecryptFor`
wrap AES-GCM with a derived key for kv. Distinct labels are independent, so a
leaked per-resource key never crosses resources. Everything is sealed-gated:
derivation returns `ErrSealed` when the vault is sealed.

## gocryptfs

Selected over CryFS / securefs / an in-process FUSE: it's Go, MIT, independently
audited (2017), rootless-native, and matches the existing `fuse-overlayfs`
vendoring exactly (`hack/build-gocryptfs.sh` builds a fully-static
`-tags without_openssl` binary; `resenc.Resolve` finds it next to buxond). Its
metadata leak (directory structure, file sizes, counts — it encrypts contents +
names) is acceptable for tile data. buxond supplies the per-resource password on
gocryptfs's **stdin** (`-passfile /dev/stdin`), so nothing sensitive lands on
disk or in `ps`. Runtime dep: `fusermount3` (the `fuse3` package the installer
already pulls in).

`internal/resenc` (`Manager`) owns the mounts:

- ciphertext: `data/resources-enc/<scopeKey>/<name>/` (what a stolen disk sees);
- decrypted view: `.buxon/resenc/<scopeKey>/<name>/`, bind-mounted into sandboxes;
- `Ensure` inits (first use) + mounts + migrates a legacy plaintext dir;
  `UnmountAll` on seal/shutdown; `RecoverStale` lazy-unmounts a crashed buxond's
  leftovers at start.

Accessibility: in range-uid mode container-uid-0 maps to the host `buxon` user
(`sandbox/launch_linux.go`), so a buxon-owned FUSE mount is reachable by the
backend running as in-ns root with **no `allow_other`** — the same profile as
today's plain bind.

## Seal ⇒ stateful workspace down (the operational cost)

With tile data encrypted, a **sealed vault can't read any of it**. So:

- `Broker.EncryptionHold(comp)` (composed into `runner.ShouldRun`) **holds** any
  component that uses a stateful resource while the vault is sealed, and holds a
  `filesystem/sqlite/blob` user until its mount is up. Held = won't spawn (like
  `disabled`), so no half-broken backends.
- **Unseal** (`UnsealOrInit`) → `MountEncrypted` mounts every file resource
  (init/migrate as needed); held components then start lazily on next request.
- **Seal** (`apiVaultSeal`) → `SealResources` stops components that depend on
  encrypted resources (releasing their binds), then `UnmountAll`.
- `kv`/`blob` API calls while sealed return **503**.

So an auto-unseal box (`BUXON_VAULT_PASSPHRASE`) behaves as before; a
manual-unseal box boots, lets you log in, and brings stateful tiles alive on
unseal.

## Backups (constraint 3)

`writeScopeData` reads through the decrypted view (mount for fs/sqlite/blob;
`decodeKV` decrypts kv values) → **plaintext tar**, refused while sealed.
`restore` writes plaintext back into the mount (re-encrypting under the *current*
vault) and re-encodes kv — so a backup is portable across workspaces/passphrases.

⚠️ **This makes the archiver responsible for encryption.** Turning this on
without an encrypting archiver would push plaintext to S3 — a regression. The
builtin `s3-archiver` must gain client-side encryption as the companion change.

## Not encrypted (follow-ups)

- buxond metadata: `data/cron-jobs.json`, `data/backup-schedule.json`,
  `data/prefs/`, the users db. Owner/config data, not tile state.
- **No legacy migration.** Since there is no plaintext resource path, an existing
  plaintext workspace's `data/resources/…` isn't auto-encrypted on upgrade (the
  new mount starts empty). Pre-release; re-import / re-create. (Greenfield is
  unaffected.)
- kv AAD (bind value↔key), and encrypting kv key *names* (currently plaintext for
  prefix-listing) — hardening, deferred.
- **Stale mounts on workspace rename.** `RecoverStale` only scans the current
  `.buxon/resenc`, so `mv`-ing a live workspace leaks its gocryptfs mounts —
  cosmetic, cleanup deferred.

## Import validation (companion)

A `uses` target that references a nonexistent resource/component was silently
dropped at spawn — which hid a tile hard-coding its pre-rename scope (e.g.
`res:apps/test-tile/home` after installing as `apps/bx-term-tile`). `unresolvedUses`
now flags these; git-import returns them as `warnings` and the admin overview
surfaces them per component. (A self-relative resource reference so installable
tiles survive a rename is a larger follow-up.)

## Decisions (VD-*)

- **VD-1 — Always encrypted, no plaintext resource path.** File resources are
  always gocryptfs mounts; kv always envelope-encrypted. If encryption can't run,
  the resource is *unavailable* (component held), never plaintext. Cost: seal ⇒
  stateful-down; a dev key auto-encrypts `make dev`.
- **VD-2 — Key never enters the sandbox.** buxond (broker for kv, a buxond-owned
  gocryptfs for files) does all crypto; backends only ever see plaintext.
- **VD-3 — gocryptfs for file-backed resources** (filesystem/sqlite/blob),
  vendored static like fuse-overlayfs; kv is broker envelope-encrypted.
- **VD-4 — Backups are plaintext**; the archiver owns archive encryption (VD-3
  of the archive/lifecycle story). Backup/restore route through the decrypted
  view.
- **VD-5 — DEK rotation out of scope**; passphrase rekey stays O(1).
