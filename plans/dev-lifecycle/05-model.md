# 05 — The model: tile deployments over a checkpoint store

> Status: live — the recommended model the rest of
> [plans/dev-lifecycle/](README.md) elaborates. Terms are defined in
> [01-glossary.md](01-glossary.md) and used verbatim. Of the invariants
> P1–P29 (§13), P1–P4 were ratified by the owner on 2026-09-27, and P7, P18,
> P19 and P22–P24 are owner-confirmed the same day. The rest are proposed and
> become D-numbers only at ratification. [04-options.md](04-options.md) argues
> each choice against the alternatives. [research/](research/README.md) holds
> the evidence behind every "today" statement.

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
                 (read → provider primary, role clamped to reader | inherit | block;
                  later: match)
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
- There is no deployment record and no new process. Its backend stays in
  today's flat `comp-<CompKey>` cgroup leaf
  (`internal/cgroup/cgroup_linux.go:89`, `internal/runner/runner.go:492`), and
  its D112 registry rows keep today's IDs (`backend:<CompKey>:g<gen>`,
  `internal/runner/sbx.go:63`) with no deployment field.

The zero state is **the absence of a deployment record**: a tile without one
behaves byte-for-byte as today, whatever else is on disk.

Opting in is an action on a tile: pausing live reload, or adding a
deployment. The checkpoint store is created only by a committed opt-in; dry
runs and diffs never create it (§5). Opting out is the reverse. Resuming live
reload on a tile whose only deployment is `main`, with default settings,
**returns it to the zero state**: the record is removed. The checkpoint store
and its deploy log are kept under normal GC (a tile manager may purge them),
so a later opt-in can still roll back to earlier checkpoints. Nothing reads
them while there is no record: no fetch remote is injected or served, no event
names the tile, nothing runs. The view repository (§3) is derived and is
removed with the record. A tile that never opted in has no store at all.

Every existing tile, and every tile created from now on, stays in the zero
state until someone acts on it. No manifest key is involved. Deployments are
an operator choice, like bindings and lifecycle. The tile author's code is
untouched.

## 3. Objects and where they live

`<TileKey>` is a 128-bit hash of the tile path. The new stores below
(`data/deployments`, `data/checkpoints`, `.xbin/deploy`) use it because
`CompKey` keeps only 32 bits and can be ground (`internal/util/util.go:137`);
a collision-free key costs nothing on a new store and removes the collision
denial of service. Existing stores keep their keys.

| Object | Key | Stored | Written by |
|---|---|---|---|
| Tile | path | work tree + registry (unchanged) | as today |
| Deployment record | tile path | `data/deployments/<TileKey>.json`. The record stores the full tile path, the owner ref and a creation stamp, and is ignored unless all three match the tile it is read for (§11). xbind-owned, masked from terminals, in component backups. | the deployments API only |
| Checkpoint store | tile path | `data/checkpoints/<TileKey>.git`, a bare repository | confined git only (D78), private config, no hooks or fsmonitor |
| View repository | tile path | `data/checkpoints/<TileKey>.view.git`, a bare repository holding only the git views of pinned deployments' checkpoints (below); derived from the store, rebuildable | confined git only |
| Checkpoint | its full tree hash (short id `c:<7+ hex>`, lengthened until unique in the tile's store; records, logs and directory names use the full hash) | an object in the store; materialized read-only under `.xbin/deploy/<TileKey>/<full tree hash>/` (derived, rebuildable) | confined git |
| Deploy log | (tile, deployment) | in the store: one commit chain per deployment (`refs/xbin/log/<name>`), one entry per finished attempt, failed ones included; `how` names the operation (`deploy`, `promote`, `rollback`, `reload-now`, `resume`, `pause`, `attach`, …; `11-contract.md` owns the list) | confined git, run by the deployments API |
| Deployment data | (scope, deployment name) | `main`: today's paths and keys; others: a dot-level namespace no existing key can produce (the exact scheme is in `08-data.md`) | the resource broker |
| Built artifact | (tile, checkpoint) | `.xbin/build/<CompKey>/…`, one per checkpoint while referenced; the live reload target keeps today's single `bin` | the runner (confined builds) |
| Dormant registration | (tile, deployment, name) | one directory per deployment beside the record (e.g. `data/deployments/<TileKey>/<name>/cron.json`), **never** extra rows in today's `data/cron-jobs.json` / `data/bus-subscriptions.json` / root `xbin.json` maps. An older xbind would load such rows as `main`'s and fire them. `main`'s registrations stay exactly where they are today. | the self-scoped APIs |
| Edge policy | (tile, edge) | the deployment record | the deployments API |

**Checkpoints are fetchable, read-only, by the tile's own sessions.** While a
tile has a deployment record, its terminal and agent sessions get a
fetch-only git remote, `xbin-deploy`. It is injected per session through
`GIT_CONFIG_*` env entries next to today's rewrite
(`internal/term/term.go:789`), and never written into the tile's
`.git/config`: clone copies `.git`, and opting out or downgrading must leave
nothing behind. Its refspec is `+refs/heads/deploy/*:refs/deploy/*`, so
`deploy/<name>` resolves after `git fetch xbin-deploy`.

The remote is served from the tile's **view repository**, never from the
store:
- It holds only `refs/heads/deploy/<name>` for each pinned deployment, plus
  `HEAD` naming the primary's ref (dangling while the primary follows the work
  tree). `deploy/<name>` exists only while `<name>` is pinned.
- No `refs/xbin/*` ref is ever advertised, and no object outside the git views
  is fetchable, because the view repository holds no other objects.
- Each ref points at the checkpoint's **git view**: the checkpoint minus every
  path the tile's own ignore rules exclude, evaluated in confine at capture
  and kept in the store with its checkpoint (never advertised), so the view
  repository can be rebuilt. The commit message names the work-tree HEAD the
  checkpoint was captured from. A branch made from the full checkpoint would commit `node_modules`,
  `.env` and build output into the tile's history, and the next branch switch
  would delete them from the work tree.

It is static dumb-HTTP from an allow-list only (`HEAD`, `info/refs`,
`objects/info/packs`, loose objects, packs). Files are opened beneath the view
repository and every symlink is refused, never through the `template`
remote's `http.ServeFile` (`internal/broker/templaterepo.go:226`). A confined
`update-server-info` refreshes it after each change to its refs. Only the
tile's own terminal and agent sessions, and humans with at least `write` on
the tile (their current level, checked per request), may fetch. Nothing is
ever pushed through it; pushing is the separate deploy-remote rung.

