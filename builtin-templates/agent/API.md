# agent — API

A durable agentic loop. State lives in this component's sqlite (`db`); the LLM
is reached through `apps/llm-gw`. Runs are driven by an in-process engine: one
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
| `GET /conversations?limit=&cursor=&archived=&scope=` | — | your conversations, newest activity first → `{pinned, items, next}`. `pinned` (your pins) comes on the first page only; `next` is the cursor for the page after. `scope=mine` (default: yours, ones you joined, and unowned ones) or `team` (others' team-shared ones). `archived=1` lists what you archived |
| `GET /conversations?q=` | — | search titles and everything said, in every conversation you may see (archived and automation runs included), up to 50; content hits carry `match {msgId, snippet}` |
| `PATCH /runs/{id}` | `{title?, pinned?, archived?, visibility?, teamRole?}` | pin and archive are yours (any viewer); title and visibility are the owner's. Making an unowned run private claims it |
| `POST /runs/{id}/read` | — | mark it read up to now |
| `GET /needs` | — | what waits for you: conversations where the agent (or a subagent) asks a question or wants an approval, and automations you own whose last run failed and you haven't looked at → `{items:[{run, reason: question\|approval\|failed, subRun}]}` |

Items are run summaries plus `access`, `mine`, `members`, `pinnedAt`,
`archivedAt`, `readMs` and `unread` (activity after you last looked). Your
own message marks the conversation read for you. The stream sends `ustate`
(`{id, pinnedAt, archivedAt, readMs}`) to your own streams only, and
`revoked` (`{id}`) when you can no longer see a conversation.

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
llm-gw's logs. Give team members `read` on the tile.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, kind, status, timestamps; a quick ask also carries `last`, its latest answer, for the home view's cards). `?roots=1` lists top-level runs only — what the sidebar shows; subagents are reached through their parent |
| `POST /runs` | `{goal, title?, system?, toolset?}` | create a run and start driving it |
| `POST /ask` | `{text, toolset?, hold?}` | a quick ask: a run titled from `text`, `kind:"quick"`, driven immediately (`hold`: see Attachments) |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, files, draft, messageFiles, slots, queued}` (`draft` = live streaming text; `files` is session-file METADATA only; `messageFiles` = `{msgId: [path…]}`, the files each user message carried; `slots` = `{active, limit}` model calls in flight; `queued` = messages not yet delivered) |
| `GET /runs/{id}/view` | — | the run as the chat draws it, plus a stream cursor — see **The live view** |
| `GET /stream?run=&since=` · `GET /runs/{id}/stream?since=` | — | SSE: run-list changes plus the whole tree of `run` — see **The live view** |
| `DELETE /runs/{id}` | — | delete a run, its history, and every subagent run below it |
| `POST /runs/{id}/message` | `{text, files?, clientId?}` | send a user message → `{inboxId, queued}`. An idle, finished or failed run starts a new turn; a working run gets it at its next step (`queued:true`). A retried post with the same `clientId` is stored once. `files` names session files (normally just uploaded) the message carries — each must exist, or **400** and nothing is written. `text` may be empty when `files` is not |
| `DELETE /runs/{id}/inbox/{iid}` | — | take back a queued message; **409** once the agent has it |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve}` | approve/deny a parked tool turn (approval mode) |
| `POST /runs/{id}/interrupt` | — | stop the turn in flight (never an error); the run goes idle, its subagents are cancelled, and messages still queued come back as `{returned:[{text, files}]}` |
| `POST /runs/{id}/resume` | — | drive the run again |
| `POST /runs/{id}/compact` | — | force a compaction now (answers when it is done) |
| `POST /runs/{id}/learn` | — | distill the run into a saved skill (the /learn flow) |
| `PUT /runs/{id}/memory` | `{key, value}` | set a memory block |
| `DELETE /runs/{id}/memory?key=` | — | delete a memory block |
| `POST /runs/{id}/cancel` | `{scope?, reason?}` | durable cancel; `scope` defaults to `subtree` |
| `GET /runs/{id}/tree` | — | the whole workflow this run belongs to: nodes (each with its `link` and `phase`), statuses, blockers, cost. Metadata only |
| `GET /halt` · `PUT /halt` | `{on}` | the global brake: cancels live runs and blocks new spawns |
| `GET /runs/{id}/files` | — | the run's session files: `[{path, bytes, version, updated, mime?, binary?}]`, no content |
| `GET /runs/{id}/file?path=` | — | one file with its content |
| `PUT /runs/{id}/file` | `{path, content, version?}` | write a file; a non-zero `version` that no longer matches answers **409** |
| `DELETE /runs/{id}/file?path=` | — | delete a file (and its blob, for an attachment) |
| `PUT /runs/{id}/upload?name=` | raw bytes, the file's own `Content-Type` | attach a file: `{path, mime, bytes, binary}`. Never overwrites — a taken name gets `-2`, `-3`…. **413** over 16 MiB, **502** when the blob store fails |
| `GET /runs/{id}/raw?path=` | — | a file's bytes with its type and `nosniff` (the tile's preview and download) |

