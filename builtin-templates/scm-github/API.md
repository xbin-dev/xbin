# scm-github — GitHub as an scm provider

## 1. What it is

A template (`bx template new scm-github`) whose copies provide service
**`scm`** — the contract in [/docs/scm.md](/docs/scm.md), protocol 1 — from
GitHub: repo credentials for sandboxes, repos, pull requests, issues, CI
(workflow runs, jobs, steps, logs, annotations, reruns), events from
GitHub's webhooks, and polling. The agent template binds it in its `scm` slot; any tile that requests service
`scm` can. /docs/scm.md is the contract; this page is GitHub's side of it.

It works through a **GitHub App** you create or paste once:

- the **global instance** holds the App — its private key, client secret
  and webhook secret (in the vault), installation tokens, setup, the
  policy, and the identity directory (which xbin person is which GitHub
  login);
- **each person's partition** holds that person's own GitHub sign-in (the
  device flow): their refresh token never leaves it.

A copy that isn't partitioned (an xbind without `--isolate`, or
`--no-partition`) keeps no one's sign-in: it hands out the App's bot only.

## 2. Setting it up

1. Instantiate it: `bx template new scm-github` (default path
   `apps/scm-github`).
2. Give it egress to GitHub: `bx bind apps/scm-github net=internet`, or
   narrowed —
   `bx bind apps/scm-github net=internet:github.com,api.github.com,*.actions.githubusercontent.com,*.blob.core.windows.net`.
   Job logs are a redirect to the last two hosts: leave them out and a log
   fetch answers 502 `upstream` naming the host it couldn't reach.
3. Approve `cap:open-links` (the page opens GitHub in new tabs).
4. Publish its hooks on the global instance:
   `bx expose apps/scm-github hooks=apps/traefik --host scm.example.com`
   (`/hook/github` for GitHub's webhooks, `/setup/github` for the App
   manifest's callback; a partition answers 404 on both).
5. Create or paste the App on the page (as a manager — §10):
   - **Create**: name it, give the hooks' public host, pick presets (`ci`:
     reruns — `actions: write`, which also lets the App dispatch, cancel
     and delete runs; `workflows`: `workflows: write`), and **Create on
     GitHub…** posts the App manifest to GitHub in a new tab. GitHub sends
     you back to `https://<host>/setup/github`, which finishes it; with no
     public host yet, paste the address GitHub sent you to into the page.
   - **Paste**: App ID, client ID, a client secret, the private key (.pem),
     and the webhook URL (the exposure + `/hook/github`); the webhook secret
     is made for you if you leave it empty. The tile checks them by signing
     a JWT and asking GitHub for the App, then points the App's webhook at
     the URL with the secret. An App whose webhook is off pastes without a
     URL (GitHub keeps no webhook settings for it, and events wait); to
     paste one with a URL, tick **Active** under Webhook in the App's
     settings first.
6. In the App's settings on GitHub: **Enable Device Flow** (the page's
   **Check** confirms it, and re-reads the App's permissions — as an
   installation accepting new ones does — so a permission changed on
   GitHub after Paste, such as `actions: write`, counts), and leave **Expire user authorization tokens**
   on: with it off GitHub hands out tokens that never expire and nothing
   to rotate them with, so a sign-in is refused (its grant revoked at
   once), and a sign-in kept from before has its grant revoked when its
   8-hour epoch ends.
7. Install the App on the accounts whose repos it serves (the page's
   install link).
8. Wire the agent: `bx bind apps/scm-github agents+=apps/agent` (events)
   and `bx bind apps/agent scm+=apps/scm-github` (the contract).
9. People who use the agent need **read access** on this tile, and — when
   the workspace's `partitionConsent` is on — their consent for the agent
   → scm-github edge ([/docs/partitions.md](/docs/partitions.md) §Calls
   between partitioned tiles).

