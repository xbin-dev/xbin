# W6-A — the agent backend: coding agents under partitions

W6's B2 ([96-agtt-merge.md](../96-agtt-merge.md) §B2: A-M1..A-M5, A-S2,
A-S3, A-S4, A-S6) on `pt/w6-a`, from `pt/w6-base` (3fdb5ca0), and the
review's fixes on `pt/w6-a-fix` (§Review fixes below). The owner's ruling
is [90-decisions.md](../90-decisions.md) §I15: coding agents (harnesses,
D147) only in a person's own conversations — never at the global instance,
in shared or hosted chats, and a harness conversation doesn't move between
homes. No new xbind HTTP/WS surface (every change is the agent template's
own API, in its `API.md`); no docs/changelog.md or plans/DECISIONS.md edits
(their texts are below — the fold must carry them).

Commits (oldest first):

| Commit | What |
|---|---|
| `b272032f` | A-M1, A-M2, A-M3 — the refusals (global, hosted, moves); `harness_partition.go` |
| `38e19a77` | A-M4, A-M5, A-S4 — the partition's wake, hold and brake |
| `a66e022a` | A-S2, A-S3 — the relays and the rows/catalog: sandboxes homed here |
| `d3f7bd43` | A-S6 — coding agents' sessions in the usage totals |
| `3c2d2c33` | the tests (`harness_partition_test.go`) |
| `94fd774f` | the template's API.md |
| `5a960d0b` | the record |
| `5e9ee37c` | review fixes: the hold, wake and brake (sign-in parks rest, the rest/work race, the silent turn, the reclaim under the brake, a stuck `sending`) |
| `f724e38e` | review fixes: moves and hosts (the 409's words and `runs`, a member's leave, the host's sandboxes) |
| `30cb09dd` | review fixes: API.md |
| this | the record, after the review |

## What was built

The rule lives in one file, `_backend/harness_partition.go`; every other
change is a line or a hunk that calls it. Unpartitioned, each new branch is
a no-op (checked item by item below and by the legacy golden). The review's
fixes are folded into the sections below; §Review fixes lists them by
finding.

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
  why. Tested with a planted harness run, and with a planted adapter left
  running at global: the takeover that finds it stops it (`hsFailed`, the
  words), never drives it.

### A-M2 — no hosted harness

- `harnessBarred(run)`: in a partitioned instance a run whose id is in
  team's range (`hostedID`, every hosted conversation's run) → `coding
  agents work only in a person's own conversations — not in a non-secure
  (hosted) one, whose members would drive it with its host's sign-in`. So a
  hosted run's `subagent_spawn` has no `harness`, refuses one, and the host
  engine's `harnessUse` refuses one.
- `POST /hosted` (global, `handleHostedMove`): a conversation
  `harnessStays` refuses → **409** before `moveIntoTeam` (the move's
  `exportConv` would refuse it too) — a harness root, or a tree with a
  harness child still in it. A private conversation can't be hosted anyway
  (≥ 2^40 → 409, B2d), and none exists at global, so this is the defence
  the plan asks for.
- **The host's sandboxes (review fix)**: a hosted conversation runs on the
  host's engine in the host's partition, where the host's own
  partition-homed sandboxes pass `partitionBoxRefusal` — and a coding
  agent's sign-in sits in its sandbox's `$HOME`. `sandboxUse` (the check
  every sandbox tool makes, every call) now refuses, for a hosted root
  (`hostedHarnessRefusal`), a sandbox where one of the host's coding agents
  signed in (`harness_seen.signed_in`) or worked (a `harness_sessions` row
  with that ref), saying why and to create another. The hosting note
  (`moveIntoTeam`) says a hosted conversation reaches "their sandboxes and
  anything signed in inside them, though never one where a coding agent of
  theirs signed in or worked". A sign-in made by hand in a sandbox's
  terminal isn't known to the agent (named in API.md and below for AR-23).

