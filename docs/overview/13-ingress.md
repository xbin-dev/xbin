# Ingress: publishing tiles

Egress asks *"what may this tile reach?"*; ingress asks *"who may reach this
tile?"*. xbin answers both with the same machinery: a manifest **declaration**
that is inert on its own, and an **owner-gated binding** that makes it real.
This chapter explains how the ingress subsystem composes with the rest of the
system — the binding graph, the principal model, the backend sandbox, the
egress relay, and the policy ceiling — and why anonymous public traffic can
enter a workspace without weakening any boundary that existed before it.

**Related:** [11-interfaces.md](11-interfaces.md) (the binding graph this
extends) · [12-egress.md](12-egress.md) (the relay this plane reuses) ·
[05-identity.md](05-identity.md) (where the `ingress` principal fits) ·
[06-authorization.md](06-authorization.md) · [07-users-orgs.md](07-users-orgs.md)
(the policy ceiling) · [08-sandbox.md](08-sandbox.md) ·
[15-operations.md](15-operations.md). Builder how-to:
[/docs/ingress.md](/docs/ingress.md); wire reference:
[/docs/protocol.md](/docs/protocol.md); design record: plans/ingress.md,
decisions ING-1..ING-6 in plans/DECISIONS.md.

## The third direction on the binding graph (ING-1)

Before ingress existed, a tile backend was reachable through exactly one door:
xbind's authenticated reverse proxy (`/api/<tile>`), behind owner/user/element
credentials, terminating on the backend's private unix socket. No host port,
no anonymous path. Ingress adds precisely that — deliberately, per endpoint —
as a third direction on the one owner-gated binding spine:

| section      | offers to        | bound by                  | direction |
|--------------|------------------|---------------------------|-----------|
| `interfaces` | (requests)       | owner → a provider        | egress    |
| `provides`   | other **tiles**  | a requester's binding     | intra-workspace |
| `exposes`    | the **outside**  | owner → an ingress source | **ingress** |

```jsonc
// xbin.json — declaring is agent-writable and INERT
"exposes": {
  "web":  { "kind": "http",   "paths": ["/", "/api/public/*"] },
  "game": { "kind": "stream", "proto": "udp", "port": 2456 }
}
```

Two properties carry the whole security story:

- **Default-deny is preserved.** An unexposed endpoint, or an exposed but
  unbound one, is exactly as unreachable as every tile was before the
  subsystem existed. Adding `exposes` to a manifest changes nothing until the
  owner acts — so an agent (or a compromised tile) can never publish itself.
- **The binding carries the route.** This is the one model extension ingress
  required: a binding entry widened from a bare provider ref to
  `{ref, host|zone, listen}` (`registry.BindRef`; bare refs still marshal as
  the plain strings they always were, so every pre-ingress workspace manifest
  parses unchanged). Publishing and routing are therefore a single atomic,
  owner-approved act — there is no separate route table an agent could edit.

Everything downstream — route resolution, host listeners, split-horizon DNS —
is **derived from registry state on demand**. There is no cached routing state
to invalidate or to drift: delete the binding and the route, the host port,
and the hairpin answer all cease to exist at the next evaluation.

## The HTTP(S) plane

### One route table, two terminators (ING-3)

An exposed `http` endpoint is already served on the tile's gateway socket;
publishing it means an **HTTP terminator** routes external requests to it by
hostname. The route table maps `host → exactly one (tile, expose slot)` and is
computed from bindings plus zone registrations. Two terminators satisfy one
contract — deliberately mirroring egress's `internet` builtin vs. provider
tiles:

**The runtime listener** (`xbind --ingress-listen`, off by default) is a
*second* TCP listener inside the daemon. Public unauthenticated traffic never
shares the console listener — the authenticated surface (`/login`, `/c/`,
`/api/xbin`) simply does not exist on the ingress port, so no routing bug can
bridge the two. TLS is bring-your-own (`--ingress-cert/--ingress-key`, re-read
on change so an in-place renewal takes effect at the next handshake) or none —
the intended deployments sit behind Tailscale, a load balancer, or an existing
reverse proxy. ACME never lives in the daemon.