The App's permissions: `contents: write`, `pull_requests: write`,
`issues: write`, `checks: read`, `statuses: read`, `actions: read` (job
logs), `metadata: read`, and the organization's `members: read`; preset
`ci` adds `actions: write`, `workflows` adds `workflows: write`. Its
events: `pull_request`, `pull_request_review`,
`pull_request_review_comment`, `issue_comment`, `issues`, `push`,
`check_suite`, `check_run`, `status`, `workflow_run`, `workflow_job`,
`member`, `membership`, `organization` (GitHub sends an App its
`installation` events without asking).

## 3. Modes

| Mode | `XBIN_PARTITION` | Holds | Identities handed out |
|---|---|---|---|
| **legacy** | `""` (not partitioned) | the App, installation tokens, setup, policy | `bot` only |
| **global** | `global` | the App, installation tokens, setup, policy, the identity directory | `bot` |
| **user** | `user:<id>` | that person's GitHub sign-in | `person`; `bot` through global under `botForPeople` |

Who is asking is decided from xbind's headers, never a body
([/docs/scm.md](/docs/scm.md) §Who is asking). A call carrying
`X-XBin-Partition: user:…` is never the tile itself at global — not even
with the tile's own path and the admin role.

## 4. Your GitHub sign-in

Open the tile: in your own partition the page is your sign-in. **Sign in
with GitHub** shows a code and GitHub's address; enter the code there
(the code is yours: never type one someone else shows you). The page — or
a consumer, through `POST /scm/signin` — starts it; a token asked for as
you while you aren't signed in starts it too (409 `signin`, with the code
for you).

Then:

- your partition keeps the pair (access and refresh token) in its own
  vault and refreshes it itself (a device-flow token refreshes without the
  App's client secret). A refresh GitHub refuses with
  `bad_refresh_token` ends the sign-in; so does
  `incorrect_client_credentials` — what GitHub answers once your grant is
  revoked (Forget, or revoking the App in your GitHub settings) — when
  GitHub's check of your access token (asked through global) says it no
  longer knows it (404). Then a token asked for as you is 409 `signin`.
  Any other refusal, or that check answering anything else (it still knows
  the token, an error, no answer), is 502 `upstream` and keeps the sign-in;
- the global instance learns your login from GitHub's own answer for a
  token only this App issued (a personal access token or another App's
  token is refused) and keeps `{login, id}` and your partition id — never
  a token;
- **Forget** (`DELETE /scm/signin`, or the page) revokes your grant at
  GitHub — every token handed out for you stops working — and clears it
  here and at global. The grant goes first: if GitHub (or global) refuses
  or doesn't answer, Forget answers that (503 `unavailable`, …) and
  clears nothing, so you can try again. An access token past its expiry
  (8 hours) is refreshed first and the grant revoked with the new one; a
  failed refresh is answered and clears nothing, and a refresh that ends
  the sign-in (above), or a sign-in itself expired, leaves nothing to
  revoke, so Forget clears — revoking the App in your GitHub settings
  doesn't leave a sign-in here you can't forget. A token GitHub no longer knows
  (already revoked) counts as revoked. GitHub's 422 can also mean "try
  later", so on a 422 Forget asks GitHub about the token: unless GitHub
  answers that it doesn't know it (404), Forget is 503 `unavailable` and
  clears nothing.

Deleting a person can't run Forget: their tokens live on until they
expire (at most the epoch, §6).

## 5. Identities and policy

A person's partition hands out the person by default; the global instance
and a copy that isn't partitioned hand out the bot
([/docs/scm.md](/docs/scm.md) §Identities). The policy — global's
`state` `policy`, `GET`/`PUT /api/policy`, the page's Policy section:

| Key | Default | Meaning |
|---|---|---|
| `botForPeople` | `off` | whether a person's partition may get a bot token: `off`; `own-access` — for repos they can push to themselves (read: pull), checked with the collaborator API and their registered login; `on`. With `own-access` or `on` a person can mint a bot token for themselves directly — their own page reaches the relay — outside any consumer's sandbox checks |
| `botRepos` | `["*/*"]` | `owner/name` globs a bot token, or a bot read, may name (any caller); others → 403 `not-allowed` |
| `allowWorkflows` | `false` | whether `workflows: write` may be asked for (an App with the `workflows` preset) |
| `allowedAccounts` | the App's own account (written at setup); `[]` refuses everything | the accounts tokens, reads and events may concern; others → 403 `not-allowed`. An installation elsewhere (a public App can be installed by anyone) is listed as **foreign** with a link to remove it, and served nothing |
| `allowRerun` | `true` | offer `checks.rerun` — only while the App has `actions: write`, so off in effect until then |
| `personTtlMin` | `0` | caps a person token's life: `0` (the epoch decides) or 50 to 1440 minutes — never under the 50 minutes `maxTtlSec` promises a consumer |

A policy change empties the bot token cache: tokens cut under the old one
aren't handed out again. People's partitions read the public half
(`botForPeople`, `allowWorkflows`, `allowedAccounts`, `botRepos`) from the
shared `conf`; global checks everything it relays itself. A partition
keeps the tokens it got for reuse too, so it re-checks every request
against that half before reusing one, and `conf` `public` carries a
`tokenGen` that a policy change, **Revoke all bot tokens** and a new App
move: a partition reuses only tokens of the generation it reads.

## 6. Tokens

- **Bot tokens** are installation tokens, minted per request narrowed to
  the asked repos (one owner) and the preset's permissions (narrowed
  further by `permissions`), cached per (consumer, purpose, installation,
  repos, permissions) and handed out again while 15 minutes (or the
  consumer's `minTtlSec`) are left. They live an hour; `refreshAfter` is 10
  minutes before. Their length is GitHub's to choose (stateless `ghs_`
  tokens run to several hundred characters).
