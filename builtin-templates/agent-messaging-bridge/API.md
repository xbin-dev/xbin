# agent-messaging-bridge — a chat platform for an agent

Connects an agent (a copy of the AI Agent template) to a chat platform over the
agent-inbox contract (`/docs/agent-inbox.md`, D86).

- **The agent** owns the conversations, who may talk, what they may do and who
  they are.
- **This tile** owns the platform.

It is a **template**. A copy speaks only the built-in *console* (its page plays
the platform) until a coding agent in its terminal adds a platform: one file,
`_backend/platform_<name>.go`. The guide is `AGENTS.md`.

## Setting it up

1. Bind it to an agent: `bx bind <this tile> agent=apps/agent`.
2. On the agent's **Automations** page, claim the new channel (Console, or
   your platform's account) and set its rules.
3. With a real platform:
   - paste its credentials on this page (they go to the vault);
   - run the egress bind the page shows (`bx bind <this tile> net=…`).

To link your chat account to your xbin account, message the bot (a new
person gets a code; `/link` asks for one). Then paste the code on this page
while signed in. The page calls the agent directly, so xbind vouches for who
you are.

## Routes (the tile's own page; changes need write access)

| Method & path | Body | Purpose |
|---|---|---|
| `GET /status` | — | `{state: {platform, phase, detail, since, accounts: [{id, name, channelId, claimed}]}, info: {name, title, secrets, egress, setup}, secretsSet, events, platforms, agent}` |
| `PUT /config` | `{platform}` | Which registered platform runs (the console, or one you added). |
| `PUT /config/secrets` | `{<name>: <value>}` | The platform's declared credentials, stored in the vault. `""` removes one. Undeclared names are refused. |
| `POST /reconnect` | — | Restart the platform connection. |
| `POST /console/send` | `{as: {id, name}, conversation: {id, type, name}, thread?, mentioned?, text, command?, files?: [{name, mime, data (base64)}]}` | The console: a pretend person says something. |
| `GET /console/transcript` | — | The console's last 200 lines, both directions. |
| `GET /console/files/{id}` | — | A file the agent sent (the console keeps the last 20). |

## Behaviour

- Every inbound event is stored before the platform is acknowledged, then
  delivered in order: attachments are uploaded first (≤16 MiB each), then the
  message is sent naming them. A restart resumes where it left off.
- Replies are pulled from the agent's outbox:
  - formatted and split for the platform, files on the last piece;
  - rate limits are waited out and transient failures retried;
  - permanent failures are reported to the agent, whose owner can retry them;
  - each reply's platform id is recorded before the ack, so a crash never
    posts twice.
- "Working" hints become the platform's typing indicator, where it has one.
- `alwaysOn`: xbind starts it at boot and restarts it after an exit.
