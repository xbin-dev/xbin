# Ingress — publishing tiles (request · provide · **expose** · bind)

> **Status: IMPLEMENTED** (2026-07-12, both planes; decisions recorded as
> ING-1..6 in DECISIONS.md; builder contract in docs/ingress.md). One
> mechanism improved over this plan: the L4 relay needs **no setns** — the
> egress relay's gVisor stack is already attached to the tile's TUN, so
> inbound is `relay.DialIn` (an outbound dial from the stack, sourced from
> the gateway IP); ingress-only tiles get the TUN+relay plumbing with a
> deny-all egress policy and no DNS. Split-horizon rides the same relay
> (DNS interception → hairpin VIP 10.0.2.4 → the ingress path).

The inverse of egress (`plans/interfaces.md`). Egress asks "what may this tile
**reach**"; ingress asks "who may **reach** this tile". Both ride the one
owner-gated binding spine, so ingress is not a new subsystem bolted on — it's a
third direction on the interface graph:

| section      | offers to        | bound by            | direction |
|--------------|------------------|---------------------|-----------|
| `interfaces` | (requests)       | owner → a provider  | egress    |
| `provides`   | other **tiles**  | a requester tile    | intra-ws  |
| **`exposes`**| the **outside**  | owner → an ingress source | ingress |

Grounding, unchanged from egress: **tile backends serve only on their gateway
unix socket** (`internal/proxy`), reached today exclusively through xbind's
*authenticated* reverse proxy (`/api/<tile>`, owner/user/frame-token only).
There is no host port and no unauthenticated path to a tile. Ingress adds
exactly that — deliberately, per exposed endpoint, owner-bound.

## Model

- **`exposes`** — a new `xbin.json` section: endpoints a tile offers to the
  outside world.
  ```jsonc
  "exposes": {
    "web":  { "kind": "http", "paths": ["/", "/api/public/*"] },   // L7
    "game": { "kind": "stream", "proto": "tcp", "port": 2456 }     // L4
  }
  ```
- **ingress binding** = `(component, expose-slot) → source [+ route config]`.
  The one model extension: **bindings now carry config**, not just a provider
  ref — a route (host/zone/path) for `http`, a listen addr for `stream`.
  Workspace storage widens `bindings[c][slot]` from a ref to
  `{ref, host?, zone?, path?, listen?}` (custom unmarshal; every existing
  binding parses unchanged — bare refs stay bare on disk, exactly like the
  string→array widening did for `multi`).
- **source** = a builtin (`runtime`) or a **tile** (`apps/traefik`, a VPN
  tile) — symmetric with egress's `internet` builtin vs a provider tile.
- **default-deny preserved**: an unexposed endpoint, or an exposed-but-unbound
  one, is unreachable from outside — precisely today's behavior. Exposing is a
  manifest declaration (agent-authored); **binding is the owner's** (loud,
  admin-gated), so a tile can't publish itself.
- **DAG** still holds (an ingress source that is itself a tile has its own
  egress/deps; no loops).

## HTTP(S) plane (L7)

An exposed `http` endpoint is already served on the tile's gateway socket;
ingress means an **HTTP terminator** routes external requests to it. Two
terminators satisfy one contract (mirroring `internet` builtin vs provider
tile):

- **`runtime` (builtin, minimal).** xbind terminates on a **second listener**
  (`--ingress-listen`), never the console port — public unauthenticated
  traffic must not share the authenticated console socket. Host-routes to the
  target tile's backend socket in-process (it holds the sockets — no extra
  hop). TLS is **bring-your-own-cert** or none: the intended deployments are
  behind Tailscale, an external LB, or a reverse proxy that already does TLS.
  No ACME in the daemon.
