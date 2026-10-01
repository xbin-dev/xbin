# F7b — records for the integrator

Work pack F7b (operations: plans/partitions/06 §4-§10; PD-07, PD-23,
PD-24, PD-26, PD-46), branch `pt/f7b` on `partitions` at 3f520431; the
review's fixes on `pt/f7b-fix` (see "Review fixes" at the end — the entries
below are the fixed branch's). It
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
    log share) and the tile's trust panel (who can change its code, live
    reload, protection, the last code change, reviewed code only); the
    tile's writers and managers totals; admins every person's metadata row,
    the personal binds and the orphans; tile code the tile-level fields only
    (not the policies). Without a tile: the partitioned tiles (admins: with
    `?untracked=1` each one's untracked files, caps hit in the last day), and
    a person's held credentials and notices.
  - `POST /api/xbin/partitions/stop|reset|purge`: stop a partition's
    instance (the person's own; a manager's or admin's for anyone); delete
    one partition — in every deployment it has data in — after a typed
    confirmation, erasing the tile's own backup keys of it (the person's
    own; an admin's for anyone, who is told); delete orphaned partitions now
    (admins).
  - `POST /api/xbin/partitions/reviewed {tile, on}` (admins): "reviewed code
    only" — needs the primary and every bound non-partitioned provider
    protected, keeps them protected while on (their unprotect and binding
    an unprotected provider in answer 409).
  - A person's partition's backend log is theirs: `GET /logs` answers each
    person — their session, frames, terminals — their own partition's log
    at any level, and admins or managers read it only while the person
    shares it (`POST|DELETE /api/xbin/partitions/share-log`, ≤ 14 days; a
    follow ends once the share does); the global instance's log with
    `?xbin-partition=global` (`bx logs --global`, `--user <id>`). `GET
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
    a held link's redemption answers 409 "waiting for <person> to confirm"
    (it is stored held: an older xbind never redeems it); refusing a link
    already used answers "already effective". A new SSO provider (`PATCH
    /auth-settings`) is such a credential for every partition holder bound
    by email; held, their SSO sign-ins wait (`sso_err=held`).
  - `/alerts` kind `partition-trust` (live reload on while non-admins can
    change a partitioned tile's code or a bound provider's); offloading a
    partitioned tile answers 409; the `partitions` event gains ops `mode`
    (the tile's readers) and `notice` (the person's own sockets).
  - `bx partition ls|stop|reset|purge|limits|share-log|credential|reviewed`
    (`purge` lists first, deletes with `--yes`), `bx logs --global|--user`,
    `bx status` prints a partition terminal's partition, `bx doctor`'s
    partition checks.

## Decision entry

- **D<next> — Operating people's partitions: a per-audience listing, one
  partition's stop/reset/purge, logs that are the person's, people hooks
  and held credentials (2026-09-30).** Implements plans/partitions 06
  §4-§10 (PD-07's a+, PD-23's warnings, PD-24, PD-26, PD-46).
  docs/partitions.md §Operating people's partitions, docs/protocol.md
  (`/partitions`, `/partitions/{stop,reset,purge,share-log,
  credential-confirm,reviewed}`, `/logs`, `/tile-status`, `/backends`,
  `/users` rows, `/invite/redeem`, `/auth-settings`, `/login/sso/callback`,
  `/deployments/protect`, `/bindings`, `/lifecycle`, the `partitions`
  event), docs/bx.md.
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
    - **One partition's deletion is one function, and deletes only the
      tile's own** (`dropOnePartition`, partitiondrop.go): F7a's hold is
      taken first and kept to the end (no session opens meanwhile), the
      person's terminals end and their layers and history go, then under
      the **tile's own** backup lock — in every deployment the partition
      has anything in — its namespaces (only when the tile roots its
      scope: a partitioned member uses no scope resources, 01 §3 rule 1),
      F5's records, registrations and vault, its log directory, other
      planes' stores (`partitionDropHooks`: F6's mail) — and last the
      tile's own `part:<tile>/<dep>/<pkey>` keys are erased (11 §3's
      order), never the scope root's. The reset, the purge and the orphan
      sweep share it. A reset holds the partition's starts from before its
      stop until the drop is done (`holdPartitionDrop`).
    - **Acts check rights before they say anything:** a tile the actor
      can't read answers 404 (unless they name their own partition of it),
      someone else's partition 403, and only then the mode (409) and the
      partition (404 — none held, and no one told).
    - **Logs: the credential decides, for as long as it streams.** The
      person's own at any level (their data); a named person's (`?user=`)
      only for an admin or manager in their own session while shared (the
      share lives in the partition's directory, so a reset or switch takes
      it) — a follow asks again every 2 s and ends when the answer changes;
      the global instance's with F9's `?xbin-partition=global` under
      today's rule, never for a partition's own credential; `?partition=`
      400. The answer names the partition (`X-XBin-Partition`).
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
    - **Held credentials live beside the person's notices, and fail
      closed** (`data/partitions/people/<uid>.json`, keyed by the
      incarnation). A held link is minted held (`CreateHeldInvite`: its
      stored hash carries `held:`, which no token's hash matches — an xbind
      without the gate, an older one after a downgrade, never redeems it);
      only a held link asks the gate, which lets it through once its hold's
      24 h passed and refuses it in every other case (waiting, no gate, a
      notices file it can't read, no hold naming it). A hold that can't be
      written revokes the link it would hold (500). A decision and the
      24 h lapse run under one lock, the store change first; a refusal is
      honoured while the credential is unused (even past 24 h); a link no
      longer pending (redeemed, replaced, expired) answers 409
      `already-effective` — change the password, sign out everywhere. A
      held password is hashed at once; nothing plaintext is kept.
    - **A new SSO provider is a credential** for every partition holder
      bound by email (PD-07, 10's "rebind SSO" row): audited naming them,
      each told; with the policy on their SSO sign-ins through it are held
      (the callback asks the store's SSO gate: `sso_err=held`) until they
      allow it or 24 h pass, and Refuse unbinds their email. Only the
      provider's identity counts (kind, issuer, client id). The SSO gate
      fails open on an unreadable notices file (it is asked for every SSO
      sign-in; failing closed would lock holders out after a downgrade).
    - **Reviewed code only** (PD-23): a per-tile admin switch beside the
      mode record (`reviewed.json`, only while on; unreadable counts as
      on). On needs the primary and every bound non-partitioned provider
      protected (D127m); while on the deployments plane asks
      `ReviewedOnlyRequires` before any unprotect (409, kind policy), and
      `validateBinding` refuses binding an unprotected provider in (409); a
      gap that opens otherwise is a trust warning. It binds only while the
      tile is partitioned.
    - **Notices** (a switch's wipe via F13a's `partitionNotice`, an
      admin's reset, credentials) are kept per incarnation (50, 90 days)
      and published as the `partitions` op `notice` to the person's own
      sockets only; op `mode` goes to the tile's readers. Both are gated
      by the event data's `VisibleTo` (server.go's event filter now asks
      any event whose data names its audience, as prefs did).
    - **Trust warnings** (06 §4) are computed, not stored: non-admin
      people with write level on a partitioned tile or a bound
      non-partitioned provider, while the deployments plane says saves
      reach its primary (live reload on the primary). The code writers are
      reused 15 s (dropped on any users change); the panel adds each
      primary's last code move (the deploy log's newest attempt).
    - **The listing builds only what the caller sees** — a person's own
      directory, totals from records, in-memory registrations and bytes
      measured at most once a minute; untracked files (a confined git in
      each tile's own repository, D78) only on `?untracked=1`.
    - **A removed tile's mode record** goes at the sweep once none of its
      partitions, namespaces or data is left (a tile that comes back with
      data keeps finding its recorded mode); an offloaded tile isn't
      removed: its record stays for the restore.
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
| `registerPartitionDropHook(partitionDropHook{name, drop})` | `drop func(b *Broker, tile, dep, pkey string) error`, run by `dropOnePartition` once per deployment the partition has data in, under the tile's backup lock, the partition's sessions held, after the instance and terminals stopped; an error keeps the keys (a retry erases them) | **F6**: a partition's mail on reset/purge/sweep (F17b's erase is not a hook: see "Merge with F17b") |
| `registerPartitionFeature(word)` | adds a word to `GET /partitions`' `features` | **F6**: `partition-mail/1` once mail is served |
| `(*Broker).notePartitionNotice(user, tile, kind, text)` | a person's notice + op `notice` | **F6** (mail notices, if any), **F11** (the page lists `notices` from `GET /partitions`) |
| `GET /partitions` fields `credentials`, `notices`, `trust`, `partitions[].logShare` | wire | **F11** (the person page), **F12** (the admin section: rows, orphans, stop/reset/purge) |
| `(*Broker).PartitionMeta`, `PartitionDisk`, `SetPartitionLiveReload`, `SetPartitionCapHits`, `SetPartitionLastCode`, `ReviewedOnlyRequires` (→ `deployments.DataHooks.ProtectRequired`), `CredentialSSOChanged`, `CredentialGateInstalled` | boot wiring | boot only |
| `GET /partitions` `reviewedOnly`, `POST /partitions/reviewed`; overview rows' `capsHit`, `untracked*` (`?untracked=1`), `reviewedOnly`; `trust.lastCodeChange`, `trust.reviewedOnly` | wire | **F12** (the admin Partitions section's "reviewed code only" switch), **F11** (the trust panel) |

### Merge with F17b (`pt/f17b-fix`)

Both packs edit `sweepPartitionRecords`' one line and the erase of a
partition's keys; F17b's record carries an older recipe (owners
`slices.Compact([]string{scope, tile})`) that this fix makes wrong. The
rule:

1. `partitionrecords.go`: take **F7b's** sweep line (`b.dropOnePartition(…,
   "partition swept: "+rec.Reason, "")`) and **delete F17b's
   `dropSweptPartition`** (backup_partition.go): one deletion path (it covers
   terminals, logs, the drop hooks and every deployment).
2. `partitiondrop.go`: make `erasePartitionKeysHeld`'s loop call F17b's
   helper per deployment, **the tile only** —
   `n, _, err := b.erasePartitionBackupsHeld([]string{tile}, d, pkey, reason, by)`
   — so each erase is in the tile's history (`noteBackupErase`) and F17b's
   "erased since export" bookkeeping. Never pass the scope root: a
   partitioned member tile's reset must not erase the root's `part:` key
   (review finding 1; `TestPartitionDropScopeMember` pins it).
3. `partitionns.go`: the two packs' edits are separate hunks (F7b's
   `sweepRemovedModeRecords` call, F17b's erase in `sweepPartitionOne`);
   keep both.
4. `TestPartitionSweepErasesArchives` (F17b) then runs through
   `dropOnePartition` and must stay green; so must
   `TestPartitionDropScopeMember` and `TestPartitionDropEveryDeployment`.

95-work-packs.md's contention table now lists these files.

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
  records, vault, registrations, log, terminals and the tile's own `part:`
  keys, never the scope's namespaces or the scope root's keys (01 §3 rule
  1: such a tile uses no scope resources).
- **The ledger line for a credential notice** isn't written (the ledger is
  per tile-partition); the audit line and the notice carry it.
- **Trust warnings go to admins and the tile's readers** (as the switch
  alert); `bx doctor` prints them for admins.
- **`bx status`'s partition line** comes from the server's answer only (not
  `$XBIN_PARTITION`), so `bx status <other tile>` never mislabels.
- **The purge also accepts `user:<id>`** of the orphan's former person.
- **Offload refusal** covers a tile whose mode record can't be read too
  (fail closed).
- **The trust panel's "last code changes"** is the primary's newest
  finished code move from the deploy log (at, by, how, result) — for a tile
  with deployments; a tile without one runs its work tree (liveReload
  says so: every save is a change), which no log records.
- **"Caps hit recently"** counts since xbind started (the runner's memory;
  not persisted), shown for the last 24 h.
- **"Reviewed code only" enforcement** refuses unprotect and binding an
  unprotected provider in; it doesn't refuse removing a provider's
  deployment record or other ways a gap may open (e.g. `--tile-deployments
  =off`'s allowed resets) — those show as a trust warning and in
  `reviewedOnly.unprotected`.
- **The SSO gate fails open** on a notices file it can't read (logged): it
  is asked for every SSO sign-in, unlike the invite gate (asked only for a
  link minted held, which fails closed).

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
- **pt/f7b-fix** (the review's fixes): broker `TestPartitionDropScopeMember`
  (a member tile's reset keeps the scope root's namespace and `part:` key,
  erases its own), `TestPartitionDropEveryDeployment` (an older primary's
  record, vault, log and key go too; F7a's hold spans the drop),
  `TestPartitionOpsTargets` (404 for a partition no one holds, nobody told;
  403 before existence; a non-reader's 404), `TestPartitionPeopleHooksThroughAPI`
  (PATCH /users disable and DELETE /users reach the hooks),
  `TestPartitionHeldLinkEdges` (stored held; no gate, an unreadable notices
  file: waits; a hold that can't be written: 500 and revoked; refusing a
  used link: 409 already-effective; refusing an unused lapsed one revokes
  it), `TestPartitionSSOProviderChange` (policy off told; on held, allow,
  24 h, refuse unbinds; a secret-only edit and a non-holder untouched),
  `TestPartitionReviewedOnly`, `TestPartitionUntrackedFiles` (a nested
  repository left out; only on `?untracked=1`), `TestRemovedTileModeRecord`
  (+ an offloaded tile keeps its record); obs
  `TestPartitionLogFollowEndsWithTheShare`; server `TestSSOLoginHeld`;
  deployments `TestUnprotectRefusedWhileRequired`; runner
  `TestPartitionBusyText` (+ the cap hit recorded); boot
  `TestPartitionWiring` (+ ops and credential gate wired, a held link's
  booted redemption 409, tile-status of a partition's frame); cmd/bx
  `TestBxPartitionOps` (+ purge without `--yes` posts nothing, `reviewed`,
  `bx logs --global|--user`).
- S1 smoke on pt/f7b-fix (isolated xbind, env from .dev.mk, Bash sandbox
  off): `TestPartitionsSmoke` 19/19 — the `logs` case now also follows
  alice's shared log as carol and checks the follow ends by itself once
  alice stops sharing — and `TestPartitionsSmokeW2` 5/5; 42 s and 75 s.
- `make fmt-check vet`, the repo guards (`.`, assetscan, sizebudget,
  docscheck, builtins, apicheck), `-race` on broker, boot, deployments,
  runner, users, server, obs, cmd/bx: green.

## Review fixes (pt/f7b-fix)

| Finding | Fix |
|---|---|
| (high) a member tile's drop erased the scope root's `part:` key and took its backup lock | `dropOnePartition` (now partitiondrop.go) holds and erases **the tile's own** only: `part:<tile>/<dep>/<pkey>`; namespaces only when the tile roots its scope (`rootsOwnScope`). `TestPartitionDropScopeMember` |
| (medium) a followed shared log outlived the share | `streamLog` takes a `still` check; partition logs re-ask `PartitionLog` every 2 s and end with a closing line. `TestPartitionLogFollowEndsWithTheShare`, the smoke's `logs` case |
| (medium) refusing a link already used said "refused" | the store change first, under one lock with the lapse; `RevokeInvite` says whether it revoked; not pending → 409 `already-effective` with the advice; an unused lapsed credential is still refused. `TestPartitionHeldLinkEdges` |
| (medium) seam collision with F17b | one deletion path and the corrected recipe in "Merge with F17b" (owners = the tile only); 95's contention table lists partitionrecords.go/partitionns.go |
| (medium) "Reviewed code only" missing; trust panel's last code changes; doctor's caps hit | `POST /partitions/reviewed` + enforcement (deployments `ProtectRequired`, `validateBinding`) + warnings + listing fields + `bx partition reviewed`; `trust.lastCodeChange` from the deploy log; runner `PartitionCapHits` → overview `capsHit` → doctor. Deviations name the limits |
| (medium) SSO provider change silent | `CredentialSSOChanged` from PATCH /auth-settings; the store's SSO gate in the callback (`sso_err=held`); refuse unbinds the email. `TestPartitionSSOProviderChange`, `TestSSOLoginHeld` |
| (medium) doctor's untracked check asked the workspace root | `?untracked=1`: a confined git (`confine.GitRead`) in each partitioned tile's own repository, nested repositories left out; doctor prints xbind's answer. `TestPartitionUntrackedFiles` |
| (medium) GET /partitions built every row for everyone | a person's own directory only; totals without vault opens or tree walks; bytes cached 1 min (dropped on a drop); code writers cached 15 s (dropped on users changes) |
| (low) held-credential gate failed open | a hold that can't be written revokes the link (500); the gate refuses unreadable/foreign/other-person files and a link no hold names; links are stored held (`held:`), so an older xbind never redeems them |
| (low) F7a's hold didn't span the drop | `dropOnePartition` takes `HoldPartition` first and keeps it to the end (counted holds nest) |
| (low) only the current primary's dep dropped | `partitionDeps`: every dep with a record, namespace, vault or log; each dropped and its key erased. `TestPartitionDropEveryDeployment` |
| (low) offloaded tile's mode record swept | `removedTileModeRecord` skips offloaded / offloaded-full tiles |
| (low) `instance` Go-cased with the error's text | lower-camel tags, `Error` never marshalled; `instanceView`: `errorClass` for all, `error` on the person's own row (`PartitionErrorClass`, moved from boot) |
| (low) `policies` to tile code | only when `canReadPolicies` |
| (low) stop/reset for a partition no one holds; state before rights | `partitionOpTarget`: rights first (non-reader 404, others' 403), then 409, then `livePartitionOf` checks a record or namespace exists (404, no notice) |
| (low) `bx partition purge` without confirmation | lists what it would delete (exit 1) unless `--yes`; doctor suggests `--partition <id> --yes` |
| (low) no CI coverage of the wiring | `TestPartitionWiring` asserts ops + gate wiring, a held link's 409, tile-status' partition; the unconverted probe says why it has nothing to probe (server's `TestPartitionGate` probes a synthetic row); `TestPartitionPeopleHooksThroughAPI` |
| (low) `bx logs` couldn't ask for global's or a shared log | `bx logs <tile> --global | --user <id>` (logs_partition.go) |
