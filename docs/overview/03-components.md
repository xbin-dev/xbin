# Components & backends

A component is a directory; everything else — views, backends, grants,
terminals — hangs off that one fact. This chapter is the component contract
from the builder's side: what a directory must contain to become a live
element, what the manifest declares, and — in the most depth — what xbind's
runner does with a backend from first request to idle reap. Field-level
reference: [/docs/elements.md](/docs/elements.md).

**Related:** [01-model.md](01-model.md) (the three levels),
[02-workspace.md](02-workspace.md) (where directories live),
[04-frontend.md](04-frontend.md) (views), [08-sandbox.md](08-sandbox.md)
(what a backend can reach), [11-interfaces.md](11-interfaces.md) (typed
slots), [16-extending.md](16-extending.md) (SDK) ·
[/docs/elements.md](/docs/elements.md), [/docs/sdk.md](/docs/sdk.md) ·
plans/implementation.md, plans/component-env.md, plans/templates.md.

## The component contract

A directory is registered as a component when it contains an `xbin.json`
**or** an `index.html`. Nothing else is required:

```
apps/thing/
  xbin.json      # manifest — optional; {} is valid, absence is fine with index.html
  index.html     # view — makes the component renderable in a <bx-frame>
  backend/       # runtime-specific entry (main.go / server.js / server.py / handler)
  deps/          # dependency symlinks, MATERIALIZED BY XBIND (never hand-edit)
  API.md         # the integration contract, if the component exposes roles
  ...anything    # it's just a directory; shells and agents do whatever
```

Consequences of "directory = component":

- **The path is the identity.** `apps/thing` names the component in grants,
  bindings, URLs (`/c/apps/thing/`, `/api/apps/thing/…`), and terminals.
  There is no registry to keep in sync — `mv` is rename, `cp -r` is fork.
- **Registration is a scan, not an act.** xbind watches the tree and
  rescans on change; a freshly `mkdir`'d component is live within the
  debounce window (~300 ms).
- **Manifest errors never take the workspace down.** A component with a
  broken `xbin.json` keeps serving statically; the parse (or `exposes`
  validation) error surfaces in `bx ls`, `bx doctor`, and the status API.
- Reserved names can't be components: top-level `.xbin`, `vendor`, `data`,
  `home`, `homes`, `xbin`, `ingress`, `runtime`; the `o`/`u` path segments
  are positional org markers ([07-users-orgs.md](07-users-orgs.md)).

### Vocabulary: component, element, tile

All three describe the same directory from different angles. **Component**
is the structural unit (this chapter). **Element** is the component as a
runtime *principal* — "element backend", "element frontend" in the auth
model ([05-identity.md](05-identity.md)). **Tile** is the component as the
user sees it — a card in the shell, the unit of human access control
("tile-level RBAC", D16). The admin console is "the admin tile" and a
database component is "a tile" in conversation; in manifests and APIs it is
always a component path.

## The manifest at a glance

Everything is optional; each field is one deliberate hook into a subsystem
(the exhaustive version with all semantics:
[/docs/elements.md](/docs/elements.md)):

```jsonc
{
  "runtime": "go",                    // static (default) | go | node | python | cgi
  "entry": "./backend",               // runtime-specific override; sane defaults

  // Source visibility: these components appear under deps/ as symlinks,
  // so shells, imports and gopls can see (and edit) them. [this chapter]
  "deps": ["lib/ui-kit"],

  // Runtime call/resource grant REQUESTS — inert until approved. [06-authorization.md]
  "uses": [
    { "target": "apps/calendar",        "role": "reader" },
    { "target": "res:apps/thing/db",    "role": "writer" }
  ],

  // The callable surface this component OFFERS: role names + descriptions
  // (mandatory — they render in the grants UI), custom role implications.
  "expose": { "roles": { "reader": "Read thing data" } },

  // Typed capability slots, wired by the OWNER: interfaces are requested
  // (net egress, http services, streams…), provides are offered to other
  // tiles, exposes are offered to the OUTSIDE world. [11..13]
  "interfaces": { "net": { "kind": "net" } },
  "provides":   { "api": { "kind": "http", "service": "thing" } },
  "exposes":    { "web": { "kind": "http", "paths": ["/*"] } },

  // One-time environment build: extra system deps beyond the base rootfs,
  // baked into a cached overlay layer. [§Environment layers]
  "setup": "apt-get install -y --no-install-recommends imagemagick",

  "inject": true,                     // false opts out of the D4 HTML injection [04-frontend.md]
  "template": { "title": "…" }        // marks a BLUEPRINT — never runs [§Templates]
}
```

