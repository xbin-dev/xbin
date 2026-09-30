# 95 — Work packs

**Sizes:** **S** ≤ 1 day, **M** 2–4 days, **L** 1–2 weeks, **XL** > 2 weeks.
Each assumes one agent-developer, targeted checks per slice, and one full
`make test` / `make integration` at the pack's end (the owner's speed
guidance).

**Documentation rules:**
- every pack that adds HTTP/WS surface updates docs/protocol.md in the same
  change;
- every builder-visible pack adds a docs/changelog.md entry;
- F17a also adds its migration note (11 §7).

**∥** marks a pack that can run in **its own worktree** in parallel with
the other ∥ packs of its wave. Where two parallel packs touch one file, the
file and the split are named under "Merge contention".

**Revision 3 (owner rulings):**
- **New packs:**
  - F13a (mode switch: acts, wipe, grey-out);
  - F13b (template default and updater guard);
  - F14 (shell marker and pending overlay);
  - F15 (bind types);
  - F16 (workspace policies and the admin Policies tab);
  - F17a (sealed backups for every archive);
  - F17b (partition archives and erase hooks).
- **Removed:**
  - the rev-2 `enable`/`disable` acts (F1 → F13a keep/switch);
  - F7b's ciphertext partition archives (→ F17b);
  - all legacy agent-conversation work in B2b: the "Before <date>" list,
    "Delete my earlier private conversations" with `secure_delete`, the v2
    move, and the `rehome` contract reservation (PD-34);
  - the opt-in instantiation checkbox (replaced by F13b's opt-out).
- **Changed:**
  - F10's consent is gated by F16's policy;
  - F7b gains the held-credential policy;
  - F11 gains switch decisions, personal binds and credential
    confirmations;
  - B2b shrinks from L to M.

## Fabric

### F1 — Manifest, registry, mode states (01 §1-§5, §2.1-§2.3) — M
- Files:
  - `internal/registry/registry.go`: `Partition`/`PartitionMail`/`PartitionNote`
    in the **code kind** of `composeManifest`
    (`internal/registry/deployview.go:148-168`), `template.partition`
    parsed, `Resource.Shared`, the parse hook `:603-616`;
  - new `internal/registry/partition.go`: `ValidatePartition`, the
    structural rules, `PartitionSpec`/`PartitionState`/`PartitionRequest`,
    `PartitionedScope`;
  - new `internal/broker/partitionmode.go`:
    - `mode.json` (recorded, request, declined, history);
    - the state table, auto-recording on a tile without data;
    - `tileHoldsData` over **today's** stores (main and deployment
      namespaces, vault, registrations), plus a hook registry that later
      packs extend with their own stores;
  - the components API row;
  - the proxy's 409 gate for pending/invalid, with the pending body;
  - grant-approval refusals (`xbin:*`, `cap:sandboxes`, `cap:net-admin`,
    `cap:containers`);
  - the `POST /deployments/primary` refusal;
  - the docs/compat.md rule-7 note.
- Depends on: PD-02, PD-03, PD-05, PD-28, PD-44/PD-50/PD-51 (decided or
  defaults).
- Accept: `TestPartitionStateTable` (auto, pending, declined, withdrawn,
  invalid), `TestManifestFieldSplit`, `TestPinnedPrimaryRollback`, and the
  components part of `TestNoPartitionGolden`.

### F13a — Mode switch: acts, wipe executor, grey-out (01 §2.4-§2.6, §6) — M
- Files:
  - `POST /api/xbin/partitions/mode {act: keep|switch}`, gated on
    `mayManageTile` (`internal/broker/orgsapi.go:433-450`) with a person
    credential;
  - the **wipe executor**: stop, revoke, `holdNS`, then remove through the
    confined removers (`removeScopeData` `internal/broker/backup.go:433-453`,
    `dropNamespaceKV` `backup_cron.go:296-314`). It covers today's stores;
    later packs register hooks (F4 namespaces, F5 vault/registrations/records,
    F6 mail, F7a layers/history, F10 consents/ledger, F15 personal binds,
    F17b subkeys);
  - history, deploy-log lines and people's notices;
  - new `internal/server/partitionpage.go` (the in-frame page, called from
    `handleComponentStatic`, `internal/server/static.go:70`; the D36
    precedent);
  - the `/alerts` kind `partition-switch` (`internal/broker/diskmon.go:55-65`)
    and pushes to managers;
  - `bx partition switch|keep`;
  - the D127 promote/rollback preflight warning.
