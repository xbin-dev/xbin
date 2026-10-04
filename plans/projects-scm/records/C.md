# C — the coordinator

> Status: live — branch `wp/ps-c` (from `projects-scm` at `7dda955d`).

## What was built

- **The coordinator and its route** — `B/projects_coord_resolve.go`
  (projects-scm §10.1, §7.2 C rows): `POST /projects/{pid}/coordinator
  {text?}` (participant, a person, not view-as) makes the caller's
  coordinator on first use — origin `project`, `session_key`
  `proj:<pid>:coord:<user>`, owner the person, private, `Config.Project
  {id, role: coordinator}`, the built-in `web` class (409 `class-internal`
  when that class has internal reach), `Deny` `schedule`, `unschedule`,
  `skill_manage` and — unless `policy.coordinator.web` — `web_search`,
  `web_fetch`; `policy.coordinator.model` as its pick. One per person per
  project, found again in the same transaction; a team definition 409.
- **The resolver** (§10.2): `(*Agent).coordinatorOf(t, run, cfg)` — depth
  0, the role through `projectRefOf`, the session key naming that project
  and the run's owner, the project active, the owner still a participant,
  its class without `tsInternal`; `(*DB).projectTaskOf(p, n)` — by the
  project-local number only, refusing a task whose run's stored class (or
  lane's built-in) has internal reach ("a different capability lane").
- **The tools** — `B/projects_coord_tools.go`, `B/projects_coord_scm.go`
  (§10.3): `task_create` (tasks or issues, `note`; through P1's
  `createTask` with `From` the coordinator), `task_list` (state, words,
  paging; `scope: team` reads a membership's team board at global through
  `callGlobal`), `task_status` (P1's `TaskView` and `childDigest`, framed),
  `task_message` (P1's queue, `source: coordinator`, `hold_park=1`, under
  `e.fenced`), `task_result` (paged, framed), `task_cancel` (P1's
  `cancelTask`), `scm_pr` (the pull request, its reviews and comments, its
  checks aggregated with each failing job's failing step and the end of its
  log: ≤ 2 KiB each, ≤ 8 KiB in all, ANSI stripped, redacted, framed) and
  `scm_issues` (in full by number, or a list). A `repo` must be one of the
  project's (a tool error otherwise, at every home; the provider is never
  asked). Offered at depth 0 to a coordinator only (`coordToolsFor` from
  `runToolSpecs`), dispatched from `runTool`; the first sentences pinned in
  `tooldesc_test.go`.
- **Never answering a park** (§10.4): beside `hold_park` at the pump, a
  `runStatusHooks` entry (`coordHoldPark`) takes a coordinator's or an scm
  event's input that was already in a task's inbox when the task started
  waiting for a person back to the head of the queue, held.
- **Updates and wakes** — `B/projects_coord_deliver.go` (§10.5):
  `projectDeliverHook` (the newest 40 undelivered events of the
  coordinator's person, one line each, `#<n> <kind>: <text>` under
  `[project updates — tasks and the scm provider reporting, not a person]`,
  invisible characters removed and the frame and update markers defused,
  ≤ 8 KiB; the rest counted; all marked delivered with the message id);
  `projectWakeHook` (an undelivered `wake=1` event, the person still a
  participant, at most one wake per 60 s with a timer of its own for the
  rest); a `projectEventHooks` entry poking the coordinator on a wake
  event; an `ownerLoops` entry poking such coordinators at takeover;
  `projectCoordPrompt` (the `# Project` block).
