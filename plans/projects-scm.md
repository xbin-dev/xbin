# Projects and the scm contract — the frozen spec

> Status: **live** — the spec of the program on branch `projects-scm` (from
> `agent/fairness-settings-ui`, which adds `maxActiveRunsPerUser`): a
> builtin template `scm-github` that offers the `scm` service, and Projects
> in the agent template. Every work package builds to §4–§16 and records
> under §19 what it built differently; where the code and §19 disagree with
> the earlier sections, they win. The program's decisions get one entry in
> plans/DECISIONS.md, numbered when the integrator lands it ("the decision
> taken at merge", §17.3) — until then nothing cites a number for it.
> Builders cite sections as "projects-scm §x.y".

**Contents.** §1 Context · §2 Owner decisions · §3 Architecture · §4 **The
scm contract, protocol 1** · §5 The scm-github template · §6 Agent: data
model · §7 Agent: routes · §8 Agent: workspace pipeline · §9 Agent:
credentials · §10 Agent: coordinator · §11 Agent: scm events and polling ·
§12 Agent: team projects · §13 UI · §14 Go seams · §15 Tests and fakes ·
§16 Work packages · §17 Docs, changelog and decision drafts · §18 Resolved
· §19 Deviations.

Path shorthand: **B** = `builtin-templates/agent/_backend`, **A** =
`builtin-templates/agent`, **G** = `builtin-templates/scm-github`.

## 1. Context

Coding agents in xbin sandboxes can't push to GitHub: a sandbox holds no
credentials and, by design, has no route back to xbin
(docs/sandbox-manager.md §Inside a sandbox). Each conversation also stands
alone — nothing groups work around a sandbox and its repos. So two things
aren't possible today: firing off many small tasks with a fast iteration
loop, and a coordinator managing implementers on the owner's behalf (from a
phone, say) while the owner can still talk to any implementer directly.

What the design rests on, checked in the code of this branch (line numbers
as of S0's commits — `B/db.go` and `B/channels.go` count S0's own lines;
builders re-check before editing):

**Runs, origins, homes**

- Runs with an origin outside `''`, `chat`, `api` are not chats: server
  `chatOrigins` (B/conversations.go:20) and the client's
  `model/conv-list.js` leave them out of the list; `isChat`
  (B/homes_move.go:120) keeps them from moving home. The index
  `idx_runs_origin(origin, origin_id, id)` exists (B/migrate_conv.go:43). So
  a task is a run with `origin='project'`, `origin_id=<pid>` — no new run
  column.
- `/needs` lists roots waiting for a person whatever their origin, and
  failed runs of non-chat origins (`handleNeeds`, B/conversations.go:471);
  `automationOrigin` (B/needs_push.go:253) counts `project` as an
  automation — its failures are news.
- The run JSON already has a `task` key: the pinned task (D133;
  `DB.taskView`, B/asks.go:344, used in B/events.go:327 and
  B/stream.go:133). Projects use **`projectTask`** (§7.4).
- `deleteOneRun` (B/db.go:442) deletes a run's rows in one transaction;
  `handleDeleteRun` (B/handlers.go:243) and `deleteConversation`
  (B/homes.go:467) call it.
- `Config` is additive JSON (B/llm.go:46); `handlePutConfig`
  (B/handlers.go:484) strips a conversation's own fields from the global
  defaults; `childConfig` (B/links.go:591) copies the parent's config to a
  subagent (a pointer field is shared).
- `startRunTx` (B/handlers.go:102) creates a run inside the caller's
  transaction; `runOpts` (:73) carries the stamp (owner, visibility, team
  role, origin, origin id, session key, title source, engine).

**Partitions**

- In a person's partition, conversations number from 2^40
  (`partitionIDBase`, B/partition_start.go:25; `seedPartitionIDs` called
  from `openDB`, B/db.go:185); triggers mirror it (`seedTriggerIDs`,
  B/trigger_registry.go:345). `model/homes.js` `homeOf(id)` routes by it.
- A person's partition refuses sharing (`userNoShare`,
  B/partition_routes.go:66; `sharesInPartition`): its conversations are
  private. Shared things live at the global instance.
- Coding agents are barred at the global instance and in hosted
  conversations (`harnessBarred`, B/harness_partition.go:72;
  `harnessClass`, B/harness_pass.go:592).
- At global, a person's partition calling in looks like that person
  (`personFromPartition`, B/partition_routes.go:53), never like the tile
  itself (docs/partitions.md §The global instance and people's partitions)
  — and so does that person's **frame or terminal** in the partition: a
  body arriving that way is the person's own input, whatever code in the
  partition was meant to send it (true of scm-github's relay too, §5.6).
  A partition reaches its own global with `callGlobal` (B/gwcall.go:135).
- The global instance hands a person's events to their partition by
  partition mail (`handEvent` → `queueHandoff("event", …)`,
  B/trigger_registry.go:259 and :275; topics in B/handoff.go:100; handlers
  register from `init()`, B/handoff_user.go:39).
- Calls between partitioned tiles: `user:alice`'s partition of the agent
  reaches `user:alice`'s partition of a partitioned provider (with her read
  access, and her consent when `partitionConsent` is on); any other caller —
  the agent's global instance, an unpartitioned agent — reaches the
  provider's global instance (docs/partitions.md §Who reaches which
  partition, §Calls between partitioned tiles). A provider keys per-caller
  state on (`X-XBin-From`, `X-XBin-Deployment`, `X-XBin-Partition-Id`)
  (docs/partitions.md §Providers).
- `hostedRoute` (B/hosted_serve.go:80) intercepts, at global, every route
  whose pattern contains `{id}` (:90): project routes use `{pid}`.
- A person's partition drives the hosted conversations it hosts with the
  same engine code over the shared `team` database (`newEngine(tr, …)`,
  B/hosted_engine.go:81; `hostedID`, B/team_runs.go:32), and `team`'s schema
  is `migrate()`'s (`migrateTeamRuns`, B/team_runs.go:59), which
  `teamCovers` (:118) compares column by column: so every project hook
  skips hosted runs, and the project tables are never made in `team` (§6,
  §14.1).

**The engine**

- `Engine.pass` (B/actor.go:106) reads the run, then
  `rows := e.db.undelivered(run.ID)` (:124), then forks to `harnessPass`
  for a coding agent (:125) — the workspace gate goes between the two.
- A reply to a parked built-in run denies its parked calls (B/actor.go:161).
- The child-side settle (`settleOwnLink`) runs only for `ParentID != 0`, at
  five sites: `stopRun` (B/actor.go:342), `endTurnTx` (B/actor.go:789), a
  subagent's `ask_user` turned into a blocker (B/actor_tools.go:327),
  `endHarnessTurnTx` (B/harness_engine.go:1322) and a coding agent's cancel
  (B/harness_pass.go:569, which bypasses `stopRun`). A run's move to
  `waiting_input` happens outside every one of them — an approval park
  (B/actor_tools.go:176), `ask_user` (:341), a coding agent's park
  (B/harness_park.go:146) and its sign-in (B/harness_login.go:164) — all
  through `DB.setStatus` (B/db.go:384; `setStatusOnly` :392). Top-level runs wake on
  background notices (`hasNotices`, B/links.go:112; used at
  B/actor.go:186 and :205). `deliverBoundary` (B/actor.go:828) delivers
  them via `deliverNotices` (:893).
- `endTurnTx` cancels below a run (`cancelBelow`, B/actor.go:795) by
  `parent_id`: a task made as a top-level run is never cancelled by its
  coordinator's turn ending.
- The model-call gate: `acquireLLM(ctx, run.Depth == 0)` (B/actor.go:631,
  B/compact.go:372; B/llmslots.go:133) — a top-level call may take a
  person's last slot, a subagent's may not.
- `hasWork` (B/engine.go:484) and `updateHoldLocked` (B/owner.go:96) decide
  whether the engine holds the process; takeover runs `sweepSigninExecs`
  (B/engine.go:172); `BeginShutdown` (B/engine.go:432) stops everything.
  `userWake` (B/resume_mode.go:70) is what a person's partition leaves
  behind when it stops; `leaveWakeUp` (:37) registers it.

**Sandboxes and credentials**

- The sandbox client: `sbxCreate` with `From:{Sandbox, Snapshot}`
  (B/sandbox_client.go:386), `Run`, `ExecStart` with `ClientID`,
  `WriteFile` with `Mode` and `Mkdirs` (B/sandbox_files.go:112). No
  snapshot methods yet. A snapshot stops the sandbox and kills its execs
  (docs/sandbox-manager.md, the snapshot routes); the contract's
  `clientId` is idempotent per consumer (and per sandbox for execs and
  snapshots).
- The fake manager's sandboxes are host directories and its execs run
  real `sh` on the host (B/fsb_fake_test.go): paths come from the sandbox's
  `workdir` and `home`, never `/work`.
- `credWhy` (B/harness_creds.go:356) is the gate for writing a person's
  coding-agent sign-in into a sandbox: own run, private sandbox
  (`sandboxPrivate`, B/harness_engine.go:774) owned by the person, never one
  a hosted conversation used. `homedHere` (B/sandbox_partition.go:56) says
  a sandbox is homed in this partition. `readyForShare`
  (B/harness_creds.go:1024) clears sign-ins before a sandbox is shared, and
  `handlePatchSandbox` (B/sandbox_routes.go:180) calls it.
- `jobExecEnv` (B/sandbox_jobs.go:64) is a bash job's environment; the
  coding agent's env is built from `credEnv` (B/harness_engine.go:545).
- Redaction: `redactor.apply` (B/harness_redact.go:58) masks exact secrets
  and the Anthropic token shape; the harness stdout reader and the harness
  log apply it (B/harness_engine.go:1040, :1084; B/harness_log.go:150).
  Every tool result is written by `addMessage` (B/db.go:529) and rewritten
  by `rewriteMessage` (B/db.go:577).
- New sandboxes are labelled `xbin.agent/home` in a partitioned agent
  (`withHomeLabel`, B/sandbox_partition.go:92); agent-made sandboxes use
  clientId `agent:<root>:name:<n>` (B/sandbox_create.go:63).

**Routes, access, tools, UI**

- `routes()` (B/routes.go:139) appends route tables in one chain; `guard`
  (:167) resolves the caller and, for a route on one run, its access
  (`needLevel`, :217). `rootACL.level` (B/acl.go:53) is the access shape.
- `/adapter/*` routes take the `channel` role (`adapterGuard`,
  B/channels.go:217); **any** bound adapter holds it — a route there that
  only scm providers may call must check the caller is one (§11.1).
- `handleMessage` (B/inbox.go:224) takes a person's message to a run;
  `noteParentTx` (B/harness_spawn.go:348) is the precedent for noting a
  direct message to a child.
- Tools: `toolSpecs` (B/tools.go:89) gates the thread tools on depth 0
  (:165); `runTool` dispatches them (:493); first sentences are pinned in
  B/tooldesc_test.go:29. Adding a class toolset needs the stored-classes
  rollback trick (B/classes.go:276): project tools are gated on
  `Config.Project` instead.
- `channelDeliver` (B/channels.go:479) resets a session whose class or
  owner changed — a chat can't be pointed at an existing conversation
  today.
- `agent.js` is at 885 of its 949-line budget (hack/size-budget.txt:28);
  the web view's seams are `web-ext.js` (`block`, `end`, `top`, `paint`,
  `newChat`, `task`; S0 adds `side`, `page`, `crumb`, `dock`, `card`,
  `childStatus`, `sbx`, §13.2); the native view's are `native/ext.js`
  (`screen`, `toolbar`, …; S0 adds `drawer`, `dock`, `card`,
  `childStatus`, §13.3). Feature keys live in `model/features.js`;
  `DIFFERENCES` lists a view's intended gaps.
- Both views keep their state in `model/`; `model/app.js` wires the stores
  (`app.board = createBoard(app)`, :422) and routes stream events to them
  (`app.board.take(ev)` in `app.event`, :358).

**The coding-agent UI (D147) that CI joins**

- `harness-board.js` (149 lines) is the web's Coding agents board: a top
  chip (`ext.top`, "⌨ 3 coding agents · 1 needs you") that toggles a right
  dock `aside#hboard` — the `.wrap` grid's third column from 1100 px
  (`.wrap.dockon`), fixed over the chat below that — whose rows are child
  cards (`harness-child.js` `cardTpl`). The dock is created by `paintDock`
  on `ext.paint`. There is one dock column; a second `aside` beside it would
  fight for it.
- `harness-web.js` lists the web's feature modules, one import line each in
  its own slot; `native/harness-all.js` is the native view's list.
- `harness-child.js` (237 lines) draws a child as a card in its parent's
  chat (`ext.block`): identity, status line, counters, inline park, the last
  blocks, Open/Stop/Cancel/Message — the card style inline CI cards follow.

**Commands as root**

- ACP (protocol version 1, the one `sdk/acp` pins, sdk/acp/types.go:8) has
  no privilege field anywhere: upstream's `CreateTerminalRequest` is
  `sessionId`, `command`, `args`, `env`, `cwd`, `outputByteLimit` and
  `_meta` (extension metadata for client and agent to agree on, not a
  privilege channel), and its session requests carry none either. More to
  the point, AgTT advertises no client terminals (`harnessCaps`,
  B/harness_engine.go:180: "no files or terminals of the client's"), so
  `terminal/create` never reaches xbin: adapters run their commands
  themselves, as the sandbox's user (docs/sandbox-manager.md §Inside a
  sandbox). So nothing in the sandbox contract can be "run as root" for a
  coding agent (§18); sudo in VM sandboxes is the separate sandbox track,
  whose decision records the ruling.

**GitHub** (from GitHub's documentation; four points are spikes, §5.11)

- A user access token from the **device flow** refreshes without the client
  secret; scoping (`POST /applications/{client_id}/token/scoped`) and
  revoking (`DELETE /applications/{client_id}/token`, `/grant`) need basic
  auth with the client secret. A refresh invalidates the old access and
  refresh tokens.
- Installation tokens last 1 h, can't be refreshed, and can be narrowed to
  repos and permissions when minted. Since 2026-04 they are stateless
  `ghs_` tokens of about 520 characters.
- GitHub never redelivers a webhook by itself.
- GitHub has no public API to upload an image into a pull request
  (screenshots in PRs are deferred, §2).

## 2. Owner decisions

### 2.1 Asked and answered (2026-10-02)

| Topic | Decision |
|---|---|
| Interface | A separate component with a repo-hosting contract: credentials are required, the other capabilities optional. |
| Naming | **Tile `scm-github`** — named for what it does; **contract and service `scm`**, so one agent slot `scm` binds any provider (a later `scm-gitlab`). |
| Packaging | **Builtin template** (`builtin-templates/scm-github`, `template.partition: ["user","global"]`). No change to xbind core. Without `--isolate` an instance is unpartitioned and offers bot tokens only. |
| Personal projects | Live in a person's partition, use their own GitHub sign-in, and only ever in a private sandbox of their own. |
| Team projects | A shared definition and task board at the agent's global instance. **Each task runs in its creator's own partition**: their own workspace sandbox, with coding agents allowed. Members see the board; only the creator opens that task's transcript. Coding agents stay barred at global (D172's rule; the plan's "D179" meant it). |
| Where tasks run | Worktrees in one workspace sandbox. Big tasks fork it. |
| Screenshots in PRs | **Deferred.** GitHub has no public upload API; the browser's drag-and-drop uses an internal endpoint. Recorded in the decision; revisit if GitHub adds an API. |
| Scope | Everything: scm-github, projects, coordinator, forks, events, team projects. |
| CI in coding mode | CI for a project task, or for a coding session's pushed branches, is **visible and inspectable in the tile**: live progress down to job steps, logs where the platform serves them, annotations, and links to runs, jobs and PRs on the platform. It must fit the existing coding-agent UI (D147), not sit beside it (§13.5). |
| Run as root | Added to the sandbox contract only if ACP has it. It doesn't (§1, §18): sudo in VM sandboxes is a separate track, and its decision records the ruling. |
| Execution | A workflow. **Pause for the owner after this contract freeze, and again before landing.** |
| Quality bar | Upstream-acceptable: AGENTS.md hard rules, docs as contract, docs/compat.md, records and decisions, tests, review. |

### 2.2 Defaults the plan adopts — the owner may veto any at the freeze

**Signed off at the freeze (2026-10-03): none of V1–V18 vetoed.** scm-github's
`net` binding defaults to `internet` (§5.2's narrowed list documented as
the alternative); the channel attach may slip (V10).

| # | Default | Where it bites |
|---|---|---|
| V1 | A team task's run owner is its creator. | §12 |
| V2 | gh config is per project (`GH_CONFIG_DIR`), so a person's own `gh` login in the sandbox is untouched. | §9.4 |
| V3 | A failed setup doesn't block the task; the output tail goes to the agent. | §8.3 `setup` |
| V4 | Branches are `xbin/<project-uid>/<n>-<slug>`; the prefix is policy `branchPrefix`. | §8.2 |
| V5 | A run waiting for a person holds its task slot. | §8.7 |
| V6 | Project setup warns when the base branch has no protection (a pushed token could merge through the API); policy `protection: "refuse"` makes it a refusal. | §8.3 `repo` |

### 2.3 Decided while writing this spec (also vetoable)

| # | Choice | Why |
|---|---|---|
| V7 | `autoPR` defaults to `off` (`draft` and `ready` are opt-in). | Opening a PR is visible to people outside xbin; the person asks once ("Open PR") or turns it on. |
| V8 | The coordinator is created lazily, one per person per project, in the person's own home (a membership's partition for a team project). A team coordinator at global is deferred. | Plan §5; coding agents and person tokens never at global. |
| V9 | Project runs refuse publish, copy into another home, hosting and moves (409); `GET /runs/{id}/export` (a download) stays. | A task's worktree, branch and credentials belong to its home. |
| V10 | The channel attach (`/project <name>` in a DM) ships last and may slip to a follow-up without blocking the landing. | Plan §5 "last slice". |
| V11 | CI is refreshed every ~15 s only while someone has the CI dock open on a conversation with work in progress; with the dock closed, a watch with anything pending is re-read in the background at 60 s for 20 min after a push, then at §11.5's cadence, and only while webhooks for it aren't arriving; a conversation's opening reads once. | One person reading a log doesn't cost the installation's rate limit for everyone else, yet the chip doesn't sit at "pending" when nobody looks. |
| V12 | **Re-run** is a person's click only (participant of the conversation), **as that person** — only in their own partition of a partitioned provider; never the bot (403 `identity` elsewhere); no tool offers it to a model. | A rerun spends CI minutes and can deploy; the bot would let anyone re-run anything (plan §8: "a person's identity only"). |
| V13 | A coding session's pushed branches are found at the end of each run's turn — the root's or a coding agent child's — from git's own record of pushes (`update by push` in a remote-tracking ref's reflog), only when an scm provider is bound and the run has a sandbox. | One `Run` per turn; no guessing from branch names. |
| V14 | Team definitions subscribe to nothing in v1; members' tasks subscribe personally (`for: user:<id>`). | Nothing at global acts on events yet (no team coordinator). |
| V15 | Where a home's identity is the bot (the global instance; an unpartitioned agent), naming a repo — a project, a repo added, a conversation made a project, a CI watch — needs the agent's manager, or the **scm bot rule** a manager sets (people and repo globs, §9.1). A partitioned agent's global instance holds team definitions only. | The binding grants the agent the bot's whole view; the agent must not hand it to everyone who may start a run (a confused deputy). |
| V16 | A membership's sandbox is cloned from the team's seed only when the membership works as the bot (its `policy.as` is `bot`, which `membersAsBot` permits); one that works as the person always starts fresh (its own `repo` jobs). | The seed is writable by every team member; anything planted there (a hook, a PATH shim, a credential helper) would run beside the person's token. |
| V17 | A membership runs the definition's security-relevant part (setup scripts, instructions, checks, task class, identity, reviews, auto-PR…) only as its member last accepted it; a change waits on "Review the team project's changes". | The definition's owner is any person at global: adopting their setup script silently would run their code beside the member's token. |
| V18 | Project tasks never run in a class with internal reach (409 `class-internal`), and the coordinator reaches only such tasks. | Every task reads scm text (CI logs, reviews, issues); the coordinator, in the web lane, steers them. |

## 3. Architecture

### 3.1 Components

```
 GitHub ──webhook──▶ scm-github (global) ──POST /adapter/scm/event──▶ agent (global)
   ▲                  │  App keys, installations, bot tokens,            │ for: user:<id>
   │                  │  identity directory, subscriptions, outbox       ▼ partition mail handoff/scm
   │                  ▲ /partition/* (relay, as the person)          agent (alice's partition)
   │                  │                                                 │ projects, tasks, coordinator,
   └── REST ── scm-github (alice's partition) ◀── /scm/* ───────────────┘ worker, credentials gate
                        alice's device-flow sign-in                     │
                                                                        ▼ /sbx/* (sandbox-manager)
                                                              alice's workspace sandbox:
                                                              .repos/*.git, tasks/<n>-<slug>/<repo>
```

- **scm-github** (§5) — a builtin template providing service `scm` (§4).
  Its global instance holds the GitHub App; each person's partition holds
  that person's sign-in.
- **The agent** — a new multi slot `scm` (service `scm`); Projects (§6–§12)
  live in the agent's homes; the worker drives sandboxes through the
  existing `sandboxes` slot.
- **Sandboxes** — the existing sandbox-manager contract. Credentials enter
  a sandbox only through the files API (§9), never through a route back to
  xbin.
- **CI in the coding UI** — the agent watches CI for a task's branch and PR,
  and for the branches a coding session pushed (§6.9); the web shows it as a
  chip and a section of the D147 right dock, inline cards and board chips,
  the native view as screens (§13.5).

### 3.2 Homes

