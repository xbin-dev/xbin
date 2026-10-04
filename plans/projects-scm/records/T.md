# T — team projects

> Status: live — branch `wp/ps-t` (from `projects-scm` at `7dda955d`).

## What was built

- **The tables** `project_board` (global) and `project_board_out` (a
  person's partition), exactly as §6.10 gives them, made by
  `addTeamSchema` from `schemaAdds` (never on team; both tables in every
  home) — `B/project_team.go` (projects-scm §6.10).
- **The security part** (`teamSecurity`, `teamHash`): the canonical JSON
  of `{policy: {instructions, checks, prConventions, taskClass, engine,
  harness, as, membersAsBot, reviews, autoPR, autoLabel, ci, coordinator,
  workflows, protection, branchPrefix}, repos: [{repo, setup}]}` — policy
  defaults filled, repos sorted, every object's keys sorted — and its
  SHA-256. `def_pending` keeps exactly those bytes, so P1's
  `Project.DefPending` (the column's SHA-256) is the hash a member sends
  (projects-scm §12.2).
- **The board at global** — `B/project_team_routes.go`: `GET
  /projects/{pid}/board` (viewer; `run` zeroed on every row but the
  caller's own; stale marked as it is read), `PUT
  /projects/{pid}/board/{n}` (participant, `personFromPartition` with a
  partition id only; the member is the caller; a PUT from another partition
  id of the same person deletes their rows first; text one line, control
  and invisible characters dropped, redacted, ≤ 200 characters; URLs kept
  only when https on the definition's host without userinfo or port; words
  held to their sets; `run` ≥ 2^40 or 400; `deleted` hides), `POST
  …/board/{member}/{n}/hide` (owner), `POST /projects/{pid}/seed` (owner,
  team only), the `project` stream event with `change: "board"`
  (projects-scm §12.1, §12.3, §12.5).
- **The push from a membership** — `B/project_team.go`: a
  `taskChangedHooks` entry writes the task's `BoardRow` (from
  `projTaskView`, `taskCISummary` through it) to `project_board_out`, the
  latest winning, a row waiting for a retry keeping its wait; an
  `ownerLoops` entry (`teamLoop`) PUTs due rows through `callGlobal`,
  retrying `teamBackoff` (10 s) doubling to 10 min; 404/403 archives the
  membership (§12.5), 400 drops the row. The same loop re-reads the
  definitions of memberships with open tasks every 10 min and, at global,
  drops the rows of definitions gone or being deleted.
- **Memberships** — `B/project_team_member.go`: `GET /memberships`,
  `POST /memberships` (the definition read with `callGlobal GET
  /projects/<gpid>` as the person and checked again — name, repos, policy;
  409 with the security part and `defHash` until `accept` is it; the
  definition's name, scm, host, repos with their setup and policy copied;
  the person's own sandbox `{ref}`/`{new}`; a seed clone only for a
  membership working as the bot whose provider offers this person the bot
  and whose seed is team-visible with a fork-base snapshot at a manager
  that clones — `from_seed=1`; else fresh, its own `repo` jobs), `GET
  …/pending`, `POST …/accept` (re-reads first; 409 when the hash isn't the
  pending one's or the definition moved on). `teamSync`/`teamApplyTx`: the
  name, removed repos (failed queued jobs, a `repo-removed` scrub so the
  token's scope narrows) and the policy's other keys at once; the security
  part into `def_pending` with a `note` event (wake, deduped per hash).
  `teamLeave`: archived, `left` scrub, tasks poked, outbox emptied
  (projects-scm §12.2, §12.4, §12.5).
- **For the coordinator** — `teamBoardRows(ctx, membership)` (the board as
  its person sees it, up to 500 rows) and `teamBoardText(rows)` (one line
  per row, framed with `untrusted`).
- **API.md §Team projects.**
- **Tests** (`B/project_team_test.go`, `B/project_team_member_test.go`):
  every T test of §15.2 — `TestTeamDefinitionAtGlobal`,
  `TestMembershipCreate`, `TestBoardPushOnlyFromOwnPartition`,
  `TestBoardOutboxRetry`, `TestBoardRowsResetForNewPartition`,
  `TestBoardRowSanitized`, `TestSeedHoldsNoCredential`,
  `TestPersonCredsRefusedInSeedClone`, `TestMembershipSetupNeedsAcceptance`,
  `TestMemberRemovedArchives` (global and partition),
  `TestTeamCoordinatorSeesBoard` (global and partition) — and
  `TestTeamSchemaMigratesTwice`, `TestTeamSchemaOldDB`. The partition
  tests run alice's partition for real (her sandbox homed there through
  `partitionManager`, K's fake provider with her signed in, real git
  against a local origin, the seed made "at global" with a real snapshot
  and clone at the fake manager) with `callGlobal` replaced by a fake global
  instance (`fakeTeamGlobal`); the global tests send real partition
  headers through the route chain (`fromOwnPartition`).

## Seams for others

- **C (`task_list {scope: "team"}`):** call `teamBoardRows(ctx, p)` with
  the coordinator's membership (`*Project`, kind `membership`) and give the
  model `teamBoardText(rows)` — the rows are other members' words, already
  framed as untrusted. `errTeamGone` means the membership was just
  archived. (Or call `GET /projects/{team_ref}/board` through `callGlobal`
  yourself and frame it the same way.)
- **U2 (`proj.team`):** the routes in API.md §Team projects. Joining is
  two calls: `POST /memberships {team, sandbox}` → 409 `{defHash,
  definition, name}` to show, then the same with `accept: defHash`. The
  "Review the team project's changes" card reads `GET
  /memberships/{pid}/pending` and posts `{hash}` to `…/accept`. The board
  lives at the definition's home (global; `homeOf(id)` routes it); `run`
  is set only on the caller's own rows.
- **P2:** a seed's fork-base snapshot is P2's `snapshot` job on the
  definition (`fork_snap`); a membership reads it from the definition's
  `ProjectView.forkSnap` and clones with `sbxCreate.From`.
- **E:** nothing at global subscribes for a definition (V14); a
  membership's tasks are personal projects' to E.
- `taskChangedHooks` and `ownerLoops` each gain one entry; nothing of
  §14 changed.

## Changelog text

**Team projects** (agent template, API.md §Team projects): in a
partitioned agent a team project is a definition in the shared space —
name, repos, policy, people, an optional seed sandbox that never holds a
credential — and each member's own membership in their own space, with
their own sandbox, tasks (coding agents allowed), coordinator and sign-in.
Joining shows the definition's setup scripts, instructions and identity
first; later changes to them wait until the member accepts them ("Review
the team project's changes"), while its name and removed repos follow at
once. The shared board shows every member's tasks — state, branch, pull
requests, CI — as plain text, each row sent only from its member's own
space, a task's conversation openable only by its member. A member who
leaves (or a definition deleted) archives their membership: credentials
scrubbed, their tasks kept.

## Decision text

- **The board is pushed, not pulled:** each membership's outbox PUTs its
  rows to global as the person (`callGlobal`), the latest row winning,
  retried 10 s → 10 min; global never reaches into a person's partition.
  Global trusts nothing in a row but its shape: the member is the caller,
  the run is offered only to them, text is plain and clipped, links only on
  the definition's host.
- **Acceptance is by hash of exactly what was shown:** the security part's
  canonical JSON is stored as `def_pending` and hashed; accepting re-reads
  the definition and refuses a hash that no longer matches, so a member
  never adopts something they didn't see. Keys outside the security part
  (`maxTasks`, ports, cleanup …) and repo removals follow at once — they
  narrow, never widen, what runs beside the member's token.
- **Leaving is detected by the member's own partition** (404/403 on a
  re-read or a push), not announced by global: global has no route into a
  partition, and the partition is the one holding the credentials to
  scrub.
- **Stale rows are computed as the board is read** — removal is P1's
  members route, and a member taken back in is un-staled the same way.
- **Not chosen:** a route to move a seed clone into a fresh sandbox (the
  tasks' worktrees live in the old one; deleting and re-joining is the
  path); pushing board rows from an archived membership; ETags on the
  definition read (P1's route has none; a re-read with no change writes
  nothing).

## Deviations

Also dated in projects-scm §19.

- §12.2 sync timing: re-read on page open (`GET /memberships`,
  `…/pending`; at most every 30 s), on task creation (after its commit —
  P1's queue has no "before a start" seam), every 10 min with open tasks,
  and before an accept; no ETag (P1's `GET /projects/{pid}` sets none).
- §12.2, §7.2 `POST /memberships`: 403 for a viewer of the definition (its
  board PUT would be refused anyway); `sandbox` left out → `{new}` at the
  seed's manager (400 without a seed); an archived membership is taken up
  again (200) after the same acceptance; the 409 adds `refusal: "accept"`
  and `name`; `…/pending` adds `state`.
- §12.2 "start in a fresh sandbox": no route; the member deletes the
  membership and joins again (see Owner questions).
- §12.3: a deleted task's row is sent with `run` 0 and global takes it
  without the run check (it only hides the row); an archived membership
  sends no other rows; 400 drops a row, 403 archives like 404.
- §12.5: `stale` is marked when the board is read, not at the removal.
- §7.2 `POST /projects/{pid}/seed`: one seed per definition (409 once it
  has one).
- §6.10, §16.2: both board tables in every home; no `partition_routes.go`
  entry was needed.

## Tests run / not run

All with `cd /work/wt/ps-t && eval "$(hack/dev-setup.sh --env)" && export
TMPDIR=/work/tmp-tests/t`, on the final code (`f72b39e0`):

| Command | Result |
|---|---|
| `TILE_TEST_FLAGS="-count=1 -v -run TestTeamDefinitionAtGlobal\|TestMembershipCreate\|TestBoardPushOnlyFromOwnPartition\|TestBoardOutboxRetry\|TestBoardRowsResetForNewPartition\|TestBoardRowSanitized\|TestSeedHoldsNoCredential\|TestPersonCredsRefusedInSeedClone\|TestMembershipSetupNeedsAcceptance\|TestMemberRemovedArchives\|TestTeamCoordinatorSeesBoard\|TestTeamSchemaMigratesTwice\|TestTeamSchemaOldDB" hack/tile-check.sh agent` (each `\|` a plain `\|` in the shell) | ok — 13 top-level tests, 10.6 s (50 s with the tile's vet) |
| the same T tests, `go test -race -count=1` (a scratch go.work over `_backend`, as tile-check builds it) | ok, no race (the outbox loop is concurrent) — before the backoff fix below; `TestBoardOutboxRetry` then `-race -count=4`: ok |
| `go test -count=1 -run TestProject\|TestTask\|TestShared\|TestGate\|TestSignin\|TestDeviceCode\|TestConfigProjectSeam\|TestAdapterRouteTables\|TestFeatureSchemasNotOnTeam\|TestSCMSecretNeverShows\|TestWorktreeFlow\|TestPrepareIdempotent\|TestTakeoverResumesJobs\|TestCleanup\|TestDeleteRunMarksTask\|TestRefsJobFiresHooks\|TestHarnessTask\|TestQueueFIFOAndSlots\|TestWaitingRunHoldsSlot\|TestHoldParkInput\|TestTurnEndHooksEverySite\|TestRunStatusHooksOnPark\|TestHostedRunReachesNoHook\|TestSeededTokenNeverStoredTask\|TestCredGateMatrix\|TestPendingSigninKept\|TestUserModeRoutes\|TestGlobalTakesPartitionCalls` (P1's, K's and gate 1's tests the new hook and loop touch) | ok, 53 s |
| `TestBoardPushOnlyFromOwnPartition` with `personFromPartition` taken out of the PUT handler | FAIL as expected (an admin-role call naming bob's partition got 200); restored, ok |
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok |

Full -race suite and make check: at the gate (lead).

Not run, and why: the UI checks (T has no UI; U2 draws it); the isolated
partitioned end-to-end tests and the UI harness (projects-scm §15.4) — so a
real global instance and a real partition talking through xbind were never
run together: the partition side runs against a fake global instance
(`callGlobal` replaced) and the global side against real partition headers
through the route chain; a live GitHub (none here).

## Merge risks

- No file outside `B/project_team*.go`, API.md §Team projects and plans/
  is edited. `partition_routes.go` is untouched.
- `taskChangedHooks` gains T's entry and `ownerLoops` gains `teamLoop`;
  `schemaAdds` gains `addTeamSchema`. V's `taskCISummary` is read inside
  T's hook (through `projTaskView`), in the task's transaction: V's
  implementation must not need its own write transaction there.
- C's `task_list {scope: "team"}` should call `teamBoardRows` /
  `teamBoardText` (above); if C wrote its own board read, check it frames
  the rows as untrusted.
- New migrations: two tables, `CREATE … IF NOT EXISTS` only.

## Commits

| Commit | Subject |
|---|---|
| `55bc8c68` | agent template: team projects — board, memberships, definition sync, seed |
| `fb622150` | agent template: API.md §Team projects |
| `f72b39e0` | agent template: team board — a PUT naming a partition from another tile or as admin is refused |
| (this commit) | plans: projects-scm — the T record and §19 |

## Owner questions

- **A seed-cloned membership whose definition stops working as the bot.**
  Built: its tasks fail with `seed-clone` (K's gate) and API.md says to
  delete the membership and join again — which deletes its task
  conversations too. A route moving a membership to a fresh sandbox
  would have to re-prepare every open task's worktrees elsewhere; worth it?
- **A member edits their own membership's policy.** A membership is a
  project its person owns, so P1's `PATCH /projects/{pid}` lets them set
  any key, `as` and `membersAsBot` included, without the definition. The
  provider's `botForPeople` still decides whether the person gets the bot
  (as for a personal project), so nothing is reached that a personal
  project couldn't; but the team owner's `membersAsBot` is advisory. Built:
  nothing (P1's route); should a membership's `PATCH` refuse the security
  keys?
