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
llm-gw's logs. Give team members `read` on the tile.

## Runs

| Method & path | Body | Purpose |
|---|---|---|
| `GET /runs` | — | list runs (id, title, kind, status, timestamps; a quick ask also carries `last`, its latest answer, for the home view's cards). `?roots=1` lists top-level runs only — what the sidebar shows; subagents are reached through their parent |
| `POST /runs` | `{goal, title?, system?, class?, toolset?}` | create a run and start driving it; `class` (or the legacy `toolset`): see **Agent classes** |
| `POST /ask` | `{text, class?, toolset?, model?, hold?, draft?, files?}` | a quick ask: a run titled from `text`, `kind:"quick"`, driven immediately (`hold`, `draft`: see Attachments; `class`: see **Agent classes**) |
| `PUT /ask/upload?draft=&name=` | raw bytes, the file's own `Content-Type` | attach a file to a new ask before it exists (a native app's upload at home): into the run held for the draft key `{path, mime, bytes, binary, run}` — see Attachments |
| `GET /runs/{id}` | — | run detail: `{run, messages, steps, memory, config, class, files, draft, messageFiles, slots, queued}` (`class`: the conversation's class, see **Agent classes**) (`draft` = live streaming text; `files` is session-file METADATA only; `messageFiles` = `{msgId: [path…]}`, the files each user message carried; `slots` = `{active, limit}` model calls in flight; `queued` = messages not yet delivered) |
| `GET /runs/{id}/view` | — | the run as the chat draws it, plus a stream cursor — see **The live view**. `?limit=&before=` pages it, newest first — see **Paging the view** |
| `GET /stream?run=&since=` · `GET /runs/{id}/stream?since=` | — | SSE: run-list changes plus the whole tree of `run` — see **The live view**. `&deltas=1`: draft text and tool-call arguments as appended pieces |
| `DELETE /runs/{id}` | — | delete a run, its history, and every subagent run below it. A conversation's sandbox jobs still running are killed in the background (§The coding tools); its sandboxes stay |
| `POST /runs/{id}/message` | `{text, files?, clientId?}` | send a user message → `{inboxId, queued}`. An idle, finished or failed run starts a new turn; a working run gets it at its next step (`queued:true`). A retried post with the same `clientId` is stored once. `files` names session files (normally just uploaded) the message carries — each must exist, or **400** and nothing is written. `text` may be empty when `files` is not |
| `DELETE /runs/{id}/inbox/{iid}` | — | take back a queued message; **409** once the agent has it |
| `POST /runs/{id}/answer` | `{text}` | answer an `ask_user` (alias of message) |
| `POST /runs/{id}/approve` | `{approve, grant?, park?}` | approve/deny a parked tool turn (approval mode). A turn parked on a **grant** (`pendingState.grant`, see **Threads, schedules and grants**) is allowed only by the conversation's owner — **403** for anyone else, who may still deny — with `grant: "once"` (the default) or `"hour"`; **400** for anything else. Every park has an id, `pendingState.park`; `park` names the one the verdict answers — **409** when that ask is no longer pending (the agent moved on) — and without it the verdict answers the one pending now. A verdict applies to its own park only: one queued for an ask that is gone is dropped, never spent on the next ask, and an allow of a grant counts only from the owner (checked again when it is applied) |
| `DELETE /runs/{id}/grants/{cap}` | — | the owner takes a grant back before it expires → `{revoked}` (false when none was in force); **404** for an unknown `cap` |
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
| `run` | a run's summary changed (or `{deleted:true}`); top-level runs arrive whatever run is followed — the sidebar. It carries the run's coding sandbox: `sandbox` — the active binding's `{ref, name, cwd, egress, manager}` or `null` — and `attached` (how many), as does the view's `run` (§Coding sandboxes) |
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
| `files` | the session files and `render_html` (`file_view` with the `vision` feature) |
| `repl` | the JavaScript sandbox (`js_eval`, `js_run`, `js_reset`) |
| `web` | `web_search`, `web_fetch` |
| `internal` | `xbin_call` and the bound MCP servers' tools — `mcp` narrows them |
| `sandbox` | the coding-sandbox tools; `managers` and `sandboxEgress` narrow what may be bound |
| `subagents` | the `subagent_*` tools |
| `schedule` | `schedule`, `unschedule` |
| `threads` | `schedules_list`, `schedule_inspect`, `threads_list`, `thread_inspect` |
| `skills` | `skills_list`, `skill_view`, `skill_manage` |

The core tools — `memory_set`/`memory_get`, `note`, `recall`, `finish`,
`yield`, `ask_user`, `state_changed`, `attach_to_reply` — are in every class.
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
| `PUT /triggers/{id}` | any of the above, `enabled` | its owner; a manager only switches it on or off. `{enabled}` alone is never refused, and the class rules are checked again only when `class`, `toolset`, `dataClass` or `deliver` change — a trigger whose class was edited or deleted since still switches and edits |
| `DELETE /triggers/{id}` | — | its owner or a manager |
| `POST /triggers/{id}/test` | `{topic?, text?, data?}` | fire it with a sample event (its owner) |
| `GET /triggers/{id}/events` | — | the last 50 events: `{eventId, source, topic, accepted, reason, runId, at}`; `reason` for one refused: `disabled`, `halted`, `data-class`, `class-mixed`, `rate`, `target-gone` |
| `GET /triggers/unmatched` | — | pushes no trigger took (managers): `{items:[{from, name, count, at}]}` — the Automations page offers to make one |

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

Each step: deliver queued messages and background answers → assemble context
(system + a stable date + memory blocks + skill list + running summary + live
transcript, compacting the oldest turns when over budget) → LLM call (streamed
when the feature is on) → execute tool calls (a step's non-control tools run in
parallel, each under `toolTimeout`) → repeat, up to `maxTurnSteps` per turn.
Built-in tools: `memory_set`/`memory_get`, `note`, `recall` (FTS5 over full
history), `xbin_call` (reach other granted components; `internal`),
`web_search`/`web_fetch` (`web`), `schedule`/`unschedule`, `state_changed`
(watcher), `schedules_list`/`schedule_inspect`/`threads_list`/`thread_inspect`
(below), `skills_list`/`skill_view`/`skill_manage`, `finish`, `ask_user`,
`yield`, the `subagent_*` tools above, the session-file and sandbox tools below,
plus any bound MCP tool — each as its class allows (**Agent classes**).
MCP servers are bound via the `mcp` interface (multi:true, like the chat tile).
Extend these in `_backend/tools.go`.

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

File versions are monotonic but **not snapshotted**: clicking an older render
chip shows the file's current content, with the header noting the difference.
The tile's Files tab edits them too, sending back the version it loaded so a
write the agent made in between comes back as a 409 instead of being lost.

## Coding sandboxes (D115)

A conversation can work in a **coding sandbox**: a box with a shell, a
filesystem and the tools of a job, run by a **sandbox manager** — a tile
that implements the `sandbox-manager` contract (docs/sandbox-manager.md;
the builtin `coding-sandbox` template, or anyone's own). The agent holds no
sandboxes itself.

**Where they come from.** The manifest's `sandboxes` interface slot (`http`,
service `sandbox-manager`, multi): `bx bind <this component>
sandboxes+=apps/coding-sandbox`, or the binding panel. Several managers may
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
  tile itself) is theirs, and people's only when it is `team`;
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
  toolset and allow the sandbox's manager and egress (D116). A `cwd` must be
  an absolute path, and a directory when the sandbox is running.
