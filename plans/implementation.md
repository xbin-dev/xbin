# XBin — Implementation Plan

Companion to `../ARCHITECTURE.md`. Phases are strictly incremental: each ends in a
runnable, demoable state, and nothing in a later phase requires reworking an earlier
one (the Runner interface and manifest-as-truth rules exist precisely to guarantee
that).

Decisions that need an opinion are marked `[D#]` and collected in `DECISIONS.md`.

## Repo layout (the xbin project itself)

```
xbin/
  cmd/xbind/            # main: flags, wiring, PID-1 duties (signal fwd, zombie reap)
  internal/
    server/              # mux, auth middleware, static /c/ serving, HTML injection
    registry/            # workspace scan, manifests, component model
    term/                # PTY sessions (creack/pty), WS protocol, scrollback
    watch/               # recursive fsnotify wrapper: debounce, ignore rules
    runner/              # Runner iface; process/, cgi/ implementations
    proxy/               # unix-socket reverse proxy incl. WebSocket pass-through
    events/              # /ws/events hub: live-reload, build errors, bus (phase 4)
    broker/              # resources (phase 4)
    deps/                # symlink materializer, go.work generator (phase 3)
  sdk/go/                # module github.com/…/xbin/sdk/go: xbin.Serve() etc. [D10]
  web/                   # bx-frame.js, bx-terminal.js, xbin-client.js, styles
  web/vendor/            # pinned, checked-in ESM builds: lit, @xterm/* [D-resolved: check in]
  workspace-template/    # scaffold for `xbind init`: root/, xbin.json, .gitignore
  examples/              # sample components used by integration tests and docs
  docker/Dockerfile
  hack/                  # dev scripts
  Makefile
```

- Single Go module. Web assets ship via `go:embed` (one static binary); `--dev` serves
  `web/` from disk instead.
- No JS build step anywhere, including for xbin's own frontend. `web/vendor/` is
  updated by `hack/vendor.sh` (fetches pinned ESM builds, commits them).

## Milestones

| # | Name | Outcome | Rough size |
|---|------|---------|-----------|
| 1 | Walking skeleton | Render dirs, edit via terminal, live reload | ~2–3 kLOC |
| 2 | Backends | Go/node/python/cgi backends, rebuild-on-change, `/api/` | ~2 kLOC |
| 3 | Structure | Manifests, scopes, deps, go.work, `bx` CLI | ~1.5 kLOC |
| 4 | Resources & auth | Gateway RBAC, grants, vault, broker (sqlite/kv/blob/bus/cron), tier-2 uids | ~3 kLOC |
| 5 | Later | element.js inline mode, mount-ns, wazero, subdomains | — |

---

## Phase 1 — Walking skeleton

**Goal:** point xbind at a directory tree; every dir with an `index.html` renders in
`<bx-frame>`; the 7×7 button opens a persistent shell in that dir; saving a file
reloads the frame. No manifests, no backends.

### Tasks

**xbind core**
- `cmd/xbind`: flags `--workspace`, `--listen` (default `127.0.0.1:8642`), `--dev`,
  `--open-token`; `log/slog` logging; graceful shutdown.
- `xbind init <dir>`: copies `workspace-template/` (root component, `xbin.json`,
  `.gitignore`), runs `git init` per policy `[D2]`.
- Auth `[D3]`: on start, generate/load token from `.xbin/token`, print a one-time
  login URL (Jupyter-style); URL sets an HttpOnly cookie; middleware guards **every**
  route including `/c/` static and both WS endpoints. `--dev` implies `--no-auth`.

**Static serving (`internal/server`)**
- `GET /c/<component-path>/…` → files under `<workspace>/<component-path>/`.
  - Path safety: clean + confine to workspace root; reject traversal, reject `.xbin/`.
  - Dir URL → `index.html`; correct MIME (explicit table for `.js`/`.mjs`/`.css`/
    `.wasm`); `Cache-Control: no-store` (always, not just dev — this is a live system).
  - Serve-time injection into HTML `<head>`: `<script type="importmap">` (workspace
    map, later merged with scope map) + `<script type=module src=/vendor/xbin-client.js>`.
    This is the **single sanctioned HTML transform** in the system `[D4]`. Byte-exact
    pass-through for non-HTML.
- `GET /vendor/…` → embedded `web/vendor`.
- `GET /` → redirect to `/c/root/`.

**Terminal (`internal/term`)**
- `WS /ws/term?cwd=<component-path>&session=<id?>`.
- Wire protocol: binary WS frames = raw PTY bytes; text frames = JSON control
  (`{"op":"resize","cols":..,"rows":..}`, `attach`, `ping`).
