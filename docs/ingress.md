# Ingress — publishing tiles to the outside world

Egress asks *"what may this tile reach?"*; ingress asks *"who may reach this
tile?"*. Both ride the same owner-gated binding model: a tile **declares** in
its manifest, and nothing is reachable until the **owner binds** the
declaration to a source. An unexposed — or exposed-but-unbound — endpoint is
exactly as unreachable as today: tile backends serve only their private
socket behind xbind's authenticated proxy.

```jsonc
// xbin.json
"exposes": {
  "web":  { "kind": "http",   "paths": ["/", "/api/public/*"] },
  "game": { "kind": "stream", "proto": "udp", "port": 2456 }
}
```

| section      | offers to      | direction |
|--------------|----------------|-----------|
| `interfaces` | (requests)     | egress    |
| `provides`   | other tiles    | intra-workspace |
| `exposes`    | the outside    | **ingress** |

Publishing is the owner's action (admin tile → interfaces → **ingress**, or
`bx expose`); a tile can never publish itself.

## HTTP endpoints (`kind: "http"`)

Your backend just serves HTTP as always — same handlers, same socket. When
the owner binds the slot, external requests for the bound hostname reach it.

- **`paths` is the public allowlist — default-deny.** Exact paths, or
  subtrees ending in `/*` (`"/*"` publishes everything). Requests outside it
  are 404'd *before* your backend sees them, with dot-segments resolved first
  (no `..` tricks). Everything not listed — and all of `/api/xbin/*`, and
  every other tile — is structurally out of reach of public traffic.
- **Public callers are anonymous.** They arrive with `X-XBin-From: ingress`
  (SDK: `xbin.Caller(r).Ingress()`), no role, and the public hostname in
  `X-XBin-Ingress-Host`. **Your app owns any further auth on those routes** —
  xbind guarantees only *external, anonymous, this tile, these paths*.
  Non-public paths keep working normally for the owner/granted tiles through
  `/api/<tile>/…`; the same handler can serve both, branching on the caller.
- The workspace session cookie is stripped from public requests; the
  visitor's own cookies (your app's sessions) pass through. WebSockets and
  SSE work.

### Binding: who terminates, and for which hostname

An http binding names an **ingress source** and a **hostname authority**:

```
bx expose apps/blog web=apps/traefik --host blog.example.com   # public TLS
bx expose apps/blog web=runtime --host blog.example.com        # builtin listener
bx expose apps/cms  web=apps/traefik --zone '*.sites.example.com'
bx unexpose apps/blog web
```

- **`runtime`** — xbind's own second listener (`xbind --ingress-listen
  :8080`, off by default; `--ingress-cert/--ingress-key` for bring-your-own
  TLS, reloaded on renewal). Right when TLS is handled in front of xbind
  (Tailscale, a load balancer, an existing reverse proxy) or for dev. It
  never shares the console listener — public traffic can't reach the
  authenticated surface.
- **A terminator tile** — a component with `provides {"…": {"kind":
  "ingress"}}`; the shipped **Public HTTPS (Traefik)** builtin terminates TLS
  with automatic Let's Encrypt certificates and hands each request back to
  xbind for the last hop. Import it, bind its `web`/`websecure` stream
  exposes to host ports 80/443, bind its `net` to `internet` (ACME), set the
  ACME email on its page — then point tiles at it. Certificates live in the
  tile's own resource, never in the daemon.

**Exact host** (`--host`): the owner names the one public hostname — done.

**Delegated zone** (`--zone '*.sites.example.com'`): for multi-site tiles
(a CMS spawning sites at runtime). The owner draws the authority boundary
once; the tile then registers concrete hostnames itself:

```
PUT /api/xbin/ingress-hosts   {"hosts": ["a.sites.example.com", "b.sites.example.com"]}
```

Self-scoped (a tile registers only its own), **bounded to the delegated
zone** — a registration outside it is refused, so a compromised tile can
never claim `bank.example.com` — and conflict-checked against every other
exact host and registration. Unregistered zone names 404.

DNS is yours to point: an `A`/`CNAME` for exact hosts, a wildcard record for
zones, at the machine (or front) reaching the terminator's port.

## TCP/UDP endpoints (`kind: "stream"`)

Least-cursed by construction: the backend opens an ordinary listener on any
port in its sandbox — `net.Listen("tcp", ":2456")`, no SDK, no fd-passing —
and declares it. (Inside your own sandbox, *any* port works, including 80.)

```
bx expose apps/game game=runtime --listen :2456
```

binds a **host port**: xbind accepts host connections and relays them into
the sandbox (TCP splice; UDP as idle-expiring sessions). `--listen` defaults
to `:<port>`; host ports below 1024 need xbind itself to hold
`CAP_NET_BIND_SERVICE` (systemd: `AmbientCapabilities=CAP_NET_BIND_SERVICE`).
Unbinding closes the port and severs live flows. One binding per host
port/proto; failures (port taken) surface in `bx ingress` and the admin UI.

### Reaching an exposed service from a sibling tile

Don't hairpin — bind it directly. The consumer declares a **stream
interface** and the owner binds it to the exposed slot:

```jsonc
// consumer xbin.json
"interfaces": { "db": { "kind": "stream" } }
```
```
bx bind apps/app db=apps/postgres#pg     # provider#expose-slot (tcp only)
```

The consumer gets `XBIN_IFACE_DB_ADDR=10.0.2.2:20000` — a stable in-sandbox
address xbind splices to the provider's port. The binding is the
authorization, exactly like http interfaces. (For HTTP services, the
existing `http` interface already covers intra-workspace calls.)

### Ingress via a VPN / router tile (`lan-ingress`)

A net-provider tile (a WireGuard terminator, say) can deliver inbound
traffic to service tiles over private links. The service tile declares
`interfaces {"vpn": {"kind": "lan-ingress"}}` and the owner binds it to the
provider; the tile gets a second link with a stable address
(`XBIN_IFACE_VPN_IP`, e.g. `10.43.0.2`) into the provider's subnet, and the
provider (which sees `XBIN_LAN_INGRESS` — a JSON map of client → link
address) routes decrypted traffic to it. Note this is an L3 link: the
provider can reach **all** the tile's ports, and is trusted to filter (it
holds the admin-only `cap:net-admin` grant). The tile's `exposes` list is
not consulted on this path.

## Using your own public URL from inside (hairpin)

A tile (with egress) resolving its **published** hostname gets a
workspace-internal answer and is routed straight back through the ingress
path — no real out-and-back, works even when public DNS doesn't point at
this machine (NAT, dev). The tile still arrives as the anonymous `ingress`
principal, exactly like an outside visitor. Tiles with no egress get no
hairpin (or DNS) at all.

## Governance

- Exposing is a manifest declaration (agent-writable, inert); **binding is
  admin/owner-only** and carries the route config. Everything shows in the
  admin tile (interfaces → ingress: publish/unpublish, live routes, listener
  health) and `bx ingress`.
- A workspace/org **policy row can deny `ingress`** (docs/auth.md): matching
  tiles can't be bound, and any existing binding goes inert — the ceiling
  holds even against a hand-edited `xbin.json`.
- Disabled/offloaded tiles publish nothing; re-enabling restores routes.
- `GET /api/xbin/ingress-routes` is readable by terminator tiles (their own
  routes) and admins only.

## Debugging

`bx ingress` shows every exposed endpoint (bound/unbound/blocked), the live
host→tile routes, stream listener state with active-connection counts, and
the builtin listener's status. First public hit builds/spawns the backend
like any request; `bx logs <tile>` as usual.