- **Anyone who may steer the conversation works in what it has bound** —
  under the binder's right, which every tool call re-checks: the class
  still allows it, the manager is still bound, the sandbox still exists, and
  the binder may still use it and still takes part in the conversation.
  Otherwise the tool says why and the conversation needs a new binding.
- **A changed egress** (its owner changed the sandbox's network access since
  it was bound, and the class still allows it): the tool call that finds it
  records the live value in the conversation's bindings (and the calling
  subagent's copy), which the turn's next step already uses. In Approve
  mode, a call that would park under the new egress is refused once — `the
  sandbox's network access changed from none to internet; call the tool
  again to ask for approval` — and parks when called again, so a side effect
  never runs on an egress nobody approved it for.
- A **sandbox created for a conversation** (`POST /sandboxes
  {conversation}`) follows it: a team conversation's is `team`; the
  conversation's owner and participants are its members; it is labeled
  `xbin.agent/conversation: <id>` and bound there.

`PATCH /runs/{id}` also takes `{sandbox: {ref, cwd?} | null, detach?: <ref>}`
— bind (and attach) a sandbox, or change the active one's `cwd`; `null`
leaves the conversation with no active sandbox (the attached stay);
`detach` takes one off (applied first when both are sent). `POST /ask` also
takes `{sandbox: {ref, cwd?}}`: the new conversation starts bound (the
caller must be able to use it; its class must allow it).

