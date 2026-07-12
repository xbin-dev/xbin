# Interfaces — typed capability wiring (request · provide · bind)

Supersedes the flat capability-grant strings (`net:*`, `gpu:*`, `res:*`) with one
uniform model. A component **requests** typed interface slots; a **provider**
(a xbind builtin or a tile) satisfies them; the **owner binds** each request to
a provider. The binding *is* the authorization (owner-gated; agents can't
self-bind), so default-deny is preserved. This unifies network egress, GPUs,
resources, and service dependencies (LLM/HTTP), makes providers swappable, and —
for networking — makes middlebox chaining *emergent from the binding graph*
rather than a first-class "chain" object.

Grounding: xbind is already the middlebox (the gVisor egress relay in
`internal/sandbox/relay` terminates, decides, and meters every flow). This makes
that middlebox pluggable and stackable, with the plugins being tiles.

## Model

- **interface slot** = `{name, kind, …params}`. A component's `xbin.json`:
  - `interfaces`: slots it **requests** (its dependencies).
  - `provides`: slots it **provides** to others.
- **binding** = `(component, slot) → provider`. Provider is a builtin id
  (`internet`, `host`, `lan:<cidr>`, `gpu:0`, …) or a component path
  (`apps/firewall`). Stored in the workspace manifest; only the owner/admin edits.
- **resolution**: at spawn xbind resolves each requested slot's binding and
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

- **Builtin providers** (xbind, *implemented as bindable providers*): `internet`
  (gVisor relay, public-only), `host` (share host net = HostNet), `lan:<cidr>`
  (relay under a LAN policy). `bx bind <comp> net=internet` etc.; unbound =
  default-deny. Egress is *only* this binding — there is no `net:*`-in-`uses`
  grant (removed); the `internet`/`lan` builtins still resolve internally to the
  `net:internet` / `net:<cidr>` relay *policy* language (the mechanism).
- **Tile providers** (`provides: {…: {kind:"net"}}`): a firewall / VPN / router /
  DPI / meter tile. xbind gives the provider **one TUN per bound client** (a
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
  - **Capability**: building that dataplane (routing tables, `ip_forward`,
    `AF_PACKET`) needs the network-admin capabilities the sandbox drops from
    every ordinary backend (DECISIONS D18/D18a). A provider tile therefore
    declares `uses {target:"cap:net-admin", role:"writer"}`; it's **admin-only
    to approve** (a reserved grant, never same-scope auto-granted) and shows
    up pending on import. With it the sandbox keeps CAP_NET_ADMIN /
    CAP_NET_RAW / CAP_NET_BIND_SERVICE **inside the tile's own network
    namespace only** — nothing reaches the host — while still dropping every
    other cap and applying the backend seccomp block-list. Without it the gate
    setup fails with "operation not permitted".
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
role:"writer"}`. Binding → the provider URL is injected as `$XBIN_IFACE_<slot>_URL`
for a backend and `xbin.iface(slot).url` (a `xbin-interfaces` meta) for a
frontend, and the **binding is also the call grant** (it grants the requester the
provider's declared `role`, so RBAC passes — no separate `uses`). Providers are
**swappable behind a service contract** (DI: swap Ollama ↔ an OpenAI proxy with no
requester change). No TUN — it reuses the gateway/RBAC. The builtin `llm-gw` +
`chat` tiles use this. HTTPS stays opaque to a proxy/DPI unless its CA is bound
into the client's trust store (a separate explicit toggle, deferred).

Recognised service contracts are just conventions on the `service` string.
Besides `openai`, the builtins use **`mcp`** — a Model Context Protocol server
(modelcontextprotocol.io): the provider serves the JSON-RPC Streamable-HTTP
endpoint at `/mcp` under itself, and a bound requester (the `chat` tile, a
`multi:true mcp` slot) offers its tools to the model. Same binding-as-grant,
same swappability.

### `resource` — xbind builtins *(designed, not implemented)*

kv / blob / bus / cron / sqlite reframed as `resource` interfaces provided by
xbind builtins; `res:*` grants become bindings. Migration, later.

## Multiplicity — multi-bind slots and provider instances *(implemented)*

Two real cases the 1:1 slot→provider model can't express:

- **Multiple inputs, dynamic N**: a communication-agent wants *all* the owner's
  comm channels on one slot — 1 Slack + 2 email accounts *this* workspace,
  different in the next. Distinct named slots (`slack:`, `email:` — which work
  today) require knowing the shape at authoring time; "a set of things
  satisfying one contract" doesn't.
- **Multiple outputs (instances)**: one imap-connector tile serves several
  accounts — `apps/imap#abc`, `apps/imap#def`. `provides` is static manifest
  data, but accounts are runtime config; the provider exposes N concrete
  instances of one provided contract, and a binding addresses a specific one.

