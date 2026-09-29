# 08 — The agent template (builtin-templates/agent)

The only builtin tile whose own data needs per-person partitions. Its
private conversations become each person's; team-shared conversations stay
on shared ground and run in the global instance ("option B", owner).
Revision 2 **inverted the resource split** (S8, S9, C3, C9, S18): the
existing `db`/`files`/`events` become the *partitioned* resources, with
global's copy at today's keys. Two small new resources carry the only things
that must be shared: configuration (read-only for people's partitions) and
non-secure conversations.

Revision 3 applies the owner's rulings:
- **new instances are partitioned by default** (PD-35);
- an existing instance switches only through the mode rule, which
  **deletes all its data** after a manager confirms. There is no migration
  (PD-34), and existing instances may simply stay unpartitioned;
- non-secure conversations get a **"share a copy"** action (PD-32).

## Current behaviour

- **Privacy is in code (D83).** `access.go:1-8`: "Privacy holds between people
  who USE the agent; anyone who can change or read the tile itself (write or
  terminal access, the owner token) can read every run anyway." `principal`
  (`_backend/access.go:49`) maps callers to `who{system|cron|user|element}`;
  other tiles are `whoElement` (`:65-66`) and, like write/terminal people,
  managers (`manager()`, `:73-82`); a self-call without a user is `whoSystem`
  (`:63-64`). `stamp` (`:98`) makes a person's run `private`, system/element
  runs `team`. ACL on the root run (`acl.go`).
- **Resources** (`scope.json`): `db` sqlite (the whole durable state;
  `XBIN_RES_DB` is a file path), `beat` cron (heartbeat/resume), `events` bus
  (run lifecycle events for other tiles), `files` blob (binary session
  files). Tables (`_backend/migrate.go:108-290`, `migrate_conv.go:19-36`,
  `channels.go:30-60`, `triggers.go:29-41`, …): runs (owner, visibility,
  team_role, origin, session_key…), messages, steps, memory, settings
  (tile-wide), skills (name PK, owner, lane), schedules (owner, visibility),
  repl_files, run_members, share_links, run_user_state, sessions, run_grants
  (D111), inbox, links, asks, summaries, channels, channel_peers (xbin_user
  link, D86), channel_events, outbox, triggers (`name UNIQUE` tile-wide,
  `triggers.go:31`), sandbox_jobs/creates.
- **Database:** `openDB` always runs `migrate()` — DDL, `sweepOrphanRuns`,
  `adoptLegacy`, FTS backfill, settings writes (`_backend/db.go:151-175`,
  `migrate.go:20-105`); rollback journal on the FUSE mount, `busy_timeout`
  5000.
- **Engine (D81):** actor + inbox, exclusive `flock` on `<db>.engine`
  (`owner.go:35`), epoch fencing, a self-call hold `GET /api/<self>/engine/hold`
  (`owner.go:154`) against the reaper; on exit with work (`hasWork`: running,
  queued, blocked, awaiting, sleeping, undelivered inbox —
  `engine.go:432-437`) a `resume` cron `@every 1m` (`owner.go:220-228`);
  schedule crons `sched-<id>` on `beat` (`schedule.go:185-199`). LLM gate per
  process, default 4 (`llmgate.go:3-32`). Live events are in-process
  (`events.go:58` `eventHub`); run lifecycle events are published on
  `res:<self>/events` (`handlers.go:25-31`).
- **Lists:** `GET /conversations?scope=mine|shared|team`
  (`conversations.go:118`; the Shared view `:113-153`). Addresses are
  `#c=<digits>` (`model/router.js:2-12`), used by push notification links
  (`needs_push.go:102`) and saved by the iOS app (`native.js:38-41`).
- **Channels (D86):** a person links a chat account from the bridge's frame
  (`POST /adapter/link`, `channel_links.go:51`, person from `X-XBin-User`,
  written at `:75`); a linked person's DM run is re-stamped theirs and
  private (`channels.go:480-490`); outbox streamed to the adapter.
- **Triggers (D87):** webhooks POST `/adapter/event`; every push trigger of
  the adapter whose `match` prefixes the topic fires — `""` matches all
  (`triggers.go:476-480`); anyone who may start runs creates one
  (`routes.go:120`), owned by the creator (`triggers.go:141-144`).
- **Sandboxes (D115):** calls carry asserted `Sbx-User`
  (`sandbox_client.go:31`); a team conversation's sandboxes are made team
  (`sandbox_routes.go:505` `forConversation`, `sandbox_create.go:377`),
  labelled `xbin.agent/conversation=<root id>` (`sandbox_routes.go:506-509`);
  the page dials managers directly for terminals (`sandboxes.js:29-33`).
- **Frontend streams** reconnect with a 400 ms backoff and ignore page
  visibility (`model/stream.js:63-75`).

## Target design

### 1. Manifest and resources

```jsonc
// xbin.json (template)
"template": { …, "partition": ["user", "global"] },   // new instances' mode (PD-35, 01 §2.7); stripped from instances
"partitionMail": "/mailbox",
"partitionNote": "Every conversation (private and shared), all memory and every schedule of this agent are deleted, for everyone, with its skills, triggers, channel links and settings. Sandboxes stay with their sandbox manager.",
"uses": [
  { "target": "res:apps/agent/db",     "role": "writer" },   // partitioned (global's = today's file)
  { "target": "res:apps/agent/files",  "role": "writer" },   // partitioned
  { "target": "res:apps/agent/events", "role": "writer" },   // partitioned
  { "target": "res:apps/agent/beat",   "role": "writer" },   // cron: per registrant
  { "target": "res:apps/agent/conf",   "role": "writer" },   // shared "read" (new)
  { "target": "res:apps/agent/team",   "role": "writer" },   // shared (new)
  …
]
// scope.json
"db":     { "type": "sqlite" },
"files":  { "type": "blob" },
"events": { "type": "bus" },
"beat":   { "type": "cron" },
"conf":   { "type": "kv",     "shared": "read" },   // tile-wide config global publishes
"team":   { "type": "sqlite", "shared": true }      // non-secure conversations + LLM slot locks
```

- **`db`, `files` and `events` keep their names and keys** (PD-30).
  Global's copies are at today's keys, and each person gets their own. No
  person's partition ever mounts global's `db`, so the outbox, share links
  and routing tables stay out of every person's sandbox (S9). An existing
  instance that switches mode starts empty in every copy (PD-34, §10).
- **`conf`** (kv, `"shared": "read"`): what a person's partition must read and
  must not be able to change — settings, classes (D116), model tiers, the halt
  switch, shared skills. Only global writes it (mirrored from global's `db` on
  every change); partitions read it (S8).
- **`team`** (sqlite, `"shared": true`): non-secure (hosted) conversations
  only (§4), and the tile-wide LLM slot lock files beside it (§8). Anything in
  it is readable by every partition's code — which is exactly the non-secure
  contract the warning states.
- `conf` and `team` ship in the template for every instance; in an
  unpartitioned instance `shared` is ignored and legacy mode doesn't open
  them (a template ready for opt-in; no doctor warning for `shared` in an
  unpartitioned scope, 04 §1).

### 2. Three modes, one binary

| `xbin.Partition()` | Mode | Engine on | Serves |
|---|---|---|---|
| `""` (an unpartitioned instance — every existing instance that keeps its mode — or an older xbind) | **legacy** | `db` | exactly today's behaviour, fully supported indefinitely |
| `global` | **global** | `db` at today's keys (flock `<db>.engine`, today's) | shared conversations, channels/adapters and their routing tables (`channel_peers`, handoff records), the trigger registry, team triggers/schedules, settings (mirrored to `conf`), share links, outbox, member inputs of hosted conversations |
| `user:<id>` | **user** | `db` = the person's own file (same canonical path, the partition's volume; flock `<db>.engine` there) | the person's private conversations, private skills/memory/automations, private trigger configs, the `hosted` table (§4), their sandboxes; reads `conf` |

