# Interfaces — typed capability wiring (request · provide · bind)

Supersedes the flat capability-grant strings (`net:*`, `gpu:*`, `res:*`) with one
uniform model. A component **requests** typed interface slots; a **provider**
(a buxond builtin or a tile) satisfies them; the **owner binds** each request to
a provider. The binding *is* the authorization (owner-gated; agents can't
self-bind), so default-deny is preserved. This unifies network egress, GPUs,
resources, and service dependencies (LLM/HTTP), makes providers swappable, and —
for networking — makes middlebox chaining *emergent from the binding graph*
rather than a first-class "chain" object.

Grounding: buxond is already the middlebox (the gVisor egress relay in
`internal/sandbox/relay` terminates, decides, and meters every flow). This makes
that middlebox pluggable and stackable, with the plugins being tiles.

## Model

- **interface slot** = `{name, kind, …params}`. A component's `buxon.json`:
  - `interfaces`: slots it **requests** (its dependencies).
  - `provides`: slots it **provides** to others.
- **binding** = `(component, slot) → provider`. Provider is a builtin id
  (`internet`, `host`, `lan:<cidr>`, `gpu:0`, …) or a component path
  (`apps/firewall`). Stored in the workspace manifest; only the owner/admin edits.
- **resolution**: at spawn buxond resolves each requested slot's binding and
  wires the mechanism *for that kind*. An **unbound** request = unsatisfied =
  default-deny (no egress / no GPU / no service).
- **emergent chains**: a provider is itself a requester (of its own egress /
  deps). Paths through the binding graph *are* the chains. The graph must be a
  **DAG** — a provider transitively bound to itself is a packet loop, rejected.

## Interface families (kinds)

Each `kind` is a **family** with (a) a wiring mechanism and (b) its own admin UX.
New families plug into the same model without changing it.

### `net` — L3 egress *(implemented)*

The requester's netns gets a TUN + default route; who's on the other end depends
on the binding:

- **Builtin providers** (buxond, *implemented as bindable providers*): `internet`
  (gVisor relay, public-only), `host` (share host net = HostNet), `lan:<cidr>`
  (relay under a LAN policy). `bx bind <comp> net=internet` etc.; unbound =
  default-deny. Egress is *only* this binding — there is no `net:*`-in-`uses`
  grant (removed); the `internet`/`lan` builtins still resolve internally to the
  `net:internet` / `net:<cidr>` relay *policy* language (the mechanism).
- **Tile providers** (`provides: {…: {kind:"net"}}`): a firewall / VPN / router /
  DPI / meter tile. buxond gives the provider **one TUN per bound client** (a
  point-to-point `/30` link) plus the provider's *own* egress (its own `net`
  binding), and **dumb-splices raw IP packets** `client.TUN ↔ provider.clientTUN`.
  The provider is then a real **multi-homed Linux router**: `ip_forward` + gating
  to its egress; a firewall is its ruleset, a VPN is a `wg` egress, a router is
  `ip rule`s, DPI reads packets. Two worked builtins ship: `examples/netrouter`
  (the minimal `ip_forward` pass-through skeleton) and the **`egress-approver`
  builtin tile** — a default-deny provider that gates *per destination IP* with
  human approval (forward-only policy routing so its own reverse-DNS/RDAP lookups
  stay free) and taps the client links with `AF_PACKET` to surface + meter each
  destination. It's the reference for a programmable net provider + its per-tile
  admin UI.
- **Chaining** = a provider's egress bound to *another* provider. gVisor runs only
  at the terminal `internet` binding (guest flows → host sockets); every
  intermediate hop is a raw splice.
- Mechanics: per-client `/30`; client `resolv.conf` → a public resolver (the
  terminal relay pins `:53` to the host resolver, so DNS resolves at the end of
  the path); MTU ~1400 (room for WireGuard encap); provider learns its client
  **roster at spawn** and creates that many TUNs — a roster change restarts it in
  v1 (hot-add is the optimization).

### `gpu` — devices *(builtin-only; already shipped as grants, folds in)*

Builtin providers = host GPUs (index/uuid); binding = choose device(s); mechanism
= the existing device-node + driver-lib bind (`internal/gpu`).

### `http` / service — a typed API endpoint *(implemented)*

