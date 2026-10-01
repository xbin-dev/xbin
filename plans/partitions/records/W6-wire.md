# W6-wire — coding agents under partitions, wired and run end to end

W6's wiring pass ([96-agtt-merge.md](../96-agtt-merge.md) "Shape of W6"
step 3: B5, the records, the fold's texts). Branch `pt/w6-wire` from
`pt/w6-base` (3fdb5ca0, B0's merge of master's AgTT into partitions), the
three packs merged with `--no-ff`: `pt/w6-f-fix` (W6-F, fabric: B1 F-M3 and
F-M4, B4's fabric part), `pt/w6-a-fix` (W6-A, the agent backend: B2) and
`pt/w6-u-fix` (W6-U, the agent frontend: B3). The owner's ruling is
[90-decisions.md](../90-decisions.md) §I15: coding agents (D147's
harnesses) only in a person's own conversations — never at the global
instance, in shared or hosted chats, and a harness conversation doesn't
move between homes.

This pass edits neither `docs/changelog.md` nor `plans/DECISIONS.md`: the
fold's texts are at the end. No new xbind HTTP/WS surface
([/docs/protocol.md](/docs/protocol.md) untouched); the agent template's
own API changes are in its `API.md`.

Commits (oldest first):

| Commit | What |
|---|---|
| `e65c6184` | merge `pt/w6-f-fix` |
| `7a6999c9` | merge `pt/w6-a-fix` |
| `b948fa22` | merge `pt/w6-u-fix` |
| `16b8b0c1` | agent: a person's partition sees the team's sandboxes; its pickers grey them with the backend's words (bug 1, the seam) |
| `a1c7af6d` | agent: a person's partition keeps its wake-up registered while it idles (bug 2) |
| `3c1b296d` | docs: the agent keeps hello's sign-in rule (W6-F's fold item); `plans/agtt-harness.md` §2.2's D-numbers |
| `e3f334e4` | test/isolated: the partition × harness e2e (B5); `TestHarnessLive` opts out of partitions |
| `a5e77f00` | docs: partitions.md — a stopping partition's token goes before its process |
| `da8a3064` | agent: the wake keeper asks the mode only with the gateway on (a test race the legacy golden caught) |
| `04d9a98f` | agent: harness_partition.go cites no plans/ path (bug 4) |
| this | the record |

## The merge

All three merged without a conflict: the packs split fabric (sdk,
sandboxcontract, docs), backend (`_backend/`) and frontend (`model/`, the
views, the JS tests), and the one file all three touched,
`builtin-templates/agent/API.md`, merged hunk by hunk — read through
after: W6-F's four in-line passages (the manager verifies the person in a
person's partition), W6-A's "Coding agents only in your own conversations"
and the halt/resume/usage/sandbox additions, W6-U's "In a partitioned
instance (the UI)" and the relay bullet sit together without contradiction.
The merged tree was green before anything else: `go build ./...`, `go vet
-tags=integration ./test/...`, `make js-test` (635 tests, 634 pass, 1
skipped as on the base), `TILE_TEST_FLAGS="-race -count=1"
hack/tile-check.sh agent coding-sandbox` (agent 510 s, coding-sandbox 5 s).

## Seams wired

