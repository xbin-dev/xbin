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
| `GET /needs` | — | what waits for you: conversations where the agent (or a subagent) asks a question or wants an approval, and automations you own whose last run failed and you haven't looked at → `{items:[{run, reason: question\|approval\|failed, subRun}]}` |

Items are run summaries plus `access`, `mine`, `members`, `pinnedAt`,
`archivedAt`, `readMs` and `unread` (activity after you last looked). Your
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
instance made before keeps its mode: unpartitioned, exactly as described in
the rest of this page, until a manager switches it — which deletes every
conversation, all memory and every schedule (`partitionNote`). The code is
the same in all three modes; `xbin.Partition()` picks one at start:

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
    of its conversations.
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
  queueing behind the brake. A partition that hasn't read `conf` yet (the
  global instance never ran, a kv error at start) treats the halt as on
  without cancelling anything: requests for work are queued, runs wait, and
  it looks at `conf` again (with backoff, only while runs wait) until it
  can — then they go on by themselves. An instance whose `uses` lacks
  `conf` (a customized copy) can't read it at all: people's requests for
  work answer 503 saying so — update it from its template.
- **Chat channels are the global instance's** — the messaging bridge
  isn't partitioned, so it reaches the global instance, where the channels,
  their people, links (`POST /adapter/link`) and rules live. From your own
  partition the channels' routes (`POST /channels/{id}/claim` and the other
  `/channels/{id}/…`, `GET /triggers/unmatched`) are forwarded to the global
  instance, attributed to you, and your Automations page lists its channels.
  Group messages, and DMs from chat accounts nobody linked, are the global
  instance's conversations, as ever.
- **A DM from the chat account you linked is yours.** The global instance
  keeps a record of where it came from — the channel, the chat account, the
  reply address and you — and hands it to your partition by partition mail
  (`handoff/dm`: the message, the rules the channel gives it — lane, class,
  `deny`, its system text — and its files inline, up to 640 KiB a message;
  a larger file is named in the text instead). Your partition runs the
  conversation (yours, private, in your list) and mails each reply back
  (`outbox/add`, naming the handoff, its files inline); the global instance
  posts it where **its own record** says the DM came from, and only when
  xbind stamps the mail as yours and the chat account is still linked to
  you — a reply naming someone else's handoff is refused (logged, never
  posted) — once however often it comes, for up to 30 days after the DM.
  `/help` and `/link` are answered by the global instance, the other chat
  commands by your partition. Until your partition has run once (open the
  agent once) your DMs wait in its inbox, and the first one is answered with
  a notice saying so (a partition mails the global instance
  `partition/hello` when it first starts). If xbind refuses the mail for
  good (you can no longer use the agent), or it can't be mailed within
  7 days, the chat is told. What the global instance holds of it:
  the message and its files only until they are mailed (or given up, at
  most 7 days; one person's full inbox holds back only their own); your
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
  per UTC day (a call counts on the day it was made, whenever its
  conversation started); never content or times of day — when it starts and
  at each UTC midnight while it runs. `GET /usage[?days=30]` (managers; forwarded from a
  partition; at most 90 days) → `{days, since, people: [{user, days: [{day,
  runs, llmCalls, promptTokens, completionTokens}], total}]}`; 404 in an
  unpartitioned agent, whose managers see every conversation anyway.
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
  being upgraded". Anything in it is readable by every partition's code.
- **Model calls have a tile-wide cap**: `maxActiveRuns` (default 4) lock
  files `llm.slot.<i>` beside `team`'s file — a flock semaphore every
  partition shares; a dead process frees its slot. As within one instance,
  a subagent's call never takes the last slot, so a new chat waits for at
  most one call to finish however wide everyone's fan-outs are. Each
  partition's own gate allows at most 2 of its calls at once; the global
  instance keeps `maxActiveRuns`. The locks need one kernel: xbind never
  runs a partitioned tile's backend in a VM (`vm` and `partition` don't
  mix). A `team` directory that can't hold the lock files costs only the
  cap (calls and titles go ahead).
