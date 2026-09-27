# 05 — The model: tile deployments over a checkpoint store

> Status: live — the recommended model the rest of
> [plans/dev-lifecycle/](README.md) elaborates. Terms are defined in
> [01-glossary.md](01-glossary.md) and used verbatim. P1–P4 were ratified by
> the owner on 2026-09-27; P5 onward are proposed and become D-numbers only at
> ratification. [04-options.md](04-options.md) argues each choice against the
> alternatives. [research/](research/README.md) holds the evidence behind
> every "today" statement.

## 1. The shape in one picture

```
                    feeds (where code comes from)
      work tree ──┐   (later: tracked branch, deploy remote)
                  ▼
        ┌──────────────────────┐      confine-only git, xbind-owned (data/)
        │   checkpoint store   │  ◀── every checkpoint: immutable, content-addressed
        └──────────┬───────────┘
                   │ deploy / promote / roll back / reload now
     ┌─────────────┴──────────────┬──────────────────────────┐
     ▼                            ▼                          ▼
 deployment "main"          deployment "dev"           deployment "…"
 PRIMARY (role)             non-primary                non-primary
 pinned to c:3f2a1c9        live reload target ◀── runs the work tree directly
 today's data + vault       own data + vault (empty / seeded)
 receives every inbound     reachable at /c/<tile>+dev/, by writers,
 edge                       own terminals, own self-calls
     │                            │
     └────────── one tile: one principal, one set of grants/bindings/caps ──────┘
                 outbound edges: the primary as today; non-primary per edge policy
                 (read → provider primary, role clamped to reader | block; later: match)
```

A tile runs one or more **tile deployments**. Each is a named runtime with a
code pointer, its own data, and its own backend and frontend. Code reaches a
deployment only as a **checkpoint**, except for the one deployment that
**live reload** is attached to, which runs the **work tree** as every tile
does today. Exactly one deployment is **primary** and receives everything
that comes from outside. **Authority stays with the tile.**

## 2. The zero state (P5)

A tile with no deployment record is **byte-for-byte today's tile**:
- It has one implicit deployment, `main`, which is primary.
- Live reload is attached to `main`. `main` runs the work tree, is served at
  `/c/<tile>/` and `/api/<tile>/`, and uses today's storage keys.
- Every event, env var, header, token and file path stays exactly as today.
- There is no deployment record, no checkpoint store, and no new process.

Opting in is an action on a tile: pausing live reload, or adding a
deployment. Opting out is the reverse. Resuming live reload on a tile whose
only deployment is `main`, with default settings, **returns it to the zero
state**: the record is removed and nothing lingers that changes behaviour.

Every existing tile, and every tile created from now on, stays in the zero
state until someone acts on it. No manifest key is involved. Deployments are
an operator choice, like bindings and lifecycle. The tile author's code is
untouched.

## 3. Objects and where they live

| Object | Key | Stored | Written by |
|---|---|---|---|
| Tile | path | work tree + registry (unchanged) | as today |
| Deployment record | tile path | `data/deployments/<CompKey>.json` (xbind-owned, masked from terminals, in component backups) | the deployments API only |
| Checkpoint store | tile path | `data/checkpoints/<CompKey>.git`, a bare repository | confined git only (D78), private config, no hooks or fsmonitor |
| Checkpoint | content hash (short id `c:<7+ hex>`) | an object in the store; materialized read-only under `.xbin/deploy/<CompKey>/<id>/` (derived, rebuildable) | confined git |
| Deploy log | (tile, deployment) | in the store, one entry per deploy | the deployments API |
| Deployment data | (scope, deployment name) | `main`: today's paths and keys; others: a dot-level namespace no existing key can produce (the exact scheme is in `08-data.md`) | the resource broker |
| Built artifact | (tile, checkpoint) | `.xbin/build/<CompKey>/…`, one per checkpoint while referenced; the live reload target keeps today's single `bin` | the runner (confined builds) |
| Dormant registration | (tile, deployment, name) | per-deployment files beside the deployment record (e.g. `data/deployments/<CompKey>/cron.json`), **never** extra rows in today's `data/cron-jobs.json` / `data/bus-subscriptions.json` / root `xbin.json` maps. An older xbind would load such rows as `main`'s and fire them. `main`'s registrations stay exactly where they are today. | the self-scoped APIs |
| Edge policy | (tile, edge) | the deployment record | the deployments API |

