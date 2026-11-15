# Protocol reference

Every xbind endpoint, header, and wire format. This is the contract that
`bx`, the core web elements, and the SDKs are built on — anything here is
fair game for your own tooling.

## Topics

The reference is one page per topic under `/docs/protocol/`; this page
keeps what they share — authentication, the routing rules, and the
contracts between xbind, a tile's page and its backend. A route is
documented in exactly one of them:

| Page | Covers |
|---|---|
| [protocol/core.md](/docs/protocol/core.md) | routes outside `/api/xbin/`: `/healthz`, `/login…`, `/logout`, `/c/…`, `/vendor/`, `/docs/`, `/xbin/…` pages, `/api/<tile>/…`, path tickets |
| [protocol/users.md](/docs/protocol/users.md) | `/frame-token`, `/path-tickets`, `/whoami`, `/impersonate`, `/users`, `/account/password`, `/login`, `/invite/…`, `/sessions`, `/auth-settings`, `/auth-rotate-token` |
| [protocol/orgs.md](/docs/protocol/orgs.md) | `/orgs`, `/permission-sets`, `/net-sets`, `/owner`, `/access…`, `/users-directory`, `/policy`, `/agents/policy`, `/partitions/limits`, `/limits` |
| [protocol/grants.md](/docs/protocol/grants.md) | `/auth-overview`, `/grants`, `/bindings`, `/iface-instances`, `/ingress…` |
| [protocol/workspace.md](/docs/protocol/workspace.md) | `/prefs`, `/screens`, `/branding`, `/native-runtime`, `/workspace-settings`, `/workspace-policies`, `/chrome`, `/defaults` |
| [protocol/tiles.md](/docs/protocol/tiles.md) | `/components`, `/tile-assets`, `/create`, `/clone`, `/builtins`, `/templates`, `/lifecycle`, `/tile-report` |
| [protocol/code.md](/docs/protocol/code.md) | `/code/…`, `/git/…`, change proposals (`/code/prs`, `/code/pr`) |
| [protocol/runtime.md](/docs/protocol/runtime.md) | `/status`, `/backends`, `/runtime`, `/gpus`, `/go-build-versions`, `/vm`, `/sandboxes?tile=`, `/tile-status`, `/term-net`, `/logs`, `/alerts` |
| [protocol/agents.md](/docs/protocol/agents.md) | `/term/sessions…`, `/agent/providers`, `/agent/history`, `/agent/prefs`, `/agents/connection`; agent session events |
| [protocol/terminals.md](/docs/protocol/terminals.md) | `/ws/term`, the terminal wire |
| [protocol/events.md](/docs/protocol/events.md) | `/ws/events` |
| [protocol/resources.md](/docs/protocol/resources.md) | `/vaults`, `/resources`, `/vault…`, `/kv`, `/blob`, `/bus`, `/cron`; bus push delivery |
| [protocol/partitions.md](/docs/protocol/partitions.md) | `/partitions…`, partition mail and its doorbell |
| [protocol/backups.md](/docs/protocol/backups.md) | `/backup…`, `/backups`, `/restore`, `/backup-schedule`, `/backup-keys`, `/partitions/backups`, `/partitions/restore` |
| [protocol/deployments.md](/docs/protocol/deployments.md) | `/deployments…`, `/checkpoints/…`; the tile deployments conventions |
| [protocol/sandboxes.md](/docs/protocol/sandboxes.md) | a manager tile's `/sandboxes…` routes |
| [protocol/devices.md](/docs/protocol/devices.md) | `/devices…`, `/web-ticket`, `/app-tunnel`, `/notify`, `/push/…`; push notifications, Live Activities |
| [protocol/delegation.md](/docs/protocol/delegation.md) | `/delegate/…`: a person's workspace agent — the delegate call, approval requests and tickets, standing approvals, admin change sets, the audit log |
| [protocol/llm.md](/docs/protocol/llm.md) | `/llm/v1/…` (the OpenAI-compatible routes), `/llm`, `/llm/config`, `/llm/upstreams`, `/llm/usage`, `/llm/quotas`; LLM keys |
| [protocol/sealed.md](/docs/protocol/sealed.md) | reserved: sealed components |

## Authentication

