# Verification and delivery infrastructure for the implementation swarm

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `delivery`.

## Report

## Brief 4: verification and delivery infrastructure for the implementation swarm

Facts carry file:line references. **(inference)** marks a conclusion drawn from them.

### 0. The key facts first

1. **Definition of done.** `make check` (Makefile:136) is the definition of done and takes under a minute. CI (`.github/workflows/ci.yml:41-43`) runs `make check`, then `make tile-check`, then `make integration`. **The UI harness is not in CI.** Each work package (WP) must run its harness passes locally and report the result honestly.
2. **The code this feature changes has no unit tests.** That code is the runner state machine and the watcher. `internal/watch` holds only `watch.go`. `internal/runner` tests only pure helpers. plans/dev-flow.md:52,57-59 promises "table-driven unit suites" with a "fake exec", but they were never built. Hot swap is covered only end to end (test/integration_test.go:179-233). A test seam has to land first.
3. **CI skips the sandboxed tool-run tests.** CI never builds `.rootfs`, so `TestHostileRepoStaysInside` (internal/confine/sandbox_linux_test.go:26-34) and `TestConfinedGoBuild` (internal/runner/build_linux_test.go:33-41) skip. Any confined git operation for deployments (promote, branch following, push-to-deploy) is verified in a sandbox only on a dev box that has `.rootfs`.
4. **The native client is the direct precedent.**
   - It was built on 2026-09-26 by 20 + 9 work packages. The sequence was:
     - a Phase 0 commit containing design records only;
     - `nat/*` and `nat2/*` branches merged into the integration branch `native`;
     - integrator-only record commits (DECISIONS, changelog, migration notes);
     - a fast-forward of master.
   - Its real merge conflicts were in `docs/protocol.md`, `internal/server/openapi.go`, `hack/ui-harness/shots.js`, `internal/server/{server,static,api,sso}.go`, `internal/auth/{impersonate,sessions}.go`, `web/xbin-client.js`, `internal/boot/serve.go` (twice), `internal/broker/usersapi.go` (twice), `cmd/bx/main.go` + `docs/bx.md`, `docs/compat.md`, `hack/tile-versions.txt` and `native/AGENTS.md` (4 times). Evidence: the "# Conflicts" lists in merge commits 6b7f652, 02d2bab, 8dfb976, 03ff9eb, 98ceadc, e935bcc, f7e860f.
5. **Files at or near their size limit.**
   - No headroom: `internal/term/term.go` 950/950 and `cmd/bx/main.go` 1150/1150.
   - `internal/runner/runner.go` has 17 lines left (833/850).
   - `web/bx-frame.js` is at 866 against the 900-line cap for unlisted files.
   - A prep WP that splits these files (the d8de0a1 pattern) must come before the feature WPs.
