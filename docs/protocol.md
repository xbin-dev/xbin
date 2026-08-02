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
X-XBin-From: owner | <component-path> | xbin/cron | ingress
X-XBin-Role: <role granted on the callee>
X-XBin-User: <user id>                   (the signed-in HUMAN driving the
                                          call, when there is one — direct,
                                          or riding the tile's frontend/
                                          terminal; absent for automation
                                          and the bootstrap token, D29)
X-XBin-User-Level: read|write|terminal   (that user's level on the callee;
                                          set with X-XBin-User)
X-XBin-Ingress-Host: <public hostname>   (ingress traffic only)
```

`From: ingress` is anonymous PUBLIC traffic through a published endpoint
(docs/ingress.md): no role, confined to the manifest's declared public
paths, and structurally unable to reach `/api/xbin/*` or any other tile. It
enters on the separate ingress listeners — never the authenticated routes
below — and no component can be named `ingress` (reserved), so the value is
always trustworthy.

## HTTP routes

### Core

```
GET  /healthz                    200 "ok", unauthenticated (liveness)
GET  /login                      login page; ?token=<root> sets the admin cookie
POST /login                      {username,password} form → session cookie (throttled)
GET  /login?invite=<tok>         invite set-password page (D22; single-use link)
POST /login/invite               {invite,password,password2} form → redeems the
                                 invite (sets the password, consumes the link),
                                 signs the user in (throttled)
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
GET    /logs?component=<p>         admin, the tile itself, or a user with
                                   TERMINAL-level access on it (read/write
                                   users don't — output can carry secrets).
                                   text/plain tail of the backend's captured
                                   stdout/stderr (.xbin/log/<key>.log, all
                                   generations). ?tail=<bytes> (default 64K,
                                   max 1M) sizes it; ?follow=1 keeps streaming
                                   appended bytes (chunked) until the client
                                   goes away. The HTTP twin of `bx logs [-f]`;
                                   the terminal window's read-only logs tab.
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
                                   users also orgs:[{id,name,level,create,
                                   admin}]
                                   (the self-service membership view). On
                                   element principals driven by a signed-in
                                   human, `user` reports the driver, SCOPED
                                   by the tile's trust (docs/auth.md): every
                                   tile gets {id,name}; a tile inside an org
                                   additionally gets that one org's
                                   membership slice; a workspace-management
                                   tile (xbin / xbin:users capability) gets
                                   {admin, orgs:[…]} in full — the element's
                                   own privilege is unchanged either way
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
                                   termNet, disabled?, invitePending?}] —
                                   levels read|write|terminal
                                   (docs/auth.md, D16)
POST   /users                     admin/xbin:users. create a user: {id,
                                   name?, role?, tiles?, canCreate?, termApi?,
                                   termNet?, password?}. WITH password →
                                   ready to sign in; WITHOUT → credential-less
                                   account + a single-use invite link the
                                   admin delivers: {user, invite, inviteUrl:
                                   /login?invite=…, inviteExpires} (72h, D22).
                                   There is NO self-signup — accounts only
                                   come from here. (id: [a-z0-9._-],
                                   immutable; password ≥ 8; the legacy body —
                                   tiles array + terminal bool — is still
                                   accepted and migrated)
POST   /account/password          signed-in users. {current, new} — self-
                                   service rotation; verifies the current
                                   password (D38)
GET    /screens                   signed-in. {default: {tiles}|null, org:
                                   [{id,org,name,edit,tiles,canEdit}]} —
                                   the ws default screen + your orgs'
                                   screens (D37)
PUT    /screens/default           ws-admin. {tiles} — the seed screen new
                                   users start from
PUT    /screens/org               {id?,org,name?,edit?,tiles} — create
                                   (org admin) / edit tiles (per the
                                   screen's edit knob: admins|write|
                                   members) / meta changes (org admin)
DELETE /screens/org               org admin. {id, org}
POST   /users/<id>/invite         admin/xbin:users — or an ORG ADMIN for a
                                   non-admin member of their org
                                   (delegated reset-by-link, D38).
                                   (re)mint an invite link
                                   for an existing user — credential delivery
                                   or reset-by-link; re-minting invalidates
                                   the previous link; the current password
                                   keeps working until redemption. → {invite,
                                   inviteUrl, inviteLink (absolute, from the
                                   request host), inviteExpires}
PATCH  /users/<id>                admin/xbin:users. update — present fields
                                   overlay (+password reset). {disabled:
                                   bool} suspends/restores the account
                                   (D34): login, sessions, tokens and
                                   invite redemption refuse while set;
                                   rows/memberships/ownership stay.
                                   Guarded: not yourself, not the last
                                   enabled admin
DELETE /users/<id>                admin/xbin:users. remove (revokes
                                   sessions) → {ok, orphanedTiles: […]} —
                                   tiles that fell to workspace-owned, so
                                   the handover is explicit
GET    /auth-settings             admin/xbin:users. {tokenLoginDisabled,
                                   hasAdminUser, canDisable} — owner-token
                                   browser-login state (docs/auth.md)
POST   /auth-rotate-token         admin. Rotate the owner token: rewrites
                                   .xbin/token, old token dies immediately
                                   (bearer + cookie). → {token} (shown once).
PATCH  /auth-settings             admin/xbin:users. {tokenLoginDisabled:bool};
                                   disabling requires a signed-in admin user
                                   (Bearer owner token unaffected)

GET    /orgs                      management view (docs/auth.md, ownership):
                                   admin/xbin:users → all orgs; a signed-in
                                   org admin → their orgs. {orgs:[{id,name,
                                   members:[{id,level,create,admin,
                                   suspended?}],tiles,
                                   sets,allow,policy,resolvedAllow,
                                   ownedTiles}]}
POST   /orgs                      admin/xbin:users. create {id, name?}
                                   (id: [a-z0-9._-], immutable; "workspace"
                                   reserved)
PATCH  /orgs/<org>                admin/xbin:users, or that org's admin:
                                   {name?, members?} — a member entry's
                                   suspended:true pauses the membership
                                   (confers nothing, stays listed; D34).
                                   WS-ADMIN ONLY fields:
                                   {sets?, allow?} — delegation is granted
                                   from above (D26/D28); xbin/xbin:* never
                                   valid in allow
DELETE /orgs/<org>                admin/xbin:users; refused while the org
                                   still OWNS tiles (transfer first)
GET    /permission-sets           admin/xbin:users. {sets:{name:{allow,
                                   policy,termApi,termNet}}, attachedTo:
                                   {name:[orgIds]}} (D28)
PUT    /permission-sets/<name>    admin/xbin:users. replace one set (same
                                   allow grammar/floor as org allow)
DELETE /permission-sets/<name>    admin/xbin:users; refused while attached
                                   to any org
GET    /owner?tile=<path>         any principal that can READ the tile.
                                   {tile, owner: "user:<id>"|"org:<id>"|""}
GET    /owner/preview             ?tile=&to= — transfer impact report
                                   (D39, never mutates): {allowed,
                                   callerLevel:{before,after},
                                   deadBindings, deadGrants, planeChanges}
                                   — what dies under the NEW owner's
                                   ceilings and what happens to YOUR level
POST   /owner                     transfer (D24/D39): {tile, to}. GIVE:
                                   ws-admin / the user-owner / an
                                   owning-org admin. RECEIVE into org:X
                                   needs X's Create knob ("transfer into ≡
                                   create in"); user:<self> with the GIVE
                                   right; user:<other> and workspace are
                                   ws-admin acts. Side effects: fully
                                   ceiling-dead binding slots are UNBOUND,
                                   affected backends restart, and the
                                   response carries the executed report
                                   (+unbound)
GET    /access?tile=<path>        ws-admin, the tile's USER-OWNER, or an
                                   owning-org admin. {tile, owner, entries:
                                   [{kind:user|org, id, level, source:
                                   exact|pattern:<pat>}]}
PUT    /access                    same gate (sharing is an ownership right,
                                   D24). set/clear one EXACT entry: {tile,
                                   kind:user|org, id, level:
                                   read|write|terminal|""}; kind user also
                                   takes "none" = explicit EXCLUSION. Exact
                                   user entries are AUTHORITATIVE (D31):
                                   they override org level, shares, pattern
                                   entries and defaults — down as well as up
GET    /access-matrix             admin/xbin:users. users×components
                                   effective levels with provenance:
                                   {users, components, matrix:{user:{tile:
                                   {level, explain:[{level,source}]}}},
                                   owners} — sources: admin | owner |
                                   org-admin:<org> | exact (authoritative,
                                   D31) | org-member:<org> |
                                   org-share:<org>:<pat> | direct:<pat> |
                                   default:<pat>
GET    /access-requests           signed-in users / admin. pending human
                                   access requests (D36), scoped to the
                                   viewer: {requests: [{user, tile, level,
                                   note?, created, mine?, manage?}]} —
                                   mine = you filed it; manage = you may
                                   approve it (the tile's owner, its org's
                                   admins, or ws-admin)
POST   /access-requests           signed-in users. file {tile, level:
                                   read|write|terminal, note?} (≤20
                                   pending each; same-tile refiles
                                   replace; refuses levels you already
                                   hold, tiles you're explicitly excluded
                                   from (exact `none`), and re-files
                                   within 24h of a manager's dismissal). A signed-in human navigating to
                                   an unreadable /c/ tile gets this as a
                                   page (owner named + one-click request)
                                   instead of a bare 403
POST   /access-requests/approve   the tile's manager set. {user, tile,
                                   level?} — writes the exact ACL entry
                                   (level defaults to the requested one)
                                   and removes the request
DELETE /access-requests           {user?, tile} — withdraw your own (no
                                   cooldown); a MANAGER dismissing someone
                                   else's starts the 24h re-file cooldown
GET    /users-directory           admin/xbin:users, or any org admin. the
                                   minimal people list for pickers:
                                   {users:[{id,name}]} — identity only
GET    /policy                    admin/xbin:users. workspace policy-ceiling
                                   rows {policy:[{tiles,deny?,mayCall?}]}
                                   (deny kinds net|gpu|xbin-caps|ingress)
PUT    /policy                    admin/xbin:users. replace the rows
GET    /orgs/<org>/policy         admin/xbin:users, or that org's admins
                                   (read-only — the ceilings their
                                   approvals can trip on). Rows apply to
                                   tiles the org OWNS
PUT    /orgs/<org>/policy         admin/xbin:users. replace them
GET    /defaults                  admin/xbin:users. {defaultTiles:
                                   {pattern: level}} — visibility every
                                   user gets (D27)
PUT    /defaults                  admin/xbin:users. replace the map

POST   /create                     owner/admin, a user whose canCreate
                                   covers the path, or an element granted
                                   target "xbin" at role writer (workspace
                                   management). body {path, runtime?,
                                   title?, expose?, owner?} → {path, files,
                                   owner?}. owner: "org:<id>" creates the
                                   tile OWNED by that org — gated by the
                                   org's Create knob / org admin INSTEAD of
                                   personal canCreate patterns (D25: the
                                   path doesn't encode the org, so the org
                                   knob is the authority; elements still
                                   need the xbin:writer capability).
                                   Default: the human creator becomes
                                   user-owner, admin/automation →
                                   workspace-owned. Same scaffolder as `bx
                                   new`; never overwrites. Clone/imports
                                   assign the same default ownership.
POST   /clone                      same authority as /create (create
                                   patterns work; the deputy clamp applies)
                                   + the human must have READ on `from`
                                   (copying is reading). body {from, to}
                                   → {path, from, rewritten, pendingGrants}.
                                   Forks a component: copies it (git history
                                   included), rewrites old-path references
                                   across its files, registers it fresh.
                                   Secrets/resource data are NOT copied;
                                   unresolvable uses reject the clone.
GET    /builtins                   any. optional tile catalog
                                   [{name,title,description,defaultPath,installed}]
POST   /builtins/import            same authority as /create, checked on
                                   the resolved target (path? or the tile's
                                   defaultPath). body {name, path?}
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
POST   /templates/new               same authority as /create on the
                                   resolved target; a workspace-template
                                   source also needs READ. body {source,
                                   path?} → {path,
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
                                   commits:[{hash,short,author,date,subject,
                                   add,del,files}], remote}. From the component's
                                   OWN repo (each component is its own git repo);
                                   add/del/files = that commit's churn; remote =
                                   origin.
GET    /git/diff                   admin OR code[:<component>]. ?component=<path>&rev=<hash> → {repo,
                                   diff}. rev empty = uncommitted changes vs HEAD.
GET    /git/activity               admin OR code[:<component>]. ?component=<path> → {repo,
                                   remote, upstreamRef, local:[{t,a}],
                                   upstream:[{t,a}]|null}. Author-date (t, unix)
                                   + author (a) timeline of the component's
                                   history and, if a remote-tracking branch
                                   exists, its upstream — for activity charts.
GET    /git/remote-info            xbin:writer. ?url=<git-url> → {defaultBranch,
                                   tags:[…] (newest first), remote}. git ls-remote
                                   on a URL to preview versions before install.
POST   /git/import                 same authority as /create on the
                                   resolved path. body {url, path?, ref?} — clone a
                                   component in from a git remote (GitHub/GitLab/
                                   any git URL); path defaults to apps/<repo>, ref
                                   = a tag/branch. Its origin remote is kept (so
                                   it's updatable). → {path, remote, ref,
                                   pendingGrants}. Rejects local/file:// URLs and
                                   repos with no xbin.json/index.html.

GET    /grants                     admin — full table {grants, pending}.
                                   Any signed-in USER gets a filtered view
                                   (D26/D33): rows their orgs own
                                   (direction consumer), rows targeting
                                   their orgs' property (provider), and
                                   their own writable tiles' rows (mine —
                                   requesters are never blind). Shape:
                                   {grants: [{from,target,role,approvedBy?,
                                   approvedAt?,direction?}], pending:
                                   [{from,target,role,blocked?,approvable?,
                                   direction?,approvers?}], scope:
                                   "org"|"mine"} — blocked names the policy
                                   row that makes a request unapprovable;
                                   approvers hints who could (["org:<id>",
                                   "workspace-admin"], plus
                                   "transfer:org:<id>" when transferring a
                                   USER-owned requesting tile to that org
                                   would put it under its allowance). Elements: admin only
POST   /grants                     admin — any. An org admin may approve on
                                   TWO edges (D26/D33): their org owns the
                                   REQUESTING tile and the target is
                                   intra-org or allowance-covered at the
                                   requested role; or their org owns the
                                   TARGET property (provider consent — no
                                   allowance needed). Ceilings still apply;
                                   xbin/xbin:* never delegable. body
                                   {from,target,role} — approve/add; the
                                   stored row records approvedBy/approvedAt.
                                   Approving a res:* / gpu:* grant restarts the
                                   caller's backend (that env/devices are captured
                                   at spawn) so it takes effect at once.
DELETE /grants                     admin; also both D26/D33 edges (an org
                                   admin may always revoke their org's or
                                   their property's rows — narrowing is
                                   safe). body {from,target,role}

GET    /bindings                   admin; signed-in users get a scoped view
                                   (D26/D33): their orgs' tiles + tiles
                                   they can write + rows whose refs point
                                   at THEIR orgs' provider tiles (the
                                   consumption of their property). Typed
                                   interface wiring (see
                                   plans/interfaces.md; manifest fields in
                                   docs/elements.md).
                                   {bindings: {comp: {slot: provider|{ref,host,
                                    zone,listen}|[…]}},
                                    components: [{component, interfaces, provides}],
                                    pending: [{component, slot, kind, service,
                                              expose?, options: [{id, label}]}]}.
                                   `pending` is the unbound slots + candidate
                                   providers — the bind-on-install prompt;
                                   expose:true rows are unpublished exposed
                                   endpoints (bind = publish, docs/ingress.md).
POST   /bindings                   admin; an org admin within D26 (their
                                   org owns the component; targets
                                   intra-org or allowance-covered — the
                                   iface:svc@tile#instance grammar pins
                                   provider/instance) or D33 (every ref is
                                   a provider THEIR org owns — provider
                                   consent). body {component, slot, provider} or
                                   {component, slot, providers:[…]} (the full
                                   set for a multi:true http slot). Refs are
                                   provider[#instance]; an instances-provide
                                   binds to a specific instance only. — bind
                                   a requested interface to a provider (a builtin
                                   id like internet/host/lan:<cidr>, or a tile
                                   path). Owner-only (agents can't self-bind).
                                   Restarts the component (+ a net provider whose
                                   roster changed) so wiring takes effect at once.
                                   For an EXPOSED endpoint slot (docs/ingress.md)
                                   the body also carries the route config:
                                   {host} or {zone} (http; source "runtime" or a
                                   terminator tile), {listen} (stream; source
                                   "runtime"); binding = publishing. A stream
                                   INTERFACE slot binds "provider#expose-slot".
DELETE /bindings                   admin / owning-org admin (always) /
                                   provider-org admin (withdrawing
                                   service). body {component, slot} — clear a binding
                                   (for an exposed slot: unpublish — the host
                                   404s, a stream port closes + live flows end)
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

PUT    /ingress-hosts              self or admin. body {component?, hosts:[…]}
                                   — a tile with a DELEGATED-ZONE http expose
                                   binding registers the concrete hostnames it
                                   serves (docs/ingress.md; replaces the set,
                                   [] clears). Every host must sit inside one
                                   of the tile's own bound zones (403 outside —
                                   the authority boundary), be a valid bare
                                   hostname, and not collide with any exact-
                                   bound host or another tile's registration
                                   (409). Routes update live; no restart.
GET    /ingress-routes             terminator tiles + admin. {routes: [{host,
                                   component, slot, paths, source, zone?}]} —
                                   the concrete host→tile routes. A tile with
                                   provides {kind:"ingress"} sees the routes
                                   bound THROUGH IT (its proxy/ACME config
                                   input); admins see all; others 403.
GET    /ingress                    admin. The whole ingress picture: {exposes:
                                   [{component, slot, kind, paths|proto+port,
                                   source, host|zone|listen, blocked?}],
                                   routes, ingressHosts, terminators, streams:
                                   [{…, error?, active}], forwards, httpListener:
                                   {listen, tls}} — every exposes slot with its
                                   binding + policy state, live routes, stream
                                   listener health, and the builtin listener.

POST   /lifecycle                  admin, the tile's user-owner, or an
                                   owning-org admin (D24: lifecycle is the
                                   owner's). body {component, state} — component
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
GET    /vault/<component>          backend/terminal self, or admin. {keys:
                                   […]} — list keys (admin
                                   may list any vault)
GET    /vault/<component>/<key>    BACKEND ONLY (instance token, D30).
                                   {value} — a secret's value is readable
                                   only by the owning tile's backend;
                                   admins AND the tile's own terminals get
                                   403 (they list + set, never read)
PUT    /vault/<component>/<key>    backend/terminal self, or admin. body
                                   {value} (write-only management — D30;
                                   frame tokens can't reach the vault API)
DELETE /vault/<component>/<key>    backend/terminal self, or admin.
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

GET    /tile-report                      any signed-in user (read-filtered).
                                         {statuses:{<component>:{level,message,ts}}}
                                         — status tiles reported about themselves,
                                         for the shell's sidebar/tab indicators.
                                         (Distinct from /tile-status = runtime
                                         metrics, above.)
POST   /tile-report                      element (self) or owner (?component=).
                                         body {level:ok|info|warn|error, message?,
                                         transient?}. Sets this tile's persistent,
                                         self-clearing status (ok+empty message
                                         clears it); transient=true fires a one-shot
                                         notification instead. Publishes a `status`
                                         event. SDK xbin.Status/Notify; JS
                                         xbin.status/notify. Cleared on backend
                                         restart. Guidelines: workspace AGENTS.md.

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
{"type":"status","component":"apps/thing",           // a tile reported its condition
 "data":{"level":"error","message":"…","ts":1785…,"transient":false}}
```

Non-bus events go to every subscriber. `bus` events are delivered only to
the owner and to elements holding a reader grant on the resource. `status`
events broadcast like the build events (the shell renders each only for tiles
it shows; the `GET /tile-report` snapshot below is read-filtered per caller).
Slow consumers are disconnected; reconnect with backoff (the bundled clients
do).

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
  (per-generation), `XBIN_RES_*` (grants), `XBIN_IFACE_<slot>_URL`
  (http interfaces). Ingress wiring (docs/ingress.md):
  `XBIN_IFACE_<slot>_ADDR` (bound stream interface — dial it),
  `XBIN_IFACE_<slot>_IP` (lan-ingress leg — the address you own),
  `XBIN_INGRESS_FORWARD_URL` + `XBIN_LAN_INGRESS` (terminator / net-provider
  tiles).

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
