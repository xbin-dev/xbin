# Research: the evidence base for the dev-lifecycle design

> Status: live — raw findings behind [../README.md](../README.md), kept so the
> design documents and the implementation swarm can check every "today" claim
> against the code. Facts as of master 59687bf..926046d (2026-09-27). The
> research was read-only; file:line references drift, so re-check before
> relying on one.

## How it was produced

- Three explore agents mapped the three core areas.
- The `dev-lifecycle-explore` workflow (8 researchers, run
  `wf_b2976d01-667`) mapped everything else, including prior art from the
  web.
- The reports are persisted verbatim, with headings demoted by one level.
  Structured results (surfaces, hazards, open questions) are rendered as
  lists.

## Files

| File | What it answers |
|---|---|
| [runner-hot-reload.md](runner-hot-reload.md) | Tile on disk, manifest keys, watcher, builds, runner (blue/green, crash, reaper, alwaysOn, VM), frontend reload, existing lifecycle controls, what can or can't be pinned today |
| [identity-resources.md](identity-resources.md) | Backend credentials and principals, `X-XBin-*` headers, every store keyed by path or scope, inbound routing, bindings/grants resolution, ownership and leftovers, existing "instance"-like concepts |
| [git-confine.md](git-confine.md) | Workspace and per-tile repos, every git call site, `internal/confine` API and cost, editing mounts, the PR system, the (absent) git server |
| [ui-surfaces.md](ui-surfaces.md) | Terminal window structure and title bar, events in the browser, shell/menus/tile-admin, native hooks, size budgets, harness coverage, UX conventions |
| [inbound-edges.md](inbound-edges.md) | Every inbound path into a backend, and every single-backend assumption that breaks when two copies run |
| [builder-contract.md](builder-contract.md) | Manifest keys, backend env/headers/SDK, frontend client, resources, protocol docs, compat rules, maintenance guards, agent guidance, bx; the "contract hooks" |
| [data-plane.md](data-plane.md) | Scopes, resource provisioning and keys, vault and other stores, backups, sandbox state; feasibility of per-deployment namespaces, seeding, reset, delete |
| [serving-fabric.md](serving-fabric.md) | `/c/` serving, frame and asset tokens, origins mode, snapshot serving, outbound calls, binding resolution, principal-keyed places, self-scoped APIs |
| [prior-art.md](prior-art.md) | 27 platforms: artifact vs pointer, follow/pause, promotion, data seeding, secrets, bindings per environment, inbound routing, UX, permissions, pitfalls |
| [terminology-census.md](terminology-census.md) | Every candidate word's existing meanings with verdicts (FREE / SAFE-EXTEND / TAKEN), and the qualifier syntax that is still free |
| [delivery-infra.md](delivery-infra.md) | Test infrastructure, guards a large change trips, how past features were staged (the native swarm precedent), conflict hotspots, the work-package template |
| [side-findings.md](side-findings.md) | Defects and doc drift found along the way (including a cgi sandbox escape); not part of the design |
