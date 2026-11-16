# The workspace agent on the web and with MCP servers

> Status: live

Owner's decision (2026-11-16): "Web access/MCP — let's make this
configurable in agent settings + have workspace policy." The decision and
its reasons are D239 in `plans/DECISIONS.md` (the outside servers of
§6.1, built after it: D240); the contract, once built, is
`docs/workspace-agent.md` and `docs/protocol/delegation.md` (§Web and MCP).
This file is the spec the implementation follows, with every route, field
and setting and its class in the delegation cap
(`internal/apicheck/testdata/delegate-classes.golden`).

## 1. What a person gets

Two capabilities for a person's workspace agent, both **off by default**:

- **web** — fetch a URL and read it (GET only); a web search where the LLM
  connector's provider has a native search tool and an admin switched it
  on;
- **mcp** — use MCP servers: the MCP tiles of this workspace the person can
  already reach and, with `listed`, the **outside servers** an admin lists
  by URL, xbind holding their credentials (§6.1; D240).

Three layers decide, each only narrowing the one above:

1. **the workspace policy** — set by a workspace admin beside the other
   workspace-agent settings (`PUT /delegate/workspace`, additive fields),
   preset and capped by the host's deployment file
   (`XBIN_LLM_CONFIG`'s `workspaceAgent` key: `web`, `mcp`);
2. **the org rules** (`Org.Agents`, internal/agentpolicy) — an org may say
   its data never goes to the web, or to MCP servers outside the org; a
   conversation that holds such an org's data loses the tool for the rest
   of the conversation;
3. **the person's settings** — two toggles in Preferences (web and iOS),
   within what the policy allows, with why when it doesn't.

xbind enforces all three. The agent's sandbox keeps its "model only"
network: nothing the backend does reaches the web except through xbind.

## 2. The workspace policy

Kept with the workspace's switch (`data/delegation/workspace.json`,
additive fields; an older file reads as everything off).

| Field (`PUT /delegate/workspace`) | Values | Default |
|---|---|---|
| `web.mode` | `off` · `read` (GET only, no body, no credentials) · `full` (reserved: refused `400 "not available yet"`) | `off` |
| `web.allow` | domain patterns (`example.com` = it and its subdomains; at most 200, each ≤ 253 bytes); empty = every public host | `[]` |
| `web.deny` | the same; a deny always wins | `[]` |
| `web.search` | `true`: offer the connector's native search where an upstream has it on (§4.5) | `false` |
| `mcp.mode` | `off` · `tiles` (MCP tiles the person can reach) · `listed` (those, and the outside servers of `mcp.servers`, §6.1) | `off` |
| `mcp.servers` | the outside servers (§6.1): the whole list, replaced; at most 50; secrets write-only | `[]` |

`GET /delegate/workspace` (admin) answers, beside what it did:
`web: {mode, allow, deny, search}` and `mcp: {mode, servers}` — the admin's
own settings, each server without its secret (`hasSecret`) — plus
`policy: {web: {mode, allow, deny, search}, mcp: {mode, servers,
serversByHost?}}`, what is in force once the host's file applied, and
`host: {active, lockAll, error?, web?, mcp?}` with `locks: {"web.mode":
{locked: true, reason: "set by your host", ceiling?}, "mcp.servers"?:
{locked: true, reason}}`.

### 2.1 The host's file

One host file, not two: the `workspaceAgent` key of `XBIN_LLM_CONFIG`
(the first-run work's, D237, `internal/llm/hostagent.go`) carries `web`
and `mcp` beside `enabled` and `who` — one parser, one reload path, one
`locks` map, one "Set by your host":

```jsonc
"workspaceAgent": { "web": { "mode": "read", "allow": ["docs.example.com"], "deny": ["intranet.example.com"] },
                    "mcp": { "mode": "listed", "servers": [
                      { "id": "tracker", "name": "Issue tracker", "url": "https://mcp.tracker.example/mcp",
                        "auth": "bearer", "secretFile": "tracker.token" } ] } }
```

Validated as an admin's settings are (a reserved mode is refused, so the
boot stops on it). Without `enabled` the switch stays the admins'.