### A-M3 — a harness conversation stays home

- `DB.harnessStays(root)`: `*harnessMoveErr` when root's engine is
  `harness` (`harnessCantMove`: "a coding agent's conversation stays in the
  space it was started in — it can't be published, copied, hosted or moved
  to another (its sign-in and sandbox are its person's)"), or when a
  harness run below it is still in it: **at work** — its run active
  (`running`, `queued`, `blocked`, `awaiting`, `sleeping`,
  `waiting_input`): `harnessStillWorks`, "a coding agent is still at work
  in this conversation (run #N) — stop it, or wait until it finishes, then
  try again: …" — or **finished but still up** — its adapter running
  (`exec_id` set, state `starting`/`live`/`login`) with its run resting:
  `harnessStillUp`, "a coding agent this conversation started (run #N) has
  finished, but it is still running, idle, until its idle stop at about
  HH:MM UTC — stop it (Stop on its run), or wait, then try again: …" (the
  time is its last activity plus `harnessIdleMin`; "until it is stopped"
  when there is none). The error carries the runs (`runs`). A harness child
  that stopped doesn't hold the tree: a bundle carries the root's
  transcript only, where its answer is.
- `exportConv` calls it first: so `POST /runs/{id}/publish`, `GET
  /runs/{id}/export` (and through it `POST /copy`, which passes global's
  409 through), `GET /moves/{id}/export`, `moveIntoTeam` and
  `continueAtGlobal` all refuse. Every route that answers it goes through
  `writeHarnessMoveErr` (`writeBundleErr`, `writeImportErr`, `writeTxErr`,
  `handleHostedMove`): **409 `{error, runs?}`** — `runs` for the page to
  offer the stop.
- AF's un-share: `moveIfUnshared` (global, inside the transaction that
  changed who shares it) returns the error with "— un-sharing it would move
  it to its owner's own space: keep it shared, or delete it" — the PATCH
  visibility / DELETE member transaction rolls back and answers **409**
  (`handleRemoveMember` answers through `writeTxErr`; for every other error
  that is the 500 it was). **A member's own leave (review fix)** is theirs:
  when `DELETE /runs/{id}/members/{self}` by a member (not the owner) would
  un-share it, the refusal is dropped and the leave goes; the conversation
  stays at the global instance with its owner (it can't run a coding agent
  there, nor move; its owner may delete it, or share it again). The
  owner's own un-share stays 409.

### A-M4 — the user-mode wake counts harness work

`resume_mode.go` `userWake` (a person's partition's exit):

- runnable (`resume`): `harnessSendingSQL` — a prompt in `sending` **on an
  adapter a successor attaches** (`exec_id` set, `starting`/`live`/
  `login`); its successor settles it (attach fails it "send it again"). One
  left on an adapter that is gone (stopped, lost, failed) isn't work — it
  would restart the partition @every 1m for good — and `resumeHarness`'s
  stop branch (an adapter still opening its session: never attachable) now
  ends that turn, "the backend was replaced while your message was on its
  way to … — send it again", instead of leaving the run `running` (review
  fix; a state today's code doesn't reach — a prompt is marked only once a
  session is open — so a guard). A turn in flight (`sent`) is a `running`
  run, counted already; one parked on a person (`waiting_input`) is not
  work.
- `wake` at `harnessIdleWake`: the earliest `last_active_ms` of an adapter
  up (`starting`/`live`, `exec_id` set) with no prompt, its run neither
  `running` nor `waiting_input`, **plus the idle reclaim's time**
  (`harnessIdleMin`, read from the cached conf — no network at shutdown),
  never earlier than now + 61 s — so it is always the 5-field `wake` job,
  **never `@every 1m`**; none when the reclaim is off (0) or for a `login`
  adapter (waiting on a person). The earlier of it and a sleep's wake wins;
  a due sleep stays `resume` as before.
