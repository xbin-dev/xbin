# 04 — The tradeoff surface

> Status: historical (the input to D119 and D127) — every design axis of the tile dev lifecycle, its real options, how each option scores, and which choice the model makes and why (part of [plans/dev-lifecycle](README.md))

This document argues each choice in [05-model.md](05-model.md) against the
alternatives. It does not restate the model. Every recommendation names the
invariant (P1–P29, [05-model.md §13](05-model.md#13-invariants-the-proposed-decisions))
that carries it. Where this document and the model differ in detail, the
model is normative; [Divergences from the model](#divergences-from-the-model)
records the ones found and how they were settled. Terms are
[01-glossary.md](01-glossary.md)'s, used verbatim.

Evidence:
- Facts about today carry `file:line`, re-verified against this worktree. The
  baseline is master: D112 is landed, and D113 is designed
  ([plans/tile-sandboxes.md](../tile-sandboxes.md)). Some line numbers differ
  from the research maps, which were taken earlier.
- Decision IDs resolve in [../DECISIONS.md](../DECISIONS.md); IFACE-7 is in
  [../interfaces.md](../interfaces.md).
- Prior art names platforms from [research/prior-art.md](research/prior-art.md).
  The codebase maps behind the rest are
  [research/git-confine.md](research/git-confine.md),
  [research/serving-fabric.md](research/serving-fabric.md),
  [research/data-plane.md](research/data-plane.md),
  [research/inbound-edges.md](research/inbound-edges.md),
  [research/builder-contract.md](research/builder-contract.md),
  [research/runner-hot-reload.md](research/runner-hot-reload.md),
  [research/terminology-census.md](research/terminology-census.md) and
  [research/sandbox-visibility.md](research/sandbox-visibility.md).

## How to read an axis

Each axis has five parts:
- **Question:** the decision to make.
- **Options:** two to five real options, each stated at its strongest. The
  owner's floated variants are among them.
- **Assessment:** a table scoring each option on UX, security, compat, cost
  (implementation cost and complexity) and prior art.
- **Recommendation:** the choice, and the P-number that carries it. Where this
  document needs a decision the model doesn't make, it adds a proposal labelled
  NP-04-n (listed in [New proposals](#new-proposals)).
- **Status:**
  - *ratified*: P1–P4, ratified by the owner on 2026-09-27;
  - *owner-confirmed*: P7, P18, P19 and P22–P24, and the owner's answers of
    the same day;
  - *proposed*: every other P-number, and every open NP-04-n.

## Constraints every option is judged against

- **C1 — Zero change for tiles that don't opt in (P5).**
  - Boot must never rewrite the root `xbin.json`
    (`test/legacy_workspace_test.go:70-71`).
  - The HTTP API is additive (compat rule 2, `docs/compat.md:24`), as is the
    manifest (rule 7, `docs/compat.md:51`). The SDK changes only
    permissively (rule 8, `docs/compat.md:54`). The rules are in
    [/docs/compat.md](/docs/compat.md).
- **C2 — The tile's own repository is untrusted.**
  - Every terminal binds the tile directory read-write, `.git` included
    (`internal/term/binds.go:57-58`).
  - Every git run xbind makes goes through confine (D78,
    `internal/broker/code.go:64-80`).
- **C3 — The path is the principal key.** Grants, the http-binding role and
  same-scope auto-grants all look up the caller's path
  (`internal/broker/broker.go:418-452`), and so do owner-derived policy
  ceilings (`internal/users/orgs.go:1210-1215`). A deployment at a new path is
  a new principal.
- **C4 — One work tree per tile.**
  - Terminals and agent sessions mount exactly one tile directory read-write
    (`internal/term/binds.go:57-58`; agents reuse the same shell sandbox,
    `internal/term/agent.go:248`).
  - The watcher skips dot directories (`internal/watch/watch.go:61-74`).
  - The generated `go.work` lists canonical tile directories
    (`internal/deps/deps.go:120-146`).
- **C5 — Every restart path rebuilds from the work tree today:**
  - `Ensure` rebuilds whenever the tile is dirty
    (`internal/runner/runner.go:190-215`), and `Changed` marks it dirty
    (`:265-285`).
  - The idle reaper marks a reaped tile dirty (`:809-825`).
  - Grant changes restart it (`internal/boot/boot.go:508-513`).
  - `/c/` serves the work tree with `no-store`
    (`internal/server/static.go:153`).
- **C6 — D4's single HTML transform.** Routing happens at the URL or the
  credential, never by rewriting documents.
- **C7 — The owner's direction.** Deployments are not built on D113's
  tile-managed sandboxes. The dev lifecycle changes the shared sandbox
  mechanics one level above them ([05-model.md §12](05-model.md#12-isolation-and-runtimes)).

## A1 — The model: what a deployment is made of

**Question.** What object records which code runs where, and how does code
move between deployments?

**Options.**
- **0 — A twin tile (clone plus a code PR), no new runtime.**
  - `POST /clone` copies the tile directory, `.git` included, to a new path
    and registers an independent tile. It rewrites the old path textually,
    copies no data and no vault, and sends cross-scope `uses` back through
    owner approval (`internal/broker/clone.go:18-26`, `:207-217`).
  - Work happens in the twin. A D48 code PR carries it back, the original's
    terminal applies it with `git am` into the work tree, and live reload
    ships it.
- **A — Pointers over a checkpoint store.**
  - A deployment is a named pointer: to live reload (the work tree) or to a
    checkpoint.
  - Checkpoints are immutable, content-addressed captures in a store that
    xbind owns under `data/`. Feeds produce them.
- **B — A deployment is a git branch, and live reload follows branches.** This
  is the owner's alternative. It has two readings:
  - **B1:** each deployment runs the tip of a named branch of the tile's
    repository, and a new commit on that branch deploys.
  - **B2:** the deployment whose branch is checked out in the work tree is
    the live reload target, and the others run their tips.

  B is gated on "a repository in the tile root", and that gate already holds.
  Every component gets its own repository at boot and on every structure
  change (`internal/broker/code.go:96-119`, `internal/boot/boot.go:416-418`).
  Only a hand-made directory waits until the next `EnsureComponentRepos`
  ([research/git-confine.md](research/git-confine.md) §1).
- **C — A blessed, runtime-injected remote with push-to-deploy.**
  - xbind serves a git remote per tile and injects it into terminals, the way
    it already does for the `template` remote
    (`internal/broker/templaterepo.go:141-151`, reached through the
    `GIT_CONFIG_COUNT` rewrite at `internal/term/term.go:789-791`).
  - Pushing `deploy/<name>` deploys that deployment.
- **D — A work tree per deployment.** Nobody floated this one, but it bounds
  the space. Each deployment gets its own editable directory with its own live
  reload.

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| 0 twin tile | Works today. Bindings and grants are made again for the twin, which clutters the workspace. Path rewrites leak into the diffs. "Promotion" is `git am` into the work tree, so it ships at once, with no pin and nothing to roll back to. | Strongest isolation: the twin is a separate principal, the only real trust boundary. Every cross-scope `uses` needs owner re-approval. | None. | None. | Fly ("a separate app"), Heroku pipelines, Wrangler's separately named Workers, and App Engine's and Firebase's advice to use separate projects. Dokku's `apps:clone` keeps the database URLs, a documented pitfall. |
| A pointers | Pausing live reload works on any tile: with or without git, committed or not. What runs is exactly what was tested, gitignored runtime files included. | The store is xbind-owned and confine-only (P16). The tile repository is never the record of what runs. | The zero state is untouched (P5). | Medium–high: the store, materialization, artifacts per checkpoint, deployment-aware routing. | Lambda `$LATEST` plus aliases, Apps Script's head plus pinned deployments, Cloud Run's LATEST, Cloudflare's immutable uploads plus a serving pointer, Netlify's locked deploys, Deno timelines, Convex. |
| B1 branch tips | Git-native, and promotion is a merge. But a commit becomes a deploy, which collides with CM-2 ("commit often and on your own initiative… never ask", `workspace-template/AGENTS.md:160-166`). Pausing live reload needs a commit, and xbind never commits (D2). Gitignored runtime files such as `node_modules` aren't at a branch tip. | Branches are writable from the tile's sandboxes: `reset`, `branch -f` or a force push rewrite what a deployment runs. Every read must be confined, and pinning needs a capture into xbind storage, which is A. | Imported repositories keep their own default branch. Nested components are separate repositories. | Medium, plus a confined poller per followed branch. | Deno timelines per branch, Vercel's branch tracking, Railway's and Amplify's trigger branches, Supabase, n8n (a server per branch). |
| B2 checkout selects | Switching deployments is `git checkout`. But agents check out branches routinely, and checking out the primary's branch turns the work tree into the primary's code at once, uncommitted changes included. | HEAD is controlled from the terminal, so checking out a protected primary's branch would bypass P21 unless the mapping were inert for it. | As B1. | Medium. | None found. n8n's guidance against pushing and pulling on the same server warns against running what is being edited. |
| C deploy remote | An explicit act: push is not commit. Agent-friendly, and a dirty tree is no problem. Non-git users are left out, and gitignored runtime files are missing unless xbind builds them. | Every receive must run confined. The remote must refuse pushes to a protected primary. | Additive routes under `/api/xbin/`. | High. There is no receive-pack in xbind. Confine buffers all output (`internal/confine/confine.go:87-89`, `:250-265`), so a push needs a confine-level streaming run ([05-model.md §12](05-model.md#12-isolation-and-runtimes)), independent of D113's unbuilt exec protocol. | Heroku (one branch deploys), Dokku (a deploy-branch filter; `apps:lock`), Piku. |
| D work tree per deployment | Every deployment can be edited at once. But agents, terminals, the watcher and `go.work` all assume one directory per tile (C4). A second tree is either an unwatched dot directory or a new path, which is option 0. | Doubles the editing surfaces per tile. | Rewrites the editing plane. | Very high. | Val Town's always-live branches, whose data semantics are undocumented; Amplify sandboxes ("only one can be running at a time"). |

**Recommendation: A**, with B and C recast as **feeds** on the same core:
- The **tracked branch** feed keeps B1's behaviour. It reads the branch inside
  confine and captures each new commit into the store, so the untrusted
  repository never becomes the record.
- The **deploy remote** feed is C's push side (M3).
- C's read side ships in v1 as the read-only checkpoint fetch remote
  `xbin-deploy` (A2), which flow H needs. Nothing is ever pushed through it.

The other options:
- **B2 is rejected.** Choosing the live reload target by checkout turns routine
  git use into a routing change.
- **D is rejected** by C4.
- **Option 0 stays as it is.** It remains the right tool when code written by
  people who can't write the tile has to run, because that needs separate
  principals (P20).

B contributes the git UX, but A is still needed underneath. The one property
B can't provide from an untrusted, mutable repository is P9: *pinned means
pinned*, through idle reap, crash, xbind restart and the loss of `.xbin/`. As
soon as xbind copies a commit's tree into storage it owns in order to
guarantee that, the design is A with a branch feed. → **P1.**

**Status:** ratified (P1).

## A2 — The checkpoint mechanism

**Question.** How is a checkpoint captured, stored and turned back into a
tree, and what does it contain?

**Options.**
- **a) A private confined git store** (the model).
  - A bare repository at `data/checkpoints/<TileKey>.git`. `<TileKey>` is a
    128-bit hash of the tile path; `CompKey` keeps only 32 bits and can be
    ground (`internal/util/util.go:137-144`), so the new stores use the
    collision-free key and existing stores keep theirs.
  - Capture is confined `git --git-dir=<store> --work-tree=<tile>` with a
    store-private index: `add --all --force`, minus exclude pathspecs, then
    `write-tree`. The tile is bound read-only. The checkpoint is named by its
    tree hash, so identical content is one checkpoint. `07-runtime.md` §2.2
    fixes the pipeline (two confined runs, with an admission check on the
    listing).
  - This is D77's private-git-dir pattern (`internal/term/agentdiff.go:91-124`;
    `add -A` and `write-tree` at `:273-279`), run with confine's hardened
    flags and env vars (`internal/confine/git.go:14-34`).
  - Checkpoints are materialized under
    `.xbin/deploy/<TileKey>/<full tree hash>/`.
- **b) Tar or copy.** A full copy of the tree per checkpoint: `rsync -aHAX` or
  `cp -a` inside confine, or an in-process Go copier.
- **c) Reflinks.** `cp --reflink` on btrfs or XFS: the filesystem shares
  blocks until either side changes.

**Fidelity by mechanism.**

| Property | a) git store | b) tar/copy | c) reflink |
|---|---|---|---|
| file contents, exec bit, symlinks (as links) | kept | kept | kept |
| other permission bits, ownership | lost | kept | kept |
| empty directories | lost | kept | kept |
| xattrs | lost | kept with `-X` | kept |
| FIFOs, devices, sockets | lost | optional | kept |
| hard links | two files (one stored blob) | kept with `-H` | kept |
| mtimes | lost (materialization time) | kept | kept |

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| a) git store | Content-addressed ids (`c:3f2a1c9`) and deduplication for free. `diff-tree` gives the promote dialog its diff. The store also keeps each checkpoint's git view, which a separate view repository serves as flow H's read-only fetch remote (below), refreshed by a confined `update-server-info` as template repositories are (`internal/broker/templaterepo.go:93`). The fidelity limits must be documented, and the glossary does. | Confined runs only (D78), with hooks and fsmonitor off. The store sits in `data/`, which every terminal masks (`internal/term/binds.go:49-53`), and is never served itself. | Internal; the `data/` layout isn't promised (`docs/compat.md:141`). | Medium. Each confined run costs about 45 ms (fuse-overlayfs) or 17 ms (kernel overlay) per D78, plus hashing the changed files. Keeping the index in the store makes repeat captures cheap. | Lambda, Apps Script and Cloud Run all keep immutable, content-fixed captures behind their pointers; Val Town built its own VCS. |
| b) tar/copy | Full fidelity. But no ids, no diff and no fetchable remote. | An in-process copier reads sandbox-writable files and races a symlink put in a file's place; the backup writer has exactly this race (`internal/backup/backup.go:124-130`). Inside confine it is safe but slow. | Same as a. | Storage grows with every checkpoint, since there is no deduplication without a hardlink farm. A content-addressed file store would reinvent git. | Heroku slugs, Fly images, Netlify atomic deploys. All of these store built artifacts, not the tree that produced them. |
| c) reflink | Instant, full fidelity. | Same race as b unless run in confine. | Depends on the filesystem: ext4 and overlay have no reflinks, so a or b is needed as a fallback anyway. | Low where it works, but two code paths overall. | Neon's copy-on-write branches (at the storage level). |

