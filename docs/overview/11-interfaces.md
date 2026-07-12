# Interfaces & bindings

Interfaces are xbin's dependency-injection and capability system in one mechanism: a
component **requests** typed slots in its manifest, tiles (or xbind builtins) **provide**
them, and the **owner binds** each request to a provider. The binding *is* the
authorization — unbound means no capability, and nothing a component writes about itself
can change that. One model covers network egress, service APIs, raw TCP dependencies,
inbound router links, and (as a third direction) publishing to the outside world.

**Related:** [12-egress.md](12-egress.md) (the `net` family in depth) ·
[13-ingress.md](13-ingress.md) (`exposes` and ingress sources) ·
[06-authorization.md](06-authorization.md) (grants this composes with) ·
[14-lifecycle.md](14-lifecycle.md) (the `@archive` pseudo-slot) ·
reference: [/docs/elements.md](/docs/elements.md), [/docs/protocol.md](/docs/protocol.md) ·
design: plans/interfaces.md, plans/ingress.md.

## Why typed slots instead of hard-coded paths

A component that hard-codes `fetch("/api/apps/llm-gw/v1/chat")` has welded itself to one
provider, invented its own authorization story, and hidden a dependency where no one can
see it. The interface model inverts all three (decision **IFACE-1**):

- **Declared, not discovered.** Dependencies live in the manifest, where the owner, the
  admin UI, `bx iface`, and other agents can read them.
- **Swappable behind a contract.** A slot names a *service contract*
  (`{kind:"http", service:"openai"}`), not a path. Swap Ollama for a proxy tile by
  re-binding; the requester never changes.
- **The binding is the authorization.** Only the owner/admin creates bindings; an agent
  can declare wants but can never satisfy them itself. Default-deny is preserved: an
  unbound slot grants nothing — no egress, no endpoint, no reachability.

Each `kind` is a **family** with its own wiring mechanism and its own admin UX
(**IFACE-2**); new families plug into the same request/provide/bind spine.

```
     component manifest                     workspace manifest (owner-only)
  ┌────────────────────────┐               ┌──────────────────────────────┐
  │ "interfaces": {        │   bx bind /   │ "bindings": {                │
  │   "llm": {kind:http,   │  admin tile   │   "apps/chat": {             │
  │           service:...} │ ────────────▶ │     "llm": "apps/llm-gw" }}  │
  │ }                      │               └──────────────┬───────────────┘
  └────────────────────────┘                              │ resolved at spawn
                                                          ▼
  ┌────────────────────────┐                XBIN_IFACE_LLM_URL=…  (backend)
  │ provider manifest      │                xbin.iface("llm").url (frontend)
  │ "provides": {          │                + the call is GRANTED at the
  │   "openai": {kind:http,│                  provider's declared role
  │     service:"openai",  │
  │     role:"writer"}}    │
  └────────────────────────┘
```

## The three directions

