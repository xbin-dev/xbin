# 04 — The tradeoff surface

> Status: live — every design axis of the tile dev lifecycle, its real options, how each option scores, and which choice the model makes and why (part of [plans/dev-lifecycle](README.md))

This document argues each choice in [05-model.md](05-model.md) against the
alternatives. It does not restate the model. Every recommendation names the
invariant (P1–P21, [05-model.md §13](05-model.md#13-invariants-the-proposed-decisions))
that carries it, and where this document and the model differ in detail, the
model is normative until the integrator resolves the entry in
[Divergences from the model](#divergences-from-the-model). Terms are
[01-glossary.md](01-glossary.md)'s, used verbatim.

Evidence:
- Facts about today carry `file:line`. They were re-verified against this
  worktree (master plus the `sandbox-visibility` branch), so some differ from
  the research maps, which were taken before those commits.
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
- **Status:** *ratified* means the owner ratified it on 2026-09-27 (P1–P4).
  *Proposed* covers P5 onward and every NP-04-n.

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
| C deploy remote | An explicit act: push is not commit. Agent-friendly, and a dirty tree is no problem. Non-git users are left out, and gitignored runtime files are missing unless xbind builds them. | Every receive must run confined. The remote must refuse pushes to a protected primary. | Additive routes under `/api/xbin/`. | High. There is no receive-pack in xbind. Confine buffers all output (`internal/confine/confine.go:87-89`, `:250-265`), so a push needs a streaming confined run; D113's exec protocol is the precedent. | Heroku (one branch deploys), Dokku (a deploy-branch filter; `apps:lock`), Piku. |
| D work tree per deployment | Every deployment can be edited at once. But agents, terminals, the watcher and `go.work` all assume one directory per tile (C4). A second tree is either an unwatched dot directory or a new path, which is option 0. | Doubles the editing surfaces per tile. | Rewrites the editing plane. | Very high. | Val Town's always-live branches, whose data semantics are undocumented; Amplify sandboxes ("only one can be running at a time"). |

**Recommendation: A**, with B and C recast as **feeds** on the same core:
- The **tracked branch** feed keeps B1's behaviour. It reads the branch inside
  confine and captures each new commit into the store, so the untrusted
  repository never becomes the record.
- The **deploy remote** feed is C.

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
  - A bare repository at `data/checkpoints/<CompKey>.git`.
  - Capture is a confined `git --git-dir=<store> --work-tree=<tile>` run with
    a store-private index: `add --all --force`, minus exclude pathspecs, then
    `write-tree`. The tile is bound read-only. The checkpoint id derives from
    the tree hash, so identical content is one checkpoint.
  - This is D77's private-git-dir pattern (`internal/term/agentdiff.go:91-124`;
    `add -A` and `write-tree` at `:273-279`), run with confine's hardened
    flags and env vars (`internal/confine/git.go:14-34`).
  - Checkpoints are materialized read-only under
    `.xbin/deploy/<CompKey>/<id>/`.
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
| a) git store | Content-addressed ids (`c:3f2a1c9`) and deduplication for free. `diff-tree` gives the promote dialog its diff. `update-server-info` makes it a fetch-only remote for flow H, as for template repositories (`internal/broker/templaterepo.go:93`). The fidelity limits must be documented, and the glossary does. | Confined runs only (D78), with hooks and fsmonitor off. The store sits in `data/`, which every terminal masks (`internal/term/binds.go:49-53`). | Internal; the `data/` layout isn't promised (`docs/compat.md:141`). | Medium. Each confined run costs about 45 ms (fuse-overlayfs) or 17 ms (kernel overlay) per D78, plus hashing the changed files. Keeping the index in the store makes repeat captures cheap. | Lambda, Apps Script and Cloud Run all keep immutable, content-fixed captures behind their pointers; Val Town built its own VCS. |
| b) tar/copy | Full fidelity. But no ids, no diff and no fetchable remote. | An in-process copier reads sandbox-writable files and races a symlink put in a file's place; the backup writer has exactly this race (`internal/backup/backup.go:124-130`). Inside confine it is safe but slow. | Same as a. | Storage grows with every checkpoint, since there is no deduplication without a hardlink farm. A content-addressed file store would reinvent git. | Heroku slugs, Fly images, Netlify atomic deploys. All of these store built artifacts, not the tree that produced them. |
| c) reflink | Instant, full fidelity. | Same race as b unless run in confine. | Depends on the filesystem: ext4 and overlay have no reflinks, so a or b is needed as a fallback anyway. | Low where it works, but two code paths overall. | Neon's copy-on-write branches (at the storage level). |

**What a checkpoint contains.**

| Content | Rule | Why |
|---|---|---|
| gitignored files | included: `add --force`, and the store's own exclude files are empty | A pinned deployment must run what the live reload target ran. `node_modules`, generated assets and local config files are usually gitignored, and node and python backends load them from the tile directory (`internal/runner/runner.go:395-405`). |
| `node_modules` | included | A runtime dependency. Backups skip it as reproducible (`internal/broker/backup.go:117-118`), but reinstalling at deploy time would be a network build, not a pin. Deduplication limits the cost to changed files. |
| `.git` directories | excluded | History isn't code, and the repository is untrusted. |
| nested components | excluded, by exclude pathspecs taken from the registry and from any directory holding a `.git`, so no gitlink entries appear | Each is a tile of its own (`internal/registry/registry.go:469-471`), with its own deployments or its own zero state. |
| `deps/` symlinks | included, as links | xbind regenerates them as relative links (`internal/deps/deps.go:59-69`). The static plane never follows a link out of a checkpoint (P16), unlike the legacy plane, which follows cross-tile links today (`internal/server/static.go:193-195`). See [Divergences](#divergences-from-the-model) for where they lead. |
| dot files and dot directories | included | The watcher ignores them (`internal/watch/watch.go:61-74`), but backends read them, `.env`-style configuration for example. |
| size | capped per checkpoint (bytes and files) and per store | Over the cap, the capture is refused with the largest directories named; it is never truncated. `07-runtime.md` sets the numbers. The exclude knob is NP-04-4. |

A capture reads a tree that may still be changing, so a file caught mid-write
can be captured torn. Live reload has the same race today. A capture must
therefore start only after the watcher's 300 ms quiet period
(`internal/boot/boot.go:40-41`).

**Recommendation: a).**
- Checkpoints use the private confined git store.
- The fidelity limits are part of the checkpoint's definition.
- Checkpoints are materialized read-only, and built artifacts are kept per
  checkpoint (P9).
