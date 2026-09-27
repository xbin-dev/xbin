# 07 — Runtime: runner, checkpoints, builds, serving

> Status: live — the runner, checkpoint store, build, serving and shared-sandbox mechanics that implement tile deployments, for the implementation swarm (part of [plans/dev-lifecycle](README.md))

This document turns the model ([05-model.md](05-model.md)) into mechanics. It
owns runner state per `(tile, deployment)`, the checkpoint pipeline, the view
repository behind the fetch remote, builds from a checkpoint, the serving
planes, the registry's composition of the primary's manifest, watcher gating,
every restart path, deploy mechanics, per-deployment artifacts and the shared
sandbox layer. It implements the runtime side of P5, P8, P9, P16, P17, P18,
P19, P21, P22 and P25.

Other documents own the rest:
- wire shapes (endpoints, events, env, tokens, `bx` and its exit codes) and
  the two naming rules: [11-contract.md](11-contract.md);
- data namespaces, seeding, backups and which `scope.json` declares a shared
  namespace's resources: [08-data.md](08-data.md);
- who may reach which deployment, the edge policy, route classification and
  dormant registrations: [09-fabric.md](09-fabric.md);
- the threat model and the git hardening this document obeys:
  [06-security.md](06-security.md);
- package and file names, budgets: [13-surfaces.md](13-surfaces.md).

Vocabulary is [01-glossary.md](01-glossary.md), verbatim.

**Baseline.** The baseline is master: D112 is landed, and D113 is designed
([plans/tile-sandboxes.md](../tile-sandboxes.md)). Every `file:line` below was
re-verified against this worktree. Lines in `internal/runner/runner.go`,
`internal/proxy/proxy.go` and `internal/registry/registry.go` move again when
14-implementation's PRE-5 lands; WP-00 re-measures them.

**The two design rules.**
- Every generation's code is decided in one place, at build time: the runner
  asks `CodeFor(tile, dep)`, and a deploy passes its code explicitly. Every
  restart path reaches a build through `EnsureDeployment → buildAndStart`
  (§7). So "pinned means pinned" (P9) holds by construction, not by patching
  each path.
- The registry's component for a path is **the primary's**: tile-level fields
  from the work tree, the inbound surface and the deployment-level fields from
  the primary's code (§5.1). Every existing reader of `c.Manifest` on an
  inbound path therefore follows the primary without an edit, and a save
  while the primary is pinned changes nothing that serves traffic.

## 1. Runner state keyed by (tile, deployment)

### 1.1 Today

Everything the runner keeps is keyed by the component path
([research/runner-hot-reload.md](research/runner-hot-reload.md),
[research/inbound-edges.md](research/inbound-edges.md)):
- `Runner.states` is `map[string]*state` (`internal/runner/runner.go:158`).
- `state()` creates a missing entry dirty (`:176-185`).
- A state holds one current generation, `cur` (`:70-82`).
- `Ensure` is single-flight (`:190-240`): the fast path is `!dirty && cur != nil`
  (`:205-209`); otherwise one caller runs `buildAndStart` (`:226-237`).
- `buildAndStart` builds from `c.Dir`, the work tree, every time (`:287-361`;
  `build` at `:365-411`).

### 1.2 The key, the shared declarations and the state

A state is keyed by `stateKey{tile, dep}`. A tile without a deployment record
only ever has `{tile, "main"}`.

The deployments plane is the package `internal/deployments`. No package is
named `internal/deploy`: that name belongs to the installer
([13-surfaces.md](13-surfaces.md)). The few shared declarations go where
their users import them without cycles, one declaration in one place (the
D109 lesson):

```go
// internal/util/util.go, beside ComponentPathOK (util.go:97-114) and
// CompKey (:137-145).
func DeploymentNameOK(s string) bool // ^[a-z][a-z0-9-]{0,23}$ (the glossary grammar)
func TileKey(path string) string     // the 128-bit key of the new stores (05-model §3); 11-contract §10 owns the encoding

// internal/runner/deploy.go: the runner defines the hook that returns it.
// Code is what one generation of a deployment runs.
type Code struct {
	WorkTree bool   // it follows the work tree (the live reload target)
	Tree     string // the pinned checkpoint's full tree hash; "" iff WorkTree
}

func sockDir(tile, dep string) string // CompKey(tile) for main; "d-"+16 hex of sha256(tile+"\x00"+dep) otherwise

// emit publishes one runner event of (tile, dep) under rule C2 (§8.5): the
// primary's keep today's types and bare component; any other deployment's
// become a `deployments` event (op "build" or "reload") with the bare
// component and the deployment named in data.
func (r *Runner) emit(tile, dep, typ, text string)
```

`TileKey` keys only the new stores (`data/deployments`, `data/checkpoints`,
`.xbin/deploy`), because `CompKey` keeps 32 bits of hash and can be ground
(05-model §3). Existing stores keep their `CompKey` keys: build output, Go
caches, env layers, logs of `main`, socket dirs and cgroup names.

**New fields:**
- `state` gains `tile` and `dep` (replacing `comp`), `code` (what `cur` runs)
  and the per-state build turn that deploys share with `EnsureDeployment`
  (§8.3).
- The runner's per-generation struct (`instance`, `runner.go:56-68`) gains
  `code`, `root` (the host directory bound at `c.Dir`), `artifact`, `leaf`
  (its cgroup leaf, §10.3) and `envHash`.
- Today `Add`, `AddMem`, `Remove` and the registry leaf recompute
  `util.CompKey(c.Path)` at each call site (`runner.go:490`, `:492`, `:614`;
  `internal/runner/sbx.go:66`). They must read the generation's own `leaf`.

**Runner API after the change.** New names sit beside today's (05-model §7;
14-implementation NP-14-8). The old names stay as shims that mean the
primary; each caller moves when the work package that owns its file moves
it, and `TestEnsureCallSitesPassPrimary` pins the callers that stay.

| Method | Semantics |
|---|---|
| `Ensure(ctx, c)` (`:190`, kept) | `EnsureDeployment(ctx, c, Primary(c.Path))`. Every inbound-edge caller keeps calling it. |
| `EnsureDeployment(ctx, c, dep) (sock, error)` | Single-flight per `(tile, dep)`; builds from `CodeFor(tile, dep)`. |
| `Track(comp)` (`:246`, kept) / `TrackDeployment(tile, dep) func()` | Today's semantics, per deployment; `Track` means the primary. |
| `Changed(c)` (`:265`, kept) / `ChangedDeployment(c, dep)` | Marks that deployment dirty and clears its crash history; `Changed` means the primary. |
| `ChangedTile(c)` | `ChangedDeployment(c, d)` for every deployment `d` with a running, building or failed generation. Authority paths call it. |
| `Deploy(ctx, c, dep, code, commit, progress)` | Puts `code` on `dep` through blue/green (§8.1). `commit` runs after the swap, before the result is reported; `progress` carries phases and the result. |
| `Stop(tile)` (`:745`) | Stops **every** deployment of the tile. |
| `StopDeployment(tile, dep)` | Stops one deployment (remove). |
| `StopAll()` `:762`, `Status()` `:784`, `Inspect()` | Iterate `(tile, dep)`. The per-tile row stays the primary's; [11-contract.md](11-contract.md) §8 owns where non-primary rows go. |
| `InUse() (roots, artifacts []string)` | The materialized trees and artifacts that running generations use. GC reads it (§2.8). |

**Hooks** on `Runner`, installed by boot. Each is nil-safe; nil gives
today's behaviour for the primary and refuses anything else:
- `CodeFor(tile, dep) (Code, error)` reads the deployment record. Without one
  it answers `{WorkTree: true}` for `main` and "no such deployment" for any
  other name.
- `Primary(tile) string` answers `"main"` without a record.
- `View(c, code) (*registry.Component, error)` is the deployment view (§5.1).
- `Materialize(tile, tree) (root string, error)`, §2.6.
- `EnvFor(c, dep) (env []string, remap map[string]ResBind)` replaces
  `EnvForComponent` and adds the resource remap of §10.4 and the
  per-deployment declarations of §5.4.
- `ShouldRun(tile, dep)` = lifecycle (per tile) ∧ ¬`EncryptionHold` (per the
  deployment's data namespace, which it mounts on demand;
  [08-data.md](08-data.md)).
- The spawn-time hooks keep their signatures (`Egress`, `GPU`, `NetRoster`,
  `NetTarget`, `NetHost`, `NetCaps`, `ContainerCaps`, `IngressNet`,
  `IngressFwd`, `NetLinks`; `runner.go:109-142`). They receive the deployment
  view, whose `Deployment` field names a non-primary deployment and is empty
  for the primary (13-surfaces NP-13-3), and answer a non-primary view by
  §10.4's table, reading the edge policy through 09-fabric's resolver.
- `LimitsFor(tile, dep) cgroup.Limits` answers a deployment's cgroup limits
  (P22, §10.3). nil gives the tile's.

### 1.3 The callers

Inbound-edge callers keep `Ensure` and `Track`, which mean the primary.
Internal callers pass the deployment they act on (05-model §7).

| Caller | Ref | Passes |
|---|---|---|
| `/api/` proxy: console, gateway, tile origins. Cron, bus and archiver dispatch re-enter it (`internal/broker/cron.go:200-208`, `internal/broker/bussubs.go:127-135`, `internal/broker/backup.go:379-388`). | `Ensure` at `internal/proxy/proxy.go:182`, `Track` at `:196` | The primary for a bare URL (`Ensure`). `EnsureDeployment` with the resolved deployment for a qualified URL, a self-call or a session's target (§4.3; who may: [09-fabric.md](09-fabric.md)). |
| Public HTTP ingress | `internal/proxy/ingress.go:58`, `:65` | The primary (`Ensure`, unchanged). |
| L4 streams, hairpin, stream interfaces (`DialInto`; also via `hostDial` `:127`) | `internal/runner/ingress.go:37`, `:40` | The primary (unchanged). |
| Net-provider bring-up (`ensureProvider`) | `internal/runner/netmux.go:62` | The primary (unchanged). |
| `Changed`'s background rebuild | `internal/runner/runner.go:281` | The deployment given to `ChangedDeployment`. |
| alwaysOn start (`aoStart`) | `internal/runner/alwayson.go:74` | The deployment being woken (§11). |
| The watcher's `Changed` | `internal/boot/serve.go:180` | `ChangedDeployment` with the live reload target only (§6). |
| `OnGrantChange` and the provider nudge | `internal/boot/boot.go:508-512`; `internal/runner/runner.go:593` | `ChangedTile`. |
| `brk.StopBackend = run.Stop`: lifecycle, restore, offload, seal | `internal/boot/boot.go:513` → `internal/broker/lifecycle.go:102`, `internal/broker/backup.go:439`, `:455`, `internal/broker/resenc_wire.go:157` | `Stop(tile)`, every deployment. |
| `StopAll`, `WakeAlwaysOn` | `internal/boot/serve.go:143`; `internal/boot/boot.go:514`, `:720`; `serve.go:183` | Unchanged; `WakeAlwaysOn` iterates `(tile, dep)` (§11). |

### 1.4 The zero-state proof obligation

For a tile without a record, all of these are exactly today's:
- `dep` is `main`, `CodeFor` returns `{WorkTree: true}`, and the state key is
  one-to-one with today's path key;
- the registry's component is today's: the `PinnedPrimary` hook (§5.1)
  answers nothing, so no field is composed and `Component.WorkTree` is nil;
- the socket dir is `CompKey`, the log `.xbin/log/<CompKey>.log`, and the build
  output `.xbin/build/<CompKey>/bin`;
- the cgroup leaf is flat (§10.3), the D112 registry row is today's (ID,
  `Leaf`, no `deployment` field; §10.1), and so are the events;
- no checkpoint store, view repository or journal exists (§2.1, §2.11).

The golden test ([15-test-plan.md](15-test-plan.md)) pins the backend env, the
full `sandbox.Spec` (binds, cwd, argv), artifact paths, leaf name, `sbx.Entry`,
and the events of a save, a crash and a reap.

## 2. The checkpoint pipeline

### 2.1 The store and its neighbours

| Path | Holds | Written by |
|---|---|---|
| `data/checkpoints/<TileKey>.git/` | A bare SHA-1 repository (§2.3). Never served. | Confined git only (P16). |
| `…/index` | The persistent private index over the tile's work tree: git's default `$GIT_DIR/index`, used with `--work-tree`. | Confined git only. |
| `…/quarantine/<run>/objects/` | One capture's new objects until admission (§2.2). | Confined git; run 2 migrates it. xbind removes a leftover with `os.RemoveAll`, never another walk. |
| `…/refs/xbin/checkpoints/<tree>` | The retention root of a checkpoint (§2.3). | Confined git only. |
| `…/refs/xbin/views/<tree>` | The checkpoint's git view (§2.3). | Confined git only. |
| `…/refs/xbin/log/<name>` | The deploy log of deployment `<name>`, a commit chain (§2.3; [11-contract.md](11-contract.md) §10.3). | Confined git only. |
| `data/checkpoints/<TileKey>.view.git/` | The view repository: only `refs/heads/deploy/<name>` for pinned deployments, and `HEAD` (§2.7). Derived and rebuildable. | Confined git only. |
| `data/deployments/<TileKey>/pending.json` | The deploy journal (§8.4, NP-07-13); 11-contract §10 adds its format if Q3 of [16-open-questions.md](16-open-questions.md) adopts it. | The deployments plane. |
| `.xbin/deploy/<TileKey>/<tree>/` | Materialized checkpoints, one per full tree hash (§2.6). | A confined checkout, then an atomic rename. |
| `.xbin/deploy/<TileKey>/d/<name>/` | Derived state of a non-`main` deployment: `backend.log`; later its D113 sandboxes. | xbind. |
| `.xbin/build/<CompKey>/c/<tree>/` | Per-checkpoint build artifacts (§3.1). An existing store, so it keeps its key. | A confined build. |
| `.xbin/deploy/<TileKey>/protected/{build,cache,env}/` | A protected primary's build products (§3.4), outside every directory another build binds. | Manager-initiated confined builds only. |

- **The store is created by the first committed opt-in** (pausing live
  reload, or adding a deployment), never at boot, and never by a dry run or a
  diff (§2.11). A zero-state tile has no store (P5), and the legacy fixture's
  second boot changes nothing.
