# Updating builtin components in existing workspaces — design

Builtins ship *embedded in the buxond binary* and are copied into a workspace
once — the scaffold at `init`, tiles at `bx tile import`. After that the copy is
the user's: they edit it (a buxon workspace is self-modifying by design). So
today a newer buxond carries newer builtins (the 700px column fix, the drag
fixes, the Tile Manager "New from template" tab, updated `AGENTS.md`, a better
`llm-gw`…) but **existing workspaces never see them** — `init` and import both
refuse to overwrite, and nothing records where a file came from.

This plan adds: **provenance + versioning** for copied builtins, **update
detection**, and an **apply** step that respects local customizations by letting
the user **replace** or **merge** (3-way) rather than clobbering their edits.

## What is a "managed builtin"

Update tracking covers the things buxon *authored and copied in*:

- **Scaffold** — everything seeded from `workspace-template/` at init:
  `shell/`, `root/`, `tiles/manager`, `tiles/admin`, `apps/welcome`,
  `AGENTS.md`, workspace `buxon.json`, `gitignore`. This is where most UX
  features land, and where customization is most common (people restyle the
  shell). Highest-value target.
- **Imported builtin tiles** — `builtin-tiles/*` brought in via `bx tile import`
  (llm-gw, chat, …), possibly renamed with `as <path>`.

Explicitly **out of scope**:

- **Template instances** (plans/templates.md). Instantiating a template is a
  *fork* — `apps/support-agent` is meant to diverge freely. Updating the `agent`
  template must **not** touch existing instances; it only changes what the
  *next* instantiation produces. (An instance is a normal user component; it
  gets no origin marker.)
- **User components** — anything the user made. Untracked, never touched.

## Provenance & versioning

Two pieces of new state, both under the already-gitignored `.buxon/` (D2 —
per-deployment runtime state, like the build cache and logs):

1. **Origin manifest** — `.buxon/builtins.json`: for each managed builtin,
   what it is and the *base* (as-installed) content, so we can tell
   "customized locally" from "stale":

   ```jsonc
   {
     "shell": {                       // component path in the workspace
       "source": "scaffold:shell",    // scaffold:<path> | tile:<name>
       "installPath": "shell",        // = source path unless imported "as"
       "version": "3",                // human-facing, from the catalog (below)
       "hash": "sha256:…",            // rollup over the base file set
       "files": { "bx-shell.js": "sha256:…", "index.html": "sha256:…", … }
     },
     "apps/llm-gw": { "source": "tile:llm-gw", "installPath": "apps/llm-gw", … }
   }
   ```

