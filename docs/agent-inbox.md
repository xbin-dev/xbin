# agent-inbox — connecting a chat platform to an agent

An **adapter tile** turns a chat platform (Slack, Telegram, Discord, …) into
messages for an agent built from the agent template, and posts the agent's
replies back. The split is deliberate (D86):

- **The adapter knows the platform:** its tokens and sockets, its event
  shapes, its formatting, its rate limits.
- **The agent owns everything else:** which conversation a message joins,
  who may talk at all, what the conversation may do, and what gets said.

An adapter never names a session or picks a lane; it reports facts and
the agent decides. This page is the contract, **protocol 1**.

**To build one, start from the `agent-messaging-bridge` template.** It
implements this whole contract: storing events before they are
acknowledged, ordered delivery, attachments both ways, retries,
never-twice replies, the typing hint, the linking page. It leaves one Go
interface for the platform. A coding agent in the copy's terminal writes
that, guided by the template's `AGENTS.md`; until then the copy's console
plays the platform.

## Wiring

The adapter requests an http interface with service `agent-inbox`:

```jsonc
"interfaces": { "agent": { "kind": "http", "service": "agent-inbox" } },
"alwaysOn": true   // it holds an outbound connection (docs/elements.md)
```

The owner binds it to an agent, for example
`bx bind apps/messaging-bridge agent=apps/agent`. The agent's `inbox` provide grants
the binding the **`channel` role**, which reaches only the `/adapter/*`
routes below and nothing else of the agent. The adapter calls the agent
at `XBIN_IFACE_AGENT_URL` (`http://xbin/api/<agent>`) with its instance
client (`xbin.Client()` in Go). The agent identifies the adapter by the
verified `X-XBin-From`.

The first `hello` creates an **unclaimed** channel. It is inert (every
message is refused as `unclaimed`) until a manager claims it on the
agent's Automations page, which sets its owner, its visibility and its
rules. Until then the binding authorizes the calls, but nobody owns what
they would do.

