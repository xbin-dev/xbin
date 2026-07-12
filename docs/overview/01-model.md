# The core model

xbin is a self-modifying browser workspace: every piece of UI on screen is
backed by a directory you can open a shell into and edit live — including the
workspace's own chrome. It takes Jupyter's "the document is the program"
premise and applies it to whole applications with real compiled backends, and
takes Smalltalk's self-hosting image and rebuilds it on a plain Unix directory
tree, where `mv`, `cp -r`, and `git` are the management UI. This page is the
top of the overview series: the shapes and principles everything else composes
from.

**Related:** [00-index.md](00-index.md) · [02-workspace.md](02-workspace.md) ·
[03-components.md](03-components.md) · [05-identity.md](05-identity.md) ·
[06-authorization.md](06-authorization.md) · [08-sandbox.md](08-sandbox.md) ·
/docs/elements.md · /docs/auth.md · /docs/isolation.md · ARCHITECTURE.md ·
plans/DECISIONS.md

## Three levels

```
Workspace  →  Scope  →  Component
(container)   (app)     (element)
```

| Level | What it is | What it owns |
|---|---|---|
| **Component** | a directory (a view, and/or a backend, and a manifest) | its files, its API surface, its declared requests (`uses`, `interfaces`, `exposes`) |
| **Scope** | a directory subtree marked by `scope.json`, grouping components into an "app" | a resource namespace (`res:<scope>/<name>`), an import-map extension, and a **trust unit**: components in one scope auto-grant each other's declared uses (ND5) |
| **Workspace** | the whole tree — one daemon, one host, one data dir | the grant/binding table, users & orgs, brokered resources, the outer trust boundary |

A **tile** is not a fourth level — it's the workspace-shell word for a
component the human sees and arranges. Every tile is a component; not every
component is surfaced as a tile.

## Directory = component = identity

A component's ID **is** its workspace-relative path
(`apps/calendar/widgets/month-view`). There is no registry to keep in sync
and none to drift:

- `mv` is rename. `cp -r` is fork. `rm -r` is delete.
- Nesting is composition: a shell in the parent trivially sees children.
- git is the version history; each component is additionally its own repo
  ([02-workspace.md](02-workspace.md) §Git model).

A central UUID registry was considered and rejected (ARCHITECTURE.md
§Alternate takes): path-as-identity is what keeps standard Unix tools — and
therefore agents — first-class operators of the system.

## The self-hosting rule

Nothing privileged is uneditable. The root page (`GET /` redirects to
`/c/root/`), the workspace shell (`shell/`), the admin console
(`tiles/admin`), the tile manager, the API-docs browser — all are ordinary
components in the tree, served, granted, and editable exactly like anything
you build. Open a terminal on the shell and change how your workspace looks
while you're looking at it. This is the property that makes xbin a workspace
rather than a dashboard product, and it is load-bearing for agents: the
platform's own UI is reachable by the same file edits as user code.

The flip side: chrome components hold no special power *because* they are
chrome. The admin tile can administer the workspace only because it holds an
owner-approved `xbin: admin` grant like any other element would
([06-authorization.md](06-authorization.md)).

## Two planes

Everything in xbin belongs to one of two planes (plans/auth.md):

- **The editing plane** — terminals, `bx`, git. Humans (and agents driving
  shells) changing *files*. Scoped per tile and per user, guarded by tile
  access levels and the terminal sandbox ([09-terminals.md](09-terminals.md)).
- **The runtime plane** — running elements calling each other. Default-deny,
  identity-carrying, role-scoped. An element can call exactly its own API
  plus whatever it was explicitly granted.

The planes meet only at the filesystem: editing changes files; xbind watches,
rebuilds, and hot-swaps the runtime ([03-components.md](03-components.md)).
Editing is deliberately *not* mediated by xbin — any editor, any tool, any
agent CLI works, and xbind only reacts to the resulting file changes.

## The security philosophy: default-deny, one spine

Every capability in the system follows the same three-step shape:

```
declare (agent-authored manifest)  →  approve (owner/admin)  →  enforce (xbind, at a chokepoint)
```

1. **Declarations are inert.** A component may declare anything in its own
   `xbin.json` — API roles it exposes, grants it wants (`uses`), interface
   slots it requests, endpoints it offers the outside (`exposes`). None of it
   does anything by itself.
2. **Approval is the owner's, and it is the record.** Approved grants and
   bindings land in the machine-managed workspace `xbin.json` — a visible,
   versioned, git-diffable capability table. For interfaces and ingress, *the
   binding is the authorization* (IFACE-1, ING-1): there is no separate
   permission object to drift from the wiring.
3. **Enforcement happens on one narrow spine.** Every cross-element call —
   from a browser frame, a backend, a terminal, cron, or the public internet —
   flows through xbind's authenticated gateway/proxy, which **strips every
   inbound `X-XBin-*` header and injects the verified identity**
   (`X-XBin-From`, `X-XBin-Role`). Callees never authenticate anything
   themselves; if the headers are present, they are true
   ([05-identity.md](05-identity.md)).

Default-deny is preserved at every layer of the same model: an unbound `net`
interface means **zero** egress; an unbound `exposes` slot means the endpoint
is unreachable from outside (ING-1); an ungranted `uses` means 403. Scopes
are the one deliberate softening: components within a scope are one trust
unit, so their declared uses of each other auto-grant (ND5) — an app never
needs the owner's blessing to talk to itself.

Workspace/org **policy ceilings** (D20) sit above all of it: pattern-keyed
rows that cap what covered tiles may ever be granted (net, gpu, xbin
capabilities, ingress), enforced both at approval *and* at every evaluation —
so even a hand-edited grant table cannot exceed the ceiling
([07-users-orgs.md](07-users-orgs.md)).

## Files are truth

Two kinds of manifest, two kinds of author:

| File | Author | Nature |
|---|---|---|
| `<component>/xbin.json` | agents/humans, by hand | jsonc, comments welcome; **requests and declarations**; parse errors are non-fatal (surface in `bx doctor`/status) |
| workspace `xbin.json` | xbind, machine-managed | rewritten whole (atomically) on every change; **approvals and wiring**: grants, bindings, lifecycle, registrations; comments do not survive |

Both are plain files in the tree. Nothing important hides in a database:
state you can't `cat` is limited to derived caches (`.xbin/`) and brokered
data (`data/`) — see [02-workspace.md](02-workspace.md) for exactly who
writes what.

## The no-build frontend

The frontend is plain ES modules + import maps: no bundler, no transpiler, no
TypeScript in the core — ever. Vendor dependencies (lit, xterm, marked) are
vendored into `vendor/` so a workspace is self-contained and offline-capable.

Because components are served as their own iframe documents, xbind performs
exactly **one sanctioned HTML transform** (D4): injecting the merged import
map, component metadata, a short-lived frame token, and `xbin-client.js` into
`<head>` of served component HTML (opt-out per component with
`"inject": false`). No other rewriting exists, and none may be added. This
single constraint is what keeps `index.html` semantics — relative URLs,
per-document import maps, script execution — intact without a compiler in the
serving path, and what makes the frame token (the browser-side identity
mechanism) injectable at all ([04-frontend.md](04-frontend.md),
[05-identity.md](05-identity.md)).

## Minimal middlebox; fat plugins are tiles

xbind stays a small broker/router. Anything with policy weight, vendor
surface, or an opinion becomes a **sandboxed tile** wired in through typed
interfaces ([11-interfaces.md](11-interfaces.md)):

| Infrastructure | Shipped as | Bound via |
|---|---|---|
| default-deny firewall with human approval | `egress-approver` builtin tile | other tiles' `net` interface → it (a net **provider**) |
| LLM gateway / API keys | `llm-gw` builtin tile | `http` service interfaces (`service: "openai"`) |
| public HTTPS + ACME certificates | `traefik` builtin tile | tiles' `exposes` → it (an ingress **terminator**, ING-3) |
| backup storage | `s3-archiver` builtin tile | the `@archive` binding (LC-3) |

