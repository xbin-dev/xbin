# agent — API

A durable agentic loop. State lives in this component's sqlite (`db`); the LLM
is reached through `apps/llm-gw`; a cron heartbeat (`beat`) re-drives sleeping
and stalled runs on demand. Design records `agent` and `agent-v2` live in the xbin repo.

All endpoints are **admin-only** — the tile is self (always admin of itself)
and the owner. There is no public surface. Paths below are relative to
`/api/<this-component>`.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, kind, status, timestamps; a quick ask also carries `last`, its latest answer, for the home view's cards) |
| `POST /runs` | `{goal, title?, system?, toolset?}` | create a run and start driving it |
| `POST /ask` | `{text, toolset?}` | a quick ask: a run titled from `text`, `kind:"quick"`, driven immediately |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, files, draft, slots}` (`draft` = live streaming text; `files` is session-file METADATA only; `slots` = `{active, limit}` drive slots, so a `queued` run can say whether it waits for one) |
| `DELETE /runs/{id}` | — | delete a run, its history, and every subagent run below it |
| `POST /runs/{id}/message` | `{text}` | inject a user message; resumes the run |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve}` | approve/deny a parked tool turn (approval mode) |
| `POST /runs/{id}/interrupt` | — | stop driving; park the run idle |
| `POST /runs/{id}/resume` | — | drive the run again |
| `POST /runs/{id}/compact` | — | force a compaction now |
| `POST /runs/{id}/learn` | — | distill the run into a saved skill (the /learn flow) |
| `PUT /runs/{id}/memory` | `{key, value}` | set a memory block |
| `DELETE /runs/{id}/memory?key=` | — | delete a memory block |
| `POST /runs/{id}/cancel` | `{scope?, reason?}` | durable cancel; `scope` defaults to `subtree` |
| `GET /runs/{id}/tree` | — | the whole workflow this run belongs to: nodes, statuses, blockers, cost. Metadata only |
| `GET /halt` · `PUT /halt` | `{on}` | the global brake: cancels live runs and blocks new spawns |
| `GET /runs/{id}/files` | — | the run's session files: `[{path, bytes, version, updated}]`, no content |
| `GET /runs/{id}/file?path=` | — | one file with its content |
| `PUT /runs/{id}/file` | `{path, content, version?}` | write a file; a non-zero `version` that no longer matches answers **409** |
| `DELETE /runs/{id}/file?path=` | — | delete a file |

Content and metadata are separate routes on purpose: `GET /runs/{id}` rides the
tile's 1.5s poll, so it must never carry file bodies.

Runs carry a `kind`: `""` for a task, `"quick"` for a quick ask. A quick ask is
an ordinary run in every other way — follow-ups go to `POST /runs/{id}/message`.
The tile opens on a home view built on this: the composer asks (in the lane
chosen with its 🔒/🌐 toggle, remembered per user through `/api/xbin/prefs` —
tile frames have no `localStorage`), recent quick asks show as cards, and the
sidebar lists tasks.

## Capability lanes (the toolset firewall)

Every run is in exactly one lane, chosen at creation and immutable
(`config.toolset`):

- **`private`** (default) — internal reach: `xbin_call` and every bound
  `mcp:*` tool. **No web tools.**
- **`web`** — `web_search` (DuckDuckGo) and `web_fetch` (a URL as readable
  text, capped). **No `xbin_call`, no `mcp:*` tools.**

A run must never hold private data AND an egress channel: content injected into
its context could otherwise steer it into sending that data out in a URL or a
query. The lane is enforced twice — in the tool list offered to the model, and
again when a tool runs (`runTool`) — and it is inherited: subagents get their
parent's lane, and **a schedule the agent creates gets the creating run's
lane**, so a private run cannot smuggle data into a future web run's goal.
Human-created runs and schedules may pick either (`toolset` on `POST /runs`,
`POST /ask`, `POST /schedules`); the tile has a select in the new-run dialog
and the schedule form, and a lane badge on each run.

The web tools go straight out, not through the gateway, so they need the `net`
interface bound (`bx bind <this component> net=internet`); unbound, they return
"web access unavailable" so the model can adapt.

## Config, models, features

`GET /config`, `PUT /config` — the default `Config` copied into each new run:

```jsonc
{
  "model": "",                 // legacy/general fallback (empty ⇒ llm-gw preferred)
  "models": {                  // per-tier models; any empty tier ⇒ llm-gw preferred for
    "general": "", "code": "", //   the mapped use-type (general←agent, code←coding,
    "memory": "", "vlm": ""    //   memory←summarizing, vlm←vlm)
  },
  "system": "…",
  "tokenBudget": 12000,        // compaction trigger (provider prompt tokens when known)
  "maxIters": 12,              // steps per drive
  "toolTimeout": 120,          // seconds per tool call
  "subagents": true,
  "approve": false,            // gate side-effecting tools on human approval
  "features": { "recall": true, "skills": true, "streaming": true,
                "vision": true, "parallelTools": true, "watcher": true,
                "files": true, "repl": true, "workflow": true },
  "replTimeoutMs": 5000,       // REPL budget per statement (max 60000)
  "replMemMB": 256,            // REPL heap watchdog
  // workflow limits (0 = default): delegation depth, lifetime runs per tree,
  // spawns per turn, concurrent drives process-wide
  "maxDepth": 3, "maxSpawn": 32, "maxSpawnPerTurn": 8, "maxActiveRuns": 4
}
```

