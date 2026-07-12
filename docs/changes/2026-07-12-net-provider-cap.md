# 2026-07-12 — net-provider tiles need the `cap:net-admin` grant

A **net-provider tile** — a router/firewall/VPN/meter tile that other tiles
bind their `net` interface to, so their egress splices through it
(`plans/interfaces.md`; the `egress-approver` builtin, `examples/netrouter`)
— must now hold the reserved capability grant **`cap:net-admin`**.

## Why

On 2026-07-10 every tile backend became fully unprivileged (all Linux
capabilities dropped at spawn, plus a seccomp block-list — good hardening for
ordinary tiles). But a net provider builds a real Linux dataplane inside its
sandbox: routing tables, the `ip_forward` sysctl, `AF_PACKET` sockets for
packet inspection. Those need `CAP_NET_ADMIN` and `CAP_NET_RAW`, which the
blanket drop removed — so providers died at gate setup with:

```
egress-approver: ip_forward: … permission denied
egress-approver: ip route add … table 100: Operation not permitted
egress-approver: af_packet socket: operation not permitted
```

## What breaks

A net-provider tile with **no** `cap:net-admin` grant fails to set up its
dataplane (the errors above) and can't route client traffic. Ordinary tiles
are unaffected — nothing changes for a tile that isn't a net provider.

## What to do

**The shipped provider tiles already declare it** (`egress-approver`,
`netrouter`) — so on a fresh import the grant shows up **pending** in the
grants panel; approve it once (it's admin-only). For an already-imported
provider, or a custom one, add to its `xbin.json`:

```jsonc
"uses": [
  { "target": "cap:net-admin", "role": "writer" }
  // …its other uses…
]
```

…then approve the pending grant (admin tile → roles & grants, or
`bx grant <tile> cap:net-admin:writer`), and its backend restarts with the
capabilities.

## Scope of the grant

`cap:net-admin` keeps exactly three capabilities — `CAP_NET_ADMIN`,
`CAP_NET_RAW`, `CAP_NET_BIND_SERVICE` — and only **inside the tile's own
network namespace**; nothing reaches the host network or the host's
capabilities. Every other capability is still dropped and the backend seccomp
block-list is unchanged. It's **admin-only to approve** (a reserved grant,
never same-scope auto-granted), and a workspace/org policy `net` deny
(`plans/orgs.md`) strips it — a tile denied network can't be a net provider.
