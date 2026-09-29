# 06 — Terminals, agent sessions and operations

Requirement: "Tile terminals (and agent sessions in them) reach only their
user's partition." This file also covers everything an operator or a person
touches:
- logs and status;
- the partitions API, consents and personal binds;
- people's lifecycle and credential resets;
- backups (pointer to 11);
- `bx`;
- the person page, the admin surfaces and the shell marker.

## Current behaviour

- **Terminal identity.** A tile terminal's shell acts as the tile (terminal
  token, plans/terminal-tokens.md), attributed to the person (`termID`,
  `internal/auth/auth.go:263`; `terminalPrincipal` `:505`). Its target
  deployment is chosen at session start (`internal/term/target.go`, D127p;
  `MintTerminalTarget` `internal/auth/deployment.go:104`).
- **Binds** (`scopedBinds`, `internal/term/binds.go:36`):
  - the workspace read-only (every readable tile's source, `:40-46`);
  - the tile's directory read-write (`:66-67`);
  - the person's `$HOME` read-write;
  - `.xbin/`, `data/` and other people's homes masked.

  Data is reachable only through the API.
- **Terminal layer.** The persistent overlay layer is **per tile**,
  `termKey(rel)` (`internal/term/term.go:493-499`). One live session claims
  it at a time; others are ephemeral (`:594-620`). A VM terminal's disk lives
  inside the layer. Backups include only `.xbin/term/<CompKey>`
  (`internal/broker/backup.go:60-62,131`).
- **Admins over others' sessions.** Admins may:
  - reattach to anyone's live session (`mayReattach`,
    `internal/term/sessions.go:132-143`);
  - drive anyone's agent session through `MayDrive`
    (`internal/term/agent.go:210-227`, admin pass at `:217`) and
    `drive`/`driveOther` (`internal/server/agentapi.go:152-182`);
  - receive every `session`/`term` event (`termEventFor`,
    `internal/server/termsessions.go:81-84`);
  - list anyone's sessions (`?user=`, `termsessions.go:101-116`).

  Agent-session history is per person (`data/agent-history/<user>/<tile>/`,
  `internal/term/history.go:5-13`), and homes are keyed by user id
  (`HomeKey`, `internal/term/homes.go:25`).
- **`bx` and logs.** `bx` in a terminal reads
  `$XBIN_COMPONENT`/`$XBIN_DEPLOYMENT` (`cmd/bx/status.go:49-50`). Logs come
  from `logOpener(comp, dep)` (`internal/obs/deploylogs.go:92`).
- **Notify.** A backend may notify any reader of the tile; frames and
  terminals only their own person (`notifier`, `internal/push/api.go:287`).
- **Users and credentials.** User delete (`internal/users/users.go:615`)
  keeps no tombstone. Admins, and org admins for non-admin members (D38),
  can re-mint a sign-in link (`apiUsersInvite`,
  `internal/broker/usersapi.go:313-345`) or upsert a person with a new
  password or SSO email (`:216-224`, `internal/users/users.go:493-523`).
  The audit trail is slog only (`internal/server/server.go:584-601`).
- **Scaffold.** The shell and admin tile are workspace scaffold that an
  xbind upgrade doesn't update (docs/compat.md rule 4). `web/` core elements
  and `/docs/` are embedded in the binary (`assets.go`).
- **Admin tile tabs.** The admin tile's tabs are grouped in `static GROUPS`
  (`workspace-template/tiles/admin/admin.js:98-127`). The workspace group
  (`:126`) holds `branding` and `xbin app`. Adding a tab means an element
  under `tabs/`, a `GROUPS` entry, a `render()` arm (`:247-270`) and the
  `adminTabs` harness pass (docs/maintenance.md:520).
- **Where a marker would go.** A sidebar row (`_itemTemplate`,
  `workspace-template/shell/bx-side.js:287-318`) shows the drawn app icon,
  the name, `prBadge`, the status dot, ⚠, "hidden" and ⋯. Its runtime dot
  and runtime label went in 0897bb0b. The window head (`_cardTemplate`,
  `workspace-template/shell/bx-canvas.js:324-355`) still shows the runtime
  dot (`:334`, `RUNTIME_COLOR`, `shell-kit.js:15-21`), then the path, the
  `+dep` chip, `prBadge`, the ⇈ badge and the buttons. Status colours: ok
  green, info blue, warn amber, error red (`shell-css.js:424-436`).

## The change

### 1. Terminal and agent-session identity

- No token changes: the session's person decides its partition
  (`addressedPartition`, 02 §3). Every API call from the shell reaches
  `user:<person>`'s instance, data, vault, logs, mail and registrations.
