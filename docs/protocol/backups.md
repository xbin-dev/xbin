# Protocol: backup and restore

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: backing a tile up, restoring it, schedules, backup keys,
erasure, and a person's partition backups.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Backup and restore


```
POST   /backup                     admin. body {component} — build a self-
                                   describing tar (source + scope data + terminal
                                   env + a manager tile's sandbox definitions,
                                   never their state; NOT vault/env-layer) and
                                   stream it to the component's bound @archive
                                   provider. {ok, version}
                                   A tile with deployments adds deployments/
                                   (its record and checkpoint store) right
                                   after backup.json, and a deployments
                                   section {record, checkpoints} in the
                                   manifest (schema still 1); a tile without
                                   one gets today's archive. The archive
                                   also carries the registration files of
                                   its deployments beyond main. When the
                                   primary isn't main, every backup first
                                   archives the primary's data in a
                                   deployment archive of its own (key
                                   .deployments.<tile-key>.<name>, manifest
                                   schema 2, naming its deployment), which
                                   the tile's archive lists; other
                                   deployments' data is archived only on
                                   request (POST /deployments/backup, or a
                                   deployment's schedule), under its own key,
                                   so it never uses the tile's retention.
                                   Readers take schema 1 and 2; an older
                                   xbind refuses a deployment archive.
                                   Sealed backups (docs/overview/14-
                                   lifecycle.md §Sealed archives): with a
                                   vault barrier every archive is sealed —
                                   XBINSEAL, a cleartext header naming the
                                   backup subkey's id, then AES-256-GCM in
                                   64 KiB chunks over the same tar — under
                                   its subject's key (the main archive:
                                   tile:, the data: ns:<namespace>), and the
                                   PUT carries X-XBin-Backup-Subkey: bk-….
                                   A scope root's main data goes to a data
                                   archive of its own (key .data.<tile-key>,
                                   schema 3, kind "data"), written before
                                   the main archive, whose manifest (schema
                                   3) names it: data {key, version, subkey}.
                                   Both manifests carry the same random
                                   backupId, and a restore pairs them only
                                   when it and the key match. A scope that
                                   declares no resource gets no data
                                   archive. The data PUT must answer a
                                   version of [A-Za-z0-9][A-Za-z0-9._:-]
                                   (≤ 128), or the backup fails before the
                                   main archive is written. The plaintext-
                                   vault mode (--insecure-vault, --no-auth)
                                   writes today's plain tars. 502 while the
                                   vault is sealed, and while it isn't set
                                   up yet (no barrier, not the plaintext-
                                   vault mode): no archive is written.
                                   Readers take schema 1-3 and plaintext
                                   archives of any age; an older xbind
                                   refuses a sealed archive.
                                   A partitioned tile's people's partitions
                                   (docs/partitions.md §Backups) are
                                   archived after the main archive, each
                                   under a key of its own
                                   (.partitions.<tile-key>.<dep>.<partition
                                   id>, schema 3, kind "partition"), sealed
                                   under that partition's own backup key
                                   (part:<tile-key>/<dep>/<partition id>);
                                   the main, data and deployment archives
                                   hold none of it (the main manifest lists
                                   the global instance's cron jobs and bus
                                   subscriptions only). A partition that
                                   can't be archived doesn't fail the
                                   backup; in the plaintext-vault mode none
                                   is archived. The answer then gains
                                   partitions: {archived, failed?:
                                   ["user:<id> (<partition id>): <why>"],
                                   skipped?: "<why none>"}; a tile without
                                   people's partitions answers as before
GET    /backups?component=…         admin. the archiver's version list passed
                                   through: {versions:[{version,time,size}]}
POST   /restore                    admin. body {component, version?, file?,
                                   confirm?}. An archive made before the
                                   tile's last partition mode switch that
                                   deleted data (docs/partitions.md
                                   §Backups) restores only with confirm set
                                   to that switch's date (YYYY-MM-DD) —
                                   into the global instance's namespace, at
                                   today's keys, never a person's partition
                                   — else 409 {error: "this backup (made …)
                                   is older than <tile>'s partition mode
                                   switch on <date> (<from> → <to>):
                                   restoring it brings back data the switch
                                   deleted …", switch: {at, from, to,
                                   confirm}}, before anything stops; the
                                   deployment archives it lists go with it.
                                   The switch is remembered apart from the
                                   tile's mode history (whose trimming never
                                   drops it). While the tile's mode record
                                   can't be read, every archive asks, with
                                   its own creation date (switch: {unknown:
                                   true, error, confirm}).
                                   A person's partition archive is never a
                                   tile (409; POST /partitions/restore), and
                                   no single file of one is ever served.
                                   No file → restore the whole version (stops the
                                   component, replaces its data/source from the
                                   archive; version defaults to latest). With file
                                   → stream one member back (recover without a full
                                   rollback): xbind fetches the version and
                                   extracts it itself (data/… of a split
                                   archive from its data archive); 404 for
                                   a member it doesn't hold, 409 when a key
                                   is erased or unknown. Restore is fully archive-driven — no
                                   local metadata needed (docs/overview/14-lifecycle.md).
                                   A sealed archive is authenticated whole
                                   before anything stops or is written
                                   (a damaged or tampered one: 502, "the
                                   sealed archive is damaged or was
                                   tampered with"). Its key erased: "this
                                   backup's data was erased on <date>
                                   (<reason>)" — a split main archive whose
                                   data key alone was erased restores its
                                   source and terminal layer, and the answer
                                   gains dataErased: "<why>", without "data"
                                   in restored. One whose data archive is
                                   gone though its key isn't erased (the
                                   archiver lost it), or that names a
                                   version no URL can, restores the same
                                   way and answers dataMissing: "<why>" (a
                                   data file of it: 404). A data archive of
                                   another backup, or sealed under another
                                   key, in its place is refused (502,
                                   nothing written). A key this workspace
                                   never had: "this backup was sealed by
                                   another workspace: import its keys (bx
                                   backup keys import)".
                                   {ok, component, restored:[parts],
                                   sandboxesSkipped?:[…]} — a manager's sandbox
                                   definitions come back by uid, stopped (§Tile
                                   sandboxes, "The workspace around them"); the
                                   ones left out, and why, are listed.
                                   The archiver is chosen by the @archive binding:
                                   bindings["<comp>"] override, else bindings["*"]
                                   default (set via POST /bindings). An archive
                                   whose deployment state belongs to another
                                   tile is refused before anything is written;
                                   this xbind leaves an archive's deployment
                                   record and checkpoint store out (logged):
                                   a tile restored where none stood comes
                                   back with plain live reload. The
                                   registration files it carries are
                                   validated row by row and merged into the
                                   deployments the tile has. When the
                                   archive lists deployment archives, each
                                   is restored by replace into the tile's
                                   deployment of its name, and the answer
                                   gains deployments: {restored: [names],
                                   skipped: ["<name>: <why>"]}
GET    /backup-schedule            admin. {schedules:[{component,schedule,retention}]}
POST   /backup-schedule            admin. body {component, schedule, retention} —
                                   owner-scheduled backup on the cron engine;
                                   retention prunes to N newest versions per run.
                                   A sealed workspace's data archives go with
                                   the main archives that name them: each run
                                   deletes the data versions no kept main
                                   archive names (never a count of their own).
                                   Each person's partition's archive keeps
                                   its own newest N; one of a partition
                                   that is gone (swept, reset, purged,
                                   switched away) loses every version once
                                   its key is erased — at an archiver
                                   without POST /archive/erase too (xbind
                                   keeps an index of the partition archives
                                   it wrote: data/backup-refs/partitions/).
DELETE /backup-schedule?component= admin. remove a component's schedule
GET    /backup-keys                admin. {mode, keys, unexported, lastExport?,
                                   exports, erased, erasedSinceExport,
                                   passphraseChanged?} — mode sealed |
                                   plaintext (the plaintext-vault mode:
                                   archives are plain tars) | vault-sealed
                                   (no backup until unsealed) | vault-locked
                                   (no vault set up yet: no backup until it
                                   is); unexported counts the backup keys no
                                   exported bundle since the last passphrase
                                   change holds; passphraseChanged is when
                                   the passphrase changed after the last
                                   export (POST /vault-rekey): every bundle
                                   before it is stale. The /alerts kind
                                   backup-keys, bx doctor and the admin
                                   console's Backup tab nudge on both.
POST   /backup-keys/export         admin, as a person: their own session or
                                   the admin tile's frame under their login
                                   (a tile's backend, terminal or agent:
                                   403, whatever its tile holds). The
                                   disaster-recovery key bundle {schema:1,
                                   workspace, created, barrier, keys:[…],
                                   erased:[…]}: the vault's barrier
                                   descriptor (the data key wrapped under
                                   the passphrase), every backup key file
                                   (wrapped under the data key) and every
                                   erase tombstone. Nothing without the
                                   vault passphrase; with the passphrase in
                                   force at export it opens the data key
                                   itself — every vault secret and all data
                                   at rest, not only the backups — and a
                                   later passphrase change doesn't take
                                   that back. Recorded as an export when
                                   answered, whether or not the client
                                   saves it. 409 without a vault barrier.
POST   /backup-keys/import         admin, as a person (as export). body
                                   {bundle, passphrase} — the bundle another
                                   workspace exported, and that workspace's
                                   vault passphrase: its data key is
                                   unwrapped in memory only, each backup key
                                   re-wrapped under this vault (never used
                                   to seal new archives), and its tombstones
                                   taken (a previously imported key they
                                   name is erased; this workspace's own keys
                                   never are). {imported, present, erased}.
                                   400 for a wrong passphrase or a bad
                                   bundle (nothing is imported; a
                                   descriptor over 1 GiB, 16 threads or 64
                                   passes is refused); needs this vault
                                   unsealed.
POST   /backup/erase               admin, as a person (as export). body
                                   {component, what: "data"|"all"} —
                                   crypto-erase a tile's backups: data
                                   deletes its data keys (main's and every
                                   deployment's namespace: the source stays
                                   restorable), all every key of the tile.
                                   It waits for the tile's backups in
                                   flight. Each key is tombstoned first (a
                                   restore then says why), then its file
                                   deleted; the subject gets a new key at
                                   its next backup, and the archiver is
                                   asked to delete the versions sealed under
                                   them (POST /archive/erase; an archiver
                                   without it keeps them, unreadable, until
                                   retention prunes them). {ok, component,
                                   erased:[{id, subject, gen}], archiver:
                                   "<what the archiver did>", note: "<what
                                   no key erases: plain archives made before
                                   sealing; a tile that doesn't root its
                                   scope names the root holding its data>",
                                   error?: "<a key file not removed yet —
                                   erased all the same>"}. data takes
                                   people's partitions' keys (part:) too.
                                   A tile with a partition mode record gets
                                   a history entry {op: "backup-erase", by,
                                   at, reason, wiped: {subkeys}}, as does
                                   every erase of a person's partition's key
                                   (its partition id in partition): its
                                   sweep, reset or purge. Of the history's
                                   200 entries backup ones (backup-erase,
                                   partition-restore) take 100 at most, the
                                   oldest going first: they never push a
                                   manager's act out.
GET    /partitions/backups?tile=…  the person (their own partition), in
       [&user=…][&partitionId=…]   their own session, app or device; an
                                   admin (anyone's: the root token, or the
                                   admin tile's frame under their login).
                                   Every tile principal else — a partition's
                                   own code, frames, terminals, agent
                                   sessions — and view-as: 403. The
                                   archiver's versions of user's (default:
                                   the caller) partition archive of tile
                                   (.partitions.<tile-key>.<dep>.<partition
                                   id>, dep the primary); partitionId
                                   (u-<32 hex>) names an earlier holder's
                                   (the id deleted and recreated since),
                                   default the person's current one. A
                                   person names only their own current id:
                                   any other 403, before the archiver is
                                   asked, never saying whose it is.
                                   {tile, partition: "user:<id>",
                                   partitionId, deployment, partitioned,
                                   archiver, versions:[{version,time,size}]};
                                   404 for no such person or tile.
POST   /partitions/restore         the person (their own partition) or an
                                   admin, as GET /partitions/backups. body
                                   {tile, user?, partitionId?, version?,
                                   confirm, to?, dryRun?} — replace user's
                                   partition of tile (its namespace's data,
                                   vault and registrations; its record stays
                                   the person's current one) with a version
                                   of its archive (default the latest),
                                   after confirm: "<tile> user:<id>", typed
                                   (400 {error, confirm} otherwise). Its
                                   instance stops and its namespace is held
                                   meanwhile; it starts again on next use.
                                   Only a sealed partition archive sealed
                                   under that partition's own backup key, of
                                   the same tile and user id, restores (409
                                   otherwise, nothing written). The same
                                   incarnation (the archive's uid is the
                                   person's now) restores by the person or
                                   an admin; an earlier holder's
                                   (partitionId) only by an admin with to:
                                   "<id>" (the same id again: audited, the
                                   person is told — push kind
                                   tile.partition-restored) — a person 403,
                                   without to 400; never into another id
                                   (403, and 400 for a to naming another).
                                   409 while the tile isn't partitioned
                                   now, while it is paused, for a key
                                   erased ("this backup's data was erased
                                   on <date> (<reason>)") or another
                                   workspace's ("… import its keys").
                                   Everything is judged again under the
                                   tile's backup lock: a switch, reset,
                                   purge, sweep or erase that ran while the
                                   restore waited wins (409 "… it changed
                                   while this restore waited; nothing was
                                   restored"). →
                                   {ok, tile, partition, partitionId, from,
                                   version, resources, earlierHolder, data,
                                   vault, registrations:[files], skipped?,
                                   vaultSkipped?} — skipped: archived
                                   resources the tile no longer keeps per
                                   person; vaultSkipped: a vault another
                                   vault sealed (another workspace's
                                   archive), left out. dryRun checks it all
                                   and writes nothing (no confirm). The
                                   tile's history gains {op:
                                   "partition-restore", by, at, reason,
                                   partition}. Audited.
```
