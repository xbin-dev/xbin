# Egress & network provider tiles

Every isolated backend starts with an empty network namespace: no interfaces, no routes,
no DNS — zero IP egress. Network is a capability delivered by binding the `net`
interface, and the delivery mechanism is a userspace gVisor relay that terminates,
filters, and meters every flow. Provider *tiles* extend that: a firewall, VPN, or router
is an ordinary sandboxed component that other tiles route their egress through, composed
purely from the binding graph.

**Related:** [11-interfaces.md](11-interfaces.md) (the `net` binding) ·
[13-ingress.md](13-ingress.md) (the inbound twin: `lan-ingress`, host relays) ·
[08-sandbox.md](08-sandbox.md) (the netns and capability model) ·
[06-authorization.md](06-authorization.md) (the policy ceiling) ·
reference: [/docs/isolation.md](/docs/isolation.md), [/docs/auth.md](/docs/auth.md) ·
design: plans/interfaces.md, plans/isolation.md, docs/changes/2026-07-12-net-provider-cap.md.

## Default-deny is the ground truth

Under isolation (`--isolate`, tier 3 — [08-sandbox.md](08-sandbox.md)) a component's
backend runs in its own network namespace with nothing but loopback. Absent a `net`
binding, the netns stays empty:

- No default route, no addresses — outbound IP has nowhere to go.
- **No DNS.** Name resolution is part of egress; a tile with no `net` binding can't
  resolve either. This closes DNS as a silent exfiltration side-channel.
- The gateway unix socket is always bind-mounted in regardless, so a tile with no
  network can still call `/api/xbin/*` and other tiles it's granted — the RBAC plane is
  separate from the IP plane.

Egress is *only* ever the `net` interface binding. There is no `net:*`-in-`uses` grant
(**IFACE-4** removed it): `POST /api/xbin/grants` refuses a new `net:*` target with a
message pointing at `bx bind`, and only allows DELETE so a stale grant from an older
workspace can be cleaned up.

## The `net` interface and its builtins

`"interfaces": { "net": { "kind": "net" } }`, bound by the owner. Three builtin
providers cover the common cases; a fourth "provider" is a tile (next section).

| binding | reach | mechanism |
|---------|-------|-----------|
| `internet` | **public addresses only** | gVisor relay under a public-only policy |
| `lan:<cidr>` | that CIDR (+ public, per rules) | relay under a CIDR policy |
| `host` | the host's full network, LAN + host services, interfaces visible | shares the host netns (`HostNet`) — powerful, owner-only |
| a provider tile | whatever the provider forwards | TUN spliced to the provider (below) |

**`internet` is public-only, exactly.** A destination is reachable iff it is a valid,
routable public address — explicitly *not* RFC1918/ULA private, loopback, link-local
(unicast or multicast), other multicast, or unspecified. So `net=internet` can never
reach the LAN or host services; that's what `lan:<cidr>` (a specific subnet) or `host`
(everything, deliberately) are for. `host` shares the host network stack outright — no
relay, no filtering — and is the owner's escape hatch, never something a tile gets
casually.

## The userspace relay

For `internet` and `lan` bindings, xbind is the middlebox. Inside the tile's netns the
sandbox init creates a TUN (`bx0`, `10.0.2.15/24`, default route via the virtual gateway
`10.0.2.2`) and hands its fd back to xbind over an SCM_RIGHTS control socket. xbind
attaches a **gVisor TCP/IP stack** to that fd and forwards every flow under a policy.

```
   tile netns                         xbind process (host)
 ┌─────────────┐   IP packets    ┌──────────────────────────────┐   host sockets
 │ backend     │───▶ bx0 (TUN) ──┼─▶ gVisor stack                │──▶ internet
 │ 10.0.2.15   │◀──── fd ────────┼   • per-flow allow(ip,port)?  │    (only if allowed)
 └─────────────┘                 │   • :53 → host resolver       │
                                 │   • ICMP echo → host ping     │
                                 │   • flow records + byte stats │
                                 └──────────────────────────────┘
```

What the relay does, per flow:

- **Allow decision.** Each new TCP/UDP flow (and ICMP echo) is checked against the
  binding's policy — `internet` ⇒ `isPublic(dst)`, `lan:<cidr>` ⇒ CIDR match, plus any
  host/port rules. Permitted flows are dialed from the host and spliced bidirectionally;
  denied TCP is RST, denied UDP is dropped.
