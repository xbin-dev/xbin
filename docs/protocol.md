# Protocol reference

Every xbind endpoint, header, and wire format. This is the contract that
`bx`, the core web elements, and the SDKs are built on — anything here is
fair game for your own tooling.

## Authentication

Every route except `/healthz` and `/login` requires a principal
([auth.md](/docs/auth.md)):

| Mechanism | Sent as | Principal |
|---|---|---|
| Owner cookie | `xbin_session` (HttpOnly, Lax; set by `/login?token=…`) | owner |
| Owner/instance bearer | `Authorization: Bearer <token>` | owner, or the element the instance token belongs to |
| Terminal bearer | `Authorization: Bearer <token>` (`$XBIN_TOKEN` in a terminal) | the tile the terminal is opened on (element principal; per-session, revoked at session end) |
| Frame token | `X-XBin-Frame-Token` header, or `?frame=` on WS URLs | element frontend (requires the owner cookie too) |

The gateway unix socket (`$XBIN_GATEWAY`, `.xbin/run/gateway.sock`) serves
this same API; element backends use it with their instance bearer token.

Identity headers **injected by xbind** on proxied component requests
(inbound values are stripped — receiving them means they're verified):

```
X-XBin-From: owner | <component-path> | xbin/cron
X-XBin-Role: <role granted on the callee>
```

## HTTP routes

### Core

```
GET  /healthz                    200 "ok", unauthenticated (liveness)
GET  /login                      login page; ?token=<root> sets the admin cookie
POST /login                      {username,password} form → session cookie (throttled)
POST /logout                     revoke the session
GET  /                           redirect /c/root/
GET  /c/<component-path>/[file]  component static files; HTML gets the
                                 <head> injection (import map, component
                                 meta, frame token, xbin-client.js) unless
                                 manifest inject:false. Cache-Control: no-store.
GET  /vendor/<file>              core elements + vendored libs (lit, xterm…)
GET  /docs/<file>.md             these docs (HTML viewer for browsers; ?raw=1
                                 or non-HTML Accept for plain markdown)
ANY  /api/<component-path>/<p>   → component backend (see below)
ANY  /api/xbin/<p>              → xbind's own API (below)
```

`/api/<component>` resolution is longest-prefix over registered components;
the remainder is the backend path. Responses stream (SSE/chunked flush
immediately) and WebSocket upgrades pass through; a `?frame=` query
credential is accepted for browser WS attribution and is consumed by xbind
(stripped before forwarding). Backends with active streams are exempt from
idle reaping; streams still end at the callee's blue/green drain (D8).
Errors are JSON:
`{"error": "...", "docs": "/docs/...", "detail": "compiler output"?}` —
404 unknown component, 403 no grant, 502 build/backend failure (build
failures carry compiler output in `detail`).

### xbind API (`/api/xbin/…`)

```
GET    /status                     admin. terminals, component count, host
                                   {cpuBusy,cpuTotal,memTotal,memAvail,
                                   diskTotal,diskFree}, traffic
                                   {reqs,bytesOut,uptimeSec} (cumulative —
                                   delta two polls for rates), and version (the
                                   running xbind build commit)
GET    /backends                   admin. per-component backend state
GET    /runtime                    admin. full runtime visibility →
                                   {host:{version,kernel,pid,uid,numCPU,goroutines,
                                   heapMB,uptimeSec,isolate,rootfs,scopeUids,
                                   protections:{seccomp,landlock,landlockAbi}},
                                   backends:[{path,runtime,state,isolated,pid,gen,
                                   uptimeSec,restarts,activeConns,rssKb,threads,fds,
                                   cpuSec,namespaces:{<ns>:{id,isolated}},egress:[…],
                                   cgroup:{memCurrent,memMax,cpuUsec,pidsCurrent},
                                   activity:{allowed,denied,active,txBytes,rxBytes,
                                   recent:[{proto,dst,port,allowed,txBytes,rxBytes,
                                   start,end}]}}], resources:[{id,type,size,detail}]}
                                   {state: idle|building|healthy|failed, gen, error?}
GET    /tile-status?component=<p>  self or admin. one tile's runtime metrics —
                                   backend {state,gen,cpuSec,cgroup:{mem,pids},
                                   rssKb,fds,activeConns,egress}, disk {usage,
                                   quota,blocked}, alerts[]. Readable from that
                                   tile's terminal (tile-scoped token). `bx
                                   status` renders it.
GET    /auth-overview              admin. components(+roles/uses/vault), grants,
                                   pending, counts — powers the admin console
GET    /vaults                     admin. [{component, keys}] across all vaults
GET    /resources                  admin. declared resources [{id,scope,name,type}]
GET    /components                 any. [{path, scope, runtime, hasIndex, roles, uses, deps, manifestError}]
GET    /components/<path>          any. {component, apiDoc: <API.md text>}
GET    /frame-token?component=<p>  a principal that may use the tile. {token}

GET    /alerts                    any. workspace health {alerts:[{level,kind,
                                   tile?,message,system}]} — disk quota / low
                                   disk / cgroup at-limit; system alerts to all,
                                   tile alerts to admins + that tile's users
GET    /whoami                    any. caller identity + permissions; for
                                   users also orgs:[{id,name,admin,teams}]
                                   (the self-service membership view)
GET    /openapi.json              any. OpenAPI 3.1 spec of this built-in API,
                                   incl. the RBAC capability per endpoint
                                   (x-xbin-capability). Rendered by the API-docs
                                   tile; importable into Swagger UI / Postman.

GET    /prefs                     the caller's per-(user×tile) prefs object
GET    /prefs/<key>               one pref value (arbitrary JSON) | 404
PUT    /prefs/<key>               set it (body = JSON value)
DELETE /prefs/<key>               remove it
                                  (each principal reads/writes only its own
                                   bucket; the shell stores layout here)
GET    /users                     admin or xbin:users. [{id,name,role,
                                   tiles:{path:level}, canCreate, termApi,
                                   termNet}] — levels read|write|terminal
                                   (docs/auth.md, D16)
POST   /users                     admin/xbin:users. create {id,name,role,
                                   tiles:{path:level}, canCreate?, termApi?,
                                   termNet?, password}
                                   (id: [a-z0-9._-], immutable; password ≥ 8;
                                   the legacy body — tiles array + terminal
                                   bool — is still accepted and migrated)
PATCH  /users/<id>                admin/xbin:users. update — present fields
                                   overlay (+password reset)
DELETE /users/<id>                admin/xbin:users. remove (revokes sessions)
GET    /auth-settings             admin/xbin:users. {tokenLoginDisabled,
                                   hasAdminUser, canDisable} — owner-token
                                   browser-login state (docs/auth.md)
POST   /auth-rotate-token         admin. Rotate the owner token: rewrites
                                   .xbin/token, old token dies immediately
                                   (bearer + cookie). → {token} (shown once).
PATCH  /auth-settings             admin/xbin:users. {tokenLoginDisabled:bool};
                                   disabling requires a signed-in admin user
                                   (Bearer owner token unaffected)

GET    /orgs                      management view (docs/auth.md, orgs&teams):
                                   admin/xbin:users → all orgs; a signed-in
                                   org admin → their orgs; others → [].
                                   {orgs:[{id,name,admins,members,
                                   basePermission,policy,teams:[…]}]}
POST   /orgs                      admin/xbin:users. create {id, name?,
                                   admins?, members?, basePermission?}
                                   (id: [a-z0-9._-], immutable; o/u/workspace
                                   reserved)
PATCH  /orgs/<org>                admin/xbin:users, or that org's admin.
                                   overlay {name?, admins?, members?,
                                   basePermission?} — members removed here
                                   leave the org's teams too
DELETE /orgs/<org>                admin/xbin:users only
POST   /orgs/<org>/teams          org-manage (as PATCH /orgs/<org>). create
                                   {id, name?, members?, tiles?, canCreate?,
                                   newTiles?} — members must be org members;
                                   termApi/termNet additionally need a
                                   workspace admin
PATCH  /orgs/<org>/teams/<team>   org-manage. overlay (same fields/rules)
DELETE /orgs/<org>/teams/<team>   org-manage
GET    /access?tile=<path>        admin/xbin:users, or the tile's org admin.
                                   the tile's resolved ACL: {tile, org?,
                                   orgAdmins?, entries:[{kind:user|team|org,
                                   id, level, source: exact|pattern:<pat>|
                                   base}]}
PUT    /access                    same gate. set/clear one EXACT entry:
                                   {tile, kind:user|team, id, level:
                                   read|write|terminal|""} — team entries
                                   only on the team's own org's tiles
GET    /policy                    admin/xbin:users. workspace policy-ceiling
                                   rows {policy:[{tiles,deny?,mayCall?}]}
                                   (deny kinds net|gpu|xbin-caps; docs/auth.md)
PUT    /policy                    admin/xbin:users. replace the rows
GET    /orgs/<org>/policy         admin/xbin:users. that org's rows
PUT    /orgs/<org>/policy         admin/xbin:users. replace them

POST   /create                     owner/admin, a user whose canCreate
                                   covers the path (creating auto-grants
                                   them terminal on it — docs/auth.md), or
                                   an element granted target "xbin" at role
                                   writer (workspace management). body
                                   {path, runtime?, title?, expose?, team?} →
                                   {path, files, team?, teamLevel?}. Same
                                   scaffolder as `bx new`; never overwrites.
                                   team: "<org>/<team>" creates IN that team
                                   (path must be in the org; caller must be a
                                   team member or org/workspace admin; the
                                   team is auto-granted its newTiles level).
                                   Paths: the segments `o`/`u` are reserved —
                                   `o` only as the org marker (o/<org>/… or
                                   <dir>/o/<org>/…) naming an existing org;
                                   applies to clone/imports too.
POST   /clone                      xbin:writer (as /create). body {from, to}
                                   → {path, from, rewritten, pendingGrants}.
                                   Forks a component: copies it (git history
                                   included), rewrites old-path references
                                   across its files, registers it fresh.
                                   Secrets/resource data are NOT copied;
                                   unresolvable uses reject the clone.
GET    /builtins                   any. optional tile catalog
                                   [{name,title,description,defaultPath,installed}]
POST   /builtins/import            xbin:writer (as /create). body {name, path?}
                                   → {path, files, pendingGrants} — installs an
                                   embedded tile (plans/tile-sharing.md).
GET    /builtins/updates            any. builtins (scaffold + imported tiles) with
                                   a newer embedded version. [{id,installPath,
                                   fromVersion,toVersion,adopted,files:[{path,
                                   status}],clean,conflicts}] (plans/builtin-updates.md)
POST   /builtins/update             xbin:writer. body {id, mode:
                                   replace|merge|pin|unpin} → {files}. replace
                                   overwrites, merge 3-way-merges (git merge-file);
                                   both re-record provenance. Never touches template
                                   instances.
GET    /templates                   any. template blueprints (builtin ∪ workspace).
                                   [{id,source,title,description,defaultName}]
POST   /templates/new               xbin:writer. body {source, path?} → {path,
                                   files, pendingGrants} — instantiates a template
                                   into a named copy (plans/templates.md). A
                                   builtin-template instance also gets a read-only
                                   `template` git remote (below).
GET    /templates/{repo}/{rest...}  authenticated. Read-only dumb-HTTP git server for a
                                   builtin template's source repo, e.g.
                                   /templates/agent.git/info/refs. Each instance
                                   has it as its `template` remote, so a builder
                                   pulls upstream fixes: git fetch template &&
                                   git merge template/main.

GET    /code/tree                  admin OR code[:<component>]. ?component=<path> → {component, files:
                                   [{path,size}]} — a component's files.
GET    /code/file                  admin OR code[:<component>]. ?component=<path>&file=<rel> →
                                   {path, content|binary|truncated}.
GET    /git/log                    admin OR code[:<component>]. ?component=<path>&limit=N → {repo,
                                   commits:[{hash,short,author,date,subject}],
                                   remote}. From the component's OWN repo (each
                                   component is its own git repo); remote = origin.
GET    /git/diff                   admin OR code[:<component>]. ?component=<path>&rev=<hash> → {repo,
                                   diff}. rev empty = uncommitted changes vs HEAD.
GET    /git/remote-info            xbin:writer. ?url=<git-url> → {defaultBranch,
                                   tags:[…] (newest first), remote}. git ls-remote
                                   on a URL to preview versions before install.
POST   /git/import                 xbin:writer. body {url, path?, ref?} — clone a
                                   component in from a git remote (GitHub/GitLab/
                                   any git URL); path defaults to apps/<repo>, ref
                                   = a tag/branch. Its origin remote is kept (so
                                   it's updatable). → {path, remote, ref,
                                   pendingGrants}. Rejects local/file:// URLs and
                                   repos with no xbin.json/index.html.

GET    /grants                     admin. {grants: [{from,target,role}], pending: […]}
POST   /grants                     admin. body {from,target,role} — approve/add.
                                   Approving a res:* / gpu:* grant restarts the
                                   caller's backend (that env/devices are captured
                                   at spawn) so it takes effect at once.
DELETE /grants                     admin. body {from,target,role} — revoke

GET    /bindings                   admin. Typed interface wiring (see
                                   plans/interfaces.md; manifest fields in
                                   docs/elements.md).
                                   {bindings: {comp: {slot: provider}},
                                    components: [{component, interfaces, provides}],
                                    pending: [{component, slot, kind, service,
                                              options: [{id, label}]}]}. `pending`
                                   is the unbound slots + candidate providers —
                                   the bind-on-install prompt.
POST   /bindings                   admin. body {component, slot, provider} or
                                   {component, slot, providers:[…]} (the full
                                   set for a multi:true http slot). Refs are
                                   provider[#instance]; an instances-provide
                                   binds to a specific instance only. — bind
                                   a requested interface to a provider (a builtin
                                   id like internet/host/lan:<cidr>, or a tile
                                   path). Owner-only (agents can't self-bind).
                                   Restarts the component (+ a net provider whose
                                   roster changed) so wiring takes effect at once.
DELETE /bindings                   admin. body {component, slot} — clear a binding
PUT    /iface-instances            self or admin. body {component?, instances:
                                   {"<id>": "/provider-relative/prefix"}} — a
                                   provider with provides {instances:true}
                                   registers its runtime instances (replaces
                                   the map; bound requesters re-wire). Bind as
                                   provider#id. Paths are PROVIDER-RELATIVE
                                   ("/m/1"): xbind injects /api/<provider><path>
                                   into consumers. Workspace-absolute paths
                                   ("/api/<self>/…") are rejected 400 — they
                                   would double the prefix, and they bake the
                                   install path into persisted state (stale
                                   after a rename/clone). Trailing "/" is
                                   normalized away.

POST   /lifecycle                  admin. body {component, state} — component
                                   lifecycle (plans/lifecycle.md). state:
                                   enabled | disabled | offloaded | offloaded-full.
                                   A non-enabled backend is not spawned (the proxy
                                   returns 409 + an X-XBin-Lifecycle header);
                                   disabling stops a running backend now. Offload
                                   archives then frees local bytes (data, or +
                                   source/term-env for -full); enabling an
                                   offloaded component restores it. State is in the
                                   overview's component list (state field).

POST   /backup                     admin. body {component} — build a self-
                                   describing tar (source + scope data + terminal
                                   env; NOT vault/env-layer) and stream it to the
                                   component's bound @archive provider. {ok, version}
GET    /backups?component=…         admin. the archiver's version list passed
                                   through: {versions:[{version,time,size}]}
POST   /restore                    admin. body {component, version?, file?}.
                                   No file → restore the whole version (stops the
                                   component, replaces its data/source from the
                                   archive; version defaults to latest). With file
                                   → stream one member back (recover without a full
                                   rollback). Restore is fully archive-driven — no
                                   local metadata needed (plans/lifecycle.md).
                                   The archiver is chosen by the @archive binding:
                                   bindings["<comp>"] override, else bindings["*"]
                                   default (set via POST /bindings).
GET    /backup-schedule            admin. {schedules:[{component,schedule,retention}]}
POST   /backup-schedule            admin. body {component, schedule, retention} —
                                   owner-scheduled backup on the cron engine;
                                   retention prunes to N newest versions per run.
DELETE /backup-schedule?component= admin. remove a component's schedule

GET    /vault-status              admin. {initialized, sealed, mode, insecure}
                                   mode: unsealed|sealed|unconfigured|plaintext
POST   /vault-rekey               admin. body {current, new} — change the
                                   passphrase (re-wraps the data key; needs
                                   unsealed + the current passphrase)
POST   /vault-unseal              admin. body {passphrase} — unseal (or init
                                   the barrier on first use). {created}
POST   /vault-seal                admin. drop the key from memory
GET    /vault/<component>          self or admin. {keys: […]} — list keys (admin
                                   may list any vault)
GET    /vault/<component>/<key>    SELF ONLY. {value} — a secret's value is
                                   readable only by the owning element; admin
                                   gets 403 (it can list + set, not read)
PUT    /vault/<component>/<key>    self or admin. body {value}
DELETE /vault/<component>/<key>    self or admin.
                                   (all vault get/set → 503 when sealed)

GET    /kv/res:<scope>/<name>/?prefix=   reader. {keys}
GET    /kv/res:<scope>/<name>/<key>      reader. raw bytes
PUT    /kv/res:<scope>/<name>/<key>      writer. body = value (≤1 MiB). 507 if
                                         the scope is over its disk quota
DELETE /kv/res:<scope>/<name>/<key>      writer.

GET    /blob/res:<scope>/<name>/[path]   reader. file bytes | {entries} for dirs
PUT    /blob/res:<scope>/<name>/<path>   writer. body = content (≤256 MiB)
DELETE /blob/res:<scope>/<name>/<path>   writer.

POST   /bus/publish                      writer on the resource.
                                         body {resource, topic, data?}

GET    /cron/jobs                        own jobs (admin: all). {jobs}
PUT    /cron/jobs                        writer on the cron resource.
                                         body {name, resource, schedule, path, role?, component?¹}
DELETE /cron/jobs/<name>[?component=]    element: own; admin: any.
```

¹ `component` is owner-only; elements always schedule themselves.

## WebSockets

### `/ws/term` — terminals (admins + users with a terminal-level tile)

Connect with `?cwd=<component-path>` (new session) or `?session=<id>`
(reattach; scrollback replays first). A session may only be opened on a tile
where the caller's access level is **terminal** (docs/auth.md), mounts its
creator's `$HOME`, and carries a per-session `XBIN_TOKEN` scoped to that tile
(plans/terminal-tokens.md). Under `--isolate` the workspace mounts read-only
(all tiles' source — for a non-admin, minus tiles below their read level,
which are masked out) with `.xbin/`, `data/`, and other users' `homes/`
**masked out** (docs/isolation.md), so the terminal can't read the owner
token or resource state. **The root terminal (no cwd) is disabled** — 403 for
everyone. Reattach/kill of another user's session: admins only.

For a **non-admin**, the query params below are clamped rather than honored
(docs/isolation.md): `api` is forced to `0` without the `termApi` grant,
`net` is forced to `none` without `termNet`, and `net=host` is admin-only
always. The session still opens; the `session` control frame reports the
effective scope. New-session query params (all optional):

- `?api=0` — mint **no** terminal token: the shell sees code but every tile/xbin
  API call is unauthorized (default `1`).
- `?net=<scope>` (default `internet`):

- `internet` — own network namespace with an **internet-only egress relay**
  (`net:internet`: public addresses only, no host interfaces visible). TCP, UDP,
  and ICMP echo (`ping`) are forwarded under the policy; `traceroute` needs the
  `host` scope. xbind stays reachable at `$XBIN_URL` via the relay's gateway
  host-forward, so `bx`/`curl` work.
- `host` — share the host network (LAN + host services reachable, host
  interfaces visible). The escape hatch.
- `none` — an isolated namespace with no egress (xbind unreachable).

A new session also takes `?gpu=<none|all|index|uuid>` (default `none`) to bind
host NVIDIA GPU(s) into the terminal's dev sandbox (owner plane; no grant
needed). Enumerate host GPUs at `GET /api/xbin/gpus` (admin).

