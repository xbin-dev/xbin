# Buxon — Architecture

A self-modifying, in-browser, multi-directory project workspace. Every piece of UI on
screen is backed by a directory you can open a shell into and edit live — including the
workspace shell itself. Think Jupyter's "the document is the program" premise, but for
whole applications, with real backends, and Smalltalk-image-style self-hosting.

---

## 1. Core model

Three levels:

```
Workspace  →  Scope  →  Component
(container)   (app)     (element)
```

- **Component** — a directory. The atomic unit. Has a view (`index.html` or a Lit
  element), optionally a backend, optionally declared dependencies and resource grants.
- **Scope** — a directory subtree grouping components into an "app" (calendar, email).
  Owns a resource namespace and can expose service APIs to other scopes.
- **Workspace** — the whole tree, one Docker container, one data dir.

**The self-hosting rule:** the workspace root UI is itself just a component
(`/workspace/root/index.html` composing `<bx-frame>` elements). There is no privileged
"chrome" that can't be edited from inside. This is the property that makes Buxon more
than a dashboard builder.

### Component contract

```
my-component/
  buxon.json        # manifest (optional — sane defaults for bare dirs)
  index.html        # view, iframe mode (default)
  element.js        # view, inline widget mode (opt-in, trusted)
  backend/          # backend entry, runtime-specific (main.go / server.js / handler.py)
  deps/             # materialized dependency links, managed by buxond (§5)
  ...anything else  # it's just a directory; shells can do whatever
```

```jsonc
// buxon.json
{
  "runtime": "go",                        // go | node | python | cgi | static (default)
  "deps": ["lib/ui-kit", "apps/calendar/widgets/month-view"],
  "resources": [
    { "name": "calendar/db", "access": "ro" }
  ],
  "expose": { "api": true }               // other components may call /api/<this>
}
```

Component IDs are workspace-relative paths (`apps/calendar/widgets/month-view`). No
registry to keep in sync; `mv` is rename, `cp -r` is fork. buxond watches the tree and
rescans manifests on change.

---

## 2. Frontend: no-build Lit

- Plain ES modules + **import maps**. Lit works buildless from an import map; no
  transpile, no bundler, ever. TypeScript stays out of the core (or type-checked
  via JSDoc if wanted — still no build step).
- All vendor deps (`lit`, `@xterm/xterm`, addons) are **vendored into
  `/workspace/vendor/`**, not loaded from a CDN. A workspace must be fully
  self-contained and offline-capable; `buxon vendor add <pkg>` fetches ESM builds.
