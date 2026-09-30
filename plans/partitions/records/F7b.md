# F7b — records for the integrator

Work pack F7b (operations: plans/partitions/06 §4-§10; PD-07, PD-23,
PD-24, PD-26, PD-46), branch `pt/f7b` on `partitions` at 3f520431. It
fills the seams W1-wire and W2-wire left to F7b. This change edits neither
`docs/changelog.md` nor `plans/DECISIONS.md`: the two texts below are the
integrator's to fold in (the D number is theirs to assign).

## Changelog entry

- **Operating people's partitions** ([partitions.md](/docs/partitions.md)
  §Operating people's partitions, [protocol.md](/docs/protocol.md),
  [bx.md](/docs/bx.md)). Nothing changes for a workspace without a
  partitioned tile.
  - `GET /api/xbin/partitions[?tile=]`: `features` (what this xbind serves:
    `partitions/1`, `mode-switch/1`, `consents/1`, `personal-binds/1`,
    `global-address/1`, `partition-ops/1`, `log-share/1`,
    `credential-confirm/1`), and per audience — a person their own
    partition's row (state, running, bytes, registrations, ledger totals,
    log share) and the tile's trust panel; the tile's writers and managers
    totals; admins every person's metadata row, the personal binds and the
    orphans; tile code the tile-level fields only. Without a tile: the
    partitioned tiles, and a person's held credentials and notices.
  - `POST /api/xbin/partitions/stop|reset|purge`: stop a partition's
    instance (the person's own; a manager's or admin's for anyone); delete
    one partition after a typed confirmation, erasing its backup key (the
    person's own; an admin's for anyone, who is told); delete orphaned
    partitions now (admins).
  - A person's partition's backend log is theirs: `GET /logs` answers each
    person — their session, frames, terminals — their own partition's log
    at any level, and admins or managers read it only while the person
    shares it (`POST|DELETE /api/xbin/partitions/share-log`, ≤ 14 days);
    the global instance's log with `?xbin-partition=global`. `GET
    /tile-status` from a partition's credential answers that partition;
    `GET /backends` nests people's running instances (metadata only).
  - People's lifecycle: deleting a person stops their instances and
    orphans their partitions (swept after 30 days, or purged), drops their
    personal binds and consents, and moves a partition holder's home and
    agent history to `data/orphans/`; disabling a person, or taking away
    their read, stops their instances at once.
  - Credential resets: a sign-in link, password or SSO email an admin
    makes for someone who holds partitions is audited and the person told
    (push and notice); with the workspace policy `credentialResetConfirm`
    on it is held until they allow it (`POST
    /api/xbin/partitions/credential-confirm`), refused, or 24 hours pass;
    a held link's redemption answers 409 "waiting for <person> to confirm".
  - `/alerts` kind `partition-trust` (live reload on while non-admins can
    change a partitioned tile's code or a bound provider's); offloading a
    partitioned tile answers 409; the `partitions` event gains ops `mode`
    (the tile's readers) and `notice` (the person's own sockets).
  - `bx partition ls|stop|reset|purge|limits|share-log|credential`, `bx
    status` prints a partition terminal's partition, `bx doctor`'s
    partition checks.

## Decision entry

- **D<next> — Operating people's partitions: a per-audience listing, one
  partition's stop/reset/purge, logs that are the person's, people hooks
  and held credentials (2026-09-30).** Implements plans/partitions 06
  §4-§10 (PD-07's a+, PD-23's warnings, PD-24, PD-26, PD-46).
  docs/partitions.md §Operating people's partitions, docs/protocol.md
  (`/partitions`, `/partitions/{stop,reset,purge,share-log,
  credential-confirm}`, `/logs`, `/tile-status`, `/backends`, `/users`
  rows, `/invite/redeem`, `/lifecycle`, the `partitions` event),
  docs/bx.md.
  - **Chosen.**
    - **The listing answers per audience, one route.** `GET /partitions`
      is PartitionNeutral: the handler — not the class — decides what
      each caller sees (PD-46), and gives a tile's own credentials the
      tile-level fields only, so tile code never reads people's metadata.
      The admin tile's frame driven by an admin reads as that admin
      (AdminFrameDriver, F13a's precedent).
    - **Acts are a person's.** stop/reset/purge judge the person
      (`partitionActor`: a person's own session, app or device, the root
      token, or the admin frame's driver) and are GlobalOnlyRefused /
      PrimaryOnly in the class tables; share-log and credential-confirm
      are PersonOnly.
    - **One partition's deletion is one function** (`dropOnePartition`):
      the person's terminals end and their layers and history go (F7a's
      hold spans it), then under the tile's backup lock the partition's
      namespaces (when the tile roots its scope), F5's records,
      registrations and vault, its log directory, other planes' stores
      (`partitionDropHooks`: F6's mail, F17b's archives) — and last its
      `part:` subkey is erased (11 §3's order). The reset, the purge and
      the orphan sweep (F5's records sweep now calls it) share it. A reset
      holds the partition's starts from before its stop until the drop is
      done (`holdPartitionDrop`, asked by ShouldRunPartition).
    - **Logs: the credential decides.** The person's own at any level
      (their data); a named person's (`?user=`) only for an admin or
      manager in their own session while shared (the share lives in the
      partition's directory, so a reset or switch takes it); the global
      instance's with F9's `?xbin-partition=global` under today's rule,
      never for a partition's own credential; `?partition=` 400. The
      answer names the partition (`X-XBin-Partition`).
    - **People hooks where the users plane already reports.** The delete
      handler reads the uid before the store forgets it and calls
      `PartitionPersonDeleted` (stop+revoke, orphan namespaces and records,
      personal binds, consents, notices, and the move of homes and agent
      history for a partition holder — a rename of the directory entry,
      never a walk of what a sandbox wrote, D78). Disabled / lost read:
      `usersEvent` — which every users-plane mutation calls — asks
      `PartitionPeopleChanged` synchronously, so the instance is stopped
      before the answer; boot also runs it on every hub `users` event (the
      SSO and device planes publish those directly).
    - **Held credentials live beside the person's notices**
      (`data/partitions/people/<uid>.json`, keyed by the incarnation), never
      in users.json. The users store only gains an invite gate (asked
      under its lock; the broker's gate reads the person's file without
      locks — atomic writes — and never calls the store) and two setters
      (SetPassHash, RevokeInvite). A held password is hashed at once;
      nothing plaintext is kept. The 24 h rule runs at redemption (the
      gate) and every 5 minutes (boot).
    - **Notices** (a switch's wipe via F13a's `partitionNotice`, an
      admin's reset, credentials) are kept per incarnation (50, 90 days)
      and published as the `partitions` op `notice` to the person's own
      sockets only; op `mode` goes to the tile's readers. Both are gated
      by the event data's `VisibleTo` (server.go's event filter now asks
      any event whose data names its audience, as prefs did).
    - **Trust warnings** (06 §4) are computed, not stored: non-admin
      people with write level on a partitioned tile or a bound
      non-partitioned provider, while the deployments plane says saves
      reach its primary (live reload on the primary).
    - **A removed tile's mode record** goes at the sweep once none of its
      partitions, namespaces or data is left (a tile that comes back with
      data keeps finding its recorded mode).
  - **Not chosen:** `?partition=` for admins (PD-07: no read path);
    per-person log files readable by managers without a share; a
    credential hold inside users.json (an older xbind would drop it and
    activate the credential); a ledger line for a credential notice (the
    ledger is per tile-partition; the audit line and the notice carry it);
    listing the global instance's log to a person's partition.

## Seams for the integrator

Filled here (W1-wire's and W2-wire's F7b lists):

| Seam | Filled with |
|---|---|
| `GET /partitions` (F9's `TestGlobalAddressFeatureServed`) | `apiPartitionsList` (partitionlist.go); `features` lists `broker.GlobalAddressFeature` |
| `PartitionRegistrations`, `eachPartitionRecord`, `consentsView`, the ledger readers | the listing's rows (`partitionPeople`, `personLedger`) |
| `InspectPartitions`, `DefaultPartitionCaps` | `SetPartitionOps` (boot/partitionops.go) and `partitionCapDefaults` |
| reset/purge: `DropPartition`, `dropPartitionNS`, `wipePersonTerminalsOf` with its hold | `dropOnePartition` (partitionops.go) |
| users-store hooks: `StopPartitionsOf`, `RevokeUserPartitionInstances`, `PartitionUserDeleted`, `PersonalBindsUserDeleted`, `dropPartitionConsents` | `PartitionPersonDeleted` (partitionpeople.go), from `apiUsersDelete`; `PartitionPeopleChanged` from `usersEvent` and boot's users events |
| `partitionNotice` (F13a) | the person's notices (partitionpeople.go's init) |
| the `partitions` event: op `state` (F2's shape kept, now documented), op `mode`, op `notice` | `publishPartitionMode` (partitionNotify's and the decision's reload), `addNotice` |
| `GET /logs`, `GET /tile-status` rows in `partitionUnconverted` | removed: the map is empty (obs/partitionlogs.go, boot/partitionstatus.go) |
| a partition's log directory (`.xbin/partition/<TileKey>/…`) | removed by a switch that deletes everything (`partition-logs` wipe hook), a reset, a purge and the orphan sweep |
| a removed tile's mode record after the sweep | `sweepRemovedModeRecords` (end of `sweepPartitionNamespaces`) |
| `bx status`'s `partition:` line, `bx logs` | tile-status' `partition`; bx logs needs nothing (the credential decides) |
| `bx partition limits` (F3) | partitionops.go |
| `bx doctor`'s partition checks (F1, F10, F13a) | cmd/bx/doctor_partitions.go |
| the offload refusal | `partitionOffloadCheck`, one line in backup.go's `offload`; `offloadStatus` maps it to 409 |

Declared here for later packs (nothing fails open without them):

| Seam | Signature | For |
|---|---|---|
| `registerPartitionDropHook(partitionDropHook{name, drop})` | `drop func(b *Broker, tile, dep, pkey string) error`, run by `dropOnePartition` under the tile's backup lock, after the instance and terminals stopped; an error keeps the subkey (a retry erases it) | **F6**: a partition's mail on reset/purge/sweep; **F17b**: anything of a partition's archives beyond the `part:` subkey erase this pack does |
| `registerPartitionFeature(word)` | adds a word to `GET /partitions`' `features` | **F6**: `partition-mail/1` once mail is served |
| `(*Broker).notePartitionNotice(user, tile, kind, text)` | a person's notice + op `notice` | **F6** (mail notices, if any), **F11** (the page lists `notices` from `GET /partitions`) |
| `GET /partitions` fields `credentials`, `notices`, `trust`, `partitions[].logShare` | wire | **F11** (the person page), **F12** (the admin section: rows, orphans, stop/reset/purge) |
| `(*Broker).PartitionMeta`, `PartitionDisk`, `SetPartitionLiveReload` | boot wiring | boot only |

## Deviations

- **06 §5's "admins and managers read a person's log while shared"** takes
  `?user=<id>` (the spec names no parameter; `?partition=` stays 400).
- **The global instance's log for a person's session** now needs
  `?xbin-partition=global` (F9's parameter): a plain `GET /logs` from a
  person on a partitioned tile answers their own partition's. The root
  token and other tiles read global's as before. S1's logs case pins it.
- **`GET /backends`' partition rows** carry what the runner and the
  partition record know: `lastStartMs` and a `lastExit` code/signal/OOM
  split aren't recorded anywhere yet (the record keeps the exit's time
  only); `errorClass` is a keyword classification of the runner's error.
- **Reset of a tile that doesn't root its scope** deletes the partition's
  records, vault, registrations, log and terminals but not the scope's
  namespaces, which are the root's (the switch's rule: `RootsScope`).
- **The ledger line for a credential notice** isn't written (the ledger is
  per tile-partition); the audit line and the notice carry it.
- **Trust warnings go to admins and the tile's readers** (as the switch
  alert); `bx doctor` prints them for admins.
- **`bx status`'s partition line** comes from the server's answer only (not
  `$XBIN_PARTITION`), so `bx status <other tile>` never mislabels.
- **The purge also accepts `user:<id>`** of the orphan's former person.
- **Offload refusal** covers a tile whose mode record can't be read too
  (fail closed).

## Bugs found in already-merged code

- **Disabling a person left their instance running** and its token
  authenticated again on re-enable (S1's note): fixed — `usersEvent` stops
  their instances synchronously; S1's token-revocation case now logs a new
  boot and a 401 for the old token after re-enable.
- **Deleting a person left their instance running** (S1's note): fixed by
  the delete hook (stop + revoke before orphaning).
- **Nothing removed people's partition logs** (`.xbin/partition/…`) on a
  switch, a delete or a sweep (W1-wire): fixed (wipe hook, drop, sweep).
- **The orphan sweep dropped a partition's records and vault but not its
  person layers, agent history or log** (F7a's note): the sweep now calls
  `dropOnePartition`.
- **Observed, not fixed:** boot's hub subscription to `users` events
  (`onUsersEvents`, the tile-sandbox precedent) didn't stop a disabled
  person's instance before the smoke re-enabled them a few requests later;
  the synchronous call in `usersEvent` covers every broker-made users
  change, and the hub path stays for the SSO/device planes' events. Why the
  hub path lagged wasn't investigated.
- **Test environment:** a tile terminal's `bx` is the base rootfs's
  (`/usr/local/bin/bx`, built when the rootfs was), not this tree's, so a
  new `bx` feature shows in terminals only after a rootfs rebuild; the smoke
  asks the route instead.

## Tests

- broker (new, partitionops_test.go): `TestPartitionsListVisibility`
  (features incl. global-address/1; a person's own row, writer/manager
  totals, admin rows and orphans, tile code tile-level only, non-reader
  404, the overview), `TestPartitionStopResetPurge` (who may stop; the
  reset hold; reset's typed confirmation, namespace/record/log/share
  removed, subkey erased, the person told; purge refuses a live partition,
  deletes an orphan, keeps others), `TestPartitionLogChoice`,
  `TestPartitionPeopleHooks` (delete: stop, revoke, orphan, homes/history
  moved for a holder only; disable stops only theirs),
  `TestPartitionHeldCredentials` (policy off: told, effective; on: held
  link 409 "waiting", Allow, Refuse revokes, 24 h with a fake clock; a held
  password; a person without partitions unaffected),
  `TestPartitionOffloadRefused`, `TestPartitionTrustAlerts`,
  `TestRemovedTileModeRecord`, `TestPartitionEventAudiences`.
  `TestPartitionEdgeMatrix`'s event subscription skips the new ops.
- cmd/bx: `TestBxPartitionOps`; `TestBxPartition`'s unknown-subcommand row
  no longer uses `stop`.
- S1 smoke (isolated xbind, env from .dev.mk, Bash sandbox off):
  `TestPartitionsSmoke` 19/19 (the `logs` case rewritten for F7b's
  answers — each person their own log, global's by the root token or
  `?xbin-partition=global`, a share for an admin only while it lasts,
  tile-status of alice's frame; new `ops` — the listing's audiences, a
  person's stop — and `reset` — erin's reset deletes her data only, after
  the typed confirmation — cases) and `TestPartitionsSmokeW2` 5/5 (the
  terminal case adds tile-status and `bx logs` in alice's terminal); 36 s
  and 72 s.
- `go test ./...`, `make fmt-check vet`, the repo guards (`.`, assetscan,
  sizebudget, docscheck, builtins, apicheck), `-race` on broker, boot,
  server, obs, users, cmd/bx, runner, auth, confine: green. No JS changed
  (`make js-check` runs in the pre-commit hook: green).