- **Init** is a confined `git init --bare`. xbind then writes `config` and
  `info/attributes` from constants; nothing from the tile is ever copied into
  either ([06-security.md](06-security.md) T1).
  - `config`: `core.bare=true`, `core.logAllRefUpdates=false`, `gc.auto=0`
    (xbind drives GC, §2.8), `core.excludesFile=/dev/null` (no user excludes
    file reaches the git view, even in a direct run whose `HOME` is xbind's),
    and fsync of objects and refs (`core.fsync=objects,reference` on
    git ≥ 2.36), because `data/` is what backups carry.
  - `info/attributes`:
    `* -text -filter -ident -working-tree-encoding -export-subst -export-ignore !diff !merge`.
    It outranks every in-tree `.gitattributes`, so capture and
    materialization are byte-exact and no driver the tile names resolves.
  - The store never has an `objects/info/alternates` or an `info/exclude`
    file of its own.
- **SHA-1 stays the object format.** Git views are fetched into tiles' own
  SHA-1 repositories (§2.7).
- **Every confined run on the store** carries `gitFlags` and `gitEnv`
  (`internal/confine/git.go:14-34`) plus 06-security T1's extra `-c` flags,
  uses `NetNone`, and binds only this tile's store and work tree.
  - It never uses the tile's `.git` as the repository, never `-C <tile>`, and
    never `runGitIn`, which binds its directory read-write
    (`internal/broker/code.go:64-80`).
  - Tile-controlled strings never reach git's argv. Pathspecs and path lists
    travel in files (`--pathspec-from-file=<f> --pathspec-file-nul`, or
    `--stdin -z`) that live in the run's quarantine directory. Object ids are
    validated as 40 hex digits and deployment names with `DeploymentNameOK`
    before use.
  - Each run's script first deletes leftover `*.lock` files under the store,
    inside the sandbox.

**Every confined run this document adds** (for 06-security's ledger):

| Run | Binds (read-write / read-only) | Where |
|---|---|---|
| Capture (run 1), and its embedded-repository re-run | store / work tree | §2.2 |
| Record (run 2): quarantine migration, retention and view commits | store / — | §2.2 |
| Materialize | tmp tree / store | §2.6 |
| Deploy-log append; deploy-log read (`git log`) | store / — | §2.3 |
| View refresh | view repository / store | §2.7 |
| Drift count | scratch index and quarantine / store, work tree | §2.9 |
| Dry-run estimate | — / work tree | §2.11 |
| Throwaway diff | scratch repository / work tree | §2.11 |
| Store GC, background repack | store / — | §2.8 |
| Checkpoint build (Go), env-layer setup | output, caches / checkpoint and workspace | §3 |

### 2.2 Creating a checkpoint (the work-tree feed)

This is confined git over a private `GIT_DIR`, the D77 pattern
(`internal/term/agentdiff.go:91-124`), with 06-security T2's quarantine. A
capture is two confined runs (D78: about 45 ms each), with an admission check
in xbind between them.

**Run 1, capture.**
- `Dir` is the store, read-write. `Binds`: `confine.RO(c.Dir)`.
- Env: `GIT_OBJECT_DIRECTORY=<store>/quarantine/<run>/objects` and
  `GIT_ALTERNATE_OBJECT_DIRECTORIES=<store>/objects`. Objects the store
  already holds are found through the alternate, and only new ones land in
  the quarantine.
- Every git command in the script carries
  `--git-dir=<store> --work-tree=<c.Dir>` and the flags of §2.1 (NP-07-9).
- The script:
  1. `git add -A -f --ignore-errors --pathspec-from-file=<f> --pathspec-file-nul`.
     The file holds `.` and one `:(top,exclude,literal)<path>` per
     registered component nested in the tile, taken from the registry.
     `-f` makes gitignore not apply. The persistent index makes this
     incremental: only files whose stat data changed are re-hashed.
  2. `git ls-files -s -z`, filtered to mode `160000`. This prints the
     **embedded repositories**: directories below the tile root that hold
     their own `.git`, recorded as gitlinks.
  3. `git write-tree`: the checkpoint's tree `T`.
  4. `git ls-tree -r -t -l -z T`: the listing xbind admits (below).
  5. **The git view.** Under a scratch index
     (`GIT_INDEX_FILE=<quarantine>/view.index`): `git read-tree T`; then
     `git ls-files -z -c -i --exclude-standard` into a file, which lists the
     entries the tile's `.gitignore` files exclude (read from the work tree in
     this same run; the store's `core.excludesFile=/dev/null` and missing
     `info/exclude` keep every other ignore rule out); then
     `git update-index -z --force-remove --stdin` fed that file; then
     `git write-tree`: the view tree `V`, a subset of `T`.

**Admission, in xbind.** A pure function over `T`'s listing, unit-testable:
- It enforces the caps of §2.5.
- It applies tree hygiene (06-security T2): it refuses a path component equal
  to `.git` in any case, `.` or `..`, any mode other than `100644`, `100755`,
  `120000` and `040000`, and any gitlink.
- On refusal, xbind removes the quarantine **and the persistent index**,
  which now references quarantined blobs, so the next capture re-hashes
  everything. The store is unchanged, and the deployment stays on its
  previous code. The error names the cap or rule, and the largest paths.
- `V` needs no check of its own: its blobs are `T`'s, and its trees are
  subsets of `T`'s.

**Run 2, record.** Only when `refs/xbin/checkpoints/T` does not exist yet. It
runs in the store, and:
1. moves each loose object file of the quarantine into `<store>/objects`
   inside the sandbox (a no-clobber rename per file, so ids the store already
   has are skipped; a symlink moves as a symlink and is never followed), so
   xbind never walks the quarantine on the host (06-security C5);
2. `git commit-tree` and `git update-ref` for the retention root and the view
   commit (§2.3).

An unchanged tree skips run 2 and removes the (empty) quarantine, so
re-checkpointing costs one run.

**Embedded repositories.** A gitlink would drop the directory's files, but the
glossary keeps "every file … except `.git` directories and nested components".
- When step 2 prints a gitlink, xbind re-runs run 1 with a pre-step
  `git rm --cached` for those paths, plus a `confine.Mask` over each
  `<dir>/.git` (or an empty-file bind over a gitfile). Git then sees a plain
  directory and adds its files.
- A directory with regular entries in the index is recursed into from then
  on, so the extra run happens once per new embedded repository.
- Without isolation, confine ignores binds. There the checkpoint fails,
  naming the embedded repository, and never drops its files. Admission also
  refuses any gitlink that remains.

**Errors fail the checkpoint.**
- Fatal: an unreadable regular file or directory (`error:` lines, or "could
  not open directory" on stderr). The error names the first 20 paths,
  because a pinned deployment silently missing files would be a lie.
- Not an error: sockets, FIFOs and devices, which git skips by type (a
  fidelity limit, §2.4).

**Serialization.**
- One store operation per tile runs at a time (a per-tile mutex in xbind).
- Leftover quarantine directories from a killed run are removed
  (`os.RemoveAll`) at the start of each serialized operation, and leftover
  `*.lock` files by the run's own script (§2.1).
- An index git reports corrupt is deleted and the capture retried once (a
  full re-hash).

**Later feeds.** The store's content model is one tree. A tracked branch or a
deploy-remote push imports a commit's tree instead of running step 1.
- Such a checkpoint holds exactly what was committed, with gitignored files
  absent. That is a property of the feed, recorded on the checkpoint. Its
  view is computed the same way.
- Imported objects also pass `git fsck --strict` on the quarantine before
  admission ([06-security.md](06-security.md) T15).

### 2.3 Naming, metadata, the git view and the deploy log

**The id is the tree hash.**
- The short id is `c:` plus the first 7 hex digits, lengthened until unique
  in the tile's store.
- Records, the deploy log and directory names store the **full** hash.
- An id from a client is validated (`c:` plus hex) and resolved only in the
  tile's own store; an ambiguous prefix is refused.
- Identical content always yields the same id, so re-checkpointing an
  unchanged tree is free.

**The retention root** is `refs/xbin/checkpoints/<tree>`. It points at a
parentless commit of that tree, made at the tree's first capture. Its message
and trailers are [11-contract.md](11-contract.md) §10.3's (`Xbin-Tile`,
`Xbin-Feed: work-tree`, `Xbin-By`, `Xbin-At`, `Xbin-Work-Tree-Head`).
- `Xbin-Work-Tree-Head` is best effort and untrusted. xbind reads the tile's
  `.git/HEAD`, and the loose or packed ref it names, through
  `fsutil.OpenBeneath`, validates 40 hex digits, and omits the trailer
  otherwise.
- It never runs git on the tile's repository to get it (06-security T1), and
  never follows a gitfile out of the tile.

**The git view** is `refs/xbin/views/<tree>`: a parentless commit of the view
tree `V` (§2.2 step 5), made in the same run as the retention root. Its
message names the checkpoint (`Xbin-Checkpoint: <full tree>`) and, when known,
the work-tree HEAD it was captured from (`Xbin-Work-Tree-Head`), which flow H's
`git rebase --onto` needs; 11-contract §10.3 owns the exact text. It carries
no `Xbin-By`, `Xbin-Session` or error trailer, because it is what a session
fetches. The store never serves it; only the view repository does (§2.7). A
branch made from the full checkpoint would commit `node_modules`, `.env` and
build output into the tile's history, and the next branch switch would delete
them from the work tree (05-model §3).

**The fetch refs** live only in the view repository (§2.7):
- `refs/heads/deploy/<name>` points at the view commit of the checkpoint
  `<name>` is pinned to.
- It is absent while `<name>` follows the work tree: a stale ref would
  mislead someone branching from it (flow H).
- `HEAD` is a symbolic ref to `refs/heads/deploy/<primary>`, dangling while
  the primary follows the work tree.
- Sessions get the fetch refspec `+refs/heads/deploy/*:refs/deploy/*`
  (11-contract §1.12), so `deploy/<name>` resolves after
  `git fetch xbin-deploy` (flow H).

**The deploy log** is a commit chain at `refs/xbin/log/<name>`, in the store,
so only confined git writes it, as the glossary requires of the store. Its
format is [11-contract.md](11-contract.md) §10.3's:
- one commit per **finished** attempt, `ok` or failed, whose tree is the
  attempted checkpoint's;
- the parent is the deployment's previous entry;
- the attempt is described in trailers (`Xbin-How`, `Xbin-Result`,
  `Xbin-Checkpoint`, …).

**Rules for the log:**
- **Writing.** The entry is written by the confined run that follows the
  attempt. The view refresh (§2.7) follows it when a pin moved. Entries that
  were in flight at a crash are written at the next boot from the journal
  (§8.4).
- **Reading.** The deployments plane reads entries with one confined
  `git log` that prints the trailers.
- **Reachability.** The chain keeps every logged checkpoint reachable. GC
  trims it by rewriting the ref to a shorter chain (§2.8).
- **Never served.** The log lives in the store, which no route serves; the
  view repository holds no log.

### 2.4 What a checkpoint contains, and its fidelity limits

| Kept | Lost or excluded | Why |
|---|---|---|
| Every regular file under the tile directory: gitignored files, `node_modules/`, `vendor/`, `deps/`, dot-directories. | — | Glossary: gitignore does not apply. A node backend's `node_modules` is runtime code. |
| Symlinks, as symlinks (mode `120000`). | — | Resolved where used (§3.1, §4.1). |
| The exec bit (`100755`/`100644`). | Other permission bits, owners, xattrs, mtimes. | The git tree format. |
| File bytes, exactly. | — | `info/attributes` disables every in-tree conversion (§2.1). |
| — | Empty directories. | Git tracks files. |
| — | Sockets, FIFOs, devices. | Skipped by type. |
| — | Every `.git` entry (the tile's and embedded repositories'). | The glossary. A pinned backend sees no `.git`; Go builds already pass `-buildvcs=false` (`internal/runner/build.go:86`). |
| — | Registered components nested in the tile. | Other tiles, bound and served from their own primary's code (§3.1, §4.3, §10.4). |
| Hardlinked files, as separate files. | The link. | The git tree format. |

The git view drops, on top of this, what the tile's `.gitignore` files
exclude (§2.3). The checkpoint itself keeps it.

### 2.5 Size caps, capture rate and the per-tile quota

06-security T2 asks for six caps and T10 for a capture rate and a quota;
these are the numbers (NP-07-7). They are v1 constants, which a workspace
policy may raise later.

| Cap | Value |
|---|---|
| Entries per checkpoint | 200 000 |
| File content per checkpoint | 2 GiB |
| Largest file | 256 MiB |
| Tile-relative path length | 1 024 bytes |
| Directory depth | 64 |
| Time | 5 min for a capture (both runs), 5 min for a materialization |
| Captures per tile | a burst of 10, then one every 3 s; past that `429` with `Retry-After`. Drift counts and dry runs create no checkpoint and don't count. |
| Per-tile disk quota | 10 GiB for the store, the view repository, materialized trees and per-checkpoint artifacts (the protected namespace included). diskmon counts it with a walk that never follows a symlink (`filepath.WalkDir`, which uses `lstat`), beside today's per-scope sums (`internal/broker/broker.go:185-198`). An alert at 90%. At 100% GC runs first; if still full, new captures and materializations are refused with the reason, and running deployments keep serving. |

- Admission enforces the per-checkpoint caps on the listing (§2.2). A refused
  capture leaves the store unchanged.
- The error lists the five largest top-level directories, and says that large
  data belongs in a resource (per-deployment data, P22) or outside the tile.
- Before the first capture of a tile (no index yet), run 1 starts with the
  stat-only estimate of §2.11. It refuses an oversized tree before hashing
  it.
- **No exclude list in v1** (NP-07-15; 04-options NP-04-4 is not adopted). An
  excluded path would be absent from the pinned code at the canonical path, so
  a backend that reads it would break only once pinned, and silently. A tile
  over the caps moves its large files into a resource first.

### 2.6 Materialization

**How.** One confined run materializes a tree at
`.xbin/deploy/<TileKey>/<tree>/` ([06-security.md](06-security.md) T2;
layout in [11-contract.md](11-contract.md) §10.4):
- `Dir` is a fresh `.xbin/deploy/<TileKey>/.tmp-<random>/`, read-write, and
  the store is bound read-only;
- the run does `git read-tree <tree>` into a scratch index outside the target
  directory, then `git --work-tree=<tmp> checkout-index -a -f`;
- the same run sets file modes `0444` (`0555` where the exec bit is kept) and
  leaves directories `0755`.

Materialized trees are read-only to sandboxes by bind flags (`RO: true`,
§10.4), not by host modes, so `rm -rf .xbin` and GC's `os.RemoveAll` work
with no chmod walk.

Never `git archive`: it honours `export-*` attributes, so its output is not
the checkpoint.

**Then** xbind renames `tmp → <tree>` (an atomic directory rename). If `<tree>`
already exists, the tmp dir is removed. A present tree is therefore complete.
Leftover `.tmp-*` directories are removed at boot and before each
materialization.

**Why it is safe.** A git tree admitted by §2.2 holds no `..`, no `.git`
component and no gitlink, and cannot hold both a symlink `a` and a path `a/x`.
The checkout also runs confined, with only `<tmp>` writable.

**It is single-flight** per `(tile, tree)`, idempotent and **rebuildable**:
`.xbin/` is "derived state — safe to delete when xbind is stopped"
(`docs/protocol.md:2418`).

**Consumers:**
- the static plane (§4.1);
- the registry's composition and the deployment views (§5.1);
- the backend bind (§10.4);
- builds, through `confine.Cmd.DirFrom` (§10.5);
- the env-layer setup bind (§3.3).

xbind itself reads a materialized tree only through `fsutil.OpenBeneath`,
never `os.ReadFile` or `os.Stat` (06-security T2), for the manifest fields,
`scope.json`, index presence and the native entry of §5.1 and §5.4.

The only thing ever added to a materialized tree afterwards is an empty
mount-point directory for a nested component, which the sandbox init creates
without following a symlink (§10.4). It carries no content.

**Timing.** A pinned deployment is **never** served or run from the work tree.
A request that arrives before its tree exists waits for the single-flight
materialization. After the 5-minute cap it gets `503` with the reason. The
operations of §8 materialize before they commit the record, so in practice
only a first request after `.xbin/` loss or eviction waits.

### 2.7 The read-only fetch remote: the view repository

The model exposes pinned deployments' code as a fetch-only dumb-HTTP remote,
`xbin-deploy` ([05-model.md](05-model.md) §3). It is served from a separate,
xbind-owned **view repository** per tile, `data/checkpoints/<TileKey>.view.git`,
never from the store: no `refs/xbin/*` ref is ever advertised, and no object
outside the current git views can be fetched, because the view repository
holds no other objects.

**Refresh.** One confined run on the view repository (read-write), with the
store bound read-only, after every committed change to which deployments are
pinned, to which checkpoint, or which deployment is primary:
1. On first use, a confined `git init --bare`; xbind then writes `config`
   from constants (`core.bare=true`, `core.logAllRefUpdates=false`,
   `gc.auto=0`, `fetch.unpackLimit=1` and `transfer.unpackLimit=1` so fetched
   objects stay packed, and §2.1's fsync settings). No hooks, no alternates.
2. `git fetch --no-tags --no-write-fetch-head <store> +refs/xbin/views/<T>:refs/heads/deploy/<name> …`,
   one refspec per pinned deployment, from validated names and ids.
3. `git update-ref -d` for each `refs/heads/deploy/*` whose deployment is no
   longer pinned (or no longer exists), then
   `git symbolic-ref HEAD refs/heads/deploy/<primary>`.
4. When a ref moved or went: `git repack -a -d -q`, then
   `git prune --expire=now`, so only the current views' objects remain.
5. `git update-server-info`.

The view repository is derived: deleting it and running a refresh rebuilds it
from the store. It is removed with the record when the tile returns to the
zero state (§8.6), and never exists for a zero-state tile.

**What is served.** Only the dumb-HTTP files of 11-contract §1.12 (`HEAD`,
`info/refs`, `objects/info/packs`, pack and index files, loose objects), each
opened beneath the view repository with every symlink refused. That is a new
`fsutil.OpenNoFollow(dir, rel)`: one `openat2` with
`RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS` and today's
read flags, as the final open of `OpenResolved` already does
(`internal/fsutil/beneath_linux.go:68-71`), regular files only. Never
`http.ServeFile` as the template remote does
(`internal/broker/templaterepo.go:226`), and never `index`, `config` or
anything else of the store.

**Who may fetch** (11-contract §1.12 owns the route): the tile's own terminal
and agent sessions, and humans with at least `write` on the tile, at their
current level, checked per request. No frame, instance or code-grant
principal: dumb HTTP cannot filter objects per caller, and the view
repository holds non-primary code.

**Injection.** Sessions get the remote through `GIT_CONFIG_*` entries next to
today's two (`internal/term/term.go:787-792`), only while the tile has a
record, and never written into `.git/config` ([10-ux.md](10-ux.md),
11-contract §1.12). Nothing is ever pushed through it; pushing is the
separate deploy-remote rung.

### 2.8 Garbage collection and retention

| Kept | Until |
|---|---|
| Every deployment's current checkpoint (the record's pointer). | No longer current. |
| The checkpoints of the last 20 successful deploy-log entries per deployment (roll-back targets). | They fall out of the 20. |
| Any checkpoint younger than 24 h (reload now, a promotion diff, a failed deploy being investigated). | 24 h. |
| Deploy-log entries: the last 50 per deployment. | Trimmed by rewriting `refs/xbin/log/<name>` to a shorter chain, in one confined run. |
| Materialized trees: current and previous per deployment. | Evicted; rebuildable. |
| Go artifacts: current plus the last 3 distinct per deployment. | Evicted; rebuildable with the caveat of §3.1. |
| A protected primary's current and previous artifacts and env layers. | Never evicted while protected: only a manager can rebuild them (§3.4). |
| Env layers any running generation or pinned manifest references. | §3.3. |

**Never evict what a running generation binds.** A host `rm -rf` of a
materialized tree empties the directory under a live bind mount, and the
backend loses its files. GC skips every root and artifact `Runner.InUse()`
names (06-security T18). D112 rows gain no field for this: `main`'s rows stay
byte-identical (§10.1).

**Removing a tree.** `os.RemoveAll`: directories are `0755` (§2.6), and Go's
implementation opens each directory with `O_NOFOLLOW`, so it never follows a
symlink out of the tree.

**When GC runs:** after each successful deploy (for that tile), at boot
(background, after the always-on step), and on deployment removal.

**Store GC** is one confined run:
1. delete the unreferenced `refs/xbin/checkpoints/*` and their
   `refs/xbin/views/*`;
2. `git prune --expire=1.hour.ago`;
3. once a day at most, `git repack -a -d -q`.

The store is never served, so it needs no other packing. A removed tile's
store is a leftover (D82, [05-model.md](05-model.md) §11), never collected
automatically. Purging a checkpoint is a tile manager's act whose route is
not defined yet (14-implementation R-4).

### 2.9 Drift: how far the work tree moved

The terminal window shows how many files changed since the pinned checkpoint
(flow A, [10-ux.md](10-ux.md)). The count is computed on demand, in one
confined run that changes nothing durable:
- it uses a scratch duplicate of the persistent index and a scratch quarantine,
  both discarded afterwards;
- it runs step 1 of §2.2 against them, then
  `git diff-index --cached --name-status <pinned tree>`.

It runs when a watcher batch touches a tile whose live reload is paused (§6),
and when a client asks, debounced to one run per tile every 2 s. That is the
only per-save cost live reload adds, and only while it is paused.

### 2.10 Cost estimates

These are grounded in D78's measured ~45 ms per confined run on
fuse-overlayfs (~17 ms on kernel overlay). They are **estimates**; the
checkpoint work package benchmarks them against §13's targets.

| Operation | Confined runs | 500 files / 5 MB | 5 k files / 50 MB | 50 k files / 300 MB (`node_modules`) |
|---|---|---|---|---|
| First capture (no index) | 2 (+1 per new embedded repo) | ~0.15 s | ~0.5 s | 2–4 s |
| Re-capture, unchanged tree | 1 | ~60 ms | ~90 ms | 0.2–0.4 s (stat walk) |
| Re-capture, 10 files changed | 2 | ~110 ms | ~150 ms | 0.3–0.6 s |
| Materialize (full checkout) | 1 | ~80 ms | ~0.4 s | 3–6 s |
| Deploy-log entry | 1 | ~50 ms | ~50 ms | ~60 ms |
| View refresh (the view excludes gitignored trees such as `node_modules`) | 1 | ~60 ms | ~0.1 s | ~0.2 s |

The big-tree materialization figure is why §13.3 proposes differential
materialization (NP-07-8).

### 2.11 Dry runs and diffs never create a store

The store and the view repository come into being only with a committed
opt-in (05-model §5).
- **Dry runs of pausing live reload and adding a deployment** capture
  nothing. They compute the impact with one confined stat-only walk of the
  work tree (`find -P`, which never follows a symlink; nested components
  pruned; the work tree bound read-only and nothing else writable): entries,
  bytes, largest file, depth, longest path and embedded repositories, checked
  against §2.5's caps. The answer adds what needs no capture: whether a Go
  rebuild follows (§8.6), whether isolation allows the operation (§12), and
  whether the backend is running. Nothing is written.
- **A work-tree diff on a tile without a record** answers `409` and runs
  no tool: the tile runs its work tree, so there is nothing to diff
  (11-contract §1.11).

## 3. Builds from a checkpoint

### 3.1 Go

**Artifacts.**
- A checkpoint's artifact is `.xbin/build/<CompKey>/c/<tree>/bin`, next to a
  `build.json` recording its inputs.
- The build writes into `…/c/<tree>.tmp-<rand>/` and renames it on success,
  so a crash never leaves a partial artifact that looks valid.
- The live reload target keeps today's single `.xbin/build/<CompKey>/bin`
  (`internal/runner/runner.go:372`). Only one deployment follows the work
  tree (P8), so nothing contends for it.

**Reuse.** Artifacts are keyed by `(tile, tree)`, not by deployment. `build()`
returns an existing artifact when its `build.json` is present and records the
same tile path (`CompKey` keeps 32 bits of hash, so the path is checked), so:
- promoting from a pinned deployment needs no compile, except onto a
  protected primary (§3.4);
- neither does adding a deployment from the primary's checkpoint;
- every restart of a pinned deployment reuses its artifact (P9).

**How.** `buildConfined` (`internal/runner/build.go:38-97`) keeps its
toolchain, caches, network and timeout. What it sees changes:

| Mount | Today | From a checkpoint |
|---|---|---|
| The tile at `c.Dir` | `Dir: c.Dir, ReadOnlyDir: true` (`build.go:87`) | `Dir: c.Dir, DirFrom: <materialized tree>, ReadOnlyDir: true` (§10.5). The checkpoint is at the canonical path, so `go.work`'s `use ./<tile>` and relative `replace` lines resolve unchanged. |
| Components nested in the tile | Visible inside the tile bind. | Excluded from the checkpoint, so each is bound back at its own path, after the checkpoint bind, from **its own primary's code** (its work tree or its pinned checkpoint). The mount point is created without following a symlink (§10.4). Without this, `go.work` would `use` a missing directory (`internal/deps/deps.go:148-163`). |
| Other tiles' Go modules | The workspace, read-only (`build.go:53`). | Unchanged, except that every tile in `go.work` whose **primary is pinned** has that checkpoint bound over its directory: cross-tile references follow the other tile's primary ([05-model.md](05-model.md) §6). |
| `deps/` links inside the checkpoint | Followed in the sandbox. | Still resolved only inside the sandbox, at the canonical path, where they reach the other tile's primary code (row above). xbind never follows them on the host (§4.1). |
| `<root>/go.work` | The workspace's file. | Unchanged, unless the checkpoint's module sits elsewhere than the work tree's (`go.mod` at the root versus `backend/`). Then a per-build `go.work` with that one `use` line corrected is file-bound over `<root>/go.work`, read-only. |
| Output | `…/build/<CompKey>/`, read-write (`build.go:57`) | `…/build/<CompKey>/c/<tree>.tmp-<rand>/`, read-write, and nothing else of `.xbin/build`. A work-tree build of a tile with a record also masks `…/build/<CompKey>/c/`, so no build ever writes a checkpoint's artifact; a zero-state tile has no `c/` and gets today's spec. |
| Caches | `.xbin/cache/tile/<CompKey>/{go-build,mod}` (`build.go:43-45`) | The same, shared by the tile's deployments, except a protected primary's (§3.4). It is one tile with one set of writers; the isolation between tiles stays. |

**`build.json`** records:
- the tile path, the Go toolchain (`go version` output), `GOFLAGS` and the
  passed Go env (`build.go:108-109`);
- every `go.work` module with its code pointer (work tree, or tree hash);
- the module set `go.sum` selects;
- the time and duration.

**The reproducibility caveat** is documented, never hidden:
- A checkpoint pins the tile's own files. Its artifact also embeds other
  tiles' Go code as their primaries stood at build time, the modules `go.sum`
  selects, and the host toolchain.
- The artifact is kept and reused, so this matters only when it must be
  rebuilt: after `.xbin/` loss, or for an evicted historical checkpoint.
- A rebuild compares its inputs with the old `build.json`, and the deployment
  status shows "rebuilt with different inputs: …". It does not block, except
  for a protected primary (§3.4).

**Without isolation** a checkpoint build is refused (§12). A direct
`go build` has no mount namespace to show the checkpoint at `c.Dir`
(`internal/runner/runner.go:385-392`).

### 3.2 node, python, static

- **node and python** have no build. `build()` checks the entry against the
  *code root* instead of `c.Dir` (`internal/runner/runner.go:403-406`),
  through `fsutil.OpenBeneath` for a materialized tree. It returns the
  **canonical** path `c.Dir/<entry>` as argv, because the code root is bound
  at `c.Dir` in the sandbox (§10.4).
- **Static** tiles (no backend) have nothing to build. A deploy is:
  materialize, commit the record, announce the reload (§8.5).

### 3.3 The env layer (`setup`)

- **The manifest.** `setup` is deployment-level ([05-model.md](05-model.md)
  §6), so `ensureEnvLayer` (`internal/runner/env.go:52-131`) reads it from the
  deployment view.
- **The setup bind** becomes `{Src: <code root>, Dst: c.Dir, RO: true}`
  (today `c.Dir` at itself, `env.go:78`).
- **Sharing.** Layers stay keyed by the script plus a rootfs fingerprint
  (`env.go:39-46`), so deployments with the same script share one. A script
  that reads tile files (for example `requirements.txt`) builds from whichever
  deployment first needed that hash. That is today's caveat extended: editing
  the file without the script never rebuilt the layer either. A protected
  primary never shares (§3.4).
- **GC.** `gcEnvLayers` keeps only the newest layer today (`env.go:129`,
  `:134-145`). It becomes `gcEnvLayers(c, keep set)`. The set is every hash
  referenced by a running generation, a pinned deployment's manifest, or the
  live reload target's current manifest. Otherwise a pinned deployment's
  restart would rebuild its layer.
- **VM backends** still refuse `setup` (`internal/runner/vm.go:70-72`).

### 3.4 A protected primary's build products

While the primary is protected, what it runs changes only by a tile manager's
act (P21; 06-security T16, T17 and NP-06-6). Artifacts, caches and env layers
are part of what it runs, so they get their own namespace:
- **Where.** Under `.xbin/deploy/<TileKey>/protected/`: artifacts at
  `build/<tree>/`, Go caches at `cache/{go-build,mod}`, env layers at
  `env/<envHash>/`. No other build binds anything there: a work-tree build
  binds `.xbin/build/<CompKey>/` read-write (`build.go:57`), and
  `gcEnvLayers` sweeps `.xbin/env/<CompKey>/` (`env.go:134-145`), so neither
  may hold them.
- **Who writes.** Only builds that a manager-initiated operation onto the
  protected primary starts: deploy, promote, roll back, reload now,
  reassignment, a restart-as-deploy (§8.7), and protecting itself. Builds for
  non-primary deployments and for the live reload target never bind these
  directories, and never read them.
- **Protecting** takes the pin (05-model §5) and rebuilds the primary's
  current checkpoint into the protected namespace, as a queued deploy of the
  same checkpoint: the first time from empty protected caches (a cold Go
  build, module downloads included) and with `setup` run afresh. The primary
  keeps serving its generation until the code-identical swap.
- **Promotion onto a protected primary** reuses an artifact or layer only
  from the protected namespace, and otherwise rebuilds there.
- **Eviction.** GC never evicts the protected primary's current and previous
  artifacts and layers (§2.8).
- **A restart that finds them missing** (`.xbin/` loss) rebuilds a Go
  artifact only when every input its `build.json` recorded matches: the
  checkpoint, each `go.work` module's code pointer, the `go.sum` module set
  and the toolchain. The plane keeps a copy of that `build.json` in
  `data/deployments/<TileKey>/protected-build.json`, which survives the
  loss. A missing env layer is never rebuilt on a restart path, because
  `setup` reaches the network. Otherwise the start is **held** (NP-07-14): the
  primary stays down, its status says "the protected primary's build products
  were lost; a tile manager must redeploy c:<id>", and the managers get an
  alert. A manager's restart-as-deploy rebuilds.
- **Unprotecting** returns the primary to the shared namespaces at its next
  build.

Tests (15-test-plan): `TestProtectedBuildProductsSeparated` (with the
lost-products case: a restart rebuilds only when every recorded input
matches, and otherwise holds the start), `TestProtectedPrimaryLayerNotShared`.

## 4. Serving

### 4.1 The code-root resolver

Every opener uses one resolver:
- `openLegacy` (`internal/server/static.go:193-219`);
- `openStrict` (`internal/server/tileassets.go:133-164`, also used by asset
  tokens at `:308`);
- the tile-origin plane;
- the native route.

The resolver is `CodeRoot(tile, dep) (root string, pinned bool, err error)`,
a new `server.Policy` method, with its opener in the new
`internal/server/deployserve.go`. `NoopPolicy` answers today's
`(c.Dir, false)`.

| The deployment's code | Root | How files are opened |
|---|---|---|
| Follows the work tree (every zero-state tile) | `c.Dir` | Exactly today: legacy `fsutil.OpenResolved` with cross-tile symlinks and the dev overlay; strict `fsutil.OpenIn`. |
| Pinned | The materialized tree (§2.6) | `fsutil.OpenBeneath(root, rel)` in **every** asset mode (`internal/fsutil/beneath.go:19-34`). An in-tree symlink works. `OpenResolved` and the dev overlay (`static.go:206-217`) never apply. `SafeJoin` and `pathAllowed` still apply to the URL. |

**Symlinks that leave a checkpoint** are never followed on disk (P16;
06-security T2):
- **`deps/` links.** When `OpenBeneath` fails with `ErrEscapes` and the path
  is `deps/<name>/<rest>`, the static plane reads the checkpoint's own
  `deps/<name>` entry with `readlinkat` beneath the materialized tree (never
  following it), resolves its text lexically against `<tile>/deps/` at the
  canonical path, and requires the result to be a registered component. It then
  re-dispatches the request to `/c/<target>/<rest>`: resolution,
  authorization and serving happen exactly as for a direct request for that
  URL by the same principal, in every asset mode, so the other tile's primary
  answers under the other tile's gate. The target never comes from the work
  tree's manifest or `deps/` links, so a work-tree edit cannot re-point what a
  pinned primary imports (P9).
- **Any other escaping symlink** (`../x`, `/etc`, a `deps/` entry that
  resolves to no registered component) answers `404` (`ErrEscapes`).
- In builds and backends the links resolve inside the sandbox at the
  canonical path (§3.1).

### 4.2 Bare URLs serve the primary

**The static handler.** `handleComponentStatic`
(`internal/server/static.go:70-180`) changes in one place: after
`owningComponent` (`:105`) and the RBAC check (`:106-115`), it opens through
`CodeRoot(owner, Primary(owner))`. A nested component URL still resolves to
the nested tile by longest prefix
(`internal/registry/registry.go:519-536`), and so to *its* primary's root.

Everything else follows the registry's composed component (§5.1), which is
already the primary's: `inject` (`:157-158`), `chrome` in `sandboxedFrame`
(`:317-325`), `native` and `HasIndex`. `Cache-Control: no-store` stays
(`:153`); pinned bytes change only by a deploy, which reloads the frames
(§8.5).

**The `/api/` proxy.** The template gate (`internal/proxy/proxy.go:135-139`)
reads the composed component: `template` is inbound surface, so the
primary's. The no-backend gate (`:140-144`) and `EnsureDeployment`'s runtime
check (`internal/runner/runner.go:191-193`) read the target deployment's view,
which for the primary is the composed component. `runtime` is
deployment-level: a pinned primary with a Go backend keeps serving `/api/`
even after the work tree switched to `static`.

### 4.3 Qualified URLs

The resolver is [11-contract.md](11-contract.md) §2.2's `ResolveRef`, in the
new `internal/registry/qualifier.go`. It replaces `Reg.Resolve` in the proxy
(`internal/proxy/proxy.go:130`) and runs before `owningComponent` in the static
handler (`internal/server/static.go:105`). The properties the runtime relies
on:

- **Every URL that resolves today resolves identically** (12-compat PO-1).
  - The fast path (`strings.IndexByte(path, '+') < 0`) keeps every existing
    URL on today's code.
  - A qualified URL resolves only for tiles with a record, and only after
    today's resolution fails. A component at least as deep, or anything on
    disk at the full candidate `<tile>+<name>`, wins. So a directory whose
    name contains `+`, and `/c/<tile>+main/` on a zero-state tile, behave
    exactly as today.
- **Unambiguous.** Names cannot contain `+` or `/`, so splitting on the last
  `+` of a segment has one reading. Two narrow refusals, for every creator,
  keep the two spaces apart (05-model §7): no tile is created at `<P>+<N>`
  while `P` has deployment `N`, and no deployment `N` is added to `P` while a
  component exists at `<P>+<N>`. Any other new tile name containing `+` gets
  a one-release warning, never a refusal (11-contract §2.1 owns the text).
- **Unknown names and nested tiles.** An unknown name on a tile with a record
  is a 404, never a fallback to a parent tile's file. A qualified URL never
  enters a nested component (11-contract §2.4), which matches checkpoint
  contents.
- **What the runtime does with the answer.** For `(t, n, rest)`, the static
  plane opens `rest` beneath `CodeRoot(t, n)` (§4.1). The proxy calls
  `EnsureDeployment(ctx, t, n)` and `TrackDeployment(t.Path, n)`.
- **Self-calls and sessions.** A call on a bare `/api/<self>/` routes to the
  calling principal's deployment (P12): the frame-token claim (§4.4), the
  instance token (§9), or a terminal or agent session's target. The target is
  chosen when the session starts, in the terminal window's API dropdown
  (P24): the primary by default; a protected primary is never offered, and
  the default then falls to the live reload target, or to "API off" when
  neither exists. It is fixed for the session's life ([10-ux.md](10-ux.md),
  11-contract §7.4).
