# AGENTS.md — developing xbin itself

This file is for agents and humans hacking on the **xbin platform** in this
repo (daemon, CLI, SDK, core elements, builtin tiles, docs). For building
tiles *inside* a workspace, the reference is `workspace-template/AGENTS.md` —
the file every workspace ships to its own agents. Design docs live in
`ARCHITECTURE.md` and `plans/` (with `plans/DECISIONS.md` recording every
decision + rationale — **check it before re-litigating a choice**).

## Layout

| Path | What |
|---|---|
| `cmd/xbind`, `cmd/bx` | daemon, CLI |
| `internal/{server,registry,watch,term,runner,proxy,broker,sandbox,auth,vault,resenc,events,deps,jsonc,util,…}` | the platform |
| `sdk/` | Go SDK — **own module, zero dependencies, forever** (components inherit it) |
| `web/` | core elements (bx-frame, bx-terminal, bx-grants, bx-bindings, bx-multiselect, xbin-client, theme.css), served at `/vendor/` |
| `workspace-template/` | the scaffold `xbind init` stamps out — incl. the shell, admin/manager tiles, welcome tile, and the workspace `AGENTS.md` |
| `builtin-tiles/`, `builtin-templates/` | optional importable tiles / blueprints |
| `docs/` | builder docs, **embedded into xbind**, served at `/docs/` |
| `examples/` | doubles as integration fixtures and docs — keep them working |

## Dev flow

```sh
make dev            # xbind from source against ./devws — isolated, auth ON
                    # (login admin/admin), dev vault key, web/ + docs/ served
                    # FROM DISK: frontend and doc edits are live, Go changes
                    # need a restart
make dev-noauth     # frictionless: every request is admin (plaintext vault)
make test           # unit tests — fast, no network
make integration    # end-to-end; compiles real Go backends (network on first
                    # run for module downloads). Includes the container-store
                    # fs suites (test/containerfs/): those need bin/gocryptfs
                    # (`make gocryptfs`) + unprivileged userns, and skip with
                    # instructions when missing
make check          # the definition of done: fmt-check vet js-check shellcheck
                    # pins-offline test — CI runs exactly this, then integration
make hooks          # once per clone: fmt-check + js-check run pre-commit
make release TAG=vX.Y.Z   # the whole release (docs/maintenance.md → Releasing)
./hack/vendor.sh    # refresh pinned frontend deps (lit, xterm, marked) + hack/vendor.sha256
```

- `devws/` is throwaway state; never ship anything that lives there.
- Build output is ignored (`bin/`, `dist/`, `/bx`, `*/backend/backend`);
  `git status` after a build must be clean. The five embedded trees (`web/`,
  `docs/`, `workspace-template/`, `builtin-tiles/`, `builtin-templates/`)
  ship inside every xbind and are guarded by `assets_test.go`: no binaries,
  nothing over 512 KB outside `web/vendor/`, no nested repos, and **no
  `plans/` pointers** — inside those trees cite the served docs and decision
  IDs (`docs/maintenance.md` → "Embedded assets").
- Frontend has no unit-test runner: `make js-check` parses every shipped
  `.js` and inline `<script type="module">` block (part of `make check`);
  behaviour is verified by clicking through in `make dev` or the harness
  below. Say so honestly in the commit if you couldn't drive the UI.
  To *look* without a browser session, `hack/ui-harness/run.sh` seeds a
  throwaway workspace (orgs, network sets, users, org tiles in every binding
  state) and writes Playwright screenshots + `<select>` option dumps of the
  admin console, organisations tile, tile popover and terminal scope menu
  under `$HARNESS_DIR/out` (needs node + Playwright's Chromium; header of the
  script). It found real bugs on its first run — use it for UI-touching work.
- Builtin tile backends aren't part of the workspace build. To typecheck one:
  `cd builtin-tiles/<t> && cp go.mod.tile go.mod`, write a throwaway
  `go.work` with `replace github.com/xbin-dev/xbin/sdk => ../../sdk`, build,
  then **delete both** (never commit them).
- Verify before pushing: `make check` (every guard, under a minute) — and
  `make integration` when you touched the runner/sandbox/broker path. The
  guards and what each protects: `docs/maintenance.md`.