- Session registry: create with UUID; PTY + shell survive WS disconnect; reattach
  replays a bounded scrollback ring (256 KiB per session, configurable); idle sessions
  (no client AND no fg process activity, 24 h) reaped.
- Spawn: `$SHELL` (fallback `/bin/bash`), `cwd` validated against workspace,
  env: `HOME` per `[D6]`, `XBIN_COMPONENT=<path>`, `XBIN_URL=http://127.0.0.1:8642`.
- Cap: max sessions (default 32), max scrollback memory global.

**Watcher (`internal/watch`)**
- fsnotify is non-recursive → maintain a watch per directory, add/remove on
  create/delete events.
- Hard ignore list: `.git/`, `.xbin/`, `node_modules/`, `deps/` (symlinks would
  double-fire), editor droppings (`*.swp`, `~`, `.#*`, `4913`).
- Debounce 300 ms per component; **coalesce editor atomic-save sequences**
  (write-tmp → rename) into one event.
- Emits `(componentPath, kind)` on an internal channel consumed by events hub (and
  runner in phase 2).

**Events (`internal/events`)**
- `WS /ws/events`: JSON messages `{"type":"reload","component":"…"}`. Fan-out hub,
  slow-client eviction.

**Frontend (`web/`)**
- `bx-frame.js` (Lit): renders `<iframe src="/c/<src>/">` — sandboxed (opaque
  origin) for non-chrome components since ND8, unsandboxed for chrome;
  7×7 px edit button (top-right, 35 % → 100 % opacity on hover); listens to
  `/ws/events` (one shared socket via module singleton) and reloads iframe on its
  component's `reload`. Height: fixed/CSS by default; auto-size only when the frame's
  `xbin-client.js` posts `resize` (guard against resize loops with 1 px hysteresis).
- `bx-terminal.js`: xterm.js + fit addon; binary WS plumbing; reconnect-and-reattach
  with backoff; renders in a bottom drawer element `bx-editor-drawer` owned by
  bx-frame (drawer holds ≥1 terminals, tab bar, "new terminal" button).
- `xbin-client.js` (runs *inside* frames): posts `resize` via ResizeObserver;
  `navigate` interception left for later; exposes `window.xbin` stub.

**Workspace template**
- `root/index.html`: import-map-driven Lit page with a hardcoded couple of
  `<bx-frame>`s and a short "mkdir your first component" hint. Root is a normal
  component — editing it via its own 7×7 button must work (the self-hosting smoke
  test).

### Acceptance demo
1. `xbind init ws && xbind --workspace ws` → browser shows root.
2. In root's terminal: `mkdir hello && vim hello/index.html` → add
   `<bx-frame src="hello">` to root → hello renders.
3. Edit `hello/index.html` in vim, `:w` → frame reloads < 500 ms.
4. Kill browser tab, reopen, reattach to the same shell session, scrollback intact.

### Risks / notes
- Watch-per-dir scales to thousands of dirs but a stray `node_modules` inside a
  component would blow the inotify budget — the ignore list is load-bearing; also
  raise `fs.inotify.max_user_watches` in the container image.
- Scrollback replay + live PTY race on reattach: sequence via session mutex,
  replay-then-stream.

---

## Phase 2 — Backends

**Goal:** `runtime: go|node|python|cgi` components serve `/api/<component-path>/…`,
rebuilt/restarted on save, blue/green, with build errors surfaced in the frame and
terminal. PHP feel, Go reality.

### Tasks

**Manifest v0 (`internal/registry`)**
- Parse `xbin.json` (JSONC `[D5]`): `runtime`, `entry` (default: `backend/` for go,
  `backend/server.js` node, `backend/handler.py` cgi-python…). No `deps`/`uses`
  yet. Absent manifest = `static`.

**Identity plumbing (auth.md §2–3, minimum viable)**
- Per-generation instance credential minted at spawn (`XBIN_GATEWAY` socket +
  token); dies at blue/green swap.
- Proxy strips inbound `X-XBin-*`, injects verified `X-XBin-From` (`owner` for
  browser/CLI in this phase — element→element calls arrive in phase 4, but headers,
  stripping, and SDK `Caller()` land now so callee code written in phase 2 never
  changes shape).

**Runner (`internal/runner`)**
- `Ensure(ctx, comp) (Target, error)` / `Stop(comp)`; per-component serialized state
  machine: `idle → building → starting → healthy → draining → stopped | failed`.