- Terminal env gains `XBIN_PARTITION=user:<id>` on partitioned tiles, so
  `bx status` and `bx logs` default to it. `bx` never sends the partition
  as a parameter: the credential decides.
- **Working directory (S16):** on a partitioned tile, shells and agent
  sessions start in `$HOME`. The terminal chip reads "partition: yours · the
  tile directory is shared code", and `bx doctor` lists untracked files in
  partitioned tiles' directories.
- **Bootstrap and root terminals** (no person): the API reaches `global`
  when the tile declares it; otherwise the session opens with the API off
  (PD-10).
- Agent sessions (D74/D75) inherit all of this.

### 2. Per-person layer and history (PD-22)

- **Layer:** a person's persistent layer on a partitioned tile lives at
  `.xbin/term-part/<TileKey>/<pkey>`, and their VM disk quota is per
  person.
- **History:** agent sessions on a partitioned tile write to
  `data/agent-history/.partitions/<pkey>/<TileKey>/`. The history API merges
  it with the person's own.
- **Lifecycle:**
  - both follow their partition: removed by the orphan sweep, the
    user-delete hook, purge and **a mode switch** (01 §2.6), using the
    confined remover (D78);
  - neither is backed up.

### 3. Admins and other people's sessions (PD-09, S2)

On a **partitioned tile**, for a session whose person isn't the caller:
- `mayReattach` refuses admins;
- `MayDrive` drops the admin pass (`:217`) when the session's `Cwd` is a
  partitioned tile. Admins get 403 on the events, log and diff reads, on
  `POST …/prompt`, and on permission, elicitation and option answers
  (`agentapi.go:152-182`). Without this an admin could prompt alice's
  agent, whose sandbox holds her terminal token, to dump her partition;
- kill stays allowed;
- `termEventFor` drops the admin pass;
- `?user=` listings leave out session names.

Unpartitioned tiles: unchanged.

### 4. The code tree and providers stay shared (PD-23, decided)

People who can change the code run it in every partition. The trust base
of a partitioned tile is:
- write level on the tile or any registered ancestor (`writesCode`,
  `internal/server/static.go:606-621`), plus terminal level;