The split matters: `uses`/`interfaces`/`exposes` are *requests* an agent may
freely author; turning any of them into reality (a grant, a binding, a
published endpoint) is the owner's act. Declaring is cheap and inert.

## Runtimes

| runtime | entry default | backend shape |
|---------|---------------|---------------|
| `static` | — | no backend; files served via `/c/`, HTML gets the D4 injection |
| `go` | `./backend` package | compiled per change (workspace `go.work`, shared build cache; `CGO_ENABLED=0` under `--isolate` so the static binary runs on the sandbox rootfs), then the blue/green dance below |
| `node` | `backend/server.js` | interpreter is the binary — restart-on-change, same swap dance, no compile |
| `python` | `backend/server.py` | as node |
| `cgi` | `backend/handler` (executable) | executed **per request** with CGI/1.1 semantics; no process lifecycle at all — caller identity arrives as `XBIN_FROM`/`XBIN_ROLE` env |

Long-running backends (`go`/`node`/`python`) serve plain HTTP on a unix
socket xbind hands them (`XBIN_SOCKET`); xbind's proxy routes
`ANY /api/<component>/<path>`, strips the prefix, scrubs and injects the
verified caller identity headers ([06-authorization.md](06-authorization.md)).

## The backend lifecycle

The runner supervises one state machine per component:

```
        save/first request
 idle ────────────────────▶ building ──▶ starting ──▶ healthy
                               │             │           │ idle ~30min
                               ▼             ▼           ▼
                          build failed   no health   stopped (lazy restart)
                          (old gen       within 5s
                           keeps serving)
```

### Lazy spawn, single-flight

Nothing runs until the first request (or the first save while previously
running). The proxy calls the runner's `Ensure`, which is **single-flight
per component**: concurrent requests during a build all wait on the same
build and are released together — a save under load never surfaces
connection-refused, and a cold tile's first hit simply pays the build. The
same property powers ingress: an inbound public request or stream connection
wakes a sleeping backend exactly like a browser hit does
([13-ingress.md](13-ingress.md)).

### Blue/green generations (D8)

Every (re)start is a *generation*:

1. **Build** — `go build` (or entry-exists check for interpreters). A failed
   build never touches the running process: the old generation keeps
   serving, the error becomes sticky state (below).
2. **Start** — the new process gets its own per-generation unix socket
   (`g<N>.sock` in the tmpfs run dir) and its own freshly minted **instance
   token** — the credential it uses to call other elements and xbin APIs
   ([05-identity.md](05-identity.md)).
3. **Health** — xbind dials the socket for up to 5 s; a process that never
   accepts is killed and reported as a build error.
4. **Swap** — atomic pointer flip; new requests hit the new generation.
5. **Drain** — the old generation gets SIGTERM and 30 s to finish in-flight
   work (decision D8), then SIGKILL. When it exits, its instance token is
   revoked — an old generation's credential cannot outlive it.

Practical consequences for backend authors: keep state in resources, not
memory (a swap is a new process); handle SIGTERM (the SDK's `xbin.Serve`
does); expect long-lived WS/SSE connections to an old generation to end at
the drain deadline and reconnect.

### Failure handling

- **Sticky build errors**: a failed build (or failed health check) is
  remembered until the next source change — requests get the error
  immediately instead of re-paying a doomed build. The failure is published
  on the event hub, rendered as a red overlay on the tile's frame, and shows
  in `bx status` / `GET /api/xbin/backends`.
- **Crash loops**: a healthy process that exits is transparently restarted
  on the next request — unless it dies fast 3 times, which trips the breaker
  ("crash-looping; fix the code and save to retry").
- **Logs**: each backend's stdout/stderr appends to
  `.xbin/log/<compkey>.log` with a generation header per start. `bx logs -f`
  tails it; the frame's read-only **logs** tab streams it live
  ([04-frontend.md](04-frontend.md)); `GET /api/xbin/logs` is the API.

### Idle reaping and streams