**Why the deployment record isn't in the root `xbin.json`:**
- Older binaries re-marshal that file through a struct and drop keys they
  don't know.
- Boot must never rewrite it (the legacy fixture).
- It is git-tracked and visible to admin terminals. Moving the primary's code
  must never be a hand edit.

The D24 ownership precedent applies: a tile terminal must not be able to edit
what the primary runs except through the gated API.

## 4. The deployment record

Illustrative shape; `11-contract.md` owns the wire.

```jsonc
{
  "schema": 1,
  "tile": "apps/crm",                    // verified against the tile it is read for
  "owner": "user:ana", "created": "…",  // the tile this record belongs to (§11)
  "seq": 19,                             // compare-and-set for reviewed operations (§5)
  "liveReload": "dev",                   // the attached deployment, or "" = paused
  "lastLiveReload": "dev",               // default for resume and reload now
  "liveReloadSince": {"by": "user:ana", "at": "…"},
  "primary": "main",
  "protectedPrimary": false,
  "edges": {"slot:llm": "read", "grant:apps/calendar": "block", "slot:net": "inherit"},  // non-primary only
  "deployments": {
    "main": {"checkpoint": "c:3f2a1c9…", "created": "…", "by": "user:ana"},
    "dev":  {"checkpoint": null, "deliveries": false, "alwaysOn": false,
             "limits": {},              // empty = the tile's limits (P22)
             "created": "…", "by": "user:ana"}
  }
}
```

A deployment's data state (empty, seeded, reset: who and when) is not stored
in the record. It is derived from its (scope, name) namespace's own metadata,
because sibling tiles share that namespace (`08-data.md`).

Invariants on the record:
- `tile`, `owner` and `created` bind the record to one tile. A record that
  doesn't match the tile it is read for is ignored (§11, P29).
- `seq` grows on every committed change. Reviewed operations compare-and-set
  on it (§5).
- `liveReload` is `""` or the name of exactly one deployment, and that
  deployment's `checkpoint` is `null`. Every other deployment has a
  checkpoint.
- A deployment's `checkpoint` is the code every restart runs. After a failed
  move off the work tree it is the attempted checkpoint, with state `failed`
  (§5).
- `primary` names an existing deployment. `main` always exists.
- Names follow the deployment-name grammar in the glossary, and are immutable.

## 5. Operations

Every operation is atomic on the record. Operations on one deployment
serialize (one deploy in flight per deployment; later ones queue). A deploy
that fails leaves the deployment serving its previous generation. The failure
is reported to the actor, in the `deployments` event and in the deploy log,
never as a bare `build-error` on the primary (§8). The record's `checkpoint`
is always the code every restart runs:
- a pinned → pinned deploy that fails keeps the previous checkpoint;
- a move off the work tree (pause, attach, a deploy onto the live reload
  target) has no previous checkpoint to keep, so the record points at the
  attempted checkpoint from the moment of the request, with state `failed`.
  Every restart then runs that checkpoint, never the work tree, and the static
  plane serves it at once.

Authority is set out in §10; events in §8.

