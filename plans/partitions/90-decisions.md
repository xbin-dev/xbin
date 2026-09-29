# 90 — Decisions register

One entry per question, deduplicated across areas. Entry kinds:
- **DECIDED** — the owner ruled (2026-09-29);
- **DEFAULT** — a sensible default the plan takes (veto if wrong).

When accepted, entries become plans/DECISIONS.md D-entries. Their numbers
are taken from the next free D-number at merge time (not reserved here), because parallel
agents have collided on D-numbers before (D88). PD-42 is unused (numbering
gap kept so references stay stable).

**Owner calls: all ten ruled on 2026-09-29.** They are PD-44 (mode), PD-16
(user → global, as bind types), PD-07 (admins), PD-13 (cross-tile), PD-35
(agent default), PD-34 (old agent chats), PD-32 (non-secure chats), PD-25
(backups), PD-23 (code trust) and PD-06 (downgrade). Revision 3 adds
PD-49–PD-57, the defaults those rulings needed, and §H lists the few
questions they raised.

---

## A. Model and manifest

**PD-01 — Partition as a second axis; key encoding.** DEFAULT.
- Rec: a second key beside the deployment; wire `user:<id>` | `global`;
  storage/wire id pkey = H(id, uid) (PD-43). Not a deployment name (user ids
  allow `.`/`_`/digits-first and 32 chars, deployment names don't; `global`
  is a legal deployment name); never a raw id on disk; never in a URL path.

**PD-02 — Manifest shape.** DEFAULT (owner confirm).
- Rec: `"partition": ["user"]` | `["user","global"]` (the owner's array) plus
  optional `"partitionMail": "/path"` (04 §3) and `"partitionNote"` (PD-57).
  Unknown words are errors, so a later `"org"` can't be silently ignored.

**PD-03 — Validation and structural rules.** DEFAULT.
- Rec: invalid lists run no backend (409) and delete nothing; a partitioned
  tile using same-scope resources must root its scope; all tiles in a
  partitioned scope ask alike. This resolves "data is per scope, the flag
  per tile".

**PD-04 — The global instance lives at today's keys.** DEFAULT (rev. 3 note).
- Rec: global uses today's keys (state, run dir, token, log, data, vault,
  registrations). With PD-44 a switch wipes them anyway. The keys still keep
  a downgrade showing global's data, never a person's, and give
  auto-recorded tiles one storage layout.

**PD-05 — Resources partitioned by default; `shared` opts out.** DEFAULT.
- Rec: partitioned by default; `"shared": true | "read"` opts out, at today's
  keys. A forgotten flag keeps data private rather than sharing it.

**PD-44 — Partition mode.** **DECIDED (owner, 2026-09-29).**
- Ruling: the manifest's `partition` sets the mode **only while the tile has
  no data**. On a tile that holds data, a manifest change is a **mode-switch
  request**:
  - the tile's frontend greys out with the alert "a partition mode switch
    is requested; all data in this tile will be deleted for the switch to
    happen";
  - the switch happens only after an explicit confirmation by someone
    allowed to decide (PD-49), and it then **deletes all the tile's data**
    (every namespace, vault and registration).
- Integrator additions (adopted):
  - the alert also offers **"Keep the current mode"**, which declines: it is
    recorded, the tile runs its recorded mode again, and nothing is deleted;
  - builtin template updates never add or remove `partition` in an existing
    instance's manifest, so an update can never trigger a wipe prompt
    (PD-52).
- Where it is designed:
  - states (recorded / requested / pending / declined / invalid), the
    audit trail and the grey-out surfaces: 01 §2;
  - what a switch deletes and keeps: 01 §2.6;
  - pending runs nothing: PD-50.
- Replaces: rev. 2's `enable`/`disable` acts, the `held` state and
  "disable leaves partitions dormant".

**PD-49 — Who decides a switch.** DEFAULT (new; the owner asked the plan to define it).
- Rec: a **tile manager** (`mayManageTile`,
  `internal/broker/orgsapi.go:433-450`): a workspace admin, the tile's user
  owner, or an admin of the owning org.
  - They act with their own session, app or device credential, never a tile
    principal. "Keep" and "switch" have the same audience.
  - Why not write level: writers raise requests by changing code, but
    deleting other people's data is governance. The manager set already
    sets lifecycle, including offload, which removes the tile's data
    (`internal/broker/lifecycle.go:36`).

