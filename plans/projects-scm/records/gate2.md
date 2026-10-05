# Gate 2 — wave 2 merged and made to fit

> Status: live — on `projects-scm` after the merges of P2, E, C, T, V, G2,
> U2 and the live checks (`wp/ps-live`), ending at `b8c74735`; the
> integration commits follow, ending at `8db964c5`.

## The merges

Every branch merged with no conflict outside §19's append-only lines
(kept from both sides). Each branch was checked before the merge: the
Co-Authored-By trailer on every commit, no edit of docs/changelog.md or
plans/DECISIONS.md, no D-number above D181, no "PR" directly followed by
a digit, no plans/ pointer inside builtin-templates/ or docs/. V's build
was also read by hand (its automated review timed out): its tokens are
test literals only, and nothing outside its worktree changed.

## What failed after the merge, and why

The first run on `b8c74735`: scm-github `-race`, builtins — pass; the
agent tile's `-race` suite, the node tests and `make check`'s js-test —
fail.

- **Data races** reported against unrelated tests (the mailbox,
  partition-route and device-code tests among them). Cause: the engine's
  project worker, its jobs and every owner loop were cancelled by
  `Shutdown` but never waited for, so E's `scmLoop` and T's `teamLoop`
  from one test's engine kept reading the run mode and `scmFor` into the
  next test; `teamLoop` also read the global mode while tests switched it
  under a running engine.
- **`TestRoutingTable/checks_failure`** — with V in, a task's CI summary is
  `none` until its first read, and `taskDerived` took it over the PR
  checks E had recorded as failed (the task showed `awaiting-review`).
- **`TestSubscriptionLifecycle`** — a task's refs now also make V's
  `ci:<watch id>` subscription, as §7.6 intends; E's test counted it.
- **The native board** said "CI success · CI ✓": the test still registered
  a stand-in `ext.card` probe from before V; V's real one added its word.
- **`native-long`** (140 rows, not 160): V's model repaints once its
  dismissed-cards pref is read, while the window is still at the bottom,
  and the chat window lets far-above rows go then (as designed). Not flaky:
  it passed at every merge before V's.

The second run on `50b18c0f`: node 303/303, scm-github `-race`, builtins —
pass. One data race left: V's after-commit goroutines (`ciRenewLater` and
its siblings) outlived a test and read `scmFor` while E's fixture put it
back.

## What changed

- `7f3a478e` the engine counts the project worker, its jobs and its owner
  loops (`projWG`) and `Shutdown` waits for them; `teamLoop` and
  `scmLoop` read the mode captured when they started (`loopMode`);
  fixtures that swap seams after their engine started stop it first.
- `b05cdeba` a `none` CI summary falls back to the PR's checks.
- `4195f5d0` the native tests load V's CI module instead of a probe; the
  long-chat test waits for the page to settle and asserts what the window
  is meant to do.
- `56b81ca4` P1's two `DELETE FROM settings WHERE key…` name the column
  `k` (found by P2): the `proj_sbx_new` and `proj_sbx_delete` rows are
  deleted now; `TestProjectDelete` checks it.
- `90eb4851` the coordinator's team board is one read: T's
  `teamBoardPage` (with its archive on 404/403), framed by C's
  `task_list`; T's unpaged helpers are gone; `TestTeamCoordinatorSeesBoard`
  goes through the tool.
- `f7230c59` `projectsWake` doesn't count a wake for a coordinator that is
  waiting for input or failed.
- `418cbc6f`, `0e2e3aa6` E's intake and G2's delivery each pin the
  documented event v1 literal (docs/scm.md §Delivery) in a test; compared
  field by field, no mismatch.
- `8db964c5` V's after-commit work runs through `ciGo`, counted in
  `ciBG`; the fixtures that swap `scmFor` wait on it.

No security check was weakened and no test expectation was loosened
beyond counting only a test's own subscriptions where V's now exist too.

## Checks

| Check | Result |
|---|---|
| `make check` | pass but for `internal/tilesbx` (below) |
| scm-github `-race` | pass |
| agent `-race` | pass but for `TestTaskSurvivesThreeCompactions` (below); the last race fixed in `8db964c5`, its tests rerun under `-race` |
| builtins | pass |
| node (31 agent-template files) | 303 pass |

Known failures, not this programme's: `TestTaskSurvivesThreeCompactions`
under `-race` (fails before the programme, at `7d310f72`);
`internal/tilesbx` `TestAdmissionCaps`, `TestResourceReconcile` (fail on
master on this 8 GB host) and `TestStdioNewestWins` (a read timeout
under load) — the programme changes nothing under `internal/`, `cmd/` or
`sdk/`. The full suites run again after ps-review's fixes.

## Open for ps-review

The waves' remaining minors (records and the gate reports), and V's
`ciTaskRefs`, which lacks the team-definition guard E's subscriptions
have (unreachable today: a definition has no tasks).