The scope is fixed at spawn; switching net or GPU restarts the session (the UI
ends the old one and opens a new WS).

- **Binary frames** both directions: raw PTY bytes.
- **Text frames**: JSON control.
  - server → client: `{"op":"session","id":"…","net":"internet","baseOutdated":false}`
    (first message; `baseOutdated:true` ⇒ this terminal's persistent layer was
    built on an older base image — reset it via `/ws/term/env` to rebuild on the
    current base), `{"op":"exit"}` (shell ended)
  - client → server: `{"op":"resize","cols":120,"rows":32}`

`DELETE /ws/term?session=<id>` ends a session immediately (creator or admin;
used by the UI to restart under a new scope); `204` on success, `404` unknown.

`DELETE /ws/term/env?cwd=<component-path>` (owner only) wipes that component's
**persistent terminal layer** (installed packages / system changes) back to the
base rootfs, killing any live session on it first; `204` on success. Each
component's terminal has its own persistent overlay layer (`.xbin/term/<key>/`)
so system-level changes survive across sessions — a resettable dev sandbox
(`plans/component-env.md`). Workspace files and `$HOME` persist independently.

Sessions survive disconnects; idle unattached sessions are reaped after 24 h;
xbind restart kills them (run `tmux` inside if you care).

### `/ws/events` — event stream

