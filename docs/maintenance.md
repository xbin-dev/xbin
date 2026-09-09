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
make check              # fmt-check vet js-check shellcheck pins-offline test
make integration        # when the runner / sandbox / broker path changed
make hooks              # once per clone: the sub-second subset runs pre-commit
```

CI (`.github/workflows/ci.yml`) runs exactly `make check` then
`make integration`; a release (`make release TAG=vX.Y.Z`) runs `make check`
and the online pin checks before building. Builder-visible behaviour also
needs a `docs/changelog.md` entry and the relevant `docs/*.md` update; every
non-obvious choice gets a numbered entry in the decision log
(`plans/DECISIONS.md` in the repo). Each guard below is its own Makefile
target, so a red line names the guard that failed.

| Guard | Target | Protects |
|---|---|---|
| gofmt over `GOFMT_DIRS` | `fmt-check` | formatting drift (CI's gofmt must match go.mod's minor — the pins check enforces that) |
| `go vet ./...` | `vet` | the usual |
| `node --check` over every shipped script and inline module block | `js-check` | a syntax error in a tile's inline `<script type="module">`, which nothing else parses before a user's browser does |
| shellcheck at warning level over `deploy/`, `hack/`, `.githooks/` | `shellcheck` | the installer and release scripts (1,300 lines of bash with no other tests) |
| vendor checksums, Go-version agreement, alpine pins | `pins-offline` | pins drifting apart between the files that state one |
| unit tests incl. the embed guard, route inventory, docs check | `test` | see the sections below |

## Size budget (the ratchet)

`internal/sizebudget` reads `hack/size-budget.txt` (`<path> <max-lines>`)
and fails `make test` when a listed file grows past its budget, when an
unlisted non-test Go file passes 800 lines or a shipped `.js`/`.mjs`/`.html`
passes 900, or when a listed file has shrunk below 90 % of its budget —
then the number in the file comes down, so a split never quietly regrows.
Raising a budget is the wrong fix; splitting the file is the right one
(the admin console's tabs and the shell's children are the pattern). The
seed numbers are the sizes on 2026-09-09; `notes.js` (prose in JS) is
listed deliberately.

## Docs check (decision ids, links, plan status)

`internal/docscheck` fails `make test` when:

- a decision id (`D48`, `ND8`, `ING-3`, `IFACE-1`, `VD-2`, …) cited anywhere
  in `docs/`, `plans/`, `internal/`, `web/`, `workspace-template/`, `cmd/`,
  `sdk/` or the builtin trees has no definition — a bullet or heading opening
  with the bold id — in any `plans/*.md` (core `D`/`ND` numbers live in
  `plans/DECISIONS.md`; the per-domain series in their design record). A
  trailing letter (`D17a`) names a labelled sub-point of the base entry and
  resolves to it;
- a relative or `/docs/`-absolute markdown link in `docs/` or `plans/` points
  at a file that does not exist;
- a `plans/*.md` has no `> Status: live | implemented | superseded |
  historical` line in its first 12 lines. *live* = still steers work;
  *implemented* = shipped as described, kept as rationale; *superseded* =
  read the newer record; *historical* = no longer applies.

So a new decision is written once, in the log, and cited by id everywhere
else; a design record that stops being true gets its status flipped rather
than deleted.

## Releasing

```
make release TAG=v0.3.44            # hack/release.sh
make release TAG=v0.3.44 RELEASE_FLAGS=--dry-run
```

The script: preflight (tag shape, clean tree, on master, `gh` authenticated)
→ `make check` → annotated tag + push of the branch and the tag → build and
publish from a **detached worktree of the tag** (`deploy/publish-release.sh`
builds whatever checkout it runs in; an edit on master during the build must
not leak into the bundles) → watch the commit's CI runs → `gh release view`
→ prune `dist/` to the two newest tags' bundles. `--no-check`, `--arch`,
`--no-watch`, `--keep N`, `--allow-branch` exist for the unusual day; the
release notes and the changelog entry are still yours to write.

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

## Editing the scaffold: the dev overlay

A workspace owns its copies of `workspace-template/` (the shell, the admin
and organisations tiles, the welcome app…) from `xbind init` on, and `--dev`
only serves `web/` and `docs/` from the source tree. `--dev-overlay DIR`
(dev only) makes the `/c/` static plane look in DIR first: `make dev` and the
UI harness pass `workspace-template/`, so an edit to `bx-shell.js` or
`admin.js` is live on reload with nothing copied into `devws/`. Files only —
manifests (`xbin.json`, `scope.json`) always come from the workspace, because
the registry read those and grants, chrome and inject state must agree with
what is served; a file only the overlay has (a scaffold file you just added)
is served too, so no re-init is needed. `internal/server/overlay_test.go`
pins the rules.

## The legacy-workspace fixture (never break users)

`test/legacy_workspace_test.go` (in `make integration`) ages a fresh
scaffold into what an old workspace looks like — a legacy shared `home/`
with real data, no `homes/`, no backfill ledger, no builtins provenance
(an *adopted* workspace), no `tiles/organisations`, a `.gitignore` without
`homes/` — and boots the real binary on it twice. The first boot may
change only the paths the contract allows (`.xbin/`, `data/`, `homes/`,
the removed `home/`, the essential `tiles/organisations/`, `go.work`,
`.gitignore`, `AGENTS.md`/`CLAUDE.md`, materialised `deps/`) and must
leave the root `xbin.json` byte-identical; the second boot must change
nothing at all. A second test asserts the hard stop when both `home/` and
`homes/` hold data. **Every new boot-time migration extends this test**
(docs/compat.md rule 9): add what it may touch to the allowed list with
the reason, and make sure the second boot still changes nothing.

## Server helpers and durable writes

- **Every non-2xx answer of the built-in API goes through
  `server.WriteError(w, code, msg[, docs])`** — the `{"error", "docs"}`
  shape is a wire contract; `server.WriteOK(w)` writes the historical
  `{"ok":"true"}`; `server.DecodeJSON(r, v)` decodes strictly (unknown
  fields are 400s). A handler that must accept unknown fields from older
  clients decodes by hand and says so in a comment.
- **Every on-disk store writes through `fsutil.WriteFileAtomic`** (same-dir
  temp, fsync, rename, fsync dir; the temp name `.<name>.<random>.tmp` is
  ignored by the workspace watcher). `WriteFileAtomicIn` creates the parent
  first. A plain `os.WriteFile` is for content that is not state: a
  scaffold file being seeded, a clone's rewritten source.

## Builtin tiles and templates

- `make tile-check` (`hack/tile-check.sh`, a CI step) vets and tests every
  `builtin-tiles/*/backend` and the agent template's `_backend` against
  its own `go.mod.tile` in a scratch copy with the sdk replaced by the
  checkout — what a workspace actually builds. `make vet` compiles the
  same sources against the root `go.mod`, which is not what runs.
- `internal/builtins` `TestBuiltinManifestsAndRoleGuards`: every shipped
  manifest parses with xbind's JSONC reader, and every role a backend
  guards with `sdk.Role` / `RoleFunc` is declared under `expose.roles`
  (`admin` excepted) — a guard on an undeclared role is a 403 nobody can
  grant their way past. `expose` (roles other tiles may be granted) and
  `exposes` (ports published through ingress) are different keys on
  purpose; both stay.
- A tile's `tile.json` `version` bumps whenever its files change, with a
  changelog line — that is how `bx builtin updates` offers the update.

## Frontend kit and theme fallbacks

`web/bx-kit.js` is the one home of the helpers every frontend used to copy
(`api`/`xbinApi`/`selfApi`, `jbody`, `esc`, `deepActive`, `pathHas`,
`clampBox`); `web/bx-code.js` owns the highlight helpers (`hl`, `langFor`,
`diffHTML`). `make js-check` refuses a second definition of any of them in
the shipped trees — import from `/vendor/…` by absolute URL instead
([frontend-kit.md](/docs/frontend-kit.md) lists what tiles may import and
why bare specifiers are banned).

`make theme-check` (`hack/theme-fallbacks.mjs`) fails when a
`var(--bx-x, <literal>)` fallback in `web/`, `workspace-template/` or the
builtin trees disagrees with `web/theme.css`; `--fix` rewrites them. The
fallbacks are the theme for a document that never linked `theme.css` — the
sheet is deliberately **not** injected into tile documents: it sets
`color-scheme: dark` and would flip a third-party tile's default colours
([compat.md](/docs/compat.md) rule 5). Font tokens are exempt (a fallback may
abbreviate the stack).

## The admin console's tabs (`workspace-template/tiles/admin`)

`admin.js` is the router: the two-level nav (`GROUPS`), hash deep-links and
their alias map, `_refresh()` (the shared lists: overview, users, orgs,
policy, sets, defaults, requests, sessions), the global `.err` /
`.notice` slots — about 300 lines. Every tab is its own element under
`tabs/<name>.js` (`runtime` for components + the code drill-in + live
stats + resources, `map`, `netsets`, `permsets`, `vault`, `cron`,
`backup`, `binding` for grants/roles/providers/wiring, `ingress` for
expose/endpoints, `orgs` for org cards, policy ceilings and the workspace
defaults, `users`, `signin`, `sessions`); the
router renders it with its inputs as properties and imports it
**relatively** (`./tabs/map.js`) — a sandboxed tile may import its own
siblings, and `bx builtin update` delivers new files inside the unit, so an
older workspace's monolith keeps working while a fresh one gets the split
([compat.md](/docs/compat.md) rule 4). A tab element:

- either takes its data as properties from the router's shared lists or
  loads its own in `connectedCallback` and exposes `refresh()` for the
  router's event-driven refresh;
- owns its state, endpoints and CSS: `static styles = [base, <slice>]`
  from `admin-css.js` (synchronous — never a `<link>` or a `fetch()`);
- extends the mixins in `shared.js` it needs: `WithRouter` (the composed
  events below, `_fail`/`_ok`, and `_orgAPI` = one write then a refresh),
  `WithDrafts` (`_draft`/`_setDraft`/`_dropDraft`/`_toggleDraft`, the
  `_tilesEditor` / `_patternsEditor` row editors, `draftApi()` for the
  harness) and `WithFilter` (`_match`, `_filterBar`, category chips);
  `shared.js` also holds the datalists (`targetDatalist`,
  `serviceDatalist`, `groupsDatalist`), `allowRows`, the membership
  presets (`PRESETS`, `presetOf`) and `setLifecycle`;
- reports through composed events the router already handles:
  `bx-admin-err` (message; `''` clears), `bx-admin-notice` (message),
  `bx-admin-refresh` (reload the shared lists), `bx-admin-tab` (navigate),
  and any shared toggle it changes (`bx-admin-show-hidden`);
- keeps the markup hooks the harness locates (`.mcell`, `.maprow`,
  `[data-set]`, `[data-netset]`, `[data-edit-allow]`, …) and, if it holds
  drafts, is routed to from the router's `testApi()` by key namespace
  (`permset:`, `netset:`, `bindcustom:`, `orgallow:`/`ws:`, `user:`).

Adding a tab: the element under `tabs/`, an entry in `GROUPS`, one arm in
`render()`, and the `adminTabs` harness pass opens every id in `GROUPS`
and fails on an empty or `.err` body (`hack/ui-harness/shots.js`).

## The shell (`workspace-template/shell`)

`bx-shell.js` is being taken apart the same way as the admin console: its
stylesheet is `shell-css.js` (`shellCss`), every pointer drag goes through
the kit's `dragPointer`, and the next steps are pure menu builders
(`menus.js`), one `revDraft` module for the three identical draft flows
(org screens, folders, share-to-org), and children (`bx-side`,
`bx-screens`, `bx-canvas`, `bx-toasts`) once the harness's `testApi()`
surface covers what moves. `bx-shell.js` keeps its name and imports the
siblings relatively (compat rule 4). The harness passes `windows`,
`screens`, `menus`, `mobile`, `reloadFocus` and `contextCopy` are the
gate for every slice.

## UI harness (`hack/ui-harness`)

The frontend has no unit-test runner; browser behaviour is pinned by
`hack/ui-harness`: `run.sh` builds xbind, seeds a throwaway workspace
(orgs, network sets, users, org tiles in every binding state, the
`focusy` and `linky` fixture tiles) and runs Playwright passes from
`shots.js` — screenshots and `<select>` dumps to look at, plus asserting
passes that write `PASS`/`FAIL` lines under `$HARNESS_DIR/out/<pass>.txt`
and exit 1 on any FAIL.

```
hack/ui-harness/run.sh                    # build, fresh workspace, seed, every pass, stop
hack/ui-harness/run.sh --keep             # …and leave xbind up on $PORT
hack/ui-harness/run.sh --shots windows    # one pass against the running instance
hack/ui-harness/run.sh --restart          # rebuild xbind, same workspace, every pass
(cd hack/ui-harness && node shots.js --list)
```

Rules that keep it cheap to maintain:

- **Passes drive the elements' `testApi()`** — `bx-shell`, `bx-frame` and
  `bx-admin` each expose stable names over their private state (open a
  tile, set a float, open the admin window, read the toasts…). A pass
  never touches a `_member` or walks `shadowRoot` by hand; `make js-check`
  fails on `._x` in that directory. When a refactor renames state, only
  `testApi()` moves. The surface reads and writes existing state — no
  test-only branches in production code.
- **DOM hooks are part of the markup contract**: `.card[data-path]`,
  `.item[data-path]`, `.float[data-path]`, `.spawn`, `.admin-pop`,
  `details[data-sec]`, `.netsetcard[data-netset]`, `.setcard[data-set]`,
  `[data-new-set]`, `[data-save-set]`. Keep them when restructuring
  templates. Playwright selectors pierce open shadow roots, so
  `bx-frame[src="apps/x"] .pop` reaches into a frame.
- **Wait for a condition, never for time**: `waitFor(page, (t) => …)`
  polls the shell's test surface, `waitSel` a selector, `settle` two
  animation frames after a state change. A fixed `sleep` is only right
  for a negative ("no menu appears") or a timer inside the element.
- One `lib.js` holds login, the in-page plumbing (`sh`, `fr`), waits,
  screenshots and the checker; a new pass is a function added to
  `PASSES` in `shots.js` — as its own module under `passes/<name>.js`
  (`shots.js` is at its size budget; `passes/users.js` is the model).

On this box: `PLAYWRIGHT_DIR=~/lcad-wasm` (Playwright + its Chromium) and
`HARNESS_DIR` somewhere outside the repo.

## Route inventory (routes ↔ OpenAPI ↔ protocol.md)

`internal/apicheck` mounts the broker on a server exactly as the daemon does
(plus the handlers `cmd/xbind/main.go` registers inline, read from its
source) and reconciles three lists:

- what is mounted — `Server.APIRoutes()` (every `RegisterAPI` pattern) and
  `Server.CoreRoutes()` (login, `/c/`, `/vendor/`, `/docs/`, `/api/`, the
  WebSockets);
- `internal/server/openapi.go` — the `/api/xbin` surface with the capability
  each operation needs (served at `/api/xbin/openapi.json`);
- `docs/protocol.md` — every route row inside a code fence, i.e. a line
  starting at column 0 with `GET|POST|PUT|PATCH|DELETE|ANY` and a path
  (continuation lines are indented, so prose never matches).

Rules: every mounted API route has an OpenAPI entry **and** a protocol.md
row; every core route has a protocol.md row; every documented row is a
mounted route; every `RegisterAPI("…")` literal under `cmd/` and `internal/`
is one the fixture mounts (a new registration site must be wired into the
test, or it silently stops being covered). Wildcards reconcile as you would
expect: a mux `{rest...}` covers one or more documented segments, so
`GET /vault/{rest...}` is documented as both `/vault/<component>` and
`/vault/<component>/<key>`; `<x>`, `{x}`, `[x]` and `res:<scope>` segments
are all "one segment"; `?query` suffixes are ignored — which is why an
*optional* query goes in the row as `?frame=<token>`, never `[?frame=…]`
(the bracket would turn the segment into a wildcard).

Adding a route is therefore three edits in one commit — `RegisterAPI` (or
`Handler`), an `openapi.go` row, a `protocol.md` row — and `make test` says
which one you forgot.

## Pins (`hack/check-pins.sh`)

Offline (`make pins-offline`, part of `make check`):

- `web/vendor/` matches `hack/vendor.sha256` and every file is listed.
  `hack/vendor.sh` fetches the pinned builds and rewrites the list; to bump
  a dependency edit the version there, run it, commit both. Never edit a
  vendored file by hand — the checksum exists so a hand edit cannot reach a
  release unnoticed.
- Go versions agree: `go.mod` and `deploy/install.sh` (`XBIN_GO_VERSION`)
  are equal; `ci.yml`'s `go-version` is go.mod's major.minor (gofmt output
  differs across minors); the rootfs-baked toolchain
  (`docker/rootfs.Dockerfile` `GO_VERSION`) satisfies every shipped `go`
  directive (sdk, builtin tiles' `go.mod.tile`, examples), because terminals
  build tiles with it.
- The alpine pins in `hack/build-*.sh` and `deploy/install.sh` agree.

Online (`make pins`, run by every release): EOL dates of the pinned Alpine
and Ubuntu releases against endoflife.date, and a HEAD request for every
tarball a build or a tile setup script downloads (Alpine APKINDEX, the Go
toolchains, the traefik release the builtin tile fetches).

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
