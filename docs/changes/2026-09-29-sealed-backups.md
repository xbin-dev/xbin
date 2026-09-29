# 2026-09-29 — Backups are sealed (PD-25, PD-56)

## What changed

In a workspace with a vault barrier (every workspace with a vault
passphrase, `XBIN_VAULT_PASSPHRASE` or one set in the admin console),
**every archive xbind writes is now encrypted by xbind itself** — main
archives, deployment archives, and a new **data archive** that now holds a
scope root's main data (`.data.<tile-key>`, written just before the main
archive, whose manifest names it; schema 3). Existing workspaces seal from
their **next** backup.

- The format: `XBINSEAL`, a cleartext header naming the **backup key**'s
  opaque id, then AES-256-GCM in 64 KiB chunks over the same tar
  ([14-lifecycle.md](/docs/overview/14-lifecycle.md) §Sealed archives).
- Backup keys are random, one per subject (a tile's source, a namespace's
  data), wrapped by the vault's data key in `data/vault/.backup-keys/`. They
  are in no archive. **Deleting one crypto-erases that data in every
  archive**: `bx backup erase <tile> --data|--all`.
- An archiver sees the object key, the backup key's id (the PUT's
  `X-XBin-Backup-Subkey`), sizes, times and the header — never plaintext or
  key material. A new, optional archiver route, `POST /archive/erase
  {"subkeys": [...]}`, deletes the versions sealed under erased keys.
- xbind extracts single files itself (`bx restore --file`, the admin
  console's *file…*): an archiver can't read a sealed archive.
- A **sealed vault now stops every backup**, not only data backups: no
  archive can be sealed without the data key. So does a vault **not set up
  yet** (production before the first `bx vault unseal`: `bx doctor` and
  `bx backup keys status` say `vault-locked`), which used to write plain
  archives. Scheduled runs log and skip, as before.
- **Plaintext archives still restore**, whatever their age. The
  plaintext-vault mode (`--insecure-vault`, `--no-auth`) keeps writing
  today's plain schema-1 archives, data inline.
- Retention deletes a data archive when no kept main archive names it. A
  main archive whose data archive is missing restores its source and
  terminal layer and says so (`dataMissing`).

## Who's affected

- **Everyone who relies on backups for disaster recovery.** Restoring sealed
  archives on a *new machine* needs this workspace's backup keys, which live
  only in this workspace. Until you export them, admins see an alert ("N
  backup keys aren't in any export yet") and `bx doctor` says so.
- **Anyone who reads archives straight from the bucket** (a script that
  untars them, the archiver's own single-file route): archives made from now
  on are ciphertext. Restore through xbind.
- **Downgrades.** An older xbind can't restore archives made from now on: it
  refuses them (it finds no `backup.json`) without writing anything, and the
  archives made before the upgrade still restore on it.
- **Custom archiver tiles** store opaque bytes, as before. Two rules of the
  contract now matter: `PUT` must answer `{"version": …}` with a version of
  letters, digits and `._:-` (at most 128, starting with a letter or digit)
  — a backup whose data archive gets no such version fails, since no main
  archive may name a version xbind can't fetch again — and an error answer
  must mean nothing was kept. They may store `X-XBin-Backup-Subkey` and
  implement `POST /archive/erase` to delete erased data; without it the dead
  versions stay (unreadable) until retention prunes them.
- **Automation holding `xbin` admin**: exporting or importing the key bundle
  and erasing backups are a person's acts — an admin in their own session
  (`bx`, the admin console). A tile's backend, terminal or agent gets 403.

## How to migrate

1. **Export the key bundle now**, and keep it with the vault passphrase:

   ```sh
   bx backup keys export > keys.xbk
   ```

   (or *export key bundle* on the admin console's Backup tab). The bundle
   holds the vault's barrier descriptor and the wrapped backup keys: without
   the passphrase it opens nothing, but **with the passphrase in force when
   it was exported it opens the workspace's data key** — every vault secret
   and all data at rest, not only the backups. Keep the two apart. Export
   again after erasing backups and after changing the vault passphrase, and
   destroy older bundles: they still hold the erased keys, and still open
   with the old passphrase (the data key never rotates).
2. **Disaster recovery onto a new machine:** set up the new workspace's vault
   (any passphrase), then

   ```sh
   bx backup keys import keys.xbk     # asks for the OLD vault passphrase
   ```

   and restore as usual. An archive whose keys were never imported is
   refused: "this backup was sealed by another workspace: import its keys
   (bx backup keys import)".
3. **Optional:** `bx builtin update s3-archiver` (v3) so erased backups are
   deleted from the bucket too.
4. Plaintext archives made before the upgrade can't be erased by key: delete
   them at the archiver when their data must go.

## Why

Backups used to leave xbind as plaintext tars, trusting the archiver to
encrypt them (VD-4), and a backup could never be erased short of finding and
deleting every copy. With one key per subject, the archiver holds only
ciphertext, and deleting a key — a tile's data, a partition, a person's
data — erases that data in every backup at once (decisions PD-25 and
PD-56).
