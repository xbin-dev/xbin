# LAND — projects-scm onto master

> Status: **landed on master 2026-10-05** as D186 (merge `ba83ab28`).
> Before that: ready to merge — branch `projects-scm` at the commit adding
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

## Decision entry

Landed as **D186** in plans/DECISIONS.md (2026-10-05), with the owner's
rulings at land: questions 1–33 accepted at their built defaults; 34–35
open (not run).

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
