# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-07-10

- terminal: **resource mounts no longer leak in `mount`.** A tile terminal
  binds the workspace read-only+recursive, which cloned in every tile's
  gocryptfs resource (resenc) mount; their contents were already masked, but
  `mount`/mountinfo still listed other tiles by resource name. The terminal
  now detaches those submounts before masking `.xbin`/`data`. Safe by
  construction (both dirs are fully masked; the sandbox root is
  MS_REC|MS_PRIVATE, so the host's live mounts are untouched). No rebuild —
  ships with xbind.
- terminal: **`apt install` fixed** (`rename … Invalid cross-device link`)
  via apt config only — `Dir::Cache::Archives` moves the download cache to
  `/var/cache/xbin-apt`, a path absent from the base image, so at runtime it
  lives entirely in the writable overlay upper and apt's partial/ → archives/
  rename never crosses layers. No overlay-mount option (an earlier
  `redirect_dir=on` attempt on the shared overlay broke component backends'
  state and was reverted). Needs a `make rootfs` rebuild.
- terminal: fixed **doubled keystroke echo after a base-image upgrade / sandbox
  reset**. Resetting kills the session server-side then reconnects; the killed
  socket's onclose could still schedule a reconnect that reattached to the new
  session, leaving two sockets writing to one terminal (every byte, incl. the
  echo of what you typed, twice). A connection epoch now ensures only the
  latest socket drives the terminal or reconnects — also closing the same
  latent race on network/GPU/API-scope switches.
- base rootfs: added the tools agents reach for — full `vim`, OpenAI `codex`
  CLI, `chromium`+Playwright (system browser path), `gh`, `fd`/`bat`/`shellcheck`,
  Go `gopls`/`dlv`/`golangci-lint`, and `pnpm`/`yarn` — so a fresh
  terminal doesn't re-install them. (docker/rootfs.Dockerfile; `make rootfs`.)
- terminal: **`bx status`** now shows the current tile's runtime metrics
  (backend state/cpu/mem/pids/fds/conns/egress, disk usage vs quota, alerts)
  via the new read-only `GET /api/xbin/tile-status?component=` — readable with
  the tile-scoped terminal token (self), or any tile for admins. `--all` keeps
  the admin global view.
- terminal pop-up: a **code browser / git-review panel** (`bx-code`) beside
  the terminal. A collapsible file tree + syntax-highlighted viewer (vendored
  highlight.js, no build step) and a **Changes** tab showing the working-tree
  diff and per-commit diffs for review. Title-bar layout switch: terminal /
  code / resizable split. Read-only (editing stays the terminal's job);
  backed by the existing grant-gated /api/xbin/code/* + /git/* endpoints.
- **resource limits (blast-radius containment for shared/mixed-team
  workspaces).** Per-tile cgroup caps are now *enforced*, not just measured:
  memory.max 2 GiB (+ memory.high), pids.max max(512, ncpu×8), cpu.weight
  fair-share (burst when idle). Per-scope **disk quota** 50 GiB with `507` on
  kv/blob writes over it, plus low-disk (<10% free) write-blocking of the
  biggest users. Backends now run **capability-dropped + seccomp block-list**
  (mount/module/kexec/reboot/ptrace/bpf/keyrings/…). Terminals capped at 32
  per user. **Alerts** (`GET /api/xbin/alerts`) surface at-limit / low-disk /
  blocking events as a banner in the workspace shell and the admin console.
  Tunable via `XBIN_LIMIT_MEM` / `XBIN_LIMIT_DISK`. Details: docs/isolation.md
  §resource limits.
- terminals: **workspace secrets are masked from tile terminals** (Gap 0).
  The isolated terminal's read-only workspace mount now covers `.xbin/`
  (owner token + frame secret), `data/` (vault, encrypted resource state,
  password hashes), and other users' `homes/` with an empty overlay — so a
  shell (or an agent in it) can read every tile's *source* for API work but
  can no longer `cat .xbin/token` to re-grant owner, which had undercut the
  tile-scoped terminal token. Applies to every terminal, including your own
  tiles. Own tile dir + `$HOME` stay read-write. New per-terminal **tile-API
  toggle** (titlebar, default on): off mints no token — the shell sees code
  but every API call is unauthorized. Isolation property of `--isolate`;
  see docs/isolation.md for the honest bound.
- terminals: **seccomp mount guard** makes those masks umount-proof. A tile
  terminal shell keeps `CAP_SYS_ADMIN` (apt / nested namespaces / profiling
  still work) but a seccomp filter — installed before the shell, inherited
  across execve/unshare — denies `umount2`, `move_mount`, `open_tree`, and
  `mount(MS_MOVE)`, the four ways to remove or relocate a mask. So a shell
  that is root in its user namespace still can't peel a mask off to read the
  owner token. Collateral: `fusermount` and nested container/browser
  sandboxes that detach their old root get `EPERM` (run them on the host).
- terminals: **Landlock read guard** — a second layer that denies reading
  the secret *files* (`.xbin`/`data`/other `homes/`) at the VFS level, so even
  if a mask were peeled the owner token / vault / password hashes / other
  agents' credentials still can't be opened. seccomp can't filter by path
  (it can't deref the `open` arg); Landlock can. Restricts only READ_FILE, so
  exec/readdir/writes are untouched (collateral nil); best-effort where
  Landlock is unavailable. The admin **runtime** tab now shows each guard's
  kernel support (*terminal guard: mount ✓ · read ✓ (ABI n)*).

## 2026-07-09

- auth: **server-side session expiry** — browser logins now die after 12 h
  idle (sliding) or 30 days absolute, whichever first, so a stolen session
  cookie can't authenticate indefinitely (previously: valid until logout or
  restart). Tunable via `XBIN_SESSION_IDLE_TTL` / `XBIN_SESSION_MAX_TTL`.
- auth: **audit log** — every mutating core-API call (`POST/PUT/PATCH/DELETE`
  on `/api/xbin/…`, minus the `prefs`/`kv`/`blob`/`bus` data plane) logs an `audit` line
  with actor + path + status; a who-changed-what trail for governance actions
  (users, grants, lifecycle, vault, token rotation).
- users: **password floor** — accounts created/updated through the API need
  an 8-character minimum (enforced at the API so the dev admin/tests, which
  write the store directly, are unaffected); the admin UI surfaces create/
  reset errors instead of silently clearing the form.
- users: **user ids are now validated at creation and immutable** (D15) —
  `[a-z0-9][a-z0-9._-]{0,31}`, `owner` reserved. An id is the permanent key
  for `homes/<user>`, the prefs bucket, and `user:<id>` attribution, so it
  can't be renamed or contain path/attribution separators; the constraint is
  set before GA because it can't be tightened once ids exist on disk.
  Existing users (loaded from disk) are unaffected — only new creates are
  gated.
- auth: **owner-token rotation** — `POST /api/xbin/auth-rotate-token` (admin;
  a button under the admin console's Users → sign-in security) rewrites
  `.xbin/token` and swaps it live: the old token stops authenticating
  immediately, bearer and cookie alike. **Rotate once after upgrading to
  terminal-scoped tokens** — before 2026-07-09 every terminal carried the
  owner token, so old agent transcripts/shell histories under `homes/` may
  hold it. (Terminal tokens themselves already revoke automatically: each
  dies with its session; deleting a user kills theirs instantly.)
- shell: tiles' **title bars gain a `>_` terminal button** (replacing the
  tiny 7×7 corner square on shell cards; standalone frames keep the corner
  button), and a **workspace settings** menu (🔧 in the top bar) with a
  per-user **global font size** (persisted via prefs, applied as a clean
  zoom). The terminal settings icon is now a wrench (🔧) instead of a gear.
- **connection-leak fix, both sides of the proxy.** xbind built a fresh HTTP
  transport per proxied request, stranding one keep-alive connection per RPC
  — on the backend that parked a goroutine + ~15 KB forever (the SDK server
  had no idle timeout), and xbind leaked the matching client conn. The proxy
  now pools one transport per backend socket (reused across requests, idle
  conns reaped at 90s, evicted when a generation's socket goes away), and
  `xbin.Serve` sets `IdleTimeout: 120s` + `ReadHeaderTimeout: 30s` (hijacked
  WebSocket conns exempt; no blanket read/write timeouts — SSE/streams still
  run indefinitely). xbind's own TCP + gateway listeners got the same
  timeouts. Backends pick up the SDK fix on their next rebuild — every spawn
  rebuilds, so an xbind upgrade + restart covers the fleet.
- terminals: **tile-scoped tokens — the owner token no longer enters any
  terminal.** A terminal's `XBIN_TOKEN` is now a per-session token resolving to
  the TILE the terminal is opened on (its element principal: admin of itself +
  its approved grants/bindings — the frame-token model, for shells), never the
  human's privilege. Agents in tile terminals can no longer read other tiles'
  admin config, call `/api/xbin` admin endpoints, or **self-approve grants**
  (now enforced, not just documented). Session-open gates: terminals only on
  tiles the user may use; **the root terminal is disabled** (workspace-wide
  work: the browser UI, or the host shell — the owner token lives only in
  `.xbin/token`). Kill/reattach of another user's session: admins only.
  Deleting a user kills their live shells' API access. **Breaking** for
  workflows that ran admin `bx` from inside tile terminals — migration:
  `docs/changes/2026-07-09-terminal-scoped-tokens.md`.
- terminals: **$HOME is now per user** — `homes/<user>` instead of one shared
  `home/`, so each human's agent-CLI config (`~/.claude`, credentials), shell
  history, and dotfiles stay their own (the root token uses `homes/owner`;
  seeded lazily on first terminal; a non-admin can't attach to another user's
  session). A legacy shared `home/` **migrates automatically at startup** to
  the workspace's sole user/admin (else `homes/owner`) — xbind refuses to
  start only when both forms hold real data, so nothing is merged by guesswork.
  Migration note: `docs/changes/2026-07-09-per-user-homes.md`.
- terminals: **base-image versioning + safe upgrades.** A terminal's
  persistent sandbox layer (apt installs / system changes) is now stamped with
  the base image it was built on and **pinned** to it — xbind never stacks that
  overlay on a different base (which corrupts apt/dpkg state). When a newer
  base is installed the terminal tile shows a **base-update** banner; clicking
  it (or the reset ⟲) rebuilds on the current base — installed packages are
  wiped, your workspace files and `$HOME` are kept. The `/ws/term` `session`
  message carries `baseOutdated`. Base images are stamped by `build-rootfs.sh`;
  `install.sh` preserves the old base as `rootfs-<version>` on upgrade (legacy
  unstamped bases become `rootfs-v0`); xbind aborts startup if a pinned base is
  missing and GCs preserved bases once no terminal pins them. Design:
  `plans/component-env.md`.

## 2026-07-08

- terminal: a **settings menu** (the ⚙ that appears top-right on hover),
  starting with a **theme picker** — Default (dark-steel), Dracula, Nord,
  Solarized Dark/Light, Monokai, Gruvbox Dark, One Dark, Tango Dark, GitHub
  Light — plus a font-size stepper. Applied live, shared across open terminals
  (same document + other tabs), and persisted in the browser.

- lifecycle: **offloading a component now fully quiesces it.** Previously an
  offloaded tile kept firing cron, still surfaced pending access/binding
  requests, and stayed visible in the shell + the admin principals list.
  Now: the cron scheduler skips jobs for non-enabled components; `Pending()`
  and pending-bindings omit offloaded components; the shell hides them (out of
  the sidebar/folders, and any open card is closed on the reload); the admin
  console lists offloaded tiles in a separate section, not the main table.
  `GET /api/xbin/components` now carries each component’s lifecycle `state`.

- interfaces: **fixed a binding-role flap**. A provider with several http
  provides (e.g. llm-gw: `openai`=writer + `metrics`=reader) resolved a
  binding’s granted role from an *arbitrary* provide — provides live in a map
  with no stable order — so a caller bound to `openai` (writer) intermittently
  got `reader`, failing ~15–30% of calls with "role writer required". The
  role/endpoint/validation now select the provide **by the requester slot’s
  service**, deterministically. (Introduced when llm-gw gained its second
  provide; single-provide tiles were never affected.)
- llm-gw: the stats table groups digits with **commas** (`123,142`) instead of
  a thin space, which was hard to read against the column gaps.

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