Content and metadata are separate routes on purpose: a run's detail and view
must never carry file bodies.

Every run records who it belongs to and where it came from (D83): `owner`
(the user id of whoever started it; `""` for runs from before, or from the
owner token and scripts), `visibility` (`private` | `team`) with `teamRole`
(`viewer` | `participant` — what team visibility grants), `origin` (`chat`,
`api`, `schedule`, `watcher`, `channel`; later `trigger`), `originId` (the
automation's id), `sessionKey`, `titleSrc` (`clip` | `auto` | `user` |
`origin`) and `activityMs` (the last thing a person or the agent said, or a
wait for someone — what the conversation list sorts by). `POST /ask` also
takes `title` and `system`. Runs from before keep `owner ""` and `team`
visibility, so nothing disappears.

Runs carry a `kind`: `""` for a task, `"quick"` for a quick ask — kept for
compatibility; both are conversations. The tile opens on a home view: the
composer starts a new conversation (in the lane chosen with its 🔒/🌐 toggle,
remembered per user through `/api/xbin/prefs` — tile frames have no
`localStorage`), and **Needs you** lists what waits for you (`GET /needs`).
The sidebar is your conversation list (`GET /conversations`): Pinned, then
Today / Yesterday / Previous 7 days / Previous 30 days / Older by last
activity, unread in bold, a search box, and a row menu (right-click or ⋯) to
rename, pin, share with the team, archive or delete; the footer switches to
conversations shared with the team and to your archive. **New chat** goes
home; **⋯** opens "New chat with options" (a title, instructions that
replace the system prompt, the tool mode). A link to a conversation is the
tile's URL with `#c=<id>`. Automation runs (schedules, watchers) are not in
this list.

### The live view

The tile never polls. It reads `GET /runs/{id}/view` — messages (with their
`reasoning`/`reasoningMs`), steps, `links` (subagents, each with its child's
summary and phase), `queued`, the calls in flight as `drafts`, `chain` (the
path from the root) and a `cursor` — then opens `GET /stream?run={id}&since=<cursor>`.
The stream is Server-Sent Events, `data:` a JSON `{type, run, root, seq, data}`:

| type | data |
|---|---|
| `run` | a run's summary changed (or `{deleted:true}`); top-level runs arrive whatever run is followed — the sidebar |
| `message` | a message added or rewritten (a settled tool result); upsert by `id` |
| `step` | a journal step |
| `inbox` | `{queued}` — the run's undelivered messages |
| `link` | a subagent link changed (spawned, phase, settled, delivered) |
| `thinking` · `text` · `tool` | a model call in flight: the ACCUMULATED reasoning, answer text, or `{index, id, name, args}` of a tool call being written |
| `draft.end` | that call finished (its message follows as `message`) |
| `reset` | the cursor is from another process or too old: re-read `/view` |
| `bye` | this process is handing over: reconnect at once, the successor answers |

A subagent's events arrive on its root's stream, which is how its work renders
inside the parent's chat. Nothing can fall between the view and the stream (the
cursor is taken before the database is read, and events are idempotent
upserts). Draft events are coalesced per subscriber — a slow reader gets the
latest text, not every token.

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

## Capability lanes (the toolset firewall)

Every run is in exactly one lane, chosen at creation and immutable
(`config.toolset`):

- **`private`** (default) — internal reach: `xbin_call` and every bound
  `mcp:*` tool. **No web tools.**
- **`web`** — `web_search` (DuckDuckGo) and `web_fetch` (a URL as readable
  text, capped). **No `xbin_call`, no `mcp:*` tools.**