| What | Home (agent) | Runs | scm identity | scm reached | Events |
|---|---|---|---|---|---|
| A personal project (partitioned agent) | the person's partition (ids ≥ 2^40), private | its tasks (built-in or coding agent), its coordinator, its worker | `person` (default), or `bot` if `policy.as` asks and the provider's `botForPeople` allows | the person's partition of scm-github | scm-github global → agent global (`for: user:<id>`) → mail `handoff/scm` → the partition |
| A team project's definition — the only kind a partitioned agent's global instance holds (409 for another) | the agent's global instance (ids < 2^40), team-visible | no tasks; its worker keeps the optional seed sandbox | `bot` only, for reads; naming its repos takes the bot rule (§9.1) | scm-github's global | `for: global` (issues, the definition's repos) |
| A team project's membership | each member's partition (`kind: membership`, `team_ref` = the global id) | that member's tasks and their coordinator | as a personal project | the member's partition of scm-github | as a personal project; board rows go to global (§12) |
| A project in an unpartitioned agent | the one instance; members and team visibility as conversations have them | tasks and one coordinator per person | `bot` only (no partition can hold a person's sign-in); naming its repos takes the bot rule (§9.1) | scm-github's global (or an unpartitioned scm-github) | `for: global` |

A partitioned agent bound to an unpartitioned scm-github: every home
reaches the one instance; a person's partition gets `bot` only, and only
under `botForPeople` (§4.4).

### 3.3 Where each part runs

| Part | Process | Identity used |
|---|---|---|
| Project store, routes, worker, gate, queue | the project's home (engine owner only for the worker) | the caller's (`who`); the worker acts as the project (its jobs carry `by`) |
| scm client | the home | the agent tile; xbind adds the partition headers, so a person's partition is that person at the provider |
| Credential writes | the worker, behind `scmCredWhy` (§9.2) | the sandbox manager sees the agent's partition |
| Coordinator | its home's engine, web lane | `Config.Project` role `coordinator` |
| Webhook intake, subscriptions, outbox | scm-github global | the App |
| Sign-in, person tokens | scm-github in the person's partition; scoping/revoking relayed through its global | the person |
| CI watches, the CI view, logs, reruns (§6.9, §7.6) | the conversation's home | reads as the home's default identity (a person's partition: that person; elsewhere the bot, for repos the bot rule lets that conversation name); a rerun only as the clicking person in their own partition. No token enters a sandbox for CI. |
| Team board rows (§12) | written by each member's partition, kept at the agent's global | the member (`personFromPartition`) |

## 4. The scm contract, protocol 1

This section becomes `docs/scm.md` (G1). It is written as the contract: a
provider implements it; a consumer (the agent, any tile) programs against
it.

### 4.1 The split

The **provider** knows the host and holds the credentials: app keys, the
people's sign-ins, installation lookups. The **consumer** knows who is
asking, why, and which sandbox a token goes to. The provider never sees a
sandbox; the consumer never sees an app key or a refresh token.

### 4.2 Wiring

- A provider declares `"provides": {"scm": {"kind": "http", "service":
  "scm", "role": "consumer"}}` and serves the routes below under
  `/scm/*`. Its `expose.roles` has `consumer` (the routes) and `admin` (its
  own page).
- A consumer declares an http slot with `"service": "scm"` (the agent:
  `"scm": {"kind": "http", "service": "scm", "multi": true}`) and the owner
  binds it: `bx bind apps/agent scm+=apps/scm-github`. The binding is the
  grant. It reads its endpoints from `XBIN_IFACE_SCM`.
- Events need the reverse binding too: the provider's `agents` slot
  (service `agent-inbox`) bound to the consumer
  (`bx bind apps/scm-github agents+=apps/agent`), §4.10.

### 4.3 Who is asking

The provider decides from the verified headers, never from the body:

| Caller (headers) | Class | Consumer key |
|---|---|---|
| `X-XBin-From` another tile, no `X-XBin-Partition` (an unpartitioned consumer) or `global` | **tile** | (From, Deployment, "") |
| `X-XBin-From` another tile, `X-XBin-Partition: user:<id>` — reaching this provider's own partition of that person | **person's consumer** | (From, Deployment, Partition-Id) |
| the provider's own partition calling its own global (`From` = self, `Partition: user:<id>`, role reader/writer) — its backend, **or the person's frame or terminal there**, which arrive identically | **relay** (§5.6) | — |
| a person through the provider's own page (frame, `User` set) | **person** (page) | — |
| the owner token, or the tile itself at global (`From` = self, no partition or `global`) | **self/owner** | — |

A global instance never treats a call carrying `X-XBin-Partition: user:…`
as itself, even when `X-XBin-From` is its own path: such a call whose role
isn't reader or writer is 403, never self. A relay call's **body is the
person's own input** — their frame or terminal in the provider's partition
reaches global with exactly the headers its backend has (docs/partitions.md
§The global instance and people's partitions) — so global re-checks every
field of it as it would a stranger's (§5.6). View-as (`ViewedBy`) is never
a person here. A consumer can't assert a person: the only person a call
can be is the partition it arrives in.

### 4.4 Identities

`as` is `person` or `bot`. It defaults to `person` where a person is
asking (a person's consumer, a person's page), `bot` elsewhere.

| The call reaches | `as: person` | `as: bot` |
|---|---|---|
| a person's partition of a partitioned provider (a person's consumer, or their page) | that person — **409 `signin`** if not signed in | only under the provider's policy `botForPeople`: `off` (default) 403 `identity`; `own-access`: allowed for repos the person can push to (read: pull), checked with their own token; `on`: allowed |
| a global instance, from a tile | **403 `identity`** | yes: the binding is the grant; policy `botRepos` patterns apply |
| an unpartitioned provider, from a tile | 403 `identity` | yes, as above |
| an unpartitioned provider, from a person's consumer | 403 `identity` (no partition holds the person's sign-in) | `botForPeople`, with `own-access` treated as `off` (no person token to check with) |

Rules every provider keeps:

- A provider that keeps people's sign-ins keeps each one **in that
  person's partition** and nowhere else. An unpartitioned provider is
  bot-only.
- A person's identity is only ever the partition's own person; naming
  another login is 403 `identity`.
- Asserted people (`Sbx-User`-style headers, body fields) and view-as are
  never accepted.
- Write calls (`POST /scm/pulls`, `PATCH`, comments) made as `bot` use the
  bot; made as `person`, the person's token.
- **A consumer that acts for people with the bot authorises each person
  itself.** The binding grants the consumer tile the bot's view; it says
  nothing about which of the consumer's own users may use it. A consumer
  whose instance has no person identity (a tile at global, an unpartitioned
  consumer) and lets people name repos, read them or get tokens for them
  must decide per person what they may name (the agent: §9.1's bot rule) —
  otherwise everyone who can use the consumer gets the bot's view.

### 4.5 Conventions

- JSON bodies; times are Unix **milliseconds**; a `repo` is `owner/name`;
  a `host` is a hostname (`github.com`).
- **No custom request headers**: parameters go in the query (GET, DELETE)
  or the body (POST, PATCH).
- **Pagination:** `limit` (default 30, max `limits.pageMax`, 100) and an
  opaque `cursor`; a list answers `{items, next}` — `next` absent on the
  last page.
- **Conditional reads:** every GET answers an `ETag` header and the same
  value in the body's `etag`; a request with `ifNoneMatch=<etag>` (query)
  or `If-None-Match` gets **304** with no body. A provider keeps the host's
  own ETags where it can (GitHub doesn't count a 304 against the rate
  limit).
- **Bodies the host's users wrote** — issue and PR text, comments, review
  bodies, check logs, commit messages — are untrusted text. A provider
  passes them through (clipped where stated), never interprets them.
- Unknown request fields are ignored; unknown answer fields must be
  ignored by consumers.

### 4.6 Errors

`{error, refusal, retryAfterMs?, …}` with the status below. `error` is for
people; `refusal` for programs. The sandbox-manager contract's refusals
(docs/sandbox-manager.md §Conventions) keep their meaning; the scm
contract adds six. A consumer treats an unknown refusal by its status.

| refusal | status | meaning | payload |
|---|---|---|---|
| `protocol` | 400 | unknown protocol version | `protocols` |
| `invalid` | 400 | a bad field: repos spanning owners, more than `limits.reposPerToken`, a permission wider than the preset, an unknown `access` | — |
| `not-found` | 404 | no such repo, pull, issue, check, subscription — or not visible to this identity | — |
| `not-allowed` | 403 | the identity may not do this; a SAML/SSO block | `sso: {url}` for SSO |
| `exists` | 409 | a `clientId` reused for a different request | — |
| `precondition` | 412 | a conditional write lost | — |
| `limit` | 429 | the host's rate limit, or the provider's | `retryAfterMs` |
| `unsupported` | 501 | a capability this provider doesn't offer | — |
| `unavailable` | 503 | the host or the provider is down or starting | `retryAfterMs` |
| **`signin`** | 409 | `as: person` and the person isn't signed in — a sign-in was started | `signin: {url, userCode, expiresAt, pollId, intervalMs}` |
| **`not-installed`** | 409 | the app isn't installed on the repo's account | `install: {url, owner}` |
| **`identity`** | 403 | not as that identity here | `identities: [...]` (what this caller may use) |
| **`setup`** | 503 | the provider isn't set up (no app yet; device flow disabled) | — |
| **`upstream`** | 502 | the host answered an error the provider can't map | `upstream: {status, message}` |
| **`in-progress`** | 409 | a job's log asked for while it runs, from a host with no partial-log API | `url` (the host's live log page) |

### 4.7 `GET /scm/hello?protocol=1`

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
  "limits": {"reposPerToken": 100, "minTtlSec": 900, "pageMax": 100, "pollItems": 50},
  "notes": []
}
```

- An unknown `protocol` → 400 `protocol` with `protocols`.
- `caps`: only `credentials` is required; a route of a capability not
  listed answers 501 `unsupported`. `partitions` is listed only by a
  partitioned instance (it keeps sign-ins per person). A dotted cap
  (`checks.rerun`) is an optional part of its capability.
- `identities` is what this instance hands out at all; `you.identities`
  what this caller may ask for now (§4.4); `you.default` the `as` it gets
  by default; `you.person` is `null` when no person is signed in here (or
  none can be).
- `notes` are sentences for people (why an identity is missing, setup left
  to do). The protocol grows additively: new caps, new fields.

### 4.8 Credentials

#### `POST /scm/token`

```json
{"repos": ["acme/web", "acme/api"], "access": "write", "as": "person",
 "permissions": {"workflows": "write"}, "minTtlSec": 1800, "purpose": "proj:k3x9:coding-sandbox|s-12"}
```

`repo` (one) or `repos` (≤ `limits.reposPerToken`, one owner); `access`
`read` or `write`; `permissions` may only narrow the preset (or add
`workflows: write` where the provider's policy allows it); `minTtlSec`
(default and minimum `limits.minTtlSec`); `purpose` is the consumer's
own key — tokens are cached and revoked by it.

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
  issues, checks, statuses, actions, metadata: read (a task's git and `gh`
  need no more; a consumer opens pull requests and comments through the
  provider, whose own write token holds what those need, §5.7). Never
  `administration`; `workflows: write` only on request and only under the
  provider's `allowWorkflows`.
- A repeat with the same (consumer, purpose, repos, permissions) returns
  the cached token while it has at least `max(minTtlSec, 15 min)` to run.
- `refreshAfter` is when the consumer should ask again (≥ 10 min before
  `expiresAt`). A consumer never needs, and never gets, a refresh token.
- Refusals: 409 `signin` (and a sign-in is started), 409
  `not-installed`, 403 `identity`, 400 `invalid`, 404 `not-found` (a repo
  the identity can't see), 503 `setup`.

#### `POST /scm/token/revoke`

`{token}` (one this consumer was given; matched by hash) or `{purpose}`
(every live token of this consumer for it). **204**; 404 `not-found` when
nothing matches. Revocation is best effort upstream; the provider forgets
the token either way.

#### Sign-in: `POST|GET|DELETE /scm/signin`, `GET /scm/signin/{pollId}`

Only where a person is asking (a person's consumer or their page);
elsewhere 403 `identity`.

- `POST /scm/signin` → `{"state": "done", "identity": {…}}` when signed in,
  else starts (or continues) a device flow →
  `{"state": "pending", "signin": {"url": "https://github.com/login/device", "userCode": "ABCD-1234", "expiresAt": …, "pollId": "p_…", "intervalMs": 5000}}`.
  503 `setup` when the host's device flow is off.
- `GET /scm/signin` → the current state without starting one:
  `{state: "none"|"pending"|"done", identity?, signin?}`.
- `GET /scm/signin/{pollId}` → `{state: "pending"|"done"|"denied"|"expired"|"error", identity?, error?, retryAfterMs}`;
  asks the host at most once per interval. 404 for an unknown or
  finished-long-ago poll id.
- `DELETE /scm/signin` → **204**: "Forget" — revokes the grant upstream
  and clears the sign-in. Every person token handed out stops working.

The device code (`userCode`) is shown only to the person it is for; a
consumer never shows it to anyone else (§9.8).

### 4.9 Reads

`as` is accepted on every read (query); the identity's view of the host
decides what is visible.

#### Repos

- `GET /scm/repos?q=&as=&limit=&cursor=` →
  `{items: [{host, owner, name, cloneUrl, defaultBranch, private, permission, archived, url}], next}`
  — the repos this identity can reach through the app (a person's: those
  of the app's installations they can see; the bot's: the installations'),
  `q` matching `owner/name`. Cached by the provider ≤ 5 min.
- `GET /scm/repo?repo=&as=` → one, plus `protected` (`true`/`false`; absent
  when the identity can't tell): whether the default branch has
  protection the identity can't bypass.

#### Pulls

- `POST /scm/pulls {repo, head, base?, title, body?, draft?, clientId?, as?}` →
  **201** the pull; **200** with `existing: true` when one is already open
  for that head. `base` defaults to the default branch.
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
  `mergeableState: "unknown"` and `retryAfterMs` while the host computes it.
- `PATCH /scm/pulls/{n} {repo, title?, body?, state?: open|closed, draft?, as?}`
  → the pull. (`draft` maps to the host's ready-for-review / to-draft
  operations.)
- `GET /scm/pulls/{n}/comments?repo=&since=&as=&limit=&cursor=` → one
  timeline, oldest first:
  `{items: [{id, kind: comment|review|review-comment, author, body, state?, path?, line?, url, createdAt}], next}`.
- `POST /scm/pulls/{n}/comments {repo, body, as?}` → **201** the comment.
- **No merge route, no approve route in protocol 1.**

#### Checks

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
      "suite": "s_77", "job": "88001"},
     {"id": "88100", "name": "codecov/patch", "app": "codecov", "status": "completed", "conclusion": "failure",
      "url": "https://github.com/acme/web/runs/88100", "detailsUrl": "https://app.codecov.io/…",
      "title": "62% of diff hit (target 80%)", "summary": "…", "annotations": 3,
      "startedAt": 1789990001000, "completedAt": 1789990002000, "suite": "s_78", "job": ""}],
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
  | `checks` | every check run on the sha (at most 100), including those that report a job (`job` = its id): a consumer that shows `jobs` folds those into their job. `annotations` is their count; `title`/`summary` the check's own output (≤ 4 KiB each). `url` is the check on the host, `detailsUrl` where the check says to look. |
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
  anything before `from` was left out. `complete` is false while the job
  runs and the host serves partial logs. The text is as the host serves it
  (escape codes kept; a consumer strips them) and untrusted.
  **409 `in-progress`** with `url` while the job runs on a host that serves
  logs only once a job completes (GitHub); 404 `not-found` for an unknown
  job or a log the host no longer keeps.
- `GET /scm/checks/runs/{id}/annotations?repo=&limit=&cursor=&as=` → a
  check run's annotations, `{items: [{path, startLine, endLine, level,
  title, message}], next}`: `level` `notice` \| `warning` \| `failure`;
  `message` ≤ 4 KiB; `limit` ≤ 50.
- `POST /scm/checks/rerun {repo, runId, failedOnly, as?}` → **202**
  `{runId, attempt}`: re-run a workflow run (`failedOnly`: only its failed
  jobs, and what depends on them). The optional cap **`checks.rerun`**;
  without it 501 `unsupported`. 409 `exists` while the run is still in
  progress. A provider may refuse it for an identity (403 `identity`);
  consumers offer it to people only (§2.3 V12).

#### Issues

- `GET /scm/issues?repo=&state=&labels=&since=&q=&as=&limit=&cursor=` →
  `{items: [{number, title, body, state, labels, author, url, updatedAt}], next}`;
  pull requests are filtered out.
- `GET /scm/issues/{n}?repo=&comments=1&as=` → the issue, with `comments`
  (oldest first) when asked.

### 4.10 Events

#### Subscriptions

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
  again before an event of a private repo is delivered for it (access is
  lost: the event is dropped and the subscription deleted; §5.10). A
  tile's subscription needs the bot to see the repo.
- `GET /scm/subscriptions` → `{items}` (this consumer's, for this caller);
  `DELETE /scm/subscriptions/{id}` → **204**.

#### Delivery

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
  "data": {"checks": {"suite": "s_77", "headSha": "9fceb02…",
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

`workflow`, `job` and `check` are **progress** events: a consumer updates
what it shows with them and never acts on them alone — `checks.completed`
(a suite, or a final status) is the one that says CI is done. A provider
that can't send progress events leaves them out; consumers then read
`GET /scm/checks`.
- `forPid` (with `for: user:<id>`): the partition id of the person the
  subscription was made in (`X-XBin-Partition-Id`). A person deleted and
  re-created under the same id is another person with another partition
  id (docs/partitions.md): a consumer drops an event whose `forPid` isn't
  its partition's, and a provider never delivers an old person's
  subscriptions to a new one (§5.6).
- `actor.self` marks the provider's own app or bot. `actor.association`
  is `OWNER`, `MEMBER`, `COLLABORATOR`, `CONTRIBUTOR` or `NONE`.
- `topic` grammar (kept for agent-inbox compatibility):
  `scm/<host>/<owner>/<repo>/(pull/<n>|issue/<n>|branch/<ref>|repo)/<kind>.<action>`.
- `eventId` is unique per event (`scm:<host>:<delivery>[:<i>]` when one
  delivery yields several events); a consumer dedupes on it.
- Bodies in `data` are untrusted; `summary` is the provider's own words.
- The consumer answers **200** (taken, or a duplicate), **404** (no such
  subscriber here — dropped and counted), anything else or no answer —
  the provider retries with backoff (10 s doubling to 1 h, for 24 h).
- `GET /scm/events?since=&repo=&limit=` → `{items: [event…], next}`: the
  events delivered (or due) to this caller in the last 7 days, for
  catching up.

### 4.11 Poll

`POST /scm/poll {as?, items: [{id, kind: pull|checks|issue|comments, repo, number?, ref?, etag?, since?}]}`
(≤ `limits.pollItems`) →

```json
{"items": [{"id": "t3-pull", "changed": false, "etag": "W/\"…\""},
           {"id": "t3-checks", "changed": true, "etag": "…", "value": {"sha": "…", "state": "failure", …}},
           {"id": "t4-pull", "changed": false, "error": {"refusal": "not-found", "error": "…"}}],
 "retryAfterMs": 0}
```

Each item is the conditional GET of its route (`pull` → `/scm/pulls/{n}`,
`checks` → `/scm/checks?ref=`, `issue` → `/scm/issues/{n}`, `comments` →
`/scm/pulls/{n}/comments?since=`); `value` is that route's answer when
`changed`. The provider makes at most 4 upstream calls at once.
`retryAfterMs` asks the consumer to wait (rate limits).

### 4.12 Partitioned consumers

- A partitioned consumer's person's partition reaches the same person's
  partition of a partitioned provider: person tokens, sign-in, the
  person's subscriptions (`for: user:<id>`).
- Its global instance reaches the provider's global: bot only,
  `for: global`.
- Events always go to the consumer's **global** instance, which hands
  `for: user:<id>` on to that person's partition (the agent: partition mail
  `handoff/scm`, §11.1).
- People who use the consumer need read access on the provider tile (and,
  when the workspace's `partitionConsent` is on, their consent for the
  edge, docs/partitions.md §Calls between partitioned tiles).

### 4.13 Handing a token to a sandbox (consumer rules)

- Put it in an exec's environment, or in a 0600 file outside any repo,
  rewritten before `refreshAfter`. Never the refresh token (a consumer
  never has one).
- A person's token only in a private sandbox homed in that person's
  partition, never one a non-secure (hosted) conversation used, nor one
  made from a sandbox other people could write to (a clone of a shared
  sandbox).
- A bot token only in a sandbox whose every user may act through the bot
  for those repos (§4.4's last rule).
- A clone or fork gets its own `purpose`; the source's is revoked when the
  source leaves.
- Redact token values everywhere output is kept.

### 4.14 Building a provider

- A GitLab or Gitea provider maps: installation → group/project access
  token or bot user; device flow → the host's OAuth device grant;
  checks → pipelines/jobs; reviews → merge-request approvals and notes.
  The contract's shapes stay; `scm.kind` says which host family.
- CI maps too: workflow runs → pipelines, jobs → jobs (no steps: `steps:
  []`), job logs → the job trace, which such hosts serve while it runs
  (`complete: false`, and no `in-progress` refusal).
- A conformance suite (modelled on `sdk/sandboxcontract`) is future work.

### 4.15 Versions

`protocol` is 1. Additions (caps, fields, routes) don't change it; a
consumer ignores what it doesn't know and checks `caps`. A change that
breaks a consumer is protocol 2, offered beside 1 (`protocols`).

## 5. The scm-github template (G1, G2)

A builtin **template** at `G` (`builtin-templates/scm-github`), built like
`builtin-templates/coding-sandbox` (its `xbin.json`, `go.mod.tile`,
`_backend/`, `API.md`, `AGENTS.md`, `CLAUDE.md`). No xbind change. Its
default path is `apps/scm-github`; resource targets are written
`res:apps/scm-github/<name>` and instantiation rewrites them (as the agent's
`res:apps/agent/db`, internal/broker/clone.go `rewriteRefs`).

### 5.1 Files

| Path | WP | What |
|---|---|---|
| `G/xbin.json` | G1 | the manifest (§5.2) |
| `G/scope.json` | G1 | `state` kv, `conf` kv `"shared":"read"`, `tick` cron |
| `G/go.mod.tile`, `G/go.sum` | G1 | module `scm-github`, **`go 1.24`** (as llm-gw and coding-sandbox: a higher go line breaks the tile's build after a downgrade to xbind v0.3.64 or older, docs/compat.md); requires the sdk only (as `builtin-tiles/llm-gw/go.mod.tile`): JWT, HTTP and storage from the standard library and the sdk |
| `G/index.html`, `G/scm.js` | G1 | the page: setup (managers), your sign-in, installations, policy, webhook health |
| `G/native.js` | G1 | the native view of the same page (status, your sign-in, Forget) |
| `G/API.md`, `G/AGENTS.md`, `G/CLAUDE.md` | G1 (G2 adds §Events) | §5.14 |
| `G/_backend/*.go`, `G/_backend/testdata/*.json` | G1, G2 | §5.12 |

### 5.2 Manifest

```jsonc
{
  "template": {
    "title": "GitHub (scm)",
    "description": "Repos, credentials, pull requests, CI and events from GitHub for the agent and any tile that binds service scm (docs/scm.md): a GitHub App at the global instance, each person's own GitHub sign-in in their partition.",
    "defaultName": "scm-github",
    "partition": ["user", "global"]
  },
  "partitionNote": "A full switch forgets every person's GitHub sign-in (they sign in again). A full switch, or removing global, deletes the App's keys and secrets, the identity directory, subscriptions and undelivered events: paste the keys again, or make a new private key in the App's settings.",
  "runtime": "go",
  "entry": "./_backend",
  "uses": [
    { "target": "res:apps/scm-github/state", "role": "writer" },
    { "target": "res:apps/scm-github/conf", "role": "writer" },
    { "target": "res:apps/scm-github/tick", "role": "writer" },
    { "target": "cap:open-links", "role": "writer" }
  ],
  "interfaces": {
    "net": { "kind": "net" },
    "agents": { "kind": "http", "service": "agent-inbox", "multi": true }
  },
  "exposes": {
    "hooks": { "kind": "http", "paths": ["/hook/github", "/setup/github"] }
  },
  "expose": {
    "roles": {
      "admin": "The tile, its owner and its managers: the page, setup and policy (API.md)",
      "consumer": "Use GitHub through this tile: the scm contract's /scm/* routes (docs/scm.md)"
    }
  },
  "provides": {
    "scm": { "kind": "http", "service": "scm", "role": "consumer" }
  }
}
```

- `net` is bound to `internet`, or narrowed to
  `internet:github.com,api.github.com,*.actions.githubusercontent.com,*.blob.core.windows.net`
  — job logs are a redirect to those last two (API.md says so; a log fetch
  that fails on egress answers 502 `upstream` naming the host).
- Comments in the manifest say what each line is for, as the agent's do.
- `exposes.hooks` is published only for the global instance
  (`bx expose apps/scm-github hooks=… --host …`); a partition never serves
  `/hook/*` or `/setup/*` (404).

### 5.3 Modes

By `xbin.Partition()` (sdk/partition.go):

| Mode | `XBIN_PARTITION` | Holds | Identities handed out |
|---|---|---|---|
| **legacy** | `""` | App keys, installation tokens, setup, webhooks (as global, below), no identity directory | `bot` only (§4.4 last two rows) |
| **global** | `global` | App keys, installation tokens, webhooks, setup, policy, **identity directory**, subscriptions, outbox | `bot` |
| **user** | `user:<id>` | that person's sign-in (vault) | `person`; `bot` relayed to global under `botForPeople` |

`mode.go` decides the mode once at start and the caller class per request
(§4.3): `self`, `owner`, `relay` (global only: `personFromPartition`'s rule —
`From == xbin.Self()`, `User != ""`, `Partition` `user:…`, role reader or
writer; any other call with `Partition: user:…` is 403, never `self` or
`owner`, an admin's included), `person-page` (a person in a frame, no ViewedBy), `tile`
(another tile, no partition or `global`), `person-consumer` (another tile,
`Partition: user:<id>`, in user mode). `consumerKey = From|Deployment|Partition-Id`.

**Guards.** `/scm/*`: role `consumer` or `admin`, class `tile` or
`person-consumer` (or `person-page` for `/scm/signin*` and `/scm/hello`).
`/partition/*`: global only, class `relay` only. `/setup/*`, `/api/policy`,
`/api/revoke-all`: `manager(r)` — the owner token; the tile itself at
global; or a person at global (or legacy) whose level is write or terminal
and who is not viewing as someone (a person's level at global is clamped
to writer by xbind, docs/partitions.md §The global instance and people's
partitions, so `RoleFunc("admin")` can't express it). `/hook/github`:
ingress, or the owner/admin testing (`builtin-tiles/webhooks` rule).

### 5.4 Secrets and state

| Where | Key | What |
|---|---|---|
| global vault | `app-private-key` | the App's PEM key |
| global vault | `app-client-secret` | the OAuth client secret |
| global vault | `app-webhook-secret`, `app-webhook-secret-prev` | the webhook secret; `-prev` for 24 h after a rotation |
| global `state` | `app` | `{appId, clientId, slug, owner, htmlUrl, host, apiBase, webBase, keyFingerprint, createdAt}` |
| global `state` | `policy` | §5.9 |
| global `state` | `ident/<xbin user>` | `{login, id, pid, at}` — the identity directory; `pid` the person's partition id (`X-XBin-Partition-Id`) |
| global `state` | `sub/<id>`, `outbox/<id>`, `seen/<installation>/<delivery>`, `inst/<owner>`, `access/<login>/<repo>` | subscriptions (a person's with its `pid`), outbox items (with `forPid`), delivery dedupe per allowed installation (7 d, ≤ 10 000 each), installation cache, read-access cache (≤ 1 h) |
| `conf` (global writes, partitions read) | `public` | `{clientId, slug, host, apiBase, webBase, deviceFlow, installUrl, policy: {botForPeople, allowWorkflows}, configured}` — never a secret |
| a person's partition vault | `user-refresh`, `user-access`, `signin-device` | the refresh token, the current parent access token with its expiry, a pending device code |
| a person's partition `state` | `person` | `{login, id, expiresAt, refreshExpiresAt, epoch}` |

No secret is ever written to kv, a log line, an error or a response. A
token in memory is a `secretString` type whose `String()`, `GoString()`,
`Format` and `MarshalJSON` say `[secret]` (as the agent's `scmSecret`, so
`%#v` and `%+v` of a struct holding one hide it too); the one response that carries a token (`POST /scm/token`)
builds its JSON explicitly.

### 5.5 Setup (managers, at global; G1)

- **Paste.** `POST /setup/app {appId, clientId, clientSecret, privateKey,
  webhookSecret?}` → validates by signing a JWT (§5.7) and calling `GET
  /app` (must answer the same `appId`) and `GET /app/hook/config`; then
  `PATCH /app/hook/config {url, secret, content_type: "json"}` when the URL
  or secret differ (the URL is the hooks exposure's public URL + `/hook/github`,
  given in the body as `hookUrl` — the page reads it from the exposure, or
  the manager types it). 200 `{app, hook: {url, active}}`. Secrets are
  write-only from then on (`GET /setup/app` shows the fingerprint only).
- **Manifest flow (spike S1).** `POST /setup/manifest {org?, name,
  publicHost?, presets: ["ci"?, "workflows"?]}` → `{postUrl, manifest,
  state}`; `state` 32 random bytes (base64url), single use, 1 h, bound to the
  manager who asked. The page submits a form POST to `postUrl` in a new tab
  (`cap:open-links`). The code comes back by (a) ingress `GET
  /setup/github?code=&state=`, (b) the tile URL top level, or (c) the
  manager pasting the address they landed on (`POST /setup/manifest/code
  {url}`). `POST /app-manifests/{code}/conversions` (within 1 h) yields
  the App, stored as for Paste. If the spike fails, (c) alone ships and §19
  says so.
- **App permissions** (both paths; the manifest's `default_permissions`):
  `contents: write`, `pull_requests: write`, `issues: write`, `checks:
  read`, `statuses: read`, `actions: read` (job logs), `metadata: read`,
  and the organization permission `members: read` (the access events
  below); preset `ci` adds `actions: write` (reruns — it also allows
  dispatching, cancelling and deleting runs, so it is opt-in), preset
  `workflows` adds `workflows: write`. **Events:** `pull_request`,
  `pull_request_review`, `pull_request_review_comment`, `issue_comment`,
  `issues`, `push`, `check_suite`, `check_run`, `status`, `workflow_run`,
  `workflow_job`, `installation`, `installation_repositories`, `member`,
  `membership`, `organization` (the last three only refresh the access
  cache, §5.10, and are never forwarded).
- **Accounts.** Setup writes `allowedAccounts` = the App's own account
  (§5.9); a manager adds others. Installations on any other account are
  listed as **foreign installations** (`GET /setup/installations`) with a
  link to remove them — a public App can be installed by anyone.
- **Afterwards the page shows:** "Enable Device Flow" (link to the App's
  settings + **Check**: `POST /setup/check` probes
  `/login/device/code` and answers `{deviceFlow: true|false}`), "Expire
  user authorization tokens must stay on", the Install link, the
  installations (`GET /setup/installations`), the policy (§5.9), webhook
  health (§5.10), and **Revoke all bot tokens** (`POST /api/revoke-all`).
- `conf` `public` is rewritten after every change.

### 5.6 Sign-in, identity directory and the relay

**Device flow (user mode; G1).**

- `POST /scm/signin` (§4.8): `POST https://<host>/login/device/code
  {client_id, scope: ""}`; keep `device_code` in the vault
  (`signin-device`, with `expiresAt`, `intervalMs`, `pollId` = 16 random
  bytes base64url); answer `pending`. A background poller (one per process)
  asks `POST /login/oauth/access_token {client_id, device_code,
  grant_type: "urn:ietf:params:oauth:grant-type:device_code"}` every
  interval (+5 s after `slow_down`); `GET /scm/signin/{pollId}` reads its
  state and, if it is due, asks once itself.
- On success: `GET /user` → `{login, id}`; vault `user-access`,
  `user-refresh`; state `person`; then **register** at global (below).
  `access_denied` → `denied`; `expired_token` → `expired`;
  `device_flow_disabled` → 503 `setup`.
- **Refresh** (user mode, direct, no secret): `POST
  /login/oauth/access_token {client_id, grant_type: "refresh_token",
  refresh_token}` under a process mutex; the answer replaces both tokens
  (the old ones die). `bad_refresh_token` → clear the pair, answer 409
  `signin` (starting a new flow).
- **Forget** (`DELETE /scm/signin`): relay `revoke-grant`, then clear the
  vault and state, then relay `DELETE /partition/identity`.

**The relay** — routes at **global** for a person's own partition
(class `relay`; the person is the call's `User`, never a body field).
**A relay body is the person's own input** (§4.3): their frame or terminal
in their partition calls these routes exactly as the partition's backend
does, so global trusts nothing in it — it re-checks every field against
the App and the policy, as below, and the partition's own checks are a
convenience. The caller's partition id (`X-XBin-Partition-Id`) is
compared with `ident/<user>.pid` on every call: a different one (a person
deleted and re-created under the same id) wipes the old `ident/<user>`
and every subscription and outbox item of the old one before anything
else.

| Route | Body | Global does | Answer |
|---|---|---|---|
| `POST /partition/identity` | `{accessToken}` | `POST /applications/{clientId}/token` (basic auth `clientId:clientSecret`) with `{access_token}` — which answers only for a token **this App** issued, with its `user`; stores `ident/<user>` = `{login, id, pid, at}` from that answer (never from `GET /user`: any token the person holds — a PAT, another App's — would name a login); forgets the token | 200 `{login, id}`; 404/422 → 403 `identity` |
| `DELETE /partition/identity` | — | deletes `ident/<user>` and that person's subscriptions | 204 |
| `POST /partition/scope` | `{accessToken, owner, repos, permissions, access}` | checks the token as `identity` does (its user must be `ident/<user>`); computes the permissions **itself** — the preset for `access` (§4.8), narrowed by `permissions`, `workflows: write` only under `allowWorkflows`; `owner` within `allowedAccounts`; then `POST /applications/{clientId}/token/scoped` (basic auth) with `{access_token, target: owner, repositories, permissions}`; the answer's expiry capped by `personTtlMin` | 200 `{token, expiresAt, repos, permissions}`; 422 → 400 `invalid`; a policy refusal → 403 `not-allowed` |
| `POST /partition/revoke-token` | `{accessToken}` | `DELETE /applications/{clientId}/token` | 204 |
| `POST /partition/revoke-grant` | `{accessToken}` | `DELETE /applications/{clientId}/grant` | 204 |
| `POST /partition/bot-token` | §4.8 token body | policy `botForPeople`: `off` → 403 `identity`; `own-access` → `GET /repos/{o}/{r}/collaborators/{login}/permission` with an installation token, `login` from `ident/<user>` (none: 409 `signin`), every repo at least `write` (or `read` for `access: read`); `on` → as a tile's request; in every case global applies the whole policy itself — the preset, `allowWorkflows`, `allowedAccounts`, `botRepos` | §4.8 token answer, `identity.kind: "bot"` |
| `POST /partition/subscriptions` | `{consumer, sub}` — `consumer` the calling tile's path as the partition saw it (`X-XBin-From`), `sub` §4.10's body | `consumer` must be the path of one of global's `agents` bindings (else 400 `invalid`) — a person naming another bound consumer only sends their own events (`for: user:<user>`, `forPid`) there; §5.10 person checks with `ident/<user>`; stored with `for: user:<user>`, the caller's `pid` and that consumer | §4.10 |
| `DELETE /partition/subscriptions/{id}` | — | only that person's | 204 |

Global never stores or logs `accessToken`; the relay handlers take it in a
`secretString` and drop it on return.

### 5.7 Tokens (G1)

- **JWT** (`gh_jwt.go`): RS256 with `crypto/rsa`; key from
  `x509.ParsePKCS1PrivateKey`, falling back to `ParsePKCS8PrivateKey`;
  `iat = now − 60 s`, `exp = now + 9 min`, `iss = clientId`; cached 8 min.
- **Bot.** Installation per owner: `GET /repos/{o}/{r}/installation`
  (cached 1 h in `inst/<owner>`; 404 → 409 `not-installed {install: {url,
  owner}}`). Repos of several owners → 400 `invalid`. Mint: `POST
  /app/installations/{id}/access_tokens {repositories: [names],
  permissions}` with the preset (§4.8) narrowed by `permissions`. Cache in
  memory, keyed `(consumerKey, purpose, installation, sorted repos,
  permissions)`, ≤ 2000 entries LRU; reuse while `expiresAt − now ≥
  max(minTtlSec, 15 min)`; `refreshAfter = expiresAt − 10 min`. A token is
  never assumed to have a length (stateless `ghs_` tokens are ~520 chars).
- **Person.** `ensureUser`: refresh when the parent has < 65 min left (or
  on a 401). Scope through the relay (`/partition/scope`); the handed-out
  `expiresAt = min(scoped.expires_at, parentExpiry − 60 min)` — the
  **epoch**: the parent refreshes only at or after that point, so every
  scoped token of a person rotates together (~7 h). If spike S2 shows a
  scoped token survives its parent's refresh, the epoch goes (§19).
  `author` = `{login, <id>+<login>@users.noreply.<host>}`.
- **Cache keys and revocation.** `POST /scm/token/revoke {token}` matches
  the SHA-256 of tokens this consumer received; `{purpose}` every live one.
  Bot: `DELETE /installation/token` with the token (spike S3: stateless
  tokens). Person: relay `revoke-token`. A policy change empties the bot
  cache. `POST /api/revoke-all` revokes every cached bot token.
- **Internal write token.** Writes made `as: bot` (`POST /scm/pulls`,
  `PATCH`, comments) use a separate cached installation token with only
  the permissions that write needs (`pull_requests`, `issues: write` for
  comments); never handed out. A rerun is never made as the bot (§4.9:
  consumers offer it to people, as themselves).

### 5.8 The contract routes (G1)

`hello.go`, `tokens.go`, `signin.go`, `repos.go`, `pulls.go`, `checks.go`,
`issues.go`, `poll.go` implement §4 exactly. GitHub specifics:

- `gh_client.go`: REST with `Accept: application/vnd.github+json`,
  `X-GitHub-Api-Version: 2022-11-28`; `Link` pagination; an ETag LRU (2000
  entries) so a repeat is conditional; `X-RateLimit-*` tracked per
  installation and per person → `limit` with `retryAfterMs`; `X-GitHub-SSO`
  → `not-allowed` with `sso.url`; GraphQL for draft ↔ ready.
- Pulls: GitHub's 422 "A pull request already exists" → find it (`GET
  /pulls?head=<owner>:<branch>&state=open`) and answer 200 `existing:
  true`. `mergeable: null` → `mergeableState: "unknown"`, `retryAfterMs:
  3000`.
- Comments: issue comments, reviews and review comments merged by
  `createdAt`.
- **Checks** (`checks.go`): `GET /repos/{o}/{r}/commits/{ref}/check-runs`
  (all pages, ≤ 100), `…/commits/{ref}/status`, `GET
  /repos/{o}/{r}/actions/runs?head_sha=<sha>&per_page=20`, and per run `GET
  /actions/runs/{id}/jobs?filter=latest&per_page=100` — at most 4 upstream
  calls at once, every one conditional, the whole answer cached per (repo,
  sha) for 5 s. Job log: `GET /repos/{o}/{r}/actions/jobs/{id}/logs` (a
  redirect, followed; only for a completed job — a running one is 409
  `in-progress` with the job's `html_url`); the tail is cut after download,
  reading at most 8 MiB. Annotations: `GET /check-runs/{id}/annotations`.
  Rerun: `POST /actions/runs/{id}/rerun-failed-jobs` or `…/rerun` with the
  person's scoped token with `actions: write` — `as: bot` is 403
  `identity`; offered as `checks.rerun` only while the App has `actions:
  write` (preset `ci`) and `allowRerun` is on.
- Spike S4: is there any partial-log API for a running job? (GitHub's live
  log page streams through an undocumented endpoint; if nothing public
  exists, `in-progress` stands.)

### 5.9 Policy (global `state` `policy`; G1)

| Key | Type | Default | Meaning |
|---|---|---|---|
| `botForPeople` | `off` \| `own-access` \| `on` | `off` | whether a person's partition may get a bot token (§4.4). `own-access` and `on` let a person mint a bot token for themselves directly — their frame reaches `/partition/bot-token` — outside any consumer's sandbox gate; the page says so beside the switch |
| `botRepos` | list of `owner/name` globs | `["*/*"]` | which repos a bot token (any caller) may name; a request outside → 403 `not-allowed` |
| `allowWorkflows` | bool | `false` | whether `workflows: write` may be asked for (preset `workflows` App only) |
| `allowedAccounts` | list of logins | the App's own account (written at setup); `[]` refuses everything | the accounts (orgs, users) tokens, reads and events may concern; others → 403 `not-allowed`, and their webhook deliveries are dropped before anything else (a public App's guard, §5.10) |
| `allowRerun` | bool | `true` | offer `checks.rerun` — only while the App has `actions: write` (preset `ci`), so off by default in effect |
| `personTtlMin` | int | `0` (the epoch decides) | caps a person token's life below the epoch |

`PUT /api/policy` (manager) replaces it; `GET /api/policy` reads it;
`conf` `public.policy` mirrors `botForPeople` and `allowWorkflows`.

### 5.10 Webhooks, subscriptions, outbox and delivery (G2)

1. **Receipt** — `POST /hook/github`: ingress (or owner/admin testing);
   body ≤ 8 MiB; HMAC `X-Hub-Signature-256` in constant time against the
   current and `-prev` secret (the `builtin-tiles/webhooks/backend/verify.go`
   pattern); a delivery whose `installation.account` isn't in
   `allowedAccounts` is dropped **before** dedupe and normalisation
   (counted; its installation shows as foreign, §5.5); dedupe on
   `X-GitHub-Delivery` per allowed installation (`seen/<installation>/`,
   7 days, ≤ 10 000 each — one installation's traffic can't evict
   another's); `installation*` refresh `inst/`, and `member`, `membership`,
   `organization` and `installation_repositories` drop the `access/` cache
   entries they touch — none of them is forwarded; `ping` answers 200. Everything else: normalise (`normalize.go`, §4.10 table; one
   delivery may yield several events, `eventId` suffix `:<i>`), match
   subscriptions, write one outbox item per (consumer, `for`), answer
   **202** at once.
2. **Normalisation table** (GitHub → v1): `pull_request` → `pull`
   (`closed`+`merged` → `merged`; `ready_for_review` → `ready`;
   `converted_to_draft` → `draft`); `check_suite.completed` and a final
   `status` → `checks.completed`; `check_run` → `check`; `workflow_run` →
   `workflow`; `workflow_job` → `job`; `issue_comment` on a PR or issue →
   `comment`; `pull_request_review_comment` → `comment` (`path`, `line`);
   `pull_request_review.submitted|dismissed` → `review`; `push` → `push`
   (`forced`); `issues` → `issue`. `actor.self` = the App's bot
   (`<slug>[bot]`). Bodies clipped at 8 KiB. A `status` and a
   `check_suite` for the same sha within 5 s produce one `checks.completed`
   when both are final.
3. **Subscriptions** (`subs.go`): `POST|GET|DELETE /scm/subscriptions` at
   global from a tile (`for: global`, the bot must see the repo); a
   person's arrive by the relay (§5.6) — global checks the person's login
   from `ident/<user>` (none → 409 `signin`) and their read access with
   `GET /repos/{o}/{r}/collaborators/{login}/permission` (installation
   token; `none` → 403 `not-allowed`). In user mode `/scm/subscriptions`
   relays. Expiry 30 days; `key` replaces. A person's subscription keeps
   the `pid` it was made with.
4. **Outbox and delivery** (`outbox.go`, `deliver.go`): one item per
   (event, consumer, `for`); a worker POSTs `/adapter/scm/event` to the
   consumer through the `agents` binding (`XBIN_IFACE_AGENTS` endpoints;
   the consumer is the binding whose tile path equals the subscription's
   `From`). An item `for: user:<id>` of a **private** repo is checked
   first: the person's read access again (`access/<login>/<repo>`, cached
   ≤ 1 h, dropped by the access events of step 1); `none` → the item is
   dropped and that person's subscriptions on the repo deleted. Every
   person's item carries its subscription's `pid` as the event's `forPid`.
   200 → delivered; 404 → dropped, counted; anything else →
   retry 10 s doubling to 1 h, for 24 h, then dropped and counted. The
   `tick` cron (`@every 1m`) is registered while the outbox holds items and
   removed when empty. Optional catch-up: on start, `GET
   /app/hook/deliveries` since the last seen, redelivering failures.
5. **Events health** (hello `events`): `webhooks` `active` when GitHub's
   hook config is active and a delivery arrived in the last 24 h (or
   `ping`), `inactive` when the config says so, else `unknown`; `healthy`
   = active and no signature failures in the last hour;
   `lastDeliveryAt`; `pollMinMs` 120000.
6. `GET /scm/events?since=&repo=&limit=` (global, a tile's; relayed for a
   person) — the outbox's delivered and pending items for that consumer and
   `for`, 7 days.

### 5.11 Spikes (G1 records each outcome in its record and §19)

| # | Question | If no |
|---|---|---|
| S1 | Does GitHub accept the manifest form POST from a sandboxed frame's new tab (`Origin: null`)? Is the frame token injected when the tile URL is loaded top level? | manifest flow via (c) paste only; Paste setup stays primary |
| S2 | Does a scoped user token survive its parent's refresh? Does `/token/scoped` take basic auth with the client secret as documented? | keep the epoch (default) |
| S3 | Does `DELETE /installation/token` revoke a stateless `ghs_` token? | revocation is best effort; the 1 h life bounds it (API.md says so) |
| S4 | Any public partial-log API for a running Actions job? | `in-progress` stands (default) |

Spikes run against the fake GitHub only where the answer is the fake's;
real answers need a live App this machine lacks — the record says which
were answered from GitHub's documentation and which are owed.

### 5.12 Backend files (each ≤ 800 lines)

| File | WP | Contents |
|---|---|---|
| `main.go` | G1 | mux, mode, start, `tick` |
| `mode.go` | G1 | caller classes, `consumerKey`, guards, `manager(r)` |
| `errors.go` | G1 | refusals (§4.6), `secretString` |
| `gh_client.go`, `gh_jwt.go` | G1 | REST/GraphQL client, ETag LRU, rate tracking; JWT |
| `app.go`, `setup.go`, `policy.go` | G1 | the App, setup routes, policy |
| `bot.go`, `person.go`, `signin.go`, `relay.go` | G1 | bot tokens; person tokens and refresh; device flow; the relay (both sides) |
| `hello.go`, `tokens.go`, `repos.go`, `pulls.go`, `checks.go`, `issues.go`, `poll.go` | G1 | §4 routes |
| `page.go` | G1 | `/api/*` for the page |
| `hook.go`, `normalize.go`, `subs.go`, `outbox.go`, `deliver.go` | G2 | §5.10 |
| `fakegh_test.go` | G1 (G2 extends) | the fake GitHub (§15.2) |
| `testdata/*.json` | G2 | webhook fixtures, each < 20 KB: pull_request (opened, closed-merged, synchronize), pull_request_review, review comment, issue_comment, issues, push, check_suite, check_run, status, workflow_run, workflow_job (queued, in_progress with steps, completed), installation |

### 5.13 Trust

Writers and admins of scm-github are in the trust base of everyone who uses
it. The global instance sees a person's access token only in transit (scope,
revoke, identity registration) and never stores it. Webhook bodies are
attacker-controlled: every field is validated, bodies are clipped and
labelled untrusted, and nothing in them is executed or interpreted.