- The import map is served at the document level. Scopes can extend it (buxond merges
  workspace + scope import maps when serving a scope's documents).
- Since iframed components are their own documents, buxond performs exactly **one
  sanctioned HTML transform** when serving component HTML: injecting the merged import
  map and `buxon-client.js` into `<head>` (opt-out via manifest `"inject": false`).
  No other rewriting, ever. (Decision D4 in `plans/DECISIONS.md`.)

### `<bx-frame>` — the main element

```html
<bx-frame src="apps/calendar"></bx-frame>
```

Responsibilities:

1. **Render** the component's view.
2. **Edit affordance**: a 7×7 px always-visible button, absolute top-right,
   ~35% opacity idle / 100% on hover. Click → terminal drawer (§3).
3. **Live reload**: subscribes to `/ws/events`; reloads the frame when the component's
   files change. Build failures render as an error overlay inside the frame (Vite-style)
   instead of a broken page.

**Rendering: two tiers.**

| | iframe mode (`index.html`) | inline mode (`element.js`) |
|---|---|---|
| Isolation | full document, CSS/JS isolated | shadow DOM only, shares JS realm |
| Relative URLs | just work (`/c/<id>/` base) | must use `import.meta.url` |
| Sizing | ResizeObserver + postMessage protocol | natural layout |
| Use for | apps, untrusted/experimental stuff | tight widgets, design-system parts |

Default is **iframe** at `/c/<component-id>/`. It's the only way `index.html` semantics
(relative script/img/css URLs, its own import map, global-variable freedom) survive
without a rewriting compiler — injecting fetched HTML into a shadow root breaks script
execution, URL resolution, and per-document import maps. Inline mode is the opt-in for
components that export a custom element and accept shared-realm discipline; parents
embed those directly for seamless layout.

Frame↔host protocol (postMessage, tiny): `resize`, `navigate`, `open-editor`,
`bus` (event forwarding, §6). A ~50-line `vendor/buxon-client.js` inside the frame
handles it; frames work (degraded) without it.

### `<bx-terminal>`

Thin Lit wrapper over **xterm.js** (`@xterm/xterm` + `fit` + `attach`-style WebSocket
plumbing + `webgl` renderer). Connects to `/ws/term`. Nothing else browser-side.

---

## 3. Edit mode & terminals

Click the 7×7 button → drawer slides up (bottom sheet, per-frame; detachable to a side
panel) containing `<bx-terminal>` with a shell **cwd'd to the component's source dir**.

Server side:

- `WS /ws/term?cwd=<component-id>&session=<id?>` → buxond spawns `$SHELL` via
  `creack/pty`.
- **Sessions persist server-side.** Disconnect ≠ SIGHUP; reattach by session id
  (tmux semantics without tmux; scrollback ring buffer kept in buxond). Multiple
  terminals per component allowed.
- The shell is a *real* shell in the *real* tree. `vim`, `go test`, `git`, `claude` —
  whatever the container image ships. Editing is not mediated by Buxon at all; Buxon
  only *reacts* to file changes. This keeps the editing story trivially powerful and
  the core small.

Terminals are the owner plane, deliberately not sandboxed from the workspace; the
**outer boundary is the VM/host** buxond runs on (§8). Component *backends*, by
contrast, do get per-component OS isolation under `--isolate` (`plans/isolation.md`).

---

## 4. Backend: hot-reload with real languages

### Routing

buxond (single Go binary) owns the front door:

```
GET  /c/<component-id>/...     static files of the component dir
ANY  /api/<component-id>/...   reverse-proxied to the component's backend
WS   /ws/term                  terminals
WS   /ws/events                live-reload + event bus
GET  /                         serves the root component
```

### Runner abstraction

Per-component backends run under a `Runner` interface; runtimes are pluggable:

```go
type Runner interface {
    // Ensure the backend for this component is current and serving; returns
    // a proxy target (unix socket). Called lazily on first request and on
    // file-change events.
    Ensure(ctx context.Context, c *Component) (Target, error)
    Stop(c *Component)
}
```

**Primary runtime: process-per-component with rebuild-on-change.** This is the
PHP-feel-for-Go answer, and it's boring on purpose:

1. fsnotify on the component dir, debounced ~300 ms.
2. `go build -o .buxon/build/<id>/next` with **shared `GOMODCACHE`/`GOCACHE`** across
   the workspace → incremental rebuilds of small components are subsecond.
3. Start the new process; it serves HTTP on a unix socket passed via
   `BUXON_SOCKET` (SDK: `buxon.Serve(mux)` — ~20 lines, plain `net/http`).
4. Health check → atomically swap the proxy target → drain and kill the old process
   (blue/green; in-flight requests finish).
5. Build failure → keep old process, push the compiler output to `/ws/events` (error
   overlay + terminal).

Same runner covers Node/Python/Bun with the build step skipped (restart-on-change),
so "more sane languages" is symmetric: `runtime: go` just adds a compile.

Components with no backend (`static`) cost nothing. Idle backends can be reaped and
lazily restarted on next request (CGI-ish economics, resident-process performance).

**Secondary runtime: `cgi`** — exec a script per request, env-passed request, stdout
response. Zero state, zero lifecycle, perfect for `handler.sh`/`handler.py` one-offs.
True PHP semantics for when you want them.

### Considered and deferred

- **Yaegi (interpreted Go):** genuinely PHP-like for Go, but no cgo, incomplete
  stdlib/reflection edges, third-party deps are painful, and perf is interpreter-tier.
  Rebuild-on-change gives 95% of the experience with 0% of the compatibility cliff.
  Could ship later as a `go-script` runtime for single-file handlers.
- **WASM (`GOOS=wasip1` + wazero):** the interesting long-term option — in-process
  hot-swappable modules, and **capability-based sandboxing that maps beautifully onto
  the resource-grant model** (§6): a component literally cannot open what it wasn't
  granted. Deferred because wasip1 has no sockets (HTTP needs a hostcall shim),
  Go-on-wasip1 is single-threaded, and debugging is worse. The Runner interface is the
  seam; wazero becomes `runtime: go-wasm` when wanted, no architectural change.
- **Go `plugin` package:** no. Unloadable, version-locked, Linux-only pain.

---

## 5. Cross-component code access

Requirement: a shell opened in component A can see and edit the code of A's
sub-components and dependency components.

**Sub-components: plain nesting.** Children are subdirectories
(`apps/calendar/widgets/month-view/`). A shell in the parent trivially sees children.
Containment *is* the dependency for the composition case. No machinery.

**Dependency components: manifest-declared, symlink-materialized.** `deps` in
`buxon.json` is the source of truth; buxond maintains `deps/<name> → ../../lib/ui-kit`
symlinks. Cheap, visible in `ls`, honest, survives tools that don't know about Buxon.
The manifest-as-truth detail matters: because components never hardcode the mechanism,
the materialization can be upgraded later without changing any component:

- **Per-terminal mount namespaces** (Plan 9 style): spawn each PTY shell in its own
  `unshare -m` namespace with dependency dirs bind-mounted into `deps/`, optionally
  read-only, optionally **pinned to a git worktree of a specific ref** — versioned
  dependency views per shell. All doable inside one container (needs
  `CAP_SYS_ADMIN`/userns).
- **FUSE view** (`/bux/<id>/deps/...`): maximal virtualization (synthesized manifests,
  remote components). Only if a real need appears.

Start with symlinks; keep the escalation path.

**Go-level code sharing:** buxond generates and maintains a workspace root `go.work`
listing every Go component module. Any shell anywhere builds against dependency
components' packages directly, cross-component refactors work, and gopls in any
editor sees the whole workspace. Node equivalent: workspace-root `package.json`
workspaces, or just the `deps/` symlinks (Node resolves through symlinks fine).

---

## 6. Shared resources

The calendar-DB-tapped-by-email problem. Two mechanisms, with a strong default:

**A. Service APIs (preferred).** The calendar scope *is* a backend; email calls it
server-side at `http://buxon/api/apps/calendar/...` through buxond's gateway, which
authenticates the caller, checks the grant table, and injects verified
`X-Buxon-From: apps/email` / `X-Buxon-Role: reader`. Callees declare **roles**
(`expose.roles`, conventionally `reader`/`writer`/`admin`) and callers request them
(`uses: [{target, role}]`) — RBAC with callee-defined roles, owner-approved grants,
buxond-verified identity, callee-enforced routes. Elements also each get a private
**vault** for third-party secrets, encrypted at rest by an AES-256-GCM barrier
with a passphrase-derived key held only in memory (seal/unseal; the key never
lives in the at-rest data). Full design: `plans/auth.md`.

**B. Brokered resources (for when you really want shared state).** Declared at scope or
workspace level:

```jsonc
// apps/calendar/scope.json
{ "resources": { "db": { "type": "sqlite" }, "attachments": { "type": "blob" } } }
```

buxond's **resource broker** provisions them under `/workspace/data/resources/<scope>/`
and hands grants to components at process start via env
(`BUXON_RES_CALENDAR_DB=/workspace/data/resources/apps~calendar/db.sqlite?mode=ro`).
Resource types, v1:

- `sqlite` — the default database. Zero-ops, file-per-resource, WAL mode; same-scope
  grants hand the file path directly, cross-scope access is brokered (query API, no
  path disclosure). Postgres becomes a `type` later (broker provisions a database +
  role in a sidecar) without changing the model.
- `kv` — namespaced keys in a buxond-owned store (bbolt), over a tiny HTTP API.
- `blob` — a directory with quota.
- `bus` — pub/sub topics. Served by buxond over `/ws/events`; bridged into frames via
  the postMessage protocol, so *frontend* components get cross-app reactivity too
  (email badge updates when calendar writes an event) without polling.

Cross-scope grants (`res:calendar/db` requested by `apps/email`) prompt once in the UI
and are recorded in the workspace manifest — a visible, versioned, git-diffable
capability table. Enforcement hardens in tiers (`plans/auth.md` §9): gateway
default-deny from day one, per-scope uids make identity and file grants mechanical
(and strip running elements of write access to source — editing stays terminal-only),
wazero/netns later for syscall- and egress-level caps.

---

## 7. The host & data layout

buxond runs on a **VM/host it controls**, one bind-mounted data dir. Host is
cattle, workspace is pet.

> **Where we landed** (`plans/runtime.md`): buxond is the sandbox runtime —
> workloads get namespaces (`plans/isolation.md`) over a fat OCI **base rootfs**
> (toolchains + `opencode`/`claude-code`) that backs both sandboxes and
> terminals. The old **single-Docker-container runtime was dropped** (it was
> container-as-boundary, no per-component isolation); Docker survives only as a
> build tool for the rootfs. An immutable **virtual appliance**
> (qcow2/OVA/ISO/cloud) is the eventual on-ramp.

```
/workspace                  ← bind mount; ALL non-runtime state
  buxon.json                # workspace manifest, grants table
  root/                     # the root UI component (self-hosted shell)
  apps/<scope>/...          # scopes and their components
  lib/...                   # shared library components
  vendor/                   # vendored frontend deps (lit, xterm, buxon-client)
  data/resources/...        # broker-provisioned state (sqlite, blobs, kv)
  .buxon/                   # derived state: build outputs, caches, logs, PTY scrollback
                            #   — safe to delete, excluded from backup/git
/opt/buxon/buxond           # the binary
/opt/toolchains             # go, node, python, git, vim, etc. (baked in image)
```

- `git init /workspace` (ignore `.buxon/`, decide per-workspace on `data/`) — the whole
  workspace including its own UI is versioned. Time-machine via git.
- Upgrading Buxon = new image, same mount. buxond migrates manifests if needed.
- buxond runs as PID 1 (or under tini), supervises component processes as children,
  reaps zombies, cgroup-limits per-scope if desired.

---

## 8. Security posture

Honest framing: Buxon is a **remote code execution appliance by design**. Therefore:

- The **VM/host is the outer boundary**; per-component OS namespaces (`--isolate`,
  `plans/isolation.md`) are the inner one. Run the host like a dev box: no secrets
  you wouldn't put on one, its own network segment if paranoid.
- **Two planes** (`plans/auth.md`): the *editing plane* (terminals, `bx`, git) is
  owner-privileged and unrestricted; the *runtime plane* (running elements) is
  default-deny, identity-carrying, role-scoped. Owner auth at the front door:
  one-time login URL → cookie, bearer token for CLI, guarding every route incl. WS.
  Multi-user is out of scope for now (the RBAC model leaves room for it).
- Browser-side, iframes give incidental containment but everything is same-origin;
  frame tokens make cross-element calls *attributed* and grant-checked, not
  *isolated*. If scope-level origin isolation ever matters, serve scopes on
  subdomains (`calendar.myworkspace.example`) — the `/c/` URL scheme maps onto that
  cleanly.
- Intra-workspace hardening is tiered (`plans/auth.md` §9): instance credentials +
  gateway default-deny → per-scope uids (identity, vault, and file grants become
  mechanical; elements lose write access to source) → **mount+network namespaces:
  filesystem isolation, ingress = runtime-only, and default-deny egress with
  `net:*` grants** (`plans/isolation.md`). Each tier tightens the floor without
  changing the model.

---

## 9. Alternate takes (considered)

- **Deno as the single backend runtime.** Genuinely strong: TS with no build step,
  built-in permission flags that match the resource-grant model, one runtime to
  supervise. Rejected as *the* answer because Go-and-friends support is a stated goal,
  and the process Runner covers Deno as just another runtime anyway.
- **Everything-WASM from day one.** Purest capability model, hot-swap without process
  churn. Rejected for v1: wasip1 friction (no sockets, single-threaded Go) taxes every
  component to secure a system whose boundary is the container anyway.
- **ttyd / wetty for terminals.** Faster to bolt on, but session persistence,
  per-component cwd, and auth integration want the PTY loop in buxond;
  `creack/pty` + xterm.js is ~200 lines.
- **Shadow-DOM-only rendering (no iframes).** Seamless layout, but breaks
  `index.html` semantics (script execution, relative URLs, per-document import maps)
  and forces an HTML rewriter into the core. The two-tier scheme keeps seamlessness
  available where it's earned (inline `element.js` mode) without the rewriter.
- **Central component registry with UUIDs.** Rejected: path-as-identity keeps `mv`,
  `cp`, and git as the management UI, which is the whole spirit of the project.

## 10. Build order

1. **Walking skeleton** — buxond: static serving of `/c/<id>/`, `<bx-frame>` (iframe +
   the 7×7 button), `<bx-terminal>` + PTY over WS with cwd. *Already usable as a
   "directory desktop."*
2. **Backends** — process Runner (Go rebuild-on-change first, then node/python/cgi),
   `/api/` routing, unix-socket blue/green swap, error overlay, live reload.
3. **Structure** — manifests, scopes, `deps/` symlink materialization, generated
   `go.work`, root-component self-hosting.
4. **Resources** — broker: sqlite + blob + kv + bus; grants table; service-API
   identity headers; frontend bus bridge.
5. **Later** — inline `element.js` mode, per-terminal mount namespaces, wazero runtime,
   subdomain-per-scope, multi-user.