- **DNS at `:53`.** UDP to port 53 is pinned to the **host's resolver** (from the host's
  `/etc/resolv.conf`, else `1.1.1.1:53`) — so name resolution works for `net=internet`
  without the tile choosing a resolver. Direct relay clients get `resolv.conf` pointing
  at the relay's own DNS address (`10.0.2.3`); the relay forwards from there.
- **Hostname rules matched at DNS time.** A `net:<hostname>` rule can't match a raw IP,
  so the relay pairs a DNS answer with the rule and admits the resolved address — the
  policy layer (`AllowsHost`) exists for this even though the shipped builtins use IP/
  CIDR rules.
- **ICMP echo (ping) works.** gVisor has no ICMP forwarder, so a link-layer tap siphons
  echo requests and re-issues them from an *unprivileged* host ICMP datagram socket
  (`SOCK_DGRAM`+`IPPROTO_ICMP`), propagating the guest's TTL. Reachability and RTT are
  genuine; concurrent pings are capped and excess dropped. Other ICMP and traceroute
  need the `host` scope.
- **Visibility.** The relay keeps a rolling ring of recent flows plus allowed/denied
  counts and tx/rx byte totals — surfaced per-backend in the admin runtime tab and
  `bx status` ([15-operations.md](15-operations.md)).
- **Gateway host-forwards.** Flows to the virtual gateway `10.0.2.2` on a *mapped* port
  are policy-exempt forwards to a host service — this is how a netns-isolated terminal
  reaches xbind's console for `bx`/`curl` without any host interface being visible, and
  how ingress terminator/stream forwards ride ([13-ingress.md](13-ingress.md)).

The relay is per-instance and closed when the backend exits. Off Linux it's a no-op
stub; the whole thing only exists under isolation.

## Net provider tiles — the middlebox pattern

A tile that declares `provides: { …: { kind: "net" } }` becomes a bindable network
provider. Other tiles bind their `net` interface to it (`bx bind apps/x net=apps/vpn`),
and xbind wires a **point-to-point L3 link** per client and **dumb-splices raw IP
packets** between the client's egress TUN and the provider's per-client TUN
(**IFACE-3**):

```
  client A netns          xbind (splice)         provider tile netns (a real router)
 ┌────────────┐         ┌──────────────┐        ┌───────────────────────────────┐
 │ bx0 TUN    │─ fd ───▶│ raw IP pump  │◀─ fd ──│ bxc0  10.42.0.1/30            │
 │ 10.42.0.2  │◀────────│ A.tun ↔ P[0] │        │ bxc1  10.42.1.1/30 ── ip_fwd ─┼─▶ its OWN
 └────────────┘         │              │        │   + routing / nftables / wg   │   net binding
  client B netns        │ B.tun ↔ P[1] │◀─ fd ──│ bx0  (provider's egress) ─────┘   (→ internet,
 ┌────────────┐         └──────────────┘        └───────────────────────────────┘    or another
 │ 10.42.1.2  │─────────────────────────────────────────────────────────────────▶    provider)
 └────────────┘
```

- **Per-client `/30`.** Client index *i* gets link `10.42.<i>.0/30` — provider side
  `.1`, client side `.2`. The provider learns its **roster at spawn** and creates that
  many extra TUNs (`bxc0`, `bxc1`, …); a roster change (a client bound or unbound)
  restarts the provider in v1 (hot-add is the deferred optimization). A provider restart
  hands back fresh link fds, so xbind nudges each already-running client to re-splice.
- **The provider is a real multi-homed Linux router.** With `ip_forward`, routing
  tables, `nftables`/`ip rule`, and `AF_PACKET` taps, it does whatever a middlebox does:
  a firewall is its ruleset, a VPN is a `wg` egress interface, a DPI reads packets, a
  meter counts them.
- **It is itself a client of its own `net` binding.** The provider forwards approved
  client traffic out *its own* egress — which is another `net` binding, so **chains
  compose emergently**: client → firewall tile → VPN tile → `internet`. gVisor runs only
  at the terminal `internet` hop; every intermediate hop is a raw splice. The graph must
  stay a DAG ([11-interfaces.md](11-interfaces.md)).