A backend with no requests for ~30 minutes is stopped; the next request
restarts it lazily (typically 100–300 ms warm). Every proxied connection is
*tracked* for its whole lifetime — including SSE and WebSocket streams — so
a backend quietly serving a stream is never reaped mid-connection. Periodic
work belongs in a `cron` resource ([10-resources.md](10-resources.md)), not
a sleeping goroutine that reaping would kill.

## The change pipeline: save → live

```
editor/agent save ──▶ fsnotify (recursive, 300ms debounce,
                      atomic-save dances coalesced)
        │
        ▼
   registry rescan (cheap: directory metadata + manifests)
        │            ├─▶ resource provisioning, ingress reconcile
        │            ├─▶ deps/ symlink + go.work reconciliation
        ▼
   per affected component:
        ├─▶ hub "reload" event  ──▶ the most specific mounted frame reloads
        └─▶ runner.Changed      ──▶ BACKGROUND rebuild starts immediately,
                                    so build errors surface on save,
                                    not on the next request
```

The background rebuild plus blue/green is the whole editing experience: save
a Go file, and about a second later the running backend has been rebuilt,
health-checked, and swapped — or the frame shows the compiler output.

## Dependencies & source visibility

- **`deps/` symlinks.** Manifest `deps` entries are materialized as
  *relative* symlinks under `<component>/deps/` (relocatable workspaces).
  The manifest is the source of truth; xbind reconciles the links on every
  change, cleans up stale ones, and never touches non-symlink files. Missing
  targets are `bx doctor` warnings, not errors.
- **Generated `go.work`.** One workspace file at the root lists every Go
  component module plus the xbin SDK (as a `replace`, resolved via
  `XBIN_SDK_PATH`, default `/opt/xbin/sdk`) — so `go build` in any terminal
  and any gopls sees the whole workspace. The file is marker-guarded: remove
  the generated-by line to take ownership; if a stray `go work use` strips
  the marker *and* modules go missing, xbind reclaims it.
- **The SDK is zero-dependency** by rule — components inherit its module
  graph, so the SDK must never pull anything in.

## Environment layers (`setup`)

The base rootfs is deliberately lean. A component that needs more (a
language runtime, `imagemagick`, a vendor CLI) declares a `setup` shell
script; xbind runs it **once** in a sandbox with `net:internet`, captures
the filesystem delta as an overlay layer under `.xbin/env/`, and stacks that
layer read-only under the backend's root from then on (plans/component-env.md).

The layer is keyed by a content hash of the script plus the base-rootfs
identity: editing the script or upgrading the base image builds a *fresh*
layer (never applied onto stale state); old hashes are garbage-collected.
Setup output lands in the component log. Cost model: slow once, then free —
a backend restart pays nothing for its extra deps.

## Templates: blueprint components

A manifest with a `template` block is a **blueprint**: it never runs (its
backend is not spawnable; the proxy answers "instantiate it first"), isn't
an openable tile, and exists to be copied. Instantiating (Tile Manager →
*New from template*, `bx template new`, `POST /api/xbin/templates/new`)
copies the files to a new path, strips the `template` block, and yields an
independent component — same capability gate as creating any component.
Sources are the embedded builtin catalog and any workspace component
carrying the block. Instances get a read-only `template` git remote pointing
at the blueprint's repo, so upstream fixes can be pulled deliberately
(plans/templates.md, plans/agent-v2.md).

## Creating components

`bx new <path>` and `POST /api/xbin/create` share one scaffolding engine: a
manifest, an `index.html`, the runtime's backend skeleton, and (with
`--expose`) a roles block plus `API.md` — never overwriting existing files.

Creation is an editing-plane action with real authorization
([06-authorization.md](06-authorization.md), [07-users-orgs.md](07-users-orgs.md)):

- **Who may create**: admins anywhere; users wherever their (org/team-
  unioned) create patterns reach; elements holding the workspace-management
  capability (`xbin:writer` — the shipped Tile Manager has it). When a human
  drives such an element, the *human's own* create rights must cover the
  path too — the confused-deputy clamp.
- **Where**: reserved segments are validated (`o/` must name an existing
  org, `u/` is reserved), and nesting is refused both ways — not inside an
  existing component, not above one.
- **Aftercare**: a non-admin creator is auto-granted terminal on their new
  tile ("create ≈ own a namespace", D16). With `team: "<org>/<team>"`, the
  path must sit in that org, the attributed human must be a member (or org
  admin), and the team is auto-granted its configured `newTiles` level on
  the result (D19) — how team workspaces accrete tiles.
