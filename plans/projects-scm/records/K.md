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
  payload (a 5xx whose body names no refusal → `unavailable`, 502
  included, 501 → `unsupported`; another bare status → its contract
  refusal; `Retry-After` kept), 304 → `Checks` answers nil, nil,
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
    `GH_CONFIG_DIR`; the row `live` (§9.3, §9.4). A live token is
    reused only while its scope (sorted repos, access, permissions) is
    the request's: a repo added or `workflows` turned on mints again.
  - `scmGitConfig` (the helper reset, the helper, `useHttpPath false`,
    the token's author) and `scmProjectEnv` (`GH_CONFIG_DIR`) (§8.5, §9.4).
  - A 409 `signin`: the device code kept in memory per (person, provider)
    = `scmPendingSignin`; the task `ws=signin`; the `creds` job polls
    `SigninPoll` at the provider's interval (`jobOutcome{WaitMs}`), at
    most 15 minutes, then writes the credential, clears the code, puts the
    task back and wakes its parked run (§8.6, §9.5, §9.8).
  - `scmCredsDue` from `project_creds`, through the gate's handle `t`
    only: no live row for the task's sandbox, another identity, or
    refresh/expiry within 10 min; never for a project not active or
    without a repo (`project_repos` counted through `t`, by the frozen
    DDL; no table, no repo) (§8.6, §9.5; see Deviations).
  - The refresher: an `ownerLoops` entry re-minting each token at the
    provider's `refreshAfter` or 75 % of its life, while a task of the
    project is at work (§9.5; see Deviations).
  - Scrubbing = `scmScrubCreds`: both files emptied (0600, zero bytes),
    the purpose revoked, the token dropped (kept masked until it
    expires), `scrubbed` with why. Files that can't be emptied (the
    manager refused a write): revoked anyway, but the row stays `live`
    with why and `refresh_ms` 0 — selected by every later scrub (a share
    stays refused), due for the gate — and the gate's refusal doesn't
    mark it `blocked` either. A `…tmp` a failed rename left is removed.
    A row whose project `projectsInSandbox` doesn't list there takes its
    uid, provider and owner from `projects` (frozen DDL), else its
    purpose, and is revoked at every bound provider when the provider is
    unknown. Triggers placed: a share through the
    agent (before the PATCH, refusing it when a file can't be emptied;
    and after it, the `stopCredsIn` sibling), stop and archive (before the
    lifecycle call), delete (`projectSandboxGone`: revoke only), Forget
    (before the provider forgets), the gate's refusal; the `scrub` job
    kind for P1's and P2's triggers, its why §14.1's word (`scmScrubWhy`:
    the job's `step` when the queuer put one of the words there, else a
    repo's job `repo-removed`, an archived or deleting project `archive`
    or `delete`, a task's fork `delete`, else `left`) (§9.6).
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
- **The `scrub` job's why:** P1 and P2 may put the word in the job's
  `step` when they queue it (`share`, `stop`, `archive`, `delete`,
  `repo-removed`, `forget`, `left`); otherwise K infers it (above).
- **What K reads of P1's tables** (the frozen DDL of §6.2–§6.6):
  `projects` (`uid`, `scm`, `owner` of a row's project the store didn't
  list in its sandbox), `project_repos` (a count, through the gate's
  `t`: whether `scmCredsDue` may say due), `project_members` (count, for the person gate; unreadable = refused),
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
  `TokenHold` (called before each `POST /token` is answered),
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
- §9.2, §9.6 (fix round 2): a credential's write and every scrub take a
  per-sandbox lock (`scmHoldSandbox`). `ensureCreds` asks the gate under
  it before minting, mints holding only the credential's key lock, then
  takes the sandbox's lock again, re-reads the sandbox, asks the gate
  again (a refusal now: the token just handed out is revoked, the row
  blocked) and holds the lock through the files and the row. A share's
  PATCH (`handlePatchSandbox`, from before the pre-PATCH scrub to after
  the post-PATCH one) and a stop's or archive's lifecycle call
  (`scmScrubOnAction`, deferred past `Lifecycle`) hold it too, so no
  credential is written between a scrub that found nothing and the change
  that shares, stops or archives the sandbox. The spec's "gate re-checked
  before every write" alone left that window open. Not taken: holding the
  sandbox's lock across the provider's `Token` (a share would wait up to
  30 s on a provider). A scrub's revocation does run under the lock
  (`scmRevoke`: best effort, at most 15 s per credential), so a share or
  stop through the agent waits that long at most on a slow provider —
  fix round 3 corrected the fix-round-2 wording that no provider call
  ever holds it. (Fix round 2 also turned down a row before the files;
  fix round 3 adds one, next item.)