### 5.14 `G/API.md` outline (G1; G2 writes §7)

1. What it is. 2. Setting it up (instantiate; `bx bind apps/scm-github
net=internet` or the narrowed list in §5.2; approve `cap:open-links`; create
or paste the App; enable Device Flow; install the App; `bx expose
apps/scm-github hooks=… --host …`; `bx bind apps/scm-github agents+=apps/agent`;
`bx bind apps/agent scm+=apps/scm-github`; people need read access on the
tile, and consent when `partitionConsent` is on). 3. Modes. 4. Your GitHub
sign-in. 5. Identities and policy. 6. Tokens (lifetimes, the epoch,
revocation, handing them to sandboxes). 7. Events and polling. 8. CI:
runs, jobs, steps, logs (completed jobs only), annotations, rerun. 9.
GitHub notes on the contract. 10. Page routes and their guards. 11. Trust.
12. Limits. 13. Spikes and what they decided. `docs/scm.md` (§4) is the
contract; API.md is GitHub's side of it and links there.

## 6. Agent: data model

All tables are additive and idempotent (`CREATE … IF NOT EXISTS`, `ALTER
TABLE … ADD COLUMN` guarded by a column check, as `migrate_conv.go` does),
created by a function each WP registers in **`schemaAdds`** (§14.1) from
its own file's `init()`; `openDB` runs them after `migrate()`
(`addFeatureSchemas`, B/migrate.go, S0), in registration order. No schema
function relies on another's tables (no foreign keys, no triggers across
them). They never run on the `team` database: `migrateTeamRuns`
(B/team_runs.go:59) calls `migrate()` alone, so `team`'s schema — which
`teamCovers` compares across versions — stays the run store's, and no hook
writes a project or CI row there (hosted runs are skipped, §14.1). Every
WP adds a migrate-twice test and an old-database fixture test — the
fixture written with `seedOldDB` (B/harness_store_test.go:37, as
`TestHarnessMigrationKeepsOldRows` does), per AGENTS.md's "migrations
idempotent with a fixture test".

Times are Unix ms (`*_ms`, `created`, `at`); JSON columns default to a
valid empty value. Types are SQLite's.

### 6.1 `projects` (P1)

```sql
CREATE TABLE IF NOT EXISTS projects (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  uid          TEXT    NOT NULL UNIQUE,          -- 6 × [a-z0-9], random, never reused: branches, paths, labels, clientIds
  name         TEXT    NOT NULL,                 -- ≤ 80 chars, shown
  slug         TEXT    NOT NULL,                 -- [a-z0-9-]{1,40}: the workspace directory
  kind         TEXT    NOT NULL DEFAULT 'personal', -- personal | team | membership
  team_ref     INTEGER NOT NULL DEFAULT 0,       -- membership: the team project's id at global
  owner        TEXT    NOT NULL DEFAULT '',      -- the person (or el:<element>)
  visibility   TEXT    NOT NULL DEFAULT 'private', -- private | team
  team_role    TEXT    NOT NULL DEFAULT 'viewer',  -- what team visibility grants: viewer | participant
  scm          TEXT    NOT NULL DEFAULT '',      -- the scm provider's tile path (a binding of the scm slot)
  host         TEXT    NOT NULL DEFAULT '',      -- github.com
  sandbox_ref  TEXT    NOT NULL DEFAULT '',      -- <manager>|<sandbox id>: the workspace sandbox ('' for a team definition without a seed)
  sandbox_made INTEGER NOT NULL DEFAULT 0,       -- 1: the project created it
  dir          TEXT    NOT NULL DEFAULT '',      -- <workdir>/<slug>, once the sandbox is known
  policy       TEXT    NOT NULL DEFAULT '{}',    -- ProjectPolicy JSON (unknown keys kept)
  fork_snap    TEXT    NOT NULL DEFAULT '',      -- the fork-base snapshot id (P2)
  fork_snap_ms INTEGER NOT NULL DEFAULT 0,
  state        TEXT    NOT NULL DEFAULT 'active', -- active | archived | deleting
  version      INTEGER NOT NULL DEFAULT 1,       -- bumped by every PATCH
  from_seed    INTEGER NOT NULL DEFAULT 0,       -- membership: its sandbox was cloned from the team's seed (§12.2)
  def_hash     TEXT    NOT NULL DEFAULT '',      -- membership: the accepted definition's security hash (§12.2)
  def_pending  TEXT    NOT NULL DEFAULT '',      -- membership: a changed definition awaiting acceptance (its JSON; '' none); the API shows its hash
  created_by   TEXT    NOT NULL DEFAULT '',
  created_ms   INTEGER NOT NULL DEFAULT 0,
  updated_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug) WHERE state<>'deleting';
CREATE INDEX IF NOT EXISTS idx_projects_team ON projects(team_ref) WHERE team_ref<>0;
CREATE INDEX IF NOT EXISTS idx_projects_sbx ON projects(sandbox_ref) WHERE sandbox_ref<>'';
```

- The slug comes from the name (lowercased, non-alphanumerics → `-`,
  trimmed, ≤ 40), with `-2`, `-3`… on a clash — a membership's too (its
  directory is local to the member's partition, §12.2).
- `kind`: `personal` in a person's partition and in an unpartitioned
  agent; `team` only at a partitioned agent's global instance, which holds
  no other kind (409); `membership` only in a person's partition.
- A person's partition numbers projects from 2^40: `seedProjectIDs` in
  `addProjectSchema`, the `seedTriggerIDs` pattern (B/trigger_registry.go:345,
  `sqlite_sequence` row `projects` at `partitionIDBase−1`), when
  `userMode()`. So `model/homes.js` `homeOf(pid)` routes a project id to
  its home exactly as a conversation id. Projects never live in the team
  database.
- A coordinator is not a column: it is the run with `origin='project'`,
  `origin_id=<pid>` and `session_key='proj:<pid>:coord:<user>'` (one per
  person per project; §10.1).
- `from_seed`, `def_hash`, `def_pending`: P1 creates the columns (one
  table, one migration) and carries them in `Project`; T writes them and
  owns what they mean (§12.2); K's gate reads `from_seed` (§9.2).

### 6.2 `project_members` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_members (
  project_id INTEGER NOT NULL,
  user       TEXT    NOT NULL,
  role       TEXT    NOT NULL DEFAULT 'participant', -- viewer | participant
  added_by   TEXT    NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, user)
);
```

The owner is `projects.owner`, never a member row. A person's partition
keeps no member rows for a personal project (409 `noShareWords`, §7.2): its
projects are private. Members exist in unpartitioned agents and on team
definitions at global.

### 6.3 `project_repos` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_repos (
  project_id     INTEGER NOT NULL,
  slug           TEXT    NOT NULL,              -- [a-z0-9-]{1,40}, unique in the project (from the repo name)
  repo           TEXT    NOT NULL,              -- owner/name
  url            TEXT    NOT NULL DEFAULT '',   -- https clone URL (scm GET /scm/repo cloneUrl)
  default_branch TEXT    NOT NULL DEFAULT '',
  base_path      TEXT    NOT NULL DEFAULT '',   -- bare: <dir>/.repos/<slug>.git; adopted: the clone's toplevel
  mode           TEXT    NOT NULL DEFAULT 'bare',     -- bare | adopted
  checkout       TEXT    NOT NULL DEFAULT 'worktree', -- worktree | clone
  setup          TEXT    NOT NULL DEFAULT '',   -- the setup script (§8.3 setup)
  state          TEXT    NOT NULL DEFAULT 'pending',  -- pending | cloning | ready | failed | removing
  error          TEXT    NOT NULL DEFAULT '',
  fetched_ms     INTEGER NOT NULL DEFAULT 0,
  head           TEXT    NOT NULL DEFAULT '',   -- origin/<default>'s sha at the last fetch
  protected      INTEGER NOT NULL DEFAULT -1,   -- the default branch is protected: 1 | 0 | -1 unknown
  created_ms     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, slug)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_repos_repo ON project_repos(project_id, repo);
```

### 6.4 `project_tasks` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_tasks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  INTEGER NOT NULL,
  n           INTEGER NOT NULL,                 -- 1, 2, …: MAX(n)+1 in the creating transaction
  run_id      INTEGER NOT NULL DEFAULT 0,       -- its conversation; 0 once deleted
  title       TEXT    NOT NULL DEFAULT '',
  slug        TEXT    NOT NULL DEFAULT '',      -- [a-z0-9-]{1,32} from the title
  size        TEXT    NOT NULL DEFAULT 'small', -- small | big
  branch      TEXT    NOT NULL DEFAULT '',      -- <branchPrefix>/<n>-<slug>
  issue       TEXT    NOT NULL DEFAULT '',      -- IssueRef JSON, or ''
  repos       TEXT    NOT NULL DEFAULT '[]',    -- the repo slugs it works in
  sandbox_ref TEXT    NOT NULL DEFAULT '',      -- the project's sandbox, or its own fork
  fork_made   INTEGER NOT NULL DEFAULT 0,
  dir         TEXT    NOT NULL DEFAULT '',      -- <dir>/tasks/<n>-<slug>
  ports_base  INTEGER NOT NULL DEFAULT 0,
  ws          TEXT    NOT NULL DEFAULT 'pending', -- §6.11 workspace states
  phase       TEXT    NOT NULL DEFAULT 'open',    -- open | pr | merged | closed | done | deleted
  prs         TEXT    NOT NULL DEFAULT '[]',    -- []TaskPR JSON
  turn_by     TEXT    NOT NULL DEFAULT '',      -- human | coordinator | event
  last        TEXT    NOT NULL DEFAULT '',      -- its latest answer, clipped to inlineBudget (B/links.go)
  setup_tail  TEXT    NOT NULL DEFAULT '',      -- redacted tail of a failed setup (into the first prompt)
  ci_fixes    TEXT    NOT NULL DEFAULT '{}',    -- {"day": "2026-10-03", "n": 2}: CI-fix inputs today (§11.3)
  error       TEXT    NOT NULL DEFAULT '',
  from_run    INTEGER NOT NULL DEFAULT 0,       -- the coordinator run that created it (0: a person)
  created_by  TEXT    NOT NULL DEFAULT '',
  created_ms  INTEGER NOT NULL DEFAULT 0,
  updated_ms  INTEGER NOT NULL DEFAULT 0,
  cleaned_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ptasks_n ON project_tasks(project_id, n);
CREATE INDEX IF NOT EXISTS idx_ptasks_run ON project_tasks(run_id) WHERE run_id<>0;
CREATE INDEX IF NOT EXISTS idx_ptasks_phase ON project_tasks(project_id, phase);
```

### 6.5 `project_checkouts` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_checkouts (
  task_id    INTEGER NOT NULL,                  -- project_tasks.id
  repo_slug  TEXT    NOT NULL,
  path       TEXT    NOT NULL DEFAULT '',
  mode       TEXT    NOT NULL DEFAULT 'worktree', -- worktree | clone | main
  state      TEXT    NOT NULL DEFAULT 'pending',  -- pending | added | setup | ready | failed | removed | kept
  setup_exit INTEGER,                            -- NULL: no setup ran
  error      TEXT    NOT NULL DEFAULT '',
  remote_sha TEXT    NOT NULL DEFAULT '',          -- the task branch on the remote at the last refs check (§8.3 refs)
  PRIMARY KEY (task_id, repo_slug)
);
```

### 6.6 `project_jobs` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_jobs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  task_id    INTEGER NOT NULL DEFAULT 0,        -- 0: the project's own
  repo_slug  TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,                  -- §8.3
  state      TEXT    NOT NULL DEFAULT 'queued', -- queued | running | waiting | done | failed
  step       TEXT    NOT NULL DEFAULT '',       -- what it is doing, for people
  attempts   INTEGER NOT NULL DEFAULT 0,
  next_ms    INTEGER NOT NULL DEFAULT 0,        -- not before (queued, waiting)
  exec_ref   TEXT    NOT NULL DEFAULT '',       -- the sandbox a background exec runs in
  exec_id    TEXT    NOT NULL DEFAULT '',
  client_id  TEXT    NOT NULL DEFAULT '',       -- the exec's (or create's, snapshot's) clientId
  out        TEXT    NOT NULL DEFAULT '',       -- redacted tail, ≤ 8 KiB
  error      TEXT    NOT NULL DEFAULT '',
  by_user    TEXT    NOT NULL DEFAULT '',       -- who caused it ('' = the worker)
  epoch      INTEGER NOT NULL DEFAULT 0,        -- the engine epoch that claimed it
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pjobs_live ON project_jobs(project_id, task_id, repo_slug, kind)
  WHERE state IN ('queued','running','waiting');
