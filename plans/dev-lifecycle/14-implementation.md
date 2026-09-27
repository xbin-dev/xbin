# 14 — Implementation plan: milestones and work packages for the swarm

> Status: live — the milestones, work packages, waves, file ownership, integration model and risk plan the implementation swarm builds tile deployments from (part of [plans/dev-lifecycle](README.md))

This document turns the design set into work that a swarm of coding agents
can run in parallel without colliding. It owns:
- the milestones and their exit criteria (§1);
- the work packages (WPs), each at most about two days of focused agent work
  (§2);
- the waves, the dependency graph and the file-ownership matrix (§3);
- how the integrator merges, numbers decisions and ships (§4), and how the
  agents work (§5);
- risks, rollback and shipping dark (§6);
- the proof that every surface of [13-surfaces.md](13-surfaces.md) and every
  test of [15-test-plan.md](15-test-plan.md) has an owner (§7).

It adds no mechanism. What a WP builds is specified by
[05-model.md](05-model.md) and the documents that elaborate it:
[06-security.md](06-security.md), [07-runtime.md](07-runtime.md),
[08-data.md](08-data.md), [09-fabric.md](09-fabric.md), [10-ux.md](10-ux.md),
[11-contract.md](11-contract.md) and [12-compat.md](12-compat.md). Where they
disagree, §0.3 says what gets built. Terms are
[01-glossary.md](01-glossary.md)'s. Delivery facts (guards, budgets, the
harness, the native-swarm precedent) come from
[research/delivery-infra.md](research/delivery-infra.md); the shared sandbox
layer from [research/sandbox-visibility.md](research/sandbox-visibility.md).
Every `file:line` below was re-checked in this worktree.

**Reading a card.** The heading gives the WP's id, slug, size, wave (§3.1;
"1.2" is M1's second wave) and merge-order dependencies ("after"). A WP may
start earlier against declared interfaces, but merges after them. Then:
- **Scope**, with the sections that specify it;
- **Owns**: the files it may create or edit, exclusively within its wave
  (§3.3);
- **Tests**: named as in [15-test-plan.md](15-test-plan.md); `=` joins an
  [06-security.md](06-security.md) alias to its name;
- **Links**: the interfaces it provides or consumes (→ the WPs that use
  them), the docs it hands over, and the [12-compat.md](12-compat.md) proof
  obligations (PO-n) it keeps green.

Sizes: **S** is about half a day, **M** one day, **L** two days of focused
agent work. Every card also carries the standard clauses of §2.1.

## 0. Before the first work package

### 0.1 Gates

| Gate | Holds when | State on 2026-09-27 |
|---|---|---|
| PRE-1 | The `sandbox-visibility` branch (D112 landed, D113 designed) is in master, so the shared sandbox layer this plan changes is master's | **Holds.** `git merge-base --is-ancestor 06c6519 master` succeeds. Master has moved on since (the agent template's model work, native fixes) and edited `docs/protocol.md`, `internal/server/openapi.go`, `internal/server/agentapi.go` and `hack/ui-harness/seed.sh`, so the integration branch is cut from current master, never from this design branch's base. `hack/size-budget.txt` is unchanged. |
| PRE-2 | Phase 0: the design set is committed as plans only, with `make check` green | Open. [README.md](README.md) and [06-security.md](06-security.md) link `16-open-questions.md`, which must exist first (`TestRelativeLinksResolve`, `internal/docscheck/docscheck_test.go:114`) |
| PRE-3 | The owner has ruled on each item of §0.2 before the wave it blocks, or accepted the default this plan builds | Open |
| PRE-4 | An integrator is named (§4.2) | Open |
| PRE-5 | The `remove-cgi` branch is in master before wave 0.1: the model assumes cgi is gone ([05-model.md](05-model.md) §12, owner, 2026-09-27), and that branch edits files M0 splits or owns (`internal/runner/runner.go`, `cmd/bx/main.go`, `internal/proxy/proxy.go`, `internal/registry/registry.go`, `internal/confine/guard_test.go`, `internal/server/openapi.go`, `docs/protocol.md`) | Open: one commit (3cc44d7), not yet an ancestor of master. WP-00's line ranges and ratchets are re-measured after it lands. |

### 0.2 Rulings that block a wave

| # | Ruling | Blocks | Built until ruled |
|---|---|---|---|
| R-1 | Non-primary activity on the old event types with a qualified `component` ([11-contract.md](11-contract.md) §3.2), or only on the `deployments` type ([12-compat.md](12-compat.md) rule C2, [04-options.md](04-options.md) NP-04-1, `TestNonPrimaryUsesNewEventTypes`)? | wave 2.1 (WP-33's `eventComp`), WP-48, WP-56, WP-60 | C2: no `reload`, `build-*` or `status` event ever names a non-primary deployment (Divergence 1) |
| R-2 | P21's "cannot target" for sessions: **ruled** by P24 (owner, 2026-09-27): the terminal's API dropdown picks the target, defaulting to the primary, and never offers a protected primary | WP-51, WP-53, WP-56 | P24; for a protected primary with live reload paused, NP-14-13 (Divergence 2) |
| R-3 | The offload fix as a P5 exception ([08-data.md](08-data.md) §9.4, its Divergence 8) | WP-45 | not built |
| R-4 | Checkpoint purge ([06-security.md](06-security.md) NP-06-16), which needs a route [11-contract.md](11-contract.md) doesn't define | WP-66 | not built; T20's purge tests wait |
| R-5 | The `full` edge value for roles that mean spend ([09-fabric.md](09-fabric.md) NP-09-14) | nothing in v1 | not built; the docs say non-primary deployments lose LLM completions |
| R-6 | Origins mode in M2: an origin per deployment (NP-12-4, NP-11-18), or a refusal | WP-38 | the refusal: 404 with the reason ([07-runtime.md](07-runtime.md) §4.6) |
| R-7 | A git-bearing rootfs in CI (NP-15-1), and the previous release's binary for downgrade tests (NP-15-4) | confined tests in CI from M1; the M2 exit | the integrator adds both to `ci.yml`, in M0 and in M2 |
| R-8 | M3's calls: `match`'s fallback (NP-09-15 or NP-04-7), force pushes on a tracked branch, creating a deployment by push ([04-options.md](04-options.md) open questions 2–3) | M3 | M3 doesn't start |
| R-9 | The ship-dark switch's default in the first release that carries M1 (NP-14-5) | that release | on |

Each milestone's records commit (§4.5) also needs the owner's ratification of
the P-numbers it implements. WPs build the proposals as written.

### 0.3 Where the design documents disagree

| Topic | Disagreement | This plan builds |
|---|---|---|
| Plane package | [15-test-plan.md](15-test-plan.md)'s paths say `internal/deploy`; [13-surfaces.md](13-surfaces.md) forbids the name (the installer's `deploy/`) | `internal/deployments`; test names unchanged |
| `bx` files | 11-contract §9.1: three files; 13-surfaces: one `cmd/bx/deploy.go` | `livereload.go`, `deploy.go`, `deployment.go`, plus `deployclient.go` for shared plumbing |
| Non-main registry ids | 11-contract §8, 07-runtime §10.1: `backend+<name>:<CompKey>:g<gen>`; 13-surfaces §4.4: `backend:<CompKey>:<name>:g<gen>` | 11-contract's |
| Non-main backend log | 07-runtime §9, 11-contract §10.4: `.xbin/deploy/<CompKey>/d/<name>/backend.log`; 08-data §2: `.xbin/log/.deployments/<CK>/<d>.log` | 11-contract's, which owns on-disk formats; D113 state follows it (`…/d/<name>/sbx/`) |
| Harness fixtures and passes | 15-test-plan §7: `apps/reloady`, `apps/deployy`, passes `livereload` and `deployments`; [10-ux.md](10-ux.md) §14: `apps/deploy-demo`, `tileDeployments`, `tileDeploymentRefusals` | 15-test-plan's; 10-ux's refusal assertions join `livereload` step 5 and `deployments` step 7; 10-ux's `testApi` member names are kept |
| `testApi` location | 10-ux §14.1: in `web/bx-frame.js`; 13-surfaces §2: moved to `web/frame-testapi.js` | `frame-testapi.js` |
| The panel | 13-surfaces: file `web/bx-deploy.js`; 10-ux: element `<bx-deployments>` | that file defines that element |
| `bx` exit codes | 11-contract §9.1: 3 refused, 4 not confirmed, 5 still running, 6 no tile deployments; 10-ux NP-10-7: 6 for refusals | 11-contract's |
| Event audience | 11-contract §3.4: ops `record` and `deploy` to readers, the rest to writers; [02-goals.md](02-goals.md) NP-02-5: all to writers | 11-contract's |
| `TestOlderBinaryIgnoresDeploymentFiles` | 12-compat PO-9: a unit test; 15-test-plan §9: another name for the downgrade pair | the unit test keeps the name (WP-49); the downgrade tests keep theirs (WP-63) |
| The checkpoint remote | 02-goals' milestone table: M2; 15-test-plan: its tests in M1 | M1: the downgrade checklist (12-compat §5.5 step 2) needs it once a primary can be pinned |
| Legacy fixture | 12-compat §6.1 adds assertions to both fixtures; 13-surfaces §4.19 and 15-test-plan §6 edit neither | new test functions in new files reuse the fixtures' helpers (NP-14-9) |
| Checkpoint manifest read | 06-security ledger row L5: confined `git cat-file`; 07-runtime §5.1: `fsutil.OpenBeneath` on the materialized tree | 07-runtime's, pinned by `TestCheckpointManifestReadNoFollow` |
| Store keys | 11-contract §10: `<CompKey>` names; 06-security NP-06-11: 128-bit keys | `<CompKey>`, plus the tile-path check of 06-security C10 |
| Runner funnel | 07-runtime §1.2 renames `Ensure(ctx, c)` to `Ensure(ctx, c, dep)`; 15-test-plan §2.2 keeps `Ensure(c)` as a shim | new names beside the old ones (Divergence 3, NP-14-8) |
| SDK owner | 13-surfaces §5: the contracts role | WP-59, in M2, when xbind first sets what the SDK reads |
| cgi | 06-security, 07-runtime, 11-contract, 13-surfaces and 15-test-plan exclude cgi and test the exclusion; 05-model §12 (owner, 2026-09-27) assumes it is removed | nothing for cgi (PRE-5); the two cgi-only tests are dropped (WP-19) |
| Resource declarations | 05-model §6 and P22 (owner, 2026-09-27): `scope.json`'s resources are deployment-level, provisioned per deployment with per-deployment limits; 07-runtime §6 and 08-data provision them tile-wide from the work tree | P22: a pinned primary provisions from its checkpoint in M1 (WP-18), every deployment from its own code in M2 (WP-40), limits default to the tile's (WP-34, WP-44) |
| Session target | P24 (owner, 2026-09-27): the API dropdown, defaulting to the primary, never offering a protected primary; 11-contract §7.4 and DIV-6 default to the live reload target and let sessions target a protected primary; 10-ux §2.3 has a separate picker | P24 (WP-51, WP-56), with NP-14-13 |

### 0.4 Defects the plan stays clear of, without fixing them

| Defect | Evidence | How the plan stays clear |
|---|---|---|
| `ScopeKey` and `CompKey` collide | side finding #7; 08-data NP-08-13; 06-security S3 | non-main keys are injective (WP-39); the record and store verify their tile path (WP-13) |
| `chrome` is tile-editable | 06-security S1 | a protected primary takes tile-level fields from its checkpoint (WP-53) |
| Restore trusts the archive | 06-security S2 | the new archive sections are validated (WP-23, WP-44) |
| Backups race symlinks | side finding #5 | every new walk opens through `OpenBeneath` (WP-42, WP-44) |
| Non-bus events reach everyone | side finding #2 | every new event type and field is filtered from day one (WP-21, WP-48) |

Side finding #1, cgi running on the host, is fixed by the `remove-cgi`
branch (PRE-5). Two more are fixed in scope: `bx logs` reading `.xbin/log`
(#22, WP-26), and `internal/sandbox`'s integration tests never running (#24,
WP-04).

## 1. Milestones

### M0 — prep

**Goal.** Before any feature code: split the files at their budgets, give the
runner and the watcher a test seam, pin today's behaviour in golden tests,
measure the latency budgets, and declare every shared interface, route and
harness pass once.

**Work packages.** M0a, on master: WP-00 to WP-04, WP-S0. M0b, on the
integration branch: WP-05 to WP-08.

**Exit criteria.**
1. `make check` green on master after M0a, and on the integration branch
   after M0b; `make integration` green with `XBIN_TEST_ROOTFS`.
2. The zero-state goldens of [15-test-plan.md](15-test-plan.md) §2.7 and seam
   rows 1–15 green against today's code; `go test -race ./internal/runner/`
   clean.
3. The latency baseline in WP-04's commit body. A budget that master already
   misses is reported to the owner, never loosened.
4. Every route of [11-contract.md](11-contract.md) §1.13 mounted as a 501
   stub with its openapi and protocol rows; both planned passes registered,
   printing SKIP.
5. The ratchets lowered as [13-surfaces.md](13-surfaces.md) §2 lists, no
   budget raised, and a fresh-seed harness run with no new FAIL against
   master (A/B).

**Afterwards.** Nothing changes for any user or tile. The reserved routes
exist only on the integration branch.

### M1 — pause live reload

**Goal.** Flow A, rolling back `main`, and flow H's fetch
([05-model.md](05-model.md) §14), on the checkpoint core, while `main` is
every tile's only deployment.

**Work packages.** WP-10 to WP-29, WP-S1.

**Exit criteria.**
1. These criteria of [02-goals.md](02-goals.md) §5 hold: SC-ZERO (the
   regression gate from here on), SC-OPT-OUT, SC-LATENCY-DEFAULT (M1 half),
   SC-LIVE-RELOAD-PAUSE (100 runs per runtime, `XBIN_TEST_FULL=1`),
   SC-LATENCY-OPS (M1 rows), SC-ROLLBACK (`main`), SC-PINNED,
   SC-SAFE-DEPLOY, SC-AGENT-BX (its M1 variant), SC-WORKTREE and SC-AUDIT
   (M1 operations), SC-FAIL-CLOSED (pausing live reload).
2. Every M1 row of [15-test-plan.md](15-test-plan.md) green under `make
   check` and `make integration` (the isolated daemon, a git-bearing rootfs).
3. The harness gate on a fresh seed, `run.sh --keep livereload viewAs agentTab
   sandboxes reloadFocus`, then a full run with no new FAIL against M0.
4. Items 1 (flows A, D and H), 2, 4, 5 and 6 of the manual checklist
   (15-test-plan §10.3), on an isolated xbind with auth on.
5. WP-29's docs merged, and the records commit (§4.5).

**Afterwards.** On any static tile, and on backend tiles of an xbind started
with `--isolate`, a person or an agent can:
- pause live reload from the terminal window or with `bx live-reload pause`,
  and keep editing while every viewer keeps the pinned checkpoint;
- see how many files moved since, reload now, and resume live reload, which
  returns the tile to the zero state;
- roll `main` back to any checkpoint in its deploy log (`bx rollback`);
- run `git fetch xbin-deploy` in the tile's terminal, and branch from exactly
  what `main` runs.

### M2 — tile deployments

**Goal.** Flows B–H and scenarios S-1 to S-10 except S-8
([02-goals.md](02-goals.md) §3): non-primary deployments with their own URL,
data, vault and dormant registrations; the edge policy; promotion,
reassignment and protection.

**Work packages.** WP-30 to WP-66; WP-S2 to WP-S4; WP-S5 if D113 is built
first.

**Exit criteria.**
1. SC-INBOUND, SC-DATA, SC-CLAMP, SC-DORMANT, SC-EVENTS, SC-PROTECT,
   SC-PRIMARY-FIRST, SC-AGENT-BX (all seven steps), SC-ROLLBACK (every
   deployment), SC-LATENCY-OPS (M2 rows), SC-LATENCY-DEFAULT (live reload
   attached to `dev`), SC-WORKTREE and SC-FAIL-CLOSED (M2 operations) hold;
   SC-ZERO and SC-OPT-OUT stay green.
2. Every M2 row of 15-test-plan green, the downgrade tests against the
   previous release included.
3. The harness gate of 15-test-plan §7.1 (`run.sh --keep livereload
   deployments agentTab sandboxes viewAs`), then a full run.
4. The whole manual checklist, and a non-main volume mounted on the QA box
   under the installer's AppArmor rule ([08-data.md](08-data.md) §3.5).
