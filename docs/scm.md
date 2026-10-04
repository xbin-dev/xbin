# scm — repo hosting as a service

An **scm provider** is a tile that knows a code host — GitHub, a GitLab, a
Gitea — and holds the credentials for it: an app's keys, people's sign-ins,
which accounts the app is installed on. A **consumer** is a tile that needs
the host: the agent template (its projects push branches, open pull
requests and follow CI), anything else. The split:

- **The provider knows the host and holds the credentials:** app keys,
  people's sign-ins and refresh tokens, installation lookups.
- **The consumer knows who is asking, why, and which sandbox a token goes
  to.**

The provider never sees a sandbox; the consumer never sees an app key or a
refresh token. The builtin provider is the **`scm-github` template**
(`bx template new scm-github`; its API.md is GitHub's side of this page).
This page is the contract, **protocol 1**: a provider implements it, a
consumer programs against it.

## Wiring

A provider provides the service, with a role of its own that reaches only
the `/scm/` routes (its own page keeps the admin role):

```jsonc
"provides": { "scm": { "kind": "http", "service": "scm", "role": "consumer" } },
"expose": { "roles": {
  "consumer": "Use GitHub through this tile: the scm contract's /scm/* routes",
  "admin": "The tile, its owner and its managers: the page, setup and policy" } }
```

A consumer requests it; several providers can be bound at once:

```jsonc
"interfaces": { "scm": { "kind": "http", "service": "scm", "multi": true } }
```

The owner binds them — `bx bind apps/agent scm+=apps/scm-github`, or the
binding panel — and the binding is the grant: the consumer holds the
`consumer` role. Its backend finds its providers in `XBIN_IFACE_SCM`
(`[{provider, url, service}]`). Every route below is under `<url>/scm/`.

Events need the reverse binding too: the provider's `agents` slot (service
`agent-inbox`) bound to the consumer — `bx bind apps/scm-github
agents+=apps/agent` — through which it delivers them (§Events).

## Who is asking

A provider decides who is asking from the headers xbind verified
(inbound `X-XBin-*` headers are stripped), never from a body:

| Caller (headers) | Class | Consumer key |
|---|---|---|
| `X-XBin-From` another tile, no `X-XBin-Partition` (a consumer that isn't partitioned) or `global` | **tile** | (From, Deployment, "") |
| `X-XBin-From` another tile, `X-XBin-Partition: user:<id>` — a partitioned consumer's person reaching this provider's partition of the same person | **person's consumer** | (From, Deployment, Partition-Id) |
| the provider's own partition calling its own global instance (`From` = the provider itself, `Partition: user:<id>`, role reader or writer) — its backend, **or the person's frame or terminal there**, which arrive identically | **relay** | — |
| a person through the provider's own page (a frame, `X-XBin-User` set) | **person** (page) | — |
| the owner token, or the provider itself at global (`From` = self, no partition or `global`) | **self/owner** | — |

A global instance never treats a call carrying `X-XBin-Partition: user:…`
as itself, even when `X-XBin-From` is its own path ([partitions.md](partitions.md)
§The global instance and people's partitions): such a call whose role
isn't reader or writer is refused, never self. A relay call's **body is the
person's own input** — their frame or terminal in the provider's partition
reaches global with exactly the headers its backend has — so global
re-checks every field of it as it would a stranger's. View-as
(`X-XBin-Viewed-By`) is never a person here. A consumer can't assert a
person: the only person a call can be is the partition it arrives in.

A provider keys per-consumer state — cached tokens, subscriptions — on
(`X-XBin-From`, `X-XBin-Deployment`, `X-XBin-Partition-Id`)
([partitions.md](partitions.md) §Providers).

## Identities

`as` is `person` or `bot`. It defaults to `person` where a person is
asking (a person's consumer, a person's page), `bot` elsewhere.

| The call reaches | `as: person` | `as: bot` |
|---|---|---|
| a person's partition of a partitioned provider (a person's consumer, or their page) | that person — **409 `signin`** if not signed in | only under the provider's policy `botForPeople`: `off` (default) 403 `identity`; `own-access`: allowed for repos the person can push to (read: pull), checked with their own identity; `on`: allowed |
| a global instance, from a tile | **403 `identity`** | yes: the binding is the grant; the provider's repo policy applies |
| an unpartitioned provider, from a tile | 403 `identity` | yes, as above |
| an unpartitioned provider, from a person's consumer | 403 `identity` (no partition holds the person's sign-in) | `botForPeople`, with `own-access` treated as `off` (no person token to check with) |

