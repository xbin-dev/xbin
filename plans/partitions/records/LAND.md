# LAND — landing the partitions branch on master v0.3.65

Step 1 of landing the partitioned-tiles work (the owner: "Main thing to do
now: land all of the partitions work"): master merged into the branch and
made green, as `pt/land` — `partitions` at 94d061d3 with master at
bb16fded (v0.3.65, ~105 commits past the branch's last master merge)
merged in — then the parallel CI on a draft PR (#10). The integrator
merges it into master; master was never pushed from here. Decision: D177
(plans/DECISIONS.md), which also carries the owner's ruling of the same
day on agent instances (below).

## Commits

| commit | what |
|---|---|
| `cb851618` | the merge: 19 conflicted files resolved (below), plus the integrations a compile needed (the proxy's rerouting for partitions, claimLayer's layerDir) |
| `0b6aef59` | partition classes for master's new routes; server.go and broker.go split under the 800-line cap |
| `bdbb0d21` | a person's partition swaps like a deployment (D173 rerouting, D174 discard), G1 counts it once; runner.go back under its budget |
| `949906ca` | base auto-update moves people's layers (D175): tests and docs |
| `0bef4ced` | the agent engine's false takeover fixed at the root (epoch read errors) |
| `972cadc8` | a base move's note goes to the person whose layer moved |
| `f4b6d899`, `8d7b01f9`, `8d51b03c` | the owner's ruling: every agent instance asks to become partitioned when it takes the template's update (D177, PD-52 amended), and the e2e that pinned the old behaviour |
| `4016df41` | a dry run's partition preflight extracts no tree when the manifest stays (the latency budget) |
| `0887e57a` | the local shard timings from the green run; oldScaffold meets v0.3.65 |
| (this record) | LAND.md |

## Conflicts and their resolutions

- **Makefile** — master's `integration` target is `hack/testshard run`;
  the branch had added `TestPartitionLayer` to the `./internal/term/` line.
  Taken master's target; `TestPartitionLayer` joins the `term` suite's run
  filter in `hack/integration.jsonc` (and `hack/testshard`'s
  TestSuitesList, which pins that filter's prefixes).
- **builtin-tiles/sandbox-terminal/tile.json** — master was already at v7
  (v6: x/crypto v0.57 security fixes; v7: an SSH client leaving while its
  command starts), the branch's fake-manager change was its own v6. Not
  "v7 with both" as briefed: **v8**, its changelog the branch's text then
  master's v7…v1. `hack/tile-versions.txt` regenerated
  (UPDATE_TILE_VERSIONS=1); its s3-archiver line (the branch's v3) needed
  nothing else. The unreleased changelog entry naming the tile's API.md
  says v8.
- **cmd/bx/main.go** — the usage concatenation: `policiesUsage +
  partitionUsage + settingsUsage`. **docs/bx.md** — both commands' lines
  and paragraphs (`bx policies`/`bx partition …` then `bx settings`).
- **docs/changelog.md** — merged by date section (a script over base,
  ours and theirs): every entry of both sides once, the branch's new ones
  first within a date; the one base entry the branch had corrected kept
  as corrected. Verified: the result differs from the branch's file only by
  added lines, and from master's only by the branch's correction.
- **plans/DECISIONS.md** — ascending: D137–D146, D147, D148–D165, **D166
  (master's)**, D167–D172, D173–D176; every block of both sides appears
  exactly once (checked by script). D177 appended.
- **docs/protocol.md** — `/alerts` (both kinds, `policies` and
  `go-build-versions`, and `dismiss`), `/workspace-policies` and
  `/workspace-settings` side by side, the primary-only list naming both
  writes, both events.
- **internal/server/openapi.go**, **deployclass.go** — both sides' rows.
- **internal/broker/diskmon.go** — `apiAlerts`: the branch's partition,
  trust, policies, backup-key and consent alerts, then master's
  `AdminAlerts` (the Go build versions alert), all admin-only as before.
- **internal/server/server.go** — `/ws/term/env` answers per person
  (`EnvStatusFor`, the branch) and says `baseAutoUpdate` (master).
- **internal/runner/runner.go** — the Runner struct: master's `GoVersions`
  and the branch's `PartitionHooks`/`parts`.
- **internal/proxy/proxy.go**, **deploy_test.go** — see D173 below.
- **internal/term/term.go**, **base.go** — see D175 below; the Manager
  struct has both sides' fields.
- **workspace-template/tiles/admin/admin.js**, **API.md** — the branch had
  moved plain tabs into `plain-tabs.js`; master's new **terminals** tab is
  one more plain tab there. Workspace tabs: branding, xbin app, terminals,
  policies. master's `alertBar` import kept. API.md lists both features
  and both routes.
- **The agent template** — no textual conflict: `harness_shapes_test.go`
  is master's root-cause version, byte for byte.
- **hack/ui-harness/shots.js** — no conflict (master didn't touch the
  harness).

## Semantic integrations decided

1. **D173 × partitions: rerouting, applied.** A person's partition swaps
   (a save restarts every live partition onto the new build; stopGen
   retires the old generation), so a request routed just before meets the
   closed socket D173 fixed. `ensureTarget` now answers a `rerouting`
   transport on both paths; the partition path's `again` is the same
   `EnsurePartition` — same partition, same start class, through
   partitionGate and admission again, never Route again (as D173 never
   re-asks Route). The call's one hold spans both attempts (the runner
   tracks the partition's state, not a generation). `proxy.PartitionRunner`
   answers a `PartitionGen` (Sock, Retired): `runner.Gen` through boot's
   adapter over the new `EnsurePartitionGen`; the fakes return their own.
   Tests: `TestPartitionProxyRerouteRetired`, and the runner half of
   `TestPartitionWorkTreeGenerationAfterPause`.
2. **D174 × partitions: already asked, its discard fixed.** Partitions
   build through `buildAndStart`, so a work-tree build asks
   `SettledCodeFor`. But D174's discard stopped the generation as a
   primary's while `partSpawned` had made it the partition's `starting`
   generation with a registered token: a stop and the running count kept
   seeing a dead generation, and the token lived until the process exited.
   `discardGen` stops it as `install` stops an uninstalled one: token
   revoked first, `starting` cleared. `TestPartitionWorkTreeGenerationAfterPause`
   (fails without the fix: "a generation that never served is still the
   partition's starting one", "the discarded generation's token still
   authenticates").
3. **D175 × F7a person layers.** `claimLayer` finds a layer by key
   (`layerDir`: `.xbin/term/<key>` or `.xbin/term-part/<TK>/<pkey>`), so a
   person's layer is pinned, refused or moved exactly as a tile's — at the
   person's next session start, never under a running session of theirs
   (held → ephemeral), never the tile's or another person's;
   `layerOutdated` reads the same way; `CheckBaseImages` lists a missing
   person layer by its layer key. The move note an agent session's start
   leaves for the next shell is keyed by the claimed layer
   (`o.layerKey`), not the tile: before, ana's agent moving her layer told
   bob's next shell (and that someone's agent ran — PD-09) and not hers.
   **The switch-wipe and the move don't fight:** a switch waits for opens
   in flight before its wipe (partitionhold.go) and the move happens
   inside an open; what a move puts aside is in `.xbin/term-moved/`, the
   remover's alone. Tests: `TestPartitionLayerBaseAutoUpdate`,
   `TestPartitionMoveNotes` (unit), `TestPartitionLayerBaseMoveIsolated`
   (real sandboxes, in the `term` integration suite).
4. **D166 G1:** keyed by registry path; a partition's view keeps the
   tile's path and a partitioned tile builds once per change — one
   baseline entry, one alert line. `TestGoVersionsPartitionedTile`. The
   partitioned agent template and the partition fixtures build under the
   per-tile go.work (tile-check, the isolated suites).
5. **Route classes** for master's routes (`TestPartitionRouteClasses`):
   `GET /workspace-settings` neutral; `PUT /workspace-settings` and the
   three `/go-build-versions` routes global-only (refused).
6. **Size budgets:** split, never raised — `/ws/term/env` handlers to
   `server/termenv.go`, the disk monitor's answers to
   `broker/diskstatus.go`, `ensurePrimary` to `runner/gen.go`.
7. **Kept apart, not unified:** `bx policies` / `/workspace-policies`
   (PD-55, `data/workspace-policies.json`) and `bx settings` /
   `/workspace-settings` (D175, released, `data/workspace-settings.json`)
   are two admin switch sets with two files and two console tabs. Folding
   them is a compat question (the released route and file can't move);
   left to the owner.

## Fixes

- **The agent engine's false takeover.** `Engine.fenced` scanned the epoch
  with its error dropped; a failed read was 0 ≠ the owner's epoch, so the
  only engine logged "another engine took over this database" and stopped
  driving runs (CI, TestHarnessQueuedPark). `readEpoch` answers 0 only for
  a missing row; `fenced` and `takeOver` try their transaction again
  (5 tries, 20–80 ms apart; fn not yet run) and return what keeps failing,
  nothing written, the engine still the owner; the harness pipe's guard
  refuses that one write instead of answering errFenced (and reads the
  engine's own epoch key); a takeover no longer rewinds the epoch to 1
  over a failed read. `TestEpochReadFailureIsNoTakeover` injects the
  failures through a seam.

- **A dry run's partition preflight missed the latency budget.** The merged
  tree's first local `make integration` failed the alone pass:
  TestLatencyLifecycleOps' "checkpoint" (a dry run of reload now on a paused
  tile) had a median of 523 ms (R-go) and 550 ms (R-node) against a 500 ms
  target, where master on this machine measures 422/432 ms. Not load: with
  the preflight switched off (a probe build) the branch measured 422/425 ms.
  The branch's `partitionPreflight` (PD-44, 01 §2.7) extracted the moving
  checkpoint's whole tree to read one manifest, ~100 ms. It now reads the
  manifest from the tree the primary already runs when the move's diff
  (which the dry run computes anyway) shows `xbin.json` unchanged — the
  same file, so the same answer; a diff naming the manifest, cut short or
  failed reads the moving tree as before. TestPartitionPreflightReadsNoNewTree
  (fails without the fix: "1 new trees extracted").

## The owner's ruling folded in: every agent instance becomes partitioned

"After this update all AgTT instances should become partitioned, no
migration from legacy needed" (2026-10-01; overrides PD-52 for the agent
template only). Found: what reaches an agent instance is its template's
update by `git merge template/main` (D50) — `bx builtin update` never
touches template instances. So the template block's new
`"partitionOnUpdate": true` makes the served repo, under `--isolate`,
carry `"partition": ["user","global"]` on the line after the opening brace
(where a new instance has it; the two merge as one line), and the manifest
merge driver takes an upstream `partition` where neither base nor ours
names one. PD-44 does the rest: with data, pending until a manager
switches (wipes; no migration) or keeps (the legacy path); empty, it
switches; without `--isolate` nothing is served. Once served the ask never
changes or goes away. The U-M4 sign-in rule is unchanged. Tests:
`TestAgentTemplateUpdateRequestsPartition`,
`TestAgentTemplateUpdateWithoutIsolation`, `TestKeptKeysConflict`,
`TestOldInstanceTakesRequestedPartition`. Docs: partitions.md,
03-components, elements.md, the template's API.md, a BREAKING changelog
entry with `docs/changes/2026-10-01-agent-instances-partitioned.md`, and
the branch's unreleased entries that said "existing instances keep their
mode" made true; PD-52 (amended), PD-44 and PD-35 noted.

## Follow-ups (none blocks landing)

- `bx policies` / `/workspace-policies` and `bx settings` /
  `/workspace-settings` stay two admin switch sets (above): the owner's
  call whether to fold them.
- A rerouted partition request whose second `EnsurePartition` is refused
  (the caps met during the swap) answers the transport's 502 with the
  refusal's text, where a first ensure answers 503 — rerouting's error
  path is the deployments' too (D173).
- The base-move line says "this tile's terminal moved…" also for a
  person's own layer; a move note left for a person whose layer a switch
  then wiped is still said once. Wording only.
- CI's timing profile has no entries for the partitions tests; they hash
  into shards, and the isolated ones skip there (no rootfs) — refresh the
  `ci` profile from a CI run's logs when convenient
  (`testshard timings -profile ci`).
- The downgrade tests stay pinned to v0.3.61 (CI's choice: the last
  release before tile deployments); a v0.3.65 binary speaks deployments,
  which makes them skip by design.

## Changelog and decisions

- docs/changelog.md (2026-10-01): "BREAKING: existing agent instances ask
  to become partitioned …", "Partitioned tiles: people's partitions swap,
  and their terminal layers move base, like a tile's", "Fix: the agent
  template's engine no longer stops on a failed read of its epoch".
- plans/DECISIONS.md: D177 (the integrations above, and the ruling).

## Results

All on this tree (pt/land), CI's Go 1.26.3 (GOTOOLCHAIN=local), the
.dev.mk env (rootfs, gocryptfs, fuse-overlayfs, VM assets), TMPDIR a short
real directory (`/tmp/claude-1000/lt`: a long one breaks unix-socket paths,
and a symlinked one breaks go.work and landlock paths — both seen and
discarded as environment failures, never counted), the base-GC rootfs copy
on /home (XBIN_ITEST_DIR, for reflinks and /tmp's inodes).

- **`make check`**: green (guards, `go test ./...`, the sdk and relay
  modules; TestNoPartitionGolden among them).
- **`TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh`**: 9 backends
  green (the agent template's 447 s under -race).
- **`make integration`** (every shard at once): green in 939 s — 2/4 369 s,
  1/4 396 s, 3/4 479 s, 4/4 631 s, then the alone latency tests
  (TestLatencyLifecycleOps R-go/R-node checkpoint 425/429 ms against
  500 ms, after the preflight fix). TestZeroStateRoute,
  TestPartitionLayer{Isolated,PerPerson,BaseAutoUpdate,BaseMoveIsolated},
  TestDowngradePartitions, TestDowngradeStatic and
  TestDowngradeDormantRegistrations (against v0.3.61, CI's pin) pass.
  SKIP lines:
  - internal/vm (vm, vm-emulate), tilesbx's and runner's `/vm` subtests:
    "VM sandboxes unavailable here: missing [xbin-vmagent]" in that run —
    then `make integration SHARD=vm` with bin/xbin-vmagent built: 48 tests,
    every one passing, none skipped (vm 48 s, vm-emulate 76 s);
  - TestRestrictNSChild, TestMountGuardChild, TestReadGuardChild:
    child-only helpers (by design);
  - TestPartitionsSmokeReap: "the idle stop takes over ten minutes:
    XBIN_SMOKE_REAP=1 runs it";
  - TestCodingSandboxContract{,VM}/user-partitions/{shares,recreated,person,apart},
    /tty/unsupported, /lifecycle/archive: "skipped by the target" (the
    contract suite's own reasons, unchanged from the branch);
  - TestHarnessLive: "the real adapters are skipped: XBIN_HARNESS_LIVE=1".
- **The isolated partitions suites**, `go test -tags=integration -count=1
  -parallel 3 -run 'TestPartitions|TestCodingSandbox|TestHarness'
  ./test/isolated/`: ok in 888 s, 19 PASS (TestPartitionsTemplateMerge
  with D177's new expectations, TestPartitionsAgentHarness 198 s, the
  coding-sandbox suites in both modes), SKIP TestPartitionsSmokeReap (as
  above).
- **The downgrade suite**: the three TestDowngrade* above (./test).
- **UI harness** (PORT=9141, HARNESS_DIR=…/scratchpad/land/h, ws/ deleted
  after each run): personPage 47, adminPartitions 36, partitionLogs 13,
  partitionConsent 36, partitionMark 46, partitionsEntry 15, agentHarness
  72 PASS (default harness); oldScaffold 15 PASS (HARNESS_NO_OVERLAY=1,
  against v0.3.65); agentHomes 20, agentHosted 24, agentMoves 17,
  channelsPartitioned 15 PASS (HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1).
  No FAIL, no SKIP.
- **CI** (draft PR #10, 13 parallel jobs): run 36874706945 on `972cadc8`
  green in 3 min 39 s (and the iOS workflow in 5 min); run 36883497928 on
  `4016df41` green in 3 min 29 s; the final head: see the PR.
- **Flakes**: none seen. The first local integration attempts failed only on
  environment (the TMPDIR paths above); the two real failures were fixed at
  the root — TestPartitionsTemplateMerge (the test's expectation, changed by
  the owner's ruling) and the latency budget (the preflight).
