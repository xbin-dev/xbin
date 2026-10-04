# V — CI view

> Status: live — branch `wp/ps-v` (from `projects-scm` at 7dda955d).

## What was built

- `ci_watch` (projects-scm §6.9) — `B/ci_store.go`: the table through
  `schemaAdds`, a watch's cleaned snapshot (every text redacted with
  `projRedact`, invisible characters removed, clipped; links kept only when
  `http(s)`; ids and words checked; state and counts computed again by
  §4.9's rules; ≤ 256 KiB), the conversation's `CISummary`, the views, the
  `ci` stream event (coalesced per root, key `ci:<root>`, not replayed),
  and `taskCISummary` (memory only, per database and root — see Decision).
- A watch's life (§7.6) — `B/ci_watch.go`: upsert per (root, scm, repo,
  ref) with the 10-watch limit (the oldest pushed ends), its subscription
  (`ci:<id>`, kinds checks, pull, workflow, job, check, push; posted again
  whenever its head moves), a pushed watch's PR looked up once, one
  conditional `GET /scm/checks` per watch at a time (shared by every
  asker), `fresh=1`'s 10 s / 30 s rule, a task's watches from
  `projectRefsHooks` and lazily from `GET /runs/{id}/ci`, `runDeletedHooks`,
  and the `ownerLoops` refresher (60 s for 20 min after a push, then 10 min
  until 2 h, 30 min until 24 h, then nothing; never while an event moved
  the watch in the last 2 min; gone watches end after a day, ended rows go
  after a week).
- Pushed-branch detection (§7.6, V13) — `B/ci_detect.go`: `turnEndHooks`
  entry for any run with a sandbox (the root's binding, a coding agent's
  `Harness.Ref`/`Cwd`) while an scm provider is bound; the spec's script in
  that run's sandbox after the commit, `SINCE` its turn start; lines parsed
  in memory (scp-like and ssh remotes too), userinfo dropped, the output
  never kept; a host a bound provider's hello lists; the bot rule at a bot
  home (a project of the home naming the repo with the owner a
  participant, else `scmBotAllowed(owner)` — and only the project in a
  shared conversation). Project and hosted runs make none.
- scm events (§7.6) — `B/ci_events.go`: `scmEventHooks` entry matching
  provider and repo, then branch, head, PR or `subs` key; progress patches
  the snapshot (cleaned like a read), `checks` and `pull` re-read, `push`
  moves the head, closed/merged/deleted → `gone`.
- Routes and the run view (§7.2 V rows, §7.3, §7.4) — `B/ci_routes.go`:
  `GET /runs/{id}/ci`, the log (ANSI and control characters stripped,
  redacted; tail ≤ 262144; 409 `in-progress` with the live URL), the
  annotations, watch / unwatch, rerun (a person, not view-as, in their own
  partition, as `person`; 403 `identity` elsewhere; 501 without
  `checks.rerun`; a journal note), ids only from the stored snapshot; the
  `ci` key of `GET /runs/{id}` and `/view` (`runViewHooks`).
- The model (§13.1, §13.5) — `A/model/ci.js`: `createCI(app)` with every
  member §13.5 lists, plus `chipWords`, `jobProgress`, `toneOf`,
  `stripAnsi`, `elapsed`, `watchRows`, `jobRow`, `cardOf`; `A/model/app.js`
  wires `app.ci` and `app.ci.take(ev)`.
- Web (§13.2, §13.5) — `A/ci-dock.js` (chip, the dock's CI section, the
  log viewer, `childStatus`, `card`, `paint`; `openCI`), `A/ci-cards.js`
  (outcome cards), the dock host in `A/harness-board.js` (tabs,
  `openDock`, `dockTab`), `ext.childStatus` in `A/harness-child.js`, two
  imports in `A/harness-web.js`.
- Native (§13.3, §13.5) — `A/native/ci.js` (the Coding agents screen's CI
  sections, `ci-job`, `ci-annotations`, `ci-watch`, `childStatus`, `card`,
  `end`, `menu`), `ext.dock` and the toolbar badge in
  `A/native/harness-board.js`, `ext.childStatus` in
  `A/native/harness-child.js`, the import in `A/native/harness-all.js`.