```
            internet
               │  TCP :8080 (BYO TLS or plain)
               ▼
      xbind --ingress-listen          (a separate listener; no console routes)
               │  Host: blog.example.com
               ▼
      route lookup (source = "runtime")   host → one tile, or 404
               │
      public-path allowlist               declared paths only, or 404
               │
      proxy.ForwardIngress                strip X-XBin-*, strip session cookie,
               │                          inject From: ingress + Ingress-Host
               ▼
      target tile's backend unix socket   (spawned on demand if idle)
```

**Terminator tiles** are components that declare `provides {kind:"ingress"}`.
They terminate TLS in *userspace, inside a sandbox*, and hand each decrypted
request back to xbind for the last hop — so the daemon keeps zero certificate
machinery while the tile keeps zero routing authority. The hand-back door is a
per-terminator **forward unix socket** in the tmpfs run dir, reachable from
the tile's netns only through an explicit relay gateway forward
(`10.0.2.2:8642 → unix:igw-<tile>.sock`, injected as
`XBIN_INGRESS_FORWARD_URL`). Possession of that socket *is* the attribution:
requests arriving on it are trusted as "this bound terminator", and route
lookups on it resolve **only hosts whose binding names this tile as the
source**. A terminator cannot serve — or even observe — hosts published
through the runtime listener or a rival terminator.

### The Traefik builtin: the planes interlock

The shipped **Public HTTPS (Traefik)** tile is the worked example, as
`egress-approver` is for egress. Its manifest is the whole story in miniature:

- `exposes web/websecure` — **stream** ports 80/443, published via the **L4
  runtime relay** (below). The tile's own public ports are themselves ordinary
  ingress bindings; the stream plane underpins the HTTP plane. This is why the
  two planes shipped together.
- `provides {public: {kind:"ingress"}}` — other tiles' `http` exposes bind to
  it.
- `interfaces {net}` bound to `internet` — its own egress, for ACME.
- `uses res:…/certs` — the ACME account and issued certificates persist in the
  tile's own resource, never in the daemon.

```
 internet ──TCP :443──▶ xbind L4 relay ──DialIn──▶ traefik sandbox :443
                        (stream expose,            │ TLS ends here
                         bound to runtime)         │ (ACME via its own
                                                   │  net=internet egress)
                        ┌──────────────────────────┘
                        ▼ dials 10.0.2.2:8642 (its relay's gateway forward)
              igw-<traefik>.sock  ──▶  route lookup (source = apps/traefik)
                                       └─▶ path gate ─▶ target tile backend
```