**What a checkpoint contains.**

| Content | Rule | Why |
|---|---|---|
| gitignored files | included: `add --force`, and the store's own exclude files are empty | A pinned deployment must run what the live reload target ran. `node_modules`, generated assets and local config files are usually gitignored, and node and python backends load them from the tile directory (`internal/runner/runner.go:395-405`). |
| `node_modules` | included | A runtime dependency. Backups skip it as reproducible (`internal/broker/backup.go:117-118`), but reinstalling at deploy time would be a network build, not a pin. Deduplication limits the cost to changed files. |
| `.git` directories | excluded | History isn't code, and the repository is untrusted. |
| nested components | excluded, by exclude pathspecs taken from the registry and from any directory holding a `.git`, so no gitlink entries appear | Each is a tile of its own (`internal/registry/registry.go:469-471`), with its own deployments or its own zero state. Inside a pinned backend's sandbox, its own code is bound back after the checkpoint bind (A13). |
| `deps/` symlinks | included, as links, and never followed on disk (P16) | xbind regenerates them as relative links (`internal/deps/deps.go:59-69`); today's legacy plane follows them out of the tile (`internal/server/static.go:193-195`). With a checkpoint: the static plane resolves a `deps/<name>/…` path by re-dispatching it to that tile's `/c/` plane (its primary); any other link that leaves the checkpoint answers 404 (`ErrEscapes`, `internal/fsutil/beneath.go:11`); confined Go builds get the other tile's pinned primary checkpoint (or its work tree, while that primary follows it) bound over its `go.work` directory ([05-model.md §6](05-model.md#6-what-code-and-which-manifest-a-deployment-runs)). |
| dot files and dot directories | included | The watcher ignores them (`internal/watch/watch.go:61-74`), but backends read them, `.env`-style configuration for example. |
| size | capped per checkpoint (entries, bytes, largest file, path length, depth, time) and per store | Over a cap, the capture is refused with the largest directories named; it is never truncated. `07-runtime.md` §2.5 sets the numbers. A per-tile exclude list, if a tile needs one, lives in the deployment record, never in a work-tree file. |
| the git view | the checkpoint minus the paths the tile's own ignore rules exclude, evaluated in confine at capture and kept in the store beside its checkpoint (never advertised) | Only the fetch remote serves it. A branch made from the full checkpoint would commit `node_modules`, `.env` and build output into the tile's history, and the next branch switch would delete them from the work tree (flow H). |

A capture reads a tree that may still be changing, so a file caught mid-write
can be captured torn. Live reload has the same race today. A capture must
therefore start only after the watcher's 300 ms quiet period
(`internal/boot/boot.go:40-41`).

**The read-only fetch remote** (`xbin-deploy`, flow H):
- It is served from a separate, xbind-owned **view repository** per tile
  (`data/checkpoints/<TileKey>.view.git`). That repository holds only
  `refs/heads/deploy/<name>` for each pinned deployment, pointing at its
  checkpoint's git view, plus `HEAD` naming the primary's ref (dangling while
  the primary follows the work tree). It holds no other objects, so no
  `refs/xbin/*` ref and no store object is ever fetchable. A confined
  `update-server-info` refreshes it after each change to its refs. The refspec
  is `+refs/heads/deploy/*:refs/deploy/*`, so `deploy/<name>` resolves after
  `git fetch xbin-deploy`.
- It serves an allow-list of dumb-HTTP files (`HEAD`, `info/refs`,
  `objects/info/packs`, loose objects, packs), opened beneath the view
  repository with every symlink refused. It never goes through the `template`
  remote's `http.ServeFile` (`internal/broker/templaterepo.go:226`).
- Only the tile's own terminal and agent sessions, and humans with at least
  `write` on the tile (their current level, checked per request), may fetch.
- It is injected per session through `GIT_CONFIG_*` env entries beside
  today's rewrite (`internal/term/term.go:789-791`), and only while the tile
  has a deployment record. It is never written into the tile's `.git/config`:
  clone copies `.git`, and opting out or downgrading must leave nothing
  behind.

**Recommendation: a).**
- Checkpoints use the private confined git store.
- The fidelity limits are part of the checkpoint's definition.
- Materialized trees use directory mode 0755 and file modes 0444/0555 (the
  exec bit kept), so `rm -rf .xbin` keeps working. They are read-only to
  sandboxes by bind flags, not by host modes. Host-side code never follows
  links or walks inside them, which 06-security's rule C5 states and a guard
  test enforces (`15-test-plan.md`).
- Built artifacts are kept per checkpoint (P9).
- Reflinks may later speed up materialization, never capture.

→ **P1** (the store), **P9**, **P16**.

**Status:** P1 is ratified (an xbind-owned checkpoint store). The mechanism
and the inclusion rules are proposed (P16).

## A3 — Pausing live reload

**Question.** What does pausing live reload hold still? When are checkpoints
taken? What do reload now and resume do?

### A3.1 Mechanism

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| a) Watcher flag: skip `reload` and `run.Changed` for the tile in `watchLoop` (`internal/boot/serve.go:178-180`) | Live reload looks paused until the first restart or page load. Every restart path rebuilds from the work tree (C5). node and python run straight from the tile directory (`internal/runner/runner.go:395-405`, `:454-462`). The next navigation serves the unfinished files. | Unfinished code reaches the primary silently. | — | Very low. | n8n 2.0 is the cautionary tale: its webhook path ran drafts, bypassing the pin. Every entry point must resolve through one "which code runs" pointer. |
| **b) Pin to a checkpoint (the model):** pausing live reload checkpoints the work tree and deploys it, and every restart runs that checkpoint | Frontend and backend both hold still. | Nothing reaches the primary without an explicit act. | The zero state is untouched. | Medium: a checkpoint and a materialization, and for Go a warm-cache rebuild keyed by checkpoint. Backends need isolation (A13). | Netlify's locked deploys, Deno's timeline locking, Cloud Run's `--no-traffic`, Vercel with auto-assign off. |
| c) Refuse edits while live reload is paused (Dokku `apps:lock`) | Defeats the purpose: the developer pauses live reload precisely to keep editing. | — | — | Low. | Dokku `apps:lock`, which rejects pushes. |

Pausing live reload is a file-identical swap only within the checkpoint's
fidelity limits (A2): a backend that reads its own `.git`, or expects an empty
directory, sees a different tree once live reload is paused. A Go backend
needs a warm-cache rebuild, because the watcher ignores directories a
checkpoint includes. If that build fails, live reload stays detached, and the
deployment is recorded pinned to the attempted checkpoint (state `failed`).
It keeps serving its current generation, and every restart runs the
attempted checkpoint, never the work tree
([05-model.md §5](05-model.md#5-operations)).

### A3.2 When checkpoints are taken

| Option | UX | Cost | Prior art |
|---|---|---|---|
| **a) On demand (the model):** when live reload is paused, and at reload now, deploy, promote and add deployment | Build errors appear only when reload now runs. The terminal bar can still show "N files changed since c:…", via a confined capture and diff on each debounced batch, for tiles whose live reload is paused. | Lowest. No per-save cost on the default path (P8). | Netlify's manual deploy activation, Piku with `PIKU_AUTO_RESTART=false`. |
| b) A checkpoint on every save while live reload is paused | A checkpoint for every save. Little extra value, since the deploy log already records what ran. | One confined run per batch. | Val Town (every change kept), Retool's history. |
| c) As b, plus a candidate build that isn't activated | Reload now activates something already built. Build errors show in the terminal window before anyone presses it. But it contradicts the glossary's "paused: saves change the work tree and nothing else", and adds activation-by-hash state. | One build per save: what live reload costs today. | Netlify (builds continue while locked, ready to activate later); Deno (pushes build but don't activate). |
| d) A checkpoint on every save, on every tile with deployments, the live reload target included | — | Breaks P8. | — |

