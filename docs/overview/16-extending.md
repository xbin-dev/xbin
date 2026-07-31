# Extending xbin: SDK, bx & the protocol

xbin has three programming surfaces — backends (any language speaking HTTP
on a unix socket, with a small zero-dependency Go SDK for comfort),
frontends (the injected in-frame JS API), and the HTTP protocol itself,
which `bx` and every UI are mere clients of. Deeper extension doesn't fork
the daemon: infrastructure capabilities (gateways, firewalls, TLS
terminators, backup stores) are **tiles** that plug into typed interface
slots — the middlebox pattern is xbin's extension mechanism.

**Related:** [03-components.md](03-components.md) (the component contract),
[04-frontend.md](04-frontend.md) (views & `window.xbin`),
[11-interfaces.md](11-interfaces.md) (slots & binding),
[/docs/sdk.md](/docs/sdk.md) · [/docs/bx.md](/docs/bx.md) ·
[/docs/protocol.md](/docs/protocol.md) · plans/interfaces.md.

## Backends: the contract is env + a socket

A backend is any long-running process that serves HTTP/1.1 on the unix
socket xbind names. No framework, no registration call — the runner injects
everything as environment:

| Env | Meaning |
|---|---|
| `XBIN_SOCKET` | where to listen (per-generation unix socket) |
| `XBIN_COMPONENT` | own path — the component's identity |
| `XBIN_GATEWAY` + `XBIN_TOKEN` | how to call *out*: the gateway unix socket + this generation's instance credential (RBAC'd, works with zero net egress) |
| `XBIN_RES_<NAME>` | each granted resource — a dsn string, or a file/dir path for same-scope sqlite/filesystem ([10-resources.md](10-resources.md)) |
| `XBIN_IFACE_<slot>_URL` / `_ADDR` / `_IP` | resolved interface bindings: http endpoint URLs, stream dial addresses, lan-ingress own-addresses ([11-interfaces.md](11-interfaces.md), [13-ingress.md](13-ingress.md)) |

Inbound requests arrive with the `/api/<component>` prefix stripped and two
trustworthy headers — `X-XBin-From` and `X-XBin-Role` (xbind deletes inbound
`X-XBin-*` before injecting verified values; public ingress traffic arrives
as `From: ingress` plus `X-XBin-Ingress-Host`).

What the runner promises in return ([03-components.md](03-components.md)):
lazy start on first request, socket-connect health check (5 s), blue/green
swap on save with a 30 s drain (D8), stdout/stderr captured to the
component log, idle reap after ~30 min (streams hold it open), and
crash-loop braking after 3 fast exits.

### The Go SDK — `github.com/xbin-dev/xbin/sdk`

**Zero-dependency by hard rule** — components inherit whatever the SDK
depends on, so it depends on nothing but the standard library. The
workspace's generated `go.work` resolves it locally (`/opt/xbin/sdk`), so
builds work offline. The full exported surface:

| Capability | API |
|---|---|
| Serve | `Serve(h)` — listen on `XBIN_SOCKET`, drain gracefully on SIGTERM; `Self()` |
| Identity & roles | `Caller(r) → CallerInfo{From, Role, Owner}`, `CallerInfo.Ingress()`, `Role(want, h)` / `RoleFunc` middleware (403 below `want`), `RoleSatisfies(have, want)` (admin ⊃ writer ⊃ reader; custom names exact) |
| Calling out | `Client()` — an `*http.Client` through the gateway with this instance's identity; URLs use the literal pseudo-host `http://xbin` (`…/api/apps/calendar/events`). Deliberately **no overall timeout**: SSE/chunked streams run until either side closes (bound calls with a request context). `GatewayDial(ctx)` for raw protocols — e.g. WebSocket to another element with any WS library |
| Resources | `Resource(name)` (reads `XBIN_RES_<NAME>`), `KV(res)` → `Get/GetJSON/Put/PutJSON/Delete/List` (+ `ErrNotFound`), `Publish(res, topic, data)` (bus) |
| Secrets | `Secret(name)` — this component's own vault key |

A 403 from `Client()` means a missing grant: declare the target in `uses`,
get it approved. Streams to another backend die at *its* blue/green drain —
reconnect loops are mandatory.

### Other runtimes: no SDK required

The contract is small enough to speak directly
([/docs/sdk.md](/docs/sdk.md) has scaffolds; `bx new --runtime …` generates
them):

- **node** — `http.createServer(...).listen(process.env.XBIN_SOCKET)`; read
  `x-xbin-from`/`x-xbin-role`; call out via `{ socketPath:
  process.env.XBIN_GATEWAY }` with the bearer token.
- **python** — a `UnixStreamServer` + `BaseHTTPRequestHandler`; any
  unix-socket-capable HTTP client for the gateway.
- **cgi** — any executable: CGI/1.1 env (`PATH_INFO`, `REQUEST_METHOD`,
  body on stdin) plus `XBIN_COMPONENT`/`XBIN_FROM`/`XBIN_ROLE`, response on
  stdout. Per-request exec, zero lifecycle — right for shell-script
  endpoints.

## Frontends

