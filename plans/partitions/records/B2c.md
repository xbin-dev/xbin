# B2c — records for the integrator

Work pack B2c (agent: channels, triggers, schedules via mail — 08 §5, §7)
of the partitioned-tiles plan, branch `pt/b2c` on `partitions` at
d6499604, and its review fixes on `pt/b2c-fix` (on `pt/b2c`). Implements
**PD-37** (channels), **PD-38** (triggers and schedules) and the agent's
part of **PD-46** (per-person usage totals for managers; the registry's
metadata), and wires W3b's open B2c seams: the mail handlers
`handoff/dm`, `handoff/event` (a person's side), `outbox/add` (global's
side), `LedgerTrigger` through mail's `source`, and B2a's interim 409s for
`POST /channels/{id}/claim` and `POST /triggers`. This pack edits neither
docs/changelog.md nor plans/DECISIONS.md; the texts below go there at
merge.

Commits on `pt/b2c`: `406c4a44` (the backend, its unit tests, the
template's API.md), `3174021c` (the e2e on a real isolated xbind),
`cb93853d` (the `channelsPartitioned` ui-harness pass), `7984b35b`
(managers' oversight from their own partition, the retry guard,
docs/partitions.md), `a5ab061d` (a partition with replies to mail asks to
be started again), `fe806e09` (this record). On `pt/b2c-fix`: the review
fixes (backend and docs; the e2e's new cases; this record) — §Review
fixes. §Tests lists what ran on the final tree.

## Changelog entry (docs/changelog.md, under the merge date)

```markdown
- **A partitioned agent's chat channels and event triggers reach people's
  own partitions** (the agent template's API.md "Partitioned instances",
  [partitions.md](/docs/partitions.md) §The mode). The messaging bridge and
  the webhooks tile aren't partitioned, so they reach a partitioned agent's
  global instance, which keeps the channels, the links of chat accounts to
  people and a registry of triggers — and now hands what is one person's to
  that person's partition by partition mail:
  - **a DM from a chat account linked to you** is answered by your own
    partition: the conversation is yours, private, in your list. The global
    instance keeps a record of where it came from, holds the message only
    until it is mailed (at most 7 days — one person's full inbox holds back
    only their own), posts your partition's replies where its own record
    says — only when xbind stamps the reply as yours and the chat account
    is still linked to you — and forgets a reply's text once the bridge
    acknowledged it (the channel's owner sees a reply wait or fail, never
    what it says). Files up to 640 KiB a message travel with it (a larger
    one is named in the text). Until your partition has run once (open the
    agent once) your DMs wait for it, and the first one is answered with a
    notice saying so;
  - **your event triggers are yours**: made in your partition, they register
    their name, source and topic prefix with the global instance (names are
    unique tile-wide; managers see that one exists, whose and what it
    listens to — never what it does or when it ran —, on their own page
    too, and may switch it off or delete it). A trigger on pushes needs a
    topic prefix, and one that overlaps anyone else's on the same tile is
    refused, so nobody can quietly take everyone's webhooks; a private
    trigger or schedule can't be made at the global instance from your
    partition (409: make it in your own space). A matching push is recorded
    at the global instance (the same event never runs twice) and handed to
    your partition, which runs it; the push's source counts in your egress
    ledger (`/xbin/partitions`);
  - claiming and managing a channel from your own partition is forwarded to
    the global instance (the 409s of the previous version are gone), and
    your Automations page lists its channels;
  - each partition mails the global instance its daily usage totals
    (conversations started, model calls and tokens spent per day — never
    content), and managers read them per person with `GET /usage`.
  Unpartitioned agents — every existing instance that keeps its mode —
  change nothing (three new routes answer 404 there). Nothing to change.
```

## Decision entry (plans/DECISIONS.md, the next free D-number)

```markdown
- **D<next> — Partitioned tiles, B2c: a partitioned agent's channels,
  triggers and usage go by partition mail (2026-09-30).** Implements PD-37,
  PD-38 and the agent's part of PD-46 of plans/partitions/90-decisions.md;
  the design is plans/partitions/08-agent-template.md §5, §7. The template's
  API.md "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **A linked DM is a handoff** (`_backend/handoff.go`): at the global
      instance, `channelMessage` — after admission, before commands — hands a
      DM whose peer is linked (`channel_peers.xbin_user`) to the person's
      partition: a `handoffs` row (id, channel, peer, session key, reply
      address, person, trigger, source; state) and the item's payload (the
      text, the channel's lane/class/deny/system text/reset, the staged
      file ids) are written in the message's transaction and mailed after
      the commit (`handoff_send.go`). **Per person, in order:** a failure
      xbind may retry — 409, 507, 5xx, network, the blob store — holds back
      only that person's handoffs (`next_try`: 1 s doubling to 5 min), so
      one full inbox never stalls anyone else's; a refusal — 400, 403, 404,
      413 — is final and tells the chat; one still queued after 7 days (a
      mail item's default life) is given up the same way. The payload is
      cleared once mailed or given up; the staged files of a queued DM
      outlive the upload prune until then; records live 30 days. `/help`
      and `/link` are answered at global (the chat account's); the other
      commands go with the handoff. A global instance stopping with
      handoffs queued leaves its `resume` job.
    - **The first-DM notice (09 §3)** (`handoff_people.go`): a person's
      partition mails `partition/hello` at its first start (once per db);
      any mail from it says the same. Global keeps two flags per person
      (`partition_people`: ran, told) — no times. A DM for someone whose
      partition hasn't run is still mailed (it waits in their inbox), is
      answered once with "open the agent once…", and the chat isn't shown
      the agent working.
    - **The person's partition** (`_backend/handoff_user.go`) takes
      `handoff/dm` only from `global` (xbind's stamp): the session key is
      global's, the run is the person's (private, origin `channel`), the
      channel's lane/class/deny/system text as global decided them, the
      files session files; its reply address is `{"handoff": <id>}`, so what
      the run answers — a turn's end, a question, a notice, an announcing
      trigger — is a row of the partition's own outbox, mailed to global
      after the commit (`outbox/add` {handoff, key, kind, text, files}) and
      settled `delivered` once xbind took it; while one waits, the
      partition's `userWake` counts it as work (a stopping partition leaves
      its `resume` job). A handoff mailed twice is taken once (its inbox
      `client_id` `ho:<id>`).
    - **Global posts a reply** (`outbox/add`) only when `from` (xbind's) is
      `user:<the handoff's person>`, the handoff is a DM's and at most 30
      days old, and its chat account is still linked to that person (not
      blocked); the destination (channel, address, session) is global's own
      record; the row's `origin` (`user:<id>/<key>`, unique; kept 30 days,
      past the outbox's week) makes a retry post once; files are staged as
      channel files (`staged:<id>` on the row, served by
      `/adapter/files/{oid}/{i}` only for that row's channel). At the
      adapter's ack — delivered or failed — the row keeps only its dedupe
      key: body and address `{}`, staged files gone; a failed one can't be
      retried. The channel owner's outbox view lists such rows without
      text, files or address.
    - **A mail item's files are stored before its transaction**
      (`mail_prepare.go`; the db has one connection): objects named after
      the item (`handed/<item>/<i>`), kept if the handler used them,
      deleted otherwise or when the transaction failed.
    - **The trigger registry is today's `triggers` table at global**, plus a
      `host` column (`user:<id>`) on the registry rows (name, source,
      source_ref, match, owner, enabled, max_per_hour; no goal). A person's
      partition registers (`POST /triggers/registry`, attributed to them;
      upsert keyed by name, `prev` for a rename, their own rows only) before
      it saves the trigger, when name/source/match/switch/cap change, and
      removes the row before a delete (`DELETE /triggers/registry/{name}`);
      a registry that doesn't answer refuses the change (502). Rules: a
      private push trigger needs a `match`; one that is a prefix of — or
      prefixed by — any other owner's on the same source (team ones
      included) is 409. **At global a person's call from their partition
      can't keep a private automation** — `POST /triggers`, `POST
      /schedules`, an edit beyond a switch of a private one: 409 — so the
      rules can't be passed by saving a private push trigger there. A push
      matching a registry row is recorded at global (`trigger_events`: the
      dedupe and the hourly cap, kept a day; the switch, the halt) and
      handed over (`handoff/event`, mailed with `source` = the pushing tile
      → `LedgerTrigger`); the partition fires its own trigger by name with
      the event (its dedupe, class and data rules). Global keeps no last-run
      time of a registry row and answers its events and test with 409. A
      manager at global may only switch a registry row or delete it (any
      other edit 409); global never subscribes a hosted bus row (the
      partition subscribes its own). **Oversight from a manager's own
      partition:** a partition numbers its triggers from 2^40 (like its
      runs), so its Automations list the global instance's rows of other
      people (`access: "oversee"`, from one short-lived read of global's
      `GET /automations` that the channels' items share; a write the
      partition forwards there makes it stale) beside its own without an id
      clash, and `PUT`/`DELETE /triggers/{id}` below 2^40 there is forwarded
      to global.
    - **Usage totals** (`_backend/usage.go`): a partition counts each model
      call on the UTC day it was made (`usage_daily`, beside the run's own
      counters) and its conversations on the day they started, and mails
      `usage/day` {days: [{day, runs, llmCalls, promptTokens,
      completionTokens}]} for the UTC days after the last one sent up to
      yesterday (≤ 31), at start and at each UTC midnight while it runs;
      global keeps them per person for 90 days (`usage_days`); `GET
      /usage?days=` for managers (forwarded from a partition; 404
      unpartitioned).
    - **From a person's partition** the channels' routes (claim, edit,
      peers, pairing, sessions, outbox, retry), `GET /triggers/unmatched`
      and `GET /usage` are forwarded to global (`userGlobal`); its
      Automations list the global instance's channels. A `team` trigger
      answers 409 in a partition (`sharesInPartition`).
    - **Unpartitioned nothing changes:** the tables and columns are made
      only in a partitioned instance (`addHandoffSchema`, from `startMode`);
      every hook is a no-op outside its mode; the new routes answer 404.
  - **Not chosen:** mailing inside the message's transaction (the write
    lock held for a network call; the SDK client has no deadline); one
    backoff for the whole sender (one person's full inbox held everyone's);
    sending the channel's whole policy to the partition (only what the run
    needs); a separate registry table (names must stay unique with team
    triggers); letting a person's partition subscribe for pushes (the
    webhooks tile isn't partitioned: it reaches global only); logging each
    handoff or event at global (an activity timeline managers would read,
    PD-46); asking xbind whether a person's partition ever ran (no such
    read for tile code: the partition says hello instead); staging large
    files for an F5 fetch (see Deviations); a manager route listing
    handoffs (the same).
```

## Review fixes (pt/b2c-fix)

Every finding of the B2c review, and what became of it (tests are the
template `_backend`'s unless said otherwise):

| # | Finding | Resolution |
|---|---|---|
| 1 (high) | a person's partition could `POST /triggers` at global (`{partition:'global'}`) with a private catch-all push trigger — past the registry's rules, quietly taking everyone's pushes; the same for private schedules | **Fixed:** `refusePrivateAtGlobal` (trigger_registry.go), at global for a call `personFromPartition`: `POST /triggers` private → 409 "a private automation is made in your own space…", `PUT /triggers/{id}` beyond an `{enabled}` switch that leaves it private → 409, `POST /schedules` private and `PUT /schedules/{id}` beyond a switch of a private one → 409. Handler-level (it needs the body), so it is independent of B2b's `globalRoute` (see Seams). The tile's own principals keep what they had. `TestPrivateAutomationsAtGlobal` (bob's catch-all, overlapping and unsaid-visibility triggers 409 and none kept, a team one 200, alice's push goes to alice and starts no run at global, a private schedule 409, bob's old private trigger and schedule: edit 409, switch 200, a manager's frame unchanged); e2e: bob's frame with `?xbin-partition=global` gets 409 for a catch-all push trigger and a private schedule. |
| 2 (medium) | the sender stopped the whole pass at one retryable failure: one person's full inbox held everyone's handoffs; queued rows had no age limit; staged files expired after 24 h | **Fixed:** `handoff_send.go` — per-person backoff (`handoffs.next_try`; a person with a held handoff is skipped, their later ones wait behind it, everyone else's go on; the next pass is timed to the earliest `next_try`); a queue TTL of 7 days (a mail item's default life): given up, payload cleared, staged files dropped, the chat told; the upload prune leaves the staged files of queued handoffs and the reply files awaiting their ack (`stagedHeld`); a payload that doesn't read is failed, not retried; queued rows ordered by insertion (`rowid`: two in one second kept their order). `TestHandoffPerPersonBackoff` (carol's 507 → bob's two mailed; carol's two mailed in order once her inbox takes them; a queued DM's staged file survives the upload prune, an unreferenced one doesn't; past 7 days: failed, payload and file gone, chat told). |
| 3 (medium) | `GET /channels/{id}/outbox?state=pending` showed people's pending private replies to the channel owner | **Fixed:** `redactHanded` — at global, rows with `origin<>''` are listed with kind, state, times and error, text "(a reply from a person's own space — not shown here)", no files, address `{}`. `TestLinkedDMHandoff` asserts a manager's pending view has neither "hi alice" nor the reply's file. |
| 4 (medium) | the 09 §3 first-DM notice wasn't built; the chat showed typing forever | **Built:** `handoff_people.go` — a partition mails `partition/hello` at its first start (per db; retried next start); any `outbox/add`/`usage/day` from it counts too; global keeps `partition_people(person, seen, noticed)`. `handDM` for a person not seen: the DM is still queued and mailed, the chat gets the notice once per person, and typing `idle` instead of `working`. `TestFirstDMNotice` (two DMs → one notice; a hello from `global` ignored, dave's recorded; after it no notice; a partition says hello once); e2e: no notice for alice, whose partition ran. |
| 5 (medium) | usage totals counted tokens on the run's creation day only | **Fixed:** `addRunCost` → `addUsageDay` bumps `usage_daily(day, …)` (a person's partition only) for the day a call is made; `usageDays` sums conversations by created day and calls/tokens from `usage_daily`; days older than 40 are pruned after a send. `TestUsageSummaries`: tokens yesterday of a conversation from two days ago are yesterday's; a call today bumps today's row and isn't sent. |
| 6 (medium) | global kept an unbounded per-event log of registry rows, `LastRunAt`/`LastStatus` in oversight, and answered `/events` and `/test` on registry rows | **Fixed:** `handEvent` prunes the row's `trigger_events` older than a day (dedupe + hourly cap) at every event and no longer writes `last_event`; the automation event isn't emitted for handed events; `triggerItems` blanks `LastRunAt`, `LastStatus`, `LastRunID` of hosted rows; `GET /triggers/{id}/events` and `POST /triggers/{id}/test` on a hosted row → 409 (`hostedElsewhere`) for every caller, the tile's system principals included. `TestRegistryActivityAtGlobal`. |
| 7 (medium) | a global instance stopping with queued handoffs left no `resume` job | **Fixed:** `leaveWakeUp`'s global/legacy branch also counts `handoffsWait()` (queued handoffs; false outside global mode, no query). `TestHandoffsWaitWakeGlobal` (through a fake gateway: nothing when idle, `resume` with one queued). |
| 8 (low) | every automations listing in a partition made two synchronous 5 s F5 calls | **Fixed:** `globalAutomations` (handoff_user.go) — one read of global's `GET /automations` shared by the channels' items and a manager's oversight rows, reused for 5 s (a listing, the badge's `summary=1`, `GET /automations/{kind}/{id}`), made stale at once by any write this partition sends global (`callGlobal` → `noteGlobalWrite`), answering the last listing when a read fails (retried after 2 s). `TestGlobalListingShared`. Not skipped for `summary=1`: the badge counts global's unclaimed channels for managers. |
| 9 (low) | `serveStagedFile` looked up `channel_files` by id only, for any row | **Fixed:** only a row with `origin<>''` and `run_id=0`, at global, and `AND channel_id=<the row's>`. `TestHandedReplyLimits` (another channel's file, a run's file named `staged:…` → 404). |
| 10 (low) | reply dedupe lost after 7 days while handoffs took replies for 30; no age check; replies after unlink | **Fixed:** the outbox's week prune leaves `origin` rows at global (`handedKept`), which `handleOutboxAdd` prunes at 30 days; `outbox/add` checks the handoff's `created` against 30 days; the peer must still be linked to the person and not blocked. `TestHandedReplyLimits` (a 30-day-old handoff refused; a retry 8 days later posts nothing; after a relink nothing is posted). |
| 11 (low) | blob uploads inside the mailbox's transaction (one db connection) | **Fixed:** `mail_prepare.go` — `pullMail` calls `prepareMail` before the transaction (handoff/dm in a partition, outbox/add at global): binary files go to `handed/<sha256(item id)[:12]>/<i>`, the handler takes them with `preparedBlob` (falls back to storing itself when there is no prepared step), `settle` deletes what the handler didn't keep or what a failed transaction stored; a store failure is returned by the handler (the item stays). `TestMailFilesPrepared`. mailbox.go: three lines. |
| 12 (low) | API.md gaps (route rows, `handed-off`, the retention wording) | **Fixed:** rows for `POST /triggers/registry`, `DELETE /triggers/registry/{name}`, `GET /usage`; `handed-off` in the events reasons and the 409s on registry rows; the DM bullet says what global holds and for how long (message ≤ 7 days, reply until the ack, record 30 days, the owner's view without content), the first-DM notice and `partition/hello`; the triggers bullet the at-global refusal and the day-long events; usage "on the day spent". |
| 13 (low) | merge seams with B2b (partition_routes.go) and shots.js | **Reduced:** `partition_routes.go` is back to `partitions`' text — the claim → `userGlobal` and `POST /triggers` → `userLocal` entries now live in `channelUserRoutes` (handoff_user.go), which `init` lays over `userRoutes`, so B2b's edits of that map (its `/join`, its values for the two keys) merge without a conflict and B2c's win at run time. shots.js: unchanged here (union of the `PASSES.x = …` lines at the merge). |

## Seams for the integrator

| Seam | Where | State / who |
|---|---|---|
| `userNoChannels`, `noChannelsWords`, partition_routes.go's header ("channels and event triggers … answer 409 here") | `_backend/partition_routes.go` | now unused and stale (B2c's values come from `channelUserRoutes`): delete / reword at the merge, or B2b — it edits that file |
| `userRoutes` additions | `_backend/handoff_user.go` `channelUserRoutes` (laid over `userRoutes` in `init`) | includes `POST /channels/{id}/claim: userGlobal` and `POST /triggers: userLocal`; if B2b maps either key to something else on purpose, the override here wins — take it out of `channelUserRoutes` then |
| `refusePrivateAtGlobal` | `_backend/trigger_registry.go`, called from `handleNewTrigger`, `handleUpdateTrigger`, `handleNewSchedule`, `handleUpdateSchedule` | **B2b**: its `globalRoute` guards `POST /runs` by route; this check needs the body (visibility, a switch vs an edit), so it stays in the handlers. If B2b makes shared (team) automations at global from partitions, the team branch is theirs; the private refusal stays |
| `sharesInPartition` | `_backend/triggers_admin.go` (a `team` trigger in a partition → 409) | **B2b**: if team triggers from a partition go to global (shared automations), replace these two calls |
| `globalAutomations` / `noteGlobalWrite` | `_backend/handoff_user.go`; `callGlobal` (gwcall.go) calls `noteGlobalWrite` after any non-GET | B2b's shared listing may read global's `GET /automations` too — reuse it rather than a second call |
| `prepareMail` / `preparedBlob` | `_backend/mail_prepare.go`; `pullMail` (mailbox.go) | **B2d**: a topic of `hosted/*` that carries files adds its case to `prepareMail` |
| large files through a handoff (08 §5: staged in global's `files`, fetched through F5) | `handoffMail` / `inlineReplyFiles` | not built: over 640 KiB a message, a file is named in the DM's text (inbound) or left out of the reply and logged (outbound) |
| the bridge's `AGENTS.md` note, webhooks' `API.md` note, `docs/agent-inbox.md` | — | **B3** (09 §3, §4): "you always talk to its global instance"; a push is answered once stored or mailed (202), not when the partition ran it; the private-trigger rules; the first-DM notice exists |
| B2d's hosted conversations | `handoff.go`, `handoff_send.go` | the same sender/queue shape (`queueHandoff`, `kickHandoffs`, per-person `next_try`, `sendMail`) fits `hosted/input`, `hosted/changed` |
| usage in the admin/person UI | `GET /usage` | no UI (API only): F12 (admin Partitions) or a settings panel could show it |
| `triggers.host`, `outbox.origin`, `handoffs.next_try`, `usage_daily`, `partition_people` | made only in partitioned instances | code that reads them runs only there (`triggerHost`, `hostAtGlobal`, `purgeHandedReply`, `redactHanded`, `handedKept`, `stagedHeld`, `handoffsWait`, `addUsageDay`); anything new must keep that rule |

## Deviations

- **The linked-DM hook is in `channelMessage`, after admission and before
  commands** (08 names channels.go:480-490, the re-stamp in
  `channelDeliver`): the session commands (`/new`, `/reset`, `/status`,
  `/stop`, `/approve`, `/deny`) act on the person's partition's session, so
  they go with the handoff; `/help` and `/link` are the chat account's and
  are answered at global.
- **Content passes through global's db, briefly and bounded.** 08 §5's
  `handoffs` holds "routing metadata, no content"; here the queued item's
  payload sits in the row until it is mailed or given up (≤ 7 days), so a
  crash never loses a DM and nothing is mailed while the db is held; a
  reply's text sits in global's outbox until the adapter acknowledges it
  (then cleared), readable by nobody but the adapter. API.md says so.
- **Files inline only** (≤ 640 KiB a message, both ways) — see Seams.
- **The overlap rule counts every other owner's push trigger on the source,
  team ones included**: a manager's catch-all (`match: ""`) team trigger on
  a tile makes private triggers on that tile impossible (fail closed).
- **Private schedules run in the person's partition** on its own cron jobs
  (B2a); nothing about them goes by mail. At global a person's call from
  their partition can't make or edit one (the review's finding 1).
