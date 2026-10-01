# 2026-09-30 — each Go tile builds with a `go.work` of its own (D166)

## What changed

xbind no longer builds a Go backend with the workspace's root `go.work`.
Each build gets a `go.work` made from the tile's own `go.mod` at build
time, under `.xbin/cache/tile/<key>/work/`. It uses:

- the tile's module (at its root, or in `backend/`, and the one holding its
  `entry` package when that is another) and the xbin SDK, as before. A tile
  with no `go.mod` of its own inside another component's module builds in
  that module, as before;
- each other tile's module that the tile's **own** `go.mod`, manifest or
  code chooses, and on through what those modules choose:
  - a `require` of a dotless module path (`calendar`), or at `v0.0.0`;
  - a `replace` with that tile's directory (a `require` the `go.mod` also
    replaces is the replace's alone);
  - an import no `require` or `replace` of the build covers, of a dotless
    path (or of a module nested in the tile's own module directory), when
    exactly one workspace module holds that package;
  - a tile named in the manifest's `deps`: its module serves any reference
    to the path it declares, dotted or published, and brings its
    `replace` lines;
- with a hand-managed root `go.work` (no xbind marker line), its `go`,
  `toolchain`, `godebug` and `replace` lines, and its `use`d modules like
  the tiles' (one outside every tile serves any reference to its path).

Nothing another tile's `go.mod` says reaches the build: not its
requirements, not its `replace` lines, and not a module path it declares
(a tile declaring `golang.org/x/sys/unix`, an SDK sub-package's path or
`calendar/store` never stands in for what another tile imports). The
build's `go` line is the highest of `1.24`, a hand-managed root's and the
used modules' (`go 1.24.0` modules build). The root `go.work` is unchanged
and still what terminals, editors and gopls use. A Go tile with no module
builds with `GOWORK=off`. Workspaces without `--isolate` get the same.

A failed build whose error is `no required module provides package …` or
`package … is not in std` now ends with `xbind:` lines saying what to add
to the tile's `go.mod` or manifest. They name module paths and versions,
never another tile.

## Who's affected

Go tiles whose build worked only because of **another** tile's `go.mod`,
or because every tile's module was in one `go.work`:

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
- **A tile importing another tile's module whose path has a dot**
  (`example.com/lib`) **with no `require` of it and no `deps`** (a child
  component nested in the tile's own module directory aside). The build
  fails with `no required module provides package`.
- **A tile requiring another tile's dotted module at a published
  version** (`require github.com/org/lib v1.2.0`, a tile declaring
  `github.com/org/lib`). The build now uses the published module from the
  module proxy — silently, if that version exists — not the tile's code.
- **A tile importing a package two workspace modules could provide**
  (a dotless `calendar/store` that both `calendar`'s `store/` and a module
  declaring `calendar/store` hold) with no `require`. It failed before too
  (`ambiguous import`); it now fails with `package calendar/store is not
  in std` and a hint.
- **A tile importing a package beneath its own module path from a module
  elsewhere** (module `suite` importing `suite/admin` from a tile outside
  `suite`'s directory) with no `require`. The build fails with a hint.
- **A tile whose binary linked a higher version of a dependency than its
  own `go.mod` selects** — because another tile's graph reached it. Under
  the shared `go.work` the go command picked each module's version over
  every tile's `go.mod` at once, so every tile linked the highest version
  any tile's graph reached (sometimes by accident: one tile's `// indirect`
  line could pull in a module's newer dependencies for everyone). Now each
  links what its own `go.mod` selects. **Nothing fails**: the build
  succeeds and the backend runs with older code — older fixes, and
  possibly known vulnerabilities fixed in the versions it had. On one
  workspace of 66 Go tiles, 25 were in this class, 20 of them for one
  line (`modernc.org/sqlite v1.39.1`, which lifts its `libc`, `mathutil`,
  `memory`, `x/sys` and `x/exp`); the rest for `golang.org/x/crypto`,
  `x/text`, `x/net`, `x/sys` and `github.com/coder/websocket`. xbind finds
  these tiles itself and tells admins (below).
- **A protected deployment's primary whose artifact is lost** (`.xbin/`
  lost or wiped) after this upgrade: its `build.json` from before records
  the workspace's `go.work`, so the restart is held for a tile manager to
  redeploy instead of silently rebuilding with a different module graph
  ([isolation.md](/docs/isolation.md)).

Builds unaffected (though any Go tile may link older versions, above):
tiles whose `go.mod` names what they use (every builtin tile and template,
and every scaffold `bx new` writes), tiles importing other
tiles' dotless modules (`module calendar`) with or without a `require`
(unless two modules could provide the package), tiles using another
tile's module through a `replace` with its directory, Go tiles with no
`go.mod` inside another component's module, terminals, gopls, `go build`
in a shell, node and python tiles.

## How to migrate

1. Read the failed build's output (`bx logs <tile>`, or the tile's status).
   An `xbind:` line under the go command's error says what to add, e.g.
   ``add `require golang.org/x/sys v0.46.0` to apps/x's go.mod``.
2. For a published module: add the requirement to the tile's `go.mod` (by
   hand: `go get` resolves the SDK's `v0.0.0` against a proxy and fails).
   Save: the tile rebuilds, and any checksum it needs is added to its
   build's own `go.work.sum`.
3. For another tile's module: name that tile in your manifest's `deps`
   (`"deps": ["apps/lib"]`) — or add `require <its module path> v0.0.0`
   and `replace <its module path> => <its directory, relative to your
   go.mod>`. A bare `require … v0.0.0` without the `replace` fails as soon
   as the go command loads the whole module graph (any import from
   outside the workspace): that is the go command's rule, with any
   `go.work`.
4. For an API newer than your requirement: raise the version in `go.mod`
   to the one the other tile requires.
5. For a held protected primary: redeploy it from its tile manager.
6. For a tile that now links older versions: admins get an alert (kind
   `go-build-versions`, in the shell, the admin tile and `GET
   /api/xbin/alerts`) naming each such tile with the fewest lines that keep
   what it had, e.g. ``apps/notes builds with older dependency versions
   since v0.3.65 (each Go tile now builds with its own go.mod's versions):
   add `require modernc.org/sqlite v1.39.1` to apps/notes's go.mod to keep
   what it had``. `bx doctor` lists the same with each module that changed
   (`GET /api/xbin/go-build-versions`). Add the lines to the tile's
   `go.mod` and keep its other lines — or raise the existing `require` of
   that module to the version (the go command takes the higher of two).
   The tile rebuilds; once its own build links what it had, its line
   leaves the alert. To keep the older versions instead, dismiss the alert
   (its line comes back only if the lines it needs change).

   xbind finds them on its own: once, in the background a little after the
   first start of an xbind with this check on a workspace an earlier xbind
   built Go tiles in, a few tiles at a time, it lists what each Go tile's
   entry links under the workspace's shared `go.work` and under its own
   (`go list -deps`, confined like a build, with the build's caches and
   settings), and where its own is lower, finds the fewest `require` lines
   that restore the rest (trying the tile's own direct requirements first,
   checking each by listing again). Its state lives in
   `data/go-build-versions.json`; an admin runs it again with `POST
   /api/xbin/go-build-versions/check`. A tile it couldn't compare (its build
   fails one way or the other) is a note in `bx doctor`.

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
dependency. The same holds for a module *path*: a tile declaring a path
another tile imports (`golang.org/x/sys/unix`, an SDK sub-package) had its
package compiled into that tile. So the build's modules are chosen only by
the tile's own `go.mod`, manifest and code. A security hole closes in the
release that finds it ([compat.md](/docs/compat.md) rule 11).

It also fixes a race: a new tile's first build could run before the root
`go.work` listed its module and fail with `go: no modules were found in the
current workspace` until its code changed. A build's own `go.work` is made
at build time from what the registry lists.