Rules every provider keeps:

- A provider that keeps people's sign-ins keeps each one **in that
  person's partition** and nowhere else. An unpartitioned provider is
  bot-only.
- A person's identity is only ever the partition's own person; naming
  another is 403 `identity`.
- Asserted people (`Sbx-User`-style headers, body fields) and view-as are
  never accepted.
- Writes (`POST /scm/pulls`, `PATCH`, comments) made `as: bot` use the bot;
  made `as: person`, the person's token.
- **A consumer that acts for people with the bot authorises each person
  itself.** The binding grants the consumer tile the bot's view; it says
  nothing about which of the consumer's own users may use it. A consumer
  whose instance has no person identity (a tile at global, an unpartitioned
  consumer) and lets people name repos, read them or get tokens for them
  must decide per person what they may name — otherwise everyone who can
  use the consumer gets the bot's view.

## Conventions

- JSON bodies; times are Unix **milliseconds**; a `repo` is `owner/name`;
  a `host` is a hostname (`github.com`).
- **No custom request headers**: parameters go in the query (GET, DELETE)
  or the body (POST, PATCH).
- **Pagination:** `limit` (default 30, at most `limits.pageMax`, 100) and
  an opaque `cursor`; a list answers `{items, next}` — `next` absent on
  the last page.
- **Conditional reads:** every GET answers an `ETag` header and the same
  value in the body's `etag`; a request with `ifNoneMatch=<etag>` (query)
  or `If-None-Match` gets **304** with no body. A provider keeps the host's
  own ETags where it can (GitHub doesn't count a 304 against its rate
  limit).
- **Bodies the host's users wrote** — issue and pull request text,
  comments, review bodies, check output, logs, commit messages — are
  untrusted text. A provider passes them through (clipped where stated) and
  never interprets them; a consumer clips, redacts and frames them wherever
  a model or a page sees them.
- Unknown request fields are ignored; unknown answer fields must be ignored
  by consumers.

## Errors

`{error, refusal, retryAfterMs?, …}` with the status below. `error` is for
people; `refusal` for programs. The sandbox-manager contract's refusals
([sandbox-manager.md](sandbox-manager.md) §Conventions) keep their meaning;
this contract adds six. A consumer treats an unknown refusal by its status.

| refusal | status | meaning | payload |
|---|---|---|---|
| `protocol` | 400 | unknown protocol version | `protocols` |
| `invalid` | 400 | a bad field: repos spanning owners, more than `limits.reposPerToken`, a permission wider than the preset, an unknown `access` | — |
| `not-found` | 404 | no such repo, pull request, issue, check, subscription — or not visible to this identity | — |
| `not-allowed` | 403 | the identity may not do this; the provider's policy refuses it; a SAML/SSO block | `sso: {url}` for SSO |
| `exists` | 409 | a `clientId` reused for a different request; a rerun of a run still going | — |
| `precondition` | 412 | a conditional write lost | — |
| `limit` | 429 | the host's rate limit, or the provider's | `retryAfterMs` |
| `unsupported` | 501 | a capability this provider doesn't offer | — |
| `unavailable` | 503 | the host or the provider is down or starting | `retryAfterMs` |
| **`signin`** | 409 | `as: person` and the person isn't signed in — a sign-in was started | `signin: {url, userCode, expiresAt, pollId, intervalMs}` |
| **`not-installed`** | 409 | the app isn't installed on the repo's account | `install: {url, owner}` |
| **`identity`** | 403 | not as that identity here | `identities: [...]` (what this caller may use) |
| **`setup`** | 503 | the provider isn't set up (no app yet; device sign-in disabled) | — |
| **`upstream`** | 502 | the host answered an error the provider can't map | `upstream: {status, message}` |
| **`in-progress`** | 409 | a job's log asked for while it runs, from a host with no partial-log API | `url` (the host's live log page) |

