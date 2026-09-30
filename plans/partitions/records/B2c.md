# B2c — records for the integrator

Work pack B2c (agent: channels, triggers, schedules via mail — 08 §5, §7)
of the partitioned-tiles plan, branch `pt/b2c` on `partitions` at
d6499604. Implements **PD-37** (channels), **PD-38** (triggers and
schedules) and the agent's part of **PD-46** (per-person usage totals for
managers; the registry's metadata), and wires W3b's open B2c seams: the
mail handlers `handoff/dm`, `handoff/event` (a person's side),
`outbox/add` (global's side), `LedgerTrigger` through mail's `source`, and
B2a's interim 409s for `POST /channels/{id}/claim` and `POST /triggers`.
This pack edits neither docs/changelog.md nor plans/DECISIONS.md; the texts
below go there at merge.

Commits: `406c4a44` (the backend, its unit tests, the template's API.md),
`3174021c` (the e2e on a real isolated xbind), `cb93853d` (the
`channelsPartitioned` ui-harness pass), `7984b35b` (managers' oversight
from their own partition, the retry guard, docs/partitions.md),
`a5ab061d` (a partition with replies to mail asks to be started again),
then this record. §Tests lists what ran on the final tree.

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
    partition: the conversation is yours, private, in your list; the global
    instance keeps only where it came from, holds the message only until it
    is mailed, posts your partition's replies where its own record says —
    only when xbind stamps the reply as yours — and forgets a reply's text
    once the bridge acknowledged it. Files up to 640 KiB a message travel
    with it (a larger one is named in the text). Until your partition has
    run once (open the agent once) your DMs wait for it;
  - **your event triggers are yours**: made in your partition, they register
    their name, source and topic prefix with the global instance (names are
    unique tile-wide; managers see that one exists, whose and what it
    listens to — never what it does —, on their own page too, and may switch
    it off or delete it). A
    trigger on pushes needs a topic prefix, and one that overlaps anyone
    else's on the same tile is refused, so nobody can quietly take everyone's
    webhooks. A matching push is recorded at the global instance (the same
    event never runs twice) and handed to your partition, which runs it; the
    push's source counts in your egress ledger (`/xbin/partitions`);
  - claiming and managing a channel from your own partition is forwarded to
    the global instance (the 409s of the previous version are gone), and
    your Automations page lists its channels;
  - each partition mails the global instance its daily usage totals
    (conversations, model calls and tokens per day — never content), and
    managers read them per person with `GET /usage`.
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
      the commit by one sender pass (`kickHandoffs`: at a commit that queued
      one, at start; a failure xbind may retry — 409, 507, 5xx, network —
      backs off 1 s → 5 min while any wait; a refusal — 400, 403, 404, 413 —
      is final and tells the chat). The payload is cleared once mailed;
      records live 30 days. `/help` and `/link` are answered at global (the
      chat account's); the other commands go with the handoff.
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
      `user:<the handoff's person>` and the handoff is a DM's; the
      destination (channel, address, session) is global's own record; the
      row's `origin` (`user:<id>/<key>`, unique) makes a retry post once;
      files are staged as channel files (`staged:<id>` on the row, served
      by `/adapter/files/{oid}/{i}`). At the adapter's ack — delivered or
      failed — the row keeps only its dedupe key: body and address `{}`,
      staged files gone; a failed one can't be retried from the channel
      owner's view (their content is gone).
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
      included) is 409. A push matching a registry row is recorded at
      global (`trigger_events`: dedupe; the switch, the halt, the hourly
      cap) and handed over (`handoff/event`, mailed with `source` = the
      pushing tile → `LedgerTrigger`); the partition fires its own trigger
      by name with the event (its dedupe, class and data rules). A manager
      at global may only switch a registry row or delete it (any other edit
      409); global never subscribes a hosted bus row (the partition
      subscribes its own). **Oversight from a manager's own partition:** a
      partition numbers its triggers from 2^40 (like its runs), so its
      Automations list the global instance's rows of other people
      (`access: "oversee"`, a 5 s F5 read) beside its own without an id
      clash, and `PUT`/`DELETE /triggers/{id}` below 2^40 there is forwarded
      to global.
    - **Usage totals** (`_backend/usage.go`): a partition mails `usage/day`
      {days: [{day, runs, llmCalls, promptTokens, completionTokens}]} for
      the UTC days after the last one sent up to yesterday (≤ 31), at start
      and at each UTC midnight while it runs; global keeps them per person
      for 90 days (`usage_days`); `GET /usage?days=` for managers
      (forwarded from a partition; 404 unpartitioned).
    - **From a person's partition** the channels' routes (claim, edit,
      peers, pairing, sessions, outbox, retry), `GET /triggers/unmatched`
      and `GET /usage` are forwarded to global (`userGlobal`); its
      Automations list the global instance's channels (a 5 s F5 read of
      `GET /automations`). A `team` trigger answers 409 in a partition
      (`sharesInPartition`).
    - **Unpartitioned nothing changes:** the tables and columns are made
      only in a partitioned instance (`addHandoffSchema`, from `startMode`);
      every hook is a no-op outside its mode; the new routes answer 404.
  - **Not chosen:** mailing inside the message's transaction (the write
    lock held for a network call; the SDK client has no deadline);
    sending the channel's whole policy to the partition (only what the run
    needs); a separate registry table (names must stay unique with team
    triggers); letting a person's partition subscribe for pushes (the
    webhooks tile isn't partitioned: it reaches global only); logging each
    handoff at global (an activity timeline managers would read, PD-46);
    staging large files for an F5 fetch (see Deviations); a manager route
    listing handoffs (the same).