A run must never hold private data AND an egress channel: content injected into
its context could otherwise steer it into sending that data out in a URL or a
query. The lane is enforced twice — in the tool list offered to the model, and
again when a tool runs (`runTool`) — and it is inherited: subagents get their
parent's lane, and **a schedule the agent creates gets the creating run's
lane**, so a private run cannot smuggle data into a future web run's goal.
Human-created runs and schedules may pick either (`toolset` on `POST /runs`,
`POST /ask`, `POST /schedules`); the tile has a select in the new-run dialog
and the schedule form, and a lane badge on each run.

The web tools go straight out, not through the gateway, so they need the `net`
interface bound (`bx bind <this component> net=internet`); unbound, they return
"web access unavailable" so the model can adapt.

## Config, models, features

`GET /config`, `PUT /config` — the default `Config` copied into each new run:

```jsonc
{
  "model": "",                 // legacy/general fallback (empty ⇒ llm-gw preferred)
  "models": {                  // per-tier models; any empty tier ⇒ llm-gw preferred for
    "general": "", "code": "", //   the mapped use-type (general←agent, code←coding,
    "memory": "", "vlm": ""    //   memory←summarizing, vlm←vlm)
  },
  "system": "…",
  "tokenBudget": 12000,        // compaction trigger (provider prompt tokens when known)
  "maxIters": 12,              // legacy: sizes the default maxTurnSteps (8×)
  "maxTurnSteps": 96,          // model calls in one turn before it stops as incomplete
  "toolTimeout": 120,          // seconds per tool call
  "wire": "auto",              // "auto" | "chat" | "responses" (below)
  "reasoningEffort": "",       // passed to reasoning models ("" = provider default)
  "subagents": true,
  "approve": false,            // gate side-effecting tools on human approval
  "features": { "recall": true, "skills": true, "streaming": true,
                "vision": true, "parallelTools": true, "watcher": true,
                "files": true, "repl": true, "workflow": true, "titles": true },
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
- `GET /models` — proxies llm-gw's aggregated model list (for the tier
  dropdowns).

The main loop uses the `general` tier (or `vlm` when a message carries image
content and the general model isn't vision-capable); compaction and the
summarizer use `memory`.

**Wires and thinking.** Two model APIs, both through llm-gw: Chat Completions
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

The non-UI agents — schedules, watchers and chat channels now, triggers as
they land — are **automations**: each belongs to a person (who created it) and
is private or team-visible like a conversation, and the runs it fires carry
its `origin`/`originId` and its owner and visibility. They are not in the
conversation list; the tile's Automations page lists them with their runs.

| Method & path | Body | Purpose |
|---|---|---|
| `GET /automations` | — | `{items:[{kind, id, name, owner, visibility, access, enabled, summary, mode, targetRun, currentRun, lastRunId, lastRunAt, lastStatus, runs, unread, config}]}`. `access` is `owner`, `viewer`, or `oversee` — a manager's view of someone else's private one (it exists, runs, whose; not what it does) |
| `GET /automations?summary=1` | — | `{count, unread, failing}` for the sidebar badge |
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
| `POST /schedules` | `{name?, cron, goal, watcher?, toolset?, visibility?, mode?, targetRun?}` | create + register a cron-agent; its owner is the caller |
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

A chat platform reaches the agent through an **adapter tile** (the `slack`
builtin, or your own) bound to this agent's `inbox` provide (service
`agent-inbox`): the binding grants the adapter the `channel` role, which
reaches only `/adapter/*`. The adapter reports messages and pulls replies;
the agent decides everything else — which conversation a message joins (a
session per DM, per thread), who may talk (pairing codes, allowlists,
mentions), the lane (web by default: a reply is an egress) and the tools
(`deny`). The adapter contract, the session keys, the chat commands (`/new`,
`/status`, `/stop`, …) and the policy fields are in `/docs/agent-inbox.md`.

A channel appears (kind `channel` in `GET /automations`, `access: "claim"`
for managers) when its adapter first says hello, and does nothing until a
manager claims it; its conversations belong to the claimer and follow its
visibility. The owner's routes (`{id}` is the channel's):

| Method & path | Body | Purpose |
|---|---|---|
| `POST /channels/{id}/claim` | `{name?, visibility?, policy?}` | a manager takes an announced channel: it becomes theirs and active |
| `PUT /channels/{id}` | `{name?, visibility?, policy?, enabled?}` | its owner changes it; a manager may only switch it on or off. A visibility change carries to its conversations |
| `DELETE /channels/{id}` | — | forget it (owner or manager): its conversations stay; a still-bound adapter announces it again, unclaimed |
| `GET /channels/{id}/peers` | — | the people it knows: `pending` (a pairing code out), `allowed`, `blocked`; `trusted` |
| `POST /channels/{id}/pair` | `{code}` | approve the stranger holding that pairing code |
| `PUT /channels/{id}/peers/{peer}` | `{state?: allowed\|blocked, trusted?, name?}` | set someone's standing (trusted: the private lane when `privateLane` is on; `/approve`) |
| `DELETE /channels/{id}/peers/{peer}` | — | forget them (a DM starts pairing again) |
| `GET /channels/{id}/sessions` | — | `{sessions:[{key, runId, resets, reset, created, lastIn, address}]}` |
| `POST /channels/{id}/sessions/reset` | `{key}` | start a session afresh, like `/new` |
| `GET /channels/{id}/outbox?state=failed\|pending` | — | replies not delivered |
| `POST /channels/{id}/outbox/{oid}/retry` | — | queue a failed reply again |

Replies are written in the transaction that ends (or parks) the turn — an
answer, an `ask_user` question, "waiting for approval" — and the adapter
acks each after posting it, so a reply is neither lost nor sent twice by
the agent. A message typed into a channel conversation from this page is
answered here, not posted. The list stream sends `{type: "automation",
data: {kind, id}}` when a channel changes (announced, a pairing request, a
failed delivery).

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

Each step: deliver queued messages and background answers → assemble context
(system + a stable date + memory blocks + skill list + running summary + live
transcript, compacting the oldest turns when over budget) → LLM call (streamed
when the feature is on) → execute tool calls (a step's non-control tools run in
parallel, each under `toolTimeout`) → repeat, up to `maxTurnSteps` per turn.
Built-in tools: `memory_set`/`memory_get`, `note`, `recall` (FTS5 over full
history), `xbin_call` (reach other granted components; private lane),
`web_search`/`web_fetch` (web lane), `schedule`/`unschedule`, `state_changed`
(watcher), `skills_list`/`skill_view`/`skill_manage`, `finish`, `ask_user`,
`yield`, the `subagent_*` tools above, the session-file and sandbox tools below,
plus any bound MCP tool.
MCP servers are bound via the `mcp` interface (multi:true, like the chat tile).
Extend these in `_backend/tools.go`.

MCP tool lists are cached per server and persisted: a list younger than 5
minutes is used as is, an older one is used at once and refreshed in the
background, and only a server never listed before is waited for —
concurrently, 5 s at most each. A run is marked `running` before any of this,
and the web lane skips discovery entirely.

## Session files + render

Feature key `files` (on by default). A per-run file store held in sqlite, not on
disk: `file_write` · `file_read` (whole file, or a line range with
`offset`/`limit`) · `file_edit` (exact-string replace) · `file_list` ·
`render_html` · `file_view` (an image — see Attachments). They touch only this
run's private rows — no egress, no other component — so they are offered in
**both** capability lanes and are not `sideEffect()` tools: the approval gate
never fires for them. Caps: 64 KiB per text file, 64 files and 512 KiB of text
per run; attachments have their own.

`render_html` journals a `render` step (`{path, version, bytes}`) and the tile
shows that file in a `sandbox=""` iframe with a prepended meta CSP
(`default-src 'none'; style-src 'unsafe-inline'; img-src data:`). **Scripts
never run and nothing external loads** — so charts must be inline SVG. Verify
with `node test/frame-policy.mjs`.

The chat is `chat-view.js` (state, fed by `stream.js`), `chat-fold.js` (pure:
messages, links and drafts → blocks), `chat-cards.js` (lit templates),
`tool-heads.js` (a tool call's headline and icon) and `chat-md.js` (markdown);
`agent.js` is the page around it. The tile's other browser tests
(`test/layout.mjs`, `chat`, `home`, `sidebar`, `attach`) drive the real page
against `test/backend.mjs`, a stubbed transport with a scriptable event
stream; `test/kit.mjs` serves the frontend kit and vendored lit from the xbin
checkout the template lives in (in an instance: `BX_KIT=/path/to/bx-kit.js`,
`BX_VENDOR=/path/to/vendor`). Each needs Playwright with a Chromium build and
skips without it. The backend: `go vet ./_backend && go test ./_backend` with
a `go.mod` copied from `go.mod.tile`.

File versions are monotonic but **not snapshotted**: clicking an older render
chip shows the file's current content, with the header noting the difference.
The tile's Files tab edits them too, sending back the version it loaded so a
write the agent made in between comes back as a 409 instead of being lost.

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