- **The first-DM notice is learned from the partition** (`partition/hello`),
  not from xbind (tile code has no read of whether a person's partition
  ran). A person whose partition ran before this version and hasn't started
  since gets the notice once on their first DM after the upgrade — their DM
  is still delivered (mail starts a partition that ran before).
- **A reply's platform failure isn't reported back** to the person's
  partition: its row there is `delivered` once xbind took the mail; the
  channel owner's view at global shows the failure without its content.
- **Typing status** for a handed DM: `working` when global hands it off to
  a partition that has run, `idle` otherwise and when a reply (not a
  question or approval) is posted.
- **Managers see usage totals and the registry rows**, not handoffs or a
  registry row's activity: no route lists handoffs, and global keeps a
  row's events a day and no last-run time.
- **Usage totals are self-reported**: `usage/day` comes from the person's
  partition, whose principals include the person's own frames, so a person
  can misstate their own totals (never anyone else's: xbind stamps
  `from`). They are a budget signal, not an audit. Days before this
  version report conversations but no model calls (the runs' counters
  don't say when they were spent).
- **A person's partition may register a registry row without a trigger**
  (its frame calling global with `{partition: 'global'}`): harmless — the
  row is theirs, held to the same rules, and an event for a name their
  partition doesn't have is dropped there (logged).
- **The automations listing in a partition may be up to 5 s old** for
  changes other people make at global (a write of this partition's own
  refreshes it at once).
- **The e2e reads alice's handoff ids through a builder topic compiled into
  the instance** (`e2e/handoffs` → an `e2e/ids` item in her own inbox), the
  way the W3b mailbox case compiles its topics in.
- **shots.js**: two `require` lines joined into one to keep the file at its
  877-line budget with the pass's own `PASSES.channelsPartitioned = …` line.

## Merge risks

- `_backend/partition_routes.go`: **unchanged** from `partitions` now (the
  fix moved B2c's two entries into `channelUserRoutes`), so B2b's edits
  merge cleanly. `partition_routes_test.go`: two lines removed from the 409
  list in `TestUserModeRoutes` (the claim and trigger cases).
- `_backend/triggers.go`: `fireTrigger` became a wrapper of `fireTriggerIn`
  (joins a caller's transaction; the poke and the automation event moved to
  `AfterCommit` — same effect unnested; the event after the hosted branch).
- `_backend/routes.go`: three rows after the triggers block;
  `_backend/main.go`: one line after `pullMailAtStart`;
  `_backend/partition_start.go`: three lines after the `partitioned()` check;
  `_backend/resume_mode.go`: `userWake`'s runnable condition gains
  `repliesWait()`, `leaveWakeUp`'s global branch `handoffsWait()`;
  `_backend/triggers_admin.go`: one line each in `triggerItems` (twice),
  `triggerFor`, `deliverOK`, the test and events handlers, and the
  registration, `sharesInPartition` and `refusePrivateAtGlobal` calls in
  the create/update/delete handlers; `_backend/schedule.go`: the
  `refusePrivateAtGlobal` calls in the create and update handlers;
  `_backend/channels_admin.go`: `channelItems`' first line, the retry's
  `body<>'{}'`, the outbox view's `redactHanded`; `_backend/channels.go`,
  `outbox.go` (two), `channel_files.go` (three), `db.go`, `gwcall.go`: one
  hook each; `_backend/mailbox.go`: `prepareMail`/`settle` around the
  handler's transaction.
- `hack/ui-harness/shots.js`: the joined require line (19) and one line
  after `PASSES.partitionMark` — a union with pt/b2b, pt/f11-fix,
  pt/f12-fix, pt/f14b-fix at the PASSES lines (the combined file stays
  under its 877-line budget because f12 removes 30 lines).
- `docs/partitions.md`: one paragraph after §The mode's agent paragraph.
- API.md: the "Channels and event triggers" bullet replaced by four; the
  "Partition mail" bullet's topics sentence; four rows in the triggers
  table.

## Tests

On the final tree of `pt/b2c-fix` (the Bash sandbox off for the isolated
runs; .dev.mk's env exported; no SKIP):

| Run | Result |
|---|---|
| the legacy golden: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test, plus the new ones | PASS (229 s) |
| the B2c template tests (`TestLinkedDMHandoff`, `TestHandoffRefused`, `TestTriggerRegistry`, `TestPrivateTriggerInPartition`, `TestUsageSummaries`, `TestChannelsUnpartitionedUnchanged`, `TestRepliesWaitWakePartition`, `TestTriggerOversightInPartition`, `TestUserModeRoutes`, `TestLeaveWakeUpByMode` and the eight new ones), `-race -count=3` | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| `make fmt-check vet js-check js-test` (453 JS tests, 1 skipped as before) | PASS |
| e2e `-run '^TestPartitionsAgentChannels$' ./test/isolated/` (3 cases, the new ones in them) | PASS (45 s) |
| e2e `-run '^(TestPartitionsAgent\|TestPartitionsSmokeMail)$'` (the agent's partitioned modes, its mailbox — now also getting `partition/hello` — and F6's mail smoke) | PASS (59 s, 36 s) |
| ui-harness, fresh workspace, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1` (PORT 8976, HARNESS_DIR …/scratchpad/h4-B2c): `channelsPartitioned` | 15/15 PASS; screenshots looked at: dev1's DM conversation, dev1's Automations (their trigger, last run, 1 run), admin's oversight card "dev1-deploys · dev1's" with no activity; the bridge's transcript has no first-DM notice for dev1 (their partition said hello) and the `working` typing status |
| the merge with `pt/b2b-fix` (`git merge-tree`: only shots.js conflicts; partition_routes.go now merges cleanly): the merged tree's `_backend` vets, and its handoff/trigger/usage/route/homes tests pass | PASS |

New template tests (`_backend/handoff_limits_test.go`):
`TestPrivateAutomationsAtGlobal` (finding 1), `TestHandoffPerPersonBackoff`
(2), `TestFirstDMNotice` (4), `TestHandoffsWaitWakeGlobal` (7),
`TestHandedReplyLimits` (9, 10), `TestRegistryActivityAtGlobal` (6),
`TestGlobalListingShared` (8), `TestMailFilesPrepared` (11); changed:
`TestLinkedDMHandoff` (3: the owner's pending view), `TestUsageSummaries`
(5: calls on the day spent), `TestLinkedDMHandoff`/`TestHandoffRefused`
mark their person's partition as run (else the new first-DM notice is one
more notice).

From `pt/b2c` (unchanged by the fix, not re-run unless listed above): the
template's other tests, `TestPartitionsAgent`, `TestPartitionsSmoke`,
`TestPartitionsSmokeW2`, `TestPartitionsSmokeMail`.

## Owner questions

- **The overlap rule and team triggers.** 08 §5 says a private push
  trigger is refused when its match overlaps "another owner's on the same
  source". Built literally: team triggers count, so a manager's catch-all
  team trigger (`match: ""`) on the webhooks tile makes private triggers on
  it impossible. Keep that (fail closed), or compare against other people's
  private triggers only? The converse is open too: a team push trigger made
  at global (by a manager, or by a person once B2b routes shared
  automations there) isn't held to the rules, so a team catch-all also
  receives pushes meant for someone's private trigger — visibly (its runs
  are the team's), not quietly. Hold team push triggers to the same rules?
- **Large files through a handoff.** Files ride inline (≤ 640 KiB a
  message); 08 §5's staging of larger ones in global's `files`, fetched by
  the partition through F5 and deleted on ack, isn't built. Wanted in v1?
- **The first-DM notice's source.** Built from the partition's own hello
  (see Deviations). A read on xbind's side ("has this person's partition
  run?") for tile code would make it exact for partitions from before this
  version — worth an F6 follow-up, or is the one-time spurious notice fine?
