# E — agent events and polling

> Status: live — branch `wp/ps-e` (from `projects-scm` at `7dda955d`).

## What was built

- **The intake** — `B/scm_events.go` (projects-scm §11.1, §7.2's E row):
  `POST /adapter/scm/event`, registered in `adapterRouteTables` (mounted
  by `adapterRoutes` behind `adapterGuard`, the channel role). The caller
  must be a provider in `scmBound()` — `X-XBin-From`, with `#<deployment>`
  for a non-primary one — calling as itself: a call acting in a person's
  partition of the provider (`X-XBin-Partition: user:…`) or carrying a
  person (`X-XBin-User`, `ViewedBy`) is 403, as is any other channel-role
  tile. Body ≤ 1 MiB (413 over), `protocol` 1 (else 400 `protocol` with
  `protocols`), `eventId` and `kind` required (400); the body's
  `scm.provider` is replaced by the caller. Transport dedupe on `eventId`
  in `scm_seen` (7 days): a repeat is 200 `{taken, duplicate}`. `for:
  global` is handled where it arrives; `for: user:<id>` at a partitioned
  agent's global is handed off; unpartitioned it is 404, and a person's
  partition answers every delivery 404.
- **The hand-off** — `B/scm_handoff.go`, `B/handoff.go` (the
  `topicSCM = "handoff/scm"` const), `B/handoff_send.go` (one line: the
  kind → topic) (§11.1): at global, in the transaction that took it, a
  handoff of kind `scm` (`queueHandoff`, as `handEvent` does) carrying the
  event, mailed after the commit with the provider as the mail's source;
  nothing of it kept once mailed. The partition's handler (`mailHandlers`
  from `init()`) takes it only from `global`, only when `for` is its own
  partition key, only when `forPid` equals `partitionID()` (fail closed: an
  unknown partition id or an empty `forPid` drops it), only from a provider
  bound there; drops are counted (`scmDrops`); it dedupes on `eventId`
  again.