- **Person tokens** are scoped copies of the person's sign-in: global
  scopes them (`POST /applications/{client_id}/token/scoped` needs the
  client secret) to the asked repos and permissions it computes itself.
  They expire at the **epoch's** end — the parent token's expiry less an
  hour — and the parent refreshes only when the epoch can't cover a
  request, so every token of a person rotates together, about every 7
  hours. "Can't cover" counts the request's own margin (15 minutes, or its
  `minTtlSec`): a request asking 50 minutes refreshes the parent up to 40
  minutes before the epoch's other tokens reach their `refreshAfter` (5
  at the default 15). Should a scoped token die with its parent (§13),
  those die that much early.
- **Revocation**: `POST /scm/token/revoke` by value (only the consumer it
  was given to) or by purpose; bot tokens with `DELETE /installation/token`,
  person tokens through global. Best effort upstream: a token GitHub can't
  revoke still dies at its expiry. Every token handed out is recorded until
  it expires or is revoked: a policy change, a new App or a person's new
  epoch ends **reuse** of the tokens before it, never the record, so a
  revoke by value, by purpose or all still reaches them. **Revoke all bot
  tokens** on the page (`POST /api/revoke-all`) revokes every live one.
- **Writes as the bot** (pull requests, comments) use an installation
  token of the tile's own with only what they need, never handed out:
  pull requests and issues: write, and contents: read (GitHub won't open a
  pull request for a token that can't read its branches). Marking a pull
  request ready for review, or turning it back into a draft, goes through
  GitHub's GraphQL API, which wants pull requests and contents: write of
  an installation token: a token with those, used for that alone.
- **Into sandboxes**: a consumer's job — an exec's environment or a 0600
  file outside every repo, never a refresh token, a person's only in their
  own private sandbox ([/docs/scm.md](/docs/scm.md) §Handing a token to a
  sandbox).

## 7. Events and polling

GitHub's webhooks reach the **global instance** (step 4's exposure), which
turns them into the contract's events and delivers each to the tiles that
subscribed — the `events` capability ([/docs/scm.md](/docs/scm.md)
§Events). Polling (`POST /scm/poll`, conditional and cheap: GitHub doesn't
count a 304 against its rate limit) stays the fallback.

