# The sandbox-visibility branch (D112 landed, D113 designed) and what it means here

> Status: live — research note for the dev-lifecycle design
> ([../README.md](../README.md)). It describes the `sandbox-visibility`
> branch at `06c6519` (2026-09-27), which is expected to merge into master
> **before** this design is implemented. The design branch is based on it,
> so file:line references in this set are against master + that branch.

## What the branch lands (D112)

**One in-memory registry of every sandbox xbind runs** (`internal/sbx/sbx.go`).
- **How it is built:** whoever starts a sandbox adds an entry and removes it
  when the process ends. The package imports nothing of xbin's, and a nil
  registry is valid.
- **Entry fields:** `ID`, `Kind` (`backend | terminal | agent | tile`), `Tile`
  (the owning component), `Parent` (reserved for tile-managed sandboxes),
  `User`, `Label`, `Mode` (`vm | namespace | host`), `Accel` (`kvm | emulate`),
  `MemMiB`/`VCPUs` (a VM's reservation), `PID`, `Gen`, `Started`, `Leaf` (the
  cgroup leaf), `Disk`, `Net`, `Restricted`.
- **Filters:** `Filter{Tile, User, Kind}`.
- **The failure ring:** 64 entries; identical failures within 10 minutes are
  coalesced. Stages are `refused | start | health | exit`. `sbx.Refuse`
  marks policy, budget and availability refusals.

**Backends** (`internal/runner/sbx.go`):
- One entry per generation while its process lives. The ID is
  `backend:<CompKey>:g<gen>`, so blue/green lists two entries sharing one
  cgroup leaf, `Leaf = CompKey(path)`.
- `sbxAdd` is called after start, and the entry is removed in the wait
  goroutine. `sbxFail` records start and health failures. `sbxExited`
  classifies shim exit 125 and init exit 127.
- `runner.go` gains `Sandboxes *sbx.Registry`. `waitHealthy` moved to
  `health.go` and now also watches the process exit.

**Terminals and agents** (`internal/term/sbx.go`) are listed by session id.
Agent restarts unlist before they relist.

**VM reservations charged to a tile.**
- `vm.Manager.Reserve(owner, mem)` keeps the global count and budget
  unchanged and books each VM to its owner (`byOwner`/`UsedBy`).
- The VM probe is cached for 5 s. `Manager.Health()` reports which VMM runs
  and what each lacks (`internal/vm/manager.go`, now 394 lines).

**The admin surface.**
- `GET /api/xbin/sandboxes?tile=` (`internal/boot/sandboxes.go`) returns:
  - every sandbox, with live stats;
  - `disks[]` and `failures[]`;
  - `health.isolation` and `health.vm`, including `usedBy:{<tile>:{vms,memMiB}}`.
- `/status` backend rows gain `sandbox` (the mode) and `vm?`.
- The admin console gains a runtime → sandboxes tab (`tiles/admin/tabs/sandboxes.js`).
- A harness pass: `passes/sandboxes.js`.

**Budgets on the branch** are unchanged where it matters: `runner.go` 831/850,
`term.go` 948/950, `cmd/bx/main.go` 1150/1150, `web/bx-frame.js` 866/900.
`boot.go` grows to 722.

## What the branch designs (D113, `plans/tile-sandboxes.md`, not built)

**A tile owns a set of named sandboxes** that xbind launches as siblings (never
nested, D82), independent of its backend's generations. They are listed in
the registry as kind `tile` under their owner.

**Three gates:**
- an admin-approved `cap:sandboxes`;
- a workspace sandboxes policy: `.xbin/sandboxes/policy.json`, with a kill
  switch, per-tile caps (8 sandboxes, 4 running, 8 GiB, 8 vCPUs, 100 GiB) and
  per-sandbox sizes;
- for VM mode, `vm.Policy.tiles` with a `tilesBudgetMiB` sub-budget.

**One exec protocol for both modes:**
- `bx __sbx-agent` is PID 1 in namespace mode, reached through `Spec.AgentFD`,
  an inherited listener fd that xbind owns. That retires D74's "no second
  process after init" rule for tile sandboxes.
