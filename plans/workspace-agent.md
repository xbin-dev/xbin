# Workspace agent (issue #51) — plan

> Status: **live** — plan only, nothing implemented (rev 2, 2026-11-14, against
> master 83c03745). Rev 2 records the owner's decisions of 2026-11-14
> (section 9) and moves the dedupe into `plans/agent-dedupe.md`. Decisions
> get their D-numbers when the work lands (D223 is the latest today).

## 1. What the issue asks for

One persistent agent per user that works at a high level across the whole
workspace:

- **First-class and xbin-managed**: shared render / ACP / agent loop with the
  agent template (AgTT) and tile agents; an LLM connection set up in xbin;
  a first-class UI element; tools to see what the user sees.
- **Semantics**: like an AgTT conversation (persistent, memories,
  compaction, long-term); carries high-level context only; has the access of
  its user clicking through the UI; starts tile agents following the user's
  preferences; the user sees those tile agents and can talk to them directly;
  it can't read or write tile code, but can ask a sub-agent to read and
  summarise.
- **Later**: org coordinators, MCP.
- **From the comments**: org policy over agents at harness/model level
  ("internal-data org only allows private LLMs"); coding-harness login as a
  workspace primitive (sign in once; tile agents, the workspace agent and
  AgTT use it).

Name: **Workspace agent** in the UI and docs, `wsagent` in code (owner,
2026-11-14). "Coordinator" stays the Projects coordinator inside AgTT.

## 2. What exists today (checked in the code)

| Area | Fact | Consequence |
|---|---|---|
| Tile agents | A tile agent is a terminal session of `kind:"agent"` (`internal/term/agent.go`), an ACP CLI under `bx __agent-host` in the tile's terminal sandbox. API: `/term/sessions…`, `/agent/providers`, `/agent/history` (`internal/server/agentapi.go`). | The thing to invoke already has a full API, web UI, iOS client and push. |
| Who may drive them | Create needs terminal level on the tile; `CanTerminalTile` is false for every principal with `Component != ""` (`internal/auth/auth.go:128`). Tile backends, frames and cron can't create, drive or follow sessions. | A server-side agent can't start a tile agent today. |
| Acting as a user | No server-side "act as user X" credential exists: no delegated tokens, PATs or service accounts. Closest: a person's partition backend calls other tiles as that person, clamped to grant ∩ the person's access (`docs/partitions.md`); view-as is read-only, admin-only and opens no partition. | "Same access as the user" needs a new, narrow auth primitive. |
| Agent engine | AgTT backend: 91k lines of Go (56k of tests), one `package main`. Actor + inbox engine, compaction, per-conversation notes, subagents, schedules, approvals, classes with a web/internal firewall. LLM wire: OpenAI Chat Completions and Responses only. | Everything "like an AgTT conversation" exists, as tile code. |
| Projects coordinator | A conversation forced into a tool-only role (`ensureCoordinator`, `coordToolNames`); no code access by construction; `research` sub-agent with read/ls/glob/grep/read-only git (D222); "never answers a task's question or approval". | The exact pattern #51 describes, one level down. |
| Duplication | The ACP client is already shared (`sdk/acp`). Above it, both sides re-implement the session rules, and the web views exist three times. | `plans/agent-dedupe.md`. |
| LLM | xbind has no LLM connector. `builtin-tiles/llm-gw` is an OpenAI-compatible gateway tile, import-only. Tiles bind an `llm` interface slot; nothing pre-sets one for hosts. Coding CLIs in tile sandboxes can't easily reach a bound gateway; OpenCode ships with its own provider config. | The connector is new (5.4). |
| Harness login | Tile agents use the CLI's own files in the user's `$HOME` (`homes/<user>`, shared by all that user's tile sessions, not encrypted). AgTT keeps saved sign-ins in the person's partition vault (D179). | Two models; neither covers both. |
| Policy | Orgs have tile shares, sets, net sets, policy rows (`deny`, `mayCall`), limits. No model or harness policy. The harness catalog is compiled in (`sdk/acp/providers.go`). No server-side "default harness" preference. | Agent constraints and preferences are new. |
| Shell | No agent affordance in the shell. The tile window (and its Agent tab) is served from `web/` as `/vendor/…`, so it reaches old scaffolds; the shell itself is scaffold. A session can't be opened or deep-linked from outside the tile window. No web "needs you" list (iOS has one). | UI work is mostly in `web/`; one shell hook. |
| Previews | No tile thumbnails or headless rendering in xbind. The rootfs ships Playwright + Chromium (best effort). | "See what the user sees" needs a way to log a headless browser in as the user. |
| Delivery | Only what xbind serves itself reaches every workspace on an upgrade: `web/` at `/vendor/`, the `/xbin/*` pages, the Go `sdk/`. Everything else is a copy the workspace owns; template instances are forks merged by hand. | A component whose code is always the release's doesn't exist yet (section 3). |

## 3. Where it runs: sealed components (decided)

The workspace agent is a **sealed system component** built from the agent
template's source. The owner: sealed components "will be useful later when
we begin a hosted xbin offering" (a component managing the hosted instance,
a platform inference gateway), and the facility needs "a very clean
auto-update story (eg code always = builtin template code)". So this is a
general registry facility, not a one-off.