## `GET /scm/hello?protocol=1`

```json
{
  "protocol": 1,
  "protocols": [1],
  "scm": {"name": "scm-github", "title": "GitHub", "version": "1.0.0", "kind": "github"},
  "hosts": ["github.com"],
  "caps": ["credentials", "repos", "pulls", "issues", "checks", "checks.rerun", "events", "poll", "partitions"],
  "identities": ["person", "bot"],
  "you": {
    "identities": ["person"],
    "default": "person",
    "person": {"login": "octocat", "id": 583231},
    "signinExpiresAt": 1790000000000
  },
  "app": {"slug": "acme-xbin", "installUrl": "https://github.com/apps/acme-xbin/installations/new", "configured": true},
  "events": {"webhooks": "active", "healthy": true, "lastDeliveryAt": 1789990000000, "pollMinMs": 120000},
  "limits": {"reposPerToken": 100, "minTtlSec": 900, "maxTtlSec": 3000, "pageMax": 100, "pollItems": 50},
  "notes": []
}
```

- An unknown `protocol` → 400 `protocol` with `protocols`.
- `caps`: only `credentials` is required; a route of a capability not
  listed answers 501 `unsupported`. `partitions` is listed only by a
  partitioned instance (it keeps sign-ins per person). A dotted cap
  (`checks.rerun`) is an optional part of its capability.
- `identities` is what this instance hands out at all; `you.identities`
  what this caller may ask for now (§Identities); `you.default` the `as` it
  gets by default; `you.person` is `null` when no person is signed in here
  (or none can be).
- `events` says how events reach consumers: `webhooks` `active`,
  `inactive` or `unknown`; `pollMinMs` the shortest polling interval the
  provider wants.
- `notes` are sentences for people (why an identity is missing, setup left
  to do). The protocol grows additively: new caps, new fields.

## Credentials

### `POST /scm/token`

```json
{"repos": ["acme/web", "acme/api"], "access": "write", "as": "person",
 "permissions": {"workflows": "write"}, "minTtlSec": 1800, "purpose": "proj:k3x9:coding-sandbox|s-12"}
```