**PD-50 — While pending, nothing runs.** DEFAULT (new; the owner asked the plan to pick).
- Options: (a) no instance of the primary runs (409, grey-out); (b) the
  recorded mode runs read-only.
- Rec: (a). Read-only can't be enforced for mounted sqlite/filesystem
  resources without breaking code. The running code asked for the other
  mode. "Keep" restores service in one act (01 §2.3).

**PD-51 — "Holds data".** DEFAULT (new).
- Rec: decided from xbind's own stores (01 §2.2). A tile holds data when it
  has any of:
  - a namespace with content (a cipher directory beyond its gocryptfs
    config, a kv key, a blob);
  - a vault key;
  - a registration row;
  - a partition record, mail or personal bind.

  Empty provisioned volumes don't count; an empty sqlite file does, so the
  rule errs toward asking.

**PD-52 — Templates and updates never switch a mode.** DEFAULT (new).
- Rec:
  - the instance default lives in the stripped `template` block
    (`template.partition`), so a `git merge template/main` (D50) can't add
    a top-level `partition`;
  - `bx builtin update` keeps the installed `partition` value
    (`internal/builtins/updates.go:504-604`);
  - instantiation writes the default unless the new body field
    `partition: false` is sent or xbind lacks `--isolate`;
  - a D127 promote/rollback preflight warns when the target code asks for a
    different mode (01 §2.7).

**PD-57 — `partitionNote`.** DEFAULT (new).
- Rec: an optional manifest string (≤ 280 chars, code kind) shown on the
  switch surfaces under xbind's fixed sentence as "`<t>` says: …". It lets a
  tile name its data in its own terms; the agent's note names conversations,
  memory and schedules (08 §10). It can't soften the fixed text, which always
  comes first.

**PD-17 — Deployments × partitions.** DEFAULT (revised, C13).
- Options:
  - (a) user partitions only on the primary in v1. A non-primary deployment
    runs one instance on its own namespace (`XBIN_PARTITION=global` when its
    code asks), writers only. Switching the primary is refused (promote
    instead).
  - (b) per-deployment partitions (rev. 1).
- Tradeoffs:
  - (b) adds a key dimension and N×M admission (D127q counts states: three
    writers on `+dev` exhaust it);
  - (a) loses in-place testing of user mode; a scratch copy of the tile
    covers that.

  Keys keep `<dep>` so (b) can come later.
- Rec: (a).

**PD-28 — What can't be partitioned.** DEFAULT (extended, S5, C14).
- Rec: for partitioned tiles, refuse `xbin`/`xbin:*`, `cap:sandboxes`,
  `cap:net-admin`, `cap:containers`, `chrome` and `vm` (409 at approval,
  invalid request otherwise). `"shared": "read"` on sqlite is invalid.
- User partitions always get the non-primary network rule. A `net → host`
  binding or splice applies to global only. Offload of a partitioned tile is
  refused in v1 (C16).

**PD-40 — The word "partition".** DEFAULT.
- Rec: keep `partition`; docs say "user partitions" / "partitioned tiles";
  sandbox-manager.md renames its prose for per-consumer sets to "consumer
  scopes" (no wire change).

**PD-41 — D-number allocation.** DEFAULT: numbers assigned at merge.

**PD-43 — Identity of a person across delete/recreate.** DEFAULT (revised, C2, S17, C11).
- Rec:
  - an immutable random `uid` in the users-store record (minted at
    creation, or lazily at first partition use); pkey = H(id, uid);
  - records (`partition.json`, each namespace's `ns.json`) carry (user, uid,
    created);
  - a user record without a uid re-adopts the uid of records created after
    its `Created`;
  - partitions are orphaned only on recorded deletes;
  - providers get the pkey as `X-XBin-Partition-Id`; consents and personal
    binds are keyed by uid;
  - for people who held partitions, deleting the user moves `homes/<id>`
    and their agent history to an orphan area.

**PD-45 — Read-only shared resources.** DEFAULT (S8).
- Rec: `"shared": "read"` — global writes, user partitions read. Anything
  that routes a person's data lives in global's own data or a `read`
  resource, never in a `true` one.

## B. Identity, routing and binds

**PD-08 — View-as into partitions.** DEFAULT.
- Rec: refused for user partitions; F5 to global allowed (read-only).

**PD-09 — Admins and other people's sessions on partitioned tiles.** DEFAULT (S2).
- Rec: on partitioned tiles:
  - refuse reattach **and every agent-session drive route**;
  - no `term`/`session` events to admins;
  - the `?user=` listing leaves out session names;
  - kill stays (governance).

