# Per-component isolation — design (enforcement Tier 3)

This specifies **Tier 3** of the enforcement roadmap (`plans/auth.md` §9,
ARCHITECTURE §8): OS-level containment of the **runtime plane** so a component
backend can touch *only what it was granted* — its own files, granted deps and
resources, granted callees (via the gateway), and granted network egress.
Nothing else: no sibling source or secrets, no arbitrary filesystem, no
arbitrary network.

The **model is unchanged** — identities, roles, grants, gateway, vault are fixed
from phase 2 on. Tier 3 only hardens the floor *and* adds one new thing to the
grant vocabulary: **network egress**. The **editing plane** (terminals, `bx`,
git) stays fully privileged — sandboxing the owner is a non-goal.

## Recap: where we are

- **Tier 1** (now): single uid; instance tokens; gateway default-deny; vault
  broker-owned. A hostile element can still read siblings' `/proc`, open their
  sockets, read/write any workspace file, and make **any** network connection.
- **Tier 2** `[ND1]`: per-scope uids (buxond is root, D13). Source becomes
  non-writable, `/proc` closes, vault/data enforced by fs perms. Residual: an
  element can still *read* much of the tree its uid can reach, and **local
  network + internet egress is wide open**.
- **Tier 3** (this doc): mount namespace (filesystem), plus network namespace +
  an egress policy (ingress/egress). Closes the residuals above.

## Threat model / goals

A backend process is assumed potentially hostile (a bad dependency, a prompt-
injected agent, a bug). After Tier 3 it can:

- **read/write** only: its own dir, granted `deps/*` (read-only), granted
  resource files (sqlite/blob), the toolchain (read-only), a private `/tmp`.
- **be reached** only by buxond (the runtime) — never by a sibling or the LAN.
- **reach out** only to: the buxond gateway (always), plus whatever network
  egress it was **granted** (internet scope and/or specific LAN targets).

Everything else fails closed. Terminals/owner are exempt (editing plane).

## 1. Filesystem isolation — mount namespace

Each backend is spawned into its own **mount namespace** with a purpose-built
root (`pivot_root` over an overlay/tmpfs, assembled from bind mounts):

| Path in sandbox | Source | Mode |
|---|---|---|
| the component dir | `apps/<scope>/<comp>` | **ro** (source is terminal-only from tier 2; here also invisible to others) |
| `deps/<x>` | each granted dep component | **ro** |
| granted resource files | `data/resources/<scope>/<name>.{sqlite,blob…}` | **rw**, only the granted names |
| toolchain | `/opt/toolchains` | **ro** (node/python need interpreter+stdlib; Go backends are static → need ~nothing) |
| `BUXON_GATEWAY` socket | `.buxon/run/gateway.sock` | bind (the one door to buxond) |
| its own listen socket dir | `.buxon/run/<compkey>/` | rw |
| `/tmp`, `/dev` (null/zero/urandom), `/proc` | fresh tmpfs / minimal / private | — |

**Hidden by construction:** other components' source, `home/`, `data/vault/`,
`.git`, the rest of `data/` and `.buxon`, the host root. The backend's entire
visible filesystem *is* its grant set — reading a sibling's secrets or source
is no longer a permission question, it's simply not mounted.

Rootless note: with a **user namespace** the mount ns + binds need no host
capability; otherwise `CAP_SYS_ADMIN`. See §4.

## 2. Ingress — only the runtime can go in

Already most of the way there: a backend serves on a **unix socket**
(`BUXON_SOCKET`) in its private run dir, and buxond's proxy dials it. Tier 3
finishes it:

- Inside the **network namespace** (§3) there is no external interface, so any
  `listen()` a backend does binds only to an unreachable loopback — **nothing on
  the LAN or from a sibling can connect to it**. buxond reaches the backend
  through the bind-mounted unix socket, from outside the ns.
- The socket is owned by the component's uid with a private parent dir, so even
  a same-host, same-uid confusion can't open a sibling's socket.

Net: the only path *into* a component is buxond. "Only the runtime can go in."

## 3. Egress — default-deny IP, grants, gateway always

Spawn each backend into a **network namespace with only loopback** — no route to
the host, LAN, or internet. Then egress resolves in three classes:

1. **To buxond (the gateway).** It's a **unix socket**, bind-mounted into the ns
   — unaffected by the empty netns, so component↔component calls and the buxon
   RBAC APIs work exactly as today. *This is "egress to bx generally allowed."*
2. **To an IP (LAN or internet).** The empty netns means the kernel has nowhere
   to send it → **default deny**. To permit it, buxond runs a **transparent
   userspace egress relay** bound to the ns (a tap device terminated by an
   in-process Go TCP/IP stack — gvisor `tcpip`/`slirp4netns`-style). Every
   outbound connection the backend makes is caught by the relay, checked against
   the component's **egress policy**, and either dialed from the host and spliced
   byte-for-byte, or refused (RST). Transparent ⇒ works for **any** binary or
   library, no `HTTP_PROXY` cooperation needed. DNS is answered by the relay,
   which then applies policy to the resolved address (or matches hostname rules).

### Egress grant vocabulary

New grant **targets**, requested in `buxon.json` `uses` and **approved by the
owner** in the grants panel exactly like a resource grant — an element can
**never** self-approve (AGENTS.md / auth.md §3):

- **`net:internet`** — the internet **scope**: any *public* destination
  (non-RFC1918, non-loopback, non-link-local, non-ULA). Optionally port-scoped:
  `net:internet:443`. Coarse, all-or-nothing internet — a single capability.