- **Authorization** for qualified targets is 11-contract §2.3's table (from
  [09-fabric.md](09-fabric.md) and [06-security.md](06-security.md)): humans
  with at least `write`, at their current level on every request, never the
  bare `p.Component == tile` test; the tile's own principals only for their
  own deployment. A non-primary principal calling `/api/xbin/*` meets the
  default-deny route classification (P26): any route not classified
  deployment-scoped or neutral refuses it.

### 4.4 The injection and the frame-token claim

The single sanctioned transform (D4, `headInjection`,
`internal/server/static.go:431-466`) gains only additive parts. They follow
[11-contract.md](11-contract.md) §0.2's two rules, and all are absent while
`main` is the primary, which covers every zero-state tile.

| Part | Rule |
|---|---|
| `xbin-component` | Always the **tile path**, never `<tile>+<name>`. `xbin.self` and `/api/${xbin.self}` keep working, routed by the claim. |
| Frame token | **Name rule.** It carries a deployment claim naming the served deployment when that is not `main`, so when `dev` is the primary, bare-URL documents carry `dev`. Renewal (`internal/server/api.go:241-252`) keeps the claim of the renewing principal. The token shape (today a 4- or 5-field verifier, `internal/auth/frametoken.go:252-266`) is 11-contract's; who may mint (`mayMintFrameToken`, `static.go:494-503`) is [06-security.md](06-security.md)'s. |
| `<meta name="xbin-deployment">` | **Role rule.** Present when the served deployment is not the primary (11-contract §6). |
| Import map | For a document served at a **qualified** URL, one added entry, `"/c/<tile>/": "/c/<tile>+<name>/"`, so absolute self-imports of modules stay in the deployment (the tokens-mode precedent, `tileassets.go:338`). Absolute `/c/<tile>/…` in `src` or `href` attributes still loads the primary's files, documented as in tokens mode. |
| CSP sandbox | A document of a non-primary deployment is always sandboxed: `sandboxedFrame` answers true for it whatever its code says about `chrome` (05-model §6). Chrome tiles have no non-primary deployments anyway (P19). |