- Reflinks may later speed up materialization, never capture.

→ **P1** (the store), **P9**, **P16**.

**Status:** P1 is ratified (an xbind-owned checkpoint store). The mechanism
and the inclusion rules are proposed (P16, NP-04-4).

## A3 — Pausing live reload

**Question.** What does pausing live reload hold still? When are checkpoints
taken? What do reload now and resume do?

### A3.1 Mechanism

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| a) Watcher flag: skip `reload` and `run.Changed` for the tile in `watchLoop` (`internal/boot/serve.go:178-180`) | Live reload looks paused until the first restart or page load. Every restart path rebuilds from the work tree (C5). node and python run straight from the tile directory (`internal/runner/runner.go:395-405`, `:454-462`). The next navigation serves the unfinished files. | Unfinished code reaches the primary silently. | — | Very low. | n8n 2.0 is the cautionary tale: its webhook path ran drafts, bypassing the pin. Every entry point must resolve through one "which code runs" pointer. |
| **b) Pin to a checkpoint (the model):** pausing live reload checkpoints the work tree and deploys it, and every restart runs that checkpoint | Frontend and backend both hold still. | Nothing reaches the primary without an explicit act. | The zero state is untouched. | Medium: a checkpoint and a materialization, and for Go a build keyed by checkpoint. Backends need isolation (A13). | Netlify's locked deploys, Deno's timeline locking, Cloud Run's `--no-traffic`, Vercel with auto-assign off. |
| c) Refuse edits while live reload is paused (Dokku `apps:lock`) | Defeats the purpose: the developer pauses live reload precisely to keep editing. | — | — | Low. | Dokku `apps:lock`, which rejects pushes. |

### A3.2 When checkpoints are taken

| Option | UX | Cost | Prior art |
|---|---|---|---|
| **a) On demand (the model):** when live reload is paused, and at reload now, deploy, promote and add deployment | Build errors appear only when reload now runs. The terminal bar can still show "N files changed since c:…", via a confined capture and diff on each debounced batch, for tiles whose live reload is paused. | Lowest. No per-save cost on the default path (P8). | Netlify's manual deploy activation, Piku with `PIKU_AUTO_RESTART=false`. |
| b) A checkpoint on every save while live reload is paused | A checkpoint for every save. Little extra value, since the deploy log already records what ran. | One confined run per batch. | Val Town (every change kept), Retool's history. |
| c) As b, plus a candidate build that isn't activated | Reload now activates something already built. Build errors show in the terminal window before anyone presses it. | One build per save: what live reload costs today. | Netlify (builds continue while locked, ready to activate later); Deno (pushes build but don't activate). |
| d) A checkpoint on every save, on every tile with deployments, the live reload target included | — | Breaks P8. | — |

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
- checkpoints on demand in v1 (A3.2 a), with candidate builds (c) as a later
  enhancement that doesn't change what activation means (NP-04-6);
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
| **a) follows the deployment** | "Primary" and `main` can diverge, so the UI must say whose data the primary serves (flow F). | There is no re-encryption step to get wrong. | No migration. The files builder docs name, such as `.xbin/log/<compkey>.log`, stay where they are. | Low: one key function routes every call site. | Neon (the default branch can be reassigned; data stays with each branch), Convex, Heroku (add-ons stay with their app), Deno Deploy's late binding per timeline. |
| b) follows the role | Intuitive: the primary always holds the real data. | Reassignment must rename directories, re-encrypt kv under its `kv:<bucket>` label (`internal/broker/resenc_wire.go:202-223`) and re-wrap every gocryptfs volume, whose key derives from `resLabel` (`:43`). None of that is atomic, and a crash midway splits the data. | Fine. | High. | Azure moves non-sticky connection settings along with the code, a documented trap. Deno Classic's bug served the wrong KV from the primary domain. |
| c) everyone namespaced | — | — | Migrates every opted-in tile's data, the opposite of "enabling deployments never migrates". | High. | Replit's shared-to-separate database migration left old remixes needing manual work. |
| d) opaque ids | Names could be renamed. | — | — | An indirection table to back up and restore. Names are immutable for the reason user ids are (D15). | — |

**Recommendation: a).** Non-primary namespaces sit at a dot level that no
scope key can produce. `util.ScopeKey` isn't injective
(`internal/util/util.go:127-132`), but no component path can start with a dot
(`internal/util/util.go:97-114`). Encrypted mounts stay under `.xbin/resenc/`
for the installer's AppArmor rule (`deploy/install.sh:909`). The exact
scheme is `08-data.md`'s. Reassigning the primary moves routing only.
→ **P6, P7.**

**Status:** proposed.