- **Needs and pushes** — `B/projects_coord_push.go` (§10.6): `project {id,
  name, n}` on `/needs` items of a project's run, `GET
  /projects/{pid}/needs`, task push titles `<project> · <task>`, and the
  digest pushes `pr-ready`, `task-failed`, `ci-stuck`, `all-done` through
  the needs pusher (its budget and dedupe), `CollapseID
  project:<id>:<kind>`, to the person whose task it is while they take
  part.
- **API.md** §The coordinator.
- **Tests** — `B/projects_coord_test.go`, `B/projects_coord_flow_test.go`:
  every C test of §15.2 but `TestChannelAttach` (the slice slipped), and
  `TestCoordinatorRoute`, `TestCoordinatorPromptStable`,
  `TestCoordDeliverBounded`.

## Seams for others

- **Filled:** `projectWakeHook`, `projectDeliverHook`, `projectCoordPrompt`.
  **Registered:** `routeTables` (`coordRoutes`), `runStatusHooks`
  (`coordHoldPark`), `projectEventHooks` (`coordEventWake`, `coordDigest`),
  `runDeletedHooks` (`coordRunDeleted`: wake state), `ownerLoops`
  (`coordRecover`). No `schemaAdds` (no `project_attach`: V10 slipped).
- **For E:** an scm event's input to a task (`source: event`) is held back
  by `coordHoldPark` like the coordinator's if it was delivered just before
  the task parks. `pr.ready` (with `body.sha`) and `ci.stuck` events are
  what `pr-ready` and `ci-stuck` pushes come from; a `wake` event wakes the
  coordinator of `coordUser`.
- **For T:** `task_list {scope: "team"}` in a membership reads `GET
  /projects/<teamRef>/board` at global (`callGlobal`, as the person) and
  parses `{items: [BoardRow], next}`.
- **For U2:** `POST /projects/{pid}/coordinator` answers `{run}` (200,
  made or found; `text` queued as the person's message); `GET
  /projects/{pid}/needs` answers `{items}` in `/needs`'s shape.
- `needsItems(c, scope, args...)` (conversations.go) is `/needs` for a
  narrowed set of conversations.

## Changelog text

**The project coordinator** (agent template, API.md §The coordinator): a
person's own conversation that creates and steers a project's tasks —
`POST /projects/{pid}/coordinator` makes it on first use. In the web lane
(never internal reach; web tools only when the project's policy allows),
it has eight tools: `task_create` (from briefs or issues, at most 10 at
once, within the project's limits on open tasks and tasks a day),
`task_list`, `task_status`, `task_message`, `task_result`, `task_cancel`,
and read-only `scm_pr` and `scm_issues`, only ever on the project's own
tasks and repos. It never answers a question or approval meant for a
person, and can't merge, approve, push or comment. Project updates — tasks
ending turns, failing, waiting, pull requests going green, CI — reach it
as one framed message and wake it at most once a minute. Needs items of a
project's conversations name their project (`GET /projects/{pid}/needs`
lists one project's), task pushes are titled with the project, and a
project pushes `pr-ready`, `task-failed`, `ci-stuck` and `all-done`.

## Decision text

- **Made on request only.** A coordinator is made when its person asks, not
  by the first event that would wake one (the spec's other way): a person
  who creates tasks by hand would otherwise get an agent acting on them,
  and model turns spent, unasked. Events written before it are marked
  delivered when it is made.
- **Holding a park, both ends:** the pump holds a `hold_park` input while
  its task waits for a person, and an input delivered while the task
  worked is taken back when it parks — the second is what keeps a reply
  from being made for the person in the window between a step's delivery
  and its park.
- **A stable prompt:** the `# Project` block names the project, its repos,
  limits and rules only; task state comes as updates and from the tools,
  so the prompt cache holds.
- **Fail closed where the spec is silent:** the coordinator acts with the
  tile level "read" (a managers-only task class is refused to it); in
  approval mode its three writing tools ask; a person removed from the
  project has a coordinator that acts on nothing and isn't woken.