- §9.1, §9.6 (fix round 3): a first write — no live row for that host in
  the sandbox — puts its row `live` with `refresh_ms` 0 (due) before the
  first file (`scmPendingRow`). A write that then fails partway is
  scrubbed at once under the sandbox's lock (`scmWriteFailed`: both files
  emptied, both `…tmp` removed, the purpose revoked) and the row put back
  as it was (none: removed); files it can't empty keep the row `live` and
  due, so a share stays refused. A first file the manager refused
  outright (4xx: nothing landed) is only revoked. A refresh that fails
  keeps the older live row: it points at the same files, and its scrub
  takes out the new `…tmp` too (revoking the purpose revokes both values),
  while the older token keeps working for the task at work. The row write
  after the files failing drops the token from memory (the next ensure
  mints again); the live row there covers the files. The spec said
  nothing about a write that fails partway.
- §16.2 (fix round 2): `handlePatchSandbox` gains one line taking that
  lock; the stop/archive trigger becomes one deferred line,
  `defer scmScrubOnAction(r.Context(), ref, action)()`, replacing the
  three-line `if` the verifier flagged as unrecorded.
- §9.8, §7.2 (fix round 2): `writeSCMErr` passes a `signin` refusal's
  payload only to the partition's own person (`scmSigninFor`: a person,
  not view-as, in their own partition); anyone else — view-as, an
  element, another person on `GET /projects/scm/repos` — gets the status,
  words and refusal without `signin`. The spec said where the code is
  shown, not what the routes anyone may call pass through.
- §9.1 (silent, fix round 2): a project whose `host` is '' (the frozen
  DDL's default) takes its credential's host before minting from its row
  in that sandbox, else from the provider's hello when it lists exactly
  one host (`scmCredHost`); only when neither can tell is the token's
  host taken, as before. The key lock and the reuse check then name the
  host the row and the scrubs use.
- §7.2: `GET /projects/scm/bot` in a person's partition answers the
  partition's own (unread) rule to a manager; only `PUT` is 409 there.
  The sign-in routes answer 403 to view-as and to components (`needUser`).
- §9.2 (silent): a bot sandbox whose owner is no person (the agent made it
  as itself) counts only its members and the team; the person gate's "no
  members" reads `project_members` and refuses when it can't.
- §9.5: `scmCredsDue` reads `project_creds` and, through the gate's `t`,
  a count of `project_repos` (frozen DDL; no table = no repos): a project
  without repos is never due. "From `project_creds` alone" would make it
  due for good (ensure writes nothing for it, so the gate would park its
  turns behind a `creds` job forever), and asking `projectReposOf`
  instead reads the database off the gate's transaction — with the one
  connection, it waits forever (fix round 1).
- §9.6 (silent): a scrub whose files can't be emptied (the manager
  refused the write) revokes anyway but leaves the row `live` with why and
  `refresh_ms` 0, not `scrubbed`: every later trigger selects it again,
  so a refused share stays refused until a scrub really empties the
  file, and the gate has the revoked value replaced or blocked before the
  next turn; the gate's own refusal leaves such a row live too. A scrub
  also removes the `…tmp` a failed rename left (fix round 1).
- §9.1: a live token is reused only while its scope — sorted repos,
  access, permissions — is what the project asks now; a repo added or
  `workflows` turned on mints again at the next ensure (§8.3's `creds`
  before `repo`), whatever the old one's time left (fix round 1).