- Mode detection is `xbin.Partition()`; the code is the same (canonical
  paths are identical in every partition, 03 §B.4).
- **Migrations (C3):** every mode migrates **only its own `db`** — global and
  legacy today's file, a user partition its own (fresh) file — so no
  partition runs migrations on global's database. A user partition's new
  `db` seeds `sqlite_sequence` for `runs` at 2^40 on creation, so its ids never
  collide with global's (§3). `team` is migrated only by global, under a
  `<team>.migrate` flock; user mode opens it with `openShared` (no migrate, no
  adopt, no sweep), checks `team`'s schema version, and when it is behind
  wakes global (F5 `GET /health`) and waits up to 30 s, else answers hosted
  features with 503 "the shared space is being upgraded".
- Legacy mode serves every instance that stays unpartitioned (PD-34:
  "Keep the current mode", or never adding the line). It is also the
  downgrade story (PD-06): an older xbind runs the new code as one instance
  on today's `db`, with today's privacy model, and people's partitions
  (`.partitions/…`) are simply not visible there.

### 3. Shared conversations (option B)

- **Live in global's `db` and run in the global instance**, which holds no
  person's partition credentials: a shared run can't reach anyone's private
  files, memory, skills, vault, sandboxes or partitions of other tiles — its
  outbound calls land in other tiles' *global* instances (05 §1). This is the
  structural form of "by preset use no partitioned resources".
