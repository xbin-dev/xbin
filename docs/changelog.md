# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-07-08

- vault: **secret values are now readable only by the element they belong to** —
  not even the owner or an `xbin:admin` tile. Admins can still list keys and
  set/rotate/delete any vault (the admin console + per-tile mini-admin drop the
  "reveal" button and show masked values), but `GET …/vault/<comp>/<key>` is
  self-only, so a compromised admin tile can't exfiltrate secrets. **Breaking**
  for any admin/owner flow that read another element's secret value via the API
  (`bx vault get <other-component>` now 403s); the owning element's own
  `xbin.Secret()` is unaffected. Migration:
  `docs/changes/2026-07-08-vault-read-lockdown.md`; see `docs/auth.md` §Vault.
- shell: the sidebar bottom shows the **running xbind build commit** (`⬡ xbind
  <commit>`). The commit is baked in at build time (`make build` ldflags →
  `git describe`, falling back to the VCS revision Go stamps into the binary)
  and surfaced on `GET /status` as `version`.

## 2026-07-07

- Templates: a **template-instance update path**. Each builtin template is
  served read-only as a git repo (`GET /api/xbin/templates/<name>.git/…`, admin)
  and added to every instance as a `template` remote, so a builder pulls
  upstream fixes with `git fetch template && git merge template/main` (or
  cherry-pick) — in control of what they adopt, no auto-merge clobber. The
  builtin-tile updater still never touches template instances. Phase 7 of the
  agent-v2 program. Terminals now resolve the SDK's `http://xbin/…` gateway host
  for raw `git`/`curl` too (rewritten to `XBIN_URL` + owner bearer, scoped to
  xbind), so the `template` remote fetches. Existing instances (pre-Phase-7) add
  the remote once by hand — see `plans/templates.md`.
- New builtin tile **prometheus-viewer**: binds one or more components that
  expose Prometheus metrics (service `prometheus`, multi:true — e.g. llm-gw)
  and renders their `/metrics` as a live dashboard (per-source counters/gauges,
  current value + per-second rate + inline sparklines). Phase 6 of the agent-v2
  program.
- llm-gw: **preferred models** — one workspace default per use-type (`agent`,
  `chat`, `pipeline`, `vlm`, `coding`, `summarizing`), set on the tile and read
  by callers via `GET /preferred`, so a model choice lives in one place. Plus
  per-model **cost** tracking, a Prometheus `GET /metrics` endpoint (bindable via
  the new `metrics`/`prometheus` provide interface), and transient-error
  **retry/backoff** on the proxy (429/5xx, pre-first-byte, honors `Retry-After`).
  First slice of the agent-v2 program (`plans/agent-v2.md`).
- agent template (v2, backend): model **tiers** (`general`/`code`/`memory`/
  `vlm`) — each empty tier resolved from llm-gw's preferred model for the mapped
  use-type, so a workspace sets models once and agents inherit; compaction and
  memory work run on the `memory` tier. Plus LLM-call **retry/backoff**
  (transport + 429/5xx, so a gateway reload no longer fails a run), the
  **provider-reported prompt tokens** as the compaction trigger (not a char/4
  estimate), a per-tool `toolTimeout` config field, and a **date-only**
  system-prompt prefix so prompt caching keeps hitting. Tools in one turn now
  run **in parallel** (bounded, each under `toolTimeout`); several
  `spawn_subagent` calls in a turn run as **parallel subagents**. Control tools
  (finish/ask_user/yield) stay sequential and stop the turn. MCP servers are now
  **bound** via an `mcp` interface (multi:true, service `mcp`, like the chat
  tile) rather than a static config list — the owner binds MCP providers in the
  Interfaces tab and their tools reach the model through the binding. New
  **`recall`** tool: FTS5 full-text search over the run's whole history
  (including turns compacted out of context), so detail folded into a summary
  is still retrievable. The wake heartbeat is now **on-demand** — registered
  only while runs are sleeping or mid-drive and removed when idle, so an agent
  with nothing pending stops polling (cron is no longer always-on).
- agent template (v2, scheduling): **cron-agents** — a session or the agent
  itself schedules recurring runs (`schedule`/`unschedule` tools, `/schedules`
  CRUD), each registering its own cron job. **Watcher mode**: a watcher schedule
  re-drives one persistent run with a "check now" nudge and, via a
  `state_changed` tool, keeps only the rounds that reported a change — no-change
  rounds are rolled back, so a long-running watch stays compact. A **Features**
  menu (`GET /features`, toggled in the config's `features` map) gates optional
  capabilities: recall, skills, streaming, vision, parallelTools, watcher.
- agent template (v2): **streaming** — with the streaming feature on, LLM calls
  stream tokens into a live per-run draft (`GET /runs/{id}` → `draft`) so the
  tile shows progress as it generates; retries only before the first token.
  **Multimodal tiers** — a message carrying image content routes to the `vlm`
  tier when the task model isn't vision-capable (name heuristic; override by
  setting the vlm model). `wireMsg` content is now text or an OpenAI
  content-parts array.
- agent template (v2): **skills** — a self-improving skill library (Hermes-
  inspired). The agent authors reusable procedures with `skill_manage`, lists
  them (`skills_list`), and loads one on demand (`skill_view`); the compact
  name+description list is injected into context. **`POST /runs/{id}/learn`**
  distills a run into a skill. `/skills` CRUD + storage in the agent's sqlite.
  Gated by the `skills` feature. (Background auto-curator deferred.)