**PD-10 — Root token and person-less terminals/frames.** DEFAULT.
- Rec: → global if declared, else 403 / API off. `--no-auth` = owner = global.

**PD-11 — An admin person calling a partitioned tile.** DEFAULT.
- Rec: their own partition; no admin qualifier.

**PD-12 — Non-partitioned callers → partitioned tile.** DEFAULT.
- Rec: global only; 403 without global; 409 at bind time for http slots
  without global.

**PD-13 — Partitioned caller → partitioned provider.** **DECIDED (owner, 2026-09-29).**
- Ruling: the grant plus the person's read access on X. **Per-person consent
  is a workspace policy**, set by admins in the admin tile on a new
  **Policies** tab under the workspace group. Default: consent **not**
  required.
- Design:
  - the policy store and tab: PD-55, 05 §2;
  - when the policy is on, the rev. 2 consent machinery applies: records
    keyed by uid, PersonOnly routes, prompts, `/xbin/partitions`;
  - the approval warning and the per-person egress ledger stay on in
    either setting.

**PD-14 — Partitioned caller → non-partitioned provider.** DEFAULT.
- Rec: allowed through a global bind (PD-16) or grant. xbind sends
  `X-XBin-Partition` (display) and `X-XBin-Partition-Id` (key). Providers
  key on (From, Partition-Id) — plus Deployment where they already key on
  it (user partitions are primary-only, PD-17) — with `""` ≡ `global`, and
  there is a consumer-side degrade for sandbox managers whose `hello.caps`
  lack `partitions`.

**PD-16 — User partitions → global things.** **DECIDED (owner, 2026-09-29).**
- Ruling: yes, modelled as **bind types**.
  - **Global binds.** Binding a shared or global tile is only possible for
    admin-level people, who could also change that shared tile's code. An
    admin's bind to any global tile is always a global bind.
  - **The same-tile path (F5).** A person's own frame, terminal or
    partition backend may call its own tile's global instance. It is
    allowed and attributed to the person.
  - **A new `personal` bind type.** When a tile is owned by a user (D88
    personal tiles; later maybe groups), a personal bind lets only that
    user's partitions reach it. Other users' backends don't see the bind,
    and xbind matches the tile's owner to the caller's partition.
- Design: PD-54, 05 §3, 05 §6.

**PD-54 — Bind records and rules.** DEFAULT (new; implements PD-16).
- **Global binds** are today's record, `bindings` in the workspace
  `xbin.json` (`internal/registry/registry.go:300`), unchanged in shape.
  **DECIDED (owner, 2026-09-29):** creating one follows **today's bind
  authority, partitioned or not** — whoever may manage the requester's
  bindings and has authority to bind to the target (workspace admin, org
  admin D26/D33, personal owner D88); the same for approving a `uses` grant
  (an http binding is a grant, `internal/broker/broker.go:467-470`). The
  only partition-specific addition is the personal bind below.
- **Personal binds** are xbind state at `data/partitions/binds/<uid>.json`:
  - only the owner of a user-owned provider creates one, for their own
    partition of a partitioned requester they can read, and only on a multi
    http slot (v1);
  - the entry shows only in that partition's `XBIN_IFACE_<SLOT>` and
    frames;
  - it counts as the call grant only when the caller's partition is the
    provider's owner (live ownership and uid check);
  - admins can list and delete personal binds but never create them;
  - they are dropped when the provider is transferred, the person is
    deleted, or the requester switches mode.
- Why not in the workspace manifest: a person's wiring is their metadata
  (PD-46), keyed by uid, and never read by older binaries.

**PD-29 — API route classes for partition principals.** DEFAULT.
- Rec: a second table with default-deny. The guard test fails on any route
  without a row, on any `DeploymentScoped` route without a partition row, and
  on any route the JS client or SDK calls without one. The PersonOnly class
  covers acts tile code must not perform: consents, log shares, personal
  binds and credential confirmations.

## C. Runtime, data, operations

**PD-06 — Older xbind fails open for new writes.** **DECIDED (owner, 2026-09-29).**
- Ruling: accept. The SDK's fail-closed `xbin.RequirePartition()` is the
  lever for tiles that must never run as one instance (10 §A.3, 07 §1).
- Rejected: renaming the runtime, and a `requires` key (deferred, C21).

