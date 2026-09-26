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

## Wiring

The adapter requests an http interface with service `agent-inbox`:

```jsonc
"interfaces": { "agent": { "kind": "http", "service": "agent-inbox" } },
"alwaysOn": true   // it holds an outbound connection (docs/elements.md)
```

The owner binds it to an agent, for example
`bx bind apps/slack agent=apps/agent`. The agent's `inbox` provide grants
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
| `pairing` | An unknown DM sender. A pairing code was queued for them. |
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

`reason` is one of `disabled`, `halted`, `data-class`, `rate` or
`target-gone`.

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
| `/approve`, `/deny` | Settle a pending tool approval; trusted peers only. |

Old conversations stay listed and searchable on the Automations page.

### Who may talk

| Policy | Values (default first) |
|---|---|
| `dm.policy` | `pairing`, `allowlist`, `open`, `disabled` |
| `groups.policy` | `allowlist` (by conversation id, `groups.allow`), `open`, `disabled` |
| `groups.requireMention` | `true` |
| `groups.followThreads` | `true` |
| `ratePerMin` | 20 per peer |

**Pairing:** an unknown DM sender gets an 8-character code, valid for an
hour and repeated at most every 10 minutes. The channel's owner approves
it on the Automations page. At most three strangers wait at once; beyond
that, new ones get no code.

### Lanes and tools

A reply to a chat is an egress, so channel conversations run in the
agent's **web lane**, which has no internal reach.

- With `privateLane: true` the private lane opens to trusted DM peers
  and to `trustedGroups`. Combining it with open DMs is refused.
- Revoking trust moves the conversation to a new web-lane run on the
  next message.
- `deny` lists tools a channel session never gets. The default is
  `schedule`, `unschedule` and `skill_manage`: a stranger's message
  leaves nothing behind that outlives the conversation.
- `system` adds to the prompt. The agent already tells the model where
  it is talking, that group members' text is untrusted, and that
  `NO_REPLY` means silence.

**The halt:** while the agent is halted, messages are kept and the
sender is told the agent is paused. A channel message never lifts the
halt.

## The owner's API (on the agent)

Channels appear in `GET /automations` as kind `channel`. The agent
template's `API.md` (§Channels) lists all of the routes:

- `POST /channels/{id}/claim` (managers);
- `PUT` and `DELETE /channels/{id}`;
- `/peers` and `/pair {code}`;
- `/sessions` and `/sessions/reset`;
- `/outbox?state=failed` and `/outbox/{oid}/retry`.
