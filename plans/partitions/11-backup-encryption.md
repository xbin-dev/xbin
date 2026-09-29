# 11 — Sealed backups and the key hierarchy (PD-25, PD-56)

The owner's ruling on PD-25 grows backups beyond partitions:
- **every** archive xbind writes is encrypted;
- the key hierarchy supports crypto-erase: the vault key encrypts
  per-partition and per-namespace subkeys, and archives are encrypted under
  those subkeys;
- archivers see subkey *names* as metadata only, so they can delete, and
  never key material;
- deleting a subkey erases that data in every existing backup;
- old unencrypted archives still restore.

This work doesn't depend on the partition fabric. Its general part (work
pack F17a) can start at once.

## Current behaviour

- **Format.** An archive is a plain, uncompressed tar. `backup.json` comes
  first, then `source/`, `data/…`, `term/` and `deployments/`
  (`internal/backup/backup.go:7-27`). The schemas are `Schema = 1` (main,
  `:47`) and `SchemaDeployment = 2` (`:52`); `MaxSchema` is at `:55`, and
  the manifest struct at `:82-104`. `NewReader` requires `backup.json` first
  and refuses newer schemas (`:345-362`).
- **Plaintext by decision.** xbind reads data through the decrypted mounts
  and writes it in the clear. The code says "Backups are plaintext
  (encryption is the archiver's job)" (`internal/broker/backup.go:148`);
  see also VD-4 (`plans/vault-data.md:159-161`), docs/resources.md:98-100
  and docs/overview/14-lifecycle.md:153-157. The vault is never archived
  (LC-2).
- **Objects.**
  - The main archive is keyed `CompKey(tile)` (`backup.go:47`) and holds
    the manifest, source, terminal layer and deployment section, plus the
    scope data when the tile roots its scope (`writeBackup`, `:72-134`).
  - Deployment namespaces get their own archives,
    `.deployments.<TileKey>.<dep>` (`internal/broker/backup_deploy.go:586-591`,
    `:597-640`). They are written first, and the main manifest lists their
    versions (`backupTile`, `backup.go:276-291`).
- **Streaming.** `putArchive` pipes a `backup.Writer` into the archiver's
  `PUT` (`backup.go:295-315`). `archiveDo` serves every archiver call
  through the proxy as the owner and buffers the response
  (`backup.go:253-262`).
- **Archiver contract** (LC-3, docs/overview/14-lifecycle.md:164-207): `PUT
  /archive/{key}`, `GET …/versions`, `GET …/versions/{v}`, `GET
  …/{v}/file?path=` and `DELETE …/versions/{v}`. The archiver sees the
  whole plaintext tar. The s3-archiver stores `<prefix>/<key>/<version>.tar`
  (`builtin-tiles/s3-archiver/backend/main.go:54`) and parses the tar itself
  for single-file restores (`:198`). xbind prunes by retention (`pruneKey`,
  `internal/broker/backup_cron.go:127-148`).
- **Restore.** Every restore parses through `backup.NewReader`:
  - a tile restore (`internal/broker/restore.go:48`);
  - a namespace restore (`internal/broker/backup_restore.go:164`);
  - the listed deployment archives (`backup_restore.go:47-68`).
- **Keys.** The barrier chain is passphrase → Argon2id KEK → wrapped random
  DEK (`internal/vault/barrier.go:6-10`, file `data/vault/.barrier.json`).
  The DEK is memory-only while unsealed (`:83`). `DeriveKey(label)` is an
  HKDF of the DEK (`:277-289`), and `EncryptFor`/`DecryptFor` pair it with
  AES-GCM (`:294-311`). Data backups fail while the vault is sealed
  (`backup.go:150-152`); scheduled runs log and skip
  (`backup_cron.go:109-113`). The plaintext-vault mode has no barrier at
  all (`internal/boot/vault.go:12-14`).

## The change

### 1. Scope

Every archive xbind writes is **sealed**:
- main archives;
- the new main-data archives;
- deployment archives;
- the new partition archives (§2).

The one exception is the plaintext-vault mode (`--insecure-vault`,
`--no-auth`). It has no DEK, so archives stay today's tars, and `bx doctor`
says so.

### 2. The key hierarchy

```
vault passphrase ─Argon2id→ KEK ─wraps→ DEK                    (today, barrier.go:6-10)
DEK ─EncryptFor("backup-subkey:"+id)→ wrapped subkey            (new: data/vault/.backup-keys/<id>.json)
subkey ─HKDF-SHA256(salt of the archive, "xbin/archive/v1")→ archive key ─AES-256-GCM STREAM→ archive body
```

- **Subkeys are random 256-bit keys**, not HKDF labels of the DEK: a derived
  key exists for as long as the DEK does, so it could never be erased.
- Each subkey has a file `{schema: 1, id: "bk-<32 hex>", subject, gen,
  created, wrapped}`. The `id` is opaque and random; it is the subkey's
  **name**, the only thing an archiver ever sees.
- **One subkey per subject, and one archive object per subkey:**

| Subject | Archive object (the archiver key) | Contents | Erased when |
|---|---|---|---|
| `tile:<TileKey>` | main archive, `CompKey(tile)` (today's key) | manifest, source, terminal layer, deployment record/checkpoints/registrations | only by an admin's `bx backup erase <tile> --all` (a removed tile's archives stay restorable, as today) |
| `ns:<main namespace>` | **new** `.data.<TileKey>` | the scope's main data (today inside the main archive) | a mode-switch wipe (01 §2.5); `bx backup erase <tile> --data` |
| `ns:.deployments/<escS>/<dep>` | `.deployments.<TileKey>.<dep>` (today's) | the deployment's data | a mode-switch wipe; `bx backup erase` (removing a deployment keeps its archives, as today) |
| `part:<TileKey>/<dep>/<pkey>` | **new** `.partitions.<TileKey>.<dep>.<pkey>` | the partition's namespace data, its vault file (values still barrier-sealed), its registration files, `ns.json`, `partition.json` | the partition's purge or sweep (user deleted, tile removed; after the retention), its reset (by the person or an admin), a mode-switch wipe |

- After an erase, the subject gets a **new** subkey (`gen + 1`) at its next
  backup.
- **Tombstones** live in `data/vault/.backup-keys/erased.json`: `{id,
  subject, gen, erasedAt, reason, by}`. They are metadata only, so a
  restore can say *why* an archive is unreadable.
- **Where keys live:** only in `data/vault/.backup-keys/`. The directory is
  broker-only, masked from every sandbox, and a `.`-level no older binary
  lists. A key is unwrapped in memory for the length of one backup or
  restore. Keys never go into an archive, to an archiver, into an API
  response or into a sandbox.

### 3. Erase (crypto-erase)

1. Delete the subkey file (fsync the directory) and drop any cached copy.
2. Append the tombstone.
3. Collect the garbage at the archiver: `POST /archive/erase {subkeys:
   [...]}` (§6). An archiver without the route leaves the dead versions
   until retention prunes them. They are unreadable either way.

Triggers are listed in the table above. A mode switch erases every `ns:`
and `part:` subkey of the tile, but not `tile:`: source backups survive a
switch. Erases are audited through slog and recorded in the partition's or
the tile's history.

### 4. Sealed format and restore

```
"XBINSEAL"        8 bytes; every tar xbind writes begins with the name "backup.json"
u32 big-endian    header length (≤ 4 KiB)
header            cleartext JSON {"v":1, "subkey":"bk-…", "salt":"<b64, 32 bytes>",
                  "chunk":65536, "kind":"main|data|deployment|partition", "created":"…"}
body              AES-256-GCM, STREAM construction: key = HKDF(subkey, salt, "xbin/archive/v1");
                  chunk i: nonce = 11-byte big-endian i ‖ 1 byte (1 on the last chunk);
                  chunk 0's AAD = the header bytes
plaintext         today's tar, unchanged layout (backup.json first)
```

- **Writer.** `backup.NewSealedWriter(w, subkey, id, kind)` wraps the pipe
  in `putArchive`. It uses only the standard library's AES-GCM and the HKDF
  the barrier already uses (`barrier.go:283`), so there is no new
  dependency. `PUT` also carries `X-XBin-Backup-Subkey: <id>` (§6).
- **Reader.** `backup.Open(r, keys) (*Reader, error)` peeks the first 8
  bytes. `XBINSEAL` means decrypt, then `NewReader`; anything else goes
  straight to `NewReader`, exactly as today. Every restore path switches to
  `Open`.
- **Main archive split.** `writeBackup` stops writing scope data into the
  main archive; `backupTile` writes the `.data.<TileKey>` archive first, the
  same way deployment archives are written. The main manifest gets
  `SchemaSplit = 3` (`MaxSchema = 3`) and a `data: {key, version}` field
  beside `deployments.archives`. Plaintext-vault workspaces keep Schema 1
  with the data inline.
- **Single-file restore** (`backup.go:538-551`). An archiver can't parse a
  sealed tar, so xbind fetches the version and extracts the member itself
  for every archive, sealed or not. Restores already buffer whole archives.
  The archiver's `/file` route stays in the contract but new xbinds stop
  calling it.

**Restore rules:**

| Archive | Rule |
|---|---|
| plaintext (Schema 1/2), any age | restores as today (never break users) |
| sealed, subkey present | restores |
| sealed, subkey erased | refused: `this backup's data was erased on <date> (<reason>)`. A main archive whose data archive is erased restores source and terminal layer only, and says so |
| sealed, subkey unknown | refused: `this backup was sealed by another workspace: import its keys (bx backup keys import)` |
| a partition archive | same tile and same user id. Same uid: restore after a typed confirmation, by the person or an admin. Different uid (the id was deleted and recreated): only an admin's `--to <id>`, audited, with the person notified. Never into another id. 409 while the tile isn't partitioned now |
| a plaintext archive older than a mode switch of its tile | restores only into global's namespace, after a typed confirmation that names the switch ("this brings back data the switch on `<date>` deleted"). Sealed ones were erased by the switch |

Restoring into partitions never uses main or data archives. A main archive
never restores partition rows: its manifest lists global's cron and bus
rows only (03 §D, S15).

### 5. Disaster recovery

The keystore is in no archive, so a new machine needs it.

- **Export:** `bx backup keys export > keys.xbk` (admin), or a button on
  the admin tile's Backup tab. It writes `{schema, workspace, created,
  barrier: <data/vault/.barrier.json>, keys: [<subkey files, wrapped under
  the DEK>], erased: [<tombstones>]}`. Without the vault passphrase it is
  useless: the barrier file wraps the DEK under the passphrase's KEK.
- **Import:** `bx backup keys import keys.xbk` (admin, on the new
  workspace). It prompts for the old passphrase, unwraps the old DEK in
  memory, re-wraps each subkey under this workspace's DEK, and appends the
  tombstones. It never adopts the old DEK or passphrase.
- **Nudges:**
  - an admin `/alerts` entry, "N backup keys aren't in any export yet";
  - `bx doctor`;
  - the Backup tab shows the last export and "erased since your last
    export: n".
- **Exports against erases:** a bundle exported before an erase still holds
  that subkey (AR-20). The docs say to re-export after erasures and destroy
  old bundles.

### 6. Archiver contract (additive)

- `PUT /archive/{key}` carries `X-XBin-Backup-Subkey: bk-…`, which
  archivers **may** store.
- `GET /archive/{key}/versions` rows **may** include `"subkey"`.
- **New, optional:** `POST /archive/erase {"subkeys": ["bk-…"]}` →
  `{"deleted": n}` deletes every version sealed under those subkeys, across
  keys. A 404 or 405 means it is unsupported.
- What an archiver sees:
  - the object key (a readable CompKey prefix, as today);
  - subkey ids;
  - sizes and times;
  - the cleartext header (`kind`, `created`).

  It never sees plaintext or key material.
- **s3-archiver** (`builtin-tiles/s3-archiver/backend/main.go`, `s3.go`):
  - A PUT with the header also writes an empty marker
    `<prefix>/.subkeys/<id>/<key>/<version>`. S3 listings return only key,
    size and time (`s3.go:226-264`), so the metadata lives in names.
  - `/archive/erase` lists `.subkeys/<id>/` and deletes each named version
    and its marker (`s3.go:213-223`).
  - `getFile` (`main.go:176-211`) answers 422 `sealed archive: xbind
    extracts it` when an object starts with the magic.
  - Its client-side encryption item in plans/vault-data.md:123-124 is
    superseded, because xbind now seals.
- Older archivers keep working, since they store opaque bytes; they just
  get no erase garbage collection.

### 7. Rollout and compatibility

- **New workspaces** seal from the first backup.
- **Existing workspaces** seal from their next backup. Their plaintext
  archives are untouched and still restore. A persistent admin alert stays
  until the first key export. §H.3 of 90 holds the one-release opt-in
  alternative.
- **Migration note:** `docs/changes/<date>-sealed-backups.md`, linked from
  the changelog, says three things:
  - DR onto a new machine needs `bx backup keys export`;
  - archives made from now on can't be restored by an older xbind;
  - `bx builtin update s3-archiver` adds erase garbage collection
    (optional).
- **A sealed vault** now blocks every backup, not only data backups: no
  archive can be sealed without the DEK. Scheduled runs skip with the
  existing warning. Backends are already stopped while sealed, through
  `SealResources`.
- **Downgrade:** an older xbind can't read sealed archives (its `NewReader`
  finds no `backup.json`); it writes plaintext ones of its own.
- **Docs to change:**
  - docs/overview/14-lifecycle.md (`:153-157` and the archiver section);
  - docs/resources.md:98-100 and docs/overview/10-resources.md:265-266;
  - docs/auth.md §Encryption at rest (`:462-500`);
  - docs/protocol.md backup rows (`:2297-2358`, `:2705-2744`);
  - docs/bx.md;
  - builtin-tiles/s3-archiver/API.md;
  - plans/vault-data.md VD-4, marked superseded.
- **Decisions:** a DECISIONS.md entry supersedes VD-4 and LC-3's
  "plaintext tar in".

## Wire (for docs/protocol.md, docs/overview/14-lifecycle.md)

- The sealed archive format (§4) and `backup.json` `schema: 3` with
  `data`.
- Archiver: `X-XBin-Backup-Subkey`, the optional `versions[].subkey` and
  `POST /archive/erase`.
- `POST /api/xbin/backup-keys/export` and `POST
  /api/xbin/backup-keys/import` (admin), plus `POST /api/xbin/backup/erase
  {component, what: "data"|"all"}` (admin).
- New restore error texts (§4), and the `/alerts` kind `backup-keys`.
- `bx backup keys export|import` and `bx backup erase`.

## Tests

- `internal/backup`:
  - a seal/open round trip at 0 bytes, 1 chunk, many chunks, and exactly
    the chunk size;
  - tampering with the header, a chunk or the order is caught, and a
    truncation (missing last flag) fails;
  - `Open` on a plaintext tar equals `NewReader` (a golden of an archive
    from master restores byte-identically).
- `broker`:
  - a sealed main plus a data archive round trip;
  - no plaintext marker in any object (grep a known string);
  - erasing the `ns:` subkey leaves the source restorable and refuses the
    data with the reason;
  - an unknown subkey names the import;
  - partition archive rules: same uid; recreated uid only with `--to`;
    another id refused; a non-partitioned tile gives 409;
  - a pre-switch plaintext archive needs the typed confirmation and lands
    in global;
  - a mode switch erases `ns:`/`part:` but not `tile:`;
  - user delete, then sweep, erases `part:`;
  - a sealed vault skips backups with the warning.
- keys: export → import into a second workspace (a different passphrase)
  restores; the old DEK is never persisted; tombstones travel with the
  bundle.
- s3-archiver: erase deletes exactly the versions of the named subkeys (a
  fake S3); `getFile` on a sealed object gives 422.
- Integration (`test/`): upgrade a workspace holding plaintext archives,
  restore one, back up (sealed), restore that, erase, restore refused;
  the previous release's xbind restores the old plaintext archive and
  refuses the sealed one.
