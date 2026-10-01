# W3a-wire — wave 3a's seams wired

Wave 3a's integration of the partitioned-tiles plan: the three packs merged
on `partitions` at 11050e65 — F6 (partition mail), F7b (operations: the
`/partitions` API, logs and tile-status, the users-store hooks, held
credentials, `bx partition`/`status`/`doctor`, "reviewed code only") and
F17b (partition archives and erase hooks) — made correct and green, their
seams wired where both sides now exist. Branch `pt/w3a-wire`. This change
edits neither `docs/changelog.md` nor `plans/DECISIONS.md` (see §Notes for
the changelog).

## What the merge broke

The tree built, and `make fmt-check vet`, the repo guards and every package
but two were green on 11050e65 as merged. The two red tests were the two
seams the packs had left forced for the integrator, as their records said:

1. **`TestPartitionMailFeatureServed`** (boot) — `GET /partitions` served
   without `partition-mail/1` while mail was served: F7b's
   `registerPartitionFeature` existed, nothing called it.
2. **`TestPartitionSweepErasesArchives`** (broker) — the merge took F7b's
   `sweepPartitionRecords` line (`dropOnePartition`), whose erase went
   straight to `eraseBackupSubjectsHeld`: the key was erased but the erase
   never reached the tile's history (F17b's `backup-erase` entry).