- **Source**: read from the embedded tree of the running release
  (`builtin-templates/agent` for the workspace agent). Never copied into the
  workspace: no instance fork, nothing to merge, no terminal, code tab, PRs
  or deployments on it.
- **Auto-update**: the code is always the running xbind's. The frontend is
  served from the embedded files, so it changes with the binary. The backend
  is prebuilt per release and shipped in the bundle beside `bin/`; dev
  builds compile from the checkout.
- **Data across versions**: migrations run forward at start. After a
  downgrade, a store newer than the code makes the component refuse to
  start with a message; its data is left alone.
- **No version skew**: the component and xbind are the same release, so it
  uses shared `/vendor/` modules and new SDK calls with no fallbacks.
- **Declaration**: a compiled-in table per sealed component: reserved path
  (`xbin/…`), source, partition mode, slots, capabilities (delegation for
  the workspace agent). Nothing in a workspace `xbin.json` can declare one.
- **Runtime**: the normal tile sandbox, runner, limits, logs, cron, bus and
  notify. The workspace agent is partitioned per user, so each person's
  agent is their partition, with its encrypted data and vault.
- **Engine**: the AgTT backend in a new **workspace mode** (as the Projects
  coordinator is a mode of a conversation), so compaction, notes,
  approvals, subagents, recall and the native iOS chat view come with it.
- **Access**: ordinary read access on the component decides who has an
  agent, with the tools admins and orgs already use.

Spike S1 measures the cost (registry, runner, partitions, frontend serving
from embedded source, prebuilt backend) and proves no workspace in the wild
can hold the reserved path. Fallback if it is unaffordable: the engine as a
library in a confined xbind worker.

## 4. Authority: "same access as the user"

A new, narrow primitive: **delegation**.

- Only a sealed system component can hold it. It is never read from a
  workspace `xbin.json`, never grantable, never approvable.
- A person's partition of that component may call xbind and other tiles
  **as that person**. xbind resolves the call to the person's principal
  with `Via: "delegate"`; the credential (instance token + partition) pins
  who the person is.
- It follows the existing partition rules: it reaches the person's own
  partition of other partitioned tiles, with their consent when
  `partitionConsent` is on.