- **process runner**:
  - Build step (go): `go build -o .xbin/build/<id>/next ./backend` with shared
    `GOCACHE`/`GOMODCACHE` under `.xbin/cache/`; capture stderr for the overlay.
    Global build semaphore = `min(NumCPU, 4)`.
  - Start: exec with `XBIN_SOCKET=.xbin/run/<id>/<gen>.sock`, `XBIN_COMPONENT`,
    resource env (phase 4); stdout/stderr → `.xbin/log/<id>.log` (lumberjack-style
    rotation) and tee to events hub.
  - Health: wait for socket connect + optional `GET /healthz` (200 or 404 both OK),
    3 s timeout.
  - Swap: atomically repoint proxy target; old gen gets SIGTERM after drain; hard
    kill at 30 s `[D8]` (long-lived WS to old gen die then — documented behavior).
  - Node/python: same runner, `build = nil`, restart-on-change.
  - Lazy start on first request; idle reap after 30 min (configurable per component),
    next request restarts (~100–300 ms penalty, fine).
- **cgi runner**: per-request exec, CGI/1.1 env vars, body on stdin, response on
  stdout; 30 s timeout; concurrency cap per component.
- Crash-loop breaker: >3 exits in 10 s → `failed`, error overlay, no restart until
  next file change.

**Proxy (`internal/proxy`)**
- `httputil.ReverseProxy` over unix socket; must pass WebSockets (Go ≥1.21 RP handles
  Upgrade over unix transport) and streaming (flush interval 0 for SSE).
- Injects `X-XBin-From: browser` (phase 4 makes this meaningful for server-to-server).

**SDK (`sdk/go`)**
- `xbin.Serve(h http.Handler)`: listen on `XBIN_SOCKET`, SIGTERM → graceful
  shutdown; `xbin.Caller(r)`, `xbin.Role(role, h)` (see auth.md §8). Zero deps.
  Equivalent snippets (not packages) documented for node/python.

**Frontend**
- Error overlay: `reload` event variant `{"type":"build-error","text":…}` →
  bx-frame renders overlay instead of reloading; next success clears it.
- `bx logs`-in-browser deferred; logs reachable via terminal (`tail -f .xbin/log/…`)
  — note `.xbin` is shell-visible on purpose.

**Examples + integration tests**
- `examples/counter-go/` (Go backend + fetch from index.html),
  `examples/notes-cgi/` (shell script). Integration test: temp workspace, real
  `go build`, curl `/api/...`, touch source, assert new behavior; assert overlay on
  syntax error.

### Acceptance demo
Edit `examples/counter-go/backend/main.go` in the component's own terminal, `:w`,
refreshing `curl /api/examples/counter-go/count` reflects the change in ≤ 1.5 s; break
the code → overlay with compiler output; fix → recovers.

### Risks / notes
- First build of a component pays module download; pre-warm `GOMODCACHE` for the SDK
  in the image.
- Two rapid saves: state machine must cancel an in-flight build (`context` on the
  `go build` cmd) rather than queue-pile.
- Unix socket path length limit (108 bytes): use short hashed run dir
  (`.xbin/run/<8-char-hash>/s.sock`), map in registry.

---

## Phase 3 — Structure

**Goal:** the full component contract: scopes, declared deps materialized as
symlinks, generated `go.work`, `bx` CLI, scope import maps.

### Tasks
- Manifest v1: `deps`, `expose` (incl. `roles` + descriptions, auth.md §3),
  `uses` (parsed and validated; enforcement arrives with the gateway in phase 4);
  `scope.json` (marks a dir as scope root; scope-level import-map extension, later
  resources).
- `API.md` convention: `bx new` scaffolds the standard skeleton when `--expose`;
  `bx doctor` warns on `expose` without `API.md`; `bx api <path>` renders
  roles + API.md.
