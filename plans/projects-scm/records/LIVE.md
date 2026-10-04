# LIVE — scm-github against real GitHub (G1's owed spikes)

> Status: live — branch `wp/ps-live` (from `projects-scm` at `7dda955d`).

## What ran, against what

- **When:** 2026-10-04, 18:26–19:05 UTC.
- **GitHub:** github.com / api.github.com. The owner's test GitHub App
  (`xbin-test-app`; permissions actions, contents, issues, pull_requests,
  statuses: write; checks, metadata: read; device flow on; user tokens
  expire), installed on one private repo of the owner's (main,
  `feature/hello` with open pull request #1, issues #2–#4, a CI workflow:
  job `pass`, job `fail` with a warning and an error annotation, job
  `slow` ~5 min, `workflow_dispatch` on). Also the owner's fine-grained
  PAT (dispatching, a review, the identity check's negative case) and a
  user-to-server pair from the App's device flow (`person.json`).
- **How:** `builtin-templates/scm-github/_backend/live_test.go`,
  `live_helpers_test.go`, `live_ci_test.go`, `live_person_test.go` — the
  template's own code (its JWT, `gh_client`, `storeApp`, the `/scm/*`
  routes, the relay, `refresh`, Forget) in a global instance and one
  person's partition in memory, relay wired as xbind attributes it, against
  real GitHub; beside them raw calls for what GitHub itself answers. Every
  test skips without `XBIN_GH_LIVE=1` and the credential files
  (`~/.config/xbin-test`): run by hand, never in CI. Nothing logged carries
  a token (`redact` keeps `ghu_…`-style prefixes only); a refreshed pair is
  written back to `person.json` only (0600, atomically).
