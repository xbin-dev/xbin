# Adding a chat platform to this bridge

This tile connects an **agent** (a copy of the AI Agent template) to a **chat
platform**. It is a template: the copy you are in speaks only the built-in
*console* (the tile's page playing the platform) until you add a real one.
Your job, when someone asks for "Slack/Discord/Telegram/Matrix/… support", is to
write **one file** — `_backend/platform_<name>.go` — plus its test. Everything
else is done, and you should not need to touch it:

| Part | Who does it |
|---|---|
| Who may talk, pairing, linking chat accounts to xbin accounts | the **agent** |
| Which conversation a message joins (sessions), commands, lanes, tools | the **agent** |
| The agent-inbox contract, storing events before they are acknowledged, delivering them in order, uploading attachments, downloading reply files, splitting long replies, retries, never posting a reply twice, the typing hint, restarts, the page | the **bridge** (`_backend/*.go` — main, inbound, outbound, agent) |
| Connecting, receiving events, turning them into `Event`s, posting, formatting, fetching attachments | **your platform file** |

Read `_backend/platform.go` (the interface, ~150 lines) and `_backend/console.go`
(a complete platform with no network) first. The contract between the bridge and
the agent is `/docs/agent-inbox.md` (`curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/docs/agent-inbox.md?raw=1"`).

## The steps

1. **Write `_backend/platform_<name>.go`** with a type implementing `Platform`
   and register it:

   ```go
   func init() { registerPlatform("discord", func(b Bridge) Platform { return &discord{b: b} }) }
   ```

   Once a real platform is registered, the bridge runs it instead of the console
   (the page can switch back; `PUT /config {"platform": "console"}`).
2. **Declare what it needs** in `Info()`:
   - `Secrets`: each credential as a `SecretField{Name, Label, Hint, Prefix}`.
     The page shows a field for each and stores the value in this tile's vault;
     read them with `b.Secret(name)`. Never put credentials in files, kv or logs.
   - `Egress`: the net rule it needs, e.g. `internet:*.discord.com:443,internet:*.discord.gg:443`.
     The page shows `bx bind <tile> net=<rule>`.
   - `Setup`: short steps for the person (where to create the bot, which
     permissions, what to paste).
3. **Add client libraries** to `go.mod` if you need them (a WebSocket library,
   an SDK). The first build fetches them.
4. **Test it** in `_backend/platform_<name>_test.go` against an `httptest` fake
   of the platform (and a fake WebSocket where it uses one). Look at
   `bridge_test.go` for the fake agent and the test platform.
   `go test ./_backend/` must pass.
5. **Tell the person** what to do next: paste the credentials on the page, run
   the egress bind, bind the agent (`bx bind <tile> agent=apps/<agent>`), then
   claim the account on the agent's Automations page.

Keep your code in `platform_<name>*.go`. The template's own files may get
updates later, and they merge cleanly when you haven't edited them.

## Receiving: `Start`

`Start(ctx, b)` connects and runs until `ctx` ends (return `ctx.Err()`) or the
connection fails (return the error; the bridge starts you again after a
backoff). Don't retry forever inside; return and let the bridge do it.

- **Prefer connections that need no public URL.** The bridge is `alwaysOn`, so
  xbind keeps it running and restarts it. Options:
  - WebSocket gateways: Slack Socket Mode, Discord Gateway, Mattermost.
  - Long polling: Telegram `getUpdates`, Matrix `/sync`, IMAP IDLE.

  If a platform only pushes webhooks, add an `exposes` entry to `xbin.json` with
  just your webhook path, verify every request's signature, and tell the person
  to publish it (`bx expose`). The webhooks tile is an example.
- **Announce each account** once per start with
  `b.Account(ctx, Account{ID, Name, BotID, BotName})`. An account is a
  workspace, bot or server: one *channel* on the agent, claimed there. A bridge
  may serve several accounts (several Slack workspaces, several bots); announce
  each. `ID` must be stable.
- **For every inbound message**, call `b.Receive(event)`. Acknowledge the event
  to the platform **only after `Receive` returns nil**: the bridge has stored it
  by then, and a crash can't lose it.
- **Skip your own messages** (set `Sender.Bot`, or don't call `Receive`). Skip
  other bots too, and edits, deletions, joins and reactions.

### The `Event` fields: this is what drives the agent

Get these right and the agent's session semantics just work. Don't invent
session ids; the agent derives them from these facts.

| Field | What to put | Why it matters |
|---|---|---|
| `Account` | the `Account.ID` it arrived on | picks the channel |
| `ID` | the platform's id for this event/delivery | dedupe: a redelivery with the same id is dropped |
| `Conversation.ID` | the DM/room/channel id | a conversation per DM peer, per group |
| `Conversation.Type` | `dm` (one person and the bot), `group` (a multi-person DM), `channel` | DMs and groups follow different rules |
| `Conversation.Name` | the channel's name ("general") | shown to the model and the owner |
| `Thread` | the thread's **root** message id, when the message is in a thread | in groups, each thread is its own conversation |
| `MessageID` | this message's own id | a top-level mention starts a thread rooted here |
| `AssistantThread` | true in a platform's dedicated assistant pane | each such thread is its own conversation, even in DMs |
| `Sender.ID`, `.Name` | the person | pairing, linking, rate limits, attribution |
| `Mentioned` | true when the bot is addressed: every DM; in groups an @mention (**remove the mention from `Text`**) or a reply to one of the bot's messages | groups answer only when addressed, then follow that thread |
| `Text` | plain text: the platform's markup turned into words (mentions → `@name`, links → `label (url)`, entities unescaped) | what the model reads |
| `Command` | a slash command the platform parsed, as `"new hello"` | commands (also recognised in text starting with `/`) |
| `Files` | attachments (next section) | the model sees images, reads documents |

For thread replies in groups that don't mention the bot, deliver them anyway
with `Mentioned: false`. The agent keeps following a thread it is already in
and drops the rest. Don't deliver unaddressed top-level group chatter; the
agent would refuse it.

**Sessions**, i.e. what the agent does with these facts (see the agent-inbox
doc for the options its owner can set):
- a DM is one conversation per person (`/new` starts a fresh one);
- in a group or channel, a mention starts a thread-scoped conversation, and
  replies in that thread join it;
- an assistant-pane thread is its own conversation.

**Multi-channel:** allowed groups and channels are the owner's choice on the
agent, by `Conversation.ID`, so use stable ids.

## Replying: `Send`, `Format`, `Limit`, `Typing`

- `Send(ctx, account, to, msg)` posts one message. `to.Conversation` is where
  to post, in `to.Thread` when set (reply in that thread). `msg.Text` is
  already your markup (`Format`) and already within `Limit()`: the bridge
  splits long replies and calls `Send` per piece, files on the last. Return
  the platform's message id.
- **Errors:**
  - wrap what retrying can't fix in `Permanent(err)` (no such channel, not a
    member, missing permission);
  - rate limits in `RetryAfter(d, err)` (honour the platform's retry header);
  - anything else is retried a few times.
- `Format(markdown)` converts the agent's Markdown to your markup, and
  escapes characters the platform would interpret:
  - Slack: `*bold*`, `_italic_`, `<url|text>`; `&`, `<`, `>` must be entities.
  - Discord: Markdown is native.
  - Telegram: HTML is the safest parse mode; escape `<`, `>`, `&`.
  - Matrix: send `body` plain plus `formatted_body` HTML.

  If unsure, strip the markup to plain text.
- `Limit()` is the longest text one message may carry (e.g. 2000 for Discord,
  4096 for Telegram). Stay a little under.
- `Typing(ctx, account, to, on)` shows a typing/"thinking" indicator where the
  platform has one. Return nil where it doesn't.

## Attachments, both ways

- **In:** put each attachment in `Event.Files` as a `FileRef`:
  - inline `Data` when the event carries the bytes;
  - otherwise `URL`/`ID` plus `Name`, `Mime`, `Size`.

  The bridge calls your `Fetch(ctx, account, f)` to download a reference. Use
  the account's credentials; many platforms need an auth header even on file
  URLs. The bridge uploads to the agent and names the files in the message.
  The limit is 16 MiB a file; larger ones are skipped with a note.
- **Out:** the model attaches session files to its reply (`attach_to_reply`),
  and they arrive in `msg.Files` as `OutFile{Name, Mime, Data}`. Upload them
  with the platform's file API into the same conversation or thread. Some
  platforms need a multi-step upload (get an upload URL, PUT the bytes, then
  complete/share it).

## People and identity (linking)

Nothing here is yours to implement except delivering DMs and commands
faithfully. How it works:

1. A person DMs the bot. If they're new, the agent replies with a one-hour
   code; anyone can ask for one with `/link`.
2. Signed in to xbin, they open **this tile's page** and paste the code under
   *Link your chat account*. The page calls the agent directly, and xbind
   attributes the call to them.
3. The agent records the link. It never takes a person from the bridge's word,
   so no bridge can claim to be anyone.
4. From then on the agent knows the chat account is that xbin user:
   - their DMs become their own conversations, in their agent sidebar and
     private to them;
   - their group messages carry their id;
   - the owner can limit the channel to linked people (`dm.policy: linked`,
     `groups.linkedOnly`) and trust them (`trustLinked`).

   That is what an org or team wants.

So the platform must deliver DMs, keep the bot reachable by DM, and pass `/link`
through as a command (or as text). Mention it in your `Setup` steps.

## Other rules

- **Credentials:** only through `Info().Secrets` and `b.Secret`.
- **Your own state:** cursors, last seen ids, caches go in `b.Store()` (kv,
  private to the platform). Keep names in an in-memory cache (a day is fine).
- **Logs:** `b.Logf(...)` shows in the page's recent activity. Keep it short.
- **Keepalive:** pings on a socket are fine. Otherwise don't poll on a timer
  where the platform can push.
- **Security:** everything a platform sends is untrusted input.
  - Verify signatures when you take webhooks.
  - Never follow URLs from message text.
  - Only `Fetch` attachment URLs that belong to the platform.
- **The page and the console stay as they are.** The console remains
  available for debugging (switch to it on the page).

## Platform pointers

These are starting points only; **check the platform's current documentation**
for exact endpoints, scopes and limits.

- **Slack:** Socket Mode (an app-level `xapp-` token with `connections:write`,
  `apps.connections.open`, then the WebSocket; ack each envelope quickly).
  - Events: `message.im`, `app_mention`, channel `message` events for
    threads, `assistant_thread_started` (→ `AssistantThread`).
  - Post with `chat.postMessage` (`thread_ts`).
  - Files: upload URL, then complete; `files:read` for inbound.
  - A bot `xoxb-` token.
  - Generating the app manifest for the person helps a lot.
- **Discord:** the Gateway (a WebSocket; the message-content intent for
  reading messages).
  - DMs are channels of type DM; threads are channels with a parent.
  - Mentions come as `<@id>`.
  - Send via REST `POST /channels/{id}/messages`; attachments as multipart.
- **Telegram:** Bot API long polling (`getUpdates` with an offset kept in
  `b.Store()`).
  - Chat types: `private` → dm, `group`/`supergroup` → group/channel.
  - Forum topics via `message_thread_id`; replies via `reply_to_message`.
  - Files via `getFile`.
- **Matrix:** `/sync` long poll with a `since` token in `b.Store()`.
  - Rooms: a DM is a room with two members.
  - Threads via `m.thread` relations; media via the content repository.
- **Email (IMAP/SMTP):**
  - A conversation per sender (dm); `Message-ID`/`References` for threads.
  - Attachments are MIME parts.
  - `Send` is SMTP with `In-Reply-To`.
