# W3b-wire — wave 3b's seams wired, and the owner's answers

Wave 3b's integration of the partitioned-tiles plan: `partitions` at
39710b6f (the merge of `pt/b2a-fix`) holds the fabric, B2a (the agent
template running partitioned) and wave 3a (F6 partition mail, F7b
operations, F17b partition archives; records/W3a-wire.md). This change wires
B2a to F6, applies the owner's answers of 2026-09-30, and removes the
template-merge conflict B2a's default brought to every existing agent
instance. Branch `pt/w3b-wire`. It edits neither `docs/changelog.md` nor
`plans/DECISIONS.md` (see §Notes for the changelog).

## What the merge left

The tree built and its tests passed on 39710b6f; the open ends were the
seams the packs named (B2a's `mailSource`, W3a's "the agent's mailbox"
leftovers) and the owner questions below. One flake surfaced in the full
broker run (§Bugs found, 1).

## Wired, fixed and changed

### 1. B2a ↔ F6: the agent's `/mailbox`

- **The source is xbind's partition mail through the SDK.**
  `builtin-templates/agent/_backend/mailbox.go`: `sdkMail` reads with
  `xbin.InboxPage(after, limit)` and acks with `xbin.Ack(ids...)`
  (`sdk/mail.go`). The template's `go.mod.tile` requires
  `github.com/xbin-dev/xbin/sdk v0.0.0`, resolved by the workspace's
  `go.work` (and `hack/tile-check.sh`'s replace), so an instance always
  builds against its xbind's SDK and the new symbols need no module change.
  The seam's interface became `Page(ctx, after, limit) (items, more, err)` /
  `Ack(ctx, ids...)`; `partitionMail` defaults to `sdkMail{}` (an
  unpartitioned instance never pulls: `pullMail` returns at once).
- **W3a's leftovers:**
  - *Page while `More`*: a pull reads on with the last item's id while
    xbind says `more` (a page cut at ~8 MiB is short but not the end); a
    page that moves the cursor nothing on ends it too.
  - *Dedupe by id*: unchanged from B2a — a handler's effect and the item's
    id (`mail_seen`) commit in one transaction; a redelivered item is only
    acked.
  - *Acks*: one `Ack` per page (handled items, redeliveries, unknown topics
    past their grace); a read or an ack that fails stops the pull and the
    doorbell answers 502 (xbind rings again on its steps).
  - *Unknown topics* — 04 §3 says only "at least once until acked or
    expired"; B2a left them for their whole ttl (a newer global mid-deploy
    may be the sender), F6's docs say to ack what you won't handle (else
    each item starts a stopped partition at every doorbell step for up to
    30 days). **Chosen: both, in time** — an item of a topic this version
    has no handler for stays while it is younger than 30 minutes
    (`mailUnknownGrace`, from its `at`, else from when this process first
    saw it: past the doorbell's first steps at once, 1 min and 5 min, and
    any deploy), and is then acked unhandled and logged. The doorbell's
    answer gains `dropped` (`{handled, left, dropped}`).
  - *Pull at start*: `main.go` runs `agent.pullMailAtStart()` (2-minute
    bound, logs what it did or why it failed); nothing unpartitioned.
- **Tests.** Template `_backend/mailbox_test.go` (moved out of
  `partition_routes_test.go`): `TestMailboxSkeleton` (B2a's, on the new
  interface), `TestMailboxPages` (pages cut short with `more`, one ack a
  page, a failed ack stops and the next pull only acks),
  `TestMailboxUnknownTopics` (young stays, past the grace is acked, a
  missing `at` counts from first sight), `TestMailboxSDK` (the adapter
  against a fake xbind gateway speaking F6's wire: `limit=50`,
  `after=…&limit=50`, the acks, and an older xbind's 404 → 502 naming
  `partition-mail/1`). e2e: `TestPartitionsAgent`'s new **mailbox** case
  compiles a builder's two topics into the instance (`paMailTopics`, the
  way a builder adds hers): alice's frame mails global `e2e/ping`, the
  global instance's handler mails her partition `e2e/note`, her partition's
  `/mailbox` makes one conversation of it — exactly once, checked after
  later rings and starts — and both inboxes end acked.
