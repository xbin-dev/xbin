# 15 — Test plan

> Status: live — how every property of pause live reload, tile deployments and promotion is verified: the M0 seam, unit tests per plane, sandboxed and end-to-end suites, UI harness passes, CI and traceability (part of [plans/dev-lifecycle](README.md))

This plan names every test the implementation swarm writes, the file it lives
in, what it needs from the host, and what it discharges:
- an invariant P1–P21 ([05-model.md](05-model.md) §13);
- a threat T1–T17 ([06-security.md](06-security.md));
- a success criterion ([02-goals.md](02-goals.md) §5).

Work packages in [14-implementation.md](14-implementation.md) pick tests from
here by name. Facts about today's test infrastructure come from
[research/delivery-infra.md](research/delivery-infra.md),
[research/ui-surfaces.md](research/ui-surfaces.md),
[research/git-confine.md](research/git-confine.md) and
[research/sandbox-visibility.md](research/sandbox-visibility.md), re-verified
in this worktree (master + `sandbox-visibility`). File and package names for
new code follow [13-surfaces.md](13-surfaces.md); where that document names a
package differently (this plan says `internal/checkpoint` for the checkpoint
store and `internal/deploy` for the deployments plane), the test names stay
and the paths follow.

## 1. Principles

1. **Test at the lowest layer that can express the property.** This is
   already the repo's stated policy (`plans/dev-flow.md:56-59`). A property
   that a pure function or a fake can decide gets a unit test. The
   integration suite proves the wiring, and the harness proves what a person
   sees.
2. **Zero state first.** The zero-state tests (§2.7) are written in M0
   against today's code and are green on master before any feature code
   lands. They then stay unchanged through M1–M3. Changing one is a compat
   change: it needs a [12-compat.md](12-compat.md) entry and the
   integrator's sign-off. The golden expectations are hand-maintained, and no
   `UPDATE_*` switch regenerates them.
3. **Coverage obligations.**
   - Every invariant P1–P21 has at least one test (§11.1).
   - Every threat T1–T17 has a test, or an explicit **manual** note that
     says why no automated test can hold it (§9).
   - Every success criterion of 02-goals.md has a test (§11.2).
4. **Old clients are fixtures, not assumptions.**
   - What old shells, `bx`, tiles and shipped iOS apps (which lag xbind by
     months, [/docs/compat.md](/docs/compat.md) rule 10) do with new events
     and listings is asserted against their actual matching code:
     `web/events-socket.js:40-49` in a node test, and `ReloadTargets` in the
     app's Swift tests.
   - Nobody asserts that "old clients ignore it" without such a test.
5. **Honest skips.**
   - A test that needs a rootfs, user namespaces, gocryptfs, KVM or
     Playwright skips with the reason (`t.Skip`, or the harness `SKIP`
     line).
   - It never times out and never passes silently. The confine test is the
     precedent (`internal/confine/sandbox_linux_test.go:26-35`).
6. **No test-only branches in shipped code.** Seams are injected
   defaults whose shipped value is today's code, moved verbatim (§2.2).
   Nothing in xbind checks "am I under test", and nothing ships as a
   test-only knob (such as a shorter idle reap).
7. **Wait for conditions, never for time.** Use bounded polls (`waitFor`,
   `waitUntil`, the harness's `waitFor`/`waitSel`). A fixed wait is
   allowed only for a negative ("no reload arrives"), and then it is
   bounded and commented.
8. **Ownership keeps merges clean.**
   - New tests go in new files owned by one work package.
   - Nobody edits `testWorkspace` (`internal/broker/broker_test.go:14-46`),
     `test/integration_test.go`, or `examples/`. The examples double as
     native fixtures.
   - These are serialized and belong to the integrator (or the M0
     contracts WP): the Makefile integration list, `ci.yml`, both legacy
     allowed lists, the `PASSES` line in `hack/ui-harness/shots.js`, and
     `hack/ui-harness/seed.sh`.
9. **Traceable.** A new test's doc comment starts with the IDs it covers,
   for example `// covers P9 T11 — pinned code survives a crash restart`.
   Then `grep -rn 'covers .*P9'` lists every test of an invariant.
10. **Done means run.** A WP's commit body lists the tests it ran and the
    ones it could not, with the reason.
    - Always: `make check`.
    - `make integration` whenever runner, sandbox, broker or confine code
      changed.
    - The confined and isolated tests locally with `XBIN_TEST_ROOTFS`.
    - The named harness passes.

**Layers** used in the tables:

| Layer | Runs in | Needs | Build tag |
|---|---|---|---|
| unit | `make test` (inside `make check`) | Go | — |
| unit-git | `make test` | host `git`; skips without it, like `TestGitDirect` (`internal/confine/confine_test.go:31-37`) | — |
| confined | `make integration` | user namespaces + a rootfs with git (`XBIN_TEST_ROOTFS`, or `.rootfs`) | `linux && integration` |
| integration | `make integration` (`./test/...`) | Go toolchain, network on first run | `integration` |
| isolated | `make integration` | as integration, plus user namespaces and a rootfs; gocryptfs for file resources | `integration` |
| harness | `hack/ui-harness/run.sh` (not in CI) | Playwright (`PLAYWRIGHT_DIR`) | — |
| js-test | `make js-test` (`Makefile:141-142`) | node | — |
| swift | `make swift-test` (CI job `native`) | swift | — |
| manual | the §10.3 checklist | an isolated xbind with auth on | — |

**Milestones**: M0 prep, M1 pause live reload, M2 tile deployments, M3 later
rungs ([14-implementation.md](14-implementation.md)).

## 2. The M0 test seam

### 2.1 What is missing today

- **The runner's tests cover only pure helpers:**
  - `TestAlwaysOnBackoff` (`internal/runner/alwayson_test.go:8`);
  - `TestResourceBinds` (`internal/runner/resourcebinds_test.go:9`);
  - `TestRootfsBin`, the proc-stats tests, and the host-env allow-list
    (`internal/runner/hostenv_test.go:9`);
  - D112's registry tests (`internal/runner/sbx_test.go:20-107`).
- **Nothing drives the state machine.** No test calls `Ensure`, `Changed` or
  `buildAndStart` (`internal/runner/runner.go:190`, `:265`, `:287`).
- **There is no injection point.** `build` and `start` are plain methods
  (`internal/runner/runner.go:365`, `:413`). `New` starts `reaper()`, which
  loops on `time.Tick` (`internal/runner/runner.go:164-174`, `:809-830`).
- **Hot swap is tested only end to end,** on a non-isolated daemon
  (`TestGoBackendLifecycle`, `test/integration_test.go:179-255`).
- **The promised suites were never built.** `plans/dev-flow.md:52` promises
  a "runner state machine (fake exec)" unit suite, and `:57-59` says the
  runner and the watcher "get table-driven unit suites from day one".
- **`internal/watch` has no tests** at all (the package is only `watch.go`).
  `changedComponents` is the one table-tested piece of the save-to-reload
  path (`internal/boot/serve.go:192`; `TestChangedComponentsNativeEntry`,
  `internal/boot/watch_native_test.go:16`).

### 2.2 The runner seam

**Where.** One new non-test file, `internal/runner/engine.go`, lands in
the M0 prep WP. It adds no behaviour: the default engine calls today's
methods, which are moved verbatim. `runner.go` has 19 lines left
(831/850, `hack/size-budget.txt:15`). The one new field therefore goes in
together with a verbatim move of `Status`/`reaper` into a new file.

```go
// internal/runner/engine.go — M0. A nil Runner.engine means today's methods.
type engine struct {
	build   func(c *registry.Component) (bin string, err error)                 // r.build (M1: + the code ref)
	start   func(c *registry.Component, bin string, gen int) (*instance, error) // r.start
	healthy func(c *registry.Component, inst *instance) error                   // waitHealthy(inst.sock, inst.waitCh, r.healthFor(c))
	stop    func(inst *instance, deadline time.Duration)                        // r.stop
	now     func() time.Time                                                    // time.Now
}
```

Rules for the seam:
- `buildAndStart`, `Ensure`, `Track` and the crash watch call only the
  engine's functions. `lastReq` is stamped with `now()`.
- `reaper()` becomes a `time.Tick` loop around a new `reapOnce()`, which
  reads `now()`. Tests call `reapOnce()` after moving the fake clock. No
  shipped constant changes: `idleReap` stays 30 minutes
  (`internal/runner/runner.go:46`).
- M1 widens `build` to `build(c, ref codeRef)`, where
  `codeRef{Deployment, Checkpoint}` has `Checkpoint == ""` for the work tree.
  M1 also adds `artifact(c, ref) (bin string, ok bool)`, which finds a
  retained per-checkpoint artifact, so that a restart can start one without
  building (P9).
- M2 keys runner state by (tile, deployment). `Ensure(c)` stays as a shim
  for `Ensure(c, primary)`, so existing callers compile unchanged (§2.5,
  row 33).

### 2.3 The fake engine and the table

`internal/runner/fake_engine_test.go` (new):

```go
type fakeEngine struct {
	mu    sync.Mutex
	log   []string          // "build apps/x main@worktree", "start apps/x main g2", "stop apps/x main g1"
	fail  map[string]int    // "build apps/x main" or "health apps/x main" → fail the next n
	gens  map[string]*instance
	clock time.Time
}

func newSeamRunner(t *testing.T, comps ...*registry.Component) (*Runner, *fakeEngine, *tape)
func (f *fakeEngine) crash(tile, dep string)   // closes the current generation's waitCh
func (f *fakeEngine) advance(d time.Duration)  // moves the fake clock
func settle(t *testing.T, r *Runner)           // bounded (2 s): no state building, no Ensure pending
```

How the fake works:
- **The runner is built without `New`,** so no real reaper starts:
  `&Runner{Root: t.TempDir(), Hub: events.NewHub(), states: map[string]*state{}, netmux: newNetMux(), engine: f.engine()}`.
- **Fake generations:** a fake `instance` has a `waitCh` and a zero `cmd`.
  Fake `stop` closes `waitCh` (it is idempotent), and `crash` closes it
  without replacing the generation. That exercises the real crash watch
  (`internal/runner/runner.go:332-356`).
- **Events:** `tape` subscribes to the hub and records each event as
  `"<type> <component>"`, plus the deployment field when present.
- **Row shape:** a row is a list of steps, then the exact fake log, the
  exact tape, and the final `Status()` entry. The steps are:
  - `ensure`, `save`, `crash`, `reap+31m`, `grant`, `restart`, `seal`,
    `unseal`;
  - `pause`, `resume`, `reload-now`, `deploy <dep> <ckpt>`,
    `fail-next-build`, `fail-next-health`.

  `restart` means a new Runner over the same `Root` and deployment store.
  It is how xbind restarts look to the runner.
- **Budget:** the whole table runs in well under a second, and
  `go test -race ./internal/runner/` must be clean.

### 2.4 Rows that pin today (M0, `internal/runner/statemachine_test.go`)

These rows must pass on master with the seam and **no** feature code. From
row 2 on, the log and tape continue after the first `ensure`'s
`build`/`start g1` and `build-start`/`build-ok`, which are not repeated.

| # | Steps | Fake log | Tape | After |
|---|---|---|---|---|
| 1 | ensure | build main@worktree; start g1 | build-start, build-ok | healthy g1 |
| 2 | ensure; save | build @worktree; start g2; stop g1 after g2 is healthy | build-start, build-ok | healthy g2 |
| 3 | save (no process yet) | — (`hadProcess` false, `runner.go:269-279`) | — | idle |
| 4 | ensure; fail-next-build; save | build fails, no start | build-start, build-error | g1 still serves (`Ensure` returns it, `runner.go:204-208`) |
| 5 | ensure; fail-next-health; save | build; start g2; stop g2 | build-start, build-error ("did not become healthy") | g1 serves |
| 6 | ensure; crash; ensure | build; start g2 | build-start, build-ok | healthy g2 (dirty after a crash, `runner.go:353`) |
| 7 | ensure; crash ×`crashLimit` inside the window | no build | build-error ("crash-looping") | failed until a save |
| 8 | ensure; reap+31m; ensure | stop g1; build; start g2 | build-start, build-ok | healthy g2 (reap marks dirty, `runner.go:819-822`) |
| 9 | ensure; hold `Track`; reap+31m | — | — | healthy g1 (an active stream is never reaped) |
| 10 | ensure (alwaysOn manifest); reap+31m | — | — | healthy g1 |
| 11 | ensure; three saves during one slow build | exactly two builds | two build-start/ok pairs | healthy |
| 12 | `ShouldRun` false; ensure; save | — | — | refusal error (`runner.go:196-198`) |
| 13 | ensure; grant | build; start g2 | build-start, build-ok | healthy g2 (`OnGrantChange` → `Changed`, `internal/boot/boot.go:508-512`) |
| 14 | restart; ensure | build @worktree; start g1 | build-start, build-ok | a fresh state is dirty (`runner.go:181`) |
| 15 | runtime `static` / `cgi` / none; ensure; save | — | — | "no long-running backend" (`runner.go:191-193`) |

### 2.5 Rows added by M1 and M2

The file is `internal/runner/pinned_test.go` (M1) and
`internal/runner/deployments_test.go` (M2). Steps are the §2.3 tokens, and
`pause`/`resume` mean pause and resume live reload. `c1` is the checkpoint
that pausing live reload takes. Rows 16–30 and 38 are M1, rows 31–37 are
M2.

| # | Steps | Expected | Covers |
|---|---|---|---|
| 16 | pause; save | no build and no event for the tile | P8 P9 |
| 17 | pause; crash; ensure | `start` from `artifact(c1)`, no build | P9 |
| 18 | pause; reap+31m; ensure | `start` from `artifact(c1)` | P9 |
| 19 | pause; grant | `start` from `artifact(c1)`, never a build @worktree | P9 |
| 20 | pause; restart; ensure | `start` from `artifact(c1)` | P9 |
| 21 | pause; drop artifacts (loss of `.xbin/`); restart; ensure | build @c1 from the materialized checkpoint, then start | P9 T11 |
| 22 | seal; unseal (`ShouldRun` false → true) with live reload paused | `start` from `artifact(c1)` | P9 |
| 23 | ensure (live reload on); pause, work tree unchanged since the last build | exactly one swap onto @c1, never a build @worktree | P9, 05-model §5 |
| 24 | ensure; edit; pause | build @c1 (the new content); swap | 05-model §5 |
| 25 | ensure; broken edit; pause | build @c1 fails; no bare `build-*` or `reload` for the tile (NP-02-4); the old generation serves until it exits, and any restart runs c1 and fails visibly, never @worktree; the state says pausing live reload did not complete (NP-02-11, Divergences 5) | SC-LIVE-RELOAD-PAUSE, P9 |
| 26 | pause; edit; reload-now | build @c2; swap; exactly one ordinary `reload` for the bare tile; the status clears when the new generation becomes current, not at `build-start` (NP-02-4); live reload still paused | P9, P13, flow A |
| 27 | pause; resume | build @worktree; swap; following the work tree again | flow A |
| 28 | deploy c2, then deploy c3 while c2 builds | never two builds in flight for one deployment; final code c3 | T10 |
| 29 | ensure (live reload on); deploy c2 | live reload pauses; pinned to c2 | glossary "Deploy" |
| 30 | save on a zero-state tile, counting checkpointer and store fakes | zero checkpoint calls and zero store reads | P5 P8 |
| 31 | main pinned c1, `dev` live; save | build dev@worktree only; events only in the non-primary shape ([11-contract.md](11-contract.md)) | P8 P13 |
| 32 | as 31; crash `dev` | only `dev` restarts; main's generation and events untouched | P13 |
| 33 | `Ensure` from each existing caller | always the primary: `internal/proxy/proxy.go:182`, `internal/proxy/ingress.go:58`, `internal/runner/ingress.go:37`, `internal/runner/alwayson.go:74`, `internal/runner/netmux.go:62`, and `Changed`'s own `internal/runner/runner.go:281` (the static guard is `TestEnsureCallSitesPassPrimary`, §3.13) | P7, SC-INBOUND |
| 34 | `Stop(tile)` | stops every deployment | 05-model §11 |
| 35 | alwaysOn in `dev`'s checkpoint, with its switch off, then on | not started, then started; the reaper exemption covers the primary and switched-on deployments only | P13 T5 |
| 36 | VM tile with file resources (`stopFirst`) | the old generation of the same deployment stops first, never another deployment's | P9 |
| 37 | reassign the primary to `dev` | routing flips; neither deployment rebuilds | P7 |
| 38 | pause; broken edit; reload-now (and the same for deploy, promote, roll back) | build fails; c1 keeps serving; no bare `build-*` or `reload`; the tile's status is unchanged; the failure and compiler output go to the actor and the `deployments` event | SC-SAFE-DEPLOY (NP-02-4) |

### 2.6 Watcher and `changedComponents` tables

- **`TestIgnoreRules`** (new file `internal/watch/watch_test.go`, M0).
  - It is a table over `ignoreDir`/`ignoreFile`
    (`internal/watch/watch.go:46-74`).
  - These are ignored: `.xbin/deploy/**`, `data/checkpoints/**`,
    `data/deployments/**`, any `.git/**`, `node_modules/**` and editor
    droppings.
  - Ordinary tile files are not ignored.
  - This pins that materializing a checkpoint, or writing the record, can
    never trigger a reload.
- **`TestChangedComponentsNesting`** (new file
  `internal/boot/watch_nesting_test.go`, M0).
  - It covers today's mapping for nested components: a change in
    `apps/a/b/x.js` maps to `apps/a/b` only.
  - A batch spanning two tiles maps to both.
  - `TestChangedComponentsNativeEntry` itself stays untouched.
- **`TestReloadPlan`** (new file `internal/boot/livereload_test.go`, M1/M2).
  - The live-reload gate is a pure function beside `changedComponents`. It
    maps (reload set, restart set, live-reload state) to (events to
    publish, `Changed` targets, pending counts).
  - Rows:
    - zero state → exactly today's output;
    - live reload paused → no event and no `Changed`;
    - live reload on `dev` → only the non-primary event and
      `Changed(tile, dev)`;
    - a native-only edit on `dev` → a non-primary reload, no restart;
    - a nested tile with its own record → it is gated independently of its
      parent;
    - a static tile with live reload paused → nothing.
- **`TestWatchLoopZeroStateNoStoreIO`** (M1). With a counting store fake, a
  batch for zero-state tiles reads only an in-memory map: no disk I/O and no
  confined run (P5 P8).
- **`TestPendingCountAfterDroppedBatch`** (M1).
  - The watcher drops a batch when its consumer is slow
    (`internal/watch/watch.go:170-177`).
  - So the "changed since the checkpoint" count must be recomputed from
    the work tree, never summed from batches.
  - The test drops a batch deliberately and asserts the count.

### 2.7 Zero-state goldens (M0, before any feature code)

These tests discharge SC-ZERO and SC-OPT-OUT ([02-goals.md](02-goals.md)
§5, with the clauses Z1–Z12 of its §6) and the proof obligations PO-1–PO-15
of [12-compat.md](12-compat.md). Every one is green on master in M0. Values
that already differ between two runs of today's xbind (tokens, socket
numbers, PIDs, timestamps) are masked, as SC-ZERO allows.

