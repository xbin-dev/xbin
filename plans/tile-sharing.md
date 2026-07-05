# Tile sharing — design

How tiles move between people once a community forms. The premise stays the
same as everything else in xbin: **a tile is a directory, its path is its
identity, and importing is copying files into a workspace.** Sharing is
therefore mostly about *distribution* (where the bytes come from) and
*trust* (what running someone else's tile grants it), not a new object model.

## The ladder (each rung builds on the last)

### 1. Builtin set — shipped, curated, in this repo *(implemented)*

Optional tiles bundled in the xbind binary under `builtin-tiles/<name>/`,
each with a `tile.json` catalog entry. They are **not** auto-installed
(unlike `tiles/manager` + `tiles/admin`, which are workspace chrome); you
import them on demand:

```
bx tile ls                        # catalog (│ * = installed at default path)
bx tile import <name> [as <path>] # copy into the workspace
```

…or from the Tile Manager's **Import** tab. Mechanics:

- Files are copied to the target path (default = the tile's authored
  `defaultPath`). `tile.json`, `.claude/`, and dotfiles are not copied.
- **Import as `<path>`**: the tile's *own* authored path is rewritten in its
  text files (view `<script src>`, its scope's `res:` ids, etc.), and a Go
  backend's module path is set to the target path — so the same tile can be
  installed twice without collision. References to *other* tiles are left
  intact (so a dependent still points at its provider's real path).
- A Go backend ships as `go.mod.tile` (renamed to `go.mod` on import),
  because `go:embed` skips nested modules — the one wrinkle of embedding
  compilable tiles in the binary.
- Importing runs deps reconciliation + `go.work` regen immediately, so the
  tile is usable the instant it lands (no watcher-tick race).

Curation model: builtins are reviewed and versioned *with xbind* — they're
as trusted as the binary itself. This is the "batteries included, but
optional" tier: `llm-gw`, `chat`, and whatever else the project blesses.

### 2. Peer export / import — tarballs *(next)*

For sharing a tile you built with one other person, no registry needed:

```
bx tile export apps/mything ./mything.tile.tar.zst   # dir → signed-ish tarball
bx tile import ./mything.tile.tar.zst [as <path>]    # same import path
```

A tarball is just the component subtree plus a generated `tile.json`
(title/description/defaultPath derived from the manifest). It carries the
tile's `uses` — so on import the recipient sees exactly which grants it will
request, and approves them the same way. Same "import as" rewriting applies.

Open question for v1 of this rung: whether to include the built Go binary or
require rebuild-on-import. Rebuild is safer (no opaque binaries) and cheap
given the runner already builds on first request — lean rebuild-only.

### 3. Remote sources — a community registry *(later)*

```
bx tile import github.com/user/cooltile [as <path>]  # git ref
bx tile import xbin://registry/user/cooltile@1.2.0  # a hosted index
```

A registry is an index of `tile.json` entries pointing at fetchable sources
(git repos / tarball URLs). `bx tile search`, versioning, and update
notifications live here. The Tile Manager's Import tab gains a "Registries"
source alongside "Builtin". This is where a real community lives; it's
deliberately last because the trust story (below) has to be solid first.

## Trust & security — the part that actually matters

Running someone else's tile is running someone else's code. xbin's existing
model does most of the work; sharing just has to *surface* it:

- **Nothing runs privileged by import.** A tile's backend is a
  least-privileged element like any other. It can reach only its own
  vault/API plus what it's **granted**.
- **Grants are the review surface.** An imported tile's `uses` are declared
  and visible; cross-scope and `xbin:*` grants land **pending** for the
  owner to approve (agents must not self-approve — AGENTS.md). So "this tile
  wants to call your LLM gateway / read a shared db / manage the workspace"
  is an explicit, inspectable decision at import time. The import API already
  returns the pending grants; the UI shows them.
- **Code is inspectable before it runs.** Import copies files; the owner can
  read the source (it's right there in the workspace, editable) before ever
  opening the tile or approving a grant. Backends build lazily — no grant,
  no first request, no execution.
- **Capability tiers, not trust levels.** Builtins are curated (tier: as
  trusted as xbind). Peer/registry tiles are not — they get exactly the
  grants the owner approves, nothing implicit. A future `xbin:admin`-style
  scary-grant prompt should be visually loud in the import flow.
- **Signing / provenance** (registry rung): tarballs and registry entries
  should be signable (author key), and the import UI should show
  who/where-from and whether the signature verifies. Not required for
  builtins (binary-trust) or LAN peer sharing, but table stakes for a public
  registry.

## Naming & identity

Path-as-identity keeps sharing honest: there's no global namespace to fight
over. A tile *suggests* a `defaultPath` (`apps/llm-gw`), the importer can
override it (`import as apps/my-gw`), and everything self-references through
xbind so multiple copies coexist. The only cross-tile coupling is when a
dependent hardcodes a provider's path (chat → `apps/llm-gw`); those tiles
should document their expected provider path in `API.md`, and a future
import flow can prompt to remap it.

## Why not npm/OCI/a package manager?

Tiles are directories, not packages — no build artifacts to resolve, no
dependency graph to solve (cross-tile deps are grants, resolved by the owner
at approval time, not by a solver). Reusing a heavy package ecosystem would
fight the "it's just files in a git-versioned workspace" ethos. The ladder
above stays filesystem-shaped: copy, inspect, grant, run.
