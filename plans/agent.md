# Agent template — design

The first builtin **template component** (plans/templates.md): a blank-slate
**agentic loop** you instantiate into a named copy (`apps/support-agent`, …)
and build up into a practical agent that pulls and pushes data around buxon
between components. Opinionated, debuggable, and durable — not a toy chat loop.

Instantiate it (Tile Manager → "New from template", or `bx template new
agent as apps/my-agent`) and you get a running component with its own sqlite,
its own scheduler heartbeat, and a control/observability tile.

## What we took from the field

Researched Letta/MemGPT, the Claude Agent SDK, LangGraph, and the durable-
execution engines (Temporal, DBOS, Restate, Inngest). The load-bearing ideas:

- **Durable execution (DBOS/Temporal/LangGraph checkpointer).** The loop is a
  state machine journaled to sqlite at every boundary. An LLM call or tool call
  is an *activity*: its result is written before we act on it, so a crash /
  backend unload / restart resumes without re-running completed side effects.
  buxond unloads idle backends — durability is not optional here.
- **Tiered memory + self-editing memory (MemGPT/Letta).** A small always-in-
  context set of **memory blocks** the agent edits with tools, plus the full
  transcript in sqlite (searchable "recall"), plus **compaction** into a
  recursive summary when the window fills.
- **Compaction (Claude Agent SDK).** Automatic when nearing the token budget;
  also manual (a tool / a tile button).
- **Subagents as tools (Claude Agent SDK).** `spawn_subagent` starts a fresh-
  context child run; only its final result returns to the parent. Disablable,
  and it *is* just a tool — nothing special in the loop.
- **Human-in-the-loop interrupt/resume (LangGraph).** `ask_user` parks the run
  in `waiting_input`; the tile injects an answer and it resumes from the same
  point. Optional **tool approval** gate before side-effecting tools run.
- **Self-scheduling / yielding.** `yield(seconds)` parks the run `sleeping`
  with a `wake_at`; a cron **heartbeat** (`@every 1m`, buxon cron resource)
  re-drives due runs — which doubles as crash recovery.

## Shape

A Go-backed component. `runtime: go`, ships `go.mod.tile` (go:embed skips
nested modules) restored on instantiate; pure-Go `modernc.org/sqlite`, no cgo.

```
apps/agent/
  buxon.json         template marker + runtime + uses (llm-gw, db, beat, events)
  scope.json         resources: db (sqlite), beat (cron), events (bus)
  go.mod.tile go.sum  module "agent" (rewritten to the instance path on copy)
  _backend/          entry "./_backend" — the underscore keeps buxon's own
                     `go build ./...` from sweeping this sqlite-dep payload into
                     the buxon module; `all:`-embed still bundles it, instances
                     still build it (explicit `go build ./_backend`).
    main.go          HTTP API for the tile + heartbeat entry (/tick)
    db.go            sqlite schema + data access
    loop.go          the step loop: context assembly, compaction, journaling
    llm.go           llm-gw client (OpenAI-compatible chat/completions + tools)
    tools.go         tool registry + opinionated builtin tools
    mcp.go           minimal MCP client (tools/list + tools/call), disablable
  API.md
  index.html agent.js the control + observability tile
```

### Connecting to the LLM

Like chat: `uses: apps/llm-gw:writer`, and the backend calls
`http://buxon/api/apps/llm-gw/v1/chat/completions` through `buxon.Client()`.
Model + system prompt + knobs live in the agent's own config (kv-in-sqlite);
the upstream key stays in llm-gw's vault. Function-calling tools go in the
`tools` array; we drive the loop off `tool_calls`.

### Persistence (in-component sqlite)

`BUXON_RES_DB` → a same-scope sqlite file path. Schema:

- `runs(id, title, status, cursor, wake_at, parent_id, config, created, updated)`
  — status ∈ idle | running | waiting_input | sleeping | done | error.
- `messages(id, run_id, seq, role, content, name, tool_call_id, tool_calls,
  tokens, compacted, created)` — the transcript; `compacted=1` rows are folded
  into a summary and excluded from the window.
- `steps(id, run_id, seq, kind, detail, created)` — the visibility journal:
  llm_call, tool_call, tool_result, compaction, yield, ask, error, finish.
  This is what the tile renders and what makes a run debuggable.
- `memory(run_id, key, value)` — self-edited memory blocks, always in context.

### The loop (one step)

1. **Assemble context**: system prompt → memory blocks → running summary →
   uncompacted transcript, trimmed to a token budget (chars/4 heuristic; no
   tokenizer dependency).
2. **Compact** if over budget: summarize the oldest uncompacted turns via the
   LLM into the summary block, mark them `compacted`. Journal it.
3. **LLM call** with the tool specs; journal the request/response + token est.
4. **Tool calls?** Execute each (builtin, subagent, MCP), append `tool` result
   messages, journal, and loop to 1. Control tools change run status:
   `yield`→sleeping, `ask_user`→waiting_input, `finish`→done.
5. **No tool calls**: record the assistant's text, run goes `idle` (awaiting the
   next injected user message) unless a goal-completion tool ended it.

Steps run to a bounded budget per drive (max iterations) so a runaway loop
can't spin forever; the heartbeat picks the run back up.

### Opinionated builtin tools

- `memory_set(key, value)` / `memory_get(key)` — core memory editing.
- `finish(result)` — end the run with a result.
- `ask_user(question)` — park for human input (interactive feedback).
- `yield(seconds)` — durable sleep; heartbeat resumes.
- `spawn_subagent(task, system?)` — fresh child run, returns its result
  (config flag `subagents: false` removes it).
- **buxon-native** (the point of the thing): `buxon_call(method, path, body)`
  — call another component through the gateway (subject to grants you approve),
  so the agent moves data between tiles. Plus `note(text)` for a visible log
  line. These are the seams you extend per instance.

### MCP

`mcp.go` is a minimal client for the streamable-HTTP transport (JSON-RPC
`tools/list` + `tools/call`). Configured MCP servers' tools are merged into the
tool set with an `mcp:<server>:<tool>` prefix; calls proxy through. Disablable
(`mcp: []`). Deliberately small — a working seam, not a full MCP stack.

### Debuggability / visibility / interactive feedback (the tile)

Opinionated single tile:

- **Runs** list with status badges; **New run** (goal + optional system prompt).
- **Timeline** of the selected run: messages and journal steps interleaved,
  tool calls expandable (args → result), compaction markers, per-call token
  and latency. Live (polling `updated` cursor).
- **Controls**: inject a user message; answer an `ask_user`; **interrupt/stop**;
  **resume**; **step once**; toggle subagents / tool-approval; edit memory
  blocks; **compact now**.
- **Config** panel: model, system prompt, token budget, max iterations,
  approval mode, MCP servers.

### Security / RBAC

The instance is an element like any other: its `uses` (llm-gw, its own
resources, and whatever components you later let it call) are grants a human
approves — the agent can't self-approve cross-scope access (docs/auth.md,
AGENTS.md). `buxon_call` is bounded by exactly those grants. Nothing the agent
does escapes the grant table.