| Obligation | Test (file) | Layer |
|---|---|---|
| URLs (Z2, PO-1): `/c/<tile>/…`, `?native=1`, `/api/<tile>/…`, and the tile-origin and asset-token URLs answer with today's status, headers and bytes, in `legacy`, `tokens` and `origins` modes | `TestZeroStateDocumentUnchanged` (`internal/server/zerostate_test.go`), in the style of `TestLegacyInjectionUnchanged` (`internal/server/tileassets_test.go:187`) | unit |
| Listings (Z7, PO-14): `/components` rows keep their keys and never gain deployment rows; `/backends`, `/runtime`, `/tile-status`, `/whoami`, `/cron/jobs` and `/bus/subscriptions` answer as today, keyed by path with no new keys; D112's `/sandboxes` main rows are unchanged | `TestZeroStateComponentsEntry` (server), `TestZeroStateListings` (`internal/boot/api_zerostate_test.go`) | unit |
| Keys (Z6, PO-2): main's storage names are unchanged (kv buckets and labels, resenc dirs, the vault file, log, build, socket and cgroup names, registry entry IDs, the cron, bus, iface and ingress stores, the backup archive key and members); every non-main key is one no path can produce today | `TestZeroStateKeys` (`internal/broker/deploy_env_test.go` + `internal/runner/zerostate_test.go`), `TestDeploymentKeysDisjoint` (12-compat PO-2), `TestZeroStateBackupMembers` | unit |
| Sandboxes (Z8, PO-11): the launch spec (binds, argv, env, egress), the VM reservation's owner, the flat `comp-<CompKey>` cgroup leaf with today's caps, and every existing `confine.Cmd` bind list are unchanged; the new bind destination defaults to `Dir` | `TestZeroStateLaunchSpec` (`internal/runner/zerostate_test.go`), `TestConfineBindDestinationDefault` (`internal/confine/bind_test.go`) | unit |
| CLI (PO-13): every `bx` invocation accepted today parses to the same request | `TestBxTodayInvocationsUnchanged` (`cmd/bx/deploy_test.go`) | unit |
| Backend env: `EnvFor` is unchanged as a joined string (the `internal/broker/ingressfn_test.go:120` style); no `XBIN_DEPLOYMENT` | `TestZeroStateBackendEnv` | unit |
| Session env: identical, including exactly the two `GIT_CONFIG_*` pairs of `internal/term/term.go:787-793` | `TestSessionEnvZeroState` (`internal/term/deploy_test.go`) | unit |
| Headers: no `X-XBin-Deployment` on the primary's calls | `TestIdentifyZeroState` (`internal/proxy/deploy_test.go`) | unit |
| Events (Z3, PO-5): exact JSON bytes of `reload`, `build-*` and `status` (no `deployment` key, no `deployments` event), and a save's sequence `reload`, `build-start`, `build-ok`/`build-error` | `TestEventBytesZeroState` (`internal/server/deployevents_test.go`) | unit |
| Tokens: frame tokens keep five fields with a bare path in field 0 (`internal/auth/frametoken.go:226-232`); the instance map and terminal tokens resolve as today | `TestZeroStateTokens` (`internal/auth/deployment_test.go`) | unit |
| Policy: `NoopPolicy` answers for new methods reproduce today (`internal/server/policy.go:46-53`) | `TestNoopPolicyZeroState` | unit |
| Files (Z1, PO-7): boot plus a save-and-swap cycle creates no `data/deployments/`, `data/checkpoints/`, `.xbin/deploy/` or per-deployment files, and no timer or confined run; the root `xbin.json` and the `data/*.json` stores stay byte-identical; reading a zero-state tile's deployment state (Z10) creates nothing | `TestZeroStateCreatesNoDeploymentFiles` (`internal/boot/deploystate_test.go`), `TestDeploymentStateReadIsPure`, `TestZeroStateLifecycleFiles` (`test/livereload_test.go`) | unit, integration |
| Per-save cost: no checkpoint work or confined run on a save | row 30 (§2.5); `TestWatchLoopZeroStateNoStoreIO` | unit |
| Boot: the legacy fixture and its allowed lists are unchanged | §6 | unit, integration |
| UI: at most one unobtrusive entry point; no frame chip | `hack/deploy-state.test.mjs` zero-state row; harness `livereload` part 0 | js-test, harness |
| Opting out (SC-OPT-OUT, Z12, PO-15): pause live reload → reload now → resume on a `main`-only tile removes the record, and the whole suite above passes again; the only leftover is the checkpoint store with its deploy log (NP-02-2) | `TestOptOutReturnsToZeroState` (plane + integration) | unit, integration |

## 3. Unit tests per plane

Each row gives the property, the test and its file, the layer, the
milestone, and what it covers. Tests that belong to a threat are repeated in
§9 by name only.

### 3.1 Registry, watch and boot

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| Qualified-name resolution: an exact component match wins; otherwise the qualifier after the last `+` names a deployment; names follow the grammar (lowercase, digits, `-`, a leading letter, at most 24 characters); a legacy directory `apps/c+d` keeps resolving; `apps/c+d+dev` is (`apps/c+d`, `dev`); a nested tile is qualified on its own segment | `TestResolveDeploymentQualifier` (`internal/registry/qualifier_test.go`) | unit | M2 | P17 |
| Tile-level fields come from the work tree and deployment-level fields from the deployment's code (05-model §6): a work-tree `runtime` change doesn't reach a pinned main; a new `uses` files a pending grant | `TestManifestFieldSplit` (`internal/registry/deployfields_test.go`) | unit | M1 | P9 P11 |
| A tile with a record stays registered while its work tree lacks `xbin.json`/`index.html`, and keeps its last-known tile-level fields; a zero-state tile disappears as today | `TestPinnedPrimarySurvivesMissingManifest` | unit | M1 | P9 T16 |
| The record lives only in `data/deployments/<CompKey>.json`, never in the root `xbin.json` or the work tree | `TestRecordLocation` (`internal/deploy/store_test.go`) | unit | M1 | P15 |
| An unreadable or invalid record (schema, §4 invariants, name grammar, tile path, missing checkpoints), or one with a newer schema, holds the tile's backends with an admin alert, and pinned deployments are never served from the work tree (06-security.md T11); `TestRecordTilePathMismatchRefused` covers a record naming another tile | `TestRecordValidationFailsClosed`, `TestRecordTilePathMismatchRefused` | unit | M1 | T11 |
| Boot twice with deployment state present: nothing changes outside derived `.xbin/` trees | `TestDeploymentStateBootsTwiceInProcess` (`internal/boot/deploystate_test.go`) | unit | M1 | P15 T11 |
| A new boot step gets its ordering edge | `TestStepsOrder` (`internal/boot/boot_test.go:45`) | unit | M1 | — |

The watcher and gate rows are in §2.6.