- **Handling** — `scmTake`: the delivery time for the repo and the sha
  (`scm_seen` keys `dlv:…`), `scmEventHooks` (V's) for every kind, then —
  but for the progress kinds — routing.
- **Routing** — `B/scm_router.go` (§11.3): `project_refs` matched by PR,
  then branch, then sha, in the provider and the (lower-cased) repo; the
  project active and the provider's, the task's run alive. Per kind:
  `checks.completed` on the current head schedules a read of the head's
  checks after `ci.delaySec` (a superseded sha is ignored);
  `review`/`comment` on the task's open PR schedule a read of its timeline
  after `reviews.batchSec` (coalesced: a read already asked for keeps its
  time); `pull.opened/reopened` of the task's branch not held open →
  `projectRefsCheck`; `pull.merged/closed` → the PR's state, and with
  none open the phase `merged`/`closed`, a scrub job (the task's fork,
  else the project's when no other task is open), a cleanup job per
  `policy.cleanup`, a waking `merged`/`closed` event (a merged PR stays
  merged); `synchronize` and `push` move the head (PR head, checks unknown
  again, `projectRefsHooks` run); a `push` by anyone else queues a note
  (`[scm: <login> pushed <sha7> to <branch> — not this task] … pull before
  pushing.`, dedupe `push:<n>:<sha>`) and a quiet `push` event; a deleted
  branch (all-zero sha) moves nothing; `issue.opened`/`labeled` → a quiet
  `issue` event (title clipped, redacted, defused, marked untrusted), waking
  when the issue carries `policy.autoLabel` — routed by the event's `subs`
  key `issues:<pid>:<slug>`, else every active project of the repo with an
  `autoLabel`. `actor.self` and the task's own login (`project_creds.login`)
  are ignored for pushes' notes, comments, reviews and `pull.opened`; not
  for head moves, merges/closes or CI.
- **CI** — `B/scm_ci.go` (§11.3's first two rows): a read of the head's
  checks (event-asked or polled) is acted on per failing suite, once
  (`sem:<pid>:<n>:checks:<sha>:<suite>:failure` in `scm_seen`). With
  `ci.autoFix` and under `ci.maxPerDay` (`project_tasks.ci_fixes`
  `{day, n}`, UTC): one task input — `[scm: CI failed on <branch>@<sha7> —
  untrusted output]`, up to 3 failing jobs (failing step, the log's last
  120 lines via `GET /scm/checks/jobs/{id}/log`, ANSI stripped, redacted;
  409 `in-progress` → steps only), then failing checks without a job
  (title, summary) and statuses, ≤ `ci.logBytes`, through P1's
  `untrusted()` frame, `— fix it and push.` — queued (`source: event`,
  `hold_park`, dedupe `ci:<n>:<sha>:<suite>`) with a quiet `ci.failed`
  event; past the cap a waking `ci.stuck` (C's push); without autoFix the
  `ci.failed` event only. Green on the head with the PR open: the PR's
  `checks` `success` (the task awaits review) and a waking `pr.ready`
  (C's push). Pending: `checks` `pending`.
- **Reviews** — `scmApplyComments` (§11.3 rows 3–4): the PR's timeline,
  each entry once (`sem:…:review:<pr>:<id>:<state>`), the task's own and
  the provider's skipped, approvals and dismissals not forwarded;
  `OWNER`/`MEMBER`/`COLLABORATOR` or `reviews.allow` (`forward: all`
  everyone, `off` nobody) → one task input (author, `review, changes
  requested` / `path:line`, ≤ 8 KiB, untrusted frame) and a quiet `review`
  event; anyone else → a quiet `comment` event "not forwarded".
- **Polling** — `B/scm_poll.go` (§11.5): `scm_poll` rows per task, repo
  and kind (`checks` watching the head, `pull` and `comments` while a PR is
  open), made and moved by the refs hook and by events (`scmNudge`). The
  cadence (2 min to 20 min, 10 min to 2 h, 30 min to 24 h, then stop — a
  waking `note` "lost track of CI — check manually" for checks still
  pending; healthy events: a first read at 30 min, then every 15; never
  below `events.pollMinMs`; checks done stop until the head moves; checks
  `none` for 30 min stop quietly). One `POST /scm/poll` per provider and
  identity per pass (≤ `limits.pollItems`; each route's read when the
  provider has no `poll`), `retryAfterMs` honoured, every update guarded so
  a row moved meanwhile (a new head, an event's read) wins. The pass runs
  in an `ownerLoops` entry, `scmLoop` (at the next due time, on a kick, at
  least every 10 min; `scm_seen` pruned hourly). `userWake` gains the next
  read's (or subscription's) time — `B/resume_mode.go`, three lines after
  P1's project term.
- **Subscriptions** — `B/scm_subs.go` (§11.2): the `projectRefsHooks`
  entry `scmRefsChanged` rewrites the task's `project_refs` (branch, each
  PR number and head, the pushed sha), wants `task:<pid>:<n>:<slug>` once
  the branch is on the remote or a PR exists (`{repo, branches, prs,
  kinds: pull, checks, comment, review, push, workflow, job, check}`), and
  keeps the poll rows. `scm_subs` holds what this home wants; the loop
  posts (`POST /scm/subscriptions`, same key replaces), re-posts at 25
  days, deletes when unwanted (task cleaned, conversation deleted, no
  longer open when due, issue subscription no longer wanted). Issue
  subscriptions `issues:<pid>:<slug>` (`issues: true`, kinds `[issue]`)
  follow `policy.autoLabel`; team definitions subscribe to nothing. A
  provider answering 501 is asked again after 6 h; other failures back
  off 1 min doubling to 1 h. The `taskChangedHooks` entry drops the reads
  of a task that is no longer open and the refs and subscriptions of one
  cleaned up or deleted.
- **Tables** (§6.10) — `project_refs`, `scm_poll`, `scm_seen`, `scm_subs`
  in `schemaAdds` (`addSCMEventSchema`), all `CREATE … IF NOT EXISTS`.
- **Docs** — `docs/agent-inbox.md` "scm events" (the route, the body, the
  caller check, `for`, the partition hand-off); the agent template's
  `API.md` §scm events and polling (§11.6, §17.1).
- **Tests** — `scm_events_test.go` (the fixture: a project and tasks in
  the store, K's fake provider behind a wrapper whose hello says whether
  events are healthy, the agent halted so inputs stay queued and no job
  runs, the loop off, `scmClockAt` the time), `scm_router_test.go`,
  `scm_poll_test.go`, `scm_subs_test.go`: every §15.2 E test, and the
  schema's migrate-twice and old-database tests.

## Seams for others

- `scmEventHooks` run in the home that owns an event (never at global for
  a `for: user:<id>`, which is handed off first), for every kind, progress
  included, before routing, inside the transaction that dedupes it.
- `projectRefsHooks`: E's entry reads `k.PRs` and the checkouts'
  `remote_sha` as given; E itself runs the whole list when an event moves
  a task's PR head (`scmSetHead`), so V's task watches follow pushes and
  `synchronize`.
- `projectJobKinds[pjPoll]`, `[pjSubscribe]`: registered; a queued one
  only wakes E's loop (`scmKickJob`) — nothing needs to queue them.
- Task state E writes: `project_tasks.prs` (`state`, `headSha`,
  `checks`, `checksAt`, `draft`), `phase` (`merged`/`closed`), `ci_fixes`;
  each followed by `onTaskChange` (`prs` or `phase`). Events E writes:
  `ci.failed`, `ci.stuck` (wake), `pr.ready` (wake), `review`, `comment`,
  `merged`/`closed` (wake), `push`, `issue` (wake with autoLabel), `note`
  ("lost track of CI", wake) — C's `projectEventHooks` make the
  `ci-stuck`/`pr-ready` pushes.
- Jobs E queues: `refs` (through `projectRefsCheck`), `scrub` (task 0 or
  the fork task), `cleanup` (P2 adds its fork rules inside it).
- Test helpers: `newEvFx(t, mode, policy)`, `addTask`, `ev`, `deliver`,
  `pass`, `advance`, `inputs`, `events`, `jobs`, `pollRow`, `failingChecks`;
  `scmLoopOff` and `scmClockAt` (atomics).

## Changelog text

- agent template: **scm events.** A bound scm provider (bind its `agents`
  slot to the agent too: `bx bind apps/scm-github agents+=apps/agent`)
  delivers its events to `POST /adapter/scm/event` (docs/agent-inbox.md
  §scm events) — only a provider bound in the agent's `scm` slot, from
  its own global instance; in a partitioned agent a person's events go on
  to their partition by partition mail. Each task keeps a subscription to
  its branch and PRs once it pushed: failing CI on its head comes back to
  the task as one input (the failing jobs' steps and log tails, redacted,
  marked untrusted; at most 5 a day, then the coordinator is woken), green
  CI marks it awaiting review, review comments from the repo's owners,
  members and collaborators are forwarded (others only noted), a merge or
  close ends the task (credentials scrubbed, workspace cleaned up as the
  policy says), someone else's push is a note to pull first, and issues
  with the project's `autoLabel` wake its coordinator. When events don't
  arrive the agent polls (every 2 minutes at first, easing off to every
  30, for a day); with healthy events only a 15-minute safety read. API.md
  §scm events and polling.

## Decision text

- One path for events and polls: an event that needs a read (CI done,
  a review) only schedules the read polling makes — after
  `policy.ci.delaySec` / `reviews.batchSec`, coalesced — and the read is
  acted on with semantic keys. Acting on the event's own `data` would
  double the code and still need the read (the logs, all suites, the whole
  timeline). Chosen over two handlers with a shared dedupe.
- Polling and subscription traffic run in an owner loop, not as
  `poll`/`subscribe` project jobs: a waiting job holds the engine and
  counts as work (the process would stay up for a day of polling), jobs
  give up after 3 h, and a task's failed job fails its workspace. The
  loop never holds the engine; a person's partition at rest comes back
  through `userWake`.
- The caller of `/adapter/scm/event` must be the provider itself, at its
  global instance: a person's frame or terminal in the provider's
  partition calls as the provider (docs/partitions.md), and would
  otherwise forge events into anyone's tasks. A person's partition drops
  an event unless its `forPid` is the partition's own id (unknown: drop).
- CI is acted on per failing suite on a head (a second suite failing on
  the same head is a new input with only its failures); CI and merges are
  facts whoever caused them — the "own identity" rule (§11.3) applies to
  actions (pushes, comments, reviews, opening a PR), since in a person's
  partition the task's identity is the person, who pushes the commit CI
  runs on and merges their own PR.
- A merged PR's scrub leaves the project's credential while another of
  its tasks is open (scrubbing it would fail that task's next push); a
  big task's fork is scrubbed at once.
- `scm_subs` (a fourth table) because a subscription must be re-posted at
  25 days and deleted at cleanup, which needs its provider id, its body
  and its times; deletion works without the project (a deleted project's
  rows are dropped through its tasks' deletion).
- Owner rulings relied on: V14 (team definitions subscribe to nothing),
  the frozen event v1 and poll shapes (§4.10, §4.11).

## Deviations

- §16.2: a new file beyond the table, `B/scm_ci.go` (the CI read's
  handling: the 800-line rule), and E's tests in four `_test.go` files.
- §16.2: `B/handoff_send.go` gains one line (`handoffMail` maps the `scm`
  kind to `handoff/scm`; the kind → topic map is there, not in
  handoff.go).
- §6.10: a fourth table, `scm_subs` (the subscriptions this home keeps:
  key, provider, body, provider id, state, next/posted/expiry times);
  `scm_poll` gains `nudge` (an event asked for this read) and its `item`
  also keeps the last checks `state`; `scm_seen` also keeps `dlv:` keys
  (when a repo, and a sha, last had a delivery: healthy events).
- §11.2, §14.2: the subscribe and poll work runs in an `ownerLoops` entry
  (`scmLoop`), not as `subscribe`/`poll` project jobs (above); the two
  kinds are registered and only wake the loop.
- §11.3: `checks.completed` and `review`/`comment` don't act on the
  event's data: they schedule the read polling makes (after `delaySec` /
  `batchSec`), which acts with the semantic dedupe.
- §11.3: the "actor.self or the task's own identity → ignored" row
  doesn't apply to `checks.completed` (its actor is whoever pushed) nor
  to `pull.merged/closed` (a person merging their own PR in their
  partition, where the task's identity is theirs).
- §11.3: the semantic key of a CI failure is per failing suite on the
  head; the input carries only the suites not acted on yet.
- §11.3: `pull.merged/closed` ends the task (phase, scrub, cleanup, the
  waking event's text says which PR) only when none of its PRs is still
  open; a merged PR is never set closed by a later event. The scrub is the
  task's fork, else the project's only when no other task of the project
  is open (`phase` open or pr). E queues the cleanup per `policy.cleanup`.
- §11.3: issue events go to the projects named by the event's `subs`
  (`issues:<pid>:<slug>`), else to every active project of the repo with an
  `autoLabel`; a `labeled` with another label does nothing.
- §11.5: only a checks row still pending ends with the waking "lost track
  of CI" note; pull and timeline rows stop quietly at 24 h, checks nobody
  reports (`none` for 30 min) stop quietly; with healthy events reads also
  stop at 24 h; `events.pollMinMs` is a floor.
- §11.5: `POST /tick` makes no pass of its own (`handlers.go` isn't E's):
  a running owner's loop is timed to the next due read, and the tick that
  starts a stopped process starts the loop, whose first pass is
  immediate. At the global instance and unpartitioned, `leaveWakeUp` is
  unchanged: due reads leave no resume job (a delivery, or the next start,
  brings the process back).
- §11.1: the caller check also refuses a call acting in a person's
  partition of the provider or carrying a person; a body over 1 MiB is
  413; a person's partition answers every delivery 404; it drops an event
  whose `forPid` is empty or whose own partition id is unknown.

## Tests run / not run

All Go runs below are in a scratch copy of the agent backend built as
`hack/tile-check.sh` builds it (go.mod.tile, go.sum, a go.work with the
sdk replaced), with `TMPDIR=/work/tmp-tests/e`; the last one is
`hack/tile-check.sh` itself.

| Command | Result |
|---|---|
| `go test -count=1 -run 'TestScmEvent\|TestHandoffScmToPartition\|TestScmEventsSchema\|TestRoutingTable\|TestCIFailureInputCapped\|TestSupersededShaIgnored\|TestReviewAssociationFilter\|TestReviewBatching\|TestOwnIdentityIgnored\|TestPollCadence\|TestPollWebhookSemanticDedupe\|TestPollDueInUserWake\|TestSubscriptionLifecycle' ./backend` | ok (5.2 s) |
| the same with `-race` (once, 114 s) | one FAIL, `TestRoutingTable/pull_merged` — a fix my own mutation script had reverted (`git checkout` of an edited file), re-applied and committed (`16efa1b3`); no DATA RACE reported |
| mutation checks (each change made, the named test run, the file restored): the caller's partition check, the `forPid` check, the transport dedupe, the in-transaction semantic dedupe, the superseded check, the own-identity login, the trusted filter, the review coalescing, the daily cap | each named test FAILs with the mutation |
| `go test -count=1 -v -run 'TestLinkedDMHandoff\|TestHandoffRefused\|TestTriggerRegistry\|TestPrivateTriggerInPartition\|TestHandoffPerPersonBackoff\|TestHandoffsWaitWakeGlobal\|TestKeepWakeUp\|TestUserModeWake\|TestLeaveWakeUpByMode\|TestRepliesWaitWakePartition\|TestProjectSeamsRegistration\|TestAdapterRouteTables\|TestFeatureSchemasNotOnTeam\|TestRefsJobFiresHooks\|TestWorktreeFlow\|TestDeleteRunMarksTask\|TestHostedRunReachesNoHook\|TestProjectArchive\|TestSeededTokenNeverStoredTask\|TestProjectSchemaOldDB' ./backend` (wave 1's tests near E's edits and hooks) | ok, 20 passes (16.7 s) |
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok |
| `go test -race -count=1 -v -run 'TestHandoffScmToPartition\|TestScmEventDedupe\|TestScmEventForPidMismatchDropped\|TestSubscriptionLifecycle\|TestPollDueInUserWake\|TestRoutingTable' ./backend` (after the last code commit) | ok, 6 passes (67.6 s), no DATA RACE |
| `TILE_TEST_FLAGS="-count=1 -v -run TestScmEventCallerMustBeProvider\|TestScmEventDedupe\|TestHandoffScmToPartition\|TestScmEventForPidMismatchDropped\|TestRoutingTable\|TestCIFailureInputCapped\|TestSupersededShaIgnored\|TestReviewAssociationFilter\|TestReviewBatching\|TestOwnIdentityIgnored\|TestPollCadence\|TestPollWebhookSemanticDedupe\|TestPollDueInUserWake\|TestSubscriptionLifecycle\|TestScmEventsSchemaMigratesTwice\|TestScmEventsSchemaOldDB" hack/tile-check.sh agent` (after the last code commit) | vet and the 16 tests ok (`-v` lists 16 PASS; 49 s) — ✓ agent |

Full `-race` suite and `make check`: at the gate (lead). Not run here
(projects-scm §15.4): the partitioned end-to-end tests and a live GitHub
App (webhooks really delivered by scm-github's G2, which is built in
parallel: E is built to §4's event contract against K's fake provider).

## Merge risks

- `B/resume_mode.go` `userWake`: E's three lines follow P1's project term
  (`projAt`); a branch that rewrites that block conflicts there.
- `B/handoff_send.go` `handoffMail`: one `if` before the `event` kind's.
- `B/handoff.go`: the topic const in the `const` block of topics.
- E's `ownerLoops` entry runs in every engine that starts the project
  worker; with no `scm_poll`/`scm_subs` rows it makes one cheap query a
  pass (at least every 10 min).
- With G2 merged: G2 must deliver through the provider's global instance
  without a person on the call, with `for`/`forPid` as §4.10 says, and with
  `subs` keys as posted — E routes issue events by them.
- With V merged: V's `scmEventHooks` entry runs in E's transaction; V's
  `projectRefsHooks` entry also runs when E moves a PR head.
- With P2 merged: P2's cleanup rules run in the `cleanup` job E queues on
  a merge or close; P2's own merge handling, if any, finds the job
  already live (deduped).

## Commits

| Commit | Subject |
|---|---|
| `8d489455` | agent template: scm events and polling — intake, hand-off, routing, polls, subscriptions |
| `16efa1b3` | agent template: scm events and polling — E's tests, an atomic clock, a merged PR stays merged |
| `0d9ca303` | docs: scm events — agent-inbox's route and hand-off, API.md §scm events and polling |
| `3ef20cb0` | agent template: scm events — a deleted branch moves no head; an unwanted subscription is marked before it is deleted |

## Owner questions

- In a person's partition a task works as that person, so their own
  review comments and pushes on the task's PR carry the task's identity
  and are ignored (§11.3's rule, which keeps a task from answering
  itself). Should a person's own review comments be forwarded to their
  task? Built meanwhile: ignored, as the spec says.
- The intake refuses scm events from a person's partition of the provider
  and from calls carrying a person (fail closed: they would let a person
  forge events into others' tasks). A provider that delivers from a
  partition would need another channel. Built meanwhile: refused.