- the writers of **every non-partitioned provider bound to it** (global
  binds, 05 §3; for personal binds that is the person's own tile);
- admins.

Mitigations, all decided:
- **Warnings** on the tile page, in `bx doctor` and on `/alerts`: live
  reload is on and non-admins hold write level, on the tile or on a bound
  provider.
- **"Reviewed code only"**, a per-tile admin switch. The primary must be
  protected (D127m), and so must every provider bound to the tile.
- **The trust panel** (§12): code writers, admins, bound providers and
  their last code changes.

### 5. Logs, status, backends (PD-24, C15, S12)

- A user partition's log is `.xbin/partition/<TileKey>/<dep>/<pkey>/backend.log`.
- `GET /api/xbin/logs?tile=` answers the **caller's own** partition's log
  at any level; `?partition=` → 400 on partitioned tiles. Admins and
  managers read a person's log only while that person shares it (`POST
  /api/xbin/partitions/share-log {tile, days ≤ 14}`, PersonOnly).
- `crashLoopError` names the partition, never the log path.
- `tile-status` and `tile-report` are per partition.
- `GET /api/xbin/backends` (admin) lists user-partition instances as rows
  with `partition` and metadata: running, since, memory, `lastExit` (code,
  signal, OOM), `restarts`, `crashLoop`, `lastStartMs`, and the `lastError`
  class.

### 6. The partitions API

```
GET  /api/xbin/partitions?tile=<t>
  → {"features": ["partitions/1", "partition-mail/1", "global-address/1",
                  "consents/1", "personal-binds/1", "mode-switch/1"],
     "tile": "<t>", "state": "partitioned|unpartitioned|pending|invalid",
     "spec": {"user": true, "global": true},
     "request": {"spec": {...}, "since": "…", "declined": false} | null,
     "policies": {"partitionConsent": false},
     "totals": {"people": 12, "running": 3, "bytes": 123456, "cron": 9, "bus": 4},
     "limits": {"maxRunning": 6, "partitionBytes": …},
     "partitions": [ … per visibility, below … ]}
```

Row visibility (S19, PD-46):
- **the person** sees their own row:
  - state, running, lastStarted, bytes;
  - registration counts, `missedTicks`, `dormantDrops`, mail counts;
  - their ledger (§6.1), their consents (when the policy is on) and their
    personal binds on the tile;
  - log shares and the trust-panel data.
- **the tile's writers and managers** see `totals` only;
- **admins** see per-person metadata rows and personal-bind rows. They
  never see contents, vault key names, log lines or mail.

Acts:
```
POST /api/xbin/partitions/mode      {tile, act: "keep"|"switch", from, to, confirm?}   // tile manager (01 §2.5)
POST /api/xbin/partitions/limits    {tile?, maxRunning?, partitionBytes?}               // admin; managers may lower their tile's
POST /api/xbin/partitions/stop      {tile, partition}                                    // the person (own) or manager/admin (any)
POST /api/xbin/partitions/reset     {tile, partition, confirm: "<tile> <partition>"}     // the person (own) or admin (any, audited, notified); erases the partition's backup subkey
POST /api/xbin/partitions/purge     {tile?, partition?}                                  // admin; orphaned partitions only
GET|POST|DELETE /api/xbin/partitions/consents       {from, to}                         // PersonOnly; only meaningful with partitionConsent on (05 §2)
GET|POST|DELETE /api/xbin/partitions/binds          {requester, slot, provider}         // personal binds (05 §3)
POST|DELETE /api/xbin/partitions/share-log          {tile, days}                        // PersonOnly
POST /api/xbin/partitions/credential-confirm        {id, allow: bool}                   // PersonOnly (§9)
GET|POST /api/xbin/partitions/mail, POST …/mail/ack                                     // 04 §3
```
- `reset` stops the instance, wipes its namespace, vault, registrations and
  mail, and erases the partition's backup subkey (11 §3). It needs a typed
  `confirm`.
- `purge` deletes orphaned partitions (user deleted, tile removed). No
  partition is ever dormant because of the mode (PD-26).
- 404 on an older xbind means no partitions; new clients read `features`.

#### 6.1 The egress ledger (S1, S10)

xbind counts, per user partition and per day, with no content ever
recorded:
- cross-tile partition calls and binds;
- calls through global and personal binds to non-partitioned providers,
  by target;
- bus subscriptions by source;
- private triggers' sources.

The ledger lives in `data/partitions/<TileKey>/<dep>/<pkey>/ledger.json`
(90 days). The person sees their rows, admins see per-target totals per
person, and the tile's writers and managers see per-target totals for the
tile. It runs in both consent settings.

The `partitions` event:
- ops `state`, `consent-needed` and `notice` go to the person's own
  sockets;
- op `mode` (`{state, spec, request}`) goes to the tile's readers, and the
  decision fields to its managers.

### 7. `bx`

- `bx partition` subcommands:
  - `ls [<tile>]`;
  - `switch <tile> --yes`, which prints the wipe summary and asks for the
    tile path;
  - `keep <tile>`;
  - `stop <tile> [--user <id>]`;
  - `reset <tile> [--user <id>] --yes`;
  - `purge`;
  - `limits <tile> …`;
  - `consent <from> <to> [--revoke]`;
  - `share-log <tile> [--days n]`;
  - `mail ls|ack` (in a partition's terminal).

  They exit 6 against an older xbind.
- `bx bind --personal <requester> <slot>=<provider>` and `bx unbind
  --personal …`.
- `bx policies [set partition-consent|credential-reset-confirm on|off]`
  (PD-55).
- `bx template new … --no-partition` (01 §2.7).
- `bx backup keys export|import` and `bx backup erase` (11).
- `bx status` prints `partition: user:alice`.
- `bx doctor`, **the guaranteed operator surface** (C23), reports:
  - pending, declined and invalid tiles;
  - partitioned tiles without isolation;
  - live reload plus writers on the tile and its bound providers;
  - global binds on partitioned tiles, for review;
  - non-partitioned requesters bound without global;
  - sandbox managers whose `hello.caps` lack `partitions`;
  - untracked files in partitioned tiles' directories;
  - orphaned partitions;
  - caps hit recently;
  - backup keys that aren't exported, and plaintext-vault backups.

### 8. Notifications (PD-27)

`notifier` (`internal/push/api.go:287`): a user-partition instance acts for
its person, so it can notify only them. The global instance keeps today's
"any reader" rule.

### 9. People's lifecycle and credentials (PD-20, PD-26, PD-43, PD-07 decided)

- **Deleted:** revoke tokens, stop every partition instance, and mark the
  person's partitions `orphaned`. Personal binds go with the uid. The sweep
  after the retention (or an admin purge) erases the partitions' backup
  subkeys (11 §3). For a partition holder, `homes/<id>` and
  `data/agent-history/<id>` move to `data/orphans/<id>-<uid8>/` (S17).
- **Disabled, or lost read on the tile:** `dormant` (no starts or
  deliveries); regaining access resumes.
- **Credential resets (S6):** an admin or org admin mints a sign-in link
  for, sets a password on, or binds an SSO email to a person who holds
  partitions (`usersapi.go:313-345`, `:216-224`). Two cases:
  - **Policy off** (the default, hygiene (a)): it takes effect as today,
    with an audit line, a push to the person ("a sign-in link for your
    account was created by `<admin>` at `<time>`"), a `notice` event, a
    banner on `/xbin/partitions` and a ledger line.
  - **Policy `credentialResetConfirm` on** (a+, PD-55): the new credential
    is minted **held**:
    - an invite link answers "waiting for `<person>` to confirm" when
      redeemed;
    - a new password or SSO email is stored pending, and the old one keeps
      working;
    - the person gets the push and notice with **Allow** / **Refuse**
      (`POST /partitions/credential-confirm`, PersonOnly, from any of their
      signed-in sessions, apps or devices);
    - Allow activates it and Refuse revokes it;
    - with no answer, it activates **24 h after the notice was sent**, which
      covers the person who lost every device;
    - resetting a person with no partitions is unaffected.

### 10. Tile lifecycle

- **Offload** of a partitioned tile is refused in v1 (409, C16).
- **Removal** orphans its partitions (`tile-removed`), sweeps them after
  the retention (erasing their subkeys) and then removes the mode record.
  The tile's `tile:`/`ns:` subkeys stay, so a removed tile's main archives
  remain restorable, as today (11 §2).
- **Mode switch:** 01 §2.

### 11. Backups (PD-25, decided)

These are specified in [11-backup-encryption.md](11-backup-encryption.md).
In short:
- every archive is sealed under a random subkey, and the subkeys are
  wrapped by the vault;
- partitions get their own archives (`.partitions.<TileKey>.<dep>.<pkey>`)
  under a per-partition subkey;
- deleting a subkey (user sweep, partition reset or purge, mode switch)
  crypto-erases that data in every backup;
- old plaintext archives still restore;
- a main archive's manifest lists global's cron and bus rows only
  (`backup.go:92-100`, S15);
- person layers, agent-session history and mail aren't backed up.

### 12. Surfaces for people and operators (C23)

#### 12.1 Guaranteed (updated with xbind)

- `bx` and `bx doctor` (§7).
- `/alerts`: `partition-switch`, trust warnings and `backup-keys`.
- Push notifications: switch requests to managers, consent prompts,
  credential confirmations, wipe notices.
- **The in-frame switch page** (01 §2.4).
- **`/xbin/partitions`**, a small xbind-served page embedded like `/docs/`
  (`assets.go`). It opens top-level with the person's session and offers:
  - their partitions, consents (when the policy is on), personal binds,
    log shares, ledger, trust panel and notices;
  - credential confirmations;
  - **for tile managers**, pending switch requests with **Keep the current
    mode** and **Switch and delete all data…**, which requires a typed
    confirmation and shows the counts and the keep list (01 §2.6).

  It is a static page calling the API; there is no HTML transform.

#### 12.2 The partitioned marker (PD-53; owner picks)

Requirements:
- small and status-like, not a button: no border, no hover state,
  `cursor: default`, `role="img"`;
- tooltip: **"Partitioned: each person here has their own data"**;
- shown on the tile's window head and its sidebar row.

The shell reads it from the components row's `partition.state`. The
sidebar lost its runtime dot and runtime label in 0897bb0b, so the row has
room at its right end, where the label used to sit.

| | Design | Window head | Sidebar row | For | Against |
|---|---|---|---|---|---|
| **A (rec.)** | **Half-split disc**: an 8 px drawn SVG ring with the left half filled, in a calm **teal** (`--bx-part`, fallback `#3fb5a3`) | replaces the runtime dot (`bx-canvas.js:334`) on partitioned tiles, same size and place | before ⋯, where `.rt` was | the owner's "dot, but another colour" idea; the split shape reads "divided"; no new element in the head; meaning carried by shape, not hue alone | teal sits near green for some viewers, so the shape does the work. Plain green was rejected: it is status "ok" (`shell-css.js:426`) and node's runtime colour (`shell-kit.js:19`) |
| B | **Split window icon**: the row's drawn app icon (`bx-side.js:27-29`) gains a vertical divider under its title bar (two panes); no new hue | the same 11 px glyph after the path | the row's own icon | zero added width; the quietest | easy to miss; the icon turns accent while open, so no hue can be added |
| C | **Person + divider glyph**: two 10 px head-and-shoulders silhouettes with a thin bar between, muted grey | after the path and `+dep` chip | before ⋯ | self-explanatory | busy at 10 px; reads like a "people/share" button |

A pending switch doesn't change the marker: the grey-out and the `/alerts`
banner carry it. The iOS app shows the same marker on tile rows when it
next updates; this is not required for v1.

#### 12.3 Scaffold (arrives with `bx builtin update`)

- **Shell:**
  - the marker (§12.2);
  - on a pending tile's card, a dimmed overlay over the frame with the
    alert text, and for managers **Keep the current mode** / **Switch and
    delete all data…** (which calls `POST /partitions/mode` after the typed
    confirmation);
  - the partition chip;
  - consent prompts (only with the policy on).
- **Admin tile, workspace → policies** (new tab, PD-55):
  `workspace-template/tiles/admin/tabs/policies.js`, a `{ id: 'policies',
  label: 'policies' }` entry in the workspace group (`admin.js:126`), a
  `render()` arm and the `adminTabs` harness pass. It follows the xbin-app
  tab's toggle pattern (`tabs/nativeapp.js:33-64`: GET, PUT, re-read on the
  `policies` event). Two switches, each with a one-line consequence:
  - **"Ask each person before another partitioned tile uses their data"**
    (`partitionConsent`, off). Turning it on first shows ledger totals of
    the edges that would start asking.
  - **"Credential resets wait for the person"** (`credentialResetConfirm`,
    off). "A sign-in link, password or SSO email set by an admin for
    someone who holds partitions works only after they confirm, or 24 h
    after they're notified."

  The D20 grant ceiling ("workspace policy") stays in the orgs tab
  (`workspace-template/tiles/admin/tabs/orgs.js:300-302`).