### 3.2 Runner (beyond the seam)

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| A non-primary deployment's data dirs are bound at the primary's paths (`Src ≠ Dst`), so the `XBIN_RES_*` values are identical and no bind's `Src` is a primary dir; main's binds equal today's (`resourceBinds`, `internal/runner/binds.go:28`) | `TestNonPrimaryResourceBindsNeverPrimary` (`internal/runner/resourcebinds_deploy_test.go`) | unit | M2 | P6 P14 |
| A pinned or non-primary launch spec binds the materialized checkpoint at `c.Dir`, read-only; the live reload target keeps `{Src: c.Dir, Dst: c.Dir, RO: true}` (`internal/runner/runner.go:640`) | `TestLaunchSpecBinds` | unit | M1 | P9 P16 |
| `XBIN_DEPLOYMENT` is set only for non-primary backends; the host-env allow-list is unchanged | `TestBackendEnvPerDeployment` | unit | M2 | P17 |
| Artifacts are kept per checkpoint while referenced, and a deployment's current checkpoint plus its previous three deploy-log entries count as references (NP-02-3); the live reload target keeps `.xbin/build/<CompKey>/bin` (`internal/runner/runner.go:372`) | `TestArtifactPerCheckpoint` | unit | M1 | P9, SC-ROLLBACK |
| With `Isolate` false: pausing live reload on, deploying to, or adding a deployment of a go, node or python tile is refused with a reason naming `--isolate`; a static tile is allowed; cgi is always refused | `TestNonIsolatedRefusesBackendDeployments` | unit | M1 | P18 T12 T13 |
| Registry entries: main keeps `backend:<CompKey>:g<gen>` (asserted today at `internal/runner/sbx_test.go:43`); a non-primary entry has a distinct ID, `Tile` is the tile, it names its deployment, and its `Leaf` is its own | `TestSandboxEntriesPerDeployment` (`internal/runner/sbx_deploy_test.go`) | unit | M2 | P13 |
| cgroup leaves: a zero-state tile's leaf is unchanged (`runner.go:490-492`); each deployment's generations share that deployment's leaf; an exiting non-primary generation never removes main's leaf (today every exit removes the tile's one leaf, `runner.go:614`) | `TestCgroupLeafPerDeployment` | unit | M2 | T10 |
| Every deployment's VM backend reserves with the owner set to the tile (`Reserve(c.Path, …)` today, `internal/runner/vm.go:80`) | `TestVMReserveOwnerIsTile` | unit | M2 | T10 |
| Instance tokens are registered with (tile, deployment) (`internal/runner/runner.go:488`) | `TestInstanceTokenRegisteredWithDeployment` | unit | M2 | P12 T3 |
| Logs are per deployment; main keeps `.xbin/log/<CompKey>.log` | `TestLogPathsPerDeployment` | unit | M2 | P6 |
| The `setup` env layer is shared by hash: the same `setup` shares a layer, a different one builds another, and GC keeps every layer a deployment or its previous three deploy-log entries reference (NP-02-3); the `setup` run sees the checkpoint, not the work tree | `TestEnvLayerGCKeepsReferenced`, `TestSetupLayerSeesCheckpointNotWorkTree` | unit | M2 | T17 |

### 3.3 The checkpoint store (`internal/checkpoint`)

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| Content follows the glossary: every file except `.git` directories at any depth and nested components; gitignored files included; a nested non-component repo captured **as files, not a gitlink**; symlinks kept as symlinks (absolute and `..` targets included); the exec bit kept; empty dirs, other permission bits, xattrs and FIFOs/sockets/devices dropped, with a warning | `TestCheckpointContent` (`internal/checkpoint/store_test.go`) | unit-git | M1 | P1 T2 |
| Ids: `c:` plus at least 7 hex digits, derived from content; the same tree gives the same id; checkpointing is idempotent | `TestCheckpointIDs` | unit-git | M1 | P1 |
| Incremental: a persistent private index, so re-checkpointing an unchanged 5 000-file tree writes no objects and uses a bounded number of confined runs | `TestCheckpointIncremental` | unit-git | M1 | P8 |
| Caps: the file-count and byte caps refuse with an error that names the cap | `TestCaptureCapsRefuseHugeTree` | unit-git | M1 | T2 T10 |
| The deploy log records who, when, which feed and how (deploy, promote, roll back, reload now, resume), in order | `TestDeployLog` | unit-git | M1 | P1 P10 |
| GC keeps what a deployment, the deploy-log retention or a materialization references, and drops the rest | `TestCheckpointGC` | unit-git | M1 | T10 |
| Materialization is atomic (temp dir + rename), read-only and rebuildable from the store; an interrupted one is never visible | `TestMaterializeAtomicReadOnly` | unit-git | M1 | P9 P16 |
| The fetch remote's files: `update-server-info` refreshes after each checkpoint; only `info/refs`, `objects/**` and `HEAD` are servable, never `config` or `hooks` | `TestFetchRemoteServesOnlyGitFiles` | unit-git | M1 | P16 T15 |
| Every git run goes through confine (a recording hook in direct mode), never touches the tile's `.git`, and runs with the private config | `TestStoreUsesConfineOnly` | unit | M1 | P16 T1 |

### 3.4 Server

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| `/c/<tile>+<name>/` serves that deployment's code; `/c/<tile>/` and `+<primary>` serve the primary; an unknown deployment or a bad name is a 404; `/c/root+dev/` is a 404; a missing slash redirects as today | `TestQualifiedURLRouting` (`internal/server/deployurl_test.go`) | unit | M2 | P7 P17 P19 |
| Opening a non-primary deployment URL: humans with at least `write` may; `read` users and other tiles' element principals get 403; the tile's own tokens only for their own deployment | `TestDeploymentURLGate` | unit | M2 | P7 P20 T9 |
| While `main` is pinned, the bare URL serves the checkpoint; work-tree edits don't show there; `Cache-Control: no-store` is kept (`internal/server/static.go:153`) | `TestPinnedPrimaryServesCheckpoint` | unit | M1 | P9 |
| Containment in all three asset modes: a symlink leaving the materialized tree (`/etc/passwd`, `../../.xbin/…`, a chain, a directory link) is a 404 and is never followed on the host; an in-tree link works; `deps/` links resolve to the provider's primary (Divergences 3) | `TestPinnedServingUsesOpenBeneath` | unit | M1 | P16 T2 |
| Tokens and origins modes: a non-primary document's asset token or origin is bound to (tile, deployment), and never serves the other deployment's files | `TestOriginLabelPerDeployment` | unit | M2 | P17 T3 |
| `?native=1` is generated from the primary's code (the checkpoint when pinned) | `TestNativeDocumentFollowsPrimary` | unit | M1 | P7 |
| The frame token injected into a non-primary document carries the deployment claim; main's has none | `TestFrameTokenClaimInjected` | unit | M2 | P12 P17 |
| A non-primary deployment's frame token fetching `/c/<tile>/` never receives the primary's token (`mayMintFrameToken` compares paths only today, `internal/server/static.go:494-503`), and the reverse holds too | `TestMayMintFrameTokenNeverCrossesDeployments` | unit | M2 | P12 T3 |
| No event naming a non-primary deployment has type `reload`, `build-start`, `build-error`, `build-ok` or `status` (12-compat.md rule C2; Divergences 1). Non-primary activity uses the new types of [11-contract.md](11-contract.md), and the `deployments` event payload is as specified | `TestNonPrimaryUsesNewEventTypes` (`internal/server/deployevents_test.go`) | unit | M2 | P13 P17, SC-EVENTS |
| Non-primary and `deployments` events reach only subscribers with at least `write` on the tile, and compiler output inside them only `terminal`-level ones (NP-02-5; the `pr` precedent, `internal/server/server.go:597-599`) | `TestDeploymentEventsFiltered` | unit | M2 | T7, SC-EVENTS |
| `/ws/term/env` only gains fields, and its terminal-level gate is unchanged (`internal/server/server.go:326-351`) | `TestTermEnvAdditive` | unit | M1 | — |

### 3.5 Proxy

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| A non-primary caller's request carries `X-XBin-Deployment`; an inbound spoofed one is stripped (`identify`, `internal/proxy/proxy.go:264-281`) | `TestIdentifyDeploymentHeader` (`internal/proxy/deploy_test.go`, beside `TestIdentifyHeaders` at `identify_test.go:16`) | unit | M2 | P17 T3 |
| `/api/<tile>+<name>/…` reaches `Ensure(tile, name)` | `TestProxyQualifiedTarget` | unit | M2 | P17 |
| cgi tiles can't pause live reload or get deployments; a zero-state cgi tile works as today (`internal/proxy/proxy.go:175-178`) | `TestCgiRefusedForPinnedDeployments` | unit | M1 | P18 T13 |
| Ingress forwards reach only the primary (`internal/proxy/ingress.go:58`) | `TestIngressForwardPrimaryOnly` | unit | M2 | P7 |

### 3.6 Auth

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| A principal's deployment field defaults to `main`; `whoami` for main is unchanged; there is no deployment principal | `TestPrincipalDeploymentZeroValue` (`internal/auth/deployment_test.go`) | unit | M2 | P11 P17 |
| The instance map resolves a token to (tile, deployment); today's call shape means `main` (`internal/auth/auth.go:445-462`) | `TestInstanceTokenCarriesDeployment` | unit | M2 | P12 T3 |
| The frame-token claim is signed: main keeps five fields; legacy four-field tokens mean `main` (the `TestLegacyFrameTokenUpgrade` pattern, `internal/auth/frametoken_test.go:157`); stripping or replacing the claim fails verification | `TestFrameTokenDeploymentClaim` | unit | M2 | P12 P17 T3 |
| A terminal token records its target; no token may target a protected primary | `TestTerminalTokenTarget` | unit | M2 | P21 T16 |

### 3.7 Broker

**Fixture.** A new `deployWorkspace(t)` / `deployBroker(t)` lives in
`internal/broker/deploy_fixture_test.go`. It must attach a users store like
`testBroker` does (`internal/broker/broker_test.go:48-63`), and it must never
edit `testWorkspace`, which the whole broker suite shares. It writes:
- `apps/calendar`, with a kv, a bus and a `filesystem` resource, and an
  `expose` that has `reader`, `writer` and `admin`;
- `apps/email`, with a `writer` grant on the calendar;
- a provider that declares only a custom role;
- a stream provider;
- a two-tile scope (`apps/shop`, `apps/shop-admin`);
- a chrome tile, and a tile holding `xbin:admin`.

Principals are injected with `auth.WithPrincipal`, and handlers are driven
with the `call` helper (`internal/broker/orgsapi_test.go:56`).

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| `EnvFor(main)` equals today's golden; `EnvFor(dev)` has the same `XBIN_RES_*` and `XBIN_IFACE_*_URL` values (the bare provider URL) plus `XBIN_DEPLOYMENT=dev` | `TestEnvForPerDeployment` (`deploy_env_test.go`) | unit | M2 | P6 P7 P17 |
| kv: a put made with a non-primary deployment's credential is invisible to main and the reverse; `dev` starts empty | `TestKVNamespacePerDeployment` | unit | M2 | P6 P14, SC-DATA |
| Every storage-key function returns today's value for `main`, and for any other deployment a value no path can produce today, even across `util.ScopeKey`'s collision `apps~x`/`apps/x` (`internal/util/util.go:127-132`) and CompKey's (`:137-144`) | `TestDeploymentKeysDisjoint` (12-compat PO-2) | unit | M2 | P6 T11 |
| Adding deployment `b` to `apps/a` is refused while a component `apps/a+b` exists; if one appears later, exact match wins and the deployment is flagged unreachable by URL (NP-02-6) | `TestAddDeploymentNameCollision` | unit | M2 | P17 |
| Flow G: `apps/shop+dev` and `apps/shop-admin+dev` share the scope's `dev` namespace; a sibling with no `dev` uses the primary namespace; `apps/shop-admin+dev` → `apps/shop` is an edge (clamped) | `TestMultiTileScopeNamespaces` | unit | M2 | P6 P3 |
| Read clamp: a non-primary caller with `writer` gets `reader`; a custom role clamps to the provider's `reader` if declared, else blocks with an error naming the policy; bus aliases map; the primary's roles equal the golden | `TestGrantedRoleReadClamp` (`deploy_edges_test.go`) | unit | M2 | P3 P11 T4 |
| `block` refuses every edge kind (slot, grant, bus subscription, net) with the policy named; streams and lan-ingress default to block; the net edge follows [09-fabric.md](09-fabric.md) | `TestEdgePolicyBlock`, `TestUnclampableEdgeDefaults`, `TestNetEdgeDefault` | unit | M2 | P3 T4 |
| The edge policy never changes a primary call (role, target, headers) | `TestEdgePolicyNeverTouchesPrimary` | unit | M2 | P3 P5 |
| Own-scope bus publishes land in the deployment's namespace; publishes to other scopes need `writer`, which the clamp removes | `TestBusPublishStaysInNamespace` | unit | M2 | P13 T4 |
| Dormant registrations: a non-primary cron job, bus subscription, interface instance or ingress host registers with 200 into the per-deployment file; `data/cron-jobs.json`, `data/bus-subscriptions.json` and the root `xbin.json` maps stay byte-identical; it never fires or routes | `TestDormantRegistrations` (`deploy_dormant_test.go`) | unit | M2 | P13 T6 |
| Deliveries on: that deployment's cron and bus deliveries reach it; ingress and interface instances stay inactive; run now delivers exactly once | `TestDeliveriesSwitch`, `TestRunNow` | unit | M2 | P13 T6 |
| Reassigning the primary is atomic: routing moves, data doesn't, the target must be healthy, and the old primary's registrations go dormant while the new one's activate | `TestReassignPrimaryAtomic` | unit | M2 | P7 |
| Notifications from a non-primary deployment are recorded as "would notify", with no push and none of the tile's budget spent | `TestNotifyNotPushed` | unit | M2 | P13 T6 |
| Self-calls: the tile's principal carrying deployment X (an instance, frame or terminal token) calling `/api/<self>/` routes to X with self-admin there; calling another deployment of its own tile is refused | `TestSelfCallRouting` | unit | M2 | P12 T3 |
| Promote moves code only: B's data, vault, dormant registrations and routing are unchanged; late-bound values are resolved for B | `TestPromoteMovesCodeOnly` | unit | M2 | P10 |
| A new deployment's vault holds the primary's key names as placeholders; copying values is manager-only; the D30 backend-only read rule holds per deployment | `TestVaultPerDeployment`, `TestVaultCopyManagerOnly` (`deploy_seed_test.go`) | unit | M2 | P14 T5 |
| Seeding is manager-only and needs the explicit PII confirmation; it stops the target, copies consistently in confine without following links, re-keys encrypted stores, never writes the primary, and records `from`/`at`/`by`. Seeding a shared (scope, name) namespace needs the actor's gate on every sibling tile and stops every sibling's same-named deployment (NP-02-10) | `TestSeedManagerOnly`, `TestSeedNeverWritesPrimary`, `TestSeedCopyConfinedNoFollow`, `TestSharedScopeSeedNeedsEveryTile` | unit | M2 | P14 T8 |
| Reset empties only its target; removing a deployment deletes its namespace, vault, logs, dormant registrations and artifacts, never main's | `TestResetAndRemoveScope` | unit | M2 | P14 |
| Leftovers: after a tile's removal, the record, the checkpoint store and non-main namespaces block a non-owner from creating that path (`newTilePathOK` / `pathLeftovers`, `internal/broker/policy.go:156`, `:218`); the path's owner is exempt | `TestPathLeftoversIncludeDeploymentState` (`deploy_life_test.go`) | unit | M2 | T11, §11 |
| `+` in new tile names: non-admins are refused; exact matches on existing directories keep resolving (the `TestNewTilePathRule` pattern, `internal/broker/policy_test.go:427`) | `TestPlusReservedInNewTilePaths` | unit | M2 | P17 |
| Chrome and `xbin`-capable tiles may pause live reload but can't add a non-primary deployment | `TestP19RefusesChromeAndXbinTiles` | unit | M2 | P19 T14 |
| Caps: per-tile and per-workspace non-primary deployment caps | `TestDeploymentCountCaps` | unit | M2 | T10 |
| Transfer (D39) moves deployments with the tile and keeps edge policies; clone, template instantiation and imports start in the zero state; disabling stops every deployment and enabling starts the primary | `TestDeploymentsAcrossTileLife` | unit | M2 | §11 |
| Backups always include the record, the store and main's data; non-primary data is opt-in under its own archive key | `TestBackupCoversDeployments` | unit | M2 | §11 |
| Builtin updates (D49, every mode) and applied PRs (D48) land in the work tree, so they reach only the live reload target | `TestWorkTreeWritersReachOnlyLiveTarget` | unit | M2 | T16 |
| The authority matrix below, one subtest per cell | `TestDeployAuthzMatrix` (`deploy_authz_test.go`) | unit | M1/M2 | P4 P11 P21 T9 |
| View-as refuses every operation with the read-only error (the `internal/server/termsessions_test.go:72-75` shape) | `TestViewAsRefusedEveryOp` | unit | M1/M2 | T9 |
| Protected primary: protecting detaches live reload from it (pinned in place); resume or attach onto it is refused for everyone; only managers change its code | `TestProtectedPrimary` | unit | M2 | P21 T16 |

**The authority matrix** (05-model §10). A ✓ means 2xx, and ✗ means 403
with a reason naming who may act and a stable error code
([11-contract.md](11-contract.md) names it; SC-PROTECT). The NoTerm,
Backend and Tokens columns are 06-security.md's `TestNoTerminalCannotOperate`
and `TestOwnRuntimePrincipalsCannotOperate`.
- *Managers* are the owner, workspace admins, and admins of the owning org or
  the user-owner (D24/D33).
- *Term* is a terminal-level user who isn't a manager.
- *NoTerm* is a `noTerminal` account holding terminal level, which is capped
  at `write` (D88).
- *Tokens* are the tile's terminal and agent tokens.
- *Backend* is the tile's own instance token.
- *Other* is another tile's element principal.

| Act | Managers | Term | Write | Read | NoTerm | Tokens | Backend | Other | View-as |
|---|---|---|---|---|---|---|---|---|---|
| Open or call a non-primary deployment | ✓ | ✓ | ✓ | ✗ | ✓ | only their target | only its own deployment | ✗ | read-only GET as the viewed user |
| Pause or resume live reload, reload now; attach live reload to a non-primary deployment | ✓ | ✓ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ |
| Add or remove a non-primary deployment; deploy, promote or roll back onto one; reset its data; set a session target | ✓ | ✓ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ |
| Deploy, promote, roll back, reload now or resume onto an unprotected primary (parity, P4) | ✓ | ✓ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ |
| The same onto a protected primary | ✓ (not resume or attach, P21) | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Seed, vault copy, deliveries, alwaysOn, edge policy, reassign, protect or unprotect | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |

### 3.8 Obs and the deployments plane

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| Status is per deployment: a non-primary report or clear never touches main's status, and a non-primary `build-start` doesn't clear main's (today any `build-start` clears the component's status, `internal/obs/status.go:129-143`); built on `testPlane` (`internal/obs/status_test.go:89-96`) | `TestStatusPerDeployment` (`internal/obs/deploy_test.go`) | unit | M2 | P13 |
| `/logs?deployment=` reads that deployment's log; the default and the gate are unchanged | `TestLogsPerDeployment` | unit | M2 | P13 |
| Every 05-model §5 operation runs on a literal of the plane's inputs (store, runner facade, hub, authority callbacks), with no broker (the D63 pattern, `internal/obs/obs.go:18-33`) | `TestDeployPlaneOperations` (`internal/deploy/plane_test.go`) | unit | M1/M2 | §5 |
| Random operation sequences keep the record invariants (05-model §4): `liveReload` is `""` or exactly one deployment with a null checkpoint; the primary exists; `main` exists and can't be removed; names are immutable | `TestRecordInvariantsRandomized` | unit | M1/M2 | P8 P15 |
| Every operation (pause and resume live reload, reload now, deploy, promote, roll back, add, remove, seed, reset, reassign, protect) leaves the tile directory, `.git` included, byte-identical by hash | `TestOperationsNeverWriteWorkTree` | unit-git | M1/M2 | SC-WORKTREE |
| An operation × principal matrix: every change to what a deployment runs has one deploy-log entry (who, the session kind for tokens, when, feed, checkpoint, how); entries survive a restart and are in component backups | `TestDeployLogAudit` | unit-git | M1 | SC-AUDIT, P1 |
| Pausing then resuming live reload on a `main`-only tile removes the record. Env, events, URLs, the terminal env and `/components` then equal a never-opted-in twin's | `TestOptOutReturnsToZeroState` | unit | M1 | P5 |