No other merge damage: the conflict resolutions (openapi.go's
`joinEndpoints` chain with both packs' endpoints, deployclass.go's and
partitionclass.go's rows, partitions.md's sections and its "Not documented
yet" list) read correctly; no conflict markers are left.

## Wired

| Seam | Wired |
|---|---|
| F6 × F7b: the feature word | `partitionmail_drop.go`'s `init` calls `registerPartitionFeature(PartitionMailFeature)`; `TestPartitionMailFeatureServed` passes; [protocol.md](/docs/protocol.md) and the OpenAPI list `partition-mail/1` among the features |
| F6 × F7b: mail counts in `GET /partitions` | `partitionRow.Mail *MailCount` (`mail: {pending, bytes, expired, undeliverable?}`), from `(*Broker).PartitionMailCounts(tile, dep)` read once per deployment per listing (`mailCountsOf`): the person's own row (`ownPartitionRows`) and every row for admins (`partitionPeople`; `adminRow` keeps it). Counts only, never a content; tile code gets no rows, as before. A row whose inbox never held an item has no `mail`. [protocol.md](/docs/protocol.md) `GET /partitions`, [partitions.md](/docs/partitions.md) §Operating people's partitions, the OpenAPI string. `TestPartitionsListMailCounts`; the mail smoke's new `listing` case |
| F6 × F7b: `bx partition mail ls\|ack` | built by F6; `bx partition` with no subcommand now prints `partitionMailUsage` too, and the unknown-subcommand list names `reviewed` and `mail` |
| F6 × F7b: the users-store delete hook | checked: `apiUsersDelete` → `PartitionPersonDeleted` → `PartitionUserDeleted` → `mailUserDeleted` (for a person who ever had a partition uid; one who never did has no inbox). Pinned through the API: `TestPartitionDropsEraseThroughHistory` deletes zed with `DELETE /users` and his inbox is gone at once |
| F6 × F7b: reset / purge drop the inbox | checked: `dropOnePartition` → `DropPartition` per deployment → `dropPartition` → `dropMailBucket`; no `partitionDropHook` needed (the hook list stays a seam for later planes; its comment now says so). Pinned by the same test (alice's reset) |
| F17b × F7b: the sweep | the merge's `sweepPartitionRecords` line (F7b's `dropOnePartition`) kept; `dropSweptPartition` (backup_partition.go) deleted, unused |
| F17b × F7b: the erase | `erasePartitionKeysHeld` (partitiondrop.go) now calls `erasePartitionBackupsHeld([]string{tile}, d, pkey, reason, by)` per deployment, so every reset, purge and records' sweep erase is in the tile's history (`backup-erase`, reason, partition id, by). `TestPartitionSweepErasesArchives` passes; `TestPartitionDropScopeMember`, `TestPartitionDropEveryDeployment` stay green. New `TestPartitionDropsEraseThroughHistory`: an admin's reset and an orphan's purge each leave a `backup-erase` entry naming the partition (it fails on the merged tree) |
| The offload refusal | checked: `offload` (backup.go, F17b's file) calls `partitionOffloadCheck` first; `offloadStatus` → 409. F7b's test called the check directly; `TestPartitionOffloadRefusedByOffload` goes through `offload` (plain and full), so losing the line fails |

## Deviations

- **The erase's owners are the tile alone, not `slices.Compact([]string{scope,
  tile})`.** The wave's task text and F17b's record carried the older
  recipe; F7b's review fix (records/F7b.md §Merge with F17b) made it wrong: a
  partitioned member tile's reset must never erase the scope root's `part:`
  key (it seals the root's namespaces, which the member doesn't use — 01 §3
  rule 1). `TestPartitionDropScopeMember` pins it and fails with the scope
  root among the owners. The namespaces' sweep still erases the scope
  root's key as the owner of the namespace it drops (F17b, unchanged).
- **Mail counts are the rows' only**; the global instance's inbox counts
  (`PartitionMailCounts(tile, "")["global"]`, F6's optional
  `globalMail`) are not in the answer — no surface reads them yet (F12's to
  add if the admin section wants them).

## Seams still open, by owning pack

- **B2a–d (the agent template)** — declare `"partitionMail": "/mailbox"`
  beside `"global"`; the handler pages with `InboxPage` and `after` while
  `More` (pt/b2a's `mailbox.go` takes a short page as the end — a page cut
  at ~8 MiB is short and not the end), dedupes by id, and acknowledges what
  it won't handle, in one `Ack` per page, stopping on an error (pt/b2a
  leaves unknown topics unacked: each is rung for, and starts the
  partition, at 1 min, 5, 30, 2 h, then every 6 h until it expires).
  **B2c**: `LedgerTrigger` — the global instance passes a private trigger's
  `source` (`MailWith(…, MailOptions{Source})`). From W2: the partition
  default and shared conversations.
- **F11 (the person's page `/xbin/partitions`)** — the person's row (now
  with `mail` counts), `notices`, `credentials` (allow/refuse), the trust
  panel (`trust`, `lastCodeChange`, `reviewedOnly`), the log share,
  consents, ledger and personal binds; "restore my partition" (`GET
  /partitions/backups`, `POST /partitions/restore`); the consent push's link
  and the card's "details…" link (F14).
- **F12 (the admin tile's Partitions section)** — people's rows (with `mail`
  counts), orphans and purge, stop/reset, the "reviewed code only" switch
  (`POST /partitions/reviewed`), caps hit and untracked files, a person's
  partition restore through `AdminFrameDriver`, `POST /backup`'s
  `partitions.failed|skipped` in the Backup tab, the tile's mode history
  (`backup-erase`, `partition-restore`, `lastWipe`), the global inbox's
  counts if wanted; from W2: personal binds list/delete, the sandbox list's
  person-terminal disks.
- **F14b (wave 4)** — the shell's consent prompts.
- **B3 (docs)** — fold F6's, F7b's and F17b's changelog and decision
  entries and §Notes below.
- **I1** — from W2: proxied long-lived streams a revoked consent doesn't
  cut, a view-as session's `GET /frame-token`, a person's start mounting the
  global instance's volumes, prefs per partition. From this wave: why boot's
  hub `users` subscription lagged behind a quick re-enable (F7b; the
  synchronous `usersEvent` call covers every broker-made change); `GET
  /backends`' `lastStartMs` and a `lastExit` code/signal/OOM split, which
  nothing records yet (F7b); "caps hit" isn't persisted across a restart
  (F7b); `bx doctor` lines for failed partition archives — nothing keeps a
  backup's `partitions.failed` past its answer (F17b).
- **F8 finalization** — the partition mail snippets for Node and Python in
  [sdk.md](/docs/sdk.md) (07 §1); the Go SDK's are built (F6).

## Flagged for the owner

- **A removed tile's mode record goes with its `lastWipe`** (F17b's seam
  note on F7b's `sweepRemovedModeRecords`): a tile removed and later
  re-added has no pre-switch guard for archives from before its removal.
  Code unchanged; a tombstone record holding `lastWipe` is the alternative.
- **Mail to a person whose partition never ran has no row and can't be
  reset.** The global instance may mail any live reader of the tile, and
  mail never starts a first instance, so the item waits in an inbox that
  has no partition record: `GET /partitions` shows no row (so no counts),
  and `POST /partitions/reset` answers 404 (no partition held). The items
  go at their ttl (at most 30 days), the person's deletion, or a switch;
  the person reads them once their partition first runs. Small, and
  arguably right (nothing of theirs runs yet); a mail-only row, or a reset
  that drops a mail-only inbox, are the alternatives.
- **The listing opens the mail store.** Each `GET /partitions?tile=` for a
  person with a partition, or an admin, opens the tile's bbolt store once
  per deployment under its tile lock (a read transaction, rolled back): the
  listing is polled, and a tile without mail costs a stat. Fine at today's
  sizes; a cached count is the lever if it shows.
- Carried from F17b: sealed pre-switch main archives ask for the
  confirmation too; an admin may roll a person's partition back to any
  version of its archive.

## Bugs found

- The two merge seams above (feature word unlisted; the drop's erase
  outside the history): fixed.
- **Pre-existing, on master too — not fixed here** (F6 found it): a new Go
  tile's first build can race the `go.work` regeneration — `backend build
  failed: go: no modules were found in the current workspace`, sticky until
  the code changes. Repro (F6): write a Go tile's files and `xbin.json`,
  then `GET /api/<tile>/…` as soon as `/components` lists it (~100 ms after
  its registration); the build ran before `deps.GoWork` listed the tile's
  module. The mail smoke waits until `go.work` lists the tile. To be fixed
  on master separately.
- **Pre-existing, on master too — not fixed here:** `-race` flagged
  `TestRegistryListsPlacedLeaf` (internal/runner/sbx_leaf_test.go) once in
  seven runs: the test sets `h.r.cgOps = nil` (line 40) while g1's exit
  goroutine still reads it in `leaveLeaf` (limits.go:38, from runner.go's
  `startDeployment`). A test-only race (the test mutates the runner under
  its goroutines); neither file is a partition change.
- **A load flake, not this wave's:** the first `make check` failed once on
  `TestSSHClientLeaves` (builtin-tiles/sandbox-terminal: "timed out
  waiting: the command is ended", a 10 s wait); it passed four runs alone
  and the second `make check`. No wave-3a pack touches that tile.

## Smokes on the merged tree

Real `xbind --isolate`, .dev.mk's env exported, Bash sandbox off:
`go test -tags=integration -count=1 -v -run
'TestPartitionsSmoke(W2|Mail|Backups)?$' ./test/isolated/` — no SKIP.

| Case | Result |
|---|---|
| `TestPartitionsSmoke` (19): zero-state, auto-mode, own-partition, data-apart, deployment, bob-never-sees-alice, cross-tile, global, admin, logs (F7b's answers, the shared log's follow ending), ops (F7b), view-as, caps, token-revocation (F7b: a new boot and a 401 for the old token after re-enable), person-recreated, reset (F7b), mode-keep, mode-switch, mode-pending-unpartitioned | PASS (45 s) |
| `TestPartitionsSmokeW2` (5): vault, global-address, personal-bind, terminal, cron | PASS (75 s) |
| `TestPartitionsSmokeMail` (F6; 5 with this wave's `listing`): global-to-person, person-to-global, outsiders, listing, never-run-restart | PASS (38 s) |
| `TestPartitionsSmokeBackups` (F17b) | PASS (21 s) |
| `TestSealedBackupsUpgrade` (./test/, the previous release v0.3.61) | PASS (9 s) |

`TestPartitionsSmokeReap` (opt-in, 11 min) was not re-run: its code is
unchanged.

## Tests run

- On the merged tree, before any change: `go build ./...`, `go test ./...`
  (the two seams red, above).
- On the branch: `go test ./...`, `make fmt-check vet js-check`, the repo
  guards (`.`, assetscan, sizebudget, docscheck, builtins, apicheck), the
  zero-state goldens (`TestNoPartitionGolden`, `TestZeroStateRoute`),
  `-race` on broker, server, boot, runner, users, obs, backup, deployments
  and `cmd/bx` (green; the runner's one flake above, green on re-run), the
  smokes above, and `make check` at the end (green; its first run hit the
  sandbox-terminal flake above).
- New: internal/broker/partitionw3a_test.go — `TestPartitionsListMailCounts`,
  `TestPartitionDropsEraseThroughHistory`,
  `TestPartitionOffloadRefusedByOffload` (the first two fail on the merged
  tree); test/isolated's mail smoke `listing` case.

## Notes for the changelog (the owner folds these in)

Fold into the wave's partition entries, none a change for a workspace
without a partitioned tile:

- F6's: `GET /api/xbin/partitions` lists `partition-mail/1` in `features`,
  and its rows carry the inbox's counts — `mail: {pending, bytes, expired,
  undeliverable?}` — on the person's own row and, for admins, on every
  person's row; never a content ([protocol.md](/docs/protocol.md),
  [partitions.md](/docs/partitions.md) §Operating people's partitions).
- F7b's: `features` also lists `partition-mail/1`; a reset deletes the
  partition's mail too; `bx partition` (no subcommand) prints `mail`'s
  usage.
- F17b's: the erase of a person's partition's backup key by `POST
  /partitions/reset`, `POST /partitions/purge` and the records' sweep is
  recorded in the tile's mode history (`backup-erase`) like the other
  erases.