Dry runs capture nothing. A dry run of pause live reload or add deployment
computes the impact without capturing, so on a zero-state tile it never
creates a checkpoint store; the store exists only after a committed opt-in
(A11).

### A3.3 What resume does

| Option | UX | Prior art |
|---|---|---|
| **a) Resuming onto X deploys the work tree at once (the model)** | Keeps P8's invariant that the live reload target runs the work tree. The control must say what will go live ("resuming deploys N changed files"). | Cloud Run's `--to-latest`; Deno's unlock (the newest build becomes active). |
| b) Re-attach, but keep the pinned checkpoint until the next save | Creates a state in which the live reload target runs code that isn't the work tree, which is exactly the confusion to avoid. | Netlify's unlock (earlier builds are not activated). |
| c) Resume to a chosen checkpoint, then follow | Two operations in one control (roll back, then resume). The deploy log already gives roll back. | Vercel's "Undo Rollback". |

Rules the prior art settles, all kept by the model:
- **A roll back pauses live reload.** So does a deploy or promotion onto the
  live reload target. Otherwise the next save undoes it (Vercel turns
  auto-assign off after an Instant Rollback).
- **Paused live reload is sticky, and visible to every viewer.** It is tile
  state, not a per-user setting. Cloud Run's silent sticky pin is the
  documented surprise to avoid.

**Recommendation:**
- the pin mechanism (A3.1 b);
- checkpoints on demand (A3.2 a). Candidate builds (c) are rejected for v1,
  and revisited only if [02-goals.md](02-goals.md)'s SC-LATENCY-OPS fails with
  warm caches and differential materialization;
- resume deploys the work tree (A3.3 a).

→ **P8, P9**, and the glossary's definitions of pause live reload, reload now
and resume.

**Status:** proposed.

## A4 — Storage keys: the deployment or the role

**Question.** Which deployment owns today's storage keys, and does data move
when the primary role moves?

**Options.**
- **a) Storage follows the deployment** (the model). `main` owns today's keys
  forever, and other deployments get namespaces at a dot level. Reassigning
  the primary moves routing only.
- **b) Storage follows the role.** The primary always owns today's keys, so
  reassigning the primary exchanges namespaces.
- **c) Everyone is namespaced.** At opt-in, `main`'s data moves into a `main`
  namespace.
- **d) Opaque storage ids.** Each deployment gets a stable internal id, so its
  name could be renamed.

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **a) follows the deployment** | "Primary" and `main` can diverge, so the UI must say whose data the primary serves (flow F). | There is no re-encryption step to get wrong. | No migration. The files builder docs name, such as `.xbin/log/<compkey>.log`, stay where they are for `main`. | Low: one key function routes every call site. | Neon (the default branch can be reassigned; data stays with each branch), Convex, Heroku (add-ons stay with their app), Deno Deploy's late binding per timeline. |
| b) follows the role | Intuitive: the primary always holds the real data. | Reassignment must rename directories, re-encrypt kv under its `kv:<bucket>` label (`internal/broker/resenc_wire.go:202-223`) and re-wrap every gocryptfs volume, whose key derives from `resLabel` (`:43`). None of that is atomic, and a crash midway splits the data. | Fine. | High. | Azure moves non-sticky connection settings along with the code, a documented trap. Deno Classic's bug served the wrong KV from the primary domain. |
| c) everyone namespaced | — | — | Migrates every opted-in tile's data, the opposite of "enabling deployments never migrates". | High. | Replit's shared-to-separate database migration left old remixes needing manual work. |
| d) opaque ids | Names could be renamed. | — | — | An indirection table to back up and restore. Names are immutable for the reason user ids are (D15). | — |

**Recommendation: a).**
- Non-primary namespaces sit at a dot level that no scope key can produce.
  `util.ScopeKey` isn't injective (`internal/util/util.go:127-132`), but no
  component path can start with a dot (`internal/util/util.go:97-114`).
  Encrypted mounts stay under `.xbin/resenc/` for the installer's AppArmor
  rule (`deploy/install.sh:909`). The exact scheme is `08-data.md`'s.
- A non-`main` deployment's backend log is
  `.xbin/deploy/<TileKey>/d/<name>/backend.log`; `main` keeps today's.
- Reassigning the primary moves routing only. It is M2, a tile-manager act
  with a loud confirmation that names whose data the new primary serves. In
  v1 the tile must be in the workspace scope or be the only member of the
  scope it roots, because reassigning one member would split the scope's
  primary data (P28).

→ **P6, P7.**

**Status:** P7 owner-confirmed; P6 and P28 proposed.

## A5 — The outbound fabric (F0–F4)

**Question.** When a non-primary deployment uses one of its tile's edges, what
does the call reach, and with which role?

Today:
- Every outbound call is authorized by the tile's grants: explicit rows, then
  the http-binding role, then same-scope `uses`
  (`internal/broker/broker.go:418-452`).
- The proxy turns the result into one role, strips every inbound `X-XBin-*`
  header, sets `X-XBin-Role` (`internal/proxy/proxy.go:160-173`,
  `:275-279`), and reaches the provider through `Runner.Ensure`
  (`:182`).
- So a marker xbind sets itself is trustworthy, and today the callee can't
  tell deployments apart.

**Options** (the owner's scale):
- **F0 — none.** Non-primary deployments get no outbound edges.
- **F1 — the providers' primaries, with full grants.**
- **F2 — the providers' primaries, read-clamped, with a per-edge `block`.**
  This is the owner's choice.
- **F3 — name matching.** The call reaches the provider's deployment of the
  same name (edge policy `match`).
- **F4 — per-binding override.** Each edge picks its own target and role.

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| F0 none | Most tiles can't be exercised: there's no llm and no calendar, and slot env is missing or dead. | Safest. | Fine. | Low. | Render's switch that blocks private traffic between its named sets. |
| F1 full primaries | Everything works. | Non-primary code writes other tiles' primaries with the tile's full rights. A webhooks tile would fire the agent's real triggers, and a bridge would post real replies ([research/inbound-edges.md](research/inbound-edges.md) E5 and §3 F). | Fine. | Lowest. | Cloudflare's per-branch Workers call the bound Worker's primary deployment; Firebase's and Vercel's per-branch URLs use the real backend. Cloudflare now advises against testing on URLs that run with the primary's resources. |
| **F2 read-clamped + `block`** | Reads work (dashboards, lookups). Writes are refused with an error that names the policy. `block` is for edges that shouldn't even read. | No writes into another tile's primary through xbind-brokered resources. For HTTP APIs, the provider enforces the role it's told (below). Reading real provider data widens nothing compared with the zero state, where a terminal-level user can make the primary read and log it by saving. Under a protected primary, though, `read` edges are the one route left by which terminal-level code reads providers' data, which is why edge policies are a tile-manager setting and `block` exists. | The primary is unaffected, and the deployment marker is absent on its calls. | Medium: a clamp in one resolver, plus a per-edge record. | Retool (resource permissions set per resource configuration, e.g. read-only on the real database). No platform clamps by default; this is new. |
| F3 `match` | The best fidelity: `dev` talks to `dev` with no code change. Needs same-named deployments to exist and be seeded, and a rule for a missing name. | Safe if a missing name never gets a full-role fallback to the primary. | Adds its exception at P7's one resolver (A16). | High for L3: net splices are numbered by the sorted client index (`internal/broker/netfn.go:182-191`, `:223-249`). Moderate for HTTP. | Railway (a service's private DNS name resolves to its counterpart in the same named set), Render's `fromService`, Amplify's backend per branch, and Cloudflare's stated future work. |
| F4 per binding | The most control, with many knobs per edge. | Depends on the knob, and easy to misconfigure: Azure's per-setting stickiness is the documented trap. | — | Medium. | Render's `previewValue`, Amplify's "share resources across branches", Cloudflare's per-branch binding overrides. |

**The owner's choice (P3, P23):**
- F2, with a per-edge `block`.
- F3 later, as the edge-policy value `match` on the same per-edge record.
- Edges that cannot be read-clamped are blocked for non-primary deployments
  in v1, with no override (P23). Loosening later is easy; tightening after
  the fact is not.
- The net edge defaults to `inherit`, with `block` available.

The per-edge record is shaped like F4, but its value set is closed per edge
kind:
- `read` | `block` for edges that can be read-clamped (`read` is the default);
- `inherit` | `block` for role-less edges: the net slot and capability grants;
- `block` only, for edges that cannot be read-clamped (P23);
- later, `match`.

A value this xbind doesn't know, or one invalid for the edge's kind, reads as
`block`, so a downgrade after `match` ships fails closed. When several edges
authorize one call, any `block` among them refuses it (P27). That keeps F4's
control without its free-form knobs.

**What the clamp can enforce.**
- **Brokered resources:** kv, blob and bus writes against `res:` targets are
  checked by xbind itself, so a clamped `reader` really can't write.
- **Tile-to-tile HTTP:** xbind authorizes the call and tells the provider the
  role in `X-XBin-Role` (`internal/proxy/proxy.go:160-173`). Each endpoint's
  role check is the provider's code, exactly as for any consumer that holds
  `reader` today. A provider that ignores roles would accept a write. The
  glossary's Read clamp says so, `06-security.md` lists the residual, and
  `block` is the tile manager's answer to it.

**Custom roles and unclampable edges.** "Clamp to `reader`" needs a `reader`
to clamp to. Roles rank `reader < writer < admin`, and a custom role satisfies
`reader` only through the provider's declared `expose.implies`
(`roleSatisfies`, `internal/broker/broker.go:371-410`).

