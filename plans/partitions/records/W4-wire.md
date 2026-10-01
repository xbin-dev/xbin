# W4-wire — wave 4's seams wired

Wave 4's integration of the partitioned-tiles plan: `partitions` at
badbb903 holds the seven wave-4 packs — T1 (template instances merge
manifests by keys), B3 (the tiles around a partitioned agent; llm-gw's
per-partition counters), F11 (the person page `/xbin/partitions`), F12
(the admin console's partitions view, the logs switcher), F14b (the
shell's consent prompts and partition chip), B2b (the agent's shared
conversations at global, two homes) and B2c (the agent's channels,
triggers and usage by partition mail). Branch `pt/w4-wire`. It edits
neither `docs/changelog.md` nor `plans/DECISIONS.md` (see §Changelog notes
and corrections). No new feature; no new HTTP route.

## What the merge left

The tree built, and on badbb903 as merged: every partition smoke passed
(below), `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` passed
(243 s: the legacy golden and both packs' tests on the auto-merged agent
template), the agent template's browser tests passed, and `make js-test`
passed. What didn't:

- **`go test ./...` failed** in `builtin-tiles/llm-gw/backend`:
  `TestCallersRecreatedPerson` failed 16 runs in 20 under plain `go test`
  (B3 ran it only through tile-check with `-race`, whose slowdown hid it) —
  a real tie in the code, §Bugs found 1.
- **Semantic leftovers of the B2b + B2c auto-merge** (nothing failed): B2c
  laid its channel routes over `userRoutes` from `handoff_user.go`'s `init`
  so the two packs' edits of `partition_routes.go` wouldn't conflict;
  merged, B2a's `userNoChannels` kind, its 409 words and the file's header
  ("channels and event triggers … answer 409 here") were dead and false.
- **B3's docs described B2c as first built** (a DM before the person's
  partition ran "waits, unanswered"); B2c's review fix built 09 §3's
  first-DM notice.

## Wired and fixed

### 1. The agent template after B2b + B2c

- **One route table** (`_backend/partition_routes.go`): the channels'
  routes, `GET /triggers/unmatched` and `GET /usage` (`userGlobal`) and
  `POST /triggers` (`userLocal`) now sit in `userRoutes` beside B2b's
  `POST /join`; `channelUserRoutes`, the `init` overlay, `userNoChannels`
  and `noChannelsWords` are gone and the header says what a person's
  partition forwards. No route answers differently (every key checked
  against `routes.go`'s patterns; `TestUserModeRoutes` and the
  handoff/trigger/homes tests green).
- **Mailbox dispatch**: nothing to change — B2c's topics (`handoff/dm`,
  `handoff/event`, `outbox/add`, `partition/hello`, `usage/day`) register
  one handler each, B2b adds none, and `prepareMail`/`settle` wrap only
  B2c's two file-carrying topics.
- **`globalRoute` (B2b) and B2c's global-side refusals**: B2c's
  `refusePrivateAtGlobal` (a person's private trigger or schedule made or
  edited at global from their partition → 409) needs the body and the
  saved row (a switch vs an edit), so it stays in the four handlers, as
  B2c's record argues; `globalRoute`'s comment now names it. Not moved.
- **Frontend homes**: B2c changed no frontend file. The automations
  models (`model/auto*.js`) call the page's own backend, which forwards a
  channel's routes and a registry row's `PUT`/`DELETE` below 2^40 to
  global; a run opened from a channel's sessions or a trigger's events goes
  through `select(runId)` → the session's `runApi`, which B2b's
  `homes.js` routes by id (global below 2^40). Nothing to wire; see
  §Looks wrong 3 for the channel's run list.
- Template browser tests and the harness passes: §Tests.

### 2. B3's bridge/webhooks e2e

`test/isolated/partitions_bridge_test.go` carries no skip naming B2c
(B3's review dropped its B2c cases for B2c's own e2e). `TestPartitionsBridge`
(6 cases) and B2c's `TestPartitionsAgentChannels` (3 cases, the
hand-offs) pass on the merged tree and on this branch — B3's acceptance.

