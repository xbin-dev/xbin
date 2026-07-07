# Agent v2 + llm-gw overhaul + observability — plan

Follow-up to `plans/agent.md` and the Hermes/Pi comparison. Scope is large, so
this lands in **dependency-ordered phases**, each independently committable with
docs + tests. Decisions here are load-bearing; log the notable ones in
`plans/DECISIONS.md` as they ship.

## Evaluation of the request (what we're building, and the calls made)

| Ask | Verdict / decision |
|---|---|
| Agent tile ~2× taller default | Yes. Tile requests a taller default height (min-height ~640px / a manifest display hint). |
| Default model from llm-gw; llm-gw has per-use-type "preferred model" dropdowns | Yes. llm-gw grows `preferred{}` for **6 use-types**: agent, chat, pipeline, vlm, coding, summarizing. Callers resolve via a new reader endpoint. |
| Agent: ≥2 models (main + summarizing) → later reconciled to **4 tiers** | Reconcile to **4 agent tiers**: `general` (main), `code`, `memory` (summarizing/memory-mgmt), `vlm`. Each empty ⇒ resolved from llm-gw preferred (general←agent, code←coding, memory←summarizing, vlm←vlm). |
| MCP → binding like the chat tile | Yes. Agent gains a `mcp` multi:true interface slot; backend reads `XBIN_IFACE_MCP` env (JSON array). Drop the static `Config.MCP` list. |
| UI to see/manage all state (memory etc.) | Yes. Expand the tile: memory CRUD, model tiers, MCP bind status, schedule/cron mgmt, skills, recall search, watcher toggle. |
| Cron not always-on; sessions schedule cron-agents | Yes. Drop the global `@every 1m` heartbeat. A sleeping run schedules a **one-shot wake** at `wake_at`; a **cron-agent** is a user/agent-created recurring schedule. |
| Watcher mode: discard no-change rounds | Yes. `watcher` runs expose `state_changed(summary)`; a round that ends without it (and with no persistent effects) is **rolled back** from the transcript — history keeps only real changes. |
| Recall/Search FTS5 | Yes. `messages_fts` + a `recall(query)` tool over full (incl. compacted) history. |
| Tokenizer / llm-gw token counts | The tile numbers **are** from the API (`usageRe` scrapes `prompt_tokens`/`completion_tokens`). Fix is agent-side: use the completion response `usage` as budget truth (already captured, currently only journaled). |
| Parallel tools + default+configurable timeout | Yes. Read-only/independent tools run in parallel (allow-list); `Config.ToolTimeout` (default 120s) per call. |
| Parallel subagents | Yes. Multiple `spawn_subagent` calls in a turn run concurrently (bounded pool). |
| Cronjob agent-callable/triggerable/inspectable/editable + UI | Yes. `schedule`/`unschedule` tools + `/schedules` CRUD endpoints + UI. |
| gw streaming | Agent streams from llm-gw (already SSE-capable), journaling deltas so the tile shows live progress. |
| retry/backoff | Both layers: llm-gw retries transient upstream (429/5xx, pre-first-byte, honor Retry-After); agent classifies llm-gw errors as retryable. |
| per-use-type model settings | Covered by preferred{} + agent tiers. |
| Caching / stable prefixes | System prompt uses **current day**, not full timestamp; memory frozen per drive; llm-gw adds Anthropic `cache_control` breakpoints for anthropic backends. |
| cost tracking per tier | llm-gw pricing map (model→$/Mtok); `stats.cost`; surfaced in /stats + /metrics. |
| Skills — Hermes-inspired | `SKILL.md` store + `skills_list/view/manage` tools + a `/learn` authoring flow. Background curator = later. |
| Multimodal 4 tiers (general/code/memory/VLM) | Vision messages route to `vlm` **only if** the active task model lacks vision. |
| Observability — Prometheus | llm-gw + agent expose `GET /metrics` behind a `prometheus` provider interface; new builtin **prometheus-viewer** tile with a multi:true `prometheus` slot renders live state. |

Non-goals for now (documented, not built): Hermes's durable kanban board, the
background skill curator, branching session tree, image *generation*.

## Phases

### Phase 1 — llm-gw foundation  ← START HERE
- `preferred map[string]string` in config (6 use-types). `GET /preferred[?use=]`
  (reader) for callers; `PUT /config/preferred {use,model}`; UI dropdowns
  (aggregated model list) per use-type.
- Retry/backoff in the proxy: loop upstream `Do` on 429/500/502/503/504 while
  **nothing has been written** to the client yet; jittered backoff; honor
  `Retry-After`. Streaming keeps working (retry only before first byte).
- Pricing map (`config.pricing` model→{inPerM,outPerM}); `stats.cost`; compute
  per request from the resolved model.
- `GET /metrics` Prometheus text (per-backend reqs/tokens/active/cost). Declare
  provider interface `metrics {kind:http, service:prometheus, path:"/metrics"}`.
- Docs: chat/agent unaffected; changelog; openapi note is component-local.

### Phase 2 — agent runtime v2 (backend)
Model tiers + preferred resolution; MCP via `XBIN_IFACE_MCP`; streaming LLM
calls with delta journaling; retry/backoff + error classifier; provider-token
budget truth; stable-prefix system prompt; parallel tools + per-tool timeout;
parallel subagents; FTS5 `messages_fts` + `recall` tool; multimodal tier
routing (vision→vlm fallback).

### Phase 3 — scheduling redesign
Drop always-on heartbeat. `schedules` table (id, cron, goal, config, watcher,
enabled, last_run). One-shot wake for sleeping runs. `schedule`/`unschedule`
agent tools. `/schedules` CRUD. Watcher mode round-rollback.

### Phase 4 — skills (Hermes-inspired)
`SKILL.md` store (a `skills` resource dir), `skills_list/skill_view/
skill_manage` tools, `/learn` authoring flow. Curator later.

### Phase 5 — agent tile UI
2× height; model-tier config; memory CRUD; MCP bind status; schedules panel;
skills panel; recall search; watcher toggle; live streaming timeline.

### Phase 6 — prometheus-viewer builtin tile
Multi:true `prometheus` slot; scrape bound `/metrics`; render parsed series as
simple live gauges/counters; llm-gw + agent expose metrics.
