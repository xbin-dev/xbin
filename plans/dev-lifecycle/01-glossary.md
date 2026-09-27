# 01 — Glossary: the words of the dev lifecycle

> Status: live — the vocabulary every document in
> [plans/dev-lifecycle/](README.md) uses verbatim, and that UI copy, `bx`, the
> wire protocol and builder docs inherit. Proposed decision P2 (tile
> deployment) was ratified by the owner on 2026-09-27. Evidence for every
> collision named here: [research/terminology-census.md](research/terminology-census.md).

## Why this document exists

xbin already has a dense vocabulary, and most obvious names are taken:

- "identity" is the tile path;
- "instance" is an interface instance;
- "environment" is the `setup` layer;
- "pause" is lifecycle disable;
- "snapshot", "version", "revision" and "release" each name something else.

A feature that says "the dev identity" or "pause the tile" would contradict
the documents a builder or a coding agent already trusts. This glossary fixes
one meaning per word before anything is designed around it.

Rules for every document in this set, and for the implementation that
follows:

1. **Use the defined terms verbatim.** Don't use synonyms in normative text.
2. **Qualify "deployment" as "tile deployment"** wherever the text could be
   read as the xbin install (operator docs, README, website). Inside this
   design set, bare "deployment" means tile deployment.
3. **Keep TAKEN words in their existing meaning.** Never repurpose one (see
   [Words this feature must not use](#words-this-feature-must-not-use)).
4. **Keep the default claim true:** a tile that never opts in has "no deploy
   step" and live reload on save, exactly as the product pitch says.

## The owner's words, mapped

| The owner said | Defined term | Note |
|---|---|---|
| hot reload | **live reload** | "Live reload" is the documented term (docs/elements.md, AGENTS.md). "Hot reload" appears only three times. It covers both the frame reload and the backend rebuild/swap. |
| pause hot reload | **pause live reload** | Never "pause the tile": `bx disable` is documented as "pause/resume a tile", and ⏸ is the Disable menu item. |
| reload-now button | **reload now** | |
| identity, sub-deployment | **tile deployment** | "Identity" is the tile path and the principal. A deployment deliberately shares the tile's identity. |
| the `main` identity is special | **`main`** (the name) and **primary** (the role) | `main` is the first deployment and owns today's storage keys. The primary is whichever deployment receives inbound traffic. They coincide unless the primary is reassigned. |
| assign which identity is main | **reassign the primary** | A routing change only. Data does not move. |
| code of an identity | **checkpoint** | |
| deploy current code to an identity | **deploy** (the work tree, to a deployment) | |
| promote code from one identity to another | **promote** | Code only. |
| identity = git branch; hot reload watches branches | **tracked branch** (a feed) | A later rung. |
| blessed runtime-injected remote | **deploy remote** (a feed) | A later rung. |
| multi-identity mode | **a tile with deployments** | There is no mode switch. A tile has deployments as soon as it has more than the implicit `main`, or any deployment state at all (for example, paused live reload). |
| resources separate per identity | **deployment data** | |
| bindings/caps per tile | **tile authority** (unchanged) | |
| parallel binding fabric | **name matching** (edge policy `match`, deferred) | |

## Definitions

Grouped by object. Wire and UI spellings are in [Spellings](#spellings).

### The tile and its code

**Tile.** Unchanged. It is a directory whose workspace-relative path is its
identity (docs/elements.md). It is also a principal. It holds authority
(grants, bindings, capabilities), an owner, access entries, policy ceilings
and a lifecycle state. Everything in this design hangs off a tile, and none of
it changes what a tile is.

**Work tree.** The tile's directory as terminals, coding agents and xbind's
own writers (builtin updates, restore, PR application by the tile's plane)
change it. It is mutable and live. This is git's "work tree" sense, already
used by the Code panel and by D77, so the term is safe to extend. The tile's
own `.git` repository sits inside the work tree and is **untrusted**: it is
writable from the tile's sandboxes (D77, D78).

**Checkpoint.** An immutable, content-addressed capture of a tile's files,
taken at a moment in time. It is what a pinned deployment runs.
- **What it contains:** every file under the tile directory except `.git`
  directories and nested components. Gitignore rules do not apply. Symlinks
  are kept as symlinks; the exec bit is kept.
- **What it loses:** empty directories, other permission bits, xattrs and
  special files. These are the fidelity limits of the storage format; see
  `05-model.md`.
- **Identity:** a checkpoint is named by a short id derived from its content
  hash, e.g. `c:3f2a1c9`.
- **Where it comes from:** a *feed*.
- **Why the word:** the census found it effectively unused, and it reads as
  "a state you can return to". That is compatible with CM-2/BU-5's "checkpoint
  commit" wording.

**Checkpoint store.** The per-tile, xbind-owned repository holding a tile's
checkpoints and deploy log. It lives under `data/`: backed up, masked from
every terminal, never inside the work tree. Only confined tools read or write
it (D78). It is not the tile's own git repository.

**Feed.** Where a new checkpoint comes from:
- the **work tree**, the default and the only v1 feed;
- a **tracked branch** of the tile's repository (later);
- a push to the **deploy remote** (later).

The feed is recorded on the checkpoint.

**Tracked branch** (later rung). A deployment may follow a named branch of the
tile's own repository. Each new commit on that branch, observed and read
inside confine, becomes a checkpoint and is deployed. "Track" is git's
upstream sense. Bare "tracked" already means builtin-update provenance, so
always say "tracked branch".

**Deploy remote** (later rung). A git remote that xbind serves and injects
into the tile's terminals: the `template` remote precedent, via
`GIT_CONFIG_COUNT`. Pushing a deployment's ref to it creates a checkpoint and
deploys it. Every receive runs confined.

### Deployments

**Tile deployment** (in this set: **deployment**). A named, long-lived runtime
of one tile. It has:
- a **code pointer**: either live reload (it runs the work tree) or a
  checkpoint (it is *pinned*);
- its own **deployment data** (scope resources and vault), backend
  process(es), frontend URL, logs, status and **dormant registrations**.

A deployment has **no authority of its own**. It acts with its tile's grants,
bindings and capabilities, narrowed for non-primary deployments by the edge
policy. It is not a principal, not an identity and not a new tile.

**`main`.** The reserved name of a tile's first deployment. Every tile has it
implicitly, with no configuration. `main` owns the storage keys the tile uses
today: data, vault, log, build output and registrations. Enabling
deployments therefore never migrates production data. `main` cannot be
deleted.

**Primary.** The role held by exactly one deployment of a tile (by default
`main`). The primary is the only deployment that receives **inbound edges**:
- the bare URLs `/c/<tile>/` and `/api/<tile>/`;
- consumers' interface bindings, grants to call the tile, and ingress routes;
- cron ticks, bus push deliveries, event triggers and alwaysOn;
- notify links, the native app, and backups of record.

The role can be **reassigned**. That is a routing change: each deployment
keeps its data. The word was free in the census (only UI button roles use
it).

**Non-primary deployment.** Any deployment other than the primary. It is
reachable only at its **deployment URL**, by:
- humans with at least `write` on the tile;
- the tile's own terminals and agent sessions that target it;
- its own self-calls.

It receives no inbound edges. Its registrations are dormant unless
**deliveries** are on.

**Deployment name.** The key of a deployment within its tile. It is lowercase
letters, digits and `-`, starts with a letter, and has at most 24 characters.
Names are immutable. They key storage, so renaming would be a data migration
(the D15 precedent). `main` is reserved. There is no `/`, so git branch names
map to deployments explicitly (tracked-branch config), never by equality.

**Deployment URL.** `/c/<tile>+<name>/` for a deployment's frontend and
`/api/<tile>+<name>/…` for its backend. `+` is the **deployment qualifier**.
It is the one punctuation character the census found free in grammar and URL
positions. It is reserved in new tile names the D82 way: an exact match on an
existing component wins, so existing directories keep resolving. The bare URL
always means the primary. `<tile>+<primary name>` also works.

**Target deployment.** The deployment that a terminal or agent session's own
calls and `bx` commands address by default. It defaults to the live reload
target, or the primary when live reload is paused. It is exposed to the
session as `XBIN_DEPLOYMENT`, set only when the target is not the primary.

### Live reload and moving code

**Live reload.** Unchanged in meaning: a save in the work tree reloads frames
and rebuilds/swaps the backend. What is new is that live reload is
**attached** to exactly one deployment (by default `main`), or to none. The
attached deployment is the **live reload target**. It runs straight from the
work tree, as every tile does today, so the default path gains no per-save
cost. At most one deployment is attached, because there is one work tree.

**Paused (live reload).** The tile state in which live reload is attached to
no deployment. Saves change the work tree and nothing else. Every deployment
is pinned. The former target is pinned to the checkpoint taken at the moment
of pausing. The terminal window shows how far the work tree has moved since.

**Pinned.** A deployment that is not the live reload target runs a
checkpoint, and is pinned to it. A pinned deployment's code changes **only**
by an explicit deploy, promote, roll back or reload now. That holds through
every restart: idle reap, crash, grant change, xbind restart, cache loss. "Pin"
already means "fix to a version" elsewhere in xbin (a terminal layer pinned to
its base image, release pinning), and this is the same sense. Always say
"pinned to <checkpoint>".

**Resume (live reload).** Re-attach live reload to a deployment, normally the
one it left. The deployment switches to the work tree at once. That is itself
a deploy, with the usual build and health check.

**Reload now.** While live reload is paused: checkpoint the work tree and
deploy it to the deployment live reload was last attached to, once. That
deployment stays pinned.

**Deploy.** Put code on a deployment. The source is a checkpoint, most often a
fresh checkpoint of the work tree. A deploy goes through the existing
blue/green path: build if needed, start, health check, swap, drain (D8). On
failure the deployment keeps running its previous code. Deploying onto the
live reload target pauses live reload, because a deploy that the next save
silently overwrote would be a lie (the prior-art "rollback implies pause"
rule).

**Promote.** Deploy deployment A's current code to deployment B. If A is the
live reload target, a fresh checkpoint of the work tree is taken first. A
promotion moves **code only**. B keeps its data, vault, dormant registrations
and routing. Everything bound late (resource env, interface URLs, secrets) is
resolved for B when B starts.

**Roll back.** Deploy an earlier checkpoint from a deployment's **deploy log**
back onto it. Like any deploy onto the live reload target, it pauses live
reload.

**Deploy log.** A deployment's ordered history of the checkpoints it has
run. Each entry records who deployed it, when, from which feed, and how
(deploy / promote / roll back / reload now / resume). It is kept in the
checkpoint store.

### Data, secrets and background work

**Deployment data.** The namespace of scope resources (kv, sqlite, blob,
filesystem, bus) and vault entries a deployment sees.
- `main` sees today's namespace.
- Every other deployment sees its own namespace, which starts **empty**.
- In a scope shared by several tiles, same-named deployments of sibling tiles
  share that scope's namespace for the name. Resources belong to scopes, not
  tiles.

**Seed.** Copy the primary's deployment data into a non-primary deployment's
namespace. This is explicit, gated to tile managers, and carries a PII
warning. It is never automatic.

**Reset.** Empty a non-primary deployment's data.

**Vault copy.** Copy selected vault values from the primary into a
non-primary deployment's vault. This is explicit and gated to tile managers.
A new deployment's vault starts with the primary's key **names** only, as
placeholders.

**Dormant registration.** A cron job, bus push subscription, interface
instance or ingress host registered by a non-primary deployment. It is stored
under that deployment and **not active**: nothing fires or routes. Registering
still succeeds, so tiles that register at startup keep working. The
deployment panel lists dormant registrations, with a manual **run now** for
scheduled handlers.

**Deliveries** (a per-deployment switch). When on for a non-primary
deployment, its dormant registrations become active for that deployment:
cron ticks and bus deliveries reach it. It is off by default and gated to tile
managers. "Deliveries" is D85's term for cron and bus POSTs. alwaysOn is a
separate per-deployment switch, also off for non-primary deployments.

### The outbound fabric

**Edge** (outbound edge). One way a tile reaches beyond itself:
- an **interface binding** (slot → provider);
- an **assigned grant**: a call to another tile, a cross-scope `res:`, a bus
  subscription;
- the **net slot**.

docs/overview/06-authorization.md already calls a cross-scope grant an
"edge", in the same sense. Edges belong to the tile.

**Edge policy.** Per tile and per edge, how the tile's **non-primary**
deployments may use that edge:
- **`read`** (the default): the call goes to the provider's primary, with the
  role clamped to `reader`;
- **`block`**: no access.

A later value, **`match`**, routes to the provider's same-named deployment:
the parallel fabric. The primary always uses edges exactly as today; the edge
policy never touches it. Edges that cannot be read-clamped (a custom role, a
raw stream) have their own defaults, set in `09-fabric.md`.

**Read clamp.** The narrowing applied under `read`: the effective role on the
provider becomes `reader` when the grant or binding carries a role that
implies it. A non-primary deployment never writes into another tile's primary
in v1.

**Name matching** (deferred). The parallel binding fabric: a non-primary
deployment's edge resolves to the provider's deployment of the same name. For
example, `apps/crm+dev` calls `apps/calendar+dev`. The edge policy record,
with its future `match` value, is the seam that adds it later.

### Governance

**Tile managers.** Unchanged (D24/D33): the tile's user-owner, the owning
org's admins, and workspace admins. They alone may:
- seed data and copy vault values;
- turn on deliveries or alwaysOn for a non-primary deployment;
- reassign the primary, and protect it.

**Protected primary.** A per-tile switch, set by tile managers. While it is
on, only tile managers may deploy, promote, roll back, reload now or resume
onto the primary. Without it, terminal-level users and their agents may, as
saving does today: **parity** (P4, ratified).

**Accident boundary.** What a non-primary deployment is. It keeps untested
code away from production data and traffic when everyone involved is already
trusted with the tile's code. It is **not a trust boundary**. Running code
written by non-writers (for example, a PR preview) would need separate
principals, and is out of scope.

## Spellings

| Concept | UI copy | `bx` | Wire / env | Notes |
|---|---|---|---|---|
| live reload on / off | "Live reload: main" / "Live reload paused" | `bx live-reload` | `liveReload: "<name>" \| ""` | never "pause the tile" |
| pause / resume | "Pause live reload" / "Resume live reload" | `bx live-reload pause\|resume <tile>` | | |
| reload now | "Reload now" | `bx live-reload now <tile>` | | |
| deployment | "Deployment", panel "Deployments" | `bx deployment ls\|add\|rm` | `deployment: "<name>"` | "tile deployment" in operator and pitch copy |
| primary | "primary" badge | `bx deployment primary <tile> <name>` | `primary: "<name>"` | |
| deploy | "Deploy to <name>" | `bx deploy <tile> --to <name>` | | |
| promote | "Promote <a> → <b>" | `bx promote <tile> <a> <b>` | | |
| roll back | "Roll back to <checkpoint>" | `bx deploy <tile> --to <name> --checkpoint <id>` | | |
| checkpoint | "checkpoint c:3f2a1c9" | `--checkpoint <id>` | `checkpoint: "<id>"` | |
| pinned | "pinned to c:3f2a1c9" | | | |
| deployment URL | | | `/c/<tile>+<name>/`, `/api/<tile>+<name>/` | |
| target deployment | "target: dev" in the terminal bar | honours `XBIN_DEPLOYMENT` | `XBIN_DEPLOYMENT` (non-primary only) | |
| deployment marker to callees | | | `X-XBin-Deployment: <name>` (non-primary only) | |
| events for non-primary | | | `component: "<tile>+<name>"`, `deployment: "<name>"` | old clients' prefix match ignores them |
| deployment data | "Data: empty / seeded from main" | `bx deployment seed\|reset` | | |
| dormant registrations | "dormant" | | `dormant: true` | |
| deliveries | "Deliveries: off" | `--deliveries on\|off` | `deliveries: bool` | |
| edge policy | "Non-primary access: read / block" | `bx deployment edge <tile> <edge> read\|block` | `edges: {"<edge>": "read"\|"block"}` | |
| protected primary | "Protected" | `--protect` | `protectedPrimary: bool` | |

The command names above are the glossary's proposal. `11-contract.md` owns
the final shapes and may refine them, but not the nouns.

Glyphs:
- Pause live reload must not use ⏸, which is Disable.
- Reload now must not use ⟲, which is reset the terminal layer, or ⟳, which
  is refetch the admin panel.
- `10-ux.md` picks the glyphs.

## Words this feature must not use

Each of these already means something builder-facing. Using it for a
deployment concept would contradict existing docs.

| Word | Why not (existing meaning) |
|---|---|
| identity | The tile path ("the path is the identity"), principals, the auth chapter, the website pitch. A deployment deliberately is **not** an identity. |
| principal | Auth principals. Deployments share the tile's principal. |
| instance | Template instances (D50), interface instances (`apps/x#dev` = "the dev instance", D32), backend instance tokens. |
| environment, env | The component env layer (`setup`), terminal env, env vars. |
| stage, staging | Free, but git's staging area confuses agents. It was offered as an alternative and not chosen. |
| slot | Interface slots. Azure's "deployment slots" would collide head-on. |
| version | Backup archive versions (`bx restore --version`), builtin versions, xbind version. |
| revision, rev | D55 screen revisions, native primitive revisions, git revs. |
| release | xbin's releases, and the compat contract's unit of time. |
| snapshot | Backup snapshots, builtin base snapshots, the template snapshot, D77 agent snapshots. Use "checkpoint". |
| build | Keep the existing sense only (each deploy may build). Never call a checkpoint "a build". |
| generation | Backend generation (blue/green) and credential generation (D93). Deployments have generations; they are not generations. |
| layer | The terminal dev layer and the env layer. |
| preview | `bx preview`, `?preview=1` (frozen in compat), `/owner/preview`. |
| channel | D86 chat channels. |
| lane | The agent template's private and web lanes. |
| live (as a noun) | Live reload, the live tile API, Live Activities. "The live deployment" would clash with live reload attached to a non-primary deployment. |
| pause (unqualified) | Lifecycle disable. Always "pause live reload". |
| freeze, frozen | Compat's "frozen URLs". Use "pinned". |
| swap | Blue/green generation swap. "Reassign the primary", never "swap deployments". |
| source | Tile source code, and the ingress `source` of an exposed slot. Use "from". |
| copy, fork, clone | A new tile at a new path. |
| publish | Ingress publishing, bus publish, D55 screen publishing. |
| prod, production | xbind's own tier-3 production mode. Say "the primary". |
| dev (as vocabulary) | The dev layer, `--dev`, devbox, and the vault dev key. Fine as a **deployment name** users pick (data), never as product vocabulary. |
| origin | Tile origins (D95), CORS origins, git `origin`. |
| automation | Unattributed automation in the auth docs, and the agent template's Automations. Use "deliveries". |

## Words kept in their existing meaning (safe to use as-is)

- **branch**, **main**, **HEAD**, **remote**, **work tree**: git's senses.
  (Tile repos are created with `init -b main`, but imported repos keep their
  own default branch. Never assume a tracked branch called `main`.)
- **promote**: previously used only for "promoting" a user to admin, a
  different object.
- **build**, **generation**, **swap**: runner mechanics. A deploy triggers a
  build and a generation swap.
- **edge**: the authorization overview's sense, extended to outbound edges.
- **sandbox**: every meaning stays as is — a backend's, terminal's or agent's
  sandbox, the D112 registry's entries, and D113's tile-managed sandboxes. A
  deployment is **not** a sandbox. Its backend generations run in sandboxes,
  listed in the registry under the tile with their deployment named.
- **rollback / roll back**: free, and compatible with backup restore's
  "without a full rollback".
