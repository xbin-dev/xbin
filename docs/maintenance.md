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
hack/dev-setup.sh       # once per machine (and after an upgrade): what the checks need here, fixed
make check              # fmt-check vet js-check js-test native-check theme-check shellcheck pins-offline large-files test
make integration-deps   # once, and after a pull: the helpers (prebuilt), Firecracker, xbind/bx/xbin-vmagent, .rootfs if missing
make integration        # when the runner / sandbox / broker path changed
make hooks              # once per clone: the sub-second subset runs pre-commit
```

**A machine that runs everything.** Much of `make integration` skips
quietly where the machine lacks something (a delegated cgroup, KVM, sub-uid
ranges, the previous release's xbind…), so a green local run can cover less
than CI. `hack/dev-setup.sh` checks every item first (Linux: tools, Go and
CI's gofmt, Node, a container engine, user namespaces, sub-uids, FUSE, tun,
KVM, cgroup delegation, inotify, AppArmor, WSL, the module cache, the
helpers, Firecracker, the rootfs, the downgrade binary, Playwright, Swift,
the hooks; macOS: the tools, Go, Node, Playwright and `mac-setup.sh`), says
what each gap costs, then fixes what you agree to — the system steps in one
`sudo` (`--no-root` prints that command instead), the rest as you, into
`${XDG_CACHE_HOME:-~/.cache}/xbin-dev` — and checks again. It writes
`.dev.mk` (gitignored), which the Makefile includes: the paths the tests read
(`XBIN_TEST_ROOTFS`, `XBIN_GOCRYPTFS`, `XBIN_DOWNGRADE_BIN`, the VM assets,
`PLAYWRIGHT_DIR`), `GOFMT` (CI's Go's) and `PATH` for a toolchain it
installed. `eval "$(hack/dev-setup.sh --env)"` gives your shell the same.
`make integration` runs the cgroup tests under `$(DELEGATED)`, a
`Delegate=yes` scope of your systemd user manager, where one can be made.

CI (`.github/workflows/ci.yml`) runs exactly `make check` then
`make integration`, and in a second job (`native`) the native client's
Linux half: `make swift-test` and `make swift-stubcheck` (the app's
SwiftUI/UIKit code against SDK stubs) under Swift 6.4, `make native-check` and the
iOS CI's own check (`native/ios/scripts/ci-local-check.sh`); a release
(`make release TAG=vX.Y.Z`) runs `make check` and the online pin checks
before building. Builder-visible behaviour also
needs a `docs/changelog.md` entry and the relevant `docs/*.md` update; every
non-obvious choice gets a numbered entry in the decision log
(`plans/DECISIONS.md` in the repo). Each guard below is its own Makefile
target, so a red line names the guard that failed.

| Guard | Target | Protects |
|---|---|---|
| gofmt over `GOFMT_DIRS` | `fmt-check` | formatting drift (CI's gofmt must match go.mod's minor — the pins check enforces that) |
| `go vet ./...` | `vet` | the usual |
| `node --check` over every shipped script and inline module block — a `.js` written as an ES module is checked as one (node's detection on a plain `.js` is lenient); named imports resolved against the exports of the relative / `/vendor/` module they name | `js-check` | a syntax error in a tile's inline `<script type="module">` or an unbalanced template expression in a module, or an import of a renamed or mislocated export — none is parsed by anything else before a user's browser (the trees include `website/`, so the landing page's inline module and its `js/` are covered) |
| `node --test hack/*.test.mjs` — unit tests for pure frontend modules, and for the installer's host-editing helpers (`hack/install-sh.test.mjs`: functions cut out of `deploy/install.sh` by name, run by bash against temp files), and for the prebuilt-helpers scripts and the large-files guard (`hack/helpers.test.mjs`: throwaway repos with fake build scripts, `python3 -m http.server` as the public bucket, `rclone serve s3` as the signed side when installed) | `js-test` | the helper keys, the manifest's syntax, a sha256 mismatch refused with nothing installed, the source-build fallback and its CI warning, publish's refusals and its upload round trip; the installer's AppArmor block in `/etc/apparmor.d/local/fusermount3` (replaced in place, never duplicated, other lines kept, a hand-broken file left alone) and the VM policy it writes (xbind's field names, never over an existing file); the shell's context-menu builders (`shell/menus.js`), revisioned-draft helpers (`shell/rev-draft.js`), grid math (`shell/grid-layout.js` — the push a drag performs) the terminal's prediction engine (`web/term-predict.js` — what a keystroke predicts, what an ack confirms), the frame's view of the session directory (`web/term-sessions.js` — how a listing becomes the tab bar, how the legacy browser record is adopted), the terminal window's live reload view (`web/deploy-state.js` — which chip, menu items and API select entries a state and a viewer's permissions yield, and every string they show), the Deployments panel's view (`web/deploy-panel.js`), session targets and deployment frames (`hack/deploy-target.test.mjs`), how the binary-served components treat a tile deployment (`web/frame-info.js`, `web/term-sessions.js`, `web/xbin-client.js`: `hack/deploy-aware.test.mjs`), and old clients as fixtures (`hack/events-socket.test.mjs`: the web shell's reload targeting exactly as it ships, replayed against a deploy's event tape, so no frame of an old event type names a non-primary deployment): every branch a menu can show, how a stale save is classified, where a pushed tile lands, which predictions survive, which tabs a listing yields — without a browser |
| the native client's contract: xb-native's own tests, the fixture runner's, and every `native/fixtures/<name>` rendered in node and compared with its `expected.json`, plus the vocabulary coverage gate | `native-check` | the tree a tile's `native.js` renders — what the app's renderer and the reference renderer draw — drifting unreviewed, and a vocabulary item no fixture exercises (native/fixtures/README.md) |
| shellcheck at warning level over `deploy/`, `hack/`, `.githooks/`, the site's `website/install.sh` bootstrap and the iOS CI scripts in `native/ios/scripts/` | `shellcheck` | the installer and release scripts (1,600 lines of bash; only the installer's host-editing helpers have unit tests, above); the iOS CI scripts, which only a macOS runner executes |
| vendor checksums, Go-version agreement, alpine pins, the helpers manifest (well-formed; each group's current key published — a warning, never a failure) | `pins-offline` | pins drifting apart between the files that state one; build inputs changed without a published helper set (see "Prebuilt helpers") |
| nothing in the index over 1 MiB, no ELF / Mach-O / PE binary, unless `hack/large-files.allow` names it with a reason (`hack/check-large-files.sh`; also in the pre-commit hook) | `large-files` | a built binary or a big blob committed by accident — history keeps it forever; helpers live in the bucket, bundles on release tags |
| unit tests incl. the embed guard, route inventory and route classes, docs check and wording guard, the exec, cgi and no-following-walk guards, the zero-state goldens | `test` | see the sections below |

Not in `check`: `make swift-test` runs the native client's Swift packages
(`native/ios/Packages/*`, Foundation only) on any machine with a swift
toolchain, Linux included — ci.yml's `native` job installs one to run them,
and the Apple CI (`.github/workflows/ios.yml`) runs them on macOS; `make
tile-check` needs the network (CI runs it).

## Exec guard (nothing runs as xbind on tile data)

`internal/confine` `TestNoDirectExec` fails `make test` when daemon code
(`internal/`, `cmd/xbind`) starts a program — `exec.Command`,
`CommandContext`, `exec.Cmd{}`, `os.StartProcess`, `syscall.Exec` — outside
`internal/confine` (the confined run), `internal/sandbox` (the sandbox
itself) and `internal/agent/host` (runs inside a sandbox), unless the call
carries `// exec-ok: <reason>` on its line or the two above. Tools on
workspace data — git on a tile, `go build` of a backend — go through
`confine.Git` / `GitRead` / `Run`, which sandbox them when isolation is on
(D78, [isolation.md](/docs/isolation.md) → "Confined tool runs"). An
`exec-ok` is for isolation-off paths and input only xbind writes; a reviewer
reads each one. The sandboxed half is exercised by `go test -tags=integration
./internal/confine/ ./internal/runner/` (needs `.rootfs` + user namespaces).

Its sibling `TestNoCGIHandler` fails when daemon code imports `net/http/cgi`
— no exemption, no annotation. `cgi.Handler` execs a program from inside the
standard library, where the regex above cannot see it; it is how the removed
`cgi` runtime ran a tile's handler on the host as xbind (D117). Tile code
runs in a backend's sandbox, never through the proxy.

`TestNoFollowingHostWalks`, in the same package, keeps xbind from following a
symlink a tile planted: inside a work tree, a resource mount, an extracted
checkpoint or a quarantine, a host-side `os.Open`/`OpenFile`/`ReadFile`/
`ReadDir`/`Stat`/`Chmod`/`Chown` or `filepath.Walk`/`WalkDir`/`EvalSymlinks`
would follow wherever the tree's writer points it (and an open blocks on a
FIFO). In the packages and files that open those trees
(`internal/checkpoint`, `internal/deployments`, and the deployment files of
the registry, server, broker and runner — its `nofollowScope`) every such
call carries `// walk-ok: <why>` on its line or the two above (an
xbind-owned file under `data/deployments`, say); `fsutil.OpenBeneath`/
`OpenIn`, `os.Lstat` and `os.RemoveAll` need none. Its behavioural half
drives checkpoint capture, extraction and GC, the drift count and the
backup of a checkpoint store through a hostile tree aimed at a FIFO. And
`TestIntegrationPackagesListed` fails when a package with
`integration`-tagged tests is missing from `make integration`'s list, which
is how `internal/sandbox`'s tests once went unrun.

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

`TestDeploymentWording` (same package) guards the vocabulary of tile
deployments and live reload: the prose strings of the terminal window's
modules (`web/deploy-state.js`, `web/deploy-panel.js`, `web/frame-deploy.js`,
`web/bx-deploy.js`) and of bx's live-reload, deploy, promote, rollback and
deployment commands, the builder page
`docs/tile-deployments.md`, and every docs section whose heading names live
reload or tile deployments may not use a word another feature already owns —
identity, instance, environment, snapshot, version, preview, slot, "pause
the tile", ⏸ … (the list in the test, each with what to say instead). It
also wants the builder page linked from `docs/index.md` and
`docs/elements.md`, and "no deploy step" still said in the scaffold's
`AGENTS.md`: a tile that never opts in has none. A new user-visible file of
the feature joins `wordingSources`.

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
  an in-tree binary — Go backends compile into `.xbin/build/`; a compiled
  file not named after its directory (a tool a tile ships under `bin/`) is
  never matched.
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

`TestLegacyWorkspaceNoDeploymentState` (beside it, and in-process as
`TestLegacyWorkspaceNoDeploymentStateInProcess`) boots the same aged
workspace and asserts that tile deployments add nothing to it: no
`data/deployments/`, `data/checkpoints/` or `.xbin/deploy/`, the cron and
bus-subscription stores byte-equal, an old tile repository's HEAD, config
and refs untouched. New assertions go in new functions reusing the
fixture's helpers; the fixture and its allowed list don't change.

The same fixture boots **in-process** in `internal/boot`
(`TestLegacyWorkspaceBootsTwiceInProcess`, part of `make check`, 0.2 s):
`boot.Run(ctx, cfg)` with a `:0` listener, `NoPrivileges`, and a cancel
once `/healthz` answers. Extend both when a migration lands — the
in-process one is the fast loop, the binary one proves the packaging.

## Boot (`internal/boot`)

`cmd/xbind/main.go` parses the command line and owns the log level, the
signals and the exit code; everything else is `boot.Run(ctx, cfg)`.

- **`boot.Config` is the one list of settings.** Its struct tags declare
  the flags (`RegisterFlags`: a flag's default is its env value, so the
  precedence is flag > env > default) and render
  [config.md](/docs/config.md); `TestConfigDoc` fails when the page is
  stale — `UPDATE_DOCS=1 go test ./internal/boot -run TestConfigDoc`
  rewrites it. A setting another package reads where it is used gets a
  `readBy` tag instead of a code move, so the reference stays complete.
  Adding a flag or variable: a field with tags, then regenerate the page.
- **`boot.Steps` is the boot order.** Sixteen named stages; the edges
  that carry migrations (workspace before privileges, users before homes,
  registry before broker, …) are comments on the list and assertions in
  `TestStepsOrder`. A new migration is a step (or a line in the step that
  owns its data) plus a line in the fixture test's allowed list.
- **Nothing in `boot` exits or handles signals.** Failures are errors
  prefixed by the step name; the console listener, the privilege drop
  (`Privileges`), a `Ready` callback and stdout are injectable, which is
  what lets a test boot a workspace in-process. `Broker.Close` releases
  what a boot holds (the KV database's file lock, cron, the disk
  monitor) so the same process can boot the workspace again.
- **`ResolveVaultMode`** is the one pure decision (passphrase → env
  unseal; `--insecure-vault`/`--no-auth` → plaintext; `--dev` → dev key;
  else sealed or locked), table-tested.

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
- **The broker is being split into planes** (D63). `internal/obs` was
  the first: tile status reports, per-user prefs and backend logs as a
  `Plane` whose fields are exactly the answers it needs from the rest
  (the workspace root, the hub, `IsAdmin`, `HasComponent`); the broker
  builds and mounts it in `Register`, and its tests run on a fixture of
  those answers, not a whole broker. Lifting the next plane: pick a file
  set that touches only its own state (the per-file field map in the
  decision), give it a struct with those answers as fields, move the
  files and their tests, keep every `RegisterAPI` literal (the route
  inventory scans `internal/`), and expect a wire-identical diff.
- **What the broker decides for the server is one interface,
  `server.Policy`** (`internal/server/policy.go`): admin status, tile
  owners, bus visibility, the interface meta a document gets, the sandbox
  tokens its grants unlock, code grants on the static plane. The broker
  installs `brokerPolicy` at boot (`InstallPolicy`); a server without one
  runs `NoopPolicy` — owner-only and fail-closed — which is also what
  tests start from (embed it and override one answer). A new question the
  server must ask the broker is a method on `Policy`, never another
  nullable func field.

## Tile deployments (`internal/deployments`, `internal/checkpoint`)

Pausing live reload and the deployments plane ([tile-deployments.md](/docs/tile-deployments.md))
are opt-in per tile, and the zero state — a tile that never opted in — must
stay byte for byte what it was.

- **The zero-state goldens are hand-maintained.** `TestZeroState*` (the
  listings, documents, `/components` entries, tokens, storage keys, launch
  specs, backend env, backup members, a tile's life on the real binary) and
  `TestZeroStateCreatesNoDeploymentFiles` pin today's bytes, with no update
  switch. A failing golden is a compatibility change, not a stale fixture:
  never regenerate one to make a change pass.
- **The deployment record,** `data/deployments/<key>.json`, is xbind's
  (terminals don't see it); never edit it by hand. A record that can't be
  read, fails validation or was written by a newer xbind holds its tile —
  its backend doesn't start and it serves nothing until the record is fixed
  or removed, and xbind logs why at startup. A record made for another owner
  (a transfer under an older xbind, say) is ignored until an admin removes
  it. Creating a tile removes any record left at its path.
- **The checkpoint store,** `data/checkpoints/<key>.git` (and its
  `.view.git`, the only thing the fetch remote serves), is written and read
  only by confined git. Extracted checkpoints live under
  `.xbin/deploy/<key>/<full tree id>/`, directories 0755 and files 0444 or
  0555, so `rm -rf .xbin` still works while xbind is stopped and trees are
  rebuilt on demand; a `.tmp-*` entry is an interrupted extraction, safe to
  delete while xbind is stopped. Kept builds are
  `.xbin/build/<key>/c/<tree>/{bin,build.json}`.
- **Retention** runs after each successful deploy of a tile with a record,
  in the goroutine that finishes it (so a deploy's waiters, and a static
  tile's reload, wait for it): the store's GC (every deployment's current
  checkpoint, its last 20 successful deploys and anything younger than 24 h
  kept; deploy logs trimmed to 50 entries; the first GC after a start also
  repacks), then the pruning of kept builds (each deployment's current
  checkpoint and its three newest other successful ones). Once a record
  governs some tile, boot sweeps the `.tmp-*` extractions killed runs left.
  A failed or cancelled deploy collects nothing, and a tile without a record
  is never touched.
- **Named deployments' state** lives beside `main`'s, never in it: each
  deployment's registration files (cron jobs, bus subscriptions,
  interface instances, ingress hosts, a backup schedule) under
  `data/deployments/<key>/<name>/`, written through the plane's records lock
  (`idx.writeIn`, so one tile's opt-out never removes the directory under
  another tile's write); its data under
  `data/resources-enc/.deployments/<scope>/<name>/` (`kv.db`, `fs/<res>/`),
  mounted from `.xbin/resenc/.deployments/…`; its vault at
  `data/vault/.deployments/<key>/<name>.json`; prefs at
  `data/prefs/<user>/.deployments/<key>/<name>.json`; its backend log at
  `.xbin/deploy/<key>/d/<name>/backend.log`. `main` keeps today's keys and
  stores byte for byte. Never add a non-`main` row to today's stores
  (`data/cron-jobs.json`, `data/bus-subscriptions.json`, the root
  `xbin.json`): an older xbind would load it as `main`'s and fire it.
- **The primary is a role, and old types speak only of it.** `Runner.Ensure`
  stays the primary's funnel, with `EnsureDeployment` and friends beside it;
  `TestEnsureCallSitesPassPrimary` pins the callers that still mean the
  primary. `reload`, `build-*`, `status` and notify never carry a
  non-primary deployment or a qualified `component`: its facts ride the
  `deployments` event, delivered to the tile's write audience only
  (`TestEventBytesZeroState` and the old-client fixtures above). `main`'s
  sandbox registry rows keep their ids (`backend:<key>:g<gen>`); another
  deployment's are `backend+<name>:<key>:g<gen>`.
- **Route classes.** xbind's API is default-deny for a non-primary
  deployment's credentials: every `/api/xbin/*` route has a class in
  `internal/server/deployclass.go` — deployment-scoped, primary-only or
  neutral — and `internal/apicheck`'s `TestDeploymentRouteClasses` fails on
  a route without one (and on a row naming no route);
  `TestDeploymentRouteClassesLive` drives the refusals with real
  credentials. A new route gets its row in the same commit (§Route
  inventory).
- **Isolated integration tests** (the checkpoint, runner and broker packages'
  confined tests, `startIsolatedDaemon` under `test/`) need
  `XBIN_TEST_ROOTFS` — a rootfs whose image has git — user namespaces and a
  shell whose own sandbox allows re-executing `/proc/self/exe`; without them
  they skip with the reason.
- **The downgrade and latency knobs.** `TestDowngradeStatic` and
  `TestDowngradeDormantRegistrations` run the previous release's xbind,
  named by `XBIN_DOWNGRADE_BIN`, on a workspace this tree left deployment
  state in; without it they skip, and they skip against a binary that
  already speaks tile deployments. CI extracts `bin/xbind` from the release
  bundle of the last release before tile deployments; locally, build it
  from the tag (`git archive <tag> | tar -x -C <dir>`, then
  `go build ./cmd/xbind` there). `XBIN_TEST_FULL=1` takes 30 samples of
  every latency row, and with `XBIN_BASELINE_BIN` (the same previous
  release) the save budgets are compared back to back with it.
- **Shipping dark.** `--tile-deployments=off` (`XBIN_TILE_DEPLOYMENTS`)
  closes opting in, enforced in the plane's one authorize function: a
  release that must ship before the feature is ready carries it off, and
  the next release turns it on with a changelog line. Off never unpins
  anything, and resuming live reload onto `main`, restarting, removing a
  deployment, resetting its data, unprotecting, run now and purging stay
  allowed (the acts `grows` doesn't mark, in `authz.go`), so every tile can
  return to the zero state without a downgrade. `Plane{}` in a test is
  open.

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
- `internal/assetscan` `TestShippedTilesPassStrictGating`: every tile xbin
  ships — the scaffold's, builtin tiles and templates, the examples — loads
  under strict tile asset gating (docs/auth.md §Tile asset gating): no
  absolute `/c/` reference a strict mode refuses, no `inject: false`, no
  symlink leaving the tile. Reference a tile's own files relatively; `bx fix
  assets <tile> --write` rewrites what the guard names. Chrome (root, shell,
  `chrome: true`) is not gated and not scanned.
- A tile's `tile.json` `version` bumps whenever its files change, with a
  changelog line — that is how `bx builtin updates` offers the update.
  `internal/builtins` `TestTileVersions` enforces it: `hack/tile-versions.txt`
  records each tile's version and a rollup hash of its files (tile.json
  excluded); a changed hash with an unchanged version is a red line. After
  the bump, `UPDATE_TILE_VERSIONS=1 go test ./internal/builtins -run
  TestTileVersions` moves the baseline.
- The pure cores have tests that run under `make tile-check`: traefik's
  static/dynamic config renderers are pinned verbatim, the egress approver's
  packet decoding likewise.
  Response bodies go through `xbin.WriteJSON` / `xbin.WriteError` from the
  SDK — a backend defining its own `writeJSON` is a copy to delete.

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

## The native reference (`docs/native.md`)

The builder reference for a tile's `native.js` is partly generated: the
primitive tables, the controlled props, the tokens, the icons and the
feature flags come from the vocabulary (`web/xb/vocab.js`) through
`node hack/native-docs.mjs --write`, between `<!-- generated:… -->`
markers. `hack/native-docs.test.mjs` (`make js-test`) fails when those
blocks are stale; when a template in the page's examples — or in the
workspace AGENTS.md's "Native app UI" section — uses a tag, prop, event or
enum value the vocabulary lacks; when the quick start drifts from
`examples/counter-go/native.js` or stops rendering the tree printed under
it; when a complete example renders with errors or warnings; and when an
`xbin.native` member or an `xb-native.js` export goes undocumented. So a
vocabulary change lands with its docs: edit `vocab.js`, then regenerate
`native/spec/vocab.json` and the page.

## The admin console's tabs (`workspace-template/tiles/admin`)

`admin.js` is the router: the two-level nav (`GROUPS`), hash deep-links and
their alias map, `_refresh()` (the shared lists: overview, users, orgs,
policy, sets, defaults, requests, sessions), the global `.err` /
`.notice` slots — about 300 lines. Every tab is its own element under
`tabs/<name>.js` (`runtime` for components + the code drill-in + live
stats + resources, `sandboxes` for every sandbox, the host's isolation and
VM health and the VM policy, `deployments` for every tile's deployment
record and a tile manager's protect / reassign / deliveries / alwaysOn — its
words and confirmations imported from `/vendor/deploy-state.js` and
`/vendor/deploy-panel.js`, never copied — `map`, `netsets`, `permsets`, `vault`, `cron`,
`backup`, `binding` for grants/roles/providers/wiring, `ingress` for
expose/endpoints, `orgs` for the org list, one org's page (`#orgs/<id>`
— the router passes the hash's `sub` down), policy ceilings and the
workspace defaults, `users`, `signin`, `sessions`); the
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
  `[data-set]`, `[data-netset]`, `[data-edit-allow]`, the sandboxes tab's
  `[data-sbx-*]` / `[data-vm-*]` / `[data-tsbx-*]` (its tile sandboxes
  part, `tabs/tilesbx.js`) and the components tab's
  `[data-sbx-cell]`, …) and, if it holds
  drafts, is routed to from the router's `testApi()` by key namespace
  (`permset:`, `netset:`, `bindcustom:`, `orgallow:`/`ws:`, `user:`).

Adding a tab: the element under `tabs/`, an entry in `GROUPS`, one arm in
`render()`, and the `adminTabs` harness pass opens every id in `GROUPS`
(it reads them from `BxAdmin.tabsFlat()`) and fails on an empty or `.err`
body (`hack/ui-harness/shots.js`). The `sandboxes` pass
(`hack/ui-harness/passes/sandboxes.js`) checks the sandboxes tab live —
the harness runs without `--isolate`, so it sees host-mode sandboxes and
refused VMs — and against a routed VM-capable host. The `adminDeployments`
pass (`hack/ui-harness/passes/admindeploy.js`) drives the deployments tab
inside the shell on the deployments pass's fixture: protect, deliveries,
unprotect and the reassign confirmation, each through the admin tile's
frame, and the link to the tile's Deployments panel.

## The shell (`workspace-template/shell`)

`bx-shell.js` is being taken apart the same way as the admin console: its
stylesheet is `shell-css.js` (`shellCss`), every pointer drag goes through
the kit's `dragPointer`, and the context menus are pure builders in
`menus.js` — `canvasMenuItems(state, actions)`, `tileMenuItems(path,
state, actions)`, `openTileItems` — over the plain state view and the
actions object the shell assembles in `_menuState()` / `_menuActions()`.
A new menu line is a builder change plus a case in `hack/menus.test.mjs`
(`make js-test`); the module imports nothing, so it runs under node. The
three revisioned-draft flows (org screens, shared folders, share-to-org;
D55) share `rev-draft.js`: `newDraft`/`withDraft`/`withoutDraft` for the
draft maps, `publish(url, body)` classifying a PUT's answer into
`ok | conflict | error | offline`, `conflictDialog(…)` for the "someone
saved first" dialog, and `ago()` — tested in `hack/rev-draft.test.mjs`
with an injected `fetch`. `zorder.js` is the one z counter floats and
spawned windows share (`nextZ()`, `raiseTo(z)`). `grid-layout.js` is the
grid module (`GRID`, `GAP`, `DEF_W/H`, `MIN_W/H`, `snap`) plus the pure
layout math — `overlaps()` and `pushLayout()`, the push a dragged or
resized tile performs on its neighbours (D66) — lit-free, tested in
`hack/grid-layout.test.mjs`; `shell-kit.js` re-exports the constants and
holds the rest of what the shell and its children share: `RUNTIME_COLOR`,
the `LongPress` gesture, `selectedText()` and the `prBadge()` template.
`layout-sync.js` is how an open shell follows a layout another client (the
app, another tab) saved: `follow(shell, event, key)` on a `prefs` event —
skip our own writes (`X-Prefs-Writer`), hold the reload while `editing()`
— tested in `hack/layout-sync.test.mjs`, end to end by the harness's
`layoutSync` pass.

The first child element is `bx-canvas.js` — the snappable grid of cards
and the floating windows, with every pointer gesture on them (grid
drag/resize, float drag/raise/resize-commit, pin/unpin, long-press and
right-click). The tiles array is a property; each geometry change comes
back as one `bx-tiles` event carrying the new array, which the shell
persists (`_mutateTiles`). What needs the shell arrives as events —
`bx-tile-menu`, `bx-canvas-menu`, `bx-admin-win`, `bx-toggle-tile` — and
the shell reaches the cards through `frameFor` / `frameOpen` / `frames`
/ `rectOf` / `raiseFocusedFloat` / `togglePin`. Its stylesheet is the
`canvasCss` slice of `shell-css.js` (the badge rule, `prbCss`, is shared
with the sidebar rows). A `.pop`, `.card` or `.float` locator in the
harness still resolves: Playwright's CSS engine pierces open shadow
roots.

The sidebar is `bx-side.js`: the owner-sectioned tree (personal folders,
each section's shared curated folders, tiles, org screens), the filter
and owner filter, the show-hidden toggle, the organisations button and
the admin footers. It is coupled to more of the shell's state than props
and events would carry cleanly, so it renders from one `state` view the
shell builds in `_sideState()` and acts through one `actions` object
from `_sideActions()` — every mutation (filing, folders, drafts,
screens, opening a tile or a menu) stays the shell's, which keeps one
owner for layout persistence and the draft flows. Its own state is the
filter text and the drag-hover highlights. The host element is the
aside: the shell sizes it and marks it `drawer`/`open` on phones; its
stylesheet is `sideCss`, with `statusCss` (level colours, dot, breathe)
shared with the screen tabs. `shell-kit.js` also carries the pure tree
helpers both sides use (`isScreenItem`, `screenIdOf`, `scopeOf`,
`sectionOf`, `ownerKeyOf`, `worstStatus`). Next: `bx-screens`,
`bx-toasts` the same way. `bx-shell.js` keeps its name and imports the
siblings relatively (compat rule 4). The harness passes `windows`, `screens`,
`menus`, `mobile`, `reloadFocus` and `contextCopy` are the gate for every
slice.

## UI harness (`hack/ui-harness`)

The frontend has no unit-test runner; browser behaviour is pinned by
`hack/ui-harness`: `run.sh` builds xbind, seeds a throwaway workspace
(orgs, network sets, users, org tiles in every binding state, the
`focusy`, `linky`, `reloady` and `deployy` fixture tiles, the agent template wired to
`hack/fakeopenai` through llm-gw and to `apps/fakesbx` — `hack/fakesandbox`
as a sandbox manager tile) and runs Playwright passes from
`shots.js` — screenshots and `<select>` dumps to look at, plus asserting
passes that write `PASS`/`FAIL` lines under `$HARNESS_DIR/out/<pass>.txt`
and exit 1 on any FAIL. A part the environment cannot exercise writes a
`SKIP <reason>` line instead (`checker().skip`, echoed at the end of
`run.sh`) — never a timeout, never a silent pass: without a `gocryptfs`
binary (a fresh worktree has no `bin/gocryptfs`: `make helpers`, `make
gocryptfs`, or `XBIN_GOCRYPTFS`) the seeded agent tiles are held, so `agentTemplate`,
`agentConvs`, `agentSandbox` and `channels` skip; a harness xbind that can
run VM sandboxes skips `vmToggle`'s disabled-toggle half. `livereload` pauses,
reloads now and resumes live reload on the static `reloady` from the
terminal window; the harness xbind runs without `--isolate`, so on a node
backend it asserts the isolation refusal and skips pausing it (`SKIP …
needs HARNESS_ISOLATE`). `HARNESS_ISOLATE=1` — with `XBIN_TEST_ROOTFS`, user
namespaces, and the agent's own tool sandbox off — runs that half.
`deployments` drives the terminal window's Deployments panel on the static,
org-owned `deployy` (so it needs no `--isolate`): a manager, a terminal-level
user and a reader add `dev` with live reload attached, see the rows, the
tile API select's targets and the reader's filtered view; promote, roll
back, set an edge, read registrations and "would notify", protect the
primary, and restore the zero state at the end — a part this xbind can't
exercise prints `SKIP` with what it got. The
agent passes don't run under `HARNESS_ISOLATE`: their scripted fake agent is
a host path the tile sandbox can't see.

```
hack/ui-harness/run.sh                    # build, fresh workspace, seed, every pass, stop
hack/ui-harness/run.sh --keep             # …and leave xbind up on $PORT
hack/ui-harness/run.sh --shots windows    # one pass against the running instance
hack/ui-harness/run.sh --restart          # rebuild xbind, same workspace, every pass
HARNESS_ISOLATE=1 hack/ui-harness/run.sh --keep livereload   # xbind with --isolate on $XBIN_TEST_ROOTFS
hack/ui-harness/app-help-shots.sh         # the iOS app's help screenshots (native/AGENTS.md)
(cd hack/ui-harness && node shots.js --list)
```

Rules that keep it cheap to maintain:

- **Passes drive the elements' `testApi()`** — `bx-shell`, `bx-frame` and
  `bx-admin` each expose stable names over their private state (open a
  tile, set a float, open the admin window, read the toasts…; the frame's
  `layouts` is what its layout switcher offers — a pass compares against
  it, never a count — `reloads` the reloads it completed, and `deploy` its
  live reload controls: `state`, `chip`, `offer`, `entry`, `banner`,
  `chipItems()`, `chipAction(label)`, the tile API select's
  `target()`, `apiOptions()` and `setTarget()`, `panel()` — the Deployments
  panel's own `testApi()` — and `refresh()`). A pass
  never touches a `_member` or walks `shadowRoot` by hand; `make js-check`
  fails on `._x` in that directory. When a refactor renames state, only
  `testApi()` moves. The surface reads and writes existing state — no
  test-only branches in production code.
- **DOM hooks are part of the markup contract**: `.card[data-path]`,
  `.item[data-path]`, `.float[data-path]`, `.ghost[data-path]`, `.spawn`, `.admin-pop`,
  `details[data-sec]`, `.netsetcard[data-netset]`, `.setcard[data-set]`,
  `[data-new-set]`, `[data-save-set]`. Keep them when restructuring
  templates. Playwright selectors pierce open shadow roots, so
  `bx-frame[src="apps/x"] .pop` reaches into a frame.
- **Terminal windows**: the title bar's pickers live on the bar or, when
  that host's full bar does not fit the window (a GPU picker, the VM
  toggle), in the tools row behind `⋯` — `showPickers(page, src)` opens
  that row and `PICKERS` scopes a selector to either place; never assume
  `.titlebar select.scope`. A pass that opens a window leaves nothing
  behind: it ends its sessions and deletes the `term:<tile>` window pref,
  or the window restores over the tile in the next pass (a right-click
  on the canvas then lands on it) — and the `termvm:<tile>` VM choice if
  it set one, or the next pass's sessions start in a VM.
- **Wait for a condition, never for time**: `waitFor(page, (t) => …)`
  polls the shell's test surface, `waitSel` a selector, `settle` two
  animation frames after a state change. A fixed `sleep` is only right
  for a negative ("no menu appears") or a timer inside the element.
- One `lib.js` holds login, the in-page plumbing (`sh`, `fr`), waits,
  screenshots and the checker; a new pass is a function added to
  `PASSES` in `shots.js` — as its own module under `passes/<name>.js`
  (`shots.js` is at its size budget; `passes/users.js` is the model).
- **Headless Chromium hides scrollbars** (`--hide-scrollbars`): every
  scroller measures 0px wide and screenshots show no bars. A pass about
  them launches its own browser without the flag — `scrollbars` (D123: the
  6px bars, the focused-scroll tint across the shell and a tile document).
  `agentLong` (D124) streams the fake agent's `long N` script (hack/fakeacp)
  and pins the Agent tab's window: what is rendered, pages loading at the
  top without moving the row being read, a reload and a past session
  opening windowed at the bottom.

On this box: `PLAYWRIGHT_DIR=~/lcad-wasm` (Playwright + its Chromium) and
`HARNESS_DIR` somewhere outside the repo.

## Route inventory (routes ↔ OpenAPI ↔ protocol.md)

`internal/apicheck` mounts the broker on a server exactly as the daemon does
(plus the runtime handlers `internal/boot/api.go` registers, read from its
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

Adding a route is therefore four edits in one commit — `RegisterAPI` (or
`Handler`), an `openapi.go` row, a `protocol.md` row, and a row in
`internal/server/deployclass.go` (its class for a non-primary tile
deployment's credentials, §Tile deployments) — and `make test` says which one
you forgot.

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
toolchains, the traefik release the builtin tile fetches, and the VM
sandboxes' Firecracker release, guest kernel tarball and the Firecracker
guest config it builds on).

VM sandbox pieces (D89) carry their own pins: `hack/fetch-firecracker.sh`
(version + per-arch sha256 of the upstream static release),
`hack/build-vmkernel.sh` (kernel version + sha256 from kernel.org's signed
sums, Firecracker's CI config by tag + sha256; `hack/vmkernel/xbin.config`
is merged on top and every line must survive `olddefconfig`), and
`hack/build-mkfs-erofs.sh` (erofs-utils tag, alpine pin), and for emulated
VMs (D90) `hack/build-qemu.sh` (QEMU version + tarball sha256; the device
set is in the script) and `hack/build-vhost-vsock.sh` (crate version,
`--locked`). Bump Firecracker and the config tag together — `make pins` warns
when they differ. CI gets them with `make helpers` (next section) and the
Firecracker fetch, turns on KVM for the runner, and boots the VM
integration tests on a small exported ubuntu image twice: on KVM, then with
`XBIN_VM_ACCEL=emulate`.

## Prebuilt helpers (`make helpers`, D126)

xbind runs native helpers it doesn't compile: the **containerfs** group
(patched static `gocryptfs`, static `fuse-overlayfs`) and the **vm** group
(the guest `vmlinux`, static `mkfs.erofs`, the TCG-only
`qemu-system-x86_64` with `qemu-bios-microvm.bin` + `qemu-pvh.bin`,
`vhost-device-vsock`; amd64 only). Every one is built from source by its
`hack/build-*.sh` (docker, pinned, above) — that stays the source of
truth. The prebuilt sets are a verified cache of those builds, so CI and
developers don't compile a kernel and QEMU on every cold cache.

- **Key.** Each group's key (`hack/helpers-lib.sh`) is the first 12 hex of
  a sha256 over its build scripts (their pinned versions and checksums live
  inside them), its patches (`hack/gocryptfs-patches/`,
  `hack/gofuse-patches/`) or `hack/vmkernel/xbin.config`, and its file
  list. Any input change is a new key. `hack/fetch-helpers.sh --status`
  prints the keys.
- **Manifest.** `hack/helpers.sha256` (committed) pins every published
  set: `<group> <key> <arch> <file> <sha256>`, where `<file>` is
  `<arch>.tar.zst` (the bucket object) or a file inside it. Only
  `make helpers-publish` writes it.
- **Where they're served.** Any plain-HTTPS origin laid out as
  `<url>/<prefix><group>/<key>/<arch>.tar.zst`. `make helpers` reads only
  that URL and prefix — `HELPERS_URL_DEFAULT` / `HELPERS_PREFIX_DEFAULT` in
  `hack/helpers-lib.sh`, overridden by `XBIN_HELPERS_URL` /
  `XBIN_HELPERS_S3_PREFIX`; no credentials. **For now it's the website:**
  `https://xbin.dev/static/helpers` (static files, deployed with the site);
  an S3-compatible public bucket (`make helpers-publish`, `s3secret.env`)
  can replace it later by changing those two defaults. Helper binaries
  never go on GitHub (only release tags carry binaries there) and never
  into git (`make large-files`).
- **Publishing to the website** (maintainers): `hack/publish-helpers.sh
  --stage-only DIR` (build + pack from a clean tree), then
  `hack/helpers-static.sh DIR` (checks each staged tarball, copies it to
  the gitignored `website/static-helpers/`, merges the entries into
  `hack/helpers.sha256`), `make website` (→ `website/dist/static/helpers/`),
  deploy the site by hand, then commit the manifest. Until the site serves a
  set, `make helpers` falls back to building it from source; `make pins`
  (online, and so a release) fails on a pinned set the URL doesn't serve —
  deploy first.

**Developers and users.** `make helpers` (or `make integration-deps`, which
adds Firecracker, xbind/bx/xbin-vmagent from the tree, and `.rootfs` when
missing) does, per group, for this arch (`PLATFORM`'s, else the host's):
`bin/.helpers-<group>` names the current key → nothing to do; the manifest
pins the current key → download, check the object's sha256 and every
file's (a mismatch is fatal and installs nothing), install; otherwise → build
from source with the group's scripts, saying why. The source build also
happens when a download fails (with a warning) and when a version override
the scripts read (`QEMU_VERSION`, `NATIVE=1`, …) is set. `HELPERS=vm`
narrows it to one group. To rebuild everything from source regardless:
`make helpers-build` (the Makefile's own `make gocryptfs fuse-overlayfs
vm-assets` targets still work too). A published key with no URL configured
fails with what to set.

**CI.** The test job runs `make helpers HELPERS=containerfs` before `make
check` and `make helpers HELPERS=vm` before the VM suite, under one
`actions/cache` of `bin/` keyed on the manifest and every build input — a
repeat run neither downloads nor builds. A PR that changes a group's inputs
builds that group from source and carries a `::warning` annotation to
publish it; `make check`'s pins-offline says the same. Neither blocks: a
maintainer publishes, the manifest commit makes CI fetch again.

**Maintainers: publishing.** On a machine with docker and the publisher's
credentials, never in CI:

1. `cp s3secret.env.example s3secret.env` (gitignored), fill it in,
   `chmod 600`; `hack/check-s3.sh` proves it (signed put, anonymous public
   get, signed delete).
2. On a clean tree (the key must describe committed inputs):
   `make helpers-publish` [`HELPERS=vm`] — builds each group whose current
   key the manifest lacks, packs `<arch>.tar.zst`, uploads it with curl
   `--aws-sigv4` (credentials on curl's stdin, never a command line),
   downloads it back from the public URL to compare, and rewrites
   `hack/helpers.sha256`. An object already at that key is never
   overwritten (builds aren't byte-reproducible; a committed manifest may pin
   it): its entries are taken from it instead.
3. Commit `hack/helpers.sha256`.

`hack/publish-helpers.sh --stage-only DIR` builds and packs into a
bucket-shaped `DIR` without uploading; `--staged DIR` uploads that later.
It refuses when `s3secret.env`'s URL/prefix differ from the lib's defaults
(`make helpers` would look elsewhere), and notes while the defaults are
still empty.

**Releases** keep building every helper from source
(`deploy/publish-release.sh` → `make build`/`vm-assets`), so a bundle never
depends on the bucket. An unpublished key doesn't block a release — it only
means CI builds from source — and `make pins` (run by every release) warns
about it, and fails when a published set's object isn't served.

## gofmt scope

`make fmt-check` runs gofmt over `GOFMT_DIRS` (Makefile): the root `*.go`,
`cmd`, `internal`, `sdk`, `test`, `builtin-tiles`, `builtin-templates`,
`examples`. It is explicit so gofmt never walks `devws*/` or `.rootfs/`
(a whole distro of files). When Go sources appear in a new top-level tree,
add it there. `make fmt` rewrites in place.

## Build output that must not be committed

`bin/`, `dist/` (release bundles), the root `bx`, `website/dist/`, packed
helper sets (`*.tar.zst`), and `*/backend/backend` / `*/_backend/_backend`
build leftovers are ignored; `make large-files` (in `check` and the
pre-commit hook) fails on anything over 1 MiB or any native binary that gets
into the index anyway.
`git status` after a build should be clean; if it is not, the fix goes in
`.gitignore` (and in `assets_test.go` when the path is under an embedded
tree), not in a commit.