| Edge | Clampable? | v1 values for non-primary deployments (default first) | Evidence |
|---|---|---|---|
| http binding or grant with `reader`, `writer` or `admin` | yes | `read` (clamped to `reader`), `block` | conventional ranking (`internal/broker/broker.go:371-410`) |
| custom role whose provider's `implies` reaches `reader` | yes | `read`, `block` | `roleSatisfies` walks `implies` |
| custom role with no path to `reader` (D86's `channel`; the agent tile's `consumer` role on its sandbox managers) | no: a clamp would either invent a `reader` the tile was never granted or leave nothing | `block` only, no override (P23) | custom roles are accepted in allowances (`internal/users/orgs.go:608-612`) |
| cross-scope `res:` (kv, blob, bus read) | yes | `read`, `block` | xbind enforces `res:` roles itself |
| bus subscription to another scope's bus | yes | `read`: deliveries come from the provider's primary bus, re-checked against the edge at every delivery; `block` | [05-model.md §7](05-model.md#7-routing) |
| stream interface (`XBIN_IFACE_<SLOT>_ADDR`) | no: a byte stream has no roles | `block` only (P23) | reached through `DialInto` (`internal/runner/ingress.go:32`; [research/inbound-edges.md](research/inbound-edges.md) E9) |
| lan-ingress link | no | `block` only (P23) | on the provider's L3 roster, by index (`internal/broker/netfn.go:223-233`) |
| net slot: internet, lan, set, org, personal, none | not a role | `inherit` (the tile's relay policy, since the network belongs to the tile, D54), `block`. `inherit` never reaches host networking: a tile whose `net` resolves to host sharing (the `host` builtin, or a set whose rules say host) gives its non-primary deployments no egress (`block`, P23), and the panel shows why. | `netBinding` (`internal/broker/netfn.go:47`); `Broker.NetHostShare` (`:196`) |
| net slot bound to a provider tile (L3 splice) | no | `block` only (P23): no egress through it. Links are keyed by (provider, client path), so a second deployment of the client would close the primary's link fd, and a new roster entry renumbers every link and restarts the provider. | `internal/runner/netmux.go:22-32`; addresses by sorted index (`internal/broker/netfn.go:182-191`, `:212-216`, `:239-249`) |
| capability grant `gpu:*` | not a role | `block` (a shared device that cgroups don't cover), `inherit` | `GPUFor` (`internal/broker/gpu.go:14`) |
| other capability grants (`cap:*`) | not a role | `inherit`, `block` | `internal/broker/gpu.go:38-96` |

**Consequences to state plainly.**
- The agent tile reaches its sandbox managers through the custom role
  `consumer` (the sandbox-managers programme, on another branch). Under P3
  and P23, its non-primary deployments are blocked from them.
- llm-gw guards completions with `writer` (`builtin-tiles/llm-gw/backend/main.go:274`)
  and model listing with `reader` (`:269`). Under the read clamp, every
  LLM-using tile's non-primary deployments can list models but can't run a
  turn.
- Refusals and clamps are visible: the deployments panel counts them per
  edge, and a clamped call's response names the clamp.

**Recommendation:**
- F2 with a per-edge `block` (P3).
- Edges that cannot be read-clamped take only `block` in v1 (P23);
  `09-fabric.md` §5.1 lists them.
- The net slot and capability grants take `inherit` | `block`, and `inherit`
  never reaches host networking or a provider splice.
- Unknown or invalid values read as `block`, and `block` wins among several
  edges (P27).

**Status:** F2, `block` and `match`-later are ratified (P3). P23 and the net
edge's `inherit` default are owner-confirmed. P27 is proposed.

## A6 — Self-scoped registrations

**Question.** What happens when a non-primary deployment registers a cron job,
a bus push subscription, an interface instance or an ingress host, reports
status, or notifies someone?

Today, all of these stores key on the tile path, and the last writer wins:

| Store | Keyed by | Evidence |
|---|---|---|
| cron jobs | path + name; a non-admin can register only its own path | `internal/broker/cron.go:108`, `:235` |
| bus subscriptions | path + name; only its own path | `internal/broker/bussubs.go:158`, `:400` |
| interface instances | path; each call replaces the whole map and restarts every requester | `internal/broker/netfn.go:1110-1123` |
| ingress hosts | path; each call replaces the set | `internal/broker/ingressfn.go:573` |

Tiles are told to register at every start (`docs/resources.md:166`;
`sdk/xbin.go:350-355`), and the agent template does so
(`builtin-templates/agent/_backend/main.go:67-68`). Without a decision, a
non-primary deployment's first start overwrites or deletes the primary's
registrations.

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **dormant (the model)** | Registration succeeds. The deployments panel lists what would fire, with **run now** for scheduled handlers, and **deliveries** turns them on for real testing. | Nothing fires until a tile manager turns deliveries on. | SDK calls keep succeeding (rule 8). Dormant registrations live in per-deployment files, so an older xbind never loads them as `main`'s. | Medium: per-deployment stores and the panel. | Netlify ("Run now" for scheduled functions on deploys that aren't active), Vercel (cron only for the primary deployment), Cloudflare's per-branch Workers (no triggers). |
| refused | Tiles that register at start fail, or log errors on every start of a non-primary deployment. | Safe. | Breaks "what succeeds today keeps succeeding" for opted-in tiles, and a silent path must warn for one release before it errors (rule 11, `docs/compat.md:71`). | Low. | — |
| no-op | Starts work, but nothing is visible or testable. A tile that lists and then reconciles sees an empty list. | Safe. | OK. | Low. | — |
| active | Crons and deliveries run for the deployment itself. | Double-firing with external effects (a cron that emails customers; two alwaysOn bridges). | OK. | Medium. | Azure (disabling timers outside the primary is "common"; azure-functions-host#8999 timers fired twice), Railway, Convex and Piku (crons in every duplicate); Render's advice to keep such crons out. |
| shared (today) | — | Rewrites the primary's schedules and routing. | — | — | n8n (the webhook that ran drafts), Apps Script (triggers run head code). |

What the model does with each kind:

| Kind | Non-primary behaviour |
|---|---|
| cron job | dormant; active for its deployment only with deliveries on; run now (`terminal` level) |
| bus push subscription | dormant; active with deliveries on (from its own-scope bus, or the provider's primary bus under `read`) |
| interface instances, ingress hosts | dormant and never active: inbound edges belong to the primary |
| status (`/tile-report`) | stored per deployment; travels only as a `deployments` event, to the non-primary audience (A10) |
| notify and push | never pushed; shown in the panel as "would notify …", to the non-primary audience (A10) |

**Recommendation: dormant**, with the deliveries switch reserved to tile
managers in a human session (A12), and run now. → **P13.**

> **Revised by the owner 2026-09-28 (P13):** "allowing cron for non-primary
> deployments is fine; bus is trickier but subscribe-only would be ok — like
> read binds." Cron jobs and bus push subscriptions take the **active** row,
> for their own deployment only (a foreign bus read under the edge policy,
> publishing unchanged); deliveries becomes a tile manager's off switch, on
> by default. Interface instances and ingress hosts stay dormant
> ([05-model.md](05-model.md) §7).

**Status:** proposed.

## A7 — The vault

**Question.** Which secrets can a non-primary deployment's backend read?

Today:
- Each tile has one vault file, `data/vault/<CompKey>.json`
  (`internal/broker/vault.go:45-47`), sealed with the DEK and no per-tile
  label (`:96`).
- A secret's value is readable only by the tile's own backend, i.e.
  `p.Component == comp` with `Via == "instance"` (`:188`).
- The SDK builds vault URLs from `Self()`, so any split has to happen on the
  server.

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| shared | Everything works immediately. | Non-primary code acts on external accounts with real credentials. The messaging bridge reads its platform token from the vault (`builtin-templates/agent-messaging-bridge/_backend/main.go:434`) and is alwaysOn (`builtin-templates/agent-messaging-bridge/xbin.json:20`), so two deployments would hold two connections on one token. | Fine. | None. | Netlify (one value for every context by default), Dokku clones, Render's per-service duplicates: all listed pitfalls. |
| copied at creation (every value) | Works at once, then drifts. | Same exposure as shared. | Fine. | Low. | Cloudflare's base configuration for per-branch Workers, and Convex's defaults ("not kept in sync"). |
| **separate, key names as placeholders (the model)** | Code sees which keys exist, and fails with a "missing secret" error that points at **vault copy**. Secrets move key by key, as a tile-manager act. | Nothing crosses without a manager. | Fine. | Low: the file carries no label, so a vault copy is a byte-level copy of the chosen values. | Glitch (a remix keeps the names and clears the values), Supabase (branch secrets aren't shared), Railway (sealed variables are excluded from duplication), Render (`sync: false`), Heroku review-app variables. |
| separate and empty | Code fails with no hint about what's missing. | Safe. | Fine. | Lowest. | — |
| per-key sticky or shared | Flexible. | Per-key state is easy to get wrong. | Fine. | Medium. | Azure's per-setting stickiness. |

**Recommendation:** a separate vault per deployment, starting with the
primary's key names as placeholders, plus **vault copy**, a tile-manager act
in a human session (A12). → **P14.**

**Status:** proposed.

## A8 — Data seeding

**Question.** What data does a new non-primary deployment start with, and how
is it filled or refreshed?

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **empty (the default)** | Tiles that migrate at start create their schema anyway. Most development runs on synthetic data the deployment's own code creates. Realistic data needs an explicit seed. | Nothing leaves the primary's namespace. | Fine. | Lowest. | Supabase ("data-less by default"), Render's full-stack duplicates, Heroku review apps with seed scripts, Convex, Railway, Replit's fresh primary database. |
| **seed** (copy the primary's deployment data, explicitly) | Realistic testing. | PII leaves the primary's namespace, so it's a tile-manager act with a warning (Netlify: per-PR databases "can contain PII"). | Fine. | Medium. kv is re-encoded label by label inside one consistent bbolt read transaction (`internal/broker/resenc_wire.go:202-223`; `dumpKV` and `loadKV` at `internal/broker/backup.go:174`, `:322`). File resources are copied in confine or duplicated at the ciphertext level, and sqlite is quiesced first. The backup tar can't be the base: it drops symlinks and non-regular files (`internal/backup/backup.go:124-129`), and restore merges instead of replacing (`internal/broker/backup.go:219-245`). | Neon (copy by default), Supabase's "Include data", PlanetScale's data branching, Val Town (a duplicated project can take its database along). |
| copy-on-write | Instant. | As seed. | Fine. | xbin has no copy-on-write substrate: kv lives in one shared bbolt file, and file resources in per-resource gocryptfs volumes. Reflinks can make a ciphertext-level duplicate cheap where the filesystem allows, but that's an implementation detail of seed, not a separate mode. | Neon, Netlify Database. |
| schema-only | Tests migrations without real rows. | Safe. | Fine. | Needs a notion of schema across kv, blob, files and sqlite. Only sqlite has one, and the rootfs ships no `sqlite3` CLI ([research/data-plane.md](research/data-plane.md) §6b). | PlanetScale's default, Neon's schema-only branches. |
| seed hook (a tile function run once at creation) | Convenient. | Safe. | Needs a manifest key or a convention, and P5 says no manifest key. A tile can already seed itself when it finds an empty store. | Medium. | Heroku's `postdeploy`, Convex's run-once seed function, Supabase's `seed.sql`. |

**Recommendation:**
- Every non-primary deployment starts **empty**. Seeding is optional
  (owner, 2026-09-27).
- **Seed** is an explicit tile-manager act in a human session, with a PII
  warning.
- **Reset** empties the namespace again, so "reset, then seed" is Neon's
  "reset from parent".
- In a multi-tile scope the namespace is (scope, deployment name), shared by
  the siblings' same-named deployments. Seed, reset and restore stop every
  claimant and need the act's authority on each (P28).
- Seed, reset, and per-deployment backup and restore use new routes under
  `/api/xbin/deployments/…` (`11-contract.md`). Existing strictly-decoded
  bodies, `POST /restore` among them, gain no fields.
- Copy-on-write is used only to make seeding faster.

→ **P14, P28.**

**Status:** proposed.

## A9 — URL and qualifier

**Question.** How are a non-primary deployment's frontend and backend
addressed?

Constraints:
- Legacy mode authorizes credential-less subresources by URL path alone
  (`internal/server/static.go:256-311`), and relative URLs drop the query.
  So the deployment must be in the path or in the host.
- `/c/` and `/api/` resolve to the longest registered prefix
  (`internal/registry/registry.go:517-536`).

**Options.**
- **a) An in-segment `+` suffix:** `/c/<tile>+<name>/` and
  `/api/<tile>+<name>/…` (the model).
- **b) A dot-marker segment inside `/c/`:** `/c/.d/<name>/<tile>/`.
- **c) A new top-level route:** `/d/<name>/<tile>/`.
- **d) A query parameter:** `?deployment=<name>`.
- **e) Origin-only:** a tile origin per deployment (D95).

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **a) `+` suffix** | Readable, and the same form works for `/c/`, `/api/` and `bx` arguments (`apps/crm+dev`). Relative URLs stay under the qualified prefix. | `+` means nothing in grants, refs, events or routes. Its one use is the `bx bind` operator (`cmd/bx/main.go:890`). | `+` is legal in directory names, since `ComponentPathOK` has no character rule (`internal/util/util.go:97-114`). So a qualified URL resolves only for tiles with a deployment record, and only after today's resolution fails: every existing path resolves as today, `<tile>+main` on a zero-state tile included. Two narrow refusals keep it unambiguous (below). `+` becomes a space in an unencoded query string, so a qualified string never goes into a query or a JSON field. | Low: today's `Resolve` runs first, and only on a miss is a `+<name>` suffix split off and matched against a tile with a record. | Cloud Run's tagged `TAG---SERVICE` hosts, App Engine's `-dot-` hosts, Netlify's per-PR hostnames: all name-qualified addresses. |
| b) dot marker | Harder to read. Two extra segments. `/api/` needs a form of its own. | Free of collisions: no component path starts with a dot (`internal/util/util.go:97-114`), and `pathAllowed` refuses dot segments today (`internal/server/static.go:347`). | Nothing existing breaks. | Low. | — |
| c) top-level route | Clear. The API side needs a second scheme. | Fine. | Additive (rule 2), with no reservation in tile names. But tile origins serve only `/c/`, `/api/`, `/ws/events`, `/vendor/` and `/healthz` (`internal/server/tileorigin.go:153-180`). | Medium: route tables, apicheck, openapi and protocol rows. | — |
| d) query | Subresources lose it. | — | Queries are forwarded to tiles, and several keys are taken (`frame`, `native`, `preview`, `xbin_*`). | — | Rejected. |
| e) origin-only | Clean URLs. | Needed anyway in origins mode: on a shared origin, a non-primary document would share the primary's localStorage and IndexedDB (`internal/server/tileassets.go:74-81`). | Origins mode is opt-in (D95), so this can't be the only form. A label hashed over tile‖NUL‖name leaves `TileHostID` unchanged for `main` (`internal/auth/assettoken.go:182`). | Medium: reverse lookup from label to (tile, name). | Cloud Run's tagged hosts. |