### 3.9 Term

`internal/term/term.go` is at 948/950 lines, so the code goes in new files.
The tests live in `internal/term/deploy_test.go`.

| Property | Test | Layer | M | Covers |
|---|---|---|---|---|
| The default target is the live reload target, or the primary while live reload is paused; `XBIN_DEPLOYMENT` is set only when the target isn't the primary | `TestSessionTarget` | unit | M2 | P12 P17 |
| Choosing a target needs terminal level; view-as and `noTerminal` are refused (extends the `TestServeWSGates` matrix, `internal/term/gates_test.go:16`) | `TestTargetGates` | unit | M2 | T9 |
| No session can target a protected primary; protecting one moves or ends sessions that target it, as [11-contract.md](11-contract.md) specifies | `TestTargetProtectedPrimary` | unit | M2 | P21 T16 |
| Sessions on a tile with a record get the read-only checkpoint remote, only through injected `GIT_CONFIG_*` entries and never written into the tile's `.git/config` (NP-02-7); zero-state tiles, and sessions without an API token or URL, don't get it (the injection needs both, `internal/term/term.go:787`) | `TestFetchRemoteInjection` | unit | M1 | P5 P16, SC-WORKTREE |
| Admin and restricted terminals mask `data/` and `.xbin/` (`internal/term/binds.go:43-53`), which covers `data/deployments`, `data/checkpoints` and `.xbin/deploy` | `TestTerminalMasksDeploymentState` | unit | M1 | P15 T11 |

`internal/term`'s `TestMain` builds `./cmd/bx`
(`internal/term/agent_test.go:30-49`), so a WP that breaks `bx` also fails
this package.

### 3.10 `bx` and the SDK

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| Argument grammar per [11-contract.md](11-contract.md), including the compat rule 6 superset (interleaved positionals, `--flag=value`, `--no-x`) | `TestParseDeploymentArgs` (`cmd/bx/deploy_test.go`, the `args_test.go` style) | unit | M1/M2 | — |
| Every invocation `bx` accepts today parses to the same request (12-compat PO-13; written in M0) | `TestBxTodayInvocationsUnchanged` | unit | M0 | P5 |
| Every command that changes where saves go (deploying onto the live reload target, pausing live reload, attaching it) says so in its output (SC-AGENT-BX) | `TestBxSaysWhereSavesGo` | unit | M1/M2 | SC-AGENT-BX |
| `XBIN_DEPLOYMENT` is the default target, and explicit flags win | `TestBxHonoursXBINDeployment` | unit | M2 | P12 |
| Against an older xbind: a 404 gives a clear message and a distinct exit code | `TestBxOldXbind` (httptest) | unit | M1 | 12-compat |
| `--json` output equals the wire shapes | `TestBxJSON` | unit | M1 | — |
| A protected-primary refusal names the managers (flow C) | `TestBxRefusalMessages` | unit | M2 | P21 |
| `xbin.Deployment()` returns the env value, or `""` for main; `CallerInfo.Deployment` is read from `X-XBin-Deployment`; `sdk/go.mod` gains no `require` (compat rule 8) | `TestDeploymentFromEnv`, `TestCallerDeployment` (`sdk/deployment_test.go`) | unit | M2 | P17 |

### 3.11 The shared sandbox layer

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| `Filter` gains a deployment field (`internal/sbx/sbx.go:77-85`); an entry without a deployment marshals exactly as today (no key) | `TestFilterDeployment`, `TestEntryJSONZeroState` (`internal/sbx/deploy_test.go`) | unit | M2 | P5 P13 |
| The per-tile cgroup parent: it never holds a process (cgroup v2); it has one leaf per deployment, plus D113's `sbx-*` leaves later; its caps bound the sum; removing one leaf never removes another; a boot sweep drops stale leaves and keeps live ones. It runs against an injectable base dir, like `TestWriteLimits` (`internal/cgroup/cgroup_linux_test.go:9`) | `TestNonPrimaryCgroupSubtree` (`internal/cgroup/parent_linux_test.go`) | unit | M2 | T10 |
| VM books: every deployment's reservation is charged to the tile, so `UsedBy()[tile]` sums them and no `<tile>+<name>` owner ever appears (beside `TestReserveAttribution`, `internal/vm/health_test.go:16`) | `TestReserveChargesTileForEveryDeployment` (`internal/vm/reserve_deploy_test.go`) | unit | M2 | T10 |
| The primary's leaf keeps today's per-component caps (`internal/boot/boot.go:565-572`) whatever its non-primary siblings use; a sibling that exhausts its own leaf's memory never lowers them (NP-02-12) | `TestPrimaryLeafKeepsCaps` | unit | M2 | SC-PRIMARY-FIRST T10 |
| When the VM budget can't admit a primary start (`internal/vm/policy.go:132-141`), xbind first stops the same tile's non-primary guests; a non-primary start is refused rather than preempting a primary, and is recorded as `refused` in the D112 failure ring (NP-02-12) | `TestNonPrimaryVMCannotStarvePrimary` | unit | M2 | SC-PRIMARY-FIRST T10 |
| `/api/xbin/sandboxes?tile=` lists every deployment's generations under the tile, each non-primary row naming its deployment; main rows are unchanged; `health.vm.usedBy` is keyed by tile; non-admins get nothing (`internal/boot/sandboxes.go:31-36`) | `TestSandboxesDeploymentRows` (`internal/boot/sandboxes_test.go`, new: the endpoint has no unit test today) | unit | M2 | P13 T7 |
| Direct mode (isolation off): a `confine.Cmd` whose bind destination differs from its host dir fails closed, and never runs in the work tree (`confine.Cmd` binds `Dir` at itself today, `internal/confine/confine.go:71-84`) | `TestDirectModeRefusesRebind` (`internal/confine/bind_test.go`) | unit | M1 | P18 T12 |
| After D113 is built: a non-primary backend's instance token sees only its deployment's sandbox set; `{"source":true}` mounts its code; resource mounts resolve in its namespace; `cap:sandboxes` and the per-tile caps stay the tile's | `TestTileSandboxesPerDeployment` | unit + isolated | after D113 is built | P11 T3 |

### 3.12 Web modules and the shipped-app mirrors

| Property | Test (file) | Layer | M | Covers |
|---|---|---|---|---|
| The terminal window's pure state module (13-surfaces names it; `web/deploy-state.js` here) maps (state, permission flags) to a view model. Zero state gives at most one entry point. Live reload paused gives the chip and a pending count. Controls the viewer may not use are disabled with a reason. A protected primary gives the manager reason. View-as gives the read-only reason. Strings use the glossary spellings | `hack/deploy-state.test.mjs` | js-test | M1/M2 | P2 P5 P21 |
| `isReloadTarget` (`web/events-socket.js:40-49`) never picks a primary frame for a non-primary event, including an **ancestor** tile of a nested tile (Divergences 1) | `hack/events-socket.test.mjs` (the module imports nothing) | js-test | M2 | P13 |
| The tile menu: new lines or squares from [10-ux.md](10-ux.md), asserted by label, not position (`hack/menus.test.mjs:110`, `:135` index today) | `hack/menus.test.mjs` | js-test | M2 | — |
| The shipped app's `ReloadTargets.covers` (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:161-166`) as it ships: `apps/x+dev` isn't covered by `apps/x`, but `apps/a/b+dev` **is** covered by `apps/a`. This pins why the server must never send non-primary activity as `reload` | new rows in `ClientEventsTests.swift` (beside `:91-101`) | swift | M2 | P13 |

### 3.13 Guards

- **`TestDeploymentWording`** (`internal/docscheck/wording_test.go`, M1,
  NP-15-9). It scans the new web modules, `cmd/bx` deployment files and the
  builder-doc sections for the glossary's banned words in user-visible
  strings (P2).
- **`TestNoDirectExec`** (`internal/confine/guard_test.go:22`). Its pattern
  (`:14`) gains `cgi\.Handler\{`, with the usual `exec-ok:` escape
  (NP-15-7, T13).
- **`TestIntegrationPackagesListed`** (beside `TestNoDirectExec`, M0,
  NP-15-6). It fails when a package has `integration`-tagged tests but is
  missing from the Makefile's `integration` target.
- **`TestEnsureCallSitesPassPrimary`** (beside `TestNoDirectExec`, M2). The
  static inventory guard of SC-INBOUND: every `Runner.Ensure` call site
  (§2.5 row 33) passes the primary unless it carries an annotation saying
  why.
- **Existing guards every WP keeps green:**
  - `TestRouteInventory` (`internal/apicheck/apicheck_test.go:244`): one
    route means a `RegisterAPI` literal, an openapi row and a protocol.md
    row, in one commit.
  - `TestDecisionIDsResolve` and `TestRelativeLinksResolve`
    (`internal/docscheck/docscheck_test.go:71`, `:114`): WPs cite no new
    D-numbers.
  - The size budget: Go `_test.go` files are exempt, but `hack/*.test.mjs`
    and harness passes count against the 900-line cap.

## 4. Confine and sandbox integration (needs a rootfs)

**Prerequisites.**
- User namespaces, and a rootfs with `/usr/bin/git`:
  - `XBIN_TEST_ROOTFS`, or `../../.rootfs` from `make rootfs`;
  - skip rule and message: `internal/confine/sandbox_linux_test.go:26-35`.
- The Go build tests also need a host `go`
  (`internal/runner/build_linux_test.go:33-42`).
- Each package's `TestMain` doubles as the sandbox's re-exec init
  (`internal/confine/sandbox_linux_test.go:19-24`).
- **CI builds no rootfs today, so all of these skip there** (§10.1).

| Test (file) | Asserts | M | Covers |
|---|---|---|---|
| `TestCaptureIgnoresTileGitConfig` (`internal/checkpoint/hostile_linux_test.go`) | The work tree's `.git/config` sets `core.fsmonitor`, `diff.external`, a `filter.x.clean`/`smudge` driver, `core.hooksPath` with hooks, and an `ext::` remote; `.git/objects/info/alternates` points at a host path; `.gitattributes` names `filter=x` and `diff=x`; `.git` is also tried as a `gitdir:` file pointing into `.xbin`. Checkpointing succeeds, a host marker is never written, and nothing of the tile's `.git` is read (the store uses a private `GIT_DIR` and the tile is bound read-only) | M1 | T1 P16 |
| `TestCheckpointStorePrivate` | The store has no hooks and a private config; `gc`/`repack` run confined; a hostile tile can't write the store through any bind | M1 | T1 T11 |
| `TestMaterializeSymlinkEscape` | A checkpoint of a tree with escaping symlinks, symlink chains, a directory symlink, a FIFO, a socket and a 0-byte device node materializes read-only, with no special files and no link followed on the host; a crash mid-way leaves nothing visible | M1 | T2 P16 |
| `TestConfinedBuildOfCheckpointAtCanonicalPath` (`internal/runner/build_checkpoint_linux_test.go`) | The `TestConfinedGoBuild` pattern (`internal/runner/build_linux_test.go:32`). The work tree's `main.go` prints `worktree` and the checkpoint's prints `checkpoint`; the build binds the materialized checkpoint at the tile's canonical path through the new confine bind destination; the binary prints `checkpoint`; the root `go.work`'s `use ./apps/x` resolves to it; the hostile `fsmonitor` marker is never written; caches stay under `.xbin/cache/tile/<CompKey>/`; the artifact lands at the per-checkpoint path | M1 | P9 P16 T1 |
| `TestPinnedBackendSeesCheckpoint` (`internal/runner/launch_checkpoint_linux_test.go`) | A tiny static backend reads a file at its canonical path and lists its `XBIN_RES_*` dirs. Pinned, it sees the checkpoint and not the work tree; a write to the canonical path fails; a non-primary deployment's data dir is mounted at the primary's path and the host dir behind it is the deployment's | M1/M2 | P6 P9 P14 |
| `TestRebindDirInSandbox` (`internal/confine/rebind_linux_test.go`) | The new bind destination: `Src ≠ Dst`, read-only, and the `.xbin`/`data`/`homes` masks still apply beneath it | M1 | P16 |
| `TestCheckpointFetchRemoteFlowH` (`internal/checkpoint/fetch_linux_test.go`) | Flow H literally: a confined git client with the terminal's `GIT_CONFIG_COUNT` rewrite runs `git fetch xbin-deploy`, then `git checkout -b hotfix deploy/main`, and gets main's checkpoint tree. A push fails (no receive-pack; the handler is GET-only); a principal without read on the tile is refused | M1 | P16 T15, flow H |
| `TestBindSrcNotDst` (`internal/sandbox/sandbox_linux_test.go` neighbour) | `sandbox.Bind` with `Src ≠ Dst` (`internal/sandbox/sandbox.go:109-114`): content, read-only, and order under `sortBinds` | M1 | P9 |
| `TestRestoreRebuildsStoreConfig`, `TestSeedSqliteConfined` (06-security.md T11, T20) | A restore rebuilds the store from its objects in confine and never restores `config`, `hooks/`, `info/` or `objects/info/alternates`; a sqlite seed copies through the backup API inside confine | M2 | T11 T20 |
| `TestTrackedBranchReadsGitOnlyInConfine`, `TestDeployRemoteHostilePack` | M3: the branch is read inside confine (the tile's `.git` is untrusted); receive-pack runs in the streaming confine (D113's exec protocol as precedent), with the ref namespace restricted, objects quarantined and fsck'd, and size caps | M3 | T15 P1 |