- `GET /features` → `{keys, features}` — the toggleable capabilities and their
  current state (the tile's Features menu). Toggle by `PUT /config` with a
  `features` map.
- `GET /models` — proxies llm-gw's aggregated model list (for the tier
  dropdowns).

The main loop uses the `general` tier (or `vlm` when a message carries image
content and the general model isn't vision-capable); compaction and the
summarizer use `memory`.

## Schedules (cron-agents)

Cron isn't always-on: a wake heartbeat exists only while runs are pending, and
schedules are individual cron jobs.

| Method & path | Body | Purpose |
|---|---|---|
| `GET /schedules` | — | list schedules |
| `POST /schedules` | `{name?, cron, goal, watcher?, toolset?}` | create + register a cron-agent |
| `PUT /schedules/{id}` | `{enabled?, cron?, goal?, …}` | edit / enable / disable |
| `DELETE /schedules/{id}` | — | remove |
| `POST /schedules/{id}/trigger` | — | run it now |
| `POST /schedules/{id}/fire` | — | the cron target (same as trigger) |

`cron` is a 5-field expression or `@every 30m`. A **watcher** schedule re-drives
one persistent run with a "check now" nudge; a round where the model doesn't
call `state_changed` is rolled back, so history keeps only the changes.

## Skills

`GET /skills` · `PUT /skills` `{name, description?, content}` · `DELETE
/skills/{name}` — a self-authored, reusable procedure library (also managed by
the agent with the `skills_*` tools; injected as a name+description list).

## Heartbeat

`POST /tick` — invoked by the on-demand `beat` cron job while anything is
pending. It first recovers runs no dispatcher can reach (a cancel that no drive
finished, a prompt whose dispatch was dropped), then admits everything
`readyRuns` selects: undelivered dependency results, unblocked runs, `queued`
runs, `sleeping` runs past their wake time, and `running` runs whose lease has
expired (crash recovery).

The job is registered only while something is pending. A registration that
fails (the gateway is still coming up after a restart) is retried every minute
by a keeper — otherwise a sleeping run with nothing else pending would never
wake. Every gateway call the agent makes on its own behalf (heartbeat, model
lookup) carries a timeout, because the SDK client has none.

## Transcript validity

The provider rejects a request in which an assistant `tool_calls` block is not
answered by exactly one tool result per call. The loop writes a placeholder
result for each call before running it and rewrites it in place when the tool
finishes, so the transcript is valid at every instant — including while a turn
is parked for approval (`(awaiting your approval)`) — and results stay in call
order. At the top of every drive, `repairTranscript` heals what a dead process
left behind: a placeholder still saying `(running…)` becomes "the backend
restarted", and a missing result is spliced in right after its block. Replying
instead of approving denies the parked calls.

## Run status

`idle` (awaiting a user message) · `running` · `waiting_input` (parked on
`ask_user`/approval) · `sleeping` (yielded; heartbeat resumes) · `queued`
(ready, waiting for a concurrency slot) · `blocked` (waiting on dependencies) ·
`done` · `error` · `canceled`.

`queued` is deliberately its own state rather than `sleeping` with a wake time:
reusing the sleep mechanism for back-pressure made a throttled run
indistinguishable from one that had chosen to wait. `blocked` does not keep the
heartbeat registered by itself — the rows it waits on are themselves pending,
so the beat stays on transitively.

## Workflows (the run graph)

A workflow **is a root run**: `runs.root_id`/`depth`/`detached` place every run
in a tree, `run_deps` holds the dependency edges, and `run_trees` holds the
per-tree lifetime spawn budget. **A node id is a run id**, so the journal,
`recall`, session files and the tile's click-through all work on a node.

Tools: `spawn_subagent` (delegate and get the answer back in the same turn —
several in one turn run in parallel), plus `workflow_spawn` (detached, returns a
node id, `after:[ids]` for dependencies), `workflow_status`, `workflow_result`,
`workflow_cancel`. Feature key `workflow`.

Two invariants are worth knowing before changing any of this:

- **A child never writes into its parent's transcript.** A settling child marks
  its dependency edges settled and kicks the dispatcher; the parent delivers
  from its own drive. That is why five children finishing together produce one
  parent drive rather than five, and why delivery can be one transaction (so a
  crash between writing the result and marking it delivered cannot duplicate it).
- **A parked parent holds no drive slot.** A synchronous spawn would hold one
  while waiting for a child that needs one, which deadlocks at a low ceiling.

Scheduling is one predicate (`readyRuns`) reached from three entry points: boot,
the cron heartbeat, and an in-process kick. The kick is pure latency — losing it
to a swap costs nothing, because `/tick` runs the identical query from cold. Two
clauses carry the weight: `NOT (parent_id<>0 AND detached=0)` makes a
non-detached child unreachable by the heartbeat (a child exists only inside its
parent's tool call, so resurrecting it runs work nobody is waiting for), and
`depth DESC` drains leaves so their parents can unblock.

Limits, all in `Config`: `maxDepth` 3, `maxSpawn` 32 per tree (lifetime, so
spawn→finish→spawn cannot loop forever), `maxSpawnPerTurn` 8, `maxActiveRuns` 4.
The tree budget, not the depth, is the real backstop. Subagents get neither
`ask_user` (they would park on a human while their parent parks on them) nor
`schedule` (a cron-agent outlives the tree that made it).

`workflow_status`/`result`/`cancel` take a model-supplied id and resolve it
through the caller's **own subtree**. That scoping is the security boundary:
before this layer no run could read another run's anything, and an unscoped id
would let a web-lane run read a private-lane result by guessing.

Leases (`lease_owner`/`lease_until`) make `claim()` correct across process
generations — a blue/green swap briefly runs two against one sqlite file.

## Loop & tools

Each step: assemble context (system + a stable date + memory blocks + skill list
+ running summary + live transcript, compacting the oldest turns when over
budget) → LLM call (streamed when the feature is on) → execute tool calls (a
turn's non-control tools run in parallel, each under `toolTimeout`) → repeat, up
to `maxIters` per drive. Built-in tools: `memory_set`/`memory_get`, `note`,
`recall` (FTS5 over full history), `xbin_call` (reach other granted components;
private lane), `web_search`/`web_fetch` (web lane), `schedule`/`unschedule`,
`state_changed` (watcher), `skills_list`/`skill_view`/`skill_manage`, `finish`,
`ask_user`, `yield`, `spawn_subagent` (several in a turn ⇒ parallel subagents),
the session-file and sandbox tools below, plus any bound `mcp:<server>:<tool>`.
MCP servers are bound via the `mcp` interface (multi:true, like the chat tile).
Extend these in `_backend/tools.go`.

MCP tool lists are cached per server and persisted: a list younger than 5
minutes is used as is, an older one is used at once and refreshed in the
background, and only a server never listed before is waited for —
concurrently, 5 s at most each. A run is marked `running` before any of this,
and the web lane skips discovery entirely.

## Session files + render

Feature key `files` (on by default). A per-run file store held in sqlite, not
on disk: `file_write` · `file_read` (whole file, or a line range with
`offset`/`limit`) · `file_edit` (exact-string replace) · `file_list` ·
`render_html`. They touch only this run's private rows — no egress, no other
component — so they are offered in **both** capability lanes and are not
`sideEffect()` tools: the approval gate never fires for them. Caps: 64 KiB per
file, 64 files and 512 KiB per run.

`render_html` journals a `render` step (`{path, version, bytes}`) and the tile
shows that file in a `sandbox=""` iframe with a prepended meta CSP
(`default-src 'none'; style-src 'unsafe-inline'; img-src data:`). **Scripts
never run and nothing external loads** — so charts must be inline SVG. Verify
with `node test/frame-policy.mjs`.

File versions are monotonic but **not snapshotted**: clicking an older render
chip shows the file's current content, with the header noting the difference.
The tile's Files tab edits them too, sending back the version it loaded so a
write the agent made in between comes back as a 409 instead of being lost.

## JavaScript sandbox (REPL)

Feature key `repl` (on by default). A per-run **goja** VM — pure Go, no JIT, no
native code — over the session files: `js_eval` (evaluate code; state persists
across calls) · `js_run` (evaluate a session file) · `js_reset`. Inside the VM:
`console.*`, `files.read/write/list/exists/remove(path)`, and `load(path)`. It
is how the agent computes instead of guessing, and how it draws a chart for
`render_html`: build inline SVG in `js_eval`, `files.write` it into an .html
file, render that.

**There is no host surface.** `goja.New()` ships no `require`, `process`,
`fetch`, `setTimeout` or `Buffer`, and the parser's default filesystem
source-map loader — goja's one way to read a host file, reachable from a
`//# sourceMappingURL=` comment at parse time — is disabled. Nothing bridges the
VM to the network or to `xbin_call`, which is why these tools are offered in
**both** capability lanes and are not `sideEffect()` tools: they mutate only
this run's private rows, so the approval gate never fires for them.

Limits per statement: a 5s uncatchable interrupt (`replTimeoutMs`), a heap
watchdog (`replMemMB`, default 256), a 2000-frame call stack, 8 KiB of captured
output, and a JS prelude capping `repeat`/`padStart`/`padEnd` at 4 MiB — those
allocate in one native call, so no sampler can catch them.

**Sessions survive restarts by replay, not by magic.** Every statement is
appended to `repl_log` as `running` *before* it executes and settled after; a
cold VM re-runs the log. Sound because the sandbox has zero I/O. A row still
`running` after a restart is therefore the statement that killed the process:
it is marked `killed` and never replayed, and two of those disable the sandbox
for that run rather than letting it crash-loop the backend. The clock is pinned
to each statement's log timestamp, so a rebuild reproduces the values the model
last saw — which also means `Date` is frozen *within* one statement.