| Operation | Precondition | Effect |
|---|---|---|
| **Pause live reload** | live reload attached to X | `liveReload = ""` (`lastLiveReload` keeps X). Checkpoint the work tree and deploy it to X, which is pinned to it. **Normally** the checkpoint is exactly what X was running: a file-identical swap within the checkpoint's fidelity limits, which moves X off the work tree. A Go backend needs a warm-cache rebuild, because the watcher ignores directories a checkpoint includes (`07-runtime.md`). **If the work tree moved since X's last successful build**, pausing live reload ships that newest state once, which is what live reload was about to do. **If that build fails**, live reload stays detached. X is recorded pinned to the attempted checkpoint (state `failed`) and keeps serving its current generation until a reload now succeeds; any restart runs the attempted checkpoint. That is no worse than today, where a crash with a broken work tree cannot rebuild either. |
| **Resume live reload** (onto X, default: the last target) | paused; X not a protected primary | `liveReload = X`. X deploys from the work tree. Its code tracks every save again. |
| **Reload now** | paused | Checkpoint the work tree and deploy it to the last target (`lastLiveReload`). It stays pinned. |
| **Attach live reload to Y** | live reload on X, X ≠ Y; Y not a protected primary | Checkpoint the work tree and pin X to it. Y deploys from the work tree and follows it. |
| **Add deployment Y** | the name is free; no component exists at `<tile>+<Y>`; within the per-tile cap | Create Y, with code from the work tree (fresh checkpoint), the primary's checkpoint, or a named checkpoint. Data: empty (default) or seeded (manager only). Vault: key names only. Deliveries and alwaysOn off. Optionally attach live reload to Y, which pins the previous target. |
| **Deploy to X** | — | Put a checkpoint on X (default: a fresh checkpoint of the work tree). If X was the live reload target, live reload pauses. |
| **Promote A → B** | A ≠ B | B receives A's current code: A's checkpoint, or a fresh checkpoint of the work tree if A follows it. Only code moves. If B was the live reload target, live reload pauses. When A follows the work tree, the diff or dry run the actor reviews names the checkpoint it captured, and the promote deploys exactly that checkpoint (`expect`; a 409 if the work tree moved since). Onto a protected primary `expect` is mandatory (*Reviewed operations*, below). |
| **Roll back X to C** | C is in X's deploy log (or any checkpoint of the tile) | Deploy C to X. It pauses live reload if X was the target. |
| **Remove deployment Y** | Y ≠ `main`; Y not primary | Stop Y. Delete its vault, logs, dormant registrations and artifacts. Its (scope, name) data namespace is deleted only when no other tile of the scope still has a deployment of that name (§9); an orphaned namespace is listed to admins and deleted after a grace period. Its checkpoints remain until GC. Detach live reload if Y held it. |
| **Reassign primary to Y** | (M2) Y exists and is healthy. Tile managers only, with a loud confirmation that names whose data the new primary serves and says the old primary's data stays behind. In v1, the tile is in the workspace scope or is the only member of the scope it roots, because reassigning one member would split the scope's primary data. | Routing only (§7). Data does not move, and the root `xbin.json` is never written. Role-dependent wiring is captured at spawn (net edge, lan-ingress legs, rosters, `XBIN_DEPLOYMENT`), so Y starts as primary first, then the old primary is restarted or stopped. The old primary's long-lived streams are cut. `reload` is emitted for the bare component. The old primary's registrations become dormant and Y's activate. Y's dormant ingress hosts are re-validated: a conflicting host stays inactive and is reported. |
| **Protect / unprotect primary** | — | While protected, only tile managers change the primary's code, and live reload cannot attach to the primary. Protecting detaches it if attached (the primary is pinned in place). Protecting restarts the sessions that target the primary onto P24's default, or ends them. While protected, the primary's env layers, build caches and artifacts come only from manager-initiated operations (§9). A protected primary never follows a tracked branch and takes no deploy-remote push (M3). |
| **Seed Y** | Y non-primary; Y stopped for the copy | Copy the primary's deployment data into Y's namespace, consistently, re-keying encrypted stores (`08-data.md`). This is a PII-bearing act, so the UI warns. Seeding is optional: most non-primary deployments run on empty or synthetic data that their own code creates. In a shared namespace, every sibling's same-named deployment stops for the copy, and the actor needs the act's authority on each of those tiles. |
| **Reset Y** | Y non-primary | Empty Y's data namespace; in a shared namespace, with the same rules as seeding. The vault is kept unless asked (`vault: true`). Resetting `main`'s data while `main` isn't primary is a tile manager's act, because `main` holds today's keys. |
| **Vault copy to Y** | Y non-primary | Copy the selected keys' values from the primary into Y's vault. |
| **Deliveries on/off for Y** | Y non-primary | Activate or deactivate Y's dormant registrations for Y. |
| **alwaysOn on/off for Y** | Y non-primary; the manifest (Y's checkpoint) says `alwaysOn` | Honour D84 for Y. It is never implied for non-primary deployments. |
| **Set edge policy** | the edge exists on the tile | `read` \| `block` for edges that can be read-clamped; `inherit` \| `block` for the net slot and capability grants (`inherit` never reaches host networking or a provider splice, §7); `block` only for edges that cannot be read-clamped (P23). Applies to all of the tile's non-primary deployments; later also `match`. |
| **Run now** (a dormant cron job of Y) | the job is dormant on Y; `terminal` level; one run in flight per job | Deliver that job once to Y, as a manual trigger (the Netlify precedent). |

**Reviewed operations onto a protected primary.** Each names the checkpoint
its actor reviewed: `checkpoint` on deploy and roll back; `expect` on promote,
reload now and reassignment. A request without it is refused with 400. The
check is a compare-and-set on the record's `seq`, with the actor's authority
re-checked at commit; a record or work tree that moved since the review
answers 409. `11-contract.md` owns the fields.

**Dry runs and diffs never create a checkpoint store on a zero-state tile.**
- Dry runs of pause live reload and add deployment compute the impact without
  capturing.
- A work-tree diff on a tile without a record either captures into a
  throwaway `GIT_DIR` under `.xbin/`, removed before the answer, or answers
  409 `record:false`; `11-contract.md` fixes which.
- The store exists only after a committed opt-in.

**Promotion is not a merge.** Nothing is combined: B's code becomes exactly
A's code. Combining work is a git activity in the work tree (branches, PRs),
and it produces the checkpoint that is then deployed.

**A pinned deployment's code changes only through the rows above.** That
holds through every restart path: idle reap, crash restart, grant or binding
change, provider nudge, alwaysOn backoff, re-enable, xbind restart, loss of
`.xbin/`, interface-instance registration, ownership transfer and vault seal
(P9).

## 6. What code and which manifest a deployment runs

The tile's manifest mixes three kinds of fields. With deployments they
separate cleanly:
- **Tile-level** fields describe the tile's existence and request authority.
  They come from the **work tree**, as today.
- **Inbound-surface** fields say what the tile exposes and provides, and how
  it is framed. They come from the **primary's code**.
- **Deployment-level** fields describe how a particular body of code runs.
  They come from the deployment's own code: the work tree for the live reload
  target, the checkpoint otherwise.

| Tile-level: authority requests (the work tree) | Inbound surface (the primary's code) | Deployment-level (the deployment's own code) |
|---|---|---|
| existence (`xbin.json` / `index.html`), path, scope membership, `uses`, `interfaces`, `deps` | `template`, `exposes`, `expose.roles`, `provides`, `chrome` | `runtime`, `entry`, `setup`, `alwaysOn`, `vm`, `inject`, `native`, `scope.json`'s `resources`, and every file served or executed |

Consequences:
- **The inbound surface follows the primary's code.** Only the primary
  receives inbound edges (P7), so what the tile exposes and provides, and how
  it is framed, come from the primary's code: the work tree while the primary
  is the live reload target (today, and the zero state), its checkpoint while
  pinned. A save while live reload is paused therefore changes nothing that
  serves traffic, and a protected primary's framing and routes change only
  through a manager's deploy. A non-primary deployment's documents are never
  chrome.
- **Resources are provisioned per deployment, from the deployment's own
  code** (P22, owner, 2026-09-27). A non-primary deployment whose code
  declares a new kv, sqlite or fs resource gets it provisioned in its own
  namespace, and it exists nowhere else. The primary gains it only when code
  declaring it is promoted; a pinned primary provisions from its checkpoint's
  `scope.json`. A checkpoint's `scope.json` is read with `OpenBeneath`
  (`internal/fsutil/beneath.go:34`) and validated (the resource-name charset)
  before anything is provisioned. Development usually needs more resources
  than the older code on the primary, so resources split per deployment
  rather than lock-step. A resource the primary's code no longer declares is
  kept, not deleted, which matches today's behaviour. For a scope whose
  `scope.json` belongs to no tile (a plain-directory scope), the declarations
  come from that file for every deployment. Resource *limits* (quota share,
  cgroup memory) are per-deployment settings: they default to the tile's, are
  set by tile managers, and never exceed the tile's ceilings (`08-data.md`,
  `07-runtime.md`).
- **Authority requests track the tile's latest intent.** A `uses` or
  `interfaces` entry added in the work tree files its pending grant or binding
  as today, and approval grants the tile. A pinned deployment's `XBIN_RES_*`
  env is built from the union of the work tree's and its own code's `uses`,
  filtered by the tile's grants, so a work-tree edit cannot strip a pinned
  deployment's resources at its next restart.
- **Bindings are the tile's.** Every deployment gets the tile's slot env; code
  that doesn't know a slot ignores it. For non-primary deployments the edge
  policy decides what a slot can actually reach (§7).
