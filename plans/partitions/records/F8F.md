# F8F — records for the integrator

Work pack F8 **finalization** (95-work-packs.md "F8", its "Finalization
adds (90 §I4)"; spec 07) of the partitioned-tiles plan, branch `pt/f8f` on
`partitions` at 3092f0f2; the review's fixes on `pt/f8f-fix` (§Review
fixes). SDK and builder docs only, plus one small wiring of the new SDK
calls into the agent template's mailbox (§Deviations 1) and the workspace
`AGENTS.md`'s SDK cheat sheet (§Deviations 5). This pack edits neither
docs/changelog.md nor plans/DECISIONS.md; the texts below go there at merge
(they extend F8's skeleton entries, records/F8.md).

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
- `2a62bbaf` this record;
- on `pt/f8f-fix`, the review's fixes (§Review fixes).

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **Partitioned tiles: realtime between partitions, and partition mail
  from any runtime** ([partitions.md](/docs/partitions.md) §Realtime
  between partitions, [sdk.md](/docs/sdk.md)). The builder page names the
  three ways live state crosses people's partitions, each for its job —
  tile-wide state is a shared resource plus a shared bus (every reader's
  page follows it — and a row's key there proves no author); member-scoped
  state has the global instance as hub (pages follow its stream with
  `xbin.fetch(…, {partition: 'global'})`, partitions post to it with
  `xbin.GlobalURL`; the hub keeps the members, stamps who posted from the
  call, and ends the stream of a follower who can no longer read the tile
  — xbind doesn't); a partition that must be woken gets partition mail —
  with one worked example tile using all three, whose Go backend is the
  SDK's `Example_realtime`. The Go SDK's mail calls gain Context variants
  — `MailContext`, `MailWithContext`, `InboxPageContext`, `InboxContext`,
  `AckContext` — which give up when their context ends, with an error
  wrapping the context's; the plain calls are unchanged. A send that gives
  up (or fails without xbind's answer) may or may not have made its item:
  sending again makes another item with another id, so a sender that
  retries puts its own key in `data` for the addressee to dedupe by.
  sdk.md gains partition mail for node and python backends (a helper for
  xbind's API through the gateway, sending, and draining the inbox page by
  page), and the workspace `AGENTS.md` (new workspaces) lists the
  partition calls in its SDK cheat sheet. The builtin agent template's
  mailbox now bounds each read and ack by its pull's two-minute deadline,
  so a hung xbind can't hold it. Nothing to change.
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
    shared bus (reaches every reader of the tile; pages subscribe before
    they read the snapshot, so a change during the read isn't lost);
    member-scoped live state has the global instance as hub (pages attach
    through their own partition's F5 call, partitions post with
    `GlobalURL`, the hub checks the poster's membership at every post and
    stamps the sender from the call, never the body); waking one partition
    is partition mail (durable, doorbell). The member gate is "From is this
    tile, X-XBin-Partition is user:<User>, no view-as": exactly the calls a
    person makes through their own partition; other tiles, the root token
    and public requests act for no member.
  - **The hub, not xbind, ends a stream when its person loses the tile.**
    xbind judges access when a call arrives and doesn't end a stream it
    already let through, so a person removed from the tile, disabled or
    deleted would keep receiving a room's posts. The example's hub asks
    xbind about each follower at a post (`xbin.AccessOf`, a yes trusted for
    10 s) and drops one who can no longer read the tile — out of the room,
    their streams closed; members also leave, or are taken out by a member
    (`DELETE /rooms/{room}/members/{user}`, the example's rule). Not
    chosen: re-checking only on follow (it misses the open stream), or an
    xbind primitive that severs proxied streams on access loss (a platform
    change beyond docs — §Owner questions 2).
  - **A shared (`true`) resource's row key is not proof of its author**:
    anything acting in any partition may write any row or publish any
    topic on a shared bus. The page says so, and routes author-bearing
    state through global (a `"read"` resource, the author stamped from the
    call).
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
    including one that ends while a 200's body is read. A read that gives
    up loses nothing; an ack that gives up may have taken effect (acking
    again is nothing to do). **A send that gives up — or fails without
    xbind's answer — may or may not have made its item, and the sender
    never learns the id**: sending again makes a second item with a new
    id, which dedupe by item id (redelivery of the same item) doesn't
    catch, and `POST /partitions/mail` takes no idempotency key. A sender
    that retries puts its own key in `data` (the source event's id) and the
    addressee dedupes by it — as the agent template's handoffs (`ho:<id>`)
    and outbox (`Key`) already do — or doesn't retry. Not chosen: an
    idempotency key on the route (xbind surface; the data key covers it).
  - **The node and python mail snippets use each runtime's standard library
    alone** (node's `http` over `socketPath`, python's `http.client` with an
    AF_UNIX connect), with a silent-minute timeout; a non-JSON 200 rejects
    the call (node parses inside a try: a throw in a response listener
    would crash the backend). They are run from the page against a
    stand-in gateway (hack/sdk-mail-snippets.test.mjs; python3 when
    present).
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
  (two notes, lines ~182 and ~6525), sdk/xbin.go's `Subscribe` comment,
  and workspace-template/AGENTS.md's partition rows in the Go SDK cheat
  sheet (added by this pack's fix).
  `git grep -n "in development" docs sdk` lists them (docs/native.md's is
  the app's, not ours).
- **B2d** — the page's pattern 2 names "the builtin agent's shared
  conversations" as its instance (§The mode's agent paragraph and the hub's
  last bullet). If B2d's hosted conversations or I10's un-share change how
  a page reaches them, those two sentences follow; nothing else here
  describes them. **Looks wrong, unverified:** the agent's event hub
  revalidates a follower's `/stream` when a conversation's ACL changes
  (`revalidate`, events.go), but I found nothing that re-checks a
  follower's access to the tile itself — so a person removed from the
  tile, disabled or deleted may keep an open stream of a shared
  conversation at global until it ends: the gap the review found in this
  pack's example. Not tested end to end; B2d (or I1's tests) should check
  it — the fix there is the example's (`xbin.AccessOf` at delivery, or
  on a timer, and cut the stream).
- **SH (I13, the "Your partitions" menu entry)** — docs/partitions.md §Your
  partitions page lists what links the page; the menu entry belongs in
  that list once SH ships it.
- **Agent template docs (B2d, AF)** — builtin-templates/agent/API.md
  lines 372 and 376 now name `xbin.InboxPageContext` / `xbin.AckContext`
  (what `sdkMail` calls). Two words, line-local: if B2d or AF rewrote
  that paragraph, keep their text and those two names.
- **Scaffold** — workspace-template/AGENTS.md's Go SDK cheat sheet gains
  seven partition rows (F8's open item, records/F8.md "Docs still to
  write"). `xbind init` writes it into new workspaces only — boot's
  backfill never overwrites an existing `AGENTS.md` — so existing
  workspaces learn it from the docs and the changelog, as for every
  cheat-sheet addition.
- **The guards this pack adds** — `TestPartitionsRealtimeQuotes`
  (internal/docscheck/realtime_test.go) fails when the page's Go quotes and
  sdk/example_realtime_test.go part; hack/partitions-realtime.test.mjs
  when the page's three JavaScript blocks stop running as documented (it
  expects exactly three, defining `board`; `follow`, `post`; `mine`, and
  that the first opens the event socket before it reads the board);
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
5. **workspace-template/AGENTS.md** (the review's last finding): the Go
   cheat sheet gains the partition calls — `RequirePartition`,
   `Partition`, `PartitionUser`, `GlobalURL`, `MailContext`,
   `InboxPageContext`, `AckContext` — marked in development, pointing at
   /docs/partitions.md. New workspaces only (never overwritten).
6. **The example's hub grew** (the review's second finding): a `remove`
   route (`DELETE /rooms/{room}/members/{user}`), `drop`, `stillReaders`
   and `accessFresh` (a package var the test zeroes so every post asks
   xbind). The page quotes `post` and `drop` and names `stillReaders`.

## Review fixes (pt/f8f-fix)

The reviewer's nine findings, each fixed — commits `eae64acf` (1),
`cf72972a` (2, 4), `0ac438a3` (6, 7), `fa7562d9` (3, 8), `e9b0ac30` (5),
`d8d908b3` (9), and this record:

1. *(medium)* **A send that gives up can't be deduped by id** —
   `MailWithContext`'s (and `MailContext`'s) doc, sdk.md's paragraph after
   the Context calls, and partitions.md §Partition mail's "At least once"
   now say: the item may or may not exist, a retry is a new item with a
   new id, so a retrying sender puts its own key in `data` and the
   addressee dedupes by it. The decision and changelog texts above say the
   same. (Checked: the agent template's retrying senders already carry
   such keys — handoff id, outbox `Key`.)
2. *(medium)* **The hub never shrank its membership** — see §Deviations
   6 and the decision's new bullet. The table cell and the comments say
   what is checked: the poster's membership at every post, each follower's
   access (a yes trusted 10 s). `TestRealtimeExample` gains: bob loses the
   tile while following → the next post ends his stream without it and
   still reaches carol, and he can't follow again; carol leaves → her
   stream ends, her post is refused; a dropped member can't take anyone
   out.
3. *(low)* §Partition mail's canonical handler now bounds its reads and
   acks (`context.WithTimeout(r.Context(), time.Minute)`,
   `InboxPageContext`, `AckContext`).
4. *(low)* **Each clause of the hub's gate is tested alone**: bob's own
   call (a member) with one thing changed — another tile's From; the
   global instance's, carol's or no partition; view-as — each 403, plus
   carol (not a member) and the root token. Mutation-checked in a scratch
   copy: dropping the From, the Partition or the ViewedBy clause, the
   access re-check, `drop`'s close, the post's drop of the gone, `remove`,
   or the poster's membership check each fails the test (and no case can
   hang it: every in-process call runs under a 2 s deadline, so a follow
   wrongly let in answers 200 instead of blocking).
5. *(low)* **The hung-xbind tests fail by name**: sdk's `within` and the
   agent's existing `within` run the call in a goroutine and fail after
   5 s instead of blocking the binary until go test's timeout.
6. *(low)* Pattern 1 says a row's key proves no author, and routes
   author-bearing state through global.
7. *(low)* Pattern 1's page subscribes first, keeps events that arrive
   while the board is read and applies them on top; a bullet says the
   bus is at most once and a page that mustn't show a stale line re-reads
   the board now and then. The node test holds the board's answer, fires
   an event meanwhile, and checks the order (socket, then fetch) and the
   merge.
8. *(low)* The node mail helper parses inside a try; the snippet test
   gains a cut-off 200 (and was checked to fail on the old helper).
9. *(low)* workspace AGENTS.md's cheat sheet and agent API.md's two names
   (§Seams, §Deviations 5).

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

Re-run on `pt/f8f-fix` after the review's fixes:

| Run | Result |
|---|---|
| sdk: `go vet ./...`; `go test -race -count=1 ./...`; `TestMail*` + `TestRealtimeExample` `-race -count=5` | PASS |
| sdk: `TestRealtimeExample` now also — each gate clause refused alone (6 of bob's calls with one header changed, carol, the root token); bob losing the tile mid-follow (his stream ends without the post, carol still gets it, he can't follow again); carol leaving (her stream ends, her post refused); a dropped member can't take anyone out | PASS |
| mutation check of the example in a scratch copy (8 mutations: the From, Partition and ViewedBy clauses, the access re-check, `drop`'s close, the post's drop of the gone, `remove`, the poster's membership check) | each fails `TestRealtimeExample` (none a build failure, none a hang) |
| mutation check of `mailCall` ignoring ctx | `TestMailContextHungXbind` fails in 45 s naming all 9 calls, instead of hanging to go test's timeout |
| `go test ./internal/docscheck` (the page's new `post` and `drop` quotes) | PASS |
| node: `hack/partitions-realtime.test.mjs` (4, the tile-wide case rewritten), `hack/sdk-mail-snippets.test.mjs` (2; the node case's cut-off 200 checked to fail on the old helper) | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| `make fmt-check vet js-check js-test` (511 JS tests, 510 pass, 1 skipped as before) | PASS |
| `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` (legacy golden + every template test, incl. the reworked `TestSDKMailGivesUpAtTheDeadline`) | PASS (237 s) |
| smokes on a real `xbind --isolate` (.dev.mk's env, the Bash sandbox off): `TestPartitionsSmokeMail` (5 subtests), `TestPartitionsAgent` (8, incl. `mailbox`) | PASS, no SKIP (68.5 s) |

No UI changed: no ui-harness pass. The agent template's browser tests
weren't run (no frontend file changed; API.md is docs).

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
2. **Should xbind end a person's open streams when they lose a tile?**
   xbind checks access when a call arrives; a frame token stops working
   when its login ends (D93), but a proxied response already streaming —
   a page following a room or a shared conversation at global — runs on
   after the person is removed from the tile, disabled or deleted. Today
   every tile that streams member-scoped state must re-check access
   itself (the example now does, with `xbin.AccessOf`). *Recommendation:
   yes, as a later platform change* — cancel the in-flight proxied
   requests a person's credentials opened to a tile when their access to
   it ends (grant removed, level lowered to none, disabled, deleted), so
   tiles can't forget; keep the docs' per-tile re-check as the pattern
   until it ships, since it also covers membership rules xbind can't see.