**PD-07 — The admin-access guarantee.** **DECIDED (owner, 2026-09-29).**
- Ruling: hygiene now (a), plus (a+) as a **workspace setting, off by
  default**. Cryptographic sealing (b) is future work.
- (a): no xbind read path for admins (API, view-as, sessions, logs, vault,
  bus, mail, backups they can decrypt), governance kept. Account takeover is
  audited and notified (06 §9).
- (a+) is the policy `credentialResetConfirm` on the Policies tab (PD-55).
  With it on, an admin- or org-admin-made sign-in link, password or SSO
  email for a person who holds partitions takes effect only after that
  person confirms from a signed-in credential, or 24 h after they were
  notified. Refusing revokes it (06 §9).

**PD-55 — Workspace policies.** DEFAULT (new; serves PD-13 and PD-07).
- Rec: a separate xbind-owned file, `data/workspace-policies.json`,
  `{schema: 1, partitionConsent: false, credentialResetConfirm: false}`.
  - A separate file survives an older xbind rewriting `data/users.json`,
    which would drop unknown keys (precedent: `data/branding.json`,
    `internal/boot/boot.go:680`).
  - Routes: `GET/PUT /api/xbin/workspace-policies` (PUT admin only),
    `policies` event, `bx policies`.
  - UI: the admin tile's new **workspace → policies** tab
    (`workspace-template/tiles/admin/admin.js:126`).
  - The name avoids the D20 grant ceiling `/policy`, which stays in the orgs
    tab (`workspace-template/tiles/admin/tabs/orgs.js:300-302`).

**PD-15 — Global ↔ user communication primitive.** DEFAULT (S4, C21).
- Rec: shared resources plus **partition mail**, an xbind-owned,
  barrier-sealed, per-addressee drop box:
  - only the tile's global instance may mail a person; a person's partition
    may mail only global;
  - xbind stamps `from`; delivery is at-least-once until acked;
  - the doorbell rings the declared path;
  - mail may start only a partition that has run before, rate-limited.

**PD-18 — Admission, caps and reaping.** DEFAULT (C4, C5, S20; rev. 3: must cover PD-35).
- Rec:
  - running caps derive from host memory: `clamp(MemTotal/4 ÷ E, 4, 32)`
    per tile and `clamp(MemTotal/2 ÷ E, 8, 128)` per workspace, admin-settable;
  - passive SSE streams don't make a partition busy, and interactive starts
    evict LRU partitions not in use;
  - background starts never evict, run at most 4 concurrently, and mail
    starts at most 6/min per tile;
  - cron retries with jitter; per-partition caps are 16 cron jobs and 16
    subscriptions;
  - a 10-min idle reap; global is exempt;
  - per-partition disk ceilings.

  With the agent partitioned by default (PD-35), 03 §A.5 checks these
  cover that load; I2 sets E from a QA-box measurement of the default agent.

**PD-19 — User partitions need `--isolate`.** DEFAULT.

**PD-20 — Liveness gate.** DEFAULT.
- Rec: a partition runs, fires cron and gets bus/mail deliveries only while
  its person exists (same uid), is enabled and can read the tile.

**PD-21 — Public and interface-instance surface.** DEFAULT.
- Rec: ingress, `exposes` and interface instances route to global only; a
  user partition's registrations are stored dormant with success.

**PD-22 — Terminal layer, working directory, history per person.** DEFAULT (S16, C17).
- Rec:
  - layers live at `.xbin/term-part/<TileKey>/<pkey>` and sessions start in
    `$HOME`;
  - agent history of partitioned tiles is keyed by pkey;
  - all of it is swept with the partition, and wiped by a mode switch;
  - none of it is backed up.