**Checkpoints are fetchable, read-only, by the tile's own sessions.** The
store is exposed to the tile's terminal and agent sessions as a fetch-only git
remote, with one ref per deployment (`deploy/<name>`). It works like the
`template` remote: static dumb-HTTP files, refreshed by a confined
`update-server-info` after each checkpoint, reached through the terminal's
injected `GIT_CONFIG_COUNT` rewrite. Git users can then branch from exactly
what a deployment runs (flow H). Nothing is ever pushed through this remote;
pushing is the separate deploy-remote rung. Reading a tile's checkpoints needs
exactly what reading its work tree needs.

**Why the deployment record isn't in the root `xbin.json`:**
- Older binaries re-marshal that file through a struct and drop keys they
  don't know.
- Boot must never rewrite it (the legacy fixture).
- It is git-tracked and visible to admin terminals. Moving production code
  must never be a hand edit.

The D24 ownership precedent applies: a tile terminal must not be able to edit
what runs in production except through the gated API.

## 4. The deployment record

Illustrative shape; `11-contract.md` owns the wire.

```jsonc
{
  "tile": "apps/crm",
  "liveReload": "dev",           // the attached deployment, or "" = paused
  "primary": "main",             // the role; default "main"
  "protectedPrimary": false,     // P4 protection switch (tile managers)
  "edges": { "slot:llm": "read", "grant:apps/calendar": "block" },  // non-primary only
  "deployments": {
    "main": { "checkpoint": "c:3f2a1c9", "created": "…", "by": "user:ana" },
    "dev":  { "checkpoint": null,        // null ⇔ it is the live reload target
              "deliveries": false, "alwaysOn": false,
              "data": { "state": "seeded", "from": "main", "at": "…", "by": "user:ana" },
              "created": "…", "by": "user:ana" }
  }
}
```

Invariants on the record:
- `liveReload` is `""` or the name of exactly one deployment, and that
  deployment's `checkpoint` is `null`. Every other deployment has a
  checkpoint.
- `primary` names an existing deployment. `main` always exists.
- Names follow the deployment-name grammar in the glossary, and are immutable.

## 5. Operations

Every operation is atomic on the record. Operations on one deployment
serialize (one deploy in flight per deployment; later ones queue). A deploy
that fails leaves the deployment on its previous code and reports the build or
start output. Authority is set out in §10; events in §8.