**Receipt** (`POST /hook/github`, from ingress — or a manager testing):

- a body of at most 8 MiB, signed (`X-Hub-Signature-256`, checked in
  constant time) with the webhook secret — or, for a day after the secret
  changes, the previous one; anything else is 401 and counts against
  `events.healthy`;
- a delivery for an account outside the policy's `allowedAccounts` (the
  installation's account, else the repo's owner) is dropped before
  anything else is done with it, and counted: a public App can be
  installed by anyone, and such an installation shows as foreign on the
  page;
- each `X-GitHub-Delivery` is taken once per installation (remembered
  seven days, 10 000 per installation, so one installation's traffic never
  pushes out another's);
- `installation` events refresh the installation cache, and `member`,
  `membership`, `organization` and `installation_repositories` drop the
  cached read access they touch (below) — none of these is delivered;
  `ping` answers 200;
- everything else is normalised, matched to subscriptions and queued: 202
  at once.

**Normalisation** (GitHub → event v1):

| GitHub event | kind.action |
|---|---|
| `pull_request` | `pull.opened`, `.closed`, `.merged` (closed and merged), `.reopened`, `.synchronize`, `.ready` (ready_for_review), `.draft` (converted_to_draft), `.edited`; other actions (labels, assignees, review requests) are no event |
| `check_suite` completed, a commit `status` that is final (`success`; `failure` and `error` as `failure`) | `checks.completed` |
| `check_run` | `check.created`, `.in_progress` (created already running), `.completed`, `.rerequested` |
| `workflow_run` | `workflow.requested`, `.in_progress`, `.completed` |
| `workflow_job` | `job.queued`, `.waiting`, `.in_progress`, `.completed` — `data.job.job.steps` as GitHub last reported them |
| `issue_comment` (on a pull request or an issue), `pull_request_review_comment` (`path`, `line`) | `comment.created`, `.edited` |
| `pull_request_review` | `review.submitted`, `.dismissed` |
| `push` to a branch (a tag is no event) | `push.pushed` (`forced`) |
| `issues` | `issue.opened`, `.edited`, `.closed`, `.reopened`, `.labeled` |

- A check suite and a final commit status of the same commit within five
  seconds are **one** `checks.completed`, with the worse conclusion and
  both halves' runs: every `checks.completed` waits those five seconds
  before its first attempt. A status's run is `{id: "status:<context>",
  name: <context>, conclusion, url}`; a suite's runs are read from GitHub
  (its webhook doesn't carry them) before the first attempt — if that
  fails it goes without them.
- `ref.branch` is a branch of this repo, never a **fork's**: a fork's pull
  request runs CI here under the fork's branch name, so a subscription to
  a same-named branch here must never match it. A pull request says whose
  its head is (`data.pull.head.repo` names a fork; when GitHub names no
  head repo — the fork was deleted — the branch was the fork's, so it has
  no `ref.branch`), and so does a workflow run. A check suite, a check run and a job don't: their branch is kept
  only when shown to be this repo's — a pull request they list has its
  head here on that branch, the job's run's head is this repo (its
  `workflow_run`, else GitHub's run), or the branch's head here is the
  commit (asked of GitHub, remembered ten minutes; "not" a minute). A
  fork's, or one GitHub didn't answer for, has no `ref.branch` (it still
  matches by pull request). A commit status names every branch whose head
  the commit is; a subscription matching one of them gets the event with
  `ref.branch` — and `topic` and `summary` — set to it. A merged
  `checks.completed` takes the pull request (its `topic` and `url`) from
  whichever half names one.
- `actor.self` is the App's own bot (`<slug>[bot]`). `actor.association`
  is the commenter's or reviewer's; GitHub's first-timers and mannequins
  are `NONE`, and events that carry none (pushes, CI) say `NONE`.
- Every field is checked — repos, logins, shas, branch names, links
  (http(s), no credentials; anything else is left out). Text people or CI
  wrote (titles, bodies, check output, step names) is untrusted: passed
  through with control characters removed, token-shaped strings
  (`ghp_…`, `ghs_…`, `github_pat_…`, a private key's header) replaced by
  `[redacted]`, bodies clipped at 8 KiB and check output at 4 KiB.
  `summary` names only numbers, logins, branches and shas.

**Subscriptions** (`POST|GET|DELETE /scm/subscriptions`):

- From a **tile** (at global, or an unpartitioned copy): `for: global`;
  the bot must see the repo, within `allowedAccounts` and `botRepos` —
  which also hold when an event is matched, delivered or listed, so
  narrowing the policy stops delivery at once (what was already queued
  outside it is dropped, counted as `policy`).
- From a **person's consumer** (their partition): relayed to global
  (`/partition/subscriptions`) as `for: user:<id>`, kept with the person's
  partition id. Not signed in here: 409 `signin` (a sign-in starts).
  Global takes the person from xbind, never the body; the consumer the
  body names must be one of the tiles bound in `agents`, the repo within
  `allowedAccounts`, and the person's registered GitHub login must be able
  to read it (GitHub's collaborator permission, asked with the App's
  installation token; `none`: 403 `not-allowed`; a repo the App can't see:
  404 `not-found`). An unpartitioned copy keeps no one's sign-in: a
  person's consumer is 403 `identity` there.
- A repeat of a `key` replaces that subscription (200, the same id); one
  lapses 30 days after its last POST. Caps (past one, 429 `limit`; §12):
  2000 per consumer and `for`; a person's 4000 across consumers, and
  people's 16 000 together, so tiles' always have room (20 000 in all).
- A consumer lists and deletes only the subscriptions it made — a
  person's other consumers' are not its own.
- Matching: the repo; `kinds` (`kind` or `kind.action`; none means every
  kind but `workflow`, `job` and `check`); and, when the subscription names
  `branches`, `prs` or `issues: true`, one of those (a branch by exact
  name). `subs` in an event lists the matching subscriptions' keys — a
  subscription without a key by its id.

**Delivery**: one item per (event, consumer, `for`), POSTed to the
consumer's `/adapter/scm/event` through the `agents` binding whose tile
made the subscription. Before a person's item goes:

- their identity at global must still be the one the subscription was
  made with (Forget, or a person re-created under the same id with
  another partition id, deletes their subscriptions and undelivered
  events);
- for a **private** repo, their read access is checked again — cached an
  hour, dropped by the access events above. Lost: the item is dropped and
  every subscription of theirs on that repo deleted.

A delivery pass that has taken a held `checks.completed` is never merged
into: the commit's other half arriving then is its own event.

200 is delivered; 404 is dropped and counted; anything else — or a
consumer that isn't bound (any more) — is retried 10 s, doubling to an
hour, for a day, then dropped and counted. The `tick` cron (every minute)
is registered while anything waits and removed when nothing does. At start
the global instance asks GitHub for the deliveries it failed to make since
the last one that arrived (GitHub keeps three days) and has them sent
again, at most 50, never one already taken.

**Health** (hello's `events`, the page; a person's partition reads
global's): `webhooks` is `active` while the App's webhook points somewhere
and a delivery (a `ping` counts) arrived in the last 24 hours, `inactive`
while it points nowhere (no hooks URL yet), else `unknown`; `healthy` is
active with no bad signature in the last hour; `pollMinMs` 120000.
Managers see the counts — received, duplicates, foreign, bad signatures,
queued, delivered, not found, expired, access lost — at `GET /api/events`.

`GET /scm/events?since=&repo=&limit=&cursor=` (a tile's at global; a
person's relayed): the events delivered or still due to that consumer and
`for` in the last seven days, oldest first, `since` in unix ms of when
they were queued. A person's private-repo events are checked as a delivery
is: their access lost, that repo's are left out (and dropped, with their
subscriptions there); GitHub not answering is 503 `unavailable`.

## 8. CI

- `GET /scm/checks` combines a commit's check runs, its commit statuses
  and its Actions workflow runs (newest 20) with their jobs (latest
  attempt) and steps — at most 4 calls to GitHub at once, every one
  conditional, the whole answer kept 5 seconds.
- **Logs** are served for **completed jobs only**: GitHub serves a job's
  log once it ends (a running one is 409 `in-progress` with the job's page
  on GitHub, where it streams). The log is a redirect to GitHub's storage,
  followed and cut to its last 8 MiB; the tail you ask for is cut from
  that. A completed job's log is kept a few minutes for paging back; an
  `until` before the kept 8 MiB answers empty `text` with `from` where
  they start and `truncated`: paging back ends there.
- **Annotations**: a check run's, 50 at most a page, each message clipped
  to 4 KiB.
- **Rerun**: `checks.rerun` is listed in a person's partition while the
  App has `actions: write` and `allowRerun` is on (global and an
  unpartitioned copy never list it: a rerun there is 403 `identity`); a rerun is made as the asking person, in their
  own partition, with their own sign-in — never the bot (403 `identity`
  elsewhere).

Text CI printed (titles, summaries, step names, annotations, logs) is
passed through untrusted.

## 9. GitHub notes on the contract

- Every call carries `X-GitHub-Api-Version: 2022-11-28`; GitHub's ETags
  are kept (2000 answers) so repeats are conditional.
- GitHub's rate limit is tracked per identity (the App, each installation,
  each person) and per GitHub resource (`core`, `search`, `graphql`): once
  spent, that identity's calls of that resource answer 429 `limit` with
  `retryAfterMs` without calling GitHub — a spent search limit leaves its
  other calls alone. A secondary limit (GitHub's 429, or a 403 with
  `Retry-After` or naming it) blocks the same way for its `Retry-After`
  (a minute when GitHub doesn't say), so the poll and event delivery wait
  too, not only the call GitHub refused. A SAML-protected organization answers 403
  `not-allowed` with `sso.url` to authorize the identity.
- `POST /scm/pulls`: GitHub's 422 "a pull request already exists" answers
  the open one, 200 `existing: true`. `mergeable: null` (GitHub still
  computing) answers `mergeableState: "unknown"`, `retryAfterMs: 3000`.
- Draft ↔ ready goes through GitHub's GraphQL API.
- The comment timeline merges issue comments, reviews (pending ones left
  out) and review comments by time. `author.association` maps GitHub's
  first-timers and mannequins to `NONE`.
- `GET /scm/issues?q=` uses GitHub's search (its own rate limit). `q` is
  words only: a search qualifier in it (`repo:`, `org:`, `is:`, …), `OR`,
  `AND`, `NOT`, quotes and parentheses are dropped, and a hit from any
  other repo than `repo` is left out.
- `GET /scm/repo`'s `protected` is GitHub's branch flag (any protection),
  except for a person who is an admin of the repo: classic protection lets
  admins bypass it unless it enforces admins, which only the Administration
  permission can read (the App has none), so their answer is absent. The
  bot never bypasses classic protection. Rulesets' bypass lists aren't
  read: a ruleset naming the App or a role as a bypass actor still reads
  `true`.
- A repo's `permission` for the bot is the App's own `contents`
  permission (`write`, or `read`): GitHub answers an installation token's
  repos with every permission flag false.
- `GET /scm/repos` as the bot is the global instance's: a person's
  partition lists the person's own repos (`as: bot` there is 403
  `identity`; name a repo instead).
- A check's `suite` is GitHub's check suite id, as a string.

## 10. Page routes and their guards

| Route | Who |
|---|---|
| `/scm/*` | the `consumer` role (a binding) or `admin`: a tile at global or legacy; a person's consumer in that person's partition (not viewed as them: view-as is no one); the person's own page for `/scm/hello` and `/scm/signin*` |
| `GET /api/page` | the page: a person in their partition, a person at global (through their partition), a manager |
| `GET`/`POST /setup/app`, `POST /setup/manifest`, `POST /setup/manifest/code`, `POST /setup/check`, `GET /setup/installations`, `GET`/`PUT /api/policy`, `POST /api/revoke-all` | **managers**, at global or legacy: the owner token; the tile itself; a person whose level on the tile is write or terminal and who isn't viewing as someone. Not in a person's partition (404) |
| `GET /setup/github` | ingress (GitHub's redirect: the flow's state proves it), or a manager loading it at top level |
| `/partition/*` | global only: a person's own partition (its backend, frame or terminal — all the person) |
| `POST /hook/github` | ingress (GitHub), or a manager testing — at global or legacy; a signature always (§7). Not in a person's partition (404) |
| `GET /api/events` | managers: the events' health and counts |
| `POST /tick` | xbind's cron |

A manifest flow's `state` is 32 random bytes, single use, an hour, bound
to the manager who started it. Secrets are write-only: `GET /setup/app`
shows the key's fingerprint, never a secret.

## 11. Trust

Writers and admins of scm-github are in the trust base of everyone who uses
it: they can change its code. The global instance sees a person's access
token only in transit — registering the identity, scoping, revoking — and
never stores or logs it. No secret is written to kv, a log line, an error
or an answer but the one that hands out a token. Webhook and API bodies
GitHub's users wrote are untrusted: passed through, clipped where stated,
never interpreted. A webhook delivery is signed with the App's webhook
secret and taken only for an account the policy serves; every field of it
is checked before it becomes an event (§7). The global instance delivers
a person's events only to a tile bound in its `agents` slot, only while
they can read the repo, and only for the partition that subscribed.

## 12. Limits

100 repos per token, one owner; `minTtlSec` 900 to 3000 (hello's
`limits.minTtlSec` and `limits.maxTtlSec`); pages of at most
100 (annotations 50); 50 items a poll; repo lists of at most 1000, kept 5
minutes; logs: the last 8 MiB, a tail of at most 1 MiB a call; 2000
reusable tokens and 2000 cached GitHub answers; 8000 live tokens recorded,
500 of them one consumer's (a person's relayed bot tokens count as one
consumer); past either, a new one is 429 `limit` until some expire or are
revoked. A commit's statuses: the first 100 contexts. Events: webhook
bodies of 8 MiB; deliveries remembered seven days, 10 000 per
installation; 2000 subscriptions per consumer and `for`, 4000 per person
across consumers, 16 000 people's together, 20 000 in all; 10 000 outbox
items (delivered ones go first), 5000 of them one consumer's pending ones
(past either, its new events are dropped and counted); events kept seven
days, retried for one; a webhook waits at most 5 s on GitHub to show a CI
event's branch is this repo's.

## 13. Spikes and what they decided

Four questions only a live GitHub App can settle. Two were checked
against GitHub with a test App (2026-10-04); for the other two this
version keeps the safe default:

- **The manifest form from a sandboxed frame** (`Origin: null`) and the
  tile's address loaded at top level: GitHub documents only the form POST
  and its `state`. All three ways back are built — the hooks exposure's
  `/setup/github`, the tile's own address, and pasting the address GitHub
  sent you to — and pasting an existing App stays the primary path.
- **A scoped person token after its parent refreshes**: undocumented, so
  the epoch stays — the parent refreshes only when the epoch can't cover a
  request, which a request asking more than 10 minutes' margin reaches
  early (§6): to be checked live.
- **Revoking a stateless installation token** (`DELETE
  /installation/token`): checked — GitHub answers 204 and the token is
  refused (401) from then on. Still best effort here (a revocation that
  doesn't reach GitHub isn't retried); the hour's life bounds it.
- **A running job's log**: checked — while a job runs GitHub's log
  endpoint redirects to storage that has no log yet (404), and there is no
  other public API for a partial log, so a running job is 409
  `in-progress` with its page on GitHub.