2. **Base snapshot** — `.buxon/builtins/<id>/…`: the exact bytes we last
   installed. Kept so a 3-way merge has a real *base* to diff against (the
   origin manifest's hashes only *detect* change; a merge needs the content).
   Cheap (a few KB per component) and disposable — regenerable from the marker
   + embed on any clean file.

**Version signal.** The primary "is there an update" test is a **content
hash**: the embedded builtin's rollup hash vs the installed `hash`. This needs
zero maintenance — no one has to remember to bump a number when they edit
`bx-shell.js`; the hash changes automatically at build time. On top of that,
each builtin carries a small **human `version`** (a monotonic integer) + a
one-line changelog in its catalog metadata, purely for display and release
notes ("shell v2→v3: 700px columns, drag fixes"). The hash decides *whether*;
the version communicates *what*.

- Tiles already have `tile.json` — add `version` + `changelog` there.
- Scaffold components have no `tile.json`; ship a tiny sibling
  `builtin-tiles`-style catalog for the scaffold (a generated
  `scaffold.json` embedded next to `workspace-template/`, or per-component
  `.builtin.json` markers stripped on seed) that carries `version`/`changelog`.
  The hash is computed from the embedded FS regardless, so `version` is
  optional sugar.

## Update detection

buxond computes, per managed builtin, a **per-file 3-way status** from three
hashes — `base` (origin manifest), `ours` (the workspace file now), `theirs`
(the embedded file now):

| ours vs base | theirs vs base | status | on "update" |
|---|---|---|---|
| same | same | up to date | — |
| same | changed | **clean** | fast-forward: write `theirs` |
| changed | same | user-only edit | keep `ours` |
| changed | changed, `ours==theirs` | converged | just refresh base |
| changed | changed, differ | **conflict** | replace or 3-way merge |
| (file added upstream) | — | **new file** | write it |
| (file deleted upstream) | — | **removed** | offer to delete |

A builtin "has an update" if any file is `clean`, `new`, `removed`, or
`conflict`. The Tile Manager and `bx` show the rollup plus the conflict count,
so the user knows up front whether it's a safe fast-forward or needs attention.

## Applying an update

Per builtin (or, for fine control, per file), the user picks:

- **Replace** — overwrite with the embedded version, discarding local edits to
  the conflicting files. Clean files fast-forward regardless. Safe because the
  workspace is a git repo (D2): the pre-update state is one `git checkout` away,
  and buxond can auto-`git add`/commit a "builtin update: <id> v2→v3" checkpoint
  first (opt-in) so the diff is reviewable.
- **Merge** — a real **3-way merge per conflicting file** via
  `git merge-file ours base theirs` (the workspace already depends on git;
  merge-file needs no repo, just the three inputs). It writes the merged result
  with standard `<<<<<<< ======= >>>>>>>` conflict markers into the working
  file; the user resolves them in a terminal/editor (the editing plane) and
  clicks **Resolve**, which refreshes the base snapshot + marker to the new
  version. Non-conflicting files fast-forward automatically.
- **Skip** — do nothing now; keep offering it.
- **Pin** — stop offering updates for this builtin (records the pinned version
  in the marker). For deployments that have deliberately taken over a component.

After a successful apply, buxond refreshes deps/go.work and reprovisions (same
as import) so a backend change (e.g. a new `llm-gw`) is live at once.

### The rename wrinkle

A tile imported `as apps/foo` had its self-references rewritten
(defaultPath→installPath) by `CopyTree`. Updating it must apply the *same*
rewrite to the new embedded version *before* diff/merge, else every rewritten
line looks like a conflict. The origin marker records both `source` (→ the
embedded defaultPath) and `installPath`, so the update path re-runs the rewrite
deterministically. Same for the module-path rewrite on Go tiles.

## Bootstrapping existing workspaces (no marker yet)

Deployments upgraded to the first buxond that has this feature have **no**
`.buxon/builtins.json`. Adoption on first run:

- For each managed builtin path present in the workspace, compare its files to
  the *current* embed. Files that match exactly → record base = current
  (up to date, silent). Files that differ → the workspace is either customized
  or stale, and we can't yet tell which (no base). Record base = **the
  installed bytes** and flag the builtin `adopted` in the marker.
- Consequence: the *first* update after adoption can't 3-way-merge a
  differing file (base == ours), so it degrades to **Replace-or-manual** for
  those files (show the diff; the user replaces or hand-edits). Every update
  after that is clean, because the marker + snapshot now exist. This is a
  one-time, clearly-messaged migration cost.

## Surfaces

- `GET /api/buxon/builtins/updates` → `[{id, source, installPath, fromVersion,
  toVersion, changelog, files:[{path, status}], conflicts, clean, adopted}]`.
- `POST /api/buxon/builtins/update {id, mode:"replace"|"merge", files?}` and
  `POST /api/buxon/builtins/update/resolve {id}` (after manual merge). Gated by
  `buxon:writer` — the same capability as `create` / tile import.
- `bx builtin updates` (list) · `bx builtin update <id> [--replace|--merge]` ·
  `bx builtin diff <id>` · `bx builtin pin <id>`. (New `bx builtin` verb; `bx
  tile` stays about the catalog.)
- Tile Manager gains an **Updates** tab beside Import/New-from-template: a list
  with a version badge and a modified/conflict indicator per builtin, a diff
  preview, and Replace / Merge / Skip / Pin buttons. Mirrors the existing
  "installed" flag machinery.
- On boot, buxond logs `N builtin updates available`; the shell shows a small,
  dismissable "updates available" chip (owner/admin only) linking to the tab.

## Security / RBAC

- Applying an update creates/overwrites component files → same gate as `create`
  and tile import: owner or `buxon:writer` (admin implies). Never self-approved
  by an element (AGENTS.md/auth.md).
- Updates only ever touch **managed** paths recorded in the origin marker,
  never user components — the marker is the whitelist.
- Everything is inside a git repo: an update is recoverable (`git checkout` /
  the optional pre-update checkpoint commit), so "Replace" is never a
  destroy-only action.

## Open decisions (for DECISIONS.md)

- **BU-1 — Version signal**: content-hash primary + human `version` sugar
  (recommended, zero-maintenance) vs hand-maintained semver only.
- **BU-2 — Marker location**: `.buxon/builtins.json` (gitignored, per-deploy;
  lost on a bare clone → re-adopt) vs a tracked workspace-root lockfile (in git
  history, but a file users can conflict on). Recommend `.buxon/`.
- **BU-3 — Merge engine**: `git merge-file` (recommended — mature, no custom
  code) vs a bespoke 3-way merge.
- **BU-4 — Auto-checkpoint**: whether "Replace/Merge" makes a git commit first
  by default (recoverability) or only on request.

## Phasing

1. **Provenance**: write `.buxon/builtins.json` + base snapshots at init and on
   import/instantiate; content-hash + `version` in catalog metadata.
2. **Detection + Replace**: `updates` API/CLI, Updates tab, fast-forward + whole
   -file replace, adoption path. Delivers the "existing workspaces get the drag
   fixes" win immediately.
3. **Merge**: `git merge-file` per-file 3-way + resolve flow + diff preview.
4. **Pin + auto-checkpoint + boot indicator** polish.

This is the "keep the batteries fresh" complement to plans/tile-sharing.md:
that plan is about *getting* tiles (builtin → peer → registry); this one is
about *keeping copied builtins current* without trampling the edits that make a
workspace someone's own.