6. **New decision IDs are a bottleneck.** Citing `D1xx` (any undefined number) anywhere under docs/, plans/, internal/, web/, cmd/, sdk/, workspace-template/ or the builtin trees fails `make test` until some plans/*.md defines it (internal/docscheck/docscheck_test.go:71-111). That is why the native WPs cited no new IDs; the integrator added the citations (commit b81f93c).

### 1. Tests

#### 1.1 Unit tests by package

**internal/runner**
- Only pure helpers are tested:
  - `nextBackoff` (alwayson_test.go:8);
  - `resourceBinds(env, root)` (resourcebinds_test.go:9): env in, bind list out. This is the natural place to pin per-deployment data paths.
  - `rootfsBin` (rootfsbin_test.go);
  - `procDescendants`, `addProcIO`, `procCPUJiffies` (stats_test.go);
  - `backendEnv` isolation allow-list (hostenv_test.go).
- No test drives `Ensure`, `Changed` or `buildAndStart` (runner.go:187, :262, :283). `build` and `start` are methods with no injection point (runner.go:359, :407).
- The one integration test is `TestConfinedGoBuild` (build_linux_test.go:32).
  - Build tag `linux && integration`; `TestMain` doubles as the sandbox's re-exec init (:21-26).
  - It needs user namespaces and `.rootfs` (or `XBIN_TEST_ROOTFS`).
  - It plants a hostile `.git/config` `core.fsmonitor` and asserts the marker file is never written on the host.
  - It also asserts the per-tile caches land in `.xbin/cache/tile/<CompKey>/`.

**internal/watch**
- No tests.
- Dot-directories at any depth, plus reserved top-level names, are ignored (watch.go:60-74). **(inference)** Ref updates under `.git/` never fire.
- When the consumer is slow, a batch is dropped with a warning (watch.go:170-175).

**internal/boot (where file changes become reloads)**
- `watchLoop` (serve.go:161-185) is the single place where a batch turns into a `reload` event (:178) plus `run.Changed(c)` (:180).
- Its pure half, `changedComponents` (serve.go:192), has a table test (watch_native_test.go:16). **(inference)** A pause or deployment gate written as a pure function next to it gets the same cheap table test.
- Other boot tests:
  - `TestStepsOrder` (boot_test.go:45) asserts the ordering edges of `boot.Steps` (boot.go:92-112).
  - `TestConfigDoc` (config_doc_test.go:17) fails when docs/config.md is stale. Regenerate with `UPDATE_DOCS=1 go test ./internal/boot -run TestConfigDoc`.
  - `TestTileAssetsConfig` (boot_test.go:343) is the pattern for validating a flag.

**internal/server**
- There is no constructor. Tests build `&Server{Reg: reg, Auth: a}` over `registry.Open(tmp)` and `auth.Load(root, false)`, then drive handlers through `httptest` (static_test.go:110-132).
- Policy answers are faked with `testPolicy{NoopPolicy; …}`, overriding one method (static_test.go:416-431). `NoopPolicy` is owner-only and fails closed (policy.go:46-53).
- Heavier fixture: `newAssetWS(t, mode)` runs a request matrix per asset mode (tileassets_test.go:36).
- `TestLegacyInjectionUnchanged` (tileassets_test.go:187) pins that the default mode is byte-for-byte unchanged. **This is the template for a "tile without deployments is unchanged" test.**
- Event shapes are tested by subscribing to the hub and comparing JSON (termsessions_test.go:78-106).

**internal/broker**
- `testWorkspace(t)` writes apps/calendar, apps/email and one workspace grant (broker_test.go:14-46).
- `testBroker(t)` always attaches a `users.Store` "like prod", because a nil store once hid a ceiling regression (broker_test.go:48-63). 20 test files use it.
- Per-area fixtures:
  - `orgFixture` with alice/bob/carol/dave/root2 and org `sales` (orgsapi_test.go:21);
  - `principalFor` and `call(t, h, p, method, url, body, pathVals)` (orgsapi_test.go:46, :56);
  - `busAPI`, `fakeBusDispatch`, `waitUntil` (bussubs_test.go:42, :28, :295);
  - `testUsers` (policy_test.go:20);
  - `transferFixture`, `netSetFixture`, `multiBroker`, `ingressBroker`.
- Principals are injected with `auth.WithPrincipal(ctx, p)`.
- `b.EnvFor(c)` is asserted as a joined string (ingressfn_test.go:120, :230, :282). **This is where to pin that main and dev get different `XBIN_RES_*` values.**

**internal/obs (the D63 plane pattern)**
- `testPlane(t)` is a struct literal of the plane's inputs (`Root`, `Hub`, `IsAdmin`, `HasComponent`), with no broker (status_test.go:89-96).
- **(inference)** A deployments plane built the same way can be unit-tested without the broker.

**internal/term**
- `NewManager(t.TempDir(), nil)` plus `ServeWS` through httptest tests the open-gates matrix before any PTY starts (gates_test.go:16-60): root terminal 403, write-level user 403, unknown session 404. `TestClampTermScopes` shows the table style.
- The `TestMain` in agent_test.go **builds `./hack/fakeacp` and `./cmd/bx`** (agent_test.go:30-49). A WP that breaks the `cmd/bx` build therefore also fails `internal/term` under `make test`.

#### 1.2 Integration suite (`test/`, run by `make integration`)
- **Setup.** Every file carries `//go:build integration`. `TestMain` builds `./cmd/xbind` into a temp dir and boots one shared daemon: `--workspace <tmp>/ws --listen 127.0.0.1:<free> --no-auth`, with `XBIN_SDK_PATH=<repo>/sdk`, no `--isolate` (integration_test.go:56-109, the command line at :85).
- **Helpers:**
  - `get`, `req`, `write`, `waitFor` (polls every 100 ms);
  - `startDaemon` (:38-54): runs its own process group and kills it, then retries `RemoveAll` to outlast in-flight `go build` runs;
  - `frameToken`, `getFramed`.
- **Auth.** Tests that need auth boot their own daemon from `xbindBin`. `TestMultiUser` (:465) uses `--insecure-vault`, reads the root token from `.xbin/token`, creates users through the API and logs in with a cookie.
- **Real Go builds.** Examples are copied in with `cp -r examples/…`. `TestGoBackendLifecycle` (:179-233) covers:
  - a cold build within 120 s;
  - a hot swap visible within 60 s;
  - a broken build that keeps the old generation serving and shows "build failed" in `/backends`;
  - recovery after the fix.

  Some tests write a backend inline, with a `go.mod` that requires the sdk (alwayson_test.go:13, streaming_test.go:85).
- **Network.** Module downloads on the first run.
- **Git.** Tests run git directly (`gitIn`, prs_test.go:22). Test files are exempt from `TestNoDirectExec`.
- **(inference) Coverage gaps.**
  - Nothing in test/ runs `--isolate`, so per-deployment data separation through sandbox binds has no end-to-end test today.
  - The latency budgets in plans/dev-flow.md:102-105 (under 500 ms static, under 2 s Go) are asserted nowhere.
- **Fixtures to leave alone.** Examples double as native fixtures (native/fixtures/tile-calendar and tile-counter-go import them), so editing an example ripples into `native-check`.

#### 1.3 Legacy-workspace fixture (D62) and migration tests

**Binary version: `test/legacy_workspace_test.go:31`**
- It runs `xbind init`, then "ages" the workspace:
  - a legacy `home/` holding real data, and no `homes/`;
  - no backfill ledger and no builtins provenance;
  - no `tiles/organisations`;
  - a `.gitignore` without `homes/`.
- It then boots twice:
  - **Boot 1** may change only `.xbin/ data/ homes/ home/ tiles/organisations/ go.work .gitignore AGENTS.md CLAUDE.md .git/`, or any path containing `/deps/` or `/.git/` (:74-86). The root `xbin.json` must stay byte-identical (:68-70).
  - **Boot 2** must change nothing (:93-97).
- `snapshot` skips `.xbin/{log,run,term,build,cache,env}`, `.git`, `*.log|sock|pid`, `.git/index` and `.git/logs/` (:171-212).
- A second test asserts that `home/` + `homes/` together stop the daemon (:100).

**In-process twin, part of `make check`**
- `TestLegacyWorkspaceBootsTwiceInProcess` (internal/boot/boot_test.go:116; allowed list at :164-172).
- It calls `boot.Run` with `testConfig` (:218: NoAuth, InsecureVault, NoPrivileges, a `:0` listener) and `bootOnce` (:232).

**The rule.** Every boot-time migration extends both allowed lists, with its reason (docs/compat.md rule 9; docs/maintenance.md "The legacy-workspace fixture").

**Unit-level "old on-disk shape still loads" tests**
- `TestLegacyUserShape` (users/users_test.go:86)
- `TestMigrateHomes*` (term/homes_test.go:35-86)
- `TestLegacyFrameTokenUpgrade` (auth/frametoken_test.go:157)
- `TestBackfillEssentials` (builtins/backfill_test.go:25)

**(inference) Two consequences for this feature**
- Any path containing `/.git/` is allowed (:86). A migration that rewrites tile branches or refs would therefore pass the fixture silently.
- State under `.xbin/` and `data/` is allowed, and is explicitly not part of the contract (docs/compat.md "What this does not promise").

#### 1.4 confine, sandbox and VM tests, and what they need from the host
- **`TestGitDirect`** (confine_test.go:31) covers direct mode with the hardened environment. It fails if `Isolated()` is true inside a unit test (:35-37), so **unit tests always exercise direct mode**.
- **`TestHostileRepoStaysInside`** (sandbox_linux_test.go:40; tag `linux && integration`; the re-exec init is in `TestMain` at :19-24):
  - a repo's `diff.external` command runs inside the sandbox, never on the host;
  - PID 1 is git;
  - a read-only bind refuses a commit.

  It needs unprivileged user namespaces plus a rootfs that has `/usr/bin/git`: either `XBIN_TEST_ROOTFS` or `../../.rootfs`, which `make rootfs` builds with docker.
- **`internal/sandbox/sandbox_linux_test.go`** (tag `linux && integration`) is **not run by `make integration`**. Makefile:110-117 lists `./test/...`, `./internal/confine/`, `./internal/runner/` and `./internal/vm/` explicitly. A new package with integration tests must be added to that list, or its tests never run.
- **VM integration tests** need `/dev/kvm` or the QEMU assets, plus `XBIN_ROOTFS`. CI builds a small ubuntu rootfs only for them (ci.yml:60-76), and that rootfs has no git.
- **Container filesystem suites** (test/containerfs_test.go:42-70) need `unshare --user`, the patched `bin/gocryptfs` and fuse-overlayfs. They skip with instructions when any is missing.
- **CI host preparation:** it sets `sysctl kernel.apparmor_restrict_unprivileged_userns=0` and installs `attr libcap2-bin fuse3 shellcheck` (ci.yml:26-28).

#### 1.5 UI harness (`hack/ui-harness`)

**run.sh**
- Environment variables:
  - `PORT`, default 8697 (:22);
  - `HARNESS_DIR`, default `$TMPDIR/xbin-ui-harness` (:25);
  - `WS=$HARNESS_DIR/ws`;
  - `TILE_ASSETS` = legacy | tokens | origins (:31);
  - `FAKEOPENAI_ADDR`, derived as PORT+10280 (:43);
  - `INGRESS_ADDR`, derived as PORT+1 (:45);
  - `OUT`.
- Modes: the default (build, fresh `xbind init`, start, seed, every pass, stop), `--keep`, `--restart`, `--shots [pass…]` and `--stop` (:94).
- `build()` (:79) builds xbind, a static bx, fakeacp and fakeopenai into `$REPO/bin`.
- `start()` (:68-77) runs `bin/xbind --dev --dev-overlay workspace-template --workspace $WS --listen 127.0.0.1:$PORT --ingress-listen … --external-url … --tile-assets …`, with `XBIN_AGENT_FAKE`, `XBIN_BIN` and `XBIN_SDK_PATH` set. Auth is on (admin/admin). **It does not use `--isolate`.**
- `stop()` (:53-66) pkills by workspace path and by port, then lazily unmounts leftover gocryptfs mounts.
- Without a gocryptfs binary, the agent passes print SKIP lines (`HARNESS_NO_GOCRYPTFS`, :86-91).

**seed.sh** (all through the owner's bearer token)
- network sets;
- orgs devs, infra, sales and exec;
- users dev1, sales1 and infra1;
- tiles:
  - apps/crawler and apps/racks (node);
  - apps/pinned, apps/offline, apps/leads, apps/dev1-notes;
  - the focusy and linky fixtures;
  - a long-path http provider plus a consumer;
- screens and folders; bindings;
- llm-gw with the agent template and fakeopenai; the messaging bridge and webhooks.

**lib.js**
- Playwright is loaded from a global install or from `PLAYWRIGHT_DIR` (:13-18).
- Navigation helpers: `login`, `openShell`, `openTile`, `tileFrame`, `gotoTab`.
- `sh(page, fn)` runs against the shell's `testApi()`; `fr(page, src, fn)` runs against a frame's `testApi()` (:51-53).
- Waiting: `waitFor` polls a condition; `waitSel` waits for a selector; `settle` waits two animation frames.
- `checker(name)` writes PASS/FAIL/SKIP lines to `$OUT/<name>.txt` and throws on any FAIL (:150).
- `showPickers` / `PICKERS` handle the terminal title bar versus the tools row (:165-166).
- `closeCtx` flushes the shell's debounced layout save before closing.

**shots.js**
- It requires each `passes/<name>.js` (:16-34).
- It lists every pass in one `PASSES` object literal, with the entries on a single line (:845-850). Its size budget is 870/877.
- Passes that edit files write into `process.env.WS`. passes/agenttemplate.js:161 saves a Go file to force a backend swap; passes/tileassets.js:195 rewrites a tile's index.html.

**Rules** (docs/maintenance.md "UI harness")
- Passes drive `testApi()` only. `make js-check` fails on any `._x` access in that directory (Makefile:192).
- Keep the DOM hooks that passes locate.
- Wait for a condition, never for a fixed time.
- A pass leaves nothing behind: no open sessions, and no `term:<tile>` or `termvm:<tile>` prefs.
- bx-frame's `testApi()` lives at web/bx-frame.js:563.

**Local environment**
- The main checkout has Playwright at `~/lcad-wasm`, `.rootfs`, `bin/gocryptfs` and `bin/fuse-overlayfs`.
- A worktree has none of these (the comment at run.sh:81-85). Point `XBIN_GOCRYPTFS`, `XBIN_TEST_ROOTFS` and `PLAYWRIGHT_DIR` at the main checkout.

**Closest precedent for a pause toggle:** passes/vmtoggle.js. It covers a per-tile choice stored in a pref, a toggle shown disabled with the server's reason, and `page.route` faking `/ws/term/env`.

#### 1.6 JS unit tests
- `make js-test` runs `node --test hack/*.test.mjs` (Makefile:141-143).
- It covers pure web modules that import nothing and take fetch/storage as arguments. Example: hack/term-sessions.test.mjs tests web/term-sessions.js.
- **(inference)** The terminal window's pause and deployment state logic should be written as such a module.

#### 1.7 Make targets and CI workflows

**Make targets**
- `test` (:105): `go test ./...`, plus `./sdk/... ./relay/...` (separate modules).
- `integration` (:110-117): see above; runs with `-count=1 -v`.
- `check` (:136): fmt-check, vet, js-check, js-test, native-check, theme-check, shellcheck, pins-offline, test.
- `js-check` (:190-192):
  - `node --check` over web, workspace-template, builtin-*, examples, hack, website and native, including inline module blocks;
  - named imports resolved against the module they name;
  - no second copy of a kit helper;
  - the harness private-member grep.
- Other guards: `native-check` (:147), `theme-check` (:177), `tile-check` (:184; needs network, runs in CI), `shellcheck` (:194; covers hack/ui-harness/*.sh), `pins-offline` (:199).
- `hooks` installs .githooks/pre-commit, which runs fmt-check and js-check.

**ci.yml**
- Job `test` (:9-76):
  - setup: Go 1.26 and Node 24;
  - host prep: the userns sysctl and apt packages;
  - cached gocryptfs and fuse-overlayfs builds;
  - the checks: `make check`, `make tile-check`, `make integration`;
  - VMs: KVM access, the VM assets, and the VM integration tests run twice (KVM, then emulated).
- Job `native` (:86-114): Swift tests, the stub checks, native-check.

**ios.yml** runs on feature-branch pushes only, never on pull requests.

### 2. Guards a large change trips

#### 2.1 Size budget
- **Mechanics** (internal/sizebudget/sizebudget_test.go:67; hack/size-budget.txt):
  - An unlisted non-test Go file may not pass 800 lines.
  - An unlisted JS, MJS or HTML file may not pass 900 lines (it walks web, workspace-template, builtin-* and hack).
  - A listed file that shrinks below 90 % of its budget also fails, until the number is lowered.
  - Raising a budget is "the wrong fix"; splitting the file is the right one.
- **Listed files in scope:**

| file | lines / budget | headroom |
|---|---|---|
| internal/term/term.go | 950/950 | 0 |
| cmd/bx/main.go | 1150/1150 | 0 |
| internal/runner/runner.go | 833/850 | 17 |
| internal/broker/usersapi.go | 785/789 | 4 |
| internal/broker/netfn.go | 1131/1170 | 39 |
| internal/broker/orgsapi.go | 747/820 | 73 (floor 738) |
| cmd/xbind/main.go | 81/85 | 4 |
| workspace-template/shell/bx-shell.js | 1879/1892 | 13 |
| workspace-template/tiles/admin/admin.js | 314/318 | 4 |
| workspace-template/apps/welcome/notes.js | 1028/1050 | 22 |
| hack/ui-harness/shots.js | 870/877 | 7 |

- **Unlisted files near the cap:**
  - Go (800): cmd/bx/agent.go 786, internal/server/sso.go 785, internal/broker/broker.go 743, cmd/bx/org.go 730, internal/broker/ingressfn.go 721, internal/boot/boot.go 708, internal/term/agent.go 706, internal/server/server.go 691, internal/server/static.go 650, internal/server/openapi.go 626.
  - JS (900): web/bx-frame.js 866, web/bx-agent.js 783, web/bx-terminal.js 713, workspace-template/tiles/admin/tabs/runtime.js 689.
- Go `_test.go` files are exempt. hack/*.test.mjs and the harness passes do count against the 900 cap.

#### 2.2 TestNoDirectExec
- **What it scans** (internal/confine/guard_test.go:13-65):
  - Scope: non-test .go files under internal/ and cmd/xbind.
  - Pattern: `exec.Command(Context)`, `exec.Cmd{}`, `os.StartProcess`, `syscall.Exec` and `unix.Exec`.
  - Exempt: internal/confine/, internal/sandbox/ and internal/agent/host/, or any call with `exec-ok:` on its line or on one of the two lines above.
- **What to use instead.** Any tool run on tile data goes through `confine.Git` / `GitRead` / `GitCmd` / `GitBytes` / `Run` (git.go:36-68).
  - The git flags pin fsmonitor, hooks, untracked cache, gpgsign, pager and `protocol.ext` (git.go:14-23).
  - The environment drops the system and global git config (:27-33).
- With isolation on, a sandbox that fails to start is an error, never a host fallback.

#### 2.3 Route inventory, OpenAPI drift, protocol.md
- **How it works** (internal/apicheck/apicheck_test.go):
  - `mount()` (:122) builds the broker on an empty workspace with a users store, calls `b.Register(srv)`, then `srv.Handler()`.
  - `mainRoutes()` (:153) reads the `RegisterAPI` literals in every internal/boot/*.go file (a glob since commit b30a2ca).
  - `sourceRoutes()` (:179) finds every literal under cmd/ and internal/.
- **`TestRouteInventory` (:244) requires that:**
  - every literal is mounted by the fixture (otherwise "wire it into mount() or mainRoutes()");
  - every mounted API route has both an openapi.go row and a protocol.md row;
  - every core route has a protocol.md row;
  - every documented row is a mounted route.

  protocol.md rows are lines starting at column 0 inside a code fence (:203-230).
- **So one route means three edits in one commit.**
- **Where the rows live:**
  - openapi.go is a single slice literal, `endpoints()` (openapi.go:83-486, 180 rows).
  - protocol.md's API section is a single code fence (docs/protocol.md:423-1934).
- `TestOpenAPISpec` (openapi_test.go:9) checks the capability of a few representative endpoints.
- **(inference)** Routes whose literals sit in a new package that boot mounts directly fail the inventory. Put the literals in one of:
  - internal/boot/<x>.go (VM precedent: internal/boot/vm.go:38-66);
  - broker.Register (obs precedent: broker.go:291-293);
  - or extend `mount()`.

#### 2.4 docscheck (internal/docscheck)
- **`TestDecisionIDsResolve`** (:71). IDs of the series `D|ND|ING|LC|IFACE|VD|RT|ISO|BU|CM|PR` (with an optional trailing letter) that are cited in docs, plans, internal, web, workspace-template, cmd, sdk or builtin-* must be defined somewhere under plans/ (searched recursively). A definition is a bullet or heading that opens with the bold ID (:24-33, :82).
- **`TestRelativeLinksResolve`** (:114): relative and `/docs/` links in docs/ and plans/ must point at existing files.
- **`TestPlansCarryStatus`** (:154): every top-level plans/*.md needs `> Status: live|implemented|superseded|historical` within its first 12 lines. Subdirectories are not checked.
- **(inference)** A new ID prefix (for example `DEP-`) is invisible to the guard unless it is added to both regexes.

#### 2.5 Embedded assets and frontend guards
- **`TestEmbeddedAssets`** (assets_test.go:27) covers web/, docs/, workspace-template/ and builtin-*. It refuses:
  - compiled (ELF) binaries;
  - files over 512 KB outside web/vendor;
  - `.git`, `node_modules`, `.claude` or `deps` directories;
  - any `plans/` citation (docs/maintenance.md is exempt).
- **Other guards:**
  - js-check (above);
  - theme-check: every CSS fallback literal must equal web/theme.css;
  - `TestShippedTilesPassStrictGating` (internal/assetscan/shipped_test.go:17);
  - `TestTileVersions` (builtin tiles only; hack/tile-versions.txt);
  - `TestBuiltinManifestsAndRoleGuards`;
  - `TestConfigDoc` and `TestStepsOrder`;
  - internal/server/overlay_test.go (dev-overlay rules);
  - hack/native-docs.test.mjs.

#### 2.6 Guards that tripped on past features
- 2114643: "embedded docs and web cite D89, not plans/ (TestEmbeddedAssets)".
- 3a8869c and 64a3f79: "the VM toggle's accent fallback matches theme.css (theme-check, CI red since D89)".
- d509715: a flake fix-forward in `TestAgentSessionEndToEnd`, which had asserted an intermediate status. It now waits, bounded.
- Code moved into new files to stay in budget:
  - fb11711: whoami.go, defaultsapi.go, usersapi_personal.go (the usersapi ratchet dropped to 789);
  - d8de0a1: runner/binds.go, runner/rundir.go, term/binds.go;
  - 083b3d3: web/frame-info.js;
  - 0d44847: web/frame-titlebar.js.
- native/AGENTS.md:375-383, "A clean merge proves nothing": two branches each defined `reserved`, git merged them without a conflict, and every pick crashed.
- D109 (DECISIONS.md:3284-3298): two WPs grew the same stub types on their own sides, and each broke the other's build. Hence "one declaration in one place".

### 3. How recent large features were staged and landed

#### D88: personal plane (2026-09-26, 7 commits on master)
1. 8374b15: the users store, plus the D88 entry in DECISIONS. userjson.go was split off to stay in budget.
2. 76b6aec: the broker's create and transfer gate.
3. ab532bd: owner self-approval. It retires an old test matrix explicitly.
4. 03cc221: the broker and terminal side of the personal network.
5. fb11711: API and bx. openapi.go updated; handlers split into new files; a ratchet lowered.
6. 2a6c1dd: UI (admin console, shell, manager, net pickers) plus a new harness pass, `personalPlane`. The commit lists which passes ran green and names a failure that was pre-existing on master, checked A/B.
7. da36225: docs (auth, protocol, isolation, bx, overview), the changelog, and the `TestMultiUser` end-to-end extension.

- No flag: both switches default to off and the personal defaults are empty.
- The changelog calls the change additive, with one tightening.
- The protocol.md field docs waited for the last commit. No routes were added, so the route inventory stayed green.

#### D89/D90: VM sandboxes (plans/vm-sandbox.md, status "live", with a "Not yet" section)
1. d8de0a1, **prep**: helpers moved verbatim out of runner.go and term.go; budgets ratcheted down; the harness builds a static bx.
2. ee4a581: the core packages, with integration tests.
3. b30a2ca: VM terminals and agent sessions. The policy file `.xbin/vm/policy.json` is **off by default**. It adds `GET /vm` and `PUT /vm/policy` (internal/boot/vm.go), the config rows, the `mainRoutes` glob in apicheck, and the openapi and protocol rows.
4. 2b973b5: the title-bar toggle, plus passes/vmtoggle.js.
5. 4dd1671: the persistent VM disk.
6. d2ee51f: the `"vm"` manifest key, which **fails closed** (parsing in registry/vm.go with a test; one field added in registry.go).
7. 17d98e1: docs (the plan, D89, the changelog, the builder docs).
8. 8f11f31: build, release, install and CI.
9. b9a5b74: the file transport replaced after a benchmark gate set in the plan.
10. 2114643 and 3a8869c: guard fixes.
11. 6d608e9: D90, emulated VMs.
12. 54cf67a: hardening.
13. 33066fa: D110, the installer turns VMs on. xbind's own default stays off.

#### D74/D75: agent sessions (plans/agent-sessions.md, stages 0–3 at :84-98)
1. edfa245: stages 0–1 as one vertical commit, including protocol.md, openapi.go, bx.md, the changelog, D74 and the plan (45 files).
2. 0d44847: stage 3 UI plus passes/agenttab.js. frame-titlebar.js was split out of bx-frame.
3. 88bf0c2: a same-day revert of the vault-key path, editing the DECISIONS text.
4. 21e0b84: D75 (history and resume) as one vertical slice, with tests at every layer (acp, term, server, node, harness), docs and the D75 entry.
- Released at stage 3 as v0.3.51.

#### D95: tile asset gating
Built as WP `nat/assets` and merged into `native` in 6b7f652, with conflicts.
1. 265ea6c: auth primitives.
2. 5bdced6: the mechanism behind the `--tile-assets` flag, with `legacy` as the byte-for-byte default (boot/config.go:50, plus a validation test and the protocol.md rows).
3. f0dd2d0: the detection API.
4. 0024953: the `bx` codemod and a doctor check.
5. 083b3d3: the web side (the frame-info.js split).
6. b0d9553: docs, including the two-release contract in compat.md.
7. ddc78a8: a harness mode (`TILE_ASSETS`) plus a pass.
8. Review fixes.
9. a424ae2: docs after the review, the migration note (docs/changes/2026-09-26-tile-asset-gating.md) and a maintenance-guard entry.
- Pinned by `TestLegacyInjectionUnchanged` and `TestShippedTilesPassStrictGating`.

#### The native swarm (the model for this effort)
1. **Phase 0.** b0787cd contained plans only (native.md, 1586 lines; two companion plans; native/AGENTS.md). Status line: "live — a proposal under review (Phase 0). Nothing here is built". The decisions were numbered 1–17 for review (§25), and "no decision IDs are taken yet".
2. **Wave 1.** 72e2ef6 merged master into `native`. 20 `nat/*` branches were then merged in order: rt, core, term, acp, ci, srvB, devauth, push, acpsrv, assets, tplsrv, tplmodel, fx, lit, tiles, swiftui, tplnative, cli, app, docs. Integration fixes followed on the branch.
3. **Records.**
   - e1100f6: "each proposed decision texts it could not write into the shared log". The integrator consolidated them into D91–D98 and flipped the plans' status.
   - b81f93c: "The work packages were not allowed to edit docs/changelog.md". The integrator wrote the changelog and the migration notes, and added the decision citations.
4. **A security round.**
5. **Wave 2.** 9 `nat2/*` branches. The stubs were unified (733551d), the seams were wired (1c973c6), and e32df4f recorded D99–D109.
- §20 of native.md tracks the server additions row by row, with a track column (S, A, B).

#### Patterns
1. **Design first.** Decisions are numbered locally in the design record; D-IDs are assigned at integration.
2. **Prep before behavior.** A no-behavior-change prep commit moves code verbatim out of budgeted files and lowers the ratchets.
3. **Slice by layer** for big features: store → policy/broker → runner/term → API + bx → UI + harness → docs + end-to-end test. Use one vertical slice when the feature is small, with tests at every layer.
4. **The default equals today.** Precedents:
   - a config flag default (`--tile-assets=legacy`);
   - a policy file that is off (`.xbin/vm/policy.json`);
   - switches off and empty defaults (D88);
   - an opt-in manifest key that fails closed (`"vm"`).

   In each case a test pins the default as unchanged.
5. **Docs discipline.**
   - A route is `RegisterAPI` + an openapi row + a protocol row in the same commit (enforced).
   - For single-author series, builder docs and the changelog land in the same commit or the same series.
   - In a swarm, the changelog and DECISIONS belong to the integrator.
6. **Migration notes only for breaking changes.** The file is docs/changes/YYYY-MM-DD-slug.md with sections What changed / Who's affected / How to migrate / Why. D95 announced next-release enforcement one release early. D88, D89 and agent sessions had no note.
7. **Harness and honesty.** Every UI slice adds or updates an asserting harness pass. Commit bodies carry "Tests:", "Verified:" or "Live:" sections and name what was not verified.

### 4. Hotspots for parallel work

#### 4.1 Empirical
- **Most-touched files since 2026-09-01** (`git log --name-only`):

| file | commits |
|---|---|
| docs/changelog.md | 107 |
| docs/protocol.md | 79 |
| plans/DECISIONS.md | 64 |
| internal/server/openapi.go | 46 |
| hack/ui-harness/shots.js | 44 |
| docs/maintenance.md | 40 |
| docs/auth.md | 35 |
| web/bx-frame.js | 31 |
| docs/overview/09-terminals.md | 30 |
| workspace-template/shell/bx-shell.js | 28 |
| docs/elements.md | 28 |
| docs/bx.md | 24 |
| workspace-template/tiles/admin/admin.js | 22 |
| workspace-template/AGENTS.md | 20 |
| Makefile | 20 |
| internal/server/server.go | 19 |
| hack/size-budget.txt | 18 |
| internal/term/agent.go | 18 |
| internal/server/static.go | 17 |
| internal/boot/boot.go | 15 |
| web/frame-titlebar.js | 14 |
| internal/term/term.go | 12 |
| cmd/bx/main.go | 11 |
| internal/broker/broker.go | 11 |

- **Real merge conflicts:** see §0 item 4.

#### 4.2 Files this feature will likely share (inference)
- **Pipeline:**
  - internal/boot/serve.go: `watchLoop`, `changedComponents`;
  - internal/boot/boot.go: `Steps`, and `stepProxy`'s wiring of `OnGrantChange → run.Changed` (:497-501) and `ShouldRun` (:506);
  - internal/runner/runner.go: state keyed by component path; `Changed`, `Ensure`; the build events (:285-352);
  - internal/events/events.go: the `Event` fields Type, Component, Text, Topic, Data (:11-17).
- **Planes:**
  - internal/broker/broker.go (`Register`, the struct fields);
  - internal/broker/resources.go (`EnvFor`, :71);
  - internal/proxy/proxy.go;
  - internal/registry/registry.go (`Manifest`);
  - internal/server/policy.go, plus `brokerPolicy` (broker.go:305-316);
  - internal/server/static.go: `/c/` serves the live tree (:70-72);
  - internal/server/server.go: `handleTermEnv` (:326).
- **Surface:**
  - internal/server/openapi.go;
  - docs/protocol.md: the API fence, the events block (:2191-2209), the backend contract (:2311-2327) and the filesystem contract (:2329-);
  - docs/changelog.md, plans/DECISIONS.md, docs/maintenance.md.
- **UI:**
  - web/bx-frame.js: the event switch (:366-395) and `testApi` (:563);
  - web/frame-titlebar.js, web/frame-launcher.js, web/term-sessions.js;
  - the admin console's tabs/runtime.js (workspace-owned).
- **Tests and harness:** shots.js (the `PASSES` line), seed.sh, test/integration_test.go (only if tests are appended there), and both legacy allowed lists.
- **CLI:** cmd/bx/main.go usage text (no headroom) and docs/bx.md.
- **Docs that state the hot-reload contract:**
  - docs/overview/03-components.md:208-228;
  - docs/overview/01-model.md:78;
  - docs/index.md:6;
  - docs/elements.md:356;
  - docs/overview/04-frontend.md:112;
  - docs/protocol.md:2311-2327;
  - workspace-template/AGENTS.md;
  - workspace-template/apps/welcome/notes.js:81, :376, :484 (1028/1050);
  - plans/dev-flow.md §2 ("phases must not regress it").

#### 4.3 Registration seams that already exist
- **`server.Policy`** (policy.go:18-43). A new question the server asks the broker is a method here, never another nullable func field (docs/maintenance.md). It has three implementations: `NoopPolicy`, `brokerPolicy`, and `testPolicy`, which embeds `NoopPolicy`.
- **D63 plane.** A struct of the inputs it needs from the broker, built and mounted in `broker.Register` (broker.go:291-293), tested on a literal of those inputs.
- **Boot-level routes.** Any `srv.RegisterAPI` in internal/boot/*.go is covered by the route inventory automatically.
- **Boot step.** One entry in `boot.Steps` plus an edge in `TestStepsOrder`.
- **Manifest key.** One field with a comment in `registry.Manifest`, with parsing in registry/<key>.go and its own test (vm.go and native.go precedents). Unknown keys are ignored (compat rule 7).
- **Config flag.** A tagged `boot.Config` field gives the flag, the env var and the docs/config.md row (config.go:50 precedent).
- **bx command.**
  - `moreCmds` (cmd/bx/native.go:26-32) is dispatched by `cmdExtra` (cmd/bx/agent.go:30-33). **(inference)** A new file can register its command in an `init()` without touching native.go.
  - Usage text is concatenated at main.go:174 (`+nativeUsage`), so adding a block there is a line-neutral edit.
- **Harness pass.** A new passes/<name>.js module, plus one `require` line and one `PASSES` entry.
- **Web modules.** New sibling modules imported by absolute `/vendor/` URL (frame-titlebar.js and frame-info.js precedents).
  - web/ ships inside the binary, so it reaches every workspace at upgrade.
  - workspace-template/ changes reach existing workspaces only through `bx builtin update` (compat rule 4).
- **Test files.** A new test/<feature>_test.go shares the suite's `TestMain` daemon; new internal/<pkg>/<feature>_test.go files need no shared edits.

#### 4.4 Recommended partition (inference, grounded above)
**Integration model**
- One integration branch cut from master. WP branches named `dl/<slug>`, each in its own worktree.
- Merge in dependency order.
- After **every** merge run `make check`. Also run `make integration` when runner, sandbox, broker or confine code changed, and run the affected harness passes.

**WP-0, prep (serial)**
- Verbatim moves out of runner.go, term.go (if the feature touches it), bx-frame.js and the cmd/bx usage text; lower the ratchets.
- Add the runner/watch test seam: injectable build/start, or pure decision functions. Table-test today's behavior first.
- No behavior change.

**WP-1, contracts (serial)** owns every shared declaration, per the D109 lesson:
- the deployment identity type, in a new leaf package;
- new event types and payloads, rather than reusing `reload` or `build-*`;
- the manifest field and its parse file;
- the Policy methods;
- one hook-struct field in `Runner`;
- optionally, a route skeleton: every `RegisterAPI` literal in internal/boot/<feature>.go, handlers delegating to interfaces, and their openapi and protocol rows written once;
- the harness seed fixture and the `PASSES` registrations for planned passes;
- the "a tile that does not opt in is unchanged" golden test (env, spawn inputs, the `/components` entry, static serving, events), which every later WP must keep green.

**Parallel WPs, each owning its new files**
- **Pause / reload now:** the runner and watch gate. This WP alone owns internal/boot/serve.go.
- **Deployments:** the store, per-deployment runner state and build dirs.
- **Broker:** per-deployment resources and authorization (internal/broker/<x>.go).
- **Confined git:** promote, branches, and the push remote. It gets its own package and `linux && integration` tests modeled on `TestHostileRepoStaysInside`, added to Makefile:113.
- **Web UI:** new web/ modules, hack/<x>.test.mjs and passes/<x>.js.
- **bx:** cmd/bx/<x>.go via `moreCmds`, plus a docs/bx.md section.
- **Builder docs.**

**Serialized files, owned by the integrator or WP-0 only**
- docs/changelog.md, plans/DECISIONS.md, docs/changes/*, and the plans' status lines;
- hack/size-budget.txt (outside WP-0);
- the `PASSES` line in shots.js;
- `boot.Steps`;
- both legacy allowed lists;
- Makefile, ci.yml, and the guard entries in docs/maintenance.md.

### 5. Recommended work-package template

```
## WP-<nn> <slug> — <one-line goal>
Branch: dl/<slug> from <integration-branch>@<sha> · depends on: WP-… · merge slot: <n>
Design: plans/<feature>.md §… · proposed decisions #… (local numbers; never cite D1xx+)

Goal / acceptance demo: what a person sees working (plans/implementation.md style).
Scope: tasks by package. Non-goals.

Files owned (create/edit freely): …
Shared files (exact seam, max lines): e.g. internal/broker/broker.go Register +1 line;
  internal/registry/registry.go Manifest +1 field; web/bx-frame.js testApi +2 lines
Must not edit (unless listed above): docs/changelog.md, plans/DECISIONS.md, docs/changes/*,
  hack/size-budget.txt, hack/ui-harness/shots.js PASSES, examples/*, boot.Steps,
  test/legacy_workspace_test.go + internal/boot/boot_test.go allowed lists, Makefile, ci.yml

Interfaces consumed: exact Go signatures / JSON bodies / event shapes, with the file that defines them.
Interfaces provided: the same, and which WPs consume them.
Routes: METHOD /api/xbin/… · capability · openapi.go row + protocol.md row in the SAME commit;
  literal in internal/boot/<x>.go or broker.Register (apicheck coverage). New handlers use
  server.WriteError / WriteOK / DecodeJSON.
Size budget: every touched file with lines/budget; the verbatim-move split done first.

Compat (docs/compat.md, per rule): (a) a tile that does not opt in is byte-for-byte unchanged:
  keep the golden test green; (b) API fields additive; new event types, never reload/build-*
  for a non-primary deployment; (c) manifest keys additive, unknown ignored; (d) CLI superset;
  (e) scaffold additive: UI in web/ unless the scaffold is the point; (f) a boot migration
  extends both legacy allowed lists and the second boot changes nothing; (g) native app contract untouched.
Security: D78 (confine.* for any tool on tile data; no exec without exec-ok; a sandbox failure
  is an error); plans/auth.md semantics (X-XBin-* stripped, default-deny for elements, owner
  is admin); who may pause/promote (tile level: read < write < terminal); dev never gets prod
  paths or tokens.

Acceptance tests (name · fixture · prerequisites):
  unit: pkg/TestX on testBroker / testPolicy{NoopPolicy} / a plane literal / a changedComponents-style table
  integration: test/<x>_test.go (//go:build integration; shared daemon or startDaemon)
  confined: internal/<pkg>/<x>_linux_test.go (//go:build linux && integration; XBIN_TEST_ROOTFS;
    hostile .git/config marker); package added to Makefile integration
  harness: passes/<x>.js (checker, testApi only, cleans its prefs/sessions)
  js-test: hack/<x>.test.mjs over a pure web module
Docs in this WP: protocol.md rows, the builder docs sections, bx.md + usage strings,
  workspace AGENTS.md / welcome notes if the WP teaches that area.
Hand-off to the integrator: "Changelog:" and "Decision:" sections in the last commit body
  (or a named hand-off file); breaking? then a migration-note draft (What changed /
  Who's affected / How to migrate / Why).
Verification env: PORT=<unique> HARNESS_DIR=<unique> PLAYWRIGHT_DIR=~/lcad-wasm
  XBIN_GOCRYPTFS=<main>/bin/gocryptfs XBIN_TEST_ROOTFS=<main>/.rootfs
Done when: make check green on the branch; make integration when the runner, sandbox, broker
  or confine code changed; the named harness passes green (every SKIP explained); no budget
  raised; area-prefixed commits whose bodies say why and list "Tests:" run and not run.
```

### 6. Where the design artifacts should live so the guards pass
- **A top-level plans/<name>.md needs the status line.** Precedent (b0787cd): `> Status: **live** — a proposal under review. Nothing here is built; the decisions at the end are open.`
- **Per-WP appendices can go in plans/<feature>/*.md.** IDs and links are checked there (the check is recursive), but no status line is required.
- **Decision numbering.** Number proposals locally, like native.md §25, and let the integrator assign D-IDs. Do not introduce a new ID prefix unless both docscheck regexes gain it.
- **Links and citations.** Relative links between artifacts must resolve. Nothing under docs/, web/ or workspace-template/ may point into plans/.
- **Names already taken:**
  - plans/deployment.md: historical, about deploying the whole workspace;
  - plans/lifecycle.md: the LC series (enable, disable, offload);
  - plans/dev-flow.md: the core dev loop and the workspace product loop.
- **Precedents for the swarm-facing sections:** native.md §20 (a server-additions table with a track column) and §21 (phases and parallel tracks).

#### Critical Files for Implementation
- /home/magik6k/buxon/internal/boot/serve.go
- /home/magik6k/buxon/internal/runner/runner.go
- /home/magik6k/buxon/internal/apicheck/apicheck_test.go
- /home/magik6k/buxon/test/legacy_workspace_test.go (with /home/magik6k/buxon/internal/boot/boot_test.go)
- /home/magik6k/buxon/hack/ui-harness/shots.js (with /home/magik6k/buxon/hack/size-budget.txt)

## Surfaces this feature touches

### Test seam for the runner and watcher

- **Paths:** `internal/runner/runner.go:187`, `internal/runner/runner.go:262`, `internal/runner/runner.go:359`, `internal/runner/runner.go:407`, `internal/watch/watch.go:60-74`, `internal/watch/watch.go:170-175`, `internal/boot/serve.go:161-205`, `internal/boot/watch_native_test.go:16`, `plans/dev-flow.md:52-59`
- **Today:** Runner tests cover only pure helpers (backoff, resourceBinds, rootfsBin, proc stats, backendEnv). internal/watch has no tests. build/start are methods with no injection point. changedComponents is the only pure, table-tested piece of the save-to-reload path.
- **Change needed:** Add a seam first: injectable build/start, or pure decision functions over (tile, deployment, pause state, change batch). Table-test today's behavior, then add the pause, reload-now and deployment cases.
- **Compat:** Land it as a no-behavior-change prep commit. The table rows for the default path must reproduce today's reload and restart results exactly.

### Broker unit-test fixtures

- **Paths:** `internal/broker/broker_test.go:14-63`, `internal/broker/orgsapi_test.go:21-72`, `internal/broker/policy_test.go:20`, `internal/broker/bussubs_test.go:28-60`, `internal/broker/bussubs_test.go:295`, `internal/broker/ingressfn_test.go:120`
- **Today:** testWorkspace (calendar + email + one grant). testBroker always attaches a users store. Per-area fixtures exist (orgFixture, transferFixture, netSetFixture). Principals go in through auth.WithPrincipal; handlers are called directly. EnvFor is asserted as joined strings.
- **Change needed:** Add a new fixture helper in its own _test file for a tile with main and dev. Assert EnvFor and resource paths per deployment (dev never sees prod paths). Add an authorization matrix for pause/promote: owner, org admin, terminal/write/read users, element and terminal tokens.
- **Compat:** Do not change testWorkspace, which 20 test files use; add a helper instead.

### Server test fixtures and the Policy interface

- **Paths:** `internal/server/policy.go:18-58`, `internal/server/static_test.go:110-132`, `internal/server/static_test.go:416-431`, `internal/server/tileassets_test.go:36`, `internal/server/tileassets_test.go:187`, `internal/broker/broker.go:305-316`
- **Today:** Tests build &Server{Reg, Auth} and fake answers with testPolicy over NoopPolicy. TestLegacyInjectionUnchanged pins that the default asset mode is byte-for-byte unchanged.
- **Change needed:** Any new question the server asks the broker (for example which tree serves a deployment's frontend) becomes a Policy method, with a NoopPolicy default equal to today plus a brokerPolicy answer. Add a 'tile without deployments is unchanged' test for /c/ serving and /components.
- **Compat:** NoopPolicy must reproduce today's answers so server tests and apicheck, which run without the broker's policy, stay valid.

### A deployments plane on the D63 pattern

- **Paths:** `internal/obs/obs.go:18-33`, `internal/obs/status_test.go:89-96`, `internal/broker/broker.go:246-294`
- **Today:** obs.Plane takes the inputs it needs from the broker (Root, Hub, IsAdmin, HasComponent) as fields. The broker builds and mounts it in Register. Its tests use a struct literal of those inputs, with no broker.
- **Change needed:** Model the deployments state (store, promotion, pause) as a struct of inputs, unit-tested standalone. Mount it from broker.Register or from internal/boot.
- **Compat:** The RegisterAPI literals must stay where apicheck finds them. Mounting must be wire-identical for existing routes.

### Terminal package tests

- **Paths:** `internal/term/gates_test.go:16-60`, `internal/term/agent_test.go:30-80`, `internal/term/term.go`
- **Today:** Gate matrices use NewManager(t.TempDir(), nil) plus httptest. The agent tests build the real bx and fakeacp in TestMain. term.go is at 950/950.
- **Change needed:** If the terminal window's server side gains pause or deployment info (session frames, /ws/term/env), extend the gate tests. term.go cannot grow, so split it first or put the code in a new file.
- **Compat:** Session frames and env answers only gain fields.

### In-process boot tests

- **Paths:** `internal/boot/boot_test.go:45-69`, `internal/boot/boot_test.go:116-185`, `internal/boot/boot_test.go:218-278`, `internal/boot/config_doc_test.go:17`, `internal/boot/boot.go:92-112`, `internal/boot/config.go:50`
- **Today:** TestStepsOrder pins boot ordering. TestConfigDoc keeps docs/config.md in sync with the Config tags. The legacy workspace boots twice in-process in about 0.2 s.
- **Change needed:** A new boot step needs an edge in TestStepsOrder. A new flag needs a tagged Config field and a regenerated docs/config.md (UPDATE_DOCS=1). A migration extends the allowed list.
- **Compat:** A flag's default must equal today's behavior, and the second boot must change nothing.

### Integration suite (test/)

- **Paths:** `test/integration_test.go:38-160`, `test/integration_test.go:179-233`, `test/integration_test.go:465`, `test/alwayson_test.go:13`, `test/streaming_test.go:85`, `test/prs_test.go:22`
- **Today:** One shared daemon, non-isolated, --no-auth. Real Go builds from copied examples or inline backends. Tests that need auth start their own daemon. Git runs directly in test code.
- **Change needed:** Put new tests in a new file, test/<feature>_test.go. Cover: pause suppresses swap and reload, reload-now applies; the dev deployment has its own data (calendar kv); bindings and traffic reach the primary only (email to calendar); promotion; a timing sanity check.
- **Compat:** Do not edit examples/ (native fixtures import calendar and counter-go). Do not append to integration_test.go (a conflict magnet).

### Legacy-workspace fixture (D62)

- **Paths:** `test/legacy_workspace_test.go:31-97`, `test/legacy_workspace_test.go:171-212`, `internal/boot/boot_test.go:116-185`, `docs/compat.md`
- **Today:** An aged scaffold boots twice. Boot 1 is limited to an allowed path list, and the root xbin.json must stay byte-identical. Boot 2 must change nothing. The in-process twin is part of make check.
- **Change needed:** Only if a boot migration is added: extend both allowed lists with the reason, and add an explicit assertion if tile .git refs are touched, because the /.git/ wildcard would otherwise hide it.
- **Compat:** Compat rules 1 and 9. State under .xbin/ and data/ is not part of the contract.

### Confined tool runs (git for deployments)

- **Paths:** `internal/confine/guard_test.go:22-65`, `internal/confine/git.go:14-68`, `internal/confine/confine_test.go:31`, `internal/confine/sandbox_linux_test.go:19-80`, `internal/runner/build_linux_test.go:32-91`, `Makefile:110-117`, `.github/workflows/ci.yml:9-76`
- **Today:** TestNoDirectExec is in make check. Unit tests always run confine in direct mode. The sandboxed tests need .rootfs with git and skip in CI.
- **Change needed:** Every new git operation (branch switch, snapshot, promote, receiving a push) goes through confine.Git* or Run, with a hostile-repo integration test in its package. Add that package to the Makefile integration target. Consider XBIN_TEST_ROOTFS with git in CI.
- **Compat:** With isolation off, tools run directly with the hardened environment. With isolation on, a sandbox failure is an error, never a host fallback.

### UI harness

- **Paths:** `hack/ui-harness/run.sh:22-116`, `hack/ui-harness/seed.sh`, `hack/ui-harness/lib.js:13-175`, `hack/ui-harness/shots.js:16-34`, `hack/ui-harness/shots.js:845-870`, `hack/ui-harness/passes/vmtoggle.js`, `web/bx-frame.js:563`
- **Today:** A freshly seeded workspace (auth on, not isolated) and Playwright passes that drive testApi() only. Results are PASS/FAIL/SKIP files. The PASSES registry is a single line; shots.js is at 870/877.
- **Change needed:** Add passes/<x>.js files: the pause toggle and reload-now (write into $WS/<tile>, assert no reload while paused, reload-now applies), and the deployment picker. Add testApi entries on bx-frame (tight budget). Seed a fixture tile.
- **Compat:** Passes must leave no prefs or sessions behind. Parallel agents need a unique PORT and HARNESS_DIR each.

### JS unit tests

- **Paths:** `Makefile:141-143`, `hack/term-sessions.test.mjs`, `web/term-sessions.js`
- **Today:** Pure web modules that import nothing and take fetch/storage as arguments are tested with node --test.
- **Change needed:** Write the terminal window's pause and deployment state logic as such a module, with hack/<x>.test.mjs.
- **Compat:** The module imports nothing; only the element imports by absolute /vendor/ URL (no bare specifiers, compat rule 3).

### Size budget ratchet

- **Paths:** `hack/size-budget.txt`, `internal/sizebudget/sizebudget_test.go:21-24`, `internal/sizebudget/sizebudget_test.go:67-140`
- **Today:** Listed files have ratchets, with 0 headroom on term.go and cmd/bx/main.go and 17 lines on runner.go. The caps are 800 (Go) and 900 (JS/HTML). A listed file below 90 % of its budget fails.
- **Change needed:** Prep WP moves code verbatim into new files and lowers the numbers. Feature WPs add new files instead of growing budgeted ones.
- **Compat:** Internal only.

### Route inventory, OpenAPI and protocol.md

- **Paths:** `internal/apicheck/apicheck_test.go:122-330`, `internal/server/openapi.go:83-486`, `docs/protocol.md:421-1934`, `internal/boot/vm.go:38-66`, `internal/boot/api.go:20-134`, `internal/server/openapi_test.go:9`
- **Today:** A three-way check between mounted routes, openapi.go rows and protocol.md rows. RegisterAPI literals in internal/boot/*.go are covered automatically.
- **Change needed:** Every new route is a literal plus an openapi row plus a protocol row, in one commit. Put the literals in internal/boot/<x>.go or broker.Register; optionally register them all up front in a contracts WP.
- **Compat:** Additive only (compat rule 2). Existing rows and response fields are never removed or retyped.

### docscheck and the design artifacts

- **Paths:** `internal/docscheck/docscheck_test.go:24-33`, `internal/docscheck/docscheck_test.go:71-172`, `plans/DECISIONS.md`
- **Today:** Cited decision IDs must be defined in plans/. Relative links must resolve. Top-level plans need a status line.
- **Change needed:** The design artifacts carry a status line and number their decisions locally. The integrator assigns D-IDs and then adds the citations.
- **Compat:** None.

### Embedded-asset and frontend guards

- **Paths:** `assets_test.go:27-90`, `hack/check-js.mjs`, `hack/theme-fallbacks.mjs`, `internal/assetscan/shipped_test.go:17`
- **Today:** No plans/ citations in the embedded trees. JS parse checks and import resolution. Theme fallbacks must equal theme.css. Shipped tiles must pass strict asset gating.
- **Change needed:** New web/ modules cite docs and decision IDs only. Their CSS fallbacks are copied from theme.css. Any shipped fixture tile must pass strict gating.
- **Compat:** /vendor/ URLs are frozen (compat rule 3), so add new files rather than renaming.

### Changelog, decision log and migration notes

- **Paths:** `docs/changelog.md`, `plans/DECISIONS.md`, `docs/changes/`, `AGENTS.md`
- **Today:** Single-author series follow the same-commit rule. In the native swarm, WPs were barred from editing these files and the integrator consolidated them (e1100f6, b81f93c, e32df4f).
- **Change needed:** WPs hand changelog and decision texts to the integrator, who deduplicates them, assigns IDs and writes any migration note.
- **Compat:** A BREAKING entry needs a migration note under docs/changes/. None is expected if tiles that do not opt in are unchanged.

### Registration seams (bx, manifest, events)

- **Paths:** `cmd/bx/native.go:26-32`, `cmd/bx/agent.go:30-46`, `cmd/bx/main.go:174`, `internal/registry/registry.go:75-83`, `internal/registry/vm.go`, `internal/events/events.go:11-17`
- **Today:** bx has the moreCmds map and usage-text concatenation (main.go is at budget). Manifest keys get their own parse file. events.Event has fixed fields plus a Data field.
- **Change needed:** New bx deploy/pause commands register through moreCmds from a new file. The manifest key gets its own file. Event payloads go in Data or in new event types.
- **Compat:** CLI stays a superset (rule 6). The manifest is additive with unknown keys ignored (rule 7). Events only gain fields.

### Clients that consume events

- **Paths:** `web/bx-frame.js:366-395`, `web/bx-code.js:268`, `workspace-template/shell/bx-shell.js:195`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:54-55`, `docs/protocol.md:2191-2230`
- **Today:** reload and build-* events are keyed only by component. The shipped app maps any reload with a component to a view reload.
- **Change needed:** Activity on a non-primary deployment must use new event types, or be filtered out, so that old clients do not act on it.
- **Compat:** Shipped mobile apps lag by months (rule 10). Old shells ignore unknown event types.

### Docs that state the hot-reload contract

- **Paths:** `docs/overview/03-components.md:208-228`, `docs/overview/01-model.md:78`, `docs/index.md:6`, `docs/elements.md:356`, `docs/overview/04-frontend.md:112`, `docs/protocol.md:2311-2327`, `workspace-template/AGENTS.md`, `workspace-template/apps/welcome/notes.js:81`, `plans/dev-flow.md:83-105`
- **Today:** 'Every save hot-reloads' is stated in many places. welcome/notes.js is at 1028/1050 lines.
- **Change needed:** Wherever it is stated, add 'unless paused / only the attached deployment'. Grep every statement, per AGENTS.md's contract-change rule.
- **Compat:** The statements stay true for tiles that do not opt in.

### CI and make targets

- **Paths:** `Makefile:105-207`, `.github/workflows/ci.yml:9-114`, `.githooks/pre-commit`
- **Today:** CI runs check, tile-check and integration. The harness is not in CI, and no rootfs is built for the confine tests.
- **Change needed:** Add new integration packages to the Makefile target. Optionally add a rootfs with git to CI for the confine tests. Document any new guard in docs/maintenance.md.
- **Compat:** None.

## Hazards

- Emitting the existing reload or build-* events for a non-primary deployment would reload or flag the primary tile everywhere: bx-frame keys only on component (web/bx-frame.js:366-379), and shipped iOS apps map any reload with a component to a view reload (native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:54-55) and lag by months (docs/compat.md rule 10).
- The /c/ static plane serves the live tile directory (internal/server/static.go:70-72), so a pause that only suppresses reload events still ships frontend edits to anyone who opens or refreshes the tile.
- The runner rebuilds from the live tree whenever a component's state is dirty: the next request after an idle reap, a crash restart, a grant change (OnGrantChange calls run.Changed, internal/boot/boot.go:497-501) or a daemon restart (internal/runner/runner.go:187-280). Paused code is therefore not pinned unless every one of these paths is gated or builds come from a snapshot.
- The watcher ignores dot-directories at every depth (internal/watch/watch.go:60-74), so ref updates from a push into a tile's .git never trigger anything; push-to-deploy needs its own trigger.
- CI never runs the sandboxed tool-run tests (no .rootfs in .github/workflows/ci.yml; they skip at internal/confine/sandbox_linux_test.go:26-34 and internal/runner/build_linux_test.go:33-41), so confined git for deployments would be verified only on a developer box.
- Integration tests (tag linux && integration) in a new package never run unless the package is added to the explicit list in Makefile:110-117; internal/sandbox's integration tests already sit outside it.
- Citing a new decision ID before plans/DECISIONS.md defines it fails make test (internal/docscheck/docscheck_test.go:71-111), and a plans/ path in web/, docs/ or workspace-template/ fails TestEmbeddedAssets (assets_test.go:27-90); the VM series tripped exactly this (commit 2114643).
- internal/term/term.go (950/950) and cmd/bx/main.go (1150/1150) have no headroom and internal/runner/runner.go has 17 lines (hack/size-budget.txt), so parallel WPs touching them fail make test or collide unless a verbatim-move prep commit lands first.
- A RegisterAPI literal that is neither mounted by broker.Register nor placed in internal/boot/*.go fails TestRouteInventory (internal/apicheck/apicheck_test.go:153-275).
- Parallel harness runs collide on the defaults PORT=8697 and HARNESS_DIR=$TMPDIR/xbin-ui-harness, and stop() pkills by port and by workspace path (hack/ui-harness/run.sh:22-66).
- A merge without textual conflicts can still break behavior (native/AGENTS.md:375-383); the previous swarm's real conflicts hit openapi.go, protocol.md, shots.js, the server files and internal/boot/serve.go (merge commits 6b7f652, 02d2bab, 8dfb976).
- The legacy fixture allows any change under a path containing /.git/ (test/legacy_workspace_test.go:74-86), so a boot migration that rewrites tile branches or refs would pass unnoticed.
- The only end-to-end coverage of hot swap is TestGoBackendLifecycle (test/integration_test.go:179-233), on a non-isolated --no-auth daemon (test/integration_test.go:85), so per-deployment data separation through sandbox binds is not exercised by the existing suite.
- workspace-template changes (shell, admin console) reach existing workspaces only through bx builtin update (docs/compat.md rule 4); a toggle placed there would be missing from every existing workspace, while one in web/ (served at /vendor/) ships with the binary.
- The internal/term unit tests compile ./cmd/bx in TestMain (internal/term/agent_test.go:30-49), so one WP breaking the bx build fails a seemingly unrelated package.
- New handlers copied from internal/boot/vm.go or internal/boot/api.go use http.Error, which violates the documented server.WriteError wire shape (internal/server/api.go:48-59; docs/maintenance.md 'Server helpers'); no guard enforces it.

## Open questions

- Will the swarm follow the native precedent: an integration branch, WPs barred from docs/changelog.md and plans/DECISIONS.md, and an integrator who assigns D-IDs and writes migration notes? Who is the integrator?
- Should a contracts WP register every planned route up front (literals in internal/boot/<feature>.go, with their openapi.go and protocol.md rows) to remove the openapi/protocol conflict hotspot, accepting stub handlers on the integration branch?
- Should CI gain a rootfs with git (XBIN_TEST_ROOTFS), so confined git operations for deployments run in CI and not only on a developer box?
- Is a boot.Config flag needed (a D95-style default switch), or does per-tile opt-in (a manifest key or the terminal toggle) by itself guarantee zero change for everyone else?
- Where do the design artifacts live: one top-level plans/<name>.md with a status line plus per-WP appendices in plans/<feature>/? What is the feature called, given that plans/deployment.md and the LC series are taken?
- D-numbers assigned at integration, or a new per-domain decision series (which requires extending both docscheck regexes)?
- Should the UI harness gain an --isolate mode (the local box has .rootfs) for data-separation passes, or is that covered by unit tests on EnvFor and resourceBinds plus a manual isolated run?
- Does pause also gate grant-change restarts, lazy starts after an idle reap, crash restarts and daemon restarts, or only file-change rebuilds? The answer decides which acceptance tests are mandatory.
- Should the latency budgets in plans/dev-flow.md:102-105 become asserted tests before the pipeline changes, so the default hot-reload loop cannot regress unnoticed?