## A5 — The outbound fabric (F0–F4)

**Question.** When a non-primary deployment uses one of its tile's edges, what
does the call reach, and with which role?

Today:
- Every outbound call is authorized by the tile's grants: explicit rows, then
  the http-binding role, then same-scope `uses`
  (`internal/broker/broker.go:418-452`).
- The proxy turns the result into one role, strips every inbound `X-XBin-*`
  header, sets `X-XBin-Role` (`internal/proxy/proxy.go:160-173`,
  `:275-279`), and reaches the provider through `Runner.Ensure(comp)`
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
| **F2 read-clamped + `block`** | Reads work (dashboards, lookups). Writes are refused with an error that names the policy. `block` is for edges that shouldn't even read. | No writes into another tile's primary through xbind-brokered resources. For HTTP APIs, the provider enforces the role it's told (see below). Reading real provider data widens nothing compared with the zero state, where a terminal-level user can make the primary read and log it by saving. Under a protected primary, though, `read` edges are the one route left by which terminal-level code reads providers' data, which is why edge policies are a tile-manager setting and `block` exists. | The primary is unaffected, and the deployment marker is absent on its calls. | Medium: a clamp in one resolver, plus a per-edge record. | Retool (resource permissions set per resource configuration, e.g. read-only on the real database). No platform clamps by default; this is new. |
| F3 `match` | The best fidelity: `dev` talks to `dev` with no code change. Needs same-named deployments to exist and be seeded, and a rule for a missing name. | Safe if a missing name is refused; a leak if it falls back to the primary. | Needs an exception to P7 (see A16). | High for L3: net splices are numbered by the sorted client index (`internal/broker/netfn.go:182-191`, `:223-249`). Moderate for HTTP. | Railway (a service's private DNS name resolves to its counterpart in the same named set), Render's `fromService`, Amplify's backend per branch, and Cloudflare's stated future work. |
| F4 per binding | The most control, with many knobs per edge. | Depends on the knob, and easy to misconfigure: Azure's per-setting stickiness is the documented trap. | — | Medium. | Render's `previewValue`, Amplify's "share resources across branches", Cloudflare's per-branch binding overrides. |

**The owner's choice (P3):**
- F2, with a per-edge `block`.
- F3 later, as the edge-policy value `match` on the same per-edge record.

The per-edge record is shaped like F4, but its value set is closed (`read` |
`block`, later `match`). That keeps F4's control without its free-form knobs.

**What the clamp can enforce.**
- **Brokered resources:** kv, blob and bus writes against `res:` targets are
  checked by xbind itself, so a clamped `reader` really can't write.
- **Tile-to-tile HTTP:** xbind authorizes the call and tells the provider the
  role in `X-XBin-Role` (`internal/proxy/proxy.go:160-173`). Each endpoint's
  role check is the provider's code, exactly as for any consumer that holds
  `reader` today. A provider that ignores roles would accept a write. This is
  the residual `06-security.md` must list, and `block` is the tile manager's
  answer to it (see [Divergences](#divergences-from-the-model)).

**Custom roles and unclampable edges.** "Clamp to `reader`" needs a `reader`
to clamp to. Roles rank `reader < writer < admin`, and a custom role satisfies
`reader` only through the provider's declared `expose.implies`
(`roleSatisfies`, `internal/broker/broker.go:370-410`).

| Edge | Clampable? | Proposed non-primary default | Evidence |
|---|---|---|---|
| http binding or grant with `reader`, `writer` or `admin` | yes | `read` (clamped to `reader`) | conventional ranking (`internal/broker/broker.go:370-410`) |
| custom role whose provider's `implies` reaches `reader` | yes | `read` | `roleSatisfies` walks `implies` |
| custom role with no path to `reader` (D86's `channel`) | no: a clamp would either invent a `reader` the tile was never granted or leave nothing | `block`; `read` is refused on this edge | custom roles are accepted in allowances (`internal/users/orgs.go:609-610`) |
| cross-scope `res:` (kv, blob, bus read) | yes | `read` | xbind enforces `res:` roles itself |
| bus subscription to another scope's bus | yes | `read`: deliveries come from the provider's primary bus | [05-model.md §7](05-model.md#7-routing) |
| stream interface (`XBIN_IFACE_<SLOT>_ADDR`) | no: a byte stream has no roles | `block` | reached through `DialInto(prov)` ([research/inbound-edges.md](research/inbound-edges.md) E9) |
| lan-ingress link | no | `block` | on the provider's L3 roster, by index (`internal/broker/netfn.go:223-233`) |
| net slot: internet, lan, set, org, personal, none | not a role | inherit the tile's policy, since the network belongs to the tile (D54) | `netBinding`, `internal/broker/netfn.go:47` |
| net slot bound to a provider tile (L3 splice) | no | `block`: relay-only egress or none. Links are keyed by (provider, client path), so a second deployment of the client would close the primary's link fd, and a new roster entry renumbers every link and restarts the provider. | `internal/runner/netmux.go:22-32`; addresses by sorted index (`internal/broker/netfn.go:182-191`, `:212-216`, `:239-249`) |

**Recommendation:**
- F2 with a per-edge `block` (P3).
- The unclampable defaults above, proposed for `09-fabric.md` to adopt
  (NP-04-3).
- Any edge-policy value an xbind doesn't know reads as `block` (NP-04-2).

**Status:** F2, `block` and `match`-later are ratified (P3). The unclampable
defaults are proposed.

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
| cron job | dormant; active for its deployment only with deliveries on; run now |
| bus push subscription | dormant; active with deliveries on (from its own-scope bus, or the provider's primary bus under `read`) |
| interface instances, ingress hosts | dormant and never active: inbound edges belong to the primary |
| status (`/tile-report`) | stored per deployment, and emitted only under a new event type (A10, NP-04-1) |
| notify and push | never pushed; shown in the panel as "would notify …" |

**Recommendation: dormant**, with the deliveries switch reserved to tile
managers, and run now. → **P13.**

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
primary's key names as placeholders, plus a tile-manager **vault copy**.
→ **P14.**

**Status:** proposed.

## A8 — Data seeding

**Question.** What data does a new non-primary deployment start with, and how
is it filled or refreshed?

**Assessment.**

| Option | UX | Security | Compat | Cost | Prior art |
|---|---|---|---|---|---|
| **empty (the default)** | Tiles that migrate at start create their schema anyway. Realistic data needs an explicit seed. | Nothing leaves the primary's namespace. | Fine. | Lowest. | Supabase ("data-less by default"), Render's full-stack duplicates, Heroku review apps with seed scripts, Convex, Railway, Replit's fresh primary database. |
| **seed** (copy the primary's deployment data, explicitly) | Realistic testing. | PII leaves the primary's namespace, so it's a tile-manager act with a warning (Netlify: per-PR databases "can contain PII"). | Fine. | Medium. kv is re-encoded label by label inside one consistent bbolt read transaction (`internal/broker/resenc_wire.go:202-223`; `dumpKV` and `loadKV` at `internal/broker/backup.go:174`, `:322`). File resources are copied in confine or duplicated at the ciphertext level, and sqlite is quiesced first. The backup tar can't be the base: it drops symlinks and non-regular files (`internal/backup/backup.go:124-129`), and restore merges instead of replacing (`internal/broker/backup.go:219-245`). | Neon (copy by default), Supabase's "Include data", PlanetScale's data branching, Val Town (a duplicated project can take its database along). |
| copy-on-write | Instant. | As seed. | Fine. | xbin has no copy-on-write substrate: kv lives in one shared bbolt file, and file resources in per-resource gocryptfs volumes. Reflinks can make a ciphertext-level duplicate cheap where the filesystem allows, but that's an implementation detail of seed, not a separate mode. | Neon, Netlify Database. |
| schema-only | Tests migrations without real rows. | Safe. | Fine. | Needs a notion of schema across kv, blob, files and sqlite. Only sqlite has one, and the rootfs ships no `sqlite3` CLI ([research/data-plane.md](research/data-plane.md) §6b). | PlanetScale's default, Neon's schema-only branches. |
| seed hook (a tile function run once at creation) | Convenient. | Safe. | Needs a manifest key or a convention, and P5 says no manifest key. A tile can already seed itself when it finds an empty store. | Medium. | Heroku's `postdeploy`, Convex's run-once seed function, Supabase's `seed.sql`. |

**Recommendation:**
- Every non-primary deployment starts **empty**.
- **Seed** is an explicit tile-manager act with a PII warning.
- **Reset** empties the namespace again, so "reset, then seed" is Neon's
  "reset from parent".
- Copy-on-write is used only to make seeding faster.

→ **P14.**

**Status:** proposed.

## A9 — URL and qualifier

**Question.** How are a non-primary deployment's frontend and backend
addressed?

Constraints:
- Legacy mode authorizes credential-less subresources by URL path alone
  (`internal/server/static.go:256-311`), and relative URLs drop the query.
  So the deployment must be in the path or in the host.
- `/c/` and `/api/` resolve to the longest registered prefix
  (`internal/registry/registry.go:519-536`).

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
| **a) `+` suffix** | Readable, and the same form works for `/c/`, `/api/` and `bx` arguments (`apps/crm+dev`). Relative URLs stay under the qualified prefix. | `+` means nothing in grants, refs, events or routes. Its one use is the `bx bind` operator (`cmd/bx/main.go:890`). | `+` is legal in directory names, since `ComponentPathOK` has no character rule (`internal/util/util.go:97-114`). It is therefore reserved for new tile names the D82 way, and an exact match on an existing component wins. `+` becomes a space in an unencoded query string, so a qualified string must never go into a query (NP-04-5). | Low: `Resolve` splits the suffix off the last segment before the prefix walk. | Cloud Run's tagged `TAG---SERVICE` hosts, App Engine's `-dot-` hosts, Netlify's per-PR hostnames: all name-qualified addresses. |
| b) dot marker | Harder to read. Two extra segments. `/api/` needs a form of its own. | Free of collisions: no component path starts with a dot (`internal/util/util.go:97-114`), and `pathAllowed` refuses dot segments today (`internal/server/static.go:347`). | Nothing existing breaks. | Low. | — |
| c) top-level route | Clear. The API side needs a second scheme. | Fine. | Additive (rule 2), with no reservation in tile names. But tile origins serve only `/c/`, `/api/`, `/ws/events`, `/vendor/` and `/healthz` (`internal/server/tileorigin.go:153-180`). | Medium: route tables, apicheck, openapi and protocol rows. | — |
| d) query | Subresources lose it. | — | Queries are forwarded to tiles, and several keys are taken (`frame`, `native`, `preview`, `xbin_*`). | — | Rejected. |
| e) origin-only | Clean URLs. | Needed anyway in origins mode: on a shared origin, a non-primary document would share the primary's localStorage and IndexedDB (`internal/server/tileassets.go:74-81`). | Origins mode is opt-in (D95), so this can't be the only form. A label hashed over tile‖NUL‖name leaves `TileHostID` unchanged for primaries (`internal/auth/assettoken.go:182`). | Medium: reverse lookup from label to (tile, name). | Cloud Run's tagged hosts. |

**Recommendation:**
- a) for `/c/` and `/api/`;
- plus e) in origins mode;
- plus the credential for self-calls: the frame-token claim and the terminal
  session's target (P12).