1. **W6-A's per-row `homed` / `why` into W6-U's sandbox pick.** The names
   already agreed on the fix branches (`homed` + `why`; W6-U's `homedWhy`
   reads `why`, then `bindWhy`), so the merged code needed no change for
   them to meet — but on a real manager they never met: the team's
   sandboxes weren't listed in a person's partition at all (bug 1). With
   that fixed, the e2e asserts the seam through xbind: alice's `GET
   /sandboxes` in her partition marks her sandbox `homed: true` (no `why`)
   and the team's `homed: false` with `why` the backend's words ("team-box
   isn't a sandbox of your own space …"), and the global instance's rows
   carry neither. W6-U's `test/homes.mjs` and node tests already check the
   page shows `why`.
   - **The built-in agent's picker too** (W6-U's owner question 1, now
     that the rows exist): `model/sandboxes.js` — a row with `homed: false`
     is disabled in the composer's picker with the backend's words, and
     the Sandboxes dialog offers no "Use here" for it. Only a person's
     partition's list carries `homed`, so every other list (unpartitioned,
     the global instance's) is unchanged. `hack/agent-template-
     sandboxes.test.mjs` pins it.
2. **The member's Leave** (W6-U's seam, W6-A's review fix 9): W6-A lets a
   member's own leave go (the conversation stays at the global instance
   with its owner); W6-U's page gates only the owner's Remove and "Only
   you…" (`unshareWhy`), never Leave — they agree. The e2e runs it: the
   owner's un-share of a coding agent's conversation 409 with bob kept,
   bob's own leave 200, the conversation at the global instance with alice
   alone.
3. **The native sandbox relay** (`relaySrc`, W6-U's seam): one rule —
   `GET /sandboxes/{ref}/terminal` opens a sandbox the person may use in
   the list of where they are (a team sandbox from their partition
   included, sandbox_partition.go's header), while a coding agent's own
   terminal, sign-in and log are checked as a use (A-S2: homed only). The
   relay's 404 for a team sandbox in a person's partition was bug 1, not a
   rule; fixed, `relaySrc` needs no home.
4. **W6-F's fold checklist**: the agent keeps sandbox-manager.md's "offer
   the sign-in only in a sandbox homed in the person's partition" on every
   sign-in path — the `login=1` relay and `POST …/authenticate` (API key
   and device code alike) go through `personSandbox` (hello with
   `partitions`, then `partitionBoxRefusal`), and the global instance
   answers 409 before either; a hosted run's host engine refuses a coding
   agent outright. hello's `login` bullet now says so (`3c1b296d`). W6-F's
   four agent API.md passages merged cleanly with W6-A's and W6-U's edits.
   `TestCodingSandboxContract`/`…VM` ran on the merged tree (§Tests).
5. **`plans/agtt-harness.md` §2.2** (96 §B4): the stale line on
   partitions' D-numbers now says partitions' later numbers start at D148
   and that W6's decision amends D147 for partitioned instances.

## The e2e (B5)

`test/isolated/partitions_agent_harness_test.go` +
`…_refusals_test.go`, `TestPartitionsAgentHarness`, on a real `xbind
--isolate` with owner auth: coding-sandbox (namespace sandboxes; hello has
`stdio` and `partitions`) whose image advertises only the fake coding agent
(hack/fakeacp, `--steer --persist --require-login`, copied into the
sandbox), a copy of hack/fakesandbox without `partitions` bound beside it,
llm-gw on hack/fakeopenai, and the agent template as a new instance —
partitioned by default. Cases:

- **A private coding agent in a partition-homed sandbox runs and answers**:
  alice's partition makes the sandbox (owner.partitionId hers), `POST /ask
  {harness: fake}` in her partition (id ≥ 2^40, engine harness), the
  signed-out park, the sign-in in the run's relayed login terminal
  (`?login=1`: `personSandbox` with `partitions` and the home check — A-S2's
  positive path), Retry, the permission park, approve, the answer — the
  adapter's exec split (the manager's stdio socket).
- **Rows, catalog, old manager**: §Seams 1; `GET /harnesses?probe=` hers
  installed, the team's refused (not homed); the catalog keeps hers only;
  `claude` (only the old manager might have it) `manager-error`, `why`
  naming the old manager and `"partitions"`; the old manager refused in
  her partition (`refusal: partitions`, 409 on a create there) and used at
  the global instance; the global instance's catalog every entry
  `shared-space`.
- **Another partition's dial gets 404** (F-S4's agent half): bob's
  partition, carol's, the global instance and another consumer's page
  dialing `…/execs/{id}/stdio`, `…/execs/{id}/tty` and `…/tty` of alice's
  coding agent's sandbox at the manager get 404 before the upgrade; bob's
  `GET /runs/{her id}/harness/log` 404; her next message answered on the
  same adapter generation (nothing took her stdin).
- **The §I15 refusals**: publish and export of her coding agent's
  conversation 409 (`harnessCantMove`); a coding agent in the team's
  sandbox from her partition 403 (`partitionBoxRefusal`); at the global
  instance `POST /ask` with `harness` (the owner token's, and alice's
  shared chat) and the owner token's `POST /runs` 409 (`harnessNotAtGlobal`;
  a person's `POST /runs` there is refused whatever it holds); authenticate
  (owner, alice) and the login terminal 409; the global agent's
  `subagent_spawn {harness: fake}` in a coding class with the team's
  sandbox bound is refused ("harness: coding agents work only …", which
  its model then reports) and no `harness:` exec appears in that sandbox.
  A coding agent's conversation at the global instance — a tree before
  §I15 could hold one, planted through a builder's mail topic
  (`_backend/zz_e2e_plant.go` in the test's instance: `UPDATE runs SET
  engine='harness'` at global) — alice's `POST /hosting` 409, alice's and
  bob's `POST /copy` 409, the owner's un-share (`DELETE …/members/bob`) 409
  with bob kept, bob's own leave 200 (it stays at global with alice).
- **A halt set at global reaches a harness run in a partition**: a silent
  turn (the fake's `stall`) in alice's partition; `PUT /halt {on: true}`
  at global; the run is `canceled` within ≈ 3.2 s (brakeLook) and its
  adapter's exec stopped; lifted, her partition answers 423 until it reads
  conf, then her message is answered (a fresh adapter, still signed in).
- **The user-mode wake**: `harnessIdleMin` 2 at global; after a turn,
  alice's partition, idle with the adapter up, has exactly one cron job,
  `wake` (`CRON_TZ=UTC m h d M *`), never `resume`; `POST
  /partitions/stop` (hers) leaves the adapter running (let go) and the one
  `wake`; nobody touching the tile, xbind's cron starts her partition at
  that minute and its takeover stops the idle adapter — 2 m 0 s – 2 m 31 s
  after the stop across the runs.
- **Not in it**: the usage count (`harnessSessions`) — a partition mails
  the days before today only, so an e2e can't see it; W6-A's
  `TestHarnessUsageMail` is its check.

## Bugs found (and fixed here)

1. **A person's partition never saw the team's sandboxes** (since the
   partitioned agent, B2a: D115's share rule met D140's `shared` marking;
   not W6's code, found by the e2e). The manager
   shows a partition the sandboxes homed at its consumer's own global
   identity marked `shared: true` (sandbox-manager.md §Partitioned
   consumers). The agent's `sandboxAccess` read `shared` as another
   consumer's share and, finding no share for this tile, gave nobody
   access — so they weren't listed, their terminal didn't open (the native
   relay of a shared conversation's sandbox from a partition 404'd), and
   W6-A's `homed:false`/`why` rows for them never reached W6-U's pick. The
   unit tests' fixture (`atGlobal`) always added a share, hiding it; the
   fake manager and coding-sandbox both mark it. **Fix** (`16b8b0c1`):
   `homedAtOwnGlobal` — in a person's partition a sandbox homed at the
   agent's own global identity is used by the person rules as at the
   global instance, never managed or edited from the partition (the
   manager refuses both anyway). `TestTeamSandboxInPartition` (mutation-
   checked: the access fix reverted, and the read-only rule reverted, each
   fails it).
2. **A person's partition's exit left no wake-up** (B2a's resume model ×
   F3's stops; found by the e2e). xbind stops a person's partition — idle
   reap, the person's own stop, a switch, their removal — revoking its
   instance token first (D143), then SIGTERM; the agent registered its
   `resume`/`wake` jobs at the exit (`Engine.Shutdown` → `leaveWakeUp`), so
   those calls got 401 and nothing was left: A-M4's wake for an idle coding
   agent never landed (the adapter ran on until its person came back), and
   a sleeping run's or a subagent deadline's wake was lost the same way.
   The first e2e run showed it (the stopped partition's cron jobs: `[]`).
   **Fix** (`a1c7af6d`, `_backend/resume_keep.go`): in a person's
   partition the owner keeps the jobs registered while it idles — whenever
   its engines let the hold go (the moment xbind may reap it) and once its
   takeover cleared the last owner's jobs — computing what the exit would
   leave (resume_mode.go's rule, plus its hosted conversations' through
   `hostedWakeAt`), deleting a job no longer wanted, never while an engine
   holds or once it shuts down (the successor's takeover owns them then).
   A job firing while it runs is a pass it would make anyway (`/tick` →
   `recover`). The exit still leaves them where it can. Unpartitioned and
   at the global instance nothing runs. `TestKeepWakeUp` (mutation-checked:
   never registering, ignoring the hold, never readied — each fails it);
   end to end in the e2e's wake case. [/docs/partitions.md](/docs/partitions.md)
   §How people's partitions run now says a stopping partition's token goes
   before its process (`a5e77f00`).
3. **`TestHarnessLive` silently tested a partitioned agent**: on this
   branch a new instance under `--isolate` is partitioned by default, and
   AgTT's setup (from master) didn't opt out — unlike
   `consumers_test.go`. It now instantiates with `"partition": false`:
   AgTT's check of an unpartitioned agent, as on master; the partitioned
   one is `TestPartitionsAgentHarness`.
4. **W6-A's `harness_partition.go` cited `plans/`** in an embedded file:
   `make test`'s `TestEmbeddedAssets` refuses that (the packs ran
   tile-check, which doesn't run the root package). Its header now names
   the ruling and D158/D147 (`04d9a98f`).

## Open seams

- **I2 rollout** (96 "When" C): the partitions branch holds back from
  master until W6 folds; then I1's suites on the merged tree (below:
  rerun here) and I2.
- **UI harness `agentHarness` under `HARNESS_ISOLATE`**: not built (96
  §B5 "or record the gap for I1"). Its fake adapter is a host path, which a
  tile sandbox can't start; the isolated e2e above covers the backend
  through xbind, and W6-U's template browser tests cover the page in both
  states. A partitioned `agentHarness` would need the fake inside the
  sandbox (as the e2e copies it) — I1's to decide.
- **Other partitioned tiles and exit-time calls**: bug 2's cause is the
  platform's (revoke first, then stop). The agent no longer depends on
  it; any builder's partitioned backend that registers cron jobs (or mails)
  at SIGTERM loses them the same way — partitions.md now says so. Whether
  xbind should let a *graceful* stop's (the idle reap's, the person's own)
  exit calls land — revoke once the process has exited, keeping revoke-
  first for switches, removals and resets — is an owner question (below).
- **B2d's host engine's own exit** (`hostedWakeUp`) is unchanged and still
  refused after a revoke; the keeper covers what it would leave
  (`hostedWakeAt`), so nothing depends on it.
- **W6-A's four owner questions** (a halt and idle adapters; a device-code
  sign-in holding the partition; a member's leave; the host's sandboxes
  with an unknown sign-in) and **W6-U's** first (answered by building it:
  the built-in picker greys a sandbox not homed there) stand as the
  records give them.

## Owner questions

1. **Revoke-first on a graceful stop.** Every stop of a person's partition
   revokes its token before SIGTERM (D143). For switches, removals and
   resets that is the point; for the idle reap and a person's own stop it
   only takes away the backend's last calls (bug 2). *Built*: the agent
   keeps its wake-up registered while idle, not relying on the exit.
   Should xbind also revoke after the exit for those two (a runner change,
   F3's)?
2. **The team's sandboxes in a person's partition** are now listed there
   (read-only: no manage, no edit; a terminal opens; conversations there
   can't bind them, and the pickers say why). *Built as sandbox_partition.go's
   header and API.md already said*; the alternative is hiding them there
   (then their terminals are reached only from the global instance's own
   page, and the native relay of a shared conversation's team sandbox from
   a partition needs the global home, W6-U's seam 3).

## Tests

(`.dev.mk`'s env exported for integration runs and the harness; the Bash
sandbox off for isolated runs and the harness; `-parallel 3`)

| Run | Result |
|---|---|
| the merged tree before any change: `go build ./...`, `go vet -tags=integration ./test/...`, `make js-test`, `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent coding-sandbox` | PASS; PASS; 635 tests, 634 pass, 1 skipped (as on the base); PASS (agent 510 s, coding-sandbox 5 s) |
| `TestPartitionsAgentHarness` while building it | run 1: the team's row missing (bug 1), the stopped partition's cron jobs `[]` (bug 2), two test mistakes (`POST /runs`, the halt's lift read by the partition a few seconds later); run 2 (bugs fixed): the wake case PASS — the idle adapter stopped 2 m 18 s after the stop — the usage check impossible (today's counts aren't mailed: dropped); run 4: **PASS** (179 s; every case) |
| agent `_backend`: `TestKeepWakeUp`, `TestTeamSandboxInPartition` with `TestLeaveWakeUp*`, `TestPartitionSandboxes`, `TestSandboxRowsHomed`, `TestHarnessInPartition`, `TestHarnessUserWake`, `TestHosted*` — `-race -count=3` (the two new `-count=5`) | PASS; mutations (scratch copies): the access fix reverted, its read-only rule reverted, the keeper never registering, ignoring the hold, never readied — each FAILs its test |
| JS: `hack/agent-template-sandboxes.test.mjs` (the built-in picker and the dialog's Use here for a row not homed) | PASS |
| template browser tests from a scratch copy: sandbox, homes, harness-homes, harness-start, partition | all PASS |
| **I1's suites and every partitions smoke**: `go test -tags=integration -count=1 -parallel 3 -run 'TestPartitions\|TestCodingSandbox\|TestHarness' ./test/isolated/` | **ok, 869 s.** Per group (subtests pass/skip/fail): `TestCodingSandboxContract` 60/6/0 and `…VM` 59/6/0 (skips: user-partitions' declared apart, person, recreated, shares; tty/unsupported, lifecycle/archive — `user-partitions/sockets` PASS in both, W6-F's checklist); `TestCodingSandbox` 12/0/0, `…VM` 12/0/0; `TestCodingSandboxConsumers` 7/0/0, `…VM` 7/0/0; `TestHarnessLive` 7/0/0, `…VM` 7/0/0 (unpartitioned now; the real adapters skipped by design without `XBIN_HARNESS_LIVE=1`, logged); `TestPartitionsAgentHarness` 6/0/0; `TestPartitionsAgent` 10/0/0; `TestPartitionsAgentChannels` 4/0/0; `TestPartitionsBridge` 6/0/0; `TestPartitionsSecurity` 14/0/0; `TestPartitionsSmoke` 19/0/0; `TestPartitionsSmokeMail` 5/0/0; `TestPartitionsSmokeW2` 5/0/0; `TestPartitionsPage`, `TestPartitionsSmokeBackups`, `TestPartitionsTemplateMerge` PASS (no subtests); `TestPartitionsSmokeReap` SKIP (opt-in, `XBIN_SMOKE_REAP=1`: ten idle minutes) |
| the downgrade suite and the non-isolated partitions test (`./test/`, previous release v0.3.61 from `.dev.mk`): `-run 'TestDowngrade\|TestPartitionsNoIsolate'` | ok, 128 s: `TestDowngradePartitions` PASS (93 s), `TestDowngradeStatic` PASS, `TestDowngradeDormantRegistrations` PASS, `TestPartitionsNoIsolate` 7/0/0 |
| UI harness `agentHarness`, unisolated (`PORT=9121`, `HARNESS_DIR=…/w6/wire/h`, `.dev.mk`'s env) | 72 PASS, 0 FAIL, 0 SKIP (the admin console's 18 resource 404s in `shots.log`, as on W6-U's runs); xbind stopped, `ws/` deleted |
| UI harness `agentHomes` + `agentHosted`, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1` (same port and dir) | agentHomes 20 PASS, agentHosted 24 PASS, 0 FAIL, 0 SKIP; xbind stopped, `ws/` deleted |
| `go test ./internal/docscheck ./internal/sizebudget ./internal/builtins ./internal/assetscan` | ok |
| the legacy golden on the final tree: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent coding-sandbox` | first on `a5e77f00`: agent FAIL — 7 tests (`TestHarnessCatalogOldManager`, `TestCopyKeepsTheLedgerAndMasks`, `TestCopyTooLarge`, `TestJoinAtTheSharedSpace`, `TestMailboxPages`, `TestMailboxUnknownTopics`, `TestPartitionSandboxes`) under `-race`, each the takeover's goroutine reading the run mode in `wakeKeepReady` while the test's `setMode` cleanup switched it — a test-only race (production sets the mode once), fixed by checking `noGateway` first (`da8a3064`); then **PASS** (agent 512 s, coding-sandbox 5 s). The e2e, I1's suites, the downgrade suite and the harness passes ran before `da8a3064`: it only reorders a guard (the gateway before the mode), which production takes the same way |
| `make js-test` on the final tree | 635 tests, 634 pass, 1 skipped |
| `make check` on the final tree | first on `da8a3064`: `TestEmbeddedAssets` FAIL (bug 4) and `TestSSHClientLeaves` (sandbox-terminal, untouched by W6) timed out under the full run's load — alone `-count=3` it passes; after `04d9a98f`: **green** (`>> make check: green`) |

## Changelog entry (docs/changelog.md, under the fold's date)

Three bullets. The fabric one is W6-F's, taken **verbatim** from
records/W6-F.md "## Changelog entry" (not repeated here), with **its two
corrections** to existing 2026-09-30 lines (same place). The two below:
the agent's is one bullet for W6-A, W6-U and this pass — it replaces the
entries in records/W6-A.md and records/W6-U.md (their texts describe the
same feature from the backend and the page); the partitions one is this
pass's.

```markdown
- **The agent template: coding agents only in your own conversations**
  (the template's API.md "Partitioned instances" → "Coding agents only in
  your own conversations", and "Coding agents" → "In a partitioned
  instance (the UI)"). In a partitioned agent a coding agent (Claude Code,
  Codex, Gemini CLI, opencode) signs in inside its sandbox, so it works
  only where that sign-in stays its person's: their own conversations, in
  a sandbox homed in their partition.
  - The global instance never starts, drives or signs one in: `POST /ask`
    and `POST /runs` with `harness`, `POST /runs/{id}/harness/authenticate`
    and the run terminal's `login=1` answer 409 there, its catalog lists
    each coding agent unavailable (`reason: "shared-space"`), its agent's
    `subagent_spawn` has no `harness` — so a shared conversation, a
    channel, a trigger or a schedule never reaches one. Its page offers no
    "Who answers", and a new chat shared with others is the built-in
    agent's.
  - A non-secure (hosted) conversation has none, one a coding agent
    answers can't be hosted, and a hosted conversation doesn't work in a
    sandbox of its host's where a coding agent of theirs signed in or
    worked.
  - A coding agent's conversation — or one where a coding agent it started
    is still at work or still running — can't be published, copied, hosted
    or moved to another space (409 `{error, runs}`, the runs it waits on);
    the page offers none of those on one. Its owner's un-share of one at
    the shared instance answers 409 (it would move it); a member may still
    leave it. One already in the shared space (from before this rule) is
    read, not driven: no Retry, no message, its mode and options not
    switched.
  - In your own partition a coding agent starts only in a sandbox of your
    own space: `GET /sandboxes` says of each sandbox there whether it is
    `homed` (and `why` not), the catalog keeps only yours, the pickers show
    the team's sandboxes as not fitting with the reason — the built-in
    agent's too — and offer Create; its terminal, sign-in and log are
    refused for a sandbox not homed there (403) or a manager that can't
    keep people apart (409). A sign-in is offered only there; elsewhere its
    card says why. A shared conversation's coding agent is reached at the
    shared instance (its buttons, the app's terminal).
  - Only a coding agent at work (a turn, or a sign-in the agent waits on)
    keeps your partition running — never one idle or waiting for you. An
    idle one is stopped at its idle time: while your partition idles it
    keeps a `wake` cron job registered for that minute (never an
    every-minute `resume`), which brings a stopped partition back to stop
    it, also under a halt. A manager's halt reaches a coding agent's turn
    at its next step, or within a few seconds when it says nothing, and
    one waiting for the settings to be read doesn't start. `GET /usage`
    adds each person's coding-agent sessions (`harnessSessions`).
  - Fixed: a person's partition now lists the team's sandboxes (those it
    may use at the shared instance, read-only there: their terminal opens,
    conversations there don't use them) — they were missing, and a shared
    conversation's sandbox terminal from your own partition didn't open;
    and a person's partition's `resume`/`wake` jobs are kept registered
    while it idles rather than left at its exit, which xbind refused (its
    token is revoked before it stops), so a sleeping run's wake-up or a
    coding agent's idle stop was lost when the partition stopped.

  Unpartitioned instances change nothing. Nothing to change.
- **Partitioned tiles: a stopping partition's token goes before its
  process** ([partitions.md](/docs/partitions.md) §How people's partitions
  run). Said now, as it always was: every stop of a person's partition —
  idle, its person's own, a switch, their removal — revokes its instance
  token first, so its backend's calls to xbind while it exits (a cron job
  registered at SIGTERM) are refused; register what should bring it back
  while it runs. Nothing to change unless your partitioned backend does
  that at its exit.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

One entry for W6 (96 §B4: "coding agents under partitions"), taking in
W6-A's and W6-U's decision texts (records/W6-A.md, records/W6-U.md) and
W6-F's two bullets (records/W6-F.md "## Decision entry"). The number is
the fold's: the next free one on the merged tree (master's
go-build-isolation fix holds one; partitions' last is D171).

```markdown
- **D<next> — Coding agents under partitions: only in a person's own
  conversations (2026-10-01).** The owner's ruling
  plans/partitions/90-decisions.md §I15 built into the agent template and
  the sandbox contract (plans/partitions/96-agtt-merge.md, W6: records
  W6-F, W6-A, W6-U, W6-wire); amends D147 (coding agents) for partitioned
  instances (D158). The template's API.md "Partitioned instances" →
  "Coding agents only in your own conversations" and "Coding agents" →
  "In a partitioned instance (the UI)"; docs/sandbox-manager.md
  §Partitioned consumers, §Terminals, §stdio, §hello.
  - **Chosen.**
    - **A coding agent works only where its sign-in stays its person's**:
      a conversation of their own partition, in a sandbox homed there (a
      sign-in lives in the sandbox's `$HOME`). One predicate,
      `harnessBarred(run)` (the global instance, or a hosted run), checked
      at every door (`/ask`, `/runs`, the spawn tool's schema and call,
      sign-in and the login terminal, the catalog: `shared-space`) and in
      the engine (`harnessUse`: every spawn, attach and re-check), so no
      route or run — a channel's, a trigger's, a schedule's, a planted one
      — reaches an adapter at the global instance.
    - **No hosted coding agent**: a hosted run spawns none, one a coding
      agent answers can't be hosted, and a hosted conversation keeps off
      its host's sandboxes where a coding agent of theirs signed in or
      worked.
    - **No moves**: `exportConv`, which every copy between homes uses,
      refuses a coding agent's root and a tree whose coding agent is at
      work or still up (409 `{error, runs}`); the owner's un-share is
      refused inside its transaction; a member's own leave goes (it stays
      at the global instance with its owner).
    - **A partition's lifetime follows work**: only a coding agent at work
      (or a sign-in the agent awaits) holds a person's partition; an idle
      one leaves one `wake` at its idle stop, and the woken takeover counts
      from its last activity. **The wake-up is kept registered while the
      partition idles** — not only left at its exit — because xbind
      revokes a stopping partition's token before its process stops
      (D143): the exit's cron calls were refused (the e2e's bug), for every
      kind of wake-up of the partition, not only coding agents'.
    - **The brake by reading**, as a built-in turn does: the harness
      pass's halt branch is `onBrake`, a live turn looks at the cached conf
      at each event, and one engine timer looks every confTTL while a
      coding agent works.
    - **Sandboxes by home**: the relays (terminal, sign-in, log) check the
      hello and the home like every use; `GET /sandboxes` in a partition
      says `homed`/`why` and the pickers — the coding agent's and the
      built-in agent's — grey what isn't; the catalog keeps and probes
      homed ones only; the team's sandboxes are listed in a person's
      partition (the manager shows them `shared`), read-only there.
    - **The page follows one pure model file** (`model/harness-homes.js`):
      where one starts, whose sandbox fits, where a sign-in is offered
      (read-only elsewhere), a shared new chat's "Who answers" (the
      built-in agent), a shared-space run read not driven, no share-a-copy,
      copy or hosting on a coding agent's root; its calls follow the run's
      home.
    - **The contract, unchanged in rule** (W6-F): from a partitioned
      consumer's user partition the person is the partition's and verified
      on every route, the `tty` and `stdio` sockets included (D140);
      D147's "asserted, the consumer's to check" is an unpartitioned
      consumer's, or a global instance's. The consumer is the pair
      (`X-XBin-From`, partition id). `sdk/sandboxcontract` checks it
      (`user-partitions/sockets`, `apart`).
    - **AR-23 grows** (W6-F): partitions that see one sandbox share its
      stdio sockets (an attach takes over a coding agent's stdin) and its
      `$HOME` (a sign-in serves whoever runs the agent there, and its
      clones); a partitioned consumer should offer sign-ins only in
      sandboxes homed in the person's partition — the agent does — and
      whoever sets an image's `harnesses` commands is in its users' trust
      base.
    - **Usage**: coding agents' sessions counted per day, no content.
  - **Not chosen:** a harness check in every route alone (the engine guard
    is still needed; one predicate covers both); emptying the catalog at
    the global instance (its managers set coding agents and classes up
    there); moving a coding agent's conversation without its session (its
    sign-in and sandbox are its person's); refusing every tree that ever
    had a coding agent (a stopped one's answer is plain transcript);
    holding a partition while a coding agent waits for its person (C4);
    keeping the brake off the idle reclaim; refusing a member's leave;
    hiding the team's sandboxes in a person's partition (their terminals
    open there; `homed` says why a conversation can't use one); relying on
    the partition's exit for its wake-up (refused after the revoke), or
    changing the runner to revoke after a graceful stop's exit (the
    agent's own fix needs no platform change; the owner's question);
    keying `prefs/harness-sandbox` by home (a remembered sandbox that
    isn't the person's own doesn't fit, and is replaced).
```

## Amendment line for D147 (plans/DECISIONS.md, at the end of D147's entry)

```markdown
  *Amended 2026-10-01 (D<next>): in a partitioned agent (D158) coding
  agents work only in a person's own conversations, in a sandbox homed in
  their partition — the global instance, shared and hosted conversations
  never start, drive or sign one in, and a coding agent's conversation
  never moves between homes (plans/partitions/90-decisions.md §I15); from
  a person's partition the relays' `Sbx-User` is the partition's verified
  person (D140), not asserted.*
```
