# Gate 1 — wave 1 merged and made to fit

> Status: live — on `projects-scm` after the merges of G1, P1, K and U1
> (`5582fc91`, `7bfc5868`, `b045c040`, `1bdedacd`).

## What failed after the merge, and why

The agent tile's suite, once without `-race` on the merged branch: 16
failures, all P1 tests through its project fixture (`newProjFix`), each a
timeout:

- `TestProjectAccessMatrix/unpartitioned`, `TestProjectDelete`,
  `TestProjectArchive`, `TestProjectReposAndStatus`, `TestWorktreeFlow`,
  `TestPrepareIdempotent`, `TestTakeoverResumesJobs`,
  `TestCleanupRefusesUnpushed`, `TestCleanupForceOwnerOnly`,
  `TestDeleteRunMarksTask`, `TestRefsJobFiresHooks`, `TestHarnessTask`,
  `TestGateParksAndResumes`, `TestProjectPromptStable`,
  `TestHoldParkInput` ("timed out waiting for task workspace ready", "…
  the project's repos to be ready", "… the first turn"), and
  `TestSigninParksTask` ("… the turn after the sign-in").
- **Cause 1 (15 of them).** P1 tested against the seams' no-op defaults.
  With K in, every git step mints the project's credential first
  (`scmEnsureCreds` → K's `ensureCreds`) and the workspace gate parks a
  task whose credential is due (`scmCredsDue`), queuing a `creds` job. The
  fixture's in-memory provider (`p1SCM`) answered 501 to `Token`, so the
  repo job and the creds job never succeeded. (`TestSigninParksTask` stubs
  `scmEnsureCreds` as its spec row says, but K's own `creds` job calls
  `ensureCreds` directly, so it hit the same 501.)
- **Cause 2 (`TestHarnessTask`, once cause 1 was fixed).** K's
  `scmCredsReady` queued an `inboxWake` for every run the gate had parked —
  also one parked only while its workspace was prepared. A coding agent's
  pass takes a wake on its own (`harnessWake`) and returns; the task's
  first prompt beside it stayed in the inbox with nothing to poke the run
  again. P1 had already moved `bind` from a wake to a poke for the same
  family of reasons (projects-scm §19, P1, §8.3/§8.6).

The full `-race` run then found two more, from one cause:

- A **data race** on the package's `agent` variable (in
  `TestTaskRunsNotChats`: K's `scmScrubRow` on a repo job of the project
  worker; in `TestRefreshRewritesFile`'s engine: `scmMemberCount` from the
  refresher), and a **nil-pointer panic** in `scmPendingRow` from the
  refresher that killed the test binary (the rest of the suite didn't
  run).
- **Cause 3.** K's credential code read the global `agent`; with P1 in it
  runs on the project worker's jobs and in an owner loop (the refresher),
  goroutines of an engine. The tests put `agent` back in a cleanup that
  runs before their engine stops, so those reads raced with the write, and
  a refresh in that window found `agent` nil. P1's worker code reads
  `projAg()` (the engine's agent, an atomic) for exactly this reason.

## What changed

- `project_steps.go`: P1's wrapper `ensureCreds` → `stepCreds` (it clashed
  with K's `ensureCreds`; the lead's rename, committed here).
- `project_fakes_test.go`: `p1SCM` keeps its in-memory reads (repos,
  issues, pulls — what Projects' own tests are about) and hands `Token`,
  `Revoke`, `Signin*` and `Forget` to the scm client bound to K's fake
  provider (`newFakeSCM` + `bindSCM`, scm_fake_test.go), reached as
  `fx.scm.prov`. A project's credential now goes through K's gate,
  minting, files, git config and redaction as in production. No stub hides
  K's gate; `TestSigninParksTask` and `TestDeviceCodeOnlyToRequester` keep
  their `scmEnsureCreds`/`scmPendingSignin` stubs (their spec row).
- `scm_creds_test.go`: K's in-process state reset (live tokens, retired
  values, priors, sign-ins) moved from `credFixture`'s cleanup into
  `scmForgetState(t)`, which both fixtures call.
- `scm_ensure.go`: `scmCredsReady` pokes a parked run (`scmPokeRun`, a var
  so a test can watch it) instead of queuing an `inboxWake`; the gate looks
  again and the input waiting in the inbox starts the turn.
  `TestPendingSigninKept` checks the poke and that no wake row is left.
- `scm_creds.go`, `scm_ensure.go`, `scm_scrub.go`, `scm_gate.go`: every
  read of the global `agent` goes through `scmDB()` (= `projAg().db`); the
  routes (`scm_routes.go`) are unchanged. In production both are the one
  agent.
- No security check was weakened and no test expectation was loosened.
  S0's seam files (`projects_seams.go`, `projects_types.go`) are
  untouched.

Not changed, noted: a coding agent's pass that holds a wake and a prompt
takes the wake alone and leaves the prompt for the next poke
(`harnessPass`, harness_pass.go). Nothing of Projects queues such a wake
any more; whether the harness should go on to the prompt is outside gate
1.

## The gate-1 test

`TestSeededTokenNeverStoredTask` (`B/project_scm_test.go`): P1's project
and task flow with K's credential in the task's sandbox, from the fake
provider (520-character tokens, so no shape to go by). A repo `setup`
prints the credential file to stdout and an error line to stderr; the
task's bash prints it with `cat`, `git credential fill` (the real helper),
`$GH_CONFIG_DIR/hosts.yml` and an error. Then: every token's prefix,
middle and suffix (40 characters each) is in no row of any table
(`tablesHolding`), no log line, no stream event (the task's tree replayed
from its ring, and the list stream), no answer of `GET /runs/{id}/view`,
`/runs/{id}/task`, `/projects/{pid}/tasks/1`, `/projects/{pid}`, and
nothing sent to the model; the commands' output is there
(`password=`, `username=`, `oauth_token`, `error: bad`, the setup's
`setup error: password=`), masked (`password=[redacted]…`) in the
transcript and the setup job's output.

Fails without redaction (each tried, then restored; the file compared
clean with `git diff`):

- `scmRedact` made the identity (scm_creds.go): "the token is in table(s)
  [messages messages_fts messages_fts_content]", "… in a stream event",
  "… in GET /runs/1/view", "… in a model call", and not masked where it
  was printed.
- the redactor's scm masking removed (`maskExact(scmLiveSecrets())` and
  `scmShapes` in harness_redact.go): the same, plus `project_jobs` (the
  setup job's output).

## Deviations

- projects-scm §8.6, §9.5: a credential written pokes the gate-parked task
  run (`scmCredsReady`) instead of queuing an `inboxWake` — as `bind` does
  since P1: a wake row can be left over to start a turn later, and a coding
  agent's pass takes a wake alone, leaving the prompt beside it.
- projects-scm §15.1: P1's fixture provider (`p1SCM`) keeps its in-memory
  reads; its credential methods go to the scm client bound to K's fake
  provider, so Projects' tests get real credentials through K's gate.

## Tests run / not run

| Command | Result |
|---|---|
| `TILE_TEST_FLAGS="-count=1 -timeout 30m" hack/tile-check.sh agent` (merged branch, before any change) | FAIL — the 16 above; `TestTaskSurvivesThreeCompactions` passed |
| `TILE_TEST_FLAGS="-count=1 -v -run <the 16>\|TestDeviceCodeOnlyToRequester" hack/tile-check.sh agent` (after the fixture) | 16 pass, `TestHarnessTask` FAIL (cause 2) |
| `-count=3 -run TestHarnessTask\|TestPendingSigninKept\|TestSigninParksTask` (after the poke) | ok (9 passes) |
| `-count=1 -run TestSeededTokenNeverStoredTask` | ok; FAIL with `scmRedact` removed, FAIL with the redactor's scm masking removed (above) |
| `TILE_TEST_FLAGS="-race -count=1 -timeout 50m" hack/tile-check.sh agent` (first) | FAIL — `TestTaskSurvivesThreeCompactions`; a data race in `TestTaskRunsNotChats` and in `TestRefreshRewritesFile`'s refresher; a nil-pointer panic in `scmPendingRow` aborted the binary (cause 3) |
| `-race -count=1 -v -run TestTaskRunsNotChats\|TestRefreshRewritesFile\|TestSeededTokenNeverStoredTask\|TestSeededTokenNeverStored\|TestCredGateMatrix\|TestScmBotRule\|TestScrubOnShare\|TestScrubOnStopDeleteForget\|TestPendingSigninKept\|TestCredWriteFailsPartway\|TestHarnessTask\|TestWorktreeFlow\|TestSigninParksTask` (after `scmDB`) | ok (14 passes, no race) |
| `TILE_TEST_FLAGS="-race -count=1 -timeout 50m" hack/tile-check.sh agent` (second, whole suite: the first was cut short by the panic) | FAIL — `TestTaskSurvivesThreeCompactions` only (2489 s); no race, no panic |
| `TestTaskSurvivesThreeCompactions` at `7d310f72` (scratch worktree, removed) | `-count=1`: ok; `-race -count=3`: FAIL 3/3, the same timeout ("run #1 to be idle (is running)") — not this programme's |
| `go test ./internal/docscheck` | ok (before and after the last code commit) |
| `make fmt-check vet` | ok (before and after the last code commit) |

Not run here (projects-scm §16.4, the lead's other gate commands): `make
check`, scm-github's `-race` tile-check, `go test ./internal/builtins/...`
and the node tests — the lead's.

## Commits

| Commit | Subject |
|---|---|
| `b942e890` | agent template: Projects' credential step is stepCreds, K's ensureCreds keeps its name |
| `7c8eae16` | agent template: Projects' tests mint their credentials at the fake scm provider |
| `23021c92` | agent template: a credential written pokes the task's parked run, queues no wake |
| `5db7c4c6` | agent template: TestSeededTokenNeverStoredTask — a task's printed token is stored nowhere |
| `0a781675` | agent template: the credentials' code reads the project worker's agent (scmDB) |
| (this) | plans: projects-scm — gate 1's record and §19 |

## Owner questions

None.