Auth: cookie (owner) or `?frame=<frame-token>` (element). JSON text frames:

```jsonc
{"type":"reload","component":"apps/thing"}          // source changed
{"type":"build-start","component":"apps/thing"}
{"type":"build-error","component":"apps/thing","text":"compiler output"}
{"type":"build-ok","component":"apps/thing"}
{"type":"grants"}                                    // grant table changed
{"type":"bus","topic":"res:<scope>/<name>/<topic>","data":…}
```

Non-bus events go to every subscriber. `bus` events are delivered only to
the owner and to elements holding a reader grant on the resource. Slow
consumers are disconnected; reconnect with backoff (the bundled clients do).

## Tile ↔ shell messaging (window.postMessage)

Tiles are same-origin iframes; a small `postMessage` protocol between a tile's
`xbin-client.js` and its embedding `<bx-frame>` (relayed to `<bx-shell>`) backs
the height, dialog, and pop-out-window features. Every message is
`{ type: "xbin:…", … }` and is only honored from the frame's *own* iframe
(`event.source` match) — the sender window is the verified component identity.

```
tile → frame   xbin:resize   {component, height}     auto-height (informational)
tile → frame   xbin:dialog   {id, spec}              request a shell modal
tile → frame   xbin:window   {id, spec}              request a pop-out window
tile → frame   xbin:window-close {id}                close a window it opened
frame → tile   xbin:reply    {id, result}            dialog result / window closed
```