- `web.mode`, `mcp.mode`, `web.search` are a **preset and a ceiling** —
  unlike `enabled` and `who`, which lock outright: the value in force is
  the admin's when they set one, never past the host's; without one of
  the admin's, the host's. The admin can narrow (turn it off); a write
  past the ceiling is `409 {settings}` naming it. The card says "Set by
  your host: at most …".
- `web.allow` holds always: a URL must match the host's list (when it has
  one) and the admin's (when they have one). `web.deny`: the host's and the
  admin's both hold. The host's lists lock nothing.
- `mcp.servers` — the outside servers — **lock outright** when the file
  names them (`[]` included): the list in force is the host's alone, the
  admin's is kept but not in force, and a `PUT` of `mcp.servers` is `409
  {settings: ["mcp.servers"]}`. A host's server names its secret by
  reference, the way an upstream names its key: `secretFile` (one line,
  relative to the file; its stamp watched like a key file's, so a rotated
  secret is read again) or `secretEnv` (an environment variable of xbind's,
  read when the file loads). A secret that can't be read stops the boot as
  a missing key file does. A list is a list of addresses that leave the
  workspace: a host that names it means it to be exactly that.

`locks` (`GET /delegate/workspace`) names the held settings by their `PUT`
names beside the switch's and who's (`internal/delegation/hostfile.go`:
`Locks`, `Refuses`; `delegation.Store.SetHost` takes the key as the file
reads now).

Rejected: a lock that also forbids narrowing (the switch's kind):
turning a capability off is never a risk a host must prevent. Rejected
too: a file of its own (`XBIN_WSAGENT_CONFIG`, this change's first
version) — two host files would have meant two parsers and two "Set by
your host"s.

## 3. The person's settings

In delegation's per-person state (`state.json`, additive):

| Field (`PUT /delegate/state`) | Meaning | Who |
|---|---|---|
| `web` | my agent may read the web | the person's own session; widening — armed like Allow in the chrome; `409 {refusal: "policy"}` while the workspace policy has web off |
| `mcp` | my agent may use MCP tools | the same |
| `mcpReadOnly` | MCP tools marked read-only (`readOnlyHint`) don't ask me — on an outside server only where the admin trusts its marks (§6.1) | the same |

The agent's page may still only pause (403 for these). `GET /delegate/state`
adds `web`, `mcp`, `mcpReadOnly` and `net: {web: {policy, why?}, mcp:
{policy, why?, servers?: [{server, name, host, trustReadOnly}]}, search}`
— what the policy allows (and the outside servers the person may use) and, when it doesn't,
why in the chrome's words ("off for this workspace — an admin can change
it").

## 4. Web: `POST /delegate/web/fetch`

| | |
|---|---|
| Route | `POST /api/xbin/delegate/web/fetch {url}` |
| Class | `act` (golden: `POST /delegate/web/fetch act`); not statically ticketed — the handler decides (§4.2) |
| Caller | the person's agent only (a delegate under a live handle; every other credential `403`) |
| Answer | `{url, finalUrl, status, contentType, title?, text, bytes, truncated, redirects, readWeb: true}` |

### 4.1 What is checked, in order

1. the delegate gate: credential, off switches (paused / off / held /
   no-agent / no-sandbox), the handle, the act rate (30 a minute);
2. the workspace policy: `web.mode` is `read` (else `403 {refusal:
   "web-off"}` with who can change it);
3. the person's `web` setting (else `403 {refusal: "web-person"}`);
4. the org rules of every org in the handle's `tainted` (§5): one whose
   rule is `never` → `403 {refusal: "web-org", org, reason}`;
5. the URL: `http`/`https`, ≤ 2 KiB, no user info, a host (no bare IP
   literal in a form other than the canonical dotted quad or a bracketed
   IPv6 — `http://2130706433/`, `http://0x7f.1/`, `http://0177.0.0.1/` are
   refused), the host lists (host's and admin's allow and deny), not the
   workspace's own hosts (its external URL, the tiles domain and its
   subdomains);
6. a ticket when the conversation holds data (§4.2);
7. the per-person web limit: 12 fetches a burst, one more every 5 s
   (`429 {refusal: "rate-limited"}` + `Retry-After`).

### 4.2 A URL is a way out