- **Pausing live reload changes no injection.** A pinned `main` primary's
  documents are byte-identical to today's except for the file bytes, so
  `TestLegacyInjectionUnchanged` (`internal/server/tileassets_test.go:187`)
  keeps holding for every tile whose primary is `main`.
- **Legacy-mode subresources** of a non-primary document arrive with no
  credential and stay authorized by path, as code the tile's readers may
  already read; documents and `/api/` are write-gated in every mode
  (05-model §7).

### 4.5 The native runtime document

- **The entry.** `serveNativeRoute` (`internal/server/native.go:54-97`)
  resolves it from the served deployment's view: `native` is
  deployment-level, and `native.js` must exist in the served code. For a
  pinned deployment the view resolves it with an `OpenBeneath` check against
  the materialized tree. `resolveNative`'s `os.Stat`
  (`internal/registry/native.go:140-167`) never runs on a checkpoint
  (06-security T2).
- **The generated document** (`native.go:108-128`) imports `./<entry>`
  relative to its URL, so a qualified URL loads the deployment's entry.
- **Shipped apps** address only `/c/<tile>/` (the compat native contract), so
  they always get the primary.
- **Reloads.** A pinned primary's saves emit nothing, so the app never reloads
  on them. A deploy that changes the primary's code emits one bare `reload`,
  and the app reloads once (§8.5).

### 4.6 Strict asset modes

- **tokens.** `serveAssetToken` (`internal/server/tileassets.go:275-324`)
  resolves through the qualifier and opens through `CodeRoot`. `tokenBase`
  puts `r.URL.Path` into `<base>`, so a qualified document's relative
  subresources keep the qualifier under the token.
- **origins.** Each deployment has its own origin label, keyed by name;
  `main` keeps today's, and the bare URL goes to the primary's origin
  (05-model §7). On the tile's origin a non-primary document would share the
  primary's localStorage and IndexedDB
  ([research/serving-fabric.md](research/serving-fabric.md) A.8).
  - The label, ticket and cookie changes are [11-contract.md](11-contract.md)'s
    and [06-security.md](06-security.md)'s; 14-implementation's WP-38 builds
    them in M2.
  - A qualified document is served, through `CodeRoot(t, n)`, only on its
    deployment's origin, never on the tile's. A build without those labels
    answers `404` with the reason for a qualified document in origins mode;
    it never falls back to the tile's origin.

## 5. The registry split

### 5.1 The registry's component is the primary's; views for the others

The model splits the manifest three ways ([05-model.md](05-model.md) §6).
"The primary's code" is the work tree while the primary is the live reload
target (every zero-state tile), and its checkpoint while it is pinned.

| Field | The registry's component (the primary) | A non-primary deployment's view |
|---|---|---|
| `Path`, `Dir` (canonical), `Scope` (membership) | the work tree | the same |
| Tile-level (authority requests): existence, `uses`, `interfaces`, `deps` | the work tree (the primary's checkpoint when Kept, §5.3) | the same, plus §5.2's `uses` union for env |
| Inbound surface: `template`, `exposes`, `expose` (its `roles`), `provides`, `chrome` | the primary's code | not used: a non-primary deployment receives no inbound edge, and its documents are never chrome |
| Deployment-level: `runtime`, `entry`, `setup`, `alwaysOn`, `vm`, `inject`, `native`, `scope.json`'s `resources` | the primary's code | its own code: the work tree for the live reload target, its checkpoint otherwise |
| `HasIndex`, `Native`, `NativeErr` | resolved in the primary's code | resolved in its own code |
| `ManifestErr` | the work tree's parse error | — |

**Composition** (NP-07-11). The registry keeps one `Component` per path
(`internal/registry/registry.go:373-385`), and that component describes the
primary:
- The registry gains one hook, `PinnedPrimary func(rel string) (*PinnedCode, bool)`,
  set by the deployments plane: an O(1) map lookup that answers only for a
  tile with a record whose primary is pinned. It subsumes 13-surfaces'
  `Keep` hook (§5.3).
- `PinnedCode` holds what the plane read from the primary's materialized
  checkpoint through `fsutil.OpenBeneath`, parsed as `Rescan` parses a
  manifest (JSONC, `registry.go:458-467`): the manifest or its parse error,
  `HasIndex`, `Native` and `NativeErr` (resolved by `OpenBeneath`, never by
  `resolveNative`'s `os.Stat`), and the checkpoint's `scope.json` when the
  tile roots its scope (§5.4). File contents never appear in errors. The plane
  prepares it before it commits a pointer onto the primary.
- `Rescan` builds each directory's component as today. When the hook
  answers, it replaces the inbound-surface fields, the deployment-level
  fields, `HasIndex`, `Native` and `NativeErr` with the primary's, and keeps
  the work tree's scan (manifest, `HasIndex`, native entry) as
  `Component.WorkTree`, which is nil whenever the hook doesn't answer. When
  the tile roots its scope, the scope's `resources` and `importMap` come from
  the checkpoint's `scope.json` (§5.4; NP-07-12); whether the directory roots
  a scope at all stays the work tree's.
- A checkpoint manifest that does not parse composes as the zero manifest,
  exactly as today's registry treats a work-tree manifest that does not parse:
  no backend, no exposes, no chrome.
- The plane re-runs `Rescan` after every commit that changes the primary's
  code or which deployment is primary (a deploy, promote, roll back or reload
  now onto it; pausing live reload on it; attach; protect; reassignment). The
  tile-level reactions of a watcher batch follow: `Provision` and the ingress
  reconcile (§6).
- **Boot** installs the hook and re-runs `Rescan` before the first
  `Provision`, ingress reconcile and listener start. A pinned primary whose
  tree is missing is materialized first (tiles in parallel, §2.5's time cap
  each). A tile whose preparation fails composes with no inbound surface and
  no backend (no chrome, exposes or provides), and its status names the
  failure: it fails closed.

**What follows, with no reader edited:**
- Every reader of `c.Manifest` on an inbound path follows the primary:
  `sandboxedFrame` (`internal/server/static.go:317-325`), the proxy's template
  gate (`internal/proxy/proxy.go:135-139`), public ingress
  (`internal/proxy/ingress.go:33`), the ingress and expose planes
  (`internal/broker/ingressfn.go:45`, `:73-95`;
  `internal/broker/exposefn.go:29`, `:210`), role checks
  (`roleSatisfies`, `internal/broker/broker.go:371`), and the admin list
  (`internal/broker/admin.go:124-134`).
- A save while the primary is pinned changes nothing that serves traffic,
  and a protected primary's framing and routes change only through a
  manager's deploy (05-model §6).
- **`/components`** (`internal/server/api.go:162-198`; one tile at
  `:202-230`): `runtime`, `hasIndex`, `native` and `/c/<tile>/?native=1`
  describe the primary's code, and `chrome` and `template` its inbound
  surface. `manifestError`, `uses`, `deps` and `roles` stay the work tree's
  (05-model §6): the handler reads `roles` from `c.WorkTreeManifest().Expose`,
  the one edit in `api.go`. The grants UI therefore offers the roles the
  tile's authors are writing, while enforcement follows the primary's code; a
  grant approved for a role only the work tree declares takes effect when that
  code reaches the primary. The deployments summary `/components` adds is
  11-contract §8's, and for a reader it carries only primary-scoped facts
  (05-model §8).

**Views for the other deployments.** `View(c, code)`, in the new
`internal/registry/deployview.go` ([13-surfaces.md](13-surfaces.md)
NP-13-3):
- `code` is the primary's code: `c` itself. The zero-state view is the
  registry's own pointer.
- `code` is the work tree while the primary is pinned (a non-primary live
  reload target): the fields of the table's right column from
  `c.WorkTree`.
- any other checkpoint: parsed from its materialized tree through
  `OpenBeneath`, the same way as `PinnedCode`.
- Every view carries the in-memory `Deployment` (empty for the primary) and
  code-root fields of 13-surfaces NP-13-3, which the spawn-time hooks read
  (§1.2). The registry's own component leaves both empty.
- **Caching.** Views are cached by `(path, tree, hash of the work-tree
  manifest)`; checkpoints are immutable.
- **Errors.** A checkpoint whose manifest does not parse cannot start. A
  deploy that would put it on a deployment fails before it commits
  (materialize and parse come first), and a restart reports it as that
  deployment's build failure (§8.5).

### 5.2 Spawn-time env for a pinned deployment

`EnvFor` derives `XBIN_RES_*` from the manifest's `uses`, filtered by grants
(`internal/broker/resources.go:71-98`). Taking `uses` purely from the work
tree would let a work-tree edit (dropping a `uses` entry) remove a pinned
deployment's resource env at its next restart, which breaks P9.

**The rule for a pinned deployment** (05-model §6): env comes from
`uses(work tree) ∪ uses(its own code)`, **still filtered by `grantedRole`**.
- Authority is unchanged: only targets the tile holds grants for yield env.
- Interface and ingress env (`resources.go:103-143`) come from bindings and
  stay late-bound (P10).

### 5.3 A pinned primary survives the work tree

**Today.** `Rescan` registers a directory only if it holds `xbin.json` or
`index.html` (`internal/registry/registry.go:471-474`). A work tree
mid-refactor would take the tile down: `/api/<tile>` answers 404
(`internal/proxy/proxy.go:130-134`).

**With the `PinnedPrimary` hook** (§5.1):
- When the hook answers and the directory exists but has neither file, **or**
  its `xbin.json` does not parse, `Rescan` still
  registers the component, with `Kept: true`, the tile-level fields of the
  primary's checkpoint manifest, and the work tree's error surfaced ("the work
  tree has no valid xbin.json; serving the pinned primary").

**What follows:**
- The pinned primary keeps serving the static plane, `/api/` and its inbound
  edges, with its resources, slots and routes intact.
- A non-primary live reload target rebuilds from the broken work tree and
  shows an honest build failure (§8.5).
- `go.work` is unaffected: `goModules` lists only directories with a `go.mod`
  (`internal/deps/deps.go:155-158`), and checkpoint builds bind their own
  `go.work` when needed (§3.1).
- Only lifecycle state, or the directory itself disappearing, takes such a
  tile down.
- With the primary following the work tree, nothing is kept. That is today's
  behaviour, and it is the behaviour of every zero-state tile.

### 5.4 Resource declarations per deployment (P22)

- **Where they come from.** A deployment provisions what its own code
  declares (05-model §6): the `resources` of its own code's `scope.json` when
  the tile roots its scope, which is the work tree's for the live reload
  target and the checkpoint's otherwise. For a tile that doesn't root its
  scope, which file declares a namespace's resources is
  [08-data.md](08-data.md)'s (a plain-directory scope's own file applies to
  every deployment).
- **Reading a checkpoint's `scope.json`.** Through `OpenBeneath` on the
  materialized tree, JSONC-parsed, then every resource name validated with
  08-data's rule and error text (non-empty; no NUL, no leading `/`, no `.`,
  `..` or empty segment). A file that fails either check provisions nothing,
  and the deployment's start fails with the reason.
- **The primary.** The registry's composed scope (§5.1) carries the primary's
  declarations, so today's `Provision` (`internal/broker/resources.go:36-66`)
  provisions them unchanged. A resource the primary's code no longer declares
  is kept, as today.
- **A non-primary deployment.** The deployments plane has the data plane
  provision the view's declarations in `(scope, name)` (08-data owns the
  call) before the deployment starts: at every deploy onto it, and on each
  watcher batch while it is the live reload target (§6).