**B3's docs follow B2c as built now**: [/docs/agent-inbox.md](/docs/agent-inbox.md)
§A partitioned agent, the messaging bridge's `AGENTS.md` and `API.md`
say a DM from a linked person whose partition never ran is answered once
with a notice (open the agent once; the message waits up to 7 days) and
the `status` is `idle`. webhooks' API.md was already true ("a hand-off
waits for it").

### 3. F11 ↔ F14b ↔ F10

- **The shell names and links the page.** F14b's `PARTITIONS_PAGE` is
  `/xbin/partitions`: the Allow answer says "Take it back on your
  partitions page (/xbin/partitions)." with an **open it** link
  (`bx-part-consent.js`, accent-coloured). The `partitionConsent` pass
  follows the link — xbind serves dev1 the page (200). The node test's
  expectation moved with it.
- **The links**: the consent refusal (`… (they allow it at
  /xbin/partitions)`), the consent push and the switch/wipe pushes
  (`xbin/partitions`), the shell's paused card (**details…**) and now the
  consent answer all name or link the page, which F11 serves (checked
  live in `personPage`, `partitionConsent`, `partitionMark` and
  `TestPartitionsPage`). The pushes' `xbin/partitions` link is opened by
  the xbin app only once it learns the link (native programme; today it
  opens the workspace — native/spec/push.md says so).
- **The `/alerts` switch hint names the page** (done: small).
  `partitionnotify.go`'s `partition-switch` message ends "(bx partition
  switch|keep <tile>, or on /xbin/partitions), it doesn't run."; the
  shell's `pendingText` strips the parenthesis from its `(bx partition
  switch|keep <tile>` head to its `)` — a prefix match (`stripHint`),
  F14's exact-text strip before. F14's shell isn't on master, so no shell
  in the wild strips the old text. Node test for both texts; the admin
  console's banner shows the new one (screenshot `admin-partitions-tile`),
  the card doesn't (`partition-pending-reader`). Docs: partitions.md (the
  alert names both; the page section), protocol.md (`/alerts`).
- **Asks go with their tile** (F14b's and F10's open end).
  `dropAsksNaming(tile)` (partitionconsent.go) forgets the day's asks —
  and their answers — naming a tile as caller or callee; called from
  `PartitionTileChanged` when a tile goes (deleted or moved: the zero mode)
  and from the consents' wipe hook when a switch takes the consents (not a
  dry run). `GET /partitions/consents`' `asked` no longer lists an ask its
  person could only answer with a 404; a new tile at the path that asks the
  same day asks afresh. `TestConsentAsksGoWithTheTile` (fails without the
  change: both asks stay). Docs: protocol.md (`GET /partitions/consents`),
  partitions.md (the shell's ask). The shell's own filter by the tiles it
  lists stays (its list may be a moment apart from xbind's).

### 4. F12 ↔ F11

- **Personal-bind rows: one server contract.** Both pages read
  `personalBindRow` (`bindRow`): the person page's overview from `GET
  /partitions/binds`, both pages' per-tile views from `GET
  /partitions?tile=`'s `binds` (`tileBindRows`), and both remove with
  `DELETE /partitions/binds` (`{id}` for one's own, `{id, user}` for an
  admin). Only `GET /partitions/binds` was sorted; `tileBindRows` now sorts
  the same way (`sortBindRows`), protocol.md says the per-tile field is
  that route's rows, and `TestPersonalBindLifecycle` checks the two answers
  are equal for each caller (alice, admin bob, carol).
- **F12's console-driver hole has no F11 equivalent** (verified, no
  change): the page holds no credential of its own — it is top-level only
  and reads with the person's own session (`p.Component == ""`); tile code
  gets the tile-level fields; `consoleAdminDriver` grants an admin's view
  only to the admin tile's frame driven by an admin, which the page never
  is; every act the page offers is judged again by xbind as the person
  (`PersonOnly` for consents, binds and credentials; `mayManageTile` /
  `modeDecider` / `partitionActor` for switches, stop, reset, restore,
  share-log). The page filters an admin's answer to the caller's own rows
  and binds (F11's review fix 1), which is presentation, not access.

### 5. T1's changelog correction

Recorded in §Changelog notes and corrections (not applied: the owner
folds).

## Deviations

- **The automations' refusal stays in the handlers**, not `globalRoute`
  (item 1).
- **The alert's wording changed** (item 3): a client matching the
  `partition-switch` message by its full text would no longer match; the
  message is informational (the alert's `kind` and `tile` are the
  contract), and the only client that parsed it is F14's unreleased shell.
- **llm-gw stays at v8** (B3's unreleased bump): the fix rehashed
  `hack/tile-versions.txt` instead of a v9, as B3 did for sandbox-terminal.
- **The consent answer gained a link** ("open it") beside the words F14b's
  seam asked for.

## Bugs found

1. **llm-gw: a recreated person could lose their new row** (B3's code,
   `callers.go`). `supersedeLocked` dropped any row of the same person
   with `Last <= row.Last`; when the old partition's last call and the new
   one's first fell in the same millisecond, whichever came first in sort
   order won — at a load after a kv error, the old row (`u-a`) deleted the
   new (`u-a2`). Now a row this process counted supersedes unconditionally
   (it is the person as they are now), and rows read back from kv go by
   `Last`. The test pins the clock (5/5 FAIL before, 50/50 PASS after,
   `-race` too).
2. **Docs vs B2c's review fix** (the first-DM notice), fixed (item 2).
3. **Stale route words** after the auto-merge, fixed (item 1).
4. **Pre-existing, not wave 4's**: the agent template's browser test
   `test/terminal.mjs` (a coding sandbox's terminal, D115) times out
   waiting for `#sbxterm-pane bx-terminal` — on this branch and on the
   partitions base d6499604 alike (from `git archive d6499604`). Not in the
   packs' list; left for its owner.