- **Admin tile, runtime → partitions** (a section under runtime):
  - per tile: the mode, any request or decline, Switch/Keep, and the
    "reviewed code only" switch;
  - limits and per-person metadata rows;
  - stop, reset and purge;
  - personal-bind rows.

  The admin runtime → sandboxes view labels partition instances.
- **Admin tile, Backup tab:** export keys, last export, and "erased since
  export" (11 §5).
- The web shell and the app treat a partitioned tile as one tile (one
  card); opening it opens the viewer's partition.

## Wire (for docs/protocol.md, docs/bx.md)

- `GET /api/xbin/partitions`, `POST /api/xbin/partitions/{mode,limits,stop,
  reset,purge}`, `/partitions/consents`, `/partitions/binds`,
  `/partitions/share-log`, `/partitions/credential-confirm`,
  `/partitions/mail…`, the `partitions` event and the `/xbin/partitions`
  page.
- `GET/PUT /api/xbin/workspace-policies` and the `policies` event (05 §2).
- `XBIN_PARTITION` in terminals; logs/tile-status/tile-report semantics;
  `?partition=` 400 on partitioned tiles; the reattach/drive refusal texts;
  `?user=` listings without names; offload 409.
- Held invites: the redemption text and the 24 h rule.
- `bx partition …`, `bx bind --personal`, `bx policies`, `bx backup keys`,
  and exit codes.