- **Env.** `EnvFor(c, dep)` resolves an own-scope `uses` target against that
  deployment's declarations, and every other target against the registry as
  today (`parseRes`, `resources.go:73-78`). A `uses` entry for a resource the
  deployment's code doesn't declare yields no env, as today.
- **Limits** are per deployment (§10.3).

## 6. Watcher gating

**Today.** The watcher's consumer is `watchLoop`
(`internal/boot/serve.go:161-185`). After the tile-level work (rescan,
provision, pending grants, ingress, `deps`, `go.work`: `:163-174`), it calls
`changedComponents` (`:175`, defined at `:192-205`). For each changed
component it emits `reload` (`:178`) and calls `run.Changed(c)`, unless the
change touched only the native entry (`:179-181`).

**The tile-level work keeps its order and calls.** What each call sees
changes through the composed component (§5.1):
- `Rescan` recomposes; `RefreshPending` reads `uses` from the work tree, as
  today;
- `Provision` provisions the primary's code's declarations, and the ingress
  reconcile reads the primary's `exposes`: while the primary is pinned, a
  work-tree edit changes neither;
- one addition: for each tile whose live reload target is not the primary,
  the plane provisions that deployment's namespace from its work-tree
  declarations (§5.4).

The loop over changed components goes through one new pure function in
`internal/boot/liveroute.go`, table-tested in `liveroute_test.go` beside
`TestChangedComponentsNativeEntry` (`internal/boot/watch_native_test.go:16`):

```go
type liveTarget struct {
	c       *registry.Component
	dep     string // the live reload target
	primary bool   // dep is the primary: today's bare `reload`; otherwise a `deployments` op "reload"
	restart bool   // not a native-entry-only change, judged by the live reload target's own manifest
}

// liveTargets maps one batch's changed components to what live reload drives.
// lr answers from the deployment records in memory: ("main", true) for a tile
// without a record; ("", false) while its live reload is paused.
func liveTargets(reload map[string]*registry.Component, restart map[string]bool,
	lr func(tile string) (dep string, attached bool), primary func(tile string) string) []liveTarget
```

`changedComponents` computes `restart` with the live reload target's view
(`View(c, {WorkTree: true})`), so a native-only edit in the work tree is
judged by the work tree's manifest even while the composed component
describes a pinned primary.

**The new body of `serve.go:176-182`.** For each target: when `t.primary`,
emit today's bare `reload`; otherwise emit `deployments` op `reload`
naming `t.dep` (§8.5). Then call `run.ChangedDeployment(t.c, t.dep)` when
`t.restart` is set. So:
- A zero-state tile gets exactly today's event and call.
- A tile whose live reload is paused yields **no target**: no event, no
  rebuild. Frames of pinned deployments never reload on saves.
- A non-primary live reload target's frames, served at its deployment URL,
  reload on its `deployments` op; no old event type ever names it.

**Timing and extras:**
- Pausing live reload, attaching it elsewhere, and deploying onto the live
  reload target clear the in-memory target at the **start** of the
  operation, before the record commits (§8.6). Saves stop driving that
  deployment at once.
- `run.WakeAlwaysOn()` (`:183`) stays, waking `(tile, dep)` pairs (§11).
- For a tile whose live reload is paused, a batch that touches it triggers a
  drift count (§2.9), debounced to one run per tile every 2 s. The loop emits
  `deployments` op `work-tree` when the count moved
  ([11-contract.md](11-contract.md) §3.3).

## 7. Every restart path

Every row reaches `EnsureDeployment → buildAndStart → CodeFor`, or is an
explicit code change. That is what makes P9 hold.

| # | Path | Today | A pinned deployment | Non-primary notes |
|---|---|---|---|---|
| 1 | Lazy start: proxy, ingress, `DialInto`, `ensureProvider` | the `Ensure` callers (§1.3) | Builds from `CodeFor`, its pinned tree; the artifact is reused, with no compile. | Only the proxy reaches it: qualified URL, self-call, session target. |
| 2 | Idle reap | `internal/runner/runner.go:809-831` sets `dirty` (`:822`) | The next request restarts the same checkpoint. | Reaped like any backend unless its alwaysOn switch is on (§11). |
| 3 | Crash watch | `runner.go:334-356`: `dirty` (`:353`) + `afterExit`; the breaker sets a sticky `lastErr` (`:349-351`) | Restarts the same checkpoint. The breaker clears only on a deploy or restart of that deployment (§8.7) or on `ChangedTile`, never on a save. Its message (`:350`) says "deploy a fixed checkpoint or restart it" for a pinned deployment, and names the deployment's log. | The same, per deployment, reported on `deployments` op `build` (§8.5). |
| 4 | alwaysOn backoff | `internal/runner/alwayson.go:100-137` (`AfterFunc` `:121`) | `aoStart → EnsureDeployment`: the same checkpoint. | Only with its switch on (§11). |
| 5 | Grant, binding, net set, interface-instance or transfer change | `OnGrantChange` (`internal/boot/boot.go:508-512`), fired from `internal/broker/broker.go:648-666`, `internal/broker/netfn.go:825-845`, `:1117-1128`, `internal/broker/netsetsapi.go:180-183`, `internal/broker/transfer.go:267-277` | `ChangedTile`: every deployment with a generation rebuilds from `CodeFor`. Same tree and artifact, **new spawn-time env** (P10). | Included: authority is per tile. |
| 6 | Provider nudge | `runner.go:581-597` (`:593`) | `ChangedTile(client)`. | Non-primary generations never splice (§10.4). |
| 7 | Re-enable | `internal/broker/lifecycle.go:101-105` → `WakeAlwaysOn`; others lazily | The same checkpoint. | alwaysOn only with the switch. |
| 8 | Vault unseal | `internal/broker/broker.go:214-236` → `WakeAlwaysOn`; `EncryptionHold` gates starts (`internal/broker/resenc_wire.go:106-129`) | The same checkpoint, once its namespace is mounted. | The hold checks its own namespace and mounts it on demand ([08-data.md](08-data.md)). |
| 9 | xbind restart | the state map starts empty (`runner.go:168-171`) | Records persist in `data/`, and artifacts and trees in `.xbin/`. The journal is reconciled first (§8.4). The first start reuses them. | Lazy. |
| 10 | Loss of `.xbin/` | "derived state, safe to delete" (`docs/protocol.md:2418`) | Materialized again from the store in `data/`. Go is rebuilt with §3.1's caveat. The checkpoint, the code, never changes. A protected primary's start may be held instead (§3.4). | The same. |
| 11 | A save (watcher batch) | `internal/boot/serve.go:176-182` | Nothing (§6). | Only as the live reload target. |
| 12 | Deploy, promote, roll back, reload now, resume, pause live reload, attach | — | **The only ways its code changes** (§8). | The same. |
| 13 | Disable, hide, offload, restore, seal | `Stop` via `StopBackend` (§1.3) | Stopped. Restore rewrites the work tree, which reaches only the live reload target, and restores the record and store ([08-data.md](08-data.md)). | Stopped with the tile. |
| 14 | Builtin updates (D49), code PRs (D48), clone, import | work-tree writes → watcher | Nothing. | Only as the live reload target. |
| 15 | Env-layer rebuild | inside `start` (`runner.go:442`) | Its own manifest's `setup` and code root (§3.3); GC keeps the layer. | The same. |
| 16 | Primary reassignment | — | Its running generation is restarted, blue/green, with the same checkpoint and artifact and a new spawn-time env (§8.8). | The same, for the old and the new primary. |

## 8. Deploy mechanics

### 8.1 The deploy worker

`Runner.Deploy(ctx, c, dep, code, commit, progress)` lives in
`internal/runner/deploy.go`.
It takes the state's single-flight build turn exactly as `EnsureDeployment`
does, waiting on `buildDone` while another build runs. Then it runs
`buildAndStart(c, s, code)` with the code given explicitly.

**How it reports.** A deploy that puts a **checkpoint** on a deployment
reports through a `progress(phase, result, err)` callback, never through
`build-*` ([11-contract.md](11-contract.md) §3). The deployments plane turns
the callback into `deployments` op `deploy` events, the journal's phases
(§8.4) and the deploy-log entry. `build-*` keep today's meaning, for the
primary only: builds of the work tree (live reload, resume, attach) and
restarts of the code it already runs. The same builds of a non-primary
deployment ride `deployments` op `build` (§8.5).

**The steps:**
1. **Build.** Report phase `build`. Resolve the view and code root; build or
   reuse the artifact (§3; a protected primary's from §3.4's namespace).
2. **`stopFirst`** (`internal/runner/vm.go:128-133`): a VM backend with file
   resources stops the old generation first.
3. **Start and check.** Start the new generation (§10.4) and `waitHealthy`
   (`internal/runner/health.go:12`). The check stays a socket connect within
   5 s (60 s for a VM, 180 s emulated, `vm.go:47-55`), the documented backend
   contract (`docs/protocol.md:2394-2400`).
4. **Swap** `cur`, then call `commit()`. `commit` comes from the deployments
   plane; for a pinned → pinned deploy it writes the record's new pointer
   atomically (temp file, rename) and marks the journal entry `swapped`. When
   the deployment is the primary, the plane then clears the primary's stored
   status (a deploy emits no `build-start`, which would otherwise clear it),
   re-runs `Rescan` and the tile-level reactions (§5.1). A non-primary
   deployment's stored status is cleared in the deployments plane.
5. **Drain** the old generation: SIGTERM, then kill after 30 s (D8;
   `internal/runner/runner.go:328-330`, `:728-740`).
6. **Announce.** Report result `ok`, then announce one reload for the
   deployment, only when the served code changed (§8.5). The deploy-log run
   (§2.3) and the view refresh (§2.7) follow, off the critical path.

Blue/green overlap stays bounded (30 s) **within** a deployment. Deployments
of one tile never share a data namespace, so D81's flock-and-epoch design,
which assumes a bounded overlap on a shared db, is not stretched
([research/inbound-edges.md](research/inbound-edges.md) §1).

### 8.2 What the record's pointer means

The record's `checkpoint` is **the code every (re)start of that deployment
builds from** (05-model §4). It is written at one of two moments:

| Transition | When the pointer is written |
|---|---|
| Pinned → pinned (deploy, promote, roll back or reload now onto a pinned deployment) | **After a successful swap** (`commit`). A failed deploy leaves the pointer, so restarts keep running the previous code, whose artifact is kept. |
| Work tree → pinned (pause live reload; attach live reload elsewhere; deploy, promote or roll back onto the live reload target) | **At request time**, atomically with `liveReload`, once the checkpoint exists and is materialized; its state becomes `failed` if the build or start then fails. There is no nameable previous code to fall back to. After a failed build the old generation keeps serving, with the error shown ([05-model.md](05-model.md) §5). |
| Pinned → work tree (resume; attach live reload to this deployment) | At request time: `checkpoint = null` and `liveReload = X`, then `ChangedDeployment(c, X)`, with today's semantics. |

Beside the pointer, the runner reports `serving` (the code its current
generation runs) and the last error. The status can then say "pinned to
c:3f2a1c9 — build failed; serving an earlier generation". 11-contract owns the
field names.

### 8.3 Queueing: one deploy in flight per deployment

- The deployments plane (`internal/deployments`) serializes the operations
  on each deployment: a FIFO at most 8 deep, one running. A full queue
  answers "deploy queue full" and changes nothing.
- The runner's `Deploy` then serializes with `EnsureDeployment` through the
  state's build turn, so a lazy start, a crash restart and a deploy never
  race.
- The checkpoint of a request is taken at **request time**, synchronously,
  before queueing. The API returns its id and the journal entry's id, and a
  promotion diff can be shown first.
- A request whose checkpoint equals the queue's tail is merged into it.
- Deployments of one tile run in parallel. Attaching live reload to `Y`
  queues `X`'s pin and `Y`'s work-tree build on their own queues.
- A deploy of a tile that is not enabled fails the `ShouldRun` gate, as
  `Ensure` does today (`internal/runner/runner.go:197-199`).

**Reviewed operations onto a protected primary** (05-model §5; 11-contract
owns the fields):
- Deploy and roll back name `checkpoint`; promote, reload now and
  reassignment name `expect`. Without it the request answers `400` before
  anything is captured or queued.
- The plane deploys exactly the named checkpoint and never a fresh capture.
  A promote from a deployment that follows the work tree, and a reload now,
  capture once only to compare with `expect`, and answer `409` when the tree
  differs.
- The commit is a compare-and-set on the record's `seq`, with the actor's
  authority re-checked; a record that moved since the review answers `409`.

**The manager gate.** Manager operations (limits, protect and unprotect,
reassignment, edge policies, deliveries, alwaysOn, seeding, purge) pass it
before anything else: a human session (`p.Component == ""`) and `IsAdmin`
(`internal/auth/auth.go:92`) or `mayManageTile`
(`internal/broker/orgsapi.go:427`). Terminal and agent tokens fail it, and so
does every element principal, whatever `xbin` or `xbin:users` grants its tile
holds (05-model §10).

### 8.4 Failure semantics

| Failure | The deployment | Record | Clients see |
|---|---|---|---|
| The checkpoint fails (a cap, a hygiene rule, an unreadable file, a store error, the rate limit, the quota) | Nothing changes. When pausing or attaching live reload, it stays attached: the in-memory target is restored, and `ChangedDeployment(c, X)` runs once to catch up on the saves ignored meanwhile. | Unchanged. | The API error, with the reason. No hub event. |
| Materialization fails, or the checkpoint's manifest or `scope.json` is refused (§5.1, §5.4) | The previous generation keeps serving. | Unchanged. | The API error. |
| The build fails | The previous generation keeps serving. | Pinned → pinned: unchanged, with `lastError` set. Work tree → pinned: the pointer is already set (state `failed`), so restarts retry the pinned tree while the old generation serves. | `deployments` op `deploy`, result failed; the compiler output goes to the deploy log and the deployment's log, and no frame overlay is painted. A work-tree build (resume, attach) fails with today's bare `build-error` on the primary, or `deployments` op `build` on a non-primary deployment. |
| Start or health fails | The same as a failed build. After a `stopFirst` (a VM with file resources) on a pinned → pinned deploy, the runner restarts the **previous** checkpoint from its kept artifact: "keeps running its previous code" holds as "restarts its previous code". On a work tree → pinned deploy there is no previous checkpoint, so the deployment stays down with the error until a reload now succeeds, as today when a crash meets a broken work tree. | As above. | As above. |
| xbind crashes mid-deploy | At boot the record is the last committed state, and the runner starts that code lazily. | Authoritative. | The journal's entry, reconciled into the deploy log (below). |
| A VM reservation is refused (budget, headroom: §10.2) | The previous generation keeps serving. | Unchanged. | As for a failed build, plus an `sbx.Refuse` failure row carrying the deployment. |

**The deploy journal keeps every attempt through a crash** (NP-07-13;
02-goals SC-AUDIT). The plane keeps `data/deployments/<TileKey>/pending.json`,
written atomically (temp file, rename); 11-contract §10 owns the format.
- A request gets its entry id when it is accepted, from a per-tile counter
  kept in the journal, and is written there as `queued` before the API
  answers.
- It becomes `running` when its turn starts. The write that commits the
  record's pointer also marks it: at request time for work tree → pinned
  (`pointer: request`), and `swapped` after the swap for pinned → pinned.
- When the attempt finishes, the confined deploy-log run appends the entry
  (§2.3), and the journal then drops it.
- **At boot**, before any start, every journal entry is appended to the
  deploy log: `queued` as `cancelled`; `swapped` as `ok`; `running` as
  `interrupted`, naming the checkpoint the record now points at (the
  attempted one when the pointer was written at request time, the previous
  one otherwise), which is the code the deployment will run. The journal is
  then emptied.
- Entry ids survive the restart, so a `log?id=` poller gets the reconciled
  entry instead of a 404 (11-contract §1.10 follows).
- Test: `TestDeployLogSurvivesCrashMidDeploy`, with a pausing-live-reload
  variant and a pinned → pinned variant. 15-test-plan and WP-14b add it if
  Q3 of [16-open-questions.md](16-open-questions.md) adopts the journal.

### 8.5 Events per deployment (rule C2)

**No old event type ever names a non-primary deployment**
([05-model.md](05-model.md) §8; [11-contract.md](11-contract.md) §3):
- The primary's events keep today's types (`reload`, `build-*`, `status`) and
  the bare tile path, exactly as today.
