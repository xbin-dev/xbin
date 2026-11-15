# Protocol: grants, bindings and ingress

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the edges between tiles and the world — grants, interface
bindings and instances, ingress hosts and routes.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Grants

```
GET    /auth-overview              admin. components(+roles/uses/vault, vm?:
                                   {memMiB?,vcpus?} when the manifest asks for
                                   a VM), grants, pending, counts — powers the
                                   admin console

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
                                   direction?,approvers?,warning?}], scope:
                                   "org"|"mine"} — blocked names the policy
                                   row that makes a request unapprovable;
                                   warning, on a partitioned tile's request
                                   on another partitioned tile's people's
                                   data, says whose data its code will reach
                                   ("…of every person who can read <x>", or
                                   "…who allows it" with partitionConsent
                                   on; docs/partitions.md);
                                   approvers hints who could (["org:<id>",
                                   "workspace-admin"], plus
                                   "transfer:org:<id>" when transferring a
                                   USER-owned requesting tile to that org
                                   would put it under its allowance, and
                                   "owner" when a personal tile's owner may
                                   approve it themselves, D88). Elements: admin only
POST   /grants                     admin — any. An org admin may approve on
                                   TWO edges (D26/D33): their org owns the
                                   REQUESTING tile and the target is
                                   intra-org or allowance-covered at the
                                   requested role; or their org owns the
                                   TARGET property (provider consent — no
                                   allowance needed). A PERSONAL tile's
                                   owner approves on it (D88): targets they
                                   own themselves or their personal
                                   allowance covers. Ceilings still apply;
                                   xbin/xbin:* and cap:sandboxes never
                                   delegable (D120). body
                                   {from,target,role} — approve/add; the
                                   stored row records approvedBy/approvedAt.
                                   409 for a target that is a sealed xbind
                                   component acting for people (a workspace
                                   agent): no tile calls one.
                                   → {ok, warning?}: warning is the pending
                                   row's approval warning (GET /grants) when
                                   a partitioned tile's grant reaches
                                   another partitioned tile's people's data.
                                   Approving a res:* / gpu:* grant restarts the
                                   caller's backend (that env/devices are captured
                                   at spawn) so it takes effect at once.
                                   Granting xbin or an xbin:* target to a tile
                                   that has non-primary deployments answers
                                   409 "<tile> has non-primary deployments:
                                   remove them before granting it <target>".
                                   Granting xbin, xbin:*, cap:sandboxes,
                                   cap:net-admin or cap:containers to a
                                   tile whose recorded partition mode, or
                                   whose code's request, has user
                                   partitions answers 409 "<tile> is
                                   partitioned (or asks to be): a
                                   partitioned tile can't hold <target> …"
DELETE /grants                     admin; also both D26/D33 edges (an org
                                   admin may always revoke their org's or
                                   their property's rows, a personal
                                   tile's owner that tile's — narrowing is
                                   safe). body {from,target,role}
```

## Interface bindings and instances