- Depends on: F1. Subkey erasure calls F17a's erase API (a stub until
  F17a merges).
- Accept:
  - 01 §Tests "mode acts" (who decides, stale `from`/`to`, keep deletes
    nothing, switch deletes the §2.6 list and keeps the keep list);
  - the switch page contains the note escaped, and the banner reaches
    readers;
  - a ui-harness pass of the in-frame page inside an **old** shell.

### F13b — Templates and updates never switch a mode (01 §2.7) — S ∥
- Files:
  - `internal/builtins/builtins.go` (`Instantiate` `:173-187`,
    `stripTemplateBlock` `:376-390`: write `template.partition` as the
    top-level key unless opted out or not isolated);
  - `internal/broker/templates.go` (the body gains `partition: false`,
    `:69-73`; `instantiateWorkspace` `:190-199`);
  - `internal/builtins/updates.go` (`ApplyReplace`/`ApplyMerge`
    `:504-604`: keep the installed `partition` with a jsonc-aware splice,
    and report the difference);
  - `cmd/bx/template.go` (`--no-partition`);
  - the Tile Manager's template card
    (`workspace-template/tiles/manager/index.html:415-450`: the checkbox,
    disabled without isolation).
- Depends on: F1 (the key names).
- Accept: `TestInstantiatePartitionDefault`, `TestUpdateKeepsPartition`,
  and the template-merge fixture (01 §Tests).

### F2 — Identity and routing (02) — L
- Files:
  - new `internal/util/partition.go`;
  - `internal/users` (`User.UID`, minted at creation or lazily);
  - `internal/auth/auth.go` (`Principal.Partition`);
  - `internal/auth/deployment.go` (instanceID partition and uid, 401 when
    not covered, synchronous revoke);
  - new `internal/broker/partitionroute.go` (`addressedPartition`,
    `callerPartition`, liveness);
  - `internal/broker/deploydata.go`, **Route/Decision section only**;
  - `internal/boot` (the Decision converter);
  - `internal/proxy/proxy.go` (Decision fields, identify headers,
    `EnsurePartition`/`TrackPartition`) and `internal/proxy/ingress.go`;
  - new `internal/server/partitionclass.go` plus the `internal/apicheck`
    guard;
  - the event filters (`internal/server/server.go:678-684`,
    `internal/server/termsessions.go`), `internal/events`, `busFilter`;
  - the `<meta>` in `static.go` (`headInjection`, not the switch page);
  - runner/status event routing.