**The Makefile `integration` target** (`Makefile:110-117`) gains:
- `./internal/checkpoint/`;
- `./internal/sandbox/`, whose tests exist today and never run (research
  side finding 24). The `Src ≠ Dst` bind now carries weight.

## 5. Integration tests (`test/`)

### 5.1 Daemons

| Daemon | How | Used for |
|---|---|---|
| **shared** | the suite's `TestMain`: `--no-auth`, not isolated (`test/integration_test.go:56-109`, the command line at `:85`) | static tiles, zero state, latency, and the refusals without isolation |
| **auth-on** | its own daemon from `xbindBin`, the `TestMultiUser` way (`test/integration_test.go:487`): `--insecure-vault`, the root token from `.xbin/token`, users through the API | user levels, managers, and URL gates for static tiles |
| **isolated** (new, NP-15-2) | `startIsolatedDaemon(t, opts)` in `test/isolated_test.go`: `--isolate --rootfs <XBIN_TEST_ROOTFS>`, plus `XBIN_FUSE_OVERLAYFS` / `XBIN_GOCRYPTFS` from `<repo>/bin` when present; an optional auth-on flavour and `--ingress-listen`; `d.restart(t)`; process-group kill through `startDaemon` (`test/integration_test.go:38`). It skips without user namespaces or a git-bearing rootfs | every pinned or non-primary **backend** (P18), data binds, and restart paths |

Pinned and non-primary backends need isolation (P18). So the shared daemon
can only prove the refusal for backend tiles, and every backend property
lives on the isolated daemon.

### 5.2 The probe backend

`test/probe_test.go` provides `writeProbe(t, ws, tile, marker)`. It writes an
inline Go backend whose `go.mod` requires the sdk, in the
`test/bussubs_test.go:13-40` style. `examples/` is never edited. It serves:
- `GET /v`: the marker compiled in (which code runs);
- `GET /file`: a file read at its canonical path (which tree is bound);
- `GET /env`: `XBIN_RES_*`, `XBIN_DEPLOYMENT` and `XBIN_IFACE_*_URL`;
- `PUT`/`GET /kv/{k}`: through the SDK;
- `POST /tick` and `POST /on`: record cron and bus deliveries with the
  caller;
- `GET /seen`: the deliveries recorded;
- `GET /caller`: `xbin.Caller(r)`, including the deployment;
- `GET /self`: calls `/api/$XBIN_COMPONENT/v` and
  `/api/$XBIN_COMPONENT+main/v`, and returns both statuses.

### 5.3 Tests

| File | Test | Daemon | Asserts | M | Covers |
|---|---|---|---|---|---|
| `test/latency_test.go` | `TestLatencyStaticSaveToReload`, `TestLatencyGoSaveToServing` | shared | §8 | M0 | SC-LATENCY-DEFAULT |
| | `TestLatencyLifecycleOps` | shared (static), isolated (backends) | §8 | M1/M2 | SC-LATENCY-OPS |
| `test/livereload_test.go` | `TestZeroStateLifecycleFiles` | shared | Create, save, swap and restart a zero-state Go tile: no deployment files appear, and the `data/*.json` stores and root `xbin.json` are byte-identical | M0 | P5 P15 |
| | `TestLiveReloadPauseStatic` | shared | Flow A on a static tile. Pausing live reload → an edit changes neither `/c/<tile>/` nor the event stream. Reload now → exactly one `reload`, new content, still paused. Resume → the record is gone and saves reload again | M1 | P5 P8 P9 P18, SC-FAIL-CLOSED (static half) |
| | `TestLiveReloadPauseRefusedWithoutIsolation` | shared | On the counter example, pausing live reload is refused with a reason naming `--isolate`, before any state changes (no record, no store); a later save still swaps | M1 | P18 T12, SC-FAIL-CLOSED |
| | `TestLiveReloadPauseRace` | shared (static), isolated (go, node, python) | Saves run continuously across the request that pauses live reload. The checkpoint holds every save that completed before the request; afterwards no save changes served bytes or answers, including after a reap (seam), a crash, a grant change and an xbind restart. 10 runs per runtime by default, 100 with `XBIN_TEST_FULL=1` (milestone exit) | M1 | SC-LIVE-RELOAD-PAUSE, P9 |
| `test/deploy_static_test.go` | `TestStaticDeploymentURLAndData` | auth-on | A static `dev` deployment: `/c/<tile>+dev/` for a write user 200, a read user 403; kv through `dev`'s frame token is invisible to main's and the reverse; bare URLs serve the primary | M2 | P6 P7 P14 P17 P20 |
| `test/pinned_test.go` | `TestLiveReloadPauseGo` | isolated | Flow A on a probe: pause live reload, edit to v2 → `/v` still v1; reload now → v2; resume → it follows saves | M1 | P9, flow A |
| | `TestPinnedThroughRestartPaths` | isolated | §5.4 | M1 | P9 T11 |
| `test/deployments_test.go` | `TestDataSeparation` | isolated | A `dev` probe exercises every resource kind (kv, sqlite, blob, filesystem, bus, cron) through every addressing path (`XBIN_RES_*`, canonical `res:` ids, hand-built `res:<scope>/…` ids) and reads every vault key; the primary's data and vault are hashed before and after. Before a seed, `dev` sees empty namespaces and placeholder keys; `/env` values are equal in both; after a seed (as a manager), `dev` sees the copy and the primary is still unchanged by `dev`'s writes; reset → empty. File-backed kinds need gocryptfs and skip without it | M2 | P6 P14 T8, SC-DATA |
| | `TestPromoteAndRollBack` | isolated | Flows B and D: promote `dev` → main moves code (`/v`), while main's kv and vault are unchanged and the tile directory's hash is unchanged; the deploy log has both entries; roll back → the previous code, data untouched | M2 | P10, SC-WORKTREE, SC-AUDIT |
| | `TestRollBackBound` | isolated | On R-go and R-static (02-goals §5), roll back to each of the previous three deploy-log entries, with the daemon restarted without `go` on its `PATH` and the work tree replaced by a broken version. Bounds: 2 s p95 and 10 s worst for backends, 1 s p95 for static; the data hash is unchanged; live reload pauses if the deployment was its target | M1 (`main`)/M2 | SC-ROLLBACK, P9 |
| | `TestFailedDeployInvisible` | isolated | Fault injection per runtime (a broken build, a crash at start, a health timeout) for deploy, promote, roll back and reload now, with an events subscriber throughout: the previous code serves every request; no bare `build-start`, `build-error` or `reload` for the tile; the status is unchanged; the failure and compiler output reach the actor. The recorded tape is replayed through the app's parser in the Swift rows of §3.12 | M1 | SC-SAFE-DEPLOY (NP-02-4) |
| | `TestRegistrationsAtStartStayDormant` | isolated | A probe that registers a cron job and a bus subscription at start runs on main and on `dev`: `dev` gets no deliveries while its deliveries are off; main's registration stores are byte-identical after `dev` starts; no notification is pushed | M2 | SC-DORMANT, P13 |
| | `TestSelfCallsStayInDeployment` | isolated | A `dev` probe's `/self` returns 200 for its own `/api/<self>/` (served by `dev`) and 403 for `+main` | M2 | P12 T3 |
| | `TestMultiTileScope` | isolated | Flow G end to end | M2 | P6 P3 |
| `test/edges_test.go` | `TestInboundEdgesReachOnlyPrimary` | isolated + ingress | §5.5 | M2 | P7 P13 T6, SC-INBOUND |
| | `TestOutboundEdgePolicy` | isolated | A `dev` consumer attempts a write over each edge kind (interface binding, grant call, cross-scope `res:`, bus subscription, stream, lan-ingress, the net slot): no write succeeds; the provider's primary sees `reader` and `X-XBin-Deployment: dev`; `block` fails closed naming the policy; the primary's calls carry `writer` and no header | M2 | P3 P11 T4, SC-CLAMP |
| | `TestReassignPrimaryFlowF` | isolated | Flow F: every inbound edge moves to `dev` and `dev`'s data; main stays pinned with dormant registrations; reassigning back restores it | M2 | P7 |
| `test/flowc_test.go` | `TestAgentFlowCWithBxOnly` | isolated, auth-on | SC-AGENT-BX's seven steps with the real `bx`, run **inside an isolated terminal session** over `/ws/term` (target `dev`), so `.xbin` is masked as in any real session (`internal/term/binds.go:50`) and `bx logs` must go through `GET /logs` rather than the file (`cmd/bx/main.go:511`): read the state; see `dev`'s build and logs; call `dev`'s API with curl; run now; see the diff against main's checkpoint; promote `dev → main` and roll back; then, with the primary protected by a manager, get a refusal with a stable error code and nothing changed | M2 | P4 P21, SC-AGENT-BX, SC-PROTECT |
| | `TestAgentBxLiveReload` | isolated | The M1 variant of SC-AGENT-BX in the same session: pause live reload, reload now, `bx status`, `bx logs`, roll back, resume | M1 | SC-AGENT-BX |
| | `TestPrimaryFirstUnderPressure` | isolated | A `dev` backend exhausts its leaf's memory while main keeps serving within today's caps; with VMs available, a primary crash while the tile's `dev` guests fill the budget restarts main and refuses `dev`. It skips without delegated cgroups or VM support | M2 | SC-PRIMARY-FIRST |
| `test/deploystate_boot_test.go` | `TestDeploymentStateBootsTwice` | own | §6 | M1 | P15 T11 |
| `test/downgrade_test.go` | `TestDowngradeStatic`, `TestDowngradeDormantRegistrations` (together, the `TestOlderBinaryIgnoresDeploymentFiles` of 06-security.md and 12-compat.md) | own (`XBIN_DOWNGRADE_BIN`) | §5.6 | M2 | T11, SC-DORMANT, 12-compat |

### 5.4 Every restart path runs pinned code

`TestPinnedThroughRestartPaths` (SC-PINNED) pins a probe at v1 with the work
tree at v3. After each trigger, `/v` must answer v1, and the daemon must
have started the retained artifact instead of building from the work tree.

| Path | Trigger in the test | Where else |
|---|---|---|
| Crash restart | `kill -9` the PID from `GET /api/xbin/sandboxes?tile=` (D112 lists it) | seam row 17 |
| Crash-loop recovery | crash it `crashLimit` times: once the breaker trips, a save doesn't restart it (saves don't reach a pinned deployment); a deploy or roll back does, onto a checkpoint | seam rows 7, 17 |
| Idle reap | **not end to end**: `idleReap` is a 30-minute constant (`internal/runner/runner.go:46`), and no test knob is added (principle 6) | seam row 18 |
| Grant change | Add a `uses` for a resource in the work tree, then approve with `POST /grants`, which calls `OnGrantChange` | seam row 19 |
| xbind restart | `d.restart(t)` | seam row 20 |
| Loss of `.xbin` | stop; remove `.xbin/build` and `.xbin/deploy`; start | seam row 21 |
| Lifecycle | `POST /lifecycle` disabled, then enabled | seam row 12 |
| Vault seal/unseal | not on a plaintext vault | seam row 22 |
| Provider nudge | needs a net-provider tile | seam row 19 (the same `Changed` path, `internal/runner/runner.go:593`) |
| alwaysOn backoff | an alwaysOn probe crash-restarts through `afterExit` | seam row 35 |

### 5.5 Inbound edges

The inbound edges are inventoried in
[research/inbound-edges.md](research/inbound-edges.md) (E1–E16). Each row
names its unit test and, where it is feasible, the
`TestInboundEdgesReachOnlyPrimary` step.

| Edge | Unit test | Integration step |
|---|---|---|
| Bare `/c/`, `/api/` (E1, E15) | `TestQualifiedURLRouting`, seam row 33 | the bare URL answers main's marker |
| Cron (E2) | `TestDormantRegistrations` | main's job ticks main; `dev`'s job never fires; run now fires it once to `dev` |
| Bus push and triggers (E4, E5) | `TestDormantRegistrations`, `TestDeliveriesSwitch` | a publish delivers to main only; with deliveries on, `dev`'s own subscription delivers to `dev` |
| alwaysOn (E6) | seam row 35 | an alwaysOn manifest in `dev`'s checkpoint doesn't start `dev` at boot |
| Ingress HTTP and terminators (E7) | `TestIngressForwardPrimaryOnly` | a Host-routed request on `--ingress-listen` answers main's marker |
| L4 streams, hairpin, stream interfaces (E8, E9) | seam row 33 (`DialInto` → primary) | — |
| lan-ingress and net-provider splices (E10) | `TestUnclampableEdgeDefaults` | — |
| Interface bindings and grants (E11) | `TestEnvForPerDeployment` | a consumer's call through its slot URL reaches main |
| Archiver and backups (E3) | `TestBackupCoversDeployments` | — |
| Notify links (E12) | `TestNotifyNotPushed` | — |
| The native document (E15) | `TestNativeDocumentFollowsPrimary` | `?native=1` is built from main's code |

### 5.6 Downgrade simulation

**The binary.** `XBIN_DOWNGRADE_BIN` points at the previous release's xbind.
CI downloads it from the release assets (NP-15-4); locally, build it from
the tag. Without it, the test skips.

**What each test does:**
1. With the new binary, create the state:
   - static tile A: live reload paused, main pinned to c1, work tree at
     v2;
   - (`TestDowngradeDormantRegistrations`, isolated) probe tile B: a `dev`
     deployment whose cron job `/dev-tick` is dormant, plus a main job
     `/tick`.
2. Stop, then start the old binary on the same workspace.
3. Assert what [12-compat.md](12-compat.md) promises:
   - it boots;
   - `/c/A/` serves v2, because an old binary serves the work tree;
   - B's `/seen` never shows `/dev-tick` across two main ticks;
   - the root `xbin.json` and `data/cron-jobs.json` are byte-identical;
   - `data/deployments/` and `data/checkpoints/` are untouched.
4. Stop, then start the new binary: A serves c1 again, and B's `dev` job is
   still dormant.

## 6. The legacy fixture