When the handle's `tainted` names ANY org — "workspace" (the tiles in no
org) included — a fetch needs the person's approval of that exact URL,
unless every org in `tainted` allows the web outright (`web: "allow"`, §5).
The agent files `POST /delegate/requests {method: "POST", path:
"/api/xbin/delegate/web/fetch", body: "{\"url\": …}"}`; the request has
`kind: "web"` and `preview: {class: "web", url, host, orgs, readWeb}`; the
person approves it once (no standing approval of a URL: `hour` and
`always` are refused); the agent sends the same body with `X-XBin-Ticket`.
Without one: `403 {refusal: "needs-approval"}`. An untainted conversation
fetches freely within the policy. Filing a web request for a call that
needs none is `400`, as for any call.

### 4.3 The fetch

- dialled to an address checked **when it connects**: every address the
  name resolves to must be public — not loopback, private (RFC 1918, ULA),
  link-local, multicast, unspecified, CGNAT, benchmarking, reserved, NAT64,
  IPv4-mapped forms of those — and the connection is made to the checked
  address (a DNS answer that changes between check and connect reaches
  nothing; the dialer's Control checks the socket's own peer address);
- redirects followed by xbind, at most 5, each hop judged again (scheme,
  length, lists, own hosts, address); a `POST`-changing redirect is
  followed as `GET` with no body;
- `GET` only, no cookies, no `Authorization`, no `Referer`, a fixed
  `User-Agent: xbin-workspace-agent/1 (+https://…/docs/workspace-agent.md)`,
  `Accept` of the allowed types;
- at most 2 MiB read, 20 s in all; content types `text/html`,
  `application/xhtml+xml`, `text/plain`, `text/markdown`, `text/csv`,
  `application/json`, `application/xml`, `text/xml`, `application/rss+xml`,
  `application/atom+xml` — anything else is `415 {refusal: "web-type"}`
  with the type;
- HTML is turned into text (scripts, styles, `noscript` and comments
  dropped, block elements as lines, the title kept); text is cut at
  64 KiB (`truncated`);
- every fetch is a row of the person's audit log (`class: "web"`, the
  URL as `path` — host and path, no query, cut at 512 bytes — `status`,
  `n` bytes in `effect`), refused ones too.

### 4.4 After a fetch

