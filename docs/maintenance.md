# Maintaining xbin — guards, budgets, checklists

For people (and agents) changing **xbin itself**: the checks CI runs, what
each one protects, and the checklists that keep the repo in the shape a
maintainer can work in after a long absence. Workspace builders can skip
this page; the builder contract is [compat.md](/docs/compat.md).

Every guard here is a red CI line, not a remembered rule. If you find
yourself writing "remember to…" in a commit message or a doc, the fix is a
test or a Makefile target next to the thing that needs remembering.

## Definition of done

```
make fmt-check vet      # gofmt over GOFMT_DIRS, go vet ./...
make test               # unit tests, incl. the embed guard (assets_test.go)
node --check <file.js>  # every touched .js (extract inline <script type=module> first)
make integration        # when the runner / sandbox / broker path changed
```

CI (`.github/workflows/ci.yml`) runs exactly `make fmt-check`, `make vet`,
`make test`, `make integration`. Builder-visible behaviour also needs a
`docs/changelog.md` entry and the relevant `docs/*.md` update; every
non-obvious choice gets a numbered entry in the decision log
(`plans/DECISIONS.md` in the repo). Releases run `hack/check-pins.sh` first.

## Embedded assets

`assets.go` embeds five trees into every xbind byte-for-byte: `web/`
(served at `/vendor/…` and the injected client), `docs/` (served at
`/docs/`), `workspace-template/` (copied by `xbind init`),
`builtin-tiles/` (copied by `bx tile import`) and `builtin-templates/`
(copied by *new from template*). What lands in them is therefore a release
decision, and `assets_test.go` refuses:

- **compiled binaries** (ELF magic anywhere). A bare `go build` inside
  `builtin-tiles/<t>/backend` leaves `backend/backend`; that once rode along
  in every xbind (+10 MB) and would have been copied into workspaces by
  `bx tile import`. `.gitignore` covers those paths, and the copier
  (`internal/builtins` `strayBuildOutput`) skips an ELF named after its own
  directory even when it finds one in a workspace template. xbind never runs
  an in-tree binary — Go backends compile into `.xbin/build/`; the one
  legitimate executable, a `cgi` runtime's `backend/handler`, is never
  matched.
- **files over 512 KB** outside `web/vendor/` — the vendored frontend deps
  are the only big files by design.
- **nested repos and dependency trees** (`.git`, `node_modules`, `.claude`,
  `deps`): `all:` embeds dotfiles too.
- **pointers into `plans/`**. The design records are *not* embedded, so a
  `plans/x.md` citation in a served doc, the scaffolded `AGENTS.md`, or an
  imported tile's comments is a dead path for the reader. Cite the served
  docs (`docs/auth.md`, `docs/overview/11-interfaces.md`, …) and decision
  IDs (`D48`, `ND8`, `ING-3`) instead; the decision IDs resolve in the repo's
  `plans/DECISIONS.md`, and `docs/overview/00-index.md` tells a reader where
  the design records live. In Go/JS source that only the repo sees
  (`internal/`, `cmd/`, `sdk/`), `plans/` citations are fine and welcome.

The test walks the real embed, so it catches anything `go:embed` picked up —
run `make test` after adding files under those trees.

## Vendored frontend deps

`hack/vendor.sh` fetches the pinned builds into `web/vendor/` and writes
`hack/vendor.sha256`; `hack/check-pins.sh` (the release preflight) fails when
the tree and the list disagree or a file is unlisted. To bump a dependency:
edit the version in `hack/vendor.sh`, run it, commit `web/vendor/` and the
checksum file together. Never edit a vendored file by hand — the checksum
check exists so that a hand edit cannot survive to a release unnoticed.

## gofmt scope

`make fmt-check` runs gofmt over `GOFMT_DIRS` (Makefile): the root `*.go`,
`cmd`, `internal`, `sdk`, `test`, `builtin-tiles`, `builtin-templates`,
`examples`. It is explicit so gofmt never walks `devws*/` or `.rootfs/`
(a whole distro of files). When Go sources appear in a new top-level tree,
add it there. `make fmt` rewrites in place.

## Build output that must not be committed

`bin/`, `dist/` (release bundles), the root `bx`, `website/dist/`, and
`*/backend/backend` / `*/_backend/_backend` build leftovers are ignored.
`git status` after a build should be clean; if it is not, the fix goes in
`.gitignore` (and in `assets_test.go` when the path is under an embedded
tree), not in a commit.