## Seams still open, by pack

- **B2d** — B2b's: `shareSpec.check` for a hosted conversation's members;
  the bundle's file part (`bundleFile`, `acceptUploadSrc` source `copy`)
  and `vouchedBy` for "add a copy of my …"; a shared conversation's
  sandbox picker (it lists the person's partition's sandboxes; global
  refuses binding one); native publish / copy / shared new chat (feature
  DIFFERENCES). B2c's: `prepareMail` cases for `hosted/*` topics with files;
  `queueHandoff`/`kickHandoffs`/per-person `next_try` for `hosted/input`
  and `hosted/changed`. W3b's: `teamStore`, `teamUnavailable`,
  `ownConversation`. Owner questions of B2b (un-sharing at global) and
  B2c (the overlap rule and team triggers; large files; the notice's
  source) stand.
- **I1** — an isolated harness run of F11's `asked` consents with Allow
  (node-tested only); `TestPartitionsSmokeReap` (SKIP here: over ten
  minutes, `XBIN_SMOKE_REAP=1`); llm-gw's residual (a recreated person
  sees the old row until their new partition calls it once; xbind
  announces no deletion to tiles); W3b's ring-in-flight re-arm; the
  security and downgrade suites; `test/terminal.mjs` above if it is
  wanted in the suite.
- **F8 finalization** — the Node/Python partition-mail snippets in
  [/docs/sdk.md](/docs/sdk.md); context-taking `InboxPage`/`Ack`; 90
  §I4's "realtime between partitions" section. docs/partitions.md has no
  item marked TODO left: its status note's "Items marked **TODO** aren't
  built yet" can go with the finalization.
- **I2** — the changelog and decision folds below; release notes.
- **Unassigned (owner)** — B2c's `GET /usage` (per-person usage totals for
  managers) has no UI: neither F12's console nor the agent's settings show
  it. The xbin app: the `xbin/partitions` push link, an in-app consent
  prompt and the chip on pushed `xbin.window` screens (native programme).

## Looks wrong once the packs meet (flagged, not redesigned)

1. **Times without a zone in the admin console**: F11's page shows "17:46
   GMT+2" (browser zone, named), F12's tile view "2026-09-30 15:46" (no
   zone) — the same fact in two formats for an admin who opens both.