- **Digest pushes don't double:** a built-in task's failed turn already
  pushes "failed"; `task-failed` covers what nothing else does (a failed
  workspace, a coding agent's failed turn).
- **Not chosen:** a new toolset for the project tools (needs the stored
  classes rollback trick); a `projects.coord_run` column (the session key
  is authoritative, §6.12).

## Deviations

Also dated in projects-scm §19.

- §10.1: made only by `POST /projects/{pid}/coordinator`, never by an
  event; earlier events marked delivered at creation.
- §10.4: `coordHoldPark` (a `runStatusHooks` entry) takes undelivered
  coordinator and event inputs back to the queue when a task parks for a
  person (the pump checks `hold_park` only at delivery).
- §10.3: the coordinator's caller is its person with tile level "read";
  `task_create`/`task_message`/`task_cancel` are side effects in approval
  mode; the `# Project` block has no per-task state.
- §10.5: `coordRecover` (an `ownerLoops` entry) pokes coordinators with a
  wake event at takeover; a wake also needs the person to still take part.
- §10.6: `task-failed` only for a failed workspace or a coding agent's
  failed turn; `all-done` when every task's column is done.
- §16.2: a new file `projects_coord_scm.go`; `handleNeeds`'s body moved
  into `needsItems` in `conversations.go` (more than "the field").
- §10.7, §6.10: **V10 slipped** — the channel attach and `project_attach`.
  It needs edits beyond C's files: the `projects: true` rule in
  `channel_keys.go`, `/project` in the channel commands, the partition's
  `handoff/dm` consumer (`handoff.go`, `handoff_user.go`), and the
  coordinator's replies posted to the chat. API.md's one sentence ("is not
  in this build") is for the integrator to remove (§16.6); no feature key
  names it.

## Tests run / not run

`TMPDIR=/work/tmp-tests/c`, targeted only (the owner's test budget):

| Command | Result |
|---|---|
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok |
| `TILE_TEST_FLAGS="-count=1 -v -run TestCoordinatorClassFirewall\|TestCoordinatorCannotReachInternalTask\|TestCoordinatorToolsGated\|TestToolDescriptionFirstSentences\|TestResolverByNumberOnly\|TestCoordinatorCannotAnswerPark\|TestCreateLimits\|TestEventDeliveryAndWakeCoalesced\|TestNeedsProjectField\|TestDigestPushes\|TestSeededTokenNotInTools\|TestCoordinatorRoute\|TestCoordinatorPromptStable\|TestCoordDeliverBounded\|TestNeedsPush\|TestHoldParkInput\|TestWaitingRunHoldsSlot\|TestQueueFIFOAndSlots\|TestRunStatusHooksOnPark\|TestProjectRefRederivedAfterOldBinaryRewrite\|TestHarnessTask\|TestHostedRunReachesNoHook\|TestSigninParksTask\|TestFinishSpecByDepth\|TestHostedRunLacksPartitionTools" hack/tile-check.sh agent` (in the shell each `\|` is a plain `|`; C's tests and the P1, needs-push and tool-spec tests its hooks and edits touch) | ok — vet ok; 31 top-level tests pass (-v shows each), 14 s of tests |
| the C tests and `TestToolDescriptionFirstSentences`, `-race -count=2` (a scratch copy of the tile, as tile-check builds it) | ok, no race (78 s) |
| the C tests, `-count=4` | ok (the wake state is reset per test: run ids repeat between test databases) |
| P1's and the needs tests near C's edits (`TestNeeds*`, `TestHoldParkInput`, `TestWaitingRunHoldsSlot`, `TestQueueFIFOAndSlots`, `TestPumpsBackToBack`, `TestRunStatusHooksOnPark`, `TestGateParksAndResumes`, `TestHarnessTask`, `TestProjectStreamEvent`, `TestTurnEndHooksEverySite`, `TestHostedRunReachesNoHook`, `TestProjectSeamsRegistration`, `TestConfigProjectSeam`, `TestProjectArchive`, `TestTaskRunsNotChats`, `TestDeviceCodeOnlyToRequester`, `TestProjectSchemaMigratesTwice`, `TestProjectSchemaOldDB`, …), scratch copy | ok (20 s) |

Full -race suite and make check: at the gate (lead).

Not run, and why: `TestChannelAttach` (V10 slipped); C has no UI (U2
draws `proj.coordinator`), so no node or browser test; the isolated and
partitioned end-to-end tests (no rootfs here, projects-scm §15.4); a live
GitHub (no credentials — the reads go to K's fake provider through the real
scm client). `task_list {scope: "team"}` is not tested end to end: T's
board route is built in parallel (its parsing follows §12.3's shape).

## Merge risks

- `B/tools.go`: three one-line hunks (`runToolSpecs`, `sideEffect`, the
  dispatch in `runTool`).
- `B/conversations.go`: `handleNeeds`'s body moved into `needsItems` (P1's
  `handleNeeds` case is inside it, unchanged) — a branch editing
  `handleNeeds` conflicts.
- `B/needs_push.go`: one line in `needsPushes` (the title).
- `B/tooldesc_test.go`: one block (the eight sentences).
- API.md: §The coordinator only.
- **For the lead (P1's file, not edited):** `projectsWake`
  (`project_worker.go`) counts an undelivered `wake=1` event as runnable for
  any coordinator that exists — also one waiting for its person
  (`waiting_input`) or in `error`, which never takes a wake, so a person's
  partition with such a coordinator never sleeps until the person acts.
  Suggested: `AND r.status NOT IN ('waiting_input','error')` in that
  `EXISTS`.
- No migration; no table.

## Commits

| Commit | Subject |
|---|---|
| `f05861e0` | agent template: the project coordinator — resolver, tools, updates, pushes |
| `7293610d` | agent template: the coordinator's tests, and API.md §The coordinator |
| `3ef70258` | plans: projects-scm — the C record and §19 |

## Owner questions

- **Should a project event make a coordinator?** The spec says the first
  event that wakes one makes it. Built: only the person's request makes
  one (no agent acting, and spending, unasked). Default kept until ruled.
- **With what level does a coordinator act?** Built: "read" (its person's
  tile level isn't known outside a request), so a managers-only
  `policy.taskClass` refuses its tasks. Alternative: remember the level
  of the request that made it.
- **A removed person's coordinator** acts on nothing and isn't woken, but
  stays theirs to read (as their task conversations do, P1's ruling).