`repo` (one) or `repos` (at most `limits.reposPerToken`, one owner);
`access` `read` or `write`; `permissions` may only narrow the preset (or add
`workflows: write` where the provider's policy allows it); `minTtlSec`
(default and minimum `limits.minTtlSec`; a provider refuses one its host's
tokens can't meet with 400 `invalid`, and says its ceiling as
`limits.maxTtlSec`); `purpose` is the consumer's own key — tokens are
cached and revoked by it.

**200**:

```json
{"host": "github.com", "username": "x-access-token", "token": "ghu_…",
 "expiresAt": 1790000000000, "refreshAfter": 1789999400000,
 "identity": {"kind": "person", "login": "octocat", "id": 583231},
 "author": {"name": "octocat", "email": "583231+octocat@users.noreply.github.com"},
 "repos": ["acme/web", "acme/api"],
 "permissions": {"contents": "write", "pull_requests": "write", "issues": "read", "checks": "read",
                 "statuses": "read", "actions": "read", "metadata": "read"}}
```

- Presets: `read` — contents, metadata, pull_requests, issues, checks,
  statuses, actions: read. `write` — contents, pull_requests: write;
  issues, checks, statuses, actions, metadata: read (a sandbox's git and
  `gh` need no more; a consumer opens pull requests and comments through
  the provider). Never `administration`; `workflows: write` only on request
  and only where the provider's policy allows it.
- A repeat with the same (consumer, purpose, repos, permissions) answers
  the cached token while it has at least `max(minTtlSec, 15 min)` to run.
- `refreshAfter` is when the consumer should ask again (at least 10 min
  before `expiresAt`). A consumer never needs, and never gets, a refresh
  token.
- Refusals: 409 `signin` (and a sign-in is started), 409 `not-installed`,
  403 `identity`, 403 `not-allowed` (the provider's policy), 400 `invalid`,
  404 `not-found` (a repo the identity can't see), 503 `setup`.

### `POST /scm/token/revoke`

`{token}` (one this consumer was given; matched by hash) or `{purpose}`
(every live token of this consumer for it). **204**; 404 `not-found` when
nothing matches. Revocation is best effort upstream; the provider forgets
the token either way. A token stays revocable until it expires: a provider
may stop handing one out again (its policy changed, its sign-in
refreshed) but never stops answering a revoke for it.

### Sign-in: `POST|GET|DELETE /scm/signin`, `GET /scm/signin/{pollId}`

Only where a person is asking (a person's consumer or their page);
elsewhere 403 `identity`.

- `POST /scm/signin` → `{"state": "done", "identity": {…}}` when signed in,
  else starts (or continues) a device sign-in →
  `{"state": "pending", "signin": {"url": "https://github.com/login/device", "userCode": "ABCD-1234", "expiresAt": …, "pollId": "p_…", "intervalMs": 5000}}`.
  503 `setup` when the host's device sign-in is off.
- `GET /scm/signin` → the current state without starting one:
  `{state: "none"|"pending"|"done", identity?, signin?}`.
- `GET /scm/signin/{pollId}` →
  `{state: "pending"|"done"|"denied"|"expired"|"error", identity?, error?, retryAfterMs}`;
  asks the host at most once per interval. 404 for an unknown or
  finished-long-ago poll id.
- `DELETE /scm/signin` → **204**: "Forget" — revokes the grant upstream and
  clears the sign-in. Every person token handed out stops working. When
  the host refuses the revocation (or can't be reached) Forget answers
  that refusal and clears nothing, so it can be tried again.

The device code (`userCode`) is shown only to the person it is for; a
consumer never shows it to anyone else.

## Reads

`as` is accepted on every read (query); the identity's view of the host
decides what is visible.

### Repos

- `GET /scm/repos?q=&as=&limit=&cursor=` →
  `{items: [{host, owner, name, cloneUrl, defaultBranch, private, permission, archived, url}], next}`
  — the repos this identity can reach through the app (a person's: those
  of the app's installations they can see; the bot's: the installations'),
  `q` matching `owner/name`. Cached by the provider at most 5 min.
- `GET /scm/repo?repo=&as=` → one, plus `protected` (`true`/`false`;
  absent when the identity can't tell): whether the default branch has
  protection the identity can't bypass.

### Pull requests

- `POST /scm/pulls {repo, head, base?, title, body?, draft?, clientId?, as?}`
  → **201** the pull request; **200** with `existing: true` when one is
  already open for that head. `base` defaults to the default branch.
- `GET /scm/pulls?repo=&state=open|closed|merged|all&head=&base=&as=&limit=&cursor=`
  → `{items, next}`.
- `GET /scm/pulls/{n}?repo=&as=` →

  ```json
  {"number": 42, "url": "…", "title": "…", "body": "…", "state": "open", "draft": true,
   "mergeable": null, "mergeableState": "unknown", "retryAfterMs": 3000,
   "head": {"ref": "xbin/k3x9/3-fix-login", "sha": "9fceb02…", "repo": "acme/web"},
   "base": {"ref": "main", "sha": "…"},
   "author": {"login": "octocat", "association": "MEMBER"},
   "labels": [], "updatedAt": 1789990000000, "etag": "…"}
  ```

  `state` is `open`, `closed` or `merged`. `mergeable: null` with
  `mergeableState: "unknown"` and `retryAfterMs` while the host computes
  it. `author.association` is `OWNER`, `MEMBER`, `COLLABORATOR`,
  `CONTRIBUTOR` or `NONE`.
- `PATCH /scm/pulls/{n} {repo, title?, body?, state?: open|closed, draft?, as?}`
  → the pull request (`draft` maps to the host's ready-for-review and
  to-draft operations).
- `GET /scm/pulls/{n}/comments?repo=&since=&as=&limit=&cursor=` → one
  timeline, oldest first:
  `{items: [{id, kind: comment|review|review-comment, author, body, state?, path?, line?, url, createdAt}], next}`.
- `POST /scm/pulls/{n}/comments {repo, body, as?}` → **201** the comment.
- **No merge route and no approve route in protocol 1.**

### Checks

The `checks` capability: what CI says about a commit, down to the steps of
each job, so a consumer can show progress, logs, annotations and links
without knowing the host. Every text here that a build or a check printed
(`title`, `summary`, step names, annotation messages, log text) is
untrusted.

- `GET /scm/checks?repo=&ref=&as=` → everything reported on `ref` (a
  branch, a pull ref `pull/<n>`, or a sha — a branch or pull ref resolves to
  its head sha first):

  ```json
  {"sha": "9fceb02…", "ref": "xbin/k3x9/3-fix-login", "state": "pending",
   "counts": {"total": 7, "success": 4, "failure": 1, "pending": 1, "neutral": 1, "skipped": 0, "cancelled": 0},
   "workflowRuns": [
     {"id": "7001", "name": "ci", "event": "push", "status": "in_progress", "conclusion": "",
      "url": "https://github.com/acme/web/actions/runs/7001", "startedAt": 1789990000000,
      "updatedAt": 1789990090000, "attempt": 1, "headSha": "9fceb02…", "headBranch": "xbin/k3x9/3-fix-login",
      "jobs": [
        {"id": "88001", "name": "test (ubuntu)", "status": "in_progress", "conclusion": "",
         "url": "https://github.com/acme/web/actions/runs/7001/job/88001",
         "startedAt": 1789990010000, "completedAt": 0, "runner": "ubuntu-24.04", "check": "88001",
         "steps": [{"n": 1, "name": "Set up job", "status": "completed", "conclusion": "success",
                    "startedAt": 1789990010000, "completedAt": 1789990012000},
                   {"n": 4, "name": "go test ./...", "status": "in_progress", "conclusion": "",
                    "startedAt": 1789990030000, "completedAt": 0}]}]}],
   "checks": [
     {"id": "88001", "name": "test (ubuntu)", "app": "github-actions", "status": "in_progress", "conclusion": "",
      "url": "https://github.com/acme/web/runs/88001", "detailsUrl": "https://github.com/acme/web/actions/runs/7001/job/88001",
      "title": "", "summary": "", "annotations": 0, "startedAt": 1789990010000, "completedAt": 0,
      "suite": "77", "job": "88001"},
     {"id": "88100", "name": "codecov/patch", "app": "codecov", "status": "completed", "conclusion": "failure",
      "url": "https://github.com/acme/web/runs/88100", "detailsUrl": "https://app.codecov.io/…",
      "title": "62% of diff hit (target 80%)", "summary": "…", "annotations": 3,
      "startedAt": 1789990001000, "completedAt": 1789990002000, "suite": "78", "job": ""}],
   "statuses": [{"context": "ci/jenkins", "state": "success", "url": "https://jenkins…", "description": "Build #12 passed",
                 "updatedAt": 1789990003000}],
   "etag": "…"}
  ```

  | Field | Meaning |
  |---|---|
  | `state` | `none` (nothing reported), `pending` (any check, job or status not completed), `failure` (any conclusion `failure`, `timed_out`, `action_required`, `startup_failure`, or a status `failure`/`error`), else `success`. Computed over `checks` and `statuses`. |
  | `counts` | over `checks` and `statuses`: each in exactly one bucket — `success`; `failure` (as `state`); `pending` (not completed); `neutral`; `skipped`; `cancelled` (`cancelled`, `stale`). `total` is their sum. |
  | `workflowRuns` | the host's CI runs for this sha (GitHub Actions workflow runs), newest first, at most 20, each at its latest `attempt`. `status` `queued` \| `waiting` \| `in_progress` \| `completed`; `conclusion` as checks' (`""` until completed). Absent (not empty) when the host has no such thing. |
  | `jobs` | a run's jobs (at most 100), in the host's order. `runner` the runner's label or name (`""` while queued). `check` the id of the check run that reports this job (GitHub: the same number), or `""`. |
  | `steps` | a job's steps, `n` the host's step number. A queued job has none. Progress is `steps` completed of all. |
  | `checks` | every check run on the sha (at most 100), including those that report a job (`job` = its id): a consumer that shows `jobs` folds those into their job. `annotations` is their count; `title`/`summary` the check's own output (at most 4 KiB each). `url` is the check on the host, `detailsUrl` where the check says to look. `suite` is opaque. |
  | `statuses` | commit statuses, the latest per `context`. A combined status with none is left out. |

  A provider fills what its host has: a host with no step detail sends
  `steps: []`; one with no commit statuses sends `statuses: []`.
- `GET /scm/checks/jobs/{id}/log?repo=&tailBytes=&since=&until=&as=` → a
  job's log:

  ```json
  {"id": "88001", "text": "…", "bytes": 482113, "from": 416577, "complete": true, "truncated": true,
   "url": "https://github.com/acme/web/actions/runs/7001/job/88001"}
  ```

  `bytes` is the whole log's length; `text` its bytes from
  `max(since, end − tailBytes)` to `end` = `until` (a byte offset; default
  and at most `bytes`), `tailBytes` default 65536, at most 1048576, `since`
  default 0; the start is cut forward to a line start and `from` is where
  `text` starts — a viewer pages back with `until=<from>`; `truncated` when
  anything before `from` was left out. A provider that keeps only a long
  log's end answers an `until` before what it keeps with empty `text`,
  `from` where the kept part starts and `truncated`: paging back ends
  there. `complete` is false while the job
  runs and the host serves partial logs. The text is as the host serves it
  (escape codes kept; a consumer strips them) and untrusted.
  **409 `in-progress`** with `url` while the job runs on a host that serves
  logs only once a job completes (GitHub); 404 `not-found` for an unknown
  job or a log the host no longer keeps.
- `GET /scm/checks/runs/{id}/annotations?repo=&limit=&cursor=&as=` → a
  check run's annotations, `{items: [{path, startLine, endLine, level,
  title, message}], next}`: `level` `notice` \| `warning` \| `failure`;
  `message` at most 4 KiB; `limit` at most 50.
- `POST /scm/checks/rerun {repo, runId, failedOnly, as?}` → **202**
  `{runId, attempt}`: re-run a workflow run (`failedOnly`: only its failed
  jobs, and what depends on them). The optional cap **`checks.rerun`**;
  without it 501 `unsupported`. 409 `exists` while the run is still in
  progress. A provider may refuse it for an identity (403 `identity`):
  a rerun spends CI minutes and can deploy, so consumers offer it to
  people, as themselves, never to a model.

### Issues

- `GET /scm/issues?repo=&state=&labels=&since=&q=&as=&limit=&cursor=` →
  `{items: [{number, title, body, state, labels, author, url, updatedAt}], next}`;
  pull requests are left out. `q` is words to match, never the host's
  search syntax: a provider answers only issues of `repo`, whatever `q`
  says.
- `GET /scm/issues/{n}?repo=&comments=1&as=` → the issue, with `comments`
  (oldest first) when asked.

## Events

### Subscriptions

- `POST /scm/subscriptions {repo, branches?, prs?, issues?, kinds?, key?}` →
  **201** `{id, repo, branches, prs, issues, kinds, key, for, expires}`.
  `key` is the consumer's own: a second POST with the same key replaces the
  first (200). `for` is set by the provider: `user:<id>` from a person's
  consumer, `global` from a tile. A subscription lapses at `expires` (30
  days) unless posted again. `kinds` empty means every kind **but** the
  progress kinds (`workflow`, `job`, `check`), which a subscription names
  to get — they are many (a job alone reports queued, in progress and
  completed).
- A person's subscription is checked: the provider must know the person's
  verified login and that they can read the repo — when it is made, and
  again before an event of a private repo is delivered for it (access lost:
  the event is dropped and the subscription deleted). A tile's subscription
  needs the bot to see the repo.
- `GET /scm/subscriptions` → `{items}` (this consumer's, for this caller);
  `DELETE /scm/subscriptions/{id}` → **204**.

### Delivery

The provider POSTs each matching event to the consumer's
`POST /adapter/scm/event` (service `agent-inbox`, through its own `agents`
binding; a partitioned consumer's **global** instance), body an **event
v1**:

```json
{
  "protocol": 1,
  "eventId": "scm:github.com:72d3162e-cc78-11e3-81ab-4c9367dc0958",
  "for": "user:alice",
  "forPid": "p_8d1f…",
  "scm": {"provider": "apps/scm-github", "host": "github.com"},
  "kind": "checks",
  "action": "completed",
  "topic": "scm/github.com/acme/web/branch/xbin/k3x9/3-fix-login/checks.completed",
  "repo": "acme/web",
  "private": true,
  "ref": {"branch": "xbin/k3x9/3-fix-login", "sha": "9fceb02…", "pr": 42},
  "actor": {"login": "github-actions[bot]", "association": "NONE", "bot": true, "self": false},
  "conclusion": "failure",
  "summary": "test failed on xbin/k3x9/3-fix-login",
  "url": "https://github.com/acme/web/pull/42/checks",
  "at": 1789990000000,
  "subs": ["task:3:7:web"],
  "data": {"checks": {"suite": "77", "headSha": "9fceb02…",
           "runs": [{"id": "88001", "name": "test (ubuntu)", "conclusion": "failure", "url": "…"}]}}
}
```

One event reaches a consumer once per `for`, however many of its
subscriptions match; `subs` lists the keys of those that did.

| kind | actions | `ref` | `data` |
|---|---|---|---|
| `pull` | `opened`, `closed`, `merged`, `reopened`, `synchronize`, `ready`, `draft`, `edited` | branch, sha (head), pr | `pull: {number, title, state, draft, head{ref,sha}, base{ref}, url}` |
| `checks` | `completed` (a check suite, or a final commit status) | branch, sha, pr? | `checks: {suite, headSha, runs: [{id, name, conclusion, url}]}` |
| `comment` | `created`, `edited` | pr or issue | `comment: {id, body (≤ 8 KiB), path?, line?, url}` |
| `review` | `submitted`, `dismissed` | pr, sha | `review: {id, state, body (≤ 8 KiB), url}` |
| `push` | `pushed` | branch, sha (after) | `push: {before, after, commits, forced}` |
| `issue` | `opened`, `edited`, `closed`, `reopened`, `labeled` | issue | `issue: {number, title, state, labels, url}` |
| `workflow` | `requested`, `in_progress`, `completed` | branch, sha, pr? | `workflow: <a workflowRuns entry of GET /scm/checks, without jobs>` |
| `job` | `queued`, `waiting`, `in_progress`, `completed` | branch, sha, pr? | `job: {runId, job: <a jobs entry, with its steps as the host last reported them>}` |
| `check` | `created`, `in_progress`, `completed`, `rerequested` | branch?, sha, pr? | `check: <a checks entry>` |

- `workflow`, `job` and `check` are **progress** events: a consumer
  updates what it shows with them and never acts on them alone —
  `checks.completed` (a suite, or a final status) is the one that says CI
  is done. A provider that can't send progress events leaves them out;
  consumers then read `GET /scm/checks`.
- `forPid` (with `for: user:<id>`): the partition id of the person the
  subscription was made in (`X-XBin-Partition-Id`). A person deleted and
  re-created under the same id is another person with another partition
  id ([partitions.md](partitions.md)): a consumer drops an event whose
  `forPid` isn't its partition's, and a provider never delivers an old
  person's subscriptions to a new one.
- `actor.self` marks the provider's own app or bot. `actor.association` is
  `OWNER`, `MEMBER`, `COLLABORATOR`, `CONTRIBUTOR` or `NONE`.
- `topic` grammar (kept for agent-inbox compatibility):
  `scm/<host>/<owner>/<repo>/(pull/<n>|issue/<n>|branch/<ref>|repo)/<kind>.<action>`.
- `eventId` is unique per event (`scm:<host>:<delivery>[:<i>]` when one
  delivery yields several events); a consumer dedupes on it.
- Bodies in `data` are untrusted; `summary` is the provider's own words.
- The consumer answers **200** (taken, or a duplicate), **404** (no such
  subscriber here — dropped and counted); anything else, or no answer, and
  the provider retries with backoff (10 s doubling to 1 h, for 24 h).
- `GET /scm/events?since=&repo=&limit=` → `{items: [event…], next}`: the
  events delivered (or due) to this caller in the last 7 days, for
  catching up.

## Poll

`POST /scm/poll {as?, items: [{id, kind: pull|checks|issue|comments, repo, number?, ref?, etag?, since?}]}`
(at most `limits.pollItems`) →

```json
{"items": [{"id": "t3-pull", "changed": false, "etag": "W/\"…\""},
           {"id": "t3-checks", "changed": true, "etag": "…", "value": {"sha": "…", "state": "failure", …}},
           {"id": "t4-pull", "changed": false, "error": {"refusal": "not-found", "error": "…"}}],
 "retryAfterMs": 0}
```

Each item is the conditional GET of its route (`pull` → `/scm/pulls/{n}`,
`checks` → `/scm/checks?ref=`, `issue` → `/scm/issues/{n}`, `comments` →
`/scm/pulls/{n}/comments?since=`); `value` is that route's answer when
`changed`, and `etag` the route's own. The provider makes at most 4
upstream calls at once. `retryAfterMs` asks the consumer to wait (rate
limits).

## Partitioned consumers

- A partitioned consumer's person's partition reaches the same person's
  partition of a partitioned provider: person tokens, sign-in, the person's
  subscriptions (`for: user:<id>`).
- Its global instance reaches the provider's global: bot only, `for:
  global`.
- Events always go to the consumer's **global** instance, which hands
  `for: user:<id>` on to that person's partition (the agent: partition
  mail).
- People who use the consumer need read access on the provider tile (and,
  when the workspace's `partitionConsent` is on, their consent for the
  edge — [partitions.md](partitions.md) §Calls between partitioned tiles).

## Handing a token to a sandbox (consumer rules)

- Put it in an exec's environment, or in a 0600 file outside any repo,
  rewritten before `refreshAfter`. Never a refresh token (a consumer never
  has one).
- A person's token only in a private sandbox homed in that person's
  partition — never one a non-secure (hosted) conversation used, nor one
  made from a sandbox other people could write to (a clone of a shared
  sandbox).
- A bot token only in a sandbox whose every user may act through the bot
  for those repos (§Identities' last rule).
- A clone or fork gets its own `purpose`; the source's is revoked when the
  source goes.
- Redact token values everywhere output is kept.

## Building a provider

- A GitLab or Gitea provider maps: installation → group or project access
  token, or a bot user; device sign-in → the host's OAuth device grant;
  checks → pipelines and jobs; reviews → merge-request approvals and notes.
  The contract's shapes stay; `scm.kind` says which host family.
- CI maps too: workflow runs → pipelines, jobs → jobs (no steps:
  `steps: []`), job logs → the job trace, which such hosts serve while it
  runs (`complete: false`, and no `in-progress` refusal).
- Provide service `scm` with a `consumer` role (§Wiring), keep people's
  sign-ins in their partitions (§Identities), and list only the caps you
  serve.
- A conformance suite (modelled on `sdk/sandboxcontract`) is future work.

## Versions

`protocol` is 1. Additions (caps, fields, routes) don't change it; a
consumer ignores what it doesn't know and checks `caps`. A change that
breaks a consumer is protocol 2, offered beside 1 (`protocols`).