- **`net:<cidr|host>[:port]`** — LAN / specific hosts, **per address/subnet/
  port**: `net:192.168.1.0/24:5432`, `net:10.0.0.5:22`, `net:db.internal:5432`.

Crucially, `net:internet` **excludes** RFC1918/LAN ranges — reaching the local
network always needs an explicit `net:<cidr>` grant. So "internet" and "LAN" are
**separately grantable**, as required. The relay classifies each destination and
allows it iff some granted rule matches; otherwise deny. Grants surface as
pending on tile import / template instantiate, like every other cross-scope use.

```jsonc
// buxon.json
"uses": [
  { "target": "net:internet:443",        "role": "egress" },  // HTTPS out
  { "target": "net:192.168.1.10:5432",    "role": "egress" }   // one LAN db
]
```

### Interim mechanism (Tier 3a) — nftables per-uid

If per-scope uids (tier 2) are on and buxond holds `CAP_NET_ADMIN`, egress can be
enforced **without** a netns/relay using **`nftables meta skuid` owner rules**:
default-drop for scope uids; generate allow rules from the `net:*` grants
(`net:internet` = drop RFC1918 + allow the rest; `net:<cidr:port>` = allow that).
Kernel-enforced, no userspace stack. Trade-off: granularity is **per scope**
(uids are per scope), not per component, and no transparent DNS. Recommended as
the pragmatic first cut; the netns+relay (Tier 3b) gives **per-component** policy
and is rootless-capable.

## 4. Deployment & capabilities

The default container runs with **no extra capabilities** (deployment.md) — that
stays **Tier 1** (unprivileged, attribution-only). Tier 3 is **opt-in** and needs
one of:

- **User namespaces (rootless)** — mount + net ns + tap without host caps.
  Preferred long-term; matches where rootless containers are going.
- **`CAP_NET_ADMIN` + `CAP_SYS_ADMIN`** (or a small privileged helper) for the
  classic root path with real veth/netns.

Gate it like `--scope-uids`: e.g. `--isolate` selecting the tier. `--dev` /
`--no-auth` ⇒ Tier 0, no sandbox. Ship a **hardened `docker-compose`** variant
documenting the added caps / userns config alongside the default one.

**Overhead:** building a ns per spawn taxes the hot-reload loop. Mitigate by
**reusing a per-scope namespace** across generations (the scope's uid/ns is
stable; only the process restarts), or a pre-forked sandbox helper. Measure
before choosing.

## 5. Interactions & edge cases

- **Resources.** Same-scope sqlite is still handed as a file path — it must be
  the rw bind mount inside the ns. Cross-scope resources stay **brokered** over
  the gateway (unchanged, and now the *only* cross-scope path).
- **Build vs runtime egress.** `go build` / `npm i` / `pip` run in the
  **editing/build plane** (buxond + terminals), **not** in the sandboxed runtime.
  Module downloads are therefore governed by the container's network, not by a
  component's runtime egress grants — a component with no `net:*` grant still
  builds fine; it just can't phone home *at runtime*.
- **Terminals** are unsandboxed (owner power) — deliberately outside this.
- **DNS / IPv6 / UDP (QUIC).** The relay must handle all three; call it out in
  the implementation, don't hand-wave it.
- **Browser-plane isolation** (subdomain-per-scope, auth.md §9 tier 3) is
  orthogonal (frontend origin) and not covered here.

## 6. Phasing

1. **Tier 3a** — mount-ns filesystem isolation per component (on tier-2 uids) +
   nftables per-uid egress with the `net:*` grant model. Opt-in `--isolate`,
   needs caps. Delivers "can't read the tree / can't phone home" fast.
2. **Tier 3b** — netns-per-component + transparent userspace egress relay
   (per-component policy, DNS, rootless via userns). The full picture.
3. **Ingress hardening** (socket perms/dir audit) + hardened-deployment docs and
   compose; wire egress grants into the Tile Manager / Admin approval UI.

## 7. Decisions to record (ISO-*)

- **ISO-1 — Egress mechanism**: netns + transparent userspace relay
  (per-component, rootless-capable, DNS-aware) as the target; nftables-per-uid
  (simpler, per-scope, kernel-enforced) as the interim. Phased, not either/or.
- **ISO-2 — Egress grant syntax**: `net:internet[:port]` (the internet scope) and
  `net:<cidr|host>[:port]` (LAN/specific), in `uses` at role `egress`; internet
  and LAN separately grantable; owner-approved, never self-approved. `net:internet`
  never covers RFC1918.
- **ISO-3 — Capability model**: default container stays unprivileged (Tier 1);
  Tier 3 is opt-in and needs user namespaces (preferred) or CAP_NET_ADMIN/
  CAP_SYS_ADMIN. Documented as a separate hardened deployment.
- **ISO-4 — Namespace lifecycle**: reuse a per-scope namespace across backend
  generations vs fresh per spawn (perf vs blast-radius) — measure, default to
  per-scope reuse.
- **ISO-5 — Filesystem view**: sandbox root is exactly {own dir ro, granted deps
  ro, granted resource files rw, toolchain ro, gateway socket, private tmp/dev/
  proc}. Everything else unmounted, not merely unreadable.

This is the runtime-plane counterpart to `tile-sharing.md` (which governs *how
components arrive*): once you can import a stranger's tile, Tier 3 is what makes
running it safe.
