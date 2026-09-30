# B2a — records for the integrator

Work pack B2a (agent: modes, split, engine, default — 08 §1-§2, §6-§9, §11
states) of the partitioned-tiles plan, branch `pt/b2a` on `partitions` at
a5e26298. Implements **PD-30**, **PD-35** (decided: new agent instances are
partitioned by default), the agent's side of **PD-34** (decided: existing
instances keep their mode — no migration), **PD-36** and the agent's half
of **PD-39** (the old-manager degrade, C12). This pack edits neither
docs/changelog.md nor plans/DECISIONS.md; the texts below go there at merge.

Commits: `0e38c7af` (the backend: three modes, the split, the engine),
`75451a5f` (the default, the frontend's three layouts, docs), `f1bd5949`
(the e2e and the real-template merge guard), `da9cbd53` (slots fail open),
`98b57d60` (the harness pass), `4d9e41ab` (this record); then the review
fixes on `pt/b2a-fix` (§Review fixes below): the backend and API.md, the
e2e, the harness seed, and this record's update.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: new instances keep each person's conversations
  apart** (the template's API.md "Partitioned instances",
  [partitions.md](/docs/partitions.md) §The mode,
  [sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned consumers). The
  builtin agent template now asks for `"template": {"partition": ["user",
  "global"]}`: a new instance (Tile Manager → New from template, `bx
  template new agent`) runs one backend per person who uses it — their own
  conversations, memory, skills, schedules and sandboxes, which nobody
  else's frame, terminal or tile reaches, workspace admins included — and a
  global instance for the tile-wide settings, chat channels, event triggers
  and other tiles' calls. Untick **Keep each person's data apart** (or
  `--no-partition`) for an unpartitioned instance; without `xbind
  --isolate` it is unpartitioned anyway. **Existing instances keep their
  mode** and run exactly as before; switching one deletes its
  conversations, memory and schedules (its `partitionNote` says so). In a
  partitioned instance: a person's conversation ids start at 2^40 (the
  global instance's, like an unpartitioned instance's, at 1); the config,
  classes, halt switch and shared skills are the global instance's,
  mirrored into a new `conf` resource that everyone who can open the agent
  can read — so it carries the config as viewers see it, and **a static MCP
  server with `headers` is not used in people's conversations** (bind it as
  a tile instead); a manager reading or changing the settings from their
  own partition is forwarded to the global instance; a config or shared
  skill over 900 KiB is refused there; a halt stops every partition's runs
  within a step, and a partition that can't read `conf` yet waits (runs
  parked, work queued) rather than run without the managers' settings; a
  conversation or schedule in a person's partition can't be shared, and
  channels and triggers are set up at the global instance (409 in a
  partition); model calls have a tile-wide cap (`maxActiveRuns`, default
  4) across every partition — a subagent's call never takes the last slot
  — as lock files beside the new shared `team` resource, and each
  partition's own gate allows 2; a partition that stops with work asks to
  be started again only for work that moves without its person; a sandbox
  manager whose `hello.caps` lack `partitions` isn't used in people's
  partitions (refusal `partitions`, 409, and one banner on the page naming
  it and the update — update the sandbox managers before creating or
  switching an agent), and a person's conversation works only in a
  sandbox its manager says is homed in their partition; every sandbox the
  agent makes is labelled `xbin.agent/home`; a person's own providers
  (personal binds) are offered in their own conversations only; the page's
  live streams close while it is hidden. New in every instance: `GET
  /health`, `POST /mailbox` (the partition-mail doorbell, `partitionMail`;
  an unpartitioned instance answers 404), `partition` in `GET /me` on a
  partitioned instance, and the `conf` and `team` resources in scope.json
  (never opened unpartitioned — though xbind provisions them, so an
  existing instance that merges the template gains one more, unused,
  encrypted `team` volume and an empty `conf` kv). Existing instances take
  the new files with a template merge (`git merge template/main`); its
  `xbin.json` conflicts in the `template` block, which the instance
  dropped — keep your side. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, B2a: the agent template partitioned by
  default — three modes in one binary (2026-09-30).** Implements PD-30,
  PD-35, the agent's side of PD-34 and PD-36, and C12 (PD-39) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §1-§2, §6-§9, §11. The template's
  API.md "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **One binary, three modes from `xbin.Partition()`** (mode.go): ""
      legacy (every existing instance, the opt-out, an older xbind — today's
      code path, every hook nil), `global`, `user:<id>`; an unknown kind of
      partition exits (status 3) rather than serve everyone. The legacy
      golden is the whole existing template suite, unchanged, plus
      TestLegacyModeUnchanged.
    - **The split (PD-30).** `db`, `files`, `events` stay per partition at
      their names (global's at today's keys, by F4); no person's partition
      mounts global's db (checked from /proc in the e2e). Each mode
      migrates only its own db — no code change: the partition's db is its
      own volume at the same path. A person's partition raises runs'
      AUTOINCREMENT to 2^40 − 1 at every open (idempotent, never lowers),
      so its ids start at 2^40.
    - **`conf`** (kv, `"shared": "read"`) is readable by everyone who can
      open the agent (their frame, through xbind's kv API), so it holds
      nothing managers keep from people: the config as viewers see it
      (`forView`) and without the static MCP servers that have `headers`,
      which stay global's (people's conversations don't get them). Keys:
      `halt` (its own small key, written first), `settings` (`config`,
      `classes`, a revision of the mirrored shared skills) and
      `skill:<name>`; written after every change, in the writer's goroutine
      after the commit, and at start; a key the kv refuses is logged and
      skipped (the rest still goes out) and retried every 30 s; a config or
      shared skill over 900 KiB is refused at save in a partitioned agent.
      A partition reads it through `getSetting`'s hook (cached 3 s;
      invalidated after a forwarded write; inside a transaction never
      waiting on the network — the cached copy, a refresh beside it; read
      once in `startMode` before the engine starts), refuses writing a
      mirrored key (`errSharedSetting`), lists shared skills beside its own
      and reloads its classes when conf's change. **It fails closed:**
      until both keys have been read once (global never ran — it is woken,
      at most every 5 min —, a kv error at start) the halt reads as on;
      without conf in `uses` it can never be read.
    - **Managers edit through global.** In a partition `GET`/`PUT /config`
      (the whole config: conf's is the viewers'), `PUT /classes`, `/halt`,
      and `PUT`/`DELETE` of a skill that isn't the partition's own are
      relayed by the partition's backend to `xbin.GlobalURL(path)` (F5/F9:
      attributed to its person; the relay's 30 s bound is the call's). A
      skill in a partition is its person's or everyone's: `owner` naming
      anyone else is 400; a new one with `owner` = the person stays local;
      their own saved with `owner: ""` is published (relayed, then deleted
      locally). The partition's guard checks first, global again (its
      level). The page is unchanged.
    - **The brake in a partition** (brake.go): a halt conf says is on
      cancels a run at its next step or pass (the turn loop checks it
      between steps in user mode only), as `PUT /halt` cancels live runs
      elsewhere; the manager's own partition cancels at once. While conf is
      unknown the halt reads as on without cancelling: runs park, requests
      for work are queued, and a one-shot timer with backoff (3 s → 1 min,
      only while runs are parked) re-reads conf and recovers them once the
      brake is off (`onHaltOff`: `eng.recover()`). Without conf in `uses`
      requests for work answer 503 saying so. A manager's request whose
      halt can't be lifted at global answers 503 (never queued behind the
      brake).
    - **Global takes a person's attributed calls** (agentRole): the route
      gate stays `RoleFunc("admin")` except, in global mode, a call with
      From = the tile, X-XBin-Partition `user:…`, a person and the role
      xbind clamps to (reader/writer) — read as `whoUser` with their level
      (W2's "F9's attribution reads as whoUser"). Such a call without its
      person is nobody (`whoNone`), never the tile itself.
    - **`team`** (sqlite, `"shared": true`): migrated only by global under a
      `<team>.migrate` flock (schema 1: its version row); a partition opens
      it without migrating (`openShared`), wakes global (`GET /health`) and
      waits ≤30 s when behind; `teamUnavailable` answers 503 "the shared
      space is being upgraded" for B2d's hosted features.
    - **LLM concurrency (PD-36):** `maxActiveRuns` lock files
      `llm.slot.<i>` in team's directory, try-locked (LOCK_NB, random
      start, 20→400 ms jittered backoff) after the process gate; released
      with the call; a dead process's slot frees with its fds. As the gate's
      kidCap, a subagent's call (and a title's) may take only slots
      0..n-2 when n ≥ 2, so a top-level call waits for at most one call
      tile-wide. A directory that can't hold them costs only the cap (calls
      and titles go ahead). A partition's gate: 2. The flocks need one
      kernel — xbind refuses `vm` with `partition` (F1), so a partitioned
      agent's backends never run in separate guests.
    - **Resume (C4):** a partition leaves `resume` (@every 1m) only for
      work a pass takes up without its person — running/queued runs,
      undelivered inbox rows, a settled link its parent takes (`fg` at a
      running/queued/awaiting parent; `bg` at a root that is running,
      queued, sleeping, idle, done or canceled — never an errored root or a
      non-root parent, which pass() leaves for a person's message), a run
      sleeping on a sandbox job (its end is seen only by looking); else
      `wake` (`CRON_TZ=UTC m h d M *`, the earliest `wake_at` of a sleeping
      or awaiting run, rounded up to its minute; within a minute counts as
      runnable); else nothing — and nothing at all while a known halt is
      on. Takeover deletes `wake` too. Global and legacy: today's rule.
    - **Sandboxes (C12):** a partition refuses a manager's hello without
      `partitions` (refusal `partitions` → 409) — catalog, tools, dialogs;
      global keeps using it. A partition's conversation binds and uses
      (checked again at every use) only a sandbox homed there by the
      manager's own word: `owner.via` = the tile, `owner.partitionId` = the
      partition's id (`owner.partition` = its key while the id isn't
      known), not `shared`; anything else — a manager that says nothing of
      the home included — is refused (403); its terminal still opens.
      `xbin.agent/home` on every create (`sbxConn.Create`) = the partition
      id (learned from X-XBin-Partition-Id on calls into it, kept in
      settings; the partition key until known) or `global`; none
      unpartitioned.
    - **Personal providers:** `personal: true` rows of XBIN_IFACE_LLM/MCP
      are offered only when a context says the conversation is the
      partition's person's (a turn, a compaction and a title set it from the
      root's owner; the model picker from the caller, not view-as). The
      default model's lookup uses shared providers only.
    - **Mail skeleton:** `POST /mailbox` (mounted apart: xbind rings it as
      xbin/mail with the writer role; else only the owner token or the tile
      itself — `principal()`, never callerOf's nobody-is-the-tile
      fallback) and every start pull a `mailSource`;
      a topic's handler runs with the item's id recorded in `mail_seen` in
      one transaction, then the item is acked; a redelivery is only acked;
      an unknown topic stays unacked. No handler, and the source holds
      nothing, until F6 is wired.
    - **No sharing in a partition** (until B2b): the sharing routes, `PATCH
      /runs/{id}` with a `visibility` other than private or a `teamRole`
      other than viewer, and a `team` schedule answer 409.
    - **The page's three layouts** (model/partition.js): unset —
      unchanged, not one element or call added; `user:` — no sharing (row
      menu, top bar, the Shared view; web and native), a banner for old
      sandbox managers (read at start when the slot is bound); `global` —
      a note to sign in as a person. Partitioned pages close their live
      stream while hidden and resume from the cursor.
  - **Not chosen:** the frame calling global for settings (the forward
    keeps the page unchanged and one place checks); a sqlite table or kv
    counter for the LLM cap (a write per call, and no release on death);
    one conf key holding the skills too (kv values are cut at 1 MiB);
    mirroring the static MCP headers, or a backend-only path to them
    (conf is people-readable, and a person's partition can't tell its own
    backend from its frame when it calls global); refusing `mcp[].headers`
    in a partitioned agent's config (global still uses them); parking a
    partition's runs under a known halt (they would stay `running` and the
    partition would restart every minute for the halt's length — they are
    cancelled, as unpartitioned); settings defaults while conf is unread
    (a manager's halt or classes would silently not apply); migrating team
    from any partition; dropping unknown mail topics (a newer global
    mid-deploy may be the sender); pausing unpartitioned pages' streams
    (xbind counts them as activity there today); filtering personal
    sandbox managers (hosted conversations may use the host's).
```

## Review fixes (pt/b2a-fix)

Every finding of the B2a review, and what became of it (the tests named are
the template `_backend`'s unless said otherwise):

| # | Finding | Resolution |
|---|---|---|
| 1, 11 (medium) | conf mirrored global's raw config — static MCP servers' `headers` (tokens) readable by any person through the kv API | **Fixed.** conf carries the config as viewers see it (`forView`) and without the static MCP servers that have headers (global keeps and uses them; people's conversations don't get them — API.md says so, and to bind such a server as a tile). `GET /config` in a partition is forwarded to global, so a manager never saves back a header-less config. `TestConfMirror` asserts `settings` never holds a header, the header-bearing server, or its token, and global's own config keeps them. |
| 2, 17a (low) | a partition ran on defaults whenever conf was missing, unread or failing — a manager's halt or classes silently not applied; halt shared the big `settings` value | **Fixed (fail closed).** Until both conf keys have been read once, the halt reads as on: runs park (nothing cancelled), requests for work are queued, and a backoff watch recovers them when conf answers (`TestBrakeFailsClosed`); without conf in `uses`, requests for work answer 503 saying why (`TestBrakeRequests`); conf is read in `startMode` before the engine starts; the halt has its own key, written first. |
| 3, 22 (low) | `/mailbox` authorized with `callerOf`, whose fallback makes an unidentified caller the tile itself | **Fixed:** `principal(r).kind == whoSystem` or `From == xbin/mail`. `TestMailboxSkeleton`: no headers, a person's frame, another tile and (at global) a person's partition without its person → 403; the tile itself → 200. |
| 4 (low) | `PATCH /runs/{id}` could still share (`visibility: team`, `teamRole`) in a partition | **Fixed:** 409 `noShareWords` for a visibility other than private or a teamRole other than viewer; a `team` schedule likewise (create and update). `TestUserModeRoutes`. |
| 5 (low) | a relayed `PUT /skills` with `owner: <person>` stored a private skill at global | **Fixed:** in a partition a skill is its person's or everyone's — another owner is 400, a new one with owner = the person stays local, their own saved with `owner: ""` is published (relayed, then deleted locally). `TestUserModeRoutes`. |
| 6 (low) | a private conversation was kept out of foreign sandboxes only by the manager's `shared` flag, at bind time only | **Fixed:** bound and used (re-checked in `sandboxUse`) only when the manager's owner says it is homed here — `owner.via` = the tile, `owner.partitionId` = the partition's id (`owner.partition` while the id isn't known), not `shared`; anything else, a manager silent on the home included, is refused. `TestPartitionSandboxes` (five owner shapes, before/after the id is known); the e2e binds alice's own sandbox (200) and the team's (403) through the real fake manager. |
| 7, 20 (low) | the e2e's "global's db not mounted" check could skip (environ unreadable) and classified only `resources-enc` lines | **Fixed:** the backend is found by its mount table (world-readable; environ isn't under a multi-uid user namespace): the process whose namespace has alice's volume at the canonical db path; a GET keeps the partition running first; not found → the subtest fails (no skip). Every mount under the workspace's `data/` and `.xbin/res*` is classified (hers, the shared team, anything else a BUG). Where the host may look inside her namespace it also reads the db file at the canonical path (not on this box: permission denied, logged). |
| 8 (low) | `xbin.agent/home` only on sandboxes made for a conversation | **Fixed:** `sbxConn.Create` labels every create in a partitioned mode. The e2e checks a sandbox alice makes without a conversation and one the global instance makes. |
| 9 (high) | `userWake` counted settled links whose parent never takes them (an errored root, a finished subagent) → `resume @every 1m` forever | **Fixed:** a settled link counts only where pass() takes it — `fg` at a running/queued/awaiting parent, `bg` at a root that is running, queued, sleeping, idle, done or canceled. `TestUserModeWake`: six link cases (errored root, finished subagent, failed subagent → nothing). |
| 10 (medium) | a halt set at global never cancelled other partitions' runs, and nothing re-poked them when lifted; parked runs stayed `running` → a restart every minute for the halt's length | **Fixed:** a halt conf says is on cancels a partition's live run at its next step or pass (the turn loop checks between steps in user mode), as `PUT /halt` does unpartitioned; `leaveWakeUp` registers nothing under a known halt; a run parked with pending work is recovered by the conf watch (`onHaltOff` → `eng.recover()`) or the next start. `TestBrakeInPartition` (halt at global → cancelled at the next step, no wake-up, the next message works after it lifts), `TestLeaveWakeUpByMode`. |
| 12 (medium) | the tile-wide slots gave top-level calls no priority | **Fixed:** a subagent's (and a title's) call may take only slots 0..n-2 when n ≥ 2. `TestLLMSlotsTopFirst` (two semaphores: three kids fill n-1, a fourth can't, a top call gets one at once; n = 1 shared). |
| 13 (medium) | the partitioned default ships before B2b/B2c | **Kept, gated:** this pack's acceptance asks for the default (PD-35); the merge risks now say `partitions` must not reach master or a release before B2b and B2c (or the line is dropped for such a merge) — an owner question. |
| 14 (low) | one refused skill blocked every later conf write | **Fixed:** the halt key first; a refused skill is logged and skipped (left out of the revision), the settings key still written, the error arms the retry; a config or shared skill over 900 KiB is refused at save in a partitioned agent. `TestConfMirrorSkipsWhatItCantWrite`. |
| 15 (low) | titles never ran when the slots were unusable | **Fixed:** `tryBackgroundLLM` fails open like `acquireLLM`. `TestAcquireLLMWithSlots`. |
| 16 (low) | a failed `clearHalt` (global unreachable) queued the manager's message behind a brake that stayed on | **Fixed:** `resumeIfHalted` reports it and `haltBlocks` answers 503; `gwDo` now honours the caller's deadline (the relay's 30 s), else 5 s. `TestBrakeRequests`. |
| 17b (low) | conf reads inside a sqlite transaction could wait on the network | **Fixed:** inside a transaction `getSetting` (and shared skills) serve the cached copy and refresh beside it; classes never wait. `TestConfNeverWaitsInTx`. |
| 18 (low) | `until_job` sleepers and awaiting deadlines were ignored by the partition's wake-up | **Fixed:** a run sleeping on a sandbox job leaves `resume`; an awaiting run's `wake_at` joins the `wake` job. `TestUserModeWake`. |
| 19 (low) | under `HARNESS_ISOLATE=1` the seeded agent became partitioned, breaking isolated passes that bind managers without `partitions` | **Fixed:** the seed makes `apps/agent` with `"partition": false`; `HARNESS_AGENT_PARTITION=1` keeps the template's default for the agentTemplate pass (run.sh documents it). |
| 21 (low) | flock-based cap and team lock across VM guests | **Refuted:** xbind refuses `vm` with `partition` (internal/registry/partition.go: "a vm backend can't be partitioned in this release"; docs/elements.md), so a partitioned agent's backends share one kernel. llmslots.go and API.md say so; B2d's hosted table inherits the same guarantee as long as that rule stands. |
| 23 (low) | API.md said `GET /health` answers any caller; no e2e of a merged pre-default instance; legacy instances mount an unused `team` | **Doc fixed** (the admin role — the tile's own frames, terminals and backend, the owner token — and, at global, a person's own partition). The unused `team` volume on merged legacy instances is in the changelog text and the merge risks. The merge itself is covered by `TestAgentTemplateDefaultKeepsExistingMode` (internal/broker, the real template); an e2e merge of a pre-default instance was not added (it needs a pre-default template version inside the test workspace). |

## Seams for the integrator

| Seam | Where | Filled by |
|---|---|---|
| `partitionMail mailSource` (default `noMail{}`: holds nothing) | `_backend/mailbox.go` | **F6**: an adapter over the SDK's `xbin.Inbox(after, limit)` / `xbin.Ack(ids...)` mapping `xbin.MailItem{ID, From, Topic, Data}` to `mailItem` (ctx unused by the SDK), set in `startMode` for partitioned modes. The doorbell `POST /mailbox` is already mounted (any role; `From` must be `xbin/mail`, or `principal()` the owner token or the tile itself) and xbin.json declares `"partitionMail": "/mailbox"`. F6 documents `partitionMail` in docs/partitions.md (its TODO line stays) |
| `mailHandlers map[topic]mailHandler` (empty) | `_backend/mailbox.go` | **B2c**: `handoff/dm`, `handoff/event`, `outbox/add` (global side), handoff ids idempotent through `mail_seen` + their own records |
| `userNoChannels` routes (409 in a partition: `POST /channels/{id}/claim`, `POST /triggers`) | `_backend/partition_routes.go` `userRoutes` | **B2c**: private triggers registered at global through F5 (the registry stays global's) |
| `userNoShare` routes (409: members, links, join) and `sharing()` (false in `user:`) | `partition_routes.go`, `model/partition.js` | **B2b**: the Shared list from global (`{partition: 'global'}`), publish-a-copy; the router by id (≥ 2^40: the partition; below: global — `partitionIDBase`) |
| global mode's F5 persons (`agentRole`, `personFromPartition`) | `partition_routes.go` | **B2b**: shared conversations created and listed there; today a person's `POST /ask` at global makes a private conversation in global's db — B2b decides whether global refuses that |
| `teamStore`, `teamUnavailable(w)`, `teamMigrations` (schema 1) | `_backend/team.go` | **B2d**: the hosted table, host scope lock `<team>.engine.<pkey>`, schema 2+ appended to `teamMigrations` |
| `ownConversation(root)` / `personalCtx` | `_backend/iface_personal.go` | **B2d**: a hosted conversation's runs are not the host's own (false today: they aren't in the partition's runs) |
| `callGlobal`, `gwDo`, `gatewayKV` | `_backend/gwcall.go` | B2b–d reuse them for F5 calls and kv (`gwDo` honours the caller's deadline, else 5 s) |
| `sharesInPartition(vis, role)` (409 on PATCH/schedule sharing in `user:`) | `_backend/partition_routes.go` | **B2b**: the shared-conversation path replaces the refusal |
| `confIn.view(block)` / `confState` (known · pending · missing), `onHaltOff`, `brakeIdle` | `_backend/conf.go`, `brake.go` | **B2c/B2d**: anything new that must not run under a halt or without the managers' settings reads the brake the same way |
| managers' `partitions` cap at instantiation / switch / `bx doctor` | xbind | **F13a/F11/F7b** (08 §6: the checks come first; the agent degrades on its own meanwhile) |
| the agent's `xbin.agent/home` in the admin's sandbox registry | xbind | B1's follow-up (`forPartition` on `SandboxSpec`) |

## Deviations

- **Settings writes are relayed by the partition's backend**, not sent by
  the page to global: the page and the native view need no second base for
  settings, and the partition checks the manager before global does.
- **In a person's partition, sharing and channel/trigger setup answer 409**
  and the page hides sharing and the Shared view, until B2b/B2c build their
  global halves. Schedules work in the partition (its own cron store).
- **The halt in a partition**: a halt set at global cancels a partition's
  live runs at their next step or pass (the turn loop reads conf between
  steps, in user mode only; cached 3 s), not at once — a model call in
  flight finishes first; the manager's own partition cancels its live runs
  at once. A run resting with pending work (a subagent's notice) is parked,
  not cancelled, and moves when a watch (only while such runs exist) sees
  the halt lifted, or at the partition's next start.
- **conf fails closed** (the review): while a partition hasn't read conf
  the halt reads as on — the page shows the agent paused for those seconds
  (`GET /me` `halted: true`), and requests for work are queued, not
  refused.
- **Static MCP servers with `headers` aren't offered in people's
  partitions** (the review: conf is people-readable). 08 §7 doesn't
  mention them; the global instance keeps using them.
- **The partition gate is 2 at start** (min with `maxActiveRuns`); a later
  config change reaches the tile-wide cap at once, not the partition's gate.
- **`xbin.agent/home`** is the partition key (`user:<id>`) until the
  partition learns its id from a call — normally before any sandbox. It is
  set on every create (`sbxConn.Create`), with or without a conversation.
- **`GET /health`** is a new route in every mode (the wake-up and a probe).
- **The personal filter covers `llm` and `mcp`**, not `sandboxes` (08 §4
  lets a hosted conversation use the host's sandboxes).
- **Stream pausing is partitioned-only** (08 §11 lists it for
  `model/stream.js` generally): unpartitioned pages keep today's stream,
  which xbind counts as activity for today's idle reap.
- **test/isolated/consumers_test.go opts out** (`"partition": false`): its
  cases share conversations and team sandboxes between people in one
  instance. On a remote QA xbind older than F13b, the field is refused
  (400) — run it against a release that has F13b.
- **The native view** gets the no-sharing state and the notices on its home
  screen; it reads `xbin.partition` as the web does (absent: today's).
- **hack/ui-harness/passes/agenttemplate.js**: the fan-out check expects 1
  subagent holding the model in a person's partition (its gate is 2), 3
  elsewhere, as before.
- **The harness seeds `apps/agent` unpartitioned** (`"partition": false`)
  also under `HARNESS_ISOLATE=1` — every agent pass, and the isolated
  codingSandbox/sandboxes passes whose managers lack `partitions`, are
  written for one instance; `HARNESS_AGENT_PARTITION=1` keeps the
  template's partitioned default for the agentTemplate pass.
- **The LLM slots fail open**: a team directory that can't hold the lock
  files (missing, read-only, no flock) lets calls and titles through
  without the tile-wide cap rather than hold them (logged nowhere: the gate
  still caps each process). Likewise a copy whose `uses` lacks `team` runs
  without the cap (logged at start) — only a missing `conf` fails closed
  (finding 2): the cap is a budget, conf carries the managers' brake and
  classes.

## Merge risks

- **The partitioned default (PD-35) is live from this pack, before B2b and
  B2c** (review finding): a new instance made from a build with only B2a
  has no sharing (409s, UI hidden), no Shared list, and channel claims and
  triggers only at the global instance, which no person's page reaches
  yet — fewer features than an unpartitioned instance. The acceptance of
  this pack asks for the default, so the `template.partition` line stays;
  **`partitions` must not merge to master, or be released, before B2b and
  B2c land** (or the line must be dropped from builtin-templates/agent/
  xbin.json for such a merge — the three-mode code and its tests don't
  depend on it). An owner question below.
- Existing (unpartitioned) instances that merge the template get the `conf`
  and `team` resources provisioned but never opened (an extra encrypted
  volume): the changelog says so; lazy provisioning for unpartitioned
  scopes would be xbind's (F4's) follow-up.

- **Existing instances merging template/main** get a conflict in
  `xbin.json` (the `template` block they dropped changed): keep ours. That
  is F13b's design (a block change always conflicts), now live for every
  agent instance; the changelog says so. `TestAgentTemplateDefaultKeepsExistingMode`
  (internal/broker) pins that neither side of that conflict carries a
  top-level `partition`.
- **F6 in parallel** touches docs/partitions.md (the `partitionMail`
  paragraph and the TODO list) — this pack changed only §The mode's last
  paragraph.
- `builtin-templates/agent/agent.js` is at 991 of its 1016-line budget.

## Tests

### After the review fixes (pt/b2a-fix, on the final tree)

- **The legacy golden:** `TILE_TEST_FLAGS="-race -count=1"
  hack/tile-check.sh agent` — every existing template test unchanged, plus
  the new ones — PASS (215 s).
- New and extended Go (template `_backend`): `TestBrakeInPartition` (a
  halt mirrored at global cancels alice's run at its next step; no
  wake-up; the next message works once lifted), `TestBrakeFailsClosed`
  (unread conf parks, nothing cancelled, the run answers once conf is
  written — no new message), `TestBrakeRequests` (queued while unread; 503
  without conf in uses; 503 when the halt can't be lifted at global),
  `TestBrakeWatchStops`, `TestConfNeverWaitsInTx`,
  `TestConfMirrorSkipsWhatItCantWrite` (and the 900 KiB refusal),
  `TestLLMSlotsTopFirst`; `TestConfMirror` (no header, no header-bearing
  server, global keeps them; the halt key), `TestConfReaderCaches`,
  `TestUserModeWake` (six settled-link cases, a job sleeper, an awaiting
  deadline), `TestLeaveWakeUpByMode` (nothing under a halt),
  `TestAcquireLLMWithSlots` (titles fail open), `TestUserModeRoutes` (GET
  /config forwarded, skill owners, PATCH/schedule sharing 409),
  `TestMailboxSkeleton` (nobody isn't the tile), `TestPartitionSandboxes`
  (five owner shapes).
- **e2e** `TestPartitionsAgent` — PASS (50 s): the mount check found
  alice's backend by its mount table (her db volume + the shared team, no
  other data; reading her db file from the host is refused on this box —
  logged, the mount table is the check); alice binds her own sandbox into
  a coding conversation (200), not the team's (403); home labels on
  sandboxes made without a conversation (hers, global's).
- S1 smoke `-run 'TestPartitionsSmoke(W2)?$'` — PASS (32 s + 69 s);
  `TestCodingSandboxConsumers` — PASS (77 s).
- internal/broker `TestAgentTemplateDefaultKeepsExistingMode`,
  `TestTemplatesNoTopLevelPartition` — PASS.
- Repo guards: `go test . ./internal/assetscan ./internal/sizebudget
  ./internal/docscheck ./internal/builtins ./internal/apicheck`, `make
  fmt-check vet js-check js-test` — PASS. Template browser test
  `test/partition.mjs` (scratch copy) — PASS (the fixes touch no frontend
  file).
- UI harness (PORT 8996, HARNESS_DIR …/scratchpad/h3-B2a): unisolated
  `agentTemplate` 28, `agentConvs` 19, `agentTask` 13, `agentSandbox` 76
  checks — all PASS; `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`
  `agentTemplate` against the partitioned instance (the admin's own
  partition) — 28/28 PASS (a new chat answered in 134 ms while 1 subagent
  held the model); `HARNESS_ISOLATE=1` with the default (unpartitioned)
  seed: `codingSandbox` 22, `sandboxes` 46, `agentSandbox` 76 — all PASS
  (finding 19).

### Before the review (pt/b2a)

- **The legacy golden:** `hack/tile-check.sh agent` with
  `TILE_TEST_FLAGS="-race -count=1"`: every existing template test passes
  unchanged (XBIN_PARTITION unset) — PASS (208 s, on the final tree).
- New Go (template `_backend`): `TestModeOf`, `TestDetectMode`,
  `TestLegacyModeUnchanged`, `TestNotePartitionID`,
  `TestPartitionIDsStartAt2to40`, `TestConfMirror` (mirror, read, refusal,
  skills merge, a deleted shared skill, classes and halt from conf),
  `TestConfReaderCaches`, `TestTeamOpenShared` (no migration from a
  partition, wake, 503, upgrade seen), `TestLLMSlotsCap` (the cap across
  two semaphores; a slot held by a killed helper process frees),
  `TestAcquireLLMWithSlots`, `TestUserModeWake`, `TestLeaveWakeUpByMode`
  (legacy resume, a partition's wake job, nothing for a waiting person, the
  takeover deletes), `TestUserModeRoutes` (forwarding, the reader refused
  locally, own vs shared skills, the 409s, `/me`), `TestUserModeHalt`,
  `TestGlobalTakesPartitionCalls`, `TestOldManagerDegrade`,
  `TestPartitionSandboxes` (labels by mode, the shared-sandbox refusal),
  `TestMailboxSkeleton`, `TestPersonalProviders`.
- internal/broker: `TestAgentTemplateDefaultKeepsExistingMode` (the real
  template: new instance partitioned, opt-out not, an instance from before
  the default never gains one through a template merge).
- JS: `hack/agent-template-partition.test.mjs` (layouts, rules, notices,
  the stream's pause/resume, unpartitioned unchanged) in `make js-test`;
  the template browser test `test/partition.mjs` (the three layouts
  against the real page; the stream closes hidden and resumes from its
  cursor); the features registry's `state.partition.*` keys in both views.
  Template browser tests from a scratch copy: all pass except
  `terminal.mjs`, which fails identically on the base commit a5e26298
  (`#sbxterm-pane bx-terminal` never shows in this environment) — not this
  pack.
- **e2e** `TestPartitionsAgent` (test/isolated/partitions_agent_test.go,
  real isolated xbind): modes (new instance partitioned and recorded
  `auto`, opt-out unpartitioned), private chats (alice, bob, carol's own
  partition, the owner token at global: each sees only their own; ids ≥
  2^40 vs 1; files stay; a reader can't change settings; sharing 409),
  global's db not mounted in alice's partition (/proc mount table: her db
  volume and the shared team only), sandboxes (old manager refused with
  `partitions` naming it, alice's sandbox labelled with her partition id,
  unseen by bob's partition and global; global keeps both managers),
  the existing (unpartitioned) instance unchanged — PASS (50 s).
- S1 smoke `-run 'TestPartitionsSmoke(W2)?$'`: PASS (35 s + 71 s).
  `TestCodingSandboxConsumers` (its agent now opted out): PASS (77 s).
- Repo guards: `go test . ./internal/assetscan ./internal/sizebudget
  ./internal/docscheck ./internal/builtins ./internal/apicheck`, `go test
  ./internal/broker`, `make fmt-check vet js-check js-test`: PASS.
- UI harness (PORT 8991, HARNESS_DIR …/scratchpad/h3-B2a), unisolated (the
  seeded agent is unpartitioned there: the legacy page): `agentTemplate` 28,
  `agentConvs` 19, `agentTask` 13, `agentSandbox` 76 checks, all PASS.
  With `HARNESS_ISOLATE=1` the seeded `apps/agent` is a partitioned instance
  (recorded `partitioned`, user + global) and `agentTemplate` runs in the
  admin's own partition: 28/28 PASS after one check was taught the
  partition's gate — its fan-out check wanted 3 subagents holding the model
  at once, and a person's partition allows 2 model calls, so 1 (the new
  chat still answered in ~140 ms). The other agent passes (sharing
  conversations with dev1, etc.) are written for the unisolated harness and
  weren't run isolated.

## Owner questions

- **Merge gating of the partitioned default (review finding 13):** keep
  the `template.partition` line on `partitions` and hold the branch back
  from master/releases until B2b and B2c land — or drop the line for any
  earlier merge? This record assumes the former.
- **Static MCP servers with headers in a partitioned agent:** they now
  stay with the global instance (people's conversations don't get them).
  Should a later pack give partitions a vault-backed path to such headers,
  or is "bind it as a tile / personal bind" the answer?