- **Under the brake (review fix)**: stopping an idle adapter moves no work,
  so the brake doesn't hold the reclaim off. `leaveWakeUp` under a halt
  (`brakeIdle`) still leaves the reclaim's `wake` (nothing else), and the
  woken partition's `harnessPass` reaches the reclaim under the halt
  (`harnessIdleUnderBrake`, after `onBrake`): a session whose reclaim is
  due is stopped; a predecessor's idle adapter is taken over for it
  (`resumeHarness`). A turn, a park or a sign-in waits for the brake as
  ever. Before, with conf unread the woken partition did nothing and left
  the wake again (a restart every one or two minutes while conf stayed
  unread), and under a known halt the adapter lingered until the halt
  lifted and the person came back.
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
  as before. A coding agent a built-in conversation spawned is reached the
  same (its own pass).
- A live turn: `hsess.brakeSoon`, called by `consume` after **every** event
  (the cached `confIn.view(false)`: a mutex and a clock read; at most one
  kv refresh at a time beside it), and — **a silent turn (review fix)** —
  `brakeLook`: while any coding agent works in a person's partition, one
  engine timer (`Engine.hbrake`, every `confTTL`, 3 s; floored at 250 ms
  for tests) has each working session look (`brakeSoon`); it re-arms while
  one works, stops when none does, holds nothing (a turn at work holds the
  partition already), and a turn taken over mid-way arms it too. A known
  halt pokes the run once (`braked` resets when it reads off), and the
  pass above cancels it: a long build that prints nothing is reached within
  about two `confTTL` of the halt.

### A-S2 — the relays

