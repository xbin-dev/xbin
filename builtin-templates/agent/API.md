# agent — API

A durable agentic loop. State lives in this component's sqlite (`db`); the LLM
is reached through `apps/llm-gw`; a cron heartbeat (`beat`) re-drives sleeping
and stalled runs. Design: `plans/agent.md`.

All endpoints are **admin-only** — the tile is self (always admin of itself)
and the owner. There is no public surface. Paths below are relative to
`/api/<this-component>`.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, status, timestamps) |
| `POST /runs` | `{goal, title?, system?}` | create a run and start driving it |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config}` |
| `DELETE /runs/{id}` | — | delete a run and its history |
| `POST /runs/{id}/message` | `{text}` | inject a user message; resumes the run |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve}` | approve/deny a parked tool turn (approval mode) |
| `POST /runs/{id}/interrupt` | — | stop driving; park the run idle |
| `POST /runs/{id}/resume` | — | drive the run again |
| `POST /runs/{id}/compact` | — | force a compaction now |
| `PUT /runs/{id}/memory` | `{key, value}` | set a memory block |
| `DELETE /runs/{id}/memory?key=` | — | delete a memory block |

## Config

`GET /config`, `PUT /config` — the default `Config` copied into each new run:
`{model, system, tokenBudget, maxIters, subagents, approve, mcp[]}`. `mcp` is a
list of `{name, url, headers?}` streamable-HTTP MCP servers.

## Heartbeat

`POST /tick` — invoked by the `beat` cron job (`@every 1m`, self-registered on
boot). Drives every run that is `sleeping` past its wake time or left `running`
(crash recovery). Safe to call by hand.

## Run status

`idle` (awaiting a user message) · `running` · `waiting_input` (parked on
`ask_user`/approval) · `sleeping` (yielded; heartbeat resumes) · `done` · `error`.

## Loop & tools

Each step: assemble context (system + memory blocks + running summary + live
transcript, trimmed to `tokenBudget`, compacting the oldest turns when over)
→ LLM call with tool specs → execute tool calls → repeat, up to `maxIters` per
drive. Built-in tools: `memory_set`/`memory_get`, `note`, `buxon_call` (reach
other granted components), `finish`, `ask_user`, `yield`, `spawn_subagent`
(when enabled), plus any `mcp:<server>:<tool>`. Extend these in
`_backend/tools.go`.
