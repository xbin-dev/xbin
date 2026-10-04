# K — scm client and credentials

> Status: live — branch `wp/ps-k` (from `projects-scm` at `7d310f72`).

## What was built

- **The `scm` slot** — agent `xbin.json`: `"scm": {"kind": "http",
  "service": "scm", "multi": true}`, commented like `sandboxes`
  (projects-scm §4.2, §9.1).
- **The client** — `B/scm.go`: `XBIN_IFACE_SCM` read like the sandbox
  managers' slot (service `scm` only, one per name, `#inst` instances),
  `scmFor`/`scmBound` set in `init()`, `scmConn` implementing every
  `scmAPI` method over HTTP (§4.7–§4.11), hellos cached 60 s / 10 s after a
  failure, a provider of another protocol or without `credentials`
  refused, refusals decoded into `*scmError` with the HTTP status and the
  payload (a 5xx without a body → `unavailable`, a bare status → its
  contract refusal, `Retry-After` kept), 304 → `Checks` answers nil, nil,
  the ETag header filling an answer's `etag`; `Revoke` reveals a token only
  into its own body (§9.1, §14.3).
- **The gate and the bot rule** — `B/scm_gate.go`: `scmProjectAs` (policy,
  else person in a person's partition for personal and membership
  projects, bot elsewhere); `scmCredWhy(p, as, ref, box)` with every row of
  §9.2 — a team's seed never, the home's path, a fork's source's hosted
  history, `from_seed`, `projectLevelOf` for every user of a bot sandbox
  (fail closed), team visibility only with a team-participant project, no
  shares; refusal words (`scmWhyWords`); `scmBotAllowed` filled (managers,
  else a person the `scm_bot_rule` setting names with a matching
  `owner/name` glob, case-insensitive, never view-as, never read in a
  person's partition) (§9.1, §9.2, V15).
- **Credentials** — `B/scm_creds.go` (state, `project_creds`, env and git
  config), `B/scm_ensure.go` (minting and writing), `B/scm_scrub.go`
  (scrubbing, the two job kinds, the refresher):
  - `project_creds` (§6.8) registered in `schemaAdds`; metadata only.
  - `ensureCreds` = `scmEnsureCreds`: re-reads the sandbox from its
    manager and runs the gate before every write (a refusal: emptied,
    revoked, `blocked`, the task `failed` with "credentials can't go into
    ‹sandbox›: ‹why›"); mints `POST /scm/token {repos, access: write, as,
    purpose: proj:<uid>:<sandboxRef>, permissions: workflows when the
    policy asks}`; masks the value at once; writes `<host>.cred` and
    `gh/hosts.yml` as `…tmp` files (0600, mkdirs) renamed by one `Run` that
    also makes the directories 0700 and applies the git config to every
    base repo and clone-mode checkout there; `P/.xbin/env` with
    `GH_CONFIG_DIR`; the row `live` (§9.3, §9.4).
  - `scmGitConfig` (the helper reset, the helper, `useHttpPath false`,
    the token's author) and `scmProjectEnv` (`GH_CONFIG_DIR`) (§8.5, §9.4).
  - A 409 `signin`: the device code kept in memory per (person, provider)
    = `scmPendingSignin`; the task `ws=signin`; the `creds` job polls
    `SigninPoll` at the provider's interval (`jobOutcome{WaitMs}`), at
    most 15 minutes, then writes the credential, clears the code, puts the
    task back and wakes its parked run (§8.6, §9.5, §9.8).
  - `scmCredsDue` from `project_creds` alone: no live row for the task's
    sandbox, another identity, or refresh/expiry within 10 min; never for
    a project without repos or not active (§8.6, §9.5).
  - The refresher: an `ownerLoops` entry re-minting each token at the
    provider's `refreshAfter` or 75 % of its life, while a task of the
    project is at work (§9.5; see Deviations).
  - Scrubbing = `scmScrubCreds`: both files emptied (0600, zero bytes),
    the purpose revoked, the token dropped (kept masked until it
    expires), `scrubbed` with why. Triggers placed: a share through the
    agent (before the PATCH, refusing it when a file can't be emptied;
    and after it, the `stopCredsIn` sibling), stop and archive (before the
    lifecycle call), delete (`projectSandboxGone`: revoke only), Forget
    (before the provider forgets), the gate's refusal; the `scrub` job
    kind for P1's and P2's triggers (§9.6).
  - `creds` and `scrub` registered in `projectJobKinds` (§8.3).
- **Redaction** — `B/harness_redact.go`: `gh[pousr]_[^\s"'@]{30,}` and
  `github_pat_[^\s"'@]{22,}` beside `tokenShape`, and every live (and
  recently replaced) value exactly, in `redactor.apply` — so every
  redactor (the harness stdout reader, the harness log, `redactText`)
  masks them same-length; `scmRedact` = `redactText`; one line each in
  `B/db.go` `addMessage` and `rewriteMessage` (§9.7).
- **Routes** — `B/scm_routes.go`, registered through `routeTables`: `GET
  /projects/scm`, `GET /projects/scm/repos`, `GET|PUT /projects/scm/bot`,
  `GET|POST|DELETE /projects/scm/signin`, `GET
  /projects/scm/signin/{pollId}` (§7.2 K rows).
- **The fake scm provider** — `B/scm_fake_test.go`,
  `B/scm_fake_routes_test.go`: §4's routes from memory (§15.1).
- **API.md** §scm providers and credentials (§7.5, §17.1).

## Seams for others

- `scmFor(provider)`, `scmBound()` — live; `scmOnly(name)` answers the one
  named, or the only one bound.
- `scmEnsureCreds`, `scmScrubCreds`, `scmProjectEnv`, `scmGitConfig`,
  `scmRedact`, `scmPendingSignin`, `scmCredsDue`, `scmBotAllowed` — filled
  as §14.1 declares. **`scmGitConfig`'s pairs:** a key that appears again
  adds a value — apply its first occurrence with `git config --unset-all`
  then `--add`, later ones with `--add` (P1's `repo` script loop:
  `scm_creds.go` `scmGitEnv`/`scmGitScript` do exactly this and may be
  reused; the env carries `GIT_CFG_<i>_K`, `_V`, and `_F=1` on a first
  occurrence). `user.name`/`user.email` come only once this process holds
  a token, so `repo` may get the helper lines alone; the `creds` job adds
  the author.
- `projectJobKinds["creds"]`, `projectJobKinds["scrub"]`; `ownerLoops`
  gains `scmRefreshLoop`.
- `scmCredStatus(d *DB, pid) []StatusCred` — `ProjectStatus.creds` for
  P1's `GET /projects/{pid}/status` (not a §14 seam; a helper P1 may call).
- `projectSandboxGone(ref)` — called by `handleDeleteSandbox`.
- **What K reads of P1's tables** (the frozen DDL of §6.2, §6.4–§6.6):
  `project_members` (count, for the person gate; unreadable = refused),
  `project_tasks` (`ws`, `error`, `n`, `run_id`, `sandbox_ref`),
  `project_checkouts` (clone-mode paths), `project_jobs` (whether `bind`
  finished). **What K writes there:** `project_tasks.ws`/`error`/
  `updated_ms` (signin, failed by the gate, and back), each followed by
  `projectTaskChanged(t, pid, n, "ws")`. Each query tolerates the table's
  absence.
- **The fake provider** (for E and V): `newFakeSCM(t, provider)`,
  `bindSCM(t, fakes...)`, levers `SetSignedIn`, `CompleteSignin(out...)`,
  `AddRepo`, `AddPull`, `AddComment`, `SetChecks`, `SetJobLog(id, text,
  running)`, `SetAnnotations`, `AddIssue`, `FailNext(route, *scmError)`
  (a zero `scmError` answers a bare 503), fields `Caps`, `Identities`,
  `Person`, `LongTokens`, `TokenTTL`, `NoCache`; reads `Tokens()`,
  `Requests(route)`, `Revoked()`, `Subscriptions()`; `Deliver(h, ev)`
  POSTs an event v1 to `/adapter/scm/event` as the provider's tile with
  the channel role. As a person, a call without a sign-in is 409 `signin`
  (starting one); a bot rerun is 403 `identity`; repos spanning owners
  400 `invalid`.
- Test fixture `credFixture(t, mode)` (scm_creds_test.go) — a project in
  a fake-manager sandbox, alice's partition or global, with P1's tables
  made from the frozen DDL and P1's seams stubbed for one project.

## Changelog text

- agent template: a new **`scm`** interface slot (service `scm`,
  docs/scm.md): bind one or more scm providers — `bx bind apps/agent
  scm+=apps/scm-github`. `GET /projects/scm` lists them as your space sees
  them; `GET /projects/scm/repos` lists the repos you may use;
  `GET|PUT /projects/scm/bot` is the **scm bot rule** — who besides the
  agent's managers may name which repos for the provider's bot, where the
  agent works as the bot (its shared instance, an unpartitioned agent);
  `/projects/scm/signin` signs you in to the provider from your own space,
  and `DELETE` forgets the sign-in after taking every credential of your
  projects out of their sandboxes.
- agent template: a project's sandbox gets a short-lived, scoped
  credential for the project's repos — a git credential helper and
  `GH_CONFIG_DIR`, in files outside every repo — only where everyone who
  can use that sandbox may act through that identity; refreshed while a
  task works, scrubbed when the sandbox is shared, stopped, archived or
  deleted through the agent, and when you forget your sign-in.
- agent template: every message the agent keeps, and the coding agents'
  output and logs, mask GitHub token shapes (`ghp_`, `gho_`, `ghu_`,
  `ghs_`, `ghr_`, `github_pat_`) and every credential the agent handed
  out; message rows now also mask Anthropic key shapes (`sk-ant-…`), as
  the coding agents' output already did.

## Decision text

For the program's decision (projects-scm §17.3), K's part:

- **The credential lives in memory and in the sandbox, nowhere else.**
  `project_creds` keeps who, until when and why, never the value; a
  restart re-mints. Rejected: a vault entry per project (a second copy to
  scrub, and the token is minutes from expiry anyway).
- **The gate reads the sandbox as its manager reports it at every write**,
  never a label (any editor can set one) and never a cached box: a share
  made at the manager behind the agent's back blocks the next write and
  empties what was there.
- **Refresh by one loop, only while a task works.** A per-token timer
  dies with the process, and refreshing every idle project's token
  forever at the shared instance would spend the installation's rate
  limit for nothing; the workspace gate re-mints before the next turn.
- **Scrub on share is blocking, revocation best effort:** a credential
  that can't be emptied refuses the share (others could read it);
  a provider that can't be reached doesn't (the file is empty; the token
  expires within hours).
- **Masking is global and same-length:** the live set is read by every
  redactor, so the harness reader's offsets stay the adapter's; replaced
  tokens stay masked until they expire. Applied at `addMessage` and
  `rewriteMessage`, which every tool result passes.
- **The bot rule is a setting of the bot home's own database**
  (`scm_bot_rule`), managers only, globs matched without case; a person's
  partition never reads it.

## Deviations

Also dated in projects-scm §19.

- §16.2: new files beyond the table's — `B/scm_ensure.go`,
  `B/scm_scrub.go`, `B/scm_fake_routes_test.go`, and `scm_test.go`,
  `scm_gate_test.go`, `scm_creds_test.go`, `scm_scrub_test.go` — to keep
  each file under 800 lines.
- §9.5: one `ownerLoops` entry (`scmRefreshLoop`: one timer at the
  earliest token's instant, woken when a token is handed out — the backend
  has no tickers, `TestNoTickers`) instead of a timer per token, refreshing only while a task of the project is at work
  (running, awaiting, sleeping or waiting for a person). It runs once P1
  places the `ownerLoops` call site; before that, P1's
  `scmEnsureCreds` before each git step and the gate's `scmCredsDue` keep
  credentials fresh.
- §9.2/§9.5: the spec says K sets `ws=signin` and `ws=failed` but not how
  a task gets back: K writes `project_tasks.ws` and `error` itself (from
  `pending|queued|preparing|ready` only), via `projectTaskChanged`, and
  once a credential is written moves a task it held back to what it was
  (remembered in memory; after a restart `ready` when a `bind` job
  finished and none of `sandbox|repo|fork|prepare|setup|bind` is live,
  else `preparing`), and queues an `inboxWake` for a run parked with
  `pendingState.kind = "project"` (the "bind-like finish" §8.6 names).
- §9.4: a repeated key is `--unset-all` + `--add` on its first occurrence
  and `--add` after; `--replace-all` as written would fold the empty
  reset line and the helper into one.
- §9.3: `hosts.yml` quotes `oauth_token` and `user` as single-quoted YAML
  (the same values; safe for any character but a newline).
- §9.1 (silent): one token per (project, sandbox, host) for all the
  project's repos there; repos spanning owners on one host get the
  provider's 400 `invalid`, the creds job's error. Per-owner tokens would
  need path-scoped helpers (`credential.https://host/owner/…`).
- §9.6: a share through the agent is refused (502) when a credential
  can't be emptied, like `readyForShare`; the post-PATCH sibling and the
  stop/archive triggers don't refuse (best effort). The share trigger is a
  three-line `if` (gofmt), not one line.
- §7.2: `GET /projects/scm/bot` in a person's partition answers the
  partition's own (unread) rule to a manager; only `PUT` is 409 there.
  The sign-in routes answer 403 to view-as and to components (`needUser`).
- §9.2 (silent): a bot sandbox whose owner is no person (the agent made it
  as itself) counts only its members and the team; the person gate's "no
  members" reads `project_members` and refuses when it can't.

## Tests run / not run

| Command | Result |
|---|---|
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` | (at `82ab8717`) the package exceeded go's default 10-minute test timeout under `-race` on this shared 4-core machine (every test it reached had passed but `TestNoTickers`, which caught the refresher's ticker — fixed in `16610255` — and `TestTaskSurvivesThreeCompactions`, below) |
| `TILE_TEST_FLAGS="-race -count=1 -timeout 45m" hack/tile-check.sh agent` (at `16610255`) | vet ok; every test passes (1864 s) but `TestTaskSurvivesThreeCompactions` ("timed out waiting for run #1 to be idle"), which fails the same way under `-race` on the base commit `7d310f72` run alone (its 5 s wait is too short for the race detector) and passes without `-race` — not K's |
| `TILE_TEST_FLAGS="-count=1 -v -run TestScm\|TestCred\|TestRefresh\|TestScrub\|TestRedact\|TestSeeded\|TestPending\|TestSignin\|TestGitCredential\|TestSCMCreds\|TestNoTickers" hack/tile-check.sh agent` | ok — `-v` lists TestScmHelloCache, TestScmRefusalDecode, TestScmClientRoutes, TestScmProvidersRoute, TestCredGateMatrix, TestCredGateBlocks, TestCredsDue, TestScmBotRule, TestCredFilesMode0600, TestGitCredentialFill, TestRefreshRewritesFile, TestSCMCredsSchemaMigratesTwice, TestScrubOnShare, TestScrubOnStopDeleteForget, TestRedactPatternsAndLive, TestSeededTokenNeverStored, TestPendingSigninKept, TestSigninStateStartsNothing, TestNoTickers and the harness's existing credential tests, all PASS; the same set also passes under `-race` |
| `make check` | the guards pass (fmt-check, vet, js-check, shellcheck, large-files, …); `make test` fails in `internal/boot`, `internal/runner` (backends "exited before [they] listened") and `internal/tilesbx` (`TestAdmissionCaps`, `TestResourceReconcile`: the memory admission against this 7 GiB host). K changes nothing under `internal/`, `cmd/` or `sdk/`. With `TMPDIR` on the tmpfs instead of `/work/tmp-tests/k`, `internal/boot` and `internal/runner` pass; `internal/tilesbx` still fails on host memory — an environment failure, not K's |
| `TestSeededTokenNeverStored` with `scmRedact` made the identity (by hand, not committed) | fails, naming `messages`, `messages_fts`, `messages_fts_content` — the test sees a leak |

Not run: the gate-1 test `TestSeededTokenNeverStoredTask` (the lead's,
after P1 is merged: K's own seeded test covers a plain run's bash). No
live GitHub or scm-github here (G1 builds it in parallel): the client is
tested against the fake provider only. The partitioned end-to-end suites
and the UI harness are owed by the program (projects-scm §15.4).

## Merge risks

- **Edits outside K's files:** `B/harness_redact.go` (the shapes, the live
  set, `maskExact`; the header gains a paragraph), `B/db.go` (one line
  each in `addMessage` and `rewriteMessage` — P1 edits `deleteOneRun`,
  `setStatus`, `setStatusOnly`), `B/sandbox_routes.go` (four scrub calls in
  `handlePatchSandbox`, `handleDeleteSandbox`, `handleSandboxAction`),
  agent `xbin.json` (the slot after `sandboxes`), API.md (its own `###`
  only), projects-scm §19 (K's lines replace "(none yet)"; other WPs'
  lines will conflict there — keep all).
- **P1's tables:** K queries and updates `project_tasks`,
  `project_checkouts`, `project_jobs`, `project_members` by the frozen DDL.
  If P1's columns differ, K's queries fail soft (no ws change; the person
  gate refuses). The test DDL in `scm_creds_test.go`
  (`scmTestProjectTables`) is `IF NOT EXISTS`: with P1 merged its schema
  wins; check K's tests still pass on it.
- **Names K defines that P1 might too:** `projectSandboxGone` (named by
  §9.6 as K's), `scmCredStatus`; every other K identifier carries the
  `scm` prefix (`scmPolicyOf`, not `policyOf`).
- **Behaviour others see:** every `addMessage`/`rewriteMessage` now masks
  the token shapes — a test elsewhere that stores a `ghp_…`- or
  `sk-ant-…`-shaped string in a message and reads it back would see it
  masked (none on this branch does). `TestSeededTokenNeverStoredTask`
  (gate 1) relies on P1's `projectEnv` carrying `scmProjectEnv` and on
  P1's job output passing `scmRedact`.
- The `ownerLoops` refresher and the `creds`/`scrub` jobs run only once
  P1's call sites (worker, takeover) are in.
- API.md links `/docs/scm.md`, which G1 writes: dangling on this branch
  alone.

## Commits

| Commit | Subject |
|---|---|
| `343a3403` | agent template: the scm client and project credentials (work in progress) |
| `5781a36f` | agent template: scm credential tests — the gate matrix, the bot rule, scrubs, sign-in, the seeded token |
| `82ab8717` | agent template: API.md §scm providers and credentials |
| `16610255` | agent template: the scm refresher sleeps until the next token's instant |
| (this commit) | plans: projects-scm — K's record and deviations |

## Owner questions

None. (The decisions K took where the spec was silent are in Deviations,
each with what was built.)