**Recommendation:**
- a) for `/c/` and `/api/`;
- plus e) in origins mode: each deployment has its own origin label, keyed by
  name; `main` keeps today's label, and the bare URL goes to the primary's
  origin;
- plus the credential for self-calls: the frame-token claim and the session's
  target (P12, A19).

The qualifier's rules:
- Two narrow refusals, for every creator, admins included:
  - no tile at `<P>+<N>` while `P` has deployment `N`;
  - no deployment `N` on `P` while a component exists at `<P>+<N>`.
- Other new tile names containing `+` get a one-release warning, never a
  refusal (compat rule 11, the D82 way).
- `<tile>+<primary name>` works for humans and the tile's own principals;
  other tiles use the bare URL.
- `+` appears only in URL paths, `bx` arguments and display text. Queries and
  JSON carry `deployment` as its own field, and no event carries a qualified
  `component` (A10).

→ **P17.**

**Status:** proposed.

## A10 — Event shapes

**Question.** What do events about a non-primary deployment look like on
`/ws/events`, who receives them, and what does a reader of the tile see, so
that old clients are unaffected and new clients can show them?

Today's consumers:
- The hub's event is `{Type, Component, Text, Topic, Data}`
  (`internal/events/events.go:10-16`).
- Every non-bus event reaches every subscriber, except `pr`, `term` and
  `session` (`internal/server/server.go:592-610`).
- bx-frame reloads on a prefix match (`component === src ||
  startsWith(src + '/')`) and paints build overlays only on an exact match
  (`web/bx-frame.js:366-378`).
- The status plane clears a tile's status on that tile's `build-start`
  (`internal/obs/status.go:129-143`).
- The workspace-owned shell reloads its lists on every `reload`, and renders a
  status entry or a toast for any `component` it receives
  (`workspace-template/shell/bx-shell.js:195`, `:1257-1263`).
