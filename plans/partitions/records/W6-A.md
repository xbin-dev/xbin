# W6-A — the agent backend: coding agents under partitions

W6's B2 ([96-agtt-merge.md](../96-agtt-merge.md) §B2: A-M1..A-M5, A-S2,
A-S3, A-S4, A-S6) on `pt/w6-a`, from `pt/w6-base` (3fdb5ca0). The owner's
ruling is [90-decisions.md](../90-decisions.md) §I15: coding agents
(harnesses, D147) only in a person's own conversations — never at the
global instance, in shared or hosted chats, and a harness conversation
doesn't move between homes. No new xbind HTTP/WS surface (every change is
the agent template's own API, in its `API.md`); no docs/changelog.md or
plans/DECISIONS.md edits (their texts are below).

Commits (oldest first):

| Commit | What |
|---|---|
| `b272032f` | A-M1, A-M2, A-M3 — the refusals (global, hosted, moves); `harness_partition.go` |
| `38e19a77` | A-M4, A-M5, A-S4 — the partition's wake, hold and brake |
| `a66e022a` | A-S2, A-S3 — the relays and the rows/catalog: sandboxes homed here |
| `d3f7bd43` | A-S6 — coding agents' sessions in the usage totals |
| `3c2d2c33` | the tests (`harness_partition_test.go`) |
| `94fd774f` | the template's API.md |
| this | the record |

## What was built

The rule lives in one new file, `_backend/harness_partition.go`; every
other change is a line or a hunk that calls it. Unpartitioned, each new
branch is a no-op (checked item by item below and by the legacy golden).

### A-M1 — none at the global instance

- `harnessClass` (the one door for `POST /ask` and `POST /runs` with
  `harness`, drafts included): at global → **409** `coding agents work only
  in a person's own conversations — not in the shared space, where a
  sign-in would sit in a sandbox others use: start one in your own space`
  (`harnessNotAtGlobal`).
- `subagent_spawn`: `spawnHarnessIDs` offers none at global (and none where
  `Config.noHarness` — an unexported, never-stored per-turn flag
  `runToolSpecs` sets from `harnessBarred(run)`), so neither `harness` nor
  `harness_mode` is in the schema; `harnessSpawnOf` refuses one asked for
  anyway (`harness: <the words>`).
- Sign-in: `POST /runs/{id}/harness/authenticate` and the run terminal's
  `login=1` answer **409** at global, before anything else (the relay after
  its "a terminal is a WebSocket upgrade" 400).
- The catalog at global: every entry `available: false`, `reason:
  "shared-space"`, `why` the same words (an addition to the plan — the page
  and native get a machine-readable reason instead of offering one that
  would 409).
- **Structural**: `harnessUse` (spawn, attach, the rights re-check and the
  minute re-check all go through it) returns a `harnessFail` with the words
  at global — so a channel's, a trigger's or a schedule's run, a message to
  a harness run that got there somehow (none can be made now; W6-base's
  tree could), a takeover: none reaches an adapter; the turn fails with
  why. Tested with a planted harness run.

### A-M2 — no hosted harness

- `harnessBarred(run)`: in a partitioned instance a run whose id is in
  team's range (`hostedID`, every hosted conversation's run) → `coding
  agents work only in a person's own conversations — not in a non-secure
  (hosted) one, whose members would drive it with its host's sign-in`. So a
  hosted run's `subagent_spawn` has no `harness`, refuses one, and the host
  engine's `harnessUse` refuses one.
- `POST /hosted` (global, `handleHostedMove`): a conversation
  `harnessStays` refuses → **409** before `moveIntoTeam` (the move's
  `exportConv` would refuse it too). A private conversation can't be hosted
  anyway (≥ 2^40 → 409, B2d), and none exists at global, so this is the
  defence the plan asks for.

### A-M3 — a harness conversation stays home