- In VM mode the shim is a resident router.
- Execs have output rings, cursors, stdin, signals and resize.
- File streams (`get`/`put`/`stat`) run inside the sandbox, so xbind never
  resolves a sandbox-supplied path on the host.

**Mounts only from the tile's own reach:**
- a `filesystem` resource the tile holds (`res:<scope>/<name>`; writer =
  read-write, reader = read-only; sqlite refused);
- `{"source":true}`: the tile's own directory, read-only;
- later, `code:` mounts and scratch volumes;
- never homes, the workspace view, `.xbin`, `data/`, the gateway socket, a
  token, vault contents or devices.

**Everything else:**
- **Egress:** `none` (the default), `inherit` (the tile's `EgressFor`), or a
  subset of it. A relay is always present.
- **API:** `/api/xbin/sandboxes`, driven by the tile's backend (its instance
  token; frame and terminal tokens get 403). Chunked NDJSON with cursors,
  which resume across blue/green.
- **Human attach:** a tile-minted ticket over `/ws/term`.
- **Storage:** definitions persist in the tile's `data/sandboxes.json`; state
  lives in `.xbin/sbx/<CompKey>/<name>/`. Processes don't survive a restart.
  Idle stop has no tickers.
- **Later:** a nested per-tile cgroup.

## What this means for the dev lifecycle

**Owner's direction (2026-09-27):** tile deployments are **not** built on
tile-managed sandboxes. The dev lifecycle does, however, change sandboxing
mechanics in the **shared layer just above** them: the launch spec, the
registry, the cgroup and VM books that backends, terminals and (later) tile
sandboxes all use. Concretely:

1. **The registry gains a deployment dimension.**
   - Backend entries record the deployment (the entry ID and a `Deployment`
     field; a `Filter.Deployment`).
   - `main` keeps today's ID shape, so the admin tab and `/sandboxes` stay
     unchanged for zero-state tiles. New fields go on rows, never as new keys.
   - Non-primary deployments' generations are listed under their tile.
2. **Budgets are charged to the tile.**
   - VM reservations of every deployment book to the owning tile:
     `Reserve(owner=tile)`. A deployment is never a separate budget owner.
   - The per-tile caps a later nested cgroup enforces cover the tile's
     deployments and its tile-managed sandboxes alike.
3. **A per-tile cgroup parent.**
   - Today there is one leaf per tile (`CompKey`), shared by blue/green.
   - Deployments need a leaf each: otherwise a dev generation's exit could
     remove the shared leaf, or share the primary's memory cap.
   - This is the "nested per-tile cgroup" D113 defers. Both projects want it,
     so it belongs in the shared layer, built once.
4. **Backend launch-spec binds.**
   - A pinned or non-primary backend binds its materialized checkpoint at the
     tile's canonical path (`Src ≠ Dst`, which the sandbox init already
     supports).
   - It binds its deployment's data namespace at the primary's resource paths,
     so `XBIN_RES_*` values stay identical.
   - Today's work-tree bind (`runner.go` `sandboxCmd`) stays for the live
     reload target.
5. **confine binds for builds.** A confined Go build of a checkpoint needs the
   checkpoint bound at the tile's canonical path inside the build sandbox.
   `confine.Cmd` binds `Dir` at itself today, so it needs an explicit
   destination field.
6. **Streaming for the deploy-remote rung.** `internal/confine` buffers output.
   D113's exec protocol (output rings, cursors, stdin) is the precedent for a
   streaming confined run, which `git receive-pack` needs. It is used as a
   mechanism to reuse, not as a tile sandbox.
7. **Interplay with tile-managed sandboxes, once D113 is built.** Tile
   sandboxes belong to the tile's **deployment**, like its other state:
   - a non-primary deployment's backend addresses its own set of sandbox
     definitions and state, keyed per deployment like dormant registrations,
     never the primary's;
   - `{"source":true}` mounts that deployment's code: its checkpoint, or the
     work tree for the live reload target;
   - resource mounts resolve in the deployment's data namespace;
   - `cap:sandboxes` stays tile authority;
   - per-tile caps are shared across deployments;
   - a tile's own-sandbox egress `inherit` inherits the deployment's effective
     net edge.
