# AF — records for the integrator

Work pack AF (agent follow-ups from the owner's wave-4 answers and W4's
flags) of the partitioned-tiles plan, branch `pt/af` on `partitions` at
3092f0f2, and its review fixes on `pt/af-fix` (on `pt/af`). Implements the
owner's rulings **90 §I10** (un-sharing moves the conversation), **§I11**
(big channel files staged) and **§I12** (no first-DM notice), B2b's
sandbox-picker seam and W4's "looks wrong" 3 (a channel's runs in a
person's partition). Agent template only (plus its docs, the bridge's
docs, the e2e and a harness pass); no xbind change, no new xbind HTTP/WS
surface (docs/protocol.md untouched — the new routes are the tile's own,
in its API.md). This pack edits neither docs/changelog.md nor
plans/DECISIONS.md; the texts below go there at merge.

Commits on `pt/af`: the move (`6326ea2d`), the sandbox list at the
conversation's home (`9fd611eb`), no first-DM notice (`291eb219`), staged
handoff files (`d63f8981`), a channel's runs from both homes (`8ead3d96`),
the e2e (`de18736c`), docs (`fe21028b`), the move's done route tightened
(`a174c08f`), the harness pass `agentMoves` (`659bbb65`) and this record.
On `pt/af-fix`: the move's review fixes (`36de7dbe`), staged reply files'
share and a held file's life (`96d3dfc9`), only the owner's page follows a
move (`2b5844f8`), the e2e and harness pass tightened, docs, this record.

## For the integrator: the changelog entries this pack changes

- **Amend B2c's unreleased entry in place** (docs/changelog.md, "a DM from
  a chat account linked to you" bullet): replace "Files up to 640 KiB a
  message travel with it (a larger one is named in the text). Until your
  partition has run once (open the agent once) your DMs wait for it, and
  the first one is answered with a notice saying so;" with "Its files
  travel with it — inline up to 640 KiB a message, a larger one staged at
  the global instance until your partition fetched it. Until your
  partition has run once (open the agent once) your DMs wait for it like
  unread messages — up to 7 days; nothing answers the chat meanwhile;".
- **Drop W4's pending B3 correction** (W4-wire.md, "B3's entry": adding
  "and the first is answered with a notice saying so") — ruling 90 §I12
  removed that notice; B3's entry stays as it is.