- Depends on: F1; PD-08, PD-10, PD-11, PD-12, PD-29.
- Accept: `TestAddressedPartition`, `TestRoutePartitions` (the
  consent-policy cells use F16's store or a stub), `TestPartitionRouteClasses`,
  identify/ingress tests, token revocation, and `TestZeroStateRoute`
  unchanged.

### F3 — Runner (03 A) — L
- Files:
  - new `internal/runner/partitions.go`: state keys, `EnsurePartition`,
    admission classes, caps derived from memory, background limits, the
    10-min reap, stops, `PartitionsChanged`;
  - `deployments.go`, `sandboxcmd.go:132` (the non-primary network rule),
    `deploy.go`, `states.go`, `inspect.go` (build single-flight),
    `alwayson.go`;
  - `internal/sbx/sbx.go`;
  - `POST /partitions/limits`.
- Depends on: F1; stubs for F2's registrar and F4's env hook.
- Accept: 03 §A tests, including the PD-35 coverage items (hidden-tab
  streams passive, reap, no boot starts), and the isolated host-net test.

### F4 — Data namespaces and volumes (03 B) — L
- Files:
  - `internal/broker/deploydata.go`, **keys section only**;
  - `resources.go` (reach/namespace, the `"read"` refusals);
  - `resenc_wire.go` (`PartitionEnv`; the personal-bind env hook stays an
    empty slot until F15);
  - `internal/resenc/resenc.go` (PD-48);
  - `internal/runner/binds.go`;
  - `deployns.go` (namespaces, orphan rules, and the **wipe hook** for
    partition namespaces);
  - `diskmon.go`/`resusage.go`.
- Depends on: F1, F3.
- Accept: `TestNsKeysPartition`, `TestNoAdHocResourceKeys` extended,
  `TestSharedBindNeedsRegistryShared`, the reach table (consent policy
  off/on), resenc tests, `TestPartitionOrphanRules`, and `tileHoldsData`
  seeing partition namespaces.

### F5 — Vault, registrations, notify, identity records (03 C/D/E, 06 §8) — L
- Files: `deployvault.go`, `dormant.go`, `bussubs.go`, `cron.go`,
  `internal/push/api.go:287`, and new `internal/broker/partitionrecords.go`.
  Wipe hooks for the vault, registrations and records.
- Depends on: F2, F4; PD-20, PD-21, PD-26, PD-27, PD-43.
- Accept: 03 §C/§D/§E tests, `TestWipeHooks` for these stores, and
  delete/recreate with uid re-adoption. The backup manifest filter moved to
  F17b.

### F6 — Partition mail (04 §3) — M
- Files: new `internal/broker/partitionmail.go`, the doorbell dispatcher,
  the `partition-mail/1` feature, and the reset/purge/user-delete/**switch**
  hooks.
- Depends on: F2, F3, F5; PD-15.
- Accept: `TestMailRules`, inbox privacy, the doorbell/start rules, restart
  durability, and the switch removing the store.

### F7a — Terminals and agent sessions (06 §1-3) — M
- Files: `internal/term/term.go`, `sessions.go:132`, `agent.go:210-227`,
  `internal/server/agentapi.go`, `termsessions.go:101-116`, `history.go`,
  plus wipe hooks for person layers and partition history.
- Depends on: F2; PD-09, PD-22.
- Accept: 06 §Tests (term, term/server).

### F7b — Operations (06 §4-§10) — L
- Files:
  - the server routes `/api/xbin/partitions` (+ `stop`, `reset` with subkey
    erasure through F17a, `purge` for orphans only, `share-log`), and the
    `partitions` event;
  - `internal/obs/deploylogs.go:92`, and the backends metadata;
  - user-store hooks (`internal/users/users.go:615` delete, disable,
    level);
  - **credential notices and held credentials**: `internal/broker/usersapi.go:216-224,313-345`,
    invite redemption, `POST /partitions/credential-confirm`, the 24 h rule,
    read from F16's `credentialResetConfirm`;
  - the offload refusal (`backup.go:397-430`);
  - `cmd/bx/partition.go` (ls/stop/reset/purge/limits/share-log/mail),
    `cmd/bx/status.go:49-50`, `cmd/bx/doctor.go` (06 §7);
  - the trust warnings on `/alerts`.
- Depends on: F2, F3, F4, F5, F16; PD-07, PD-23, PD-24, PD-26, PD-46.
- Accept: 06 §Tests (server, push, users hooks incl. held credentials with
  a fake clock).

### F9 — F5: attributed addressing of the own global instance (05 §6) — M ∥
- Files: `internal/proxy/proxy.go` (consume `xbin-partition` for
  partitioned targets only; attribution; refusals), the Route rule 2
  exception, the JS fetch option, the SDK's `GlobalURL`, and the
  `global-address/1` feature.
- Depends on: F2. (PD-16 decided: yes.)
- Accept: 05 §Tests "proxy F5".

### F10 — Cross-tile edges: read check, policy-gated consent, ledger (05 §1-2, 06 §6.1) — M ∥
- Files:
  - the person-read check in Route rule 4 and cross-scope reach;
  - new `internal/broker/partitionconsent.go`, active only while
    `partitionConsent` is on (records, PersonOnly routes, `consent-needed`,
    push, and the turn-on ledger preview for the Policies tab);
  - the grant-approval warning texts (both settings);
  - new `internal/broker/partitionledger.go`;
  - `bx partition consent`, and the `bx doctor` edge flags;
  - wipe hooks for consents and ledgers.

  **It doesn't touch `netfn.go`:** the bind-time refusal moved to F15.
- Depends on: F2, F16; PD-12, PD-13 (decided), PD-14, PD-46.
- Accept: `TestPartitionEdgeMatrix` in both policy settings; PersonOnly
  refusals; the ledger counts.

### F15 — Bind types: global (today's authority) and personal (05 §3) — M ∥
- Files:
  - `internal/broker/netfn.go`: in `apiBindingSet` (`:780-794`), the
    partitioned requester keeps today's authority checks; the `validateBinding` 409 for a
    non-partitioned requester → a partitioned provider without global;
    per-partition `HTTPSlots`/`HTTPInterfaces` (`:325-392`);
  - `internal/broker/delegated.go`/`personal.go`/`grantMutation`:
    unchanged — today's bind authority (PD-54, owner 2026-09-29);
  - `grantedRole` (`broker.go:452-484`) stays resolveTarget's; the
    personal half of `grantedRoleIn` rides F2's `personalBindGrant` seam;
  - new `internal/broker/personalbind.go` (`data/partitions/binds/<uid>.json`,
    routes, validation, lifecycle hooks: transfer, user delete, the switch
    wipe);
  - the `PartitionEnv` personal-bind hook (F4's slot) and a restart of the
    one partition;
  - `bx bind --personal`;
  - the "global/personal" labels in the admin console's wiring view.
- Depends on: F2 (caller partition), F4 (`PartitionEnv`); PD-16, PD-54.
- Accept: 05 §Tests "bind types" (who may create, env visibility per
  partition, the call filter, the lifecycle); unpartitioned-requester
  goldens unchanged.

### F16 — Workspace policies and the admin Policies tab (05 §2, 06 §12.3) — S ∥
- Files:
  - new `internal/broker/policies.go` (`data/workspace-policies.json`,
    `GET/PUT /api/xbin/workspace-policies`, the `policies` event, route
    classes; precedents `internal/server/native.go:178-220` and
    `data/branding.json`);
  - `bx policies`;
  - the admin tile's `tabs/policies.js`, the `GROUPS` workspace entry
    (`workspace-template/tiles/admin/admin.js:126`), a `render()` arm and
    the `adminTabs` harness pass (docs/maintenance.md:520).

  Until F10 and F7b land, the two switches store their values and say
  "applies to partitioned tiles".
- Depends on: none; PD-55.
- Accept: `PUT` is admin only; the file survives a users-store rewrite; the
  `adminTabs` pass renders the tab.

### F17a — Sealed backups: key hierarchy, format, erase, DR (11) — L ∥
- Files:
  - new `internal/backup/seal.go` (`NewSealedWriter`, `Open`);
  - `internal/backup/backup.go` (`SchemaSplit = 3`, `data` pointer,
    `MaxSchema`);
  - new `internal/broker/backupkeys.go` (the keystore at
    `data/vault/.backup-keys/`, wrapping via `Barrier.EncryptFor`
    `internal/vault/barrier.go:294-311`, erase + tombstones,
    export/import);
  - `internal/broker/backup.go`: `putArchive` seals (`:295-315`); the main
    data is split into `.data.<TileKey>` (`writeBackup` `:72-134`,
    `backupTile` `:276-291`); single-file restore extracts locally
    (`:538-551`);
  - `backup_deploy.go` (`:597-640`);
  - `restore.go:48` and `backup_restore.go:164` → `Open`;
  - `builtin-tiles/s3-archiver/backend/{main.go,s3.go}` (subkey markers,
    `POST /archive/erase`, 422 on sealed `getFile`);
  - `cmd/bx` (`backup keys export|import`, `backup erase`);
  - the admin Backup tab's export status;
  - the `/alerts` kind `backup-keys`;
  - docs (11 §7 list), the migration note, and a DECISIONS entry
    superseding VD-4.
- Depends on: nothing in the partition fabric; PD-25, PD-56. It can ship on
  its own, ahead of partitions.
- Accept: 11 §Tests (round trips, tamper, plaintext golden, erase
  semantics, export/import, s3 erase, the old-release restore).

### F17b — Partition archives and erase hooks (11 §2-§4) — M
- Files:
  - new `internal/broker/backup_partition.go` (`.partitions.<TileKey>.<dep>.<pkey>`
    under `part:` subkeys, the restore rules);
  - the main manifest's cron/bus filter to `ownerPart == ""`
    (`backup.go:92-100`, S15; moved here from F5);
  - erase on partition sweep/purge/reset and on the switch wipe (the F13a
    hook);
  - the pre-switch plaintext-restore confirmation.
- Depends on: F17a, F4, F5, F13a.
- Accept: 11 §Tests "partition archive rules" and "a switch erases
  `ns:`/`part:`, not `tile:`".

### F8 — SDK, JS client, docs (07) — M
- Files:
  - `sdk/xbin.go` + tests: `Partition`, `PartitionUser`,
    `RequirePartition`, `CallerInfo.Partition/PartitionID`, mail helpers,
    `GlobalURL`;
  - `web/xbin-client.js`: `xbin.partition`, the fetch option, `personal`
    rows;
  - docs: new `docs/partitions.md` covering:
    - the model and the **mode rule**: auto on empty tiles, then switch or
      keep;
    - shared resources, mail, bind types, providers and the trust base;
    - backups/DR (→ 11), downgrades, and the honest statement;
  - protocol.md, resources.md, auth.md, elements.md, sdk.md, bx.md and
    compat.md, plus the changelog and plans/DECISIONS.md entries.
- Depends on: the specs of F1/F2/F6/F13a/F15 (start early, finalize last).
- Accept: sdk tests; `internal/docscheck` passes; protocol rows for every
  new route, header and field.

### F11 — The person page `/xbin/partitions` (06 §12.1) — M
- Files: a static page under `web/` served at `/xbin/partitions`
  (embedded, `assets.go`). Sections:
  - partitions, ledger, trust panel, notices, log shares;
  - consents (with the policy on);
  - personal binds;
  - credential confirmations;
  - **switch decisions for managers**: the typed confirmation, counts and
    the keep list.

  Links come from 403 texts, pushes and the in-frame page.
- Depends on: F7b, F10, F13a, F15, F16; PD-47.
- Accept: a ui-harness pass (a switch decision, consent with the policy on,
  a personal bind, a credential confirmation); no HTML transform;
  top-level only.

### F12 — Admin tile: Partitions section (06 §12.3) — S/M
- Files: `workspace-template/tiles/admin`, a runtime → partitions section:
  - per tile: mode, request/decline, Switch/Keep, and "reviewed code
    only";
  - limits, metadata rows, stop/reset/purge, personal-bind rows;
  - the sandbox labels.
- Depends on: F7b, F13a.
- Accept: ui-harness passes; an old scaffold renders and ignores the new
  fields.

### F14 — Shell: partitioned marker and pending overlay (06 §12.2-§12.3) — S/M
- Files:
  - `workspace-template/shell/bx-side.js` (the marker before ⋯ in
    `_itemTemplate` `:287-318`);
  - `bx-canvas.js` (the head dot → the marker on partitioned tiles, `:334`;
    the pending card overlay with Keep/Switch… for managers);
  - `shell-kit.js` (`partitionMark`) and `shell-css.js` (the `--bx-part`
    token, marker CSS);
  - the partition chip, and consent prompts (with the policy on) — the
    consent prompts moved to F14b (F10's API isn't there in wave 2).

  Design A unless the owner picks B or C (PD-53).
- Depends on: F1 (the components row), F13a (the mode API).
- Accept: a ui-harness pass (the marker on row and head with its tooltip;
  the overlay on a pending tile: Keep → runs, Switch → typed confirmation);
  the old-shell pass unchanged.

### F14b — Shell: consent prompts and the partition chip (06 §12.3) — S ∥
- **The partition chip (owner, 90 §I3):** a chip on every partitioned
  tile's window head, beside the marker, saying whose partition the window
  shows — `yours` (the viewer's own user partition), `shared` (a
  non-primary deployment's one instance) or `global` (a window addressing
  the global instance) — with a tooltip; `bx-canvas.js _cardTemplate`, the
  marker's hue, quiet (not a button); the `partitionMark` harness pass
  extended.
- Files: `workspace-template/shell/partition-mode.js` (the prompt's words
  and bodies, beside the card overlay's; `hack/partition-mode.test.mjs`),
  `bx-canvas.js` or `bx-shell.js` (a prompt when a partitioned tile's call
  into another person-scoped tile needs the person's consent — **only with
  the `partitionConsent` policy on**), and a ui-harness pass; the
  `TODO(consent prompts)` in `partition-mode.js` and docs/partitions.md's
  "Not documented yet" line go.
- Built against F10's contract: the `partitions` op `consent-needed` event
  (to the person's own sessions) and `GET/POST/DELETE
  /api/xbin/partitions/consents {from, to}` (PersonOnly — the shell calls
  it as the signed-in person, never through `xbin.fetch`); F16's policy.
- Depends on: F10, F14, F16.
- Wave 4, beside F11/F12 (W2-wire: F10's API is merged, but the prompt is
  more than an integration's wiring — `bx-shell.js` and `shots.js` sit at
  their size budgets, so it brings a module and a pass of its own).
- Accept: a ui-harness pass (policy on: a refused cross-partition call
  prompts, consenting lets the retry through, declining keeps the 403;
  policy off: no prompt); the old-shell pass unchanged.

## Builtin tiles

### B1 — coding-sandbox partition keying (09 §1, 07 §4) — M ∥
- Files: `builtin-templates/coding-sandbox/_backend/manager.go:276-341`,
  `create.go:403`, `quotas.go`, `operator.go`, the `hello` caps,
  `sdk/sandboxcontract`, `docs/sandbox-manager.md`. There is no `rehome`.
- Depends on: only the header names (F2); wave 0 against test-set headers.
- Accept: the contract suite, including the unchanged `partitionChecks`.

### B2a — Agent: modes, split, engine, default (08 §1-2, §6-9, §11 states) — XL
- Files:
  - `builtin-templates/agent/xbin.json` (`template.partition`,
    `partitionMail`, `partitionNote`, uses);
  - `scope.json` (`conf`, `team`);
  - `_backend`: mode detection; per-mode migrations; 2^40 id seeding;
    `openShared(team)`; the `conf` mirror; per-mode locks; the user-mode
    resume rule; the LLM flock semaphore; the old-manager degrade;
    `xbin.agent/home` labels; the `/mailbox` skeleton; `personal: true`
    interface rows offered only in the person's conversations;
  - the frontend's three states and hidden-tab stream closing
    (`model/stream.js`).
- Depends on: F1–F6, F13b, B1; PD-30, PD-35 (decided), PD-36.
- Accept:
  - the legacy golden (every existing template test passes with
    `XBIN_PARTITION=""`);
  - two people's private chats in an e2e;
  - global's `db` is never mounted in partitions;
  - sandboxes are separated, and old managers degrade;
  - a new instance starts partitioned.

### B2b — Agent: shared conversations, two homes (08 §3) — M
- Files: `_backend` (global's listing/ACL under attributed F5,
  publish/copy between homes); the frontend's two homes (lists, the router
  by id range, streams); the ui-harness seed.
- Depends on: B2a, F9; PD-31. **The rev-2 legacy-conversation work is
  removed** (PD-34 decided).
- Accept: a shared chat created and streamed via global; the router picks
  the home by id; the global-viewer state.

### B2c — Agent: channels, triggers, schedules via mail (08 §5, §7) — L ∥
- Files: `_backend/channels.go:480-490`, `channel_links.go`, `outbox.go`,
  `triggers.go`, `schedule.go`, `mailbox.go`, and the usage summaries.
- Depends on: B2a, F6, F9; PD-37, PD-38, PD-46.
- Accept: a linked-DM round trip; a forged `outbox/add` refused; a private
  webhook trigger; an empty or overlapping match refused; idempotent
  retries.

### B2d — Agent: non-secure conversations and "share a copy" (08 §4) — L
- Files: `_backend` (the `team` schema, the `hosted` table, the host engine
  scope, widening pause/re-confirm, share-link refusal, mail doorbells,
  **share-a-copy** via F5); the frontend's modal, ⚠ chip, composer lock and
  "Add a copy of my …".
- Depends on: B2b, B2c; PD-32 (decided), PD-33.
- Accept:
  - a ui-harness asserting pass (the modal on each start, the chip, the
    locked composer, widening → paused);
  - a forged `team.host` row isn't driven;
  - fencing;
  - a copied file lands in the shared conversation while the original
    stays private.

### B3 — Bridge, webhooks, sandbox-terminal, llm-gw (09 §2-5) — S/M ∥
- Files: bridge `AGENTS.md`, webhooks/sandbox-terminal `API.md`,
  `docs/agent-inbox.md`, and llm-gw's per-(From, Partition-Id) counters.
  The notes cover global binds (today's authority) and personal binds.
- Depends on: B2c (for the e2e).
- Accept: the bridge and webhooks e2e against a partitioned agent; llm-gw
  counters per partition id.

## Integration

### I1 — End-to-end, compat and security suites — L
- `test/partitions_test.go`:
  - a fixture tile with shared, read-only and partitioned resources, and
    two people;
  - **the mode rule** (auto, pending, keep, switch, rollback, template and
    update fixtures);
  - cron, bus, mail, ingress → global;
  - the consent policy off and on;
  - personal binds.
- `test/isolated`: partition binds, `"read"` read-only, host-net isolation;
  the sandbox-manager suite's `user-partitions` section through xbind (a
  partitioned fixture consumer bound to the coding-sandbox copy, so the
  manager's parser meets F2's real headers; drop its skip in
  `codingsandbox_test.go`, B1).
- `test/downgrade_test.go`: the previous release's xbind, uid re-adoption,
  a plaintext archive restored by the old release, and a sealed one
  refused.
- `test/partitions_security_test.go` (10 §B.2) and `TestNoPartitionGolden`.
- ui-harness passes: the agent, the person page, the admin
  Partitions/Policies, the shell marker/overlay, and the old scaffold with
  a pending tile.
- Depends on: everything shipped.

### I2 — Rollout — S
- On the QA box (deploy/qa), measure the **default-partitioned** agent's
  RSS and mount cost: 20 people, two instances. That sets `E` and the caps
  (PD-18, PD-35).
- Verify that gocryptfs counts fall after the idle unmount.
- Verify the sealed-backup alert and the export on an upgraded workspace.
- Release notes, the migration note, and the docs site.

## Order and parallel groups

| Wave | Packs (∥ = its own worktree) | Gate to the next |
|---|---|---|
| 0 | ∥**F1** ∥**F16** ∥**F17a** ∥**B1** ∥F8 skeleton | F1 merged (F17a may run into wave 2; it gates only F17b) |
| 1 | ∥**F2** ∥**F3** ∥**F4** ∥**F13a** ∥**F13b** | F2/F3/F4 merged, zero-state goldens green |
| 2 | ∥**F5** ∥**F7a** ∥**F9** ∥**F10** ∥**F15** ∥**F14** | F5 merged |
| 3 | ∥**F6** ∥**F7b** ∥**F17b** ∥**B2a** | B2a merged |
| 4 | ∥**B2b** ∥**B2c** ∥**F11** ∥**F12** ∥**F14b** ∥B3 docs | B2b, B2c merged |
| 5 | ∥**B2d** ∥**I1**, F8 finalization | all green |
| 6 | **I2** | — |

**Dependency graph** (→ = "needed by"):
- F1 → F2, F3, F4, F13a, F13b, F14;
- F2 → F5, F7a, F9, F10, F15;
- F3 → F4 → F5 → F6, F7b, F17b;
- F4 → F15;
- F13a → F14, F17b, F11, F12;
- F16 → F10, F7b, F11;
- F17a → F17b;
- F15, F10 → F11;
- F10, F14, F16 → F14b;
- F1–F6, F13b, B1 → B2a → B2b, B2c → B2d;
- B2c → B3.

**Merge contention** (parallel packs sharing a file):

| File | Packs | Split |
|---|---|---|
| `internal/broker/deploydata.go` | F2, F4 | Route/Decision section vs keys section |
| `internal/server/static.go` | F2, F13a | `headInjection` meta vs `handleComponentStatic` switch page |
| `internal/broker/broker.go` | F1, F15 | different waves: F1's approval refusals first, then F15's `grantMutation`/`grantedRoleIn` |
| `internal/broker/netfn.go` | F15 only | F10's bind-time refusal moved into F15 |
| `internal/broker/backup.go` | F17a, F17b | sequential; F5 no longer touches it |
| `workspace-template/tiles/admin/admin.js` (`GROUPS`, `render()`) | F16, F12, F17a | append-style edits; resolve line-anchored |
| `internal/broker/usersapi.go` | F7b only | — |
| `internal/server/partitionclass.go` (`partitionUnconverted`) | F5, F7a, F7b | each pack removes only its own rows; gofmt realigns the whole map, so resolve by taking the union of the removals, never one side: after F5 and F7a only `GET /logs` and `GET /tile-status` stay (F7b's); tests probe a synthetic unconverted row, never a real one |
| `hack/ui-harness/shots.js` (the `require` lines, `PASSES`) | every pack adding a harness pass (F14, F15, …) | the union; F14 registers its pass on a line of its own after `PASSES` (`PASSES.partitionMark = …`), so it merges with the others' edits; the file sits at its size budget |

New code goes in new files (`partitionmode.go`, `partitionroute.go`,
`partitionrecords.go`, `partitionmail.go`, `partitionconsent.go`,
`partitionledger.go`, `personalbind.go`, `policies.go`, `backupkeys.go`,
`seal.go`, `partitionpage.go`) with thin call sites.

**Rough effort:**

| Area | Developer-weeks |
|---|---|
| Fabric (F1–F17b) | ≈ 12–13 (rev. 2's 8–9, plus ≈ 4.5 for F13a/b, F14, F15, F16, F17a/b, minus the moved partition archives) |
| Agent (B2a–d) | ≈ 4.5–5 (no legacy-conversation work; share-a-copy and the default added) |
| B1 + B3 | ≈ 1.5 |
| I1/I2 | ≈ 2–2.5 |

With five parallel tracks (fabric core; edges/binds/ops; backups; UI —
shell, admin, person page; agent) that is ≈ 7–8 calendar weeks. F17a is
independent and can ship first.

## Risks to watch while building

- **Wipe completeness (F13a).** Every store that lands later must register
  a wipe hook. `TestWipeHooks` fails when a store under `data/partitions`,
  `.xbin/term-part` or `.xbin/partition` survives a switch.
- **`tileHoldsData` false negatives** would let a manifest edit switch a
  tile that holds data. Test each store, and err toward "holds data".
- **Token revocation ordering (F2/F3/F13a):** revoke before publishing any
  new state or starting the wipe.
- **Sealed-backup DR (F17a):** the export nudge must be loud, and the
  migration note clear. Sealing makes a lost keystore mean lost backups.
- **gocryptfs process count and cold-start latency** with the default-
  partitioned agent (I2).
- **Build stampede** on save for tiles with many live partitions (03 §A.3/7).
- **Global binds (F15)** keep today's bind authority (PD-54, owner
  2026-09-29): no narrowing. What remains is the trust base — every global
  bind, a provider org admin's (D33) included, reaches every person's
  partition; the trust panel lists them.
- **The agent's per-host engine scope (B2d)** touches D81 fencing. Keep it
  behind the non-secure feature.