- Feature keys (§13.6) — every `ci.*` key implemented in
  `A/web-features.js` and `A/native-features.js` (V's slots); the CI groups
  of `stagedWeb` and `stagedNative` are empty (their comment lines kept).
- Docs — `A/API.md` §CI in the conversation.
- Tests (§15.2 V row) — `B/ci_store_test.go`, `B/ci_detect_test.go`,
  `B/ci_events_test.go`, `B/ci_routes_test.go`, `B/ci_watch_test.go`,
  `B/ci_fakes_test.go` (fixture: K's fake provider through its levers,
  the fake sandbox manager, real git); `hack/agent-template-ci.test.mjs`,
  `hack/agent-template-native-ci.test.mjs`; `A/test/ci.mjs` with
  `A/test/ci-stub.mjs`.

## Seams for others

- `taskCISummary(runID)` is filled (memory only: safe inside a
  transaction). `TaskView.ci` and T's board rows get it.
- `projectTaskChanged(t, pid, n, "ci")` is called (in the watch's
  transaction) whenever a task conversation's summary changes.
- `scmEventHooks`: V's entry needs to see every `checks`, `pull`, `push`,
  `workflow`, `job` and `check` event, including those whose `subs` hold
  only V's keys (`ci:<watch id>`), inside a transaction with the feature
  tables (E's intake).
- `projectRefsHooks`: V's entry makes the task's watches from its
  checkouts' `remote_sha` and its `prs` (P1's refs job, P2's `pr` job).
- Web: `ext.dock` sections are `{key, title, badge?, tpl()}`; the host is
  `harness-board.js` (`openDock(key)`, `dockTab()`). `ext.card(task)` on the
  web answers a lit template (a button that opens the task on CI);
  natively it answers **words** (a string), which U2's board row adds to
  its row's text. `ext.childStatus(r)` answers a template on the web and a
  `text` natively (one template per words, so the card's memo keys it).
- The `ci` stream event's data also carries `watches: [{id, state,
  outcome, run, repo, ref}]` (additive).
- `GET /runs/{id}` and `/view` carry `ci: {summary, canWatch}`.

## Changelog text

> **CI in the conversation.** A conversation now shows what the platform's
> CI says about what it pushed — a project task's branch and pull request,
> the branches any coding session pushed (found from git's own record of
> pushes at the end of each turn, in the run's own sandbox), or a branch or
> pull request someone names — inside the coding agents' UI: a CI chip
> beside the coding agents chip, a CI tab in their dock with live job
> progress down to each step, job logs (searchable, followed while a job
> runs where the platform allows, else a link to the live log),
> annotations, links to the runs, jobs, checks and pull requests, a person's
> own "Re-run failed", and a card in the transcript when CI passes or
> fails; a coding agent's card and a project board's task show their CI
> too. The native view has the same as screens. Read through the agent's
> scm provider — never a token in a sandbox; where the agent reads as the
> provider's bot, only repos someone may name for it. API.md §CI in the
> conversation; new routes under `/runs/{id}/ci`, a `ci` stream event, a
> `ci` key on a run's answers. Additive: an older agent leaves the
> `ci_watch` table alone.

## Decision text

- **One dock with sections**, not a second dock: `harness-board.js` hosts
  `ext.dock` sections behind tabs, opens on a section even without coding
  agents, and widens while CI's log viewer is shown.
- **`taskCISummary` reads memory, not the table.** P1 builds a TaskView
  inside transactions that hold the agent's only database connection; a
  read through another handle there would deadlock. Summaries are kept per
  (database, root), read from the table when the database opens and set in
  the transaction that changes a watch (before the board's hooks run).
- **Pushed branches from git's reflog**, in the pushing run's own sandbox,
  output parsed in memory and never kept, userinfo dropped before anything
  else; one command per turn end, off the engine's path.
- **The bot reads only what someone authorised**: a manual watch at a bot
  home takes a manager or the scm bot rule; a pushed one a project of the
  home naming the repo with the owner taking part, or the rule — and in a
  shared conversation only the project.
- **Ids only from the stored snapshot**; every text a build printed is
  cleaned before it is stored or served and drawn as text; links only
  `http(s)`.
- **Reruns are a person's own, as themselves, in their own partition**
  (V12): never the bot; the journal says who re-ran what.
- **Reads are shared and conditional**: one upstream read per watch at a
  time; the dock's 15 s re-read only while anything is pending; the
  refresher's cadence only while no event moved the watch.
- **Not chosen:** watching by polling every pending branch at a fixed
  rate (rate limits shared by everyone at the provider); a per-viewer
  dismissal on the server (the person's xbind prefs keep it).

## Deviations

- projects-scm §13.5: dismissed outcome cards are kept in the person's xbind
  prefs (`ci-dismissed`), not `localStorage` — a model module touches no DOM
  storage (hack/agent-template-model.test.mjs), and a tile frame keeps none
  of its own (model/actions.js says the same of the class pick).
- projects-scm §13.1, §16.2: `model/app.js` gains three lines — the two
  named and the `createCI` import.
- projects-scm §16.2: `harness-web.js` gains the two imports under a "CI"
  slot comment (after U8's comment, so after U7's slot).
- projects-scm §16.2: `native/harness-child.js` gains three lines, not one:
  its card memo returns a cached card unless the child's run changed, so it
  keys CI's words too (native/ci.js hands back one template per words) —
  else a CI result arriving after the child's last change would never show.
- projects-scm §7.4: the `ci` event's data also carries `watches: [{id,
  state, outcome, run, repo, ref}]` (additive), so a client keeps every
  outcome card and child glyph from the latest event alone.
- projects-scm §7.2: `POST /runs/{id}/ci/watch` answers 200 with the live
  watch when that branch is already watched (201 when made); `DELETE` of a
  task's own watch is 409 (the UI hides its ✕; it would be made again by
  the task's next refs check).
- projects-scm §7.2 (silent): `GET /runs/{id}/ci` without `fresh` reads a
  watch never read yet (opening a conversation "reads once").
- projects-scm §7.3: `canWatch` is also true for a conversation with a
  coding agent child (its own sandbox) or one that already watches
  something.
- projects-scm §7.6: a task's watch is read as its project's identity
  (`projectAs`, the same as its credentials); other watches as the home's
  default (the person in their partition, else the bot).
- projects-scm §6.9 (silent): `since` starts again when the watch's head
  moves — the refresher's cadence counts from the push it watches.
- projects-scm §7.6 (silent): a task's or a pushed watch past the 10-watch
  limit with no pushed watch to end is not made (logged); a manual one is
  409 `limit`.
- projects-scm §7.6: a summary's `current` is dropped while the snapshot
  holds an event's patch newer than its last read (that is when its steps
  may be stale), not by a clock.
- projects-scm §7.6 (silent): a subscription is posted when the watch is
  made and again whenever its head moves (a push event, or a read finding a
  new head) — a branch still pushed to keeps its subscription past the 30
  days a subscription lasts; nothing else renews it.
- projects-scm §6.14: a deleted conversation's watches are deleted, rows
  and all (their snapshots were that conversation's CI), not just ended.
- projects-scm §7.6 (silent): with no turn start recorded (an old run)
  pushes of the last hour count.
- projects-scm §7.3: `urls` are built for a GitHub provider (hello's
  `scm.kind`) only; another host's are left out.
- projects-scm §13.5: the failed chip names the first failing job, else
  the first failing check of its own ("CI ✗ codecov/patch").
- projects-scm §13.3 (silent): natively a refusal to sign in is said in the
  watch's footer (sign in from the project's settings); the inline sign-in
  card is the web's (U1's `signinTpl`).
- projects-scm §16.4: the browser test's documented command
  (`PLAYWRIGHT_BROWSERS_PATH=… node builtin-templates/agent/test/ci.mjs`)
  prints "SKIP: playwright not installed" on this machine (Playwright lives
  under `$PLAYWRIGHT_DIR`, which the bare import doesn't reach); it was run
  with a preload resolving `playwright` there (below).

## Tests run / not run

| Command | Result |
|---|---|
| `TILE_TEST_FLAGS="-count=1 -v -run TestCIWatchSchema\|TestPushedBranchDetection\|TestPushedBranchDetectionChild\|TestCIAggregate\|TestCIProgressEventsPatchSnapshot\|TestCIFreshCoalesced\|TestCILogRedactedAndStripped\|TestCILogInProgress\|TestCIAnnotationsRedacted\|TestCIRerunPersonOnly\|TestCIWatchBotRule\|TestCIIdsFromSnapshot\|TestCIWatchLimits\|TestCIBackgroundRefresh\|TestCIOutcomeCardedOnce\|TestTaskCISummary\|TestCIParsePushes\|TestTurnEndHooksEverySite\|TestRefsJobFiresHooks\|TestHostedRunReachesNoHook\|TestProjectSeamsRegistration\|TestFeatureSchemasNotOnTeam\|TestSeededTokenNeverStoredTask" hack/tile-check.sh agent` | ok — 23 tests pass (`-v` lists each), `✓ agent` (50 s) |
| `go test -race -count=1 -run 'TestCI\|TestPushedBranch\|TestTaskCISummary'` (the agent backend, in a scratch module dir with the tile's go.mod, as tile-check builds it) | ok (56 s) |
| `go test -count=3 -run 'TestCI\|TestPushedBranch\|TestTaskCISummary'` (same) | ok |
| `go test -count=1 -v -run 'TestWorktreeFlow\|TestDeleteRunMarksTask\|TestRefsJobFiresHooks\|TestHarnessTask\|TestTurnEndHooksEverySite\|TestRunStatusHooksOnPark\|TestHostedRunReachesNoHook\|TestProjectStreamEvent\|TestSeededTokenNeverStoredTask\|…'` (P1's hook and flow tests beside V's entries) | ok (all pass) |
| `node --test hack/agent-template-ci.test.mjs` | 7 pass |
| `node --test hack/agent-template-native-ci.test.mjs` | 4 pass |
| `node --test hack/agent-template-features.test.mjs` | 9 pass |
| `node --test hack/agent-template-model.test.mjs hack/agent-template-projects.test.mjs hack/agent-template-harness-board.test.mjs hack/agent-template-harness-child.test.mjs hack/agent-template-native.test.mjs` | all pass (10, 27, 13, 15, 25) |
| `node --import <a preload resolving playwright under $PLAYWRIGHT_DIR> builtin-templates/agent/test/ci.mjs` (PLAYWRIGHT_BROWSERS_PATH set) | all CI checks passed |
| the same for `test/harness-board.mjs`, `test/harness-child.mjs`, `test/projects.mjs`, `test/harness-cards.mjs` | all passed |
| `make js-check native-check` | ok (52 s) |
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok (52 s) |

Full `-race` suite and `make check`: at the gate (lead). Not run here: the
scm-github tile (V edits none of it), the partitioned end-to-end tests and
a live GitHub (projects-scm §15.4) — so no real webhook, log redirect or
rerun has been seen; the UI harness.

## Merge risks

- **E** (`POST /adapter/scm/event`): V subscribes with keys `ci:<watch
  id>`; E's intake must hand such events to `scmEventHooks` (in a
  transaction, features on) and must not answer 404 "no such subscriber"
  to an event that matched only V's watches, or the provider drops it and
  counts it.
- **U1/U2 boards**: the web's `ext.card(task)` answers a template; the
  native one a string (words) — U2's row must add it to its text.
- **`A/model/features.js`**: V emptied the CI groups of both staged lists
  (comment lines kept); U2 empties its own groups beside them.
- **`A/native/harness-board.js`** (V's ≤ 6 lines: the toolbar button and
  the `ext.dock` sections) and **`A/native/harness-child.js`** (3 lines) —
  U2 edits neither per §16.2.
- **`A/model/app.js`**: three lines after U1's (wave apart).
- **T**: board rows' `ci` comes from `taskCISummary` (memory; fine inside
  T's transactions).
- No migration beyond `ci_watch` (additive, idempotent; tested twice and
  on an old database).

## Commits

| Commit | Subject |
|---|---|
| `76927405` | agent template: CI watches — ci_watch, the aggregate, reads, events, routes |
| `9bdaa9e4` | agent template: CI watches' tests, and the refresher's clock swapped whole |
| `6044bb6f` | agent template: model/ci.js — CI in the conversation for both views |
| `6b441aa0` | agent template: CI on the web — the chip, the dock's CI section, logs, cards |
| `5fd0b5eb` | agent template: CI in the native view — native/ci.js and its call sites |
| `f33cbd97` | agent template: API.md §CI in the conversation |
| `b51e5c90` | agent template: a CI watch posts its subscription again when its head moves |
| (this) | plans: projects-scm — the V record and §19 |

## Owner questions

None.