- A non-primary deployment's activity rides only the `deployments` type: bare
  `component`, the deployment named in `data`. The runtime uses op `reload`
  and op `build` (with its phase `start`, `ok` or `error`, and the text) for
  what the primary would send as `reload` and `build-*`; 11-contract §3.3
  owns the op names and payloads.
- No qualified `component` string appears on an old type, ever: not for
  deploys, not for reassignment, not for lifecycle. A qualified component
  would not protect old clients: `bx-frame`'s ownership test
  (`web/bx-frame.js:366-369`) and `isReloadTarget`
  (`web/events-socket.js:40-49`) accept `<ancestor>/…`, so
  `apps/shop/admin+dev` would reload an open `apps/shop` frame, zero-state or
  not.

The choice is made in one place, `Runner.emit` (§1.2). The runner's `build-*`
emit points are `internal/runner/runner.go:288`, `:292`, `:312`, `:321`,
`:351`, `:358` and `internal/runner/env.go:72`; each becomes an `emit` call.

| Trigger | The runtime emits |
|---|---|
| A save, live reload on the primary | Today's `reload`, then `build-*`, bare. |
| A save, live reload on non-primary N | `deployments` op `reload` for N, then op `build` phases for N. |
| A save while live reload is paused | `deployments` op `work-tree` when the drift count moved (§6); nothing else. |
| Resume or attach onto X (X now follows the work tree) | X's build: bare `build-*` when X is the primary, op `build` otherwise; plus `deployments` op `deploy`. |
| A deploy that puts a checkpoint on X | op `deploy` on each phase or result (§8.1). After a swap that changed X's served code: one bare `reload` when X is the primary, op `reload` for X otherwise. Never `build-*`. |
| A restart of X's current code (crash, reap, grant change, alwaysOn) | Bare `build-*` when X is the primary; op `build` otherwise. |
| A record change (live reload attached or paused, primary, protection, edge policy, deliveries, alwaysOn) | `deployments` op `record`. |
| A primary reassignment | op `record`, one bare `reload`, and op `reload` naming the old and the new primary (§8.8). |
| A lifecycle change | Today's bare `reload` (`internal/broker/lifecycle.go:114`), plus op `reload` for each non-primary deployment. |

**The status plane** clears a status only for the exact component of a
`build-start` (`internal/obs/status.go:129-143`). Non-primary builds emit no
`build-start`, so they never clear the primary's status, and no `status`
event is ever published for a non-primary deployment: its status lives in
the deployments plane and rides `deployments` op `status`. The primary's
status is cleared at a deploy's swap (§8.1 step 4).

**The reload after a deploy** is announced once, after the swap, and only when
the served code changed:
- deploying, promoting or rolling back to a **different** checkpoint
  announces it, and so does resume;
- pausing live reload and restarts do not (same bytes).

Static tiles announce it after the record commits.

**Who receives it** is the server's hub filter (11-contract §3.4; 05-model
§8), not the runtime's:
- facts about the primary go to the tile's readers, as today;
- anything naming a non-primary deployment (its name, builds, compiler
  output, status, would-notify lines, data operations) goes only to admins,
  humans with at least `write` on the tile at their current level, and that
  deployment's own principals plus the tile's terminal and agent sessions;
- the primary's frame token, minted for readers, is not an own principal for
  non-primary facts;
- other tiles receive none of it.

### 8.6 Pause live reload, resume, reload now and attach: the edge cases

| Situation | Behaviour |
|---|---|
| Pause live reload, the normal case | 1. Clear the in-memory target. 2. Checkpoint `c:P` and materialize it. 3. Commit `liveReload=""` and `X.checkpoint = c:P` (state `failed` if its build then fails, §8.2). 4. Queue the deploy of `c:P` to X. That deploy is a swap that moves X off the work tree, which node and python read at runtime (and Go backends read files too). It is file-identical within the checkpoint's fidelity limits, so there is no reload. |
| Pause live reload on a Go tile | Always a warm-cache build of `c:P`, typically 1–3 s. The work tree's `bin` is never reused: the runner cannot prove it was built from exactly `c:P`, because the watcher ignores `node_modules/`, `vendor/` and dot-directories (`internal/watch/watch.go:61-74`) and a checkpoint includes them. |
| …while X is dirty or building | The checkpoint is taken at request time. The in-flight build finishes and swaps, then the deploy queued by pausing live reload swaps to `c:P` through the build turn: the newest state ships once, as the model says. |
| …while X has no process (never started, reaped) | Pin only. The artifact is prepared in the background (materialize; build for Go) without starting a process, so errors surface now. The next request starts `c:P`. |
| …while X is crash-looping | Pin. The breaker stays until a deploy or restart (§8.7). |
| …on a static-only tile | Checkpoint, materialize, commit. No process, no reload. |
| The build after pausing live reload fails | Live reload stays detached. X serves its current generation with the error shown until a reload now succeeds ([05-model.md](05-model.md) §5). |
| Resume onto X | Refused when X is a protected primary. Otherwise commit `liveReload = X`, `checkpoint = null`, then `ChangedDeployment(c, X)`, and a reload after the swap. A failed build leaves the pinned generation serving, and the next save retries: today's semantics. |
| Reload now | Checkpoint `c:R` at request time, then deploy `c:R` to the last target (pinned → pinned). If `c:R` is X's checkpoint and X is healthy, the answer is "already running c:R", with no swap. Onto a protected primary it needs `expect` (§8.3). |
| Attach live reload to Y (X was the target) | Refused when Y is a protected primary. Otherwise clear the in-memory target, checkpoint `c:A` and materialize it, then commit `X.checkpoint = c:A`, `liveReload = Y`, `Y.checkpoint = null`. X's pin deploy and Y's work-tree build queue on their own deployments. |
| Deploy, promote or roll back onto the live reload target | The work tree → pinned row of §8.2. Live reload pauses. |
| Remove Y while Y holds live reload | Detach, leaving the tile with live reload paused. `StopDeployment(tile, Y)`. Delete `.xbin/deploy/<TileKey>/d/Y/`, Y's socket dir and its cgroup leaf, and refresh the view repository. Trees and artifacts only Y used fall to GC. |
| Return to the zero state (resume with only `main` and default settings) | The record, the journal and the view repository are deleted, `Rescan` recomposes nothing, and `main` follows the work tree. The store stays, inert, until the tile opts in again, a manager purges it, or the tile is removed. |

### 8.7 A restart is a deploy of the same checkpoint

There is no restart endpoint today
([research/runner-hot-reload.md](research/runner-hot-reload.md) §4). Pinned
deployments need one, because a save cannot revive a crash-looped pinned
primary.

Deploying the checkpoint a deployment already runs (NP-07-2):
- is a **no-op** when its generation is healthy;
- **starts a new generation from the same artifact** when it is not
  (crash-looping, failed, down, held), and clears the breaker.

An explicit `restart` flag (11-contract) forces a new generation either way.
Onto a protected primary it is a reviewed operation like any deploy
(`checkpoint`, §8.3), and it rebuilds lost build products in the protected
namespace (§3.4).

### 8.8 Reassigning the primary: runtime effects

Reassignment is an M2 operation for tile managers, in a human session, with
a loud confirmation, and only where no scope's primary data would split
(05-model §5). 11-contract fixes its wire: op `record`, one bare `reload`,
and `deployments` op `reload` naming the old and the new primary, with no
qualified component on any old type (§8.5). The runtime:

1. **Protection first.** On a protected tile the request names `expect`, and
   the new primary Y is pinned to exactly that checkpoint in the same commit
   (compare-and-set on `seq`). If Y followed the work tree, live reload
   pauses, because a protected primary is never the live reload target.
2. **Routing.** `Primary(tile)` answers the new name at once, in memory. Every
   inbound caller of §1.3 resolves to it on its next call, and its events use
   the bare component from then on. `Rescan` recomposes the tile from Y's
   code (§5.1), then `Provision` and the ingress reconcile run.
3. **Restarts.** The running generations of both deployments restart,
   blue/green, with unchanged code: Y first, then the old primary.
   - Their spawn-time env changes: `XBIN_DEPLOYMENT` (§9), the registration
     set, and the network wiring of §10.4.
   - L3 roles follow the restart: the provider roster is registered by the
     generation that starts (`internal/runner/runner.go:555-561`), and
     clients splice by tile path (`:519`). The new primary registers the
     roster and nudges its clients; the old one, now non-primary, registers
     nothing.
   - A deployment with no running generation just starts right on its next
     request.
4. **Reloads.** They re-navigate open frames, which re-mint their tokens
   with the claim of the deployment they now show. Otherwise a claimless
   token (`main`) would keep routing self-calls to the old primary, because
   renewal keeps the claim (`internal/server/api.go:250`).
5. **alwaysOn** (§11). The new primary is woken if its manifest says alwaysOn.
   The old one becomes reapable unless its switch is on.

## 9. Per-deployment artifacts

Storage keys and credentials follow the deployment **name** (P6; 11-contract's
name rule). Markers that describe routing follow the **role** (its role rule).

| Artifact | `main` (today, unchanged) | Other deployments | Ref today |
|---|---|---|---|
| Socket dir | `<RunDir>/<CompKey>/g<gen>.sock` | `<RunDir>/d-<16 hex>/g<gen>.sock` (`sockDir`). A sibling of `main`'s dir, never inside it: that dir is bound read-write into `main`'s sandbox (`internal/runner/runner.go:642`). The hash keeps the path under the 108-byte socket limit. A `d-<16 hex>` name can never be a `CompKey`, whose ninth-from-last character is always `-`. | `runner.go:414-419` |
| Log | `.xbin/log/<CompKey>.log`, a documented path (`docs/protocol.md:2401`) | `.xbin/deploy/<TileKey>/d/<name>/backend.log`, read through `GET /logs?…&deployment=` (11-contract §8). `bx logs` reads the file directly (`cmd/bx/main.go:511`) and moves to the API only where the file can't answer, as in isolated terminals ([research/side-findings.md](research/side-findings.md) #22; [16-open-questions.md](16-open-questions.md) Q23). | `runner.go:471-478`; `internal/obs/logs.go:69` |
| Build output | The live reload target: `.xbin/build/<CompKey>/bin` | Pinned code, whichever deployment runs it: `.xbin/build/<CompKey>/c/<tree>/bin` (§3.1); a protected primary's under `.xbin/deploy/<TileKey>/protected/build/` (§3.4) | `runner.go:372` |
| Go caches | `.xbin/cache/tile/<CompKey>/…` | Shared within the tile, except a protected primary's (§3.4). | `build.go:43-45` |
| Env layer | `.xbin/env/<CompKey>/<envHash>/` | Shared by hash (§3.3), except a protected primary's (§3.4). | `env.go:30-35` |
| cgroup | Flat `comp-<CompKey>`, in the zero state and for main-only tiles. | Nested `tile-<CompKey>/d-<name>/backend` once the tile runs a non-main deployment (§10.3). | `internal/cgroup/cgroup_linux.go:89` |
| VM guest | One per generation; `Reserve(c.Path, …)`. | The same, charged to the **tile** (§10.2). The hostname is `hostname(c.Path)+"-"+name`, cut to 63 characters. | `internal/runner/vm.go:80`, `:136-154` |
| Instance token | `RegisterInstance(token, path)` → `Principal{Component, Via: "instance"}` | `RegisterInstance(token, path, dep)` (in `internal/auth/deployment.go`). The instance map value gains the deployment, and the principal gains `Deployment` ("" means `main`), so self-calls stay in the deployment (P12). | `internal/auth/auth.go:445-449`, `:571-572`, `:589-590`; map `:219` |
| Backend env | `XBIN_SOCKET`, `XBIN_COMPONENT` (the tile path, for every deployment), `XBIN_GATEWAY`, `XBIN_TOKEN` | Plus `XBIN_DEPLOYMENT=<name>` when the deployment is not the primary at spawn (role rule, 11-contract §5), kept true by §8.8's restarts. `XBIN_RES_*` values are identical, remapped by binds (§10.4). | `runner.go:423-431` |
| Registry entry | `backend:<CompKey>:g<gen>`, no `deployment` field, even when the tile has a record | `backend+<name>:<CompKey>:g<gen>`, with `deployment` (§10.1) | `internal/runner/sbx.go:63` |
| Stats series | Keyed by tile. | Keyed by tile and deployment. | `internal/runner/stats.go:166-170` |
| Materialized trees | — | `.xbin/deploy/<TileKey>/<tree>/`, per tile and shared (§2.6). | — |

## 10. Sandbox mechanics: the shared layer

Deployments are **not** built on D113's tile-managed sandboxes (the owner's
direction, [05-model.md](05-model.md) §12). A deployment's backend stays a
runner backend.

What changes is the layer that backends, terminals and later tile sandboxes
all share: the registry, the VM books, the cgroup tree, the launch spec, the
sandbox init and confine.

### 10.1 Registry entries per deployment (D112)

**Today** (`internal/sbx/sbx.go:50-74`; `internal/runner/sbx.go:59-75`): one
`sbx.Entry` per generation, with the ID `backend:<CompKey>:g<gen>`, `Tile` set
to the path, and `Leaf = CompKey` when cgroups are on.

**The additive changes** (05-model §12):

| Change | Rule |
|---|---|
| `Entry.Deployment string \`json:"deployment,omitempty"\`` | Set **only on non-`main` entries**. `main`'s rows are byte-identical to today's, even when the tile has a record. |
| ID | Name rule. `main` keeps `backend:<CompKey>:g<gen>`; other deployments use `backend+<name>:<CompKey>:g<gen>` (11-contract §8). The prefixes differ, so the two forms can never collide, whatever a tile path contains. Names contain no `:`, so non-`main` IDs parse unambiguously. |
| `Tile` | Always the tile path. `Filter{Tile}` and `?tile=` list every deployment's generations under their tile. |
| `Filter.Deployment` | Matches `Entry.Deployment`, with empty read as `main`. It is a new struct field; the response gains no new keys. |
| `Failure.Deployment` | Set on refusals and failures of non-`main` deployments, and part of the coalescing key (`sbx.go:223-224`). A `dev` VM refusal is then attributable, and never merges with `main`'s. |
| `Leaf` | The generation's own leaf: flat `<CompKey>`, or nested `tile-<CompKey>/d-<name>/backend` (§10.3). `main`'s `Leaf` changes only when the tile first runs a non-main deployment. |
| Stats scope | In `GET /api/xbin/sandboxes`, `main`'s backend rows keep `scope: "tile"`; non-`main` rows use `scope: "deployment"`, their own leaf (`internal/boot/sandboxes.go:117-120`; 11-contract §8). |

Confined tool runs stay **unregistered**, as D112 chose (per-call churn).

### 10.2 VM books charged to the tile

- **Booking.** `vmApply` calls `r.VM.Reserve(c.Path, mem)`
  (`internal/runner/vm.go:80`) for every deployment. `byOwner` and `UsedBy`
  (`internal/vm/policy.go:132-182`) keep booking to the tile. A deployment is
  never a budget owner.
- **Leaf caps.** The VM leaf cap stays 2 × (guest + overhead) **per
  deployment leaf** (`vm.go:106-111`), because each deployment still runs its
  own blue/green pair.
- **Headroom (P25).** A non-primary deployment's VM reservation must leave
  at least the primary's guest size free in the global budget, and never
  preempts a primary start. Otherwise a `dev` guest could block the primary's
  next blue/green swap. This is an option on `Reserve`, next to the per-owner
  books ("per-tile quotas … would be checked here", `policy.go:130-131`).

### 10.3 The per-tile cgroup parent

**Today:**
- one flat leaf per tile, `comp-<CompKey>`
  (`internal/cgroup/cgroup_linux.go:89`), shared by blue/green;
- `Add` writes the caps, then the pid (`:94-104`);
- any generation's exit calls `Remove` (`internal/runner/runner.go:613-615`).

Two deployments in one leaf would share one memory cap. Worse, one
generation's exit can `rmdir` the leaf between another's `mkdir` and its
`cgroup.procs` write. That process then stays unaccounted in xbind's `init`
leaf.

**The layout** is built once in `internal/cgroup`, for this project and D113
alike (05-model §12):