An agent may be **partitioned** — one instance per person, plus a global
one; new instances of the template are, by default. The adapter then
always talks to the agent's **global instance**, over this same contract
([A partitioned agent](#a-partitioned-agent)).

## Routes

All bodies are JSON. Errors are `{error}` with a 4xx/5xx status.

### `POST /adapter/hello`

```json
{"protocol": 1, "platform": "slack",
 "account": {"id": "T0123", "name": "Acme"},
 "bot": {"id": "B042", "name": "agentbot"},
 "features": ["threads", "assistant"]}
```

The response is `{"channelId": 7, "state": "unclaimed|active|disabled", "protocol": 1}`.

- Call it at every start and after every reconnect. It is idempotent per
  `(adapter, account.id)`.
- An unsupported protocol answers 400 with the version the agent speaks.
- `platform` and `account.id` are required. `features` are informational.

### `POST /adapter/message`

One inbound message:

```json
{"channelId": 7, "eventId": "Ev06…",
 "conversation": {"id": "C024", "type": "channel", "name": "general"},
 "thread": "1700000000.000100", "messageId": "1700000123.000200",
 "assistantThread": false,
 "sender": {"id": "U777", "name": "Ann", "isBot": false},
 "mentioned": true, "text": "deploy the site", "command": ""}
```

| Field | Meaning |
|---|---|
| `eventId` | The platform's event id. Retries with the same id are deduplicated for 7 days. |
| `conversation.type` | `dm`, `group` (a multi-person DM) or `channel`. |
| `thread` | The thread's **root** message id when the message is in a thread; empty otherwise. |
| `messageId` | This message's own id. |
| `assistantThread` | The platform's assistant view (Slack's AI pane). It always gets a session per thread. |
| `mentioned` | The bot was addressed. Strip the mention from `text`. |
| `command` | A slash command the adapter parsed, as `"new some text"`. Text starting with `/` is also read as a command. |
| `files` | Attachment ids from `POST /adapter/files` (below). |

Don't forward the bot's own messages, and mark other bots `isBot`: the
agent refuses them. Keep the body under 256 KiB.

The answer is a verdict, always 200 for a known channel:

```json
{"accepted": true, "sessionKey": "chan:7:group:C024:thread:1700000000.000100",
 "runId": 42, "inboxId": 311, "queued": false}
{"accepted": false, "reason": "pairing"}
{"accepted": true, "dup": true}
```

`reason` is one of:

| Reason | Why |
|---|---|
| `unclaimed` | Nobody has claimed the channel yet. |
| `disabled` | The owner switched the channel off. |
| `bot` | The sender is a bot. |
| `not-allowed` | The DM or group policy excludes the sender, or the sender is blocked. |
| `pairing` | An unknown DM sender. A code was queued for them: they link their account with it, or the owner approves it. |
| `link-required` | The channel only hears people who linked their xbin account (`dm.policy: linked`, `groups.linkedOnly`). In a DM, a link code was queued; in a group, nothing is posted. |
| `mention-required` | A group message that didn't address the bot, outside a thread it follows. |
| `rate` | More than the per-peer limit in a minute. |

Refusals are final; don't retry them. A 404 means the channel isn't
this adapter's; say hello again.

### `GET /adapter/outbox?since=<id>`

A server-sent event stream of what to post, for this adapter's channels
only:

```
event: hello   data: {"cursor": 0, "channels": [7]}
event: out     data: {"id": 55, "channelId": 7, "sessionKey": "chan:7:dm:U777",
                      "runId": 42, "kind": "answer",
                      "address": {"conversation": "D0DM1ABC", "type": "dm", "thread": "", "user": "U777"},
                      "body": {"text": "Done — **deployed**.", "format": "markdown"},
                      "created": 1759000000}
event: status  data: {"channelId": 7, "sessionKey": "…", "address": {…}, "state": "working"}
event: channel data: {"channelId": 7, "accountId": "T0123", "state": "active"}
event: bye     data: {}
```

**Events and kinds**

- `out` rows come oldest first: every pending row with an id above
  `since`, then new ones as they are written.
- `kind` is one of:
  - `answer`: a turn's reply;
  - `question`: the agent asks, and the peer's next message answers;
  - `approval`: a tool call waits for an operator;
  - `error`: generic by design;
  - `notice`: pairing codes, command replies, "paused".
- Post the row to `address.conversation`, in `address.thread` when set.
  Convert `body.text` from Markdown to the platform's format, and split
  it at the platform's limits.
- `status` is a hint for a typing indicator. It isn't stored and may be
  dropped.
- `channel` says the owner changed a channel of this adapter: `active`
  (claimed, or switched back on), `disabled` or `removed`. It is for the
  adapter's page, which can then stop saying "claim it". It is a hint too:
  the verdicts on `/adapter/message` tell the same (`unclaimed`, `disabled`).
  After `removed`, say hello again for that account to have it offered anew.

**Reconnecting**

- `bye` means the agent is handing over to a new process: reconnect.
- There are no keep-alive pings. Reconnect on EOF too.
- On reconnect, pass `since` = the last id this process received.
- After an adapter restart, pass `0` to get every row not yet acked.

### `POST /adapter/ack`

```json
{"acks": [{"id": 55, "ok": true, "ref": "1700000200.000300"},
          {"id": 56, "ok": false, "error": "channel_not_found"}]}
```

The response is `{"settled": n}`. Rows of other adapters are ignored.

- `ok:false` is **final**: retry transient failures (rate limits, 5xx)
  yourself before reporting.
- The owner sees failed rows and can put them back in the queue.

Delivery is **at-least-once**. Record each posted row's `ref` durably
before you ack it. After a restart, a row you already posted (you have
its `ref`) is acked without posting again.

### Files: `POST /adapter/files` and `GET /adapter/files/{row}/{i}`

**In:** upload each attachment first:

- The request is `POST /adapter/files?channelId=&name=&mime=`, with the raw
  bytes as the body (16 MiB at most). It answers `{fileId, name, mime, bytes}`.
- Then name the ids in the message (`files: [...]`).
- When the message finds its conversation, each file becomes one of that
  run's session files, attached to the message. The model sees images and
  reads documents.
- A file whose message never comes is dropped after a day.

**Out:** the model attaches session files to its reply (the
`attach_to_reply` tool, offered to channel conversations). An outbox row
then lists them in `body.files: [{name, mime, bytes, path}]`. Download
each with `GET /adapter/files/{row id}/{index}` (only your own channels'
rows) and post it with the text.

### Linking: `POST /adapter/link`, `GET /adapter/links`, `DELETE /adapter/links/{channelId}/{peer}`

A chat account can be linked to an xbin account, so the agent knows who is
talking:

1. The person messages the bot. A new person gets a one-hour code (so does
   `/link`).
2. Signed in to xbin, they paste the code on **the adapter's page**.
3. The page calls `POST /adapter/link {code}` itself, from its frame, so
   xbind attributes the call to that person.

The agent takes the person from the attribution (`X-XBin-User`), never from
the request:

- A call without a person is refused.
- So is one while viewing as someone else, or by a person who can't open
  the agent.
- The code proves the chat account; the session proves the xbin account.

`GET /adapter/links` lists the caller's links. `DELETE` undoes one. The
channel's owner can unlink anyone on the Automations page.

Once linked:
- Their DM is their own conversation: owned by them, private, and listed
  with their chats in the agent's sidebar.
- Their group messages carry their id.
- The owner can admit only linked people (`dm.policy: linked`,
  `groups.linkedOnly`) and trust them (`trustLinked`).

### `POST /adapter/event` and `GET /adapter/triggers`: pushing events

A bound tile can also push **events** that run the agent's **triggers**
(D87). The webhooks tile does this for webhooks from outside.

```json
{"trigger": "deploys", "eventId": "gh-7c1f…", "topic": "deploy/site",
 "text": "main was deployed", "data": {…}, "dataClass": "public"}
```

- `trigger` names one of the caller's push triggers. Without it, every one
  whose topic prefix matches `topic` runs.
- `eventId` dedupes: the same event never runs a trigger twice.
- `dataClass` is `public` only for data from outside the workspace. It
  decides which triggers may take it: those that reach outside take public
  data only.

The response is `{"results": [{"trigger", "accepted", "reason"?, "dup"?, "runId"?}]}`.

| Status | Meaning |
|---|---|
| 404 | No trigger takes it. The agent's owner sees "`<tile>` sent `<name>`" and can create one. |
| 503 | The agent is halted. Retry later. |

`reason` is one of `disabled`, `halted`, `data-class`, `class-mixed`
(public data into a class that can move internal data out — the agent
refuses that however the class got there), `rate` or `target-gone`.

`GET /adapter/triggers` lists the caller's push triggers:
`{"triggers": [{name, match, dataClass, enabled}]}`.

## What the agent does with a message

### Sessions

A **session key** names the conversation (a run) a message joins. The
agent builds it from the facts above. Ids are escaped, so an id holding
`:` can't forge another key.

| Situation | Key (default policy) |
|---|---|
| DM | `chan:<C>:dm:<user>` (`dm.scope: "main"`: one `chan:<C>:main` for everyone) |
| a thread in a DM | the DM's key (`dm.threads: "thread"`: `…:thread:<root>`) |
| assistant thread | `chan:<C>:dm:<user>:thread:<root>`, always |
| group or channel | `chan:<C>:group:<conv>:thread:<root>` |

In a group, a top-level mention's own id is the thread root, so each
mention starts a conversation and the reply lands under it. Replies in
that thread join it, without a mention while `groups.followThreads`
holds. `groups.threads: "parent"` makes one conversation per group, and
`groups.scope: "per-user"` one per person in it.

A session never resets by itself by default; compaction bounds its
context. The optional `reset` policy is `idle:<seconds>` or
`daily:<hour>`, checked when the next message arrives. Commands, from
anyone the channel admits:

| Command | Effect |
|---|---|
| `/new [text]` | Start a new conversation (text opens it). |
| `/reset` | The same, without text. |
| `/status` | The session, its run, lane and queue. |
| `/stop` | Interrupt the current turn. |
| `/help` | List the commands. |
| `/link` | A code to link this chat account to an xbin account (DMs only). |
| `/approve`, `/deny` | Settle a pending tool approval; trusted peers only. |

Old conversations stay listed and searchable on the Automations page.

### Who may talk

| Policy | Values (default first) |
|---|---|
| `dm.policy` | `pairing` (link, or the owner approves), `linked` (only linked people), `allowlist`, `open`, `disabled` |
| `groups.linkedOnly` | `false`; `true` hears only linked people |
| `trustLinked` | `false`; `true` counts linked people as trusted |
| `groups.policy` | `allowlist` (by conversation id, `groups.allow`), `open`, `disabled` |
| `groups.requireMention` | `true` |
| `groups.followThreads` | `true` |
| `ratePerMin` | 20 per peer |

**Codes:** an unknown DM sender gets an 8-character code, valid for an
hour and repeated at most every 10 minutes. At most three strangers wait
at once; beyond that, new ones get no code. With the code they link
their own xbin account (above). Under `pairing`, the channel's owner can
instead approve it on the Automations page.

### Lanes and tools

A reply to a chat is an egress, so channel conversations run in the
agent's **web lane**, which has no internal reach.

- With `privateLane: true` the private lane opens to trusted DM peers
  (with `trustLinked`, also to linked people) and to `trustedGroups`. Combining it with open DMs is refused.
- Each lane runs in an agent class (D116; the agent's API.md, **Agent
  classes**): `webClass` for everyone (default `web`; it must reach
  outside and have no internal reach) and `privateClass` for the trusted
  (default `internal`). A class only the agent's managers may use can be
  named only by one of them, and a class a channel names can't be deleted.
  Saving the rules re-checks a class only when it changes. Changing a
  conversation's class starts a new one on the next message, as revoking
  trust does.
- Revoking trust moves the conversation to a new web-lane run on the
  next message.
- `deny` lists tools a channel session never gets. The default is
  `schedule`, `unschedule` and `skill_manage`: a stranger's message
  leaves nothing behind that outlives the conversation.
- The thread tools (`threads_list`, `thread_inspect`, …) see only the
  channel conversation's own automations there: reading the owner's other
  conversations needs the owner's grant, and no one in a chat can give it
  (D111).
- `system` adds to the prompt. The agent already tells the model where
  it is talking, that group members' text is untrusted, and that
  `NO_REPLY` means silence.

**The halt:** while the agent is halted, messages are kept and the
sender is told the agent is paused. A channel message never lifts the
halt.

## A partitioned agent

A partitioned agent ([/docs/partitions.md](/docs/partitions.md)) runs one
instance per person who uses it — their **partition**, holding their own
conversations — and a **global instance** for what is the tile's rather
than one person's. An adapter isn't partitioned, so every call it makes
reaches the global instance (the table in partitions.md, "Who reaches which
partition"). **Nothing changes on the wire:** the same routes, one outbox
stream per adapter, the same verdicts. What changes is where the work runs:

| Arrives at the global instance | Goes to |
|---|---|
| `hello`, claiming a channel, its rules | the global instance: channels and their routing tables are the tile's, kept in its data. Managers claim and configure them there — from their own partition too, which forwards the channels' routes to it as them |
| a message in a group or channel | a shared conversation at the global instance, as without partitions |
| a DM from a chat account **linked** to a person | handed to **that person's partition**: the global instance records the hand-off (routing facts; the message itself only until it is mailed) and sends the message to the person's partition by partition mail ([/docs/partitions.md](/docs/partitions.md) §Partition mail); the DM runs there as their own private conversation. Its files go with it — inline up to 640 KiB a message; a larger one waits in the global instance's storage until the person's partition has fetched it (deleted there then). The chat commands `/help` and `/link` are answered by the global instance, the others by the person's partition |
| that person's reply | mailed by their partition back to the global instance (a file too large for the mail staged there first), which takes where to post it from **its own** hand-off record — never from the reply — and appends it to the outbox as usual |
| a DM from an account nobody linked | the global instance, as without partitions (pairing, link codes) |
| `POST /adapter/link` and unlinking, from the adapter's page | the global instance, as the signed-in person (`X-XBin-User`): links are the tile's routing, kept in its data |
| `POST /adapter/event` for a team trigger | the global instance runs it |
| … for a person's private trigger | handed to their partition by mail, which runs it with that trigger's settings |

For the adapter this means:

- **Bind it to the agent as today** (`bx bind <adapter> agent=apps/agent`):
  a global bind, which whoever may bind today makes — an admin, an org
  admin within their org, a personal tile's owner to what they own. A
  personal bind doesn't apply: the adapter isn't partitioned, so it has
  one wiring for everyone. A partitioned agent without a global instance
  can't be bound (409): nothing of it would answer.
- **A linked person's DM is answered later, not refused.** Its verdict is
  `accepted` with the `sessionKey`, and a `status` event on the outbox
  stream says `working`; the reply comes through the outbox whenever the
  person's partition has it. A person's partition starts when they use the
  agent: a DM that arrives before their partition ever ran waits in its
  inbox (partition mail never starts one) until they open the agent — up to
  7 days, like an unread message: nothing answers the chat for it
  meanwhile, and the `status` says `idle` instead of `working`. If xbind
  refuses the hand-off for good — the
  person can no longer use the agent — the chat gets a `notice` saying so. Treat
  `runId`, `inboxId` and `sessionKey` in a verdict or an `out` row as
  informational, as the bridge template does.
- **What passes through whom.** A linked person's DM and its reply are
  theirs, but they cross the adapter (it is their chat platform) and the
  global instance's outbox (whose row keeps its text and files only until
  the adapter acks it). The adapter's
  writers and the agent's are in that person's trust base, as the chat
  platform itself is.
- **Webhooks and other pushed events** get their answer from the global
  instance once the event is stored or handed on — a `2xx` means "taken",
  not "the person's partition ran it". A person's private push trigger
  needs a `match` that isn't empty (400) and that neither is a prefix of
  nor starts with anyone else's on the same source (409), so nobody can
  quietly take every event of a tile.

## The owner's API (on the agent)

Channels appear in `GET /automations` as kind `channel`. The agent
template's `API.md` (§Channels) lists all of the routes:

- `POST /channels/{id}/claim` (managers);
- `PUT` and `DELETE /channels/{id}`;
- `/peers` and `/pair {code}`;
- `/sessions` and `/sessions/reset`;
- `/outbox?state=failed` and `/outbox/{oid}/retry`.