### Request side: `multi` slots (explicit, http-only)

```jsonc
"interfaces": { "channels": { "kind": "http", "service": "comm", "multi": true } }
```

The requester must **explicitly declare** multi-input support; a `multi` slot
holds an ordered set of 0..N bindings. Workspace storage widens
`bindings[comp][slot]` from `string` to string-or-array (custom unmarshal —
every existing manifest parses unchanged; singles keep the string form on
disk). Non-multi slots keep exactly today's semantics and injection. `multi`
is **http-family only**: a multi `net` slot would mean multiple default
routes (rejected at bind time).

### Provider side: instances (a map, no labels)

```jsonc
"provides": { "email": { "kind": "http", "service": "comm", "role": "writer",
                          "instances": true } }
```

`instances: true` marks the provide as a **template**; the provider registers
concrete instances at runtime (they're config, not manifest):

```
PUT /api/xbin/iface-instances    (self-scoped; admin may pass {component})
{ "instances": { "abc": "/accounts/abc", "def": "/accounts/def" } }
```

Instances are a plain `map[id]pathPrefix` with **provider-relative** prefixes
(absolute `/api/…` registrations are rejected at PUT — they'd double the
composed prefix, and persisted install paths go stale on rename/clone); the
`provider#id` ref **is** the display form everywhere (bindings, bind options,
`bx iface`, env). The path prefix is provider-defined: the injected URL is
`/api/<provider><prefix>`, which is what lets a **non-instance-aware
requester bind to an instance like any other provider** — each instance
presents itself as a first-class option implementing the base contract, and
the requester just calls its URL. Registration replaces the whole map and
re-wires bound requesters; a vanished instance leaves its binding in place
but unresolved (no endpoint injected) — surfaced, never silently rewired.

### Binding grammar and resolution

Binding ref = `provider[#instance]`. `#instance` is required when the provide
declares `instances: true` and invalid otherwise. Binding-as-grant is
unchanged — the role comes from the provide def; the instance selects the
URL/identity, not the privilege (RBAC is per-component; all instances of a
provider grant the same role).

Injection:

- single slot (unchanged): `XBIN_IFACE_<slot>_URL`, plus
  `XBIN_IFACE_<slot>_INSTANCE` when bound to an instance.
- multi slot: `XBIN_IFACE_<slot>` = JSON array
  `[{provider, instance?, url, service}]`; frontends get the same via
  `xbin.iface(slot)` → `{service, multi, endpoints}`. Order = binding order;
  `provider#instance` is the stable per-channel key.

Validation at bind time: multi sets only on `{kind:http, multi:true}` slots;
instance refs only against registered instances; bare refs rejected where the
provide exposes instances; DAG/net semantics untouched.

### UX / CLI

- Instance options appear in bind pickers as `provider#id` (# syntax
  everywhere — no separate labels to carry around).
- A `multi` slot's control is simply a **multiselect** (admin Interfaces tab
  and the bind-on-install prompt); posting replaces the slot's whole set.
- `bx bind <comp> <slot>=<ref>` (replace), `<slot>+=<ref>` (add),
  `<slot>-=<ref>` (remove); `bx iface` lists registered instances per
  provider.

### Decisions

- **IFACE-6** — request slots gain `multi: true` (http-only): the requester
  opts in explicitly; an ordered binding set on one slot; storage
  string→string-or-array, backward-compatible; non-multi slots unchanged.
- **IFACE-7** — provides gain `instances: true`: runtime-registered instances
  as a plain `map[id]pathPrefix` (no labels; `provider#id` is the universal
  form), persisted in the workspace manifest; the instance selects
  URL/identity while the provide's role stays the grant; instances are
  first-class bind options so instance-unaware requesters connect unchanged;
  a vanished instance unresolves loudly instead of rewiring.

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
2. **`http`/service family** *(done)* — URL injection (`$XBIN_IFACE_*_URL` /
   `xbin.iface`) + binding-as-grant; `llm-gw`/`chat` use it. Still to do: the
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
- **IFACE-3** — `net` tile-providers get **one TUN per client**; xbind does a
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
`cmd/xbind` wiring · `cmd/bx` (`bind`/`iface`) · `internal/server` (bindings
API + `pending` bind-on-install list) · `web/bx-bindings.js` (the bind-on-install
prompt) · `examples/netrouter` (a router skeleton) · `builtin-tiles/egress-approver`
(the worked programmable-provider + admin-UX example).
