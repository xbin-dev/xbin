# xbin — system overview

This series is the top-down tour of xbin: how the subsystems compose, why
they are shaped the way they are, and where each boundary actually is. It
complements the reference docs (which stay the endpoint/field-level truth —
every file links its own) and the design records under `plans/` (cited by
decision ID, e.g. D4, D18a, ING-5 → plans/DECISIONS.md).

**Start here** if you're meeting the system: read 01 → 02 → 03, then follow
your interest. Each file stands alone.

## The one-screen picture

```
            browser                              the outside world
     ┌────────┴─────────┐                    ┌──────────┴──────────┐
     │  workspace shell │                    │ public HTTP · tcp/udp│
     │  (tiles = iframes│                    └──────────┬──────────┘
     │   per component) │                      ingress listeners
     └────────┬─────────┘                    (runtime / terminator tiles)
              │ cookie + frame token                  │ anonymous `ingress`
══════════════╪═══════════════ xbind ═════════════════╪══════════════════════
              ▼                                       ▼
   ┌─────────────────────┐   routes by host,   ┌──────────────┐
   │ server: auth middle- │◄─ paths allowlisted ┤ route table  │
   │ ware, static+inject, │                     └──────────────┘
   │ /api proxy, /ws      │
   └──┬────────┬──────┬───┘
      │        │      │
      ▼        ▼      ▼
 ┌────────┐ ┌──────┐ ┌──────────┐     ┌───────────────────────────┐
 │registry│ │broker│ │ terminals │     │ runner: build → health →  │
 │ (tree  │ │grants│ │ (per-tile │     │ blue/green swap → reap    │
 │  scan) │ │policy│ │ dev sand- │     └─────────────┬─────────────┘
 └────────┘ │ res- │ │  boxes)   │                   │ spawns
            │ources│ └───────────┘                   ▼
            └──────┘                  ┌──────────────────────────────┐
   every cross-element call flows    │ per-component sandboxes:      │
   through the proxy: inbound        │ userns+mountns+pidns+netns,   │
   X-XBin-* stripped, verified       │ overlay rootfs, caps dropped, │
   From/Role injected                │ default-deny egress           │
                                     └──────┬───────────────┬───────┘
                                            │ egress relay  │ splice
                                            ▼               ▼
                                      internet (public   net provider
                                      -only, per-flow    tiles (routers,
                                      policy)            firewalls)
```

Three ideas carry everything else:

1. **A component is a directory.** Its path is its identity; `mv` is rename,
   `cp -r` is fork; manifests live next to code; the chrome, the admin
   console, and the infrastructure middleboxes are themselves components.
2. **Default-deny, owner-gated capability.** Nothing reaches anything —
   another tile, the network, the outside world, a GPU, a resource — until a
   manifest *declares* the want and the owner *binds or grants* it. The
   declaration is agent-writable and inert; the authorization is the owner's.
3. **One narrow spine.** Every call crosses xbind's authenticated
   gateway/proxy, which strips inbound identity headers and injects verified
   ones — so callees never authenticate anyone, and there is exactly one
   place to meter, audit, and reason about.

## The files

| # | File | What it covers |
|---|------|----------------|
| 01 | [01-model.md](01-model.md) | The core model & philosophy: self-modification, the three levels, default-deny, self-hosting, honesty tiers |
| 02 | [02-workspace.md](02-workspace.md) | Workspace anatomy on disk: the tree, reserved names, manifests, `data/`, `.xbin/`, homes, the git model |
| 03 | [03-components.md](03-components.md) | Components & backends: manifests, runtimes, the build/blue-green/reap lifecycle, deps, env layers, templates |
| 04 | [04-frontend.md](04-frontend.md) | Views & the shell: the no-build frontend, the one HTML transform, `xbin-client.js`, `<bx-frame>`, chrome |
| 05 | [05-identity.md](05-identity.md) | Principals & tokens: owner, users, elements, frames, terminals, cron, ingress; the identity spine |
| 06 | [06-authorization.md](06-authorization.md) | Roles, grants & capabilities: the grant sources, reserved targets, confused-deputy clamps, audit |
| 07 | [07-users-orgs.md](07-users-orgs.md) | Humans: users & tile levels, orgs & teams, org admins, the policy ceiling |
| 08 | [08-sandbox.md](08-sandbox.md) | The backend sandbox: tiers, namespaces, overlay, capability drops, seccomp, cgroups, the threat model |
| 09 | [09-terminals.md](09-terminals.md) | The terminal plane: per-tile dev sandboxes, mount semantics, homes, guards, base images & layers |
| 10 | [10-resources.md](10-resources.md) | Resources, vault & data: kv/blob/bus/cron/sqlite/filesystem, encryption at rest, quotas |
| 11 | [11-interfaces.md](11-interfaces.md) | Interfaces & bindings: request/provide/bind, the kind families, injection, binding-as-grant |
| 12 | [12-egress.md](12-egress.md) | Egress & net provider tiles: default-deny, the relay, splice links, `cap:net-admin`, middleboxes |
| 13 | [13-ingress.md](13-ingress.md) | Ingress: `exposes`, the two HTTP terminators, zones, the `ingress` principal, the L4 relay, hairpin |
| 14 | [14-lifecycle.md](14-lifecycle.md) | Tile lifecycle: disable/offload, backup & restore, the archiver interface, sharing, templates, updates |
| 15 | [15-operations.md](15-operations.md) | Deployment & operations: the install, systemd, boot sequence, observability, upgrades |
| 16 | [16-extending.md](16-extending.md) | Extending xbin: the SDK, backends without SDKs, `bx`, the protocol doors, infrastructure tiles |

## Reading paths

- **Building your first tile:** 01 → 03 → 04 → 16, then /docs/getting-started.md.
- **Wiring tiles together:** 06 → 11 → 10 (and 12/13 when the network is involved).
- **Security review:** 01 (§honesty) → 05 → 06 → 08 → 09 → 07 → 12 → 13; then /docs/auth.md and /docs/isolation.md for the reference detail.
- **Operating a deployment:** 15 → 14 → 10 (§vault) → 13 (§ops); then /docs/getting-started.md §deployment.
- **Understanding an agent's world** (what a shell inside a tile can touch): 09 → 05 → 06 → 02.

## Reference docs (the field-level truth)

/docs/elements.md · /docs/auth.md · /docs/resources.md · /docs/isolation.md ·
/docs/ingress.md · /docs/sdk.md · /docs/bx.md · /docs/protocol.md ·
/docs/getting-started.md · /docs/changelog.md (+ migration notes under
/docs/changes/). Design records: `plans/` in the xbin repo — every
non-obvious choice has a decision ID in plans/DECISIONS.md.