- **Unchanged.**
  - The design adds no boot-time migration. So `TestLegacyWorkspaceBootsTwice`
    (`test/legacy_workspace_test.go:31`) and its in-process twin
    `TestLegacyWorkspaceBootsTwiceInProcess`
    (`internal/boot/boot_test.go:118`) keep their allowed lists exactly
    (`test/legacy_workspace_test.go:74-83`,
    `internal/boot/boot_test.go:166-174`).
  - The root `xbin.json` stays byte-identical (`:70`).
  - A WP that needs to extend either list has found a design problem.
    Escalate it; don't extend the list.
- **The variant with deployment state:**
  - `TestDeploymentStateBootsTwice` (a new file,
    `test/deploystate_boot_test.go`) and `TestDeploymentStateBootsTwiceInProcess`.
  - **Setup.** Create the state with the feature itself on a fresh
    workspace:
    - pause live reload on a static tile;
    - add a static `dev` deployment;
    - set an edge policy;
    - register a dormant job with a credential of `dev`;
    - stop.
  - **Assertions.**
    - Boot 1 and boot 2 change nothing outside derived trees. Materializations
      under `.xbin/deploy/` may be rebuilt and are post-filtered, like the
      fixture's own skip list (`test/legacy_workspace_test.go:182`).
    - The record, the checkpoint store and the per-deployment files are
      byte-identical.
    - Tile work trees are unchanged.
    - **Tile repositories are compared explicitly**: refs, `HEAD` and
      `config` of each tile's `.git`. The fixture's `/.git/` wildcard
      (`test/legacy_workspace_test.go:82`) would otherwise hide a rewrite.
  - It reuses `snapshot`, `changed` and `bootOnce` from the same package, so
    the serialized fixture file is not edited.

## 7. UI harness passes

### 7.1 Fixtures and rules

- **Seed** (one `seed.sh` change, owned by the contracts WP):
  - `apps/reloady`: static, owned by `org:devs`, so `dev1` has terminal
    level as a devs admin;
  - `apps/deployy`: static, owned by `org:sales`, with a `uses` edge on
    `apps/leads`;
  - in the user entries (`hack/ui-harness/seed.sh:33-41`): `dev1` gets
    `"apps/deployy":"terminal"`, so dev1 is a terminal-level non-manager
    there, and `infra1` gets `"apps/deployy":"read"`. `sales1` is the
    manager.
  - The names follow the `focusy`/`linky` convention.
- **Isolation per agent.**
  - Every agent runs with its own `PORT` and `HARNESS_DIR`
    (`hack/ui-harness/run.sh:22`, `:25`). The derived fakeopenai and
    ingress ports follow `PORT`.
  - `stop()` kills by port and by workspace path (`run.sh:53-64`), so a
    shared port kills another agent's instance.
  - `PLAYWRIGHT_DIR`, `XBIN_GOCRYPTFS` and `XBIN_TEST_ROOTFS` come from
    the main checkout ([14-implementation.md](14-implementation.md)).
- **Driving** (the rules in [/docs/maintenance.md](/docs/maintenance.md),
  "UI harness").
  - Passes click real controls and read state through `testApi()`. A
    `._x` member access fails `make js-check` (`Makefile:190-192`).
  - Dialogs are answered with `answerDialog`.
  - Routed fixtures (`page.route`) stand in for states the harness can't
    produce, as `passes/sandboxes.js` part B and `passes/vmtoggle.js` do.
- **SKIP rules.**
  - The harness xbind runs without `--isolate` (`run.sh:74-75`). Backend
    halves therefore assert the refusal and print
    `SKIP needs HARNESS_ISOLATE` for the rest (NP-15-3).
  - File-resource seeding skips on `HARNESS_NO_GOCRYPTFS` (`run.sh:86-92`).
  - A pass never times out and never passes silently.
- **Restoration.**
  - Every pass ends with its fixture tiles back in the zero state, asserted
    through the deployments state endpoint.
  - It also deletes its `term:<tile>`/`termvm:<tile>` prefs, restores the
    files it wrote into `$WS`, and ends its sessions.
  - A tile left with live reload paused would silently change later passes
    such as `reloadFocus`.
- **The gate** is a fresh-seed run (`run.sh --keep livereload deployments
  agentTab sandboxes viewAs`), not `--shots` against a reused workspace,
  which restores stale windows.

### 7.2 `livereload` (`hack/ui-harness/passes/livereload.js`, M1)

0. **Zero state.** `apps/reloady` shows at most the one entry point
   [10-ux.md](10-ux.md) defines, and no frame chip, in the bar and in the
   tools row (`showPickers`).
1. **Pause live reload.** dev1 opens the terminal window and pauses live
   reload through the title-bar control. The control shows live reload
   paused, and the chip appears on
   the frame (`.frame-wrap`, binary-served). A second context, another
   reader, sees the chip after its own load, if 10-ux shows it to readers.
2. **Save.** The pass writes `$WS/apps/reloady/index.html`. The frame does
   not reload, which is a bounded negative. The pending count becomes 1
   (`waitFor`).
3. **Reload now.** The button asks through the frame dialog and is answered
   with `answerDialog`. Both contexts reload once and show the new content.
   Live reload is still paused.
4. **Resume live reload.** The chip is gone, the state endpoint reports the zero state,
   and the next save reloads the frame.
5. **Refusal.** On `apps/crawler` (node, not isolated), the control is
   disabled and its tooltip carries the server's reason. This is the
   `vmtoggle` precedent (`hack/ui-harness/passes/vmtoggle.js:1-14`). With
   `HARNESS_ISOLATE`, steps 1–4 repeat on it instead.
6. **Restore.** Put the original file back and delete the prefs.

### 7.3 `deployments` (`hack/ui-harness/passes/deployments.js`, M2)

1. **Add `dev`.** dev1 opens the Deployments layout on `apps/deployy` and
   adds `dev`, with data empty and live reload attached to it. The rows show
   main pinned to a checkpoint and `dev` following the work tree.
2. **Open its URL.** A new page `/c/apps/deployy+dev/` shows the work tree.
   An edit reloads that page, while the bare URL is unchanged. infra1 (read
   level) is refused at the `dev` URL.
3. **Promote.** "Promote dev → main" shows the diff between the
   checkpoints, confirms through the dialog, and then the bare URL shows the
   new content.
4. **Roll back.** Main rolls back from its deploy log, and the bare URL
   shows the old content.
5. **Edge policy.** In the outbound-edges table, dev1 sees the `apps/leads`
   edge control disabled with the manager reason. sales1 sets it to
   `block`, and the row shows `block` for both.
6. **Dormant list.** A routed state renders the dormant registrations
   (cron, bus, ingress), "would notify" entries, and run now. Clicking run
   now sends the documented request.
7. **Protected primary.** sales1 protects the primary. dev1's deploy and
   promote onto main are then disabled with the managers named. A direct
   `POST` as dev1 is refused with 403, and sales1's promotion succeeds.
8. **Restore.** Unprotect, remove `dev`, resume, and assert the zero state.

### 7.4 Changes to existing passes and node tests

- **`agentTab`.**
  - `hack/ui-harness/passes/agenttab.js:238` asserts exactly five
    `.lyt button`s on `apps/crawler`.
  - It changes to compare against a new `testApi().layouts` getter
    (NP-15-12), so a Deployments layout button doesn't need a new magic
    number.
  - Zero-state visibility of that button is asserted in `livereload` part 0.
- **`sandboxes`.**
  - Part B's routed fixture gains a non-primary generation. The tab must
    list it under its tile with the deployment named, and count the tile's
    memory once per leaf.
  - Part A asserts that the live `backend:apps~crawler` row keeps its ID
    shape and carries no deployment field.
- **`viewAs`.** It gains an attempt to pause live reload and an attempt to
  deploy, both refused, in its "every write is refused" list.
- **`menus.test.mjs`.** It asserts by label (§3.12). The phone sheet's grid
  has four columns (`web/bx-menu.js:145`), so a fifth square wraps. That is
  10-ux's call, and the test pins whichever layout it chooses.
- **`shots.js`.** It costs one `require` line and one `PASSES` edit (it is at
  870/877 lines, `hack/size-budget.txt:30`).

## 8. Latency-budget tests

The budgets in `plans/dev-flow.md:102-105` are described as
"regression-tested in integration suite", but no test asserts them today.
They land in **M0**, on master, **before** the pipeline changes.

| Budget | Source | Test | Measured as | M |
|---|---|---|---|---|
| Static save → frame reload < 500 ms | `plans/dev-flow.md:103` | `TestLatencyStaticSaveToReload` | the file write → `reload` on `/ws/events` (gorilla websocket, already in `go.mod`). The watcher's 300 ms debounce (`internal/boot/boot.go:41`) is inside the budget | M0 |
| Go save → new backend serving < 2 s, warm | `plans/dev-flow.md:104` | `TestLatencyGoSaveToServing` | the write → the first 200 with the new body, polled every 20 ms | M0 |
| Terminal keystroke echo < 30 ms | `plans/dev-flow.md:105` | not asserted here; this feature doesn't touch the terminal path | — | — |
| No extra confined run, filesystem walk or disk read per save batch for a zero-state tile | SC-LATENCY-DEFAULT, P8 | seam row 30, `TestWatchLoopZeroStateNoStoreIO` | deterministic call counts | M1 |
| The same save budgets while live reload is attached to a non-primary deployment | SC-LATENCY-DEFAULT, P8 | the two tests above, run again with live reload on `dev` | as above | M2 |
| Checkpoint; pause live reload; reload now and resume; deploy, promote or roll back to a kept artifact; reassign the primary | SC-LATENCY-OPS (its table of p95 targets and hard bounds per reference tile) | `TestLatencyLifecycleOps` (static: shared; backends: isolated) | the request → the first request served by the new code, with every open frame of the deployment reloaded | M1/M2 |

The measurement rules and reference tiles are 02-goals.md's:
- R-static is a copy of `builtin-templates/agent`;
- R-go is `examples/counter-go`, copied into the workspace, with a warm
  cache;
- R-node is a node backend of R-static's size;
- R-large is for stress runs only, never in CI.

**Method (two tiers, NP-15-11).**
- **Every `make integration`:** one cold sample is discarded, then five warm
  samples are taken. Every sample must meet the hard bound, and the median
  must meet the p95 target.