- §9.1: "a 5xx without a body → `unavailable`" applied to every 5xx whose
  body names no refusal (502 from xbind's gateway included), but 501,
  which the contract (scm_types.go) keeps for `unsupported`; `upstream`
  only when a body says it (fix round 1).
- §14.1 (silent): the `scrub` job takes its why from the job's `step`
  when its queuer put one of §14.1's words there, else infers it (a
  repo's job `repo-removed`; archived/deleting `archive`/`delete`; a
  task's fork `delete`; else `left`) — the job has no field for it.
- §9.7: `addMessage`/`rewriteMessage` apply `redactText`, so message rows
  now also mask the shapes `tokenShape` already masked in the coding
  agents' output (`sk-ant-…` keys) — kept: §9.7 names `redactText` as
  `scmRedact`, and a key in a stored message is a leak either way.

## Tests run / not run

| Command | Result |
|---|---|
| `go test ./internal/docscheck` | ok |
| `make fmt-check vet` | ok |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` | (at `82ab8717`) the package exceeded go's default 10-minute test timeout under `-race` on this shared 4-core machine (every test it reached had passed but `TestNoTickers`, which caught the refresher's ticker — fixed in `16610255` — and `TestTaskSurvivesThreeCompactions`, below) |
| `TILE_TEST_FLAGS="-race -count=1 -timeout 45m" hack/tile-check.sh agent` (at `16610255`) | vet ok; every test passes (1864 s) but `TestTaskSurvivesThreeCompactions` ("timed out waiting for run #1 to be idle"), which fails the same way under `-race` on the base commit `7d310f72` run alone (its 5 s wait is too short for the race detector) and passes without `-race` — not K's |
| `TILE_TEST_FLAGS="-count=1 -v -run TestScm\|TestCred\|TestRefresh\|TestScrub\|TestRedact\|TestSeeded\|TestPending\|TestSignin\|TestGitCredential\|TestSCMCreds\|TestNoTickers" hack/tile-check.sh agent` | ok — `-v` lists TestScmHelloCache, TestScmRefusalDecode, TestScmClientRoutes, TestScmProvidersRoute, TestCredGateMatrix, TestCredGateBlocks, TestCredsDue, TestScmBotRule, TestCredFilesMode0600, TestGitCredentialFill, TestRefreshRewritesFile, TestSCMCredsSchemaMigratesTwice, TestScrubOnShare, TestScrubOnStopDeleteForget, TestRedactPatternsAndLive, TestSeededTokenNeverStored, TestPendingSigninKept, TestSigninStateStartsNothing, TestNoTickers and the harness's existing credential tests, all PASS; the same set also passes under `-race` |
| `make check` | the guards pass (fmt-check, vet, js-check, shellcheck, large-files, …); `make test` fails in `internal/boot`, `internal/runner` (backends "exited before [they] listened") and `internal/tilesbx` (`TestAdmissionCaps`, `TestResourceReconcile`: the memory admission against this 7 GiB host). K changes nothing under `internal/`, `cmd/` or `sdk/`. With `TMPDIR` on the tmpfs instead of `/work/tmp-tests/k`, `internal/boot` and `internal/runner` pass; `internal/tilesbx` still fails on host memory — an environment failure, not K's |
| **Fix round 1** (at `b4eb6b87`): `go test ./internal/docscheck`; `make fmt-check vet` | ok |
| `TILE_TEST_FLAGS="-count=1 -v -run TestScm\|TestCred\|TestScrub\|TestGitCredential\|TestRefresh\|TestPending\|TestSignin\|TestRedact\|TestSeeded\|TestSCMCreds\|TestNoTickers" hack/tile-check.sh agent` | ok — the new TestScrubOnShareRefused, TestCredsDueInTx, TestCredsCoverNewRepo, TestScrubJobWhy, `TestScrubOnStopDeleteForget/archive` and `/a_project_the_store_doesn't_list_there` PASS with the rest |
| the same new tests against the previous commit's `scm.go`, `scm_creds.go`, `scm_ensure.go`, `scm_scrub.go` (by hand, not committed) | each fails: the share retry's row `scrubbed`, `scmCredsDue` "waited on the database" in a transaction, one token for two repos, no revoke for an unlisted project, the scrub job's why, a 502 decoded `upstream`, TestGitCredentialFill "not written again" |
| `TILE_TEST_FLAGS="-race -count=1 -timeout 50m" hack/tile-check.sh agent` | vet ok; every test passes (1744 s) but `TestTaskSurvivesThreeCompactions` ("timed out waiting for run #1 to be idle"), the same pre-existing `-race` failure as above; it passes alone without `-race` |
| `make check` | the guards pass (fmt-check, vet, js-check, shellcheck, large-files); `make test` fails in `internal/boot`, `internal/runner` ("the backend exited before it listened"), `internal/tilesbx` (`TestAdmissionCaps`, `TestResourceReconcile` on host memory; `TestStdioNewestWins`, an i/o timeout under load) — K changes nothing under `internal/`, `cmd/` or `sdk/` (`git diff 7d310f72 --stat -- internal cmd sdk` is empty) |
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
  `setStatus`, `setStatusOnly`), `B/sandbox_routes.go` (in
  `handlePatchSandbox` the sandbox lock's line, the pre-PATCH `if` and the
  post-PATCH scrub; one line each in `handleDeleteSandbox` and
  `handleSandboxAction`),
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