- **The UI** (PD-31, with F5 — PD-16 decided): one model, two bases: `selfApi(path)`
  (the person's partition) and `selfApi(path, {partition: 'global'})` for
  shared conversations (the kit's `api` passes the option to `xbin.fetch`,
  `web/bx-kit.js:27`; the stream URL gets `xbin-partition=global`). F5 is
  attributed to the person (05 §6), so global applies D83 ACL with
  `X-XBin-User` exactly as today (sharing, members, team, share links,
  `run_user_state`).
- **Addresses (C8):** a person's partition ids start at 2^40 (safe in JS),
  so the router picks a conversation's home from its id alone. `#c=<id>`
  below 2^40 means global's `db`. There is no prefix: push links, the iOS
  app's saved state and legacy instances need no change.
- **Creating** a shared conversation (composer: "Shared with the team" /
  "with people…") → created in global (F5). **Sharing a private conversation
  later** is a copy into global — a "publish" act the person's frame performs
  with an F5 request carrying the transcript (a notice: "a copy goes to the
  shared space; your private files and sandbox stay yours unless you add
  them"); the private original stays or is deleted at the person's choice.
  Shared → private is a copy the partition fetches through F5.

### 4. Non-secure shared conversations (partitioned resources on a shared chat)

"Partitioned resources" of a person, for the agent: their private session
files, memory and skills, their sandboxes (homed at `(apps/agent, <their
partition id>)`), their partitions of other partitioned tiles (the internal
toolset acting from their partition), their partition's vault.

- **Enabling** one on a shared conversation (conversation settings → "Use my
  private …") makes it **non-secure** and **hosted** by that person: global
  moves the conversation (on the person's F5 request) from its `db` into
  `team`, with `host = user:<id>`; the host's partition records, in **its own
  `db`**, a `hosted` row `{conversation, members snapshot, visibility
  snapshot, resources, confirmed_at}`. The host's partition runs an engine
  scope on `team` **only over conversations its own `hosted` table lists**
  (lock `<team>.engine.<pkey>`, epoch fencing per host); the `host` column in
  `team` is a hint, never the authority (S8) — no other partition, and not
  global, can make alice's partition drive a conversation by writing a row.
  Global's engine never drives `team` rows.
- **Widening needs fresh consent (S11):** while hosted, adding members,
  raising visibility or changing the conversation's audience makes the host
  engine pause it ("waiting for <host> to confirm the new members") and
  prompts the host with the new list; confirming updates the snapshot;
  declining, or 7 days without an answer, drops hosting: the private
  resources are detached and the conversation stays paused until a member
  chooses "continue without <host>'s private resources" (global copies it
  back into its `db` as a plain shared conversation). **Share links are
  refused on hosted conversations** (and existing links are disabled when
  hosting starts).
- **The warning** (PD-33): a full modal, not a banner, every time such a
  conversation is **started** — created, or opened into a page session by any
  member (the composer stays locked until confirmed) — and each time a
  partitioned resource is added. It lists **who can read** the conversation:
  its members, "managers of this agent (N people)", workspace admins, and
  "anyone who can change this agent's code"; and what is exposed ("<alice>'s
  private files / sandbox / documents"). Buttons: "Start anyway" / "Open
  without sending". No "don't show again". A persistent ⚠ "not private" chip
  on the conversation header and in lists.
- **Members' inputs and views:** other members post through global (F5,
  attributed) into `team`'s input queue; global mails `user:<host>`
  `hosted/input {conversation}`; the host's engine consumes. The host's
  engine mails `global` `hosted/changed {conversation}` after each committed
  change; global re-emits it to members' streams. Live drafts reach the
  host's own browser only (v1).
- **One host per conversation** (v1); removing the last partitioned resource
  ends hosting (as declining does).
- **Share a copy (PD-32, decided): the non-hosting alternative.** From a
  shared conversation, a member picks **"Add a copy of my …"** and chooses
  items from their partition: files, a memory item, a skill, a document
  from another partitioned tile they use, or files from their sandbox.
  - Their partition fetches the items and posts them to global through F5
    (attributed), and global stores them in the conversation (its `db` and
    `files`).
  - The conversation stays an ordinary shared one run by global: no host,
    no pause, no non-secure chip.
  - A confirmation lists who can read the copy (members, managers, admins,
    the code's writers) and says the original stays private.
  - A sandbox itself can't be shared this way, only files from it.
- **The host leaving** (deleted/disabled/lost read, PD-20): the conversation
  pauses with "its host's private resources are gone".

### 5. Channels, triggers, schedules and handoffs

The bridge and webhooks tiles are non-partitioned, so they reach the global
instance (05 §1; 09). Global routes per person with **partition mail**
(04 §3); routing tables live in **global's `db`**, written only by global —
a partition can't redirect another person's traffic (S8).

| Arrives at global | Goes to |
|---|---|
| a message in a group/team channel | a shared conversation in global (today's behaviour) |
| a DM from a chat account **linked** to alice (`channel_peers.xbin_user` in global's `db`) | global records `handoffs(id, channel, peer, user, at, state)` — routing metadata, no content — and mails `user:alice` `handoff/dm {handoff, peer name, text, files}` (files inline up to the 1 MiB item limit; larger ones staged in global's `files`, fetched by alice's partition through F5 — attributed, checked against the handoff's user — and deleted on ack; documented as passing through global's storage). Alice's `/mailbox` stores the message privately, runs the DM conversation in her `db`, acks the mail |
| alice's reply | alice's partition mails `global` `outbox/add {inReplyTo: <handoff>, text, files}`; global checks `from` (`user:alice`, stamped by xbind) is the handoff's user and takes the destination **from its own handoff record**, never from the payload (S4) → appends to its outbox → the adapter's `/adapter/outbox` stream (one per adapter, unchanged); rows of linked-DM replies are purged once the adapter acks |
| a DM from an unlinked account | the channel's conversation in global (today) |
| `POST /adapter/link` from the bridge's frame (person = `X-XBin-User`) | global records the link in `channel_peers` (global's `db`), as today (`channel_links.go:51,75`) |
| `POST /adapter/event` for a **team** trigger | global runs it (today) |
| … for a **private** trigger of alice | global mails `user:alice` `handoff/event {trigger, eventId, data}` (same size rule); her partition runs it with the trigger's config from her `db` |
| a team schedule | global's `beat` job (today) |
| a private schedule | registered by the person's partition in its own `beat` rows, fired into it (03 §D) |

**Private triggers (S10).** The **trigger registry** stays in global's `db`
(today's `triggers` table, name unique tile-wide): `{name, source,
source_ref, match, owner, host}` — metadata. A person creates a private
trigger from her partition: the partition keeps the trigger's config
(instructions, class) in her `db` and registers the name through F5
(attributed). Global refuses a private **push** trigger with an empty
`match`, or whose `match` is a prefix of (or prefixed by) another owner's on
the same source — so no one can quietly capture every webhook. Managers see
the registry rows (name, source, match, owner) as today's oversight (D83),
not the configs. Bus-sourced private triggers are registered the same way
and show in the person's egress ledger (06 §6.1) and as per-source counts in
`GET /partitions`.

`sessions` (external session key → run) and `share_links` stay with shared
conversations in global's `db`. `run_grants` (D111), `asks`, `summaries`,
`memory`, `repl_files` live with their run's database.

### 6. Sandboxes (D115)

- A user partition calls managers with `X-XBin-Partition`/`-Id` (xbind sets
  them); the person is verified by the partition (07 §4). A private
  conversation's sandbox is homed at `(apps/agent, <partition id>)` — no
  other person's partition can list or use it, even through a buggy agent.
- A shared conversation's sandboxes are created by global (home
  `(apps/agent, "")`, which is global — so every existing sandbox stays
  global's), team-visible as today (`forConversation`). A person's page and
  partition still see and open them when the person passes `personOK` there
  (07 §4, C7).
- A hosted conversation may attach the host's own sandboxes.
- **Labels:** `xbin.agent/home=<global | partition id>` beside
  `xbin.agent/conversation=<id>` (`sandbox_routes.go:506-509`), so ids of
  different homes never collide.
- **Old managers (C12):** in user mode, a bound manager whose `hello.caps`
  lack the word `partitions` is not used: sandbox tools are unavailable in private
  conversations, with one banner naming the manager tile and the update
  command; shared conversations (global) keep working with it. The switch
  confirmation (01 §2.5), instantiation and `bx doctor` check bound managers
  first. docs/partitions.md gives the order: update the sandbox managers,
  then instantiate or switch the agent.

### 7. Managers, settings, oversight and interfaces

- `settings`, classes (D116), model tiers, the halt switch and shared skills
  live in global's `db`; managers edit them through global (F5 attributed;
  global checks `X-XBin-User-Level`); global mirrors them into `conf`. User
  partitions read `conf` at each use (the `halt` check, `access.go:116`,
  reads `conf` in user mode).
- Managers' oversight (D83: "oversee every automation") covers **team**
  automations and conversations fully; for private ones they see the trigger
  registry's metadata (§5) and, per person, **usage totals** (runs and model
  tokens per day — the tile's LLM budget is theirs to govern; each partition
  mails global a daily `usage` summary), never content or activity
  timelines. Admins see the same (they are managers of every tile).
- Shared skills (`conf`) vs private skills (the person's `db`): a person's
  partition reads both; global reads shared only.
- **Interfaces (PD-16, PD-54).** The agent's `llm`, `mcp` and `sandboxes`
  slots are multi http slots.
  - On a partitioned instance, **global binds** to shared providers
    (llm-gw, sandbox managers, shared MCP tiles) are an admin's act. An org
    admin or personal owner who managed an unpartitioned instance's wiring
    must ask an admin.
  - Each person may add their **own** user-owned tiles with a **personal
    bind**, for example a personal MCP server or model gateway. It appears
    in their partition's `XBIN_IFACE_MCP`/`_LLM` list only, and the agent
    offers its tools or models in that person's conversations only. Global
    (shared conversations) never sees it.

### 8. LLM concurrency (PD-36, C18)

- The **tile-wide cap is on by default at today's limit** (`MaxActiveRuns`,
  default 4), so partitioning doesn't multiply upstream concurrency: a flock
  semaphore of K lock files `llm.slot.<i>` beside `team`'s file (the shared
  read-write mount); a model call try-locks any free slot (polling with
  backoff), releases it on return, and a dead process releases it
  automatically — the D81 flock precedent, no sqlite writes on the hot path.
- The per-process gate stays (`llmgate.go`): global 4, user partitions 2.
- llm-gw keeps per-(From, Partition-Id) counters on by default (09 §5).

### 9. Engine details

- `acquireLock` (`owner.go:35`) is unchanged code: `<db>.engine` next to the
  mode's own `db` (today's in global/legacy, the partition's in user mode);
  plus `<team>.engine.<pkey>` for a hosted scope (§4).
- The hold self-call stays in the caller's partition (02 §4 rule 2).
- **Resume in user mode (C4):** on exit, register `resume` only for runnable
  work (running, queued, undelivered inbox), and for sleeping runs one 5-field
  cron job at the earliest `wake_at`'s minute (deleted at takeover) —
  never an `@every 1m` for awaiting/blocked runs, which wait for a person who
  will open the tile anyway. Global/legacy: today's rule.
- `sched-*` jobs register in the caller's partition's store (03 §D).
- `/mailbox`: pull (`xbin.Inbox`), dedupe by mail id, idempotent handoff ids,
  ack; also pulled at every start.
- Run lifecycle events (`handlers.go:25-31`) stay on the partitioned `events`
  bus: a person's activity stays in their partition; external subscribers of
  `res:apps/agent/events` keep today's meaning (global's runs) (S18, C9).

### 10. New and existing instances (PD-35, PD-34, PD-44 — decided)

- **New instances are partitioned by default** (PD-35). The template's
  `template.partition: ["user","global"]` is written into the instance's
  manifest at instantiation (01 §2.7). The fresh instance holds no data, so
  its first rescan records the mode.
  - The Tile Manager's template card shows a checked "Keep each person's
    data apart" box; unchecking it (or `--no-partition`) makes an
    unpartitioned instance.
  - Without `--isolate` the box is off and disabled, and the instance is
    unpartitioned (PD-19).
  - The overhead is bounded by caps and the idle stop (03 §A.5).
- **Existing instances keep their mode.** Their builder's `git merge
  template/main` (D50) never adds `partition`, because the default lives
  in the stripped `template` block. They keep running in legacy mode,
  indefinitely.
- **Switching an existing instance** (PD-34) follows the mode rule, with
  **no migration**:
  1. A manager adds `"partition": ["user","global"]`.
  2. The instance holds data, so this is a switch request: the agent greys
     out. The alert (01 §2.4) reads: "All data in this tile will be
     deleted for the switch to happen", followed by the agent's
     `partitionNote`: *"Every conversation (private and shared), all memory
     and every schedule of this agent are deleted, for everyone, with its
     skills, triggers, channel links and settings. Sandboxes stay with
     their sandbox manager."*
  3. **Keep the current mode** → the agent runs as before and nothing is
     lost. **Switch** (a tile manager, typed confirmation) → `db`,
     `files`, `events`, `conf`, `team`, the `beat` jobs (schedules, resume),
     the vault and registrations are deleted; their backup subkeys are
     erased (11 §3); every person's data starts empty. Sandboxes at the
     managers are listed so a manager can clean them up there.
- `--no-auth` dev mode: every call is the owner → global.

### 11. Frontend

- `xbin.partition` decides the layout — **three states**:
  - absent (legacy) = today's UI;
  - `user:*` = "Mine" (partition) + "Shared" (global) lists, the ⚠ chip, the
    non-secure modal, "Add a copy of my …", the sandbox-manager banner;
  - `global` (root/owner token, `--no-auth`) = the legacy single list of
    global's conversations with a notice "sign in as a person for private
    conversations" (C19).
- `model/stream.js`: one stream per home (partition, global); the shared
  stream only while a shared conversation or the Shared list is open; **every
  stream closes when `document.hidden` and reopens on visibility** (C4), so a
  background tab doesn't pin a partition against the caps.
- UI harness (`hack/ui-harness`, the `agentTemplate` seed): two people, one
  private conversation each, one shared, one non-secure; asserting passes for
  visibility, the modal on every start, the chip, widening → paused +
  prompt, `#c=` of an old global id still opening it, the global-viewer
  state.

## Tests

- Go (template `_backend`): mode detection; migrations run only on the mode's
  own `db`; partition `db` ids start at 2^40; `openShared(team)` never
  migrates and waits/503s on an old schema; engine lock per mode; the hosted
  engine drives only conversations in its own `hosted` table (a forged
  `team.host` row is ignored); widening pauses and prompts; share links
  refused on hosted conversations; trigger registry rules (empty/overlapping
  private `match` refused); handoff reply destination taken from global's
  record, and an `outbox/add` from another person refused; `halt` read from
  `conf` in user mode; the LLM slot semaphore caps concurrent calls across
  processes and frees on process death; resume registration by mode; legacy
  mode golden = today (existing tests unchanged under `XBIN_PARTITION=""`).
- e2e (fakeopenai + harness `agentTemplate`, the D81 recipe): alice and bob
  each chat privately — neither's partition `db` contains the other's rows
  (inspect via `bx` as each person, and on disk under `.partitions/`);
  global's `db` isn't mounted in either partition; a shared chat runs in
  global; a linked DM reaches alice's partition and the reply reaches the
  bridge's outbox; a private webhook trigger fires in alice's partition; an
  admin's F5 call can't mail or read alice's handoffs; alice's sandbox isn't
  listed from bob's partition; an old-manager fixture makes private
  sandboxes unavailable with the banner and leaves shared ones working.
- **Instantiation:** a new instance is partitioned (its first rescan
  records `auto`); with the box unchecked, or without isolation, it is
  unpartitioned.
- **Existing instances:** a template merge fixture never adds `partition`.
  Adding the line to an instance with runs gives pending: the switch page
  shows the note, and the API answers 409. **Keep** → legacy mode, every
  run still there. **Switch** → the `db`/`files`/`conf`/`team` volumes and
  the `beat` jobs are gone, and people's lists are empty; a sealed backup
  taken before the switch refuses to restore its data (erased).
- **Share a copy:** a copied file appears in the shared conversation, and
  the private original is unchanged.
- A downgrade to legacy mode shows global's runs only.