- `internal/deps` materializer: reconcile `deps/` symlinks to manifest on
  scan/change; refuse to touch non-symlink files in `deps/` (user data protection);
  relative symlink targets (tree stays relocatable). Cycle detection = warn, allow
  (they're just links).
- `go.work` generator: workspace root, one `use` per Go component; **generated with a
  marker header**; if user edits remove the marker, xbind leaves the file alone and
  warns (`bx doctor` reports drift) — resolves generated-vs-hand-edited ownership.
- Import-map merge: workspace map ∪ scope map, scope wins; injected per §Phase 1.
- `bx` CLI `[D7]` (thin HTTP client of xbind, ships in image, in PATH of every
  terminal): `bx new <path> [--runtime go]`, `bx logs [-f] <path>`, `bx ls`,
  `bx doctor` (checks: manifest parse errors, dangling deps, go.work drift, socket
  leaks). Talks to `XBIN_URL` with the local token.
- Registry: component metadata endpoint `GET /api/xbin/components` (xbind's own
  API lives under the reserved id `xbin` — path `xbin/` is reserved in workspaces).

### Acceptance demo
`bx new apps/todo --runtime go` scaffolds; add `"deps":["lib/ui-kit"]` → `deps/ui-kit`
symlink appears; `import "…/lib/ui-kit/pkg"` compiles via go.work from any shell.

---

## Phase 4 — Resources & auth enforcement

**Goal:** the full `auth.md` runtime plane: gateway RBAC, grants, vault, broker
resources, per-scope uids (tier 2, pending `[ND1]`); calendar/email story
end-to-end with a real grant approval.

### Tasks

**Gateway & grants (auth.md §3)**
- Grant table in workspace `xbin.json`: `(caller, target, role)`; hot-reload on
  change; default-deny for element principals.
- Element→element calls: `xbin.Client()` over the per-instance gateway socket →
  authn (instance credential) → grant check → forward with verified
  `X-XBin-From`/`X-XBin-Role`.
- `uses` reconciliation: unsatisfied entries → pending grants; same-scope
  auto-approve `[ND5]`; approval panel in xbind's own UI (rendered into root) +
  `bx grant <caller> <target>:<role>`; revocation = row delete.
- Role implication (`admin ⊃ writer ⊃ reader`, manifest `implies` for custom names)
  resolved in xbind, echoed by SDK middleware.
- Frame tokens `[ND2]`: minted at HTML-serve (D4 injection), TTL + refresh endpoint,
  `xbin.fetch()` attaches; `/api/` policy per auth.md §6; 403 body links `bx api`
  docs.

**Vault (auth.md §4)**
- Broker API `GET/PUT/DELETE /api/xbin/vault/<own>/<key>`; own-vault-only, no
  cross-element grants; storage `data/vault/` xbind-uid 0600, gitignored,
  backup-excluded by default; `bx vault set/get/ls`; SDK `xbin.Secret()`.
  At-rest encryption deferred `[ND3]`.

**Broker resources (auth.md §5)**
- `scope.json` `resources` (types: `sqlite`, `kv`, `blob`, `bus`, `cron`);
  provisioning under `data/resources/<scope-key>/`; all addressed as
  `res:<scope>/<name>` targets in `uses`.
- `sqlite`: same-scope → direct file path at spawn; cross-scope → brokered query API
  only (no path disclosure).
- `kv`: bbolt, namespaced buckets; `blob`: dir + quota, streamed via broker.
- `bus`: topics; publish via broker API, subscribe via `/ws/events` (backend) and
  `window.xbin.bus` (postMessage bridge). At-most-once, in-memory.
- `cron`: job registry (spec → own endpoint + role, bounded by own roles); invoked
  as `X-XBin-From: xbin/cron`; jobs persisted in `data/resources`, survive
  restarts; `bx cron ls`.

**Tier 2 — per-scope uids (if `[ND1]` approved)**
- uid allocator (20000+, persisted mapping); spawn backends per-scope-uid (xbind is
  root per D13b); source dirs stay owner-writable/world-readable → elements can't
  write source; `.xbin/run` sockets, vault, data perms enforced; cross-scope
  shared-rw sqlite → brokered (default) or per-resource setgid group.
- Integration tests that *attack*: sibling env read, sibling socket connect, source
  write, vault read — all must fail at tier 2 (and are documented-known-pass at
  tier 1).

**Examples (double as docs)**
- `apps/calendar`: sqlite + `expose.roles` + `API.md`; `apps/email`: `uses`
  calendar:reader + bus badge + an IMAP password in its vault.

### Acceptance demo
`apps/email` declares `uses: apps/calendar:reader` → pending grant appears → one
click approves → email's "today" widget populates and live-updates via bus; a raw
un-tokened `fetch` from a third element to calendar 403s; at tier 2, `id` in
calendar's backend shows its scope uid and `touch backend/main.go` from that backend
fails.

---

## Phase 5 — Later (unordered backlog)
Inline `element.js` mode; per-terminal mount namespaces (ref-pinned deps);
`runtime: go-wasm` (wazero) with broker-enforced capabilities (auth tier 3);
netns-based egress control per scope; subdomain-per-scope browser isolation;
`search` resource type (workspace full-text index, bleve); vault at-rest encryption
(`[ND3]`); multi-user (owner → per-user role grants); component store/sharing
(`bx export` / `bx import` tarballs); Postgres resource type; browser log viewer;
`bx snapshot` (git-based checkpoints).
