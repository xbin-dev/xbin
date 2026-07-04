# Egress Approver — API

A default-deny **net provider** tile (see `plans/interfaces.md`). Bind another
component's `net` interface to `apps/egress-approver` and its egress is routed
through here; every new destination IP is held for the owner to approve or deny.
This tile's own `net` interface must be bound to what provides its internet
(usually the `internet` builtin): `bx bind apps/egress-approver net=internet`.

The endpoints below are served on the component's own API and are only reachable
by the tile's own frontend (which is admin of itself) or an admin — there is no
exposed role for other components.

| Method | Path | Body / Query | Returns |
|--------|------|--------------|---------|
| GET  | `/state`  | — | `{pending, approved, denied, clients, egressReady}` |
| POST | `/approve` | `{ip}` | `{ok:true}` — allow egress to `ip` (route added) |
| POST | `/deny`    | `{ip}` | `{ok:true}` — block `ip` and stop prompting |
| POST | `/forget`  | `{ip}` | `{ok:true}` — drop the decision (re-prompts if still active) |
| GET  | `/detail` | `?ip=` | `{ip, rdns, rdap}` — reverse DNS + RDAP/whois |
| GET  | `/status` | — | `{clients, egress, approved}` |

`pending` / `approved` / `denied` are arrays of
`{ip, rdns, firstSeen, lastSeen, bytesIn, bytesOut}` (times are unix-ms; bytes
are since the last restart). `pending` is every observed destination that is
neither approved nor denied.

## How it works

- **Enforcement** is policy routing: client-sourced traffic (`10.42.0.0/16`) is
  diverted to a routing table whose default is a blackhole, so unapproved egress
  is dropped. Approving an IP adds a `/32` route for it out the tile's egress.
  The tile's *own* traffic (reverse DNS, RDAP) uses the main table and is never
  gated — that is what lets you look a destination up before deciding.
- **Observation** is an `AF_PACKET` tap on the client links, so a new
  destination shows up (and is metered) even though the gate drops it until you
  approve. TCP retries the connection, so approving mid-handshake just works.
- **Persistence**: approve/deny decisions live in the `approvals` kv resource
  and are re-applied on restart. Byte counters are in-memory ("since restart").

Extend the same shape into a metering tile (bill per byte), a scheduled firewall
(time-boxed approvals), or a DPI tile (inspect, not just gate, the AF_PACKET
stream).