`<bx-frame>` re-dispatches dialog/window requests as a `bx-spawn` DOM event
carrying the **verified** component (never a tile-supplied one) plus a `reply`
closure; `<bx-shell>` renders the dialog (`<bx-dialog>`, from data) or window
(a nested `<bx-frame>` on a sub-path) and calls `reply` on resolve/close.
Window sub-paths are traversal-stripped; framing still runs the normal
frame-token / tile-access checks; dialogs are rendered with the verified
originating component shown, and each tile is capped to one dialog + a few
windows. See docs/elements.md §Dialogs & windows and the `xbin.dialog` /
`xbin.window` APIs in docs/sdk.md.

## Backend contract (what the runner promises your process)

- Listen on `$XBIN_SOCKET` (unix, HTTP/1.1; WebSocket upgrades pass
  through; streaming/SSE works).
- You're started lazily, health-checked by socket-connect within 5 s,
  swapped blue/green on change, SIGTERMed with a 30 s drain, idle-reaped
  after ~30 min, and crash-loop-broken after 3 fast exits.
- stdout/stderr → `.xbin/log/<compkey>.log` (`bx logs`).
- Env: `XBIN_SOCKET`, `XBIN_COMPONENT`, `XBIN_GATEWAY`, `XBIN_TOKEN`
  (per-generation), `XBIN_RES_*` (grants).

## Filesystem contract

```
<workspace>/
  xbin.json          workspace manifest: schema, importMap, grants, resources
                      (machine-managed on grant changes — comments don't survive)
  */scope.json        scope marker: resources, importMap overrides
  */xbin.json        component manifests (JSONC, yours to edit)
  .xbin/             derived state — safe to delete when xbind is stopped:
    token, secret     owner token, frame-token HMAC key
    run/              unix sockets (short tmp dir + symlink for deep paths)
    log/  build/  cache/  uids.json
  data/               resource state: resources/, vault/, kv.db, cron-jobs.json
                      (backup unit; gitignored)
  homes/<user>/       terminal $HOME per user (dotfiles, persists across
                      upgrades; the root token uses homes/owner)
  vendor-, xbin-, …  reserved top-level names: vendor, data, home, homes, .xbin
```

Component keys in `.xbin` paths: `<path with / → ~, truncated>-<8-hex hash>`
(keeps unix socket paths under the 108-byte limit).