- **DNS in a chain.** A spliced client's `resolv.conf` points at a public resolver
  (`1.1.1.1`); the request travels the chain to the terminal relay, which pins `:53` to
  the host resolver — so DNS resolves at the *end* of the path, wherever that egresses.

### `cap:net-admin` — the capability providers need

Building that dataplane (routing tables, the `ip_forward` sysctl, `AF_PACKET` sockets)
requires network-admin capabilities the sandbox drops from *every* ordinary backend. On
2026-07-10 the hardening that made all tile backends fully unprivileged broke net
providers — they died at gate setup with `operation not permitted` (ip_forward, ip route,
AF_PACKET). The fix (**D18a**) is a narrow, admin-only capability grant:

- A provider declares `uses: [{ "target": "cap:net-admin", "role": "writer" }]`. It's a
  **reserved grant** — admin-only to approve, never same-scope auto-granted — and shows
  up *pending* in the grants panel on import.
- With it the sandbox keeps exactly `CAP_NET_ADMIN`, `CAP_NET_RAW`, and
  `CAP_NET_BIND_SERVICE` — **inside the tile's own network namespace only**. Nothing
  reaches the host network or host capabilities; every other capability is still dropped
  and the backend seccomp block-list is unchanged.
- A workspace/org policy `net` deny (below) strips it: a tile denied network can't be a
  net provider.

The shipped provider tiles already declare it, so a fresh import lands it pending —
approve once and the backend restarts with the caps. See
docs/changes/2026-07-12-net-provider-cap.md.

### The worked examples

- **`examples/netrouter`** — the minimal skeleton: `interfaces:{net}` (its own egress),
  `provides:{net}` (offers the interface), `uses:[cap:net-admin]`, and a backend that
  just enables `ip_forward` and passes traffic through. The base you extend with nftables
  or a `wg` interface.
- **`builtin-tiles/egress-approver`** — the reference programmable provider. A
  **default-deny forward gate**: client traffic is dropped unless its destination IP has
  been *approved by a human*, surfaced on the tile's own page with reverse-DNS and
  RDAP/whois detail. The gate is forward-only so the tile's own lookups stay free; an
  `AF_PACKET` tap on the client links reports every forwarded packet (even dropped ones)
  to both surface new destinations and **meter bytes in/out per remote** since restart.
  Approvals persist in a kv resource; byte counters are memory-only by design. It is the
  template for "a net provider with its own admin UI."

## Inbound: `lan-ingress` provider legs

A net provider can also deliver traffic *into* a service tile — the inbound twin of the
client splice. A tile binds a `lan-ingress` interface to the provider and gets a second
addressed link on the provider's subnet (`10.43.<i>.0/30`, the twin of the `10.42/16`
egress links); the provider routes decrypted/terminated traffic (a VPN's inner packets,
say) to it. It's an L3 link — the provider reaches all the tile's ports and is the
filter. The full story, including how host-side inbound and hairpin resolution compose,
is [13-ingress.md](13-ingress.md).

## The policy ceiling on network

Workspace/org policy rows (**D20**, [06-authorization.md](06-authorization.md)) can deny
the `net` capability class for a set of tiles. The deny is enforced twice: **at bind
time** the bindings API refuses with the blocking row named, and **at every evaluation**
`netBinding` returns unbound for a covered tile — so even a pre-existing binding (or a
hand-edited `xbin.json`) goes inert the moment a deny row covers the tile, and its
`cap:net-admin` and lan-ingress legs go with it. This is how an org says "these tiles
never touch the network," and has it hold against the tiles themselves.

## Where you see it

| surface | shows |
|---------|-------|
| admin **runtime** tab | per-backend namespaces, live egress flows (allowed/denied, dst, bytes), the wiring graph |
| `bx status [<tile>]` / `GET /api/xbin/tile-status` | one tile's backend + egress policy + activity |
| an egress-approver / router tile's own page (`<bx-frame>`) | its approve/deny queue, per-IP RDAP, throughput |
| `bx iface` / admin interfaces tab | which tiles provide net, who's bound to whom |

The through-line: **the network is a middlebox xbind already owns.** `net=internet` is
that middlebox with a public-only policy; a provider tile is that middlebox made
pluggable — a sandboxed program on the packet path, wired in by one loud owner decision.
