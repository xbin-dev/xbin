# 2026-09-30 — each Go tile builds with a `go.work` of its own (D166)

## What changed

xbind no longer builds a Go backend with the workspace's root `go.work`.
Each build gets a `go.work` made from the tile's own `go.mod` at build
time, under `.xbin/cache/tile/<key>/work/`. It uses:

- the tile's module (at its root, or in `backend/`, and the one holding its
  `entry` package when that is another) and the xbin SDK, as before;
- each other tile's module the tile **reaches** — through its `go.mod`'s
  `require` and `replace` lines and its code's imports, and on through what
  those reach — when the reference can only mean the workspace: a module
  path with no dot in its first element (`calendar`), a `require … v0.0.0`,
  a `replace` with that tile's directory, an import the `go.mod` doesn't
  require (unless some `go.mod` of the workspace requires that path at a
  published version), or a tile named in the manifest's `deps`;
- with a hand-managed root `go.work` (no xbind marker line), its `go`,
  `toolchain`, `godebug` and `replace` lines, and its `use`d modules as
  candidates like the tiles'.

Modules a build doesn't reach are not in its `go.work`, so their
requirements and `replace` lines no longer change what the tile compiles.
The root `go.work` is unchanged and still what terminals, editors and gopls
use. A tile with no `go.mod` builds with `GOWORK=off`. Workspaces without
`--isolate` get the same.

A failed build whose error is `no required module provides package …`
now ends with an `xbind:` line naming the `require` to add.

## Who's affected

Go tiles whose build worked only because of **another** tile's `go.mod`:

- **A tile importing a package its `go.mod` doesn't require**, where
  another tile's `go.mod` requires that module (say, your code imports
  `golang.org/x/sys/unix`, only `sandbox-terminal` requires `x/sys`). The
  build now fails with `no required module provides package
  golang.org/x/sys/unix`.
- **A tile using an API newer than its own `go.mod` requires**, where
  another tile's higher requirement raised the version for everyone. The
  build now fails to compile (`undefined: …`).
- **A tile relying on another tile's `replace`.** Its build now uses the
  module the tile's own `go.mod` names.
- **A tile importing another tile's module whose path has a dot**, at a
  version that is published (another tile's `go.mod` requires that path at
  a real version), without naming that tile in `deps`.

Unaffected: tiles whose `go.mod` names what they use (every builtin tile
and template, and every scaffold `bx new` writes), tiles importing other
tiles' dotless modules (`module calendar`) with or without a `require`,
terminals, gopls, `go build` in a shell, node and python tiles.

## How to migrate

1. Read the failed build's output (`bx logs <tile>`, or the tile's status).
   An `xbind:` line under `no required module provides package …` names
   the line to add, e.g. ``add `require golang.org/x/sys v0.46.0` to
   apps/x's go.mod``.
2. Add the requirement to the tile's `go.mod` (by hand: `go get` resolves
   the SDK's `v0.0.0` against a proxy and fails). Save: the tile rebuilds,
   and any checksum it needs is added to its build's own `go.work.sum`.
3. For another tile's module with a dotted path, add `require
   <path> v0.0.0`, or name the tile in your manifest's `deps`.
4. For an API newer than your requirement: raise the version in `go.mod`
   to the one the other tile requires.

## Why

In workspace mode the go command builds with one module graph over every
module the `go.work` uses, and the root `go.work` used every Go tile. So a
tile requiring a newer version of a dependency silently changed the version
every other Go tile built with, one tile's broken `go.mod` broke every Go
build, and a `replace` in any tile's `go.mod` applied to every build: a
person who could change only tile A could point tile B's dependency at code
of their choosing, and B's backend ran it. Reproduced with the go command
itself (`TestConfinedGoBuildOwnWorkspace`): under the shared `go.work`,
tile B compiled tile A's `replace` target and A's version of a shared
dependency. A security hole closes in the release that finds it
([compat.md](/docs/compat.md) rule 11).

It also fixes a race: a new tile's first build could run before the root
`go.work` listed its module and fail with `go: no modules were found in the
current workspace` until its code changed. A build's own `go.work` is made
at build time from what the registry lists.