- The shipped iOS app reloads by the same prefix rule
  (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`)
  and maps unknown types to `.other` (`:44`).

**Options.**
- **a) Existing types** with a qualified component `"<tile>+<name>"` and a
  `deployment` field.
- **b) One new type, `deployments`, for everything non-primary** (the model):
  the bare tile path as `component`, and the deployment named in its data.
- **c) Existing types** with the bare component and a `deployment` field.
- **d) No hub events** for non-primary deployments; the panel polls instead.

**Assessment.**

| Option | UX | Security / isolation | Compat | Cost |
|---|---|---|---|---|
| a) qualified component | New clients reuse today's handlers. | Two leaks. The old shell renders any component's `status`, so a non-primary `xbin.Status` or transient notice shows as a toast in every old shell. And a nested tile's component `apps/cal/widgets/month+dev` is covered by an ancestor frame `apps/cal` in bx-frame and in the iOS app, so a non-primary save reloads the ancestor's primary view, even when that ancestor tile is in the zero state (a P5 break). | Old clients ignore `reload` and `build-*` only for tiles with no mounted ancestor. | Low. |
| **b) `deployments` only** | New vendor code handles it. The terminal window is `/vendor/` code that ships with the binary (`internal/server/server.go:156-160`). | None: old clients ignore unknown types (rule 10; iOS `.other`). | Additive: one more row in protocol.md. | Low. |
| c) bare component + field | — | Old clients reload the primary's frames, paint non-primary build errors on them and clear the primary's status. | Breaks old clients. | — |
| d) no hub events | The panel has no push updates. | None. | Fine. | A second transport. |

**Recommendation: b)**, which is 12-compat's rule C2:
- Non-primary activity never rides `reload`, `build-*`, `status` or notify. It
  rides only the `deployments` type, which carries `deployment`.
  `11-contract.md` §3.3 owns the shape and the ops.
- No qualified `component` string appears on an old event type, ever: not for
  deploys, not for reassignment, not for lifecycle.
- Everything about the primary is emitted exactly as today, with the bare
  component (P5). `build-*` keep today's meaning, for the primary only.
- A deploy's phases and failures ride `deployments`. A failed deploy onto a
  pinned primary paints no overlay: the primary keeps serving, and the actor,
  the panel and `bx` see the failure.
- A successful swap on the primary emits one `reload` for the bare component,
  only when the primary's code changed, and clears the primary's status at
  the swap, not at build start. A non-primary swap is announced only in
  `deployments`.

**Delivery is filtered by audience.**
- Facts about the primary go to the tile's readers, as today.
- Anything naming a non-primary deployment (its name, builds, compiler output,
  status, would-notify lines, data ops) goes only to admins, humans with at
  least `write` on the tile (their current level), and that deployment's own
  principals plus the tile's terminal and agent sessions.
- The primary's frame token, minted for the tile's readers, is not an own
  principal for non-primary facts.
- Other tiles receive none of it.

**What a reader sees.** A reader of the tile gets only primary-scoped facts.
`GET /api/xbin/deployments` returns live reload state as it concerns the
primary, the primary's checkpoint and deploy state, and protection: no
non-primary deployment names, and no counts that reveal them. `/components`
adds only the primary summary (A18). `11-contract.md` owns the exact shape;
the harness asserts this filtered view, not a 403.

→ **P13, P17.**

**Status:** proposed.

## A11 — Where deployment state lives

**Question.** Where do the deployment record, the checkpoint store, dormant
registrations and edge policies live, and when do they come into existence?

**Assessment.**

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| root `xbin.json` | Sits next to `lifecycle` (`internal/registry/registry.go:231`), an obvious precedent. | Admin terminals can read it. | It is re-marshalled through a struct, so an older binary drops unknown keys on its next grant change (`internal/registry/registry.go:586-595`). Boot must never rewrite it (`test/legacy_workspace_test.go:70-71`). | Low, but downgrades are unsafe. |
| **`data/` (the model)** | Invisible to builders, as operator state should be. | xbind-owned and masked from every terminal (`internal/term/binds.go:49-53`). | `data/` is the backup unit (`docs/protocol.md:2421-2423`). Precedents: `data/cron-jobs.json` and `data/bus-subscriptions.json` (`internal/broker/cron.go:77`, `internal/broker/bussubs.go:161`). | Low. |
| `.xbin/` | — | Masked. | Documented as "derived state — safe to delete when xbind is stopped" (`docs/protocol.md:2417`), while P9 requires pins to survive losing `.xbin/`. Right only for materialized trees and built artifacts. | Low. |
| a key in the tile's `xbin.json` | Visible, and kept in git with the code. | Writable from the tile's terminals, so the primary would move by hand edit, against the D24 precedent. | Every xbind ignores unknown keys (`internal/jsonc/jsonc.go:90-95`), but the key travels with clone, import and templates. Toggling it is a file edit that restarts the backend (`internal/boot/serve.go:178-180`). It is circular for branch feeds, where `xbin.json` differs per branch. | Low, but wrong. |
| refs or notes in the tile repository | Visible to git users. | Writable from the tile's sandboxes. D48 rejected foreign refs in component repositories. | — | Medium. |

**Recommendation:**
- The record, the checkpoint store, the view repository, dormant registrations
  and edge policies live in `data/`: `data/deployments/<TileKey>.json` and
  `data/deployments/<TileKey>/<name>/`, `data/checkpoints/<TileKey>.git` and
  `.view.git`. Only the deployments API (`internal/deployments`) writes the
  record, and only confined git touches the repositories.
- The record carries and verifies the tile path, the owner ref and a creation
  stamp, so a tile re-created at the path never inherits it (P29).
- Materialized trees and built artifacts are derived, and live in `.xbin/`
  (`.xbin/deploy/<TileKey>/…`).
- No manifest key.
- **Nothing is created before a committed opt-in.** Dry runs of pause live
  reload and add deployment compute the impact without capturing. A
  work-tree diff on a tile without a record answers 409 and captures nothing
  (`11-contract.md` §1.11).
- Opting out removes the record. The checkpoint store stays, inert, until GC
  or a manager's purge; the view repository is removed with the record (P5).

→ **P5, P15, P29.**

**Status:** proposed.

## A12 — Authority

**Question.** Who may change what the primary runs, and who may perform the
manager acts?

Today:
- `terminal` is a root shell in the tile's directory
  (`docs/auth.md:584-590`), and a save deploys to the primary.
- Lifecycle changes are reserved to tile managers
  (`internal/broker/lifecycle.go:28-30`, via `IsAdmin` or `mayManageTile`,
  `internal/broker/orgsapi.go:427`). `Broker.IsAdmin` also admits an element
  principal whose tile holds `xbin` at `admin`
  (`internal/broker/broker.go:466-476`), so that gate is not a human gate.

**Options.**
- **a) Parity, plus an optional protected primary** (the owner's choice).
- **b) The primary is protected by default.**
- **c) A new capability or access level for deploying to the primary.**
- **d) Humans may deploy to the primary; agents may not.**

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **a) parity + protected primary** | Opting in takes nothing away from a terminal-level user. Tile managers opt into protection. | The same power as today. Protection makes every change to the primary's code a tile-manager act, and detaches live reload from the primary (P21). | None. | Low. | Shopify (`--allow-live` is required to touch the theme customers see), Vercel's separate right to change what its primary deployment serves, protection switches at Render and n8n, Neon's protected branches. |
| b) protected by default | Opting in demotes contributors: a terminal-level user who pauses live reload can no longer reload now onto the primary, which is less than saving gave them. | Stricter than today. | Surprising: the tile behaves differently for the same people. | Low. | Convex (only admins may edit the primary deployment). |
| c) a new capability or level | Another concept in every ACL screen. | It doesn't bite while the primary is the live reload target, since a save deploys, so it needs protection's detach rule anyway. | Touches the ACL, allowances, D88's caps and the UI. | High. | n8n (`workflow:publish` versus `workflow:update`). |
| d) humans yes, agents no | Appealing. | Unenforceable for deploying. An agent runs in the human's terminal with the same terminal token, a (tile, user) principal (`internal/auth/auth.go:471`, `:499`), and D74 agent sessions reuse the terminal sandbox (`internal/term/agent.go:248`). What xbind can tell apart is a browser session from a terminal or agent token, which is why the manager acts below refuse those tokens outright. | — | — | Replit's agent can't touch the primary database, because a platform boundary sits between the human and the agent. xbin has no such boundary. |

**Recommendation: a)**, with these rules:
- **Reviewed operations onto a protected primary** name the checkpoint the
  actor reviewed: `checkpoint` on deploy and roll back; `expect` on promote,
  reload now and reassignment. A request without it is refused with 400. The
  check is a compare-and-set on the record's `seq`, with the actor's authority
  re-checked at commit; a record or work tree that moved since the review
  answers 409.
- **A protected primary is never the live reload target or a session's
  target** (P21, P24; A19).
- **Manager acts need a human session:** `p.Component == ""` and (`IsAdmin`
  or `mayManageTile`) (`internal/auth/auth.go:92`,
  `internal/broker/orgsapi.go:427`). They are seed, vault copy, deliveries,
  alwaysOn, edge policy, limits, reassign, protect, purge, and non-primary
  backup and restore. Terminal and agent tokens are refused even for
  managers, because agents share them. No element principal passes the
  manager gate in the deployments plane, whatever `xbin` or `xbin:users`
  grants its tile holds.
- Every mutating operation is a non-GET JSON request, never a WebSocket
  upgrade, so view-as sessions and form posts cannot forge it.

→ **P4** (ratified), **P21** (proposed).

**Status:** P4 ratified; P21 proposed.

## A13 — The isolation requirement and degraded modes

**Question.** What must the sandbox provide for pinned and non-primary
backends, what happens where it can't, and which sandbox mechanics change?

Why isolation is needed:
- A pinned backend must see its checkpoint at the tile's canonical path, and
  its data namespace at the primary's resource paths.
- Under isolation that is two binds with `Src ≠ Dst`, which the sandbox init
  already supports (`mountBind`, `internal/sandbox/init_linux.go:391`).
  Today the backend sandbox binds the tile directory at itself
  (`internal/runner/runner.go:641`).
- Without isolation, backends run as xbind with `cmd.Dir = c.Dir`
  (`internal/runner/runner.go:454-462`), and confine runs tools directly
  (`internal/confine/confine.go:140-143`). With no mount namespace, a
  checkpoint at another path breaks `go.work` (`internal/deps/deps.go:120-146`),
  relative `replace` directives and persisted absolute resource paths. And
  there is no boundary in that mode anyway (D78).

**Options.**
- **a) Require isolation** for pinned and non-primary backends, and refuse
  otherwise (the model). Static-only tiles can pause live reload everywhere,
  because the static plane serves checkpoints.
- **b) A non-isolated fallback:** run from the materialized path with a
  generated `GOWORK`.
- **c) Split pinning:** pin frontends everywhere, but leave backends on the
  work tree when there is no isolation.
- **d) Run deployments as D113 tile-managed sandboxes.**

**Assessment.**

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| **a) require isolation** | A clear refusal with the reason on dev-mode daemons. Installed daemons run isolated (`deploy/xbin.service:48`, `deploy/install.sh:1156`). | D78: under isolation a sandbox failure is an error, never a host fallback. | The zero state is untouched. | Medium (the shared mechanics below). |
| b) non-isolated fallback | Works on some tiles and breaks others unpredictably: relative `replace`, `go.work` `use` entries, absolute resource paths, and `XBIN_RES_*` paths that differ per deployment. | No boundary. | — | Medium, for a mode no installed daemon runs. |
| c) split pinning | The frontend claims a pin the backend doesn't honour. | Unfinished backend code serves a pinned frontend. | — | Low. |
| d) D113 tile sandboxes | A tile sandbox is an exec box the tile drives, gated by `cap:sandboxes`, with no serving lifecycle: no blue/green, health check, reaper, alwaysOn, env layer or ingress plumbing. | Mixes tile-driven and xbind-driven runtimes. | — | High, and rejected by the owner's direction. |

**The shared sandbox mechanics** that change, one level above tile-managed
sandboxes:

| Sub-decision | Options | Recommendation | Evidence |
|---|---|---|---|
| D112 registry entry for a non-`main` generation | new id + `deployment` field / same id, deployment in the label / a new kind | `main`'s rows stay byte-identical to today's (`backend:<CompKey>:g<gen>`, no deployment field), even when the tile has a record and even when `main` isn't primary. A non-`main` entry's ID is `backend+<name>:<CompKey>:g<gen>`, and only non-`main` entries set `Entry.Deployment`. `Filter.Deployment` is additive. | `internal/runner/sbx.go:63`; `internal/sbx/sbx.go:50-83` |
| VM budget owner | per deployment / per tile | per tile. `Reserve` already books to the tile path. A non-primary reservation leaves the primary's guest size free and never preempts a primary start (P25). | `internal/runner/vm.go:80`; `internal/vm/policy.go:132-171` |
| cgroup | today's shared leaf / a flat leaf per deployment / a per-tile parent with one cgroup per deployment | separate cgroups per deployment (owner, 2026-09-27) under a per-tile parent, `tile-<CompKey>/d-<name>/{backend, sbx-*}`, created when the tile first runs a non-`main` deployment. Zero-state and main-only tiles keep today's flat `comp-<CompKey>` leaf; `main` moves at a generation boundary. With a shared leaf, one deployment's exit removes the others' leaf. With a flat leaf each, every deployment gets the tile's whole cap, so adding `dev` doubles the tile's memory. The parent holds the tile's ceiling; each deployment's limits default to the tile's and never exceed it (P22), and the primary has the higher CPU weight (P25). The parent is the "nested per-tile cgroup" D113 defers, and D113 reuses it. | leaves are flat `comp-<name>` under one base (`internal/cgroup/cgroup_linux.go:78-94`), removed on exit (`internal/runner/runner.go:614`); the VM leaf is sized for two guests (`internal/runner/vm.go:103-111`) |
| code bind | the checkpoint at its own path / at the canonical path | canonical path: `Src = .xbin/deploy/<TileKey>/<full tree hash>`, `Dst = c.Dir` | `internal/runner/runner.go:641` |
| nested components inside the checkpoint bind | no bind inside / bind each nested component's code over the checkpoint | each nested component's own code (its primary's work tree or pinned checkpoint) is bound after the checkpoint bind. Mountpoints under the canonical path are created without traversing symlinks (`openat2` with `RESOLVE_NO_SYMLINKS \| RESOLVE_BENEATH` in the sandbox init), because the checkpoint's contents are tile-written. | today's `mountBind` creates targets with `os.MkdirAll`, which follows symlinks (`internal/sandbox/init_linux.go:400-407`); deeper binds mount later (`internal/sandbox/sandbox.go:122-127`) |
| data bind | the deployment's own paths / the primary's paths | the primary's paths, so `XBIN_RES_*` is identical in every deployment | [research/data-plane.md](research/data-plane.md) §6a |
| confine | an extra bind at the same depth, relying on the stable sort / an explicit bind destination | an explicit destination (`DirFrom`). The stable-sort trick (`internal/sandbox/sandbox.go:122-127`) is implicit behaviour a refactor could break. A non-isolated run with such a bind fails with a clear error instead of building the work tree. | `Dir` is bound at itself (`internal/confine/confine.go:218-224`) |
| streaming (deploy-remote rung) | buffered / streaming | a confine-level streaming run (caller-supplied readers and writers through `confine.Run`), independent of D113's unbuilt exec protocol; D113 may reuse it | `internal/confine/confine.go:87-89` |

Once D113 is built, a tile's managed sandboxes belong to its deployment,
following [05-model.md §12](05-model.md#12-isolation-and-runtimes).

**Degraded modes.**

| Host state | Pause live reload | Non-primary deployments |
|---|---|---|
| isolation off (dev mode, tier 1) | static-only tiles: yes. Tiles with a backend: refused, with the reason. | static-only: yes. With a backend: refused. |
| isolation on, a `vm` backend, VMs unavailable | refused like any VM start today; the reason goes into D112's failure ring | the same |

**Recommendation: a)**, with the shared mechanics above. Non-isolated mode is
unsupported for pinned or non-primary backends. → **P18.**

**Status:** the shared mechanics are the owner's direction (2026-09-27), and
separate cgroups per deployment is the owner's answer. P18 is
owner-confirmed. P25 is proposed.

## A14 — Chrome and governance tiles

**Question.** Which tiles may pause live reload but not have non-primary
deployments?

cgi no longer exists (removed from xbin by its own change), so no runtime
needs an exclusion here.

Today:
- Chrome runs unsandboxed and acts as the signed-in human (`sandboxedFrame`,
  `internal/server/static.go:317`; the `chrome` field,
  `internal/registry/registry.go:59-65`).
- `xbin` and `xbin:*` capabilities are tile authority
  (`internal/broker/broker.go:466-476`).
- Users, grants and tiles have no deployment dimension.

| Option | Assessment |
|---|---|
| **allow pausing live reload, forbid non-primary deployments (the model)** | Pinning a shell protects everyone from a broken save. A non-primary deployment would run unreviewed code with the human's whole session, or act on real users and grants. |
| forbid both | Loses a safe, useful way to pause live reload. |
| allow both, with non-primary principals narrowed on governance calls | A non-primary admin tile that can't govern can't do its job. A non-primary chrome deployment can't be narrowed at all: it is the human. |

**Recommendation:**
- Chrome tiles (root, shell, `chrome: true`) and tiles holding
  `xbin`/`xbin:*` capabilities may pause live reload, but may not have
  non-primary deployments.
- Approving an `xbin`/`xbin:*` grant is refused while the tile has
  non-primary deployments, and a non-primary principal never satisfies
  `IsAdmin` or any governance check.
- `chrome` is part of the inbound surface, so it follows the primary's code
  (A18). A non-primary deployment's documents are never chrome.

→ **P19.**

**Status:** owner-confirmed (P19).

## A15 — Naming (ratified)

**Question.** What is a named runtime of one tile called?

| Word | Existing meaning in xbin | Prior art | Verdict |
|---|---|---|---|
| **tile deployment** | Bare "deployment" means the xbin install in the operator docs (`docs/overview/15-operations.md:1`). The pitch says "Live, no deploy step." (`website/index.html:376`; `workspace-template/AGENTS.md:35`). | Convex (a named runtime with its own data), Cloudflare, Apps Script | **chosen**: always qualified outside this set, and the default stays deploy-free |
| stage | Free, but git's staging area confuses agents. | Heroku pipeline stages | offered, not chosen |
| identity | The path is the tile's identity (`docs/elements.md:4-5`); also principals and the auth chapter. | — | rejected: it would state the opposite of P11 |
| environment | The env layer (`setup`) and env vars. | Vercel, Railway, Render, Retool | rejected |
| slot | Interface slots. | Azure's deployment slots | rejected |
| instance | Interface instances: `apps/warehouse#dev` is documented as "the dev instance" (`docs/auth.md:1136-1137`). | — | rejected |

`main` is the name of a tile's first deployment. The primary is a role, and
the two are kept separate (Neon's reassignable default branch).

**Recommendation:** tile deployment. → **P2.**

**Status:** ratified (P2).

## A16 — The parallel-fabric path

**Question.** How can `match` extend F2 later without a redesign, and what
must v1 build now so that it can?

**Seams v1 builds anyway.**

| Seam | Built for | What `match` adds |
|---|---|---|
| the per-edge record `edges: {"<edge>": "read"\|"block"\|"inherit"}` | P3, P23 | one more value |
| one outbound resolver, `resolveTarget(caller, callerDeployment, target, edgePolicy)`, used by the HTTP proxy, stream forwards, bus-subscription resolution and `res:` namespace resolution | the read clamp | picks the provider's deployment of the caller's name |
| one inbound resolver, through which every inbound edge resolves and which returns the primary for every v1 edge-policy value | P7 | the exception for calls from same-named deployments whose edge policy is `match` |
| a deployment on `Principal` | P12 (self-calls) | carries the caller's name to the resolver |
| `EnsureDeployment` and the other new runner names, beside `Runner.Ensure` (`internal/runner/runner.go:190`), which keeps meaning the primary | non-primary backends | a non-primary target |
| `X-XBin-Deployment` on non-primary calls | P17 | the same header, on both sides |

**What `match` needs beyond the seams.**
- **A rule for a missing name** (table below).
- **The exception at P7's resolver.** A non-primary deployment receives calls
  from other tiles' same-named deployments whose edge policy is `match`.
- **No clamp.** The target is itself a non-primary deployment, inside the
  accident boundary. The authority is still the tile's grant, unchanged
  (P11).
- **Data.** `match` on a `res:` edge resolves to the provider scope's
  namespace of the same name, which exists only when that scope has the
  deployment; otherwise the missing-name rule applies.
- **Streams.** The consumer's forward map is computed per spawn, so passing a
  deployment to `DialInto` is a small change
  ([research/inbound-edges.md](research/inbound-edges.md) E9).
- **Net splices** are the only real redesign. They need rosters keyed by
  (client, deployment), with addresses that don't depend on the sorted index
  (`internal/broker/netfn.go:182-191`, `:223-249`;
  `internal/runner/netmux.go:22-32`). Because v1 blocks provider splices for
  non-primary deployments (P23), nothing in v1 depends on index-derived
  addresses.

**Missing-name options.**

| Option | Assessment |
|---|---|
| refuse (NP-04-7) | Safe and explicit. The error names the missing deployment. Switching an edge from `read` to `match` can lose reads that worked. |
| fall back to the primary, read-clamped (09-fabric §9, NP-09-15) | `match` strictly extends `read`, so no working read is lost. It can hide a miswired deployment unless the fallback is visible. |
| fall back to the primary with full rights | Leaks non-primary effects into the primary. Never offered. |
| a choice per edge | Belongs to the per-edge record, if ever wanted. |

**What v1 must do now:**
- Every edge-policy value it doesn't know reads as `block`, so a downgrade
  after `match` ships fails closed (P27).
- The resolvers are the only places that pick a target deployment.
- Edge keys (`slot:<name>`, `grant:<target>`, …) are stable identifiers.

Prior art:
- Cloudflare lists keeping a whole request path inside matching per-branch
  Workers as future work. Railway, Render and Amplify resolve each name to its
  counterpart in the same named set.
- D32's `iface:<svc>@<tile-glob>[#<inst-glob>]` already names which of a
  provider's interface instances an allowance covers
  (`internal/users/orgs.go:531`). But interface instances (IFACE-7) are
  addresses, not boundaries: `httpBindingRole` ignores the instance
  (`internal/broker/netfn.go:286-312`). So `match` must not be built on them.

**Recommendation:** `match` on the same record, with its exception at P7's
resolver when it ships. The missing-name default is M3's call, between refuse
and a read-clamped fallback; both exclude a full-role fallback (NP-04-7).

**Status:** `match` later, on the same per-edge record, is ratified (P3). P7's
one-resolver wording is owner-confirmed. The missing-name default is open
for M3.

## A17 — The principal model

An axis the list above leaves implicit. P11, P12, P20 and P26 rest on it.

**Question.** Is a deployment its own principal?

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| **(i) one principal per tile, with a deployment attribute (the model)** | Tile code is unchanged: `xbin.self`, `XBIN_COMPONENT` and `res:${xbin.self}` all still name the tile. | Grants, bindings, ceilings and ownership apply unchanged. The risk is a self-scoped handler nobody converted: it reads or writes the primary's state. That fails open for isolation, not for privilege. P26 counters it: every `/api/xbin/*` route is classified deployment-scoped, primary-only or neutral, unclassified routes refuse non-primary principals (reads included), and a guard test keeps the classification complete. | The zero value means the primary, so every existing principal behaves as today. | Moderate: about 15 self-scoped handlers ([research/serving-fabric.md](research/serving-fabric.md) B.7), plus the route classification. |
| (ii) deployment-qualified principals (`apps/x@dev`) | Every store separates automatically. | Ceilings fail open: owner-derived rows look up `owners[path]` (`internal/users/orgs.go:1210-1215`), and a qualified name has no owner entry. | `/api/${xbin.self}` resolves by longest prefix (`internal/registry/registry.go:517-536`), and vault URLs derive from `Self()`, so both break. There is no free separator: `@` is legal in paths and is the allowance separator. | High, and the risk is to privilege. |
| (iii) separate tiles (A1's option 0) | Everything is set up again for each twin. | The only real trust boundary. | None. | None. |

**Recommendation: (i)**, with non-primary deployments as an accident boundary
rather than a trust boundary, and xbind's API default-deny for non-primary
principals. → **P11, P12, P20, P26.**

**Status:** proposed.

## A18 — Which code each manifest field follows

**Question.** Once the work tree, the primary's code and a non-primary
deployment's code can differ, which of them does each manifest field, each
resource declaration and each `/components` field come from?

**Options.**
- **a) Everything from the work tree** (one manifest, as today).
- **b) Everything from each deployment's own code.**
- **c) Two columns:** tile-level fields from the work tree, deployment-level
  fields from the deployment's own code.