5. WP-64 and WP-65 merged, and the records commit with 12-compat §10.4's
   changelog entry. No migration note unless a 12-compat §10.2 tripwire was
   crossed.

Gated WPs (WP-38, WP-45, WP-66) never hold the exit: without them M2 ships
the refusal, or nothing (NP-14-12).

**Afterwards.** A person or an agent can:
- add `dev`, with empty or seeded data and a vault holding key names only,
  attach live reload to it, and open `/c/<tile>+dev/`;
- point terminal and agent sessions at `dev`, so their calls and `bx`
  commands reach it;
- promote `dev → main` after reading the diff, roll back from the deploy log,
  and reassign the primary (flow F);
- as a tile manager, seed, copy vault values, protect the primary, set each
  edge to `read` or `block`, turn deliveries on, and run a dormant cron job
  now;
- follow `dev`'s status, logs and "would notify" list in the Deployments
  panel, or with `bx deployment`.

### M3 — later rungs

**Goal.** The tracked-branch and deploy-remote feeds on the same core, and
edge policy `match` (P1, P3). **Work packages:** WP-70 to WP-75 and WP-S6,
outlined in §2.6; each gets a full card once R-8 is ruled. **Exit:** the M3
rows of 15-test-plan, scenario S-8, WP-75's docs, the records commit.
**Afterwards:** a deployment can follow a branch of the tile's repository, a
push to the deploy remote deploys, and an edge can reach the provider's
same-named deployment.

## 2. Work packages

### 2.1 Standard clauses

Every card includes these; a card states only what differs.

1. **Place.** A branch `dl/<slug>` from the integration branch's head, in its
   own worktree (§4.1); M0a WPs branch from master. Never in
   `/home/magik6k/buxon`.
2. **Owned files only.** A seam needed elsewhere is a contract amendment
   (§4.4): stop and ask the integrator.
3. **Never edited by a WP:** `docs/changelog.md`, `docs/changes/*`,
   `plans/DECISIONS.md`, plan status lines, `Makefile`,
   `.github/workflows/ci.yml`; `hack/size-budget.txt` after WP-00; the
   `shots.js` requires and `PASSES` entries, and `seed.sh`, after WP-08;
   `internal/boot/boot.go` outside WP-06, WP-07 and WP-30; openapi rows and
   protocol.md API-fence rows outside WP-07; `examples/`,
   `test/integration_test.go`, both legacy allowed lists,
   `workspace-template/shell/bx-shell.js`,
   `workspace-template/tiles/admin/admin.js`, `internal/builtins/updates.go`,
   `registry.WorkspaceManifest`, `builtin-templates/agent/`.
4. **Budgets.** New logic goes in new files; a budgeted or near-cap file takes
   seams only (a call, a field, a `case`). No budget is raised: a file that
   won't fit gets a verbatim split from the integrator.
5. **Citations.** No new D-number anywhere; P-numbers only in Go comments, as
   `(P9)`; no `plans/` path in `docs/`, `web/` or `workspace-template/`
   (`TestEmbeddedAssets`).
6. **Security.** D78: every tool run on tile data or the checkpoint store goes
   through `internal/confine`, no `exec.Command` without `// exec-ok:`, and
   under isolation a sandbox failure is an error. `plans/auth.md` holds:
   inbound `X-XBin-*` stripped, element principals default-deny, the owner is
   admin. 06-security C5: no `os.Open`, `os.Stat`, `os.Chmod`, `os.Chown` or
   symlink-following walk inside a work tree, resource mount, materialized
   tree or quarantine.
7. **Wire.** Handlers use `server.WriteError`, `WriteOK` and `DecodeJSON`,
   with [11-contract.md](11-contract.md) §1.14's texts; no existing request
   body gains a field (11-contract §0.6).
8. **Zero state.** The goldens of WP-01 to WP-03 stay green, unchanged.
   Changing one is a compat change: the integrator's sign-off and a
   [12-compat.md](12-compat.md) entry.
9. **Tests.** New files, one owner each; the doc comment starts with the IDs
   covered (`// covers P9 T11 — …`, 15-test-plan §1).
10. **Done when** `make check` is green; `make integration` is green when
    runner, sandbox, cgroup, VM, broker, confine, proxy or term code changed;
    the card's harness passes are green on a fresh seed, every SKIP
    explained; commits are area-prefixed and the last body carries
    **Tests:** (run, and not run with the reason), **Docs:** (text for the
    docs WPs), **Changelog:** and **Decision:** (for the integrator).

### 2.2 M0 — prep

#### WP-00 prep-split · S · wave 0.1 · on master
- **Scope.** 13-surfaces §2's verbatim moves, a commit each, ratchets lowered:
  `build` (`internal/runner/runner.go:363-411`) → `build.go`; `sandboxable`,
  `sandboxCmd` (`:625-726`) → `sandboxcmd.go`; `stop` … `reaper`
  (`:728-831`) → `states.go`; `hostForward`, `sandboxEnv`
  (`internal/term/term.go:715-795`) → `sandboxenv.go`; `cmdStatus` …
  `cmdLogs` (`cmd/bx/main.go:346-530`) → `status.go`; the `testApi()` body
  (`web/bx-frame.js:560-601`) → `web/frame-testapi.js`, with a delegate.
  The ranges are measured before `remove-cgi`; re-measure after PRE-5.
- **Owns.** `internal/runner/{runner,build,sandboxcmd,states}.go`,
  `internal/term/{term,sandboxenv}.go`, `cmd/bx/{main,status}.go`,
  `web/{bx-frame,frame-testapi}.js`, `hack/size-budget.txt`.
- **Tests.** `make check`; `git diff --color-moved` shows moved blocks only;
  harness `windows agentTab reloadFocus`. No behaviour changes.

#### WP-01 prep-seam · L · wave 0.2 · after WP-00 · on master
- **Scope** (15-test-plan §2.2–§2.4, §2.6). The engine seam, `reapOnce()`,
  the fake engine and rows 1–15; `sandboxCmd` split into a pure `launchSpec`
  and `sandbox.Launch`; the runner goldens; the watcher tables. `idleReap`
  stays 30 minutes (`internal/runner/runner.go:46`).
- **Owns.** `internal/runner/{runner,engine,states,sandboxcmd}.go`;
  `internal/runner/{fake_engine,statemachine,zerostate}_test.go`,
  `internal/watch/watch_test.go`, `internal/boot/watch_nesting_test.go`.
- **Tests.** Rows 1–15, `TestZeroStateLaunchSpec`, `TestZeroStateKeys`
  (runner half), `TestIgnoreRules`, `TestChangedComponentsNesting`, `go test
  -race ./internal/runner/`.
- **Links.** The engine and `launchSpec` → WP-16, WP-17, WP-33 to WP-35.
  PO-2, PO-11, PO-12.

#### WP-02 goldens-wire · L · wave 0.2 · after WP-00 · on master
- **Scope** (15-test-plan §2.7; NP-15-8). Hand-maintained goldens for URLs,
  listings, events, tokens, headers, session env and `bx`, run-to-run values
  masked.
- **Owns (new).** `internal/server/{zerostate,deployevents}_test.go`,
  `internal/boot/{api_zerostate,deploystate}_test.go`,
  `internal/auth/deployment_test.go`, `internal/proxy/deploy_test.go`,
  `internal/term/deploy_test.go`, `cmd/bx/deploy_test.go`.
- **Tests.** `TestZeroStateDocumentUnchanged` (three asset modes, PO-1's `+`
  matrix), `TestZeroStateComponentsEntry`, `TestZeroStateListings`,
  `TestEventBytesZeroState`, `TestZeroStateTokens`, `TestIdentifyZeroState`,
  `TestSessionEnvZeroState`, `TestBxTodayInvocationsUnchanged`,
  `TestZeroStateCreatesNoDeploymentFiles`.
- **Links.** PO-1, PO-3 to PO-7, PO-13, PO-14.

#### WP-03 goldens-data · M · wave 0.2 · after WP-00 · on master
- **Scope.** Goldens for backend env, storage keys, backups, confine binds
  and the sandbox registry; the zero-state lifecycle test; 12-compat §6.1's
  "no deployment state" checks on the aged workspace, as new functions that
  reuse the fixtures' helpers (NP-14-9).
- **Owns (new).** `internal/broker/{deploy_env,deploy_backup}_test.go`,
  `internal/confine/bind_test.go`, `internal/sbx/deploy_test.go`,
  `test/{livereload,legacy_nodeploy}_test.go`,
  `internal/boot/legacy_nodeploy_test.go`.
