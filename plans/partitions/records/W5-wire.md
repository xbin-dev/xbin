# W5-wire — wave 5's seams wired

Wave 5's integration of the partitioned-tiles plan: `partitions` at
72946b86 holds the five wave-5 packs — B2d (the agent's hosted, non-secure
conversations and "Add a copy of my …"), AF (un-sharing moves a chat home,
staged handoff files, no first-DM notice), SH (the shell's "your
partitions" entry, the chip's tooltip, times with their zone), F8F (the
SDK's context-taking mail calls, [/docs/partitions.md](/docs/partitions.md)
§Realtime, the review pass) and I1 (the security, non-isolated, downgrade
and contract suites). Branch `pt/w5-wire`. It edits neither
`docs/changelog.md` nor `plans/DECISIONS.md` (§Changelog notes and
corrections). No new feature; no new xbind HTTP/WS surface
([/docs/protocol.md](/docs/protocol.md) untouched); no new route — one
existing handler of B2d's becomes reachable (§Wired 3).

Commits (oldest first): the AF × B2d wiring (`3682ac25`), the hosted
conversation's controls (`f0a7b491`), the partitions page's consents row
(`14d45903`), the e2e and harness steps for an un-shared hosted
conversation (`f4d95b53`), partitions.md's agent hub bullet (`c0820861`),
and this record.

## What the merge left

The tree built and — first thing, on 72946b86 as merged — was green:
`TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` (268 s: the
legacy golden and every pack's tests on the auto-merged template), `make
fmt-check vet js-check js-test` (525 JS tests, 524 pass, 1 skipped as
before), `go test ./...`. Nothing the merge did broke a test. What it left
was the AF × B2d interplay, wired by neither pack: the one line each record
reserved for the other (B2d's `unshareHosted` comment, AF's `movingRefused`
seam), B2d's own read of AF's table (`convMovingHome`, "asked" only), and
a test of B2d's that faked AF's table with a schema AF's code no longer
reads.

## Wired and decided: AF × B2d

Each decision is pinned by a test in `_backend/hosted_moves_test.go`
(each mutation-checked: it fails with the wiring reverted) or the named
JS test.

1. **Un-sharing a hosted conversation** (only its owner left: its last
   member removed or gone, made private with nobody in it). *Decided:
   hosting ends first, then AF's move* — not refused (a member must be able
   to leave; B2d's "Not chosen" already said so), and not a new direct
   team → partition path. B2d's `unshareHosted` continues it at the global
   instance as its owner's plain conversation (a tombstone in team); it
   now calls `moveIfUnshared(back, true)` in a transaction there, so a
   person's chat is mailed `conv/move` and moves to its owner's partition
   like any un-shared one (refused changes meanwhile, AF's export, done,
   tombstone). Not a person's (the owner token's — owner `""`): it stays at
   the global instance, private, as AF does for its own. Pinned:
   `TestHostedUnshareMovesHome` (bob hosts alice's; bob leaves → back at
   global, `conv_moves` "asked", the `conv/move` mail to alice with a key,
   a message 409, the export answers a ticket and the transcript; alice
   makes her own hosted team conversation private → the same through
   PATCH), `TestHostedUnshareNotAPersons`; end to end in
   `TestPartitionsAgent/hosted-chats` (below).
2. **A conversation moving out, and hosting or copying into it.** *Decided:
   refused (409, AF's words) in both AF states, "asked" and "leaving"* —
   AF's `movingRefused` replaces B2d's `convMovingHome` (which read only
   "asked" and ignored AF's 8-day expiry) in `handleHostedMove`, and
   `handleCopyInAt` calls it too; both are mounted outside `globalRoute`,
   whose `refuseWhileMoving` covers every other change. A move given up
   makes it hostable again. Pinned: `TestHostedMovingAtGlobal`.
3. **The route tables.** Order in `routes()`: `homeRoutes`,
   `hostedRoutes`, `moveRoutes`, `fetchRoutes`, the table (each route
   `agentRole(hostedRoute(guard(partitionRoute(…))))`), `mailboxRoutes`.
   ServeMux patterns don't depend on order and none overlap. For a hosted
   id (team's range) `hostedRoute` is outermost, so `globalRoute`'s
   `refuseWhileMoving` never sees one — correct: AF moves only global's
   own db (ids below 2^39), never a team row, and nothing puts a hosted id
   in `conv_moves`. **Fixed:** `homeRoutes` mounted `GET
   /runs/{id}/export` without `hostedRoute`, so B2d's `handleHostedExport`
   (in `hostedHandlers`) was dead and a member's **Copy to my own space**
   on a hosted conversation answered 404; it is wrapped now (`publish` on
   a hosted id: 409). Pinned: `TestHostedExportAtGlobal` (a viewer's
   export 200 with the transcript, dave 404, publish 409).
4. **AF's move bundle and team-resident runs.** Nothing to join: AF
   exports from global's db and imports with a partition id from 2^40;
   B2d's runs stay in [2^39, 2^40) and leave team only by continue or
   un-share (both into global's own range). `GET /moves/{hosted id}`
   answers 404 (no move of a team row exists) — see §Seams, B2d: an old
   `#c=<hosted id>` link after an un-share still goes home.
5. **Events: B2d's allowlist vs AF's `movedTo`.** *Decided: only the
   global instance says where a conversation went.* AF's `movedTo` rides
   the `run` event of global's own delete (`dropConversationTo`), and
   B2d's continue/un-share publish theirs on the same hub through the team
   view; a host's forwarded `run` event is re-read from team
   (`publishRun`), so a `deleted`/`movedTo` in its body never reaches a
   member. Pinned: `TestHostedRunEventNoMovedTo`. **The page:** AF's
   `followsMove` followed only ids from 2^40, so the owner's page went home
   when their hosted conversation came back to the global instance; it
   now also follows a hosted id to the global instance's own range (an
   un-share — then on to the partition, a second follow — or a continue);
   owner-only as before. Pinned: `hack/agent-template-moves.test.mjs`.
