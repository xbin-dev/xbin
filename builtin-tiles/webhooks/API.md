# webhooks — public webhook URLs for agents

Each **hook** is a public URL, `/hook/<id>`, checked by its secret. Each
delivery is pushed to the agents this tile is bound to as an event, and runs
their matching **triggers** (the agent template, D87). Anything that can POST
can use a hook: GitHub, a CI, a monitor. The data comes from outside, so it
is marked `public`, and only triggers allowed to take outside data run on it.

## Setting it up

1. **Bind it to the agents** it feeds (their `inbox` provide):
   `bx bind apps/webhooks agents=apps/agent` (more agents: `--add`).
2. **Publish the hooks.** Only `/hook/*` is public:
   `bx expose apps/webhooks hooks=apps/traefik --host hooks.example.com`
   (or `hooks=runtime` behind your own TLS).
3. **Make a hook** on the tile's page. Its name is the topic its events
   carry; with *topic from* set, the topic becomes `<name>/<value>`.
   Choose token or HMAC; the secret is shown once.
4. **On the agent**, make a trigger: *a tile bound to this agent pushes*,
   `apps/webhooks`, topics starting with the hook's name, data class
   *public*. A push nothing takes shows on the agent's Automations page
   with *Create a trigger*.

## The hook endpoint (public)

`POST /hook/{id}` takes a body of at most 256 KB and one of:

- **token:** `?token=<secret>` or `Authorization: Bearer <secret>`;
- **hmac:** `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the raw body>`,
  as GitHub sends it. The header and prefix are the hook's.

| Status | Meaning |
|---|---|
| 202 | An agent took it. |
| 401 | Bad token or signature. |
| 404 | No such hook, or no agent has a trigger for it. |
| 413 | The body is too large. |
| 503 | An agent is halted or unreachable, or none is bound: retry later. |

The event sent to each agent (`POST /adapter/event`, docs/agent-inbox.md):

- `eventId` is `<hook>:<the id the hook reads>`: from a header
  (`header:X-GitHub-Delivery`) or a JSON key (`json:id`), else a hash of the
  body. The same delivery never runs a trigger twice.
- `topic` is the name, plus `/<value>` from `topicFrom`
  (`header:X-GitHub-Event`, `json:action`).
- `data` is the JSON body (or `text`, for other bodies).
- `dataClass` is `public`.

## Routes (the tile's own page; admin, changes need write access)

| Method & path | Body | Purpose |
|---|---|---|
| `GET /hooks` | — | `{hooks, deliveries: [last 50], agents: [bound], host: the public host last seen}` |
| `POST /hooks` | `{name, id?, agent?, auth: token\|hmac, sigHeader?, sigPrefix?, eventIdFrom?, topicFrom?}` | Make a hook. Answers `{hook, secret}`; the secret is shown this once. |
| `PUT /hooks/{id}` | `{enabled?, name?, agent?, eventIdFrom?, topicFrom?}` | Change a hook. |
| `POST /hooks/{id}/rotate` | — | A new secret, shown once. The old one stops working. |
| `DELETE /hooks/{id}` | — | Remove a hook and its secret. |
| `GET /agent-triggers` | — | Each bound agent's push triggers for this tile. |