Requester declares `{kind:"http", service:"<contract>"}` (e.g. `openai`, `s3`,
`smtp`). Providers are tiles that `provides {kind:"http", service:"openai",
role:"writer"}`. Binding → the provider URL is injected as `$BUXON_IFACE_<slot>_URL`
for a backend and `buxon.iface(slot).url` (a `buxon-interfaces` meta) for a
frontend, and the **binding is also the call grant** (it grants the requester the
provider's declared `role`, so RBAC passes — no separate `uses`). Providers are
**swappable behind a service contract** (DI: swap Ollama ↔ an OpenAI proxy with no
requester change). No TUN — it reuses the gateway/RBAC. The builtin `llm-gw` +
`chat` tiles use this. HTTPS stays opaque to a proxy/DPI unless its CA is bound
into the client's trust store (a separate explicit toggle, deferred).

### `resource` — buxond builtins *(designed, not implemented)*

kv / blob / bus / cron / sqlite reframed as `resource` interfaces provided by
buxond builtins; `res:*` grants become bindings. Migration, later.

## Binding replaces grant-approval

- `net:*` egress grants are **removed** — egress is a `net` interface binding,
  full stop (no compat sugar; a `net:*` in `uses` is ignored). `gpu:*` / `res:*`
  remain grant strings for now (they fold into bindings later; §resource,
  §gpu).
- **Owner-gated**: only the owner/admin creates bindings (`bx bind`, admin UI);
  agents declare `interfaces`/`provides` and leave binding to the owner (AGENTS.md
  restates the no-self-approve rule).
- **Default-deny preserved**: an unbound request grants nothing.

## Security

- A `net` provider is a **privileged tap** — it sees and can rewrite all traffic
  of its bound clients. The binding is the explicit, loudly-surfaced owner
  decision ("apps/dpi will see/alter all traffic from apps/x, apps/y").
- Providers are themselves **sandboxed** (only their client TUNs + their own
  egress binding), so a compromised provider is contained to what it's bound to.
- Binding graph is **DAG-validated** (no packet loops / self-chaining).
- `http` providers can't read TLS without a separate CA-trust binding.

## UX — a UX *per interface family*

The admin tile gets an **Interfaces** area that dispatches to a family sub-UI
(each family registers its own; the shell composes them):

- **net**: a **wiring graph** — nodes are components + builtin providers, edges
  are bindings; each provider node embeds its *own* config UI via `<bx-frame>`
  (the firewall's rule editor, the VPN's peers) and shows live per-link
  throughput/drops.
- **http/service**: a **service catalog + plumbing matrix** — service contracts,
  the tiles offering each, and a requester×provider matrix of bindings
  (dropdowns); define new contracts.
- **gpu**: the per-component GPU picker.
- **resource**: the existing resources view.

`bx` mirrors it: `bx iface ls`, `bx bind <comp> net=apps/fw llm=apps/ollama
gpu=0`, `bx net graph`, `bx net flows`.

## Phasing

1. **`net` packet-plane** (this change): the request/provide/bind data model +
   per-client TUN **splice** + builtin providers (`internet`/`host`/`lan`) + a
   `netfn` masquerade-router skeleton + `bx bind` + the net wiring UI. `gpu`/`net:*`
   grants keep working as builtins so nothing regresses.
2. **`http`/service family** *(done)* — URL injection (`$BUXON_IFACE_*_URL` /
   `buxon.iface`) + binding-as-grant; `llm-gw`/`chat` use it. Still to do: the
   service-catalog UX and a TLS CA-trust binding.
3. **`resource` family** — fold `res:*` into bindings.
4. Perf (veth/kernel forwarding, eBPF metering), roster **hot-add**, recursive-
   chain polish, **ingress** chains (WAF), and the parked **flow-plane** as a
   `flow` kind on the same framework.

## Decisions (IFACE-*)

- **IFACE-1** — request/provide/bind model; the binding is the owner-gated
  authorization; unbound = default-deny.
- **IFACE-2** — each `kind` is a family with its own wiring mechanism *and* its
  own admin UX; families register into a common shell.
- **IFACE-3** — `net` tile-providers get **one TUN per client**; buxond does a
  dumb L3 splice; the provider is a real router; chains are emergent (DAG).
- **IFACE-4** — builtins (`internet`/`host`/`lan` for net; GPU devices) are
  bindable system providers (net builtins *implemented*). `net:*`-in-`uses`
  egress grants are **removed** (egress is the `net` binding only); `gpu:*` /
  `res:*` remain grants for now, folding into bindings later.
- **IFACE-5** — `http`/service is implemented (URL injection + binding-as-grant,
  used by `llm-gw`/`chat`); the `resource` family and a `flow` kind are deferred.

## Touchpoints

`internal/registry` (Interfaces/Provides on the manifest, Bindings on the
workspace) · `internal/broker` (binding resolution, provider rosters) ·
`internal/sandbox` (multi-TUN provider + spliced client) · a splice pump ·
`internal/runner` (orchestrate provider+client lifecycle + splice) ·
`cmd/buxond` wiring · `cmd/bx` (`bind`/`iface`) · `internal/server` (bindings
API + `pending` bind-on-install list) · `web/bx-bindings.js` (the bind-on-install
prompt) · `examples/netrouter` (a router skeleton) · `builtin-tiles/egress-approver`
(the worked programmable-provider + admin-UX example).