- **Runs:** `TestLiveAppBot`, `TestLiveReads`, `TestLiveWrites` (several
  times while fixing), `TestLiveS4` (51 s), `TestLivePersonS2` (26 s, 19:29–19:30 UTC, after the owner's device-flow sign-in — the first two codes expired unentered), and last `TestLiveForget` (2026-10-04, passed), which revoked the owner's grant (`XBIN_GH_LIVE_FORGET=1`): the owner's sign-in is gone, so every person check needs a new device-flow sign-in first.

## The spikes

| # | Question | Live answer (2026-10-04) |
|---|---|---|
| S1 | The manifest form POST from a sandboxed frame's new tab; the frame token at top level | **Still owed.** It needs the owner's browser (a frame, a new tab, GitHub's App-creation page) — nothing headless can answer it. The safe default stands: all three ways back are built, Paste stays primary. |
| S2 | Does a scoped user token survive its parent's refresh? Does `/token/scoped` take basic auth with the client id and secret? | **Yes, and yes** (2026-10-04, `TestLivePersonS2`). A scoped token made through the relay (global's basic auth on `/token/scoped`: 200; the person's bearer token there: 404) kept working after its parent was refreshed — `GET` of the repo 200 right after and 10 s later, and GitHub's check of it 200 — while the old parent was 401 and the new one 200. Its `expires_at` is its own 8 h (not the parent's). A scoped token can't make another (401 "A scoped token cannot create another scoped token"); a read-scoped one is refused a write (403). Revoking one scoped token leaves its parent and its siblings working. So the epoch (§5.7) isn't needed for scoped tokens to survive a refresh; it stays as built until the owner decides (Owner questions). |
| S3 | Does `DELETE /installation/token` revoke a stateless `ghs_` token? | **Yes.** A read with the token: 200; `DELETE /installation/token` with it: **204**; the same read after: **401**; the DELETE again: **401**. The template's own path (`POST /scm/token/revoke {token}`, and `POST /api/revoke-all`) left the tokens at 401 too. Revocation of bot tokens is real, not only the hour's bound. (The tokens were 390 characters: `ghs_` and a dotted, JWT-like body.) |
| S4 | Any partial log while a job runs? | **No.** With job `slow` mid-step (dispatched with the PAT), `GET /actions/jobs/{id}/logs` answers **302** to the log storage (`*.blob.core.windows.net`), which answers **404** (BlobNotFound) — the same 15 s later; the run's log archive is **404**. The template's `GET /scm/checks/jobs/{id}/log` answers **409 `in-progress`** with the job's page. The job's check run (same id) reads `in_progress`, 0 annotations; `/scm/checks` shows the run in progress, the job's steps 1/2. `in-progress` stands. |

## Bot tokens, the identity check, Forget's revocations

- **Minted down-scoped:** `POST /scm/token` (read) → `repos` the one repo,
  the read preset's seven permissions, 1 h; `GET /installation/repositories`
  with it lists that one repo (`repository_selection: selected`); a
  contents write with it is 403. A write token with `pull_requests`
  narrowed to read comes back so narrowed. GitHub's mint answer has
  `token, expires_at, permissions, repository_selection, repositories`.
- **Identity check** (`POST /applications/{client_id}/token`, basic auth):
  the PAT → **404**; a made-up `ghu_` → 404; the right body with a bearer
  token instead of basic auth → 404. `checkUserToken(PAT)` and
  `POST /partition/identity {PAT}` → 403 `identity`. As built.
- **Revocation endpoints for tokens the App never issued**
  (`DELETE /applications/{cid}/token` and `/grant`, the PAT or a made-up
  `ghu_`): **404**, never 422. For a token this App issued and already revoked (a scoped token, `TestLivePersonS2`): `DELETE /applications/{cid}/token` **404**, its check 404, the token itself 401 — so G1's fix-round-4 handling (404 gone; 422 checked) matches.
- **Forget** (`TestLiveForget`, 2026-10-04, passed): `DELETE /scm/signin`
  → global's `DELETE /applications/{cid}/grant` **204**, the sign-in and
  the identity cleared. After it: the parent token **401**, a scoped token
  **401**, GitHub's check of the parent **404**; `DELETE
  /applications/{cid}/grant` and `/token` again with the parent: **404**;
  `POST /partition/revoke-grant` again: **204** (404 is gone). The grant's
  refresh token at `POST https://github.com/login/oauth/access_token`
  (`client_id`, `grant_type=refresh_token`, no `client_secret` — exactly
  what the template sends; the same refresh works while the grant is live,
  `TestLivePersonS2`): error **`incorrect_client_credentials`**, not
  `bad_refresh_token`. The template took only `bad_refresh_token` as the
  sign-in over, so a grant revoked elsewhere (the person revoking the App
  in their GitHub settings) left every person token request past the
  access token's expiry a 502 `upstream`, and Forget refused for good once
  the access token had expired — fixed, below.
- **Installation lookup:** an account without the App →
  409 `not-installed`.

## The contract's reads and writes

- **Repos:** as the bot, `permission` was `none` — GitHub answers an
  installation token's repos with every permission flag false (fixed,
  below). `protected` false (no protection on main).
- **Pulls:** `GET /scm/pulls/1`: `mergeable` true, `mergeableState`
  GitHub's (`unstable`, `clean` — passed through), author association
  `OWNER`. A new pull answers `mergeable: null` → `unknown`,
  `retryAfterMs` 3000, then a value within a second or two. Create
  idempotence: same `clientId` → 200 `existing`; no `clientId` → GitHub's
  422 "A pull request already exists for owner:branch." → found → 200
  `existing`; the same `clientId` for another title → 409 `exists`.
  Opening one as the bot failed at first (fixed, below). Ready-for-review
  and back to draft through GraphQL failed as the bot at first (fixed,
  below); GraphQL's errors come as 200 with `errors[].type`
  (`FORBIDDEN`, `NOT_FOUND`), as `gh_client` reads them.
- **Comments timeline:** a bot comment (`self: true`, `bot: true`, `NONE`),
  a COMMENT review (`kind: review`, `state: commented`) and its inline
  comment (`kind: review-comment`, `path`, `line`), merged by time.
- **Issues:** the list leaves pull request #1 out; `q=login` searches
  (GitHub's `search` rate resource, `repository_url` the API repo URL the
  template compares with); `q='repo:octocat/hello-world is:pr login'`
  answers the same as `q=login` (qualifiers dropped); `/scm/issues/1` (a
  pull request) 404.
- **Checks combined:** check runs, a commit status (set by the App on
  #1's head: `statuses` has it, counts it), workflow runs with jobs and
  steps, `check` = the job id, `runner` the runner's name; `state`
  pending while a job runs. A branch made from main's commit is a `push`
  run on that commit (so a sha collects runs of every branch at it).
- **Job logs:** completed → 302 to `*.blob.core.windows.net`, followed;
  `bytes`, `from` at a line start, `truncated`. In progress: S4.
- **Annotations:** the warning (`warning`), the error and the runner's
  exit-code line (`failure`), a runner notice (`notice`); `title` empty.
- **Conditional requests:** `ifNoneMatch` → 304; raw, `If-None-Match` →
  304 and `X-RateLimit-Remaining` unchanged (a 304 is free). `POST
  /scm/poll` with the answered etags → `changed: false`. GitHub's ETags
  come weak (`W/"…"`) and strong.
- **Rate limit:** `X-RateLimit-Limit` 5000 for the installation,
  `X-RateLimit-Resource` `core` / `search`; `/rate_limit` lists `core`,
  `search`, `graphql` and a dozen more.
- **Pagination:** issues `per_page=1` → `Link` `rel="next"` with
  `after=<cursor>&page=2` under `/repositories/{id}/…`; the template's
  page cursor (GitHub's `page`) walks it correctly.
- **Rerun (person only):** as the person, failed jobs of a completed run: 202 `{runId, attempt: 2}`; the same again at once: 409 (the run is going again); as the bot: 403 `identity`, refused locally.
- **The person's reads and writes** through the template: registration from GitHub's own answer (`POST /partition/identity`), `/scm/repos` and `/scm/repo` (`permission: admin`, `protected: false`), a pull, issues, checks, a comment (201), a person token (`POST /scm/token`, a fresh token after the parent's refresh), and its revocation by value (204; the token 401 after).

## Fake and code corrections

Each with a non-live test that fails without it (commit `04193f22`):

| GitHub answered | The fake answered | The code | Fix | Pinned by |
|---|---|---|---|---|
| `POST /pulls` with a token without contents: 422 "not all refs are readable" | 201 | the internal write token had pull_requests, issues: write, metadata: read — opening a pull as the bot failed (400 `invalid`) | write token + contents: read | `TestPullCreateIdempotent`; fixture `pull-create-without-contents` |
| GraphQL `markPullRequestReadyForReview` / `convertPullRequestToDraft` with an installation token of contents read: 200 `FORBIDDEN`; with contents and pull_requests: write: done | any token | draft ↔ ready as the bot failed (403) | a separate internal token (pull_requests, contents: write, metadata: read) for that mutation only | `TestReadyForReviewGraphQL`; fixtures `graphql-ready-*` |
| an installation token's repos: every permission flag false | `admin` / the token's contents | the bot's `permission` read `none` | the bot's is the App's contents permission (`write`/`read`); conf `public` gains `botContents` | `TestReposPagination`; fixtures `*-permissions*` |
| `GET /app/hook/config` for an App whose webhook is off: 404 | 200, `url: ""` | Paste failed outright (404 `not-found`) | 404 is no webhook; a PATCH refused the same way says to tick Active | `TestPasteAppWebhookOff`, every legacy-paste test; fixture `hook-config-webhook-off`; and live: Paste without `hookUrl` → 200 |
| a token GitHub doesn't take, on any content route: 401 | 404 | (the person's refresh-on-401 never ran in tests) | — | `TestPersonReadRefreshesOn401`; fixture `installation-token-revoke` |
| revoking or checking a token the App never issued: 404 | 422 (revoke) | handled (404 = gone) | — | fixtures `revoke-*-not-this-apps`, `check-token-*` |
| a revoked grant's refresh token: `incorrect_client_credentials` | `bad_refresh_token` | only `bad_refresh_token` ended the sign-in: a token request 502 `upstream`, Forget refused for good once the access token expired | on `incorrect_client_credentials` the partition asks GitHub's check of the access token through global (new relay `POST /partition/check-token`, raw status): 404 ends the sign-in (Forget clears, 204; a token request 409 `signin`); anything else stays 502 `upstream`, nothing cleared (`b9a79693`) | `TestRevokedGrantEndsSignin`; fixture `refresh-after-grant-revoked` (message only: the HTTP status wasn't recorded) |
| revoking an already revoked token of this App again (grant or token): 404 | 204 | handled (404 = gone) | — | fixture `revoke-again-after-grant`; `check-token-after-grant` (404) |
| mint answer has `repository_selection`, `repositories` | absent | not read | — | fixture `mint-answer` |
| a running job's log: 302, the target 404 | 404 at once | checks the job's status first | — | fixture `job-log-running` |


`testdata/github-live.json` keeps GitHub's answers (token-free);
`TestFakeMatchesLiveGitHub` asks the fake each and fails 10 of its 18
App/bot cases against the fake as it was; Forget's three cases (21 now)
failed two against the fake before `b9a79693`.

## Still owed

- **S1** (the owner's browser).
- Whether `PATCH /app/hook/config` works on an App whose webhook is off
  (not tried: it would change the owner's App); the template now says to
  tick Active first if GitHub refuses it.
- A SAML/SSO-protected organization (`X-GitHub-SSO`), GHES, a protected
  default branch, a rate limit actually spent, an installation on an
  organization (members/access events) — the test App has none of them.
- Webhooks and events (G2's).

## Owner questions

- **The bot's draft token holds contents: write.** GitHub's GraphQL
  won't mark a pull request ready (or turn it back into a draft) for an
  installation token without contents: write, so the tile now mints one
  with pull_requests and contents: write for that mutation alone, never
  handed out. The alternative is refusing `draft` changes as the bot
  (`as: person` only). Default: the token, as built.
- **The epoch, now that S2 says scoped tokens survive a refresh.** The
  epoch (§5.7) caps a scoped token's expiry at its parent's next refresh
  because nobody knew whether it would outlive it; live, it does, with its
  own 8 h life. The epoch could go (scoped tokens would live their full
  hour-or-more, fewer re-mints), or stay as a bound on how long a token
  lives after the person's sign-in moves on. **Answered:** the owner
  keeps the epoch (2026-10-04): the cap is the safer bound (§5.7
  unchanged).
- **When to run `TestLiveForget`** (it revokes the owner's grant).
  **Answered:** run 2026-10-04, last (above).

## Commits

| Commit | Subject |
|---|---|
| `04193f22` | scm-github: what live GitHub answered — pull writes, drafts, the bot's repo permission, Paste without a webhook |
| `17281685` | scm-github: live checks against real GitHub, run by hand; S3 and S4 answered |
| `9952d923` | plans: projects-scm — the live checks' record, G1's spikes and §19 |
| `37f41479` | plans: projects-scm — S2 answered live: a scoped token survives its parent's refresh |
| `b9a79693` | scm-github: a grant revoked elsewhere ends the sign-in on refresh |
| (this commit) | plans: projects-scm — Forget answered live; the epoch stays |

Checks before the last commit: `go test ./internal/docscheck` ok;
`make fmt-check vet` ok; the touched tests by name (non-live) ok;
`hack/tile-check.sh scm-github` with the new tests ok; every `TestLive*`
skips without `XBIN_GH_LIVE`. The full `-race` suite and `make check`:
at the gate.
