# F8F — records for the integrator

Work pack F8 **finalization** (95-work-packs.md "F8", its "Finalization
adds (90 §I4)"; spec 07) of the partitioned-tiles plan, branch `pt/f8f` on
`partitions` at 3092f0f2. SDK and builder docs only, plus one small wiring
of the new SDK calls into the agent template's mailbox (§Deviations 1).
This pack edits neither docs/changelog.md nor plans/DECISIONS.md; the texts
below go there at merge (they extend F8's skeleton entries, records/F8.md).

Commits:
- `02afdf31` sdk: the partition mail calls' Context variants, tests,
  sdk.md and 16-extending.md;
- `74d47804` docs/partitions.md §Realtime between partitions, the Go SDK's
  `Example_realtime` with a test against a stand-in xbind, the docscheck
  quote guard and the node test of the page's JavaScript;
- `ece88f58` docs/sdk.md: partition mail from node and python, and a node
  test that runs both snippets;
- `5cb05d5f` docs/partitions.md: the status line, and the review pass
  (plus one stale sentence in resources.md);
- `09a3e7c8` agent template: the mailbox's reads and acks honour the pull's
  deadline;
- this record.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **Partitioned tiles: realtime between partitions, and partition mail
  from any runtime** ([partitions.md](/docs/partitions.md) §Realtime
  between partitions, [sdk.md](/docs/sdk.md)). The builder page names the
  three ways live state crosses people's partitions, each for its job —
  tile-wide state is a shared resource plus a shared bus (every reader's
  page follows it); member-scoped state has the global instance as hub
  (pages follow its stream with `xbin.fetch(…, {partition: 'global'})`,
  partitions post to it with `xbin.GlobalURL`, and the hub judges
  membership and stamps who posted from the call); a partition that must
  be woken gets partition mail — with one worked example tile using all
  three, whose Go backend is the SDK's `Example_realtime`. The Go SDK's
  mail calls gain Context variants — `MailContext`, `MailWithContext`,
  `InboxPageContext`, `InboxContext`, `AckContext` — which give up when
  their context ends, with an error wrapping the context's; the plain
  calls are unchanged. sdk.md gains partition mail for node and python
  backends (a helper for xbind's API through the gateway, sending, and
  draining the inbox page by page). The builtin agent template's mailbox
  now bounds each read and ack by its pull's two-minute deadline, so a
  hung xbind can't hold it. Nothing to change.
```

(The status line of partitions.md stays "in development until the release
notes say otherwise": the release notes of the I2 rollout lift it —
§Seams, I2.)

## Decision entry (plans/DECISIONS.md, the next free D-number)

Implements 90 §I4 (realtime between partitions: global is the hub, no new
xbind primitive) as builder docs, and closes W3b-wire's "the SDK's mail
calls take no context". Suggested text:

```markdown
- **D<next> — Partitioned tiles, SDK and docs finalized: realtime patterns,
  Context mail calls.** Extends the F8 skeleton's entry.
  - **Realtime between partitions is documentation, not a primitive** (90
    §I4): three patterns, each named by the job it does, on what partitioned
    tiles already have — tile-wide live state is a shared resource and a
    shared bus (reaches every reader of the tile); member-scoped live state
    has the global instance as hub (pages attach through their own
    partition's F5 call, partitions post with `GlobalURL`, the hub judges
    membership at every post and stamps the sender from the call, never the
    body); waking one partition is partition mail (durable, doorbell). The
    member gate is "From is this tile, X-XBin-Partition is user:<User>, no
    view-as": exactly the calls a person makes through their own partition;
    other tiles, the root token and public requests act for no member.
  - **The worked example is code that runs, and the page can't drift from
    it.** The Go backend is `Example_realtime` in the SDK
    (sdk/example_realtime_test.go, compiled by `go test`), run by a test
    against a stand-in xbind that plays each instance by `XBIN_PARTITION`
    and forwards `?xbin-partition=global` as xbind does; internal/docscheck
    checks every Go block of the section is quoted line for line, in order,
    from that file (blocks may elide at `// …` lines); the section's
    JavaScript blocks are taken from the page and run against
    xbin-client.js (hack/partitions-realtime.test.mjs). Not chosen: an
    `examples/` tile — it would need a real partitioned xbind to test, and
    the SDK's example is what `go doc` shows builders.
  - **Context variants, named `…Context`** (database/sql's convention; the
    SDK had none yet — `NotifyUser` and `AccessOf` take ctx first because
    they were born with it): `MailContext`, `MailWithContext`,
    `InboxPageContext`, `InboxContext`, `AckContext`. The plain calls are
    the variants with `context.Background()`: same requests, same answers,
    no deadline added behind a caller's back (compat rule 8). An error from
    an ended context wraps it (`errors.Is(err, context.DeadlineExceeded)`),
    including one that ends while a 200's body is read; a send or ack xbind
    completed just before may still take effect — the addressee dedupes by
    id, as for every item.
  - **The node and python mail snippets use each runtime's standard library
    alone** (node's `http` over `socketPath`, python's `http.client` with an
    AF_UNIX connect), with a silent-minute timeout, and are run from the
    page against a stand-in gateway (hack/sdk-mail-snippets.test.mjs;
    python3 when present).
  - **The page's status stays "in development until the release notes say
    otherwise"**: no release carries partitioned tiles; the I2 rollout's
    release notes lift it and the other docs' "(in development)" notes
    together. Its "Items marked TODO" sentence is gone: none is left.
```

## Seams

- **I2 (rollout)** — when the release notes ship, lift every "in
  development" note in one change: docs/partitions.md's status block (line
  3), docs/sdk.md (§Resources' Subscribe exception, the "Partitioned tiles
  (in development)" heading and its first paragraph, the in-frame
  `xbin.partition` comment), docs/elements.md (`XBIN_PARTITION` row),
  docs/frontend-kit.md (the xbin-client row), docs/overview/04-frontend.md
  (`xbin.partition` row), docs/overview/16-extending.md (the
  `XBIN_PARTITION` row and the SDK table's Partitions row), docs/protocol.md
  (two notes, lines ~182 and ~6525) and sdk/xbin.go's `Subscribe` comment.
  `git grep -n "in development" docs sdk` lists them (docs/native.md's is
  the app's, not ours).
- **B2d** — the page's pattern 2 names "the builtin agent's shared
  conversations" as its instance (§The mode's agent paragraph and the hub's
  last bullet). If B2d's hosted conversations or I10's un-share change how
  a page reaches them, those two sentences follow; nothing else here
  describes them.
- **SH (I13, the "Your partitions" menu entry)** — docs/partitions.md §Your
  partitions page lists what links the page; the menu entry belongs in
  that list once SH ships it.
- **Agent template docs** — builtin-templates/agent/API.md line ~372 says
  the mailbox pulls "through the SDK (`xbin.InboxPage`, …)"; it now calls
  `InboxPageContext` (the same read, bounded). Left for B2d/AF, who edit
  API.md; a one-word change if wanted.
- **Scaffold** — workspace-template/AGENTS.md's SDK cheat sheet still lists
  none of the partition functions (F8's open item; a scaffold change, lands
  with `bx builtin update`). Not done here: it changes what `xbind init`
  writes.
- **The guards this pack adds** — `TestPartitionsRealtimeQuotes`
  (internal/docscheck/realtime_test.go) fails when the page's Go quotes and
  sdk/example_realtime_test.go part; hack/partitions-realtime.test.mjs
  when the page's three JavaScript blocks stop running as documented (it
  expects exactly three, defining `board`; `follow`, `post`; `mine`);
  hack/sdk-mail-snippets.test.mjs when sdk.md's node or python mail block
  does (one each, in §node backend / §python backend, found by the mail
  route in them). Anyone editing those docs edits the example or the tests
  with them.
- **sdk/export_test.go** exports `ResetClient()` to the SDK's external
  tests (test-only; not public API).

## Deviations

1. **The agent template changed** (`09a3e7c8`), though this pack is SDK and
   docs: `sdkMail` (builtin-templates/agent/_backend/mailbox.go) now calls
   `InboxPageContext`/`AckContext` with the pull's ctx, and its comment no
   longer claims the SDK's calls carry their own deadline (they never
   did). Without it the Context variants fix nothing W3b-wire named. Two
   lines plus a new test file (`mailbox_deadline_test.go`); the only
   behaviour change is a partitioned instance's pull giving up after two
   minutes of a hung xbind instead of hanging. Unpartitioned instances
   never pull. It is its own commit: drop it if B2d/AF rework `sdkMail`.
2. **resources.md** (not in the pack's list) lost a stale sentence:
   "Deleting a deleted person's partitions 30 days later is not available
   yet" — F7b built it (`PartitionPersonDeleted` orphans at once, the sweep
   deletes after `partitionRetention`). It now says what partitions.md
   says.
3. **The JavaScript snippets run in node, not a ui-harness pass**: they
   need only the client and xbind's answers, which the node test stubs as
   hack/xbin-client-partition.test.mjs does; a real xbind is exercised by
   the Go example's stand-in instead. No UI, so no harness pass.
4. **The review pass added three sentences, not only rewording**: the
   per-partition list names the mail inbox, and the "who reaches which
   partition" table gains two rows (the same-tile call to global, the mail
   doorbell) that the rest of the page already relied on.

## Tests

| Run | Result |
|---|---|
| sdk: `go vet ./...`; `go test -race -count=1 ./...` (sdk, sandboxcontract, ws) | PASS |
| sdk: `TestMail*` `-race -count=5` (new: `TestMailContextShapes`, `TestMailContextHungXbind` — no answer and a stalled body) | PASS |
| sdk: `TestRealtimeExample` (the three patterns against the stand-in: board and bus; invite, refusals of a non-member, another tile, the root token and view-as; a partition's post through global reaching another member's stream; the mention mailed, kept once under redelivery, pushed once; mentions per person) `-race -count=3` | PASS |
| `go test ./internal/docscheck` incl. `TestPartitionsRealtimeQuotes` (and checked to fail on an edited quote) | PASS |
| node: `hack/partitions-realtime.test.mjs` (4), `hack/sdk-mail-snippets.test.mjs` (2: node, python3 3.14) | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| `make fmt-check vet js-check js-test` (CI's gofmt; 511 JS tests, 510 pass, 1 skipped as before) | PASS |
| `TILE_TEST_FLAGS="-race -count=1 -run TestSDKMail…" hack/tile-check.sh agent` (new `TestSDKMailGivesUpAtTheDeadline`) | PASS |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` (legacy golden + every template test) | PASS (238 s) |
| smokes on a real `xbind --isolate` (.dev.mk's env, the Bash sandbox off): `TestPartitionsSmokeMail` (5 subtests), `TestPartitionsAgent` (8, incl. `mailbox` — the agent built against this SDK), `TestPartitionsAgentChannels` (3) | PASS, no SKIP (71.7 s) |

No UI changed: no ui-harness pass. The agent template's browser tests
weren't run (no frontend file changed).

## Owner questions

1. **A partition option for `xbin.ws`?** Pattern 2's pages follow the
   global instance with a streaming `xbin.fetch(…, {partition: 'global'})`;
   `xbin.ws(path)` takes no options, so a page wanting a WebSocket to
   global appends `?xbin-partition=global` itself (xbind accepts it from
   every document of a partitioned tile — it changes nothing for
   credentials already in global — but on an unpartitioned tile it would
   reach the backend as a plain parameter). *Recommendation: not now* —
   fetch streaming covers the pattern and the agent's shared
   conversations; add `xbin.ws(path, {partition: 'global'})`, mirroring
   fetch's rules, when a tile needs a two-way socket to global.
