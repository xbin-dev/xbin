# Protocol reference

Every buxond endpoint, header, and wire format. This is the contract that
`bx`, the core web elements, and the SDKs are built on — anything here is
fair game for your own tooling.

## Authentication

Every route except `/healthz` and `/login` requires a principal
([auth.md](/docs/auth.md)):

| Mechanism | Sent as | Principal |
|---|---|---|
| Owner cookie | `buxon_session` (HttpOnly, Lax; set by `/login?token=…`) | owner |
| Owner/instance bearer | `Authorization: Bearer <token>` | owner, or the element the instance token belongs to |
| Frame token | `X-Buxon-Frame-Token` header, or `?frame=` on WS URLs | element frontend (requires the owner cookie too) |

The gateway unix socket (`$BUXON_GATEWAY`, `.buxon/run/gateway.sock`) serves
this same API; element backends use it with their instance bearer token.

Identity headers **injected by buxond** on proxied component requests
(inbound values are stripped — receiving them means they're verified):

```
X-Buxon-From: owner | <component-path> | buxon/cron
X-Buxon-Role: <role granted on the callee>
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
                                 meta, frame token, buxon-client.js) unless
                                 manifest inject:false. Cache-Control: no-store.
GET  /vendor/<file>              core elements + vendored libs (lit, xterm…)
GET  /docs/<file>.md             these docs (HTML viewer for browsers; ?raw=1
                                 or non-HTML Accept for plain markdown)
ANY  /api/<component-path>/<p>   → component backend (see below)
ANY  /api/buxon/<p>              → buxond's own API (below)
```

`/api/<component>` resolution is longest-prefix over registered components;
the remainder is the backend path. Responses stream (SSE/chunked flush
immediately) and WebSocket upgrades pass through; a `?frame=` query
credential is accepted for browser WS attribution and is consumed by buxond
(stripped before forwarding). Backends with active streams are exempt from
idle reaping; streams still end at the callee's blue/green drain (D8).
Errors are JSON:
`{"error": "...", "docs": "/docs/...", "detail": "compiler output"?}` —
404 unknown component, 403 no grant, 502 build/backend failure (build
failures carry compiler output in `detail`).

### buxond API (`/api/buxon/…`)

```
GET    /status                     admin. terminals, component count
GET    /backends                   admin. per-component backend state
                                   {state: idle|building|healthy|failed, gen, error?}
GET    /auth-overview              admin. components(+roles/uses/vault), grants,
                                   pending, counts — powers the admin console
GET    /vaults                     admin. [{component, keys}] across all vaults
GET    /resources                  admin. declared resources [{id,scope,name,type}]
GET    /components                 any. [{path, scope, runtime, hasIndex, roles, uses, deps, manifestError}]
GET    /components/<path>          any. {component, apiDoc: <API.md text>}
GET    /frame-token?component=<p>  a principal that may use the tile. {token}

GET    /whoami                    any. caller identity + permissions

GET    /prefs                     the caller's per-(user×tile) prefs object
GET    /prefs/<key>               one pref value (arbitrary JSON) | 404
PUT    /prefs/<key>               set it (body = JSON value)
DELETE /prefs/<key>               remove it
                                  (each principal reads/writes only its own
                                   bucket; the shell stores layout here)
GET    /users                     admin or buxon:users. [{id,name,role,tiles,terminal}]
POST   /users                     admin/buxon:users. create {id,name,role,tiles,terminal,password}
PATCH  /users/<id>                admin/buxon:users. update fields (+password reset)
DELETE /users/<id>                admin/buxon:users. remove (revokes sessions)

POST   /create                     owner, or an element granted target
                                   "buxon" at role writer (workspace
                                   management). body {path, runtime?, title?,
                                   expose?} → {path, files}. Same scaffolder
                                   as `bx new`; never overwrites.
GET    /builtins                   any. optional tile catalog
                                   [{name,title,description,defaultPath,installed}]
POST   /builtins/import            buxon:writer (as /create). body {name, path?}
                                   → {path, files, pendingGrants} — installs an
                                   embedded tile (plans/tile-sharing.md).
GET    /builtins/updates            any. builtins (scaffold + imported tiles) with
                                   a newer embedded version. [{id,installPath,
                                   fromVersion,toVersion,adopted,files:[{path,
                                   status}],clean,conflicts}] (plans/builtin-updates.md)
POST   /builtins/update             buxon:writer. body {id, mode:
                                   replace|merge|pin|unpin} → {files}. replace
                                   overwrites, merge 3-way-merges (git merge-file);
                                   both re-record provenance. Never touches template
                                   instances.
GET    /templates                   any. template blueprints (builtin ∪ workspace).
                                   [{id,source,title,description,defaultName}]
POST   /templates/new               buxon:writer. body {source, path?} → {path,
                                   files, pendingGrants} — instantiates a template
                                   into a named copy (plans/templates.md).

GET    /grants                     admin. {grants: [{from,target,role}], pending: […]}
POST   /grants                     admin. body {from,target,role} — approve/add
DELETE /grants                     admin. body {from,target,role} — revoke

GET    /vault-status              admin. {initialized, sealed, insecure}
POST   /vault-unseal              admin. body {passphrase} — unseal (or init
                                   the barrier on first use). {created}
POST   /vault-seal                admin. drop the key from memory
GET    /vault/<component>          self or admin. {keys: […]}
GET    /vault/<component>/<key>    self or admin. {value}
PUT    /vault/<component>/<key>    self or admin. body {value}
DELETE /vault/<component>/<key>    self or admin.
                                   (all vault get/set → 503 when sealed)

GET    /kv/res:<scope>/<name>/?prefix=   reader. {keys}
GET    /kv/res:<scope>/<name>/<key>      reader. raw bytes
PUT    /kv/res:<scope>/<name>/<key>      writer. body = value (≤1 MiB)
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

### `/ws/term` — terminals (owner only)

Connect with `?cwd=<component-path>` (new session) or `?session=<id>`
(reattach; scrollback replays first).

- **Binary frames** both directions: raw PTY bytes.
- **Text frames**: JSON control.
  - server → client: `{"op":"session","id":"…"}` (first message),
    `{"op":"exit"}` (shell ended)
  - client → server: `{"op":"resize","cols":120,"rows":32}`

Sessions survive disconnects; idle unattached sessions are reaped after 24 h;
buxond restart kills them (run `tmux` inside if you care).

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

## Backend contract (what the runner promises your process)

- Listen on `$BUXON_SOCKET` (unix, HTTP/1.1; WebSocket upgrades pass
  through; streaming/SSE works).
- You're started lazily, health-checked by socket-connect within 5 s,
  swapped blue/green on change, SIGTERMed with a 30 s drain, idle-reaped
  after ~30 min, and crash-loop-broken after 3 fast exits.
- stdout/stderr → `.buxon/log/<compkey>.log` (`bx logs`).
- Env: `BUXON_SOCKET`, `BUXON_COMPONENT`, `BUXON_GATEWAY`, `BUXON_TOKEN`
  (per-generation), `BUXON_RES_*` (grants).

## Filesystem contract

```
<workspace>/
  buxon.json          workspace manifest: schema, importMap, grants, resources
                      (machine-managed on grant changes — comments don't survive)
  */scope.json        scope marker: resources, importMap overrides
  */buxon.json        component manifests (JSONC, yours to edit)
  .buxon/             derived state — safe to delete when buxond is stopped:
    token, secret     owner token, frame-token HMAC key
    run/              unix sockets (short tmp dir + symlink for deep paths)
    log/  build/  cache/  uids.json
  data/               resource state: resources/, vault/, kv.db, cron-jobs.json
                      (backup unit; gitignored)
  home/               terminal $HOME (dotfiles, persists across upgrades)
  vendor-, buxon-, …  reserved top-level names: vendor, data, home, .buxon
```

Component keys in `.buxon` paths: `<path with / → ~, truncated>-<8-hex hash>`
(keeps unix socket paths under the 108-byte limit).