```
GET    /bindings                   admin; signed-in users get a scoped view
                                   (D26/D33): their orgs' tiles + tiles
                                   they can write + rows whose refs point
                                   at THEIR orgs' provider tiles (the
                                   consumption of their property). Typed
                                   interface wiring (see
                                   docs/overview/11-interfaces.md; manifest fields in
                                   docs/elements.md).
                                   {bindings: {comp: {slot: provider|{ref,host,
                                    zone,listen}|[…]}},
                                    components: [{component, interfaces, provides,
                                                  partitioned?}],
                                    pending: [{component, slot, kind, service,
                                              expose?, default?, approvable,
                                              options: [{id, label, blocked?}]}],
                                    exposes: [{component, slot, kind, paths?,
                                              proto?, port?, approvable,
                                              routes: [{source, host?, zone?,
                                                        listen?}],
                                              options: [{id, label}]}],
                                    inert: {comp: {slot: reason}},
                                    approvable: {comp: true},
                                    netOptions: {comp: [{id, label, blocked?}]},
                                    sandboxNetOptions: {comp: [{id, label,
                                                                blocked?}]}}.
                                   `exposes` is every exposed endpoint in
                                   view, bound or not, with ALL its routes —
                                   an endpoint takes many (D79) — and the
                                   sources it can bind: the "add a hostname /
                                   port" data (pending lists a slot only while
                                   it has no route).
                                   `pending` is the unbound slots + candidate
                                   providers — the bind-on-install prompt;
                                   expose:true rows are unpublished exposed
                                   endpoints (bind = publish, docs/ingress.md).
                                   `approvable` (the map, and the flag on each
                                   pending row) names the components whose
                                   wiring THIS caller may change: every one
                                   for a workspace admin, the tiles of orgs
                                   they administer for an org admin (D26),
                                   the tiles you own personally (D88 —
                                   within your allowance); a tile you
                                   merely write is listed but not
                                   approvable — UIs show its wiring
                                   read-only. An option `blocked:true` is one
                                   POST /bindings refuses (a net ref outside
                                   the owning org's network sets — for
                                   everyone; outside a personal tile
                                   owner's allowance — for them, labelled
                                   "outside your network allowance"; an
                                   http provider that is partitioned without
                                   a global instance, on an unpartitioned
                                   component's slot — for everyone, 409,
                                   labelled "partitioned, no global
                                   instance"); pickers grey it out.
                                   `partitioned` (components rows) is true
                                   for a tile whose recorded mode has user
                                   partitions: its bindings are global binds
                                   (docs/partitions.md §Bind types).
                                   default:"org" marks an unbound net slot on
                                   an org-owned tile with network sets — it
                                   is already satisfied (D54); binding only
                                   narrows or overrides. default:"personal"
                                   is the same on a personal tile whose
                                   owner has network sets (D88). `inert` lists net
                                   bindings that are stored but resolve to
                                   no egress (a set narrowed, a transfer),
                                   with the reason. Net options carry `org`
                                   (org-owned tiles; label = the live reach),
                                   `personal` (personal tiles; label = the
                                   owner's personal network, D88),
                                   `none` and one `set:<name>` row per
                                   network set (D65) — blocked for anyone
                                   but a workspace admin, for a provider-
                                   only set, and where the org's sets don't
                                   cover it; options the org's sets refuse
                                   say "not covered". `netOptions` maps
                                   every visible net slot's component to
                                   its full option list, bound or not (the
                                   re-bind pickers read it).
                                   `sandboxNetOptions` does the same for
                                   components with sandbox-net slots — a
                                   sandbox manager's network classes
                                   (docs/isolation.md §Network egress):
                                   one list per component (every class of
                                   a tile offers the same), never host or
                                   a provider tile; a set that says host
                                   is blocked. Their pending rows carry no
                                   default (an unbound class is none), and
                                   a class whose binding resolves to no
                                   network is listed in `inert` by slot
POST   /bindings                   admin; an org admin within D26 (their
                                   org owns the component; targets
                                   intra-org or allowance-covered — the
                                   iface:svc@tile#instance grammar pins
                                   provider/instance), D33 (every ref is a
                                   provider THEIR org owns — provider
                                   consent), or D41 (expose host/zone
                                   routed through a terminator tile their
                                   org owns; listen/runtime never); a
                                   personal tile's owner within D88 (unbind,
                                   none/personal, their own provider tiles,
                                   or allowance-covered targets). body {component, slot, provider} or
                                   {component, slot, providers:[…]} (the full
                                   set for a multi:true http slot). Refs are
                                   provider[#instance]; an instances-provide
                                   binds to a specific instance only; 409 for
                                   a provider that is a sealed xbind
                                   component acting for people. — bind
                                   a requested interface to a provider (a builtin
                                   id like internet/host/lan:<cidr>/
                                   internet:<spec>, `org` — the owning org's
                                   network sets, live; org-owned tiles only —
                                   `personal` — the owner's personal network
                                   sets, live; user-owned tiles only (D88) —
                                   `none` — explicitly no egress — or
                                   `set:<name>` — one named network set, a
                                   WORKSPACE-ADMIN act: 403 for org admins,
                                   provider-only sets refused, inside the
                                   org's sets on org-owned tiles (D65); or a
                                   tile path). On an org-owned tile with
                                   network sets every net ref must be inside
                                   the sets (400 names the set; ws-admins
                                   widen the set instead) — org admins bind
                                   inside them without an allowance (D54).
                                   Owner-only (agents can't self-bind).
                                   Restarts the component (+ a net provider whose
                                   roster changed) so wiring takes effect at once.
                                   A sandbox-net slot (a sandbox manager's
                                   network class) takes one of none,
                                   internet, internet:<spec>, lan:<cidr>,
                                   org, personal, set:<name> — host, a
                                   provider tile, a set that says host and
                                   #instance are 400 — under the same
                                   approval rules and ceilings as net; its
                                   (un)binding restarts nothing (the running
                                   sandboxes of that class re-resolve it).
                                   For an EXPOSED endpoint slot (docs/ingress.md)
                                   the body also carries the route config:
                                   {host} or {zone} (http; source "runtime" or a
                                   terminator tile), {listen} (stream; source
                                   "runtime"); binding = publishing. An exposed
                                   endpoint takes MANY routes (D79): add:true
                                   appends this one (another hostname, zone or
                                   host port — each still exclusive across the
                                   workspace, none twice on the slot) instead
                                   of replacing the slot's routes; an org
                                   admin's rights are judged on the route
                                   added. A stream INTERFACE slot binds
                                   "provider#expose-slot".
                                   A new ref of an unpartitioned component's
                                   http slot to a tile whose partition mode
                                   has user partitions and no global
                                   instance is 409: no call of it would
                                   reach that tile (docs/partitions.md); a
                                   ref the slot already holds isn't judged
                                   again. A partitioned
                                   component binds under the same rules as
                                   any other: its bindings are global binds,
                                   seen by every person's partition; binding
                                   its http slot to another partitioned tile
                                   answers {ok, warning} — the approval
                                   warning of POST /grants. Into a tile
                                   that runs reviewed code only (POST
                                   /partitions/reviewed), a provider that
                                   isn't partitioned and whose primary
                                   isn't protected is 409.
DELETE /bindings                   admin / owning-org admin (always) /
                                   provider-org admin (withdrawing
                                   service). body {component, slot} — clear a binding
                                   (for an exposed slot: unpublish — the host
                                   404s, a stream port closes + live flows end).
                                   With provider and/or host|zone|listen:
                                   remove only the matching route(s) — 404 if
                                   none; the provider-side right is judged on
                                   what is removed
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
                                   From a deployment that isn't the primary
                                   (main beside another primary included):
                                   200, today's body plus dormant:true; the
                                   map is stored for that deployment
                                   (protocol.md §Filesystem contract) and never routed —
                                   no grants event, no re-wiring, not even
                                   with its deliveries on. provider#id always
                                   resolves against the provider primary's
                                   map. From a person's partition of a
                                   partitioned tile the same: 200 with
                                   dormant:true, stored for that partition
                                   and never routed (instances are the
                                   global instance's, docs/partitions.md)
```