```
<xbind base>/
  comp-<CompKey>                  zero-state and main-only tiles: today's flat leaf, unchanged
  tile-<CompKey>/                 from the tile's first non-main deployment (or, later, its first D113 sandbox)
    d-main/                       per-deployment node (no processes of its own)
      backend                     that deployment's generations (blue/green share it)
      sbx-<name>                  D113: that deployment's tile-managed sandboxes (later)
    d-dev/
      backend
```

| Node | Controllers | Caps (v1) |
|---|---|---|
| `tile-<CompKey>` | `+cpu +memory +pids` in `subtree_control` | `cpu.weight` 100. The tile competes with other tiles exactly as one flat leaf does today, so deployments never enlarge a tile's share of the machine. No memory or pids cap in v1; per-tile totals are D113's admission check. |
| `d-<name>` | the same | `cpu.weight` 100 for the primary, 50 for non-primary deployments (NP-07-5). Under contention, one busy non-primary deployment takes at most a third of the tile's share. No memory cap. |
| `backend` | leaf | The deployment's limits (below). A VM uses `AddMem` with the 2× guest cap. |

**Per-deployment limits (P22).**
- Every deployment's backend leaf carries its own memory and pids caps, and
  no memory cap above the leaves is shared by the primary and a non-primary
  deployment. With the weights, that meets 06-security T10: a non-primary
  runaway hits its own limits first.
- The tile's limits are today's per-component caps: `XBIN_LIMIT_MEM` (default
  2 GiB), `memory.high` at 7/8 and the pids ceiling
  (`internal/boot/boot.go:565-580`). A deployment's `limits` in the record
  (05-model §4) may lower them, never raise them; empty means the tile's.
- Only tile managers set them, through the manager gate (§8.3). The runner
  reads them through `LimitsFor` at each start, and the cgroup manager gains
  `AddWith(name, pid, Limits)` beside `Add`.

**When a tile moves** (05-model §12):
- The layout is chosen per generation, at start, and stored on the generation
  (`instance.leaf`).
- A tile stays flat while it has only `main`, so pausing live reload changes
  nothing here.
- It gets the parent when it first runs a non-`main` deployment. That
  deployment starts in the nested layout, and `main` moves in at its next
  generation. The old generation drains in `comp-<CompKey>`, whose last exit
  removes it.
- The two layouts coexist for at most one drain. No live process is ever
  moved between cgroups.

**Why the flat leaf stays.** P5 stays literal: the leaf names and paths of
zero-state tiles never change. That covers `/api/xbin/sandboxes`'s `leaf` and
the alerts (`internal/boot/boot.go:434-463`, `:443`).

**The manager API** grows in `internal/cgroup/cgroup_linux.go`, with stubs in
`cgroup_other.go`.
- A name containing `/` is a nested path under the base, used verbatim. A
  name without one keeps today's `comp-<name>` mapping. So `Usage`, `AtLimit`
  and `Procs` work on both.
- Parents are created with `subtree_control` before their leaf. Empty nodes
  are removed bottom-up after their last leaf goes, each `rmdir` only when
  `cgroup.procs` is empty.
- `cgroup.procs` is not recursive, so the manager adds a recursive
  `ProcsTree(name)`. The stats sampler (`internal/runner/stats.go:166-170`)
  and the alerts use it, and read the parent's hierarchical counters for
  per-tile totals.

**Boot sweep.** Empty `tile-*` subtrees left by a crashed xbind are removed.
Non-empty ones are logged, and D113's orphan sweep kills them
(`plans/tile-sandboxes.md:242-243`).

**Admission caps and the build limiter (NP-07-7; P25).**
- At most 3 non-primary deployments per tile, and 24 per workspace.
- At most 12 non-primary backends **running** at once per workspace. A start
  past that is refused with `sbx.Refuse`, and the reason reaches the
  deployment's build failure (§8.5) and the failure ring.
- At most `max(1, NumCPU/4)` builds for non-primary deployments at once,
  workspace-wide (Go builds, env-layer setups and materializations). A
  primary's build never waits for this limiter: it builds as every build does
  today, so the zero state keeps its timing and the primary always goes first.

### 10.4 Backend launch-spec binds and network wiring

`sandboxCmd` (`internal/runner/runner.go:638-726`, moving to
`internal/runner/sandboxcmd.go`, §14) builds this bind list:

| Bind | Today | Pinned or non-primary |
|---|---|---|
| Code | `{Src: c.Dir, Dst: c.Dir, RO: true}` (`:641`) | `{Src: <code root>, Dst: c.Dir, RO: true}`. `Src ≠ Dst` is already supported by the init (`internal/sandbox/init_linux.go:391-423`), and VM exports use the destination as the shim sees it (`internal/vm/manager.go:339-371`). The live reload target keeps today's work-tree bind. |
| Nested components | inside the code bind | Bound back from their primaries' code (§3.1), after the code bind, at mount points made without following symlinks (below). |
| Run dir | `{Src: dir, Dst: dir}` (`:642`) | The deployment's own socket dir (§9). |
| Gateway socket | `:643` | Unchanged: one handler for every deployment. The instance principal's `Deployment` routes self-calls, and xbind's own routes refuse a non-primary principal unless classified for it (P26). |
| Resources | `resourceBinds(env, root)` binds each `XBIN_RES_*` dir at itself (`internal/runner/binds.go:28-54`, `:51`) | `resourceBinds(env, root, remap)` → `{Src: remap[d].Src, Dst: d}`. A non-`main` deployment's volume is bound **at the canonical resource path**, `main`'s mount, so `XBIN_RES_*` values are identical (P17; the scheme and its `.xbin/resenc/.deployments/…` volumes are [08-data.md](08-data.md)'s). |
| Go artifact | `{Src: bin, Dst: /run/backend, RO: true}` (`:652-655`) | The per-checkpoint artifact. |

**Nested mount points never follow a symlink** (06-security T18.3). A
checkpoint taken before a nested tile existed can hold a symlink at its path,
and roll back, adding from `c:<id>` or restore can bring such a checkpoint
back. So in the sandbox init (`internal/sandbox/init_linux.go`):
- a bind whose destination lies inside another bind's destination gets its
  mount point created by walking from the enclosing bind's mounted root with
  `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` and `mkdirat` for missing
  components, then mounted onto that opened directory (its `/proc/self/fd`
  path), never by `os.MkdirAll` on a path string;
- an enclosing bind's read-only remount happens after its nested binds are
  mounted, because the mount point doesn't exist in the checkpoint (nested
  components are excluded from it) and a read-only bind can't take a
  `mkdirat`;
- a symlink or file in the way fails the start with the path named.

The empty directories this creates in a materialized tree carry no content
(§2.6). Confined builds get the same rule, because they launch through the
same init.

**The remap entry**, per 08-data §4:

```go
type ResBind struct {
	Src  string // the host dir backing the canonical path for this deployment
	RO   bool   // read-only (v1 never sets it)
	Omit bool   // no bind at all: a blocked edge, so the path is absent in the sandbox
}
```

For `main` the remap maps every path to itself, so the binds are today's
(`Src == Dst`).

**Network and capability wiring of a non-primary generation** (P3, P23;
05-model §7). The primary's wiring is today's. For a non-primary view the
spawn-time hooks (§1.2) answer as below, and the runner backstops the
primary-only rows itself: whatever a hook answers, it never registers a
roster, splices, takes lan-ingress legs or builds ingress plumbing for a
non-primary generation, and never sets `HostNet` for one.