CREATE INDEX IF NOT EXISTS idx_pjobs_due ON project_jobs(state, next_ms);
```

Finished jobs are kept 7 days (the sweep deletes older `done`/`failed`).

### 6.7 `project_queue` and `project_events` (P1)

```sql
CREATE TABLE IF NOT EXISTS project_queue (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL,
  kind       TEXT    NOT NULL,                  -- start | input
  text       TEXT    NOT NULL DEFAULT '',
  source     TEXT    NOT NULL DEFAULT 'human',  -- human | coordinator | event
  sender     TEXT    NOT NULL DEFAULT '',       -- the person ('' for the coordinator and events)
  hold_park  INTEGER NOT NULL DEFAULT 0,        -- 1: held while the run waits for a person
  dedupe     TEXT    NOT NULL DEFAULT '',
  created    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pqueue_dedupe ON project_queue(project_id, dedupe) WHERE dedupe<>'';
CREATE INDEX IF NOT EXISTS idx_pqueue_order ON project_queue(project_id, id);

CREATE TABLE IF NOT EXISTS project_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL DEFAULT 0,        -- 0: about the project
  kind       TEXT    NOT NULL,                  -- pev* (projects_types.go)
  body       TEXT    NOT NULL DEFAULT '{}',     -- {text, by?, sha?, url?, …}; scm text inside is untrusted and redacted
  wake       INTEGER NOT NULL DEFAULT 0,        -- 1: the coordinator should take a turn for it
  coord_user TEXT    NOT NULL DEFAULT '',       -- whose coordinator gets it: the task's creator; the owner for n=0
  delivered  INTEGER NOT NULL DEFAULT 0,        -- when it reached that coordinator (0: not yet)
  msg_id     INTEGER NOT NULL DEFAULT 0,        -- the message it was delivered in
  dedupe     TEXT    NOT NULL DEFAULT '',
  created    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_pevents_feed ON project_events(project_id, id);
CREATE INDEX IF NOT EXISTS idx_pevents_due ON project_events(coord_user, delivered, wake);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pevents_dedupe ON project_events(project_id, dedupe) WHERE dedupe<>'';
```

Events are kept 30 days.

### 6.8 `project_creds` (K)

```sql
CREATE TABLE IF NOT EXISTS project_creds (
  project_id  INTEGER NOT NULL,
  sandbox_ref TEXT    NOT NULL,
  host        TEXT    NOT NULL,
  identity    TEXT    NOT NULL DEFAULT '',      -- person | bot
  login       TEXT    NOT NULL DEFAULT '',
  for_user    TEXT    NOT NULL DEFAULT '',      -- the person whose token it is ('' for the bot)
  purpose     TEXT    NOT NULL DEFAULT '',      -- the scm token purpose (§9.1)
  expires_ms  INTEGER NOT NULL DEFAULT 0,
  refresh_ms  INTEGER NOT NULL DEFAULT 0,       -- the provider's refreshAfter
  written_ms  INTEGER NOT NULL DEFAULT 0,
  state       TEXT    NOT NULL DEFAULT 'live',  -- live | scrubbed | blocked
  why         TEXT    NOT NULL DEFAULT '',      -- the gate's or the scrub's reason
  PRIMARY KEY (project_id, sandbox_ref, host)
);
```

**Never the token.** Live tokens are in memory only (§9.1).

### 6.9 `ci_watch` (V)

```sql
CREATE TABLE IF NOT EXISTS ci_watch (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  root_run   INTEGER NOT NULL,                  -- the conversation (a task's run, or a coding session's root)
  run_id     INTEGER NOT NULL DEFAULT 0,        -- the run whose push it came from (source pushed: maybe a child coding agent), else root_run
  project_id INTEGER NOT NULL DEFAULT 0,
  n          INTEGER NOT NULL DEFAULT 0,        -- the task, for source 'task'
  source     TEXT    NOT NULL,                  -- task | pushed | manual
  scm        TEXT    NOT NULL,                  -- the provider's tile path
  host       TEXT    NOT NULL DEFAULT '',
  repo       TEXT    NOT NULL,                  -- owner/name
  ref        TEXT    NOT NULL DEFAULT '',       -- the branch
  pr         INTEGER NOT NULL DEFAULT 0,
  sha        TEXT    NOT NULL DEFAULT '',       -- the head it watches
  since      INTEGER NOT NULL DEFAULT 0,        -- when watching began
  state      TEXT    NOT NULL DEFAULT 'none',   -- none | pending | success | failure | gone (ref deleted, PR closed)
  snapshot   TEXT    NOT NULL DEFAULT '',       -- the last GET /scm/checks answer (scmChecks), redacted, ≤ 256 KiB
  etag       TEXT    NOT NULL DEFAULT '',
  sub_key    TEXT    NOT NULL DEFAULT '',       -- its scm subscription key (ci:<id>)
  by_user    TEXT    NOT NULL DEFAULT '',       -- manual: who added it
  carded     TEXT    NOT NULL DEFAULT '',       -- '<sha>:<state>' of the last final outcome (CIWatchView.outcome: the inline card's key)
  error      TEXT    NOT NULL DEFAULT '',
  refusal    TEXT    NOT NULL DEFAULT '',       -- the provider's refusal on the last read (signin → the dock offers a sign-in)
  fetched_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  ended_ms   INTEGER NOT NULL DEFAULT 0         -- stopped watching (kept 7 days, then deleted)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ciw_ref ON ci_watch(root_run, scm, repo, ref) WHERE ended_ms=0;
CREATE INDEX IF NOT EXISTS idx_ciw_match ON ci_watch(scm, repo, ref);
CREATE INDEX IF NOT EXISTS idx_ciw_sha ON ci_watch(scm, repo, sha);
```

At most 10 live watches per conversation (the oldest `pushed` one ends
first). A watch always has a `ref`: one asked for by PR number takes the
PR's head branch (`GET /scm/pulls/{n}`) before it is inserted. Reading CI
needs only read access through the home's scm identity; no token enters a
sandbox for it.

### 6.10 Tables of other WPs

| Table | WP | Columns (all NOT NULL with defaults unless marked) | Keys |
|---|---|---|---|
| `project_refs` | E | `scm TEXT`, `repo TEXT`, `kind TEXT` (branch \| pr \| issue \| sha), `value TEXT`, `project_id INTEGER`, `n INTEGER`, `created INTEGER` | PK (scm, repo, kind, value, project_id, n); index (scm, repo, kind, value) |
| `scm_poll` | E | `project_id INTEGER`, `n INTEGER`, `repo TEXT`, `kind TEXT` (pull \| checks \| comments), `item TEXT` (scmPollItem JSON), `etag TEXT`, `due_ms INTEGER`, `step INTEGER`, `pending_since INTEGER`, `last_ms INTEGER` | PK (project_id, n, repo, kind); index (due_ms) |
| `scm_seen` | E | `key TEXT` (an `eventId`, or a semantic key §11.4), `at INTEGER` | PK (key); pruned after 7 days |
| `project_board` | T (global) | `project_id INTEGER`, `member TEXT`, `member_pid TEXT` (the member's partition id: a `PUT` from another one deletes the member's rows first), `n INTEGER`, `title TEXT`, `col TEXT`, `state TEXT`, `waiting TEXT`, `branch TEXT`, `prs TEXT` (JSON), `ci TEXT` (CISummary JSON), `run INTEGER` (the member's run id — never opened by others), `updated_ms INTEGER`, `stale INTEGER`, `hidden INTEGER` | PK (project_id, member, n) |
| `project_board_out` | T (partition) | `project_id INTEGER` (the membership), `n INTEGER`, `body TEXT` (the row's JSON), `tries INTEGER`, `next_ms INTEGER` | PK (project_id, n) — the latest row wins |
| `project_attach` | C | `session_key TEXT`, `project_id INTEGER`, `user TEXT`, `handoff TEXT`, `last_in INTEGER` | PK (session_key) |

### 6.11 States

- **`project_tasks.ws`:** `pending` → `queued` (waiting for a slot or a
  prerequisite job) → `preparing` → (`signin` →) `ready`, or `failed`;
  later `cleaning` → `cleaned`, or `blocked` (cleanup refused: unpushed or
  uncommitted work).
- **`phase`:** `open` → `pr` → `merged` | `closed` | `done`; `deleted` when
  its conversation is deleted.
- **Derived (never stored):** the board **column** — `queued` (ws pending,
  queued, preparing), `working` (run running, awaiting, or sleeping on its
  own work), `needs-you` (run `waiting_input`, or ws `signin`, `failed`,
  `blocked`), `pr` (phase pr), `done` (merged, closed, done) — and the task
  **state** (`TaskView.State`, the words `task_list` uses, projects_types.go
  `task*` constants).

### 6.12 `Config.Project`

`Config.Project *ProjectRef {id, role: "task"|"coordinator", n}` (B/llm.go,
S0): set when a project run is made and never changed. `childConfig`
(B/links.go:591) copies the pointer to subagents — read it, never write
through it. `handlePutConfig` (B/handlers.go:484) strips it, as it strips
`Engine` and `Harness` (S0). It is derived data: `project_tasks.run_id`
and a coordinator's session key are authoritative, and P1 writes the
field again when a run with origin `project` lacks it (§6.15). **Every
reader of a project run's role goes through `projectRefOf(run)`** (P1,
`project_store.go`): `cfg.Project` when it is there, else — for a run with
origin `project` only — the role derived from those two and written back.
The workspace gate (§8.6), `acquireLLM` (§8.7), `deliverBoundary`'s
coordinator test and the idle-wake checks (§10.5), the coordinator's tool
gate (§10.3) and the run view's keys (§7.4) all use it; none tests the
stored field alone (`classOf`'s clamp, §10.2, reads `cfg.Project` after
the gate has restored it).

### 6.13 Access

- `loadProjectACL(pid)` builds a `rootACL{owner, visibility, teamRole,
  members}` (B/acl.go:47) from `projects` and `project_members`, so
  `level(w)` is reused; cached like `aclCache` and flushed on every change.
  Levels: **owner** — settings, repos, members, delete, cleanup with force;
  **participant** — create, message, act on tasks, warm, sign in;
  **viewer** — read.
- A task's conversation copies the project's ACL at creation and on every
  change: the run owner is the task's creator (V1); visibility, team role
  and members are the project's (written to `runs` and `run_members` for
  every root with `origin='project' AND origin_id=?`, the precedent of
  B/triggers_admin.go:255). Tasks of a membership are private (the
  member's own partition).
- On a project run, `PATCH /runs/{id}` with `visibility`/`teamRole`, `POST
  /runs/{id}/members`, `/publish`, `/copy` into another home, `/hosting`
  and moves answer **409** `projectRunBarred` words: "this conversation is
  a task of project ‹name›: its sharing is the project's, and it stays in
  its project's space" (V9). `GET /runs/{id}/export` stays.

### 6.14 Deletion and moves

- `deleteOneRun` (B/db.go:442) runs `runDeletedHooks` (§14.1) inside its
  transaction: P1 sets `project_tasks.run_id=0, phase='deleted'` and kicks
  the sweep (cleanup queued, §8.8); V ends that run's watches; C drops the
  run's `project_attach`.
- Deleting a project (`DELETE /projects/{pid}?sandbox=keep|delete`) sets
  `state='deleting'`, queues `scrub` then `cleanup` for every task, deletes
  the task conversations (as the owner), unsubscribes, and deletes the
  sandbox if `sandbox_made` and `sandbox=delete`; the rows go when the jobs
  finish.
- Archiving (`PATCH {state: "archived"}`) scrubs credentials, stops the
  pump and the fetches, and keeps everything else.
- Project runs never move home (`isChat` already says so for origin
  `project`, B/homes_move.go:120).

### 6.15 Rollback

What an older agent build (one without Projects) does with what this one
left, and what the next upgrade does about it. Nothing needs cleaning up
by hand.

| Left behind | The older build | On upgrading again |
|---|---|---|
| The tables (§6.1–§6.10) | never reads them; its `migrate()` leaves them alone | used as they are |
| `Config.Project` on a run | keeps it until it rewrites the run's config through its own `Config` — a model pick (B/conversations.go:414), a sandbox bind (`storeBinding`, B/sandbox_bind.go:314), any harness edit — which drops the unknown field (`parseConfig`, B/llm.go) | P1 derives it again from `project_tasks.run_id` (a task) or the session key `proj:<pid>:coord:<user>` (a coordinator) the first time it reads such a run (`projectRefOf(run)`), and writes it back; every reader of the role goes through `projectRefOf` (§6.12) — the gate, the wake check, delivery, the coordinator's tools, the model-call gate — so a coordinator still wakes and a task is still gated before the field is back |
| A task or coordinator run (origin `project`) | leaves it out of the chat list (`chatOrigins`) and keeps it home; reachable by its link, search and `/needs` | listed under its project again |
| A conversation upgraded into a project (P2 flips its origin) | leaves it out of the chat list too (by link and search only) — the one visible regression; the record and API.md say so | task 1 of its project |
| A task parked by the workspace gate (`sleeping` or `waiting_input` with `pendingState.kind = "project"`, its inbox unconsumed) | its engine takes the run up at the next wake and answers the queued input without the gate: the turn runs in whatever workspace is there, without fresh credentials (a push fails; nothing leaks — the gate never wrote a token the older build could see, and it scrubs nothing either); `/needs` shows the unknown park as waiting with its `result` text | the gate parks again if the workspace isn't ready |
| Queued starts and held inputs (`project_queue`) | never delivered | delivered by the pump |
| Credential files in sandboxes | stay until they expire (≤ 1 h bot, ≤ ~7 h person) — the older build neither refreshes nor scrubs them | re-written or scrubbed by the gate |

`TestProjectRefRederivedAfterOldBinaryRewrite` (P1): a task run and a
coordinator whose configs were rewritten without `project` answer their
role through `projectRefOf` at once (the gate parks the task; the model-call
gate counts it as a task) and have the field again after the next read;
C's `TestEventDeliveryAndWakeCoalesced` wakes such a coordinator on an
undelivered event and delivers it.

## 7. Agent: routes

### 7.1 Conventions

- Each WP mounts its routes from its own table, registered in
  **`routeTables`** (§14.1) from `init()`; `routes()` (B/routes.go:139)
  mounts them through the same chain as every route (`agentRole`,
  `hostedRoute`, `guard`, `partitionRoute`). No WP edits `routes.go`.
  **`/adapter/*` routes are never in `routeTables`**: that chain's
  `agentRole` is `RoleFunc("admin")` (B/partition_routes.go:37), and a
  bound provider calls with the `channel` role only. They go in
  **`adapterRouteTables`**, which `adapterRoutes` (B/channels.go) mounts
  with `adapterGuard` (S0 wired it).
- **The bot rule** (V15). Where the home's identity is the bot — a
  partitioned agent's global instance, an unpartitioned agent — every
  route that **names a repo** checks `scmBotAllowed(who, repo)` (§9.1) for
  each repo it names and answers 403 "naming ‹repo› for the bot takes a
  manager, or the agent's scm bot rule" otherwise: `POST /projects`, `POST
  /projects/{pid}/repos`, `POST /runs/{id}/project` (P2's upgrade, §8.10),
  `POST /runs/{id}/ci/watch`. A route or tool that reads through a
  project's identity reads only what the project already names — `GET
  /projects/{pid}/issues`, `POST /projects/{pid}/tasks/batch`, `TaskSpec.Issue`, and the coordinator's `scm_pr`, `scm_issues` and
  `task_create` issues form (§10.3): each `repo` must be one of the
  project's (400, or a tool error, otherwise), at every home — or what the
  caller may name (`GET /projects/scm/repos`: filtered by `scmBotAllowed`).
  In a person's partition the bot rule doesn't apply: the person's own
  identity at the provider decides.
- Project routes name the project **`{pid}`**, never `{id}`: at global
  `hostedRoute` intercepts every pattern with `{id}` (B/hosted_serve.go:90).
  `guard` doesn't resolve `{pid}`: project routes use `needAny`/`needStart`/
  `needUser` at the table and wrap their handler in **`projectNeed(lv, h)`**
  (P1, `project_routes.go`), which loads the project, resolves the caller's
  level on its ACL (404 when below viewer, 403 below `lv`), and puts the
  project in the context. Routes on `/runs/{id}/…` use `guard`'s run levels
  as every run route does; a project task's run level comes from its ACL
  copy (§6.13).
- Partition classes (B/partition_routes.go): every route here is
  `userLocal` (the default) except `POST /projects/{pid}/members`, which is
  `userNoShare` in a person's partition. At global, a person calling from
  their partition (`personFromPartition`) reaches the team routes (§12) as
  themselves.
- Errors are `{error}` with the status (`xbin.WriteError`), plus `refusal`
  where a program needs it (`signin`, `not-installed`, `identity` passed
  through from the provider with its payload; `busy`, `dirty`, `limit`,
  `barred` of the agent's own).
- Bodies and answers are JSON. Shapes in Go are `projects_types.go` and
  `scm_types.go`; their JSON field names are the contract.

### 7.2 The table

Need: **Any** = any caller but cron (the handler filters); **Start** = may
start runs; **User** = a person; **V/P/O** = viewer / participant / owner
of the project (`projectNeed`) or, on `/runs/{id}`, of the conversation
(`guard`).

| WP | Method and path | Need | Body | Answer |
|---|---|---|---|---|
| K | `GET /projects/scm` | Any | — | `{providers: [{scm, title, kind, hosts, caps, identities, you, app, events, notes, error?}]}` — every bound provider's hello as the caller's home sees it (cached 60 s; 10 s after an error) |
| K | `GET /projects/scm/repos?scm=&q=&cursor=` | Any | — | `scmPage[scmRepo]`, through the provider as this home; at a bot home only the repos `scmBotAllowed(who, …)` lets the caller name |
| K | `GET /projects/scm/bot` | Any (a manager) | — | `scmBotRule` `{users, repos}` — 403 for others |
| K | `PUT /projects/scm/bot` | Any (a manager) | `scmBotRule` | `scmBotRule`; 409 in a person's partition (no bot home) |
| K | `GET /projects/scm/signin?scm=` | User | — | `scmSigninState` (`state: none\|pending\|done`) — a person's partition only (elsewhere 409 "sign in to GitHub from your own space") |
| K | `POST /projects/scm/signin` | User | `{scm}` | `scmSigninState` (pending with `signin`, or done) |
| K | `GET /projects/scm/signin/{pollId}?scm=` | User | — | `scmSigninState` |
| K | `DELETE /projects/scm/signin?scm=` | User | — | 204; scrubs every credential of this person's projects first (§9.6) |
| P1 | `GET /projects?state=&kind=&cursor=` | Any | — | `{items: [ProjectView], next}` — those the caller may see, newest activity first |
| P1 | `POST /projects` | Start | `{name, scm, repos: [{repo, slug?, setup?}], sandbox: {ref} \| {new: {provider, image?, size?, egress?}}, policy?, share?: {visibility, teamRole, members: [{user, role}]}, kind?: "team"}` | **201** `{project: ProjectView, jobs: [ProjectJob]}`. 409 `share` in a person's partition; at a partitioned agent's global `kind` must be `"team"` (409 otherwise) and a person there must send `share`; `"team"` elsewhere is 409; 403 a repo the bot rule refuses (§7.1); 409 `class-internal` / 403 a `policy.taskClass` the caller may not use (§10.2); 400 a repo the provider can't see (its refusal passed through) |
| P1 | `GET /projects/{pid}` | V | — | `{project: ProjectView}` |
| P1 | `PATCH /projects/{pid}` | O | `{version, name?, policy?, visibility?, teamRole?, state?: "active"\|"archived"}` | `{project}`; **412** `{error, version}` when `version` is stale; 409 `visibility` in a person's partition; 409 `class-internal` for a `policy.taskClass` with internal reach |
| P1 | `DELETE /projects/{pid}?sandbox=keep\|delete` | O | — | **202** `{state: "deleting"}` |
| P1 | `GET /projects/{pid}/members` | V | — | `{owner, members: [{user, role}]}` |
| P1 | `POST /projects/{pid}/members` | O | `{user, role}` | `{owner, members}` (userNoShare in a person's partition) |
| P1 | `DELETE /projects/{pid}/members/{user}` | V (self) / O | — | 204 |
| P1 | `POST /projects/{pid}/repos` | O | `{repo, slug?, setup?}` | **201** `{repo: ProjectRepo, jobs}`; 403 a repo the bot rule refuses (§7.1) |
| P1 | `PATCH /projects/{pid}/repos/{slug}` | O | `{setup?, checkout?}` | `{repo}` |
| P1 | `DELETE /projects/{pid}/repos/{slug}?force=` | O | — | **202**; 409 `busy` while open tasks use it, unless `force=1` |
| P1 | `GET /projects/{pid}/status` | V | — | `ProjectStatus` (§7.3) |
| P1 | `POST /projects/{pid}/warm` | P | — | **202** `{jobs}`: start the sandbox, fetch, refresh credentials |
| P1 | `GET /projects/{pid}/issues?repo=&q=&state=&labels=&cursor=` | V | — | `scmPage[scmIssue]` (bodies clipped to 2 KiB; untrusted); `repo` must be one of the project's (400 otherwise) |
| P1 | `GET /projects/{pid}/tasks?col=&phase=&q=&mine=&cursor=&limit=` | V | — | `{items: [TaskView], next}` |
| P1 | `POST /projects/{pid}/tasks` | P | `TaskSpec` | **201** `{task: TaskView, run: <runSummary>}`; 429 `limit` past `maxOpenTasks` for the coordinator; 409 `barred` (a coding agent where none may run, §8.1); 409 `class-internal`, 403 a class the caller may not use or whose sandbox managers exclude the project's (§10.2) |
| P1 | `POST /projects/{pid}/tasks/batch` | P | `{issues: [{repo, number}] (≤ 20), size?, agent?, text?}` | **201** `{tasks: [TaskView], errors: [{issue, error}]}`; 400 when any `repo` isn't one of the project's (§7.1) |
| P1 | `GET /projects/{pid}/tasks/{n}` | V | — | `TaskView` |
| P1 | `POST /projects/{pid}/tasks/{n}/cancel` | P | `{reason?}` | `TaskView` |
| P1 | `GET /projects/{pid}/events?since=&limit=` | V | — | `{items: [ProjectEvent], next}` |
| P1 | `GET /runs/{id}/task` | V | — | `TaskView`; 404 when the run is no task |
| P1 | `POST /runs/{id}/task/refresh` | P | — | **202**: credentials, fetch, the PR and checks re-read |
| P1 | `POST /runs/{id}/task/retry` | P | — | **202**: failed workspace jobs queued again |
| P1 | `POST /runs/{id}/task/close` | P | `{cleanup?, closePRs?}` | `TaskView` (phase `closed`) |
| P1 | `POST /runs/{id}/task/cleanup` | O | `{force?}` | **202**; 409 `dirty` `{error, repos: [{slug, dirty, unpushed}]}` without `force` |
| P2 | `POST /runs/{id}/task/pr` | P | `{draft?, title?, body?}` | **202** `{job}`; the PR arrives in `TaskView.prs` |
| P2 | `GET /runs/{id}/project/detect` | O | — | `{sandbox, cwd, candidates: [{path, remote, host, repo, scm, defaultBranch, branch, dirty, ssh, hasCredentials}]}` — `remote` without userinfo (§8.10) |
| P2 | `POST /runs/{id}/project` | O | `{name, scm, repos: [{path, repo}], branch?: "keep"\|"new", switchHttps?: [path], policy?}` | **201** `{project, task}` (the conversation is task 1); 409 at a partitioned agent's global (it holds team definitions only, which have no tasks); 403 a repo the bot rule refuses at a bot home (§7.1); 409 `class-internal` when the conversation's class or `policy.taskClass` has internal reach, 403 one the caller may not use (§10.2) |
| P2 | `POST /projects/{pid}/fork-base` | O | `{now?: true}` | **202** `{job}`; `now` stops the sandbox to snapshot it (the UI confirms first) |
| C | `POST /projects/{pid}/coordinator` | P | `{text?}` | `{run: <runSummary>}` — the caller's coordinator, made on first use; `text` queued to it |
| C | `GET /projects/{pid}/needs` | V | — | `{items}` — `/needs` items of the project's runs (each with `project: {id, name, n}`) |
| E | `POST /adapter/scm/event` (in `adapterRouteTables`, not `routeTables`) | `adapterGuard` (the `channel` role) + the caller is a bound scm provider (`scmBound()` holds `X-XBin-From`), else 403 | event v1 (§4.10) | 200 `{taken: true}` (or a duplicate); 404 no such subscriber here |
| T | `GET /projects/{pid}/board?cursor=` | V (global) | — | `{items: [BoardRow], next}` |
| T | `PUT /projects/{pid}/board/{n}` | P (global), `personFromPartition` only | `BoardRow` | 200; 403 not from the member's own partition; 404 not a member (the partition archives its membership, §12.5) |
| T | `POST /projects/{pid}/board/{member}/{n}/hide` | O (global) | — | 204 |
| T | `POST /projects/{pid}/seed` | O (global, team) | `{sandbox: {ref} \| {new: {…}}}` | **202** `{jobs}` |
| T | `GET /memberships` | User (a person's partition) | — | `{items: [ProjectView]}` — this person's memberships |
| T | `POST /memberships` | User (a person's partition) | `{team: <global pid>, sandbox?: {ref} \| {new: {…}}, accept: <hash>}` | **201** `{project}` (or 200, the existing one); 409 `{error, defHash, definition}` when `accept` isn't the definition's current security hash (the member is shown it first, §12.2) |
| T | `GET /memberships/{pid}/pending` | User (the member) | — | `{hash, accepted, pending}` — the security part (§12.2) as the member accepted it and as the definition has it now (`pending` null, `hash` "", when nothing waits) |
| T | `POST /memberships/{pid}/accept` | User (the member) | `{hash}` | `{project}` — the pending definition becomes the running one; 409 when `hash` isn't the pending one's |
| V | `GET /runs/{id}/ci?fresh=` | V | — | `CIView` (§7.3) of the conversation's root |
| V | `GET /runs/{id}/ci/jobs/{job}/log?watch=&tail=&since=&until=` | V | — | `{text, bytes, from, complete, truncated, url}` — ANSI stripped, redacted (§9.7); `tail` ≤ 262144 (default 65536); `job` must be in the watch's stored snapshot (404 otherwise); 409 `{error, refusal: "in-progress", url}` |
| V | `GET /runs/{id}/ci/checks/{check}/annotations?watch=&cursor=` | V | — | `{items: [scmAnnotation], next}`, redacted; `check` must be in the watch's snapshot (404 otherwise) |
| V | `POST /runs/{id}/ci/watch` | P | `{scm?, repo, ref?, pr?}` | **201** `CIWatchView`; 400 neither `ref` nor `pr`; 409 `limit` past 10; 403 a repo the bot rule refuses (§7.1) |
| V | `DELETE /runs/{id}/ci/watch/{wid}` | P | — | 204 |
| V | `POST /runs/{id}/ci/rerun` | P, and a person (not view-as), in their own partition | `{watch, runId, failedOnly}` | **202** `{runId, attempt}`; `runId` must be a workflow run in the watch's snapshot (404 otherwise); 403 `identity` where the home has no person identity (the global instance, an unpartitioned agent); 501 `unsupported` without `checks.rerun` |

### 7.3 Answer shapes

```json
// ProjectView — GET /projects/{pid}
{"id": 1099511627777, "uid": "k3x9qa", "name": "Web", "slug": "web", "kind": "personal", "teamRef": 0,
 "owner": "alice", "visibility": "private", "teamRole": "viewer", "level": "owner",
 "scm": "apps/scm-github", "host": "github.com", "sandboxRef": "apps/coding-sandbox|sb-7f3a", "sandboxMade": true,
 "dir": "/work/web", "policy": {…every key, defaults filled…}, "state": "active", "version": 3,
 "repos": [ProjectRepo…], "counts": {"queued": 1, "working": 2, "needs-you": 0, "pr": 1, "done": 7},
 "slots": {"used": 2, "max": 3}, "createdBy": "alice", "createdMs": …, "updatedMs": …}

// ProjectStatus — GET /projects/{pid}/status
{"sandbox": {"ref", "name", "state", "workdir", "shared": false},
 "repos": [{"slug", "state", "fetchedMs", "head", "protected", "error"}],
 "creds": [{"sandbox", "host", "identity", "login", "state", "expiresMs", "why"}],
 "jobs": [ProjectJob…], "slots": {"used", "max"},
 "warnings": [{"kind": "unprotected", "repo": "acme/web", "text": "…"}]}

// CIView — GET /runs/{id}/ci
{"root": 12, "summary": CISummary, "live": true, "canRerun": true, "canWatch": true,
 "watches": [{"id": 4, "source": "task", "scm": "apps/scm-github", "host": "github.com", "repo": "acme/web",
              "ref": "xbin/k3x9/3-fix-login", "pr": 42, "sha": "9fceb02…", "state": "pending",
              "since": …, "updatedMs": …, "fetchedMs": …, "error": "",
              "urls": {"pr": "https://github.com/acme/web/pull/42", "commit": "…", "checks": "…/pull/42/checks"},
              "checks": <scmChecks, redacted>}]}

// CISummary — CIView.summary, the ci stream event's, TaskView.ci, a board row's ci
{"state": "pending", "jobs": {"total": 5, "done": 3, "failed": 0, "running": 1, "queued": 1},
 "current": "test (ubuntu) › go test ./...", "startedAt": …, "updatedAt": …, "url": "…/actions/runs/7001"}
```

`live` is the provider's `events.healthy`. `canRerun`: the caller is a
person with participant level, in their own partition, and the provider
lists `checks.rerun`. `canWatch`: a sandbox is bound (or the run is a
task), an scm provider is bound, and the caller is a participant. Each
watch carries its `refusal` and `outcome` (§6.9).

### 7.4 Stream events

- **`project`** (P1): `{"type": "project", "run": 0, "root": 0, "data":
  {"id": <pid>, "change": "project"|"task"|"repo"|"job"|"event"|"board"|"deleted", "n"?: <task>}}`
  — sent with `hub.publishTo` (B/events.go:160) to list subscribers (no
  open run) who may see the project, and to the task's own conversation
  stream for `task`. Not replayed: a client re-reads what changed. Changes
  are coalesced per (project, change, n) for 250 ms.
- **`ci`** (V): `{"type": "ci", "run": <root>, "root": <root>, "data":
  {"root", "watch", "summary": CISummary, "state", "outcome"}}` — to the
  root's viewers, coalesced per root (key `ci:<root>`, like drafts): a
  client that falls behind sees only the latest. Nothing in it is a
  one-shot cue: the inline cards come from each watch's `outcome`, which
  the next event and `GET /runs/{id}/ci` repeat.
- The run view (`viewWith`, B/stream.go:156) and `GET /runs/{id}` gain
  keys through **`runViewHooks`** (§14.1; P1 places the two calls):
  **`project`** `{id, name, n, role}` on a project run and
  **`projectTask`** (`TaskView`, with `ProjectPark.signin` only for the
  person who must sign in) on a task — not `task`, which is the pinned task
  (D133) — from P1; **`ci`** `{summary: CISummary|null, canWatch}` from V,
  so the chip is right the moment a conversation opens.

### 7.5 `API.md` "## Projects" outline

S0 writes the skeleton (one `###` per area, each saying what it covers and
"Described when it lands." — no plans vocabulary in a served doc); each WP
fills **only its own subsection** with the routes and shapes above, in
API.md's style (bold lead-ins, code blocks, no `plans/` links), replacing
that sentence:

| Subsection | WP |
|---|---|
| `### Projects and tasks` — the model, homes, policy keys, states, the board columns, refusals; rolling back to a build without Projects (§6.15) | P1 |
| `### The workspace` — layout, jobs, the gate, ports, setup | P1 |
| `### scm providers and credentials` — the slot, sign-in, the gate, files, redaction | K |
| `### Big tasks, upgrades and pull requests` | P2 |
| `### The coordinator` — tools, authority, limits, pushes, channel attach | C |
| `### scm events and polling` | E |
| `### Team projects` | T |
| `### CI in the conversation` — watches, the aggregate, logs, rerun, the UI | V |
| `### Projects in the UI` — web and native | U1 (U2 adds native) |

### 7.6 CI watches (V)

**What is watched.**

- **Project tasks** (`source: task`): the task's branch and each PR head,
  per repo — made by V's `projectRefsHooks` entry when P1's `refs` job sees
  the branch on the remote or a PR opens (§8.3), and lazily by `GET
  /runs/{id}/ci` on a task.
- **Coding sessions outside projects** (`source: pushed`, V13): V's
  `turnEndHooks` entry, for **any run's** turn end — a root conversation,
  or a coding agent child under it (D147), whose turn the hook sees itself
  (§14.1: the hooks run at every turn end, not only top-level ones) — with
  a sandbox binding (the root's `cfg.Sandbox`; a child's `cfg.Harness.Ref`
  and `Cwd`), when `scmBound()` is not empty. Skipped: runs with origin
  `project` (P1's `refs` job covers them, so a branch never gets two
  watches) and hosted runs (never reach a hook). `AfterCommit`, off the
  engine's path: one `Run` (10 s) in **that run's** sandbox, cwd that run's
  binding, env `SINCE` = **that run's** turn start (unix seconds):

  ```sh
  set -eu
  for top in $( { git rev-parse --show-toplevel 2>/dev/null || find . -maxdepth 3 -name .git -prune -print | sed 's#/\.git$##'; } | sort -u); do
    git -C "$top" for-each-ref --format='%(refname) %(objectname)' refs/remotes |
    while read -r ref sha; do
      case "$ref" in */HEAD) continue ;; esac
      line=$(git -C "$top" reflog show --date=unix --format='%gd %gs' -n 1 "$ref" 2>/dev/null || true)
      case "$line" in *"update by push"*) ;; *) continue ;; esac
      at=$(printf '%s' "$line" | sed -n 's/.*@{\([0-9]*\)}.*/\1/p')
      [ "${at:-0}" -ge "$SINCE" ] || continue
      rest=${ref#refs/remotes/}; remote=${rest%%/*}; br=${rest#*/}
      printf '%s\t%s\t%s\t%s\n' "$top" "$br" "$sha" "$(git -C "$top" remote get-url "$remote")"
    done
  done
  ```

  (Checked by hand with git 2.53.0 against a local bare origin: a `git
  push origin feature` — with or without `-u` — writes `update by push` to
  `refs/remotes/origin/feature`'s reflog in a non-bare clone, and an older
  push is ignored; §8.3 turns `core.logAllRefUpdates` on in base repos so
  worktrees of a bare base behave the same. Paths with spaces and remote
  names with `/` are skipped.) Each line — toplevel, remote branch, sha,
  remote URL — is parsed in memory; the URL's userinfo is dropped
  (`url.Parse`, `u.User = nil`) before anything is kept, answered or
  logged (an `https://user:token@host/…` remote is common), and the Run's
  output is never stored. A line whose URL's host is in a bound provider's
  `hosts` becomes (or refreshes) a watch: `repo` from the URL's path
  (`.git` trimmed), `ref` the remote branch, `sha`, `run_id` the run that
  pushed, `root_run` its root. At a bot home (the global instance — its
  built-in conversations may push from a sandbox, though no coding agent
  runs there — or an unpartitioned agent) the watch is made only for a
  repo a project of that home names whose ACL makes the conversation's
  owner a participant, or one `scmBotAllowed(owner, repo)` allows (§7.1) —
  and in a conversation others share (team visibility, members) only for a
  repo of such a project: a faked remote URL and reflog can't make the bot
  read a repo nobody authorised. A PR for it is looked up once (`GET
  /scm/pulls?head=&state=open`). Then a subscription (key `ci:<watch id>`,
  kinds `[checks, pull, workflow, job, check, push]`, branches `[ref]`).
- **By hand** (`source: manual`): `POST /runs/{id}/ci/watch` (the bot
  rule at a bot home; a PR number resolved to its head branch first).

**Keeping it current.**

- Events: V's `scmEventHooks` entry matches `(scm, repo, ref)` or `(scm,
  repo, sha)`: a progress event updates the stored snapshot in place (a
  `workflow`, `job` or `check` entry replaced by id) and emits `ci`; a
  `checks.completed` or `pull` re-reads `GET /scm/checks` (conditional,
  with the stored `etag`); a `push` moves `sha` to the new head (the
  snapshot starts over); a merged or closed PR, or a deleted branch, sets
  `gone` and ends the watch after 24 h.
- **Step progress comes from reads.** GitHub's `workflow_job` webhook
  fires on queued, in progress and completed — not per step — so a job's
  `steps` and `CISummary.current` ("job › step") are as fresh as the last
  `GET /scm/checks`; a summary drops `current` once it is older than the
  snapshot it came from.
- Reads: `GET /runs/{id}/ci?fresh=1` re-reads a watch whose snapshot is
  older than 10 s (webhooks unhealthy) or 30 s (healthy) and still has
  anything not completed — at most one upstream read per watch at a time,
  coalesced across viewers. The dock asks with `fresh=1` every 15 s while
  it is open and anything is pending (V11); opening a conversation reads
  once. **In the background** (V's `ownerLoops` entry, §14.1: in the engine
  owner, never holding the engine up or waking it), a
  live watch with anything pending and no event for its sha in the last
  2 min is re-read every 60 s for the first 20 min after its push, then at
  §11.5's cadence (10 min until 2 h, 30 min until 24 h), then left until
  someone looks — so the chip, a child card's glyph and the outcome card
  move with the dock closed and webhooks missing. A task's watch is read
  by V; E's polling (§11.5) acts on the outcome for the task.
- Each outcome (`<sha>:<state>` with state success or failure) is kept on
  the watch (`carded`, shown as `outcome`): the inline card's key. A
  client draws a card for every watch whose `outcome` it hasn't
  dismissed — from `GET /runs/{id}/ci` or any later `ci` event — so a
  coalesced event never loses one.
- Identity: reads as the home's default (§9.1); a manual watch of a repo
  the identity can't see answers the provider's 404. A refusal on a read
  is kept on the watch (`error`, `refusal`): `signin` makes the dock offer
  "Sign in to ‹provider›" (K's `/projects/scm/signin`), then retry.
- Ids: a job, check or workflow run named by a route (the log,
  annotations, a rerun) must be one in the watch's stored snapshot — 404
  otherwise; the agent never reads or acts on an id a caller made up.
- Text: snapshot titles, summaries, step names, annotation messages and
  log text are redacted (`scmRedact`, §9.7) before they are stored or
  served, and served as plain text the UI never renders as markdown or
  HTML.
- Rerun: `POST /runs/{id}/ci/rerun` — a person (`who.kind == whoUser`, not
  view-as) with participant level, **in their own partition, as
  themselves** (`as: person`); 403 `identity` at the global instance or in
  an unpartitioned agent (no person identity; never the bot, V12); logged
  in the conversation's journal as a note ("‹who› re-ran the failed jobs
  of ‹run›").
- `TaskView.ci` (`taskCISummary`, §14.2) and the board row's `ci` are the
  task's watches' `CISummary`. V tells the board when it changes:
  `projectTaskChanged(t, pid, n, "ci")` (§14.1), which P1 fills (the
  `project` event and `taskChangedHooks`). The task's PR chip shows
  `TaskPR.checks` (E keeps it) only while `TaskView.ci` is null; once V
  has a summary the CI chip is the one source.

## 8. Agent: the workspace pipeline (P1; P2 where marked)

### 8.1 Layout

`W` = the sandbox's `workdir`, `H` = its `home` (from the manager's sandbox
answer — never a hardcoded `/work`: the fake manager's sandboxes are host
directories, B/fsb_fake_test.go). `P = W/<project slug>`.

| Path | What |
|---|---|
| `P/.repos/<repo slug>.git` | a bare base repo (mode `bare`) |
| `P/tasks/<n>-<task slug>/<repo slug>` | a task's checkout of one repo |
| `P/tasks/<n>-<task slug>/.task-env` | `BRANCH=`, `TASK_PORT_BASE=`, `TASK_PORT_SPAN=`, `PORT=`, `GH_CONFIG_DIR=` lines (for people's terminals: `. .task-env`) |
| `P/.xbin/setup-<repo slug>.sh` | the repo's setup script (written with the files API, 0755) |
| `P/.xbin/env` | `GH_CONFIG_DIR=…` for people's terminals |
| `H/.config/xbin-scm/<project uid>/` | credentials (0700; §9.3) — never under `W`, never in a worktree |

A task's **cwd** (the coding agent's, the built-in agent's `bash`) is the
checkout for a one-repo task, else the task directory — so the repo's
`AGENTS.md`/`CLAUDE.md` is at its root.

**Engine.** A task's engine is fixed at creation: a coding agent where one
may run (`!harnessBarred(run)` — a person's own partition, including a team
membership, or an unpartitioned agent; the class allows it; an image of the
sandbox's manager offers it), else the built-in agent. `TaskSpec.Agent`
names the coding agent; `policy.engine` `auto` picks the person's last
coding agent (`prefs/agent`) when one is available, else the built-in.

### 8.2 Names and ports

- Branch: `<branchPrefix>/<n>-<task slug>`; `branchPrefix` defaults to
  `xbin/<uid>` (V4). Task slug: the title lowercased, `[^a-z0-9]+` → `-`,
  trimmed, ≤ 32. A branch that exists on the remote and isn't this task's
  gets `-2`, `-3`…
- Ports: `ports_base = policy.ports.base + ((n−1) mod policy.ports.slots) ×
  policy.ports.span`; the task may use `[base, base+span)`; `PORT=base`.

### 8.3 Job kinds

Every job looks before it acts (re-running a finished step is a no-op).
Shell steps are one `Run` (or a background `ExecStart` with a clientId, for
long ones) whose **values are passed in `env`**, never spliced into the
script: a repo name, branch or URL is data. Every script starts `set -eu`.
`$B` the base repo, `$C` the checkout, `$U` the clone URL, `$DEF` the
default branch, `$PFX` the branch prefix, `$BR` the branch.

| Kind | WP | Does |
|---|---|---|
| `sandbox` | P1 | Find or create the workspace sandbox: an existing `{ref}` gets the label `xbin.agent/project=<uid>` patched on (for display and lookup only — a label proves nothing, and no gate reads it, §9.2); `{new}` creates with clientId `agent:proj:<pid>:sbx:<k>` (k counts attempts, as `sandbox_creates`), labels `xbin.agent/project=<uid>` plus `withHomeLabel` (B/sandbox_partition.go:92), visibility private (or the project's, for a team seed); start it; `mkdir -p "$P/.repos" "$P/tasks" "$P/.xbin"`; record `dir`. |
| `creds` | K | §9 — runs before `repo` for every repo of a private host and before each task's `prepare`; idempotent. |
| `repo` | P1 | Background exec, clientId `agent:proj:<pid>:repo:<slug>:<attempt>`, timeout 30 min: the script below. Learns `$DEF` from the provider (`GET /scm/repo`), else `git ls-remote --symref "$U" HEAD`. Then `protected` from the provider; `policy.protection` `refuse` with an unprotected default branch fails the job with the reason (V6). |
| `fetch` | P1 | `git -C "$B" fetch -q --prune --no-tags origin` (Run, 120 s); records `fetched_ms`, `head`. Every `fetchEveryMin` while the project has a task working or a page open on it (the project's `status` was read in the last 5 min); before `prepare` when older than 2 min. Never wakes a stopped partition just to fetch. |
| `prepare` | P1 | One Run for every repo of the task: the script below; then `.task-env` and the checkout rows. A big task runs `fork` first (P2). |
| `setup` | P1 | When the repo has a setup script — in a membership, the script as its member accepted it (§12.2), never a newer one: write `P/.xbin/setup-<slug>.sh` (files API, 0755), background exec with cwd `$C`, env §8.5, clientId `agent:proj:<pid>:setup:<n>:<slug>`, timeout `setupTimeoutSec`. Keeps the redacted tail (8 KiB). `setupBlocking` (default true): `bind` waits for it; a failure **doesn't block** the task — the tail goes into the first prompt (V3). |
| `bind` | P1 | `prepareBinding(creator, cfg, {Ref: task sandbox, Cwd})` (B/sandbox_bind.go:234), then `storeBinding(t, run, …)` (:314) — and for a coding agent `cfg.Harness.Ref`/`Cwd` (already set at creation; checked equal). `ws=ready`, emit, queue an `inboxWake`. |
| `snapshot` | P2 | §8.9 |
| `fork` | P2 | §8.9 |
| `pr` | P2 | `git -C "$C" push` (the helper supplies the token) for every repo with commits ahead of `origin/$DEF`; then `POST /scm/pulls {repo, head: $BR, base: $DEF, title, body, draft, clientId: "agent:proj:<pid>:pr:<n>:<slug>"}`; records `TaskPR`; `phase=pr`; runs `projectRefsHooks`. |
| `refs` | P1 | **What the task pushed itself.** Queued by P1's `turnEndHooks` entry at the end of every turn of a task (`AfterCommit`; deduped by the live-job index), because the brief tells the agent to push and `gh pr create` works in the sandbox — nothing else would notice. One Run per task over its checkouts: `git -C "$C" rev-parse -q --verify "refs/remotes/origin/$BR"` (the push updated it — worktrees share the base's refs). A sha other than `project_checkouts.remote_sha`: store it. Then, when the sha moved **or** the branch is on the remote and `prs` holds no open PR for that repo (the agent pushed in one turn and ran `gh pr create` in a later one): `GET /scm/pulls?repo=&head=$BR&state=open`, merged into `project_tasks.prs` (`phase=pr` once one is open). When either changed, `projectRefsHooks` (E's subscription and routing, V's task watches) run in the transaction that recorded it. Nothing changed: nothing. Also queued through `projectRefsCheck(t, pid, n)` (§14.1) by E for a `pull.opened`/`reopened` on the task's branch (§11.3), so a PR opened outside the task's turns is recorded too. |
| `poll` | E | §11.5 |
| `subscribe` | E | §11.2 |
| `cleanup` | P1 (P2 adds the fork) | §8.8 |
| `scrub` | K | §9.6 |

**`repo` script** (bare mode):

```sh
set -eu
[ -d "$B" ] || git init -q --bare "$B"
git -C "$B" remote get-url origin >/dev/null 2>&1 || git -C "$B" remote add origin "$U"
git -C "$B" remote set-url origin "$U"
git -C "$B" config --unset-all remote.origin.fetch || true
git -C "$B" config --add remote.origin.fetch "+refs/heads/$DEF:refs/remotes/origin/$DEF"
git -C "$B" config --add remote.origin.fetch "+refs/heads/$PFX/*:refs/remotes/origin/$PFX/*"
git -C "$B" config push.default current
git -C "$B" config push.autoSetupRemote true
git -C "$B" config core.logAllRefUpdates true
# the credential lines (§9.4), given as GIT_CFG_<i>_K / GIT_CFG_<i>_V pairs, applied in a loop
git -C "$B" fetch -q --prune --no-tags origin
git -C "$B" rev-parse "refs/remotes/origin/$DEF"
```

**`prepare` script** (per repo; worktree mode):

```sh
set -eu
git -C "$B" worktree prune
if [ ! -e "$C/.git" ]; then
  mkdir -p "$(dirname "$C")"
  if git -C "$B" show-ref -q --verify "refs/heads/$BR"; then
    git -C "$B" worktree add -q "$C" "$BR"
  elif git -C "$B" show-ref -q --verify "refs/remotes/origin/$BR"; then
    git -C "$B" worktree add -q --track -b "$BR" "$C" "origin/$BR"
  else
    git -C "$B" worktree add -q --no-track -b "$BR" "$C" "origin/$DEF"
  fi
fi
git -C "$C" rev-parse HEAD
```

Clone mode (`checkout: clone`): `git init -q "$C"`, an alternates file
`$C/.git/objects/info/alternates` holding `$B/objects`, `git -C "$C" fetch
-q "$B" "+refs/remotes/origin/*:refs/remotes/origin/*"`, `git -C "$C"
checkout -q -b "$BR" "origin/$DEF"` (or the existing remote branch),
`git -C "$C" remote add origin "$U"`, the credential lines, and `git -C
"$B" config gc.auto 0` while any clone exists (a gc in the base would
drop objects a clone borrows). An adopted repo's task 1 uses the clone
itself (mode `main`).

### 8.4 The worker, idempotency and fencing

- `project_worker.go` runs in the **engine owner** only: started at
  takeover beside `sweepSigninExecs` (B/engine.go:172), stopped by
  `BeginShutdown` (:432). Beside it P1 starts every `ownerLoops` entry
  (§14.1) with a context `BeginShutdown` cancels — V's CI refresher
  (§7.6); they count in neither `hasWork` nor the hold. `updateHoldLocked` (B/owner.go:96) gains `||
  e.projectsHoldLocked()`; `hasWork` (B/engine.go:484) counts queued and
  running `project_jobs` — one-line edits each.
- Claiming a job writes the engine **epoch** under `e.fenced`; at takeover,
  `running` jobs of an older epoch go back to `queued` (an exec they started
  is found again by its clientId — the manager answers the same exec — so
  nothing runs twice).
- One job at a time per base repo and per sandbox for git steps (an
  in-process lock keyed `ref|path`); git's own `index.lock`/`*.lock` errors
  retry with backoff (2 s, 4 s, … 60 s, 6 tries); stale locks are **never**
  removed automatically.
- Failures: `attempts` + 1, `next_ms` backoff (10 s doubling to 10 min), up
  to 5 attempts, then `failed` with the error, the task `ws=failed` and an
  event `workspace` (wake).
- The kinds' runners live in `projectJobKinds` (projects_seams.go); each WP
  registers its kinds in `init()`. A job of a kind nobody registered fails
  "not in this build".

### 8.5 Env

Every exec in a task's workspace — the setup, the jobs, a bash job of the
task (`jobExecEnv`, B/sandbox_jobs.go:64), the coding agent (after
`credEnv`, B/harness_engine.go:545) — gets `projectEnv(run)` (P1), which
merges in `scmProjectEnv(p, home)` (K's hook) itself. P1 owns both call
lines (one in `spawnHarness` after `credEnv`, one in `jobExecEnv`); K
edits neither file, so `GH_CONFIG_DIR` is set once:

| Var | Value |
|---|---|
| `TASK_DIR` | the task directory |
| `REPO`, `REPO_DIR` | setup only: the repo's `owner/name` and checkout |
| `BRANCH` | the task's branch |
| `TASK_PORT_BASE`, `TASK_PORT_SPAN`, `PORT` | §8.2 |
| `GH_CONFIG_DIR` | `H/.config/xbin-scm/<uid>/gh` (K) |

### 8.6 The gate

In `Engine.pass` (B/actor.go:106), after `rows :=
e.db.undelivered(run.ID)` (:124) and before the harness fork (:126), one
line:

```go
if run.ParentID == 0 && run.Origin == originProject && !e.projectGate(run, rows) { return }
```

- Cancel and interrupt rows always pass.
- The gate reads the database only — never the network: it never calls
  `scmEnsureCreds`, and it never reads K's `project_creds` itself (K's
  table, built in parallel): when `scmCredsDue(t, p, k)` (§14.1, K fills
  it: a credential missing or due within 10 min) says so, it queues a
  `creds` job and parks as `preparing`; the job's `bind`-like finish (an
  `inboxWake`) lets the turn run. Before K is in, the hook says no.
- A run with origin `project` whose config lacks `Config.Project` (an
  older build rewrote it, §6.15) is given it again here first
  (`projectRefOf`, §6.12).
- `ws` `pending`/`queued`/`preparing`: park once with status `sleeping`
  and `pendingState {kind: "project", project: ProjectPark}`, leaving the
  inbox unconsumed (it counts as work: the worker's jobs hold the engine).
- `ws` `signin`/`failed`/`blocked`: status `waiting_input` with the same
  `pendingState`; `handleNeeds` (B/conversations.go:471) gains `case
  "project"` (reason `project`, "‹task› needs you: sign in to GitHub" /
  "its workspace failed"). The sign-in's device code is
  `scmPendingSignin(owner, scm)` (§14.1: K keeps the last 409 `signin` the
  provider answered for that person), put into the view only for that
  person (`runViewHooks`, §9.8).
- `bind` finishing queues an `inboxWake`, which lets the parked turn run.
- A coordinator (role `coordinator`) is never gated.

### 8.7 The queue and the pump

- `maxTasks` (policy, default 3, 1–16) bounds the tasks **holding a
  slot**: their run is `running`, `awaiting`, `sleeping` or
  `waiting_input` (V5); an idle coding agent's adapter holds none.
- Task starts and the coordinator's messages go through `project_queue`;
  `projectPump(pid)` (in one transaction) counts holders and moves queued
  items, oldest first, into their runs' inboxes while `holders <
  maxTasks`. It runs after a turn ends (`turnEndHooks`), whenever a task
  run's status changes (`runStatusHooks` — a park, a cancel of a coding
  agent, a wake: §1 lists the writes that bypass the turn ends), a
  create, a queue insert and a policy change; never while the tile is
  halted.
- An item with `hold_park=1` waits while its run is `waiting_input` (the
  coordinator never answers a park, §10.4); the pump releases it once the
  run leaves that state — seen through `runStatusHooks`.
- A person's direct message (`POST /runs/{id}/message`) **bypasses** the
  queue but counts as a holder.
- Built-in task runs take the subagent class at the model-call gate:
  `acquireLLM(ctx, run.Depth == 0 && !projectRefOf(run).isTask())` at
  B/actor.go:631 and B/compact.go:372 (one-line edits). The project page
  warns when the engine is built-in and `maxTasks` exceeds the person's
  subagent slots.

### 8.8 Cleanup

- Queued on close (`cleanup: true`), on merge or close per
  `policy.cleanup`, on a deleted conversation (the sweep), and by `POST
  /runs/{id}/task/cleanup`.
- **Safety** (each repo, one Run): `git -C "$C" status --porcelain` empty
  and `git -C "$C" log --oneline "@{u}..HEAD"` empty (or, without an
  upstream, `git -C "$C" log --oneline "origin/$DEF..HEAD"` empty); a PR
  that is merged counts as pushed. Otherwise `ws=blocked` with `{repos:
  [{slug, dirty, unpushed}]}` — unless `force`, which only the run's owner
  may send.
- Then `git -C "$B" worktree remove --force "$C"` and `git -C "$B" branch
  -D "$BR"` (clone mode: `rm -rf -- "$C"`); a `main` checkout is never
  removed. Then `rm -rf -- "$TASK_DIR"` if empty of checkouts. P2 adds:
  delete the fork (credentials scrubbed first) unless `bigTasks.keepFork`.
- `ws=cleaned`, `cleaned_ms`; the conversation stays.

### 8.9 Big tasks (P2)

1. **Fork-base snapshot** (job `snapshot`, project level): only while the
   primary is **quiet** — no task run of the project running, no running
   sandbox job for its ref (`sandbox_jobs`), no busy coding agent on it —
   because a snapshot stops the sandbox (docs/sandbox-manager.md
   §Snapshots, clones and archives). After the first warm-up and again when
   older than 24 h. New client methods in `sandbox_client.go`:
   `Snapshot(ctx, id, name, clientID)`, `Snapshots(ctx, id)`,
   `DeleteSnapshot(ctx, id, sid)`; clientId `agent:proj:<pid>:snap:<yyyymmdd>`.
   Needs hello caps `snapshots` and `clone`.
2. **Fork** (`provisionFork`, §14): `Create {name: <project>-<n>, from:
   {sandbox: primary, snapshot: fork_snap}, labels: project, task and home,
   visibility/members: the primary's, clientId: agent:proj:<pid>:fork:<n>}`,
   wait until running; then, before `prepare`, one Run: `git -C "$B"
   worktree prune` for every base, `rm -rf -- "$P/tasks"/*`, `git -C "$B"
   worktree repair`; then fresh credentials (`creds`, the gate checking the
   fork and that its source was never used by a hosted conversation);
   then `fetch`.
3. **Fallbacks:** no `clone` cap or no snapshot yet → a fresh sandbox from
   the primary's image plus the `repo` jobs. `POST /projects/{pid}/fork-base
   {now: true}` (stops the primary) only after the person confirms.

### 8.10 Upgrade (P2)

1. `GET /runs/{id}/project/detect` (owner of a root chat/api conversation
   with a sandbox bound, not hosted): one Run: `git -C "$CWD" rev-parse
   --show-toplevel 2>/dev/null || find "$CWD" -maxdepth 3 -name .git -prune
   -print`; per toplevel: `git remote get-url origin`, `git symbolic-ref
   -q --short refs/remotes/origin/HEAD`, `git branch --show-current`, `git
   status --porcelain | wc -l`. The host matched to a bound provider's
   `hosts` names the `scm`; an `ssh` remote is marked. A remote URL's
   userinfo is dropped (`url.Parse`, `u.User = nil`) before it is
   answered, stored or logged, and the candidate says `hasCredentials:
   true` so the dialog offers `switchHttps` (the project's helper replaces
   it).
2. `POST /runs/{id}/project`: refused at a partitioned agent's global
   (409: it holds team definitions only), for a repo `scmBotAllowed`
   refuses at a bot home (403, §7.1), and for a conversation whose class —
   or the `policy.taskClass` sent — has internal reach (409
   `class-internal`) or isn't `usableBy` the caller (403), as a task's
   class is checked (§10.2): the conversation becomes a task, and a task
   never has internal reach (V18). Then it creates the project in the
   conversation's home, labels the sandbox, records the repos `mode=adopted` with
   `base_path` the toplevel, and makes the conversation **task 1**: its
   `origin` becomes `project`, `origin_id` the project, `Config.Project`
   set, checkouts `mode=main`, `ws=ready`. `branch: "new"` runs `git switch
   -c "$BR"`; `switchHttps` paths get `git remote set-url origin <https>`.
   Then `creds`.
3. Cleanup never removes a `main` checkout.

### 8.11 Policy prompt

- Built-in tasks: `projectPrompt(run, cfg)` (P1) adds a `# Project` section
  after `sandboxPrompt` (B/context.go:39): project name, task number,
  title, size, branch, paths and cwd, "each repo's AGENTS.md / CLAUDE.md
  governs — read it first", `policy.instructions`, `policy.checks`,
  `policy.prConventions`, "push your branch, never the default branch; never
  merge", the ports, the setup outcome. Built from the database only and
  stable across turns (the prompt cache stays warm). For a coordinator it
  is `projectCoordPrompt` (C).
- Coding agents: there is no system prompt (B/harness_pass.go), so the
  same brief starts the first `hprompt`, the task text last.
- Issue text and setup output are clipped (8 KiB) and framed `[untrusted —
  from <host>: …]`. Every task takes scm text — issues, CI logs, forwarded
  reviews — so a task's class never has internal reach, whatever the
  repo's visibility (V18, §10.2).

## 9. Agent: credentials (K)

### 9.1 The client

- `xbin.json` gains the slot `"scm": {"kind": "http", "service": "scm",
  "multi": true}` (commented like `sandboxes`). `scm.go` reads
  `XBIN_IFACE_SCM` (each endpoint's tile path is the provider's name,
  `Project.SCM`) and implements `scmAPI` (scm_types.go) over HTTP, as
  `sandboxManagers`/`managerHello` do (B/sandboxes.go): hello cached 60 s
  (10 s after a failure); refusals decoded into `*scmError` with the HTTP
  status; a 5xx without a body → `unavailable`. K sets `scmFor` and
  `scmBound` in `init()`.
- Calls go out as the agent tile; xbind adds the partition headers, so from
  a person's partition the provider sees that person (and reaches its own
  partition for them), and from global or an unpartitioned agent a tile
  (bot only). The agent never sends `as: person` from global.