The deployments API must refuse a name whose qualified path is already a
component (NP-04-9). → **P17.**

**Status:** proposed.

## A10 — Event shapes

**Question.** What do events about a non-primary deployment look like on
`/ws/events`, so that old clients are unaffected and new clients can show
them?

Today's consumers:
- The hub's event is `{Type, Component, Text, Topic, Data}`
  (`internal/events/events.go:11-17`).
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
  `deployment` field (the model).
- **b) New types for everything non-primary**, for example one `deployment`
  type with `data.kind`, still carrying `component: "<tile>+<name>"` and
  `deployment`.
- **c) Existing types** with the bare component and a `deployment` field.
- **d) No hub events** for non-primary deployments; the panel polls instead.

**Assessment.**

| Option | UX | Security / isolation | Compat | Cost |
|---|---|---|---|---|
| a) qualified component | New clients reuse today's handlers. | Two leaks. The old shell renders any component's `status`, so a non-primary `xbin.Status` or transient notice shows as a toast in every old shell. And a nested tile's component `apps/cal/widgets/month+dev` is covered by an ancestor frame `apps/cal` in bx-frame and in the iOS app, so a non-primary save reloads the ancestor's primary view. | Old clients ignore `reload` and `build-*` only for tiles with no mounted ancestor. | Low. |
| **b) new types** | New vendor code handles them. The terminal window is `/vendor/` code that ships with the binary (`internal/server/server.go:156-160`). | None: old clients ignore unknown types (rule 10; iOS `.other`). | Additive: one more row in protocol.md. | Low. |
| c) bare component + field | — | Old clients reload the primary's frames, paint non-primary build errors on them and clear the primary's status. | Breaks old clients. | — |
| d) no hub events | The panel has no push updates. | None. | Fine. | A second transport. |