```

## Seams for the integrator

| Seam | Where | State / who |
|---|---|---|
| `userNoChannels`, `noChannelsWords` | `_backend/partition_routes.go` | now unused (no route maps to them): delete at the merge, or leave — B2b edits the lines beside them |
| `userRoutes` additions | `_backend/handoff_user.go` `channelUserRoutes` (merged into `userRoutes` in `init`) | kept out of `partition_routes.go` so its map isn't realigned; B2b's own entries merge cleanly |
| `sharesInPartition` | `_backend/triggers_admin.go` (a `team` trigger in a partition → 409) | **B2b**: if team triggers from a partition go to global (shared automations), replace these two calls |
| the first-DM notice (09 §3: "open <agent> once to receive your messages privately") | global's `handDM` | **xbind (F6 follow-up)**: mail doesn't tell the sender the addressee's partition never ran; the DM waits in the inbox until it runs. A `waiting` flag on `POST /partitions/mail`'s answer would let global post the notice |
| large files through a handoff (08 §5: staged in global's `files`, fetched through F5) | `handoffMail` / `inlineReplyFiles` | not built: over 640 KiB a message, a file is named in the DM's text (inbound) or left out of the reply and logged (outbound) |
| the bridge's `AGENTS.md` note, webhooks' `API.md` note, `docs/agent-inbox.md` | — | **B3** (09 §3, §4): "you always talk to its global instance"; a push is answered once stored or mailed (202), not when the partition ran it; the private-trigger rules |
| B2d's hosted conversations | `handoff.go` | the same sender/queue shape (`queueHandoff`, `kickHandoffs`, `sendMail`) fits `hosted/input`, `hosted/changed` |
| usage in the admin/person UI | `GET /usage` | no UI (API only): F12 (admin Partitions) or a settings panel could show it |
| `triggers.host`, `outbox.origin` | columns ALTERed in only in partitioned instances | code that reads them runs only there (`triggerHost`, `hostAtGlobal`, `purgeHandedReply`); anything new must keep that rule |

## Deviations

- **The linked-DM hook is in `channelMessage`, after admission and before
  commands** (08 names channels.go:480-490, the re-stamp in
  `channelDeliver`): the session commands (`/new`, `/reset`, `/status`,
  `/stop`, `/approve`, `/deny`) act on the person's partition's session, so
  they go with the handoff; `/help` and `/link` are the chat account's and
  are answered at global.
- **Content passes through global's db, briefly.** 08 §5's `handoffs` holds
  "routing metadata, no content"; here the queued item's payload sits in
  the row until it is mailed (then cleared), so a crash never loses a DM and
  nothing is mailed while the db is held; a reply's text sits in global's
  outbox until the adapter acknowledges it (then cleared). API.md says so.
- **Files inline only** (≤ 640 KiB a message, both ways) — see Seams.
- **The overlap rule counts every other owner's push trigger on the source,
  team ones included**: a manager's catch-all (`match: ""`) team trigger on
  a tile makes private triggers on that tile impossible (fail closed).
- **Private schedules are unchanged**: they already run in the person's
  partition on its own cron jobs (B2a); nothing about them goes by mail.
  `schedule.go` is untouched.
- **The first-DM notice isn't sent** (see Seams); instead a mail xbind
  refuses for good (the person can't use the agent any more) tells the chat.
- **A reply's platform failure isn't reported back** to the person's
  partition: its row there is `delivered` once xbind took the mail; the
  channel owner's view at global shows the failure without its content.
- **Typing status** for a handed DM: `working` when global hands it off,
  `idle` when a reply (not a question or approval) is posted.
- **Managers see usage totals and the registry rows**, not handoffs: no
  route lists handoffs (per person they would be an activity timeline).
- **Usage totals are self-reported**: `usage/day` comes from the person's
  partition, whose principals include the person's own frames, so a person
  can misstate their own totals (never anyone else's: xbind stamps
  `from`). They are a budget signal, not an audit.
- **A person's partition may register a registry row without a trigger**
  (its frame calling global with `{partition: 'global'}`): harmless — the
  row is theirs, and an event for a name their partition doesn't have is
  dropped there (logged).
- **The e2e reads alice's handoff ids through a builder topic compiled into
  the instance** (`e2e/handoffs` → an `e2e/ids` item in her own inbox), the
  way the W3b mailbox case compiles its topics in.
- **shots.js**: two `require` lines joined into one to keep the file at its
  877-line budget with the pass's own `PASSES.channelsPartitioned = …` line.

## Merge risks

- `_backend/partition_routes.go` (B2b): only the two map lines for claim
  and `POST /triggers` changed (values; keys and alignment kept).
  `partition_routes_test.go`: two lines removed from the 409 list in
  `TestUserModeRoutes` (the claim and trigger cases), which B2b's sharing
  edits sit beside.
- `_backend/triggers.go`: `fireTrigger` became a wrapper of `fireTriggerIn`
  (joins a caller's transaction; the poke and the automation event moved to
  `AfterCommit` — same effect unnested).
- `_backend/routes.go`: three rows after the triggers block;
  `_backend/main.go`: one line after `pullMailAtStart`;
  `_backend/partition_start.go`: three lines after the `partitioned()` check;
  `_backend/resume_mode.go`: `userWake`'s runnable condition gains
  `repliesWait()`; `_backend/triggers_admin.go`: one line each in
  `triggerItems`, `triggerFor`, `deliverOK`, and the registration and
  `sharesInPartition` calls in the create/update/delete handlers;
  `_backend/channels_admin.go`: `channelItems`' first line, the retry's
  `body<>'{}'`; `_backend/channels.go`, `outbox.go`, `channel_files.go`:
  one hook each.
- `hack/ui-harness/shots.js`: the joined require line (19) and one line
  after `PASSES.partitionMark`.
- `docs/partitions.md`: one new paragraph after §The mode's agent paragraph.
- API.md: the "Channels and event triggers" bullet replaced by four; the
  "Partition mail" bullet's "No topic has a handler yet" sentence.

## Tests

On the final tree (the Bash sandbox off for the isolated runs; .dev.mk's
env exported; no SKIP but the one noted):

| Run | Result |
|---|---|
| the legacy golden: `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent` — every existing template test, plus the new ones | PASS (225 s) |
| the new template tests, `-race -count=2` | PASS |
| repo guards: `go test . ./internal/assetscan ./internal/sizebudget ./internal/docscheck ./internal/builtins ./internal/apicheck` | PASS |
| `make fmt-check vet js-check js-test` (453 JS tests, 1 skipped as before) | PASS |
| e2e `-run '^(TestPartitionsAgentChannels\|TestPartitionsAgent\|TestPartitionsSmoke\|TestPartitionsSmokeW2)$' ./test/isolated/` — Channels (3 cases, 43 s), Agent (6, 62 s), Smoke (19, 45 s), SmokeW2 (73 s) | PASS |
| earlier on the branch: `TestPartitionsSmokeMail` (5 cases, 37 s) | PASS |
| ui-harness, fresh workspace, `HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1` (PORT 8971, HARNESS_DIR …/scratchpad/h4-B2c): `channelsPartitioned` | 15/15 PASS; screenshots looked at: the claim form and the claimed channel in admin's own partition, dev1's DM conversation in dev1's list, dev1's Automations (the global channel, their trigger, 1 run), admin's oversight card "dev1-deploys · dev1's" |
| ui-harness, unisolated (the seeded agent unpartitioned): `channels` (the D84–D87 pass, legacy paths through the changed files) | 23/23 PASS; `channelsPartitioned` SKIP there (not partitioned), as written |

The template's browser tests weren't run: this pack changes no frontend
file of the template.

- Template `_backend` (new, `handoff_test.go`): `TestLinkedDMHandoff`
  (global hands a linked DM with its file — no run, staged file dropped,
  payload cleared; `/help` answered at global; alice's partition refuses a
  DM mailed by bob, runs global's as hers with the file, mails the answer
  with her handoff; a second mail of the same handoff taken once; global
  refuses bob's forged reply, an unknown handoff and one from `global`,
  posts alice's once with the address from its record, serves the reply's
  staged file, forgets it at the ack, won't retry a failed one, posts no
  late retry), `TestHandoffRefused` (a retryable failure stays queued, a
  refusal fails it and tells the chat), `TestTriggerRegistry` (empty match
  400, the same prefix under/over another owner's 409, another's name 409,
  an owner's own overlap and another source fine, a "rename" of someone
  else's row leaves it alone, only a person's partition registers, a push
  handed to alice twice with `source`, the same event again a dup, no run
  at global, a manager may only switch a row, switched off → `disabled`,
  halted → 503, oversight rows without config, delete by its person only,
  404 unpartitioned), `TestPrivateTriggerInPartition` (a refusal at global
  is the answer and nothing is kept, registration body, a duplicate name
  409 without a call, a team trigger 409, a rename with `prev`, a
  config-only edit stays local, an event from bob refused, global's once
  even mailed twice, the delete at global), `TestUsageSummaries` (a day's
  totals mailed once, today not counted; global keeps them; `GET /usage`
  for managers, 403 for a reader, 404 unpartitioned),
  `TestChannelsUnpartitionedUnchanged` (a linked DM is the agent's own
  conversation, no table, no column, no mail), `TestRepliesWaitWakePartition`
  (a reply xbind hasn't taken is work for `userWake`),
  `TestTriggerOversightInPartition` (a manager's partition lists others'
  rows at global and only those, its own ids ≥ 2^40, switching one off is
  forwarded).
- The legacy golden: every existing template test unchanged except
  `TestUserModeRoutes`' two interim-409 cases (lifted by this pack).
- e2e `TestPartitionsAgentChannels` (test/isolated/partitions_agent_channels_test.go):
  the real messaging-bridge template (its console) and webhooks tile
  against a partitioned agent — the channel claimed at global (listed in
  alice's own partition); alice links her chat account from the bridge's
  page; a DM → global → alice's partition (the doorbell starts it) → the
  answer → `outbox/add` → global → the bridge posts it; the conversation is
  alice's (id ≥ 2^40), not in global's list, not readable by bob; bob's
  frame mailing `outbox/add` with alice's handoff id posts nothing, alice's
  own frame's is posted, global's inbox ends acked; alice's private webhook
  trigger (an empty prefix 400; bob's overlapping one and one with her name
  409) runs in her partition from a delivery through the ingress and
  announces into her DM once — the same delivery again runs nothing more —
  and her egress ledger counts `trigger` `apps/webhooks` once.

## Owner questions

- **The overlap rule and team triggers.** 08 §5 says a private push
  trigger is refused when its match overlaps "another owner's on the same
  source". Built literally: team triggers count, so a manager's catch-all
  team trigger (`match: ""`) on the webhooks tile makes private triggers on
  it impossible. Keep that (fail closed), or compare against other people's
  private triggers only?
- **Large files through a handoff.** Files ride inline (≤ 640 KiB a
  message); 08 §5's staging of larger ones in global's `files`, fetched by
  the partition through F5 and deleted on ack, isn't built. Wanted in v1?
- **The first-DM notice** (09 §3) needs xbind to tell global that the
  addressee's partition has never run (e.g. a `waiting` flag on `POST
  /partitions/mail`'s answer) — an F6 follow-up, or drop the notice?
