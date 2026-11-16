# Protocol: delegation (a person's workspace agent)

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
how a sealed system component acts as its person (the delegate call), the
routes the person's clients and the agent call about it (`/delegate/…`):
the switches, approval requests and tickets, standing approvals, admin
change sets and the audit log. The rules — who may delegate, what a
delegate reaches, what a tile sees — are in
[auth.md §Delegation](/docs/auth.md#delegation-a-persons-workspace-agent).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it. Three callers appear:

- **the person** — their own top-level credential: a browser session, the
  app's session or a device's. Never a view-as session, a tile's
  credential or the root token. **Everything that grants is the person's
  alone**: approving a request, a standing approval, resuming a paused
  agent, the admin opt-in, deciding a change set, reading the log;
- **the agent's page** — the sealed component's own frame, when its token
  was minted under the person's own login. It shows the agent's cards, and
  it renders what the agent wrote — so it only **reads** (the state, the
  requests, the standing approvals, the change sets) and may **pause** the
  agent. A script injected into it can approve nothing. The approval
  itself is given in chrome xbind serves, or in the app;
- **the agent** — the sealed component's backend making a *delegate call*
  (below). It asks; it never answers itself.

Nothing on this page exists until an admin turns workspace agents on
(`PUT /delegate/workspace`), and no credential delegates on an xbind that
ships no sealed workspace-agent component.

## The delegate call

A person's partition of a sealed component that holds delegation calls
xbind **as that person** by sending its own instance token under another
scheme, on the gateway socket:

```
Authorization: XBin-Delegate <XBIN_TOKEN>
```

- The same token as `Bearer` is the component itself (its own kv, vault,
  bus…). Under `XBin-Delegate` it is the person the partition belongs to.
  Nothing the caller sends names the person.
- Honoured on the **gateway socket** only (`XBIN_GATEWAY`), and only on
  `/api/…`. Everywhere else — the console listener, `/c/`, `/ws/…`,
  `/docs/`, a device's tunnel — it is no credential: `401`.
- `403 {error, refusal}` while delegation is off: `paused` — the person
  paused their agent; `workspace-off` — the workspace's switch; `no-sandbox`
  — an xbind that runs tiles without sandboxes (`--isolate` off; `--dev`
  and `--no-auth` excepted); `no-agent` — the person wasn't given an agent
  (§The switches: the admin role alone gives none); `held` — the agent
  is held on this xbind (`held` of `GET /delegate/state`).
- A request that carries the scheme is judged as the delegate's or refused,
  whatever else it carries.
- A call to a tile's API carries **these request headers and no other**
  to the tile: `Accept`, `Accept-Encoding`, `Accept-Language`,
  `Content-Type`, `Content-Length`, `If-Match`, `If-None-Match`,
  `If-Modified-Since`, `If-Unmodified-Since`, `Range`, `Cache-Control`,
  `User-Agent` (xbind adds its `X-XBin-*`). Cookies, `Origin`, forwarded
  addresses, `Content-Encoding` and every `X-…` switch are dropped: a
  header a tile could act on would be a part of the call nobody approved.
- A tile's answer never sends the call on: `Location`, `Content-Location`
  and `Refresh` are removed from it (the status stays). Every request is
  judged as it arrives, on its own.
- The method judged is the method made: `X-HTTP-Method-Override`,
  `X-HTTP-Method` and `X-Method-Override` are removed before a tile sees
  the call, and a call whose query carries `_method` is refused (`403`).

Request headers a delegate may add (xbind reads them at the gate and never
forwards them):

| Header | Meaning |
|---|---|
| `X-XBin-Agent-Session: <id>` | the agent session (handle) the call runs under — required on every call but the bootstrap ones ([Agent sessions](#agent-sessions)). It is where the conversation's scope and model come from; `X-XBin-Agent-Model` is ignored |
| `X-XBin-Dry-Run: 1` | describe the call instead of making it (below) |
| `X-XBin-Ticket: <request id>` | the approved request this side effect runs under (below) |

**What a delegate reaches** is a closed list — the delegation cap:

| Class | Routes |
|---|---|
| reads, at the person's level | `GET /whoami`, `/components`, `/components/<path>`, `/tile-status`, `/tile-report`, `/logs`, `/alerts`, `/screens`, `/templates`, `/agent/providers`, `/term/sessions`, `/term/sessions/<id>`, `…/events`, `…/log`, `…/diff`, `/agent/history`, `/agent/history/<id>/events`, `/code/tree`, `/code/file`, and its own `/delegate/state`, `/delegate/approvals`, `/delegate/requests/<id>`, `/delegate/changesets[/<id>]` |
| a tile's API | `/api/<tile>/…` where the person can read the tile: `GET`/`HEAD` freely; any other method needs the person's approval — except on a tile's declared MCP endpoint, where `initialize`, `ping` and `tools/list` are reads and every other POST is refused (§MCP tiles) |
| acts | `POST /preview` (a picture of one tile, as the person: [preview.md](/docs/protocol/preview.md) — no approval; rate-limited), `POST /term/sessions` (approval, or the person's preference), `POST /templates/new` (approval); on sessions **it opened**: `…/prompt`, `…/queue`, `DELETE …/queue[/<qid>]`, `…/cancel`, `DELETE /term/sessions/<id>`; asking: `POST`/`DELETE /delegate/requests…`, `POST`/`DELETE /delegate/changesets…`; the web: `POST /delegate/web/fetch` (§Web and MCP: the person's approval of the URL when the conversation holds data); MCP tiles: `POST /delegate/mcp/call` (approval of each tool, §MCP tiles) |
| admin reads, only for an admin who opted in | `GET /grants`, `/bindings`, `/orgs` (without SSO group rules), `/limits` — answered as the person |
| refused | everything else: `403 {error, refusal: "not-delegated", class}`. In particular: answering a tile agent's permission request or question, a session's options and restart, terminals, deleting or disabling tiles, code writes and pull requests, the account, devices and sign-in tickets, frame tokens and path tickets, view-as, a person's consents, tile stores, the admin API, and every approval on this page |

A tile that isn't there and a tile the person can't read get **one
answer** — `403 {refusal: "tile"}` with the same words, naming no tile — on
a call, its dry run, a request for approval and the routes that name a
tile.

Listings leave out the tiles whose org keeps the workspace agent out (on
the model it says it is on) — the rows of `/components`, `/tile-report`,
`/term/sessions` and `/agent/history`, a tile's alerts in `/alerts`, the
tiles and folder items of `/screens`; a request about such a tile is `403
{refusal: "tile"}` with the org's reason. The org rules are the ones admins
set ([protocol/orgs.md](/docs/protocol/orgs.md): `workspaceAgent`,
`models`, `harnesses`); whether an agent starts a tile agent without asking
is the person's agent preference (`PUT /agent/prefs`, theirs alone to
set). The rules themselves, the connection view (`/agents/connection`,
which names the tiles the rules block) and the LLM connector's routes are
not in the cap. No WebSocket upgrade (`403`). An admin's
agent is not an admin.

Refusals are `403 {error, refusal, class, docs}` with `refusal` one of
`not-delegated`, `tile`, `needs-approval`, `ticket`, `admin-read`, the
off reasons above, and the web's (§Web and MCP);
`413 {refusal: "too-large"}` for a ticketed body over 256 KiB; and
`429 {refusal: "rate-limited"}` + `Retry-After` past 30 acts a minute (a
burst of 30, one more every 2 seconds; reads and dry runs don't count).

**A call ends with what allowed it.** A call in flight (a followed stream,
a slow tile) is cut when the person pauses their agent or the workspace's
switch goes off — at once — and when the person is disabled or deleted,
loses the tile, the tile or its org turns agents away, or the standing
approval a write ran under is revoked: at once where xbind makes the
change, within 3 seconds otherwise.

### Dry run

Any allowed delegate call with `X-XBin-Dry-Run: 1` answers `200` and runs
nothing:

```
{dryRun: true, allowed, class, as, agent, sideEffect,
 tile?, owner?, level?, role?, agentAccess?,
 reversible?: false, needsApproval?, under?, refusal?}
```

`class` is `read`, `act`, `admin-read`, `tile-read` or `tile-write`;
`level` and `role` what a tile call would carry; `needsApproval` whether a
ticket is needed now (`false` with `under: "approval:<id>"` when a standing
approval covers it); `allowed: false` with `refusal` when the tile or its
org would refuse it. A call the cap refuses is refused as without the
header.

A dry run of a session start (`POST /term/sessions`, with the body the
start would send) answers for that start: `tile`; `needsApproval: false`
with `under: "preference"` when the person's preference lets that harness
start there without asking and the body names no mode, network, API
access, VM or resume of its own; `allowed: false` with the reason when the
start would be refused whatever approves it (its first prompt begins with
a harness command, or the conversation's taint keeps it from briefing a
session on that tile — §Agent sessions).

### What a tile sees

A delegate's call to `/api/<tile>/…` reaches the tile's primary — the
person's own partition of a partitioned tile — with:

| Header | Value |
|---|---|
| `X-XBin-From` | `user:<id>` |
| `X-XBin-User`, `X-XBin-User-Level` | the person and their level on the tile now |
| `X-XBin-Role` | `reader` for read, `writer` for write and terminal — never `admin` |
| `X-XBin-Agent` | the sealed component's path: the call is the person's agent's, not their own hand |
| `X-XBin-Partition`, `X-XBin-Partition-Id` | the person's partition, on a partitioned tile |

A tile limits what agents use of its API with `"agent"` in its manifest
(`"off"`, `"read"`, `"full"`), and its backend tells an agent's call from
the person's own by `X-XBin-Agent`
([elements.md → A person's agent](/docs/elements.md)).

`?xbin-partition=global` on such a call reaches a partitioned tile's global
instance under the same rules as the person's own partition: with
`partitionConsent` on it needs the person's consent for *agent → tile*, and
it is counted in their ledger.

## Agent sessions

An **agent session** is xbind's record of one conversation of a person's
agent: its **scope** (what the person let it see), its **model**, and its
**taint** (the orgs whose data it has touched). The agent no longer says
what model it is on: it opens a handle, and every call carries it.

```
POST   /delegate/sessions       the person's agent. {scope: {orgs: […],
                                 tiles: […]} | null, model:
                                 "<upstream>/<model>"} → {id, scope,
                                 model, tainted: [org], created, used,
                                 connection: {counts}}. scope null:
                                 everything the person can see; orgs takes
                                 "workspace" for the tiles in no org. At
                                 most 32 a person and holder (the least
                                 recently used go); gone after 24 h unused
GET    /delegate/sessions/<id>  the person's agent. The session
PATCH  /delegate/sessions/<id>  the person's agent. {scope?, model?} → the
                                 session. The scope may always change. The
                                 model may change only to one every
                                 tainted org's rules allow: 409 {code:
                                 "model_taint", orgs, reason, error}
                                 otherwise — open a new session
DELETE /delegate/sessions/<id>  the person's agent. 204
GET    /delegate/sessions/<id>/connection
                                 the person's agent. [?whatIf=<model>] →
                                 {id, model, scope, orgs, tiles:
                                 [{tile, name, org, harnesses}], blocked:
                                 [{org, blocked, reason, rule}], counts,
                                 tainted, whatIf?: {model, connects,
                                 disconnects, counts, allowed,
                                 taintedOrgs}}
POST   /delegate/sessions/<id>/allows
                                 the person's agent. {orgs: [id, …]} →
                                 {id, model, allowed, reasons: {org:
                                 why}}: does the session's model satisfy
                                 those orgs' rules. 403 for an org the
                                 person can't see
POST   /delegate/sessions/<id>/taint
                                 the person's agent. {orgs: [id, …]} → the
                                 session, with them in tainted. Add-only.
                                 403 for an org the person can't see; 409
                                 {code: "model_taint", orgs, reason} for
                                 one the session's model may not see
GET    /delegate/start-defaults  the person's agent. ?tile= → {tile,
                                 harness, harnesses: [{id, name, mode,
                                 asks, net, api, needsTicket, noSignIn}],
                                 why?}: what a tile agent started there
                                 opens with; noSignIn: it runs on the
                                 workspace's LLM connector, no sign-in of
                                 the person's needed. 403 without terminal
                                 access to the tile
GET    /delegate/map            a person, or their agent. {user, tiles:
                                 [{tile, title, kind: "app" | "page",
                                 orgs, state, agent, tileAgents}],
                                 screens: [{id, name, org?, tiles}], orgs:
                                 [{id, name, level, admin, suspended}],
                                 leftOut: {hiddenByYou, blocked}}
GET    /docs/search             signed in. ?q= (≤ 200 bytes; 8 words)
                                 &limit= (≤ 20, default 8) → {q, results:
                                 [{path, title, section, heading, score,
                                 snippet}], total}
GET    /docs/page               signed in. ?path=&section= → {path, title,
                                 section?, markdown, truncated?, sections:
                                 [{section, heading}]}; at most 64 KiB
```

- **Every delegate call carries `X-XBin-Agent-Session`.** Without a live
  handle of that person and that holder: `403 {refusal: "no-session"}`.
  Excepted, so an agent can start: `POST /delegate/sessions`,
  `GET /delegate/state`, `GET /whoami`, `GET /docs/search`, `/docs/page`.
- **The model is the handle's.** The orgs' model rules are asked with it,
  and the LLM connector holds the holder's own model calls to it
  ([llm.md](/docs/llm.md)): the model a tile's data is judged against is the
  model the conversation's text goes to.
- **The scope confines.** A tile outside it is `403 {refusal: "hidden"}`
  ("hidden by you"), and left out of listings; a tile the orgs' rules keep
  the agent out of stays `{refusal: "tile"}` with the org's reason.
- **Taint.** The first allowed call that touches a tile — a read, a row of
  a listing, the map — adds the tile's orgs to the handle's `tainted`. It
  only grows.
- **The connection view here is redacted**: connected tiles by name;
  blocked, hidden and no-access ones as counts and org-level reasons. It
  enters a model's context, and a blocked tile's name is the blocking
  org's data. The agent's page shows its person the full view:
  `GET /agents/connection?session=<id>[&whatIf=]`, answered only for the
  holder's own frame and its driver's session
  ([agents.md](/docs/protocol/agents.md)).
- **The map** is what the person sees, compact, within the scope and the
  rules; what is left out is counted. A tile's own `API.md` is read with
  `GET /code/file?component=<tile>&file=API.md` (the code-read rule: read
  on the tile).
- **The session directory**: `GET /term/sessions?origin=agent` lists the
  tile agents this agent started for the person; `&compact=1` answers
  `[{id, cwd, kind, provider, mode, status, origin, pending, questions,
  turn, last}]` — for an agent session, how many permission requests and
  questions wait for its person, the number of the turn it is on (0
  before its first prompt), and the last line it wrote (`last`: only in a
  person's own listing, their agent's included — never in an admin's of
  someone else's). With `&ids=a,b` it
  adds, for each id that isn't live any more, `{id, status: "ended",
  endedBy, ended?}`: `endedBy: "exit"` when xbind recorded the end (its
  history entry; `ended` is when), `"restart"` when it has no record of
  one — the session was running when xbind stopped.
- **Taint binds what the agent tells tile agents.** What a conversation
  sends a tile agent — a start's first prompt, `…/prompt`, `…/queue` —
  goes to THAT session's model. So each is refused (`403 {refusal:
  "taint"}`, with the org and its rule) unless the session's model source
  satisfies the model rule of every org in the handle's `tainted`: the
  session's own tile is under that org's rule (its sessions run on the LLM
  connector alone, which asks the rule for every call), or the session is
  held to the connector — not merely offered it — on a model the rule
  allows (the connector's model it calls: the one its model setting names
  when that is one of the connector's, else the connector's default). A session on its person's own sign-in is refused wherever a
  tainted org has a model rule other than `any`: nobody can say which
  model it calls — and so is a running session nothing says who serves
  (a workspace with no connector, or one not settled yet). A session that
  isn't started yet is judged by what xbind can vouch for beforehand: a
  start passes on a tile under the rule, and where the agents policy holds
  coding agents to the connector (the workspace's `connector-only`) when
  every model the session may start on — the one the start names and the
  connector's default — satisfies the rule; a start that would run on the
  person's own sign-in, or by their choice, is refused. An org with no model
  rule binds nothing. Reading a session — its row, events, log or diff —
  adds its tile's orgs to the taint like any other read. A session's
  person changing its model later is their own act: it isn't judged again.
- **What a session the agent started takes from it.** Besides the taint
  rule, a prompt or a queued message is refused:
  `403 {refusal: "command"}` when its text begins with `/` (after any
  white space) — every shipped harness reads that as one of its own
  commands (a model change, a sign-out, a project command that carries its
  own tool permissions, a restore of files), and those are the person's to
  give; a start's first prompt alike, approved or not, and a request for
  such a start isn't taken. `403 {refusal: "mode"}` when the session's
  CURRENT mode doesn't ask before every edit and command and isn't the
  mode its person approved the start in: one they have since switched to
  such a mode takes no more of the agent's messages until they switch it
  back. The person's own messages are never judged by these.
- **Taint from elsewhere.** What the agent keeps itself (its notebook) is
  stamped with orgs by the agent: before reading a page into a
  conversation it asks `…/allows` for the page's orgs, and after reading
  adds them with `…/taint`; a page it writes carries the session's whole
  `tainted` set. Taint is add-only — no call removes an org — and binds
  later model changes like taint xbind recorded. A holder keeps what a
  conversation holds with the conversation, and gives a handle it opens
  again for it (the old one gone after 24 h unused) all of it with
  `…/taint` before the handle carries anything: a `409 model_taint` there
  means the conversation can't go on on that model.
- Audit rows carry `session`.

## Web and MCP

A person's agent reads the web and uses MCP servers only through xbind
(D239): its backend's sandbox has no network but its model. Both are **off
until an admin turns them on** for the workspace, and then each person
turns them on for their own agent; an org may keep its data off the web.
Each layer only narrows the one above:

1. **the workspace policy** — `web` and `mcp` of `PUT /delegate/workspace`
   (§The switches), preset and capped by the host's file
   (`XBIN_WSAGENT_CONFIG`, below);
2. **the org rules** — `web` and `mcp` of an org's agent rules
   ([protocol/orgs.md](/docs/protocol/orgs.md)), asked for every org
   whose data the conversation holds (its handle's `tainted`);
3. **the person** — `web`, `mcp`, `mcpReadOnly` of `PUT /delegate/state`,
   their own session's to turn on (the agent's page can't).

```
POST   /delegate/web/fetch      the person's agent. {url} → {url,
                                 finalUrl, status, contentType, title?,
                                 text, bytes, truncated, redirects,
                                 readWeb: true}
```

**What a fetch is.** One `GET`, made by xbind: no body, no cookies, no
`Authorization` or `Referer`, a fixed `User-Agent`
(`xbin-workspace-agent/1 …`), never through a proxy of the environment.
The name is resolved and **every address it resolves to must be public**
— not loopback, private (RFC 1918, ULA), link-local (the cloud metadata
address among them), multicast, unspecified, CGNAT, documentation,
benchmarking or reserved, nor an IPv6 form that maps or translates to one
(`::ffff:…`, NAT64, 6to4, Teredo) — and the connection is made to the
address that was checked, the socket's own peer checked again as it
connects: a DNS answer that changes in between reaches nothing. Names of
this machine and of local networks (`localhost`, `*.local`, `*.home.arpa`,
`*.internal`) are refused by name. Redirects are followed by xbind, at
most 5, each hop judged again (scheme, length, the lists, the address). At
most 2 MiB of the answer is read (`truncated` past it), in 20 s in all;
the types read are `text/html`, `application/xhtml+xml`, `text/plain`,
`text/markdown`, `text/csv`, `application/json`, `application/xml`,
`text/xml`, `application/rss+xml` and `application/atom+xml` (UTF-8 or
Latin-1) — anything else is refused unread. HTML comes back as text —
scripts, styles and comments dropped, headings marked `#`, a link's
address after its words — at most 64 KiB.

**The URL.** `http` or `https`, at most 2 KiB, no user name or password, a
host that is a name or a canonical address (a dotted quad, a bracketed
IPv6 address without a zone — `http://2130706433/`, `http://0x7f.1/`,
`http://0177.0.0.1/` are refused), allowed by the lists in force (a URL
must match every allow list that isn't empty, and no deny list), and never
one of the workspace's own hosts (its external URL, the tiles' domain and
the names under it, the hostnames its tiles are published at).

**A URL is a way out.** While the handle's `tainted` names any org — the
tiles in no org (`workspace`) included — a fetch needs its person's
approval of that exact URL: the agent files `POST /delegate/requests
{method: "POST", path: "/api/xbin/delegate/web/fetch", body:
"{\"url\": …}"}` and sends the same body with `X-XBin-Ticket` once it is
approved. The request is `kind: "web"`, its `preview` `{class: "web",
url, host, orgs, why}` — `url` in the one spelling the fetch will name
(the host in its ASCII form, lower case, no fragment); it is approved once (`hour` and `always` are
refused: the URL is the data). That holds unless every org in `tainted`
allows the web outright (its rule `web: "allow"`); an untainted
conversation fetches freely within the policy. A request for a fetch that
needs no approval is `400`; one the reach refuses whatever approves it,
or a URL that would be refused anyway, `403`.

**After a fetch** the handle is `web: true` — "this conversation has read
the web" — and every request filed under it says so (`preview.readWeb`).
A backend that renews a handle gives it that too (`POST
/delegate/sessions/<id>/taint {web: true}`). What a page says is
untrusted input: the agent reads it as data, never as instructions.

**The reach.** Every answer that carries a session (`POST`, `GET`,
`PATCH /delegate/sessions…`, `…/taint`) adds `web` and `reach: {web,
search, mcp}`, each `{state, why?, code?, orgs?}`: `state` is `on` (it
may), `asks` (each use needs the person's approval — the conversation
holds data of `orgs`) or `off` (`why` in a person's words, `code` the
refusal a call meets). Search is **not built yet**: `reach.search` is
`off`, and `web.search: true` is refused (`400 "not available yet"`). When
it is, it will be a provider's own search through the LLM connector, only
where the web is `on` without asking — a search's words can't be approved
one by one; there is no third-party search service.

**Refusals** (`403 {error, refusal, class: "web", docs}` unless said):
`web-off` (the workspace policy, or the host's), `web-person` (the
person's setting), `web-org` (an org whose data the conversation holds
keeps its data off the web: no fetch for the rest of the conversation — a
new session that hasn't read it can), `needs-approval`, `ticket`,
`web-url` (400), `web-host`, `web-address`, `web-redirect`, `web-type`
(415), `web-unreachable` (502), `web-timeout` (504), `rate-limited` (429
with `Retry-After`: 12 fetches a burst, one more every 5 s, a person; on
top of the act limit). Every fetch — refused ones up to the log's rate —
is a row of the person's log (`class: "web"`, `path` the URL's scheme,
host and path with `?…` for a query, `bytes`, `status`).

### MCP tiles

An **MCP tile** is one whose manifest provides an `http` interface of
service `mcp` (`"provides": {"mcp": {"kind": "http", "service": "mcp"}}`);
its endpoint is `/api/<tile>/mcp`. With the workspace policy's `mcp.mode`
at `tiles` and the person's `mcp` on, the agent uses it as its person, by
two ways — each judged by xbind, under the tile's own rules (the person's
level on it, its `"agent"` level, its orgs' rules, the session's scope):

```
POST   /api/<tile>/mcp          the person's agent. One JSON-RPC request
                                 (not a batch, at most 64 KiB, nothing but
                                 jsonrpc, id, method, params) of exactly
                                 initialize, ping or tools/list: a read,
                                 asked of nobody — Mcp-Session-Id and
                                 MCP-Protocol-Version are carried. Any
                                 other POST there is 403 {refusal:
                                 "mcp-call"}
POST   /delegate/mcp/call       the person's agent. {server: "<tile>",
                                 tool, arguments} → {server, tool, isError,
                                 content: [{type, text?}], structured?}
```

**Read one way.** Every body xbind judges for an MCP server — a read on
the endpoint, a tool's call — is read strictly: one JSON object, UTF-8,
its members in exact lower case from the route's list, no key twice in
any object at any depth, nothing after it (else `mcp-call`: 403 on the
endpoint, 400 on `/delegate/mcp/call`). What reaches the tile is the
request xbind builds from what it read — `{"id", "jsonrpc", "method",
"params"}`, re-encoded — never the agent's bytes: a tile whose own parser
reads keys case-sensitively or keeps the first of two equal keys sees
what xbind judged.

- **A tool's call goes through xbind**, which is the MCP client: it opens
  a session with the server as the person (`initialize`), lists its tools
  (the tool must be there: `404 {refusal: "mcp-tool"}`), and calls it. A
  call to the endpoint itself is refused (`mcp-call`), and so is a request
  for one as a tile call (`400`): a ticket or standing approval of `POST
  /api/<tile>/mcp` would cover every tool of the server, and the MCP
  policy could be stepped around with an ordinary tile call.
- **Each tool asks the first time.** The agent files `POST
  /delegate/requests {method: "POST", path: "/api/xbin/delegate/mcp/call",
  body}` and sends the same body with `X-XBin-Ticket` once it is approved.
  The request is `kind: "mcp"`, its `preview` `{class: "mcp", server,
  serverName, tool, title?, description, readOnly, arguments, standing:
  {tool}}`. Approved with `grant: "always" | "hour"` it leaves a standing
  approval of `scope: "tool"` — exactly that server and that tool, any
  arguments (`method: "MCP"`, `path`: the tool's name); `scope: "call"` or
  `"tile"` is refused for it. A standing approval of every write to the
  tile (`scope: "tile"`) covers its tools too.
- **Read-only tools.** A tool the server marks `readOnlyHint` runs without
  asking when the person turned on `mcpReadOnly` ("MCP tools marked
  read-only don't ask me") — the mark is the server's word, and the tile's
  own access rules still hold. A tile at `"agent": "read"` takes read-only
  tools only (`403 {refusal: "tile"}`), whatever is approved.
- **What a tool answers is the tile's data**: the conversation is tainted
  with the tile's orgs, as by any read of it. An org whose rule is `mcp:
  "org"` keeps a conversation that holds its data to the MCP tiles under it
  (`403 {refusal: "mcp-org"}` for any other, a read included).
- Refusals beside those: `mcp-off` (the workspace policy), `mcp-person`
  (the person's setting), `mcp-unreachable` (502), `needs-approval`,
  `ticket`, `tile`, `hidden`. A dry run of a call says whether it would
  need approval (`needsApproval`) or what covers it (`under`). Every call
  is a row of the person's log (`class: "mcp"`, the endpoint's path, the
  tile, the ticket or `under`); a read on the endpoint is `class:
  "mcp-read"`.
- `GET /delegate/map` marks an MCP tile's row `mcp: true`.
- **Not yet**: `mcp.mode: "listed"` — servers an admin lists by URL, with
  credentials xbind holds — is refused (`400 "not available yet"`).

### The host's file (`XBIN_WSAGENT_CONFIG`)

Whoever runs xbind can preset and cap the web and MCP policy from a file,
as `XBIN_LLM_CONFIG` holds the LLM connector ([llm.md](/docs/llm.md)):
JSON with comments, read at boot — xbind doesn't start with a file it
can't read — and again when it changes (looked at at most every 2 s); an
invalid change is refused, the last good file stays in force and the
admin's view says so (`host.error`).

```jsonc
// XBIN_WSAGENT_CONFIG=/etc/xbin/wsagent.jsonc
{
  "web": { "mode": "read", "allow": ["docs.example.com"], "deny": ["intranet.example.com"], "search": false },
  "mcp": { "mode": "tiles" }
  // "lockAll": true    // every web and MCP setting is the host's
}
```

- `locks` (of `GET /delegate/workspace`) names the settings the file
  holds, by their names in the `PUT`: `{"web.mode": {locked: true,
  reason: "set by your host", ceiling?}, …}`. A write the file refuses is
  `409 {error, settings}`, naming them.
- `web.mode`, `web.search` and `mcp.mode` are a **preset and a ceiling**:
  in force is the admin's value when they set one, never past the host's,
  and the host's without one. An admin can narrow (turn it off); a write
  past the ceiling is refused.
- `web.allow` and `web.deny` always hold, beside the admin's own lists;
  they lock nothing (`policy.web.hostAllow`, `hostDeny`).
- `"lockAll": true`: every web and MCP setting is the host's — `locks`
  names each, without a ceiling — and writing any is refused.

## Notices to the agent's backend

A holder's backend learns that something it waits on changed without
polling: xbind POSTs to the person's partition of the holder, on the path
the holder's entry in xbind's table names, over the delivery path
partition mail's doorbell uses (it arrives as `xbin/mail`, role `writer`,
with the person's partition).

```
{"partition": "user:<id>", "events": [{"kind": "request", "id": "<id>"}, …]}
```

| `kind` | When | `id` |
|---|---|---|
| `request` | an approval request was approved, denied, withdrawn, used, or expired | the request |
| `approval` | a standing approval was added or taken back | the approval |
| `changeset` | a change set was decided, applied or withdrawn | the set |
| `state` | the person paused or resumed their agent, or changed what it may propose or reach (the web, MCP tools); delegation was switched on or off for the workspace, or its web and MCP policy changed | — |
| `session` | a tile agent this holder started changed status (idle after a turn, waiting for the person, ended) | the session |
| `resync` | events were dropped (more than 256 waited): re-read everything | — |

A notice carries **ids and kinds only**. The backend re-reads what an event
names through this API. Treat a notice as a hint — "look again" — never as
a fact: it is input from outside the conversation. Coalesced (one queue a
person; a kind and id once); at least once (an event is sent again, with
backoff, until a delivery that carried it is answered 2xx, for up to an
hour); answer 2xx quickly and do the reading afterwards. Across an xbind
restart: the queue is in memory, so a backend re-reads its state when it
starts; a request's expiry is re-armed at boot from the stored requests
(one that fell due while xbind was down is told at once), and reading a
request always answers its true state.

## The switches

```
GET    /delegate/state          the person, their agent's page, or their
                                 agent. {user, workspace, paused,
                                 adminChanges, canAdminChanges,
                                 pendingRequests, standingApprovals,
                                 openChangeSets, workspaceAt?, pausedAt?,
                                 access?, agent?, held?, heldWhy?, who?}:
                                 workspace is the workspace's
                                 switch (workspaceAt: when it was last
                                 set — absent: never); canAdminChanges
                                 whether the person is a workspace or org
                                 admin; pausedAt while paused. Where this
                                 xbind ships an agent: access, whether
                                 the person was given one (nobody is
                                 until an access entry names the
                                 component — an admin too: the role
                                 reads the component as they manage it
                                 and is no agent of their own); agent,
                                 the component whose page is their
                                 agent's conversation — only with
                                 access; held, why it can't run now
                                 ("older": its data is a newer xbin's;
                                 "locked": the vault is locked;
                                 "unavailable", with xbind's reason in
                                 heldWhy for an admin — who reads held
                                 without an agent of their own); who,
                                 for a
                                 workspace admin: {everyone, entries,
                                 chosen} — whether the workspace
                                 defaults name the agent, how many
                                 people and orgs have an entry of their
                                 own, and whether anyone was given one.
                                 web, mcp, mcpReadOnly: the person's own
                                 settings (§Web and MCP); net: {web:
                                 {policy, why?, only?}, mcp: {policy,
                                 why?}, search} — what the workspace
                                 policy lets them turn on, and why not
PUT    /delegate/state          the person, or their agent's page —
                                 which may send {paused: true} and nothing
                                 else (403). {paused?, adminChanges?, web?,
                                 mcp?, mcpReadOnly?} → the state. web, mcp
                                 (my agent may read the web, use MCP tools)
                                 and mcpReadOnly (tools marked read-only
                                 don't ask me) are the person's own
                                 session's, and turn on only while the
                                 workspace policy allows them (409
                                 {refusal: "policy"}). Pausing refuses the agent's every
                                 call at once and cuts the calls running
                                 (a followed event stream ends). Resuming,
                                 and adminChanges — the agent may read what
                                 change sets cover and propose them — are
                                 the person's own session's; only a
                                 workspace or org admin turns adminChanges
                                 on (403)
GET    /delegate/workspace      admin. {enabled, by?, at?, agent?, who?,
                                 web, mcp, policy, host, locks}: off until
                                 an admin turns it on; who as in the state;
                                 web {mode, allow, deny, search} and mcp
                                 {mode} the admin's own settings, policy
                                 what is in force under the host's file
                                 ({web: {mode, allow, deny, hostAllow?,
                                 hostDeny?, search, byHost?}, mcp: {mode,
                                 byHost?}}), host {active, lockAll,
                                 error?, web?, mcp?} and locks what of it
                                 holds (§Web and MCP)
PUT    /delegate/workspace      admin. {enabled?, who?, web?: {mode?:
                                 "off" | "read", allow?, deny?, search?},
                                 mcp?: {mode?: "off" | "tiles"}}, at least
                                 one. web and mcp are off by default;
                                 web.mode "full", web.search true and
                                 mcp.mode "listed" are reserved: 400 "not
                                 available yet". allow
                                 and deny are host patterns (example.com:
                                 it and the names under it; *.example.com:
                                 only the names under it), at most 200
                                 each.
                                 Off refuses every person's agent at
                                 once. who gives the agent to people —
                                 access to the component, written where
                                 access is kept: "everyone" (an entry in
                                 the workspace defaults), "me" (the
                                 admin's own entry, and the defaults'
                                 goes), "chosen" (the defaults' entry
                                 goes; entries of people's and orgs' own
                                 stay). One person or org at a time is
                                 an ordinary access entry that names the
                                 component, at read. 409 where this
                                 xbind ships no agent or has no accounts
POST   /delegate/page/dismiss   the person, or the shell they drive. Ends
                                 the alert that names /xbin/agent for
                                 them (§The chrome). {ok}
```

**What each switch stops.**

| | new calls | calls in flight | the agent's model turns | approved, unused tickets | messages it queued for tile agents | standing approvals | pending requests and proposed change sets | the person allowing (Allow, Always, a standing approval, Apply) |
|---|---|---|---|---|---|---|---|---|
| the person pauses | refused | cut | refused at the LLM connector | void | taken back | **kept** (the person resumes on purpose) | kept: decided after resuming | refused (`paused`) |
| the workspace's switch off | refused | cut | refused | void | taken back | dropped | **withdrawn** by xbin | refused (`workspace-off`) |
| the person disabled or deleted | refused (no credential) | cut | refused | void | taken back | dropped | **withdrawn** by xbin | — (no session) |
| the agent is held (a newer xbin's data, a locked vault) | refused (`held`) | cut at the next check | refused | void | taken back | **kept** (for when it clears) | kept: decided once it clears | refused (`held`) |
| the person is no longer given an agent | refused (`no-agent`) | cut | refused | void | taken back | dropped | **withdrawn** by xbin | refused (`no-agent`) |
| the person loses a tile | refused for that tile | cut | — | refused at use (the tile is judged again) | — | dropped for that tile | refused at use | — |
| the workspace's web or MCP policy off (the admin's, or the host's file) | web fetches or MCP calls refused (`web-off`, `mcp-off`); the rest goes on | finishes (a fetch: at most 20 s) | — | refused at use (the policy is judged before the ticket) | — | `tool` approvals kept, refused at use | kept; allowing works, the ticket is refused at use | — |
| the person turns the web or MCP tools off | refused (`web-person`, `mcp-person`) | finishes | — | refused at use | — | `tool` approvals kept, refused at use | kept; refused at use | — |
| an org's rule `web: "never"` or `mcp: "org"` | refused for a conversation that holds its data (`web-org`, `mcp-org`), said once; a new session that hasn't read it can | finishes | — | refused at use | — | kept, refused at use there | refused at use there | — |

**While the agent is stopped nothing can be allowed.** Approving a request
(once, hour or always), `POST /delegate/approvals` and applying a change set
answer `409 {error, refusal, docs}` — `refusal` is the switch, the
delegate gate's own word for it (`workspace-off`, `paused`, `held`,
`no-agent`; `no-sandbox` on an xbind that can't run agents at all) —
unless the workspace's switch is on, the person was given an agent, it is
not paused and not held. Denying a request and rejecting a change set
always work. Otherwise a ticket or an "always" given while it was stopped
would be live the moment it ran again: a stop voids only what was approved
before it. A request or set **withdrawn by xbin** carries `withdrawnBy:
"xbin"`; the agent's backend is told of each once, as of any request or set
that changed (§Notices).

**Having an agent is being given one.** The delegate gate, the LLM
connector's holder rule and `access` all ask the same thing: an access
entry that names the component reaches the person — their own, an org's
share, the workspace defaults. The admin role doesn't count: an admin who
wasn't given one reads the component (its system page, `held`, `who`) and
gets `403 {refusal: "no-agent"}` for a delegate call made in their name.
`who: "me"` is the entry that gives an admin theirs.

**A deleted account takes its records with it.** Requests and tickets,
standing approvals, agent sessions, change sets, the pause and the admin
opt-in, the audit log: all are kept by user id, and an account made later
under the same id inherits none of them. When a person is deleted their
directory under `data/delegation/` moves to `data/delegation/deleted/`,
where no id reaches it; it stays there for the audit log's retention (13
months) and is then removed. The directory also records the creation
stamp of the account it belongs to, so a deletion xbind didn't see (the
users file edited while it was down) is caught the next time people
change, and at boot.

Resuming, or switching back on, brings nothing back: a ticket that was
void stays void, a withdrawn request or change set stays withdrawn (the
agent asks again), and a dropped approval is given again by the person.
Tile agents the agent started keep running through all of these — they are
the person's sessions; only what the workspace agent had queued for them
is taken back.

The workspace's switch is kept with the rest of delegation's state
(`data/delegation/workspace.json`), not in `/workspace-settings`: the admin
console and `bx settings` list that store's entries by name, so a new one
would show in neither without its own UI. Set it with this route.

## Approval requests and tickets

A side effect that needs approval passes the gate only when **the person**
approved it, and xbind keeps the approval — a credential taken from the
agent's sandbox can ask and can read, and nothing more.

1. The agent posts what it wants to do, exactly as it will send it.
2. The person sees xbind's description beside the agent's words, and
   approves or denies it (a `delegate` event, a push of kind
   `agent.approval`).
3. Approved, the request **is a ticket**: for 15 minutes, once, the agent
   makes that call with `X-XBin-Ticket: <id>`. Another method, path, query,
   body or `Content-Type` (the whole of it: type and parameters) — or a
   path that now resolves to another tile — is `403 {refusal: "ticket"}`
   and spends nothing.

```
POST   /delegate/requests       the person's agent only. {method, path,
                                 body? | bodyBase64?, contentType?,
                                 summary?} → the request, pending. path is
                                 the call's path under /api/ with its query
                                 (at most 2048 bytes, no _method); the body
                                 at most 256 KiB; contentType the
                                 Content-Type the call will carry
                                 (application/json when there is a body and
                                 none is said). Only a call that needs
                                 approval (400 otherwise): a non-GET call
                                 to a tile's API, POST /term/sessions,
                                 POST /templates/new. 403 when the tile or
                                 its org refuses the call anyway; 409 past
                                 20 pending
GET    /delegate/requests       the person, or their agent's page.
                                 {requests}, oldest first; ?all=1 adds the
                                 ones decided in the last day
GET    /delegate/requests/<id>  the person, their agent's page, or their
                                 agent (it polls: state approved = a
                                 ticket)
POST   /delegate/requests/<id>/approve
                                 the person, in their own session, app or
                                 device — never the agent's page (403).
                                 {grant?: "once" | "hour" | "always",
                                 scope?: "call" | "tile", unseen?: true}
                                 → {request, approval?}. A request whose
                                 body can't be shown (bodyBinary) is
                                 approved only with unseen: true — 409
                                 {unseen, bodySize, bodySha256} without. once is the default; hour and
                                 always (a tile API call only) also record
                                 a standing approval — of this call
                                 (scope "call", the default) or of every
                                 write to the tile (scope "tile"). 409 when
                                 no longer pending; 409 {refusal} while
                                 the agent is stopped (§The switches)
POST   /delegate/requests/<id>/deny
                                 the person. → {request}
DELETE /delegate/requests/<id>  the person, or their agent. withdraws a
                                 pending or approved request
```

A request:

```
{id, user, agent, kind: "call" | "session" | "template",
 method, target, tile?, contentType?, body?, bodyShown, bodySize, bodySha256,
 summary?, preview: {class, tile?, owner?, level?, role?, standing?, session?},
 state: "pending" | "approved" | "denied" | "used" | "expired" | "withdrawn",
 grant?, created, decided?, expires, decidedVia?, withdrawnBy?}
```

`body` is shown while the request is pending or approved, when it is text.
A body that isn't — not UTF-8, or with control characters — sets
`bodyBinary`, and `preview.body` is `{binary: true, size, sha256}`: show
that, and that approving it approves bytes nobody read (`approvedUnseen` is
recorded on the request);
`summary` is the agent's own text — show it as the agent's. For a tile
call `preview.standing` is `{call, tile}`: xbind's sentence for what each
standing scope would allow from then on — show the one the person is about
to choose. For a session start `preview.session` is `{tile, provider,
providerName, mode, asks, options, net, api, vm, gpu, model, name,
resume}`: the harness and mode the session **will** run in — a resumed
session's come from its history when the request names none — and every
option the start carries; `asks: false` means the mode lets the tile agent
act without asking — say so on the card. A pending request expires after 24
hours; decided and withdrawn ones are kept a day, 200 a person at most.

**What a start leaves out, xbind fills.** A start may name only its tile
(`{cwd, kind: "agent"}`): the harness is the person's default for agent-
started sessions among those the tile's orgs allow — the one their
preference lets start unasked; without one, the first allowed that can run
as things are: on the person's own sign-in where a session saw one, or on
the workspace's LLM connector (`noSignIn`), so a start doesn't open on a
coding agent the connector can't serve and nobody is signed in to. The
mode and network are their preference's (or the harness's asking mode), and the request's
`preview.session` shows the **effective** `provider`, `mode`, `net` — what
`GET /delegate/start-defaults?tile=` answers beforehand. A value the rules
don't allow is refused with the rule's reason.

**The first prompt is part of the start.** A start may carry `prompt` (at
most 64 KiB): what the session is told first. The ticket binds it with the
rest of the body, the card shows it (`preview.session.prompt`), and xbind
itself hands it to the session once it is up — a session that can't take
it is ended. **Nothing the agent sends the session afterwards is approved
one by one**: `…/prompt` and `…/queue` on a session it started are free
acts. What stands between a later message and a change is the session's
own mode — a mode that asks its person (`asks: true`) asks them, whatever
the workspace agent told it.

**Starting a tile agent.** With a ticket the session opens as the person
approved it — the harness and mode on the card, which no option of the
start can change (an option that would set the mode is refused). Without
one it opens only when the person's agent preferences allow starts without
asking, and then the mode, network and API access are the preference's:
sending `mode`, `net` or `api` is `403`. Either way: the tile's default
deployment, a harness the tile's org allows, never a tile that is itself a
workspace agent. The start is strict: a mode the harness won't take ends
the session (it is never run in the harness's own mode instead), and of
its options only a model or a thinking level is set — any other ends it.
A preference applies to the harness it was set for, in a mode that asks.
Also: and the session's row carries `origin: "agent"`. An agent
resumes only past sessions it started.

## Standing approvals

```
GET    /delegate/approvals      the person, their agent's page, or their
                                 agent. {approvals: [{id, user, tile,
                                 scope, method?, path?, until?, created,
                                 via?, request?}]}
POST   /delegate/approvals      the person. {tile, grant: "hour" |
                                 "always", scope: "call", method, path} or
                                 {tile, grant, scope: "tile"} → the
                                 approval. scope is required (400). 404
                                 for a tile the person can't read; 409
                                 {refusal} while the agent is stopped
DELETE /delegate/approvals/<id> the person. 204; a write running under it
                                 is cut
```

A standing approval has a **scope**:

- `call` — one call, again: that `method` on that `path` of the tile's API
  (`/api/<tile>/…`, no query), with any body and query. What "always" means
  unless the person chose wider.
- `tile` — every write to the tile's API: any method, any path. A separate,
  explicit choice; say what it is before the person makes it
  (`preview.standing.tile`).

An approval goes when its tile is deleted or the person can no longer read
it, and does not come back with the access. A session start and a tile
create are approved each time.

## Change sets

An agent never calls the admin API. For an admin who turned `adminChanges`
on, it **proposes** a batch; the person approves all, some or none; xbind
applies the approved operations in order, as that person, through the same
handlers as a click (the same checks, restarts, events and audit).

```
POST   /delegate/changesets     the person's agent only. {title, items:
                                 [{kind, args, why?}]} → the change set,
                                 proposed. Nothing is applied. 403 without
                                 the opt-in; 400 for an unknown kind or
                                 arguments; at most 20 items; 409 past 10
                                 open sets
GET    /delegate/changesets     the person, their agent's page, or their
                                 agent. {changesets}, oldest first
GET    /delegate/changesets/<id>
                                 the person, their agent's page, or their
                                 agent
DELETE /delegate/changesets/<id>
                                 the person, or their agent. withdraws a
                                 proposal
POST   /delegate/changesets/<id>/approve
                                 the person whose agent proposed it, in
                                 their own session, app or device. {items?:
                                 [n, …], password?} → the set with each
                                 item's result. items is required and
                                 not empty: no body, null and [] apply
                                 nothing (400; the set stays proposed).
                                 Needs a sign-in under 10
                                 minutes old, or the password in the body:
                                 403 {error, stepUp: "password" | "signin"}
                                 otherwise, as for adding a device. 409
                                 {refusal} while the agent is stopped
                                 (§The switches); nothing is applied
POST   /delegate/changesets/<id>/reject
                                 the person. → the set
POST   /delegate/changesets/<id>/undo
                                 the person. → a new change set that
                                 undoes the applied items of this one that
                                 can be undone, to approve like any other.
                                 409 when none can
```

Kinds — a closed list; there is no kind that names a route:

| `kind` | `args` | Applies as |
|---|---|---|
| `grant.add`, `grant.revoke` | `{from, target, role}` | `POST` / `DELETE /grants` |
| `binding.set` | `{component, slot, provider \| providers, host?, zone?, listen?, add?}` | `POST /bindings` |
| `binding.remove` | `{component, slot, provider?, host?, zone?, listen?}` | `DELETE /bindings` |
| `org.share` | `{org, tile, level}` (`""` stops sharing) | `PUT /access` |
| `limits.set` | `{tile, kind, value}` — a raise | `POST /limits` |

What a kind can't express — refused as no change at all (`400` at the
proposal, and again if it became true by the time the set is applied):

- an operation **on a workspace agent**: a grant from it or on it (its
  code too), a binding of it or to it, a share of it, its limits;
- a grant or revoke whose target is `xbin`, `xbin:*`, `cap:*`, `gpu:*`,
  the bare `code`, `net:*` or a pattern — those are given and taken away on
  the Grants page;
- a binding of an `@` pseudo-slot (`@archive`) or of the workspace default
  (`*`), a binding to the host's network (`host`, or a network set that
  says host), and `provider` together with `providers`;
- a share of a tile that itself holds `xbin`, `cap:*`, `gpu:*` or `code`.

A change set:

```
{id, user, agent?, title, undoOf?,
 state: "proposed" | "applying" | "applied" | "partial" | "rejected" | "expired" | "withdrawn",
 created, decided?, expires, withdrawnBy?,
 items: [{n, kind, args, why?, line, before?, after?, effects?, warnings?,
          blocked?, inverse?, result?, error?}]}
```

- `line`, `before`, `after`, `effects` and `warnings` are **xbind's**: the
  sentence is written from the kind and its arguments, the state read by
  running the handler's own checks without writing. `why` is the agent's
  text. A binding's `line` states its `host`, `zone` and `listen`, and its
  `warnings` say what the routes it adds open: `PUBLIC: …` for a hostname
  or zone (with the endpoint's paths), `OPEN PORT: …` for a listen
  address, `EGRESS: …` for what a network slot will reach. Show warnings
  as prominently as the line.
- `blocked` is the handler's refusal: that item can't be applied.
- At approve, each chosen item is checked again. `result`: `applied`;
  `failed` (`error`: the handler's words); `stale` — the state, or
  xbind's line, effects or warnings for it, changed since the proposal:
  nothing applied; `skipped` — an earlier item failed;
  `rejected` — the person left it out; `blocked`.
- The set is not atomic: what was applied stays. `applied` when every
  chosen item applied, else `partial`.
- A proposal expires after 7 days. A person keeps at most 10 waiting and
  50 decided sets (the oldest go), a decided one for 30 days; an operation
  carries at most 4 KiB of arguments and a set 256 KiB (`413`).
- Every name an operation carries (`from`, `target`, `role`, `component`,
  `slot`, `provider`, `host`, `zone`, `listen`, `org`, `tile`, `level`,
  `kind`) is at most 200 bytes of letters, digits and `._~:@#/*+=,[]-`
  (`400` otherwise): they are written into xbind's line.

## A tile's agent level

A tile's `"agent"` (`off`, `read`, `full`) is a line of its own manifest,
which its terminals and tile agents can write. So the manifest alone never
**raises** it: xbind keeps the level each tile last had approved
(`data/agent-levels.json`).

- A tile that never stated a level is `full`. Its first explicit value is
  taken as it is.
- A manifest that states **less** lowers the level at once.
- A manifest that states **more** — or drops the key after stating less —
  changes nothing. The level on record stays in force, the tile's managers
  get an alert (`GET /alerts`, kind `agent-level`), `GET /components` rows
  carry `agentRequested`, and one of them approves it here.
- The record outlives the tile: one deleted and made again at the path
  starts where the old one was.
- A workspace's consoles (`tiles/admin`, `tiles/organisations`,
  `tiles/manager`) are `off` whatever their manifest says.

```
GET    /agent-levels            a person, in their own session, app or
                                 device. {tiles: [{tile, level,
                                 requested?, stated?, builtin?, by?, at?}]}
                                 for the tiles they may manage that have a
                                 level on record or a raise waiting. level
                                 is the one in force; requested what the
                                 manifest asks for when that is more
PUT    /agent-levels            the tile's owner, an admin of the owning
                                 org, or a workspace admin — in their own
                                 session, never a tile's credential, an
                                 agent or view-as (403). {tile, level} →
                                 the row. level is the level approved and
                                 must be what the manifest asks for now
                                 (409); 409 for a console
```

## The audit log

```
GET    /delegate/audit          the person, in their own session, app or
                                 device; an admin with ?user=<id>. {user,
                                 calls, skipped?}, newest first; ?before=<RFC 3339>,
                                 ?limit=<n> (default 200, at most 1000)
```

A row: `{ts, user, agent, method, path, tile?, class, effect?, dryRun?,
status, refused?, ticket?, under?, n?}`. Written for every side effect
that ran, every approval decision (`class: "decision:…"`) and every
applied change-set item (`class: "changeset:<kind>"`) — always — and for
tile reads, refusals, dry runs and reads of code, logs and admin state up
to 60 in a burst and one a second after that, per person: the next row's
`n` says how many were left out before it. Not for the listing reads a UI
makes. Never a body, a query or a header; a path is cut at 512 bytes.
Every field is bounded when written (a name 256 bytes, a row 4 KiB), and a
line of the log that is no row is skipped and counted (`skipped`), never an
error. Thirteen months are kept. The agent, its
page, tiles and view-as sessions can't read it.

## Events and pushes

`/ws/events` carries `{type: "delegate", data: {op, id?, user}}` to the
person's own sockets and their agent's page — `op`: `request` (one
appeared, was decided, withdrawn or used), `approval`, `changeset`,
`state` (the person paused or resumed, changed the admin opt-in or who gets
an agent — and the workspace's switch going on or off, told to every
person who has an agent and to the admin who turned it). Refetch the list
it names. Pushes: `agent.approval` (a request
waits — one notification a person: the same collapse key, a count, at most
one a minute) and `agent.changeset` (a proposal waits). Their link is the path of
the sealed component that is the person's agent (the `agent` of the request
or change set); their text is xbind's, never the agent's.

## The chrome

Everything a person decides about their agent is drawn and sent by xbin's
own chrome, in a document that holds the person's session: the **dock**
(`/vendor/bx-agent-dock.js`, which a shell puts beside its canvas) and
**`/xbin/agent`**, xbind's own page that shows the same dock full width
([core.md](/docs/protocol/core.md)). Below the chrome's header sits the
agent's own page — the `agent` of `GET /delegate/state` — in a sandboxed
frame on its tile credential; a holder is never trusted chrome. The page
can't draw over the chrome, read it, or send a decision: every route that
decides refuses its credential.

**Only the chrome frames the agent's page**, in every asset mode. In
`origins` mode the page has an origin of its own and its
`frame-ancestors` names itself and the workspace, where no tile document
runs. In `legacy` and `tokens` every tile's document is served from the
workspace origin, and `frame-ancestors` matches a framing document by its
URL — so there xbind looks at who started the load: a framed load of
`/c/<agent>/…` (`Sec-Fetch-Dest: iframe | frame | embed | object`) that
isn't `Sec-Fetch-Site: same-origin` is `403` — a tile's sandboxed document
has an opaque origin, so its frames arrive cross-site, as another site's
do — and the page carries `frame-ancestors 'self'`. Opened as a page of
its own, or by a client that sends no Fetch Metadata (the native app, bx),
it is served as before. The agent's page therefore can't frame documents
of its own in those two modes.

**What the agent's page may ask of the chrome** — `postMessage` to its
parent; the chrome takes a message only from its own frame's window and
reads the fields named here, as ids, and no other. An id is the shape
xbind issues or the message is ignored: a request's or change set's is 24
lowercase hex digits, a session's letters, digits, `_` and `-` (never a
dot or a slash) — they go into URLs the chrome sends with the person's
session:

```
xbin:agent-open-request   {id}             open the approval of one request
xbin:agent-open-changeset {id}             open the review of one change set
xbin:open-session         {session, tile}  show one of the person's sessions
                                           in its tile's window
xbin:open-session         {session: "new", a new session of the tile's own
                           tile, draft}    agent: its window opens on a new
                                           session's launcher, draft (text
                                           the page suggests, at most 8000
                                           characters) in the composer of the
                                           one they pick — never sent by the
                                           message
xbin:agent-open           {view}           needs | approvals | activity — a
                                           view that changes nothing; never
                                           Preferences
```

A new session's `draft` is read as plain text — line ends made `\n`,
control and invisible (bidi) characters dropped, capped — and never
interpreted: it is shown, and waits in the composer until the person
presses Send. It comes from the agent's page, so it is untrusted: the
chrome labels it as text the agent's page suggests, for the person to read
and edit — never as their own words. The chrome hands it to the shell
(`bx-agent-new-session`); where no shell takes it (an older scaffold,
`/xbin/agent`) the dock shows a plain link to the tile and the suggested
text to copy.

The chrome reads the request or change set from xbind itself
(`GET /delegate/requests/<id>`, `/delegate/changesets/<id>`) and draws
xbind's reading: the call, its class, the body's text or its size and
hash, the warnings. A body or a brief longer than 4000 characters shows
its first 4000 with "first 4000 of N characters" and Show all; "shown in
full" is said only of one shown whole. A request or change set in a state the chrome doesn't
know (a newer xbind's) is never taken for one that can be decided: no
Allow, no Apply, and a line that says the chrome is older than the
workspace. The agent's own sentence (`summary`, `why`) is shown
quoted and labelled as the agent's. Nothing the agent wrote — a body, a
brief, a summary, a set's title, a reason — is drawn with the characters
that reorder or hide text: control characters but newline and tab, and
every format character (bidi embeddings, overrides and isolates, the
zero-width ones, the word joiner, the byte order mark) show as their
escape, `⟨U+202E⟩`, never interpreted, under a line that says the text
holds hidden characters. An id that isn't the person's opens
nothing. No message approves, denies, resumes or changes a preference, and
none opens Preferences: they open from the chrome's own menu only.

**While the agent is stopped** — workspace agents off, paused, held, not
given to the person (`GET /delegate/state`) — a sheet says why in the place
of Allow or Apply, and Deny or Reject stays. A sheet opened before the stop
that sends an Allow gets the `409 {refusal}` and shows the same words; it
never sends the Allow again.

**What widens the agent's reach is armed like Allow.** A page that baits
a double-click, or scripts one, must not land it on a control that widens
what the agent may do: Allow, the approval's wider standing scope, and in
Preferences "start without asking", a wider network, other tiles' APIs and
the admin opt-in each answer only the person's own click (`isTrusted`),
not for 600 ms after the control appears, only while nothing covers it and
the window has their attention — and never Enter. Until then it carries
`aria-disabled="true"`. Narrowing (back to asking, no internet, the
opt-in off) is never held up.

**What the chrome tells the page**: `{type: "xbin:agent-context", tile}`
or `{…, screen}` when the dock was opened about a tile or a screen — once
the frame has loaded, and when it changes. A shell opens it about a tile
from the chip (the window in front) and from a tile's menu, **Ask my agent
about this tile** — shown where the chip is, and not before setup or while
workspace agents are off.

**Preferences** set the mode a tile agent the person's agent starts gets
(`agentSessions.modes` of `/agent/prefs`, [agents.md](/docs/protocol/agents.md)):
the chrome offers, per coding agent of `GET /agent/providers`, only the
modes that ask before acting, and names the ones that don't with why —
xbind refuses those (400) whatever sends them.

**A shell without the agent's chip** — a scaffold from before it — learns
of the page through one alert: once an admin has turned workspace agents
on and this xbind has an agent to show, `GET /alerts` carries kind
`agent-page` for each person (their own session, and the shell they
drive), its message naming `/xbin/agent`, its `dismiss`
`/delegate/page/dismiss` — a POST ends it for that person, for good. A
shell that has the chip leaves alerts of this kind out.

**Developing an agent.** On an xbind started with `--dev` or `--no-auth`,
approving a change set needs no recent sign-in. Under `--no-auth`, where
nobody signs in, the implicit owner answers as the person on every route of
this page — for the person `?user=<id>` names (default `owner`) — and
approved changes are applied as the owner. Neither applies to any other
xbind.

Bus events and partition mail delivered **into** the sealed component are
other tiles' data: the agent treats them as untrusted input, like any tile
data it reads.
