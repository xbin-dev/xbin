# agent — API

A durable agentic loop. State lives in this component's sqlite (`db`); the
models are reached through the `llm` interface (below: one or more bound
OpenAI-compatible providers, llm-gw usually). Runs are driven by an in-process engine: one
actor per run with work, fed by a durable inbox, woken by events — never by a
timer that polls (see **The engine**). Design records `agent`, `agent-v2` and
D81 live in the xbin repo.

All endpoints are **admin-only** at the platform level — the tile is self
(always admin of itself) and the owner. There is no public surface. Paths
below are relative to `/api/<this-component>`.

## Who sees what (D83)

Conversations are **per user**. The backend reads the human behind each
request from `X-XBin-User` (the tile's own frame and terminals carry it) and
a run belongs to whoever started it. Access is decided on a run's **root**, so
a subagent is exactly as visible as the conversation it works for.

| Access | May |
|---|---|
| owner | everything, including rename, share and delete |
| participant | read, send messages, steer, approve, stop, upload |
| viewer | read |

- **Private** (the default for a new chat): only its owner and the people it
  was shared with (members, as viewer or participant).
- **Team**: everyone who can open the tile, as `teamRole` (`viewer` or
  `participant`).
- **Unowned** runs (from before D83, the owner token, or a script) are
  team-visible, and the tile's **managers** own them.
- **Managers** are people with write (or terminal) access to the tile. They
  change the tile-wide settings (`/config`, `/models`, shared skills, `PUT
  /halt`) and oversee every automation. They do **not** see other people's
  private conversations.
- **View-as** (an admin viewing the workspace as a user, D64) sees only
  team-visible runs, never the user's private ones.
- **The owner token and the tile itself** see everything. Another component
  calling the API sees only the runs it started.
- A run you may not see answers **404**. One you see but may not change
  answers **403**. While the agent is halted, a non-manager's request for
  work answers **423**; only a manager's lifts the brake.
- `GET /me` → `{kind, user, level, manager, viewedBy, halted, epochMs}` tells
  the tile who it is talking for. `GET /runs` and the stream list only what
  the caller may see. Run rows on the stream carry `access` and `mine`.
  `GET /runs/{id}/view` carries `access` and `acl {owner, visibility,
  teamRole, members}`.

### The conversation list

| Method & path | Body | Purpose |
|---|---|---|
| `GET /conversations?limit=&cursor=&archived=&scope=` | — | your conversations, newest activity first → `{pinned, items, next}`. `pinned` (your pins) comes on the first page only; `next` is the cursor for the page after. `scope=mine` (default: yours, ones you joined, and unowned ones), `shared` (sharing both ways: yours shared with the team or with people, and others' that reach you — team-visible or you were added; each row's `access` tells them apart) or `team` (older: only others' team-shared ones). `archived=1` lists what you archived |
| `GET /conversations?q=` | — | search titles and everything said, in every conversation you may see (archived and automation runs included), up to 50; content hits carry `match {msgId, snippet}` |
| `PATCH /runs/{id}` | `{title?, pinned?, archived?, visibility?, teamRole?, model?}` | pin and archive are yours (any viewer); title and visibility are the owner's. Making an unowned run private claims it. `model` switches the conversation's model from its next turn (anyone who may talk in it; `""` = the agent's default) |
| `POST /runs/{id}/read` | — | mark it read up to now |
| `GET /needs` | — | what waits for you: conversations where the agent (or a subagent) asks a question or wants an approval — or a coding agent waits for you to sign in (`login`, D147 §4.3.9) — and automations you own whose last run failed and you haven't looked at → `{items:[{run, reason: question\|approval\|login\|failed, subRun, harness?}]}`; `harness` is the waiting run's compact summary when a coding agent waits (as `/tree` nodes carry it) |

Items are run summaries plus `access`, `mine`, `members`, `pinnedAt`,
`archivedAt`, `readMs` and `unread` (activity after you last looked) — and
(D147 §4.3.8; list, search, `/needs` and `PATCH /runs/{id}` rows)
`waiting: true` when it or a run below it waits for a person
(`waiting_input`), and `kids: {harness, waiting}` — its coding agents at
work below the root, and its runs below the root that wait — when either
isn't 0 (one query per page). Your
own message marks the conversation read for you. The stream sends `ustate`
(`{id, pinnedAt, archivedAt, readMs}`) to your own streams only, and
`revoked` (`{id}`) when you can no longer see a conversation.

**Needs you on your phone** (D106). The moments `/needs` lists are also pushed to the
xbin app of each person who would see them there (`xbin.NotifyUserWith` →
`POST /api/xbin/notify`; xbind delivers only to people who can read this tile,
sealed to their devices, and only when the workspace has push set up):

| When | Who | Push |
|---|---|---|
| a run (or a subagent) starts waiting on an `ask_user` question | its owner and participant members | kind `question` (the app sees `tile.question`), the question as the body |
| a run (or a subagent) parks a tool call for approval | its owner and participant members | kind `approval`, the tools it wants to run |
| a run asks its owner for a grant (D111) | its owner alone | kind `approval`, what it asks to read |
| a coding agent parks a permission request or a question (D147 §4.3.9) | its owner and participant members | kind `approval` ("‹name› wants to run ‹title› — approve or deny.") or `question` (its message) |
| a coding agent waits for a sign-in | its owner and participant members | kind `login` ("‹name› needs you to sign in to it.") |
| an automation's run (schedule, watcher, channel, trigger) fails | its owner | kind `failed`, the error |

The title is the conversation's; tapping it opens `#c=<run>` (the subagent's
own run for a subagent's approval — where its card is); `collapseId`
`needs:<run>` lets a later push for the same run replace an earlier one.
Recipients come from the run's own access list only — never from a request —
so an admin's view-as (D64) neither triggers nor receives one, and a
team-wide role reaches nobody in particular (those people see it under Needs
you when they look). It is best-effort: after a 3 s grace the run is read
again and nothing is sent if it moved on (someone answered at once); a
question or approval is sent once per 6 h (a new one in the same run is news
again), a run that keeps failing once per 6 h; each person gets at most 10 at
once, refilled one per 6 min, over the whole tile; xbind's own limits apply on
top, and a refusal is logged, never retried. `needs_push.go`; an instance
that wants none sets `agent.needs = nil` in `main.go`.

### Titles

A new conversation is titled with the start of its first message
(`titleSrc: clip`). After its first answer the `memory`-tier model names it
in 3–7 words (`titleSrc: auto`) — once, in the background, only when a model
slot is free and nobody waits for one, and never over a name someone gave it
(a rename or a title from "New chat with options" is `titleSrc: user`).
Automation runs keep their automation's name (`origin`). Feature key
`titles` (on by default).

### Sharing

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs/{id}/members` | — | `{owner, visibility, teamRole, members:[{user, role, addedBy, via, created}]}`; the owner also gets `links:[{id, role, created, expires, maxUses, uses}]` |
| `POST /runs/{id}/members` | `{user, role}` | the owner shares it with a person, by user id (the login name), as `viewer` or `participant` |
| `DELETE /runs/{id}/members/{user}` | — | the owner removes someone; a member removes themselves (leave) |
| `POST /runs/{id}/links` | `{role, expiresIn?, maxUses?}` | the owner makes an invite link → `{id, token, hash:"#join=<token>"}`. The token is shown once and only its sha256 is stored |
| `DELETE /runs/{id}/links/{lid}` | — | revoke it |
| `POST /join` | `{token}` | a person redeems a link and becomes a member (never lowering a role they have); every failure answers 404 |

The tile builds an invite as its own address without the query string plus
`#join=<token>` — anyone who can open the tile and has it joins, whether they
open it or paste it into the search box. A person who loses access to an open
conversation is sent home. Others' messages are labelled with who wrote them;
messages an automation delivered (a schedule firing into a chat, a watcher's
check, "Learn a skill") show as notices, not as a person's message.

When a second person speaks in a conversation, each person's message reaches
the model prefixed `[<user id>]`, and the system prompt says the
conversation is shared. **Stop** returns only your own queued messages.

This is privacy **between people who use the agent**, enforced by the tile's
own code. Anyone who can change or read the tile itself can read every
conversation: write or terminal access, the owner token, the LLM provider and
llm-gw's logs. Give team members `read` on the tile. A **partitioned**
instance (below) keeps people apart structurally instead.

## Partitioned instances

A new instance of this template is **partitioned** (xbin.json's
`"template": {"partition": ["user", "global"]}`, [/docs/partitions.md](/docs/partitions.md)):
xbind runs one backend per person who uses it — their **partition**, with
its own `db`, `files` and `events` — and one **global** instance for
everything that doesn't act for a person. Opt out when you create it ("Keep
each person's data apart" on the template card, `bx template new agent
--no-partition`); without `xbind --isolate` it is unpartitioned anyway. An
instance made before (or opted out) is asked to become partitioned too
when it takes this template's update (`git fetch template && git merge
template/main` brings `"partition": ["user", "global"]` into its
`xbin.json`; the block's `"partitionOnUpdate": true`, under `--isolate`
only): holding data, it pauses until a manager switches it — which deletes
every conversation, all memory and every schedule (`partitionNote`); there
is no migration — or keeps its current mode, after which it runs
unpartitioned, exactly as described in the rest of this page; empty, it
switches at once ([/docs/changes/2026-10-01-agent-instances-partitioned.md](/docs/changes/2026-10-01-agent-instances-partitioned.md)).
The code is the same in all three modes; `xbin.Partition()` picks one at
start:

| `XBIN_PARTITION` | Mode | Serves |
|---|---|---|
| unset | **unpartitioned** | today's agent, unchanged |
| `user:<id>` | a person's **partition** | that person's own conversations, memory, skills and schedules, and their sandboxes — nobody else's frame, terminal or tile reaches it, workspace admins included |
| `global` | the **global instance** | what is no one person's: the tile-wide settings, chat channels and event triggers (adapters and webhooks reach it), other tiles' and the owner token's calls |

What a partitioned instance does differently:

- **Your conversations are yours.** Everything you start is in your
  partition's `db`; a conversation there can't be shared (`POST
  /runs/{id}/members` and `/links` answer 409, so do `POST /ask` with a
  `share`, `PATCH /runs/{id}` with a `visibility` other than `private` or a
  `teamRole`, and a schedule with `visibility: "team"`) — share a copy
  instead (below). Its ids start at 2^40, so an id says where it lives —
  below 2^40 is the global instance's `db` (and every id of an
  unpartitioned instance). Its lifecycle events stay on your partition's
  `events` bus; another tile subscribed to `res:<this tile>/events` hears
  the global instance's, as ever.
- **Shared conversations live at the global instance** — in its `db`, run
  by it (so they reach no one's partition: their tool calls land in other
  tiles' global instances). The page reaches them with `xbin.fetch(…,
  {partition: 'global'})` (`?xbin-partition=global`), attributed to you, and
  the global instance applies the sharing rules above to you exactly as an
  unpartitioned instance does: sharing, members, the team, join links, your
  pins and read state. Everyone in one follows its stream there, so all see
  a run as it streams.
  - `POST /ask` **at the global instance** makes one: a person's must carry
    `share` (409 otherwise — the shared space holds shared conversations;
    `POST /runs` and a new ask's draft, `PUT /ask/upload`, from a person are
    refused there alike: a shared chat with files is made `hold: true` and
    uploaded into). A `POST /ask` with `share` in your partition answers
    409.
  - `POST /join` in your partition is redeemed at the global instance (join
    links are its).
  - `POST /runs/{id}/publish {share, files?, keep?}` in your partition
    **shares a copy** of one of your conversations: its transcript (and,
    with `files`, its session files) goes to the global instance as a new
    conversation of yours shared as `share` says (required) → `{run: the
    copy, deleted, left}`; the original is deleted unless `keep` (`left`:
    files too large to carry — 16 MiB of session files together at most).
    The copy is the conversation the model reads: every message with what
    its tools returned (folded and stubbed ones as they were) and its task
    ledger (the requests `# Your task` pins, compacted ones too).
    Subagents' transcripts, memory, grants and sandboxes stay behind. A
    conversation over 48 MiB as a whole answers 413 — leave its files out.
  - `POST /copy {from, files?}` in your partition makes a **private copy**
    of a shared conversation you can see (`from`: its id) → the new run.
  - **Un-sharing moves it to its owner's own space.** A person's chat at
    the global instance that an act leaves shared with nobody — made
    private (`PATCH /runs/{id} {visibility: "private"}`) with nobody else
    in it, or its last member removed or gone — moves to its owner's
    partition. Only a chat moves (origin `chat`, `api` or none, with
    something said in it): an automation's thread there — a channel's
    group thread or DM, a schedule's, a trigger's — stays, and a `PATCH`
    that changes nothing about who shares it (the same visibility again,
    a `teamRole` on a private one) moves nothing. Its join links are
    revoked and every change to it is refused (409) while it moves —
    reads go on, and so do stopping it (`/cancel`, `/interrupt`),
    deciding an approval it waits for (`/approve`), answering what it
    asked (`/answer`, to a run waiting for one) and taking back a message
    not delivered yet; an automation's delivery into it is refused too.
    Its owner's partition, told by partition mail (`conv/move`), reads it
    once nothing in it is under way — no run of it working, sleeping,
    waiting for an answer, an approval or its subagents, no input waiting
    to be taken, no sandbox command running (`GET /moves/{id}/export`: the
    bundle with its session files, files past the bundle's cap one by one,
    its notes (`memory`), its owner's schedules reporting into it, their
    pin and archive, `behind` — what doesn't travel — and a `ticket`),
    takes it in hidden, has the global instance delete its copy (`POST
    /moves/{id}/done {to, ticket}`: 412 when it changed or works again
    since that export — the partition reads it again), then lists it — a
    new id from 2^40, its whole transcript, task ledger, files and notes,
    its schedules (now the person's own, private, reporting into the new
    id), their pin. What stays behind is said in the note it arrives
    with: its subagents' own transcripts (what they found is in it), its
    sandboxes (the shared space's), the capabilities granted to it. It is
    never listed in two homes: at the global instance until `done`, in
    the partition once it shows there (between the two, for as long as
    the partition takes — after a crash until it starts again — it is
    listed in neither, and opens by its new id). The global instance's
    `run` event deleting it carries `movedTo` (the new id), and `GET
    /moves/{id}` (its owner only) answers `{run, state: "asked" |
    "leaving" | "moved", to}` for 30 days, so its owner's page open on
    it, a push link or a saved place follows it (the page does; any other
    page goes home). Only the owner's partition drives a move — export,
    done and abandon answer 403 to their page or terminals, and anyone
    else's partition learns nothing (404; done: `{state: "gone"}` for
    every id). Every step is idempotent and taken up again after a crash
    or a stop. A conversation too large to copy (a 48 MiB bundle) or that
    the partition can't take (past its file store's limits, a class the
    person may no longer use), or whose owner can no longer be mailed, or
    whose move nobody took within 8 days, stays where it is — private,
    and changeable again (`POST /moves/{id}/abandon` is the partition's
    way to say so). Conversations that aren't a person's (the owner
    token's, an element's) don't move.
  - The copies travel as a bundle: `GET /runs/{id}/export[?files=1]` (a
    viewer; either home) and `POST /import {conversation, share}` (the
    global instance only; a person's must name `share`). An unpartitioned
    instance has none of these four routes. A bundle is its caller's word:
    at `POST /import` a message keeps its writer only when that is the
    caller — anyone else's comes as a copy (`origin: "copy"`, `label`: the
    id it named; the page says "copied · <id>", the model reads it as no
    one's), and a request in the ledger that isn't the caller's becomes a
    `copy` one. `POST /copy` reads the global instance's own export, whose
    writers stand.
  - The page (web and native) lists both homes in **Mine** — your own and
    the shared ones you take part in — and the global instance's in
    **Shared**; `#c=<id>` opens a conversation at its home. The web's Share
    on one of your own is **Share a copy…**; a shared one's share dialog
    offers **Copy to my own space**; **New chat with options** asks who can
    see it (only you, the team, or people you name). While Mine lists
    nothing shared, the page reads the shared space's list again when it
    shows or gains focus, so a conversation shared with you since appears.
    The global instance's own page (the owner token) is today's single list
    of its conversations. While a shared conversation is open the sandbox
    picker and the Sandboxes dialog list the global instance's sandboxes
    (a shared conversation's are its), and every call about one goes there;
    at home and in your own conversations, your partition's.
- **Non-secure conversations: a shared one that uses your private
  resources.** A shared conversation runs at the global instance, which
  reaches no one's partition. A participant may let it use **theirs** —
  their sandboxes, their data in other partitioned tiles, their vault:
  everything their partition reaches (though never a sandbox where a
  coding agent of theirs signed in or worked: §Partitioned instances) —
  which makes it **non-secure** and **hosted** by them:
  - `POST /hosting {conversation, seen}` in your partition (after the
    warning; `seen`, required: the audience you were shown, `{owner,
    visibility, teamRole, members: {user: role}}`) → the global instance
    moves it into `team` — a new id from 2^39, still below 2^40, so pages
    reach it at the global instance; its join links are deleted, its binary
    session files stay behind (named in a note) — and your partition records
    it in its own `hosted` table and drives it with an engine of its own over
    `team` (its own lock `<team>.engine.<partition id>` and epoch). Only what
    that table lists is ever driven: a row in `team` naming you as host makes
    your partition do nothing. `GET /hosting` lists yours; `DELETE
    /hosting/{id}` takes your resources back. An audience wider than `seen`
    is recorded paused at once. The move is two-phase: your partition notes
    it first and asks with a context of its own (closing the page cancels
    nothing), the copy in `team` is `pending` until your partition takes it
    up, and a move asked for before a stop is looked up at your partition's
    next start (`POST /hosted {conversation, lookup: true}` — never made
    then). A copy no partition took up within 10 minutes shows as dropped
    (reason `unclaimed`); one whose original is still at the global instance
    is deleted there instead.
  - The members keep using it at the global instance, with the routes of any
    shared conversation — the view (with `hosted: {host, state, reason,
    resources, pending, pendingKey, dropsAt}`, also on its run summaries in
    the stream and the lists), the stream, messages, answers, stop and
    interrupt, Retry (`resume`), members, pins, read state, files (text; a
    binary one's bytes are the host's), its export (the transcript without
    files: **Copy to my own space**). Their inputs go into `team`; the
    global instance rings the host's partition (partition mail
    `hosted/input`), whose engine takes them up — never the global
    instance's. While it runs, the host's partition posts its run to its
    global instance (`POST /hosted/events`, batched, attributed — only for
    runs of conversations `team` says it hosts), which publishes it on the
    members' streams: everyone sees it stream at once. What is durable (a
    run, a message, a step, the queue, a link) is re-read from `team` by its
    id, never taken from the post; a draft that hears nothing for a minute
    is let go. A batch that doesn't get through (or is still queued when the
    host's partition stops) becomes `hosted/changed` mail: the global
    instance drops its drafts and has the streams re-read it (`reset`).
    Other routes on it answer 409; join links 409. A parked tool call runs
    with the host's resources: only the host approves it, any participant
    may deny it; a grant can't be given in it.
  - **A wider audience pauses it** — a member added, the team let in, a
    viewer made a participant, at the global instance: the host's engine
    stops at its next step (it looks before each model call and each batch
    of tools, not only when rung; a model call in flight is abandoned,
    writing nothing, and made again once confirmed; a tool call in flight
    ends as after a restart), members' messages answer 409, and the host is
    asked (on the page, and by a push to their xbin app): `POST
    /hosting/{id}/confirm {seen: pendingKey}` (required; 409 if it changed
    again) or `/decline`. A further change while paused is what the host is
    asked about (a new `pendingKey`); an audience back within what they
    confirmed makes it active again by itself. Declining, `DELETE
    /hosting/{id}`, 7 days without an answer (a confirmation after that
    answers 409), the host no longer able to talk in it (removed, left,
    made a viewer), or the host's partition refusing mail for good (the
    person deleted, disabled or no longer able to open the agent) end
    hosting; then any participant may `POST /hosted/{id}/continue` at the
    global instance: it becomes a plain shared conversation there again (a
    new id), without the host's resources — one at a time (409 while someone
    else does), and a retry answers the id it got. Deleting it rings the
    host, whose partition stops at once.
  - **Un-sharing ends hosting**: when only its owner is left (its last
    member removed or gone, made private with nobody in it) it leaves `team`
    for the global instance as its owner's plain conversation (the answer's
    `movedTo`, or PATCH's item with `movedFrom`; its `run` event with
    `deleted` names `movedTo`), and its host's partition stops. From there
    a person's chat moves on to its owner's own space like any un-shared
    one (above: `conv/move`, 409 to changes meanwhile); its owner's page
    follows it both steps. A conversation moving out can't be hosted
    (`POST /hosted`) nor copied into (`POST /runs/{id}/copyin`): 409.
  - A hosted run has no schedule, automation-thread or skill tools
    (`schedule`, `unschedule`, `schedules_list`, `schedule_inspect`,
    `threads_list`, `thread_inspect`, `skills_*`): they would keep state
    outside the conversation — in its host's partition, or in `team` where
    every hosted conversation reads it. One called anyway is refused.
  - The page shows a hosted conversation with a ⚠ **not private** chip (its
    header and its row) and opens the warning — who can read it (its
    members, the agent's managers, workspace admins, anyone who can change
    the agent's code) and whose private resources it uses — every time it is
    opened into a page session, with **Start anyway** / **Open without
    sending** and no "don't show again"; the composer stays locked until it
    is started, while it is paused or moving, and once hosting ended. Its
    top bar has no Compact or Learn skill, and neither its bar nor its
    composer the sandbox badge or picker (the sandboxes its run uses are its
    host's). A
    shared conversation's share dialog offers **Use my private resources…**
    (the same warning first). The native view opens the warning as a modal
    sheet the first time it is opened in an app session (the composer's
    **Read the warning…** opens it again), heads its transcript with it, and
    locks its composer the same way (the host's **Confirm**, **Continue
    without …** are the composer's buttons); hosting and adding a copy are
    the web's for now.
  - **Add a copy of my …** (the non-hosting way): `POST /copyin
    {conversation, files: [{run, path}]}` in your partition sends copies of
    session files of your own conversations to a shared one at the global
    instance (`POST /runs/{id}/copyin {files}`, a participant: they land
    under `from-<you>/`, with a note in the conversation); it stays an
    ordinary shared conversation, and the originals stay private. The share
    dialog's **Add a copy of my files…** says who can read the copies. At
    most 20 files, 16 MiB together.
  - An unpartitioned instance has none of this (the routes answer 404).
- **Settings are the tile's.** The config, classes, the halt switch and the
  shared skills live in the global instance's `db`. It mirrors them into
  `conf` (kv, `"shared": "read"`), which every partition reads at each use
  (cached for a few seconds) and can't write. **Everyone who can open the
  agent can read `conf`** (their frame reaches it through xbind's kv API),
  so it holds the config only as a conversation's viewers see it: a
  **static MCP server with `headers`** (they can carry tokens) is left out
  of it altogether — people's conversations don't get that server; the
  global instance still uses it: **it works in shared (global)
  conversations only**. To use it in people's own conversations, bind it as
  a tile (a global bind of the `mcp` slot) or as a personal bind (`bx bind
  --personal`, that person's conversations only). The settings' **MCP**
  list (web and native) shows a partitioned instance's static servers from
  the config and says so beside each one with `headers`; an unpartitioned
  instance's list is unchanged. A manager reading or changing the settings
  from their own partition (`GET`/`PUT /config` — the whole config, headers and all, as
  ever — `PUT /classes`, `PUT /halt`, `PUT /skills` or `DELETE
  /skills/{name}` of a shared skill) is forwarded to the global instance,
  attributed to them — it checks they may, exactly as here — and conf is
  read again. A config or shared skill over 900 KiB is refused (400): it
  couldn't be mirrored. Your own skills stay in your partition; `GET
  /skills` lists them beside the shared ones. In your partition a skill is
  yours or everyone's: a new one saved with `owner` = you stays there,
  saving one of yours with `owner: ""` publishes it (it moves to the global
  instance), and an `owner` naming anyone else is refused (400).
- **The halt switch in a partition.** A halt set anywhere (the owner token
  at the global instance, a manager in their partition) stops every
  partition's runs within a step of when the partition reads it —
  cancelled, as `PUT /halt` cancels them unpartitioned (in the manager's
  own partition at once); a manager's request for work lifts it tile-wide,
  at the global instance, and when that fails answers 503 instead of
  queueing behind the brake. A coding agent's turn looks at each thing it
  does (a message chunk, a tool call…), and the partition looks for it
  every few seconds while one works — so a turn is cancelled at its next
  step after the partition reads the halt, or within a few seconds of it
  when it says nothing (a long command): the coding agent stopped, as `PUT
  /halt` stops one unpartitioned. An idle coding agent is still stopped at
  its idle time under the halt (that moves no work). A partition that
  hasn't read `conf` yet (the global instance never ran, a kv error at
  start) treats the halt as on without cancelling anything: requests for
  work are queued, runs wait — a message to a coding agent waits too, and
  none is started — and it looks at `conf` again (with backoff, only while
  runs wait) until it can — then they go on by themselves. An instance
  whose `uses` lacks `conf` (a customized copy) can't read it at all:
  people's requests for work answer 503 saying so — update it from its
  template.
- **Chat channels are the global instance's** — the messaging bridge
  isn't partitioned, so it reaches the global instance, where the channels,
  their people, links (`POST /adapter/link`) and rules live. From your own
  partition the channels' routes (`POST /channels/{id}/claim` and the other
  `/channels/{id}/…`, `GET /triggers/unmatched`) are forwarded to the global
  instance, attributed to you, and your Automations page lists its channels.
  Group messages, and DMs from chat accounts nobody linked, are the global
  instance's conversations, as ever. In your partition a channel's card
  counts, and its run list shows, both its conversations here (your DMs)
  and those at the global instance you may see (its group threads) — `GET
  /automations/{kind}/{aid}/runs` reads both and merges them by its
  cursor, `POST …/read` marks both; the same for a trigger of the global
  instance's (an id below 2^40).
- **A DM from the chat account you linked is yours.** The global instance
  keeps a record of where it came from — the channel, the chat account, the
  reply address and you — and hands it to your partition by partition mail
  (`handoff/dm`: the message, the rules the channel gives it — lane, class,
  `deny`, its system text — and its files inline, up to 640 KiB a message;
  a larger one waits in the global instance's storage and the mail names
  it in `fetch`: your partition reads it, as you, from `GET
  /handoffs/{id}/files/{fid}` before it takes the DM, and once it has it
  `POST /handoffs/{id}/fetched` deletes it there — if it is gone meanwhile
  the DM says so in its text). Your partition runs the
  conversation (yours, private, in your list) and mails each reply back
  (`outbox/add`, naming the handoff, its files inline — one too large for
  the mail is staged at the global instance first, `PUT
  /handoffs/{id}/reply-files?key=&name=&mime=`, and named in `staged`;
  up to 10 files a chat and 32 files / 64 MiB a person staged and not yet
  sent — past those 413, and 507 until they are sent); the global instance
  posts it where **its own record** says the DM came from, and only when
  xbind stamps the mail as yours and the chat account is still linked to
  you — a reply naming someone else's handoff is refused (logged, never
  posted) — once however often it comes, for up to 30 days after the DM.
  `/help` and `/link` are answered by the global instance, the other chat
  commands by your partition. Until your partition has run once (open the
  agent once) your DMs wait in its inbox, like unread messages — nothing
  answers the chat for them meanwhile, and its typing status says `idle`
  (a partition mails the global instance `partition/hello` when it first
  starts). If xbind refuses the mail for good (you can no longer use the
  agent), or it can't be mailed within 7 days, the chat is told. What the
  global instance holds of it: the message and its files only until they
  are mailed (or given up, at most 7 days; one person's full inbox holds
  back only their own) — a file too large for the mail until your
  partition fetched it (at most 8 days); your
  reply's text and files only until the bridge acknowledged it (the
  channel's owner sees such a reply wait or fail, never what it says); a
  record of the handoff (no content) for 30 days.
- **Your triggers are yours.** `POST /triggers` in your partition keeps the
  trigger — its goal, class, mode, data class, delivery — and its runs
  there, after registering its name, source, topic prefix (`match`), on/off
  and hourly cap at the global instance, as you (`POST /triggers/registry`,
  `DELETE /triggers/registry/{name}`: your own rows only). Names are unique
  tile-wide. A trigger on pushes needs a `match`, and one that is a prefix
  of — or prefixed by — anyone else's on the same tile is refused (409), so
  nobody can quietly take everyone's webhooks. A push it matches reaches the
  global instance (the webhooks tile isn't partitioned), which records it —
  the same event id never runs it twice; its hourly cap and the halt apply
  there too — hands it to your partition (`handoff/event`, naming its
  source, which xbind counts in your egress ledger on `/xbin/partitions`)
  and answers the sender once it is stored, not when it ran; your partition
  runs it as an unpartitioned agent would. A bus trigger subscribes in your
  partition itself. A rename, a switch or a new match updates the registry
  first, and a delete removes it: when the global instance doesn't answer,
  the change is refused (502) so the two halves agree. It may announce into
  your own DM (`deliver`: that session's key). Managers see the registry's
  rows among the Automations (it exists, whose, what it listens to — not
  what it does) — at the global instance, and on their own page in their
  partition, which lists the global instance's rows of other people (a
  partition numbers its own triggers from 2^40, so an id below that is a
  row at the global instance, and `PUT`/`DELETE /triggers/{id}` of one is
  forwarded there) — and may switch one off or delete it; any other edit of
  a row there, and its events or a test, answer 409 there. The global
  instance keeps a row's events only a day (for the dedupe and the hourly
  cap) and no time of its last run. A trigger with `visibility: "team"`
  answers 409 in a partition. A private trigger or schedule is made in
  your partition: from it, the global instance answers `POST /triggers`,
  `POST /schedules` and an edit (beyond switching one on or off) of a
  private one with 409 — so nobody passes the registry's rules there.
- **Usage totals for managers.** Your partition mails the global instance
  its daily totals — conversations started, model calls and tokens spent,
  coding agents' sessions started (`harnessSessions`: each adapter a
  coding agent of yours started — a number, nothing it did), per UTC day (a
  call counts on the day it was made, whenever its conversation started);
  never content or times of day — when it starts and at each UTC midnight
  while it runs. `GET /usage[?days=30]` (managers; forwarded from a
  partition; at most 90 days) → `{days, since, people: [{user, days: [{day,
  runs, llmCalls, promptTokens, completionTokens, harnessSessions}],
  total}]}`; 404 in an unpartitioned agent, whose managers see every
  conversation anyway.
- Schedules work in your partition (its own cron jobs).
- **The global instance** takes a person's calls from their partition — a
  frame's `xbin.fetch(…, {partition: 'global'})`, or the partition's own
  backend — as that person, with their access level (xbind clamps their
  role to `reader`/`writer`; this tile checks the level as for any person).
  Such a call is never the tile itself. `GET /me` adds `partition` (which
  instance answered) on a partitioned instance.
- **`team`** (sqlite, `"shared": true`) is the one database every
  partition shares: only the global instance migrates it (under a
  `<team>.migrate` flock); a partition opens it without migrating, and when
  its schema is behind wakes the global instance (`GET /health`) and waits
  up to 30 s — meanwhile what needs it answers 503 "the shared space is
  being upgraded". It holds the non-secure conversations (the agent's own
  run schema, which the global instance re-applies at every start; a
  partition also waits while a column this version needs is missing).
  Anything in it is readable by every partition's code.
- **Model calls have a tile-wide cap**: `maxActiveRuns` (default 4) lock
  files `llm.slot.<i>` beside `team`'s file — a flock semaphore every
  partition shares; a dead process frees its slot. As within one instance,
  a subagent's call never takes the last slot, so a new chat waits for at
  most one call to finish however wide everyone's fan-outs are. Each
  partition's own gate allows at most `maxActiveRunsPerUser` (default 2,
  never above `maxActiveRuns`) of its calls at once — at the default that
  leaves a person's subagents one call at a time — and follows a change to
  it at its next model call; the global instance keeps `maxActiveRuns`. The locks need one kernel: xbind never
  runs a partitioned tile's backend in a VM (`vm` and `partition` don't
  mix). A `team` directory that can't hold the lock files costs only the
  cap (calls and titles go ahead).
- **Resume.** A partition that stops with work leaves the `resume` job only
  for work that moves without its person (a running or queued run, an
  undelivered input, a subagent's settled result its parent will take up, a
  run sleeping until a sandbox job ends, a message on its way to a coding
  agent), a `wake` job at the minute its earliest timed wait ends (a
  sleeping run's wake, a subagent deadline, a coding agent's idle stop:
  below), and nothing for runs waiting on a person — who opens the tile
  anyway — nor while the halt is on. xbind stops a person's partition with
  its token revoked first, so the partition can't leave them as it exits:
  it registers them while it idles instead — each time nothing holds it up
  any more — and deletes one no longer wanted (a job that fires while it
  still runs is a pass it would make anyway). The global instance follows
  the unpartitioned rule.
- **Sandboxes.** A partition calls its sandbox managers as itself: a
  manager whose `hello.caps` carry `partitions` homes the sandboxes it
  makes there ([/docs/sandbox-manager.md](/docs/sandbox-manager.md)). One
  without it isn't used in a partition at all — its hello is refused with
  refusal `partitions` (409), naming it and how to update it, in the tools,
  the catalog and the Sandboxes dialog; the global instance keeps using it.
  A partition also sees the team's sandboxes (`shared`: those homed at the
  global instance that its person may use there, by §Coding sandboxes'
  rules — listed, and a terminal opens on them, but never changed or
  deleted from the partition); its conversations work only in a sandbox
  the manager says is homed in the partition
  (`owner.partitionId` its id, `owner.via` this tile, not `shared`) —
  checked when it is bound and at every use; any other is refused (403),
  though you can open its terminal (`GET /sandboxes/{ref}/terminal`; a
  coding agent's own terminal and log, `GET /runs/{id}/harness/terminal`
  and `…/log`, are checked as a use: 403, and 409 for a manager without
  `partitions`). In a partition `GET /sandboxes` (and `GET
  /sandboxes/{ref}`) says of each sandbox whether a conversation there may
  work in it — `homed` (true or false) and, when not, `why` (in words) —
  and the coding-agent catalog's `sandboxes` keeps only sandboxes homed
  there. Every sandbox a partitioned instance
  makes — for a conversation or not — carries the label `xbin.agent/home`
  (the partition's id, or `global`), beside `xbin.agent/conversation` when
  it is made for one.
- **Coding agents only in your own conversations.** A coding
  agent signs in inside its sandbox (`$HOME`), so it works only where its
  sign-in stays yours: a conversation of your own partition, in a sandbox
  homed there, or a subagent one of them spawns.
  - **The global instance never starts or drives one**: `POST /ask` and
    `POST /runs` with `harness` answer 409 there, the catalog lists every
    coding agent unavailable (`reason: "shared-space"`), `subagent_spawn`
    has no `harness` (and refuses one), signing one in (`POST
    /runs/{id}/harness/authenticate`, the terminal's `login=1`) answers
    409, and its engine refuses to start one — so neither a shared
    conversation nor a channel's, a trigger's or a schedule's run there
    reaches one.
  - **A non-secure (hosted) conversation has none**: its runs'
    `subagent_spawn` offers none (and refuses one), and a conversation a
    coding agent answers can't be hosted (`POST /hosted`: 409). Nor does it
    work in a sandbox of its host's where a coding agent of theirs signed
    in or worked (its sandbox tools refuse it, saying why): its members
    could have the agent read that sign-in. The agent knows only the
    sign-ins it saw (a coding agent's session, a probe); anything signed in
    by hand in a sandbox's terminal goes with that sandbox.
  - **It doesn't move between homes**: a conversation a coding agent
    answers — or one in which a coding agent it spawned still works (a
    turn, a question, its adapter up) — can't be published (`POST
    /runs/{id}/publish`), exported for a copy (`GET /runs/{id}/export`,
    so `POST /copy`), hosted or moved — 409 `{error, runs?}`, saying why
    (`runs`: the coding agents' runs it waits on — at work, or finished but
    still running until their idle stop, whose time the words give) — and
    un-sharing one at the global instance answers 409 rather than moving it
    (keep it shared, or delete it); a member may still leave it, and it
    then stays at the global instance with its owner. Once the coding agent
    it spawned has stopped (`/cancel` on it, or its idle stop), the
    conversation copies as any other — without its subagents' transcripts,
    as ever.
  - **An idle one doesn't keep your partition running**: only a coding
    agent at work holds it up — a turn, or a sign-in you started through
    the agent while it waits for the coding agent's answer (a device code:
    at most 15 minutes) — never one idle, one waiting for your answer, or
    one waiting for you to sign in. A partition that stops with one idle
    leaves a `wake` job at the minute it would be stopped (its last
    activity plus `harnessIdleMin`; none when that is 0) — also while the
    halt is on — and the partition then started stops it at once.
  - Its sessions count in your usage totals (`harnessSessions`, above).
- **Your own providers.** A model gateway or MCP server you bound into your
  partition yourself (a personal bind: `bx bind --personal`) is offered only
  in conversations you own, and in your model picker — never in anyone
  else's.
- **Partition mail.** xbind rings `POST /mailbox` (`partitionMail`) as
  `xbin/mail` when items wait in the instance's drop box
  ([/docs/partitions.md](/docs/partitions.md) §Partition mail); it and every
  start pull the inbox through the SDK (`xbin.InboxPageContext`, reading on while
  xbind says `more` — a page cut at ~8 MiB is short but not the end), hand
  each item to its topic's handler once (its id is recorded with the
  handler's effect, so a redelivery is only acknowledged) and acknowledge
  each page in one `xbin.AckContext`; a read or an acknowledgement that fails stops
  the pull (the doorbell rings again). An item a handler fails on stays for
  the next pull. An item whose topic this version has no handler for stays
  for 30 minutes after it was sent — a newer version mid-deploy may read it
  — and is then acknowledged unhandled (logged), rather than start the
  partition at every doorbell step until it expires. The agent's own
  topics are `handoff/dm`, `handoff/event`, `conv/move` and `hosted/input`
  (global → a person), `outbox/add`, `usage/day`, `partition/hello` and
  `hosted/changed` (a person → global), above; an item's files are stored
  before its transaction; add yours to
  `mailHandlers` (`_backend/mailbox.go`). The doorbell
  answers `{handled, left, dropped}`. Only xbind's `xbin/mail`, the owner
  token and the tile itself may ring it. `GET /health` → `{ok, mode, team?}` answers whoever may call this
  API — the tile's own frames, terminals and backend, and the owner token
  (the admin role) — and, at the global instance, a person's own partition
  (the wake-up); another tile gets 403.

The web view follows `xbin.partition`: unset, today's; in your partition
("user:…") your conversations with the sharing controls gone, and — when a
bound sandbox manager can't keep people apart — one banner naming it and the
update; at the global instance (the owner token, `--no-auth`) the list of
the global instance's conversations with a note to sign in as a person for
private ones. In a partitioned instance every live stream closes while the
page is hidden and resumes from its cursor when it shows again, so a
background tab doesn't keep a partition running.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, kind, status, timestamps; a quick ask also carries `last`, its latest answer, for the home view's cards). `?roots=1` lists top-level runs only — what the sidebar shows; subagents are reached through their parent |
| `POST /runs` | `{goal, title?, system?, class?, toolset?}` | create a run and start driving it; `class` (or the legacy `toolset`): see **Agent classes** |
| `POST /ask` | `{text, class?, toolset?, model?, hold?, draft?, files?, share?}` | a quick ask: a run titled from `text`, `kind:"quick"`, driven immediately (`hold`, `draft`: see Attachments; `class`: see **Agent classes**; `share` — `{visibility: "team", teamRole}` and/or `{members: [{user, role}]}` — shares it at once, as the sharing routes below would right after; not with `draft`) |
| `PUT /ask/upload?draft=&name=` | raw bytes, the file's own `Content-Type` | attach a file to a new ask before it exists (a native app's upload at home): into the run held for the draft key `{path, mime, bytes, binary, run}` — see Attachments |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, class, files, draft, messageFiles, slots, queued}` (`class`: the conversation's class, see **Agent classes**) (`draft` = live streaming text; `files` is session-file METADATA only; `messageFiles` = `{msgId: [path…]}`, the files each user message carried; `slots` = `{active, limit}` model calls in flight; `queued` = messages not yet delivered) |
| `GET /runs/{id}/view` | — | the run as the chat draws it, plus a stream cursor — see **The live view**. `?limit=&before=` pages it, newest first — see **Paging the view** |
| `GET /stream?run=&since=` · `GET /runs/{id}/stream?since=` | — | SSE: run-list changes plus the whole tree of `run` — see **The live view**. `&deltas=1`: draft text and tool-call arguments as appended pieces |
| `DELETE /runs/{id}` | — | delete a run, its history, and every subagent run below it. A conversation's sandbox jobs still running are killed in the background (§The coding tools); its sandboxes stay |
| `POST /runs/{id}/message` | `{text, files?, clientId?}` | send a user message → `{inboxId, queued}`. An idle, finished or failed run starts a new turn; a working run gets it at its next step (`queued:true`). A retried post with the same `clientId` is stored once. `files` names session files (normally just uploaded) the message carries — each must exist, or **400** and nothing is written. `text` may be empty when `files` is not |
| `DELETE /runs/{id}/inbox/{iid}` | — | take back a queued message; **409** once the agent has it |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve, grant?, park?}` | approve/deny a parked tool turn (approval mode). A turn parked on a **grant** (`pendingState.grant`, see **Threads, schedules and grants**) is allowed only by the conversation's owner — **403** for anyone else, who may still deny — with `grant: "once"` (the default) or `"hour"`; **400** for anything else. Every park has an id, `pendingState.park`; `park` names the one the verdict answers — **409** when that ask is no longer pending (the agent moved on; the web card and the xbin app say so beside the card, and the run event redraws it) — and without it the verdict answers the one pending now. A verdict applies to its own park only: one queued for an ask that is gone is dropped, never spent on the next ask, and an allow of a grant counts only from the owner (checked again when it is applied) |
| `DELETE /runs/{id}/grants/{cap}` | — | the owner takes a grant back before it expires → `{revoked}` (false when none was in force); **404** for an unknown `cap` |
| `POST /runs/{id}/interrupt` | — | stop the turn in flight (never an error); the run goes idle, its subagents are cancelled, and messages still queued come back as `{returned:[{text, files}]}` |
| `POST /runs/{id}/resume` | — | drive the run again |
| `POST /runs/{id}/compact` | — | force a compaction now — both stages, see **The task and compaction** (answers when it is done) |
| `GET /runs/{id}/asks` | — | the run's task ledger (D133): `{asks: [{id, runId, msgId, seq, source, who, text, at, live}]}`, oldest first — every request it was given, verbatim. Read-only: nothing writes it but delivery. `source` is `human`, `parent`, `schedule`, `trigger`, `channel` or `learn`; `live` is false once the message was compacted out of the model's window (it is then pinned in the prompt) |
| `POST /runs/{id}/learn` | — | distill the run into a saved skill (the /learn flow) |
| `PUT /runs/{id}/memory` | `{key, value}` | set a memory block (one of the agent's notes) |
| `DELETE /runs/{id}/memory?key=` | — | delete a memory block |
| `POST /runs/{id}/cancel` | `{scope?, reason?}` | durable cancel; `scope` defaults to `subtree` |
| `GET /runs/{id}/tree` | — | the whole workflow this run belongs to: nodes (each with its `link` and `phase`, `engine`, and a coding agent's compact `harness` — §Coding agents), statuses, blockers, cost. Metadata only |
| `GET /halt` · `PUT /halt` | `{on}` | the global brake: cancels live runs and blocks new spawns |
| `GET /runs/{id}/files` | — | the run's session files: `[{path, bytes, version, updated, mime?, binary?}]`, no content |
| `GET /runs/{id}/file?path=` | — | one file with its content — a binary-stored text file up to 2 MiB too (a report over the text cap that `render_html` shows), still `binary: true` |
| `PUT /runs/{id}/file` | `{path, content, version?}` | write a file; a non-zero `version` that no longer matches answers **409** |
| `DELETE /runs/{id}/file?path=` | — | delete a file (and its blob, for an attachment) |
| `PUT /runs/{id}/upload?name=` | raw bytes, the file's own `Content-Type` | attach a file: `{path, mime, bytes, binary}`. Never overwrites — a taken name gets `-2`, `-3`…. **413** over 16 MiB, **502** when the blob store fails |
| `GET /runs/{id}/raw?path=` | — | a file's bytes with its type and `nosniff` (the tile's preview and download) |
| `GET /runs/{id}/thumb?path=&w=&h=&fmt=` | — | a sized copy of an image file (PNG, JPEG, GIF's first frame) — see **Thumbnails** |

Content and metadata are separate routes on purpose: a run's detail and view
must never carry file bodies.

Every run records who it belongs to and where it came from (D83): `owner`
(the user id of whoever started it; `""` for runs from before, or from the
owner token and scripts), `visibility` (`private` | `team`) with `teamRole`
(`viewer` | `participant` — what team visibility grants), `origin` (`chat`,
`api`, `schedule`, `watcher`, `channel`, `trigger`), `originId` (the
automation's id), `sessionKey`, `titleSrc` (`clip` | `auto` | `user` |
`origin`) and `activityMs` (the last thing a person or the agent said, or a
wait for someone — what the conversation list sorts by). `POST /ask` also
takes `title` and `system`. Runs from before keep `owner ""` and `team`
visibility, so nothing disappears.

Runs carry a `kind`: `""` for a task, `"quick"` for a quick ask — kept for
compatibility; both are conversations. The tile opens on a home view: the
composer starts a new conversation (in the class chosen with its picker —
"Agent classes" below — remembered per user through `/api/xbin/prefs` —
tile frames have no `localStorage`), and **Needs you** lists what waits for
you (`GET /needs`).
The sidebar is your conversation list (`GET /conversations`): Pinned, then
Today / Yesterday / Previous 7 days / Previous 30 days / Older by last
activity, unread in bold, a search box, and a row menu (right-click or ⋯) to
rename, pin, share, archive or delete. A switch above the list picks the
view — **Mine**, **Shared** (what you shared, then what was shared with
you) or **Archived** — and a shared row says how in chips (the team, to
read or write; how many people; from whom). The open conversation's top bar
says who can see it (private · team can read/write · shared with N · from
its owner) and opens the share dialog. **New chat** goes
home; **⋯** opens "New chat with options" (a title, instructions that
replace the system prompt, the class). A link to a conversation is the
tile's URL with `#c=<id>`. Automation runs (schedules, watchers) are not in
this list.

### The live view

The tile never polls. It reads `GET /runs/{id}/view` — messages (with their
`reasoning`/`reasoningMs`), steps, `links` (subagents, each with its child's
summary and phase), `queued`, the calls in flight as `drafts`, `chain` (the
path from the root), `config`, `class` (the conversation's class as `GET
/classes` shows one, `mixed` included) and a `cursor` — then opens `GET /stream?run={id}&since=<cursor>`.
The stream is Server-Sent Events, `data:` a JSON `{type, run, root, seq, data}`:

| type | data |
|---|---|
| `run` | a run's summary changed (or `{deleted:true}`); top-level runs arrive whatever run is followed — the sidebar. It carries the run's coding sandbox: `sandbox` — the active binding's `{ref, name, cwd, egress, manager}` or `null` — and `attached` (how many), as does the view's `run` (§Coding sandboxes); and its pinned task, `task`: `{count, first, latest?}` (each `{id, seq, source, who, text, cut, at, live}`, `text` clipped — `cut` says so; the whole ledger is `GET /runs/{id}/asks`), or `null` when it has none (a watcher's) |
| `message` | a message added or rewritten (a settled tool result); upsert by `id` |
| `step` | a journal step |
| `inbox` | `{queued}` — the run's undelivered messages |
| `link` | a subagent link changed (spawned, phase, settled, delivered) |
| `thinking` · `text` · `tool` | a model call in flight: the ACCUMULATED reasoning, answer text, or `{index, id, name, args}` of a tool call being written |
| `thinking.delta` · `text.delta` · `tool.delta` | only with `deltas=1`: `{delta, at}` — text appended to that draft; `tool.delta` is `{index, delta, at}`, appended to that tool call's `args` (see **Deltas** below) |
| `draft.end` | that call finished (its message follows as `message`) |
| `reset` | the cursor is from another process or too old: re-read `/view` |
| `bye` | this process is handing over: reconnect at once, the successor answers |

A subagent's events arrive on its root's stream, which is how its work renders
inside the parent's chat. Nothing can fall between the view and the stream (the
cursor is taken before the database is read, and events are idempotent
upserts). Draft events are coalesced per subscriber — a slow reader gets the
latest text, not every token.

**Deltas.** With `&deltas=1` the stream sends what a draft APPENDED instead of
the whole text so far: `text.delta` / `thinking.delta` with
`{delta, at}` — `delta` goes on the end of the text you hold, and `at` is that
text's length in UTF-16 code units (a JS string's `.length`) before it. A tool
call being written streams the same way, per call: `tool.delta` with
`{index, delta, at}` appends to the `args` of the call at `index` (its `id`
and `name` are what the last full `tool` event said). The first draft event of
a run (or of a tool call) on a connection, and any that does not simply
extend what the connection wrote (a new model call, a rewritten text, a new
`id` or `name` at that index), still comes as the full `text` / `thinking` /
`tool` event, which always replaces; `draft.end` and `reset` start that over,
and a new connection sends every live draft in full. So a client handles both
forms, and when what it holds is not `at` long (it replaced the draft from a
`/view` meanwhile, or holds no call at that index) it reconnects the stream.
`hello` carries `deltas: true` when the backend honours the flag — an older
one ignores it and sends full text, which such a client handles anyway (a
backend from before `tool.delta` sends `tool` events whole, which a client
also handles). Without the flag nothing changes.

### Paging the view

`GET /runs/{id}/view?limit=<n>&before=<seq>` returns the newest `n` messages
with `seq < before` (no `before`: the newest; `n` defaults to 50, at most
500; bad values are **400**), for long conversations. Without either
parameter the view is the whole run, as always. A page:

- holds only what the chat shows: messages compacted out of the context and
  the stored system prompt are left out; `compacted` counts the former (the
  "earlier turns were compacted" line);
- never starts with a tool result: it reaches back to the call that made it
  (so it may hold a few more than `n`);
- carries `hasOlder`, and when true `nextBefore` — the `before` of the next
  older page (its first message's `seq`);
- carries the `steps` of its time span (a step shows after the messages of
  its second): from its first message's time when older pages exist, to the
  first message of the next newer page; the oldest page takes every earlier
  step — so pages partition the steps;
- carries the `links` of the subagents its calls and spawn steps started
  (all links of each such child), and `linkCount`, the run's total;
- carries `messageFiles` for its messages.

Everything else — `run`, `drafts`, `queued`, `files`, `memory`, `chain`,
`config`, `access`, `acl`, `cursor` — is the run's current state in every
page. Open the stream from the newest page's cursor and merge older pages
into what you hold (upserts by id): the union folds as the whole view does,
with `compacted` standing in for the compacted messages it leaves out.
On `reset` or `resync`, drop the older pages and re-read the newest. When
older pages exist, the first message you hold is not the run's first: a
subagent's first user message (its task) and a run's opening message are
only in the oldest page.

### Thumbnails

`GET /runs/{id}/thumb?path=<file>&w=<px>` scales an image session file to fit
inside `w` × `h` (never enlarged; aspect kept): `w` defaults to 320, `h` to
4·`w`, both clamped to 16–2048. A JPEG's EXIF orientation is applied, so the
thumbnail is upright as the photo is shown. It is written in the source's
format (PNG for PNG and GIF, JPEG for JPEG) or as `fmt=png|jpeg` asks (JPEG
flattens transparency on white); an image that already fits in the wanted
format comes back as it is. The answer carries an `ETag` and `Cache-Control:
private, no-cache` — revalidate with `If-None-Match` for a **304**. **415**
for what it cannot scale (a text file, WebP, SVG, anything not PNG/JPEG/GIF —
fall back to `/raw` or a file chip), **422** for an image too large to decode
here (over 50 megapixels or ~100 MiB decoded) or a broken one. Access is
`/raw`'s: a viewer of the run.

### Attachments

The owner attaches files from the composer (📎, drop, or paste). They land in the
run's session files — the same store the model's `file_*` tools and the REPL
use. **Text** (`text/*`, JSON, XML, CSV, SVG… that is valid UTF-8 and within 64
KiB) is stored as an ordinary text file. **Everything else is binary**: its
bytes go to the scope's `files` **blob** resource at a random, never-reused
path, and sqlite keeps only the metadata. Binary caps: 16 MiB per file, 64 MiB
per run. Deleting a file or a run deletes its objects, after the rows (a failed
object delete is logged and leaves an orphan object, never a row pointing at
nothing).

Upload order matters: a file must be uploaded **before** the message naming it.
The stored user text gets a short note — `[attached: a.png (image/png, 120 KB)]`
— so the model knows what arrived even when it cannot see images, and
`messageFiles` links the message to the files for the tile.

A quick ask from the home view has no run to upload into yet, so the tile sends
`POST /ask {text, toolset, hold:true}` — a run titled from the text with no
user message and no drive — uploads into it, then sends the message with
`POST /runs/{id}/message {text, files}`. If an upload fails it deletes the
empty run and keeps the files for another try.

**A draft** is the same for a client that uploads a file the moment it is
picked, before the text is written — the native view, whose app uploads to a
path the tile names in advance (the composer's `upload`). The tile makes up a
draft key (8–64 of `A–Z a–z 0–9 _ -`, e.g. `d` + a random UUID's hex) for the
next ask and names `PUT /ask/upload?draft=<key>&name={name}`:

- the first upload for a key creates the run **held** for it (as `hold:true`
  does, owned by the caller) and every later one — parallel ones too — goes
  into the same run; each answers the upload's `{path, mime, bytes, binary}`
  plus `run`. A key is the caller's own: someone else's upload with it makes
  their own draft;
- until it is sent the run is listed nowhere (`origin: "held"`: not in
  `/conversations`, `/runs`, search, `/needs` or the stream's conversation
  list);
- `POST /ask {text, toolset?, title?, system?, draft: <key>, files: [path…]}`
  sends it: titled from `title`, the text or the files' names, the tool mode
  and instructions of this call, the first message with the files still
  chosen (a removed chip's upload stays in the run's files), driven — and
  answers the run as `/ask` does. `text` may be empty when `files` is not.
  A `draft` nothing was uploaded for is an ordinary ask; **409** when files
  are named for a draft that is gone (already sent, or expired) — the chips
  must be attached again;
- a draft nobody sent is deleted, files and all, when its owner starts
  another more than a day later.

## Agent classes (D116) and the toolset firewall

A run's **class** says which toolsets it gets. It is chosen when a
conversation starts and fixed for its life (`config.class`); subagents get
their parent's, and **a schedule the agent creates gets the creating run's**.

| Toolset | Tools |
|---|---|
| `files` | the session files (`file_*`, including `file_info` and `file_diff`) and `render_html` (`file_view` with the `vision` feature) |
| `repl` | the JavaScript sandbox (`js_eval`, `js_run`, `js_reset`) |
| `web` | `web_search`, `web_fetch` |
| `internal` | `xbin_call` and the bound MCP servers' tools — `mcp` narrows them |
| `sandbox` | the coding-sandbox tools, `browser_check` among them; `managers` and `sandboxEgress` narrow what may be bound |
| `subagents` | the `subagent_*` tools |
| `schedule` | `schedule`, `unschedule` |
| `threads` | `schedules_list`, `schedule_inspect`, `threads_list`, `thread_inspect` |
| `skills` | `skills_list`, `skill_view`, `skill_manage` |
| `harness` | coding agents — Claude Code, Codex, Gemini CLI, OpenCode — in a sandbox (§Coding agents); needs `sandbox` and an egress other than `none` (a coding agent must reach its provider); `harnesses` narrows which |

The core tools — `memory_set`/`memory_get`/`memory_delete`, `note`, `recall`,
`message_get`, `finish`, `yield`, `ask_user`, `state_changed`,
`attach_to_reply` — are in every class.
The Features menu can still switch an optional toolset off, and a run's
`deny` list still hides tools.

A class is `{id, name, description?, icon?, toolsets, mcp, managers,
sandboxEgress?, harnesses?, model?, system?, who?}`: `mcp` and `managers` are
`"all"` or a list (MCP server names; sandbox-manager tile paths);
`harnesses` is `"all"` or a list of coding-agent ids (`GET /harnesses`;
`[]` in a class without the `harness` toolset, `"all"` when a class with it
names none, and a save that leaves it out keeps what a class that had the
toolset has);
`sandboxEgress` is the
egress a bound sandbox may have (`none`, `internet`, `open`; `["none"]` when a
sandbox class names none); `model` is the class's model when the person picked
none; `system` is added to the agent's prompt when the conversation has no
system prompt of its own; `who` is `everyone` (default) or `managers` (only
the tile's managers may start conversations or automations in it). Built in:

- **`internal`** 🔒 — files, repl, internal (every MCP server), subagents,
  schedule, threads, skills. **No web.** The old private lane.
- **`web`** 🌐 — files, repl, web, subagents, schedule, threads, skills.
  **No `xbin_call`, no MCP tools.** The old web lane.
- **`coding`** ▣ — sandbox (any manager; egress `none` or `internet`), web,
  files, subagents, skills, harness (every coding agent). No internal
  reach.

A run must never hold private data AND an egress channel: content injected into
its context could otherwise steer it into sending that data out in a URL or a
query. So the firewall is the class's property: a class that holds `internal`
together with any egress — `web`, or `sandbox` with an egress other than
`none` — **can move internal data out**. Saving one takes `confirmMixed`, and
its conversations say so (`class.mixed`). The built-ins never mix them. The
class is enforced twice — in the tool list offered to the model, and again
when a tool runs (`runTool`).

`config.toolset` stays, and still answers the lane: `"web"` for a class that
reaches outside with no internal reach, else `"private"`. Skills, the channel
policy and the thread tools keyed by it work as before, and `toolset:
"private"|"web"` keeps working everywhere it was accepted (`POST /ask`, `POST
/runs`, schedules, triggers), naming `internal`/`web`; `class` is accepted in
the same places and wins. A request naming neither gets the caller's default
(below). A stored config without `class` resolves from its `toolset` (one
from before the lanes, with no `toolset` either, is the private lane — and
held there); a class that was deleted resolves the same way, and so does a
schedule's. The tile's managers can edit a class
at any time: the edit applies from the next step of its conversations, but
never carries one across the firewall — one that started reaching outside
never gains internal reach, and one that started without egress gains it only
if its class was saved as mixed. `PATCH /runs/{id} {class}` naming another
class is refused (400).

| Method & path | Body | Purpose |
|---|---|---|
| `GET /classes` | — | `{classes: [class…], default}` — the classes the caller may start conversations in (a manager sees every one): the built-ins first, then the others as saved, each with `builtin`, `stored` (it is in the saved set — a built-in that is not is its default), `lane` (`private`\|`web`), `egress` and `mixed`; `default` is the class a new conversation of theirs gets when it names none |
| `PUT /classes` | `{classes: [class…], default?, confirmMixed?}` | managers: replace the classes. A built-in left out comes back as its default (old conversations and APIs name it). **409** `{error, mixed: [id…]}` when a class mixes internal reach with egress and `confirmMixed` isn't set; **400** for a bad id (`a–z 0–9 -`, a letter first, ≤ 32), an unknown toolset or egress, a repeated id, an unknown `default`, or a `who` other than `everyone`/`managers`; **400** too for `harness` without `sandbox` or without an egress other than `none` (`the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider`), and for a `harnesses` id no one knows — not the SDK catalog's, not `fake`, not advertised by a bound sandbox manager, and not already in the class (`class <id>: no coding agent "<x>"`); **400** too for an edit that would take a class a channel runs strangers in — a channel policy's `webClass`, and the built-in `web` for every channel that names none — out of the web lane (losing its egress or gaining internal reach), or delete it (a built-in left out is fine: its default is web-lane); the error names the channel. **400** as well for deleting a class a trigger or a channel policy (`privateClass`, `webClass`) names, and for making mixed a class a public-data trigger runs in (data from outside must not steer a class that can move internal data out; `confirmMixed` doesn't change that — a legacy webhook trigger's is the built-in `internal`); the error names the trigger or channel. Schedules and conversations don't hold a deletion up: theirs fall back to their lane's built-in. Answers as `GET /classes` does |

**In the tile.** The composer's class picker (at home, where a new chat
starts) shows the classes you may use — icon and name, each one's
description in its menu, a ⚠ on one that can move internal data out; the
open conversation's top bar shows its class, with the same warning (a
conversation's class is fixed, so there is no picker there). Your last pick
is your default for new chats, kept per person at `/api/xbin/prefs/class`;
with no pick yet, the lane picked before classes (`/api/xbin/prefs/toolset`:
`web` → `web`, else `internal`), then `GET /classes`' `default`. A new ask
sends `class` and, beside it, its lane as `toolset`. Managers edit the
classes under ⚙ → **Classes**: name, icon, description, toolsets, the MCP
servers and sandbox managers (all, or a list), a sandbox's egress, model,
system addendum and who may use them, and the default for new chats. A save
sends the saved classes back with the one edited (a built-in nobody edited
stays at its default); **Delete** on a built-in is **Reset to default**;
saving a mixed class asks first, then sends `confirmMixed`. The native view
has the same: a Class picker in the home toolbar, the class in the
conversation's subtitle, Settings → Classes.

**Automations.** The schedule/watcher and trigger forms pick a class the same
way — the classes you may use; a new schedule starts in `GET /classes`'
`default`, a new trigger in `internal` (what the backend gives either when
it names none) — and send `class` with its lane beside it as `toolset`. A
schedule's class is fixed once it is made, so its edit form only shows it.
Cards and details say each automation's class (`config.class` in `GET
/automations`; one from before classes: its lane's built-in; one you may not
use: its id), with the warning of a mixed one. The trigger form holds Save
on a clash — private data into a web-lane class or a chat, public data into a
mixed class. A channel's rules pick everyone else's class (`webClass`: only
web-lane classes) and, with the private lane open, trusted people's
(`privateClass`); `PUT /channels/{id}` replaces the whole policy, so the form
sends back every field, the ones it does not show included.

The web tools go straight out, not through the gateway, so they need the `net`
interface bound (`bx bind <this component> net=internet`); unbound, they return
"web access unavailable" so the model can adapt.

## Config, models, features

`GET /config`, `PUT /config` — the default `Config` copied into each new run:

```jsonc
{
  "model": "",                 // legacy/general fallback (empty ⇒ the provider's preferred)
  "pick": "",                  // a conversation's own pick (composer, PATCH/ask {model}):
                               //   wins over the tiers for its main loop; not a default
  "models": {                  // per-tier models; any empty tier ⇒ the provider's preferred for
    "general": "", "code": "", //   the mapped use-type (general←agent, code←coding,
    "memory": "", "vlm": ""    //   memory←summarizing, vlm←vlm)
  },
  "system": "…",
  "tokenBudget": 0,            // compaction trigger in prompt tokens (the provider's count when
                               //   known); 0 — and 12000, the old default — = 60% of the model's
                               //   context window as its provider lists it, at least 32000
                               //   (32000 when unknown). See **The task and compaction**
  "maxIters": 12,              // legacy: sizes the default maxTurnSteps (8×)
  "maxTurnSteps": 96,          // model calls in one turn before it stops as incomplete
  "toolTimeout": 120,          // seconds per tool call
  "wire": "auto",              // "auto" | "chat" | "responses" (below)
  "reasoningEffort": "",       // passed to reasoning models ("" = provider default)
  "subagents": true,
  "approve": false,            // gate side-effecting tools on human approval
  "features": { "recall": true, "skills": true, "streaming": true,
                "vision": true, "parallelTools": true, "watcher": true,
                "files": true, "repl": true, "workflow": true, "titles": true,
                "threads": true },
  "replTimeoutMs": 5000,       // REPL budget per statement (max 60000)
  "replMemMB": 256,            // REPL heap watchdog
  // workflow limits (0 = default): delegation depth, lifetime runs per tree,
  // spawns per turn, concurrent MODEL CALLS process-wide, a person's own
  // concurrent model calls in a partitioned agent (never above
  // maxActiveRuns), and how long a foreground subagent is waited for before
  // it moves to the background. The ⚙ Config tab edits them
  "maxDepth": 3, "maxSpawn": 32, "maxSpawnPerTurn": 8, "maxActiveRuns": 4,
  "maxActiveRunsPerUser": 2, "subagentTimeout": 900,
  // coding agents (§Coding agents): an idle one is stopped after
  // harnessIdleMin minutes (absent = 15, 0 = never), and at most maxHarness
  // run at once per conversation tree (0 = 3)
  "harnessIdleMin": 15, "maxHarness": 3
}
```

A conversation's own `engine` and `harness` (§Coding agents) are never
defaults: `PUT /config` drops them, as it drops `sandbox` and `attached`.

- `GET /features` → `{keys, features}` — the toggleable capabilities and their
  current state (the tile's Features menu). Toggle by `PUT /config` with a
  `features` map.
- `GET /models` → `{data: [{id, provider, ref, owned_by?, alias_of?}],
  providers: [{path, ok, legacy?, error?}], error?}` — every bound provider's
  models (for the composer's picker and the tier dropdowns; anyone who can
  use the tile). `ref` is what a tier, `pick` or `{model}` stores.

**Where the models come from (D111).** The manifest's `llm` interface slot
(`http`, service `openai`, multi): bind it to llm-gw, or to any tile that
provides the OpenAI API — several at once. Rebinding restarts the backend.
A model reference is a bare id while one provider is bound (every config
from before the slot), or `<provider>|<id>` when there are several; a bare id
goes to the first provider that lists it, else the first one, and a provider
that is no longer bound falls back to the first with the same id. Requests
carry the bare id. `GET /preferred` (llm-gw's per-use default) is asked of
whichever bound provider answers it. **Unbound**, the agent reaches
`apps/llm-gw` by name — an instance made before the slot keeps working on its
old grant (`/models` then marks the provider `legacy`).

The main loop uses the conversation's `pick` when it has one, else its
class's `model` (see **Agent classes**), else the `general` tier (or `vlm` when a message carries image content and that model
isn't vision-capable); compaction and the summarizer use `memory`. The chat's
composer picks a conversation's model (grouped by provider when several are
bound); the last pick is the person's default for new chats (`/api/xbin/prefs/model`).

**Wires and thinking.** Two model APIs, both through the bound provider: Chat Completions
(`/v1/chat/completions`) and the Responses API (`/v1/responses`), which OpenAI's
gpt-5 and o-series models need to show their reasoning. `wire: "auto"` picks
Responses for model ids `gpt-5*` and `o<digit>*` (after any `backend/` prefix),
and falls back to Chat Completions — remembered per model — when the upstream
does not have `/v1/responses`. Reasoning streams as thinking from either:
`reasoning_content`, `reasoning` or `reasoning_details` deltas and a leading
`<think>…</think>` on Chat Completions; reasoning summaries on Responses
(requested with `reasoning.summary:"auto"`, `store:false`, and encrypted
reasoning items kept for replay). It is stored on the assistant message
(`messages.meta`: `reasoning`, `reasoningMs`, model, usage) and replayed to the
model only on the same wire and model. A stream that goes 90 s without a byte
is abandoned; a call that failed before its first token is retried.

**Tool summaries.** Every tool offered to the model gets a required `summary`
argument — "one short line for the person watching: what this call does" — and
the chat heads the call's card with it (streamed as the arguments arrive). It is
removed before the tool runs, so tools never see it. A tool that already has a
`summary` parameter (`state_changed`, some MCP tools) keeps its own. MCP tool
names (`mcp:<server>:<tool>`) go to the model sanitised to what providers accept
(`mcp_<server>_<tool>_<hash>`) and are mapped back on the way in.

**Images reach the model at context assembly and are never stored** in the
transcript (it is sent to the tile, and feeds search, token estimates and
compaction). An image the owner attached becomes an `image_url`
part (a data URI) on the user message it came with. The `file_view(path)` tool
(feature `vision`) lets the model look at any image in its files; its result
is text, and the image is added in one user message placed after that turn's
**whole** tool-result block — labelled as coming from the tools, not the owner
— or merged into the owner's next message when one follows. Only PNG, JPEG,
GIF and WebP are shown, each ≤ 3 MiB, and only the **4 newest** images in the
context; older ones become `[image x.png not shown — file_view to see it
again]`. With `vision` off, nothing is inlined and `file_view` is not offered.
`file_read` on a binary file returns a pointer to `file_view`; the REPL's
`files.read` throws, and `files.remove` refuses attachments (delete them from
the Files tab).

Known limitation: whether a model can see is a name heuristic
(`modelHasVision`), and the `vlm` tier's last fallback is any gateway model,
with no capability check. If images are ignored, set the `vlm` tier
explicitly.

## Automations (D83)

The non-UI agents — schedules, watchers, chat channels and event
triggers — are **automations**: each belongs to a person (who created it) and
is private or team-visible like a conversation, and the runs it fires carry
its `origin`/`originId` and its owner and visibility. They are not in the
conversation list; the tile's Automations page lists them with their runs.

| Method & path | Body | Purpose |
|---|---|---|
| `GET /automations` | — | `{items:[{kind, id, name, owner, visibility, access, enabled, summary, mode, targetRun, currentRun, lastRunId, lastRunAt, lastStatus, runs, unread, config}]}`. `access` is `owner`, `viewer`, or `oversee` — a manager's view of someone else's private one (it exists, runs, whose; not what it does) |
| `GET /automations?summary=1` | — | `{count, unread, failing, attention}` for the sidebar badge (`attention`: what waits on you — a channel to claim, pairing requests, undelivered replies; also per item) |
| `GET /automations/{kind}/{id}` | — | one of them |
| `GET /automations/{kind}/{id}/runs?cursor=&limit=` | — | its runs, newest activity first, as conversation rows (unread per caller) |
| `POST /automations/{kind}/{id}/read` | — | mark all its runs read |
| `POST /automations/{kind}/{id}/reset` | — | start a thread (a persistent schedule, a watcher) afresh: the next firing opens a new run; the old ones stay listed |

The tile's **Automations** page (the sidebar entry above the conversations,
with a count of what is new) lists them by kind: what each does, when, whose
it is, how its last run went; open one for its runs (opening marks them read;
a run opens with an "Automations ›" way back), its settings, "Run now", on /
off, "Start afresh" for a thread, delete. New schedules and watchers are made
there (it replaces the old Schedules settings tab). `#auto` and
`#auto=<kind>:<id>` link to it.

A **session** is "a key names the current run" (`sched:<id>`, `watch:<id>`,
`chan:<id>:dm:<user>` and the like for a channel's DMs and threads). It never resets on its own by default;
a reset policy (`idle:<seconds>`, `daily:<hour>`) is applied when the next
input arrives — never by a timer.

### Schedules (cron-agents)

Schedules are individual cron jobs; nothing else polls (see **The engine**).

| Method & path | Body | Purpose |
|---|---|---|
| `GET /schedules` | — | the schedules the caller may see (a manager also sees others' private ones, without their goal) |
| `POST /schedules` | `{name?, cron, goal, watcher?, class?, toolset?, visibility?, mode?, targetRun?}` | create + register a cron-agent; its owner is the caller. `class` (or the legacy `toolset`) is its runs' class, fixed at creation; a schedule from before classes answers the built-in its `toolset` names |
| `PUT /schedules/{id}` | `{enabled?, cron?, goal?, visibility?, mode?, targetRun?, …}` | edit (its owner) / enable or disable (also a manager) |
| `DELETE /schedules/{id}` | — | remove (its owner or a manager) |
| `POST /schedules/{id}/trigger` | — | run it now (its owner) |
| `POST /schedules/{id}/fire` | — | the cron target |

`cron` is a 5-field expression or `@every 30m`. `mode` says where a firing
goes:

- `isolated` (the default for schedules made here): a new run each time,
  listed under the automation.
- `persistent`: one ongoing run (session `sched:<id>`) that builds on the
  previous firings; reset starts it afresh.
- `conversation`: into `targetRun`, as a message there ("⏱ Scheduled ·
  name") that the agent answers in that chat. The agent's `schedule` tool
  does this by default (`deliver: "here"`; `"new"` and `"thread"` pick the
  other two). If that chat is gone, or its owner can no longer write there,
  the schedule becomes `isolated` and says so in `lastStatus`.

A **watcher** schedule re-drives one persistent run with a "check now" nudge;
a round where the model doesn't call `state_changed` is rolled back, so
history keeps only the changes. `lastStatus` is how its last run's turn
ended (`ok`, `done`, `error: …`, `incomplete`). Changing a schedule's
visibility changes its runs' too.

### Channels (D86)

A chat platform reaches the agent through an **adapter tile** (a copy of the
`agent-messaging-bridge` template, customised for your platform by a coding
agent) bound to this agent's `inbox` provide (service `agent-inbox`): the binding grants the adapter the `channel` role, which
reaches only `/adapter/*`. The adapter reports messages and pulls replies;
the agent decides everything else — which conversation a message joins (a
session per DM, per thread), who may talk (pairing codes, allowlists,
mentions), the lane (web by default: a reply is an egress) and its class
(policy `webClass` for everyone, default `web` — a class that reaches outside
and has no internal reach, which `PUT /classes` keeps so while a channel
names it; `privateClass` for trusted people when `privateLane` is on,
default `internal`) and the tools (`deny`). The adapter contract, the session keys, the chat commands (`/new`,
`/status`, `/stop`, …) and the policy fields are in `/docs/agent-inbox.md`.

A channel appears (kind `channel` in `GET /automations`, `access: "claim"`
for managers) when its adapter first says hello, and does nothing until a
manager claims it; its conversations belong to the claimer and follow its
visibility. On the Automations page, Channels come first: claim one (with
its rules), approve pairing codes, allow, block or trust people, open or
restart its sessions, retry undelivered replies, edit the rules. The owner's routes (`{id}` is the channel's):

| Method & path | Body | Purpose |
|---|---|---|
| `POST /channels/{id}/claim` | `{name?, visibility?, policy?}` | a manager takes an announced channel: it becomes theirs and active |
| `PUT /channels/{id}` | `{name?, visibility?, policy?, enabled?}` | its owner changes it; a manager may only switch it on or off. A visibility change carries to its conversations |
| `DELETE /channels/{id}` | — | forget it (owner or manager): its conversations stay; a still-bound adapter announces it again, unclaimed |
| `GET /channels/{id}/peers` | — | the people it knows: `pending` (a pairing code out), `allowed`, `blocked`; `trusted` |
| `POST /channels/{id}/pair` | `{code}` | approve the stranger holding that pairing code |
| `PUT /channels/{id}/peers/{peer}` | `{state?: allowed\|blocked, trusted?, name?, unlink?}` | set someone's standing (trusted: the private lane when `privateLane` is on; `/approve`); `unlink` forgets their xbin account |
| `DELETE /channels/{id}/peers/{peer}` | — | forget them (a DM starts pairing again) |
| `GET /channels/{id}/sessions` | — | `{sessions:[{key, runId, resets, reset, created, lastIn, address}]}` |
| `POST /channels/{id}/sessions/reset` | `{key}` | start a session afresh, like `/new` |
| `GET /channels/{id}/outbox?state=failed\|pending` | — | replies not delivered |
| `POST /channels/{id}/outbox/{oid}/retry` | — | queue a failed reply again |

**People linked to xbin accounts.** A person who messages the bot gets a code
(`/link` asks for one) and pastes it, signed in, on the adapter's page; from
then on the agent knows that chat account is them. Their DM is their own
conversation (theirs, private, in their sidebar next to their chats), their
group messages carry their id, and the channel can hear only linked people
(`dm.policy: linked`, `groups.linkedOnly`) or trust them (`trustLinked`). The
peers list shows who is linked to whom.

**Files.** Attachments people send arrive as session files attached to their
message (the model sees images); the model sends files back with
`attach_to_reply` (offered only to conversations that answer into a chat).

Replies are written in the transaction that ends (or parks) the turn — an
answer, an `ask_user` question, "waiting for approval" — and the adapter
acks each after posting it, so a reply is neither lost nor sent twice by
the agent. A message typed into a channel conversation from this page is
answered here, not posted. The list stream sends `{type: "automation",
data: {kind, id}}` when a channel changes (announced, a pairing request, a
failed delivery).

### Triggers (D87)

A **trigger** starts work when something happens, with the event in its
goal:
- an event on a bus this agent may read: an xbind bus push subscription,
  `trig-<id>` → `POST /trigger/bus/<id>`, from `xbin/bus` only;
- a push from a tile bound to this agent's `inbox` (the webhooks tile):
  `POST /adapter/event`, `/docs/agent-inbox.md`.

Where each event goes (`mode`):
- `isolated`: a run of its own;
- `persistent`: one ongoing thread, session `trig:<id>`;
- `conversation`: a message into `targetRun`.

**Event handling:**
- Every event is recorded: the same event id never runs a trigger twice.
- `maxPerHour` caps it.
- While the agent is halted, events are dropped, and a push answers 503
  so its sender retries.
- The goal may use `{{topic}}` and `{{text}}`. The data follows it, fenced
  and marked as data (16 KB at most).

**Data classes keep the lane firewall whole:**
- Event data is `private` unless its source says `public` (a webhook from
  outside). Bus data is always private.
- A trigger that reaches outside takes public data only, checked on save
  and for every event. Reaching outside means a class in the web lane
  (`web`, `coding`, …), or announcing its answers to a chat channel
  (`deliver`: a session key of a channel you own).
- Public data never steers a class that can move internal data out (a
  mixed class, D116): refused when the trigger is saved, `PUT /classes`
  won't make its class mixed, and an event with public data into a class
  that is mixed now — the trigger's, or the ongoing thread's or target
  conversation's — is refused (`reason: "class-mixed"`).
- A trigger's lane (`toolset`) is set when its class is picked and kept
  after, like a conversation's: an edit to the class never carries its runs
  across the firewall.
- Runs on public data also can't schedule or save skills.

| Method & path | Body | Purpose |
|---|---|---|
| `POST /triggers` | `{name, source: push\|bus, sourceRef, match?, goal, system?, mode?, targetRun?, class?, toolset?, dataClass?, deliver?, maxPerHour?, visibility?}` | create; the caller owns it. `class` (or the legacy `toolset`) is its runs' class; an edit that changes only `toolset` to the other lane names that lane's built-in. A bus trigger subscribes at once; `status` says `ok`, or `needs-grant: …` naming the `uses` entry (`{"target": "<bus>", "role": "reader"}`) |
| `PUT /triggers/{id}` | any of the above, `enabled` | its owner; a manager only switches it on or off. `{enabled}` alone is never refused, and the class rules are checked again only when `class`, `toolset`, `dataClass` or `deliver` change — a trigger whose class was edited or deleted since still switches and edits. A `toolset` that switches lanes (without `class`) always applies: the lane's built-in class, picked as on create (its lane is that class's lane now) |
| `DELETE /triggers/{id}` | — | its owner or a manager |
| `POST /triggers/{id}/test` | `{topic?, text?, data?}` | fire it with a sample event (its owner). A partitioned agent's global instance answers 409 for a person's registry row (its tests are theirs, in their partition) |
| `GET /triggers/{id}/events` | — | the last 50 events: `{eventId, source, topic, accepted, reason, runId, at}`; `reason` for one refused: `disabled`, `halted`, `data-class`, `class-mixed`, `rate`, `target-gone`; `handed-off` for one a partitioned agent's global instance handed to its person's partition. The global instance answers 409 for a person's registry row (its events are theirs) |
| `GET /triggers/unmatched` | — | pushes no trigger took (managers): `{items:[{from, name, count, at}]}` — the Automations page offers to make one. Forwarded to the global instance from a person's partition |
| `POST /triggers/registry` | `{name, prev?, source, sourceRef, match, enabled, maxPerHour}` | a partitioned agent's global instance: a person's partition registers (or updates) one of its person's private triggers, as them — never anyone else's; `prev` renames. A private push trigger needs a `match` (400), and one that is a prefix of — or prefixed by — anyone else's on the same source is 409; so is a name someone else has → `{id, name, host}`. 404 anywhere else; 403 for any caller but a person from their own partition |
| `DELETE /triggers/registry/{name}` | — | removes the caller's own registry row (the same callers); 404 when there is none |
| `GET /usage` | `?days=30` (1–90) | people's daily usage totals (managers; a partitioned agent's global instance, forwarded from a partition): `{days, since, people: [{user, days: [{day, runs, llmCalls, promptTokens, completionTokens, harnessSessions}], total}]}`; 404 unpartitioned |
| `GET /handoffs/{id}/files/{fid}` | — | a partitioned agent's global instance: a file of a DM handed to the caller (too large for its mail, named in its `fetch`) — the bytes; 404 for anyone but the handoff's person from their own partition, and once it was fetched |
| `POST /handoffs/{id}/fetched` | — | …the caller's partition has the handoff's files: the global instance deletes what it held → `{deleted}` |
| `PUT /handoffs/{id}/reply-files` | `?key=<outbox key>-<row>/<file>&name=&mime=`, body: the bytes (≤ 16 MiB) | …stages a file of the caller's reply too large for its mail (named then in `outbox/add`'s `staged`) → `{id}`; the same key stages once. The same callers. 400 for another key; 410 for a DM older than 30 days; 413 past 10 files staged for the chat; 507 past 32 files or 64 MiB the person staged and hasn't sent (later) |
| `GET /moves/{id}` | — | a partitioned agent's global instance: where a conversation of the caller's that stopped being shared went → `{run, state: "asked" \| "leaving" \| "moved", to}` (30 days); 404 for anyone else (Partitioned instances → Shared conversations) |
| `GET /moves/{id}/export`, `POST /moves/{id}/done {to, ticket}`, `POST /moves/{id}/abandon {why}` | | …its owner's partition — only it: 403 to their page or terminals — drives the move: reads it (`{…the bundle, ticket, memory, schedules, pinnedAt, archivedAt, behind}`; 409 while anything in it is under way), says it has it (with that export's ticket: it is deleted here; idempotent; 412 when it changed or works again since — read it again; 409 when the move was given up or the conversation left another way), or gives it up (it stays here). Anyone else's partition: 404, and done `{state: "gone"}` whatever the id |

Triggers are kind `trigger` in `GET /automations` (reset starts a persistent
one's thread afresh). An agent-made loop is refused: a trigger on this
agent's own `events` bus needs a topic prefix.

## Skills

`GET /skills` · `PUT /skills` `{name, description?, content, owner?, lane?}` ·
`DELETE /skills/{name}` — a self-authored, reusable procedure library (also
managed by the agent with the `skills_*` tools; injected as a
name+description list). Saving and deleting through the API is the managers'.

Skills have an owner and a lane (D83). A skill the agent writes belongs to
its conversation's owner and its tool mode: a run sees the **shared** skills
(no owner — every skill from before, and what managers save) plus its
owner's, and only those of its own lane (a skill learned in the web mode
never reaches a run with internal reach, nor the other way round). The agent
can't overwrite a shared skill or someone else's — the model is told to pick
another name. `GET /skills` shows a manager every skill and anyone else the
shared ones and their own; a manager editing a skill keeps whose it is unless
they set `owner` (`""` publishes it to everyone).

## The engine (who drives runs)

Every input is a row in the durable **inbox** (`user`, `approve`, `wake`,
`interrupt`, `cancel`, `compact`, `watch`), written by the HTTP handler and
consumed exactly once. A commit that leaves work for a run pokes that run's
**actor** — one goroutine per run with work, never two — which drives it until
there is nothing left to do. Nothing polls: timers exist only for a known
instant (a `yield` wake, a subagent deadline) and are one-shot.

- **One owner.** The process that drives runs holds an exclusive `flock` on
  `<db>.engine` for its lifetime. A new process (a save's blue/green swap)
  serves HTTP at once — its handlers write inbox rows — while its engine waits
  on the lock; the old one cancels its model calls on SIGTERM, tells streams
  `bye` and exits, and the successor takes over the instant the lock drops,
  re-issuing the calls that were cut off. The takeover bumps
  `settings.engine_epoch`, and every actor transaction checks it, so a stale
  process can never write over its successor.
- **Keep-alive.** xbind reaps a backend after 30 idle minutes, counting only
  requests into it. While any run has work or a timer, the engine holds one
  request to itself open (`GET /engine/hold`); it is closed the moment work runs
  out.
- **Resume job.** A process that exits with work pending (xbind stopping, a
  disable; a coding agent's adapter running counts, idle too) leaves a
  one-shot `resume` cron job (in the `beat` resource) that starts the
  backend again; the next owner deletes it, and the pre-D81 `heartbeat`
  job, at takeover. `POST /tick` is the idempotent recovery scan it
  calls.
- **Model calls are gated, not runs.** `maxActiveRuns` bounds concurrent model
  calls. A top-level run's call goes first, and subagents may hold at most
  `limit − 1` slots, so a new chat never waits behind a fan-out. In a
  partitioned agent a person's calls also share their own gate of
  `maxActiveRunsPerUser`.

Upgrading from the pre-D81 loop: runs that were mid-drive in the old binary
finish their lease (up to 30 s) before the new engine adopts them — once.

## Transcript validity

The provider rejects a request in which an assistant `tool_calls` block is not
answered by exactly one tool result per call. The loop writes a placeholder
result for each call before running it and rewrites it in place when the tool
finishes, so the transcript is valid at every instant — including while a turn
is parked for approval (`(awaiting your approval)`) — and results stay in call
order. Placeholders are compare-and-swap — `(running…)`, `(awaiting your
approval)`, `(waiting for subagent #N…)` — so a late writer never clobbers a
settled result. Before a turn, `repairTranscript` heals what a dead process left
behind: a lost placeholder becomes "the backend restarted", a missing result is
spliced in right after its block, and a user message stranded between a call
and its results is moved after them. Replying instead of approving denies the
parked calls.

**Steering.** A message sent while the agent works is queued, and delivered at
the next step boundary — after the current step's tool results, before the next
model call — so the agent reads it mid-turn without ever breaking the
call/result pairing. A plain answer with a message waiting loops again instead
of going idle.

## Run status

`idle` (awaiting a message) · `running` · `waiting_input` (parked on
`ask_user`/approval) · `awaiting` (a subagent it is waiting for) · `sleeping`
(yielded; its wake is a timer) · `done` · `error` · `canceled`.

A top-level run is never finished for good: `done`, `error` and `canceled` all
start a new turn on the next message. A background subagent's answer wakes an
`idle` or `done` run, but not one in `error` or `waiting_input` — it waits
there and rides along with the next message. `queued` and `blocked` are
pre-D81 values, rewritten on upgrade.

## Workflows (the run graph)

A workflow **is a root run**: `runs.root_id`/`depth` place every run in a tree,
`links` records each parent→child delegation, `link_deps` the `after`
dependencies, and `run_trees` the per-tree lifetime spawn budget. **A node id
is a run id**, so the journal, `recall`, session files and the tile's
click-through all work on a node. Feature key `workflow`.

Tools (all need `subagents` on and depth below `maxDepth`):

| tool | what it does |
|---|---|
| `subagent_spawn {task, label?, wait?, timeout_s?, after?, system?, harness?, harness_mode?}` | start a subagent. `wait:true` (default) waits for its answer — several in one step run in parallel, and their answers land in call order in one step. Past `timeout_s` (default `subagentTimeout`) the wait ends with a progress digest and the subagent **moves to the background**; its answer arrives later. `wait:false` starts it in the background at once. `after:[ids]` starts it once those runs settled, with their results in its first message. `harness` starts a **coding agent** instead (§Coding agents, "The agent's coding agents") |
| `subagent_wait {ids, mode?, timeout_s?}` | wait for background subagents: `all` or `any`; answers for the settled, digests for the rest |
| `subagent_status {ids?}` | phase, elapsed time, model calls, its latest tool summaries, pending approval, queued messages |
| `subagent_result {id, offset?, limit?}` | page through a long answer |
| `subagent_message {id, text, wait?}` | steer a working subagent, or give a finished one a follow-up (a coding agent's: sent as is, into its running turn, after it, or as its next prompt) |
| `subagent_cancel {ids, reason?}` | stop subagents and everything below them |

The pre-D81 names (`spawn_subagent`, `workflow_spawn`, `workflow_status`,
`workflow_result`, `workflow_cancel`) still work as hidden aliases, so old
transcripts and prompts replay unchanged.

The rules that keep a tree from hanging:

- **Who writes what.** A child writes only its link's outcome (state, result)
  in its turn-end transaction; the parent writes only delivery and demotion.
  A child settling at the instant its deadline demotes it resolves either way.
- **A parent waits only on its own step's calls** — not on unrelated
  background children — and **a message from the human ends the wait**: the
  remaining subagents move to the background and the agent reads the message.
- **Background answers are batched.** Everything that settled arrives as one
  notice at the next step boundary, so five children finishing produce one
  parent step.
- **A child always settles**: answered, done, incomplete (it hit the step
  cap, or tried to ask a question — a subagent has no human to ask), error,
  canceled or interrupted. A child parked on approval shows the approval in its parent's
  chat. A child's turn end cancels its own descendants still running.
- **A top-level error does not cancel its children**; their answers arrive when
  the chat resumes.

Limits, all in `Config`: `maxDepth` 3, `maxSpawn` 32 per tree (lifetime, so
spawn→finish→spawn cannot loop forever), `maxSpawnPerTurn` 8, `maxHarness` 3
coding agents at work per tree, enforced when the spawn runs. Subagents get neither `ask_user` nor `schedule` (a cron-agent
outlives the tree that made it).

Every `subagent_*` id resolves through the caller's **own subtree**. That
scoping is the security boundary: an unscoped id would let a web-lane run read
a private-lane result by guessing.

## Loop & tools

Each step: deliver queued messages and background answers → compact when over
budget → assemble context (the system prompt, the pinned task, a stable date,
the sandbox, the agent's notes, the skill list, the running summary, then the
live transcript and the task reminder — see **The task and compaction**) → LLM
call (streamed when the feature is on) → execute tool calls (a step's
non-control tools run in parallel, each under `toolTimeout`) → repeat, up to
`maxTurnSteps` per turn.
Built-in tools: `memory_set`/`memory_get`/`memory_delete` (the agent's notes),
`note`, `recall` (search of the whole history), `message_get` (one message in
full), `xbin_call` (reach other granted components; `internal`),
`web_search`/`web_fetch` (`web`), `schedule`/`unschedule`, `state_changed`
(watcher), `schedules_list`/`schedule_inspect`/`threads_list`/`thread_inspect`
(below), `skills_list`/`skill_view`/`skill_manage`, `finish`, `ask_user`,
`yield`, the `subagent_*` tools above, the session-file and sandbox tools below,
plus any bound MCP tool — each as its class allows (**Agent classes**).
`finish` is worded for who reads its result: a top-level run ends its turn,
its `result` a one- or two-sentence status line (the chat's `✓` line, which
renders markdown) with the answer itself in the reply before it; a
subagent's `result` is the full answer its parent receives; and a channel
conversation's or a trigger's top-level run keeps `result` as the full
reply, because that is what is posted (§Channels).
MCP servers are bound via the `mcp` interface (multi:true, like the chat tile).
Extend these in `_backend/tools.go`.

### The task and compaction (D133)

**The task ledger.** Every request a run is given is recorded verbatim, in the
transaction that delivers it into the transcript: `POST /runs` and `POST /ask`,
a person's message, **Learn skill**, a schedule's firing (a run of its own, or
a message into a conversation), a trigger's event, a channel's message, a
subagent's task and every `subagent_message` its parent sends it. A watcher's
"check now" is not a request (its job is its instructions), and neither is a
background subagent's answer. The model reads the ledger but no tool writes
it. `GET /runs/{id}/asks` serves it; the tile pins the **current** request —
the latest one (the run's `task.latest`, else `task.first`) — under its top
bar, and **Task (+N)** unfolds every request. A run
from before the ledger has its first user message recorded when the tile
upgrades (and a run an older process makes meanwhile, when it is first read).

**What the model sees.** The system prompt holds, in order: the configured
system prompt; `# Your task (verbatim — it outranks your notes and the
summary)` — the first request, then later requests whose turns were compacted,
newest last (the first up to 6000 characters, the others 1500, 12000 in all; a
cut one names the `message_get {"seq": N}` that has the rest); the date; the
bound sandbox; `# Your notes (you wrote these; the task above outranks them)`
— the memory blocks, sorted by key; the skills; the latest summary. Requests
still in the conversation are not repeated, so everything up to the last
message changes only when a compaction runs, a note is written or the date
turns — the provider's prompt cache keeps hitting. The last message of every
request carries a reminder that is never stored:

```
<task-reminder>
Current task — "<title>". Latest request (#12, human alice): "<up to 400 characters>". Its full text: # Your task, the conversation, or message_get {"seq": 12}.
</task-reminder>
```

— appended to that message's text (a tool result, or the person's message;
a parts array gets one more text part), on both wires; it is left out when the
last message is the latest request itself. The call right after a compaction
also carries, once: "Earlier turns were compacted: old tool outputs are now
short stubs (message_get restores one) and older turns are summarized. Re-read
# Your task before you continue."

**Compaction** runs when the last call's prompt (as the provider counted it)
passed the budget — `tokenBudget` when set, else 60% of the model's context
window (read from the providers' model lists: `context_length`,
`max_input_tokens`, `context_window`, `max_model_len`, … — llm-gw passes them
through), at least 32000 — or when asked (`POST /runs/{id}/compact` runs both
stages):

1. **Masking.** Tool results older than the newest 5 steps (and over 1200
   bytes) are shown to the model as a stub — `[bash output, 41.3 KB — hidden
   to save context; message_get {"seq": 812} shows it, or bash_output {"job":
   3, "offset": 0} rereads the job while the sandbox keeps it]` with its first
   and last words. The message keeps its content (`masked: true` in the view;
   the chat still shows it) and stays searchable. If that brings the prompt
   under 75% of the budget, compaction stops here.
2. **Summary.** Otherwise the oldest turns — all but those that fit in half the
   budget, at least the newest 6 messages — are folded into a summary by the
   `memory` model, and leave the window (`compacted`). The summarizer is shown
   the pinned task (not to restate it: requests appear in what it folds only as
   `[a request — pinned verbatim]`), the prior summary and the turns with their
   tool calls. Each summary is kept (`summaries`, searched by `recall`);
   `runs.summary` is the latest.

The journal's `compaction` step says what happened: `{messages?,
summaryTokens?, masked?, savedTokens?, budget, budgetFrom, promptTokens}`.

**Getting it back.** `recall {query, match?, order?, limit?}` searches the
whole transcript — compacted and masked messages included — and the summary
history: ranked by relevance (bm25) by default, `order: "oldest"` (the
original request) or `"newest"`; every word must match unless `match: "any"`;
8 hits by default, 20 at most. Its own earlier results (and `message_get`'s)
are never hits. A short hit is shown whole, a long one as an excerpt with its
size. `message_get {seq, offset?}` returns one message in full, 12000
characters a page. Notes: `memory_set` (8000 characters at most),
`memory_get`, `memory_delete`.

MCP tool lists are cached per server and persisted: a list younger than 5
minutes is used as is, an older one is used at once and refreshed in the
background, and only a server never listed before is waited for —
concurrently, 5 s at most each. A run is marked `running` before any of this;
a class without `internal` skips discovery entirely, and one naming servers
wakes only those.

### Threads, schedules and grants (D111)

Four tools let a conversation's agent look at its automations and what they
did — top-level runs only, feature `threads`:

| Tool | Arguments | Returns |
|---|---|---|
| `schedules_list` | `{scope?, enabled?, q?, cursor?, limit?}` (limit ≤ 50, default 20) | one line per schedule, newest first: id, name, cron, where firings go, enabled, the last run — then its goal |
| `schedule_inspect` | `{id, cursor?, limit?}` (≤ 30, default 10) | the schedule's settings and goal, then the runs it fired (newest activity first), each with its latest answer |
| `threads_list` | `{scope?, origin?, status?, q?, archived?, cursor?, limit?}` (≤ 50, default 20) | one line per thread, newest activity first: `#id [origin] "title" — status · when`, plus its owner when it is someone else's. `origin` is `chat`, `schedule`, `watcher`, `channel`, `trigger` or `api`; `status` a run status (`waiting` for `waiting_input`); `q` matches the title or anything said in it (FTS); `archived` filters by the owner's archive (both when absent) |
| `thread_inspect` | `{id, before?, limit?}` (≤ 100, default 30) | who and what it is, the summary of its compacted turns, then its messages by `seq` — the latest page, and `before=<seq>` for older ones |

A list that has more ends with `more: cursor "<c>"`; pass it back as `cursor`.
Results are capped like every tool result.

**Scopes.** `scope: "mine"` (the default) is free: the schedules this
conversation created or that deliver into it, the threads those schedules
ran, and this conversation's own tree. `scope: "all"` is what the
conversation's **owner** has: every thread they own or were added to (not
other people's team conversations, not held drafts) and every schedule they
own; inspecting an id outside mine counts as all. An id in neither reads as
missing. A schedule removed since keeps its past runs, but they are only
found through all.

**Grants.** Reading "all" needs the owner's grant. The step parks like an
approval — `status: "waiting_input"`, `pendingState: {kind: "approval",
grant: "threads", toolCalls}` — and only the owner may allow it (`POST
/runs/{id}/approve {approve: true, grant}`): `"once"` runs the parked calls
and keeps nothing; `"hour"` also lets later calls in this conversation read
all for an hour. Anyone who may steer the conversation may deny it; Needs you
and push notifications reach the owner alone. A grant in force is on the
conversation's `run` (view and stream): `grants: [{cap, grantedBy,
expiresMs, ask, chip}]` (`ask`/`chip`: the capability in words, as the
pending ask carries them in `pendingState.grantAsk`); expiry is read where a grant is used (nothing ticks), and the
owner can revoke it (`DELETE /runs/{id}/grants/threads`).

The grants are a registry: `threads` (above) and `sandboxes` — creating a
coding sandbox with `sandbox_create` (§The coding tools), whose
`pendingState.grantAsk` names the sandbox that will be made.

"all" is refused outright — the call says why — in a web-toolset run (a web
lane carries public data only; its "mine" also lists only web-toolset
automations), in a chat channel's run (no one there can allow it), and in a
conversation no person owns.

## Session files + render

Feature key `files` (on by default). A per-run file store held in sqlite, not on
disk: `file_write` · `file_read` (whole file, or a line range with
`offset`/`limit`) · `file_edit` (exact-string replace) · `file_list` ·
`file_info` · `file_diff` · `render_html` · `file_view` (an image — see
Attachments). They touch only this
run's private rows — no egress, no other component — so they are offered in
**both** capability lanes and are not `sideEffect()` tools: the approval gate
never fires for them. Caps: 64 KiB per text file, 64 files and 512 KiB of text
per run; attachments have their own.

`render_html` journals a `render` step (`{path, version, bytes}`) and
streams it at once, so the pane opens while the turn goes on; the tile
shows that file in a `sandbox=""` iframe with a prepended meta CSP
(`default-src 'none'; style-src 'unsafe-inline'; img-src data:`). **Scripts
never run and nothing external loads** — so charts must be inline SVG. Its
description and result say so first (D134): a static snapshot, not a
browser, that verifies no JavaScript. Verify
with `node test/frame-policy.mjs`. It shows HTML up to **2 MiB**: a file
over the 64 KiB text cap (a report written in the sandbox, typically) is
stored as a binary session file (`text/html`) and still renders — `GET
/runs/{id}/file` answers its text, painted through the same policy. The
chat's `🖼 rendered …` line is a button that shows the file again once the
pane is closed (a subagent's, from its own run's files).

The tile's frontend is **one model, thin views** — see **The frontend** below.
The chat's state is `model/session.js` (fed by `model/stream.js`),
`model/fold.js` (pure: messages, links and drafts → blocks), `chat-cards.js`
(lit templates), `model/tool-heads.js` (a tool call's headline and icon) and
`chat-md.js` (markdown); `agent.js` is the page around it. The tile's other
browser tests (`test/layout.mjs`, `chat`, `home`, `sidebar`, `attach`) drive
the real page against `test/backend.mjs`, a stubbed transport with a
scriptable event stream; `test/kit.mjs` serves the frontend kit and vendored
lit from the xbin checkout the template lives in (in an instance:
`BX_KIT=/path/to/bx-kit.js`, `BX_VENDOR=/path/to/vendor`). Each needs
Playwright with a Chromium build and skips without it. The backend: `go vet
./_backend && go test ./_backend` with a `go.mod` copied from `go.mod.tile`.

File versions are monotonic. Clicking an older render chip shows the file's
current content, with the header noting the difference. The tile's Files tab
edits them too, sending back the version it loaded so a write the agent made
in between comes back as a 409 instead of being lost.

### Hashes, sources and earlier versions (D136)

Every write records, for the version it makes, the content's **sha256**, its
**source** and the version it replaced (`parent`): `{kind: "tool", tool,
call}` (file_write, file_edit), `{kind: "sandbox", sandbox, box, path, etag,
tool, call}` (a copy of a sandbox file), `{kind: "browser", target, atMs,
call}` (a browser_check screenshot), `{kind: "upload"}` (a person's upload).
`GET /runs/{id}/files` rows carry `sha256`, `source` and `parent` when known.
A row written before D136 — or by an older backend during a blue/green
overlap, which bumps the version without them (`meta_ver` ≠ `version`) — has
none: unknown, never wrong; a text file's hash is computed on demand. An
overwrite keeps the version it replaces in `repl_file_versions` (the last 10
per file; 1 MiB of earlier text and 32 MiB of earlier binary objects per run,
oldest out first), so `file_diff` and `file_read session:x@N` can reach it.
Deleting a file deletes its earlier versions (and their objects).

- `file_list` — one line per file: size, version, a short hash, where it came
  from; `same content as …` for duplicates; `changed in the sandbox since` /
  `gone from the sandbox since` for a copy whose sandbox file's etag moved
  (one stat each, at most 10, only while that sandbox is attached).
- `file_info {path}` — a session file's full hash, source, parent, duplicates,
  sandbox drift and earlier versions; or a sandbox file's type, size, mode,
  mtime, etag, sha256 and the session files copied from it.
- `file_diff {a, b?}` — a unified diff (3 lines of context, ≤ 12 KiB) of two
  text files, each in either place; identical content is reported by hash,
  binary files by hash and size. `b` omitted: a session file's previous
  version against its current one, or a sandbox file's session copy against
  it.

### Two places, one way to name them (D136)

`file_read`, `file_view`, `render_html`, `file_info`, `file_diff` and
`browser_check` take:

| Path | Means |
|---|---|
| `report.html`, `session:report.html` | a session file (a bare key always meant one) |
| `session:report.html@2` | an earlier version of one (`file_read`, `file_info`, `file_diff`) |
| `/work/report.html`, `./out/r.html`, `~/r.html`, `sandbox:r.html` | a file in the bound sandbox (relative to the binding's cwd) |

Without a bound sandbox (or the `sandbox` toolset), `./x` stays the session
file `x` and `/x` is refused with that reason. A miss names the other place
when it has the file: `no such file "notes.md" — not a session file, but the
sandbox has /work/notes.md`, or `/work/todo.md doesn't exist in the sandbox —
session:todo.md is a session file`. `file_read` of a sandbox path is the
sandbox's `read`; `render_html` and `file_view` of one copy it into the
session files first, in place, as `sandbox_download` does (its result line
leads). The copy's name keeps different files apart: the session file an
earlier copy of that very file (sandbox and path) holds — a repeated render
is its next version — else the first free of its base name, its parent
directory and base name (`site/index.html`), and the sandbox's name with
both (`web/site/index.html`); two different `…/index.html` never become
versions of each other, nor overwrite a session file the agent wrote. A
download's result names the sandbox it came from, with the replaced
version's own source in parentheses (`downloaded /work/r.txt from the
sandbox "b" to the session file r.txt (text, 7 B) — v2, replacing v1
(copied from the sandbox "a": /work/r.txt)`).

## Coding sandboxes (D115)

A conversation can work in a **coding sandbox**: a box with a shell, a
filesystem and the tools of a job, run by a **sandbox manager** — a tile
that implements the `sandbox-manager` contract (docs/sandbox-manager.md;
the builtin `coding-sandbox` template once it ships, or anyone's own). The agent holds no
sandboxes itself.

**Where they come from.** The manifest's `sandboxes` interface slot (`http`,
service `sandbox-manager`, multi): `bx bind <this component>
sandboxes+=apps/<manager>`, or the binding panel. Several managers may
be bound at once; rebinding restarts the backend; unbound, there are no
sandboxes. The agent says `hello` to each (protocol 1; cached five minutes)
and ignores — listing it with the reason — one that speaks another protocol
or lacks the `exec` and `files` capabilities. A manager shows this agent the
sandboxes it created and those shared with it (what it sees as this
consumer; a partition sees its own, §Partitioned instances).

**References.** A sandbox is named `<provider>[#inst]|<id>` — the manager
tile as its binding names it and the manager's id — always qualified, so a
stored reference keeps naming the same sandbox however many managers are
bound. In a URL path it may be sent as is or percent-encoded.

**People (D83).** Every call the agent makes to a manager names the person
it acts for in `Sbx-User` (asserted: the manager records it as the owner of
what it creates — except in a person's partition, whose person is the
partition's and verified: the manager applies its person rules to them
itself and refuses any other `Sbx-User`, 403,
[/docs/sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned
consumers); the agent enforces who may do what:

- **use** (bind it, work in it, start it): its owner, a member, or anyone
  when it is `team`. A sandbox with no owner (created by a component or the
  tile itself) is theirs, and people's only when it is `team`. A sandbox
  another consumer shared with this agent (`shared`) is, besides, only for
  the people its share names (`users`: `"*"` or their ids) — with no share
  for this agent, it is nobody's here;
- **manage** (stop, archive, delete): its owner, and the tile's managers —
  who may stop or delete any sandbox but never bind someone else's private
  one;
- **edit** (name, visibility, members, shares): its owner.

**A conversation's sandbox.** `config.sandbox` is the one its tools work in
and `config.attached` every sandbox it has attached (up to 8, the active one
among them — a subagent may be spawned onto another, and files copied
between them). Each is a binding:

```jsonc
{"ref": "apps/coding-sandbox|sb-7f3a", "cwd": "/work/api",  // where tools work (default: the sandbox's workdir)
 "name": "api-dev", "manager": "Coding sandboxes",           // as they were when it was bound (for display)
 "image": "base", "egress": "none",
 "by": "alice", "at": 1790000000000}                          // who bound it: tools act for them (Sbx-User)
```

Both live in the conversation's stored config (the view's `config`) like
its model pick: read every turn — a rebind applies from the next one — and
copied into subagents and workflows, which work in their root's sandbox.
The global defaults (`PUT /config`) never hold either. `run` events and the
view's `run` carry the active one in short — `sandbox: {ref, name, cwd,
egress, manager} | null` — and `attached` (how many), so a view follows a
rebind without reading the view again (a tile that predates them re-reads
the view).

- **Binding** takes participant access to the conversation **and** the
  right to use the sandbox; the conversation's class must have the `sandbox`
  toolset and allow the sandbox's manager and egress (D116). The egress
  checked — here and on every tool call — is the less restrictive of the
  sandbox's `egress` and its `egressNext` (the one a change gives it at its
  next start: a stopped sandbox starts on a command), a missing or unknown
  one counting as `open`; it is also what the binding records. A `cwd` must
  be an absolute path, and a directory when the sandbox is running; no
  `cwd` is the sandbox's workdir — or, for a sandbox the conversation has
  attached already (a re-pick), the `cwd` it is attached at.
- **Anyone who may steer the conversation works in what it has bound** —
  under the binder's right, which every tool call re-checks: the class
  still allows it, the manager is still bound, the sandbox still exists, and
  the binder may still use it and still takes part in the conversation.
  A subagent's copy of a binding holds only while the conversation still
  has that sandbox bound or attached, by the same binder: a detach (or a
  rebind by someone else) reaches every subagent at once — and the turn in
  flight, whose later tool calls there are refused (`was detached from this
  conversation during this turn`).
  Otherwise the tool says why and the conversation needs a new binding.
- **The firewall across a shared sandbox.** A sandbox outlives a
  conversation and may be bound to several, so the class firewall follows
  what it has held: binding a sandbox to a conversation whose class has
  internal reach first labels it `xbin.agent/internal: "1"` (merged into
  its labels; a manager that won't keep it refuses the binding), and a
  class that reaches outside (`web`, or a sandbox egress other than `none`)
  with no internal reach may then neither bind it nor keep working in it —
  `this sandbox has held data from an internal-reach conversation` — even
  where it was bound first. A confirmed mixed class may. The mark spreads
  within a conversation, whatever its class: once a conversation has had a
  marked sandbox — bound it, or worked in one that was marked since — it has
  held internal data (`config.heldInternal`, kept for good), every sandbox
  it has attached is labeled then, and every one it binds or works in after
  is labeled first (a detach doesn't undo it: the data may be in its session
  files or its context); `sandbox_copy` from a marked sandbox so labels its
  target before it writes. So a class with a sandbox but neither internal
  reach nor egress can't launder data into a clean sandbox for a web-lane
  conversation. A tool call of an internal-reach conversation (or one that
  has held internal data) re-labels a sandbox that lost the label, and
  `PATCH /sandboxes/{ref}` keeps it when it replaces the labels — against a
  concurrent label too (it sends the sandbox's `version`, and re-reads once
  on a 412). The only way back is a new sandbox.
- **A changed egress** (its owner changed the sandbox's network access since
  it was bound, and the class still allows it): the tool call that finds it
  records the live value in the conversation's bindings (and the calling
  subagent's copy), which the turn's next step already uses. In Approve
  mode, a call that would park under the new egress is refused once — `the
  sandbox's network access changed from none to internet; call the tool
  again to ask for approval` — and parks when called again, so a side effect
  never runs on an egress nobody approved it for.
- A **sandbox created for a conversation** (`POST /sandboxes
  {conversation}`, or `sandbox_create`) takes the conversation's audience
  when it is made: a team conversation's is `team` (whatever the team's
  role there — anyone on the team may use it); the conversation's owner and
  participants are its members; it is labeled `xbin.agent/conversation:
  <id>` and bound there. It is copied once: sharing the conversation
  differently later, or removing a participant, doesn't change the
  sandbox — its owner manages that in the Sandboxes dialog (visibility,
  members). `sandbox_create`'s grant card says when it will be the team's.

`PATCH /runs/{id}` also takes `{sandbox: {ref, cwd?} | null, detach?: <ref>}`
— bind (and attach) a sandbox, or change the active one's `cwd`; `null`
leaves the conversation with no active sandbox (the attached stay);
`detach` takes one off (applied first when both are sent). `POST /ask` also
takes `{sandbox: {ref, cwd?}}`: the new conversation starts bound (the
caller must be able to use it; its class must allow it) — refused as the
sandbox routes refuse (a manager's `refusal` with its status: 404 gone or
unbound, 403 not allowed, 502 its manager down), and nothing is created.

| Route | Body / query | Result |
|---|---|---|
| `GET /sandboxes` | `?fresh=1` skips the cache | `{sandboxes: [{ref, provider, manager, …the contract's sandbox…, mine, canUse, canManage, canEdit, boundTo?, homed?, why?}], managers: [{provider, title, ok, error?, refusal?, caps, egress, images, sizes, limits}]}` — every sandbox the caller may see across the bound managers, and those bound to a conversation the caller sees (`boundTo`: its ids; `homed`/`why`: in a person's partition only — whether its conversations may work in it, and why not: §Partitioned instances). Merged, cached 15 s (the agent's own changes show at once); `manager` is the manager's title. Anyone who can use the tile |
| `POST /sandboxes` | `{name, provider?, image?, size?, egress?, visibility?, members?, conversation?, bind?, cwd?, clientId?, start?}` | **201** + the sandbox (as below), with `binding` when it was bound. Created at `provider` (optional while one manager is bound), owned by the caller. With `conversation` (the caller takes part in it): made for it (above) and bound there unless `bind: false` — refused up front when its class wouldn't allow it, and deleted again if the binding fails. `clientId` makes a retry return the same sandbox (per person) |
| `GET /sandboxes/{ref}` | | one sandbox, fresh from its manager, as `GET /sandboxes` lists it |
| `PATCH /sandboxes/{ref}` | `{name?, visibility?, members?, shares?, labels?, egress?, size?, autoStopMin?, version?}` | the sandbox — its owner's (the contract's `PATCH`; `restartNeeded` when a change waits for the next start, and `egressNext` while an egress does). New `labels` keep `xbin.agent/internal` (sent with the sandbox's `version` unless you send one: a label set meanwhile is read again and kept). One that shares it first ends the saved sign-ins there (D179: guided sign-ins, adapters holding one, codex's key file) — **502** `‹name› isn't shared: …` when it can't |
| `DELETE /sandboxes/{ref}` | | `{ok, detached}` — its owner's or a tile manager's; it is detached from every conversation that had it |
| `POST /sandboxes/{ref}/{start\|stop\|archive\|thaw}` | `?wait=<s>` (≤ 120), `?conversation=<id>`; `{start?}` on thaw | the sandbox. Start, stop and thaw: who may use or manage it — or, with `conversation`, a participant of a conversation it is bound to (as the binder). Archive: its owner or a tile manager |

Refusals from a manager keep its `refusal` (and `state`) in the error body,
with the status the contract gives it (a manager that is down or
unreachable: 502). A route's sandbox the caller may neither see nor find
bound to a conversation of theirs is 404.

### The coding tools

A conversation whose class has the `sandbox` toolset **and** has a sandbox
bound gets these tools (subagents too — they work in their root's sandbox);
otherwise they are absent, and a call that names one anyway is refused
(`sandbox_create`, below, is the exception: it makes the first one). Every
call re-runs the binding check above. Paths are absolute, relative to the
binding's `cwd` (else the sandbox's workdir), or `~/…` (the sandbox user's
home). The session files (`file_*`) are a different store: nothing moves
between the two unless a tool below moves it. The system prompt carries a
`# Sandbox` section — the active sandbox's name, manager, image (and the
tools its manager says the image has, `images[].tools` of its hello, kept in
the binding as `tools` when it is bound), egress and `cwd`, the two places
files live (the session files and the sandbox's own filesystem), how jobs
are stopped (`bash_kill`, never `pkill -f`/`killall`), and the other
attached ones — built from the binding alone, so it changes on a rebind only
(the prompt's cached prefix stays valid).

| Tool | Arguments | What it does |
|---|---|---|
| `bash` | `{command, cwd?, timeout_s? (120), background?, force?}` | runs `command` with the sandbox user's login shell (an exec named `agent:<run>:<tool call>`, so the same call re-issued — the same command, cwd and sandbox, while it hasn't ended — finds the one command; anything else under that call id is a job of its own, `agent:<root>:job-<n>`), no TTY, no stdin, `TERM=dumb NO_COLOR=1 PAGER=cat GIT_TERMINAL_PROMPT=0 PYTHONUNBUFFERED=1`, and follows its combined output. The command reaches the shell **through the environment** (D134): the exec's `cmd` is `eval "$AGENT_JOB_CMD"` with `AGENT_JOB_CMD` = the command, so its text is in no process's command line and a `pkill -f <pattern>` in it can't match the job's own shell; the exec's `label` is `agent · job <n> · <the command, one line, clipped>`. A command with `pkill -f`, `pkill --full` (any flag cluster with `f`) or `killall` isn't run: the answer (`not run: stop jobs with bash_kill …`) lists the conversation's jobs; `force: true` runs it anyway. The result is at most 12 KiB — a short head and a long tail with `… N bytes elided …` between, escapes and `\r` redraws cleaned — and a footer: `[exit 1 · 14s · job 3]`. At `timeout_s` (or just before the tool's own `toolTimeout`) the command **goes on as a job**: the footer says `still running after 2m00s · job 3` and how to follow it — and, when the `timeout_s` the model gave was cut by the tool's time limit or the 3600 s cap, says so (`still running after 1m57s (timeout_s 600 was cut to 117s: a tool call's time limit) · job 3`). `background: true` starts it as a job at once. A start the manager doesn't answer (a timeout, a lost connection, the tool's own timeout) may have started all the same: the job stays, and the result names it — `bash_output` finds its command by its clientId (or says it never started); only the manager's refusal drops it |
| `bash_output` | `{job, wait_s? (0, ≤ 600), offset?}` | a job's output since it was last read (or from byte `offset`), waiting up to `wait_s` for it to end; the footer says it still runs (and up to which byte it was read, and when `wait_s` was cut) or how it ended (`exit 3`, `killed by TERM`). A job in a sandbox the conversation can no longer use (below) answers what is known of it |
| `bash_kill` | `{job, signal?}` | signals the job's whole process group: `INT`, `TERM`, `KILL` or `HUP`; by default TERM, then KILL if it hasn't ended 3 s later. The answer is what the job wrote since it was last read — its last 4 KiB at most, after `… its last N bytes (bash_output {"job": 3, "offset": K} reads what came before) …` — and a footer `[job 3 stopped · killed by TERM]` (or `exit N`); the read offset moves past it |
| `jobs` | `{limit? (12)}` | the conversation's jobs, newest first — the running ones asked of their sandboxes first — one line each: `job 3 · running · 2m14s so far · <command> (in <cwd>)`, `job 2 · killed by TERM · ran 12s · ended 5m ago · …` |
| `read` | `{path, offset?, limit? (2000)}` | numbered lines (`cat -n` style), within ~14 KiB, saying what it left out; a file up to 256 KiB is read whole and sliced, a larger one ranged with `sed -n`; a binary file (a NUL or non-UTF-8 near its start) gets a hint instead. A symlink is followed to its file (a relative target against the link's directory; at most 40 links) |
| `write` | `{path, content}` | replaces the file atomically (the contract's `PUT …/files/content`), creating missing directories. It replaces what is at `path`: a symlink there becomes the file (write the target to write through it) |
| `edit` | `{path, old_string, new_string, replace_all?}` | `file_edit`'s exact-string replacement (the same rules, one shared implementation) on a sandbox file of up to 4 MiB, written back with `ifMatch` = the etag it read; a `precondition` refusal (the file changed meanwhile) is retried once from a fresh read. A symlink is followed as `read` follows it: the target is edited, the link stays. The result shows the changed lines, numbered |
| `ls` | `{path?}` | a directory (≤ 500 entries): subdirectories first, with `/`; files with their size; symlinks with their target |
| `glob` | `{pattern, path?}` | files by name, relative to the working directory, sorted, at most 200: `**` spans directories, a pattern without `/` matches names at any depth, `{a,b}` alternates. The listing is the sandbox's own `rg --files` (which honours `.gitignore`) or `find` (skipping `.git` and `node_modules`) — at most 20 000 files — matched here |
| `grep` | `{pattern, path?, glob?, ignore_case?}` | `path:line: text` lines, at most 100 (then how many more), text clipped at 300 characters: `rg` where the sandbox has it (its regex syntax), else `grep -rE`; skips `.git` and binary files |
| `sandbox_upload` | `{file, path?}` | copies a session file (text or attachment) into the sandbox: to `path`, into it when it ends in `/` or is a directory (default: the working directory). Feature `files` |
| `sandbox_download` | `{path, name?, keep_both?}` | copies a sandbox file (≤ 16 MiB) into the session files — text within the text cap as text, anything else as an attachment — under `name` or its own, **in place** (D136): an existing session file of that name becomes the next version, the replaced one kept (`file_diff`), text or binary either way. A file that hasn't changed writes nothing — `unchanged: … (sha …, v2)` — known by the etag the copy recorded (no read) or else by its sha256. `keep_both: true` is the old behaviour: a taken name gets a suffix (`-2`, `-3`…). A directory is refused: pack it with `bash` first. Feature `files` |
| `browser_check` | `{target, wait_ms? (1000, ≤ 20000), screenshots_ms? ([wait_ms], ≤ 4), viewport? {width, height}, script?}` | loads a page in a headless Chromium **inside the sandbox** and returns raw facts (below). A side effect when the sandbox has egress |
| `sandbox_copy` | `{from: {sandbox?, path}, to: {sandbox?, path}}` | between the conversation's attached sandboxes (a ref or a unique name; default the active one), or within one: a directory is tar-streamed (`GET …/tar` into `PUT …/tar`; both managers need `tar`) and its **contents** land in `to.path`; a file goes through the file routes (mode kept) to `to.path`, or into it when it is a directory. The source streams as the destination's request body, so a failure names the side that failed: `reading "a":/p failed: …` (the source's refusal, or its stream cut short — the destination may hold a partial copy) or `writing "b":/q failed: …`. Offered when more than one sandbox is attached |
| `sandbox_info` | `{}` | every attached sandbox as its manager describes it now (active or attached, state, egress, image, manager, cwd, workdir, home, user, caps, the manager's caps and whether a live preview works — or why it is unavailable) and the conversation's latest 15 jobs |

**`browser_check`** (D136). The target is a sandbox path (a directory:
its `index.html`), `session:<file>` (copied to
`~/.cache/xbin-browser/session/<run>/` in the sandbox first) or an `http(s)`
URL the sandbox reaches — `http://localhost:8080/` for a server started with
`bash background:true`. The runner (`_backend/browser_runner.mjs`, embedded)
is written to `~/.cache/xbin-browser/runner-<hash>.mjs` once per version and
run with the contract's `run` (`sh -c` finding `/usr/local/node/bin/node`,
else `node` on the PATH); it finds Playwright in the project
(`node_modules` up from the cwd), then the global installs
(`/usr/local/node/lib/node_modules`, …), and browsers in
`$PLAYWRIGHT_BROWSERS_PATH`, else `/usr/local/ms-playwright` (the xbin
rootfs). Its time budget is what the tool timeout leaves, at most 90 s (the
navigation at most 20 s; a script 15 s). The result is a headline —
`browser_check <url> — loaded in 12 ms · status 200 · 3 console message(s), 1
error(s) · 0 page error(s) · 2 failed request(s)` — then
`screenshots (shown to you after these tool results): shots/index-0ms.png`
and JSON (≤ 12 KiB, the snapshot and lists shrunk to fit): `url`, `title`,
`status`, `load {state, ms, error?}`, `console [{type, text, location,
at_ms}]` (≤ 60), `page_errors [{message, stack}]`, `requests_failed [{url,
method, resource, error | status, blocked?}]` — `blocked` names a request
the sandbox's egress refused (a network error for an outside host with
egress `none`, or for a private address with egress `internet`) —,
`snapshot` (Playwright's aria snapshot of `body`, ≤ 9000 characters),
`screenshots [{at_ms, file, bytes}]`, `script {value} | {error, stack}` (the
return value, JSON as it is, ≤ 16 KiB). Screenshots are PNG session files
`shots/<page>-<ms>ms.png` (in place: the next check of the page replaces them,
keeping the earlier version) and are shown to a vision model as `file_view`'s
are. Without the `files` feature none are taken. A sandbox without Node,
Playwright or a Chromium gets an error saying which, and how to install it.

**`sandbox_create`** `{name, manager?, image?, size?, egress?, cwd?}` — the
agent makes a sandbox for its conversation. It is offered to a top-level
conversation whose class has the `sandbox` toolset and allows a bound
manager, **with or without** a sandbox bound, and not to a chat channel's
conversation. It takes the conversation **owner's grant** `sandboxes`
(§Threads, schedules and grants): the step parks — `pendingState: {kind:
"approval", grant: "sandboxes", grantAsk, toolCalls}`, where `grantAsk` says
exactly what will be made (`create the coding sandbox "api-dev" at Coding
sandboxes — image base, size small, egress none`: the manager asked first,
its defaults resolved; the name quoted) — and only the owner may allow it,
once or for an hour (`POST /runs/{id}/approve {approve: true, grant:
"once"|"hour"}`; `DELETE /runs/{id}/grants/sandboxes` takes an hour's grant
back); anyone who may steer the conversation may deny it.

- **Defaults**: `manager` — the first bound manager the class allows (a
  provider, as `GET /sandboxes` names it); `egress` — the first the class
  allows that the manager offers, `none` first; `image` and `size` — the
  manager's defaults; `cwd` — the sandbox's workdir (a relative one is
  under it; a missing one is made — once the sandbox runs: a create the
  manager answers while it is still starting waits for it, up to 2 min).
- **As asked**: the call is resolved against its manager before it parks,
  and the parked calls the owner allows — once or for the hour — make
  exactly what `grantAsk` said, or refuse (saying what changed) when the
  manager's offer changed meanwhile. A call whose manager doesn't answer
  isn't parked on a guess: it runs and says why. `name` is 1–64 characters
  on one line, with no control or format characters.
- **What is made**: the sandbox is created for the owner — `Sbx-User` is the
  owner, who approved it — as `POST /sandboxes {conversation}` makes one (a
  team conversation's is `team`, its participants are members, it is
  labeled `xbin.agent/conversation`), and bound with `by` = the owner: the
  **active** sandbox when none is active, else attached beside it. The coding
  tools work in it from the **next step of the same turn**. The result names
  it, its ref, its workdir and egress, and whether it is now active.
- **Refused, never parked**: in a subagent, in a chat channel's
  conversation, in a conversation no person owns, after **4** creates in one
  conversation, and when the class doesn't allow the manager or the egress.
  An image, size or egress the manager doesn't offer is refused before
  anyone is asked; a binding that can't be made deletes the new sandbox
  again.
- **Idempotent**: each create is numbered per conversation
  (`sandbox_creates`) and sent with `clientId` `agent:<root>:name:<n>`. A
  call a restart cut off leaves its number pending; the next call with the
  same name reuses it, so the manager answers with the sandbox it already
  made. A sandbox of the same name this conversation made and still has
  attached is answered as already there.

**Jobs** are numbered per conversation (subagents share their root's
numbers) and kept in the `sandbox_jobs` table (`root_id, job, run_id,
tool_call_id, ref, exec_id, command, cwd, state, exit_code, read_off, fg,
created_ms, ended_ms, client_id, signal` — `client_id` set when it isn't
`agent:<run>:<call>`; `signal` the signal that ended it, `''` when none or
recorded before D134); a conversation runs at most **8** at once (asked of
the manager before a start is refused). **A sandbox deleted elsewhere** (by
an operator, another tile, or no longer shared with whoever bound it) comes
off the conversation the first time anything finds it gone — a tool call, a
live page, the popover: detached in one transaction, and when it was the
active one the first other attached sandbox becomes active (the sandbox
tools need one); the run event says so, and the refusal tells the model
what is active now (`…is gone … and was detached; the active sandbox is now
"b" from your next step (attached: "b", "c")`, or that none is bound: ask
the user, or `sandbox_create`). `sandbox_info` names the active one after.
**Detaching a sandbox** (`PATCH
/runs/{id} {detach}`, or deleting it, which detaches it everywhere) KILLs
the process group of every job the conversation still runs in it — best
effort, in the background, so the change doesn't wait for a manager — and
records them `killed`. A `bash` whose start is still in flight then is
`killed` too: when the start answers, its command gets a KILL at once and
the call fails with `job N was stopped as it started: its sandbox was
detached from this conversation…` (so for a job given up as `lost` while
its start was in flight). A job whose sandbox the conversation can no longer
use at all (detached, deleted, its manager unbound, no longer allowed) is
`lost` and doesn't count toward the 8; `bash_output` and `bash_kill` on it
say what is known of it. **Interrupting or cancelling** the
turn stops the command bash is following — TERM to its process group, KILL
if it is still there 3 s later — while background jobs keep running; the
call's result keeps what the command wrote until then, with a footer
`[interrupted by the owner · job 3 got TERM (then KILL, if it outlives a few
seconds) — bash_output {"job": 3} shows the rest and how it ended]`.
**Sleeping on jobs** (D134): `yield` — `{seconds?, until_job?}`, `until_job`
offered where a sandbox is bound — by a run with jobs of its own still
running sleeps as `{kind: "sleep", since}` (its `pendingState`) and wakes
early when one of them ends (`(yielded 600s — waking early when job 3
ends)`); `until_job: N` sleeps until job N ends (`seconds` then its most,
default 3600), and a job that has already ended answers at once without
sleeping. The wake is durable: every pass over the sleeping run (the poke a
recorded end gives the run that started the job, a restart's recovery) asks
`sandbox_jobs`; while nothing reads the jobs, the engine follows them at
their manager with a long-poll per job (never their output) as long as the
run sleeps. **A
backend restart** (a handoff to the next process) leaves it running: the
call's result becomes `(no result: the backend restarted while this command
ran. It went on in the sandbox as job 3 — bash_output {"job": 3} shows its
output from the start …)` instead of the generic lost-result text, and the
job's output resumes by offset. **Deleting the conversation** (`DELETE
/runs/{id}`) KILLs the process group of every job it still has running —
best effort, in the background, so the delete doesn't wait for a manager
(the sandboxes themselves stay; they are their owners'). **Archiving** it
leaves them running.

**Approve mode.** `bash`, `write`, `edit` and `sandbox_upload` are
side-effecting tools — the step parks for approval — only when the bound
sandbox has egress other than `none` (`sandbox_copy`: when any attached one
has); a sandbox with no network is private scratch.

### Live previews (D135)

**`preview_port {port, path?}`** — "Show the human a LIVE page served by a
program in your sandbox (e.g. python3 -m http.server 8000) — scripts run,
in an isolated frame." Offered with the other coding tools; it needs the
manager's `ports` capability (docs/sandbox-manager.md §Ports — an older
manager or xbind lacks it, and the tool says so and points at
`render_html`). The manager is asked again before that refusal (the agent
caches its hello for minutes; an updated manager is never refused on a
stale one — so is the `tar` check of `sandbox_copy`), and the refusal names
the manager, its version and what it offers. `sandbox_info` prints the
manager's caps beside the sandbox's and `live preview (preview_port):
available`, or `not available — <why>`. xbind's refusal for a sandbox whose
agent predates ports (started under an older xbind, or from an older VM
image) reaches the model verbatim, with what to do: have the sandbox
restarted, then preview_port again. It checks the binding as every coding tool does, asks the
page once through the manager (nothing listening is an error that says to
start the server as a background job, on 127.0.0.1 or 0.0.0.0) and records
a **`live` step** `{sandbox, name, port, path}`; the result states the
limits (relative URLs only; it stays live while the server runs).

**`ANY /runs/{id}/live/{sbx}/{port}/{path…}`** serves the page: only to the
run's **participants** (a viewer gets 403, anyone who can't see the run
404), for a sandbox bound or attached to that run (`{sbx}` its id at the
manager), re-checked as `sandboxUse` checks a tool call (cached 5 s), and
proxied to the manager's ports route as the person who bound it. It is a
path-prefix proxy: `{path…}` and the query go on unchanged, relative URLs
resolve below the prefix, nothing is rewritten; a `.`/`..` segment is 400.
Every answer carries this tile's headers, never the sandbox's:
`Content-Security-Policy: sandbox allow-scripts allow-forms` (an opaque
origin even opened directly — no cookies, no storage, no reach into this
tile or xbind; no `frame-ancestors`, because the pane framing it is itself
an opaque origin that no source expression matches), `Referrer-Policy:
no-referrer`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`;
of the sandbox server's headers only content ones pass (type, length,
encoding, range, ETag, Last-Modified, Vary, Location, …) — never
`Set-Cookie`, `Clear-Site-Data`, NEL/`Report-To`, HSTS, `Alt-Svc`, CORS,
`WWW-Authenticate` or `X-XBin-*`. The viewer's cookies, credentials,
identity headers, `Referer` and forwarding headers never reach the sandbox.

**Diagnostics** (participants, as the live route; never the page's body):

| Route | Answers |
|---|---|
| `GET /runs/{id}/ports` | `{previews: [{sandbox, name, port, path, run, at, ok, status?, contentType?, refusal?, error?, ms}]}` — the conversation tree's live previews (its `live` steps), newest first, at most 8 distinct, each probed now as its binder reaches it (the live route's checks, then the manager's ports route) |
| `GET /runs/{id}/ports/{sbx}/{port}?path=` | one such probe of any port of a sandbox bound to the run (`{sbx}` its id at the manager) |

`refusal` is the contract's (`not-listening`, `state`, `unsupported` — a
sandbox started before its runtime served ports: restart it), the agent's
(`not-attached`, `not-allowed`, `unbound`, …) or `invalid`. The live route's
own refusals now carry `refusal` beside `error` too.

**The pane.** A new `live` step opens the render pane (as a render does;
one you closed stays closed; the chat's `📡 showing …` line opens it again)
on the page, labelled **● live from the
sandbox — name:port/path**, with **↻ Reload**: an `<iframe
sandbox="allow-scripts allow-forms" credentialless
referrerpolicy="no-referrer">` — never `allow-same-origin` — whose URL is
below an xbind **path ticket** (docs/auth.md §Path tickets) the pane mints
for `runs/{id}/live/{sbx}/{port}` (`model/live.js`): the frame holds no
token or cookie, so the credential rides in the path and reaches that prefix
only. The native view says what is live and that it opens on the web: its
only WebView island (`canvas src=`) is a tile WebView with the tile's
bridge and frame token, never for an untrusted page — its live screen has
**Check** (the probe route above). On the web a **status strip** under the
pane's header checks the page's own URL (`/api/~<ticket>/…`, a plain fetch —
the ticket rides in it) as the frame loads, and again on **Check**: the HTTP
status and type, or the refusal and what to do about it — nothing listening
(start the server), the sandbox stopped, an agent from before ports
(restart the sandbox), xbind's 401s (the link expired or the sign-in ended:
↻ Reload; the link works only from where you signed in within the hour),
the tile-origin 403. A page that answers an error is said there, with the
frame hidden, never shown blank. The ▣ popover's **Ports** section lists
the previews with their probes and **Open**, and probes any port of the
active sandbox. `test/live-policy.mjs` drives a hostile page through the real
frame in Chromium (and the strip's Check on an answer, a `not-listening`
and an expired link); its scripts run, and it gets no cookie, no storage, no
identity from `/api/xbin/whoami` or this tile's API, no parent or top
document, no top navigation or pop-up, and its `postMessage` changes
nothing.

### In the UI

The model is `model/sandboxes.js` (pure: what the controls say) and
`app.sbx` (`model/sandbox-store.js`: the list, the next new chat's pick and
the calls); the web draws it in `sandboxes.js`, the native view in
`native/sandboxes.js`.

- **The composer's picker** (`#ssel`, beside the model's) shows where the
  class — the open conversation's, or at home the next new chat's — has the
  `sandbox` toolset: no sandbox, then This conversation (what it has
  attached or bound) · Yours · Shared · Team, then ＋ New sandbox… and
  Manage sandboxes…. One you may not use, or that the class does not allow
  (its manager, its egress — the less restrictive of `egress` and
  `egressNext` — or, for a class that reaches outside, the internal data it
  has held), is listed disabled with the reason. A pick
  binds it (`PATCH /runs/{root} {sandbox: {ref}}`, from the next turn) — one
  the conversation has attached is sent with the `cwd` it had there; a
  private one going into a conversation other people are in (shared with
  the team or with people) is confirmed first: they will be able to work in
  it. At home it goes with the new chat (`POST /ask {sandbox}`) while the
  ask's class has the toolset; once the list is read, a pick it no longer
  has says why (gone, its manager unbound or down). An ask refused for its
  sandbox keeps the typed message, drops the pick and says why — the next
  new chat goes without one until another is picked. New sandbox is not
  offered in a conversation you may only read.
- **The ▣ badge** in the top bar: the active sandbox and its `cwd` — or,
  marked, why the binding no longer resolves (its class no longer allows it,
  its manager is unbound or unavailable, its manager no longer has it). Its
  popover sets the working directory (absolute; empty is the sandbox's
  workdir), makes another attached sandbox the active one, detaches the
  active one (`{detach}`) and opens Manage. A coding agent's conversation
  keeps the sandbox and directory it started in (the backend refuses a
  change): its popover shows the working directory read-only, with no
  switch or Detach, and a binding that no longer resolves says to start a
  new chat with the coding agent in another sandbox.
- **The Sandboxes dialog** (`#sbxdlg`): every sandbox you may see, yours
  first — state, manager, image, size, egress (and the one it takes at its
  next start, when a change waits for it), owner, private/team, when it
  was last active, how many conversations have it — with **Use here** (or
  for a new chat; confirmed as the picker confirms it), Start / Stop / Thaw
  (who may use or manage it — and, for one the open conversation holds that
  you may neither use nor manage, anyone who may talk in it: through the
  conversation, `?conversation=`, as the one who bound it), Archive (who
  may manage it, where its manager archives), Share with the team / Make
  private (its owner), **Share with a terminal tile…** and Delete (who may
  manage it, confirmed). The rows
  keep their order while it is open (a Start doesn't move one under the
  cursor); new ones come after. **New sandbox**: the manager, a name, its
  image and size, the network (the class's `sandboxEgress` only), who may
  use it, a working directory — in a conversation it is made for it and
  bound there (`POST /sandboxes {conversation}`; not offered when you may
  only read it), at home it becomes the new chat's.
- **Share with a terminal tile…** (D121) on a sandbox you own whose home
  is this agent (not one another consumer shared with it: only a sandbox's
  home changes its shares): the terminal tile's path — the builtin
  `sandbox-terminal`'s `apps/sandbox-terminal` by default — and who the
  share is for: you (joining whoever that tile's share already names), or
  everyone who may use it when it is a team sandbox (`"*"`). Share sends
  `PATCH /sandboxes/{ref} {shares, version}`: the sandbox's shares with
  that tile's replaced, the others kept, at the `version` they were read
  at — when someone changed the sandbox since (412 `precondition`), it is
  read again (`GET /sandboxes/{ref}`) and the list sent once more from
  what it holds now, so their change isn't overwritten. The form lists the
  shares it has, each with **Stop sharing** (confirmed; sent the same way). The tile applies the person rules too, so
  a share never widens who may use the sandbox; a row says who it is
  shared with.
- **A terminal** where the sandbox's manager offers one (`tty` in its
  hello): **Open terminal** in the ▣ popover (the active sandbox, at the
  conversation's working directory) and **Terminal** on a Sandboxes row (at
  the working directory the open conversation has it at, else its
  workdir). The pane (`#sbxterm`, over the chat — not a modal: Escape goes
  to the shell) holds `<bx-terminal src>` on the manager's
  `…/sbx/sandboxes/{id}/tty?cwd=`, which the page dials itself, through
  xbind with its frame token (`xbin.iface('sandboxes')`: the endpoints of
  the slot). So the manager sees the verified person and applies its own
  rules to them: offered only for a sandbox you may use yourself (not one
  a conversation holds for someone else), running or able to start (an
  archived one says to thaw it). ⤢ makes it larger; when the shell exits it
  says so and offers **New shell**; **✕** ends the shell (`DELETE
  …/execs/{id}` at the manager, from the page). Each terminal is a tab of
  one dock (`terminals.js`): **＋** opens another shell in the same sandbox,
  a tab's ✕ ends that one, **▾** hides the dock with its shells still
  running (a top-bar pill, "2 terminals" — a button: click, Enter or
  Space — brings it back, the shown shell taking the keys), and the tabs
  stay open while you switch conversations. One left by a page that closed
  runs on until its manager ends it.
- **Keeping current.** After a change the conversation's binding is read
  again (`GET /runs/{id}/view?limit=1` → `config`); a `run` event that
  carries `sandbox` (and `attached`, a count) updates it at once, and a
  changed count reads the binding again. When the classes change (a
  manager's save here, or `GET /classes` read afresh) the open
  conversation's `class` is read again too — its badge, the mixed warning
  and what its sandboxes may be follow the edit, as the backend applies it
  from the conversation's next step.
- **Tool cards**: the coding tools are the ▣ family. A card shows the call's
  own words (the command, `old → new`, the pattern) under the model's
  summary, and what it came to, read from the result: bash's footer
  (`exit 1 · 14s · job 3`, `still running · 2m00s · job 3`, `job 3
  started`), match, file and entry counts, lines read, sizes
  (`model/tool-heads.js` `subline`, `outcome`; the fold's blocks carry them
  as `sub` and `outcome`).
- **In the native view** the picker is a Sandbox picker in the chat and
  home toolbars, beside the model's, with short labels (a picker cannot
  disable an option: one you may not use is marked, and picking it says
  why; a private one into a conversation other people are in asks in a
  sheet first). The ▣ badge is in the conversation's subtitle, a notice in the
  transcript says why a binding no longer resolves, and ⋯ → Sandbox pushes
  the popover's screen (working directory, the attached ones, Detach,
  Manage sandboxes… — a coding agent's: its working directory read-only
  and Manage only). The Sandboxes screen puts each row's actions behind
  its swipe and ⋯ (Archive and Delete confirmed), New sandbox pushes
  the create form, and Share with a terminal tile… pushes its form (Stop
  sharing behind a share's swipe, confirmed). A ▣ tool card is a `terminal` icon; what the call came to
  is a chip (`exit 1 · 14s · job 3` in red), its command the card's first
  line. A row's **Terminal** and the Sandbox screen's **Open terminal**
  push a Terminal screen: the app's `terminal` dials only the tile's own
  routes, so it goes through the agent's relay (`GET
  /sandboxes/{ref}/terminal?cwd=`, D147 §4.2.8), which checks that you
  may use the sandbox and dials its manager's `tty` as you. One at a time
  (the app's terminal closes its socket when its screen goes — and the
  relay then ends the shell it started, §Coding agents "Terminal relays").

**Subagents on another sandbox.** `subagent_spawn` also takes `{sandbox?,
cwd?}` where a sandbox is bound: `sandbox` names one of the conversation's
attached sandboxes (a ref or a unique name; any other is refused), which
becomes the subagent's active one; `cwd` is its working directory there
(absolute, or relative to that sandbox's). The subagent keeps the attached
list, and the root's binding is unchanged.

## Coding agents (harnesses)

A conversation — or a subagent the agent spawns — can be answered by a
**coding agent** (a *harness*: Claude Code, Codex, Gemini CLI, OpenCode, or
another the sandbox manager names) run over ACP inside a coding sandbox,
instead of the agent's own loop: the catalog, each person's setting, the
class gates, and the engine that starts a coding agent in its sandbox and
turns what it does into the conversation (below, "Driving one"). Its own
routes — the mode and options, answering its questions, signing it in —
are "Its own routes", its terminals and log "Terminal relays and the log";
the UI that uses them follows each. The design, and what was not chosen,
is D147.

- **Runs.** A run's `engine` is `""` (the agent's own loop) or `"harness"`
  — set when it is made, never changed; `GET /runs/{id}`'s `run`, run
  events, the view's `run`, conversation rows and a link's `child` carry it.
  A harness run's config holds `engine` and `harness: {provider, mode?,
  options?, ref, cwd?, by?}` (the coding agent, the sandbox it works in —
  fixed for the conversation — and who started it).
- **Sandbox bindings** carry `harnesses`: the coding agents the manager says
  the sandbox's image has (`hello.images[].harnesses`) when it was bound —
  absent in one bound before, or from a manager that says nothing (unknown,
  not none: a probe decides).
- **Classes.** The `harness` toolset and the `harnesses` field (§Agent
  classes); the built-in `coding` has both (`"all"`). A coding-agent
  conversation or spawn needs a class that has the toolset, allows the
  coding agent, and allows a sandbox with an egress other than `none`.

| Method & path | Who | Body / query | Answer |
|---|---|---|---|
| `GET /harnesses` | anyone who can use the tile | `?probe=<ref>` | `{harnesses: [entry…], probe?}` — below. Managers' hellos come from their cache; answers in ≤ 10 s |
| `GET /prefs/harness-mode` | a person | — | `{modes: {"<id>": "auto"\|"approve"}}` — the caller's own settings (set ones only) |
| `PUT /prefs/harness-mode/{id}` | a person | `{mode: "auto"\|"approve"}` | `{provider, mode}`. **400** `mode is "auto" or "approve"`; **400** `no coding agent "<id>"`; **400** `<name> has no auto mode — it asks as its own settings say` (`auto` for one whose `autoMode` is empty); **403** `the setting is a person's own` for anyone else — an element, the scheduler, the tile itself, an admin viewing as someone |

**The catalog.** One entry per coding agent any bound sandbox manager
advertises, after the SDK catalog's four (`claude`, `codex`, `gemini`,
`opencode`), which are always listed:

```jsonc
{"id": "claude", "name": "Claude Code",
 "available": false, "reason": "no-egress",
 "why": "needs internet access — Coding sandboxes offers none (bind its internet class)",
 "classes": ["coding"],                       // the caller's classes that allow it, their default first
 "images": [{"provider": "apps/coding-sandbox", "manager": "Coding sandboxes", "image": "base",
             "advertised": true, "egress": ["internet"]}],
 "modes": [{"id": "default", "name": "Ask before acting"}, {"id": "bypassPermissions", "name": "Bypass permissions", "explicit": true}],
 "defaultMode": "default", "autoMode": "acceptEdits", "approveMode": "default", "planMode": "plan",
 "setting": "approve",                        // the caller's own (below)
 "login": {"command": "claude auth login", "guided": true, "mint": true}, // its terminal sign-in; guided: the guided sign-in (D179), mint: its Remember (a person's partition)
 "options": [ /* the config options its last session reported, any conversation — absent before one */ ],
 "sandboxes": {"apps/coding-sandbox|sb-7f3a": {"installed": true, "signedIn": false, "at": 1790000100000}}}
```

- `images`: every (manager, image) that advertises it (`advertised:
  true`), and every image of a manager whose hello predates
  `images[].harnesses` (none of its images says — `advertised: false`: its
  sandboxes are built on a rootfs that has the four, and a probe decides),
  each with the manager's egress other than `none`. An advertised entry
  that isn't one of the four (and isn't `fake`, the test fixture) needs its
  own `argv`, or it is ignored.
- `available`, `reason`, `why` — the first that holds: `no-image` (no bound
  manager's image has it; `manager-error` instead, `why` its error, when a
  manager that didn't answer might), `no-class` (no class you may use
  allows coding agents — or this one), `no-egress` (no manager offering it
  offers an egress other than `none` that such a class allows); at a
  partitioned agent's global instance every one is `shared-space` (coding
  agents work only in a person's own conversations: §Partitioned
  instances).
- `modes`, `defaultMode`, `autoMode` (empty: it has none), `approveMode`,
  `planMode`: the SDK catalog's (`sdk/acp`); the name and login command are
  the catalog's, else the manager's advertisement. A manager's own `argv`
  for one of the four is what runs, and what a probe looks for.
- `sandboxes`: what the agent last learned about it per sandbox — by a probe
  (`installed`) or a session (`signedIn`); a field absent is unknown, a
  sandbox absent never asked. Only sandboxes the caller may see (in a
  person's partition, only those homed there).
- **`?probe=<ref>`** also asks that sandbox now which of the coding agents'
  commands it has (`command -v`, one run of at most 8 s through the
  contract's `/run`): the ones its image advertises, or the four when its
  manager says nothing. Only a running sandbox the caller may use — a stopped
  one isn't started — and at most once in 10 minutes per sandbox. The answer
  says what it did: `probe: {ref, ran, cached?, error?}` (`error`: why it
  didn't ask — `no such sandbox`, `the sandbox is stopped — a probe doesn't
  start it`, …); what it learned is in each entry's `sandboxes[ref]`. A bad
  reference is **400**.

**Auto / Always approve** is each person's own setting per coding agent,
kept by the agent (not in xbind's prefs — the agent reads the conversation
owner's when it spawns a coding agent for them): `auto` is the agent's
auto-edit mode (`autoMode`: claude `acceptEdits`, codex `agent`, gemini
`autoEdit`), `approve` — also what unset means — its ask-first mode
(`approveMode`). It applies when a coding-agent conversation or child is
created; one that exists keeps its mode. Explicit modes (bypass, full
access) are never a setting.

**Driving one (the API).** `POST /ask` and `POST /runs` take `harness:
{provider, mode?, options?}` with a `sandbox: {ref, cwd?}` (`POST /runs`
gains `sandbox` as `/ask` has it, bound as you): the run is made with
`engine: "harness"` and its first message queued as the coding agent's
first prompt. `class` is optional (yours when it allows the coding agent,
else the first class you may use that does). `mode` is a mode of the
coding agent's (`GET /harnesses` `modes`) — absent, your Auto / Always
approve — and an explicit one only from a person (every mode the catalog
doesn't know to be safe: below, "The mode and options"); `options` are its
config options (`{"model": "…"}`) and never carry the mode (one the running
coding agent reports as its mode option is skipped and dropped, with a
note). The sandbox is fixed for the conversation. **400** `harness.provider: no coding agent "x" (GET
/harnesses lists them)`, `class: the <class> class doesn't allow <name>`,
`harness.mode: one of …`, `harness.options: the mode is harness.mode`,
`system: a coding agent keeps its own instructions — system is for the
built-in agent`, `model: a coding agent's model is harness.options.model`,
`a coding agent needs a sandbox: sandbox {ref, cwd?} whose image has
<name>`; **403** `no class you may use allows <name>`, `only a person can
start <name> in <mode>`; **409** `<sandbox>'s image doesn't have <name>`,
`<name> must reach its provider — <sandbox>'s egress is none`, `<sandbox>
doesn't have <name> (<command> not found)` (a running sandbox is probed);
the binding's own refusals as for any sandbox. `hold`, `draft`, `files` and
`title` work as for any ask.

- **Messages.** `POST /runs/{id}/message` (and `/answer`) on a harness run
  queue a prompt (inbox kind `hprompt`; `queued`, `DELETE
  /runs/{id}/inbox/{iid}` and `/interrupt`'s `returned` include them like
  messages). It goes to the coding agent when no turn runs and nothing
  waits for you. During a turn, a coding agent that takes messages
  mid-turn (`harness.steering`: claude, codex) gets it at its next step —
  its user row is written then and the turn goes on; one that doesn't gets
  it as the next prompt once the turn ends. A steer is never sent twice:
  one whose answer is lost (a save or restart of the agent as it went, the
  connection to the sandbox dropping, no answer within 30 s) may have
  joined the turn — its user row is written with a note that the coding
  agent may not have received it (send it again if it doesn't act on
  it). A person's message sent while a permission or a question waits
  answers that first — the permission rejected (reject once, else always,
  else the cancelled outcome), the question declined — then is steered or
  waits (the parent agent's message waits for the person instead: "The
  agent's coding agents"); while the coding agent waits for a sign-in it
  waits with it. `interrupt: true` stops the running turn first and goes
  next (ignored on the agent's own runs). Messages from schedules,
  triggers, watchers or `/learn` never drive a coding agent: they are
  consumed with a note.
- **A turn.** The message becomes the user row as it is sent; what the
  coding agent writes streams as the run's draft (`text`/`thinking` events,
  the view's `drafts`) and lands as assistant rows (thinking in
  `reasoning`). **Each call it makes is one assistant row and one tool
  row:** `toolCalls: [{id: "h<gen>:<id>", function: {name: "acp:<kind>",
  arguments}}]` (an agent that uses an id again for a new call once the
  earlier one ended gets `h<gen>:<id>#<n>` for its n-th — the earlier
  call's rows stay as they were; `kind`: `read`, `edit`, `delete`, `move`, `search`,
  `execute`, `think`, `fetch`, `switch_mode`, `other`; `arguments`: the
  call's input plus `summary`), the tool row `(running…)` until the call
  ends, then its result (an edit's `edited <path> (+a −d)`, a command's
  last 8 KiB and `[exit N]`, `error: …`, `(cancelled)`). The tool row
  carries **`acp`**: `{kind, title, label?, tool?, status, parent?,
  subagent, planReview, locations?, files?, exitCode?, output?,
  outputTruncated?, diffs?: [{path, status, add, del, patch, truncated}]}`
  (a patch is a unified diff with 3 lines of context, ≤ 64 KiB a file); a
  command's output streams into it as message upserts at most every 250
  ms. Text of the coding agent's own subagents (claude's Task) is an
  assistant row with `acp: {parent}`. The turn ends `idle` (a stop reason
  other than the end of the answer is said in a note), or `error` with why;
  a subagent's link settles with its last text. The task ledger records
  each prompt as it is sent.
- **Its summary.** A harness run's summary (run events, the view's `run`,
  conversation rows, a link's `child`) carries **`harness`**: `{provider,
  name, state (stopped | starting | ready | working | login | lost |
  failed), error, mode: {current, available}, options, commands, usage?,
  plan?, activity?: {kind: idle | thinking | writing | tool | waiting,
  title?, at}, counts: {tools, files, add, del}, pending?: {park, kind,
  title}, login?, sandbox: {ref, name, cwd, shared}, steering, title,
  gen}`; `mode` is the coding agent's session modes, else its config
  option of category `mode` (opencode speaks its build/plan agents only
  that way), else the catalog's; `explicit` on one of `mode.available`
  marks it the conversation owner's (below). The stream's **`harness`**
  event (`data`: the whole object, coalesced per run) says when it changes
  between run events. The conversation's title follows the coding agent's own while it
  is the first message clipped. An agent-loop run has no `harness`.
- **Waiting for you.** A permission request parks the run (`waiting_input`,
  the call's row `(awaiting your approval)`): `pendingState: {kind:
  "approval", park, toolCalls, harness: {callId, options: [{optionId,
  name, kind, explicit?}], tool: {title, kind, name?, label?, command?,
  rawInput?, content?}, rule?, defaultToNo?, description?, planApproval?,
  plan?}}`. `POST /runs/{id}/approve {approve?, park?, option?,
  feedback?}` answers it: `option` (an `optionId`) picks the coding
  agent's own answer — an `allow_always` one is remembered for the
  conversation (the same kind and title isn't asked again, even by a
  restarted coding agent; a mode switch's never is). Without it `approve:
  true` picks allow once, else another allow that doesn't raise the mode
  (**400** `option: name one — every allow here raises <name> to an
  explicit mode` when every allow does), `approve: false` rejects (once,
  else always, else the cancelled outcome). An option marked `explicit`
  (an allow that switches the session to a mode that is the owner's —
  "The mode and options" below — by the mode it names, Claude Code's
  `exit-plan-bypass` included; on a mode switch such as a plan approval,
  also any `allow_always` whose mode isn't known) is the conversation owner's:
  **403** `only <owner> can allow <option>`. `feedback` (a plan approval's
  "keep planning") goes with a rejection only (**400** `feedback goes with
  a rejection`): the rejection is answered, then `feedback` is your next
  message. **400** `option: one of …`; on the agent's own approval **400**
  `option is for a coding agent's permission request`; `grant` is ignored
  on a coding agent's. A question (a form) parks `pendingState: {kind:
  "question", park, harness: {eid, callId?, message, schema}}`, answered
  by `POST /runs/{id}/harness/answer {park, action, content?}` (an
  `hanswer` inbox row `{park, action: accept | decline | cancel,
  content}`). One that comes while another waits is queued behind it and
  becomes the park once that one is answered.
- **Signing in.** A coding agent that says it is signed out — or refuses a
  message so — parks the run on `pendingState: {kind: "login", park,
  harness: {login}}` (`waiting_input`; `harness.state` `login`,
  `harness.login: {command, methods: [{id, name, kind: terminal | api-key
  | device-code}], device?: {by}}`) and keeps the message that failed. **Retry**
  (`/resume`) ends the signed-out coding agent, starts a fresh one — it
  reads what a terminal sign-in left in the sandbox's home — and sends the
  message again (signed out still, it parks again). Signing in through the
  coding agent (an API key, a device code) is `POST /runs/{id}/harness/
  authenticate`: the key goes to the coding agent once and is never
  stored; a device code's page and code are yours alone — the answer, and
  your `GET /runs/{id}/harness` — while everyone sees who is signing in
  (`harness.login.device: {by}`); it stays up until you finish, then the
  run goes on by itself — across a save or restart of the agent too: the
  next process takes the coding agent's word that the sign-in is done and
  starts it afresh, as Retry does; a code no process waits on any more is
  taken away. A page the
  coding agent asks to have opened at any other time is declined.
  **The guided sign-in** (D179; `harness.login` of `GET /harnesses` says
  `guided: true` — Claude Code, and the test fake) runs the coding agent's
  own sign-in CLI in its sandbox — Claude Code's `claude auth login
  --claudeai` — and hands you its link and takes the code its page shows:
  no terminal (`authenticate {method: "guided"}`, below). **Saved
  sign-ins** — in a person's own partition only — are your own
  credentials for a coding agent's CLI, named ("Personal", "Work"), one
  per coding agent your default, kept in your partition's vault and handed
  to the CLI in its environment where the gate allows (below); `Remember
  for my other sandboxes` on the guided sign-in mints one with Claude
  Code's own `claude setup-token`.
- **Stops and restarts.** `/interrupt` stops the turn: a permission or
  question waiting settles `(interrupted)` and the coding agent ends its
  turn (`idle`) — one that doesn't within 15 s is stopped. `/cancel` and
  deleting the conversation stop the coding agent too (a subagent's link
  settles `canceled`); `/cancel` on a conversation that rests with its
  coding agent idle (`ready`) stops the coding agent (`stopped`, a note;
  `cancelled` lists the run) and leaves the status as it was — there was no
  turn to cancel; the next message starts it again. `/resume` starts a
  fresh one when none runs and sends again a message that couldn't reach
  it. `/compact` sends `/compact`
  to a coding agent that offers it (`harness.commands`). A coding agent
  idle between turns keeps running for the tile's `harnessIdleMin`
  (default 15 minutes; 0: until stopped) and is then stopped
  (`harness.state` `stopped`) — never while a turn runs or something waits
  for you; the next message starts it again, reopening its session when it
  can. One that exited or was cut off (its sandbox stopped, xbind
  restarted, its output lost mid-turn) ends its turn with why, a request
  waiting settles `(interrupted)`, and the next message starts a new one
  (reopening its earlier session when it can). The conversation's use of
  the sandbox is checked again before each message, answer and steer, and
  at most once a minute while the coding agent works: when it may no
  longer use the sandbox — or the sandbox is detached from the
  conversation — the coding agent is stopped (`failed`, with why) and a
  turn in flight ends with it. A message that may not have reached it (the agent saved
  mid-send, the connection to the sandbox dropped as it went) fails —
  "send it again" — and is never sent twice. A save or restart of the
  agent never stops one: the next process takes it over where the last one
  left it, mid-turn, parked on its sign-in or in a turn of its own too
  (trying again — 2 s, doubling to a minute — while its sandbox manager
  doesn't answer; stopping it, `failed` with why, and ending its turn when
  the conversation may no longer use the sandbox; an answer you gave that
  was still on its way is sent again, a question it had just asked is
  asked again) — and a process that exits with one running, idle too,
  and none to follow leaves the resume job (§The engine, "Resume job"),
  whose next process takes it over and stops it once it has been idle for
  `harnessIdleMin` (in a person's partition a `wake` job at that minute
  instead, and an idle one doesn't keep the partition running: §Partitioned
  instances). **Rolling back** to an agent from before coding agents
  (v0.3.64 or older): its model loop never answers a coding agent's
  conversation — every turn it would start there ends at once at its step
  cap ("stopped after 500 steps in one turn (maxTurnSteps)"; the
  conversation's `turnSteps` is kept at that ceiling) — but it leaves the
  messages queued for the coding agent waiting, reads calls in flight as
  interrupted, answers an approval there with an error result, and the
  coding agent itself runs on unwatched in the sandbox until the sandbox
  stops or a newer agent takes it over again. What waits there for the
  coding agent — a message queued for it, an answer to its question — is
  work to that agent that it never does: its resume job wakes the tile
  every minute until the conversation is deleted (or, for the workspace
  owner, `DELETE FROM inbox WHERE kind IN ('hprompt','hanswer') AND
  delivered_at=0` in the agent's `db` resource). Its class editor still
  saves (the stored classes keep the `harness` toolset apart from
  `toolsets`, where that agent would refuse it), but any class save there
  rewrites every stored class (its editor sends them all), so each one
  loses its coding agents: tick them again after upgrading (Reset brings
  the built-in Coding class back as it ships).

**Its own routes** (D147 §4.2.4–§4.2.6). On a run the agent's own
loop answers they are **409** `not a coding-agent conversation`.

| Method & path | Who | Body | Answer |
|---|---|---|---|
| `GET /runs/{id}/harness` | a viewer | — | `{harness, session: {gen, execId, acpSessionId, loadable, steering, startedAt, lastActive}, rules: [{kind, title}]}` — its summary, the adapter process (`startedAt`: its current generation's start, ms) and what "allow always" answers remember in this conversation; to the person who started a device-code sign-in that waits, its `harness.login.device` is `{url, message, by}` (everyone else's, and every other view's, only `{by}`) |
| `PATCH /runs/{id}/harness` | a participant; an explicit mode: the owner | `{mode?, option?: {id, value}}` | `{harness}` |
| `POST /runs/{id}/harness/answer` | a participant | `{park?, action: accept\|decline\|cancel, content?}` | `{ok: "true"}` |
| `POST /runs/{id}/harness/authenticate` | a participant who may use its sandbox | `{method, apiKey?, confirm?}`; `method: "guided"`: `{code?, remember?, name?, confirm?}` | **200** `{ok: "true", state: "ready"}` (guided with Remember: also `saved`, the saved sign-in's row) · **202** `{ok: "true", device: {url, message}}` · guided: **202** `{ok: "true", signin: {url, paste}}` |
| `PUT /runs/{id}/harness/signin` | the conversation's owner, in their own partition | `{signin: "default" \| "sandbox" \| ‹id›}` | `{harness}` — the saved sign-in it uses, switched (D179) |

- **The mode and options.** `PATCH` switches the coding agent's mode (one
  of `harness.mode.available`) and/or one of its config options (one of
  `harness.options` — before its first session, one it last reported, as
  `GET /harnesses` lists them — and `value` one of that option's values).
  On a running coding agent it asks the agent now (`session/set_mode`, or
  its config option of category `mode` when it speaks one;
  `session/set_config_option`); either way the choice is kept in the
  conversation's `config.harness` (`mode`, `options`) for its next start —
  a stopped one's summary shows it as current. The change reaches the
  stream as a `harness` event. A mode the coding agent took with an option
  it refused is kept, and the answer is the option's refusal. **Bypass
  modes are default-deny**: a mode is anyone's (who may talk in the
  conversation) only when the SDK catalog knows it never takes the coding
  agent past its own asks (`acp.Provider.Safe`: claude's `default`,
  `acceptEdits`, `plan`, `auto`; codex's `read-only`, `agent`; gemini's
  `default`, `autoEdit`, `plan`; opencode's `build`, `plan`), or it is the
  mode the coding agent opened its first session in by itself; every other
  — bypass and full access, a mode a newer version of it adds, any other
  mode of one the catalog doesn't know — is `explicit` in `mode.available`
  and the conversation owner's, a person's (to switch to here, to start in
  with `POST /ask`, or to allow as a permission's option).
  **400** `mode or option: name one`, `mode: one of …`, `option: one of …`,
  `value: one of …`; **403** `only ‹owner› can switch ‹name› to ‹mode›`;
  **502** `{error}`: the coding agent refused, in its words; **504**
  `‹name› didn't answer` (30 s); **503** with `Retry-After: 1` while another
  process of the agent holds the session (a redeploy's handoff) — try again.
- **Answering its question.** `answer` answers the question the run is
  parked on (`pendingState.kind == "question"`): `accept` with `content`,
  the form's values (an object; its keys the schema's properties),
  `decline` or `cancel`. `park` names the question it answers (the one
  pending now when absent). It is queued (an `hanswer` inbox row) and
  delivered at once. **400** `no pending question`, `action is accept,
  decline or cancel`, `content: an object with the form's fields`; **409**
  `that question is no longer pending — the agent is asking something else
  now`.
- **Signing it in.** `authenticate` signs a coding agent that waits for a
  sign-in (`harness.state` `login`) in through the agent itself: `method`
  one of `harness.login.methods` of kind `api-key` (`apiKey` needed — it
  goes to the coding agent once, in the one call, and is never stored,
  logged or echoed; within 30 s the answer is 200 and the message that
  waited goes) or `device-code` (the page and code within 30 s: 202; the
  run goes on by itself once you finish). The page and code are yours
  alone — never stored, and in no summary but your own `GET
  /runs/{id}/harness` (anyone else who saw them could enter the code
  first, signing the coding agent in as themselves); everyone sees
  `harness.login.device: {by}`, and asking again for the device code of
  the sign-in you started answers it again (202). Only
  a person who may use the sandbox **themself** — asked of its manager now
  (the manager doesn't police the person this agent names, except in a
  person's partition, where it verifies them) — and, on a
  sandbox others may use too (team visibility, members or shares: they act
  as you with the coding agent there, its credentials living in the
  sandbox's HOME), with `confirm: true`. **400** `method: one of …`,
  `apiKey: needed for ‹method›`, `apiKey: only for an API-key method`;
  **403** `only someone who may use ‹sandbox› can sign it in` (also an
  element, the scheduler, an admin viewing as someone); **409** `‹name› is
  signed in`, `a sign-in to ‹name› is already under way`, and `{error:
  "anyone who may use ‹sandbox› acts as you with ‹name› there — confirm to
  sign in", confirm: true}`; **502** `{error}` in the coding agent's words;
  **504** `‹name› didn't start its sign-in`; **503** as `PATCH`. The key
  rides `_meta["api-key"]` in the shape the coding agent reads: Gemini
  CLI takes the key itself there (a string), codex-acp an object
  `{apiKey}` (an object sent to Gemini CLI read as no key: fixed in
  D179).
- **The guided sign-in** (`method: "guided"`, D179) — for a coding agent
  whose catalog entry says `login.guided` (Claude Code's `Signin` in
  sdk/acp: `claude auth login --claudeai`; the test fake signs in the same
  way, through a `claude` on the sandbox's `PATH`). Without `code` it
  starts the CLI as an exec in the run's sandbox (stdin open; a terminal
  one at 1000 columns, so nothing wraps), reads what it prints
  (`acp.Signin.Scan`: an OSC 8 link's target first, else a URL rejoined
  from its rows, only `https` on the provider's exact hosts at its sign-in
  path, no user part) and answers **202**
  `{ok, signin: {url, paste}}` — `paste`: it asks for the code — to you
  alone: another person's start or code while yours waits is **409**
  `a sign-in to ‹name› is already under way` / `no sign-in of yours is
  under way here — start one`, and your own start again answers yours
  again. With `code` it writes the code and Enter and waits (60 s) for the
  CLI's word: signed in (its `Login successful.`, or exit 0) → **200**
  `{ok, state: "ready"}` and the run's Retry (an inbox wake: a fresh
  adapter reads the new sign-in, the held message goes again); a
  malformed code (`Invalid code`, the CLI asking again) → **409** `‹name›
  says that isn't the whole code — copy it again …`, the link standing;
  any other end → **502** with the CLI's reason line (`Login failed:
  Request failed with status code 400`), or `‹name›'s sign-in ended
  without a link: ‹its last line›` (a CLI too old for `auth login`: sign
  in in a terminal — `claude /exit` there); **504** `‹name› didn't print
  its sign-in link` (30 s) / `didn't answer the code`. Once the code is
  in, the exchange runs to its end even when your request is gone (60 s
  at most). The exec is always deleted once it is over — recorded
  (`harness_signin_execs`) from before it starts until the manager took
  the delete: one the manager didn't take is tried again, and the next
  start of the agent sweeps what is left; one guided sign-in per
  conversation, bounded at 15 minutes (the exec's own timeout too), and
  one a save or restart of the agent cuts off is gone (start again);
  while it waits for your code it holds your partition up, as a sign-in
  the agent awaits does. A sandbox others may use takes `confirm: true`,
  as above — the sign-in lands in its HOME. **With
  `remember: true`** (and `name`, default `Personal` for a coding agent's
  first, else `Sign-in ‹n›`) it runs the provider's `Mint` instead —
  Claude Code's `claude setup-token`, on a terminal (a manager without
  `tty`: **409**, paste a token instead) — and the one-year
  `sk-ant-oat01-…` token it prints is scraped by the backend and kept as
  your saved sign-in in your partition's vault (a saved sign-in of that
  name replaced, its refusal cleared, and the coding agents that started
  with the old one stopped): it is in no answer, row, log line or event,
  and never reaches a page; the answer's `saved` is its row (no secret).
  The Mint runs **clean**: the CLI found on the image's own directories
  (`/usr/local/bun/bin`, `/usr/local/bin`, `/usr/bin`, … — never the
  sandbox's `PATH`, where a shim in `~/.local/bin` would see the token),
  run by its absolute path with nothing but `PATH`, `HOME`, `TERM` and
  `LANG` in its environment (no `NODE_OPTIONS`, no `LD_PRELOAD`, nothing
  a profile set) and a throwaway `HOME` (none of the sandbox's settings,
  hooks or plugins), removed after; a Mint that ends without a token
  never shows the CLI's last line (a token in a format the scan doesn't
  know would be that line) — `‹name›'s sign-in didn't finish: no word
  from it (it ended without a token)`. The sandbox's HOME isn't signed
  in: the conversation uses the saved sign-in (picked for it when it
  isn't your default). Only in your own partition, your own conversation,
  in a sandbox of yours no one else uses and no hosted conversation ever
  worked in — the exec's output is readable by whoever may use the
  sandbox while it lives — else **409** with why (unpartitioned: `saved
  sign-ins need a partitioned agent …`); checked when it starts and again
  when the code comes: a sandbox shared at its manager meanwhile ends it
  (**409** `that sign-in was ended: …`, nothing saved), and one shared
  through this agent ends it before the share goes out. At the global
  instance every sign-in is **409** (above).
- **Saved sign-ins** (D179) — a person's own credentials for a coding
  agent's CLI, in **their own partition only** (`GET
  /prefs/harness-signins` says `available: false` and why on an
  unpartitioned agent, whose one vault is everybody's — sign each sandbox
  in on its own there; at the global instance every route is **409**
  `saved sign-ins live only in a person's own space …`). Each is `{id,
  harness, name, kind: setup-token | api-key, env, mintedAt?, expiresAt?,
  refusedAt?, refused?, isDefault, expiring?}` — never its secret, which
  lives only in the partition's vault (`harness-signin.‹id›`) and in the
  adapter's exec request. `env` is the variable the CLI reads it from
  (sdk/acp `Provider.Keys`): Claude Code `CLAUDE_CODE_OAUTH_TOKEN` (a
  setup-token, `sk-ant-oat…`) or `ANTHROPIC_API_KEY`; Codex
  `CODEX_API_KEY`; Gemini CLI `GEMINI_API_KEY`; opencode its provider keys
  (`ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY`,
  `GOOGLE_GENERATIVE_AI_API_KEY`, `GROQ_API_KEY`, `XAI_API_KEY`, by the
  value's prefix or named; a secret is one line of printable ASCII
  without `" \ < > &`, which a key file would escape). A coding agent's
  start puts it in the adapter's environment — winning over the sandbox's
  own `$HOME` sign-in (an env token outranks Claude Code's `/login`) —
  only when **all** hold: the run is the person's own conversation (not
  hosted), in their own partition, its sandbox is theirs (`owner.user`)
  and no one else's — **private**: visibility unset or `private`, not
  seen through a share, no members, no shares; any other visibility,
  one a newer manager adds included, counts as shared (they could read
  the process's environment) — **no hosted (non-secure) conversation has
  ever worked in it** (every use by one is recorded first, for good:
  its members could have left something there that reads the next
  process's environment — a hook, a `PATH` shim, a poller; such a
  sandbox says so in the note: create another), and it isn't refused or
  expired. Which one: the conversation's pick (`config.harness.signin`),
  else the person's default for that coding agent; one that can't go in
  is said in a note. A pick that was forgotten is **never** replaced by
  the default (another account): the sandbox's own sign-in, with the
  note `the saved sign-in this conversation picked is gone (forgotten) —
  it uses the sandbox's own sign-in until you pick another`.
  The environment is never stored: `harness_sessions.cred` keeps which
  saved sign-in the current generation started with (its id), and the
  summary's `signin: {pick: "default" | "sandbox" | ‹id›, using: {id,
  name, refused?, forgotten?} | null}` names it (a person's partition
  only). An adapter that refuses a prompt or a session while one is in
  marks it refused (`refusedAt`, `refused`: its words) and parks on its
  sign-in with the note `‹name› refused your saved sign-in ‹n› — saved
  sign-in refused: sign in again`; the next start leaves it out until it
  is replaced (a Remember of the same name, or a new secret). A status
  update alone refuses nothing (claude-agent-acp 0.81's `claude auth
  status` probe reports an env token's sign-in as kind `none`). A saved
  API key an adapter reads only through its own `authenticate` (codex-acp
  — codex 0.156's app-server reads no key from its environment at start
  and has no setting that keeps it out of its file store; Gemini CLI with
  another sign-in selected) is handed to that method once when the
  adapter refuses its session signed out. **Codex writes it to its
  `${CODEX_HOME:-~/.codex}/auth.json`** (sdk/acp `Provider.AuthFile`):
  the agent removes that file right after the hand-over (codex keeps the
  key in memory: the running adapter stays signed in), and — matched by
  the key itself, read from the vault on stdin, never in an argv — again
  before every start with another sign-in or the sandbox's own (so a
  switch really switches account: codex would start signed in by the
  file), before a share through this agent, and at **Forget** in every
  sandbox codex ran in; a removal that fails is recorded
  (`harness_sessions.scrub`) and done before the next start, which it
  refuses while it can't (`Codex kept the key of its last start … — try
  again; it doesn't start as the wrong account`). Codex signed in in a
  sandbox **on its own** (`codex login` there) is left as it is: it uses
  that sign-in, the saved one isn't named as in use, and a note says so
  (`codex logout` there to use the saved one). Gemini CLI keeps a key it
  is handed in memory only (0.60: no file). A saved sign-in warns from
  14 days before it expires (`expiring`). A sandbox shared while an
  adapter holds one stops that adapter — when shared through this agent
  (`PATCH /sandboxes/{ref}` with `visibility`, `members` or `shares` that
  make it shared), **before the share goes out**: the guided sign-ins
  under way there end, the adapters that started there with a saved
  sign-in are killed at the manager (waited for) and stopped, codex's
  file is removed — any of it failing refuses the share (**502** `‹name›
  isn't shared: … — try again`); shared elsewhere, at the next re-check
  (before every message, and at most every minute while it works; an
  idle one at the latest at its idle stop) — and the next message starts
  it with the sandbox's own sign-in. A new secret for a saved sign-in (a
  paste over it, a Remember or a paste under its name) stops the coding
  agents that started with the old one, as Forget does. A person's
  partition stopping (idle, an update) stops each adapter of theirs that
  **rests** with a saved sign-in in its environment, waiting for the
  manager's kill (the next message starts it again, resuming its
  session): xbind gives a partition no hook before it purges a deleted
  person, whose partition then never comes back to stop them. A person's
  removal or purge deletes the vault with their partition.

  **What prints the secret.** Every process the coding agent starts —
  its tools, hooks, MCP servers — inherits its environment, so a routine
  `env` or a prompt-injected command can print it. The adapter's output
  is **redacted** before it becomes anything here: the exact secret of
  the generation, and any string shaped like an Anthropic token
  (`sk-ant-‹kind›‹nn›-…`), become `[redacted]***…` of the same length (the
  stream's offsets stay the adapter's), in the transcript's rows and
  events (and so an AgTT parent's context, notes, the page), the adapter's
  log (`GET /runs/{id}/harness/log`), this agent's log lines and a
  refusal's words kept with a saved sign-in (`refused`). What a tool
  writes into the sandbox's files isn't. Claude Code 2.1.280's
  `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` isn't used: it needs bubblewrap (the
  rootfs has none; it fails without it) and writes stub dotfiles into
  `HOME`. Codex's own shell tool leaves out variables named `*KEY*`,
  `*TOKEN*` and `*SECRET*` by default.

  **Residual risk.** A process running as the same user in the person's
  own private sandbox — anything they, or a coding agent working for
  them, started there — can read the adapter's environment
  (`/proc/‹pid›/environ`) and so the secret; the gate keeps everyone
  else's processes out (no co-users, no hosted conversation). One working
  adapter (a turn in flight) of a person deleted mid-turn runs on until
  it ends or the sandbox stops. **The trust base:** the coding-sandbox's
  operators — who set its image, `argv` and `login`, and whose manager
  receives the secret in the exec request's `env` and can read a running
  process's environment — and xbind's admins (who run the host the vault
  and the sandboxes live on) are in it;
  so is the image: a Mint runs the image's own CLI, and an image whose
  own directories were altered is out of scope.

  | Method & path | Who | Body | Answer |
  |---|---|---|---|
  | `GET /prefs/harness-signins` | a person | — | `{available, why?, signins: […], harnesses: {‹id›: {name, keys: [{env, label, kind, prefix?}], mint}}, warnDays}` |
  | `POST /prefs/harness-signins` | a person, in their partition | `{harness, secret, name?, env?, default?}` | **201** `{signin}` — a pasted key or token (one line, ≤ 8 KiB), its `env` by prefix unless named; the first for a coding agent is its default; one of a name you have replaces its secret (the coding agents on the old one stopped) |
  | `PUT /prefs/harness-signins/{id}` | its person | `{name?, default?, secret?, env?}` | `{signin}` — renamed (unique per coding agent: **409**), the default (or none), a new secret (its refusal cleared; the coding agents that started with the old one stopped) |
  | `DELETE /prefs/harness-signins/{id}` | its person | — | `{ok, stopped}` — **Forget**: a key codex kept removed from every sandbox it ran in (a removal that fails: **502**, nothing forgotten — try again), out of the vault, the row gone, and every coding agent that started with it stopped now (an `hstop`: a turn in flight ends saying so; the next message starts it without it) |

  **Switching accounts within a conversation** (`PUT
  /runs/{id}/harness/signin`, the owner's, while no turn runs and no
  question waits — else **409**): the pick is stored, and an adapter at
  rest is stopped at once (an `hswitch` inbox row; a note `switched to
  ‹name› — ‹agent› resumes this conversation with it`); the next message
  starts it with the other saved sign-in and **resumes the same session**
  (`session/load` — claude-agent-acp's `loadSession` reads the transcript
  from `$HOME/.claude/projects` in the sandbox, whatever account signs the
  requests); one parked on its sign-in is retried with it at once. A
  session that can't be reopened falls back to a fresh one with a note,
  as any resume does.