| Route | Body / query | Result |
|---|---|---|
| `GET /sandboxes` | `?fresh=1` skips the cache | `{sandboxes: [{ref, provider, manager, …the contract's sandbox…, mine, canUse, canManage, canEdit, boundTo?}], managers: [{provider, title, ok, error?, refusal?, caps, egress, images, sizes, limits}]}` — every sandbox the caller may see across the bound managers, and those bound to a conversation the caller sees (`boundTo`: its ids). Merged, cached 15 s (the agent's own changes show at once); `manager` is the manager's title. Anyone who can use the tile |
| `POST /sandboxes` | `{name, provider?, image?, size?, egress?, visibility?, members?, conversation?, bind?, cwd?, clientId?, start?}` | **201** + the sandbox (as below), with `binding` when it was bound. Created at `provider` (optional while one manager is bound), owned by the caller. With `conversation` (the caller takes part in it): made for it (above) and bound there unless `bind: false` — refused up front when its class wouldn't allow it, and deleted again if the binding fails. `clientId` makes a retry return the same sandbox (per person) |
| `GET /sandboxes/{ref}` | | one sandbox, fresh from its manager, as `GET /sandboxes` lists it |
| `PATCH /sandboxes/{ref}` | `{name?, visibility?, members?, shares?, labels?, egress?, size?, autoStopMin?, version?}` | the sandbox — its owner's (the contract's `PATCH`; `restartNeeded` when a change waits for the next start) |
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
`# Sandbox` section — the active sandbox's name, manager, image, egress and
`cwd`, and the other attached ones — built from the binding alone, so it
changes on a rebind only (the prompt's cached prefix stays valid).

| Tool | Arguments | What it does |
|---|---|---|
| `bash` | `{command, cwd?, timeout_s? (120), background?}` | runs `command` with the sandbox user's login shell (an exec named `agent:<run>:<tool call>`, so starting it twice finds the one command), no TTY, no stdin, `TERM=dumb NO_COLOR=1 PAGER=cat GIT_TERMINAL_PROMPT=0`, and follows its combined output. The result is at most 12 KiB — a short head and a long tail with `… N bytes elided …` between, escapes and `\r` redraws cleaned — and a footer: `[exit 1 · 14s · job 3]`. At `timeout_s` (or just before the tool's own `toolTimeout`) the command **goes on as a job**: the footer says `still running after 2m00s · job 3` and how to follow it. `background: true` starts it as a job at once |
| `bash_output` | `{job, wait_s? (0, ≤ 600), offset?}` | a job's output since it was last read (or from byte `offset`), waiting up to `wait_s` for it to end; the footer says it still runs (and up to which byte it was read) or how it ended |
| `bash_kill` | `{job, signal?}` | signals the job's whole process group: `INT`, `TERM`, `KILL` or `HUP`; by default TERM, then KILL if it hasn't ended 3 s later |
| `read` | `{path, offset?, limit? (2000)}` | numbered lines (`cat -n` style), within ~14 KiB, saying what it left out; a file up to 256 KiB is read whole and sliced, a larger one ranged with `sed -n`; a binary file (a NUL or non-UTF-8 near its start) gets a hint instead |
| `write` | `{path, content}` | replaces the file atomically (the contract's `PUT …/files/content`), creating missing directories |
| `edit` | `{path, old_string, new_string, replace_all?}` | `file_edit`'s exact-string replacement (the same rules, one shared implementation) on a sandbox file of up to 4 MiB, written back with `ifMatch` = the etag it read; a `precondition` refusal (the file changed meanwhile) is retried once from a fresh read. The result shows the changed lines, numbered |
| `ls` | `{path?}` | a directory (≤ 500 entries): subdirectories first, with `/`; files with their size; symlinks with their target |
| `glob` | `{pattern, path?}` | files by name, relative to the working directory, sorted, at most 200: `**` spans directories, a pattern without `/` matches names at any depth, `{a,b}` alternates. The listing is the sandbox's own `rg --files` (which honours `.gitignore`) or `find` (skipping `.git` and `node_modules`) — at most 20 000 files — matched here |
| `grep` | `{pattern, path?, glob?, ignore_case?}` | `path:line: text` lines, at most 100 (then how many more), text clipped at 300 characters: `rg` where the sandbox has it (its regex syntax), else `grep -rE`; skips `.git` and binary files |
| `sandbox_upload` | `{file, path?}` | copies a session file (text or attachment) into the sandbox: to `path`, into it when it ends in `/` or is a directory (default: the working directory). Feature `files` |
| `sandbox_download` | `{path, name?}` | copies a sandbox file (≤ 16 MiB) into the session files as an upload would be stored — text within the text cap as text, anything else as an attachment — under `name` or its own (a taken name gets a suffix). A directory is refused: pack it with `bash` first. Feature `files` |
| `sandbox_copy` | `{from: {sandbox?, path}, to: {sandbox?, path}}` | between the conversation's attached sandboxes (a ref or a unique name; default the active one), or within one: a directory is tar-streamed (`GET …/tar` into `PUT …/tar`; both managers need `tar`) and its **contents** land in `to.path`; a file goes through the file routes (mode kept) to `to.path`, or into it when it is a directory. Offered when more than one sandbox is attached |
| `sandbox_info` | `{}` | every attached sandbox as its manager describes it now (active or attached, state, egress, image, manager, cwd, workdir, home, user, caps — or why it is unavailable) and the conversation's latest 15 jobs |

**`sandbox_create`** `{name, manager?, image?, size?, egress?, cwd?}` — the
agent makes a sandbox for its conversation. It is offered to a top-level
conversation whose class has the `sandbox` toolset and allows a bound
manager, **with or without** a sandbox bound, and not to a chat channel's
conversation. It takes the conversation **owner's grant** `sandboxes`
(§Threads, schedules and grants): the step parks — `pendingState: {kind:
"approval", grant: "sandboxes", grantAsk, toolCalls}`, where `grantAsk` says
what will be made (`create the coding sandbox “api-dev” at Coding sandboxes
— image base, size small, egress none`) — and only the owner may allow it,
once or for an hour (`POST /runs/{id}/approve {approve: true, grant:
"once"|"hour"}`; `DELETE /runs/{id}/grants/sandboxes` takes an hour's grant
back); anyone who may steer the conversation may deny it.

- **Defaults**: `manager` — the first bound manager the class allows (a
  provider, as `GET /sandboxes` names it); `egress` — the first the class
  allows that the manager offers, `none` first; `image` and `size` — the
  manager's defaults; `cwd` — the sandbox's workdir (a relative one is
  under it; a missing one is made).
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
  An image, size or egress the manager doesn't offer is refused once
  allowed; a binding that can't be made deletes the new sandbox again.
- **Idempotent**: each create is numbered per conversation
  (`sandbox_creates`) and sent with `clientId` `agent:<root>:name:<n>`. A
  call a restart cut off leaves its number pending; the next call with the
  same name reuses it, so the manager answers with the sandbox it already
  made. A sandbox of the same name this conversation made and still has
  attached is answered as already there.

**Jobs** are numbered per conversation (subagents share their root's
numbers) and kept in the `sandbox_jobs` table (`root_id, job, run_id,
tool_call_id, ref, exec_id, command, cwd, state, exit_code, read_off, fg,
created_ms, ended_ms`); a conversation runs at most **8** at once (asked of
the manager before a start is refused). **Interrupting or cancelling** the
turn stops the command bash is following — TERM to its process group, KILL
if it is still there 3 s later — while background jobs keep running. **A
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
  (its manager, its egress), is listed disabled with the reason. A pick
  binds it (`PATCH /runs/{root} {sandbox: {ref}}`, from the next turn); at
  home it goes with the new chat (`POST /ask {sandbox}`) while the ask's
  class has the toolset.
- **The ▣ badge** in the top bar: the active sandbox and its `cwd` — or,
  marked, why the binding no longer resolves (its class no longer allows it,
  its manager is unbound or unavailable, its manager no longer has it). Its
  popover sets the working directory (absolute; empty is the sandbox's
  workdir), makes another attached sandbox the active one, detaches the
  active one (`{detach}`) and opens Manage.
- **The Sandboxes dialog** (`#sbxdlg`): every sandbox you may see, yours
  first — state, manager, image, size, egress, owner, private/team, when it
  was last active, how many conversations have it — with **Use here** (or
  for a new chat), Start / Stop / Thaw (who may use or manage it; one bound
  to the open conversation that you may not use goes through
  `?conversation=`), Archive (who may manage it, where its manager
  archives), Share with the team / Make private (its owner) and Delete (who
  may manage it, confirmed). **New sandbox**: the manager, a name, its image
  and size, the network (the class's `sandboxEgress` only), who may use it,
  a working directory — in a conversation it is made for it and bound there
  (`POST /sandboxes {conversation}`), at home it becomes the new chat's.
  Opening a terminal onto one comes with the `sandbox-terminal` tile.
- **Keeping current.** After a change the conversation's binding is read
  again (`GET /runs/{id}/view?limit=1` → `config`); a `run` event that
  carries `sandbox` (and `attached`, a count) updates it at once, and a
  changed count reads the binding again.
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
  why). The ▣ badge is in the conversation's subtitle, a notice in the
  transcript says why a binding no longer resolves, and ⋯ → Sandbox pushes
  the popover's screen (working directory, the attached ones, Detach,
  Manage sandboxes…). The Sandboxes screen puts each row's actions behind
  its swipe and ⋯ (Archive and Delete confirmed), and New sandbox pushes
  the create form. A ▣ tool card is a `terminal` icon; what the call came to
  is a chip (`exit 1 · 14s · job 3` in red), its command the card's first
  line.

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
| `session.js` | the open conversation: its views, the model calls in flight, `shown()` (what the chat draws), `blocks(id)` (a held run folded through its cache) |
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
| `sandboxes.js`, `sandbox-store.js` | coding sandboxes (D115): the composer's picker, the ▣ badge and why a binding no longer resolves, the Sandboxes dialog's rows and their actions, the create form; `app.sbx` — the list, the next new chat's pick, binding, the working directory, detaching, creating, the lifecycle, the run events that carry a binding |

`createApp({deltas, page})` are the native view's options: drafts arrive as
deltas (`/stream?deltas=1`, "Deltas" above) and the open conversation is read
in pages (`?limit=`, `Session.loadOlder()`, "Paging the view"); the web
passes neither and reads whole views. An app that uploads picked files itself
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