Views get `window.xbin` injected (`xbin.self`, `xbin.fetch` for attributed
cross-element calls, `xbin.iface(slot)` for bound dependencies, bus/events,
dialogs and pop-out windows) — the whole surface is
[04-frontend.md](04-frontend.md)'s subject.

The integration contract between tiles is the **`API.md` convention**: a
component that exposes roles ships an `API.md` documenting its endpoints per
role with a copy-paste `uses` snippet. `bx api <component>` prints roles +
that file — it is how agents (and humans) learn to integrate with any tile
without reading its source. `bx doctor` flags exposing components that lack
one.

## Operating: `bx`, and the protocol under it

`bx` is on PATH in every terminal and inside every backend sandbox. It is
**convenience, not magic** — a thin client of the HTTP API using the
session's credentials (`XBIN_URL`+`XBIN_TOKEN` in terminals; the gateway
socket + instance token inside a component; the owner token on the host).
Anything bx does, curl can do. Grouped by plane:

| Plane | Commands |
|---|---|
| scaffold & inspect | `ls` · `new` · `status [--all]` · `logs [-f]` · `api` · `doctor` |
| authorization | `grants` · `grant [--revoke]` · `user` · `org` (+ `org policy`) · `team` · `access` |
| wiring | `iface` · `bind` (`slot=p`, `slot+=p[#i]`, `slot-=p`, `--unset`) · `expose` / `unexpose` · `ingress [routes]` |
| data & secrets | `vault status\|unseal\|seal\|rekey` · `vault ls\|get\|set\|rm` · `cron ls` |
| lifecycle | `enable` / `disable` · `offload [--full]` · `backup` / `backups` / `restore` · `backup-schedule` |
| sharing | `tile ls\|import` · `template ls\|new` · `builtin updates\|update` |

The protocol itself is **one API behind three doors**
([/docs/protocol.md](/docs/protocol.md) is the endpoint reference):

1. **The console TCP listener** — humans (cookie sessions), the owner token,
   terminal tokens.
2. **The gateway unix socket** (`$XBIN_GATEWAY`) — the same handler, reached
   by element backends with instance tokens; a component's only door out,
   independent of network egress.
3. **The ingress listeners** — the runtime public listener and terminator
   forward sockets, which serve *only* published tile routes as the
   anonymous `ingress` principal ([13-ingress.md](13-ingress.md)); the
   authenticated API surface structurally does not exist there.

## Building infrastructure tiles — the middlebox pattern

xbin's deepest extension point needs no daemon changes: declare a
**`provides`** slot of the right kind, and the owner can route other tiles
through you ([11-interfaces.md](11-interfaces.md)). The binding is the
authorization; your tile runs sandboxed like any other, so a compromised
middlebox is contained to what it was bound. The shipped tiles are worked
examples, each demonstrating one seam:

| Tile | Provides | Demonstrates |
|---|---|---|
| `egress-approver` | `net` | a **network provider**: a real Linux dataplane (routing, `ip_forward`, AF_PACKET tap) under the admin-only `cap:net-admin` grant, default-deny forwarding with a human-in-the-loop approve/deny UI ([12-egress.md](12-egress.md)) |
| `llm-gw` | `http` service `"openai"` (+ a `"prometheus"` metrics provide) | a **service contract**: consumers bind an `{kind:http, service:"openai"}` slot and get the writer role + URL injected — providers are swappable without callers changing; also self-config via "self is admin of itself" |
| `traefik` | `ingress` | an **ingress terminator**: ACME/Let's Encrypt TLS lives in a sandboxed tile (certs in its own resource, never the daemon), raw `:80/:443` arriving via the runtime stream relay, decrypted requests handed back through its forward door ([13-ingress.md](13-ingress.md)) |
| `s3-archiver` | `archive` service `"s3"` | a **platform-service provider**: xbind itself is the client — components' `@archive` bindings stream backup tars here (manual, scheduled, offload; [14-lifecycle.md](14-lifecycle.md)) |

(`chat` and `prometheus-viewer` round out the catalog as consumers of the
`openai` and `prometheus` contracts.) The pattern generalizes: a WireGuard
tile is `provides {net, lan-ingress}`; a mail gateway is an `http` service
with per-account `instances`; a WAF is an ingress terminator.

## Being a good citizen

Conventions the platform checks or the ecosystem relies on
(workspace-template/AGENTS.md is the in-workspace statement of these):

- **Ship `API.md`** and give every exposed role a real description — both
  render in the grant-approval UI and `bx api`; `bx doctor` flags omissions.
- **Declare, don't assume**: every cross-scope call in `uses`, every
  capability as an `interfaces`/`exposes` slot — and leave **binding to the
  owner**. Reuse existing `service` contract names (`openai`, `prometheus`,
  `s3`, …) so your tile is interchangeable; coin a new one only for a
  genuinely new API shape.
- **Runtime deps go in `setup`**, not just your terminal's dev layer — the
  deployed backend never sees the terminal overlay
  ([09-terminals.md](09-terminals.md)).
- Treat every public (`exposes`) path as hostile input — `ingress` callers
  are anonymous by design; your app owns auth there.
- If you change builder-visible behavior of shared/builtin content, land a
  `/docs/changelog.md` entry (breaking: a `/docs/changes/` migration note) —
  the contract agents rely on across upgrades.