- **Milestone exits** (`XBIN_TEST_FULL=1`): at least 30 samples, with p95
  against the targets.
  - With `XBIN_BASELINE_BIN` set (the previous release's xbind, the same
    binary as §5.6's), the baseline is measured back to back on the same
    host.
  - p95 may regress by at most the larger of 10 % and 50 ms
    (SC-LATENCY-DEFAULT).
- Every sample is logged with its breakdown: debounce, rescan, checkpoint,
  build, start, health.
- If master already misses a budget in M0, the WP reports it to the owner
  and neither loosens the number nor adds slack.

## 9. Security tests (T1–T20 of [06-security.md](06-security.md))

06-security.md defines T1–T17, and adds three more:
- T18: the shared sandbox layer;
- T19: tile-managed sandboxes per deployment;
- T20: checkpoint content disclosure and retention.

It names a test for each mitigation. This table lists those names, plus
this plan's other tests for the threat.
- **Where the two documents have one test under two names,** the list after
  the table pairs them. The integrator keeps one name per pair.
- **Tests that only 06-security.md names** are specified there. Each is a
  unit test in the package that owns the mitigation, at that mitigation's
  milestone, unless it is marked (confined), (isolated) or (M3).

| Threat | Tests | Manual or residual |
|---|---|---|
| T1 code execution as xbind through git on tile data | `TestCaptureIgnoresTileGitConfig` (confined), `TestCaptureDirectModeHardening`, `TestCapturePathNamesNeverArgv`, `TestGitDirect`, `TestHostileRepoStaysInside` (confined), `TestNoDirectExec` · `TestCheckpointStorePrivate` (confined), `TestStoreUsesConfineOnly`, `TestConfinedBuildOfCheckpointAtCanonicalPath` (confined) | — |
| T2 poisoning the store and materialization | `TestCaptureCapsRefuseHugeTree`, `TestMaterializeSymlinkEscape` (confined), `TestPinnedServingUsesOpenBeneath`, `TestCheckpointManifestReadNoFollow`, `TestAdmissionRefusesBadTrees` · `TestCheckpointContent`; M3 `TestDeployRemoteHostilePack` | — |
| T3 cross-deployment privilege | `TestMintRefusesCrossDeployment`, `TestFrameTokenBoundToDeployment`, `TestSelfCallStaysInDeployment`, `TestTerminalTargetBinding`, `TestTerminalTokenCannotMintProtectedPrimaryToken`, `TestDeploymentURLRefusesOtherTiles`, `TestIngressNeverReachesNonPrimary`, `TestOriginLabelPerDeployment`, `TestPlusReservedInNewTilePaths` · `TestInstanceTokenCarriesDeployment`, `TestSelfCallsStayInDeployment` (isolated), `TestAddDeploymentNameCollision` | — |
| T4 mutation or exfiltration through outbound edges | `TestReadClampOnEveryEdge`, `TestEdgeBlockFailsClosed`, `TestDeploymentHeaderOnlyFromXbind`, `TestNonPrimaryBusPublishIsolated`, `TestNonPrimaryNeverJoinsProviderRoster` · `TestUnclampableEdgeDefaults`, `TestNetEdgeDefault`, `TestOutboundEdgePolicy` (isolated) | Residual: `read` edges and the inherited net edge let non-primary code read what the tile reads (P20, an accident boundary) |
| T5 secrets | `TestVaultPerDeployment`, `TestVaultCopyManagerOnly`, `TestAlwaysOnNonPrimaryManagerOnly`, `TestProtectedPrimaryVaultWritesManagerOnly` · seam row 35 | Manual: the warning copy when a manager copies an exclusive connection's token and turns alwaysOn on |
| T6 side effects | `TestNonPrimaryCronDormant`, `TestNonPrimaryBusSubDormant`, `TestNonPrimaryIfaceInstancesNeverRouted`, `TestNonPrimaryIngressHostsNeverRouted`, `TestNonPrimaryNotifyNeverPushed`, `TestNonPrimaryStatusNamespaced`, `TestDeliveriesSwitchManagerOnly`, `TestReassignMovesActiveRegistrations` · `TestRunNow`, `TestInboundEdgesReachOnlyPrimary` (isolated), `TestRegistrationsAtStartStayDormant` (isolated) | — |
| T7 information disclosure through events | `TestDeploymentEventsFiltered`, `TestNonPrimaryBuildErrorNotBroadcast`, `TestNonPrimaryRowsNeedWrite` · `TestNonPrimaryUsesNewEventTypes`, `TestSandboxesDeploymentRows` | Pre-existing: every other non-bus event reaches every subscriber (`internal/server/server.go:603-605`; side finding 2). That is out of scope here, and its fix carries its own test |
| T8 seeding and PII | `TestSeedManagerOnly`, `TestSeedNeverWritesPrimary`, `TestSeedCopyConfinedNoFollow`, `TestSharedScopeSeedNeedsEveryTile` · `TestDataSeparation` (isolated) | Manual: the PII warning text |
| T9 authority enforcement | `TestDeployAuthzMatrix`, `TestViewAsRefusedEveryOp`, `TestNoTerminalCannotOperate`, `TestOwnRuntimePrincipalsCannotOperate`, `TestAuthorizationRecheckedAtCommit` · `TestTargetGates`, `TestDeploymentURLGate`; harness `deployments` step 7, `viewAs` | — |
| T10 resource exhaustion | `TestDeploymentCountCaps`, `TestCheckpointRateLimit`, `TestDeployQueueCoalesces`, `TestDiskQuotaCountsDeploymentData`, `TestGCKeepsTreesOfRunningGenerations`, `TestNonPrimaryCgroupSubtree`, `TestNonPrimaryVMCannotStarvePrimary` · `TestCaptureCapsRefuseHugeTree`, `TestCheckpointGC`, `TestPrimaryLeafKeepsCaps`, `TestReserveChargesTileForEveryDeployment`, `TestVMReserveOwnerIsTile` | Manual: a build storm across several deployments on a real box (§10.3) |
| T11 tampering, downgrade and restore | `TestRecordValidationFailsClosed`, `TestRecordTilePathMismatchRefused`, `TestRestoreRefusesForeignManifest`, `TestRestoreRebuildsStoreConfig` (confined), `TestRestoreRevalidatesRegistrations`, `TestOlderBinaryIgnoresDeploymentFiles` · `TestTerminalMasksDeploymentState`, `TestDeploymentStateBootsTwice(InProcess)`, `TestDeploymentKeysDisjoint`, `TestPathLeftoversIncludeDeploymentState` | Manual: operator edits from a host shell are out of scope |
| T12 non-isolated mode | `TestNonIsolatedRefusesBackendDeployments`, `TestStaticTilePauseLiveReloadWithoutIsolation`, `TestIsolationLossHoldsPinnedBackends`, `TestNoAuthProtectionMarkedUnenforced` · `TestLiveReloadPauseRefusedWithoutIsolation`, `TestDirectModeRefusesRebind`; harness `livereload` step 5 | — |
| T13 cgi | `TestCgiRefusedForPinnedDeployments`, `TestProxyRuntimeFromDeployment` (a work-tree edit to `"runtime":"cgi"` never makes pinned code run as cgi), `TestNoDirectExecFlagsCGIHandler` (NP-15-7) | The existing host-exec hole (`internal/proxy/proxy.go:239-261`; side finding 1) is a separate urgent fix with its own test |
| T14 chrome and `xbin`-capable tiles | `TestP19RefusesChromeAndXbinTiles`, `TestNonPrimaryDocumentAlwaysSandboxed`, `TestNonPrimaryPrincipalNeverAdmin`, `TestXbinGrantRefusedWithNonPrimary` · `TestQualifiedURLRouting` (`root+dev` is a 404) | — |
| T15 the fetch remote, tracked branches, the deploy remote | M1: `TestFetchRemoteReadGate`, `TestFetchRemoteServesOnlyGitFiles`, `TestFetchRemoteRefusesSymlinks` · `TestCheckpointFetchRemoteFlowH` (confined). M3: `TestTrackedBranchReadsGitOnlyInConfine`, `TestTrackedBranchRefusedOnProtectedPrimary`, `TestDeployRemoteBearerOnly`, `TestDeployRemoteHostilePack`, `TestDeployRemoteRefNamespace`. The watcher never sees ref changes (`internal/watch/watch.go:61-74`), so the tracked branch needs its own trigger test | — |
| T16 protected-primary bypass | `TestProtectedPrimaryRefusesTerminalTokens`, `TestProtectedPrimaryIgnoresWorkTreeTileFields`, `TestProtectedPromoteNeedsReviewedCheckpoint`, `TestReassignPinsReviewedCheckpoint`, `TestProtectedBuildProductsSeparated`, `TestProtectionCoversNestedComponents` · `TestProtectedPrimary`, `TestWorkTreeWritersReachOnlyLiveTarget`, `TestPinnedPrimarySurvivesMissingManifest`, `TestAgentFlowCWithBxOnly` (isolated); harness `deployments` step 7 | — |
| T17 `setup` supply chain per deployment | `TestEnvLayerGCKeepsReferenced`, `TestSetupLayerSeesCheckpointNotWorkTree`, `TestProtectedPrimaryLayerNotShared` · `TestNetEdgeDefault` (the setup run uses the deployment's effective net edge) | Residual: a malicious `setup` still runs sandboxed with the deployment's network (P20) |
| T18 the shared sandbox layer | `TestLeafPerDeployment`, `TestNonPrimaryResourceBindsNeverPrimary`, `TestRegistryRowsPerDeployment`, `TestRunDirPerDeployment`, `TestVMBackendNamespaceBinds` · `TestLaunchSpecBinds`, `TestNonPrimaryCgroupSubtree`, `TestSandboxesDeploymentRows`, `TestPinnedBackendSeesCheckpoint` (confined) | — |
| T19 tile-managed sandboxes per deployment (after D113) | `TestTileSandboxesPerDeployment`, `TestTileSandboxMountsDeploymentNamespace`, `TestAttachTicketBindsDeployment` | Until D113 is built there is nothing to test |
| T20 checkpoint content disclosure and retention | `TestPurgeManagerOnly`, `TestPurgeRefusesDeployedCheckpoint`, `TestPurgeRemovesObjects`, `TestCheckpointDiffNoExternalTools` (the diff runs with `--no-ext-diff --no-textconv`), `TestSeedSqliteConfined` (confined), plus the T1/T2/T15 tests it reuses · `TestFetchRemoteInjection` (only readers' sessions get the remote) | — |

**One test, two names** (06-security.md's name = this plan's, which 09-fabric.md or 12-compat.md already cite):
- **Tokens and self-calls:**
  - `TestMintRefusesCrossDeployment` = `TestMayMintFrameTokenNeverCrossesDeployments`;
  - `TestFrameTokenBoundToDeployment` = `TestFrameTokenDeploymentClaim`;
  - `TestSelfCallStaysInDeployment` = `TestSelfCallRouting`;
  - `TestTerminalTargetBinding` and `TestTerminalTokenCannotMintProtectedPrimaryToken` = `TestTerminalTokenTarget`;
  - `TestProtectedPrimaryRefusesTerminalTokens` = `TestTargetProtectedPrimary`;
  - `TestDeploymentURLRefusesOtherTiles` = a case of `TestDeploymentURLGate`.
- **Edges and ingress:**
  - `TestIngressNeverReachesNonPrimary` = `TestIngressForwardPrimaryOnly`;
  - `TestDeploymentHeaderOnlyFromXbind` = `TestIdentifyDeploymentHeader`;
  - `TestEdgeBlockFailsClosed` = `TestEdgePolicyBlock`;
  - `TestReadClampOnEveryEdge` = `TestGrantedRoleReadClamp`;
  - `TestNonPrimaryBusPublishIsolated` = `TestBusPublishStaysInNamespace`.
- **Registrations and status:**
  - `TestNonPrimaryCronDormant`, `…BusSubDormant`, `…IfaceInstancesNeverRouted` and `…IngressHostsNeverRouted` = the subtests of `TestDormantRegistrations`;
  - `TestNonPrimaryNotifyNeverPushed` = `TestNotifyNotPushed`;
  - `TestNonPrimaryStatusNamespaced` = `TestStatusPerDeployment`;
  - `TestDeliveriesSwitchManagerOnly` = `TestDeliveriesSwitch`;
  - `TestReassignMovesActiveRegistrations` = `TestReassignPrimaryAtomic`.
- **The sandbox layer:**
  - `TestLeafPerDeployment` = `TestCgroupLeafPerDeployment`;
  - `TestRegistryRowsPerDeployment` = `TestSandboxEntriesPerDeployment`.
- **The rest:**
  - `TestOlderBinaryIgnoresDeploymentFiles` = `TestDowngradeStatic` + `TestDowngradeDormantRegistrations`;
  - `TestStaticTilePauseLiveReloadWithoutIsolation` = `TestLiveReloadPauseStatic`;
  - `TestGCKeepsTreesOfRunningGenerations` = a case of `TestCheckpointGC`;
  - `TestDeployQueueCoalesces` = seam row 28;
  - `TestNoDirectExecFlagsCGIHandler` = the `TestNoDirectExec` pattern of NP-15-7.

## 10. CI and manual verification

### 10.1 CI (recommendations, NP-15-1)

- **A git-bearing rootfs before `make integration`.**
  - Today CI runs `make check`, `make tile-check` and `make integration`
    (`.github/workflows/ci.yml:41-43`). It builds a small Ubuntu rootfs only
    for the VM tests, later and without git (`ci.yml:70-76`).
  - Build one rootfs before line 43, holding:
    - the VM tools;
    - `git` and `ca-certificates`;
    - `nodejs` and `python3` for the runtime matrix of
      SC-LIVE-RELOAD-PAUSE.

    It needs no Go toolchain: confined builds bind the host GOROOT
    read-only (`internal/runner/build.go:51-52`). This is NP-02-8's intent.
  - Export it as both `XBIN_ROOTFS` (VM tests) and `XBIN_TEST_ROOTFS`, cached
    on the inline Dockerfile's hash.
  - The existing sandboxed tests then stop skipping in CI:
    `TestHostileRepoStaysInside`, `TestConfinedGoBuild`, every §4 test and
    the isolated daemon of §5.
  - User namespaces already work there, because the AppArmor sysctl is set
    at `ci.yml:28`.

  ```yaml
  - run: |
      printf 'FROM docker.io/library/ubuntu:26.04\nRUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates nodejs python3 bash curl e2fsprogs iproute2 util-linux && rm -rf /var/lib/apt/lists/*\n' | docker build -t xbin-test-rootfs -
      mkdir -p "$RUNNER_TEMP/rootfs" && docker export "$(docker create xbin-test-rootfs)" | tar -C "$RUNNER_TEMP/rootfs" -x
      echo "XBIN_TEST_ROOTFS=$RUNNER_TEMP/rootfs" >> "$GITHUB_ENV"
      echo "XBIN_ROOTFS=$RUNNER_TEMP/rootfs" >> "$GITHUB_ENV"
  - run: XBIN_GOCRYPTFS="$PWD/bin/gocryptfs" XBIN_FUSE_OVERLAYFS="$PWD/bin/fuse-overlayfs" make integration
  ```

- **The Makefile integration list** gains `./internal/checkpoint/` and
  `./internal/sandbox/` (§4). `TestIntegrationPackagesListed` (NP-15-6)
  keeps it complete.
- **gocryptfs.** CI already builds `bin/gocryptfs`. Passing
  `XBIN_GOCRYPTFS` lets the isolated tests cover file resources.
- **The downgrade binary.** A CI step downloads the previous release's
  xbind and exports `XBIN_DOWNGRADE_BIN` (NP-15-4).
- **Owners.** `Makefile` and `ci.yml` are integrator-owned (principle 8).

### 10.2 What stays local

- **The UI harness is not in CI** (research/delivery-infra.md §0). Each WP
  runs its passes and reports them in its commit body.
- **Sandboxed tests on a developer's machine** run with the agent's own
  tool sandbox off. Claude Code's sandbox refuses the `/proc/self/exe` re-exec the
  sandbox init needs.
- **The Swift rows** (§3.12) run in the `native` CI job.

### 10.3 Manual checklist (an isolated xbind with auth on, before each milestone ships)

Run `bin/xbind --isolate --rootfs <rootfs> --workspace <tmp>`, with
`XBIN_FUSE_OVERLAYFS`, `XBIN_SDK_PATH` and `XBIN_BIN` set and two users (a
terminal-level non-manager and a manager). Stop it by PID.

1. **Flows A–H** from 05-model §14 by hand, on a Go tile and a static tile,
   through the terminal window and through `bx`.
2. **Restart paths.**
   - Kill `-9` the backend (the PID is in runtime → sandboxes), restart
     xbind, and delete `.xbin/build`.
   - Disable then enable the tile, and seal then unseal a passphrase
     vault.
   - Each time, the pinned code serves.
3. **Admin console, runtime → sandboxes.**
   - Non-primary generations appear under their tile with the deployment
     named.
   - On a KVM host, a VM backend in `dev` is charged to the tile in
     `usedBy`.
4. **An old shell:** a workspace initialized by the previous release, with
   no `bx builtin update`.
   - There are no extra sidebar rows, and `dev` builds change no toast,
     status dot or title mark.
   - The binary-served terminal window shows the new controls.
5. **The iOS app** (a TestFlight build or the simulator).
   - Primary tiles load and reload as today.
   - A `dev` build or reload never reloads the open view; a promote
     reloads it once.
   - A nested tile's `dev` activity doesn't reload its ancestor
     (Divergences 1).
6. **Downgrade** to the previous release, then upgrade again, checking what
   [12-compat.md](12-compat.md) says.
7. **A build storm:** rapid reload-now presses on three deployments. There
   is one build in flight per deployment, the cgroup caps hold, and the box
   stays responsive.
8. **After release,** the QA box (`deploy/qa/`) repeats 1 and 2 on the
   shipped build.

## 11. Traceability

### 11.1 Invariants

| Invariant | Tests |
|---|---|
| P1 checkpoint core + feeds | `TestCheckpointContent`, `TestCheckpointIDs`, `TestDeployLog` (feed recorded), seam rows 23–29; M3 `TestTrackedBranchReadsGitOnlyInConfine`, `TestDeployRemoteHostilePack` |
| P2 the term "tile deployment" | `TestDeploymentWording`, `hack/deploy-state.test.mjs` (glossary spellings) |
| P3 read clamp + per-edge block | `TestGrantedRoleReadClamp`, `TestEdgePolicyBlock`, `TestEdgePolicyNeverTouchesPrimary`, `TestOutboundEdgePolicy` |
| P4 parity | `TestDeployAuthzMatrix` (unprotected-primary row), `TestAgentFlowCWithBxOnly` |
| P5 zero state | §2.7 (all), `TestOptOutReturnsToZeroState`, §6 unchanged fixture |
| P6 storage follows the deployment | `TestZeroStateKeys`, `TestKVNamespacePerDeployment`, `TestNonPrimaryResourceBindsNeverPrimary`, `TestDataSeparation` |
| P7 only the primary receives inbound edges | `TestInboundEdgesReachOnlyPrimary`, seam row 33, `TestQualifiedURLRouting`, `TestReassignPrimaryAtomic`, `TestReassignPrimaryFlowF` |
| P8 at most one live reload target, no per-save cost | seam rows 16, 30, 31; `TestReloadPlan`, `TestWatchLoopZeroStateNoStoreIO`, `TestRecordInvariantsRandomized`, §8 |
| P9 pinned means pinned | seam rows 16–27; `TestPinnedThroughRestartPaths`, `TestArtifactPerCheckpoint`, `TestLaunchSpecBinds`, `TestPinnedBackendSeesCheckpoint` |
| P10 promotion moves code only | `TestPromoteMovesCodeOnly`, `TestPromoteAndRollBack` |
| P11 authority stays per tile | `TestPrincipalDeploymentZeroValue`, `TestDeployAuthzMatrix`, `TestGrantedRoleReadClamp` |
| P12 self-calls stay inside the deployment | `TestSelfCallRouting`, `TestMayMintFrameTokenNeverCrossesDeployments`, `TestInstanceTokenCarriesDeployment`, `TestSelfCallsStayInDeployment` |
| P13 dormant registrations, no pushes, namespaced status and events | `TestDormantRegistrations`, `TestNotifyNotPushed`, `TestStatusPerDeployment`, `TestNonPrimaryUsesNewEventTypes`, `hack/events-socket.test.mjs` |
| P14 empty start; seeding and vault copy by managers | `TestKVNamespacePerDeployment`, `TestVaultPerDeployment`, `TestVaultCopyManagerOnly`, `TestSeedManagerOnly`, `TestSeedNeverWritesPrimary` |
| P15 xbind-owned state | `TestRecordLocation`, `TestTerminalMasksDeploymentState`, `TestZeroStateCreatesNoDeploymentFiles`, `TestDeploymentStateBootsTwice` |
| P16 confine-only git; contained serving | `TestCaptureIgnoresTileGitConfig`, `TestStoreUsesConfineOnly`, `TestPinnedServingUsesOpenBeneath`, `TestMaterializeSymlinkEscape`, `TestRebindDirInSandbox` |
| P17 the `+` qualifier; additive markers | `TestResolveDeploymentQualifier`, `TestQualifiedURLRouting`, `TestIdentifyDeploymentHeader`, `TestSessionTarget`, `TestFrameTokenDeploymentClaim` |
| P18 isolation; static pauses; cgi excluded | `TestNonIsolatedRefusesBackendDeployments`, `TestLiveReloadPauseStatic`, `TestCgiRefusedForPinnedDeployments`, `TestDirectModeRefusesRebind` |
| P19 chrome and xbin-capable tiles may pause live reload, nothing more | `TestP19RefusesChromeAndXbinTiles` |
| P20 an accident boundary | `TestDeploymentURLGate` (only humans with at least `write`); manual review that the builder docs say it is not a trust boundary |
| P21 protected primary | `TestProtectedPrimary`, `TestTargetProtectedPrimary`, `TestTerminalTokenTarget`, harness `deployments` step 7 |

### 11.2 Success criteria ([02-goals.md](02-goals.md) §5)

| Criterion | Tests | M |
|---|---|---|
| SC-ZERO | §2.7 (all), §6, harness `livereload` part 0 | M0 → every milestone |
| SC-OPT-OUT | `TestOptOutReturnsToZeroState` | M1 |
| SC-LATENCY-DEFAULT | `TestLatencyStaticSaveToReload`, `TestLatencyGoSaveToServing`, seam row 30, `TestWatchLoopZeroStateNoStoreIO` | M0 baseline, M1, M2 |
| SC-LIVE-RELOAD-PAUSE | `TestLiveReloadPauseRace`, `TestLiveReloadPauseStatic`, `TestLiveReloadPauseGo`, seam rows 16–25 | M1 |
| SC-LATENCY-OPS | `TestLatencyLifecycleOps` | M1, M2 |
| SC-ROLLBACK | `TestRollBackBound`, `TestArtifactPerCheckpoint`, `TestEnvLayerGCKeepsReferenced` | M1, M2 |
| SC-PINNED | `TestPinnedThroughRestartPaths` (§5.4), seam rows 16–22 | M1 |
| SC-SAFE-DEPLOY | `TestFailedDeployInvisible`, seam rows 25 and 38, the Swift replay rows (§3.12) | M1 |
| SC-INBOUND | `TestInboundEdgesReachOnlyPrimary`, `TestEnsureCallSitesPassPrimary`, seam row 33, the §5.5 unit rows | M2 |
| SC-DATA | `TestDataSeparation`, `TestKVNamespacePerDeployment`, `TestNonPrimaryResourceBindsNeverPrimary`, `TestPinnedBackendSeesCheckpoint`, `TestSeedNeverWritesPrimary` | M2 |
| SC-CLAMP | `TestOutboundEdgePolicy`, `TestGrantedRoleReadClamp`, `TestEdgePolicyBlock`, `TestUnclampableEdgeDefaults`, `TestIdentifyDeploymentHeader` | M2 |
| SC-DORMANT | `TestDormantRegistrations`, `TestRegistrationsAtStartStayDormant`, `TestNotifyNotPushed`, `TestDowngradeDormantRegistrations` | M2 |
| SC-EVENTS | `TestNonPrimaryUsesNewEventTypes`, `TestDeploymentEventsFiltered`, `TestStatusPerDeployment`, `hack/events-socket.test.mjs`, the Swift rows | M2 |
| SC-PROTECT | `TestProtectedPrimary`, `TestDeployAuthzMatrix` (protected row, stable error code), `TestTargetProtectedPrimary`, `TestAgentFlowCWithBxOnly` step 7, harness `deployments` step 7 | M2 |
| SC-AGENT-BX | `TestAgentFlowCWithBxOnly`, `TestAgentBxLiveReload`, `TestBxSaysWhereSavesGo` | M1 variant, M2 |
| SC-WORKTREE | `TestOperationsNeverWriteWorkTree`, `TestFetchRemoteInjection`, `TestPromoteAndRollBack` | M1, M2 |
| SC-AUDIT | `TestDeployLogAudit`, `TestDeployLog`, `TestBackupCoversDeployments` | M1 |
| SC-FAIL-CLOSED | `TestNonIsolatedRefusesBackendDeployments`, `TestLiveReloadPauseRefusedWithoutIsolation`, `TestCgiRefusedForPinnedDeployments`, `TestP19RefusesChromeAndXbinTiles`, `TestLiveReloadPauseStatic` | M1, M2 |
| SC-PRIMARY-FIRST | `TestPrimaryLeafKeepsCaps`, `TestNonPrimaryVMCannotStarvePrimary`, `TestPrimaryFirstUnderPressure` | M2 |

## Open questions

- **Test names.** 06-security.md, 09-fabric.md and 12-compat.md were
  written in parallel with this plan. §9 pairs the names that differ, and
  the integrator keeps one per pair before 14-implementation.md assigns
  tests to work packages.
- **Where main's cgroup leaf lives once a tile has a record.**
  - 02-goals.md Z8 keeps a zero-state tile in today's flat
    `comp-<CompKey>` leaf (`internal/cgroup/cgroup_linux.go:89`), and
    creates the per-tile parent only for tiles with a record.
  - cgroup v2 forbids processes in a cgroup whose children have
    controllers, so the flat leaf can't also be the parent. Adding the
    first non-primary deployment therefore has to move main's processes, or
    start its next generation under the parent.
  - 07-runtime.md decides which. `TestCgroupLeafPerDeployment` and
    `TestNonPrimaryCgroupSubtree` pin the result.
- **Idle reap end to end.** The idle-reap path is covered only by the
  seam's fake clock, because `idleReap` is a 30-minute constant
  (`internal/runner/runner.go:46`). Is that enough, or should an admin
  "reap now" action exist (a product feature, not a test knob)?
- **The final event scheme** (Divergences 1) decides the exact bytes in
  `TestNonPrimaryUsesNewEventTypes`.
- **Flow H's refspec.** The checkpoint remote's ref layout must let
  `deploy/<name>` resolve after `git fetch xbin-deploy`, for example through
  a refspec into `refs/remotes/deploy/*`. `TestCheckpointFetchRemoteFlowH`
  pins the observable. 11-contract.md picks the refs.

## Divergences from the model

1. **§8's claim that old clients ignore `<tile>+<name>` events is false for
   nested tiles.**
   - The matchers accept `component === src || startsWith(src + '/')`. That
     covers the web (`web/events-socket.js:40-49`) and the shipped app
     (`Events.swift:161-166`, whose shipped copies lag xbind by months,
     compat rule 10).
   - So `apps/a/b+dev` still matches an ancestor frame `apps/a`, and a
     non-primary `reload` would reload the ancestor's primary frame and its
     open iOS view.
   - The registry does allow nested components: the scan doesn't stop at a
     component (`internal/registry/registry.go:440-474`). Only the create
     API refuses nesting (`internal/broker/policy.go:293-306`).
   - **Recommended fix:** never publish non-primary activity under
     `reload`, `build-*` or `status`. Use new event types (or only the
     `deployments` event), keeping `component: "<tile>+<name>"` and
     `deployment` inside them. 12-compat.md reaches the same rule (its C2).
   - Tests: `TestNonPrimaryUsesNewEventTypes`, `hack/events-socket.test.mjs`
     and the Swift rows.
2. **§2's zero state says "no checkpoint store", but opting out only
   "removes the record".**
   - §3 also exposes the store to terminals as a fetch remote. If that
     injection keyed off the store, a tile that opted out would get a
     different terminal env from a tile that never opted in.
   - **Recommended fix:** define the zero state behaviourally, and key
     every behaviour (including `TestFetchRemoteInjection`) off the record.
     The store stays for history until GC or tile removal, where it already
     counts as a leftover (§11). 02-goals.md makes the same call (NP-02-2,
     with the remote kept in the session env by NP-02-7).
   - Test: `TestOptOutReturnsToZeroState`.
3. **P16 conflicts with §6.**
   - P16: materialized trees "never followed out through symlinks".
   - §6: cross-tile references follow the other tile's primary, including
     `deps/` links. Those are relative symlinks out of the tile
     (`internal/deps/deps.go:59-69`), which today's `/c/` plane follows
     with `OpenResolved` (`internal/server/static.go:182-219`). That opener
     also refuses anything under `.xbin/` through `pathAllowed`
     (`internal/server/static.go:347-361`), so it can't serve
     `.xbin/deploy/…` at all: checkpoint serving needs its own opener
     either way.
   - **Recommended fix:** open materialized files with `OpenBeneath`. A
     link whose target leaves the checkpoint is resolved **by path**
     against the tile's canonical location and re-dispatched through the
     `/c/` resolution of the target tile's primary, under today's
     allow-rules. The host symlink is never followed.
   - Test: `TestPinnedServingUsesOpenBeneath`.
4. **§6 keeps a pinned primary serving while the work tree lacks its
   manifest, but also takes tile-level fields from the work tree.**
   - `exposes`, `provides`, `interfaces` and `uses` would vanish mid-refactor,
     and bindings and ingress would flap.
   - **Recommended fix:** while the work-tree manifest is missing or
     unparsable, keep the last-known-good tile-level fields.
   - Test: `TestPinnedPrimarySurvivesMissingManifest`.
5. **A failed pause of live reload (§5) leaves the state claiming what
   isn't true.**
   - When the build fails while pausing live reload, "X keeps serving its
     current generation".
     That generation is bound to the live work tree
     (`{Src: c.Dir, Dst: c.Dir, RO: true}`,
     `internal/runner/runner.go:640`), so files read at runtime still
     follow saves.
   - Yet §4's record invariant would give X a checkpoint, claiming it is
     pinned.
   - **Recommended fix:** adopt NP-02-11.
     - X is recorded pinned to the attempted checkpoint, in state `failed`.
     - The state endpoint, `bx` and the terminal window say that pausing
       live reload did not complete.
     - Any restart runs the attempted checkpoint and fails visibly, never
       the work tree.
     - The static plane serves the attempted checkpoint at once.
     - No bare event reaches viewers (NP-02-4).
   - Test: seam row 25.

## New proposals

NP-15-5, NP-15-10 and NP-15-13 were dropped, and their numbers aren't
reused. The sibling documents already propose them:
- NP-02-5 covers the event audience, at `write` level;
- 06-security.md T11 covers record validation failing closed;
- SC-LATENCY-OPS covers the latency targets.

- **NP-15-1.** Build one rootfs in CI before `make integration`, with `git`,
  `ca-certificates`, `nodejs` and `python3`, and export it as both
  `XBIN_TEST_ROOTFS` and `XBIN_ROOTFS`. The confined and isolated tests then
  run in CI instead of skipping (§10.1; `.github/workflows/ci.yml:43`,
  `:70`).
  - This is NP-02-8's intent, minus its Go toolchain: confined builds bind
    the host GOROOT (`internal/runner/build.go:51-52`).
- **NP-15-2.** Add an isolated integration daemon, `startIsolatedDaemon`, in
  `test/`. It hosts every pinned or non-primary backend test, while the
  shared daemon stays non-isolated (§5.1; P18; `test/integration_test.go:85`).
- **NP-15-3.** Add a `HARNESS_ISOLATE=1` mode to `hack/ui-harness/run.sh`
  that adds `--isolate --rootfs $XBIN_TEST_ROOTFS`. The backend halves of
  `livereload` and `deployments` SKIP without it (`run.sh:74-75`).
- **NP-15-4.** Run downgrade tests against the previous release's published
  binary through `XBIN_DOWNGRADE_BIN`, which CI downloads (§5.6).
- **NP-15-6.** Add `TestIntegrationPackagesListed`. `internal/sandbox`'s
  integration tests are missing from `Makefile:110-117` and never run
  (side finding 24).
- **NP-15-7.** Teach `TestNoDirectExec` (`internal/confine/guard_test.go:14`)
  to flag `cgi.Handler{`, so the cgi exclusion can't regress silently
  (`internal/proxy/proxy.go:249`; T13).
- **NP-15-8.** Write the zero-state goldens in M0 against master. They are
  hand-maintained, with no update switch, and changing one is a compat
  change (§1 principle 2).
- **NP-15-9.** Add `TestDeploymentWording`, a vocabulary guard over the new
  UI, `bx` and doc strings for the glossary's banned words (P2).
- **NP-15-11.** Run the latency tests in two tiers (§8).
  - **Every `make integration`:** five warm samples, with the hard bounds on
    every sample and the median against the p95 target.
  - **Milestone exits** (`XBIN_TEST_FULL=1`): 02-goals.md's full p95 over at
    least 30 samples, compared back to back with `XBIN_BASELINE_BIN`.
  - SC-LATENCY-DEFAULT's 30-sample rule on every commit would add minutes
    of Go builds to CI.
  - If master already misses a budget in M0, the WP reports it and doesn't
    loosen it (`plans/dev-flow.md:102-104`).
- **NP-15-12.** Replace the magic numbers.
  - `agentTab`'s layout count (`hack/ui-harness/passes/agenttab.js:238`)
    reads a new `testApi().layouts` getter.
  - `menus.test.mjs`'s positional asserts (`:110`, `:135`) become
    label-based.