`personSandbox` (the run terminal, `authenticate`'s check, the log) in a
person's partition: `managerHello` first (a manager without `partitions`
→ refusal `partitions`, **409**), then `partitionBoxRefusal` after the Get
(not homed here → `not-allowed`, **403**, the words saying why), then the
existing `Use` check. `GET /sandboxes/{ref}/terminal` is unchanged — it
already asks the hello, and a person may open a team sandbox's terminal
(sandbox_partition.go's rule). Tested at the function and through `GET
/runs/{id}/harness/log` and `…/terminal` (403).

### A-S3 — only sandboxes homed here

- **Rows** (`sandboxItem`: `GET /sandboxes`, `GET /sandboxes/{ref}`), in a
  person's partition only: `homed` (bool, `homedHere`) and, when false,
  `why` (`partitionBoxRefusal`'s words, exactly — asserted). **Agreed field
  names: `homed` + `why`** — W6-U's fix branch reads `why` (then `bindWhy`,
  its first version's name; `pt/w6-u-fix` 78977711). Absent unpartitioned
  and at global (rows byte-identical there).
- **The catalog**: `GET /harnesses`' `sandboxes` map keeps refs homed here
  only; `?probe=` refuses a sandbox not homed here (its `error` the words)
  — so nothing a partition learns is about another home's sandbox.
- **`prefs/harness-sandbox`** is an xbind pref the page reads and writes
  (`model/harness-store.js`); the backend never sees it. Keying it by home
  is W6-U's (U-M3); with `homed` the page can also drop a remembered ref
  that isn't homed here.

### A-S4 — only a coding agent at work holds a partition

`hsess.rest` (atomic, **changed under `s.mu`** since the review): true
while the session rests (`armIdleFrom`) or waits on a person — a question
(`parkOrQueue`; an attach onto a `waiting_input` run) or **its sign-in
(review fix)**: `loginTx` marks it after its commit (`AfterCommit(toRest)`),
so every road to a sign-in park rests — a prompt refused signed out
(`onTurnEnd`), codex's refused `session/new` at spawn, a wake, an
`_auth/status_update` with no turn. False while it works (`toWork`:
`sendPrompt`, a cleared park whose turn goes on, `followDetached`; a new
session starts false). `updateHoldLocked` → `harnessHoldsLocked`:
unpartitioned and at global `len(e.harness) > 0` (today's); in a person's
partition only a session at work, **or one AgTT is signing in**
(`hsess.signing`, set by `beginAuth`/`endAuth`: its `authenticate` awaits
the adapter's answer in this process — an API key 30 s, a device code at
most `hDeviceFor`, 15 min — whose outcome, resending the held prompt,
would be lost with the process). A person's wait never holds it.

**The rest/work race (review fix)**: a rest after a turn's end ran after
the commit whose poke may send a queued prompt, so it could land after the
next turn's `toWork` (rest true, the reclaim armed while a turn works: the
hold dropped mid-turn). Now `hsess.work` counts each `toWork`; `onTurnEnd`,
`endDetached` and `onResolved` take `workMark()` before their commit, and
`armIdleFrom(…, mark)` rests and arms only if no work came since — all
under `s.mu`; the hold is re-read after (`holdMoved`: never `e.mu` under
`s.mu` — `adopt` takes `s.mu` under `e.mu`). Unpartitioned the same check
only skips an idle timer the next turn's pass would disarm anyway.

A stopped partition's adapters are let go untouched (BeginShutdown), and
A-M4's wake brings it back for the reclaim.

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
| `homed`, `why` on sandbox rows | `sandbox_routes.go` `sandboxItem` | a person's partition only; `why` only when `homed` is false, `partitionBoxRefusal`'s words. `pt/w6-u` read `bindWhy`; `pt/w6-u-fix` reads `why` (then `bindWhy`) — the merged tree needs no change; the wiring pass adds one integration assertion that a non-homed row shows the backend's words |
| `reason: "shared-space"` | `harness_catalog.go` | every catalog entry at a partitioned agent's global instance |
| 409 words | `harness_partition.go` consts | `harnessNotAtGlobal` (/ask, /runs, authenticate, login=1), `harnessCantMove` / `harnessStillWorks` / `harnessStillUp` (+ `harnessUnshare` for an un-share) — publish, export, un-share, hosting; the page keys on the status and shows `error` |
| `runs` on a move's 409 | `writeHarnessMoveErr` | `{error, runs?}`: the coding agents' runs the conversation waits on (at work, or finished but still up) — for the page to offer their Stop |
| a member's leave | `share.go` `handleRemoveMember` | goes, even when it un-shares a coding agent's conversation (it stays at global with its owner) — W6-U's record's owner question 2 ("should its last member be able to leave it?") is answered: yes |
| the host's sandboxes | `sandbox_use.go` → `hostedHarnessRefusal` | a hosted run's sandbox tools refuse a sandbox where a coding agent of the host's signed in or worked (`not-allowed`, words) — W6-U's hosting warning may say so too |
| `harnessSessions` | `usage.go` `usageDay` | per day and in `total` of `GET /usage` |
| `harnessBarred(run)` | `harness_partition.go` | the one predicate for "may a coding agent start/work in this run here" — B5's e2e can assert through it |

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **The agent template: coding agents only in a person's own
  conversations** (the template's API.md "Partitioned instances" → "Coding
  agents only in your own conversations"). In a partitioned agent a coding
  agent (Claude Code, Codex, …) signs in inside its sandbox, so it now
  works only where that sign-in stays its person's: their own
  conversations, in a sandbox homed in their partition. The global instance
  never starts or drives one — `POST /ask` and `POST /runs` with `harness`
  answer 409 there, its catalog lists each one unavailable (`reason:
  "shared-space"`), its agent can't spawn one, and signing one in there
  answers 409 — so a shared conversation, a channel, a trigger or a
  schedule never reaches one. A non-secure (hosted) conversation has none,
  one a coding agent answers can't be hosted, and a hosted conversation
  doesn't work in a sandbox of its host's where a coding agent of theirs
  signed in or worked. A coding agent's conversation — or one where a
  coding agent it started is still at work or still running — can't be
  published, copied, hosted or moved to another space (409 `{error,
  runs}`, the runs it waits on); un-sharing one answers 409, though a
  member may still leave it. In a person's partition only a coding agent
  at work (a turn, or a sign-in the agent waits on) keeps the partition
  running — never one idle or waiting for you: an idle one is stopped at
  its idle time by a wake-up the partition leaves (never an every-minute
  restart; also under a halt); a manager's halt reaches a coding agent's
  turn at its next step, or within a few seconds when it says nothing, and
  one waiting for the settings to be read doesn't start; its terminal,
  sign-in and log are refused for a sandbox not homed there (403) or a
  manager that can't keep people apart (409); `GET /sandboxes` says of each
  sandbox whether it is `homed` there (and `why` not); `GET /usage` adds
  each person's coding-agent sessions (`harnessSessions`). Unpartitioned
  instances change nothing. Nothing to change.
```

(The first version linked [/docs/partitions.md](/docs/partitions.md),
which has no coding-agents section; the link is dropped. If the fold adds
one — B4's AR-23 paragraph would fit there — link it.)

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, W6-A: coding agents only in a person's
  own conversations — the agent backend (2026-10-01).** Implements the
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
      whatever door it came through; a planted adapter found by a takeover
      at global is stopped.
    - **A hosted conversation keeps off its host's coding agents'
      sandboxes**: `sandboxUse` refuses, for a hosted root, a sandbox where
      one of the host's coding agents signed in or worked — the sign-in in
      its `$HOME` stays theirs.
    - **No moves while a coding agent is in it**: `exportConv` — the one
      reader every copy between homes uses — refuses a harness root, and a
      tree whose harness child is at work or still running; a stopped child
      doesn't hold the tree (the bundle carries the root's transcript
      only). The 409 names the runs. Un-sharing one at the global instance
      is refused (409) inside its transaction rather than moved or left
      private-at-global — except a member's own leave, which is theirs:
      the conversation stays there with its owner.
    - **A partition's lifetime follows work, not adapters**: only a coding
      agent at work — a turn, or a sign-in AgTT waits on (bounded) — holds
      the backend up; one idle or waiting on a person (a question, its
      sign-in) doesn't. An idle one leaves one 5-field `wake` at last
      activity + harnessIdleMin (never `@every 1m`, never within the
      minute, also under a halt: stopping an idle adapter moves no work),
      and the woken takeover counts the reclaim from last activity, so it
      stops the adapter at once. A prompt in `sending` on an adapter a
      successor attaches is `resume` work.
    - **The brake by reading, as a built-in turn does**: the harness
      pass's halt branch is `onBrake` (cancel when known, park with the
      release timer when conf isn't read); a live turn looks at the cached
      conf at each event, and one engine timer looks every confTTL while a
      coding agent works, so a silent turn is reached too.
    - **Sandboxes**: the relays check the hello and the home like every
      use; rows say `homed`/`why` in a partition (the page's pick decides
      with them); the catalog keeps and probes only homed ones.
    - **Usage**: adapter sessions counted per day, no content.
  - **Not chosen:** a separate harness check in every route (the engine
    guard would still be needed; the predicate covers both); refusing
    every tree that ever had a harness child (its answer is plain
    transcript once it stopped); moving a harness conversation without its
    session (its sign-in and sandbox are its person's — the owner ruled
    no moves); holding the partition while a coding agent waits for a
    person (C4: a person's wait never keeps a partition up); keeping the
    brake off the idle reclaim (an idle adapter would linger in its
    sandbox until the halt lifted and its person came back); dropping team
    sandboxes from a partition's list (their terminals still open there —
    `homed` says why a conversation can't use one); refusing a member's
    leave (the leave is theirs; the owner's un-share is what moves).
```

## Deviations

1. **The brake looks at every event**, not only durable ones (96 said
   "via durable events or recheckSoon"): a streaming turn's deltas are
   where a long answer spends its time, the look is a cached read, and a
   test can see it (a turn with only chunks is cancelled mid-way). **And
   at a timer while a turn works** (`brakeLook`, review fix): a silent
   turn — a long command — is reached within about two `confTTL` instead
   of at its next event.
2. **The catalog at global answers `shared-space`** for every entry — not
   asked for; it makes the refusal machine-readable for the page and
   native.
3. **`harnessStays`' "still in it"** includes a child parked on a person
   (`waiting_input`) and an idle adapter still up: the person stops it
   (`/cancel`) or its idle stop does, then the copy goes. The words tell
   the two apart and give the idle stop's time; the 409 carries the runs.
4. **A sign-in AgTT started holds a person's partition** (the device
   code's, at most 15 min; an API key's, 30 s) — its outcome, resending
   the held prompt, is this process's memory — while a sign-in park, and a
   successor's attach onto one, rests (review fix; the first version held
   the partition from any sign-in park until the person signed in).
5. **The relative reclaim is a person's partition's only**: unpartitioned
   the takeover re-arms a whole idle time (today's; see Bugs).
6. **`prefs/harness-sandbox`** is frontend-owned (xbind prefs); the
   backend part of A-S3 is `homed`/`why` and the catalog filter.
7. **`userWake` doesn't consult `harnessWorkSQL`** (96 §B2 A-M4's first
   bullet): `harnessWorkSQL` counts any adapter up, so an idle one would be
   `resume` work — the `@every 1m` restart A-M4 exists to prevent. It uses
   `harnessSendingSQL` (a prompt on its way, to an adapter a successor
   attaches) for `resume`, and `harnessIdleWake` for the `wake` at the
   reclaim's minute; `harnessWorkSQL` stays the unpartitioned `hasWork`'s.
8. **The idle reclaim goes on under the brake** in a person's partition
   (review fix): unpartitioned a halted pass reclaims nothing (kept).
9. **A member's own leave of a coding agent's conversation at global goes**
   (review fix), leaving it shared with no one but its owner — the one
   un-share that doesn't move a conversation home.
10. **A hosted conversation's sandbox refusal** (`hostedHarnessRefusal`,
    review fix) goes beyond the plan's A-M2 (which covered the harness,
    not its sign-in in a sandbox the hosted conversation uses).

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
- **Unpartitioned too, a prompt marked `sending` on an adapter a successor
  can't attach** (still opening its session) left the run `running` for
  good (`resumeHarness` stopped the adapter and kept the mark; the pass
  then saw a turn with no session). Not reachable through today's code
  paths (a prompt is marked only once a session is open); the stop branch
  now ends the turn, "send it again".

## Owner questions

1. **A halt and idle adapters.** In a person's partition the reclaim now
   goes on under a halt (an idle adapter stops at its idle time, and the
   partition comes back for it); unpartitioned a halt still leaves idle
   adapters up until it lifts (today's). *Built so;* should a halt also
   stop every idle coding agent at once (a small addition: `haltStop`
   taking idle adapters too)?
2. **A sign-in in progress holds a person's partition** (at most 15
   minutes for a device code) so its outcome — the held prompt resent —
   isn't lost; a sign-in park alone never holds. *Built so;* say if the
   device code's wait should let the partition go too (the person then
   Retries after signing in).
3. **A member leaving a coding agent's conversation at global** leaves it
   with its owner, shared with no one (it can't move home). *Built so* (a
   leave is the member's); the alternative is a 409 telling them the
   owner must delete it.
4. **The host's sandboxes** are refused to a hosted conversation only where
   the agent saw a coding agent sign in or work (a session, a probe); a
   sign-in made by hand in a terminal isn't known. Should hosting refuse
   every sandbox with an unknown sign-in state (stricter: a new sandbox per
   hosted conversation), or is the note enough? AR-23 (B4) should name the
   path either way.

## Tests

- `_backend/harness_partition_test.go` (14): `TestHarnessInPartition`
  (turn, hold let go, wake at the reclaim's minute, usage count, hold
  while working), `TestHarnessHoldUnpartitioned` (control),
  `TestHarnessReclaimAfterWake`, `TestHarnessUserWake` (sending, idle,
  overdue, sleep vs reclaim, parked, login, stopped, reclaim off, a
  `sending` on a gone adapter), `TestHarnessBrakeInPartition` (mid-turn at
  an event; a silent turn by `brakeLook`, no poke),
  `TestHarnessBrakeFailsClosed`, `TestHarnessNotAtGlobal` (/ask, /runs,
  catalog, authenticate, login=1, spawn schema and call, a planted run
  fails without an adapter), `TestHarnessSpawnInPartition` (own vs hosted;
  legacy control incl. `hostedHarnessRefusal`), `TestHarnessStaysHome`
  (publish/export 409; a child at work vs finished-but-up: the words, the
  idle stop's time, `runs`; a stopped one copies),
  `TestHarnessUnshareRefused` (DELETE member and PATCH visibility → 409
  with what to do, no move, members kept; hosting 409; a member's own
  leave goes and it stays with its owner; built-in control moves),
  `TestHarnessRelayInPartition`, `TestHarnessCatalogOldManager`,
  `TestSandboxRowsHomed` (the row's `why` is `partitionBoxRefusal`'s
  words), `TestHarnessUsageMail`.
- `_backend/harness_partition_more_test.go` (10, the review's):
  `TestHarnessSignInRests` (a prompt refused signed out: the hold let go,
  no wake; a device-code sign-in holds; the held prompt's turn, then
  rest), `TestHarnessRestAfterWork` (a stale mark doesn't rest; a queued
  prompt's turn keeps the hold), `TestHarnessReclaimUnderBrake` (conf
  unread at the takeover: the idle adapter is stopped, no wake after),
  `TestHarnessIdleWakeUnderHalt` (a halt leaves only the reclaim's wake),
  `TestHarnessSendingGone` (the takeover ends a stuck `sending`),
  `TestHarnessBrakeReachesChild` (a halt cancels a coding agent a built-in
  conversation spawned, mid-turn), `TestHarnessHostedSandbox` (a hosted
  root refused a sandbox where her coding agent worked or signed in; a
  clean one and her own conversation not; the host's engine refuses a
  coding agent), `TestHarnessHostingLiveChild` (POST /hosted with a child
  at work: 409, `runs`), `TestHarnessPlantedAtGlobalStopped` (a takeover
  at global stops a planted adapter: `hsFailed`, the words, no new
  generation), `TestHarnessRelayRoutesInPartition` (the log and terminal
  routes: 403 for a sandbox not homed here).
- Mutation checks (each fix reverted in a scratch copy, the test that
  should catch it run): the sign-in rest, the signing hold, the mark, the
  reclaim under the brake, the halt's wake, the `sending` SQL, the stuck
  `sending`'s end, `brakeLook`, the hosted refusal, the member's leave,
  the idle words — all caught.
- The partition tests under `-race -count=3/4`: green. The review fixes'
  tests revealed one test-only race (a `setMode` at a test's end while its
  engine still ran — its cleanup runs before the engine settles); the
  checks moved into engine-less tests.
- The legacy golden, `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh
  agent` (vet + every test, `-race`) on the final tree (`30cb09dd`),
  default `/tmp`: **green** (`ok agent/backend 503 s`) — no existing test
  edited but this pack's own, AgTT's harness tests unchanged and passing
  unpartitioned. (Before the review: three runs, the first two with an
  environment's failures — a long `TMPDIR`'s socket path, one load flake
  in `TestSharedAskAtGlobal` — the third green.) Each fix's commit was
  checked on its own tree (vet + every `TestHarness*`). `make fmt-check`:
  clean. Not run here (the wiring pass's, B5): the isolated partition ×
  harness e2e and I1's suites; `hack/tile-check.sh coding-sandbox`
  (nothing of it changed).

## Review fixes (pt/w6-a-fix)

| # | Finding | Resolution |
|---|---|---|
| 1 (medium) | A sign-in park held a person's partition (`rest=false`, no bound): a prompt refused signed out, codex's refused `session/new`. | **Fixed** (`5e9ee37c`): `loginTx` rests the session after its commit, on every road; only a sign-in AgTT is waiting on holds (`signing`, bounded). `TestHarnessSignInRests`. API.md corrected. |
| 2 (medium) | The seam's field: W6-A sends `why`, W6-U read `bindWhy`. | **Resolved on W6-U's side** (`pt/w6-u-fix` 78977711 reads `why`, then `bindWhy`); W6-A keeps the agreed `homed` + `why`. `TestSandboxRowsHomed` asserts the row's `why` is `partitionBoxRefusal`'s exact words; the wiring pass's integration assertion is in §Seams. |
| 3 (medium) | A hosted chat could read the host's coding-agent credentials through a sandbox tool. | **Fixed** (`f724e38e`): `sandboxUse` refuses, for a hosted root, a sandbox where one of the host's coding agents signed in or worked; the hosting note says so. `TestHarnessHostedSandbox`. The unknown case (a sign-in by hand) is owner question 4 and AR-23's. |
| 4 (low) | The idle wake couldn't reclaim under the brake; with conf unread, a restart loop. | **Fixed** (`5e9ee37c`): `harnessIdleUnderBrake` after `onBrake`; the halt still leaves the reclaim's wake. `TestHarnessReclaimUnderBrake`, `TestHarnessIdleWakeUnderHalt`. Owner question 1 narrowed. |
| 5 (low) | The rest flag race: a turn end's rest could land after the next turn's work. | **Fixed** (`5e9ee37c`): a work counter under `s.mu`, the mark taken before the commit (`onTurnEnd`, `endDetached`, `onResolved`). `TestHarnessRestAfterWork`. |
| 6 (low) | A silent turn kept running under a halt until its next event. | **Fixed** (`5e9ee37c`): `brakeLook`, one engine timer while a coding agent works. `TestHarnessBrakeInPartition` no longer pokes. |
| 7 (low) | `harnessSendingSQL` counted a `sending` on a gone adapter: an @every-1m loop. | **Fixed** (`5e9ee37c`): only adapters a successor attaches; `resumeHarness`'s stop branch ends such a turn. `TestHarnessUserWake`, `TestHarnessSendingGone`. |
| 8 (low) | "still at work" for a finished child whose adapter idles; no ids. | **Fixed** (`f724e38e`): at work vs finished-but-running, the idle stop's time, `runs` in the 409. `TestHarnessStaysHome`, `TestHarnessHostingLiveChild`. |
| 9 (low) | A member couldn't leave a coding agent's conversation at global. | **Fixed** (`f724e38e`): a member's own leave goes (it stays with its owner); the owner's un-share 409 says "keep it shared, or delete it". `TestHarnessUnshareRefused`. Owner question 3. |
| 10 (low) | Test gaps (1)–(8); the record said 15 tests, there were 14. | **Added** (1) sign-in rest, (2) queued prompt holds, (3) PATCH visibility, (4) hosting a live child, (5) the host engine's `harnessUse`, (6) a planted adapter stopped at global, (7) the log/terminal routes, (8) the brake on a harness child — 10 new tests (24 in all); counts corrected. |
| 11 (low) | Record/docs gaps: the `harnessWorkSQL` deviation, the changelog's partitions.md link, API.md's hold claim. | **Fixed** (`30cb09dd` and this record): deviation 7; the link dropped (in API.md too — its "§Coding agents" didn't exist); API.md's hold paragraph says what holds and what doesn't. |
