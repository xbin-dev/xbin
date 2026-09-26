# slack — a Slack adapter for agents

Connects a Slack app to an agent built from the agent template (D86):

- people DM the bot, mention it in a channel, or use Slack's assistant pane;
- the agent answers in the same place (a mention gets its reply in a thread
  under it);
- each DM and each thread is a conversation of its own on the agent.

It uses **Socket Mode**, so the workspace needs no public URL. It is
`alwaysOn`: xbind starts it at boot and restarts it if it exits.

This tile knows Slack; the agent decides the rest. The agent owns who may
talk (pairing codes for new people, allowed channels), which conversation a
message joins, what that conversation may do (the web lane by default: a
reply leaves the workspace), and the chat commands. The contract between
them is `/docs/agent-inbox.md`.

## Setting it up (the tile's page walks through it)

1. **Create the Slack app** from the manifest on this tile's page
   (`GET /manifest?name=…`). It sets up the bot, its scopes, the events,
   the `/agent` command, the assistant view and Socket Mode. Go to
   api.slack.com/apps → *Create New App* → *From an app manifest*.
2. **Install it** to your Slack workspace. Copy the *Bot User OAuth Token*
   (`xoxb-…`) from *OAuth & Permissions*.
3. **Make an app-level token.** Under *Basic Information* → *App-Level
   Tokens*, generate one with the scope `connections:write` (`xapp-…`).
4. **Paste both** on the page. They go to this tile's vault.
5. **Let it reach Slack:**
   `bx bind apps/slack net=internet:*.slack.com:443`.
6. **Bind it to an agent:** `bx bind apps/slack agent=apps/agent`. The
   Slack account then shows up on that agent's Automations page, where a
   manager claims it and sets its rules.

## Routes (the tile's own page; admin)

| Method & path | Body | Purpose |
|---|---|---|
| `GET /status` | — | `{state: {phase, detail, team, botName, channelId, claimed, since}, events: [last 50], hasBotToken, hasAppToken, agent, apiBase}`. `phase` is `needs-tokens`, `needs-agent`, `connecting`, `connected` or `error`. |
| `PUT /config/tokens` | `{botToken?, appToken?}` | Store the tokens (needs write access to the tile). |
| `DELETE /config/tokens` | — | Remove both tokens. |
| `PUT /config` | `{apiBase}` | The Web API's base: Slack's, or a loopback fake for tests (`hack/fakeslack`). |
| `POST /reconnect` | — | Start over (tokens, auth.test, hello to the agent, the socket). |
| `GET /manifest?name=` | — | The Slack app manifest (JSON). |

## Behaviour

**What reaches the agent**
- DMs.
- Mentions: the mention is stripped and `mentioned` is set.
- Replies in a thread. The agent decides whether it follows the thread.
- Messages in assistant-pane threads.
- `/agent <command>` in a DM.

**What doesn't**
- The bot's own messages and other bots'.
- Edits, deletions and joins.
- Channel messages that neither mention the bot nor reply in a thread.

**Inbound safety**
- Every event is stored before it is acknowledged to Slack.
- Stored events are delivered in order after a restart.
- Duplicates are dropped by the agent (by the event id).

**Replies**
- They arrive from the agent's outbox and are converted from Markdown to
  mrkdwn.
- They are split into pieces of about 3500 characters.
- Rate limits are waited out.
- A reply is acknowledged with its Slack `ts`, recorded first so a crash
  never posts it twice.
- Failures Slack calls permanent are reported to the agent, and the owner
  can retry them there.

**Status and names**
- While the agent works on an assistant-pane thread, the pane shows
  "is thinking…".
- User and channel names are cached for a day.

Later: streamed replies, files in and out, the HTTP Events API mode.