- **d) Three columns (the model):** authority requests from the work tree;
  the inbound surface from the primary's code; deployment-level fields from
  the deployment's own code.

**Assessment.**

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| a) work tree | One place to look. | Pinned stops meaning pinned: a work-tree edit to `runtime`, `entry` or `scope.json` changes what a pinned deployment runs at its next restart (P9). | Zero-state behaviour, but wrong for every pinned deployment. | Lowest. |
| b) own code | Each deployment is self-contained. | A pinned checkpoint's stale `uses` could not file new requests, and a non-primary deployment's `exposes` has no meaning, since only the primary receives inbound edges (P7). Authority is the tile's (P11), so per-deployment requests would conflict. | Breaks the pending-grant flow for anything not yet deployed. | Medium. |
| c) two columns | Close to the code's reading. | `exposes`, `provides`, `template` and `chrome` stay tile-level, so a save while live reload is paused changes what the pinned primary exposes and how it is framed, and a terminal-level user changes a protected primary's framing and routes without a manager. | Fine. | Medium. |
| **d) three columns** | A save while live reload is paused changes nothing that serves traffic. | A protected primary's inbound surface changes only through a manager's deploy (P21). | The zero state reads every column from the work tree, as today. | Medium. |

**Resource declarations.** The owner decided them separately (P22):

| Option | Assessment |
|---|---|
| lock-step (the tile declares; every deployment gets every resource) | The primary provisions resources only development code needs, and a pinned primary's set moves with the work tree. |
| the primary's code declares for everyone | Development usually needs more resources than the older code on the primary, so `dev` could never add one. |
| **per deployment, from each deployment's own code (P22)** | A new resource exists only in the namespace of the deployment whose code declares it; the primary gains it when that code is promoted. |