| Operation | Precondition | Effect |
|---|---|---|
| **Pause live reload** | live reload attached to X | `liveReload = ""`. Checkpoint the work tree and deploy it to X, which is pinned to it. **Normally** the checkpoint is exactly what X was running: a code-identical swap that moves X off the live directory (`07-runtime.md`). **If the work tree moved since X's last successful build**, pausing ships that newest state once, which is what live reload was about to do. **If that build fails**, live reload stays detached, and X keeps serving its current generation with the error shown until a reload now succeeds. That is no worse than today, where a crash with a broken work tree cannot rebuild either. |
| **Resume live reload** (onto X, default: the last target) | paused; X not a protected primary | `liveReload = X`. X deploys from the work tree. Its code tracks every save again. |
| **Reload now** | paused | Checkpoint the work tree and deploy it to the last target. It stays pinned. |
| **Attach live reload to Y** | live reload on X, X ≠ Y | Checkpoint the work tree and pin X to it. Y deploys from the work tree and follows it. |
| **Add deployment Y** | the name is free; within the per-tile cap | Create Y, with code from the work tree (fresh checkpoint), the primary's checkpoint, or a named checkpoint. Data: empty (default) or seeded (manager only). Vault: key names only. Deliveries and alwaysOn off. Optionally attach live reload to Y, which pins the previous target. |
| **Deploy to X** | — | Put a checkpoint on X (default: a fresh checkpoint of the work tree). If X was the live reload target, live reload pauses. |
| **Promote A → B** | A ≠ B | B receives A's current code: A's checkpoint, or a fresh checkpoint of the work tree if A follows it. Only code moves. If B was the live reload target, live reload pauses. |
| **Roll back X to C** | C is in X's deploy log (or any checkpoint of the tile) | Deploy C to X. It pauses live reload if X was the target. |
| **Remove deployment Y** | Y ≠ `main`; Y not primary | Stop Y. Delete its data namespace, vault, logs, dormant registrations and artifacts. Its checkpoints remain until GC. Detach live reload if Y held it. |
| **Reassign primary to Y** | Y exists and is healthy | Routing moves atomically (§7). Data does not move. The UI must say whose data the new primary serves. The old primary's registrations become dormant; Y's activate. |
| **Protect / unprotect primary** | — | While protected, only tile managers change the primary's code, and live reload cannot attach to the primary. Protecting detaches it if attached (the primary is pinned in place). |
| **Seed Y** | Y non-primary; Y stopped for the copy | Copy the primary's deployment data into Y's namespace, consistently, re-keying encrypted stores (`08-data.md`). This is a PII-bearing act, so the UI warns. |
| **Reset Y** | Y non-primary | Empty Y's data namespace. |
| **Vault copy to Y** | Y non-primary | Copy the selected keys' values from the primary into Y's vault. |
| **Deliveries on/off for Y** | Y non-primary | Activate or deactivate Y's dormant registrations for Y. |
| **alwaysOn on/off for Y** | Y non-primary; the manifest (Y's checkpoint) says `alwaysOn` | Honour D84 for Y. It is never implied for non-primary deployments. |
| **Set edge policy** | the edge exists on the tile | `read` \| `block` for all of the tile's non-primary deployments (later also `match`). |
| **Run now** (a dormant cron job of Y) | — | Deliver that job once to Y, as a manual trigger (the Netlify precedent). |

**Promotion is not a merge.** Nothing is combined: B's code becomes exactly
A's code. Combining work is a git activity in the work tree (branches, PRs),
and it produces the checkpoint that is then deployed.

**A pinned deployment's code changes only through the rows above.** That
holds through every restart path: idle reap, crash restart, grant or binding
change, provider nudge, alwaysOn backoff, re-enable, xbind restart, and loss
of `.xbin/` (P9).

## 6. What code and which manifest a deployment runs

The tile's manifest mixes two kinds of fields. With deployments they separate
cleanly. **Tile-level** fields describe the tile's existence and authority,
and come from the **work tree**, as today. **Deployment-level** fields
describe how a particular body of code runs, and come from the deployment's
own code: the work tree for the live reload target, the checkpoint otherwise.

| Tile-level (work tree) | Deployment-level (the deployment's code) |
|---|---|
| existence (`xbin.json` / `index.html`), path, scope membership (`scope.json`), `chrome`, `template`, `uses`, `interfaces`, `provides`, `exposes`, `expose.roles`, `deps` | `runtime`, `entry`, `setup`, `alwaysOn`, `vm`, `inject`, `native`, and every file the frontend serves and the backend executes |

Consequences:
- **Authority requests track the tile's latest intent.** A `uses` entry added
  in the work tree files its pending grant as today. Approval grants the
  tile. A deployment whose code doesn't use the grant simply doesn't use it.
- **Bindings are the tile's.** Every deployment gets the tile's slot env; code
  that doesn't know a slot ignores it. For non-primary deployments the edge
  policy decides what a slot can actually reach (§7).
- **A pinned primary survives the work tree.** A tile with a deployment record
  stays registered while its directory exists, even if the work tree
  temporarily lacks `xbin.json`/`index.html` (an agent checking out an old
  branch, a refactor in progress). The pinned primary keeps serving. Only
  lifecycle state, or the directory disappearing, takes the tile down. A
  tile in the zero state behaves exactly as today.
- **Cross-tile references follow the other tile's primary.** This covers
  `deps/` symlinks, `go.work` modules, and absolute `/c/<other>/…` imports in
  frontend code. A checkpoint pins the tile's own files, not its
  dependencies. A pinned Go backend keeps its built artifact, so this matters
  only when an artifact must be rebuilt (`07-runtime.md` records the
  reproducibility caveat). This is documented, not hidden.

## 7. Routing

**Inbound edges reach only the primary (P7).** Every existing entry point
resolves `(tile) → primary`. That covers:
- the bare `/c/<tile>/` and `/api/<tile>/`;
- consumers' interface bindings and grants to call the tile;
- ingress routes and terminator forwards;
- stream and lan-ingress links, and net-provider splices;
- cron ticks, bus push deliveries, event triggers and alwaysOn;
- archiver calls, notify links, and the native app.

The runner's single funnel (`Runner.Ensure(comp)`, research/inbound-edges.md)
becomes `Ensure(comp, deployment)`, and every existing caller passes the
primary. Adding a deployment adds no inbound path.

**Non-primary deployments** are reached only through:
- the **deployment URL** (`/c/<tile>+<name>/`, `/api/<tile>+<name>/…`), for
  humans with at least `write` on the tile;
- the tile's own **terminal and agent sessions**, whose own calls go to their
  **target deployment**;
- the deployment's **own self-calls**.

**Self-calls stay inside the caller's deployment (P12).**
- A backend's instance token records its deployment.
- A frontend's frame token records the deployment of the document it was
  minted for. The claim is present only for non-`main` deployments; a token
  without the claim means `main`.
- A terminal or agent session carries its target.
- A self-call (`/api/<self>/…`) routes to that deployment and gets self-admin
  there, never on another deployment.
- A tile principal calling a *different* deployment of its own tile is
  refused. That stops dev code from reaching the primary's admin surface with
  the tile's credentials. Humans switch deployments by URL, with their own
  session.

**Outbound edges (P3, P11).**
- The primary uses every edge exactly as today.
- Each non-primary deployment uses each edge according to the tile's **edge
  policy** for it:
  - **`read`** (default): the call goes to the provider's **primary**, with
    the effective role clamped to `reader`;
  - **`block`**: the call fails closed with an error that names the policy.
- Callees receive `X-XBin-Deployment: <name>` on calls from a non-primary
  deployment, so a provider that cares can tell. The header is absent on the
  primary's calls, so nothing changes today.
- Edges that cannot be read-clamped have their own safe defaults, defined in
  `09-fabric.md`: a custom role with no `reader` to clamp to, a raw stream, a
  lan-ingress link.
- A later edge-policy value, `match`, resolves to the provider's same-named
  deployment. That is the parallel fabric. It plugs into the same resolver:
  `resolveTarget(caller, callerDeployment, target, edgePolicy)`.
- The net slot is an edge too. Its values are in `09-fabric.md`; the default
  proposal is that non-primary deployments inherit the tile's network, since
  the network belongs to the tile (D54).

**Registrations and side effects from non-primary deployments (P13).**
- **Registrations are stored and dormant.** Cron jobs, bus push
  subscriptions, interface instances and ingress hosts registered by a
  non-primary deployment are stored under that deployment. They are dormant
  unless its deliveries switch is on, and never active in routing: ingress and
  interface instances belong to the primary only. Registration succeeds, so a
  backend that registers at start keeps working in dev.
- **Registrations don't collide.** `main`'s registrations stay in today's
  stores, byte-for-byte. Every other deployment's registrations live in its
  own per-deployment files (§3). They can't overwrite `main`'s rows, and an
  older xbind never loads them as `main`'s (`12-compat.md`). Which set is
  *active* follows the primary role: only the primary's fire, plus any
  non-primary deployment with deliveries on.
- **Notifications are never pushed.** A notification from a non-primary
  deployment is shown to the developers in the deployments panel, as "would
  notify …".
- **Status is per deployment.** Status reports and build events are namespaced
  per deployment, so a dev build can't clear or paint the primary's status.
- **Bus publishes stay in the deployment's namespace.** Publishes to the
  tile's own scope land there. Publishes to other scopes need `writer`, which
  the read clamp removes.

**Which deployment reads which bus.** A non-primary deployment's own-scope
bus is its namespace's bus. Its subscriptions to other scopes' buses read the
provider's primary bus under the `read` policy.

## 8. Events and what clients see

- Everything about the primary is published exactly as today, with the bare
  `component: "<tile>"`. Old clients keep working.
- Everything about a non-primary deployment is published with
  `component: "<tile>+<name>"` and a `deployment: "<name>"` field. Old
  clients' exact or prefix matching (`component === src ||
  startsWith(src + '/')`) ignores those events. A dev build error can never
  paint the primary's frame, clear its status, or reload a shipped iOS app.
- Tile-level changes are announced with one new event type. `11-contract.md`
  names it (proposal: `deployments`). The changes are: live reload attached,
  paused or resumed; deploy, promote or roll back; primary reassigned;
  protection changed; edge policy changed. Unknown types are ignored by old
  clients.
- Frames of a pinned deployment do **not** reload on saves. They reload when
  a deploy changes their deployment's code.

## 9. Data, secrets and state per deployment

- **`main` keeps today's keys, forever (P6).** Storage follows the
  deployment, not the primary role. Enabling deployments never migrates
  production data. Reassigning the primary doesn't move data either.
- **A non-primary deployment's data starts empty (P14).** It lives in its own
  namespace, and seeding it from the primary is a tile-manager act. Resources
  belong to **scopes**, so the namespace is keyed by (scope, deployment name).
  In a multi-tile scope, `apps/a+dev` and `apps/b+dev` share the scope's `dev`
  namespace, just as `main`s share the scope's data today. A sibling with no
  `dev` deployment uses the primary namespace. A same-scope call from
  `apps/a+dev` to `apps/b` is an edge like any other (the read clamp applies),
  never a shortcut into the primary's data.
- **Vault per deployment.** A new deployment gets the primary's key names as
  placeholders. Copying values is a tile-manager act.
- **Logs, status, prefs and agent history are keyed per deployment.** `main`
  keeps today's keys.
- **The env layer (`setup`) is shared by hash.** GC keeps every layer any
  deployment references.

`08-data.md` specifies the keys per resource kind, including kv buckets and
their encryption labels, and resenc mount paths under `.xbin/resenc/**`, which
the installer's AppArmor rule requires. It also covers consistent online
seeding, reset, delete, quotas, backup and offload.

## 10. Authority

Authority is the tile's, and a deployment never widens it (P11). Who may
**operate** deployments:

| Act | Who |
|---|---|
| Open or call a non-primary deployment (its URL, UI, API) | humans with ≥ `write` on the tile; the tile's own terminal/agent sessions (via target) |
| Pause / resume / reload now; attach live reload to a non-primary deployment | `terminal` level (and the tile's terminal/agent tokens), the same power as editing code today; resuming onto the primary is refused while it is protected |
| Add, remove, deploy to, promote to, or roll back a **non-primary** deployment; reset its data; set its target in a terminal | `terminal` level (and the tile's terminal/agent tokens) |
| Deploy, promote or roll back onto the **primary** | `terminal` level and their agents: **parity** with today's save-to-prod (P4). While the primary is protected: tile managers only, and terminal/agent tokens cannot target the primary at all |
| Seed data, copy vault values, deliveries or alwaysOn for non-primary, set edge policies, reassign the primary, protect or unprotect it | tile managers (user-owner, owning-org admins, workspace admins: the D24/D33 gate) |
| Any of the above from a view-as session (D64) | refused (read-only) |

Edge policies are a tile-manager act because they narrow or widen what the
tile's non-primary code may reach in other tiles.

`noTerminal` accounts (D88) are capped at `write`, so they can view
non-primary deployments but not operate them.

## 11. Lifecycle, ownership and the rest of the tile's life

- **Lifecycle** is the tile's. Disabling, hiding or offloading stops every
  deployment. Enabling starts the primary as today; non-primary deployments
  start on demand. Offload and restore cover every deployment's data and the
  checkpoint store (`08-data.md`).
- **Transfer (D39)** moves the tile's deployments with it. Ceilings are
  re-evaluated as today. Edge policies stay.
- **Clone, template instantiation, import and builtin import** copy the work
  tree only. The new tile starts in the zero state.
- **Tile removal:**
  - The deployment record, checkpoint store and non-`main` namespaces are
    path-keyed **leftovers**.
  - D82's refusal list gains them: a new tile at the path must not inherit
    another owner's code history or data.
  - The path's current owner is exempt, as today.
- **Builtin updates (D49) and code PRs (D48)** land in the work tree. They
  therefore reach only the live reload target. With a pinned primary and live
  reload on `dev`, an update is exercised on `dev` and promoted when it works.
  That is safer, and nothing about the PR flow changes.
- **Backups:**
  - A component backup always includes the deployment record, the checkpoint
    store and `main`'s data (today's backup).
  - Non-primary data is opt-in, under its own archive key, so it doesn't
    consume the primary's retention.

## 12. Isolation and runtimes

- **Backends of pinned or non-primary deployments need isolation.** A
  go/node/python backend must see its checkpoint at the tile's canonical path
  (a sandbox bind), and its own data namespace at the paths its env names.
  Without `--isolate` xbind refuses these operations with a clear reason. That
  is D78's fail-closed rule; there is no silent fallback to the work tree.
- **Static-only tiles** (no backend) can pause everywhere: the static plane
  serves the checkpoint.
- **VM backends (D89/D90)** run one guest per deployment, counted against the
  VM budget.
- **`runtime: "cgi"`** is excluded from pinning and non-primary deployments
  until cgi runs sandboxed. It runs on the host today, which is a separate
  urgent fix (research/side-findings.md #1).
- **Chrome tiles** (root, shell, `chrome: true`) **and tiles holding
  `xbin`/`xbin:*` capabilities** may pause live reload: freezing their code is
  safe. They may not have non-primary deployments, because workspace
  governance has no deployment dimension (P19). A dev copy of the admin tile
  would act on real users and grants.
- **Resource caps:**
  - The number of non-primary deployments per tile and per workspace is
    capped.
  - Each deployment gets its own cgroup leaf.
  - `07-runtime.md` sets the numbers.

**Sandbox mechanics: the shared layer.** This design is based on the
`sandbox-visibility` branch: D112's registry is landed and D113's tile-managed
sandboxes are designed ([research/sandbox-visibility.md](research/sandbox-visibility.md)).

Tile deployments are **not** built on D113's tile-managed sandboxes (the
owner's direction, 2026-09-27). A deployment's backend is a runner backend,
with its blue/green, health check, reaper, alwaysOn, env layer and ingress
plumbing. A tile sandbox is an exec box driven by its tile, with no serving
lifecycle.

The dev lifecycle does change the mechanics **one layer up**, which backends,
terminals and tile sandboxes all share:
- **The D112 registry** gains a deployment dimension on backend entries.
  `main`'s entries keep today's ID and shape, and new fields are added on
  rows, never as new keys.
- **Budgets:** VM reservations of every deployment are charged to the tile
  (`Reserve(owner = tile)`). A deployment is never its own budget owner.
- **A per-tile cgroup parent** holds a leaf per deployment. This is the
  "nested per-tile cgroup" D113 defers: both projects need it, so it is built
  once, in the shared layer.
- **Backend launch-spec binds:**
  - the checkpoint is bound at the canonical path;
  - the deployment's data namespace is bound at the primary's resource paths,
    so `XBIN_RES_*` stays identical;
  - the live reload target keeps today's work-tree bind.
- **confine** gains an explicit bind destination, so confined builds can see a
  checkpoint at the tile's canonical path.
- **Streaming (M3):** a streaming confined run, for the deploy remote's
  `receive-pack`, follows D113's exec-protocol precedent.

When D113 is built, **tile-managed sandboxes belong to the deployment**:
- a non-primary deployment's backend addresses its own sandbox set (keyed per
  deployment, like dormant registrations), never the primary's;
- `{"source":true}` mounts that deployment's code;
- resource mounts resolve in its data namespace;
- per-tile caps and `cap:sandboxes` stay the tile's.

## 13. Invariants (the proposed decisions)

| # | Invariant | Status |
|---|---|---|
| P1 | Deployments are pointers over an xbind-owned checkpoint store. The work tree is the default feed; tracked branches and a deploy remote are later feeds on the same core. | ratified 2026-09-27 |
| P2 | The term is *tile deployment*. "Identity" stays reserved for principals. | ratified 2026-09-27 |
| P3 | Non-primary outbound: the provider's primary, read-clamped, with a per-edge `block`. `match` (the parallel fabric) later, on the same per-edge record. | ratified 2026-09-27 |
| P4 | Parity: terminal-level users and their agents deploy to the primary as saving does today. Tile managers can protect the primary. | ratified 2026-09-27 |
| P5 | The zero state is byte-for-byte today. Opting out returns to it. No manifest key. | proposed |
| P6 | Storage follows the deployment. `main` owns today's keys forever. | proposed |
| P7 | Primary is a role. Only the primary receives inbound edges. Reassignment moves routing, not data. | proposed |
| P8 | Live reload attaches to at most one deployment, which runs the work tree directly. The default path adds no per-save cost. | proposed |
| P9 | Pinned means pinned. Every restart path runs the pinned checkpoint, and built artifacts are kept per checkpoint. | proposed |
| P10 | Promotion moves code only. Data, vault, config and routing are late-bound to the target. | proposed |
| P11 | Authority stays per tile. A deployment is not a principal, and non-primary deployments are narrowed by edge policy. | proposed |
| P12 | Self-calls stay inside the caller's deployment. Tile principals can't call another deployment of their own tile. | proposed |
| P13 | Non-primary registrations are dormant. Notifications aren't pushed. Status and events are namespaced. | proposed |
| P14 | Non-primary data and vault start empty. Seeding and vault copy are tile-manager acts. | proposed |
| P15 | Deployment state is xbind-owned (`data/`), never in the root `xbin.json` or the work tree. | proposed |
| P16 | Every git or tool run touching tile code happens in confine. The checkpoint store is confine-only. Materialized trees are served with containment and never followed out through symlinks. | proposed |
| P17 | Deployment URL qualifier `+`; the bare URL is the primary. Non-primary markers (env, header, event, frame-token claim) are additive and absent for `main`. | proposed |
| P18 | Pinned or non-primary backends need isolation. Static tiles pause everywhere. cgi is excluded until sandboxed. | proposed |
| P19 | Chrome tiles and xbin-capable tiles may pause, but can't have non-primary deployments. | proposed |
| P20 | A non-primary deployment is an accident boundary, not a trust boundary. | proposed |
| P21 | A protected primary cannot be the live reload target. Every change to its code is a tile-manager act, and terminal/agent tokens cannot target it. | proposed |

## 14. Worked flows

These are the flows every other document must support. The UX of each is in
`10-ux.md`.

**A — Pause, fix, reload now, resume** (single deployment, no new concepts).
1. Ana's tile is in heavy use, and she needs to try a risky change. In the
   terminal window she pauses live reload. `main` keeps running the same
   code, now pinned to `c:3f2a1c9`.
2. She edits. Saves reload nothing, and the bar counts the files changed
   since the checkpoint.
3. She presses **Reload now**. The work tree is checkpointed and deployed to
   `main`, with the usual blue/green swap, and every viewer's frame reloads
   once. It is still paused.
4. Later she resumes live reload. `main` follows the work tree again. The
   tile returns to the zero state.

**B — A dev deployment for a busy tile.**
1. Ana adds deployment `dev`: code from the work tree, data empty, live reload
   attached to `dev`.
2. `main` is pinned to a fresh checkpoint of the work tree. Production sees no
   change.
3. Her saves now reach `/c/apps/crm+dev/`. A manager seeds `dev` from `main`
   when realistic data is needed.
4. When `dev` looks right, **Promote dev → main** shows the diff between
   `main`'s checkpoint and a fresh checkpoint of the work tree, then deploys
   it to `main`.
5. `main`'s data is untouched, and `dev` keeps following the work tree.

**C — An agent iterating on dev.**
1. An agent session on the tile has target `dev` (`XBIN_DEPLOYMENT=dev`). Its
   `curl $XBIN_URL/api/$XBIN_COMPONENT/…` and `bx` calls hit `dev`.
2. It commits often (CM-2); committing deploys nothing.
3. It may `bx promote apps/crm dev main` (parity), unless the primary is
   protected. Then the promotion is Ana's, as a manager, and the agent's
   attempt is refused with a message that says so.

**D — Roll back.**
1. A promotion broke something. From `main`'s deploy log, Ana rolls `main`
   back to the previous checkpoint.
2. `main`'s data stays as it is. A rollback moves code, not state, and the UI
   says so.

**E — A builtin update tested on dev.**
1. The Updates tab files a D49 PR. The tile's own plane applies it in the work
   tree, which reaches `dev`.
2. `main` is unaffected until promotion.

**F — Cut over with data (advanced).**
1. A schema migration was rehearsed on a seeded `dev`.
2. A manager reassigns the primary to `dev`. Every inbound edge now reaches
   `dev`, and `dev`'s data.
3. The UI requires confirming that the old primary's data does not follow.
4. `main` remains, pinned. Its registrations are dormant. It can become the
   primary again.

**G — A multi-tile scope.**
1. `apps/shop` and `apps/shop-admin` share a scope. Both get a `dev`
   deployment, which share `res:apps/shop/*` in the scope's `dev` namespace.
2. `apps/shop-admin+dev`'s calls to `apps/shop` go to `apps/shop`'s primary
   under the read clamp. Calls resolve to the provider's primary, and the
   calling tile's deployment name doesn't change that.

   This is the case `match` will improve: `match` would route to
   `apps/shop+dev`.

**H — A hotfix while dev holds unfinished work.**
1. `main` is pinned to `c:3f2a1c9`. Live reload is on `dev`, and the work tree
   holds unpromoted work.
2. A production bug needs a fix now. In the tile's terminal:
   - `git fetch xbin-deploy` (the read-only checkpoint remote);
   - commit or stash the unfinished work on a branch;
   - `git checkout -b hotfix deploy/main`;
   - fix and commit.
3. The work tree now holds `main`'s code plus the fix. `dev`, which follows the
   work tree, runs it too, so the fix is exercised there first.
4. **Deploy to main** ships it.
5. The developer checks out the unfinished branch again (and rebases it on the
   hotfix), and `dev` follows.
6. Nothing in xbind merged anything. Git did the combining in the work tree,
   and deployments only moved checkpoints (the "promotion is not a merge"
   rule).

## 15. Deliberately not in this model

**Later rungs** (designed for, not built in v1):
- **Tracked branches and the deploy remote**, as additional feeds (M3). They
  add checkpoint sources and nothing else. The deploy remote needs a
  streaming confine (research/git-confine.md).
- **Name matching** (edge policy `match`) and any cross-tile "environment"
  object built on it (M3).
- **Traffic splits and gradual rollouts** (the Cloudflare pattern). The model
  has one primary, and a later split would be a routing feature on the same
  funnel.
- **Per-deployment ingress hostnames** for testing webhooks against a
  non-primary deployment. Ingress belongs to the primary. A later rung could
  let a manager route a test hostname to a named deployment.
- **Data flowing back** (dev → primary): never automatic. Promotion moves
  code; data changes to the primary happen through the primary's own code or
  its migrations.
- **A trust boundary.** Running code written by people who can't write the
  tile (PR previews) needs separate principals, and is out of scope (P20).
- **A deployment dimension for chrome and governance tiles** (P19).