- Then add AF's own entry below (it describes the feature as it ships —
  the partitions branch was never released, so there is no "previous
  version" to compare with).

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **A partitioned agent: un-sharing moves a chat home, large chat files
  are staged** (the agent template's API.md "Partitioned instances",
  [partitions.md](/docs/partitions.md) §The mode,
  [agent-inbox.md](/docs/agent-inbox.md) §A partitioned agent):
  - **A shared chat that stops being shared moves to its owner's own
    space.** At the global instance, a person's chat that an act leaves
    shared with nobody — made private with nobody else in it, or its last
    member removed or gone — moves to its owner's partition once nothing
    in it is under way: its join links are revoked, changes to it answer
    409 while it moves (reading, stopping it, deciding an approval and
    answering what it asked go on), and then it is the owner's — a new id
    from 2^40, its transcript, task ledger, files and notes, the schedules
    that report into it (now private, the owner's own) and their pin.
    What stays behind — its subagents' own transcripts, its sandboxes,
    the capabilities granted to it — is said in the note it arrives with.
    It is never listed in two homes; its owner's page follows it (the
    `run` event deleting it carries `movedTo`; `GET /moves/{id}` answers
    where it went for 30 days, so an old `#c=` link or push opens the new
    one). An automation's thread at the global instance (a channel's, a
    schedule's, a trigger's) doesn't move, nor does a conversation that
    isn't a person's (the owner token's, another tile's). A chat too large
    to copy (48 MiB), or that its owner's space can't take, or whose owner
    can no longer be mailed or doesn't open the agent within 8 days, stays
    where it is, private.
  - **A chat file too large for a handoff's mail (640 KiB)** waits in the
    global instance's storage and the person's partition fetches it (as
    them) before it takes the DM, then it is deleted there; a reply's file
    too large for its mail is staged at the global instance the same way
    (up to 10 files a chat, 32 files / 64 MiB a person not yet sent).
    Adapters change nothing.
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
- **D<next> — Partitioned tiles, AF: the agent's un-share moves a chat
  home, staged handoff files, no first-DM notice (2026-09-30).**
  Implements the owner's rulings 90 §I10, §I11 and §I12 of
  plans/partitions/90-decisions.md (08 §3, §5), B2b's sandbox-picker seam
  and W4's flag on a channel's runs in a partition. The template's API.md
  "Partitioned instances"; docs/agent-inbox.md; docs/partitions.md §The
  mode.
  - **Chosen.**
    - **Un-sharing is the act's, on a chat** (`moveIfUnshared(root, was)`,
      homes_move.go): in the transaction of `PATCH` visibility or of
      removing a member (a member leaving), whoever acts, when the
      conversation was shared before the act (`sharedAtGlobal`: team, or
      someone in it) and isn't after, and it is a person's chat (origin
      `chat`/`api`/none, something said in it) — a `conv_moves` row
      ("asked", a random key) and a `conv/move {run, key}` mail to
      `user:<owner>` through B2c's handoffs queue (per person, retried,
      refused-for-good and 7-day give-ups abandon the move). An
      automation's thread, a draft, a PATCH that changes nothing about who
      shares it: nothing. Its join links are revoked; every change to it
      answers 409 (`refuseWhileMoving`, in `globalRoute`) except reading,
      marking read, `/cancel`, `/interrupt`, `/approve`, taking back a
      queued message and `/answer` to a run waiting for one; an
      automation's delivery into it is refused (`errConvMoving`,
      `deliverInboundTx`).
    - **The partition drives the move** (homes_move_user.go) with the
      mailed key — xbind stamps a person's page and terminals as it stamps
      their partition's backend, and only the backend takes the mail: the
      mail is recorded (`moves_in`); off the mailbox, one move at a time
      with backoff: delete an earlier attempt's hidden copy → `GET
      /moves/{id}/export?key=` (409 while anything in the tree is under
      way: an `active()` status, input not taken, a sandbox command; the
      bundle with files to 16 MiB inline, the rest one by one from `GET
      /runs/{id}/raw`; its notes, its owner's schedules reporting into it,
      their pin and archive, `behind`, and a ticket — global notes the
      tree's mark) → import hidden (origin `held`, session `move:<id>`),
      notes with it → "arrived" → `POST /moves/{id}/done {to, ticket,
      key}` → the copy shows, its schedules made (private, reporting into
      the new id), the pin set, "done". Global's done: the ticket must be
      the latest export's and the tree at rest and unchanged (its mark),
      else 412 — the partition drops its copy and reads it again; then
      "leaving" (to), the tree deleted with the schedules that went, its
      event carrying `movedTo`, "moved" (a 30-day tombstone). A
      conversation gone while "asked" left another way (B2d's hosting):
      409, no move; the partition drops its copy. Idempotent at every
      step; a stopping partition with a move under way asks to be started
      again (`userWake`). What the partition can't take — too large, a
      bundle it refuses, past its file store's limits, a class the person
      may no longer use — is given up at global (`abandon`); an export's
      404 is checked against `GET /moves/{id}` before the move is dropped.
      Anyone but the owner: 404, and done `{state: "gone"}` for every id.
    - **Never two homes**: global lists it until `done` committed, the
      partition from its flip; between the two it is listed nowhere and
      opens by its new id (the tombstone and the event name it).
    - **Only its owner's page follows it** (model/moves.js `followsMove`):
      a person's partition page, their own conversation; an address whose
      view 404s asks global's `GET /moves/{id}` first.
    - **Staged handoff files** (handoff_fetch.go): a DM's staged channel
      file past the mail budget stays at global, held (`handoff_files`,
      kept by the upload prune, its 8 days from the latest attempt to mail
      it), named in `handoff/dm`'s `fetch`; the partition reads it in
      `prepareMail` from `GET /handoffs/{id}/files/{fid}` (the handoff's
      person only, from their own partition) and acknowledges after the
      commit (`POST /handoffs/{id}/fetched`: deleted); a fetch that fails
      leaves the mail for the next pull; a file gone meanwhile is said in
      the DM's text. The reply direction mirrors it: `PUT
      /handoffs/{id}/reply-files?key=<outbox key>-<row>/<file>`
      (idempotent by key; a DM under 30 days; 10 files a chat — 413, the
      reply goes without it; 32 files / 64 MiB a person not yet sent —
      507, later) before `outbox/add` names it in `staged`; global takes
      only the caller's own staged files for that handoff, and deletes
      them at the adapter's ack like inline ones.
    - **No first-DM notice** (90 §I12): the partition's
      `partition/hello` stays, for the chat's typing status only (`idle`
      until the partition ran).
    - **A channel's runs from both homes** (automations_global.go): in a
      person's partition an automation the global instance keeps (a
      channel; a trigger registry row below 2^40) counts the global
      instance's runs with its own, and `GET
      /automations/{kind}/{aid}/runs` asks global the same page below the
      same cursor and merges by (activity, id); `POST …/read` marks both.
    - **The sandbox list at the conversation's home** (model/
      sandbox-store.js): one list per home; while a shared conversation is
      open `GET /sandboxes` and every call about a sandbox go to the
      global instance; the old-manager banner reads the partition's own.
  - **Not chosen:** global pushing the transcript to the partition by mail
    (a bundle up to 48 MiB against a 1 MiB item); the partition's copy
    visible before global's delete (two homes); cancelling a working
    conversation at the un-share act (the export waits for it to rest);
    refusing the un-share act when the conversation is too large (a member
    leaving can't be refused); moving an automation's thread (its channel
    session and outbox mapping stay at global); carrying subagents'
    transcripts, sandbox bindings or grants (they are the shared space's);
    asking xbind whether a person's partition ever ran; merging a
    channel's runs in the page.
```

