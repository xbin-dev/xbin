# W6-base — master (AgTT × coding harnesses) merged into partitions

W6's B0 ([96-agtt-merge.md](../96-agtt-merge.md) §B0): `partitions` at
cb141c31 (wave 5 in and wired) merged with `master` at d7748301 (the
`agtt-harness` merge, D147, and the agent regressions). Branch
`pt/w6-base`; the base the three W6 packs (W6-F fabric, W6-A agent
backend, W6-U agent frontend) start from. It edits `docs/changelog.md` and
`plans/DECISIONS.md` only as the merge requires (both sides kept; D147's
block between D146 and D148). No new feature, no new xbind HTTP/WS surface
of its own: the stdio route it classifies is master's, already in
[/docs/protocol.md](/docs/protocol.md).

Commits (oldest first): the merge (`4da83106`), F-M1 (`4f57cd9b`), F-M2
(`43af5a2a`), and this record.

## The conflicts, one line each

The trial merge's 23 files plus seven wave 5 added (30). Where both sides
changed the same lines, partitions' structure is kept and AgTT's addition
re-applied inside it.

Agent backend:

1. `_backend/routes.go` — `homeRoutes`, `hostedRoutes`, `moveRoutes`,
   `fetchRoutes` before the loop; the loop over AgTT's appended tables
   (`harnessRoutes`, `harnessRelayRoutes`, `harnessAPIRoutes`) with
   `agentRole(hostedRoute(…, guard(…, partitionRoute(…))))` — B2d's wrapper
   outermost inside the role gate, as `homeRoutes` has it (unchanged);
   `mailboxRoutes` after.
2. `_backend/ask.go` — the body has both `Share` and `Harness`; the tail is
   `shareNew`, then `runAnswer(run)`.
3. `_backend/triggers_admin.go` — create: AgTT's harness-target 400 right
   after the target check, then `sharesInPartition`, `refusePrivateAtGlobal`,
   `registerPrivate`; update: AgTT's `harness` patch 400 first (as on
   master, right after decoding), then `hostedEditRefused`; the update's
   harness-target 400 before `sharesInPartition` and `registerPrivate`.
4. `_backend/fsb_fake_test.go` — a byte copy of the resolved
   `hack/fakesandbox/fsb.go` (TestMirror).

Fakes, contract, coding-sandbox, sandbox-terminal:

5. `hack/fakesandbox/fsb.go` — hello's caps are the union (`tty` where the
   host has ptys, `stdio`, and `partitions` last); `boxCaps` (partitions')
   drops only `partitions`, so a sandbox's caps carry `stdio`.
6. `hack/fakesandbox/fsb_test.go` — TestContract: `Strict` and the union of
   the two `Caps` lists (`… archive partitions`); both new tests kept
   (`TestFakeWithoutPartitions`, then `TestFakeHarnessesAndTTYEnv`), with
   master's `TestContractBeforeStdio` and `TestContractPolicingTTY`
   (auto-merged) — the four.
7. `builtin-tiles/sandbox-terminal/backend/fsb_fake_test.go` — the same
   byte copy.
8. `builtin-tiles/sandbox-terminal/tile.json` — version 6; changelog v6
   (partitions' v3 text), then AgTT's v5, v4, v3, then v2, v1.
9. `hack/tile-versions.txt` — regenerated (`UPDATE_TILE_VERSIONS=1 go test
   ./internal/builtins -run TestTileVersions`): s3-archiver 3 (partitions'),
   sandbox-terminal 6.
10. `builtin-templates/coding-sandbox/_backend/manager.go` — partitions'
    deletion (the "who is asking" block moved to `access.go`); AgTT's only
    change inside it, the widened `mutates`, is F-M2 below.
11. `builtin-templates/coding-sandbox/_backend/contract_test.go` — `Strict`;
    Caps exec, files, tar, stdio, snapshots, clone, partitions.
12. `builtin-templates/coding-sandbox/API.md` — `caps` names `ports` and
    `stdio` and keeps partitions' "Hello's also carry `partitions`"; AgTT's
    stdio bullet; the Ports bullet keeps AgTT's "this page's readers get
    403 … whatever the method"; both bullets say "the consumer, the person
    rules" (partitions' word — AgTT's text said "the partition"); the
    on-xbin hello is `exec files tar tty stdio snapshots clone ports` plus
    the manager's `partitions`; the no-auth skip list gains
    `stdio/refusals` and keeps partitions' `user-partitions` sentence.
13. `sdk/sandboxcontract/sandboxes.go` — one allowed-caps list with `stdio`
    and `partitions`.
14. `docs/sandbox-manager.md` — the hello example's caps hold both words;
    the optional-caps paragraph is AgTT's (with `stdio`) plus partitions'
    `partitions` sentence, then AgTT's image/harnesses paragraphs; the
    suite paragraph names the partition headers (partitions'), AgTT's
    `stdio`/`tty/backend`/`caps/missing` sentences, then partitions'
    `partitions`/`user-partitions` sections.
15. `test/isolated/codingsandbox_test.go` — the skip map from
    `csPartitionSkips` (so it starts with `user-partitions`) plus the
    no-auth keys with `stdio/refusals`; AgTT's `caps` (stdio unless remote)
    and `Strict` beside partitions' `Partition` hook; hello expects
    `[exec files tar tty snapshots clone ports stdio partitions]` (a remote
    xbind without stdio: without `stdio`).

Agent frontend:

16. `agent.js` — both import sets; the top bar is `ext.top(v)`, then
    partitions' conditional share/publish button; `n-create` sends
    `Object.assign({…}, ...newExt bodies, share)` — `share` last, after
    partitions' `newShare()` check.
17. `model/app.js` — both import sets.
18. `model/features.js` — native DIFFERENCES: partitions' five
    `state.partition.*` entries (publish, copy, newShared, host, copyIn)
    and AgTT's `tools.terminal.tabs`; the stale `tools.sandboxes.terminal`
    difference dropped (the app relays terminals now).
19. `model/rules.js` — both `hosted` and `harness`/`hasCompact`; retry is
    AgTT's; compact `talk && !hosted && (!harness || hasCompact)`; learn
    `talk && !hosted && !harness`; memory AgTT's.
20. `model/sandbox-store.js` — both import sets; AgTT's `recheckRefs` on the
    per-home state (`at().loadedAt`, `at().inflight`); `ensure(ref = '',
    home = here())` — AgTT's ref first, partitions' home second (the one
    caller that names a home, `model/partition.js`, now says
    `ensure('', '')`); `picker()` AgTT's (`app.newClassId()`, `fits`)
    hidden for a hosted conversation or a harness run; `badge()` null for a
    hosted one, else AgTT's with `fixed`.
21. `native/chat.js` — both imports; `c` is partitions' `hostedComposer(…)`
    and the seams' placeholder/slash come after it.
22. `native/convs.js` — the subtitle: AgTT's kind and `⧉` first, then a
    search hit's snippet or partitions' `⚠ not private` and the shared
    chips.
23. `native/home.js` — both imports; partitions' notices, then
    `homeSetupTpl()`.
24. `native/ui.js` — both import sets.
25. `sidebar.js` — both import sets; `hostedRowChip(r)`, then AgTT's kids
    chip.
26. `API.md` — the frontend table: AgTT's rows in AgTT's order; the
    sandboxes row is AgTT's text plus partitions' "in a person's
    partition … `listAt(home)`"; partitions' homes row right after it.

Harness and records:

27. `hack/ui-harness/run.sh` — AgTT's `FSB_HARNESS_FAKE` line with
    partitions' `"${overlay_flags[@]}"` (HARNESS_NO_OVERLAY).
28. `hack/ui-harness/shots.js` — partitions' side (imports, PASSES), and
    `PASSES.agentHarness = require('./passes/agentharness').agentHarness`
    on its own line after `agentHosted`.
29. `docs/changelog.md` — one `## 2026-09-30` heading: partitions' entries,
    then master's.
30. `plans/DECISIONS.md` — partitions' side with master's D147 block
    between D146 and D148. Every master block (D133's, D135's and D136's
    dated amendments included) is in the result verbatim and once (checked
    block by block).

Merge-caused fixes in the merge commit (nothing conflicted; the tree
didn't build or pass): the agent backend's test helper `scanStrings`
(AgTT's `harness_routes_test.go`, used by `harness_device_test.go`) is
renamed `dbStrings` — B2c's `handoff_fetch.go` defines a `scanStrings`
(vet: redeclared); `hack/agent-template-hosted.test.mjs`'s app stub gains
`newClassId` (the store's picker now asks for it); `model/partition.js`'s
`ensure` call (item 20).

## The plan's silent losses

- **F-M1** (`4f57cd9b`): `GET /sandboxes/{name}/execs/{id}/stdio` is
  `GlobalOnlyRefused` in `internal/server/partitionclass.go`, beside
  `…/stdin`. TestPartitionRouteClasses failed on the merge and passes.
- **F-M2** (`43af5a2a`): AgTT's `mutates` (any `Upgrade` header, a path
  ending `/stdio`) ported as written into
  `builtin-templates/coding-sandbox/_backend/access.go`. Coding-sandbox's
  TestPageReaders failed on the merge (`GET …/execs/e1-1/stdio` as a
  page reader: 400, want 403) and passes.

## Left for the W6 packs (seen while merging, not done here)

- **W6-F (F-M3):** AgTT's "§Partitions, sharing and people" still appears
  where the heading is partitions' "§Consumers, sharing and people":
  `docs/sandbox-manager.md` §Terminals (its "relays a terminal … checks
  that person first" bullet), `docs/sdk.md`, and
  `docs/changes/2026-09-30-manager-terminals-for-backends.md` (the
  changelog's 2026-09-30 line quoting the old name is history). The
  "asserted, not verified" texts are as AgTT wrote them.
- **W6-F (F-M4):** fakesandbox's `TestContractBeforeStdio` and
  `TestContractPolicingTTY` pass with `user-partitions` running (the fake's
  default caps now carry `partitions`); the plan's skip is still to add, or
  to decide against.
- **W6-F (B4):** coding-sandbox API.md's stdio bullet says "the consumer,
  the person rules" (item 12); the plan's "(a user partition is its
  own)" is B4's.
- **W6-U:** `model/sandbox-store.js`'s `ensure(ref)` rechecks a sign-in
  card's sandbox in the list of where you are; a harness run homed
  elsewhere (U-M1, U-M3) is W6-U's.

## Changelog entry

None: the merge brings master's entries under the one 2026-09-30 heading;
F-M1 is PD-28's existing rule on a route master added (partitions' entries
already say a person's partition gets 403 on tile sandboxes), and F-M2
makes the coding-sandbox page do what master's entry and its API.md
already say. W6's own entry comes with B4's decision.

## Decision entry

None here. B4 records the next free D-number, "coding agents under
partitions" (90 §I15), and D147's dated amendment pointing to it.

## Deviations

- Item 3's order on update puts AgTT's `harness` 400 before
  `hostedEditRefused` (the plan: "beside"): a malformed patch is refused as
  on master whoever holds the trigger; either order refuses both.
- Item 12 and 14 carry partitions' "consumer" for AgTT's "partition" where
  the two texts met; the rest of F-M3 is W6-F's.

## Bugs found

None beyond the plan's two (F-M1, F-M2) and the merge-caused redeclared
test helper.

## Tests

On `pt/w6-base` after F-M2:

- `go build ./...`; `make fmt-check vet js-check`.
- `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck
  ./internal/builtins ./internal/apicheck ./internal/server ./internal/boot
  ./hack/fakesandbox` — ok (apicheck failed on the merge alone: F-M1).
- `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent
  coding-sandbox` — both ok: the agent's in 475 s (the legacy golden,
  AgTT's harness tests and every partition pack's, together), the
  coding-sandbox's in 5 s. The agent's race run is now at 475 s of `go
  test`'s default 10-minute timeout (W5-wire's was 268 s; without `-race`
  it is 94 s): the W6 packs' tests will push it closer — a `-timeout` in
  tile-check, or splitting the package's slowest tests, before it fails
  for time alone.
- sandbox-terminal's backend tests (its fake manager copy) — ok.
- `make js-test` — 628 tests, 627 pass, 1 skipped (as before), 0 fail
  (the hosted rules test failed on the merge until its stub had
  `newClassId`).
- The agent template's browser tests from a scratch copy: homes,
  partition, hosted, harness, harness-start, harness-term, share — all
  passed; also sidebar, native, sandbox, home, chat, harness-board,
  harness-child (the files the resolutions touched) — all passed.
- `go vet -tags=integration ./test/...` (the isolated suite's edited
  file compiles); the isolated coding-sandbox run itself is B5's rerun.