It needs no `cap:net-admin`: it is a userspace proxy, not a router, and the
sandbox init sets `net.ipv4.ip_unprivileged_port_start=0` inside each tile's
*own* netns, so a fully capability-dropped backend can bind :80/:443 there
(the host's port privilege is untouched — see the operations note below).
Traefik's routers are generated from `GET /api/xbin/ingress-routes` — which a
terminator may read only for its own routes — and every router forwards to the
single xbind service, so even a fully compromised terminator config cannot
reroute one tile's host to another: xbind re-resolves the Host and re-applies
the target's path allowlist on every request.

### Hostname authority (ING-2)

An http binding grants a **hostname authority**, not just a hookup:

- **Exact host** (`--host blog.example.com`) — the common case; the owner
  names the one public hostname at bind time and the tile registers nothing.
- **Delegated zone** (`--zone '*.sites.example.com'`) — for multi-site tiles
  (a CMS spawning sites at runtime). The owner draws the boundary once; the
  tile then self-registers concrete hostnames (`PUT /api/xbin/ingress-hosts`,
  replacing its set), following the same bounded-runtime-registration
  precedent as interface `instances`. Registrations are self-scoped (a tile
  registers only its own), must fall **inside one of the tile's own bound
  zones** (label-aligned, at least one extra label, never the apex — so
  `bank.example.com` is structurally unclaimable), and are conflict-checked
  against every exact host and every other tile's registrations.

Exact hosts win over zone registrations at lookup; duplicate exact hosts,
duplicate zones, and exact hosts shadowing another tile's registration are
refused at bind time. A zone-covered but *unregistered* name is answered by
split-horizon DNS (matching what public wildcard DNS would do) but routes
nowhere — the 404 comes from the same place it would come from outside.

## The `ingress` principal (ING-5)

Public traffic needed a caller identity, and it is deliberately **structural
rather than credential**: nothing authenticates, so nothing can be stolen.
The principal is defined entirely by which walls the request passed through:

1. It enters only on ingress listeners — the authenticated mux does not exist
   there, so `/api/xbin/*` and `/c/*` are unreachable by construction.
2. The route table admits it to **exactly one tile** (and only via the source
   its binding names).
3. The **public-path allowlist** (`exposes.….paths`, default-deny) is applied
   after path normalization — dot-segments are resolved *before* matching and
   the backend receives the cleaned path, so `/api/public/../../secret`
   cannot slip past the gate or reach the handler.
4. Every inbound `X-XBin-*` header is stripped, and the workspace session
   cookie is removed (the visitor's own cookies — the tile's app sessions —
   pass through); the `?frame=` credential shape is consumed, never forwarded.
5. xbind injects `X-XBin-From: ingress` and `X-XBin-Ingress-Host: <public
   hostname>`. `ingress` and `runtime` are reserved component names, so no
   tile can ever produce that `From` — a backend may trust it outright
   (SDK: `xbin.Caller(r).Ingress()`).

The contract with the tile is exactly: *external, anonymous, this tile, these
paths*. The tile owns any app-level auth on its public routes; xbind takes no
position on who the visitor is. Errors stay opaque to the outside (plain
502/503, no build output), WebSocket/SSE pass through, and the runtime
listener stamps `X-Forwarded-*` while a terminator's (owner-bound, trusted)
forwarded headers are preserved.

## The TCP/UDP plane (ING-4)

A `stream` expose is least-cursed by construction: the backend opens an
ordinary listener on any port inside its netns — `net.Listen("tcp", ":2456")`,
no SDK, no fd-passing — and declares it. Binding the slot to `runtime` opens a
**host** port (`listen` defaults to `:<port>`), and xbind relays each inbound
connection into the sandbox.

The mechanism is the subsystem's one genuinely novel move: **the egress
relay's gVisor stack doubles as the inbound door**. The stack is already
attached to the tile's TUN ([12-egress.md](12-egress.md)); an inbound
connection is just an *outbound dial from the stack* (`relay.DialIn`), sourced
at the virtual gateway address — to the backend it is an ordinary connection
from its default gateway. No `setns`, no extra file descriptors, no privilege,
and no host firewall or routing mutation (the deferred kernel veth/DNAT path
is a later throughput optimization, not a correctness need).

Consequences that fall out of that choice:

- **Ingress-only tiles get the plumbing without the egress.** A tile with a
  bound stream expose but no `net` binding is spawned with the TUN + relay
  under a **deny-all** outbound policy — and with **no DNS forwarding**, since
  the relay's resolver would otherwise be a free exfiltration channel for a
  tile that was granted no network at all.
- **Inbound connections wake idle backends.** `DialIn` runs behind the same
  single-flight `Ensure` as HTTP requests, so a reaped backend is rebuilt and
  spawned by the first packet — the scale-to-zero shape, for raw TCP. A short
  connect-refused retry smooths the boot race, and live streams hold the
  backend against the idle reaper.
- **Unbinding severs, not drains.** Removing the binding closes the host
  listener *and* cuts every live flow — unpublishing means traffic stops now.
- TCP is spliced; UDP is a sessioned packet relay with conntrack-style idle
  expiry (30 s) and a per-listener session cap. One binding per host
  port/proto, enforced at bind time; listener failures (port taken,
  privileged port) surface in `bx ingress` and the admin panel rather than
  silently.
- **Non-isolated tiers degrade honestly.** Without `--isolate` (tier 1/2,
  `make dev`) backends live on the host network, so the relay dials
  `127.0.0.1:<port>` — same contract, no sandbox required.

## Three ways in, mapped

Ingress traffic has three legitimate origins, each with its own mechanism:

1. **The internet via a runtime host port** — the two planes above.
2. **A VPN/router tile** (ING-6): a net-provider tile terminating e.g.
   WireGuard delivers inbound traffic over **lan-ingress legs** — a service
   tile binds `interfaces {kind:"lan-ingress"}` to the provider and receives a
   second addressed TUN (the 10.43/16 inbound twin of the 10.42/16 egress
   splice links; provider `.1`, service `.2`, no default route). The provider
   learns its client map from `XBIN_LAN_INGRESS`; the service learns its
   stable address from `XBIN_IFACE_<slot>_IP`. This is an **L3 link**: the
   provider can reach *all* of the tile's ports and is trusted to filter — it
   holds the admin-only `cap:net-admin` grant, and the `exposes` allowlist is
   not consulted on this path ([12-egress.md](12-egress.md)).
3. **A sibling tile** — deliberately *not* through ingress. A consumer
   declares a `stream` interface and the owner binds it to the provider's
   exposed slot (`db=apps/postgres#pg`, tcp-only); xbind splices the two
   netns's and injects `XBIN_IFACE_<slot>_ADDR`. The binding is the
   authorization, exactly like http interfaces
   ([11-interfaces.md](11-interfaces.md)) — no public round-trip, no
   anonymous downgrade.

## Hairpin without leaving home (ING-6)

A tile that hardcodes its own public URL would naively need to go out and come
back — which the `internet` egress policy would correctly drop (the packet's
destination is this host, not a public address), and which breaks anyway when
public DNS doesn't point at the box (NAT, dev). xbin solves it with
**split-horizon resolution** instead of a real out-and-back:

```
  tile resolves blog.example.com          tile connects 10.0.2.4:443
        │ (relay answers :53)                    │ (relay TCP handler)
        ▼                                        ▼
  published name?  ──yes──▶  A = 10.0.2.4   HairpinDial(443)
                             (hairpin VIP)       │
                                                 ▼
                                     the same ingress path that port takes
                                     from outside (L4 route / builtin
                                     listener) — same anonymous principal
```

Every egress relay intercepts DNS queries for **published** hostnames (exact
hosts and zone-covered names) and answers with the hairpin VIP; TCP flows to
the VIP short-circuit into whatever the ingress path for that port is. The
tile arrives as the same anonymous `ingress` principal an outside visitor
would — hairpinning grants nothing that the public internet doesn't already
have. Only tiles with *some* egress get split-horizon at all: a no-egress tile
has no DNS and no hairpin, so "no network" keeps meaning no network.

## Governance, lifecycle, operations

- **Policy ceiling** ([07-users-orgs.md](07-users-orgs.md)): the `ingress`
  deny kind makes covered tiles unpublishable — enforced at **approval** (the
  bind API refuses, naming the row) *and* at **every evaluation** (existing
  bindings go inert: routes vanish, host listeners close, split-horizon stops
  answering). A hand-edited `xbin.json` cannot out-vote the ceiling. A `net`
  deny additionally severs lan-ingress legs — they are network links.
- **Lifecycle** ([14-lifecycle.md](14-lifecycle.md)): templates and
  disabled/offloaded tiles publish nothing; re-enabling restores routes from
  the same bindings.
- **Low ports**: binding a host port below 1024 (the Traefik tile's :80/:443)
  needs `AmbientCapabilities=CAP_NET_BIND_SERVICE` on the xbind unit — that
  grant covers host binding only; in-netns low ports need nothing.
- **Surfaces**: `bx expose / unexpose / ingress [routes]`; the admin tile's
  interfaces → ingress panel (publish/unpublish with route editors, live
  routes, listener and forward-door health); `GET /api/xbin/ingress` (admin
  overview) and `GET /api/xbin/ingress-routes` (terminator-scoped); unbound
  expose slots appear as pending binds so publishing rides the same
  bind-on-install flow as every other capability.

## Invariants (ING-1..ING-6)

- **ING-1** — `exposes` + config-carrying bindings; declaring is inert,
  binding is the owner's publish act; unexposed/unbound = unreachable.
- **ING-2** — hostname authority at bind: exact host, or a delegated zone
  with bounded runtime self-registration; no per-host approval queue, no
  out-of-zone claims.
- **ING-3** — two HTTP terminators, one broker-computed route table; ACME
  lives in a tile, never the daemon; terminators route only what is bound
  through them.
- **ING-4** — L4 as a userspace relay: the egress stack is also the inbound
  door; deny-all plumbing for ingress-only tiles; kernel fast-path deferred.
- **ING-5** — the `ingress` principal is structural: anonymous, one tile,
  declared paths only, unforgeable `From`, no reach beyond.
- **ING-6** — VPN-side ingress is a lan-ingress link (the provider is the
  filter); hairpin is split-horizon resolution, never a real out-and-back.