## The review's findings, and what became of them

| # | Finding | Fix | Test |
|---|---|---|---|
| 1 (high) | un-sharing moved automations' threads (channel group threads, unlinked DMs, schedule/trigger threads) and fired on no-op PATCHes | `moveIfUnshared(root, was)`: only on the act that took the last sharing away (`sharedAtGlobal` read before the change, in `handlePatchRun` and `handleRemoveMember`), only a person's chat (`isChat` origin, something said in it) | `TestUnshareOnlyChats` (a channel's group thread its last member left, a team channel thread made private, an unlinked DM saved unchanged twice then still takes a message, schedule/trigger/watcher/held threads made private, a chat never said anything in, a private chat saved unchanged — none move; a team chat made private does) |
| 2 (medium) | a conversation mid-work (sleeping, waiting, awaiting…) could be moved and lose that work; done didn't re-check | `moveBusy` (any `active()` status in the tree, undelivered input, a running sandbox job) refuses the export (409) and done; the export notes `moveMark` (runs, messages, inbox, files, notes, schedules) and hands out a ticket; done with an old ticket or a changed/busy tree answers 412, which the partition reads as "read it again" (its copy deleted, `asked`) — not 409. `deliverInboundTx` refuses a moving root (`errConvMoving`) | `TestUnshareMovesAtGlobal` (running/sleeping/waiting/awaiting/queued and a queued inbox row: 409; a wrong ticket 412), `TestMoveWaitsAndRechecks` (a running sandbox job 409; a late message → 412, read again with it; sleeping again → 412; a schedule's delivery refused), `TestMoveIntoPartition` (a 412 → the copy goes, read again, done once) |
| 3 (medium) | the owner couldn't stop, interrupt or answer a moving conversation | `whileMoving`: `/read`, `/cancel`, `/interrupt`, `/approve`, `DELETE /runs/{id}/inbox/{iid}`; `/answer` to a run waiting for one | `TestUnshareMovesAtGlobal` (cancel, interrupt 200), `TestMoveWaitsAndRechecks` (cancel/interrupt/approve not refused by the move; an answer nothing waits for 409) |
| 4 (medium) | notes, schedules, pin lost at done; docs promised "moves" | the export carries `memory`, the owner's `schedules` reporting into it (conversation mode), `pinnedAt`/`archivedAt`, `behind`; the partition stores the notes at import and, at the flip, makes the schedules (private, reporting into the new id; `created_by_run` the new id when it was the root) and sets the pin; global deletes those schedules at done; the note names what stays behind. Anyone else's (legacy team) schedule stays and falls back to a new run per firing, as ever | `TestMoveCarries` (notes, pin, alice's schedule carried and gone from global, the legacy one stays, behind names the subagent's transcript and the capabilities), `TestMoveIntoPartition` (notes, schedule made at the flip — not before —, pin, the note's words) |
| 5 (medium) | B2d's `POST /hosted` and `POST /runs/{id}/copyin` bypass the refusal; done recorded "moved" for a run gone another way | done goes "asked" → "leaving" (to) → delete → "moved"; a run gone while "asked" is 409 and the row deleted (the partition drops its copy); a "leaving" one is finished by the same `to` only. The export of a gone run: 404, row deleted. **At merge**: B2d's `handleHostedMove` and `handleCopyInAt` call `movingRefused(w, id)` (see Seams) | `TestMoveWaitsAndRechecks` (a conversation deleted meanwhile: done 409 and no move; export 404 and no move; a done cut short in "leaving": another `to` 409, the same finishes) |
| 6 (low) | done told non-owners whether an id existed | anyone but the owner: `{state: "gone"}` 200 for every id (no side effect); export/abandon/GET 404 | `TestUnshareMovesAtGlobal` (bob on alice's id and on 99999: GET/export/abandon 404, done "gone"; alice's move unchanged) |
| 7 (low) | permanent import failures retried for 8 days; an export 404 not from the handler dropped the move | `permanentImport` (badRequest, a class error, `errTooLarge`, the file store's limits) → `abandonMove` then drop; an export 404 is checked with `GET /moves/{id}` (a move that stands: later); `moveIfUnshared` skips a chat with nothing said in it | `TestMoveIntoPartition` (a bundle with no messages → abandoned at global and dropped; export 404 while the move stands → later, `GET /moves/12` asked; the move's own 404 → dropped after the check) |
| 8 (low) | staging reply files had no quota | `PUT /handoffs/{id}/reply-files`: reply-key form only (400), a DM under 30 days (410), 10 files a chat (413), 32 files / 64 MiB a person not yet sent (507) | `TestHandoffLargeFiles` (a bad key 400, a third file for one chat 413, past the person's files and bytes 507, an old DM 410) |
| 9 (low) | the changelog's B2c text and W4's B3 correction contradict §I12; AF's text compared with an unreleased version | "For the integrator" above; AF's entry reworded | — |
| 10 (low) | "listed in one home at every moment" overstated | docs say "never listed in two homes" and say where it is between the two commits; Owner questions below | — |
| 11 (low) | weak test checks | the e2e: bob's page at global on `/moves/{old}` (404), `/export` (404), `/done` (`gone`, as for 99999), `/abandon` (404), `/runs/{old}`, `/raw` (404); alice's page on export/done/abandon (403); the tombstone unchanged after. The big-file e2e reads, through a compiled-in probe (`e2e/held`), that global holds none of alice's DM files after the ack. Unit: bob's real staged id named in alice's `outbox/add` is left out; the held file after the ack 404. The harness pass `agentMoves` does the same attempts from the pages | `TestPartitionsAgent/unshare-moves`, `TestPartitionsAgentChannels/big-file`, `agentMoves` |
| 12 (low) | the page followed `movedTo` for any viewer | `followsMove`: a person's partition page, their own conversation; the event still reaches everyone the conversation's ACL admits (at that moment its owner and system-level viewers) — not narrowed: the hub has no owner-only filter, and the new id tells a system viewer nothing it can open | `hack/agent-template-moves.test.mjs` (bob's conversation moved: home; the owner token's page: home) |
| 13 (low) | a held file's TTL ran from the first mailing attempt | `holdForFetch` upserts `created` at every mailing attempt | `TestHandoffLargeFiles` (a file held 8 days less a minute, mailed again: its time starts again) |
| 14 (low) | export/done/abandon open to the owner's page and terminals | the mailed key (`conv/move {run, key}`): export `?key=`, done and abandon `{key}` — 403 without it; `fromPartition` also refuses anything not stamped as a person's partition (the owner token, a tile's admin frame). The ticket ties done to the latest export | `TestUnshareMovesAtGlobal` (alice's admin frame, her partition-stamped call without the key, the owner token: 403 on all three; a wrong key 403), `TestMoveIntoPartition` (the mailed key drives export and done; a move asked anew takes the new key) |

## Seams for the integrator

| Seam | Where | For |
|---|---|---|
| `movingRefused(w, root) bool` — 409 when root is moving out | `_backend/homes_move.go` | **B2d, at merge**: `handleHostedMove` (hosted_global.go, after `runAccess`) and `handleCopyInAt` (copyin.go) are mounted outside `globalRoute`; each calls `if movingRefused(w, rootOf(run)) { return }` so a moving conversation can't be taken into `team` or copied into. Without it done still answers 409 for a conversation hosted away meanwhile (the partition drops its copy) — so I10's "one home" holds, but the owner's hosting would win over the move |
| `moveIfUnshared(t, root, was)` + `sharedAtGlobal(root)` read before the change — in `PATCH /runs/{id}` (visibility) and `DELETE /runs/{id}/members/{user}` | `_backend/homes_move.go`; `conversations.go` (2 lines), `share.go` (the delete wrapped in a transaction) | **B2d**: a hosted (non-secure) conversation lives in `team`, not in global's `db` — un-sharing one is B2d's (the move covers global's `db` only) |
| `refuseWhileMoving(pattern, h)` — `globalRoute`'s default branch; `whileMoving` lists what still goes | `_backend/homes.go` (one line), `homes_move.go` | **B2d**: a new case in `globalRoute`'s switch keeps the default |
| `errConvMoving` from `deliverInboundTx` (mode `run` or a session's current run) | `_backend/sessions.go` (3 lines) | anything delivering into a run at global: a refused delivery is the caller's to log |
| the handoffs queue's `move` kind (`handoffMail`'s first line, `failHandoff`'s `moveMailRefused`) | `_backend/handoff_send.go` | **B2d**'s `hosted/*` kinds can join the queue the same way |
| `holdForFetch` / `fetchHeld` / `takeFetched` / `stageReply` (`handoff_files`, the `fetch` and `staged` fields), the reply-file share vars | `_backend/handoff_fetch.go`, `mail_prepare.go` | **B2d**: a `hosted/*` item with files over the mail budget can stage them the same way |
| `globalItem`, `withGlobalRuns` (`userBoth` in `userRoutes`) | `_backend/automations_global.go`, `partition_routes.go` | anything else a person's partition lists from global with runs in both homes |
| `app.sbx`'s per-home lists (`here()`, `listAt(home)`) | `model/sandbox-store.js` | **B2d**: a hosted conversation's sandboxes (the host's partition's) — `here()` is `homeOf(root)`; a hosted id may need its own rule |
| `movedTo(id)`, `followsMove(id, to, row, me)`, the session's `gone(id, movedTo, row)` | `model/moves.js`, `model/session.js`, `model/app.js` | the native app's saved places follow the same tombstone if it keeps ids (it reuses the model) |
| `pcHeldProbe` (`e2e/held` → `e2e/held-n`) compiled into the channels e2e's agent | `test/isolated/partitions_agent_files_test.go`, one map entry in `partitions_agent_channels_test.go` | **I1**: another e2e reading what global holds for a person |
| e2e crash windows | — | **I1**: the unit tests cover every crash window of the move; an isolated case killing a partition between import and done isn't built |
| F8 finalization | docs/partitions.md | the realtime section may cite the move as a "never two homes" pattern |

## Deviations

- **The partition drives the move** over its own-global calls (08 §3's
  "shared → private is a copy the partition fetches through F5"), the mail
  only rings it (and carries the key); the owner's ruling's "copy there,
  then delete at global".
- **"Exactly one home at every observable moment"** holds for the lists as
  "never two"; between global's delete and the partition's flip it is
  listed in neither (in-process a moment; after a crash, until the
  partition's next start takes the move up), while the tombstone and the
  event already name the hidden copy, which opens by id. Between the
  partition's import and global's done two copies exist, one hidden. See
  Owner questions.
- **A working conversation isn't cancelled at the un-share**: its move
  waits until nothing in it is under way, and done re-checks; a move whose
  conversation never rests within 8 days is given up (it stays private at
  global).
- **The state rule, whoever acts**: a member leaving, a manager or the
  owner token making it private moves the owner's chat too (the ruling
  names the person un-sharing; "no private conversation stays in the
  shared space" decides it). See Owner questions.
- **Only chats move**: an automation's thread at global made private or
  left by its last member stays there, private (its channel session and
  outbox mapping, its schedule's or trigger's session live there).
- **Every other change to a moving conversation is refused** for every
  caller (the owner token included); reading, marking read, stopping it,
  deciding an approval, answering what it asked and taking back a queued
  message go on. Join links are revoked at the act. A schedule firing into
  it meanwhile loses that firing (the schedule itself moves with it).
- **Schedules move with it** — the owner's, in conversation mode,
  reporting into it — as private schedules of the owner's partition
  (partitions keep private automations only); a legacy (ownerless) team
  schedule reporting into it stays at global and falls back to a new run
  per firing, as it does for any conversation deleted.
- **Too large to move** (a 48 MiB bundle, rare: files travel one by one
  past 16 MiB) or not importable in the owner's space: the move is given
  up and the conversation stays at global, private.
- **Staged files both ways**: the ruling names the DM's (in); a reply's
  file too large for `outbox/add` was dropped (logged) — it is staged too,
  within a per-person share.
- **The typing status keeps the hello**: `partition/hello` and
  `partition_people.seen` stay for `idle` vs `working`; `noticed` is no
  longer written (the column stays: dropping one is a migration).
- **A trigger registry row below 2^40** in a partition merges its global
  runs too (the same code as a channel's).
- **`guard`'s `needUser`** answers the owner token 403 on the new routes;
  `fromPartition` answers 403 to anything not stamped as a person's
  partition; the key answers 403 to the owner's page and terminals.
- **`ask.go`**: the stale-draft cleanup matches `session_key LIKE
  'held:%'` so a hidden moving copy (also origin `held`) is never taken
  for a draft.
- **`importConv`** keeps a stamp's `held` origin (a move's hidden copy);
  every other caller stamps `chat` as before.
- **`movedTo` reaches everyone the conversation's ACL admits** when it is
  deleted (its owner; system-level viewers such as the owner token's
  page); only the owner's own page follows it.

## Bugs found

- **Found and fixed before commit** (`pt/af`): `POST /moves/{id}/done`
  answered any person whether a conversation id existed at global.
- **The review's 14 findings**: all fixed on `pt/af-fix` (table above).
- **Pre-existing, not changed**: the template's `test/terminal.mjs` times
  out as W4 recorded (not re-run here).

## Tests

On `pt/af-fix`'s final tree (.dev.mk's env exported for the integration
runs, the Bash sandbox off for isolated runs and the harness):

| Run | Result |
|---|---|
| template `_backend`: `TestUnshareMovesAtGlobal`, `TestUnshareOnlyChats`, `TestMoveWaitsAndRechecks`, `TestMoveCarries`, `TestUnshareMoveMailRefused`, `TestUnshareUnpartitioned`, `TestMoveIntoPartition`, `TestHandoffLargeFiles` (what each covers: the findings table), with `TestChannelRunsBothHomes`, `TestNoFirstDMNotice`, `TestLinkedDMHandoff`, `TestMailFilesPrepared` | PASS |
| the whole template backend (`go test ./backend`, scratch module on go.mod.tile) | PASS |
| the legacy golden: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test plus the new ones | PASS (246 s) |
| `make fmt-check vet js-check js-test` (509 JS tests, 508 pass, 1 skipped as before) | PASS |
| JS: `hack/agent-template-moves.test.mjs` (4; now also: someone else's moved conversation and the owner token's page go home); `node --test hack/agent-template-*.test.mjs` (118) | PASS |
| repo guards `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| **smokes** `go test -tags=integration -count=1 -v -run '^(TestPartitionsAgent\|TestPartitionsAgentChannels\|TestPartitionsBridge)$' ./test/isolated/` on a real `xbind --isolate` | PASS (66 s): TestPartitionsAgent 9/9 incl. `unshare-moves` (bob's page at global: `/moves/{old}` 404, `/export` 404, `/done` `gone` as for 99999, `/abandon` 404, `/runs/{old}` and `/raw` 404; alice's page on export/done/abandon 403; the tombstone unchanged), TestPartitionsAgentChannels 4/4 incl. `big-file` (global holds none of alice's DM files after the ack, read through `pcHeldProbe`), TestPartitionsBridge 6/6 |
| **UI harness** (PORT 8976, HARNESS_DIR …/scratchpad/h5-AF, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`, fresh workspace; stopped after) | PASS: `agentMoves` 17 (the page follows the move; the list shows it once; `#c=<old>` opens the new; dev1's page: GET/export/abandon 404, done `gone` for the old id and 99999; the admin's page: export/done/abandon 403; the record unchanged; no page errors), `agentHomes` 20, `channelsPartitioned` 15 — no FAIL, no SKIP. Screenshots looked at: `agent-moves-followed` (the conversation in the admin's own space: the move's note, `private`, one row), `agent-moves-shared` |

## Merge risks

- **Shared agent files, line-local edits**: `conversations.go`
  (`sharedAtGlobal` before and `moveIfUnshared` in `handlePatchRun`'s
  transaction), `share.go` (`handleRemoveMember`'s delete in a
  transaction), `sessions.go` (`deliverInboundTx`: three lines),
  `homes.go` (`globalRoute`'s default), `homes_bundle.go` (`importConv`
  keeps `held`), `ask.go` (the stale-draft query), `routes.go` (two calls
  and a comment after `homeRoutes`), `partition_start.go`
  (`addMoveSchema`), `resume_mode.go` (`movesWait` in `userWake`),
  `partition_routes.go` (`userBoth` + two `userRoutes` rows at the end +
  its case), `automations.go` (two count lines), `handoff.go` (the
  `handoff_files` table, the `Fetch`/`Staged` fields, `partitionRan`'s
  call, `takeStagedReplies` in `handleOutboxAdd`), `handoff_send.go`
  (`handoffMail`'s move kind and held files, `failHandoff`, `stagedHeld`,
  `expireHandoffs`), `handoff_user.go` (`takeFetched`, `replyFiles`
  replacing `inlineReplyFiles`, `kickMoves` at start), `mail_prepare.go`
  (the fetch step and its ack), `handoff_people.go` (rewritten: the
  notice gone). Frontend: `model/app.js` (import, `select`'s catch,
  `gone`), `model/session.js` (`gone`'s arguments), `model/actions.js`
  (the sandbox calls take `home`), `model/partition.js` (`appNotices`
  reads the own list), `model/sandbox-store.js` (per-home state).
- **B2d**: see Seams — `movingRefused` in `handleHostedMove` and
  `handleCopyInAt`.
- **`hack/ui-harness/shots.js`**: `PASSES.agentMoves` on its own line after
  `PASSES.channelsPartitioned` (a union with other packs' lines).
- **`test/isolated/partitions_agent_test.go`**: one `t.Run("unshare-moves",
  …)` line (with a comment line) after `shared-chats`, one header bullet;
  `partitions_agent_channels_test.go`: one `t.Run("big-file", …)` before
  `forged-outbox`, one header bullet, and `pcHeldProbe` beside
  `pcHandoffProbe` in the setup's `WriteFiles` map.
- **Docs**: the template's API.md (four places in "Partitioned instances",
  five rows after `GET /usage`, two rows of the model table),
  docs/agent-inbox.md (three sentences), docs/partitions.md (two
  sentences), the bridge's AGENTS.md and API.md (one paragraph each).

## Owner questions

- **"Exactly one home at every moment."** Built as "never listed in two
  homes": the conversation is listed at global until its delete commits,
  and in the owner's partition from its flip right after; between the two
  (a moment; after a partition crash, until it starts again) it is listed
  nowhere but opens by its new id, and before the delete a hidden copy
  exists in the partition. A strict single decider — the partition's copy
  asking global on every read until the move is done — costs a round trip
  per read of a hidden copy. *Recommendation: keep "never two", which is
  what a person can observe; the hidden copy is nobody's to open.*
- **Who un-shares.** Built as a state rule on the act: whenever an act
  leaves a person's shared chat at global shared with nobody — its owner
  made it private, a manager did, or its last member left — it moves to
  its owner's own space. The alternative is to move it only on its owner's
  own act, leaving a chat its last member left private at global (as
  before). *Recommendation: keep the state rule* — it is what "no private
  conversation stays in the shared space" says, and the owner loses
  nothing (it opens where it went).
- **Too large to move.** A conversation whose bundle passes 48 MiB (its
  files travel one by one, so only a huge transcript does) stays at global,
  private, when un-shared. Refusing the un-share instead would block a
  member from leaving. *Recommendation: keep; revisit if it is ever seen.*