- Default identity per project: `policy.as` if set; else `person` in a
  person's partition (personal and membership projects), `bot` elsewhere.
  A membership may use `bot` only when `policy.membersAsBot` and the
  provider's `you.identities` both allow it: `membersAsBot` is a
  permission, never the identity — a membership works as the bot only when
  its `policy.as` is `bot` (§12.2).
- **The bot rule** (V15, §7.1). Where the home's identity is the bot, the
  agent is the bot's only gatekeeper (§4.4's last rule): `scmBotAllowed(w,
  repo)` (K fills it) is true for the agent's managers (`w.manager()`,
  which is true for every element, B/access.go:78: a tile holding a grant
  to the agent is a manager everywhere already) and for a person the
  **scm bot rule** names with a repo glob that matches — a setting:
  `scmBotRule {users, repos}` as JSON in the bot home's own database
  (`putSetting("scm_bot_rule", …)`, read with `getSetting`; `GET|PUT
  /projects/scm/bot`, managers only; empty by default). Only a bot home
  reads it — a partitioned agent's global instance or an unpartitioned
  agent, each its own database; a person's partition never consults it. A
  build without Projects keeps it as an unknown setting, and it rides
  along wherever the database goes (a backup, a rollback, the next
  upgrade). Checked where a repo is named (a project, a repo added, an
  upgrade, a CI watch) — reads and tokens afterwards follow
  from the project, whose ACL decides who acts in it. The project's later
  participants act through the bot for its repos; that is what naming the
  repo granted, and the person who named it is recorded (`created_by`,
  the job's `by_user`).
- **Live tokens** are held in memory only: `liveTokens[pid|sandboxRef|host]
  = {token, expiresAt, refreshAfter, purpose, identity}`. `purpose` =
  `proj:<uid>:<sandboxRef>`. Nothing durable holds a token (`project_creds`
  holds metadata). A process restart re-mints on the next `ensure`.

### 9.2 The gate: `scmCredWhy(p, as, ref, box) string`

`""` means the token may be written into sandbox `ref`. It is the
`credWhy` pattern (B/harness_creds.go:356) and is re-checked before **every**
write and refresh.

| `as` | Every one of these must hold |
|---|---|
| `person` | `userMode()`; `p.Owner` is the partition's person (`runUser`); `p.Kind` is `personal` or `membership`; `p.Visibility` private and no members; `sandboxPrivate(box)` (B/harness_engine.go:774) and the sandbox's owner is that person; `homedHere(box)` (B/sandbox_partition.go:56); not `hostedUsed(ref)` (B/harness_creds.go:395) — nor, for a fork, of its source; **not a sandbox the project cloned from a team's seed** (`p.FromSeed`, `why` `seed-clone`: the seed is writable by the whole team, V16). That is all the gate can tell of a sandbox's past: the manager reports no clone ancestry (the sandbox answer has no source, docs/sandbox-manager.md §Snapshots, clones and archives), so a sandbox the person cloned from a seed themselves, outside the agent, passes (`credWhy` can't tell either): cloning a seed by hand is the person's own act, outside what the agent can check |
| `bot` | **where:** in a person's partition `homedHere(box)`; elsewhere the sandbox is homed at this agent's own identity — `box.Owner.Via == xbin.Self()`, no `partitionId`, not `box.Shared` (the test `homedAtOwnGlobal` makes, B/sandbox_partition.go:75, without its user-mode clause). **who:** every user of the sandbox may act in the project — its owner, each member, the team when its visibility is `team` — at participant level or above on the project's ACL — `projectLevelOf(p, user)` (§14.1, P1 fills it from `loadProjectACL`; `lvNone` until P1 is in, so the clause fails closed) for the owner and each member; team visibility: the project's is `team` with `teamRole` participant (columns of `Project`) — and it has no shares (a share's shape is the manager's: fail closed). **history:** not `hostedUsed(ref)`. **policy:** in a person's partition the project's identity is the bot — `policy.as == "bot"`, which a membership may set only under `membersAsBot` (§9.1) |

The `xbin.agent/project=<uid>` label is for display and lookup only; no
row above reads it (any participant could patch it onto a sandbox). A team
**seed** sandbox never gets a credential (§12.1). A refusal sets
`project_creds.state='blocked'` with `why`, the task `ws=failed` with
"credentials can't go into ‹sandbox›: ‹why›", and scrubs anything already
there. K's `TestCredGateMatrix` covers each clause: person/bot × private,
shared, member-shared, team-visible, another owner's, hosted-used, a fork,
a seed clone, not homed, at its own global identity, a team seed.

### 9.3 Files

Written with `WriteFile` (`Mode: "0600"`, `Mkdirs: true`; the directory
0700; B/sandbox_files.go:112), atomically (write `…tmp`, then rename via
one `Run`):

| Path (under `H/.config/xbin-scm/<uid>/`) | Content |
|---|---|
| `<host>.cred` | `username=x-access-token\npassword=<token>\n` — git's credential protocol |
| `gh/hosts.yml` | `<host>:\n    oauth_token: <token>\n    user: <login>\n    git_protocol: https\n` |

`H` must match `^/[A-Za-z0-9._/-]+$` (it is spliced into the helper line);
otherwise the gate refuses ("this sandbox's home can't hold credentials").

### 9.4 Git config

Set on each **base** repo (worktrees share it) and on each clone-mode
checkout, by `creds` (and passed to `repo` through `scmGitConfig`, §14.2),
key/value pairs applied with `git config --replace-all` (the first with
`--unset-all` before):

```
credential.https://<host>.helper   ""            # resets helpers a global or system config adds
credential.https://<host>.helper   !f(){ test "$1" = get && cat '<H>/.config/xbin-scm/<uid>/<host>.cred'; }; f
credential.https://<host>.useHttpPath false
user.name                           <author.name>   # from the token answer
user.email                          <author.email>
```

A person's own `~/.gitconfig` and `gh` login are untouched (V2):
`GH_CONFIG_DIR` points `gh` at the project's directory, set in every task
exec's env (§8.5) and in `P/.xbin/env` for terminals.

### 9.5 Refresh

- A timer per live token at `min(refreshAfter, written + 75 % of its
  life)`: re-mint (same purpose), rewrite both files, update
  `project_creds`. Long harness turns keep the partition running, so the
  timer fires.
- `ensureCreds(ctx, p, k, ref, minLeft)` (the `scmEnsureCreds` hook) runs
  **synchronously** in the worker — before every git step, before `bind`,
  and in the `creds` job the gate queues when a task's token is near its
  refresh (§8.6) — never on the engine's path; `minLeft` 10 min.
- 409 `signin` from the provider: K keeps the answer's `signin` in memory
  per (person, provider) — what `scmPendingSignin` answers — `ws=signin`,
  the task parks (`ProjectPark.signin` — the device code only in the
  requesting person's own view, §9.8), and K's `creds` job polls
  `SigninPoll` itself: it answers `jobOutcome{WaitMs: intervalMs}` (the
  job `waiting`, `next_ms` = now + `intervalMs`) while the sign-in is
  pending, for at most 15 min, then writes the credential and finishes —
  P1's worker loop only runs the job; it never calls the provider for K.
- `scmCredsDue(t, p, k)` (§14.1) is K's answer to the gate (§8.6), from
  `project_creds` alone: no live credential for the task's sandbox, or its
  `refresh_ms` within 10 min. `GET
  /projects/scm/signin` reads the provider's state with `SigninState`
  (`GET /scm/signin`), which starts nothing.

### 9.6 Scrub

`scmScrubCreds(ctx, p, ref, why)` empties both files (0600, zero bytes),
revokes the purpose at the provider (`POST /scm/token/revoke {purpose}`),
drops the live token and sets `state='scrubbed'`. Triggers (each a
one-line call):

| Trigger | Where |
|---|---|
| a sandbox shared (visibility, members) | `readyForShare` (B/harness_creds.go:1024), called from `handlePatchSandbox` (B/sandbox_routes.go:221), and a sibling of `stopCredsIn` (:256) |
| a sandbox stopped or archived through the agent | `handleSandboxAction` (B/sandbox_routes.go:331) |
| a sandbox deleted | `handleDeleteSandbox` (B/sandbox_routes.go:280) → `projectSandboxGone(ref)` |
| a project archived or deleted, a repo removed, the sandbox leaving the project | P1's handlers |
| Forget (`DELETE /projects/scm/signin`) | every project of the person, before the provider forgets |
| a task's fork deleted | P2's cleanup |
| the gate refusing a sandbox that had a credential | §9.2 |

`projectsInSandbox(ref)` (P1, §14.2) says which projects to scrub.

### 9.7 Redaction

- **Patterns** (added to `redactor.apply`, B/harness_redact.go:58, beside
  `tokenShape`): `gh[pousr]_[^\s"'@]{30,}` and `github_pat_[^\s"'@]{22,}`
  — up to the next space, quote or `@`, assuming no charset (a stateless
  `ghs_` token is ~520 characters of a format GitHub doesn't document);
  plus **every live token** exactly (a package-level set the client keeps,
  read by every redactor). Masking is same-length (`mask`).
- **In memory** a token is an `scmSecret` (scm_types.go, S0): `%v`, `%+v`,
  `%#v` and `json.Marshal` of anything holding one say `[secret]`;
  `Reveal()` is the one way to the value (the credential files, the
  explicitly built revoke body). `TestSCMSecretNeverShows` (S0) pins it.
- **Applied at:** `addMessage` (B/db.go:529) and `rewriteMessage` (:577) on
  `content` (every tool result passes through one of them); the harness
  stdout reader (B/harness_engine.go:1040) and the harness log
  (B/harness_log.go:150) — both through `redactor.apply`, so they gain the
  patterns at once; `project_jobs.out`, `project_tasks.setup_tail` and
  `last`, `project_events.body`; CI log text, annotations and snapshots
  (V, through `scmRedact`); log excerpts sent to tasks (E).
- `scmRedact` (the hook, §14.2) is `redactText` with the live set.
- **The seeded-token test** (K's for what K owns, C's for its tools, and
  the lead's at gate 1 for a task, §15.2): a token minted by the fake
  provider and printed by bash (`cat` of the cred file, `env`, an error)
  appears in no row, log, event, job output or transcript.

### 9.8 Who sees what

- The device code (`signin.userCode`) is shown only to the person who must
  sign in: `ProjectPark.signin` is filled from `scmPendingSignin` only in
  that person's own `GET /runs/{id}/view` and `GET /runs/{id}/task` (P1's
  `runViewHooks` entry checks `who`; never a stream event, never another
  viewer), as `harness.login.device` is (D147 §4.3.2).
- `GET /projects/{pid}/status` shows credential metadata (identity, login,
  expiry, state, why), never a token.

## 10. Agent: the coordinator (C)

### 10.1 Placement and class

- One per person per project, made on first use (`POST
  /projects/{pid}/coordinator`, or the first project event that wakes
  one): a run with `origin='project'`, `origin_id=<pid>`,
  `session_key='proj:<pid>:coord:<user>'`, owner the person, private,
  `Config.Project {id, role: "coordinator"}`. In a team project it lives in
  the member's partition, on their membership (V8).
- **Class: the built-in `web`** (the web lane — never internal reach: it
  instructs tasks that have egress, so it must not hold `tsInternal`;
  checked when it is made and on every project tool call). Run `Deny`:
  `web_search`, `web_fetch`, `schedule`, `unschedule`, `skill_manage` —
  `policy.coordinator.web: true` lifts the first two. Model:
  `policy.coordinator.model` or the agent's default.
- Never gated (§8.6); takes the model-call gate as any top-level run.

### 10.2 The resolver

```go
func (ag *Agent) coordinatorOf(t *DB, run *Run, cfg Config) (*Project, error) // depth 0, Role coordinator, the session key's project active, the firewall rule
func (d *DB) projectTaskOf(p *Project, n int64) (*ProjectTask, *Run, error)    // by project-local number, never a run id
```

No `ParentID` walk (the replaced `Engine.node` rule, B/subagent_tools.go):
project membership is the boundary. **The lane rule stays, as a class
rule** (V18): `Engine.node` refuses to reach a run in another capability
lane (B/subagent_tools.go:205); here the coordinator is in the web lane
and no task's class holds `tsInternal` (`refusalClassInternal`, 409
`class-internal`: `POST /projects` and `PATCH` on `policy.taskClass`,
`TaskSpec.Class`, the coordinator's `task_create`, and P2's upgrade, whose
conversation becomes a task, §8.10). The rule is on
`tsInternal`, not on lane equality: a class without egress is lane
`private` in `lane()` (B/classes.go:177), yet has no internal reach
either. A task's stored lane can't hold that line: `clampTo("web")` strips
`tsInternal`, but `clampTo("private")` (B/classes.go:192–203) strips only
egress, so a lane-`private` task whose class is later edited to add
`tsInternal` would gain it. So P1 adds one line to `classState.classOf`
(B/classes.go:398), after `clampTo`: a config with `Project` set (a task
or a coordinator, and their subagents, which copy it; the gate restores a
task's before any turn, §8.6, and a coordinator is in the web lane, which
`clampTo` already holds) loses `tsInternal` and its MCP servers
(`c.Toolsets = without(c.Toolsets, tsInternal); c.MCP = classSet{}`),
whatever its lane, at turn start and at every tool call alike.
`projectTaskOf` refuses, as `Engine.node` does ("in a different capability
lane"), a task whose run's class or lane has internal reach anyway (a
run made by an older build, a class edited in the database). A class is
also checked as `POST /ask` checks it: `usableBy(who)` (the class's
`who`: managers-only classes need a manager) for the person creating, and
`allowsManager` for the project's sandbox manager. Subagents of a
coordinator get no project tools. Writes run under `e.fenced`, pokes
`AfterCommit`.

### 10.3 Tools

Offered at depth 0 only, when `projectRefOf(run).isCoordinator()` (§6.12; the way
`cfg.Channel` gates `attach_to_reply`, B/tools.go:208 — no new toolset,
which would need the stored-classes rollback trick, B/classes.go).
Dispatched in `runTool` (B/tools.go:331) beside the thread tools. First
sentences are pinned in B/tooldesc_test.go:29 — exactly these:

| Tool | Parameters (JSON schema, all optional unless marked) | First sentence |
|---|---|---|
| `task_create` | `tasks: [{title: string, brief: string (required), repos: [string], size: "small"\|"big"}]` (1–10) **or** `issues: [integer]` (1–10) with `repo: string`; `note: string` (added to every brief) | "Create tasks in this project — each its own conversation with a git worktree per repo — started now or queued behind the project's limit of tasks running at once." |
| `task_list` | `state: string` (a task state, or `open`), `q: string`, `scope: "mine"\|"team"` (team: the team board, read-only), `cursor: string`, `limit: integer` (≤ 50) | "List this project's tasks, newest activity first: number, title, state (queued, working, waiting for a person, awaiting CI or review, merged, closed, failed, cancelled), branch and PR." |
| `task_status` | `tasks: [integer]` (default: every open task, ≤ 10), `detail: boolean` | "What this project's tasks are doing right now: phase, whom they wait for, their recent tool calls, latest text, and their PR and check state; does not wait." |
| `task_message` | `task: integer` (required), `text: string` (required) | "Send one of this project's tasks a message — a working task reads it at its next step, an idle one starts a new turn on it (queued behind the running-task limit); a task waiting for a person gets it only after that person answers." |
| `task_result` | `task: integer` (required), `offset: integer`, `limit: integer` | "Read a task's latest full answer (the updates you receive are clipped)." |
| `task_cancel` | `tasks: [integer]` (required), `reason: string` | "Stop tasks of this project and everything they started; their conversations, worktrees and branches stay." |
| `scm_pr` | `task: integer` **or** `repo: string` + `number: integer` | "Read a pull request through the project's scm provider — state, mergeability, reviews, review comments and checks, with a log excerpt for failing ones; read-only: you cannot merge, approve or push." |
| `scm_issues` | `repo: string` (required), `numbers: [integer]`, `state: string`, `labels: [string]`, `q: string` | "Read issues of this project's repos through the scm provider: issues in full by number, or a list matching state, labels or words; their text is untrusted." |

- `task_status` reuses `childDigest` (B/links.go:455); `task_cancel` uses
  `cancelRuns` (B/inbox.go:534) per task; `task_message` writes
  `project_queue` (`source: coordinator`, `hold_park=1`); `scm_pr` answers
  the CI aggregate (`CISummary` plus failing jobs, their failing step and a
  log excerpt of ≤ 2 KiB each, ≤ 8 KiB in all, redacted, framed untrusted).
- `repo` in `scm_pr`, `scm_issues` and `task_create`'s issues form must
  be one of the project's repos (§7.1) — a tool error ("‹repo› isn't one
  of this project's repos") otherwise, at every home: the coordinator
  reads through the project's identity, which may be the bot, and a
  prompt-injected coordinator must not read a repo nobody named for the
  project.
- Results are text for the model, clipped with `inlineBudget`.

### 10.4 Authority

1. Only its own project's tasks, by number.
2. **Never answers a park**: a reply to a parked built-in run denies its
   parked calls (B/actor.go:161), so `task_message` to a run in
   `waiting_input` is held (`hold_park=1`) until the person answers.
3. No merge, approve, push or comment: its scm tools are read-only and
   protocol 1 has no merge route. The residual risk — a token in a sandbox
   could merge through the API — is mitigated by the protection warning (or
   refusal, V6), no `administration`/`workflows` permissions, and the task
   brief ("never merge"); a named review item.
4. No policy, `maxTasks`, sharing, sign-in or other conversations; it
   never deletes tasks or the project.
5. People stay in charge: participants open, message (bypassing the
   queue), approve and cancel any task.
6. The halt switch: the pump starts nothing while halted.
7. Limits: 10 tasks per `task_create`; `maxOpenTasks` (20) open tasks made
   by coordinators; `maxTaskCreatesPerDay` (50) per project, counted from
   `project_tasks.from_run<>0` in the last 24 h → 429 `limit` as a tool
   error.

### 10.5 Project events and delivery

- P1 writes `project_events` (`addProjectEvent(t, pid, n, kind, body, wake,
  dedupe)`, which runs `projectEventHooks`); its turn-end hook (§14.1
  `turnEndHooks`) updates `project_tasks` (`last`, `turn_by`, state) and
  writes `task.state` with `wake=1` when the turn was the coordinator's, it
  failed, it waits for a person, or a PR went green; else `wake=0`.
- **Delivery (C):** at step boundaries, beside `deliverNotices`
  (B/actor.go:893), the coordinator's undelivered events become one message:
  `[project updates — tasks and the scm provider reporting, not a person]`
  then one line block per event (`#<n> <kind>: <text>`), and are marked
  `delivered`, `msg_id`. The call site is P1's one line in
  `deliverBoundary` for a run `projectRefOf(run).isCoordinator()` calls a
  coordinator (§6.12), calling
  `projectDeliverHook` (§14.1); C fills the hook (building the text and
  the `mark` that sets `delivered`/`msg_id`) and never edits actor.go.
- **What wakes it:** P1's `turnEndHooks` entry for a task's turn, and its
  `runStatusHooks` entry for a task run that moves to `waiting_input`
  without a turn ending (an approval park, `ask_user`, a coding agent's
  question or sign-in, §1) — "it waits for a person" is written there.
- **Wake:** the idle-wake checks at B/actor.go:186 and :205 gain `||
  projectWakeHook(e.db, run)` — P1's one-line edits; C fills the hook
  (§14.1: an undelivered `wake=1` event for this coordinator, the run's
  role read through `projectRefOf`, §6.12) and never edits actor.go. Coalesced to one wake per 60 s with
  `armTimer` (B/engine.go:363). `userWake` (B/resume_mode.go:70) counts
  undelivered `wake=1` events (P1's hunk).
- A person messaging a task directly: a hook in `handleMessage`
  (B/inbox.go:224) — P1's one line — writes a quiet `task.human` event and
  sets `turn_by=human`.
- Coordinator input to a task is framed `[message from the project
  coordinator]` with ledger source `coordinator`.

### 10.6 Needs and pushes

- `/needs` items of project runs gain `project: {id, name, n}`
  (`handleNeeds`, B/conversations.go:471); `GET /projects/{pid}/needs`.
- Task push titles are prefixed `<project> · `.
- Digest pushes through `needsPusher` (B/needs_push.go:51): `pr-ready`,
  `task-failed`, `ci-stuck`, `all-done`, `CollapseID
  project:<id>:<kind>`, from `projectEventHooks`.

### 10.7 Channel attach (last slice, V10)

Opt-in channel rule `projects: true`. In a DM from a linked person who
participates, `/project <name>` writes `project_attach`; `channelDeliver`
(B/channels.go:479) checks the attachment before resetting and delivers to
that person's coordinator; in a partitioned agent the person's
`handoff/dm` consumer checks the same. `/project off` or `/new` detaches.
Personal projects only.

## 11. Agent: scm events and polling (E)

### 11.1 Intake

1. `POST /adapter/scm/event` (E's `adapterRouteTables` entry, §7.1;
   `adapterRoutes` mounts it with `adapterGuard`): the caller's `X-XBin-From` must be
   in `scmBound()` — any other adapter holding the channel role gets 403
   ("only a bound scm provider delivers scm events"). Body: event v1 ≤ 1
   MiB; `protocol` 1 (else 400). The `scm.provider` field is ignored —
   the caller's path is the provider.
2. Dedupe on `eventId` in `scm_seen` (7 days): a repeat answers 200.
3. Route by `for`:
   - `global` — handled here (the agent's global, or an unpartitioned
     agent).
   - `user:<id>` at a partitioned agent's global — handed to that person's
     partition by partition mail **`handoff/scm`** (a new topic beside
     `topicEvent`, B/handoff.go:100), reusing `queueHandoff` as `handEvent`
     does (B/trigger_registry.go:259); the partition's handler (registered
     from `init()`, as B/handoff_user.go:39) dedupes on `eventId` again and
     handles it. Answer 200 once queued. The partition's handler drops an
     event whose `forPid` isn't `partitionID()` (B/mode.go:143; counted): a
     person re-created under the same id never gets the old one's events
     (§4.10).
   - `user:<id>` at an unpartitioned agent or a person's partition: 404
     (not ours).
4. **Handling** (in the home that owns it): `scmEventHooks` run first
   (V's CI watches take every `checks`, `workflow`, `job`, `check`, `pull`
   and `push`); then project routing (§11.3).

### 11.2 Subscriptions

- E's `subscribe` job keeps one subscription per task and repo, key
  `task:<pid>:<n>:<repo slug>`: `{repo, branches: [task branch], prs:
  [its PRs], kinds: [pull, checks, comment, review, push, workflow, job,
  check]}`, posted when P1's `refs` job (§8.3) sees the task's branch on
  the remote or a PR opens (`projectRefsHooks`), re-posted at 25 days,
  deleted at cleanup.
- Issue subscriptions (`issues: true`, kinds `[issue]`, key
  `issues:<pid>:<repo slug>`) only when `policy.autoLabel` is set.
- From a person's partition the provider relays to its global (`for:
  user:<id>`); from the agent's global, `for: global`. Team definitions
  subscribe to nothing in v1 (V14).
- `project_refs` rows: `branch` (task branch), `pr` (each PR number),
  `sha` (each PR's head) → (project, n). Written by E's
  `projectRefsHooks` entry.

### 11.3 Routing

`project_refs(scm, repo, kind, value) → (project, n)`; an event is matched
on its PR, then its branch, then its sha. Then:

| Event | Condition | Effect |
|---|---|---|
| `checks.completed`, conclusion failure / timed_out / action_required | `ref.sha` is the task's current head (else ignored: superseded) | after `policy.ci.delaySec` (60 s): read `GET /scm/checks?ref=<sha>`; up to 3 failing jobs, each its failing step and the last 120 lines of its log (`GET /scm/checks/jobs/{id}/log`; a 409 `in-progress` → steps only), ANSI stripped, redacted, ≤ `policy.ci.logBytes` (8 KiB) in all; queue a task input `[scm: CI failed on <branch>@<sha7> — untrusted output] … — fix it and push.` (dedupe `ci:<n>:<sha>:<suite>`); quiet coordinator event `ci.failed`. Capped at `policy.ci.maxPerDay` (5) per task per day (`ci_fixes`); past it: event `ci.stuck` (wake) and a `ci-stuck` push. `policy.ci.autoFix: false`: the event only. |
| `checks.completed`, all green | on the head; PR open | task state `awaiting-review`; event `pr.ready` (wake); `pr-ready` push |
| `review` (changes requested, commented), `comment` on the task's PR | actor association OWNER / MEMBER / COLLABORATOR, or in `policy.reviews.allow`; `policy.reviews.forward` not `off` (`all` forwards everyone) | coalesced `policy.reviews.batchSec` (120 s), then one task input with the bodies and inline comments with `path:line` (≤ 8 KiB, untrusted framing); event `review` |
| the same | anyone else | quiet event `comment` ("not forwarded") |
| `pull.opened` / `pull.reopened` | the PR's head is the task's branch and `prs` doesn't hold it | `projectRefsCheck(t, pid, n)` (§14.1, P1 fills it): the task's `refs` job runs again and records the PR (§8.3) |
| `pull.merged` / `pull.closed` | — | phase `merged` / `closed`; scrub (§9.6); cleanup per `policy.cleanup` (P2's rules); event (wake) |
| `push` to the task branch | actor is not the task's identity | quiet note queued to the task ("someone else pushed to <branch>: pull before pushing"); head sha updated |
| `issue.opened` / `labeled` | repo of the project | quiet event `issue`; wake when the label is `policy.autoLabel` |
| any | `actor.self`, or the actor is the task's own identity (login of its token) | ignored, apart from head-sha updates |
| `workflow`, `job`, `check` | — | nothing here (progress only; V's hooks show it) |

Every input to a task goes through `project_queue` (`source: event`,
`hold_park=1`) and the pump.

### 11.4 Dedupe

- Transport: `eventId` in `scm_seen`.
- Semantic (webhook and poll describe the same fact):
  `sem:<pid>:<n>:<kind>:<sha|pr>:<suite or review id>:<conclusion|state>`
  in `scm_seen`, checked before acting on either.

### 11.5 Polling

`scm_poll(project, n, repo, kind)` rows, active while a task has an open
PR awaiting CI or review:

- Cadence while no webhook has arrived for that sha: every 2 min for the
  first 20 min, every 10 min until 2 h, every 30 min until 24 h; then stop
  with an event `note` "lost track of CI — check manually" (wake).
- With healthy webhooks (hello `events.healthy`, or a delivery for that
  repo within 30 min): only a 15-min safety poll once CI has been pending
  more than 30 min.
- One `POST /scm/poll` per provider and home per pass (≤ `limits.pollItems`
  items, conditional), from the worker; a `changed` item is handled as the
  matching event would be (with the semantic dedupe).
- A stopped partition: the next due time is merged into `userWake`
  (B/resume_mode.go:70, E's hunk); a webhook's mail starts the partition
  through the doorbell. At global or unpartitioned, `leaveWakeUp` (:37)
  applies. `POST /tick` (B/handlers.go:536) also runs a poll pass.

### 11.6 Docs

`docs/agent-inbox.md` gains "scm events" (E): the route, the body (link to
docs/scm.md), the caller check, `for` and the partition hand-off.

## 12. Agent: team projects (T)

### 12.1 The definition (at the agent's global instance)

- A `projects` row with `kind='team'` (ids < 2^40), visibility `team` or
  members, made by `POST /projects {kind: "team", share, …}` at global (a
  person reaches it from their page with `?xbin-partition=global`, or a
  manager). It holds the name, repos, policy, members and an optional
  **seed sandbox**; it has **no tasks** and no coordinator (coding agents
  are barred at global, D172).
- Its worker keeps only the seed: `sandbox`, `repo`, `fetch` and the
  fork-base `snapshot` jobs (P2's) — never `creds`: **a seed holds no
  credential**, so its `repo` jobs work only for repos the sandbox can
  clone without one (public), else the seed is left without them and each
  membership clones for itself.
- Its routes are P1's (`GET/PATCH/DELETE /projects/{pid}`, members, repos)
  plus T's board and seed routes (§7.2).

### 12.2 Memberships (in each member's partition)

- `POST /memberships {team: <gpid>}` in the person's partition: reads the
  definition (`callGlobal GET /projects/<gpid>`, B/gwcall.go:135 — the
  person calling as themselves; 404 if they can't see it) and creates a
  `projects` row `kind='membership'`, `team_ref=<gpid>`, owner the person,
  private, with the definition's name, scm, host, repos and policy, and
  its own slug made from the name by §6.1's rule (`-2`, `-3`… on a clash
  in this partition). Made lazily: when the member opens the team project's page and picks
  "Work on this", or creates a task in it; creating one shows the
  definition's security part (below) first and sends its hash as
  `accept` (§7.2).
- **Its sandbox:** the person's own, private, homed in their partition —
  `sandbox: {ref}` one they have, or `{new}`. Only when the identity §9.1
  resolves for the membership is the bot — its accepted `policy.as` is
  `bot`, which `policy.membersAsBot` and the provider's `you.identities`
  allow (V16; `membersAsBot` alone, with `as` unset, is the person) — and
  the seed has a fork-base snapshot
  the manager can clone for this person (a team-visible seed they can see,
  `clone` cap), is it `from: {sandbox: seed, snapshot}` with
  `from_seed=1`. Otherwise it starts fresh with its own `repo` jobs. Then
  `creds`: the person's identity — never in a seed clone (§9.2
  `seed-clone`) — or the bot when `policy.as` is `bot`. A seed-cloned
  membership whose accepted definition no longer resolves to the bot
  (`as` changed, or `membersAsBot` off) fails its tasks with `seed-clone`,
  and its page offers "start in a fresh sandbox". T's
  `TestPersonCredsRefusedInSeedClone` includes a definition with
  `membersAsBot` and `as` unset: its membership starts fresh
  (`from_seed=0`) and its person credential is written.
- **Definition sync:** the membership re-reads the definition (ETag on
  `version`) when its page opens, before each task start, and every 10 min
  while it has open tasks. The name and removed repos follow at once (a
  removed repo stops new tasks using it; running ones keep their
  checkouts). The **security part** follows only on acceptance (V17):
  the canonical JSON of `{repos: [{repo, setup}], policy: instructions,
  checks, prConventions, taskClass, engine, harness, as, membersAsBot,
  reviews, autoPR, autoLabel, ci, coordinator, workflows, protection,
  branchPrefix}` (the remaining policy keys follow at once), and
  `def_hash` is its SHA-256. While it differs, `def_pending` keeps the new
  definition and the membership keeps running the values it accepted (its
  own row's `policy` and `project_repos.setup`; an added repo isn't used
  yet). A `note` project event (`pevNote`, wake; the coordinator can only
  tell the person, never accept) and the page card "Review the team
  project's changes" tell the member; the card reads `GET
  /memberships/{pid}/pending` (the member only: `{hash, accepted,
  pending}`: the security part as accepted — the membership's own `policy`
  and `project_repos.setup` — and as pending, from `def_pending`'s JSON,
  `hash` its hash) and shows the two side by side,
  each changed key and setup script marked; `POST /memberships/{pid}/accept
  {hash}` adopts exactly what it showed (409 if the definition moved on).
- Its tasks, its coordinator and its credentials are a personal project's
  in every respect (§8–§11), coding agents allowed. Its coordinator's
  `task_list {scope: "team"}` reads the board.

### 12.3 The board

- `project_board` at global: one row per (member, task). `BoardRow` JSON:
  `{member, n, title, col, state, waiting, branch, prs: [TaskPR], ci:
  CISummary|null, run, updatedMs}` — `run` the member's own run id, which
  only that member can open (others see the row, never the transcript).
- **Push:** every task change in a membership (`taskChangedHooks`, T's
  entry) writes the row to `project_board_out` (latest wins); a sender
  (in the partition's worker) `PUT`s it to global `/projects/<gpid>/board/<n>`
  via `callGlobal`, retrying 10 s doubling to 10 min; 404 (no longer a
  member) → §12.5. A deleted task is `PUT` with `state: "deleted"` and the
  row hidden.
- At global, `PUT /projects/{pid}/board/{n}` is accepted only from a
  person's partition (`personFromPartition`, B/partition_routes.go:53) whose
  person is a participant of the definition; `member` is that person,
  never a body field. The row keeps the caller's partition id
  (`member_pid`, `X-XBin-Partition-Id`); a `PUT` from a different one
  deletes that member's rows first (a re-created person starts fresh).
- Global validates every row: `title`, `waiting`, `branch` and
  `ci.current` are clipped (200 chars) and shown as plain text, never
  markdown or HTML; each `prs[].url` and `ci.url` must be https on the
  definition's `host` (`projects.host`) or it is dropped; `run` must be
  ≥ 2^40 (a partition's id, B/partition_start.go:25) or the `PUT` is 400,
  and is offered only to that member, as "open (theirs)"; anything else
  in the body is ignored.
- A `project` stream event (`change: "board"`) at global tells viewers.

### 12.4 Credentials and events

A person's identity by default; the bot only when the accepted
definition's `policy.as` is `bot`, which its `policy.membersAsBot` and the
provider's `botForPeople` must allow (§9.1, §12.2). A person's token never enters the seed, nor a
sandbox cloned from it (V16).
Events are personal (`for: user:<id>`, §11.2); the board follows from the
member's partition.

### 12.5 A member removed

- At global: their board rows are marked `stale=1` (shown greyed, "no
  longer a member"); the owner may hide them (`…/hide`).
- In their partition, at the next sync or board push (404/403): the
  membership is archived — credentials scrubbed, the pump stopped, fetches
  stopped; the tasks and their conversations stay the person's own (they
  are their data, deleted with their partition).
- A deleted definition: the same, plus the board rows go.

## 13. UI

Plain ES modules, no build step; state and behaviour in `model/` (no DOM,
no lit), thin web and native views over it; every feature key in both
views (or listed in `DIFFERENCES` with the reason until the other view's
WP lands). New code goes in new files; hot files get only the lines
named here.

### 13.1 Model (U1; V for CI)

| Module | WP | Exports |
|---|---|---|
| `model/project-api.js` | U1 | `projectApi(app, pid)` → the home's API for a project (`homeApi(homeOf(pid))`, model/home-api.js); route helpers for §7.2 |
| `model/projects.js` | U1 | `createProjects(app)` → `{list, load(), open(pid), opened, board(pid), tasks(pid, filter), take(ev), create(body), createTask(pid, spec), batch(pid, issues), patch(pid, body), remove(pid, sandbox), status(pid), warm(pid), issues(pid, q), signin(scm), pending(pid), accept(pid, hash), …}` — the `AutoPage` pattern (model/auto.js); events `projects`, `project` |
| `model/project-task.js` | U1 | pure words for a task conversation: `taskChips(view)` (branch, PR #n with state, the setup outcome), `prepCard(view, me)` (the workspace steps; the sign-in card only for the person who must sign in), `prButton(view)`, `crumb(view)` ("‹project› ›"), `columnOf(task)` |
| `model/ci.js` | V | §13.5 |
| `model/app.js` | U1 (V) | U1: `app.projects = createProjects(app)`; `app.projects.take(ev)` in `app.event`; `page: 'projects'`, `app.openProjects(pid?)`; `follow()` of `#proj`. V: `app.ci = createCI(app)`; `app.ci.take(ev)` in `app.event` (two lines) |
| `model/router.js` | U1 | `parse()` gains `proj: {id} \| null` from `#proj` / `#proj=<id>`; `projHash(id)` |
| `model/features.js` | S0 (U1, V, U2 unstage) | areas `proj` and `ci` and the keys of §13.6, registered by S0 and staged; each WP takes its keys out of the staged lists |

### 13.2 Web (U1; V for CI; U2 for C, E, P2 and T surfaces)

**Seams** — S0 added these to `web-ext.js`, documented in its header like
the others; nobody else edits that file. Each call site is placed by the
WP that draws there (named after each):

```js
//   side()         entries in the sidebar under Automations            — agent.js (U1)
//   page(p)        the page app.page names when no conversation is open
//                  (not 'automations'): {top, body} templates — the first
//                  module that knows p answers                         — agent.js (U1)
//   crumb(v)       a link before the open conversation's title ("Web ›")
//                  when no automation crumb is shown                   — agent.js (U1)
//   dock(v)        sections of the right dock beside Coding agents:
//                  {key, title, badge?, tpl()}                         — harness-board.js (V)
//   card(task)     chips on a project board's task card (a TaskView)   — projects.js (U1)
//   childStatus(r) words after a coding agent card's status line       — harness-child.js (V)
//   sbx(b, close)  actions at the end of the ▣ sandbox popover (#sbxpop;
//                  b the conversation's binding, close() closes it)    — sandboxes.js (U2)
export const ext = makeExt({ block: 'first', end: 'all', top: 'all', paint: 'each', newChat: 'all', task: 'all',
  side: 'all', page: 'first', crumb: 'first', dock: 'all', card: 'all', childStatus: 'all', sbx: 'all' });
```

**`agent.js`** (885 of 949 lines; U1 adds at most 12, nobody else adds any):
- `paintSide()`: `render(ext.side() || nothing, $('sideext'))`;
- `topTpl(null)`: `app.page` other than automations → `ext.page(app.page)?.top`;
- `paint()`: the timeline when no conversation is open → `ext.page(app.page)?.body`
  before `homeView()`;
- `topTpl(v)`: `${t.crumb ? … : ext.crumb(v) || nothing}`;
- one import: `import './project-web.js';` beside `./harness-web.js`.
- `index.html`: `<div id="sideext"></div>` after `#autos` (U1).

| Module | WP | What |
|---|---|---|
| `project-web.js` | U1 | the import list (one line per module, each in its own slot, as harness-web.js) |
| `projects.js` | U1 (U2 adds C, E, T sections) | `side` entry "Projects" (with needs-you count); `page('projects')`: the list (yours, team), a project's page — board columns (§6.11) with task cards (title, #n, state, branch, PR; the **CI chip** is `${ext.card(task) \|\| nothing}`, V's; the PR chip shows `prs[].checks` only while `task.ci` is null), "New task" (text, size, agent, repos), the issue picker (batch), the event feed (U2), the coordinator card (U2), the team board (its rows' text plain, never markdown, §12.3) and "Work on this" with the definition's security part to accept, and the "Review the team project's changes" card (U2, §12.2: `GET /memberships/{pid}/pending`, the accepted and the pending security part side by side, changed keys and setup scripts marked, then Accept) |
| `project-new.js` | U1 (U2 adds upgrade) | the new-project dialog (provider, repos picker `GET /projects/scm/repos`, sandbox pick or new, policy basics); the "Make this a project…" dialog (U2, P2's routes), opened by U2's `sbx(b, close)` action in the ▣ popover of a non-project conversation with a sandbox |
| `project-settings.js` | U1 | repos (add, remove, setup script), policy (every key of `ProjectPolicy`, grouped), members (unpartitioned and team), status (sandbox, repos, credentials, jobs, warnings), archive and delete |
| `project-chips.js` | U1 | `top(v)` on a task: branch chip, PR chip (↗ to the PR; its checks only while `task.ci` is null), "Open PR" (P2's route; hidden until `POST /runs/{id}/task/pr` answers other than 404); `crumb(v)`; `end(s)`: the prep card (steps, Retry) and the sign-in card (device code and link, for the person only); `task(v)`: the task's project, size, repos, checkouts |
| `ci-dock.js` | V | §13.5: `top` (the CI chip), `dock` (the CI section), `childStatus` (the child glyph), `card` (the board chip), `paint` (live refresh on/off); exports `openCI(root, {job?})` (V's own modules only) |
| `ci-cards.js` | V | `end(s)`: CI outcome cards; their "Open logs" calls `openCI` |
| `harness-board.js` | V (≤ 30 lines) | becomes the right dock's host: `st.tab` (`'agents'` or a section key); a tab strip when `ext.dock(v)` answers any section; the dock opens for a section even with no coding agents; `export function openDock(key)` |
| `harness-child.js` | V (1 line) | the status line gains `${ext.childStatus(r) \|\| nothing}` — CI of what that coding agent pushed; no import (it imports `ext` already, and importing ci-dock.js would close a cycle through harness-board.js) |
| `harness-web.js` | V (2 lines) | `import './ci-dock.js';` and `import './ci-cards.js';` in a "CI" slot after U7 |
| `sandboxes.js` | U2 (2 lines) | `import { ext } from './web-ext.js';` and `${ext.sbx(b, closePop) \|\| nothing}` at the end of `.sbxacts` |

### 13.3 Native (U2; V for CI)

| Module | WP | What |
|---|---|---|
| `native/ext.js` | S0 | S0 added `drawer`, `dock`, `card` and `childStatus` (documented in its header); nobody else edits it. Call sites are placed by the WP that draws there: U2 `drawer` (native/convs.js) and `card` (its board rows), V `dock` (native/harness-board.js) and `childStatus` (native/harness-child.js) |
| `native/convs.js` | U2 (1 line) | `${ext.drawer(close) \|\| nothing}` after the Automations row |
| `native/project-all.js` | U2 | the import list; `native.js` gains one import of it |
| `native/projects.js` | U2 | screens (`ext.screen`) `projects`, `project` (board as sections per column; each task row ends with `ext.card(task)`, V's CI words), `project-task-new`, `project-settings`, `project-team`; the drawer row "Projects" |
| `native/project-task.js` | U2 | `toolbar(v)` branch/PR items, `subtitle(v)` "‹project› #n", `menu(v)` "Make this a project…" (a non-project conversation with a sandbox), `end(v, s)` prep and sign-in cards, `task(s)` the project section |
| `native/ci.js` | V | `dock(v)` the CI section of the Coding agents screen (watches → runs → jobs with progress) and its badge on the Coding agents toolbar button (no separate toolbar item); pushed screens `ci-job` (steps, the log with search and follow, "Open live log ↗") and `ci-annotations`; `childStatus(r)` the glyph on a coding agent row; `card(task)` the native board's CI words; `end(v, s)` outcome cards; `menu(v)` "Watch CI for…"; "Re-run failed" for people |
| `native/harness-board.js` | V (≤ 6 lines) | calls `ext.dock(v)` after its sections; shows the toolbar button when any dock section answers, with its badge |
| `native/harness-child.js` | V (1 line) | `${ext.childStatus(r) \|\| nothing}` after a coding agent row's status |
| `native/harness-all.js` | V (1 line) | `import './ci.js';` in a "CI" slot |

### 13.4 Router

`#proj` opens the Projects page, `#proj=<id>` a project (its home from
`homeOf(id)`); tasks stay `#c=<id>` with the crumb back. Native deep links
map onto the same words (`app.follow()`).

### 13.5 CI in the coding UI (V, web and native)

**Where it shows — inside the D147 coding-agent UI, not beside it:**

- **The CI chip** in the conversation's top bar, right after the ⌨ coding
  agents chip (`ext.top`, harness-board.js; ci-dock.js registers after it):
  "CI ● 3/5 jobs · 2:14" while running, "CI ✓" green, "CI ✗ test (ubuntu)"
  red, "CI —" when nothing reported yet, and also when there is no watch
  but `ci.canWatch` (the run view, §7.4); hidden when neither. Its tone
  follows `CISummary.state`. Clicking it opens the right dock on the
  **CI** tab (`openDock('ci')`); for "CI —" without a watch that section
  holds only "Watch CI for…". At home it is absent.
- **The CI section of the right dock** (`#hboard`, the third grid column
  from 1100 px, over the chat below that): tabs "Coding agents · CI"
  when both have content. Per watch: `repo · branch` (↗ the branch),
  `PR #42` (↗), state, "watching since", ✕ (unwatch; not for task
  watches). Per workflow run: name, event, attempt, state, elapsed, ↗ the
  run; **Re-run failed** (people only, `canRerun`, a failed run, confirmed).
  Per job: a row with a progress bar (steps done / total), the current
  step, elapsed, ↗ the job; expand → its steps (✓ ✗ ● ○ with durations);
  **Log** → the log viewer; **N annotations** → `path:startLine` rows with
  level, title and message (↗ the check). Other checks (non-job) and
  statuses follow, each with ↗ `detailsUrl`/`url`. A "Watch CI for…" form
  (repo, branch or PR) at the end.
- **The log viewer** (in the dock, replacing the section until ← Back): a
  lazy tail (64 KiB; "Earlier" asks `until=<from>` of the last answer and
  prepends it), ANSI stripped, monospace, plain text (never markdown or
  HTML), a search box (highlight, next/previous), **Follow** while the job
  runs on a host that serves partial logs (re-read `since=bytes` every
  5 s); on 409 `in-progress`: the job's steps with "the log is ready when
  the job finishes" and **Open live log ↗**. While the viewer is shown the
  dock widens (the `.wrap.dockon` third column becomes `minmax(340px,
  50vw)`; below 1100 px it is already an overlay).
- **Inline cards** at the end of the transcript (`ext.end`, ci-cards.js):
  "CI passed on ‹branch›" / "CI failed on ‹branch› — test (ubuntu) › go
  test ./…" with **Open logs** (the dock on that job) and ✕ (dismissed per
  `<watch>:<outcome>`, in `localStorage`); one per watch whose `outcome`
  isn't dismissed, from `GET /runs/{id}/ci` or any `ci` event.
- **Child cards** (harness-child.js): a coding agent that pushed shows a
  small CI glyph with its state in its status line (`ext.childStatus`,
  registered by ci-dock.js).
- **The project board**: V's `card(task)` entry (ci-dock.js; native/ci.js
  natively) draws each task card's CI chip from `task.ci` and opens the
  task with the dock on CI itself; U1's board and U2's native board call
  `ext.card(task)` — nothing outside V's files imports `openCI`.
- **Native** (`native/ci.js`, V): the CI section of the Coding agents
  screen (`dock(v)`; its badge on the Coding agents toolbar button, no
  separate toolbar item), the pushed `ci-job` and `ci-annotations` screens
  with the same content and actions, the child glyph (`childStatus`), the
  board's CI words (`card`), outcome cards at the transcript end, "Watch
  CI for…" in the menu.

**`model/ci.js`** (V; `native/ci.js`, V's too, builds on exactly this):

```js
export function createCI(app) → {
  take(ev),                       // stream 'ci' events (and 'run' for a root with watches)
  load(root, {fresh}) → Promise<CIView>,   // one request in flight per root
  view(root) → CIView | null,
  chip(root) → {text, tone: 'run'|'ok'|'bad'|'warn'|'idle', title} | null,
  rows(root) → [{watch, title, state, urls, runs: [{id, name, event, attempt, state, tone, url, elapsedMs,
                 jobs: [JobRow]}], checks: [CheckRow], statuses: [StatusRow]}],
  child(root, runId) → {tone, title} | null,
  cards(root) → [{key, tone, text, watch, job}],           // each watch's outcome not dismissed (key <watch>:<outcome>)
  dismiss(key),
  log(root, watch, job, {tail, since, until}) → Promise<{text, bytes, from, complete, truncated, url, inProgress}>,
  annotations(root, watch, check) → Promise<[{path, startLine, endLine, level, title, message}]>,
  watch(root, {scm, repo, ref, pr}), unwatch(root, wid), rerun(root, {watch, runId, failedOnly}),
  live(root, on),                 // the dock shows root: refresh with fresh=1 every 15 s while anything is pending
}
// JobRow: {id, name, status, conclusion, tone, url, check, annotations, elapsedMs,
//          progress: {done, total, pct, current}, steps: [{n, name, status, conclusion, tone, elapsedMs}]}
export function chipWords(summary, now), jobProgress(job), toneOf(status, conclusion), stripAnsi(s), elapsed(ms)
```

### 13.6 Feature keys

New areas in `model/features.js`: `proj: 'Projects — a sandbox, its repos
and task conversations'` and `ci: 'CI — what a conversation pushed, as the
platform's CI reports it'`. "Web" and "native" name the WP that implements
the key in that view.

| Key | Meaning | Web | Native |
|---|---|---|---|
| `proj.entry` | the Projects entry (sidebar; drawer row) | U1 | U2 |
| `proj.list` | the list: yours and team, states | U1 | U2 |
| `proj.new` | new project: provider, repos, sandbox, policy basics | U1 | U2 |
| `proj.upgrade` | "Make this a project…" from a conversation with a sandbox | U2 | U2 |
| `proj.board` | the board columns and task cards | U1 | U2 |
| `proj.task.new` | new task: text, title | U1 | U2 |
| `proj.task.issues` | tasks from issues (batch picker) | U1 | U2 |
| `proj.task.size` | small or big | U1 | U2 |
| `proj.task.agent` | who answers: built-in or a coding agent | U1 | U2 |
| `proj.repos` | repos: add, remove, setup script | U1 | U2 |
| `proj.policy` | every policy key | U1 | U2 |
| `proj.members` | members (unpartitioned, team) | U1 | U2 |
| `proj.status` | sandbox, repos, credentials, jobs, warnings | U1 | U2 |
| `proj.signin` | sign in to the provider (device code for the person only) | U1 | U2 |
| `proj.coordinator` | the coordinator card: open it, message it | U2 | U2 |
| `proj.events` | the project's event feed | U2 | U2 |
| `proj.team` | team board, "Work on this", accepting a definition's changes, stale rows | U2 | U2 |
| `proj.delete` | archive and delete (sandbox keep/delete) | U1 | U2 |
| `top.task.chips` | a task's branch and PR chips | U1 | U2 |
| `top.task.pr` | Open PR | U1 | U2 |
| `chat.task.prep` | the prep and sign-in cards | U1 | U2 |
| `link.project` | `#proj` / `#proj=<id>` and the crumb | U1 | U2 |
| `ci.chip` | the CI chip in the top bar (the Coding agents button's badge natively) | V | V |
| `ci.dock` | the CI section of the right dock (of the Coding agents screen natively) | V | V |
| `ci.jobs` | runs → jobs → steps with live progress | V | V |
| `ci.logs` | job logs: tail, search, follow, live-log link | V | V |
| `ci.annotations` | annotations as `path:line` | V | V |
| `ci.links` | ↗ to runs, jobs, checks, PRs and branches on the platform | V | V |
| `ci.rerun` | Re-run failed (people only) | V | V |
| `ci.watch` | Watch CI for… and unwatch | V | V |
| `ci.cards` | outcome cards in the transcript | V | V |
| `ci.board` | CI chips on the project board and child cards | V (U1 places `ext.card`) | V (U2 places `ext.card`) |

**S0 registers every key above** in `model/features.js` (the two areas
included), each listed for **both** views as not built yet: the lists
`stagedWeb` and `stagedNative` there, spread into `DIFFERENCES` — grouped
by stage (web: U1's keys, then U2's, then V's; native: U2's, then V's), one
comment line between groups so each WP's edits stay in its own hunk. A WP
that implements a key in a view adds the view's `IMPLEMENTS` line
(`web-features.js` / `native-features.js`; under its own slot comment,
which S0 placed at the end of each: on the web U1's, U2's, then V's;
natively U2's — every native `proj.*`, `top.task.*`, `chat.task.prep` and
`link.project` key — then V's, as U1 implements no native key) and deletes the
key from that view's staged list — nothing else in those files; comment
lines stay (the integrator removes the slots and groups with the lists). When the lists are
empty the integrator deletes them and `STAGED` (the landing gate, §16.6).
`hack/agent-template-features.test.mjs` enforces the rest (a key both
implemented and staged is "stale" and fails).

## 14. Go seams

S0 commits these in B and A (types, constants, registration points, no-op
hooks and view seams; nothing changes behaviour until a WP registers or
fills one):

| File | What |
|---|---|
| `B/projects_types.go` | every Projects and CI shape of §6–§13 (`Project`, `ProjectView`, `ProjectStatus`, `ProjectRepo`, `ProjectTask`, `TaskPR`, `ProjectCheckout`, `IssueRef`, `TaskAgent`, `TaskSpec`, `TaskFilter`, `ActOpts`, `TaskView`, `TaskPorts`, `TaskRefs`, `ProjectPark`, `BoardRow`, `taskInput`, `ProjectEvent`, `ProjectJob`, `jobOutcome`, `projectJobFunc`, `ProjectPolicy` and its parts with `defaultProjectPolicy()`, `CISummary`, `CIJobs`, `CIView`, `CIWatchView`, `CIURLs`), their constants (origins, roles, kinds, states, columns, sources, event kinds, job kinds, credential states, CI sources, `evProject`, `evCI`, `pendKindProject`), `coordSessionKey` |
| `B/projects_seams.go` | the registration points and hooks (§14.1) — the only place they are declared |
| `B/scm_types.go` | the scm contract, protocol 1, as the agent speaks it: `scmHello` and its parts, `scmTokenReq`, `scmSecret` (prints and marshals as `"[secret]"` under `%v`, `%+v`, `%#v` and JSON; `Reveal` is the one way to the value), `scmToken` (its token an `scmSecret`; its `String()` never shows it), `scmRevokeReq`, `scmSignin`, `scmSigninState`, `scmPage[T]`, `scmRepo`, `scmQuery`, `scmRef`, `scmActor`, `scmPullReq`, `scmPull`, `scmPullPatch`, `scmComment`, `scmChecks`, `scmCounts`, `scmWorkflowRun`, `scmJob`, `scmStep`, `scmCheck`, `scmStatus`, `scmJobLog` (with `from`, §4.9), `scmAnnotation`, `scmRerunReq`, `scmRerun`, `scmIssue`, `scmPollReq`/`Item`/`Resp`/`Result`, `scmSubscription`, `scmEvent` and its parts, the caps, kinds and refusals, `scmError` with `scmRefused`, and `scmAPI` with `scmFor`/`scmBound` (§14.3) |
| `B/llm.go` | `Config.Project *ProjectRef` (`json:"project,omitempty"`) |
| `B/handlers.go` | `handlePutConfig` strips `Project` with `Engine` and `Harness` |
| `B/routes.go` | `routes()` mounts every `routeTables` entry |
| `B/channels.go` | `adapterRoutes()` mounts every `adapterRouteTables` entry with `adapterGuard` |
| `B/migrate.go` | `addFeatureSchemas()` runs every `schemaAdds` entry, then sets `DB.features` |
| `B/db.go` | `openDB` calls `addFeatureSchemas()` after `migrate()` — on the agent's own database only; team (`migrateTeamRuns`) runs `migrate()` alone. `DB.features` (copied into every `Tx` view) says the feature schemas are in: `addFeatureSchemas` sets it last, so it is false while `migrate()` rewrites legacy rows and always on team |
| `B/projects_seams_test.go` | the strip, the Config round trip, the registration points, adapter routes behind `adapterGuard`, feature schemas never on team, `scmSecret` never shown, the policy's JSON names (§15.2 S0) |
| `A/web-ext.js` | the web seams `side`, `page`, `crumb`, `dock`, `card`, `childStatus`, `sbx`, documented in its header (§13.2); their call sites are placed by the WP that draws there |
| `A/native/ext.js` | the native seams `drawer`, `dock`, `card`, `childStatus` (§13.3); call sites as above |
| `A/model/features.js` | the `proj` and `ci` areas and keys, staged for both views, one comment line per WP's group (§13.6) |
| `A/web-features.js`, `A/native-features.js` | slot comments at the end under which each WP adds its `IMPLEMENTS` lines (§13.6): on the web U1's, U2's and V's; natively U2's and V's |

### 14.1 Registration points and hooks (verbatim from projects_seams.go)

The file without its package clause and imports:

```go
// projects_seams.go — the registration points and hooks of Projects and CI
// (projects_types.go has the shapes): lists each part appends to from its
// init() (routeTables, adapterRouteTables and schemaAdds are wired; the
// others' call sites land with the part that owns the moment, one line
// each), and hooks one part fills from its init(). Each default does
// nothing, or refuses where nothing would be wrong, so every part compiles
// and runs before the one that fills it is in. A list runs its entries in
// registration order — never rely on the order.
//
// Hosted conversations (a person's partition driving them over team,
// hosted_engine.go) never reach a hook: every call site skips a run whose id
// is hostedID (team_runs.go), and schemaAdds never run on team
// (addFeatureSchemas), so nothing of a project or a CI watch is ever written
// to team.

// errNotInBuild: the part that does this isn't in this build.
var errNotInBuild = errors.New("not in this build")

var (
	// routeTables are each part's route tables: routes() mounts them like
	// routeTable() (the same agentRole, hostedRoute, guard, partitionRoute
	// chain), so no part edits routes.go.
	routeTables []func() []routeDef
	// adapterRouteTables are each part's /adapter/* routes: adapterRoutes()
	// (channels.go) mounts them with adapterGuard — the channel role a bound
	// adapter or provider holds, never the admin chain routeTables get (a
	// bound provider isn't admin). The handler still checks which tile is
	// calling (X-XBin-From).
	adapterRouteTables []func() map[string]http.HandlerFunc
	// schemaAdds are each part's tables: openDB runs them after migrate()
	// (addFeatureSchemas, migrate.go) — on the agent's own database only,
	// never on team. Additive and idempotent; none may rely on another's.
	schemaAdds []func(d *DB) error
	// turnEndHooks run inside the transaction that ends any run's turn —
	// top-level or a subagent, built-in or a coding agent (endTurnTx,
	// endHarnessTurnTx) — or stops it (stopRun, a coding agent's cancel in
	// harness_pass.go); each entry filters the runs it cares about
	// (run.ParentID, the origin). why is one word for every site:
	// "answered", "finished", "incomplete", "error", "canceled" or
	// "interrupted" (the sites map their own to these); outcome is the link
	// outcome's words, result its text. Database only; anything slower goes
	// in t.AfterCommit. (Projects: the task's state and events, the refs
	// check; CI: pushed branches.)
	turnEndHooks []func(t *DB, run *Run, why, outcome, result string)
	// runStatusHooks run whenever a run's status is written (DB.setStatus,
	// DB.setStatusOnly), in the same handle — a park waiting for a person
	// (an approval, ask_user, a coding agent's question or sign-in), a
	// cancel, a wake — so no move to or from waiting_input is missed; never
	// where the handle's features flag is off (migrate() rewriting a legacy
	// database's rows before any feature table exists; team). Keep an entry
	// to an indexed lookup; anything more in t.AfterCommit.
	runStatusHooks []func(t *DB, runID int64, status string)
	// runViewHooks add keys to a run's answers (viewWith for GET
	// /runs/{id}/view, handleGetRun for GET /runs/{id}): project,
	// projectTask (Projects), ci (the CI view's summary and canWatch). view
	// is the answer being built; w is who asks (what only they may see, a
	// device code, goes in only for them).
	runViewHooks []func(t *DB, w who, run *Run, view map[string]any)
	// runDeletedHooks run inside deleteOneRun's transaction for every run
	// deleted.
	runDeletedHooks []func(t *DB, runID int64) error
	// projectRefsHooks run when a task's branch is pushed, its head moves or
	// a PR opens or changes (in the transaction that recorded it): scm
	// subscriptions and routing, CI watches.
	projectRefsHooks []func(t *DB, p *Project, k *ProjectTask)
	// projectEventHooks see every project event as it is written (in its
	// transaction; use t.AfterCommit): needs and pushes.
	projectEventHooks []func(t *DB, p *Project, ev *ProjectEvent)
	// taskChangedHooks run when a task's row or its run's state changes
	// (what: "created" | "state" | "ws" | "phase" | "prs" | "ci" | "deleted"):
	// the team board's push.
	taskChangedHooks []func(t *DB, p *Project, k *ProjectTask, what string)
	// scmEventHooks see every scm event the home handles (deduped, in the
	// home that owns it), before projects route it: the CI watches.
	scmEventHooks []func(t *DB, ev *scmEvent)
	// ownerLoops are background loops of the engine owner: started beside
	// the project worker at takeover, their ctx cancelled at BeginShutdown.
	// They never hold the engine or wake it (hasWork doesn't count them): a
	// loop works while the engine runs anyway. (CI: refreshing live watches.)
	ownerLoops []func(ctx context.Context, e *Engine)
)

// projectJobKinds is the project worker's table: each part registers its
// kinds from init(). A job of a kind nobody registered fails "not in this
// build".
var projectJobKinds = map[string]projectJobFunc{}

// The scm client and credentials (K, scm*.go): who may name a repo for the
// bot; write, refresh and scrub a project's token in a sandbox, behind
// scmCredWhy.
var (
	// scmBotAllowed says whether w may name repo for the bot — where the
	// home's identity is the bot (the global instance, an unpartitioned
	// agent): creating a project, adding a repo, making a conversation a
	// project, a CI watch. The agent's managers may; others only as the
	// agent's scm bot rule (scmBotRule, the bot home's setting
	// scm_bot_rule) allows. Not consulted in a person's partition, where
	// the person's own identity decides.
	scmBotAllowed = func(w who, repo string) bool { return w.manager() }
	// scmEnsureCreds makes sure ref holds a live credential for p (and k,
	// when it is a task's own sandbox) with at least minLeft to run. It
	// calls the provider and the sandbox: never on the engine's path (the
	// gate queues a creds job instead).
	scmEnsureCreds = func(ctx context.Context, p *Project, k *ProjectTask, ref string, minLeft time.Duration) error {
		return nil
	}
	// scmScrubCreds empties p's credential files in ref ("" = every sandbox
	// p wrote them to) and revokes what the provider handed out; why is
	// recorded (share, stop, archive, delete, repo-removed, forget, left).
	scmScrubCreds = func(ctx context.Context, p *Project, ref, why string) error { return nil }
	// scmProjectEnv is what a project's execs and coding agents get from
	// the credentials (GH_CONFIG_DIR); P1's projectEnv merges it with the
	// task's own (ports, branch).
	scmProjectEnv = func(p *Project, home string) map[string]string { return nil }
	// scmGitConfig is the git config a base repo (or a clone) of p gets for
	// host — key/value pairs, in order (the credential helper lines); home
	// is the sandbox's home.
	scmGitConfig = func(p *Project, host, home string) [][2]string { return nil }
	// scmRedact masks scm tokens — their shapes and every live one — in
	// text a row, a log or a job's output keeps.
	scmRedact = func(s string) string { return s }
	// scmPendingSignin is the device-flow sign-in under way for user at
	// provider scm, as the last 409 signin from the provider said (nil:
	// none): the workspace gate's park shows it to that person only.
	scmPendingSignin = func(user, scm string) *scmSignin { return nil }
	// scmCredsDue says, from the database alone, whether k's credential (or
	// p's, for no task) is missing or due for a refresh within 10 min: the
	// workspace gate then queues a creds job and parks.
	scmCredsDue = func(t *DB, p *Project, k *ProjectTask) bool { return false }
)

// Projects (P1), for the parts that build on them in parallel.
var (
	// projectsInSandbox lists the active projects whose workspace (or a
	// task's fork) is sandbox ref: whose credentials to scrub.
	projectsInSandbox = func(ref string) []*Project { return nil }
	// projectReposOf is project pid's repos.
	projectReposOf = func(pid int64) []ProjectRepo { return nil }
	// projectLevelOf is user's level on p's ACL (owner, participant,
	// viewer; lvNone: none, or Projects isn't in): the credential gate's
	// "every user of the sandbox may act in the project".
	projectLevelOf = func(p *Project, user string) level { return lvNone }
	// projectRefsCheck queues task n's refs job (its pushed branch and open
	// PRs read again): a PR opened outside the task's turns.
	projectRefsCheck = func(t *DB, pid, n int64) {}
	// projectTaskChanged says task n of project pid changed (what as
	// taskChangedHooks'): it emits the project stream event and runs
	// taskChangedHooks — what a part that changes a task's derived state
	// (its CI) calls.
	projectTaskChanged = func(t *DB, pid, n int64, what string) {}
)

// Big tasks, upgrades and pull requests (P2).
var (
	// provisionFork makes k's own sandbox from p's fork-base snapshot (or a
	// fresh one) and answers its ref; errNotInBuild runs a big task in the
	// project's sandbox instead.
	provisionFork = func(ctx context.Context, p *Project, k *ProjectTask) (string, error) { return "", errNotInBuild }
	// projectRested: k's run came to rest — auto-PR, if the policy says so.
	projectRested = func(t *DB, p *Project, k *ProjectTask) {}
)

// The coordinator (C).
var (
	// projectWakeHook says whether a coordinator run has an undelivered
	// event that should wake it (the idle-wake checks in actor.go call it).
	projectWakeHook = func(d *DB, run *Run) bool { return false }
	// projectDeliverHook is a coordinator's undelivered project events as
	// one message's text, at a step boundary (deliverBoundary, beside
	// deliverNotices); the caller writes the message and then calls mark
	// with its id, which marks the events delivered. "": nothing to deliver.
	projectDeliverHook = func(t *DB, run *Run) (text string, mark func(msgID int64)) { return "", nil }
	// projectCoordPrompt is a coordinator's # Project block.
	projectCoordPrompt = func(p *Project, run *Run) string { return "" }
)

// The CI view (V): a task's CI watches' aggregate (TaskView.ci, the board
// row's ci); nil: none.
var taskCISummary = func(runID int64) *CISummary { return nil }
```

### 14.2 Who places, registers or fills each

| Seam | Call site placed by | Registered / filled by |
|---|---|---|
| `routeTables` | S0 (routes.go) | P1, K, P2, C, E, T, V — one `routeTables = append(routeTables, xxxRoutes)` in each WP's own file |
| `adapterRouteTables` | S0 (channels.go `adapterRoutes`) | E (`POST /adapter/scm/event`, §11.1) |
| `schemaAdds` | S0 (migrate.go `addFeatureSchemas`, called by openDB) | P1 (`addProjectSchema`), K (`project_creds`), E, T, V, C |
| `turnEndHooks` | P1: one line at each of four sites, after the `ParentID` branch so it runs for every run (hosted ids skipped): `stopRun` (B/actor.go:311, after the `ParentID` branch at :337–343), `endTurnTx` (:753, after the `settleOwnLink` block at :785–790), `endHarnessTurnTx` (B/harness_engine.go:1272, after the if/else at :1316–1323), the coding agent's cancel (B/harness_pass.go:564–569); each site maps its own `why` to the seam's six words | P1 (`projectTurnEnd`, filters `ParentID == 0` and origin `project`), V (`ciTurnEnd`) |
| `runStatusHooks` | P1: one line each in `DB.setStatus` and `DB.setStatusOnly` (B/db.go:384, :392), after the `UPDATE` succeeds, skipped for `hostedID` and while `!d.features` (§14: `migrate()` calls both on a legacy database — B/migrate.go:525–565 — before `addFeatureSchemas` made any project table, and team never has them) | P1 (pump, `hold_park`, the `task.state` wake, `onTaskChange` "state") |
| `runViewHooks` | P1: one line each in `viewWith` (B/stream.go:156, GET /runs/{id}/view) and `handleGetRun` (B/handlers.go:216), skipped for `hostedID` | P1 (`project`, `projectTask`, the pending sign-in for its person), V (`ci`) |
| `runDeletedHooks` | P1: one line in `deleteOneRun` (B/db.go:442) | P1, V, C |
| `projectRefsHooks` | P1 (the `refs` job, §8.3), P2 (the `pr` job) | E (subscriptions, `project_refs`), V (task watches) |
| `projectEventHooks` | P1 (`addProjectEvent`) | C (needs, pushes), T (board events) |
| `taskChangedHooks` | P1 (`onTaskChange`) | T (board push) |
| `projectTaskChanged` | V (a task watch's summary changed, what `"ci"`) | P1 (`onTaskChange`: the `project` event, change `"task"`, and `taskChangedHooks`) |
| `scmEventHooks` | E (its handler) | V |
| `ownerLoops` | P1: at takeover beside the project worker (B/engine.go:172), each with a context `BeginShutdown` (:432) cancels; not counted in `hasWork` or the hold — a loop never keeps the engine up or wakes it | V (the CI refresher, §7.6) |
| `projectJobKinds` (projects_seams.go) | P1's worker | P1 (sandbox, repo, fetch, prepare, setup, bind, refs, cleanup), K (creds, scrub), P2 (snapshot, fork, pr), E (poll, subscribe) |
| `scmBotAllowed` | P1 (`POST /projects`, `POST /projects/{pid}/repos`), P2 (`POST /runs/{id}/project`, §8.10), V (`POST /runs/{id}/ci/watch`, a pushed watch at a bot home, §7.6), K (`GET /projects/scm/repos`) | K (the default lets managers only) |
| `scmEnsureCreds`, `scmScrubCreds`, `scmProjectEnv`, `scmGitConfig`, `scmRedact` | P1 (worker, env via `projectEnv`), K (sandbox routes) | K |
| `scmPendingSignin` | P1 (the gate's park and its `runViewHooks` entry, §8.6, §9.8) | K |
| `scmCredsDue` | P1 (the gate, §8.6: true → a `creds` job and a park) | K (from `project_creds`, §9.5) |
| `scmFor`, `scmBound` (scm_types.go) | everyone | K |
| `projectsInSandbox`, `projectReposOf` | K | P1 |
| `projectLevelOf` | K (the bot row of `scmCredWhy`, §9.2) | P1 (`loadProjectACL`, §6.13) |
| `projectRefsCheck` | E (`pull.opened`/`reopened` on a task's branch, §11.3) | P1 (queues the task's `refs` job, §8.3) |
| `provisionFork`, `projectRested` | P1 (prepare of a big task; turn end) | P2 |
| `projectWakeHook`, `projectCoordPrompt` | P1 (the two idle-wake checks in actor.go, §10.5; context.go) | C (the run's role through `projectRefOf`, §6.12 — never the stored `cfg.Project` alone) |
| `projectDeliverHook` | P1: one line in `deliverBoundary` (B/actor.go:828) beside `deliverNotices` (:893), for a run `projectRefOf(run).isCoordinator()` calls a coordinator (§6.12): write the text as one message, then `mark(msgID)` | C |
| `taskCISummary` | P1 (`TaskView`), T (board rows) | V |


Every call site P1 places for a run (turn end, status, view, delete, wake,
delivery) skips a run whose `hostedID(run.ID)` (B/team_runs.go:32), team's
own `DB.setStatus` included: hosted conversations never reach a hook, and
nothing of a project or a CI watch is written to team (§1, §6).

### 14.3 The client interface (verbatim from scm_types.go)

```go
// scmAPI is one bound provider, as the agent calls it: from a person's
// partition the call reaches that person's partition of a partitioned
// provider (they are the person asking), from the global instance or an
// unpartitioned agent its global instance (no person: bot only). Every
// method answers *scmError for a refusal.
type scmAPI interface {
	Provider() string // the binding's provider path: Project.SCM
	Hello(ctx context.Context) (*scmHello, error)

	Token(ctx context.Context, req scmTokenReq) (*scmToken, error)
	Revoke(ctx context.Context, req scmRevokeReq) error
	Signin(ctx context.Context) (*scmSigninState, error)      // POST /scm/signin: starts one when needed
	SigninState(ctx context.Context) (*scmSigninState, error) // GET /scm/signin: the state, starting nothing
	SigninPoll(ctx context.Context, pollID string) (*scmSigninState, error)
	Forget(ctx context.Context) error

	Repos(ctx context.Context, q scmQuery) (*scmPage[scmRepo], error)
	Repo(ctx context.Context, repo, as string) (*scmRepo, error)

	PullCreate(ctx context.Context, req scmPullReq) (*scmPull, error)
	Pulls(ctx context.Context, q scmQuery) (*scmPage[scmPull], error)
	Pull(ctx context.Context, repo string, n int, as string) (*scmPull, error)
	PullPatch(ctx context.Context, n int, p scmPullPatch) (*scmPull, error)
	Comments(ctx context.Context, repo string, n int, since int64, as string) (*scmPage[scmComment], error)

	Checks(ctx context.Context, repo, ref, ifNoneMatch, as string) (*scmChecks, error)                              // nil, nil: not modified
	JobLog(ctx context.Context, repo, job string, tailBytes int, since, until int64, as string) (*scmJobLog, error) // until 0: the end
	Annotations(ctx context.Context, repo, check, cursor, as string) (*scmPage[scmAnnotation], error)
	Rerun(ctx context.Context, req scmRerunReq) (*scmRerun, error)

	Issues(ctx context.Context, q scmQuery) (*scmPage[scmIssue], error)
	Issue(ctx context.Context, repo string, n int, comments bool, as string) (*scmIssue, error)

	Poll(ctx context.Context, req scmPollReq) (*scmPollResp, error)
	Subscribe(ctx context.Context, s scmSubscription) (*scmSubscription, error)
	Subscriptions(ctx context.Context) ([]scmSubscription, error)
	Unsubscribe(ctx context.Context, id string) error
}

// errScmUnbound: no provider of that name is bound in the `scm` slot.
var errScmUnbound = &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no scm provider of that name is bound to this agent (bind one in its scm slot)"}

// scmFor is the bound provider named provider (Project.SCM); scmBound lists
// them. The client (K, scm.go) sets both; until then nothing is bound.
var (
	scmFor   = func(provider string) (scmAPI, error) { return nil, errScmUnbound }
	scmBound = func() []string { return nil }
)
```

K's `scm.go` implements it over HTTP; `scm_fake_test.go` (K) is an
`httptest` provider serving §4's routes from in-memory state that every
agent WP's tests use (§15.1).

## 15. Tests and fakes

### 15.1 The fakes

- **The fake sandbox manager** (B/fsb_fake_test.go) runs real `sh` on the
  host in host directories: the git flows of §8 are tested for real
  against a local bare "origin" (`file://` URL, made by the test). Its
  `workdir` and `home` are temp dirs — the tests prove no path assumes
  `/work`.
- **The fake scm provider** (`B/scm_fake_test.go`, K; extended by E and V
  in their own `*_test.go` files through exported helpers, never by
  editing K's file): an `httptest.Server` serving §4's routes from memory —
  hello (caps configurable), tokens (`ghs_` + 40 random chars, or a
  520-char one; records every request), revoke, sign-in (pending → done on
  a test call), repos, pulls (422-style `existing`), comments, checks
  (workflow runs, jobs, steps, checks, statuses settable by the test), job
  logs (`in-progress` for a running job), annotations, rerun, issues, poll
  (ETags), subscriptions; plus `deliver(ev)` that POSTs an event v1 to the
  agent's `/adapter/scm/event` as the provider's tile path. Wired with
  `scmFor`/`scmBound` swapped in the test (restored by `t.Cleanup`).
- **The fake GitHub** (`G/_backend/fakegh_test.go`, G1; G2 adds hook
  helpers): an `httptest.Server` for §5's GitHub surface — `/app`,
  `/app/installations`, `/repos/{o}/{r}/installation`,
  `/app/installations/{id}/access_tokens` (verifies the JWT against the test
  key, records repos and permissions, can mint 520-char tokens),
  `DELETE /installation/token`, `/applications/{cid}/token/scoped`,
  `/applications/{cid}/token`, `/grant` (basic auth),
  `/login/device/code` and `/login/oauth/access_token` (pending,
  `slow_down`, expired, denied; refresh rotation that rejects the old
  token), `/user`, `/user/installations*`, `/installation/repositories`,
  `/repos/{o}/{r}` (+ branch protection), pulls (422 "already exists",
  `mergeable: null`), GraphQL ready/draft, issues and comments, reviews,
  `/commits/{ref}/check-runs`, `/commits/{ref}/status`,
  `/actions/runs?head_sha=`, `/actions/runs/{id}/jobs`,
  `/actions/jobs/{id}/logs` (302 to a second path, 404 while running),
  `/check-runs/{id}/annotations`, `/actions/runs/{id}/rerun-failed-jobs`,
  `/collaborators/{login}/permission`, `/app-manifests/{code}/conversions`,
  `/app/hook/config`, `/app/hook/deliveries`; ETag and 304 everywhere;
  request counters; injected rate limits, SSO blocks and 5xx.
- **Fixtures:** webhook bodies in `G/_backend/testdata/*.json`, each < 20
  KB, trimmed from GitHub's documented examples; no real tokens, logins or
  repos beyond `octocat`/`acme`; no `plans/` pointers anywhere in
  `builtin-templates/` or `docs/`.

### 15.2 Required tests (named; each WP may add more)

| WP | Tests |
|---|---|
| S0 | `TestConfigProjectSeam`, `TestProjectSeamsRegistration`, `TestAdapterRouteTables`, `TestFeatureSchemasNotOnTeam`, `TestSCMSecretNeverShows`, `TestProjectPolicyDefaults`; `hack/agent-template-features.test.mjs` with the staged keys |
| G1 | `TestJWTSignsPKCS1AndPKCS8`; `TestIdentityMatrix`, `TestGlobalNeverTreatsPartitionAsSelf`, `TestPersonModeRefusesOtherPerson`, `TestLegacyBotOnly`, `TestBotForPeoplePolicy`; `TestBotTokenDownScoped`, `TestBotTokenCacheMarginPerConsumer`, `TestReposSpanOwners`, `TestNotInstalled`, `TestPermissionsNarrowOnly`, `TestRevokeByValueAndPurpose`, `TestLongStatelessTokens`, `TestBotReposPolicy`, `TestAllowedAccounts`; `TestSigninDeviceFlow`, `TestSigninExpiredDenied`, `TestSigninDisabled`, `TestTokenWithoutSigninStartsFlow`; `TestRefreshDirectRotates`, `TestEpochCapsExpiry`, `TestBadRefreshNeedsSignin`, `TestScopedViaGlobal`, `TestRelayRefusesOthers`, `TestForgetRevokesGrant`, `TestIdentityRegistrationVerifiedAtGlobal`, `TestIdentityRequiresAppToken` (a PAT or another App's token at `/partition/identity` → 403 `identity`), `TestRelayRevalidatesPolicy` (from the person's frame headers: `/partition/scope` with a permission wider than the preset, `workflows: write` without `allowWorkflows`, or an owner outside `allowedAccounts`, and `/partition/bot-token` outside `botRepos`, each refused at global), `TestRelayNewPartitionWipesOld`; `TestHelloShape`, `TestHelloProtocolRefusal`, `TestUpstreamErrorMapping`, `TestReposPagination`, `TestPullCreateIdempotent`, `TestMergeableUnknown`, `TestReadyForReviewGraphQL`, `TestCommentsTimeline`, `TestChecksCombined` (runs, jobs, steps, checks with `job`, statuses, counts, state), `TestJobLogCompletedOnly` (409 `in-progress` with `url`; tail and `since`), `TestAnnotations`, `TestRerunCapAndIdentity`, `TestConditionalETag`, `TestPollChangedOnly`; `TestSetupManifestState`, `TestSetupIngressCallback`, `TestSetupPasteValidates`, `TestSetupNeedsManager`; `TestNoSecretsInResponsesOrLogs` |
| G2 | `TestWebhookHMAC` (current and previous secret), `TestWebhookDedupe`, `TestOutboxRetryAndTick`, `TestNormalize<Kind>` per fixture (incl. `TestNormalizeWorkflowJobSteps`), `TestProgressKindsOptIn`, `TestOneDeliveryPerConsumer` (`subs`), `TestEventsCursor`, `TestSubscriptionPersonChecks`, `TestDeliveryRechecksAccess` (collaborator permission turned `none`: no further private-repo item, that person's subscriptions on the repo deleted; a member or membership webhook drops the cached access), `TestEventsHealth` |
| P1 | `TestProjectSchemaMigratesTwice`, `TestProjectSchemaOldDB` (a fixture database from before), `TestProjectIDsFrom2to40` (under `setMode` user), `TestProjectAccessMatrix` (`accessFixture`/`callAs`: owner, participant, viewer, view-as, element, nobody, at global and in a partition; and the bot rule at a bot home: a non-manager naming a repo 403, a bot-rule person with a matching glob 201, a kind other than team at a partitioned global 409, kind team elsewhere 409), `TestTaskClassNeverInternal` (`POST /projects`, `PATCH` `policy.taskClass` and `TaskSpec.Class` with an internal class → 409 `class-internal`; a managers-only class from a non-manager → 403; a class edited after the task started — a lane-`private` one gaining `tsInternal` — leaves the task's `classOf` without `tsInternal` or MCP), `TestTaskReposOfProjectOnly` (`tasks/batch`, `TaskSpec.Issue` and `GET /projects/{pid}/issues` naming a repo the project doesn't have → 400, at a bot home and in a partition), `TestProjectRoutesUsePid`, `TestTaskRunsNotChats`, `TestProjectRunBarred` (publish, copy, hosting, moves, PATCH sharing → 409), `TestWorktreeFlow` (repo → prepare → bind, real git, a bare local origin, odd workdir), `TestPrepareIdempotent`, `TestGateParksAndResumes`, `TestGateLetsCancelThrough`, `TestQueueFIFOAndSlots`, `TestWaitingRunHoldsSlot`, `TestHoldParkInput`, `TestTakeoverResumesJobs` (epoch fencing), `TestCleanupRefusesUnpushed`, `TestCleanupForceOwnerOnly`, `TestDeleteRunMarksTask`, `TestProjectPromptStable`, `TestTaskClassAtModelGate`, `TestProjectStreamEvent`, `TestTurnEndHooksEverySite` (a top-level run, a subagent, a coding agent's turn end and its cancel each run `turnEndHooks` once, with the seam's `why` words), `TestRunStatusHooksOnPark` (an approval park, `ask_user` and a coding agent's question reach `runStatusHooks`; a hosted id never does, nor the legacy rows `migrate()` rewrites when a database from before Projects is opened), `TestRefsJobFiresHooks` (a task's branch on the remote, then a moved head, each runs `projectRefsHooks` once; a PR opened in a later turn without a new push, and one opened outside any turn through `projectRefsCheck`, are recorded in `prs`), `TestSigninParksTask`, `TestDeviceCodeOnlyToRequester` (both with `scmEnsureCreds`/`scmPendingSignin` stubbed), `TestHostedRunReachesNoHook` (a hosted run's turn end, status change, view and delete call no hook and write no row to team), `TestProjectRefRederivedAfterOldBinaryRewrite` (§6.15) |
| K | `TestScmHelloCache`, `TestScmRefusalDecode`, `TestCredGateMatrix` (person/bot × private, shared, member-shared, team-visible, another owner's, hosted-used, a fork, a seed clone, not homed, at its own global identity, a team seed; the bot row with `projectLevelOf` stubbed: a member below participant refuses, the default `lvNone` refuses), `TestCredsDue` (`scmCredsDue` from `project_creds` alone), `TestScmBotRule` (`scmBotAllowed` for a manager, a matching rule glob and anyone else; `GET/PUT /projects/scm/bot` manager-only, `PUT` 409 in a person's partition; `GET /projects/scm/repos` filtered), `TestCredFilesMode0600`, `TestGitCredentialFill` (real `git credential fill` through the helper), `TestRefreshRewritesFile`, `TestScrubOnShare`, `TestScrubOnStopDeleteForget`, `TestRedactPatternsAndLive`, `TestSeededTokenNeverStored` (a token printed by a bash job in a plain run, `cat` of the cred file, an error: in no row, log, job output or transcript), `TestPendingSigninKept` (a 409 `signin` fills `scmPendingSignin` per person and provider; done clears it), `TestSigninStateStartsNothing` |
| U1 | `hack/agent-template-projects.test.mjs` (model: list, board columns, task words, prep and sign-in cards, router), `hack/agent-template-features.test.mjs`, `test/projects.mjs` with `test/projects-stub.mjs` (browser, stub routes) |
| E | `TestScmEventCallerMustBeProvider` (mounted through `adapterRouteTables`: a bound provider holding only the `channel` role, not admin, gets 200; another channel-role adapter 403), `TestScmEventDedupe`, `TestHandoffScmToPartition`, `TestScmEventForPidMismatchDropped`, `TestRoutingTable` (each row of §11.3), `TestCIFailureInputCapped`, `TestSupersededShaIgnored`, `TestReviewAssociationFilter`, `TestReviewBatching`, `TestOwnIdentityIgnored`, `TestPollCadence`, `TestPollWebhookSemanticDedupe`, `TestPollDueInUserWake`, `TestSubscriptionLifecycle` |
| C | `TestCoordinatorClassFirewall`, `TestCoordinatorCannotReachInternalTask` (`task_message`, `task_result`, `task_status` refuse a task whose run's class or lane has internal reach), `TestCoordinatorToolsGated` (depth 0, a coordinator by `projectRefOf` only; `scm_pr`, `scm_issues` and `task_create`'s issues form naming a repo outside the project → a tool error, at a bot home and in a partition), `TestToolDescriptionFirstSentences` (the eight pinned), `TestResolverByNumberOnly`, `TestCoordinatorCannotAnswerPark`, `TestCreateLimits`, `TestEventDeliveryAndWakeCoalesced` (incl. a coordinator whose stored config lost `project`, §6.15), `TestNeedsProjectField`, `TestDigestPushes`, `TestSeededTokenNotInTools` (scm_pr's log excerpt), `TestChannelAttach` (if the slice ships) |
| P2 | `TestSnapshotOnlyWhenQuiet`, `TestForkFlow` (fork, prune, repair, fresh creds), `TestForkFallbackFresh`, `TestUpgradeDetect`, `TestUpgradeAdoptsTask1`, `TestUpgradeRefusals` (a repo the bot rule refuses at a bot home → 403; at a partitioned agent's global → 409; a conversation whose class, or a `policy.taskClass`, has internal reach → 409 `class-internal`, one the caller may not use → 403), `TestUpgradeNeverRemovesMain`, `TestAutoPROnRest`, `TestPRJobIdempotent` |
| T | `TestTeamDefinitionAtGlobal`, `TestMembershipCreate`, `TestBoardPushOnlyFromOwnPartition`, `TestBoardOutboxRetry`, `TestBoardRowsResetForNewPartition`, `TestBoardRowSanitized`, `TestSeedHoldsNoCredential`, `TestPersonCredsRefusedInSeedClone`, `TestMembershipSetupNeedsAcceptance` (a changed setup, `policy.as` bot or `reviews.forward` all isn't run until accepted; `GET /memberships/{pid}/pending` shows the accepted and the pending part to the member only; a stale hash → 409), `TestMemberRemovedArchives`, `TestTeamCoordinatorSeesBoard` |
| V | `TestCIWatchSchema` (twice, old DB), `TestPushedBranchDetection` (real git: reflog `update by push`, an older push ignored, a non-scm host ignored, a hosted run makes no watch), `TestPushedBranchDetectionChild` (a coding agent child pushes after its parent's turn ended: the watch has `run_id` the child, `root_run` the root), `TestCIAggregate`, `TestCIProgressEventsPatchSnapshot`, `TestCIFreshCoalesced`, `TestCILogRedactedAndStripped`, `TestCILogInProgress`, `TestCIAnnotationsRedacted`, `TestCIRerunPersonOnly`, `TestCIWatchBotRule` (at a bot home neither a manual nor a pushed watch is made for an unnamed repo, even from a faked remote and reflog), `TestCIIdsFromSnapshot` (a log, annotations or rerun naming an id outside the snapshot → 404), `TestCIWatchLimits`, `TestCIBackgroundRefresh` (the `ownerLoops` entry re-reads a pending watch with no event at the §7.6 cadence and stops with the context), `TestCIOutcomeCardedOnce`, `TestTaskCISummary`; `hack/agent-template-ci.test.mjs` (model/ci.js: chip words, progress, tones, ANSI strip, cards and dismissal), `hack/agent-template-native-ci.test.mjs` (native/ci.js: the dock section, the child glyph, the board words, the `ci-job` screen); `test/ci.mjs` with a stub (the chip, the dock tab, a job's steps, the log viewer's search, Open live log) |
| U2 | `hack/agent-template-native-projects.test.mjs`; the features test with its staged keys gone |
| Gate 1 (the lead, on `projects-scm` after merging wave 1) | `TestSeededTokenNeverStoredTask` (the fake provider's token printed by a task's bash: in no row, log, event, job output or transcript) |

### 15.3 Commands

**Nothing that takes more than 1–2 minutes belongs on a WP's iteration
path** (the owner, 2026-10-04): a WP runs targeted checks only, and the
full `-race` suites and `make check` run once per gate, by the lead, on
`projects-scm` after the wave's merges. A WP never runs a tile-check
without `-run`, a `-race` over a whole tile, or `make check`; a targeted
run over ~2 minutes gets narrowed.

- Agent WPs: `TILE_TEST_FLAGS="-count=1 -v -run TestA|TestB" hack/tile-check.sh agent`
  — the pattern **unquoted and without spaces**: tile-check.sh
  word-splits `TILE_TEST_FLAGS` without re-parsing quotes, so `-run 'X'`
  matches nothing and passes having run no test (`-v` shows what ran).
  Add `-race` to a targeted run when the change is about concurrency.
- G1, G2: `TILE_TEST_FLAGS="-count=1 -v -run TestA|TestB"
  hack/tile-check.sh scm-github`; `go test ./internal/builtins/...` when
  the manifest changes (the template catalog loads it; its manifest's
  roles match its guards).
- JS: the one `node --test hack/agent-template-*.test.mjs` file the
  change touches; `make js-check native-check`; the one browser test,
  `PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/ms-playwright node
  builtin-templates/agent/test/<name>.mjs`.
- Everyone: `go test ./internal/docscheck`, `make fmt-check vet`.

### 15.4 What can't run here

No sudo, docker or rootfs on this machine: `test/isolated`, the UI harness
(`hack/ui-harness`), `make integration`'s isolated shards and
`make large-files` (no `/dev/fd`) don't run; `make js-test` has 4 known
failures in `hack/helpers.test.mjs` (no `zstd`). No GitHub App or network
to GitHub: spikes S1–S4 are answered from GitHub's documentation or owed.
**Owed and stated in the landing record:** the Playwright UI harness, the
partitioned end-to-end tests (`test/isolated/partitions_agent_*`), and a
live GitHub App smoke test (sign in, a task, a push, a PR, CI events, a
log, a rerun). Each record says which of its tests ran and which didn't,
and why.

## 16. Work packages

Base: the integration branch `projects-scm` (from
`agent/fairness-settings-ui`, which adds `maxActiveRunsPerUser`). Each WP
builds on `wp/ps-<id>` from `projects-scm` **as it stands at its wave's
start** (wave 1 after S0; wave 2 after gate 1), in its own worktree, and
leaves `plans/projects-scm/records/<WP>.md` (the template is
`records/README.md`). Review fixes go on `wp/ps-<id>-fix`.

### 16.1 The table

| WP | Wave | Delivers | Depends on |
|---|---|---|---|
| **S0** spec and seams | 0 | this spec; `records/README.md`, `records/S0.md`; §14's seams; the staged feature keys; the API.md "## Projects" skeleton | — |
| **G1** scm-github core | 1 | §5 except §5.10: the template, modes, setup (paste; the manifest flow behind spike S1), device flow, identity directory and relay, bot and person tokens, hello, repos, pulls, issues, checks with runs/jobs/steps, job logs, annotations, rerun, poll; the fake GitHub; `docs/scm.md` (§4 as the contract) | §4 |
| **P1** projects core | 1 | §6.1–§6.7, §6.11–§6.14, §7 P1 rows, §8 (but §8.9–§8.10), §8.11, the gate, queue and pump, `project` events, the turn-end, run-status, run-view, run-deleted, wake and delivery call sites (§14.2), refusals | §14 |
| **K** scm client and credentials | 1 | §9; the `scm` slot; `scm.go` (`scmAPI` over HTTP); `/projects/scm*` routes; `project_creds`; the fake scm provider | §4, §14 |
| **U1** web UI | 1 | §13.1–§13.2 U1 rows, §13.4; the U1 web keys of §13.6 | §7, §13 |
| **G2** scm-github events | 2 | §5.10: webhooks, normalisation (incl. `workflow`, `job`, `check`), subscriptions with person checks, outbox, delivery, events health; G's API.md §7 | G1 |
| **E** agent events and polling | 2 | §11; §6.10 `project_refs`, `scm_poll`, `scm_seen`; `/adapter/scm/event`; `handoff/scm`; docs/agent-inbox.md "scm events" | P1, K |
| **C** coordinator | 2 | §10; §6.10 `project_attach` | P1, K |
| **P2** forks, upgrade, PRs | 2 | §8.9, §8.10, the `pr` job and route, auto-PR, cleanup of forks; snapshot client methods | P1, K |
| **T** team projects | 2 | §12; §6.10 `project_board`, `project_board_out`; the T routes | P1, K |
| **V** CI view | 2 | §6.9, §7.6, the V routes and the `ci` event; §13.5 web: `model/ci.js`, `ci-dock.js`, `ci-cards.js`, the dock host in harness-board.js, the child glyph, the board chip (`card`); §13.3/§13.5 native: `native/ci.js` and its call sites; the `ci.*` keys in both views | G1's checks shapes (§4, frozen here), P1, K |
| **U2** native UI and web follow-ups | 2 | §13.3's U2 rows; web surfaces for C, E, P2, T (`proj.coordinator`, `proj.events`, `proj.upgrade` through `sbx`, `proj.team`); every native key but `ci.*` (V's); the staged lists emptied of its keys | U1 |

### 16.2 Files

New files are the WP's own; "edits" are bounded as stated. A WP edits
nothing else — a need beyond this goes in its record as a deviation, and
the lead decides.

| WP | New files | Edits (bounded) | Never |
|---|---|---|---|
| G1 | `builtin-templates/scm-github/**` (but G2's files), `docs/scm.md` | `docs/index.md` (one link), `docs/overview/11-interfaces.md` (the `scm` service), `docs/overview/16-extending.md` (a mention), `docs/partitions.md` §Providers (a paragraph) and §The builtin tiles around a partitioned agent (the opening sentence and one bullet), `workspace-template/AGENTS.md` (one clause in "Install a bundled optional tile") | anything in `builtin-templates/agent/` |
| G2 | `G/_backend/{hook,normalize,subs,outbox,deliver}.go`, their tests, `G/_backend/testdata/*` | `G/_backend/main.go` (mounting), `G/API.md` §7, `docs/scm.md` §Events (fixes only) | the agent |
| P1 | `B/project_{store,routes,tasks,gate,worker,steps,prompt,events,queue}.go` and tests (each ≤ 800 lines) | one-line hooks: `B/actor.go` (gate; `turnEndHooks` ×2; wake ×2 via `projectWakeHook`; delivery via `projectDeliverHook` in `deliverBoundary`; `acquireLLM`; `pendingState.Project`), `B/compact.go` (`acquireLLM`), `B/harness_engine.go` (`turnEndHooks`; env), `B/harness_pass.go` (`turnEndHooks`, one line), `B/engine.go` (worker and `ownerLoops` start/stop; `hasWork`), `B/classes.go` (one line in `classState.classOf`, §10.2), `B/owner.go` (`updateHoldLocked`), `B/context.go` (prompt), `B/db.go` (`runDeletedHooks` in `deleteOneRun`; `runStatusHooks` in `setStatus`/`setStatusOnly`), `B/inbox.go` (`handleMessage` hook), `B/sandbox_jobs.go` (env, one line), `B/stream.go` (`runViewHooks` in `viewWith`), `B/conversations.go` (`handleNeeds` case), `B/homes.go`, `B/hosted_move.go`, `B/handlers.go` (409 barred entry points; `runViewHooks` in `handleGetRun`), `B/resume_mode.go` (queue term), `B/partition_routes.go` (one entry), API.md §Projects and tasks, §The workspace | `B/routes.go`, `B/migrate.go`, `B/llm.go`, `B/channels.go` (S0's hunk), `projects_types.go`, `projects_seams.go`, `scm_types.go` (S0's; a needed change is a deviation) |
| K | `B/scm.go`, `B/scm_routes.go`, `B/scm_creds.go`, `B/scm_gate.go`, `B/scm_fake_test.go` and tests | `B/harness_redact.go` (patterns, live set), `B/db.go` (`addMessage`/`rewriteMessage`: one line each), `B/sandbox_routes.go` (scrub triggers, one line each), agent `xbin.json` (the `scm` slot), API.md §scm providers and credentials | P1's files, `B/harness_engine.go` and `B/sandbox_jobs.go` (P1's `projectEnv` lines carry `scmProjectEnv`, §8.5) |
| U1 | `A/model/project-api.js`, `A/model/projects.js`, `A/model/project-task.js`, `A/projects.js`, `A/project-new.js`, `A/project-settings.js`, `A/project-chips.js`, `A/project-web.js`, `A/test/projects.mjs`, `A/test/projects-stub.mjs`, `hack/agent-template-projects.test.mjs` | `A/agent.js` (≤ 12 lines, §13.2), `A/index.html` (`#sideext`), `A/model/app.js`, `A/model/router.js`, `A/web-features.js` + `A/model/features.js` (staged lists only), API.md §Projects in the UI | `A/test/backend.mjs` (661 lines: stub in its own file), `A/web-ext.js` (S0's) |
| G2 | (above) | | |
| E | `B/scm_{events,handoff,router,poll,subs}.go` and tests | `B/handoff.go` (the topic const), `B/resume_mode.go` (its own hunk), `docs/agent-inbox.md`, API.md §scm events and polling | P1's files, `B/channels.go` |
| C | `B/projects_coord_{tools,deliver,resolve,push,attach}.go` and tests | `B/tools.go` (specs and dispatch), `B/tooldesc_test.go` (eight entries), `B/needs_push.go`, `B/conversations.go` (the `project` field), `B/channels.go` (attach check), API.md §The coordinator | `B/actor.go` (P1 placed `projectWakeHook` and `projectDeliverHook`) |
| P2 | `B/project_{sandbox,upgrade,pr}.go`, `B/sandbox_snapshots.go` and tests | `B/sandbox_client.go` (snapshot methods), API.md §Big tasks, upgrades and pull requests | P1's files beyond registering |
| T | `B/project_team*.go` and tests | `B/partition_routes.go` (entries only), API.md §Team projects | |
| V | `B/ci_{store,watch,detect,routes,events}.go` and tests, `A/model/ci.js`, `A/ci-dock.js`, `A/ci-cards.js`, `A/native/ci.js`, `hack/agent-template-ci.test.mjs`, `hack/agent-template-native-ci.test.mjs`, `A/test/ci.mjs`, `A/test/ci-stub.mjs` | `A/harness-board.js` (≤ 30 lines: the dock host), `A/harness-child.js` (1 line: `ext.childStatus(r)`), `A/harness-web.js` (2 imports), `A/native/harness-board.js` (≤ 6 lines: the `ext.dock(v)` sections and the toolbar badge), `A/native/harness-child.js` (1 line: `ext.childStatus(r)`), `A/native/harness-all.js` (1 import, a CI slot), `A/model/app.js` (2 lines), `A/web-features.js` + `A/native-features.js` (its slot) + `A/model/features.js` (its staged groups only), API.md §CI in the conversation | `A/agent.js`, `A/web-ext.js` and `A/native/ext.js` (S0's), U1's and U2's files (the board calls `ext.card`) |
| U2 | `A/native/project-all.js`, `A/native/projects.js`, `A/native/project-task.js`, `hack/agent-template-native-projects.test.mjs` | `A/native.js` (1 import), `A/native/convs.js` (1 line), `A/sandboxes.js` (2 lines: `ext.sbx`), `A/native-features.js` + `A/web-features.js` (its slot) + `A/model/features.js` (its staged groups only), `A/projects.js`, `A/project-new.js` (its sections), API.md §Projects in the UI (native) | `A/model/ci.js`, `A/native/ci.js` (V's), `A/web-ext.js` and `A/native/ext.js` (S0's) |

### 16.3 Merge contention

| File | Who | How it merges |
|---|---|---|
| `B/routes.go`, `B/migrate.go`, `B/llm.go`, `B/projects_types.go`, `B/projects_seams.go`, `B/scm_types.go`, `A/web-ext.js`, `A/native/ext.js` | S0 only | nobody else edits them (registration, hooks and seams instead) |
| `B/actor.go`, `B/harness_engine.go`, `B/harness_pass.go`, `B/engine.go`, `B/owner.go`, `B/compact.go`, `B/context.go`, `B/inbox.go`, `B/stream.go`, `B/classes.go` | P1 only | one-line hooks |
| `B/db.go` | S0 (`openDB`'s `addFeatureSchemas` call, `DB.features` and its copy in `Tx`), then P1 (`deleteOneRun`, `setStatus`, `setStatusOnly`) and K (`addMessage`, `rewriteMessage`) | separate functions |
| `B/channels.go` | S0 (`adapterRoutes`), then C (the attach check in `channelDeliver`) | separate functions, wave apart |
| `B/handlers.go` | S0 (`handlePutConfig`), then P1 (barred entry points, `handleGetRun`) | separate functions, wave apart |
| `B/resume_mode.go` | P1, then E | wave apart |
| `B/partition_routes.go` | P1, then T | entries only, wave apart |
| `B/conversations.go` | P1 (`handleNeeds` case), then C (field) | wave apart |
| agent `API.md` | each WP its own `###` subsection | S0's skeleton keeps them apart |
| `A/model/features.js`, `A/web-features.js`, `A/native-features.js` | U1, then V and U2 in parallel | per-WP staged groups and slot comments (§13.6) |
| `A/native/harness-all.js`, `A/harness-web.js` | V | its own CI slot |
| `A/model/app.js` | U1, then V | wave apart |
| `A/projects.js`, `A/project-new.js` | U1, then U2 | wave apart |
| `docs/changelog.md`, `plans/DECISIONS.md` | the integrator only | from the records |

### 16.4 Verification per WP

Every WP runs, and pastes the results in its record (§15.3's budget: no
command over ~2 minutes; the record says "full `-race` suite and `make
check`: at the gate" for the rest):

```sh
go test ./internal/docscheck
make fmt-check vet
# agent WPs (P1 K E C P2 T V): §15.2's tests for the WP, by name
TILE_TEST_FLAGS="-count=1 -v -run TestA|TestB" hack/tile-check.sh agent
# G1, G2:
TILE_TEST_FLAGS="-count=1 -v -run TestA|TestB" hack/tile-check.sh scm-github && go test ./internal/builtins/...
# UI WPs (U1 V U2): its own node and browser tests
node --test hack/agent-template-<its>.test.mjs
make js-check native-check
PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/ms-playwright node builtin-templates/agent/test/<its test>.mjs
```

The lead at each gate, on `projects-scm` after merging the wave, once:

```sh
make check
TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent
TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh scm-github && go test ./internal/builtins/...
node --test hack/agent-template-projects*.test.mjs hack/agent-template-ci*.test.mjs hack/agent-template-features.test.mjs hack/agent-template-model.test.mjs
# gate 1 only:
TILE_TEST_FLAGS="-count=1 -run TestSeededTokenNeverStoredTask" hack/tile-check.sh agent
```

### 16.5 Rules

1. Work in your worktree on `wp/ps-<id>`; commit there with
   area-prefixed subjects (`agent template: …`, `scm-github: …`, `docs:
   …`), explanatory bodies and the Co-Authored-By trailer. Never push, tag,
   release or merge; kill only PIDs you started.
2. Build to this spec. Where it is wrong or silent, decide, build, and
   record it in your record's Deviations and in §19 (a dated line) — don't
   stop.
3. Don't edit another WP's files or S0's seams; a needed change is a
   deviation the lead resolves.
4. APIs are additive; stored data keeps working (docs/compat.md):
   idempotent migrations with a migrate-twice and an old-database test.
5. New code in new files, thin call sites; new Go files ≤ 800 lines (the
   repo's convention — `internal/sizebudget` checks Go only under `cmd/`,
   `internal/` and `sdk/`, so a template's Go is held to it by review), JS
   ≤ 900 (`hack/size-budget.txt`, enforced for builtin templates' JS and
   HTML); `agent.js` only as §13.2 allows.
6. Feature keys in both views or staged (§13.6).
7. Never edit `docs/changelog.md` or `plans/DECISIONS.md`; write their text
   in your record.
8. Never write "PR" followed by a number (the docscheck series is taken:
   write "PR #12"); never cite a D-number above D181 — say "the decision
   taken at merge".
9. No `plans/` pointers inside `builtin-templates/` or `docs/`; API.md and
   docs cite served docs (`/docs/scm.md`, docs/partitions.md …).
10. Untrusted text (issues, comments, reviews, CI output) is clipped,
    redacted and framed wherever a model or a page sees it.
11. Run targeted checks only, none over ~2 minutes (§15.3); the full
    `-race` suites and `make check` are the lead's, at the gate (§16.4).
    Say honestly what didn't run (§15.4).

### 16.6 Landing gate

Before `projects-scm` lands on master (the integrator, once):

- no "Described when it lands." is left in API.md §Projects (`grep -n
  'Described when it lands' builtin-templates/agent/API.md` prints
  nothing);
- `stagedWeb`, `stagedNative` and `STAGED` are gone from
  `model/features.js`, with the slot comments and group lines (§13.6), and
  `hack/agent-template-features.test.mjs` passes;
- a slice that slipped (V10's channel attach, a spike's part) has its
  sentences removed from API.md (§The coordinator's "attaching a chat to
  it") and its feature keys deleted, not left staged.

## 17. Docs, changelog and decision drafts

### 17.1 Docs each WP updates (AGENTS.md's "what to update")

| Doc | WP | What |
|---|---|---|
| `docs/scm.md` (new) | G1 (G2: §Events) | §4 as the contract, outline in §4's order plus "Building a provider" |
| `docs/index.md` | G1 | a link to scm.md |
| `docs/overview/11-interfaces.md` | G1 | the `scm` service beside `sandbox-manager` |
| `docs/overview/16-extending.md` | G1 | scm-github as a builtin template |
| `docs/partitions.md` §Providers | G1 | scm-github is a partitioned provider template: each person's sign-in in their partition, the App at global, the relay |
| `docs/agent-inbox.md` | E | "scm events": the route, the body, the caller check, `for`, the partition hand-off |
| `builtin-templates/scm-github/API.md`, `AGENTS.md`, `CLAUDE.md` | G1, G2 | §5.14 |
| `builtin-templates/agent/API.md` "## Projects" | S0 skeleton; each WP its subsection (§7.5) | |
| file headers of `B/project_*.go` | P1 | each says what it holds, as projects_types.go and projects_seams.go do (the agent template has no AGENTS.md) |
| `workspace-template/AGENTS.md` "Install a bundled optional tile" | G1 | a clause: the `scm-github` template (`bx template new scm-github`) offers the `scm` service — repos, credentials, pull requests, CI — docs/scm.md |
| `docs/partitions.md` §The builtin tiles around a partitioned agent | G1 | the opening sentence gains the exception — scm-github is partitioned (each person's sign-in in their partition, the App at global) — and a bullet linking §Providers |
| `docs/changelog.md`, `plans/DECISIONS.md` | **the integrator** | from §17.2–§17.3 and the records |

### 17.2 Changelog draft (the integrator adapts it to what landed)

> **Projects and the scm contract.** A new builtin template, **scm-github**
> (`bx template new scm-github`), offers the new **`scm`** service
> (docs/scm.md, protocol 1): repo credentials for sandboxes — each person's
> own GitHub sign-in, kept in their partition, or the App's bot —
> repos, pull requests, issues, CI (workflow runs, jobs, steps, logs,
> annotations, rerun), polling and events. The agent template gains an
> `scm` slot and **Projects**: a coding sandbox, its repos, a policy, task
> conversations — each with a git worktree per repo, a branch, ports and a
> setup script — that push and open PRs with short-lived, scoped,
> redacted credentials; big tasks in a forked sandbox; a coordinator that
> creates and steers tasks; CI and review events that wake them; team
> projects whose tasks run in each member's own space; and **CI in the
> conversation** — a chip and a section of the coding agents' dock with
> live job progress, logs, annotations and links, for project tasks and for
> any coding session's pushed branches. Web and the native view.
> Additive: an older agent leaves the new tables alone; rolled back, it
> lists no project conversations among its chats (a conversation made into
> a project included) and may drop a run's project field, which the next
> upgrade derives again (API.md §Projects and tasks).

### 17.3 Decision draft (D-number: the next free at merge)

> **D‹next› — Projects in the agent template and the scm contract
> (‹date›).** builtin-templates/scm-github; docs/scm.md; the agent's
> `_backend/project_*.go`, `scm*.go`, `projects_coord_*.go`,
> `project_team*.go`, `ci_*.go`, `projects_types.go`, `projects_seams.go`,
> `scm_types.go`;
> API.md §Projects.
> - **The owner's rulings (2026-10-02).** A separate provider with a
>   repo-hosting contract (credentials required, the rest optional); tile
>   `scm-github`, contract and service `scm`; a builtin template,
>   partitioned (`user`, `global`), bot-only unpartitioned; personal
>   projects in a person's partition with their own sign-in, only in a
>   private sandbox of theirs; team projects: a definition and board at
>   global, each task in its creator's partition (coding agents stay barred
>   at global, D172); worktrees in one sandbox, big tasks fork it;
>   screenshots in PRs deferred (GitHub has no public upload API); CI
>   visible and inspectable in the coding UI (D147's chip and dock), down
>   to steps and logs; "run as root" left to the sandbox track's
>   decision; the whole scope.
> - **Why a contract and a provider:** the provider knows the host and
>   the credentials, the consumer knows who, why and which sandbox; a
>   person's sign-in lives in their partition, the App's keys at global;
>   the agent never sees an App key or a refresh token.
> - **Credentials:** short-lived, scoped to the project's repos, written
>   0600 outside every repo, rotated before expiry, scrubbed on share,
>   stop, archive, delete, fork and Forget, redacted by shape and by
>   value everywhere output is kept; a person's only in their own private
>   sandbox homed in their partition and never one a hosted conversation
>   used.
> - **Authority over the bot:** where a home's identity is the bot (global,
>   unpartitioned), naming a repo takes a manager or the scm bot rule; a
>   partitioned global holds team definitions only; reruns are a person's
>   own, never the bot. A team's seed holds no credential and a sandbox
>   cloned from it takes only the bot; a membership runs the definition's
>   setup and policy only as its member accepted them; a project task's
>   class never has internal reach.
> - **Not chosen:** a run column for tasks (origin `project` instead);
>   project tools as a class toolset (the stored-classes rollback; gated on
>   `Config.Project`); a team coordinator at global; a merge route;
>   polling as the primary event path (webhooks via the provider's
>   outbox, polling as fallback); a second right dock for CI (one dock,
>   sections).
> - **Defaults the owner may veto:** §2.2–§2.3 of the spec, as landed.
> - **Owed:** the UI harness, the partitioned end-to-end tests, a live
>   GitHub App smoke test; spikes S1–S4 as their records say.

## 18. Resolved

Decisions taken while writing this spec, one line each with the reason
(vetoable at the freeze like §2.3):

- **"Run as root" is not added to the sandbox-manager contract.** Upstream
  ACP (schema v1.23.0, protocol 1, which sdk/acp/types.go:5–8 names) has
  no user or privilege field: `CreateTerminalRequest` is `sessionId`,
  `command`, `args`, `env`, `cwd`, `outputByteLimit` and `_meta`
  (extension metadata, not a privilege channel; xbin's `TermCreateParams`,
  sdk/acp/types.go:366, is its subset), and AgTT advertises no client
  terminals (`harnessCaps`, B/harness_engine.go:176–180) — adapters run
  commands themselves as the sandbox's user (docs/sandbox-manager.md
  §Inside a sandbox). The ruling is the sandbox track's (branch
  `sbx/dev-root`, its decision); if ACP gains such a field, mirror it
  then.
- **Registration points instead of edits to `routes.go`/`migrate.go`**
  (`routeTables`, `adapterRouteTables`, `schemaAdds`, wired by S0): ten WPs
  appending to one long line would conflict on every merge; an
  `/adapter/*` route needs `adapterGuard` (a bound provider isn't admin),
  so it has its own list.
- **Hook lists, not single hooks, where several WPs listen**
  (`turnEndHooks`, `runStatusHooks`, `runViewHooks`, `runDeletedHooks`,
  `projectRefsHooks`, `projectEventHooks`, `taskChangedHooks`,
  `scmEventHooks`): V, E, C and T each need the same moments.
- **P1 places every call site in the engine's hot files** (actor.go,
  harness_engine.go, harness_pass.go, db.go, stream.go): `turnEndHooks`
  fire for every run — a subagent's and a coding agent's pushes count —
  and each entry filters; moves to `waiting_input` that end no turn reach
  `runStatusHooks`; K's credentials reach execs through P1's `projectEnv`
  and C's events through `projectDeliverHook`, so K and C edit none of
  those files.
- **Native CI is V's** (`native/ci.js`), not U2's: U2 and V share wave 2
  and neither can import or test against the other's files; `dock`,
  `card` and `childStatus` on both views (S0) keep every CI surface in V's
  own modules, and the board calls `ext.card(task)` instead of importing
  `openCI`.
- **"Make this a project…" on the web is a ▣ popover action** (`sbx` in
  web-ext.js, S0; U2 places the one call in sandboxes.js): the upgrade is
  about the conversation's sandbox, and the top bar has no menu.
- **Feature keys registered by S0, staged for both views** (§13.6): U2 and
  V work in parallel and would otherwise implement keys the other branch
  adds.
- **No `projects.coord_run` column:** the coordinator is the run with
  session key `proj:<pid>:coord:<user>` — one per person per project
  works unpartitioned too, and a deleted coordinator needs no cleanup.
- **Setup scripts live in `project_repos.setup`, not in the policy:** one
  place, per repo.
- **Sign-in routes are `/projects/scm/signin*` (K), not
  `/projects/{pid}/signin`:** a sign-in is the person's at the provider,
  not a project's; the project page and the gate's card use the same
  routes.
- **Team membership routes are `/memberships`**, not under
  `/projects/team/…`: no pattern overlap with `/projects/{pid}/…`.
- **CI is one dock with sections** (harness-board.js becomes the host,
  `ext.dock`): the `.wrap` grid has one dock column (§1); a second `aside`
  would fight for it, and CI must sit inside the coding UI (owner).
- **Progress events (`workflow`, `job`, `check`) are opt-in per
  subscription:** a job alone reports three times; consumers that don't
  show progress shouldn't pay for it.
- **One delivery per consumer and `for`, with `subs`:** task and CI
  subscriptions on the same branch must not double-deliver.
- **Job logs are served only for completed jobs on GitHub** (`in-progress`
  409 with the live-log URL; spike S4): GitHub's documented logs endpoint
  answers only once a job ends.
- **Pushed-branch detection reads git's reflog** (`update by push`, checked
  by hand): exact, one Run, no false positives from branch names; base
  repos get `core.logAllRefUpdates true` so it works there too.
- **`ci_watch` is keyed by the root conversation** with `run_id` for the
  pushing run: the dock is per conversation; child cards still show their
  own.
- **Project events go to one coordinator** (`coord_user`: the task's
  creator, the owner for project-level events): no per-coordinator
  delivery bookkeeping, and a person's coordinator follows their tasks.
- **`/runs/{id}/ci*` on a hosted conversation answer 409** (hostedRoute's
  default): CI needs the home's scm identity, which a hosted conversation
  at global doesn't have.
- **`net` may be narrowed but must include the log hosts**
  (`*.actions.githubusercontent.com`, `*.blob.core.windows.net`): job logs
  are a redirect there.
- **The protection check is a warning by default** (V6) and per repo
  (`project_repos.protected`), read from the provider's `GET /scm/repo`
  `protected`.
- **A project reads only its own repos, at every home** (§7.1, §10.3): the
  bot rule guards naming a repo, so every later read — the issue picker,
  batches, the coordinator's scm tools — is held to the project's repos
  rather than re-checking the rule; the same in a person's partition, so
  a coordinator's reach is the same everywhere.
- **A project run's class loses internal reach in `classOf` itself**
  (§10.2): the stored lane can't hold the line for a lane-`private` class,
  and a check at turn start alone would miss an edit mid-turn.
- **Hooks that read feature tables are off until the feature schemas are
  in** (`DB.features`, §14): `migrate()` writes run statuses on a legacy
  database before any project table exists, and team never has them.
- **The credential gate can't see a sandbox's clone ancestry** (§9.2): the
  manager doesn't report it, so `seed-clone` covers the clones the project
  made; a seed cloned by hand is the person's own act.

## 19. Deviations

What a WP built differently from §1–§18, one dated line each, newest last,
with the record that explains it:

```
- YYYY-MM-DD (<WP>) §x.y: what changed and why — records/<WP>.md
```

- 2026-10-03 (G1) §5.12: new backend files beyond the table — `store.go` (kv and vault behind interfaces, so the tests run every instance in memory), `types.go` (the contract's shapes), `fakegh_ci_test.go`, `harness_test.go` and per-area tests — records/G1.md
- 2026-10-03 (G1) §5.1: no `G/go.sum` — the module requires only the sdk, which the workspace go.work resolves (as llm-gw ships none) — records/G1.md
- 2026-10-03 (G1) §5.4, §5.9: conf `public` also carries `policy.allowedAccounts` (a partition's own reads keep to them) and `rerun` (whether `checks.rerun` is offered, for a partition's hello); state `app` also keeps `permissions`, `events`, `hookUrl`, `botId` — records/G1.md
- 2026-10-03 (G1) §5.7: the parent refreshes when the epoch (its expiry − 60 min) can't cover the request's margin (`max(minTtlSec, 15 min)`), or on a 401 — not at "< 65 min left", which would hand out 5-minute tokens — records/G1.md
- 2026-10-03 (G1) §5.8: a rerun uses the person's parent token in their own partition (the App's `actions: write` ∩ the person's), not a scoped one: `/partition/scope` cuts permissions from the presets, which never hold `actions: write`, and the token is never handed out — records/G1.md
- 2026-10-03 (G1) §4.9: a check's `suite` is GitHub's check-suite id as a string (`"77"`; the spec's `"s_77"` was a placeholder); docs/scm.md calls it opaque — records/G1.md
- 2026-10-03 (G1) §5.6, §5.10: `/partition/subscriptions` (POST, DELETE) are left to G2 with the subscriptions they store; G1 gives G2 seams (`extraRoutes`, `extraCaps`, `eventsHealth`, `tickHooks`, `wipePerson`) and answers 501 `unsupported` on `/scm/subscriptions*` and `/scm/events` until G2 mounts them — records/G1.md
- 2026-10-03 (G1) §5.9: `botRepos` applies to reads as the bot too, not only to bot tokens — a read is as wide as a token — records/G1.md
- 2026-10-03 (G1) §4.9: `GET /scm/repos` with `as: bot` in a person's partition is 403 `identity` (no relay route lists the bot's repos; name a repo instead) — records/G1.md
- 2026-10-03 (G1) §5.6: global mints a fresh bot token on every `/partition/bot-token` (kept in its cache only for Revoke all): the partition caches and revokes them itself, so global must never hand back one the partition revoked — records/G1.md
- 2026-10-03 (G1) §5.5: the manifest's `default_events` leave out `installation` and `installation_repositories` (GitHub sends an App those unasked); without a public host the hook is inactive and `redirect_url` is the App list, whose address the manager pastes (way c); `hook.active` in setup answers means "a URL is set" (GitHub's hook config has no active flag) — records/G1.md
- 2026-10-03 (G1) §4.8: `minTtlSec` is 900 to 3000 (installation tokens live an hour); above is 400 `invalid` — records/G1.md
- 2026-10-03 (G1) §5.11: spikes answered from GitHub's documentation, none live (no App here): S1 — only the form POST and `state` are documented, so all three ways back (a, b, c) ship and Paste stays primary; S2 — a scoped token's fate at its parent's refresh is undocumented: the epoch stays; S3 — `DELETE /installation/token` is documented to revoke the token used, kept best effort; S4 — no public partial-log API: `in-progress` stands. Each is owed a live check — records/G1.md
- 2026-10-04 (G1) §4.7: hello's `limits` gain `maxTtlSec` (3000, additive) — the ceiling above which `POST /scm/token` refuses `minTtlSec` with 400 `invalid`, which a consumer otherwise couldn't learn — records/G1.md
- 2026-10-04 (G1) §4.9: issue `q` is words only — a qualifier-shaped token (`repo:`, `org:`, `-label:`, …), `OR`/`AND`/`NOT`, quotes and parentheses are dropped, and every search hit is kept only when it is in the repo asked about (GitHub ORs `repo:` qualifiers, and the bot's read token covers the whole installation) — records/G1.md
- 2026-10-04 (G1) §4.9: scm-github's `protected` is GitHub's branch flag, absent for a person who is an admin of the repo (whether classic protection enforces admins needs the Administration permission, which the App lacks); rulesets' bypass lists aren't read — records/G1.md
- 2026-10-04 (G1) §5.7: the bot-token LRU (2000) only governs reuse; every token handed out stays in a separate live record until it expires or is revoked (a policy change, a new App, a refresh or eviction ends reuse, never revocability), bounded at 8000 — past that, with nothing expired, a new token is 429 `limit` — records/G1.md
- 2026-10-04 (G1) §5.4, §5.7: conf `public` also carries `tokenGen` (moved by a policy change, Revoke all and a new App) and `policy.botRepos`; a person's partition reuses only tokens of the current generation and re-checks `allowedAccounts`, `botRepos` and `allowWorkflows` before any reuse — records/G1.md
- 2026-10-04 (G1) §5.8: Forget revokes the grant first; refused (anything but GitHub's "already gone" 404/422) it answers that refusal and clears nothing, so it can be retried — records/G1.md
- 2026-10-04 (G1) §5.9: `personTtlMin` is 0 or 50 to 1440 (every allowed `minTtlSec` met); one consumer holds at most 500 live tokens; `checks.rerun` is listed only in a person's partition (403 `identity` elsewhere) — records/G1.md
- 2026-10-04 (G1) §4.9: a job log's `until` before what a provider keeps answers empty `text` from the kept start, `truncated` — records/G1.md
- 2026-10-04 (G1) §5.8: only GitHub's 404 to a revocation is "already gone"; on its 422 (also "the endpoint has been spammed") global asks GitHub about the token and answers 204 only when that check answers 404 (its own 422 is not "gone"; corrected in fix round 4), else 503 `unavailable` — Forget clears nothing — records/G1.md
- 2026-10-04 (G1) §5.10: GitHub's rate resource is read from the API root (a repo named `search` is `core`); GraphQL on a GHES is `https://<host>/api/graphql`, not under `/api/v3` — records/G1.md
- 2026-10-04 (G1) §5.8: Forget refreshes an expired access token first and revokes the grant with the new one; a refused or expired refresh token leaves nothing to revoke (cleared, 204), any other refresh failure is answered and clears nothing — records/G1.md
- 2026-10-04 (P1) §16.2: three more new files (`project_rows.go`, `project_settings.go`, `project_steps_task.go`) keep each Go file under 800 lines — records/P1.md
- 2026-10-04 (P1) §16.2: the 409 on a project run's own sharing is in `conversations.go` (`handlePatchRun`) and `share.go` (`handleAddMember`), where those handlers are; `/copy` needs none (its source is in the shared space, which holds no project run) — records/P1.md
- 2026-10-04 (P1) §6.11: a task whose turn ended without a pull request is `needs-you` (waiting for you); a cancelled one is column `done`, state `cancelled` — records/P1.md
- 2026-10-04 (P1) §8.3, §8.6: the gate ends its own park once the workspace is ready (the run rests, the pass goes on) and `bind` queues no `inboxWake` (one could be left over to start a turn later): it pokes the run; a turn in flight or a resting run with no input is never parked; a workspace change pokes the task's run — records/P1.md
- 2026-10-04 (P1) §8.3: a coding agent's first prompt gets the brief at `bind` (its start row, client id `proj-start:<pid>:<n>`, rewritten); a `{new}` sandbox's request waits in the setting `proj_sbx_new:<pid>`; a refs check an scm event asks for reads the PRs even with one open — records/P1.md
- 2026-10-04 (P1) §8.1: `engine: auto`'s "last coding agent" is that of the person's latest coding-agent conversation — records/P1.md
- 2026-10-04 (P1) §8.5: `projectEnv(run, home)` takes the sandbox's home from its call sites — records/P1.md
- 2026-10-04 (P1) §8.4: a job fails 3 h after it was queued, a sign-in wait after 20 min; §10.5: `userWake` counts a `wake=1` event only for a coordinator that exists — records/P1.md
- 2026-10-04 (P1) §9.6: `projectsInSandbox` answers every project of the sandbox, archived and deleting ones too — records/P1.md
- 2026-10-04 (P1) §6.14: a deleted project's tasks are cleaned up only when its sandbox stays; its subscriptions end through `runDeletedHooks` (no project-deleted seam) — records/P1.md
- 2026-10-04 (P1) §8.3: `bind` binds as the project's owner, not the task's creator (a participant's task failed on the owner's sandbox) — records/P1.md
- 2026-10-04 (P1) §8.7: input delivered but not yet taken up holds its run's slot; cancel and close drop it; the worker pumps the queues once a halt is lifted — records/P1.md
- 2026-10-04 (P1) §6.14: archiving holds the project's workspace jobs (counted as no one's work) and shelves turns waiting for the workspace back into the queue's head; unarchiving restarts the jobs' clock — records/P1.md
- 2026-10-04 (P1) §8.6: `hasWork` and `userWake` don't count the inbox of a task the gate parked for a person (`gateHeldSQL`) — records/P1.md
- 2026-10-04 (P1) §6.13: `/runs/{id}/task/*` also need a participant of the project; a removed member keeps their task conversations (V1) — records/P1.md
- 2026-10-04 (P1) §6.13, §8.3: the project's owner is a `participant` member of every task someone else created (written at create and on every sharing change), so `bind`'s binding as the owner holds at every tool call and the owner reads and messages every task — records/P1.md
- 2026-10-04 (P1) §6.13, §9.2: a shared project (team-visible or with members) has its sandbox to itself and no project joins a sandbox a shared one is in (409 `sandbox-shared` at create, sharing and adding a member): a project's people run commands in its sandbox and would read another project's credentials there, which the bot gate (judging the sandbox's own users) can't see — records/P1.md
- 2026-10-04 (P1) §6.14, §8.6: `hasWork` and `userWake` don't count the inbox of a task shelved for an archived project (a message with files stays there); making the project active again pokes its tasks — records/P1.md
- 2026-10-04 (P1) §6.13, §16.2: on a project run `DELETE /runs/{id}/members/{user}` (leaving too) and `POST /runs/{id}/links` also answer 409 `projectRunBarred`, and `POST /join` lets no one in through a link to one (404): a task's people are the project's, and anyone let in would run commands in the owner's sandbox — records/P1.md
- 2026-10-04 (P1) §6.13, §7: a component's project with tasks isn't shared (409 at `POST /projects` with `share`, a team visibility, `POST /projects/{pid}/members`): its tasks are bound with the component's authority, which no person's conversation admits — records/P1.md
- 2026-10-04 (P1) §8.11: the untrusted frame drops the text's Unicode format characters before defusing its markers, so none hides inside a word — records/P1.md
- 2026-10-04 (P1) §6.13: on a project's run a person's level is held to their level on the project (`projectRunCap` in `rootACL.level`, `acl.go`): someone removed, made a viewer or no longer reached by team visibility reads the tasks they created and does nothing more (403), since talking to a task runs it in the owner's sandbox — records/P1.md
- 2026-10-04 (P1) §6.13, §9.2: a project with a task conversation someone other than its owner made counts as shared for the sandbox-alone rule (409 `sandbox-shared`), since that conversation's creator still reads it — records/P1.md
- 2026-10-04 (P1) §8.11: the untrusted frame removes invisible characters (the default-ignorable set, not only `\p{Cf}`) before it redacts and clips — records/P1.md
- 2026-10-04 (K) §16.2: K's new files also include `B/scm_ensure.go`, `B/scm_scrub.go` and `B/scm_fake_routes_test.go` (and per-area test files) — the 800-line rule — records/K.md
- 2026-10-04 (K) §9.5: the refresh is one `ownerLoops` entry (`scmRefreshLoop`, one timer at the earliest token's instant) re-minting each due token, not a timer per token, and only while a task of the project is at work (an idle project's token lapses; the gate's `scmCredsDue` has the next turn's written first) — records/K.md
- 2026-10-04 (K) §9.2, §9.5: K writes `project_tasks.ws` itself (`signin` on a 409 signin, `failed` on a gate refusal, then back to what it was — remembered in memory, else `ready` when a `bind` job finished and no preparing job is live, else `preparing`) through `projectTaskChanged`, and queues an `inboxWake` for a run the gate parked once a credential is written — records/K.md
- 2026-10-04 (K) §9.4: a key repeated in `scmGitConfig` is applied `--unset-all` + `--add` at its first occurrence and `--add` after (`--replace-all` would fold the empty reset helper and the project's into one) — records/K.md
- 2026-10-04 (K) §9.3: `hosts.yml`'s `oauth_token` and `user` are single-quoted YAML scalars — records/K.md
- 2026-10-04 (K) §9.1: one token per (project, sandbox, host) for all of the project's repos there; repos spanning owners on one host get the provider's 400 `invalid` as the creds job's error (the spec is silent) — records/K.md
- 2026-10-04 (K) §9.6: a share through the agent is refused (502) when a credential there can't be emptied, as `readyForShare` refuses; the scrub also runs after the PATCH (the `stopCredsIn` sibling) — records/K.md
- 2026-10-04 (K) §7.2: `GET /projects/scm/bot` in a person's partition answers that partition's (never read) rule to a manager; only `PUT` is 409 there — records/K.md
- 2026-10-04 (K) §7.2: the sign-in routes (`/projects/scm/signin*`) answer 403 to view-as callers and to components (a person's own, `needUser`) — records/K.md
- 2026-10-04 (K) §9.2: a bot sandbox owned by no person (the agent made it as itself) counts only its members and the team — records/K.md
- 2026-10-04 (K) §16.2: the share trigger in `handlePatchSandbox` is a three-line `if` (gofmt; it refuses the share) plus one line after the PATCH, not one line — records/K.md
- 2026-10-04 (K) §9.7: message rows now also mask `sk-ant-…` keys (`scmRedact` is `redactText`, which applies `tokenShape` too) — records/K.md
- 2026-10-04 (K) §9.5: `scmCredsDue` also counts `project_repos` through the gate's `t` (frozen DDL; no table = none) — a project without repos is never due, and the gate's transaction never waits on `projectReposOf` — records/K.md
- 2026-10-04 (K) §9.6: a scrub that can't empty the files revokes but leaves the row `live` (why set, `refresh_ms` 0), so every later trigger retries and a share stays refused; a scrub also removes a left-over `…tmp` — records/K.md
- 2026-10-04 (K) §9.1: a live token is reused only while its scope (repos, access, permissions) is the project's; a repo added or `workflows` turned on mints again — records/K.md
- 2026-10-04 (K) §9.1: every 5xx whose body names no refusal is `unavailable` (502 included), but 501 stays `unsupported` (the contract's) — records/K.md
- 2026-10-04 (K) §14.1: the `scrub` job's why is the job's `step` when it holds one of §14.1's words, else inferred (repo-removed, archive, delete, left) — records/K.md
- 2026-10-04 (K) §9.2, §9.6: a credential's write and every scrub take a per-sandbox lock — `ensureCreds` asks the gate again after minting and holds the lock through the files and the row; a share's PATCH and a stop's or archive's lifecycle call through the agent hold it too; a provider's `Token` never holds it (a scrub's revocation does: best effort, at most 15 s per credential) — records/K.md
- 2026-10-04 (K) §16.2: `handlePatchSandbox` gains one more line (taking that lock); the stop/archive trigger is one deferred line (`scmScrubOnAction`), no longer an `if` — records/K.md
- 2026-10-04 (K) §9.8, §7.2: a provider's `signin` refusal on a route anyone may call (`GET /projects/scm/repos`) keeps its device code for the partition's own person; anyone else gets the refusal without `signin` — records/K.md
- 2026-10-04 (K) §9.1 (silent): a project with no host (`host` DEFAULT '') takes its credential's host, before minting, from its row there or the provider's hello when it lists one host — records/K.md
- 2026-10-04 (K) §9.1, §9.6: a first write (no live row there) puts its row `live` with `refresh_ms` 0 before the files; a write that fails partway is scrubbed at once under the sandbox's lock (files emptied, `…tmp` removed, purpose revoked) and the row put back as it was (none: removed) — or, unemptied, left `live` and due; a failed refresh leaves the older live row, which covers the files — records/K.md
- 2026-10-04 (K) §4.13, §9.6 (silent), §16.2: a hosted conversation's sandbox use in a person's partition (`sandboxUse`, one line beside `hostedHarnessRefusal`) is refused where a project's credential is live, and noted hosted-used only under the sandbox's lock (`scmHostedUse`), so a credential written first is never readable by a hosted conversation — records/K.md
- 2026-10-04 (U1) §13.2: agent.js gains one line beyond the bullets, `app.on('projects', …)` repainting the sidebar entry and the page (the seams' ctx has no sidebar repaint) — 3 lines net — records/U1.md
- 2026-10-04 (U1) §13.1: `router.parse()` answers `proj` only when the address names it (absent rather than null otherwise), so the other addresses parse as before — records/U1.md
- 2026-10-04 (U1) §13.2: "Open PR" learns that `POST /runs/{id}/task/pr` exists from a `GET` of it (405 mounted, 404 not), once per home, never by POSTing — records/U1.md
- 2026-10-04 (U1) §13.2: the new-project dialog is a form in the Projects page, not a modal — records/U1.md
- 2026-10-04 (U1) §13.1: `projectApi` calls through U1's `projCall` (xbin.fetch at the project's home) instead of `homeApi(homeOf(pid))`, because the kit's `api()` drops the status, `refusal` and body the views need — records/U1.md
- 2026-10-04 (U1) §7.2/§8.1: `TaskSpec` can't ask for the built-in agent where `policy.engine` picks a coding agent; the UI names the built-in agent only where the default is it (owner question) — records/U1.md
- 2026-10-04 (U1) §12.1: U1's new-project form makes a team definition only on the global instance's own page (share always sent, seed optional); creating one from a person's partition is U2's `proj.team` — records/U1.md
- 2026-10-04 (gate 1) §8.6, §9.5: a credential written pokes the task run the gate parked (`scmCredsReady`) instead of queuing an `inboxWake`, as `bind` does since P1 — a wake row can be left over to start a turn later, and a coding agent's pass takes a wake alone, leaving the prompt beside it waiting (`TestHarnessTask` hung after the merge) — records/gate1.md
- 2026-10-04 (gate 1) §15.1: P1's fixture provider (`p1SCM`) keeps its in-memory reads (repos, issues, pulls) and hands its credential methods to the scm client bound to K's fake provider, so Projects' tests mint real credentials through K's gate — records/gate1.md
- 2026-10-04 (V) §13.5: dismissed outcome cards are kept in the person's xbind prefs (`ci-dismissed`), not `localStorage` — a model module touches no DOM storage, and a tile frame keeps none — records/V.md
- 2026-10-04 (V) §13.1, §16.2: `model/app.js` gains three lines (the `createCI` import too); `harness-web.js` its two imports under a "CI" slot comment; `native/harness-child.js` three lines — its card memo keys CI's words, else a later CI result never shows — records/V.md
- 2026-10-04 (V) §7.4: the `ci` event's data also carries `watches: [{id, state, outcome, run, repo, ref}]` (additive), so the latest event alone keeps every outcome card and child glyph — records/V.md
- 2026-10-04 (V) §7.2: `POST /runs/{id}/ci/watch` answers 200 with the live watch when that branch is already watched; `DELETE` of a task's own watch is 409; `GET /runs/{id}/ci` without `fresh` reads a watch never read — records/V.md
- 2026-10-04 (V) §7.3, §7.6: `canWatch` also holds for a conversation with a coding agent child or a watch already; a task's watch is read as its project's identity (`projectAs`); `urls` are built for GitHub only — records/V.md
- 2026-10-04 (V) §6.9, §7.6 (silent): `since` starts again when the head moves (the refresher's cadence counts from that push); past 10 a task's or pushed watch with no pushed one to end isn't made; `current` is dropped while an event's patch is newer than the last read; a subscription is posted again whenever the head moves (its 30 days renewed) — records/V.md
- 2026-10-04 (V) §6.14: a deleted conversation's watches are deleted, rows and all (their snapshots were its CI) — records/V.md
- 2026-10-04 (V) §13.3 (silent): natively the board's `ext.card(task)` answers words (a string) and a refusal to sign in is said in the watch's footer (the inline sign-in card is the web's) — records/V.md