## Hard rules

- **No JS build step, ever.** Plain ES modules + import maps; vendored
  single-file deps in `web/vendor/`. TS syntax is banned in `web/`.
- **One sanctioned HTML transform** (`internal/server/static.go`). No other
  rewriting.
- `sdk/` stays zero-dependency.
- Grants/identity keep `plans/auth.md` semantics: xbind strips inbound
  `X-XBin-*`, default-deny for element principals, owner is admin.
- Never hardcode an install path in tiles OR persist one into workspace
  state — paths change under rename/clone.
- **Never break an existing workspace.** `docs/compat.md` is the contract
  and every change to shipped files, routes, manifests, `bx`, the SDK or
  boot-time migrations is checked against it: API additive-only, shipped
  URLs frozen, scaffold layouts additive (entry files keep their names, new
  siblings imported relatively, no bare import-map specifiers), theme
  fallbacks kept, CLI a superset, migrations idempotent with a fixture test.
  A silent path that must become an error warns for one release first.

## Docs discipline — docs are the contract

`docs/` is what `bx`, the SDKs, workspace agents, and every tile builder
program against; drift there becomes other people's 404s. **Update docs in
the same commit as the change**, per this table:

| You changed | Update |
|---|---|
| HTTP/WS surface (any `/api/xbin/*`, `/ws/*`) | `docs/protocol.md` **and** `internal/server/openapi.go` — `make test` fails on drift (`internal/apicheck`; rules in `docs/maintenance.md`) |
| builder-visible behavior | the relevant `docs/*.md`, `workspace-template/AGENTS.md`, and the welcome tile notes (`workspace-template/apps/welcome/notes.js`) if it teaches that area |
| a design decision | `plans/<area>.md` + an entry in `plans/DECISIONS.md` |
| a guard, budget or check | `docs/maintenance.md` (what it protects, how to satisfy it) |
| the upgrade contract | `docs/compat.md` — and a changelog line when a rule gains a warned transition |
| `bx` | `docs/bx.md` + the usage strings in `cmd/bx` |
| **anything builder-visible** | **a `docs/changelog.md` entry (see below)** |
| **a breaking change** | **also `docs/changes/YYYY-MM-DD-<slug>.md`, linked from the changelog** |

When you change a *contract*, grep for **every place it is stated** — docs,
examples, error messages, comments — and fix them all. Cautionary tale: the
instance-path contract was correct in `plans/` and in code, but one
misleading example literal (`"/api/path/prefix"`) repeated across
`protocol.md`, `elements.md`, the workspace `AGENTS.md` *and the endpoint's
own 400 message* taught two independent teams the wrong contract
(`docs/changes/2026-07-06-instance-paths.md`). The API's error strings are
documentation too.

## The changelog (`docs/changelog.md`)

Every **builder-visible** change lands a changelog entry in the same commit:
new/changed endpoints or env vars, manifest fields, SDK/`xbin.*` API, core
element behavior, builtin tile capabilities, wire/on-disk formats. Internal
refactors don't.

- Newest first, grouped by date under `## YYYY-MM-DD`, one `- area: what
  changed` line each — written for a tile builder, not for us.
- A change is **BREAKING** when existing tiles/providers must change code or
  config, or a wire/persisted format changes incompatibly. Mark the entry
  `**BREAKING**`, and add a migration note at
  `docs/changes/YYYY-MM-DD-<slug>.md` with exactly these sections:
  **What changed · Who's affected · How to migrate · Why**. Link it from the
  entry.
- These files are embedded and served at `/docs/changelog.md` and
  `/docs/changes/…` — workspace agents are told (in the workspace
  `AGENTS.md`) to check the changelog after an xbind upgrade, so this is the
  channel by which every workspace learns what moved. Keep it true.

## Commit style

Small commits, imperative subject with an area prefix (`interfaces:`,
`shell:`, `term:`, `vault:`, `install:`, `docs:` — see `git log`). The body
explains *why* and names the failure mode fixed, not just the diff. Report
verification honestly (what was tested, what wasn't).
