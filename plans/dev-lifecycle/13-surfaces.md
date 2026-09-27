# 13 — Surfaces: every package and file touched

> Status: live — the file-by-file inventory the implementation swarm builds from: what changes where, in which milestone, with size headroom, prep splits and conflict owners (part of [plans/dev-lifecycle](README.md))

[05-model.md](05-model.md) says what tile deployments are. This document says
where they land in the code. Every row names a file, or a named part of one,
with:

- its change;
- its milestone;
- its size against its limit;
- whether a prep split must come first;
- whether it is a conflict hotspot;
- today's code reference.

Terms follow [01-glossary.md](01-glossary.md). Other documents in the set own
the rest:

- the work packages: `14-implementation.md`;
- the wire shapes: `11-contract.md`;
- runtime, data and fabric semantics: `07-runtime.md`, `08-data.md`,
  `09-fabric.md`;
- verification: `15-test-plan.md`.

Where this inventory needs a decision that none of them makes, it is listed
under [New proposals](#new-proposals).

## 1. How to read the inventory

**Measured on** this worktree at `3eb9fd8` (master + `sandbox-visibility` +
this design set) with `wc -l`. The research maps predate the sandbox-visibility
commits, so every reference below was re-checked against the worktree. One
drift: [research/sandbox-visibility.md](research/sandbox-visibility.md) places
`Reserve`/`UsedBy` in `internal/vm/manager.go`. They are in
`internal/vm/policy.go:132-182`; only the `byOwner` books field is in
`internal/vm/manager.go:41`.

**Size limits.**
- `hack/size-budget.txt` lists budgets. Everything else falls under the caps
  in `internal/sizebudget/sizebudget_test.go:22-23`:
  - 800 lines for a non-test Go file under `cmd/`, `internal/` and `sdk/`;
  - 900 for a `.js`/`.mjs`/`.html` file under `web/`, `workspace-template/`,
    `builtin-*` and `hack/` (`:117-121`).
- A listed file fails when it grows past its number (`:81`) and also when it
  shrinks below 90 % of it (`:83`).
- An unlisted file fails past the cap (`:88`).
- `_test.go`, `.md` and `.sh` files are not counted.

**Columns.**

| Column | Meaning |
|---|---|
| **M** | M0 prep (no behaviour change) · M1 pause live reload (one deployment, `main`) · M2 tile deployments · M3 later rungs (feeds, `match`) · — no change, verify only · "opt." optional scaffold work |
| **Lines / limit** | current / limit. A trailing `b` marks a budget from `hack/size-budget.txt`; otherwise the limit is the cap. `—` for docs |
| **Prep** | `yes`: a verbatim move (§2) must land first |
| **Hot** | `yes`: a conflict hotspot, with its owner and rule in §5 |
| **Ref** | today's code, re-verified here |

**Rules for every row.**

1. New logic goes into new files. A budgeted or near-cap file takes only
   seams: a call, a field, a `case`.
2. A tile in the zero state takes today's code path byte for byte (P5). Every
   change keeps a zero-state default, and the golden test (§4.19) pins it.
3. A route is a `RegisterAPI` literal in `internal/boot/*.go` or
   `broker.Register`, plus an `internal/server/openapi.go` row and a
   `docs/protocol.md` row, all in one commit
   (`internal/apicheck/apicheck_test.go:122-244`).
4. Embedded trees (`docs/`, `web/`, `workspace-template/`) never cite a
   `plans/` path. Decision IDs are the integrator's
   (`internal/docscheck/docscheck_test.go:71`).
5. Every tool run on tile data goes through `internal/confine` (D78;
   `internal/confine/guard_test.go:22`). With isolation on, a sandbox failure
   is an error, never a host fallback.

## 2. Budget pressure and the M0 prep moves

Evidence: [research/delivery-infra.md](research/delivery-infra.md) §2.1,
[research/ui-surfaces.md](research/ui-surfaces.md) §5.

| File | Lines / limit | Headroom | What the feature needs there | M0 action |
|---|---|---|---|---|
| `internal/term/term.go` | 948 / 950 b | 2 | target deployment in `prepare`, `ServeWS`, `shellCmd`; env and remote config in `sandboxEnv` | move `hostForward` + `sandboxEnv` (`term.go:715-795`) out |
| `cmd/bx/main.go` | 1150 / 1150 b | 0 | `--deployment` on `status`/`logs`; one usage line | move `cmdStatus` … `cmdLogs` (`main.go:345-530`) out |
| `internal/runner/runner.go` | 831 / 850 b | 19 | code hook, keys, `Ensure` signature, binds, fan-out | move `build`, `sandboxCmd`, `stop` … `reaper` out |
| `web/bx-frame.js` | 866 / 900 | 34 | layout id, `deployments` case, state load, panel mount, test names | move the `testApi()` body (`bx-frame.js:560-601`) out |
| `hack/ui-harness/shots.js` | 870 / 877 b | 7 | two passes | none: append the `require` to an existing line (`shots.js:34` precedent) and the entries to the `PASSES` line (`:848`) |
| `internal/auth/auth.go` | 707 / 732 b | 25 | a `Principal` field, the instance map value | none; new code in `internal/auth/deployment.go`, ≤ 15 lines here |
| `internal/broker/netfn.go` | 1131 / 1170 b | 39 | read-clamp hook, dormant interface instances, restart fan-out | none; logic in new broker files, ≤ 15 lines here |
| `internal/builtins/updates.go` | 843 / 850 b | 7 | nothing | never edit |
| `workspace-template/shell/bx-shell.js` | 1881 / 1892 b | 11 | nothing required | never edit in this feature |
| `workspace-template/tiles/admin/admin.js` | 317 / 318 b | 1 | nothing | never edit |
| `cmd/bx/agent.go` | 786 / 800 | 14 | optional `--deployment` on `bx agent run` | only through a new file |
| `internal/boot/boot.go` | 722 / 800 | 78 | wiring, route registration | none (≤ 15 lines) |
| `internal/broker/broker.go` | 743 / 800 | 57 | `Policy` seam | none (≤ 15 lines) |
| `internal/server/static.go` | 650 / 800 | 150 | root selection, qualifier | openers in a new `internal/server/deployserve.go` |
| `internal/server/openapi.go` | 638 / 800 | 162 | ≈ 20 rows | none, but one owner (§5) |

The moves, in order. Each is verbatim, gets its own commit, and keeps
`make check` green:

1. **`internal/runner/runner.go` → three files.**
   - `build` (`runner.go:363-411`) joins `internal/runner/build.go`.
   - `sandboxable` and `sandboxCmd` (`:625-726`) move to a new
     `internal/runner/sandboxcmd.go`.
   - `stop`, `Stop`, `StopAll`, `Status` and `reaper` (`:728-831`) move to a
     new `internal/runner/states.go`.
   - About 574 lines stay, so the ratchet forces the budget down to at most
     ≈ 638.
   - The same work package adds the test seam the runner lacks
     ([research/delivery-infra.md](research/delivery-infra.md) §1.1):
     injectable build and start, or pure decision functions. Today's
     behaviour is table-tested first.
2. **`internal/term/term.go`.**
   - `hostForward` and `sandboxEnv` (`term.go:715-795`) move to a new
     `internal/term/sandboxenv.go`.
   - About 866 lines stay, above the 855 floor.
   - Lower the budget to ≈ 890. That leaves the term work package ≈ 24
     lines.
3. **`cmd/bx/main.go`.**
   - `cmdStatus`, `tileStatus`, `printTileStatus`, `humanBytesBx` and
     `cmdLogs` (`main.go:345-530`) move to a new `cmd/bx/status.go`.
   - About 963 lines stay, so the ratchet forces the budget to at most
     ≈ 1070.
4. **`web/bx-frame.js`.**
   - The `testApi()` body (`bx-frame.js:560-601`) moves to a new
     `web/frame-testapi.js`, exporting `testApi(f)`. The element keeps a
     one-line delegate.
   - About 826 lines stay.
   - Every later work package adds its test names there, never in the
     element.
5. **`hack/size-budget.txt`** takes the lowered numbers in the same commits.
   After M0, only the integrator edits it.

## 3. New files

| Path | Purpose | M | Owner role (§5) |
|---|---|---|---|
| `internal/deployments/` | The deployment record (`data/deployments/<CompKey>.json` through `fsutil.WriteFileAtomic`, `internal/fsutil/fsutil.go:19`); the operations of model §5 with their preconditions and per-deployment serialization; the authority table of §10; the `deployments` event payload. A plane in the D63 style: a struct of the inputs it needs (runner, broker, term, checkpoint store), testable without the broker (`internal/obs/obs.go:18-26` precedent) | M1, M2 | Store |
| `internal/checkpoint/` | The per-tile store `data/checkpoints/<CompKey>.git` (bare, private config, no hooks). The work-tree feed: a private index with the tile mounted read-only, the D77 technique (`internal/term/agentdiff.go:89-124`, `:268-283`). Materialization into `.xbin/deploy/<CompKey>/<id>/`, the deploy log, diffs for promote, GC, and `update-server-info` for the fetch-only remote. Every git run is confined | M1 | Store |
| `internal/boot/deployments.go` | Every deployments route literal (thin handlers using `server.WriteError`), the checkpoint-remote file route and the tile-state answer for the terminal window. Boot-level literals are covered by the route inventory automatically (`internal/apicheck/apicheck_test.go:153-155`) | M1, M2 | Contracts (literals, rows), Store (handlers) |
| `internal/registry/deployview.go` | A deployment's view of a component: tile-level fields from the work tree, deployment-level fields from the checkpoint (model §6) | M1 | Contracts, Runtime |
| `internal/registry/qualifier.go` | `<tile>+<name>` resolution | M2 | Fabric |
| `internal/runner/deploy.go` | The code hook glue, the per-checkpoint artifact path and key helpers (`main` keeps today's keys); from M2, the per-deployment state | M1, M2 | Runtime |
| `internal/runner/sandboxcmd.go`, `internal/runner/states.go` | M0 moves | M0 | Prep |
| `internal/server/deployserve.go` | Serving-root selection and the checkpoint opener (`fsutil.OpenBeneath`, `internal/fsutil/beneath.go:34`) | M1 | Runtime |
| `internal/auth/deployment.go` | Registration and minting helpers that take a deployment; the old signatures stay as `main` wrappers | M2 | Fabric |
| `internal/broker/deploydata.go` | The key function for per-deployment data; seed, reset, delete and usage | M2 | Data |
| `internal/broker/edgepolicy.go` | A tile's edges, the verdict for non-primary callers, the block error | M2 | Fabric |
| `internal/broker/dormant.go` | Per-deployment registration stores beside the record, the active-set rule, run now | M2 | Registrations |
| `internal/term/sandboxenv.go` | M0 move; then `XBIN_DEPLOYMENT` and the remote config | M0, M2 | Prep, Term |
| `internal/term/target.go` | A session's target deployment: its default and refusals | M2 | Term |
| `cmd/bx/status.go` | M0 move; then lenient `--deployment` | M0, M2 | Prep, CLI |
| `cmd/bx/deploy.go` | `bx live-reload` (M1); `bx deployment`, `bx deploy`, `bx promote` (M2) | M1, M2 | CLI |
| `web/frame-testapi.js` | M0 move; then the test names for the new controls | M0, M1, M2 | Prep, Web |
| `web/frame-deploy.js` | Frame-side glue: state, the event, dialogs, the optional "live reload paused" chip | M1 | Web |
| `web/deploy-state.js` | Pure logic that imports nothing, node-tested | M1 | Web |
| `web/bx-deploy.js` | The deployments panel | M2 | Web |
| `hack/deploy-state.test.mjs`, `hack/ui-harness/passes/livereload.js`, `hack/ui-harness/passes/deployments.js` | Tests (§4.19) | M1, M2 | Web |
| `test/deployments_test.go`, the golden zero-state test, `internal/boot/liveroute_test.go` | Tests (§4.19) | M0, M1, M2 | Contracts, Runtime, Store |

Never name a package `internal/deploy`. The repo's `deploy/` directory is the
installer (`deploy/install.sh`, `deploy/xbin.service`), where "deployment"
means the xbin install (glossary rule 2).

## 4. Inventory by plane

### 4.1 Registry

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §1,
[research/serving-fabric.md](research/serving-fabric.md) A.7–A.8,
[research/terminology-census.md](research/terminology-census.md) (qualifier
syntax).

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/registry/registry.go` | `Component` gains two in-memory fields with no JSON: the deployment it describes and its code root (NP-13-3). `Rescan` keeps a tile with a deployment record registered while its directory exists, through a `Keep(path) bool` hook installed by boot (model §6; but see Divergences 3). `WorkspaceManifest` gains **no** key (P15). `Resolve` is unchanged | M1 | 595 / 800 | — | yes | `registry.go:373-384`, `:471-474`, `:207-236`, `:519-536` |
| `internal/registry/deployview.go` (new) | Parses the checkpoint's `xbin.json` with `jsonc` and merges fields: `runtime`, `entry`, `setup`, `alwaysOn`, `vm`, `inject` and `native` from the checkpoint; everything else from the work tree. The zero state returns the registry's own pointer | M1 | new | — | — | `registry.go:52-99` |
| `internal/registry/native.go` | `resolveNative` stats the entry under the view's code root instead of `c.Dir` | M1 | 167 / 800 | — | — | `native.go:140-162` (`:152`, `:158`) |
| `internal/registry/qualifier.go` (new) | Walks prefixes as `Resolve` does, and an exact component match wins. Otherwise it splits the last tile segment at `+` and returns (tile, name, rest). The caller checks that the deployment exists | M2 | new | — | — | `registry.go:519-536` |
| `internal/util/util.go` | `ComponentPathOK` stays as it is, so existing `+` directories keep working. A per-deployment key helper goes next to `CompKey`, bounded so sockets stay under 108 bytes | M2 | 144 / 800 | — | — | `util.go:97-114`, `:137-144` |

### 4.2 Watch and boot

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §2a,
[research/delivery-infra.md](research/delivery-infra.md) §1.1.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/boot/serve.go` | `watchLoop` routes each changed tile by its live reload state. **Attached to the primary:** today's `reload` + `run.Changed`. **Attached to a non-primary deployment:** a qualified-component `reload` and `Changed` for that deployment only. **Live reload paused:** neither; instead a debounced "work tree moved" notice goes to the deployments plane (NP-13-12). The decision is a pure function beside `changedComponents`, with a table test | M1 | 272 / 800 | — | yes | `serve.go:161-185` (`:178-181`), `:192-205` |
| `internal/boot/boot.go` | `stepProxy` wires the runner's code hook next to `EnvForComponent`, and the registry `Keep` hook. `OnGrantChange` and `StopBackend` fan out to every deployment of the tile (Divergences 4). `stepServer` calls `registerDeploymentsAPI`. `stepLimitAlerts` reads the per-tile cgroup parent, not `CompKey` (NP-13-2). No new `Steps` entry; if one is added, `TestStepsOrder` (`boot_test.go:45`) gains its edge | M1, M2 | 722 / 800 | — | yes | `boot.go:96-116`, `:492-527` (`:505`, `:508-513`, `:517-519`), `:434-464`, `:695-698` |
| `internal/boot/deployments.go` (new) | See §3 | M1, M2 | new | — | yes | `boot/vm.go:40-70`, `boot/sandboxes.go:41-50` |
| `internal/boot/api.go` | `/backends` keeps one key per tile, describing the primary, with a nested `deployments` map. `/runtime` keeps one row per tile with nested rows. `/tile-status` takes `?deployment=` (default: the caller's own deployment) and selects that row (NP-13-4) | M2 | 134 / 800 | — | — | `api.go:21-27`, `:30-66`, `:87-117` (`:101-108`) |
| `internal/boot/sandboxes.go` | Rows carry the registry's new `deployment` field through the embedded `sbx.Entry`. Backend stats stay tile-scoped, read from the per-tile cgroup parent, so an old admin console's per-tile figure stays right. An optional `?deployment=` filter | M2 | 235 / 800 | — | — | `sandboxes.go:104-156` (`:119`) |
| `internal/boot/config.go` | Only if `07-runtime.md` makes the deployment caps configurable: a tagged `Config` field, and `docs/config.md` regenerated (`UPDATE_DOCS=1`, `config_doc_test.go:17`) | M2 | 278 / 800 | — | — | `config.go:50` precedent |
| `internal/boot/vm.go`, `push.go`, `workspace.go`, `vault.go` | none | — | 70 / 157 / 213 / 53 | — | — | |
| `internal/watch/watch.go` | none. `data/` and `.xbin/` never drive reloads, so the store, the record and materialized checkpoints are invisible to it. It drops batches under load, which is why the moved-files count is computed, never counted | — | 183 / 800 | — | — | `watch.go:61-74`, `:173-177` |

### 4.3 Runner

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §2,
[research/inbound-edges.md](research/inbound-edges.md) §1–§3,
[research/data-plane.md](research/data-plane.md) §5.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `runner.go`: `Runner`, `state` | A code hook beside `EnvForComponent` that returns the deployment view (P9). From M2, `states` is keyed by (tile, deployment), with `main` on today's key | M1, M2 | 831 / 850 b | yes | yes | `runner.go:84-162` (`:92`, `:158`), `:176-185` |
| `runner.go`: `Ensure`, `Track`, `Changed` | **M1:** `Ensure` takes the view, so every restart path of a pinned deployment builds its checkpoint (P9). `Changed` from a grant change rebuilds the checkpoint, never the work tree. **M2:** `Ensure(ctx, c, dep)`, where every inbound caller passes the primary (P7); `Track(path, dep)` | M1, M2 | | yes | yes | `runner.go:190-240`, `:246-261`, `:265-284` |
| `runner.go`: `buildAndStart`, `build` | **M1:** `build` reuses the artifact kept for a checkpoint. It builds a checkpoint only under isolation (through `buildConfined` with the checkpoint bound at `c.Dir`), and checks node/python entries under the code root. **M2:** events use the deployment's event component, and the crash text names the deployment's log | M1, M2 | | yes (build moves) | yes | `runner.go:287-361` (`:288`, `:292`, `:312`, `:321`, `:350-351`, `:358`), `:365-411` (`:372`, `:403`) |
| `runner.go`: `start` | **M1:** the non-isolated branch refuses a checkpoint view (P18), and each generation (`instance`) remembers its cgroup leaf. **M2:** per deployment: run dir, log path, `XBIN_DEPLOYMENT` (non-main), `RegisterInstance` with the deployment, the cgroup leaf, the registry entry, and `Cgroup.Remove(inst.leaf)`. The network block registers rosters and splices only for the primary | M1, M2 | | — | yes | `runner.go:413-623` (`:414`, `:423-431`, `:454-468`, `:471-478`, `:487-493`, `:502-598`, `:614`) |
| `sandboxcmd.go` (moved from `runner.go:625-726`) | **M1:** the component bind becomes `{Src: <code root>, Dst: c.Dir, RO: true}`. **M2:** resource binds are remapped (a deployment's mount at the primary's path). `Cwd` stays `c.Dir` | M0, M1, M2 | new | — | — | `runner.go:641`, `:647`, `:682` |
| `states.go` (moved from `runner.go:728-831`) | **M2:** `Stop` stops every deployment of a tile; `Status` nests non-primary states; the reaper reads the per-deployment alwaysOn | M0, M2 | new | — | — | `runner.go:745-759`, `:784-807`, `:809-831` (`:819`) |
| `internal/runner/alwayson.go` | **M1:** `isAlwaysOn` reads the view (a pinned deployment's checkpoint manifest). **M2:** `WakeAlwaysOn` covers the primary plus non-primary deployments with the switch on; backoff maps are keyed per deployment | M1, M2 | 137 / 800 | — | — | `alwayson.go:30-35`, `:38-41`, `:45-61`, `:63-84`, `:100-137` |
| `internal/runner/vm.go` | **M1:** `wantsVM` reads the view. **M2:** `Reserve` keeps the tile path as owner for every deployment (charged to the tile; the model's §12 rule); `vmLeafBytes` sizes each deployment's leaf; `stopFirst` is per deployment | M1, M2 | 154 / 800 | — | — | `vm.go:43-45`, `:59-101` (`:80`), `:106-111`, `:128-133` |
| `internal/runner/build.go` | **M0:** receives `build`. **M1:** `buildConfined` binds the checkpoint at `c.Dir` through the new confine field, and writes the per-checkpoint output. Per-tile caches stay shared by the tile's deployments | M0, M1 | 160 / 800 | — | — | `build.go:38-97` (`:43-45`, `:87`) |
| `internal/runner/env.go` | **M1:** the env layer is hashed from the view's `setup`, and its build sandbox binds the code root at `c.Dir`. **M2:** its log is per deployment, and `gcEnvLayers` keeps every hash a running deployment uses (model §9) | M1, M2 | 145 / 800 | — | — | `env.go:30-46`, `:78`, `:89`, `:134-145` |
| `internal/runner/binds.go` | `resourceBinds` takes a remap from the canonical path to the deployment's mount. `Src == Dst` stays for `main` | M2 | 54 / 800 | — | — | `binds.go:28-54` (`:51`) |
| `internal/runner/rundir.go` | A run dir per deployment under the 108-byte socket limit | M2 | 86 / 800 | — | — | `rundir.go:24-48` |
| `internal/runner/ingress.go`, `netmux.go` | `DialInto` and `ensureProvider` reach the primary. Rosters and splices are the primary's only | M2 | 136 / 63 | — | — | `ingress.go:32-47`; `netmux.go:22-41`, `:55-63` |
| `internal/runner/inspect.go` | `Backend` gains `deployment` and `checkpoint` (omitempty); `Inspect` nests non-primary rows (NP-13-4) | M1, M2 | 211 / 800 | — | — | `inspect.go:26-58`, `:62-124` (`:114`) |
| `internal/runner/stats.go` | Samples the per-tile cgroup parent, not `Procs(CompKey)`; `backendPids` is per deployment | M2 | 375 / 800 | — | — | `stats.go:121-137`, `:151-199` (`:168`), `:270-289` |
| `internal/runner/hostenv.go` | none: `XBIN_DEPLOYMENT` is added in `start`, not taken from the host | — | 46 | — | — | `hostenv.go:21-46` |
| `internal/runner/deploy.go` (new) | See §3 | M1, M2 | new | — | — | |

### 4.4 The shared sandbox layer

The layer that backends, terminals and, later, D113's tile-managed sandboxes
all use (model §12). Evidence:
[research/sandbox-visibility.md](research/sandbox-visibility.md). The backend
launch-spec binds are the `sandboxcmd.go`, `env.go` and `binds.go` rows in
§4.3.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/sbx/sbx.go` | `Entry` gains `deployment` (omitempty, set only for non-main) and `checkpoint` (omitempty). `Filter` gains `Deployment`; `Failure` gains `deployment`. The package keeps importing nothing of xbin's | M1 (`checkpoint`), M2 | 291 / 800 | — | yes | `sbx.go:50-74`, `:77-84`, `:97-106` |
| `internal/runner/sbx.go` | `main` keeps `backend:<CompKey>:g<gen>`; other deployments get `backend:<CompKey>:<name>:g<gen>` and their own `Leaf`. `sbxFail` and `sbxExited` carry the deployment and name its log | M2 | 102 / 800 | — | — | `runner/sbx.go:59-75` (`:63`, `:65-67`), `:78-99` |
| `internal/runner/health.go` | none: `waitHealthy` works per generation | — | 27 | — | — | `health.go:12-27` |
| `internal/term/sbx.go` | none: sessions are listed by id, and a session's target deployment is not a sandbox property | — | 129 | — | — | `term/sbx.go:1-50` |
| `internal/boot/sandboxes.go` | See §4.2 | M2 | 235 | — | — | |
| `internal/cgroup/cgroup_linux.go`, `cgroup_other.go` | A per-tile cgroup parent with one leaf per deployment (NP-13-2). Four new operations: add-into-parent; remove-leaf; parent procs, which walks the children because `cgroup.procs` lists only direct members; and parent usage and limit counters, which are hierarchical. The flat `comp-<CompKey>` leaf stays for zero-state and main-only tiles | M2 | 226 / 31 | — | yes | `cgroup_linux.go:89`, `:94-125`, `:148-160`, `:174-219` |
| `internal/vm/policy.go` | No API change. `Reserve(owner, mem)` receives the tile path for every deployment, and `UsedBy` stays per tile. D113's per-tile caps plug in here later | — | 200 | — | — | `vm/policy.go:132-168`, `:171-182` |
| `internal/vm/manager.go` | none: `exports` mounts binds by destination as the shim sees them, so a checkpoint bound `Src ≠ Dst` at the canonical path reaches the guest. Needs a VM integration test | — | 394 | — | — | `vm/manager.go:339-371`, `:41` |
| `internal/sandbox/sandbox.go`, `init_linux.go` | none: `Bind` already carries `Src ≠ Dst`, `sortBinds` orders by depth (stable within a depth), and `mountBind` binds `Src` onto `Dst`. Add a unit test for a read-only `Src ≠ Dst` bind at a component dir | — | 282 / 543 | — | — | `sandbox.go:108-113`, `:116-128`; `init_linux.go:389-423` |
| `workspace-template/tiles/admin/tabs/sandboxes.js` | Groups backend rows by (tile, deployment): `curGen` per deployment, and the deployment name shown on the row. An old copy labels another deployment's generation "draining", which is cosmetic | M2 | 291 / 900 | — | — | `tabs/sandboxes.js:212-239` (`:220`, `:238-239`) |
| `hack/ui-harness/passes/sandboxes.js` | The routed fixture gains a non-main generation; the pass asserts its label and grouping | M2 | 159 / 900 | — | — | `passes/sandboxes.js:20-40` |

### 4.5 Confine

Evidence: [research/git-confine.md](research/git-confine.md) §3,
[research/serving-fabric.md](research/serving-fabric.md) A.9.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/confine/confine.go` | **M1:** `Cmd` gains `DirFrom`, a host path bound at `Dir` (read-only with `ReadOnlyDir`), and `binds()` uses it. Direct mode refuses `DirFrom ≠ Dir`, failing closed (P18). Do not rely on an extra same-depth bind shadowing `Dir`'s own: `sortBinds` keeps caller order only within a depth, and the rule is invisible at the call site. **M3:** a streaming run that passes caller writers instead of the capped buffers (NP-13-9) | M1, M3 | 265 / 800 | — | yes | `confine.go:73-84`, `:133`, `:140-143`, `:153`, `:171-172`, `:218-224`; `sandbox.go:116-128` |
| `internal/confine/git.go` | none. The checkpoint store builds `GitCmd` runs over its private git dir, with the tile read-only as work tree, and the hardening flags and env apply. Do not reuse `runGitIn` from `internal/broker/code.go`: it binds the tile read-write even for reads | — | 68 | — | — | `git.go:14-34`, `:50-68`; `term/agentdiff.go:115-124`; `broker/code.go:64-80` |
| `internal/confine/confine_test.go`, `sandbox_linux_test.go` | `DirFrom` tests. In direct mode it is refused. In a sandbox it appears at `Dir` read-only, and a hostile `.git/config` inside it runs nothing on the host | M1 | test | — | — | `confine_test.go:31`; `sandbox_linux_test.go:40-81` |
| `internal/confine/guard_test.go` | none: it already scans every new package under `internal/` | — | test | — | — | `guard_test.go:22` |

### 4.6 Server

Evidence: [research/serving-fabric.md](research/serving-fabric.md) A.1–A.8,
[research/identity-resources.md](research/identity-resources.md) §3.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `static.go`: `handleComponentStatic`, `owningComponent` | **M1:** asks the new `Policy` question for the serving root: the work tree for the live reload target, the materialized checkpoint for a pinned deployment. It opens that root through `deployserve.go` (P16). **M2:** the registry resolver parses the qualifier before the owner check; a non-primary document needs ≥ `write` on the tile | M1, M2 | 650 / 800 | — | yes | `static.go:70-180` (`:105-115`, `:127`), `:330-340` |
| `static.go`: `openLegacy`, `pathAllowed` | `openLegacy` keeps serving the work tree, with the dev overlay, for the live reload target. `pathAllowed` is unchanged, because a materialized root is never reached by URL path | — | | — | | `static.go:193-219`, `:347-361` |
| `static.go`: `headInjection`, `mayMintFrameToken` | **M1:** `inject` is read from the view's manifest. **M2:** the frame token carries the document's deployment, and a non-primary document may carry a deployment meta (`11-contract.md`). `mayMintFrameToken` refuses to mint across deployments of one tile (P12). `xbin-component` stays the tile path | M1, M2 | | — | | `static.go:431-466` (`:435-436`, `:461`), `:494-503` |
| `internal/server/deployserve.go` (new) | See §3 | M1 | new | — | — | `fsutil/beneath.go:34` |
| `internal/server/tileassets.go` | **M1:** `openStrict` and `serveStrictStatic` open the selected root. **M2:** the asset token carries the deployment, and `assetHead` remaps for deployment documents | M1, M2 | 408 / 800 | — | — | `tileassets.go:133-164`, `:169-217`, `:275-336`, `:338-376` |
| `internal/server/tileorigin.go`, `tilenav.go` | Origins mode: a non-primary deployment's documents need an origin of their own, or a refusal (open question 1). Every `TileHostID(owner) == id` comparison learns the deployment | M2 | 515 / 292 | — | — | `tileorigin.go:127-135`, `:163`, `:296`, `:309`, `:375`, `:411`; `tilenav.go:203-222` |
| `internal/server/native.go` | **M1:** the pinned primary's native entry comes from its checkpoint view. **M2:** `?native=1` on a deployment URL resolves against that deployment's files. Shipped apps only use bare URLs | M1, M2 | 212 / 800 | — | — | `native.go:37-44`, `:54-106`, `:108-137` |
| `internal/server/api.go` | `componentInfo` gains an omitempty summary of the tile's live reload and deployments, absent in the zero state (`11-contract.md` names it). `apiFrameToken` is unchanged: renewal copies the claim inside `MintFrameTokenFor` | M1, M2 | 252 / 800 | — | — | `api.go:130-160`, `:162-200`, `:241-252` |
| `internal/server/policy.go` | New `Policy` methods for what the server asks: the serving root of (tile, deployment), whether a deployment exists, and which deployments a caller may address. `NoopPolicy` answers today's (the work tree, `main` only). The contracts work package declares them once (the D109 lesson) | M1 | 64 / 800 | — | yes | `policy.go:18-43`, `:46-53`; `broker/broker.go:307-320` |
| `internal/server/server.go` | `handleEventsWS` filters the new `deployments` type and qualified-component events to subscribers who can read the tile (NP-13-6). `handleTermEnv` may carry the live reload state for the terminal window (additive), or the window reads the new endpoint. No new mux pattern: deployment URLs live under `/c/` and `/api/`. View-as sessions are already refused writes | M1, M2 | 691 / 800 | — | yes | `server.go:592-612`, `:326-352`, `:129-173`, `:229` |
| `internal/server/openapi.go` | One row per new route, written once by the contracts work package | M1, M2 | 638 / 800 | — | yes | `openapi.go:83-496` |
| `internal/server/termsessions.go`, `agentapi.go` | Session rows show the target deployment. `apiAgentCreate` accepts an optional `deployment`; its decoder ignores unknown fields, so an older server ignores it | M2 | 134 / 569 | — | — | `agentapi.go:66-111` |

### 4.7 Proxy

Evidence: [research/inbound-edges.md](research/inbound-edges.md) E1, E7,
[research/serving-fabric.md](research/serving-fabric.md) B.1–B.3.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `proxy.go`: `ServeHTTP` | Resolves with the qualifier, then chooses the deployment: an explicit `+name` for humans with ≥ `write`, the principal's own deployment for self-calls, and the primary for everything else (P7). A tile principal addressing another deployment of its own tile is refused (P12). `Ensure(ctx, comp, dep)` and `Track(path, dep)`. For a non-primary caller, `broker.Policy` returns the edge-policy verdict (read clamp or block). `serveCGI` is unchanged: cgi is excluded (P18) | M2 | 319 / 800 | — | yes | `proxy.go:128-235` (`:130`, `:160-171`, `:175-178`, `:182`, `:196`), `:239-261` |
| `proxy.go`: `identify` | Adds `X-XBin-Deployment: <name>` to calls from a non-primary deployment. Inbound `X-XBin-*` is already stripped, so the header is trustworthy | M2 | | — | | `proxy.go:275-305` |
| `internal/proxy/ingress.go` | `ForwardIngress` reaches the primary; nothing else | M2 | 120 / 800 | — | — | `ingress.go:31-95` (`:58`, `:65`) |

### 4.8 Auth

Evidence: [research/identity-resources.md](research/identity-resources.md) §1,
[research/serving-fabric.md](research/serving-fabric.md) A.3, B.5.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/auth/auth.go` | `Principal` gains `Deployment` (`""` means `main`). The instance map records (tile, deployment), and instance and terminal principals set the field. `From()` stays the tile path (P11). The code goes in `auth/deployment.go`; this file takes ≤ 15 lines | M2 | 707 / 732 b | — | yes | `auth.go:54-84`, `:196-207`, `:219`, `:445-462`, `:471-478`, `:498-507`, `:572`, `:590` |
| `internal/auth/frametoken.go` | A deployment claim for non-`main` documents, as a new field; 4- and 5-field tokens keep verifying. `framePrincipal` sets `Deployment`, and `MintFrameTokenFor` copies it on renewal | M2 | 320 / 800 | — | — | `frametoken.go:218-232`, `:252-300`, `:304-320` |
| `internal/auth/assettoken.go`, `tilebinding.go` | **Tokens mode:** the asset grant carries the deployment. **Origins mode:** the label, ticket, cookie and `TilePrincipal` carry it (open question 1) | M2 | 229 / 340 | — | — | `assettoken.go:102-176`, `:182-193`, `:215-228`; `tilebinding.go:233-300` |
| `internal/auth/deployment.go` (new) | See §3 | M2 | new | — | — | |

### 4.9 Broker

Evidence: [research/data-plane.md](research/data-plane.md) §2–§6,
[research/inbound-edges.md](research/inbound-edges.md) §2–§3,
[research/identity-resources.md](research/identity-resources.md) §2, §4, §5.

**(a) Resources, vault, cron, bus subscriptions, netfn, ingressfn**

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/broker/resources.go` | `EnvFor` takes the view: `uses` stay tile-level, fs/sqlite values stay the primary's paths (the runner binds the deployment's mount there), and kv/blob/bus/cron values stay canonical ids. `kvAccess`, `blobAccess` and `apiBusPublish` map (canonical id, the principal's deployment) to the physical key. `busFilter` delivers within the subscriber's deployment. `Provision` mounts the namespaces of existing deployments | M2 | 458 / 800 | — | — | `resources.go:36-66`, `:71-144`, `:173-202`, `:302-338`, `:410-437`, `:441-458` |
| `internal/broker/deploydata.go` (new) | One key function: (canonical id, deployment) → dir key, fs label, kv bucket, kv label, where `main` returns today's values (P6). Every `util.ScopeKey` site and every hand-built bucket string goes through it. It also does seed (re-encoding kv under the new labels, blob, file resources), reset, delete and usage. `08-data.md` owns the scheme | M2 | new | — | — | `ScopeKey` at `resources.go:38`, `:318`, `resusage.go:119`, `diskmon.go:243`, `backup.go:48`, `:312`, `broker.go:188`, `:730`, `resenc_wire.go:95`, `:119`, `:140`; bucket strings `backup.go:146`, `:332`, `:482` |
| `internal/broker/resenc_wire.go`, `internal/resenc/resenc.go` | `fsResPath`, `EncryptionHold`, `MountEncrypted` and `SealResources` work per namespace. Mounts stay under `.xbin/resenc/` for the installer's AppArmor rule. `Unmount` gets its first caller (reset, delete). `Ensure` already takes the key label apart from the location | M2 | 229 / 365 | — | — | `resenc_wire.go:43`, `:94-162`; `resenc.go:132`, `:243-253`; `deploy/install.sh:909` |
| `internal/broker/vault.go` | `vaultPath` is per deployment; `main` keeps `data/vault/<CompKey>.json`. `vaultAccess` picks by the principal's deployment, and D30's read rule is unchanged. `migrateVaults` must skip the non-main layout | M2 | 370 / 800 | — | — | `vault.go:45-47`, `:115-147`, `:151-176`, `:178-215` |
| `internal/broker/cron.go` | A non-primary PUT or DELETE goes to the deployment's dormant store, never `data/cron-jobs.json` (model §3). `fire` delivers the primary's jobs, plus a non-primary deployment's while its deliveries are on. The dispatch carries a routing hint, since it runs as `xbin/cron`. `forget` also drops dormant stores | M2 | 270 / 800 | — | — | `cron.go:108`, `:148-164`, `:166-186`, `:200-208`, `:227-270` |
| `internal/broker/bussubs.go` | As cron: dormant per-deployment subscriptions, a routing hint on delivery, and the reader re-check under the edge policy | M2 | 449 / 800 | — | — | `bussubs.go:127-135`, `:158`, `:186-235`, `:271-362`, `:391-449` |
| `internal/broker/dormant.go` (new) | Stores under `data/deployments/<CompKey>/`, the active-set rule (the primary, plus deliveries on), run now, and the list the panel shows | M2 | new | — | — | model §3, §7 |
| `internal/broker/netfn.go` | A non-primary `apiIfaceInstancesSet` stores dormant rows. `httpBindingRole` feeds the read clamp. The net edge for non-primary deployments, with rosters (`NetProviderRoster`, `NetClientTarget`) for the primary only. Binding and instance restarts fan out to every deployment. Logic in new files; ≤ 15 lines here | M2 | 1131 / 1170 b | — | yes | `netfn.go:47-114`, `:223-251`, `:286-311`, `:725-857` (`:825-826`, `:844`), `:1037-1131` (`:1117-1123`) |
| `internal/broker/ingressfn.go` | A non-primary `apiIngressHosts` goes dormant. `apiIngressRoutes` and `IngressFwdFor` answer the primary only. `IngressLookup` is unchanged | M2 | 721 / 800 | — | — | `ingressfn.go:104-146`, `:313-331`, `:491-586`, `:613-642` |

**(b) Policy, leftovers, lifecycle, backup, transfer**

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/broker/broker.go` | `Policy`: the self rule compares deployments (P12), and a non-primary caller's result passes the edge policy (P3). `grantedRole` is unchanged, since authority is the tile's. `allowRes` clamps `res:` targets. `brokerPolicy` answers the new server questions (M1). `grantRestart` fans out, and `scopeDiskUsage` counts namespaces | M1, M2 | 743 / 800 | — | yes | `broker.go:74-98`, `:185-198`, `:246-297`, `:307-320`, `:418-452`, `:478-490`, `:493`, `:648-666` |
| `internal/broker/edgepolicy.go` (new) | A tile's edges (bindings, assigned grants, the net slot), the per-edge verdict for non-primary callers, and the block error that names the policy | M2 | new | — | — | model §7; [research/serving-fabric.md](research/serving-fabric.md) B.6 |
| `internal/broker/policy.go` | `newTilePathOK` refuses `+`, and the check moves ahead of the admin early return (NP-13-7). `pathLeftovers` lists a record, a checkpoint store and non-main namespaces (model §11, and Divergences 5) | M1, M2 | 306 / 800 | — | — | `policy.go:105-129` (`:106-108`), `:156-176`, `:218-258` |
| `internal/broker/lifecycle.go` | Disable, hide and offload stop every deployment; enable starts the primary. The `reload` it emits stays bare | M2 | 116 / 800 | — | — | `lifecycle.go:22-116` (`:28`, `:102`, `:114`) |
| `internal/broker/backup.go`, `backup_cron.go` | **M1:** a component backup adds the record and the checkpoint store, and restore puts them back. **M2:** non-primary data is opt-in, under its own archive key with its own retention; offload and `StopBackendSafe` cover every deployment | M1, M2 | 590 / 168 | — | — | `backup.go:45`, `:60-108`, `:219-305`, `:443-491`; `backup_cron.go:86-121` |
| `internal/backup/backup.go` | Additive manifest fields only; `Schema` stays 1 | M1 | 186 / 800 | — | — | `backup.go:28-29` |
| `internal/broker/transfer.go` | `executeTransferEffects` restarts every deployment. Nothing needs re-keying: the record is path-keyed | M2 | 281 / 800 | — | — | `transfer.go:239-281` |
| `internal/broker/orgsapi.go` | none: `mayManageTile` is the tile-manager gate the deployments plane calls (D24/D33) | — | 747 / 820 b | — | — | `orgsapi.go:427` |
| `internal/broker/whoami.go` | Element principals report `deployment` (omitempty). `elementXbinCapable` is reused for P19 | M2 | 156 / 800 | — | — | `whoami.go:15-84`, `:119-131` |
| `internal/broker/diskmon.go`, `resusage.go` | Namespaces are measured and get per-deployment rows; `08-data.md` decides pooled or separate quota | M2 | 248 / 200 | — | — | `diskmon.go:114-166`, `:239-248`; `resusage.go:62-131` |

**(c) Code PRs, builtin updates, templates, clone, create**

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/broker/prs.go` | none: PRs land in the work tree, so they reach the live reload target (model §11) | — | 631 | — | — | `prs.go:76-84` |
| `internal/broker/tiles.go`, `internal/builtins/updates.go` | none: replace and merge write the work tree, and the watcher routes it. `updates.go` is at its budget | — | 216 / 843 of 850 b | — | — | `tiles.go:147-206`; `updates.go:504`, `:538` |
| `internal/broker/templates.go`, `templaterepo.go` | none for new tiles, which start in the zero state. `serveTemplateRepo` is the pattern the checkpoint-remote route follows: static files, GET only, containment | — | 194 / 227 | — | — | `templates.go:24-31`; `templaterepo.go:214-227` |
| `internal/broker/clone.go`, `gitimport.go` | none: both carry the work tree only | — | 287 / 269 | — | — | `clone.go:159-205` |
| `internal/broker/create.go` | `assignOwner` drops a removed tile's dormant registrations along with its cron jobs and subscriptions (D85) | M2 | 198 / 800 | — | — | `create.go:188-198` (`:189`) |
| `internal/broker/code.go` | none; `requireCodeRead` is the gate precedent for reading checkpoints ("what reading its work tree needs") | — | 426 | — | — | `code.go:151-165` |

### 4.10 Obs

Evidence: [research/data-plane.md](research/data-plane.md) §3,
[research/ui-surfaces.md](research/ui-surfaces.md) §2.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/obs/status.go` | Status is keyed per deployment. A non-primary deployment's report never becomes a `status` event or a `GET /tile-report` entry: it travels in the `deployments` event and the deployments API (NP-13-5). `watchStatusRestarts` clears only the restarted deployment's key | M2 | 143 / 800 | — | — | `status.go:42-59`, `:61-116`, `:118-127`, `:129-143` |
| `internal/obs/logs.go` | `?deployment=`, with a per-deployment path. `main` keeps `.xbin/log/<CompKey>.log`, which the docs name. The gate is unchanged | M2 | 153 / 800 | — | — | `logs.go:42-49`, `:51-153` (`:69`) |
| `internal/obs/prefs.go` | Frame principals of non-main deployments get per-deployment prefs (model §9); `main` keeps today's path | M2 | 129 / 800 | — | — | `prefs.go:33-48` |
| `internal/obs/obs.go` | The plane learns a principal's deployment through a new input field (D63 pattern) | M2 | 34 | — | — | `obs.go:18-26` |

### 4.11 Term

Evidence: [research/inbound-edges.md](research/inbound-edges.md) E14,
[research/git-confine.md](research/git-confine.md) §4, §6.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/term/term.go` | After the M0 move: `prepare` mints the terminal token with the session's target, `ServeWS` reads an optional target, and `shellCmd` sets `XBIN_DEPLOYMENT` on the non-isolated path | M2 | 948 / 950 b | yes | yes | `term.go:183-250`, `:294-346` (`:338`), `:424-479` (`:460`) |
| `internal/term/sandboxenv.go` (moved from `term.go:715-795`) | `sandboxEnv` sets `XBIN_DEPLOYMENT` for a non-main target, and adds the checkpoint remote's config entries next to today's `http://xbin/` rewrite (NP-13-8) | M0, M2 | new (≈ 81) | — | — | `term.go:737-795` (`:740`, `:787-792`) |
| `internal/term/target.go` (new) | The target's default (the live reload target, else the primary; glossary), and the refusals (P19, P21), through a hook installed by boot | M2 | new | — | — | |
| `internal/term/attach.go`, `sessions.go` | The session frame and `SessionInfo` carry the target (omitempty) | M2 | 258 / 151 | — | — | `attach.go:190-194`; `sessions.go:19-40` |
| `internal/term/agent.go` | `OpenAgentWith` passes the target to `createAgent` | M2 | 705 / 800 | — | — | `agent.go:129-185`, `:242` |
| `internal/term/history.go` | Agent history of a non-main target is keyed per deployment (model §9) | M2 | 250 / 800 | — | — | `history.go:53-55` |
| `internal/term/binds.go` | none: terminals mount the work tree read-write, and `.xbin` and `data` stay masked, so the store and materialized checkpoints are never visible | — | 142 | — | — | `binds.go:49-53`, `:57-58` |
| `internal/term/vm.go` | none: VM terminals already book to the tile | — | 156 | — | — | `term/vm.go:85` |

### 4.12 Push, ingress, VM

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/push/api.go` | A notification from a non-primary deployment is not pushed and spends no budget. It is kept for the panel as "would notify …" (model §7) | M2 | 431 / 800 | — | — | `api.go:287-300`, `:305-400` (`:356-360`, `:368`) |
| `internal/boot/push.go` | none | — | 157 | — | — | `push.go:155` |
| `internal/ingress/ingress.go`, `forwards.go`, `streams.go` | none. Routes carry a component and reach the primary through the runner, and the terminator door exists only in the primary's spawn (§4.3, §4.9) | — | 158 / 114 / 280 | — | — | `forwards.go:36-40`; `streams.go:176-195` |
| `internal/vm/*` | See §4.4 | — | | | | |

### 4.13 Users

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `internal/users/*` | none. The tile-manager gate is `broker.mayManageTile`, and `noTerminal` accounts are already capped at `write` through `Access` (D88) | — | | — | — | `users/personal.go:346`; `broker/orgsapi.go:427` |

### 4.14 cmd/bx

Evidence: [research/builder-contract.md](research/builder-contract.md) §9.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `cmd/bx/main.go` | **M0:** the moves of §2. **M1:** the usage text gets a new block on the existing concatenation line, which is line-neutral | M0, M1 | 1150 / 1150 b | yes | yes | `main.go:345-530`, `:111-174` (`:174`) |
| `cmd/bx/status.go` (moved) | `status` and `logs` take a lenient `--deployment`, defaulting to `$XBIN_DEPLOYMENT`. `logs` reads `GET /logs` instead of `.xbin/log`, which isolated terminals cannot see | M0, M2 | new (≈ 187) | — | — | `main.go:494-530`; `term/binds.go:49-53` |
| `cmd/bx/deploy.go` (new) | Registered into `moreCmds` from an `init()`, so `native.go` is not edited. Names follow the glossary spellings; shapes are `11-contract.md`'s | M1, M2 | new | — | — | `native.go:28-32`; `agent.go:30-33` |
| `cmd/bx/native.go`, `agent.go`, `args.go` | none; new flags stay lenient, as on `status`/`logs` today | — | 569 / 786 / 34 | — | — | `args.go` |
| `cmd/xbind/main.go` | none | — | 81 / 85 b | — | — | |

### 4.15 SDK

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `sdk/xbin.go` | `Deployment()` reads `XBIN_DEPLOYMENT` (`""` for `main`), and `CallerInfo.Deployment` reads `X-XBin-Deployment`. Additive and zero-dependency (compat rule 8) | M2 | 418 / 800 | — | — | `xbin.go:82`, `:85-117`; `sdk/go.mod` |
| `sdk/kv.go`, `notify.go`, `http.go` | none: the server maps by the token's deployment | — | 120 / 84 / 22 | — | — | |

### 4.16 Web

Evidence: [research/ui-surfaces.md](research/ui-surfaces.md) §1–§2, §5–§7.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `web/bx-frame.js` | **M0:** the `testApi()` move. **M1:** imports from `frame-deploy.js`, a `deployments` case in `_event`, the live reload state loaded with the window state. **M2:** a new layout id in `_layout`, the `open()` doc, the `_setLayout` widen list and a lazily mounted panel in `.panels`. One line per hook | M0, M1, M2 | 866 / 900 | yes | yes | `bx-frame.js:86-110`, `:250-255`, `:366-396`, `:533-541`, `:560-601`, `:792-800`, `:840-845` |
| `web/frame-testapi.js` (moved) | Test names for live reload, pending count, reload now and the panel | M0, M1, M2 | new (≈ 45) | — | — | |
| `web/frame-titlebar.js` | **M1:** the live reload control and reload now beside `layerButtons`, in both `settings()` branches and in the tools row; `barKey` gains the live-reload-paused flag, the pending count and the target; controls are `button`s, which the drag rule skips. **M2:** a sixth `.lyt` button, and "target: <name>" in the bar | M1, M2 | 314 / 900 | — | yes | `frame-titlebar.js:61-62`, `:100-109`, `:121-136`, `:205-213`; `bx-kit.js:145-146` |
| `web/frame-launcher.js` | `loadTileState` loads the live reload state with an explicit may-control flag, and the launcher shows a banner modelled on `.lbase` | M1 | 231 / 900 | — | — | `frame-launcher.js:34-40`, `:152-165` |
| `web/frame-deploy.js` (new) | Confirms through `f._confirm`; draws the optional "live reload paused" chip on `.frame-wrap` | M1 | new | — | — | `bx-frame.js:717-725`, `:820-829` |
| `web/deploy-state.js` (new) | Button states, pending text, which actions a level may take | M1 | new | — | — | `hack/term-sessions.test.mjs` precedent |
| `web/bx-deploy.js` (new) | The deployments panel in bx-prs's list and detail layout. A row per deployment: status, primary badge, live reload target, checkpoint, dormant registrations with run now, "would notify". Per-deployment logs through `<bx-logs deployment>` | M2 | new | — | — | `bx-prs.js:41-95` |
| `web/bx-logs.js` | A `deployment` attribute (observed) and `&deployment=` | M2 | 170 / 900 | — | — | `bx-logs.js:61`, `:141` |
| `web/frame-info.js` | `infoFor` resolves a qualified src to its tile's facts, and bootstraps the token for a deployment document | M2 | 100 / 900 | — | — | `frame-info.js:44-51`, `:55-62`, `:75-95` |
| `web/events-socket.js` | none: a frame whose src is `<tile>+<name>` covers exactly its own events, and the primary's frame ignores them | — | 49 | — | — | `events-socket.js:40-49` |
| `web/xbin-client.js` | Optional `xbin.deployment` from a deployment meta (`11-contract.md`). Renewal is unchanged: the server copies the claim | M2 opt. | 412 / 900 | — | — | `xbin-client.js:50`, `:81-93`, `:412` |
| `web/bx-code.js`, `bx-terminal.js` | Optional: the checkpoint each deployment runs; grey notice lines on live reload and target changes (netNote precedent) | M1, M2 opt. | 507 / 713 | — | — | `bx-code.js:266-272`; `bx-terminal.js:619-647` |
| `web/term-sessions.js` | The saved window pref may hold the new layout id; a downgraded binary shows an empty body | M2 | 124 / 900 | — | — | `term-sessions.js:16`, `:37-38` |
| `native/ios/…` | none in v1: the app uses bare URLs and matches components on a `/` boundary | — | — | — | — | `Events.swift:54`; `WorkspaceEvents.swift:243-245` |

### 4.17 Workspace template

All workspace-owned. An upgrade never rewrites these files; they reach
existing workspaces only through `bx builtin update` (compat rule 4). The
terminal window, which ships with the binary, must stand alone.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| `shell/bx-shell.js` | none. An old copy toasts and marks the browser title for **any** `status` event, whatever its component, which is why NP-13-5 exists | — | 1881 / 1892 b | — | yes | `bx-shell.js:199`, `:1257-1266`, `:1278-1283`, `:1742-1744` |
| `shell/menus.js` | Optional: a deployments square, lines for pause live reload and reload now, "open <name> full page". `hack/menus.test.mjs` asserts four squares and indexes the admin block by position | M2 opt. | 127 / 900 | — | — | `menus.js:86-127`; `menus.test.mjs:110`, `:131-150` |
| `shell/bx-tile-admin.js` | Optional: a `deployments` section and a pill beside the lifecycle pill. It matches `/runtime` rows by path (first match), which NP-13-4 keeps meaning the primary | M2 opt. | 533 / 900 | — | — | `bx-tile-admin.js:154`, `:521-528` |
| `shell/bx-canvas.js`, `bx-side.js`, `shell-kit.js` | Optional chips. Never a second card or row for a deployment: layouts key by path | M2 opt. | 428 / 454 / 91 | — | — | `bx-canvas.js:274-303`, `:419`; `bx-side.js:270-301` |
| `tiles/admin/tabs/runtime.js` | Non-primary rows nested under the tile. It indexes `/runtime` rows by path, last wins | M2 | 725 / 900 | — | — | `runtime.js:338` |
| `tiles/admin/tabs/sandboxes.js` | See §4.4 | M2 | 291 | — | — | |
| `tiles/admin/admin.js` | none | — | 317 / 318 b | — | — | |
| `tiles/manager/index.html` | Optional: the clone text says deployments don't come along | M2 opt. | 564 / 900 | — | — | `index.html:113-118` |
| `AGENTS.md` | **M1:** which deployment a save reaches; pause live reload and reload now. **M2:** testing on a non-primary deployment, `XBIN_DEPLOYMENT`, promotion as a deliberate step beside CM-2's commit rule, debug recipes per deployment. It is seeded only when absent, so existing workspaces learn this from `/docs/` and the changelog | M1, M2 | 886 / — | — | yes | `AGENTS.md:34-36`, `:150-178`, `:472-486`, `:534-535`, `:883-884`; `boot/boot.go:157-161` |
| `apps/welcome/notes.js` | Optional | — | 1028 / 1050 b | — | — | |

### 4.18 Docs

Builder docs are embedded and served at `/docs/`. The docs work package writes
them from this set at implementation time. Evidence:
[research/builder-contract.md](research/builder-contract.md) §5–§8.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| [/docs/protocol.md](/docs/protocol.md) | New rows in the API fence, including dormant semantics on interface instances, ingress hosts, cron, bus subscriptions and tile-report. The `deployments` event and the qualified component in the events block. `XBIN_DEPLOYMENT` in the backend contract. `data/deployments/`, `data/checkpoints/` and `.xbin/deploy/` in the filesystem contract. `X-XBin-Deployment` next to `X-XBin-From` in the header list. The `+` qualifier in the `/c/` and `/api/<component>` resolution text. `?deployment=`, nested rows and `deployment` fields on the routes changed above | M1, M2 | 2430 / — | — | yes | `protocol.md:7-97`, `:209-270`, `:412-419`, `:425-2014`, `:2271-2289`, `:2391-2407`, `:2409-2427` |
| [/docs/elements.md](/docs/elements.md) | Live reload happens only on the attached deployment, and not at all while live reload is paused. The manifest's tile-level and deployment-level fields. `XBIN_DEPLOYMENT` in the env table. The `<bx-frame>` layout and events | M1, M2 | 590 / — | — | yes | `elements.md:22-191`, `:310-363` (`:356`), `:500-546` (`:533-541`) |
| [/docs/sdk.md](/docs/sdk.md) | `xbin.Deployment()`, `CallerInfo.Deployment`, `xbin.deployment`, the event list | M2 | 307 / — | — | — | `sdk.md:12-52`, `:201-288` |
| [/docs/resources.md](/docs/resources.md) | Per-deployment namespaces. Ids and `XBIN_RES_*` are unchanged; registrations from non-primary deployments are dormant; seed and reset | M2 | 291 / — | — | — | `resources.md:120-197` |
| [/docs/auth.md](/docs/auth.md) | Principals carry a deployment, and self-calls stay inside it. The edge policy: read clamp and block. The per-deployment vault and vault copy. Tile-manager acts. The new D82 leftovers | M2 | 1398 / — | — | yes | `auth.md:14-116`, `:245-343`, `:344-379`, `:1061-1196` |
| [/docs/isolation.md](/docs/isolation.md) | The checkpoint bound at the canonical path, and isolation required for pinned and non-primary backends. The store is confine-only. Per-deployment leaves under a per-tile cgroup parent. One VM guest per deployment, charged to the tile | M1, M2 | 482 / — | — | — | `isolation.md:14-73`, `:344-482` |
| [/docs/bx.md](/docs/bx.md) | New commands, `--deployment`, `XBIN_DEPLOYMENT` | M1, M2 | 400 / — | — | yes | `bx.md:165-`, `:381-` |
| [/docs/changelog.md](/docs/changelog.md), `docs/changes/` | Integrator only. No migration note is expected, because nothing breaks | M1, M2 | 2999 / — | — | yes | |
| [/docs/overview/01-model.md](/docs/overview/01-model.md), [03-components](/docs/overview/03-components.md), [04-frontend](/docs/overview/04-frontend.md) | The save-to-reload pipeline gains the live reload target and paused live reload | M1 | 232 / 297 / 286 | — | — | `01-model.md:78`; `03-components.md:206-228`; `04-frontend.md:112` |
| [/docs/overview/05-identity.md](/docs/overview/05-identity.md), [06-authorization](/docs/overview/06-authorization.md) | `X-XBin-Deployment`; edges and the edge policy | M2 | 276 / 226 | — | — | `05-identity.md:16-32`; `06-authorization.md:152-168` |
| [/docs/overview/09-terminals.md](/docs/overview/09-terminals.md) | The session target and the title bar; the logs tab per deployment | M1, M2 | 547 / — | — | yes | `09-terminals.md:217-243`, `:541-547` |
| [/docs/overview/08-sandbox.md](/docs/overview/08-sandbox.md), [10-resources](/docs/overview/10-resources.md), [13-ingress](/docs/overview/13-ingress.md), [14-lifecycle](/docs/overview/14-lifecycle.md), [15-operations](/docs/overview/15-operations.md), [16-extending](/docs/overview/16-extending.md) | The bind set; namespaces; primary-only ingress; lifecycle stops every deployment, and backups; the watch loop; the env contract | M1, M2 | 271 / 300 / 331 / 292 / 389 / 165 | — | — | `08-sandbox.md:89-105`; `10-resources.md:107-175`; `14-lifecycle.md:75-111`; `15-operations.md:272`; `16-extending.md:17-41` |
| [/docs/index.md](/docs/index.md), [/docs/getting-started.md](/docs/getting-started.md) | Keep "hot-reload on save" true with one qualifier | M1 | 72 / 209 | — | — | `index.md:6`; `getting-started.md:85` |
| [/docs/frontend-kit.md](/docs/frontend-kit.md) | The new shell-only modules | M1, M2 | 87 / — | — | — | `frontend-kit.md:37-50` |
| [/docs/config.md](/docs/config.md) | Only if a flag is added; the file is generated | M2 | 62 / — | — | — | |
| [/docs/compat.md](/docs/compat.md) | `12-compat.md` decides. At least "what this does not promise" covers the new `data/` and `.xbin/` layouts | M1 | 147 / — | — | — | `compat.md:141-147` |
| [/docs/maintenance.md](/docs/maintenance.md) | New guards (NP-13-11) and harness passes | M1, M2 | 566 / — | — | yes | `maintenance.md:423-484` |

### 4.19 Tests, harness and CI

Evidence: [research/delivery-infra.md](research/delivery-infra.md) §1, §2.

| File | Change | M | Lines / limit | Prep | Hot | Ref |
|---|---|---|---|---|---|---|
| Golden zero-state test (new, contracts) | A tile with no record keeps identical env, spawn spec, `/components` entry, served bytes, events and registry rows. It has the shape of `TestLegacyInjectionUnchanged`, and every work package keeps it green | M0 | new | — | — | `tileassets_test.go:187` |
| Runner and watch seam tests (new) | Today's behaviour, table-tested before any change | M0 | new | — | — | `runner.go:287-411`; `watch_native_test.go:16` |
| `internal/boot/liveroute_test.go` (new) | A table test of save routing: target primary, target non-primary, live reload paused | M1 | new | — | — | |
| `internal/checkpoint` tests (new) | Unit tests in direct mode (run by `make check`), plus `linux && integration` tests with a hostile `.git/config` for checkpoint, materialize and remote export | M1 | new | — | — | `confine/sandbox_linux_test.go:40-81` |
| `internal/runner/resourcebinds_test.go`, `sbx_test.go`; `internal/sbx/sbx_test.go` | The remapped bind; the deployment dimension, with `main` IDs unchanged | M2 | test | — | — | `resourcebinds_test.go:9` |
| Broker fixture file (new) | A tile with `main` and `dev`: `EnvFor` per deployment and the authority matrix. `testWorkspace` stays unedited | M2 | new | — | — | `broker_test.go:14-63` |
| `internal/term/gates_test.go` | Target-deployment gates | M2 | test | — | — | `gates_test.go:16-60` |
| `test/deployments_test.go` (new) | **Shared non-isolated daemon:** a static tile with live reload paused, reloaded now, and resumed to the zero state. **Its own isolated daemon** (skips without rootfs or userns): a Go tile pinned through a crash restart and through `.xbin` removal. M2: data separation and routing. Nothing is appended to `integration_test.go` | M1, M2 | new | — | — | `integration_test.go:56-109`, `:179-233` |
| `test/legacy_workspace_test.go`, `internal/boot/boot_test.go` | none: there is no boot migration (P5, P15), so the allowed lists stay untouched | — | test | — | yes | `legacy_workspace_test.go:68-97`; `boot_test.go:118`, `:166` |
| `internal/apicheck`, `internal/docscheck`, `internal/sizebudget` | No code change; they enforce §1 | — | test | — | — | `apicheck_test.go:153-155`; `docscheck_test.go:25-28` |
| `hack/ui-harness/shots.js` | A `require` appended to an existing line, and entries appended to the `PASSES` line | M1, M2 | 870 / 877 b | — | yes | `shots.js:16-34`, `:845-849` |
| `passes/livereload.js` (new) | Pause live reload; the pending count after a write into `$WS/<tile>`; no reload while live reload is paused; reload now applies; resume. Restores the tile's state at the end | M1 | new | — | — | `passes/vmtoggle.js`; `passes/tileassets.js` |
| `passes/deployments.js` (new) | The panel; add and remove; promote; the target in the bar | M2 | new | — | — | |
| `passes/agenttab.js` | Asserts exactly five `.lyt` buttons; gains the sixth | M2 | 362 / 900 | — | — | `agenttab.js:238` |
| `hack/ui-harness/seed.sh`, `run.sh` | A fixture tile; an optional `--isolate` mode (open question 4) | M1 | 156 / 116 | — | — | `run.sh:68-77` |
| `hack/deploy-state.test.mjs` (new) | Node tests of `web/deploy-state.js` | M1 | new | — | — | `Makefile:141-143` |
| `Makefile` | `make integration` gains `./internal/checkpoint/`, and `./internal/sandbox/`, which it misses today | M1 | 224 / — | — | yes | `Makefile:110-117` |
| `.github/workflows/ci.yml` | A rootfs with git for the confined tests (NP-13-10) | M1 | 110 / — | — | yes | `ci.yml:41-43` |

**Files no work package edits for this feature:**
- `workspace-template/shell/bx-shell.js` and
  `workspace-template/tiles/admin/admin.js`;
- `internal/builtins/updates.go`;
- the root `xbin.json` struct (`registry.WorkspaceManifest`);
- both legacy allowed lists and `test/integration_test.go`;
- `examples/`, which are also native fixtures;
- `builtin-templates/agent/`: dormant registrations and self-calls that stay
  inside the caller's deployment keep its start-up re-registration and engine
  hold working (P12, P13), with no template change.

## 5. Conflict hotspots and owners

The work packages are numbered in `14-implementation.md`. This section assigns
files to owner roles.

| Role | Owns |
|---|---|
| Prep (M0) | The verbatim moves; `hack/size-budget.txt` during M0; the runner and watch seams |
| Contracts | Every shared declaration: `Principal.Deployment`, the `sbx.Entry`/`Filter` fields, the `deployments` event type and payload, the registry view fields and `Keep` hook, the runner code hook and the `Ensure` signature, the `server.Policy` methods, and the `boot.go` wiring. Also every route literal, with its `openapi.go` and `protocol.md` rows, behind stub handlers; the planned harness pass registrations; the golden zero-state test; the SDK additions |
| Store | `internal/checkpoint`, `internal/deployments`, the handlers in `internal/boot/deployments.go` |
| Runtime | `internal/runner` (except `sbx.go`), `internal/boot/serve.go`, `internal/server/deployserve.go` and its call sites in `static.go`, `tileassets.go` and `native.go` |
| Shared sandbox | `internal/sbx`, `internal/runner/sbx.go`, `internal/cgroup`, `internal/confine`, the VM and sandbox tests, `boot/sandboxes.go`, the admin sandboxes tab and its pass |
| Fabric | `registry/qualifier.go`, `internal/proxy`, `internal/auth`, `broker/edgepolicy.go`, `broker.go` `Policy`, `netfn.go`, the `server.go` event filter, the origins-mode files |
| Data | `broker/deploydata.go`, `resources.go`, `resenc_wire.go`, `internal/resenc`, `vault.go`, `backup*.go`, `diskmon.go`, `resusage.go`, the leftovers in `policy.go` |
| Registrations | `broker/dormant.go`, `cron.go`, `bussubs.go`, `ingressfn.go`, `create.go`, `internal/push/api.go`, `internal/obs` |
| Term | `internal/term`, `server/termsessions.go`, `server/agentapi.go` |
| Web | New and changed `web/` modules, the workspace-template UI files, harness passes, node tests |
| CLI | `cmd/bx` after M0 |
| Docs | Everything under `docs/` except the changelog and `docs/changes/`; `workspace-template/AGENTS.md` |
| Integrator | `docs/changelog.md`, `docs/changes/*`, `plans/DECISIONS.md`, plan status lines, `Makefile`, `.github/workflows/ci.yml`, `hack/size-budget.txt` after M0, and the `shots.js` `PASSES` line after the contracts work package |

Commit counts and merge conflicts below are from
[research/delivery-infra.md](research/delivery-infra.md) §0 and §4.1.

| File | Why it is hot | Owner | Rule for everyone else |
|---|---|---|---|
| `docs/protocol.md` | 79 commits since 2026-09-01; merge conflicts in the native swarm | Contracts (rows), Docs (prose) | Never add a row. Hand prose to the docs work package in the commit body |
| `internal/server/openapi.go` | 46 commits; one slice literal | Contracts | Never edit |
| `docs/changelog.md`, `plans/DECISIONS.md`, `docs/changes/*` | 107 and 64 commits; the integrator consolidated them for the native swarm | Integrator | Put "Changelog:" and "Decision:" sections in the last commit body |
| `hack/ui-harness/shots.js` | 44 commits; 7 lines left | Contracts registers pass stubs that SKIP; then Web | Edit only `passes/<yours>.js` |
| `hack/size-budget.txt` | the ratchet | Prep, then Integrator | Split files; never raise a number |
| `internal/boot/serve.go` | two native-swarm conflicts | Runtime | No edits |
| `internal/boot/boot.go` | `Steps` and all wiring | Contracts | Request a seam from Contracts |
| `internal/runner/runner.go` | at budget; the core of M1 and M2 | Runtime | Shared sandbox edits only `runner/sbx.go`, through one call in `start` |
| `internal/term/term.go` | at budget | Term | No edits |
| `cmd/bx/main.go` | at budget | Prep, then CLI (usage line only) | Add commands in new files |
| `web/bx-frame.js` | 31 commits; 34 lines left | Web | Test names go in `frame-testapi.js` |
| `web/frame-titlebar.js` | 14 commits; the bar degrades by measurement | Web | No edits |
| `internal/auth/auth.go`, `frametoken.go` | native-swarm conflicts in auth | Fabric | Field declared by Contracts |
| `internal/broker/broker.go` | `Register`, `Policy` | Contracts (seam), Fabric (`Policy`) | Logic in your own broker file |
| `internal/broker/netfn.go` | at budget | Fabric | Registrations and Data call in through their own files |
| `internal/server/static.go`, `server.go`, `policy.go` | 17 and 19 commits; native-swarm conflicts | Runtime (`static.go`), Fabric (`server.go`), Contracts (`policy.go`) | No edits |
| `internal/events/events.go`, `internal/registry/registry.go`, `internal/sbx/sbx.go` | shared declarations; D113 edits `sbx.go` next | Contracts; Shared sandbox (`sbx.go`) | No edits |
| `internal/confine/confine.go`, `internal/cgroup/cgroup_linux.go` | the shared layer that D113 also builds on | Shared sandbox | No edits |
| `Makefile`, `.github/workflows/ci.yml` | 20 commits; CI | Integrator | Request in the commit body |
| `docs/elements.md`, `docs/auth.md`, `docs/bx.md`, `docs/overview/09-terminals.md`, `docs/maintenance.md`, `workspace-template/AGENTS.md` | 28, 35, 24, 30, 40 and 20 commits | Docs | Hand text over |
| `test/integration_test.go`, the legacy allowed lists | a conflict magnet; D62 | nobody | New test files only |
| `workspace-template/shell/bx-shell.js` | 28 commits; 11 lines left | nobody | Sibling modules only |

## 6. Operations → surfaces

Plane tags:
- **STORE**: `internal/checkpoint`, `internal/deployments`, `boot/deployments.go`.
- **RUN**: the runner rows of §4.3.
- **SERVE**: `static.go`, `deployserve.go`, `tileassets.go`, `native.go`.
- **WATCH**: `boot/serve.go`.
- **EVT**: `events.go` and the `deployments` event.
- **FAB**: `registry/qualifier.go`, `proxy.go`, `internal/auth`,
  `edgepolicy.go`, `broker.Policy`.
- **DATA**: the data rows of §4.9(a–b).
- **REGS**: `dormant.go`, `cron.go`, `bussubs.go`, `netfn.go` interface
  instances, `ingressfn.go` hosts, `push/api.go`, `obs/status.go`.
- **SBX**: §4.4.
- **CONF**: `confine.go`.
- **TERM**: §4.11.
- **UI**: §4.16.
- **CLI**: `cmd/bx/deploy.go`.

| Operation (model §5) | Surfaces | Gate and refusals (model §10, §12) |
|---|---|---|
| Pause live reload | STORE checkpoints the work tree and sets `liveReload = ""`, with a log entry. RUN swaps the target onto the checkpoint, reusing the artifact when the work tree has not moved. CONF builds a checkpoint when it must. SERVE serves the primary from the materialized checkpoint. WATCH stops reloading and starts the moved-files count. EVT; UI control; CLI `bx live-reload pause` | Terminal level (`auth.go:104`) or the tile's terminal/agent token. Refused for cgi and for a backend tile without isolation (P18) |
| Resume live reload | STORE sets `liveReload` and removes the record when the tile returns to the zero state (P5). RUN deploys from the work tree; SERVE switches back to it; WATCH reloads again. EVT, UI, CLI | As pause. Refused onto a protected primary (P21) |
| Reload now | STORE: a checkpoint and a log entry. RUN: blue/green. SERVE: the new root. EVT: a bare `reload` when the last target is the primary, a qualified one otherwise. UI: the button with the pending count. CLI | As pause |
| Attach live reload to Y | STORE pins X to a fresh checkpoint and puts Y on the work tree. RUN does both swaps; WATCH routes saves to Y. EVT, UI | Terminal level. Not onto a protected primary |
| Add deployment Y | STORE: the record entry (name grammar, cap, no component at `<tile>+<name>`: NP-13-7), with code from the work tree, the primary or a named checkpoint. DATA: an empty namespace, and vault key names as placeholders. RUN: per-deployment state, started lazily. SBX: its leaf. FAB: the qualifier. TERM: a new target. EVT, UI panel, CLI | Terminal level; seeded data is a tile-manager act. Refused for chrome and `xbin`-capable tiles (P19: `static.go:342-344`, `whoami.go:119-131`) |
| Deploy to X | STORE: the checkpoint, the log, and pausing live reload if X was the target. RUN, CONF, SERVE, EVT, UI, CLI | Terminal level. Onto a protected primary: tile managers only, never a token (P21, `orgsapi.go:427`) |
| Promote A → B | STORE: A's checkpoint, or a fresh one if A follows the work tree, plus the diff for the confirm. RUN on B. EVT, UI diff and confirm, CLI | As deploy, judged on B |
| Roll back X to C | STORE: the deploy log, and rematerializing C. RUN: the artifact kept for C, or a rebuild under CONF. EVT, UI, CLI | As deploy |
| Remove deployment Y | STORE. RUN stops Y. DATA deletes the namespace (unmount, remove, drop the kv namespace) and the vault file. REGS drops the dormant stores. Obs: logs and status. SBX: the leaf. STORE: GC eligibility. TERM: sessions targeting Y lose their target. EVT, UI, CLI | Terminal level. Not `main`, not the primary |
| Reassign primary to Y | STORE flips it atomically. RUN: the primary resolution of every inbound caller (P7), and the alwaysOn and reaper rules. FAB: the bare URL. REGS: the active set flips (cron, bus, interface instances, ingress hosts); ingress reconciles; netmux rosters. EVT. UI: a confirm that data does not follow. CLI | Tile managers |
| Protect / unprotect the primary | STORE sets the flag and detaches live reload from the primary. TERM and FAB refuse token-driven code changes to the primary. UI, CLI | Tile managers |
| Seed Y | DATA: kv re-encoded under Y's labels; blob; file resources, as ciphertext or a confined transfer (CONF). RUN stops Y in every tile of the scope that has Y. UI: the PII warning. CLI | Tile managers |
| Reset Y | DATA; RUN stops it; UI; CLI | Terminal level |
| Vault copy to Y | DATA vault; UI; CLI | Tile managers |
| Deliveries on/off for Y | STORE sets the switch; REGS changes the active set. UI, CLI | Tile managers |
| alwaysOn on/off for Y | STORE sets the switch; RUN `alwayson.go` honours it if Y's checkpoint says `alwaysOn`. UI, CLI | Tile managers |
| Set edge policy | STORE writes `edges`. FAB gives the verdict at call time (`broker.Policy`, `allowRes`, bus reader checks). FAB: the proxy's header and block error. RUN restarts non-primary deployments when the net edge changes, since it is captured at spawn. UI, CLI | Tile managers |
| Run now (a dormant cron job of Y) | REGS dispatches the job once to Y through the cron dispatcher, with a routing hint. UI | Terminal level, like any act on a non-primary deployment |
| Feed: checkpoint the work tree | STORE: confined, with a private index and the tile read-only; `update-server-info` for the remote | Internal |
| Flow H: fetch a deployment's code | STORE: the remote route (template-remote pattern). TERM: the remote config entries (NP-13-8) | What reading the work tree needs (`code.go:151-165`) |
| A session's target deployment | TERM: the default, the token claim, `XBIN_DEPLOYMENT`. FAB: the terminal principal and routing. UI: the bar marker. CLI honours the env | Terminal level |

## 7. Invariants → surfaces

| # | Enforced in | Pinned by |
|---|---|---|
| P1 | `internal/checkpoint` is the only way code reaches a pinned deployment, and the record stores checkpoint ids. The M3 feeds implement the same feed interface | Store unit tests |
| P2 | Names only: UI copy, `cmd/bx/deploy.go`, wire fields (`11-contract.md`), docs | Review against [01-glossary.md](01-glossary.md) |
| P3 | `broker/edgepolicy.go` through `broker.Policy` (`broker.go:478-490`), `allowRes` (`:493`) and the bus reader checks (`bussubs.go:337-362`, `resources.go:441-458`); the block error and `X-XBin-Deployment` in `proxy.go`; `edges` in the record | Broker fixture matrix |
| P4 | The authority table in `internal/deployments`, using `CanTerminalTile` and `mayManageTile` (`orgsapi.go:427`) | Authority matrix |
| P5 | Every row's zero-state default: the registry view is the registry pointer; runner keys and paths are today's; the `/c/` opener is today's; events stay bare; sandbox-registry IDs are unchanged; the flat cgroup leaf stays (NP-13-2); `watchLoop` takes today's branch; boot writes nothing | The golden test; `tileassets_test.go:187`; `legacy_workspace_test.go:68-97` |
| P6 | The key function in `deploydata.go`; `vault.go:45-47`; `obs/logs.go:69`; the runner's build and log paths. `main`'s registrations stay in `data/cron-jobs.json`, `data/bus-subscriptions.json` and the root `xbin.json` | Key-function test |
| P7 | Every inbound caller passes the primary: `proxy.go:182`, `proxy/ingress.go:58`, `runner/ingress.go:32-47`, `netmux.go:55-63`, `alwayson.go:63-84`, the cron and bus dispatch (`cron.go:200-208`, `bussubs.go:127-135`), archiver calls (`backup.go:379-390`), push links (`push/api.go:368`) | Integration: email → calendar reaches the primary |
| P8 | The record invariant in `internal/deployments`; `watchLoop` routes to one target; the target keeps `c.Dir`, with no per-save checkpoint | `liveroute_test.go` |
| P9 | The runner's code hook consulted by `Ensure` on every restart path: crash watch (`runner.go:334-356`), reaper (`states.go`), `afterExit` (`alwayson.go:100-137`), `OnGrantChange` (`boot.go:508-512`), provider nudges (`runner.go:586-597`), re-enable, xbind restart. Artifacts are kept per checkpoint and rematerialized after `.xbin/` loss | Isolated integration test |
| P10 | Promote writes only the code pointer. Env, vault and bindings resolve at start (`resources.go:71-144`) | Store unit test |
| P11 | `Principal.Deployment` without new grants; `From()` unchanged (`auth.go:196-207`); `grantedRole` unchanged (`broker.go:418-452`); the P3 clamps | Broker matrix |
| P12 | Claims in instance, frame and terminal tokens; self-call routing in the proxy; `mayMintFrameToken` (`static.go:494-503`); renewal copies the claim (`frametoken.go:218-224`); the self rule (`broker.go:482-484`) | Server and proxy tests |
| P13 | `dormant.go`, `cron.go`, `bussubs.go`, `netfn.go:1037-1131`, `ingressfn.go:491-586`, `push/api.go:305-400`, `obs/status.go` (NP-13-5); qualified events | Broker tests; harness |
| P14 | `deploydata.go` (empty namespaces, key-name placeholders); tile-manager gates | Broker tests |
| P15 | The record in `data/deployments/`. `registry.WorkspaceManifest` is unchanged (`registry.go:207-236`), and nothing is written to the work tree | Legacy fixture |
| P16 | `internal/checkpoint` runs `confine.GitCmd` with the hardening (`git.go:14-34`); `deployserve.go` opens with `fsutil.OpenBeneath`; confine's `DirFrom` | `TestNoDirectExec`; the hostile-repo test |
| P17 | `registry/qualifier.go`; `policy.go:156-176`; auth claims; the header (`proxy.go:275-305`); env (runner `start`, `term/sandboxenv.go`); events | Golden test (all absent in the zero state) |
| P18 | `internal/deployments` refuses. The runner's non-isolated branch refuses a checkpoint (`runner.go:454-468`), and confine's direct mode refuses `DirFrom` | Unit tests |
| P19 | `internal/deployments` refuses non-primary deployments for chrome (`static.go:342-344`, `Manifest.Chrome`) and for `xbin`-capable tiles (`whoami.go:119-131`, `broker.go:466-475`) | Unit test |
| P20 | Docs (`/docs/auth.md`, `/docs/isolation.md`) and panel copy; the P12 and P16 mechanisms | Review |
| P21 | `internal/deployments`: protecting detaches live reload; no attach or resume onto a protected primary; code changes are for managers only, and tokens are refused. Also `term/target.go` and `proxy.go` | Authority matrix |

## 8. Open questions

1. **Origins mode.** Does a non-primary deployment get its own tile origin
   ([research/serving-fabric.md](research/serving-fabric.md) A.8), or are
   deployment URLs refused under `--tile-assets=origins` in M2? On a shared
   origin, a non-primary deployment would share the primary's storage
   (`tileassets.go:73-84`).
2. **Re-creating a tile.** Does a path's current owner, re-creating their own
   tile, inherit its leftover record, checkpoint store and namespaces, or
   start in the zero state? (`create.go:188-198`, `policy.go:218-221`)
3. **Who sees that live reload is paused.** `/ws/term/env` is gated to terminal
   level (`server.go:339-352`). A chip every viewer sees needs a read-level
   source: a `/components` field or the `deployments` event.
4. **Harness isolation.** Does the harness gain an `--isolate` mode? Pinned
   backends need isolation (P18), and `run.sh:68-77` starts without it.
5. **Deployment caps.** Are they constants, or `Config` fields covered by
   `TestConfigDoc`?
6. **The terminal window's new controls.** Do they appear on every tile, or
   only on tiles that opted in (`agenttab.js:238`; P5 promises no change for
   such tiles)?
7. **The per-tile cgroup parent.** Is it needed in M2, or do separate leaves
   suffice until D113 needs the parent?

## Divergences from the model

1. **A non-primary status still reaches old shells.**
   - **What the model says:** §8 says old clients ignore events with
     `component: "<tile>+<name>"` because they match exactly or by prefix.
     That holds for `web/bx-frame.js:366-369`, `web/events-socket.js:40-49`
     and the iOS app.
   - **Where it fails:** the workspace-owned shell handles `status` events
     with no component match at all.
     - It stores any component (`bx-shell.js:199`, `:1257-1266`).
     - It marks the browser title from every key (`:1278-1283`).
     - It toasts every transient report (`:1260`, `:1742-1744`).
     - Its initial list comes from `GET /tile-report`, keyed by any component
       (`obs/status.go:42-59`).
   - **Result:** a non-primary deployment's backend calling `xbin.Status` or
     `xbin.Notify` would mark every old shell's title and raise toasts.
   - **Fix:** NP-13-5.
2. **The non-primary marker rules disagree.**
   - P17 says the markers are absent for **`main`**.
   - §7 makes the frame-token claim main-keyed ("present only for non-main
     deployments"), but the header primary-keyed ("absent on the primary's
     calls").
   - The glossary sets `XBIN_DEPLOYMENT` "only when the target is not the
     primary".
   - After the primary is reassigned (flow F), these give opposite answers
     for both deployments. A primary-keyed env var would also go stale in
     running backends, since env is captured at spawn (`runner.go:423-431`).
   - **Fix:** two predicates, from one helper in `internal/deployments`.
     - **Keyed on "not `main`":** credentials and spawn-time values, which are
       static per deployment. That is the instance, frame and terminal token
       claims, and `XBIN_DEPLOYMENT`. A claimless token means `main`
       everywhere.
     - **Keyed on "not the primary":** per-request markers. That is
       `X-XBin-Deployment`, the qualified event component and the bare URL.
3. **"A pinned primary survives the work tree" is incomplete.**
   - Tile-level fields (`uses`, `interfaces`, `provides`, `exposes`) come from
     the work tree (§6).
   - With `xbin.json` missing or unparseable, `Rescan` would keep the tile
     only through the new hook (`registry.go:471-474`). But `EnvFor` walks
     `c.Manifest.Uses` (`resources.go:73`), same-scope auto-grants read the
     caller's `Uses` (`broker.go:438-450`), and interfaces and exposes come
     from the same manifest.
   - **Result:** the next restart of the pinned primary loses its resources,
     slots and routes.
   - **Fix:** while a tile with a record lacks a parseable work-tree
     manifest, its tile-level fields fall back to the primary's checkpoint
     manifest, and the manifest error is surfaced.
4. **Tile-level restarts must reach every deployment.**
   - §7 says every existing caller of `Ensure` passes the primary. That is
     right for inbound edges.
   - Restarts and stops that exist because the **tile** changed must fan out
     to every running deployment. Otherwise a non-primary deployment keeps
     stale spawn-time env, or keeps running after disable. Those paths:
     - grant, binding and interface-instance changes (`broker.go:648-666`,
       `netfn.go:825-826`, `:844`, `:1117-1123`);
     - transfer effects (`transfer.go:239-281`);
     - seal (`resenc_wire.go:150-162`);
     - lifecycle stops (`lifecycle.go:102`, `backup.go:439`, `:455`).
   - **Fix:** all of them funnel through `OnGrantChange` and `StopBackend`,
     so the fan-out is two closures in `boot.go:508-513`.
5. **D82's refusal list doesn't cover admin creators.**
   - §11 adds the record, the checkpoint store and non-main namespaces to
     D82's refusal list. But `canCreateAt` returns before `newTilePathOK` for
     admins (`policy.go:106-108`), and also for unattributed elements holding
     `xbin:writer` (`:116-118`).
   - **Result:** an admin creating a tile at such a path would inherit a
     pinned primary serving the removed tile's checkpoint instead of the new
     work tree.
   - **Fix:** every creation path resets the path's deployment state before
     the tile first registers, in the style of `assignOwner`'s `forget`
     (`create.go:188-198`). The fields are: the record, dormant registrations,
     the checkpoint store and non-main namespaces. The refusal for non-admin,
     non-owner creators stays as it is.
6. **"Terminal/agent tokens cannot target the primary at all" (§10, P21)
   collides with the target default.**
   - The glossary defaults a session's target to the primary while live
     reload is paused. So on a tile with live reload paused and a protected
     primary, a session would have no allowed target, and
     `curl $XBIN_URL/api/$XBIN_COMPONENT` would fail.
   - **Fix:** read "target" as the object of deploy, promote, roll back,
     reload now and resume. Sessions may still direct their API calls at a
     protected primary.

## New proposals

- **NP-13-1 — Code layout.** The record and operations live in
  `internal/deployments`, a plane in the D63 style. The store lives in
  `internal/checkpoint`. Every route literal sits in
  `internal/boot/deployments.go` (the `boot/vm.go` and `boot/sandboxes.go`
  precedents), because literals outside `internal/boot/*.go` and
  `broker.Register` fail `TestRouteInventory`
  (`apicheck_test.go:153-155`, `:244`). Never `internal/deploy`: that name
  collides with the installer's `deploy/` directory.
- **NP-13-2 — Cgroup layout.**
  - Zero-state and main-only tiles keep today's flat `comp-<CompKey>` leaf
    (P5; the `Leaf` values in `/sandboxes`, `runner/sbx.go:65-67`).
  - A tile gets the per-tile cgroup parent, with one leaf per deployment, when
    it first runs a non-main deployment. `main` moves in at its next
    generation.
  - The reason: cgroup v2 forbids processes in a cgroup whose children have
    controllers enabled, and `Procs` lists only direct members
    (`cgroup_linux.go:198-212`, `stats.go:168`).
  - Each generation remembers its leaf for removal (`runner.go:614`).
  - Stats and limit alerts read the parent (`boot.go:434-464`), so old
    consoles' per-tile figures stay right.
  - D113 may later move every tile under a parent.
- **NP-13-3 — A per-spawn deployment view.**
  - `registry.Component` gains in-memory `Deployment` and code-root fields.
    `internal/registry/deployview.go` builds them per spawn (tile-level fields
    from the work tree, deployment-level fields from the checkpoint), and the
    runner hooks receive the view unchanged (`runner.go:92-151`).
  - All runner key and path sites go through one helper, where `main` gives
    today's value. There are 18 `util.CompKey` sites in `internal/runner`.
  - The zero-state view is the registry's own pointer.
- **NP-13-4 — Runtime API shapes.**
  - `/backends` and `/runtime` keep exactly one key or row per tile,
    describing the primary. Non-primary deployments are nested under
    `deployments`.
  - `/tile-status` takes `?deployment=`.
  - The reason: old consoles and shells index these by path and count them
    (`runtime.js:338`, `bx-tile-admin.js:154`, `boot/api.go:101-108`,
    `bx-shell.js:1326`).
- **NP-13-5 — Where a non-primary deployment's status and notices travel.** A
  non-primary deployment's `POST /tile-report` status or notice, and a
  diverted `POST /notify`, travel only in the `deployments` event and the
  deployments API, and are shown in the panel. They never become `status`
  events or entries in `GET /tile-report`'s `statuses` (Divergences 1).
- **NP-13-6 — Filter the new event traffic.** Deliver the `deployments` event
  and qualified-component `reload`/`build-*` events only to subscribers who can
  read the tile, following the `pr` precedent (`server.go:597-599`). New types
  can be filtered without breaking anyone. Today non-bus events reach every
  subscriber ([research/side-findings.md](research/side-findings.md) #2).
- **NP-13-7 — Reserve `+`.**
  - `+` is refused in new tile paths for every creator. Unlike D82's `:`,
    which binds only non-admins (`policy.go:161-165`), an admin-created
    `apps/x+dev` would shadow `apps/x`'s `dev` URL, because an exact match
    wins.
  - Adding a deployment is refused when a component exists at
    `<tile>+<name>`.
- **NP-13-8 — The fetch-only checkpoint remote is session config.** It is
  added through `GIT_CONFIG_*` entries in `internal/term/sandboxenv.go`, next
  to today's `http://xbin/` rewrite (`term.go:787-792`). Nothing is written
  into the tile's `.git/config`, so resuming to the zero state leaves nothing
  behind (P5). This needs the M0 `term.go` move.
- **NP-13-9 — M3 streaming is a confine change.** The deploy remote's
  streaming run passes caller writers through `confine.Run`
  (`confine.go:133`, `:171-172`). It does not depend on D113's exec protocol,
  which is not built.
- **NP-13-10 — Confined tests run in CI.**
  - CI builds a rootfs with git (`XBIN_TEST_ROOTFS`), so the checkpoint
    store's hostile-repo tests and one isolated pinned-backend test run in CI.
    Today they skip there (`confine/sandbox_linux_test.go:26-34`).
  - `make integration` lists `./internal/checkpoint/` and
    `./internal/sandbox/`.
- **NP-13-11 — A guard for non-primary writes.** A test in the style of
  `TestNoDirectExec`: every `/api/xbin/*` write handler that a caller from a
  non-primary deployment can reach is on an explicit allow-list, reviewed
  with the list of self-scoped APIs
  ([research/serving-fabric.md](research/serving-fabric.md) B.7). It is owned
  by the contracts work package and documented in `docs/maintenance.md`.
- **NP-13-12 — The moved-files count is computed.** It comes from a confined
  diff of the work tree against the checkpoint, debounced on watcher batches.
  It is never a count of batches, because the watcher drops batches under
  load (`watch.go:173-177`).
- **NP-13-13 — The M0 moves and budgets of §2.** `runner.go` → `build.go`,
  `sandboxcmd.go`, `states.go`; `term.go` → `sandboxenv.go`; `cmd/bx/main.go`
  → `status.go`; `bx-frame.js` → `frame-testapi.js`. Each lands in one
  verbatim commit, and the budgets are lowered as the ratchet demands.
- **NP-13-14 — The contracts work package registers the shared surface up
  front.**
  - Every planned route literal, with its `openapi.go` and `protocol.md`
    rows, behind stub handlers that return 501 with `server.WriteError`.
  - Every planned harness pass, as a stub that writes `SKIP`.
  - After that, feature work packages never touch the three most-conflicted
    files of the native swarm.