- **Commit `343a3403`'s subject** ends "(work in progress)", against
  §16.5's explanatory subjects; the history is left as the verifier saw
  it — reword it when the integration branch takes K (suggested:
  "agent template: the scm client, the credential gate, minting, files
  and scrubs").

## Commits

| Commit | Subject |
|---|---|
| `343a3403` | agent template: the scm client and project credentials (work in progress) |
| `5781a36f` | agent template: scm credential tests — the gate matrix, the bot rule, scrubs, sign-in, the seeded token |
| `82ab8717` | agent template: API.md §scm providers and credentials |
| `16610255` | agent template: the scm refresher sleeps until the next token's instant |
| `5ed9f899` | plans: projects-scm — K's record and deviations |
| `b4eb6b87` | agent template: scm credential fixes — an unemptied scrub stays live, scope re-mints, the gate's question in its transaction |
| `0009b115` | plans: projects-scm — K's record after fix round 1 |
| `b7762eff` | agent template: scm credential writes and scrubs take turns per sandbox; a device code only to its person; a hostless project's host |
| `c00f1558` | plans: projects-scm — K's record after fix round 2 |
| `7c1e3e0f` | agent template: a credential write that fails partway is scrubbed; the hello's protocol refusal tested |
| this commit | plans: projects-scm — K's record after fix round 3 |

## Fix round 2

The verifier's five findings (`last-verify-issues.json`), each checked
against the code before fixing:

- **Blocker — a share, stop or archive racing a credential's write:
  real.** `ensureCreds` read the sandbox and ran the gate before minting,
  then wrote the files and only then the row; the share's scrubs select
  `live` rows, so a share during the `Token` call found nothing, answered
  200, and the token was then written into the shared sandbox (the new
  test reproduces it on the previous code: the write after the share
  succeeded). Fixed with the per-sandbox lock described in Deviations
  (§9.2, §9.6 fix round 2): the gate asked again after minting under the
  lock held through files and row; the share's PATCH and the stop/archive
  lifecycle call hold it; `scmScrub`, Forget and `projectSandboxGone` take
  it per row/sandbox; `scmScrubRow` now always expects it held (the old
  "box passed = lock held" convention is gone). New
  `TestScrubRacesEnsure` (share, archive): the provider's `Token` blocks
  (`fakeSCM.TokenHold`), the share/archive answers 200 without waiting
  on it, and afterwards no file holds the token, no row or memory entry
  is live, and for the share the row is `blocked` and the purpose
  revoked. The archive case passes on the previous code too (the fake
  manager, like the real one, refuses a file write into an archived
  sandbox); it pins that the lock doesn't deadlock the lifecycle path. A
  stop followed by a write still starts the sandbox again (the manager's
  documented "a stopped sandbox starts on a file operation"): the write
  comes after the stop's scrub, for a task that needs the sandbox anyway.
- **Major — the device code reaching other callers: real.** `writeSCMErr`
  marshalled the whole `*scmError`; `GET /projects/scm/repos` (anyone)
  in alice's partition passed `signin.userCode` to view-as, elements and
  other people. Fixed: `writeSCMErr(w, r, err)` drops `Signin` unless
  `scmSigninFor(callerOf(r))`. New `TestSigninCodeOnlyToPerson` (fails on
  the previous code: view-as got `ABCD-1234`). API.md says it.
- **Minor — the lock key with an empty host: real.** Fixed by
  `scmCredHost` (Deviations, §9.1 fix round 2); `scmBlock`'s scrub now
  relies on the sandbox lock, whatever the host. New `TestCredsHostless`
  (fails on the previous code: two tokens minted for one credential).
- **Minor — the stop/archive trigger's three lines unrecorded: real.**
  Now one deferred line (`scmScrubOnAction`); the lock's line in
  `handlePatchSandbox` is recorded with it (§19, Deviations).
- **Minor — commit `343a3403`'s subject and the "5 commits" count:
  partly fixed.** The count was in the previous builder's summary, not in
  this record; the branch has 7 commits before this round and 9 after it
  (the table above). The subject stays as is: rewording it needs a
  history rewrite of a branch others have verified, which this round
  doesn't do; the integration branch rewords it (suggested subject under
  Merge risks).