2. **The chip says `yours` on a partitioned agent's window**, which since
   B2b also lists and opens shared conversations at the global instance
   (its Shared view, `#c=<id below 2^40>`). The chip describes the frame's
   partition, so it is accurate; whether I3 wants the chip to follow the
   open conversation is the owner's call.
3. **A channel's run list in a person's partition shows only that
   person's own handed DMs**: `GET /automations/channel/{id}/runs` and the
   card's counts read the partition's own db (`handleAutomationRuns`, no
   forwarding), so a manager's own page shows "1 conversation" for a
   channel whose group threads live at global (they open from its
   sessions list, which is forwarded). B2c's choice or a gap — for B2c's
   follow-up or B2d.

## Changelog notes and corrections (the owner folds these)

- **T1's correction of B2a's paragraph — must be folded** (the 2026-09-30
  B2a entry, docs/changelog.md lines 209–213 today). Replace exactly:

  > but an instance's `xbin.json` — rewritten when it was created —
  > conflicts on this version's manifest changes: keep your side (an
  > unpartitioned instance needs none of them; to switch one to
  > partitioned later, add the `conf` and `team` lines to its `uses` and
  > `"partitionMail": "/mailbox"`).

  with:

  > and its `xbin.json` merges by keys (see the template-instances
  > entry): it gains the `conf` and `team` uses, `partitionMail` and
  > `partitionNote` — unused unpartitioned — and keeps its mode.

  (The sentence before it, "the template's `template` block never
  reaches it (see the templates entry),", stays.)
- **F11's correction of F10's paragraph** (docs/changelog.md lines
  350–352 today): replace "The push links to the partitions page
  (`xbin/partitions`), which a later change serves; until then the app
  opens the workspace and people allow an edge with `bx partition
  consent`." with "The push links to the partitions page
  (`/xbin/partitions`, the partitions-page entry), where people allow an
  edge — or with `bx partition consent`; the xbin app opens the workspace
  until it knows the link."
- **B3's entry** (the "Builtin tiles around a partitioned agent" bullet):
  replace "a DM that arrives before the person's partition ever ran waits
  in its inbox until they open the agent;" with "a DM that arrives before
  the person's partition ever ran waits in its inbox until they open the
  agent, and the first is answered with a notice saying so;".
- **F11's entry**: its "**Links to it:**" sentence gains "the
  `partition-switch` alert (`/alerts`, the shell's banner) names the page
  beside `bx partition switch|keep`;" and its decision's "The
  partition-switch alert's text is unchanged (the F14 shell strips its bx
  hint by exact text)" becomes "The partition-switch alert names the page
  beside the bx hint; the shell strips the whole hint (a prefix match)".
- **F14b's entry**: "The ask shows … until you answer it, goes when you
  answer it anywhere … and when the policy is turned off" gains "or either
  tile is removed, moved or switched to or from unpartitioned"; and after
  "Allow … (the next call goes through; …)" add "— the shell then says
  where to take it back and links your partitions page
  (`/xbin/partitions`)". Its decision's "(`bx partition consent …
  --revoke` until a partitions page is served)" becomes "(the partitions
  page, linked)", and its "Not chosen"/Deviations line about an ask
  outliving a removed tile is resolved: xbind drops such asks.
- **F12's entry**: `GET /api/xbin/partitions?tile=`'s `binds` are `GET
  /partitions/binds`' rows in its order (additive; one sentence if
  wanted).
- **B3's llm-gw** needs no new text (v8 is unreleased; the recreated
  person's rule is already in its entry).

## Tests

On the branch's final tree (.dev.mk's env exported for the integration
runs, the Bash sandbox off for isolated runs and the harness):

