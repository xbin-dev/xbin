# The project overview page (D214, D215, D216)

> Status: implemented (2026-10-31) — D214 (the route and the page, web and native), D215 (tile sandbox usage), D216 (scm repo stats). Shapes as built are in the template's API.md, docs/protocol.md, docs/sandbox-manager.md and docs/scm.md; where they differ from this page, they win.

The owner, 2026-10-31: a project's page shows, on one screen, all the
machinery that moves the project — dense, for professionals, every part
with a tooltip. Agreed from the prototype
(https://claude.ai/artifact/12KYvCmre4yKkCFXF1aunG, four rounds).

## The page, top to bottom

1. **Status strip** (~125 px, six cells; folds to one line, kept per person):
   coordinator · worker agents · subagents · tokens (day/week chart) ·
   source control (provider-neutral; the provider's title is the label) ·
   sandbox (state, size, disk, commands, CPU and memory over the last hour).
2. **Board** (the kanban). It grows into what is left over; each card also
   says its agent's state and its running subagents.
3. **A drag bar** (role separator; arrows ±24 px, Shift ±96; double-click
   resets; kept per person) shares the height between the board and the
   coordinator's conversation.
4. **Coordinator**: a header (state, model, tokens today, Stop, Open chat ↗),
   a one-line **goal** (click expands; idle: what wakes it, then the goal),
   its **conversation** — a fixed-height window on its newest messages
   (default 300 px, 200 px on a short tile), more loaded than fit, the
   oldest clipped at the top under a fade with "earlier messages are in its
   chat"; a new message pushes the oldest out and nothing below moves; no
   scrollbar of its own — then its **input**.
5. **Height.** A tile ≥ 600 px tall: the page fits the tile — strip, board
   (scrolls inside its share when its cards don't fit), bar, coordinator,
   and the input is the page's bottom bar (page-composer.js), always in
   view. Shorter: the whole page scrolls as one, the board at its own
   height, and the input ends the coordinator's section.
6. **Tooltips** on everything, after a hover delay (~600 ms; none while
   moving across; focus shows at once); touch: a tap shows one.

iOS (native view) follows the same order with the app's primitives: a
status section (rows and `chart`s), the board's sections, a coordinator
section (goal row, its newest messages, Open chat) and the composer docked
at the bottom (D212). Tooltips become each row's subtitle/footer or an ⓘ
sheet. No drag bar (a list screen).

## Contracts

### `GET /projects/{pid}/overview?range=day|week` (agent, viewer) — D214

One read for the strip and the cards. `range` picks the token chart
(default `day`: 24 hourly buckets; `week`: 7 daily). Fields a backend
can't tell are absent (or `null`), never guessed.

```jsonc
{"at": 1793445000000, "range": "day",
 "coordinator": {
   "run": {"id": 900, "status": "running", "turnStartedMs": 1793444962000} | null,
   "state": "working" | "waiting" | "idle" | "failed" | "none",   // none: you have no coordinator yet
   "goal": {"text": "Triage open bugs…", "now": "making a task for…", "by": "coordinator" | "you", "atMs": …} | null,
   "wakesOn": [{"kind": "ci", "n": 1, "label": "CI on #1"}, {"kind": "review", "n": 1, "pr": 42, "label": "review on #42"},
               {"kind": "issues", "label": "new issues"}, {"kind": "you", "label": "your message"}],
   "lastEvent": {"kind": "ci.failed", "n": 1, "label": "CI failed on #1", "atMs": …} | null,
   "turnsToday": 6, "tasksMade7d": 11, "model": "Claude Opus 5.5", "tokensToday": 312000},
 "workers": {"total": 7, "working": 2, "needsYou": 3, "idle": 2, "queued": 1, "done": 1,
             "slots": {"used": 2, "max": 3},
             "byEngine": [{"engine": "claude-code", "title": "Claude Code", "count": 4}, {"engine": "agent", "title": "agent", "count": 3}]},
 "subagents": {"running": 4, "waiting": 1, "finished24h": 17, "failed24h": 2,
               "by": [{"n": 2, "count": 2}, {"n": 1, "count": 1}, {"coordinator": true, "count": 1}]},
 "tokens": {"buckets": [{"t": …, "workers": 0, "coordinator": 0, "subagents": 0}, …],   // oldest first
            "total": {"workers": 0, "coordinator": 0, "subagents": 0},
            "cost": {"amount": 6.4, "currency": "USD"} | null},   // coding agents' own reported cost over the range
 "scm": {"provider": {"name": "scm-github", "title": "GitHub", "kind": "github"} | null,
         "stats": true,                       // the provider serves repo stats (cap `stats`, D216)
         "repos": [{"repo": "acme/web", "url": "…", "defaultBranch": "main",
                    "checks": {"state": "success", "url": "…"} | null,
                    "openPulls": 3, "openIssues": 14, "merged7d": 5, "err": "…"?}],
         "pulls": {"open": 3, "failing": 1, "review": 1}},   // the project's own tasks' PRs (from the board)
 "sandbox": {"ref": "…", "name": "sb-web", "state": "running", "vcpus": 2, "memMiB": 4096, "diskGiB": 20,
             "diskBytes": 3328599654, "commands": 2,
             "usage": {"at": …, "cpuPct": 63, "memBytes": 2791728742, "memMaxBytes": 4294967296,
                       "series": [{"t": …, "cpu": 41.5, "mem": 2600000000}, …]} | null} | null,
 "tasks": {"2": {"engine": "agent", "title": "agent", "state": "working", "turnStartedMs": …, "subagents": 2, "tokens": 120000}, …}}
```

- **Classes of runs.** *coordinator*: the caller's coordinator run.
  *workers*: each task's conversation and the coding agents it drives
  (harness children). *subagents*: every other descendant (subagent_spawn)
  of a task's conversation or of the coordinator. Counts are over the
  project's tasks the caller may see.
- **Tokens** come from a new table `usage_hourly(run_id, hour, calls,
  prompt, completion, cost_micros)` written beside `addRunCost` (and, for
  coding agents, the deltas of their reported cumulative cost), kept 8 days.
  Earlier spend (before this build) isn't in it: the chart starts empty.
- **`goal`**: set by the coordinator with a new tool `project_goal {text,
  now?}` (its prompt says when: a new instruction, a finished aim); kept per
  coordinator. Without one: your latest message to it (`by: "you"`).
- **`wakesOn`**: from what would wake it now — tasks with CI pending or a
  PR awaiting review, the project's issue subscription, and always `you`.
- **`scm.repos`** from the provider's `GET /scm/repo/stats` (D216), cached
  5 min by the agent; a provider without the cap: `stats: false`, the repo
  rows with `url`/`defaultBranch` and `checks` only (the existing checks
  route on the default branch).
- **`sandbox.usage`** from the sandbox manager's view (D215); absent when the
  manager or xbind don't report it.
- The `project` stream event gains `change: "coordinator"` (goal set, its
  run moved); the page re-reads the overview on `project` events
  (coalesced) and every 60 s while it is shown (the sandbox chart).

### Tile sandbox usage (xbind, D215)

- xbind samples every **running** tile sandbox's cgroup leaf once a minute
  (cpu.stat `usage_usec`, `memory.current`, `memory.max`, `pids.current`)
  and keeps the last 60 points per sandbox (in memory; gone on restart or
  stop). Without cgroup delegation there is nothing to sample: no `usage`.
- `SandboxInfo` (docs/protocol.md §Tile sandboxes) gains
  `usage?: {at, cpuPct, memBytes, memMaxBytes, pids, series: [{t, cpu, mem}]}`
  — `cpuPct` percent of one core over the last interval; `series` oldest
  first, only on `GET` of one sandbox (the list carries `usage` without it).
- The sandbox-manager contract (docs/sandbox-manager.md) gains the same
  optional `usage` on a manager's sandbox view; coding-sandbox passes
  xbind's through (`GET /sandboxes/{id}` with `series`, the list without).
- Additive everywhere; old managers and old xbinds simply don't send it.

### Repo stats in the scm protocol (D216)

- New cap **`stats`**: `GET /scm/repo/stats?repo=&as=` →

  ```json
  {"repo": "acme/web", "url": "…", "defaultBranch": "main",
   "openPulls": 3, "openIssues": 14, "mergedPulls": {"days": 7, "count": 5},
   "defaultBranchChecks": {"sha": "…", "state": "success", "url": "…"},
   "pushedAt": 1793440000000, "at": 1793445000000}
  ```

  Provider-neutral words (`pulls` = pull/merge requests); a field the host
  can't tell is absent. Cached by the provider at most 5 min. Without the
  cap: 501 `unsupported` (protocol 1 grows additively, docs/scm.md §Versions).
- scm-github implements it (counts from the host's search or GraphQL
  totalCount; default-branch checks as `GET /scm/checks` on it).

## Feature keys (parity: model/features.js, web-features.js, native-features.js)

`proj.overview` (the page's order and fold), `proj.overview.coordinator`,
`proj.overview.workers`, `proj.overview.subagents`, `proj.overview.tokens`,
`proj.overview.scm`, `proj.overview.sandbox`, `proj.overview.cards`
(agent state on cards), `proj.overview.goal`, `proj.overview.chat` (the
fixed-height conversation), `proj.overview.split` (web: the drag bar;
native: n/a by the parity test's convention for web-only features).

## Prefs (per person, xbind's prefs store)

`/api/xbin/prefs/project-status-folded` (bool),
`/api/xbin/prefs/project-chat-height` (px; absent = default).

## Work split

- **W-A agent backend** (D214): the overview route, `usage_hourly`,
  `project_goal`, the scm stats read (fake provider in tests), the sandbox
  usage read, the `coordinator` change, API.md, Go tests.
- **W-B xbind + coding-sandbox** (D215): sampling, `SandboxInfo.usage`,
  protocol.md, sandbox-manager.md, coding-sandbox pass-through, tests.
- **W-C scm** (D216): docs/scm.md, scm-github's route and tests.
- **W-D web** (D214): the page per the prototype on model/project-overview.js,
  tooltips, the bar, the fold, the chat window, browser tests, a harness
  pass, the feature entries.
- **W-E native** (D214): native/projects.js's project screen per the same
  model, native tests, native-features entries.