- **Docs.** The template's API.md "Partition mail" bullet.

### 2. The owner's answer on the global inbox (2026-09-30)

- **Rule.** `internal/broker/partitionmail_api.go`: `mailBoxOf` is the
  inbox a principal reads — a person's partition's (unchanged), or the
  global instance's for its backend **and** for the tile's frames,
  terminals and agent sessions whose partition is global (no person behind
  them: the owner token's frames, root terminals; agent sessions are
  terminal principals), normalised to the tile (an `xbin.window` sub-path
  acts as its tile, as the partition gate does), on the primary. `GET` and
  `ack` use `mailReader`; the send uses `mailSelf`, which refuses those
  readers with 403 "only the global instance's backend sends partition mail
  as global…". Still refused: view-as (the impersonator check comes first),
  other tiles, people's partitions (their own inbox only — an admin's
  frame included, PD-11), a deployment beyond the primary, deliveries.
- **Sending as global stays the backend's.** 04 §3's sender table lists
  "owner-token frames, global's frames" among those who send nothing, and
  its "Only the addressee's principals read (GET) and ack" is about
  reading; the owner's answer is about reading and acking. So the frames
  and terminals acting as global read and ack, and send nothing.
- **Tests.** `TestMailGlobalInboxReaders` (new: the owner token's frame, a
  root terminal and an agent session read global's inbox and ack there
  only; alice's frame and terminal, admin bob's frame, shell and partition
  read their own; an ack by the owner's frame is gone for the backend);
  `TestMailRules` (global's frame and a root terminal sending as global:
  403 with the reason); `TestMailInboxPrivacy` (view-as alice and view-as
  global, another tile's frame, global's frame on the dev deployment
  refused; global's frame no longer among the refused readers). e2e: the
  agent's mailbox case (the owner's frame reads global's inbox empty after
  the handlers acked, is refused sending, sees an unknown topic wait there
  while young and acks it) and the mail smoke's `outsiders` case (the
  owner's frame on a plain partitioned tile).
- **Docs.** [/docs/protocol.md](/docs/protocol.md) (`POST` and `GET
  /partitions/mail`), [/docs/partitions.md](/docs/partitions.md) §Partition
  mail (the sender table's new row, "Only the addressee reads", the
  closing line on what global reads), [/docs/bx.md](/docs/bx.md) (`bx
  partition mail` in the root terminal reads global's), the OpenAPI
  strings, `cmd/bx/partitionmail.go`'s comment.

### 3. Never break users: the template-merge conflict

- **Where it happens.** Not in the builtin updater: `internal/builtins`'
  `ApplyMerge`/`ApplyReplace`/`Propose` handle builtin tiles and the
  scaffold only — template instances are deliberately untracked there
  (updates.go's scope; no unit, no marker). An instance takes template
  changes with `git merge template/main` from the builtin template's served
  repo (`internal/broker/templaterepo.go`, D50), which mirrored the
  template's files verbatim, `template` block included. Instantiation
  strips the block, so any change inside it (B2a's `partition` line and its
  comment) was a change to lines the instance doesn't have: a conflict in
  every existing instance.
- **Fix, at the served repo** (`internal/broker/templaterepo_block.go`,
  with F13b's jsonc helpers `TopLevel`, `SetTopLevel`, `DeleteTopLevel`):
  the repo's `xbin.json` never changes the block. A repo this xbind creates
  carries none (the member deleted, the rest of the manifest byte for
  byte); one an older xbind created keeps the block it has, verbatim
  (spliced into each new snapshot's manifest). So a block-only change is no
  change to any file an instance merges: the merge is clean, the instance
  keeps its mode and has no `template` key.
- **Reported as information, not a conflict.** The snapshot commit that
  first sees a changed block says so — "The template's "template" block
  changed — what new instances are made from; they now start with partition
  […]. An instance never carries that block … your instance keeps its own
  mode" — as an empty commit when nothing else changed (so `GET
  /templates/updates` lists the instances once, and their merge is a
  no-op), with an `Xbin-Template-Block: sha256:…` trailer so it is said
  once; a repo an older xbind wrote is compared with the block it carries.
- **Tests.** `TestAgentTemplateDefaultKeepsExistingMode` (the real agent
  template) now requires the merge to be **clean**: an instance made from
  the template before the default, seeded from a repo written the way an
  older xbind wrote it (block and all), merges today's template cleanly,
  has no partition and no `template` key, and the snapshot's message
  carries the note; a second start commits nothing; a repo this xbind
  creates carries no block and keeps every other line and comment; a
  partitioned instance of today's template merges a later default change
  (`["user"]`) cleanly and keeps `["user","global"]`. It fails on the old
  materialisation. F13b's `TestTemplateMergeNeverAddsPartition` expected
  conflicts for a changed default; it now expects clean merges that keep the
  instance's value, bring no block and take upstream's other change (the
  title). F13b's `internal/builtins` tests are untouched and green.
- **Docs.** [/docs/partitions.md](/docs/partitions.md) §The mode ("A
  template"), [/docs/protocol.md](/docs/protocol.md) (`GET
  /templates/{repo}/…`).

### 4. The owner's answer on static MCP servers with auth headers

- In a partitioned agent they stay the global instance's (B2a keeps them
  out of `conf`); a person's path is a tile bind or a personal bind. The
  settings' **MCP** list now shows a partitioned instance's static servers
  from the config (`GET /config`, a manager's view) and marks each one with
  `headers`: "works in shared (global) conversations only — to use it in
  your own conversations, bind it as a tile or a personal bind".
  - web: `partition-ui.js` `mountStaticMcp` (a "Static servers (config)"
    table under the bound ones; one line in `agent.js` `tabMcp`);
  - native: `native/settings.js` `mcpTpl` (a section, `detail` "shared
    only" on the row, the sentence as its footer);
  - both from `model/partition.js` (`staticMcp`, `mcpNote`,
    `MCP_GLOBAL_ONLY`); feature key `state.partition.mcp` in the three
    registries.
- An unpartitioned instance's list is unchanged: no element, no call (the
  web test checks the element; the native test checks no `GET /config`).
- **Tests.** `hack/agent-template-partition.test.mjs` (the model),
  `hack/agent-template-native-partition.test.mjs` (new, beside the native
  test at its size cap: the screen partitioned and not), the template's
  browser test `test/partition.mjs` (the web tab in all three layouts; the
  header's value never on the page); `test/native-stub.mjs` passes
  `seed.partition` through.
- **Docs.** The template's API.md "Settings are the tile's".

### 5. The full run

§Tests below. One flake fixed (§Bugs found, 1); nothing else broken by the
merge.

## Owner answers applied

- **The global inbox (2026-09-30):** the tile's principals acting as global
  read and ack it (item 2). Sending as global: the backend's alone, from 04
  §3's sender table.
- **Static MCP servers with auth headers:** global-only in a partitioned
  agent; bind as a tile or a personal bind; the settings say so (item 4).
  B2a's owner question ("a vault-backed path for partitions, or bind it as a
  tile / personal bind?") is answered by the latter.

## Deviations

- **Item 3 is fixed in the served template repo, not the builtin updater**
  (see above): the updater never meets an instance. The informational
  report is the snapshot's commit message and trailer (what a builder reads
  in `git log template/main`), not an `Applied.Notes` line or a new API
  field.
- **Unknown mail topics: a 30-minute grace, then acked** — between B2a's
  "leave until it expires" and F6/W3a's "ack what you won't handle".
- **"Agent sessions" are terminal principals** (`Via: terminal`); the rule
  covers every frame and terminal of the tile whose partition is global,
  path tickets and `xbin.window` sub-path frames included.
- **The MCP note lists static servers only in a partitioned instance's
  settings**, only for managers (it reads `GET /config`); nothing changes
  unpartitioned.
- **`POST /mailbox` answers `dropped` too.**

## Seams still open, by pack

- **B2b** — unchanged from B2a's record: shared conversations at global
  (`userNoShare`, `sharesInPartition`, the Shared list and router by id,
  global mode's F5 persons).
- **B2c** — the handlers: `mailHandlers["handoff/dm"]`, `["handoff/event"]`
  (a person's side) and `["outbox/add"]` (global's side, checking
  `it.From` against its own handoff record, 08 §5). Notes for them: a
  handler runs inside the db transaction that records its id, so a handler
  that mails or calls out (as the e2e's global handler does) holds the
  write lock for that call — send after the commit (`t.AfterCommit`) where
  it matters; items of a topic B2c introduces are safe mid-deploy for 30
  minutes (the grace); `LedgerTrigger` — the global instance passes a
  private trigger's `source` (`xbin.MailWith(…, MailOptions{Source})`).
  Global's inbox can now also be acked by the owner token's frames and root
  terminals: global's handlers must not assume only their backend acks (an
  item acked by a manager's shell is simply gone).
- **B2d** — unchanged (`teamStore`, `teamUnavailable`, `ownConversation`).
- **F11** — unchanged from W3a (the person's page, mail counts included).
- **F12** — unchanged from W3a; the admin tile's frames are the admin
  tile's, not the partitioned tile's, so they don't read a tile's global
  inbox (counts only, `PartitionMailCounts(tile, "")["global"]`, if
  wanted).
- **F14b** — the shell's consent prompts (unchanged).
- **B3** — fold the changelog and decision entries of B2a, F6, F7b and
  F17b with §Notes below; B2a's changelog paragraph needs the correction
  there.
- **I1** — (1) instantiation re-marshals `xbin.json`
  (`builtins.stripTemplateBlock`: `json.MarshalIndent` — comments dropped,
  keys sorted), so *any* upstream change to a template's manifest conflicts
  in *every* instance's `git merge template/main`: checked with the real
  pre-B2a agent template (master's), an instance at `apps/agent` and one at
  `apps/my-agent` both still conflict in `xbin.json` on B2a's other
  manifest changes (the `conf` and `team` uses, `partitionMail`,
  `partitionNote`) — no longer on the block. For an unpartitioned instance
  "keep ours" is right (it never opens `conf`/`team`, and xbind rings no
  mailbox unpartitioned); one that later switches to partitioned must add
  those uses (B2a's start log says so). A JSONC-aware strip at
  instantiation (`jsonc.DeleteTopLevel` + `SetTopLevelLike`) would make new
  instances' manifests merge line by line; not done here (it changes what
  instantiation writes). (2) A ring in flight can re-arm a doorbell right
  after `forgetBellsOf` (production-benign: the ring finds nothing; the
  test fix is in §Bugs found). (3) The SDK's mail calls take no context:
  a hung xbind would hold the agent's pull (and `mailMu`) past the
  doorbell's 2-minute bound — ctx variants are F8's to add.
  Carried from W3a: the list there stands.
- **F8 finalization** — the Node/Python partition-mail snippets in
  [/docs/sdk.md](/docs/sdk.md); context-taking `InboxPage`/`Ack` (above).

## Bugs found

1. **`TestMailStartKeepsBackoff` flaked** (about one run in eight) with
   "TempDir RemoveAll cleanup: directory not empty": a doorbell ring in
   flight at the test's end started alice's partition again (writing its
   record) and armed a new bell after `forgetBellsOf`. Fixed in the test:
   the fake starts nothing once the test is over, and the bells are
   forgotten again after an in-flight ring (15/15 runs green).
2. **B2a's changelog text misstated the merge conflict**: not "in the
   `template` block" but the whole `xbin.json` (the re-marshal, I1 above);
   the block no longer conflicts. §Notes has the corrected paragraph.
3. **B2a's mailbox took a short page as the end and left unknown topics
   for their ttl** (W3a's findings): fixed with item 1.

## Notes for the changelog (the owner folds these in)

- **B2a's entry**, replace "New in every instance: … `POST /mailbox` (the
  partition-mail doorbell, `partitionMail`; an unpartitioned instance
  answers 404)" with: "`POST /mailbox` — the partition-mail doorbell
  (`partitionMail`): it and every start read the instance's inbox from
  xbind (page by page), hand each item to its topic's handler once, and
  acknowledge it; an item of a topic the code doesn't handle is
  acknowledged unhandled 30 minutes after it was sent; it answers
  `{handled, left, dropped}` (an unpartitioned instance answers 404)". And
  replace its last sentences ("Existing instances take the new files with a
  template merge … keep your side. Nothing to change.") with: "Existing
  instances take the new files with a template merge (`git merge
  template/main`); the template's `template` block never reaches it (see
  the templates entry), but an instance's `xbin.json` — rewritten when it
  was created — conflicts on this version's manifest changes: keep your
  side (an unpartitioned instance needs none of them; to switch one to
  partitioned later, add the `conf` and `team` lines to its `uses` and
  `"partitionMail": "/mailbox"`)."
- **F6's entry**, replace "Each inbox is read … and acknowledged … only by
  its addressee — admins, the root token and other tiles get 403" with
  "Each inbox is read and acknowledged only by its addressee — a person's
  partition by its backend and that person's frames, terminals and agent
  sessions; the global instance's by its backend and by the tile's frames,
  terminals and agent sessions acting as global (the owner token's frames,
  root terminals), which send nothing — admins, the root token, view-as
  and other tiles get 403", and "`bx partition mail ls|ack` reads and
  acknowledges the inbox of the partition a terminal runs in" gains "(the
  tile's root terminal: the global instance's)".
- **New (templates):** "**A builtin template's `template` block never
  reaches a template merge** ([partitions.md](/docs/partitions.md) §The
  mode, [protocol.md](/docs/protocol.md) `GET /templates/{repo}`). The
  repository xbind serves as each instance's `template` remote no longer
  changes the template's `template` block (instances never carry it): one
  created by this xbind has none, one an older xbind created keeps its own.
  A change to the block — like the agent template's new default for new
  instances — is no longer a conflict in `git merge template/main`; the
  snapshot's commit message says what changed and what new instances now
  start with (an empty snapshot when nothing else changed, so the Tile
  Manager's Updates lists the instance once). Nothing to change."
- **New (the agent template):** "In a partitioned agent the settings' MCP
  list shows the static MCP servers of the config and marks one with
  `headers`: it works in shared (global) conversations only — bind it as a
  tile or a personal bind to use it in your own (the template's API.md)."

## Tests

On the branch (.dev.mk's env exported for the integration runs, the Bash
sandbox off; no SKIP anywhere):

| Run | Result |
|---|---|
| `go build ./...`, `make fmt-check vet js-check js-test` (454 JS tests, 1 skipped as before) | PASS |
| `go test ./...` (56 packages) | PASS |
| `-race`: `./internal/broker ./internal/server ./internal/builtins ./internal/boot ./cmd/bx ./sdk` | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| zero-state goldens: `TestNoPartitionGolden` (boot), `TestZeroStateRoute` (broker) | PASS |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — the legacy golden (every existing template test) plus the mailbox tests | PASS (220 s) |
| `TestMailStartKeepsBackoff` ×15 after its fix | PASS |
| template browser tests from a scratch copy (`BX_KIT`, `BX_WEB`): `test/partition.mjs`, `test/settings.mjs`, `test/native.mjs` | PASS |
| smokes: `go test -tags=integration -count=1 -v -run 'TestPartitions(Smoke\|SmokeW2\|SmokeMail\|SmokeBackups\|Agent)$' ./test/isolated/` — Smoke (19 cases, 43 s), SmokeW2 (5, 74 s), SmokeMail (5, 38 s), SmokeBackups (20 s), Agent (6 cases incl. the new `mailbox`, 60 s) | PASS |
| `make check` (final tree), with `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` (215 s) and `TestPartitionsAgent` (58 s) again on it | PASS ("make check: green") |