**Recommendation: d)**, as [05-model.md §6](05-model.md#6-what-code-and-which-manifest-a-deployment-runs)
tabulates:
- **Tile-level (the work tree):** existence (`xbin.json` / `index.html`),
  path, scope membership, `uses`, `interfaces`, `deps`. A pinned deployment's
  `XBIN_RES_*` env is built from the union of the work tree's and its own
  code's `uses`, filtered by the tile's grants.
- **Inbound surface (the primary's code):** `template`, `exposes`,
  `expose.roles`, `provides`, `chrome`: the work tree while the primary
  follows it, its checkpoint while pinned. A non-primary deployment's
  documents are never chrome.
- **Deployment-level (the deployment's own code):** `runtime`, `entry`,
  `setup`, `alwaysOn`, `vm`, `inject`, `native`, `scope.json`'s `resources`,
  and every file served or executed.
- **Resources (P22):** every deployment provisions what its own code
  declares, in its own namespace; a pinned primary provisions from its
  checkpoint's `scope.json`. A checkpoint's `scope.json` is read with
  `OpenBeneath` (`internal/fsutil/beneath.go:34`) and validated (the
  resource-name charset) before anything is provisioned. A resource the
  primary's code no longer declares is kept, as today. A plain-directory
  scope's `scope.json` applies to every deployment. Limits are per
  deployment: they default to the tile's, are set by tile managers, and never
  exceed the tile's ceilings.
- **`/components`** (`internal/server/api.go:129-160`): the deployment-level
  fields `runtime`, `hasIndex` and `native` (and `/c/<tile>/?native=1`)
  describe the primary's code, its checkpoint when pinned; `chrome` follows
  the inbound surface; `manifestError`, `roles`, `uses` and `deps` stay the
  work tree's. It adds only the primary summary (A10).
- When the work tree has no valid manifest, a tile with a record keeps
  serving its pinned primary; the tile-level fields fall back to the
  primary's checkpoint manifest, and the manifest error is surfaced.

→ **P9, P21, P22.**

**Status:** P22 owner-confirmed; the three-column split (P9, P21) proposed.

## A19 — A session's target

**Question.** Which deployment do a terminal or agent session's self-calls and
`bx` commands address, and how is it chosen?

**Options.**
- **a) The terminal's existing API dropdown**, with one entry per deployment
  the user may reach (the owner's choice).
- **b) A separate target picker** beside the API dropdown.
- **c) Chosen per command** (`XBIN_DEPLOYMENT` changed in the shell, or a
  flag on each `bx` call).
- **d) Always the live reload target.**

**Assessment.**

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| **a) API dropdown** | One control users already know: today it switches the tile API on or off. The default (the primary) is what every session reaches today. | The target is bound into the session's token at start and fixed for its life, so an agent can't retarget itself mid-session; changing it restarts the session, a terminal-level act (P12, P20). | A zero-state tile's dropdown looks as today. | Low. |
| b) separate picker | Two controls that can disagree (API off, target `dev`). | As a. | A new control in every terminal window. | Low–medium. |
| c) per command | Flexible. | The server must trust whatever the shell says, and an agent could flip a call to the primary. Operation commands (`bx deploy --to`) already name their deployment explicitly; self-calls need one bound target. | — | Medium. |
| d) live reload target | Follows the code being edited. | Sessions on a tile whose live reload is on `dev` silently stop reaching the primary, and while live reload is paused there is no target at all. | Surprising for existing sessions. | Low. |

**Recommendation: a).**
- The default is the primary. A protected primary is never offered, and the
  default then falls to the live reload target. When neither exists (a
  protected primary with live reload paused), the dropdown falls to "API
  off".
- A session's target is fixed for its life: a named deployment or "the
  primary". `XBIN_DEPLOYMENT` is set only when the target is not the primary
  at session start.
- Protecting the primary restarts the sessions that target it onto the
  default, or ends them.
- The dropdown offers only deployments the user may reach: at least `write`
  on the tile, and `terminal` level to open the session at all.

→ **P24**, with P12 and P21.

**Status:** owner-confirmed (P24).

## Open questions

These go to [16-open-questions.md](16-open-questions.md).

1. **Nested components under a parent's deployment URL** (settled). This
   document proposed that `/c/<parent>+<name>/<child>/` serve the child's
   primary. It withdraws that in favour of `11-contract.md` §2.4: a qualified
   URL never enters a nested component, and the answer is 404 naming the
   child's own deployment URLs. The parent's deployment never speaks for the
   child. Serving the child under the parent's write-gated prefix would also
   show the child's code to a user who can write the parent but not read the
   child.
2. **Tracked-branch feed.** Does any new head deploy, force pushes included,
   or only fast-forwards? The proposal is any head, since each is simply a new
   checkpoint, but it's worth a deliberate call.
3. **Deploy-remote feed.** May a push to `deploy/<name>` create deployment
   `<name>`? The proposal is no: Dokku's auto-creation on push is a documented
   pitfall.
4. **Provider role enforcement.** Should `bx doctor` warn a tile manager when
   a `read` edge points at a provider that declares no `expose.roles` (A5)?

## Decision summary

| Axis | Choice | P | Status |
|---|---|---|---|
| A1 model | pointers over a checkpoint store; tracked branch and deploy remote as later feeds; a read-only fetch remote in v1; twin tiles unchanged | P1 | ratified |
| A2 checkpoint mechanism | private confined git store keyed by `<TileKey>`; gitignore doesn't apply; `.git` and nested components excluded; fidelity limits documented; caps; a git view served from a separate view repository | P1, P9, P16 | ratified (store); proposed (mechanism) |
| A3 pausing live reload | pin to a checkpoint; checkpoints on demand; no candidate builds in v1; resume deploys the work tree; a deploy onto the target pauses live reload; dry runs capture nothing | P8, P9 | proposed |
| A4 storage keys | follow the deployment; `main` owns today's keys; reassignment (M2, managers) moves routing only | P6, P7, P28 | owner-confirmed (P7); proposed |
| A5 fabric | F2: providers' primaries, read-clamped, per-edge `block`; unclampable edges `block` only; net and capability edges `inherit` \| `block`, never host networking or a splice; fail-closed values | P3, P23, P27 | ratified; owner-confirmed (P23); proposed (P27) |
| A6 registrations | dormant; deliveries switch; run now; notifications never pushed | P13 | proposed |
| A7 vault | separate, key names as placeholders; tile-manager vault copy | P14 | proposed |
| A8 seeding | empty by default; optional tile-manager seed with PII warning; reset; shared (scope, name) namespaces | P14, P28 | proposed |
| A9 URL | `+` suffix for `/c/` and `/api/`, resolved only for tiles with a record after today's resolution fails; two narrow refusals; an origin per deployment in origins mode; `deployment` as its own wire field | P17 | proposed |
| A10 events | only the `deployments` type for anything non-primary (12-compat rule C2); audience-filtered delivery; readers see primary-scoped facts only | P13, P17 | proposed |
| A11 state | `data/`, keyed by `<TileKey>`; `.xbin/` only for derived trees and artifacts; no manifest key; nothing created before a committed opt-in | P5, P15, P29 | proposed |
| A12 authority | parity plus an optional protected primary; reviewed operations name the checkpoint; manager acts need a human session | P4, P21 | ratified; proposed (P21) |
| A13 isolation | required for pinned and non-primary backends; the shared mechanics (registry IDs, VM books, separate cgroups under a per-tile parent, launch binds, nested binds, confine destination) | P18, P25 | owner-confirmed; owner's direction; proposed (P25) |
| A14 chrome and governance | may pause live reload, with no non-primary deployments; `xbin` grants refused while non-primary deployments exist | P19 | owner-confirmed |
| A15 naming | tile deployment; `main` (name) and primary (role) | P2 | ratified |
| A16 parallel fabric | `match` on the same record; its exception at P7's resolver; the missing-name default decided in M3 | P3, P7; NP-04-7 | ratified; owner-confirmed (P7); open (rule) |
| A17 principal | one principal plus a deployment attribute; an accident boundary; default-deny API | P11, P12, P20, P26 | proposed |
| A18 manifest fields | three columns: authority requests from the work tree, inbound surface from the primary's code, the rest (resources included) from each deployment's own code | P9, P21, P22 | owner-confirmed (P22); proposed |
| A19 session target | the terminal's API dropdown; default the primary; a protected primary never offered; fixed for the session's life | P24 | owner-confirmed |

## Divergences from the model

None is open. The six this document raised were settled by the spine and the
integrator's rulings of 2026-09-27:

1. Events on existing types with a qualified component: resolved by P13 and
   [05-model.md §8](05-model.md#8-events-and-what-clients-see) (12-compat's
   rule C2; A10).
2. Cross-tile references: resolved by [05-model.md §6](05-model.md#6-what-code-and-which-manifest-a-deployment-runs)
   (`deps/` re-dispatched through the other tile's `/c/` plane, `go.work`
   binds; A2).
3. The read clamp's "never writes": resolved by the glossary's Read clamp
   (HTTP roles are the provider's to enforce; A5).
4. `match` and P7: resolved by P7's one-resolver wording (A16).
5. "Code-identical swap": resolved by [05-model.md §5](05-model.md#5-operations)
   ("file-identical within the checkpoint's fidelity limits"; A3.1).
6. The net edge and provider splices: resolved by P23 and
   [05-model.md §7](05-model.md#7-routing) (`inherit` never reaches host
   networking or a splice; A5).

## New proposals

IDs are stable; resolved entries keep one line.

- **NP-04-1** — resolved by P13 and [05-model.md §8](05-model.md#8-events-and-what-clients-see) (12-compat's rule C2).
- **NP-04-2** — resolved by P27.
- **NP-04-3** — resolved by P23 and [05-model.md §7](05-model.md#7-routing) (the net edge's `inherit`).
- **NP-04-4** — resolved: adopted into `07-runtime.md` (caps in §2.5; a per-tile exclude list, if any, lives in the record).
- **NP-04-5** — resolved: adopted into `11-contract.md`; under 12-compat's rule C2 no event carries a qualified `component` (A9).
- **NP-04-6** — resolved: rejected for v1 by the glossary's "paused: saves change the work tree and nothing else" (A3.2).
- **NP-04-7 — The missing-name default for `match`** (A16; M3, open). P7's
  one-resolver wording is settled; what remains is the fallback when the
  provider has no same-named deployment:
  - this proposal: refuse, naming the missing deployment;
  - `09-fabric.md` §9's NP-09-15: fall back to the provider's primary,
    read-clamped, with `deny` available per edge.

  Both exclude a full-role fallback to the primary. M3's design decides
  between them.
- **NP-04-8** — resolved by P26, which also covers reads.
- **NP-04-9** — resolved by P17 and the glossary's Deployment URL (the two narrow refusals, for every creator).
