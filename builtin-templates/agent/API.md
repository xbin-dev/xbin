# agent — API

A durable agentic loop. State lives in this component's sqlite (`db`); the LLM
is reached through `apps/llm-gw`; a cron heartbeat (`beat`) re-drives sleeping
and stalled runs on demand. Design: `plans/agent.md`, `plans/agent-v2.md`.

All endpoints are **admin-only** — the tile is self (always admin of itself)
and the owner. There is no public surface. Paths below are relative to
`/api/<this-component>`.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, status, timestamps) |
| `POST /runs` | `{goal, title?, system?}` | create a run and start driving it |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, draft}` (`draft` = live streaming text) |
| `DELETE /runs/{id}` | — | delete a run and its history |
| `POST /runs/{id}/message` | `{text}` | inject a user message; resumes the run |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve}` | approve/deny a parked tool turn (approval mode) |
| `POST /runs/{id}/interrupt` | — | stop driving; park the run idle |
| `POST /runs/{id}/resume` | — | drive the run again |
| `POST /runs/{id}/compact` | — | force a compaction now |
| `POST /runs/{id}/learn` | — | distill the run into a saved skill (the /learn flow) |
| `PUT /runs/{id}/memory` | `{key, value}` | set a memory block |
| `DELETE /runs/{id}/memory?key=` | — | delete a memory block |

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
                "vision": true, "parallelTools": true, "watcher": true }
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
| `POST /schedules` | `{name?, cron, goal, watcher?}` | create + register a cron-agent |
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
components), `schedule`/`unschedule`, `state_changed` (watcher),
`skills_list`/`skill_view`/`skill_manage`, `finish`, `ask_user`, `yield`,
`spawn_subagent` (several in a turn ⇒ parallel subagents), plus any bound
`mcp:<server>:<tool>`. MCP servers are bound via the `mcp` interface (multi:true,
like the chat tile). Extend these in `_backend/tools.go`.