| Wiring (today's hook) | A non-primary generation |
|---|---|
| Net slot bound to the host, directly or through an org, personal or named set whose rules say host (`NetHost`, `runner.go:702`; `Broker.NetHostShare`, `internal/broker/netfn.go:196`) | No egress (`block`, P23), with the reason recorded ("the tile's network shares the host"). Never host networking. |
| Net slot bound to a provider tile: a splice (`NetTarget`, `:512-524`, `:704-707`) | No egress and no splice (P23). Two splicers on one provider link would also split packets, because the link is keyed by tile path (`:519`). |
| Relay egress under the tile's `net:*` grants and builtin policy (`Egress`, `:709-711`) | `inherit` (the default): the same relay policy. `block`: deny-all, and no relay unless ingress plumbing needs one, which it doesn't (next rows). |
| Provider roster (`NetRoster`, `:508-510`, `:559`, `:690-692`) | None: L3 roles are primary-only ([09-fabric.md](09-fabric.md) §5.8). |
| Lan-ingress legs (`NetLinks`, `:564-567`, `:693-695`) | None (P23). |
| Ingress plumbing (`IngressNet` `:716`, `IngressFwd` `:541-542`) | None: inbound edges reach only the primary. |
| `gpu:*` devices (`GPU`, `:663-668`) | `block` by default (a shared device cgroups don't cover); `inherit` binds the tile's devices. |
| `cap:net-admin`, `cap:containers` (`NetCaps`, `ContainerCaps`, `:696-701`) | `inherit` by default; `block` drops them. |

Calls a non-primary deployment makes through the gateway (custom roles with
no path to `reader`, stream interfaces) are refused or clamped by 09-fabric's
resolver, not by the runner.

**The runner fails closed:**
- **No unmapped data.** A non-`main` deployment with a path-valued resource
  that has no remap entry aborts its start ("no data namespace for `<name>`'s
  resource `<X>`"). A missing entry would bind `main`'s data. An `Omit` entry
  binds nothing, and the env var stays.
- **An unknown or invalid edge value** reads as `block` in every hook's answer (P27).
- **Tests.** `resourceBinds` stays pure, so `TestResourceBinds`
  (`internal/runner/resourcebinds_test.go:9`) gains the remap cases:
  - `main` unchanged;
  - `dev` remapped, `Omit` unbound;
  - a missing entry → error;
  - no non-`main` spec binds a `Src` under `main`'s `.xbin/resenc/<ScopeKey>/`
    (08-data §4).

  A table test over the wiring above pins each row for a non-primary spec,
  the host-sharing tile included.

### 10.5 The confine bind destination

**Today.** `confine.Cmd.binds()` always binds `Dir` at itself
(`internal/confine/confine.go:218-224`), and a direct run (isolation off)
ignores `Binds` entirely (`:140-143`).

**Why a new field.** A same-depth extra bind would shadow `Dir` through
`sortBinds`' stable ordering (`internal/sandbox/sandbox.go:116-128`). But in
direct mode it would **silently build the work tree instead**: the host
fallback D78 forbids.

**The change** (05-model §12; 13-surfaces agrees):

```go
type Cmd struct {
	// …today's fields…
	DirFrom string // host path mounted at Dir (default Dir itself); needs isolation
}

// At binds a host path at another destination; needs isolation.
func At(src, dst string, ro bool) sandbox.Bind

var ErrNeedsIsolation = errors.New("confined run: this job shows another path at its destination and needs --isolate")
```

- **Isolated runs.** `binds()` uses `{Src: DirFrom, Dst: Dir, RO: ReadOnlyDir}`
  when `DirFrom` is set, and the working directory stays `Dir` (`:153`).
- **Direct runs.** A direct run whose `DirFrom` differs from `Dir`, or that
  carries a bind made by `At`, returns `ErrNeedsIsolation` and never runs. A
  bind a caller builds by hand keeps today's direct-run behaviour whatever
  its `Src` and `Dst` (the git import's `~/.ssh` at `/root/.ssh`,
  `internal/broker/gitimport.go:63-67`), so a user without `--isolate` loses
  nothing (16-open-questions Q22).
- **Users.** Checkpoint builds (§3.1), plus the `go.work` file bind and the
  nested-component binds through `At`.
- **Tests.**
  - `TestConfinedGoBuild` (`internal/runner/build_linux_test.go:32`) gains a
    checkpoint build that plants a different file in the work tree and
    asserts the artifact came from the checkpoint.
  - A unit test asserts the direct-mode refusal.

### 10.6 Streaming confined runs (M3)

**Why.** Confine buffers and caps output (`capped`,
`internal/confine/confine.go:250-265`; `Result`, `:87-89`); only stdin streams
in (`Cmd.Stdin`, `:79`). The deploy remote (a later rung,
[05-model.md](05-model.md) §15) runs `git receive-pack --stateless-rpc` per
request, with the request body on stdin and stdout as the response body.
- Its report is small, so a buffered first cut works.
- Streaming is needed for side-band progress, and for any future
  `upload-pack`.

**The design.** It borrows the conventions of D113's exec protocol
(`plans/tile-sandboxes.md:87-121`) without depending on its unbuilt code,
through the plainest API: `Cmd` gains optional `Stdout` and `Stderr`
`io.Writer` fields. `confine.Run` then streams into them instead of the
capped buffers (05-model §12). An HTTP handler passes its
flushing `ResponseWriter`.

**What it takes from D113's conventions:**
- an output cap that **ends the run** with a loss marker instead of dropping
  silently, because a protocol stream cannot survive silent truncation;
- on timeout, TERM to the sandbox's process group, then KILL after a grace;
- the same D78 guarantees: no host fallback under isolation, and direct mode
  streams as xbind.

**What it does not take:** D113's resident agent and `Spec.AgentFD`. A
confined run is one process tree whose stdio pipes are already xbind-owned,
so there is no second process to reach. It is not a tile sandbox, and not in
the registry.

### 10.7 Coordination with D113 and the sandbox-managers work

**Shared pieces.** Both projects need the same pieces, built once by
whichever project lands first and reviewed by the other:
- the nested cgroup layout and manager API (§10.3);
- `sbx.Entry.Deployment` and `Failure.Deployment` (§10.1);
- the `Reserve` option (§10.2);
- the no-follow nested mount points in the sandbox init (§10.4).

**D113's leaves.** Its planned per-sandbox leaves (`sbx-*`,
`plans/tile-sandboxes.md:80-81`, `:242-243`) become
`tile-<CompKey>/d-<dep>/sbx-<name>`.
- A tile's first tile sandbox gives it the nested layout: its sandboxes at
  once, its backend at its next generation.
- D113's "nested per-tile cgroup later" (`:229`, `:290`) is this layout.

**Once D113 is built, tile sandboxes belong to the deployment**
([05-model.md](05-model.md) §12):
- `{"source":true}` (`plans/tile-sandboxes.md:69`) mounts `CodeRoot(tile, dep)`,
  the resolver of §4.1;
- resource mounts use the remap of §10.4;
- `main` keeps `data/sandboxes.json` and `.xbin/sbx/<CompKey>/` (`:56`,
  `:60`), and other deployments keep theirs beside their record (like dormant
  registrations), with state under `.xbin/deploy/<TileKey>/d/<name>/sbx/`;
- `cap:sandboxes` and the per-tile caps stay the tile's, and the admission
  sum counts every deployment's sandboxes and backend leaves.

**What D113 must not assume: one backend per tile.** Its streams resume
"across blue/green" by cursor. With deployments, a cursor is valid only within
the deployment that created the exec.

**The sandbox-managers work** (on another branch) gives the agent tile its
sandbox managers through providers it reaches with a custom role
(`consumer`). A custom role has no path to `reader`, so under P3 and P23 a
non-primary deployment of the agent tile is blocked from them, and it cannot
run agents in sandboxes. llm-gw guards completions with `writer`, which the
read clamp removes, so any LLM-using tile's non-primary deployments can list
models but can't run a turn (05-model §7). The runtime adds nothing for
either: the refusals come from 09-fabric's resolver, and the deployments
panel counts them per edge.

## 11. alwaysOn, the reaper and lifecycle fan-out

**Today:**
- `isAlwaysOn(comp)` reads the registry's manifest
  (`internal/runner/alwayson.go:38-41`);
- the backoff maps are keyed by path (`:30-36`);
- the reaper's exemption uses that read (`internal/runner/runner.go:819`).

**The changes:**
- **Effective alwaysOn** of `(tile, dep)`:
  - for the primary: the composed component's `alwaysOn` (§5.1), which is
    its own code's;
  - for a non-primary deployment: its view's `alwaysOn` **and** its
    per-deployment `alwaysOn` switch ([05-model.md](05-model.md) §5). It is
    never implied.
- **Waking.** `WakeAlwaysOn` walks the components and, for each, the
  deployments with effective alwaysOn, calling `aoStart(c, dep)`. The limit
  of 2 concurrent starts (`alwayson.go:23-28`) is shared across deployments,
  so a boot doesn't storm.
- **Backoff maps** (`backoff`, `upSince`, `pending`) are keyed by `stateKey`.
  `afterExit` re-checks the *effective* flag (`alwayson.go:126`).
- **The reaper** reaps any state whose effective alwaysOn is false, with
  today's 30-minute idle rule for every deployment. A non-primary backend is
  lazy and reapable by default.
- **Lifecycle is the tile's** ([05-model.md](05-model.md) §11):
  - `Stop(tile)` stops every deployment, and `ShouldRun`'s lifecycle half
    applies to all of them;
  - disabled means `409` for bare **and** qualified `/api/` URLs
    (`internal/proxy/proxy.go:148-158`); public ingress is primary-only
    anyway (`internal/proxy/ingress.go:37-40`);
  - enabling starts the primary as today (alwaysOn), and non-primary
    deployments on demand.
- **Sealing.** `SealResources` (`internal/broker/resenc_wire.go:150-162`)
  stops every deployment that uses file resources, in any namespace.

## 12. Non-isolated workspaces and VM backends

**Without `--isolate`** there is no mount namespace, so no way to show a
checkpoint at `c.Dir` or a data namespace at the canonical paths. Backends run
as xbind in `c.Dir` (`internal/runner/runner.go:453-469`). Per P18 and D78,
non-isolated mode is unsupported for pinned or non-primary backends:

| Tile | Pause live reload | Non-primary deployments |
|---|---|---|
| Static-only | Allowed. The static plane serves the checkpoint. Confined git runs directly, hardened, as every confined run does without isolation. | Allowed: no process and no data paths. Qualified static URLs work. |
| go / node / python backend | **Refused** by the deployments API, with the reason ("pinned backends need --isolate"). | **Refused.** |

**Backstops and consequences:**
- `buildAndStart` refuses to start any code other than the work tree without
  isolation, reporting a build failure (§8.5). An operator who restarts xbind
  without `--isolate` over existing records gets honest failures, never the
  work tree run silently. The static plane keeps serving pinned frontends.
- Non-isolated stream dials go to `127.0.0.1:<port>`
  (`internal/runner/ingress.go:53-56`), where two deployments would collide
  on a fixed port. Refusing non-primary backends removes that case. Under
  isolation, a host-sharing tile's non-primary deployments get no host
  networking (§10.4), so they never bind a host port either.
- A `vm` tile without isolation runs as a host process today
  ([research/side-findings.md](research/side-findings.md) #9). Under the
  refusal above, a pinned or non-primary `vm` tile is refused too, never run
  silently on the host.

**VM backends (D89, D90):**
- One guest per generation per deployment, each reservation charged to the
  tile with the headroom rule (§10.2).
- `stopFirst` applies per deployment. Two deployments never share file
  resources, because their namespaces differ.
- The code root is exported over the VM file server at `c.Dir`
  (`internal/vm/manager.go:339-371`).
- Everything a VM refuses today still fails closed with its reason: host
  networking, splices, links, `setup` (`internal/vm/manager.go:245-252`;
  `internal/runner/vm.go:59-72`).

## 13. Performance

### 13.1 The default path is unchanged (P8)

For a tile without a record:

| Hot path | Added work |
|---|---|
| A save | `liveTargets`: one in-memory lookup per changed component. No checkpoint, no confined run, no file I/O. |
| A rescan | One `PinnedPrimary` map miss per directory. |
| `/c/` and `/api/` requests | `strings.IndexByte(path, '+')`; without `+`, today's `Resolve`. `CodeRoot` and `CodeFor` are map misses returning the work tree, so today's openers and builds run. |
| A start | The leaf chooser (flat), `sockDir` (`CompKey`), `CodeFor` (work tree). The spawn spec is identical, pinned by the golden test (§1.4). Primary builds bypass the non-primary build limiter (§10.3). |
| Boot | Nothing for tiles without records. Records load with one read of the `data/deployments` directory. |

**What pins it** ([15-test-plan.md](15-test-plan.md)):
- the golden spawn-spec and event test;
- `TestLegacyInjectionUnchanged` and the legacy boot fixture;
- a benchmark of `watchLoop` over a 1 000-path batch with no records, within
  5% of today;
- a latency check of `plans/dev-flow.md`'s save-to-reload budgets.

### 13.2 Targets for the new operations

p50 on a developer box with kernel overlay (fuse-overlayfs adds about 30 ms
per confined run). The reference tile has at most 2 000 files and 20 MB, and
Go builds are warm-cache.

| Operation | Target | Confined runs on the path |
|---|---|---|
| Pause live reload, static tile | ≤ 0.5 s | capture (1–2), materialize |
| Pause live reload, node or python | ≤ 1 s to the swap | capture (1–2), materialize |
| Pause live reload, Go | ≤ 3 s to the swap | capture (1–2), materialize, build |
| Reload now | as pausing live reload | the same |
| Promote from a pinned deployment (artifact reused) | ≤ 1.5 s to the swap (start plus health); onto a protected primary only with a protected artifact (§3.4) | none (ref update and view refresh afterwards) |
| Promote from the live reload target | as pausing live reload (fresh checkpoint and build) | capture, materialize, build |
| Roll back to a retained checkpoint | ≤ 1.5 s (tree and artifact retained, §2.8) | none |
| Resume | today's live reload build time | none |
| Drift count | ≤ 150 ms | one |

### 13.3 Big trees (`node_modules`)

Captures stay incremental through the index; only the first one hashes
everything (2–4 s for 300 MB). **Materialization dominates**: a full checkout
takes 3–6 s for 50 000 files, on every pinned change. v1 ships full
checkouts, with progress (`deployments` phase events) and §2.5's caps.

**NP-07-8, differential materialization:**
1. xbind hardlinks the tile's previous materialized tree into the new tmp dir,
   on the host. Both trees are xbind-owned and immutable, and a confined run
   could not link across two bind mounts (`EXDEV`).
2. It removes the paths `git diff-tree -r` reports changed or deleted.
3. A confined `checkout-index` of only those paths fills them in.

The target is a reload now of a 50 000-file tree with 10 changed files in
≤ 1.5 s, gated on a benchmark in its work package. The host-side link walk
must never follow a symlink (06-security C5).

## 14. Size budget and new files

Budgets on this baseline (`hack/size-budget.txt`; unlisted caps are 800 lines
for Go, 900 for JS):

| File | Lines / budget | Headroom |
|---|---|---|
| `internal/runner/runner.go` | 831 / 850 | 19 |
| `internal/auth/auth.go` | 707 / 732 | 25 |
| `internal/term/term.go` | 948 / 950 | 2 |
| `cmd/bx/main.go` | 1150 / 1150 | 0 |
| `internal/boot/boot.go` | 722, unlisted | 78 |
| `internal/server/static.go` | 650, unlisted | 150 |
| `web/bx-frame.js` | 866, unlisted | 34 |

**The M0 moves** are [13-surfaces.md](13-surfaces.md)'s (its §2): verbatim,
each in its own commit, with no behaviour change.
- `build` joins `internal/runner/build.go`.
- `sandboxable` and `sandboxCmd` (`runner.go:625-726`) move to
  `internal/runner/sandboxcmd.go`.
- `stop`, `Stop`, `StopAll`, `Status` and `reaper` (`:728-831`) move to
  `internal/runner/states.go`.

About 574 lines stay in `runner.go`, and the ratchet forces its budget down.

**Where this document's pieces go** (13-surfaces' names):

| File | Holds |
|---|---|
| `internal/runner/deploy.go` | `Code`, `sockDir`, `emit`, `EnsureDeployment`, `TrackDeployment`, `ChangedDeployment`, `ChangedTile`, `Deploy`, `InUse`, the `CodeFor` plumbing, restart-as-deploy, the leaf choice, the admission caps and the non-primary build limiter. |
| `internal/runner/build.go` | Checkpoint builds (§3.1): `DirFrom`, the nested and pinned-primary binds, `go.work`, `build.json`, the protected namespace (§3.4). |
| `internal/runner/sandboxcmd.go`, `binds.go`, `env.go`, `states.go` | §10.4's binds, remap and network wiring, §3.3, and §11's per-deployment stop, status and reaper. |
| `internal/checkpoint/` + `checkpoint_linux_test.go` | All of §2, through `confine`: capture, the git view, admission, materialization, the view repository, drift, dry runs, GC. The integration test plants a hostile `.git/config`, an embedded repository, argv-shaped file names and a symlink in the quarantine, and asserts admission, the caps, containment and that the view repository advertises no `refs/xbin/*`. Add the package to the `integration` target (`Makefile:110-117`). |
| `internal/deployments/` | The plane: record, queue, journal (§8.4), reviewed operations and the manager gate (§8.3), the `PinnedPrimary` preparation (§5.1) and the per-deployment provisioning calls (§5.4). |
| `internal/registry/qualifier.go`, `deployview.go` | §4.3 and §5.1's views. `registry.go` gains the `PinnedPrimary` hook, the composition in `Rescan`, and the `WorkTree` and `Kept` fields. |
| `internal/boot/liveroute.go` + `liveroute_test.go` | `liveTargets` (§6). `serve.go` changes by about 8 lines. |
| `internal/server/deployserve.go` | `CodeRoot`, the pinned opener and the `deps/` re-dispatch (§4.1). `static.go`, `tileassets.go` and `native.go` change by about 10 lines each; `api.go` by one (`roles`, §5.1). |
| `internal/auth/deployment.go` | `RegisterInstance(token, path, dep)` and the claim-aware minting helpers; the old signatures stay as `main` wrappers. |
| `internal/cgroup/cgroup_linux.go`, `cgroup_other.go` | §10.3's nested names, parents, `AddWith` and `ProcsTree`. |
| `internal/confine/confine.go` | `DirFrom`, `At`, `ErrNeedsIsolation` (§10.5); the M3 writers (§10.6). |
| `internal/sandbox/init_linux.go` | §10.4's no-follow nested mount points and the deferred read-only remount. |
| `internal/fsutil/beneath.go`, `beneath_linux.go` | `OpenNoFollow` (§2.7). |

**Small edits:**
- `internal/util/util.go`: `DeploymentNameOK`, `TileKey`;
- `internal/sbx/sbx.go`: about 8 lines (§10.1);
- `internal/vm/policy.go`: about 10 (§10.2);
- `internal/proxy/proxy.go`: about 15;
- `internal/broker/broker.go`: diskmon's per-tile quota walk (§2.5);
- `internal/auth/auth.go`: one `Principal` field.

**Guards.** `TestNoDirectExec` keeps covering every new daemon file. The
no-following host-walk guard of 06-security C5 (15-test-plan) covers
`internal/checkpoint`, `internal/deployments` and
`internal/server/deployserve.go`: host code there reaches tile-derived paths
only through `fsutil` (`OpenBeneath`, `OpenNoFollow`, and `readlinkat` on a
directory opened beneath the tree, §4.1), and removes trees only with
`os.RemoveAll`.

`term.go` and `cmd/bx/main.go` must not grow: terminal targets (10-ux) and
the `bx` verbs (11-contract) go into new files through the `moreCmds` seam,
`cmd/bx/livereload.go`, `deploy.go`, `deployment.go` and `deployclient.go`.

## Divergences from the model

1. Resolved by 05-model §6 (a pinned deployment's env comes from the `uses`
   union).
2. Resolved by 05-model §6 (`deps/` links resolve through the other tile's
   `/c/` plane and are never followed on disk; §4.1).
3. Resolved by 05-model §4–§5 and P9 (after a failed move off the work tree
   the pointer is the attempted checkpoint, state `failed`).
4. Resolved by 05-model §5 (reassignment restarts both primaries, because
   wiring is captured at spawn; §8.8).
5. Resolved by 05-model §12 (tile → deployment → {`backend`, `sbx-*`}, with
   zero-state and main-only tiles kept flat).
6. Resolved by 05-model §7 (legacy-mode subresources are authorized by path;
   documents and `/api/` are write-gated in every mode).
7. Resolved by 05-model §5 (pausing live reload on a Go tile is a warm-cache
   rebuild).
8. Resolved by 05-model §7 (inbound-edge callers pass the primary; `Changed`
   and alwaysOn pass the deployment they act on).
9. Resolved by rule C2 (05-model §8): non-primary activity rides only the
   `deployments` type.

## New proposals

- **NP-07-1 — The capture pipeline.**
  - A checkpoint is its tree hash, shown as `c:` plus 7 hex and stored in
    full.
  - A capture is two confined runs around an admission check done in xbind:
    run 1 captures into a quarantine, computes the git view tree under a
    scratch index, and prints the tree listing; xbind checks the caps and
    tree hygiene on that listing, without reading blobs; run 2 migrates the
    quarantine inside the sandbox and records the retention and view commits.
  - A refused capture discards the quarantine and the persistent index, so the
    store never holds refused objects.
  - The store's refs and the deploy-log chain are
    [11-contract.md](11-contract.md) §10.3's (§2.2, §2.3).
- **NP-07-2 — A restart is a deploy of the current checkpoint.** It is a
  no-op when the generation is healthy, a new generation from the same
  artifact otherwise, and a reviewed operation onto a protected primary
  (§8.7). The pointer rule it came with is resolved by 05-model §4–§5.
- **NP-07-3** — resolved by 05-model §6.
- **NP-07-4** — resolved by 05-model §6.
- **NP-07-5 — The cgroup weights.** Tile node 100; inside it, the primary 100
  and non-primary deployments 50; no node-level memory caps in v1 (§10.3).
  The layout itself is resolved by 05-model §12.
- **NP-07-6** — resolved by P25 (05-model §12).
- **NP-07-7 — The numbers.**
  - Per checkpoint: at most 200 000 entries, 2 GiB, a 256 MiB largest file,
    1 024-byte paths, depth 64, and 5 minutes per capture or
    materialization.
  - Captures per tile: a burst of 10, then one every 3 s.
  - A 10 GiB per-tile quota for the store, the view repository, materialized
    trees and per-checkpoint artifacts, counted by diskmon, alerting at 90%.
  - Non-primary deployments: at most 3 per tile and 24 per workspace, with at
    most 12 running, and at most `max(1, NumCPU/4)` non-primary builds at
    once; primary builds never wait for them.
  - A deploy queue of 8.
  - Retention: 20 roll-back targets, 24 h, and 50 log entries per deployment;
    materialized trees current plus previous; artifacts current plus 3 (§2.5,
    §2.8, §10.3).
- **NP-07-8 — Differential materialization.** Host hardlinks from the previous
  tree, plus a confined checkout of only the changed paths. It is gated on a
  benchmark, with a target of 1.5 s for a 50 000-file reload now (§13.3).
- **NP-07-9 — Several hardened git commands per confined run.** A capture's
  git commands share one sandbox, each carrying 06-security T1's flags, with
  tile strings only in files. That saves about 45 ms per extra command (§2.2).
- **NP-07-10** — resolved by 05-model §12 (`DirFrom`; a direct run refuses).
- **NP-07-11 — The registry's component is the primary's.** One
  `PinnedPrimary` hook lets `Rescan` compose the inbound surface and the
  deployment-level fields from the primary's checkpoint, keeping the work
  tree's scan as `Component.WorkTree`, so every inbound reader follows the
  primary without an edit, and a boot that can't prepare a pinned primary
  fails closed (§5.1). It subsumes 13-surfaces' `Keep`; 13-surfaces'
  registry and deployview rows follow.
- **NP-07-12 — A tile-rooted scope's `importMap` follows the primary's code**,
  like its `resources` (§5.1). 05-model §6 classifies only `resources`, but
  the import map shapes the documents the primary serves, as `inject` does.
- **NP-07-13 — The deploy journal.** `data/deployments/<TileKey>/pending.json`
  records every accepted request before the API answers, and boot reconciles
  what was in flight into the deploy log as `cancelled`, `ok` or
  `interrupted` (§8.4). 11-contract §1.10's "lost with an xbind restart"
  follows.
- **NP-07-14 — A protected primary's lost build products hold its start.**
  After `.xbin/` loss, a Go artifact is rebuilt only with identical inputs,
  and an env layer never on a restart path; otherwise the primary stays down,
  with a manager alert, until a manager redeploys (§3.4).
- **NP-07-15 — No checkpoint exclude list in v1.** 04-options NP-04-4 is not
  adopted: excluded paths would silently vanish from pinned code. The cap
  error points large files to a resource (§2.5). 04-options and
  16-open-questions mark NP-04-4 rejected.