Every route except `/healthz`, `/login` (with its `/login/…` legs) and
the native app's public routes — the credential-in-body ones (`POST
/api/xbin/login`, `POST /api/xbin/devices/enroll`, `POST
/api/xbin/invite/check`, `POST /api/xbin/invite/redeem`) and the sign-in
discovery (`GET /api/xbin/login/methods`) — requires a principal
([auth.md](/docs/auth.md)):

| Mechanism | Sent as | Principal |
|---|---|---|
| Owner cookie | `xbin_session` (HttpOnly, Lax; set by `/login?token=…`; `__Host-xbin_session` under `--tile-assets=origins`) | owner |
| Tile-origin cookie | `__Host-xbin_tile` on a tile's own origin (`--tile-assets=origins` only; set by the ticket exchange, bound to the browser session; each tile deployment has its own origin, whose ticket and cookie beyond `main` are the `x2`/`c2` forms) | that tile's frontend (element principal), in that origin's deployment |
| Asset token | the `/c/~<token>/…` path prefix (`--tile-assets=tokens` only; minted into a tile document's `<base>`) | none — admits non-document static files of tiles its user can read, nothing else |
| Owner/instance bearer | `Authorization: Bearer <token>` | owner, or the element the instance token belongs to |
| Terminal bearer | `Authorization: Bearer <token>` (`$XBIN_TOKEN` in a terminal) | the tile the terminal is opened on (element principal; per-session, revoked at session end) |
| Frame token | `X-XBin-Frame-Token` header, or `?frame=` on any URL (WS, document loads, tag-driven requests like `<a download>` — anything that can't set headers; xbind consumes it and never forwards it to backends) | element frontend — **standalone** (no cookie needed; sandboxed tile frames hold nothing else) |
| App session | `Authorization: Bearer <token>` from `POST /login/device`, `POST /api/xbin/login` or `POST /login/ticket` (the native app's own code only — never tile code) | the signed-in **user**: the same human principal and lifetimes as their browser session (docs/auth.md §Device login) |

**The app tunnel** (D204, [app-tunnel.md](/docs/app-tunnel.md)). The xbin
app may reach xbind through an iroh tunnel: each connection is a paired
device's, after a Noise handshake inside iroh's own encryption, and carries
plain HTTP/1.1 on the app's loopback `Host` — never `Secure` or `__Host-`
cookies, `xbin-ws-origin` is `ws://<Host>`, `X-Forwarded-*` is dropped, and
the client address (throttle, session records) is the device. One device per
tunnel: device D's tunnel serves requests with D's credentials (its
sessions, the web sessions it opened, what they minted), D's own sign-in
(`/login/device…` for D, web-ticket redemption), `GET
/api/xbin/login/methods` and `/vendor/`; anything else answers `403
{error, tunnel: "device", docs}`, and so do the app's other sign-ins over
it. A session opened over a tunnel is bound to it: off it, it and
everything derived from it answer `401 {error, tunnel: "bound", docs}`.
While the workspace requires the tunnel, the app's credentials and sign-ins
off it answer `403 {error, tunnel: "required", docs: "/docs/app-tunnel.md"}`
— never 401 or 404; browsers and the SSO browser leg are not affected.

**Frame tokens are bound to the login that minted them.** A token carries
its credential generation — the browser or app session behind the
`/c/` document load or `/api/xbin/frame-token` call (on a tile origin, the
browser login its tile cookie is bound to), and renewals copy it —
so logout, revoking the device, "sign out everywhere", disabling the user,
and (for the bootstrap token's frames) rotating the owner token end it at
once instead of at expiry. Frame use counts as that session's activity (its
idle window slides; the absolute cap stays). An xbind restart ends the
sessions but not the bound tokens: open tiles renew until their login would
have expired. Tokens minted by an xbind older than this rule (four
`|`-separated fields instead of five) verify until they expire and renew
into bound ones, tied to the user's current generation. A frame token of a
tile deployment other than `main` has a sixth field before the signature,
the deployment's name in base64url, covered by the signature; tokens of
`main`, and every token of a tile without deployments, keep five fields.
Renewal (`GET /api/xbin/frame-token`) keeps the deployment, and an xbind
older than tile deployments refuses six-field tokens. The token stays
opaque: frontends pass it along, never parse it; a renewal answering 401
means the login ended.