## Ingress

```
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
                                   Only the primary's hosts route, and the
                                   conflict check meets only other tiles'
                                   primaries' hosts. From a deployment that
                                   isn't the primary (main beside another
                                   primary included): 200, today's body plus
                                   dormant:true; the set is stored for that
                                   deployment, zone-validated but not
                                   conflict-checked, and never routed — no
                                   reconcile, not even with its deliveries on.
                                   A person's partition of a partitioned tile
                                   likewise: 200 dormant:true, stored for it,
                                   never routed
GET    /ingress-routes             terminator tiles + admin. {routes: [{host,
                                   component, slot, paths, source, zone?}]} —
                                   the concrete host→tile routes. A tile with
                                   provides {kind:"ingress"} sees the routes
                                   bound THROUGH IT (its proxy/ACME config
                                   input); admins see all; others 403. A
                                   non-primary deployment of a terminator,
                                   and a person's partition of one, reads
                                   {"routes":[]}.
GET    /ingress                    admin. The whole ingress picture: {exposes:
                                   [{component, slot, kind, paths|proto+port,
                                   source, host|zone|listen, routes:[{source,
                                   host?, zone?, listen?}], blocked?}] (routes =
                                   every route of the slot, D79; the scalars
                                   repeat the first),
                                   routes, ingressHosts, terminators, streams:
                                   [{…, error?, active}], forwards, httpListener:
                                   {listen, tls}} — every exposes slot with its
                                   binding + policy state, live routes, stream
                                   listener health, and the builtin listener.
```