6. **AF's staged handoff files and a hosted conversation.** *No
   interplay:* staged files belong to a channel's DMs (in a person's
   partition, which `POST /hosting` refuses: "yours alone") and their
   replies; a channel's thread at the global instance can't be hosted
   (automation origin, 409); B2d's `hosted/*` mail carries no files.
   Pinned: `TestHostedNoChannelThreads`.

B2d's `TestHostedUnshareAndDelete` lost its last block (the fake
`conv_moves` table); `TestHostedMovingAtGlobal` covers it on AF's real
schema.

End to end: `TestPartitionsAgent/hosted-chats` now ends with alice taking
dave and bob out of her hosted conversation — `movedTo` a global id, then
`GET /moves/{it}` "moved" to an id from 2^40, the transcript (her ask and
the hosted run's answer) in her partition, bob 404 on both old ids. The
`agentHosted` harness pass does the same from the page: the admin's page
follows the conversation both steps (#549755813888 → #1099511627776) and
shows its transcript.

## Follow-ups the packs flagged

- **B2d — the top bar's 409 buttons: hidden.** `rules.topBar` drops Compact
  and Learn skill for a hosted view (web and native share it). The sandbox
  store shows neither the composer's picker nor the bar's badge for a
  hosted conversation: AF's per-home list made the picker list the global
  instance's sandboxes, which a hosted conversation can't bind (409), and
  the badge looked the host's sandbox up in that list ("gone ⚠"). Memory
  and Files stay: they read, and their edits answer 409 as a viewer's
  answer 403. Pinned: `hack/agent-template-hosted.test.mjs`, and the
  `agentHosted` pass (no Compact / Learn skill on the hosted bar, both on
  the plain shared one before it). Not changed: the composer's 📎 attach
  (uploads answer 409 on a hosted conversation, B2d's deviation) — it is
  the composer's, not the bar's, and agent.js is 998 of 1016 lines.
- **I1 — "Allowed: …" in the consents row until reload: fixed**
  (`web/partitions-more.js`): the asked card has its own UI key
  (`asked:<from>→<to>`); the `personPageAsked` pass now takes the consent
  back without reloading and checks the row.
- **F8F — the agent's hub and a follower who loses the tile: real, not
  fixed.** `eventHub.revalidate` cuts a follower when the conversation's
  ACL stops admitting them; nothing re-checks the tile itself: the stream
  loop (`streamWith`) waits only for events and the request's end, with no
  lifetime, and xbind doesn't cancel a proxied response when access ends
  (no such hook in `internal/server` or `internal/proxy`). So a person
  removed from the agent, disabled or deleted keeps an open stream of a
  shared (or, unpartitioned, a team-visible) conversation until it ends —
  pre-existing, not partitions'. [/docs/partitions.md](/docs/partitions.md) §Realtime's agent
  bullet claimed the agent works like the example (which asks
  `xbin.AccessOf`); it now says the agent doesn't yet. **Platform
  follow-up** (F8F's owner question 2): xbind cancels the in-flight
  proxied requests a person's credentials opened to a tile when their
  access ends; until then, or instead, the agent's hub asks
  `xbin.AccessOf` for its followers on a timer.

## Deviations

- **The hosted export is reachable** (§Wired 3): a route B2d built and
  documented nowhere now answers; API.md names it.
- **The owner's page follows a hosted conversation to the global
  instance** (§Wired 5) — AF's rule was "moved into their own space" only.
- **[/docs/partitions.md](/docs/partitions.md) §Realtime** says what the
  agent's hub doesn't do (F8F's flag), rather than the code learning it
  (no new feature).

## Bugs found

1. **B2d's hosted export was unreachable** (`homeRoutes` without
   `hostedRoute`): Copy to my own space on a hosted conversation 404'd.
   Fixed (§Wired 3).
2. **The AF × B2d wiring was missing** — un-sharing a hosted chat left it
   private at the global instance (against 90 §I10), and a conversation in
   AF's "leaving" state could be hosted or copied into. Fixed (§Wired 1,
   2).
3. **The page's controls**: Compact/Learn and the sandbox picker/badge on
   a hosted conversation (B2d's flag) — fixed; the consents row (I1's) —
   fixed.
4. **The agent's hub keeps a stream past the tile's loss** (F8F's flag) —
   confirmed, recorded above, not fixed.
5. **Pre-existing, unchanged**: the template's `test/terminal.mjs` times
   out waiting for `#sbxterm-pane bx-terminal` (W4's §Bugs found 4); the
   share pill's "shared with N people" isn't live (B2d's; seen again in
   `agent-hosted-paused-admin`: "1 person" with dev1 and sales1 in it).

## Looks wrong once the packs meet (flagged, not redesigned)

1. **A conversation's history of moves collapses to the last note.** A
   bundle carries messages, the ledger and files, not the journal's notes;
   each import writes its own. So a hosted conversation un-shared and moved
   home arrives with AF's note only ("Moved here from the agent's shared
   space…", screenshot `agent-hosted-unshared-home`): nothing says it was
   once non-secure (run by someone else's partition, readable by the
   members) or which binary files the hosting left behind at the global
   instance. B2b's bundle design; if wanted, the bundle carries the
   earlier move notes, or the arriving note names "it was hosted by …".
2. **Two retry mechanisms for partition mail at the global instance**:
   the durable handoffs queue (B2c's DMs, AF's `conv/move`) and B2d's
   in-memory rings (§Seams, B2d).

## Seams still open, by pack

- **W6 (AgTT merge, plans/partitions/96-agtt-merge.md)** — B0's
  `routes.go` resolution must keep this branch's wrapping:
  `agentRole(hostedRoute(rt.pattern, rt.need, guard(rt.need,
  partitionRoute(rt.pattern, rt.h))))` in the table loop and `hostedRoute`
  in `homeRoutes` (the plan's text predates both). agtt's new
  `/runs/{id}…` routes answer 409 on a hosted id until added to
  `hostedHandlers` — right for harness routes (A-M2). The hosted paths
  that copy a transcript (`handleHostedExport`; continue and un-share
  through `continueAtGlobal`) all call `exportConv`, so A-M3's refusal
  there covers them — though with A-M2 no hosted conversation holds a
  harness; an un-shared chat's move home also passes `moveIfUnshared`,
  which A-M3 refuses for a harness root. The hosted top bar rule
  (`rules.topBar`'s `hosted`) sits where F-S3's "hide the actions" goes.
- **B2d** — the rings (`hosted/input`) retry in memory (`ringHost`,
  `wakeRetries`), not through AF/B2c's durable handoffs queue: a global
  restart mid-ring leaves a member's input in team's inbox until the host's
  partition next starts. `GET /moves/{hosted id}` knows nothing of the
  continue/un-share tombstone (`team_hosts.continued_to`): an old
  `#c=<hosted id>` link goes home (B2d's "moving changes the id"). Its
  owner questions stand (per-resource hosting, skills/memory, binary
  files, copy-in beyond session files, approvals, frame vs backend).
- **AF** — its owner questions stand ("never two homes", the state rule,
  too large to move); the e2e crash window between import and done is
  unit-tested only.
- **F8F** — the hub follow-up above; I2 lifts the "in development" notes
  (F8F's list).
- **SH** — the size budget of `bx-shell.js` may be lowered to 1853.
- **I1** — the suites in CI (owner question 4); the non-isolated data
  plane (question 3).
- **I2 (rollout)** — the folds below, F8F's "in development" sweep, the
  release checklist's isolated commands (I1 §Where these run).
- **Unowned** — xbind can't tell a person's frame from their partition's
  backend (B2d's and AF's records both lean on keys and re-reads for it);
  a platform header would let tiles require the backend.

## Changelog notes and corrections (the owner folds these)

- **AF's two corrections** (records/AF.md "For the integrator"): amend
  B2c's entry in place (docs/changelog.md lines 60–63 today: "Files up to
  640 KiB … the first one is answered with a notice saying so;" → AF's
  text), and drop W4-wire's pending B3 correction (the first-DM notice is
  gone, 90 §I12).
- **B3's entry** (line 228 today): "(its files inline up to 640 KiB a
  message)" → "(its files inline up to 640 KiB a message, a larger one
  staged at the global instance until the partition fetched it)".
- **B2d's entry** (records/B2d.md): "un-sharing it (only its owner left)
  brings it back to the shared instance" → "un-sharing it (only its owner
  left) ends hosting, and a person's chat then moves to its owner's own
  partition like any un-shared one"; after "It has no join links." add
  "Its members can copy it into their own space (the transcript); its top
  bar has no Compact or Learn skill and it shows no sandbox picker." Its
  decision's "un-sharing ends hosting (90 §I10)" gains "— then AF's move
  takes a person's chat home".
- **AF's entry**: after "It is never listed in two homes; its owner's page
  follows it" add "(a non-secure conversation un-shared goes back to the
  shared instance first, and on from there)".
- **F8F's entry**: its "ends the stream of a follower who can no longer
  read the tile — xbind doesn't" describes the example; add "(the builtin
  agent's hub doesn't yet: a platform follow-up)".
- **I1's partitions page fix**: F11's entry (the page) needs no words —
  the row's Take back was always the design; if wanted: "Allowing an asked
  consent shows its Take back at once."
- **SH, F8F, I1** otherwise fold as their records give them.

## Tests

(`.dev.mk`'s env exported for integration runs and the harness; the Bash
sandbox off for isolated runs and the harness; `-parallel 3`)

| Run | Result |
|---|---|
| on 72946b86 as merged: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent`; `make fmt-check vet js-check js-test`; `go test ./...` | PASS (268 s); PASS (525 JS tests, 524 pass, 1 skipped as before); PASS |
| template `_backend`, new `hosted_moves_test.go` (six tests, above) with every `TestHosted*`, `TestUnshare*`, `TestMove*` — `-race -count=1` | PASS (25 tests); the three wiring changes reverted: `TestHostedUnshareMovesHome`, `TestHostedMovingAtGlobal`, `TestHostedExportAtGlobal` FAIL, as they should |
| **the legacy golden**: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` on the final template | PASS (283 s) |
| JS: `hack/agent-template-moves.test.mjs` (+1: the hosted hop), `hack/agent-template-hosted.test.mjs` (+1: the top bar and the sandbox store) | PASS |
| template browser tests from a scratch copy (web → the worktree's, node_modules → the dev playwright): `hosted homes share sidebar partition chat home attach automations settings native sandbox layout triggers channels grant long live-policy frame-policy` — no SKIP | PASS |
| … `terminal` | FAIL — pre-existing (§Bugs found 5) |
| `go test ./...` | PASS |
| `-race -count=1`: `./internal/broker ./internal/server ./internal/boot ./cmd/bx`; `sdk/` `./...` (sdk, sandboxcontract, ws) | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| zero-state goldens: `TestNoPartitionGolden` (boot, `-race`), `TestZeroStateRoute` (broker, `-race`), `TestZeroStateLifecycleFiles` (`test/`, integration) | PASS |

**Isolated suites** — `go test -tags=integration -count=1 -parallel 3 -v
-run 'TestPartitions' ./test/isolated/` on a real `xbind --isolate`
(160 s), then `-run '^(TestPartitionsAgent|TestCodingSandbox.*)$'` again
on the final e2e (506 s):

| Test | Subtests | Result |
|---|---|---|
| TestPartitionsSmoke | 19 (zero-state … mode-pending-unpartitioned) | PASS |
| TestPartitionsSmokeW2 | vault, global-address, personal-bind, terminal, cron | PASS |
| TestPartitionsSmokeMail | global-to-person, person-to-global, outsiders, listing, never-run-restart | PASS |
| TestPartitionsSmokeBackups | — | PASS |
| TestPartitionsAgent | modes, private-chats, shared-chats, unshare-moves, **hosted-chats** (with the un-share → home step), global-db-not-mounted, mailbox, sandboxes, existing-instance, llm-gw-counters (10) | PASS (both runs) |
| TestPartitionsAgentChannels | linked-dm, big-file, forged-outbox, private-webhook-trigger | PASS |
| TestPartitionsBridge | claim-at-global, group-at-global, link-at-global, unlink, webhook-team, llm-gw-counters | PASS |
| TestPartitionsPage | — | PASS |
| TestPartitionsTemplateMerge | — | PASS |
| TestPartitionsSecurity | tokens, read-only, events, terminal, host-net, global-no-route, notify, mode-deciders, bind-authority, personal-bind, consent, credential-reset, lost-read, crash (14) | PASS |
| TestPartitionsSmokeReap | — | SKIP ("the idle stop takes over ten minutes: XBIN_SMOKE_REAP=1 runs it", its own gate) |
| TestCodingSandbox, …VM | 12 each | PASS |
| TestCodingSandboxConsumers, …VM | agent, sandbox-terminal | PASS |
| TestCodingSandboxContract | 15 sections incl. user-partitions-xbind | PASS; declared SKIPs: tty/unsupported, lifecycle/archive, user-partitions/{apart, recreated, shares, person} |
| TestCodingSandboxContractVM | 14 sections | PASS; the same declared SKIPs |

**Downgrade and non-isolated** — `go test -tags=integration -count=1 -run
'^(TestPartitionsNoIsolate|TestDowngradePartitions|TestDowngradeStatic|TestDowngradeDormantRegistrations|TestSealedBackupsUpgrade|TestZeroStateLifecycleFiles)$'
./test/` (previous release v0.3.61 from `.dev.mk`; 144 s):
TestDowngradePartitions, TestDowngradeStatic,
TestDowngradeDormantRegistrations, TestSealedBackupsUpgrade,
TestZeroStateLifecycleFiles, TestPartitionsNoIsolate (7 subtests) — all
PASS, no SKIP.

**UI harness** (PORT 9081, HARNESS_DIR …/scratchpad/w5wire/h, a fresh
workspace each run, stopped and `ws/` deleted after each):

| Run | Pass | Checks |
|---|---|---|
| unisolated | partitionsEntry | 15 PASS |
| | personPage | 47 PASS |
| | personPageAsked | 12 PASS (+1: the row's Take back without a reload) |
| | partitionMark | 46 PASS |
| | adminPartitions | 36 PASS |
| | agentTemplate | 28 PASS |
| `HARNESS_NO_OVERLAY=1` | oldScaffold | 15 PASS |
| `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1` | agentHosted | 24 PASS (+6: the bar's control and hosted check, the un-share, the follow, the transcript) |
| | agentMoves | 17 PASS |
| | agentHomes | 20 PASS |
| | channelsPartitioned | 15 PASS |

No FAIL and no SKIP in any pass. Screenshots looked at:
`person-page-asked-allowed` (the new row: Take back, no "Allowed:"),
`partitions-entry` (the settings menu's "your partitions ↗"),
`agent-hosted-live-dev1` and `agent-hosted-paused-admin` (the hosted bar:
⚠ NOT PRIVATE, no Compact / Learn skill; Confirm/Decline above the
composer), `agent-hosted-unshared-home` (in the admin's own space:
private, Compact and Learn back, AF's note, the transcript — §Looks
wrong 1).

**`make check`** on the final tree (the record aside): green (">> make
check: green"; 527 JS tests, 526 pass, 1 skipped as before; native 86).