## Tests

- **term:**
  - env golden for partitioned vs not;
  - sessions start in `$HOME`;
  - the layer path is per person;
  - the bootstrap terminal has the API off without global.
- **term/server:**
  - on a partitioned tile an admin gets 403 on reattach and every drive
    route, can kill, receives no events, and lists sessions without names;
  - unpartitioned tiles are as today (golden).
- **server:**
  - logs, tile-status and tile-report per partition; share-log;
  - `GET /partitions` row visibility for the person, writer, manager and
    admin;
  - reset needs a confirmation and erases the subkey;
  - purge only removes orphaned partitions;
  - PersonOnly routes refuse tile principals.
- **users hooks:**
  - delete → tokens revoked, partitions orphaned, homes/history moved for a
    partition holder only;
  - recreate → a new uid, nothing inherited;
  - disable → cron stops;
  - credential reset, policy off → audit, push, notice;
  - policy on → the link is held (redeeming answers "waiting"), Allow
    activates, Refuse revokes, no answer activates at 24 h (fake clock), and
    a person without partitions is unaffected.
- **policies:** `PUT` admin only; the file survives a users-store rewrite;
  the tab toggles reflect the event.
- **ui-harness:**
  - the `/xbin/partitions` page (a switch decision, consent grant/revoke
    with the policy on, a personal bind, a credential confirmation);
  - the in-frame switch page inside an **old** shell, with the old shell's
    banner showing the alert;
  - the new shell's marker on the row and the head (design A), and the
    card overlay on a pending tile (Keep → the tile runs; Switch → typed
    confirmation);
  - the admin Policies tab (`adminTabs`) and the Partitions section;
  - an old admin scaffold and shell against a partitioned tile render and
    ignore the new fields.