- `DB.harnessStays(root)`: `*harnessMoveErr` when root's engine is
  `harness` (`harnessCantMove`: "a coding agent's conversation stays in the
  space it was started in — it can't be published, copied, hosted or moved
  to another (its sign-in and sandbox are its person's)"), or when a harness
  run below it is **live** — its run active (`running`, `queued`,
  `blocked`, `awaiting`, `sleeping`, `waiting_input`) or its adapter up
  (`exec_id` set, state `starting`/`live`/`login`) — (`harnessStillWorks`:
  "… stop it (or wait until it finishes), then try again …"). A harness
  child that finished (adapter stopped, run resting) doesn't hold the tree:
  a bundle carries the root's transcript only, where its answer is.
- `exportConv` calls it first: so `POST /runs/{id}/publish`, `GET
  /runs/{id}/export` (and through it `POST /copy`, which passes global's
  409 through), `GET /moves/{id}/export`, `moveIntoTeam` and
  `continueAtGlobal` all refuse. `writeBundleErr`, `writeImportErr` and
  `writeTxErr` map it to **409**.
- AF's un-share: `moveIfUnshared` (global, inside the transaction that
  changed who shares it) returns the error — the PATCH visibility / DELETE
  member transaction rolls back and answers **409** (`handleRemoveMember`
  now answers through `writeTxErr`; for every other error that is the 500 it
  was). The plan's "un-share answers 409".

### A-M4 — the user-mode wake counts harness work

`resume_mode.go` `userWake` (a person's partition's exit):

- runnable (`resume`): `harnessSendingSQL` — a prompt in `sending` (its
  successor settles it: attach fails it "send it again"). A turn in flight
  (`sent`) is a `running` run, counted already; one parked on a person
  (`waiting_input`) is not work.
- `wake` at `harnessIdleWake`: the earliest `last_active_ms` of an adapter
  up (`starting`/`live`, `exec_id` set) with no prompt, its run neither
  `running` nor `waiting_input`, **plus the idle reclaim's time**
  (`harnessIdleMin`, read from the cached conf — no network at shutdown),
  never earlier than now + 61 s — so it is always the 5-field `wake` job,
  **never `@every 1m`**; none when the reclaim is off (0) or for a `login`
  adapter (waiting on a person). The earlier of it and a sleep's wake wins;
  a due sleep stays `resume` as before.
- The takeover it brings: `attachHarness` in a person's partition arms the
  reclaim at `last_active + idle` (`armIdleFrom`) instead of a whole idle
  time from now, so the woken partition stops the adapter at once and goes
  idle. Unpartitioned the attach keeps today's full re-arm (see Bugs).
- `hostedWakeUp` takes `agent.db.userWake`'s wake already, so a host's
  partition gets it too.

### A-M5 — the brake reaches harness runs

- `harnessPass`'s halted branch calls `e.onBrake(run)` in a person's
  partition (the run re-read first): a known halt cancels it
  (`cancelRuns(…, haltReason)` → the cancel row → `harnessCancel` stops the
  adapter, the run `canceled` "halted: a manager paused the agent"); conf not
  read yet (fail-closed) → `watchBrake` — the release timer, armed while
  `brakeParked` (a queued `hprompt` counts) — and the prompt goes once conf
  answers (`onHaltOff` → `recover`). Unpartitioned and at global it returns
  as before.
- A live turn: `hsess.brakeSoon`, called by `consume` after **every** event
  (the cached `confIn.view(false)`: a mutex and a clock read; at most one
  kv refresh at a time beside it, i.e. one per `confTTL` while a turn
  streams — a built-in turn reads it between steps): a known halt pokes
  the run once (`braked` resets when it reads off), and the pass above
  cancels it.

### A-S2 — the relays

`personSandbox` (the run terminal, `authenticate`'s check, the log) in a
person's partition: `managerHello` first (a manager without `partitions`
→ refusal `partitions`, **409**), then `partitionBoxRefusal` after the Get
(not homed here → `not-allowed`, **403**, the words saying why), then the
existing `Use` check. `GET /sandboxes/{ref}/terminal` is unchanged — it
already asks the hello, and a person may open a team sandbox's terminal
(sandbox_partition.go's rule).

### A-S3 — only sandboxes homed here

- **Rows** (`sandboxItem`: `GET /sandboxes`, `GET /sandboxes/{ref}`), in a
  person's partition only: `homed` (bool, `homedHere`) and, when false,
  `why` (`partitionBoxRefusal`'s words). **Agreed field names for W6-U:
  `homed` + `why`.** Absent unpartitioned and at global (rows byte-identical
  there).
- **The catalog**: `GET /harnesses`' `sandboxes` map keeps refs homed here
  only; `?probe=` refuses a sandbox not homed here (its `error` the words)
  — so nothing a partition learns is about another home's sandbox.
- **`prefs/harness-sandbox`** is an xbind pref the page reads and writes
  (`model/harness-store.js`); the backend never sees it. Keying it by home
  is W6-U's (U-M3); with `homed` the page can also drop a remembered ref
  that isn't homed here.

### A-S4 — an idle adapter doesn't hold a partition

`hsess.rest` (atomic): true while the session rests (`armIdleFrom`) or is
parked on a person (`parkOrQueue`; an attach onto a `waiting_input` run);
false while it works (`sendPrompt`, a cleared park whose turn goes on,
`followDetached`; a new session starts false). `updateHoldLocked` →
`harnessHoldsLocked`: unpartitioned and at global `len(e.harness) > 0`
(today's); in a person's partition only a session not resting. Each change
recomputes the hold (user mode only). A stopped partition's adapters are
let go untouched (BeginShutdown), and A-M4's wake brings it back for the
reclaim.

### A-S6 — usage

`usage_daily.harness_sessions` (a person's partition) counts each adapter
generation started (`harnessUsageTx` in `spawnHarness`'s fenced
transaction), `usageDays` sums it, the mail carries it, global's
`usage_days.harness_sessions` keeps it, `GET /usage` shows
`harnessSessions` per day and in `total`. Columns added in
`addHandoffSchema` (partitioned only; `ALTER … ADD COLUMN` for tables an
earlier build made, `CREATE` for new ones). An older global ignores the
field; a newer global reading an older partition's mail gets 0.

## Seams for W6-U and the wiring pass

| Seam | Where | Contract |
|---|---|---|
| `homed`, `why` on sandbox rows | `sandbox_routes.go` `sandboxItem` | a person's partition only; `why` only when `homed` is false |
| `reason: "shared-space"` | `harness_catalog.go` | every catalog entry at a partitioned agent's global instance |
| 409 words | `harness_partition.go` consts | `harnessNotAtGlobal` (/ask, /runs, authenticate, login=1), `harnessCantMove` / `harnessStillWorks` (publish, export, un-share, hosting) — the page can key on the status and show `error` |
| `harnessSessions` | `usage.go` `usageDay` | per day and in `total` of `GET /usage` |
| `harnessBarred(run)` | `harness_partition.go` | the one predicate for "may a coding agent start/work in this run here" — B5's e2e can assert through it |

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: coding agents only in a person's own
  conversations** (the template's API.md "Partitioned instances" → "Coding
  agents only in your own conversations"; [partitions.md](/docs/partitions.md)).
  In a partitioned agent a coding agent (Claude Code, Codex, …) signs in
  inside its sandbox, so it now works only where that sign-in stays its
  person's: their own conversations, in a sandbox homed in their partition.
  The global instance never starts or drives one — `POST /ask` and `POST
  /runs` with `harness` answer 409 there, its catalog lists each one
  unavailable (`reason: "shared-space"`), its agent can't spawn one, and
  signing one in there answers 409 — so a shared conversation, a channel,
  a trigger or a schedule never reaches one. A non-secure (hosted)
  conversation has none, and one a coding agent answers can't be hosted. A
  coding agent's conversation — or one where a coding agent it started
  still works — can't be published, copied, hosted or moved to another
  space, and un-sharing one answers 409. In a person's partition an idle
  coding agent no longer keeps the partition running: it is stopped at its
  idle time by a wake-up the partition leaves (never an every-minute
  restart); a manager's halt reaches a coding agent's turn at its next
  step, and one waiting for the settings to be read doesn't start; its
  terminal, sign-in and log are refused for a sandbox not homed there (403)
  or a manager that can't keep people apart (409); `GET /sandboxes` says of
  each sandbox whether it is `homed` there (and `why` not); `GET /usage`
  adds each person's coding-agent sessions (`harnessSessions`).
  Unpartitioned instances change nothing. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, W6-A: coding agents only in a person's
  own conversations — the agent backend (2026-09-30).** Implements the
  owner's ruling 90 §I15 of plans/partitions/90-decisions.md in the
  agent's backend, as plans/partitions/96-agtt-merge.md §B2 planned it;
  amends D147 (coding agents) for partitioned instances. The template's
  API.md "Partitioned instances".
  - **Chosen.**
    - **One predicate, checked at the doors and at the engine**:
      `harnessBarred(run)` — the global instance, or a hosted (team-range)
      run. The doors (`harnessClass` for /ask and /runs, the spawn tool's
      schema and call, sign-in and the login terminal, the catalog) say why
      with 409 or leave the option out; `harnessUse` — which every spawn,
      attach and re-check passes — refuses too, so no route or run
      (channel, trigger, schedule, a planted one) reaches an adapter
      whatever door it came through.
    - **No moves while a coding agent is in it**: `exportConv` — the one
      reader every copy between homes uses — refuses a harness root, and a
      tree whose harness child is still live (run active or adapter up);
      a finished child doesn't hold the tree (the bundle carries the
      root's transcript only). Un-sharing one at the global instance is
      refused (409) inside its transaction rather than moved or left
      private-at-global.
    - **A partition's lifetime follows work, not adapters**: only a coding
      agent at work holds the backend up; an idle one leaves one 5-field
      `wake` at last activity + harnessIdleMin (never `@every 1m`, never
      within the minute), and the woken takeover counts the reclaim from
      last activity, so it stops the adapter at once. A prompt in
      `sending` is `resume` work.
    - **The brake by reading, as a built-in turn does**: the harness
      pass's halt branch is `onBrake` (cancel when known, park with the
      release timer when conf isn't read), and a live turn looks at the
      cached conf at each event, poking its run once.
    - **Sandboxes**: the relays check the hello and the home like every
      use; rows say `homed`/`why` in a partition (the page's pick decides
      with them); the catalog keeps and probes only homed ones.
    - **Usage**: adapter sessions counted per day, no content.
  - **Not chosen:** a separate harness check in every route (the engine
    guard would still be needed; the predicate covers both); refusing
    every tree that ever had a harness child (its answer is plain
    transcript once it finished); moving a harness conversation without its
    session (its sign-in and sandbox are its person's — the owner ruled
    no moves); a ticker to read the halt during a silent turn (the engine
    has none; a turn is reached at its next event, as a built-in one at its
    next step); holding the partition while a coding agent waits for a
    person (C4: a person's wait never keeps a partition up); dropping team
    sandboxes from a partition's list (their terminals still open there —
    `homed` says why a conversation can't use one).
```

## Deviations

1. **The brake looks at every event**, not only durable ones (96 said
   "via durable events or recheckSoon"): a streaming turn's deltas are
   where a long answer spends its time, the look is a cached read, and a
   test can see it (a turn with only chunks is cancelled mid-way). Silent
   turns (a long command) are reached at their next event — a built-in
   turn's step has the same bound.
2. **The catalog at global answers `shared-space`** for every entry — not
   asked for; it makes the refusal machine-readable for the page and
   native.
3. **`harnessStays`' "live"** includes a child parked on a person
   (`waiting_input`) and an idle adapter still up: the person stops it
   (`/cancel`) or its idle stop does, then the copy goes.
4. **A sign-in started by a spawn keeps the hold** (the session never
   rests before its sign-in; the person's own requests keep the partition
   up meanwhile); after a takeover a run waiting on a sign-in rests (parked
   on a person). Either is harmless; noted for the owner below.
5. **The relative reclaim is a person's partition's only**: unpartitioned
   the takeover re-arms a whole idle time (today's; see Bugs).
6. **`prefs/harness-sandbox`** is frontend-owned (xbind prefs); the
   backend part of A-S3 is `homed`/`why` and the catalog filter.

## Bugs found

- **Unpartitioned, a takeover re-arms the reclaim for a whole
  `harnessIdleMin`** (`attachHarness` → `armIdle`), so an adapter idle
  across a restart lives up to twice its idle time; harness_life.go's
  header says "at last_active + harnessIdleMin". Left as is for the legacy
  golden (AgTT unchanged unpartitioned); fixed in a person's partition,
  where it matters (it decided how long the woken partition stays up).
- **(the plan's gap, confirmed)** a halt in a person's partition never
  reached a harness run: `harnessPass`'s halted branch returned without
  `onBrake`, so a known halt cancelled nothing and conf-not-read parked a
  prompt with no timer to release it; and a live turn never read conf.

## Owner questions

1. **The halt and an idle adapter in a partition.** Unpartitioned, a halt
   doesn't stop an idle coding agent (`haltStop` takes active runs), and
   while halted a pass reclaims nothing — kept as is in a partition, where
   the halt also leaves no wake-up, so an idle adapter stays up in its
   sandbox until the halt lifts and the person comes back (or its sandbox
   stops). *Built as today's rule;* stopping idle adapters on a halt would
   be a small addition.
2. **A sign-in holding a partition** (deviation 4): *built as it falls
   out*; say if a person's partition should let go during a device-code
   sign-in too.

## Tests

- New, `_backend/harness_partition_test.go` (all green, `-race -count=4`):
  `TestHarnessInPartition` (turn, hold let go, wake at the reclaim's
  minute, usage count, hold while working), `TestHarnessHoldUnpartitioned`
  (control), `TestHarnessReclaimAfterWake`, `TestHarnessUserWake` (sending,
  idle, overdue, sleep vs reclaim, parked, login, stopped, reclaim off),
  `TestHarnessBrakeInPartition` (mid-turn at an event; a silent turn at a
  pass), `TestHarnessBrakeFailsClosed`, `TestHarnessNotAtGlobal` (/ask,
  /runs, catalog, authenticate, login=1, spawn schema and call, a planted
  run fails without an adapter), `TestHarnessSpawnInPartition` (own vs
  hosted; legacy control), `TestHarnessStaysHome` (publish/export 409, a
  live child 409, a stopped one copies), `TestHarnessUnshareRefused` (409,
  no move, members kept; hosting 409; built-in control moves),
  `TestHarnessRelayInPartition`, `TestHarnessCatalogOldManager`
  (`manager-error` saying `partitions`), `TestSandboxRowsHomed`,
  `TestHarnessUsageMail`.
- The partition fixture (`harnessPartition`) runs the fake coding agent in
  a sandbox homed in alice's partition: the fake manager's handler is
  wrapped to hear the agent as xbind stamps her partition
  (`partitionManager`), with a switch to make one at the global identity
  (`atGlobal`) — B5's e2e can mirror it on a real isolated xbind.
- Whole package: `go test ./...` green (≈ 95 s), after every commit's
  tree vetted on its own.
- The legacy golden, `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh
  agent` (vet + every test, `-race`): three runs of the final tree —
  1. with `TMPDIR` in the session scratchpad: only `TestLeaveWakeUpByMode`
     (its gateway's unix socket path over the length limit) and
     `TestSandboxRead` (a byte budget that counts the temp path) failed —
     both the long path, not the code: **run with the default `/tmp`**;
  2. default `/tmp`: `TestSharedAskAtGlobal` failed once (a homes test at
     global that touches no code this pack changed; `-race -count=10`
     alone: green) — a load flake;
  3. the same tree, `go test -race -count=1 ./...` in the tile-check's
     layout: **green** (491 s).
- `make fmt-check`: clean. Not run here (the wiring pass's, B5): the
  isolated partition × harness e2e and I1's suites.