- agent template (v2, tile): the control tile is rebuilt ~2× taller with a
  settings overlay — **Config** (model tiers + budget/iters/timeout), a
  **Features** menu (toggle recall/skills/streaming/vision/parallelTools/
  watcher), **Memory** CRUD, **Schedules** (create/enable/run-now cron-agents),
  **Skills** (view/edit), and **MCP** bind status — plus a **live streaming**
  draft in the timeline and a "learn skill from this run" action.

- auth: the `code` capability now also has a **blanket form** — `uses {target:
  "code", role:"reader"}` grants read-only source access to **every** component
  (for scanners/linters/stats), alongside `code:<component>` for one. Honored
  on `/api/xbin/code/*` and `/git/{log,diff}`.
- docs: corrected the workspace `AGENTS.md` §Sandbox — `deps/` symlinks are
  editing-plane only; their targets are NOT mounted in the backend sandbox
  (matches docs/isolation.md). Read a sibling's source via a `code:` grant, or
  call it over HTTP.

## 2026-07-06

- frontend: tiles can spawn workspace-level UI via the shell — `xbin.dialog(spec)`
  (a data-driven modal that escapes the iframe; resolves `{button, values}`) and
  `xbin.window(spec)` (a floating window framing one of the tile's own sub-paths,
  a real tile frame that uses `xbin.fetch` normally). New core element
  `<bx-dialog>`. Docs: elements.md §Dialogs & windows, sdk.md, protocol.md.

- interfaces/auth: new `code:<component>` capability — grant a component
  read-only access to *another* component's source (files + git log/diff via
  `/api/xbin/code/*`, `/git/{log,diff}`), owner-approved like any cross-scope
  grant. A component always reads its own source; admin reads any.

- **BREAKING** — interfaces: instance path prefixes registered via
  `PUT /api/xbin/iface-instances` are **provider-relative** and
  workspace-absolute (`/api/<self>/…`) registrations are now rejected with
  a 400. Providers that registered absolute paths must re-register.
  → [migration note](/docs/changes/2026-07-06-instance-paths.md)
- interfaces: multi-input slots (`{kind:"http", multi:true}` — a slot binds a
  SET of providers) and provider instances (`provides {instances:true}` +
  runtime registration, bound as `provider#id`). Injection shapes documented
  in [elements.md](/docs/elements.md); multiselect binding UI everywhere.
- interfaces (admin UI): http bind options are filtered by the slot's
  `service` contract (an `s3basic` slot no longer offers an `openai`
  provider).
- chat tile: binds multiple `llm` providers (model picker aggregates all) and
  any number of `mcp` servers — Model Context Protocol tools are offered to
  the model and executed in an agent loop. MCP providers serve
  Streamable-HTTP JSON-RPC at `/mcp` under themselves (`service: "mcp"`).
- llm-gw tile: multiplexes multiple named upstream backends; model ids become
  `<backend>/<model>` once more than one is configured; per-backend usage
  (reqs, tokens in/out, in-flight) shown on the tile.
- shell: per-tile ⚙ mini-admin on card title bars (admins), sidebar folders /
  collapse / resize, and a system-status footer (cpu, mem, disk, services,
  vault state, req/s). `GET /api/xbin/status` grew `host` and `traffic`
  gauges.
- workspace: `POST /api/xbin/clone {from,to}` forks a component (git history
  kept, old-path references rewritten); Tile Manager grew a clone tab.
  Prefer it over `cp -r`.
- terminals: `IN_SANDBOX=1` / `IS_SANDBOX=1` set in sandboxed terminals;
  terminal panes use a distinct `--bx-term-bg` theme token.
- vault: admin-tile barrier UI (seal state, first-time passphrase, unseal,
  rekey), `POST /api/xbin/vault-rekey`, `bx vault rekey`, and a red
  vault-sealed banner on the admin overview.
- auth: `PATCH /api/xbin/auth-settings` can disable owner-token *browser*
  login once an admin user exists (`bx` Bearer token unaffected).
- terminals: Ctrl+W now reaches the shell as word-erase instead of closing the
  browser tab (best-effort — some browsers reserve it); overlapping floating
  windows get a higher-contrast edge.
- terminals: exiting the shell now closes that terminal (its tab, or the
  window if it was the last one) instead of leaving a dead "[session ended]"
  pane; terminal tabs can be renamed (double-click).

## 2026-07-05

- **BREAKING** — the project renamed **buxon → xbin**: module
  `github.com/magik6k/xbin`, daemon `xbind`, env `XBIN_*`, API `/api/xbin/*`,
  headers `X-XBin-*`, manifest `xbin.json`, runtime dir `.xbin/`, JS global
  `xbin`. Workspaces are migrated on upgrade; external tiles need the same
  rename (see the bx-term-tile migration for the pattern).
- resources: encryption at rest is **always on** — filesystem/sqlite/blob are
  per-resource gocryptfs mounts, kv is envelope-encrypted; sealed vault ⇒
  resource unavailable + component held, never silent plaintext
  ([resources.md](/docs/resources.md)).
- terminals: sessions are persistent server-side (reattach with exact
  scrollback); component terminals are scoped read-only outside the
  component's own dir + `$HOME`.
- shell: tiles can be unpinned into floating windows (persisted per user).
- imports: a component whose `uses` reference nonexistent resources is
  rejected at import time instead of silently landing broken.