- **Its summary everywhere.** `POST /ask` and `POST /runs` answer a coding
  agent's new run with its `harness` too. `/tree` nodes carry `engine`
  and, for a coding agent, `harness` without `options`, `commands`,
  `mode.available` and `login.methods` (the board's row); `/needs` items
  carry the same for the run that waits. A coding agent a sandbox manager
  advertises that the SDK catalog doesn't know is `name`d as the manager
  titles it (from its first start). The push for a coding agent's park
  says it in its words: "‹name› wants to run ‹title› — approve or deny.",
  its question, or "‹name› needs you to sign in to it." (kind `login`).
- **The agent's own routes on a coding agent's run** (D147 §4.2.11):
  `PUT`/`DELETE /runs/{id}/memory` **409** `a coding agent has no memory`;
  `POST /runs/{id}/learn` **409** `a coding agent can't learn a skill`;
  `POST /runs/{id}/compact` **409** `‹name› has no /compact` unless it
  advertises it (`harness.commands`; then it is sent); `PATCH /runs/{id}`
  **400** with `model` (`a coding agent's model is an option: PATCH
  /runs/{id}/harness {option: {id: "model", …}}`) and with `sandbox` or
  `detach` (`a coding agent's sandbox is fixed for the conversation`).
  Schedules and triggers run the agent's own loop: a `harness` in their
  bodies is **400** `schedules run the built-in agent` (`triggers …`), and
  a coding agent's conversation as their `targetRun` is **400**
  `targetRun: a coding agent's conversation takes messages from people —
  schedules run the built-in agent`.

**Starting one (the UI).** "Who answers" sits in the home composer (web
`#apick`; the native view: at the top of the home page — a phone's home bar
already holds the class, model and sandbox pickers) and in the new-chat
dialog (`#n-agent`):
the agent itself, or a coding agent of the catalog with its monogram (CC,
CX, GM, OC) — one that isn't available is listed with its `why`. Your pick
is your default for new chats (xbind's `prefs/agent`: `"agent"` or a coding
agent's id). Picking a coding agent hides the class picker — the class
resolves as `POST /ask` resolves it: yours when it allows the coding agent,
else the first in `classes` — and the model picker (its model is an
option); the sandbox picker keeps the sandboxes it fits (their (manager,
image) is in `images`, their egress isn't `none`, a probe didn't find it
missing, and the class may use them) and starts with the one you last used with it (xbind's
`prefs/harness-sandbox`: `{"<id>": "<ref>"}`), else the best that fits. A
running sandbox it wasn't looked for in is probed. When none fits, a setup
card offers the create form filled in for it (a manager and image that have
it, `internet`, `<id>-dev`); when `sandboxes[ref].signedIn` is `false`, it
says the first message asks you to sign in there (and, on a sandbox shared
with others, that they act as you). The ask carries `harness: {provider,
options?}`, the resolved `class` and the `sandbox` — never a `mode`: the
backend applies your Auto / Always approve. A coding agent's conversation
carries its monogram in the list (the app: its name before the subtitle),
and its top bar (the app: the start of its subtitle) says which coding
agent, its state and — its sandbox being shared — that the sandbox's users
can read what it does. The top bar leaves out Memory and Learn skill (the
built-in agent's; the engine turns memory and skills off for a coding
agent) and offers Compact only when the coding agent advertises `/compact`
(`POST /runs/{id}/compact` sends it); Retry (`POST /runs/{id}/resume`)
shows when it was cut off (`lost`) or couldn't start (`failed`). In a
narrow tile the composer puts its pickers (and `#hctl`) on a line above the
message box, which keeps its width (`index.html` `.cpicks`, `.cinput`).

**For managers (the UI)** (`harness-catalog.js`, `native/harness-catalog.js`;
the words `model/harness-manage.js`). ⚙ Classes has the Coding agents toolset and,
with it, which coding agents the class allows (all of them, or a checklist
of the catalog's — `harnesses`); the form warns while the toolset lacks a
sandbox or an egress other than `none`, and a refused save says the
backend's words. ⚙ Coding agents (the app: Settings → Coding agents) lists
the catalog — whether each can be started and why not, the managers and
images that have it, the sandboxes it was found or signed in on, the classes
that allow it, its modes and sign-in command — and checks a running sandbox
now (`?probe=`).

**Guided and saved sign-ins in the UI** (D179; `model/harness-signins.js`,
both views). On a sign-in card whose coding agent has a guided sign-in,
**Sign in to ‹name›** comes first: once the CLI prints its link the card
offers **Open sign-in page ↗** (a real link), **Copy link**, a field for
the code the page shows and **Finish**, and a status line in the CLI's own
words (a malformed code: paste it again; a refusal: its reason, Sign in
again); signed in, the message goes again by itself. **Use a terminal
instead** opens the login terminal as below. In your own partition, in a
sandbox of yours no one else uses, **Remember for my other sandboxes**
(with a name) mints a saved sign-in instead (the card says the token stays
yours, out of the sandbox's home); elsewhere the card says why it isn't
offered. **Coding-agent sign-ins** — the ⚙ Coding agents tab (managers),
and for everyone the dialog behind **Saved sign-ins…** on the card and in
the coding agent's ▾ menu (the app: Coding agent settings) — lists yours
per coding agent: what each is (a subscription token, an API key), its
state (expires in N days from 14 days before; expired; refused — sign in
again), the default; **Make default**, **Rename**, **Forget** (confirmed),
and a key or token pasted (a password field emptied as it is sent; which
variable by its prefix, or picked). In a conversation the coding agent's
▾ button ends with the account (`· using Work`, or `· this sandbox's
sign-in`) and its **Account** section switches it — Default (‹name›),
another saved sign-in (a refused or expired one greyed, saying why), or
This sandbox's own sign-in — resuming the session at the next message.

**Terminals and sign-in in the UI.** A coding agent that needs you to sign
in parks its run on `pendingState.kind == "login"` (status `waiting_input`);
the conversation then shows a sign-in card with the agent's own methods:
a **login terminal** (web: a tab of the terminal dock running the agent's
sign-in command through the manager's `tty?cwd=&cmd=`, as you; the app:
the run's terminal relay, `…/harness/terminal?login=1`), then **Signed
in? Retry** (`POST /runs/{id}/resume`: the agent starts afresh and reads
the new credentials); an **API key** (a password field, sent once to
`POST /runs/{id}/harness/authenticate` — never stored, never shown again);
a **device code** (the page to open and the code to enter, shown only to
you — someone else who asks while your sign-in waits is told it is under
way; the run goes on by itself once you are done). The card says that the credentials land in
the sandbox's home — anyone who may use it acts as you with that agent
there, and its clones and snapshots keep them — and on a sandbox others
may use it asks for a confirm first (`confirm: true`). Someone who may not
use the sandbox is told whom to ask (its binder — or, when you bound it,
its owner); a sandbox missing from `GET /sandboxes` (which lists every
sandbox bound to a conversation you see) — even once read again fresh
(`?fresh=1`, once per sandbox: one just made may be missing from the
cached list) — is gone, or its manager is
unbound or down, and the card says so in place of the methods — start a
new chat in another sandbox, or Retry once its manager is back; someone the conversation is shared
with to read sees what it waits for and no actions (nor the app's ⋯ →
Sign in… or Terminal). While it waits, the composer says to sign in first
and the activity line has no spinner. A coding agent's conversation also
has **>_ Terminal** (web: the top bar; the app: ⋯ → Terminal) — a shell
in its sandbox at its working directory. Only a harness run parked on
`login` gets the card (`signin.js`, `native/terminal.js`); other parks are
their own cards' — and a park of a kind no module draws falls back to the
built-in approval or question card.

**In a partitioned instance (the UI)** (`model/harness-homes.js`; both
views). A coding agent works only in a person's own conversations: its
sign-in lives in its sandbox's HOME, and the shared space holds no one's
credentials.
- **The global instance's own page** (the owner token) starts none: "Who
  answers" isn't shown there, and a new chat is the built-in agent's.
- **In a person's partition** a coding agent starts only in a sandbox
  homed there. The sandbox picker, the new-chat dialog's `#n-sandbox` and
  the setup card count the team's sandboxes, and ones shared with them, as
  not fitting ("… isn't a sandbox of your own space"). When none of their
  own fits, the setup card offers Create, which makes one in their own
  space. A row's `homed` (with `why`, the backend's words), when `GET
  /sandboxes` sends it, decides. Otherwise the page derives it as the
  backend checks it: not `shared`, and `owner.partitionId`,
  `owner.partition` (this page's partition) and `owner.via` (this tile) all
  set.
- **New chat with options:** a coding agent's chat is made in the person's
  own partition, so the dialog (and the app's sheet) offers the sandboxes
  of their own list, whatever conversation is open. "Who can see it" other
  than "Only you" fixes "Who answers" to the built-in agent (`#n-agent`
  disabled, saying why). Such a chat is made at the global instance, so the
  next new chat's sandbox goes with it only when the shared space sees it
  too (a row marked `shared`: the team's, or one shared with the agent);
  one homed in the person's partition stays behind.
- **Sign-in** is offered only for a run homed in the person's own
  partition. Anywhere else — a run at the global instance, seen from a
  person's partition or from the global instance's own page — the card is
  read-only and says why, and the app offers no Sign in.
- **Saved sign-ins** (D179) live only here, in the person's own
  partition and its vault: Coding-agent sign-ins, Remember and the
  account switch appear only in its page; the global instance's own page
  has none (the shared space holds no one's credentials), and an
  unpartitioned agent's says why it has none.
- **Calls follow the run's home.** A coding agent's calls (mode, options,
  a permission, an answer, a message, Stop, Cancel, Retry, the log) go to
  the run's home, as every call about a conversation does. So does the
  app's run relay: `…/harness/terminal?xbin-partition=global` for a shared
  conversation's run.
- **A coding agent's run in the shared space** (a shared conversation's,
  or any on the global instance's own page — data from before this rule:
  the global instance never runs one again) is read, not driven. The
  composer is off and says why, there is no Retry, and its mode and
  options are shown but not switched. Stop, Cancel and the log stay.
- **A coding agent's conversation never moves between homes.** Its row
  and top bar offer no Share a copy…, and its top-bar chip says it stays
  in your own space. A shared one's dialog offers no Copy to my own space
  and no "Use my private resources…"; adding a copy of your files is still
  offered. Nor is it left shared with no one, which would move it: "Only
  you and the people below" with no one below, and removing the last
  person from a private one, are disabled with the reason (the app's sheet
  says why when picked).

**Terminal relays and the log** (D147 §4.2.7, §4.2.8). The native
view's terminals and a coding agent's stderr, for a person who may use the
sandbox **themself** — checked here first, fresh from the manager (by the
rules of §Coding sandboxes: owner, members, `team`, a share), because the
manager doesn't police the person this agent names (in a person's
partition it does: the partition's person, verified, any other `Sbx-User`
refused): both relays dial the manager's `tty` route as this tile with
`Sbx-User: <you>` (asserted; in your partition, verified) and
relay `/ws/term`'s wire byte for byte (`xbin.RelayManagerTTY`); the runtime
still refuses a person with `noTerminal` (D88). The web doesn't use them —
its terminals dial the manager directly, as you (verified).

| Method & path | Who | Query | Answer |
|---|---|---|---|
| `GET /runs/{id}/harness/terminal` | a participant who may use its sandbox | `login=1`, `rows`, `cols`, `exec` | WebSocket: a terminal in the coding agent's sandbox at its cwd — its sign-in command with `login=1` (`harness.login.command`: the adapter's own when it offered one, else the sandbox manager's advertised `login`, else the catalog's (D179: the manager's wins — an older coding-sandbox's `claude /login` keeps working; Claude Code's catalog sign-in is `claude auth login`, no `CLAUDE_CODE_REMOTE`)), else the login shell |
| `GET /sandboxes/{ref}/terminal` | a person who may use the sandbox | `cwd`, `cmd`, `rows`, `cols`, `exec` | WebSocket: `cmd` as the contract's `tty` route runs it (the login shell unless given) |
| `GET /runs/{id}/harness/log` | a viewer who may use its sandbox | `max` (bytes, ≤ 65536: the default; more is the cap) | `text/plain`: the tail of the coding agent's stderr, its current generation |

- **`exec=<id>`** (a terminal's session id, from its session frame)
  attaches again to that tty exec instead of starting one; the other
  parameters are then ignored — send a resize. `GET /sandboxes/{ref}` is
  still the sandbox: only a path ending in `/terminal` is the relay (a
  sandbox id never holds `/`).
- **Refusals come before the upgrade, as JSON:** **400** `a terminal is a
  WebSocket upgrade`, `rows: a number of character cells`; **403** `only a
  person can open a terminal` (an element, the scheduler, the tile itself,
  an admin viewing as someone); **403** `you may not use ‹sandbox› — ask
  ‹who›` (who bound it into the conversation, else its owner, else this
  agent's managers); **404** a run you can't see (`no such run`), a
  sandbox you neither see nor have bound (`no such sandbox`), or the
  manager's `not-found` (a sandbox or exec gone); **409** `not a
  coding-agent conversation` (the run relay on a built-in run), `‹name› has
  no sign-in command to run in a terminal`; the manager's other refusals
  pass through as `{error, refusal, state?}` (`unsupported` 501 without
  `tty`, `state` 409 on an archived sandbox).
- **What a relay started, it ends.** A terminal a relay started (no
  `exec=`) — a shell or a sign-in — is ended (`DELETE …/execs/{id}` at the
  manager, as you) 5 s after its client goes, unless its command exited, or
  a client attached to it again through a relay (`exec=<id>`) before then:
  that client keeps it, and it runs until it exits or someone ends it. The
  app's terminal can neither end a shell nor come back to one, so what it
  leaves would otherwise run on for nobody. The rule is the process's that
  relayed it: across a redeploy, a terminal whose relay ran in the old
  process runs on.
- **A relay holds the partition it reaches.** Its socket is a held
  connection, so the app's Terminal screen keeps running, for as long as
  it is up, the person's partition — or, for a shared conversation's run
  (`?xbin-partition=global`), the global instance
  ([/docs/partitions.md](/docs/partitions.md) §How people's partitions
  run). Going back from the screen closes it, and the partition may go
  idle again.
- **The log** is read as you: a split exec's own stderr (a manager offering
  `stdio`), else the file the wrapper writes in the sandbox's HOME
  (`~/.cache/xbin-harness/<run>-<gen>.log`, read with `tail -c` through the
  contract's `run` — which starts a stopped sandbox). **403** `only a
  person who may use ‹sandbox› can read its log`; **404** `no log yet`
  (never started, or no file); **409** on a built-in run; **400** `max: a
  number of bytes, at most 65536`.

**The agent's coding agents (the tools)** (D147 §4.4, §4.3.13).
`subagent_spawn` takes **`harness`** — an enum of the coding agents the
class allows (`harnesses`) that the spawn's sandbox (the active one, or the
attached one `sandbox` names) offers (its binding's `harnesses`; the SDK
catalog's four when its manager says nothing) with an egress other than
`none` — and **`harness_mode`** (`approve` | `plan`). Both are offered only
when the class holds the `harness` toolset and at least one coding agent
qualifies. Its description says the limit first: a coding agent sees only
the sandbox and the task, not the conversation; each is a full CLI costing
hundreds of MB in the sandbox; parallel ones want a distinct `cwd` or a git
worktree each. The child is a harness run (`engine: "harness"`, its
config's `harness: {provider, mode, ref, cwd}`) of its parent's class in
that sandbox; the task is its first prompt (an `hprompt` row: the user row
and the task ledger's entry, source `parent`, are written as it is
delivered). Its mode is the **root conversation owner's** Auto / Always
approve for that coding agent (`/prefs/harness-mode`; a conversation owned
by no person: Always approve), which `harness_mode` only narrows —
`approve` asks before every edit and command, `plan` only plans (the
provider's plan mode, else its approve mode); the model never picks an
explicit (bypass) mode. The link, the placeholder, the digest and the
delivery are a subagent's: the child's turn end settles the link —
answered with that turn's last text (`(no answer)` when the turn wrote
none — never an earlier turn's), incomplete, error with the error, or
canceled; `subagent_cancel` on a coding agent that rests idle stops it, and
so does what cancels a run's subtree — nothing below a turn outlives it:
its parent's turn ending (a subagent parent's every turn, a top-level
one's `finish`), the owner interrupting the parent, a channel's stop (a
note says why; the next message starts it again, reopening its session).
Refused, as the call's result: `system` with `harness` (`system: a coding
agent keeps its own instructions — leave system out with harness`), `after` with `harness`, `harness_mode` other than `approve` or
`plan` or without `harness`, a coding agent the class doesn't allow or the
sandbox doesn't offer (`harness: <sandbox> doesn't offer <name> (it offers
…)`), a sandbox without egress or known not to have it, one the
conversation may no longer use, **`3 coding agents already run in this
conversation (the limit) — wait for one or cancel one`** (`maxHarness`,
tile config, default 3: harness runs below the root with a turn running or
parked on a person), and — so a coding agent's own commands have room —
**`<sandbox> runs N commands (its limit is M) — a coding agent needs
room`** when the sandbox's running execs would exceed its manager's
`limits.execsRunning` − 4. `subagent_message` to a coding agent queues an
`hprompt` sent as is (no `[message from your parent run …]` wrapper) and
answers `steered into #N's running turn` (it steers), `queued until #N's
current turn ends` (it doesn't), `sent as #N's next prompt` (idle; a new
link, so its answer comes back), or — while it waits for a person —
`queued: #N is waiting for a person to approve: <title> — it gets your
message once they have`: the parent's message never answers a child's
permission or question (a person's reply would reject it; the agent's
waits). `subagent_status`/`_wait`/`_result`/`_cancel` work as for any
subagent; a coding agent's digest line is `#N <label> — <phase> for <time>,
harness <provider> · N tool calls · $0.40` (the cost when the coding agent
reports one), with `waiting for a person to approve: <title>` / `to
answer: <question>` / `to sign in` below it, and in detail `doing:
<activity>`, its last calls (by their summaries) and its latest text. The
parent model is never offered a child's permission: a park goes to people
(Needs, push, the child card). **A person's direct message** (`POST
/runs/{child}/message` or `/answer` by a person, not the parent agent) is
told to the parent as a notice (an `hnote`, kept apart from the inbox) —
`[direct message to #<child> (<name>) from <user>]\n<message>` — delivered
as a user-role message at the parent's next step boundary, or before its
next turn's first message, among what was queued for it then; it never
starts a turn, isn't a request of the task ledger, and isn't work for
`hasWork` — an older build never sees it, so a notice an idle parent keeps
never wakes that build either.

**The agent's coding agents (the UI).** A coding agent the agent started
(`subagent_spawn` with `harness`, D147 §4.4) is drawn where the spawn
call is as its own card instead of the subagent card (`harness-child.js`,
`native/harness-child.js`): its monogram and name, the link's label, `#id`
and state; what it does now (its `harness.activity`, a park, its answer's
first line); where it works (`▣ sandbox:cwd`) and its counters (`counts`,
`usage.cost`, the time since its link was made). The card reads no route
for that — its summary is the link's `child` with the run and `harness`
events since. A park — a permission, a plan approval, a question, a
sign-in — is drawn on the card and answered on the **child's** run
(`POST /runs/{child}/approve`, `…/harness/answer`, `…/harness/authenticate`;
the app opens the card while it waits, and signs in from the child's own
chat). Opened, the card shows the task, the plan and the child's last 3
blocks — read once, as its newest page (`GET /runs/{child}/view?limit=8`),
when the card is open and on screen, then kept current by the stream; a
read that fails says why ("Couldn't read its latest steps: …") with
**Retry** (the app: open the card again) and is tried again by itself only
after a while (15 s, doubling to 2 min; 2 s once an event says the child
moved; at once after a stream reset), never at every repaint — and
its answer. **Stop** (`POST /runs/{child}/interrupt`), **Cancel** (confirmed;
`POST /runs/{child}/cancel`) and **Message** (`POST /runs/{child}/message`;
Enter queues or steers, ⌘/Ctrl+Enter or Send now adds `interrupt: true`)
act on the child from its card (the app: from its own chat — its composer,
Stop, ⋯ → Cancel task). A person's message to a coding agent the agent
started is told to that agent (`[direct message to #<child> (<name>) from
<who>]`, D147 §4.3.13), which its chat shows as a folded notice once it
is delivered. A conversation row says `?` while it or a run below it waits
(`waiting`) and `⧉ N` for the coding agents at work below it (`kids.harness`).

**The Coding agents board (the UI).** Every coding agent in the open
conversation's tree — at home, every one of yours that runs or needs you:
your coding-agent conversations and those the agent started below your
conversations — in one place (`harness-board.js`, `native/harness-board.js`;
the rows are `app.board`'s, `model/harness-board.js`). The top bar's chip
(the app: a toolbar button while one needs you, ⋯ → Coding agents in a
conversation and in the main menu at home)
says "⌨ 3 coding agents · 1 needs you" and opens it: on the web a dock at
the right (over the chat in a narrow window), rows in the order they
started — never re-sorted as they change — each a child card as above (its
park answered in place, on its own run; Open ↗, Stop, Cancel, Message),
with a "needs you" filter; in the app a screen with sections Needs you,
Running and Done (a row's swipe: Stop, Message, Cancel task). The board
reads `GET /runs/{root}/tree` (D147 §4.3.6's harness nodes) once something says a
coding agent is there — the row's `kids`, a link held — and again only when
the stream names a run or link the tree lacks; the rest is the links the
tile holds and the run, link and `harness` events. At home, whose stream
follows the run list only, it reads the trees of the rows with `kids.harness`
(the first 12) and again when their root's row changes. A parked row whose
summary has only the compact `harness.pending` reads the child's newest page
(`?limit=8`) once (a failed read waits, as the card's), so its park can be answered there. The unfolded 📌 Task
lists what it **Delegated** — each coding agent below the run, its state, its
task (the spawn's) and a way to its chat (the app: a section of the Task
screen). "Needs you" says `login` as "needs you to sign in to ‹name›" when
the item names the coding agent (its own `harness`, or its conversation's).

**Testing coding agents.** `hack/harness-smoke.sh` runs, in about a
minute and with no sandbox, the ACP client and its scripted fake adapter
(`sdk/acp`, `sdk/acp/acptest` — `hack/fakeacp`) and this template's engine,
pipe, catalog and relays against fakesandbox with that adapter.
`HARNESS_SMOKE_LIVE=1` adds the live check (`test/isolated`
`TestHarnessLive`): an isolated xbind with owner auth, coding-sandbox on its
runtime and this template bound to it, a sandbox with internet egress; the
rootfs's Claude Code, Codex, Gemini CLI and opencode adapters each go
through `initialize` and `session/new` to a sign-in park (or an answer —
opencode has free models), and the fake adapter (copied into the sandbox,
advertised by the image as `fake`) through a terminal sign-in on the run
relay, a permission, the stdio pipe and a redeploy of this backend
mid-turn, which the next process attaches to. It prints what each real
adapter did (`adapter <id>: …`: its sign-in methods, its login command and
what that shows in a terminal). `HARNESS_SMOKE_VM=1` repeats it with VM
sandboxes; `HARNESS_LIVE_ONLY=claude,codex` narrows the adapters. The real
adapters reach outside services (opencode's model answers; codex asks
OpenAI for a device code), so `make integration` runs `TestHarnessLive`
with the fake adapter only unless `XBIN_HARNESS_LIVE=1` (which the smoke's
live step sets). It needs
user namespaces, the base rootfs (`make rootfs`) and `bin/`'s helpers, and
takes about a minute more. To try an adapter of your own, advertise it on a
coding-sandbox image (`harnesses: [{id, title, argv, login}]`, §Coding
agents "The catalog").

## Projects

A **project** groups work around one coding sandbox: its git repos, a
policy, task conversations and a coordinator. Each **task** is its own
conversation with a git worktree per repo, a branch, a range of ports and
the repos' setup run for it; it pushes and opens pull requests with a
short-lived credential an **scm provider** hands out — a tile that offers
the `scm` service (the scm contract, docs/scm.md), such as the builtin
`scm-github` template — and the provider's CI and review events wake it.
A **coordinator** creates and steers tasks for a person; **team projects**
share a definition and a task board while each member's tasks run in
their own space. A conversation's **CI** — for a task, or for the branches
any coding session pushed — shows in the conversation beside its coding
agents, down to job steps and logs.

Each part below says what it covers; its routes and shapes are described
here when it lands.

### Projects and tasks

**The model.** A project is a coding sandbox (its **workspace**), the git
repos it works on, a **policy**, and its **tasks** — each task its own
conversation, with a git worktree of every repo it works in, a branch of its
own, a range of ports and the repos' setup run for it. The project names the
**scm provider** its repos live at (`scm`: a tile bound to the agent's `scm`
slot, §scm providers and credentials) and the host (`github.com`).

| Kind | Where | What |
|---|---|---|
| `personal` | a person's own space (their partition), or an unpartitioned agent | the project and its tasks; in a person's space private — nobody else's, no members; unpartitioned, shared as a conversation is (members, team visibility) |
| `team` | a partitioned agent's shared space (its global instance) — the only kind it holds | a team's definition — repos, policy, members, an optional seed sandbox — and its board; it has **no tasks** (§Team projects) |
| `membership` | a member's own space | that member's half of a team project (`teamRef` its definition): their own sandbox, tasks and coordinator |

A project's id says its home as a conversation's does: in a person's
partition projects number from 2^40 (`model/homes.js` `homeOf(id)`).

**Its identity at the provider.** What a project reads, and the token its
tasks push with, are as `policy.as` says, else as the person in a person's
own space (`person`), else as the provider's bot (`bot`: the shared space,
an unpartitioned agent). Where the home's identity is the bot, **naming a
repo for it** — a project's repos, a repo added — takes one of the agent's
managers, or the **scm bot rule** a manager sets (who may name which repos,
§scm providers and credentials): 403 "naming ‹repo› for the bot takes a
manager, or the agent's scm bot rule" otherwise. Every later read through
the project — the issue picker, a batch of tasks from issues, a task's
issue — is held to **the project's own repos** (400 otherwise), at every
home.

**Who may do what** — a caller's level on the project, as on a
conversation (§Who sees what): the **owner** (`owner`; never a member row)
changes settings, repos and members, deletes, archives, and forces a
cleanup; a **participant** (a member, or anyone when `visibility` is `team`
and `teamRole` `participant`) creates tasks, messages and acts on them,
warms the workspace; a **viewer** reads. A project you may not see is a 404.
Someone removed from a project keeps the conversations of the tasks they
created (they own them) but no longer acts on its tasks: on a project's
conversation a person's level is held to their level on the project, so
below a participant of it — removed, made a viewer, or the project no
longer team-visible — they read a task they created and nothing more
(talking to it, answering, interrupting, deleting it and `POST
/runs/{id}/task/…` answer 403): talking to a task runs it in the project's
sandbox.
A **task's conversation** is a run with `origin` `project` and `originId`
the project's id: its owner is the person who created the task, its
visibility, team role and members are the project's — written onto every
task when they change — and the project's owner takes part in every task
(a participant member of each task someone else created), so the people of
a project are the people of its tasks. Such a conversation never appears in the conversation list (it is
listed under its project), never moves to another space, and is refused
(409, `refusal: "barred"`, "this conversation is a task of project ‹name›:
its sharing is the project's, and it stays in its project's space") by its
own sharing (`PATCH /runs/{id}` `visibility`/`teamRole`, `POST
/runs/{id}/members`, `DELETE /runs/{id}/members/{user}` — leaving
included: you leave the project — and `POST /runs/{id}/links`),
publishing, a copy into another space and hosting; a join link to it (one
made before it became a task) lets no one in (`POST /join` answers 404).
`GET /runs/{id}/export` stays. Its config carries `project: {id, role:
"task" | "coordinator", n}`.

**Routes.** A project is `{pid}` in a path (never `{id}`). Need: *Any* any
caller (the list filters), *Start* who may start runs, *V/P/O* viewer,
participant, owner of the project — or, on `/runs/{id}/…`, of the
conversation.

| Method and path | Need | Body | Answer |
|---|---|---|---|
| `GET /projects?state=&kind=&cursor=` | Any | — | `{items: [ProjectView], next}` — those you may see, latest activity first (`state` default: all but `deleting`) |
| `POST /projects` | Start | `{name, scm, repos: [{repo, slug?, setup?}], sandbox: {ref} \| {new: {provider, image?, size?, egress?}}, policy?, share?: {visibility, teamRole, members: [{user, role}]}, kind?: "team"}` | **201** `{project, jobs}` |
| `GET /projects/{pid}` | V | — | `{project: ProjectView}` |
| `PATCH /projects/{pid}` | O | `{version, name?, policy?, visibility?, teamRole?, state?: "active" \| "archived"}` | `{project}`; **412** `{error, version}` when `version` isn't the current one |
| `DELETE /projects/{pid}?sandbox=keep\|delete` | O | — | **202** `{state: "deleting"}` |
| `GET /projects/{pid}/members` | V | — | `{owner, members: [{user, role}]}` |
| `POST /projects/{pid}/members` | O | `{user, role}` | `{owner, members}` |
| `DELETE /projects/{pid}/members/{user}` | V (yourself) / O | — | 204 |
| `POST /projects/{pid}/repos` | O | `{repo, slug?, setup?}` | **201** `{repo, jobs}` |
| `PATCH /projects/{pid}/repos/{slug}` | O | `{setup?, checkout?: "worktree" \| "clone"}` | `{repo}` |
| `DELETE /projects/{pid}/repos/{slug}?force=1` | O | — | **202**; 409 `busy` `{error, tasks}` while open tasks work in it |
| `GET /projects/{pid}/status` | V | — | `ProjectStatus` |
| `POST /projects/{pid}/warm` | P | — | **202** `{jobs}`: start the sandbox, fetch the repos, renew credentials |
| `GET /projects/{pid}/issues?repo=&q=&state=&labels=&cursor=` | V | — | the provider's page of issues of one of the project's repos (bodies clipped to 2 KiB; untrusted text) |
| `GET /projects/{pid}/tasks?col=&phase=&q=&mine=1&cursor=&limit=` | V | — | `{items: [TaskView], next}` |
| `POST /projects/{pid}/tasks` | P | `TaskSpec` | **201** `{task: TaskView, run}` |
| `POST /projects/{pid}/tasks/batch` | P | `{issues: [{repo, number}] (≤ 20), size?, agent?, text?}` | **201** `{tasks, errors: [{issue, error}]}` |
| `GET /projects/{pid}/tasks/{n}` | V | — | `TaskView` (with `park`) |
| `POST /projects/{pid}/tasks/{n}/cancel` | P | `{reason?}` | `TaskView` — stopped, its queued input dropped (in the queue, and what waits in its inbox untaken); its conversation, worktrees and branch stay |
| `GET /projects/{pid}/events?since=&limit=` | V | — | `{items: [ProjectEvent], next}` |
| `GET /runs/{id}/task` | V | — | `TaskView` with `park`; 404 when the conversation is no task |
| `POST /runs/{id}/task/refresh` | P | — | **202**: fresh credentials, a fetch, its branch and pull requests read again |
| `POST /runs/{id}/task/retry` | P | — | **202**: its failed workspace jobs (and the project's) queued again |
| `POST /runs/{id}/task/close` | P | `{cleanup?, closePRs?}` | `TaskView`, phase `closed`; `closePRs` closes its open pull requests at the provider |
| `POST /runs/{id}/task/cleanup` | O | `{force?}` | **202**; 409 `dirty` `{error, repos: [{slug, dirty, unpushed}]}` without `force` |

`POST /projects` checks everything before it makes anything: the provider is
bound; each repo is `owner/name`, the bot rule allows it (where the home's
identity is the bot) and the provider can see it as the project (400 with
the provider's refusal otherwise — its other refusals, such as `signin`,
pass through with their status and payload); a `sandbox.ref` is one you may
use (in your own space, homed there) that offers commands and files; the
policy's task class is one you may use and has no internal reach. Then it
answers at once with the first job queued. In a person's own space `share`
is refused (409) and `kind: "team"` too; in the shared space a project is a
team definition (`kind: "team"`, 409 otherwise) and a person must say who
shares it (`share`, 409 otherwise); `kind: "team"` anywhere else is 409.
`POST /projects/{pid}/members` and a `visibility`/`teamRole` other than
private's are 409 in a person's own space. **A component's project** (one
another tile made) isn't shared: `share`, a team visibility and `POST
/projects/{pid}/members` answer 409 "a component's project isn't shared"
— its tasks work with the component's authority, which takes part in no
one else's conversation (a team definition, which has no tasks, may be).

**A shared project has its sandbox to itself.** Everyone who may talk in a
project's tasks runs commands in its sandbox, and so may read anything kept
there — another project's credentials included. So a project that is shared
(team-visible, with members, or with a task conversation someone other than
its owner made, which they read still) is the only project in its sandbox, and no
project joins a sandbox a shared one is in: `POST /projects` with such a
`sandbox.ref`, a `visibility`/`teamRole` change and `POST
/projects/{pid}/members` answer 409 `refusal: "sandbox-shared"` (a project
being deleted no longer counts).

**A new task** (`TaskSpec`):

```jsonc
{"text": "Fix the login page's redirect",   // the brief (≤ 64 KiB); its first prompt ends with it
 "title": "…",                              // default: the issue's title, else the brief's first line (≤ 80)
 "size": "small",                           // small | big (its own sandbox, §Big tasks…)
 "issue": {"repo": "acme/web", "number": 12}, // one of the project's repos; its text goes in, framed as untrusted
 "repos": ["web"],                          // repo slugs; default every repo (an issue's: its repo)
 "agent": {"provider": "claude-code", "mode": "…"}, // a coding agent answers it; default policy.engine
 "class": "coding",                         // default policy.taskClass
 "model": "…"}                              // the built-in agent's model pick
```

Its number `n` is the project's next; its branch `‹branchPrefix›/‹n›-‹slug›`
(the slug from the title: `[a-z0-9-]`, ≤ 32; `-2`, `-3`… when a branch of
that name is on the remote already); its conversation is made at once,
holding no message — its **start** waits in the project's queue (§The
workspace). Refused: 409 `class-internal` for a class with internal reach
(every task reads the provider's text — issues, reviews, CI logs — and never
runs with internal reach), 403 for a class you may not use, without the
`sandbox` toolset or not allowing the project's sandbox manager; 409
`barred` for a coding agent named where none may run (the shared space),
whose class doesn't allow it or whose sandbox image lacks it (policy's
choice instead falls back to the built-in agent); 429 `limit` for a
coordinator past `maxOpenTasks` open tasks it made, or
`maxTaskCreatesPerDay` in 24 hours; 409 for a team definition (no tasks)
or a project that isn't active.

**`policy`** — every key optional; `GET` shows every key with its default
filled in (and keeps keys a newer build stored); `PATCH` merges objects key
by key, and `null` takes a key back to its default.

| Key | Default | What |
|---|---|---|
| `instructions` | — | for every task, after each repo's own AGENTS.md / CLAUDE.md (≤ 32 KiB) |
| `checks` | `[]` | commands a task runs before it pushes (≤ 20) |
| `prConventions` | — | how its pull requests are written |
| `taskClass` | `coding` | the class tasks run in (never one with internal reach) |
| `engine` | `auto` | `builtin`, `harness` (`harness`'s coding agent), or `auto`: `harness` if set, else the coding agent the task's creator last started a conversation with, else the built-in agent |
| `harness` | — | the coding agent `engine` picks |
| `as` | — | `person` or `bot`: the identity at the provider (above) |
| `membersAsBot` | `false` | a team's members may work as the bot (§Team projects) |
| `maxTasks` | `3` | tasks at work at once (1–16, §The workspace) |
| `maxOpenTasks`, `maxTaskCreatesPerDay` | `20`, `50` | a coordinator's limits |
| `branchPrefix` | `xbin/‹uid›` | where task branches live (a ref path) |
| `autoPR` | `off` | `draft` or `ready`: open a pull request when a task comes to rest |
| `checkout` | `worktree` | a new repo's checkouts: a worktree of the base, or a `clone` borrowing its objects |
| `setupTimeoutSec`, `setupBlocking` | `600`, `true` | a repo's setup: its limit, and whether the task waits for it (a failed one never stops the task) |
| `ports` | `{base: 20000, span: 10, slots: 100}` | task `n` listens on `base + ((n−1) mod slots) × span`, `span` ports |
| `fetchEveryMin` | `10` | the repos' fetch while someone works in or looks at the project |
| `protection` | `warn` | a repo whose default branch has no protection: a warning, or `refuse` it |
| `workflows` | `false` | tokens may change `.github/workflows` |
| `ci` | `{autoFix: true, maxPerDay: 5, delaySec: 60, logBytes: 8192}` | what a failing check does (§scm events and polling) |
| `reviews` | `{forward: "trusted", allow: [], batchSec: 120}` | which review comments reach the task |
| `autoLabel` | — | an issue with this label wakes the coordinator |
| `bigTasks` | `{mode: "fork", keepFork: false}` | a big task's sandbox |
| `cleanup` | `{onMerge: true, onClose: true}` | when a task's workspace is cleaned up |
| `coordinator` | `{web: false, model: ""}` | the coordinator's web tools and model |

**`ProjectView`** — the row, your `level`, its `repos`, the board's `counts`
and the slots in use:

```jsonc
{"id": 1099511627777, "uid": "k3x9qa", "name": "Web", "slug": "web", "kind": "personal",
 "owner": "alice", "visibility": "private", "teamRole": "viewer", "level": "owner",
 "scm": "apps/scm-github", "host": "github.com", "sandboxRef": "apps/coding-sandbox|sb-7f3a", "sandboxMade": true,
 "dir": "/work/web", "policy": {…}, "state": "active", "version": 3,
 "repos": [{"slug": "web", "repo": "acme/web", "url": "https://github.com/acme/web.git", "defaultBranch": "main",
            "mode": "bare", "checkout": "worktree", "setup": "npm ci", "state": "ready", "fetchedMs": …,
            "head": "9fceb02…", "protected": true}],
 "counts": {"queued": 1, "working": 2, "needs-you": 0, "pr": 1, "done": 7},
 "slots": {"used": 2, "max": 3}, "createdBy": "alice", "createdMs": …, "updatedMs": …}
```

`state` is `active`, `archived` (its credentials scrubbed, nothing starts,
nothing is fetched; everything kept — its workspace jobs wait, untouched,
until it is active again, and a task whose turn waited for its workspace
rests, its input back at the head of the queue, a message with files left
in its inbox until then) or `deleting` (only reads answer). A
repo's `state`: `pending`, `cloning`, `ready`, `failed` (`error` says why);
`protected` is null while unknown. Deleting a project scrubs its
credentials, cleans its tasks' workspaces up (unless its sandbox goes too),
deletes its tasks' and coordinators' conversations, deletes its sandbox
when the project made it and `sandbox=delete` asked, then its rows.

**`TaskView`** — a task as every route shows it:

```jsonc
{"project": 1099511627777, "n": 3, "run": 1099511627790, "title": "Fix login", "size": "small",
 "branch": "xbin/k3x9qa/3-fix-login", "issue": {"repo": "acme/web", "number": 12, "title": "…", "url": "…"},
 "repos": ["web"], "ws": "ready", "phase": "pr",
 "column": "pr", "state": "ci", "waitingFor": "ci",
 "runStatus": "idle", "engine": "harness", "harness": "claude-code",
 "sandboxRef": "apps/coding-sandbox|sb-7f3a", "fork": false, "dir": "/work/web/tasks/3-fix-login",
 "ports": {"base": 20020, "span": 10},
 "checkouts": [{"repo": "web", "path": "/work/web/tasks/3-fix-login/web", "mode": "worktree", "state": "ready",
                "setupExit": 0, "remoteSha": "…"}],
 "prs": [{"repo": "acme/web", "number": 42, "url": "…", "state": "open", "draft": false, "headSha": "…", "checks": "pending"}],
 "ci": {…},                 // its CI at a glance, when watched (§CI in the conversation)
 "turnBy": "coordinator",   // who asked for its latest turn: human | coordinator | event
 "step": "running web's setup", "error": "", "last": "…its latest answer, clipped…",
 "createdBy": "alice", "createdMs": …, "updatedMs": …}
```

**Its states.** `ws`, the workspace: `pending` → `queued` (waiting for the
project's sandbox or a repo) → `preparing` → (`signin` →) `ready`, or
`failed`; later `cleaning` → `cleaned`, or `blocked` (cleanup refused).
`phase`: `open` → `pr` (a pull request is open) → `merged` | `closed` |
`done`; `deleted` once its conversation is. The board's **`column`** and
the task's **`state`** follow from those and its conversation's status:

| `column` | `state` (`waitingFor`) | When |
|---|---|---|
| `queued` | `queued` (`slot` while its start waits in the queue), `preparing` | its workspace isn't ready |
| `working` | `working` | its conversation runs, waits on its subagents or its own work |
| `needs-you` | `needs-you` (`you`), `signin` (`signin`), `failed` (`you`), `blocked` (`you`) | it asks a person something (an approval, a question, a sign-in), its workspace failed or its cleanup was refused, its turn failed — or its turn ended and its answer waits for you (no pull request yet) |
| `pr` | `ci` (`ci`), `ci-failed` (`you`), `awaiting-review` (`review`) | a pull request is open and its turn is over |
| `done` | `merged`, `closed`, `done`, `cancelled`, `deleted` | — |

**`park`** (in `GET /runs/{id}/task`, `GET /projects/{pid}/tasks/{n}` and
the run view's `projectTask`): the workspace's park of its conversation —
`{project, n, ws, step?, detail?, signin?}` — while it holds the turn
(§The workspace). `signin` (`{url, userCode, expiresAt}`, the provider's
device flow) is there **only for the person who must sign in**, in their
own answers: never in another viewer's, never in a stream event.

**The run's answers.** `GET /runs/{id}` and `GET /runs/{id}/view` of a
project's conversation carry **`project`** `{id, name, n, role}` (`role`:
`task` or `coordinator`; a subagent's too) and, on a task, **`projectTask`**:
its `TaskView` with `park`. (`task` stays the pinned task.)

**Status** (`GET /projects/{pid}/status`):

```jsonc
{"sandbox": {"ref": "…", "name": "web", "state": "running", "workdir": "/work", "shared": false},
 "repos": [{"slug": "web", "state": "ready", "fetchedMs": …, "head": "…", "protected": false, "error": ""}],
 "creds": [{"sandbox": "…", "host": "github.com", "identity": "person", "login": "alice", "state": "live", "expiresMs": …}],
 "jobs": [ProjectJob…],                 // live ones and the last 20 finished
 "slots": {"used": 2, "max": 3},
 "warnings": [{"kind": "unprotected", "repo": "acme/web", "text": "…"}]}
```

Credentials show their metadata only — never a token. A `slots` warning
says when `maxTasks` is more than the model calls built-in tasks may make
at once (a built-in task takes a subagent's place at the model-call gate:
never the last one a person's top-level conversation may take).

**Events.** A project's feed (`GET /projects/{pid}/events`, kept 30 days):
`{id, project, n, kind, body: {text, …}, wake, coordUser, delivered,
msgId, created}` — `n` 0 is about the project. Kinds: `task.created`,
`task.state` (a turn ended: answered, failed, waiting for a person),
`task.human` (a person wrote to the task), `task.cancel`, `workspace`,
`pr.opened`, `pr.ready`, `ci.failed`, `ci.stuck`, `review`, `comment`,
`merged`, `closed`, `push`, `issue`, `note`. `wake` asks the coordinator of
`coordUser` (the task's creator; the owner for the project's own) to take a
turn for it. Text from the provider inside a body is untrusted and
redacted.

**The `project` stream event** — `{"type": "project", "data": {"id",
"change": "project" | "task" | "repo" | "job" | "event" | "board" |
"deleted", "n"?}}` — reaches the list streams (`GET /stream` with no run)
of those who may see the project, and, for `task`, the task's own
conversation's stream. It says what to read again; it is never replayed
and is coalesced per project, change and task (250 ms).

**Rolling back to a build without Projects** leaves nothing to clean up by
hand. The older build never reads the project tables. It keeps a task's and
a coordinator's conversation out of its conversation list (reachable by
link, search and Needs) and in its space; it may drop a run's `project`
field when it rewrites the run's config (a model pick, a sandbox bind),
which this build derives again — from the task's row, or the coordinator's
`session_key` `proj:‹pid›:coord:‹user›` — the first time it reads the run
(so a coordinator still wakes and a task is still held at its workspace).
A task the older build finds parked at its workspace (`sleeping` or
`waiting_input` with `pendingState.kind` `project`) runs at the next wake in
whatever workspace there is, without fresh credentials (a push fails;
nothing leaks); Needs shows such a park as waiting, with its words.
Starts and messages waiting in the project's queue wait until this build
is back; credential files in sandboxes expire on their own (an hour for
the bot's, hours for a person's).

### The workspace

**Layout.** `W` is the sandbox's `workdir` and `H` its `home`, as its
manager says (never assumed); a project lives in `P = W/‹project slug›`.

| Path | What |
|---|---|
| `P/.repos/‹repo slug›.git` | a repo's bare **base**: cloned once, fetched; every task's checkout shares its objects and refs |
| `P/tasks/‹n›-‹task slug›/‹repo slug›` | a task's checkout of one repo, on its branch |
| `P/tasks/‹n›-‹task slug›/.task-env` | `BRANCH=`, `TASK_PORT_BASE=`, `TASK_PORT_SPAN=`, `PORT=`, `GH_CONFIG_DIR=` — `. .task-env` in a terminal |
| `P/.xbin/setup-‹repo slug›.sh` | a repo's setup script (written 0755) |
| `P/.xbin/env` | `GH_CONFIG_DIR=…` for people's terminals |
| `H/.config/xbin-scm/‹project uid›/` | credentials (§scm providers and credentials): never under `W`, never in a checkout |

A task works in its checkout — or, working in several repos, its task
directory — so each repo's AGENTS.md / CLAUDE.md is at its root. The base's
remote fetches the default branch and the project's branch prefix
(`+refs/heads/‹branchPrefix›/*`), `push.default current` and
`push.autoSetupRemote` make a plain `git push` push the task's branch, and
`core.logAllRefUpdates` keeps the reflog of what was pushed. A repo with
`checkout: clone` gets a clone of its own instead (its objects borrowed
from the base, which then never runs `gc` on its own).

**Jobs.** A **worker** in the process that drives conversations prepares
workspaces; the project's `jobs` (`ProjectJob`: `{id, project, task, repo,
kind, state, step, attempts, nextMs, out, error, by, created, updated}`)
are what it does. Each job looks before it acts, so running one again does
no harm.

| Kind | Does |
|---|---|
| `sandbox` | finds the sandbox (`{ref}`: labelled `xbin.agent/project: ‹uid›`, for display — a label proves nothing) or creates it (`{new}`: private — a team's seed the team's — with the label and the space it belongs to, `clientId` `agent:proj:‹pid›:sbx:‹try›`), starts it, lays out `P` and queues the repos |
| `repo` | clones a base in the background (30 min): the default branch from the provider, else the remote's `HEAD`; with `policy.protection` `refuse`, an unprotected default branch fails it |
| `fetch` | fetches a base (2 min): every `fetchEveryMin` while a task works or someone has the project's status open — never waking a stopped space for it — and before a task is prepared when its last is older than 2 min |
| `prepare` | a task's checkouts, in one command: an existing local branch, else the remote's (tracking it), else a new one from the default branch; then `.task-env`, its setup jobs and `bind` |
| `setup` | a repo's setup script, in the background, in the checkout, with the task's environment and `REPO`, `REPO_DIR`; its output's last 8 KiB kept, redacted |
| `bind` | binds the task's conversation to its checkout (as the project's owner, whose sandbox it is and who takes part in every task — a participant's task works there without being the sandbox's member — under every binding rule: §Coding sandboxes), a coding agent's sandbox and directory too: the workspace is `ready` |
| `refs` | after each of a task's turns: whether its branch moved on the remote (what the task pushed itself) and which pull requests are open for it — recorded in the task, and told to the parts that follow a task's branch (events, CI) |
| `cleanup` | a task's worktrees and branch (a clone: its directory), then its task directory; a project's deletion |

A failed step is tried again after 10 s, doubling to 10 min, up to five
tries; then the job fails — the task's workspace with it (`ws` `failed`, a
`workspace` event) — and `retry` queues it again. One git step at a time
runs in a sandbox; git's own lock refusals are waited out (never removed).
A job one process was running when it handed over is taken up again by the
next, which finds the same command in the sandbox by its `clientId`. A job
of a kind this build doesn't know fails "not in this build".

**The workspace gate.** A task's turn waits until its workspace is ready.
Its conversation is parked — `sleeping` while it is being prepared (or its
credential renewed), `waiting_input` when a person must act (a sign-in, a
failed workspace, a refused cleanup; Needs reason `project`) — with
`pendingState` `{kind: "project", project: {project, n, ws, step, detail}}`
and its messages left in its inbox; once the workspace is ready the turn
goes on with them. A task waiting for a person this way keeps no process
up and wakes no stopped space for itself: the person's act does. A cancel or an interrupt always passes, and a turn
already under way is never stopped by it. A coordinator is never gated.

**At most `maxTasks` at work.** A task holds one of its project's slots
while its conversation runs, waits on its subagents or its own work, is
parked at the gate, or waits for a person, and from the moment its start
or a message is delivered to it until it takes it up (an idle coding agent
holds none). A task's **start**, and every message the coordinator or a provider's
event sends a task, wait in the project's **queue** and are delivered
oldest first while a slot is free (a message to a task already at work goes
at once: it reads it at its next step); one sent for a person to answer
first waits while its task waits for a person — the coordinator never
answers in a person's place. A person's own message to a task (`POST
/runs/{id}/message`) goes straight to it. Nothing starts while the agent is
halted or the project isn't active; the queues move again once the halt
is lifted. The coordinator's messages reach the
task framed `[message from the project coordinator]`.

**What a task is told.** A built-in task's system prompt has a `# Project`
section after its sandbox's — the project, the task, its branch ("push it,
never the default branch; never merge"), its checkouts and where it
starts, "each repo's AGENTS.md / CLAUDE.md governs — read it first", the
project's instructions, checks and pull request conventions, its ports,
and a failed setup's outcome — built from the project's state alone, so it
is the same from one turn to the next. A coding agent's first prompt starts
with the same words and ends with the task's text. Text from outside — an
issue's, a setup's output — loses its invisible characters, then is
redacted, clipped (8 KiB) and framed
`[untrusted — from ‹where›: …]` … `[end of untrusted text]`; the frame's
own markers inside the text lose their bracket, so it can't close early.

**Environment.** Every command of a task — its bash jobs, its coding
agent, its setup — gets `TASK_DIR`, `BRANCH`, `TASK_PORT_BASE`,
`TASK_PORT_SPAN`, `PORT` (the first of its ports) and `GH_CONFIG_DIR`
(§scm providers and credentials).

**Cleanup** — on close with `cleanup`, by the policy on a merge or a close,
when its conversation is deleted, and by `POST /runs/{id}/task/cleanup` —
first makes sure nothing would be lost: each checkout has no uncommitted
change and no commit its upstream (else the default branch on the remote)
lacks; a merged pull request counts as pushed. Otherwise it is refused —
409 `dirty` from the route, `ws` `blocked` from the worker — unless
`force`, which only the conversation's owner sends. The conversation stays.

### scm providers and credentials

The `scm` slot (bind any tile that provides service `scm`), signing in to a
provider, which identity a project uses (a person's own sign-in, or the
provider's bot), when a credential may be written into a sandbox, where it
goes (files outside every repo, a git credential helper, `GH_CONFIG_DIR`),
how it is refreshed, when it is scrubbed, and how tokens are kept out of
every transcript, log and event.

**The slot.** The manifest's `scm` interface slot (`http`, service `scm`,
multi): `bx bind <this component> scm+=apps/scm-github`, or the binding
panel. A provider implements the scm contract, protocol 1
([/docs/scm.md](/docs/scm.md)) — the builtin `scm-github` template, or
another host's; the binding is the grant. A project names one bound
provider (its `scm`, the provider's tile path; `apps/x#inst` for an
instance). The agent says `hello` to each (cached 60 s, 10 s after a
failure) and lists, with the reason, one that speaks another protocol or
doesn't offer `credentials`. Unbound, there are no credentials to push
with.

**Who the provider sees.** The agent sends no identity of its own: xbind
says who is calling. From a person's partition the provider sees that
person (a partitioned provider answers from its own partition for them —
their sign-in, their tokens); from the shared instance or an
unpartitioned agent it sees the agent, which may use only the provider's
**bot**. A project's identity (`policy.as`) defaults to `person` in a
person's partition and `bot` elsewhere; a membership works as the bot only
when its `policy.as` says `bot` and the team's `membersAsBot` allows it.
The agent never asks as a person outside a person's partition.

**The scm bot rule.** Where a home's identity is the bot (the shared
instance, an unpartitioned agent), the binding gives the agent the bot's
whole view of the host, and the agent decides which of its people may use
it. Naming a repo for the bot — creating a project, adding a repo, making
a conversation a project, watching CI — takes one of the agent's managers
(every component holding a grant to the agent is one), or a person the
rule names with a repo pattern that matches (`owner/name` globs, matched
without case: `acme/*`). Reads and tokens afterwards follow from the
project, whose sharing decides who acts in it. A person's partition never
reads the rule: there the person's own sign-in decides.

| Method and path | Who | Answer |
|---|---|---|
| `GET /projects/scm` | anyone | `{providers: [{scm, title, kind, hosts, caps, identities, you, app, events, notes, error?, refusal?}]}` — every bound provider's hello as this home sees it (`you` says what this caller may be there) |
| `GET /projects/scm/repos?scm=&q=&cursor=` | anyone | `{items: [{host, owner, name, cloneUrl, defaultBranch, private, permission, archived, url}], next}` — through the provider as this home; at a bot home only what the caller may name. `scm` may be left out when one provider is bound |
| `GET /projects/scm/bot` | a manager | `{users: [ids], repos: [globs]}` (empty by default) |
| `PUT /projects/scm/bot` | a manager | the same body; up to 200 of each; 409 in a person's partition |
| `GET /projects/scm/signin?scm=` | the person | `{state: "none"\|"pending"\|"done", identity?, signin?}` — reads, starts nothing |
| `POST /projects/scm/signin` `{scm}` | the person | starts (or continues) a sign-in: `{state: "pending", signin: {url, userCode, expiresAt, pollId, intervalMs}}`, or `{state: "done", identity}` |
| `GET /projects/scm/signin/{pollId}?scm=` | the person | `{state: "pending"\|"done"\|"denied"\|"expired"\|"error", identity?, error?, retryAfterMs}` |
| `DELETE /projects/scm/signin?scm=` | the person | **204** — Forget: every credential of the person's projects from that provider is emptied and revoked first, then the provider revokes the grant and forgets the sign-in |

The sign-in routes are a person's own, in their own partition (409 "sign in
to ‹provider› from your own space" elsewhere; 403 for view-as and for
components). A provider's refusal comes back with its status and its
`refusal` (`signin`, `not-installed`, `identity`, `setup`, `limit`, …) and
payload, as [/docs/scm.md](/docs/scm.md) §Errors lists them — but a
`signin`'s device code goes only to the person who must sign in (the
partition's own, not viewed as): anyone else gets the refusal without it.
A 5xx that names none (a provider down behind xbind's gateway) is
`unavailable`.

**When a credential may go into a sandbox.** Checked before every write and
every refresh, against the sandbox as its manager reports it now — never a
label, which anyone who may edit the sandbox could set:

- **A person's token** only for their own personal project or membership,
  private with no members, in a sandbox that is private, theirs, homed in
  their partition, that no non-secure (hosted) conversation ever used —
  nor, for a task's fork, the sandbox it was forked from — and that wasn't
  cloned from a team's seed sandbox (which every member can write to).
- **The bot's token** only in a sandbox homed in this partition (a
  person's) or at this agent's own identity (no partition, not seen
  through a share), with no shares, never used by a hosted conversation,
  where everyone who can use it — its owner, its members, the team when it
  is team-visible — takes part in the project (participant or owner; a
  team-visible sandbox needs a team-visible project whose team role is
  participant).
- **Never** in a team project's seed sandbox.
- **The other way round:** a non-secure (hosted) conversation doesn't
  work in a sandbox that holds a project's credential (its sandbox tools
  refuse it, saying why — create another sandbox for it): its members
  could have the agent read the files or push with them. The credential
  stays where it is; once it is scrubbed (the sandbox stopped through the
  agent, say), the conversation may work there, and from then on no
  credential goes into that sandbox.

A refusal marks the credential `blocked` with why, empties and revokes
anything written there before, and fails the task: "credentials can't go
into ‹sandbox›: ‹why›". It is tried again the next time the task's
workspace is retried.

**Where it goes.** Two files under the sandbox's home — never under its
workdir, never in a repo or a worktree — in directories only its user can
read (0700), the files 0600, written beside and then renamed into place:

| Path (under `$HOME/.config/xbin-scm/<project uid>/`) | Content |
|---|---|
| `<host>.cred` | `username=x-access-token` and `password=<token>` lines (git's credential format) |
| `gh/hosts.yml` | `<host>:` with `oauth_token`, `user` and `git_protocol: https`, for `gh` |

Each of the project's base repos (worktrees share it) and each clone-mode
checkout gets, in its own git config: an empty
`credential.https://<host>.helper` (so a helper from the sandbox's global
or system config doesn't answer for the project), then a helper that
prints the `.cred` file, `useHttpPath false`, and `user.name` and
`user.email` from the token's author. Every exec of a task — its setup, its
jobs, its bash, its coding agent — gets `GH_CONFIG_DIR` pointing at the
project's own `gh` directory, and `<project dir>/.xbin/env` says the same
for people's terminals (`. .xbin/env`): a person's own `~/.gitconfig` and
`gh` login are left alone. The home must be a plain absolute path
(letters, digits, `.`, `_`, `-`, `/`), or no credential goes there.

**Refresh.** A credential is short-lived (an installation token an hour, a
person's some hours) and is minted again before every git step of the
workspace when it has less than 10 minutes left; a task whose credential is
missing or due waits (`preparing`) while a `creds` job writes a fresh one.
A repo added to the project, or `workflows` turned on, gets a credential
that covers it at the next write, whatever the old one's time left.
While a task of the project is at work, each token is also re-minted at the
provider's `refreshAfter` (or at 75 % of its life, if sooner) and both files
rewritten; an idle project's token is left to lapse.

**Signing in.** When the provider answers that the person isn't signed in,
it has started a sign-in: the task waits (`signin`), its card shows the
device code — to that person only, never in a stream event or anyone else's
view — and the `creds` job asks the provider at its interval, for at most
15 minutes, then writes the credential and lets the task go on.

**Scrubbing.** Both files are emptied (zero bytes, still 0600), the token
is revoked at the provider (best effort — the provider forgets it either
way) and the credential's state becomes `scrubbed` with why, when: the
sandbox is shared through the agent (before the share goes out — a
credential that can't be emptied refuses the share), stopped or archived
through the agent, or deleted (revoked only); the project is archived or
deleted, a repo is removed, or the sandbox leaves the project; a task's
fork is deleted; the person forgets their sign-in; or the gate refuses the
sandbox. When the files can't be emptied (the manager refused the write),
the token is revoked anyway but the credential stays `live`, due at once:
every later scrub tries again, a share stays refused until one succeeds,
and the next turn has it replaced (or blocked) first. A share, stop or
archive through the agent and a credential's write take turns: one being
minted while the sandbox is shared is checked again once minted and, the
sandbox now shared, revoked and never written. A write that fails partway
(a file, the rename) is scrubbed at once — both files emptied, what was
written beside them removed, the token revoked — unless an older live
credential there covers the files (a refresh: the older token keeps
working, and the next scrub takes both out); one that can't be emptied
stays `live`, as above.

**Kept out of what is kept.** The agent holds a token in memory only;
`GET /projects/{pid}/status` shows its metadata (`creds: [{sandbox, host,
identity, login, state: "live"|"scrubbed"|"blocked", expiresMs, why}]`),
never the value. Every message the agent stores — every tool result, the
coding agents' output, their log — has GitHub's token shapes (`ghp_`,
`gho_`, `ghu_`, `ghs_`, `ghr_` and `github_pat_` tokens, up to the next
space, quote or `@`) and every value handed out masked, same length, with
`[redacted]`; a replaced token stays masked until it expires. After a
restart the shapes still apply; a value of no known shape is masked again
once it is minted again.

**Rolling back.** A build without Projects leaves `project_creds` alone
and keeps `scm_bot_rule` as an unknown setting; credential files already in
sandboxes stay until their tokens expire (at most a few hours), as the
older build neither refreshes nor empties them.

### Big tasks, upgrades and pull requests

A big task's own sandbox, forked from a snapshot of the project's taken
while it is quiet (or made fresh); "Make this a project…" turning a
conversation with a sandbox and its git repos into a project, the
conversation its first task; opening a pull request, by hand or when a
task comes to rest.

| Method and path | Need | Body | Answer |
|---|---|---|---|
| `POST /runs/{id}/task/pr` | P (and a participant of the project) | `{draft?, title?, body?}` | **202** `{job}`; the pull requests arrive in `TaskView.prs`. 409 when the project isn't active, the task is over or its workspace isn't `ready`. Only `POST` is mounted: a `GET` answers 405 (how a client learns the route is there) |
| `POST /projects/{pid}/fork-base` | O | `{now?: true}` | **202** `{job}`: a fork base taken when the sandbox is next quiet (a person's ask waits up to 30 min for that, from the ask — one the agent queued itself and hasn't started becomes the person's), or with `now` at once — it may stop the sandbox: ask first. 409 `refusal: "unsupported"` when its manager takes no snapshots or clones; 409 `refusal: "busy"` while the agent's own snapshot job, or one not taken `now`, is at work (ask again once it ends) |
| `GET /runs/{id}/project/detect` | O | — | `{sandbox, cwd, candidates: [{path, remote, host, repo, scm, defaultBranch, branch, dirty, ssh, hasCredentials}]}` |
| `POST /runs/{id}/project` | O | `{name, scm, repos: [{path, repo?}], branch?: "keep" \| "new", switchHttps?: [path], policy?}` | **201** `{project: ProjectView, task: TaskView}` — the conversation is task 1 |

**Big tasks.** A task created with `size: "big"` works in **a sandbox of
its own**, made by its `prepare` job before its checkouts:

- **From the fork base.** A project whose policy says `bigTasks.mode`
  `fork` (the default) keeps a **fork base**: a snapshot of its sandbox
  (`forkSnap`, `forkSnapMs` on the project), taken by the `snapshot` job
  after its repos are first ready and again when it is a day old and a
  task changed since — only while the sandbox is **quiet**: no task of any
  project at work there, no command a conversation runs there, no coding
  agent busy in it, no conversation at work bound to it, no workspace job
  running in it and no command its manager runs there (a person's
  terminal), because a snapshot may stop the sandbox. Every project's
  credential in the sandbox is emptied (and revoked) before the snapshot
  is taken, and none is written there, nor does a git step start there,
  until the manager has answered — so no snapshot holds a live token; the
  workspace gate writes a fresh one when a task next needs it. Even one
  asked for `now` waits for a git step running in the sandbox, or due to
  start there (a person's ask waits, the agent's own gives up). Each
  snapshot job asks for one snapshot (`clientId`
  `agent:proj:‹pid›:snap:‹job›:‹queued at›`, named `fork base of ‹project
  slug›`), so a retry asks the same. One the agent asks for itself is not
  asked again within the hour after the last one ended, taken or not. The
  newer fork base replaces the older (which is deleted). Needs the
  manager's `snapshots` and `clone`.
- The task's sandbox is a **clone** of the fork base (`clientId`
  `agent:proj:‹pid›:fork:‹n›`, named `‹project slug›-‹n›`), labelled with
  the project (`xbin.agent/project`), the task (`xbin.agent/task`) and the
  space it belongs to, with the project sandbox's visibility and members.
  In it, before anything else runs: every credential the snapshot carried
  is removed (`H/.config/xbin-scm`), the other tasks' checkouts are removed
  and git forgets them (`worktree prune`, then `repair`); then it gets a
  **credential of its own** (the credential gate judges the fork — and the
  sandbox it came from: one a non-secure conversation used refuses it) and
  every base is **fetched**, so the task starts from the remote's default
  branch as it is now. Its checkouts, setup and binding then follow as for
  any task, in the fork; `TaskView` says `fork: true` and the fork's
  `sandboxRef`.
- **Fresh.** Without a fork base, with `bigTasks.mode` `fresh`, or where
  the manager can't clone, the task's sandbox is a new one of the project
  sandbox's image, size and egress, laid out as the project's, with every
  repo cloned into it. A fork base the manager no longer has falls back to
  this too (and is forgotten).
- A fork works at the project's paths: its manager must give a clone the
  same working directory (managers do); one that doesn't fails the task's
  workspace with words saying so.
- **Cleanup** of a big task (§The workspace) is followed by its `fork`
  job: the fork's credential is scrubbed, then the fork deleted — unless
  `bigTasks.keepFork`, which keeps the sandbox but scrubs its credential
  all the same (no task works there any more). A fork its task never
  worked in — `prepare` made it, then failed — goes the same way once the
  task is cleaned up or ends (closed, done, merged, its conversation
  deleted). A fork outlives neither its project: the forks of a deleted
  project are deleted soon after (unless they were to be kept).

**Pull requests.** The `pr` job, for each of the task's checkouts **on the
task's branch** with commits the repo's default branch (as last fetched)
lacks, pushes that branch — `git push origin refs/heads/‹branch›`, never
anything else, and **never the default branch**: a task whose branch is
its repo's default branch is refused (the job fails, saying so) — then,
for each such repo with no open pull request in `prs`, opens one through
the provider (`POST /scm/pulls` `{repo, head: ‹branch›, base: ‹default›,
title, body, draft, clientId: "agent:proj:‹pid›:pr:‹n›:‹repo slug›"}`,
as the project's identity), so asking again opens nothing twice. The
title is the one given, else the task's; the body the one given (redacted),
else one line saying which task of which project it is for — and `Closes
#‹n›` for its issue in that repo; nothing of the conversation goes out.
`draft` defaults to `policy.autoPR` being `draft`. The pull requests are
recorded in the task's `prs` (`phase` `pr`, a `pr.opened` event each, a
`note` for each push), and the parts that follow a task's branch (scm
events, CI) are told. A checkout on another branch is left alone (a `note`
says so). Asking again while the job runs runs it once more after. A
pull request's create that fails as a connection would (or the provider
answers `limit`, `unavailable` or `upstream`) after the push is tried
again: the job records what it did and runs again, with backoff. At its
last try it ends with a `note` instead (the task's workspace untouched,
never failed), and the ask stays for the next ask or turn.

**Auto-PR.** With `policy.autoPR` `draft` or `ready`, a task whose turn
ended well (it rests `idle` or `done` — not cancelled, not failed), whose
workspace is ready and that has a repo with no open pull request gets the
`pr` job on its own, for those repos only (one whose pull request is open
is the task's to push to). A person's ask waiting already wins.

**"Make this a project…"** — for the owner of a conversation (a chat or an
API conversation, not a subagent's, an automation's or a non-secure one)
that has a sandbox bound (or whose coding agent works in one), in a
person's own space or an unpartitioned agent; at a partitioned agent's
shared space it answers 409 (it holds team definitions, which have no
tasks).

- `GET /runs/{id}/project/detect` runs one command in the sandbox: the
  git clone at the conversation's working directory, else every clone up
  to three levels below it (at most 20); for each its `remote` (origin,
  **without its userinfo, query or fragment** — never answered, stored or
  logged; `hasCredentials` says one was there), `host`, `repo`
  (`owner/name`), `scm` (the bound provider whose `hosts` hold the host;
  `""` none), `defaultBranch` (as the clone knows it), `branch` (`""`
  detached), `dirty` (changed files) and `ssh`.
- `POST /runs/{id}/project` checks each `{path, repo}` again in the
  sandbox (the path is a clone's top directory; its origin is `repo` —
  `repo` may be left out — at one of the provider's `hosts`) and at the
  provider (it sees the repo; at a bot home the scm bot rule: 403), and
  the classes: the conversation's class, or `policy.taskClass`, with
  internal reach is 409 `class-internal`, as is a conversation that has
  held internal data (a task reads text from the provider and pushes to
  it); one the caller may not use is 403. Then it makes the project in the
  conversation's space — the conversation's sandbox its workspace (labelled
  by the `sandbox` job), its clones its repos (`mode` `adopted`, `basePath`
  the clone: never removed; later tasks take worktrees of it) — and the
  conversation **task 1**: `origin` `project`, `config.project`, its
  checkouts the clones themselves (`mode` `main`), its workspace `ready`.
  `branch` `new` (the default here) starts the task's branch
  (`‹branchPrefix›/1-‹slug›`) in each clone, the work in it carried over;
  `keep` keeps the branch the clones are on — refused (409) when it is a
  repo's default branch, when they are on different ones or not on one.
  `switchHttps` paths get their origin set to the provider's https clone
  URL (what a remote's own credential is replaced with: the project's
  credential helper serves https); an ssh origin is otherwise left alone.
  Each clone gets `push.autoSetupRemote` and `core.logAllRefUpdates`. Then
  a credential is written for the task. The project's directory
  (`‹workdir›/‹slug›`) is a new one: its slug, from the name, skips every
  name already in the working directory and any path that is a clone,
  holds one or lies inside one (a project named after its clone gets
  `‹name›-2`). Refused, too: a shared conversation (its sharing would
  become the project's — unshare it, upgrade, share the project; 409), one
  at work (409 `busy`), one being made a project already (409 `busy`), one
  that is a project's already, and a clone that holds the sandbox's
  working directory (409: every project directory would be inside it). A
  refusal leaves no project behind; a failure after the clones were
  changed takes the new branch back (the clone back where it was, the
  branch deleted) — the origin switched to https stays.
- Cleanup never removes a `main` checkout: cleaning task 1 up keeps the
  clones and the work in them.

**Rolling back.** A build without Projects leaves an upgraded
conversation out of its conversation list, as it does every task — the one
visible regression: it is reachable by its link and by search, and is task
1 of its project again once this build is back. Forks and fork bases stay
at their manager until this build is back (a fork base is replaced, a
cleaned task's fork deleted, as above); the settings `proj_fork:‹pid›:‹n›`
and `proj_pr:‹pid›:‹n›` are unknown settings to it.

### The coordinator

A **coordinator** is a person's conversation that creates and steers a
project's tasks: one per person per project, made the first time they ask
for it (a membership's in the member's own space; a team project's
definition has none). It is a conversation of the project (origin
`project`, `session_key` `proj:‹pid›:coord:‹user›`, `project` `{id, role:
"coordinator"}`), owned by the person and private — out of the
conversation list like a task, and deleted with its project.

```
POST /projects/{pid}/coordinator   {text?}   → {run}   (participant; a person, not viewing as someone)
GET  /projects/{pid}/needs                   → {items} (viewer)
```

`POST` answers the caller's coordinator, made on first use; `text` is
queued to it as their message (as `POST /runs/{id}/message` would). 404
for someone viewing as another person (as on every project route); 403 for
a viewer, for a component that takes part (a coordinator is a person's),
and for someone who isn't one of the agent's managers while the `web`
class is kept for managers (its `who`, checked as `POST /ask` checks a
class); 409 for a team project's definition or a project that isn't
active. Events written before it was made are not delivered to it as a
backlog: it starts from the project as it stands.

**Its class.** The built-in `web` class — the web lane: it steers tasks
that reach outside, so it never holds internal reach (409
`class-internal` when the web class has been given internal reach, and
its tools refuse while its class has it). `web_search` and `web_fetch` are
denied unless `policy.coordinator.web` is on; `schedule`, `unschedule` and
`skill_manage` always are. Its model is `policy.coordinator.model`, else
the agent's default. It is never held at a workspace gate and takes the
model-call gate as any conversation does. Its system prompt has a
`# Project` section — the project, its repos, its limits and the rules
below — built from the project's settings alone, so it stays the same from
turn to turn.

**Its tools** — offered to the coordinator itself (never its subagents),
each call checking again that it is one, that its project is active and
that its person still takes part:

| Tool | Parameters | What it does |
|---|---|---|
| `task_create` | `tasks: [{title?, brief, repos?, size?}]` (1–10) or `issues: [n]` (1–10) with `repo`; `note?` (added to every brief) | creates tasks as its person (each starts when a slot is free and its workspace is ready) |
| `task_list` | `state?` (a task state, or `open`), `q?`, `scope?` (`mine`, or `team`: a team project's board, read-only), `cursor?`, `limit?` (≤ 50) | the project's tasks, newest activity first: number, title, state, branch, pull requests |
| `task_status` | `tasks?` (≤ 10; default every open task), `detail?` | what each is doing now: its phase, whom it waits for, its recent tool calls and latest text, its pull requests and checks |
| `task_message` | `task`, `text` | a message to a task, through the project's queue |
| `task_result` | `task`, `offset?`, `limit?` | a task's latest full answer, paged |
| `task_cancel` | `tasks` (≤ 10), `reason?` | stops tasks and what they started; conversations, worktrees and branches stay |
| `scm_pr` | `task`, or `repo` and `number` | a pull request: state, mergeability, reviews and comments, its checks in one aggregate with each failing job's failing step and the end of its log (≤ 2 KiB each, 8 KiB in all) |
| `scm_issues` | `repo`, `numbers?` (≤ 10), `state?`, `labels?`, `q?` | issues in full with their comments, or a list |

A task is named by its **number** in the project, never by a conversation
id. `repo` — in `scm_pr`, `scm_issues` and `task_create`'s issues form — must
be one of the project's repos (owner/name or its slug), wherever the agent
runs: the coordinator reads through the project's identity, which may be
the provider's bot, and never about a repo nobody named for the project. In
approval mode `task_create`, `task_message` and `task_cancel` ask first.

**What it may not do.** It acts only on its own project's tasks. It
**never answers in a person's place**: a message to a task that waits for a
person (an approval, a question, a coding agent's question or sign-in)
waits in the queue until that person has answered — one already delivered
when the task starts waiting goes back to the head of the queue, held the
same way. It can't merge, approve, push or comment (its scm tools only
read, and the scm contract has no merge); it changes no policy, sharing or
sign-in, and deletes no task or project. People stay in charge: everyone
taking part in the project opens, messages, approves and cancels any task.
The halt stops it as it stops everything.

**Limits.** At most 10 tasks per `task_create`; coordinators of a project
may have `policy.maxOpenTasks` (20) open tasks and create
`policy.maxTaskCreatesPerDay` (50) in 24 hours — past them `task_create`
answers `limit` for the tasks it couldn't make.

**Project updates.** The project's events for its person (`coordUser`: the
creator of the task, the owner for the project's own) reach the
coordinator at its next step as one message:

```
[project updates — tasks and the scm provider reporting, not a person]
#3 task.state: answered (Opened PR #12 on acme/web)
#4 ci.failed: test (ubuntu) failed on xbin/k3x9qa/4-fix-signup@9fceb02
project workspace: the project's repo job failed: …
```

one line each (at most 40 lines and 8 KiB; older ones are counted, not
shown), each event then `delivered` with the message's id (`msgId`). An
event that asks for a wake (`wake`) — a turn the coordinator asked for
ended, a task failed or waits for a person, a pull request's checks
passed, CI stuck — starts an idle coordinator's turn, at most once a
minute; the others wait for its next turn. A coordinator that can't take
a turn now — it waits for its person, its last turn failed, its project
isn't active, or its person no longer takes part — gets no wake: its
undelivered events' `wake` is cleared, and they reach it with its next
turn, whatever starts it. What tasks, issues, reviews and
logs say reaches it clipped, redacted and framed as untrusted data, and the
updates' and frames' markers inside such text lose their bracket.

**Needs and pushes.** `GET /needs` items of a project's conversation carry
`project` `{id, name, n}` (`n` 0 for a coordinator); `GET
/projects/{pid}/needs` lists the project's alone. A push about a task (a
question, an approval, a failure) is titled `‹project› · ‹task›`. The
project's own pushes go to the person whose task it is, within the same
per-person budget, collapsed on the device per project and kind
(`project:‹id›:‹kind›`): `pr-ready` (a task's pull request is green),
`task-failed` (its workspace failed, or a coding agent's turn did),
`ci-stuck` (CI kept failing past the day's fixes) and `all-done` (every
task of the project finished).

**Attaching a chat** to a coordinator (`/project ‹name›` in a direct
message) is not in this build.

### scm events and polling

How a provider's events reach the agent (`POST /adapter/scm/event`, the
partition hand-off), how they are routed to tasks — CI failures, green
checks, reviews and comments, merges, pushes — what each does, and the
polling that stands in when events don't arrive.

**Wiring.** Events need both bindings: the agent's `scm` slot names the
provider (`bx bind <this component> scm+=apps/scm-github`), and the
provider's `agents` slot (service `agent-inbox`) is bound to the agent
(`bx bind apps/scm-github agents+=<this component>`), which gives the
provider the `channel` role on the agent's `/adapter/*` routes. Without the
second, nothing is delivered and polling alone keeps tasks up to date.

**`POST /adapter/scm/event`** takes an event v1
([/docs/scm.md](/docs/scm.md) §Events), at most 1 MiB, from a provider
bound in the `scm` slot — its own backend, at its global instance or
unpartitioned; any other caller, a person's partition of the provider or a
person through it is 403 ([/docs/agent-inbox.md](/docs/agent-inbox.md)
§scm events has the checks). The caller is the provider, whatever the
body's `scm.provider` says. `eventId` with `for` dedupes for 7 days (the
copies of one event for two people, or for a person and `global`, are each
taken). Answers: 200
`{taken: true}` (and `duplicate: true` for a repeat); 400 a body that isn't
an event v1 (`refusal: "protocol"` with `protocols` for another
protocol); 404 not this instance's (`for: user:<id>` at an unpartitioned
agent, or any delivery to a person's partition); 413 too large; 5xx — the
provider delivers it again.

**A person's events.** `for: global` is handled where it arrives (the
shared instance — which holds team definitions only, so only the CI view's
watches take them there — or an unpartitioned agent). At a partitioned
agent's shared instance `for: user:<id>` is handed to that person's
partition by partition mail (`handoff/scm`), which takes it only from the
shared instance, only for its own person and only when the event's
`forPid` is its partition id — a person re-created under the same id never
gets the earlier one's events — and dedupes it again.

**Subscriptions.** Once a task's branch is on the remote, or it has a PR,
the agent keeps one subscription per task and repo at the project's
provider, key `task:<pid>:<n>:<repo slug>`: the task's branch, its PRs, and
the kinds `pull`, `checks`, `comment`, `review`, `push` and the progress
kinds `workflow`, `job`, `check` (the CI view shows those). It is posted
again when the PRs change and every 25 days while the task is open (a
subscription lapses after 30), and deleted when the task is cleaned up or
its conversation deleted. A project whose policy sets `autoLabel` keeps one
issue subscription per repo (`issues:<pid>:<repo slug>`). From a person's
partition a subscription is that person's at the provider (`for:
user:<id>`); team definitions subscribe to nothing. A provider without
events (no `events` cap) keeps none: polling stands in.

**Routing.** An event finds its task by its pull request, then its
branch, then its head sha (each task's, in that provider and repo);
progress events (`workflow`, `job`, `check`) go to the CI view only. Then:

| Event | When | What happens |
|---|---|---|
| `checks.completed` | on the task's current head (else ignored: superseded) | after `policy.ci.delaySec` (60 s) the head's checks are read (`GET /scm/checks`, through `POST /scm/poll`) and acted on as below |
| CI failing | each failing suite on the head, once | with `policy.ci.autoFix`: a task input — `[scm: CI failed on <branch>@<sha7> — untrusted output]`, up to 3 failing jobs each with its failing step and its log's last 120 lines (ANSI stripped, redacted; only the steps while a host serves no log of a running job), ≤ `policy.ci.logBytes` in all, framed as untrusted, then `— fix it and push.` — and a quiet `ci.failed` event; at most `policy.ci.maxPerDay` (5) such inputs per task per day, past which a waking `ci.stuck` event instead (the coordinator's `ci-stuck` push). Without `autoFix`: the `ci.failed` event alone. The PR's checks say `failure` |
| CI green | on the head, the PR open | the PR's checks say `success` — the task **awaits review** — and a waking `pr.ready` event (the `pr-ready` push) |
| `review` (changes requested, commented), `comment` on the task's PR | after `policy.reviews.batchSec` (120 s; later ones join the same read) | the PR's timeline is read: words from an `OWNER`, `MEMBER` or `COLLABORATOR`, or a login in `policy.reviews.allow` (everyone with `forward: "all"`, nobody with `"off"`), go to the task as one input — each with its author and, inline, `path:line`, ≤ 8 KiB, framed as untrusted — with a quiet `review` event; anyone else's is a quiet `comment` event, "not forwarded". Approvals and dismissals aren't forwarded; each entry is taken once |
| `pull.opened`, `reopened` | the PR's head is the task's branch and the task doesn't hold it open | the task's refs check runs again and records the PR |
| `pull.merged`, `closed` | the task's PR | the PR's state; with none of the task's PRs open, its phase `merged` (one was) or `closed`, its credentials scrubbed (its own sandbox's; the project's when no other task of the project is open), its workspace cleaned up as `policy.cleanup` says, and a waking `merged` / `closed` event |
| `pull.synchronize`, a `push` to the task's branch | | the task's head moves (the PR's checks unknown again) |
| a `push` by anyone but the task | | a quiet note queued to the task — someone else pushed to its branch: pull before pushing — and a quiet `push` event |
| `issue.opened`, `labeled` | a repo of a project with `autoLabel` | a quiet `issue` event (the title framed as untrusted); waking when the issue carries the `autoLabel` label |

Every input to a task goes through the project's queue (source `event`,
held while its conversation waits for a person) and the pump. The
provider's own app (`actor.self`) and the task's own identity (the login of
its credential) are ignored — no note, no forwarded words, no refs check —
except for the facts they carry: a head they moved, a PR merged or closed,
and CI's result on a commit, whoever pushed it.

**Polling.** While a task has an open PR the agent also reads, per repo,
the head's checks, the PR and its timeline, conditionally (`POST
/scm/poll`, one call per provider and identity per pass, at most its
`limits.pollItems` items; each route's own read where a provider has no
`poll`). Without events for the head: every 2 minutes for its first 20
minutes, every 10 to 2 hours, every 30 to 24 hours, then it stops — with a
waking `note` "lost track of CI — check manually" for checks still
pending. With healthy events (the provider's hello says so, a delivery
for the repo in the last 30 minutes, or one for the head) only a safety
read every 15 minutes once the head has waited 30. Never sooner than the
provider's `events.pollMinMs`. Checks that finished stop until the head
moves; checks nobody reports for 30 minutes (a repo without CI) stop
quietly. A changed read is handled as the event would be. The reads run in
the background while the agent runs, never keeping it up; an agent
stopped with reads due — a person's partition at rest, the shared instance
or an unpartitioned agent idle-stopped — is started again at the next
read's (or subscription renewal's) minute, and a delivery or a tick that
starts the agent makes the pass at once.

**Once only.** An event id is taken once for each `for`; a fact both an
event and a read describe is acted on once — a failing suite on a head,
green CI on a head, a review entry, a merge — whichever comes first.

**Rolling back.** A build without scm events leaves its tables
(`project_refs`, `scm_poll`, `scm_seen`, `scm_subs`) alone and answers the
provider's deliveries 404 (no such route): the provider drops them, and
the subscriptions lapse within 30 days. The next upgrade picks the tables
up as they are.

### Team projects

A team project's definition and board in the shared space, each member's
own half in their own space with their own sandbox, tasks and
coordinator, the optional seed sandbox, how the board stays current, how
a member sees and accepts the team's changes to setup and policy, and what
happens when a member leaves. Described when it lands.

### CI in the conversation

What CI is watched (a task's branch and pull request, the branches a
coding session pushed, a branch or pull request a person names), the
routes under `/runs/{id}/ci`, the `ci` stream event, job logs and
annotations (untrusted text, redacted), re-running failed jobs, and how
the conversation shows it: the CI chip beside the coding agents chip, the
CI section of their dock, outcome cards and the board's chips.
Described when it lands.

### Projects in the UI

**The Projects page** opens from the sidebar's **Projects** entry (under
Automations; its badge counts the tasks that need you across your active
projects) or the address `#proj`; one project is `#proj=<id>`. Its id says
where it lives (`model/homes.js`): in a person's partition their own
projects are in their partition and a team project's definition in the
shared space, and the page lists both (every page of `GET /projects`
at each) — yours, then team projects, archived ones last — each with its
repos, the slots at work and its counts per column. A backend without Projects (`GET /projects` 404) shows no
entry.

**A project's page** has two tabs:

- **Board** — a column per state (queued, working, needs you, PR, done:
  `TaskView.column`), each task a card: `#n`, its title, its state (and
  what it waits for), its branch, its pull requests (↗ to the platform;
  their checks only while the task has no CI summary — then the CI chip
  says it) and the chips other modules add (`ext.card(task)`: CI's). A
  card opens its task's conversation. **New task** (participants): what to
  do, a title, small or big, who works on it — the project's default,
  said as what it does (its policy's coding agent, else the one you used
  last, else the built-in agent; the built-in agent when the policy says
  so or no coding agent is available), or a coding agent of the catalog
  by name — which repos —
  `POST /projects/{pid}/tasks`. **From issues…**: a repo's issues (open or
  closed, words), up to 20 picked, a task each —
  `POST /projects/{pid}/tasks/batch`; a refused issue is said. Issue text
  is the issue tracker's — anyone may have written it — so it is drawn as
  plain text, clipped, marked as untrusted; never markdown or HTML.
  **Warm** starts the sandbox, fetches and refreshes the credentials. A
  search box and "mine" narrow the board. A team project's definition
  (`kind: "team"`, at the shared space) has no tasks of its own — they run
  in each member's own space — so its board is a line saying so, with no
  task actions and no read of tasks.
- **Settings** — everyone who sees the project reads it; its owner changes
  it. **Status**: the sandbox, each repo (fetched, head, whether its base
  branch is protected), each credential's metadata (whose, its state, until
  when, why it is blocked — never a token), the jobs, warnings; Warm; your
  sign-in to the provider (below). **Repos**: add one by `owner/name`,
  remove one (confirmed; again, with `force`, when open tasks use it), each
  one's setup script and checkout. **Policy**: every key, grouped (tasks;
  branches and pull requests; CI and reviews; workspace, ports and setup;
  big tasks; cleanup; the coordinator — its class of new tasks lists only
  classes without internal reach, as a task may not have it), saved with
  the version the edit
  began at — when someone saved a change meanwhile (412) the project is
  read again, their change shown with yours, and the next Save saves
  both; keys this build doesn't know are kept as stored. A rename and the
  team visibility are sent with the version they began at too. **Members** and what team visibility
  grants, where sharing is possible (an unpartitioned agent's projects;
  never a person's own project in their partition, which is theirs alone).
  **The project**: rename, archive (its credentials leave the sandbox) or
  unarchive, delete — confirmed, keeping its sandbox or, when the project
  made it, deleting it too (that choice is the project's own: it keeps no
  hold on the next project's tab).

**A new project** (the page's **New project**): the scm provider
(`GET /projects/scm` — each bound one as this home sees it, what it says
of you), the repos — a picker of what you can reach through it
(`GET /projects/scm/repos`), each with an optional setup script — a name,
its sandbox (a new one: manager, image, size, network, internet by
default; or one of your own private sandboxes) and the policy basics
(tasks at once, who answers tasks, pull requests opened by hand or as a
draft or ready when a task rests, whose identity it works as when the
provider offers both). In an unpartitioned agent it may be shared with
everyone who can open the agent at once. At a partitioned agent's shared
space the form makes a team project's definition (`kind: "team"`), sent
shared — with everyone who can open the agent, or only the members added
next — its seed sandbox optional (none at first). A seed picked from your
own sandboxes is one you have shared with the team: an existing sandbox
keeps its own visibility, and members' sandboxes fork from the seed only
when they can see it, so the form offers only those (a private one of
yours must be shared first). Then `POST /projects` and its page.

**Signing in to the provider** (a person's partition, where projects use
your own sign-in): offered only there, and only when the provider lets you
work as yourself (its `you.identities` has `person`) — the sign-in routes
answer 409 anywhere else, so an unpartitioned agent or the shared space
says its projects work as the provider's bot (at the shared space, that
each member signs in from their own). **Sign in to ‹provider›** starts the device flow
(`POST /projects/scm/signin`) and shows its page and code — your own,
read from your own space, to you only — polling until it is done (a
failed poll is tried again, later each time; a task's card counts only
its own sign-in as done). A sign-in already pending when the Settings tab
reads it (`GET /projects/scm/signin` — a parked task's, or one started
elsewhere) is followed the same way; only the latest one is polled, and an
answer of an earlier one, or one after Forget, changes nothing.
**Forget** (`DELETE /projects/scm/signin`, confirmed) removes your
projects' credentials from their sandboxes first.

**A task's conversation** (a run with origin `project`, kept out of the
conversation list) shows its project:

- before its title, **‹project› ›** — back to the project's board (`project`
  in the run view);
- in the top bar, its **branch** (↗ to it on the platform), each **pull
  request** with its state (↗; its checks while the task has no CI summary)
  and the setup outcome; **Open PR** on a task with a branch and no open
  pull request, for people who may act on it, once the backend is known to
  have `POST /runs/{id}/task/pr` (a `GET` of it answers 405 there, 404 where
  it isn't — asked once, nothing opened to find out), confirmed;
- at the end of the transcript, while its workspace is prepared, the
  **prep card**: what is under way, a step per repo (its checkout, its
  setup's exit), **Retry** when it failed (`POST /runs/{id}/task/retry`);
  when it needs a sign-in, the **sign-in card** — the provider's page and
  code, shown only to the person who must sign in (the run view carries it
  to them alone), polled until done, then the task is looked at again
  (`POST /runs/{id}/task/refresh`); anyone else reads whom it waits for;
- in the unfolded pinned task, its project, number, size, repos, issue,
  checkouts and ports.

**Kept current**: the `project` stream event (`{id, change, n}`) carries no
data of its own; the page reads the list, the open project and its board
again, and an open task conversation its task (`GET /runs/{id}/task`) —
events coalesced for 250 ms into one read each.

**For a view** (the model; `model/` as above):

| Module | What it holds |
|---|---|
| `model/projects.js` | `createProjects(app)` → `app.projects`: `list`, `load()`, `open(pid, tab)`, `opened`, `board(pid)`, `tasks(pid, filter)`, `take(ev)`, `create(body)`, `createTask(pid, spec)`, `batch(pid, issues)`, `patch(pid, body)`, `remove(pid, sandbox)`, `status(pid)`, `warm(pid)`, `issues(pid, q)`, `signin(scm)`, `forget(scm)`, `pending(pid)`, `accept(pid, hash)`, the forms (`newProject`, `newTask`, `openPicker`, `editPolicy`) and `POLICY` (the policy's keys, grouped); it emits `projects` |
| `model/project-task.js` | a task's words: `columns`, `columnOf`, `cardWords`, `stateWords`, `taskChips(view, project)`, `prChip`, `setupOutcome`, `prepCard(view, me)`, `prButton(view, route)`, `crumb(view)`, `taskSection(view)` |
| `model/project-api.js` | `projectApi(app, pid)`, `taskApi(runId)`, `scmApi(home)`, `listProjects`, `createProject` — each call at its home, a refusal kept whole (`e.status`, `e.refusal`, `e.data`) |
| `model/router.js` | `#proj`, `#proj=<id>` (`parse().proj`, `projHash`); `app.openProjects(pid)` |

On the web, `project-web.js` lists the modules: `projects.js` (the entry,
`ext.side`; the page, `ext.page('projects')`), `project-new.js`,
`project-settings.js` and `project-chips.js` (`ext.top`, `ext.crumb`,
`ext.end`, `ext.task` on a project's conversation).

## The frontend: one model, thin views

The tile's state and behaviour live in **`model/`** — plain ES modules with no
lit and no DOM — and each way of showing the tile is a thin view over it. The
web view is the files you know (`agent.js`, `chat-cards.js`, `sidebar.js`,
`home.js`, `share.js`, `classes.js`, `sandboxes.js`, `automations.js`,
`auto-*.js`, `index.html`); the
**native view** is `native.js` and `native/` — what the xbin app draws with
platform controls (`/vendor/xb-native.js`, docs/frontend-kit.md). Both draw
the same model.

| `model/` | What it holds |
|---|---|
| `app.js` | `createApp()`: the model in one object — where you are (`sel`, `page`), who you are (`me`), the class for new asks (`classes`, `classId`, `pickClass`; `toolset` is its lane), what needs you, the halt switch, the composer's attachments and sending — wired to the one live stream; views subscribe with `app.on(event, fn)` |
| `session.js` | the open conversation: its views, the model calls in flight, `shown()` (what the chat draws), `blocks(id)` (a held run folded through its cache); a long one held as a run of pages — `loadOlder()`, `keep(lo, hi, canDetach)` (let go of what lies far from the blocks drawn), `loadNewer()`, `latest()`, `follow(atBottom)`; `failed` (`Failures`: a card's child whose read failed — `loadChild` and `loadTail` wait before reading it again; `ui.readError(id)` says why, `ui.act.retryRead(id)` is the card's Retry) |
| `fold.js`, `tool-heads.js` | a run's view → chat blocks (with a `FoldCache`, only the blocks whose message, result, step, link or subagent changed are rebuilt; the rest come back as the same objects); a tool call's headline, family and state |
| `conv-list.js`, `conv-groups.js` | the conversation list: paging, search, pins, read state, live updates; date groups |
| `stream.js` | the live connection (`GET /stream`, resumable) |
| `actions.js` | the calls a view makes: ask, send, attachments, control, halt, the class pick, row actions, sharing, joining, and the settings (config, models, features, classes), a run's memory and files, the skill library |
| `rules.js` | who may do what and what the controls say: the top bar, the composer's state, the halt switch, a row's menu, the share dialog |
| `router.js` | addresses: `#c=<id>`, `#auto[=kind:id]`, `#join=<token>` |
| `auto.js`, `auto-channels.js`, `auto-triggers.js` | the Automations page's state, its kinds (`registerKind`), and each kind's actions |
| `home.js` | `HOME` — the home view's words — and what "Needs you" says |
| `features.js` | `FEATURES`: every feature of the UI by key, and the intended differences between views |
| `classes.js` | agent classes (D116): the composer's picker and your pick, the conversation's badge, the managers' editor (a class as a form, its checks, what a save sends), an automation's class (its forms' choices, what its card says, a channel's two classes) |
| `harness.js`, `harness-heads.js`, `harness-store.js` | coding agents (Claude Code, Codex, Gemini CLI, opencode in a coding sandbox — §Coding agents): a harness run's summary (`run.harness`) in words — its state, park, activity, counts, usage, plan, mode — and the catalog (`GET /harnesses`: why one isn't available, the class a conversation starts in, whether a sandbox fits); a harness call (`acp:<kind>`) as tool-heads.js says a built-in one; `app.harness` — the catalog, "Who answers" (`prefs/agent`), the sandbox last used per harness (`prefs/harness-sandbox`), Auto / Always approve per harness (`/prefs/harness-mode`), what a new ask carries, and a harness run's calls (mode, options, a permission's option, a question's answer, sign-in, the adapter's log, a message that interrupts) |
| `harness-manage.js` | the Coding agents catalog as the managers' view says it (`catalogRows`, `modesWords`, `probeTargets`); the class editor's toolset and checklist are `classes.js`'s (`harnessNames`, `harnessWhy`) |
| `harness-start.js` | starting a conversation with a coding agent: "Who answers" (`agentPicker`), the sandbox it starts in (`sandboxOptions`, `preferredSandbox`, `createPrefill`), the home's setup card (`setupOf`), a row's kind and the top bar's chip (`kindOf`, `topChip`), the new-chat dialog's part of the ask (`newChatPick`); `keepSandbox` keeps the next chat's sandbox one the coding agent picked fits (wired by `createApp`; `app.newClassId()` is the class a new ask starts in) |
| `harness-ask.js` | a coding harness asking and driven, in words both views draw (below): a permission request as its own options (`permission`: reject first when it defaults to no, an explicit option the owner's only, the call, a diff preview, what "always" remembers; a plan approval with its plan), a question (`question`, `formFields`/`formContent`/`missingRequired`, `nativeSchema`/`nativeContent` for the native `question`; url mode), the live mode and options (`controls`), Auto / Always approve (`settingOf`), the slash menu (`slashCommands`, `slashMatches`), and the composer while a turn runs (`steerWords`; `steerTrack` notices a message steered into it) |
| `ext.js` | seams: named hooks a view calls at fixed points of its drawing, filled by feature modules (below) |
| `sandboxes.js`, `sandbox-store.js` | coding sandboxes (D115): the composer's picker, the ▣ badge and why a binding no longer resolves, the Sandboxes dialog's rows and their actions, the create form, a terminal onto one (its manager's `tty` — or, for the native view, the tile's relay (`RELAY`, `relaySrc`): the route, a command, whether it is offered and why not), sharing one with a terminal tile (`shareForm`); `app.sbx` — the list (in a person's partition, where the open conversation lives: `listAt(home)`), the next new chat's pick, binding, the working directory, detaching, creating, the lifecycle, sharing (`shareTerminal`, `unshare`), the run events that carry a binding, ending a terminal's shell |
| `homes.js`, `home-api.js`, `moves.js` | a partitioned instance's two homes (a person's own partition, the shared space): a conversation's home by its id, calls and streams sent there; a shared conversation that moved to your own space, followed (`movedTo`) |
| `harness-homes.js` | coding agents in a partitioned instance (§Coding agents, "In a partitioned instance (the UI)"): whether this page starts one (`harnessesHere`), whether a sandbox is your own space's (`homedWhy`), where a sign-in is offered (`signInAway`), a shared new chat's "Who answers" (`sharedNewChat`) and the sandbox it takes along (`sharedSees`), a run in the shared space that isn't driven (`barredWhy`), and that a coding agent's conversation never moves (`keepsHome`, `unshareWhy`) |
| `harness-child.js` | a coding agent the agent started, as its card in the parent's chat (`childCard`: its state, status line, where, counters, park, what it may do; `childRun`: the link's child with the stream's newer summary; `tailOf`, `loadTail`: its last blocks, read once; `tailError`: why they couldn't be), and a row's coding agents at work below it (`kidsWords`) |
| `harness-board.js` | the Coding agents board: `app.board` (`createBoard`, wired by `createApp`) — `rows(root)` (a conversation's tree, or at home yours at work: each row a child card and its section), `chip(root)`, `delegated(v)`, `take(ev)`; the words (`chipWords`, `filterWords`, `sectioned`, `emptyWords`, `delegatedWords`) |
| `terminals.js` | the terminal dock's tabs (`termsOf(app)`: open, show, hide, close, a New shell in place — page-level, not a conversation's), a coding agent's run relay (`runTerminalSrc`), and the sign-in card (`signIn`): a login park's methods, the sandbox whose home the credentials land in, whether it is shared (a confirm), whom to ask, and whether that sandbox is gone or its manager down (`gone`, `goneText`), and whether it offers the guided sign-in and its Remember (`guided`, `remember`) |
| `harness-signins.js` | a coding agent's sign-ins (D179): the guided sign-in's steps and words (`newGuided`, `guidedStarted`, `guidedFailed`, `guidedFinished`, `guidedWords`), whether Remember is offered (`rememberOf`), saved sign-ins (`signinsOf`, `signinGroups`, `statusOf`, `keyFor`) and a conversation's account and its switch (`accountOf`) |

`createApp({deltas, page})`: drafts arrive as deltas (`/stream?deltas=1`,
"Deltas" above) and the open conversation is read in pages (`?limit=`,
"Paging the view"); both views pass them (without them a view reads whole
views and full drafts, as an instance's own view may). A long conversation
is held as a run of consecutive pages: the newest when it opens,
`Session.loadOlder()` the page before them, and `keep(lo, hi, canDetach)` —
given the block indices a view draws — lets go of the messages about a page
or more beyond them, older ones always, newer ones (the live tail with them:
`shown().detached`, and what arrives meanwhile is counted, `shown().fresh`)
only while the reader is away from the bottom; `loadNewer()` reads them back
a page at a time, `latest()` starts over from the newest. Cuts fall at a
message, so what goes is what a page read brings back, and blocks keep their
ids: a view that anchors by id keeps the reader's place. A reset or resync
reads the pages held again (the rows stay). The web draws a window of the
blocks (`chat-window.js`, over xbind's `/vendor/scroll-window.js`: nothing
the reader looks at moves; the "↓ N new — jump to latest" pill); the native
view draws the tail and grows it on `more`, trimming it — and letting go
above — only while `scrolled` says the reader is at the bottom (the app's
transcript keeps its bottom still). An app that uploads picked files itself
asks `app.uploadTarget()` where (the open run, or at home the new ask's draft
`app.draft`) and hands the answer to `app.attach.uploaded({…, at: app.place})`:
such a chip stays where it was picked (`app.attach.here(place)`), and Send at
home sends the draft (`POST /ask {draft, files}`).

| The native view | What it draws |
|---|---|
| `native.js` | the entry: one surface at a time — home or the open conversation (a subagent's parents under it; back goes up), the Automations screens, pushed tools — plus the conversations drawer and the sheets; deep links (`#c=`, `#auto`, `#join=`) and a restarted runtime (`xbin.native.state`) go through `model/router.js` |
| `native/chat.js` | the conversation: `fold()` blocks as the chat family (`message`, `thinking`, `toolcard` with a subagent's transcript inside, `step`, `activity`, `approval`, `question`), the composer (attachments the app uploads to `PUT /runs/{id}/upload`, or at home into the new ask's draft, `PUT /ask/upload?draft=`), the top bar as the toolbar's menu |
| `native/home.js`, `native/convs.js`, `native/share.js` | home and Needs you; the conversations drawer (a `sheet edge="leading"`), new chat with options, rename; the share sheet |
| `native/tools.js`, `native/settings.js` | memory, files (+ editor, share/export), skills, the workflow tree, one call in full, the render preview (a `canvas html=` island, `native/render-doc.js` — the web's CSP); settings for managers |
| `native/classes.js` | agent classes: the Class picker in the home toolbar, the new-chat sheet's class, Settings → Classes (the list, one class's form), an automation's class row and picker |
| `native/sandboxes.js` | coding sandboxes: the Sandbox picker in the chat and home toolbars, the ▣ in the subtitle and the broken-binding notice, the Sandbox screen (⋯ → Sandbox), the Sandboxes screen and the create form |
| `native/terminal.js` | the Terminal screen (the app's `terminal` on the tile's relays) and a coding agent's sign-in: the notice, Sign in in the composer and ⋯, the Sign in screen |
| `native/auto.js`, `native/auto-channels.js`, `native/auto-triggers.js` | the Automations screens for all four kinds |
| `native-features.js` | `IMPLEMENTS`: what the native view implements, by feature key (as `web-features.js` for the web) |
| `native/ext.js`, `native/harness-all.js` | the native view's seams, and the feature modules that hook into them (below) |
| `native/harness-start.js` | starting with a coding agent: "Who answers" at the top of the home page, the home's setup notice, the new-chat sheet's section, and a coding agent's chip, plan and context at the start of the conversation's subtitle |
| `native/harness-cards.js` | a coding agent's calls as `toolcard`s (`code`, `diff`, a Task's nested `transcript`), one call in full, and the Progress screen (`plan`; the plan's progress and the context in use start the subtitle: `native/harness-start.js`) |
| `native/harness-ask.js` | a coding harness asking and driven: its permission as an `approval` (its options; a bypass one confirmed by a second approval), a plan above it as `markdown`, a `diff` preview, its question as a `question`; the toolbar's Mode menu (its config options but the model, and your Auto / Always approve) and the Model picker; the composer's slash commands, Send now (interrupts); ⋯ → Coding agent settings (the main menu: home's and the drawer's) → your setting per harness |
| `native/harness-child.js` | a coding agent the agent started, as the spawn's `toolcard` in the parent's chat (its park inside it, answered on the child's run), and Cancel task in a harness child's own ⋯ |
| `native/harness-catalog.js` | Settings → Coding agents (the managers' catalog: `model/harness-manage.js`) |
| `native/harness-board.js` | the Coding agents board: its screen (sections Needs you, Running, Done; a row's swipe Stop, Message, Cancel task; a parked row's approval or question), the Message screen, the toolbar button and ⋯ item, the Task screen's Delegated section |

**Seams.** A feature can land as a module of its own instead of edits to the
views' hot files: it registers hooks on a view's seams when imported —
`web-ext.js` (`ext.register({block, end, top, paint, newChat, task})`;
`ctx.app`, `ctx.paint()` once agent.js starts) for the web, imported from
`harness-web.js`; `native/ext.js` (`block`, `end`, `toolbar`, `subtitle`,
`menu`, `main`, `composer`, `newChat`, `screen`, `task`; the native `ctx`
as before) for the native view, imported from `native/harness-all.js`. A hook answers a template, or
null when the block, run or screen isn't its: `block` replaces the built-in
card of a transcript block, `end` adds to the end of the transcript (a
coding harness's park is then its to draw), `top`/`toolbar`/`menu` add
controls (`main`: the main ⋯ menu — home's and the drawer's), `subtitle`
words for a conversation's subtitle, `composer` a placeholder, slash commands and buttons, `newChat`
a field of the new-chat dialog and its part of the ask, `screen` a pushed
native screen of its own kind, `task` a part of the unfolded pinned task
(the native Task screen). Each file's header says the signatures; a
hook that throws is logged and skipped. An instance can add modules of its
own the same way. The coding harnesses' UI is built on them, tested
against the STUB's harness routes and `test/harness-fixtures.mjs` — which
may use only what the real backend produces: `_backend/testdata/
harness_shapes.json` records every shape's key paths and JSON types
(`TestHarnessShapes` makes the backend produce them and fails when it stops
producing one; regenerate the file as its header says), and
`hack/agent-template-harness-shapes.test.mjs` fails on a path a fixture or
the STUB serves that the file lacks.

**A coding agent's transcript** (`harness-cards.js` on the web,
`native/harness-cards.js`; the words are `model/harness-heads.js` and
`model/harness.js`). Each `acp:<kind>` call is a card of its kind, drawn
from its tool row's `acp`: a command (the command, its output without
colour codes — the last 20 000 characters, all of it on asking — and the
exit code), an edit (a row per file, `+a −d`, unfolding to its patch; the
web highlights patches with xbind's `/vendor/bx-code.js` when the page can
import it, else draws them plain; native uses `diff`), read, search, fetch,
delete, move, think, switch_mode and other; its chip says pending, running,
needs approval, failed or cancelled, and a failed one opens by itself. A
Claude Task's steps and text sit inside its card (the fold's `kids`); one
whose Task is paged out shows flat, marked ↳. The top bar (native: a
subtitle, and ⋯ → Progress) carries the context in use and the cost,
what the conversation changed (`counts`), and the 📋 plan, pinned under the
task (unfolding to its entries); all follow the `harness` stream event.

**A coding agent asking and driven** (`harness-ask.js` and
`harness-controls.js` on the web, `native/harness-ask.js`; the words
`model/harness-ask.js`). Its park (`pendingState.harness`) is drawn at the
end of the chat — the `end` seam, only for a permission or a question: the
harness's own options as buttons, reject first when it defaults to no; an
option with `explicit` (it raises the session to a bypass mode) only for the
conversation's owner, a person, marked ⚠ and confirmed; the call's title,
command and a diff preview; what `allow_always` would remember; an optional
word sent with a rejection (`POST /runs/{id}/approve {park, option,
feedback?}`). A plan approval shows the plan and a "keep planning" box (the
feedback of its rejection). A question is a form from its schema (Submit:
`POST /runs/{id}/harness/answer {park, action: "accept", content}`; Skip:
`decline`); url mode shows the page, then Done. The web's `#hctl` (the last of
the composer's pickers) switches the live mode and config options (`PATCH
/runs/{id}/harness {mode}` / `{option: {id, value}}`; a bypass mode ⚠, the
owner's only, confirmed) and holds your Auto / Always approve for the
harness (`PUT /prefs/harness-mode/{id}`) — at home, for the harness that
answers new chats; the built-in model picker hides in a harness
conversation. Typing `/` offers its advertised commands. While a turn runs
the placeholder says whether a message steers it or waits for it, the
queued chips say so too, a message steered into the turn is said for a
moment, and ⌘/Ctrl+Enter sends it with `interrupt: true` (`app.send(text,
clear, {interrupt: true})`; the native composer's Send now). Native: the
toolbar's Mode menu (the options but the model too) and Model picker, ⋯ → Coding agent settings (the main menu) for your setting.

**Customising an instance.** A persona or domain changes `HOME` in
`model/home.js`. The web files keep their names, and the modules that moved
into `model/` (`chat-fold.js`, `tool-heads.js`, `conv-groups.js`,
`stream.js`, `conv-list.js`) are one-line re-exports at their old paths, so
imports and patches written against the old layout keep working.
`chat-view.js` is now the model's `Session` plus the web's `template()`;
`automations.js` still exports `registerKind` and `AutoPage` (a view adds its
drawing to a kind the model registered with `extendKind`).

**The rule: a UX change lands in the model and in both views in the same
change.** `model/features.js` lists each feature by key; each view declares
what it implements (`web-features.js` for the web, `native-features.js` for
the native view), and `hack/agent-template-features.test.mjs` in the xbin
repo fails when a view misses a key that is not listed as an intended
difference (`DIFFERENCES.web` / `.native`) with its reason. A change that is
only for one view says why it is view-specific there. Model logic never
touches the DOM or opens a dialog (the web asks "are you sure?", the native
view puts a `confirm` on the button, before calling a model action);
`hack/agent-template-model.test.mjs` runs the model in node against a
scripted backend to keep it that way. The native view imports only the
model, `/vendor/xb-native.js` and the kit — never lit or the web's views.

**Testing the native view.** `hack/agent-template-native.test.mjs` renders
`native.js` in node (`hack/xbn/node.mjs`) against the web tests' fake backend
(`test/backend.mjs` STUB, through `test/native-stub.mjs`) and asserts what a
person gets — an approval card with Approve and Deny, Retry on a failed run,
a disabled composer in a view-only conversation, senders in a shared one.
`node test/native.mjs` runs it in a real browser the way the app does (the
runtime, the tile's `native.js`, the reference renderer as the app) and
walks home, the drawer, a streamed answer, sending, Files, the render
preview and an approval. `node test/native-shots.mjs` draws the key screens
with the reference renderer at 390×844, light and dark — look at them.

## JavaScript sandbox (REPL)

Feature key `repl` (on by default). A per-run **goja** VM — pure Go, no JIT, no
native code — over the session files: `js_eval` (evaluate code; state persists
across calls) · `js_run` (evaluate a session file) · `js_reset`. Inside the VM:
`console.*`, `files.read/write/list/exists/remove(path)`, and `load(path)`. It
is how the agent computes instead of guessing, and how it draws a chart for
`render_html`: build inline SVG in `js_eval`, `files.write` it into an .html
file, render that.

**There is no host surface.** `goja.New()` ships no `require`, `process`,
`fetch`, `setTimeout` or `Buffer`, and the parser's default filesystem
source-map loader — goja's one way to read a host file, reachable from a
`//# sourceMappingURL=` comment at parse time — is disabled. Nothing bridges the
VM to the network or to `xbin_call`, which is why these tools are offered in
**both** capability lanes and are not `sideEffect()` tools: they mutate only
this run's private rows, so the approval gate never fires for them.

Limits per statement: a 5s uncatchable interrupt (`replTimeoutMs`), a heap
watchdog (`replMemMB`, default 256), a 2000-frame call stack, 8 KiB of captured
output, and a JS prelude capping `repeat`/`padStart`/`padEnd` at 4 MiB — those
allocate in one native call, so no sampler can catch them.

**Sessions survive restarts by replay, not by magic.** Every statement is
appended to `repl_log` as `running` *before* it executes and settled after; a
cold VM re-runs the log. Sound because the sandbox has zero I/O. A row still
`running` after a restart is therefore the statement that killed the process:
it is marked `killed` and never replayed, and two of those disable the sandbox
for that run rather than letting it crash-loop the backend. The clock is pinned
to each statement's log timestamp, so a rebuild reproduces the values the model
last saw — which also means `Date` is frozen *within* one statement.
