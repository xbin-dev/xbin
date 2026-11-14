# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-11-14

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
    checks it**: a one-shot ticket bound to the exact method, path and
    body, or a standing approval of your tile the person gave and can
    revoke. Your tile needs no code for this.
  - No tile can be granted or bound to a sealed component that acts for
    people: `POST /grants` and `POST /bindings` answer 409 for such a
    target.
- **API: `/api/xbin/delegate/…`** ([protocol/delegation.md](/docs/protocol/delegation.md)):
  the workspace's switch and a person's pause, approval requests and
  tickets, standing approvals, admin change sets (an admin's agent proposes
  grants, bindings, org shares and limit raises; the person approves after
  a recent sign-in; xbind applies them as the person), and the person's
  audit log of what their agent did. A `delegate` event on `/ws/events`;
  push kinds `agent.approval` and `agent.changeset`.
- **Agent sessions: `origin`.** A session row, and its history entry,
  carries `origin: "agent"` when the person's workspace agent opened it
  (`GET /term/sessions`, `GET /agent/history`). Absent otherwise.
- **`GET /tile-status`** also answers a person's workspace agent for a tile
  that person can read.

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