Checks run in fix round 2 (each targeted; nothing over two minutes):

| Command | Result |
|---|---|
| `go test -count=1 -run 'TestScrubRacesEnsure\|TestSigninCodeOnlyToPerson\|TestCredsHostless'` (the agent backend in a scratch copy shaped as `hack/tile-check.sh` builds it) | ok |
| the same three against the previous commit's `scm_ensure.go`, `scm_scrub.go`, `scm_creds.go`, `scm_routes.go`, `sandbox_routes.go` (by hand, not committed) | `TestScrubRacesEnsure/share`, `TestSigninCodeOnlyToPerson`, `TestCredsHostless` fail as described; `/archive` passes (above) |
| `go test -count=1 -run 'TestScm\|TestCred\|TestScrub\|TestGitCredential\|TestRefresh\|TestPending\|TestSignin\|TestRedact\|TestSeeded\|TestSCMCreds\|TestNoTickers\|TestSandbox'` | ok (12 s) |
| every test in `sandbox*_test.go` and `harness_creds*_test.go` (68, by name) | ok (20 s) |
| `go test -race -count=1 -run 'TestScrubRacesEnsure\|TestSigninCodeOnlyToPerson\|TestCredsHostless\|TestScrubOn\|TestCredGateBlocks'` | ok |
| `TILE_TEST_FLAGS="-count=1 -v -run TestScrubRacesEnsure\|TestSigninCodeOnlyToPerson\|TestCredsHostless\|TestScrubOnShare\|TestScrubOnStopDeleteForget" hack/tile-check.sh agent` | ok — vet, and the five tests (with every subtest) PASS (36 s) |
| `go test ./internal/docscheck`; `make fmt-check vet` | ok |

Full -race suite and make check: at the gate (lead).

## Fix round 3

The verifier's four findings (`last-verify-issues.json`), each checked
against the code before fixing:

- **Blocker — a write that fails partway leaves an unrevoked token with
  no row: real.** `ensureCreds` wrote both `…tmp` files, ran the rename
  and git-config script, and only then put the row; on a failure it only
  dropped the token from memory. Every scrub selects `live` rows, so the
  `…tmp` (or the renamed files) kept a working token that no scrub would
  find, and a share then answered 200. Fixed as in Deviations (§9.1, §9.6
  fix round 3): a pending `live` row (`refresh_ms` 0) before the first
  file when none is live there, and `scmWriteFailed` scrubbing a failed
  first write under the lock (or only revoking when the manager refused
  the first file outright), then putting the row back. Fix round 2 had
  turned down a row before the files on the grounds that the lock covers
  it; the lock covers a race, not a failed write. New
  `TestCredWriteFailsPartway`: `rename` (the script fails: no row,
  revoked, no file holds the token, the share answers 200),
  `refused outright` (a 409 on the first file: no row, revoked),
  `after a scrub` (the row goes back to `scrubbed`/`stop`), `second file`
  (the second `…tmp` can't be written or removed: the first `…tmp` is
  removed, the row stays `live` and due, revoked, the share is refused
  502 until the obstruction goes, then 200 and `scrubbed`/`share`), and
  `refresh` (the older row is kept, nothing revoked; the share then
  empties every file and revokes). On the previous `scm_ensure.go` the
  first four fail (no revoke, the `…tmp` holds the token); `refresh`
  passes there too — it pins that a failed refresh doesn't kill the token
  a task is using. `TestScrubRacesEnsure/archive` covers the outright
  refusal path through the manager's own 409.
- **Minor — "a provider's call never holds the sandbox lock": real.** A
  scrub's `scmRevoke` (15 s bound) runs under it in every scrub, the
  gate's refusal and `projectSandboxGone`. Corrected the wording, not the
  code: the `scmHoldSandbox` and `ensureCreds` comments, the §19 line
  and Deviations now say a provider's `Token` never holds it and a
  revocation does (best effort, at most 15 s per credential). Moving the
  revocation past the lock would touch every scrub caller for a bounded
  wait on a path that already calls the manager.
- **Minor — the hello's protocol refusal untested: real.** The fake
  provider gains `Speaks` (the protocol its hello names);
  `TestScmRefusalDecode` asserts `protocol` with the provider's
  `protocols` and words, and `TestScmProvidersRoute` lists a third
  provider speaking protocol 2 with `refusal: protocol`. The mislabelled
  block is now "a hello at a missing route".
- **Minor — the Commits table's placeholders: real.** Filled in
  (`b7762eff`, `c00f1558`); this round's two commits are named by
  subject (a commit can't name its own hash).

API.md §scm providers and credentials says what a failed write does.

Checks run in fix round 3 (each targeted; nothing over two minutes):

| Command | Result |
|---|---|
| `go test -count=1 -run TestCredWriteFailsPartway` (the agent backend in a scratch copy shaped as `hack/tile-check.sh` builds it) | ok (5 subtests) |
| the same against the previous commit's `scm_ensure.go` | `rename`, `refused outright`, `after a scrub`, `second file` fail as described; `refresh` passes |
| `go test -count=1 -run 'TestScm\|TestCred\|TestScrub\|TestGitCredential\|TestRefresh\|TestPending\|TestSignin\|TestRedact\|TestSeeded\|TestSCMCreds\|TestNoTickers\|TestSandbox'` | ok (13 s) |
| `go test -race -count=1 -run 'TestCredWriteFailsPartway\|TestScmRefusalDecode\|TestScmProvidersRoute\|TestScrubRacesEnsure\|TestScrubOnShare\|TestScrubOnStopDeleteForget\|TestCredsHostless\|TestRefreshRewritesFile'` | ok (53 s) |
| `TILE_TEST_FLAGS="-count=1 -v -run TestCredWriteFailsPartway\|TestScmRefusalDecode\|TestScmProvidersRoute\|TestScrubRacesEnsure\|TestScrubOnShare" hack/tile-check.sh agent` | ok — vet, and the six matched tests (every subtest) PASS |
| `go test ./internal/docscheck`; `make fmt-check vet` | ok |

Full -race suite and make check: at the gate (lead).

## Owner questions

None. (The decisions K took where the spec was silent are in Deviations,
each with what was built.)