| section      | declares                     | offers to           | bound by             |
|--------------|------------------------------|---------------------|----------------------|
| `interfaces` | slots this component NEEDS   | — (it's a request)  | owner → a provider   |
| `provides`   | slots it OFFERS              | other tiles         | (requesters bind it) |
| `exposes`    | endpoints it OFFERS          | the **outside world** | owner → an ingress source |

`exposes` is the ingress plane's mirror of this model — same manifest-declares /
owner-binds / unbound-is-unreachable shape, with the binding carrying the public route.
It gets its own chapter: [13-ingress.md](13-ingress.md).

## The kind families

| kind          | side            | what a binding delivers | detail |
|---------------|-----------------|--------------------------|--------|
| `net`         | request + provide | L3 egress: the netns's default route, through a builtin or a provider tile | [12-egress.md](12-egress.md) |
| `http`        | request + provide | a service URL injected into the requester; the binding is also the call grant | below |
| `stream`      | request         | a raw TCP address for a sibling's exposed port | below |
| `lan-ingress` | request + provide | an inbound link leg into a router tile's subnet (**ING-6**) | below + [13-ingress.md](13-ingress.md) |
| `ingress`     | provide only    | marks a tile as an HTTP ingress terminator | [13-ingress.md](13-ingress.md) |
| `gpu`, `resource` | — | declared in the model, still delivered as `gpu:*` / `res:*` **grants** today (**IFACE-4/5**); they fold into bindings later | [06-authorization.md](06-authorization.md), [10-resources.md](10-resources.md) |

### `net` — L3 egress

`"interfaces": { "net": { "kind": "net" } }`, bound to the `internet` / `host` /
`lan:<cidr>` builtins or to a tile that `provides {kind:"net"}` (a firewall, VPN, or
router — the middlebox pattern). No binding = an empty network namespace = zero IP
egress. A multi `net` slot is rejected at bind time (it would mean multiple default
routes). The whole family — relay, splice, provider tiles, `cap:net-admin` — is
[12-egress.md](12-egress.md).

### `http` — service contracts

The workhorse family. Requester:

```jsonc
"interfaces": { "llm": { "kind": "http", "service": "openai" } }
```

Provider (the `llm-gw` builtin):

```jsonc
"provides": {
  "openai":  { "kind": "http", "service": "openai",     "role": "writer" },
  "metrics": { "kind": "http", "service": "prometheus", "role": "reader" }
}
```

- **Service contracts are conventions on the `service` string** — reuse standard names
  (`openai`, `prometheus`, `mcp` for Model Context Protocol servers, …) so components
  stay interchangeable; invent a new one only for a genuinely new API shape. A provider
  may offer several http provides; a binding selects the provide **matching the slot's
  service**, deterministically — so a multi-provide tile like `llm-gw` grants `writer`
  through its `openai` provide and `reader` through `metrics`, never a coin-flip.
- **Binding-as-grant.** The provide's `role` (default `reader`) is what a bound
  requester holds on the provider — no separate `uses` entry, no second approval. One
  owner action wires the dependency *and* authorizes the call.
- **No network involved**: http bindings ride the gateway socket and the normal RBAC
  spine ([05-identity.md](05-identity.md)) — they work for tiles with zero egress.

**Multi-input slots** (`multi: true`, http-only — **IFACE-6**): the requester explicitly
declares it accepts a *set* — e.g. the `chat` builtin's `llm` and `mcp` slots take any
number of model providers and MCP servers. The owner's control becomes a multiselect;
the injection becomes a JSON list (below); binding order is preserved.

**Provider instances** (`instances: true` on a provide — **IFACE-7**): for providers
whose outputs are runtime *config*, not manifest data (one IMAP tile serving three
accounts). The provider registers a plain `map[id]pathPrefix` at runtime:

```
PUT /api/xbin/iface-instances   { "instances": { "abc": "/accounts/abc" } }
```

Self-scoped (a provider registers only its own; admin may pass `component`); prefixes
are **provider-relative** — xbind composes `/api/<provider><prefix>`, and absolute
`/api/…` registrations are rejected so install paths never leak into persisted state.
Each instance binds as **`provider#id`** and appears in pickers as a first-class option
implementing the base contract — an instance-unaware requester connects to one
unchanged. Registration replaces the whole map and re-wires bound requesters; a
vanished instance leaves its binding visibly unresolved, never silently rewired.

### `stream` — a raw TCP dependency on a sibling

```jsonc
"interfaces": { "db": { "kind": "stream" } }        // + bx bind apps/app db=apps/postgres#pg
```

Binds to a sibling's **exposed stream slot** (`provider#expose-slot`; the target must
declare `exposes: { pg: {kind:"stream", port:…} }` — tcp only). The requester gets an
ordinary dialable address (`XBIN_IFACE_DB_ADDR=10.0.2.2:20000`) that xbind splices into
the provider's in-sandbox port — no ports opened on the host, no shared netns, and the
binding is the authorization, exactly like http. Policy `mayCall` ceilings apply to the
target ([06-authorization.md](06-authorization.md)).

### `lan-ingress` — an inbound leg from a router tile

```jsonc
"interfaces": { "vpn": { "kind": "lan-ingress" } }   // bound to a tile providing lan-ingress
```

Gives a service tile a second, addressed link into a net-provider tile's subnet
(**ING-6**): the tile learns its stable address (`XBIN_IFACE_VPN_IP`, e.g. `10.43.0.2`),
the provider learns the client map (`XBIN_LAN_INGRESS`), and inbound traffic the
provider terminates (a VPN, say) routes straight to the tile. It is an L3 link — the
provider can reach *all* of the tile's ports and is trusted to filter. Details ride
with the ingress plane: [13-ingress.md](13-ingress.md).

## Binding mechanics

**Storage.** Bindings live in the workspace `xbin.json` (machine-managed, owner-plane):
`bindings[component][slot] = ref | [refs…]`. A ref is `"provider[#instance]"`. Since the
ingress work, a ref may carry route config — `{ref, host|zone, listen}` — for exposed
endpoints; a config-free ref still marshals as the plain string, so every pre-existing
manifest parses and round-trips unchanged.

**Who binds.** Owner/admin only — the API (`POST /api/xbin/bindings`) is admin-gated,
and AGENTS.md restates the rule for agents: declare `interfaces`/`provides`/`exposes`,
leave binding to the owner. `bx` grammar:

```
bx bind <comp> <slot>=<ref>            # replace (bind or rebind)
bx bind <comp> <slot>+=<ref>           # add to a multi slot's set
bx bind <comp> <slot>-=<ref>           # remove from the set
bx bind --unset <comp> <slot>          # clear (back to default-deny)
bx iface                               # providers, requests, current wiring
```