The daemon carries the *mechanism* (splicing packets, routing hosts, moving
tars); the tile carries the *policy and lifecycle* (approval UIs, ACME
accounts, retention). A compromised infrastructure tile is contained by the
same sandbox and grants as any other tile — which is precisely why ACME, for
example, is never allowed into the daemon (ING-3).

## Enforcement honesty

xbin's security claims are tiered, and the documentation is required to state
only what the OS actually enforces at the tier you run
(/docs/auth.md §Honesty, /docs/isolation.md):

| Tier | Flag | What's real |
|---|---|---|
| 1 | (default) | gateway identity + default-deny policy; same-uid processes — identity is soft against a local attacker |
| 2 | `--scope-uids` | per-scope uids: file grants and identity become mechanical |
| 3 | `--isolate` | per-component user/mount/pid/net namespaces over a base rootfs; default-deny egress at the packet level; terminal mount masks + Landlock read guard (D17/D18) |

Production runs tier 3. The model is identical at every tier — lower tiers
weaken enforcement, never semantics. When xbind starts as root on someone
else's workspace it drops to the workspace owner's uid first (D13).

## Organizational memory: plans/ and DECISIONS.md

The repository treats decisions as artifacts: every subsystem has a design
doc under `plans/`, and every settled choice gets a numbered entry with
rationale in plans/DECISIONS.md (D-numbers for core decisions, and per-domain
series: IFACE-*, ING-*, LC-*, ND-*). Overview and reference docs cite these
IDs inline so "why is it like this?" always has a one-hop answer — and so
agents don't re-litigate settled trade-offs.

## The composition map

```
        browser: shell / tiles / terminals            bx · SDK · curl
                │ cookie (+ frame token)                  │ bearer tokens
                ▼                                         ▼
 ┌───────────────────────────────  xbind  ─────────────────────────────────┐
 │ server — auth middleware in front of every route                        │
 │   /c/<comp>/…    static files + the ONE injection (D4)                  │
 │   /api/<comp>/…  reverse proxy → backend socket                         │
 │                  (strips inbound X-XBin-*, injects verified From/Role)  │
 │   /api/xbin/…    core API: grants, bindings, resources, users, vault…   │
 │   /ws/term       PTY sessions      /ws/events   live reload + bus       │
 │                                                                         │
 │ registry — tree scan: components, scopes, manifests                     │
 │ broker   — grants & policy ceilings, resources, vault, orgs, ingress    │
 │ runner   — build → health-check → blue/green swap → reap (D8)           │
 │ auth     — principals: owner, users, elements, terminals, cron, ingress │
 └──┬────────────┬─────────────────────────────┬──────────────────┬────────┘
    │ ingress    │ unix socket per backend     │ gateway.sock     │ PTY
    ▼ listeners  ▼                             │ (backends call   ▼
 public      ┌────────────────────┐            │  back in here)  ┌─────────────┐
 internet ─▶ │ backend sandbox    │ ×N tiles   └────────────────▶│ terminal    │
 (second     │ userns+overlay+netns             (same auth,      │ sandbox     │
 listener,   │ egress: relay / splice ─▶ net     same policy)    │ per tile ×  │
 terminator  │ ingress: DialIn  ◀─  provider                     │ per user    │
 tiles)      └────────────────────┘   tiles                      └─────────────┘
```

Every arrow into a backend or out to the network passes a policy question;
every identity on the spine is injected by xbind, never asserted by a caller.
The rest of this series walks each box: the tree on disk
([02-workspace.md](02-workspace.md)), components and their lifecycle
([03-components.md](03-components.md)), who's calling
([05-identity.md](05-identity.md)) and what they may do
([06-authorization.md](06-authorization.md)), the sandboxes
([08-sandbox.md](08-sandbox.md), [09-terminals.md](09-terminals.md)), and the
network edges ([12-egress.md](12-egress.md), [13-ingress.md](13-ingress.md)).
