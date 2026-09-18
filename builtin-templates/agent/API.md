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
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, files, draft}` (`draft` = live streaming text; `files` is session-file METADATA only) |
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
                "files": true }
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

`POST /tick` — invoked by the on-demand `beat` cron job. Drives every run that
is `sleeping` past its wake time or left `running` (crash recovery).

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
`ask_user`/approval) · `sleeping` (yielded; heartbeat resumes) · `done` · `error`.

## Loop & tools

Each step: assemble context (system + a stable date + memory blocks + skill list
+ running summary + live transcript, compacting the oldest turns when over
budget) → LLM call (streamed when the feature is on) → execute tool calls (a
turn's non-control tools run in parallel, each under `toolTimeout`) → repeat, up
to `maxIters` per drive. Built-in tools: `memory_set`/`memory_get`, `note`,
`recall` (FTS5 over full history), `xbin_call` (reach other granted
components; private lane), `web_search`/`web_fetch` (web lane), `schedule`/`unschedule`, `state_changed` (watcher),
`skills_list`/`skill_view`/`skill_manage`, `finish`, `ask_user`, `yield`,
`spawn_subagent` (several in a turn ⇒ parallel subagents), the session-file
tools below, plus any bound `mcp:<server>:<tool>`. MCP servers are bound via the `mcp` interface (multi:true,
like the chat tile). Extend these in `_backend/tools.go`.

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
