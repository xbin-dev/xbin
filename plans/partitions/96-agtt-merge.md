# Merging AgTT × coding harnesses with partitions

Status: plan (2026-09-30). The `agtt-harness` branch (6f4118f3, 139 commits
over master bf5a56b1; its decision entry and spec `plans/agtt-harness.md` there) adds coding
agents to the agent template: ACP harnesses in coding sandboxes, the
`sdk/acp` packages, and sandbox-manager contract additions (`harnesses` in
hello, backend terminals, the optional `stdio` capability). It was built on
master, where the agent template has one backend. The partitions branch
runs the agent in three modes (legacy, global, a person's partition) with
two homes. This file is the plan for bringing the two together.

Three read-only reviews (backend/engine, contract/fabric, frontend)
informed it. They did a trial merge with the resolutions below and got
green `go vet`, the agent backend's tests and 231 JS tests. They also
found that nothing in agtt-harness knows the partition modes.

## When

**A — now, on master:** merge `agtt-harness` into master. It is green on
its own, and master has no partitions, so none of the refactors below
apply there.

**B — after wave 5 is merged and wired on partitions:** a dedicated wave,
**W6 "AgTT under partitions"**, before I2's rollout.
- Wave 5 (B2d hosted chats, AF's moves, I1's suites) edits the same agent
  backend and frontend. Merging agtt into partitions while wave 5 runs
  would move its base under five packs.
- W6 also needs B2d's hosted model, which exists only once wave 5 lands, to
  refuse hosted harnesses.
- The partitions branch is held back from master until this lands (90 §I).

**C — I2 rollout** once W6 is green and I1's suites are rerun on the merged
tree.

## A. Master (steps from the agtt hand-off)

1. The fix/agent-regressions step is done: master is bf5a56b1.
2. `git merge --no-ff agtt-harness`. It fast-forwards in content, and the
   merge commit is for history. agtt's decision takes the number partitions skipped.
3. Verify with CI's Go:
   - `make check`;
   - `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh`.

   Known flakes also fail on master; rerun them alone:
   - TestPinsDeployIdenticalCode;
   - agentcore TestNamespaceAgent/rootfs;
   - TestLiveReloadPauseRace/python.
4. Then `fix/go-build-isolation` (per-tile Go build workspaces) merges on
   top, re-verified. Its base is bf5a56b1; expect little overlap (runner and
   deps vs tilesbx).
5. No push, tag or release without the owner. After a push, the sdk pin
   bump in the root go.mod (xbind now imports `sdk/acp`) and the worktree
   and branch cleanup follow, as in agtt's hand-off.

## B. W6 — AgTT under partitions

### B0. Merge master into partitions (the integrator)

The trial gives 23 conflicting files. Resolutions:

- **Agent backend:**
  - `routes.go`: keep `homeRoutes(mux)`, agtt's route appends,
    `agentRole(guard(rt.need, partitionRoute(rt.pattern, rt.h)))` and
    `mailboxRoutes(mux)`.
  - `ask.go`: keep both `Share` and `Harness`; the tail is `shareNew`, then
    `runAnswer`.
  - `triggers_admin.go`: agtt's harness 400 checks go before
    `registerPrivate`, beside `hostedEditRefused`.
- **Fakes:** in `hack/fakesandbox/fsb.go`, caps are the union (`stdio` and
  `partitions`), and `boxCaps` keeps `stdio` and never `partitions`. Copy
  it byte-identical over both mirrors (the agent's and sandbox-terminal's
  `fsb_fake_test.go`; `TestMirror`). `fsb_test.go`: Strict, the union caps,
  and all four new tests.
- **sandbox-terminal:** `tile.json` goes to v6. Its changelog order is v6
  (partitions' v3 text), then agtt's v5, v4, v3, then v2, v1.
  `hack/tile-versions.txt`: s3-archiver 3, sandbox-terminal 6; regenerate
  with `UPDATE_TILE_VERSIONS=1`.
- **coding-sandbox:**
  - `manager.go`: take partitions' deletion, then port agtt's widened
    `mutates` (any `Upgrade`, a path ending `/stdio`) into `access.go`.
    Taking the partitions side alone silently loses it, and page readers
    could then take an exec's stdin.
  - `API.md`: the caps, ports and §2/§3 texts are the union, per the
    fabric review.
  - `contract_test.go`: Strict, Caps = exec, files, tar, stdio, snapshots,
    clone, partitions.
- **Contract and docs:**
  - `sdk/sandboxcontract/sandboxes.go`: one allowed-caps list holding
    `stdio` and `partitions`.
  - `docs/sandbox-manager.md`: the caps example has both words; the
    optional-caps and suite paragraphs are the union.
  - `test/isolated/codingsandbox_test.go`: the skip map starts with
    `user-partitions`; `stdio/refusals` joins the no-auth list; hello
    expects `[… ports stdio partitions]`.
- **Frontend:**
  - `agent.js`: both import sets; the top bar is `ext.top(v)`, then the
    share/publish button; in `n-create` the `share` goes last in the
    `Object.assign`.
  - `model/app.js`, `native/home.js`, `native/ui.js`: both import sets
    (home: notices, then `homeSetupTpl()`).
  - `model/features.js`: partitions' three `state.partition.*` entries plus
    `tools.terminal.tabs`. Drop `tools.sandboxes.terminal`, which is stale.
  - `hack/ui-harness/shots.js`: partitions' side, plus
    `PASSES.agentHarness = …` on its own line.
- **Records:**
  - `docs/changelog.md`: one `## 2026-09-30` heading holding both blocks.
  - `plans/DECISIONS.md`: partitions' side, with AgTT's decision inserted between
    D146 and D148.

After B0: `go vet`, the agent backend's tests, `make js-test`,
sizebudget and `TestPartitionRouteClasses` (it fails until F-M1).

### B1. Fabric: MUST

- **F-M1:** the new xbind route `GET /sandboxes/{name}/execs/{id}/stdio`
  needs `GlobalOnlyRefused` in `partitionclass.go`, like its siblings
  (PD-28, W1).
- **F-M2:** the `mutates` port into `access.go` (B0), pinned by coding-sandbox's
  `TestPageReaders`.
- **F-M3:** contract text on who the person is. From a user partition the
  person is the partition's and is verified: the manager applies the
  person rules, and any other `Sbx-User` gets 403. agtt's "asserted, not
  verified" text needs correcting in:
  - sandbox-manager.md §Terminals and §stdio;
  - sdk.md;
  - `docs/changes/2026-09-30-manager-terminals-for-backends.md`;
  - the `ManagerTTYOptions` / `ManagerStdioOptions` comments;
  - the tty.go check comment.

  agtt's "§Partitions, sharing and people" becomes "§Consumers, sharing and
  people", so that "partition" means one thing.
- **F-M4:** user-partitions suite cases for the sockets, gated on
  `tty`/`stdio`:
  - another partition, global and an asserted person dialing
    `…/tty`, `…/execs/{id}/tty` and `…/execs/{id}/stdio` of a
    partition-homed sandbox get 404;
  - `alice.Asserting("mallory")` gets 403;
  - alice's partition opening a tty in carol's private global-home sandbox
    gets 404;
  - a team sandbox at global attaches.

  Add `user-partitions` to fakesandbox's `TestContractBeforeStdio` and
  `TestContractPolicingTTY` skip lists.

### B2. Agent backend: MUST

- **A-M1, coding agents at global (90 §I15):** in `globalMode()`, refuse
  `harness` on `/ask` and `/runs`, hide the spawn tool, and answer 409 on
  `authenticate` and the `login=1` relay. Channel- or trigger-started
  global runs must never reach a harness.
- **A-M2, no hosted harness:** an engine=harness conversation can't become
  hosted, and spawning a harness in a hosted conversation is refused
  (B2d).
- **A-M3, no moving a harness conversation between homes:** publish/copy,
  AF's un-share move and `exportConv` answer 409 for a harness root, or a
  tree with live harness children. The page hides the actions (F-S3).
- **A-M4, the user-mode wake counts harness work:**
  - `resume_mode.go` `userWake` consults `harnessWorkSQL`;
  - a prompt in `sending` counts as work;
  - an adapter up with no turn leaves one `wake` at
    `last_active_ms + harnessIdleMin`, never `@every 1m`.
- **A-M5, the brake reaches harness runs in a partition:**
  - `harnessPass`'s halted branch calls `e.onBrake(run)`;
  - a halt read from conf cancels a live harness turn, via durable events
    or `recheckSoon`;
  - fail-closed conf parks harness runs with a release timer.
- **A-S2:** the terminal relay (`personSandbox`) goes through
  `managerHello` and `partitionBoxRefusal`, like every sandbox use.
- **A-S3:** in the catalog's sandboxes and the remembered
  `prefs/harness-sandbox`, a partition keeps only the sandboxes homed there,
  keyed by home.
- **A-S4:** an idle adapter doesn't hold a person's partition for 15
  minutes. Release the hold and rely on A-M4's wake.
- **A-S6:** harness session counts (no content) in `usage.go`'s daily
  totals.

### B3. Agent frontend: MUST

- **U-M1:** `model/harness-store.js` `call()` routes by the run's home:
  `x.fetch(url, at(homeOf(runOfPath(path)), opts))`. Every harness action
  in a shared conversation 404s today.
- **U-M2:** native run relay (`model/terminals.js` `runTerminalSrc`)
  appends `xbin-partition=global` for a global-home id.
- **U-M3:** in the `user` state, the harness sandbox pick treats rows not
  homed in this partition as not fitting, with the reason, and offers
  Create. Ideally the backend gives each row a `homed` / `bindWhy` field
  (pairs with A-S3).
- **U-M4:** sign-in is offered only where credentials stay the person's:
  legacy as today, and in the `user` state for runs homed there. Elsewhere
  a read-only card says why. This belongs in the model, so web and native
  follow it.
- **U-M5:** in the new-chat dialog, a share other than "mine" disables
  "Who answers" (the harness choice). In the `global` state the agent
  picker hides harnesses.
- **U-S3:** hide Share a copy / Copy to my own space on harness
  conversations (A-M3).
- **U-S4:** document that native relay terminals hold the person's
  partition while their screen is up.
- **U-S5:**
  - a `state.partition.harness` feature in all three registries;
  - harness cases in `hack/agent-template-homes.test.mjs` (`user` and
    `global` states);
  - a shared harness row in `test/homes.mjs`.

### B4. Docs and records

- **§Partitioned consumers (AR-23):** extend it to cover two new risks.
  Stdio attach takes over a coding agent's stdin, and harness logins in a
  shared sandbox's `$HOME` serve everyone who sees it.
- **Hello's `harnesses.login` paragraph:** the same note, plus that
  operator-set harness `argv`/`login` are part of the trust base.
- **coding-sandbox API.md:** the stdio bullet says "the consumer (a user
  partition is its own), the person rules".
- **A new D-number** (the next free one at fold time; the go-build-isolation fix on master may take the next)
  records "coding agents under partitions": the owner's §I15 decision and
  the refusals. AgTT's decision gets a dated amendment pointing to it.
- `plans/agtt-harness.md` §2.2's line on partitions' D-numbers is stale.

### B5. Tests

- **Partition × harness e2e** on a real isolated xbind (`test/isolated`):
  - a private harness in a partition-homed sandbox (the fake adapter);
  - an old manager shown as `manager-error`;
  - the A-M1/A-M2/A-M3 refusals;
  - a halt reaching a harness in a partition;
  - the A-M4 wake;
  - another partition's stdio dial gets 404 (F-S4).
- **UI harness:** `agentHarness` doesn't run under `HARNESS_ISOLATE` (its
  fake adapter is a host path). Make a partitioned variant, or record the
  gap for I1.
- **Rerun:** I1's suites and all `TestPartitions*` smokes on the merged
  tree, then `make check`.

### Shape of W6

1. **B0**, by the integrator: merge the conflicts on `pt/w6-base`.
2. Three packs in parallel on that base, each reviewed and fixed:
   - **W6-F** fabric: B1 plus the fabric part of B4;
   - **W6-A** agent backend: B2;
   - **W6-U** agent frontend: B3.
3. A wiring pass: B5, the records, the fold.

Rough size: B0 S–M; W6-F S–M; W6-A M; W6-U M.

## Risks

- **No test yet combines a partition with a harness.** A-M4 and A-M5 are
  silent today, so B5 is not optional.
- **AR-23 grows:** stdio takeover, and harness credentials in team or
  shared sandboxes and their clones. §I15's recommendation keeps
  credentials out of global, but a person may still share their own
  sandbox.
- **ACP "allow always" is per session.** If a session were ever shared
  across people, one person's allowance would cover everyone. It isn't
  under §I15(a); revisit with hosted harnesses.
- **Silent losses in the merge:** F-M2's `mutates` rule and F-M3's doc
  anchors. The B0 checklist names them.
