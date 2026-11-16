# Agent code dedupe — plan

> Status: live — being built since 2026-11-14 (against master
> 83c03745). Companion to `plans/workspace-agent.md` (issue #51, first
> checkbox: "render / ACP / agent loop extracted and deduped between AgTT and
> tile agents"). Owner decisions of 2026-11-14: **shared chat kit, two
> reducers** for the frontend; **shared session core, two thin hosts** for the
> backend; run as Track A in parallel with the platform work.

Three consumers must end up on one implementation:

- **tile agents**: xbind's Agent tab (`web/bx-agent.js`, `internal/term`,
  `internal/agent`);
- **AgTT**: the agent template (`builtin-templates/agent/`), whose instances
  are forks living in workspaces;
- **the workspace agent**: the AgTT code in a new workspace mode, run as a
  sealed component, plus cards for the tile agent sessions it starts.

## 1. What is really duplicated (checked in the code)

- **The ACP client is already single.** `internal/agent/acp/driver.go` is 60
  lines of options; both sides run `sdk/acp` (4,188 non-test lines, standard
  library only). An earlier plan item ("AgTT consumes `sdk/acp`'s normalised
  events") is void: it already does.
- **The duplication sits above the client**, in what each side does with the
  `acp.Event` stream, and in the web views.
- **None of the frontend pairs is a copy.** They are separate
  implementations because the inputs differ. The Agent tab folds raw ACP
  events in the browser (`GET /term/sessions/{id}/events`). AgTT's backend
  turns the same events into chat rows first (`_backend/harness_map.go`), so
  its frontend never sees an ACP event.

| Area | xbind / `web/` | AgTT (`builtin-templates/agent/`) | Also |
|---|---|---|---|
| Markdown | `web/bx-md.js` (122) | `chat-md.js` (107) | `web/xb/rt-markdown.js` (175); all three configure one global `marked` |
| Tool heads | `web/agent-tools.js` (247) | `model/harness-heads.js` (225) | Swift port (`XbinAgent`), parity fixtures |
| Ask logic (permissions, forms, plan approval, slash, steer words) | `agent-tools.js`, `agent-slash.js` (34), `agent-queue.js` (72) | `model/harness-ask.js` (361) | Swift port |
| Chat views | `agent-cards.js` (440), `agent-css.js` (130), parts of `bx-agent.js` (827) | `chat-cards.js` (300), `harness-cards.js` (322), `harness-ask.js` (199), `harness-controls.js` (277) | `web/xb/render-chat.js` (448), a third web renderer |
| Sign-in UI | `agent-signin.js` (280) + `signin-scan.js` (156, a JS twin of Go's `Signin.Scan`) | `signin.js` (240) + `model/harness-signins.js` (244) | `cleanCode` exists twice with the same body |
| Guided sign-in driver | in the browser, over a hidden terminal (`internal/term/purpose.go`) | `harness_guided.go` (663), server-side | same rules in two languages |
| Queue / steer policy | `internal/term/agentqueue.go` (407) | `harness_steer.go` (409) | only AgTT handles Codex's detached turn |
| Permission escalation check | none | `optionRaises` (`harness_park.go`) | the Agent tab doesn't confirm bypass options |
| Tool-call record | client-side fold; `internal/agent/page.go` mirrors its cuts | `harness_map.go` (896): `hcall`, `mergeTool`, `appendOutput` | fold logic exists in five places (web JS, Swift, `page.go`, `harness_map.go`, AgTT's `model/fold.js`) |
| Auth-needed detection and hint | `internal/agent/acp/driver.go`, `providers.go` | `harness_creds.go`, `harness_engine.go` | two wordings |
| Status words for "Needs you" | `internal/term/agentstatus.go` (68) | `publishSummary`, `harness_view.go` | |
| Fakes and catalogs | a hand-built fake provider; `internal/agent/acp/client_test.go` (896) is a near copy of `sdk/acp/client_test.go` | `acp.Fake`; `coding-sandbox/_backend/config.go` repeats the harness argv table | `fsb` fake: three identical copies of 3,244 lines, kept equal by a mirror test |
| LLM wire | none yet (the built-in connector would be a new copy) | `llm_chat.go` (460), `llm_responses.go` (415) | llm-gw `stats.go`, `hack/fakeopenai` |

Already shared today: `sdk/acp` (client, event vocabulary, tool-meta lift,
`Signin.Scan`, provider catalog), `/vendor/bx-kit.js`, `/vendor/marked.esm.js`,
`/vendor/highlight.min.js`, `/vendor/scroll-window.js`, `/vendor/bx-code.js`
`diffHTML`, `/vendor/bx-icons.js`, the theme tokens, and on iOS the chat
views (`XbinRenderer/Chat`), which draw both the app's agent screen and
native tiles.

## 2. Rules the dedupe must keep

- **`/vendor/` URLs are frozen** (`docs/compat.md` rule 3). A module that
  moves leaves a re-export at its old URL. Shared modules import lit by
  absolute URL; today's `agent-*.js` import a bare `lit`, which only works
  where the workspace's import map has it.
- **The kit is public.** Today `agent-*.js` and `bx-md.js` are "shell-only,
  their shapes follow the shell" (`docs/frontend-kit.md`). The shared kit
  goes in the tile-importable table; its exports only grow.
- **Template instances are forks.** Instances in the wild are untouched and
  keep working: nothing they import is removed or re-shaped.
- **New template code needs the xbind it shipped with.** This is already
  true in Go: a new SDK call doesn't build on an older xbind, and
  `docs/compat.md` says so under "What this does not promise". For the
  frontend: the template's entry makes one guarded import of the kit and
  checks its revision; if it is missing or too old, the page says "This
  agent needs xbind ≥ vX" and nothing else. No second code path is kept.
  This is the one new compat statement the dedupe needs; it gets a D-entry
  and a line in `docs/compat.md`. (Owner: object if a fallback path must
  stay.)
- **The sealed workspace agent has no skew**: its code and xbind are the
  same release.
- **`sdk/` stays zero-dependency and additive** (rule 8,
  `sdk/deployment_test.go`). A tile can import only `sdk/…`, and AgTT's
  backend is one `package main`, so shared Go lives in `sdk/`.
- **AgTT's native view imports models only** (never lit or the web's
  views), so shared model modules have no lit and no DOM.
- **Guards:** the 1000-line cap per file (`TestSizeBudget`); the js-check
  duplicate ban (`hack/check-js.mjs`) is extended to the newly shared
  helpers so copies can't come back; theme fallbacks equal Night
  (`hack/theme-fallbacks.mjs`); no `plans/` citations in embedded trees;
  the Swift parity fixtures (`native/tools/agent-parity.mjs`) are
  regenerated when a shared model's output changes.

## 3. Frontend: shared chat kit, two reducers

```
ACP events ──► web/agent-fold.js ──┐
                                   ├─► block model ─► /vendor/chat/* views
AgTT rows ───► model/fold.js ──────┘
```

**The contract is the block model** (`/vendor/chat/blocks.js`, pure):
`{k, v, kind, …}` with a key and a version for cheap re-render, and kinds
user, assistant, thought, tool (nested), changes, plan, perm, ask, signin,
notice, turn, gap, agent (a sub-conversation), draft. Both reducers produce
it; the views read nothing else.

| Shared module (`web/chat/`, served as `/vendor/chat/…`) | Kind | Built from | Then |
|---|---|---|---|
| `/vendor/bx-md.js` (stays, grows) | `md` pure, `mdInto` DOM | both markdown modules: AgTT's fence check and lazy highlighter, the web's block memo; its own `Marked` instance | `chat-md.js` becomes a re-export; `rt-markdown.js` shares the instance and the link rules |
| `chat/tools.js` | pure | `agent-tools.js` + `model/harness-heads.js` | one tool record; headline, outcome and state chips, `stripAnsi`, output tail, diff helpers. `agent-tools.js` re-exports; `harness-heads.js` becomes an adapter from AgTT's row |
| `chat/ask.js` | pure | `model/harness-ask.js` (the superset) + the web's form, slash and queue helpers | option order, bypass confirm, forms, plan approval, slash matching, steer words |
| `chat/views.js`, `cards.js`, `asks.js`, `css.js` | lit templates | `agent-cards.js` + AgTT's three view files | message, thought, tool card, changes, plan, permission, question, notice, queue chips, usage badge |
| `chat/composer.js` | element | `bx-agent.js`'s composer + AgTT's composer and controls | attachments, slash menu, mode and model pickers, steer or queue |
| `chat/window.js` | DOM | AgTT's `chat-window.js` over the already shared `scroll-window.js` | one windowed transcript |
| `agent-signin.js` (stays) | element | + AgTT's `signin.js` features | one sign-in card, driven by the server (section 4, B4) |

How the views stay host-free:

- A view is a function of `(block, ctx)`. `ctx` is a small explicit
  interface (open/closed state by key, and callbacks such as `permit`,
  `answer`, `openDiff`), replacing today's reach into the host element
  (`a._permit`, `a._isOpen`).
- Widgets with their own state (question form, plan feedback, sign-in,
  composer) are elements.
- One CSS text with `bxc-` class names and Night fallbacks, usable in a
  shadow root (Agent tab) and in a page (AgTT). Today's class names
  (`.chip`, `.ask`, `.md`, `.btn`) mean different things on the two sides.

Where the two sides differ today, the kit takes the better behaviour and
both get it:

| Difference | Kit behaviour |
|---|---|
| Questions: the Agent tab ignores URL mode, `anyOf`, `enumNames`, defaults, "Other" | AgTT's form logic for everyone |
| Bypass options are confirmed only in AgTT | `explicit` options confirm everywhere (flag set by the shared Go rule) |
| Plan feedback: sent as the next prompt vs as a `feedback` field | the card emits `{verdict, feedback}`; the host picks the transport |
| Diffs: client-side LCS vs server patches | the card takes a patch or an old/new pair; one renderer (`bx-code` `diffHTML`); AgTT's `patchLines` copy goes |
| Answered asks: kept in history vs not drawn | an ask block can sit anywhere; each host keeps its own placement |
| Highlighter: required vs optional | optional, plain text until it loads |
| Usage: tokens only vs % of context, cost, tone | AgTT's badge; cost shown when the host has it |
| Composer: the Agent tab has no upload though the API takes attachments | attachments in the shared composer |
| Markdown: different size caps, fence checks, language-class rules, image classes | one set, AgTT's line-based fence check |

Steps (each one PR; Agent tab behaviour unchanged unless the table above
says so):

1. **F0 — spec and fixtures.** The block model, the tool record, and shared
   fixture transcripts (ACP event logs with expected tool records and
   blocks) under `sdk/acp/testdata/`, read by Go, node and Swift tests. A
   spike on the `agentlongperf` harness pass settles templates vs elements
   for rows and records today's numbers.
2. **F1 — markdown.** One module, one `Marked` instance. *Landed (D224),
   with one change to the table above:* the renderer is
   `/vendor/chat/md.js` (lazy highlighter) and `/vendor/bx-md.js` is that
   module with the highlighter loaded up front — scaffold copies of
   `shell-blocks.js` in workspaces call `md()` once and can't follow a
   lazy highlighter. `chat-md.js` re-exports `/vendor/chat/md.js`.
   rt-markdown has its own instance and keeps its stricter link rule (one
   shared rule would change output). The guard (`/vendor/chat/rev.js`,
   the template's `boot.js`) landed here rather than in F4.
3. **F2 — models.** `chat/tools.js`, `chat/ask.js`; old modules re-export;
   Swift fixtures regenerated. *Landed (D224 "F2",
   `plans/wsagent/chat-kit-spec.md` §10), with these changes to the table
   above:* `patchLines` stays, in the kit (`bx-code.js` needs lit and
   highlight.js, so a tile page draws a patch plain first); plan feedback
   is `permAnswer` → `{verdict, feedback}` and each host still sends it its
   own way; the Agent tab's cards keep reading its old record until F3 —
   the fold already holds the kit's record beside it — so the bypass
   confirmation and the reading by kind in a headline reach the tab with
   its views. Three `stripAnsi` outputs of the Swift parity corpus changed
   and the port's regex with them.
4. **F3 — views.** The kit; `<bx-agent>` moves onto it. `agent-cards.js`
   and `agent-css.js` stay as re-exports. *Landed (D224 "F3",
   `plans/wsagent/chat-kit-spec.md` §11–§12): the views are template
   functions as measured, the composer and the question's form light-DOM
   elements; the Agent tab gained what the table above promised (files in
   the composer, a confirmed bypass option, the usage badge, url questions
   and the richer forms, an optional highlighter) and the harnesses not
   offered with their reason. The kit's look is the tab's; where the
   template's cards differ F4 decides (§12 lists each).*
5. **F4 — AgTT onto the kit**, one view at a time (tool cards, asks,
   messages, composer), with the guarded kit import and the "needs xbind ≥"
   page. `model/fold.js` gains the small adapter to the block model.
6. **F5 — sign-in**, together with backend step B4.
7. **F6 — optional:** `web/xb/render-chat.js` maps vocabulary nodes onto
   the kit's views, so `bx preview --native` draws the same cards.

Not changed: the Swift side already shares its views between the app's
agent screen and native tiles; its reducer (`XbinAgent`, about 6.5k lines)
stays a port held by parity fixtures. AgTT's native view keeps importing
its models, which now come from the kit.

Honest size: about 1.6k lines of template JS and 0.6k of `web/` JS turn
into adapters or re-exports. The larger gain is one behaviour for cards and
asks, and no third copy for the workspace agent.

## 4. Backend: shared session core, two thin hosts

The two session hosts stay:

- **xbind's**: `bx __agent-host` as PID 1 of the tile's sandbox, serving fs
  and terminal; an in-memory event log plus history files; a session ends
  with xbind and resumes through `session/load`.
- **AgTT's**: a sandbox-manager exec with takeover across restarts; fenced
  sqlite transactions; no fs or terminal capability; saved credentials
  injected as environment.

They stop re-implementing the rules.

**B0 — cleanup**

- Delete the test cases in `internal/agent/acp/client_test.go` that repeat
  `sdk/acp/client_test.go`; keep the xbind-only ones.
- One fake provider: `internal/agent/providers.go` uses `acp.Fake` (today
  its hand-built fake has modes ask/yolo, the SDK's has ask/auto/yolo).
- `builtin-templates/coding-sandbox/_backend/config.go` derives its harness
  table from `acp.Providers()`.
- Optional: the `fsb` fake moves to `sdk/sandboxcontract/` if it needs
  nothing outside the standard library, as its import list suggests.

Status (2026-11-14, branch `wsagent/a0-cleanup`): the first three are done,
with two more copied test files in `internal/agent` (attachments,
permissions). The fake provider keeps xbind's login line and claude's
`Signin` over `acp.Fake`; the manager's harness table lists its four ids and
reads the rest from the catalog, pinned by `TestBaseHarnessesPinned`.

The `fsb` move is **not done**. Its imports allow it (the standard library
and `sdk/ws`), and `TestNoDirectExec` does not read `sdk/`. What stands in
the way is that it is not a file move:

- `fsb.go` is 3,244 lines and `sdk/` is under the 1000-line cap
  (`TestSizeBudget`): four files or more, or a budget line.
- The agent template's tests use the manager from inside its package. They
  embed `*fsbManager` (`sbxTestManager`) and read, write and delete entries
  of its private `boxes` map under its private mutex (`setBox` in
  `sandbox_access_test.go`, and `attach_test.go`, `harness_takeover_test.go`,
  `project_fork_states_test.go`, `project_resandbox_test.go`,
  `project_sbx_adopt_test.go`, `sandbox_bind_test.go`). A package needs
  exported types (manager, box, sandbox, users, share, call, harness) and new
  hooks to edit, list and drop a box.
- Those exports become public SDK API that only grows, for a fixture whose
  header says it isolates nothing and never ships. That is a decision with a
  D-entry, `docs/sdk.md` and a changelog line, not a cleanup.
- It touches about twenty test files of the agent template while Projects
  work is active there, and the sandbox-terminal tile needs a version bump.

A work package of its own could do it as `sdk/sandboxcontract/sbxfake`:
split by area (sandboxes, exec, files, stdio and tty, snapshots), export the
fixture API on purpose, and keep `type fsbManager = sbxfake.Manager` aliases
in each consumer so their tests change little.

**B1 — session core in `sdk/acp`** (pure, no I/O, table-tested on the
shared fixtures)

| New in `sdk/acp` | Comes from | xbind uses it in | AgTT uses it in | Gained |
|---|---|---|---|---|
| `queue.go`: steer / hold / flush decisions, detached turns | `agentqueue.go`, `harness_steer.go` | `agentqueue.go` (keeps its in-memory store) | `harness_steer.go` (keeps its inbox rows) | tile agents handle Codex's detached turn |
| `escalation.go`: does this option raise the mode | `optionRaises` | `permission.request` gains `explicit` (additive) | `harness_park.go` | the Agent tab confirms bypass options |
| `toolrec.go`: the tool record, merge, id reuse, output tail | `harness_map.go`, `page.go`, `agentpage.go` | `page.go`, `agentpage.go` | `harness_map.go`, `harness_view.go` | one record shape, the one `chat/tools.js` reads |
| `authstate.go`: auth-needed detection, one default hint | both sides | `acp/driver.go`, `providers.go` | `harness_creds.go`, `harness_engine.go` | one wording |
| `summary.go`: status words for "Needs you" | `agentstatus.go`, `publishSummary` | `agentstatus.go` | `harness_view.go` | same words on web, iOS and AgTT |
| `guided.go`: the guided sign-in driver over an `Exec` interface | `harness_guided.go` | a new server-side `/agent/signin` | `harness_guided.go` becomes an adapter | see B4 |
| provider table: how to point each CLI at the LLM connector | new | session spawn | harness spawn | OpenCode works by default |

Status (2026-11-14, branch `wsagent/a2-core`, D225): the first five rows
are in — `toolrec.go`, `escalation.go`, `queue.go`, `authstate.go`,
`summary.go`, each adopted by both hosts — with one addition to the table:
`permission.request`'s `explicit` is set by the client itself (so the
pending list and the page state carry it), and tile agents end a detached
turn with a `turn.end` marked `detached`. Sizes: the template's backend
lost 123 lines net (313 out, 190 in; `harness_map.go` 896 → 828), xbind's
existing files stayed level (142 out, 146 in) and gained the 158 lines of
`agentdetached.go`; the five SDK files are 1,094 lines. `guided.go` and the
provider table wait for B4 and the connector.

**B2 — `sdk/llm`** (standard library only): wire types and usage extraction
for Chat Completions and Responses, streaming included, and the price-table
type. Used by the new connector (`internal/llm`), llm-gw's `stats.go`,
AgTT's `llm_chat.go` and `llm_responses.go` (types first), and
`hack/fakeopenai`.

**B3 — coordinator kit inside the template.** The workspace agent is the
AgTT engine in workspace mode, so there is no second agent loop. What it
shares with the Projects coordinator becomes one layer with two profiles
(project, workspace): the tool-only role (`coordToolNames`), messages held
behind a person's question, status words, the wiki as notebook, and the
research sub-agent with a `codeReader` interface (sandbox files for
Projects, `/code/*` through delegation for the workspace).

Status (2026-11-15, branch `wsagent/c1-coordkit`): done as a
behaviour-neutral refactor — Projects' prompts, tool lists and refusals
are byte-identical to the base build's, and its tests pass unmodified.

- **Profiles** (`_backend/coord_profile.go`): `coordProfile` — origin,
  forced class, denials, tool allowlist, limits, status words, and the
  hooks `Is`, `Resolve`, `Pending`, `Deliver`, `Prompt`, `Knowledge`.
  `project` is the first (`projects_coord_profile.go`); `workspace` is
  registered empty (`coord_workspace.go`) and nothing stamps its origin.
  The kit resolves the coordinator and holds the lane on every call
  (`coord_tools.go`), coalesces wakes and writes the updates message
  (`coord_wake.go`), words a park (`coord_hold.go`) and assembles the
  prompt block from parts (`coord_prompt.go`).
- **Research** (`coord_research.go`, `coord_reader.go`): `researchSpawn`
  for any binding; the child's tools are defined against `codeReader`
  (`Read`, `Ls`, `Glob`, `Grep`, `Git`), with git only for a reader kind
  that declares its subcommands. `CodeReadBinding` gained `kind`; the
  sandbox reader is kind `""` (`projects_coord_reader.go`).
- **Knowledge store** (`coord_know*.go`): `knowOwner{Kind, Key}` →
  `DB.knowID`. A project's store id is its project id, so no row, index or
  table changed and **no migration runs**; other owners get negative ids
  from the new `wiki_owners` table. (Adding an owner column was rejected:
  an FTS5 table can't gain a column, and the unique `(project_id, slug)`
  index would have had to be replaced — one an older build re-creates at
  start and could then fail on.)
- **Left for the workspace package**: the `workspace` class and the code
  that makes the conversation (with origin `workspace`); its allowlist,
  tools and hooks; a remote `codeReader` kind and its binding fields; a
  notebook `knowKind`, its `Knowledge` hook, and the wording of the wiki
  tools' descriptions and of `errNoWiki` for a notebook (they still say
  "this project's wiki"); how a message to a tile agent session is held
  behind its question (`coordHolds` is the predicate; Projects' queue is
  not reusable for a session that lives in xbind); `coordMarkers` if its
  updates wear a header of their own.

**B4 — sign-in.** xbind runs the guided flow server-side with
`acp/guided.go` and holds per-user sign-ins (`plans/workspace-agent.md`
5.7). `<bx-agent-signin>` talks to that API; the browser-side scanner is no
longer used by new code (`signin-scan.js` stays served). AgTT asks xbind
for the person's sign-in and keeps its own store as the fallback for saved
sign-ins that already exist.

**Not merged, on purpose:** one session host for everything. It needs xbind
sessions that survive a restart and run in manager sandboxes, and a
migration of instances in the wild. Revisit when xbind sessions gain
restart survival. Until then an xbind update ends running tile agent
sessions, so the workspace agent resumes them (`resume:<historyId>`) and
tells the person.

## 5. Order (Track A)

- **A0**: F0 fixtures and specs; B0 cleanup.
- **A1**: F1, F2 with `toolrec.go` and `escalation.go`.
- **A2**: F3 (Agent tab on the kit); `queue.go`, `authstate.go`,
  `summary.go`.
- **A3**: F4 (AgTT on the kit), in small PRs because AgTT Projects work is
  ongoing.
- **A4**: B3 coordinator kit.
- B2 (`sdk/llm`) lands with the connector on the platform track; B4 and F5
  land with the sign-in store.

## 6. Verification

- **Every PR:** `make js-test` (`hack/agent-*.test.mjs`,
  `hack/agent-template-*.test.mjs`), `make js-check`, `make theme-check`,
  `make test`, `./hack/tile-check.sh agent`, then one `make check`.
- **UI harness, light and dark:** `agenttab`, `agentsignin`, `agentlong`,
  `agentlongperf` (no slower than the numbers recorded in F0),
  `agentreview`, `agenttemplate`, `agenttemplatelong`, `agentharness`,
  `agentsignins`, `oldscaffold`.
- **Shared fixtures** pass in Go (`sdk/acp`), node (`chat/tools.js`,
  `chat/ask.js`, both reducers) and Swift (`ParityTests` on the Mac mini).
- **Old instance, new xbind:** a harness pass seeds the agent template as
  released in v0.3.78 and runs it unchanged.
- **New template, old xbind:** the kit URL answers 404 (as `noWindow` does
  today for `scroll-window.js`); the page shows "needs xbind ≥".
- **Gains are tested, not assumed:** a Codex detached turn in
  `internal/term`; a bypass option confirmed in the Agent tab; URL-mode and
  `anyOf` questions in the Agent tab.

## 7. Risks

- **AgTT is under active work** (Projects): F4 is split per view and each
  PR is rebased on the day it merges.
- **A view regression in the Agent tab**: F3 changes no behaviour and is
  held by the existing harness passes plus the perf pass.
- **The kit's shapes become public API**: the block model is specified in
  F0 before any view is written, and only grows afterwards.
- **Swift drift**: any change to a shared model's output regenerates the
  parity fixtures in the same PR; the Mac mini run is part of the PR's
  checks.
- **The frontend skew rule** is a new statement in `docs/compat.md`; if the
  owner wants a fallback path kept, F4 keeps AgTT's old views behind the
  guard and the template-side saving disappears.