**Recommendation: b)** for everything a non-primary deployment emits.
- The glossary's spelling stays as the payload (`component: "<tile>+<name>"`,
  `deployment: "<name>"`).
- Tile-level changes use the model's `deployments` type.
- Everything about the primary is emitted exactly as today (P5).

This departs from [05-model.md §8](05-model.md#8-events-and-what-clients-see)
(see [Divergences](#divergences-from-the-model)). → **P13, P17**, **NP-04-1.**

**Status:** proposed.

## A11 — Where deployment state lives

**Question.** Where do the deployment record, the checkpoint store, dormant
registrations and edge policies live?

**Assessment.**

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| root `xbin.json` | Sits next to `lifecycle` (`internal/registry/registry.go:231`), an obvious precedent. | Admin terminals can read it. | It is re-marshalled through a struct, so an older binary drops unknown keys on its next grant change (`internal/registry/registry.go:586-595`). Boot must never rewrite it (`test/legacy_workspace_test.go:70-71`). | Low, but downgrades are unsafe. |
| **`data/` (the model)** | Invisible to builders, as operator state should be. | xbind-owned and masked from every terminal (`internal/term/binds.go:49-53`). | `data/` is the backup unit (`docs/protocol.md:2421-2423`). Precedents: `data/cron-jobs.json` and `data/bus-subscriptions.json` (`internal/broker/cron.go:77`, `internal/broker/bussubs.go:161`). | Low. |
| `.xbin/` | — | Masked. | Documented as "derived state — safe to delete when xbind is stopped" (`docs/protocol.md:2417`), while P9 requires pins to survive losing `.xbin/`. Right only for materialized trees and built artifacts. | Low. |
| a key in the tile's `xbin.json` | Visible, and kept in git with the code. | Writable from the tile's terminals, so the primary would move by hand edit, against the D24 precedent. | Every xbind ignores unknown keys (`internal/jsonc/jsonc.go:90-95`), but the key travels with clone, import and templates. Toggling it is a file edit that restarts the backend (`internal/boot/serve.go:178-180`). It is circular for branch feeds, where `xbin.json` differs per branch. | Low, but wrong. |
| refs or notes in the tile repository | Visible to git users. | Writable from the tile's sandboxes. D48 rejected foreign refs in component repositories. | — | Medium. |

**Recommendation:** the record, the checkpoint store, dormant registrations
and edge policies live in `data/`. Materialized trees and built artifacts are
derived, and live in `.xbin/`. No manifest key. → **P5, P15.**

**Status:** proposed.

## A12 — Authority

**Question.** Who may change what the primary runs?

Today:
- `terminal` is a root shell in the tile's directory
  (`docs/auth.md:584-590`), and a save deploys to the primary.
- Lifecycle changes are reserved to tile managers
  (`internal/broker/lifecycle.go:28-30`, via `mayManageTile`,
  `internal/broker/orgsapi.go:427`).

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
| d) humans yes, agents no | Appealing. | Unenforceable. An agent runs in the human's terminal with the same terminal token, a (tile, user) principal (`internal/auth/auth.go:471`, `:499`), and D74 agent sessions reuse the terminal sandbox (`internal/term/agent.go:248`). | — | — | Replit's agent can't touch the primary database, because a platform boundary sits between the human and the agent. xbin has no such boundary. |

**Recommendation: a).** → **P4** (ratified), **P21** (proposed).

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
| D112 registry entry for a non-primary generation | new id + `deployment` field / same id, deployment in the label / a new kind | `main` keeps `backend:<CompKey>:g<gen>`. Non-primary entries get their own id and a `deployment` field; `Filter.Deployment` is additive. | `internal/runner/sbx.go:59-66`; `internal/sbx/sbx.go:50-83` |
| VM budget owner | per deployment / per tile | per tile. `Reserve` already books to the tile path. | `internal/runner/vm.go:80`; `internal/vm/policy.go:132-171` |
| cgroup | today's shared leaf / a flat leaf per deployment / a per-tile parent with one leaf per deployment | per-tile parent. With a shared leaf, one deployment's exit removes the others' leaf. With a flat leaf each, every deployment gets the tile's whole cap, so adding `dev` doubles the tile's memory. The parent is the "nested per-tile cgroup" D113 defers. | leaves are flat `comp-<name>` under one base (`internal/cgroup/cgroup_linux.go:78-94`), removed on exit (`internal/runner/runner.go:614`); the VM leaf is sized for two guests (`internal/runner/vm.go:103-111`) |
| code bind | the checkpoint at its own path / at the canonical path | canonical path: `Src = .xbin/deploy/<CompKey>/<id>`, `Dst = c.Dir` | `internal/runner/runner.go:641` |
| data bind | the deployment's own paths / the primary's paths | the primary's paths, so `XBIN_RES_*` is identical in every deployment | [research/data-plane.md](research/data-plane.md) §6a |
| confine | an extra bind at the same depth, relying on the stable sort / an explicit bind destination | an explicit destination. The stable-sort trick (`internal/sandbox/sandbox.go:122-127`) is implicit behaviour a refactor could break. | `Dir` is bound at itself (`internal/confine/confine.go:218-224`) |
| streaming (deploy-remote rung) | buffered / streaming | streaming, reusing D113's exec-protocol mechanism | `internal/confine/confine.go:87-89` |

Once D113 is built, a tile's managed sandboxes belong to its deployment,
following [05-model.md §12](05-model.md#12-isolation-and-runtimes).

**Degraded modes.**

| Host state | Pause live reload | Non-primary deployments |
|---|---|---|
| isolation off (dev mode, tier 1) | static-only tiles: yes. Tiles with a backend: refused, with the reason. | static-only: yes. With a backend: refused. |
| isolation on, a `vm` backend, VMs unavailable | refused like any VM start today; the reason goes into D112's failure ring | the same |
| `runtime: "cgi"` | refused until cgi runs sandboxed (A14) | refused |

**Recommendation: a)**, with the shared mechanics above. → **P18.**

**Status:** the shared mechanics are the owner's direction (2026-09-27). P18
is proposed.

## A14 — cgi, chrome and governance tiles

**Question.** Which tiles are excluded from pausing live reload, or from
having non-primary deployments?

**cgi.**
- `serveCGI` runs `backend/handler` through `net/http/cgi`, as xbind on the
  host, even under `--isolate` (`internal/proxy/proxy.go:239-261`).
- There is no long-running backend to pin (`internal/runner/runner.go:191-193`).

| Option | Assessment |
|---|---|
| run the handler from the materialized checkpoint, on the host | Widens an existing D78 violation ([research/side-findings.md](research/side-findings.md) #1). Rejected. |
| **exclude until cgi is sandboxed (the model)** | Safe. The separate, urgent cgi fix then lets cgi run per request from the checkpoint, inside the sandbox. |
| fix cgi first, then include | The right order, but not this feature's work package. |

**Chrome and governance tiles.**
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

**Recommendation:** cgi is excluded (P18). Chrome tiles and tiles holding
`xbin`/`xbin:*` capabilities may pause live reload, but may not have
non-primary deployments (P19).

**Status:** proposed.

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
| the per-edge record `edges: {"<edge>": "read"\|"block"}` | P3 | one more value |
| one resolver, `resolveTarget(caller, callerDeployment, target, edgePolicy)`, used by the HTTP proxy, stream forwards, bus-subscription resolution and `res:` namespace resolution | the read clamp | picks the provider's deployment of the caller's name |
| a deployment on `Principal` | P12 (self-calls) | carries the caller's name to the resolver |
| `Runner.Ensure(comp, deployment)` | non-primary backends | a non-primary target |
| `X-XBin-Deployment` on non-primary calls | P17 | the same header, on both sides |

**What `match` needs beyond the seams.**
- **A rule for a missing name** (table below).
- **An exception to P7.** A non-primary deployment receives calls from other
  tiles' same-named deployments whose edge policy is `match`.
- **No clamp.** The target is itself a non-primary deployment, inside the
  accident boundary. The authority is still the tile's grant, unchanged
  (P11).
- **Data.** `match` on a `res:` edge resolves to the provider scope's
  namespace of the same name, and refuses if no tile in that scope has the
  deployment.
- **Streams.** The consumer's forward map is computed per spawn, so
  `DialInto(prov, name)` is a small change
  ([research/inbound-edges.md](research/inbound-edges.md) E9).
- **Net splices** are the only real redesign. They need rosters keyed by
  (client, deployment), with addresses that don't depend on the sorted index
  (`internal/broker/netfn.go:182-191`, `:223-249`;
  `internal/runner/netmux.go:22-32`). Because v1 blocks provider splices for
  non-primary deployments (A5), nothing in v1 depends on index-derived
  addresses.

**Missing-name options.**

| Option | Assessment |
|---|---|
| **refuse (proposed)** | Safe and explicit. The error names the missing deployment. |
| fall back to the primary, read-clamped | Tolerable as a separate, explicit value later. As the meaning of `match` it would hide a miswired deployment. |
| fall back to the primary with full rights | Leaks non-primary effects into the primary. Rejected. |
| a choice per binding | Belongs to the per-edge record, if ever wanted. |

**What v1 must do now:**
- Every edge-policy value it doesn't know reads as `block`, so a downgrade
  after `match` ships fails closed (NP-04-2).
- The resolver is the only place that picks a target deployment.
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

**Recommendation:** `match` on the same record. A missing name is refused,
and P7 gains its exception when `match` ships (NP-04-7).

**Status:** `match` later, on the same per-edge record, is ratified (P3). The
missing-name rule and the P7 exception are proposed.

## A17 — The principal model

An axis the list above leaves implicit. P11, P12 and P20 rest on it.

**Question.** Is a deployment its own principal?

| Option | UX | Security | Compat | Cost |
|---|---|---|---|---|
| **(i) one principal per tile, with a deployment attribute (the model)** | Tile code is unchanged: `xbin.self`, `XBIN_COMPONENT` and `res:${xbin.self}` all still name the tile. | Grants, bindings, ceilings and ownership apply unchanged. The risk is a self-scoped handler nobody converted: it writes the primary's state. That fails open for isolation, not for privilege. Countered by NP-04-8. | The zero value means the primary, so every existing principal behaves as today. | Moderate: about 15 self-scoped handlers ([research/serving-fabric.md](research/serving-fabric.md) B.7). |
| (ii) deployment-qualified principals (`apps/x@dev`) | Every store separates automatically. | Ceilings fail open: owner-derived rows look up `owners[path]` (`internal/users/orgs.go:1210-1215`), and a qualified name has no owner entry. | `/api/${xbin.self}` resolves by longest prefix (`internal/registry/registry.go:519-536`), and vault URLs derive from `Self()`, so both break. There is no free separator: `@` is legal in paths and is the allowance separator. | High, and the risk is to privilege. |
| (iii) separate tiles (A1's option 0) | Everything is set up again for each twin. | The only real trust boundary. | None. | None. |

**Recommendation: (i)**, with non-primary deployments as an accident boundary
rather than a trust boundary. → **P11, P12, P20**, **NP-04-8.**

**Status:** proposed.

## Open questions

These go to `16-open-questions.md`.

1. **Nested components under a parent's deployment URL.** What does
   `/c/<parent>+<name>/<child>/` serve? The parent's checkpoint excludes the
   child. The proposal is that the path resolves to the child's primary, as
   the bare URL does, so a parent page's relative references keep working.
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
| A1 model | pointers over a checkpoint store; tracked branch and deploy remote as later feeds; twin tiles unchanged | P1 | ratified |
| A2 checkpoint mechanism | private confined git store; gitignore doesn't apply; `.git` and nested components excluded; fidelity limits documented; caps | P1, P9, P16; NP-04-4 | ratified (store); proposed (mechanism) |
| A3 pausing live reload | pin to a checkpoint; checkpoints on demand; resume deploys the work tree; a deploy onto the target pauses live reload | P8, P9; NP-04-6 (later) | proposed |
| A4 storage keys | follow the deployment; `main` owns today's keys; reassignment moves routing only | P6, P7 | proposed |
| A5 fabric | F2: providers' primaries, read-clamped, per-edge `block`; unclampable edges `block`; net egress inherited | P3; NP-04-2, NP-04-3 | ratified; proposed (defaults) |
| A6 registrations | dormant; deliveries switch; run now; notifications never pushed | P13 | proposed |
| A7 vault | separate, key names as placeholders; tile-manager vault copy | P14 | proposed |
| A8 seeding | empty; tile-manager seed with PII warning; reset | P14 | proposed |
| A9 URL | `+` suffix for `/c/` and `/api/`; an origin per deployment in origins mode; `deployment` as its own wire field | P17; NP-04-5, NP-04-9 | proposed |
| A10 events | new event types for everything non-primary; `deployments` for tile-level changes | P13, P17; NP-04-1 | proposed |
| A11 state | `data/`; `.xbin/` only for derived trees and artifacts; no manifest key | P5, P15 | proposed |
| A12 authority | parity plus an optional protected primary | P4, P21 | ratified; proposed (P21) |
| A13 isolation | required for pinned and non-primary backends; the shared mechanics (registry, VM books, per-tile cgroup parent, launch binds, confine destination) | P18 | proposed; owner's direction |
| A14 cgi, chrome | cgi excluded; chrome and `xbin`-capable tiles may pause live reload, with no non-primary deployments | P18, P19 | proposed |
| A15 naming | tile deployment; `main` (name) and primary (role) | P2 | ratified |
| A16 parallel fabric | `match` on the same record; a missing name is refused; P7 exception | P3; NP-04-7 | ratified; proposed (rule) |
| A17 principal | one principal plus a deployment attribute; an accident boundary | P11, P12, P20; NP-04-8 | proposed |

## Divergences from the model

1. **Events (§8): a qualified component on existing types doesn't keep old
   clients unaffected.**
   - *Evidence:* the old shell renders a status entry or toast for any
     `component` (`workspace-template/shell/bx-shell.js:1257-1263`).
     bx-frame and the iOS app treat any `<ancestor>/…` component as their
     own (`web/bx-frame.js:368`, `web/events-socket.js:40-42`,
     `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`).
   - *Effect:* a non-primary `status` shows as a toast in every old shell,
     and a nested tile's non-primary save reloads its ancestor's primary view
     in bx-frame and in the shipped app. §8's "a dev build error can never …
     reload a shipped iOS app" doesn't hold for nested tiles.
   - *Fix:* non-primary deployments emit only new event types, keeping the
     glossary's payload fields (NP-04-1).
2. **Cross-tile references (§6) don't all "follow the other tile's primary".**
   - *Evidence:* confined Go builds bind the whole workspace read-only and
     resolve `go.work` `use` entries to the other tile's directory, i.e. its
     work tree (`internal/runner/build.go:51-56`, `:85-87`;
     `internal/deps/deps.go:120-146`).
     `deps/` links are relative links into that same directory
     (`internal/deps/deps.go:59-69`), and the static plane doesn't follow them
     out of a checkpoint at all (P16). Only absolute `/c/<other>/…` imports
     reach the other tile's primary.
   - *Fix:* restate §6. `/c/` imports resolve to the other tile's primary at
     serve time. Build-time references resolve to its work tree, so
     `07-runtime.md`'s reproducibility caveat must name them. Frontends of
     tiles that pin should import other tiles by absolute `/c/<other>/` URL.
3. **The read clamp's "never writes" (glossary, §7) holds only for brokered
   resources.**
   - *Evidence:* for tile-to-tile HTTP, xbind only authorizes the call and
     passes the role in `X-XBin-Role` (`internal/proxy/proxy.go:160-173`).
     Endpoint-level role checks are the provider's code.
   - *Fix:* state it that way. `06-security.md` lists the residual (a
     provider that ignores roles), and `block` is the tile manager's control
     for such providers. See also open question 4.
4. **`match` needs an exception to P7** (§7 and §15 say it plugs in "without
   redesign").
   - *Evidence:* `match` routes calls into a non-primary deployment, while P7
     says only the primary receives inbound edges.
   - *Fix:* word P7 now as "every inbound edge resolves through one resolver,
     which returns the primary for every v1 policy value", and add the
     `match` exception with M3 (NP-04-7).
5. **Pausing live reload is a "code-identical swap" (§5) only within the
   checkpoint's fidelity limits.**
   - *Evidence:* a checkpoint drops `.git`, empty directories, other
     permission bits, xattrs and special files (A2's table), while the process
     it replaces ran from the full work tree (`internal/runner/runner.go:641`).
     A backend that reads its own `.git`, or expects an empty directory, sees
     a different tree once live reload is paused.
   - *Fix:* say "file-identical within the checkpoint's fidelity limits", and
     have `07-runtime.md` list what differs.
6. **"Non-primary deployments inherit the tile's network" (§7) can't hold for
   a net slot bound to a provider tile.**
   - *Evidence:* provider links are keyed by (provider, client path), and
     registering a link closes any earlier fd for the same key
     (`internal/runner/netmux.go:22-32`). Link addresses come from the
     client's index in a sorted roster (`internal/broker/netfn.go:182-191`,
     `:212-216`, `:239-249`). A second deployment of the client would either
     share the primary's link and split its packets, or need a roster entry
     that renumbers every client and restarts the provider.
   - *Fix:* inherit egress for internet, LAN, set, org and personal nets;
     `block` (relay-only egress, or none) for provider splices and
     lan-ingress (NP-04-3), until `match` brings per-deployment rosters
     (A16).

## New proposals

- **NP-04-1 — Non-primary deployments emit only new event types** (A10).
  - They never emit `reload`, `build-*` or `status`. The payload keeps
    `component: "<tile>+<name>"` and `deployment: "<name>"`.
  - The primary's events are unchanged. `11-contract.md` names the types.
- **NP-04-2 — An unknown edge-policy value reads as `block`** (A5, A16).
  v1 must fail closed on values it doesn't know, so a downgrade after
  `match` ships can't widen access.
- **NP-04-3 — Defaults for unclampable edges** (A5), for `09-fabric.md` to
  adopt:
  - a custom role with no path to `reader`, stream interfaces, lan-ingress
    links and net-provider splices default to `block`, and refuse `read`;
  - net egress to the internet, the LAN or net sets inherits the tile's
    policy (D54).
- **NP-04-4 — Checkpoint caps and a per-tile exclude list** (A2).
  - Over a cap, a capture is refused with the largest directories named.
    It is never truncated.
  - Terminal-level users can set an exclude list, kept in the deployment
    record and shown in the deploy and promote dialogs. It is never a
    work-tree file, which would add a builder-facing convention and travel
    with clones.
- **NP-04-5 — The deployment is its own wire field** (A9). The
  `<tile>+<name>` form appears only in URL paths, `bx` arguments, display
  text and the event `component`. Query parameters and JSON bodies carry
  `deployment` separately, because an unencoded `+` in a query decodes to a
  space.
- **NP-04-6 — Candidate builds while live reload is paused** (A3, later).
  - Saves build a candidate without activating it, and build errors show in
    the terminal window.
  - Reload now activates the candidate if the work tree's tree hash still
    matches. Activation semantics don't change.
- **NP-04-7 — `match` refuses a missing name, and P7 gains an exception**
  (A16).
  - `match` resolves to the provider's same-named deployment, or refuses. It
    never falls back to the primary.
  - When `match` ships, P7 admits inbound calls into a non-primary deployment
    only from same-named deployments whose edge policy is `match`.
- **NP-04-8 — Default-deny for non-primary principals on xbind writes** (A17).
  Every `/api/xbin/*` write from a non-primary principal is refused unless it
  is on an explicit allow-list of converted handlers. A guard test in the
  style of `TestNoDirectExec` enforces the list.
- **NP-04-9 — Deployment names can't shadow components** (A9).
  - The deployments API refuses name `n` on tile `t` when `t+n` is an
    existing component.
  - The registry reports a component created later at `t+n` as shadowing
    that deployment's URL.