**Validation at bind time** (the friendly half — resolution re-checks everything at
evaluation, so a hand-edited manifest can't out-run policy):

| rule | applies to |
|------|------------|
| slot must exist in the component's `interfaces` (or `exposes`, or be an `@` pseudo-slot) | all |
| multi-ref sets only on `{kind:http, multi:true}` slots; no duplicate refs | all |
| a component can never be its own provider | all |
| provider must offer a matching provide (`net` ⇒ provides net or a builtin id; `http` ⇒ same `service`; `lan-ingress` ⇒ provides lan-ingress) | by kind |
| `#instance` refs only against instances-provides with that id registered; bare refs rejected where the provide declares instances; `net`/`lan-ingress` take no `#instance` | http/stream |
| `stream` refs must name `provider#expose-slot`, target tcp; policy `mayCall` must cover the provider | stream |
| policy ceiling: a `net` deny refuses net/lan-ingress binds; an `ingress` deny refuses expose binds — with the blocking row named | net, exposes |
| expose slots: hostname authority, listen address, and conflict rules | [13-ingress.md](13-ingress.md) |

**Rebinding restarts.** Interface wiring is materialized at spawn (env vars, TUNs,
splices), so a bind/unbind/rebind restarts the requester's backend — and the old and new
*providers* too when their client roster changed. Instance re-registration likewise
restarts bound requesters. This is deliberate: wiring changes are loud, atomic events,
not something a running backend half-observes.

**Pending binds — the bind-on-install prompt.** Every requested slot with no binding
(and every unbound exposed endpoint, flagged `expose:true`) surfaces in
`GET /api/xbin/bindings` as `pending`, each with its candidate options: the builtins for
its kind plus every tile whose provides match (service-filtered; instances expanded to
`provider#id` entries). The shell's `bx-bindings` prompt and the admin tile's
**interfaces** tab render exactly this list — importing a tile that requests `net` and
`openai` immediately asks the owner two concrete questions. (The `net` picker offers
`internet` and `host`; `lan:<cidr>` is accepted as a typed ref.)

## What the component sees

Injection is exact and captured at spawn. `<SLOT>` is the slot name uppercased,
non-alphanumerics → `_`.

| binding | backend env | frontend (`xbin.iface(slot)`) |
|---------|-------------|-------------------------------|
| http, single | `XBIN_IFACE_<SLOT>_URL=http://xbin/api/<provider><prefix>` (+ `XBIN_IFACE_<SLOT>_INSTANCE=<id>` when instanced) | `{url:"/api/…", service, instance?}` |
| http, multi | `XBIN_IFACE_<SLOT>=` JSON `[{provider, instance?, url, service}]` | `{service, multi:true, endpoints:[…]}` |
| stream | `XBIN_IFACE_<SLOT>_ADDR=10.0.2.2:<port>` — dial it like any TCP address | — |
| lan-ingress | `XBIN_IFACE_<SLOT>_IP=<own address>`; the provider side gets `XBIN_LAN_INGRESS=` JSON client map | — |
| net | no variable — the network namespace itself is the delivery | — |
| provide `ingress` | `XBIN_INGRESS_FORWARD_URL=http://10.0.2.2:8642` (the terminator's hand-back door) | — |

`http://xbin` is the gateway pseudo-host (backends dial the unix socket; the SDK's
client handles it); frontends get same-origin `/api/…` paths and fetch them directly.
Resolution never guesses: a ref whose provider vanished or whose instance was
unregistered simply yields no endpoint and shows as broken in the wiring UI.

## The `@archive` pseudo-slot

Backup and offload need "which archiver tile stores this component's snapshots" — an
owner decision with the exact shape of a binding, minus a manifest declaration. `@`
pseudo-slots fill that: `bindings["*"]["@archive"]` is the workspace default,
`bindings["<comp>"]["@archive"]` a per-component override; single-valued, owner-set
through the same API. The backup engine resolves override-then-default. See
[14-lifecycle.md](14-lifecycle.md).

## Chains, the graph, and trust

A provider is itself a requester — of its own egress, its own resources. Paths through
the binding graph therefore *compose*: a tile bound to a firewall tile bound to a VPN
tile bound to `internet` is a three-hop chain that nobody declared as a "chain"
(**IFACE-3** — chaining is emergent from the graph). The graph must stay a DAG; binding
a component to itself is rejected outright, and longer cycles are the owner's to avoid
when wiring (there is no transitive cycle check today).

The trust framing to keep in mind when binding:

- A `net` provider is a **privileged tap** — it sees and can rewrite every packet of its
  bound clients. The binding is the explicit, owner-made statement that this is wanted.
- Providers are themselves sandboxed tiles ([08-sandbox.md](08-sandbox.md)): a
  compromised provider is contained to its client links plus whatever *it* was bound to.
- http providers see plaintext only for calls made *to them*; they can't read TLS of
  traffic they merely carry (a CA-trust binding for DPI is deliberately unbuilt).

The unifying rule, one last time, because everything above is an instance of it: **a
manifest can only ever ask; the owner's binding is the only thing that gives.**