- **A pinned primary survives the work tree.** A tile with a deployment record
  stays registered while its directory exists, even if the work tree
  temporarily lacks `xbin.json`/`index.html` (an agent checking out an old
  branch, a refactor in progress). The pinned primary keeps serving. Only
  lifecycle state, or the directory disappearing, takes the tile down. A
  tile in the zero state behaves exactly as today. When the work tree has no
  valid manifest, the tile-level fields fall back to the primary's checkpoint
  manifest, and the manifest error is surfaced.
- **Cross-tile references resolve to the other tile's primary where xbind
  resolves them:**
  - absolute `/c/<other>/…` imports, through routing;
  - `deps/` links inside a checkpoint, resolved by path through the other
    tile's `/c/` plane and never followed on disk (P16);
  - `go.work` modules in confined builds, which get the other tile's pinned
    primary checkpoint (or its work tree, while that primary follows it) bound
    over their directory.

  A checkpoint pins the tile's own files only; `07-runtime.md` records the
  reproducibility caveat. A zero-state tile that references a tile with
  deployments sees that tile's primary. That is the other tile's change: P5 is
  per tile.
- **`/components` follows the same split** (`internal/server/api.go:129-160`).
  Its deployment-level fields (`runtime`, `hasIndex`, `native`) and
  `/c/<tile>/?native=1` describe the primary's code (its checkpoint when
  pinned), and `chrome` follows the inbound surface. `manifestError`, `roles`,
  `uses` and `deps` keep describing the work tree.

## 7. Routing

**Inbound edges reach only the primary (P7).** Every existing entry point
resolves `(tile) → primary`. That covers:
- the bare `/c/<tile>/` and `/api/<tile>/`;
- consumers' interface bindings and grants to call the tile;
- ingress routes and terminator forwards;
- stream and lan-ingress links, and net-provider splices;
- cron ticks, bus push deliveries, event triggers and alwaysOn;
- archiver calls, notify links, and the native app.

Every runner map keyed by path today gains the deployment: generation state
and the current generation, the run dir and its sockets, net-link keys, the
proxy's transport pool, the reaper, alwaysOn, and Stop/Status/Inspect. New
names (`EnsureDeployment`, …) sit beside today's funnel (`Runner.Ensure`,
`internal/runner/runner.go:190`; research/inbound-edges.md), which keeps
meaning the primary. Callers pass the deployment as follows:
- inbound-edge callers pass the primary;
- `Changed` and alwaysOn pass the deployment they act on;
- tile-level restarts and stops (grant, binding and interface-instance
  changes, transfer, vault seal, lifecycle) reach every running deployment.

Every inbound edge resolves through one resolver, which returns the primary
for every v1 edge-policy value; `match` adds its exception there (M3). Adding
a deployment adds no inbound path.

**Non-primary deployments** are reached only through:
- the **deployment URL** (`/c/<tile>+<name>/`, `/api/<tile>+<name>/…`), for
  humans with at least `write` on the tile.
  - The check uses the user's current level on every request, because frame
    tokens renew for 15 minutes (`internal/server/static.go:24`). It never
    uses the bare `p.Component == tile` test, which also admits a reader's
    frame principal.
  - A qualified URL resolves only for tiles with a record, and only after
    today's resolution fails. `<tile>+<primary name>` is for humans and the
    tile's own principals; other tiles use the bare URL.
  - Two narrow refusals, for every creator, keep it unambiguous: no tile at
    `<P>+<N>` while `P` has deployment `N`, and no deployment `N` on `P` while
    a component exists at `<P>+<N>`. Other new tile names containing `+` get a
    one-release warning, never a refusal.
  - In the legacy asset mode, subresources stay authorized by path: they are
    code, which the tile's readers may already read (§3). Documents and
    `/api/` are write-gated in every mode.
  - In origins mode (D95) each deployment has its own origin label, keyed by
    name. `main` keeps today's label, and the bare URL goes to the primary's
    origin.
- the tile's own **terminal and agent sessions**, whose calls go to their
  **target deployment** (P24).
  - The target is fixed for the session's life: a named deployment or "the
    primary". Changing it restarts the session.
  - It is chosen in the terminal's API dropdown, which has one entry per
    reachable deployment; there is no separate target picker. The default is
    the primary. A protected primary is not offered, and the default then
    falls to the live reload target. When neither exists, the dropdown falls
    to "API off".
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
  refused. That stops non-primary code from reaching the primary's admin
  surface with the tile's credentials. For instance and frame tokens the
  refusal is hard. Terminal and agent sessions can change target by
  restarting, which is a terminal-level act, so for them it is an accident
  guard (P20). Humans switch deployments by URL, with their own session.

**Outbound edges (P3, P11).**
- The primary uses every edge exactly as today.
- Each non-primary deployment uses each edge according to the tile's **edge
  policy** for it:
  - **`read`** (default for edges that can be read-clamped): the call goes to
    the provider's **primary**, with the effective role clamped to `reader`;
  - **`inherit`** (role-less edges only: the net slot and capability grants):
    the tile's own authority, never host networking or a provider splice;
  - **`block`**: the call fails closed with an error that names the policy.
- Callees receive `X-XBin-Deployment: <name>` on calls from a non-primary
  deployment, so a provider that cares can tell. The header is absent on the
  primary's calls (the role rule, P17), so nothing changes today.
- **Edges that cannot be read-clamped are blocked for non-primary deployments
  in v1, with no override (P23).** These are custom roles with no path to
  `reader`, stream interfaces, lan-ingress links, and net-provider splices.
  Two consequences to state plainly:
  - The agent tile reaches its sandbox managers through a custom role (the
    sandbox-managers programme, on another branch). A non-primary deployment
    of the agent tile is therefore blocked from them.
  - llm-gw guards completions with `writer`. Under the read clamp, every
    LLM-using tile's non-primary deployments can list models but can't run a
    turn.

  Refusals and clamps are visible: the deployments panel counts them per
  edge, and a clamped call's response names the clamp.
- A later edge-policy value, `match`, resolves to the provider's same-named
  deployment. That is the parallel fabric. It plugs into the same resolver:
  `resolveTarget(caller, callerDeployment, target, edgePolicy)`.
