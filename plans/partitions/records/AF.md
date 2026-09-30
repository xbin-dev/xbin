# AF — records for the integrator

Work pack AF (agent follow-ups from the owner's wave-4 answers and W4's
flags) of the partitioned-tiles plan, branch `pt/af` on `partitions` at
3092f0f2. Implements the owner's rulings **90 §I10** (un-sharing moves the
conversation), **§I11** (big channel files staged) and **§I12** (no
first-DM notice), B2b's sandbox-picker seam and W4's "looks wrong" 3 (a
channel's runs in a person's partition). Agent template only (plus its
docs, the bridge's docs, the e2e and a harness pass); no xbind change, no
new xbind HTTP/WS surface (docs/protocol.md untouched — the new routes are
the tile's own, in its API.md). This pack edits neither docs/changelog.md
nor plans/DECISIONS.md; the texts below go there at merge.

Commits: the move (`6326ea2d`), the sandbox list at the conversation's home
(`9fd611eb`), no first-DM notice (`291eb219`), staged handoff files
(`d63f8981`), a channel's runs from both homes (`8ead3d96`), the e2e
(`de18736c`), docs (`fe21028b`), the move's done route tightened
(`a174c08f`), the harness pass `agentMoves` and this record.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **A partitioned agent: un-sharing moves a conversation home, large chat
  files are staged, no first-DM notice** (the agent template's API.md
  "Partitioned instances", [partitions.md](/docs/partitions.md) §The mode,
  [agent-inbox.md](/docs/agent-inbox.md) §A partitioned agent):
  - **A shared conversation that stops being shared moves to its owner's
    own space.** At the global instance, a person's conversation made
    private with nobody else in it, or whose last member was removed or
    left, moves to its owner's partition: its join links are revoked,
    changes to it answer 409 while it moves (reading goes on), and in about
    a second it is the owner's — a new id from 2^40, its whole transcript,
    task ledger and files — listed in one home at every moment, never two.
    Its page follows it (the `run` event deleting it carries `movedTo`; `GET
    /moves/{id}` answers where it went for 30 days, so an old `#c=` link
    or push opens the new one). A conversation too large to copy (48 MiB),
    or whose owner can no longer be mailed or doesn't open the agent within
    8 days, stays where it is, private. Conversations that aren't a
    person's (the owner token's, another tile's) don't move.
  - **A chat file too large for a handoff's mail (640 KiB) is no longer
    just named in the text**: it waits in the global instance's storage
    and the person's partition fetches it (as them) before it takes the DM,
    then it is deleted there; a reply's file too large for its mail is
    staged at the global instance the same way. Adapters change nothing.
  - **No first-DM notice.** A DM for a person whose partition never ran
    waits in its inbox like an unread message; nothing answers the chat
    for it (its typing status stays `idle`). The notice of the previous
    version is gone.
  - In a person's partition **a channel's card counts, and its run list
    shows, its conversations in both homes** (your DMs, and its group
    threads at the global instance you may see); the Sandboxes list and
    picker **while a shared conversation is open are the global
    instance's** (a shared conversation's sandboxes are its).
  New routes, at a partitioned agent's global instance only: `GET
  /moves/{id}`, `GET /moves/{id}/export`, `POST /moves/{id}/done`, `POST
  /moves/{id}/abandon`, `GET /handoffs/{id}/files/{fid}`, `POST
  /handoffs/{id}/fetched`, `PUT /handoffs/{id}/reply-files`; the mail topic
  `conv/move`. Unpartitioned agents — every existing instance that keeps its
  mode — change nothing. Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, AF: the agent's un-share moves a
  conversation home, staged handoff files, no first-DM notice
  (2026-09-30).** Implements the owner's rulings 90 §I10, §I11 and §I12 of
  plans/partitions/90-decisions.md (08 §3, §5), B2b's sandbox-picker seam
  and W4's flag on a channel's runs in a partition. The template's API.md
  "Partitioned instances"; docs/agent-inbox.md; docs/partitions.md §The
  mode.
  - **Chosen.**
    - **Un-sharing is a state rule** (`moveIfUnshared`, homes_move.go): in
      the transaction of any act that leaves a person-owned conversation at
      global private with no members — `PATCH` visibility, removing a
      member, a member leaving; whoever acts — a `conv_moves` row
      ("asked") and a `conv/move {run}` mail to `user:<owner>` through B2c's
      handoffs queue (per person, retried, refused-for-good and 7-day
      give-ups abandon the move); its join links are revoked; every change
      to it (`refuseWhileMoving`, in `globalRoute`) answers 409 for every
      caller. Not a person's (the owner token's, an element's, unowned):
      it stays.
    - **The partition drives the move** (homes_move_user.go): the mail is
      recorded (`moves_in`) in the mailbox transaction; off the mailbox, one
      move at a time with backoff: delete an earlier attempt's hidden copy
      → `GET /moves/{id}/export` (as the owner; 409 while a run of it is
      running or queued, so no turn's output is left behind; its files up
      to the bundle's 16 MiB inline, the rest one by one from `GET
      /runs/{id}/raw`, linked to the messages that carried them) → import
      hidden (origin `held`, session `move:<id>`: listed nowhere) →
      "arrived" → `POST /moves/{id}/done {to}` (global deletes it, its run
      event carrying `movedTo`, the row becomes a 30-day tombstone) → flip
      the copy visible, "done". Each step idempotent: done again with the
      same `to` answers the same; a move given up meanwhile answers 409 and
      the partition drops its copy; a conversation gone with no move on
      record answers 200 (the copy is the only one). A stopping partition
      with a move under way asks to be started again (`userWake`), and
      every start takes it up. Too large (a 48 MiB bundle): given up at
      global (`POST /moves/{id}/abandon`) — it stays there, private.
    - **One home at every moment**: global lists it until `done`
      committed, the partition from its flip; the page's merged list never
      shows both. Between the two commits (in-process: milliseconds; across
      a partition crash: until its next start, which takes the move up at
      once) it is listed nowhere, but the tombstone and the event already
      name the hidden copy, which opens by id.
    - **The page follows it** (model/moves.js): the session's `gone`
      carries `movedTo` and the app opens the new id; an address whose view
      404s asks global's `GET /moves/{id}` first. Only a person's partition
      asks; elsewhere nothing changes.
    - **Staged handoff files** (handoff_fetch.go): a DM's staged channel
      file past the mail budget stays at global, held (`handoff_files`,
      kept by the upload prune), named in `handoff/dm`'s `fetch`; the
      partition reads it in `prepareMail` (before the transaction, as the
      inline ones are stored) from `GET /handoffs/{id}/files/{fid}` (the
      handoff's person only, from their own partition), and acknowledges
      after the commit (`POST /handoffs/{id}/fetched`: deleted); a fetch
      that fails leaves the mail for the next pull; a file gone meanwhile
      is said in the DM's text; one nobody fetched goes after 8 days. The
      reply direction mirrors it: `PUT /handoffs/{id}/reply-files?key=`
      (idempotent by the reply's key) before `outbox/add` names it in
      `staged`; global takes only the caller's own staged files for that
      handoff, and deletes them at the adapter's ack like inline ones.
    - **No first-DM notice** (90 §I12): the notice and its once-per-person
      flag are gone; the partition's `partition/hello` stays, for the chat's
      typing status only (`idle` until the partition ran).
    - **A channel's runs from both homes** (automations_global.go): in a
      person's partition an automation the global instance keeps (a
      channel; a trigger registry row below 2^40) counts the global
      instance's runs (from the listing the partition reads anyway) with
      its own, and `GET /automations/{kind}/{aid}/runs` asks global the
      same page below the same cursor and merges by (activity, id) — the
      newest `limit` of the union are exactly the list's next; `POST
      …/read` marks both. Global not answering: the partition's own.
    - **The sandbox list at the conversation's home** (model/
      sandbox-store.js): one list per home; while a shared conversation is
      open `GET /sandboxes` and every call about a sandbox go to the global
      instance; the old-manager banner reads the partition's own list.
  - **Not chosen:** global pushing the transcript to the partition by mail
    (a bundle up to 48 MiB against a 1 MiB item); the partition's copy
    visible before global's delete (a window with two copies); cancelling
    a working conversation at the un-share act (the export waits for it to
    rest instead); refusing the un-share act when the conversation is too
    large to move (a member leaving can't be refused; it stays private at
    global instead); asking xbind whether a person's partition ever ran;
    merging a channel's runs in the page (the backend already merges
    homes' pages for the list's cursor).
```

## Seams for the integrator

| Seam | Where | For |
|---|---|---|
| `moveIfUnshared(t, root)` — called in the transaction of `PATCH /runs/{id}` (visibility) and `DELETE /runs/{id}/members/{user}` | `_backend/homes_move.go`; `conversations.go` (3 lines), `share.go` (the delete wrapped in a transaction) | **B2d**: a hosted (non-secure) conversation lives in `team`, not in global's `db` — un-sharing one is B2d's (the move covers global's `db` only) |
| `refuseWhileMoving(pattern, h)` — `globalRoute`'s default branch | `_backend/homes.go` (one line) | **B2d**: a new case in `globalRoute`'s switch keeps the default |
| the handoffs queue's `move` kind (`handoffMail`'s first line, `failHandoff`'s `moveMailRefused`) | `_backend/handoff_send.go` | **B2d**'s `hosted/*` kinds can join the queue the same way |
| `holdForFetch` / `fetchHeld` / `takeFetched` / `stageReply` (`handoff_files`, the `fetch` and `staged` fields) | `_backend/handoff_fetch.go`, `mail_prepare.go` | **B2d**: a `hosted/*` item with files over the mail budget can stage them the same way |
| `globalItem`, `withGlobalRuns` (`userBoth` in `userRoutes`) | `_backend/automations_global.go`, `partition_routes.go` | anything else a person's partition lists from global with runs in both homes |
| `app.sbx`'s per-home lists (`here()`, `listAt(home)`) | `model/sandbox-store.js` | **B2d**: a hosted conversation's sandboxes (the host's partition's) — `here()` is `homeOf(root)`; a hosted id may need its own rule |
| `movedTo(id)`, the session's `gone(id, movedTo)` | `model/moves.js`, `model/session.js`, `model/app.js` | the native app's saved places follow the same tombstone if it keeps ids (it reuses the model) |
| e2e crash windows | — | **I1**: the unit tests cover every crash window of the move; an isolated case killing a partition between import and done isn't built |
| F8 finalization | docs/partitions.md | the realtime section may cite the move as a "two homes" pattern |

## Deviations

- **The partition drives the move** over its own-global calls (08 §3's
  "shared → private is a copy the partition fetches through F5"), the mail
  only rings it; the owner's ruling's "copy there, then delete at global".
- **"Exactly one home at every observable moment"** holds for the lists as
  "never two"; between global's delete and the partition's flip it is
  listed in neither (milliseconds; after a crash, until the partition's
  next start takes the move up), while the tombstone and the event already
  name the hidden copy, which opens by id. A strict single decider would
  need a round trip to global on every read of a hidden copy.
- **A working conversation isn't cancelled at the un-share**: its move
  waits until no run of it is running or queued; a sleeping run that wakes
  between the export and the delete (milliseconds) loses that turn.
- **The state rule, whoever acts**: a member leaving, a manager or the
  owner token making it private moves the owner's conversation too (the
  ruling names the person un-sharing; "no private conversation stays in the
  shared space" decides it). See Owner questions.
- **Every change to a moving conversation is refused** for every caller
  (the owner token included); reading and marking read go on. Join links
  are revoked at the act.
- **Too large to move** (a 48 MiB bundle, rare: files travel one by one past
  16 MiB): the move is given up and the conversation stays at global,
  private — as before this version.
- **Staged files both ways**: the ruling names the DM's (in); a reply's
  file too large for `outbox/add` was dropped (logged) — it is staged too.
- **The typing status keeps the hello**: `partition/hello` and
  `partition_people.seen` stay for `idle` vs `working`; `noticed` is no
  longer written (the column stays: dropping one is a migration).
- **A trigger registry row below 2^40** in a partition merges its global
  runs too (the same code as a channel's; its oversight counts would
  otherwise disagree with its list).
- **`guard`'s `needUser`** answers the owner token 403 on the new routes
  (its generic "only a person …" words), a person who isn't the move's or
  handoff's 404.
- **`ask.go`**: the stale-draft cleanup matches `session_key LIKE 'held:%'`
  so a hidden moving copy (also origin `held`) is never taken for a draft;
  every draft has that key, so nothing else changes.
- **`importConv`** keeps a stamp's `held` origin (a move's hidden copy);
  every other caller stamps `chat` as before.

## Bugs found

- **Found and fixed before commit**: `POST /moves/{id}/done` answered any
  person whether a conversation id existed at global (409 vs 200); it
  answers 404 unless the caller owns it.
- **Pre-existing, not changed**: the template's `test/terminal.mjs` times
  out as W4 recorded (not re-run here).

## Tests

On the final tree (.dev.mk's env exported for the integration runs, the
Bash sandbox off for isolated runs and the harness):

| Run | Result |
|---|---|
| template `_backend`, new: `TestUnshareMovesAtGlobal` (members/PATCH → asked, the mail, links revoked, 409s for every change and caller, reads go on, only the owner reads/exports/dones, busy → 409, done → deleted + `movedTo` event + tombstone, done again idempotent, another `to` 409, a team conversation made private moves, abandon → writable again, done after abandon 409 (owner) / 404 (bob), gone-no-record 200, the owner token's stays), `TestUnshareMoveMailRefused`, `TestUnshareUnpartitioned` (no tables, no move, /moves 404), `TestMoveIntoPartition` (a bob-sent move refused; mailed twice → once; hidden while global hasn't let go (a gate on done), listed after; transcript, inline file and a file past the cap linked to its message; a crash after an import: the half-made copy deleted, made again; a failing done retried; given up at global → copy dropped; busy → retried; too large → abandoned at global + dropped; asked again after a drop), `TestHandoffLargeFiles` (fetch named, not inlined; the prune keeps it; only alice fetches; a failed fetch leaves the mail; taken whole; acked → deleted; gone → said in the text; TTL; reply files staged once, bob refused, posted and deleted at the ack; a partition's reply stages what doesn't fit), `TestChannelRunsBothHomes`; changed: `TestNoFirstDMNotice` | PASS |
| the legacy golden: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test plus the new ones | PASS (242 s) |
| the new tests with their neighbours (`TestUnshare…`, `TestMoveIntoPartition`, `TestHandoffLargeFiles`, `TestChannelRunsBothHomes`, `TestNoFirstDMNotice`, `TestLinkedDMHandoff`, `TestMailFilesPrepared`, `TestHandoffPerPersonBackoff`) `-race -count=5` | PASS |
| `make fmt-check vet js-check js-test` (509 JS tests, 508 pass, 1 skipped as before) | PASS |
| JS: new `hack/agent-template-moves.test.mjs` (4: `movedTo`; following a moved conversation by its old address and by the deleting event; the sandbox list per home, calls at the home, the banner's own list; unpartitioned asks nothing); `node --test hack/agent-template-*.test.mjs` (118) | PASS |
| template browser tests from a scratch copy: `homes partition sandbox automations channels share sidebar chat home attach native settings layout triggers grant long live-policy frame-policy` | PASS (18) |
| repo guards `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS (after dropping two `plans/` citations from code comments) |
| **smokes** `go test -tags=integration -count=1 -v -run '^(TestPartitionsAgent\|TestPartitionsAgentChannels\|TestPartitionsBridge)$' ./test/isolated/` on a real `xbind --isolate` | PASS (71 s): TestPartitionsAgent 9/9 incl. the new `unshare-moves`; TestPartitionsAgentChannels 4/4 incl. the new `big-file`; TestPartitionsBridge 6/6 |
| **UI harness** (PORT 8971, HARNESS_DIR …/scratchpad/h5-AF, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, fresh workspace; stopped after): new `agentMoves` (9: the admin's page is their partition; a conversation shared with dev1 at global; dev1 leaves; the admin's open page follows it to their own space (#c ≥ 2^40); its transcript there; listed once at its new id, never the old; `#c=<old id>` opens the new; dev1 reads neither; no page errors), `agentHomes` (20), `channelsPartitioned` (15) — no FAIL, no SKIP. Screenshots looked at: `agent-moves-shared` (open at global, "shared with 1 person"), `agent-moves-followed` (the same conversation in the admin's own space: the move's note, "private", one row), `channelsp-dev1-automations` (the channel's card: 1 conversation) | PASS |

## Merge risks

- **Shared agent files, line-local edits**: `conversations.go`
  (`moveIfUnshared` in `handlePatchRun`'s transaction), `share.go`
  (`handleRemoveMember`'s delete in a transaction), `homes.go`
  (`globalRoute`'s default), `homes_bundle.go` (`importConv` keeps `held`),
  `ask.go` (the stale-draft query), `routes.go` (two calls and a comment
  after `homeRoutes`), `partition_start.go` (`addMoveSchema`),
  `resume_mode.go` (`movesWait` in `userWake`), `partition_routes.go`
  (`userBoth` + two `userRoutes` rows at the end + its case),
  `automations.go` (two count lines), `handoff.go` (the `handoff_files`
  table, the `Fetch`/`Staged` fields, `partitionRan`'s call,
  `takeStagedReplies` in `handleOutboxAdd`), `handoff_send.go`
  (`handoffMail`'s move kind and held files, `failHandoff`,
  `stagedHeld`, `expireHandoffs`), `handoff_user.go` (`takeFetched`,
  `replyFiles` replacing `inlineReplyFiles`, `kickMoves` at start),
  `mail_prepare.go` (the fetch step and its ack), `handoff_people.go`
  (rewritten: the notice gone). Frontend: `model/app.js` (import, `select`'s
  catch, `gone`), `model/session.js` (`gone`'s argument),
  `model/actions.js` (the sandbox calls take `home`),
  `model/partition.js` (`appNotices` reads the own list),
  `model/sandbox-store.js` (per-home state).
- **`hack/ui-harness/shots.js`**: `PASSES.agentMoves` on its own line after
  `PASSES.channelsPartitioned` (a union with other packs' lines).
- **`test/isolated/partitions_agent_test.go`**: one `t.Run("unshare-moves",
  …)` line (with a comment line) after `shared-chats`, one header bullet;
  `partitions_agent_channels_test.go`: one `t.Run("big-file", …)` before
  `forged-outbox`, one header bullet.
- **Docs**: the template's API.md (four places in "Partitioned instances",
  five rows after `GET /usage`, two rows of the model table),
  docs/agent-inbox.md (three sentences), docs/partitions.md (two
  sentences), the bridge's AGENTS.md and API.md (one paragraph each).

## Owner questions

- **Who un-shares.** Built as a state rule: whenever a person's shared
  conversation at global ends up private with nobody in it — its owner
  made it private, a manager did, or its last member left — it moves to
  its owner's own space. The alternative is to move it only on its owner's
  own act, leaving a conversation its last member left private at global
  (as before). *Recommendation: keep the state rule* — it is what "no
  private conversation stays in the shared space" says, and the owner
  loses nothing (it opens where it went).
- **Too large to move.** A conversation whose bundle passes 48 MiB (its
  files travel one by one, so only a huge transcript does) stays at global,
  private, when un-shared. Refusing the un-share instead would block a
  member from leaving. *Recommendation: keep; revisit if it is ever seen.*
