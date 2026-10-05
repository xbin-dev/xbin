# LAND — projects-scm onto master

> Status: ready to merge — branch `projects-scm` at the commit adding
> this line (on `35290d7e`); master (`4a9b5b47`) is its ancestor, so it
> merges with no conflict. The merger runs the agent `-race` suite again
> (Checks, below).

## Landing order

`projects-scm` is built on **`agent/fairness-settings-ui`** (`77d35ba9`,
"agent template: subagent and fairness limits in Settings; a per-person
model-call limit", with its 2026-10-02 changelog entry), which is **not on
master yet**. It must land first, or together with this programme (a merge
of `projects-scm` brings it along). Its base on master is `4a9b5b47`
(release/v0.3.67's merge).

The decision entry below is numbered at land: the next decision number
is already taken by another branch, so this one takes the number free
when it lands.

## What lands

- **Work packages** (each with its record here): S0 (seams, types,
  feature registry, API.md skeleton), G1 (scm-github core: App, sign-in,
  tokens, reads, CI, docs/scm.md), P1 (projects, tasks, the workspace
  pipeline, the gate, the queue), K (credentials: client, gate, files,
  refresh, scrub, redaction, the bot rule, sign-in routes), U1 (Projects
  on the web), P2 (big tasks and forks, "Make this a project…", pull
  requests), E (scm events and polling), C (the coordinator), T (team
  projects), V (CI in the conversation, web and native), G2 (scm-github
  webhooks, subscriptions, outbox, delivery), U2 (the coordinator, feed,
  team and upgrade on the web; all of Projects natively).
- **Gate 1** (records/gate1.md): wave 1 merged; P1's fixtures mint through
  K's fake provider; a written credential pokes the parked run instead of
  queuing a wake; K's code reads the worker's agent (`scmDB`);
  `TestSeededTokenNeverStoredTask`.
- **Gate 2** (records/gate2.md): wave 2 merged; the engine waits for its
  project worker and owner loops at Shutdown; a `none` CI summary falls
  back to the PR's checks; the coordinator's team board through T's
  paged read; the event v1 literal pinned on both sides; V's after-commit
  work counted (`ciGo`).
- **Live checks** (records/LIVE.md): scm-github against real GitHub with
  the owner's test App — S2, S3, S4 answered; fixes for pull writes as
  the bot, draft ↔ ready, the bot's repo permission, Paste without a
  webhook, a grant revoked elsewhere.
- **ps-review** (projects-scm §19, the 2026-10-05 ps-review lines; merges
  `72d2c6a5` scm, `a1d9cd9c` agent, `c8ba3317` ui, and the follow-ups to
  `3f9e9b8e`): fork PRs with a deleted head repo, non-expiring user
  tokens refused, events' `accessLost` and secondary rate limits and the
  `policy` counter, Paste's webhook secret ordering, issue titles framed
  untrusted, closing a task stops its setup and bind, the team seed's
  fork base, the hosted-use note failing closed, big-task prepare locks,
  forks and fork bases tracked to deletion, coordinators' limits in the
  insert's transaction, new sandboxes with `internet` egress, https-only
  provider links, CI reads stopping when unseen, and §16.6's staging lists
  removed (`97f4905f`).
- **Integration** (this branch): the landing gate (below), the
  changelog entry, this record and the decision entry.

The programme changes nothing under `internal/`, `cmd/`, `sdk/`, `web/`
or `native/`: 282 files over master's base, in `builtin-templates/agent`,
`builtin-templates/scm-github`, `docs/` (scm.md new; index, partitions,
agent-inbox, overview 11 and 16, the changelog), `workspace-template/
AGENTS.md`, five node tests under `hack/` and `plans/`.

## The landing gate (projects-scm §16.6)

- **No "Described when it lands."** in builtin-templates/agent/API.md:
  none was left; the Projects introduction's "described here when it
  lands" now says each part has its routes and shapes.
- **Staging lists:** `stagedWeb`, `stagedNative`, `STAGED`, their slot
  comments and spreads were removed by ps-review (`97f4905f`); the
  section comments left in web-features.js / native-features.js are the
  files' own groupings (Projects, CI), not slots. One WP name in a
  shipped string ("U2's board") now names the board.
  `node --test hack/agent-template-features.test.mjs`: 9/9. The test
  needed no change: it never knew the staging lists — a key both
  implemented and listed as a difference is still "stale" and fails.
- **Slipped slices:** V10, the channel attach (`/project <name>` in a
  DM), slipped (records/C.md; §19 C line) — its API.md sentence is
  removed; it never had a feature key or code. S1 (the manifest form
  from a sandboxed frame) was not run live, but nothing documents it as
  verified: scm-github's API.md §13 says GitHub documents only the form
  POST, that all three ways back are built and that Paste stays the
  primary path — the spec's safe default — so the Create flow stays
  documented as built. No feature key belongs to it.
- Also: scm-github's API.md §6 and §13 said S2 was unanswered; they now
  say what the live check found and that the epoch stays.

## Docs (projects-scm §17.1)

Every row is in place: docs/scm.md (G1, G2's §Events); docs/index.md's
link; overview/11-interfaces.md's `scm` beside `sandbox-manager`;
overview/16-extending.md's scm-github; partitions.md §Providers and the
builtin-tiles exception; agent-inbox.md §scm events (E); scm-github's
API.md, AGENTS.md, CLAUDE.md; agent API.md "## Projects"; file headers of
`project_*.go`; workspace-template/AGENTS.md's clause; docs/changelog.md
(this branch). plans/DECISIONS.md is written at land, from the entry
below.

**Compatibility (docs/compat.md):** additive. No xbind route, manifest
key, CLI, SDK, native contract or boot migration changes; the agent
template gains tables an older agent never reads, routes, a slot, a
stream event and fields (rule 2); scm-github is a new template. Rolling
the agent back leaves nothing to clean up (API.md "Rolling back to a
build without Projects"). No migration note under docs/changes/.

## Checks

The final full run, on `5e98b0f9` (2026-10-05), then the fix it called for:

| Check | Result |
|---|---|
| `make check` | pass but for `internal/tilesbx` (known, below) |
| scm-github `-race` (`hack/tile-check.sh scm-github`) | pass |
| agent `-race` (`hack/tile-check.sh agent`) | pass but for `TestTaskSurvivesThreeCompactions` (known, below), `TestKeepWakeUp` and `TestProjectStreamEvent` — both fixed in `35290d7e` (below) |
| `go test ./internal/builtins/...` | pass |
| node (31 agent-template files) | 311 pass |
| `35290d7e`: `TestKeepWakeUp`, `TestProjectStreamEvent` `-race -count=5`; `TestBusyCountsProjectJobs`, the keeper's and worker's tests `-race -count=3` | pass |
| `35290d7e`: pre-commit (js-check, fmt-check, large-files) | ok |

**Not rerun on `35290d7e`:** the full agent `-race` suite (about 50
minutes; stopped by the owner to ship) — the merger runs it.
`35290d7e` makes the wake keeper's `busy()` count the project worker's
live jobs, as the hold does (it said so and didn't), and makes two tests
robust to a loaded host.

**Known failures, not this programme's:** `TestTaskSurvivesThreeCompactions`
under `-race` (fails 3/3 before the programme, at `7d310f72`);
`internal/tilesbx` `TestAdmissionCaps`, `TestResourceReconcile` (fail on
master on this 8 GB host) and `TestStdioNewestWins` (a read timeout under
load) — the programme changes nothing under `internal/`.

**Not run:** S1 (needs the owner's browser: a frame, a new tab, GitHub's
App-creation page); webhooks and events against live GitHub (G2's, tested
against the fake only); a SAML/SSO organization, GHES, a protected default
branch, a spent rate limit, an organization installation; the UI harness
passes for Projects; Apple CI for the native view.

## Open owner questions

Each with the default that was built. Answered ones are under the
decision entry ("owner rulings").

From the work packages:

1. **G1 — Forget with nothing left to revoke with** (refresh token
   refused or expired): built 204, cleared. Alternative: tell the person
   to revoke the App in their GitHub settings.
2. **G1 — `botRepos` on reads:** built, bot reads are held to `botRepos`
   as tokens are. Alternative: only `allowedAccounts`.
3. **P1 — whose authority binds a task to the project's sandbox:** built,
   the project owner's, the owner a participant of every task, and a
   shared project has its sandbox to itself (409 `sandbox-shared`).
   Open: whether K's gate should count a project's people as the
   sandbox's users, and whether projects with exactly the same people may
   share a sandbox.
4. **P1 — what removal from a project takes away:** built, everything but
   reading (the level held to the project's). Open: nobody but the
   project's deletion can delete such a conversation; its creator reads
   what the owner later does there. Alternatives: cap at none, or re-own
   to the project's owner on removal.
5. **K — a hosted conversation reaching a sandbox with a project's
   credential:** built, refused (fail closed). Alternative: scrub and let
   it in (the sandbox then hosted-used for good).
6. **U1/U2 — an explicit built-in pick in `TaskSpec`:** built, none
   (`agent` absent means the policy's engine); the UI names the built-in
   agent only where that is the default. Alternative: `agent: {provider:
   "builtin"}` or `engine: "builtin"`.
7. **P2 — scrubbing before a snapshot:** built, every project's
   credential in the sandbox is scrubbed before a fork base (a re-mint per
   project per snapshot). Alternative: rely on the fork's removal.
8. **P2 — upgrading a conversation that held internal data:** built,
   refused (409 `class-internal`).
9. **P2 — upgrading a shared conversation:** built, refused (unshare,
   upgrade, share the project). Alternative: copy its sharing onto the
   project.
10. **P2 — fork bases for every project:** built, every active project in
    `fork` mode gets one after its first warm-up. Alternative: only once
    its first big task is created.
11. **E — a person's own review comments on their task's PR:** built,
    ignored (the "own identity" rule). Alternative: forward them.
12. **E — events from a provider's person partition:** built, refused.
13. **C — should a project event make a coordinator:** built, only the
    person's request makes one.
14. **C — the level a coordinator acts with:** built, "read" (a
    managers-only task class refuses its tasks). Alternative: the level of
    the request that made it.
15. **C — a removed person's coordinator:** built, acts on nothing and
    isn't woken, stays theirs to read.
16. **T — a seed-cloned membership whose definition stops working as the
    bot:** built, its tasks fail `seed-clone`; delete and join again.
    Alternative: a route moving it to a fresh sandbox.
17. **T — a member edits their membership's security keys:** built,
    nothing refuses it (the provider's `botForPeople` still decides).
    Alternative: a membership's `PATCH` refuses the security keys.
18. **G2 — fork branches:** built, a fork's PR or run names no
    `ref.branch`; matching by PR number only.
19. **G2 — a subscription without an `agents` binding:** built, accepted
    and retried a day. Alternative: 400 `invalid`.
20. **G2 — proving a CI event's branch inside the webhook:** built, fail
    closed within 5 s. Alternative: prove at delivery.
21. **G2 — a person's catch-up when GitHub doesn't answer:** built, 503
    `unavailable`. Alternative: a list without the private repos.
22. **G2 — redaction in passed-through text:** built, token shapes become
    `[redacted]`.
23. **U2 — the definition's security part and hash on a read:** built,
    "Work on this" learns them from the 409 of `POST /memberships` with
    `accept: ""`. Alternative: `defHash` on `GET /projects/{gpid}` or a
    preview route.
24. **U2 — newest-first project events:** built, oldest-first reads of at
    most five pages of 200 and "Read newer". Alternative: `before=` or
    `order=desc` on `GET /projects/{pid}/events`.
25. **LIVE — the bot's draft token holds `contents: write`** (GitHub's
    GraphQL needs it to mark ready / back to draft): built, a separate
    internal token for that mutation alone. Alternative: refuse `draft`
    changes as the bot.

From ps-review:

26. **GitHub App token expiry off:** built, a sign-in without a refresh
    token is refused and its grant revoked. Alternative: accept
    non-expiring tokens.
27. **An issue's title as the task's title:** built, kept on the board
    and pages but out of the brief (the brief names the issue by number;
    the title framed untrusted). Alternative: a neutral "issue
    ‹repo›#‹n›" title.
28. **The backend's egress default for `POST /projects` `{new:
    {provider}}`:** still empty, which a manager may make offline; both
    UIs now send `internet` where offered. Should the backend default to
    internet?
29. **Provider links https-only in both views:** built (CI keeps
    `http(s)`).
30. **`POST /setup/check` re-reads `GET /app` strictly:** built (a failed
    re-read fails the check).
31. **`accessLost` when only delivered items of a lost repo remain:**
    built, not counted.
32. **The team seed's fork base renewed by age alone:** built (a seed has
    no tasks to say it was worked in).
33. **The new `policy` counter in scm-github's `GET /api/events`:**
    undocumented; where should it be documented (scm-github's API.md §10,
    or docs/scm.md)?

Not run, owner's to schedule:

34. **S1 live** — the manifest form from a sandboxed frame's new tab and
    the tile's address at top level; needs the owner's browser. Default:
    all three ways back built, Paste primary.
35. **Webhooks live** — G2's delivery against real GitHub (the test App's
    webhook, an exposure). Default: tested against the fake.

## Decision entry (numbered at land)

- **D‹next› — Projects in the agent template and the scm contract
  (2026-10-05).** builtin-templates/scm-github; docs/scm.md; the agent's
  `_backend/project_*.go`, `scm*.go`, `projects_coord_*.go`,
  `project_team*.go`, `ci_*.go`, `projects_types.go`, `projects_seams.go`,
  `scm_types.go`; its model/project*.js, projects.js, ci-dock.js,
  native/projects.js, native/ci.js and siblings; API.md §Projects;
  plans/projects-scm.md and its records.
  - **The owner's rulings (2026-10-02).** A separate provider with a
    repo-hosting contract (credentials required, the rest optional); tile
    `scm-github`, contract and service `scm`; a builtin template,
    partitioned (`user`, `global`), bot-only unpartitioned; personal
    projects in a person's partition with their own sign-in, only in a
    private sandbox of theirs; team projects: a definition and board at
    global, each task in its creator's partition (coding agents stay
    barred at global, D172); worktrees in one sandbox, big tasks fork it;
    screenshots in PRs deferred (GitHub has no public upload API); CI
    visible and inspectable in the coding UI (D147's chip and dock), down
    to steps and logs; "run as root" left to the sandbox track's decision
    (ACP has no such field); the whole scope. At the freeze (2026-10-03)
    none of the vetoable defaults V1–V18 was vetoed; scm-github's `net`
    is plain `internet` (the narrowed list documented, which must include
    the Actions log hosts); the channel attach (V10) may slip — it did.
    After the live checks (2026-10-04) the owner kept the **epoch**: a
    person's scoped tokens end at their parent's expiry less an hour,
    although S2 showed a scoped token outlives its parent's refresh — the
    cap bounds how long a token lives after a sign-in moves on.
  - **Why a contract and a provider:** the provider knows the host and the
    credentials, the consumer knows who, why and which sandbox; a person's
    sign-in lives in their partition, the App's keys at global; the agent
    never sees an App key or a refresh token. Global holds the client
    secret, so global scopes and revokes, recomputing permissions,
    accounts and repos itself from a relay body the person could have
    written; a partition refreshes itself. The identity directory comes
    from GitHub's answer for a token only this App issued, never `GET
    /user`. A partition's token cache follows global through conf
    `public` (the policy's public half and a `tokenGen`). Not chosen:
    people's tokens at global (one place to steal every sign-in), an
    OAuth web flow (the client secret in every partition), no caching in
    the partition (a relay and a mint per git operation).
  - **Credentials:** short-lived, scoped to the project's repos, written
    0600 outside every repo, kept in memory and the sandbox only
    (`project_creds` holds who, until when and why, never the value),
    rotated by one loop only while a task works, scrubbed on share, stop,
    archive, delete, fork, snapshot and Forget, redacted by shape and by
    value everywhere output is kept (same-length masks); a person's only in
    their own private sandbox homed in their partition and never one a
    hosted conversation used (that use is refused while a credential is
    live). The gate reads the sandbox as its manager reports it at every
    write. Scrub on share blocks; revocation is best effort. A GitHub App
    whose user tokens don't expire is refused at sign-in: nothing could
    rotate or end them. Not chosen: a vault entry per project.
  - **Authority over the bot:** where a home's identity is the bot
    (global, unpartitioned), naming a repo takes a manager or the scm bot
    rule; a project reads only its own repos thereafter; a partitioned
    global holds team definitions only; reruns are a person's own, never
    the bot (V12). A team's seed holds no credential and a sandbox cloned
    from it takes only the bot; a membership runs the definition's setup
    and policy only as its member accepted them, by hash of exactly what
    was shown; a project task's class never has internal reach, checked in
    `classOf` itself. The bot's own writes use internal tokens never
    handed out — including, for draft ↔ ready, one with `contents: write`
    because GitHub's GraphQL requires it.
  - **A project's sandbox and its people:** tasks bind with the project
    owner's authority and the owner takes part in every task; so a shared
    project has its sandbox to itself (409 `sandbox-shared`) — the
    credential gate judges a sandbox by its own users and can't see
    people who reach it through another project. A person removed from a
    project keeps reading their tasks and nothing more. A project's runs
    refuse publish, copy, hosting and moves (V9).
  - **The pipeline:** one worker per engine owner with jobs in the
    database, epoch-fenced, execs found again by `clientId`; the gate
    parks and never consumes, ending its park itself and poking (never
    queuing a wake that could be left over); slots count runs, from
    delivery until taken up; archiving holds rather than fails. Big tasks'
    forks are made inside `prepare` from a fork base taken while the
    sandbox is quiet (agent and manager both asked), with no live token in
    a snapshot or fork, tracked in a registry until deleted. The pr job
    pushes one explicit refspec and never a default branch; auto-PR is off
    by default (V7). An upgrade fails closed (internal data, shared, busy).
  - **Events:** webhooks at global through an outbox (GitHub never
    retries), foreign accounts dropped first, a person's subscriptions
    only through their partition and their private-repo access re-read
    before each event, fork branches never reported as the repo's,
    passed-through text redacted. In the agent one path for events and
    polls: an event schedules the read polling makes, acted on with
    semantic keys; polling in an owner loop, not jobs; the intake takes
    only the provider bound in `scm`, at its global instance.
  - **The coordinator** is made only when its person asks, acts with the
    tile level "read", holds the park on both ends so it never answers
    for a person, and keeps a stable prompt; project events go to one
    coordinator (the task's creator's).
  - **CI:** one dock with sections (the D147 board hosts `ext.dock`);
    summaries kept in memory per (database, root) to avoid a second
    handle inside P1's transactions; pushed branches from git's reflog in
    the pushing run's own sandbox; reads shared and conditional, the 15 s
    re-read only while someone looks (V11); logs only for completed jobs
    on GitHub (S4, checked live).
  - **Not chosen:** a run column for tasks (origin `project` instead);
    project tools as a class toolset (the stored-classes rollback; gated
    on `Config.Project`); a team coordinator at global; a merge route;
    polling as the primary event path; a second right dock for CI; a
    `projects.coord_run` column (the session key); a route moving a
    seed-cloned membership to a fresh sandbox.
  - **Slipped:** the channel attach (`/project <name>` in a DM, V10) — it
    needs the channel rule, the partition's `handoff/dm` consumer and the
    coordinator's replies to the chat; a follow-up.
  - **Defaults the owner may veto:** §2.2–§2.3 of the spec as landed, and
    the open owner questions in records/LAND.md, each with the default
    built.
  - **Owed:** S1 and webhooks against live GitHub; a SAML/SSO
    organization, GHES, a protected branch, a spent rate limit; the UI
    harness passes for Projects; the native view on Apple CI.
  - **Verified:** each WP's named tests (projects-scm §15.2), gate 1's
    `TestSeededTokenNeverStoredTask` (fails without either redaction
    layer), the event v1 literal pinned on both sides, scm-github against
    real GitHub with a test App (`TestLive*`, run by hand; its answers in
    `testdata/github-live.json`, `TestFakeMatchesLiveGitHub`), the agent
    and scm-github `-race` suites, the node tests and `make check` (known
    failures in records/LAND.md).

## Commits

| Commit | Subject |
|---|---|
| `2116f2e1` | agent template: Projects' API.md as landed — the channel attach slipped |
| `654def34` | scm-github: API.md says what the live check found about scoped tokens |
| `f84016cf` | docs: the changelog's entry for Projects and the scm contract |
| `b6bc6b60` | plans: projects-scm — the landing record and the decision entry |
| `60b520d8` | agent template: the policy table in its own module (`model/project-policy.js`) |
| `07e3e2c7` | agent template: a person's conf reader is published atomically |
| `35290d7e` | agent template: the wake keeper's busy check counts project jobs |