- The net slot is an edge: `inherit` (the default) or `block`. `inherit` gives
  non-primary deployments the tile's relay policy, since the network belongs
  to the tile (D54), but never host networking or a provider splice:
  - a tile whose `net` resolves to host sharing (the `host` builtin, or an
    org, personal or named set whose rules say host: `Broker.NetHostShare`,
    `internal/broker/netfn.go:196`) gives its non-primary deployments no
    egress (`block`, P23), and the panel shows why;
  - a net slot bound to a provider tile is a splice, so non-primary
    deployments get no egress through it (P23).

  Capability grants are edges too, with `inherit` | `block`: `gpu:*` defaults
  to `block` (a shared device that cgroups don't cover), the others to
  `inherit`.
- A value this xbind doesn't know, or one invalid for the edge's kind, reads
  as `block`, so a later `match` fails closed on an older binary. When several
  edges authorize one call, any `block` among them refuses it (P27).

**Registrations and side effects from non-primary deployments (P13).**
- **Registrations are stored and dormant.** Cron jobs, bus push
  subscriptions, interface instances and ingress hosts registered by a
  non-primary deployment are stored under that deployment. They are dormant
  unless its deliveries switch is on, and never active in routing: ingress and
  interface instances belong to the primary only. Registration succeeds, so a
  backend that registers at start keeps working on a non-primary deployment.
- **Registrations don't collide.** `main`'s registrations stay in today's
  stores, byte-for-byte. Every other deployment's registrations live in its
  own per-deployment files (§3). They can't overwrite `main`'s rows, and an
  older xbind never loads them as `main`'s (`12-compat.md`). Which set is
  *active* follows the primary role: only the primary's fire, plus any
  non-primary deployment with deliveries on.
- **Notifications are never pushed.** A notification from a non-primary
  deployment is shown to the developers in the deployments panel, as "would
  notify …".
- **Status is per deployment.** A non-primary deployment's status, notices and
  build activity travel only in the `deployments` event (§8), so a
  non-primary build can't clear or paint the primary's status, and a
  non-primary status never reaches an old shell's toasts or title mark.
- **Bus publishes stay in the deployment's namespace.** Publishes to the
  tile's own scope land there. Publishes to other scopes need `writer`, which
  the read clamp removes.

**Which deployment reads which bus.** A non-primary deployment's own-scope
bus is its namespace's bus. Its subscriptions to other scopes' buses read the
provider's primary bus under the `read` policy. A bus push subscription on
another scope's bus is re-checked against its edge at every delivery.

## 8. Events and what clients see

- Everything about the primary is emitted exactly as today, with the bare
  `component: "<tile>"`. `build-*` keep today's meaning, for the primary only:
  builds of the work tree while the primary follows it, and rebuilds of the
  code it already runs.
- **Non-primary activity never uses today's event types** (`reload`,
  `build-*`, `status`, notify). No qualified `component` string appears on an
  old event type, ever: not for deploys, not for reassignment, not for
  lifecycle. A qualified component would not protect old clients:
  - ancestor frames and the shipped app claim `<ancestor>/…` components, so
    `apps/shop/admin+dev` would reload an open `apps/shop` frame;
  - the old shell stores, toasts and title-marks a `status` for any
    component;
  - several consumers refetch on any `reload`.

  Such activity travels in the new `deployments` type, which carries the
  deployment name. Frames served at a deployment URL listen for it.
- A deploy that puts a checkpoint on a deployment reports its phases and
  result through `deployments`, never as `build-start`/`build-error`.
  - A failed deploy onto a pinned primary paints no overlay. The primary keeps
    serving, and the actor, the panel and `bx` see the failure.
  - A successful swap on the primary emits one `reload` for the bare
    component, only when the primary's code changed, and clears the primary's
    status at the swap, not at build start. A non-primary swap is announced
    only in `deployments`.
- `deployments` also announces tile-level changes: live reload attached,
  paused or resumed; deploy, promote or roll back; primary reassigned;
  protection, edge policy, deliveries or alwaysOn changed; data seeded, reset,
  restored or vault-copied; the work tree's drift while live reload is paused.
  `11-contract.md` names the ops.
- **Delivery is filtered.** Today every non-bus event reaches every
  subscriber.
  - Facts about the primary go to the tile's readers, as today.
  - Anything naming a non-primary deployment (its name, builds, compiler
    output, status, would-notify lines, data ops) goes only to admins, humans
    with at least `write` on the tile (their current level), and that
    deployment's own principals plus the tile's terminal and agent sessions.
  - The primary's frame token, minted for the tile's readers, is not an own
    principal for non-primary facts.
  - Other tiles receive none of it.
- **What readers see.** A reader of the tile gets only primary-scoped facts.
  `GET /api/xbin/deployments` returns live reload state as it concerns the
  primary, the primary's checkpoint and deploy state, and protection: no
  non-primary deployment names, and no counts that reveal them. `/components`
  adds only the primary summary. `11-contract.md` owns the exact shape.
- Frames of a pinned deployment do **not** reload on saves. They reload when
  a deploy changes their deployment's code.

## 9. Data, secrets and state per deployment

- **`main` keeps today's keys, forever (P6).** Storage follows the
  deployment, not the primary role. Enabling deployments never migrates
  `main`'s data. Reassigning the primary doesn't move data either.
- **A non-primary deployment's data starts empty (P14).** It lives in its own
  namespace, and seeding it from the primary is a tile-manager act. Resources
  belong to **scopes**, so the namespace is keyed by (scope, deployment name).
  In a multi-tile scope, `apps/a+dev` and `apps/b+dev` share the scope's `dev`
  namespace, just as `main`s share the scope's data today (P28). A sibling
  with no `dev` deployment uses the primary namespace. A same-scope call from
  `apps/a+dev` to `apps/b` is an edge like any other (the read clamp applies),
  never a shortcut into the primary's data plane.
  - Under `read` the call reaches the sibling's primary with role `reader`,
    so it can read the scope's primary namespace through that sibling's API;
    `block` on that edge stops it.
  - Acts on a shared namespace (seed, reset, restore) stop every sibling's
    same-named deployment and need the act's authority on each. The namespace
    is deleted with its last claimant.
  - `res:workspace/*` is never split: it is always an edge.
  - Seeding is optional: the default is empty data, and the common case is
    synthetic data the deployment's own code creates.
- **Vault per deployment.** A new deployment gets the primary's key names as
  placeholders. Copying values is a tile-manager act.