**Browser-plane isolation (ND8):** the cookie proves the human, and humans
act only from *chrome* (the shell, plus `chrome: true` components that are
shipped chrome — `tiles/organisations` — or that a workspace admin approved
with `PUT /api/xbin/chrome`, D118; the manifest flag alone is only a request).
Non-chrome tile documents are served with `Content-Security-Policy: sandbox
allow-scripts allow-forms allow-modals allow-downloads` (plus
`allow-popups allow-popups-to-escape-sandbox` for a tile granted
`cap:open-links`, ND11 — the same extras `/components` reports as `sandbox`
so `bx-frame`'s attribute matches) and framed sandboxed by `bx-frame`
(plus `credentialless` where supported) — an opaque origin with no DOM access
either way, no storage, no ambient cookie. Served HTML also carries
`<meta name="xbin-sandbox">` with the full token list (absent on chrome).
A partitioned tile's documents carry `<meta name="xbin-partition"
content="user:<id>">` naming the partition the viewer reaches (`global` for
the owner token and `--no-auth`, and in a non-primary deployment's document,
`/c/<tile>+<name>/`, whose one instance every writer shares; none when the
viewer reaches none), which the client exposes as `xbin.partition`; an
admin viewing as someone gets the document without a frame token.
The person's **appearance** (D184) rides the same block, only when they
chose one: `<meta name="xbin-theme" content="light">` (or `dark`; absent:
follow the system) and `<meta name="xbin-density" content="comfortable">`
(absent: compact), from the `theme` and `density` keys of their shell
bucket (protocol/workspace.md, `/prefs`). The person is the principal's: a
session's or device's, the person behind a frame or terminal token, the
viewed person while an admin views as someone, the owner's (`root`'s
bucket) for the root token and `--no-auth`; a tile's backend gets neither.
Every injected document carries them — tile pages, deployment URLs,
partitioned tiles, chrome, the native runtime document — and so does the
docs viewer (`/docs/…`); a person who never chose gets the injection as
before, byte for byte. A **device override** (D188) rides the document's
URL: when its query carries `xbin-appearance=light` (or `dark`) the theme
meta holds that value instead of the person's — `<bx-frame>` adds it to a
tile's URL while the browser overrides the person's theme in the shell's
settings (*This device*), so the tile's first frame matches it; the
density stays the person's, any other value is ignored, the parameter
survives the tile-origin redirects (origins mode), and nothing else reads
it. `/vendor/theme.css` reads them in documents that opt
in with `<html data-bx-theme="auto">`; frames hear later changes as
`xbin:appearance` (§Tile ↔ shell messaging). xbind's own pages that get no
injection read the **hint cookie** `xbin_theme=light|dark` (absent: the
system's; `Path=/`, `SameSite=Lax`, 400 days, `Secure` on https, not
HttpOnly, host-only: never sent to tile origins). The sign-in, invite and
invalid-invite pages, "Continue as", the partition switch page, the tile
navigation hop and the tile-origin refusal render the meta from it
server-side; request access and the partitions page copy it with
`/vendor/theme-boot.js` — the same rule everywhere: exactly `light` or
`dark`, else the system's. A page served on a tile's own origin (origins
mode) never gets the cookie and follows the system. The shell keeps it
equal to the person's choice (`/vendor/bx-theme.js`) — or to the device's
theme while that browser overrides it (D188); it is a UI hint,
never a credential, and any other value is ignored.
Server-side, any request carrying
the cookie with the opaque-origin fingerprint — `Sec-Fetch-Site: cross-site`
(or `same-site`) on a non-navigation, or a non-GET navigation to `/api/*` or
`/ws/*` (form-POST CSRF) — has the **cookie dropped** before principal
resolution: a tile that omits its frame token cannot ride the human's
session. Requests with `Origin: null` (opaque-origin fetches) get
`Access-Control-Allow-Origin: null` and preflight answers — required for
tile `fetch()` to function at all, and safe because tile requests carry no
ambient credentials. A tile's own origin (origins mode) answers them on
`/vendor/` too: a sandboxed frame there loads the theme's fonts
cross-origin (D184). A preflight allows `Authorization`, `Content-Type`,
`X-XBin-Frame-Token` and MCP's streamable-HTTP headers (`Mcp-Session-Id`,
`MCP-Protocol-Version`, `Last-Event-ID`), and every answer exposes
`Mcp-Session-Id`, so a tile page can talk MCP to a provider it binds.

The gateway unix socket (`$XBIN_GATEWAY`, `.xbin/run/gateway.sock`) serves
this same API; element backends use it with their instance bearer token.

Identity headers **injected by xbind** on proxied component requests
(inbound values are stripped — receiving them means they're verified):

```
X-XBin-From: owner | <component-path> | xbin/cron | xbin/bus | xbin/mail | ingress
X-XBin-Role: <role granted on the callee>
X-XBin-User: <user id>                   (the signed-in HUMAN driving the
                                          call, when there is one — direct,
                                          or riding the tile's frontend/
                                          terminal; absent for automation
                                          and the bootstrap token, D29)
X-XBin-User-Level: read|write|terminal   (that user's level on the callee;
                                          set with X-XBin-User)
X-XBin-Viewed-By: owner | <admin id>     (set with X-XBin-User when an admin
                                          is VIEWING AS that user, D64 — a
                                          backend keeping per-user private
                                          data should not show it)
X-XBin-Agent: <sealed component path>    (set with X-XBin-User when the call is
                                          that person's workspace agent acting
                                          for them, not their own hand:
                                          X-XBin-From is user:<id>, X-XBin-Role
                                          reader | writer — their level, never
                                          admin; protocol/delegation.md)
X-XBin-Ingress-Host: <public hostname>   (ingress traffic only)
X-XBin-Deployment: <name>                (the calling tile's deployment, when
                                          the caller is one of a tile's own
                                          credentials — backend, frame,
                                          terminal or agent session — bound
                                          to a deployment that isn't the
                                          tile's primary; on its self-calls
                                          and its calls to other tiles.
                                          Absent for a primary's calls,
                                          people, the owner, xbin/cron,
                                          xbin/bus and ingress. X-XBin-From
                                          stays the tile path)
X-XBin-Partition: user:<id> | global    (partitioned tiles: the partition
                                          the caller acts in — its own
                                          partition for a partitioned tile's
                                          credentials and deliveries, on
                                          every call, to tiles that aren't
                                          partitioned too; user:<id> for a
                                          person calling a partitioned tile;
                                          global for a global instance's
                                          calls and the owner token reaching
                                          one. Absent for everything else, so
                                          a tile that isn't partitioned
                                          calling in sends none. A display
                                          name: key state on the id below)
X-XBin-Partition-Id: u-<32 hex>          (with X-XBin-Partition: user:<id>
                                          only: the partition's opaque id,
                                          stable for the person and never
                                          reused by a person later created
                                          under the same id. Providers key
                                          per-caller state on (X-XBin-From,
                                          X-XBin-Partition-Id); absent means
                                          the caller's one non-personal
                                          identity, global included)
X-XBin-Backup-Subkey: bk-<32 hex>        (only on xbind's own archive PUT
                                          of a sealed archive to an
                                          archiver: the opaque id of the
                                          backup key it is sealed under —
                                          §Backup; never on anything else)
```

A proxied response from a non-primary deployment carries
`X-XBin-Deployment: <name>`, set by xbind over any value the backend set; a
primary's responses are unchanged (protocol/deployments.md).

A partitioned tile's own call **addressing its global instance**
(`?xbin-partition=global`, protocol/core.md) arrives there as the
partition's **person**, whatever the credential — a user partition's
backend's instance token included, and any value the caller sent
stripped: `X-XBin-User` is the person, `X-XBin-User-Level` their level on
the tile (read live), `X-XBin-Role` `reader` for read or `writer` for write
and terminal — never the self-call's `admin` — `X-XBin-From` the tile
itself, and `X-XBin-Partition: user:<id>` with its `X-XBin-Partition-Id`. A
global instance must never treat a call carrying `X-XBin-Partition:
user:…` as the tile itself. A view-as credential's call is `reader` there
whatever the viewed person's level, with `X-XBin-Viewed-By`. An admin
calling the tile directly — the only person who can call `/api/<tile>/…`
by themselves; everyone else comes in through the tile's frame — keeps
their own identity and role there.

xbind's own credentials never reach a backend: the session cookie
(`xbin_session` / `__Host-xbin_session`), the tile-origin cookie, an
`Authorization: Bearer` (owner, instance, terminal or app-session token)
and `?frame=` are removed before a request is proxied. The tile's own
cookies and a non-bearer `Authorization` pass. Who called is what the
headers above say.

`From: ingress` is anonymous PUBLIC traffic through a published endpoint
(docs/ingress.md): no role, confined to the manifest's declared public
paths, and structurally unable to reach `/api/xbin/*` or any other tile. It
enters on the separate ingress listeners — never the authenticated routes
below — and no component can be named `ingress` (reserved), so the value is
always trustworthy.

**A tile's own credentials act in one of its deployments**
([tile-deployments.md](/docs/tile-deployments.md)): a backend's instance
token in the deployment its generation runs, a frame token (or tile-origin
cookie) in the deployment of the document it was minted for, a terminal or
agent session's token in the session's target (the current primary, for a
session that follows it). The tile path stays what grants, `X-XBin-From` and
other tiles see. Such a credential bound to a deployment that isn't the
primary is default-deny on `/api/xbin/*`: every route is classed
deployment-scoped (it acts on the caller's own deployment), primary-only
(refused) or neutral (no deployment in it, unchanged), and a route without a
class refuses it too, reads included (protocol/deployments.md, *Which deployment
a call acts on*).

**A partitioned tile's credentials act in a partition**
([partitions.md](/docs/partitions.md)). On a tile whose
recorded mode has user partitions, xbind decides the partition from the
credential, never from the URL or a header:

- the tile's frames, terminals, agent sessions and path tickets, and a
  person calling it directly (an admin included), act in **their person's
  partition**, `user:<id>`, while that person exists, is enabled and can
  read the tile; the owner token's (no person) in `global`, or nowhere
  (`403 sign in as a person: <tile> keeps each person's data apart`) when
  the tile has no global instance; a view-as session in none (`403 <tile>
  keeps <user>'s data private: view-as can't open it`);
- a backend's instance token in the partition its generation was started
  for. A user partition's token authenticates only while that partition
  is covered — the tile is still partitioned and its person still exists
  (the same incarnation), is enabled and can read the tile: otherwise it is
  a **401**, at once;
- cron and bus deliveries in their registration's partition, and a
  partition mail doorbell (`xbin/mail`) in its addressee's, refused (403)
  while its person is gone, disabled or can't read the tile;
- another tile's credentials in their own partition, mapped onto the
  callee (§Providers in partitions.md): a partitioned caller's user
  partition reaches the same person's partition of a partitioned callee
  when the grant allows and the person can read the callee (and, with the
  workspace setting `partitionConsent` on, consented: `403 <id> hasn't let
  <caller> use their <tile> data (they allow it at /xbin/partitions)`);
  anything else reaches the callee's
  global instance, or `403 <tile> is partitioned: only partitioned tiles
  reach its people's data, and it has no global instance`;
- a non-primary deployment of a partitioned tile has one instance,
  `global`;
- the one way out of a partition by the URL: the tile's own frames,
  terminals, agent sessions and user-partition backends — and an admin
  calling it directly — reach its **global instance** with
  `?xbin-partition=global` (protocol/core.md), attributed to the person.
  Nothing addresses a user partition by the URL.

A user partition's credential is default-deny on `/api/xbin/*`, like a
non-primary deployment's: every route has a partition class too —
partition-scoped (the handler acts on the caller's partition: its own vault,
cron jobs and bus subscriptions among them), dormant (the global instance's
registrations — interface instances and ingress hosts: a person's partition's
are stored for it and answered with success and `dormant:true`, and never
route), global-only (refused: `403 this route is
the global instance's alone: a person's partition (user:<id>) can't use
it`), person-only (a person's own session, app or device credential only:
`403 this is a person's own act …` for every tile credential, partitioned
or not, the owner token and view-as) or neutral — and a route without one,
or whose handler doesn't act per partition yet, answers `403 this route
isn't available to a partition's credentials yet`. The `/prefs` routes keep
a person's own bucket, so a user partition's frames, terminals and agent
sessions use them, while its instance token, which names no person, is
refused them (`403 … that name no person yet`). The global instance, and
every credential of a tile that isn't partitioned, meet none of this.

## HTTP routes

### Core

Moved to [protocol/core.md](/docs/protocol/core.md): every route outside `/api/xbin/`.

### xbind API (`/api/xbin/…`)

A path is routed segment by segment as it was sent: an encoded `/` (`%2F`)
stays part of its segment's value and never reaches another route (a
catch-all route — `kv`, `blob`, `vault` — sees the decoded value, as it
always did), and a segment that decodes to `.` or `..` (`%2E`) is 400
`{"error": …, "refusal": "invalid"}`.

The routes are documented by topic — the table under [Topics](#topics)
names the page for each. The machine-readable list of all of them:

```
GET    /openapi.json              any. OpenAPI 3.1 spec of this built-in API,
                                   incl. the RBAC capability per endpoint
                                   (x-xbin-capability). Rendered by the API-docs
                                   tile; importable into Swagger UI / Postman.
```

### Tile deployments (`/deployments`, `/checkpoints`)

Moved to [protocol/deployments.md](/docs/protocol/deployments.md).

### Tile sandboxes (manager tiles)

Moved to [protocol/sandboxes.md](/docs/protocol/sandboxes.md).

## WebSockets

### `/ws/term` — terminals (admins + users with a terminal-level tile)

Moved to [protocol/terminals.md](/docs/protocol/terminals.md).

### The terminal wire

Moved to [protocol/terminals.md](/docs/protocol/terminals.md).

### `/ws/events` — event stream

Moved to [protocol/events.md](/docs/protocol/events.md).

### Agent session events

Moved to [protocol/agents.md](/docs/protocol/agents.md).

## Tile ↔ shell messaging (window.postMessage)

Tiles are sandboxed opaque-origin iframes (ND8); a tile's `xbin-client.js`
and its embedding `<bx-frame>` (relayed to `<bx-shell>`) talk by
`postMessage`. Every message is `{ type: "xbin:…", … }` and is only honored from
the frame's *own* iframe (`event.source` match) — the sender window is the
verified component identity. Because an opaque origin matches no origin
string, `targetOrigin` is `*` in both directions; confidentiality relies on
the messages being addressed to specific windows, never broadcast.

```
tile → frame   xbin:resize   {component, height}     auto-height (informational)
tile → frame   xbin:dialog   {id, spec}              request a shell modal
tile → frame   xbin:window   {id, spec}              request a pop-out window
tile → frame   xbin:window-close {id}                close a window it opened
tile → frame   xbin:open-deployments {tile}          open another tile's Deployments panel
tile → frame   xbin:open-session {id, tile, session} show one of the viewer's sessions in its tile's window (D229)
tile → frame   xbin:meta     {meta: {title?, icon?, badge?}}  the page's title, icon and badge for its window (D194)
tile → parent  xbin:scroll-focus {}                  the pointer entered this document (cosmetic)
tile → frame   xbin:contextmenu {x, y, selection, html} a right-click: the shell's tile menu (Copy first with a selection)
tile → frame   xbin:copy     {id, text, html}        copy to the clipboard, text and HTML (D199)
tile ⇄ frame   xbin:location, xbin:navigate {hash}  (D220)
frame → tile   xbin:reply    {id, result}            dialog result / window closed / copy taken (true|false)
frame → tile   xbin:appearance {theme, density}      the person's appearance changed (cosmetic)
```

`xbin:appearance` (D184) keeps a frame's theme and density in step with
its embedder's: `<bx-frame>` posts it to its iframe on every load and on
every change of the person's choice, and only when its own document
follows the person (`<html data-bx-theme="auto">`) — an older shell that
never opted in sends nothing, so a tile keeps what xbind injected. `theme`
is `system`, `light` or `dark`; `density` is `compact` or `comfortable`.
`xbin-client.js` applies it only from its parent window (on a tile's own
origin, only from the workspace origin) and only with those values: it
rewrites the document's `xbin-theme` / `xbin-density` metas, so a document
that opted in restyles at once, its own frames pass it on, and code that
paints from tokens hears it through `/vendor/bx-theme.js`'s
`onAppearance()`. It carries no credential and asks for nothing; a tile
can post it only to frames it embeds, which changes nothing but their
colours. While the browser overrides the person's theme (D188) the
message carries the device's theme — what the embedder shows.

`xbin:open-deployments` (the admin console's runtime → deployments tab
links with it) asks the shell to open `tile`'s terminal window on its
Deployments panel (a tab, D202), as the tile menu's "Deployments…" does. `<bx-frame>`
passes on only a well-formed tile path (no `.`/`..` segment), and
`<bx-shell>` opens it only for a tile its viewer's `/components` lists; the
panel then runs as the viewer, so the message grants nothing. Anything else
ignores it (the xbin app has no terminal window).

`xbin:open-session` (D229; `xbin.openSession({tile, session})`) asks for
one of the viewer's own terminal or agent sessions to be shown in its
tile's terminal window. `<bx-frame>` passes on only a well-formed tile path
and session id, as the cancelable event `bx-open-session` `{from, tile,
session}`; `<bx-shell>` takes it (`preventDefault`), and for a tile its
viewer's `/components` lists lands on it as a link would and calls that
tile's `<bx-frame>` `open({session})` — for any other it sets
`detail.refused`. A shell that doesn't take the event (an older scaffold)
leaves it to the frame: the session opens when its tile's frame is on the
page, and otherwise the frame follows the link
`?tile=<tile>&session=<id>` — only while the click that asked is still
active, so a page cannot navigate the workspace by itself. The answer is
`xbin:reply {id, result}`: whether the request was taken. The window lists
the viewer's own sessions, so the message grants nothing; the xbin app has
no terminal window and never answers.

`xbin:copy` (D199; `xbin.copy({text, html})`) asks the embedding page to
write the person's clipboard — a sandboxed tile has none of its own. Text
and HTML are each clamped to 256 KiB; the HTML is the tile's, so
`/vendor/copy-rich.js` rebuilds it from an allowlist before it is written
(p, br, strong, em, code, pre, lists with an ol's `start`, tables with
`colspan`/`rowspan`, h1–h6, blockquote, hr, and links to http(s) or mailto
only; every other element unwrapped to its text or, for scripts, styles,
controls, images and embeds, dropped; no other attribute). The page writes
it within the click's activation — the async clipboard, else
`execCommand('copy')` setting both flavours — answers `xbin:reply {id,
result: true|false}`, and fires `bx-copied {from, ok}`. A page that doesn't
know the message never answers; `xbin.copy` resolves `false` after 3 s.
`xbin:contextmenu` carries the selection's HTML as `html` beside its text
(a mouse right-click only; 256 KiB): the shell's Copy writes both, rebuilt
the same way.

`xbin:meta` (D194, `xbin.meta()`) carries the page's whole current meta —
a title (at most 80 characters), an icon name from `/vendor/bx-icons.js`
(at most 40) and a badge (a count or a short word, at most 8); a field the
page cleared is absent. `<bx-frame>` cleans it again (control and bidi
characters out, an icon that isn't an icon name dropped), keeps it as
`tileMeta` for as long as it shows that tile, and announces it as a
`bx-meta` DOM event `{path, meta}`; the shell draws it in the window's
title bar and the sidebar row, beside the tile's path — a title never
stands in for the path, so a tile can't pass itself off as another or as
the shell. It is per viewer and per window: nothing is stored, and the
manifest's `title`/`icon` (`/components`) are what everyone else sees.
The xbin app relays it too and shows it as `xbin.native.meta`'s.

`xbin:scroll-focus` (D123) is sent by `/vendor/bx-scroll.js`'s tracker in a
framed document when the pointer arrives, so the embedding document's tracker
drops its own focused-scroll tint (browsers do not reliably tell the parent
when the pointer crosses into an iframe). It carries nothing and asks for
nothing; any window may clear a tint, so it is not source-checked.

`<bx-frame>` re-dispatches dialog/window requests as a `bx-spawn` DOM event
carrying the **verified** component (never a tile-supplied one) plus a `reply`
closure; `<bx-shell>` renders the dialog (`<bx-dialog>`, from data) or window
(a nested `<bx-frame>` on a sub-path) and calls `reply` on resolve/close.
Window sub-paths are traversal-stripped; framing still runs the normal
frame-token / tile-access checks; dialogs are rendered with the verified
originating component shown, and each tile is capped to one dialog + a few
windows. See docs/elements.md §Dialogs & windows and the `xbin.dialog` /
`xbin.window` APIs in docs/sdk.md.

**In the xbin app** a tile page is a top-level WebView, so `window.parent` is
the page itself. The app registers a WebKit message handler named `xbin` and
injects a document-start script that relays the page's own `xbin:dialog`,
`xbin:window`, `xbin:window-close`, `xbin:contextmenu` and `xbin:meta` posts (sender = the
page's own window; never a frame inside it) to the app as JSON strings;
`xbin-client.js` treats a top-level page with that handler as embedded. The
app answers with the same `xbin:reply {id, result}` posted to the page's
window, which passes xbin-client's sender check. The rules are the shell's:
the tile is the one the WebView was opened for, one dialog and six windows at
a time, sub-paths traversal-stripped. The native runtime document
(`?native=1`) uses the same relay for `xbin.dialog`.

## Backend contract (what the runner promises your process)

- Listen on `$XBIN_SOCKET` (unix, HTTP/1.1; WebSocket upgrades pass
  through; streaming/SSE works).
- You're started lazily, health-checked by socket-connect within 5 s,
  swapped blue/green on change (an edit to the tile's native UI entry
  alone reloads its views without a swap — docs/elements.md §Native app
  UI), SIGTERMed with a 30 s drain, idle-reaped
  after ~30 min, and crash-loop-broken after 3 fast exits. On SIGTERM stop
  taking connections and answer the requests you hold. A request your stop
  cut off before any answer goes to the generation that replaced you when
  sending it again is safe (no body, and an idempotent method or nothing
  of it reached you), even one you had begun; any other is lost
  (docs/elements.md §Runtimes & backend lifecycle).
- While your deployment is pinned (docs/tile-deployments.md) you run a
  checkpoint instead of the work tree — read-only at the tile's own path,
  restarted from its kept build on every restart — and a save doesn't reach
  you: a crash loop clears with a deploy or a restart. Your env is the same.
- A partitioned tile (docs/partitions.md) runs one process per person who
  uses it, `XBIN_PARTITION=user:<id>`, beside its optional global instance
  (`XBIN_PARTITION=global`, today's process at today's keys): started on
  the person's first use, never at boot, stopped 10 min after its last
  use (an open SSE stream isn't use), capped per tile and workspace (503
  `too many people's instances of <tile> are running; try again shortly`),
  and wired as a non-primary deployment is: relayed egress under the tile's
  policy, no host network, splice, roster, lan-ingress legs, ingress or
  stream-slot dials. Its socket, token, log and cgroup are its own; its code
  and build are the tile's. `XBIN_PARTITION` is set after
  `XBIN_COMPONENT` (after `XBIN_DEPLOYMENT` when that is set, for a
  non-primary deployment whose code asks for partitions: `global`) and
  never on an unpartitioned tile.
- A tile may run several deployments, each its own process with its own
  code, manifest, data, vault and log. A deployment that isn't the tile's
  primary needs `--isolate`; it starts on its first request and is reaped
  when idle unless its code declares `alwaysOn` and a tile manager turned its
  alwaysOn switch on; and it receives only its own requests: no other tile's
  calls, no ingress, no lan-ingress legs, no stream interface dials, no host
  networking or provider splice. Its calls to other tiles follow the tile's
  edge policy (protocol/deployments.md), its cron jobs and bus subscriptions fire
  for it unless a tile manager switched its deliveries off, and its
  notifications are held.
- stdout/stderr → `.xbin/log/<compkey>.log` (`bx logs`), where a failed
  deploy of a checkpoint also writes its compiler output; a deployment
  beyond `main` logs to `.xbin/deploy/<tile-key>/d/<name>/backend.log`
  (`GET /api/xbin/logs?deployment=`), setup output included.
- Env: `XBIN_SOCKET`, `XBIN_COMPONENT`, `XBIN_GATEWAY`, `XBIN_TOKEN`
  (per-generation), `XBIN_RES_*` (grants), `XBIN_IFACE_<slot>_URL`
  (http interfaces). Ingress wiring (docs/ingress.md):
  `XBIN_IFACE_<slot>_ADDR` (bound stream interface — dial it),
  `XBIN_IFACE_<slot>_IP` (lan-ingress leg — the address you own),
  `XBIN_INGRESS_FORWARD_URL` + `XBIN_LAN_INGRESS` (terminator / net-provider
  tiles). `XBIN_DEPLOYMENT=<name>` is set for a backend of a deployment that
  isn't its tile's primary when it spawns, and absent for the primary; a
  reassignment of the primary restarts both backends, the new primary first,
  so it holds for a process's life. `XBIN_COMPONENT` stays the tile path and
  `XBIN_RES_*` are the same in every deployment (their paths are bound to the
  deployment's own data). `XBIN_PARTITION=user:<id> | global` is set for a
  partitioned tile's instances ([partitions.md](/docs/partitions.md), in
  development): the person's partition the instance serves, or its global
  instance (a non-primary deployment's included); absent for every tile that
  isn't partitioned. `XBIN_RES_*` then name the partition's own data. A
  person's partition's multi-slot `XBIN_IFACE_<slot>` also lists their
  personal binds, each `{provider, url, service, personal: true}` after the
  global rows (`POST /api/xbin/partitions/binds`); the global instance's
  never does.

## Filesystem contract

```
<workspace>/
  xbin.json          workspace manifest: schema, importMap, grants, resources
                      (machine-managed on grant changes — comments don't survive)
  */scope.json        scope marker: resources, importMap overrides
  */xbin.json        component manifests (JSONC, yours to edit)
  .xbin/             xbind's local state (never a tile's):
    run/  log/  build/  cache/  env/  deploy/  docs/  restore/
                      derived — rebuilt on demand, safe to delete while
                      xbind is stopped: sockets (short tmp dir + symlink for
                      deep paths), backend logs, build output (and the
                      builds kept per checkpoint,
                      build/<key>/c/<tree>/{bin,build.json}), caches, env
                      layers, extracted checkpoints (deploy/<tile-key>/
                      <tree>/, read-only files in writable directories, so
                      rm -rf works), a deployment beyond main's backend log
                      (deploy/<tile-key>/d/<name>/), a protected primary's
                      build products (deploy/<tile-key>/protected/: once
                      lost, a restart rebuilds them only when every
                      recorded input matches, else the primary is held
                      until a tile manager redeploys its checkpoint), the
                      docs copy, a restore's transient staging
    token, secret     owner token, frame-token HMAC key (regenerated when
                      missing: the old owner token and every frame token
                      stop working)
    term/  uids.json  builtins.json  vm/policy.json  …
                      everything else is kept state, not derived: the
                      terminal dev layers (hand-installed, not
                      reproducible), the tier-2 uid map, builtin
                      provenance, the VM policy — deleting it loses what
                      it records
  data/               resource state: resources/, vault/, kv.db, cron-jobs.json,
                      bus-subscriptions.json; deployments/ (a tile's
                      deployment record, its deploy journal, and per
                      deployment beyond main <tile-key>/<name>/ with
                      cron.json, bus-subscriptions.json,
                      iface-instances.json, ingress-hosts.json and
                      backup-schedule.json — each {"schema":1, …} in the
                      rows' usual shape without component) and
                      checkpoints/ (its checkpoint store and the view
                      repository the fetch remote serves) — xbind's, masked
                      from terminals, created at a tile's first opt-in.
                      A deployment beyond main keeps its data under
                      resources-enc/.deployments/<scope>/<name>/ (kv.db,
                      fs/<resource>/; mounted at .xbin/resenc/.deployments/
                      …), its vault at vault/.deployments/<tile-key>/
                      <name>.json and its prefs at prefs/<user>/
                      .deployments/<tile-key>/<name>.json; main's stay
                      where they always were (backup unit; gitignored)
  homes/<user>/       terminal $HOME per user (dotfiles, persists across
                      upgrades; the root token uses homes/owner)
  vendor-, xbin-, …  reserved top-level names: vendor, data, home, homes, .xbin
```

Component keys in `.xbin` paths: `<path with / → ~, truncated>-<8-hex hash>`
(keeps unix socket paths under the 108-byte limit). The tile-deployments
stores — `data/deployments/`, `data/checkpoints/`, `.xbin/deploy/` — are
keyed by a tile key instead, 32 hex digits of a hash of the tile path, and
each records the full path it belongs to. In the `.deployments/` paths a
scope is written as its data key (`/` → `~`, a literal `~` as `%7E`); a
scope whose key is longer than 200 bytes can't have deployments beyond
`main`. Their layout is not a builder contract (docs/compat.md); an older
xbind never reads them.