- **Tests.** `TestZeroStateKeys` (broker half), `TestZeroStateBackendEnv`,
  `TestZeroStateBackupMembers`, `TestConfineBindDestinationDefault` (every
  `confine.Cmd` user's bind list), `TestEntryJSONZeroState`,
  `TestZeroStateLifecycleFiles`.
- **Links.** PO-2, PO-3, PO-7 to PO-9, PO-11.

#### WP-04 latency-guards · M · wave 0.2 · on master
- **Scope.** The budgets of `plans/dev-flow.md:102-105` as tests, in
  15-test-plan §8's two tiers (NP-15-11); `TestIntegrationPackagesListed`
  beside `TestNoDirectExec` (NP-15-6).
- **Owns.** `test/latency_test.go`, `internal/confine/integration_list_test.go`.
- **Tests.** `TestLatencyStaticSaveToReload`, `TestLatencyGoSaveToServing`,
  `TestIntegrationPackagesListed`.
- **Links.** The integrator adds `./internal/sandbox/` to `make integration`
  (`Makefile:110-117`) in this merge. The measured numbers go in the commit
  body.

#### WP-05 contracts-types · L · wave 0.3 · after M0a
- **Scope** (13-surfaces §5 "Contracts"; 07-runtime §1.2). Each shared
  declaration once, with today's behaviour behind it:
  `util.DeploymentNameOK`, `events.Event.Deployment`,
  `auth.Principal.Deployment`, the registry's deployment fields with `Keep`
  and `Kept`, the terminal manager's has-record hook, 07-runtime §1.2's
  runner hooks and helpers in `internal/runner/deploy.go` (with a stub
  `Deploy` and NP-14-8's names), and `server.Policy`'s `CodeRoot`,
  `HasDeployment` and `Addressable` (`internal/server/policy.go:18-43`). No
  field or signature that `boot.go` uses changes.
- **Owns.** `internal/util/util.go`, `internal/events/events.go`,
  `internal/auth/auth.go`, `internal/registry/registry.go`,
  `internal/term/term.go` (one field),
  `internal/runner/{runner,deploy,build,sandboxcmd,env,states}.go`,
  `internal/server/policy.go`, `internal/broker/broker.go`,
  `internal/server/policy_zerostate_test.go`.
- **Tests.** `TestNoopPolicyZeroState`; every golden unchanged.
- **Links.** Every declaration M1 consumes. PO-1 to PO-15 hold: nothing reads
  the new fields yet.

#### WP-06 contracts-wiring · M · wave 0.4 · after WP-05, WP-07
- **Scope.** `internal/deployments/plane.go`: the plane in the D63 style,
  with every method boot, the runner, the broker, the server and the terminal
  manager call, answering the zero state. `boot.go` installs the hooks, points
  `OnGrantChange` at `run.ChangedTile` (`internal/boot/boot.go:508-512`), and
  hands the plane to `watchLoop` and `registerDeploymentsAPI`. The ship-dark
  switch (NP-14-5), a tagged `Config` field on the `--tile-assets` precedent
  (`internal/boot/config.go:50`), with `docs/config.md` regenerated. Then
  `boot.go` closes to feature WPs (NP-14-2).
- **Owns.** `internal/deployments/plane.go`,
  `internal/boot/{boot,serve,config,deployments}.go`, `docs/config.md`.
- **Tests.** `TestConfigDoc` (`internal/boot/config_doc_test.go:17`);
  `TestStepsOrder` if a boot step is added (none is planned).

#### WP-07 contracts-routes · M · wave 0.3 · after M0a
- **Scope.** Every route of 11-contract §1.13 as a `RegisterAPI` literal in
  `internal/boot/deployments.go`, answering 501 through `server.WriteError`,
  with its `internal/server/openapi.go` row (§1.15's capability phrases,
  added to `apiInfo`) and its column-0 protocol.md row, marked reserved until
  its handler lands (NP-14-4); one line in `stepServer` registers them.
- **Owns.** `internal/boot/deployments.go`, `internal/server/openapi.go`,
  `docs/protocol.md` (API-fence rows), `internal/boot/boot.go` (one line).
- **Tests.** `TestRouteInventory` (`internal/apicheck/apicheck_test.go:244`),
  `TestOpenAPISpec`.
- **Links.** PO-14.

#### WP-08 harness-infra · M · wave 0.3 · after M0a
- **Scope** (15-test-plan §7.1). The `livereload` and `deployments` passes
  registered (a `require` on an existing line, names in `PASSES` at
  `hack/ui-harness/shots.js:845-849`) as stubs printing `SKIP not
  implemented yet`; the fixtures `apps/reloady` and `apps/deployy` and their
  user entries in `seed.sh`; `HARNESS_ISOLATE=1` in `run.sh` (NP-15-3);
  `testApi().layouts` for `agentTab`, and label-based `hack/menus.test.mjs`
  (NP-15-12).
- **Owns.** `hack/ui-harness/shots.js`, `hack/ui-harness/{seed,run}.sh`,
  `hack/ui-harness/passes/{livereload,deployments,agenttab}.js`,
  `web/frame-testapi.js`, `hack/menus.test.mjs`.
- **Tests.** A fresh-seed full run with no new FAIL (A/B); `make js-test`;
  `make shellcheck`.

### 2.3 M1 — pause live reload

#### WP-10 checkpoint-capture · L · wave 1.1 · after WP-05
- **Scope** (07-runtime §2.1–§2.5; 06-security T1, T2, ledger L1–L2). The
  `Store` skeleton with every method WP-11 and WP-12 fill; the lazy,
  confined store; capture into a quarantine, the pure admission check and
  the retention commit (07-runtime §2.2); ids; the per-tile mutex and rate
  limit.
- **Owns.** `internal/checkpoint/{store,capture,admit}.go`,
  `hostile_linux_test.go`, unit tests.
- **Tests.** `TestCheckpointContent`, `TestCheckpointIDs`,
  `TestCheckpointIncremental`, `TestCaptureCapsRefuseHugeTree`,
  `TestStoreUsesConfineOnly`, `TestCaptureDirectModeHardening`,
  `TestCapturePathNamesNeverArgv`, `TestAdmissionRefusesBadTrees`,
  `TestCheckpointRateLimit`; confined `TestCaptureIgnoresTileGitConfig`,
  `TestCheckpointStorePrivate`.
- **Links.** `Store` → WP-11, WP-12, WP-14, WP-23. PO-7 (the store is lazy).
  The integrator adds `./internal/checkpoint/` to `make integration`.

#### WP-11 checkpoint-materialize · M · wave 1.2 · after WP-10
- **Scope** (07-runtime §2.6, §2.8; 06-security T2, L3, L7). Confined
  `read-tree` and `checkout-index` into `.xbin/deploy/<CompKey>/.tmp-*`, write
  bits dropped, an atomic rename to the full tree id, single-flight, stale
  `.tmp-*` swept; retention by 07-runtime §2.8 (artifacts are WP-16's); GC,
  in one confined run, never evicts a tree a running generation binds.
- **Owns.** `internal/checkpoint/{materialize,gc}.go`,
  `materialize_linux_test.go`, unit tests.
- **Tests.** `TestMaterializeAtomicReadOnly`, `TestCheckpointGC` (with
  `TestGCKeepsTreesOfRunningGenerations`); confined
  `TestMaterializeSymlinkEscape`.
- **Links.** `Store.Materialize`, `Store.GC` → WP-14, WP-16.

#### WP-12 checkpoint-log-remote · L · wave 1.2 · after WP-10
- **Scope** (07-runtime §2.3, §2.7, §2.9; 11-contract §1.10–§1.12, §10.3;
  06-security T15, L4, L6). The deploy log's commit chain; the fetch refs
  and `update-server-info`; `ServeFetch` (allow-listed paths, symlinks
  refused: NP-06-18); the diff (`--no-ext-diff --no-textconv`, its caps); the
  drift count (NP-13-12).
- **Owns.** `internal/checkpoint/{log,remote,diff,drift}.go`,
  `fetch_linux_test.go`, unit tests.
- **Tests.** `TestDeployLog`, `TestFetchRemoteServesOnlyGitFiles`,
  `TestFetchRemoteRefusesSymlinks`, `TestCheckpointDiffNoExternalTools`;
  confined `TestCheckpointFetchRemoteFlowH`.
- **Links.** `Log`, `AppendLog`, `Diff`, `Drift`, `ServeFetch` → WP-14,
  WP-15, WP-20. Nothing is written to a tile's repository (NP-12-7).

#### WP-13 plane-record · M · wave 1.1 · after WP-06
- **Scope** (11-contract §10.1; 06-security T11, C10; NP-12-9). The record;
  a load that validates and fails closed; atomic writes that keep unknown
  fields; the owner ref; an in-memory index for O(1) lookups; the zero state
  synthesized, writing nothing.
- **Owns.** `internal/deployments/{record,index,plane}.go`, unit tests.
- **Tests.** `TestRecordLocation`, `TestRecordValidationFailsClosed`,
  `TestRecordTilePathMismatchRefused`, `TestRecordInvariantsRandomized`
  (record half).
- **Links.** The record and the index → WP-14, WP-18, WP-20, WP-22. PO-7,
  PO-8 (boot reads records, never rewrites them).

#### WP-14 plane-ops · L · wave 1.2, merged after WP-11, WP-12, WP-16 · after WP-13
- **Scope** (05-model §5, §10; 07-runtime §8.2–§8.7; 11-contract §1.2, §1.4,
  §1.6; 06-security T9, C4). The M1 operations onto `main`, with 07-runtime
  §8.2's pointer rules, the queue and `seq`, `expect`, `confirm`, `dryRun`;
  one authorize function over the M1 rows (`CanTerminalTileVia`,
  `internal/auth/sessiongate.go:7`; `mayManageTile`,
  `internal/broker/orgsapi.go:427`), re-checked at commit; P18's refusals
  before any change; a failed attempt to pause live reload (NP-02-11);
  opt-out; the ship-dark switch; `deployments` events; deploy-log writes;
  the op registry (NP-14-3).
- **Owns.** `internal/deployments/{ops,queue,authz,events,dispatch,plane}.go`,
  unit tests.
- **Tests.** For the M1 ops: `TestDeployPlaneOperations`,
  `TestDeployAuthzMatrix`, `TestViewAsRefusedEveryOp`,
  `TestOperationsNeverWriteWorkTree`, `TestRecordInvariantsRandomized`, the
  plane half of `TestNonIsolatedRefusesBackendDeployments`. Also
  `TestNoTerminalCannotOperate`,
  `TestOwnRuntimePrincipalsCannotOperate`,
  `TestAuthorizationRecheckedAtCommit`, `TestDeployLogAudit`,
  `TestOptOutReturnsToZeroState` (plane).
- **Links.** The op registry → WP-15, WP-52, WP-53. PO-15.

#### WP-15 plane-api · M · wave 1.2, merged after WP-14 · after WP-07
- **Scope** (11-contract §1). Generic handlers: decode a route's body into its
  op's request type, call the registry, answer `State`, `DeployEntry` or
  `Impact`; an unregistered op answers 501. `GET /deployments` with the
  reader/writer split (NP-11-19) and `features` (NP-14-4); the log (with
  `wait`); the diff (text/x-diff or `stat=1`, `X-XBin-Checkpoint-*`);
  `GET /checkpoints/{rest...}` → `ServeFetch` behind NP-11-15's gate.
- **Owns.** `internal/boot/deployments.go`, `internal/boot/deployments_test.go`.
- **Tests.** `TestDeploymentStateReadIsPure`, `TestFetchRemoteReadGate`, a
  handler test per route.
- **Links.** Protocol prose for WP-29. PO-14.

#### WP-16 runtime-pinned · L · wave 1.2 · after WP-01, WP-05
- **Scope** (07-runtime §1, §7, §8.1–§8.7, §11, §12). Every restart path
  builds from `CodeFor` (P9); per-checkpoint artifacts, current plus three
  kept (NP-02-3); `Runner.Deploy` on the build turn, reporting through
  `commit` and `progress`, never `build-*` (NP-11-21); restart as a deploy of
  the same checkpoint; alwaysOn, VM and entry checks read the view; no
  checkpoint starts without isolation, and lost isolation holds pinned
  backends (06-security C7); a failed attempt to pause live reload
  (NP-02-11).
- **Owns.** `internal/runner/{runner,deploy,states,alwayson,vm,inspect}.go`,
  `pinned_test.go`.
- **Tests.** Seam rows 16–30 and 38, `TestArtifactPerCheckpoint`,
  `TestNonIsolatedRefusesBackendDeployments` (runner half),
  `TestIsolationLossHoldsPinnedBackends`, `TestDeployQueueCoalesces` (= row
  28).
- **Links.** `Deploy`, `Artifact`, the roots in use → WP-11, WP-14. PO-3,
  PO-11, PO-12; rows 1–15 unchanged.

#### WP-17 runtime-checkpoint-build · L · wave 1.2 · after WP-01, WP-05, WP-S1
- **Scope** (07-runtime §3, §10.4's code bind; 06-security L8, L9, T17).
  Checkpoint builds through `DirFrom`, with 07-runtime §3.1's binds
  (NP-07-4) and `build.json`; the launch spec binds `{Src: <code root>, Dst:
  c.Dir, RO: true}` for pinned code; the env layer hashes the view's `setup`
  and binds the code root; `gcEnvLayers` keeps every referenced hash.
- **Owns.** `internal/runner/{build,sandboxcmd,env}.go`,
  `build_checkpoint_linux_test.go`, `launch_checkpoint_linux_test.go`.
- **Tests.** `TestLaunchSpecBinds`, `TestEnvLayerGCKeepsReferenced`,
  `TestSetupLayerSeesCheckpointNotWorkTree`, `TestConfinedGoBuild` (its
  checkpoint case); confined `TestConfinedBuildOfCheckpointAtCanonicalPath`,
  `TestPinnedBackendSeesCheckpoint` (code half, with a VM variant that skips
  without KVM).
- **Links.** PO-11: the live reload target's spec equals the golden.

#### WP-18 registry-view · M · wave 1.1 · after WP-05
- **Scope** (07-runtime §5; 05-model §6). `View` parses the checkpoint's
  `xbin.json` through `OpenBeneath`, takes deployment-level fields from it
  and tile-level fields from the work tree, caches by (path, tree, manifest
  hash); a pinned view's `uses` is the union of both, for env (NP-07-3);
  `resolveNative` checks the code root; `Rescan` keeps a record-bearing tile
  whose work-tree manifest is missing or broken, with the primary
  checkpoint's tile-level fields and the error surfaced. P22: the view
  carries the deployment's resource declarations (the checkpoint's
  `scope.json` when the tile roots its scope), and `Provision`
  (`internal/broker/resources.go:36`, called at `internal/boot/serve.go:166`)
  provisions a pinned primary from them, keeping resources it no longer
  declares.
- **Owns.** `internal/registry/{deployview,native,registry}.go`,
  `internal/broker/resources.go` (`Provision`), unit tests.
- **Tests.** `TestManifestFieldSplit`, `TestPinnedPrimarySurvivesMissingManifest`,
  `TestCheckpointManifestReadNoFollow`, and a new `TestProvisionFollowsCode`
  (P22).
- **Links.** The view → WP-16, WP-19. The zero-state view is the registry's
  own pointer (NP-13-3).

#### WP-19 serving-pinned · M · wave 1.2 · after WP-18
- **Scope** (07-runtime §4.1, §4.2, §4.5; 06-security T2, T13).
  `internal/server/deployserve.go` and its call sites in the static, strict
  and native openers, with `inject`, `native` and `HasIndex` from the view;
  the proxy's template and no-backend gates read the primary's view, since
  `runtime` is deployment-level; `componentInfo`'s live-reload summary.
- **Owns.** `internal/server/{deployserve,static,tileassets,native,api}.go`,
  `internal/proxy/proxy.go`, tests.
- **Tests.** `TestPinnedPrimaryServesCheckpoint`,
  `TestPinnedServingUsesOpenBeneath`, `TestNativeDocumentFollowsPrimary`,
  `TestProxyRuntimeFromDeployment` (a work-tree `runtime` edit never changes
  what pinned code runs as); `TestLegacyInjectionUnchanged` unchanged.
  Dropped with cgi (PRE-5): `TestCgiRefusedForPinnedDeployments`,
  `TestNoDirectExecFlagsCGIHandler`.
- **Links.** PO-1, PO-10.

#### WP-20 watch-gate · M · wave 1.2 · after WP-06, WP-12, WP-13
- **Scope** (07-runtime §6; NP-13-12). The pure `liveTargets` in
  `internal/boot/liveroute.go`; the six-line change in `watchLoop`
  (`internal/boot/serve.go:161`, its loop at `:178-180`); while live reload
  is paused, a drift count debounced to one per tile every 2 s, and the
  `deployments` op `work-tree`.
- **Owns.** `internal/boot/{serve,liveroute}.go`, `liveroute_test.go`.
- **Tests.** `TestReloadPlan`, `TestWatchLoopZeroStateNoStoreIO`,
  `TestPendingCountAfterDroppedBatch`, a 1 000-path batch benchmark within 5 %
  of today (07-runtime §13.1).
- **Links.** PO-5, PO-12.

#### WP-21 events-status · S · wave 1.1 · after WP-05
- **Scope** (11-contract §3.3–§3.4; 06-security T7). `handleEventsWS`
  (`internal/server/server.go:592-612`) filters the `deployments` type: ops
  `record` and `deploy` to the tile's readers, the rest to writers, never
  other tiles' element principals. The status plane clears a deployment's
  status when a `deployments` op `deploy` swaps, since checkpoint deploys
  emit no `build-start` (`internal/obs/status.go:129-143`). `/ws/term/env`
  only gains fields.
- **Owns.** `internal/server/server.go`, `internal/obs/status.go`, tests.
- **Tests.** `TestDeploymentEventsFiltered` (`deployments` rows),
  `TestTermEnvAdditive`, a status test on `testPlane`.
- **Links.** PO-5.

#### WP-22 term-remote · S · wave 1.1 · after WP-05
- **Scope** (11-contract §1.12; NP-12-7, NP-13-8). A session opened while its
  tile has a record gets `GIT_CONFIG_COUNT=4`: today's two pairs
  (`internal/term/term.go:787-792`, now in `sandboxenv.go`) plus
  `remote.xbin-deploy.url` and `.fetch`. Zero-state sessions keep two.
- **Owns.** `internal/term/sandboxenv.go`, `internal/term/remote_test.go`.
- **Tests.** `TestFetchRemoteInjection`, `TestTerminalMasksDeploymentState`;
  `TestSessionEnvZeroState` unchanged. `internal/term`'s tests build `./cmd/bx`
  (`internal/term/agent_test.go:30-49`).
- **Links.** PO-3.

#### WP-23 backup-record · M · wave 1.2 · after WP-10, WP-13
- **Scope** (08-data §11.1, §11.2, §11.4; 06-security T11, L13; NP-12-10).
  Tar prefixes `deployments/record.json` and `deployments/checkpoints/…`;
  restore refuses an archive naming another tile before writing anything,
  installs the record only when none exists, rebuilds the store from its
  objects in confine (never `config`, `hooks/`, `info/` or alternates), and
  keeps archived refs in a restore-only namespace; `Schema` stays 1.
- **Owns.** `internal/broker/backup_deploy.go`, `internal/broker/backup.go`
  (seams), `internal/backup/backup.go`, tests.
- **Tests.** `TestBackupCoversDeployments` (record, store),
  `TestRestoreRefusesForeignManifest`; confined
  `TestRestoreRebuildsStoreConfig`.
- **Links.** PO-9; older xbinds skip the new prefixes (12-compat §5.1).

#### WP-24 deploy-state · M · wave 1.1
- **Scope** (10-ux §2, §4, §12, §14.5; NP-10-3). `web/deploy-state.js`, which
  imports nothing: (state, permission flags) → view model (chip, offer,
  pending count, disabled, protected and view-as reasons), and every §12
  string M1 needs. Its exports are fixed in the first commit, for WP-25.
- **Owns.** `web/deploy-state.js`, `hack/deploy-state.test.mjs`.
- **Tests.** `hack/deploy-state.test.mjs` (M1 rows, the zero-state row first).
- **Links.** A new `/vendor/` module (compat rules 3, 4).

#### WP-25 terminal-window · L · wave 1.2, merged after WP-15 · after WP-24
- **Scope** (10-ux §2, §5, §9, §14; 13-surfaces §4.16). `frame-deploy.js`
  (state reloaded after every events reconnect, 12-compat NC-5); bx-frame's
  one-line hooks; the chip and the Reload now offer in the bar and the tools
  row; the launcher banner; bx-terminal's `note(text)` (NP-10-1); the test
  names; the `livereload` pass; `viewAs`'s attempts to pause live reload
  and to deploy.
- **Owns.** `web/{frame-deploy,bx-frame,frame-titlebar,frame-launcher,bx-terminal,frame-testapi}.js`,
  `hack/ui-harness/passes/{livereload,viewas}.js`.
- **Tests.** Harness `livereload` (parts 0–6), `viewAs`; `agentTab`,
  `reloadFocus`, `windows` unchanged; `js-check`, `theme-check`.
- **Links.** PO-10; the window pref never persists an unknown layout
  (NP-12-3).

#### WP-26 bx-live-reload · L · wave 1.2, merged after WP-15 · after WP-07
- **Scope** (11-contract §9; 10-ux §8; 12-compat §4.2). `bx live-reload`;
  deploy and roll back onto `main`; `deployclient.go` (feature detection, the
  `Can` pre-check, `--dry-run`, `--yes`, `--json`, waiting, exit codes 3–6);
  registration through `moreCmds` (`cmd/bx/native.go:28-32`), usage on the
  `+nativeUsage` line (`cmd/bx/main.go:174`); `bx logs` through `GET /logs`.
- **Owns.** `cmd/bx/{livereload,deploy,deployclient,status,main}.go`,
  `cmd/bx/deploy_test.go`.
- **Tests.** `TestParseDeploymentArgs`, `TestBxSaysWhereSavesGo` (M1),
  `TestBxOldXbind`, `TestBxJSON`; `TestBxTodayInvocationsUnchanged`
  unchanged.
- **Links.** /docs/bx.md text for WP-29. PO-13.

#### WP-27 itest-infra · M · wave 1.1
- **Scope** (15-test-plan §5.1–§5.2; NP-15-2). `startIsolatedDaemon(t, opts)`
  (`--isolate --rootfs $XBIN_TEST_ROOTFS`, optional auth and ingress,
  `d.restart(t)`, a process-group kill, an honest skip), and `writeProbe`.
- **Owns.** `test/isolated_test.go`, `test/probe_test.go`.
- **Tests.** A smoke test of a probe on the isolated daemon.

#### WP-28 itest-m1 · L · wave 1.3 · after every M1 feature WP
- **Owns.** `test/{livereload,pinned,deployments,flowc,deploystate_boot,latency}_test.go`,
  `internal/boot/deploystate_test.go`.
- **Tests.** `TestLiveReloadPauseStatic` (=
  `TestStaticTilePauseLiveReloadWithoutIsolation`),
  `TestLiveReloadPauseRefusedWithoutIsolation`, `TestLiveReloadPauseRace`,
  `TestLiveReloadPauseGo`, `TestPinnedThroughRestartPaths`,
  `TestRollBackBound` (`main`), `TestFailedDeployInvisible`,
  `TestAgentBxLiveReload`, `TestDeploymentStateBootsTwice`,
  `TestDeploymentStateBootsTwiceInProcess`, `TestOptOutReturnsToZeroState`
  (integration), `TestLatencyLifecycleOps` (M1 rows).
- **Links.** `TestFailedDeployInvisible`'s event tape → WP-60.

#### WP-29 docs-m1 · L · wave 1.3, drafting from 1.2 · after every M1 feature WP
- **Scope** (13-surfaces §4.18's M1 rows; 12-compat §8, §9; 10-ux §10.2).
  [/docs/protocol.md](/docs/protocol.md)'s M1 prose, routes, event and paths;
  [/docs/elements.md](/docs/elements.md), [/docs/isolation.md](/docs/isolation.md),
  [/docs/bx.md](/docs/bx.md), [/docs/frontend-kit.md](/docs/frontend-kit.md),
  [/docs/compat.md](/docs/compat.md), [/docs/maintenance.md](/docs/maintenance.md);
  the save-to-reload statements in `docs/overview/{01,03,04,09,14,15}-*.md`,
  `docs/index.md` and `docs/getting-started.md`; `workspace-template/AGENTS.md`;
  the builder page `docs/tile-deployments.md` (NP-14-10);
  `TestDeploymentWording` (NP-15-9).
- **Owns.** Those files, `internal/docscheck/wording_test.go`.
- **Tests.** `TestDeploymentWording`, `TestRelativeLinksResolve`,
  `TestDecisionIDsResolve`, `TestEmbeddedAssets`.
- **Links.** "No deploy step" stays true for tiles that never opt in.

### 2.4 M2 — tile deployments

#### WP-30 m2-interfaces · M · wave 2.0 · after the M1 exit
- **Scope** (NP-14-11; the D109 lesson). Every cross-WP M2 interface, once,
  answering today's: the broker hooks the plane installs (a tile's
  deployments and primary, a principal's addressed deployment, the active
  registrations, the edge verdict); `resKeys`'s signature; the obs plane's
  deployment input; the terminal-target hook; the M2 ops' request types; the
  shared broker fixture of 15-test-plan §3.7, with a users store; a `boot.go`
  amendment installing the hooks.
- **Owns.** `internal/broker/{broker,deploydata}.go` (declarations),
  `internal/broker/deploy_fixture_test.go`, `internal/obs/obs.go`,
  `internal/term/target.go`, `internal/deployments/{plane,m2types}.go`,
  `internal/boot/boot.go`.
- **Tests.** 09-fabric §10's `TestZeroStateRoute`; every golden unchanged.

#### WP-31 qualifier · S · wave 2.1 · after WP-30
- **Scope** (11-contract §2.1, §2.2, §2.4; NP-12-1). `ResolveRef`: a `+`-free
  fast path; a split only for a tile with a record; a component or on-disk
  path at the full candidate wins; unknown names 404; nested components never
  entered.
- **Owns.** `internal/registry/qualifier.go`, `qualifier_test.go`.
- **Tests.** `TestResolveDeploymentQualifier`, with PO-1's rows.
- **Links.** `ResolveRef` → WP-36, WP-37, WP-38. PO-1.

#### WP-32 auth-claims · M · wave 2.1 · after WP-30
- **Scope** (11-contract §7.1–§7.4; 09-fabric §3). `RegisterInstance(token,
  path, dep)` and the instance map by (tile, deployment); `MintTerminal`
  with a target; the sixth frame-token field under the name rule (never for
  `main`; 4- and 5-field forms mean `main`); renewal copies the claim; old
  signatures stay as `main` wrappers.
- **Owns.** `internal/auth/{deployment,auth,frametoken}.go`,
  `deployment_test.go`.
- **Tests.** `TestPrincipalDeploymentZeroValue`,
  `TestInstanceTokenCarriesDeployment`, `TestFrameTokenDeploymentClaim` (=
  `TestFrameTokenBoundToDeployment`), `TestTerminalTokenTarget` (=
  `TestTerminalTargetBinding`, `TestTerminalTokenCannotMintProtectedPrimaryToken`);
  `TestLegacyFrameTokenUpgrade` unchanged.
- **Links.** Claims → WP-33, WP-36, WP-37, WP-51. PO-6.

#### WP-33 runner-deployments · L · wave 2.1, merged after WP-32 · after WP-30
- **Scope** (07-runtime §1.2–§1.3, §8.5, §8.8, §9; NP-07-7). The runner API
  of 07-runtime §1.2 keyed by (tile, deployment), under NP-14-8's names;
  per-deployment sockets, logs and `XBIN_DEPLOYMENT` (the role rule);
  `RegisterInstance` with the deployment; `eventComp` per R-1; reassignment
  restarts, new primary first; the admission caps (3, 24, 12 running).
- **Owns.** `internal/runner/{runner,deploy,states,inspect,rundir}.go`,
  `deployments_test.go`.
- **Tests.** Seam rows 31–34, 37; `TestBackendEnvPerDeployment`,
  `TestInstanceTokenRegisteredWithDeployment`, `TestLogPathsPerDeployment`,
  `TestRunDirPerDeployment`, `TestEnsureCallSitesPassPrimary`, 09-fabric §10's
  `TestDrainAuthority`.
- **Links.** Per-deployment state → WP-34, WP-37, WP-52, WP-54, WP-S4. PO-2,
  PO-3, PO-11.

#### WP-34 runner-edges · L · wave 2.2 · after WP-33, WP-S2, WP-S3
- **Scope** (07-runtime §10.2, §10.3, §11, §12; 09-fabric §5.7–§5.8;
  NP-02-12). Effective alwaysOn and backoff per deployment; VM reservations
  for the tile with headroom, `stopFirst` per deployment; `DialInto` and
  `ensureProvider` resolve the primary; non-primary generations never join a
  roster or splice; the leaf chooser (flat while `main` is alone), with each
  deployment's caps defaulting to the tile's (P22); stats from `ProcsTree`.
- **Owns.** `internal/runner/{runner,alwayson,vm,ingress,netmux,stats}.go`,
  tests.
- **Tests.** Seam rows 35–36; `TestCgroupLeafPerDeployment` (=
  `TestLeafPerDeployment`), `TestVMReserveOwnerIsTile`,
  `TestNonPrimaryNeverJoinsProviderRoster`; 09-fabric §10's
  `TestDialIntoPrimary`, `TestTerminatorDoorPrimaryOnly`,
  `TestNonPrimaryAlwaysOn`, `TestEdgeStreamDial`.
- **Links.** PO-11: zero-state and main-only tiles keep the flat leaf.

#### WP-35 launch-spec · M · wave 2.1 · after WP-30
- **Scope** (07-runtime §10.4; 08-data §5; 06-security T17, T18, NP-06-6,
  T10 item 3). `resourceBinds(env, root, remap)`, failing a start on an
  unmapped path; per-deployment env-layer logs and GC keep set; a protected
  primary's build products kept apart; one build limiter, primary first.
- **Owns.** `internal/runner/{binds,sandboxcmd,env,build}.go`,
  `resourcebinds_deploy_test.go`.
- **Tests.** `TestNonPrimaryResourceBindsNeverPrimary`, `TestResourceBinds`
  (remap rows), `TestProtectedBuildProductsSeparated`,
  `TestProtectedPrimaryLayerNotShared`, `TestVMBackendNamespaceBinds`;
  confined `TestPinnedBackendSeesCheckpoint` (data half).
- **Links.** PO-11: `main`'s binds keep `Src == Dst`.

#### WP-36 deployment-urls · L · wave 2.2 · after WP-31, WP-32
- **Scope** (11-contract §2.3, §2.5–§2.7, §7.2; 07-runtime §4.3–§4.6;
  NP-09-11). Deployment URLs on the static and tokens planes, with the write
  gate, the D4 additions and the claim; `mayMintFrameToken` never crosses
  deployments; `?native=1` per deployment; origins mode refuses until WP-38.
- **Owns.** `internal/server/{static,tileassets,deployserve,native}.go`,
  `deployurl_test.go`.
- **Tests.** `TestQualifiedURLRouting`, `TestDeploymentURLGate` (with
  `TestDeploymentURLRefusesOtherTiles`), `TestFrameTokenClaimInjected`,
  `TestMayMintFrameTokenNeverCrossesDeployments` (=
  `TestMintRefusesCrossDeployment`), `TestNonPrimaryDocumentAlwaysSandboxed`.
- **Links.** PO-1, PO-10; `TestLegacyInjectionUnchanged` unchanged.

#### WP-37 proxy-routing · M · wave 2.2 · after WP-31, WP-32, WP-33
- **Scope** (11-contract §2.3, §4; 09-fabric §4; P12). `ResolveRef` in the
  proxy; the deployment choice and the cross-deployment refusal of 09-fabric
  §4; `EnsureDeployment` at `internal/proxy/proxy.go:182`;
  `X-XBin-Deployment` on non-primary calls and responses (NP-11-12); the
  edge verdict for non-primary callers; ingress to the primary.
- **Owns.** `internal/proxy/{proxy,ingress}.go`, `deploy_test.go`.
- **Tests.** `TestProxyQualifiedTarget`, `TestIdentifyDeploymentHeader` (=
  `TestDeploymentHeaderOnlyFromXbind`), `TestIngressForwardPrimaryOnly` (=
  `TestIngressNeverReachesNonPrimary`), 09-fabric §10's
  `TestCrossDeploymentRefused`.
- **Links.** PO-4; `TestIdentifyZeroState` unchanged.

#### WP-38 origins-mode · L · wave 2.3 · after WP-36 · gated by R-6
- **Scope** (11-contract §2.6, §7.5; NP-12-4, NP-11-18). Name-based labels for
  non-main deployments; `x2`/`c2` tickets and cookies; a label → (tile,
  deployment) lookup at every `TileHostID` comparison; navigation to the right
  origin; `TilePrincipal` bound to the deployment.
- **Owns.** `internal/server/{tileorigin,tilenav}.go`,
  `internal/auth/{assettoken,tilebinding}.go`, tests.
- **Tests.** `TestOriginLabelPerDeployment`.
- **Links.** `main`'s `x1` and `c1` stay byte-identical (PO-6).

#### WP-39 data-keys · L · wave 2.1 · after WP-30
- **Scope** (08-data §2, §3; NP-08-1, NP-08-11). `resKeys` with the
  injective `escS`, the `.deployments` level, a bbolt file per namespace;
  every `util.ScopeKey` site and hand-built bucket of 08-data §3.2 routed
  through it; resource names validated everywhere; a guard against new
  `util.ScopeKey(` or `"res:" +` in `internal/broker` outside
  `deploydata.go`.
- **Owns.** `internal/broker/{deploydata,broker,resources,resenc_wire,backup,diskmon,resusage}.go`
  (key sites), tests.
- **Tests.** `TestDeploymentKeysDisjoint`, the guard (proposed name
  `TestNoAdHocResourceKeys`), the resource-name test.
- **Links.** `resKeys` → WP-40 to WP-44. PO-2; `TestZeroStateKeys` unchanged.

#### WP-40 data-access · L · wave 2.2 · after WP-39
- **Scope** (08-data §3.6, §4–§6; 09-fabric §5.10). `EnvFor(view, dep)`:
  identical values plus the remap; `kvAccess`, `blobAccess` and bus publish by
  the principal's deployment (08-data §4.2), the workspace scope never split;
  `busFilter` by namespace; non-main volumes mounted on first use;
  `EncryptionHold` per namespace; `SealResources` stops every deployment;
  `cap:containers` changes re-`Ensure` mounted non-main volumes; each
  deployment provisions what its own code declares, in its own namespace
  (P22).
- **Owns.** `internal/broker/{resources,resenc_wire}.go`,
  `internal/resenc/resenc.go`, `internal/broker/deploy_env_test.go`.
- **Tests.** `TestEnvForPerDeployment`, `TestKVNamespacePerDeployment`,
  `TestMultiTileScopeNamespaces`, `TestBusPublishStaysInNamespace` (=
  `TestNonPrimaryBusPublishIsolated`), 09-fabric §10's `TestBusNamespaceMatch`.
- **Links.** The remap → WP-35. PO-3.

#### WP-41 data-namespaces · M · wave 2.2 · after WP-39
- **Scope** (08-data §6.3–§6.4, §8.2's holds, §9; NP-08-7, NP-08-14,
  NP-02-10). `ns.json` states and history; the namespace hold and write gate;
  reset (unmount, verify, remove, abort when busy, `vault: true`); deletion by
  the last claimant only; the sweep, orphans, the 14-day grace.
- **Owns.** `internal/broker/deployns.go`, tests.
- **Tests.** `TestResetAndRemoveScope`; 08-data §14's reset, multi-tile and GC
  rows.

#### WP-42 data-seed · L · wave 2.3 · after WP-40, WP-41
- **Scope** (08-data §8; 06-security T8, L10, L11; NP-08-5, NP-08-6). A
  confirmed act of a tile manager of every claimant, by a human credential;
  preflight (vault, gocryptfs, quota, reserve, online or stopped); kv spooled
  and re-encoded in bounded transactions; sqlite through the backup API and
  `filesystem` through rsync, both confined; single-tenant copy and re-wrap;
  blob through `OpenBeneath`; verification, `partial` on failure, provenance.
- **Owns.** `internal/broker/deployseed.go`, `deploy_seed_test.go`,
  `seed_linux_test.go`.
- **Tests.** `TestSeedManagerOnly`, `TestSeedNeverWritesPrimary`,
  `TestSeedCopyConfinedNoFollow`, `TestSharedScopeSeedNeedsEveryTile`;
  confined `TestSeedSqliteConfined`; 08-data §14's seed rows.

#### WP-43 data-vault · M · wave 2.2 · after WP-39
- **Scope** (08-data §10; 06-security T5, NP-06-9). `vaultPath(comp, dep)`;
  access by the addressed deployment, D30 re-checked; computed placeholders;
  vault copy (tile manager, human credential, keys-only audit);
  `migrateVaults` walks `.deployments/`; the reassignment preflight; a
  protected primary's vault writes are tile managers' acts.
- **Owns.** `internal/broker/{deployvault,vault}.go`, tests.
- **Tests.** `TestVaultPerDeployment`, `TestVaultCopyManagerOnly`,
  `TestProtectedPrimaryVaultWritesManagerOnly`.

#### WP-44 data-backup-quota · L · wave 2.3 · after WP-39, WP-41
- **Scope** (08-data §11, §12; NP-12-10; 06-security T10 item 4). Deployment
  archives (schema 2, key `.deployments.<CK>.<d>`); the primary archived on
  every backup when it isn't `main`; opt-in non-primary archives with their
  own retention; replace-restores into namespaces; registrations restored for
  existing deployments; offload of every namespace; a quota bucket per
  non-main namespace with a quota share defaulting to the tile's (P22),
  non-primary blocked first on low disk, a per-tile quota for stores,
  artifacts and trees.
- **Owns.** `internal/broker/{backup_deploy,backup,backup_cron,diskmon,resusage}.go`,
  `internal/backup/backup.go`, tests.
- **Tests.** `TestBackupCoversDeployments` (M2), `TestDiskQuotaCountsDeploymentData`,
  `TestRestoreRevalidatesRegistrations`; 08-data §14's archive and quota rows.
- **Links.** PO-9: the main archive means what it means today.

#### WP-45 offload-fix · M · wave 2.4 · after WP-44 · gated by R-3
- **Scope** (08-data §9.4, §11.6; NP-08-10; side findings #3, #5). The backup
  walk opens through `OpenBeneath` and reports lossy resources; headers carry
  mode and mtime; ciphertext removed after confirmed PUTs; `MountEncrypted`
  skips offloaded scopes.
- **Owns.** `internal/broker/{backup,resenc_wire}.go`,
  `internal/backup/backup.go`, tests.
- **Tests.** 08-data §14's offload and restore rows.
- **Links.** A deliberate P5 exception: ships only if ratified.

#### WP-46 leftovers-plus · M · wave 2.3 · after WP-39, WP-49
- **Scope** (05-model §11; 08-data §9.3; 12-compat §7, NP-12-11; NP-13-7,
  13-surfaces Divergence 5). `pathLeftovers` lists the record, store, non-main
  vaults, registration directories and namespaces at or under a path, the
  owner exempt; the narrow `<P>+<N>` collision refused for every creator,
  before the admin early return; any other `+` warned now and refused for
  non-admins next release; creation clears a path's deployment state first.
- **Owns.** `internal/broker/{policy,create}.go`, `deploy_life_test.go`.
- **Tests.** `TestPathLeftoversIncludeDeploymentState`,
  `TestPlusReservedInNewTilePaths`; `TestNewTilePathRule` unchanged.
- **Links.** The one warned transition; a changelog hand-off.

#### WP-47 edge-policy · L · wave 2.2 · after WP-30, WP-39
- **Scope** (09-fabric §5, NP-09-1 to NP-09-6, NP-09-10; 08-data §7; P3,
  P11, P12, P23). `internal/broker/edgepolicy.go`, as 09-fabric §5
  specifies, with every edge that can't be read-clamped blocked (P23);
  `broker.Policy`'s self rule compares deployments and non-primary results
  pass the verdict; `allowRes` clamps `res:`; `httpBindingRole` feeds the
  clamp; grant, binding and interface-instance restarts fan out to every
  deployment.
- **Owns.** `internal/broker/{edgepolicy,broker,netfn}.go`,
  `deploy_edges_test.go`.
- **Tests.** `TestGrantedRoleReadClamp` (= `TestReadClampOnEveryEdge`),
  `TestEdgePolicyBlock` (= `TestEdgeBlockFailsClosed`),
  `TestUnclampableEdgeDefaults`, `TestNetEdgeDefault`,
  `TestEdgePolicyNeverTouchesPrimary`, `TestSelfCallRouting` (=
  `TestSelfCallStaysInDeployment`), `TestXbinGrantRefusedWithNonPrimary`,
  `TestNonPrimaryPrincipalNeverAdmin`; 09-fabric §10's `TestEdgeBlockWins`,
  `TestEdgeUnknownValueBlocks`, `TestEdgeCapabilities`, `TestEdgePolicyCallers`.
- **Links.** The primary equals the goldens (12-compat Z3). `netfn.go` (1131
  of 1170) takes at most 15 lines across WP-47 and WP-50.

#### WP-48 route-classes-events · M · wave 2.3 · after WP-32, WP-33
- **Scope** (09-fabric §6's enforcement; NP-09-4, NP-06-1, NP-04-8,
  NP-13-11; 11-contract §3.2–§3.4; R-1). One table classifies every
  `RegisterAPI` pattern; `handleAPI` refuses unclassified routes to
  non-primary tile principals (403), and a guard over the route inventory
  keeps the table complete; the event filter for non-primary events; non-main
  bus events reach admins and principals of that namespace only.
- **Owns.** `internal/server/{server,deployclass}.go`,
  `internal/apicheck/deployclass_test.go`, `internal/server/deployevents_test.go`.
- **Tests.** 09-fabric §10's `TestDeploymentRouteClasses`,
  `TestNonPrimaryUsesNewEventTypes`, `TestDeploymentEventsFiltered`
  (non-primary rows), `TestNonPrimaryBuildErrorNotBroadcast`.
- **Links.** PO-5; zero-state tiles never meet the class check.

#### WP-49 dormant-cron-bus · L · wave 2.1 · after WP-30
- **Scope** (09-fabric §6, §7; 11-contract §10.2; P13; NP-09-9, NP-09-16,
  NP-09-18). `internal/broker/dormant.go` (the per-deployment files, the
  active set, the panel's list); dormant cron jobs and bus subscriptions,
  gated at fire and delivery; run now; `assignOwner` drops dormant stores.
- **Owns.** `internal/broker/{dormant,cron,bussubs,create}.go`,
  `deploy_dormant_test.go`.
- **Tests.** `TestDormantRegistrations` (cron and bus subtests =
  `TestNonPrimaryCronDormant`, `TestNonPrimaryBusSubDormant`),
  `TestDeliveriesSwitch` (= `TestDeliveriesSwitchManagerOnly`), `TestRunNow`,
  `TestOlderBinaryIgnoresDeploymentFiles` (12-compat PO-9's unit test).
- **Links.** The active set → WP-50, WP-53. PO-9.

#### WP-50 dormant-rest-obs · L · wave 2.3 · after WP-47, WP-49
- **Scope** (09-fabric §6's rows, NP-09-12, NP-09-13; 11-contract §3.3, §8;
  NP-13-5). Dormant interface instances and ingress hosts; held
  notifications ("would notify"); per-deployment status on `deployments` op
  `status`, never a `status` event; `/logs?deployment=` with its echo;
  per-deployment prefs.
- **Owns.** `internal/broker/{netfn,ingressfn}.go`, `internal/push/api.go`,
  `internal/obs/{status,logs,prefs}.go`, tests.
- **Tests.** `TestDormantRegistrations` (its interface-instance and
  ingress-host subtests = `TestNonPrimaryIfaceInstancesNeverRouted`,
  `TestNonPrimaryIngressHostsNeverRouted`), `TestNotifyNotPushed` (=
  `TestNonPrimaryNotifyNeverPushed`), `TestStatusPerDeployment` (=
  `TestNonPrimaryStatusNamespaced`), `TestLogsPerDeployment`, 09-fabric §10's
  `TestIfaceInstanceFollowsPrimary`.
- **Links.** PO-9; old shells never see non-primary status.

#### WP-51 term-target · L · wave 2.2 · after WP-32
- **Scope** (P24; 11-contract §7.4's wire; 09-fabric §2.3). A session's
  target, fixed for its life and echoed: the primary by default; a protected
  primary never, the default then falling to the live reload target, else
  the API off (NP-14-13); `XBIN_DEPLOYMENT` in both session env paths; agent
  history per tile with a `deployment` field (08-data Divergence 2).
- **Owns.** `internal/term/{term,target,sandboxenv,attach,sessions,agent,history}.go`,
  `internal/server/{termsessions,agentapi}.go`,
  `internal/term/{gates,deploy}_test.go`.
- **Tests.** `TestSessionTarget`, `TestTargetGates` (extends
  `TestServeWSGates`), `TestTargetProtectedPrimary` (=
  `TestProtectedPrimaryRefusesTerminalTokens`).
- **Links.** PO-3; `TestSessionEnvZeroState` unchanged; `term.go` keeps its
  lowered budget.

#### WP-52 plane-code-ops · L · wave 2.2 · after WP-30, WP-33
- **Scope** (05-model §5, §10; 11-contract §1.4–§1.6; 07-runtime §8.6;
  NP-11-4, NP-11-5, NP-06-17). Add, remove (its data through WP-41), promote
  (the diff and `expect`) and attach, as those sections specify, with their
  authority rows; joining a seeded namespace is a manager's act (08-data
  §6.2).
- **Owns.** `internal/deployments/{ops_code,authz}.go`, tests.
- **Tests.** `TestDeployPlaneOperations`, `TestDeployAuthzMatrix`,
  `TestOperationsNeverWriteWorkTree`, `TestViewAsRefusedEveryOp` (these ops);
  `TestPromoteMovesCodeOnly`, `TestAddDeploymentNameCollision`,
  `TestDeploymentCountCaps`, `TestP19RefusesChromeAndXbinTiles`,
  `TestWorkTreeWritersReachOnlyLiveTarget`.
- **Links.** `deployments/1` joins `features` when these register.

#### WP-53 plane-governance · L · wave 2.3 · after WP-47, WP-49, WP-52
- **Scope** (05-model §5, §10, §11; 11-contract §1.7–§1.9; 09-fabric §8;
  06-security T16, NP-06-3, NP-06-4, NP-06-8; NP-08-4). Reassign and protect
  as those sections specify; reviewed-checkpoint binding and compare-and-set
  onto a protected primary; protecting restarts sessions that target the
  primary onto P24's default, or ends them (NP-14-13); the data, delivery,
  alwaysOn, edge and run-now
  ops delegating to their planes; lifecycle and transfer reaching every
  deployment; a protected primary's tile-level fields from its checkpoint;
  "not enforced" under `--no-auth`.
- **Owns.** `internal/deployments/{ops_gov,authz}.go`,
  `internal/broker/{lifecycle,transfer}.go`, `internal/registry/deployview.go`,
  tests.
- **Tests.** `TestReassignPrimaryAtomic` (=
  `TestReassignMovesActiveRegistrations`), `TestProtectedPrimary`,
  `TestProtectedPromoteNeedsReviewedCheckpoint`,
  `TestReassignPinsReviewedCheckpoint`, `TestProtectionCoversNestedComponents`,
  `TestProtectedPrimaryIgnoresWorkTreeTileFields`,
  `TestDeploymentsAcrossTileLife`, `TestAlwaysOnNonPrimaryManagerOnly`,
  `TestNoAuthProtectionMarkedUnenforced`; `TestDeployAuthzMatrix` and
  `TestDeployPlaneOperations` (these ops).

#### WP-54 api-shapes · M · wave 2.3 · after WP-33
- **Scope** (11-contract §8; NP-13-4; 06-security T7 item 2). `/backends`
  keeps one key per tile and nests `deployments`; `/runtime` adds
  `deploymentBackends[]`; `/tile-status?deployment=` and
  `/frame-token?deployment=` echo; the `/components` summary gains `count`
  and `primary`; `/whoami` reports `deployment`; non-primary rows reach
  writers only.
- **Owns.** `internal/boot/api.go`, `internal/server/api.go`,
  `internal/broker/whoami.go`, tests.
- **Tests.** `TestNonPrimaryRowsNeedWrite`; `TestZeroStateListings`,
  `TestZeroStateComponentsEntry` unchanged.
- **Links.** PO-11, PO-14; 12-compat C3.

#### WP-55 panel · L · wave 2.1 · after WP-24
- **Scope** (10-ux §3–§6.1, §12, §14.1; NP-10-4). `web/bx-deploy.js` defines
  `<bx-deployments>` and its `testApi()`: rows, overview, actions, the
  promotion diff, the deploy log, add and remove, data, vault, deliveries,
  alwaysOn, primary and protection, the edges table, dormant registrations
  with run now, "would notify", the view tab; the M2 strings join
  `deploy-state.js`.
- **Owns.** `web/{bx-deploy,deploy-state}.js`, `hack/deploy-state.test.mjs`.
- **Tests.** `hack/deploy-state.test.mjs` (M2 rows); `js-check`,
  `theme-check`.

#### WP-56 frame-deployments · L · wave 2.2 · after WP-55
- **Scope** (10-ux §2.3, §3.1, §6, §9; 11-contract §2.5; NP-10-2, NP-10-8,
  NP-12-3; R-1; P24). The sixth `.lyt` layout, never persisted, and the lazy
  panel; the API dropdown's entry per deployment the viewer may reach, and
  "target: <name>" in the bar; bx-logs' `deployment`; frame-info's
  qualified `src`; term-sessions' pref; the optional `xbin.deployment` and
  `bx-code` line; non-primary frames reloading on R-1's event shape; target
  notices; the `deployments` pass.
- **Owns.** `web/{bx-frame,frame-titlebar,frame-deploy,frame-testapi,bx-logs,frame-info,term-sessions,xbin-client,bx-terminal,bx-code}.js`,
  `hack/ui-harness/passes/deployments.js`.
- **Tests.** Harness `deployments` (steps 1–8), `agentTab` (six buttons);
  `livereload` unchanged.

#### WP-57 scaffold · M · wave 2.3 · after WP-54
- **Scope** (13-surfaces §4.17). The admin runtime tab nests non-primary rows
  from `deploymentBackends[]`. Optional entries that tolerate an absent field
  and load new modules by dynamic `import()` with a fallback (12-compat R-9):
  the tile menu's `⇈ Deployments…`, the tile-admin section, the clone text,
  the chips.
- **Owns.** `workspace-template/tiles/admin/tabs/runtime.js`,
  `workspace-template/shell/{menus,bx-tile-admin}.js`,
  `workspace-template/tiles/manager/index.html`, `hack/menus.test.mjs`.
- **Tests.** `hack/menus.test.mjs`; harness `adminTabs admin menus`.
- **Links.** Compat rule 4.

#### WP-58 bx-deployments · L · wave 2.1 · after WP-26
- **Scope** (11-contract §9.2–§9.3; 10-ux §8). `cmd/bx/deployment.go` (`ls`,
  `add`, `rm`, `primary`, `protect`, `seed`, `reset`, `vault-copy`, `set`,
  `edge`, `run-now`, `log`, `diff`); `bx promote`; `--deployment` on `status`
  and `logs`, echo checked; `bx agent run --deployment` through a new file; a
  one-line protected-primary hint in `apiJSON`; `$XBIN_DEPLOYMENT` defaults
  read commands only.
- **Owns.** `cmd/bx/{deployment,deploy,status,agentdeploy,main,deployclient}.go`,
  `cmd/bx/deploy_test.go`.
- **Tests.** `TestParseDeploymentArgs`, `TestBxSaysWhereSavesGo` (M2),
  `TestBxHonoursXBINDeployment`, `TestBxRefusalMessages`.

#### WP-59 sdk · S · wave 2.1 · after WP-30
- **Scope** (11-contract §6). `xbin.Deployment()`, `CallerInfo.Deployment`.
- **Owns.** `sdk/xbin.go`, `sdk/deployment_test.go`.
- **Tests.** `TestDeploymentFromEnv`, `TestCallerDeployment`; no new
  `require` in `sdk/go.mod`.
- **Links.** Compat rule 8; /docs/sdk.md text for WP-64.

#### WP-60 old-client-fixtures · S · wave 2.3 · after WP-28, WP-48
- **Scope** (15-test-plan §1 principle 4, §3.12). `isReloadTarget`
  (`web/events-socket.js:40-49`) under node, ancestors included; rows for the
  shipped app's `ReloadTargets`
  (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`),
  replaying WP-28's tape.
- **Owns.** `hack/events-socket.test.mjs`,
  `native/ios/Packages/XbinCore/Tests/XbinCoreTests/ClientEventsTests.swift`
  (new rows).
- **Tests.** Both; `make swift-test` where Swift exists, else CI's `native`
  job, as the commit body says.

#### WP-61 itest-data · L · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{deploy_static,deployments}_test.go`.
- **Tests.** `TestStaticDeploymentURLAndData`, `TestDataSeparation`,
  `TestPromoteAndRollBack`, `TestRollBackBound` (every deployment),
  `TestRegistrationsAtStartStayDormant`, `TestSelfCallsStayInDeployment`,
  `TestMultiTileScope`.

#### WP-62 itest-fabric · L · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{edges,flowc}_test.go`.
- **Tests.** `TestInboundEdgesReachOnlyPrimary`, `TestOutboundEdgePolicy`,
  `TestReassignPrimaryFlowF`, `TestAgentFlowCWithBxOnly`,
  `TestPrimaryFirstUnderPressure`.

#### WP-63 itest-downgrade-latency · M · wave 2.4 · after every M2 feature WP
- **Owns.** `test/{downgrade,latency}_test.go`.
- **Tests.** `TestDowngradeStatic`, `TestDowngradeDormantRegistrations`
  (`XBIN_DOWNGRADE_BIN`), `TestLatencyLifecycleOps` (M2 rows), the save
  budgets with live reload on `dev`.
- **Links.** The integrator adds CI's download of the previous release.

#### WP-64 docs-m2-wire · L · wave 2.4 · after every M2 feature WP
- **Scope.** [/docs/protocol.md](/docs/protocol.md) (every M2 route,
  qualified URLs and the `+` rule, `X-XBin-Deployment`, `XBIN_DEPLOYMENT`,
  the 6-field token, `?deployment=` and echoes, dormant semantics, the event
  scheme, per-deployment files), [/docs/sdk.md](/docs/sdk.md),
  [/docs/resources.md](/docs/resources.md), [/docs/auth.md](/docs/auth.md)
  (principals' deployments, the edge policy, the vault, tile managers' acts,
  leftovers, origins labels, the accident boundary of P20).
- **Owns.** Those four files. **Tests.** The docscheck guards,
  `TestDeploymentWording`.

#### WP-65 docs-m2-guides · L · wave 2.4 · after every M2 feature WP
- **Scope.** [/docs/elements.md](/docs/elements.md),
  [/docs/isolation.md](/docs/isolation.md), [/docs/bx.md](/docs/bx.md),
  `docs/overview/{05,06,08,09,10,13,14,15,16}-*.md`,
  [/docs/frontend-kit.md](/docs/frontend-kit.md), [/docs/compat.md](/docs/compat.md),
  [/docs/maintenance.md](/docs/maintenance.md) (the new guards and pass),
  `workspace-template/AGENTS.md`, the builder page.
- **Owns.** Those files. **Tests.** The docscheck guards,
  `TestDeploymentWording`.

#### WP-66 checkpoint-purge · M · wave 2.3 · after WP-11, WP-12 and a route amendment · gated by R-4
- **Scope** (06-security T20, NP-06-16). A tile manager removes an unused
  checkpoint from every deploy log, its objects are pruned at once, and the
  purge is logged.
- **Owns.** `internal/checkpoint/purge.go`, `internal/deployments/ops_purge.go`,
  tests.
- **Tests.** `TestPurgeManagerOnly`, `TestPurgeRefusesDeployedCheckpoint`,
  `TestPurgeRemovesObjects`.

### 2.5 Shared sandbox mechanics (WP-S), with D113

Tile deployments are not built on D113's tile-managed sandboxes (the owner's
direction, [05-model.md](05-model.md) §12). They change the layer one level
up, which backends, terminals and D113's sandboxes share: the registry, the
VM books, the cgroup tree, the launch spec and confine. WP-S is that work,
one owner role in seven packages across the milestones. The D113 project
([../tile-sandboxes.md](../tile-sandboxes.md)) may run at the same time and
needs the same cgroup parent and registry fields, so four rules hold:

1. **One declaration in one place** (D109). Each shared field, type or
   function is declared once, by whichever project needs it first, to
   [07-runtime.md](07-runtime.md) §10's specification; the other project's
   owner reviews that commit before it merges.
2. **Split files.** The projects edit different files (below). Where they
   meet, one declares and the other adds a field to it, never a second
   signature.
3. **Either order.** Each rebases on whatever landed first, and both
   projects' suites run after the merge: a clean textual merge proves nothing
   (`native/AGENTS.md:375-383`).
4. **No single backend.** D113 must not assume one backend per tile: an
   exec's cursor is valid only in the deployment that created it (07-runtime
   §10.7).

| Piece | Files | This feature builds | D113 builds |
|---|---|---|---|
| Registry fields | `internal/sbx/sbx.go` (`Entry` `:50-74`, `Filter` `:77-80`, `Failure` `:97`) | `Deployment` and `Checkpoint` on entries, `Filter.Deployment`, `Failure.Deployment` in the coalescing key (WP-S0) | its `tile` entries' fields; `Deployment` on them once WP-S5 applies |
| Backend rows | `internal/runner/sbx.go`, `internal/boot/sandboxes.go`, the admin tab and its pass | per-deployment ids, leaves and rows (WP-S0, WP-S4) | its routes in their own file, not `sandboxes.go` (requested) |
| VM books | `internal/vm/reserve.go` (options); `internal/vm/policy.go` (policy fields; `Reserve` `:132`, `UsedBy` `:171`) | `Reserve(owner, memMiB, opts...)`, headroom and primary-first options (WP-S0, WP-S3) | `tiles` and `tilesBudgetMiB`, and its sub-budget as another option |
| cgroup tree | `internal/cgroup/*` (today's leaf, `cgroup_linux.go:89`) | the nested layout, parents, `ProcsTree`, the sweep (WP-S2) | its `sbx-<name>` leaves under `tile-<CompKey>/d-<dep>/` |
| confine | `internal/confine/confine.go` | `DirFrom`, `At` (WP-S1); streaming writers (WP-S6) | nothing |
| Sandbox init | `internal/sandbox/*` | a `Src ≠ Dst` test (WP-S1) | `Spec.AgentFD` |
| Tile sandboxes per deployment | D113's manager | the specification (WP-S5) | the code, if D113 lands after M2 |

The cgroup parent is the piece both want soonest. If D113 reaches its
nested-cgroup step first, D113 builds 07-runtime §10.3's layout and WP-S2
shrinks to review plus the deployment leaves; if WP-S2 lands first, D113
places its leaves in it.

#### WP-S0 sbx-decl · S · wave 0.2 · after WP-00 · on master
- **Scope.** The registry fields above; `Reserve` gains variadic options in
  `internal/vm/reserve.go`, none honoured yet; the D112 sandboxes tab keys
  the current generation by `(tile, deployment ?? '')` (NP-12-8), and its
  pass asserts `main`'s rows carry no deployment field.
- **Owns.** `internal/sbx/sbx.go`, `internal/sbx/filter_deploy_test.go`,
  `internal/vm/{policy,reserve}.go`,
  `workspace-template/tiles/admin/tabs/sandboxes.js`,
  `hack/ui-harness/passes/sandboxes.js`.
- **Tests.** `TestFilterDeployment`; harness `sandboxes`;
  `TestEntryJSONZeroState`, `TestReserveAttribution` unchanged.
- **Links.** PO-11. D113's owner reviews.

#### WP-S1 confine-dirfrom · M · wave 1.1 · after WP-03
- **Scope** (07-runtime §10.5; NP-07-10). `confine.Cmd.DirFrom`,
  `confine.At(src, dst, ro)`, `ErrNeedsIsolation`; `binds()`
  (`internal/confine/confine.go:218-224`) binds `DirFrom` at `Dir`; a direct
  run (`:140-143`) with `DirFrom ≠ Dir`, or any non-mask bind with `Src ≠
  Dst`, returns `ErrNeedsIsolation` and never runs.
- **Owns.** `internal/confine/{confine.go,bind_test.go,rebind_linux_test.go}`,
  `internal/sandbox/bind_srcdst_linux_test.go`.
- **Tests.** `TestDirectModeRefusesRebind`, `TestConfineBindDestinationDefault`
  (defaults to `Dir`); confined `TestRebindDirInSandbox`, `TestBindSrcNotDst`.
- **Links.** `DirFrom` → WP-17. PO-11.

#### WP-S2 cgroup-parent · M · wave 2.1 · after WP-30
- **Scope** (07-runtime §10.3; NP-07-5, NP-13-2, NP-06-14). A name holding
  `/` is a path under the base; parents made with `subtree_control` before
  their leaf, removed bottom-up once empty; `ProcsTree`; hierarchical usage
  and limit readers; a boot sweep of empty `tile-*` subtrees; weights (tile
  100; primary 100, non-primary 50); `memory.low` on the primary's node;
  stubs in `cgroup_other.go`.
- **Owns.** `internal/cgroup/{cgroup_linux,cgroup_other}.go`,
  `parent_linux_test.go`.
- **Tests.** `TestNonPrimaryCgroupSubtree`, `TestPrimaryLeafKeepsCaps`;
  `TestWriteLimits` unchanged.
- **Links.** → WP-34, WP-S4, D113. PO-11: the flat leaf stays for zero-state
  and main-only tiles.

#### WP-S3 vm-books · M · wave 2.1 · after WP-S0
- **Scope** (07-runtime §10.2; NP-07-6, NP-02-12). Headroom: a non-primary
  reservation leaves the primary's guest size free. Primary first: when the
  budget can't admit a primary start, a runner-supplied callback stops the
  tile's non-primary guests; a non-primary start is refused with
  `sbx.Refuse`, never preempting. `UsedBy` stays per tile.
- **Owns.** `internal/vm/{policy,reserve}.go`, `reserve_deploy_test.go`.
- **Tests.** `TestReserveChargesTileForEveryDeployment`,
  `TestNonPrimaryVMCannotStarvePrimary`; `TestReserveAttribution` unchanged.
- **Links.** → WP-34.

#### WP-S4 registry-rows · M · wave 2.2 · after WP-33, WP-S2
- **Scope** (07-runtime §10.1; 11-contract §8's `/sandboxes` row). `main`
  keeps `backend:<CompKey>:g<gen>` (`internal/runner/sbx.go:63`), others get
  `backend+<name>:<CompKey>:g<gen>`; `Deployment` on every entry of a tile
  with a record; `Leaf` from the generation; failures carry the deployment;
  `/sandboxes?deployment=`, `stats.scope: "deployment"`; the admin tab groups
  by (tile, deployment); the pass gains a non-primary generation.
- **Owns.** `internal/runner/sbx.go`, `internal/boot/sandboxes.go`, the admin
  `tabs/sandboxes.js`, `passes/sandboxes.js`,
  `internal/runner/sbx_deploy_test.go`, `internal/boot/sandboxes_test.go`.
- **Tests.** `TestSandboxEntriesPerDeployment` (=
  `TestRegistryRowsPerDeployment`), `TestSandboxesDeploymentRows`; harness
  `sandboxes`.

#### WP-S5 tile-sandboxes-per-deployment · M · wave 2.4, or in D113's project
- **When.** With whichever of M2 and D113 lands second, as
  [02-goals.md](02-goals.md)'s milestone table says.
- **Scope** (05-model §12; 07-runtime §10.7; 08-data §2). D113's definitions
  and state per deployment (`main` keeps `data/sandboxes.json` and
  `.xbin/sbx/<CompKey>/`; others beside their record and under
  `.xbin/deploy/<CompKey>/d/<name>/sbx/`); `{"source":true}` mounts
  `CodeRoot(tile, dep)`; resource mounts through the remap; a non-primary
  backend drives only its own set; `cap:sandboxes` and the caps stay the
  tile's.
- **Owns.** The manager files D113's implementation names.
- **Tests.** `TestTileSandboxesPerDeployment`,
  `TestTileSandboxMountsDeploymentNamespace`, `TestAttachTicketBindsDeployment`.

#### WP-S6 confine-streaming · M · wave 3.1 · after the M2 exit
- **Scope** (07-runtime §10.6; NP-13-9). `Cmd.Stdout` and `Stderr` writers,
  used instead of the buffered `Result` (`internal/confine/confine.go:87-89`)
  and the capped writer (`:251`); an output cap that ends the run with a loss
  marker; TERM then KILL on timeout; D78's guarantees unchanged; no D113 code.
- **Owns.** `internal/confine/confine.go`, tests. **Tests.** A loss-marker
  test and a timeout test, named in the M3 card.

### 2.6 M3 — later rungs (outlines)

- **WP-70 tracked-branch (L).** The tracked-branch config in the record; a
  ref poll, since the watcher ignores `.git` (`internal/watch/watch.go:61-74`);
  a confined fetch into a quarantine, `fsck --strict`, admission; refused
  onto a protected primary (NP-06-7); "committing deploys nothing" kept true,
  or CM-2's guidance changed in the same release (12-compat G4). Tests:
  `TestTrackedBranchReadsGitOnlyInConfine`,
  `TestTrackedBranchRefusedOnProtectedPrimary`, a trigger test.
- **WP-71 deploy-remote (L), after WP-S6.** `receive-pack --stateless-rpc` in
  the streaming confine; bearer-only; refs limited to `deploy/<name>` of
  existing deployments; a per-push quarantine; size caps. Tests:
  `TestDeployRemoteBearerOnly`, `TestDeployRemoteHostilePack`,
  `TestDeployRemoteRefNamespace`.
- **WP-72 edge-match (L).** `match` on the existing edge record, behind a
  `features` string; provider consent; per-deployment rosters; the P7
  exception ([09-fabric.md](09-fabric.md) §9).
- **WP-73 materialize-diff (M).** Differential materialization (NP-07-8),
  gated on its benchmark; pulled into M2 if SC-LATENCY-OPS's large-tree case
  fails there.
- **WP-74 test-hostnames (M, optional).** A manager-routed test hostname for
  a named deployment ([05-model.md](05-model.md) §15).
- **WP-75 docs-m3 (M).** The feeds and `match` in the builder docs.

## 3. Waves, dependencies and file ownership

### 3.1 Waves

A wave's WPs run in parallel. The integrator merges them one at a time in
the order listed, then runs the gate.

| Wave | Base | WPs, in merge order | Gate |
|---|---|---|---|
| 0.1 | master | WP-00 | `make check` |
| 0.2 | master | WP-S0, WP-04, WP-03, WP-02, WP-01 | `make check`, `make integration`, a fresh-seed harness run (A/B) |
| — | | the integrator cuts `dev-lifecycle` and commits Phase 0 (PRE-2) | `make check` |
| 0.3 | `dev-lifecycle` | WP-05, WP-07, WP-08 | `make check` |
| 0.4 | | WP-06 | the M0 exit |
| 1.1 | | WP-24, WP-27, WP-S1, WP-10, WP-13, WP-18, WP-21, WP-22 | `make check`, `make integration` |
| 1.2 | | WP-11, WP-12, WP-17, WP-16, WP-19, WP-20, WP-23, WP-14, WP-15, WP-25, WP-26 | the same, and harness `livereload` |
| 1.3 | | WP-28, WP-29 | the M1 exit |
| 2.0 | | WP-30 | `make check` |
| 2.1 | | WP-31, WP-32, WP-35, WP-39, WP-59, WP-S2, WP-S3, WP-33, WP-49, WP-55, WP-58 | `make check`, `make integration` |
| 2.2 | | WP-34, WP-40, WP-41, WP-43, WP-47, WP-36, WP-37, WP-51, WP-S4, WP-52, WP-56 | the same, and harness `deployments` |
| 2.3 | | WP-38, WP-42, WP-44, WP-46, WP-48, WP-50, WP-53, WP-54, WP-57, WP-60, WP-66 | the same |
| 2.4 | | WP-61, WP-62, WP-63, WP-45, WP-S5, WP-64, WP-65 | the M2 exit |
| 3.1 | | WP-S6, WP-70, WP-72, WP-73, WP-74 | `make check`, `make integration` |
| 3.2 | | WP-71, WP-75 | the M3 exit |

### 3.2 Dependency graph

The cards' "after" lists are authoritative; this is the backbone.

```
M0a  WP-00 ─┬─ WP-01, WP-02, WP-03, WP-04, WP-S0 ─┐
            │                                      ├─ cut dev-lifecycle, Phase 0
M0b         └──────────────────────────────────────┘   WP-05, WP-07, WP-08 ── WP-06 ── M0 exit

M1   WP-13 ───────────────┬─ WP-14 ── WP-15 ─┬─ WP-25 ─┐
     WP-10 ─┬─ WP-11 ─────┤                  └─ WP-26 ─┼─ WP-28, WP-29 ── M1 exit
            ├─ WP-12 ─────┼─ WP-20                     │
            └─ WP-23      │                            │
     WP-01, WP-05 ─ WP-16 ┘   WP-S1 ── WP-17           │
     WP-18 ── WP-19   WP-24 ── WP-25   WP-21, WP-22, WP-27 ────────┘

M2   WP-30 ─┬─ WP-31, WP-32 ─┬─ WP-36 ── WP-38
            │                ├─ WP-37 (also after WP-33)
            │                └─ WP-51
            ├─ WP-33 ─┬─ WP-34 (also after WP-S2, WP-S3)
            │         ├─ WP-S4 (also after WP-S2)
            │         ├─ WP-52 ── WP-53 (also after WP-47, WP-49)
            │         ├─ WP-54 ── WP-57
            │         └─ WP-48 (also after WP-32) ── WP-60
            ├─ WP-39 ─┬─ WP-40 ─┬─ WP-42
            │         ├─ WP-41 ─┴─ WP-44 ── WP-45
            │         ├─ WP-43
            │         └─ WP-47 ── WP-50 (also after WP-49)
            ├─ WP-49 ── WP-46 (also after WP-39)
            └─ WP-35, WP-59, WP-S2, WP-S3
     WP-24 ── WP-55 ── WP-56      WP-26 ── WP-58
     every M2 feature WP ── WP-61, WP-62, WP-63, WP-64, WP-65 ── M2 exit
```

**Critical path** in agent-days, not counting the integrator's gates:
- M0: WP-00 (S) → WP-01 (L) → WP-05 (L) → WP-06 (M), about 5.5.
- M1: WP-13 (M) → WP-14 (L) → WP-15 (M) → WP-28 (L), about 6; WP-10 (L) →
  WP-11 (M) runs beside it and merges before WP-14.
- M2: WP-30 (M) → WP-33 (L) → WP-52 (L) → WP-53 (L) → WP-61 (L), about 9.

Waves 1.2 and 2.1 to 2.3 hold about eleven WPs each, so keeping to the path
takes that many agents.

### 3.3 File-ownership matrix

Files that one WP owns for the whole plan appear only on its card. These are
the files with several owners. Each row shows at most one owner per wave,
which proves that no two WPs in one wave own the same file.

| File | Owners, by wave |
|---|---|
| `internal/runner/runner.go` | 0.1 WP-00 · 0.2 WP-01 · 0.3 WP-05 · 1.2 WP-16 · 2.1 WP-33 · 2.2 WP-34 |
| `internal/runner/states.go` | 0.1 WP-00 · 0.2 WP-01 · 0.3 WP-05 · 1.2 WP-16 · 2.1 WP-33 |
| `internal/runner/sandboxcmd.go` | 0.1 WP-00 · 0.2 WP-01 · 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/build.go` | 0.1 WP-00 · 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/env.go` | 0.3 WP-05 · 1.2 WP-17 · 2.1 WP-35 |
| `internal/runner/deploy.go` | 0.3 WP-05 · 1.2 WP-16 · 2.1 WP-33 |
| `internal/runner/{alwayson,vm}.go` | 1.2 WP-16 · 2.2 WP-34 |
| `internal/runner/inspect.go` | 1.2 WP-16 · 2.1 WP-33 |
| `internal/term/term.go` | 0.1 WP-00 · 0.3 WP-05 · 2.2 WP-51 |
| `internal/term/sandboxenv.go` | 0.1 WP-00 · 1.1 WP-22 · 2.2 WP-51 |
| `internal/term/deploy_test.go` | 0.2 WP-02 · 2.2 WP-51 |
| `internal/term/target.go` | 2.0 WP-30 · 2.2 WP-51 |
| `cmd/bx/{main,status}.go` | 0.1 WP-00 · 1.2 WP-26 · 2.1 WP-58 |
| `cmd/bx/{deploy,deployclient}.go` | 1.2 WP-26 · 2.1 WP-58 |
| `cmd/bx/deploy_test.go` | 0.2 WP-02 · 1.2 WP-26 · 2.1 WP-58 |
| `web/bx-frame.js` | 0.1 WP-00 · 1.2 WP-25 · 2.2 WP-56 |
| `web/frame-testapi.js` | 0.1 WP-00 · 0.3 WP-08 · 1.2 WP-25 · 2.2 WP-56 |
| `web/{frame-titlebar,frame-deploy,bx-terminal}.js` | 1.2 WP-25 · 2.2 WP-56 |
| `web/deploy-state.js`, `hack/deploy-state.test.mjs` | 1.1 WP-24 · 2.1 WP-55 |
| `hack/ui-harness/passes/livereload.js` | 0.3 WP-08 · 1.2 WP-25 |
| `hack/ui-harness/passes/deployments.js` | 0.3 WP-08 · 2.2 WP-56 |
| `hack/ui-harness/passes/sandboxes.js`, admin `tabs/sandboxes.js` | 0.2 WP-S0 · 2.2 WP-S4 |
| `hack/menus.test.mjs` | 0.3 WP-08 · 2.3 WP-57 |
| `internal/boot/boot.go` | 0.3 WP-07 (one line) · 0.4 WP-06 · 2.0 WP-30 |
| `internal/boot/serve.go` | 0.4 WP-06 · 1.2 WP-20 |
| `internal/boot/deployments.go` | 0.3 WP-07 · 0.4 WP-06 · 1.2 WP-15 |
| `internal/boot/deploystate_test.go` | 0.2 WP-02 · 1.3 WP-28 |
| `internal/server/{static,tileassets,native,deployserve}.go` | 1.2 WP-19 · 2.2 WP-36 |
| `internal/server/api.go` | 1.2 WP-19 · 2.3 WP-54 |
| `internal/server/server.go` | 1.1 WP-21 · 2.3 WP-48 |
| `internal/server/deployevents_test.go` | 0.2 WP-02 · 2.3 WP-48 |
| `internal/proxy/proxy.go` | 1.2 WP-19 · 2.2 WP-37 |
| `internal/proxy/deploy_test.go` | 0.2 WP-02 · 2.2 WP-37 |
| `internal/auth/auth.go` | 0.3 WP-05 · 2.1 WP-32 |
| `internal/auth/deployment_test.go` | 0.2 WP-02 · 2.1 WP-32 |
| `internal/registry/registry.go` | 0.3 WP-05 · 1.1 WP-18 |
| `internal/registry/deployview.go` | 1.1 WP-18 · 2.3 WP-53 |
| `internal/deployments/plane.go` | 0.4 WP-06 · 1.1 WP-13 · 1.2 WP-14 · 2.0 WP-30 |
| `internal/deployments/authz.go` | 1.2 WP-14 · 2.2 WP-52 · 2.3 WP-53 |
| `internal/broker/broker.go` | 0.3 WP-05 · 2.0 WP-30 · 2.1 WP-39 · 2.2 WP-47 |
| `internal/broker/deploydata.go` | 2.0 WP-30 · 2.1 WP-39 |
| `internal/broker/resources.go` | 1.1 WP-18 (`Provision`) · 2.1 WP-39 · 2.2 WP-40 |
| `internal/broker/resenc_wire.go` | 2.1 WP-39 · 2.2 WP-40 · 2.4 WP-45 |
| `internal/broker/backup.go` | 1.2 WP-23 · 2.1 WP-39 · 2.3 WP-44 · 2.4 WP-45 |
| `internal/backup/backup.go` | 1.2 WP-23 · 2.3 WP-44 · 2.4 WP-45 |
| `internal/broker/backup_deploy.go` | 1.2 WP-23 · 2.3 WP-44 |
| `internal/broker/{diskmon,resusage}.go` | 2.1 WP-39 · 2.3 WP-44 |
| `internal/broker/deploy_env_test.go` | 0.2 WP-03 · 2.2 WP-40 |
| `internal/broker/netfn.go` | 2.2 WP-47 · 2.3 WP-50 |
| `internal/broker/create.go` | 2.1 WP-49 · 2.3 WP-46 |
| `internal/obs/status.go` | 1.1 WP-21 · 2.3 WP-50 |
| `internal/vm/{policy,reserve}.go` | 0.2 WP-S0 · 2.1 WP-S3 |
| `internal/confine/confine.go` | 1.1 WP-S1 · 3.1 WP-S6 |
| `internal/confine/bind_test.go` | 0.2 WP-03 · 1.1 WP-S1 |
| `test/livereload_test.go` | 0.2 WP-03 · 1.3 WP-28 |
| `test/latency_test.go` | 0.2 WP-04 · 1.3 WP-28 · 2.4 WP-63 |
| `test/deployments_test.go` | 1.3 WP-28 · 2.4 WP-61 |
| `test/flowc_test.go` | 1.3 WP-28 · 2.4 WP-62 |
| `docs/protocol.md` | 0.3 WP-07 (API-fence rows) · 1.3 WP-29 · 2.4 WP-64 |
| `docs/{elements,isolation,bx,frontend-kit,compat,maintenance}.md`, `docs/overview/{09-terminals,14-lifecycle,15-operations}.md`, `workspace-template/AGENTS.md` | 1.3 WP-29 · 2.4 WP-65 |

**Seams where a clean merge proves nothing.** After merging the second WP of
each pair, run both WPs' suites: WP-16 and WP-17 (`Code` through `build`),
WP-33 and WP-34 (the `start` network block), WP-39 and WP-40 (the key
function's callers), WP-47 and WP-50 (`netfn.go`), WP-52 and WP-53
(`authz.go`), WP-S2 and WP-34 (leaf names), WP-S3 and WP-34 (the preempt
callback).

## 4. Integration model

### 4.1 Branches and worktrees

- **M0a** WPs branch from master and merge back into master (NP-14-1): they
  change no behaviour, and landing the budget splits early keeps concurrent
  work on master from colliding with them later.
- **The integration branch** `dev-lifecycle` is cut from master after wave
  0.2. Its first commit is Phase 0, the design set as plans only (the b0787cd
  precedent).
- **Every other WP** branches `dl/<slug>` from the head the integrator names,
  in its own worktree: `git worktree add /home/magik6k/buxon-wt/<slug> -b
  dl/<slug> dev-lifecycle`.
- Nothing happens in `/home/magik6k/buxon`, where another agent works.

### 4.2 The integrator

One agent, or the owner. Only the integrator merges WPs; syncs master into
the integration branch at every wave boundary (the 72e2ef6 precedent; master
already moved, PRE-1); edits §2.1 clause 3's files and makes contract
amendments (§4.4); runs the gates (§3.1); writes the records (§4.5); and
moves master (§4.6).

### 4.3 Merging a WP

1. Merge `dl/<slug>` with `--no-ff`, in the wave's order; list conflicts
   under "Conflicts:" in the merge body, as the native swarm did (6b7f652,
   02d2bab).
2. Run `make check`; `make integration` when runner, sandbox, cgroup, VM,
   broker, confine, proxy or term code changed, with `XBIN_TEST_ROOTFS` and
   the agent's tool sandbox off; the WP's harness passes on a fresh seed.
3. After the second WP of a seam pair (§3.3), run both WPs' suites.
4. A red gate is fixed on the WP's branch and merged again; the integrator
   never fixes a WP's code inside a merge commit.

### 4.4 Contract amendments

A WP that needs a declaration, a route, an openapi or protocol row, a
`boot.go` line, or an edit outside its Owns list stops and asks. The
integrator makes the amendment in its own commit, or hands it to the next
contracts WP, and every open WP rebases. Feature WPs never add routes:
`TestRouteInventory` would force them into protocol.md and `openapi.go`, the
native swarm's worst conflict files
([research/delivery-infra.md](research/delivery-infra.md) §0).

### 4.5 Records at a milestone exit

After e1100f6, b81f93c and e32df4f, the integrator:
1. collects the **Decision:** hand-offs and gets the owner's ratification of
   the P-numbers and NP-numbers the milestone implemented;
2. assigns D-numbers: the next free ones after the highest on master at that
   moment, never a number another branch holds. Each becomes a bold-ID bullet
   in [../DECISIONS.md](../DECISIONS.md);
3. rewrites every `(Pn)` in Go comments to its D-number, and adds the
   citations to the docs;
4. writes the changelog entry from the **Changelog:** hand-offs, starting
   from [12-compat.md](12-compat.md) §10.4's draft and its downgrade note;
5. checks 12-compat §10.2's tripwires: none crossed means no migration note
   and no BREAKING mark; a crossed one gets `docs/changes/YYYY-MM-DD-<slug>.md`
   with What changed, Who's affected, How to migrate and Why, linked, and the
   entry marked BREAKING;
6. updates the status lines of [README.md](README.md) and the implemented
   documents;
7. runs `make check`, `make integration` with the rootfs, the full fresh
   harness, and the milestone's manual checklist items.

### 4.6 Master and releases

The integration branch reaches master only at a milestone exit, after its
records commit. Pushing and releasing stay the owner's; a release is cut from
a clean worktree with `make release`. M1 may ship alone: its M2 routes then
answer 501, documented as reserved, and `features` lists only `live-reload/1`
(NP-14-4).

## 5. Swarm operating notes

**Variables for a WP's worktree.** A worktree has no `.rootfs`,
`bin/gocryptfs` or Playwright (`hack/ui-harness/run.sh:80-91`), so they come
read-only from the main checkout
([research/delivery-infra.md](research/delivery-infra.md) §1.5):

```sh
export PORT=$((8700 + 20 * N))    # WP-N; WP-S<k> uses 9900 + 20*k (NP-14-7)
export HARNESS_DIR=<the agent's scratchpad>/harness-<slug>
export PLAYWRIGHT_DIR=/home/magik6k/lcad-wasm
export XBIN_TEST_ROOTFS=/home/magik6k/buxon/.rootfs
export XBIN_GOCRYPTFS=/home/magik6k/buxon/bin/gocryptfs
export XBIN_FUSE_OVERLAYFS=/home/magik6k/buxon/bin/fuse-overlayfs
```

- **Ports.** `run.sh` derives the ingress port (`PORT+1`) and fakeopenai's
  (`PORT+10280`), and `stop()` kills by port and workspace path
  (`hack/ui-harness/run.sh:22`, `:25`, `:53-66`): a shared port kills another
  agent's harness. The grid's step of 20 keeps the derived ports apart.
- **Done is `make check`** (`Makefile:136`; [/docs/maintenance.md](/docs/maintenance.md)
  "Definition of done"), which CI runs before `make integration`
  (`.github/workflows/ci.yml:41-43`). gofmt must match go.mod's minor, as the
  pins check enforces.
- **Confined and isolated tests** need user namespaces and a git-bearing
  rootfs, and skip with the reason otherwise
  (`internal/confine/sandbox_linux_test.go:26-35`). They need the agent's
  tool sandbox off, since it refuses the `/proc/self/exe` re-exec
  (15-test-plan §10.2). CI skips them until R-7 lands.
- **The harness isn't in CI, so WPs report it honestly:** every PASS, FAIL
  and SKIP line from `$HARNESS_DIR/out/<pass>.txt`; each SKIP's reason; for a
  FAIL that looks pre-existing, an A/B run against the integration branch's
  head, named as such (the D88 precedent). A failure that moves to a
  different pass on each run comes from the box, not the code. The gate is a
  fresh-seed run, never `--shots` on a reused workspace.
- **Harness habits.** Never pipe `run.sh --keep` or `--restart`: xbind holds
  the pipe open; read the out files. Stop a hand-started xbind by PID, since a
  `pkill -f` pattern can match the calling shell. A read-only Go module cache
  needs `GOMODCACHE` pointed at a writable directory.
- **A WP that finds the design wrong stops and reports.** It doesn't
  improvise another design, raise a budget, or edit outside its Owns list.

## 6. Risks, rollback and shipping dark

### 6.1 Risk register

| # | Risk | Mitigation | Watched by |
|---|---|---|---|
| K1 | Two WPs collide on a hot or budgeted file | M0's splits; §3.3's matrix; amendments only through the integrator | integrator |
| K2 | A zero-state tile changes (P5) | goldens first, on master, never regenerated; SC-ZERO gates every milestone | WP-01 to WP-03, every gate |
| K3 | The default save path slows down | M0's baseline; per-save counters (seam row 30, `TestWatchLoopZeroStateNoStoreIO`); SC-LATENCY-DEFAULT at each exit | WP-04, WP-20, WP-63 |
| K4 | The event scheme is open when wave 2.1 starts | R-1 before wave 2.1; `eventComp` is the one switch; default C2 | owner, WP-33 |
| K5 | Confined and isolated tests pass only on a developer's box | R-7's CI rootfs in M0; meanwhile, local runs reported in commit bodies | integrator |
| K6 | D113 collides with the shared layer | §2.5's split files, first-lander rule, cross-review | WP-S owner, D113's owner |
| K7 | Parallel WPs grow duplicate stubs (the D109 failure) | WP-05 to WP-07 and WP-30 declare every cross-WP interface once | integrator |
| K8 | git runs unconfined on tile data, or a symlink is followed | `TestNoDirectExec`, the hostile-repo tests, 06-security's ledger as the checkpoint WPs' review list | WP-10 to WP-12 |
| K9 | A seed, reset or removal loses data | holds; unmount verified before removal; last-claimant deletion; manager-only acts | WP-41, WP-42 |
| K10 | A flaky harness hides a regression or fakes one | fresh-seed gates, A/B against the branch head, numbers never loosened | every UI WP |
| K11 | The integration branch drifts from master | M0a on master; master merged at every wave boundary | integrator |
| K12 | Sibling proposals creep into the exits | gated WPs never hold an exit; R-5 not built | integrator |
| K13 | Large trees make pinned changes slow | 07-runtime §2.5's caps; progress events; WP-73 pulled forward if needed | WP-11, WP-63 |
| K14 | A non-primary agent tile loses LLM completions (the read clamp on writer roles, 09-fabric Divergence 1) | documented as a limitation; R-5 | WP-64 |
| K15 | Docs drift from the code | docs WPs are exit criteria; reserved routes marked; `TestDeploymentWording` | WP-29, WP-64, WP-65 |
| K16 | A downgrade surprises an operator: pinned primaries go live | 12-compat §5.5's checklist in the changelog entry; the downgrade tests | WP-63, integrator |
| K17 | `remove-cgi` lands after M0 has split the files it edits | PRE-5 before wave 0.1; otherwise the integrator rebases it onto the split files | integrator |

### 6.2 Rollback per milestone

| Milestone | Before it reaches master | After a release |
|---|---|---|
| M0a | revert the WP's commits; moves and ratchets revert with them | the same; nothing behaves differently |
| M0b | drop it from the integration branch | not released on its own |
| M1 | revert the WP on the integration branch; the reserved routes keep answering 501 | turn the ship-dark switch off (§6.3): no new opt-ins, and users resume live reload to return tiles to the zero state. A defect in pinned serving or restarts is fixed forward, or the operator downgrades by 12-compat §5.5's checklist; an older xbind ignores every record and store. Records and stores are never deleted by hand. |
| M2 | the same | as M1; primaries are reassigned to `main` before any downgrade (12-compat §5.5 step 1); an older xbind stops non-primary deployments and loses nothing (12-compat §5.4, R-3) |
| M3 | the same | a feed is per-deployment configuration: turn it off; its checkpoints stay ordinary checkpoints |

### 6.3 Shipping dark

The feature is opt-in per tile, so a tile that nobody opts in is untouched
whatever ships. To ship the code with opting in closed, WP-06 adds one switch
and WP-14 enforces it in the plane's single authorize function (NP-14-5):
- **Off:** `GET /deployments` lists no `features` and reports every
  `allowed` entry refused with kind `policy` and the reason; POSTs that would
  create or extend deployment state answer 409 with that reason; the terminal
  window renders the zero-state bar, since its controls come from `allowed`;
  `bx` exits 3 with the reason.
- **Still allowed while off:** resuming live reload onto `main`, removing a
  deployment, resetting its data, and unprotecting, so that every tile can
  return to the zero state without a downgrade.
- **Never changed by the switch:** existing records keep governing serving
  and restarts (P9), so turning it off never unpins anything; the zero-state
  path doesn't read it.
- **Use:** a release that must ship before a milestone's exit criteria are
  all met carries the switch off by default (R-9); the next release turns it
  on, and the changelog says so.

## 7. Coverage check

### 7.1 Every surface has an owner

By the sections of [13-surfaces.md](13-surfaces.md). A "no change" row names
the test that verifies it.

| 13-surfaces | Files → WPs |
|---|---|
| §3 new files | `internal/deployments/` WP-06, 13, 14, 30, 52, 53, 66 · `internal/checkpoint/` WP-10, 11, 12, 66 · `boot/deployments.go` WP-07, 06, 15 · `registry/deployview.go` WP-18, 53 · `registry/qualifier.go` WP-31 · `runner/deploy.go` WP-05, 16, 33 · `server/deployserve.go` WP-19, 36 · `auth/deployment.go` WP-32 · `broker/deploydata.go` WP-30, 39 · `broker/edgepolicy.go` WP-47 · `broker/dormant.go` WP-49 · `term/target.go` WP-30, 51 · `cmd/bx/deploy.go` WP-26, 58 · `web/frame-deploy.js` WP-25, 56 · `web/deploy-state.js` WP-24, 55 · `web/bx-deploy.js` WP-55 · the moved files and tests: their §4 rows |
| §4.1 registry | `registry.go` WP-05, 18 · `native.go` WP-18 · `util.go` WP-05 (the per-deployment key helper is `sockDir`, 07-runtime §1.2) |
| §4.2 watch, boot | `serve.go` WP-06, 20 · `boot.go` WP-07, 06, 30 · `api.go` WP-54 · `sandboxes.go` WP-S4 · `config.go` WP-06 (caps stay constants, NP-07-7) · `vm.go`, `push.go`, `workspace.go`, `vault.go`, `watch/watch.go`: no change (`TestIgnoreRules`) |
| §4.3 runner | §3.3's rows · `binds.go` WP-35 · `rundir.go` WP-33 · `ingress.go`, `netmux.go`, `stats.go` WP-34 · `engine.go` WP-01 · `hostenv.go`, `health.go`: no change (`TestZeroStateBackendEnv`) |
| §4.4 shared sandbox layer | `sbx/sbx.go` WP-S0 · `runner/sbx.go`, `boot/sandboxes.go` WP-S4 · `cgroup/*` WP-S2 · `vm/policy.go` WP-S0, S3 · admin `tabs/sandboxes.js`, `passes/sandboxes.js` WP-S0, S4 · `vm/manager.go`, `sandbox/*`, `term/sbx.go`: no change (`TestPinnedBackendSeesCheckpoint`'s VM variant, `TestBindSrcNotDst`) |
| §4.5 confine | `confine.go` WP-S1, S6 · its tests WP-03, S1 · `guard_test.go`: changed by `remove-cgi` (PRE-5), then no change · `git.go`: no change (`TestStoreUsesConfineOnly`) |
| §4.6 server | `static.go`, `tileassets.go`, `native.go` WP-19, 36 · `tileorigin.go`, `tilenav.go` WP-38 · `api.go` WP-19, 54 · `policy.go` WP-05 · `server.go` WP-21, 48 · `openapi.go` WP-07 · `termsessions.go`, `agentapi.go` WP-51 |
| §4.7 proxy | `proxy.go` WP-19, 37 · `ingress.go` WP-37 |
| §4.8 auth | `auth.go` WP-05, 32 · `frametoken.go` WP-32 · `assettoken.go`, `tilebinding.go` WP-38 |
| §4.9(a) broker | `resources.go` WP-18, 39, 40 · `resenc_wire.go`, `resenc/resenc.go` WP-39, 40, 45 · `vault.go` WP-43 · `cron.go`, `bussubs.go` WP-49 · `netfn.go` WP-47, 50 · `ingressfn.go` WP-50 · 08-data §14's `deployns.go` WP-41, `deployseed.go` WP-42, `deployvault.go` WP-43, `backup_deploy.go` WP-23, 44 |
| §4.9(b) broker | `broker.go` WP-05, 30, 39, 47 · `policy.go` WP-46 · `lifecycle.go`, `transfer.go` WP-53 · `backup.go` WP-23, 39, 44, 45 · `backup_cron.go` WP-44 · `backup/backup.go` WP-23, 44, 45 · `whoami.go` WP-54 · `diskmon.go`, `resusage.go` WP-39, 44 · `orgsapi.go`: no change (`TestDeployAuthzMatrix`) |
| §4.9(c) broker | `create.go` WP-49, 46 · `prs.go`, `tiles.go`, `updates.go`, `templates.go`, `templaterepo.go`, `clone.go`, `gitimport.go`, `code.go`: no change (`TestWorkTreeWritersReachOnlyLiveTarget`, `TestDeploymentsAcrossTileLife`) |
| §4.10 obs | `status.go` WP-21, 50 · `logs.go`, `prefs.go` WP-50 · `obs.go` WP-30 |
| §4.11 term | `term.go` WP-00, 05, 51 · `sandboxenv.go` WP-00, 22, 51 · `target.go` WP-30, 51 · `attach.go`, `sessions.go`, `agent.go`, `history.go` WP-51 · `binds.go`, `vm.go`: no change (`TestTerminalMasksDeploymentState`) |
| §4.12 push, ingress, VM | `push/api.go` WP-50 · `boot/push.go`, `internal/ingress/*`: no change (`TestInboundEdgesReachOnlyPrimary`) |
| §4.13 users | no change (`TestDeployAuthzMatrix`'s `noTerminal` column) |
| §4.14 cmd/bx | `main.go`, `status.go` WP-00, 26, 58 · `deploy.go`, `deployclient.go` WP-26, 58 · `livereload.go` WP-26 · `deployment.go`, `agentdeploy.go` WP-58 · `native.go`, `agent.go`, `args.go`, `cmd/xbind/main.go`: no change (`TestBxTodayInvocationsUnchanged`) |
| §4.15 SDK | `sdk/xbin.go` WP-59 · `kv.go`, `notify.go`, `http.go`: no change (`TestDataSeparation`) |
| §4.16 web | `bx-frame.js` WP-00, 25, 56 · `frame-testapi.js` WP-00, 08, 25, 56 · `frame-titlebar.js`, `frame-deploy.js`, `bx-terminal.js` WP-25, 56 · `frame-launcher.js` WP-25 · `bx-logs.js`, `frame-info.js`, `term-sessions.js`, `xbin-client.js`, `bx-code.js` WP-56 · `events-socket.js`, `native/ios/…`: no change (WP-60's tests) |
| §4.17 workspace template | `tabs/runtime.js`, `shell/menus.js`, `shell/bx-tile-admin.js`, `manager/index.html`, the optional chips WP-57 · `tabs/sandboxes.js` WP-S0, S4 · `AGENTS.md` WP-29, 65 · `bx-shell.js`, `admin.js`, `welcome/notes.js`: not edited |
| §4.18 docs | `protocol.md` WP-07, 29, 64 · `sdk.md`, `resources.md`, `auth.md` WP-64 · `elements.md`, `isolation.md`, `bx.md`, `frontend-kit.md`, `compat.md`, `maintenance.md` WP-29, 65 · `overview/01`, `03`, `04`, `index.md`, `getting-started.md` WP-29 · `overview/05`, `06`, `08`, `10`, `13`, `16` WP-65 · `overview/09`, `14`, `15` WP-29, 65 · `config.md` WP-06 · `changelog.md`, `changes/`: the integrator |
| §4.19 tests, harness, CI | goldens WP-01, 02, 03, 05 · seam tests WP-01 · `liveroute_test.go` WP-20 · checkpoint tests WP-10, 11, 12 · `resourcebinds_test.go` WP-35 · `runner/sbx_test.go` WP-S4 · `sbx/sbx_test.go` WP-S0 · broker fixture WP-30 · `gates_test.go` WP-51 · `test/deployments_test.go` WP-28, 61 · legacy fixtures: untouched (WP-03 adds files) · `shots.js`, `seed.sh`, `run.sh`, `passes/agenttab.js` WP-08 · `passes/livereload.js` WP-08, 25 · `passes/deployments.js` WP-08, 56 · `hack/deploy-state.test.mjs` WP-24, 55 · `Makefile`, `ci.yml`: the integrator |

### 7.2 Every test has an owner

- **By name.** Every test [15-test-plan.md](15-test-plan.md) names is on the
  Tests line of the card that writes or extends it, with each
  [06-security.md](06-security.md) alias joined by `=` to its name. Two are
  dropped with cgi (PRE-5), as WP-19's card records:
  `TestCgiRefusedForPinnedDeployments` and `TestNoDirectExecFlagsCGIHandler`.
  The tests that [09-fabric.md](09-fabric.md) §10 and [08-data.md](08-data.md)
  §14 add are on WP-30, 33, 34, 37, 40, 41, 42, 44, 45, 47, 48 and 50.
- **Seam rows.** 1–15 WP-01; 16–30 and 38 WP-16; 31–34 and 37 WP-33; 35–36
  WP-34.
- **Harness passes and node tests.** `livereload` WP-08 then WP-25;
  `deployments` WP-08 then WP-56; `agentTab` WP-08, WP-56; `sandboxes` WP-S0,
  WP-S4; `viewAs` WP-25; `hack/deploy-state.test.mjs` WP-24, WP-55;
  `hack/events-socket.test.mjs` and the Swift rows WP-60;
  `hack/menus.test.mjs` WP-08, WP-57.
- **Existing tests every WP keeps green**, assigned to no one:
  `TestAlwaysOnBackoff`, `TestChangedComponentsNativeEntry`,
  `TestGitDirect`, `TestGoBackendLifecycle`, `TestHostileRepoStaysInside`,
  `TestIdentifyHeaders`, `TestLegacyWorkspaceBootsTwice`,
  `TestLegacyWorkspaceBootsTwiceInProcess`, `TestMultiUser`,
  `TestRootfsBin`.
- **The mechanical check.** Run in this directory, it prints every name
  15-test-plan.md uses that no card in §2 mentions:

  ```sh
  comm -23 <(grep -oE 'Test[A-Z][A-Za-z0-9]+' 15-test-plan.md | sort -u) \
    <(sed -n '/^## 2\. /,/^## 3\. /p' 14-implementation.md | grep -oE 'Test[A-Z][A-Za-z0-9]+' | sort -u)
  ```

  Its output must be exactly the keep-green list above, plus two matches that
  aren't tests: `TestMain` and `TestFlight` (from "TestFlight build"). Any
  other name is a test without an owner.

## Divergences from the model

1. **§8 overstates what old clients ignore.** The model, and the glossary's
   Spellings row, say old clients' exact or prefix matching ignores
   `component: "<tile>+<name>"`. It doesn't for a nested tile's deployment:
   `web/events-socket.js:40-49` and the shipped app
   (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`)
   accept `<ancestor>/…`, and the old shell stores and toasts a `status` for
   any component (`workspace-template/shell/bx-shell.js:1257-1267`). Six
   sibling documents found this, yet 11-contract §3.2 keeps qualified
   `reload` and `build-*`; for this plan the open scheme blocks wave 2.1
   (R-1). **Fix:** adopt 12-compat's rule C2 in §8, with `eventComp` as the
   one switch.
2. **P24's fallback leaves a hole.** P24 (owner, 2026-09-27) settles P21
   for sessions: the terminal's API dropdown, today an on/off switch
   (`web/frame-titlebar.js:164`), defaults to the primary, never offers a
   protected primary, and then falls back to the live reload target. With
   live reload paused there is no live reload target, so a protected tile's
   sessions get no target at all; and 11-contract §7.4 and DIV-6, written
   before P24, still let sessions target a protected primary. **Fix:** fall
   back to the dropdown's existing "off", and have protecting the primary
   restart its sessions onto that default (NP-14-13); 11-contract §7.4
   follows P24 (R-2).
3. **§7's funnel change can't land in one step.** "`Runner.Ensure(comp)`
   becomes `Ensure(comp, deployment)`, and every existing caller passes the
   primary." A changed Go signature edits every caller at once, across files
   four WPs own (`internal/proxy/proxy.go:182` among them), and 07-runtime's
   Divergence 8 shows `Changed` and `aoStart` pass the deployment they act on.
   **Fix:** add `EnsureDeployment`, `TrackDeployment` and
   `ChangedDeployment` beside the old names, which become primary shims
   (15-test-plan §2.2); callers move with the WPs that own them, and
   `TestEnsureCallSitesPassPrimary` pins the rest (NP-14-8).
4. **§12 names the shared pieces but not who builds them.** The per-tile
   cgroup parent, the registry fields and the VM books serve D113 too:
   [../tile-sandboxes.md](../tile-sandboxes.md) §6 changes `Reserve`'s
   admission in its first phase and defers "a nested per-tile cgroup" to its
   second (§8). Built twice, they would collide. **Fix:** §2.5's split
   files, first-lander rule and cross-review (NP-14-6).
5. **§12's streaming run "follows D113's exec-protocol precedent", which
   reads as a dependency on unbuilt code.** D113's protocol is a design
   (../tile-sandboxes.md §2), while confine's buffered `Result`
   (`internal/confine/confine.go:87-89`) needs only caller writers.
   **Fix:** the streaming run is a confine change (07-runtime §10.6,
   NP-13-9), scheduled as WP-S6 whatever D113's state.
6. **§2's zero state has "no checkpoint store", yet opting out keeps it**
   (NP-02-2, NP-11-16). A behaviour keyed on the store would give an
   opted-out tile a terminal env that a never-opted-in tile lacks. **Fix:**
   key every behaviour on the record, as WP-22's remote injection does
   (`TestFetchRemoteInjection`; also 15-test-plan Divergence 2).

## New proposals

- **NP-14-1 — M0a lands on master first.** The splits, the seam, the goldens,
  the latency tests and WP-S0 change no behaviour, so they merge into master
  before the integration branch is cut, and concurrent work meets the split
  files at once instead of at a late merge.
- **NP-14-2 — `boot.go` is edited by contracts WPs only.** After WP-06, only
  WP-30 and the integrator's amendments edit `internal/boot/boot.go`; every
  hook a feature needs is installed there by a contracts WP, with a body that
  gives today's behaviour.
- **NP-14-3 — Generic operation dispatch.** WP-15's handlers decode a route's
  body into the request type its op registers and call the plane's op
  registry; an unregistered op answers 501. M2's operations register in
  `internal/deployments` without touching the handler file.
- **NP-14-4 — Reserved routes, and `features` per milestone.** Routes whose
  operations aren't built answer 501 and are documented as reserved;
  `features` lists `live-reload/1` from M1 and `deployments/1` from M2
  (extending NP-11-13), so M1 can ship alone and clients can tell what works.
- **NP-14-5 — A ship-dark switch.** One `boot.Config` field, enforced in the
  plane's authorize function (§6.3): while off, operations that create or
  extend deployment state are refused with kind `policy`, and those that
  return a tile to the zero state stay allowed. The zero-state path never
  reads it.
- **NP-14-6 — Shared-layer ownership with D113.** This feature's WP-S series
  owns `internal/cgroup`, the deployment fields of `internal/sbx/sbx.go`,
  `internal/vm/reserve.go`, `internal/confine` and the rows of
  `internal/boot/sandboxes.go`; D113 owns `Spec.AgentFD`, its policy fields,
  its API file and its manager. `Reserve(owner, memMiB, opts...)` is declared
  once and both projects add options to one type. Whichever lands a shared
  piece first builds it to 07-runtime §10; the other reviews.
- **NP-14-7 — A port grid for the harness.** `PORT = 8700 + 20 × N` for WP-N
  and `9900 + 20 × k` for WP-S<k>, with `HARNESS_DIR` in the agent's
  scratchpad, so no two agents' derived ports collide.
- **NP-14-8 — Additive runner names.** `EnsureDeployment`, `TrackDeployment`
  and `ChangedDeployment` beside `Ensure`, `Track` and `Changed`, which become
  primary shims; no caller changes until its owner moves it.
- **NP-14-9 — Legacy-fixture assertions in new files.** 12-compat §6.1's "no
  deployment state" checks run as new test functions reusing the fixtures'
  helpers, so neither allowed list nor fixture file changes.
- **NP-14-10 — One builder page.** `docs/tile-deployments.md`, written by
  WP-29 for pausing live reload and extended by WP-65, linked from
  `docs/index.md` and `docs/elements.md` (the page 12-compat G1 asks for).
- **NP-14-11 — An M2 interfaces WP.** WP-30 opens M2 by declaring every
  cross-WP broker, obs and term interface, the M2 request types and the
  shared broker fixture, before the parallel waves start.
- **NP-14-12 — Gated WPs never hold a milestone.** WP-38, WP-45 and WP-66
  land complete or not at all; a milestone ships without them, with the
  refusal where one exists.
- **NP-14-13 — P24's last fallback is "API off".** When neither the primary
  (protected) nor a live reload target (paused) can be offered, a new
  session's API dropdown defaults to its existing "off"
  (`web/frame-titlebar.js:164`). Protecting the primary restarts every
  session that targets it onto P24's default, since a target is fixed for a
  session's life (11-contract NP-11-2).