**PD-23 — People who can change the code (and providers').** **DECIDED (owner, 2026-09-29).**
- Ruling: warn (docs, trust panel, `bx doctor`, `/alerts`), plus
  the optional per-tile admin switch **"reviewed code only"**. It requires
  the tile's primary and every provider bound to it to be protected (D127m)
  (06 §4).

**PD-24 — Logs and debugging.** DEFAULT (C15).
- Rec: a person reads their own partition log and may share it for up to 14
  days; admins see crash metadata; crash errors name the partition.

**PD-25 — Backups.** **DECIDED (owner, 2026-09-29).**
- Ruling: per-partition archives as ciphertext, and the scope grows:
  **encrypt all backups**, with a **key hierarchy for crypto-erase**.
  - The vault key encrypts per-partition and per-namespace subkeys, and
    archives are encrypted under those subkeys.
  - Archivers see subkey names as metadata only (to delete them), never key
    material.
  - Deleting a subkey crypto-erases that data in every existing backup (a
    deleted user or partition, a wiped tile after a mode switch).
  - Old unencrypted archives still restore.
- Design: PD-56 and [11-backup-encryption.md](11-backup-encryption.md).

**PD-56 — Key hierarchy and sealed archives.** DEFAULT (new; implements PD-25).
- Rec:
  - **Subkeys** are random 256-bit keys (HKDF keys derived from the DEK
    can't be deleted), one per subject: `tile:` (source, terminal layer,
    records), `ns:` (a namespace's data) and `part:` (a partition). Each is
    wrapped by the barrier (`Barrier.EncryptFor`,
    `internal/vault/barrier.go:294-311`) under
    `data/vault/.backup-keys/<id>.json`. They never go into an archive or
    to an archiver.
  - **One subkey per archive object.** The main archive loses its data,
    which moves to a `.data.<TileKey>` archive, like today's deployment
    archives. Partition archives are new objects.
  - **Format:** `XBINSEAL` magic, a cleartext header naming the subkey id,
    then chunked AES-256-GCM (STREAM) over today's tar. `backup.Open` sniffs
    the magic, so old plaintext tars restore unchanged.
  - **Erase:** delete the subkey and keep a tombstone, then ask the
    archiver (optional `POST /archive/erase`) to drop the dead versions.
  - **DR:** `bx backup keys export` bundles the wrapped subkeys with the
    passphrase-wrapped barrier file; import re-wraps them under the new
    workspace's DEK.
  - Plaintext-vault workspaces keep today's unencrypted archives.

**PD-26 — Orphans and retention.** DEFAULT (revised for PD-44).
- Rec: orphaned only on recorded events (user deleted, id recreated, tile
  removed); swept 30 days later (number: owner may change). "Dormant by
  mode" no longer exists:
  - a pending tile keeps everything until a manager decides;
  - a switch deletes at once;
  - "keep" changes nothing;
  - a missing index never orphans.

  `purge` deletes orphaned partitions only. Partition subkeys are erased at
  the sweep or purge (PD-56).

**PD-27 — Notifications from a partition.** DEFAULT.
- Rec: a user partition notifies only its person; a partition's tile report
  is its person's only.

**PD-46 — Who sees partition metadata.** DEFAULT (S19, S10, C18).
- Rec:
  - the person sees their own rows, ledger, consents and personal binds;
  - the tile's writers and managers see tile totals and, for the agent,
    per-person LLM usage totals;
  - admins see per-person metadata rows and personal-bind rows;
  - nobody sees partitions' vault key names, log lines, mail or content.

**PD-47 — Guaranteed surfaces.** DEFAULT (C23, S1).
- Rec: safety never depends on scaffold updates. These ship with xbind:
  - `bx` and `bx doctor`;
  - `/alerts` (incl. `partition-switch`) and push notifications;
  - the in-frame switch page;
  - `/xbin/partitions`: consents, log shares, ledger, trust panel, notices,
    switch decisions, personal binds and credential confirmations.

  The admin Partitions section, the Policies tab, the shell marker and the
  card overlay come with `bx builtin update`.

**PD-48 — Volume handling at N people.** DEFAULT (C6).
- Rec: per-(dirKey, name) locks in resenc; `-scryptn 10` for new partition
  volumes; refcounted idle unmount after 60 min.

**PD-53 — The partitioned marker.** DEFAULT (new; owner picks the design).
- Rec: design A of 06 §12.2, a half-split disc in a calm teal. The window
  head's runtime dot becomes the disc on partitioned tiles, and the sidebar
  shows the disc at the row's right end. Tooltip: "Partitioned: each person
  here has their own data". B (split window icon) and C (person-and-divider
  glyph) are the alternatives. Plain green was rejected: green is the status
  colour for "ok" (`workspace-template/shell/shell-css.js:426`) and node's
  runtime colour (`shell-kit.js:19`).

## D. Builtin tiles

**PD-30 — Agent resource split.** DEFAULT (S8, S9, C3, C9, S18; rev. 3 rationale).
- Rec:
  - `db`/`files`/`events` become partitioned, with global's at today's
    keys;
  - new `conf` (kv, `"shared": "read"`) and `team` (sqlite,
    `"shared": true`);
  - no person's partition mounts global's `db`;
  - every mode migrates only its own `db`.

  With PD-34 no pre-partition data survives a switch, so the split is about
  the steady state, not a migration.

**PD-31 — Shared conversations: where they run, how the UI reaches them.** DEFAULT (follows PD-16).
- Rec: global runs them from its `db`, and the UI reaches them with
  attributed F5. Partition ids start at 2^40, so the router picks a
  conversation's home from its id alone and `#c=<id>` below 2^40 always
  means global. Sandbox labels gain `xbin.agent/home`.

**PD-32 — Non-secure shared conversations.** **DECIDED (owner, 2026-09-29).**
- Ruling: hosted by the person's partition, as recommended:
  - one host per conversation, transcript in `team`;
  - the host engine drives only its own `hosted` table;
  - a change of audience pauses the conversation until the host
    re-confirms;
  - share links are refused;
  - plus the extra **"share a copy"** action (08 §4).

**PD-33 — "A big warning every time such a chat is started".** DEFAULT.
- Rec: a full modal at creation, at every added resource, and every time any
  member opens the conversation into a page session; it lists who can read;
  no "don't show again"; a persistent ⚠ chip.

**PD-34 — Existing agent chats.** **DECIDED (owner, 2026-09-29).**
- Ruling: **follow the mode rule**. An existing agent instance that switches
  to partitioned deletes its data after the confirmation. There is **no
  migration path**. Instances may simply stay unpartitioned ("Keep the
  current mode", or never adding the line).
- The alert names what goes: all conversations, memory and schedules
  (08 §10).
- Removes rev. 2's legacy-conversation work:
  - the "Before <date>" list and its notice;
  - "Delete my earlier private conversations" with `secure_delete`;
  - the v2 "move into my space";
  - the sandbox-manager `rehome` reservation.

**PD-35 — Template default for new agent instances.** **DECIDED (owner, 2026-09-29).**
- Ruling: new agent-template instances are **partitioned by default**. The
  owner accepts the overhead because idle backends stop.
- Implementation:
  - `template.partition: ["user","global"]` (PD-52), with an opt-out box at
    instantiation, which is off without `--isolate`;
  - caps and idle-stop are checked for this load (03 §A.5, PD-18);
  - viewers signing in with the owner token get the global view (08 §11).

**PD-36 — LLM concurrency across partitions.** DEFAULT (C18).
- Rec: a tile-wide cap, on by default at today's limit (4), as a flock
  semaphore beside `team`; the per-process gate stays; llm-gw keeps
  per-(From, Partition-Id) counters.

**PD-37 — Channels.** DEFAULT.
- Rec:
  - adapters reach global, and routing tables live in global's `db`;
  - linked DMs are mailed to the person's partition;
  - replies are mailed back referencing the handoff, and global takes the
    destination from its own record.

**PD-38 — Triggers and schedules.** DEFAULT (S10).
- Rec:
  - team ones live in global, and private configs in the person's
    partition;
  - the trigger registry lives in global;
  - private push triggers need a non-empty, non-overlapping match;
  - events are handed off by mail.

**PD-39 — coding-sandbox.** DEFAULT (S14, C7, C11, C12, S19; rev. 3: no `rehome`).
- Rec:
  - it stays non-partitioned;
  - consumer = (From, partition id), with `""` ≡ global, and the person is
    verified by the partition;
  - global-home records are visible to the same consumer's partitions when
    `personOK` passes;
  - operator names are redacted;
  - `partitions` in `hello.caps`, and the agent degrades with old managers.

## E. Accepted risks (why each is acceptable)

| # | Risk | Why accepted |
|---|---|---|
| AR-1 | An older xbind runs a partitioned tile as one instance on global's data; new writes merge there (PD-06, decided) | no partition data is exposed; `RequirePartition` fails closed; the agent's legacy mode is today's model |
| AR-2 | Code writers and writers of bound providers can read every partition (PD-23, decided) | the code runs in every partition by design; visible (trust panel, deploy log); "reviewed code only" exists |
| AR-3 | An admin can take over a person's account by resetting credentials (PD-07, decided) | "owner is admin"; audited and notified; `credentialResetConfirm` exists (off by default) |
| AR-4 | Host root and holders of the vault unseal key read everything; the host disk may keep deleted key files until overwritten (CoW) | hygiene guarantee, stated as a non-goal; crypto-erase covers archives wherever they are |
| AR-5 | *(withdrawn in rev. 3: no pre-partition data survives a switch — PD-34)* | — |
| AR-6 | Anything written to a `"shared": true` resource or mailed to global is readable by every partition's code / by global | the contract of shared data, stated in docs |
| AR-7 | Metadata stays visible (who uses a tile, sizes, crash data, egress totals, personal-bind rows to admins) | governance needs it; no content, vault names, logs or mail |
| AR-8 | Linked-DM files > 1 MiB and replies pass through global's storage until acked | staged content is deleted on ack; documented |
| AR-9 | Files written to the tile directory from a partition's terminal are shared code | sessions start in `$HOME`, the chip warns, doctor lists untracked files |
| AR-10 | Caps bound the number of people with an agent tab visible at once — more often now the agent is partitioned by default (PD-35) | streams close when hidden; 10-min idle stop; caps derive from memory and are admin-settable; the 503 text is clear |
| AR-11 | Shared/foreign bus deliveries reach a user partition only while it runs | cold-starting every subscriber would thrash caps; mail is the durable path |
| AR-12 | No user partitions in non-primary deployments; switching a partitioned tile's primary is refused (PD-17) | promote/rollback cover D127 flows; keys keep `<dep>` |
| AR-13 | Partitioned `vm` tiles and offloading a partitioned tile aren't supported in v1 | VM budget is workspace-wide; offload would strand people's data |
| AR-14 | Person layers, agent-session history and mail aren't backed up | environment and transient data; documented |
| AR-15 | A person's first linked DM waits until they open the agent once | mail never cold-starts a never-run partition |
| AR-16 | `homes/<id>` of people who never held a partition is still inherited by a recreated id | pre-existing behaviour unrelated to partitions |
| AR-17 | Managers see private triggers' registry metadata | today's D83 oversight of automations |
| AR-18 | Disaster recovery of sealed archives needs the exported key bundle and its passphrase (PD-56) | the point of sealing; admins are nudged to export (11 §5) |
| AR-19 | With consent off (the default, PD-13), an admin-approved grant lets partitioned Z's code read every person's X data that they can read | the owner's default; the approval warning, the ledger and the Policies switch exist |
| AR-20 | Archives made before sealing, and key bundles exported before an erase, still hold erased data | plaintext archives can't be erased by key; the switch confirmation counts them; docs say to delete them and re-export bundles |
| AR-21 | A mode switch doesn't delete provider-held records (sandboxes at a manager) | xbind can't reach into a provider; the confirmation names bound providers |

## F. Contradictions resolved (summary)

| Tension | Resolution |
|---|---|
| "frontend routes to the viewer's partition" vs the agent's shared UI | PD-16 (decided): default routing unchanged; attributed F5 to the own global instance |
| "global talks to users via shared resources" vs handing one person a DM | PD-15: partition mail |
| "outgoing calls land in the same user's partition" vs a tile reading everyone's data through that edge | PD-13 (decided): grant + read; per-person consent as a workspace policy |
| "binds to non-partitioned tiles are allowed" vs every partition's trust base widening | PD-16/PD-54: global binds need today's bind authority (admin or org admin on both ends); people add their own tiles with personal binds |
| "owner is admin" vs "admins shouldn't have easy access" | PD-07 (decided): governance kept, read paths removed, takeover audited; confirm-reset policy |
| the `partition` flag is sandbox-writable (D118) | PD-44 (decided): it sets the mode only on an empty tile; afterwards it only requests a switch that a manager confirms (wiping) or declines |
| "turning partitions on keeps existing data" (rev. 2) vs the owner's switch rule | PD-44/PD-34 (decided): a switch deletes all the tile's data; no migration |
| data is per scope, the flag per tile | PD-03 |
| "partition" already used by D115 | PD-40 |
| "option B: shared chats on shared resources" vs routing rows every partition can write | PD-30/PD-45 |

## G. Review findings → where they landed

| Id | Finding (short) | Resolution |
|---|---|---|
| S1 | cross-tile partition edge needs no consent; mail bootstraps it | PD-13 (decided): consent is a workspace policy (off by default, AR-19); approval warning, ledger, mail never starts a never-run partition |
| S2 | admins drive/read agent sessions via `MayDrive` | PD-09 |
| S3 | F5 from a partition reaches global as the tile itself | PD-16 (decided): attribution |
| S4 | mail forgeable; `outbox/add` to any addr | PD-15 |
| S5 | user partitions get the primary's network wiring | PD-28 |
| S6 | account takeover missing from G2 | PD-07 (decided): residual named, audit + notice, confirm-reset policy |
| S7 | trust base understated | PD-23 (decided); global binds need today's bind authority, listed in the trust panel (PD-54) |
| S8 | routing from rows every partition can write | PD-45, PD-30 |
| S9 | legacy data mounted into every partition | PD-30; PD-34 (decided): no legacy data survives a switch |
| S10 | private triggers capture webhooks | PD-38 |
| S11 | hosted conversation audience widens silently | PD-32 (decided) |
| S12 | route classes miss routes; runner events leak | PD-29 |
| S13 | a live partition token becomes main's after flag off | token identity includes the partition; synchronous revocation before any switch |
| S14 | `""` vs `global` sandbox consumer | `""` ≡ `global` (PD-39) |
| S15 | backup manifest lists partition rows; layers outside backups | manifest filter; PD-22; partition archives sealed (PD-56) |
| S16 | tile directory is shared code | 06 §1; AR-9 |
| S17 | recreated id inherits homes/history | PD-43 |
| S18 | shared `events` bus leaks activity | PD-30 |
| S19 | managers see peers' activity | PD-46 |
| S20 | mail wakes everyone; noisy neighbour | PD-18 |
| C1 | manifest-only flag + auto-sweep deletes data | PD-44 (decided): the manifest acts alone only on empty tiles; with data a manager confirms (wipe) or keeps; no sweep ever follows a flag |
| C2 | side-index loss re-mints everyone | PD-43; restore rules (11 §4) |
| C3 | every partition migrates shared `db` | PD-30 |
| C4 | streams pin partitions; fixed caps | PD-18 |
| C5 | background fan-out unbounded | PD-18 |
| C6 | resenc mutex/scrypt | PD-48 |
| C7 | agent page can't open shared sandboxes' terminals | PD-39 |
| C8 | `g:` prefix breaks old links | PD-31 |
| C9 | shared `events` | = S18 |
| C10 | `/tile-report` unclassified | = S12 |
| C11 | wire key reusable by a recreated user | PD-14/PD-43 |
| C12 | agent upgrade ahead of managers | PD-39; switch lists managers without caps (01 §2.5) |
| C13 | deployments × partitions | PD-17 |
| C14 | `vm` partitions | PD-28 |
| C15 | nobody can debug a partition | PD-24 |
| C16 | offload ignores partitions | PD-28 |
| C17 | layers/history lifecycle | PD-22 |
| C18 | LLM concurrency ×N | PD-36 |
| C19 | template default on; owner-token viewers | PD-35 (decided): default on, opt-out, isolation check, caps coverage, global view for the owner token |
| C20 | account takeover | = S6 |
| C21 | over-engineering | bus unchanged, per-partition ceilings, no `requires`; the PD-34 move is dropped entirely (decided) |
| C22 | `?partition=` 400 everywhere | 400 on partitioned tiles only |
| C23 | scaffold not updated | PD-47 |

## H. Open questions raised by the rulings — answered (owner, 2026-09-29)

- **H1 — adding or removing `global` on a partitioned tile: non-destructive.**
  Adding it creates the empty global instance; removing it (after the same
  confirmation) deletes only global's data and the shared resources —
  people's partitions stay. Only user ↔ unpartitioned is a wiping switch
  (01 §2.6).
- **H2 — marker: A**, the teal half-split disc (06 §12.2).
- **H3 — sealing for existing workspaces: seal at once**, with a persistent
  admin alert until the first key-bundle export, plus the migration note
  (11).
- **Grant rule (PD-54): today's bind authority**, not a new admin-only rule
  — see PD-54.

The questions as they were raised:



Three, each with the plan's default in place until the owner answers.

1. **Adding or removing `global` on a partitioned tile.** Under the literal
   rule this is a switch, and it deletes everything, people's partitions
   included. It could be non-destructive:
   - adding `global` finds today's keys empty;
   - removing it would only need global's own data gone.

   *Default: literal (wipe all).*
2. **The marker design (PD-53).** A, B or C, and the hue. *Default: A in teal.*
3. **Sealing for existing workspaces (PD-56).** After an upgrade, disaster
   recovery onto a new machine needs an exported key bundle. The options
   are sealing at once with a persistent admin alert until the first export,
   or letting existing workspaces opt in for one release. *Default: seal at
   once with the alert (11 §7), which ships a migration note.*