- **Delegation cap.** Delegates are default-deny by route class. Allowed:
  - reads the UI makes: components, screens, tile status and reports,
    alerts, logs (at the person's level), the session directory, agent
    history;
  - tile APIs (`/api/<tile>/…`) at the person's level;
  - agent sessions: create, prompt, queue, cancel, read events;
  - code **reads** (`/code/tree`, `/code/file`), used only by the research
    sub-agent;
  - creating a tile from a template, where the person may create.
- **Never delegated:** answering a tile agent's permission request or
  question; terminals (PTYs); deleting tiles; code writes; passwords,
  devices and enrolment, web tickets.
- **Admin actions, opt-in per admin** (owner: "as an admin I'm ok to let the
  agent handle binding, but ideally those actions 1. can be batches, 2.
  should be set (by default) to require manual approval"):
  - an admin switches on "let my agent propose admin changes";
  - the agent never calls the admin API directly. It builds a **change
    set**: a batch of admin operations (bindings, grants, org shares,
    limits…), each with a plain-language line and a before/after diff;
  - default: manual approval (approve all, per item, or reject). xbind then
    applies the batch in order as the admin and writes the audit log;
  - never in a change set: passwords, devices and enrolment, SSO secrets,
    the vault, impersonation.
- **Attribution and audit.** Tiles see `X-XBin-User` as today plus a new
  `X-XBin-Agent` (additive; xbind strips inbound). Every delegate call is
  logged per user; the person can read their own log ("what my agent did").
- **Off switches.** Workspace setting off, org constraints (5.6), or the
  person pausing their agent: delegation stops at once.

Side effects are also gated in the engine (AgTT's approval machinery):
reads run freely; a tile API write, a tile creation or starting a tile
agent asks the person, with "once", "this tile for an hour" and "always for
this tile".

**Spicy tools explain themselves** (owner: they "should give the workspace
agent great understanding what they are doing within the workspace"). Each
has a dry-run that returns what it would do here: tiles touched, who gains
or loses access, reversible or not. The approval card shows that preview,
not only the agent's words.

Prompt injection is the reason for all of the above: the agent reads tile
data, holds the person's access, and tiles such as email can send data out.

## 5. Design

### 5.1 The agent and its sessions
- One default long-lived session per person, with AgTT's two-stage
  compaction, `recall` over old messages and notes.
- Further sessions with their own scope and model (5.6) are allowed; they
  are AgTT conversations in workspace mode. (My reading of the owner's
  "visible to the current session"; say if it should be strictly one.)
- **Notebook**: the project wiki store (D222: pages, revisions, compare-and-
  swap, FTS, an `index` page injected into the prompt) reused as the
  person's long-term memory: preferences, a map of their tiles, and code
  summaries the research sub-agent wrote (stamped with the tile's revision
  and org).
- Class: a fixed built-in `workspace` class. No sandbox toolset, no web
  tools, no `xbin_call`, no harness. The class can't be edited.
- Wake-ups: xbind tells the person's partition when a session the agent
  started changes status (bus delivery, coalesced).
- An xbind update ends running tile agent sessions (they don't survive a
  restart today); the agent resumes them (`resume:<historyId>`) and tells
  the person.

### 5.2 Tools
| Tool | Does |
|---|---|
| `workspace_map`, `workspace_tiles`, `workspace_screens`, `tile_status`, `tile_logs` | what the person sees: tiles, orgs, bindings, grants, layout, status, recent logs |
| `docs_search`, `docs_read` | xbind's embedded `/docs/`; each tile's `API.md` |
| `tile_call {tile, method, path, body}` | the tile's own API as the person; writes need approval |
| `tile_agent_start {tile, prompt, provider?, mode?, model?}` | opens a tile agent session as the person, marked `origin: agent`; defaults come from the person's preferences and the tile's org constraints |
| `tile_agent_message`, `tile_agent_status`, `tile_agent_result`, `tile_agent_cancel`, `tile_agents` | steer and read sessions it may drive; a message waits behind a pending question, as in Projects |
| `research {tile, question}` | read-only sub-agent on the tile's code through `/code/*`; returns a summary and may write a notebook page |
| `preview {tile | screen, viewport?}` | section 5.5 |
| `tile_create {template, name, …}` | with approval |
| `admin_propose {changes[]}` | builds a change set (section 4); only when the admin opted in |
| `ask_user`, notes, notebook, `recall` | as in AgTT |

Every tool with side effects has a dry-run form (section 4). The agent
never answers a tile agent's permission or question: those go to the person
(Agent tab, iOS, push), and the chat shows that the session waits for them.

### 5.3 Tile agents the person can see and use
- Sessions the agent starts are ordinary sessions in the person's session
  directory, with `origin` shown ("started by your agent").
- The chat shows a card per session: tile, harness, status, last line,
  **Open**.
- New in `web/bx-frame.js`: `open({session})`, and `&session=<id>` in the
  D220 link. Old shells get a plain link to the tile.
- Server-side per-user **agent preferences** (new; today only
  `localStorage`): default harness, model and mode per harness, and whether
  the agent may start sessions without asking. The `+` launcher uses them
  too.

### 5.4 LLM connector (built into xbind, in the daemon)
Owner: a connector built into xbind, "configurable both from xbin admin UI
and from xbin deployment config (so eg hosted instances would pre-populate
the llm config and lock admin settings)", OpenCode hooked up by default,
and "some user-based metering/quota system in mind".

- **Endpoint**: OpenAI-compatible routes served by xbind
  (`/api/xbin/llm/v1/chat/completions`, `/responses`, `/models`), a
  streaming reverse proxy behind one internal interface (`internal/llm`) so
  the data plane can move later. An Anthropic-wire passthrough
  (`/v1/messages`) is a follow-up; Claude Code needs it to use the
  connector.
- **Config, two sources**:
  - admin UI (a new LLM section in the admin console), an xbind-served
    `/xbin/llm` page for old scaffolds, and `bx llm`;
  - deployment config (`XBIN_LLM_CONFIG=<file>`): upstreams, keys by file
    reference, default models, quotas. What the file sets is **locked**:
    the admin UI shows it read-only as "set by your host".
- **Upstreams**: name, kind, base URL, key (in xbind's vault), models, and a
  `private` mark that org constraints use. llm-gw can be an upstream.
- **Callers**: a person (browser, a terminal or agent session token, a
  delegate) or a component bound to it. The connector is a provider for the
  existing `llm` interface slot, so AgTT binds to "LLM by xbin" the way it
  binds to llm-gw today.
- **Metering and quotas**: counters per user and per component (tokens in
  and out, cost from a price table), per day and month. Workspace default,
  per-user and per-org overrides, warn or stop. `GET /llm/usage` and an
  admin table. A host can set and lock quotas.
- **Harness hook-up by default**:
  - `sdk/acp/providers.go` gains, per harness, how to point its CLI at an
    OpenAI-compatible endpoint (one table for xbind and AgTT);
  - tile agent sessions: xbind injects the connector's address and a
    per-session token into the session's environment. OpenCode gets an
    inline provider config, so it works with no sign-in (the variable name
    is checked against the pinned OpenCode in spike S4);
  - plain tile terminals get the same variables when policy allows, so a
    CLI in a terminal can reach the connector;
  - AgTT's harness runs in coding sandboxes: a new SDK call returns a
    scoped token for the acting person; the template injects the same
    variables. Whether a manager sandbox can reach xbind is part of S4.
- **No second copy of wire code**: `sdk/llm` (`plans/agent-dedupe.md`, B2).
- Known limit: the engine speaks OpenAI wires only. Native Anthropic
  Messages (cache control, thinking) is a follow-up; a long-lived
  conversation is where it pays.

### 5.5 See what the user sees
A new xbind service, `POST /api/xbin/preview`, on the read-only session
machinery view-as already has:

- callable by a person for themselves and by their delegate;
- xbind starts headless Chromium (the rootfs's Playwright) in a throwaway
  `internal/confine` sandbox whose only network is an xbind loopback
  listener;
- the browser gets a one-shot **read-only session of that person**, injected
  by xbind and never handed to the caller; unlike view-as it opens the
  person's own partitions, since it is their own authority reduced to read;
- targets: one tile at a viewport, or a screen (`?screen=` from D220);
- returns a PNG, an accessibility snapshot (for models without vision),
  console errors and failed requests;
- limits: per-user concurrency, a timeout, a cgroup; no rootfs Chromium →
  the tool says previews are unavailable.

Spike S3 covers both frame modes (credentialless and per-tile origins).
Also exposed as `bx preview <tile>`.

### 5.6 Org constraints and scope
Owner: "Orgs define constraints; workspace agents then get their calls
filtered based on those constraints"; a multi-select of orgs, unfolding to
tiles, says what the current session sees; the UI "should very clearly
indicate what orgs/tiles are hooked up and which are not (incl top level UI
element showing eg aggregate numbers)".

- **Orgs define constraints** (`Org.Agents`, additive): workspace agent on
  or off; allowed harnesses; allowed models (`any`, `private only`, or a
  list of connector upstreams/models). The workspace sets the outer bounds.
- **Filtering follows the tile's org, on every call.** A delegate call or a
  tile agent start on a tile of org O passes only if O allows the workspace
  agent, the session's current model satisfies O's model rule, and (for a
  tile agent) the harness is on O's list. `createAgent` and
  `GET /agent/providers` apply the same harness list to sessions people
  start themselves.
- **Scope picker per session**: a multi-select of orgs that unfolds to
  tiles. Default: everything the person can see that the constraints allow
  with the current model.
- **Connection state is always visible**:
  - a top-level chip with totals, e.g. "14 tiles · 3 orgs connected · 6 not
    connected";
  - a panel per org and tile: connected / hidden by you / blocked by org
    constraints (with the reason, e.g. "Finance allows private models
    only") / no access;
  - changing the model shows what will connect and disconnect before it
    applies.
- **A conversation can't carry data to a looser model.** Once data from org
  O is in the context, the model can only change to one O allows; otherwise
  the person starts a new session. Notebook pages carry their org and are
  not injected or readable outside the scope.

### 5.7 Harness credentials (decided: connector by default + sign-ins held by xbind)
- Harnesses that accept a custom endpoint use the connector with no sign-in
  (5.4).
- For subscription logins (Claude, ChatGPT), lift D179's saved sign-ins
  from AgTT into xbind:
  - a per-user credential store in xbind, sealed under the vault barrier:
    named sign-ins per harness, one default; made by the guided flow
    (`claude setup-token`, or an API key), never a copy of the CLI's
    credential file;
  - xbind injects the default into that person's tile agent sessions, on
    every tile (partitioned tiles and VM sandboxes included, where the
    shared `$HOME` doesn't reach today); a login already in `$HOME` keeps
    working;
  - a consumer interface for components acting for a person: AgTT's
    partition asks xbind for that person's sign-in instead of keeping its
    own (its own store stays as a fallback and for sign-ins that already
    exist);
  - the guided flow runs server-side in xbind and one sign-in UI
    (`<bx-agent-signin>`) serves the tile window, the workspace agent's
    chat and AgTT (`plans/agent-dedupe.md`, B4).

Touches #4 and #25 (AgTT sign-in prompts), which get fixed in the shared
flow.

### 5.8 Web UI
- **Top-level button** in the shell's top bar, with a badge when the agent
  waits for the person. It opens `/vendor/bx-agent-dock.js`, a resizable
  side dock showing the sealed component's own frontend (the AgTT chat in
  workspace mode, on the shared chat kit).
- **Scope chip and panel** (5.6) at the top of the dock.
- **Guaranteed surface**: `/xbin/agent`, an xbind-served page with the same
  chat, for old scaffolds and direct links. Old shells learn about it from
  one dismissible alert once an admin has set the agent up.
- **Web "Needs you"**: the dock lists sessions waiting for the person (the
  iOS inbox's data), since the agent will create more of them.

### 5.9 iOS (first-class)
Owner: "agent creating apps from the phone is fine; give iOS UX lots of
love, the mobile UX of the workspace agent is one of the most important
aspects of the entire xbin project"; reachable from a top-level button that
on the iPhone Duo is always visible on its side bar.

- **Top-level button on every screen** (Home, a tile, a terminal), with the
  needs-you count. On an open iPhone Duo it is an item on the vertical bar
  (D200, `axisBehavior(.verticalPreferred)`), always visible; on iPad it is
  in the sidebar.
- **Opens the agent beside what the person is looking at**: a sheet on
  iPhone, a second pane on iPad and the Duo. The current tile or screen is
  offered as a context chip.
- **Native chat**: the sealed component's `native.js` in workspace mode,
  drawn by the shared `XbinRenderer/Chat` views. Approval and change-set
  cards with quick replies, the scope chip and panel, session cards that
  open the native agent screen.
- **Mobile-first details**: dictation, push and a Live Activity for
  delegated work ("3 tile agents running · 1 needs you"), Handoff to the
  web.
- Creating tiles from the phone is allowed. The App Review notes describe
  the feature (guideline 2.5.19).
- It gets its own design pass and UI tests on the Mac mini; it is not a
  port made at the end.

### 5.10 Prompt and nudges
Owner: no mining of their own transcripts ("this is a product for a fairly
broad audience"); research current practice instead, and add "UX nudges
that make the user prompt in correct ways (it's clear what the agent can
and can't do, that the instructions should be high level)".

The rules below come from the Projects coordinator and from a first web
research pass (one sub-agent, 2026-11-14). **I have not re-read its sources
myself**; the appendix lists them for checking before the prompt is
written.

- **Prompt rules**:
  - State the goal, what counts as done, allowed side effects and when to
    stop; no step-by-step procedure unless the path matters.
  - Give reasons with rules. Plain wording, no capitals; absolutes only for
    true invariants ("ask for approval before deleting", not "never
    delete").
  - Approval rules are specific and list what needs no approval; a vague
    rule makes the agent ask about everything.
  - Risk is tied to reversibility and to who is affected; this is what the
    dry-run previews feed.
  - Tool descriptions are prompts: one clear purpose each, no overlapping
    tools, consequences stated.
  - The mental model is loaded on demand: a short map of xbin in the prompt
    (components, partitions, orgs, interfaces and bindings, grants,
    sandboxes, tile agents), generated at build time from
    `docs/overview/*` under a size budget and tested against the docs; the
    docs tools for the rest. The agent searches its tools before saying it
    can't do something.
  - A tile agent brief has an objective, the output wanted, what is already
    known, boundaries and how to verify. A tile agent's "done" is a report
    to check, not proof.
  - Tile data, session output and notebook pages are data to report, never
    instructions. Never answer a tile agent's question or approval. Use
    `research` for any question about code.
  - Notebook hygiene: cautions carry their reason and scope and are deleted
    when the reason is gone, so old warnings don't pile up in a long-lived
    agent.
  - Look around first, ask only for the missing piece, keep working on what
    doesn't depend on the answer.
  - Reports are written for a reader who saw none of the work, and say what
    was checked and what wasn't.
- **What the research says ordinary users do**: write search-style requests
  with no context, leave out what "done" means, micromanage or
  over-constrain, don't know what the agent can do, give up after one
  failure, and often can't answer open clarifying questions.
- **UX nudges**:
  - an empty state that says what the agent can and can't do, with starter
    requests built from the person's own tiles (generic ones get ignored);
  - the scope chip, so it is clear what the agent is connected to;
  - clarifying questions as choices with a free-text option, not open
    questions;
  - "scope, then hand off": the agent restates the request as a brief the
    person can edit before any tile agent starts, and shows a plan before
    work with side effects;
  - a redirect when a request is low-level ("change line 40…"): the agent
    offers to open the tile's own agent instead;
  - progress lines while it works, and a Needs-you list; on the phone, push
    and a Live Activity.
- Closest existing product, per the research: Claude Dispatch (one
  long-running thread that fans out to child sessions and pushes when it
  needs a go-ahead). Worth a closer look before the UI design pass.
- Not found: independent evidence that starter chips or plan modes help
  non-experts (only vendor numbers), and published guidance on an
  orchestrator delegating to other vendors' coding CLIs.
- **Evaluation**: a fakeopenai script suite plus a small set of real-model
  scenarios (start an agent, wait, summarise; refuse to answer a
  permission; injected instruction in a tile's data; a vague request; a
  low-level request; a request for something it can't do).

## 6. Dedupe

See `plans/agent-dedupe.md`. In short:

- **Frontend**: one chat kit in `web/chat/` (block model, pure tool and ask
  models, views, composer, windowed transcript, one sign-in card). The
  Agent tab and AgTT each keep a small reducer into the block model. The
  workspace agent's chat is AgTT's frontend in workspace mode, so it adds
  no third copy.
- **Backend**: a session core in `sdk/acp` (queue/steer policy, escalation
  check, tool record, auth state, status words, guided sign-in driver);
  `sdk/llm` for wire and usage code; a coordinator kit inside the template
  shared by the Projects coordinator and workspace mode. The two session
  hosts stay.

## 7. Compatibility
- Everything is additive: new routes, new optional fields (`origin`,
  preferences, `Org.Agents`, `explicit`), a new header, new `/vendor/`
  modules.
- Old shells: no top-bar button, but `/xbin/agent`, `/xbin/llm` and the
  tile window's new behaviour all work, because they are xbind-served.
- The agent is **off until an LLM is configured** (by an admin, or by the
  host's deployment config); an upgrade changes nothing a user sees except
  the admin's setup card.
- Injected LLM variables in sessions and terminals appear only when the
  connector is configured and policy allows; a CLI's own login keeps
  working.
- Downgrade: sealed components and their data stay on disk, unused.
- iOS ignores unknown events and fields; the button appears only when
  `/whoami` reports the agent.
- One new compat statement, from the dedupe: a newer agent template needs
  the xbind it shipped with for its frontend too (it already does for Go).

## 8. Tracks and phases (decided: two tracks in parallel)

**First**: split `docs/protocol.md`, which is at its 512 KiB embedded cap.

**Track A — dedupe** (`plans/agent-dedupe.md`): A0 fixtures, specs and
cleanup → A1 markdown and models → A2 Agent tab on the kit, session core →
A3 AgTT on the kit → A4 coordinator kit.

**Track B — platform**
- **Spikes** (no product code):
  - S1: a sealed component: registry, runner, partitions, frontend serving
    from embedded source, prebuilt backend, the update and downgrade path.
  - S2: delegation end to end for two calls (one tile API read as the
    person, one agent session create), the route-class cap, the audit line,
    and one change set.
  - S3: preview: headless Chromium in confine, logged in read-only, one
    tile and one screen, both frame modes.
  - S4: the connector reached by OpenCode from a tile sandbox and from a
    manager sandbox.
- **B1 — LLM connector** (5.4): the proxy, both config sources and the
  lock, metering and quotas, the admin section, `/xbin/llm`, `bx llm`, the
  harness hook-up.
  - Verification: locked fields can't be changed through the API; a
    per-user quota stops at the limit; OpenCode in a tile session answers
    through the connector with no sign-in; integration test for a boot from
    the deployment file.
- **B2 — sealed components** (section 3).
  - Verification: the component's code changes with the binary; a newer
    store refuses to start after a downgrade and keeps its data.
- **B3 — delegation, constraints, scope** (4, 5.6): the primitive, the cap,
  change sets, org constraints, agent preferences, session `origin`,
  `open({session})`.
  - Verification: the delegation authz matrix (each denied class, each
    principal kind); nothing in a change set applies before approval and
    excluded areas are refused; each blocked case shows its reason.
- **B4 — sign-in store** (5.7, with the dedupe's B4 + F5).
  - Verification: a session on a partitioned tile starts signed in; AgTT
    bound to xbind's store with fallback; a live check with one real
    sign-in.

**Then, on both tracks**
- **C — the agent** (5.1, 5.2, 5.10): workspace mode, tools, docs tools,
  research, prompt, scope, approvals, audit. Behind the workspace setting.
  - Verification: engine tests with the fake model (start → wake on status
    → summarise; held behind a question; approval flows; injection in tile
    data ignored; scope and model-change rules); `make integration` with a
    fake harness.
- **D — web UI** (5.3, 5.8) and **E — iOS** (5.9), in parallel.
  - Verification: harness passes light and dark and an old scaffold; iOS UI
    tests on the Mac mini, including the Duo build.
- **F — previews** (5.5).
  - Verification: the PNG is non-empty, the read-only session can't write,
    the sandbox can't reach the network, a partitioned tile shows the
    person's data.

**Later**: org agents (a shared agent per org needs its own identity, not a
person's delegation); an `mcp` slot on the component; per-model rules
inside one upstream; one session host for everything; the Anthropic
Messages wire; the agent arranging screens; letting a person allow their
agent to answer low-risk permissions.

Each phase is one or a few PRs with its docs, changelog entry and D-entry.

## 9. Decisions

Taken by the owner on 2026-11-14:

| # | Question | Decision |
|---|---|---|
| 1 | Where it runs | A sealed system component, as a general facility with a clean auto-update story (section 3) |
| 2 | UI label | Workspace agent |
| 3 | LLM | A connector built into xbind, in the daemon; admin UI and lockable deployment config; OpenCode by default; per-user metering and quotas (5.4) |
| 4 | Delegation cap | As proposed, plus opt-in admin actions as batched change sets with manual approval by default; self-explaining tools; docs tools and a mental model (section 4, 5.2, 5.10) |
| 5 | Org policy | Orgs define constraints; calls are filtered by the tile's org; a scope picker per session; connection state always visible (5.6) |
| 6 | Harness credentials | Connector by default + sign-ins held by xbind (5.7) |
| 7 | iOS | First-class; creating tiles from the phone is fine; a top-level button, on the Duo's bar (5.9) |
| 8 | Prompt | No mining of the owner's transcripts; researched practice and UX nudges (5.10) |
| 9 | Frontend dedupe | Shared chat kit, two reducers |
| 10 | Backend dedupe | Shared session core, two thin hosts |
| 11 | Order | Two tracks in parallel |

Open points (my default unless the owner objects):

- More than one workspace-agent session per person (5.1).
- The frontend skew rule: a newer agent template needs its xbind; no
  fallback path is kept (`plans/agent-dedupe.md`, section 2).
- The Anthropic-wire passthrough comes after the OpenAI routes.
- Which admin areas a change set may touch in the first release (proposal:
  bindings, grants, org shares, limits).

## 10. Risks
- **Prompt injection with a person's access**: the cap, approvals on side
  effects, dry-run previews, no web tools, the audit log, the scope, and
  "data, not instructions" in the prompt. Still the main risk; the
  evaluation suite tests it. Opt-in admin change sets raise the stakes, so
  they never apply without a person's approval in the first release.
- **The sealed-component facility turns out expensive**: S1 is first.
- **An LLM proxy in the daemon**: streams and keys pass through xbind. It
  interprets no model output; upstream URLs are admin or host config only;
  it sits behind one interface so it can move to a sealed component.
- **One partition backend per active person**: measure memory and idle stop
  in S1 (AgTT partitions measured about 96 MiB).
- **Cost**: a long-lived conversation on a strong model; per-user quotas
  apply, and the admin table shows usage.
- **Template drift**: workspace mode lives in the template's source, so
  template work can break it; its tests run in the template's suite.
- **App Review**: an agent that creates tiles from the phone adds to the
  guideline 2.5.2 exposure already open for Create tile; the owner accepts
  it.

## Appendix: sources of the prompting research (unchecked)

Collected by a research sub-agent on 2026-11-14. Re-read before relying on
any of them.

- Anthropic: prompting best practices
  (`platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices`),
  the model-specific prompting pages beside it; "Effective context
  engineering for AI agents" (2025-09); "How we built our multi-agent
  research system" (2025-06); "Writing tools for agents" (2025-09);
  "Effective harnesses for long-running agents" (2025-11); Agent Skills
  (2025-10); "Mitigate jailbreaks and prompt injections" (docs); Economic
  Index reports (2026-01, 2026-03); Claude Dispatch
  (`claude.com/docs/cowork/guide/dispatch`).
- OpenAI: the latest-model prompting guide
  (`developers.openai.com/api/docs/guides/latest-model`), the GPT-5
  prompting guide (cookbook, 2025-08); "Designing agents to resist prompt
  injection" (2026-03); "Introducing ChatGPT agent" (2025-07); "Work with
  Codex from anywhere" (2026-05).
- Research on how people prompt: NN/g on AI literacy (2026-02), prompt
  structure, prompt controls and prompt suggestions; Zamfirescu-Pereira et
  al., "Why Johnny can't prompt" (CHI '23); Subramonyam et al. on the gulf
  of envisioning (CHI '24); a CHI '25 diary study on articulation problems
  (doi 10.1145/3706599.3719892); Microsoft HAX guidelines G1 and G2.
- Products: Lovable (chat, plan and build modes; question cards), Replit
  (plan mode, Agent 4), Devin 2.1 (confidence ratings; Ask Devin), Bolt
  (prompt enhancement), Linear (agent as delegate, session states).
