# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-11-16

- **Workspace agent: fixes from a security review**
  ([workspace-agent.md](/docs/workspace-agent.md),
  [protocol/delegation.md](/docs/protocol/delegation.md#the-chrome)).
  What a conversation has read is kept with the conversation: a session
  whose record xbind renewed (a day unused) gives the new record
  everything it holds before it reads or calls a model again, and stops,
  saying so, where an org's rules no longer allow its model (`held` on
  `GET /ws/sessions`; the agent template's `API.md` §Workspace mode). A
  notebook page's org marks can't be cleared in place any more (the
  `clearStamps` field of `PUT /ws/notebook/{slug}` is gone and ignored):
  delete the page and have a clean session write it again. `research`'s
  `save_as` writes over an existing page only where the session may read
  it, and adds to its stamps instead of replacing them. `admin_changes`
  lists the change sets its own conversation proposed, no other
  session's. A tile agent's start follows xbind's dry run at the brief's
  Start: no request where the person lets starts through, one filed
  wherever xbind says it is needed. `notebook_search` counts the pages a
  session can't read whatever the words searched for (the count of the
  matching ones said what they held). The chrome: the agent's page may
  open only views that change nothing (`xbin:agent-open {view}`: `needs`,
  `approvals`, `activity` — no longer `prefs`), and every control that
  widens what the agent may do is armed like Allow.

- **Agent sessions: "offered" versus "on" the LLM connector**
  ([llm.md](/docs/llm.md#agent-sessions),
  [protocol/agents.md](/docs/protocol/agents.md)). A session's row and its
  tail page's state add `llmOffered: true` beside `llm: "connector"` when
  nothing holds the CLI to the connector: no rule gives it the connector
  alone, and the harness keeps the person's and the project's own
  providers (OpenCode), whose config can leave it out. Its first notice
  carries `offered: true` and says the connector is offered as the default
  model. The Agent tab's header says "on the workspace connector" only when
  the rule holds the session there or the harness's settings replace the
  person's (Codex), else "offered: workspace connector"; its launcher says
  "on" only when the provider's row is `forced`. Before, both said "LLM by
  xbin" whenever the connector was the default. Additive: a client that
  ignores the new field reads what it did.
- **docs: the changelog's older entries** moved, unchanged, to archive
  pages: everything before 2026-10-01 is in `/docs/changelog/<YYYY-MM>.md`
  (September in two, `2026-09a.md` and `2026-09b.md`), listed under
  [Older entries](#older-entries) at the end of this page.
  `/docs/changelog.md` still works and keeps the recent weeks, so its
  `?raw=1` fetch returns only those; read an archive page for anything
  older.

## 2026-11-15

- **The workspace agent's page, its backend and xbin's chrome meet** (D235;
  [workspace-agent.md](/docs/workspace-agent.md), [protocol/delegation.md
  §The chrome](/docs/protocol/delegation.md#the-chrome)): the run view
  answers a sent message's `context` (its "About …" chip);
  `cards.requests[]` and `cards.briefs[]` carry `callId`, the tool call
  that made them, for the item's life, and the page places its cards by it
  instead of the tool's words; `xbin:open-session {session: "new", tile,
  draft}` opens the tile's window on a new session's launcher with the
  person's words waiting, unsent, in the composer of the coding agent they
  pick (`<bx-frame>` `open({session: "new", draft})`, the dock's
  `bx-agent-new-session`; a shell from before it gets a plain link). A
  tile's ⋯ menu has **Ask my agent about this tile** (the scaffold's
  `shell/shell-agent.js`), which opens the dock about that tile.
  Preferences pick the mode a tile agent your agent starts gets among the
  modes that ask before acting, naming the ones that don't.
- **The workspace agent's page draws from fields** (D235): the context
  chip is sent as the message's `context`; request cards, refusals
  (`cards.refusals`), the agent's question kinds (a redirect, can't), the
  quota banner and the pause time come from the backend's fields, never
  its sentences. A redirect's "Hand this to <tile>'s own agent" posts
  `xbin:open-session {session: "new", tile, draft}`.
- **The workspace agent's page** (D235; [workspace-agent.md](/docs/workspace-agent.md#the-agents-page),
- **The workspace agent is in xbind: `xbin/wsagent`** (D231 on D227;
  [workspace-agent.md](/docs/workspace-agent.md#setting-it-up),
  [sealed.md](/docs/sealed.md)). The first system component: the agent
  template's backend in workspace mode, one backend per person, its model
  slot on the LLM connector alone. It is **inert until an admin** connects
  a model, turns workspace agents on and chooses who gets one — and a
  workspace that does nothing sees nothing new: no chip, no notice, no
  backend, no row in `GET /components`.
  - **Nobody has a system component until it is given to them.** Only an
    access entry that names it gives read (a person's, an org's share, the
    workspace defaults): `*` and other patterns give nothing there. Admins
    read it as they manage it.
  - `PUT /api/xbin/delegate/workspace` takes `who: "everyone" | "me" |
    "chosen"` beside `enabled`, and answers `who: {everyone, entries,
    chosen}`; xbin's chrome has the two buttons on its third setup step.
    `GET /delegate/state` adds `access`, `held` / `heldWhy` (why the agent
    can't run: `older`, `locked`, `unavailable`) and, for an admin, `who`;
    `agent` is there only for a person who has one, and so is the notice
    that names `/xbin/agent`.
  - A system component's release may start it **below a tile's limits**
    and its partition mode is fixed for good (a release that says
    otherwise holds it: `partition-mode`). The agent: 512 MiB a backend,
    stopped after three idle minutes, answering again in under a second.
  - A tile agent's ended turn, question or failure reaches its person's
    workspace agent **at once** (it could wait out a minute); bursts are
    still folded into one wake.
  - `GET /term/sessions?compact=1` rows of agent sessions add `pending`,
    `questions`, `turn` and — in a person's own listing — `last`, the last
    line the session wrote; a tile-agent card in the agent's page reads
    them, takes the coding agent's name from `GET
    /delegate/start-defaults`, and is placed by the request that started
    it (`request`, `callId` in `GET /ws/sessions/{id}/cards`).
  - Releases carry its prebuilt backend (`bin/sealed/wsagent-<digest>`,
    about 45 MB, 16 MB in the bundle); `make build` needs the agent
    template's Go modules. A development xbind compiles it on first use.

- **The workspace agent's page** (D232; [workspace-agent.md](/docs/workspace-agent.md#the-agents-page),
  the agent template's `API.md` §Workspace mode → The page). On the sealed
  component's path the agent template's `boot.js` asks `GET /ws/state` and
  loads its workspace page (`ws/`) in place of `agent.js`; an instance of
  the template asks nothing more than before. The page talks to xbin's
  chrome by `postMessage` with ids only — `xbin:agent-open-request {id}`,
  `xbin:agent-open-changeset {id}`, `xbin:open-session {session, tile}` out,
  `xbin:agent-context {tile | screen}` in — and draws no grant.
- **Chat kit revision 5** ([frontend-kit.md](/docs/frontend-kit.md)): a
  plan block's `title`, `meta`, `note`, `numbered` and an entry's `tag`
  (`{text, icon, tone}`); an ask block's `submit` and `skip` words (`skip:
  ''` draws no Skip); `<bx-chat-composer label>`, the text box's accessible
  name.
- **Workspace agents: a start carries its first prompt; xbind resolves a
  start's defaults; taint from the agent's own store** (D228 C0;
  [protocol/delegation.md](/docs/protocol/delegation.md)). A delegate's
  `POST /term/sessions` takes `prompt` (bound by the ticket, shown on the
  card, sent by xbind as the session's first prompt) and may name only its
  tile — the card shows the effective harness, mode and network. New:
  `GET /delegate/start-defaults?tile=`, `POST
  /delegate/sessions/<id>/allows` and `…/taint`, `&ids=` on the compact
  session read (`endedBy: exit | restart`). SDK: `StartDefaultsOf`,
  `SessionAllows`, `TaintSession`.
- **The workspace agent's chrome: the dock, approvals, admin change sets,
  `/xbin/agent`** (D232; [workspace-agent.md](/docs/workspace-agent.md)
  §Using it, [protocol/delegation.md](/docs/protocol/delegation.md) §The
  chrome, [frontend-kit.md](/docs/frontend-kit.md) §Shell-only modules,
  [compat.md](/docs/compat.md)). Everything a person decides about their
  agent is drawn and sent by xbin's own chrome, in a document that holds
  their session: a dock beside the canvas (`/vendor/bx-agent-dock.js`; a
  **workspace agent** chip in the scaffold shell's top bar with what waits,
  Ctrl+J / ⌘J) whose header — Needs you, Pause / Resume, Approvals, What my
  agent did, Preferences — is xbin's, with the agent's own page in a
  sandboxed frame below. An approval is a sheet drawn from xbind's reading
  of the request, the agent's sentence quoted as the agent's; a change set
  is a dialog with warnings unticked and blocked items shown.
  **`/xbin/agent`** is the same dock as a page of xbind's own, for a shell
  without the chip and for direct links (`?request=`, `?changes=`,
  `?view=`). New wire, all additive: `GET /delegate/state` answers
  `agent`, `pausedAt` and `workspaceAt`; alert kind `agent-page` (one a
  person, naming `/xbin/agent`, for shells without the chip) and its
  `POST /api/xbin/delegate/page/dismiss`. **For whoever builds an agent's
  page:** ask the chrome with `postMessage` —
  `xbin:agent-open-request {id}`, `xbin:agent-open-changeset {id}`,
  `xbin:open-session {session, tile}` — and listen for
  `xbin:agent-context {tile | screen}`; ids only, the chrome reads
  everything else from xbind. **For a shell of your own:**
  `shell/shell-agent.js` in the scaffold is the pattern. No agent ships
  with this xbind yet: with workspace agents on, the dock shows approvals,
  the log and preferences, and says the conversation arrives with an
  update.
- **`XBIN_CONFINE_TRACE`**: a file every confined tool run (git, go, … on
  workspace data) appends a line to — its start and end, whether it ran in
  a sandbox, the tool ([config.md](/docs/config.md)). A measurement aid: the
  latency tests use it to tell an operation's sandbox runs from the rest.
- **agent template: cancelling a project task that waits for its
  workspace sticks.** `POST /runs/{id}/cancel` (or a cancel from its parent)
  on a task the workspace gate held parked stopped it only until the gate's
  next look: the start it was parked with stayed in its inbox, so the gate
  parked it again and the turn ran once the workspace was ready. The cancel
  now drops the input queued before it, as cancelling the task does; a
  message sent afterwards starts it as usual (the template's `API.md`
  §The workspace). An instance gets it with `git merge template/main`.
- **coding-sandbox: an image's `sudo` gives `/dev/fuse` and `/dev/net/tun`
  from the sandbox's first boot** (D182). The template wrote the guest's
  device list (`/etc/xbin-vm-devices`) after a new sandbox had booted, and
  the guest reads it only as it boots: a sandbox of a `sudo` image with no
  setup script had both nodes 0600 until its first restart, where
  the template's API.md (§Testing on xbind, 8) promises 0666 at the first boot.
  The first start now sets the two modes itself as well. An instance gets
  it with `git merge template/main`; a sandbox made before needs nothing
  but a restart, as before.
- **The workspace agent's backend: the agent template in workspace mode**
  (D231; [workspace-agent.md](/docs/workspace-agent.md), the template's
  `API.md` §Workspace mode). Started with `XBIN_AGENT_MODE=workspace` — by
  xbind, for the sealed component `xbin/wsagent`; never by an instance of
  the template — the agent template's backend is a person's workspace
  agent: one fixed class with no sandbox, web, `xbin_call` or coding agent,
  only the `workspace` profile's conversations (a long-lived **Main** and
  further sessions, each with its own scope and model), a closed route
  list (every other route of the template is 404), and tools that look
  (`workspace_map`, `tile_status`, `tile_logs`, `docs_search`, `docs_read`,
  `tile_api_doc`), act as the person through xbind's delegation
  (`tile_call`, `tile_create`, `admin_propose`), hand building to tile
  agents (`tile_agent_brief`, `tile_agent_start`, …) and ask a read-only
  sub-agent about a tile's code (`research`). Every side effect is filed
  for the person's approval — pending, never holding the conversation up — and xbind keeps and checks the
  approval. **For tile authors:** the agent reads your tile's `API.md`
  before it calls your API, calls arrive with `X-XBin-Agent`
  (`xbin.Caller(r).Agent`), and `"agent"` in the manifest says what agents
  may use. A normal instance of the template is unchanged: without the
  variable none of this exists, and an unknown value refuses to serve. The
  template's coordinator kit gained the profile hooks `System` and `Specs`.
  What its page draws is answered in fields beside the model's sentences
  (`API.md` §Workspace mode, "In fields"): a message's `context`, a
  request's outcome, refusals and questions with a `kind`, the stopping
  quota and the pause's time.
- **Agent sessions: a workspace agent's conversation is a handle xbind
  issues** (D228 part C0;
  [protocol/delegation.md](/docs/protocol/delegation.md) §Agent sessions).
  `POST /api/xbin/delegate/sessions {scope, model}` → a session; every
  delegate call carries `X-XBin-Agent-Session` (403 `no-session`
  without). The orgs' rules are asked with the **session's** model —
  `X-XBin-Agent-Model` is ignored now — the scope confines the call
  (`hidden`), the orgs whose tiles a call touched taint the session, and
  the model changes only to one every tainted org allows (409
  `model_taint`). The LLM connector serves a workspace agent's calls on
  its session's model only ([llm.md](/docs/llm.md)). New reads:
  `…/sessions/<id>/connection` (blocked tiles as counts), `GET
  /delegate/map`, `GET /docs/search`, `GET /docs/page` (for every
  signed-in caller), `GET /term/sessions?origin=agent&compact=1`, and
  `GET /agents/connection?session=` for the agent's own page. xbind sends
  the agent's backend notices (kinds and ids only) when a request, an
  approval, a change set, the person's pause or a session it started
  changes. SDK: `InAgentSession`, `OpenAgentSession`, `SetSessionModel`,
  `SetSessionScope`, `ConnectionOf`, `MapOf`, `DocsSearch`, `DocsPage`,
  `ReadHolderNotice`, `ErrNoSession`, `ErrHiddenByScope`, `ErrModelTaint`.
  Nothing delegates until a holder ships; tiles need no change.
- **Previews: xbind renders a tile or a screen as the person who asks**
  (D234; [protocol/preview.md](/docs/protocol/preview.md),
  [auth.md](/docs/auth.md) §Previews, [isolation.md](/docs/isolation.md)
  §Previews). `POST /api/xbin/preview {tile | screen, viewport?}` opens the
  page in a headless Chromium of xbind's own — a throwaway sandbox with no
  network but one socket to xbind — as a one-shot **read-only** session of
  the caller that nobody is handed, and answers with a PNG, a text
  rendering (an accessibility tree per frame), console errors, failed
  requests and a `notice` that the content is data, not instructions.
  `GET /api/xbin/preview` says whether this xbind can render (it needs
  `--isolate` and a rootfs with Chromium) and the limits in force:
  `XBIN_PREVIEWS`, `XBIN_PREVIEW_PER_USER`, `XBIN_PREVIEW_TOTAL`,
  `XBIN_PREVIEW_TIMEOUT`, `XBIN_PREVIEW_MEM` ([config.md](/docs/config.md)).
  `bx preview <tile>` and `bx preview --screen <name>` save the picture.
  **For tile builders:** every call a render makes to your backend carries
  `X-XBin-Preview: 1` (`xbin.Caller(r).Preview`) — a GET or a socket
  message of yours that has an effect (mark-as-read, presence) must skip
  it then ([sdk.md](/docs/sdk.md)); xbind refuses the render's non-GET
  requests itself, and on every call it lets through — GETs and WebSocket
  upgrades — sends role `reader` and user level `read` (`UserCanWrite()` is
  false in a preview). Don't draw differently for it: `/whoami` in a render
  says `readOnly: true`, as in view-as, and nothing more. Without a
  delegated cgroup (no enforceable memory cap) previews answer 503 unless
  `XBIN_PREVIEWS=uncapped`.
- **Builtin tiles and templates honour `X-XBin-Preview`** (D234): in a
  preview their GETs and sockets change nothing. The agent template: a
  project's status and overview reads start no repo fetches, the CI read
  makes no watch and re-reads none, the team board and membership reads
  apply no definition, a harness probe runs nothing in a sandbox, the
  sandbox terminals answer 403, an SCM sign-in poll reads the state without
  trading the code. scm-github: the device-flow poll and the App manifest
  callback do nothing. coding-sandbox (and `examples/sandbox-go`): a file
  read starts no stopped sandbox, and no terminal opens. sandbox-terminal
  v12: the page is not a visit and re-checks nobody's access. A workspace's
  instances get the template changes as template updates (`bx template
  updates`).
- **Client: `xbin.ready()`** (D234; [elements.md](/docs/elements.md)) — a
  tile says "I have drawn"; with `<meta name="xbin-ready"
  content="manual">` it is waited for. Additive: a tile that never calls it
  behaves as before.
- **Read-only sessions (view-as) no longer do three things a GET let them
  do:** `GET /api/xbin/deployments/diff` with a work-tree side — the
  default `to` — captured a checkpoint of the tile; it answers 403 now
  (diff two checkpoints or deployments instead). `GET
  /api/xbin/git/remote-info` dials out and is refused. And a partitioned
  tile's frame calling another partitioned tile no longer starts the
  person's first partition there or prompts them for consent (previews;
  view-as was already refused).
- **Shell: nothing is saved under `readOnly`** — the layout and settings
  saves and the "couldn't save your dismissal" toast are skipped in a
  view-as session and a preview (the server refused them before; now the
  shell doesn't ask). Scaffolded shells get it with the next scaffold
  update; a preview hides an older shell's toasts by itself.
- **LLM connector for coding agents and terminals: a security pass**
  (D226; [llm.md](/docs/llm.md) §Coding agents and terminals). None of it
  changes a workspace with no connector configured.
  - **`connector-only` fails closed.** Where the workspace's credential
    mode or an org's model rule gives the connector alone, a session that
    can't be put on it is refused (`403`) instead of opening without it, a
    session whose agent host doesn't confirm it is ended with the reason,
    OpenCode is configured with the connector as its only provider, and a
    model that isn't the connector's can't be set — at `POST
    /term/sessions` (`model`, `options`) or `POST …/options` (`403`).
  - **`POST /api/xbin/llm/keys` is for a tile's backend only.** A frame,
    terminal or agent-session credential is now `403` (before, a tile's
    page could mint a key for its person). A key lives **24 h at most**
    (was 30 days); `POST /api/xbin/llm/keys/<id>/renew` keeps a longer
    run's alive (`xbin.RenewLLMKey`, `ErrLLMKeyGone`). A tile holds at most
    64 live keys for one person and 512 in all (`429 xbin_llm_keys`,
    `xbin.ErrLLMKeyLimit`). A key names its backend's deployment: it is
    held to the tile's edge policy for that deployment on every call, and
    `DELETE /llm/keys?label=` ends that deployment's keys only.
    **If your tile's page asked for keys, move the call to its backend.**
  - **A session with no network reaches the connector** — and nothing
    else: `net: none` sessions and terminals get `XBIN_LLM_URL` =
    `http://10.0.2.2:4180/v1`, one gateway port forwarded to xbind's
    LLM-only listener (before: no key, "can't be reached"). **A session
    on a non-primary tile deployment gets a key** that names the
    deployment (before: none).
  - Only models whose ids are plain model names are written into a CLI's
    configuration (`acp.ValidConnectorModel`); an upstream's list can't
    put `{env:…}` or `{file:…}` into OpenCode's.
  - An agent can no longer forge the agent host's own frames (who serves
    its model calls); LLM keys are masked in a dead terminal's logged tail
    before the cut.
  - `sdk/acp`: `ConnectorParams.Only`, `Connector.AllowsOption`,
    `ValidConnectorModel` ([sdk.md](/docs/sdk.md)).
  - **Agent template**: a coding agent runs on the connector only when the
    workspace set a model for coding agents (else it says why and uses its
    own sign-in); it starts in a sandbox with **no egress** when the
    connector is reachable there (`GET /harnesses` images gain `llm`); its
    key is renewed while the run lives and its end is retried until xbind
    acknowledged it. Template instances pick it up with a template update.
  - The Agent tab's launcher gains **"runs on"**: a person's saved choice
    between their own sign-in and the workspace's connector for a harness
    (`harnesses.<id>.credentials` in their agent preferences), shown only
    where both can serve and no rule forces one.
- **Agent template: a Settings tab that loads late no longer draws over
  the tab opened after it.** Opening Settings starts Config, which reads
  the models; a click on another tab meanwhile was drawn — and then
  overwritten when Config's reads landed. Each opening of a tab now draws
  into a box of its own (`#sbd > .tabbox`, `display: contents`); a tab
  function still receives the element it draws into. The gear is hidden
  until the page can open Settings. Likewise the Automations page: a
  form opened while its list was still being read (New schedule, right
  after opening the page) is no longer closed when the read lands.
- **BREAKING for patched instances of the agent template: its chat is
  drawn with the chat kit; a tool call's card says what it came to**
  (D224; [frontend-kit.md](/docs/frontend-kit.md) §The chat kit;
  migration:
  [changes/2026-11-15-agent-chat-on-the-kit.md](/docs/changes/2026-11-15-agent-chat-on-the-kit.md)
  — every old class inside a card with its new name; nothing breaks until
  an instance merges the template). The kit (`/vendor/chat/…`, revision 4 in
  `/vendor/chat/rev.js`) has a second host: the agent template hands its
  blocks to the kit's views (`model/fold.js` `blockOf`, `chat-kit.js`), so
  a coding agent's calls and the template's own tools are one card, in the
  Agent tab and in the template alike.
  - **The tool card** (`/vendor/chat/cards.js` `toolView`) is one line —
    glyph, headline with the command under a description, what the call
    came to (`exit 1`, `+3 −1`, `12 matches`), a still mark while it runs,
    and its state only while that needs a look (needs approval, failed,
    cancelled) — in place of a boxed status badge on every card. Open, it
    adds the tool's name, an `exit N` line under a command's output, the
    places a read or a search touched, a fetch's address as a link, and a
    file a patch could not be drawn for by name. The Agent tab's cards
    change with it.
  - **The request card** (`/vendor/chat/asks.js`) is one card for a
    coding agent's permission request, the agent template's own approval
    of its tools and a capability's grant: it leads with who asks what
    ("Claude Code asks to run a command"), shows the command or the calls,
    the diff the call would make (the request's own, else the call's:
    `ChatCtx.callOf`), an optional word with a rejection where the host
    can send one (`ChatCtx.rejectWord`), and the options. A question leads
    with "<agent> asks". A `perm` block may carry its own `lead`; its
    `calls` are listed; `cards.js` exports `diffsView`.
  - **Messages** (`/vendor/chat/views.js`): a person's own words sit on
    their side of the chat without a "YOU" label (another person's, in a
    shared conversation, on the other side under their name), the agent's
    without an "AGENT" label and with a Copy that shows with the pointer
    where the host copies (`ChatCtx.copyMessage`); a message's files are
    chips with a picture or a glyph, the path and a size. A thought says
    "Thought for 1m 5s". Markdown (`mdCssText`) gains heading sizes, list
    and paragraph spacing, a scrolling table and the reading colours
    (headings, list markers, inline code, the quote's rule). A `notice`
    block may carry `icon`, `tone`, `md`, `fold` / `title` and `action`
    (`ChatCtx.act`) — the agent template's journal lines are these. A
    sub-conversation's card (`agentView`) reads like a call's, with what
    it waits on a person for under it. `ChatCtx.agentLabel` is no longer
    drawn.
  - **For a tile that draws with the kit**: a `tool` block may carry its
    own `icon`, `sub`, `outcome`, `args` and `result` (a tool of the
    host's own); `ChatCtx.rowAttrs(block, depth)` puts the host's hook
    classes and `data-*` attributes on a block's element; a block inside a
    card sits in `<div class="bxc-in" data-k>` (`innerView`); `cards.js`
    exports `argsView` and `clipView`; `tools.js` `stateChip` says
    `waiting`. A fold is now drawn as `ChatCtx.isOpen` says and `setOpen`
    hears a fold closing as well as opening — a host whose `isOpen`
    returned `true` for a fold the reader had closed will see it stay
    open; return what the reader last did.
  - **An instance of the agent template** merges this by git. Its page
    needs kit revision 4 (`boot.js` `NEEDS`: it says "This agent needs
    xbind ≥ v0.3.80" on an older xbind). A tool call's row keeps its
    hooks — `.tcard` (`.hcard` for a coding agent's), its state class,
    `.on` while open, `data-k`, `data-fam`, `data-tool`, `data-status` —
    on the kit's row; inside it the markup is the kit's (`.bxc-title` for
    `.hl`, `.bxc-sub` for `.sub`, `.bxc-oc` for `.oc`, `.bxc-st` for
    `.st`, `summary` for `.tch`, `.bxc-body` for `.tcb`; a coding agent's
    `data-kind` is read from `data-tool`), so a stylesheet of your own
    that reached inside a card needs those names. `harness-cards.js`
    keeps the top bar's usage badge, counts and plan pin. What a run
    waits on a person for keeps `.ask` (`.hask`, `.hplan`, `.hq` for a
    coding agent's; `.approve`, `.grant` for the engine's own) and
    `data-park` on its element; inside, the buttons are the kit's
    (`button[data-option]` for `.hopts [data-opt]`, `.bxc-word` and
    `.bxc-reject textarea` for `.hfb`, `.bxc-field[data-field]` for
    `.hfield`). `harness-ask.js` still exports `permissionTpl` and
    `questionTpl` (they draw the run's park; the card model argument is
    ignored), and `chat-cards.js` still exports `approvalTpl` (drawn by
    the kit). A
    message, a thought, a notice, a journal line and a subagent's card
    keep `.msg.user` / `.msg.assistant` (`.live`, `.other`), `.think`,
    `.notice`, `.step.<kind>` and `.acard` with their state classes on the
    kit's row; inside, `.bxc-text` is `.txt`, `.bxc-who` `.who`,
    `.bxc-md` `.md`, `.bxc-att-file[data-path]` `.afile[data-afile]`,
    `.bxc-acts .bxc-act` `.acts .act`, `summary` the `.th` / `.nh` /
    `.ach` line, `.bxc-nbody` `.nb`, `.bxc-children` `.acb`. A card a
    feature module draws itself (`ext.block`: a coding agent's
    `.acard.hkid`, a document's `.step.doc`) is unchanged. The window of
    rows is the kit's `ChatWindow` (`chat-window.js` keeps its class and
    methods); its every-block fallback for an xbind without
    `/vendor/scroll-window.js` is gone — the page needs a newer xbind
    than that anyway. The composer is unchanged.

- **SDK: `sdk/llm` reads a stream a data line at a time, and its types
  write the wire too** (D226; [sdk.md](/docs/sdk.md) §Calling a model).
  `llm.ReadData(r, fn)` hands each `data:` line's payload to `fn` by itself
  — how Chat Completions and the Responses API write a stream, and it reads
  a server that leaves out the blank line between events (`ReadSSE` still
  reads events as the SSE format defines them). `llm.ChatChunkChoice`,
  `llm.InputDetails` and `llm.OutputDetails` name what were unnamed struct
  types inside `ChatChunk`, `ChatUsage` and `ResponsesUsage`, so a fake
  upstream or a translating proxy can build its answers from them; code
  that reads their fields is unchanged.
- **llm-gw v11: usage, prices and upstream URLs through `sdk/llm`** (D226).
  The gateway's backend reads token usage, prices a call and joins a
  backend's base URL with the SDK's shared wire code in place of its own
  copies. What it counts is unchanged — the usage block of either wire from
  an answer's last 8 KiB, a price by the upstream's exact model id — except
  that a cost may differ in its last binary digit; a stored price may now
  carry `cachedIn` (USD per million input tokens read from a prompt cache).
  Like every builtin tile it builds against the SDK of the xbind it ships
  with: v11 needs this release's xbind ([compat.md](/docs/compat.md) "What
  this does not promise"); an llm-gw you already run keeps building against
  the newer SDK, which only grew.
- **Agent template: its model wires read through `sdk/llm`** (D226). The
  template's backend takes the wire types of Chat Completions and of the
  Responses API, the stream reader and the model list's context sizes from
  the SDK in place of its own copies. Nothing it sends or reads changes:
  requests are encoded byte for byte as before, and answers — reasoning
  under each provider's name, tool-call fragments, usage as the provider
  counted it, errors in whatever shape a server sends them, streams with or
  without blank lines — are read the same (58 recorded answers and six
  requests are pinned in its tests). A template with this change needs
  this release's xbind, as template code written against a later SDK always
  does ([compat.md](/docs/compat.md) "What this does not promise"): update
  xbind before you merge it into an instance. An instance you already have
  keeps building against the newer SDK, which only grew.
- **Frontend kit: the chat kit's views — `/vendor/chat/views.js`, `cards.js`,
  `asks.js`, `composer.js`, `window.js`, `css.js`; `KIT` is 3** (D224;
  [frontend-kit.md](/docs/frontend-kit.md) → The chat kit). What draws a
  conversation with a coding agent in xbind's Agent tab is now a set of
  modules a tile may import: lit template functions of `(block, ctx)` for a
  transcript's rows, messages, tool cards (terminal output, diffs, a
  subagent's nested calls), change sets, permission requests, plan
  approvals and questions; `<bx-chat-composer>` (the message box: text,
  files, slash commands, pickers, held messages, Send / Stop) and
  `<bx-chat-form>` (a question's form); `ChatWindow` (a long transcript as
  a window of rows); and one stylesheet as text, `chatCssText`, whose
  classes all start with `bxc-`. A view reaches its host only through the
  `ChatCtx` the host hands it. `/vendor/chat/blocks.js` gains the `host`
  block kind (a card the host draws itself), `usageBadge`, `cached` and
  `ChatCtx`'s `readonly`, `role`, `rev`, `state`, `copy`, `openDiff`,
  `signin`, `host`, `sessionRules`; `/vendor/chat/tools.js` `DIFF_LINES`,
  `lineCount`, `headLines`, `diffBody`; `/vendor/chat/ask.js` `stageFiles`,
  `fmtSize`, `ATTACH_LIMITS`. The view modules import lit by URL, so they
  load on a page with no import map. `/vendor/agent-cards.js` and
  `/vendor/agent-css.js` keep their exports (the cards are the kit's; the
  transcript's class names changed to `bxc-…` — they were never a contract).
- **Agent tab: drawn with the chat kit, and what it gains.** The tab looks
  as it did; these are new:
  - **files in the message box** — a paperclip button, a paste or a drop
    stages files as chips; they go with the message (`POST …/queue`
    `attachments`, which the session API took all along: at most 10 files,
    10 MiB each, 20 MiB together — a file past a limit is left out and
    said);
  - **an option that stops the agent asking is confirmed** — a permission
    option xbind marks `explicit` (it raises the session's mode, D225) is
    drawn as a caution and asks "Let the agent stop asking?" in the
    window's dialog before it is sent; Cancel sends nothing;
  - **a call that waits for your answer says so** — its card's badge reads
    "needs approval" while a permission request about it is open;
  - **how full the context is** — the status line says `ctx 26%` (with the
    cost when the agent reports one; amber from 75 %, red from 90 %; the
    tokens in its tooltip) where it said `52 000/200 000 tokens`;
  - **harnesses that are not offered here** — the tab's own chooser lists
    each harness the tile's rules keep out with the reason (`GET
    /agent/providers?cwd=…&all=1` `unavailable`), as the `+` menu does;
  - a question's form says "Answer … first." under itself for a required
    answer that is missing (it was in the status line), and a tool call
    without a title is read from its arguments ("Read a.go:10–49") where it
    showed the tool's name or id;
  - a session that is still starting is polled like a running turn: a tab
    that missed the one live event saying it became idle no longer stays on
    "starting".
- **A tool call's output: a whole output, then a delta.** An update that
  carries both the whole output so far and a further delta (`output` and
  `outputDelta` in one `tool.update`) is read the same way everywhere now —
  the whole output replaces what was gathered, then the delta is appended:
  `sdk/acp`'s `ToolRecord`, the chat kit's (`mergeTool`), `/vendor/agent-tools.js`
  `foldTool` (it took the whole output alone) and the iOS app's port. The
  shared transcript `output` pins it; the scripted test agent's `recap`
  prompt plays it (`sdk/acp/acptest`).

## 2026-11-14

- **Coding agents and terminals run on the workspace's LLM connector by
  default** (D226; [llm.md](/docs/llm.md) §Coding agents and terminals).
  Nothing changes for a workspace with no connector configured: no new
  variable, no new behaviour.
  - **Agent sessions**: OpenCode and Codex start pointed at the connector
    with no sign-in — OpenCode with the provider "LLM by xbin" declared
    beside the person's own, Codex through its gateway sign-in. A person's
    own sign-in keeps working and, by default, wins (the agents policy's
    `credentials: own-first`, D229); a workspace admin can make the
    connector the rule (`bx org agents --credentials connector`) or take it
    away from agent sessions (`bx llm sessions agents=off`). Claude Code and
    Gemini CLI keep running on the person's own sign-in. `POST /api/xbin/term/sessions`
    takes `llm: "connector" | "own"` (`bx agent run --llm`), a session row
    gains `llm`, a `notice` event says when the connector serves, and `GET
    /api/xbin/agent/providers` gains `llm` per row — `{use, connector,
    note?, signedIn?}` ([protocol/agents.md](/docs/protocol/agents.md));
    rows are unchanged without a connector.
  - **Terminals** get `XBIN_LLM_URL` and `XBIN_LLM_KEY`; `OPENAI_BASE_URL`
    and `OPENAI_API_KEY` only with `bx llm sessions openai-env=on`.
  - **The credential is a per-session LLM key** (`xbl_…`), never the
    terminal token: good for the connector's model routes only, metered to
    the session's person and tile, revoked when the session ends.
  - **Settings**: `sessions: {agents, terminals, openaiEnv}` in `PUT
    /api/xbin/llm/config` and the host's `XBIN_LLM_CONFIG` (lockable), read
    back by `GET /api/xbin/llm` and `/llm/config`; `bx llm sessions`.
- **A tile bound to `xbin:llm` can ask for an LLM key** for a coding tool it
  runs in a sandbox: `POST /api/xbin/llm/keys` → `{id, token, expires, …}`,
  `DELETE /api/xbin/llm/keys/<id>` ([protocol/llm.md](/docs/protocol/llm.md)
  §Keys for a tile's harnesses). The key is for the person the tile's
  credential acts for, and no one else.
- **A sandbox can reach the LLM connector, and nothing else of xbind**:
  a tile sandbox created with `net.llm: true`
  ([protocol/sandboxes.md](/docs/protocol/sandboxes.md)) gets the
  connector's model routes at `http://10.0.2.2:4180/v1` — its SandboxInfo
  says so in `net.llm` — for the holder of an LLM key, with any egress,
  `none` included. A command's `env` there may now carry `XBIN_LLM_URL`
  and `XBIN_LLM_KEY` (every other `XBIN_*` is still refused, and so are
  these two in `defaults.env`). Go SDK: `xbin.SandboxNet.LLM`, `SandboxNetInfo.LLM`,
  and **`xbin.LLMKeyFor` / `xbin.RevokeLLMKey`** for the key
  ([sdk.md](/docs/sdk.md) §An LLM key for a coding tool).
- **Sandbox-manager contract: the optional capability `llm`**
  ([sandbox-manager.md](/docs/sandbox-manager.md) §The LLM connector in a
  sandbox). A manager that offers it takes `llm: true` on `POST
  /sbx/sandboxes` and the sandbox then carries `llm: {url}` — where a
  process inside reaches the workspace's LLM connector. Nothing changes for
  a manager that doesn't offer it: a consumer reads `hello.caps` first. The
  builtin `coding-sandbox` manager offers it on an xbind that forwards the
  connector (the runtime's `caps` carry `llm`), and the conformance suite
  (`sdk/sandboxcontract`) gained the section `llm`. A manager that offers
  it accepts `XBIN_LLM_URL` and `XBIN_LLM_KEY` in a command's `env`.
  (`sandbox-terminal` v11 is its tests' copy of the fake manager following
  this; nothing changes for people or managers.)
- **Agent template: coding agents start on the workspace's LLM connector**
  when they have no sign-in (D226; the template's API.md §Coding agents).
  Bind the agent's `llm` slot to `xbin:llm` and use a sandbox manager that
  offers `llm` (the builtin `coding-sandbox` on this xbind): new sandboxes
  are created with the connector asked for, and OpenCode or Codex with no
  saved sign-in and none in the sandbox runs on it, with a key xbind mints
  for the person. Existing sandboxes, saved sign-ins and instances not
  bound to `xbin:llm` behave as before. Template instances pick it up with
  a template update. New SDK call `xbin.RevokeLLMKeysLabeled` and route
  `DELETE /api/xbin/llm/keys?label=`.
- **Go SDK, `sdk/acp`**: `Provider.Connector`, `Provider.ConnectorEnv`,
  `acp.ConnectorServes`, `Provider.HasOwnLogin` and `acp.ExpandWord` — how
  each coding CLI is pointed at an LLM connector from its environment, and
  who serves a session ([sdk.md](/docs/sdk.md) §Driving a coding agent).
  `acp.Fake` takes a connector too, and the scripted agent (`acptest`)
  gains the `llm-call` script.
- **Delegation: a person's workspace agent acts as them, under a cap
  (D228).** The auth primitive the workspace agent will run on; nothing
  uses it until an xbind ships that sealed component and an admin turns
  workspace agents on, so an upgrade changes no tile's behaviour today.
  What a tile builder should know now ([auth.md
  §Delegation](/docs/auth.md#delegation-a-persons-workspace-agent)):
  - **A new header, `X-XBin-Agent`.** A call made by a person's workspace
    agent for them carries it (the sealed component's path) beside
    `X-XBin-User`, with `X-XBin-From: user:<id>` and `X-XBin-Role:
    reader | writer` — the person's level on your tile. Stripped inbound
    like every `X-XBin-*`. SDK: `xbin.Caller(r).Agent` and `.ByAgent()`.
  - **`X-XBin-From: user:<id>` no longer implies an admin.** Until now
    only an admin could call a tile's API in person, so a backend saw
    `user:<id>` only with role `admin`. An agent's call arrives as
    `user:<id>` at `reader` or `writer`. If your backend treats the `user:`
    prefix as "an admin", change it to read `X-XBin-Role` or
    `X-XBin-User-Level` (`xbin.Role(…)`, `Caller(r).UserCanWrite()` already
    do). The shipped tiles and templates judge by level.
  - **A new manifest key, `"agent"`**: `"full"` (the default), `"read"`
    (an agent may only `GET`/`HEAD` your API) or `"off"`
    ([elements.md](/docs/elements.md)). `GET /api/xbin/components` rows
    carry `agent` when it isn't full.
  - **Every write an agent makes was approved by its person, and xbind
    checks it**: a one-shot ticket bound to the exact method, path, body
    and content type, or a standing approval the person gave and can
    revoke — of that call (method and path), or of every write to your
    tile when they chose that. Your tile needs no code for this.
  - No tile can be granted or bound to a sealed component that acts for
    people: `POST /grants` and `POST /bindings` answer 409 for such a
    target.
  - **Decide what an agent may do in your tile**
    ([elements.md → A person's agent](/docs/elements.md)): xbind approves a
    *call*, and only your tile knows what the call means. A route that
    answers an approval, a permission request, a question or a sign-in must
    refuse a caller with `ByAgent()`, or the manifest says `"read"` or
    `"off"`. A backend written before this release treats an agent's call
    as its person's.
  - **The infrastructure that ships with xbind now says `"agent"`.**
    `off`: egress-approver, llm-gw, s3-archiver, sandbox-terminal, traefik,
    webhooks, the agent-messaging-bridge, coding-sandbox and scm-github
    templates, and a new workspace's admin console, organisations console
    and tile manager. `read`: prometheus-viewer and the agent template.
    **Apps stay unspecified** — the chat tile, the starter template, the
    examples: an app is what a workspace agent is for, and without the key
    a person's agent may use it fully, each write under their approval. The
    starter template gained an `AGENTS.md` that tells its builder when to
    narrow that and how to keep a route human-only. Each changed builtin
    tile is one version up (`bx builtin updates`); a template's instances
    get it with `git merge template/main`.
  - **The agent template refuses a person's agent everything but reads**
    (and stopping a run, marking one read) in its backend too
    (`_backend/wsagent.go`; API.md "A person's workspace agent"). **An
    instance made before this release can't tell the agent from the
    person**: unmodified, its routes all need the `admin` role, which an
    agent's call never has, so it refuses the agent outright; if you opened
    routes to `writer`, merge the template's update or add `"agent": "off"`
    to the instance's `xbin.json`. Letting a person's agent message a
    conversation in a template instance is a possible later addition, under
    tickets; this release has none.
- **API: `/api/xbin/delegate/…`** ([protocol/delegation.md](/docs/protocol/delegation.md)):
  the workspace's switch and a person's pause, approval requests and
  tickets (bound to the call's method, path, query, body and content
  type), standing approvals — of one call (`scope: "call"`, the default) or,
  as a separate explicit choice, of every write to a tile (`scope:
  "tile"`) — admin change sets (an admin's agent proposes grants, bindings,
  org shares and limit raises, none of them on an agent itself or of
  workspace-level power; the person approves after a recent sign-in; xbind
  applies them as the person), and the person's audit log of what their
  agent did. A `delegate` event on `/ws/events`;
  push kinds `agent.approval` and `agent.changeset`.
- **A tile's `"agent"` level is raised only by a person**
  ([protocol/delegation.md](/docs/protocol/delegation.md) §A tile's agent
  level). A manifest lowers the level at once; one that states more than
  xbind has on record, or drops the key after stating less, changes nothing
  until someone who manages the tile approves it (`GET` / `PUT
  /api/xbin/agent-levels`; an `agent-level` alert; `agentRequested` on
  `/components` rows). A tile that never stated a level is `full`, and its
  first value is taken as is. A workspace's consoles are `off` whatever
  their copy of the manifest says.
- **A delegate's call to a tile carries a fixed list of request headers**
  (Accept…, Content-Type, the conditionals, Range, Cache-Control,
  User-Agent) and a tile's answer to it no `Location`: write a tile's API
  so an agent needs neither a custom header nor a redirect.
- **SDK: `xbin.AsPerson()` and the delegate helpers** (`RequestApproval`,
  `RequestApprovalAs`, `ApprovalOf`, `StandingApprovals`, `WithTicket`,
  `WithAgentModel`, `DryRun`, `ProposeChanges`,
  `ChangeSetOf`, `DelegateRefusal` and its `Err…` values) — for xbind's own
  workspace-agent component; from any other tile they answer 401
  ([sdk.md](/docs/sdk.md)). `AsPerson()` follows no redirect. `sdk/acp`:
  `Config.Strict` — a start whose mode the harness refuses, or that asks
  for an option other than a model or a thinking level, fails (xbind sets
  it for sessions a workspace agent opens).
- **Agent sessions: `origin`.** A session row, and its history entry,
  carries `origin: "agent"` when the person's workspace agent opened it
  (`GET /term/sessions`, `GET /agent/history`). Absent otherwise.
- **`GET /tile-status`** also answers a person's workspace agent for a tile
  that person can read.

- **The LLM connector has a page and an admin console tab** (D226;
  [llm.md](/docs/llm.md)). `/xbin/llm` — xbind's own page, there whatever
  scaffold a workspace runs — is where a workspace admin sets the connector
  up: status, providers (add, edit, remove; a masked key that is never
  shown again; **Test**), default models by role and aliases, who may call
  directly, quotas (workspace total, default per person, per-person and
  per-org overrides; stop or warn) and usage per person and per component
  for today and the month, sortable. Everyone else reads **their own usage
  and quota** there. The admin console's new **workspace → LLM** tab is the
  same page; its users tab has *LLM quota…* per person and an org's page an
  *LLM quota* panel (after `bx builtin update scaffold:tiles/admin`).
  - **What a host's file sets is read-only**, marked **Set by your host**
    with its controls disabled; a connector the host locks entirely says
    so at the top.
  - **A quota that is reached raises an admin alert** (`GET
    /api/xbin/alerts`, kind `llm-quota`; a banner in an admin's shell, a
    row in the admin console): the person, the limit and where
    it comes from, whether their calls are refused or only warned about,
    when it resets, and `/xbin/llm`. The person reads a plain sentence on
    their own page; `GET /api/xbin/llm` now answers a person's `user` and
    `quota` even when the people switch doesn't admit their direct calls.
  - **One setup card for admins** while no provider exists: an alert of
    kind `llm-setup` — answered to admins and to the shell an admin
    drives, nobody else — that the admin console draws as a card, an
    admin's shell as a banner, and older shells and consoles as text
    naming `/xbin/llm`. Its `dismiss` route, `POST
    /api/xbin/llm/setup/dismiss`, ends it for the workspace; so does
    adding a provider. Nothing else changes for a
    workspace that ignores the connector ([compat.md](/docs/compat.md)).
- **Orgs constrain agents** (D229; [auth.md](/docs/auth.md) §Agent
  constraints, [protocol/orgs.md](/docs/protocol/orgs.md) §Agent
  constraints). An org can now say what agents may do on its tiles:
  `workspaceAgent` on or off, the coding-agent `harnesses` an agent session
  may run there (`any` or a list of ids), and the `models` that may see
  their data (`any`, `private`, or a list of LLM connector ids) —
  `PUT /api/xbin/orgs/<org>/agents`, `bx org agents <org>`; `GET /orgs`
  carries `agents`. The workspace sets the outer bounds and the mode for
  whose credentials serve a coding agent — `own-first`, `connector-first`
  or `connector-only` (`GET`/`PUT /api/xbin/agents/policy`, `bx org
  agents`): an org only narrows them. Workspace admins set both; an org's
  admin sets their own org, in their own session. A tile falls under the
  org that owns it and every org it is shared into, and the strictest
  wins. **With nothing set, nothing changes.**
  - **A workspace admin can pin an org's rules.**
    `PUT /api/xbin/orgs/<org>/agents/pin` (`bx org agents <org> --pin …`;
    `GET /orgs` carries `agentsPin`): the org's admins narrow below the
    pin and never loosen it. A refusal says where its rule comes from —
    "(pinned by a workspace admin)", and for a tile the org doesn't own
    the share that put it under the org ("this tile is under Finance by
    its share apps/*").
  - **An org's models rule binds agent sessions on its tiles.** Where a
    `models` rule other than `any` applies, an agent session runs on the
    workspace's LLM connector only, with the models the rule allows; a
    harness that can't run on the connector isn't offered there
    (`GET /agent/providers?cwd=<tile>&all=1` keeps it in the list with
    `unavailable`: why; the tile window's chooser shows it). Neither this
    nor the harness list governs what a person runs in a plain terminal.
  - **Agent sessions follow the tile's harness list.**
    `GET /api/xbin/agent/providers?cwd=<tile>` lists only what may run
    there, and `POST /api/xbin/term/sessions` answers 403 with the reason
    ("Finance allows these coding agents only: claude, codex (not
    gemini)") for anything else. A session already running keeps running;
    it can't be restarted, or resumed once it ended, while its harness is
    disallowed. Without `cwd` the list is the whole catalog, as before.
  - **The connection view.** `GET /api/xbin/agents/connection
    ?model=&orgs=&tiles=&whatIf=` (and `bx agent connection`) lists every
    org and tile a person can see as `connected`, `hidden` (left out of
    the scope), `blocked` (with the reason) or `no-access`, with totals
    ("14 tiles · 3 orgs connected · 6 not connected"), what an agent
    session on each tile runs on (`credentials`, `unavailable`), and what
    a model change would connect and disconnect
    ([protocol/agents.md](/docs/protocol/agents.md) §The connection view).
  - **`origin` on a session.** A `GET /term/sessions` row and a
    `GET /agent/history` entry carry `origin: "agent"` when the person's
    workspace agent started the session; absent otherwise.
  - New event type `agents` (`{org}`) when the rules change.
- **Agent preferences live on the server** (D229;
  [protocol/agents.md](/docs/protocol/agents.md) §Agent preferences).
  `GET`/`PUT /api/xbin/agent/prefs` keep, per person: the harness a new
  agent session starts with, per harness the mode, model, options and
  whose credentials (`own` | `connector`), and what their workspace agent
  may start a tile agent session with (`agentSessions`: `start` ask —
  the default — or allow, a mode per harness, the network and tile-API
  pickers). `PUT` is a JSON merge patch. Only the person's own session
  reads or writes them: a tile's credentials, a terminal's or an agent
  session's token, view-as and the root token get 403. A bypass
  (`explicit`) mode can't be saved. **The tile window's launcher and Agent
  tab now use them**: the last session kind and harness you started, and
  the mode, model and settings you last picked for a harness, follow you
  to every browser, and a new agent session starts with them. What the
  launcher remembered in this browser (`localStorage`) is moved over on
  first use and still answers where the server can't (the root token,
  `--no-auth`). New shared module `/vendor/agent-prefs.js`.
- **Open a session from outside its tile's window** (D229;
  [elements.md](/docs/elements.md) §`<bx-frame>`, §Links to a place in
  your tile). `<bx-frame>`'s `open({session})` shows one of the viewer's
  sessions on the tile — a live one's tab, an ended one's transcript — and
  resolves whether it found it. A tile's page asks with
  **`xbin.openSession({tile, session})`** (frame message
  `xbin:open-session`), and a link can carry it:
  `/c/root/?tile=apps/notes&session=<id>`. A session's tab that your
  workspace agent started is marked "your agent". The shell part is a few
  lines in the scaffold's `shell/route.js` and `shell/shell-route.js`
  (`bx builtin update`); an older shell treats the link as a link to the
  tile, and `xbin.openSession` still works for a tile on the page.
- **Admin console: agent rules.** The organisations tab gained an
  **agents** panel on each org's page and one for the workspace's bounds
  under the list (new scaffold file `tiles/admin/tabs/org-agents.js`;
  `bx builtin update`). `bx org agents` and the API do the same for a
  workspace whose console predates it.
- **xbind has an LLM connector built in** (D226; [llm.md](/docs/llm.md),
  routes in [protocol/llm.md](/docs/protocol/llm.md)). One
  OpenAI-compatible endpoint, `/api/xbin/llm/v1` (`chat/completions`,
  `responses`, `models`), in front of the providers a workspace admin
  configures — `bx llm upstream add <name> <base-url>`, keys in xbind's
  vault. It is off until an upstream exists: nothing changes for a
  workspace that doesn't set it up, and the `llm-gw` tile works as before.
  - **Tiles bind to it like to a provider tile**: an `openai` slot takes
    the new builtin ref **`xbin:llm`** (`bx bind apps/chat llm=xbin:llm`; a
    workspace admin's bind, offered in pickers once the connector is
    configured). The slot resolves to `/api/xbin/llm`
    (`XBIN_IFACE_<SLOT>_URL`, or a `{provider: "xbin:llm", …}` row of a
    multi slot; `xbin.iface(slot).url`), and `GET <url>/preferred?use=`
    answers as `llm-gw`'s does. Calls are metered to the person acting
    and the tile.
  - **Per-person metering and quotas**: `GET /api/xbin/llm/usage` (your
    own rows; an admin's: everyone's), quotas per UTC day and month in
    tokens and cost — a workspace default, per-user and per-org overrides,
    a workspace total — that stop (`429` in OpenAI's error shape, naming
    the quota and its reset) or warn (`X-XBin-LLM-Quota`). `bx llm usage`,
    `bx llm quota`.
  - **A host can configure and lock it**: `XBIN_LLM_CONFIG=<file>`
    ([config.md](/docs/config.md)) sets upstreams (keys by file), default
    models, aliases, switches and quotas; what it sets reads `locked: true,
    reason: "set by your host"` in `GET /api/xbin/llm/config` and can't be
    changed through the API (409).
  - New `bx llm` ([bx.md](/docs/bx.md)); new SDK package
    `github.com/xbin-dev/xbin/sdk/llm` (the Chat Completions and Responses
    wire types, usage extraction from a body or a stream, a price table);
    a new credential kind, the LLM key (`xbl_…`, good for the connector's
    routes only — [auth.md](/docs/auth.md) §LLM keys; nothing hands them
    out yet).
  - What a caller can't do to it: a call abandoned mid-answer is still
    metered (the provider's answer is read for 5 s more,
    `XBIN_LLM_CANCEL_GRACE`, then charged as an estimate — usage rows carry
    `estimated`); model names a caller makes up are counted together as
    `<upstream>/(unknown)`; one caller has 16 calls in flight (`429
    xbin_llm_busy`; the `concurrency` setting); an alias never takes over a
    name the configuration has; a stored key never follows an upstream to
    another address. A tile's non-primary deployments use the binding by
    the tile's edge policy: read-clamped by default (models, no calls).
  - Not yet: an admin console section, the connector in terminals and
    agent sessions, an Anthropic-wire route, `llm-gw` as an upstream.

- **BREAKING for a habit, not for tiles — fixed: a terminal could be
  opened on xbind's own directories**
  ([changes/2026-11-15-terminals-on-tiles.md](/docs/changes/2026-11-15-terminals-on-tiles.md):
  what to do instead of a terminal at a non-tile folder). A
  terminal or agent session's directory only had to be one the caller had
  terminal level on, and an admin has that everywhere, so
  `/ws/term?cwd=.xbin`, `cwd=data` and `cwd=homes` opened with that
  directory writable: the shell could not read the owner token or the
  secrets there, but could replace them, rewrite the users file and write
  into other people's homes. **A terminal now opens only on a registered
  tile or a directory inside one** (403 otherwise). A path that is not a
  tile — a plain folder such as `apps`, a directory with no manifest and
  no `index.html` — takes no terminal: open it on a tile.
- **`GET /api/xbin/frame-token`** answers `404` for a path in xbind's own
  namespace (`xbin/…`, `.xbin`, `data`, …) that is no registered
  component, and `400` for a path written with a leading,
  trailing or doubled slash. A cron job or bus subscription is registered
  only for a component's path as xbind stores it (`400` otherwise), and a
  restore takes the same.
- **Top-level names starting with `xbin~` are reserved** when a tile is
  created, cloned, imported or instantiated: they would share a system
  component's data key. An existing directory of such a name keeps
  running ([compat.md](/docs/compat.md) → Sealed components; the same
  [migration note](/docs/changes/2026-11-15-terminals-on-tiles.md)).
- **Fixed: a backend that was still starting when xbind shut down kept
  running.** Shutdown stopped the backends that were up; one that was
  being built, spawned or waited for at that moment was started (or
  finished starting) afterwards and nobody stopped it. Under systemd the
  unit's cgroup ended it; run by hand, or from a test, it stayed behind.
  xbind now starts nothing once it is shutting down and stops every
  backend it started, healthy yet or not.
- **Fixed: a restore could register another tile's cron jobs.** Restoring
  a tile registered every cron job its archive's manifest listed, whatever
  component a job named; the bus subscriptions beside them were always
  checked. A restore now registers only the jobs of the archive's own
  component. In a workspace without sealed backups the manifest is the
  archiver tile's word, so this let an archiver plant a job that calls
  another tile's backend at a chosen path and role.
- **Fixed: `GET /api/xbin/kv/res:/x/y` never answered.** A resource target
  whose path starts with `/` made the scope lookup loop forever, holding a
  CPU per request, for any authenticated caller. It now answers 404.
- **`POST /api/xbin/restore` with `file`** refuses (409) an archive whose
  manifest names another component than the one asked for.
- **Sealed components (system components): the facility, with none shipped
  yet** (D227). A release can now ship parts of xbin that run like tiles —
  in the tile sandbox, with limits, logs, cron, bus and people's
  partitions — whose code is always the running release's: no directory in
  the workspace, nothing to edit, clone, deploy or merge, updated by
  upgrading xbin. They live at `xbin/<name>` (a reserved name no tile can
  take), and nothing in a workspace declares one. **This release adds none:
  no workspace changes.** What is new for builders and admins, described in
  [sealed.md](/docs/sealed.md) and
  [protocol/sealed.md](/docs/protocol/sealed.md):
  - `GET /api/xbin/sealed` (admins): each one, its code digest, state,
    hold reason, partition mode, data format, slots and grants. The page
    `/xbin/system` and `bx system` show it.
  - Listings (`/components`, `/auth-overview`, `/grants`, `/backends`,
    `/runtime`, `/access-matrix`, `/resources`, `/cron/jobs`,
    `/bus/subscriptions`) answer as before and leave sealed components out
    unless asked with `?sealed=1`; a row then carries `sealed: true`. A
    client that shows one must not offer a terminal, source, deployments,
    ownership or grants on it.
  - An admin decides four things about one, with the routes a tile has:
    who has read access (`read` or none — an entry naming it at `write` or
    `terminal` is 400 on every route that writes entries; a pattern gives
    read), enabled/disabled/hidden (`POST /lifecycle`), its limits, and the
    bindings of the slots the release declares. A slot may have a default
    binding to a built-in provider (`xbin:llm` once the LLM connector is
    configured): it applies while no admin has bound or unbound the slot,
    and is never written to `xbin.json`. Every other
    route that names a tile answers a path under `xbin/` with a refusal
    whose text starts "`<path>` is a system component, managed by xbin:"
    (400; 403 for its resources, secrets and registrations, which only the
    component itself touches). No tile can be granted a call into one or
    bind a slot to one.
  - The app's and `bx preview --native`'s confinement of what a tile's
    native UI may name (`web/xb/tile-resource.js`, XbinCore) resolves a
    sealed component's own `/c/xbin/<name>/…` and `/api/xbin/<name>/…`
    once xbind listed it to that client, and still nothing else under
    `xbin/`. Its link is a tile link (`c/xbin/<name>/…`).
  - Its backend is the release's own build (`bin/sealed/<name>-<digest>`
    in the bundle; `make build` and `xbind sealed-build` make it). A
    production xbind never compiles one: without the build the component
    is *held* and says why. Other holds: data at its key it didn't write,
    data newer than the release reads (after a downgrade), a partitioned
    one without `--isolate`.
  - A sealed component's page left open across an xbind restart onto
    another release reloads onto the new code at its first call: its
    documents carry `<meta name="xbin-code">`, `xbin.fetch`/`xbin.ws`/
    `xbin.url` name it on calls to the component's own API (`X-XBin-Code`,
    `xbin-code=`), and a call naming other code is answered `409` with
    `X-XBin-Reload` and a `reload` event. Tiles' documents and calls are
    unchanged.
  - A sealed component's **page holds a viewer's credential**: its frame
    token (and its origin's cookie under `--tile-assets=origins`) reaches
    the component's own backend, its own files, its renewal, `whoami` and
    the events socket — nothing else (403). Only the backend's instance
    credential uses the component's resources, secrets, registrations and
    status; an admin and the root token don't. A tile's frame token is
    unchanged.
  - A sealed component's slot binds only to its default provider or one
    the release names for it; another provider is refused (400). The
    workspace `importMap` doesn't apply to its documents.
  - Its backup is made and restored only as a sealed (encrypted,
    authenticated) archive: nothing of it is archived in a workspace
    without a vault barrier, a plain archive is refused at restore (409),
    and nothing is restored while it is held for its data.
  - `bin/sealed/sealed.json` lists each prebuilt backend's SHA-256, source
    digest, SDK digest and go version; xbind runs only a file it lists,
    checked at boot and before every start (`no-build` otherwise). Its
    files on disk are checked before every start too, and its pages are
    served from the release's bytes in memory.
  - A JSON body that names a tile field twice is refused (400) on the
    routes that refuse a sealed component.
  - A row of the table may give its backend fixed environment (`Env`), a
    path xbind delivers holder notifications to (`Notify`), and reads its
    page may make beside a viewer's own (`FrameReads`: exact paths under
    `/api/xbin/`, GET only). A component whose row holds delegation
    (D228) keeps the delegation API for its page. A person's agent does
    nothing in a system component: `PUT /agent-levels` and `POST
    /delegate/approvals` refuse one, and so does every delegate call.
    xbind's notices to a holder's backend go to its row's `Notify` path
    and nowhere else; the fixture holder's page reads
    `GET /agents/connection?session=` through its row's `FrameReads`.
    Which component is the agent xbin's chrome frames (`agent` in
    `GET /delegate/state`) is the one row the table marks.
  - No preview shows a system component: `POST /preview {tile:
    "xbin/<name>"}` answers 400, and a screen render leaves it out.
  - A sealed path has one spelling, `xbin/<name>`: a leading, trailing or
    doubled slash, `./` or `a/../` in it is refused (400) on every route.
  - A sealed component's backup restores only under a backup key this
    workspace created for it — not one from an imported key bundle. To
    move one to another host, restore the workspace's own directory.
  - A view-as session opens nothing of a sealed component, an admin's
    event stream carries none of its bus events, a person's own bind of
    its slot takes only the providers the release names, and a
    partitioned one without `--isolate` runs as one shared instance only
    under `--no-auth`.
  - Backups of one are its data only; the manifest's `sealedData` is the
    data format, and a restore into a release that reads only an older
    format is refused.
  - A directory or tile literally named `xbin~<name>` shares a data key
    with a sealed component `xbin/<name>` and would lose its declared
    resources once a release ships that component
    ([compat.md](/docs/compat.md) → Sealed components).

- **Agent sessions: one sign-in hint (D225).** When a coding agent
  refuses a session or a prompt because it is signed out, the error and
  the `status` event's `detail` end with one sentence everywhere,
  `sdk/acp`'s new `Provider.SignInHint`. **Wording that changed** — a tile
  agent session's (the Agent tab's status line, `bx agent`): "‹the
  agent's message›: the agent isn't signed in — in a terminal on ‹tile›,
  run: ‹command›", where it read "… — open a terminal on ‹tile› and run:
  ‹command›  (your terminal $HOME is shared with agent sessions, so one
  login serves every tile)"; and ‹command› is now the provider's terminal
  sign-in line (`LoginCmd`: `codex login --device-auth`, `NO_BROWSER=true
  gemini` — the forms that print a URL or code instead of opening a
  browser) where it was the display line (`codex login`, `gemini (then
  choose Login with Google)`). The `login` object of a status event
  (`{needed, provider, command}`) and the agent template's texts are
  unchanged. Also new in `sdk/acp`, additive: `IsAuthRequired`,
  `SaysSignedOut`, `AuthState` (`/docs/sdk.md` "The session rules").
- **Agent sessions: a turn the agent starts on its own is followed
  (D225).** codex-acp answers a message steered just after its turn ended
  by starting a turn of its own, which no prompt's answer ends. A tile
  agent session used to read as idle through such a turn — the next
  message was prompted into it, and held messages were sent while it ran.
  Now `POST …/queue` still answers `steered`, a `status` event says
  `running` (statuses the agent's client restates as idle meanwhile are
  served as `running`, or `waiting_permission` while a request waits), a
  message sent meanwhile is held, `POST …/prompt` answers 409 as for any
  running turn, and the turn ends with xbind's own `turn.end`
  `{stopReason, detached: true}` (no `turn` number) once the agent has
  sent nothing for 5 s with no request waiting — or at `POST …/cancel`
  (`stopReason: "cancelled"`; the requests it waited on are answered
  cancelled) — followed by an idle `status`, after which held messages go
  in. Additive: a client that doesn't know `detached` shows an ordinary
  turn end. `/docs/protocol/agents.md` §Agent session events. In
  **`sdk/acp`** the rules both hosts follow are new API — `QueueState`
  (`Message`, `Flush`, `Busy`), `SteerOutcome`, the `Message*` constants,
  `DetachedTurn` and `DetachedQuiet` (`/docs/sdk.md` "The session rules")
  — and one behaviour of `Client` changed: a permission request or form
  question the agent makes outside a prompt leaves the status `idle` again
  once answered (it stayed `waiting_permission` until the next prompt).
  `sdk/acp/acptest` gains the `detach…` script (with `--steer`) to play
  such a turn. The agent template decides with the same functions; one
  thing moves there: a message sent during the agent's own turn no longer
  splits the text the agent is writing into two rows (it waits, as before).
- **sdk/acp: status words — `Busy`, `TurnOver`, `StateOf`, `Summary`,
  `Need` (D225).** New, additive (`/docs/sdk.md` "The session rules"):
  the status classes, a session's state in one word (`starting`, `ready`,
  `working`, `login`, `failed`, `stopped`), the summary an inbox compares,
  and what a session waits on a person for as a heading and a sentence.
  xbind's agent-session summaries and notifications and the agent
  template's "Needs you" now take their words from it. **Wording that
  changed** — xbind's push notifications for a tile agent session, only
  when the request says nothing of itself: a permission request without a
  title reads "The agent wants to run a command — approve or deny." (was
  "A tool call is waiting for your answer."), a question without a message
  "The agent is waiting for your answer." (was "The agent is asking you
  something."). Titles ("Permission needed — ‹tile›", "Question —
  ‹tile›"), every other body, the `term` status event and the agent
  template's texts and `harness.state` words are unchanged.
- **Agent sessions: a permission option that raises the mode says so —
  `explicit: true` (D225).** A `permission.request`'s options (the event,
  `GET /term/sessions/<id>`'s `permissions`, the tail page's
  `state.permissions`) gain an optional `explicit`: answering with that
  option switches the session to a mode beyond the agent's own asks — a
  plan approval's "yes, and bypass permissions", or a "yes, and switch to
  ‹mode›" the provider catalog can't place — unless the session was opened
  in that mode. Additive: absent on every other option, ignored by clients
  that don't know it (the app's decoder does), and xbind answers such an
  option like any other — it is there so a client can ask twice.
  `/docs/protocol/agents.md` §Agent session events. In **`sdk/acp`** the
  rule is `Provider.OptionRaises`, with `Provider.ModeOpen`,
  `Provider.ModeIDs`, `ModeOption`, `SessionState.Mode` and the new field
  `PermissionOption.Explicit`, which a `Client` now sets on the requests it
  files (`Config.Mode` is the mode the session began in). The agent
  template judges its owner-only options with the same function (it was
  its own `optionRaises`); what it refuses is unchanged.
- **sdk/acp: the tool record (D225).** New, additive: `acp.ToolRecord`
  (one tool call, its `tool.call` / `tool.update` events merged),
  `acp.ToolRecords` (a session's fold: `Apply`, `Entries`, `Newest`,
  `Observe`, `EndTurn`), `ToolEntry`, `ToolEvent` / `ParseToolEvent`,
  `ToolPolicy`, `ToolDiff`, `ToolLocation`, `ChangeSet`, `FileChange`,
  `ChangePatch`, `OutputChunk`, `StartsToolRecord`, `ToolDone`,
  `NewToolRecord`, `NewToolRecords`, the `Tool*` status constants and
  `EvFilesChanged` — `/docs/sdk.md` "The session rules". xbind's event
  pager and the agent template's backend now use it, so they agree on one
  reading of a call. What changes in the **agent template** (a new
  instance, or one that merges the template; it needs the SDK of this
  xbind, as any newer template does — `/docs/compat.md`): a call id the
  coding agent uses again in a later turn is a new card even when the
  earlier call never reported its end (it was merged into the earlier
  card); a call's kept output and its result's tail are cut between
  characters, never inside one; a tool kind ACP doesn't define is stored
  as `other` (the tool row's name was `acp:<that kind>`); a permission
  request for a call the agent has not announced yet makes the card with
  the request's arguments and content (a plan approval shows its plan
  then). The tool row's `acp` object is unchanged for every other call.
  xbind's `GET …/events?before=` pages: a page is never cut between two
  calls that share an id within one turn; nothing else moves.
- **Frontend kit: `/vendor/chat/tools.js` and `/vendor/chat/ask.js` — one
  model of a tool call and of what an agent asks; the chat kit's `KIT` is
  2** (D224; [frontend-kit.md](/docs/frontend-kit.md)). Two new pure
  modules a tile may import, made of what xbind's Agent tab and the agent
  template each had: `tools.js` reads a call from its `ToolRecord` (the
  merge of an agent's `tool.call` / `tool.update` events, headline, the
  command under it, outcome and state chips, `stripAnsi`, the output's
  tail, diffs as a patch or as the two texts, plan approval); `ask.js` a
  permission request as its card (option order, who sees an option that
  stops the agent asking, what "always" remembers), a question as a form or
  a page to open, the slash menu, the composer's words while a turn runs.
  `/vendor/agent-tools.js`, `/vendor/agent-slash.js` and
  `/vendor/signin-scan.js`'s `cleanCode` are re-exports now: every name
  they exported is there and says what it said, with the changes below.
  An event log's tool block in the Agent tab's fold (`/vendor/agent-fold.js`)
  also carries `rec`, the kit's record (its locations and files; a diff a
  later content list left out; `changed`), and a question's block its
  `mode` and `url`.
- **Agent tab: what changes in it.** Command output: a terminal hyperlink
  keeps its text (it was dropped, with everything up to the last link),
  and colours written with colon sub-parameters (`ESC[38:2::…m`), a
  character-set designation (`ESC ( B`) and the 8-bit CSI are stripped
  (they showed as stray characters). A question's form: a single choice
  written as `anyOf`, labels from `enumNames`, and schema `default`s (drawn
  chosen; an untouched field answers its default) are read; a choice
  without a title is called by its value; a required choice answered in
  its "Other" box counts as answered. A question that is a page to open
  (URL mode) is drawn as its link with Done / Skip. The iOS app's port
  follows the output rule in its next build.
- **Agent template: a coding agent's calls and requests are said by
  xbind's chat kit, and the page needs chat kit revision 2 (xbind ≥
  v0.3.80).** `model/harness-heads.js`, `model/harness-ask.js` and
  `cleanCode` in `model/harness-signins.js` keep their exports and become
  adapters from the template's rows to the kit. What reads differently:
  - a call's headline. With the agent's own label (Claude Code's
    description of a command) nothing changes. Without one, a shell command
    is read ("Python script (12 lines) → main.go", "Edit a.txt (sed)",
    else its first line) with the command under it, where it was the
    command's first hundred characters; a title of several lines is its
    first; a call with neither label nor title is read from its arguments
    ("Read /w/a.go:10–49") where it said its kind ("read"), and an untitled
    command is `make test`, not `$ make test`;
  - a command given as `script`, and a string command's `args`, are read;
  - a question's plain `enum` (no `enumNames`) is labelled by its values —
    every choice was labelled "false";
  - the slash menu also finds a command by its description, offers at most
    8 (it was 12, by name only), and opens while the draft is `/` and a
    name of letters, digits, `_`, `:`, `.` and `-`.
  The template's native view imports these models, so on an xbind older
  than v0.3.80 its `native.js` does not load: the app shows the tile's web
  page ([native.md](/docs/native.md) → Fallback), which says "This agent
  needs xbind ≥ v0.3.80". Instances you already have are not touched.
- **Admin console and organisations tile: agent rules.** The admin
  console's organisations tab gained **agents** panels on each org's page
  (its own rules, and the pin) and one for the workspace's bounds under
  the list; the organisations tile gained an **agents** card on each org
  its viewer admins, so an org's admin has a UI for their own rules. Both
  use xbind's `<bx-org-agents>` (`/vendor/org-agents.js`); the scaffold
  part is a few lines in `tiles/admin/tabs/orgs.js` and a new file,
  `tiles/organisations/agents.js` (`bx builtin update`). `bx org agents`
  and the API do the same for a workspace whose scaffold predates it.
- **docs: the protocol reference is split by topic.** `/docs/protocol.md`
  had reached the size limit of a served doc, so its route tables and
  WebSocket sections moved, unchanged, to one page per area under
  `/docs/protocol/`: `core.md`, `users.md`, `orgs.md`, `grants.md`,
  `workspace.md`, `tiles.md`, `code.md`, `runtime.md`, `agents.md`,
  `terminals.md`, `events.md`, `resources.md`, `partitions.md`,
  `backups.md`, `deployments.md`, `sandboxes.md`, `devices.md`.
  `/docs/protocol.md` still works: it keeps authentication, the routing
  rule, the tile ↔ shell, backend and filesystem contracts, a Topics table
  naming the page for every route, and each moved section's heading with a
  pointer to its new page. No route, header or wire format changed; an
  error's `docs` field still says `/docs/protocol.md`.
- **Tests: the `fake` agent provider is `acp.Fake`.** An xbind started with
  `XBIN_AGENT_FAKE` (tests, the UI harness) lists the scripted agent in
  `GET /agent/providers` with the modes `sdk/acp`'s `Fake` has — `ask`
  ("Ask before acting"), `auto`, `yolo` — where it listed `ask` and `yolo`.
  Nothing changes for an xbind without that variable.
- **Frontend kit: one markdown renderer for untrusted text, and tiles may
  import it** (D224; [frontend-kit.md](/docs/frontend-kit.md)).
  `/vendor/bx-md.js` — the Agent tab's and the screen text blocks' renderer
  — moves from the shell-only modules to the ones a tile may import: `md`,
  `mdInto`, `mdCssText`, and new `closedFence`, `mdEpoch`, `onHighlighter`.
  New `/vendor/chat/md.js` is the same renderer without the code highlighter
  up front (it is imported on the first fence that needs it; code is plain
  until then), and new `/vendor/chat/rev.js` exports `KIT`, the revision of
  the chat kit this xbind serves, for a page that must check before it
  imports the kit. The renderer has a marked instance of its own: xbind's
  modules no longer change the `marked` a document imports from
  `/vendor/marked.esm.js`. One change in what it draws, in the Agent tab
  and in a screen's text: a code fence counts as closed (and is coloured)
  only when its last line is a fence of the same character at least as long
  as the opening one — a block still being written whose last line was a
  shorter or different fence used to be coloured early.
- **Agent template: its chat's markdown is xbind's renderer, and a newer
  template needs xbind ≥ v0.3.80 for its page** (D224;
  [compat.md](/docs/compat.md) §What this does not promise). `chat-md.js`
  is now a re-export of `/vendor/chat/md.js`, and `index.html` starts
  `boot.js`, which checks the chat kit's revision before it loads
  `agent.js`: on an older xbind the page says "This agent needs xbind ≥
  v0.3.80 — update xbind" instead of the app. Instances you already have
  are not touched and keep working on this and later xbinds; this is about
  merging a newer template into an instance on an older xbind — update
  xbind first. (The native view: see the entry above.) What the template's chat
  draws differently: code is coloured up to 32 KiB a block (it was 20,000
  characters), a fence's language becomes a `language-…` class only when
  it is a plain name, and an image's placeholder carries the class `md-img`
  (it was `muted`; `index.html` colours it the same).
- **`/vendor/chat/blocks.js` and `/vendor/chat/rev.js`: the chat kit's block
  model.** New, pure modules a tile may import
  ([frontend-kit.md](/docs/frontend-kit.md)): the shape of a chat
  transcript's blocks (kinds, keys, versions), the one record of a tool
  call, what a view may ask of its host, and a reducer's helpers;
  `rev.js` exports `KIT` (1), the revision a tile probes before relying on
  the kit. Nothing draws with them yet — the Agent tab and the agent
  template are unchanged; the views follow.

## 2026-11-09

- **Agent template Projects: a new project is made in a wizard, and can
  start with built-in work** (D223). Code, Name, Sandbox, How it works, Get
  started, Review — on the web and in the app. Repos are picked from a
  readable dropdown that adds a repo when you choose it. **Get started**
  offers "Clone, build and run the tests" (task #1: it works out how each
  repo builds and tests, runs them, and records what it verified in the
  wiki) and "Create the initial knowledge base" (afterwards, your
  coordinator maps the code with research subagents and writes the
  project wiki). `POST /projects` takes `starter: {build, wiki}`.
- **Agent template: a model that goes quiet mid-answer is given 5 minutes,
  not 90 s.** Some upstreams (kimi-k3, deepseek-v4.1-flash through llm-gw)
  send a tool call whole once it is composed, so a large one, such as a file
  written in one call, was minutes of silence that the agent cut as a stalled
  stream, again and again. The 90 s bound still holds until the first byte.

## 2026-11-08

- **Links to a place in the workspace** (#7, D220;
  [elements.md](/docs/elements.md) §Links to a place in your tile). The
  shell's URL now says where the person is —
  `/c/root/?screen=<id>&tile=apps/notes&at=<the tile page's fragment>` —
  and a link to it lands there: the screen, the tile brought to the front
  (opened on the person's own screen when no tab holds it), the tile's page
  at that fragment. Switching screens or tiles adds a browser history
  entry, so Back and Forward walk them. A link to a screen or tile the
  viewer can't see (gone, not shared, no access, offloaded) says so in a
  strip above the canvas. New frame messages `xbin:location` and
  `xbin:navigate` ([protocol.md](/docs/protocol.md) §Tile ↔ shell
  messaging), and `<bx-frame>`'s `hash`, `navigate()` and `bx-location`
  event. `xbin-client.js` reports your page's fragment by itself; to be
  linkable, **read `location.hash` on `hashchange` as well as on start** —
  the scaffold's admin console and welcome notes now do. The shell part
  is new scaffold files (`shell/route.js`, `shell/shell-route.js`) and a
  few lines in `shell/bx-shell.js`: an existing workspace gets it with `bx
  builtin update`; until then its shell ignores the query, as before.
- **Agent template Projects: a project wiki, and the coordinator reads
  code** (D222). Each project keeps a versioned wiki — a Wiki tab with
  search, history and edits — that its coordinator and the coordinator's
  new **research** subagents (read-only: files, search and `git log/show/
  diff/blame`, no shell) read and write. Tasks read it (a coding agent
  from `.xbin/wiki/` in its workspace) and, when their PR opens, get one
  "dream" turn to write down what they verified (project setting `dream`,
  on by default). Writes are optimistic: a page someone changed since you
  read it refuses your write until you read it again. API:
  `GET/PUT/DELETE /projects/{pid}/wiki[/{slug}]`, `…/{slug}/history`.
- **Agent template Projects: a task follows its pull request wherever its
  coding agent opened it, and sees a merge however late** (D221). An open
  PR the agent opened itself from another branch is linked to its task when
  the task's checkout is on that branch at the PR's head (a merged or
  closed one is only named; a task holds one open PR per repo); otherwise
  the task says so, and the coordinator's new `task_link_pr` (or
  `POST /projects/{pid}/tasks/{n}/link-pr`) links it — one the task can't
  push to (another author's, a fork's) is `tracked`: its CI and reviews
  are shown, not sent to the task. Cleanup no longer counts a merged PR
  as holding commits its head doesn't contain. An open PR is polled
  every 6 h past its first day instead of no more, so a merge no webhook
  reported still lands; `task_refresh` reads a task's PRs now. Tasks
  stranded by the old 24 h stop are picked up again after the upgrade.
- **Agent template Projects: what waits for a person says what it is**
  (D221, #56). A project's page opens with **Waiting for you** — a task's
  question or approval and the coordinator's own question, in its words
  and with its age, each with Open; failures in yellow. `GET /needs` items
  carry `text` and `since`; the conversation list's count no longer counts
  tasks whose answer is merely ready. The coordinator quotes a task's
  question instead of "waiting for a person", and calls an idle task's
  answer ready.
- **Agent template Projects: a big task's own sandbox waits while it is
  made, and its coding agent signs in on its own** (D221). A fork still
  being created no longer fails the task after a few minutes. A fork no
  longer carries the project sandbox's coding-agent login (two copies of
  one login sign each other out): it uses your saved sign-in (Settings →
  Coding agents), else asks for one — a new task state, `agent-signin`.
  The coordinator gained `task_retry` for a failed workspace.
- **SDK (`sdk/acp`): a question the agent withdraws is settled** — an
  `$/cancel_request` for an elicitation now emits `elicitation.resolved`
  (action `cancel`, by `agent`), as a withdrawn permission already did
  ([sdk.md](/docs/sdk.md), [protocol.md](/docs/protocol.md)). New
  `Provider.LoginFiles`: where each CLI keeps its login.

## 2026-11-03

- **Agent template: a session file may be 128 MiB** (was 16 MiB; a run's
  files 512 MiB, was 64). That covers uploads and attachments,
  `sandbox_download`, the sandbox's `files.write` and archive extraction,
  and a channel adapter's `POST /adapter/files`
  ([agent-inbox.md](/docs/agent-inbox.md)). An adapter that refused larger
  attachments itself to match the old cap can pass them on now.

## 2026-11-02

- **Partitions: a tile's agent reads the global instance's log when its
  person can change the tile's code** (D218, #55;
  [partitions.md](/docs/partitions.md) §Logs and status). On a partitioned
  tile, a terminal, agent session or frame of the tile in a person's
  partition was refused `GET /api/xbin/logs?component=<tile>&xbin-partition=global`
  (`bx logs <tile> --global`) with 403 even for a writer of the tile. It
  now reads it when that person holds write on the tile; for anyone else,
  and for the partition's backend, it stays 403 and their own partition's
  log stays the default.

## 2026-11-01

- **App tunnels: a device paired as the tunnel's sidecar starts is let in**
  (D204; [app-tunnel.md](/docs/app-tunnel.md)). The sidecar could get the
  allow list as it stood when it came up after a newer one, and refuse a
  device xbind had just paired until the next change to the list.
- **xbin app: either side of a `split halves` drags down to 280 points**
  (D213; [native.md](/docs/native.md) §A document to read). On an iPad in
  portrait the bar stopped 32 points into the document — iPadOS kept that
  column at 448 points of its own; now the document goes down to 280 as the
  conversation does. On an open iPhone Duo the two panes keep the same
  minimum wherever the system lets the person resize them.

## 2026-10-31

- **Shell: a tile keeps the keyboard through a window drag** (D217;
  [frontend-kit.md](/docs/frontend-kit.md)). Dragging a window's title bar,
  corner or a divider while typing in a tile no longer leaves the tile
  without the keyboard: the drag takes it (so Escape reaches the shell) and
  gives it back when the drag ends, released or Escape. The IDE window's
  status bar keeps its last button clear of the resize corner.

- **xbin app: a tunnel that is down looks like a dropped network** (D213;
  [app-tunnel.md](/docs/app-tunnel.md)). Tile pages no longer get a page of
  the app's own ("the workspace's tunnel isn't connected", a 502) while the
  tunnel is down: their requests fail as on a lost connection, and when the
  workspace is reachable again — its tunnel back, or its connection back
  for a workspace reached directly — pages that failed load again and the
  others get the browser's `online` event.
- **xbin app: a document beside its conversation drags to any ratio**
  (D213; [native.md](/docs/native.md) §A document to read). On an iPad,
  drag the bar between a `split halves` pair's columns; each side keeps at
  least 280 points.
- **scm: repo stats** (D216; [scm.md](/docs/scm.md) §Repo stats). A new
  optional cap **`stats`** and route **`GET /scm/repo/stats?repo=&as=`** →
  `{repo, url, defaultBranch, openPulls, openIssues, mergedPulls: {days: 7,
  count}, defaultBranchChecks: {sha, state, url}, pushedAt, at}`: a repo at
  a glance in one read, counted as the identity sees it, cached by the
  provider at most 5 minutes (`at` says when it was read). A field the host
  can't tell is absent, never `0`. A provider without the cap answers 501
  `unsupported`; protocol stays 1. The builtin **scm-github** serves it —
  one GitHub GraphQL query for the counts (none of the REST search limit),
  the default branch's checks as `GET /scm/checks` reads them — and lists
  `stats` in hello. Instances take it with a template update (`bx template
  updates` lists the ones behind).
- **Tile sandboxes: live CPU and memory** (D215; protocol.md §Tile
  sandboxes, sandbox-manager.md §The sandbox). xbind reads every running
  tile sandbox's cgroup once a minute and keeps the last hour in memory.
  `SandboxInfo` gains an optional **`usage`**: `{at, cpuPct, memBytes,
  memMaxBytes?, pids, series?: [{t, cpu, mem}]}`. `cpuPct` is the percent
  of one core over the last minute. `GET /api/xbin/sandboxes/<name>`
  carries the `series` (oldest first); the list carries `usage` without
  it. There is no `usage` before the first reading after a start, once
  stopped, or on an xbind without cgroup delegation. A stop or a restart
  starts the series over. The Go SDK has it as `SandboxInfo.Usage`
  (`*xbin.SandboxLiveUsage`). The sandbox-manager contract gains the same
  optional `usage` on its sandbox: with `series` on `GET
  /sbx/sandboxes/{id}`, without it in the list. A manager that can't tell
  leaves it out, so consumers must treat it as optional. The builtin
  **coding-sandbox** passes xbind's through. The conformance suite
  (`sdk/sandboxcontract`) checks a `usage` only where a manager gives one,
  and that a stopped sandbox has none. Additive: older managers and
  consumers need no change.
- **Agent template: a project's page is its overview** (D214; the
  template's API.md "The overview", "Projects in the UI").
  - **New route `GET /projects/{pid}/overview?range=day|week`** (viewer
    level). One read returns the coordinator, the worker agents and their
    subagents, the repos at the scm provider (its new `stats` cap, D216) and
    the sandbox (its `usage`, D215). It also returns tokens per hour or day,
    from a new table `usage_hourly`, kept 8 days.
  - **Coordinator:** a new `project_goal` tool. The `project` stream event
    gains a `coordinator` change.
  - **Web:** where the agent serves the route, the Board tab becomes
    **Overview**:
    - A status strip: the coordinator, worker agents, subagents, tokens
      over a day or a week, source control, and the sandbox's CPU and
      memory.
    - The board, each card naming its agent.
    - A bar that shares the height between the board and the coordinator.
    - The coordinator's section: its state, Stop, Open chat, its goal in
      one line, and its newest messages in a window of fixed height.
    - On a tile at least 600 px tall the page fits the tile and the input
      stays in view at the bottom. Shorter, the page scrolls.
    - Every part has a tooltip after a short hover delay.
  - **App:** the project screen follows the same order. Tapping a status
    row shows what it counts.
  - **Kept per person:** the strip's fold and the chat's height
    (`prefs/project-status-folded`, `prefs/project-chat-height`).
  - **Compatibility:** additive. An older agent backend gets the page as it
    was. Older apps draw charts only where they support them, and an older
    build ignores the new tables. The coordinator's messages are now read
    30 at a time (`limit=30`), not 20.
  - **Instances that customised the template:** the native view has a new
    `ext.drawn()` seam, called after each draw.
    `projectFeed(app).preview(pid, n)` takes how many messages to show.
- **Agent template: the project page's bottom bar writes to its
  coordinator** (D212). On a project's page, the composer at the bottom
  sends to the project's coordinator instead of starting a new chat; it is
  hidden on the Projects list and forms. In the conversation list, **All
  projects** stays visible when Projects is folded, and says how many
  active projects didn't fit.
- **Shell: a drag no longer falls off over a tile** (D217, issue #54;
  [frontend-kit.md](/docs/frontend-kit.md)). A window's bottom-right corner
  (the tile window, floating tiles, pop-outs) is now a grip instead of CSS
  `resize`, so a fast stretch across another tile's page keeps following,
  and a resized `xbin.window()` sub-window no longer snaps back to its
  first size when its title bar is pressed or dragged.
  Every drag holds the pointer and ends cleanly — release, Escape (which
  puts it back), a lost focus — leaving tile pages clickable. Kit:
  `dragPointer` takes `ev` and `onCancel`; new `dragResize(ev, el, edge,
  opts)`.

## 2026-10-30

- **Agent template: Projects atop your conversations, and the coordinator
  as a small chat** (D209, issues #43 and #44; the template's API.md
  "Projects in the UI", "The coordinator"). On the web and in the app, Mine
  now starts with a **Projects** section above Pinned: your active projects
  (those with tasks that need you first, at most five), each with how many
  need you and a click or tap to its board, then **All projects**. Its
  heading folds it, and the fold is kept per person
  (`prefs/projects-folded`). With no project yet it is one row to the page.
  The web's "Projects" entry under Automations is gone. The app keeps
  Projects in the list's ⋯ menu. A project's **coordinator card** now shows
  what the coordinator is doing and its last three messages, with the
  message line right under them. They are refreshed when the page opens,
  after Send and when its turns start and end. New route **`GET
  /projects/{pid}/coordinator`** → `{run}`: your coordinator, or `null`. It
  never creates one; the `POST` still does that, on first use. Older apps
  need no update: it is all vocabulary they already have. **Instances that
  customised the template:** `ext.drawer` (native) now answers sections,
  drawn in Mine only and not while searching, below the scope. The web has a
  new `ext.listTop(list)` seam, and `ext.side` stays for entries under
  Automations.

- **Native vocabulary: a document to read — `canvas` rev 2 and `split`
  rev 3** (D208; [native.md](/docs/native.md) §A document to read). A
  `canvas` takes `fill` (the island takes the rest of the screen; its page
  scrolls and pinch-zooms itself, the screen no longer scrolls around it),
  `zoom` (the page zoom, 0.5–3: it reflows, and the reader keeps their
  place) and `@link {href}` on an `html` island (a tapped link still never
  navigates; your code gets its href, relative to the page). A `split`
  takes `halves`: on a regular width the two columns share it equally — a
  document beside its conversation (the second keeps at least 480 points;
  an open iPhone Duo puts one each side of the hinge); compact, it collapses
  as before. Apps that predate them show your web page when the tree uses
  them: branch on `xbin.native.supports('canvas', 2)` / `('split', 3)`.
  `bx preview --native` draws them.
- **Fix (app): `xbin.native.share({file})` with your own absolute path**
  (D208; [native.md](/docs/native.md) §`xbin.native`). The app refused
  every `file` starting with `/` — `/api/<self>/…`, `/c/<self>/…` — as
  "share: nothing to share", so a backend's file never reached the share
  sheet (the agent template's Share / export did nothing). It now takes
  your own paths as an image `src` does, and names the file by the
  response's `Content-Disposition` filename, else the last path segment (or
  a `?path=`'s) — no longer `raw?path=…`. Apps before this release still
  refuse absolute paths.
- **Agent template in the app: the report preview is a reader** (D208,
  issues #41, #49, #46). On a phone the preview fills the screen and zooms
  (pinch, or − / + in its bottom bar, kept per person like the web's
  zoom); on an iPad or an open Duo it opens
  beside its conversation, half each, the conversation live, with a Close
  (the list comes back when it closes). A report's links to the run's own
  files (its render step's `links`, D207) open the share sheet for the
  file; every other link stays inert. Older apps keep the preview pushed,
  zoom by re-rendering it, and list the linked files under it (sharing
  from those apps waits for the share fix above).
- **The agent template: reports link their files, the preview zooms, Files
  downloads and deletes in bulk** (D207, issues #46, #47, #49; the
  template's API.md §Session files + render). A report's links to files
  (`<a href="results.csv">`, relative to the report, or `session:<key>`)
  are downloads in the preview; `render_html` copies a sandbox report's
  linked files into the session files beside it and records them in its
  `render` step as `links: [{href, path}]` (`doc_write`/`slides_write`
  previews too). Every other link stays inert; the preview frame's sandbox
  gains `allow-downloads`, its only token. New routes `GET
  /runs/{id}/dl/<path>` (one file as an attachment — what the links reach,
  below an xbind path ticket) and `GET /runs/{id}/zip?path=…` (several,
  streamed). The web preview zooms (−, %, +; kept per person in
  `prefs/preview-zoom`). Files: a Download on every row, checkboxes and
  select-all to download a zip or delete with one confirmation; the app's
  Files gets Share per row and Select (Share as zip, Delete). **Instances:**
  take it with `git merge template/main`.
- **The Agent tab reviews a turn's changes a file at a time** (D210, issue
  #45; [09-terminals.md](/docs/overview/09-terminals.md) §Agent sessions).
  A turn that rewrote thousands of lines no longer renders as one huge diff.
  *This turn changed N files +a −d* (and a shell call's own files) is
  folded; open, it lists the files; a file's row opens that file's diff
  alone, its first 400 lines then *Show all N lines*. A file past the
  event's inline patch (64 KiB a call, 192 KiB a turn) loads on demand from
  the existing `GET /api/xbin/term/sessions/<id>/diff?turn=|toolCallId=&path=`
  while the session runs. In the phone app the card is folded too and a
  file opens a review screen: numbered lines, wrap or scroll sideways,
  previous and next file, the file list behind Back. The `files.changed`
  event is unchanged. `diffHTML` in `/vendor/bx-code.js` takes an optional
  second argument, the language of lines before any `diff --git` header.
- **Fix: the xbin app lost its iroh tunnel after the phone was locked**
  (D211, issue #42; [app-tunnel.md](/docs/app-tunnel.md)). Coming back to
  the app now always brings up a fresh tunnel, checks it with a request
  before using it, and the screen that was open carries on in place — no
  relaunch. Settings → Connection lists what the tunnel did and has **Copy
  diagnostics** (no keys or tokens) for a bug report. **Tiles:** a tile
  page's `xbin.bus` socket now reconnects at once on the browser's
  `online` event (the app sends one when its tunnel is back) instead of at
  the end of its backoff.
- **Fix: people without admin saw no code in a tile's window** (D206,
  issue #50; [auth.md](/docs/auth.md), [protocol.md](/docs/protocol.md)).
  `/api/xbin/code/*` and `/api/xbin/git/{log,diff,activity}` now answer a
  person who has read on the tile — what their terminal and the `/c/` plane
  already gave them — so the window's Files and Changes, and the app's Code
  tools, work for every reader. Elements still need a `code[:<tile>]` grant.
- **The shell: Tiled drops, and new screens start Tiled** (D205;
  [frontend-kit.md](/docs/frontend-kit.md) §Screen modes). On a Tiled
  screen, drag a tile's title bar onto another tile: its centre still swaps
  the two; near one of its edges the tile takes that half of the other's
  space, and the space it left goes to what shared it. Along the screen's
  edge it takes that half of the screen. While dragging, the shell shows
  where the tile lands and where the other one goes; Escape cancels. A new
  screen (the tab strip's +, New screen) now starts **Tiled**; screens you
  already have keep their layout. **Tiles:** a tile on a new screen gets a
  share of the window rather than a fixed-size card — it already must work
  at any size from about 192 px wide.

## 2026-10-27

- **The xbin app over an iroh tunnel** (D204; [app-tunnel.md](/docs/app-tunnel.md),
  [protocol.md](/docs/protocol.md), [bx.md](/docs/bx.md)). Admins turn on
  **Iroh tunneling** (admin console → workspace → app, `bx app tunnel on`,
  `PUT /api/xbin/app-tunnel`): phones then reach the workspace through
  n0's public relays or a self-hosted `iroh-relay`, with an xbin Noise layer
  inside iroh's encryption. Adding a device shows a larger QR code and a
  six-digit code to compare on both screens (`POST /devices/enroll-code
  {"tunnel":true}`, `GET /devices/pairing/<id>`, `POST
  /devices/pairing/<id>/confirm`; the page `/xbin/devices` for older
  shells). **Refuse app connections outside the iroh tunnel** (`bx app
  tunnel only on`) refuses the app's sessions and sign-ins elsewhere with
  `403 {"tunnel":"required"}`; browsers are not affected. `GET
  /login/methods` gains `tunnel {enabled, required}`; new env
  `XBIN_IROH_RELAY`, `XBIN_IROH`. Off by default: nothing changes until an
  admin turns it on.
- **App devices per person are limited — one by default** (D204;
  [auth.md](/docs/auth.md) §Device login, [bx.md](/docs/bx.md)). Adding a
  device past the limit answers `409` "you have N devices; this workspace
  allows M per person — remove one first (an admin can raise your limit)".
  Admins set the workspace default (1–32: `bx app limit <n>`,
  `GET|PUT /api/xbin/devices/limits`) and per-person overrides (0–32:
  `bx user set <id> --max-devices n|default`, `PATCH /api/xbin/users/<id>
  {maxDevices}`). Devices already paired stay (it is enforced only when a
  device is added) — but **after this upgrade someone who has a phone
  can't add a second device until an admin raises the limit**: run
  `bx app limit 3` (or whatever suits) if your people use several.
  `GET /devices` gains `limit`, `count` and a per-device `tunnel`;
  `GET /users/<id>/devices` gains `limit` and `override`; `/users` rows
  gain `deviceLimit` and `maxDevices`.
- **Tiles can ask for more memory, disk and sandbox/VM room; admins raise
  a cap when a tile meets it** (D203; [isolation.md](/docs/isolation.md)
  §Resource limits, [elements.md](/docs/elements.md),
  [auth.md](/docs/auth.md) §Ownership, [bx.md](/docs/bx.md)). A manifest's
  new `"limits"` key — `memory`, `disk` (on the tile that roots its
  scope), `sandboxes: {memory, vcpus, disk}` — and `vm` sizes above the VM
  policy are requests: nothing changes until a workspace admin, or an
  admin of the owning org up to the per-tile ceiling a workspace admin
  set for the org (`PUT /api/xbin/orgs/<org>/limits`), approves them. A
  backend OOM-killed at its cap now says so (its log, its crash-loop
  error) and leaves an alert that stays until someone decides; a full
  disk quota alerts from 90 %; both offer +25 %, +50 % or +100 %, greyed
  with the reason when the workspace hasn't the room (free space less a
  10 % reserve and what other tiles were granted but don't use). A memory
  raise resizes the running backend at once; a disk raise lifts the `507`
  within seconds. Decide on the tile's window, the shell's banner, the
  tile's ⚙ → Limits, the admin console's runtime → limits tab, the new
  `/xbin/limits` page, or `bx limits` (`ls`, `show`, `approve`, `raise`,
  `set`, `reset`, `dismiss`; `bx org limits` for ceilings). New:
  `/api/xbin/limits` routes, the `limits` event, alert kinds `quota-near`,
  `sbx-cap`, `tile-limits` and `memory` (memory pressure without a kill,
  which was `oom`), an alert's optional `action`
  ([protocol.md](/docs/protocol.md)). Not breaking: `XBIN_LIMIT_MEM` and
  `XBIN_LIMIT_DISK` are now the defaults; a `vm` size above the policy is
  still clamped until approved, now with a prompt; a tile's own top-level
  `"limits"` key, if it had one, is now read as an ask, and a malformed
  value is a manifest warning, never an error. Older shells and admin
  consoles show the alert's text, which names `/xbin/limits` and
  `bx limits`, until `bx builtin update`
  ([compat.md](/docs/compat.md) §Tile resource limits).

- **The tile window is an IDE** (D202; [elements.md](/docs/elements.md),
  [terminals](/docs/overview/09-terminals.md)). Down its left, a tool
  strip: **Files**, **Changes** and **PRs** open their list beside the
  panes, **Deploy** opens the Deployments panel as a tab, **Logs** opens a
  dock along the bottom. The rest is panes, each with its own tabs —
  sessions (Bash, coding agents), files, a file's uncommitted change, a
  commit, a change proposal, Deployments — and its own `+`, split right and
  split down; drag a tab to another pane, or onto a pane's right or bottom
  edge to split it; drag a divider (or focus it and use the arrow keys).
  A session keeps its scrollback through every move. The network, tile
  API, VM and GPU pickers, the base update and the live reload chip moved
  to a **status bar** along the bottom, for the session in view (one chip
  in a narrow window); the title bar keeps the name, the path and the ✕,
  which now always sits in the top-right corner. On a phone the window
  shows one pane. Nothing breaks: `open(layout)` takes the same values,
  the window pref keeps its fields (and adds `ide`), and
  `/vendor/frame-titlebar.js` and `/vendor/frame-panels.js` keep their
  exports. The lists and views are elements of their own —
  `<bx-code-tree>`, `<bx-code-changes>`, `<bx-code-view>`,
  `<bx-code-diff>`, `<bx-pr-list>`, `<bx-pr-view>` ([frontend
  kit](/docs/frontend-kit.md)); `<bx-code>` and `<bx-prs>` stay.

## 2026-10-26

- **Tiles can name themselves and set their own icon** (D194;
  [elements.md](/docs/elements.md)). `xbin.json` takes `title` (≤ 80
  characters) and `icon` (a name from the icon set), shown in the window's
  title bar, the sidebar, the Open-tile menu and the Tiled switcher — always
  beside the tile's path. A page can call `xbin.meta({title, icon, badge})`
  for a live title and badge in this viewer's window (not stored).
  `/components` rows carry `title`/`icon`.
- **Decisions open on the tile that asked** (D195). Grant requests,
  interfaces to bind and partition consent asks are no longer a strip at
  the top: the asking tile's window gets a key badge and a popover (fold
  it with ⌃; this browser remembers the fold until a new request), and the
  sidebar flags the tile — and counts flagged tiles on
  a closed folder. `<bx-grants>`/`<bx-bindings>` take a `tile` attribute.
- **Fix: a dismissed grant request or interface to bind came back.**
  Dismissing a few quickly lost all but one, and another open tab could
  undo a dismissal; both are fixed, and a dismissal that can't be saved
  says so.
- **A window says when its backend is compiling or starting** (D196):
  "Compiling…", "Preparing…", "Setting up…", "Starting…" in its title bar,
  in the sidebar and on the app's cards. A new `backend-starting` event and
  a `backend` field on `/components` rows
  ([protocol.md](/docs/protocol.md)); `build-start`/`build-ok` unchanged.
- **The Agent tab takes messages while a turn runs** (D197). Claude Code and
  Codex take them into the running turn at their next step; otherwise they
  wait as chips and go in when the turn ends. ⌘/Ctrl+Enter interrupts and
  sends. `POST /term/sessions/{id}/queue`, `bx agent send --queue` /
  `--interrupt`, `bx agent unqueue`. The composer now shrinks after a send
  and is capped at 40 % of the tab. The iOS app's Agent tab does the same
  (Send beside Stop, held messages as chips, "taken in mid-turn" bubbles);
  against an older xbind it waits for the turn's end, as before.
- **Coloured code** in the Agent tab, screen text blocks and the agent
  template's answers (highlight.js, closed fences only); headings, lists and
  quotes in the agent's answers get theme colours.
- **Copy with formatting** (D199). `xbin.copy({text, html})` asks the shell
  to write both flavours (the HTML is rebuilt from an allowlist); the shell's
  right-click Copy carries the selection's formatting; the agent template's
  Copy and Ctrl/⌘+C keep numbered lists and tables.
- **The agent template: needs you, more accurately** (D198). A coding
  agent's question, permission request or sign-in now sends a push and
  shows on the parent conversation at once; a coding-agent or project turn
  that stopped part-way is marked "not finished" (a cheap memory-tier check,
  the `judge` feature), with Mark as finished. **Instances:** take it with
  `git merge template/main`.
- **The agent template reads and writes Office documents and PDFs** (D201;
  the template's API.md). `doc_read` (PDF, Word, Excel, PowerPoint, CSV),
  `doc_view` (a PDF page as a picture), `sheet_write`, `doc_write`,
  `slides_write` — real .xlsx/.docx/.pptx with a preview in chat, all in
  the agent's own backend, no coding sandbox (the `docs` feature).
  **Instances:** take it with `git merge template/main`.
- **The iOS app: the keyboard can always be put away** (#30) — a Hide
  keyboard button above it in every field and composer, and a drag down a
  list, form or transcript. **Copy keeps formatting** (#34): a message's
  Copy puts HTML beside plain text, markdown messages are selectable, and
  the Agent tab gets Copy. The `button` vocabulary is rev 2 (`copyFormat`,
  [native.md](/docs/native.md)); older apps copy the markdown as before.
- **The iOS app on an open iPhone Duo** puts the navigation bar's controls
  in the system's vertical bar down the side (D200).
- **Docs: [install.md](/docs/install.md)** — installing, the first login and
  running xbin day to day (moved out of the README, which is now short).

- **Fix: the agent template's project policy said "Someone changed this
  policy meanwhile"** after the person's own repo or setup-script change,
  and wouldn't save. **Instances:** take it with `git merge template/main`.

## 2026-10-22

- **The shell: screen modes** (D193;
  [frontend-kit.md](/docs/frontend-kit.md) §Screen modes). A screen's
  Layout (the tab's or the canvas's menu) is now **Canvas · Fixed width ·
  Document · Tiled**, and a screen keeps its mode and arrangement wherever
  it goes: shared to an org (a preset for every member), as the
  workspace's default screen, copied to your screens.
  - **Document:** a row of one tile spans the page — also the tile a
    2-wide row is left with, which drew half wide before; no 1100 px cap.
    A handle on the page's right edge limits its width, centred (drag it
    back out, or click it twice, for the full width again).
  - **Fixed width:** the grid cut to 48 columns that stretch with the
    window (rows stay 48 px), so it never scrolls sideways. It keeps its
    own places: switching keeps what fits where it was, puts the rest
    below, and Canvas shows the canvas exactly as it was.
  - **Tiled:** one tile fills the screen; each tile opened splits the
    focused tile's space in two, closing one gives its space back. Drag a
    divider to resize, a title bar onto another tile to swap; on a phone
    one tile at a time.
  - **Tiles:** in Fixed width and Tiled a tile's window changes size with
    the browser window and the other tiles — make it work at any size
    from about 192 px wide (it already must on the canvas).
  - **API:** `/screens/org` and `/screens/default` take `mode`
    `canvas|fixed|doc|tiled` and an opaque `arrange` object, content
    under the revision like `blocks` ([protocol.md](/docs/protocol.md)).

## 2026-10-08

- **Fix: scm-github's "Create on GitHub…" and "Finish" stayed disabled
  while you typed.** The page stored each field but didn't redraw, so a
  button that depends on a field (the App's name; the address GitHub sent
  you to) kept the state it had when the page loaded. Instances take it
  with a template update (`bx template updates` lists the ones behind).
- **Fix: scm-github hid why an action failed.** After Finish, Check and
  save, Create on GitHub… and the other buttons, the page reloaded its
  state and that reload cleared the action's error, so a failure (no
  egress to api.github.com, an expired code) looked like nothing
  happened. The error now stays on the page.

- **The agent template: a task waiting for a sign-in stays waiting.** While
  a Projects task waited for its owner to sign in to the scm provider, its
  workspace went back to `preparing` each time the prepare step looked
  again (every 10 s, and whenever another job of the project ended): the
  task's run left the park that shows the device code (`park.signin` on
  `GET /runs/{id}/task`), moved to sleeping and came back. It stays at
  `signin`, with its code, until the credential is there. **Instances:**
  take it with `git merge template/main`.
- **The iOS app: confirmations and sheets over a pushed screen stay
  there** (D189/D190). On an iPhone, where a native view's collapsed
  `split` pushes its detail onto the app's stack (the agent template's
  conversation and what is pushed over it), a button's `confirm` (Delete,
  Close tab, Cancel task…) now asks over that screen — it showed nothing
  and popped up over the list after Back. A sheet the tile adds beside the
  split (Share, Share a copy, Add a copy of my files…) now opens over the
  pushed screen and leaves it there — the conversation closed under it,
  and the list was what showed when the sheet went. More generally, a
  sheet coming or going at a tree's top level, or beside a screen, no
  longer draws the view under it anew, so a screen keeps its scroll
  position and what was typed. **Tiles:** nothing to do.
- **The agent template: a terminal tab opened at a directory comes back.** A
  native Terminals tab opened from a conversation's Sandbox screen (Open
  terminal) or a coding agent's Terminal — dialled with `cwd=` (and
  `rows=`/`cols=`) — was refused 400 when its tab was shown again (the
  relay passed them to an attach, which takes none). It attaches to its
  shell again, as a tab without them did. **Instances:** take it with
  `git merge template/main`.
- **The shell: the focused tile holds its place** (D191;
  [frontend-kit.md](/docs/frontend-kit.md) §Document mode). While a tile
  is focused — its title bar clicked, its page clicked into, a control in
  its title bar focused — its title bar stays at the same height in the
  browser window when the page changes around it: in Document mode a tile
  above or below growing or shrinking to its content, a tile opened or
  closed, the strip above the tiles filling; on a phone the stacked cards
  changing size. The focused tile growing or shrinking itself leaves its
  title bar where it was too — at the end of the page the shell keeps
  room below it, which goes as you scroll back up. Scroll the tile fully
  out of view and it lets go until you focus it or scroll back to it. On
  the desktop canvas a tile moved by a layout change still moves, and
  dragging places tiles as before. **Tiles:** nothing to do — a page that
  grows while the person works in it no longer pushes its neighbours'
  content around.
- **The shell's strip and banners stay out of the way.** The grant strip
  (`<bx-grants>`, `<bx-bindings>`) now renders nothing unless there is a
  request or an interface to bind you haven't dismissed — no "N interface
  bindings" line, no "dismissed (N) · show" line
  ([elements.md](/docs/elements.md) §`<bx-grants>`). Warning banners
  (`/alerts`, such as a partitioned tile's trust warning) all have
  **Dismiss** now: one the server can't acknowledge is hidden for you on
  all your devices and shows again if its words change (your shell pref
  `dismissed`, its new `alerts` kind). Banners sit in the page above the
  top bar instead of over it, one line each — click one for its full
  text. The settings menu's **Show dismissed (N)** brings back everything
  you dismissed. Nothing to change.
- **Headings and text on a screen, without a tile** (D192;
  [frontend-kit.md](/docs/frontend-kit.md) §Headings and text on a screen,
  [protocol.md](/docs/protocol.md) `PUT /screens/org`). The canvas menu and
  a screen tab's menu gain **Add heading** and **Add text**: a heading
  (H1–H3) or Markdown text placed on the screen's grid, moved by its grip,
  resized by its corner, pushing tiles out of the way like a tile does,
  and edited in place (double-click; Enter, Escape or leaving the field
  saves); its ⋯ menu has Edit, Heading level and Delete. In Document mode
  each is a row. A screen keeps them in a `blocks` array beside `tiles`
  (`layout` pref; an org screen's new optional `blocks`, saved with its
  tiles under the same revision and draft rules — a PUT without `blocks`
  keeps the stored ones). Older shells and the xbin app show the screen's
  tiles as before and keep the blocks when they save; nothing changes for
  a tile.

## 2026-10-07

- **Native views: vocabulary revision 2 — navigation** (D189;
  [native.md](/docs/native.md) §Navigation, search and scrolling, §How it
  runs). For the app release that follows this server release; until then
  a tile that uses any of it on an older app shows its web page, so branch
  on `xbin.native.supports(name, 2)`.
  - **Deep links reach the native view:** a link with a fragment
    (`xbin://<ws>/c/apps/x#t=42`) starts the view with that
    `location.hash`, and a link while it runs fires `hashchange`.
  - **`split` collapses on a phone** when it has `detail`: the list, with
    the detail pushed over it while `detail` is true; Back reports
    `close`. `columns` (auto·all·detail, `columns {value}`) controls the
    columns on a tablet. A `split` without `detail` is unchanged.
  - **`toolbar place`** leading·trailing·bottom; **`actions edge`**
    leading·trailing and **`full`** swipes; a **`menu` inside a `menu`** is
    a submenu.
  - **`screen`:** `refreshing` keeps pull-to-refresh up until you clear it;
    search `submit {value}`, `scopes`/`scope` (`scope {value}`) and
    `suggestions`.
  - **`sheet detents="full"`** (a full-screen cover) and a `sheet` inside a
    `sheet` (stacked over it).
  - **`list` and `transcript`:** `anchor` (open at a child and keep it in
    place), `scrollTo` (jump when the value changes) and `edge {edge, at}`.
  - Nothing changes for a tile that uses none of it.

- **The agent template on the xbin app: the conversation list first, and
  Back** (D190; the template's API.md "The frontend"). Its native view is
  now a navigation stack whose root is your conversations — search, what
  needs you, pinned, by date, Mine / Shared / Archived, a row's swipe
  actions — with New chat and ⋯ (Automations, Projects, Coding agents,
  Settings) in the bar. A conversation, the new chat screen, a project's
  task, an automation's run, a Coding agents board row and a subagent are
  pushed, and Back returns where you came from; the conversations drawer
  is gone. Links (`#c=`, `#auto…`, `#proj…`, `#join=`) open with the list
  under them, and a restarted view comes back to the same screens. Every
  app version gets it (it uses only today's native vocabulary). An
  instance that customised the native view: `ui.drawer` and
  `ui.opening` are gone, `ui.stack` now holds conversations and pages too
  (`native/nav.js`), the main ⋯ is the list's, and `ext.drawer` rows draw
  at the top of the list.

- **The agent template on the xbin app: iPad and Duo side by side, and the
  web's remaining features** (D190; the template's API.md "The frontend",
  "Terminal relays"). With an app of vocabulary rev 2, the conversation
  list sits beside the open conversation (and what is opened over it) on
  an iPad or a Duo, and collapses to the list with Back on a phone; an
  older app keeps the phone stack everywhere. Also: **terminals as tabs**
  (several shells on one Terminals screen; leaving a tab or the screen
  keeps them running — Terminals (N) in ⋯ — and Close tab ends one),
  **Ports** on the Sandbox screen, a person's **partition forms** (Share a
  copy, Copy to my own space, Use my private resources, Add a copy of my
  files, who can see a new chat), **jump to latest** ("↓ N new" while you
  read far up; rev 2), **steering a coding agent from its card** (Send,
  Send now, Stop, Cancel task), and the composer keeps **a draft per
  conversation**. The agent backend's terminal relays take **`tab=<name>`**:
  a person's tab terminal outlives its client and is attached to again by
  name, until **`DELETE /terminals/{tab}`** or 30 minutes with no client.

- **`xbin.native.width`: a native view knows its screen's width class**
  ([native.md](/docs/native.md) §`xbin.native`). `"compact"` (a phone, an
  iPad's narrow Split View) or `"regular"` (an iPad), `null` from an app that
  doesn't say; `xbin.native.on('width', fn)` hears a change. A phone's bar
  drops its title when it holds more than about two items — lay it out with
  this. The agent template does: on a phone the conversation list's bar is
  New chat and ⋯, a conversation's New chat and ⋯ with the model, the
  sandbox and the seams' choices as submenus of ⋯, the new chat screen's ⋯
  alone (`ext.toolbar(v, {fold})` for a seam's items). On an iPad the app's
  bar names the tile and the detail the conversation, the list column is
  280–400 pt, and a list hidden with the sidebar button stays hidden — when
  a link opens another conversation, and after a relaunch.

- **Fixes from the xbin app on a simulator** (for the same app release).
  A collapsed `split` whose detail is a `nav` (the agent template's) now
  works on a phone: the conversation no longer closed as it opened, and
  what is pushed sends its events (typing, a card's Open, the transcript's
  `edge` — so jump to latest counts). An event stream (`text/event-stream`)
  a native view or a tile page reads in the app may now carry SSE comment
  lines (`:`) when it goes quiet: they push WebKit's held-back last chunk
  through (a finished turn showed as working), and every SSE parser skips
  them — a hand-written one must ignore lines that start with `:`, as the
  format says. A `nav` in a `split`'s detail column on an iPad pushes and
  goes back (it showed the screen under it, covered). A short transcript
  reports its end in view (a just-started conversation showed "↓ 1 new"). In
  the agent template, a restarted view whose saved
  conversation or automation is gone comes back on the list (or the
  Automations page) without an error.

- **Fix: a page without a `<head>` keeps its doctype first.** xbind's
  injection (the import map, the client) used to go in front of such a
  page, so a leading `<!doctype html>` no longer came first and the
  browser rendered the page in quirks mode; it now goes right after the
  doctype. Such a page now renders in the standards mode it declared — a
  layout that only looked right in quirks mode may shift. Pages with a
  `<head>` are unchanged. (A quirks-mode page also reports its viewport's
  height, so it never fitted its content in Document mode.)

- **The shell: a Document display mode, and the screen tabs in the top
  bar** (D187; [frontend-kit.md](/docs/frontend-kit.md) §Document mode,
  [elements.md](/docs/elements.md) `<bx-frame>`,
  [protocol.md](/docs/protocol.md) `PUT /screens/org`).
  - **Document mode**, per screen: a screen tab's menu (right-click) and
    the canvas menu offer **Layout: Canvas · Document**. In Document mode
    the screen is a scrolling page of rows — one tile per row, or two or
    four side by side — and every tile is **as tall as its document**: no
    fixed window height, no scrolling inside the tile. Drag a card's title
    bar beside another card to share its row, or between rows to move it;
    the tile menu's **Row** submenu does the same from the keyboard
    (1 · 2 · 4 columns, Move up, Move down). A tile whose page is pinned
    to its viewport (a terminal, a chat) stays 480 px tall and gets a
    fixed height from the handle under its card. The top bar slides away
    while you read and comes back at the top edge or when you scroll up.
    Below 820 px every row is one column. Switching back to Canvas shows
    every tile exactly where it was: the canvas geometry is never touched
    (a tile's place in the rows is a separate, optional `doc` field of its
    layout entry).
  - **Tiles:** let your page's height be its content's — don't pin `html`
    / `body` to the viewport (`height: 100%`, `100vh`) unless the view is
    an app that scrolls inside on purpose. Nothing to call: xbin-client
    already reports your height; frontend-kit.md §Document mode.
  - **Org screens** carry the mode for every member: `PUT /screens/org`
    takes an optional `mode` (`canvas` | `doc`), an org admin's change like
    the name — it never bumps the revision, and a PUT without it keeps the
    stored one, so an older shell re-saving tiles never resets it. `GET
    /screens` lists it on screens that have one.
  - **`<bx-frame>` auto-height is reactive:** setting or removing `height`
    (or a CSS height in its `style`) switches a frame between fixed and
    auto-height in place, without reloading the tile.
  - **The screen tabs sit in the top bar** on a wide screen, between the
    mark and the settings chip (scrolling sideways when they don't fit),
    which saves the 32 px row they had. Below 820 px they stay a row of
    their own; the org bar still sits under the bar on shared screens.
    Nothing to change: a saved layout, an older shell and the app read
    the same prefs as before.

- **Dismiss grant requests and interfaces to bind; no "N grants active"**
  ([elements.md](/docs/elements.md) §`<bx-grants>` and `<bx-bindings>`).
  Every pending request in the shell's decision strip, and every
  "interfaces to bind" row, has a quiet **Dismiss**: it hides the item for
  you on all your devices — the request stays pending, and other admins,
  the organisations tile and the admin console still show it. A
  **dismissed (N) · show** line brings them back, the organisations badge
  leaves them out, and a request that changes (another role or target)
  shows again. They are your shell pref `dismissed`
  (`{grants:{"from|target|role": at}, bindings:{"component|slot|kind":
  at}}`, [protocol.md](/docs/protocol.md) `/prefs`); the logic is
  `/vendor/bx-dismiss.js`. `<bx-grants>` no longer shows the "N grants
  active" line and its list of active grants with revoke: the admin
  console's *binding* → grants is the full list. Nothing to change in a
  tile.
- **A light/dark override per device** ([frontend-kit.md](/docs/frontend-kit.md)
  §Theme precedence). The shell's settings gain *This device*: Follow my
  setting · Light · Dark. Light or Dark holds in that browser only,
  whatever your Theme says; your other devices keep your Theme. The order
  is device → person → system. Tiles get it with no change: `<bx-frame>`
  asks xbind for it with `?xbin-appearance=light|dark` on the tile's URL,
  which xbind's appearance injection now honours over the person's theme
  ([protocol.md](/docs/protocol.md) §Authentication), and later
  changes arrive as `xbin:appearance` as before. The workspace root page
  template runs `/vendor/theme-boot.js` before the stylesheet, so the
  shell opens in the device's theme from its first frame; a root page from
  before this gets it when the shell loads (an admin brings it up to date
  with `bx builtin update scaffold:root`). `/vendor/bx-theme.js` adds
  `deviceTheme`, `setDeviceTheme`, `syncDeviceTheme`, `personTheme`,
  `personAppearance`, `setPersonAppearance`, `effectiveTheme`, `deviceUrl`.

## 2026-10-05

- **Projects and the scm contract: a new `scm-github` template, and
  Projects and CI in the agent template** ([scm.md](/docs/scm.md),
  [agent-inbox.md](/docs/agent-inbox.md) §scm events,
  [partitions.md](/docs/partitions.md) §Providers; the agent template's
  API.md "Projects", and scm-github's API.md). A tile that holds a code
  host's credentials now offers the new **`scm`** service (protocol 1):
  repo credentials for sandboxes, and optionally repos, pull requests,
  issues, CI, polling and events.
  - **scm-github** (`bx template new scm-github`) provides it from a
    GitHub App, which managers create from a manifest or paste on its page
    (Paste is the primary path), with a policy: which accounts and repos
    it serves, whether people may use the App's bot, workflows, reruns, a
    cap on a token's life. Each person signs in with GitHub's device flow
    from their own partition, where the sign-in stays (its refresh token
    never leaves it); the App's keys stay at the global instance; an
    unpartitioned copy hands out bot tokens only. Tokens are short-lived,
    narrowed to the repos and permissions asked, and revocable by value or
    purpose (**Revoke all bot tokens** on the page). Reads cover repos,
    pull requests (create, draft ↔ ready, comments and reviews), issues
    (words-only search), and CI — workflow runs, jobs, steps, annotations,
    logs of completed jobs (a running one is 409 `in-progress` with its
    page on GitHub) and **Re-run failed**, a person's own only. GitHub's
    webhooks (`bx expose apps/scm-github hooks=… --host …`) become the
    contract's events, delivered to the tiles bound in its `agents` slot
    that subscribed, retried for a day and listed for catching up
    (`GET /scm/events`, seven days); a person's subscriptions are made
    from their partition and their access to a private repo is re-checked
    before each event; a fork's branch is never reported as the repo's;
    token shapes in passed-through text are redacted. Leave **Expire user
    authorization tokens** on in the App's settings: with it off GitHub
    hands out tokens nothing can rotate, so a sign-in is refused (its
    grant revoked at once). Give it egress to GitHub — plain `internet`,
    or a narrowed list that includes the Actions log hosts.
  - **The agent's `scm` slot** (`bx bind apps/agent scm+=apps/scm-github`;
    for events also `bx bind apps/scm-github agents+=apps/agent`):
    `GET /projects/scm` lists your providers, `/projects/scm/signin` signs
    you in from your own space and `DELETE` forgets it after taking your
    projects' credentials out of their sandboxes; the **scm bot rule**
    (`GET|PUT /projects/scm/bot`) says who besides the agent's managers
    may name which repos for the bot where the agent works as the bot.
  - **Projects**: a coding sandbox, its repos, a policy and task
    conversations (`POST /projects`, `POST /projects/{pid}/tasks`). Each
    task gets a git worktree per repo on its own branch, a range of ports
    and the repos' setup scripts, answered by the built-in agent or a
    coding agent; at most `maxTasks` work at once, the rest queue. A
    task's sandbox gets a short-lived credential scoped to the project's
    repos (a git credential helper and `GH_CONFIG_DIR`, outside every
    repo) only where everyone who can use that sandbox may act as that
    identity; it is refreshed while a task works and scrubbed on share,
    stop, archive, delete, fork and Forget. **Big tasks** work in a fork
    of the project's sandbox, made from a fork base taken while it is
    quiet and emptied of every credential. **Make this a project…** turns
    a conversation with a sandbox and git clones into a project, the
    conversation its first task. **Open PR** (`POST /runs/{id}/task/pr`,
    or `policy.autoPR`, off by default) pushes the task's own branch —
    never a default branch — and opens a pull request per repo.
  - **The coordinator** (`POST /projects/{pid}/coordinator`, made only
    when you ask): your conversation that creates, lists, messages and
    cancels the project's tasks and reads its pull requests and issues —
    never internal reach, never merging, pushing or answering for you.
    Project updates reach it framed as untrusted, at most one wake a
    minute; a project pushes `pr-ready`, `task-failed`, `ci-stuck` and
    `all-done`.
  - **scm events** reach the agent at `POST /adapter/scm/event` (only from
    a provider bound in its `scm` slot, at the provider's global instance;
    on to a person's partition by partition mail): failing CI comes back
    to the task as one input with the failing steps' log tails, green CI
    marks it awaiting review, reviews from the repo's people are
    forwarded, a merge or close ends the task. When events don't arrive
    the agent polls.
  - **Team projects** (a partitioned agent): a definition in the shared
    space and each member's own membership — sandbox, tasks (coding
    agents allowed), coordinator and sign-in — in their own; **Work on
    this** shows the setup scripts and policy you accept first, and later
    changes to them wait for you; the shared board shows every member's
    tasks as plain text, each openable only by its member.
  - **CI in the conversation**, for a project task and for the branches
    any coding session pushed (found from git's record of pushes at each
    turn's end): a CI chip beside the coding agents chip, a CI tab in
    their dock with live job progress down to steps, logs (searchable,
    followed while a job runs where the platform allows), annotations,
    links to the platform, a person's own Re-run failed, **Watch CI
    for…**, and a card in the transcript when CI passes or fails. New
    routes under `/runs/{id}/ci`, a `ci` stream event, a `ci` key on a
    run's answers.
  - **On the web and natively**: the Projects entry with what needs you,
    the list, a project's board, New task and From issues…, settings
    (status — never a token —, repos and setup scripts, every policy key,
    members, archive and delete), the activity feed, the coordinator
    card, team boards, a task's branch and PR chips and its prep and
    sign-in cards; links `#proj`, `#proj=<id>`. Text from the provider
    (issues, task titles from issues, events) is drawn plain and its
    links only when `https`; a new project's sandbox defaults to the
    manager's image and size with `internet` egress where offered.
  - Also: every message the agent keeps, and the coding agents' output,
    mask GitHub token shapes and every credential the agent handed out
    (and now Anthropic key shapes in message rows too); a hosted
    conversation is refused a sandbox where a project's credential is
    live; a project's conversations can't be published, copied, hosted or
    moved (409), and a shared project has its sandbox to itself (409
    `sandbox-shared`).

  Not in this build: attaching a chat to a coordinator (`/project <name>`
  in a direct message). Additive: an older agent leaves the new tables
  alone; rolled back, it lists no project conversations among its chats
  (reachable by link, search and Needs) and may drop a run's `project`
  field, which the next upgrade derives again. Nothing to change.

## 2026-10-04

- **The iOS app and a tile's native view take Base Two** (D185): Concrete
  Day and Concrete Night, the cobalt accent (periwinkle in dark), status in
  its colours with a glyph and a word, square corners (2 pt) on cards,
  badges and buttons, large titles in Bricolage Grotesque 800 and terminals
  in JetBrains Mono, with the new mark and app icon. A native view's roles
  keep their names (`tone`, the type roles, gaps and heights,
  [native.md](/docs/native.md) §Tokens): `accent` is the cobalt where it was
  the amber, `largeTitle` is set in Bricolage Grotesque, `mono` (and code,
  and the terminal) in JetBrains Mono without ligatures, so `->` and `!=`
  stay two characters as on the web, and `bx preview --native` shows the
  same look the phone draws. The renderers draw no sparkles of their own:
  a `notice` in the `accent` tone leads with the info glyph, and
  `thinking` reads "Thinking…" without a glyph or motion (an `icon`
  named `sparkles` in a tile's own tree still draws one). The `composer`
  is square too: its field, its send and attach buttons and its
  attachment chips (a pill and circles before). The
  app follows the person's Theme setting (System, Light, Dark) in the
  workspace a window shows, as the tiles' own pages in it already did.
- **coding-sandbox: an open terminal or stdio socket keeps its sandbox's
  `lastActive` current** (the template's API.md, `autoStopMin`). While a
  terminal or a stdio socket — a coding agent's ACP pipe — is open on a
  sandbox, the manager now counts it as activity every 30 s, so the
  sandbox's `lastActive` keeps up with its use and its idle stop counts
  from when the last socket closed, however quiet the socket was (a long
  command with no output). Nothing to change.

## 2026-10-03

- **docs: [design.md](/docs/design.md), how a tile should look and read**
  (Base Two, D183, D184), for people and coding agents building tiles:
  ten rules (tokens only; one accent, for actions; status as a glyph, a
  word and a colour; square corners; small, steady type; dense, on a 4 px
  grid; glyphs, not emoji; flat; no brand colour fields in a tile; motion
  that only confirms), the components on the tokens (buttons, fields,
  lists, tabs, tables, badges, alerts, dialogs that show the plan before
  they ask, plates, empty states), the workspace's voice, both themes and
  what not to do, with examples. [frontend-kit.md](/docs/frontend-kit.md)
  §Theme stays the mechanism and links it. The workspace `AGENTS.md` (new
  workspaces) carries the nine rules that matter most, as *Design
  guidelines* under Theme & shell, and points there, as do the welcome
  tile's "light & dark" note (`scaffold:apps/welcome`) and the page `bx new`
  writes; any workspace's agents can read the page at `/docs/` or in
  `$XBIN_DOCS`. A workspace from before this keeps its own `AGENTS.md`
  (xbind never rewrites it), so its agents don't hear of the guide: add
  the pointer under *Theme & shell* yourself, for example
  `**Design guidelines.** Read /docs/design.md (on disk:
  $XBIN_DOCS/design.md) before you build or restyle a tile's UI.`
- **xbin's mark is the bˣ tile** (Base Two, D183): a white b with a yellow
  x raised as its exponent, on a square cobalt tile, beside the wordmark
  "xbin" in Bricolage Grotesque 800. `/vendor/favicon.svg` and
  `/vendor/logo.svg` (the mark with the wordmark) keep their URLs and carry
  the new drawings, and so do the PNGs beside them (`favicon.png`,
  `favicon-256.png`, `favicon-32.png`, `apple-touch-icon.png`,
  `logo.png`). The logo's proportions changed: `logo.svg` is 2.4:1 (3.7:1
  before) and `logo.png` 1200 × 501, so a page that showed it at a fixed
  width and height should size one side only. The tile keeps its colours in
  both themes; `logo.svg`'s word is ink on a light system and near-white on
  a dark one. xbind's own pages (sign-in, invite, "Continue as", request
  access, the partition switch page, the tile navigation hop and the
  tile-origin refusal) show the mark and the wordmark, drawn inline with
  the word in the page's text colour, and carry the mark as their favicon
  — unless the workspace set its own branding (D76): its title (after its
  icon) as before, and a workspace icon set without a title now stands
  beside the wordmark, never beside xbin's tile. The shell's header does
  the same, in its text colour, before the workspace chip
  (`bx builtin update scaffold:shell`), and the admin console's branding
  tab previews the new mark (`scaffold:tiles/admin`); until then an older
  shell keeps drawing the old mark in the accent.
- **The workspace follows your system's light or dark setting** (Base Two:
  Concrete Day and Concrete Night, D184). Settings → Theme overrides it per
  person (System, Light, Dark), and Density offers Comfortable; terminals
  follow unless you pick a palette in their settings menu, which now lists
  Concrete Night and Concrete Day. A tile document follows only when it
  opts in with `<html data-bx-theme="auto">` and links `/vendor/theme.css`;
  every other document that links the sheet stays dark, with new values
  under the same tokens (`--bx-green`, `--bx-amber`, `--bx-red` keep working
  as aliases of `--bx-ok`, `--bx-warn`, `--bx-danger`). New tokens:
  surfaces, focus, status tints, shadows, window chrome, part tabs, syntax
  and diff colours, the terminal's 16 colours, type and density
  ([frontend-kit.md](/docs/frontend-kit.md) §Theme). New modules:
  `/vendor/bx-theme.js` (follow the person's appearance from code) and
  `/vendor/bx-icons.js` (`<bx-icon name>`). xbind adds
  `<meta name="xbin-theme">` / `<meta name="xbin-density">` to a tile
  document when the person chose a theme or density, and frames hear
  changes as `xbin:appearance` ([protocol.md](/docs/protocol.md)); the docs
  viewer follows the person too, and so do xbind's own pages — sign-in,
  invite, "Continue as", request access, the partition switch page, the
  tile navigation hop and the tile-origin refusal — through the
  `xbin_theme` hint cookie the shell keeps (a UI hint, never a credential);
  their default logo is xbin's mark and wordmark (above). Corners are 2 px;
  the fonts are Instrument Sans, JetBrains Mono and Bricolage Grotesque,
  served from `/vendor/fonts/`; code faces and terminals draw no
  ligatures, and a new workspace's seeded zsh prompt draws its path in the
  terminal's bright black (a fixed 256-colour grey before, which no theme
  follows). Status
  shows as a glyph of its own shape with its word (`ok`, `info`, `warning`,
  `error`), never a coloured or breathing dot alone; emoji used as icons
  became drawn glyphs, and view-model strings lost their emoji (a test that
  matched "🔌 tile API" matches "tile API"; the agent template's sandbox
  labels lost their ▣: "▣ api-dev · /work" is "api-dev · /work", the web
  drawing the box glyph beside it). New token `--bx-mono-size` (mono beside
  UI text). The builtin tiles follow the theme as chat v5, egress-approver
  v4, llm-gw v10, prometheus-viewer v4, s3-archiver v4, sandbox-terminal
  v9, traefik v6 and webhooks v4 (`bx builtin update tile:<name>`). The
  scaffold takes it one unit at a time: `bx builtin update scaffold:root`,
  `scaffold:shell`, `scaffold:tiles/admin`, and likewise
  `scaffold:tiles/manager`, `scaffold:tiles/apidocs`,
  `scaffold:tiles/organisations` and `scaffold:apps/welcome` (`bx builtin
  updates` lists them). Until the root page is updated the shell stays
  dark, and with the new shell on an old root page its Theme and Density
  are shown disabled, saying so. `/vendor/` answers now carry an `ETag`
  (a reload revalidates to a 304) and `Vary: Origin`, and a font asked for
  with its version (`?v=`, as `theme.css` writes it) is immutable, so a
  reload draws the theme's faces from the cache ([protocol.md](/docs/protocol.md)).
  The native reference renderer (`bx preview --native`, `<xb-view>`) keeps
  drawing the app's look — amber — since the iOS app hasn't taken Base Two
  yet. **What a tile from before Base Two sees** (it links `theme.css`
  without opting in): Concrete Night's colours and faces, 2 px corners,
  `--bx-font`'s new face and 13/18 type, `.bx` buttons in weight 600 on a
  stronger edge and the focus ring — but its own palette, if it set the old
  token names, still carries (the hover, the code well, the dialog's tints
  and the accent's hover and ink are written from them); `--bx-green`,
  `--bx-amber` and `--bx-red` keep their old values there, so white text
  on them reads as before; and its own `button.primary`, `.quiet`,
  `.danger` and compact sizes stay its own (those variants and the 28 px
  controls apply only once it opts in). Nothing has to change; to keep a
  look exactly, or to opt in, see
  [changes/2026-10-03-base-two.md](/docs/changes/2026-10-03-base-two.md).
- **Fix: the Agent tab ends a session that is gone.** When its session
  answered 404 (exited or removed server-side) the tab threw instead of
  showing the transcript as ended; it now keeps it, greyed, as it does for a
  session that ends while open. Nothing to change.
- **Fix: MCP from a tile's page works** — the builtin chat tile's tools from
  the MCP servers bound to its `mcp` slot. Its client sends MCP's
  `Mcp-Session-Id` and `MCP-Protocol-Version` headers after `initialize`,
  which xbind's answer to a sandboxed frame's preflight didn't allow, so
  every call after the first failed in the browser (the chat showed
  "⚠ 0 tools"). Preflights now allow them (and `Last-Event-ID`), and
  answers expose `Mcp-Session-Id` ([protocol.md](/docs/protocol.md)
  §Authentication). Nothing to change.
- **chat tile v4**: a model round that only calls tools (no text, no
  reasoning) no longer draws an empty reply bubble above its tool cards,
  and on a narrow tile (a phone) its top row wraps instead of cutting
  "New chat" off. `bx builtin update tile:chat` takes it.
- **Fix: the shell on a phone** (under 820 px, the stacked layout) no longer
  draws the canvas's crosshair grid over the tab strip, the bind panel and
  every card's head: there is no snap grid to show there, and it is gone.
  The bind panel's provider picker takes a line of its own there rather
  than shrink to "apps/egress-appr…".
- **The grants and bindings line** reads "1 grant active", "11 grants
  active", "20 interface bindings" (no more "(s)"), and a person's own view
  (`GET /grants` scope `mine`) with nothing waiting shows no count at all;
  admins (workspace or org) keep it. The bindings count counts only the
  tiles whose wiring the person may change (`GET /bindings` `approvable`). The admin console's access map says
  "1 allowance", "2 policy rows", and on a touch or phone-sized screen
  points at the policies tab instead of saying "hover". The view-as banner and the
  partitions page are reworded ("…in every tab of this browser, until you
  exit"; "through the workspace").
- **Agent template: a narrow layout.** In a tile under 560 px (a phone's
  stacked card) the page is one column — the open conversation, or home —
  and the conversation list slides in from the top bar's ☰; Compact, Learn
  skill, Memory, Files and Delete fold behind its ⋯. Everywhere, Memory and
  Files show a count only once there is one, Delete is no longer a red
  button in every header (it turns red on hover), and the composer's
  pickers keep their width — the message row wraps instead of squeezing
  the model picker to "a…" (whose default now reads "default model"). New agents get it; an existing one through
  `bx template updates`.
- **Coding-sandbox template:** the operators' page names xbind's own runtime
  by the workspace's branding title (else "this workspace") rather than
  "xbin"; a sandbox only its owner may use reads "private"; the quota
  table's headers line up with their numbers. New managers get it (an
  existing one through `bx template updates`).
- **traefik tile v5**: its setup script unpacks the traefik binary as the
  sandbox's own user (`tar --no-same-owner`). It failed ("Cannot change
  ownership to uid 1001") where a tile sandbox maps a single uid, as on a
  rootless host with one uid. `bx builtin update tile:traefik` takes it.
- **coding-sandbox: an image's `sudo`** (`builtin-templates/coding-sandbox/API.md`
  §Images). `images[].sudo: true` lets the sandboxes' user become root
  with `sudo` (no password) in VM sandboxes on KVM (Firecracker; an
  emulated VM's get none, and hello's `notes` say so), and makes `/dev/fuse` and
  `/dev/net/tun` usable by it at every boot (rootless podman, FUSE); the
  image editor sets it (web and app), and `/ops/state` marks the
  sandboxes that have it (`sudo`). A namespace sandbox can't give it: a
  manager whose mode is `namespace` refuses such an image, and `auto`
  without VMs makes its sandboxes without it and says so in hello's
  `notes`. A changed `sudo` rebuilds an image with a setup script; images
  without it keep their builds. API.md also documents `xbin-dev`, an image
  in which a VM sandbox develops xbin itself (podman, FUSE, Node 24) — an
  example, not a default. Nothing to change.
- **VM sandboxes: `/etc/xbin-vm-devices` opens devices to the guest's
  users** ([isolation.md](isolation.md) §VM sandboxes). `/dev` is a fresh
  devtmpfs at every boot and the guest runs no udev, so `/dev/fuse` and
  `/dev/net/tun` are root's (0600). A guest's root may now list the modes
  it wants in `/etc/xbin-vm-devices` (`0666 /dev/fuse` a line), and the
  guest sets them at every boot, before anything runs — character devices
  only. Without the file nothing changes.
- **VM sandboxes: the base image's setuid, setgid and sticky bits are back
  in the guest** ([isolation.md](isolation.md) §VM sandboxes). In a VM,
  `sudo`, `su`, `passwd` and `mount` were plain 0755 and `/var/tmp`
  wasn't sticky: the unprivileged unpack of the rootfs drops those bits on
  the host, on purpose (no setuid-root program in xbind's install
  directory; namespace sandboxes run with no new privileges anyway). The
  base image now lists them in `/etc/xbin-rootfs-modes`, and the VM guest
  puts them back at every boot in an in-memory layer over the image — the
  VM disk isn't written, and the host tree and namespace sandboxes stay as
  they were. It takes a base built with the list (the next base update);
  an older one boots as before. An image of your own `FROM` the base that
  adds setuid programs runs the Dockerfile's last step again. Nothing to
  change.

## 2026-10-02

- **Agent template: the subagent and fairness limits in Settings, and a
  per-person model-call limit.** The ⚙ Config tab (and the app's Config
  screen) now edits the workflow limits `PUT /config` already took —
  `maxActiveRuns`, `maxDepth`, `maxSpawn`, `maxSpawnPerTurn`,
  `subagentTimeout` — plus a new `maxActiveRunsPerUser`: how many of one
  person's model calls run at once in a partitioned agent (default 2, as
  before; never above `maxActiveRuns`). At 2 a person's subagents run their
  model calls one at a time, because a subagent never takes the last free
  slot; raise it for wider fan-outs. A person's partition follows a change at
  its next model call, without a restart. Managers (anyone with write access
  to the agent, which includes every workspace admin) set them, once for the
  whole tile. Nothing to change.

## 2026-10-01

- **Fix: an unmounted encrypted volume's gocryptfs now ends, and a volume
  never runs two** ([partitions.md](/docs/partitions.md) §How people's
  partitions run, [resources.md](/docs/resources.md)). Every sandbox or
  terminal started while a volume was mounted carries a copy of its mount,
  so a person's volumes unmounted after the idle hour kept their gocryptfs
  processes running (~9 MiB a person) until those sandboxes ended — and
  the person's next start ran a second gocryptfs on the same encrypted
  data. xbind now removes a volume's mountpoint when it unmounts it (idle,
  a reset or removal, a seal, shutdown), which ends those copies too, and
  waits for the process to exit; before mounting a volume it ends any
  earlier gocryptfs still serving it. Nothing to change.
- **Fix: partitioned tiles — a start turned away at the running caps no
  longer leaves the person's volumes mounted**
  ([partitions.md](/docs/partitions.md) §How people's partitions run,
  [resources.md](/docs/resources.md)). The 503 `too many people's
  instances of <tile> are running`, or a cron, bus or mail start
  deferred, kept the encrypted volumes xbind had mounted for that start —
  two gocryptfs processes per person — for the idle hour, exactly while
  the caps were full; they now unmount at the disk monitor's next pass,
  within a minute, unless an instance of the person runs by then. Nothing
  to change.
- **Partitioned tiles: higher default running caps, from a measurement**
  ([partitions.md](/docs/partitions.md) §How people's partitions run,
  [protocol.md](/docs/protocol.md) `POST /partitions/limits`). The default
  caps on people's instances running at once — clamp(M/4 ÷ E, 4, 32) per
  tile, clamp(M/2 ÷ E, 8, 128) per workspace — now take E = 96 MiB, from
  a measurement of a person's agent partition with its gocryptfs
  processes (about 56 MiB resident) plus room for heavier use, instead of
  the 160 MiB placeholder: a 4 GiB machine runs 10 per tile and 21 in all
  (was 6 and 12), 8 GiB 21 and 42 (was 12 and 25). Caps an admin or a
  tile manager set are unchanged. Nothing to change.
- **xbin app: partitioned tiles** ([partitions.md](/docs/partitions.md)
  §Your partitions page; D181). A partitioned tile's row and card carry
  the web shell's marker, the half-split teal disc (VoiceOver reads its
  words); a tile whose partition mode switch waits for a manager says
  **paused** and opens xbind's switch page, never its native view, until
  the switch is decided. The pushes that link the partitions page
  (`tile.partition-switch`, `-deleted`, `-consent`, `-reset`,
  `account.credential` — which the app now registers for) open it in the
  app, top level in a web view of its own signed in through a one-shot web
  ticket (“Continue as …” once), grouped apart from the tiles' and agents'
  notifications and offering no “Mute this tile”; Settings links it as
  **Your partitions** while you see a partitioned tile. Feature-detected:
  on an xbind without the page (`partitions-page/1`) the link opens the
  workspace and Settings shows no entry. Nothing to change.
- **One set of workspace settings: base auto-update and the partitioned
  tiles' switches together** ([bx.md](/docs/bx.md) §bx settings,
  [protocol.md](/docs/protocol.md) `/workspace-settings`,
  [partitions.md](/docs/partitions.md); D180). The two admin switch sets —
  `bx settings` / `/workspace-settings` (base auto-update, D175) and `bx
  policies` / `/workspace-policies` (`partitionConsent`,
  `credentialResetConfirm`, v0.3.66) — are one: the **workspace
  settings**, kept in `data/workspace-settings.json`, with one admin
  console tab (workspace → **settings**, grouped as Terminals and
  Partitioned tiles; the terminals and policies tabs are gone, their links
  open it) and one event. `GET /workspace-settings` now also carries
  `partitionConsent` and `credentialResetConfirm` (for people and admins;
  other tile code gets base auto-update alone, as before), and `errors`
  (why a setting can't be read); `PUT` takes all three; the
  `workspace-settings` event says which keys a write set (`changed`) and
  carries no partitioned tiles' switch. `bx settings` prints every
  setting by topic (`--json` too) and sets any of them — `bx settings set
  partition-consent on`, `set base-auto-update off`, or D175's
  `--base-auto-update[=…]` flag form, now for every name. Each switch
  keeps its default, who may read and set it, its fail-safe value when the
  file can't be read (base auto-update off, a partitioned tiles' switch
  its last value or on), its audit line and its timing (a credential reset
  waits for the person to confirm, or 24 h after they're notified).
  **Nothing breaks:** `bx policies` works as before (with a one-line note
  pointing to `bx settings`); `GET/PUT /workspace-policies` answer as
  before from the same store, and a change of a partitioned tiles' switch
  still publishes `policies`, so an older admin console, shell or bx keeps
  working; a new admin console against an older xbind reads what it has.
  The first read after the upgrade imports `data/workspace-policies.json`
  into the settings file, once; that file stays, and every change of
  either switch is written there too, so a downgrade to v0.3.66 keeps them
  (and what it changes there is imported again on the upgrade back). The
  admins' alert for a setting xbind can't read is kind
  `workspace-settings` (it was `policies`), now for base auto-update too.
  Nothing to change.
- **Agent template: coding agents sign in with a link and a code, and a
  person keeps saved sign-ins — several per coding agent — in their own
  partition** (the template's API.md §Coding agents → "Signing in", "Saved
  sign-ins", "Guided and saved sign-ins in the UI";
  [sandbox-manager.md](/docs/sandbox-manager.md) §hello; [sdk.md](/docs/sdk.md)
  `Provider.Mint`, `Keys`; D179). A coding agent waiting for its sign-in
  offers **Sign in to Claude Code**: the agent runs Claude Code's own
  `claude auth login` in the sandbox and hands you **Open sign-in page ↗**,
  **Copy link** and a field for the code (`POST /runs/{id}/harness/
  authenticate {method: "guided", code?}`), **Use a terminal instead** as
  before. In a person's own partition **Remember for my other sandboxes**
  runs the official `claude setup-token` instead and keeps its one-year
  token as a **saved sign-in** in their partition's vault — it never
  reaches a page, a row or a log; a key or token can be pasted too
  (Anthropic, `CODEX_API_KEY`, `GEMINI_API_KEY`, opencode's provider keys:
  `GET/POST/PUT/DELETE /prefs/harness-signins`). Saved sign-ins are named
  ("Personal", "Work"), one per coding agent the default, and go into a
  coding agent's environment — winning over the sandbox's own sign-in —
  only in the person's own conversation, in a sandbox of theirs no one
  else uses and no hosted conversation ever worked in; never copied into
  a sandbox's home (Codex, which writes a key it is handed to its
  `auth.json`, has that file removed at once, before a switch, a share
  and at Forget). Whatever a coding agent prints is redacted before it
  is kept or shown: its secret, and anything shaped like an Anthropic
  token, become `[redacted]`. `setup-token` runs the image's own `claude`
  with an empty environment and a throwaway home. Sharing a sandbox
  through the agent first stops the coding agents holding a saved
  sign-in there (or refuses: 502); a new secret stops the ones on the
  old one. A conversation shows
  `· using Work` on its coding agent's ▾ and switches account there (`PUT
  /runs/{id}/harness/signin`): the adapter restarts with the other sign-in
  and resumes the same session. A refused one is marked and left out
  until signed in again; it warns 14 days before it expires; Forget stops
  the coding agents using it, and a conversation that had picked it uses
  the sandbox's own sign-in (never the default) until another is picked.
  A person's space stopping stops their resting coding agents that hold
  one. Residual risk, in the template's API.md: a process of the person's
  own in their sandbox can read the coding agent's environment; the
  coding-sandbox's operators and xbind's admins are in the trust base.
  Unpartitioned agents keep the per-sandbox
  sign-in (and the guided one); the shared space has none. Also: an API
  key now reaches Gemini CLI's `authenticate` in the shape it reads (the
  key itself — it read the object it was sent as no key), and a sandbox
  manager's advertised `login` wins over the template's own (an older
  coding-sandbox's `claude /login` keeps working; Claude Code's terminal
  sign-in is `claude auth login`, without `CLAUDE_CODE_REMOTE`). Nothing
  to change.
- **Claude Code signs in once, with a link and a code — guided in the
  Agent tab; terminal links open whole**
  ([09-terminals.md](/docs/overview/09-terminals.md) §Agent sessions,
  [protocol.md](/docs/protocol.md) `GET /agent/providers`,
  [elements.md](/docs/elements.md) §`<bx-terminal>`, D178). Claude Code's
  sign-in is now `claude auth login` — one sign-in, no first-run screens,
  a link to open and the code it shows pasted back — where `claude /login`
  signed a fresh home in twice (its onboarding, then the command). When a
  Claude Code agent is signed out, the Agent tab's **Sign in** runs it in a
  terminal session of its own that no tab shows — in the browser, and in
  the xbin app's session lists, which skip it too — and offers **Open sign-in
  page ↗**, **Copy link**, a code field with **Finish**, and the CLI's own
  word on how it went; **Use a terminal instead** opens the shell tab as
  before (codex, gemini and opencode sign in that way still). `GET
  /agent/providers` gains an additive `signin` on claude: the command and
  how to read its output (sdk/acp `Provider.Signin`, `Signin.Scan`), for
  any client that wants to drive it. In every terminal a click on an OSC 8
  link opens its whole target from any row it spans — at once when the row
  shows part of that target, after a confirm naming the real host and URL
  when it shows something else — a URL broken over rows is joined back,
  and a program's OSC 52 copy (Claude Code's "c to copy") reaches the
  clipboard while the terminal has the focus. Amended after a security
  review: a link that shows one URL and opens another asks first, a URL
  with a user part never opens, an OSC 52 clipboard read is never
  answered; the sign-in's session is marked by xbind
  (`/ws/term?purpose=signin` → `purpose: "signin"` in `GET
  /term/sessions` rows and `term` events; any other purpose 400), not by
  its name — clients hide shell rows with that purpose, `PATCH
  /term/sessions/<id>` refuses the name `xbin:sign-in` (400) and renaming
  such a session (409), and it ends after 15 minutes; `signin.url` is
  anchored and `signin.hosts` match exactly (no subdomains). Claude Code agents no longer
  run with `CLAUDE_CODE_REMOTE=1`, which put them in Anthropic's own
  remote-session mode (auto memory off, a 2-minute API timeout, a
  `bypassPermissions` default mode in your settings refused); the
  adapter's sign-in methods are now `claude auth login`'s. coding-sandbox:
  a new manager advertises `claude auth login` for Claude Code; a saved
  config keeps its login, and an older one still works. Nothing to change.
- **BREAKING: existing agent instances ask to become partitioned when they
  take the template's update**
  ([changes/2026-10-01-agent-instances-partitioned.md](/docs/changes/2026-10-01-agent-instances-partitioned.md);
  [partitions.md](/docs/partitions.md) §The mode, the agent template's
  API.md "Partitioned instances"). On an xbind with `--isolate`, the agent
  template's served repository now asks every instance for `"partition":
  ["user", "global"]` (its block's new `"partitionOnUpdate": true`), so an
  instance made before the partitioned default — or opted out of it — gains
  that line when it merges the update (`git fetch template && git merge
  template/main`). One that holds data then pauses for a tile manager:
  **switch** deletes its conversations, memory and schedules (nothing is
  migrated) and starts it partitioned; **Keep the current mode** runs it
  unpartitioned again, deleting nothing. An empty one switches at once.
  Without `--isolate` nothing asks. The template merge driver takes an
  upstream `partition` where neither the base nor your manifest names one;
  a mode you wrote stays yours. Other builtins and `bx builtin update` are
  unchanged.
- **Partitioned tiles: people's partitions swap, and their terminal layers
  move base, like a tile's** ([partitions.md](/docs/partitions.md) §How
  people's partitions run, §Terminals and agent sessions;
  [09-terminals.md](/docs/overview/09-terminals.md) §Base images and their
  lifecycle). What the runtime fixes below (a request that meets a swap,
  pausing live reload while a save builds, base auto-update) do for a tile's
  own instance they do for each person's partition of a partitioned tile
  too: a request routed to a partition's old generation just as a swap
  stopped it, which it never answered, goes to the partition's new
  generation when sending it again is safe — the same partition, through the
  same admission, whose refusal answers 503 with its reason as a first
  request's does (fix below); and a partition's work-tree build waits for a
  live-reload pause in progress, and never serves once the pause pinned the tile — its
  token revoked before it stops, as every partition's stop does. Base
  auto-update moves each person's terminal layer on its own, at the start of
  that person's next session, never under a running one of theirs, and
  nobody else's layer with it, and its lines name it as theirs (`xbin: your
  terminal on <tile> moved to the new base image — …`, where a tile's
  layer says `this tile's terminal`); `GET /ws/term/env` and the terminal window's
  base-update offer are that person's layer's. The admin console's workspace
  tab has both the terminals (base auto-update) and the policies sub-tabs;
  `bx settings` and `bx policies` are separate commands. `sandbox-terminal`
  is v8: v6 and v7 (below) plus its tests' fake manager following the
  contract's partitioned consumers. Nothing changes for a workspace without
  a partitioned tile. Nothing to change.
- **Fix: a request a swap cut off, sent again where nothing can serve it,
  answers as a request arriving then would**
  ([elements.md](/docs/elements.md) §Runtimes & backend lifecycle). When
  the generation a request was sent again to (after its old one was
  swapped out under it) can't be had, the answer was always a 502 naming
  the failure; now it is what the same request gets when it arrives after
  the swap: a deployment removed meanwhile 404, a person's partition refused
  at its cap 503 with the reason, a failed build 502 `backend build failed`
  with the build output. Nothing to change.
- **Fix: the agent template's engine no longer stops on a failed read of
  its epoch** (the template's API.md is unchanged). Every write the agent
  engine makes first reads the engine epoch, the fence that keeps a
  replaced backend from writing; a read that failed was taken for epoch 0,
  so the only engine logged "another engine took over this database" and
  stopped driving runs until the next restart. Now a failed read is tried
  again (a few times, within a fraction of a second) and, if it keeps
  failing, that one write is dropped with a log line while the engine
  keeps running; the coding-agent pipe's ownership check does the same,
  and a takeover never rewinds the epoch to 1 over a failed read. Nothing
  to change.

- **Sandbox managers: a user partition's person on terminals and stdio
  sockets, and what one shared sandbox shares**
  ([sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned consumers,
  §Terminals, §stdio, §hello). The contract now says what its partitions
  rule already meant for the routes coding agents added: from a
  partitioned consumer's user partition the person is the partition's and
  verified on the `tty` and `stdio` routes too — a manager with
  `partitions` applies the person rules itself, and an `Sbx-User` naming
  anyone else is `403 not-allowed`; "asserted, the consumer's to check" is
  an unpartitioned consumer's backend call (or a global instance's).
  A partition id alone names no consumer — one person's partitions of two
  consumers may carry the same id — so a manager keys on both.
  `sdk/sandboxcontract`'s `user-partitions` section gains `sockets` (run
  where hello offers `tty` or `stdio`): another partition, the same
  person's partition of another consumer, the consumer's global instance
  and a person it names get `404` on a partition-homed sandbox's terminals
  and stdio sockets, the partition naming someone else `403`, a refused
  dial takes no stdin, and a partition's exec named under a sandbox the
  caller does see is `404`; `apart` checks the other consumer's partition
  of the same person on the HTTP routes. `hack/fakesandbox` and
  `coding-sandbox` pass. §Partitioned consumers names two more things the
  partitions that see one sandbox share: an exec's stdio socket, whose
  attach takes over its stdin (a coding agent's), and the sandbox's home
  directory, where a coding agent's sign-in serves whoever runs that agent
  there afterwards; hello's `harnesses[].login` adds that a partitioned
  consumer should offer the sign-in only in a sandbox homed in the
  person's own partition (the manager can't tell a sign-in from another
  terminal), and that whoever sets `argv`/`login` is in its users' trust
  base. `ManagerTTYOptions.User` and `ManagerStdioOptions.User` say the
  same, for a manager with `partitions` (a partitioned tile uses no other
  from a person's partition: one without it takes the call as the tile's,
  the person asserted). Nothing changes for an unpartitioned consumer or a
  manager without `partitions`.
- **The agent template: coding agents only in your own conversations**
  (the template's API.md "Partitioned instances" → "Coding agents only in
  your own conversations", and "Coding agents" → "In a partitioned
  instance (the UI)"). In a partitioned agent a coding agent (Claude Code,
  Codex, Gemini CLI, opencode) signs in inside its sandbox, so it works
  only where that sign-in stays its person's: their own conversations, in
  a sandbox homed in their partition.
  - The global instance never starts, drives or signs one in: `POST /ask`
    and `POST /runs` with `harness`, `POST /runs/{id}/harness/authenticate`
    and the run terminal's `login=1` answer 409 there, its catalog lists
    each coding agent unavailable (`reason: "shared-space"`), its agent's
    `subagent_spawn` has no `harness` — so a shared conversation, a
    channel, a trigger or a schedule never reaches one. Its page offers no
    "Who answers", and a new chat shared with others is the built-in
    agent's.
  - A non-secure (hosted) conversation has none, one a coding agent
    answers can't be hosted, and a hosted conversation doesn't work in a
    sandbox of its host's where a coding agent of theirs signed in or
    worked.
  - A coding agent's conversation — or one where a coding agent it started
    is still at work or still running — can't be published, copied, hosted
    or moved to another space (409 `{error, runs}`, the runs it waits on);
    the page offers none of those on one. Its owner's un-share of one at
    the shared instance answers 409 (it would move it); a member may still
    leave it. One already in the shared space (from before this rule) is
    read, not driven: no Retry, no message, its mode and options not
    switched.
  - In your own partition a coding agent starts only in a sandbox of your
    own space: `GET /sandboxes` says of each sandbox there whether it is
    `homed` (and `why` not), the catalog keeps only yours, the pickers show
    the team's sandboxes as not fitting with the reason — the built-in
    agent's too — and offer Create; its terminal, sign-in and log are
    refused for a sandbox not homed there (403) or a manager that can't
    keep people apart (409). A sign-in is offered only there; elsewhere its
    card says why. A shared conversation's coding agent is reached at the
    shared instance (its buttons, the app's terminal).
  - Only a coding agent at work (a turn, or a sign-in the agent waits on)
    keeps your partition running — never one idle or waiting for you. An
    idle one is stopped at its idle time: while your partition idles it
    keeps a `wake` cron job registered for that minute (never an
    every-minute `resume`), which brings a stopped partition back to stop
    it, also under a halt. A manager's halt reaches a coding agent's turn
    at its next step, or within a few seconds when it says nothing, and
    one waiting for the settings to be read doesn't start. `GET /usage`
    adds each person's coding-agent sessions (`harnessSessions`).
  - Fixed: a person's partition now lists the team's sandboxes (those it
    may use at the shared instance, read-only there: their terminal opens,
    conversations there don't use them) — they were missing, and a shared
    conversation's sandbox terminal from your own partition didn't open;
    and a person's partition's `resume`/`wake` jobs are kept registered
    while it idles rather than left at its exit, which xbind refused (its
    token is revoked before it stops), so a sleeping run's wake-up or a
    coding agent's idle stop was lost when the partition stopped.

  Unpartitioned instances change nothing. Nothing to change.
- **Partitioned tiles: a stopping partition's token goes before its
  process** ([partitions.md](/docs/partitions.md) §How people's partitions
  run). Said now, as it always was: every stop of a person's partition —
  idle, its person's own, a switch, their removal — revokes its instance
  token first, so its backend's calls to xbind while it exits (a cron job
  registered at SIGTERM) are refused; register what should bring it back
  while it runs. Nothing to change unless your partitioned backend does
  that at its exit.
- **Agent template: a coding agent's `activity` no longer reads `idle`
  while it works.** The summary's `activity` (the `harness` event,
  `/tree`, `/needs`) went to `idle` on every status update that said the
  adapter ran no prompt — its usage, slash commands, mode, options or
  title. During a turn the agent started by itself (Codex, answering a
  message that came as a turn ended) each such update said so mid-turn,
  and one handled just after a message went (the commands an adapter
  lists as it starts, say) showed it idle until its first words. Only a
  turn's end rests it now. Nothing to change.
- **SDK: `acptest` plays `perm2…`** ([sdk.md](/docs/sdk.md) §Testing an
  ACP client): two tool calls that ask at once, as Claude's parallel
  calls do — the second `session/request_permission` sent before the
  first is answered, the turn waiting for both, then "perm2: ‹option›
  ‹option›" — to test how a client queues permission requests. Every
  other script plays as before.
- **BREAKING: terminals move to a new base image by themselves — base
  auto-update, a workspace setting, on by default**
  ([changes/2026-10-01-base-auto-update.md](/docs/changes/2026-10-01-base-auto-update.md),
  [09-terminals.md](/docs/overview/09-terminals.md) §Base images, D175).
  A tile's terminal layer built on an older base image now moves to the
  current base at its next session start: it is reset as **⬆ base
  update** resets it — everything outside the workspace files and `$HOME`
  goes, for good (installed packages, `/etc`, `/var`, `/opt`…, a VM
  terminal's whole disk) — and the shell's first line says so, in grey.
  An agent session's move shows in its Agent tab (a new `notice` agent
  event) and in the tile's next shell, once. A running terminal keeps its
  base until it ends. The old layer is put aside and removed in the
  background, so the session starts at once. A layer whose base isn't
  installed any more moves the same way. The terminal window's chooser
  says the next session moves instead of offering the button. Turn it off
  to keep today's behaviour — before upgrading, to keep what the layers
  hold: the admin console's new workspace → **terminals** tab, the new
  **`bx settings`** (`set --base-auto-update=false`), or the new
  `GET`/`PUT /api/xbin/workspace-settings` (`{baseAutoUpdate}`, admin to
  change, publishing a `workspace-settings` event; kept in
  `data/workspace-settings.json`, whose other keys a write keeps). `GET
  /ws/term/env` gains `baseAutoUpdate`. Tile sandboxes are not touched.
  This release's base ships Go 1.26.3, so with the setting on a terminal
  no longer needs **⬆ base update** for the builtins' `go 1.26.0`
  ([changes/2026-09-30-builtins-go-1-26.md](/docs/changes/2026-09-30-builtins-go-1-26.md)).
- **xbind no longer refuses to start over a terminal layer pinned to a
  base image that isn't installed** ([09-terminals.md](/docs/overview/09-terminals.md)
  §Base images, D175). It logs the layer; the layer's sessions refuse to
  start until it is reset, as they did behind the boot gate (or move, with
  base auto-update on). A layer stamp or base version that can't be read
  now fails a session's start, the layer untouched, instead of being read
  as an unstamped one.
- **`sandbox-terminal` v7: an SSH client that disconnects while its
  command is still starting no longer leaves the command running**
  ([sandbox-manager.md](/docs/sandbox-manager.md) §People's terminals).
  The tile gave up on the manager's answer to the command's start when
  the client left — but the manager started the command anyway, and
  without its id nothing ended it: `ssh box@host cmd` interrupted while a
  stopped sandbox booted ran `cmd` to the end, and a terminal whose
  client left while it opened kept its shell. The tile now waits for that
  answer (up to two minutes after the client left; for a terminal, the
  `tty` route's upgrade and its `session` frame) and ends the command as
  for a client that leaves while it runs: `HUP`, then `DELETE` if it
  still runs. A manager sees its exec start or terminal upgrade completed
  instead of abandoned, then the `HUP` and `DELETE`. `GET /sessions`
  stops listing a connection as soon as its client has gone. Tile Manager
  → Updates.
- **Fix: a sandbox's network relay sends nothing after it is closed.**
  When a sandboxed backend, terminal or tool run stopped, a flow still
  finishing (the reset of a connection that was being dialed, say) could
  write its packet after the relay had closed its TUN, into whatever file
  xbind had opened under the same number by then. The relay now stops its
  writers before it closes the TUN. Nothing to change.
- **Fix: sandbox file operations through `../` symlinks on a busy host.**
  Reading, writing, listing or tarring a path whose symlink climbs with
  `..` could fail with `resource temporarily unavailable` while the host
  was busy renaming files elsewhere. The sandbox agent now retries longer,
  with a short backoff (about half a second at most). Nothing to change.
- **A request that meets a swap reaches the new generation**
  ([elements.md](/docs/elements.md) §Runtimes & backend lifecycle). A
  request xbind had routed to a backend generation just as a save, a
  deploy or a grant change replaced it (or a reap stopped it) could fail
  with `502 backend error: … connection reset by peer` (or `EOF`, or a
  refused dial): the old generation's SIGTERM closed its socket before it
  took the request. Such a request now goes to the deployment's current
  generation when sending it again is safe: it has no body and is a `GET`,
  `HEAD`, `OPTIONS` or `TRACE` (or carries an `Idempotency-Key` header),
  or nothing of it reached the old generation. Other requests answer 502
  as before, and so does any request of a backend that crashed. A backend
  that exits on SIGTERM without answering the requests it holds still
  loses them, and one of those idempotent requests may now run on both
  generations: drain on SIGTERM, as `xbin.Serve` does.
- **Pausing live reload while a save still builds ships the checkpoint
  only** ([tile-deployments.md](/docs/tile-deployments.md) §Pausing live
  reload). The save's build used to finish and swap in before the pause's
  checkpoint did, and as it read the work tree while it built, it could
  briefly serve saves made after the pause. Its generation now never
  serves once the pause commits: the code running before keeps serving
  until the checkpoint swaps in.

## Older entries

Entries before 2026-10-01 live in archive pages, unchanged, newest first:

- [changelog/2026-09b.md](/docs/changelog/2026-09b.md) — September 2026, 28th to 30th (2026-09-28 to 2026-09-30)
- [changelog/2026-09a.md](/docs/changelog/2026-09a.md) — September 2026, 4th to 27th (2026-09-04 to 2026-09-27)
- [changelog/2026-08.md](/docs/changelog/2026-08.md) — August 2026 (2026-08-01 to 2026-08-25)
- [changelog/2026-07.md](/docs/changelog/2026-07.md) — July 2026 (2026-07-05 to 2026-07-30)