| Run | Result |
|---|---|
| `go build ./...`; `make fmt-check vet js-check js-test` (505 JS tests, 504 pass, 1 skipped as before) | PASS |
| `go test ./...` (on badbb903: FAIL, llm-gw's tie — §Bugs found 1) | PASS (in `make check`) |
| `-race -count=1`: `./internal/broker ./internal/server ./internal/boot ./internal/builtins ./cmd/bx ./internal/deps` | PASS |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh llm-gw agent-messaging-bridge webhooks` | PASS |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` (the legacy golden + B2a/B2b/B2c tests), on badbb903 and on the final tree | PASS (243 s, 240 s) |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| zero-state goldens: `TestNoPartitionGolden` (boot), `TestZeroStateRoute` (broker) | PASS |
| llm-gw `TestCallers*` `-count=50`, `-race -count=20` | PASS |
| broker `TestConsentAsksGoWithTheTile`, `TestPartitionEdgeMatrix`, `TestPartitionConsent*` `-count=3`; `TestPersonalBind*` | PASS |
| node: `hack/partition-mode.test.mjs` (22), `hack/partitions-page.test.mjs` (14) | PASS |
| agent template browser tests from a scratch copy (web → the worktree's, node_modules → the dev playwright): `homes partition share sidebar chat home attach automations settings native sandbox layout triggers channels grant long live-policy frame-policy` — no SKIP | PASS |
| … `terminal` | FAIL — pre-existing (the partitions base fails alike), §Bugs found 4 |

**Smokes** — `go test -tags=integration -count=1 -v -run 'TestPartitions'
./test/isolated/` on a real `xbind --isolate`, run on badbb903 and again on
the final tree, the same result both times (final: 78.7 s):

| Test | Subtests | Result |
|---|---|---|
| TestPartitionsSmoke | zero-state, auto-mode, own-partition, data-apart, deployment, bob-never-sees-alice, cross-tile, global, admin, logs, ops, view-as, caps, token-revocation, person-recreated, reset, mode-keep, mode-switch, mode-pending-unpartitioned (19) | PASS |
| TestPartitionsSmokeW2 | vault, global-address, personal-bind, terminal, cron (5) | PASS |
| TestPartitionsSmokeMail | global-to-person, person-to-global, outsiders, listing, never-run-restart (5) | PASS |
| TestPartitionsSmokeBackups | — | PASS |
| TestPartitionsAgent | modes, private-chats, shared-chats, global-db-not-mounted, mailbox, sandboxes, existing-instance, llm-gw-counters (8) | PASS |
| TestPartitionsAgentChannels (B2c) | linked-dm, forged-outbox, private-webhook-trigger (3) | PASS |
| TestPartitionsBridge (B3) | claim-at-global, group-at-global, link-at-global, unlink, webhook-team, llm-gw-counters (6) | PASS |
| TestPartitionsPage (F11) | — | PASS |
| TestPartitionsTemplateMerge (T1) | — | PASS |
| TestPartitionsSmokeReap | — | SKIP ("the idle stop takes over ten minutes: XBIN_SMOKE_REAP=1 runs it", its own gate) |

**UI harness** (PORT 9071, HARNESS_DIR …/scratchpad/h-w4wire, fresh
workspace each run; stopped after each):

| Run | Pass | Checks |
|---|---|---|
| unisolated | personPage | 47 PASS |
| | adminPartitions | 36 PASS |
| | partitionLogs | 13 PASS |
| | partitionConsent | 36 PASS (incl. the answer's link: `/xbin/partitions` → 200 for dev1); rerun after the link's style, 36 PASS |
| | partitionMark | 46 PASS (the reader's card without the hint, whose alert now names the page) |
| | adminTabs | 25 PASS |
| | agentTemplate | 28 PASS |
| | agentConvs | 19 PASS |
| `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1` | agentHomes | 20 PASS |
| | channelsPartitioned | 15 PASS |
| | agentTemplate (against the admin's partition) | 28 PASS |

No FAIL and no SKIP in any pass. Screenshots looked at:
`partition-consent-allowed` (the answer, "open it" in the accent colour),
`partition-pending-reader` (the card: no hint), `admin-partitions-tile`
(the banner naming the page; the tile view's people, personal binds,
history), `person-page-bind`, `channelsp-dev1-automations` (dev1's
partition: the global instance's channel, their own trigger),
`agent-homes-mine`.

**`make check`** on the final tree: green (">> make check: green").