- **Resume.** A partition that stops with work leaves the `resume` job only
  for work that moves without its person (a running or queued run, an
  undelivered input, a subagent's settled result its parent will take up, a
  run sleeping until a sandbox job ends), a `wake` job at the minute its
  earliest timed wait ends (a sleeping run's wake, a subagent deadline), and
  nothing for runs waiting on a person — who opens the tile anyway — nor
  while the halt is on. The global instance follows the unpartitioned
  rule.
- **Sandboxes.** A partition calls its sandbox managers as itself: a
  manager whose `hello.caps` carry `partitions` homes the sandboxes it
  makes there ([/docs/sandbox-manager.md](/docs/sandbox-manager.md)). One
  without it isn't used in a partition at all — its hello is refused with
  refusal `partitions` (409), naming it and how to update it, in the tools,
  the catalog and the Sandboxes dialog; the global instance keeps using it.
  A partition also sees the team's sandboxes (`shared`); its conversations
  work only in a sandbox the manager says is homed in the partition
  (`owner.partitionId` its id, `owner.via` this tile, not `shared`) —
  checked when it is bound and at every use; any other is refused (403),
  though you can open its terminal. Every sandbox a partitioned instance
  makes — for a conversation or not — carries the label `xbin.agent/home`
  (the partition's id, or `global`), beside `xbin.agent/conversation` when
  it is made for one.
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
  topics are `handoff/dm` and `handoff/event` (global → a person),
  `outbox/add`, `usage/day` and `partition/hello` (a person → global),
  above; an item's files are stored before its transaction; add yours to
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
| `GET /runs/{id}/tree` | — | the whole workflow this run belongs to: nodes (each with its `link` and `phase`), statuses, blockers, cost. Metadata only |
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

The core tools — `memory_set`/`memory_get`/`memory_delete`, `note`, `recall`,
`message_get`, `finish`, `yield`, `ask_user`, `state_changed`,
`attach_to_reply` — are in every class.
The Features menu can still switch an optional toolset off, and a run's
`deny` list still hides tools.

A class is `{id, name, description?, icon?, toolsets, mcp, managers,
sandboxEgress?, model?, system?, who?}`: `mcp` and `managers` are `"all"` or a
list (MCP server names; sandbox-manager tile paths); `sandboxEgress` is the
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
  files, subagents, skills. No internal reach.

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
| `PUT /classes` | `{classes: [class…], default?, confirmMixed?}` | managers: replace the classes. A built-in left out comes back as its default (old conversations and APIs name it). **409** `{error, mixed: [id…]}` when a class mixes internal reach with egress and `confirmMixed` isn't set; **400** for a bad id (`a–z 0–9 -`, a letter first, ≤ 32), an unknown toolset or egress, a repeated id, an unknown `default`, or a `who` other than `everyone`/`managers`; **400** too for an edit that would take a class a channel runs strangers in — a channel policy's `webClass`, and the built-in `web` for every channel that names none — out of the web lane (losing its egress or gaining internal reach), or delete it (a built-in left out is fine: its default is web-lane); the error names the channel. **400** as well for deleting a class a trigger or a channel policy (`privateClass`, `webClass`) names, and for making mixed a class a public-data trigger runs in (data from outside must not steer a class that can move internal data out; `confirmMixed` doesn't change that — a legacy webhook trigger's is the built-in `internal`); the error names the trigger or channel. Schedules and conversations don't hold a deletion up: theirs fall back to their lane's built-in. Answers as `GET /classes` does |

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
  // spawns per turn, concurrent MODEL CALLS process-wide, and how long a
  // foreground subagent is waited for before it moves to the background
  "maxDepth": 3, "maxSpawn": 32, "maxSpawnPerTurn": 8, "maxActiveRuns": 4,
  "subagentTimeout": 900
}
```

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
| `GET /usage` | `?days=30` (1–90) | people's daily usage totals (managers; a partitioned agent's global instance, forwarded from a partition): `{days, since, people: [{user, days: [{day, runs, llmCalls, promptTokens, completionTokens}], total}]}`; 404 unpartitioned |

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
  disable) leaves a one-shot `resume` cron job (in the `beat` resource) that
  starts the backend again; the next owner deletes it, and the pre-D81
  `heartbeat` job, at takeover. `POST /tick` is the idempotent recovery scan it
  calls.
- **Model calls are gated, not runs.** `maxActiveRuns` bounds concurrent model
  calls. A top-level run's call goes first, and subagents may hold at most
  `limit − 1` slots, so a new chat never waits behind a fan-out.

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
| `subagent_spawn {task, label?, wait?, timeout_s?, after?, system?}` | start a subagent. `wait:true` (default) waits for its answer — several in one step run in parallel, and their answers land in call order in one step. Past `timeout_s` (default `subagentTimeout`) the wait ends with a progress digest and the subagent **moves to the background**; its answer arrives later. `wait:false` starts it in the background at once. `after:[ids]` starts it once those runs settled, with their results in its first message |
| `subagent_wait {ids, mode?, timeout_s?}` | wait for background subagents: `all` or `any`; answers for the settled, digests for the rest |
| `subagent_status {ids?}` | phase, elapsed time, model calls, its latest tool summaries, pending approval, queued messages |
| `subagent_result {id, offset?, limit?}` | page through a long answer |
| `subagent_message {id, text, wait?}` | steer a working subagent, or give a finished one a follow-up |
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
spawn→finish→spawn cannot loop forever), `maxSpawnPerTurn` 8, enforced when the
spawn runs. Subagents get neither `ask_user` nor `schedule` (a cron-agent
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
sandboxes it created and those shared with it (its **partition**).

**References.** A sandbox is named `<provider>[#inst]|<id>` — the manager
tile as its binding names it and the manager's id — always qualified, so a
stored reference keeps naming the same sandbox however many managers are
bound. In a URL path it may be sent as is or percent-encoded.

**People (D83).** Every call the agent makes to a manager names the person
it acts for in `Sbx-User` (asserted: the manager records it as the owner of
what it creates); the agent enforces who may do what:

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
| `GET /sandboxes` | `?fresh=1` skips the cache | `{sandboxes: [{ref, provider, manager, …the contract's sandbox…, mine, canUse, canManage, canEdit, boundTo?}], managers: [{provider, title, ok, error?, refusal?, caps, egress, images, sizes, limits}]}` — every sandbox the caller may see across the bound managers, and those bound to a conversation the caller sees (`boundTo`: its ids). Merged, cached 15 s (the agent's own changes show at once); `manager` is the manager's title. Anyone who can use the tile |
| `POST /sandboxes` | `{name, provider?, image?, size?, egress?, visibility?, members?, conversation?, bind?, cwd?, clientId?, start?}` | **201** + the sandbox (as below), with `binding` when it was bound. Created at `provider` (optional while one manager is bound), owned by the caller. With `conversation` (the caller takes part in it): made for it (above) and bound there unless `bind: false` — refused up front when its class wouldn't allow it, and deleted again if the binding fails. `clientId` makes a retry return the same sandbox (per person) |
| `GET /sandboxes/{ref}` | | one sandbox, fresh from its manager, as `GET /sandboxes` lists it |
| `PATCH /sandboxes/{ref}` | `{name?, visibility?, members?, shares?, labels?, egress?, size?, autoStopMin?, version?}` | the sandbox — its owner's (the contract's `PATCH`; `restartNeeded` when a change waits for the next start, and `egressNext` while an egress does). New `labels` keep `xbin.agent/internal` (sent with the sandbox's `version` unless you send one: a label set meanwhile is read again and kept) |
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
  active one (`{detach}`) and opens Manage.
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
  …/execs/{id}` at the manager, from the page). One terminal at a time;
  one left by a page that closed runs on until its manager ends it.
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
  Manage sandboxes…). The Sandboxes screen puts each row's actions behind
  its swipe and ⋯ (Archive and Delete confirmed), New sandbox pushes
  the create form, and Share with a terminal tile… pushes its form (Stop
  sharing behind a share's swipe, confirmed). A ▣ tool card is a `terminal` icon; what the call came to
  is a chip (`exit 1 · 14s · job 3` in red), its command the card's first
  line. It opens no terminal: the app's `terminal` dials only the tile's own
  routes, and a manager's `tty` is another tile's (a D96 difference,
  `model/features.js`).

**Subagents on another sandbox.** `subagent_spawn` also takes `{sandbox?,
cwd?}` where a sandbox is bound: `sandbox` names one of the conversation's
attached sandboxes (a ref or a unique name; any other is refused), which
becomes the subagent's active one; `cwd` is its working directory there
(absolute, or relative to that sandbox's). The subagent keeps the attached
list, and the root's binding is unchanged.

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
| `session.js` | the open conversation: its views, the model calls in flight, `shown()` (what the chat draws), `blocks(id)` (a held run folded through its cache); a long one held as a run of pages — `loadOlder()`, `keep(lo, hi, canDetach)` (let go of what lies far from the blocks drawn), `loadNewer()`, `latest()`, `follow(atBottom)` |
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
| `sandboxes.js`, `sandbox-store.js` | coding sandboxes (D115): the composer's picker, the ▣ badge and why a binding no longer resolves, the Sandboxes dialog's rows and their actions, the create form, a terminal onto one (its manager's `tty`: the route, whether it is offered and why not), sharing one with a terminal tile (`shareForm`); `app.sbx` — the list, the next new chat's pick, binding, the working directory, detaching, creating, the lifecycle, sharing (`shareTerminal`, `unshare`), the run events that carry a binding, ending a terminal's shell |

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
| `native/auto.js`, `native/auto-channels.js`, `native/auto-triggers.js` | the Automations screens for all four kinds |
| `native-features.js` | `IMPLEMENTS`: what the native view implements, by feature key (as `web-features.js` for the web) |

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
