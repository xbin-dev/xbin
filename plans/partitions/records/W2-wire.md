# W2-wire — wave 2's seams wired

Wave 2's integration of the partitioned-tiles plan: the seven packs merged
on `partitions` at ab705c60 — F5 (vault, registrations, notify, identity
records), F7a (terminals and agent sessions), F9 (attributed addressing of
the own global instance), F10 (cross-tile consent and the ledger), F15
(global and personal binds), F14 (the shell's marker and pending overlay)
and S1 (the partitions smoke) — made correct and green, their seams wired
where both sides now exist. Branch `pt/w2-wire`. This change edits neither
`docs/changelog.md` nor `plans/DECISIONS.md` (see §Notes for the
changelog).

## What the merge broke

Nothing at the build or test level: on ab705c60 as merged, `go build`,
`make fmt-check vet js-check`, `go test ./...`, `-race` on broker, runner,
proxy, auth, boot, server, term, push and `cmd/bx`, and the repo guards
(`.`, assetscan, sizebudget, docscheck, builtins, apicheck) were green, and
S1's smoke passed its 17 cases on a real isolated xbind. The packs' seams,
read against each other, held four semantic conflicts, each fixed at its
root with a test that fails without the fix:

1. **Bus subscriptions stopped counting in the ledger.** F10's review moved
   the ledger count out of `reachPartition` (into `allowAt`, after the
   grant); F5's `partSubReach` counted a registration and each delivery
   through `reachPartition`, so after the merge nothing of it was counted.
   Fixed with item 2 below.
2. **A shared bus needed consent.** F10 gave `reachPartition` a `shared`
   flag (a shared resource is no person's data); the merge wrapped F5's
   call as `shared=false`. Item 2.
3. **A failed data store took the metadata.** F5's `partition-records` hook
   was a data hook, registered (by file name) before `person-terminals`,
   the namespaces and `personal-binds`, and it removes each person's
   directory whole — which since F10 also holds that person's `ledger.json`.
   A switch whose later data store failed had already taken the partition
   records and the ledgers that `wipeHook.meta` exists to keep; a ledger
   write in flight could also leave an empty partition directory behind,
   which "holds data" would then count. Item 4.
4. **Sockets got a shared bus without the person's read.** F4's
   `busPartitionReaches` (the `/ws/events` filter) passed every event of a
   partitioned scope's shared bus on the tile's grant alone, so another
   tile's frame acting in a person's partition kept getting it after the
   person lost read on the scope. F10's rule for shared resources (the grant
   and the person's read, never consent) held for calls, data and push
   subscriptions, not for sockets — F10 had listed it for F5's conversion,
   which covered push subscriptions only. The filter now asks
   `reachPartition(…, shared=true)` per event
   (`TestPartitionSharedBusSocket`; [protocol.md](/docs/protocol.md)
   §/ws/events).

## Items 2–4

- **2. `reachPartition`'s `shared` flag vs F5's bus subscriptions.** The
  rule, from F10's and F5's: a subscription to another partitioned tile's
  **shared** bus is no person's data — its person's read on the scope is
  asked at registration (403), at every publish and at every delivery,
  never their consent, in both policy settings; its unstamped events reach
  the subscription only while the partition runs (F5's rule, unchanged);
  it is never counted in the ledger. A **per-person** bus of another
  partitioned tile needs the read and, with the policy on, the consent, and
  counts one `edge` in the subscriber partition's ledger at registration
  (after its write) and at each delivery (at its reach check, as Route
  counts a call). `partSubReach` now passes `sharedRes` of the bus resource
  (undeclared: per person) and returns the edge it reaches; `partSubCount`
  counts through the one ledger seam; the `count` flag and the closure are
  gone (partitionbussubs.go). `TestPartitionBusSubsSharedReach` (consent on:
  shared wall 200, feed 403; carol without read 403; dormant drop while
  stopped, delivered while running; lost read → dormant row, skipped at
  publish, refused at delivery; the feed's registration and delivery each
  one edge, the wall's none, an own-scope subscription none). Docs:
  partitions.md, resources.md, protocol.md (the subscription rows, the
  ledger row).
- **3. F13a's switch hold.** `actSwitch` released the hold (`end()`)
  before `settleAfterDecision`'s rescan; until the rescan the registry
  still shows the old mode, so on a tile that was running (a switch after
  "keep") a person's partition write could land after the wipe. The hold
  now spans the rescan and is released before the `reload` event, so
  reloading frames find the tile running (`rescanAfterDecision`, `end()`,
  `reloadAfterDecision`). S1's remount (runSwitch's deferred
  `MountEncrypted`) is untouched. `TestPartitionSwitchHoldsUntilSettled`:
  the registry's change hooks run in that window, and must see the hold and
  a partition's write refused (fails with the old order).
- **4. Wipe-hook order.** The order is now: every data hook (F13a's
  namespaces, vault, registrations; F5's partition-vault and
  partition-registrations; F7a's person-terminals; the partition
  namespaces; F15's personal-binds), then the metadata hooks in
  partitionwire.go's init: F10's `consents`, `ledgers`, and F5's
  `partition-records` last (`meta: true`, registered there). The records
  name whose a partition's leftovers are (PD-43): a data store's failure
  leaves them — and the ledgers and consents — whole, and a retry, a
  re-adoption and a record-driven sweep (F7b) still find them; the records'
  wipe removes each person's directory whole once the ledgers' hook has
  stopped their writes. `partition-records` stays a "holds data" store
  (the flag orders the wipe only). Checked: F5's partition-vault and
  partition-registrations, F7a's person-terminals and F15's personal-binds
  are data and stay data; F10's consents and ledgers are metadata.
  `TestWipeHooksCoverHoldsData` passes; `TestPartitionWireSeams` pins the
  order; `TestPartitionWipeFailureKeepsMeta` (a failing data hook: cron
  file gone, partition.json, ledger.json and the consent kept; the retry
  removes the directory and its level and the consent) fails on the merged
  order.

## Seams wired

| Seam | Wired |
|---|---|
| F7a ↔ F5: `termPartitionRecord` | `(*Broker).noteTermPartition` (partitionwire.go): a person's terminal or agent session opening on a partitioned tile writes their `partition.json` on the tile's primary (a non-primary target too), unless one exists; none while the tile is paused. `TestTermPartitionRecordWired` |
| F10 ↔ F15: the approval warning on `bx grant` | `writeGrantOK` (partitionconsent.go; broker.go's `grantMutation` calls it, 0 lines net): an approval reaching another partitioned tile's people's data answers `{ok, warning}`; `bx grant` prints it on stderr, stdout stays `ok`, no request added. Every other answer byte-identical. `TestGrantApprovalWarning`, `TestBxGrantWarning` |
| F10 ↔ F15: the warning on bindings (F10's "may") | `writeBindOK`: a global bind of a partitioned tile's http slot to another partitioned tile (an http binding is a call grant, 05 §3) answers `{ok, warning}`; `bx bind` prints it once. `TestBindApprovalWarning`, `TestBxBindWarning` |
| F10 ↔ F15: personal binds and the consent/ledger | per 05 §2–§3 already right once both met: a personal bind's provider isn't partitioned, so no consent is asked (either setting) and Route counts each call as `provider` to the personal tile. Pinned: `TestPersonalBindLedgerNoConsent` |
| F10 ↔ F15: the two "stop one person's instance" hooks | folded (below) |
| F9 ↔ F5: `?xbin-partition` at registration | `globalAddressRegRefused` (partitionglobal.go), from `PUT /cron/jobs` and `PUT /bus/subscriptions`: 400 with F9's words when the tile's recorded mode has user partitions or can't be read (the proxy's rule), a person's and the global instance's registrations alike, an encoded key too; unpartitioned tiles register it as before. `TestGlobalAddressRegRefused`; protocol.md, partitions.md |
| F10 → F5: a shared bus's delivery re-asks the read | push subscriptions (item 2) and sockets (merge conflict 4) |
| F15 → F8/F9: `xbin.iface(slot).endpoints[i].personal` | named in web/xbin-client.js's header |
| S1 → F9: drop today's answer from the `?xbin-partition` rows | done: `bob-never-sees-alice` and `admin` pin F9's answers (`psBadPartitionParam`, global's value) |
| F14 consent prompts | **not wired** — more than wiring (a module and a harness pass: `bx-shell.js` and `shots.js` are at their budgets); F14b moved to wave 4 beside F11/F12 in 95-work-packs.md |

## Deduplicated

- **One stop hook for a person's instance.** F10's `SetPartitionEdgeStop`
  and F15's `SetPartitionRestart` held the same `runner.StopPartition` in
  two places and boot wired it twice. Now `SetPartitionInstanceStop`
  (partitionRunSlot, beside the runner's other hooks); both old setters and
  their `…Wired` guards remain as thin names; boot wires it once;
  `TestPartitionConsentWiring` and `TestPersonalBindRestartWired` pass.
- The apicheck PersonOnly refusal cases and `TestBxOldXbind`'s race fix:
  one copy each after the merge (checked).

## Deviations

- **The switch's hold spans its rescan.** While held, `decide`'s "holds
  data" answers yes, so a code change racing a switch's own settle makes a
  pending request rather than an auto record (a manager decides it; no
  data is at risk).
- **A terminal makes a person's partition, so the tile holds data.** With
  `termPartitionRecord` filled, a person's terminal on a partitioned tile
  writes a `partition.json`, which F5's `partition-records` store counts:
  an empty manifest change after only terminal use is now a request, not
  an auto flip — and a switch then deletes the person layers and history
  F7a's auto flip left behind. partitions.md's "holds data" sentence says
  so.
- **Bus subscriptions count as `edge`, not `bus`.** A subscription into
  another partitioned tile's per-person bus is a reach of that tile's
  people's data: `edge` makes it show in `/partitions/edges` and the
  Policies tab's consent preview. F10's `LedgerBus` kind stays declared and
  unused; subscriptions to shared or unpartitioned buses are not counted
  (06 §6.1's "bus subscriptions by source" read as the cross-partition
  reach) — the owner may want the unpartitioned-source rows too.
- **The binding warning** goes beyond "bx grant prints it": F10 offered the
  bindings path as optional; it is additive and on partitioned tiles only.

## Seams still open, by owning pack

- **F6 (mail)** — `StartMail`, the mail store's wipe hook (per `<dep>` beside
  `partitionRecordDir`, which the records' wipe leaves), `LedgerTrigger`
  for private triggers' sources (with B2c).
- **F7b (ops)** — `GET /partitions` (`PartitionRegistrations`,
  `eachPartitionRecord`, `consentsView`, the ledger readers, `features`
  with `broker.GlobalAddressFeature`, guarded by
  `TestGlobalAddressFeatureServed`); reset/purge (`DropPartition`,
  `dropPartitionNS`, `wipePersonTerminalsOf` with `HoldPartition`); the
  users-store hooks (`PartitionUserDeleted`, `PersonalBindsUserDeleted`,
  `dropPartitionConsents`, `StopPartitionsOf`,
  `RevokeUserPartitionInstances`); the `partitions` event (ops `state`,
  `mode`, `consent-needed`, `consent` folded, PersonOnly kept); `GET /logs`
  and `GET /tile-status` for partition credentials; `bx status`'s
  `partition:` line and `bx logs`; `bx doctor`'s partition checks; personal
  binds as a uid-adoption source (optional).
- **F17b** — `partitionBackupSubject` for archives, `resenc` `Hold`/`Touch`,
  `part:` subkeys for partitions without namespaces.
- **B2a–d** — the agent template's partition default and shared
  conversations (F9's attribution reads as `whoUser`); `LedgerTrigger`
  (B2c).
- **F11** — the partitions page `/xbin/partitions` (the consent push's
  link), the person's consents, ledger and personal binds; the card's
  "details…" link (F14).
- **F12** — the admin Partitions section (personal binds list/delete, the
  sandbox list's person-terminal disks and blanked session names).
- **F14b (wave 4)** — the shell's consent prompts.
- **B3** — docs.
- **I1** — proxied long-lived streams (WebSocket/SSE) a revoked consent or
  a policy turned on doesn't cut (F10's open end; needs a proxy registry);
  a view-as session's `GET /frame-token` (S1: refused on every route,
  never minted outright); W1's "a person's start mounts the global
  instance's volumes" (F3/F4); prefs per partition.

## Flagged for the owner

- **PD-54 vs G1** (F15): every admin principal may list and delete a
  partition's personal binds. Code unchanged, as asked.
- **D33 and the trust base** (F15): a provider org's admin can bind their
  provider into every person's partition of a tile whose code they can't
  change.
- **"The partition chip"** (F14): read as the marker plus the `shared` chip
  on a non-primary deployment's window; the other reading (a chip on every
  window saying whose partition it shows) is a small addition.
- **03 §E's `state`** (F5): `active|orphaned`, never `dormant`.
- **06 §6.1's "bus subscriptions by source"**: see Deviations.

## Bugs found

- The four merge conflicts above (ledger count lost, shared bus consent,
  wipe order, socket filter) and F13a's switch-hold window (item 3, found
  by F5): fixed here.
- The smoke found no bug in daemon code: every failure while writing the
  wave-2 cases was the fixture (a user's terminal has no token without
  `termApi` and is offline without `termNet`, D17).

## S1 smoke on the merged tree

`TestPartitionsSmoke` (17 cases) and the new `TestPartitionsSmokeW2`
(test/isolated/partitions_smoke_w2_test.go, its own isolated xbind, S1's
helpers), with .dev.mk's env exported and the Bash sandbox off:

| Case | Result |
|---|---|
| zero-state, auto-mode, own-partition, data-apart, deployment, bob-never-sees-alice (now pinning F9's answers), cross-tile, global, admin (F9's 400 pinned), logs, view-as, caps, token-revocation, person-recreated, mode-keep, mode-switch, mode-pending-unpartitioned | PASS (17/17; 36 s) |
| W2 vault — alice's key is her backend's alone: bob's partition and global read 404, admins' `/vaults` and `/vault/<tile>` never name it; global's key is global's | PASS |
| W2 global-address — alice's frame with `?xbin-partition=global` reaches global as alice (reader, level read, the parameter consumed); bob's frame forging alice's headers reaches global as bob, never alice's data; other values 400; a cron job carrying it 400 | PASS |
| W2 personal-bind — alice's bind is in her partition's `XBIN_IFACE_MCP` (`personal: true`) and her document only; her partition's call reaches her tile (`From` the tile, `user:alice`); bob's partition and global 403; an admin can't create one | PASS |
| W2 terminal — alice's session frame and `$XBIN_PARTITION` say `user:alice`, it starts in her home; curl (kv API, the tile's API with bob's forged headers, `?xbin-partition=user:bob` → 400, the vault's keys) and `bx vault ls` reach her data and vault only | PASS |
| W2 cron — alice's job ticks her partition as `xbin/cron` (`user:alice`); nothing in bob's or global's kv; bob's partition doesn't list it | PASS (≈ 54 s waiting for the tick) |

`TestPartitionsSmokeReap` (opt-in, 11 min) was not re-run: its code is
unchanged.

## Tests run

- `go test ./...` (merged tree, then the branch), `make fmt-check vet
  js-check`, `-race` on broker, runner, proxy, auth, boot, server, term,
  push, cmd/bx, the repo guards; `make check` at the end.
- Integration (env exported, Bash sandbox off): `./internal/term/`
  (`TestConfined|TestTermMountPoints|TestPartitionLayer`) PASS;
  `./internal/runner/ ./internal/broker/` PASS (one SKIP:
  `TestPinnedBackendSeesCheckpoint/vm`, no `xbin-vmagent` built in this
  worktree); `TestCodingSandbox` and `TestCodingSandboxContract` PASS (the
  contract's target-declared SKIPs: `user-partitions` — in-process only —,
  `tty/unsupported`, `lifecycle/archive`).
- UI harness (unisolated, PORT 9041, fresh seed): `partitionMark` 35 PASS,
  `partitionSwitch` (the old shell) 14, `personalBinds` 10,
  `adminPolicies` 14 — F14's, F15's and F10's passes together on the merged
  shell and `shots.js`.
- New unit tests (internal/broker/partitionw2_test.go):
  `TestPartitionBusSubsSharedReach`, `TestPartitionSharedBusSocket`,
  `TestPartitionSwitchHoldsUntilSettled`, `TestPartitionWipeFailureKeepsMeta`,
  `TestTermPartitionRecordWired`, `TestGrantApprovalWarning`,
  `TestBindApprovalWarning`, `TestPersonalBindLedgerNoConsent`,
  `TestGlobalAddressRegRefused`; cmd/bx: `TestBxGrantWarning`,
  `TestBxBindWarning`; `TestPartitionWireSeams` pins the new hook order.

## Notes for the changelog (the owner folds these in)

Fold into the wave's partition entries (F5, F7a, F9, F10, F15), none a
change for a workspace without a partitioned tile:

- F5's: a subscription to another partitioned tile's **shared** bus needs
  the person's read access, never their consent; such a subscription into
  a per-person bus counts in the person's egress ledger (`edge`) when made
  and per delivery ([partitions.md](/docs/partitions.md),
  [resources.md](/docs/resources.md), [protocol.md](/docs/protocol.md)).
  A partitioned tile's `PUT /cron/jobs` or `PUT /bus/subscriptions` whose
  `path` carries `?xbin-partition` answers 400 when registered.
- F7a's: a person's terminal or agent session on a partitioned tile
  records their partition — the tile then holds data, so a later mode
  change is a request ([partitions.md](/docs/partitions.md) "holds data").
- F10's: `POST /api/xbin/grants` answers `{ok, warning}` for a partitioned
  tile's grant on another partitioned tile's people's data, and `bx grant`
  prints the warning; `POST /api/xbin/bindings` does too for a partitioned
  tile's http slot bound to another partitioned tile, and `bx bind` prints
  it ([protocol.md](/docs/protocol.md), [bx.md](/docs/bx.md)).
  `/ws/events` delivers a partitioned scope's shared bus to another tile's
  credential acting in a person's partition only while that person can
  read the scope.
- F13a's (already folded or not): a mode switch keeps its tile paused until
  the new mode is settled.