The handle gains `web: true` (additive on the session's answer): "this
conversation has read the web". The approval sheet of every later request
filed under that handle says so (`preview.readWeb`). The backend keeps it
with the conversation and gives it to a handle it opens again
(`POST /delegate/sessions/{id}/taint {orgs?, web: true}`).

Fetched text is untrusted input: the backend frames it as it frames a
tile's answer (`[untrusted — from <host>: …]`), defuses markers inside it,
and says once per conversation that a page's words are content, never
instructions.

### 4.5 Search — not built yet

`web.search: true` is refused (`400 "not available yet"`) and the reach's
`search` is always `off`: the connector half (the upstream flag, the
holder rule, the backend's hosted tool on the Responses wire and its
answers) is a piece of its own. What it will be: only where an upstream of
the LLM connector has a native web search and an admin switched it on: `webSearch: true` on the upstream (additive,
admin-set; the host file may set it too) and `web.search` in the policy.
The backend then offers the provider's hosted search tool on the session's
model calls; the connector refuses a holder's call that carries one
unless the session's reach says search is on (`403 xbin_llm_policy`).
Otherwise there is no search tool, and the agent is told to fetch the URLs
it knows. No third-party search dependency.

### 4.6 The session's reach

`GET /delegate/sessions/{id}` (and every answer that carries a session)
adds `web` (the flag above) and `reach: {web: {state, why?}, mcp: {state,
why?}, search: {state, why?}}`, `state` one of `on` (it may, freely),
`asks` (each fetch needs the person's approval: the conversation holds
data), `off` (with why: the policy, the person, an org's rule). The
backend builds its tool list and its prompt's line from it; the page and
the native view show it in the header's connection line.

## 5. Org rules

`agentpolicy.Rules` gains two rules (additive, omitted when unset; an org
narrows the workspace's bounds and a pin, as for the others):

| Rule | Values | Meaning for a conversation that holds the org's data |
|---|---|---|
| `web` | `ask` (unset) · `allow` · `never` | ask: each fetch needs the person's approval of its URL; allow: fetches freely; never: no fetch and no search for the rest of the conversation |
| `mcp` | `any` (unset) · `listed` · `org` · `never` | any: every MCP tile; an outside server only with the person's approval of each call — what is sent leaves the workspace (§6.1). listed: also the listed outside servers, under their ordinary approvals (the org trusts the admin's list). org: only MCP servers under this org — its MCP tiles (owned by it or shared into it) and the outside servers listed with it (`orgs`), those under their ordinary approvals. never: no MCP server at all |

Strictest wins across a tile's layers (bounds, pin, own): never > ask >
allow; never > org > any > listed (D240: `listed` is the one value that
loosens the default, as `web: "allow"` does). `PUT /orgs/{org}/agents`, `…/pin` and
`PUT /agents/policy` take them; `bx org agents --web --mcp`; the
`<bx-org-agents>` editor shows them. Refused as the model rule is: with
the reason, said once to the model (the tool leaves the conversation's
list) and once to the person (a refusal card).

## 6. MCP tiles

An MCP tile is one whose manifest provides an `http` interface of service
`mcp`; its endpoint is `/api/<tile>/mcp`. Everything goes **as the
person**, through the tile route a delegate's call takes (the person's
level, the tile's `"agent"` level, the org rules, `X-XBin-Agent`), under
the session's MCP reach. Two ways (revised after the journey plan's G4,
`plans/wsagent/journey.md` §5 — it replaced a `GET /delegate/mcp/servers`
listing this spec first had):

| Route | Class | Answer |
|---|---|---|
| `POST /api/<tile>/mcp` (the tile's own endpoint) | the tile's API, judged by its JSON-RPC body | exactly one request (no batch, ≤ 64 KiB, nothing but `jsonrpc`, `id`, `method`, `params`) of `initialize`, `ping` or `tools/list` is a **read** (`class: "mcp-read"`): no ticket, `Mcp-Session-Id` and `MCP-Protocol-Version` carried. Any other POST there is refused (`403 mcp-call`); a request for one as a tile call is `400`. The backend reads tools this way with the template's own MCP client |
| `POST /delegate/mcp/call {server, tool, arguments}` | `act` | xbind is the client for a tool's call: `{server, tool, isError, content: [{type, text?}], structured?}`; a ticket unless a standing approval of that tool covers it or the person's `mcpReadOnly` and the tool's `readOnlyHint` let it through; a tile at `"agent": "read"` takes read-only tools only. The answer taints with the tile's orgs |

Why the call isn't the raw endpoint under a tile-call ticket: a ticket or a
standing approval of `POST /api/<tile>/mcp` would cover every tool of the
server, and `tile_call` could step around the MCP policy; and the tool's
`readOnlyHint` must be read by xbind, from the server's own `tools/list`.
`GET /delegate/map` marks an MCP tile's row `mcp: true`.

The request is `kind: "mcp"`, `preview: {class: "mcp", server, serverName,
tool, title, description, readOnly, arguments}`. Its standing choice is
"always for this tool": `grant: "always" | "hour"` with `scope: "tool"` —
an approval of exactly that server and tool, any arguments.

### 6.1 Outside servers (`mcp.mode: "listed"`, D240)

An admin lists MCP servers outside the workspace by URL; xbind holds their
credentials and is the client — the agent's sandbox keeps its "model only"
network, and nothing of a server's (its address beyond the host, its
secret, its session) reaches the agent, its page or the model.

**A listed server** (`PUT /delegate/workspace {mcp: {servers: [...]}}`,
the whole list, replaced):

| Field | |
|---|---|
| `id` | `[a-z0-9][a-z0-9-]{0,31}`, unique; the agent names the server `ext:<id>` (a tile is never named so — `server` values starting `ext:` are outside servers) |
| `name` | what people and the model read; ≤ 80 bytes, no control characters |
| `url` | the streamable-HTTP endpoint: `https`, a host that is a name or a canonical address, no user info or fragment, ≤ 2 KiB; `http` only with `plainHttp` ("plain http, I know" — the card warns that the secret and every argument cross the network in the clear); never one of the workspace's own hosts |
| `auth` | `none` · `bearer` (`Authorization: Bearer <secret>`) · `header` (`<header>: <secret>`; `header` a token name, not one xbind sets itself: `Host`, `Content-*`, `Accept*`, `Mcp-*`, `Connection`, `Transfer-Encoding`, `Cookie`, `User-Agent`…) |
| `secret` | write-only: absent keeps the stored one (unless the URL's scheme, host or port changed: then it is given again — a secret never follows its server elsewhere), `""` removes it, a value (one line, ≤ 4 KiB) replaces it. Reads say `hasSecret`. `bearer`/`header` without one is `400`. |
| `note` | the admin's words for people and the model (≤ 500 bytes) |
| `orgs` | `[]` (everyone given an agent) or org ids: only active members of one of them see the server — the others don't see it at all (no listing, `404` on a call, the same answer as for no such server); and the server counts as **inside** those orgs for their `mcp` rule |
| `privateNetwork` | the server's addresses may be private (RFC 1918, ULA, CGNAT), loopback or xbind's own host's — link-local (the metadata address), multicast and unspecified stay refused. Off: the web's address rule. Armed and warned on the card |
| `trustReadOnly` | the person's "read-only tools don't ask me" applies to this server's `readOnlyHint` (off: an outside server's mark is an outsider's claim, and its tools always ask the first time) |

Validated at the write (`400` naming the server and the field); the
`orgs` must exist. Secrets are kept in `data/delegation/mcp-secrets.json`,
sealed under the vault barrier (label `delegation/mcp-secrets`) in the
envelope the LLM connector's upstream keys use (`internal/vault`
`SealedMap`, shared with `internal/llm`): with the vault sealed, a server
with a secret answers `503 {refusal: "mcp-sealed"}`; with no vault set up a
secret is refused at the write, never kept in the clear (but in the
plaintext vault mode). Never logged, never in an answer, an audit row or an
error: what a server answers is scrubbed of its secret before anyone reads
it (a server that echoes the header back shows `[secret]`).

**The identity of a server** for what a person allows is its id AND its
address: standing approvals and pins are kept under `ext:<id>@<hash of
the url>`, so a server whose URL changes — or another server given the
same id — is one nobody allowed yet. An approval of a server no longer in
force (gone from the list, its URL changed, or the person no longer in its
orgs) is dropped at the next sweep (the list's every write sweeps).

**Routes** (the person's agent; a handle on every call):

| Route | Class | Answer |
|---|---|---|
| `GET /delegate/mcp/servers` | read | `{servers: [{server: "ext:<id>", name, host, note?, trustReadOnly, state, why?, code?}]}` — the outside servers this person's agent may use, with the conversation's state for each (`on`: by the ordinary approvals; `asks`: each call asks — the conversation holds data of an org that doesn't trust the list; `off`: why). `[]` unless the mode is `listed` and the person's `mcp` is on |
| `POST /delegate/mcp/tools {server}` | act (not ticketed) | xbind's `initialize` (+ `notifications/initialized`) and `tools/list` (pages followed, at most 10) → `{server, name, host, outside: true, tools: [{name, title?, description?, readOnly, inputSchema?}]}`. Free: no ticket, sends nothing of the conversation; counts against the MCP limit; marks the conversation `web` |
| `POST /delegate/mcp/call {server: "ext:<id>", tool, arguments}` | act | as for a tile (§6), and `host`, `outside: true` |
| `POST /delegate/mcp/test {id}` | never (an admin's own session) | `initialize` and `tools/list` as the admin, no session kept → `{ok, server: {id, name, host}, protocolVersion?, serverInfo?, tools?, error?, refusal?}` — always `200`; the card shows the tools or the exact error |

**Judged in order** (a call): the reach (mode `listed`, the person's `mcp`)
→ the server in force and visible to the person (`404 mcp-server` alike
for none and not yours) → the org rules of every org in `tainted`:
`never` refuses (`mcp-org`); `org` refuses unless the server is listed
with that org; `any` (unset) makes the call **asks each time** unless the
server is listed with that org; `listed` passes → the server's tool
(`mcp-tool`) → covered: never while it asks each time; else a standing
approval of the tool whose pin matches, or — with the server's
`trustReadOnly` — the person's `mcpReadOnly` for a tool marked read-only
whose pin is the one first seen; else a ticket → the limit → the ticket →
the call. A request filed while the call asks each time carries
`preview.each: true` and its `hour`/`always` are refused (`400`): what
leaves the workspace from such a conversation is approved call by call.

**The wire.** xbind builds every request from what it read strictly
(`mcpjson.go`), and speaks streamable HTTP to the listed URL alone:

- `POST` to the URL with `Content-Type: application/json`, `Accept:
  application/json, text/event-stream`, the fixed `User-Agent`, the auth
  header, and after `initialize` `Mcp-Session-Id` and
  `MCP-Protocol-Version` (the version the server answered, a date); no
  cookies, no `Referer`, never a proxy of the environment;
- dialled through the web's guarded dialer (`internal/webfetch`): the
  URL's host is the one name it dials; its addresses checked when the
  socket connects (the web's rule, or `privateNetwork`'s); TLS verified;
- a redirect is followed only to the same scheme, host and port (at most
  3, the `POST` re-sent), any other is `mcp-redirect`;
- an answer is `application/json` (one JSON-RPC response, never a batch)
  or `text/event-stream`, read event by event until the response with the
  request's id: notifications are skipped, a **request from the server**
  (sampling, elicitation, roots, a ping — anything with an id and a
  method) ends the call (`mcp-protocol`): xbind is a client for
  `initialize`, `ping`, `tools/list` and `tools/call` and answers no
  server; xbind never opens the `GET` stream;
- at most 1 MiB of one answer (an event stream's whole body counted),
  20 s for `initialize` and `tools/list`, 60 s for `tools/call`;
- the session id is xbind's, kept per (person, server, conversation) in
  memory (30 minutes unused, 4096 at most), never in an answer; a `404`
  on it starts a new session once;
- `401`/`403` from the server is `mcp-auth` ("the server refused xbin's
  credentials: an admin checks this server's secret"), a status ≥ 300
  otherwise `mcp-unreachable` with a clipped, scrubbed excerpt; can't
  connect `mcp-unreachable`, no answer in time `mcp-timeout` (504), an
  address refused `mcp-address`.

**Data.** A call sends its arguments out of the workspace: from a
conversation that holds an org's data it is the web's way out (§4.2), so
it asks each time unless the org trusts the list or the server is listed
with it. What a server answers — its tools' descriptions, a call's
result — is untrusted input from outside: it taints with no org (it isn't
the workspace's data) but marks the conversation `web: true` ("has read
the web or an outside server"), and the backend frames it as a page's
words. The sheet says plainly "Outside this workspace: <host>", the host
written by xbind from the listed URL.

**Limits and the log**: the MCP limits of §6 (one bucket for tiles and
outside servers); every list and call is a row of the person's log —
`class: "mcp"` (a call) or `"mcp-read"` (a list), `server` (`ext:<id>`),
`tool`, `path` the URL's scheme and host (never its path: a URL can hold
a token), `bytes`, `status` — never an argument's value, never a secret.

## 7. Not built yet

- `web.mode: "full"` (methods with a body, credentials) — refused `400`.
- `web.search: true` (a provider's native search through the connector,
  §4.5) — refused `400 "not available yet"`.
- An outside server's sign-in by OAuth (only a secret the admin sets: none,
  bearer or a header), resources and prompts, and the server-to-client
  `GET` stream.

## 8. Off switches

Pause, the workspace's switch off, a hold and no-agent stop both: a fetch
or an MCP call in flight is cut with the other delegate calls, a ticket of
either kind is void, a pending one is kept or withdrawn as every request
is (the off-switches table gains the two rows: web fetches, MCP calls).
An outside server adds one (D240): **the server leaves the list, its URL
changes, or the person leaves its orgs** — new calls and lists refused
(`mcp-server`), a call in flight finishes (60 s at most), a ticket for it
refused at use, its `tool` approvals dropped at the sweep (the key no
longer in force), pending requests refused at use.

## 9. UI

- **Preferences** (`<bx-agent-prefs>`; the app's AgentPrefsView): "Let my
  agent read the web", "Let my agent use MCP tools" and, under it, "MCP
  tools marked read-only don't ask me". Turning one on widens — armed like
  Allow (`WidenGuard`; the app's `AgentWidening`); off is never held. When
  the policy forbids, the toggle is off and disabled with why ("Off for
  this workspace — an admin can change it").
- **The approval sheet** draws `kind: "web"` (the full URL, its host, why
  xbin asks: the orgs the conversation holds; once only) and `kind: "mcp"`
  (server, tool, read-only or not, the arguments; once, or always for this
  tool), and says "This conversation has read the web" when
  `preview.readWeb`.
- **The admin's settings** (`/xbin/system`, the workspace agent's card):
  web off/read, the allow and deny lists, search, MCP off/tiles/listed,
  each "Set by your host" where the host's file holds it; under `listed`
  the outside servers' list (`<bx-wsagent-mcp-servers>`): add, edit,
  remove; the secret a password field that shows "set" and never the
  value; "Plain http, I know" and "On a private network" and "Trust its
  read-only marks" each a checkbox armed like Allow with its warning
  beside it; **Test** on a saved server shows its tools or the exact
  error; Save sends the whole list.
- **Preferences** (web and the app) list under the MCP toggle the outside
  servers the person's agent may use (`net.mcp.servers` of
  `GET /delegate/state`: name and host) and say that a call to one leaves
  the workspace.
- **The sheets** (web and the app) for an outside server's tool lead with
  "Outside this workspace: <host>" (xbind's host), the server's name, and
  — while the call asks each time — why, with Once the only choice.
- **The header's connection line** counts the outside servers the session
  may use ("MCP · 2 outside").
- **The agent's page and native view**: the header's connection line adds
  "web" / "MCP" when on; the empty state's lists say what is true.
- **The empty state's lists** (the owner's second item): CAN — "Look across
  your N connected tiles", "Change a tile's code — by briefing that tile's
  own agent", "Do things in a tile for you, once you approve", "Remember
  what you tell it", and "Read web pages" / "Use MCP tools" when on;
  CAN'T — "Answer a tile agent's questions for you — those come to you",
  "Act without your approval where a change is involved", "See the N tiles
  that aren't connected" (when there are), and "Use the web" / "Use MCP
  tools" only when off, with who can turn it on.

## 10. Tests (each rule fails without its code)

SSRF: loopback, private ranges, link-local and metadata, IPv6 loopback /
ULA / mapped forms, decimal / octal / hex IPv4 forms, a redirect to a
private address, DNS rebinding through a test resolver whose second answer
is loopback; the size and time caps; the content-type list; the taint
ticket (tainted asks, untainted doesn't, `allow` org skips it, `never` org
refuses); policy narrowing (host ceiling, admin narrowing, person within);
the host lock and reload; the off switches. Backend scripted evals: one
refusal and the model stops; a tainted fetch asks; a page that says
"ignore your instructions" is framed and not followed. End to end
(`test/isolated/wsagent_mcp_test.go`), the builtin `mcp-server` template is
the MCP tile: the agent lists `roll_dice` with the free reads, calls it
under a ticket its person gave once, and — with "read-only tools don't ask"
— at once.

Outside servers (D240), each against local fake servers, never the
internet: plain JSON, bearer and header auth (the secret reaches the
server, never an answer — an echoing server is scrubbed), an event
stream (notifications skipped), a slow one (the time cap), an oversized
one (the size cap, a stream too), a redirect to another host (refused) and
to the same host (followed), one that sends a sampling request mid-call
(the call ends), one on loopback without `privateNetwork` (refused at the
dial) and with it (reached), link-local refused even with it, a DNS answer
that changes to loopback; TLS verified (an unknown certificate fails);
the session id reused per conversation and never answered; the list's
validation (http without `plainHttp`, auth without a secret, a reserved
header, an unknown org); secrets sealed (the file holds no plaintext with a
vault, the write refused without one), never in a read; the host's list
locked (409), its `secretFile`/`secretEnv`; visibility by `orgs`; the
org rule (`any` asks each time and refuses `hour`/`always`, `listed`
passes, `org` inside/outside, `never`); the key with the URL's hash (a
changed URL is not allowed); `trustReadOnly` off by default; the sweep.
Backend scripted eval: the model lists an outside server's tools, calls
one under a ticket, and stops after one refusal. End to end
(`test/isolated/wsagent_mcpext_test.go`): a fake outside server on the
host's loopback, listed with `privateNetwork`.