- **Traefik+Certbot builtin tile.** The batteries-included public-TLS path —
  ACME/cert lifecycle lives in a tile, not the daemon (keeping the "minimal
  middlebox, fat plugins are tiles" ethos, interfaces.md). It terminates TLS,
  then reverse-proxies **into xbind's gateway** (it can't reach unix sockets
  from its netns) via a builtin `http` interface xbind provides
  (`$XBIN_IFACE_forward_URL` → xbind's ingress-forward endpoint); xbind does
  the last hop by Host. **Recursion that justifies "both planes together":**
  the Traefik tile gets its own raw `:80/:443` via the **L4 runtime ingress**
  (it `exposes` those ports, owner binds them to `runtime`), so the stream
  plane underpins the public HTTP tile. It needs no `cap:net-admin` (it's a
  userspace proxy, not a router) — only that bound forward interface + its own
  egress for ACME challenges.

**Route table** lives in xbind (one source of truth both terminators consult):
`host → (tile, expose-slot)`, assembled from bindings + runtime host
registrations (below).

### Domain assignment — delegated-zone authority (ING-2)

A binding grants a **hostname authority**; runtime registration only fills in
specifics *within* it. This generalizes "owner sets the host" and "tile picks
the host" and reuses the `instances` precedent:

- **Exact host** (the common single-domain case): owner sets it at bind —
  `bx expose apps/blog web=traefik --host blog.example.com`. The tile
  registers nothing.
- **Delegated zone** (a CMS/multi-site tile): owner delegates a wildcard —
  `--zone '*.sites.example.com'`. The tile self-registers concrete hostnames
  at runtime (`PUT /api/xbin/ingress-hosts {hosts:[…]}`, self-scoped like
  iface-instances), **bounded to its zone** — a registration outside the zone
  is rejected. The owner drew the authority boundary once; no per-host
  approval queue, and a tile can never claim `bank.example.com`.

### Security — the `ingress` principal (ING-5)

Published endpoints are reachable by **unauthenticated external clients**, so
this path bypasses the owner-auth middleware and presents a new principal:

- **`ingress`** — the request reaches the target backend as its-own-public
  caller, **confined to the paths the tile declared** (`exposes.web.paths`,
  default-deny everything else), and structurally unable to reach
  `/api/xbin/*` or any *other* tile (the route table only maps a host to the
  one tile that exposed it). The tile owns its app-level auth on those routes;
  xbind guarantees only "external, anonymous, this-tile, these-paths".
- xbind injects a verified `X-XBin-From: ingress` (inbound `X-XBin-*` stripped
  as always), so a tile can tell a public hit from an owner/tile call and
  vary behavior (rate-limit, hide admin UI).
- A workspace/org **policy ceiling** may deny ingress (a new `ingress` deny
  kind, or fold into `net`) — an org whose tiles must never be publicly
  reachable.

## TCP/UDP plane (L4)

Least-cursed by construction: **the tile listens on an ordinary port in its
netns** (`net.Listen("tcp", ":2456")`) and declares it in `exposes`. No
fd-passing, no SCM_RIGHTS, no language-specific shim — ordinary socket code.
xbind (or a net provider) splices inbound connections to that port. This is
the inbound twin of the egress relay, which already moves userspace TCP/UDP
(guest→host); ingress is host→guest.

- **v1 mechanism: userspace relay (ING-4).** xbind accepts the inbound
  connection and, holding the tile's netns fd, dials the tile's listening port
  (setns→dial→copy, or a connector pinned to the netns) and bidirectionally
  copies; UDP is a sessioned packet relay (same as egress). No host
  firewall/routing mutation — safe and portable. A **kernel fast-path**
  (veth + nftables DNAT) is a later throughput optimization, not v1.

### The three ingress sources, mapped

1. **Runtime host port.** `bx expose apps/game net=runtime --tcp :2456` → xbind
   listens on host `:2456`, relays each connection into the tile's netns port.
   (This is exactly what the Traefik tile uses for `:80/:443`.)
2. **Net / VPN tile.** A VPN tile decrypts packets destined for a workspace
   service. It works when the target tile binds a **second link into the
   provider's internal subnet**: the net provider gains a `lan-ingress`
   provide (an internal `/24`), a service tile binds it and gets a stable
   address, and the provider (already a real router with `ip_forward`,
   `cap:net-admin`) routes decrypted traffic to it. Pure reuse of the
   per-client TUN/splice — an inbound link with a fixed address instead of a
   default route (ING-6).
3. **Internal tile → exposed service (same workspace).** Prefer a **direct
   `stream` binding**: tile A binds tile B's exposed slot; xbind splices
   `A.conn ↔ B.port` with **no hairpin at all**. For HTTP the existing
   `http` interface already covers intra-workspace calls.

### Hairpin

- The direct `stream`/`http` bindings above need **no hairpin** — that's the
  preferred intra-workspace path.
- Where a tile must use the *public* address (a library that hardcodes the
  hostname), solve it with **split-horizon resolution**: a workspace tile
  resolving `blog.example.com` gets xbind's internal address and is routed
  straight to the target. "Hairpin" becomes an internal direct path — never a
  real out-and-back (which the `internet` relay's public-only policy would
  otherwise drop). The relay already pins DNS at the terminal hop; this adds a
  workspace-internal answer for published names.

## The Traefik+Certbot builtin tile

`builtin-tiles/traefik` — the worked public-TLS example, as `egress-approver`
is for egress:

- `exposes` `:80`/`:443` (owner binds → `runtime`); `interfaces` a `forward`
  http slot (→ xbind's ingress-forward builtin) + `net` (→ `internet`, for
  ACME); `uses` its own `res:` for cert storage.
- Runs Traefik with dynamic config generated from xbind's route table
  (host→tile) + certbot/Traefik-native ACME for certs; its own admin UI
  (routes, cert status, per-route throughput) via `<bx-frame>` like the
  net-provider UIs.
- Sandboxed like any tile — a compromised terminator is contained to what it's
  bound to (forward endpoint + its cert vault + its egress).

## Binding grammar / CLI / UX

```
bx expose apps/blog web=traefik --host blog.example.com     # exact host
bx expose apps/cms  web=traefik --zone '*.sites.example.com' # delegated zone
bx expose apps/blog web=runtime                              # builtin terminator (BYO/none TLS)
bx expose apps/game net=runtime --tcp :2456                  # L4 host port
bx expose apps/db   net=apps/vpn                             # reachable only via the VPN tile
bx unexpose apps/blog web
bx ingress ls | routes | flows                               # published endpoints, live routing
```

Admin **Interfaces** tab gains an **Ingress** sub-UI: a published-endpoint
table (tile · slot · source · host/zone/port · TLS · live throughput), the
bind/route editor, and each terminator tile's own config embedded via
`<bx-frame>` (Traefik routes/certs). `whoami`/route data is admin-gated; the
public path is anonymous by construction.

## Build order (both planes, one design)

1. **Model** — `exposes` on the manifest, binding config on the workspace
   (custom unmarshal, back-compat), validation + DAG.
2. **`ingress` principal + per-expose public-path allowlist** in
   `internal/auth` (the security core — default-deny, no xbin/sibling reach).
3. **Runtime HTTP terminator** — second listener, Host route table, in-process
   forward to tile sockets; BYO-cert.
4. **Runtime L4 relay** — host-port accept → netns splice (TCP+UDP).
5. **Route registration** — bindings + `PUT /ingress-hosts` (delegated-zone
   bounded); one route table both terminators read.
6. **Net-tile `lan-ingress`** — the inbound provider link (reuse TUN/splice).
7. **Traefik+Certbot tile** — ingress-forward builtin endpoint + the tile.
8. **Split-horizon resolution** for published names (hairpin).
9. **CLI (`bx expose`/`ingress`) + admin Ingress UI**.
10. **Docs** — auth.md (the ingress principal), protocol.md (new surface),
    interfaces.md (unpark ingress), a `docs/ingress.md` builder guide,
    changelog + AGENTS.md (how to `exposes` a tile).

## Decisions (ING-*)

- **ING-1** — `exposes` section + config-carrying bindings; the owner binds an
  ingress **source** to each exposed slot; unexposed/unbound = default-deny
  (mirror of `interfaces`/IFACE-1).
- **ING-2** — hostname **authority** at bind (exact host or delegated wildcard
  zone) + bounded runtime registration within the zone (reuse the `instances`
  precedent); a tile can't claim outside its zone; no per-host approval.
- **ING-3** — **two HTTP terminators**, one contract: a minimal builtin
  second-listener (BYO/no TLS, for Tailscale/LB/dev) and a Traefik+Certbot
  tile (public ACME TLS); ACME stays out of the daemon.
- **ING-4** — L4 via **userspace relay first** (inbound twin of the egress
  relay; no host-firewall mutation); kernel veth+DNAT fast-path deferred.
- **ING-5** — a distinct **`ingress` principal**: anonymous, this-tile,
  declared-public-paths-only, structurally walled off from `/api/xbin/*` and
  sibling tiles; a policy ceiling may deny ingress.
- **ING-6** — net-tile ingress = a **`lan-ingress` provide** + service-tile
  bind (reuse per-client TUN/splice + the provider's `ip_forward`); hairpin
  solved by **split-horizon resolution**, not real out-and-back.

## Touchpoints

`internal/registry` (`Exposes` on the manifest, config on `Bindings`) ·
`internal/broker` (ingress resolution, route table, host registration,
policy) · `internal/auth` (the `ingress` principal + public-path gate) ·
`internal/server` (second listener, ingress serve/route, ingress-forward
endpoint) · `internal/sandbox` (inbound relay, `lan-ingress` link) ·
`internal/runner` (wire relays + inbound links) · `cmd/bx` (`expose`,
`ingress`) · `web` (Ingress sub-UI) · `builtin-tiles/traefik` (the worked
terminator) · `workspace-template/AGENTS.md` + `docs/ingress.md` (builder
guide). Unparks the ingress item in `plans/interfaces.md` §Phasing.
```