- **Logs and prefs are keyed per deployment** (the name rule, P17); `main`
  keeps today's keys. A non-`main` deployment's backend log is
  `.xbin/deploy/<TileKey>/d/<name>/backend.log`. **Status** is emitted by role
  (§8): the primary's is today's bare status. **Agent history** stays per
  tile, because it is a conversation about the one work tree, and each entry
  records its session's target.
- **The env layer (`setup`) is shared by hash** between deployments, and GC
  keeps every layer any deployment references. The exception is a protected
  primary: its env layers, Go build cache and artifacts are built only by
  manager-initiated operations, in a namespace other deployments' builds never
  write. The layer hash covers only the script and the rootfs, while `setup`
  sees the whole tile and the network.

`08-data.md` specifies the keys per resource kind, including kv buckets and
their encryption labels, and resenc mount paths under `.xbin/resenc/**`, which
the installer's AppArmor rule requires. It also covers consistent online
seeding, reset, delete, quotas, backup and offload.

## 10. Authority

Authority is the tile's, and a deployment never widens it (P11). Who may
**operate** deployments:

| Act | Who |
|---|---|
| Open or call a non-primary deployment (its URL, UI, API) | humans with ≥ `write` on the tile, checked per request against their current level; the tile's own terminal/agent sessions (via target) |
| Pause / resume live reload / reload now; attach live reload to a non-primary deployment | `terminal` level (and the tile's terminal/agent tokens), the same power as editing code today. While the primary is protected, resuming onto it is refused, and reload now onto it follows the **primary** row below |
| Add, remove, deploy to, promote to, or roll back a **non-primary** deployment; reset its data; set its target in a terminal; run now on its dormant jobs | `terminal` level (and the tile's terminal/agent tokens) |
| Deploy, promote or roll back onto the **primary** | `terminal` level and their agents: **parity** with saving today (P4). While the primary is protected: tile managers only, in a human session, naming the reviewed checkpoint (§5). Terminal and agent tokens cannot deploy, promote, roll back, reload now or resume onto it, and it is never a session's target (P24). |
| Seed data, copy vault values, deliveries or alwaysOn for non-primary, set edge policies, reassign the primary, protect or unprotect it; reset `main`'s data while it isn't primary; set a deployment's resource limits (P22, defaulting to the tile's and never above the tile's own ceilings); purge a checkpoint | tile managers (user-owner, owning-org admins, workspace admins: the D24/D33 gate) |
| Any of the above from a view-as session (D64) | refused (read-only) |

**Operating is checked per request.**
- The tile's own instance, frame, cron and bus principals never operate
  deployments.
- Terminal and agent tokens pass only while the driving user holds `terminal`
  level.
- Manager acts (seed, vault copy, deliveries, alwaysOn, edge policy, limits,
  reassign, protect, purge, non-primary backup and restore) need a human
  session: `p.Component == ""` and (`IsAdmin` or `mayManageTile`;
  `internal/auth/auth.go:92`, `internal/broker/orgsapi.go:427`). Terminal and
  agent tokens are refused even for managers, because agents share them. No
  element principal passes the manager gate in the deployments plane,
  whatever `xbin` or `xbin:users` grants its tile holds.
- Every mutating operation is a non-GET JSON request, never a WebSocket
  upgrade, so view-as sessions and form posts cannot forge it.
- A non-primary principal never satisfies `IsAdmin` or any governance
  capability. Approving an `xbin`/`xbin:*` grant is refused while the tile has
  non-primary deployments (P19).

**xbind's API is default-deny for non-primary principals (P26).** Every
`/api/xbin/*` route is classified deployment-scoped, primary-only or neutral.
Non-primary principals are refused on any unclassified route, reads included,
and a guard test keeps the classification complete. PR decisions, for
example, are primary-only for instance principals.

Edge policies are a tile-manager act because they narrow or widen what the
tile's non-primary code may reach in other tiles.

`noTerminal` accounts (D88) are capped at `write`, so they can view
non-primary deployments but not operate them.

## 11. Lifecycle, ownership and the rest of the tile's life

- **Lifecycle** is the tile's. Disabling, hiding or offloading stops every
  deployment. Enabling starts the primary as today; non-primary deployments
  start on demand. Offload archives every deployment's data and the
  checkpoint store before anything is removed, and restore covers them
  (`08-data.md`).
- **Transfer (D39)** moves the tile's deployments with it, and rewrites the
  record's owner ref in the same step. Ceilings are re-evaluated as today.
  Edge policies stay.
- **Clone, template instantiation, import and builtin import** copy the work
  tree only. The new tile starts in the zero state.
- **Tile removal:**
  - The deployment record belongs to the tile it was created for (path, owner
    ref, creation stamp; §3). It never applies to a new tile at that path,
    whoever creates it, admins and `xbin:writer` elements included: every
    creation path resets the path's deployment state first. A record whose
    owner ref no longer matches (an older binary knew nothing of it) is inert
    until an admin adopts or clears it.
  - The checkpoint store and the non-`main` namespaces are path-keyed
    **leftovers** on D82's refusal list. The path's current owner is exempt,
    as today; the record itself is still not re-applied.
- **Builtin updates (D49) and code PRs (D48)** land in the work tree. They
  therefore reach only the live reload target. With a pinned primary and live
  reload on `dev`, an update is exercised on `dev` and promoted when it works.
  That is safer, and nothing about the PR flow changes.
- **Backups:**
  - A component backup keeps today's archive byte-for-byte for `main`'s data.
    It adds the record and the checkpoint store under new tar prefixes, which
    older restores skip.
  - When the primary isn't `main`, every backup also archives the primary's
    data, in its own deployment archive.
  - Other non-primary data is opt-in, under its own archive key, so it doesn't
    consume the primary's retention.
  - Deployment archives use keys no `CompKey(path)` can produce and a manifest
    schema older readers refuse, and they never add rows to the manifest's
    cron or bus lists.
  - Restore refuses an archive whose manifest names another tile before
    writing anything, rebuilds the store from its objects only, and validates
    the record.

## 12. Isolation and runtimes

- **Backends of pinned or non-primary deployments need isolation.** A
  go/node/python backend must see its checkpoint at the tile's canonical path
  (a sandbox bind), and its own data namespace at the paths its env names.
  Without `--isolate` xbind refuses these operations with a clear reason. That
  is D78's fail-closed rule; there is no silent fallback to the work tree.
- **Static-only tiles** (no backend) can pause live reload everywhere: the
  static plane serves the checkpoint.
- **VM backends (D89/D90)** run one guest per deployment, counted against the
  VM budget.
- **cgi no longer exists** (removed from xbin by its own change).
- **Chrome tiles** (root, shell, `chrome: true`) **and tiles holding
  `xbin`/`xbin:*` capabilities** may pause live reload: pinning their code is
  safe. They may not have non-primary deployments, because workspace
  governance has no deployment dimension (P19). A non-primary deployment of
  the admin tile would act on real users and grants.
- **Resource caps:**
  - The number of non-primary deployments per tile and per workspace is
    capped.
  - Each deployment gets its own cgroup (below), with limits defaulting to the
    tile's (P22).
  - `07-runtime.md` sets the numbers.
- **Primary first (P25).** A non-primary deployment never takes what the
  primary needs:
  - its VM reservation leaves the primary's guest size free in the budget and
    never preempts a primary start;
  - inside the tile the primary has the higher CPU weight;
  - a non-primary namespace has its own quota bucket and is write-blocked
    first on low disk.

**Sandbox mechanics: the shared layer.** This design is based on master:
D112's registry is landed, and D113's tile-managed sandboxes are designed
([plans/tile-sandboxes.md](../tile-sandboxes.md);
[research/sandbox-visibility.md](research/sandbox-visibility.md)).

Tile deployments are **not** built on D113's tile-managed sandboxes (the
owner's direction, 2026-09-27). A deployment's backend is a runner backend,
with its blue/green, health check, reaper, alwaysOn, env layer and ingress
plumbing. A tile sandbox is an exec box driven by its tile, with no serving
lifecycle.

This design does change the mechanics **one layer up**, which backends,
terminals and tile sandboxes all share:
- **The D112 registry** gains a deployment dimension on non-`main` backend
  entries only. Their ID is `backend+<name>:<CompKey>:g<gen>`, and
  `Entry.Deployment` names the deployment. `main`'s rows are byte-identical to
  today's (`backend:<CompKey>:g<gen>`, no `Deployment` field,
  `internal/runner/sbx.go:63`), even when the tile has a record; only a tile
  that runs a non-main deployment moves `main`'s `Leaf` (next bullet).
- **Budgets:** VM reservations of every deployment are charged to the tile
  (`Reserve(owner = tile)`). A deployment is never its own budget owner.
- **Separate cgroups per deployment.** Zero-state and main-only tiles keep
  today's flat `comp-<CompKey>` leaf.
  - A tile gets a per-tile parent when it first runs a non-main deployment:
    `tile-<CompKey>/d-<name>/{backend, sbx-*}`. cgroup v2 forbids processes in
    a cgroup whose children have controllers, and D113's sandboxes will sit
    beside a deployment's backend.
  - `main` moves at a generation boundary: its next generation starts under
    the parent.
  - Consumers keyed by the flat leaf (at-limit alerts, `/sandboxes` leaf
    stats, the registry's `Leaf`, `internal/sbx/sbx.go:70`) read the new
    layout only for tiles that have it.
  - This is the nested per-tile cgroup D113 defers, built once in the shared
    layer by this programme; D113 reuses it.
- **Backend launch-spec binds:**
  - the checkpoint is bound at the canonical path;
  - the deployment's data namespace is bound at the primary's resource paths,
    so `XBIN_RES_*` stays identical;
  - the live reload target keeps today's work-tree bind.
- **confine** gains an explicit bind destination (`DirFrom`), so confined
  builds see a checkpoint at the tile's canonical path and never have the work
  tree mounted. A non-isolated run with such a bind fails with a clear error
  instead of building the work tree.
- **Streaming (M3):** the deploy remote's `receive-pack` needs a
  confine-level streaming run (caller-supplied readers and writers through
  `confine.Run`). It is independent of D113's unbuilt exec protocol, and D113
  may reuse it.

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
| P5 | The zero state is the absence of a deployment record, and it is byte-for-byte today (the flat cgroup leaf and today's registry rows included). Opting out removes the record; the checkpoint store may remain, inert. No manifest key. | proposed |
| P6 | Storage follows the deployment. `main` owns today's keys forever. | proposed |
| P7 | Primary is a role. Every inbound edge resolves through one resolver that returns the primary for every v1 edge-policy value. Reassignment moves routing, not data. | owner-confirmed 2026-09-27 |
| P8 | Live reload attaches to at most one deployment, which runs the work tree directly. The default path adds no per-save cost. | proposed |
| P9 | Pinned means pinned. Every restart path runs the record's checkpoint (after a failed move off the work tree, the attempted one). Built artifacts are kept per checkpoint. The inbound surface follows the primary's code. | proposed |
| P10 | Promotion moves code only. Data, vault, config and routing are late-bound to the target. | proposed |
| P11 | Authority stays per tile. A deployment is not a principal, and non-primary deployments are narrowed by edge policy. | proposed |
| P12 | Self-calls stay inside the caller's deployment. Tile principals can't call another deployment of their own tile. | proposed |
| P13 | Non-primary registrations are dormant. Notifications aren't pushed. Non-primary status and build activity travel only in the `deployments` event, delivered by access. | proposed |
| P14 | Non-primary data and vault start empty. Seeding is optional; seeding and vault copy are tile-manager acts. | proposed |
| P15 | Deployment state is xbind-owned (`data/`), never in the root `xbin.json` or the work tree. | proposed |
| P16 | Every git or tool run touching tile code happens in confine. The checkpoint store is confine-only. Materialized trees are served with containment and never followed out through symlinks. | proposed |
| P17 | Deployment URL qualifier `+`, resolved only for tiles with a record and only after today's resolution fails; the bare URL is the primary. Two naming rules. Signals (`X-XBin-Deployment`, backend `XBIN_DEPLOYMENT`, meta, whoami) are absent for the primary, and a reassignment restarts both primaries so they stay true. Credentials and stored state (frame-token claim, origin labels, sandbox IDs, storage keys) are absent for `main`. | proposed |
| P18 | Pinned or non-primary backends need isolation: non-isolated mode is unsupported for them. Static tiles pause live reload everywhere. cgi no longer exists. | owner-confirmed 2026-09-27 |
| P19 | Chrome tiles and xbin-capable tiles may pause live reload, but can't have non-primary deployments. Approving an `xbin`/`xbin:*` grant is refused while non-primary deployments exist, and a non-primary principal never satisfies governance checks. | owner-confirmed 2026-09-27 |
| P20 | A non-primary deployment is an accident boundary, not a trust boundary. | proposed |
| P21 | A protected primary cannot be the live reload target or a session's target (P24). Every change to its code, and so to the inbound surface it serves, is a tile-manager act in a human session naming the reviewed checkpoint. Its env layers, caches and artifacts come only from manager operations. It follows no tracked branch and takes no deploy-remote push. | proposed |
| P22 | Resource declarations are deployment-level. Each deployment provisions what its own code declares, in its own namespace, with per-deployment limits defaulting to the tile's; limits are set by tile managers, never above the tile's ceilings. | owner-confirmed 2026-09-27 |
| P23 | Edges that cannot be read-clamped (custom roles, stream interfaces, lan-ingress links, net-provider splices) are blocked for non-primary deployments in v1, with no override. Loosening later is easy; tightening after the fact is not. | owner-confirmed 2026-09-27 |
| P24 | The terminal's API dropdown selects the session's target deployment, defaulting to the primary. A protected primary is not offered, and the default then falls to the live reload target. When neither exists, the dropdown falls to "API off". A session's target is fixed for its life. | owner-confirmed 2026-09-27 |
| P25 | Primary first: a non-primary deployment never takes the VM budget, CPU weight, disk quota or per-tile caps the primary needs. | proposed |
| P26 | xbind's API is default-deny for non-primary principals: every `/api/xbin/*` route is classified (deployment-scoped, primary-only or neutral), unclassified routes refuse them, and a guard test keeps the list complete. | proposed |
| P27 | Edge-policy values fail closed: an unknown value, or one invalid for the edge's kind, reads as `block`, and any `block` among several authorizing edges refuses the call. | proposed |
| P28 | A (scope, name) namespace is shared by the scope's same-named deployments. Seeding, resetting or restoring it needs the act's authority on every claimant and stops all of them. It is deleted with its last claimant. In v1 no reassignment splits a scope's primary data. | proposed |
| P29 | Deployment state belongs to the tile it was created for: collision-free path keys, and a record that carries and verifies path, owner ref and creation stamp. A tile re-created at the path never inherits it. | proposed |

## 14. Worked flows

These are the flows every other document must support. The UX of each is in
`10-ux.md`.

**A — Pause live reload, fix, reload now, resume** (single deployment, no new
concepts).
1. Ana's tile is in heavy use, and she needs to try a risky change. In the
   terminal window she pauses live reload. `main` keeps running the same
   code, now pinned to `c:3f2a1c9`.
2. She edits. Saves reload nothing, and the bar counts the files changed
   since the checkpoint.
3. She presses **Reload now**. The work tree is checkpointed and deployed to
   `main`, with the usual blue/green swap, and every viewer's frame reloads
   once. Live reload is still paused.
4. Later she resumes live reload. `main` follows the work tree again. The
   tile returns to the zero state.

**B — A `dev` deployment for a busy tile.**
1. Ana adds deployment `dev`: code from the work tree, data empty, live reload
   attached to `dev`.
2. `main` is pinned to a fresh checkpoint of the work tree. The primary sees
   no change.
3. Her saves now reach `/c/apps/crm+dev/`. Most of the time `dev` runs on data
   its own code creates; a manager seeds `dev` from `main` only when realistic
   data is needed.
4. When `dev` looks right, **Promote dev → main** shows the diff between
   `main`'s checkpoint and a fresh checkpoint of the work tree, then deploys
   exactly the checkpoint the diff showed (a 409 if the work tree moved
   since).
5. `main`'s data is untouched, and `dev` keeps following the work tree.

**C — An agent iterating on `dev`.**
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

**E — A builtin update tested on `dev`.**
1. The Updates tab files a D49 PR. The tile's own plane applies it in the work
   tree, which reaches `dev`.
2. `main` is unaffected until promotion.

**F — Cut over with data (advanced).**
1. A schema migration was rehearsed on a seeded `dev`.
2. A manager reassigns the primary to `dev` (M2; the tile roots its scope
   alone). `dev` starts as the primary first, then `main` restarts. Every
   inbound edge now reaches `dev`, and `dev`'s data.
3. A loud confirmation says the old primary's data does not follow.
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

   Seeding or resetting the scope's `dev` namespace stops both tiles' `dev`
   and needs authority on both. Under `read`, `apps/shop-admin+dev` can read
   `apps/shop`'s primary data through its API; `block` on that edge stops it.

**H — A hotfix while `dev` holds unfinished work.**
1. `main` is pinned to `c:3f2a1c9`. Live reload is on `dev`, and the work tree
   holds unpromoted work.
2. A bug on the primary needs a fix now. In the tile's terminal:
   - `git fetch xbin-deploy` (the read-only checkpoint remote);
   - commit or stash the unfinished work on a branch;
   - `git checkout -b hotfix deploy/main` (the checkpoint's git view:
     gitignored files such as `node_modules` and `.env` are not in it);
   - fix and commit.
3. The work tree now holds `main`'s code plus the fix. `dev`, which follows the
   work tree, runs it too, so the fix is exercised there first.
4. **Deploy to main** ships it.
5. The developer checks out the unfinished branch again and moves it onto the
   hotfix with `git rebase --onto hotfix <work-tree head> <branch>`. The head
   is named in the checkpoint commit's message, and `bx` prints the exact
   command. `dev` follows.
6. Nothing in xbind merged anything. Git did the combining in the work tree,
   and deployments only moved checkpoints (the "promotion is not a merge"
   rule).

## 15. Deliberately not in this model

**Later rungs** (designed for, not built in v1):
- **Tracked branches and the deploy remote**, as additional feeds (M3). They
  add feeds and nothing else. The deploy remote needs a
  streaming confine (research/git-confine.md).
- **Name matching** (edge policy `match`) and any cross-tile grouping of
  same-named deployments built on it (M3).
- **Traffic splits and gradual rollouts** (the Cloudflare pattern). The model
  has one primary, and a later split would be a routing feature on the same
  funnel.
- **Per-deployment ingress hostnames** for testing webhooks against a
  non-primary deployment. Ingress belongs to the primary. A later rung could
  let a manager route a test hostname to a named deployment.
- **Data flowing back** (non-primary → primary): never automatic. Promotion
  moves code; data changes to the primary happen through the primary's own
  code or its migrations.
- **A trust boundary.** Running code written by people who can't write the
  tile (for example, a deployment per pull request from a non-writer) needs
  separate principals, and is out of scope (P20).
- **A deployment dimension for chrome and governance tiles** (P19).
